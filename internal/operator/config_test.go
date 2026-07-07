package operator

// config_test.go — RED-phase spec tests for per-session throwaway config generation
// (features/operator/config_opencode|hermes|aider|isolation.feature made executable).
// NO production code exists yet — the package build failure is the RED evidence.
// REAL dependencies: a real filesystem via t.TempDir(), byte-exact golden artifacts,
// no mocks anywhere. Stdlib testing only (repo convention).
//
// Proposed API under test (implemented only after founder approval):
//
//	type Session struct{ BaseURL, SessionKey, Model, Workdir, ScratchRoot string }
//	type Launch struct {
//	    Argv []string // full child argv, argv[0] = the guest binary name
//	    Env  []string // KEY=VAL additions over the inherited parent env
//	    Dir  string   // the session scratch dir ("" when no file is generated)
//	}
//	func Materialize(g Guest, s Session) (Launch, func() error, error) // cleanup fn
//	func SweepStale(scratchRoot string, olderThan time.Duration) int

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	tBase  = "http://127.0.0.1:44017/v1"
	tKey   = "sk-test-0123"
	tModel = "qwen3-32b-fp8"
)

func guest(t *testing.T, name string) Guest {
	t.Helper()
	for _, g := range Registry() {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("guest %q not in registry", name)
	return Guest{}
}

func session(t *testing.T) Session {
	t.Helper()
	return Session{BaseURL: tBase, SessionKey: tKey, Model: tModel,
		Workdir: t.TempDir(), ScratchRoot: t.TempDir()}
}

// listFiles returns every regular file under root, relative paths, sorted.
func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// sameStringSet fails unless got and want hold the same elements (order-free).
func sameStringSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	g, w := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s: want %v, got %v", what, want, got)
	}
}

// ── opencode ──────────────────────────────────────────────────────────────────────────

// goldenOpencode is the byte-exact artifact (config_opencode.feature): the §4-proven
// custom provider on @ai-sdk/openai-compatible, key by {env:...} REFERENCE (verified
// supported in the 1.17.11 binary) so the secret never touches disk.
const goldenOpencode = `{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "roger": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "RogerAI",
      "options": {
        "baseURL": "http://127.0.0.1:44017/v1",
        "apiKey": "{env:ROGER_SESSION_KEY}"
      },
      "models": {
        "qwen3-32b-fp8": { "name": "qwen3-32b-fp8" }
      }
    }
  },
  "model": "roger/qwen3-32b-fp8"
}
`

