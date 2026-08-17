package station

// execute.go is the Station actually doing the work: checking that Roger Core really
// authorized it, running it, and signing for exactly what came back.
//
// # THE STATION IS THE LAST PLACE ANY OF THIS CAN BE CAUGHT
//
// By the time a request arrives here it has passed through the Tower, which is the one party
// in the exchange that is not trusted and the one holding every byte. So the Station does not
// take the Tower's word for anything:
//
//	the GRANT must be signed by CORE, using a key pinned into this Station out of band. A
//	relay cannot mint one, and cannot alter one it was given.
//	the GRANT must name THIS Station. A valid grant for a different machine is somebody
//	else's authorization, and pointing it here is exactly what a relay is positioned to do.
//	the REQUEST must be the bytes the grant commits to. Otherwise a relay could pair a real
//	grant with a request of its own, and the receipt would attest to work nobody asked for.
//
// Only then does it execute. What it signs afterwards is a digest of the exact bytes it is
// returning, so a relay that changes the answer on the way back invalidates the receipt and
// Core refuses the result.
//
// # THE PINNED KEY
//
// A Station never talks to Core. It only ever talks to its Tower - which is precisely the
// party a forged grant would come from - so it cannot fetch Core's key over that channel and
// learn anything. The key is pinned by the operator from Core's public endpoint, and that
// out-of-band step is what the whole verification rests on.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
)

// ExecuteRequest is what a Tower hands a Station.
type ExecuteRequest struct {
	// Grant is Core's signed authorization, relayed verbatim.
	Grant json.RawMessage `json:"grant"`
	// Envelope is the request, SEALED to this Station's secure-session key. The Tower carries
	// it and cannot read it; the grant commits to a digest of what is inside.
	Envelope json.RawMessage `json:"envelope"`
}

// ExecuteResponse is what the Station hands back.
type ExecuteResponse struct {
	Receipt *dispatch.Receipt `json:"receipt,omitempty"`
	// Envelope is the result, SEALED to Roger Core's envelope key. The receipt commits to a
	// digest of the PLAINTEXT inside it, so Core checks after opening.
	Envelope json.RawMessage `json:"envelope,omitempty"`
	// Failure is set when the Station could not serve. It carries NO receipt: a failure is
	// not a result, and must never be capable of settling one.
	Failure string `json:"failure,omitempty"`
}

// Upstream is the local model this Station serves from. It is an interface so the executor
// can be tested against a real HTTP server without a real model, and so a Station can later
// serve something that is not an HTTP endpoint at all.
type Upstream interface {
	Serve(ctx context.Context, request []byte) ([]byte, error)
}

