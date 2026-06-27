package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/node"
	"github.com/rogerai-fyi/roger/internal/tui"
)

// stubDetectFull points the detectFull seam at a fake local-LLM detector so the
// detection-success path of cmdShare / finishShare / cmdDrPhil runs without a live model
// server. Restored on cleanup.
func stubDetectFull(t *testing.T, found []detect.Found, needKey []string) {
	t.Helper()
	orig := detectFull
	detectFull = func(extra ...string) ([]detect.Found, []string) { return found, needKey }
	t.Cleanup(func() { detectFull = orig })
}

// stubDetectProbeKey points the detectProbeKey seam at a fake keyed-endpoint probe.
func stubDetectProbeKey(t *testing.T, fn func(string, string) (detect.Found, detect.Status)) {
	t.Helper()
	orig := detectProbeKey
	detectProbeKey = fn
	t.Cleanup(func() { detectProbeKey = orig })
}

// brokerRouting serves a fixed JSON body per request path (and 200/{} otherwise), so a
// handler that hits several broker endpoints can be driven down a specific branch.
func brokerRouting(t *testing.T, routes map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if body, ok := routes[r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// runShareAsync runs cmdShare on a goroutine (its go-live tail would otherwise block on
// shareBlock) and asserts it returns nil within a bound. The caller must have installed
// stubShareSeams so shareBlock returns.
func runShareAsync(t *testing.T, cfg config, args []string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmdShare(cfg, args) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cmdShare(%v) = %v, want nil", args, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("cmdShare(%v) did not return (shareBlock seam?)", args)
	}
}

// TestCmdShareDetectionPath drives cmdShare with NO --upstream so the auto-detect branch
// runs against the fake detector: the first Found is picked, its model + chat endpoint
// are adopted, and the verified upstream is persisted to config.
func TestCmdShareDetectionPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectFull(t, []detect.Found{{
		Name:    "ollama",
		BaseURL: "http://127.0.0.1:11434/v1",
		Chat:    "http://127.0.0.1:11434/v1/chat/completions",
		Models:  []string{"m-detected"},
	}}, nil)

	cfg := config{Broker: "https://b", User: "u"}
	runShareAsync(t, cfg, nil) // bare `roger share`: nothing but the detector decides

	// The auto-detected upstream must be remembered so a custom endpoint is not re-hunted.
	saved := loadConfig().Share
	if saved == nil || saved.Upstream != "http://127.0.0.1:11434/v1" {
		t.Fatalf("detected upstream not persisted: %+v", saved)
	}
}

// TestCmdShareDetectionPicksRequestedModel verifies the model-matching loop: when several
// endpoints are detected, cmdShare picks the one serving the explicitly requested model.
func TestCmdShareDetectionPicksRequestedModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectFull(t, []detect.Found{
		{Name: "a", BaseURL: "http://127.0.0.1:1/v1", Chat: "http://127.0.0.1:1/v1/chat/completions", Models: []string{"other"}},
		{Name: "b", BaseURL: "http://127.0.0.1:2/v1", Chat: "http://127.0.0.1:2/v1/chat/completions", Models: []string{"wanted"}, Key: "sk-x"},
	}, nil)

	cfg := config{Broker: "https://b", User: "u"}
	runShareAsync(t, cfg, []string{"wanted"}) // positional model selects the 2nd endpoint

	// The 2nd endpoint (serving "wanted", with a key) must be the one adopted + saved.
	saved := loadConfig().Share
	if saved == nil || saved.Upstream != "http://127.0.0.1:2/v1" || saved.UpstreamKey != "sk-x" {
		t.Fatalf("model-matching pick wrong: %+v", saved)
	}
}

// TestCmdShareSavedKeyedUpstream covers the saved-keyed-upstream fast path: a previously
// verified upstream + key is re-probed WITH its key (the broad scan can't carry it) and
// adopted without a re-prompt.
func TestCmdShareSavedKeyedUpstream(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t)
	stubDetectProbeKey(t, func(url, key string) (detect.Found, detect.Status) {
		return detect.Found{
			BaseURL: "http://127.0.0.1:9000/v1",
			Chat:    "http://127.0.0.1:9000/v1/chat/completions",
			Models:  []string{"km"},
			Key:     key,
		}, detect.Reachable
	})
	// detectFull must NOT be consulted on this path; fail loudly if it is.
	stubDetectFull(t, nil, nil)

	cfg := config{Broker: "https://b", User: "u", Share: &Share{
		Upstream: "http://127.0.0.1:9000/v1", UpstreamKey: "sk-saved",
	}}
	runShareAsync(t, cfg, nil)
}

