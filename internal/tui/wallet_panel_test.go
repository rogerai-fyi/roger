package tui

import (
	"strings"
	"testing"
)

// TestWalletPanel pins the dedicated WALLET panel that groups the money-facing readout on
// the spend-limits surface into one block: the account/balance lockup, the running SESSION
// telemetry (↑in ↓out · $cost via the shared meterTotals), and the determinate monthly-budget
// bar (monthlyBudgetLine, which owns the one-red-at-the-cap discipline). Pure function of
// model state; reduced-motion/narrow safe.
func TestWalletPanel(t *testing.T) {
	// logged in, with an active AGENT session and a monthly cap.
	m := browseSeed(120) // logs in (balance 42.17), width 120 (not narrow)
	m.monthlyCap = 10.0
	m.monthlySpend = 5.0
	m.agentTokensIn = 1234
	m.agentTokensOut = 5678
	m.agentCost = 0.05
	full := stripANSI(m.walletPanel())

	// a single dedicated panel, labelled so it reads as "the wallet".
	if !strings.Contains(strings.ToLower(full), "wallet") {
		t.Errorf("the panel should carry a wallet label, got:\n%s", full)
	}
	// the balance lockup (browseSeed logs in with $42.17).
	if !strings.Contains(full, "42.17") {
		t.Errorf("a logged-in wallet panel should show the balance, got:\n%s", full)
	}
	// the running SESSION telemetry: ↑in ↓out · $cost (the shared meterTotals form).
	if !strings.Contains(full, "↑1.2k") || !strings.Contains(full, "↓5.7k") {
		t.Errorf("the wallet panel should surface the running session tokens, got:\n%s", full)
	}
	if !strings.Contains(full, "$0.05") {
		t.Errorf("the wallet panel should surface the running session cost, got:\n%s", full)
	}
	// the determinate monthly-budget bar (a real spend ÷ cap fraction).
	if !strings.ContainsAny(full, "▰▱") {
		t.Errorf("a logged-in capped wallet panel should show the budget bar, got:\n%s", full)
	}
	if !strings.Contains(full, "this month") {
		t.Errorf("the wallet panel should show the month-to-date budget line, got:\n%s", full)
	}

	// logged in but a FRESH session (no tokens, no cost): no stray "session" row.
	fresh := browseSeed(120)
	fresh.monthlyCap = 10.0
	if got := stripANSI(fresh.walletPanel()); strings.Contains(got, "session") {
		t.Errorf("a fresh session must not render a session row (no totals yet), got:\n%s", got)
	}

	// anonymous: the calm /login prompt, NO balance number, NO session row.
	anon := model{width: 120}
	ap := stripANSI(anon.walletPanel())
	if !strings.Contains(ap, "/login") {
		t.Errorf("an anonymous wallet panel should prompt /login, got:\n%s", ap)
	}
	if strings.Contains(ap, "42.17") || strings.Contains(ap, "session ↑") {
		t.Errorf("an anonymous wallet panel must not show a balance or session total, got:\n%s", ap)
	}

	// one-red discipline: at/over the hard cap the budget line says so; below cap it is calm.
	over := browseSeed(120)
	over.monthlyCap = 10.0
	over.monthlySpend = 12.0
	if !strings.Contains(stripANSI(over.walletPanel()), "limit reached") {
		t.Errorf("at the hard cap the wallet panel must surface the limit, got:\n%s", stripANSI(over.walletPanel()))
	}
	under := browseSeed(120)
	under.monthlyCap = 10.0
	under.monthlySpend = 1.0
	if strings.Contains(stripANSI(under.walletPanel()), "limit reached") {
		t.Errorf("well under the cap the wallet panel must stay calm, got:\n%s", stripANSI(under.walletPanel()))
	}

	// narrow-safe: the budget bar drops on a slim terminal (never wraps), but the account
	// lockup still renders — the panel degrades, it doesn't break.
	nar := browseSeed(120)
	nar.width = 50 // <= narrowCols (64)
	nar.monthlyCap = 10.0
	nar.monthlySpend = 5.0
	np := stripANSI(nar.walletPanel())
	if strings.ContainsAny(np, "▰▱") {
		t.Errorf("a narrow wallet panel must drop the determinate budget bar, got:\n%s", np)
	}
	if !strings.Contains(np, "42.17") {
		t.Errorf("a narrow wallet panel should still show the balance, got:\n%s", np)
	}
}
