package tui

// Executable spec: features/onboarding/welcome_card.feature (@tui) - the one-time welcome card
// drawn inside the TUI. The real model, the real key routing and the real renderer at fixed
// widths; the host's side (when the card is due, writing welcome_seen) is the cmd/rogerai
// runner's. The broker's public GET /promo is served by a real HTTP listener with the broker's
// response shape, the same way the balance tests read /balance.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/muesli/termenv"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/look"
	"rogerai.fm/roger/v7/internal/ownerjoin"
	towerpkg "rogerai.fm/roger/v7/internal/tower"
)

type welcomeBDD struct {
	t        *testing.T
	m        model
	built    bool
	w, h     int
	hooks    Hooks
	seen     int   // SaveWelcomeSeen calls
	seenErr  error // what SaveWelcomeSeen returns
	saves    int   // every other config-writing hook
	scans    map[string]int
	scanEnds map[string]int
	opens    []string
	found    []detect.Found
	promo    *httptest.Server
	tuned    bool
	ownerReq bool
	color    bool
	before   string // status before a key, for "nothing else happens"
	frame    string
	frames   map[int]string
}

func (s *welcomeBDD) reset() {
	s.m, s.built, s.w, s.h = model{}, false, 100, 40
	s.seen, s.seenErr, s.saves = 0, nil, 0
	s.scans, s.scanEnds = map[string]int{}, map[string]int{}
	s.opens, s.found, s.tuned, s.ownerReq, s.color = nil, nil, false, false, false
	s.frame, s.before, s.frames = "", "", map[int]string{}
	if s.promo != nil {
		s.promo.Close()
		s.promo = nil
	}
	s.hooks = Hooks{
		WelcomeDue:      true,
		SaveWelcomeSeen: func() error { s.seen++; return s.seenErr },
		ConsoleURL:      "http://127.0.0.1:4100/?t=tok",
		Station:         "gentle-mongoose-93",
		SaveStation:     func(string) { s.saves++ },
		SaveCompact:     func(bool) { s.saves++ },
		SaveUpstream:    func(string, string) { s.saves++ },
		EdgeScan: func(kind string) func() {
			s.scans[kind]++
			return func() { s.scanEnds[kind]++ }
		},
	}
	lipgloss.SetColorProfile(termenv.Ascii)
}

func (s *welcomeBDD) broker() string {
	if s.promo != nil {
		return s.promo.URL
	}
	return "http://127.0.0.1:1"
}

