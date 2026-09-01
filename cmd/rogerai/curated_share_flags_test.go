package main

// Pre-push audit regression: the --curated help promised "requires --upstream" but nothing
// enforced it, so a bare `roger share --curated openrouter` registered a curated station
// fronting NO commercial endpoint - auto-detect would aim it at a local model, the exact
// misrepresentation the curated flag exists to prevent. The rule, CLI-side and testable:
// curated requires an explicit --upstream. Zero upstream prices stay legal (a free
// upstream posts free - spec: curated_pricing.feature "free upstream stays free").

import (
	"strings"
	"testing"
)

func TestCuratedShareRequiresAnExplicitUpstream(t *testing.T) {
	cases := []struct {
		name             string
		curated, up      string
		upIn, upOut      float64
		wantErrMentions  string
	}{
		{"bare curated, no upstream", "openrouter", "", 1, 2, "--upstream"},
		{"curated with endpoint ok", "openrouter", "https://openrouter.ai/api/v1/chat/completions", 1, 2, ""},
		{"curated free upstream ok", "openrouter", "https://openrouter.ai/api/v1/chat/completions", 0, 0, ""},
		{"not curated, nothing required", "", "", 0, 0, ""},
		{"curated negative list", "openrouter", "https://openrouter.ai/api/v1/chat/completions", -1, 2, "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateCuratedShare(c.curated, c.up, c.upIn, c.upOut)
			if c.wantErrMentions == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErrMentions) {
				t.Fatalf("want an error mentioning %q, got %v", c.wantErrMentions, err)
			}
		})
	}
}
