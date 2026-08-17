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
