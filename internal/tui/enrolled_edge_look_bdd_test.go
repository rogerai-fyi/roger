package tui

// Executable spec: features/edge/enrolled_edge_look.feature - the enrolled [3] EDGE drawn in the
// Health/Fleet look, and nothing it does changes. Every frame is the TUI's own View(), at the
// size the scenario names, with no colour (NO_COLOR) and again with colour for the colour rules.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/muesli/termenv"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/look"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// ---- colour runs ------------------------------------------------------------

// lookRun is a stretch of visible text and the foreground and weight it was drawn in.
type lookRun struct {
	text string
	fg   look.RGB
	hasF bool
	bold bool
}

var sgrRE = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// lookRuns splits one ANSI line into runs of one style each.
func lookRuns(line string) []lookRun {
	var out []lookRun
	var cur lookRun
	flush := func() {
		if cur.text != "" {
			out = append(out, cur)
		}
		cur.text = ""
	}
	pos := 0
	for _, loc := range sgrRE.FindAllStringSubmatchIndex(line, -1) {
		cur.text += line[pos:loc[0]]
		pos = loc[1]
		flush()
		codes := strings.Split(line[loc[2]:loc[3]], ";")
		for i := 0; i < len(codes); i++ {
			switch codes[i] {
			case "", "0":
				cur.hasF, cur.bold = false, false
			case "1":
				cur.bold = true
			case "22":
				cur.bold = false
			case "39":
				cur.hasF = false
			case "38", "48":
				if i+4 < len(codes) && codes[i+1] == "2" {
					r, _ := strconv.Atoi(codes[i+2])
					g, _ := strconv.Atoi(codes[i+3])
					b, _ := strconv.Atoi(codes[i+4])
					if codes[i] == "38" {
						cur.fg, cur.hasF = look.RGB{R: uint8(r), G: uint8(g), B: uint8(b)}, true
					}
					i += 4
				}
			}
		}
	}
	cur.text += line[pos:]
	flush()
	return out
}

// styleAt is the run that draws the first rune of the first occurrence of text in line.
func styleAt(line, text string) (lookRun, bool) {
	plain := stripANSI(line)
	at := strings.Index(plain, text)
	if at < 0 {
		return lookRun{}, false
	}
	n := 0
	for _, r := range lookRuns(line) {
		if at < n+len(r.text) {
			return r, true
		}
		n += len(r.text)
	}
	return lookRun{}, false
}

// near reports whether two colours are the same to within the rounding a terminal profile applies.
func near(a, b look.RGB) bool {
	d := func(x, y uint8) bool { v := int(x) - int(y); return v >= -2 && v <= 2 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
}

// cLiveDark is the TUI's own lamp red (the kind glyph tint the hub wears), not the look's red.
var cLiveDark = look.RGB{R: 255, G: 86, B: 54}

// ---- the runner -------------------------------------------------------------

type lookBDD struct {
	t       *testing.T
	f       *enrolledLook
	m       model
	opened  bool
	w, h    int
	pre     []func(*model)
	plain   []string // the frame, no colour
	color   []string // the same frame, in colour
	prev    []string
	effect  model
	before  model
	errText string
	reads   int  // the host reads before the last key
	noColor bool // NO_COLOR is set in the environment for this scenario
}

func (s *lookBDD) reset(t *testing.T) {
	*s = lookBDD{t: t, f: newEnrolledLook(t)}
	lipgloss.SetColorProfile(termenv.Ascii)
	SetLANDial(nil)
}

func (s *lookBDD) open(w, h int) {
	s.w, s.h = w, h
	s.m = s.f.model(s.t, w, h)
	for _, p := range s.pre {
		p(&s.m)
	}
	s.opened = true
	s.render()
}

func (s *lookBDD) render() {
	if s.noColor {
		// The profile the environment asks for, as a terminal would get it: NO_COLOR is set.
		lipgloss.SetColorProfile(termenv.NewOutput(os.Stdout).EnvColorProfile())
		s.plain = strings.Split(s.m.View(), "\n")
	} else {
		s.plain = strings.Split(lookFrame(s.m), "\n")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	s.color = strings.Split(s.m.View(), "\n")
	lipgloss.SetColorProfile(termenv.Ascii)
}

func (s *lookBDD) frame() string { return strings.Join(s.plain, "\n") }

// at is the index of the first plain line containing text, or -1.
func (s *lookBDD) at(text string) int {
	for i, ln := range s.plain {
		if strings.Contains(ln, text) {
			return i
		}
	}
	return -1
}

func (s *lookBDD) need(text string) (int, error) {
	if i := s.at(text); i >= 0 {
		return i, nil
	}
	return -1, fmt.Errorf("no row carries %q:\n%s", text, s.frame())
}

// colorOf is the run drawing text on the first line that carries it.
func (s *lookBDD) colorOf(text string) (lookRun, error) {
	i, err := s.need(text)
	if err != nil {
		return lookRun{}, err
	}
	r, ok := styleAt(s.color[i], text)
	if !ok {
		return lookRun{}, fmt.Errorf("%q not found in the colour frame's row %d: %q", text, i, stripANSI(s.color[i]))
	}
	return r, nil
}

func (s *lookBDD) wantColor(text string, want look.RGB, what string) error {
	r, err := s.colorOf(text)
	if err != nil {
		return err
	}
	if !r.hasF || !near(r.fg, want) {
		return fmt.Errorf("%q is drawn in %v (set %v), want %s %v", text, r.fg, r.hasF, what, want)
	}
	return nil
}

func (s *lookBDD) press(keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		tm, cmd := s.m.Update(msg)
		s.m = tm.(model)
		if k == "i" {
			// The invite is minted off the update loop; land its message as the runtime would.
			for _, msg := range lookMsgs(cmd) {
				if inv, ok := msg.(edgeNearbyInviteMsg); ok {
					tm, _ = s.m.Update(inv)
					s.m = tm.(model)
				}
			}
		}
	}
	s.render()
}

// lookMsgs runs a command the way the runtime would and collects what it sends, leaving any timer
// that has not fired within a moment (the status re-read tick) unread.
func lookMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case msg := <-got:
		if b, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range b {
				out = append(out, lookMsgs(c)...)
			}
			return out
		}
		return []tea.Msg{msg}
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

func (s *lookBDD) selectUnit(name string) func(*model) {
	return func(m *model) {
		for i, it := range m.edgeNetItems() {
			if it.name == name {
				m.edge.netSel, m.edge.netSelSet = i, true
				return
			}
		}
		s.t.Errorf("no unit %q on the map", name)
	}
}

// body is the frame between the header rule and the blank row above the footer.
func (s *lookBDD) body() []string {
	start, end := -1, -1
	for i, ln := range s.plain {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "────") && !strings.HasPrefix(t, "----") {
			continue
		}
		if start < 0 {
			start = i + 1
		} else {
			end = i // the footer's rule
			break
		}
	}
	if start < 0 || end < start {
		return nil
	}
	for end > start && strings.TrimSpace(s.plain[end-1]) == "" {
		end--
	}
	return s.plain[start:end]
}

func (s *lookBDD) colorBody() []string {
	b := s.body()
	if len(b) == 0 {
		return nil
	}
	start := s.at(b[0])
	return s.color[start : start+len(b)]
}

// pressOpen presses keys on [3], opening it at 100x30 first if no scenario step has.
func (s *lookBDD) pressOpen(keys ...string) {
	if !s.opened {
		s.open(100, 30)
	}
	s.press(keys...)
}

// ---- Background ---------------------------------------------------------------

func (s *lookBDD) background(string) error                { return nil }
func (s *lookBDD) background2(string, string) error       { return nil }
func (s *lookBDD) backgroundDevices(string, string) error { return nil }
func (s *lookBDD) backgroundPass() error                  { return nil }

// ---- §1 regions -------------------------------------------------------------

func (s *lookBDD) opensAt(w, h int) error { s.open(w, h); return nil }
func (s *lookBDD) opensAtWithColour(w, h int) error {
	return s.opensAt(w, h)
}
func (s *lookBDD) opensInShade(w, h int) error {
	s.pre = append(s.pre, func(m *model) { m.compact = true })
	s.open(w, h)
	return nil
}

func (s *lookBDD) presetAndBrand() error {
	f := s.frame()
	if !strings.Contains(f, "[3] EDGE") || !strings.Contains(f, "R O G E R") {
		return fmt.Errorf("the preset bar or the brand is missing:\n%s", f)
	}
	return nil
}

func (s *lookBDD) brandRow() int {
	return s.at(look.Fold(look.EmptyEdgeBrand, true))
}

func (s *lookBDD) mutedEyebrowRow(text string) error {
	if s.brandRow() < 0 {
		return fmt.Errorf("no %q row:\n%s", look.Fold(text, true), s.frame())
	}
	r, ok := styleAt(s.color[s.brandRow()], text)
	if !ok || !near(r.fg, look.Muted) {
		return fmt.Errorf("%q is not a muted eyebrow: %+v", text, r)
	}
	return nil
}

func (s *lookBDD) noCabinet(title, onAir string) error {
	f := s.frame()
	for _, bad := range []string{"╔", "═", "║", title, onAir, "▐▓▒░", "░▒▓▌"} {
		if strings.Contains(f, bad) {
			return fmt.Errorf("the cabinet's %q is still drawn:\n%s", bad, f)
		}
	}
	return nil
}

func (s *lookBDD) brandEndsWithRedLamp(lamp string) error {
	i := s.brandRow()
	if i < 0 || !strings.HasSuffix(strings.TrimRight(s.plain[i], " "), lamp) {
		return fmt.Errorf("the brand band does not end with %q:\n%s", lamp, s.frame())
	}
	r, ok := styleAt(s.color[i], lamp)
	if !ok || !near(r.fg, look.Live) {
		return fmt.Errorf("the lamp is not the one red: %+v", r)
	}
	return nil
}

func (s *lookBDD) lampSteady() error {
	a := s.color[s.brandRow()]
	s.f.now = s.f.now.Add(time.Second)
	s.render()
	if b := s.color[s.brandRow()]; a != b {
		return fmt.Errorf("the lamp row moved on a still fleet:\n%q\n%q", a, b)
	}
	return nil
}

func (s *lookBDD) authorityNotEnrolled() error {
	s.f.self.Enrolled = false
	return nil
}

func (s *lookBDD) brandEndsWithMuted(text string) error {
	i := s.brandRow()
	if i < 0 || !strings.HasSuffix(strings.TrimRight(s.plain[i], " "), text) {
		return fmt.Errorf("the brand band does not end with %q:\n%s", text, s.frame())
	}
	if r, _ := styleAt(s.color[i], text); !near(r.fg, look.Muted) {
		return fmt.Errorf("%q is not muted: %+v", text, r)
	}
	return nil
}

func (s *lookBDD) sessionsMix(local, market int) error {
	for i := 0; i < local+market; i++ {
		// Each on its own model, so each is its own row in the SESSIONS block.
		rec := protocol.UsageReceipt{RequestID: "r" + strconv.Itoa(i), NodeID: "house-or-1", Model: "model-" + strconv.Itoa(i)}
		if i < local {
			rec.NodeID, rec.Instance, rec.Local = lookGarageID, "serve", true
		}
		if _, err := s.f.sess.Record(edge.Traffic{Account: "acct-1", Kind: edge.FromUse, Request: rec.RequestID, Receipts: []protocol.UsageReceipt{rec}}); err != nil {
			return err
		}
	}
	return nil
}

// titleRow is the row under the brand band (the first body row in the windowshade).
func (s *lookBDD) titleRow() int { return s.at("YOUR EDGE ·") }

func (s *lookBDD) rowUnderBrandReads(want string) error {
	i := s.brandRow()
	if i < 0 || i+1 >= len(s.plain) {
		return fmt.Errorf("no brand band:\n%s", s.frame())
	}
	got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.plain[i+1]), ">"))
	if got != want {
		return fmt.Errorf("the row under the brand band reads %q, want %q", got, want)
	}
	return nil
}

