package protocol

import (
	"reflect"
	"testing"
)

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
