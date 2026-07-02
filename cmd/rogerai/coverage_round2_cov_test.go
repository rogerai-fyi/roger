package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/tui"
)

// dateServer stands up a broker whose /health (and every) response carries a FIXED Date
// header, so BrokerClockSkew computes a deterministic skew - letting cmdDrPhil's clock-skew
// warn/fail/negative branches run without depending on the test host's real clock.
func dateServer(t *testing.T, when time.Time) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", when.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// statusRouter serves a per-path (method-agnostic) HTTP status + body, so a handler that
// walks several broker endpoints can be driven down a specific failure branch.
func statusRouter(t *testing.T, routes map[string]struct {
	code int
	body string
}) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if rt, ok := routes[r.URL.Path]; ok {
			w.WriteHeader(rt.code)
			_, _ = w.Write([]byte(rt.body))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// --- Dr. Phil: parse error, malformed-URL auto-fix, clock-skew, strike branches ---

// TestCmdDrPhilParseError covers cmdDrPhil's flag-parse error return (an unknown flag is
// surfaced verbatim, before any diagnostic I/O).
func TestCmdDrPhilParseError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdDrPhil(config{Broker: "https://b", User: "u"}, []string{"--no-such-flag"}); err == nil {
		t.Fatal("cmdDrPhil(--no-such-flag) should surface the parse error")
	}
}

// TestCmdDrPhilMalformedFix covers the malformed-broker-URL + --fix auto-reset branch: the
// broken URL is reset to the default and persisted.
func TestCmdDrPhilMalformedFix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, nil, nil)
	if err := cmdDrPhil(config{Broker: "%%nope", User: "u"}, []string{"--fix"}); err != nil {
		t.Fatalf("cmdDrPhil(malformed, --fix) = %v", err)
	}
	if loadConfig().Broker != defaultBroker {
		t.Fatalf("--fix did not reset malformed broker to %q: %q", defaultBroker, loadConfig().Broker)
	}
}

// TestCmdDrPhilClockSkewWarn covers the 30s..2m clock-skew WARN branch: the broker's Date
// is ~60s in the past, so the local clock reads ~60s ahead.
func TestCmdDrPhilClockSkewWarn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"m"}}}, nil)
	broker := dateServer(t, time.Now().Add(-60*time.Second))
	out := captureStdout(t, func() {
		if err := cmdDrPhil(config{Broker: broker, User: "u"}, nil); err != nil {
			t.Fatalf("cmdDrPhil(skew warn) = %v", err)
		}
	})
	if !strings.Contains(out, "sync NTP soon") {
		t.Errorf("a 30s-2m skew should emit the WARN line (sync NTP soon); got:\n%s", out)
	}
	if strings.Contains(out, "REJECTED") {
		t.Errorf("a 30s-2m skew must NOT escalate to the FAIL line; got:\n%s", out)
	}
}

// TestCmdDrPhilClockSkewFail covers BOTH the negative-skew abs branch and the >2m FAIL
// branch: the broker's Date is 5 minutes in the FUTURE (local clock reads behind).
func TestCmdDrPhilClockSkewFail(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, nil, nil)
	broker := dateServer(t, time.Now().Add(5*time.Minute))
	out := captureStdout(t, func() {
		if err := cmdDrPhil(config{Broker: broker, User: "u"}, nil); err != nil {
			t.Fatalf("cmdDrPhil(skew fail) = %v", err)
		}
	})
	if !strings.Contains(out, "signatures will be REJECTED") {
		t.Errorf("a >2m skew should emit the FAIL line (signatures will be REJECTED); got:\n%s", out)
	}
}

// TestCmdDrPhilNoStrikesNoNodeBans covers the logged-in, reachable, clean-account branch:
// count 0 + no node bans -> the "no strikes" + "none suspended" OK lines.
func TestCmdDrPhilNoStrikesNoNodeBans(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"m"}}}, nil)
	broker := brokerRouting(t, map[string]string{
		"/owner/strikes": `{"banned":false,"count":0,"node_bans":{}}`,
	})
	if err := cmdDrPhil(config{Broker: broker, User: "u"}, nil); err != nil {
		t.Fatalf("cmdDrPhil(clean) = %v", err)
	}
}

