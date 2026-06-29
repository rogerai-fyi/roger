package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// registerWith drives the full /nodes/register HTTP path with one offer, signing the
// node registration (proof of possession) and, when signUser, the request with the
// owner key (login-to-monetize / login-to-go-private). Returns the broker status code
// and the (best-effort) JSON error message. Used to assert the operator price ceiling
// fires uniformly for PUBLIC, PRIVATE, and CONFIDENTIAL registrations.
func registerWith(t *testing.T, b *broker, nodeID string, nodePriv ed25519.PrivateKey, nodePubHex string, userPriv ed25519.PrivateKey, signUser bool, offer protocol.ModelOffer, private, confidential bool) (int, string) {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{offer}, Private: private, Confidential: confidential,
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	if signUser {
		signReq(r, userPriv, body)
	}
	w := httptest.NewRecorder()
	b.register(w, r)
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.Error.Message
}

// TestRegisterPriceCeilingPure checks the pure helper covers base AND scheduled-window
// prices, on the IN and OUT axes, with no flag exemption (the helper sees only offers).
func TestRegisterPriceCeilingPure(t *testing.T) {
	t.Setenv("ROGERAI_MAX_PRICE_OUT", "100")
	t.Setenv("ROGERAI_MAX_PRICE_IN", "50")

	if msg := registerPriceCeiling([]protocol.ModelOffer{{Model: "m", PriceOut: 99, PriceIn: 49}}); msg != "" {
		t.Errorf("within-ceiling offer rejected: %q", msg)
	}
	if msg := registerPriceCeiling([]protocol.ModelOffer{{Model: "m", PriceOut: 250}}); msg == "" {
		t.Error("over-ceiling base OUT price not rejected")
	}
	if msg := registerPriceCeiling([]protocol.ModelOffer{{Model: "m", PriceIn: 75}}); msg == "" {
		t.Error("over-ceiling base IN price not rejected")
	}
	// A scheduled window over the ceiling must also trip it (a free window is skipped).
	sched := protocol.ModelOffer{Model: "m", PriceOut: 1, Schedule: []protocol.PriceWindow{{Out: 500}}}
	if msg := registerPriceCeiling([]protocol.ModelOffer{sched}); msg == "" {
		t.Error("over-ceiling scheduled-window price not rejected")
	}
}

// TestRegisterCeilingGlobalAllBands is the integration assertion: an over-ceiling price
// is rejected at register for a PUBLIC, a PRIVATE, AND a CONFIDENTIAL registration -
// no flag exempts the operator ceiling. (The ceiling runs before owner-binding and
// attestation, so even an unverifiable confidential claim is rejected on price first.)
func TestRegisterCeilingGlobalAllBands(t *testing.T) {
	over := protocol.ModelOffer{Model: "m", Ctx: 4096, PriceOut: 250} // > $100/1M ceiling
	ok := protocol.ModelOffer{Model: "m", Ctx: 4096, PriceOut: 5}     // within ceiling

	t.Run("public", func(t *testing.T) {
		b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
		// PUBLIC priced node: owner-bound (priced), so sign with the owner key.
		code, msg := registerWith(t, b, "pub1", nodePriv, nodePubHex, userPriv, true, over, false, false)
		if code != http.StatusBadRequest {
			t.Fatalf("public over-ceiling register = %d, want 400 (msg=%q)", code, msg)
		}
		if !strings.Contains(msg, "ceiling") {
			t.Errorf("public rejection missing ceiling copy: %q", msg)
		}
		if code, _ := registerWith(t, b, "pub1", nodePriv, nodePubHex, userPriv, true, ok, false, false); code != http.StatusOK {
			t.Errorf("public within-ceiling register = %d, want 200", code)
		}
	})

	t.Run("private", func(t *testing.T) {
		b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
		code, msg := registerWith(t, b, "priv1", nodePriv, nodePubHex, userPriv, true, over, true, false)
		if code != http.StatusBadRequest {
			t.Fatalf("PRIVATE over-ceiling register = %d, want 400 (the --private flag must NOT exempt the ceiling) (msg=%q)", code, msg)
		}
		if !strings.Contains(msg, "ceiling") {
			t.Errorf("private rejection missing ceiling copy: %q", msg)
		}
	})

	t.Run("confidential", func(t *testing.T) {
		b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
		code, msg := registerWith(t, b, "conf1", nodePriv, nodePubHex, userPriv, true, over, false, true)
		if code != http.StatusBadRequest {
			t.Fatalf("CONFIDENTIAL over-ceiling register = %d, want 400 (the confidential tier must NOT exempt the ceiling) (msg=%q)", code, msg)
		}
		if !strings.Contains(msg, "ceiling") {
			t.Errorf("confidential rejection missing ceiling copy: %q", msg)
		}
	})
}

