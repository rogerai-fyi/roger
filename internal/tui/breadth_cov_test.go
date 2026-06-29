package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/agent"
)

// ---------------------------------------------------------------------------
// Run* + PingWalk seam
// ---------------------------------------------------------------------------

// withStubRunProgram swaps the behaviour-preserving runProgram seam for a recorder
// that captures the model + options it was handed and returns ret, restoring the real
// implementation when the returned func is called.
func withStubRunProgram(ret error, sink func(tea.Model, []tea.ProgramOption)) func() {
	orig := runProgram
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		if sink != nil {
			sink(m, opts)
		}
		return ret
	}
	return func() { runProgram = orig }
}

// TestRunSeamEntryPoints drives every public Run* entry point through the stubbed
// runProgram seam (so no real terminal program launches) and asserts each one builds
// the model the way it documents - the notice line, the hooks, and the shared
// controller all flow into the launched model, and the program error propagates.
func TestRunSeamEntryPoints(t *testing.T) {
	sentinel := errMsgSentinel("boom")

	t.Run("Run defaults to no notice/limits", func(t *testing.T) {
		var got model
		restore := withStubRunProgram(sentinel, func(m tea.Model, _ []tea.ProgramOption) { got = m.(model) })
		defer restore()
		if err := Run("http://b1", "alice"); err != sentinel {
			t.Fatalf("Run should propagate the program error, got %v", err)
		}
		if got.broker != "http://b1" || got.user != "alice" {
			t.Errorf("Run launched the wrong identity: broker=%q user=%q", got.broker, got.user)
		}
		if got.updateLine != "" {
			t.Errorf("Run should carry no update notice, got %q", got.updateLine)
		}
	})

	t.Run("RunWithNotice carries the update line", func(t *testing.T) {
		var got model
		restore := withStubRunProgram(nil, func(m tea.Model, _ []tea.ProgramOption) { got = m.(model) })
		defer restore()
		if err := RunWithNotice("http://b2", "bob", nil, "v9.9.9 available"); err != nil {
			t.Fatalf("RunWithNotice returned %v", err)
		}
		if got.updateLine != "v9.9.9 available" {
			t.Errorf("RunWithNotice should set the notice, got %q", got.updateLine)
		}
	})

	t.Run("RunWithHooks carries the hooks", func(t *testing.T) {
		var got model
		restore := withStubRunProgram(nil, func(m tea.Model, _ []tea.ProgramOption) { got = m.(model) })
		defer restore()
		hooks := Hooks{Station: "brave-otter"}
		if err := RunWithHooks("http://b3", "carol", nil, "", hooks); err != nil {
			t.Fatalf("RunWithHooks returned %v", err)
		}
		if got.hooks.Station != "brave-otter" {
			t.Errorf("RunWithHooks should thread the hooks through, station=%q", got.hooks.Station)
		}
	})

	t.Run("RunWithController reuses the shared controller + altscreen (no mouse capture)", func(t *testing.T) {
		var got model
		var opts []tea.ProgramOption
		restore := withStubRunProgram(nil, func(m tea.Model, o []tea.ProgramOption) { got = m.(model); opts = o })
		defer restore()
		ctrl := NewController("http://b4", Hooks{})
		if err := RunWithController("http://b4", "dave", nil, "note", Hooks{}, ctrl); err != nil {
			t.Fatalf("RunWithController returned %v", err)
		}
		if got.ctrl != ctrl {
			t.Errorf("RunWithController must launch over the SAME controller it was handed")
		}
		if got.updateLine != "note" {
			t.Errorf("RunWithController should set the notice, got %q", got.updateLine)
		}
		// Only AltScreen is passed: mouse capture is OFF by default so native drag-select +
		// copy works on any text (opencode-style); the user opts into wheel-scroll via ctrl+o.
		if len(opts) != 1 {
			t.Errorf("RunWithController should pass alt-screen ONLY (1 opt; mouse capture off), got %d", len(opts))
		}
		if !got.mouseOff {
			t.Errorf("RunWithController model should start mouseOff=true (native copy default)")
		}
	})

	t.Run("RunWith delegates with no notice", func(t *testing.T) {
		var got model
		restore := withStubRunProgram(nil, func(m tea.Model, _ []tea.ProgramOption) { got = m.(model) })
		defer restore()
		if err := RunWith("http://b5", "erin", nil); err != nil {
			t.Fatalf("RunWith returned %v", err)
		}
		if got.broker != "http://b5" || got.updateLine != "" {
			t.Errorf("RunWith built the wrong model: %+v", got)
		}
	})
}