// TestCmdDrPhilStrikesUnreadable covers the logged-in + reachable but FetchStrikes-errors
// branch: a 500 on /owner/strikes yields the "could not read your strike/ban status" warn.
func TestCmdDrPhilStrikesUnreadable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"m"}}}, nil)
	broker := statusRouter(t, map[string]struct {
		code int
		body string
	}{
		"/owner/strikes": {http.StatusInternalServerError, `{"error":{"message":"boom"}}`},
	})
	if err := cmdDrPhil(config{Broker: broker, User: "u"}, nil); err != nil {
		t.Fatalf("cmdDrPhil(strikes unreadable) = %v", err)
	}
}

// TestSummarizeModelsDedup covers summarizeModels' empty/duplicate skip (the existing test
// only feeds a 4-distinct list that breaks at 3).
func TestSummarizeModelsDedup(t *testing.T) {
	if got := summarizeModels([]string{"a", "", "a", "b"}); got != "a, b, ..." {
		t.Fatalf("summarizeModels(dup/empty) = %q, want %q", got, "a, b, ...")
	}
}

// --- cmdAppeal: not-logged-in status, list error, parse error, file error ---

// TestCmdAppealStatusNotLoggedIn covers the `appeal status` login gate (no auth.json).
func TestCmdAppealStatusNotLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdAppeal(config{Broker: "https://b", User: "u"}, []string{"status"}); err == nil {
		t.Fatal("appeal status without login should error")
	}
}

// TestCmdAppealListError covers the ListAppeals error surface (broker 500 on GET).
func TestCmdAppealListError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	broker := statusRouter(t, map[string]struct {
		code int
		body string
	}{"/owner/appeal": {http.StatusInternalServerError, `{}`}})
	if err := cmdAppeal(config{Broker: broker, User: "u"}, []string{"status"}); err == nil {
		t.Fatal("appeal status with a failing broker should surface the error")
	}
}

// TestCmdAppealParseError covers the flag-parse error path of `roger appeal` (an unknown
// flag, not the status/list subcommand).
func TestCmdAppealParseError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	if err := cmdAppeal(config{Broker: "https://b", User: "u"}, []string{"--bogus"}); err == nil {
		t.Fatal("appeal with an unknown flag should surface the parse error")
	}
}

// TestCmdAppealFileError covers the FileAppeal error surface (broker 500 on POST) on the
// fully-validated file path.
func TestCmdAppealFileError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	broker := statusRouter(t, map[string]struct {
		code int
		body string
	}{"/owner/appeal": {http.StatusInternalServerError, `{}`}})
	if err := cmdAppeal(config{Broker: broker, User: "u"}, []string{"--reason", "false positive"}); err == nil {
		t.Fatal("appeal file against a failing broker should surface the error")
	}
}

// --- grant: no-args/help/unknown, recover-secret branches ---

// TestCmdGrantNoArgsHelpUnknown covers cmdGrant's no-args usage, the help alias, and the
// unknown-subcommand error.
func TestCmdGrantNoArgsHelpUnknown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}
	if err := cmdGrant(cfg, nil); err != nil {
		t.Errorf("cmdGrant(nil) = %v, want nil (usage)", err)
	}
	if err := cmdGrant(cfg, []string{"help"}); err != nil {
		t.Errorf("cmdGrant(help) = %v, want nil", err)
	}
	if err := cmdGrant(cfg, []string{"frobnicate"}); err == nil {
		t.Error("cmdGrant(unknown) should error")
	}
}

// TestGrantRecoverSecretRotates covers the happy free-key rotation: list -> found free ->
// revoke -> mint a fresh secret (printed once).
func TestGrantRecoverSecretRotates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	cfg := config{Broker: fakeBroker(t), User: "u"}
	if err := cmdGrant(cfg, []string{"show", "--secret", "petlings"}); err != nil {
		t.Fatalf("grant show --secret (free rotate) = %v", err)
	}
}

