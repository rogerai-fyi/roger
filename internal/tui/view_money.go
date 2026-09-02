// Package tui is the interactive `rogerai` experience - a two-way radio for Local Models,
// and the terminal twin of the website's "Live Operating Manual". Stations
// (providers) go on air; you tune in to a channel and talk. The look is the web's:
// ~95% monochrome + ONE red beacon, the shared instrument glyphs (◉ on air, ○ off
// air, ◆ verified, ▁▂▃▄▅▆▇█ signal bars), flat hairline structure, and a single
// carrier beat driving the beacon, the ((•)) spinner, and the signal-bar shimmer.
// Built on Bubble Tea + Lipgloss.
package tui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// GrantRow is a compact grant summary for the in-TUI /grant list.
type GrantRow struct {
	Name, Price, Status string
}

// Limit is the per-model spend ceiling (mirrors cmd/rogerai's config.Limit).
// Zero fields mean "no cap on that knob". Units match /discover.
type Limit struct {
	MaxIn  float64
	MaxOut float64
	MinTPS float64
	// Quants is the set of compression labels this band may be served at ("Q4_K_M",
	// "IQ4_XS"). Empty = any, which is the default and what most operators will want.
	//
	// It is the STANDING half of the quant choice (MODEL-VARIANTS-DESIGN-2026-08-22). The
	// dial's Q filter is a VIEW - it narrows what you are looking at and binds nothing -
	// while this is a RULE: it is enforced at routing like MinTPS, so it also governs the
	// agent, `roger use`, and every turn nobody is watching.
	Quants []string
}

// LimitStore is the TUI's view of the persisted spend limits: a per-model map, a
// Default for unpinned bands, the typical reply size for est-cost, and a Save
// callback so the host (cmd/rogerai) owns persistence. nil-safe: an empty store
// means no caps. Resolve picks per-model else Default.
//
// ONE STORE, TWO GOROUTINES. The TUI's update loop and the browser console's HTTP
// handlers write these caps through the SAME instance (that is the point - the two
// surfaces must agree on what the operator will pay). Without mu that is a concurrent
// map access on money settings, which for two writers is a hard Go crash, not a subtle
// drift. mu guards every read and write of Models; use Update for a read-modify-write
// that must be atomic against a concurrent edit. mu is the zero value, so the keyed
// struct literals that build a store elsewhere need no change.
type LimitStore struct {
	mu         sync.Mutex
	Models     map[string]Limit
	Default    Limit
	TypicalOut int
	Save       func(models map[string]Limit, def Limit) // persist (nil = no-op)
}

// payoutSnapshot is the TUI's compact view of `roger payout status` (enough for the
// earnings hint). kyc is the Connect status (none|onboarding|active|restricted).
type payoutSnapshot struct {
	loaded  bool
	kyc     string
	payable float64
	min     float64
}

