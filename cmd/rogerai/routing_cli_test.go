package main

// routing_cli_test.go - in-process units for routing_cli.go. The behaviour is pinned end to
// end by routing_profiles_bdd_test.go (features/cli/routing_flags.feature and profiles.feature),
// which execs the REAL `roger` binary, so its statements are not counted here; these tables
// run the same parsing, layering, rendering and config writes in-process.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
)

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var back any
	require.NoError(t, json.Unmarshal(b, &back))
	b, _ = json.Marshal(back)
	return string(b)
}

func TestParseUseFlagsBody(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"m"}, `{}`},
		{[]string{"m", "--models", "a,b", "--models", "c,a"}, `{"models":["a","b","c"]}`},
		{[]string{"m", "--models", "m,b"}, `{"models":["b"]}`},
		{[]string{"m", "--only", " n1 , n2 ,"}, `{"provider":{"only":["n1","n2"]}}`},
		{[]string{"m", "--order", "n2,n1,n2"}, `{"provider":{"order":["n2","n1"]}}`},
		{[]string{"m", "--exclude", "n1", "--exclude", "n2"}, `{"provider":{"ignore":["n1","n2"]}}`},
		{[]string{"m", "--node", "n1", "--only", "n1,n2"}, `{"provider":{"only":["n1","n2"],"order":["n1"],"allow_fallbacks":false}}`},
		{[]string{"m", "--no-fallbacks", "--sort", "latency"}, `{"provider":{"allow_fallbacks":false,"sort":"latency"}}`},
		{[]string{"m", "--quant", "Q4_K_M,q4_k_m,IQ4_XS"}, `{"provider":{"quantizations":["Q4_K_M","IQ4_XS"]}}`},
		{[]string{"m", "--require", "Tools,VISION,tools"}, `{"roger":{"require":["tools","vision"]}}`},
		{[]string{"m", "--params", "7b-70b", "--min-ctx", "32K", "--max-ttft", "1.5s"}, `{"roger":{"params_b":[7,70],"min_ctx":32768,"max_ttft_ms":1500}}`},
		{[]string{"m", "--trust", "verified", "--self-hosted", "--region", "eu,us", "--confidential"}, `{"roger":{"trust_min":"verified","self_hosted_only":true,"region":["eu","us"],"confidential":true}}`},
		{[]string{"m", "--max-in", "0.2", "--max-out", "0.6", "--max-cost", ".05", "--min-tps", "20", "--pref", "cheap"},
			`{"provider":{"max_price":{"prompt":0.2,"completion":0.6,"request":0.05}},"roger":{"min_tps":20,"pref":"cheap"}}`},
		{[]string{"m", "--max-in", "0", "--max-cost", "0", "--min-tps", "0", "--max-out", "0"},
			`{"provider":{"max_price":{"prompt":null,"request":null,"completion":10}},"roger":{"min_tps":null}}`},
		{[]string{"m", "--freq", " 147.520 MHz 8F3K "}, `{"roger":{"freq":"147.520 MHz 8F3K"}}`},
	} {
		f, help, err := parseUseFlags(tc.args)
		require.NoError(t, err, "%v", tc.args)
		require.False(t, help)
		require.Equal(t, canon(t, mustJSON(t, tc.want)), canon(t, flagsBody(f)), "%v", tc.args)
	}
	_, help, err := parseUseFlags([]string{"-h"})
	require.NoError(t, err)
	require.True(t, help)
	f, _, err := parseUseFlags([]string{"m", "--port", "4242", "--raw", "--yes", "--advanced"})
	require.NoError(t, err)
	require.True(t, f.raw && f.yes && f.advanced)
	require.Equal(t, 4242, f.port)
}

