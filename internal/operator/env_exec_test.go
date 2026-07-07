package operator

// env_exec_test.go - table tests for the seams the TUI composes a child from: the REAL
// DefaultEnv (a genuine LookPath + a bounded `--version` probe against a real script),
// ComposeEnv's override-not-merge contract, Command's cwd/env composition, and the
// Materialize/newScratchDir failure paths. Real dependencies only (real filesystem, a
// real spawned probe process), stdlib testing per repo convention.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestDefaultEnvRealProbe: DefaultEnv wires a real exec.LookPath and a real bounded
// probe - proven against an actual executable that answers --version.
func TestDefaultEnvRealProbe(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakeguest")
	script := "#!/bin/sh\n[ \"$1\" = --version ] && echo 9.9.9\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := DefaultEnv()

	t.Run("LookPath resolves via the real PATH", func(t *testing.T) {
		t.Setenv("PATH", dir)
		p, err := env.LookPath("fakeguest")
		if err != nil || p != fake {
			t.Fatalf("LookPath = (%q, %v), want %q", p, err, fake)
		}
		if _, err := env.LookPath("no-such-guest-on-earth"); err == nil {
			t.Fatalf("a missing binary must miss")
		}
	})

	t.Run("Probe runs --version for real", func(t *testing.T) {
		out, err := env.Probe(fake)
		if err != nil || strings.TrimSpace(out) != "9.9.9" {
			t.Fatalf("Probe = (%q, %v), want 9.9.9", out, err)
		}
	})

	t.Run("Probe kills a genuinely hung binary at the deadline", func(t *testing.T) {
		hung := filepath.Join(dir, "hungguest")
		if err := os.WriteFile(hung, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		saved := ProbeTimeout
		ProbeTimeout = 150 * time.Millisecond
		defer func() { ProbeTimeout = saved }()
		start := time.Now()
		_, err := DefaultEnv().Probe(hung) // rebuild so the probe closes over the small deadline
		if err == nil {
			t.Fatalf("a hung --version must error at the deadline")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("the probe did not kill the hung binary promptly (took %v)", elapsed)
		}
	})

	t.Run("Probe surfaces a failing binary as an error", func(t *testing.T) {
		bad := filepath.Join(dir, "brokenguest")
		if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := env.Probe(bad); err == nil {
			t.Fatalf("a probe exiting non-zero must error (degrades to UNVERIFIED)")
		}
	})
}

// TestComposeEnvOverridesNotMerges: an addition RE-DECLARING a parent var replaces it -
// the user's real OPENAI_API_KEY must never survive into the child (config_aider.feature).
func TestComposeEnvOverridesNotMerges(t *testing.T) {
	parent := []string{"HOME=/home/u", "OPENAI_API_KEY=sk-users-real-key", "PATH=/usr/bin"}
	adds := []string{"OPENAI_API_KEY=sk-session", "OPENAI_API_BASE=http://127.0.0.1:1/v1"}
	got := ComposeEnv(parent, adds)
	want := []string{"HOME=/home/u", "PATH=/usr/bin", "OPENAI_API_KEY=sk-session", "OPENAI_API_BASE=http://127.0.0.1:1/v1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ComposeEnv = %v, want %v", got, want)
	}
	// Malformed parent entries (no '=') pass through untouched rather than crashing.
	got = ComposeEnv([]string{"JUNK", "A=1"}, []string{"A=2"})
	if !reflect.DeepEqual(got, []string{"JUNK", "A=2"}) {
		t.Fatalf("malformed parent entries must pass through, got %v", got)
	}
}

// TestCommandComposition: the child runs the RESOLVED binary in the USER'S workdir
// (never the scratch dir) with the composed env.
func TestCommandComposition(t *testing.T) {
	l := Launch{Argv: []string{"opencode", "-m", "roger/m"}, Env: []string{"K=v"}, Dir: "/scratch/x"}
	c := Command(l, "/resolved/opencode", "/work/project", []string{"HOME=/h", "K=old"})
	if c.Path != "/resolved/opencode" {
		t.Fatalf("path = %q, want the resolved binary", c.Path)
	}
	if !reflect.DeepEqual(c.Args, []string{"/resolved/opencode", "-m", "roger/m"}) {
		t.Fatalf("args = %v", c.Args)
	}
	if c.Dir != "/work/project" {
		t.Fatalf("dir = %q, want the launch workdir (never the scratch dir)", c.Dir)
	}
	if !reflect.DeepEqual(c.Env, []string{"HOME=/h", "K=v"}) {
		t.Fatalf("env = %v, want the composed override", c.Env)
	}
}

// TestMaterializeUnknownStrategy: a registry bug (unset strategy) refuses loudly rather
// than launching an unwired guest.
func TestMaterializeUnknownStrategy(t *testing.T) {
	s := Session{BaseURL: "http://x/v1", SessionKey: "k", Model: "m",
		Workdir: t.TempDir(), ScratchRoot: t.TempDir()}
	if _, _, err := Materialize(Guest{Name: "mystery", Bin: "mystery"}, s); err == nil {
		t.Fatalf("an unknown wiring strategy must refuse to materialize")
	}
}