func TestMaterializeOpencode(t *testing.T) {
	s := session(t)
	l, cleanup, err := Materialize(guest(t, "opencode"), s)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}()

	if got := listFiles(t, l.Dir); !reflect.DeepEqual(got, []string{"opencode.json"}) {
		t.Fatalf("exactly one generated file, got %v", got)
	}
	b, err := os.ReadFile(filepath.Join(l.Dir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != goldenOpencode {
		t.Fatalf("golden artifact mismatch:\n--- want ---\n%s\n--- got ---\n%s", goldenOpencode, b)
	}

	wantArgv := []string{"opencode", "-m", "roger/qwen3-32b-fp8"}
	if !reflect.DeepEqual(l.Argv, wantArgv) {
		t.Fatalf("argv must pin the model so no config layer (project opencode.json) can re-route the guest: want %v, got %v", wantArgv, l.Argv)
	}
	sameStringSet(t, "opencode env additions", l.Env, []string{
		"OPENCODE_CONFIG=" + filepath.Join(l.Dir, "opencode.json"),
		"ROGER_SESSION_KEY=" + tKey,
	})
	if !strings.HasPrefix(filepath.Base(l.Dir), "rogerai-operator-") {
		t.Fatalf("scratch dir naming rogerai-operator-*, got %s", l.Dir)
	}
	if !strings.HasPrefix(l.Dir, s.ScratchRoot) {
		t.Fatalf("scratch dir must live under the scratch root, got %s", l.Dir)
	}
}

// ── hermes ────────────────────────────────────────────────────────────────────────────

// goldenHermes: scratch HERMES_HOME config.yaml using the keyed providers schema — the
// ONE hermes-0.16.0 path that delivers an api_key to a loopback base_url (model_switch.py
// :900-931 expands ${ROGER_SESSION_KEY} from the env). A bare model_aliases entry would
// resolve to "no-key-required" and 401 against the Phase 1 bearer proxy (the regression
// pinned in TestHermesNeverBareModelAliases).
const goldenHermes = `providers:
  roger:
    base_url: http://127.0.0.1:44017/v1
    api_key: ${ROGER_SESSION_KEY}
model:
  provider: roger
  default: qwen3-32b-fp8
`

func TestMaterializeHermes(t *testing.T) {
	s := session(t)
	l, cleanup, err := Materialize(guest(t, "hermes"), s)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}()

	b, err := os.ReadFile(filepath.Join(l.Dir, "hermes-home", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != goldenHermes {
		t.Fatalf("golden artifact mismatch:\n--- want ---\n%s\n--- got ---\n%s", goldenHermes, b)
	}

	wantArgv := []string{"hermes", "-m", "roger/qwen3-32b-fp8"}
	if !reflect.DeepEqual(l.Argv, wantArgv) {
		t.Fatalf("argv: want %v, got %v", wantArgv, l.Argv)
	}
	sameStringSet(t, "hermes env additions", l.Env, []string{
		"HERMES_HOME=" + filepath.Join(l.Dir, "hermes-home"),
		"ROGER_SESSION_KEY=" + tKey,
	})
}

func TestHermesNeverBareModelAliases(t *testing.T) {
	s := session(t)
	l, cleanup, err := Materialize(guest(t, "hermes"), s)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	defer func() { _ = cleanup() }()
	b, err := os.ReadFile(filepath.Join(l.Dir, "hermes-home", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "providers:") || !strings.Contains(string(b), "api_key: ${ROGER_SESSION_KEY}") {
		t.Fatalf("hermes wiring must be the keyed providers schema with the key by env reference (a bare model_aliases entry 401s against the Phase 1 bearer proxy):\n%s", b)
	}
}

// ── aider ─────────────────────────────────────────────────────────────────────────────

func TestMaterializeAider(t *testing.T) {
	s := session(t)
	l, cleanup, err := Materialize(guest(t, "aider"), s)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup must be a no-op success for aider: %v", err)
		}
	}()

	wantArgv := []string{"aider",
		"--model", "openai/qwen3-32b-fp8",
		"--no-show-model-warnings",
		"--no-auto-commits",
	}
	if !reflect.DeepEqual(l.Argv, wantArgv) {
		t.Fatalf("§4 flag set verbatim (--no-auto-commits is a permanent safety pin): want %v, got %v", wantArgv, l.Argv)
	}
	sameStringSet(t, "aider env additions", l.Env, []string{
		"OPENAI_API_BASE=" + tBase,
		"OPENAI_API_KEY=" + tKey,
	})
	if l.Dir != "" {
		t.Fatalf("aider generates NO file — no scratch dir at all (minimization), got %s", l.Dir)
	}
	if got := listFiles(t, s.ScratchRoot); len(got) != 0 {
		t.Fatalf("nothing written anywhere for aider, got %v", got)
	}
}

// ── cross-guest isolation invariants ─────────────────────────────────────────────────

// TestNeverWriteOutsideScratch: every byte lands under the session scratch dir; the
// user's workdir (including a project opencode.json) stays byte-identical
// (config_isolation.feature).
func TestNeverWriteOutsideScratch(t *testing.T) {
	for _, name := range []string{"opencode", "hermes", "aider"} {
		t.Run(name, func(t *testing.T) {
			s := session(t)
			project := filepath.Join(s.Workdir, "opencode.json")
			sentinel := []byte(`{"model":"anthropic/claude-opus-4"}`)
			if err := os.WriteFile(project, sentinel, 0o644); err != nil {
				t.Fatal(err)
			}
			before := listFiles(t, s.Workdir)

			l, cleanup, err := Materialize(guest(t, name), s)
			if err != nil {
				t.Fatalf("materialize: %v", err)
			}
			defer func() { _ = cleanup() }()

			if after := listFiles(t, s.Workdir); !reflect.DeepEqual(before, after) {
				t.Fatalf("the user's workdir must be untouched: before %v, after %v", before, after)
			}
			if b, _ := os.ReadFile(project); !reflect.DeepEqual(b, sentinel) {
				t.Fatalf("the user's project opencode.json was modified")
			}
			if l.Dir != "" && !strings.HasPrefix(l.Dir, s.ScratchRoot) {
				t.Fatalf("all writes stay under the scratch root, got %s", l.Dir)
			}
		})
	}
}

// TestSessionKeyNeverOnDisk: the bearer secret appears in Env only — never in any
// generated file (env-reference indirection for opencode/hermes, pure env for aider).
func TestSessionKeyNeverOnDisk(t *testing.T) {
	for _, name := range []string{"opencode", "hermes", "aider"} {
		t.Run(name, func(t *testing.T) {
			s := session(t)
			_, cleanup, err := Materialize(guest(t, name), s)
			if err != nil {
				t.Fatalf("materialize: %v", err)
			}
			defer func() { _ = cleanup() }()
			for _, f := range listFiles(t, s.ScratchRoot) {
				b, err := os.ReadFile(filepath.Join(s.ScratchRoot, f))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), tKey) {
					t.Fatalf("%s: the session key must NEVER touch disk", f)
				}
			}
		})
	}
}