func TestParseUseFlagsRefusals(t *testing.T) {
	ids33 := make([]string, 33)
	for i := range ids33 {
		ids33[i] = "n" + strings.Repeat("x", i+1)
	}
	for _, tc := range []struct {
		args  []string
		names []string
	}{
		{[]string{"m", "--models", ""}, []string{"--models"}},
		{[]string{"m", "--models", "a,b,c,d,e"}, []string{"--models", "5"}},
		{[]string{"m", "--only", ""}, []string{"--only"}},
		{[]string{"m", "--only", strings.Join(ids33, ",")}, []string{"--only", "32"}},
		{[]string{"m", "--order", ","}, []string{"--order"}},
		{[]string{"m", "--sort", "cheapest"}, []string{"--sort"}},
		{[]string{"m", "--pref", "fastest"}, []string{"--pref"}},
		{[]string{"m", "--quant", ""}, []string{"--quant"}},
		{[]string{"m", "--require", "TOOLS,speech"}, []string{"--require"}},
		{[]string{"m", "--params", "70-7"}, []string{"--params"}},
		{[]string{"m", "--min-ctx", "32kb"}, []string{"--min-ctx"}},
		{[]string{"m", "--max-ttft", "-5ms"}, []string{"--max-ttft"}},
		{[]string{"m", "--trust", "high"}, []string{"--trust"}},
		{[]string{"m", "--region", "EU"}, []string{"--region"}},
		{[]string{"m", "--max-cost", "inf"}, []string{"--max-cost"}},
		{[]string{"m", "--max-in", "-1"}, []string{"--max-in"}},
		{[]string{"m", "--max-out", "NaN"}, []string{"--max-out"}},
		{[]string{"m", "--min-tps", "-3"}, []string{"--min-tps"}},
		{[]string{"m", "--node", ""}, []string{"--node"}},
		{[]string{"m", "--node", "n1,n2"}, []string{"--node"}},
		{[]string{"m", "--profile", "Bad"}, []string{"--profile"}},
		{[]string{"m", "--sort", "price", "--pref", "fast"}, []string{"--sort", "--pref"}},
		{[]string{"m", "--node", "n1", "--order", "n2"}, []string{"--node", "--order"}},
		{[]string{"m", "--node", "n9", "--only", "n1"}, []string{"--node", "--only"}},
		{[]string{"m", "--cheapest"}, []string{"--cheapest"}},
		{[]string{"m", "--sort"}, []string{"--sort"}},
		{[]string{"m", "--yes=maybe"}, []string{"--yes"}},
		{[]string{"--pref", "fast", "m"}, []string{"roger use <model> [flags]"}},
		{[]string{"m", "extra"}, []string{"roger use <model> [flags]"}},
	} {
		_, _, err := parseUseFlags(tc.args)
		require.Error(t, err, "%v", tc.args)
		require.NotContains(t, err.Error(), "\n", "one line: %v", tc.args)
		for _, n := range tc.names {
			require.Contains(t, err.Error(), n, "%v", tc.args)
		}
	}
}

func TestFlagErrorShapes(t *testing.T) {
	require.EqualError(t, flagError("use", flagErrText("flag provided but not defined: -cheapest")),
		"use: unknown flag --cheapest (roger use --help lists them)")
	require.EqualError(t, flagError("use", flagErrText("flag needs an argument: -sort")), "use: --sort needs a value")
	require.EqualError(t, flagError("use", flagErrText(`invalid value "x" for flag -sort: "x" is bad`)), `use: --sort: "x" is bad`)
	require.EqualError(t, flagError("use", flagErrText(`invalid boolean value "m" for -yes: parse error`)), "use: --yes: parse error")
	require.EqualError(t, flagError("use", flagErrText("weird")), "use: weird")
}

type flagErrText string

func (e flagErrText) Error() string { return string(e) }

