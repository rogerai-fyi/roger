package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPublicHoldSizedAtRealPrice is the C1 regression guard: a PAID public-market relay
// must pre-authorize the hold at the offer's REAL active price, not ~$0. With a tiny
// monthly cap and no prior spend, a paid request whose real worst-case cost EXCEEDS the
// cap must be rejected (402) at the hold gate. Before the fix the hold was sized from the
// public plan's zero prices (~1e-6), so it slipped under any cap and the settle clamp
// then capped the real charge down to ~$0 - i.e. free paid inference.
func TestPublicHoldSizedAtRealPrice(t *testing.T) {
	b, userPriv, wallet := capBroker(t)
	// paid-m is PriceOut 0.5/1M, ctx default 8192 -> real worst-case hold ~= 8192*0.5/1e6
	// = ~$0.0041. A $0.002 cap with $0 spent is below that, so the FIXED hold trips the cap.
	if err := b.db.SetMonthlyCap(wallet, 0.002); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"paid-m","max_tokens":8192}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("paid public relay with a real hold over the cap = %d, want 402 "+
			"(C1: the hold must be sized at the offer's real price, not ~$0)", w.Code)
	}
}
