package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func guestNamed(t *testing.T, name string) Guest {
	t.Helper()
	for _, g := range Registry() {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("%s is not in the registry", name)
	return Guest{}
}

func liveSession(t *testing.T) Session {
	t.Helper()
	return Session{
		SessionKey:  "rog-sess-abc123",
		BaseURL:     "http://127.0.0.1:4141/v1",
		Model:       "qwen3.8-27b",
		ScratchRoot: t.TempDir(),
	}
}

// The bug this whole change exists to stop: `dsh` shared opencode's strategy CONSTANT and
// therefore silently got opencode's config FORMAT - a models file it cannot read, an env
// var it does not consult, and an `-m` flag it does not accept. It answered
// `error: --profile <name> is required` and had never once worked.
//
// A shared strategy is not a shared format. A guest with no recipe must refuse.
func TestScratchConfigGuestWithNoRecipeRefuses(t *testing.T) {
	mystery := Guest{Name: "not-a-real-guest", Bin: "nope", Provider: "openai", Strategy: StrategyScratchConfig}
	_, _, err := Materialize(mystery, liveSession(t))
	if err == nil {
		t.Fatal("a scratch-config guest with no recipe was materialized - it would have run with another guest's wiring")
	}
	if !strings.Contains(err.Error(), "no config recipe") {
		t.Fatalf("the error does not name the cause: %v", err)
	}
}

// The refusal must not leave the scratch dir behind. A dir created and abandoned on every
// rejected launch is a slow leak in the one place that holds credentials.
func TestRefusedScratchConfigLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	mystery := Guest{Name: "not-a-real-guest", Bin: "nope", Provider: "openai", Strategy: StrategyScratchConfig}
	s := liveSession(t)
	s.ScratchRoot = root
	if _, _, err := Materialize(mystery, s); err == nil {
		t.Fatal("expected a refusal")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refusal left %d entries in the scratch root: %v", len(entries), entries)
	}
}

// dsh is gated rather than removed: it is a real CLI the operator may have installed, and
// the desk should say why it cannot take the mic instead of pretending it is not there.
// The gate is what stops it exec'ing with a wiring that has never worked.
func TestDshIsGatedNotSilentlyBroken(t *testing.T) {
	dsh := guestNamed(t, "dsh")
	if !dsh.NeedsSetup {
		t.Fatal("dsh must be gated: its wiring launches `dsh -m roger/<model>`, which dsh rejects")
	}
	if strings.TrimSpace(dsh.SetupNote) == "" {
		t.Fatal("a gated guest must say what the operator can do instead")
	}
	// The note has to point at the real mechanism, or it is just an apology.
	for _, want := range []string{"profile", "settings.yaml", "apiKeyEnv"} {
		if !strings.Contains(dsh.SetupNote, want) {
			t.Errorf("the setup note does not mention %q, so it cannot actually be followed", want)
		}
	}
}

// pi's generated catalog must be VALID JSON matching pi's provider schema, carry the band
// verbatim, and be the only provider present - a user layer left visible is a route the
// operator did not choose.
func TestPiConfigIsAValidSingleProviderCatalog(t *testing.T) {
	pi := guestNamed(t, "pi")
	s := liveSession(t)
	l, cleanup, err := Materialize(pi, s)
	if err != nil {
		t.Fatalf("materialize pi: %v", err)
	}
	defer cleanup()

	var agentDir string
	for _, e := range l.Env {
		if strings.HasPrefix(e, piAgentDirEnv+"=") {
			agentDir = strings.TrimPrefix(e, piAgentDirEnv+"=")
		}
	}
	if agentDir == "" {
		t.Fatalf("no %s in env: %v", piAgentDirEnv, l.Env)
	}

	raw, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var cfg struct {
		Providers map[string]struct {
			Name    string `json:"name"`
			API     string `json:"api"`
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("generated models.json is not valid JSON: %v\n%s", err, raw)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected exactly one provider, got %d: %v", len(cfg.Providers), cfg.Providers)
	}
	p, ok := cfg.Providers[piProviderName]
	if !ok {
		t.Fatalf("provider %q missing; got %v", piProviderName, cfg.Providers)
	}
	if p.BaseURL != s.BaseURL {
		t.Errorf("baseUrl = %q, want the band's %q", p.BaseURL, s.BaseURL)
	}
	if p.APIKey != s.SessionKey {
		t.Errorf("apiKey = %q, want the session key", p.APIKey)
	}
	if len(p.Models) != 1 || p.Models[0].ID != s.Model {
		t.Errorf("models = %v, want exactly the band's model %q", p.Models, s.Model)
	}
}

// The argv must pin BOTH provider and model. pi defaults to the google provider and
// accepts fuzzy model patterns, so an unpinned launch could reach a model - and an
// account - the operator never chose.
func TestPiArgvPinsProviderAndModel(t *testing.T) {
	pi := guestNamed(t, "pi")
	s := liveSession(t)
	l, cleanup, err := Materialize(pi, s)
	if err != nil {
		t.Fatalf("materialize pi: %v", err)
	}
	defer cleanup()

	argv := strings.Join(l.Argv, " ")
	if !strings.Contains(argv, "--provider "+piProviderName) {
		t.Errorf("argv does not pin the provider: %v", l.Argv)
	}
	if !strings.Contains(argv, "--model "+s.Model) {
		t.Errorf("argv does not pin the model: %v", l.Argv)
	}
}

// The provider name is used in two places that must agree - the JSON key and the --provider
// flag. They come from one constant precisely so they cannot drift, and this fails if
// someone re-hardcodes either.
func TestPiProviderNameIsUsedConsistently(t *testing.T) {
	pi := guestNamed(t, "pi")
	l, cleanup, err := Materialize(pi, liveSession(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	var agentDir string
	for _, e := range l.Env {
		if strings.HasPrefix(e, piAgentDirEnv+"=") {
			agentDir = strings.TrimPrefix(e, piAgentDirEnv+"=")
		}
	}
	raw, _ := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if !strings.Contains(string(raw), `"`+piProviderName+`"`) {
		t.Errorf("models.json does not use the shared provider constant")
	}
	if !strings.Contains(strings.Join(l.Argv, " "), piProviderName) {
		t.Errorf("argv does not use the shared provider constant")
	}
}

// The scratch dir holds a live session key in a world-readable filesystem. Same rule the
// other config-generating guests follow.
func TestPiConfigIsNotWorldReadable(t *testing.T) {
	pi := guestNamed(t, "pi")
	l, cleanup, err := Materialize(pi, liveSession(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	var agentDir string
	for _, e := range l.Env {
		if strings.HasPrefix(e, piAgentDirEnv+"=") {
			agentDir = strings.TrimPrefix(e, piAgentDirEnv+"=")
		}
	}
	fi, err := os.Stat(filepath.Join(agentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("models.json mode %v exposes the session key to other users", perm)
	}
}

// Cleanup must actually remove the tree - the config carries the key.
func TestPiScratchIsRemovedOnCleanup(t *testing.T) {
	pi := guestNamed(t, "pi")
	s := liveSession(t)
	l, cleanup, err := Materialize(pi, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(l.Dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir survived cleanup: %v", err)
	}
}