func TestMergeLayersAndRoutingLine(t *testing.T) {
	r := mergeLayers(
		routingLayer{"limits.default", mustJSON(t, `{"provider":{"max_price":{"completion":4}},"roger":{"min_tps":5,"pref":"cheap"}}`)},
		routingLayer{"limits.models.m", mustJSON(t, `{"provider":{"max_price":{"completion":3}}}`)},
		routingLayer{"profile coding", mustJSON(t, `{"provider":{"sort":"throughput"},"roger":{"require":["tools"]}}`)},
		routingLayer{"flag", mustJSON(t, `{"roger":{"region":["eu"],"min_tps":null}}`)},
	)
	require.Equal(t, canon(t, mustJSON(t, `{"provider":{"max_price":{"completion":3},"sort":"throughput"},"roger":{"require":["tools"],"region":["eu"]}}`)), canon(t, r.body))
	require.Equal(t, map[string]string{"provider.max_price.completion": "limits.models.m", "provider.sort": "profile coding",
		"roger.require": "profile coding", "roger.region": "flag"}, r.src)
	require.Equal(t, "routing: throughput (profile coding) · require tools (profile coding) · region eu (flag) · out ≤ $3/1M (model limit)", routingLine(r))

	// a higher pref replaces a lower sort
	r = mergeLayers(routingLayer{"profile p", mustJSON(t, `{"provider":{"sort":"price"}}`)}, routingLayer{"flag", mustJSON(t, `{"roger":{"pref":"fast"}}`)})
	require.Equal(t, "routing: fast", routingLine(r))

	all := mustJSON(t, `{"models":["a","b","c","d"],"provider":{"only":["n1"],"order":["n1"],"ignore":["n9"],"allow_fallbacks":false,
		"quantizations":["Q8_0"],"require_parameters":true,"max_price":{"prompt":0.2,"completion":2,"request":0.05}},
		"roger":{"pref":"fast","require":["tools"],"params_b":[7,70],"min_ctx":32768,"max_ttft_ms":900,"trust_min":"verified",
		"self_hosted_only":true,"region":["eu"],"confidential":true,"min_tps":20,"freq":"147.520 MHz 8F3K-9M2Q"}}`)
	src := map[string]string{}
	for _, l := range routingLeaves(all) {
		src[l] = "flag"
	}
	line := routingLine(resolvedRouting{body: all, src: src})
	require.Equal(t, "routing: fast · models a,b,c +1 · only n1 · order n1 · exclude n9 · no fallbacks · require tools · 7-70B · "+
		"ctx ≥ 32768 · ttft ≤ 900ms · trust verified · self-hosted · region eu · quant Q8_0 · require params · confidential · "+
		"≥20 t/s · in ≤ $0.2/1M · out ≤ $2/1M · cost ≤ $0.05 · private freq", line)
	require.NotContains(t, line, "8F3K")
	require.Equal(t, "", routingLine(resolvedRouting{body: map[string]any{}}))
	require.Equal(t, "limits.x", sourceWord("limits.x"))
}

func TestUseOptionsMapping(t *testing.T) {
	o := useOptions(mustJSON(t, `{"models":["a"],"provider":{"only":["n1"],"order":["n1"],"ignore":["n9"],"allow_fallbacks":false,
		"sort":"price","quantizations":["Q8_0"],"require_parameters":true,"max_price":{"prompt":0.2,"completion":2,"request":0.05}},
		"roger":{"pref":"fast","require":["tools"],"params_b":[7,70],"min_ctx":32768,"max_ttft_ms":900,"trust_min":"verified",
		"self_hosted_only":true,"region":["eu"],"confidential":true,"min_tps":20,"freq":"F"}}`))
	require.Equal(t, client.UseOptions{Models: []string{"a"}, Only: []string{"n1"}, Prefer: []string{"n1"}, ExcludeNodes: []string{"n9"},
		NoFallbacks: true, RequireParams: true, Sort: "price", Quantizations: []string{"Q8_0"}, MaxIn: 0.2, MaxOut: 2, MaxCost: 0.05,
		Pref: "fast", Require: []string{"tools"}, Trust: "verified", Region: []string{"eu"}, ParamsB: []float64{7, 70}, MinCtx: 32768,
		MaxTTFT: 900, SelfHostedOnly: true, Confidential: true, MinTPS: 20, Freq: "F"}, o)
	require.Equal(t, client.UseOptions{}, useOptions(mustJSON(t, `{"provider":{"allow_fallbacks":true}}`)))
}

func profilesOf(t *testing.T, doc string) *client.Profiles {
	t.Helper()
	ps, err := client.ParseProfiles([]byte(doc))
	require.NoError(t, err)
	return ps
}