// TestGrantRecoverSecretNonFree covers the refusal to rotate a PRICED grant (its caps/scope
// can't be reconstructed): an error pointing the user at revoke + create.
func TestGrantRecoverSecretNonFree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	broker := brokerRouting(t, map[string]string{
		"/grants": `{"grants":[{"id":"g2","name":"paid","free":false,"price":"$0.30","status":"active"}]}`,
	})
	if err := cmdGrant(config{Broker: broker, User: "u"}, []string{"show", "--secret", "paid"}); err == nil {
		t.Fatal("recovering a priced grant should refuse (can't reconstruct caps)")
	}
}

// TestGrantRecoverSecretListError covers the GrantListRows error surface (broker 403 on
// GET /grants).
func TestGrantRecoverSecretListError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	broker := statusRouter(t, map[string]struct {
		code int
		body string
	}{"/grants": {http.StatusForbidden, `{}`}})
	if err := cmdGrant(config{Broker: broker, User: "u"}, []string{"show", "--secret", "x"}); err == nil {
		t.Fatal("recover-secret with a 403 grants list should error")
	}
}

// TestGrantRecoverSecretRevokeError covers the GrantRevoke error in the rotate path: the
// grant lists as free, but the DELETE fails.
func TestGrantRecoverSecretRevokeError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"grants":[{"id":"g1","name":"petlings","free":true,"price":"free","status":"active"}]}`))
	}))
	t.Cleanup(srv.Close)
	if err := cmdGrant(config{Broker: srv.URL, User: "u"}, []string{"show", "--secret", "petlings"}); err == nil {
		t.Fatal("recover-secret should surface a failing revoke")
	}
}

// TestGrantRecoverSecretCreateError covers the GrantCreateSecret error in the rotate path:
// list + revoke succeed, but minting the fresh secret fails.
func TestGrantRecoverSecretCreateError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK) // revoke ok
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError) // mint fails
		default:
			_, _ = w.Write([]byte(`{"grants":[{"id":"g1","name":"petlings","free":true,"price":"free","status":"active"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	if err := cmdGrant(config{Broker: srv.URL, User: "u"}, []string{"show", "--secret", "petlings"}); err == nil {
		t.Fatal("recover-secret should surface a failing secret mint")
	}
}

// TestCmdGrantCreateBadExpires covers cmdGrantCreate's parseExpires error branch.
func TestCmdGrantCreateBadExpires(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	if err := cmdGrantCreate(config{Broker: "https://b", User: "u"}, []string{"--name", "k", "--expires", "garbage"}); err == nil {
		t.Fatal("grant create with a bad --expires should error")
	}
}

// --- payout: status onboarding/below-min/error, onboard error ---

// TestPayoutStatusOnboardingBranch covers the KYC-not-active actionable line in payoutStatus.
func TestPayoutStatusOnboardingBranch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broker := brokerRouting(t, map[string]string{
		"/connect/status": `{"status":"onboarding","min_payout":25,"earnings":{"payable":0,"held":2}}`,
	})
	out := captureStdout(t, func() {
		if err := payoutStatus(config{Broker: broker, User: "u"}); err != nil {
			t.Fatalf("payoutStatus(onboarding) = %v", err)
		}
	})
	if !strings.Contains(out, "complete KYC to cash out") {
		t.Errorf("an onboarding (KYC-not-active) account should print the complete-KYC next step; got:\n%s", out)
	}
}

// TestPayoutStatusBelowMinBranch covers the active-but-below-minimum actionable line + the
// "paid out" + "next due" optional lines.
func TestPayoutStatusBelowMinBranch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broker := brokerRouting(t, map[string]string{
		"/connect/status": `{"status":"active","min_payout":25,"earnings":{"payable":5,"held":3,"paid":40,"next_release":1893456000}}`,
	})
	out := captureStdout(t, func() {
		if err := payoutStatus(config{Broker: broker, User: "u"}); err != nil {
			t.Fatalf("payoutStatus(below-min) = %v", err)
		}
	})
	if !strings.Contains(out, "below the $25 minimum") {
		t.Errorf("payable $5 < $25 min should print the below-minimum next step; got:\n%s", out)
	}
	if !strings.Contains(out, "paid out") {
		t.Errorf("a non-zero lifetime paid total should print the paid-out line; got:\n%s", out)
	}
	if !strings.Contains(out, "next due") {
		t.Errorf("a future next_release should print the next-due line; got:\n%s", out)
	}
}