// monthlyBudgetLine renders the per-account MONTHLY SPEND CAP (a budget limit) row
// shown atop the spend-limits editor: month-to-date spend vs the cap, with an ember
// "approaching"/"reached" tint near/at the cap. "no cap" when unset (the opt-in
// default). Edited from the CLI (`roger limit --monthly $X`), shown here.
func monthlyBudgetLine(m model) string {
	label := stDim.Render("    monthly budget   ")
	// The cursor reaches this row (up off the top of the band table), exactly as it
	// reaches every band row: the thing you are looking at is the thing you edit.
	if m.mode == modeLimits && m.limOnBudget {
		label = stSelBar.Render("  ▌ ") + stSelText.Render("monthly budget") + " "
	}
	// MID-EDIT: the row becomes the editor, same value-first fit discipline as the band
	// plate - the draft never drops, the keys go first.
	if m.mode == modeLimits && m.limEditBudget {
		line := label + stSelText.Render("["+m.editBuf+"]")
		if m.width == 0 || m.width >= 72 {
			line += stDim.Render("   enter save   esc cancel   (0/off = no cap)")
		}
		return line
	}
	if !m.loggedInState() {
		return label + stDim.Render("log in to set a monthly spend limit")
	}
	// The EDIT AFFORDANCE survives every width. The old tail (`set: roger limit
	// --monthly $X`) was dropped under 92 columns to stop the row wrapping - so on an
	// ordinary 80-column terminal the one account-wide money control showed "no cap"
	// with no way to discover how to set one (founder screenshot, 2026-09-01). A limit
	// you cannot find out how to set is a limit that does not exist. The short form
	// fits beside everything else at 80 columns; only the long CLI reminder stays
	// width-gated.
	hint := stDim.Render("   ·   ↑ edit")
	if m.mode == modeLimits && m.limOnBudget {
		hint = stDim.Render("   ·   enter edit")
	}
	if m.monthlyCap <= 0 {
		line := label + stLive.Render("no cap") + stDim.Render("   ·   used "+dollars(m.monthlySpend)+" this month") + hint
		if m.width == 0 || m.width >= 104 {
			line += stDim.Render("   ·   or: roger limit --monthly $X")
		}
		return line
	}
	used := dollars(m.monthlySpend) + stDim.Render(" of ") + stEmber.Render(dollars(m.monthlyCap))
	tail := ""
	fillStyle := stLive
	switch {
	case m.monthlySpend >= m.monthlyCap:
		tail = stEmber.Render("   ⚠ limit reached")
		fillStyle = stPingEye // a red bar at the hard limit - the one deliberate red: you are stopped
	case m.monthlySpend >= m.monthlyCap*0.80:
		tail = stEmber.Render(fmt.Sprintf("   ⚠ %.0f%% used", m.monthlySpend/m.monthlyCap*100))
	}
	// A determinate spend ÷ cap bar (a real fraction, unlike the in-turn sweep). Dropped
	// on narrow terminals so this single line never wraps.
	bar := ""
	if !m.narrow() {
		bar = "   " + tintBar(meterBar(m.monthlySpend/m.monthlyCap, budgetBarWidth), fillStyle)
	}
	line := label + used + stDim.Render(" this month") + bar + tail
	if m.narrow() {
		return line // the bar already dropped; the affordance is the row above's job at this width
	}
	return line + hint
}

// walletPanel groups the money-facing readout into ONE dedicated block on the spend-limits
// surface: the account/balance lockup, the running SESSION telemetry (↑in ↓out · $cost — the
// broker's BILLED re-count, via the shared meterTotals so it never drifts from the AGENT /
// CHANNEL live meters), and the determinate monthly-budget bar (monthlyBudgetLine, which owns
// the one-red-AT-the-cap discipline). Pure function of model state; reduced-motion/narrow safe
// (no animation; the budget bar already drops itself on a narrow terminal via monthlyBudgetLine).
func (m model) walletPanel() string {
	var b strings.Builder
	b.WriteString("    " + stBrand.Render("wallet") + "\n")
	// account + balance lockup (or the calm anonymous /login prompt; no balance when anon).
	b.WriteString("    " + m.accountTag(false) + "\n")
	// running SESSION telemetry — the COMBINED spend across BOTH money surfaces (AGENT + the
	// CHANNEL chat), via the shared sessionFooter so this panel never drifts from the live
	// meters. Omitted entirely while the session is still empty, so an untouched session shows
	// no stray "session" row.
	if f := sessionFooter(m.agentTokensIn+m.sessTokensIn, m.agentTokensOut+m.sessTokensOut, m.agentCost+m.sessCost); f != "" {
		b.WriteString("    " + f + "\n")
	}
	// the determinate monthly-budget bar (its own indentation + the one red AT the cap).
	b.WriteString(monthlyBudgetLine(m))
	return b.String()
}

// limitsView is the per-model spend-limits editor (3.4).
//
// Its output goes through clampLines: the screen is a TABLE plus prose, and a table is
// exactly the shape that quietly grows a column past the terminal. The targeted narrow
// forms below keep the clamp from biting; the clamp is the guarantee that it cannot run
// off the screen even when someone adds a column and forgets this file.
func (m model) limitsView(w int) string {
	return clampLines(m.limitsBody(w), w)
}

