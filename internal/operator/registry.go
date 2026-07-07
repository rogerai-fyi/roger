// Package operator is the pure core of Guest Operators Phase 2 ("hand the mic" to an
// installed agent CLI): the static registry of known guests, PATH detection through an
// injectable Env seam, and per-session throwaway config materialization. It has ZERO
// bubbletea dependencies (the internal/audio precedent) - internal/tui keeps only the
// command/picker/exec glue. Spec: features/operator/*.feature (founder-approved
// 2026-07-07); design: rogerai-internal-docs/GUEST-OPERATORS.md.
package operator

// Wiring strategies (design doc §4, empirically proven per guest). The strategy names are
// pinned by detection.feature ("Registry entries carry the empirically-proven wiring
// strategy") and drive Materialize.
const (
	// StrategyScratchConfig: a throwaway opencode.json in the session scratch dir, pointed
	// at via OPENCODE_CONFIG, with the model ALSO pinned on the argv (-m roger/<model>) so
	// no config layer (a user project's own opencode.json loads AFTER OPENCODE_CONFIG in
	// 1.17.11) can re-route the guest.
	StrategyScratchConfig = "scratch-config"
	// StrategyScratchHome: a throwaway HERMES_HOME (config.yaml + sessions + checkpoints
	// all land inside it) using the KEYED providers.<name> schema with api_key ${VAR} env
	// expansion. NEVER the bare model_aliases DirectAlias route - it resolves to
	// "no-key-required" on loopback and 401s against the Phase 1 bearer proxy (permanent
	// regression, config_hermes.feature).
	StrategyScratchHome = "scratch-home"
	// StrategyEnvFlags: pure env + flags, zero generated files (aider): OPENAI_API_BASE +
	// OPENAI_API_KEY in the child env, model + safety flags on the argv.
	StrategyEnvFlags = "env-and-flags"
)

// Guest is one registry entry: an agent CLI that can take the mic at THE DESK.
type Guest struct {
	Name        string // the desk name ("opencode")
	Bin         string // the PATH binary to look up
	Provider    string // wire tag - all MVP guests speak the OpenAI-compatible wire
	InstallHint string // the one-liner shown for a not-installed suggestion row
	// KnownGood is the version floor proven end-to-end on the dev box; a probe below it
	// (or unparsable) degrades the detection to UNVERIFIED - never hidden (§8 version skew).
	KnownGood string
	Strategy  string // one of the Strategy* constants
	// NeedsSetup marks a guest that is detectable but not launchable without user setup
	// (reserved for the future claude row - picking it prints SetupNote instead of execing).
	// Every MVP guest is config-generated, so none of the three sets it.
	NeedsSetup bool
	SetupNote  string

	// Per-brand presence seam (founder direction 2026-07-06): a dedicated design pass
	// will land each operator's wordmark as DATA-ONLY registry changes - the transition
	// logic never changes. All three are optional; empty means the tasteful text-only
	// house default.
	BrandPlate  string // multi-line ASCII wordmark for the PATCHING YOU THROUGH screen
	BrandAccent string // accent color (hex like "#fab387" or ANSI-256 index) for plate + glyph
	BrandGlyph  string // single-cell picker-row glyph
}

// Registry is the ONE source of who can ever appear at the desk (MVP set, design doc §4/§6).
// claude and codex are EXCLUDED in v1: they speak the Anthropic /v1/messages + Responses-API
// wire, and a naive launch silently falls back to the user's REAL Anthropic account - the
// exact failure §4 measured. Order is the desk display order.
func Registry() []Guest {
	return []Guest{
		{
			Name: "opencode", Bin: "opencode", Provider: "openai",
			InstallHint: "curl -fsSL https://opencode.ai/install | bash",
			KnownGood:   "1.17.11", // proven end-to-end on the dev box, 2026-07-06
			Strategy:    StrategyScratchConfig,
		},
		{
			Name: "hermes", Bin: "hermes", Provider: "openai",
			InstallHint: "pip install hermes-agent",
			KnownGood:   "0.16.0", // proven end-to-end on the dev box, 2026-07-06
			Strategy:    StrategyScratchHome,
		},
		{
			Name: "aider", Bin: "aider", Provider: "openai",
			InstallHint: "uv tool install aider-chat",
			KnownGood:   "0.86.2", // verified at GREEN stage (founder ruling 6): installed + run live 2026-07-06
			Strategy:    StrategyEnvFlags,
		},
	}
}