// TestMaterializeScratchRootFailure: an unusable scratch root fails cleanly (no partial
// dirs, no panic) for both file-backed strategies.
func TestMaterializeScratchRootFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	root := filepath.Join(t.TempDir(), "sealed")
	if err := os.Mkdir(root, 0o500); err != nil { // read+exec only: MkdirTemp must fail
		t.Fatal(err)
	}
	s := Session{BaseURL: "http://x/v1", SessionKey: "k", Model: "m",
		Workdir: t.TempDir(), ScratchRoot: root}
	for _, name := range []string{"opencode", "hermes"} {
		if _, _, err := Materialize(guest(t, name), s); err == nil {
			t.Fatalf("%s: an unwritable scratch root must fail the materialize", name)
		}
	}
}

// TestMaterializeDefaultScratchRoot: an empty ScratchRoot falls back to os.TempDir()
// (the production default the TUI relies on).
func TestMaterializeDefaultScratchRoot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	s := Session{BaseURL: "http://x/v1", SessionKey: "k", Model: "m", Workdir: t.TempDir()}
	l, cleanup, err := Materialize(guest(t, "opencode"), s)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	defer func() { _ = cleanup() }()
	if !strings.HasPrefix(l.Dir, tmp) {
		t.Fatalf("scratch dir %s not under os.TempDir() %s", l.Dir, tmp)
	}
}

// TestSweepStaleEdgeCases: a missing root sweeps nothing; stale FILES (not dirs) with
// the prefix are left alone; the age boundary is respected.
func TestSweepStaleEdgeCases(t *testing.T) {
	if got := SweepStale(filepath.Join(t.TempDir(), "nope"), time.Hour); got != 0 {
		t.Fatalf("missing root: swept %d, want 0", got)
	}
	root := t.TempDir()
	f := filepath.Join(root, "rogerai-operator-file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(f, old, old); err != nil {
		t.Fatal(err)
	}
	if got := SweepStale(root, 24*time.Hour); got != 0 {
		t.Fatalf("a stale FILE must not be swept (dirs only), swept %d", got)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("the file was removed: %v", err)
	}
}

// TestVersionBelowTable: numeric dot-segment comparison across lengths and boundaries.
func TestVersionBelowTable(t *testing.T) {
	cases := []struct {
		v, floor string
		want     bool
	}{
		{"0.9.0", "1.17.11", true},
		{"1.17.11", "1.17.11", false},
		{"1.17.12", "1.17.11", false},
		{"2.0", "1.17.11", false},
		{"1.17", "1.17.11", true}, // missing segment counts as 0
		{"1.17.11", "", false},    // no floor: nothing is below
		{"10.0.0", "9.9.9", false},
	}
	for _, tc := range cases {
		if got := versionBelow(tc.v, tc.floor); got != tc.want {
			t.Fatalf("versionBelow(%q, %q) = %v, want %v", tc.v, tc.floor, got, tc.want)
		}
	}
}

// TestParseVersionEdge: dotted-token discipline - punctuation-wrapped or non-numeric
// tokens never parse as a version.
func TestParseVersionEdge(t *testing.T) {
	cases := []struct {
		raw  string
		ok   bool
		want string
	}{
		{"v1.2.3", true, "1.2.3"},
		{"version: 1.2", true, "1.2"},
		{"1.", false, ""},
		{".1", false, ""},
		{"1", false, ""},
		{"1.2a.3", false, ""},
		{"built (2026.6.5)", false, ""}, // parens keep the token from parsing
	}
	for _, tc := range cases {
		got, ok := ParseVersion("any", tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("ParseVersion(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

// TestMaterializeRejectsUnsafeValues: audit regression - Model/BaseURL are interpolated
// into JSON/YAML templates, so a value carrying quotes/newlines/control bytes (or the
// YAML ": " hazard) must REFUSE to materialize (fail-closed) rather than emit a broken
// or injectable config. Band values from the broker never contain these; a value that
// does is hostile or corrupt.
func TestMaterializeRejectsUnsafeValues(t *testing.T) {
	base := func() Session {
		return Session{BaseURL: "http://127.0.0.1:1/v1", SessionKey: "k", Model: "m",
			Workdir: t.TempDir(), ScratchRoot: t.TempDir()}
	}
	cases := []struct {
		name string
		mut  func(*Session)
	}{
		{"newline in model", func(s *Session) { s.Model = "m\nproviders: {}" }},
		{"quote in model", func(s *Session) { s.Model = `m"},"x":{` }},
		{"newline in base URL", func(s *Session) { s.BaseURL = "http://x/v1\napi_key: stolen" }},
		{"backslash in model", func(s *Session) { s.Model = `m\"` }},
		{"yaml colon-space in model", func(s *Session) { s.Model = "m: evil" }},
		{"yaml comment hash in model", func(s *Session) { s.Model = "m #evil" }}, // " #" starts a YAML comment in a plain scalar
		{"leading hash in model", func(s *Session) { s.Model = "#evil" }},        // "default: #evil" comments the value out - the key silently nulls (audit finding)
		{"control byte in base URL", func(s *Session) { s.BaseURL = "http://x/v1\x07" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"opencode", "hermes", "aider"} {
				s := base()
				tc.mut(&s)
				if _, cleanup, err := Materialize(guest(t, name), s); err == nil {
					_ = cleanup()
					t.Fatalf("%s: unsafe %s must refuse to materialize", name, tc.name)
				}
			}
		})
	}
}
