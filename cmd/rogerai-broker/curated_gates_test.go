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
	// A declared list whose DERIVED posted price (list x 1.30) lands over the hard global
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
