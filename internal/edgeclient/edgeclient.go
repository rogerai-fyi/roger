// Package edgeclient is a first-party consumer of the Tower edge path: authorize with Roger
// Core, speak to the Station through a Tower that cannot listen, and acknowledge what came
// back.
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
//
// # WHAT THIS CLIENT KNOWS THAT AN ORDINARY ONE DOES NOT
//
// Three things only: where to ask for a grant, which header carries it, and which header
// brings the receipt back. The request body and the response body are untouched - what
// travels is exactly what an ordinary client would have sent.
package edgeclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// GrantHeader and ReceiptHeader mirror the Station's - redeclared rather than imported so a
// consumer binary does not link the Station's serving code in.
const (
	GrantHeader   = "X-Rogerai-Grant"
	ReceiptHeader = "X-Rogerai-Receipt"
)

// Client speaks the edge path for one consumer identity.
type Client struct {
	// Broker is Roger Core's base URL.
	Broker string
	// Key signs the authorize request and the acknowledgement. One key for both, so the
	// account that asked for the work is the account whose ack corroborates it.
	Key ed25519.PrivateKey
	// Roots verifies the Station's certificate. Nil means the system pool, which is correct
	// in production - Core provisions publicly-trusted certificates precisely so unmodified
	// clients work.
	Roots *x509.CertPool
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
	return &http.Client{Timeout: 30 * time.Second}
}

// Authorization is Core's answer: permission, and a place to use it.
type Authorization struct {
	AttemptID string `json:"attempt_id"`
	Grant     string `json:"grant"`
	RelayName string `json:"relay_name"`
	Endpoint  string `json:"endpoint"`
	Deadline  int64  `json:"deadline"`
	MaxIn     int64  `json:"max_in"`
	MaxOut    int64  `json:"max_out"`
}

// Authorize asks Core for an edge grant.
func (c *Client) Authorize(ctx context.Context, model string, maxIn, maxOut int64) (Authorization, error) {
	if c.Key == nil {
		return Authorization{}, errors.New("an edge consumer needs a signing key: " +
			"the grant is issued to an account, and the acknowledgement must come from the same one")
	}
	body, err := json.Marshal(map[string]any{"model": model, "max_in": maxIn, "max_out": maxOut})
	if err != nil {
		return Authorization{}, err
	}
	var out Authorization
	if err := c.signedPost(ctx, "/tower/edge/authorize", body, &out); err != nil {
		return Authorization{}, err
	}
	if out.Grant == "" || out.Endpoint == "" || out.RelayName == "" {
		return Authorization{}, errors.New("Roger Core's authorization is missing a grant, an endpoint, or a relay name")
	}
	return out, nil
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

// Do sends one request through the Tower to the Station.
//
// The connection dials the ENDPOINT - the Tower's data plane - while TLS verifies the RELAY
// NAME, which only the Station holds a certificate for. That split is the entire trick: the
// Tower is reached, the Station is authenticated, and nothing in between can read a byte.
func (c *Client) Do(ctx context.Context, auth Authorization, path string, body []byte) (Result, error) {
	if int64(len(body)) > auth.MaxIn {
		// Refused HERE, before spending a connection: the Station will refuse it anyway,
		// because the grant's ceiling is checked against the bytes, not taken on trust.
		return Result{}, fmt.Errorf("this request is %d bytes and the grant allows %d", len(body), auth.MaxIn)
	}
	transport := &http.Transport{
		DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(dctx, "tcp", auth.Endpoint)
		},
		TLSClientConfig: &tls.Config{RootCAs: c.Roots, ServerName: auth.RelayName, MinVersion: tls.VersionTLS12},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Minute}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+auth.RelayName+path, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(GrantHeader, auth.Grant)

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	firstByte := time.Now()
	// Bounded by the grant's own ceiling plus one byte, so an over-long answer is detected
	// rather than truncated into a digest that can never match anybody's.
	got, err := io.ReadAll(io.LimitReader(resp.Body, auth.MaxOut+1))
	if err != nil {
		return Result{}, err
	}
	return Result{
		Status: resp.StatusCode, Body: got,
		receipt:   resp.Header.Get(ReceiptHeader),
		firstByte: firstByte, completed: time.Now(),
	}, nil
}

// Receipt is the base64 Station receipt that came back in the response header, for a caller
// that verifies it itself - a canary checks the receipt is a valid Station signature over the
// bytes, which is how it tells a Tower that served from one that returned nothing.
func (r Result) Receipt() string { return r.receipt }

// Ack tells Core what was actually received. Best effort by design: a consumer that cannot
// reach Core has still been served, and the attempt settles uncorroborated without it.
func (c *Client) Ack(ctx context.Context, auth Authorization, res Result) error {
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
	a, err := dispatch.SignAck(c.Key, c.network(), auth.AttemptID, res.Body,
		dispatch.Usage{In: 0, Out: int64(len(res.Body))}, res.firstByte, res.completed)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"attempt_id": auth.AttemptID,
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
