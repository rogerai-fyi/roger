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

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/glyphs"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/pricetier"
	"rogerai.fm/roger/v6/internal/protocol"
)

// SchedWindow is the TUI's editable view of a time-of-use price window (mirrors
// protocol.PriceWindow). Times are "HH:MM" UTC; Free zeroes the price in-window.
// SchedWindow and Pricing are aliases for the canonical types in internal/node, so
// the controller, the TUI editor, and the host config all speak one type. (Aliases,
// not new types, so existing Pricing{...}/SchedWindow{...} literals keep compiling.)
type SchedWindow = node.SchedWindow

// VoiceConfig is the per-model on-air voice identity (dj name / default voice / speed /
// language / sample clip URL) - the same alias idiom as Pricing, so the host config's
// share_voices block, Hooks.SavedVoices, and the controller all speak one type.
type VoiceConfig = node.VoiceConfig

// shareRow is one model in the k9s-style provider table: a locally-detected model
// plus its share status. Live metrics are read off the session when on air. Each
// row carries its OWN upstream (the detected server's chat URL) so a multi-endpoint
// box (e.g. :8060 gpt-oss-20b + :8080 gpt-oss-120b + :8081 qwen3-vl-8b) shares each
// model against the server that actually serves it - not a single shared upstream.
type shareRow struct {
	model        string
	modality     string // "" / chat | tts | stt — carried onto the offer so a voice shares as a voice
	ctx          int
	ctxEstimated bool   // ctx is the estimated default (no real window detected), not measured
	upstream     string // the normalized chat-completions URL backing THIS row's model
	upstreamKey  string // bearer key THIS row's key-protected upstream needs (env/paste), if any
	// quant / weights / variant are what detection read off THIS machine's runtime and
	// model file. They ride onto the offer, and the band card shows them back so the
	// operator can see what the market will see. Detected only — empty means the runtime
	// and the file said nothing, which is common and renders as absent, never as a guess.
	quant   string
	weights string
	variant string
}

// schedToProtocol converts the TUI's editable windows into the wire
// protocol.PriceWindow the agent publishes (times "HH:MM" UTC; Free zeroes the
// in-window price). Empty in -> no schedule.
func schedToProtocol(ws []SchedWindow) []protocol.PriceWindow { return node.SchedToProtocol(ws) }

// presetKey is one button on the always-visible preset-station bar: a radio
// preset that lights up when its mode is active and jumps to it when pressed.
type presetKey struct {
	key, label string
	active     bool
}

// presetButtons returns the preset bank for the current mode, with exactly one
// preset lit (the section/screen the user is in). TUNE IN covers browse/command/
// chat/connect; SHARE covers the provider table / editor / setup; CONFIG maps to
// the limits screen (the in-TUI config surface). LOGIN + HELP are always-available
// actions (lit only while their screen shows).
func (m model) presetButtons() []presetKey {
	tuneActive := !m.inShareSection() && m.mode != modeLimits && m.mode != modeHelp && m.mode != modeAgent && m.mode != modeLogin
	// [L] flips its label by state: LOGOUT when an account is linked, LOGIN otherwise.
	// It is a resting-capable mode now (the confirmable panel), so it lights while open.
	loginLabel := "LOGIN"
	if m.loggedInState() {
		loginLabel = "LOGOUT"
	}
	return []presetKey{
		{"0", "AGENT", m.mode == modeAgent},
		{"1", "TUNE IN", tuneActive},
		{"2", "SHARE", m.inShareSection()},
		{"3", "CONFIG", m.mode == modeLimits},
		{"L", loginLabel, m.mode == modeLogin},
		{"?", "HELP", m.mode == modeHelp},
	}
}

// presetBar renders the always-visible "preset bank" of radio-station buttons:
// [1] TUNE IN  [2] SHARE  [3] CONFIG  [L] LOGIN  [?] HELP, with the CURRENT mode
// lit like a pressed preset. It replaces the buried single "s share" hint and makes
// the two modes unmistakable. Compact + NO_COLOR-safe: under a narrow width it drops
// to just key glyphs ([1][2][3][L][?]) so it never overflows.
func (m model) presetBar(w int) string {
	btns := m.presetButtons()
	narrow := m.narrow()
	parts := make([]string, 0, len(btns))
	for _, b := range btns {
		var cell string
		if narrow {
			// Narrow: just the key, lit preset reverse-video (or `>key` under NO_COLOR).
			if b.active {
				cell = stPresetOn.Render(" " + b.key + " ")
			} else {
				cell = stPreset.Render("[" + b.key + "]")
			}
		} else {
			label := "[" + b.key + "] " + b.label
			if b.active {
				// A leading dot survives NO_COLOR (where the bg glint is stripped) so the
				// lit preset reads as pressed even with no color.
				cell = stPresetOn.Render(" •" + label + " ")
			} else {
				cell = stPreset.Render(" " + label + " ")
			}
		}
		parts = append(parts, cell)
	}
	bar := strings.Join(parts, stPreset.Render(" "))
	// CLIPPED to the terminal. Below ~80 columns the full bank is wider than the screen
	// and wrapped, which cost a row and - now that the frame is painted on a deck ground
	// - broke the ground's rectangle where it spilled. Every other row on this screen
	// already clips; this one was the exception.
	return truncVisible("  "+bar, w)
}

