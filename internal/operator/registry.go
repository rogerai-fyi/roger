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
	// StrategyContextOnly: hand over the CONTEXT and nothing else. No config, no base URL,
	// no session key, no model - the guest runs on its own account, exactly as the user's
	// own install would. It is defined by what it does NOT inject: that absence is what
	// makes the billing story honest (see the claude entry below).
	StrategyContextOnly = "context-only"
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
	// NeedsSetup marks a guest that is detectable but not launchable without user setup:
	// picking it prints SetupNote instead of execing. No guest sets it today - the three
	// wired ones are config-generated and claude needs no configuring at all - but the gate
	// stays, because a future guest that needs a key of its own must not silently launch.
	NeedsSetup bool
	SetupNote  string

	// Brand is the finished per-row plate the design pass landed (brand.go, from
	// GUEST-OPERATOR-PLATES.md): styled spans, adaptive hues, the ASCII/narrow lockup
	// rendered on the PATCHING YOU THROUGH screen. nil = the text-only house default.
	Brand *BrandArt
}

// Registry is the ONE source of who can ever appear at the desk. Order is the desk display
// order.
//
// claude and codex are CONTEXT-ONLY guests. They receive the handoff brief but no RogerAI
// credentials, endpoint, or model override, and run on the user's existing vendor account.
// The desk says that plainly before launch, turning the historical silent-billing failure
// into an informed choice without pretending either native wire is OpenAI-compatible.
func Registry() []Guest {
	plates := BrandArts()
	return []Guest{
		{
			Name: "opencode", Bin: "opencode", Provider: "openai",
			InstallHint: "curl -fsSL https://opencode.ai/install | bash",
			KnownGood:   "1.17.11", // proven end-to-end on the dev box, 2026-07-06
			Strategy:    StrategyScratchConfig,
			Brand:       plates["opencode"],
		},
		{
			Name: "hermes", Bin: "hermes", Provider: "openai",
			InstallHint: "pip install hermes-agent",
			KnownGood:   "0.16.0", // proven end-to-end on the dev box, 2026-07-06
			Strategy:    StrategyScratchHome,
			Brand:       plates["hermes"],
		},
		{
			Name: "aider", Bin: "aider", Provider: "openai",
			InstallHint: "uv tool install aider-chat",
			KnownGood:   "0.86.2", // verified at GREEN stage (founder ruling 6): installed + run live 2026-07-06
			Strategy:    StrategyEnvFlags,
			Brand:       plates["aider"],
		},
		{
			Name: "claude", Bin: "claude", Provider: "anthropic",
			InstallHint: "npm install -g @anthropic-ai/claude-code",
			KnownGood:   "2.1.220", // verified on the dev box, 2026-07-28
			Strategy:    StrategyContextOnly,
			Brand:       plates["claude"],
		},
		{
			Name: "codex", Bin: "codex", Provider: "openai",
			InstallHint: "npm install -g @openai/codex",
			KnownGood:   "0.1.0", // conservative compatibility floor; version parsing is format-tolerant
			Strategy:    StrategyContextOnly,
			Brand:       plates["codex"],
		},
	}
}
