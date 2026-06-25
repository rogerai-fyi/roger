package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/agent"
)

func TestRenderBrowse(t *testing.T) {
	// The browse view is now a BAND list (offers grouped by model) showing a
	// cross-station out-price RANGE - not a flat per-station list.
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "demo-node", Region: "home", Model: "gpt-oss-20b", PriceIn: 0.2, PriceOut: 0.3, Ctx: 32768, Online: true},
		{NodeID: "alt-node", Region: "us-w", Model: "gpt-oss-20b", PriceIn: 0.25, PriceOut: 0.41, Ctx: 32768, Online: true},
	})
	m, _ = m.Update(balanceMsg{balance: 100, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	out := m.View()
	// model name, the range column header, the live range, the logged-in balance ($)
	// + footer.
	for _, want := range []string{"R O G E R", "gpt-oss-20b", "$/1M out (range)", "0.30 ~ 0.41", "$100", "enter tune in"} {
		if !strings.Contains(out, want) {
			t.Errorf("browse view missing %q\n---\n%s", want, out)
		}
	}
}

// TestEmptyStates: before any scan, the browse view shows the "tuning in"
// loading line; after a scan returns empty (broker serializes offers as null),
// it must flip to the idle "band is quiet" standing-by line, NOT stay on the
// loading pose; a broker drop shows the "...static" line.
func TestEmptyStates(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 92, Height: 30})
	if !strings.Contains(m.View(), "tuning in") {
		t.Errorf("pre-scan should show the loading line:\n%s", m.View())
	}
	// An empty scan (offers null -> nil slice) must reach the static empty-band CTA
	// (audit #10): ONE actionable line naming the empty band AND the [2] share move -
	// not a blank screen, not the loading pose, and no rotating carousel.
	m, _ = m.Update(offersMsg(nil))
	if !strings.Contains(m.View(), "No stations on air") || !strings.Contains(m.View(), "[2]") {
		t.Errorf("empty scan should show the static empty-band CTA with the [2] share move, not loading:\n%s", m.View())
	}
	// A broker drop shows the static line.
	d, _ := New("http://broker.local", "tester").Update(tea.WindowSizeMsg{Width: 92, Height: 30})
	d, _ = d.Update(errMsg("broker unreachable: x"))
	if !strings.Contains(d.View(), "static") {
		t.Errorf("broker drop should show the static line:\n%s", d.View())
	}
}

func TestConnectConfirmAndHelp(t *testing.T) {
	// Enter now opens the cost-confirmation screen FIRST (default DENY); the
	// endpoint binds only on accept.
	mm := New("http://broker.local", "tester")
	mm.proxyAddr = "127.0.0.1:0" // ephemeral port - no fixed-port conflict/leak in tests
	var m tea.Model = mm
	m, _ = m.Update(balanceMsg{balance: 42, loggedIn: true})
	m, _ = m.Update(offersMsg{{NodeID: "nyx-home", Model: "llama-3.3-70b", PriceIn: 0.2, PriceOut: 0.55, Online: true}})

	// select + connect (enter) -> confirmation screen, NOT yet bound
	cm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cv := cm.View()
	if !strings.Contains(cv, "open channel") || !strings.Contains(cv, "/ reply") {
		t.Errorf("confirm screen not shown:\n%s", cv)
	}
	if strings.Contains(cv, "127.0.0.1:") {
		t.Error("endpoint should NOT bind before the user accepts")
	}

	// accept (enter) -> endpoint binds AND we auto-switch to CHANNEL mode with a
	// compact header (compact-on-connect). The endpoint is revealed via /endpoint.
	om, _ := cm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ov := om.View()
	if !strings.Contains(ov, "CHANNEL") || !strings.Contains(ov, "on channel nyx-home") {
		t.Errorf("expected compact CHANNEL view after accept:\n%s", ov)
	}
	// /endpoint in-session surfaces the bound 127.0.0.1 endpoint.
	em := om
	em, _ = em.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "endpoint" {
		em, _ = em.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	em, _ = em.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(em.View(), "127.0.0.1:") {
		t.Errorf("/endpoint should surface the bound endpoint:\n%s", em.View())
	}

	// deny path: a fresh connect, then esc -> no endpoint, back to browse
	mm2 := New("http://broker.local", "tester")
	mm2.proxyAddr = "127.0.0.1:0"
	var d tea.Model = mm2
	d, _ = d.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.1, Online: true}})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter}) // -> confirm
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})   // deny
	if strings.Contains(d.View(), "127.0.0.1:") {
		t.Error("deny should not bind an endpoint")
	}

	// help command
	hm, _ := New("x", "y").run("help")
	if !strings.Contains(hm.View(), "commands") {
		t.Error("help view not shown")
	}
}

