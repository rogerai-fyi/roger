package main

import (
	"encoding/json"
	"testing"
)

// /market carried min_price, which is the cheapest INPUT price, and computed the cheapest
// OUT-price only to derive the display tier - it never serialized it. Every price the
// website quotes is an OUT price ("$/1M out"), so a reader of the feed had no way to get
// the number the site talks in, and the pricing calculator quietly multiplied an output
// volume by an input rate. That understates the band and overstates the saving, on the
// page whose whole argument is that we do not publish numbers we cannot support.
//
// The field is additive, so an older consumer is unaffected.
func TestMarketViewSerializesTheCheapestOutPrice(t *testing.T) {
	v := marketView{Model: "m", MinPrice: 0.10, MinOut: 0.40}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := back["min_out"]
	if !ok {
		t.Fatal("the market view does not serialize min_out, so no consumer can read the out-price")
	}
	if got != 0.40 {
		t.Errorf("min_out = %v, want 0.40", got)
	}
	if back["min_price"] != 0.10 {
		t.Errorf("min_price = %v, want the input price 0.10 - the two must not be confused", back["min_price"])
	}
}
