package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestCapabilitiesJSONShape pins the omitempty wire shape that keeps the registration
// possession-proof stable across binary versions: a real capability (["vision"]) survives, while
// nil AND [] both serialize to NO "capabilities" key. If nil emitted a key, a node that predates
// the field and one that carries it would sign different bytes and the broker would 401 them
// (the outage this reverts). The app treats vision/[]/absent identically for non-vision models.
func TestCapabilitiesJSONShape(t *testing.T) {
	roundTrip := func(in []string) []string {
		b, _ := json.Marshal(ModelOffer{Model: "m", Capabilities: in})
		var o ModelOffer
		_ = json.Unmarshal(b, &o)
		return o.Capabilities
	}
	if got := roundTrip([]string{"vision"}); !reflect.DeepEqual(got, []string{"vision"}) {
		t.Errorf("a real capability must survive, got %#v", got)
	}
	if got := roundTrip([]string{}); got != nil {
		t.Errorf("empty [] must serialize to absent (nil round-trip), got %#v", got)
	}
	if got := roundTrip(nil); got != nil {
		t.Errorf("nil stays absent, got %#v", got)
	}
	// The KEY must be absent for a nil value, so old + new binaries produce byte-identical offers.
	if b, _ := json.Marshal(ModelOffer{Model: "m"}); strings.Contains(string(b), "capabilities") {
		t.Errorf("nil capabilities must omit the key entirely: %s", b)
	}
}

func TestCanonicalCapabilities(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays nil (undetermined)", nil, nil},
		{"empty stays empty (text-only)", []string{}, []string{}},
		{"vision passes", []string{"vision"}, []string{"vision"}},
		{"lowercased + trimmed", []string{" Vision "}, []string{"vision"}},
		{"deduped", []string{"vision", "vision"}, []string{"vision"}},
		{"unknown dropped -> [] not nil (a real declaration)", []string{"telepathy"}, []string{}},
		{"unknown dropped, vision kept", []string{"telepathy", "VISION"}, []string{"vision"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CanonicalCapabilities(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CanonicalCapabilities(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeCanonicalizesCapabilities(t *testing.T) {
	o := ModelOffer{Model: "m", Capabilities: []string{"VISION", "vision", "bogus"}}
	o.Normalize()
	if !reflect.DeepEqual(o.Capabilities, []string{"vision"}) {
		t.Fatalf("Normalize capabilities = %v, want [vision]", o.Capabilities)
	}
	// A nil capabilities stays nil (omitted on the wire = undetermined), never fabricated.
	o2 := ModelOffer{Model: "m"}
	o2.Normalize()
	if o2.Capabilities != nil {
		t.Fatalf("Normalize should leave undetermined capabilities nil, got %v", o2.Capabilities)
	}
}