// TestScratchDirPrivate: 0700 — other local users cannot even enumerate the session dir.
func TestScratchDirPrivate(t *testing.T) {
	s := session(t)
	l, cleanup, err := Materialize(guest(t, "opencode"), s)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	defer func() { _ = cleanup() }()
	info, err := os.Stat(l.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("scratch dir mode: want 0700, got %o", perm)
	}
}

// TestCleanupRemovesScratch: cleanup removes the whole dir on every return path, is
// idempotent, and tolerates a guest that deleted files itself.
func TestCleanupRemovesScratch(t *testing.T) {
	for _, name := range []string{"opencode", "hermes"} {
		t.Run(name, func(t *testing.T) {
			s := session(t)
			l, cleanup, err := Materialize(guest(t, name), s)
			if err != nil {
				t.Fatalf("materialize: %v", err)
			}
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
			if _, err := os.Stat(l.Dir); !os.IsNotExist(err) {
				t.Fatalf("scratch dir must be gone, stat err=%v", err)
			}
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup must be idempotent (crash-path re-entry): %v", err)
			}
		})
	}
	t.Run("guest deleted its own config", func(t *testing.T) {
		s := session(t)
		l, cleanup, err := Materialize(guest(t, "opencode"), s)
		if err != nil {
			t.Fatalf("materialize: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(l.Dir, "opencode.json")); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup must tolerate missing files: %v", err)
		}
		if _, err := os.Stat(l.Dir); !os.IsNotExist(err) {
			t.Fatalf("scratch dir must be gone, stat err=%v", err)
		}
	})
}

// TestTwoHandoffsNeverShareScratch: rapid re-handoff mints a fresh dir every time.
func TestTwoHandoffsNeverShareScratch(t *testing.T) {
	s := session(t)
	l1, c1, err := Materialize(guest(t, "opencode"), s)
	if err != nil {
		t.Fatalf("materialize #1: %v", err)
	}
	if err := c1(); err != nil {
		t.Fatalf("cleanup #1: %v", err)
	}
	l2, c2, err := Materialize(guest(t, "opencode"), s)
	if err != nil {
		t.Fatalf("materialize #2: %v", err)
	}
	defer func() { _ = c2() }()
	if l1.Dir == l2.Dir {
		t.Fatalf("two handoffs must never share a scratch dir: %s", l1.Dir)
	}
	if _, err := os.Stat(l1.Dir); !os.IsNotExist(err) {
		t.Fatalf("only the second scratch dir may exist, first still present")
	}
}

// TestSweepStale: a leftover dir from a crashed roger is swept; fresh dirs and foreign
// names are untouched (config_isolation.feature "swept at the next desk scan").
func TestSweepStale(t *testing.T) {
	root := t.TempDir()
	dead := filepath.Join(root, "rogerai-operator-dead1")
	live := filepath.Join(root, "rogerai-operator-live2")
	foreign := filepath.Join(root, "someone-elses-dir")
	for _, d := range []string{dead, live, foreign} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dead, old, old); err != nil {
		t.Fatal(err)
	}

	if swept := SweepStale(root, 24*time.Hour); swept != 1 {
		t.Fatalf("want 1 dir swept, got %d", swept)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("stale dir must be swept")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("fresh dir must be untouched: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign dirs are NEVER touched: %v", err)
	}
}

// TestMaterializeReadsLiveOptions: the artifact reflects the Session values passed at
// materialize time — the caller feeds ProxyOptionsHolder.Get() at exec, so a re-tuned
// band lands in the wiring (config_*.feature "LIVE proxy options").
func TestMaterializeReadsLiveOptions(t *testing.T) {
	s := session(t)
	s.Model = "llama-3.3-70b"
	l, cleanup, err := Materialize(guest(t, "opencode"), s)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	defer func() { _ = cleanup() }()
	if !strings.Contains(strings.Join(l.Argv, " "), "roger/llama-3.3-70b") {
		t.Fatalf("argv must pin the re-tuned model, got %v", l.Argv)
	}
	b, err := os.ReadFile(filepath.Join(l.Dir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "llama-3.3-70b") || strings.Contains(string(b), tModel) {
		t.Fatalf("config must wire the LIVE model, not a stale one:\n%s", b)
	}
}

// TestMaterializeRejectsEmptySession: money-path inputs are validated — an empty
// session key or base URL must refuse to materialize (a keyless launch would hand the
// guest an unauthenticated 401 wall; a URL-less one would fall back to the agent's real
// default provider, the exact claude-exclusion failure class).
func TestMaterializeRejectsEmptySession(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Session)
	}{
		{"empty key", func(s *Session) { s.SessionKey = "" }},
		{"empty base URL", func(s *Session) { s.BaseURL = "" }},
		{"empty model", func(s *Session) { s.Model = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := session(t)
			tc.mut(&s)
			if _, _, err := Materialize(guest(t, "opencode"), s); err == nil {
				t.Fatalf("materialize must refuse a session with %s", tc.name)
			}
		})
	}
}