func (m model) presetForKey(key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "right":
		// Sequential tab navigation across the preset bank: step to the NEXT preset
		// (0 -> 1 -> 2 -> 3 -> L -> ? -> wrap to 0) and fire its jump, so left/right
		// behave exactly like pressing the number/letter. presetForKey is only ever
		// consulted from non-text-entry contexts (browse / a SHARE sub-screen not pasting
		// / limits-not-editing / help), so left/right inherit that exact guard and never
		// steal a cursor move in the schedule editor's window sub-fields, the command
		// palette, chat, the AGENT prompt, the `f` filter, or a numeric field.
		return m.cyclePreset(+1)
	case "left":
		// Previous preset (wraps the other way: 0 -> ? -> L -> 3 -> 2 -> 1 -> 0).
		return m.cyclePreset(-1)
	case "m":
		// COMPACT (the "windowshade"): toggle the calm, dense, animation-free view. Lives
		// alongside the preset jumps so it works in every non-text-entry context (browse /
		// the SHARE table / limits-not-editing / help) and is NEVER stolen while typing in
		// chat, the command palette, or a numeric price/limit/schedule editor (those modes
		// own their keys and don't consult presetForKey). Persisted via SaveCompact so the
		// choice sticks across launches (nil = session-only).
		return m.toggleCompact(), nil, true
	case "0":
		// AGENT: open the embedded tool-capable harness (dj.md persona). It runs on the
		// open channel's model, else the last band tuned in this session; /model switches.
		nm, cmd := m.enterAgent()
		return nm, cmd, true
	case "1":
		// TUNE IN: leave any SHARE/limits screen, back to the band browser. A live
		// channel stays open (tab/c returns to it).
		if m.inShareSection() || m.mode == modeLimits {
			m.mode = modeBrowse
			m.status = stDim.Render("TUNE IN - browse the band, enter to tune in")
		}
		return m, nil, true
	case "2":
		// SHARE: open the provider table (or the guided fallback). doShare returns the
		// (model, cmd) so we surface it as-is.
		nm, cmd := m.doShare(nil)
		return nm, cmd, true
	case "3":
		// CONFIG: the in-TUI per-model spend-limits screen.
		m.enterLimits()
		return m, nil, true
	case "l", "L":
		nm, cmd := m.doLogin()
		return nm, cmd, true
	case "?":
		m.mode = modeHelp
		m.helpVP.GotoTop()
		return m, nil, true
	}
	return m, nil, false
}

// priceTierCell renders the $-tier as a row suffix: the $-glyphs in the price style plus
// (tier 1 only) a subtle "good price" chip. Monochrome by design - the chip carries the
// favorable signal as TEXT, not hue. Returns "" for FREE / unknown (the caller already
// shows the FREE tag or the raw price). The tier->glyph render is the shared canonical one
// (internal/pricetier), so the TUI reads identically to the CLI + web surfaces.
func priceTierCell(tier int, priceOut float64) string {
	bars, chip := pricetier.Render(tier, priceOut)
	if bars == "" || bars == "FREE" {
		return ""
	}
	out := stEmber.Render(bars)
	if chip != "" {
		out += stLive.Render(" " + chip)
	}
	return out
}

// priceTierSuffix is the leading-space " $$ [good price]" suffix appended after a price;
// empty when there is no $-tier to show (FREE / unknown).
func priceTierSuffix(tier int, priceOut float64) string {
	if cell := priceTierCell(tier, priceOut); cell != "" {
		return " " + cell
	}
	return ""
}

// priceInOut renders a band's headline price as "$in·$out" - the cheapest active
// input price and cheapest active output price - exactly mirroring the web /models
// row (fmtPrice(priceIn) · fmtPrice(priceOut)). Honest-empty: an offline band shows
// a bare "-", and a fully free band (both 0) reads "free" rather than "$0.00·$0.00".
// This is the band-LIST twin of the web's in·out split; the [i] station log keeps the
// per-station in·out detail.
func priceInOut(b band) string {
	if !b.online {
		return "-"
	}
	if b.minIn == 0 && b.minOut == 0 {
		return "free"
	}
	return money(b.minIn) + "·" + money(b.minOut)
}