func TestResolveUse(t *testing.T) {
	cfg := config{}
	cfg.Limits.Default = Limit{MaxOut: 4, MinTPS: 5}
	cfg.Limits.Models = map[string]Limit{"m": {MaxOut: 3, Quants: []string{"Q8_0"}}}
	ps := profilesOf(t, `{"profiles":{"coding":{"models":["m","x"],"roger":{"pref":"reliable"}},
		"pinned":{"model":"p","models":["q"]},"bare":{"roger":{"pref":"fast"}},"bad":{"roger":{"min_ctx":0}}}}`)
	parse := func(args ...string) *useFlags {
		f, _, err := parseUseFlags(args)
		require.NoError(t, err)
		return f
	}
	tgt, err := resolveUse(cfg, parse("@profile/coding"), ps)
	require.NoError(t, err)
	require.Equal(t, "m", tgt.model)
	require.Equal(t, canon(t, mustJSON(t, `{"models":["m","x"],"provider":{"max_price":{"completion":3},"quantizations":["Q8_0","unknown"]},"roger":{"min_tps":5,"pref":"reliable"}}`)), canon(t, tgt.r.body))

	tgt, err = resolveUse(cfg, parse("@profile/pinned"), ps)
	require.NoError(t, err)
	require.Equal(t, "p", tgt.model)

	tgt, err = resolveUse(cfg, parse("m:free", "--profile", "bare", "--pref", "cheap"), ps)
	require.NoError(t, err)
	require.Equal(t, "m:free", tgt.model)
	require.Equal(t, "flag", tgt.r.src["roger.pref"])
	require.Equal(t, "limits.models.m", tgt.r.src["provider.max_price.completion"], "the limits lookup reads the bare band")

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"m", "--profile", "nope"}, "use: --profile nope: no such profile (roger profile list)"},
		{[]string{"@profile/nope"}, "use: @profile/nope: no such profile (roger profile list)"},
		{[]string{"m", "--profile", "bad"}, "profile bad: roger.min_ctx must be a positive integer"},
		{[]string{"@profile/bare"}, "profile bare names no model: roger use <model> --profile bare"},
		{[]string{"@profile/coding", "--profile", "bare"}, "use: two profiles named: @profile/coding and --profile bare"},
		{[]string{"@profile/coding", "--models", "z"}, "use: --models cannot be combined with @profile/coding (the profile's list is the one source)"},
	} {
		_, err := resolveUse(cfg, parse(tc.args...), ps)
		require.EqualError(t, err, tc.want, "%v", tc.args)
	}
}

func writeDoc(t *testing.T, path, doc string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return mustJSON(t, string(b))
}

