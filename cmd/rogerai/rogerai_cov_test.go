package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
)

// fakeBroker answers every signed broker call the CLI makes with one permissive JSON
// blob, so the command handlers can be driven to their success path without a live broker.
func fakeBroker(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"user":"u_gh_1","balance":12.5,"logged_in":true,"monthly_cap":100,"monthly_spend":20,"cap":100,
			"url":"https://pay.example/checkout","credits":10,
			"offers":[{"node_id":"amber-fox-m1","model":"m1","price_out":2.0,"online":true}],
			"strikes":[],"appeals":[],"appeal":{"id":1,"state":"open"},"ok":true,
			"github_login":"octocat","github_id":7,
			"grants":[{"id":"grant_1","name":"petlings","free":true,"status":"active","price":"free"}],
			"grant":{"id":"grant_1","name":"petlings"},"secret":"rog-grant_abc",
			"status":"active","connected":true,"hold_days":120,"min_payout":25,"schedule":"monthly",
			"earnings":{"payable":30,"held":5,"reserved":1,"paid":10,"next_release":1893456000},
			"payout":{"id":1,"amount":0,"state":"pending"},"payouts":[]
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// writeAuth seeds a local auth.json so client.LinkedLogin()!="" (the login gate on the
// payout/appeal verbs passes).
func writeAuth(t *testing.T) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "rogerai")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "auth.json"),
		[]byte(`{"github_login":"octocat","github_id":7,"bound_at":1}`), 0o600)
}

// TestPureHelpers covers the CLI's pure formatting/parsing helpers.
func TestPureHelpers(t *testing.T) {
	if orDash("  ") != "(no reason recorded)" || orDash("x") != "x" {
		t.Error("orDash wrong")
	}
	if got, err := parseExpires("2026-12-31"); err != nil || got <= 0 {
		t.Errorf("parseExpires(date) = %d/%v", got, err)
	}
	if got, err := parseExpires("30d"); err != nil || got <= 0 {
		t.Errorf("parseExpires(30d) = %d/%v", got, err)
	}
	if got, err := parseExpires("2h"); err != nil || got <= 0 {
		t.Errorf("parseExpires(2h) = %d/%v", got, err)
	}
	if _, err := parseExpires("garbage"); err == nil {
		t.Error("parseExpires(garbage) should error")
	}
	if s := splitCSV(" a, b ,,c "); len(s) != 3 || s[0] != "a" || s[2] != "c" {
		t.Errorf("splitCSV = %v", s)
	}
	if splitCSV("  ") != nil {
		t.Error("splitCSV(blank) should be nil")
	}
	if parsePrice("1.5") != 1.5 || parsePrice("-1") != 0 || parsePrice("x") != 0 {
		t.Error("parsePrice wrong")
	}
	if payoutDate(0) != "-" || payoutDate(1_700_000_000) == "-" {
		t.Error("payoutDate wrong")
	}
	for st, want := range map[string]string{"active": "active (KYC complete)", "onboarding": "pending (finish onboarding)", "restricted": "restricted (Stripe needs more info)", "none": "not onboarded"} {
		if kycLabel(st) != want {
			t.Errorf("kycLabel(%q) = %q, want %q", st, kycLabel(st), want)
		}
	}
	if holdOr90(client.PayoutStatus{}) != 120 || holdOr90(client.PayoutStatus{HoldDays: 30}) != 30 {
		t.Error("holdOr90 wrong")
	}
	if minOr25(client.PayoutStatus{}) != 25 || minOr25(client.PayoutStatus{MinPayout: 50}) != 50 {
		t.Error("minOr25 wrong")
	}
	if limitStr(Limit{}) != "no caps" || limitStr(Limit{MaxOut: 2, MinTPS: 5}) == "no caps" {
		t.Error("limitStr wrong")
	}
	if trimAmt(25) != "25" || trimAmt(25.5) != "25.50" {
		t.Errorf("trimAmt wrong: %q %q", trimAmt(25), trimAmt(25.5))
	}
	if summarizeModels(nil) != "(none reported)" || summarizeModels([]string{"a", "b", "c", "d"}) == "" {
		t.Error("summarizeModels wrong")
	}
	if payoutPolicyLine(client.PayoutStatus{}) == "" {
		t.Error("payoutPolicyLine should render a policy line")
	}
	if agentSlugStation("Brave Otter!") == "" {
		t.Error("agentSlugStation should slugify")
	}
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "cid123")
	if gitHubClientID() != "cid123" {
		t.Error("gitHubClientID env override")
	}
}