func (s *lookBDD) titleLeadsWithRail() error {
	i := s.titleRow()
	if i < 0 {
		return fmt.Errorf("no title row:\n%s", s.frame())
	}
	runs := lookRuns(s.color[i])
	var vis []lookRun
	for _, r := range runs {
		if strings.TrimSpace(r.text) != "" {
			vis = append(vis, r)
		}
	}
	if len(vis) < 2 || strings.TrimSpace(vis[0].text) != look.Rail || !near(vis[0].fg, look.Live) {
		return fmt.Errorf("the title row does not lead with the red rail: %+v", vis)
	}
	if strings.TrimSpace(vis[1].text) != "YOUR EDGE" || !near(vis[1].fg, look.Paper) || !vis[1].bold {
		return fmt.Errorf("YOUR EDGE is not bold paper: %+v", vis[1])
	}
	return nil
}

func (s *lookBDD) badgesSeparate() error {
	i := s.titleRow()
	if !strings.Contains(s.plain[i], "LOCAL ROOT · shed · prefers local") {
		return fmt.Errorf("the badges are merged: %q", s.plain[i])
	}
	return nil
}

func (s *lookBDD) runsWithLAN() error {
	SetLANDial(func() client.LANDial { return client.LANDial{} })
	s.t.Cleanup(func() { SetLANDial(nil) })
	return nil
}

func (s *lookBDD) titleReads(want string) error {
	i := s.titleRow()
	if i < 0 {
		return fmt.Errorf("no title row:\n%s", s.frame())
	}
	got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.plain[i]), ">"))
	if got != want {
		return fmt.Errorf("the title row reads %q, want %q", got, want)
	}
	return nil
}

func (s *lookBDD) threeFacts() error {
	t := s.titleRow()
	for k, label := range []string{"THIS MACHINE", "AUTHORITY", "DISCOVERY"} {
		if t+1+k >= len(s.plain) || !strings.HasPrefix(strings.TrimSpace(s.plain[t+1+k]), label) {
			return fmt.Errorf("row %d under the title is not %s:\n%s", k+1, label, s.frame())
		}
	}
	return nil
}

func (s *lookBDD) factsEyebrowInk() error {
	for _, label := range []string{"THIS MACHINE", "AUTHORITY", "DISCOVERY"} {
		i, err := s.need(label)
		if err != nil {
			return err
		}
		ln := s.plain[i]
		if !strings.HasPrefix(ln, "  "+label) || len([]rune(ln)) < 16 {
			return fmt.Errorf("%s is not at the margin: %q", label, ln)
		}
		if r, _ := styleAt(s.color[i], label); !near(r.fg, look.Muted) {
			return fmt.Errorf("%s is not muted: %+v", label, r)
		}
		val := strings.TrimLeft(string([]rune(ln)[2+edgeFactW:]), " ")
		if string([]rune(ln)[2+edgeFactW-1]) != " " || val == "" {
			return fmt.Errorf("%s's value does not start in the 14-column field: %q", label, ln)
		}
		probe := val
		if label == "DISCOVERY" {
			probe = strings.TrimSpace(strings.SplitN(val, " ", 2)[1])
		}
		if r, _ := styleAt(s.color[i], probe[:4]); !near(r.fg, look.Ink) {
			return fmt.Errorf("%s's value is not ink: %+v", label, r)
		}
	}
	return nil
}

func (s *lookBDD) factValue(label string) string {
	i := s.at(label)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(string([]rune(s.plain[i])[2+edgeFactW:]))
}

func (s *lookBDD) factReads(label, want string) error {
	if got := s.factValue(label); got != want {
		return fmt.Errorf("%s reads %q, want %q", label, got, want)
	}
	return nil
}
func (s *lookBDD) thisMachineReads(want string) error { return s.factReads("THIS MACHINE", want) }
func (s *lookBDD) authorityReads(want string) error {
	if got := s.f.self.AuthorityLines()[0]; got != want {
		return fmt.Errorf("the authority fact's first line is %q, want %q", got, want)
	}
	return s.factReads("AUTHORITY", want)
}
func (s *lookBDD) discoveryReads(want string) error {
	got := s.factValue("DISCOVERY")
	eye := look.Fold(s.f.self.Scan().Eye(), true)
	if !strings.HasPrefix(got, eye+" ") {
		return fmt.Errorf("DISCOVERY does not lead with the magic eye %q: %q", eye, got)
	}
	if rest := strings.TrimPrefix(got, eye+" "); rest != want {
		return fmt.Errorf("DISCOVERY reads %q, want %q", rest, want)
	}
	return nil
}

func (s *lookBDD) authorityEllipsis(cols int) error {
	i, err := s.need("AUTHORITY")
	if err != nil {
		return err
	}
	if !strings.HasSuffix(strings.TrimRight(s.plain[i], " "), "…") {
		return fmt.Errorf("the AUTHORITY row is not ended with an ellipsis: %q", s.plain[i])
	}
	return s.noLineExceeds(cols)
}

func (s *lookBDD) selectsAndEnter(name string) error {
	s.open(100, 30)
	s.selectUnit(name)(&s.m)
	s.press("enter")
	return nil
}

func (s *lookBDD) detailAllAuthorityLines() error {
	f := stripANSI(s.m.edgeView(100))
	for _, ln := range s.f.self.AuthorityLines() {
		for _, part := range wrapCommand(ln, max(24, edgeSetupContentWidth(100)-14)) {
			if !strings.Contains(f, part) {
				return fmt.Errorf("the detail page lacks %q:\n%s", part, f)
			}
		}
	}
	return nil
}

func (s *lookBDD) aRowReads(want string) error {
	for _, ln := range s.plain {
		if strings.TrimSpace(ln) == want {
			return nil
		}
	}
	return fmt.Errorf("no row reads %q:\n%s", want, s.frame())
}

func (s *lookBDD) networkEyebrowMuted() error {
	return s.wantColor("YOUR EDGE NETWORK", look.Muted, "muted")
}

func (s *lookBDD) sentenceNotDrawn(text string) error {
	if strings.Contains(s.frame(), text) {
		return fmt.Errorf("%q is drawn:\n%s", text, s.frame())
	}
	return nil
}

func (s *lookBDD) headingReads(want string) error {
	i, err := s.need("YOUR EDGE NETWORK")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(s.plain[i]); got != want {
		return fmt.Errorf("the map heading reads %q, want %q", got, want)
	}
	return nil
}

func (s *lookBDD) noClauseCut() error {
	i, _ := s.need("YOUR EDGE NETWORK")
	tail := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.plain[i]), "YOUR EDGE NETWORK"))
	for _, c := range strings.Split(tail, "  ·  ") {
		ok := false
		for _, whole := range []string{"1 unit on your Edge", "2 devices nearby, not on your Edge yet", "short pane: the list", "narrow pane: the list"} {
			ok = ok || c == whole
		}
		if !ok {
			return fmt.Errorf("the clause %q is cut", c)
		}
	}
	return nil
}

// cardRows are the plain rows of the card whose first row names glyph+name, top to bottom.
func (s *lookBDD) cardRows(head string) (int, []string, error) {
	i, err := s.need(head)
	if err != nil {
		return 0, nil, err
	}
	col := strings.Index(s.plain[i], head) - 2 // the rail sits two cells before the glyph
	rows := []string{}
	for j := i; j < len(s.plain); j++ {
		r := []rune(s.plain[j])
		c := len([]rune(s.plain[j][:max(0, col)]))
		if c >= len(r) || (r[c] != '|' && r[c] != '>') {
			break
		}
		rows = append(rows, strings.TrimSpace(string(r[c+1:])))
	}
	return i, rows, nil
}

func (s *lookBDD) hubCardReads(a, b, c string) error {
	_, rows, err := s.cardRows("◉ shed")
	if err != nil {
		return err
	}
	if len(rows) != 3 || rows[0] != a || rows[1] != b || rows[2] != c {
		return fmt.Errorf("the hub card reads %q", rows)
	}
	return nil
}

func (s *lookBDD) railsNoBorder() error {
	i, rows, err := s.cardRows("◉ shed")
	if err != nil {
		return err
	}
	for k := range rows {
		ln := strings.TrimSpace(s.plain[i+k])
		if !strings.HasPrefix(ln, "|") && !strings.HasPrefix(ln, ">") {
			return fmt.Errorf("hub row %d does not start with a rail: %q", k, ln)
		}
	}
	return s.noBorders()
}

func (s *lookBDD) noBorders() error {
	f := strings.Join(s.body(), "\n")
	for _, g := range []string{"╭", "╮", "╰", "╯", "╔", "╗"} {
		if strings.Contains(f, g) {
			return fmt.Errorf("a border %q is drawn:\n%s", g, f)
		}
	}
	return nil
}

func (s *lookBDD) joinAddressInk() error {
	return s.wantColor("http://192.168.1.5:8791", look.Ink, "ink")
}

// railOf is the colour and weight of a card's rail and name.
func (s *lookBDD) railOf(head string) (rail, name lookRun, err error) {
	i, err := s.need(head)
	if err != nil {
		return
	}
	at := strings.Index(stripANSI(s.color[i]), head)
	plain := stripANSI(s.color[i])
	railText := strings.TrimSpace(plain[:at])
	railText = string([]rune(railText)[len([]rune(railText))-1:])
	rail, _ = styleAt(s.color[i], railText)
	nm := strings.SplitN(head, " ", 2)[1]
	name, _ = styleAt(s.color[i], nm)
	return
}

func (s *lookBDD) selectionOn(name string) error {
	if s.opened {
		s.selectUnit(name)(&s.m)
		s.render()
		return nil
	}
	s.pre = append(s.pre, s.selectUnit(name))
	return nil
}

var lookCards = map[string]string{"shed": "◉ shed", "garage": "▣ garage"}

func (s *lookBDD) cardRailRed(name string) error {
	rail, nm, err := s.railOf(lookCards[name])
	if err != nil {
		return err
	}
	if !near(rail.fg, look.Live) || !near(nm.fg, look.Paper) || !nm.bold {
		return fmt.Errorf("%s: rail %+v name %+v", name, rail, nm)
	}
	return nil
}

func (s *lookBDD) otherRailsDim() error {
	it, _ := s.m.edgeSelectedNet()
	for name, head := range lookCards {
		if name == it.name {
			continue
		}
		rail, nm, err := s.railOf(head)
		if err != nil {
			return err
		}
		if !near(rail.fg, look.Dim) || !near(nm.fg, look.Ink) || nm.bold {
			return fmt.Errorf("%s: rail %+v name %+v", name, rail, nm)
		}
	}
	return nil
}

func (s *lookBDD) memberCardReads(name, a, b string) error {
	_, rows, err := s.cardRows("▣ " + name)
	if err != nil {
		return err
	}
	if len(rows) != 2 || !strings.HasPrefix(rows[0], a) || rows[1] != b {
		return fmt.Errorf("the %s card reads %q", name, rows)
	}
	return nil
}

func (s *lookBDD) hubGlyphCol() int {
	i := s.at("◉ shed")
	return len([]rune(s.plain[i][:strings.Index(s.plain[i], "◉")]))
}

func (s *lookBDD) colOf(i int, text string) int {
	k := strings.Index(s.plain[i], text)
	if k < 0 {
		return -1
	}
	return len([]rune(s.plain[i][:k]))
}

func (s *lookBDD) trunkUnderHub() error {
	i := s.at("no rogers running here yet")
	if i < 0 {
		return fmt.Errorf("no trunk row:\n%s", s.frame())
	}
	if c := s.colOf(i, "│"); c != s.hubGlyphCol() {
		return fmt.Errorf("the trunk drops at column %d, the hub's glyph is at %d:\n%s", c, s.hubGlyphCol(), s.frame())
	}
	return nil
}