// priceInOutTier is priceInOut plus the compact $-tier tag when it fits the price column,
// so the wide band table reads "0.20·0.30 $$" - the actual price AND its cheap/fair/dear
// level at a glance - WITHOUT breaking the fixed-width grid. The tag is dropped if it would
// overflow colW (a pricey band already reads expensive on its number), and pad() does the
// final clamp. colW is measured in runes (the "·" is one column).
func priceInOutTier(b band, colW int) string {
	s := priceInOut(b)
	if tag := bandTierTag(b); tag != "" && len([]rune(s))+1+len(tag) <= colW {
		s += " " + tag
	}
	return s
}

// sharesOnAir counts how many local models are currently on air.
func (m model) sharesOnAir() int { return m.ctrl.OnAirCount() }

// sharePrice returns the price a row WOULD share at (its saved/edited price, FREE
// by default), or the live session's price when it's on air.
func (m model) sharePrice(row shareRow, live *agent.Session) (in, out float64) {
	if live != nil {
		return live.Price()
	}
	p := m.pricingFor(row.model)
	return p.In, p.Out
}

// shareView is the k9s-style provider table: one row per locally-detected model
// with an unmistakable reverse-video selection cursor, a clear ON-AIR / OFF-AIR
// status column, the price (FREE or $/1M out), and the live earning metrics
// (requests served, out tokens, earnings $) for any model that is on air. The
// founder can glance and instantly see what is shared vs not, and flip any model
// on/off air with one key. This replaces the old silent auto-share.
//
// k9s patterns applied (cited for the local design record): a highly visible
// cursor row (k9s flips the selected row to its accent background; we use the
// brand-volt reverse-video bar, with a `>` carat under NO_COLOR), status columns
// per resource, and a contextual key footer - k9scli.io + github.com/derailed/k9s.
func (m model) shareView(w int) string {
	var b strings.Builder
	// dense drops the metrics columns (SERVED/OUT TOK/EARNINGS): the full grid is
	// ~88 cols, so anything narrower uses the 3-column model·status·price layout to
	// stay width-safe (the band grid uses the same idea at its own threshold). The
	// windowshade compact mode forces the dense layout regardless of width.
	dense := w < 88 || m.compact
	head := stSelBar.Render("▌") + " " + stBrand.Render("SHARE")
	// Slot meter: ON AIR n/max (the soft share.max_on_air cap). At the cap the count
	// reads in the ember accent so the operator sees there are no free slots; below it,
	// dim. NO_COLOR-safe (the n/max text carries the meaning, color is only emphasis).
	on, max := m.sharesOnAir(), m.maxOnAir()
	slot := fmt.Sprintf("ON AIR %d/%d", on, max)
	slotCell := stDim.Render(slot)
	if on >= max {
		slotCell = stEmber.Render(slot)
	}
	if dense {
		b.WriteString("  " + head + "   " + slotCell + "\n")
	} else {
		b.WriteString("  " + head +
			stDim.Render(fmt.Sprintf("   your GPU as a station   %s detected   ", plural(len(m.shareRows), "model"))) +
			slotCell + "\n")
	}

	// Station line: the friendly broadcast callsign every band's node id carries into
	// /discover (the owner sees THEIR name, never the hostname). While renaming, it shows
	// the live edit buffer + a cursor; otherwise the current station + the `n` rename
	// affordance. Width/NO_COLOR-safe (plain text carries it).
	if m.renaming {
		ln := "  " + stDim.Render("station ") + stSelText.Render(m.stationEdit+"_") +
			stDim.Render("  ") + stKey.Render("enter") + stDim.Render(" save · ") + stKey.Render("esc") + stDim.Render(" cancel")
		b.WriteString(truncVisible(ln, w-2) + "\n")
	} else {
		ln := "  " + stDim.Render("station ") + stKey.Render(m.station) +
			stDim.Render(" · ") + stKey.Render("n") + stDim.Render(" rename")
		b.WriteString(truncVisible(ln, w-2) + "\n")
	}

	// LOADING: detection runs off the event loop, so while it's in flight we show a
	// clear indicator instead of a frozen UI. The ((•)) working spinner pulses with the
	// tick; quiet (NO_COLOR / non-TTY) and compact (windowshade) both freeze it to a
	// static (•) glyph + phrase via transmitLineFor.
	if m.shareLoading {
		spin := m.transmitLineFor(0)
		return b.String() + "\n  " + spin + "\n  " +
			stDim.Render("scanning the band for local models…") + "\n"
	}

	if len(m.shareRows) == 0 {
		return b.String() + "\n  " + stEmber.Render("no local models detected") +
			stDim.Render(" - start a local LLM and press r to re-detect") + "\n"
	}

	// Column geometry. dense drops the metrics columns so nothing overflows.
	nameW := 24
	if dense {
		nameW = 14
	}
	// Header (k9s-style ALL-CAPS column labels). Windowshade compact omits the header
	// row entirely for density (the cells stay self-evident).
	switch {
	case m.compact:
		// no column-header row
	case dense:
		b.WriteString("  " + stDim.Render(fmt.Sprintf("  %-14s  %-8s  %s", "MODEL", "STATUS", "PRICE")) + "\n")
	default:
		b.WriteString("  " + stDim.Render(fmt.Sprintf("  %-24s  %-9s  %-12s  %-9s  %-10s  %s",
			"MODEL", "STATUS", "PRICE", "SERVED", "OUT TOK", "AT LIST")) + "\n")
	}

	// WHY "AT LIST" AND NOT "EARNINGS".
	//
	// Session.Earnings() is a NODE-LOCAL tally: the node prices the work it just did with
	// its own price card and adds the owner share. It is not the ledger, and it cannot be -
	// the node does not learn what the broker decided to charge.
	//
	// The gap is not hypothetical. Consuming your OWN node is $0 by design ("signed
	// self-use: consuming your OWN node is $0, automatically (metering only)"), so a rig
	// serving its owner's traffic accrues a number here while the broker mints nothing. On
	// the founder's machine this read $0.27 against a ledger of $0.00 payable, $0.00 held.
	//
	// Both numbers were right; only the WORD was wrong. This column is what the served work
	// is worth at list price. Money lives on the broker, and `roger payout` is what reads it.

	// Table width for the reverse-video bar (the highlight spans the whole row).
	tableW := w - 4
	if tableW < 20 {
		tableW = 20
	}

	for i, row := range m.shareRows {
		sel := i == m.shareCursor
		live := m.shares[row.model]
		on := live != nil
		// The dispatch pilot lamp for this model (● on air / ◐ warming / ○ idle).
		link := agent.LinkConnecting
		if live != nil {
			link = live.Link()
		}
		lampG, lampS := pilotLamp(on, link)
		// Status cell text (plain, so the reverse-video bar governs a selected row). A
		// row on a private (hidden) band reads PRIVATE instead of ON-AIR so the operator
		// sees at a glance which models are freq-code-only.
		statusTxt := "OFF-AIR"
		if on {
			statusTxt = "ON-AIR"
			if m.sharePrivate[row.model] {
				statusTxt = "PRIVATE"
			}
		}
		in, out := m.sharePrice(row, live)
		priceTxt := sharePriceText(in, out)
		// A time-of-use schedule is flagged with a clock so the table shows it at a
		// glance (the per-window detail lives in the editor).
		if !on && m.hasSchedule(row) {
			priceTxt += " ~tou"
		}
		// VOICE rows read model-first with a tiny mono modality tag (♪ tts / ▽ stt, fold-safe) so the
		// operator sees which rows are voices without a separate section (founder DELTA §D2). A tts
		// row's price is in its REAL unit ($/1k chars); until a voice is picked it prompts "set
		// voice…" (you can't go on air as a nameless default). An stt row can go straight on air.
		modelCell := row.model
		if tag := shareModalityTag(row.modality); tag != "" {
			modelCell = row.model + "  " + tag
		}
		if row.modality == "tts" {
			vc := m.ctrl.VoiceConfigFor(row.model)
			if vc.Voice == "" {
				priceTxt = "set voice…"
			} else if in > 0 {
				priceTxt = dollars(in/1000) + "/1k ch"
			} else {
				priceTxt = "FREE"
			}
		} else if row.modality == "stt" {
			priceTxt = "FREE ~bytes"
			if in > 0 {
				priceTxt = dollars(in) + "/1M B"
			}
		}

		// Build the row body as PLAIN text first (cells padded), then color it: a
		// selected row is one reverse-video bar; an unselected row tints the status
		// + price cells. This keeps the k9s "the cursor row is obvious" contract.
		var plain string
		if dense {
			plain = fmt.Sprintf("%s %-14s  %-8s  %s", lampG, pad(modelCell, 14), statusTxt, priceTxt)
		} else {
			served, outTok, earn := "-", "-", "-"
			if on {
				reqs, toks := live.Served()
				served = fmt.Sprintf("%d", reqs)
				outTok = fmt.Sprintf("%d", toks)
				earn = dollars(live.Earnings())
			}
			plain = fmt.Sprintf("%s %-24s  %-9s  %-12s  %-9s  %-10s  %s",
				lampG, pad(modelCell, nameW), statusTxt, priceTxt, served, outTok, earn)
		}

		if sel {
			// Reverse-video accent bar across the whole row - unmistakable cursor.
			b.WriteString(selCarat(true) + rowSel(true, plain, tableW) + "\n")
			continue
		}
		// Unselected: a dot/blank gutter, dim model, colored status + price cells.
		st := stDim.Render(pad(statusTxt, 9))
		if on {
			st = stRed.Render(pad(glyphOnAir+" "+statusTxt, 9))
		}
		if dense {
			stN := stDim.Render(pad(statusTxt, 8))
			if on {
				stN = stRed.Render(pad(glyphOnAir+statusTxt, 8))
			}
			b.WriteString(selCarat(false) + lampS.Render(lampG) + " " + stDim.Render(pad(modelCell, 14)) + "  " + stN + "  " + sharePriceCell(priceTxt) + "\n")
			continue
		}
		served, outTok, earn := stDim.Render(pad("-", 9)), stDim.Render(pad("-", 10)), stDim.Render("-")
		if on {
			reqs, toks := live.Served()
			served = stLive.Render(pad(fmt.Sprintf("%d", reqs), 9))
			outTok = stDim.Render(pad(fmt.Sprintf("%d", toks), 10))
			earn = stEmber.Render(dollars(live.Earnings()))
		}
		b.WriteString(selCarat(false) + lampS.Render(lampG) + " " + stDim.Render(pad(modelCell, nameW)) + "  " + st + "  " +
			sharePriceCell(pad(priceTxt, 12)) + "  " + served + "  " + outTok + "  " + earn + "\n")
	}

	// DETAIL BANNER: a full-width contextual line for the SELECTED row (only when
	// there ARE rows and the cursor is on one), so a terse cell like "set voice…" reads
	// as its full state + next action. A ▌-barred, dim line matching the SHARE chrome; it
	// marquee-scrolls only if the detail overflows the available width (static otherwise),
	// driven by the SAME frame counter as the signal bars (sigFrame — frozen when compact).
	if len(m.shareRows) > 0 && m.shareCursor >= 0 && m.shareCursor < len(m.shareRows) {
		row := m.shareRows[m.shareCursor]
		detail := m.shareRowDetail(row, m.shares[row.model])
		// The bar + a leading space cost 2 cols; the 2-col left margin costs 2 more.
		avail := w - 4
		if avail < 8 {
			avail = 8
		}
		detail = marquee(glyphs.Fold(detail), avail, m.sigFrame())
		b.WriteString("\n  " + stSelBar.Render("▌") + stDim.Render(" "+detail) + "\n")
	}

	// Pricing affordance: logged in -> the per-model editor; anonymous -> the clear
	// "log in to earn" gate (free sharing still works without an account).
	if dense {
		ph := stKey.Render("p") + stDim.Render(" price")
		if !m.loggedInState() {
			ph = stDim.Render("log in to earn")
		}
		// Dense (narrow) footer keeps it short; the `n rename` affordance already rides on
		// the station line above, so it is omitted here to stay within 40 cols.
		b.WriteString("\n  " + stDim.Render("free · ") + stKey.Render("⏎") + stDim.Render("/") + stKey.Render("a") + stDim.Render(" toggle · ") + stKey.Render("h") + stDim.Render(" hide · ") + ph + "\n")
	} else {
		ph := stKey.Render("p") + stDim.Render(" set price + schedule")
		if !m.loggedInState() {
			ph = stDim.Render("log in to earn (") + stKey.Render("/login") + stDim.Render(")")
		}
		b.WriteString("\n  " + stDim.Render("free by default · ") +
			stKey.Render("enter") + stDim.Render("/") + stKey.Render("a") + stDim.Render(" toggles on/off air · ") +
			stKey.Render("h") + stDim.Render(" hide on a private band · ") +
			stKey.Render("n") + stDim.Render(" rename station · ") + ph + "\n")
	}
	// What AT LIST means, said once and near the number rather than left to be inferred.
	// It shows only when the column is populated - a rig with nothing on air does not need
	// a lesson about settlement.
	if m.anyLiveShare() {
		note := stDim.Render("AT LIST is this work priced at your card - not settled money. " +
			"Serving your OWN traffic is $0. Real earnings: ") + stKey.Render("roger payout")
		b.WriteString("  " + truncVisible(note, w-4) + "\n")
	}
	// Cash-out hint for an earning provider (KYC / payable), under the affordance line.
	// Width-safe + NO_COLOR-safe; empty when there's nothing actionable.
	if hint := m.payoutHint(); hint != "" {
		b.WriteString("  " + truncVisible(hint, w-4) + "\n")
	}
	return b.String()
}