func (s *welcomeBDD) build() {
	if s.built {
		return
	}
	var tm tea.Model = NewWithHooks(s.broker(), "tester", nil, s.hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
	m := tm.(model)
	if s.tuned {
		m.connected = &offer{NodeID: "nyx-home", Model: "qwen3-coder-30b", Online: true}
	}
	if s.ownerReq {
		m.own.reqs = []ownerjoin.Request{ownerjoin.RequestFor(ownerjoin.Tower{Dir: "/srv/lab", Label: "lab"},
			towerpkg.PairRequest{ID: "r1", Name: "laptop-x", Phrase: "amber-river-71"})}
	}
	s.m, s.built = m, true
}

func (s *welcomeBDD) view() string {
	s.build()
	s.frame = s.m.View()
	return stripANSI(s.frame)
}

func wkey(k string) tea.KeyMsg {
	switch k {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func (s *welcomeBDD) press(k string) error {
	s.build()
	s.before = s.m.status
	tm, _ := s.m.Update(wkey(k))
	m, ok := tm.(model)
	if !ok {
		return fmt.Errorf("Update returned %T", tm)
	}
	s.m = m
	return nil
}

// detected lands the SHARE scan's result the way detectSharesCmd delivers it.
func (s *welcomeBDD) detected() {
	tm, _ := s.m.Update(sharesDetectedMsg{found: s.found})
	s.m = tm.(model)
}

// cardLines is the card's own rows: from the heading to the action bar.
func (s *welcomeBDD) cardLines() []string {
	v := s.view()
	lines := strings.Split(v, "\n")
	start, end := -1, -1
	for i, ln := range lines {
		if start < 0 && strings.Contains(ln, look.Eyebrow(look.WelcomeHeading)) {
			start = i
		}
		if strings.Contains(ln, look.WelcomeKeys+" jump") {
			end = i
		}
	}
	if start < 0 || end < start {
		return nil
	}
	return lines[start : end+1]
}

func (s *welcomeBDD) cardOpen() error {
	s.build()
	if !s.m.welcome {
		return fmt.Errorf("the welcome card is not open")
	}
	if s.cardLines() == nil {
		return fmt.Errorf("the welcome card is not drawn:\n%s", s.view())
	}
	return nil
}

func (s *welcomeBDD) cardClosed() error {
	if s.m.welcome {
		return fmt.Errorf("the welcome card is still open")
	}
	if strings.Contains(s.view(), look.Eyebrow(look.WelcomeHeading)) {
		return fmt.Errorf("the card is still drawn after closing")
	}
	return nil
}

func (s *welcomeBDD) noOverflow() error {
	v := s.view()
	for _, ln := range strings.Split(v, "\n") {
		if lipgloss.Width(ln) > s.w {
			return fmt.Errorf("a line is %d wide at %d columns: %q", lipgloss.Width(ln), s.w, ln)
		}
	}
	return nil
}

// ---- §2 what it says -------------------------------------------------------

func (s *welcomeBDD) showsTable(tbl *godog.Table) error {
	lines := s.cardLines()
	at := -1
	for _, row := range tbl.Rows[1:] {
		key, label, what := row.Cells[0].Value, row.Cells[1].Value, row.Cells[2].Value
		found := -1
		for i, ln := range lines {
			isKey := strings.Contains(ln, " "+key+" ") || strings.Contains(ln, "["+key+"]")
			if i > at && isKey && strings.Contains(ln, label) && strings.Contains(ln, what) {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("no line %q %q %q after row %d:\n%s", key, label, what, at, strings.Join(lines, "\n"))
		}
		at = found
	}
	n := 0
	for _, o := range look.WelcomeOptions {
		for _, ln := range lines {
			if strings.Contains(ln, o.Label) {
				n++
			}
		}
	}
	if n != len(tbl.Rows)-1 {
		return fmt.Errorf("the card shows %d option lines, want exactly %d", n, len(tbl.Rows)-1)
	}
	return nil
}

func (s *welcomeBDD) endsWith(text string) error {
	lines := s.cardLines()
	last := ""
	for _, ln := range lines[:len(lines)-1] { // the action bar is the foot, after it
		if strings.TrimSpace(ln) != "" {
			last = ln
		}
	}
	if !strings.Contains(last, text) {
		return fmt.Errorf("the card's last line is %q, want %q", last, text)
	}
	return nil
}

// sameAsDial: every card key lands where the same key lands from the dial.
func (s *welcomeBDD) sameAsDial() error {
	for _, k := range []string{"1", "0", "2", "3", "w", "l"} {
		card := s.fresh(true)
		dial := s.fresh(false)
		tc, _ := card.Update(wkey(k))
		td, _ := dial.Update(wkey(k))
		mc, md := tc.(model), td.(model)
		if mc.mode != md.mode {
			return fmt.Errorf("%s: the card lands on mode %v, the dial on %v", k, mc.mode, md.mode)
		}
		if k == "w" && stripANSI(mc.status) != stripANSI(md.status) {
			return fmt.Errorf("w: the card says %q, the dial %q", stripANSI(mc.status), stripANSI(md.status))
		}
	}
	return nil
}

func (s *welcomeBDD) fresh(card bool) model {
	h := s.hooks
	h.WelcomeDue = card
	var tm tea.Model = NewWithHooks(s.broker(), "tester", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
	return tm.(model)
}

func (s *welcomeBDD) barMatches() error {
	bar := stripANSI(s.fresh(false).presetBar(100))
	want := map[string]string{"0": "Run an agent", "1": "Chat with a model", "2": "Share a model", "3": "Edge network"}
	sect := map[string]string{"0": "AGENT", "1": "TUNE IN", "2": "SHARE", "3": "EDGE"}
	for k, label := range want {
		if !regexp.MustCompile(`\[` + k + `\]\s*` + sect[k]).MatchString(strings.ToUpper(bar)) {
			return fmt.Errorf("the section bar has no [%s] %s: %q", k, sect[k], bar)
		}
		ok := false
		for _, o := range look.WelcomeOptions {
			ok = ok || (o.Key == k && o.Label == label)
		}
		if !ok {
			return fmt.Errorf("the card has no %q on %s", label, k)
		}
	}
	return nil
}

func (s *welcomeBDD) servePromo(active bool) error {
	s.promo = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/promo" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"seeds_remaining": map[bool]int{true: 12, false: 0}[active], "unlimited": false, "active": active})
	}))
	return nil
}

func (s *welcomeBDD) loginLineNoAmount() error {
	for _, ln := range s.cardLines() {
		if strings.Contains(ln, "Log in") && regexp.MustCompile(`\$\s*\d`).MatchString(ln) {
			return fmt.Errorf("the login line names an amount: %q", ln)
		}
	}
	return nil
}

func (s *welcomeBDD) neverSays(text string) error {
	if strings.Contains(s.view(), text) {
		return fmt.Errorf("the frame says %q", text)
	}
	return nil
}

// picksLogin presses l and lands what the login panel's cmd fetched.
func (s *welcomeBDD) picksLogin() error {
	s.build()
	tm, cmd := s.m.Update(wkey("l"))
	s.m = tm.(model)
	for _, msg := range runCmds(cmd) {
		if _, ok := msg.(promoMsg); ok {
			tm, _ = s.m.Update(msg)
			s.m = tm.(model)
		}
	}
	return nil
}

// runCmds runs a command and any batch under it, keeping only quick ones (the login panel's
// fetch); a tick would block, so a command that is a tick is never run here.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, runCmds(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func (s *welcomeBDD) panelSaysCredit() error {
	v := s.view()
	if s.m.mode != modeLogin {
		return fmt.Errorf("the login panel is not open (mode %v)", s.m.mode)
	}
	if !strings.Contains(v, "new accounts get a starter credit") {
		return fmt.Errorf("the login panel does not mention the starter credit:\n%s", v)
	}
	return nil
}

func (s *welcomeBDD) panelNoAmount() error {
	if regexp.MustCompile(`starter credit[^\n]*\$\s*\d`).MatchString(s.view()) {
		return fmt.Errorf("the login panel names an amount")
	}
	return nil
}

func (s *welcomeBDD) panelNoCredit() error {
	if s.m.mode != modeLogin {
		return fmt.Errorf("the login panel is not open (mode %v)", s.m.mode)
	}
	if strings.Contains(s.view(), "starter credit") {
		return fmt.Errorf("the login panel mentions a starter credit the broker no longer offers")
	}
	return nil
}

// ---- §3 what each line does -------------------------------------------------

func (s *welcomeBDD) dialShown() error {
	if err := s.cardClosed(); err != nil {
		return err
	}
	if s.m.mode != modeBrowse {
		return fmt.Errorf("the dial is not shown (mode %v)", s.m.mode)
	}
	return nil
}

func (s *welcomeBDD) tuneHint() error {
	st := stripANSI(s.m.status)
	if !strings.Contains(st, "enter") || !strings.Contains(st, "tune in") {
		return fmt.Errorf("the hint line does not say how to tune in: %q", st)
	}
	return nil
}

func (s *welcomeBDD) agentOnBand() error {
	if err := s.cardClosed(); err != nil {
		return err
	}
	if s.m.mode != modeAgent || s.m.agent == nil || s.m.agent.model != "qwen3-coder-30b" {
		return fmt.Errorf("[0] AGENT is not open on the tuned band (mode %v)", s.m.mode)
	}
	return nil
}

func (s *welcomeBDD) agentOpen() error {
	if s.m.mode != modeAgent {
		return fmt.Errorf("[0] AGENT is not open (mode %v)", s.m.mode)
	}
	return nil
}

func (s *welcomeBDD) tuneFirst() error {
	st := stripANSI(s.m.status)
	if !strings.Contains(st, "tune a band first") || !strings.Contains(st, "1") {
		return fmt.Errorf("the status does not say to tune first and name the dial's key: %q", st)
	}
	return nil
}

func (s *welcomeBDD) shareRows() error {
	if err := s.cardClosed(); err != nil {
		return err
	}
	s.detected()
	if s.m.mode != modeShare {
		return fmt.Errorf("[2] SHARE is not open (mode %v)", s.m.mode)
	}
	for _, f := range s.found {
		for _, mdl := range f.Models {
			if !strings.Contains(s.view(), mdl) {
				return fmt.Errorf("SHARE has no row for %s:\n%s", mdl, s.view())
			}
		}
	}
	return nil
}

func (s *welcomeBDD) shareSaysOneLine() error {
	if !strings.Contains(s.view(), welcomeShareLine) {
		return fmt.Errorf("SHARE does not say %q:\n%s", welcomeShareLine, s.view())
	}
	return nil
}

func (s *welcomeBDD) guidedSetup() error {
	s.detected()
	if s.m.mode != modeShareSetup {
		return fmt.Errorf("SHARE did not open its guided setup (mode %v)", s.m.mode)
	}
	return nil
}

func (s *welcomeBDD) nothingOnAir() error {
	if s.m.onAir {
		return fmt.Errorf("something went on air without the user turning a row on")
	}
	for _, r := range s.m.ctrl.Snapshot().Rows {
		if r.Link == "on-air" {
			return fmt.Errorf("%s is on air", r.Model)
		}
	}
	return nil
}

func (s *welcomeBDD) shareFromCard() error {
	s.found = []detect.Found{{Name: "ollama", BaseURL: "http://127.0.0.1:11434", Models: []string{"llama3.2"}}}
	if err := s.press("2"); err != nil {
		return err
	}
	s.detected()
	return nil
}

func (s *welcomeBDD) earnRow() error { return s.press("p") }

func (s *welcomeBDD) disclosedFirst() error {
	if s.m.mode == modeShareEditor {
		return fmt.Errorf("a price was asked before the login requirement and payout terms")
	}
	v := strings.Join(strings.Fields(s.view()), " ")
	for _, line := range []string{EarnLoginLine, EarnPayoutLine} {
		if !strings.Contains(v, strings.Join(strings.Fields(line), " ")) {
			return fmt.Errorf("SHARE does not show %q:\n%s", line, s.view())
		}
	}
	return nil
}

func (s *welcomeBDD) sameDisclosure() error {
	// The old earn wizard (cmd/rogerai finishShare) prints these same two lines; the
	// cmd/rogerai runner pins that side.
	if !strings.Contains(EarnLoginLine, "roger login") || !strings.Contains(EarnPayoutLine, "30-day hold") {
		return fmt.Errorf("the disclosure is not the earn wizard's")
	}
	return nil
}

func (s *welcomeBDD) edgeOpens() error {
	if s.m.mode != modeEdge {
		return fmt.Errorf("[3] EDGE is not open (mode %v)", s.m.mode)
	}
	return nil
}

func (s *welcomeBDD) scanOnlyWhileOpen() error {
	if s.scans["edge"] != 1 || s.scanEnds["edge"] != 0 {
		return fmt.Errorf("edge scan opened %d, ended %d; want 1 and 0", s.scans["edge"], s.scanEnds["edge"])
	}
	if err := s.press("1"); err != nil {
		return err
	}
	if s.scanEnds["edge"] != 1 {
		return fmt.Errorf("leaving [3] EDGE did not end the scan")
	}
	return nil
}

func (s *welcomeBDD) consoleSameAsDial() error {
	dial := s.fresh(false)
	td, _ := dial.Update(wkey("w"))
	want := stripANSI(td.(model).status)
	if got := stripANSI(s.m.status); got != want {
		return fmt.Errorf("the card's w says %q, the dial's %q", got, want)
	}
	return nil
}

func (s *welcomeBDD) statusShowsURL() error {
	if !strings.Contains(stripANSI(s.m.status), s.hooks.ConsoleURL) {
		return fmt.Errorf("the status does not show %s: %q", s.hooks.ConsoleURL, stripANSI(s.m.status))
	}
	return nil
}

func (s *welcomeBDD) browserOnlyInteractive() error {
	// Tests run with no terminal: the dial's w opens nothing here, and neither does the card.
	if len(s.opens) != 0 {
		return fmt.Errorf("a browser was opened with no terminal: %v", s.opens)
	}
	return nil
}

func (s *welcomeBDD) consoleOffSays() error {
	st := stripANSI(s.m.status)
	if !strings.Contains(st, "off") || !strings.Contains(st, "--no-webui") {
		return fmt.Errorf("the status does not say the console is off and how to turn it on: %q", st)
	}
	return nil
}

func (s *welcomeBDD) loginFlowStarts() error {
	if s.m.mode != modeLogin {
		return fmt.Errorf("the login panel did not open (mode %v)", s.m.mode)
	}
	return nil
}

func (s *welcomeBDD) closedAndSeen() error {
	if err := s.cardClosed(); err != nil {
		return err
	}
	if s.seen != 1 {
		return fmt.Errorf("welcome_seen saved %d times, want 1", s.seen)
	}
	return nil
}

func (s *welcomeBDD) logoutConfirm() error {
	if s.m.mode != modeLogin {
		return fmt.Errorf("the [L] panel did not open (mode %v)", s.m.mode)
	}
	if !strings.Contains(s.view(), "log out?") {
		return fmt.Errorf("the [L] panel does not offer the logout confirm:\n%s", s.view())
	}
	return nil
}

func (s *welcomeBDD) stillLoggedIn() error {
	if !s.m.loggedInState() {
		return fmt.Errorf("the user was logged out without confirming")
	}
	return nil
}

func (s *welcomeBDD) logoutLine() error {
	for _, ln := range s.cardLines() {
		if strings.Contains(ln, " l ") || strings.Contains(ln, "[l]") {
			if !strings.Contains(ln, "Log out") || !strings.Contains(ln, "@octo") {
				return fmt.Errorf("the l line is %q, want Log out and who", ln)
			}
			return nil
		}
	}
	return fmt.Errorf("no l line on the card")
}

// ---- §4 keys and layout -----------------------------------------------------

func (s *welcomeBDD) firstSelected() error {
	if err := s.cardOpen(); err != nil {
		return err
	}
	if s.m.welcomeSel != 0 {
		return fmt.Errorf("line %d is selected, want the first", s.m.welcomeSel)
	}
	return nil
}

func (s *welcomeBDD) downTwiceEnter() error {
	for _, k := range []string{"down", "down", "enter"} {
		if err := s.press(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *welcomeBDD) sharePicked() error {
	if s.m.mode != modeShare && s.m.mode != modeShareSetup {
		return fmt.Errorf("Share a model was not picked (mode %v)", s.m.mode)
	}
	return nil
}

func (s *welcomeBDD) pressOffCard() error {
	for _, k := range []string{"x", "9", "t", "/", "f", "z", "4", "q "} {
		if k == "q " {
			continue
		}
		if err := s.press(k); err != nil {
			return err
		}
		if err := s.stillOpen(); err != nil {
			return fmt.Errorf("after %q: %w", k, err)
		}
	}
	return nil
}

func (s *welcomeBDD) stillOpen() error {
	if !s.m.welcome {
		return fmt.Errorf("the card closed")
	}
	if s.m.mode != modeBrowse {
		return fmt.Errorf("the mode moved to %v", s.m.mode)
	}
	if s.m.status != s.before {
		return fmt.Errorf("the status changed to %q", stripANSI(s.m.status))
	}
	if s.seen != 0 {
		return fmt.Errorf("welcome_seen was saved")
	}
	return nil
}

func (s *welcomeBDD) noPalette() error {
	if s.m.mode == modeCommand {
		return fmt.Errorf("ctrl+p opened the palette or history")
	}
	return nil
}

func (s *welcomeBDD) cols(n int) error { s.w = n; return nil }

func (s *welcomeBDD) colsRows(w, h int) error { s.w, s.h = w, h; return nil }

func (s *welcomeBDD) descDropped() error {
	v := strings.Join(s.cardLines(), "\n")
	for _, o := range look.WelcomeOptions {
		if !strings.Contains(v, o.Label) {
			return fmt.Errorf("the label %q was cut:\n%s", o.Label, v)
		}
		if strings.Contains(v, o.What) {
			return fmt.Errorf("the description %q is still drawn at %d columns", o.What, s.w)
		}
	}
	return nil
}

func (s *welcomeBDD) onePerLine() error {
	lines := s.cardLines()
	idx := []int{}
	for i, ln := range lines {
		for _, o := range look.WelcomeOptions {
			if strings.Contains(ln, o.Label) {
				idx = append(idx, i)
			}
		}
	}
	if len(idx) != len(look.WelcomeOptions) {
		return fmt.Errorf("%d option lines, want %d", len(idx), len(look.WelcomeOptions))
	}
	for i := 1; i < len(idx); i++ {
		if idx[i] != idx[i-1]+1 {
			return fmt.Errorf("options are not one per line:\n%s", strings.Join(lines, "\n"))
		}
	}
	return nil
}

func (s *welcomeBDD) compactOn() error { s.hooks.Compact = true; return nil }

func (s *welcomeBDD) compactCard() error {
	if err := s.onePerLine(); err != nil {
		return err
	}
	if !s.m.compact {
		return fmt.Errorf("the windowshade is not on")
	}
	if !strings.Contains(strings.Split(s.view(), "\n")[0], "m:expand") {
		return fmt.Errorf("the card is not under the compact header:\n%s", s.view())
	}
	return nil
}

func (s *welcomeBDD) noColor() error {
	quiet = true
	lipgloss.SetColorProfile(termenv.Ascii)
	return nil
}

func (s *welcomeBDD) keysWithoutColor() error {
	if strings.Contains(s.frame, "\x1b[") || strings.Contains(s.view(), "\x1b[") {
		return fmt.Errorf("the frame carries ANSI under NO_COLOR")
	}
	v := strings.Join(s.cardLines(), "\n")
	for _, o := range look.WelcomeOptions {
		if !strings.Contains(v, "["+o.Key+"]") {
			return fmt.Errorf("the key %s is not marked as a key without color:\n%s", o.Key, v)
		}
	}
	return nil
}

func (s *welcomeBDD) asciiGlyphs() error {
	v := strings.Join(s.cardLines(), "\n")
	for _, r := range v {
		if r > 127 && !strings.ContainsRune("·↑↓", r) {
			return fmt.Errorf("the card carries %q under NO_COLOR:\n%s", r, v)
		}
	}
	return nil
}

func (s *welcomeBDD) truecolor() { s.color = true; lipgloss.SetColorProfile(termenv.TrueColor) }

// sgrColors lists every 24-bit foreground/background the card's rows set.
func (s *welcomeBDD) cardANSI() string {
	s.truecolor()
	s.build()
	raw := s.m.View()
	lines := strings.Split(raw, "\n")
	var out []string
	in := false
	for _, ln := range lines {
		p := stripANSI(ln)
		if strings.Contains(p, look.Eyebrow(look.WelcomeHeading)) {
			in = true
		}
		if in {
			out = append(out, ln)
		}
		if strings.Contains(p, look.WelcomeKeys+" jump") {
			break
		}
	}
	return strings.Join(out, "\n")
}

var sgrRGB = regexp.MustCompile(`\x1b\[[0-9;]*?(?:38|48);2;(\d+);(\d+);(\d+)`)

func rgbIn(raw string) map[look.RGB]bool {
	out := map[look.RGB]bool{}
	for _, m := range sgrRGB.FindAllStringSubmatch(raw, -1) {
		var r, g, b int
		fmt.Sscan(m[1], &r)
		fmt.Sscan(m[2], &g)
		fmt.Sscan(m[3], &b)
		out[look.RGB{R: uint8(r), G: uint8(g), B: uint8(b)}] = true
	}
	return out
}

func (s *welcomeBDD) onlyFleetPalette() error {
	raw := s.cardANSI()
	pal := map[look.RGB]bool{}
	for _, c := range look.Palette() {
		pal[c] = true
	}
	// The deck ground the TUI paints under every screen (paintDeck) is not the card's.
	pal[look.RGB{R: 0xFB, G: 0xFB, B: 0xFA}] = true
	used := rgbIn(raw)
	if len(used) == 0 {
		return fmt.Errorf("the card sets no colors in truecolor")
	}
	for c := range used {
		if !pal[c] {
			return fmt.Errorf("the card wears %v, which is not in the Health/Fleet palette", c)
		}
	}
	return nil
}

func (s *welcomeBDD) brandAbove() error {
	v := s.view()
	brand := strings.Index(v, "R O G E R")
	card := strings.Index(v, look.Eyebrow(look.WelcomeHeading))
	if brand < 0 || card < 0 || brand > card {
		return fmt.Errorf("the ROGER·AI brand is not above the card:\n%s", v)
	}
	return nil
}

// ---- §5 getting back --------------------------------------------------------

func (s *welcomeBDD) cardWasClosed() error { return s.press("esc") }

func (s *welcomeBDD) helpStartHere() error {
	if s.m.mode != modeHelp {
		return fmt.Errorf("help did not open (mode %v)", s.m.mode)
	}
	help := stripANSI(s.m.helpView())
	i := strings.Index(help, "start here")
	j := strings.Index(help, "all commands")
	if i < 0 || j < i {
		return fmt.Errorf("help has no start here section:\n%s", help)
	}
	sect := help[i:j]
	n := 0
	for _, ln := range strings.Split(sect, "\n") {
		if strings.TrimSpace(ln) != "" && !strings.Contains(ln, "start here") {
			n++
		}
	}
	for _, o := range look.WelcomeOptions {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(o.Key) + `\s+.*` + regexp.QuoteMeta(o.Label)).MatchString(sect) {
			return fmt.Errorf("start here has no %s %s:\n%s", o.Key, o.Label, sect)
		}
	}
	if n != len(look.WelcomeOptions) {
		return fmt.Errorf("start here has %d lines, want exactly the %d card lines:\n%s", n, len(look.WelcomeOptions), sect)
	}
	return nil
}

// ---- §8 safety -------------------------------------------------------------

func (s *welcomeBDD) picksEveryLine() error {
	for _, o := range look.WelcomeOptions {
		s.reset()
		s.build()
		if err := s.press(o.Key); err != nil {
			return err
		}
		if s.seen != 1 || s.saves != 0 {
			return fmt.Errorf("%s: welcome_seen saved %d times, other config writes %d; want 1 and 0", o.Key, s.seen, s.saves)
		}
		if s.m.onAir || s.m.loginWaiting {
			return fmt.Errorf("%s: picking the line changed sharing or login by itself", o.Key)
		}
	}
	return nil
}

func (s *welcomeBDD) onlySeenWritten() error { return nil } // checked per line in picksEveryLine

func (s *welcomeBDD) onlyThroughScreen() error { return s.nothingOnAir() }

// ---- §9 look ----------------------------------------------------------------

func (s *welcomeBDD) fleetPalette() error {
	raw := s.cardANSI()
	used := rgbIn(raw)
	for name, c := range map[string]look.RGB{"paper": look.Paper, "ink": look.Ink, "muted": look.Muted, "the one red": look.Live} {
		if !used[c] {
			return fmt.Errorf("the card does not use %s", name)
		}
	}
	if !regexp.MustCompile(`38;2;154;150;139[^\x1b]*` + regexp.QuoteMeta(look.Eyebrow(look.WelcomeHeading))).MatchString(raw) {
		return fmt.Errorf("the eyebrow is not muted")
	}
	return nil
}

func (s *welcomeBDD) capsAndGlyphs() error {
	raw := s.cardANSI()
	for _, o := range look.WelcomeOptions {
		cap := regexp.MustCompile(`48;2;243;241;234[^\x1b]*` + regexp.QuoteMeta(" "+o.Key+" "))
		if !cap.MatchString(raw) {
			return fmt.Errorf("the key %s is not a paper key cap", o.Key)
		}
		gl := regexp.MustCompile(`38;2;255;68;56[^\x1b]*` + regexp.QuoteMeta(o.Glyph))
		if !gl.MatchString(raw) {
			return fmt.Errorf("the glyph %s is not in the one red", o.Glyph)
		}
	}
	return nil
}

func (s *welcomeBDD) redRail() error {
	raw := s.cardANSI()
	for _, ln := range strings.Split(raw, "\n") {
		if strings.Contains(stripANSI(ln), look.WelcomeOptions[s.m.welcomeSel].Label) {
			if !regexp.MustCompile(`38;2;255;68;56[^\x1b]*` + look.Rail).MatchString(ln) {
				return fmt.Errorf("the selected line has no red rail: %q", ln)
			}
			return nil
		}
	}
	return fmt.Errorf("no selected line")
}

func (s *welcomeBDD) footBar(a, b, c, d string) error {
	lines := s.cardLines()
	foot := lines[len(lines)-1]
	for _, x := range []string{a, b, c, d} {
		if !strings.Contains(foot, x) {
			return fmt.Errorf("the foot %q has no %q", foot, x)
		}
	}
	return nil
}

func (s *welcomeBDD) lockupUnchanged() error {
	v := strings.Split(s.view(), "\n")
	plain := strings.Split(stripANSI(s.fresh(false).View()), "\n")
	for i := 0; i < 4; i++ {
		if v[i] != plain[i] {
			return fmt.Errorf("row %d differs with the card open:\n%q\n%q", i, v[i], plain[i])
		}
	}
	return nil
}

func (s *welcomeBDD) foldsAt(what string) error {
	if err := s.noOverflow(); err != nil {
		return err
	}
	v := strings.Join(s.cardLines(), "\n")
	switch {
	case strings.HasPrefix(what, "nothing is cut"):
		for _, o := range look.WelcomeOptions {
			if !strings.Contains(v, o.Label) || !strings.Contains(v, o.What) {
				return fmt.Errorf("%q or %q is cut at %d:\n%s", o.Label, o.What, s.w, v)
			}
		}
		if strings.Contains(what, "blank line") {
			for i, ln := range s.cardLines() {
				if strings.Contains(ln, look.WelcomeOptions[0].Label) {
					if strings.TrimSpace(s.cardLines()[i+1]) != "" {
						return fmt.Errorf("no blank line between options at %d", s.w)
					}
				}
			}
		}
	case strings.HasPrefix(what, "the description column is dropped"):
		if err := s.descDropped(); err != nil {
			return err
		}
		return s.onePerLine()
	default:
		return fmt.Errorf("unknown fold %q", what)
	}
	return nil
}

func (s *welcomeBDD) shortTerminal() error {
	v := s.view()
	if n := len(strings.Split(strings.TrimRight(v, "\n"), "\n")); n > s.h {
		return fmt.Errorf("the frame is %d rows on a %d-row terminal", n, s.h)
	}
	return nil
}

func (s *welcomeBDD) barAndSix() error {
	lines := s.cardLines()
	if lines == nil {
		return fmt.Errorf("the card or its action bar is off screen:\n%s", s.view())
	}
	return s.onePerLineOrSpaced()
}

func (s *welcomeBDD) onePerLineOrSpaced() error {
	v := strings.Join(s.cardLines(), "\n")
	for _, o := range look.WelcomeOptions {
		if !strings.Contains(v, o.Label) {
			return fmt.Errorf("%q is off screen", o.Label)
		}
	}
	return nil
}

func (s *welcomeBDD) ownerWaiting() error { s.ownerReq = true; return nil }

func (s *welcomeBDD) bannerAbove() error {
	v := s.view()
	b := strings.Index(v, "laptop-x")
	c := strings.Index(v, look.Eyebrow(look.WelcomeHeading))
	if b < 0 || c < 0 || b > c {
		return fmt.Errorf("the owner-join banner is not above the card:\n%s", v)
	}
	return nil
}

func (s *welcomeBDD) renderAll() error {
	for _, w := range []int{100, 80, 60} {
		s.w, s.built = w, false
		s.build()
		s.frames[w] = s.view()
	}
	return nil
}

func (s *welcomeBDD) everyKeyLabel() error {
	for w, v := range s.frames {
		for _, o := range look.WelcomeOptions {
			if !strings.Contains(v, o.Label) || !strings.Contains(v, "["+o.Key+"]") {
				return fmt.Errorf("%d columns: %s %q missing", w, o.Key, o.Label)
			}
		}
		for _, ln := range strings.Split(v, "\n") {
			if lipgloss.Width(ln) > w {
				return fmt.Errorf("%d columns: a line overflows: %q", w, ln)
			}
		}
	}
	return nil
}

func (s *welcomeBDD) golden() error {
	return goldenFile(s.t, "welcome_card_100.golden", s.frames[100])
}

func TestWelcomeCardFeatureTUI(t *testing.T) {
	st := &welcomeBDD{t: t}
	q := quiet
	t.Cleanup(func() { quiet = q; lipgloss.SetColorProfile(termenv.Ascii) })
	ex := openURLExec
	openURLExec = func(u string) { st.opens = append(st.opens, u) }
	t.Cleanup(func() { openURLExec = ex })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				quiet = q
				st.reset()
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				lipgloss.SetColorProfile(termenv.Ascii)
				return c, err
			})
			sc.Given(`^roger is launched with no arguments on an interactive terminal$`, func() error { return nil })
			sc.Step(`^the welcome card is open$`, st.cardOpen)
			sc.Then(`^it shows exactly these lines, in this order, each with its key:$`, st.showsTable)
			sc.Then(`^it ends with "([^"]*)"$`, st.endsWith)
			sc.Then(`^no line is longer than one terminal line$`, st.noOverflow)
			sc.Then(`^every key it shows does the same thing anywhere else on the dial$`, st.sameAsDial)
			sc.Then(`^the section bar's \[0\] \[1\] \[2\] \[3\] keys match the card's agent, dial, share and edge lines$`, st.barMatches)
			sc.Given(`^the broker no longer seeds new wallets$`, func() error { return st.servePromo(false) })
			sc.Given(`^the broker reports new wallets are seeded with a starter credit$`, func() error { return st.servePromo(true) })
			sc.Then(`^the login line does not mention an amount$`, st.loginLineNoAmount)
			sc.Then(`^it never says "([^"]*)"$`, st.neverSays)
			sc.When(`^the user picks Log in$`, st.picksLogin)
			sc.Then(`^the login panel says new accounts get a starter credit$`, st.panelSaysCredit)
			sc.Then(`^it names no amount$`, st.panelNoAmount)
			sc.Then(`^the login panel does not mention a starter credit$`, st.panelNoCredit)

			sc.When(`^the user presses (\S+) on the welcome card$`, st.press)
			sc.Then(`^the card closes and the dial is shown$`, st.dialShown)
			sc.Then(`^the dial's hint line says how to tune in$`, st.tuneHint)
			sc.Given(`^a band is tuned in$`, func() error { st.tuned = true; return nil })
			sc.Given(`^no band is tuned in$`, func() error { return nil })
			sc.Then(`^the card closes and \[0\] AGENT opens on that band$`, st.agentOnBand)
			sc.Then(`^\[0\] AGENT opens$`, st.agentOpen)
			sc.Then(`^it says in one line to tune a band first, and names the key that opens the dial$`, st.tuneFirst)
			sc.Given(`^a local model server is detected$`, func() error {
				st.found = []detect.Found{{Name: "ollama", BaseURL: "http://127.0.0.1:11434", Models: []string{"llama3.2"}}}
				return nil
			})
			sc.Given(`^no local model server is detected$`, func() error { st.found = nil; return nil })
			sc.Then(`^the card closes and \[2\] SHARE opens with the detected models as rows$`, st.shareRows)
			sc.Then(`^SHARE says in one line: free needs no login, earning needs a login and a price$`, st.shareSaysOneLine)
			sc.Then(`^SHARE opens its guided setup \(start a tool, paste an address, or get a model that fits\)$`, st.guidedSetup)
			sc.Then(`^nothing is put on air until the user chooses a row and turns it on$`, st.nothingOnAir)
			sc.Given(`^SHARE is open from the welcome card$`, st.shareFromCard)
			sc.When(`^the user chooses to earn on a row$`, st.earnRow)
			sc.Then(`^the login requirement and the payout terms are shown before any price is asked$`, st.disclosedFirst)
			sc.Then(`^the same disclosure the old earn wizard showed is used$`, st.sameDisclosure)
			sc.Given(`^this machine has not joined an Edge$`, func() error { return nil })
			sc.Then(`^\[3\] EDGE opens$`, st.edgeOpens)
			sc.Then(`^the LAN scan runs only while \[3\] EDGE is open \(features/edge/scan_on_request\.feature\)$`, st.scanOnlyWhileOpen)
			sc.Then(`^the web console opens the same way the w key opens it from the dial$`, st.consoleSameAsDial)
			sc.Then(`^the status line shows the console's local address$`, st.statusShowsURL)
			sc.Then(`^a browser is opened only on an interactive terminal, as the dial's w does$`, st.browserOnlyInteractive)
			sc.Given(`^roger was started with --no-webui$`, func() error { st.hooks.ConsoleURL = ""; return nil })
			sc.Then(`^the status line says the web console is off for this run, and how to turn it on$`, st.consoleOffSays)
			sc.Then(`^the same login flow as the l key on the dial starts$`, st.loginFlowStarts)
			sc.Then(`^the card is closed and marked seen$`, st.closedAndSeen)
			sc.Given(`^the user is logged in$`, func() error { st.hooks.LinkedLogin = "octo"; return nil })
			sc.Then(`^the \[L\] panel opens with the logout confirm, as the l key does on the dial$`, st.logoutConfirm)
			sc.Then(`^nothing is logged out until the user confirms$`, st.stillLoggedIn)
			sc.Then(`^the l line reads "Log out" and says who is logged in$`, st.logoutLine)

			sc.Given(`^the welcome card is open with the first line selected$`, st.firstSelected)
			sc.When(`^the user presses down twice and enter$`, st.downTwiceEnter)
			sc.Then(`^the third line \(Share a model\) is picked$`, st.sharePicked)
			sc.When(`^the user presses a key that is not on the card$`, st.pressOffCard)
			sc.Then(`^the card stays open and nothing else happens$`, st.stillOpen)
			sc.When(`^the user presses ctrl\+p$`, func() error { return st.press("ctrl+p") })
			sc.Then(`^ctrl\+p never opens a palette or history from the card$`, st.noPalette)
			sc.Given(`^a terminal (\d+) columns wide$`, st.cols)
			sc.Given(`^a terminal (\d+) columns wide and (\d+) rows tall$`, st.colsRows)
			sc.Then(`^no line overflows the width$`, st.noOverflow)
			sc.Then(`^the "what it does" column is dropped before any label is cut$`, st.descDropped)
			sc.Given(`^the compact windowshade mode is on$`, st.compactOn)
			sc.When(`^the TUI opens for the first time$`, st.cardOpen)
			sc.Then(`^the card is drawn as one line per option under the compact header$`, st.compactCard)
			sc.Given(`^NO_COLOR is set$`, st.noColor)
			sc.Then(`^every key is still marked as a key without color$`, st.keysWithoutColor)
			sc.Then(`^the brand and the card use only ASCII-folded glyphs where the TUI already folds them$`, st.asciiGlyphs)
			sc.Then(`^it uses only the Health and Fleet palette the \[3\] screens use$`, st.onlyFleetPalette)
			sc.Then(`^the big ROGER·AI brand stays visible above it$`, st.brandAbove)

			sc.Given(`^the welcome card was closed$`, st.cardWasClosed)
			sc.When(`^the user presses \? on the dial$`, func() error { return st.press("?") })
			sc.Then(`^the help screen opens with a "start here" section listing exactly the same six lines and keys$`, st.helpStartHere)

			sc.When(`^the user picks any line on the welcome card$`, st.picksEveryLine)
			sc.Then(`^only welcome_seen is written by the card$`, st.onlySeenWritten)
			sc.Then(`^sharing, prices, the Edge and login change only through the screen the card opened$`, st.onlyThroughScreen)
			sc.When(`^the user picks Share a model$`, st.shareFromCard)
			sc.Then(`^no model is on air until the user turns a SHARE row on$`, st.nothingOnAir)
			sc.Then(`^nothing is offered on the market without the market confirmation$`, st.nothingOnAir)

			sc.Then(`^it uses the Health/Fleet palette \(paper, ink, a muted eyebrow, the one red\)$`, st.fleetPalette)
			sc.Then(`^each key is a paper key cap and each line leads with its glyph in the one red$`, st.capsAndGlyphs)
			sc.Then(`^the selected line has the red rail at its left$`, st.redRail)
			sc.Then(`^the foot is the fleetview action bar: "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)"$`, st.footBar)
			sc.Then(`^the preset bar and the ▟▄▙ R O G E R · A I lockup are drawn above it, unchanged$`, st.lockupUnchanged)
			sc.When(`^the welcome card is open at (\d+) columns$`, func(n int) error { st.w = n; return st.cardOpen() })
			sc.Then(`^no line overflows$`, st.noOverflow)
			sc.Then(`^(nothing is cut.*|the description column is dropped.*)$`, st.foldsAt)
			sc.Then(`^the frame is no taller than (\d+) rows$`, func(int) error { return st.shortTerminal() })
			sc.Then(`^the action bar and all six lines are still on screen$`, st.barAndSix)
			sc.Given(`^an owner-join request is waiting$`, st.ownerWaiting)
			sc.Then(`^the owner-join banner is still drawn above the card$`, st.bannerAbove)
			sc.Then(`^the card's action bar is still on screen$`, st.barAndSix)
			sc.When(`^the welcome card is rendered by the TUI at 100, 80 and 60 columns$`, st.renderAll)
			sc.Then(`^each frame contains every key and label, and no line overflows$`, st.everyKeyLabel)
			sc.Then(`^the 100-column frame matches the committed golden file for the welcome card$`, st.golden)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/onboarding/welcome_card.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the welcome card scenarios failed")
	}
}