func (m model) limitsBody(w int) string {
	var b strings.Builder
	head := stBrand.Render("  spend limits") + stDim.Render("    what you are willing to pay, per band")
	if w < 60 {
		head = stBrand.Render("  spend limits")
	}
	b.WriteString("\n" + head + "\n\n")
	// The dedicated WALLET panel: balance + running session totals + the monthly-budget bar
	// (a per-account spend cap, enforced server-side at every paid path). The budget row is
	// EDITABLE here - up off the top of the table reaches it, enter edits - because this
	// screen is the spend-limits editor and the account-wide cap is a spend limit.
	b.WriteString(m.walletPanel() + "\n\n")
	// A DENSE TABLE on a slim terminal. The full grid is 76 cells and simply did not fit
	// a minimized or narrow window, which is where an operator most often IS when they
	// glance at their caps. What goes is "live now" and "status" - status is DERIVED from
	// the two caps beside it, and live-now is a market reading rather than a setting - so
	// the columns that remain are the ones this screen exists to edit.
	dense := w < 80
	if dense {
		b.WriteString(stDim.Render(fmt.Sprintf("    %-18s %-13s %s", "band", "max $/1M out", "min t/s")) + "\n")
	} else {
		b.WriteString(stDim.Render(fmt.Sprintf("    %-22s %-13s %-10s %-15s %s", "band", "max $/1M out", "min t/s", "live now", "status")) + "\n")
	}
	if len(m.limModels) == 0 {
		b.WriteString(stDim.Render("    (none yet - press a / set one in `roger config set-limit`)") + "\n")
	}
	// VIRTUALIZE like the dial: only the rows that fit the terminal render, the window
	// follows limCursor, and "more" hints say what scrolled off. The full table was 34
	// rows on a 24-row terminal - the same alt-buffer scroll that stacks the logos.
	// Chrome (wallet panel, headings, edit plate, keys, signpost, footer) measured at
	// 22 by the full-mode audit (the narrow reflow wraps one wallet line).
	limRows := len(m.limModels)
	if m.height > 0 {
		if room := m.height - 22; room > 0 && limRows > room {
			limRows = room
		} else if room <= 0 {
			limRows = 3
		}
	}
	limTop, limEnd := windowFor(0, m.limCursor, limRows, len(m.limModels))
	if limTop > 0 {
		b.WriteString("    " + stDim.Render(fmt.Sprintf("↑ %d more above", limTop)) + "\n")
	}
	for i := limTop; i < limEnd; i++ {
		mdl := m.limModels[i]
		cur := " "
		nameStyle := lipgloss.NewStyle().Foreground(cInk)
		if i == m.limCursor && !m.limOnBudget {
			cur = stSelBar.Render("▌")
			nameStyle = stSelText
		}
		lim := m.limits.resolve(mdl)
		maxOut := "-"
		if lim.MaxOut > 0 {
			maxOut = money(lim.MaxOut)
		}
		mtps := "-"
		if lim.MinTPS > 0 {
			mtps = fmt.Sprintf("%g", lim.MinTPS)
		}
		live, status := "-", stDim.Render("·")
		for _, bd := range m.bands {
			if bd.model == mdl && bd.online {
				live = rangeStr(bd)
				if lim.MaxOut > 0 && bd.minOut > lim.MaxOut {
					status = stEmber.Render(fmt.Sprintf("⚠ over by %.2f", bd.minOut-lim.MaxOut))
				} else {
					status = stLive.Render("✓ within")
				}
				break
			}
		}
		row := fmt.Sprintf("%s   %s %s %s %s %s",
			cur, nameStyle.Render(pad(mdl, 22)), stEmber.Render(pad(maxOut, 13)), stDim.Render(pad(mtps, 10)), stDim.Render(pad(live, 15)), status)
		if dense {
			row = fmt.Sprintf("%s   %s %s %s",
				cur, nameStyle.Render(pad(mdl, 18)), stEmber.Render(pad(maxOut, 13)), stDim.Render(mtps))
		}
		b.WriteString(truncVisible(row, w) + "\n")
	}
	if n := len(m.limModels) - limEnd; n > 0 {
		b.WriteString("    " + stDim.Render(fmt.Sprintf("↓ %d more below", n)) + "\n")
	}
	if m.editField >= 0 && m.limCursor < len(m.limModels) {
		field := "max $/1M out"
		if m.editField == 1 {
			field = "min t/s"
		}
		// THE EDIT PLATE. Bounded to the terminal: lipgloss draws a border at the
		// content's natural width, so a plate wider than the screen had its right edge
		// pushed off and the box read as broken open on one side (founder screenshot).
		//
		// The KEYS are what gets dropped when it does not fit, never the field being
		// edited or its value - an operator mid-edit needs to see what they are typing
		// into far more than they need to be re-told that esc cancels.
		// WIDTH-SAFE CONTENT. The box looked broken open on one side twice, and the cause
		// is not the border: it is that lipgloss measures ⏎ (U+23CE) and ▏ (U+258F) as one
		// cell while a terminal is free to render them as two - both are East-Asian-Width
		// AMBIGUOUS. The border characters are drawn at the width lipgloss computed, the
		// content row is drawn wider by the terminal, and the box no longer lines up.
		//
		// Inside a bordered box the fix is to stop using ambiguous glyphs at all. Outside
		// one they are harmless (nothing has to line up with them), which is why ⏎ still
		// rides the footers.
		lead := stDim.Render("edit " + m.limModels[m.limCursor] + "   " + field + "  ")
		short := stDim.Render(field + "  ")
		val := stSelText.Render("[" + m.editBuf + "]")
		// THE FIT LADDER, widest first. What gets dropped is always the least load-bearing
		// thing left: the keys, then the model name, then the field label. The VALUE never
		// goes - an operator mid-edit needs to see what they are typing far more than they
		// need to be re-told that esc cancels, and a clipped number is a number they cannot
		// trust. The last rung is the value alone, which fits any terminal worth drawing on.
		const editChrome = 6 // 2 indent + 2 border + 2 padding
		avail := max(4, w-editChrome)
		plate := val
		for _, cand := range []string{
			lead + val + stDim.Render("   enter save   tab next field   esc cancel"),
			lead + val + stDim.Render("   enter save   esc cancel"),
			lead + val,
			short + val,
		} {
			if lipgloss.Width(cand) <= avail {
				plate = cand
				break
			}
		}
		// AND IT MUST NOT WRAP. This is what actually made the box look broken: the plate
		// was one cell too wide for the content area, lipgloss WRAPPED it, and the box grew
		// a second row with "esc / cancel" split across the fold. MaxWidth does not prevent
		// that - it clips the already-wrapped block.
		//
		// The geometry, stated once: 2 indent + 2 border + 2 padding = 6 cells of overhead,
		// so the content area is w-6. Style.Width() sets the TOTAL width INCLUDING padding,
		// which is the off-by-two that let the wrap through - it is content+2, never content.
		plate = truncVisible(plate, avail)
		inner := lipgloss.Width(plate) + 2 // + the style's horizontal padding
		// INDENT EVERY LINE. "\n  " + a three-line render indents only the top border,
		// so the box sat two cells askew from its own content row - the misaligned
		// border the founder screenshotted (2026-09-02).
		box := stPanel.Width(inner).Render(plate)
		b.WriteString("\n  " + strings.ReplaceAll(box, "\n", "\n  ") + "\n")
	}
	keys := "↑↓ move   ⏎ edit   tab next field   d clear   esc done"
	if w < 60 {
		keys = "↑↓ · ⏎ edit · d clear · esc"
	}
	b.WriteString("\n    " + stDim.Render(keys) + "\n")
	// Cross-link the two split "config" surfaces: this screen is what you PAY as a
	// consumer; the provider PRICING editor (what you EARN, with time-of-use windows)
	// lives on a SHARE row. Signpost it so the operator isn't left hunting for it.
	signpost := stDim.Render("(this is what you PAY · to set what you EARN, go to ") + stKey.Render("[2] SHARE") +
		stDim.Render(" and press ") + stKey.Render("p") + stDim.Render(" on a row)")
	if w < 92 {
		signpost = stDim.Render("(what you PAY · what you EARN is ") + stKey.Render("[2] SHARE") + stDim.Render(" · p)")
	}
	b.WriteString("    " + truncVisible(signpost, w-4) + "\n")
	return b.String()
}

// payoutHint returns a compact, single-line cash-out hint for the SHARE / earnings
// surface, or "" when there is nothing to say (not logged in, snapshot not loaded, or
// nothing actionable). It is plain text under stDim/stEmber so it stays readable under
// NO_COLOR and narrow widths (the caller truncates to width). The two states that
// matter to a provider: KYC not done -> point at onboarding; payable at/above the
// minimum -> point at `roger payout` to withdraw.
func (m model) payoutHint() string {
	if !m.loggedInState() || !m.payout.loaded {
		return ""
	}
	min := m.payout.min
	if min == 0 {
		min = 25
	}
	switch {
	case m.payout.kyc != "active":
		// Earnings can accrue before KYC, so nudge onboarding once there's anything held
		// or payable; stay quiet for a brand-new owner with zero earnings.
		if m.payout.payable <= 0 {
			return ""
		}
		return stDim.Render("complete KYC to cash out: ") + stKey.Render("roger payout onboard")
	case m.payout.payable >= min:
		return stEmber.Render(dollars(m.payout.payable)) + stDim.Render(" payable - run ") + stKey.Render("roger payout") + stDim.Render(" to cash out")
	default:
		return ""
	}
}
