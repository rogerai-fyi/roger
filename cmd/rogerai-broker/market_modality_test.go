package main

// market_modality_test.go pins the /market modality parity fix: the aggregated per-model
// market rows must carry the SAME canonical modality the per-offer /discover feed
// (offerView) already does - "tts"/"stt" for voice stations, "chat" for chat offers AND
// for legacy empty-modality offers (offerModality normalization, never a bare "") - so a
// voice model ("voice", "whisper-1") can never masquerade as a usable CHAT model in an
// aggregated client picker (the iOS EXTERNAL-READINESS item 1; the TUI had the same leak).
// Asserted on the WIRE (the real GET /market handler + raw JSON), not on the struct.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestMarketRowsCarryModality: every /market row carries the canonical modality of its
// offers - tts and stt ride verbatim, an explicit chat rides as "chat", and a pre-voice
// offer with NO modality normalizes to "chat" exactly like offerView does.
func TestMarketRowsCarryModality(t *testing.T) {
	now := time.Now()
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{
			"speaker":  {NodeID: "speaker", Offers: []protocol.ModelOffer{{Model: "voice", Modality: "tts", PriceIn: 0.2}}},
			"listener": {NodeID: "listener", Offers: []protocol.ModelOffer{{Model: "whisper-1", Modality: "stt", PriceIn: 0.1}}},
			"chatter":  {NodeID: "chatter", Offers: []protocol.ModelOffer{{Model: "gpt-oss-20b", Modality: "chat", PriceIn: 0.3}}},
			"legacy":   {NodeID: "legacy", Offers: []protocol.ModelOffer{{Model: "llama-3.3-70b", PriceIn: 0.4}}}, // pre-voice: empty modality
		},
		lastSeen: map[string]time.Time{"speaker": now, "listener": now, "chatter": now, "legacy": now},
	}

	rec := httptest.NewRecorder()
	b.market(rec, httptest.NewRequest(http.MethodGet, "/market", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /market status = %d, want 200", rec.Code)
	}
	var resp struct {
		Market []struct {
			Model    string `json:"model"`
			Modality string `json:"modality"`
		} `json:"market"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad /market JSON: %v", err)
	}
	got := map[string]string{}
	for _, row := range resp.Market {
		got[row.Model] = row.Modality
	}

	cases := []struct {
		name, model, want string
	}{
		{"a tts voice rides verbatim", "voice", "tts"},
		{"an stt listener rides verbatim", "whisper-1", "stt"},
		{"an explicit chat offer rides as chat", "gpt-oss-20b", "chat"},
		{"a legacy empty modality normalizes to chat", "llama-3.3-70b", "chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mod, ok := got[tc.model]
			if !ok {
				t.Fatalf("model %q missing from /market: %+v", tc.model, got)
			}
			if mod != tc.want {
				t.Fatalf("/market modality for %q = %q, want %q (offerView parity)", tc.model, mod, tc.want)
			}
		})
	}
}
