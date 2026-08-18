// Package payauth is the authority boundary for external cash events.
//
// Contract: features/tower/payment_authority.feature.
//
// # THE ONE RULE
//
// A push notification from a payment provider is a HINT. It is evidence that something may
// have happened, and nothing more: it can schedule a look, and it can never mark cash
// captured, mature, refunded, disputed, fee-final, or compensation-eligible. Authority comes
// only from Roger Core going and ASKING the provider over a purpose-scoped credential on a
// pinned endpoint, and committing what it read as an authoritative revision.
//
// The reason is the threat: a webhook is an endpoint anybody on the internet can post to. If
// a webhook could move money, forging one would be a way to mint compensation. Because it can
// only schedule an authenticated fetch of a source Core itself names, forging one buys an
// attacker a wasted API call - the fetch reads the provider's own answer, not the attacker's.
//
// # WHY THE CREDENTIALS ARE SEPARATE
//
// Webhook verification, authenticated fetch, and payout authorization use DISTINCT
// credentials. One key doing all three means a leaked ingress secret is also a key that can
// move money out. They are separated here by type, not by convention, so a caller cannot pass
// the wrong one by accident.
package payauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Purpose is what a credential is allowed to do. A credential is valid for exactly one.
type Purpose string

const (
	// PurposeWebhook verifies inbound push notifications. It authorizes NOTHING else - in
	// particular it cannot fetch, and it cannot authorize a payout.
	PurposeWebhook Purpose = "webhook"
	// PurposeFetch authenticates Core's outbound read of a payment source. This is the only
	// credential that can produce authority.
	PurposeFetch Purpose = "fetch"
	// PurposePayout authorizes moving money out. Held furthest from ingress.
	PurposePayout Purpose = "payout"
)

// Credential is one purpose-scoped, merchant-scoped, VERSIONED secret.
//
// Versioning is what makes rotation safe: the replacement is authorized before the incumbent
// retires, both verify during a bounded overlap, and the incumbent stops verifying at a time
// that was decided in advance rather than whenever the last caller happened to upgrade.
type Credential struct {
	Version  int
	Purpose  Purpose
	Merchant string
	Secret   []byte
	// NotBefore and RetiresAt bound this version. The overlap between a retiring version and
	// its replacement is the difference between the two windows, and it is deliberately finite:
	// a rotation that never closes is not a rotation.
	NotBefore time.Time
	RetiresAt time.Time
}

func (c Credential) live(now time.Time) bool {
	return !now.Before(c.NotBefore) && now.Before(c.RetiresAt)
}

// Adapter is one provider integration, named provider-neutrally so nothing downstream reads
// as "the Stripe path". Its endpoint allowlist is what stops a hint from steering Core's
// authenticated fetch at an attacker's host.
type Adapter struct {
	Name      string
	Merchant  string
	Endpoints []string
	Timeout   time.Duration
	// Scheme verifies a signature the way this provider signs. Provider-neutral by
	// indirection rather than by pretending every provider agrees.
	Scheme Scheme
}

// AllowsEndpoint reports whether Core may talk to this URL for this adapter.
func (a Adapter) AllowsEndpoint(endpoint string) bool {
	for _, e := range a.Endpoints {
		if e == endpoint {
			return true
		}
	}
	return false
}

// Scheme is how one provider signs a webhook body.
type Scheme interface {
	// Verify checks sig over the EXACT bytes the provider signed, which always includes the
	// timestamp so a captured body cannot be replayed under a fresh clock.
	Verify(secret []byte, rawBody []byte, timestamp int64, sig string) error
}

// HMACSHA256 is the common scheme: HMAC-SHA256 over "<timestamp>.<raw body>", compared in
// constant time. Stripe's shape, and enough providers' shape to be the default.
type HMACSHA256 struct{}

func (HMACSHA256) Verify(secret, rawBody []byte, timestamp int64, sig string) error {
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return errors.New("signature is not raw-url base64")
	}
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(rawBody)
	if !hmac.Equal(mac.Sum(nil), want) {
		return errors.New("signature does not match the exact signed bytes")
	}
	return nil
}

// Sign produces a signature in the HMACSHA256 shape. Test and adapter helper; Core never
// signs an inbound webhook in production.
func Sign(secret, rawBody []byte, timestamp int64) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(rawBody)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IngressPolicy bounds what ingress will even look at, before any cryptography.
type IngressPolicy struct {
	MaxBodyBytes  int
	MaxEventIDLen int
	// ReplayWindow is how old a signed timestamp may be; FutureSkew how far ahead. Both are
	// finite: an unbounded past accepts captured replays forever, an unbounded future lets a
	// clock-skewed forgery sit valid until it is convenient.
	ReplayWindow time.Duration
	FutureSkew   time.Duration
	ContentType  string
}

// DefaultIngressPolicy is the conservative shape: small bodies, five-minute replay window,
// one minute of future skew.
func DefaultIngressPolicy() IngressPolicy {
	return IngressPolicy{
		MaxBodyBytes: 64 << 10, MaxEventIDLen: 200,
		ReplayWindow: 5 * time.Minute, FutureSkew: time.Minute,
		ContentType: "application/json",
	}
}

// Delivery is one inbound webhook, exactly as it arrived.
type Delivery struct {
	RawBody           []byte
	ContentType       string
	Signature         string
	CredentialVersion int
	Timestamp         int64
	// EndpointRole is which of our endpoints received this. A payout callback arriving on the
	// payment endpoint is a misrouted or forged delivery, not a payment.
	EndpointRole Purpose
	Merchant     string
	EventID      string
	// SourceID/SourceKind name what the provider says changed. They are a HINT: Core fetches
	// this source itself rather than believing anything else in the body.
	SourceID   string
	SourceKind string
}