// TestPayoutStatusError covers payoutStatus' FetchPayoutStatus error surface (broker 500).
func TestPayoutStatusError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broker := statusRouter(t, map[string]struct {
		code int
		body string
	}{"/connect/status": {http.StatusInternalServerError, `{}`}})
	if err := payoutStatus(config{Broker: broker, User: "u"}); err == nil {
		t.Fatal("payoutStatus against a failing broker should surface the error")
	}
}

// TestPayoutOnboardError covers payoutOnboard's FetchOnboardURL error surface (broker 500).
func TestPayoutOnboardError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broker := statusRouter(t, map[string]struct {
		code int
		body string
	}{"/connect/onboard": {http.StatusInternalServerError, `{}`}})
	if err := payoutOnboard(config{Broker: broker, User: "u"}); err == nil {
		t.Fatal("payoutOnboard against a failing broker should surface the error")
	}
}

// --- limit / set-limit / account / config edges ---

// TestCmdLimitErrors covers cmdLimit's GetMonthlyLimit (read) and SetMonthlyLimit (write)
// error surfaces against a dead broker.
func TestCmdLimitErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close() // connection refused
	cfg := config{Broker: url, User: "u"}
	if err := cmdLimit(cfg, nil); err == nil {
		t.Error("cmdLimit(read) against a dead broker should error")
	}
	if err := cmdLimit(cfg, []string{"--monthly", "25"}); err == nil {
		t.Error("cmdLimit(set) against a dead broker should error")
	}
}

// TestCmdSetLimitExistingModel covers cmdSetLimit's no-args error and the
// existing-model-in-map update path (cur seeded from the saved limit).
func TestCmdSetLimitExistingModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdSetLimit(nil); err == nil {
		t.Error("set-limit with no model should error")
	}
	// Seed a limit for m1 (creates the Models map), then update it -> the existing-model
	// read branch runs (cur = c.Limits.Models[model]).
	if err := cmdSetLimit([]string{"m1", "--max-out", "2"}); err != nil {
		t.Fatalf("set-limit(seed) = %v", err)
	}
	if err := cmdSetLimit([]string{"m1", "--min-tps", "30"}); err != nil {
		t.Fatalf("set-limit(update) = %v", err)
	}
	got := loadConfig().Limits.Models["m1"]
	if got.MaxOut != 2 || got.MinTPS != 30 {
		t.Fatalf("set-limit update did not preserve+merge: %+v", got)
	}
}

// TestCmdConfigSetLimitRoute covers cmdConfig's set-limit subcommand route (delegating to
// cmdSetLimit) and the default-model fallback limit.
func TestCmdConfigSetLimitRoute(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdConfig([]string{"set-limit", "default", "--max-out", "1"}); err != nil {
		t.Fatalf("config set-limit default = %v", err)
	}
	if loadConfig().Limits.Default.MaxOut != 1 {
		t.Fatalf("config set-limit default did not persist: %+v", loadConfig().Limits.Default)
	}
}

// TestCmdAccountLogoutAndUnknown covers cmdAccount's logout alias and the unknown-subcommand
// usage error (the login alias hits live OAuth and is out of scope).
func TestCmdAccountLogoutAndUnknown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}
	if err := cmdAccount(cfg, []string{"logout"}); err != nil {
		t.Errorf("cmdAccount(logout) = %v, want nil", err)
	}
	if err := cmdAccount(cfg, []string{"bogus"}); err == nil {
		t.Error("cmdAccount(bogus) should error with usage")
	}
}

// --- cmdShare branch coverage ---

// TestCmdShareEarnDisclosureAndPriceWarn covers the EARN disclosure line + the soft
// price-typo warning on a logged-in priced share whose price is far above the market median.
func TestCmdShareEarnDisclosureAndPriceWarn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t) // earning needs a linked owner
	stubShareSeams(t)
	cfg := config{Broker: fakeBroker(t), User: "u"} // fakeBroker prices m1 at 2.0 $/1M out
	out := captureStdout(t, func() {
		runShareAsync(t, cfg, []string{"m1", "--upstream", "http://127.0.0.1:1234/v1", "--price-out", "100"})
	})
	if !strings.Contains(out, "earning: payouts are 120-day hold") {
		t.Errorf("a priced (earning) share should pre-disclose the payout policy once; got:\n%s", out)
	}
	if !strings.Contains(out, "double-check it's not a typo") {
		t.Errorf("100 $/1M out is 50x the median 2.0 - the soft price-typo warning should fire; got:\n%s", out)
	}
}