// TestStationConfig covers the station callsign create + rename + reload cycle.
func TestStationConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first := loadOrCreateStation()
	if first == "" {
		t.Fatal("loadOrCreateStation should auto-generate a callsign")
	}
	if again := loadOrCreateStation(); again != first {
		t.Errorf("station should persist: %q vs %q", first, again)
	}
	saveStation("Amber Fox")
	if got := loadOrCreateStation(); got != "amber-fox" {
		t.Errorf("rename = %q, want amber-fox", got)
	}
	saveStation("") // empty rename is ignored
	if got := loadOrCreateStation(); got != "amber-fox" {
		t.Errorf("blank rename must not clear: %q", got)
	}
}

// TestFreePortReachable covers freePort (binds a free port) and reachable (health probe).
func TestFreePortReachable(t *testing.T) {
	p, err := freePort(40000)
	if err != nil || p < 40000 {
		t.Fatalf("freePort = %d/%v", p, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if !reachable(srv.URL) {
		t.Error("reachable should be true for a live server")
	}
	if reachable("http://127.0.0.1:0") {
		t.Error("reachable should be false for a dead address")
	}
}

// TestCommandHandlers drives the broker-facing command handlers against a fake broker.
func TestCommandHandlers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t) // satisfy the login gate on payout/appeal
	cfg := config{Broker: fakeBroker(t), User: "u_gh_1"}

	mustNil := func(name string, err error) {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	mustNil("Search", client.Search(cfg.Broker))
	mustNil("cmdBalance", cmdBalance(cfg, nil))
	mustNil("cmdAccount", cmdAccount(cfg, nil))
	mustNil("cmdLimit get", cmdLimit(cfg, nil))
	mustNil("cmdLimit set", cmdLimit(cfg, []string{"--monthly", "25"}))
	mustNil("cmdGrant list", cmdGrant(cfg, []string{"list"}))
	mustNil("cmdGrant show", cmdGrant(cfg, []string{"show", "petlings"}))
	mustNil("cmdGrant revoke", cmdGrant(cfg, []string{"revoke", "petlings"}))
	mustNil("cmdGrant create", cmdGrant(cfg, []string{"create", "--name", "newkey", "--free"}))
	mustNil("cmdPayout status", cmdPayout(cfg, []string{"status"}))
	mustNil("cmdPayout onboard", cmdPayout(cfg, []string{"onboard"}))
	mustNil("cmdPayout request", cmdPayout(cfg, []string{"request"}))
	mustNil("cmdPayout history", cmdPayout(cfg, []string{"history"}))
	mustNil("cmdAppeal status", cmdAppeal(cfg, []string{"status"}))
	mustNil("cmdConfig", cmdConfig(nil))
	_ = time.Now
}

// TestDispatchRouting covers the argv router for the non-interactive verbs + version/
// help/empty/unknown (the interactive use/share/onboard verbs launch stdin/TUI flows and
// are exercised by their own handlers, not here).
func TestDispatchRouting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	cfg := config{Broker: fakeBroker(t), User: "u_gh_1"}

	// NOTE: "logout" is deliberately excluded - it deletes the local auth file, which
	// would break the login-gated verbs (payout/appeal) later in the loop. Logout is
	// covered directly in the client package tests.
	for _, args := range [][]string{
		{"search"}, {"balance"}, {"account"}, {"whoami"},
		{"topup", "5"}, {"limit"}, {"payout", "status"}, {"grant", "list"},
		{"config"}, {"support"}, {"appeal", "status"}, {"version"}, {"help"}, {},
	} {
		if err := dispatch(cfg, args); err != nil {
			t.Errorf("dispatch(%v) = %v, want nil", args, err)
		}
	}
	// Unknown command -> the sentinel error (main turns it into exit 1).
	if err := dispatch(cfg, []string{"bogus-verb"}); err != errUnknownCommand {
		t.Errorf("dispatch(unknown) = %v, want errUnknownCommand", err)
	}
}

// TestHWDetection covers the Linux hardware probe (returns whatever the host has; the
// point is to exercise the detection code paths, GPU-less or not).
func TestHWDetection(t *testing.T) {
	hw := detectHW()
	if hw == "" {
		t.Error("detectHW should return a non-empty descriptor")
	}
	if detectHWClass() == "" {
		t.Error("detectHWClass should return a class label")
	}
	// GPU counts are >= 0 (0 on a GPU-less CI box) - just exercise the probes.
	if nvidiaGPUCount() < 0 || rocmGPUCount() < 0 {
		t.Error("GPU counts must be non-negative")
	}
	// runHW is a small command-runner helper: a known command returns output, a missing
	// one returns "" (never panics).
	if out, ok := runHW("echo", "hi"); !ok || out == "" {
		t.Errorf("runHW(echo) = %q/%v, want output", out, ok)
	}
	if _, ok := runHW("definitely-not-a-real-binary-xyz"); ok {
		t.Error("runHW(missing) should report ok=false")
	}
}
