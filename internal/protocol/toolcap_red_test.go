package protocol

import (
	"reflect"
	"testing"
)

// TestToolsCapabilityRecognized is the SIGNAL half of the tool-call capability probe
// (features/trust/toolcall_probe.feature). It is RED against origin/main: knownCapabilities
// is the CLOSED set {"vision"} (protocol.go:105-109), so "tools" is dropped as unknown and
// CanonicalCapabilities([]string{"tools"}) returns []string{} today. GREEN adds
// CapTools ("tools") to knownCapabilities so a probed model can carry it exactly like
// "vision" — canonicalized, lowercased, trimmed, deduped, sorted with "vision".
func TestToolsCapabilityRecognized(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"tools survives canonicalization", []string{"tools"}, []string{"tools"}},
		{"lowercased + trimmed like vision", []string{" Tools "}, []string{"tools"}},
		{"deduped", []string{"tools", "tools"}, []string{"tools"}},
		{"tools + vision sorted canonically", []string{"vision", "tools"}, []string{"tools", "vision"}},
		{"unknown dropped, tools kept", []string{"telepathy", "TOOLS"}, []string{"tools"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CanonicalCapabilities(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CanonicalCapabilities(%v) = %v, want %v (\"tools\" must be a known, verified capability)", c.in, got, c.want)
			}
		})
	}
}
