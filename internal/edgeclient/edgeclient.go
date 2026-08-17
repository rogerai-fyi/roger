// Package edgeclient is the first-party consumer of the Tower edge path: authorize with
// Roger Core, submit SEALED work through a Tower's hub (sealed.go - the only data plane this
// client speaks since the TLS-splice generation was retired), and acknowledge what came back.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY A CLIENT PACKAGE AT ALL
//
// The edge path works for any OpenAI-compatible client - that is its whole compatibility
// claim - but a plain client leaves one thing on the table: the ACKNOWLEDGEMENT. That is the
// only account of an attempt that does not come from the party being paid, and it is what
// turns "settled" into "corroborated". A first-party client sends it; everybody else's
// attempts settle uncorroborated, which is funded and fine, and strictly worse evidence.
//
// An honest acknowledgement can only ever REDUCE what the consumer is billed - settlement
// takes the lower of the two claims - so sending one is in the consumer's own interest.
// That alignment is not an accident; it is what makes the evidence design work without
// anybody being ordered to participate.
package edgeclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// Client speaks the edge path for one consumer identity.
type Client struct {
	// Broker is Roger Core's base URL.
	Broker string
	// Key signs the authorize request and the acknowledgement. One key for both, so the
	// account that asked for the work is the account whose ack corroborates it.
	Key ed25519.PrivateKey
	// HTTP is the control-plane client. Nil means a bounded default.
	HTTP *http.Client
	// Network names the public network for signed objects. Empty means roger-public.
	Network string
}

func (c *Client) network() string {
	if c.Network == "" {
		return "roger-public"
	}
	return c.Network
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// The control plane delivers key-trust material (the station session key), so the guard
	// re-applies on every redirect hop - an https broker front cannot 30x this client onto
	// plaintext or another host after the initial TrustedBase check passed.
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}
}

// Result is what came back, with the evidence needed to acknowledge it.
type Result struct {
	Status int
	Body   []byte
	// receipt is kept for Ack, unexported: the caller's business is the body, and the
	// evidence flow stays inside the client where it cannot be half-done.
	receipt string
	// timings for the acknowledgement.
	firstByte time.Time
	completed time.Time
}

// Receipt is the base64 Station receipt that came back in the response header, for a caller
// that verifies it itself - a canary checks the receipt is a valid Station signature over the
// bytes, which is how it tells a Tower that served from one that returned nothing.
func (r Result) Receipt() string { return r.receipt }

// ack tells Core what was actually received. Best effort by design: a consumer that cannot
// reach Core has still been served, and the attempt settles uncorroborated without it.
func (c *Client) ack(ctx context.Context, attemptID string, res Result) error {
	if res.Status != http.StatusOK || len(res.Body) == 0 || res.receipt == "" {
		// There is nothing to corroborate. A refusal produced no receipt, and acknowledging
		// an error body would be signing a claim about an answer that was not one.
		return nil
	}
	// In is 0 and that is not a false claim: the acknowledgement commits only to the RESPONSE
	// digest, so it cannot attest the request, and Core does not reconcile input against it -
	// input billing rests on the Station's receipt, bounded by the grant ceiling and checked at
	// audit. Out is the one figure the consumer genuinely witnesses (it holds the bytes), so it
	// is signed truthfully and is what corroborates - or, if the Station lied, disputes - output.
	a, err := dispatch.SignAck(c.Key, c.network(), attemptID, res.Body,
		dispatch.Usage{In: 0, Out: int64(len(res.Body))}, res.firstByte, res.completed)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"attempt_id": attemptID,
		"ack":        base64.StdEncoding.EncodeToString(a.Signed),
	})
	if err != nil {
		return err
	}
	return c.signedPost(ctx, "/tower/edge/ack", body, nil)
}

// signedPost is the control-plane call, signed with the consumer's key.
func (c *Client) signedPost(ctx context.Context, path string, body []byte, out any) error {
	// Authorize hands back the STATION SESSION KEY - what the consumer seals its plaintext
	// to - so the transport delivering it must be trusted (audit M-3): over plaintext http a
	// MITM could substitute its own key and read every prompt.
	if err := protocol.TrustedBase(c.Broker); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.Broker, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(c.Key, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
			return fmt.Errorf("roger core answered %d: %s", resp.StatusCode, env.Error.Message)
		}
		return fmt.Errorf("roger core answered %d", resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("could not read roger core's reply: %w", err)
		}
	}
	return nil
}
