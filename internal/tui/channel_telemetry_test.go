package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
)

// TestSessionFooter pins the SHARED running-session footer used by BOTH the AGENT turn-final
// footer and the CHANNEL per-turn footer so the two money-facing surfaces never drift: a dim
// "session ↑in ↓out · $cost" built on meterTotals, and "" while the session is still empty (no
// stray row on a fresh surface).
func TestSessionFooter(t *testing.T) {
	if got := sessionFooter(0, 0, 0); got != "" {
		t.Errorf("an empty session must render no footer, got %q", got)
	}
	got := stripANSI(sessionFooter(1200, 3400, 0.05))
	for _, want := range []string{"session", "↑1.2k", "↓3.4k", "$0.05"} {
		if !strings.Contains(got, want) {
			t.Errorf("sessionFooter missing %q, got %q", want, got)
		}
	}
	// it carries the shared meterTotals form verbatim (never a second, drifting renderer).
	if !strings.Contains(got, stripANSI(meterTotals(1200, 3400, 0.05))) {
		t.Errorf("sessionFooter must wrap meterTotals verbatim, got %q", got)
	}
}

// TestChatSessionTokensAccumulate pins the channel's running session telemetry: each billed
// chatMsg ADDS its billed ↑in ↓out tokens AND cost to the running session totals (the same
// honest broker re-count the AGENT meter sums), a per-turn session footer rides into the
// transcript, and BOTH /clear and leaving the channel reset the running totals so a new
// conversation starts from zero.
func TestChatSessionTokensAccumulate(t *testing.T) {
	m := seedFor(120, modeChat, false)
	nm, _ := m.Update(chatMsg{reply: "hi", cost: 0.01, tokensIn: 100, tokensOut: 250, tps: 40})
	m = asModel(nm)
	nm, _ = m.Update(chatMsg{reply: "again", cost: 0.02, tokensIn: 50, tokensOut: 75, tps: 30})
	m = asModel(nm)
	if m.sessTokensIn != 150 || m.sessTokensOut != 325 {
		t.Errorf("accumulated channel tokens = ↑%d ↓%d, want ↑150 ↓325", m.sessTokensIn, m.sessTokensOut)
	}
	if d := m.sessCost - 0.03; d > 1e-9 || d < -1e-9 {
		t.Errorf("accumulated channel cost = %v, want 0.03", m.sessCost)
	}
	// a per-turn session footer (the running total after the turn) rode into the transcript.
	joined := stripANSI(strings.Join(m.transcript, "\n"))
	if !strings.Contains(joined, "session") || !strings.Contains(joined, "↑150") {
		t.Errorf("the transcript should carry the running session footer, got:\n%s", joined)
	}

	// /clear resets the running token totals (not just the cost).
	cl, _ := m.runSession("/clear")
	cm := asModel(cl)
	if cm.sessTokensIn != 0 || cm.sessTokensOut != 0 || cm.sessCost != 0 {
		t.Errorf("/clear must reset the running session totals, got ↑%d ↓%d $%v", cm.sessTokensIn, cm.sessTokensOut, cm.sessCost)
	}

	// leaving the channel (disconnect) also resets them - a new channel starts fresh.
	dm, _ := m.disconnect()
	dd := asModel(dm)
	if dd.sessTokensIn != 0 || dd.sessTokensOut != 0 || dd.sessCost != 0 {
		t.Errorf("disconnect must reset the running session totals, got ↑%d ↓%d $%v", dd.sessTokensIn, dd.sessTokensOut, dd.sessCost)
	}
}

// TestChatViewSessionReadout pins the channel's IN-FLIGHT running readout: while a reply is
// relaying AND the session already has billed turns, the channel surfaces the same meterTotals
// readout the AGENT shows during its turn (so a multi-turn channel reads its running ↑↓ + cost
// while it waits). A fresh session (nothing billed yet) shows none.
func TestChatViewSessionReadout(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.relaying = true

	// fresh: no billed turns yet -> no session readout under the transmit line.
	if got := stripANSI(m.chatView(120)); strings.Contains(got, "session ↑") {
		t.Errorf("a fresh relaying channel must not show a session readout, got:\n%s", got)
	}

	// after prior billed turns, the in-flight wait carries the running session readout.
	m.sessTokensIn, m.sessTokensOut, m.sessCost = 1200, 3400, 0.05
	got := stripANSI(m.chatView(120))
	for _, want := range []string{"session", "↑1.2k", "↓3.4k", "$0.05"} {
		if !strings.Contains(got, want) {
			t.Errorf("the in-flight channel should surface the running session readout (%q), got:\n%s", want, got)
		}
	}
}

// TestAgentSessionFooterShared pins that the AGENT turn-final footer uses the SAME shared
// sessionFooter as the CHANNEL: with billed tokens accrued, the final answer is followed by a
// "session ↑in ↓out · $cost" line, identical in form to the channel's — so the two money
// surfaces never drift.
func TestAgentSessionFooterShared(t *testing.T) {
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0")) // enter [0] AGENT (a fresh session zeroes the running totals)
	m := asModel(am)
	m.agentTokensIn, m.agentTokensOut, m.agentCost = 1200, 3400, 0.05 // accrued AFTER the entry reset
	nm, _ := m.Update(agentEventMsg{Kind: harness.EventFinal, Text: "done"})
	out := stripANSI(asModel(nm).View())
	for _, want := range []string{"session", "↑1.2k", "↓3.4k", "$0.05"} {
		if !strings.Contains(out, want) {
			t.Errorf("the AGENT turn-final footer should carry the shared session readout (%q):\n%s", want, out)
		}
	}
}