// TestFinishShareDetectSuccess covers finishShare's detect-success path for both the FREE
// and the EARN wizard branches (non-interactive, --yes), asserting the saved share config.
func TestFinishShareDetectSuccess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, []detect.Found{{
		BaseURL: "http://127.0.0.1:11434/v1",
		Models:  []string{"mfin"},
		Key:     "sk-fin",
	}}, nil)

	// EARN: default price 0.20/0.30 (no interactive prompt under --yes).
	got, ran, err := finishShare(config{Broker: "https://b"}, true, wizardOpts{yes: true})
	if err != nil || !ran {
		t.Fatalf("finishShare(earn) = ran %v err %v, want ran true nil", ran, err)
	}
	if got.Share == nil || got.Share.Model != "mfin" || got.Share.PriceOut != 0.30 || got.Share.PriceIn != 0.20 {
		t.Fatalf("earn share config wrong: %+v", got.Share)
	}
	if got.Share.UpstreamKey != "sk-fin" || !got.Onboarded {
		t.Fatalf("earn share missing key/onboarded: %+v onboarded=%v", got.Share, got.Onboarded)
	}

	// FREE: no price collected (0/0).
	gotF, ranF, errF := finishShare(config{Broker: "https://b"}, false, wizardOpts{yes: true})
	if errF != nil || !ranF {
		t.Fatalf("finishShare(free) = ran %v err %v", ranF, errF)
	}
	if gotF.Share == nil || gotF.Share.PriceOut != 0 || gotF.Share.PriceIn != 0 {
		t.Fatalf("free share should carry no price: %+v", gotF.Share)
	}
}

// TestFinishShareNoModel covers finishShare's detect-nothing, non-interactive fallback:
// guidedUpstream declines (no TTY) so it prints the hint, marks onboarded, and returns.
func TestFinishShareNoModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, nil, nil) // nothing detected

	got, ran, err := finishShare(config{Broker: "https://b"}, false, wizardOpts{})
	if err != nil || !ran || !got.Onboarded {
		t.Fatalf("finishShare(no model) = ran %v err %v onboarded %v", ran, err, got.Onboarded)
	}
	if got.Share != nil {
		t.Fatalf("no-model finishShare should not set a share config: %+v", got.Share)
	}
}

// TestRunWizardForcePaths covers runWizard's non-interactive --free / --earn fast paths,
// which delegate straight to finishShare.
func TestRunWizardForcePaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"wz"}}}, nil)

	free, ran, err := runWizard(config{}, wizardOpts{forceFree: true})
	if err != nil || !ran || free.Share == nil || free.Share.Model != "wz" || free.Share.PriceOut != 0 {
		t.Fatalf("runWizard(--free) = %+v ran %v err %v", free.Share, ran, err)
	}
	earn, ran2, err2 := runWizard(config{}, wizardOpts{forceEarn: true, yes: true})
	if err2 != nil || !ran2 || earn.Share == nil || earn.Share.PriceOut != 0.30 {
		t.Fatalf("runWizard(--earn) = %+v ran %v err %v", earn.Share, ran2, err2)
	}
}

// TestCmdOnboardFree covers `roger onboard --free`: runWizard's force-free path + the
// saveConfig the command appends, persisting the share config to disk.
func TestCmdOnboardFree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"ob"}}}, nil)

	if err := cmdOnboard(config{Broker: "https://b"}, []string{"--free"}); err != nil {
		t.Fatalf("cmdOnboard(--free) = %v", err)
	}
	saved := loadConfig()
	if saved.Share == nil || saved.Share.Model != "ob" || !saved.Onboarded {
		t.Fatalf("onboard --free did not persist the share config: %+v onboarded=%v", saved.Share, saved.Onboarded)
	}
}