// Hint is a verified delivery reduced to what it is allowed to be: a request to go and look.
// It deliberately carries no amounts. There is nowhere in this type to put money.
type Hint struct {
	EventID     string
	RawBodyHash string
	Merchant    string
	SourceID    string
	SourceKind  string
	ReceivedAt  time.Time
}

// Refusals. Each is distinct because a caller (and an operator reading a log) does something
// different about each - and because a single opaque "invalid" makes an ingress bug and an
// attack indistinguishable.
var (
	ErrBodyTooLarge      = errors.New("payauth: body above the ingress limit")
	ErrContentType       = errors.New("payauth: invalid content type")
	ErrUnknownVersion    = errors.New("payauth: unknown credential version")
	ErrRetiredCredential = errors.New("payauth: retired credential outside its overlap window")
	ErrWrongPurpose      = errors.New("payauth: wrong endpoint or event purpose")
	ErrWrongMerchant     = errors.New("payauth: wrong merchant or platform account")
	ErrNoAuth            = errors.New("payauth: missing provider authentication")
	ErrBadAuth           = errors.New("payauth: invalid provider authentication")
	ErrStale             = errors.New("payauth: timestamp older than the admitted replay window")
	ErrFuture            = errors.New("payauth: timestamp too far in the future")
	ErrEventID           = errors.New("payauth: missing, malformed, or oversized event ID")
	ErrForeignSource     = errors.New("payauth: a source ID from another merchant account")
	ErrRateLimited       = errors.New("payauth: above the source or merchant rate limit")
)

// Limiter answers whether this delivery is within rate. Ingress asks BEFORE verifying, so a
// flood cannot be turned into a cryptographic workload.
type Limiter interface {
	Allow(merchant, sourceID string) bool
}

// VerifyDelivery authenticates one webhook and reduces it to a Hint, or refuses it.
//
// ORDER MATTERS and is chosen deliberately: the cheap structural checks run before the
// expensive cryptographic one, so an unauthenticated flood costs an allocation rather than an
// HMAC; and every refusal happens before anything is recorded, so a rejected delivery leaves
// no trace an attacker chose the shape of.
//
// The signature is verified over the EXACT bytes received. Canonicalizing first would be a
// vulnerability, not a tidiness: the provider signed the bytes it sent, and any normalization
// - reordering members, rewriting numbers, changing escapes - verifies a DIFFERENT document
// than the one that arrived, so a body could be altered in ways that survive our rewrite.
func VerifyDelivery(a Adapter, creds []Credential, pol IngressPolicy, d Delivery, lim Limiter, now time.Time) (Hint, error) {
	if lim != nil && !lim.Allow(d.Merchant, d.SourceID) {
		return Hint{}, ErrRateLimited
	}
	if len(d.RawBody) > pol.MaxBodyBytes {
		return Hint{}, ErrBodyTooLarge
	}
	if pol.ContentType != "" && !strings.EqualFold(mediaType(d.ContentType), pol.ContentType) {
		return Hint{}, ErrContentType
	}
	if d.EndpointRole != PurposeWebhook {
		return Hint{}, ErrWrongPurpose
	}
	if d.Merchant == "" || d.Merchant != a.Merchant {
		return Hint{}, ErrWrongMerchant
	}
	if d.EventID == "" || len(d.EventID) > pol.MaxEventIDLen || strings.ContainsAny(d.EventID, "\x00\n\r") {
		return Hint{}, ErrEventID
	}
	// A source id that names another merchant's object is either a misroute or an attempt to
	// aim our authenticated fetch at somebody else's data. Either way it is not ours to read.
	if d.SourceID == "" || !strings.HasPrefix(d.SourceID, sourcePrefix(d.Merchant)) && strings.Contains(d.SourceID, "/") {
		return Hint{}, ErrForeignSource
	}
	ts := time.Unix(d.Timestamp, 0)
	if now.Sub(ts) > pol.ReplayWindow {
		return Hint{}, ErrStale
	}
	if ts.Sub(now) > pol.FutureSkew {
		return Hint{}, ErrFuture
	}
	if d.Signature == "" {
		return Hint{}, ErrNoAuth
	}
	cred, ok := pick(creds, d.CredentialVersion)
	if !ok {
		return Hint{}, ErrUnknownVersion
	}
	if cred.Purpose != PurposeWebhook {
		return Hint{}, ErrWrongPurpose
	}
	if cred.Merchant != d.Merchant {
		return Hint{}, ErrWrongMerchant
	}
	if !cred.live(now) {
		return Hint{}, ErrRetiredCredential
	}
	scheme := a.Scheme
	if scheme == nil {
		scheme = HMACSHA256{}
	}
	if err := scheme.Verify(cred.Secret, d.RawBody, d.Timestamp, d.Signature); err != nil {
		return Hint{}, ErrBadAuth
	}
	sum := sha256.Sum256(d.RawBody)
	return Hint{
		EventID:     d.EventID,
		RawBodyHash: base64.RawURLEncoding.EncodeToString(sum[:]),
		Merchant:    d.Merchant,
		SourceID:    d.SourceID,
		SourceKind:  d.SourceKind,
		ReceivedAt:  now,
	}, nil
}

func pick(creds []Credential, version int) (Credential, bool) {
	for _, c := range creds {
		if c.Version == version {
			return c, true
		}
	}
	return Credential{}, false
}

// mediaType strips parameters ("application/json; charset=utf-8").
func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// sourcePrefix is the namespace a merchant's source ids live under when an adapter qualifies
// them. Unqualified ids (no separator) are accepted and bound to the merchant by the
// authenticated fetch, which can only read this merchant's objects anyway.
func sourcePrefix(merchant string) string { return merchant + "/" }