func (s *lookBDD) noRogersOnFirstTrunkRow() error {
	hub := s.at("◉ shed")
	_, rows, _ := s.cardRows("◉ shed")
	if s.at("no rogers running here yet") != hub+len(rows) {
		return fmt.Errorf("the trunk's first row is not the one under the hub card:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) dropSharesEyebrow(name string) error {
	i := s.at("no rogers running here yet")
	if i+1 >= len(s.plain) || !strings.Contains(s.plain[i+1], "ON THE EDGE") || !strings.Contains(s.plain[i+1], "┊") {
		return fmt.Errorf("the drop and ON THE EDGE do not share the next row:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) cardUnderDrop(name string) error {
	d := s.at("ON THE EDGE")
	c := s.at("▣ " + name)
	if c != d+1 {
		return fmt.Errorf("%s does not hang directly under its drop:\n%s", name, s.frame())
	}
	if s.colOf(d, "┊") != s.colOf(c, "▣")-2 {
		return fmt.Errorf("%s's rail is not under its drop:\n%s", name, s.frame())
	}
	return nil
}

func (s *lookBDD) dropDialTint(name string) error {
	d := s.at("ON THE EDGE")
	r, ok := styleAt(s.color[d], "┊")
	dial := look.Dial
	if !ok || !near(r.fg, dial) {
		return fmt.Errorf("the drop to %s is %+v, want the dial tint", name, r)
	}
	return nil
}

// wiresFixture adds a LAN-direct member and one not heard from.
func (s *lookBDD) wiresFixture() {
	s.f.enroll(s.t, store.EdgeNode{ID: "n_" + strings.Repeat("d", 48), Name: "den", Kind: "host", Presence: "VERIFIED",
		LastSeen: s.f.now.Unix() - 5, Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.7:8791", Fingerprint: strings.Repeat("cd", 32)}}})
	s.f.enroll(s.t, store.EdgeNode{ID: "n_" + strings.Repeat("e", 48), Name: "attic", Kind: "board", Presence: string(edge.PresenceDark),
		LastSeen: s.f.now.Unix() - 3600})
}

func (s *lookBDD) otherWires() error {
	s.wiresFixture()
	s.open(120, 0)
	for name, want := range map[string]struct {
		glyph string
		tint  look.RGB
	}{"den": {"│", look.Signal}, "attic": {"╎", look.Dim}} {
		c := s.at("▤ attic")
		if name == "den" {
			c = s.at("▣ den")
		}
		if c < 1 {
			return fmt.Errorf("no %s card:\n%s", name, s.frame())
		}
		r, ok := styleAt(s.color[c-1], want.glyph)
		if !ok {
			return fmt.Errorf("%s's drop is not %q:\n%s", name, want.glyph, s.frame())
		}
		if name == "den" && !near(r.fg, cSignalDark()) {
			return fmt.Errorf("den's drop is %+v, want the signal tint", r)
		}
		if name == "attic" && !near(r.fg, cDimDark()) {
			return fmt.Errorf("attic's drop is %+v, want dim", r)
		}
	}
	return nil
}

// The tints the screen already used: the lamp hues in their dark-ground form.
func cSignalDark() look.RGB { return look.Signal }
func cDimDark() look.RGB    { return look.RGB{R: 154, G: 150, B: 139} }

func (s *lookBDD) heartbeatLightsDrop() error {
	s.m.edge.pulses = []edgePulse{{id: lookGarageID}}
	s.render()
	d := s.at("▣ garage") - 1
	r, ok := styleAt(s.color[d], "●")
	if !ok || (!near(r.fg, look.Live) && !near(r.fg, cLiveDark)) {
		return fmt.Errorf("a heartbeat does not light the drop red: %q %+v", s.plain[d], r)
	}
	return nil
}

func (s *lookBDD) legendBesideCard(text, name string) error {
	c := s.at("▣ " + name)
	if c < 0 || !strings.HasSuffix(strings.TrimRight(s.plain[c], " "), text) {
		return fmt.Errorf("%q is not on %s's first row:\n%s", text, name, s.frame())
	}
	return nil
}

func (s *lookBDD) noLegendAllLAN() error {
	f := newEnrolledLook(s.t)
	f.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	f.fleet.SetClock(func() time.Time { return f.now })
	f.enroll(s.t, store.EdgeNode{ID: lookGarageID, Name: "garage", Kind: "host", Presence: "VERIFIED", LastSeen: f.now.Unix() - 20,
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.8:8791", Fingerprint: strings.Repeat("ef", 32)}}})
	out := stripANSI(f.model(s.t, 100, 0).edgeView(100))
	for _, w := range []string{"through a relay", "not heard from", "│ direct"} {
		if strings.Contains(out, w) {
			return fmt.Errorf("a legend %q is drawn on an all-LAN Edge:\n%s", w, out)
		}
	}
	return nil
}

func (s *lookBDD) rogersAndRelay() error {
	s.f.house = []edge.Instance{{Name: "desk"}, {Name: "helper", Agent: true}}
	s.f.selfInst = "desk"
	s.f.enroll(s.t, store.EdgeNode{ID: "n_" + strings.Repeat("a", 48), Name: "attic", Kind: "host", Presence: "VERIFIED",
		LastSeen: s.f.now.Unix() - 9, Transports: []store.EdgeTransport{{Kind: "relay", Addr: "garage"}}})
	// A LAN-direct Pi that reports its hardware, and a unit with a rule firing.
	den := store.EdgeNode{ID: "n_" + strings.Repeat("d", 48), Name: "den", Kind: "host", Presence: "VERIFIED",
		LastSeen: s.f.now.Unix() - 5, Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.7:8791", Fingerprint: strings.Repeat("cd", 32)}},
		Instances: []store.EdgeInstance{{Name: "den", Software: &store.EdgeSoftware{Machine: "Raspberry Pi 5 Model B Rev 1.0"}}}}
	s.f.enroll(s.t, den)
	s.f.alerts = map[string][]string{lookGarageID: {"cpu hot"}}
	return nil
}

func (s *lookBDD) rogersHang() error {
	for _, head := range []string{"● desk", "◆ helper"} {
		i, rows, err := s.cardRows(head)
		if err != nil {
			return err
		}
		if len(rows) != 2 || s.colOf(i, strings.SplitN(head, " ", 2)[0]) != s.hubGlyphCol()+2 {
			return fmt.Errorf("%s is not a rail card hung from the trunk:\n%s", head, s.frame())
		}
		if !strings.Contains(s.plain[i-1], "│") {
			return fmt.Errorf("%s has no drop above it:\n%s", head, s.frame())
		}
	}
	i := s.at("‹ this one")
	if i != s.at("● desk")+1 {
		return fmt.Errorf("‹ this one is not on the roger being looked at:\n%s", s.frame())
	}
	r, _ := styleAt(s.color[i], "‹ this one")
	if !near(r.fg, look.Live) {
		return fmt.Errorf("‹ this one is not red: %+v", r)
	}
	return nil
}

func (s *lookBDD) atticOneDown() error {
	g := s.colOf(s.at("▣ garage"), "▣")
	a := s.colOf(s.at("▣ attic"), "▣")
	if a != g+2 {
		return fmt.Errorf("attic (col %d) is not one level under garage (col %d):\n%s", a, g, s.frame())
	}
	if !strings.Contains(s.plain[s.at("▣ attic")-1], "│") && !strings.Contains(s.plain[s.at("▣ attic")-1], "┊") {
		return fmt.Errorf("attic has no drop of its own:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) cardsKeepWords() error {
	// Today's words, written out: kind, hardware when it fits, via, state word and age, alerts.
	want := map[string][]string{
		"● desk":   {"‹ this one"},
		"◆ helper": {"agent · idle"},
		"▣ garage": {"MACHINE · TROUBLE 20s · 1 alert"},
		"▣ attic":  {"MACHINE · via garage"},
		"▣ den":    {"MACHINE · Pi 5 · ON AIR 5s"},
	}
	for head, words := range want {
		i, rows, err := s.cardRows(head)
		if err != nil {
			return err
		}
		if len(rows) < 2 {
			return fmt.Errorf("%s has no words row:\n%s", head, s.frame())
		}
		for _, w := range words {
			if !strings.Contains(rows[1], w) {
				return fmt.Errorf("%s's card reads %q, missing %q (row %d)", head, rows[1], w, i+1)
			}
		}
	}
	return nil
}

func (s *lookBDD) garageDark() error {
	// garage was last heard 20 seconds ago; a 10-second window ages it out.
	n, err := s.f.fleet.Sweep(10 * time.Second)
	if err == nil && n != 1 {
		err = fmt.Errorf("%d units went dark, want garage alone", n)
	}
	return err
}

func (s *lookBDD) garageDim() error {
	i, rows, err := s.cardRows("▣ garage")
	if err != nil {
		return err
	}
	if len(rows) != 2 {
		return fmt.Errorf("garage's card: %q", rows)
	}
	for _, w := range []string{"▣", "garage"} {
		if r, _ := styleAt(s.color[i], w); !near(r.fg, look.Ash) && !near(r.fg, look.Dim) {
			return fmt.Errorf("%q is not dim: %+v", w, r)
		}
	}
	if !strings.Contains(s.plain[i-1], "╎") {
		return fmt.Errorf("garage's drop is not ╎:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) nearbyUnchanged() error {
	want := s.m.edgeNearbyLines(s.w, true)
	f := s.frame()
	if !strings.Contains(f, strings.Join(want, "\n")) {
		return fmt.Errorf("the NEARBY rows are not §7's rows:\n%s\nwant:\n%s", f, strings.Join(want, "\n"))
	}
	return nil
}

func (s *lookBDD) aRowReadsMutedEyebrow(want string) error {
	if err := s.aRowReads(want); err != nil {
		return err
	}
	return s.wantColor("WHAT YOU CAN DO", look.Muted, "muted")
}

func (s *lookBDD) buttonsOnePerRow() error {
	acts := s.m.edgeActions()
	for _, a := range acts {
		i, err := s.need("[" + a.key + "] " + a.label)
		if err != nil {
			return err
		}
		ln := s.color[i]
		cap, _ := styleAt(ln, " "+a.key+" ")
		lab, _ := styleAt(ln, a.label)
		sub, _ := styleAt(ln, a.sub)
		if !near(lab.fg, look.Paper) || !near(sub.fg, look.Muted) {
			return fmt.Errorf("button %s: label %+v explanation %+v", a.key, lab, sub)
		}
		if !strings.Contains(ln, "48;2;243;241;234") || cap.text == "" {
			return fmt.Errorf("button %s's key is not a paper cap: %q", a.key, ln)
		}
		if a.lit {
			if dot, _ := styleAt(ln, "●"); !near(dot.fg, look.Live) {
				return fmt.Errorf("button %s's lit dot is %+v", a.key, dot)
			}
		}
	}
	return nil
}

func (s *lookBDD) buttonRowCarriesRail(on, off string) error {
	i, err := s.need(on)
	if err != nil {
		return err
	}
	j, err := s.need(off)
	if err != nil {
		return err
	}
	if !strings.Contains(s.plain[i][:strings.Index(s.plain[i], on)], ">") {
		return fmt.Errorf("the %s row carries no rail: %q", on, s.plain[i])
	}
	if strings.Contains(s.plain[j][:strings.Index(s.plain[j], off)], ">") {
		return fmt.Errorf("the %s row carries a rail: %q", off, s.plain[j])
	}
	return nil
}

func (s *lookBDD) pressesTabRight() error { s.pressOpen("tab", "right"); return nil }

func (s *lookBDD) actionBarReads(want string) error {
	b := s.body()
	if len(b) == 0 || strings.TrimSpace(b[len(b)-1]) != want {
		return fmt.Errorf("the action bar reads %q, want %q:\n%s", strings.TrimSpace(b[len(b)-1]), want, s.frame())
	}
	return nil
}

func (s *lookBDD) startsRename(name string) error {
	s.open(100, 0)
	s.selectUnit(name)(&s.m)
	s.m.edge.renaming, s.m.edge.renameBuf = true, "bench"
	s.render()
	return nil
}

func (s *lookBDD) promptEyebrow(text, keys string) error {
	i, err := s.need(text)
	if err != nil {
		return err
	}
	if !strings.Contains(s.plain[i], keys) {
		return fmt.Errorf("the prompt row lacks %q: %q", keys, s.plain[i])
	}
	if r, _ := styleAt(s.color[i], text); !near(r.fg, look.Muted) {
		return fmt.Errorf("%q is not a muted eyebrow: %+v", text, r)
	}
	return nil
}

func (s *lookBDD) inputRedCursor() error {
	i, err := s.need("▶ bench_")
	if err != nil {
		return err
	}
	for _, g := range []string{"▶", "_"} {
		if r, _ := styleAt(s.color[i], g); !near(r.fg, look.Live) {
			return fmt.Errorf("%q is not red: %+v", g, r)
		}
	}
	return nil
}

func (s *lookBDD) lastBodyRowIsBar(want string) error { return s.actionBarReads(want) }

func (s *lookBDD) barKeysPaper() error {
	b := s.colorBody()
	ln := b[len(b)-1]
	for _, k := range []string{"↑↓←→ move", "⏎ open", "tab buttons", "v list", "esc back"} {
		if r, _ := styleAt(ln, k); !near(r.fg, look.Paper) || !r.bold {
			return fmt.Errorf("key %q is %+v", k, r)
		}
	}
	for _, l := range []string{"move", "open", "buttons", "list", "back"} {
		if r, _ := styleAt(ln, l); !near(r.fg, look.Muted) {
			return fmt.Errorf("label %q is %+v", l, r)
		}
	}
	return nil
}

func (s *lookBDD) footerUnchanged() error {
	foot := strings.Split(stripANSI(s.m.footer(s.m.effWidth())), "\n")
	got := s.plain[len(s.plain)-len(foot):]
	for i := range foot {
		if strings.TrimRight(got[i], " ") != strings.TrimRight(foot[i], " ") {
			return fmt.Errorf("the footer moved: %q != %q", got[i], foot[i])
		}
	}
	return nil
}

// ---- §2 colour ----------------------------------------------------------------

// lookTints are the colours the body may wear: the look's palette and the tints the [3] screen
// already used for wires and kind glyphs.
func lookTints() map[look.RGB]bool {
	ok := map[look.RGB]bool{}
	for _, c := range look.Palette() {
		ok[c] = true
	}
	for _, hex := range []string{"#84C255", "#7EA6D8", "#FF5636", "#9A968B", "#F3F1EA"} {
		var r, g, b int
		fmt.Sscanf(hex, "#%02X%02X%02X", &r, &g, &b)
		ok[look.RGB{R: uint8(r), G: uint8(g), B: uint8(b)}] = true
	}
	delete(ok, look.Glow)
	return ok
}

func (s *lookBDD) colourInPalette() error {
	ok := lookTints()
	for _, ln := range s.colorBody() {
		for _, r := range lookRuns(ln) {
			hit := false
			for c := range ok {
				hit = hit || near(r.fg, c)
			}
			if r.hasF && strings.TrimSpace(r.text) != "" && !hit {
				return fmt.Errorf("%q is drawn in %v, outside the look and the screen's tints", r.text, r.fg)
			}
		}
	}
	return nil
}

func (s *lookBDD) rolesColoured() error {
	checks := []struct {
		text string
		want look.RGB
	}{{"YOUR EDGE NETWORK", look.Muted}, {"WHAT YOU CAN DO", look.Muted}, {"THIS MACHINE", look.Muted},
		{"garage", look.Ink}, {"shed · enrolled", look.Ink}, {"gentle-ibex-14", look.Paper}}
	for _, c := range checks {
		if err := s.wantColor(c.text, c.want, fmt.Sprint(c.want)); err != nil {
			return err
		}
	}
	return nil
}

func (s *lookBDD) redOnlyFor() error {
	// Exactly these, and "‹ this one" leading the words of the roger being looked at.
	allowed := map[string]bool{look.Rail: true, "● ON AIR": true, "●": true, look.NearbyInvited: true,
		"◉": true, "◆": true, "▶": true, "_": true}
	for _, ln := range s.colorBody() {
		for _, r := range lookRuns(ln) {
			t := strings.TrimSpace(r.text)
			if !r.hasF || t == "" || (!near(r.fg, look.Live) && !near(r.fg, cLiveDark)) {
				continue
			}
			if !allowed[t] && !strings.HasPrefix(t, "‹ this one") {
				return fmt.Errorf("%q is drawn in red", t)
			}
			if (t == "◉" || t == "◆") && !near(r.fg, cLiveDark) {
				return fmt.Errorf("the kind glyph %q is not its own tint", t)
			}
		}
	}
	return nil
}

func (s *lookBDD) titleInkMutedSeps() error {
	i := s.titleRow()
	for _, r := range lookRuns(s.color[i]) {
		t := strings.TrimSpace(r.text)
		switch {
		case t == "" || t == look.Rail || t == "YOUR EDGE":
		case t == "·":
			if !near(r.fg, look.Muted) {
				return fmt.Errorf("a separator is %+v", r)
			}
		default:
			if !near(r.fg, look.Ink) {
				return fmt.Errorf("the title word %q is %+v, not ink", t, r)
			}
		}
	}
	return nil
}

func (s *lookBDD) factLabelsMuted() error {
	for _, l := range []string{"THIS MACHINE", "AUTHORITY", "DISCOVERY"} {
		if err := s.wantColor(l, look.Muted, "muted"); err != nil {
			return err
		}
	}
	return nil
}

func (s *lookBDD) noGlow() error {
	for _, ln := range s.colorBody() {
		for _, r := range lookRuns(ln) {
			if r.hasF && near(r.fg, look.Glow) && strings.TrimSpace(r.text) != "" {
				return fmt.Errorf("%q is drawn in the glow colour", r.text)
			}
		}
	}
	return nil
}

// ---- §3 sizes -------------------------------------------------------------------

func (s *lookBDD) noLineExceeds(cols int) error {
	for _, ln := range s.plain {
		if lipgloss.Width(ln) > cols {
			return fmt.Errorf("a line is %d wide at %d columns: %q", lipgloss.Width(ln), cols, ln)
		}
	}
	return nil
}

func (s *lookBDD) mapIsTree() error {
	if s.at("ON THE EDGE") < 0 || s.at("◉ shed") < 0 || strings.Contains(s.frame(), "pane: the list") {
		return fmt.Errorf("the map is not the tree:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) mapIsList(note string) error {
	if s.at("ON THE EDGE") >= 0 {
		return fmt.Errorf("the tree is drawn, want the list:\n%s", s.frame())
	}
	i, err := s.need("YOUR EDGE NETWORK")
	if err != nil {
		return err
	}
	if note != "" && !strings.Contains(s.plain[i], note) {
		return fmt.Errorf("the list does not say %q: %q", note, s.plain[i])
	}
	if note == "" && strings.Contains(s.plain[i], "pane: the list") {
		return fmt.Errorf("the owner's list carries a pane note: %q", s.plain[i])
	}
	for _, n := range []string{"◉ shed", "▣ garage"} {
		if s.at(n) < 0 {
			return fmt.Errorf("the list lacks %q:\n%s", n, s.frame())
		}
	}
	return nil
}

func (s *lookBDD) mapDrawnAs(what, note string) error {
	if what == "the tree" {
		return s.mapIsTree()
	}
	return s.mapIsList(note)
}

func (s *lookBDD) brandAndTitleOnScreen() error {
	if s.titleRow() < 0 {
		return fmt.Errorf("no title row:\n%s", s.frame())
	}
	if !s.m.compact && s.brandRow() < 0 {
		return fmt.Errorf("no brand band:\n%s", s.frame())
	}
	return nil
}

// lookShape is what a frame shows of the rows that give way.
type lookShape struct {
	blanks, explained, facts, bar, tree bool
	blankN                              int
}

func (s *lookBDD) shapeAt(h int) lookShape {
	m := s.f.model(s.t, 100, h)
	s.m, s.h = m, h
	s.render()
	var sh lookShape
	b := s.body()
	for _, ln := range b {
		if strings.TrimSpace(ln) == "" {
			sh.blankN++
		}
	}
	sh.blanks = sh.blankN > 0
	sh.explained = s.at("an invite only this device can use") >= 0
	sh.facts = s.at("THIS MACHINE") >= 0
	sh.bar = s.at("esc back") >= 0 && len(b) > 0 && strings.Contains(b[len(b)-1], "esc back")
	sh.tree = s.at("ON THE EDGE") >= 0
	return sh
}

func (s *lookBDD) opensShort() error { return nil }

func (s *lookBDD) giveWayOrder(_ *godog.Table) error {
	// From the tallest frame down, each step down in height may only take the next thing in order.
	full := s.shapeAt(60)
	if !full.tree || !full.facts || !full.explained || !full.bar {
		return fmt.Errorf("a tall frame does not draw everything (%+v):\n%s", full, s.frame())
	}
	rank := func(sh lookShape) [2]int {
		r := 0
		switch {
		case !sh.bar:
			r = 4
		case !sh.facts:
			r = 3
		case !sh.explained:
			r = 2
		case !sh.blanks:
			r = 1
		}
		phase := 0
		if !sh.tree {
			phase = 1
		}
		return [2]int{phase, r}
	}
	last := rank(full)
	seenList := false
	// Down to 20 rows: the least a frame of this Edge fits in with everything given way (9 rows of
	// chrome, 11 that never give way).
	for h := 60; h >= 20; h-- {
		sh := s.shapeAt(h)
		rk := rank(sh)
		if rk[0] < last[0] || (rk[0] == last[0] && rk[1] < last[1]) {
			return fmt.Errorf("at %d rows %+v gave back something an earlier height took", h, sh)
		}
		if rk[0] == 1 && !seenList {
			seenList = true
			if last[1] != 4 {
				return fmt.Errorf("the map gave way at %d rows before the bar did (%+v)", h, last)
			}
			if !strings.Contains(s.frame(), "short pane: the list") {
				return fmt.Errorf("the list does not say so at %d rows:\n%s", h, s.frame())
			}
		}
		// Without the explanations, every blank row has gone; without the facts, every explanation.
		if !sh.explained && sh.blanks {
			return fmt.Errorf("at %d rows an explanation went before the blank rows:\n%s", h, s.frame())
		}
		if !sh.facts && sh.explained {
			return fmt.Errorf("at %d rows a fact went before the explanations:\n%s", h, s.frame())
		}
		if !sh.bar && sh.facts {
			return fmt.Errorf("at %d rows the action bar went before the facts:\n%s", h, s.frame())
		}
		if !sh.explained && s.at("[i] Invite ●   [a] Approve ●") < 0 {
			return fmt.Errorf("at %d rows the caps did not move onto one row:\n%s", h, s.frame())
		}
		last = rk
	}
	if !seenList {
		return fmt.Errorf("the map never gave way to the list")
	}
	return nil
}

func (s *lookBDD) neverGiveWay() error {
	for h := 60; h >= 22; h-- {
		s.shapeAt(h)
		for _, w := range []string{"ROGER . EDGE", "YOUR EDGE ·", "DISCOVERY", "YOUR EDGE NETWORK", "NEARBY", "WHAT YOU CAN DO"} {
			if s.at(w) < 0 {
				return fmt.Errorf("at %d rows %q gave way:\n%s", h, w, s.frame())
			}
		}
	}
	return nil
}

func (s *lookBDD) frameExactly(rows int) error {
	if len(s.plain) != rows {
		return fmt.Errorf("the frame is %d rows, want %d", len(s.plain), rows)
	}
	return nil
}

func (s *lookBDD) showsEverything() error {
	for _, w := range []string{"[3] EDGE", "R O G E R", "ROGER . EDGE", "YOUR EDGE ·", "THIS MACHINE", "AUTHORITY", "DISCOVERY",
		"ON THE EDGE", "NEARBY", "WHAT YOU CAN DO", "an invite only this device can use", "tab buttons", "EDGE - your fleet"} {
		if s.at(w) < 0 {
			return fmt.Errorf("%q is not on the 30-row frame:\n%s", w, s.frame())
		}
	}
	if !strings.Contains(s.plain[0], "[0] AGENT") {
		return fmt.Errorf("the preset bar was clipped:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) opensAtThree() error { return nil }

func (s *lookBDD) neverClipped() error {
	// finishFrame keeps the BOTTOM rows of a frame taller than the terminal, so a frame that had to
	// be clipped has lost its top: the preset bar. One that fits keeps it, and is padded, never cut.
	for _, sz := range [][2]int{{100, 30}, {80, 24}, {60, 24}} {
		m := s.f.model(s.t, sz[0], sz[1])
		out := strings.Split(lookFrame(m), "\n")
		if len(out) != sz[1] || !strings.Contains(out[0], "[0]") {
			return fmt.Errorf("at %dx%d the backstop clipped the frame:\n%s", sz[0], sz[1], strings.Join(out, "\n"))
		}
		foot := strings.Split(stripANSI(m.footer(m.effWidth())), "\n")
		if strings.TrimRight(out[len(out)-1], " ") != strings.TrimRight(foot[len(foot)-1], " ") {
			return fmt.Errorf("at %dx%d the footer is not the last row", sz[0], sz[1])
		}
	}
	return nil
}

func (s *lookBDD) noWindowSize() error { s.h = -1; return nil }

func (s *lookBDD) rendersAtCols(w int) error { s.open(w, 0); return nil }

func (s *lookBDD) everyRowDrawn() error {
	out := stripANSI(s.m.edgeView(s.w))
	for _, w := range []string{"ROGER . EDGE", "THIS MACHINE", "AUTHORITY", "DISCOVERY", "ON THE EDGE", "NEARBY",
		"an invite only this device can use", "tab buttons"} {
		if !strings.Contains(out, w) {
			return fmt.Errorf("%q is not drawn with no measured height:\n%s", w, out)
		}
	}
	return nil
}

func (s *lookBDD) treeAt100x30() error { s.open(100, 30); return s.mapIsTree() }
func (s *lookBDD) listNoShortNote() error {
	return s.mapIsList("")
}
func (s *lookBDD) vAgainTree() error { s.press("v"); return s.mapIsTree() }

func (s *lookBDD) listRowsRails() error {
	for _, head := range []string{"◉ shed", "▣ garage"} {
		i, err := s.need(head)
		if err != nil {
			return err
		}
		lead := strings.TrimSpace(s.plain[i][:strings.Index(s.plain[i], head)])
		if lead != "|" && lead != ">" {
			return fmt.Errorf("the list row %q does not start with a rail", s.plain[i])
		}
		rail, _ := styleAt(s.color[i], strings.TrimSpace(stripANSI(s.color[i]))[:len(look.Rail)])
		if !near(rail.fg, look.Dim) && !near(rail.fg, look.Live) {
			return fmt.Errorf("the list rail is %+v", rail)
		}
	}
	return nil
}

func (s *lookBDD) noTriangleMarker() error { return s.sentenceNotDrawn("▸ ") }

func (s *lookBDD) stateEllipsis() error {
	i, err := s.need("◉ shed")
	if err != nil {
		return err
	}
	if !strings.HasSuffix(strings.TrimRight(s.plain[i], " "), "…") {
		return fmt.Errorf("the hub's state is not ended with an ellipsis at %d columns: %q", s.w, s.plain[i])
	}
	return nil
}

// ---- §4 windowshade ---------------------------------------------------------------

func (s *lookBDD) shadeOn() error {
	s.pre = append(s.pre, func(m *model) { m.compact = true })
	return nil
}

func (s *lookBDD) compactNamesEdge() error {
	if !strings.Contains(s.plain[0], "[3] EDGE") {
		return fmt.Errorf("the compact header does not name [3] EDGE: %q", s.plain[0])
	}
	return nil
}

func (s *lookBDD) firstBodyRowLamp(want string) error {
	b := s.body()
	if len(b) == 0 {
		return fmt.Errorf("no body:\n%s", s.frame())
	}
	got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(b[0]), ">"))
	if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "● ON AIR") {
		return fmt.Errorf("the first body row reads %q", got)
	}
	return nil
}

func (s *lookBDD) noSeparateRow(text string) error {
	if s.at(look.Fold(text, true)) >= 0 {
		return fmt.Errorf("a separate %q row is drawn:\n%s", text, s.frame())
	}
	return nil
}

func (s *lookBDD) pressesMTwice() error {
	s.open(100, 30)
	s.press("m", "m")
	return nil
}

func (s *lookBDD) selectionStillOn(name string) error {
	it, _ := s.m.edgeSelectedNet()
	if it.name != name {
		return fmt.Errorf("the selection is on %q, want %q", it.name, name)
	}
	return nil
}

// ---- §5 NO_COLOR --------------------------------------------------------------------

func (s *lookBDD) noColorSet() error {
	s.noColor = true
	return os.Setenv("NO_COLOR", "1")
}

func (s *lookBDD) noANSI() error {
	if strings.Contains(s.frame(), "\x1b[") {
		return fmt.Errorf("an escape under NO_COLOR")
	}
	return nil
}

func (s *lookBDD) glyphsFold() error {
	f := s.frame()
	for _, w := range []string{"[*] ROGER . EDGE", look.Fold(look.EyeSeen, true) + " scanning", "[i] Invite", "[a] Approve"} {
		if !strings.Contains(f, w) {
			return fmt.Errorf("%q is not in the NO_COLOR frame:\n%s", w, f)
		}
	}
	return nil
}

func (s *lookBDD) railsReadMono() error {
	if !strings.Contains(s.frame(), "> gentle-ibex-14") || s.at("| ◉ shed") < 0 {
		return fmt.Errorf("the rails do not read > and |:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) wiresReadMono() error {
	s.wiresFixture()
	s.open(120, 0)
	for _, g := range []string{"│", "┊", "╎"} {
		if !strings.Contains(strings.Join(s.body(), "\n"), g) {
			return fmt.Errorf("the wire %q is not drawn:\n%s", g, s.frame())
		}
	}
	return nil
}

var wordRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9-]+`)

func (s *lookBDD) everyWordPresent() error {
	mono := map[string]bool{}
	for _, w := range wordRE.FindAllString(strings.Join(s.body(), " "), -1) {
		mono[w] = true
	}
	for _, ln := range s.colorBody() {
		for _, w := range wordRE.FindAllString(stripANSI(ln), -1) {
			if !mono[w] {
				return fmt.Errorf("the word %q is missing under NO_COLOR", w)
			}
		}
	}
	return nil
}

func (s *lookBDD) onlyGarageRail() error {
	i, rows, err := s.cardRows("▣ garage")
	if err != nil {
		return err
	}
	for k := range rows {
		if !strings.HasPrefix(strings.TrimSpace(s.plain[i+k]), ">") {
			return fmt.Errorf("garage's row %q does not start with >", s.plain[i+k])
		}
	}
	n := 0
	for _, ln := range s.body() {
		if strings.HasPrefix(strings.TrimSpace(ln), ">") && !strings.Contains(ln, "YOUR EDGE ·") {
			n++
		}
	}
	if n != len(rows) {
		return fmt.Errorf("%d rows start with >, want only garage's %d:\n%s", n, len(rows), s.frame())
	}
	return nil
}

func (s *lookBDD) shedRowsBar() error {
	i, rows, err := s.cardRows("◉ shed")
	if err != nil {
		return err
	}
	for k := range rows {
		if !strings.HasPrefix(strings.TrimSpace(s.plain[i+k]), "|") {
			return fmt.Errorf("shed's row %q does not start with |", s.plain[i+k])
		}
	}
	return nil
}

func (s *lookBDD) asciiGlyphs() error {
	// Set for this scenario alone: the After hook clears it, so no later scenario is drawn in ASCII.
	return os.Setenv("ROGERAI_ASCII", "1")
}

func (s *lookBDD) everyGlyphASCII() error {
	for _, ln := range s.body() {
		for _, r := range ln {
			if r > 127 {
				return fmt.Errorf("%q is not ASCII in %q", string(r), ln)
			}
		}
	}
	return nil
}

// ---- §6 behaviour -----------------------------------------------------------------

func (s *lookBDD) presses(key string) error {
	if !s.opened {
		s.open(100, 30)
	}
	s.before, s.reads = s.m, s.f.reads
	s.press(key)
	return nil
}

func (s *lookBDD) effectIs(effect string) error {
	b, a := s.before, s.m
	bi, bs := b.edgeSelectedNet()
	_, as := a.edgeSelectedNet()
	q := regexp.MustCompile(`"([^"]*)"`).FindStringSubmatch(effect)
	name := ""
	if q != nil {
		name = q[1]
	}
	ok := false
	switch {
	case strings.HasPrefix(effect, "the selection moves to the next unit"):
		ok = as == bs+1
	case strings.HasPrefix(effect, "the detail page for"):
		ok = a.edge.netDetail && bi.name == name
	case strings.HasPrefix(effect, "the invite panel for"):
		ok = a.edge.nearby.invite != nil && a.edge.nearby.invite.name == name
	case strings.HasPrefix(effect, "the approve flow for"):
		ok = a.edge.nearby.approveN == name
	case strings.HasPrefix(effect, "the first button takes"):
		ok = a.edge.actionFocused && a.edge.actionSel == 0
	case strings.HasPrefix(effect, "the map and the list switch"):
		ok = a.edge.mapList != b.edge.mapList
	case strings.HasPrefix(effect, "the Edge is re-read"):
		ok = s.f.reads > s.reads && strings.Contains(stripANSI(a.status), "re-read the fleet")
	case strings.HasPrefix(effect, "the screen goes back"):
		ok = a.mode != modeEdge
	case strings.HasPrefix(effect, "the windowshade toggles"):
		ok = a.compact != b.compact
	default:
		return fmt.Errorf("no check for %q", effect)
	}
	if !ok {
		return fmt.Errorf("%s did not happen", effect)
	}
	return nil
}

// lookActionsBefore are edgeActions() for each unit of the Background, read from the code at
// 04c878ec before the restyle (key, label, lit, in order).
var lookActionsBefore = map[string][]edgeAction{
	"shed":           {{key: "s", label: "Add Device"}, {key: "y", label: "Allow Machine"}},
	"garage":         {{key: "n", label: "Rename"}},
	"gentle-ibex-14": {{key: "i", label: "Invite", lit: true}, {key: "a", label: "Approve", lit: true}},
	"deskside":       {{key: "i", label: "Invite", lit: true}, {key: "a", label: "Approve", lit: true}},
}

func (s *lookBDD) movesAcrossEveryUnit() error {
	s.open(100, 0)
	return nil
}

func (s *lookBDD) buttonsSameAsBefore() error {
	for i, it := range s.m.edgeNetItems() {
		s.m.edge.netSel, s.m.edge.netSelSet = i, true
		got := s.m.edgeActions()
		want := lookActionsBefore[it.name]
		if len(got) != len(want) {
			return fmt.Errorf("%s offers %d buttons, before %d", it.name, len(got), len(want))
		}
		s.render()
		for k := range got {
			if got[k].key != want[k].key || got[k].label != want[k].label || got[k].lit != want[k].lit {
				return fmt.Errorf("%s button %d is %+v, before %+v", it.name, k, got[k], want[k])
			}
			if s.at("["+got[k].key+"] "+got[k].label) < 0 {
				return fmt.Errorf("%s's button %s is not drawn:\n%s", it.name, got[k].label, s.frame())
			}
		}
	}
	return nil
}

func (s *lookBDD) movesThroughEveryUnit() error { s.open(100, 30); return nil }

func (s *lookBDD) stateUnchanged() error {
	// The order the units are visited in, and their partition, as the code drew them before.
	var names []string
	for _, it := range s.m.edgeNetItems() {
		kind := "hub"
		switch {
		case it.member:
			kind = "member"
		case it.cand:
			kind = "nearby"
		}
		names = append(names, it.name+"/"+kind)
	}
	if want := "shed/hub garage/member gentle-ibex-14/nearby deskside/nearby"; strings.Join(names, " ") != want {
		return fmt.Errorf("the units are %q, before %q", strings.Join(names, " "), want)
	}
	if _, sel := s.m.edgeSelectedNet(); sel != 2 {
		return fmt.Errorf("the default selection is %d, before 2 (the first nearby device)", sel)
	}
	s.press("right")
	if it, _ := s.m.edgeSelectedNet(); it.name != "deskside" {
		return fmt.Errorf("right moved to %q, before deskside", it.name)
	}
	// The held selection survives a device appearing above it.
	s.f.cands = append([]store.EdgeNode{{ID: "n_" + strings.Repeat("f", 48), Name: "fresh", Kind: "mobile", Presence: "CANDIDATE", LastSeen: s.f.now.Unix()}}, s.f.cands...)
	s.m.refreshEdge()
	if it, _ := s.m.edgeSelectedNet(); it.name != "deskside" {
		return fmt.Errorf("a fleet change moved the selection to %q", it.name)
	}
	// The session block is still drawn when traffic flowed.
	if err := s.sessionsMix(1, 0); err != nil {
		return err
	}
	s.m.refreshEdge()
	if !strings.Contains(stripANSI(s.m.edgeView(100)), "SESSIONS · 1 session") {
		return fmt.Errorf("the session block is gone:\n%s", stripANSI(s.m.edgeView(100)))
	}
	return nil
}

func (s *lookBDD) opensOthers() error { return nil }

func (s *lookBDD) othersByteForByte() error {
	for name, got := range untouchedFrames(s.t) {
		if err := untouchedMatches(name, got); err != nil {
			return err
		}
	}
	return nil
}

func (s *lookBDD) wizardCabinet() error {
	if !strings.Contains(untouchedFrames(s.t)["enrolled_untouched_wizard_100.golden"], "╔") {
		return fmt.Errorf("the wizard lost its cabinet")
	}
	return nil
}

func (s *lookBDD) fleetUnreadable() error {
	s.errText = "the fleet file is unreadable"
	s.pre = append(s.pre, func(m *model) { m.edge.err = s.errText })
	return nil
}

func (s *lookBDD) rowUnderFactsRed(want string) error {
	want = strings.ReplaceAll(want, "<reason>", s.errText)
	i, err := s.need("DISCOVERY")
	if err != nil {
		return err
	}
	if strings.TrimSpace(s.plain[i+1]) != want {
		return fmt.Errorf("the row under the facts reads %q, want %q", strings.TrimSpace(s.plain[i+1]), want)
	}
	return s.wantColor(want, look.Live, "the one red")
}

func (s *lookBDD) notOnlyNode() error { return s.sentenceNotDrawn("is the only node") }

// ---- §7 motion -----------------------------------------------------------------------

func (s *lookBDD) stillFleet() error { return nil }

func (s *lookBDD) twoFramesSecondApart() error {
	s.open(100, 30)
	s.prev = append([]string(nil), s.color...)
	s.f.now = s.f.now.Add(time.Second)
	s.m.edgeClock = func() time.Time { return s.f.now }
	s.render()
	return nil
}

func (s *lookBDD) framesSame() error {
	// 2026-10-14 founder ruling R2: NEARBY's ages tick live; every other row is still.
	return s.sameOutsideNearby(s.prev, s.color)
}

// nearbySpan is the rows from the NEARBY eyebrow to the row before WHAT YOU CAN DO.
func nearbySpan(rows []string) (int, int) {
	from, to := -1, -1
	for i, ln := range rows {
		p := stripANSI(ln)
		if from < 0 && strings.HasPrefix(strings.TrimSpace(p), "NEARBY") {
			from = i
		}
		if from >= 0 && strings.Contains(p, "WHAT YOU CAN DO") {
			to = i
			break
		}
	}
	return from, to
}

func (s *lookBDD) sameOutsideNearby(a, b []string) error {
	// The rows outside NEARBY, blank rows aside: a NEARBY row that grows a second row (a stale
	// device's hint) may let a blank row give way, and that moves nothing else.
	rest := func(rows []string) []string {
		from, to := nearbySpan(rows)
		var out []string
		for i, r := range rows {
			if (i < from || i >= to) && strings.TrimSpace(stripANSI(r)) != "" {
				out = append(out, r)
			}
		}
		return out
	}
	ra, rb := rest(a), rest(b)
	if len(ra) != len(rb) {
		return fmt.Errorf("rows outside NEARBY came or went: %d, then %d", len(ra), len(rb))
	}
	for i := range ra {
		if ra[i] != rb[i] {
			return fmt.Errorf("a row moved on a still fleet: %q then %q", stripANSI(ra[i]), stripANSI(rb[i]))
		}
	}
	return nil
}

// ---- §9 the 2026-10-14 rulings ----------------------------------------------------------

func (s *lookBDD) sessionsNoNearby(n int) error {
	s.f.cands = nil
	return s.sessionsMix(n, 0)
}

func (s *lookBDD) frameRowsTopAndStatus(rows int) error {
	if len(s.plain) != rows {
		return fmt.Errorf("the frame is %d rows, want %d:\n%s", len(s.plain), rows, s.frame())
	}
	return s.topAndStatus()
}

// topAndStatus: the tabs (or the windowshade's header) open the frame and the status line ends it,
// so the height backstop cut nothing.
func (s *lookBDD) topAndStatus() error {
	if !strings.Contains(s.plain[0], "[0]") && !strings.Contains(s.plain[0], "[3] EDGE") {
		return fmt.Errorf("the tabs were cut off the top:\n%s", s.frame())
	}
	// The status line is the last row drawn (a short frame is padded below it, never cut).
	foot := strings.Split(stripANSI(s.m.footer(s.m.effWidth())), "\n")
	last := len(s.plain) - 1
	for last > 0 && strings.TrimSpace(s.plain[last]) == "" {
		last--
	}
	if strings.TrimRight(s.plain[last], " ") != strings.TrimRight(foot[len(foot)-1], " ") {
		return fmt.Errorf("the status line is not the last row:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) oneRow(label, want string) error {
	n := 0
	for _, ln := range s.plain {
		if strings.TrimSpace(ln) == want {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%s is not the one row %q:\n%s", label, want, s.frame())
	}
	return nil
}

func (s *lookBDD) noDevicesOneRow(want string) error {
	if s.at("open the Roger app and point it at this Edge:") >= 0 {
		return fmt.Errorf("NO DEVICES YET did not fold:\n%s", s.frame())
	}
	return s.oneRow("NO DEVICES YET", want)
}

func (s *lookBDD) sessionsOneRow(want string) error {
	if s.at("SESSIONS ·") >= 0 {
		return fmt.Errorf("SESSIONS did not fold:\n%s", s.frame())
	}
	return s.oneRow("SESSIONS", want)
}

// lookFolds is which notes a frame has folded, and whether the map is the list.
type lookFolds struct{ noDevices, sessions, list bool }

func (s *lookBDD) foldsAt(h int) lookFolds {
	s.m, s.h = s.f.model(s.t, 100, h), h
	s.render()
	return lookFolds{noDevices: s.at("no devices yet ·") >= 0, sessions: s.at("sessions · a taller pane shows them") >= 0,
		list: s.at("ON THE EDGE") < 0}
}

func (s *lookBDD) opensAtHeights(from, to, w int) error {
	s.w = w
	return nil
}

func (s *lookBDD) noDevicesFoldsFirst() error {
	for h := 60; h >= 20; h-- {
		if f := s.foldsAt(h); f.sessions && !f.noDevices {
			return fmt.Errorf("at %d rows SESSIONS folded before NO DEVICES YET:\n%s", h, s.frame())
		}
	}
	return nil
}

func (s *lookBDD) notesFoldBeforeList() error {
	sawList := false
	for h := 60; h >= 20; h-- {
		f := s.foldsAt(h)
		if f.list && !(f.noDevices && f.sessions) {
			return fmt.Errorf("at %d rows the map gave way before the notes folded:\n%s", h, s.frame())
		}
		sawList = sawList || f.list
	}
	if !sawList {
		return fmt.Errorf("no height drew the list, so the order was never tested")
	}
	return nil
}

func (s *lookBDD) foldsBeforeExplanations() error {
	for h := 60; h >= 20; h-- {
		f := s.foldsAt(h)
		if s.at("another machine joins") < 0 && !(f.noDevices && f.sessions) {
			return fmt.Errorf("at %d rows the explanations gave way before the notes folded:\n%s", h, s.frame())
		}
	}
	return nil
}

func (s *lookBDD) topNeverCut() error {
	for h := 60; h >= 20; h-- {
		s.foldsAt(h)
		if len(s.plain) != h {
			return fmt.Errorf("at %d rows the frame is %d rows", h, len(s.plain))
		}
		if err := s.topAndStatus(); err != nil {
			return fmt.Errorf("at %d rows: %w", h, err)
		}
		if s.brandRow() < 0 || s.titleRow() < 0 {
			return fmt.Errorf("at %d rows the brand band or the title gave way:\n%s", h, s.frame())
		}
	}
	return nil
}

func (s *lookBDD) adoptingWithCode(name string) error {
	for _, c := range s.f.cands {
		if c.Name == name {
			s.f.adopting = []store.EdgeNode{c}
		}
	}
	return s.sessionsMix(8, 0)
}

func (s *lookBDD) opensFolding() error { s.open(100, 24); return nil }

func (s *lookBDD) adoptingOneRow(name string) error {
	n := 0
	for _, ln := range s.plain {
		if strings.Contains(ln, "ADOPTING") {
			n++
			if !strings.Contains(ln, name) || !strings.Contains(ln, "a taller pane shows the code") {
				return fmt.Errorf("the folded ADOPTING row reads %q", strings.TrimSpace(ln))
			}
		}
	}
	if n != 1 || s.at("7KQ2M-9XH4P") >= 0 {
		return fmt.Errorf("ADOPTING did not fold to one row:\n%s", s.frame())
	}
	return nil
}

func (s *lookBDD) discoveryOff() error {
	s.f.self.Discovery = edge.DiscoveryOff
	return nil
}

func (s *lookBDD) clockPastStale() error {
	s.prev = append([]string(nil), s.color...)
	s.reads = s.f.reads
	s.f.now = s.f.now.Add(look.NearbyStaleAfter + time.Minute)
	tm, _ := s.m.Update(edgeStatusTickMsg{})
	s.m = tm.(model)
	s.render()
	if s.f.reads != s.reads {
		return fmt.Errorf("the tick read the Edge (%d reads, before %d)", s.f.reads, s.reads)
	}
	return nil
}

func (s *lookBDD) rowReadsBoth(name, a, b string) error {
	i, err := s.need(name)
	if err != nil {
		return err
	}
	row := s.plain[i]
	if i+1 < len(s.plain) && strings.Contains(s.plain[i+1], "↳") {
		row += s.plain[i+1]
	}
	if !strings.Contains(row, a) || !strings.Contains(row, b) {
		return fmt.Errorf("the %s row reads %q, want %q and %q", name, row, a, b)
	}
	return nil
}

func (s *lookBDD) restStill() error { return s.sameOutsideNearby(s.prev, s.color) }

func (s *lookBDD) holdsMembers(n int) error {
	for i := 2; i <= n; i++ {
		s.f.enroll(s.t, store.EdgeNode{ID: fmt.Sprintf("n_%048d", i), Name: fmt.Sprintf("unit-%02d", i), Kind: "host",
			Presence: "VERIFIED", LastSeen: s.f.now.Unix() - int64(i),
			Transports: []store.EdgeTransport{{Kind: "lan", Addr: fmt.Sprintf("192.168.1.%d:8791", 10+i), Fingerprint: strings.Repeat(fmt.Sprintf("%02x", i), 32)}}})
	}
	return nil
}

func (s *lookBDD) opensAsItIs(w, h int) error { s.open(w, h); return nil }

func (s *lookBDD) railRowsCounted() error {
	if !regexp.MustCompile(`showing \d+-\d+ of 27 units`).MatchString(s.frame()) {
		return fmt.Errorf("the list does not say how many of how many it shows:\n%s", s.frame())
	}
	it, _ := s.m.edgeSelectedNet()
	i, err := s.need(it.name)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(s.plain[i]), ">") {
		return fmt.Errorf("the selection %q is not on screen with its rail:\n%s", it.name, s.frame())
	}
	for _, ln := range s.plain {
		if strings.Contains(ln, "▣ unit-") && !strings.HasPrefix(strings.TrimSpace(ln), "|") && !strings.HasPrefix(strings.TrimSpace(ln), ">") {
			return fmt.Errorf("a unit row has no rail: %q", ln)
		}
	}
	return nil
}

func (s *lookBDD) noOldGraph() error {
	f := s.frame()
	for _, bad := range []string{"›", "CANDIDATES", "a adopts it"} {
		if strings.Contains(f, bad) {
			return fmt.Errorf("the old graph's %q is drawn:\n%s", bad, f)
		}
	}
	if spineRe.MatchString(strings.Join(s.body(), "\n")) {
		return fmt.Errorf("a graph spine is drawn:\n%s", f)
	}
	return nil
}

func (s *lookBDD) matchesGolden(name string) error { return goldenFile(s.t, name+".golden", s.frame()) }

func (s *lookBDD) vSaysTallerPane() error {
	if err := s.mapIsList(edgeShortNote); err != nil {
		return err
	}
	if s.m.edge.mapList {
		return fmt.Errorf("v switched the owner's choice to the list, which the pane already forced")
	}
	if st := stripANSI(s.m.status); !strings.Contains(st, "the map needs a taller pane") {
		return fmt.Errorf("the status line reads %q", st)
	}
	return nil
}

func (s *lookBDD) turnInFlight(name string) error {
	id := s.f.ids[name]
	s.pre = append(s.pre, func(m *model) { m.edge.pulses = []edgePulse{{id: id, step: 1}} })
	return nil
}

func (s *lookBDD) framesRender() error {
	// A frame while the heartbeat is on the wire, then the frame after it has arrived.
	s.open(100, 30)
	s.prev = append([]string(nil), s.color...)
	s.m.edge.pulses = nil
	s.render()
	return nil
}

func (s *lookBDD) onlyDropChanges(name string) error {
	d := s.at("ON THE EDGE")
	for i := range s.color {
		if i >= len(s.prev) {
			return fmt.Errorf("the frame grew")
		}
		if s.color[i] != s.prev[i] && i != d {
			return fmt.Errorf("row %d changed and it is not the drop to %s: %q", i, name, stripANSI(s.color[i]))
		}
	}
	if s.color[d] == s.prev[d] {
		return fmt.Errorf("the drop to %s did not change", name)
	}
	return nil
}

// ---- §8 goldens -------------------------------------------------------------------------

var lookGoldens = []struct {
	name    string
	w, h    int
	compact bool
}{
	{"enrolled_edge_100x30", 100, 30, false},
	{"enrolled_edge_80x24", 80, 24, false},
	{"enrolled_edge_60x24", 60, 24, false},
	{"enrolled_edge_80x24_shade", 80, 24, true},
}

func (s *lookBDD) goldensRendered() error { return nil }

func (s *lookBDD) goldenWords() error {
	for _, g := range lookGoldens {
		m := s.f.model(s.t, g.w, g.h)
		m.compact = g.compact
		out := lookFrame(m)
		for _, w := range []string{"YOUR EDGE ·", "DISCOVERY", "YOUR EDGE NETWORK", "◉ shed", "▣ garage", "NEARBY",
			"gentle-ibex-14", "deskside", "WHAT YOU CAN DO", "[i] Invite", "[a] Approve", "esc back"} {
			if !strings.Contains(out, w) {
				return fmt.Errorf("%s lacks %q:\n%s", g.name, w, out)
			}
		}
		for _, ln := range strings.Split(out, "\n") {
			if lipgloss.Width(ln) > g.w {
				return fmt.Errorf("%s overflows: %q", g.name, ln)
			}
		}
	}
	return nil
}

func (s *lookBDD) goldensMatch() error {
	for _, g := range lookGoldens {
		m := s.f.model(s.t, g.w, g.h)
		m.compact = g.compact
		if err := goldenFile(s.t, g.name+".golden", lookFrame(m)); err != nil {
			return err
		}
	}
	return nil
}

func (s *lookBDD) oldGoldensReplaced() error {
	for _, w := range []string{"100", "80", "60"} {
		if _, err := os.Stat("testdata/nearby_enrolled_" + w + ".golden"); !os.IsNotExist(err) {
			return fmt.Errorf("nearby_enrolled_%s.golden is still committed", w)
		}
	}
	return nil
}

func TestEnrolledEdgeLookFeature(t *testing.T) {
	st := &lookBDD{}
	suite := godog.TestSuite{
		Name: "enrolled-edge-look",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				lipgloss.SetColorProfile(termenv.Ascii)
				SetLANDial(nil)
				_ = os.Unsetenv("NO_COLOR")
				return ctx, os.Unsetenv("ROGERAI_ASCII")
			})
			// Background
			sc.Step(`^this machine "([^"]*)" is the local authority of its Edge and is enrolled$`, st.background)
			sc.Step(`^"([^"]*)" is reachable at "([^"]*)"$`, st.background2)
			sc.Step(`^the member "([^"]*)" is a host reached through a relay, heard 20 seconds ago$`, st.background)
			sc.Step(`^two devices are nearby: the phone "([^"]*)", invited, and the host "([^"]*)", seen 20 minutes ago$`, st.backgroundDevices)
			sc.Step(`^discovery passes every 30 seconds and the last pass was just now$`, st.backgroundPass)
			// §1
			sc.Step(`^the owner opens \[3\] EDGE at (\d+)x(\d+)$`, st.opensAt)
			sc.Step(`^the owner opens \[3\] EDGE at (\d+)x(\d+) with colour$`, st.opensAtWithColour)
			sc.Step(`^the owner opens \[3\] EDGE at (\d+)x(\d+) in the windowshade$`, st.opensInShade)
			sc.Step(`^the preset bar and the big ROGER·AI brand are on screen$`, st.presetAndBrand)
			sc.Step(`^under them "([^"]*)" is a muted eyebrow row$`, st.mutedEyebrowRow)
			sc.Step(`^no double-ruled marquee, "([^"]*)", spectrum shoulders or "([^"]*)" is drawn$`, st.noCabinet)
			sc.Step(`^the brand band ends with a red "([^"]*)" lamp$`, st.brandEndsWithRedLamp)
			sc.Step(`^the lamp is steady from frame to frame on a still fleet$`, st.lampSteady)
			sc.Step(`^this machine is the authority but its own enrollment has not completed$`, st.authorityNotEnrolled)
			sc.Step(`^the brand band ends with a muted "([^"]*)"$`, st.brandEndsWithMuted)
			sc.Step(`^(\d+) sessions went local and (\d+) went to the market$`, st.sessionsMix)
			sc.Step(`^the row under the brand band reads "([^"]*)"$`, st.rowUnderBrandReads)
			sc.Step(`^it leads with the red rail and "YOUR EDGE" in bold paper$`, st.titleLeadsWithRail)
			sc.Step(`^the root badge and the preference badge are separate words, never merged into one$`, st.badgesSeparate)
			sc.Step(`^the TUI runs with --lan$`, st.runsWithLAN)
			sc.Step(`^the title row reads "([^"]*)"$`, st.titleReads)
			sc.Step(`^three rows follow the title: THIS MACHINE, AUTHORITY and DISCOVERY$`, st.threeFacts)
			sc.Step(`^each label is a muted eyebrow in a 14-column field and each value is ink$`, st.factsEyebrowInk)
			sc.Step(`^THIS MACHINE reads "([^"]*)"$`, st.thisMachineReads)
			sc.Step(`^AUTHORITY reads the first line of the authority fact, "([^"]*)"$`, st.authorityReads)
			sc.Step(`^DISCOVERY leads with the magic eye and reads "([^"]*)"$`, st.discoveryReads)
			sc.Step(`^the AUTHORITY row ends with "…" and no line exceeds (\d+) columns$`, st.authorityEllipsis)
			sc.Step(`^the owner selects "([^"]*)" and presses enter$`, st.selectsAndEnter)
			sc.Step(`^the detail page shows every authority line, as today$`, st.detailAllAuthorityLines)
			sc.Step(`^a row reads "([^"]*)"$`, st.aRowReads)
			sc.Step(`^"YOUR EDGE NETWORK" is a muted eyebrow$`, st.networkEyebrowMuted)
			sc.Step(`^the sentence "([^"]*)" is not drawn$`, st.sentenceNotDrawn)
			sc.Step(`^the map heading reads "([^"]*)"$`, st.headingReads)
			sc.Step(`^no clause is cut in half$`, st.noClauseCut)
			sc.Step(`^the hub card reads "([^"]*)", "([^"]*)" and "([^"]*)"$`, st.hubCardReads)
			sc.Step(`^each of its rows starts with a rail and none is drawn inside a border$`, st.railsNoBorder)
			sc.Step(`^the join address is ink, not amber$`, st.joinAddressInk)
			sc.Step(`^the selection is on "([^"]*)"$`, st.selectionOn)
			sc.Step(`^"([^"]*)"'s rail is red and its name is bold paper$`, st.cardRailRed)
			sc.Step(`^every other card's rail is dim and its name is ink$`, st.otherRailsDim)
			sc.Step(`^the "([^"]*)" card reads "▣ garage" and "([^"]*)" behind a rail$`, func(name, b string) error {
				return st.memberCardReads(name, "▣ "+name, b)
			})
			sc.Step(`^no card on the screen is drawn with ╭, ╮, ╰, ╯, ╔ or ╗$`, st.noBorders)
			sc.Step(`^the trunk drops from the column under the hub's glyph$`, st.trunkUnderHub)
			sc.Step(`^"no rogers running here yet" sits on the trunk's first row$`, st.noRogersOnFirstTrunkRow)
			sc.Step(`^the drop to "([^"]*)" and the "ON THE EDGE" eyebrow share the next row$`, st.dropSharesEyebrow)
			sc.Step(`^the "([^"]*)" card hangs directly under its drop$`, st.cardUnderDrop)
			sc.Step(`^the drop to "([^"]*)" is "┊" in the dial tint, because it is reached through a relay$`, st.dropDialTint)
			sc.Step(`^a LAN-direct member's drop is "│" in the signal tint and a member not heard from is "╎" dim$`, st.otherWires)
			sc.Step(`^a heartbeat arriving from a member lights its own drop red for that frame, as today$`, st.heartbeatLightsDrop)
			sc.Step(`^"([^"]*)" is on the "([^"]*)" card's first row, after the card$`, st.legendBesideCard)
			sc.Step(`^no legend is drawn when every wire on screen is LAN-direct$`, st.noLegendAllLAN)
			sc.Step(`^two rogers run on "shed", one of them an agent, and "garage" relays a member "attic"$`, st.rogersAndRelay)
			sc.Step(`^the rogers hang from the trunk as rail cards with "‹ this one" on the roger being looked at, in red$`, st.rogersHang)
			sc.Step(`^"attic" hangs one level down, under "garage", on its own drop$`, st.atticOneDown)
			sc.Step(`^every card keeps today's words: kind, hardware when it fits, via, state word and age, alert count$`, st.cardsKeepWords)
			sc.Step(`^"garage" has gone dark$`, st.garageDark)
			sc.Step(`^the "garage" card is still drawn, its glyph and words dim, its drop "╎"$`, st.garageDim)
			sc.Step(`^the NEARBY rows are byte for byte the rows empty_edge_simple.feature §7 draws at this width$`, st.nearbyUnchanged)
			sc.Step(`^a row reads "([^"]*)" with the eyebrow muted$`, st.aRowReadsMutedEyebrow)
			sc.Step(`^the buttons follow one per row: the key as a paper cap, the label in paper, the red dot when lit, the explanation muted$`, st.buttonsOnePerRow)
			sc.Step(`^no button is drawn inside a border$`, st.noBorders)
			sc.Step(`^the owner presses tab and then right$`, st.pressesTabRight)
			sc.Step(`^the "([^"]*)" row carries the red rail and the "([^"]*)" row does not$`, st.buttonRowCarriesRail)
			sc.Step(`^the action bar reads "([^"]*)"$`, st.actionBarReads)
			sc.Step(`^the owner starts a rename on "([^"]*)"$`, st.startsRename)
			sc.Step(`^the prompt row is a muted eyebrow "([^"]*)" with "([^"]*)"$`, st.promptEyebrow)
			sc.Step(`^the input row leads with the red "▶" and a red cursor, as today$`, st.inputRedCursor)
			sc.Step(`^the last body row is the action bar "([^"]*)"$`, st.lastBodyRowIsBar)
			sc.Step(`^its keys are bold paper and its labels muted$`, st.barKeysPaper)
			sc.Step(`^the centred hint "([^"]*)" is not drawn$`, st.sentenceNotDrawn)
			sc.Step(`^the TUI footer below is unchanged$`, st.footerUnchanged)
			// §2
			sc.Step(`^every colour in the body is one of look.Palette or a wire or kind tint the screen already used$`, st.colourInPalette)
			sc.Step(`^eyebrows are muted, names and values ink, the selected name and key caps paper$`, st.rolesColoured)
			sc.Step(`^red is used only for: the title rail, the selected rail, the ON AIR lamp, a lit dot, a pulse, the invited state and "‹ this one"$`, st.redOnlyFor)
			sc.Step(`^the title row's words are ink with muted separators, not one hue per badge$`, st.titleInkMutedSeps)
			sc.Step(`^the fact labels are muted, not lamp hues$`, st.factLabelsMuted)
			sc.Step(`^no span is drawn in the glow colour$`, st.noGlow)
			// §3
			sc.Step(`^no line exceeds (\d+) columns$`, st.noLineExceeds)
			sc.Step(`^the map is drawn as (the tree|the list), with "([^"]*)"$`, st.mapDrawnAs)
			sc.Step(`^the map is drawn as the tree$`, st.mapIsTree)
			sc.Step(`^the brand band and the title row are on screen$`, st.brandAndTitleOnScreen)
			sc.Step(`^the owner opens \[3\] EDGE at a height that cannot hold every row$`, st.opensShort)
			sc.Step(`^rows give way in this order until the frame fits:$`, st.giveWayOrder)
			sc.Step(`^the brand band, the title row, DISCOVERY, the map heading, NEARBY and WHAT YOU CAN DO never give way$`, st.neverGiveWay)
			sc.Step(`^the frame is exactly (\d+) rows$`, st.frameExactly)
			sc.Step(`^it shows the preset bar, the brand, the brand band, the title, three facts, the tree, NEARBY, WHAT YOU CAN DO with explained buttons, the action bar and the footer$`, st.showsEverything)
			sc.Step(`^the owner opens \[3\] EDGE at 100x30, 80x24 and 60x24$`, st.opensAtThree)
			sc.Step(`^the body plus the chrome is never taller than the terminal before finishFrame runs$`, st.neverClipped)
			sc.Step(`^no window size has been received$`, st.noWindowSize)
			sc.Step(`^the Edge screen renders at (\d+) columns$`, st.rendersAtCols)
			sc.Step(`^every row of every region is drawn and the map is the tree$`, st.everyRowDrawn)
			sc.Step(`^the map is drawn as the tree at 100x30$`, st.treeAt100x30)
			sc.Step(`^the map is drawn as the list, with no "short pane" note$`, st.listNoShortNote)
			sc.Step(`^pressing v again draws the tree$`, st.vAgainTree)
			sc.Step(`^each unit row starts with a rail, red for the selected unit and dim otherwise$`, st.listRowsRails)
			sc.Step(`^the "▸ " marker is not drawn$`, st.noTriangleMarker)
			sc.Step(`^a state too long for its row ends in "…"$`, st.stateEllipsis)
			// §4
			sc.Step(`^the compact windowshade mode is on$`, st.shadeOn)
			sc.Step(`^the compact header names \[3\] EDGE$`, st.compactNamesEdge)
			sc.Step(`^the first body row reads "([^"]*)" followed by the "● ON AIR" lamp$`, st.firstBodyRowLamp)
			sc.Step(`^no separate "([^"]*)" row is drawn$`, st.noSeparateRow)
			sc.Step(`^the owner presses m twice$`, st.pressesMTwice)
			sc.Step(`^the selection is still on "([^"]*)"$`, st.selectionStillOn)
			// §5
			sc.Step(`^NO_COLOR is set$`, st.noColorSet)
			sc.Step(`^the frame carries no ANSI escape$`, st.noANSI)
			sc.Step(`^"▟•▙" folds to "\[\*\]", the magic eye folds through glyphs.Fold, and key caps read "\[i\]" and "\[a\]"$`, st.glyphsFold)
			sc.Step(`^the selected rail reads ">" and an unselected rail reads "\|"$`, st.railsReadMono)
			sc.Step(`^the wires still read "│" direct, "┊" through a relay and "╎" not heard from$`, st.wiresReadMono)
			sc.Step(`^every word of the colour frame is present$`, st.everyWordPresent)
			sc.Step(`^only the "garage" card's rows start with ">"$`, st.onlyGarageRail)
			sc.Step(`^the "shed" card's rows start with "\|"$`, st.shedRowsBar)
			sc.Step(`^the ASCII glyph set is in force$`, st.asciiGlyphs)
			sc.Step(`^every glyph on the screen is one glyphs.Fold or the Edge ASCII fold folds$`, st.everyGlyphASCII)
			// §6
			sc.Step(`^the owner presses (\S+)$`, st.presses)
			sc.Step(`^the effect is (.+), the same as before the restyle$`, st.effectIs)
			sc.Step(`^the owner moves the selection across every unit on the screen$`, st.movesAcrossEveryUnit)
			sc.Step(`^for each unit the buttons offered, their keys, labels, lit state and order equal edgeActions\(\) before the restyle$`, st.buttonsSameAsBefore)
			sc.Step(`^the owner opens \[3\] EDGE and moves through every unit$`, st.movesThroughEveryUnit)
			sc.Step(`^the selection order, the held selection across a fleet change, the map's partition into hub, members and nearby, and the session block are the same as before the restyle$`, st.stateUnchanged)
			sc.Step(`^the owner opens the empty state, the setup wizard and a node's detail page$`, st.opensOthers)
			sc.Step(`^each renders byte for byte as before the restyle$`, st.othersByteForByte)
			sc.Step(`^the setup wizard still wears its marquee cabinet$`, st.wizardCabinet)
			sc.Step(`^the fleet cannot be read$`, st.fleetUnreadable)
			sc.Step(`^the row under the facts reads "([^"]*)" in the one red$`, st.rowUnderFactsRed)
			sc.Step(`^the screen does not claim this machine is the only node$`, st.notOnlyNode)
			// §7
			sc.Step(`^no traffic, no heartbeat and no pass completes$`, st.stillFleet)
			sc.Step(`^two frames render a second apart$`, st.twoFramesSecondApart)
			sc.Step(`^they are byte for byte the same$`, st.framesSame)
			sc.Step(`^a turn in flight to "([^"]*)"$`, st.turnInFlight)
			sc.Step(`^the frames render$`, st.framesRender)
			sc.Step(`^only the drop to "([^"]*)" changes between frames$`, st.onlyDropChanges)
			// §9 the 2026-10-14 rulings
			sc.Step(`^(\d+) sessions went through this Edge and no device is nearby$`, st.sessionsNoNearby)
			sc.Step(`^the frame is exactly (\d+) rows, with the tabs first and the status line last$`, st.frameRowsTopAndStatus)
			sc.Step(`^NO DEVICES YET is one row: "([^"]*)"$`, st.noDevicesOneRow)
			sc.Step(`^SESSIONS is one row: "([^"]*)"$`, st.sessionsOneRow)
			sc.Step(`^the owner opens \[3\] EDGE at heights from (\d+) rows down to \d+ at (\d+) columns$`, func(from, w int) error { return st.opensAtHeights(from, 20, w) })
			sc.Step(`^NO DEVICES YET folds before ADOPTING and SESSIONS do$`, st.noDevicesFoldsFirst)
			sc.Step(`^ADOPTING and SESSIONS fold before the map becomes the list$`, st.notesFoldBeforeList)
			sc.Step(`^both fold before the buttons' explanations give way$`, st.foldsBeforeExplanations)
			sc.Step(`^no height cuts the tabs, the brand band or the status line$`, st.topNeverCut)
			sc.Step(`^"([^"]*)" is adopting with a code$`, st.adoptingWithCode)
			sc.Step(`^the owner opens \[3\] EDGE at a height that folds the notes$`, st.opensFolding)
			sc.Step(`^ADOPTING is one row naming "([^"]*)", saying a taller pane shows the code$`, st.adoptingOneRow)
			sc.Step(`^discovery is off$`, st.discoveryOff)
			sc.Step(`^the clock moves past the stale mark and the 2-second tick comes, with no read of the Edge$`, st.clockPastStale)
			sc.Step(`^the "([^"]*)" row reads "([^"]*)" and "([^"]*)"$`, st.rowReadsBoth)
			sc.Step(`^every row outside NEARBY is the same as before the clock moved$`, st.restStill)
			sc.Step(`^the Edge holds (\d+) members$`, st.holdsMembers)
			sc.Step(`^the owner opens \[3\] EDGE at (\d+)x(\d+) as it is$`, st.opensAsItIs)
			sc.Step(`^the units are rail rows, the list says how many of how many it shows, and the selection is on screen$`, st.railRowsCounted)
			sc.Step(`^no "›" mark, no "CANDIDATES", no "a adopts it" and no graph spine is drawn$`, st.noOldGraph)
			sc.Step(`^the frame matches its committed golden file (\S+)$`, st.matchesGolden)
			sc.Step(`^the map is still the list and the status line says the map needs a taller pane$`, st.vSaysTallerPane)
			// §8
			sc.Step(`^the enrolled \[3\] EDGE is rendered by the TUI's View\(\) at 100x30, 80x24 and 60x24, and at 80x24 in the windowshade$`, st.goldensRendered)
			sc.Step(`^each frame contains every key and word the region rules above name, and no line overflows$`, st.goldenWords)
			sc.Step(`^each matches its committed golden file: enrolled_edge_100x30, enrolled_edge_80x24, enrolled_edge_60x24 and enrolled_edge_80x24_shade$`, st.goldensMatch)
			sc.Step(`^the old nearby_enrolled_100, _80 and _60 goldens are replaced by these$`, st.oldGoldensReplaced)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/enrolled_edge_look.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the enrolled [3] EDGE look scenarios failed")
	}
}
