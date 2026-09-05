package main

// ctx_gate_test.go - features/routing/eligibility.feature, the declared-window gate
// (live catch 2026-09-05: an honest 8192-window operator was struck to a HOLD by a
// 13k-token request the broker dispatched into a guaranteed refusal).

import (
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

func ctxGateBroker(t *testing.T, ctx int, estimated bool) *broker {
	t.Helper()
	b, _, _, _ := newBandBroker(t)
	b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m1", Ctx: ctx, CtxEstimated: estimated}}}
	b.lastSeen["n1"] = time.Now()
	return b
}

func TestDeclaredCtxGatesOversizedRequests(t *testing.T) {
	b := ctxGateBroker(t, 8192, false)
	if _, _, ok := b.pickFor("m1", false, 0, 0, 0, "", nil, nil, nil, pickReq{promptTokens: 13073}); ok {
		t.Fatal("a 13k request was dispatched into a declared 8192 window - a guaranteed refusal")
	}
	if _, _, ok := b.pickFor("m1", false, 0, 0, 0, "", nil, nil, nil, pickReq{promptTokens: 4000}); !ok {
		t.Fatal("a fitting request must still pick the band")
	}
}

func TestEstimatedCtxNeverGates(t *testing.T) {
	b := ctxGateBroker(t, 8192, true)
	if _, _, ok := b.pickFor("m1", false, 0, 0, 0, "", nil, nil, nil, pickReq{promptTokens: 13073}); !ok {
		t.Fatal("an ESTIMATED window must not gate - it is a display guess")
	}
}

func TestOversizedFailureNeverStrikesTheOperator(t *testing.T) {
	b := ctxGateBroker(t, 8192, false)
	rec := protocol.UsageReceipt{RequestID: "req_over", NodeID: "n1", Model: "m1"}
	if !b.oversizedForNode("n1", "m1", 13073) {
		t.Fatal("a 13k-token request against a declared 8192 window must classify as oversized")
	}
	if b.oversizedForNode("n1", "m1", 4000) {
		t.Fatal("a fitting request must not classify as oversized")
	}
	if b.maybeFlagEmptyOutput("n1", "m1", rec, 400, 13073, "") {
		t.Fatal("an oversized refusal struck the operator")
	}
	if !b.maybeFlagEmptyOutput("n1", "m1", rec, 400, 4000, "") {
		t.Fatal("a fitting-request void must still strike")
	}
	// the upstream's confession covers the chars/4 under-count (code/CJK): a 5000-
	// estimate against an 8192 window plausibly overflows (x2 slack), no strike.
	if b.maybeFlagEmptyOutput("n1", "m1", rec, 400, 5000,
		`{"error":{"message":"request (9000 tokens) exceeds the available context size (8192 tokens)"}}`) {
		t.Fatal("the upstream said context-overflow and the operator was struck anyway")
	}
	// but the confession is NOT a skeleton key: a tiny request echoing overflow
	// vocabulary still strikes - a node cannot chant "kv cache" into unstrikeability.
	if !b.maybeFlagEmptyOutput("n1", "m1", rec, 400, 100,
		`{"error":{"message":"kv cache exhausted"}}`) {
		t.Fatal("a tiny request's overflow-flavored error suppressed the strike - the gaming vector")
	}
}

// The measure is TEXT tokens, never body bytes: a photo request carries megabytes of
// base64 that are near-zero prompt tokens - it must neither be gated off vision bands
// nor suppress a real strike (the audit's vision catch).
func TestImageBodiesMeasureAsTheirText(t *testing.T) {
	img := strings.Repeat("A", 200_000) // ~200KB of base64
	body := []byte(`{"model":"m1","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is in this photo?"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]}]}`)
	if got := approxPromptTokens(body); got > 64 {
		t.Fatalf("a photo request measured %d tokens - the image bytes leaked into the gate", got)
	}
}
