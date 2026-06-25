package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/client"
)

// brokerDiscoverFields is the set of REAL per-offer fields the broker emits on
// /discover + /bands/resolve (cmd/rogerai-broker market.go offerView). The TUI and
// the web both consume this; the TUI offer struct must decode every one (a
// decoded-but-dropped field is a silent metric loss). Kept as the explicit contract
// so a broker field that the TUI forgets to decode trips this test.
var brokerDiscoverFields = []string{
	"node_id", "region", "hw", "model",
	"price_in", "price_out", "ctx", "ctx_estimated",
	"online", "confidential", "free_now",
	"tps", "ttft_ms", "success", "success_seen", "verified",
	"signal", "in_flight", "terms",
}

// jsonTags returns the set of json tag names declared on a struct type.
func jsonTags(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := tag
		if c := strings.IndexByte(name, ','); c >= 0 {
			name = name[:c]
		}
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// TestTUIOfferDecodesEveryBrokerField: the TUI offer struct decodes EVERY real field
// the broker /discover emits (the source of truth). A dropped field here is a metric
// the band row / [i] detail can never show.
func TestTUIOfferDecodesEveryBrokerField(t *testing.T) {
	tags := jsonTags(reflect.TypeOf(offer{}))
	for _, f := range brokerDiscoverFields {
		if !tags[f] {
			t.Errorf("TUI offer struct drops broker /discover field %q (decoded-but-dropped == invisible metric)", f)
		}
	}
}

// TestParityFieldSetTUIvsWeb: the TUI and the web consume the same /discover offer.
// This decodes a representative broker offer into the TUI struct and asserts the
// values the row + detail render match the raw JSON (same values, only labels may
// differ between surfaces). The web's bands.js reads the identical keys.
func TestParityFieldSetTUIvsWeb(t *testing.T) {
	raw := `{
		"node_id":"keqxz","region":"us-west-2","hw":"multi-gpu","model":"qwen3-8b",
		"price_in":0.10,"price_out":0.40,"ctx":131072,"ctx_estimated":false,
		"online":true,"confidential":false,"free_now":false,
		"tps":142,"ttft_ms":180,"success":0.98,"success_seen":true,"verified":true,
		"signal":71,"in_flight":2,
		"terms":{"supply":4,"speed":13,"latency":10,"verified":20,"success":14,"trust":10,"congestion":0,"total":71}
	}`
	var o offer
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Values the band row + [i] detail render must equal the broker's (no recompute).
	if o.Signal != 71 || o.Terms.Total != 71 {
		t.Errorf("signal/terms.total must be the broker's 71, got %d/%d", o.Signal, o.Terms.Total)
	}
	if o.PriceIn != 0.10 || o.PriceOut != 0.40 {
		t.Errorf("price in/out must match the broker: %v/%v", o.PriceIn, o.PriceOut)
	}
	if o.TPS != 142 || o.TTFTMs != 180 {
		t.Errorf("tps/ttft must match the broker: %v/%v", o.TPS, o.TTFTMs)
	}
	if o.Ctx != 131072 || o.CtxEstimated {
		t.Errorf("ctx must be the detected 131072 (not estimated): %d est=%v", o.Ctx, o.CtxEstimated)
	}
	if o.SuccessRate != 0.98 || !o.SuccessSeen || !o.Verified || o.HW != "multi-gpu" || o.InFlight != 2 {
		t.Errorf("success/verified/hw/in_flight must match the broker: %v/%v/%v/%q/%d",
			o.SuccessRate, o.SuccessSeen, o.Verified, o.HW, o.InFlight)
	}
	// The grouped band's signal-term breakdown == the broker's terms (the [i] detail
	// source), proving the detail shows the broker's value, never a local recompute.
	bands := groupBands([]offer{o}, nil)
	terms, sig, ok := bands[0].termsBreakdown()
	if !ok || sig != 71 || terms.Verified != 20 || terms.Total != 71 {
		t.Errorf("band detail term breakdown must be the broker's (sig 71, verified 20), got ok=%v sig=%d terms=%+v", ok, sig, terms)
	}
}

// TestClientOfferDecodesPrivateBandFields: the client.Offer (the PRIVATE-band
// /bands/resolve decode) carries the real fields the broker sends there - region, hw,
// ctx (+estimated), free-now, ttft, verified - so a private band's row + [i] detail
// are not a stripped-down subset of a public one.
func TestClientOfferDecodesPrivateBandFields(t *testing.T) {
	tags := jsonTags(reflect.TypeOf(client.Offer{}))
	for _, f := range []string{"node_id", "region", "hw", "model", "price_in", "price_out", "ctx", "ctx_estimated", "online", "confidential", "free_now", "tps", "ttft_ms", "verified", "signal", "in_flight"} {
		if !tags[f] {
			t.Errorf("client.Offer drops the /bands/resolve field %q (private bands would render it blank)", f)
		}
	}
}