// errMsgSentinel is a tiny error type local to this file so the seam tests can assert
// the EXACT error runProgram returned propagates back out of the Run* funcs.
type errMsgSentinel string

func (e errMsgSentinel) Error() string { return string(e) }

// TestInitBatchesStartupWork: the launched model's Init schedules the startup work
// (discover + balance + the animation tick) as a single batch command.
func TestInitBatchesStartupWork(t *testing.T) {
	m := New("http://broker.local", "tester")
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init should schedule the startup batch (discover + balance + tick)")
	}
}

// TestPingWalkSeam covers BOTH PingWalk paths: the quiet (non-TTY) branch prints a
// static pose and returns nil without touching the program seam, and the animated
// branch routes through runProgram with the alt-screen option and propagates its error.
func TestPingWalkSeam(t *testing.T) {
	origQuiet := quiet
	defer func() { quiet = origQuiet }()

	// Quiet branch: no program is launched (the seam must NOT be called).
	quiet = true
	called := false
	restore := withStubRunProgram(nil, func(tea.Model, []tea.ProgramOption) { called = true })
	if err := PingWalk(); err != nil {
		t.Fatalf("quiet PingWalk should return nil, got %v", err)
	}
	if called {
		t.Error("quiet PingWalk must NOT launch a program")
	}
	restore()

	// Animated branch: routes a pingWalkModel through the seam with alt-screen, and the
	// program error propagates.
	quiet = false
	var launched tea.Model
	var opts []tea.ProgramOption
	sentinel := errMsgSentinel("walk-exit")
	restore = withStubRunProgram(sentinel, func(m tea.Model, o []tea.ProgramOption) { launched = m; opts = o })
	defer restore()
	if err := PingWalk(); err != sentinel {
		t.Fatalf("animated PingWalk should propagate the program error, got %v", err)
	}
	if _, ok := launched.(pingWalkModel); !ok {
		t.Errorf("PingWalk should launch a pingWalkModel, got %T", launched)
	}
	if len(opts) != 1 {
		t.Errorf("PingWalk should pass exactly the alt-screen option, got %d", len(opts))
	}
}

// ---------------------------------------------------------------------------
// link / headline badges (real sessions)
// ---------------------------------------------------------------------------

// waitLinkState polls a real session until its truthful broker link reaches want.
func waitLinkState(t *testing.T, s *agent.Session, want agent.LinkState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Link() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session never reached link state %v (got %v)", want, s.Link())
}