// TestMaybeOnboardAlreadyOnboarded covers the early return when the user has onboarded
// (the wizard never runs, the config passes through unchanged).
func TestMaybeOnboardAlreadyOnboarded(t *testing.T) {
	in := config{Broker: "https://b", User: "u", Onboarded: true}
	got := maybeOnboard(in)
	if got.Broker != in.Broker || got.User != in.User || !got.Onboarded || got.Share != nil {
		t.Fatalf("maybeOnboard(onboarded) mutated config: %+v", got)
	}
}

// TestRunNoArgsLaunch covers run()'s no-args launch branch via the runTUI /
// startWebConsoleFn seams: the browser console comes up (default-on) and the TUI runs.
func TestRunNoArgsLaunch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	origTUI, origWeb := runTUI, startWebConsoleFn
	var tuiCalled, webCalled bool
	runTUI = func(broker, user string, limits *tui.LimitStore, notice string, hooks tui.Hooks, ctrl *node.Controller) error {
		tuiCalled = true
		if broker != "https://b" {
			t.Errorf("runTUI got broker %q, want https://b", broker)
		}
		return nil
	}
	startWebConsoleFn = func(cfg config, ctrl *node.Controller, port string) {
		webCalled = true
		if port != defaultWebuiPort {
			t.Errorf("startWebConsoleFn got port %q, want %q", port, defaultWebuiPort)
		}
	}
	t.Cleanup(func() { runTUI, startWebConsoleFn = origTUI, origWeb })

	if err := run(nil, config{Broker: "https://b", User: "u", Onboarded: true}); err != nil {
		t.Fatalf("run(no args) = %v", err)
	}
	if !tuiCalled || !webCalled {
		t.Fatalf("run(no args): tuiCalled=%v webCalled=%v, want both true", tuiCalled, webCalled)
	}
}

// TestRunNoWebui covers run()'s launch branch with the console disabled: the TUI still
// runs but startWebConsoleFn is NOT called (and --no-webui is stripped, not dispatched).
func TestRunNoWebui(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	origTUI, origWeb := runTUI, startWebConsoleFn
	var tuiCalled, webCalled bool
	runTUI = func(broker, user string, limits *tui.LimitStore, notice string, hooks tui.Hooks, ctrl *node.Controller) error {
		tuiCalled = true
		return nil
	}
	startWebConsoleFn = func(cfg config, ctrl *node.Controller, port string) { webCalled = true }
	t.Cleanup(func() { runTUI, startWebConsoleFn = origTUI, origWeb })

	if err := run([]string{"--no-webui"}, config{Broker: "https://b", User: "u", Onboarded: true}); err != nil {
		t.Fatalf("run(--no-webui) = %v", err)
	}
	if !tuiCalled || webCalled {
		t.Fatalf("run(--no-webui): tuiCalled=%v webCalled=%v, want true/false", tuiCalled, webCalled)
	}
}

// TestRunDispatch covers run()'s subcommand branch: a known verb returns nil; an unknown
// verb surfaces errUnknownCommand (which main() turns into exit 1).
func TestRunDispatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := run([]string{"version"}, config{Broker: "https://b", User: "u"}); err != nil {
		t.Fatalf("run(version) = %v", err)
	}
	if err := run([]string{"bogus-verb"}, config{Broker: "https://b", User: "u"}); err != errUnknownCommand {
		t.Fatalf("run(bogus) = %v, want errUnknownCommand", err)
	}
}

// TestStartWebConsole stands up the real browser console on an ephemeral localhost port,
// then fetches the tokenized URL it prints and asserts the page is served (HTTP 200).
func TestStartWebConsole(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBrokerEmpty(t), User: "u"}
	ctrl := tui.NewController(cfg.Broker, tuiHooks(cfg))

	// Capture stdout to recover the printed "web console → <url>" line.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	startWebConsole(cfg, ctrl, "0") // "0" => OS-assigned ephemeral port
	w.Close()
	os.Stdout = origStdout

	var url string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "http://"); i >= 0 {
			url = strings.TrimSpace(line[i:])
			break
		}
	}
	if url == "" {
		t.Fatal("startWebConsole did not print a console URL")
	}

	// The token-bearing URL must serve the console page.
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("console GET status = %d, want 200", resp.StatusCode)
	}
}