// Executor turns an authorized request into a signed result.
type Executor struct {
	Station *Station
	// CoreKey is Core's grant-signing public key, pinned by the operator. Without it nothing
	// can be verified and the Station refuses everything - which is the correct behaviour for
	// a Station that does not know who is allowed to give it work.
	CoreKey []byte
	// CoreEnvelopeKey is the X25519 key results are sealed to, pinned alongside CoreKey. A
	// Station cannot reach Core directly, so both come from the operator out of band.
	CoreEnvelopeKey []byte
	Network         string
	Upstream        Upstream
	Now             func() time.Time
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Execute verifies, runs, and signs. Every refusal returns a Failure rather than a receipt.
func (e Executor) Execute(ctx context.Context, in ExecuteRequest) ExecuteResponse {
	if len(e.CoreKey) == 0 {
		// FAIL CLOSED. A Station with no pinned key cannot tell a real grant from one its own
		// relay wrote, and serving anyway would make every check below theatre.
		return ExecuteResponse{Failure: "this Station has no pinned Roger Core key, so it cannot verify a grant"}
	}
	if len(e.CoreEnvelopeKey) == 0 {
		// Without it a result could only be returned in the clear, past the relay this whole
		// mechanism exists to keep content away from.
		return ExecuteResponse{Failure: "this Station has no pinned Roger Core envelope key, " +
			"so it cannot return a result the relay cannot read"}
	}
	// OPENED HERE AND NOWHERE ELSE. The attempt id is the additional data, so an envelope for
	// another attempt will not open even though the relay may hold both.
	attemptID := attemptOf(in.Grant)
	sealed, err := envelope.Parse(in.Envelope)
	if err != nil {
		return ExecuteResponse{Failure: err.Error()}
	}
	request, err := envelope.OpenWith(e.Station.SessionPriv(), sealed, attemptID)
	if err != nil {
		return ExecuteResponse{Failure: err.Error()}
	}
	// The grant is checked against the PLAINTEXT, which is what it committed to. Checking the
	// ciphertext would bind the envelope rather than the request, and a relay could then
	// re-seal the same bytes under a different attempt.
	grant, err := dispatch.ParseGrant(in.Grant, e.CoreKey, e.Network,
		e.Station.StationID, request, e.now())
	if err != nil {
		return ExecuteResponse{Failure: err.Error()}
	}
	if e.Upstream == nil {
		return ExecuteResponse{Failure: "this Station has no upstream model configured"}
	}

	body, err := e.Upstream.Serve(ctx, request)
	if err != nil {
		// The upstream's own words, not a reinterpretation of them: an operator debugging a
		// Station needs what the model actually said.
		return ExecuteResponse{Failure: "the model did not answer: " + err.Error()}
	}
	// SIGNED OVER WHAT IS BEING RETURNED, and produced from the same bytes that go on the
	// wire. Signing anything else - a re-encoding, a copy made earlier - would leave a gap
	// between what was attested and what was sent.
	rec, err := dispatch.SignReceipt(e.Station.assertionPriv, e.Network, grant, request, body,
		dispatch.Usage{In: int64(len(request)), Out: int64(len(body))}, tokenUsageOf(body))
	if err != nil {
		return ExecuteResponse{Failure: "this Station could not sign its result: " + err.Error()}
	}
	// Sealed to CORE, so the answer crosses the relay unreadable too. The receipt commits to
	// the plaintext digest, so Core verifies what it opens rather than what it received.
	out, err := envelope.SealTo(e.CoreEnvelopeKey, body, grant.AttemptID)
	if err != nil {
		return ExecuteResponse{Failure: "this Station could not seal its result: " + err.Error()}
	}
	raw, err := out.Marshal()
	if err != nil {
		return ExecuteResponse{Failure: err.Error()}
	}
	return ExecuteResponse{Receipt: &rec, Envelope: raw}
}

// attemptOf reads the attempt id out of a grant WITHOUT verifying it.
//
// Only used as the envelope's additional data, and a wrong value simply means the envelope
// does not open - which is a refusal, not a bypass. The grant's real verification happens
// immediately afterwards against the plaintext it protects.
func attemptOf(grant []byte) string {
	var obj struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal(grant, &obj); err != nil {
		return ""
	}
	return obj.AttemptID
}

// HTTPUpstream serves from an OpenAI-compatible endpoint - the shape every local runner
// already speaks, so a Station is pointed at what the operator is running rather than
// requiring anything new of them.
type HTTPUpstream struct {
	URL     string
	Client  *http.Client
	Timeout time.Duration
}

func (u HTTPUpstream) Serve(ctx context.Context, request []byte) ([]byte, error) {
	timeout := u.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.URL, bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := u.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Bounded: an upstream that streams forever must not be able to exhaust this Station's
	// memory on one request.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("the model replied %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if len(body) == 0 {
		return nil, errors.New("the model returned an empty body")
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// tokenUsageOf parses the model's own token counts from an OpenAI-compatible response body
// (its "usage" object), for the Option C per-token receipt. A missing usage object, a non-JSON
// body, or negative counts yield zero - the per-token settle path then bills nothing for this
// request and the byte cap + audit govern, exactly as an un-tokened receipt. The node signs
// whatever this returns; Core clamps it to the grant's token ceiling and the Tower's
// byte-attestation, so an inflated figure here cannot exceed what was authorized.
func tokenUsageOf(body []byte) dispatch.Usage {
	var parsed struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &parsed)
	in, out := parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	if in < 0 {
		in = 0
	}
	if out < 0 {
		out = 0
	}
	return dispatch.Usage{In: in, Out: out}
}
