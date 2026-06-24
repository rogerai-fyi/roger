package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// TestShareViewNarrowSafe: the k9s provider table must not overflow at narrow
// widths (it drops the metrics columns under 64 cols, like the band grid).
func TestShareViewNarrowSafe(t *testing.T) {
	for _, w := range []int{40, 50, 64, 80, 120} {
		mm := New("http://broker.local", "tester")
		mm.width, mm.height = w, 30
		mm.mode = modeShare
		mm.shareRows = []shareRow{
			{model: "gpt-oss-20b", ctx: 32768},
			{model: "qwen3-coder-30b-a3b-instruct", ctx: 32768},
		}
		var m tea.Model = mm
		m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
		m, _ = m.Update(tickMsg{})
		for _, line := range strings.Split(m.View(), "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: share view line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
}

// TestChatFailureIsInline is the regression guard for the founder's silent
// no-response: a failed chat turn must land IN the CHANNEL transcript (not just
// the footer), and an empty reply must show a clear note rather than a blank line.
func TestChatFailureIsInline(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "roggentoo", Model: "gpt-oss-20b", Online: true}
	mm.mode = modeChat
	var m tea.Model = mm

	// A failure surfaces inline in the transcript, red ✕, with the broker's reason.
	m, _ = m.Update(chatErrMsg("no node offers gpt-oss-20b"))
	if v := m.View(); !strings.Contains(v, "✕") || !strings.Contains(v, "no node offers gpt-oss-20b") {
		t.Errorf("chat failure not surfaced inline in the transcript:\n%s", v)
	}

	// An empty reply (no error) shows a clear "(no text)" note, never a blank arrow.
	m, _ = m.Update(chatMsg{reply: "   ", status: "roggentoo · $0"})
	if !strings.Contains(m.View(), "replied with no text") {
		t.Errorf("empty reply should show a note, not a blank line:\n%s", m.View())
	}

	// A real reply still renders.
	m, _ = m.Update(chatMsg{reply: "roger that", status: "roggentoo · $0"})
	if !strings.Contains(m.View(), "roger that") {
		t.Errorf("a real reply should render:\n%s", m.View())
	}
}

// TestChatPreflightNoStation: sending in CHANNEL when no station is on air for the
// band must report it inline immediately, not fire a doomed request silently.
func TestChatPreflightNoStation(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "roggentoo", Model: "gpt-oss-20b"}
	mm.mode = modeChat
	mm.chatIn.Focus()
	var m tea.Model = mm
	// type a turn + enter; bands is empty so bandOnAir is false -> inline notice.
	for _, r := range "hello" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := m.View(); !strings.Contains(v, "no station on air for gpt-oss-20b") {
		t.Errorf("pre-flight no-station notice missing:\n%s", v)
	}
}

// TestShareViewK9s: /share opens the provider table (no silent auto-share); it
// lists detected models with an OFF-AIR status + FREE price, a visible selection
// cursor (the `>` carat under NO_COLOR), and a contextual key footer.
func TestShareViewK9s(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeShare
	mm.shareRows = []shareRow{
		{model: "gpt-oss-20b", ctx: 32768},
		{model: "llama-3.3-70b", ctx: 32768},
	}
	mm.shareCursor = 1
	var m tea.Model = mm
	v := m.View()
	for _, want := range []string{"SHARE", "MODEL", "STATUS", "OFF-AIR", "FREE", "toggle"} {
		if !strings.Contains(v, want) {
			t.Errorf("share view missing %q:\n%s", want, v)
		}
	}
	// The selection carat marks the cursor row (row 1 = llama) under NO_COLOR.
	var caratLine string
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "llama-3.3-70b") {
			caratLine = line
		}
	}
	if !strings.Contains(stripANSI(caratLine), ">") {
		t.Errorf("selected row should carry the `>` selection carat: %q", stripANSI(caratLine))
	}
}

// TestBandHighlightCarat: the band browser selection is k9s-grade - the selected
// row carries the `>` carat (NO_COLOR fallback for the reverse-video bar).
func TestBandHighlightCarat(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "a", Model: "gpt-oss-20b", PriceOut: 0, Online: true, FreeNow: true},
		{NodeID: "b", Model: "llama-3.3-70b", PriceOut: 0.41, Online: true},
	})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown}) // cursor -> row 1
	var line string
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, "llama-3.3-70b") {
			line = l
		}
	}
	if !strings.Contains(stripANSI(line), ">") {
		t.Errorf("selected band row should carry the `>` carat: %q", stripANSI(line))
	}
}