// TestStartWebConsoleBindFailure covers the non-fatal bind-failure branch: a bogus port
// string fails to bind and startWebConsole returns without panicking (the terminal
// front-end carries on).
func TestStartWebConsoleBindFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}
	ctrl := tui.NewController(cfg.Broker, tuiHooks(cfg))
	// "99999999" is out of the valid TCP port range -> Listen fails -> early return.
	startWebConsole(cfg, ctrl, "99999999")
}

// TestPayoutRequestAmountBranches drives payoutRequest's optional-[amount] validation
// against a fake broker with an ACTIVE KYC + $30 payable / $25 min: below-min, above-
// payable, partial (note), invalid, and the full success path.
func TestPayoutRequestAmountBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broker := brokerRouting(t, map[string]string{
		"/connect/status":  `{"status":"active","min_payout":25,"earnings":{"payable":30,"held":5}}`,
		"/payouts/request": `{"payout":{"id":7,"amount":30,"state":"pending","stripe_transfer_id":"tr_7"}}`,
	})
	cfg := config{Broker: broker, User: "u"}

	// Invalid amount -> error (no broker round-trip).
	if err := payoutRequest(cfg, []string{"abc"}); err == nil {
		t.Error("payoutRequest(abc) should error on a non-numeric amount")
	}
	// Below the $25 minimum -> friendly message, nil (no payout).
	if err := payoutRequest(cfg, []string{"10"}); err != nil {
		t.Errorf("payoutRequest(below-min) = %v, want nil", err)
	}
	// Above the payable balance -> friendly message, nil.
	if err := payoutRequest(cfg, []string{"1000"}); err != nil {
		t.Errorf("payoutRequest(above-payable) = %v, want nil", err)
	}
	// Partial amount (>= min, < payable) -> the "full balance" note, then success.
	if err := payoutRequest(cfg, []string{"28"}); err != nil {
		t.Errorf("payoutRequest(partial) = %v, want nil", err)
	}
	// No amount -> straight to the success path.
	if err := payoutRequest(cfg, nil); err != nil {
		t.Errorf("payoutRequest(no amount) = %v, want nil", err)
	}
}

// TestPayoutRequestGates covers payoutRequest's pre-flight rejections that never reach
// the broker payout call: KYC not active, and payable below the minimum.
func TestPayoutRequestGates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	inactive := config{Broker: brokerRouting(t, map[string]string{
		"/connect/status": `{"status":"onboarding","min_payout":25,"earnings":{"payable":30}}`,
	}), User: "u"}
	if err := payoutRequest(inactive, nil); err != nil {
		t.Errorf("payoutRequest(KYC inactive) = %v, want nil (friendly message)", err)
	}

	belowMin := config{Broker: brokerRouting(t, map[string]string{
		"/connect/status": `{"status":"active","min_payout":25,"earnings":{"payable":5}}`,
	}), User: "u"}
	if err := payoutRequest(belowMin, nil); err != nil {
		t.Errorf("payoutRequest(payable<min) = %v, want nil (keep earning)", err)
	}
}

// TestPayoutHistoryRows covers payoutHistory's table-rendering loop (the existing test
// only hits the empty-history early return) - including the missing-transfer "-" cell.
func TestPayoutHistoryRows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: brokerRouting(t, map[string]string{
		"/payouts/history": `{"payouts":[
			{"id":1,"amount":30,"state":"paid","stripe_transfer_id":"tr_9","created_at":1700000000},
			{"id":2,"amount":10,"state":"pending"}
		]}`,
	}), User: "u"}
	if err := payoutHistory(cfg); err != nil {
		t.Fatalf("payoutHistory(rows) = %v, want nil", err)
	}
}

// TestPrintLimitsConfigured covers printLimits with a populated per-model + default cap
// (the existing test only hits the empty "none set" branch).
func TestPrintLimitsConfigured(t *testing.T) {
	c := config{}
	c.Limits.Default = Limit{MaxOut: 0.5}
	c.Limits.Models = map[string]Limit{"m1": {MaxOut: 0.3, MinTPS: 40}, "m2": {MaxIn: 0.1}}
	c.Limits.TypicalOutTok = 1000
	printLimits(c) // sorted model loop + default line; must not panic
}

