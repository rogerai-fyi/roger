package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestShareModelArg covers the positional-model parsing for `roger share`: a leading
// non-flag token is the model (and is stripped from the args the flag parser sees),
// while a leading flag (or no args) leaves model "" and args untouched.
func TestShareModelArg(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		wantModel string
		wantRest  []string
	}{
		{"bare positional model", []string{"gpt-oss-120b"}, "gpt-oss-120b", []string{}},
		{"positional then flags", []string{"gpt-oss-120b", "--price-out", "0.5"}, "gpt-oss-120b", []string{"--price-out", "0.5"}},
		{"leading flag, no positional", []string{"--model", "x"}, "", []string{"--model", "x"}},
		{"no args", nil, "", nil},
		{"leading dash only", []string{"-"}, "", []string{"-"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotModel, gotRest := shareModelArg(c.in)
			if gotModel != c.wantModel {
				t.Errorf("model = %q, want %q", gotModel, c.wantModel)
			}
			if len(gotRest) != len(c.wantRest) {
				t.Fatalf("rest = %v, want %v", gotRest, c.wantRest)
			}
			for i := range gotRest {
				if gotRest[i] != c.wantRest[i] {
					t.Errorf("rest[%d] = %q, want %q", i, gotRest[i], c.wantRest[i])
				}
			}
		})
	}
}

// TestSharePositionalPrecedence mirrors cmdShare's resolution order: the positional
// model overrides the saved-config default, but an explicit --model (parsed from the
// remaining args) still wins over both - exactly as the flagset default + Parse(rest)
// behave in cmdShare.
func TestSharePositionalPrecedence(t *testing.T) {
	resolve := func(savedDefault string, args []string) string {
		posModel, rest := shareModelArg(args)
		defModelFlag := savedDefault
		if posModel != "" {
			defModelFlag = posModel
		}
		fs := flag.NewFlagSet("share", flag.ContinueOnError)
		model := fs.String("model", defModelFlag, "")
		_ = fs.Parse(rest)
		return *model
	}
	// Positional overrides the saved-config default (the founder's bug: `share gpt-oss-120b`
	// must expose gpt-oss-120b, not the saved gpt-oss-20b).
	if got := resolve("gpt-oss-20b", []string{"gpt-oss-120b"}); got != "gpt-oss-120b" {
		t.Errorf("positional should override saved default: got %q, want gpt-oss-120b", got)
	}
	// No positional -> the saved-config default still applies.
	if got := resolve("gpt-oss-20b", nil); got != "gpt-oss-20b" {
		t.Errorf("no positional should keep saved default: got %q, want gpt-oss-20b", got)
	}
	// Explicit --model still works (and wins, as it is parsed last).
	if got := resolve("gpt-oss-20b", []string{"--model", "qwen3-coder"}); got != "qwen3-coder" {
		t.Errorf("--model should be honored: got %q, want qwen3-coder", got)
	}
	// Positional AND --model: the explicit flag wins (parsed after the positional seeds
	// the default).
	if got := resolve("", []string{"gpt-oss-120b", "--model", "qwen3-coder"}); got != "qwen3-coder" {
		t.Errorf("--model should override the positional: got %q, want qwen3-coder", got)
	}
}

func TestNormalizeUpstream(t *testing.T) {
	const want = "http://127.0.0.1:8060/v1/chat/completions"
	cases := map[string]string{
		"http://127.0.0.1:8060":                      want, // base URL (the natural input)
		"http://127.0.0.1:8060/":                     want, // trailing slash
		"http://127.0.0.1:8060/v1":                   want, // /v1 base
		"http://127.0.0.1:8060/v1/":                  want, // /v1 with slash
		"http://127.0.0.1:8060/v1/chat/completions":  want, // already full (idempotent)
		"http://127.0.0.1:8060/v1/chat/completions/": want, // full + slash
	}
	for in, exp := range cases {
		if got := normalizeUpstream(in); got != exp {
			t.Errorf("normalizeUpstream(%q) = %q, want %q", in, got, exp)
		}
	}
	if got := normalizeUpstream(""); got != "" {
		t.Errorf("normalizeUpstream(\"\") = %q, want empty", got)
	}
}