// TestDollarsAdaptivePrecision: balances at 2dp, tiny costs keep significant
// digits and never collapse to $0.00.
func TestDollarsAdaptivePrecision(t *testing.T) {
	cases := map[float64]string{
		0:         "$0.00",
		12.34:     "$12.34",
		0.01:      "$0.01",
		0.000123:  "$0.000123",
		0.0000005: "$0.0000005",
	}
	for in, want := range cases {
		if got := dollars(in); got != want {
			t.Errorf("dollars(%v) = %q, want %q", in, got, want)
		}
	}
	// a real sub-cent cost must never read as $0.00
	if dollars(0.0004) == "$0.00" {
		t.Error("sub-cent cost collapsed to $0.00")
	}
}

// TestLiveInputEcho proves the input-bug fix: a typed command echoes into the
// view LIVE, before Enter, in a clearly labeled `rog ›` prompt; likewise the
// channel `you ›` prompt echoes live before send. Regression guard for the bug
// where the user saw nothing until pressing Enter.
func TestLiveInputEcho(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// browse always shows the labeled prompt + the press-/ hint.
	if !strings.Contains(m.View(), "rog ›") {
		t.Fatalf("browse view missing the rog prompt:\n%s", m.View())
	}
	// enter command mode and type, WITHOUT pressing enter - it must echo live.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "search" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !strings.Contains(m.View(), "search") {
		t.Errorf("command input did not echo live before Enter:\n%s", m.View())
	}

	// channel prompt echoes live too. Connect first.
	cm := New("http://broker.local", "tester")
	cm.proxyAddr = "127.0.0.1:0"
	var c tea.Model = cm
	c, _ = c.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	c, _ = c.Update(balanceMsg{balance: 50, loggedIn: true}) // paid bands need an account
	c, _ = c.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.1, Online: true}})
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyEnter}) // accept -> connected
	c, _ = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	for _, r := range "hello" {
		c, _ = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	cv := c.View()
	if !strings.Contains(cv, "you ›") || !strings.Contains(cv, "hello") {
		t.Errorf("channel input did not echo live before send:\n%s", cv)
	}
}

// TestOverLimitFlow: a band priced over the per-model max enters the over-limit
// screen; raising the max inline (digits + enter) unblocks it into the confirm.
func TestOverLimitFlow(t *testing.T) {
	store := &LimitStore{Models: map[string]Limit{"m": {MaxOut: 0.20}}, TypicalOut: 800}
	mm := NewWith("http://broker.local", "tester", store)
	mm.proxyAddr = "127.0.0.1:0"
	var m tea.Model = mm
	m, _ = m.Update(balanceMsg{balance: 50, loggedIn: true}) // paid bands need an account
	m, _ = m.Update(offersMsg{{NodeID: "n", Model: "m", PriceOut: 0.34, Online: true}})

	// connect -> over-limit (0.34 > 0.20)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ov := m.View()
	if !strings.Contains(ov, "above your limit") {
		t.Fatalf("expected over-limit screen:\n%s", ov)
	}

	// The field is pre-filled to the cheapest price (the smallest unblocking
	// raise). nudge up twice (+0.02 -> 0.36) then save -> re-check into confirm.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "open channel") {
		t.Errorf("after raising the max, expected the confirm screen:\n%s", m.View())
	}
	// and the new limit was persisted into the store
	if store.Models["m"].MaxOut < 0.34 {
		t.Errorf("limit not saved/raised, got %v", store.Models["m"].MaxOut)
	}
}

