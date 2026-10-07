package main

// routing_flags_test.go - in-process units for the routing flag values and the limit paths
// that carry them. The behaviour is pinned end to end by routing_flags_bdd_test.go, which
// drives the REAL `roger` binary as a subprocess (so its statements are not counted in this
// package's coverage); these table tests exercise the same code in-process: every accepted
// and refused spelling of --pref and --max-out, the per-model pref falling through to the
// default, the persisted set-limit forms, and how a limit renders.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/tui"
)

func TestPrefFlag(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"cheap", "cheap", true},
		{"balanced", "balanced", true},
		{"fast", "fast", true},
		{"reliable", "reliable", true},
		{" FAST ", "fast", true}, // the CLI lower-cases and trims; the broker is strict
		{"", "", true},           // explicit empty = no pref (client.ValidPref treats "" as unset)
		{"turbo", "", false},
		{"cheap,fast", "", false},
	} {
		var p prefFlag
		err := p.Set(tc.in)
		if !tc.ok {
			require.Error(t, err, "pref %q must be refused", tc.in)
			for _, v := range client.RoutingPrefs {
				require.Contains(t, err.Error(), v, "the refusal names every accepted value")
			}
			require.Empty(t, p.String(), "a refused value is never stored")
			continue
		}
		require.NoError(t, err, "pref %q", tc.in)
		require.Equal(t, tc.want, p.String())
	}
}

func TestMaxOutFlag(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0", 0, true}, // 0 = the default $10/1M cap, applied by the relay
		{"0.5", 0.5, true},
		{"10", 10, true},
		{"10.01", 10.01, true},
		{"100", 100, true},
		{"250", 250, true}, // above the ceiling is sent as given; the broker clamps
		{"unlimited", client.ConsumerCeilingMaxOut, true},
		{" UNLIMITED ", client.ConsumerCeilingMaxOut, true},
		{"Unlimited", client.ConsumerCeilingMaxOut, true},
		{"-1", 0, false},
		{"-0.01", 0, false},
		{"NaN", 0, false},
		{"Inf", 0, false},
		{"+Inf", 0, false},
		{"free", 0, false},
		{"none", 0, false},
		{"10USD", 0, false},
		{"", 0, false},
	} {
		var m maxOutFlag
		err := m.Set(tc.in)
		if !tc.ok {
			require.Error(t, err, "max-out %q must be refused", tc.in)
			require.Contains(t, err.Error(), "a $/1M price or the word unlimited")
			require.False(t, m.set, "a refused value never counts as passed")
			continue
		}
		require.NoError(t, err, "max-out %q", tc.in)
		require.True(t, m.set)
		require.InDelta(t, tc.want, m.v, 1e-12)
	}
	// String is what the flag package prints as the default in -h.
	require.Equal(t, "0", (&maxOutFlag{}).String())
	require.Equal(t, "42", (&maxOutFlag{v: 42, set: true}).String())
}

func TestMaxOutHelpSaysWhatZeroMeans(t *testing.T) {
	// routing_flags.feature: the help reads "0 = the default $10/1M consumer cap".
	require.Contains(t, maxOutHelp, "0 = the default $10/1M consumer cap")
	require.NotContains(t, strings.ToLower(maxOutHelp), "no cap")
	require.Contains(t, maxOutHelp, "unlimited")
}

func TestResolvePrefFallsThroughToDefault(t *testing.T) {
	var c config
	c.Limits.Default = Limit{Pref: "cheap", MaxOut: 3}
	c.Limits.Models = map[string]Limit{
		"with-pref":    {Pref: "fast", MaxOut: 1},
		"without-pref": {MaxOut: 2},
	}
	l, typ := c.resolve("with-pref")
	require.Equal(t, "fast", l.Pref, "a per-model pref wins")
	require.Equal(t, 800, typ, "typical reply size defaults to 800")

	l, _ = c.resolve("without-pref")
	require.Equal(t, "cheap", l.Pref, "a per-model limit with no pref inherits the default pref")
	require.Equal(t, 2.0, l.MaxOut, "and keeps its own caps")

	l, _ = c.resolve("unknown-model")
	require.Equal(t, c.Limits.Default, l, "no per-model limit resolves to the default")

	c.Limits.TypicalOutTok = 1200
	_, typ = c.resolve("unknown-model")
	require.Equal(t, 1200, typ)
}

// TestLimitSummary pins the `roger limits` row in the routing-line vocabulary
// (routing_flags.feature: the row shows "out ≤ $3/1M", "fast", "Q8_0", "self-hosted").
func TestLimitSummary(t *testing.T) {
	for _, tc := range []struct {
		l    Limit
		want string
	}{
		{Limit{}, "no caps"},
		{Limit{MaxOut: 2}, "out ≤ $2/1M"},
		{Limit{MaxOut: client.ConsumerCeilingMaxOut}, "out ≤ $100/1M (network ceiling)"},
		{Limit{MaxOut: 250}, "out ≤ $100/1M (network ceiling)"}, // at or above the ceiling reads as the ceiling
		{Limit{MaxIn: 0.5, MinTPS: 30}, "≥30 t/s · in ≤ $0.5/1M"},
		{Limit{Pref: "reliable"}, "pref=reliable"},
		{Limit{MaxOut: 1, MaxIn: 0.2, MinTPS: 20, Pref: "fast"}, "pref=fast · ≥20 t/s · in ≤ $0.2/1M · out ≤ $1/1M"},
		{Limit{MaxOut: 3, Pref: "fast", Quants: []string{"Q8_0"}, SelfHosted: true}, "pref=fast · self-hosted · quant Q8_0 · out ≤ $3/1M"},
		{Limit{Require: []string{"tools"}, ParamsB: []float64{7, 70}, MinCtx: 32768, MaxTTFTMs: 1500, TrustMin: "verified",
			Region: []string{"eu"}, MaxCost: 0.02},
			"require tools · 7-70B · ctx ≥ 32768 · ttft ≤ 1500ms · trust verified · region eu · cost ≤ $0.02"},
	} {
		require.Equal(t, tc.want, limitSummary(tc.l))
	}
}

