package edgeclient

// sealed.go is the client side of Option C, Topology 2 - the TOWER-HOSTED data plane:
//
//	authorize -> seal -> submit -> open -> ack
//
// The consumer authorizes at Roger Core (which pins the node's OWN listed per-token price
// into the grant and hands back the Station's session key), seals the request TO THE NODE,
// submits the ciphertext to the tower's hub, opens the answer sealed back to it, and then
// acknowledges to Core - the one account of the attempt that does not come from a party
// being paid, which upgrades the settlement from funded to corroborated.
//
// The tower carries ciphertext both ways and the broker carries none of it. Compare Do
// (edgeclient.go), the TLS-splice relay path this supersedes for tower traffic.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/envelope"
	"rogerai.fm/roger/v5/internal/towerhub"
)

// SealedAuthorization is Core's Topology-2 answer: the grant, where to submit, the Station's
// sealing key - and, held privately, the one key that can open the answer.
type SealedAuthorization struct {
	AttemptID string
	Grant     []byte
	// Endpoint is the tower hub's address, always bare host:port. The comment here used to
	// promise that "an endpoint carrying its own scheme is honored verbatim", which was the same
	// false promise the node's side of this carried: no such endpoint can exist, because both
	// places one enters the system validate it with net.SplitHostPort.
	Endpoint string
	// EndpointTLSSPKI is the hub certificate pin Core published for that endpoint: hex sha256
	// over the SubjectPublicKeyInfo of the certificate the tower's hub presents. Non-empty means
	// this client submits over https and accepts THAT CERTIFICATE AND NO OTHER - not a chain to
	// a public root, not a matching hostname, which are the wrong questions for a volunteer's
	// box on a dynamic address. Empty means the tower serves plaintext, which is what every
	// tower did before this field existed.
	//
	// It comes from Core rather than from the tower, and that is the whole mechanism: Core
	// already tells this client where to connect, which key to seal to and which grant key to
	// trust, so the certificate to expect is one more fact from a party it cannot function
	// without. See internal/towerhub/pin.go.
	EndpointTLSSPKI string
	// StationSessionKey is the node's X25519 public key - what the request is sealed to.
	// Core hands it over, so the tower never chooses an encryption key.
	StationSessionKey []byte
	// The grant's pinned economics, echoed for display: the node's own listed price.
	PriceInMicros, PriceOutMicros int64
	MaxTokIn, MaxTokOut           int64
	MaxHoldCredits                float64
	// envPriv opens the sealed answer. Unexported: the evidence and privacy flow stay
	// inside the client where they cannot be half-done.
	envPriv []byte
}

// AuthorizeSealed asks Core for a Topology-2 grant, minting a fresh X25519 envelope keypair
// for the answer. The public half rides into the Core-signed grant, so the serving node
// seals the result to a key Core attested - not one the tower could substitute.
func (c *Client) AuthorizeSealed(ctx context.Context, model string) (SealedAuthorization, error) {
	if c.Key == nil {
		return SealedAuthorization{}, errors.New("an edge consumer needs a signing key: " +
			"the grant is issued to an account, and the acknowledgement must come from the same one")
	}
	envPub, envPriv, err := envelope.NewKey()
	if err != nil {
		return SealedAuthorization{}, err
	}
	body, err := json.Marshal(map[string]any{
		"model":            model,
		"consumer_env_key": hex.EncodeToString(envPub),
	})
	if err != nil {
		return SealedAuthorization{}, err
	}
	var out struct {
		AttemptID         string  `json:"attempt_id"`
		Grant             string  `json:"grant"`
		Endpoint          string  `json:"endpoint"`
		EndpointTLSSPKI   string  `json:"endpoint_tls_spki"`
		StationSessionKey string  `json:"station_session_key"`
		PriceInMicros     int64   `json:"price_in_micros"`
		PriceOutMicros    int64   `json:"price_out_micros"`
		MaxTokIn          int64   `json:"max_tok_in"`
		MaxTokOut         int64   `json:"max_tok_out"`
		MaxHoldCredits    float64 `json:"max_hold_credits"`
	}
	if err := c.signedPost(ctx, "/tower/edge/authorize", body, &out); err != nil {
		return SealedAuthorization{}, err
	}
	grant, err := base64.StdEncoding.DecodeString(out.Grant)
	if err != nil || len(grant) == 0 {
		return SealedAuthorization{}, errors.New("Roger Core's authorization carries no readable grant")
	}
	sessionKey, err := hex.DecodeString(out.StationSessionKey)
	if err != nil || len(sessionKey) != 32 {
		return SealedAuthorization{}, errors.New("Roger Core's authorization carries no Station session key - " +
			"is this model served through a tower hub?")
	}
	if out.Endpoint == "" || out.AttemptID == "" {
		return SealedAuthorization{}, errors.New("Roger Core's authorization is missing an endpoint or attempt id")
	}
	return SealedAuthorization{
		AttemptID: out.AttemptID, Grant: grant, Endpoint: out.Endpoint,
		EndpointTLSSPKI:   out.EndpointTLSSPKI,
		StationSessionKey: sessionKey,
		PriceInMicros:     out.PriceInMicros, PriceOutMicros: out.PriceOutMicros,
		MaxTokIn: out.MaxTokIn, MaxTokOut: out.MaxTokOut, MaxHoldCredits: out.MaxHoldCredits,
		envPriv: envPriv,
	}, nil
}