// runCmd types a slash command into the palette and submits it.
func runCmd(m tea.Model, cmd string) tea.Model {
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range cmd {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m
}

// runShare submits /share and then SYNCHRONOUSLY drives the async detection: /share
// now opens the SHARE table in a loading pose and returns a tea.Cmd that probes for
// local models off the event loop (detectShares is injectable, so deterministic in
// tests). runShare runs that command and feeds its sharesDetectedMsg back so the
// caller sees the settled state (the table or the guided wizard).
func runShare(m tea.Model) tea.Model {
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "share" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	var c tea.Cmd
	m, c = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for c != nil {
		msg := c()
		if msg == nil {
			break
		}
		m, c = m.Update(msg)
	}
	return m
}

// TestInTUIFlows: /help lists the in-TUI verbs, an empty balance surfaces /topup,
// and the /grant list + /login flows fire through their hooks and update state.
func TestInTUIFlows(t *testing.T) {
	var called struct{ login, grant bool }
	hooks := Hooks{
		Login: func(broker, id string) (string, error) { called.login = true; return "octocat", nil },
		GrantList: func(broker string) ([]GrantRow, error) {
			called.grant = true
			return []GrantRow{{Name: "petlings", Price: "free", Status: "active"}}, nil
		},
	}
	m := NewWithHooks("http://broker.local", "tester", nil, hooks)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// /help lists the new verbs.
	hv := runCmd(tm, "help").View()
	for _, want := range []string{"/share", "/login", "/grant", "/topup"} {
		if !strings.Contains(hv, want) {
			t.Errorf("/help missing %q:\n%s", want, hv)
		}
	}

	// Empty balance + /balance surfaces the /topup hint.
	bm, _ := tm.Update(balanceMsg{balance: 0, loggedIn: true})
	bm = runCmd(bm, "balance")
	if !strings.Contains(bm.View(), "/topup") {
		t.Errorf("empty balance should surface /topup:\n%s", bm.View())
	}

	// /login opens the confirmable login panel (never an instant flow); pressing
	// ENTER inside it dispatches the device-flow hook (run the returned cmd) and the
	// resulting loginMsg lands the github login on the status line.
	lm, _ := tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "login" {
		lm, _ = lm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	lm, _ = lm.Update(tea.KeyMsg{Type: tea.KeyEnter}) // run the /login command -> panel
	if called.login {
		t.Errorf("opening the login panel must NOT start the flow on its own")
	}
	var lcmd tea.Cmd
	lm, lcmd = lm.Update(tea.KeyMsg{Type: tea.KeyEnter}) // ENTER in the panel starts the flow
	if lcmd != nil {
		lm, _ = lm.Update(lcmd())
	}
	if !called.login || !strings.Contains(lm.View(), "octocat") {
		t.Errorf("login flow did not complete: called=%v\n%s", called.login, lm.View())
	}

	// /grant list fires the hook (run the returned cmd to execute the closure).
	gm, _ := tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "grant list" {
		gm, _ = gm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	var gcmd tea.Cmd
	gm, gcmd = gm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if gcmd != nil {
		gm, _ = gm.Update(gcmd())
	}
	if !called.grant {
		t.Errorf("grant list hook not called")
	}
	if !strings.Contains(gm.View(), "grant") {
		t.Errorf("grant list result not surfaced:\n%s", gm.View())
	}
}

// stripANSI removes color escape sequences so we can measure visible width.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TestNarrowReflow: at widths 40-64 the view reflows to a single column and no
// rendered line overflows the terminal width; at 80/120 the full grid renders.
func TestNarrowReflow(t *testing.T) {
	for _, w := range []int{40, 50, 64, 80, 120} {
		var m tea.Model = New("http://broker.local", "tester")
		m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		m, _ = m.Update(offersMsg{
			{NodeID: "demo-node", Region: "home", Model: "gpt-oss-20b", PriceIn: 0.2, PriceOut: 0.3, Ctx: 32768, Online: true, FreeNow: true},
			{NodeID: "alt-node", Region: "us-w", Model: "llama-3.3-70b-instruct", PriceIn: 0.25, PriceOut: 0.41, Online: true},
		})
		m, _ = m.Update(balanceMsg{balance: 12.5, loggedIn: true})
		m, _ = m.Update(tickMsg{})
		for _, line := range strings.Split(m.View(), "\n") {
			vis := utf8.RuneCountInString(stripANSI(line))
			if vis > w {
				t.Errorf("width %d: line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
}

// TestLoginStateHeader: the header shows a clear login prompt when anonymous (no
// balance number), and flips to "@login · $balance" once logged in. Balance ONLY
// appears when logged in (the founder-approved anon = no-balance rule).
func TestLoginStateHeader(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// Anonymous: the broker reports logged_in=false (no balance).
	m, _ = m.Update(balanceMsg{loggedIn: false})
	m, _ = m.Update(tickMsg{})
	anon := stripANSI(m.View())
	if !strings.Contains(anon, "not logged in") || !strings.Contains(anon, "/login") {
		t.Errorf("anon header missing the login prompt:\n%s", anon)
	}
	if strings.Contains(anon, "$") {
		t.Errorf("anon header must show NO balance:\n%s", anon)
	}
	// Log in (the in-TUI loginMsg lands the github login), then balance comes back.
	m, _ = m.Update(loginMsg("octocat"))
	m, _ = m.Update(balanceMsg{balance: 12.5, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	in := stripANSI(m.View())
	if !strings.Contains(in, "@octocat") || !strings.Contains(in, "$12.50") {
		t.Errorf("logged-in header missing @login + $balance:\n%s", in)
	}
	if strings.Contains(in, "not logged in") {
		t.Errorf("logged-in header still shows the anon prompt:\n%s", in)
	}
}

// TestAnonPaidConnectPrompt: an anonymous user tuning a PRICED band gets an inline
// "type /login" prompt and no channel opens; a FREE band stays open to anyone.
func TestAnonPaidConnectPrompt(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(balanceMsg{loggedIn: false}) // anonymous
	m, _ = m.Update(offersMsg{{NodeID: "n", Model: "paid", PriceOut: 0.5, Online: true}})
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // tune the priced band
	v := stripANSI(mm.View())
	if !strings.Contains(v, "/login") {
		t.Errorf("anon paid connect should prompt /login:\n%s", v)
	}
	if strings.Contains(v, "open the channel") || strings.Contains(v, "accept") {
		t.Errorf("anon paid connect must NOT open the confirm:\n%s", v)
	}
}

// TestOnAirQuitGuard: quitting while ON AIR (sharing) opens a confirm asking to go
// off air (does not quit); declining stays on air; off air, quit is immediate. The
// on-air session is created against a tiny stub broker so register() succeeds.
func TestOnAirQuitGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	mm := New(srv.URL, "tester")
	mm.proxyAddr = "127.0.0.1:0"
	// Off air: q quits immediately (returns the tea.Quit command).
	if _, cmd := mm.requestQuit(); cmd == nil {
		t.Fatal("off-air quit should be immediate (a tea.Quit cmd)")
	}

	// Go ON AIR: one live in-process share session (registers with the stub broker).
	sess, err := agent.Start(agent.Config{Broker: srv.URL, Upstream: "http://127.0.0.1:0", NodeID: "n", Model: "m", Ctx: 8192, Parallel: 1})
	if err != nil {
		t.Fatalf("agent.Start: %v", err)
	}
	defer sess.Stop()
	mm.shares = map[string]*agent.Session{"m": sess}
	mm.refreshShareHeadline()
	if mm.onAirCount() != 1 {
		t.Fatalf("onAirCount = %d, want 1", mm.onAirCount())
	}

	// q while on air -> NO immediate quit; enters the quit-confirm modal.
	var m tea.Model = mm
	qm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Fatal("on-air quit must NOT quit immediately (it should confirm first)")
	}
	v := stripANSI(qm.View())
	if !strings.Contains(v, "ON AIR") || !strings.Contains(v, "go off air") || !strings.Contains(v, "[y/N]") {
		t.Errorf("quit-guard prompt missing:\n%s", v)
	}

	// Decline (n) -> stays on air, back to browse, no quit.
	dm, dcmd := qm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if dcmd != nil {
		t.Fatal("declining the quit-guard must not quit")
	}
	if !strings.Contains(stripANSI(dm.View()), "still on air") {
		t.Errorf("decline should keep sharing:\n%s", stripANSI(dm.View()))
	}

	// Confirm (y) -> goes off air + quits (a tea.Quit cmd).
	qm2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_, ycmd := qm2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if ycmd == nil {
		t.Fatal("confirming the quit-guard should quit (a tea.Quit cmd)")
	}
}