func TestSetLimitPersistsPrefAndUnlimited(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	require.NoError(t, cmdSetLimit([]string{"default", "--pref", "reliable"}))
	require.Equal(t, "reliable", loadConfig().Limits.Default.Pref)

	require.NoError(t, cmdSetLimit([]string{"default", "--max-out", "unlimited"}))
	c := loadConfig()
	require.Equal(t, client.ConsumerCeilingMaxOut, c.Limits.Default.MaxOut, "unlimited persists the register ceiling")
	require.Equal(t, "reliable", c.Limits.Default.Pref, "flags not passed are preserved")

	// --max-out 0 is a passed value (it clears the stored cap back to the default), which the
	// old -1 sentinel could not tell from "not passed".
	require.NoError(t, cmdSetLimit([]string{"default", "--max-out", "0"}))
	require.Zero(t, loadConfig().Limits.Default.MaxOut)

	require.NoError(t, cmdSetLimit([]string{"qwen3-32b", "--pref", "fast", "--max-out", "1.5", "--min-tps", "20", "--max-in", "0.4"}))
	c = loadConfig()
	require.Equal(t, Limit{MaxIn: 0.4, MaxOut: 1.5, MinTPS: 20, Pref: "fast"}, c.Limits.Models["qwen3-32b"])
	l, _ := c.resolve("qwen3-32b")
	require.Equal(t, "fast", l.Pref)

	// A second per-model limit with no pref of its own inherits the default's.
	require.NoError(t, cmdSetLimit([]string{"gpt-oss-120b", "--max-out", "2"}))
	l, _ = loadConfig().resolve("gpt-oss-120b")
	require.Equal(t, "reliable", l.Pref)

	// Flags before the model leave no positional: the usage error, nothing written.
	before := loadConfig()
	require.Error(t, cmdSetLimit([]string{"--pref", "cheap"}))
	require.Equal(t, before.Limits, loadConfig().Limits)
}

// TestCmdUseRoutingFlagBranches runs cmdUse in-process with each routing flag against an
// empty market (it returns before the blocking relay): --pref, --self-hosted, --quant, an
// above-ceiling --max-out (the one-time warning), unlimited, the config pref and quant rule
// with no flag at all, and a leading flag with no model (the usage error).
func TestCmdUseRoutingFlagBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBrokerEmpty(t), User: "u"}
	for _, args := range [][]string{
		{"m1", "--pref", "cheap", "--self-hosted", "--quant", "Q8_0,BF16"},
		{"m1", "--max-out", "250"},
		{"m1", "--max-out", "unlimited", "--min-tps", "20"},
		{"m1", "--max-out", "0"},
	} {
		require.NoError(t, cmdUse(cfg, args), "cmdUse %v", args)
	}
	// The stored rule and pref are what a flagless `roger use` resolves.
	cfg.Limits.Default = Limit{Pref: "reliable"}
	cfg.Limits.Models = map[string]Limit{"m1": {Quants: []string{"Q8_0"}}}
	require.NoError(t, cmdUse(cfg, []string{"m1"}))
	lim, _ := cfg.resolve("m1")
	require.Equal(t, "reliable", lim.Pref)
	require.Equal(t, []string{"Q8_0", client.QuantUnknown}, client.RuleQuantizations(lim.Quants),
		"a standing rule travels as its labels plus unknown")
	require.Nil(t, client.RuleQuantizations(nil), "no rule sends no quantizations key")

	// A leading flag leaves no model: flags parse, then the usage error (no request made).
	err := cmdUse(cfg, []string{"--pref", "fast"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage: roger use <model>")
}

// TestCLIAndTUIResolveLimitsByOneRule: `roger use` and the booth resolve a band's rule with
// the same per-key merge - held to the one shared table, through both real paths.
func TestCLIAndTUIResolveLimitsByOneRule(t *testing.T) {
	for _, tc := range tui.LimitMergeCases() {
		t.Run(tc.Name, func(t *testing.T) {
			var c config
			c.Limits.Default = fromTUILimit(tc.Default)
			c.Limits.Models = map[string]Limit{"m": fromTUILimit(tc.Model)}
			cli, _ := c.resolve("m")
			require.Equal(t, fromTUILimit(tc.Want), cli, "CLI")
			require.Equal(t, tc.Want, tuiLimits(c).Resolve("m"), "TUI")
		})
	}
}
