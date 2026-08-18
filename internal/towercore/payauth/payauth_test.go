package payauth

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func world() (Adapter, []Credential, IngressPolicy, time.Time) {
	now := time.Unix(1_700_000_000, 0)
	a := Adapter{Name: "provider-x", Merchant: "acct_1", Endpoints: []string{"https://api.provider.test/v1"},
		Timeout: 10 * time.Second, Scheme: HMACSHA256{}}
	creds := []Credential{{
		Version: 7, Purpose: PurposeWebhook, Merchant: "acct_1", Secret: []byte("s3cret"),
		NotBefore: now.Add(-time.Hour), RetiresAt: now.Add(time.Hour),
	}}
	return a, creds, DefaultIngressPolicy(), now
}

func good(now time.Time, secret []byte) Delivery {
	body := []byte(`{"id":"evt_1","object":"event"}`)
	return Delivery{
		RawBody: body, ContentType: "application/json; charset=utf-8",
		Signature: Sign(secret, body, now.Unix()), CredentialVersion: 7,
		Timestamp: now.Unix(), EndpointRole: PurposeWebhook, Merchant: "acct_1",
		EventID: "evt_1", SourceID: "pi_123", SourceKind: "payment_intent",
	}
}

// A valid webhook is reduced to a HINT and nothing else. The type itself is the guarantee:
// there is nowhere in it to put an amount.
func TestAValidWebhookYieldsOnlyAHint(t *testing.T) {
	a, creds, pol, now := world()
	h, err := VerifyDelivery(a, creds, pol, good(now, creds[0].Secret), nil, now)
	if err != nil {
		t.Fatalf("valid delivery refused: %v", err)
	}
	if h.EventID != "evt_1" || h.SourceID != "pi_123" || h.RawBodyHash == "" {
		t.Fatalf("hint did not carry what a fetch needs: %+v", h)
	}
}

// THE DEFECT TABLE, exhaustively, from the spec's Scenario Outline. Each row must refuse -
// and refuse for its OWN reason, because one opaque "invalid" makes an ingress bug and an
// attack indistinguishable in a log.
func TestAnInvalidWebhookHasNoAuthority(t *testing.T) {
	for _, tc := range []struct {
		defect string
		want   error
		bend   func(*Delivery, time.Time, []byte)
	}{
		{"missing provider authentication", ErrNoAuth, func(d *Delivery, _ time.Time, _ []byte) { d.Signature = "" }},
		{"invalid provider authentication", ErrBadAuth, func(d *Delivery, _ time.Time, _ []byte) {
			d.Signature = base64.RawURLEncoding.EncodeToString([]byte("not the mac"))
		}},
		{"signature over normalized rather than exact raw bytes", ErrBadAuth, func(d *Delivery, now time.Time, sec []byte) {
			// The attacker signs a REORDERED but semantically identical body, then sends the
			// original. Anything that canonicalized before verifying would accept this.
			normalized := []byte(`{"object":"event","id":"evt_1"}`)
			d.Signature = Sign(sec, normalized, now.Unix())
		}},
		{"wrong endpoint or event purpose", ErrWrongPurpose, func(d *Delivery, _ time.Time, _ []byte) {
			d.EndpointRole = PurposePayout
		}},
		{"timestamp older than the admitted replay window", ErrStale, func(d *Delivery, now time.Time, sec []byte) {
			old := now.Add(-10 * time.Minute).Unix()
			d.Timestamp, d.Signature = old, Sign(sec, d.RawBody, old)
		}},
		{"timestamp too far in the future", ErrFuture, func(d *Delivery, now time.Time, sec []byte) {
			fut := now.Add(10 * time.Minute).Unix()
			d.Timestamp, d.Signature = fut, Sign(sec, d.RawBody, fut)
		}},
		{"unknown credential version", ErrUnknownVersion, func(d *Delivery, _ time.Time, _ []byte) {
			d.CredentialVersion = 99
		}},
		{"wrong merchant or platform account", ErrWrongMerchant, func(d *Delivery, _ time.Time, _ []byte) {
			d.Merchant = "acct_someone_else"
		}},
		{"missing event ID", ErrEventID, func(d *Delivery, _ time.Time, _ []byte) { d.EventID = "" }},
		{"oversized event ID", ErrEventID, func(d *Delivery, _ time.Time, _ []byte) {
			d.EventID = string(make([]byte, 500))
		}},
		{"a source ID from another merchant account", ErrForeignSource, func(d *Delivery, _ time.Time, _ []byte) {
			d.SourceID = "acct_other/pi_123"
		}},
		{"body above the ingress limit", ErrBodyTooLarge, func(d *Delivery, _ time.Time, _ []byte) {
			d.RawBody = make([]byte, 1<<20)
		}},
		{"invalid content type", ErrContentType, func(d *Delivery, _ time.Time, _ []byte) {
			d.ContentType = "text/plain"
		}},
	} {
		t.Run(tc.defect, func(t *testing.T) {
			a, creds, pol, now := world()
			d := good(now, creds[0].Secret)
			tc.bend(&d, now, creds[0].Secret)
			_, err := VerifyDelivery(a, creds, pol, d, nil, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.defect, err, tc.want)
			}
		})
	}
}

