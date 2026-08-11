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
	// Request is the body to serve, which the grant commits to by digest.
	Request json.RawMessage `json:"request"`
}

// ExecuteResponse is what the Station hands back.
type ExecuteResponse struct {
	Receipt *dispatch.Receipt `json:"receipt,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
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
	CoreKey  []byte
	Network  string
	Upstream Upstream
	Now      func() time.Time
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
	grant, err := dispatch.ParseGrant(in.Grant, e.CoreKey, e.Network,
		e.Station.StationID, in.Request, e.now())
	if err != nil {
		return ExecuteResponse{Failure: err.Error()}
	}
	if e.Upstream == nil {
		return ExecuteResponse{Failure: "this Station has no upstream model configured"}
	}

	body, err := e.Upstream.Serve(ctx, in.Request)
	if err != nil {
		// The upstream's own words, not a reinterpretation of them: an operator debugging a
		// Station needs what the model actually said.
		return ExecuteResponse{Failure: "the model did not answer: " + err.Error()}
	}
	// SIGNED OVER WHAT IS BEING RETURNED, and produced from the same bytes that go on the
	// wire. Signing anything else - a re-encoding, a copy made earlier - would leave a gap
	// between what was attested and what was sent.
	rec, err := dispatch.SignReceipt(e.Station.assertionPriv, e.Network, grant, body)
	if err != nil {
		return ExecuteResponse{Failure: "this Station could not sign its result: " + err.Error()}
	}
	return ExecuteResponse{Receipt: &rec, Body: body}
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