// sharePriceText renders a chat row's price cell showing BOTH BILLED AXES.
//
// Cost() is (prompt x PriceIn + completion x PriceOut) / 1e6, so both terms are real
// money. This cell used to print only the output price, and on a row priced 0.20 in /
// 0.01 out that hid the term producing almost the entire bill: the operator read
// "$0.01/1M out" beside a figure two columns over and had no way to reconcile them,
// because the number driving it was not on screen.
//
// An unpriced input axis is still omitted - there is nothing to say about an axis that
// bills nothing, and saying "$0.00 in" would read as a rate rather than an absence.
func sharePriceText(in, out float64) string {
	switch {
	case in <= 0 && out <= 0:
		return "FREE"
	case in > 0:
		return dollars(in) + " in · " + dollars(out) + " out"
	default:
		return dollars(out) + "/1M out"
	}
}

// anyLiveShare reports whether any row is actually on air, so the settlement note is
// shown beside a populated AT LIST column rather than an empty one.
func (m model) anyLiveShare() bool {
	for _, r := range m.shareRows {
		if m.shares[r.model] != nil {
			return true
		}
	}
	return false
}

// shareModalityTag is the tiny mono modality tag for a SHARE voice row (♪ tts / ▽ stt, fold-safe:
// ♪→>, ▽→v). Empty for a chat/back-compat row. It routes the glyph through the SINGLE
// voiceBadgeForModality source so the SHARE table + the consumer Booth share ONE ♪/▽ definition and
// the ASCII-fold house rule.
func shareModalityTag(modality string) string {
	badge := voiceBadgeForModality(modality)
	if badge == "" {
		return ""
	}
	return glyphs.Fold(badge) + " " + modality
}

