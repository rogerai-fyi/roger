package main

// curated_gates_test.go pins the audit's CRITICAL: the curated block must run BEFORE the
// register door's money gates, so the gates judge the DERIVED posted price rather than
// the zero the CLI sends. Each case here was an open door before the reorder.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

func TestCuratedDerivedPriceCannotEscapeTheCeiling(t *testing.T) {
	b, userPriv, nodePriv, nodePub := newBandBroker(t)
	// A declared list whose DERIVED posted price (list x the markup) lands over the hard global
	// ceiling. Pre-reorder, the ceiling ran on the pre-derivation zeros and this sailed
	// through; the ceiling is a consumer-burn safety and no flag may exempt it.
	over := maxPriceOutCeiling()/curatedMarkup + 1
	reg := protocol.NodeRegistration{
		NodeID: "curc1", PubKey: nodePub, BridgeToken: "tok", TS: time.Now().Unix(),
		Curated: true, CuratedProvider: "openrouter",
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 8192, UpstreamIn: 1, UpstreamOut: over}},
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("a curated list deriving past the global ceiling registered: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ceiling") {
		t.Fatalf("the rejection should be the ceiling's own sentence, got: %s", w.Body.String())
	}
}

func TestCuratedShareIsAnEarningNodeToTheLoginGate(t *testing.T) {
	b, _, nodePriv, nodePub := newBandBroker(t)
	// The CLI sends PriceIn/Out = 0 and declares the upstream list; pre-reorder the
	// login-to-monetize gate saw the zeros and admitted an ANONYMOUS earning node. With
	// derivation first, an unsigned-by-owner curated share with a nonzero list must be
	// rejected exactly as any priced share is.
	reg := protocol.NodeRegistration{
		NodeID: "curc2", PubKey: nodePub, BridgeToken: "tok", TS: time.Now().Unix(),
		Curated: true, CuratedProvider: "openrouter",
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 8192, UpstreamIn: 1, UpstreamOut: 2}},
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	// NO owner signature: an anonymous registrant.
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("an anonymous curated share with a priced upstream registered as an earning node")
	}
}

func TestCuratedExplicitPriceMustEqualTheDerivation(t *testing.T) {
	// The posted price of a curated offer is DERIVED (list x the one broker-owned markup
	// constant). Below-list was already refused as underwater; an ABOVE-list explicit
	// price was silently overwritten, which hides an operator's mistaken belief that
	// they set their own posted price. Any explicit price that is not the list is refused.
	b, userPriv, nodePriv, nodePub := newBandBroker(t)
	reg := protocol.NodeRegistration{
		NodeID: "curc3", PubKey: nodePub, BridgeToken: "tok", TS: time.Now().Unix(),
		Curated: true, CuratedProvider: "openrouter",
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 8192, UpstreamIn: 1, UpstreamOut: 2, PriceOut: 4}},
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("an explicit curated price above the list registered (silently overwritten)")
	}
	if !strings.Contains(w.Body.String(), "derive") {
		t.Fatalf("the rejection should say the price is derived, got: %s", w.Body.String())
	}
}

func TestHumanRegistrationCannotCarryACuratedProviderName(t *testing.T) {
	// CuratedProvider is a display string the dial renders and /discover publishes. A
	// HUMAN registration carrying one would dress a local station in a commercial badge
	// (curated=false so no gate saw it) - zeroed at the door, like human upstream_* prices.
	b, userPriv, nodePriv, nodePub := newBandBroker(t)
	reg := protocol.NodeRegistration{
		NodeID: "hum1", PubKey: nodePub, BridgeToken: "tok", TS: time.Now().Unix(),
		CuratedProvider: "openrouter", // curated=false
		Offers:          []protocol.ModelOffer{{Model: "m", Ctx: 8192}},
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("human register = %d: %s", w.Code, w.Body.String())
	}
	b.mu.Lock()
	got := b.nodes["hum1"].CuratedProvider
	b.mu.Unlock()
	if got != "" {
		t.Fatalf("a human registration published curated_provider %q", got)
	}
}

func TestCuratedProviderNameIsBoundedAndControlStripped(t *testing.T) {
	// The provider name rides /discover, /market, becomes the Region, and renders raw in
	// every TUI band badge - the same terminal surface quant/weights/variant are
	// sanitized for. An ANSI escape or a 300-char name must not survive the door.
	b, userPriv, nodePriv, nodePub := newBandBroker(t)
	reg := protocol.NodeRegistration{
		NodeID: "curc4", PubKey: nodePub, BridgeToken: "tok", TS: time.Now().Unix(),
		Curated: true, CuratedProvider: "open\x1b[2Jrouter" + strings.Repeat("x", 300),
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 8192}},
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("curated register = %d: %s", w.Code, w.Body.String())
	}
	b.mu.Lock()
	got := b.nodes["curc4"].CuratedProvider
	region := b.nodes["curc4"].Region
	b.mu.Unlock()
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(region, 0x1b) {
		t.Fatalf("an ANSI escape survived: provider %q region %q", got, region)
	}
	if len(got) > 40 || len(region) > 40 {
		t.Fatalf("unbounded display string: provider %d bytes, region %d bytes", len(got), len(region))
	}
}