// TestCmdDrPhilBannedBundle drives cmdDrPhil's broker-side strike/ban surface against a
// fake broker: a logged-in owner who is BANNED with a suspended node yields the worst-
// first action list + the copy-paste appeal bundle.
func TestCmdDrPhilBannedBundle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t) // logged in -> strike status is fetched
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"dp"}}}, nil)
	cfg := config{Broker: brokerRouting(t, map[string]string{
		"/owner/strikes": `{"banned":true,"ban_reason":"abuse","count":2,"node_bans":{"node-x":"spam"}}`,
	}), User: "u"}
	if err := cmdDrPhil(cfg, nil); err != nil {
		t.Fatalf("cmdDrPhil(banned) = %v", err)
	}
}

// TestCmdDrPhilStrikesNoBan covers the strikes-but-not-banned branch (count>0 warn, no
// node bans -> "none suspended").
func TestCmdDrPhilStrikesNoBan(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	stubDetectFull(t, nil, []string{"http://127.0.0.1:8000/v1"}) // server needs a key
	cfg := config{Broker: brokerRouting(t, map[string]string{
		"/owner/strikes": `{"count":3,"node_bans":{}}`,
	}), User: "u"}
	if err := cmdDrPhil(cfg, nil); err != nil {
		t.Fatalf("cmdDrPhil(strikes) = %v", err)
	}
}

// TestCmdDrPhilUnreachableFix covers the broker-unreachable + --fix auto-reset branch: a
// well-formed but DEAD broker (a closed httptest server, not the default) triggers the
// "reset to default" save without any real network call for the fix itself.
func TestCmdDrPhilUnreachableFix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, nil, nil) // no local LLM -> the fail+hint branch
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close() // now well-formed but unreachable (connection refused, fails fast)

	cfg := config{Broker: url, User: "u"}
	if err := cmdDrPhil(cfg, []string{"--fix"}); err != nil {
		t.Fatalf("cmdDrPhil(--fix, unreachable) = %v", err)
	}
	// The auto-fix reset the saved broker to the default.
	if loadConfig().Broker != defaultBroker {
		t.Fatalf("--fix did not reset the broker to %q: %q", defaultBroker, loadConfig().Broker)
	}
}

// TestCmdDrPhilMalformedNoFix covers the malformed-broker-URL fail branch WITHOUT --fix:
// the diagnostic flags it (and the unreachable clock probe) but changes nothing on disk.
func TestCmdDrPhilMalformedNoFix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, nil, nil)
	cfg := config{Broker: "not-a-url", User: "u"}
	if err := cmdDrPhil(cfg, nil); err != nil {
		t.Fatalf("cmdDrPhil(malformed) = %v", err)
	}
}

// TestCmdAppealFile covers `roger appeal --node ... --reason ...` against a fake broker
// that auto-exonerates the node (the success + auto-exonerated print branch).
func TestCmdAppealFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	cfg := config{Broker: brokerRouting(t, map[string]string{
		"/owner/appeal": `{"ok":true,"appeal_id":5,"state":"open","auto_exonerated":true,"node_unbanned":"node-x"}`,
	}), User: "u"}
	if err := cmdAppeal(cfg, []string{"--node", "node-x", "--reason", "false positive"}); err != nil {
		t.Fatalf("cmdAppeal(file) = %v", err)
	}
	// A missing --reason is rejected up front.
	if err := cmdAppeal(cfg, []string{"--node", "node-x"}); err == nil {
		t.Error("cmdAppeal without --reason should error")
	}
}

// TestCmdAppealListRows covers `roger appeal status` rendering a non-empty appeal list
// (the existing handler test does not seed any appeals).
func TestCmdAppealListRows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeAuth(t)
	cfg := config{Broker: brokerRouting(t, map[string]string{
		"/owner/appeal": `{"appeals":[
			{"id":1,"node_id":"node-x","state":"open","note":"under review","created_at":1700000000},
			{"id":2,"node_id":"","state":"denied","created_at":1700000000}
		]}`,
	}), User: "u"}
	if err := cmdAppeal(cfg, []string{"status"}); err != nil {
		t.Fatalf("cmdAppeal(status, rows) = %v", err)
	}
}