// TestConsumerBackstopDefault verifies the broker-side default consumer out-cap that
// makes the cap GLOBAL across every relay path (use/--freq/grant/agent/chat): a request
// carrying NO X-Roger-Max-Price-Out is bounded to the default, while an explicit higher
// cap is honored (the user can opt into paying more on purpose).
func TestConsumerBackstopDefault(t *testing.T) {
	t.Setenv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT", "10")

	if got := effectiveRelayMaxOut(0); got != 10 {
		t.Errorf("no-cap request -> %g, want the $10 default backstop", got)
	}
	if got := effectiveRelayMaxOut(50); got != 50 {
		t.Errorf("explicit $50 cap -> %g, want 50 (opt-in to pay more must survive)", got)
	}

	// disabling the backstop (<=0) means "no cap" so the operator ceiling alone bounds.
	t.Setenv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT", "0")
	if got := effectiveRelayMaxOut(0); got != 0 {
		t.Errorf("disabled backstop -> %g, want 0 (no cap)", got)
	}
}

// pickBroker registers ONE public priced node serving model "m" at priceOut and returns
// a broker whose pick we can exercise directly (the relay's enforcement point).
func pickBroker(t *testing.T, priceOut float64) *broker {
	t.Helper()
	mem := store.NewMem()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := &broker{
		db:           mem,
		priv:         brokerPriv,
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		private:      map[string]bool{},
		bandOf:       map[string]string{},
		tps:          map[string]float64{},
		pubOfUser:    map[string]string{},
	}
	b.nodes["n"] = protocol.NodeRegistration{NodeID: "n", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: priceOut}}}
	b.tunnels["n"] = &nodeTunnel{}
	b.lastSeen["n"] = time.Now()
	return b
}

// TestBrokerBackstopBoundsPick is the server-side defense in depth: a relay request that
// carries NO max-out (a hand-rolled API client) must still be bounded by the broker's
// default cap. We drive pick with the SAME effectiveRelayMaxOut the relay handler uses
// for the no-header case, and assert an over-default ($25) band is filtered out, while an
// explicit higher cap ($50) admits it (the deliberate opt-in path stays open). This is
// the global backstop shared by the --freq, agent-harness, grant, and chat paths - all
// reach pick through the same handler.
func TestBrokerBackstopBoundsPick(t *testing.T) {
	t.Setenv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT", "10")
	b := pickBroker(t, 25) // station charges $25/1M out - over the $10 default

	// No explicit cap on the request -> the broker applies the default backstop -> reject.
	noHeaderCap := effectiveRelayMaxOut(0)
	if _, _, ok := b.pickFor("m", false, 0, 0, noHeaderCap, "", nil, nil, nil, pickReq{}); ok {
		t.Error("broker bound to a $25 band with NO max-out header - the default backstop did not apply")
	}

	// An explicit $50 cap (opt-in to pay more) admits the same $25 station.
	explicit := effectiveRelayMaxOut(50)
	if _, _, ok := b.pickFor("m", false, 0, 0, explicit, "", nil, nil, nil, pickReq{}); !ok {
		t.Error("explicit $50 max-out failed to admit a $25 band - the opt-in-to-pay-more path is broken")
	}
}

// TestBrokerBackstopBoundsGrantUse: a GRANT-key request (routing confined to the grant
// owner's nodes via the allow-set) reaches the SAME pick + the SAME default backstop, so
// a grant caller that omits a max-out is bounded too. The allow-set confines routing; it
// does NOT exempt the price cap.
func TestBrokerBackstopBoundsGrantUse(t *testing.T) {
	t.Setenv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT", "10")
	b := pickBroker(t, 25) // the grant owner's node charges $25/1M out
	allow := map[string]bool{"n": true}

	noHeaderCap := effectiveRelayMaxOut(0)
	if _, _, ok := b.pickFor("m", false, 0, 0, noHeaderCap, "", nil, allow, nil, pickReq{}); ok {
		t.Error("grant use bound to a $25 band with NO max-out header - the default backstop did not apply on the grant path")
	}
	// An explicit higher cap still lets the grant caller pay more on purpose.
	if _, _, ok := b.pickFor("m", false, 0, 0, effectiveRelayMaxOut(50), "", nil, allow, nil, pickReq{}); !ok {
		t.Error("grant use with explicit $50 max-out failed to admit a $25 band")
	}
}