// TestParseMonthlyCap verifies the `roger limit --monthly` value parsing: a dollar
// amount (with or without a leading $), the clear spellings (0/off/none/unlimited),
// and the invalid cases.
func TestParseMonthlyCap(t *testing.T) {
	ok := map[string]float64{
		"25": 25, "$25": 25, " 25 ": 25, "25.50": 25.5,
		"0": 0, "off": 0, "OFF": 0, "none": 0, "unlimited": 0, "$0": 0,
	}
	for in, want := range ok {
		got, err := parseMonthlyCap(in)
		if err != nil {
			t.Errorf("parseMonthlyCap(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseMonthlyCap(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"-5", "abc", "$-1", "12x"} {
		if _, err := parseMonthlyCap(bad); err == nil {
			t.Errorf("parseMonthlyCap(%q) should have errored", bad)
		}
	}
}

// TestLimitsLoadSaveBackCompat verifies the new limits section round-trips and
// that an OLD config with NO limits section still loads (back-compat) with no
// caps. Uses XDG_CONFIG_HOME so configPath() points at a temp dir.
// TestShareMaxOnAirDefault: the soft local on-air cap defaults to 4 and is read from
// share.max_on_air, surviving a save/reload. The default holds for an old config with
// no share section.
func TestShareMaxOnAirDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ROGER_BROKER", "")
	t.Setenv("ROGER_USER", "")

	// No share section at all -> default 4.
	if err := os.MkdirAll(filepath.Dir(configPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), []byte(`{"broker":"https://b.example"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadConfig().shareMaxOnAir(); got != 4 {
		t.Fatalf("default share.max_on_air = %d, want 4", got)
	}
	if defaultShareMaxOnAir != 4 {
		t.Fatalf("defaultShareMaxOnAir = %d, want 4", defaultShareMaxOnAir)
	}

	// A configured share.max_on_air is read back.
	c := loadConfig()
	c.Share = &Share{Model: "m", MaxOnAir: 8}
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
	if got := loadConfig().shareMaxOnAir(); got != 8 {
		t.Errorf("configured share.max_on_air = %d, want 8", got)
	}
	// A non-positive value falls back to the default (it is a deliberate restart knob).
	c2 := loadConfig()
	c2.Share.MaxOnAir = 0
	_ = saveConfig(c2)
	if got := loadConfig().shareMaxOnAir(); got != 4 {
		t.Errorf("zero share.max_on_air should fall back to the default 4, got %d", got)
	}
}

func TestLimitsLoadSaveBackCompat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ROGER_BROKER", "")
	t.Setenv("ROGER_USER", "")

	// 1) Old config: no "limits" key at all - must load, no caps.
	old := `{"broker":"https://b.example","user":"luis"}`
	if err := os.MkdirAll(filepath.Dir(configPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	c := loadConfig()
	if c.Broker != "https://b.example" || c.User != "luis" {
		t.Fatalf("old config lost fields: %+v", c)
	}
	lim, typ := c.resolve("anything")
	if lim != (Limit{}) || typ != 800 {
		t.Errorf("old config should resolve to no caps + default 800, got %+v typ=%d", lim, typ)
	}

	// 2) Set a per-model + default limit, save, reload.
	c.Limits.Default = Limit{MaxOut: 0.40}
	c.Limits.Models = map[string]Limit{"qwen3-coder-30b": {MaxOut: 0.30, MinTPS: 40}}
	c.Limits.TypicalOutTok = 1000
	if err := saveConfig(c); err != nil {
		t.Fatal(err)
	}
	c2 := loadConfig()
	if got, typ := c2.resolve("qwen3-coder-30b"); got.MaxOut != 0.30 || got.MinTPS != 40 || typ != 1000 {
		t.Errorf("per-model resolve = %+v typ=%d, want max-out 0.30 min-tps 40 typ 1000", got, typ)
	}
	if got, _ := c2.resolve("unpinned-model"); got.MaxOut != 0.40 {
		t.Errorf("default resolve = %+v, want max-out 0.40", got)
	}
}

// TestSeedSharePricing covers the P0-A parity fix: `roger share` honors the TUI
// editor's saved per-model price + schedule (cfg.Prices) when no explicit flags are
// passed, and explicit flags still override.
func TestSeedSharePricing(t *testing.T) {
	cfg := config{Prices: map[string]SharePrice{
		"gpt-oss-20b": {
			PriceIn:  0.3,
			PriceOut: 0.7,
			Windows:  []SchedWindow{{Start: "03:00", End: "03:30", Free: true}},
		},
	}}

	// No flags passed -> seed price + windows from the saved profile (parity).
	in, out, sched := seedSharePricing(cfg, "gpt-oss-20b", 0, 0, nil, sharePricingFlags{})
	if in != 0.3 || out != 0.7 {
		t.Errorf("no-flag price = %v/%v, want 0.3/0.7 (saved profile)", in, out)
	}
	if len(sched) != 1 || sched[0].Start != "03:00" || !sched[0].Free {
		t.Errorf("no-flag schedule = %+v, want the saved 03:00-03:30 FREE window", sched)
	}

	// Explicit price flags override the saved profile; explicit schedule flag means the
	// saved windows are NOT appended (the one-off owns the schedule).
	flagSched := []protocol.PriceWindow{{Start: "18:00", End: "22:00", Out: 1.0}}
	in, out, sched = seedSharePricing(cfg, "gpt-oss-20b", 5.0, 9.0, flagSched,
		sharePricingFlags{in: true, out: true, schedule: true})
	if in != 5.0 || out != 9.0 {
		t.Errorf("flag override price = %v/%v, want 5/9 (explicit flags win)", in, out)
	}
	if len(sched) != 1 || sched[0].Start != "18:00" {
		t.Errorf("flag schedule = %+v, want only the explicit window (saved not appended)", sched)
	}

	// A model with no saved profile leaves the inputs untouched (free stays free).
	in, out, sched = seedSharePricing(cfg, "no-profile", 0, 0, nil, sharePricingFlags{})
	if in != 0 || out != 0 || len(sched) != 0 {
		t.Errorf("no-profile model = %v/%v/%+v, want 0/0/nil", in, out, sched)
	}

	// Mixed: explicit price-out only -> out from flag, in from saved profile, and (no
	// schedule flag) the saved window IS appended.
	in, out, sched = seedSharePricing(cfg, "gpt-oss-20b", 0, 2.5, nil,
		sharePricingFlags{out: true})
	if in != 0.3 || out != 2.5 {
		t.Errorf("mixed price = %v/%v, want in=0.3 (saved) out=2.5 (flag)", in, out)
	}
	if len(sched) != 1 || !sched[0].Free {
		t.Errorf("mixed schedule = %+v, want the saved FREE window appended", sched)
	}
}

// TestOnAirLine verifies the single go-live success line (audit #5): the model,
// station, and a free-vs-earning mode, ending at the website.
func TestOnAirLine(t *testing.T) {
	free := onAirLine("gpt-oss-120b", "brave-otter-37", 0, 0, false)
	for _, want := range []string{"on air", "gpt-oss-120b", "brave-otter-37", "free", "rogerai.fyi"} {
		if !strings.Contains(free, want) {
			t.Errorf("free on-air line %q missing %q", free, want)
		}
	}
	earn := onAirLine("qwen3-coder", "calm-fox-9", 0.20, 0.30, false)
	for _, want := range []string{"qwen3-coder", "calm-fox-9", "earning", "$0.20", "$0.30", "rogerai.fyi"} {
		if !strings.Contains(earn, want) {
			t.Errorf("earning on-air line %q missing %q", earn, want)
		}
	}
	if strings.Contains(earn, "free") {
		t.Errorf("earning line should not say free: %q", earn)
	}
	if strings.Contains(earn, "broker override") {
		t.Errorf("non-override line should not mention an override: %q", earn)
	}
	// A broker-effective price set on the web Console is flagged so the operator knows the
	// published number is the broker override, not the locally requested one.
	ov := onAirLine("gpt-oss-120b", "brave-otter-37", 1.0, 2.0, true)
	for _, want := range []string{"$1", "$2", "broker override active"} {
		if !strings.Contains(ov, want) {
			t.Errorf("override on-air line %q missing %q", ov, want)
		}
	}
}

// TestEarningsLine: the provider money-OUT pointer printed under the go-live line
// names the dashboard and the payout-status verb so a fresh provider knows where
// earnings show up.
func TestEarningsLine(t *testing.T) {
	got := earningsLine()
	for _, want := range []string{"earnings", "rogerai.fyi/dashboard.html", "roger payout status"} {
		if !strings.Contains(got, want) {
			t.Errorf("earnings line %q missing %q", got, want)
		}
	}
}

// TestBalanceTopupAlias verifies the retired-but-still-working topup spellings under
// `balance` (C7 hidden aliases): `balance topup [usd]`, `balance --topup`, and the
// `--topup=<usd>` form, plus the bare `balance` (no alias).
func TestBalanceTopupAlias(t *testing.T) {
	cases := []struct {
		args    []string
		wantUSD float64
		wantOK  bool
	}{
		{nil, 0, false},                        // bare balance -> show, no topup
		{[]string{"topup"}, 10, true},          // positional, default $10
		{[]string{"topup", "25"}, 25, true},    // positional + amount
		{[]string{"topup", "$25"}, 25, true},   // $ prefix tolerated
		{[]string{"--topup"}, 10, true},        // flag, default $10
		{[]string{"--topup", "40"}, 40, true},  // flag + amount
		{[]string{"--topup=15"}, 15, true},     // flag=value
		{[]string{"--topup=$15"}, 15, true},    // flag=$value
		{[]string{"topup", "bogus"}, 10, true}, // bad amount -> default $10
	}
	for _, c := range cases {
		gotUSD, gotOK := balanceTopupAlias(c.args)
		if gotOK != c.wantOK || (gotOK && gotUSD != c.wantUSD) {
			t.Errorf("balanceTopupAlias(%v) = ($%.0f, %v), want ($%.0f, %v)", c.args, gotUSD, gotOK, c.wantUSD, c.wantOK)
		}
	}
}