// TestCmdShareNodeRenameAndSchedule covers the --node station rename (persisted) plus the
// --free-window and --schedule flag-set branches (fs.Visit + schedule build).
func TestCmdShareNodeRenameAndSchedule(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	cfg := config{Broker: "https://b", User: "u"}
	runShareAsync(t, cfg, []string{"m1",
		"--upstream", "http://127.0.0.1:1234/v1",
		"--node", "Brave Otter",
		"--free-window", "03:00-03:30",
		"--schedule", `[{"start":"18:00","end":"22:00","price_in":0.5,"price_out":0.7}]`,
	})
	if got := loadOrCreateStation(); got != "brave-otter" {
		t.Fatalf("--node rename not persisted: station=%q, want brave-otter", got)
	}
}

// TestCmdShareNoDetectionNeedsKey covers the non-interactive detect-nothing-but-key-needed
// branch: no --upstream, detection reports a key-protected server, guided fallback declines
// (no TTY) -> the "--upstream-key" error.
func TestCmdShareNoDetectionNeedsKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectFull(t, nil, []string{"http://127.0.0.1:8000/v1"})
	err := cmdShare(config{Broker: "https://b", User: "u"}, []string{"m1"})
	if err == nil {
		t.Fatal("share with a key-protected, undetected upstream should error")
	}
}

// TestCmdShareNoDetectionNoServer covers the non-interactive detect-nothing branch: the
// plain "no local LLM detected" error.
func TestCmdShareNoDetectionNoServer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectFull(t, nil, nil)
	if err := cmdShare(config{Broker: "https://b", User: "u"}, []string{"m1"}); err == nil {
		t.Fatal("share with nothing detected should error")
	}
}

// TestCmdShareExplicitUpstreamHarvestsEnvKey covers the explicit --upstream-without-key
// branch that best-effort harvests a working key from the environment via the probe seam.
func TestCmdShareExplicitUpstreamHarvestsEnvKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectProbeKey(t, func(url, key string) (detect.Found, detect.Status) {
		return detect.Found{BaseURL: url, Chat: url, Models: []string{"m1"}, Key: "sk-env"}, detect.Reachable
	})
	runShareAsync(t, config{Broker: "https://b", User: "u"},
		[]string{"m1", "--upstream", "http://127.0.0.1:5555/v1"})
}

// TestCmdShareNoModelError covers the "could not determine a model" error: an explicit
// --upstream that the probe can't reach, with no model given anywhere.
func TestCmdShareNoModelError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectProbeKey(t, func(string, string) (detect.Found, detect.Status) {
		return detect.Found{}, detect.Unreachable
	})
	if err := cmdShare(config{Broker: "https://b", User: "u"},
		[]string{"--upstream", "http://127.0.0.1:5555/v1"}); err == nil {
		t.Fatal("share with no resolvable model should error")
	}
}

// TestCmdSharePublicAgentStartError covers the public-path agentStart error surface.
func TestCmdSharePublicAgentStartError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	origStart, origBlock := agentStart, shareBlock
	agentStart = func(agent.Config) (*agent.Session, error) { return nil, fmt.Errorf("register boom") }
	shareBlock = func() {}
	t.Cleanup(func() { agentStart, shareBlock = origStart, origBlock })
	if err := cmdShare(config{Broker: "https://b", User: "u"},
		[]string{"m1", "--upstream", "http://127.0.0.1:1/v1"}); err == nil {
		t.Fatal("share should surface an agentStart failure (public path)")
	}
}

// TestCmdSharePrivateAgentStartError covers the private-path agentStart error surface.
func TestCmdSharePrivateAgentStartError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t) // private requires a linked owner
	origStart, origBlock := agentStart, shareBlock
	agentStart = func(agent.Config) (*agent.Session, error) { return nil, fmt.Errorf("private boom") }
	shareBlock = func() {}
	t.Cleanup(func() { agentStart, shareBlock = origStart, origBlock })
	if err := cmdShare(config{Broker: "https://b", User: "u"},
		[]string{"m1", "--upstream", "http://127.0.0.1:1/v1", "--private"}); err == nil {
		t.Fatal("share should surface an agentStart failure (private path)")
	}
}