// rejectHeartbeatBroker registers nodes ok but 401s every heartbeat, so a session it
// backs settles into the truthful RECONNECTING state.
func rejectHeartbeatBroker(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes/register":
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "/nodes/heartbeat":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestLinkBadgeAllStates exercises linkBadge across its three truthful link states
// using REAL sessions: a fresh (un-acknowledged) session reads "connecting", an
// okBroker-backed session flips to "ON AIR", and a heartbeat-rejecting broker yields
// "RECONNECTING - broker not acknowledging".
func TestLinkBadgeAllStates(t *testing.T) {
	// Connecting: a zero session has never had a heartbeat acknowledged.
	if got := stripANSI(linkBadge(&agent.Session{})); !strings.Contains(got, "connecting") {
		t.Errorf("linkBadge(fresh) should read connecting, got %q", got)
	}

	// On air: okBroker accepts heartbeats.
	ok := okBroker(t)
	defer ok.Close()
	onair, err := agent.Start(agent.Config{Broker: ok.URL, Upstream: "http://127.0.0.1:0", NodeID: "n-on", Model: "m", Ctx: 8192, Parallel: 1})
	if err != nil {
		t.Fatalf("agent.Start(on-air): %v", err)
	}
	defer onair.Stop()
	waitLinkState(t, onair, agent.LinkOnAir)
	if got := stripANSI(linkBadge(onair)); !strings.Contains(got, "ON AIR") {
		t.Errorf("linkBadge(on-air) should read ON AIR, got %q", got)
	}

	// Reconnecting: heartbeats are rejected.
	rej := rejectHeartbeatBroker(t)
	recon, err := agent.Start(agent.Config{Broker: rej.URL, Upstream: "http://127.0.0.1:0", NodeID: "n-re", Model: "m", Ctx: 8192, Parallel: 1})
	if err != nil {
		t.Fatalf("agent.Start(reconnecting): %v", err)
	}
	defer recon.Stop()
	waitLinkState(t, recon, agent.LinkReconnecting)
	got := stripANSI(linkBadge(recon))
	if !strings.Contains(got, "RECONNECTING") || !strings.Contains(got, "not acknowledging") {
		t.Errorf("linkBadge(reconnecting) should explain the broker is not acknowledging, got %q", got)
	}

	// headlineBadge reads the SAME link state off the headline session.
	if got := stripANSI((model{}).headlineBadge()); !strings.Contains(got, "ON AIR") {
		t.Errorf("headlineBadge(no session) should read ON AIR, got %q", got)
	}
	if got := stripANSI((model{share: onair}).headlineBadge()); !strings.Contains(got, "ON AIR") {
		t.Errorf("headlineBadge(on-air) should read ON AIR, got %q", got)
	}
	if got := stripANSI((model{share: recon}).headlineBadge()); !strings.Contains(got, "RECONNECTING") {
		t.Errorf("headlineBadge(reconnecting) should read RECONNECTING, got %q", got)
	}
	if got := stripANSI((model{share: &agent.Session{}}).headlineBadge()); !strings.Contains(got, "connecting") {
		t.Errorf("headlineBadge(connecting) should read connecting, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// private band toggle + one-time code card + clipboard
// ---------------------------------------------------------------------------

// privateBandBroker registers a node WITH a freshly-minted one-time band code (so a
// private toggle routes into the code card), and 200s everything else.
func privateBandBroker(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nodes/register" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"band_id":      "band-1",
				"band_code":    "8F3K9M2Q",
				"band_display": "147.520 MHz · 8F3K-9M2Q",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPrivateBandLoginGate: an anonymous operator pressing `h` (hide / go private) on
// the SHARE table gets the login gate, NOT a private band - going private is earning-
// adjacent and login-gated.
func TestPrivateBandLoginGate(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 30
	m.mode = modeShare
	m.setShareRows([]shareRow{{model: "gpt-oss-20b", ctx: 32768}})
	var tm tea.Model = m
	tm, _ = tm.Update(balanceMsg{loggedIn: false})
	tm, _ = tm.Update(keyMsg("h"))
	gm := asModel(tm)
	if gm.mode == modeBandCard {
		t.Fatal("anonymous h must NOT open a private band card")
	}
	if !strings.Contains(stripANSI(gm.status), "log in to go private") {
		t.Errorf("anon private toggle should show the login gate, got %q", stripANSI(gm.status))
	}
}

// TestPrivateBandMintsCodeCard: a logged-in operator going private mints a one-time
// frequency code, lands on the code card, and `c` copies it (best-effort) while any
// other key clears the secret and returns to the SHARE table.
func TestPrivateBandMintsCodeCard(t *testing.T) {
	srv := privateBandBroker(t)
	m := New(srv.URL, "tester")
	m.width, m.height = 100, 30
	m.mode = modeShare
	m.ctrl.SetLoggedIn(true)
	m.setShareRows([]shareRow{{model: "gpt-oss-20b", ctx: 32768}})

	m.togglePrivateAt(0)
	if m.mode != modeBandCard {
		t.Fatalf("a freshly-minted private band should route to the code card, mode=%v status=%q", m.mode, stripANSI(m.status))
	}
	if m.bandCardCode != "8F3K9M2Q" {
		t.Fatalf("the one-time code should be carried onto the card, got %q", m.bandCardCode)
	}

	// `c` copies (best-effort): either it worked or there is no clipboard tool, but the
	// status reflects exactly one of the two real outcomes (never silence).
	cm, _ := m.onBandCardKey(keyMsg("c"))
	st := stripANSI(asModel(cm).status)
	if !strings.Contains(st, "Copied to clipboard") && !strings.Contains(st, "no clipboard tool") {
		t.Errorf("c on the card should report copy success or a missing tool, got %q", st)
	}

	// Any other key clears the secret (shown exactly once) and returns to SHARE.
	lm, _ := m.onBandCardKey(keyMsg("x"))
	left := asModel(lm)
	if left.mode != modeShare || left.bandCardCode != "" {
		t.Errorf("leaving the card must clear the secret + return to SHARE, mode=%v code=%q", left.mode, left.bandCardCode)
	}

	// Toggling again takes it back to the OPEN MARKET (public).
	m.togglePrivateAt(0)
	if !strings.Contains(stripANSI(m.status), "OPEN MARKET") {
		t.Errorf("re-toggle should return the band to the OPEN MARKET, got %q", stripANSI(m.status))
	}
}

// TestCopyToClipboardEmpty: an empty string is never copied (concrete false).
func TestCopyToClipboardEmpty(t *testing.T) {
	if copyToClipboard("") {
		t.Error("copyToClipboard(\"\") must return false")
	}
}

// ---------------------------------------------------------------------------
// /freq private-frequency flows
// ---------------------------------------------------------------------------

// resolveBandBroker answers /bands/resolve with a single live offer for any freq, so
// the resolve path produces a populated private band.
func resolveBandBroker(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bands/resolve" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"band":   map[string]string{"display": "147.520 MHz · 8F3K-9M2Q"},
				"offers": []map[string]any{{"node_id": "n1", "model": "gpt-oss-20b", "price_out": 0.3, "online": true, "tps": 60}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"offers":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDoFreqUsageAndClear: a bare /freq with nothing tuned prints the usage hint; a
// bare /freq while a private freq IS tuned clears back to the OPEN MARKET and re-scans.
func TestDoFreqUsageAndClear(t *testing.T) {
	m := browseSeed(100)
	// Nothing tuned: usage hint, no command.
	nm, cmd := m.doFreq("")
	if !strings.Contains(stripANSI(asModel(nm).status), "usage:") {
		t.Errorf("bare /freq with nothing tuned should show usage, got %q", stripANSI(asModel(nm).status))
	}
	if cmd != nil {
		t.Error("usage hint should not issue a command")
	}
	// A freq is tuned: clearing returns to OPEN MARKET and re-scans.
	m.tuneFreq, m.tuneFreqLabel = "147.520 MHz 8F3K9M2Q", "147.520 MHz"
	cm, ccmd := m.doFreq("")
	if asModel(cm).tuneFreq != "" {
		t.Error("clearing /freq should drop the tuned frequency")
	}
	if !strings.Contains(stripANSI(asModel(cm).status), "OPEN MARKET") {
		t.Errorf("clearing /freq should announce OPEN MARKET, got %q", stripANSI(asModel(cm).status))
	}
	if ccmd == nil {
		t.Error("clearing /freq should re-scan the public band")
	}
}

// TestResolveFreqSuccessAndMiss drives the OFF-event-loop resolve command against a
// real broker: a code that resolves populates a single private band (PRIVATE FREQ
// header), and a miss reports the uniform negative (no enumeration tell).
func TestResolveFreqSuccessAndMiss(t *testing.T) {
	hit := resolveBandBroker(t)
	m := browseSeed(100)
	m.broker = hit.URL

	// Success: run the resolve command, feed its message back into Update.
	_, cmd := m.resolveFreq("147.520 MHz 8F3K9M2Q")
	msg := cmd()
	fr, ok := msg.(freqResolvedMsg)
	if !ok || !fr.ok {
		t.Fatalf("resolveFreq(hit) should yield a positive freqResolvedMsg, got %#v", msg)
	}
	if len(fr.offers) != 1 || fr.offers[0].Model != "gpt-oss-20b" {
		t.Fatalf("resolved band should carry the broker's offer, got %#v", fr.offers)
	}
	nm, _ := m.Update(fr)
	got := asModel(nm)
	if got.tuneFreq == "" || !strings.Contains(stripANSI(got.status), "PRIVATE FREQ") {
		t.Errorf("a resolved freq should tune the private band, status=%q", stripANSI(got.status))
	}

	// Miss: a broker that returns the uniform empty/404 negative.
	miss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"offers":[]}`))
	}))
	defer miss.Close()
	m.broker = miss.URL
	_, mcmd := m.resolveFreq("nope")
	mfr := mcmd().(freqResolvedMsg)
	if mfr.ok {
		t.Fatal("a wrong code must resolve to the uniform negative")
	}
	mm, _ := m.Update(mfr)
	if !strings.Contains(stripANSI(asModel(mm).status), "no station on that frequency") {
		t.Errorf("a miss should show the uniform negative, got %q", stripANSI(asModel(mm).status))
	}
}

// TestFreqEntryModeResolves: the [~] PRIVATE FREQUENCY input resolves on enter through
// the same constant-work path (and esc cancels).
func TestFreqEntryModeResolves(t *testing.T) {
	hit := resolveBandBroker(t)
	m := browseSeed(100)
	m.broker = hit.URL
	m.mode = modeFreqEntry
	m.freqIn.SetValue("147.520 MHz 8F3K9M2Q")
	var tm tea.Model = m
	tm, cmd := tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if asModel(tm).mode != modeBrowse {
		t.Errorf("enter in freq-entry should return to browse, mode=%v", asModel(tm).mode)
	}
	if cmd == nil {
		t.Fatal("enter in freq-entry should issue the resolve command")
	}
	if fr, ok := cmd().(freqResolvedMsg); !ok || !fr.ok {
		t.Errorf("freq-entry enter should resolve the band, got %#v", cmd())
	}

	// esc cancels back to browse.
	c := browseSeed(100)
	c.mode = modeFreqEntry
	var cm tea.Model = c
	cm, _ = cm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(cm).mode != modeBrowse {
		t.Errorf("esc in freq-entry should cancel to browse, mode=%v", asModel(cm).mode)
	}
}

// ---------------------------------------------------------------------------
// /topup + /grant host-hook flows
// ---------------------------------------------------------------------------

// TestDoTopupFlows covers the top-up command: no hook (unavailable), the default $10,
// an explicit amount, the success message (checkout URL), and the error path.
func TestDoTopupFlows(t *testing.T) {
	// No hook in this build.
	m := browseSeed(100)
	nm, _ := m.doTopup(nil)
	if !strings.Contains(stripANSI(asModel(nm).status), "top-up unavailable") {
		t.Errorf("no-hook topup should say unavailable, got %q", stripANSI(asModel(nm).status))
	}

	// With a hook: capture the requested amount and return a checkout URL.
	var gotUSD float64
	hooks := Hooks{TopupURL: func(broker, user string, usd float64) (string, error) {
		gotUSD = usd
		return "https://pay.example/checkout/abc", nil
	}}
	hm := NewWithHooks("http://broker.local", "tester", nil, hooks)
	hm.width, hm.height = 100, 30
	// Explicit amount.
	_, cmd := hm.doTopup([]string{"25"})
	if msg := cmd(); msg.(topupMsg) != "https://pay.example/checkout/abc" {
		t.Errorf("topup should yield the checkout URL, got %#v", msg)
	}
	if gotUSD != 25 {
		t.Errorf("topup should pass the explicit $25, got %v", gotUSD)
	}
	// Default amount when none given.
	_, dcmd := hm.doTopup(nil)
	_ = dcmd()
	if gotUSD != 10 {
		t.Errorf("bare topup should default to $10, got %v", gotUSD)
	}

	// Error path surfaces a flowErrMsg.
	em := NewWithHooks("http://broker.local", "tester", nil, Hooks{
		TopupURL: func(string, string, float64) (string, error) { return "", errMsgSentinel("declined") },
	})
	_, ecmd := em.doTopup([]string{"5"})
	if fe, ok := ecmd().(flowErrMsg); !ok || !strings.Contains(string(fe), "top-up failed") {
		t.Errorf("topup error should be a flowErrMsg, got %#v", ecmd())
	}
}

// TestDoGrantFlows covers grant create (mints a one-shot secret), list (rows), the
// no-hook unavailable messages, and the create error path.
func TestDoGrantFlows(t *testing.T) {
	// No hooks: both create + list report unavailable.
	m := browseSeed(100)
	cm, _ := m.doGrant([]string{"create"})
	if !strings.Contains(stripANSI(asModel(cm).status), "grants unavailable") {
		t.Errorf("no-hook grant create should say unavailable, got %q", stripANSI(asModel(cm).status))
	}
	lm, _ := m.doGrant([]string{"list"})
	if !strings.Contains(stripANSI(asModel(lm).status), "grants unavailable") {
		t.Errorf("no-hook grant list should say unavailable, got %q", stripANSI(asModel(lm).status))
	}

	// With hooks: create returns a secret; list returns rows.
	var gotName string
	hooks := Hooks{
		GrantCreate: func(broker, name string, free bool) (string, error) {
			gotName = name
			if !free {
				t.Error("in-TUI grant create should request a FREE key")
			}
			return "rog-grant_secret123", nil
		},
		GrantList: func(broker string) ([]GrantRow, error) {
			return []GrantRow{{Name: "my-bots", Price: "free", Status: "active"}}, nil
		},
	}
	hm := NewWithHooks("http://broker.local", "tester", nil, hooks)
	_, ccmd := hm.doGrant([]string{"create", "fleet"})
	if gm := ccmd().(grantMsg); gm.secret != "rog-grant_secret123" {
		t.Errorf("grant create should yield the minted secret, got %q", gm.secret)
	}
	if gotName != "fleet" {
		t.Errorf("grant create should pass the requested name, got %q", gotName)
	}
	_, lcmd := hm.doGrant(nil) // bare /grant => list
	if rows := lcmd().(grantListMsg); len(rows) != 1 || rows[0].Name != "my-bots" {
		t.Errorf("bare /grant should list grants, got %#v", lcmd())
	}

	// Create error path.
	em := NewWithHooks("http://broker.local", "tester", nil, Hooks{
		GrantCreate: func(string, string, bool) (string, error) { return "", errMsgSentinel("nope") },
	})
	_, ecmd := em.doGrant([]string{"new"})
	if fe, ok := ecmd().(flowErrMsg); !ok || !strings.Contains(string(fe), "grant create failed") {
		t.Errorf("grant create error should be a flowErrMsg, got %#v", ecmd())
	}
}

// ---------------------------------------------------------------------------
// LIMITS editor (commitLimitField) + over-limit editor
// ---------------------------------------------------------------------------

// TestLimitsEditorCommit drives the per-model limits editor: enter opens the out-price
// field, Tab commits it and moves to min-tps, enter commits that, and the values are
// persisted to the store. Then `d` clears the model's limit.
func TestLimitsEditorCommit(t *testing.T) {
	var saved struct {
		models map[string]Limit
	}
	store := &LimitStore{
		Models: map[string]Limit{"gpt-oss-20b": {}},
		Save:   func(models map[string]Limit, def Limit) { saved.models = models },
	}
	m := NewWith("http://broker.local", "tester", store)
	m.width, m.height = 100, 30
	m.enterLimits()
	if len(m.limModels) == 0 {
		t.Fatal("limits view should list the stored model")
	}
	var tm tea.Model = m
	// enter -> edit the out-price field.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if asModel(tm).editField != 0 {
		t.Fatalf("enter should open the out-price field, editField=%d", asModel(tm).editField)
	}
	for _, r := range "0.75" {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	// tab commits out-price and moves to min-tps.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyTab})
	for _, r := range "40" {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	// enter commits min-tps and leaves edit.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gm := asModel(tm)
	got := gm.limits.resolve("gpt-oss-20b")
	if got.MaxOut != 0.75 || got.MinTPS != 40 {
		t.Fatalf("limits editor should persist out=0.75 tps=40, got %+v", got)
	}
	if saved.models == nil {
		t.Error("committing a field should persist via the Save callback")
	}

	// `d` clears the selected model's limit.
	dm, _ := tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if _, ok := asModel(dm).limits.Models["gpt-oss-20b"]; ok {
		t.Error("d should clear the model's limit from the store")
	}
}

// TestOverLimitEditor drives the over-limit screen: nudges with up/down, a too-low
// enter stays blocked, and a sufficient raise persists the new max + proceeds.
func TestOverLimitEditor(t *testing.T) {
	store := &LimitStore{Models: map[string]Limit{"m": {MaxOut: 0.30}}}
	m := NewWith("http://broker.local", "tester", store)
	m.width, m.height = 100, 30
	m.loggedIn, m.haveBal = true, true
	m.mode = modeOverLimit
	bd := band{model: "m", minOut: 0.50, online: true, cheapest: &offer{NodeID: "n", Model: "m", PriceOut: 0.50, Online: true}}
	m.q = quote{b: bd, limit: store.resolve("m"), typical: 800}
	m.editBuf = "0.30"

	// up nudges the buffer above the previous value.
	nm, _ := m.onOverLimitKey(tea.KeyMsg{Type: tea.KeyUp})
	if asModel(nm).editBuf == "0.30" {
		t.Errorf("up should nudge the buffer above 0.30, got %q", asModel(nm).editBuf)
	}
	// down + backspace + a typed digit also edit the buffer (no panic, buffer changes).
	dn, _ := m.onOverLimitKey(tea.KeyMsg{Type: tea.KeyDown})
	if asModel(dn).editBuf == "" {
		t.Error("down should still leave a clamped numeric buffer")
	}
	bk, _ := m.onOverLimitKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if asModel(bk).editBuf != "0.3" {
		t.Errorf("backspace should trim the buffer, got %q", asModel(bk).editBuf)
	}

	// A too-low enter stays blocked (validation), still in over-limit.
	m.editBuf = "0.10"
	bm, _ := m.onOverLimitKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(stripANSI(asModel(bm).status), "still below the band") {
		t.Errorf("a too-low raise should stay blocked, got %q", stripANSI(asModel(bm).status))
	}

	// A sufficient raise persists + leaves the over-limit screen.
	m.editBuf = "0.60"
	rm, _ := m.onOverLimitKey(tea.KeyMsg{Type: tea.KeyEnter})
	if got := store.resolve("m").MaxOut; got != 0.60 {
		t.Errorf("a valid raise should persist the new max, got %v", got)
	}
	if asModel(rm).mode == modeOverLimit {
		t.Error("a valid raise should leave the over-limit screen")
	}

	// `w` = wait & notify path.
	wm, _ := m.onOverLimitKey(keyMsg("w"))
	if wmm := asModel(wm); wmm.watching != "m" || wmm.mode != modeBrowse {
		t.Errorf("w should watch the band and return to browse, got watching=%q mode=%v", wmm.watching, wmm.mode)
	}
}

// ---------------------------------------------------------------------------
// small helpers: ctxCell, slowTick, alertBox, LimitStore set/clear
// ---------------------------------------------------------------------------

// TestCtxCell covers the three context-window cells: unknown, estimated (~), and a
// solid detected window.
func TestCtxCell(t *testing.T) {
	if got := stripANSI(ctxCell(0, false)); got != "-" {
		t.Errorf("ctxCell(0) = %q, want -", got)
	}
	if got := stripANSI(ctxCell(32768, true)); !strings.HasPrefix(got, "~") {
		t.Errorf("ctxCell(estimated) should be prefixed ~, got %q", got)
	}
	if got := stripANSI(ctxCell(131072, false)); strings.HasPrefix(got, "~") || !strings.Contains(got, "k") {
		t.Errorf("ctxCell(detected) should be a solid k-cell, got %q", got)
	}
}

// TestSlowTick: the calm compact cadence emits a tickMsg, just like the fast tick.
func TestSlowTick(t *testing.T) {
	if _, ok := slowTick()().(tickMsg); !ok {
		t.Error("slowTick should produce a tickMsg")
	}
	if _, ok := tick()().(tickMsg); !ok {
		t.Error("tick should produce a tickMsg")
	}
}

// TestAlertBoxSetTake: the thread-safe mailbox returns and clears the last set message.
func TestAlertBoxSetTake(t *testing.T) {
	a := &alertBox{}
	if a.take() != "" {
		t.Error("a fresh alertBox should take an empty string")
	}
	a.set("failover: switched station")
	if got := a.take(); got != "failover: switched station" {
		t.Errorf("alertBox.take = %q, want the set message", got)
	}
	if a.take() != "" {
		t.Error("alertBox should be empty after a take (drained)")
	}
}

// TestLimitStoreSetClear covers LimitStore.set/clear incl. the Save callback and the
// nil-receiver no-ops.
func TestLimitStoreSetClear(t *testing.T) {
	var savedDef Limit
	saveCalls := 0
	s := &LimitStore{Save: func(models map[string]Limit, def Limit) { saveCalls++; savedDef = def }}
	s.set("m", Limit{MaxOut: 1.25})
	if s.Models["m"].MaxOut != 1.25 {
		t.Errorf("set should store the limit, got %+v", s.Models["m"])
	}
	if saveCalls != 1 {
		t.Errorf("set should persist once, got %d", saveCalls)
	}
	s.clear("m")
	if _, ok := s.Models["m"]; ok {
		t.Error("clear should remove the model")
	}
	if saveCalls != 2 {
		t.Errorf("clear should persist, got %d save calls", saveCalls)
	}
	_ = savedDef

	// nil-receiver no-ops (anonymous: no store).
	var n *LimitStore
	n.set("x", Limit{MaxOut: 1}) // must not panic
	n.clear("x")                 // must not panic
	if n.resolve("x") != (Limit{}) {
		t.Error("nil store resolve should be the zero limit")
	}
}

// ---------------------------------------------------------------------------
// agent turn end-to-end (startAgentTurn / waitAgentEvent / onAgentEvent)
// ---------------------------------------------------------------------------

// chatBroker answers /v1/chat/completions with a single assistant reply (no tool
// calls) and a per-turn cost header, so an agent turn runs to a clean final answer.
func chatBroker(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("X-RogerAI-Cost", "0.0021")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": reply}}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAgentTurnEndToEnd runs one real agent turn against a chat broker: startAgentTurn
// launches the harness loop in the background, waitAgentEvent drains the streamed
// events (incl. the cost side-channel) and the final answer lands in the transcript.
func TestAgentTurnEndToEnd(t *testing.T) {
	srv := chatBroker(t, "two go files here")
	base := browseSeed(120)
	base.broker = srv.URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}

	nm, _ := base.enterAgent()
	am := asModel(nm)
	if am.agent == nil || am.agent.model == "" {
		t.Fatalf("enterAgent should build a runtime on a tuned model, got %+v", am.agent)
	}

	am.agentBusy = true
	// Launch the turn goroutine.
	if msg := am.startAgentTurn("how many go files")(); msg != nil {
		t.Errorf("startAgentTurn's launch cmd should return a nil placeholder, got %#v", msg)
	}

	// Drain the stream until the turn is done.
	sawCost := false
	done := false
	for i := 0; i < 50 && !done; i++ {
		msg := am.waitAgentEvent()()
		switch msg.(type) {
		case agentCostMsg:
			sawCost = true
		case agentDoneMsg:
			done = true
		}
		nm, _ := am.Update(msg)
		am = asModel(nm)
	}
	if !done {
		t.Fatal("the agent turn never reported done")
	}
	out := stripANSI(am.View())
	if !strings.Contains(out, "two go files here") {
		t.Errorf("the final answer should land in the AGENT transcript:\n%s", out)
	}
	if !sawCost {
		t.Error("the per-turn cost side-channel (X-RogerAI-Cost) should surface an agentCostMsg")
	}
}

// TestRunSessionCommands exercises the in-CHANNEL slash command set via runSession,
// asserting concrete state changes (toggles, transcript lines, mode hops).
func TestRunSessionCommands(t *testing.T) {
	base := seedFor(120, modeChat, false)

	// /stats toggles the verbose footer on then off.
	on, _ := base.runSession("/stats")
	if !on.(model).showStats {
		t.Error("/stats should turn the verbose footer on")
	}
	off, _ := on.(model).runSession("/detail")
	if off.(model).showStats {
		t.Error("/detail (alias) should toggle the verbose footer back off")
	}

	// /confidential toggles confidential-only routing.
	cf, _ := base.runSession("/conf")
	if !cf.(model).confidentialOnly {
		t.Error("/conf should turn confidential-only on")
	}

	// /system sets a prompt; bare /system echoes it.
	sp, _ := base.runSession("/system be terse")
	if sp.(model).sysPrompt != "be terse" {
		t.Errorf("/system should set the prompt, got %q", sp.(model).sysPrompt)
	}

	// /clear empties the transcript + resets session cost.
	dirty := base
	dirty.transcript = []string{"a", "b"}
	dirty.sessCost = 1.5
	cl, _ := dirty.runSession("/clear")
	if len(cl.(model).transcript) != 1 || cl.(model).sessCost != 0 {
		t.Errorf("/clear should reset the transcript + cost, got %d lines cost %v", len(cl.(model).transcript), cl.(model).sessCost)
	}

	// /model drops back to the band browser to re-tune.
	rt, _ := base.runSession("/retune")
	if rt.(model).mode != modeBrowse {
		t.Errorf("/retune should return to the band browser, mode=%v", rt.(model).mode)
	}

	// /help, /cost, /save, /endpoint, /support, unknown all append a system line (no panic).
	for _, c := range []string{"/help", "/cost", "/save", "/endpoint", "/support", "/bogus"} {
		nm, _ := base.runSession(c)
		if len(nm.(model).transcript) <= len(base.transcript) {
			t.Errorf("%s should append a system line", c)
		}
	}
}

// TestRunSlashDispatch covers the BROWSE-level slash command dispatcher (run) across
// its toggle + view-changing branches.
func TestRunSlashDispatch(t *testing.T) {
	base := browseSeed(120)

	// /confidential toggles + sets a status.
	cf, _ := base.run("confidential")
	if !cf.(model).confidentialOnly {
		t.Error("/confidential should toggle confidential-only")
	}

	// /limits enters the limits view.
	lm, _ := base.run("limits")
	if lm.(model).mode != modeLimits {
		t.Errorf("/limits should enter the limits view, mode=%v", lm.(model).mode)
	}

	// /help enters the help view.
	hm, _ := base.run("help")
	if hm.(model).mode != modeHelp {
		t.Errorf("/help should enter the help view, mode=%v", hm.(model).mode)
	}

	// /search issues a re-scan command.
	if _, cmd := base.run("search"); cmd == nil {
		t.Error("/search should issue a re-scan command")
	}

	// /config sets the broker/user status line.
	cm, _ := base.run("config")
	if !strings.Contains(stripANSI(cm.(model).status), "broker") {
		t.Errorf("/config should report the broker, got %q", stripANSI(cm.(model).status))
	}

	// unknown command falls through to the hint.
	um, _ := base.run("zzz")
	if !strings.Contains(stripANSI(um.(model).status), "unknown") {
		t.Errorf("an unknown command should hint, got %q", stripANSI(um.(model).status))
	}
}
