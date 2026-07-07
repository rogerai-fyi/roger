package operator

// registry_test.go — RED-phase spec tests for the Guest Operators Phase 2 detection
// registry (features/operator/detection.feature made table-executable). NO production
// code exists yet: this package intentionally fails to compile ("undefined: Registry",
// "undefined: Detect", …) — that build failure is the RED evidence for the not-yet-built
// unit, pasted in the approval report. Real deps only: the Env seam is the
// internal/audio/audio.go:35 pattern (injected LookPath + bounded Probe), no mocks.
// Stdlib testing only (repo convention pinned in internal/client/proxy_hardening_test.go:21).
//
// Proposed API under test (to be implemented ONLY after founder approval):
//
//	type Guest struct{ Name, Bin, Provider, InstallHint, KnownGood string }
//	type Detection struct{ Guest Guest; Path, Version string; Unverified bool }
//	type Env struct {
//	    LookPath func(string) (string, error)
//	    Probe    func(bin string) (string, error) // runs `bin --version`, bounded
//	}
//	func Registry() []Guest
//	func Detect(env Env) []Detection
//	func ParseVersion(guest, raw string) (version string, ok bool)

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// notFound mimics exec.LookPath's miss for every binary.
func notFound(string) (string, error) { return "", errors.New("executable file not found in $PATH") }

// lookup returns a LookPath seam resolving only the given name->path pairs.
func lookup(paths map[string]string) func(string) (string, error) {
	return func(bin string) (string, error) {
		if p, ok := paths[bin]; ok {
			return p, nil
		}
		return "", errors.New("executable file not found in $PATH")
	}
}

func detectionNames(ds []Detection) []string {
	var names []string
	for _, d := range ds {
		names = append(names, d.Guest.Name)
	}
	return names
}

// TestRegistryMVPSet: the registry is exactly the §4-proven MVP set, in order, with
// claude/codex excluded and every field populated.
func TestRegistryMVPSet(t *testing.T) {
	reg := Registry()
	var names []string
	for _, g := range reg {
		names = append(names, g.Name)
	}
	if want := []string{"opencode", "hermes", "aider"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("registry must be the MVP set in order %v, got %v", want, names)
	}
	for _, g := range reg {
		if g.Name == "claude" || g.Name == "codex" {
			t.Fatalf("claude/codex are EXCLUDED v1, registry lists %q", g.Name)
		}
		if g.Bin == "" || g.Provider == "" || g.InstallHint == "" {
			t.Fatalf("%s: every entry carries bin/provider/install hint, got %+v", g.Name, g)
		}
	}
}

// TestRegistryWiringStrategies pins each entry's empirically-proven wiring identity.
func TestRegistryWiringStrategies(t *testing.T) {
	byName := map[string]Guest{}
	for _, g := range Registry() {
		byName[g.Name] = g
	}
	if v := byName["opencode"].KnownGood; v != "1.17.11" {
		t.Fatalf("opencode known-good must pin 1.17.11 (dev box, 2026-07-06), got %q", v)
	}
	if v := byName["hermes"].KnownGood; v != "0.16.0" {
		t.Fatalf("hermes known-good must pin 0.16.0 (dev box, 2026-07-06), got %q", v)
	}
	// aider: no known-good pin yet — not installed on the dev box; founder ruling pending.
	for _, n := range []string{"opencode", "hermes", "aider"} {
		if p := byName[n].Provider; p != "openai" {
			t.Fatalf("%s provider tag must be openai (OpenAI-compatible wire), got %q", n, p)
		}
	}
}

// TestDetect is the found / not-found / PATH-edge / version table
// (detection.feature scenarios 3-13).
func TestDetect(t *testing.T) {
	allPaths := map[string]string{
		"opencode": "/home/u/.opencode/bin/opencode",
		"hermes":   "/home/u/.local/bin/hermes",
		"aider":    "/home/u/.local/bin/aider",
	}
	versionOK := func(bin string) (string, error) {
		switch bin {
		case allPaths["opencode"]:
			return "1.17.11\n", nil
		case allPaths["hermes"]:
			return "Hermes Agent v0.16.0 (2026.6.5) · upstream 9688c1a9", nil
		case allPaths["aider"]:
			return "aider 0.86.1", nil
		}
		return "", errors.New("unknown bin")
	}

	cases := []struct {
		name       string
		env        Env
		wantNames  []string
		unverified map[string]bool // name -> expected Unverified flag (nil = not asserted)
		versions   map[string]string
	}{
		{
			name:      "all three on PATH, registry order, versions verified",
			env:       Env{LookPath: lookup(allPaths), Probe: versionOK},
			wantNames: []string{"opencode", "hermes", "aider"},
			versions:  map[string]string{"opencode": "1.17.11", "hermes": "0.16.0", "aider": "0.86.1"},
		},
		{
			name:      "missing guests are absent, never an error",
			env:       Env{LookPath: lookup(map[string]string{"opencode": allPaths["opencode"]}), Probe: versionOK},
			wantNames: []string{"opencode"},
		},
		{
			name:      "empty PATH detects nothing",
			env:       Env{LookPath: notFound, Probe: versionOK},
			wantNames: nil,
		},
		{
			name: "exists but not executable is not at the desk (LookPath contract)",
			env: Env{LookPath: func(bin string) (string, error) {
				if bin == "opencode" {
					return "", errors.New("permission denied")
				}
				return "", errors.New("executable file not found in $PATH")
			}, Probe: versionOK},
			wantNames: nil,
		},
		{
			name: "failed version probe degrades to UNVERIFIED, never hidden",
			env: Env{LookPath: lookup(map[string]string{"opencode": allPaths["opencode"]}),
				Probe: func(string) (string, error) { return "", errors.New("exit status 1") }},
			wantNames:  []string{"opencode"},
			unverified: map[string]bool{"opencode": true},
		},
		{
			name: "garbage version output degrades to UNVERIFIED",
			env: Env{LookPath: lookup(map[string]string{"hermes": allPaths["hermes"]}),
				Probe: func(string) (string, error) { return "Traceback (most recent call last): ...", nil }},
			wantNames:  []string{"hermes"},
			unverified: map[string]bool{"hermes": true},
		},
		{
			name: "version below the known-good floor is detected but unverified",
			env: Env{LookPath: lookup(map[string]string{"opencode": "/usr/bin/opencode"}),
				Probe: func(string) (string, error) { return "0.9.0", nil }},
			wantNames:  []string{"opencode"},
			unverified: map[string]bool{"opencode": true},
			versions:   map[string]string{"opencode": "0.9.0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.env)
			if names := detectionNames(got); !reflect.DeepEqual(names, tc.wantNames) {
				t.Fatalf("detections: want %v, got %v", tc.wantNames, names)
			}
			for _, d := range got {
				if tc.unverified != nil && d.Unverified != tc.unverified[d.Guest.Name] {
					t.Fatalf("%s: Unverified=%v, want %v", d.Guest.Name, d.Unverified, tc.unverified[d.Guest.Name])
				}
				if want, ok := tc.versions[d.Guest.Name]; ok && d.Version != want {
					t.Fatalf("%s: version %q, want %q", d.Guest.Name, d.Version, want)
				}
			}
		})
	}
}