// DoSealed sends one request through the tower's hub: seal to the Station, submit the
// ciphertext, open the answer. The returned Result carries the opened plaintext and the
// node's receipt, ready for Ack - the same acknowledgement flow as the TLS path.
//
// The submit can legitimately be HELD for the hub's full submit TTL (90s by default) while
// the node serves, so ctx - not a client timeout - is the deadline: give it at least a
// couple of minutes for a slow model. On success the authorization's opening key is zeroed;
// the attempt is one-use end to end, and a spent key should not linger in memory.
func (c *Client) DoSealed(ctx context.Context, auth *SealedAuthorization, body []byte) (Result, error) {
	if auth == nil || len(auth.envPriv) == 0 {
		return Result{}, errors.New("this authorization cannot open an answer - it did not come from AuthorizeSealed (or was already used)")
	}
	sealed, err := envelope.SealTo(auth.StationSessionKey, body, auth.AttemptID)
	if err != nil {
		return Result{}, fmt.Errorf("could not seal the request to the Station: %w", err)
	}
	sealedRaw, err := sealed.Marshal()
	if err != nil {
		return Result{}, err
	}
	// A dedicated DATA-PLANE client (audit H-A): the control-plane client's 30s timeout would
	// abort a submit the hub is legitimately holding while the node serves - and an aborted
	// wait is not an unserved attempt: the node may still complete, the receipt still settles,
	// and re-submitting the same attempt is forbidden. No fixed timeout (ctx bounds the wait),
	// and no redirects at all: a hub has no business redirecting a submit.
	//
	// THE SCHEME AND THE CERTIFICATE CHECK COME FROM towerhub.Reach, which is the one place in
	// the tree that turns Core's (endpoint, pin) into a dialable client. This used to be a local
	// hubBase() with its own copy of the rule, and the node's leg had a third; three copies of
	// "how do I reach a hub" is how half the traffic to a TLS tower stays plaintext.
	base, httpc, err := towerhub.Reach(auth.Endpoint, auth.EndpointTLSSPKI, &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the tower hub does not redirect")
		},
	})
	if err != nil {
		return Result{}, err
	}
	hc := &towerhub.Client{BaseURL: base, HTTP: httpc}
	res, err := hc.SubmitJob(ctx, auth.Grant, sealedRaw)
	if err != nil {
		return Result{}, err
	}
	firstByte := time.Now()
	if res.Failure != "" {
		// The node refused or failed upstream. No receipt was produced (the serve loop zeroes
		// it on failure) and there is nothing to open or acknowledge.
		return Result{Status: http.StatusBadGateway, Body: []byte(res.Failure)}, nil
	}
	parsed, err := envelope.Parse(res.Envelope)
	if err != nil {
		return Result{}, fmt.Errorf("the answer is not a sealed envelope: %w", err)
	}
	plain, err := envelope.OpenWith(auth.envPriv, parsed, auth.AttemptID)
	if err != nil {
		return Result{}, fmt.Errorf("could not open the sealed answer (wrong key or tampered in transit): %w", err)
	}
	// Spent: this attempt is one-use end to end, and the opening key has no further purpose.
	for i := range auth.envPriv {
		auth.envPriv[i] = 0
	}
	auth.envPriv = nil
	return Result{
		Status: http.StatusOK, Body: plain,
		receipt:   base64.StdEncoding.EncodeToString(res.Receipt),
		firstByte: firstByte, completed: time.Now(),
	}, nil
}

// AckSealed acknowledges a DoSealed result to Core. Identical alignment to Ack: an honest
// acknowledgement can only ever reduce what the consumer is billed, and it is what turns a
// settled attempt into a corroborated one.
func (c *Client) AckSealed(ctx context.Context, auth *SealedAuthorization, res Result) error {
	if auth == nil {
		return errors.New("no authorization to acknowledge against")
	}
	return c.ack(ctx, auth.AttemptID, res)
}
