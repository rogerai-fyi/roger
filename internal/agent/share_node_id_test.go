package agent

import (
	"strings"
	"testing"
)

// TestShareNodeIDDistinctModels: two DIFFERENT models on the same host must get
// distinct ids - the whole point of the fix (no more bare-hostname collision).
func TestShareNodeIDDistinctModels(t *testing.T) {
	host := "larrys-mac-studio"
	a := ShareNodeID(host, "qwen3-coder-next", "http://127.0.0.1:8080/v1/chat/completions")
	b := ShareNodeID(host, "gpt-oss-20b", "http://127.0.0.1:8080/v1/chat/completions")
	if a == b {
		t.Fatalf("two models on one host collided: %q == %q", a, b)
	}
	if !strings.HasPrefix(a, host+"-qwen3-coder-next") {
		t.Errorf("id not readable/derived from host+model: %q", a)
	}
}

// TestShareNodeIDSameModelDisambiguated: the SAME model shared twice on one host
// (different local servers/ports) must still NOT collide.
func TestShareNodeIDSameModelDisambiguated(t *testing.T) {
	host := "larrys-mac-studio"
	a := ShareNodeID(host, "gpt-oss-20b", "http://127.0.0.1:8080/v1/chat/completions")
	b := ShareNodeID(host, "gpt-oss-20b", "http://127.0.0.1:8081/v1/chat/completions")
	if a == b {
		t.Fatalf("same model on two ports collided: %q == %q", a, b)
	}
}

// TestShareNodeIDStable: repeated calls with the SAME (host, model, upstream) must
// yield the SAME id, so a restart re-registers as the same node (no orphan churn /
// no new id each launch).
func TestShareNodeIDStable(t *testing.T) {
	host, model, up := "larrys-mac-studio", "gpt-oss-20b", "http://127.0.0.1:8080/v1/chat/completions"
	first := ShareNodeID(host, model, up)
	for i := 0; i < 5; i++ {
		if got := ShareNodeID(host, model, up); got != first {
			t.Fatalf("id not stable across calls: call %d gave %q, want %q", i, got, first)
		}
	}
	// Stable even when the upstream lacks a port (deterministic hash fallback, not
	// a fresh random suffix each call).
	noPort := "http://localhost/v1/chat/completions"
	s1 := ShareNodeID(host, model, noPort)
	s2 := ShareNodeID(host, model, noPort)
	if s1 != s2 {
		t.Fatalf("portless upstream not stable: %q != %q", s1, s2)
	}
}

// TestShareNodeIDReadableSlug: the slug is lowercased + non-alphanumerics collapsed
// to single dashes, trimmed - readable and broker-safe.
func TestShareNodeIDReadableSlug(t *testing.T) {
	id := ShareNodeID("Larrys-MAC-Studio", "Qwen3-Coder/Next  v2", "http://127.0.0.1:8080")
	if id != strings.ToLower(id) {
		t.Errorf("id not lowercased: %q", id)
	}
	if strings.Contains(id, "--") || strings.Contains(id, "/") || strings.Contains(id, " ") {
		t.Errorf("id not cleanly slugged: %q", id)
	}
	if !strings.HasPrefix(id, "larrys-mac-studio-qwen3-coder-next-v2") {
		t.Errorf("unexpected slug: %q", id)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"qwen3-coder-next": "qwen3-coder-next",
		"Qwen3-Coder/Next": "qwen3-coder-next",
		"  gpt oss 20b  ":  "gpt-oss-20b",
		"llama-3.3-70b!!":  "llama-3-3-70b", // dots are non-alphanumeric -> collapse to a dash
		"___":              "",
		"a..b__c":          "a-b-c",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