// sharePriceCell tints a price cell: FREE live-green, a priced cell ember.
func sharePriceCell(txt string) string {
	if strings.HasPrefix(strings.TrimSpace(txt), "FREE") {
		return stLive.Render(txt)
	}
	return stEmber.Render(txt)
}

// shareRowDetail is the PLAIN full-detail line the SHARE view's DETAIL BANNER renders
// for the selected row: it spells out the row's full state + the next action, so a terse
// table cell (e.g. "set voice…") becomes readable. It is model-first (LLM-first framing),
// uses the SAME real row / live-session / VoiceConfig data + helpers the table cells use
// (sharePrice, VoiceConfigFor, Served/Earnings, dollars, fmtCtx), leads with the shared
// fold-safe glyphs (♪ tts · ▽ stt · ◉ on air), and returns NO ANSI — the banner applies the
// chrome. The caller folds the whole line for ASCII terminals.
//
//   - tts, no voice → prompts the VOICE BOOTH (p), then enter to go on air
//   - tts, configured, off air → dj-name (or model) · voice · price/FREE — enter · p to edit
//   - tts, on air → ◉ on air · name · voice · N served · earn
//   - stt, off air → transcriber, metered per uploaded byte — enter · p to price
//   - stt, on air → ◉ on air · N served · earn
//   - chat, off air → model · ctx — enter to go on air free · p to set a price + schedule
//   - chat, on air → ◉ on air · N served · out tok · earn
//
// A row on a hidden (private) band appends a code-only note so the banner never implies
// it's on the open market.
func (m model) shareRowDetail(row shareRow, live *agent.Session) string {
	on := live != nil
	in, _ := m.sharePrice(row, live)

	// on-air served/earn suffix shared by every modality.
	onAirTail := func(withTok bool) string {
		reqs, toks := live.Served()
		s := fmt.Sprintf("%s on air · %d served", glyphOnAir, reqs)
		if withTok {
			s += fmt.Sprintf(" · %d tok", toks)
		}
		return s + " · " + dollars(live.Earnings())
	}

	var detail string
	switch row.modality {
	case "tts":
		switch {
		case on:
			vc := m.ctrl.VoiceConfigFor(row.model)
			name := vc.Name
			if name == "" {
				name = row.model
			}
			reqs, _ := live.Served()
			detail = fmt.Sprintf("%s on air · %s · %s · %d served · %s",
				glyphOnAir, name, vc.Voice, reqs, dollars(live.Earnings()))
		default:
			// A voice is READY only with BOTH a DJ name AND a picked voice (the broker 400s a
			// nameless offer), so an unnamed/voiceless row prompts the VOICE BOOTH (press p),
			// matching the on-air toggle guard — never an "enter to go on air" it can't honor.
			vc := m.ctrl.VoiceConfigFor(row.model)
			if vc.Name == "" || vc.Voice == "" {
				detail = "♪ " + row.model + " needs a name + voice — press p to set it in the VOICE BOOTH (voice · blend · speed · price), then enter to go on air"
			} else {
				price := "FREE"
				if in > 0 {
					price = dollars(in/1000) + "/1k ch"
				}
				detail = fmt.Sprintf("♪ %s · %s · %s — enter to go on air · p to edit", vc.Name, vc.Voice, price)
			}
		}
	case "stt":
		if on {
			detail = onAirTail(false)
		} else {
			detail = "▽ " + row.model + " transcriber — enter to go on air (metered per uploaded byte) · p to price"
		}
	default: // chat (default/empty modality)
		if on {
			detail = onAirTail(true)
		} else {
			detail = fmt.Sprintf("%s · %s ctx — enter to go on air free · p to set a price + schedule", row.model, fmtCtx(row.ctx))
		}
	}

	// A hidden (private-band) row is code-only: say so, so the banner never reads as open-market.
	if on && m.sharePrivate[row.model] {
		detail += " · hidden on a private band (code-only)"
	}
	return detail
}

