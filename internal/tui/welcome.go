package tui

// THE WELCOME CARD (features/onboarding/welcome_card.feature): a one-time card over the dial,
// the first time the TUI opens. Each line names one thing roger does and its key, which is the
// TUI's own key for it, so choosing a line lands exactly where that key lands from the dial.
// The card configures nothing; the only thing it writes is welcome_seen, through the host's
// SaveWelcomeSeen hook. It wears the Health/Fleet look (internal/look), painted with lipgloss.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"rogerai.fm/roger/v7/internal/look"
)

// promoMsg carries the broker's public GET /promo read: whether new accounts still get a
// starter credit.
type promoMsg struct{ active, ok bool }

// The earn disclosure, shown before any price is asked: the same two lines the old earn
// wizard printed (cmd/rogerai finishShare prints these constants).
const (
	EarnLoginLine  = "earning needs a linked GitHub: you'll run `roger login` once before going on air."
	EarnPayoutLine = "payouts when you earn: 30-day hold (10% reserved to day 90), $25 min, monthly (`roger payout status` for details)."
)

// welcomeShareLine is SHARE's one line when the card opened it.
const welcomeShareLine = "free needs no login · earning needs a login and a price"

// fetchPromo reads the broker's public promo state. Best-effort: a failed read says nothing.
func fetchPromo(broker string) tea.Cmd {
	return func() tea.Msg {
		c := http.Client{Timeout: 5 * time.Second}
		resp, err := c.Get(broker + "/promo")
		if err != nil {
			return promoMsg{}
		}
		defer resp.Body.Close()
		var p struct {
			Active bool `json:"active"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&p) != nil {
			return promoMsg{}
		}
		return promoMsg{active: p.Active, ok: true}
	}
}

// onWelcomeKey owns every key while the card is open (ctrl+c still quits): arrows move, enter
// and the six keys pick, esc and q close. Anything else does nothing at all.
func (m model) onWelcomeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up":
		if m.welcomeSel > 0 {
			m.welcomeSel--
		}
		return m, nil
	case "down":
		if m.welcomeSel < len(look.WelcomeOptions)-1 {
			m.welcomeSel++
		}
		return m, nil
	case "enter":
		return m.welcomePick(look.WelcomeOptions[m.welcomeSel].Key)
	case "esc", "q":
		return m.closeWelcome(), nil
	}
	for _, o := range look.WelcomeOptions {
		if k.String() == o.Key {
			return m.welcomePick(o.Key)
		}
	}
	return m, nil
}

// closeWelcome closes the card and remembers it was seen. A failed save never blocks the app.
func (m model) closeWelcome() model {
	m.welcome = false
	if m.hooks.SaveWelcomeSeen != nil {
		if err := m.hooks.SaveWelcomeSeen(); err != nil {
			m.welcomeErr = "the welcome choice could not be remembered: " + err.Error()
			m.status = stEmber.Render(m.welcomeErr)
		}
	}
	return m
}

// welcomePick closes the card and goes where the key goes from the dial.
func (m model) welcomePick(key string) (tea.Model, tea.Cmd) {
	m = m.closeWelcome()
	var nm tea.Model = m
	var cmd tea.Cmd
	switch key {
	case "1":
		m.mode = modeBrowse
		m.status = stDim.Render("pick a band with ↑↓, then enter to tune in")
		nm = m
	case "w":
		m.status = stDim.Render(m.openConsole())
		nm = m
	default:
		nm, cmd, _ = m.presetForKey(key)
	}
	out := nm.(model)
	switch key {
	case "0":
		if out.agent != nil && out.agent.model == "" {
			out.status = stDim.Render("tune a band first: esc, then 1 opens the dial (a free band is being looked for meanwhile)")
		}
	case "2":
		out.shareIntro = true
	}
	if out.welcomeErr != "" {
		out.status = stEmber.Render(out.welcomeErr)
	}
	return out, cmd
}

// lookMono reports a frame drawn with no color at all: key caps become bracketed keys and the
// look's glyphs fold to ASCII, so every key and mark still reads.
func lookMono() bool { return lipgloss.ColorProfile() == termenv.Ascii }

func lookStyle(c look.RGB) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex()))
}

// lookKeyCap is a key as a paper cap, or [k] with no color.
func lookKeyCap(k string, mono bool) string {
	if mono {
		return look.KeyCap(k, true)
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(look.Ground.Hex())).
		Background(lipgloss.Color(look.Paper.Hex())).Render(look.KeyCap(k, false))
}

// lookActionBar is the action bar at a screen's foot: keys in paper, labels muted.
func lookActionBar(acts []look.Action) string {
	key, lab := lookStyle(look.Paper).Bold(true), lookStyle(look.Muted)
	parts := make([]string, 0, len(acts))
	for _, a := range acts {
		parts = append(parts, key.Render(a.Key)+lab.Render(" "+a.Label))
	}
	return "  " + strings.Join(parts, lab.Render(look.ActionGap))
}

// welcomeView draws the card in the rows left under the header. When rows are short it drops
// the blank lines first and the reopen line next, so the six lines and the action bar always
// stay on screen.
func (m model) welcomeView(w, rows int) string {
	mono := lookMono()
	muted, ink, paper, live := lookStyle(look.Muted), lookStyle(look.Ink), lookStyle(look.Paper), lookStyle(look.Live)
	narrow := m.compact || m.narrow()
	n := len(look.WelcomeOptions)
	spacious := !m.compact && !narrow && (rows <= 0 || rows >= 2*n+7)
	again := !m.compact && (rows <= 0 || rows >= n+5)
	var out []string
	out = append(out, "  "+muted.Render(look.Eyebrow(look.WelcomeHeading))+muted.Render("  "+look.WelcomeSkip))
	if !m.compact && (rows <= 0 || rows >= n+4) {
		out = append(out, "")
	}
	for i, o := range look.WelcomeOptions {
		if o.Key == "l" && m.loggedInState() {
			o = look.WelcomeLoggedIn(m.ghLogin)
		}
		rail := "  "
		lab, what := ink.Bold(true), muted
		if i == m.welcomeSel {
			rail = live.Bold(true).Render(look.Fold(look.Rail, mono)) + " "
			lab, what = paper.Bold(true), ink
		}
		g := look.Fold(o.Glyph, mono)
		row := rail + lookKeyCap(o.Key, mono) + " " + live.Render(g) + strings.Repeat(" ", max(1, 6-lipgloss.Width(g))) +
			lab.Render(o.Label)
		if !narrow {
			row += strings.Repeat(" ", max(2, 20-lipgloss.Width(o.Label))) + what.Render(o.What)
		}
		out = append(out, row)
		if spacious && i < n-1 {
			out = append(out, "")
		}
	}
	if again {
		out = append(out, "", "  "+muted.Render(strings.TrimSuffix(look.WelcomeAgain, "?"))+paper.Bold(true).Render("?"))
	}
	if !m.compact {
		out = append(out, "")
	}
	out = append(out, lookActionBar(look.WelcomeActions))
	for i, ln := range out {
		out[i] = truncVisible(ln, w)
	}
	return strings.Join(out, "\n") + "\n"
}

// wordWrap breaks plain text into lines of at most n cells at spaces (a word longer than n
// keeps its own line). Copy that must stay whole words - a disclosure, a sentence - uses it.
func wordWrap(s string, n int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= n:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