func TestCmdSetLimitRoutingKeys(t *testing.T) {
	useTempConfig(t)
	path := configPath()
	out := captureStdout(t, func() {
		require.NoError(t, cmdSetLimit([]string{"qwen3-32b", "--quant", "Q8_0,BF16", "--pref", "reliable", "--require", "tools",
			"--params", "7-70", "--min-ctx", "32k", "--max-ttft", "1500ms", "--trust", "verified", "--self-hosted", "true",
			"--region", "eu", "--max-cost", "0.02", "--max-in", "0.3", "--min-tps", "9", "--max-out", "3"}))
	})
	for _, line := range []string{"set qwen3-32b quants = Q8_0,BF16", "set qwen3-32b pref = reliable", "set qwen3-32b require = tools",
		"set qwen3-32b params_b = 7-70", "set qwen3-32b min_ctx = 32k", "set qwen3-32b max_ttft_ms = 1500ms",
		"set qwen3-32b trust_min = verified", "set qwen3-32b self_hosted = true", "set qwen3-32b region = eu",
		"set qwen3-32b max_cost = 0.02", "set qwen3-32b max_in = 0.3", "set qwen3-32b min_tps = 9", "set qwen3-32b max_out = 3"} {
		require.Contains(t, out, line)
	}
	got := loadConfig().Limits.Models["qwen3-32b"]
	require.Equal(t, Limit{MaxIn: 0.3, MaxOut: 3, MinTPS: 9, Quants: []string{"Q8_0", "BF16"}, Pref: "reliable", Require: []string{"tools"},
		ParamsB: []float64{7, 70}, MinCtx: 32768, MaxTTFTMs: 1500, TrustMin: "verified", SelfHosted: true, Region: []string{"eu"}, MaxCost: 0.02}, got)

	out = captureStdout(t, func() { require.NoError(t, cmdSetLimit([]string{"qwen3-32b", "--max-out", "0"})) })
	require.Contains(t, out, "max_out cleared for qwen3-32b (the $10/1M default applies)")
	require.Zero(t, loadConfig().Limits.Models["qwen3-32b"].MaxOut)

	captureStdout(t, func() { require.NoError(t, cmdSetLimit([]string{"default", "--self-hosted", "true"})) })
	require.True(t, loadConfig().Limits.Default.SelfHosted)

	before, _ := os.ReadFile(path)
	for _, bad := range [][]string{
		{"qwen3-32b", "--params", "70-7"}, {"qwen3-32b", "--quant", ""}, {"qwen3-32b", "--pref", "warp"},
		{"qwen3-32b", "--require", "json"}, {"qwen3-32b", "--min-ctx", "0"}, {"qwen3-32b", "--max-ttft", "soon"},
		{"qwen3-32b", "--trust", "gold"}, {"qwen3-32b", "--self-hosted", "maybe"}, {"qwen3-32b", "--region", "EU"},
		{"qwen3-32b", "--max-cost", "-1"}, {"qwen3-32b", "--max-in", "x"}, {"qwen3-32b", "--min-tps", "-1"},
		{"qwen3-32b", "--max-out", "-1"}, {"qwen3-32b", "stray"}, {}, {"--pref", "fast"},
	} {
		err := cmdSetLimit(bad)
		require.Error(t, err, "%v", bad)
		require.NotContains(t, err.Error(), "\n")
	}
	after, _ := os.ReadFile(path)
	require.Equal(t, string(before), string(after), "a refused set-limit leaves the config unchanged")
	require.Contains(t, cmdSetLimit(nil).Error(), setLimitFlags)

	for _, h := range [][]string{{"--help"}, {"qwen3-32b", "-h"}} {
		out = captureStdout(t, func() { require.NoError(t, cmdSetLimit(h)) })
		require.Contains(t, out, "the default $10/1M cap")
		require.NotContains(t, out, "no cap")
	}
}

func TestCmdConfigShowAndLimits(t *testing.T) {
	useTempConfig(t)
	path := configPath()
	writeDoc(t, path, `{"limits":{"default":{"max_out":4},"models":{"qwen3-32b":{"pref":"reliable","max_out":100,"quants":["Q8_0"],"self_hosted":true}}},
		"profiles":{"coding":{"provider":{"only":["n1"]},"roger":{"pref":"fast"}},"home":{"roger":{"freq":"147.520 MHz 8F3K-9M2Q"}}}}`)
	out := captureStdout(t, func() { require.NoError(t, cmdConfigShow([]string{"llama"})) })
	require.Contains(t, out, "max_out: 4 (limits.default)")
	require.Contains(t, out, "min_tps: - (unset, unmeasured stations pass)")
	require.Contains(t, out, "pref: - (unset)")

	out = captureStdout(t, func() { require.NoError(t, cmdConfigShow([]string{"qwen3-32b", "--profile", "coding"})) })
	require.Contains(t, out, "pref: fast (profile coding)")
	require.Contains(t, out, "only: n1 (profile coding)")
	require.Contains(t, out, "quants: Q8_0,unknown (limits.models.qwen3-32b)")

	out = captureStdout(t, func() { require.NoError(t, cmdConfigShow([]string{"x", "--profile", "home"})) })
	require.Contains(t, out, "freq: (set, hidden)")
	require.NotContains(t, out, "8F3K")

	writeDoc(t, path, `{}`)
	out = captureStdout(t, func() { require.NoError(t, cmdConfigShow([]string{"llama"})) })
	require.Contains(t, out, "max_out: 10 (built-in)")

	require.Error(t, cmdConfigShow(nil))
	require.Error(t, cmdConfigShow([]string{"--profile", "x"}))
	require.Error(t, cmdConfigShow([]string{"x", "-h"}))
	require.Error(t, cmdConfigShow([]string{"x", "--sort", "nope"}))
	require.Error(t, cmdConfigShow([]string{"x", "--profile", "nope"}))

	writeDoc(t, path, `{"limits":{"default":{"max_out":100,"pref":"fast"},"models":{"a":{"max_out":3}}}}`)
	out = captureStdout(t, func() { printLimits(loadConfig()) })
	require.Contains(t, out, "out ≤ $100/1M (network ceiling)")
	require.Contains(t, out, "pref=fast")
	require.Contains(t, out, "out ≤ $3/1M")
	writeDoc(t, path, `{}`)
	out = captureStdout(t, func() { printLimits(loadConfig()) })
	require.Contains(t, out, "none set")
}