// --- dispatch routes: share, onboard, logout ---

// TestDispatchShareOnboard covers the dispatch routes for `share` and `onboard` (the
// interactive verbs not exercised by TestDispatchRouting).
func TestDispatchShareOnboard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"m"}}}, nil)
	cfg := config{Broker: "https://b", User: "u"}

	done := make(chan error, 1)
	go func() { done <- dispatch(cfg, []string{"share", "m", "--upstream", "http://127.0.0.1:1234/v1"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dispatch(share) = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch(share) did not return")
	}

	if err := dispatch(cfg, []string{"onboard", "--free"}); err != nil {
		t.Fatalf("dispatch(onboard --free) = %v", err)
	}
}

// TestDispatchLogout covers the dispatch `logout` route (deletes the local auth binding).
func TestDispatchLogout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	if err := dispatch(config{Broker: "https://b", User: "u"}, []string{"logout"}); err != nil {
		t.Fatalf("dispatch(logout) = %v", err)
	}
}

// --- tuiHooks closures: GrantList (ok + error), LoginBegin/LoginPoll error paths ---

// TestTuiHookGrantListClosure drives the GrantList hook closure both ways: a working broker
// (rows mapped to tui.GrantRow) and a 403 broker (error surfaced).
func TestTuiHookGrantListClosure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	h := tuiHooks(config{Broker: "https://b", User: "u"})

	rows, err := h.GrantList(fakeBroker(t))
	if err != nil {
		t.Fatalf("GrantList(ok) = %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "petlings" {
		t.Fatalf("GrantList rows wrong: %+v", rows)
	}

	denied := statusRouter(t, map[string]struct {
		code int
		body string
	}{"/grants": {http.StatusForbidden, `{}`}})
	if _, err := h.GrantList(denied); err == nil {
		t.Fatal("GrantList against a 403 broker should error")
	}
}

// TestTuiHookLoginClosuresError covers the LoginBegin (empty client id) + LoginPoll (bad
// handle) hook closure error paths without any live OAuth round-trip.
func TestTuiHookLoginClosuresError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := tuiHooks(config{Broker: "https://b", User: "u"})
	if _, err := h.LoginBegin("https://b", ""); err == nil {
		t.Fatal("LoginBegin with an empty client id should error")
	}
	if _, err := h.LoginPoll("https://b", "cid", tui.LoginDevice{}); err == nil {
		// LoginPoll with a zero/invalid device handle must error ("invalid login handle").
		t.Fatal("LoginPoll with a zero device should error")
	}
}

// --- onboard: non-interactive short-circuit ---

// TestCmdOnboardNoFlags covers `roger onboard` with no flags on a non-TTY run: runWizard
// returns the config unchanged and cmdOnboard still saves it.
func TestCmdOnboardNoFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdOnboard(config{Broker: "https://b", User: "u"}, nil); err != nil {
		t.Fatalf("cmdOnboard(no flags) = %v", err)
	}
}

// --- on-air lock edges ---
// (TestLockSlugSanitizes moved with lockSlug to internal/onair - see
// onair.TestLockSlugKeepsNodeIDsFilesystemSafe, which keeps its exact case.)

// TestOnAirLockEmptyStationMessage covers the "this machine" fallback in the already-on-air
// error when the held lock has no station recorded.
func TestOnAirLockEmptyStationMessage(t *testing.T) {
	useTempConfig(t)
	other := os.Getppid()
	if other == os.Getpid() || !processAlive(other) {
		t.Skip("no distinct live parent PID to simulate another daemon")
	}
	held := onAirInfo{PID: other, Station: "", Model: "m1", Started: 1} // empty station
	b, _ := json.Marshal(held)
	path := onAirLockPath("brave-otter-m1")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireOnAirLock("brave-otter-m1", "brave-otter", "m1"); err == nil {
		t.Fatal("acquire should be blocked by a live held lock (empty station)")
	}
}