// shareEditorView is the per-model price + time-of-use schedule editor (the
// ChargePoint-style earning surface), reached with `p` from the provider table and
// login-gated (enterShareEditor flashes the /login prompt for anonymous users, so
// this view only renders for a logged-in owner). It carries the same designed
// look as the share table: a section tab heading, a focused-field cursor, and a
// contextual key footer.
func (m model) shareEditorView(w int) string {
	var b strings.Builder
	narrow := m.narrow()
	headTail := stDim.Render("   what you earn per 1M tokens")
	if narrow {
		headTail = ""
	}
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("PRICE + SCHEDULE") +
		stDim.Render("   ") + stKey.Render(m.edModel) + headTail + "\n\n")

	field := func(idx int, label, val, unit string) string {
		cur := "  "
		nameSt := stDim
		valSt := stEmber
		if m.edField == idx {
			cur = stSelText.Render("▌ ")
			nameSt = stSelText
		}
		shown := val
		if shown == "" {
			shown = "0"
		}
		box := "▏" + shown + "▏"
		if m.edField == idx {
			box = stSelText.Render("▏" + shown + "▏")
		} else {
			box = valSt.Render(box)
		}
		tail := stDim.Render("  " + unit)
		if narrow {
			tail = ""
		}
		return cur + nameSt.Render(pad(label, 16)) + box + tail + "\n"
	}
	b.WriteString(field(edFieldIn, "$/1M input", m.edPriceIn, "$ per 1,000,000 input tokens"))
	b.WriteString(field(edFieldOut, "$/1M output", m.edPriceOut, "$ per 1M output  (the headline price)"))

	// The add-window affordance.
	addCur := "  "
	addSt := stDim
	if m.edField == edFieldAddWin {
		addCur = stSelText.Render("▌ ")
		addSt = stSelText
	}
	winTail := stDim.Render("   ") + stKey.Render("a") + stDim.Render(" add a window · ChargePoint-style")
	if narrow {
		winTail = stDim.Render(" · ") + stKey.Render("a") + stDim.Render(" add")
	}
	b.WriteString("\n" + addCur + addSt.Render("time-of-use windows") + winTail + "\n")

	if len(m.edWindows) == 0 {
		empty := stDim.Render("    (none - flat price all day · ") + stKey.Render("a") + stDim.Render(" adds a peak)")
		if narrow {
			empty = stDim.Render("    (none · ") + stKey.Render("a") + stDim.Render(" adds one)")
		}
		b.WriteString(empty + "\n")
	}
	for i, win := range m.edWindows {
		idx := edFieldFirstWin + i
		focused := m.edField == idx
		cur := "    "
		nameSt := stDim
		if focused {
			cur = "  " + stSelText.Render("▌ ")
			nameSt = stSelText
		}
		// Each sub-field renders its value; the focused one (in the focused row) is
		// highlighted (reverse-video, no literal brackets) so the user sees which
		// Start/End/In/Out they're editing without changing the layout width.
		sub := func(s int, v string) string {
			if focused && m.edWinSub == s {
				return stSelText.Render(v)
			}
			return nameSt.Render(v)
		}
		hours := sub(winSubStart, win.Start) + nameSt.Render("-") + sub(winSubEnd, win.End)
		// Pad to the visible (ANSI-stripped) width of the hours label so the price
		// column lines up regardless of the focus highlight.
		plainHours := win.Start + "-" + win.End + " UTC"
		if vis := len([]rune(plainHours)); vis < 18 {
			hours += nameSt.Render(" UTC" + strings.Repeat(" ", 18-vis))
		} else {
			hours += nameSt.Render(" UTC ")
		}
		var price string
		if win.Free {
			price = stLive.Render("FREE")
		} else {
			outVal, inVal := dollars(win.Out), dollars(win.In)
			// While editing a price sub-field, show the raw in-progress buffer (so a
			// half-typed "0." is visible, not the rounded float).
			if focused && m.edWinSub == winSubOut {
				outVal = "$" + m.edWinBuf
			}
			if focused && m.edWinSub == winSubIn {
				inVal = "$" + m.edWinBuf
			}
			price = stEmber.Render(sub(winSubOut, outVal) + "/1M out")
			if !narrow {
				price += stDim.Render(" · ") + stEmber.Render(sub(winSubIn, inVal)+"/1M in")
			}
		}
		b.WriteString(cur + hours + price + "\n")
	}

	if !narrow {
		b.WriteString("\n  " + stDim.Render("a window's price applies in its hours; the base price applies outside them.") + "\n")
	}

	// Live preview: what this schedule charges RIGHT NOW, computed from the same
	// ActivePrice the broker uses, so the operator sees the schedule's effect at a
	// glance (e.g. a FREE 03:00-03:30 window reads FREE at 03:15, the base price
	// otherwise) instead of having to reason about whether a window is active.
	b.WriteString("\n  " + m.editorLivePreview() + "\n")

	// Inline validation error (blocks save): a malformed HH:MM, an unparseable price,
	// or a price over the public ceiling - shown at the cause, not only at broker
	// register. Cleared on a clean commit / re-open.
	if m.edErr != "" {
		b.WriteString("  " + stEmber.Render("⚠ "+m.edErr) + "\n")
	}
	return b.String()
}