func TestCmdProfile(t *testing.T) {
	useTempConfig(t)
	path := configPath()
	writeDoc(t, path, `{"broker":"http://b","palette":"mono","limits":{"default":{"max_out":4}},
		"profiles":{"coding":{"provider":{"sort":"throughput","only":["n1","n2"]},"roger":{"require":["tools"]},"max_tokens":5,"api_key":"sk-secret"},
		"cheap":{"roger":{"pref":"cheap","prefer":"x"}},"default":{},"Bad Name":{},"broken":{"roger":{"min_ctx":0}},
		"home":{"model":"m","roger":{"freq":"147.520 MHz 8F3K-9M2Q"}}}}`)

	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() { require.NoError(t, cmdProfile([]string{"list"})) })
	})
	require.Contains(t, out, "default   built from limits.* (out ≤ $4/1M)")
	require.Contains(t, out, "cheap     cheap")
	require.Contains(t, out, "coding    throughput · only n1,n2 · require tools")
	require.Contains(t, out, "home      m · private freq")
	require.NotContains(t, out, "8F3K")
	require.Contains(t, stderr, "profile default is reserved (it is built from limits.*)")
	require.Contains(t, stderr, `profile "Bad Name": invalid name`)
	require.Contains(t, stderr, "profile broken: roger.min_ctx must be a positive integer")

	out = captureStdout(t, func() { require.NoError(t, cmdProfile([]string{"show", "coding"})) })
	require.Contains(t, out, "provider.sort = throughput  (profile coding)")
	require.Contains(t, out, "provider.max_price.completion = 4  (limits.default)")
	require.Contains(t, out, "ignored keys (not routing): api_key, max_tokens")
	require.NotContains(t, out, "sk-secret")
	out = captureStdout(t, func() { require.NoError(t, cmdProfile([]string{"show", "cheap"})) })
	require.Contains(t, out, "ignored keys (unknown): roger.prefer")
	out = captureStdout(t, func() { require.NoError(t, cmdProfile([]string{"show", "home"})) })
	require.Contains(t, out, "(set, hidden)")
	require.NotContains(t, out, "8F3K")
	out = captureStdout(t, func() { require.NoError(t, cmdProfile([]string{"show", "default"})) })
	require.Contains(t, out, "(built from limits.default)")
	require.Error(t, cmdProfile([]string{"show", "broken"}))
	require.Error(t, cmdProfile([]string{"show", "nope"}))

	captureStdout(t, func() {
		require.NoError(t, cmdProfile([]string{"set", "eu", "roger.region", `["eu"]`}))
		require.NoError(t, cmdProfile([]string{"set", "coding", "roger.min_tps", "5"}))
		require.Error(t, cmdProfile([]string{"set", "coding", "roger.pref", "fast"}), "sort + pref is exclusive")
	})
	doc := readDoc(t, path)
	require.Equal(t, "mono", doc["palette"], "every other key survives a profile write")
	eu, _ := rfGetPath(doc, "profiles.eu")
	require.Equal(t, canon(t, mustJSON(t, `{"roger":{"region":["eu"]}}`)), canon(t, eu))

	before, _ := os.ReadFile(path)
	for _, bad := range [][]string{
		{"set", "coding", "roger.pref", "quickest"}, {"set", "coding", "max_tokens", "5"}, {"set", "default", "roger.pref", "fast"},
		{"set", "Bad", "roger.pref", "fast"}, {"set", "x"}, {"unset", "x"}, {"rm"}, {"show"}, {"rm", "nope"}, {"bogus"}, {},
	} {
		captureStdout(t, func() { require.Error(t, cmdProfile(bad), "%v", bad) })
	}
	after, _ := os.ReadFile(path)
	require.Equal(t, string(before), string(after), "a refused profile write leaves config.json unchanged")
	require.EqualError(t, cmdProfile([]string{"rm", "default"}), "default is built from limits.*; use roger config clear-limit default")

	captureStdout(t, func() {
		require.NoError(t, cmdProfile([]string{"unset", "coding", "roger.require"}))
		require.NoError(t, cmdProfile([]string{"rm", "eu"}))
	})
	doc = readDoc(t, path)
	_, has := rfGetPath(doc, "profiles.eu")
	require.False(t, has)
	_, has = rfGetPath(doc, "profiles.coding.roger.require")
	require.False(t, has)

	writeDoc(t, path, `{`)
	require.Error(t, cmdProfile([]string{"set", "p", "roger.pref", "fast"}), "a corrupt config is never overwritten")
}

