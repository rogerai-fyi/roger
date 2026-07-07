package operator

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// scratchPrefix names every per-handoff scratch dir (rogerai-operator-<random>) so the
// crash sweep can recognize its own leftovers and NEVER touch a foreign dir.
const scratchPrefix = "rogerai-operator-"

// SessionKeyEnv is the env var the generated configs reference the bearer secret by
// ({env:...} / ${...}); the key itself NEVER touches disk (config_isolation.feature).
const SessionKeyEnv = "ROGER_SESSION_KEY"

// Session is the live wiring a handoff materializes against - fed from
// ProxyOptionsHolder.Get() AT EXEC TIME (never options frozen at first bind).
type Session struct {
	BaseURL    string // the local proxy base, e.g. http://127.0.0.1:44017/v1
	SessionKey string // the per-session bearer secret (env-delivered, never written)
	Model      string // the tuned band's model
	Workdir    string // the user's confirmed workdir - the child's cwd, NEVER the scratch dir
	// ScratchRoot overrides where the session scratch dir is minted ("" = os.TempDir()).
	ScratchRoot string
}

// Launch is a composed child launch: the full argv (argv[0] = the guest binary name),
// the env ADDITIONS over the inherited parent env, and the session scratch dir ("" when
// the guest needs no file at all - aider).
type Launch struct {
	Argv []string
	Env  []string
	Dir  string
}

// goldenOpencodeTmpl is the §4-proven custom provider on @ai-sdk/openai-compatible. The
// apiKey is the literal {env:ROGER_SESSION_KEY} reference (verified supported in the
// 1.17.11 binary) so the secret never lands on disk.
const goldenOpencodeTmpl = `{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "roger": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "RogerAI",
      "options": {
        "baseURL": "%s",
        "apiKey": "{env:%s}"
      },
      "models": {
        "%s": { "name": "%s" }
      }
    }
  },
  "model": "roger/%s"
}
`

// goldenHermesTmpl is the KEYED providers schema - the ONE hermes-0.16.0 path that
// delivers an api_key to a loopback base_url (model_switch.py:900-931 expands ${VAR}
// from the env). A bare model_aliases entry resolves to "no-key-required" on loopback
// and 401s against the Phase 1 bearer proxy (permanent regression, config_hermes.feature).
const goldenHermesTmpl = `providers:
  roger:
    base_url: %s
    api_key: ${%s}
model:
  provider: roger
  default: %s
`

// Materialize composes the launch for guest g against the live session s: argv, env
// additions, and (for the file-backed strategies) a fresh private scratch dir holding the
// generated config. The returned cleanup removes the whole scratch dir; it is idempotent,
// tolerates a guest that deleted files itself, and MUST run on every return path (clean,
// crash, spawn failure). Money-path inputs are validated: an empty key would hand the
// guest a 401 wall, an empty base URL/model would fall back to the agent's real default
// provider - the exact claude-exclusion failure class.
func Materialize(g Guest, s Session) (Launch, func() error, error) {
	if s.SessionKey == "" || s.BaseURL == "" || s.Model == "" {
		return Launch{}, nil, fmt.Errorf("operator: refusing to materialize %s: missing %s", g.Name, describeMissing(s))
	}
	// Fail-closed value validation (audit regression): Model/BaseURL are interpolated
	// into the JSON/YAML templates below, so quotes/backslashes/control bytes (or the
	// YAML ": " hazard) would produce a broken - or injectable - config. Broker band
	// values never contain these; a value that does is corrupt or hostile.
	for _, v := range []string{s.Model, s.BaseURL} {
		if !safeConfigValue(v) {
			return Launch{}, nil, fmt.Errorf("operator: refusing to materialize %s: unsafe characters in model/base URL", g.Name)
		}
	}
	noop := func() error { return nil }
	switch g.Strategy {
	case StrategyEnvFlags:
		// aider: pure env + flags, ZERO generated files - no scratch dir is created at all
		// (minimization: nothing to leak on crash). --no-auto-commits is a permanent SAFETY
		// pin (a guest must never commit to the user's repo on its own);
		// --no-show-model-warnings suppresses the unknown-model wall for the band's model.
		return Launch{
			Argv: []string{g.Bin, "--model", "openai/" + s.Model, "--no-show-model-warnings", "--no-auto-commits"},
			Env:  []string{"OPENAI_API_BASE=" + s.BaseURL, "OPENAI_API_KEY=" + s.SessionKey},
		}, noop, nil

	case StrategyScratchConfig:
		dir, err := newScratchDir(s.ScratchRoot)
		if err != nil {
			return Launch{}, nil, err
		}
		cfg := filepath.Join(dir, "opencode.json")
		body := fmt.Sprintf(goldenOpencodeTmpl, s.BaseURL, SessionKeyEnv, s.Model, s.Model, s.Model)
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return Launch{}, nil, err
		}
		return Launch{
			// The argv -m pin beats EVERY config layer: a user project's own opencode.json
			// loads AFTER OPENCODE_CONFIG in 1.17.11 and could otherwise silently re-route
			// the guest (config_opencode.feature precedence hazard).
			Argv: []string{g.Bin, "-m", "roger/" + s.Model},
			Env:  []string{"OPENCODE_CONFIG=" + cfg, SessionKeyEnv + "=" + s.SessionKey},
			Dir:  dir,
		}, cleanupFn(dir), nil

	case StrategyScratchHome:
		dir, err := newScratchDir(s.ScratchRoot)
		if err != nil {
			return Launch{}, nil, err
		}
		home := filepath.Join(dir, "hermes-home")
		if err := os.Mkdir(home, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return Launch{}, nil, err
		}
		body := fmt.Sprintf(goldenHermesTmpl, s.BaseURL, SessionKeyEnv, s.Model)
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return Launch{}, nil, err
		}
		return Launch{
			Argv: []string{g.Bin, "-m", "roger/" + s.Model},
			Env:  []string{"HERMES_HOME=" + home, SessionKeyEnv + "=" + s.SessionKey},
			Dir:  dir,
		}, cleanupFn(dir), nil
	}
	return Launch{}, nil, errors.New("operator: unknown wiring strategy for " + g.Name)
}

