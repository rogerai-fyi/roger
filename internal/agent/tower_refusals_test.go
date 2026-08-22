package agent

// tower_refusals_test.go covers AttachTower's refusals, every one of which measured zero.
// The proof test beside this file drives the happy path; what had never run is each way the
// join must FAIL - and one of them is a security posture, not an error message: a Core that
// answers without the relay's identity fingerprint must strand the node rather than let it
// sign for an unverified hub.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func attachKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func attachBroker(t *testing.T, answer func(w http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer(w)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A private band NEVER attaches - structural, at the network act, before a single byte
// leaves the machine. The error is the sentinel a caller branches on.
func TestAPrivateShareIsRefusedBeforeAnyNetworkCall(t *testing.T) {
	reached := false
	broker := attachBroker(t, func(w http.ResponseWriter) { reached = true })
	_, _, err := AttachTower(Config{Broker: broker, NodeID: "n", Model: "m", Private: true},
		attachKey(t), t.TempDir())
	if err != ErrPrivateShareNeverRelays {
		t.Fatalf("err = %v, want ErrPrivateShareNeverRelays", err)
	}
	if reached {
		t.Fatal("a private share reached the broker: the guarantee is supposed to be structural")
	}
}

// Plaintext http to a non-loopback broker is refused: attach ships this node's keys up and
// the endpoint it will sign for back, and an untrusted transport for that is a key-swap.
func TestAttachRefusesAnUntrustedBroker(t *testing.T) {
	_, _, err := AttachTower(Config{Broker: "http://broker.example.com", NodeID: "n", Model: "m"},
		attachKey(t), t.TempDir())
	if err == nil {
		t.Fatal("an untrusted broker base was accepted")
	}
}

func TestAttachSurfacesARefusalWithItsStatus(t *testing.T) {
	broker := attachBroker(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("this account is suspended"))
	})
	_, _, err := AttachTower(Config{Broker: broker, NodeID: "n", Model: "m"}, attachKey(t), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "attach refused (403)") ||
		!strings.Contains(err.Error(), "suspended") {
		t.Fatalf("err = %v, want the status and Core's reason", err)
	}
}

func TestAttachRefusesAnUnreadableAnswer(t *testing.T) {
	broker := attachBroker(t, func(w http.ResponseWriter) { _, _ = w.Write([]byte("{nope")) })
	_, _, err := AttachTower(Config{Broker: broker, NodeID: "n", Model: "m"}, attachKey(t), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unreadable attach response") {
		t.Fatalf("err = %v", err)
	}
}

func TestAttachNeedsAnEndpoint(t *testing.T) {
	broker := attachBroker(t, func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tower_id": "tw-1", "tower_key_hash": "00"})
	})
	_, _, err := AttachTower(Config{Broker: broker, NodeID: "n", Model: "m"}, attachKey(t), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "without an endpoint") {
		t.Fatalf("err = %v", err)
	}
}

// THE SECURITY REFUSAL. The epoch this node signs over arrives on an unauthenticated 401,
// and the fingerprint is the only thing that proves which hub process named it. A Core too
// old to send one must strand the node - "carry on without it" means emitting signatures
// over an attacker's choosing, and a downgrade an attacker can provoke is not a security
// property.
func TestAttachWithoutTheRelayFingerprintStrandsTheNode(t *testing.T) {
	broker := attachBroker(t, func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tower_id": "tw-1", "endpoint": "203.0.113.9:8443", "state": "active"})
	})
	_, _, err := AttachTower(Config{Broker: broker, NodeID: "n", Model: "m"}, attachKey(t), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "tower_key_hash") ||
		!strings.Contains(err.Error(), "Roger Core must be updated") {
		t.Fatalf("err = %v, want the fingerprint refusal naming the deployment order", err)
	}
}

// The backoff's spread is the point: a tower restart strands its whole fleet in the same
// instant, and an un-jittered backoff would march them all back through the door in step.
func TestReattachDelaySpreadsAndCaps(t *testing.T) {
	if d := reattachDelay(-3); d < reattachBackoffBase/2 || d > reattachBackoffBase {
		t.Fatalf("negative streak = %v, want within [base/2, base]", d)
	}
	for i := 0; i < 50; i++ {
		d := reattachDelay(1000) // far past the cap: the doubling must stop, jitter must stay
		if d <= reattachBackoffCap/2 || d > reattachBackoffCap {
			t.Fatalf("capped delay = %v, want within (cap/2, cap]", d)
		}
	}
}

func TestPriceMicrosConversion(t *testing.T) {
	if got := microsPerDollarPer1M(0); got != 0 {
		t.Fatalf("free = %d, want 0", got)
	}
	if got := microsPerDollarPer1M(-1); got != 0 {
		t.Fatalf("negative = %d, want 0: a negative price must not become a negative bill", got)
	}
	if got := microsPerDollarPer1M(3.5); got != 3_500_000 {
		t.Fatalf("3.50/1M = %d, want 3500000", got)
	}
}

var _ = time.Second // keep the import if the jitter assertions change shape

// fetchCoreKeys pins BOTH of Core's keys - the grant key every consumer grant is verified
// against and the envelope key audit transcripts are sealed to. Every refusal here is a
// key-trust decision: a wrong-length key that parsed as "fine" would verify nothing and
// seal to nobody.
func TestFetchCoreKeysRefusesEverythingButTwoGoodKeys(t *testing.T) {
	if _, _, err := fetchCoreKeys("http://broker.example.com"); err == nil {
		t.Fatal("an untrusted base was accepted for a key fetch")
	}

	answer := func(body string, code int) string {
		return attachBroker(t, func(w http.ResponseWriter) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		})
	}
	goodGrant := strings.Repeat("ab", 32)
	cases := map[string]struct {
		body string
		code int
		want string
	}{
		"refusal":        {`nope`, 503, "grant key fetch: 503"},
		"not json":       {`{nope`, 200, "invalid character"},
		"short grant":    {`{"dispatch_key":"abcd","envelope_key":"` + goodGrant + `"}`, 200, "grant key is not"},
		"short envelope": {`{"dispatch_key":"` + goodGrant + `","envelope_key":"abcd"}`, 200, "envelope key is not"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := fetchCoreKeys(answer(tc.body, tc.code))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	_, _, err := fetchCoreKeys(answer(`{"dispatch_key":"`+goodGrant+`","envelope_key":"`+goodGrant+`"}`, 200))
	if err != nil {
		t.Fatalf("two good keys refused: %v", err)
	}
}