// TestDetectRecordsResolvedPath: each detection carries the LookPath result so the
// picker/plate can show WHICH binary will run.
func TestDetectRecordsResolvedPath(t *testing.T) {
	got := Detect(Env{
		LookPath: lookup(map[string]string{"opencode": "/home/u/.opencode/bin/opencode"}),
		Probe:    func(string) (string, error) { return "1.17.11", nil },
	})
	if len(got) != 1 || got[0].Path != "/home/u/.opencode/bin/opencode" {
		t.Fatalf("detection must record the resolved path, got %+v", got)
	}
}

// TestDetectRescanIsStateless: Detect holds no cache — a changed PATH is reflected on
// the next call (the picker's `r` re-scan).
func TestDetectRescanIsStateless(t *testing.T) {
	probe := func(string) (string, error) { return "aider 0.86.1", nil }
	if got := Detect(Env{LookPath: notFound, Probe: probe}); len(got) != 0 {
		t.Fatalf("empty PATH must detect nothing, got %v", detectionNames(got))
	}
	got := Detect(Env{LookPath: lookup(map[string]string{"aider": "/home/u/.local/bin/aider"}), Probe: probe})
	if names := detectionNames(got); !reflect.DeepEqual(names, []string{"aider"}) {
		t.Fatalf("re-scan must reflect the changed PATH, got %v", names)
	}
}

// TestDetectBoundedProbe: a wedged `--version` cannot hang the scan — the Probe seam is
// bounded by the caller contract; Detect must complete promptly when the probe errors at
// its deadline (the seam returns the timeout error; Detect never retries or blocks).
func TestDetectBoundedProbe(t *testing.T) {
	start := time.Now()
	got := Detect(Env{
		LookPath: lookup(map[string]string{"hermes": "/home/u/.local/bin/hermes"}),
		Probe:    func(string) (string, error) { return "", errors.New("probe deadline exceeded") },
	})
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("the scan must not block, took %v", elapsed)
	}
	if len(got) != 1 || !got[0].Unverified {
		t.Fatalf("a deadline-exceeded probe degrades to unverified, got %+v", got)
	}
}

// TestParseVersion is the raw-output table across the real formats observed on the dev
// box (detection.feature scenario outline).
func TestParseVersion(t *testing.T) {
	cases := []struct {
		guest, raw, want string
		ok               bool
	}{
		{"opencode", "1.17.11", "1.17.11", true},
		{"opencode", "1.17.11\n", "1.17.11", true},
		{"hermes", "Hermes Agent v0.16.0 (2026.6.5) · upstream 9688c1a9", "0.16.0", true},
		{"aider", "aider 0.86.1", "0.86.1", true},
		{"opencode", "", "", false},
		{"hermes", "Traceback (most recent call last): ...", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.guest+"/"+tc.raw, func(t *testing.T) {
			got, ok := ParseVersion(tc.guest, tc.raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("ParseVersion(%q, %q) = (%q, %v), want (%q, %v)", tc.guest, tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestReservedRCOperatorKinds pins the Phase 2 wire-name reservations
// (rc_interlock.feature "reserved operator wire names"): documented constants in
// internal/protocol/rc.go, NO behavior attached in v1. Asserted here (not in a protocol
// test file) so the RED compile failure stays contained to this new package.
func TestReservedRCOperatorKinds(t *testing.T) {
	if protocol.RCKindOperatorStatus != "operator_status" {
		t.Fatalf("RCKindOperatorStatus must reserve \"operator_status\"")
	}
	if protocol.RCInOperatorHandoff != "operator_handoff" {
		t.Fatalf("RCInOperatorHandoff must reserve \"operator_handoff\"")
	}
	if protocol.RCInOperatorRecall != "operator_recall" {
		t.Fatalf("RCInOperatorRecall must reserve \"operator_recall\"")
	}
}
