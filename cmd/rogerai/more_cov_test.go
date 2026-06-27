package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/tui"
)

// fakeBrokerEmpty serves an empty offer set, so client.Use reports "no station on air"
// and returns before its blocking relay - making cmdUse's no-station path testable.
func fakeBrokerEmpty(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"offers":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestScheduleWindowConverters covers the round-trip between the config/TUI/protocol
// schedule-window shapes (empty -> nil, and field-preserving).
func TestScheduleWindowConverters(t *testing.T) {
	if toCfgWindows(nil) != nil || toTUIWindows(nil) != nil || toProtocolWindows(nil) != nil {
		t.Error("empty window slices should convert to nil")
	}
	tw := []tui.SchedWindow{{Start: "22:00", End: "06:00", In: 1, Out: 2, Free: true}}
	cfg := toCfgWindows(tw)
	if len(cfg) != 1 || cfg[0].Start != "22:00" || !cfg[0].Free {
		t.Fatalf("toCfgWindows lost fields: %+v", cfg)
	}
	back := toTUIWindows(cfg)
	if len(back) != 1 || back[0].Out != 2 {
		t.Fatalf("toTUIWindows lost fields: %+v", back)
	}
	if p := toProtocolWindows(cfg); len(p) != 1 || p[0].Start != "22:00" {
		t.Fatalf("toProtocolWindows lost fields: %+v", p)
	}
}

// TestStatusline covers the diagnostic status-line marks.
func TestStatusline(t *testing.T) {
	statusline("ok", "fine")
	statusline("warn", "careful")
	statusline("fail", "broken")
	if !validBrokerURL("https://broker.rogerai.fyi") || validBrokerURL("not-a-url") {
		t.Error("validBrokerURL wrong")
	}
}

// TestSoftPriceWarn covers the typo guard: a price far above the market median warns; a
// sane price (or no price / no market) is silent.
func TestSoftPriceWarn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := fakeBroker(t) // m1 priced at 2.0 $/1M out, online
	if softPriceWarn(b, "m1", 0) != "" {
		t.Error("no price -> no warning")
	}
	if softPriceWarn(b, "m1", 2) != "" {
		t.Error("a sane price (~median) -> no warning")
	}
	if softPriceWarn(b, "m1", 100) == "" { // 100 >> 3*2 median
		t.Error("a price far above the median should warn")
	}
}

// TestTuiBuilders covers tuiLimits + tuiHooks built from a config.
func TestTuiBuilders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}
	cfg.Limits.Models = map[string]Limit{"m1": {MaxOut: 2, MinTPS: 5}}
	cfg.Limits.TypicalOutTok = 500
	ls := tuiLimits(cfg)
	if ls == nil {
		t.Fatal("tuiLimits should build a store")
	}
	h := tuiHooks(cfg)
	if h.Station == "" {
		t.Error("tuiHooks should carry a station callsign")
	}
}

// TestConfigSetAndLimit covers the config mutators: set broker/user, set-limit +
// clear-limit, limits view, and get.
func TestConfigSetAndLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdConfig([]string{"set", "broker", "https://example.test/"}); err != nil {
		t.Fatalf("config set broker: %v", err)
	}
	if loadConfig().Broker != "https://example.test" {
		t.Errorf("set broker did not persist (trailing slash trimmed): %q", loadConfig().Broker)
	}
	if err := cmdConfig([]string{"set", "user", "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetLimit([]string{"m1", "--max-out", "3", "--min-tps", "10"}); err != nil {
		t.Fatalf("set-limit: %v", err)
	}
	if loadConfig().Limits.Models["m1"].MaxOut != 3 {
		t.Errorf("set-limit did not persist: %+v", loadConfig().Limits.Models["m1"])
	}
	if err := cmdConfig([]string{"clear-limit", "m1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadConfig().Limits.Models["m1"]; ok {
		t.Error("clear-limit should remove the model limit")
	}
	// View + get subcommands print + return nil.
	for _, args := range [][]string{{"limits"}, {"get"}, {"get", "broker"}, {}} {
		if err := cmdConfig(args); err != nil {
			t.Errorf("cmdConfig(%v) = %v", args, err)
		}
	}
}

// TestCmdDrPhilAndUse covers the Dr. Phil diagnostic (drives many statuslines against a
// fake broker) and cmdUse's no-station early return.
func TestCmdDrPhilAndUse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBroker(t), User: "u_gh_1"}
	if err := cmdDrPhil(cfg, nil); err != nil {
		t.Errorf("cmdDrPhil = %v, want nil", err)
	}
	// cmdUse against an empty market returns cleanly (no blocking relay).
	noStation := config{Broker: fakeBrokerEmpty(t), User: "u_gh_1"}
	if err := cmdUse(noStation, []string{"ghost-model"}); err != nil {
		t.Errorf("cmdUse(no station) = %v, want nil", err)
	}
}

// TestCmdSharePricedNeedsLogin covers cmdShare's up-front EARN login-gate: a priced
// share without a GitHub link fails fast (before any upstream detection or register),
// pointing the would-be earner at `roger login`.
func TestCmdSharePricedNeedsLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no auth.json -> not logged in
	cfg := config{Broker: "https://b", User: "u"}
	if err := cmdShare(cfg, []string{"--price-out", "0.5"}); err == nil {
		t.Error("a priced share without login should error (earning needs a linked owner)")
	}
}

// TestUsagePrinters covers the help/usage printers (pure prints, must not panic).
func TestUsagePrinters(t *testing.T) {
	payoutUsage()
	grantUsage()
	usage()
}
