package agent

import (
	"regexp"
	"strings"
	"testing"
)

// TestShareNodeIDDistinctModels: two DIFFERENT models from the same station must get
// distinct ids - the per-model slug keeps every band a separate broker node.
func TestShareNodeIDDistinctModels(t *testing.T) {
	st := "brave-otter"
	a := ShareNodeID(st, "qwen3-coder-next", 0)
	b := ShareNodeID(st, "gpt-oss-20b", 0)
	if a == b {
		t.Fatalf("two models on one station collided: %q == %q", a, b)
	}
	if a != "brave-otter-qwen3-coder-next" {
		t.Errorf("id not <station>-<model>: %q", a)
	}
}

// TestShareNodeIDSameModelInstance: the SAME model shared twice from one station (two
// local servers) must NOT collide - the instance index disambiguates (NOT a port).
func TestShareNodeIDSameModelInstance(t *testing.T) {
	st := "brave-otter"
	a := ShareNodeID(st, "gpt-oss-20b", 0)
	b := ShareNodeID(st, "gpt-oss-20b", 2)
	c := ShareNodeID(st, "gpt-oss-20b", 3)
	if a == b || a == c || b == c {
		t.Fatalf("same model, different instances collided: %q %q %q", a, b, c)
	}
	if b != "brave-otter-gpt-oss-20b-2" || c != "brave-otter-gpt-oss-20b-3" {
		t.Errorf("instance suffix wrong: %q %q", b, c)
	}
	// instance 0 and 1 both yield the bare id (the common single-instance case).
	if got := ShareNodeID(st, "gpt-oss-20b", 1); got != a {
		t.Errorf("instance 1 should match the bare id: %q != %q", got, a)
	}
}

// TestShareNodeIDStable: repeated calls with the SAME (station, model, instance) must
// yield the SAME id, so a restart re-registers as the same node (no orphan churn).
func TestShareNodeIDStable(t *testing.T) {
	first := ShareNodeID("brave-otter", "gpt-oss-20b", 0)
	for i := 0; i < 5; i++ {
		if got := ShareNodeID("brave-otter", "gpt-oss-20b", 0); got != first {
			t.Fatalf("id not stable across calls: call %d gave %q, want %q", i, got, first)
		}
	}
}

// hostPortish matches anything that looks like a hostname token or a :port - the node
// id must contain NONE of it. We use a deliberately broad set of leak signals.
var leakSignals = []string{
	"demo-mac-studio", "macbook", "mac-studio", "localhost", "127", "0-0-0-0",
	":8080", "8080", "8081", "8060", "1234", "11434", "8000",
}

// TestShareNodeIDNoHostNoPort: the PRIVACY guarantee. Given a station + model, the id
// must carry ONLY the station + model slug - never the hostname, never a port. This is
// the founder's #1/#3: /discover echoes node_id verbatim to consumers.
func TestShareNodeIDNoHostNoPort(t *testing.T) {
	// Station is the friendly callsign; the model is public. No host, no port input even
	// exists in the signature anymore - assert the OUTPUT carries no leak token either.
	for _, model := range []string{"gpt-oss-20b", "qwen3-coder-next", "llama-3.3-70b"} {
		id := ShareNodeID("brave-otter-37", model, 0)
		for _, bad := range leakSignals {
			if strings.Contains(id, bad) {
				t.Errorf("node id %q leaked %q (hostname/port must never appear)", id, bad)
			}
		}
		if !strings.HasPrefix(id, "brave-otter-37-") {
			t.Errorf("node id %q does not lead with the station callsign", id)
		}
	}
}

// TestShareNodeIDEmptyStationGenerates: an empty/blank station never yields a bare,
// hostname-less or empty id - it falls back to a fresh generated callsign so the id is
// always station-prefixed (and still leaks nothing).
func TestShareNodeIDEmptyStationGenerates(t *testing.T) {
	id := ShareNodeID("", "gpt-oss-20b", 0)
	if id == "" || id == "gpt-oss-20b" || strings.HasPrefix(id, "-") {
		t.Fatalf("empty station produced a bare/empty id: %q", id)
	}
	if !strings.HasSuffix(id, "-gpt-oss-20b") {
		t.Errorf("generated id not <station>-<model>: %q", id)
	}
}

// TestShareNodeIDReadableSlug: a free-text station is lowercased + slugged, so the id
// stays readable and broker-safe regardless of how the owner typed the callsign.
func TestShareNodeIDReadableSlug(t *testing.T) {
	id := ShareNodeID("Brave Otter!!", "Qwen3-Coder/Next  v2", 0)
	if id != strings.ToLower(id) {
		t.Errorf("id not lowercased: %q", id)
	}
	if strings.Contains(id, "--") || strings.Contains(id, "/") || strings.Contains(id, " ") {
		t.Errorf("id not cleanly slugged: %q", id)
	}
	if id != "brave-otter-qwen3-coder-next-v2" {
		t.Errorf("unexpected slug: %q", id)
	}
}

// callsign is `<adj>-<animal>-<2digits>`: all-lowercase, dash-joined, ASCII letters +
// trailing number.
var callsignRe = regexp.MustCompile(`^[a-z]+-[a-z]+-[0-9]{2}$`)

// TestGenerateStationShape: an auto-generated station is a friendly callsign that
// reveals nothing about the host (no hostname token, no port). Two draws differ with
// overwhelming probability (crypto/rand over a wide combo space).
func TestGenerateStationShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		s := GenerateStation()
		if !callsignRe.MatchString(s) {
			t.Fatalf("generated station %q is not a friendly callsign", s)
		}
		for _, bad := range leakSignals {
			if strings.Contains(s, bad) {
				t.Fatalf("generated station %q leaked %q", s, bad)
			}
		}
		seen[s] = true
	}
	if len(seen) < 20 {
		t.Errorf("station generation looks non-random: only %d distinct in 200 draws", len(seen))
	}
}

// TestSlugStation: the exported slug used by the CLI/TUI matches the node-id slug, and
// a blank input returns "" (callers then auto-generate, unlike ShareNodeID).
func TestSlugStation(t *testing.T) {
	cases := map[string]string{
		"Brave Otter":  "brave-otter",
		"  swift-fox ": "swift-fox",
		"a..b__c":      "a-b-c",
		"___":          "",
		"":             "",
	}
	for in, want := range cases {
		if got := SlugStation(in); got != want {
			t.Errorf("SlugStation(%q) = %q, want %q", in, got, want)
		}
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
