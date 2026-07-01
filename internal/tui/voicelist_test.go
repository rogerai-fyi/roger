package tui

// voicelist_test.go specs the bundled Kokoro voice list + the prefix→hint decoder + the local
// /v1/audio/voices response parser that feed the SHARE VOICE BOOTH picker. These are PURE data +
// parsing (no UI), so they are table-tested directly; the model-level picker behavior (fetch,
// fallback, filter, audition) is driven through godog in voice_picker.feature.

import (
	"strings"
	"testing"
)

// The bundled list is the fallback the picker uses when the local server can't enumerate voices,
// so it must never blank: it carries the known American + British Kokoro ids and a healthy count.
func TestBundledKokoroVoices(t *testing.T) {
	got := bundledKokoroVoices()
	if len(got) < 20 {
		t.Fatalf("bundled Kokoro list has %d ids, want >= 20 (the picker must never look empty)", len(got))
	}
	set := map[string]bool{}
	for _, v := range got {
		set[v] = true
	}
	for _, want := range []string{"af_heart", "am_onyx", "bf_emma", "bm_george", "af_aoede", "af_bella"} {
		if !set[want] {
			t.Errorf("bundled Kokoro list is missing %q", want)
		}
	}
	// Every id must be a real Kokoro-shaped id (a prefix + underscore + name) so the prefix decoder
	// always has something to group on.
	for _, v := range got {
		if !strings.Contains(v, "_") {
			t.Errorf("bundled id %q is not a prefix_name Kokoro id", v)
		}
	}
	// No duplicates (a duped id would render twice in the grid).
	if len(set) != len(got) {
		t.Errorf("bundled Kokoro list has duplicates: %d ids, %d unique", len(got), len(set))
	}
}

// prefixHint decodes a Kokoro id's leading prefix into a human group label: af_/am_/bf_/bm_ =
// American/British female/male. An unknown prefix falls back to a neutral "Other" so a
// multilingual id (ef_/em_/ff_) never crashes the grouping.
func TestPrefixHint(t *testing.T) {
	cases := map[string]string{
		"af_heart":  "American female",
		"am_onyx":   "American male",
		"bf_emma":   "British female",
		"bm_george": "British male",
		"ef_dora":   "Other",
		"weird":     "Other",
		"":          "Other",
	}
	for id, want := range cases {
		if got := prefixHint(id); got != want {
			t.Errorf("prefixHint(%q) = %q, want %q", id, got, want)
		}
	}
}

// parseVoicesResponse reads the local server's GET /v1/audio/voices body into a flat id list. The
// endpoint shape is not fully standardized across Kokoro forks, so it accepts BOTH the common
// shapes: a bare JSON array of ids, and an object {"voices":[{"id":...}|"..."]}. A body it can't
// parse yields NO ids (the caller then falls back to the bundled list) — never a crash.
func TestParseVoicesResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"bare array of strings", `["af_heart","am_onyx"]`, []string{"af_heart", "am_onyx"}},
		{"object with string voices", `{"voices":["af_heart","bf_emma"]}`, []string{"af_heart", "bf_emma"}},
		{"object with id objects", `{"voices":[{"id":"af_heart"},{"id":"am_onyx"}]}`, []string{"af_heart", "am_onyx"}},
		{"data array (OpenAI-ish)", `{"data":[{"id":"af_heart"}]}`, []string{"af_heart"}},
		{"garbage", `not json`, nil},
		{"empty object", `{}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseVoicesResponse([]byte(tc.body))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("parseVoicesResponse(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// groupVoices buckets a flat id list into the four ordered groups (+ Other), preserving input
// order within each group, so the picker renders American ♀ / ♂ / British ♀ / ♂ / Other.
func TestGroupVoices(t *testing.T) {
	groups := groupVoices([]string{"bm_george", "af_heart", "am_onyx", "bf_emma", "ef_dora", "af_bella"})
	byLabel := map[string][]string{}
	for _, g := range groups {
		byLabel[g.label] = g.ids
	}
	if got := byLabel["American female"]; strings.Join(got, ",") != "af_heart,af_bella" {
		t.Errorf("American female group = %v, want [af_heart af_bella] in order", got)
	}
	if got := byLabel["American male"]; strings.Join(got, ",") != "am_onyx" {
		t.Errorf("American male group = %v, want [am_onyx]", got)
	}
	if got := byLabel["British female"]; strings.Join(got, ",") != "bf_emma" {
		t.Errorf("British female group = %v, want [bf_emma]", got)
	}
	if got := byLabel["Other"]; strings.Join(got, ",") != "ef_dora" {
		t.Errorf("Other group = %v, want [ef_dora]", got)
	}
	// Empty groups are omitted (British male has no ids here), so the grid shows no empty headers.
	for _, g := range groups {
		if len(g.ids) == 0 {
			t.Errorf("group %q rendered with zero ids (empty groups must be omitted)", g.label)
		}
	}
}