func TestProfileWritesAreSerialized(t *testing.T) {
	useTempConfig(t)
	path := configPath()
	writeDoc(t, path, `{}`)
	var wg sync.WaitGroup
	for _, n := range []string{"alpha", "beta", "gamma", "delta"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			require.NoError(t, profileWrite(n, func(p map[string]any) error {
				rfSetPath(p, "roger.pref", "fast")
				return nil
			}, ""))
		}(n)
	}
	captureStdout(t, wg.Wait)
	doc := readDoc(t, path)
	for _, n := range []string{"alpha", "beta", "gamma", "delta"} {
		_, ok := rfGetPath(doc, "profiles."+n+".roger.pref")
		require.True(t, ok, n)
	}

	// a lock left by a crashed writer is taken over once stale; a live one times out
	lock := path + ".lock"
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	old := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(lock, old, old))
	release, err := lockConfig(lock)
	require.NoError(t, err)
	release()
}

func TestCmdUseInProcess(t *testing.T) {
	useTempConfig(t)
	path := configPath()
	cfg := config{Broker: fakeBrokerEmpty(t), User: "u"}
	out := captureStdout(t, func() { require.NoError(t, cmdUse(cfg, []string{"--help"})) })
	require.Contains(t, out, "routing:")
	require.Contains(t, out, "advanced flags: --port --max-in --min-tps --confidential --yes --raw")
	require.EqualError(t, cmdUse(cfg, nil), "usage: roger use <model> [flags]")
	require.Error(t, cmdUse(cfg, []string{"m", "--sort", "x"}))

	writeDoc(t, path, `{"profiles":"not-an-object"}`)
	var stderr string
	out = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			require.NoError(t, cmdUse(cfg, []string{"m", "--advanced", "--max-out", "250", "--pref", "fast", "--yes"}))
		})
	})
	require.Contains(t, stderr, "profiles: ignored (not an object) - run roger profile list")
	require.Contains(t, out, "advanced flags:")
	require.Contains(t, out, "capped at the network ceiling")
	require.Contains(t, out, "routing: fast · out ≤ $250/1M")
	captureStderr(t, func() { require.Error(t, cmdUse(cfg, []string{"m", "--profile", "nope"})) })
}

func TestPathHelpers(t *testing.T) {
	m := map[string]any{}
	rfSetPath(m, "a.b.c", 1)
	require.True(t, hasPath(m, "a.b.c"))
	rfDelPath(m, "a.b.c")
	require.Empty(t, m, "emptied parents are removed")
	rfDelPath(m, "x.y")
	m["s"] = "str"
	_, ok := rfGetPath(m, "s.t")
	require.False(t, ok)
	require.Nil(t, strList([]any{1}))
	require.NoError(t, profileKeyOK("models"))
	require.Error(t, profileKeyOK("provider"))
	require.Equal(t, "a,b", showValue("models", []any{"a", "b"}))
	require.Equal(t, "true", showValue("roger.confidential", true))
}

// captureStderr is captureStdout for os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			b.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		done <- b.String()
	}()
	defer func() {
		os.Stderr = orig
		_ = r.Close()
	}()
	fn()
	_ = w.Close()
	return <-done
}