// safeConfigValue reports whether v can be interpolated verbatim into the generated
// JSON/YAML configs: it must START alphanumeric (a leading YAML indicator - "#" comment,
// "&" anchor, "*" alias, "-" sequence, etc. - silently nulls or hijacks the key:
// fail-open, the class two pre-push audits flagged), with no control bytes (incl.
// newlines), no quotes/backslashes/backticks, and no in-value YAML plain-scalar hazards
// (": " starts a mapping, " #" starts a comment). Broker band values (model slugs,
// http(s) base URLs) always start alphanumeric.
func safeConfigValue(v string) bool {
	if v == "" {
		return false // Materialize rejects empties earlier; fail closed here too
	}
	if c := v[0]; !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
		return false
	}
	if strings.Contains(v, ": ") || strings.Contains(v, " #") {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '"', '\'', '\\', '`':
			return false
		}
	}
	return true
}

// describeMissing names the empty money-path field(s) for the refusal error.
func describeMissing(s Session) string {
	var miss []string
	if s.SessionKey == "" {
		miss = append(miss, "session key")
	}
	if s.BaseURL == "" {
		miss = append(miss, "base URL")
	}
	if s.Model == "" {
		miss = append(miss, "model")
	}
	return strings.Join(miss, ", ")
}

// newScratchDir mints one private (0700) per-handoff dir under root (os.TempDir() when
// empty). MkdirTemp's random suffix guarantees two rapid handoffs never share a dir.
func newScratchDir(root string) (string, error) {
	if root == "" {
		root = os.TempDir()
	}
	dir, err := os.MkdirTemp(root, scratchPrefix)
	if err != nil {
		return "", fmt.Errorf("operator: scratch dir: %w", err)
	}
	// MkdirTemp already creates 0700 (modulo umask quirks) - pin it explicitly: the dir
	// holds a file whose CONTENT references the session-key env var, and other local
	// users must not even enumerate it.
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// cleanupFn removes the whole scratch dir: idempotent (RemoveAll on a missing dir is
// nil) and tolerant of whatever the guest left - or deleted - inside it.
func cleanupFn(dir string) func() error {
	return func() error { return os.RemoveAll(dir) }
}

// SweepStale removes rogerai-operator-* dirs under root older than olderThan - the
// best-effort crash sweep run at the next desk scan (a crash of roger ITSELF mid-handoff
// leaks the dir; the per-handoff cleanup covers every other path). Foreign names and
// fresh dirs are NEVER touched. Returns how many dirs were removed.
func SweepStale(root string, olderThan time.Duration) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-olderThan)
	swept := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), scratchPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.RemoveAll(filepath.Join(root, e.Name())) == nil {
			swept++
		}
	}
	return swept
}

// ComposeEnv layers the launch's env additions over the inherited parent env,
// OVERRIDING (not merging) any parent variable an addition re-declares - a user's real
// OPENAI_API_KEY must never leak into the child, or the guest bills their real account
// instead of the tuned band (config_aider.feature).
func ComposeEnv(parent, additions []string) []string {
	override := map[string]bool{}
	for _, kv := range additions {
		if i := strings.IndexByte(kv, '='); i > 0 {
			override[kv[:i]] = true
		}
	}
	out := make([]string, 0, len(parent)+len(additions))
	for _, kv := range parent {
		if i := strings.IndexByte(kv, '='); i > 0 && override[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, additions...)
}

// Command builds the child *exec.Cmd for a composed launch: the RESOLVED binary path,
// the launch argv, the user's workdir as cwd (the guest edits the user's project; only
// its CONFIG lives in scratch - mixing the two would make the guest edit throwaway files
// and then delete its own work), and the parent env with the launch additions overriding.
func Command(l Launch, binPath, workdir string, parentEnv []string) *exec.Cmd {
	c := exec.Command(binPath, l.Argv[1:]...)
	c.Dir = workdir
	c.Env = ComposeEnv(parentEnv, l.Env)
	return c
}