type denyAll struct{}

func (denyAll) Allow(string, string) bool { return false }

// Rate limiting is asked BEFORE any cryptography, so a flood costs an allocation and not an
// HMAC per request.
func TestARateLimitedDeliveryIsRefusedBeforeVerifying(t *testing.T) {
	a, creds, pol, now := world()
	_, err := VerifyDelivery(a, creds, pol, good(now, creds[0].Secret), denyAll{}, now)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want rate limited", err)
	}
}

// Rotation: both versions verify during the bounded overlap, the incumbent stops at a time
// decided in advance, and a version outside its own window has no authority whatever its
// signature says.
func TestRotationHasOneBoundedOverlap(t *testing.T) {
	a, _, pol, now := world()
	old := Credential{Version: 7, Purpose: PurposeWebhook, Merchant: "acct_1", Secret: []byte("old"),
		NotBefore: now.Add(-2 * time.Hour), RetiresAt: now.Add(30 * time.Minute)}
	next := Credential{Version: 8, Purpose: PurposeWebhook, Merchant: "acct_1", Secret: []byte("new"),
		NotBefore: now.Add(-30 * time.Minute), RetiresAt: now.Add(2 * time.Hour)}
	creds := []Credential{old, next}

	// Inside the overlap: both are honoured.
	for _, c := range creds {
		d := good(now, c.Secret)
		d.CredentialVersion = c.Version
		if _, err := VerifyDelivery(a, creds, pol, d, nil, now); err != nil {
			t.Fatalf("version %d inside the overlap was refused: %v", c.Version, err)
		}
	}

	// After the close, the retired version is refused even though its signature is perfect.
	after := now.Add(time.Hour)
	d := good(after, old.Secret)
	d.CredentialVersion = old.Version
	if _, err := VerifyDelivery(a, creds, pol, d, nil, after); !errors.Is(err, ErrRetiredCredential) {
		t.Fatalf("retired version after close: got %v, want retired", err)
	}
	dn := good(after, next.Secret)
	dn.CredentialVersion = next.Version
	if _, err := VerifyDelivery(a, creds, pol, dn, nil, after); err != nil {
		t.Fatalf("replacement should still verify after the close: %v", err)
	}
}

// A webhook credential cannot fetch and cannot pay out: purpose is checked, not assumed.
func TestACredentialIsBoundToOnePurpose(t *testing.T) {
	a, _, pol, now := world()
	creds := []Credential{{Version: 7, Purpose: PurposeFetch, Merchant: "acct_1", Secret: []byte("s3cret"),
		NotBefore: now.Add(-time.Hour), RetiresAt: now.Add(time.Hour)}}
	if _, err := VerifyDelivery(a, creds, pol, good(now, creds[0].Secret), nil, now); !errors.Is(err, ErrWrongPurpose) {
		t.Fatalf("a fetch credential verified a webhook: %v", err)
	}
}

// The endpoint allowlist is what stops a hint steering Core's authenticated fetch elsewhere.
func TestOnlyAllowlistedEndpointsAreReachable(t *testing.T) {
	a, _, _, _ := world()
	if !a.AllowsEndpoint("https://api.provider.test/v1") {
		t.Fatal("the configured endpoint must be allowed")
	}
	if a.AllowsEndpoint("https://api.provider.test.evil/v1") {
		t.Fatal("a lookalike host must not be allowed")
	}
}