// shareSetupView is the in-TUI guided fallback when no local model was detected: a
// k9s-styled option list (pick a tool for a start one-liner, or paste a URL we
// verify). It carries the same selection-cursor + contextual-footer feel as the
// provider table so the SHARE section reads as one designed system.
func (m model) shareSetupView(w int) string {
	var b strings.Builder
	narrow := m.narrow()
	headTail := stDim.Render("   no running model found - what are you using?")
	if narrow {
		headTail = ""
	}
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("SET UP A MODEL") + headTail + "\n")
	if narrow {
		b.WriteString("  " + stDim.Render("what are you running?") + "\n")
	}
	b.WriteString("\n")

	nameW := 24
	if narrow {
		nameW = 18
	}
	for i, opt := range setupOptions {
		sel := i == m.setupCursor
		label := opt.label
		row := selCarat(sel) + " "
		if sel {
			row += rowSel(true, "  "+pad(label, nameW), w-4)
		} else {
			row += "  " + stDim.Render(pad(label, nameW))
		}
		b.WriteString(row + "\n")
		// Under the selected named tool, show its start one-liner inline (truncated to
		// the terminal width so it never overflows).
		if sel && opt.key != "other" && opt.oneLiner != "" {
			line := "      " + "start it: " + opt.oneLiner
			b.WriteString(stDim.Render(pad(line, w-2)) + "\n")
		}
	}

	// The paste row turns into a live input when the "Other" option is selected.
	if m.setupCursor == len(setupOptions)-1 {
		tail := stDim.Render("   e.g. http://127.0.0.1:8081  ·  ⏎ verifies /v1/models")
		if narrow {
			tail = ""
		}
		urlCaret := "▏"
		if m.setupAwaitKey {
			urlCaret = "" // caret moves to the key line below while entering the key
		}
		b.WriteString("\n  " + stPrompt.Render("url › ") + stSelText.Render(m.setupPaste+urlCaret) + tail + "\n")
		// Second input step: a key-protected endpoint (401/403) asks for its API key,
		// masked so a shoulder-surf doesn't leak it. Sent as a Bearer to re-verify.
		if m.setupAwaitKey {
			ktail := stDim.Render("   needs an API key  ·  ⏎ verifies with it")
			if narrow {
				ktail = ""
			}
			b.WriteString("  " + stPrompt.Render("key › ") + stSelText.Render(maskKey(m.setupKey)+"▏") + ktail + "\n")
		}
	} else {
		hint := stDim.Render("started your tool? press ") + stKey.Render("r") + stDim.Render(" to re-scan")
		b.WriteString("\n  " + hint + "\n")
	}
	if m.setupErr != "" {
		b.WriteString("\n  " + stEmber.Render(pad("! "+m.setupErr, w-2)) + "\n")
	}
	return b.String()
}
