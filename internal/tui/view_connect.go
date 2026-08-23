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
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// connectExport is the paste-ready shell block that points an OpenAI-compatible agent
// (opencode, a local bot) at the tuned-in channel's endpoint.
func connectExport(base, key, model string) string {
	return "export OPENAI_BASE_URL=" + base + "\nexport OPENAI_API_KEY=" + key + "\nexport OPENAI_MODEL=" + model
}

// connect is two-phase: it builds the quote for the selected band and enters the
// cost-confirmation screen (or the over-limit screen if the cheapest station is
// above the user's max). The proxy is only bound on accept (openChannel).
func (m model) connect() (tea.Model, tea.Cmd) {
	bd, ok := m.selectedBand() // the cursor against the filtered + sorted view
	if !ok {
		return m, nil
	}
	// VOICE bands (tts/stt) can never reach the chat relay here: visibleBands() STRUCTURALLY
	// excludes them from the top-level list, so selectedBand() only ever returns an LLM (chat)
	// band. A voice band is surfaced + cued exclusively from THE DJ BOOTH (voice.go), which
	// routes to startVoicePreview — never openChannel/modeChat. This is why a consumer can no
	// longer tune a voice band as chat and hit "504 no station is serving <voice>".
	if !bd.online || bd.cheapest == nil {
		// An offline band (incl. the sticky recent station whose node aged out of
		// /discover): Enter re-scans the band to find it back on air, rather than a
		// dead-end - the natural "bring it back" action so a recent station is always
		// re-tunable from here.
		m.status = stEmber.Render(noStationServing(bd.model)) + stDim.Render(" - re-scanning the band…")
		m.scanErr, m.scanned = false, false
		return m, fetchOffers(m.broker)
	}
	// Anonymous = free models only. Tuning a PRICED band needs an account wallet:
	// flash a clear inline login prompt instead of opening a confirm the broker would
	// reject. A FREE band (minOut 0, or a free-now window) stays open to anyone.
	if !m.loggedInState() && bd.minOut > 0 && !bd.free {
		m.status = stEmber.Render("this band is paid - ") + stKey.Render("type /login") + stDim.Render(" to use your wallet (free bands work without an account)")
		return m, nil
	}
	lim := m.limits.resolve(bd.model)
	typ := m.limits.typical()
	q := quote{b: bd, limit: lim, typical: typ, estReply: bd.minOut * float64(typ) / 1e6}
	if lim.MaxOut > 0 && bd.minOut > lim.MaxOut {
		q.overLimit = true
		m.q = q
		m.editBuf = money(bd.minOut) // pre-fill the smallest unblocking raise
		m.mode = modeOverLimit
		return m, nil
	}
	m.q = q
	m.showDetail = false // open simple; [d] expands
	m.mode = modeConnectConfirm
	return m, nil
}

// connectStages is the number of staged steps in the tune-in sequence (scan, lock,
// handshake, CHANNEL OPEN). connectStageDone is the terminal stage (all steps "ok"
// and the channel held open, ready to drop into CHANNEL on the next beat).
const (
	connectStages    = 4
	connectStageDone = connectStages
	// connectDwellFrames is how many ticks each staged step holds before the next
	// reveals - ~3 frames at the 160ms tick (~0.5s/step) so the sequence reads as a
	// deliberate lock, not a flicker, and completes in ~2s.
	connectDwellFrames = 3
)

func (m model) confirmView(w int) string {
	q := m.q
	bd := q.b
	st := bd.cheapest
	var b strings.Builder

	// Section-tab heading, matching the SHARE / CHANNEL look so the connect-confirm
	// reads as part of the same designed system, not an older screen.
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("TUNE IN") +
		stDim.Render("   confirm the channel before it opens") + "\n\n")

	// A k9s-style aligned one-row table: the station you'd lock, padded under the
	// same column-header style the share table uses (reverse-video cursor row + carat).
	b.WriteString("  " + stDim.Render(fmt.Sprintf("  %-22s  %-12s  %-10s  %s", "BAND", "STATION", "SIGNAL", "FLAGS")) + "\n")
	b.WriteString("  " + selCarat(true) + rowSel(true,
		fmt.Sprintf("  %-22s  %-12s  %-10s  %s",
			pad(bd.model, 22), pad("@"+st.NodeID, 12), pad(tpsPlain(st.TPS, st.Online), 10), plainBandBadge(bd, m.limits, false)),
		w-4) + "\n\n")

	// One glanceable line: what you pay, that it's under your cap, est cost.
	cap := ""
	if q.limit.MaxOut > 0 {
		cap = stDim.Render("   ·   ") + stLive.Render("under your "+money(q.limit.MaxOut)+" cap")
	}
	b.WriteString("    " + stEmber.Render(money(bd.minOut)) + stDim.Render(" $/1M out") + bandTierSuffix(bd) + cap +
		stDim.Render("   ·   ~"+dollars(q.estReply)+" / reply") + "\n")

	// Everything else is behind [d] - keep the default screen simple.
	if m.showDetail {
		b.WriteString("\n")
		if bd.stations > 1 {
			b.WriteString(stDim.Render("    live range   ") + stEmber.Render(rangeStr(bd)) + bandTierSuffix(bd) + stDim.Render(" $/1M out  ("+fmt.Sprintf("%d", bd.stations)+" on air)") + "\n")
		}
		b.WriteString(stDim.Render("    input price  ") + stEmber.Render(money(st.PriceIn)) + stDim.Render(" $/1M in") + "\n")
		if m.haveBal {
			reps := 0.0
			if q.estReply > 0 {
				reps = m.balance / q.estReply
			}
			if q.estReply <= 0 {
				b.WriteString(stDim.Render(fmt.Sprintf("    balance      %s   · replies are free on this band", dollars(m.balance))) + "\n")
			} else {
				b.WriteString(stDim.Render(fmt.Sprintf("    balance      %s   (~%.0f replies)", dollars(m.balance), reps)) + "\n")
			}
		}
		b.WriteString(stDim.Render("    locked       each reply price-locks at send; a hold pre-auths the session") + "\n")
	}

	b.WriteString("\n")
	// One line, key beside its action (audit: the two-row form drifted, keys landing
	// left of the wrong labels).
	b.WriteString("       " + stKey.Render("[enter/y]") + " " + stLive.Render("accept · open channel") + "    " +
		stKey.Render("[esc/n]") + " " + stDim.Render("deny · back") + "    " + stKey.Render("[d]") + " " + stDim.Render("detail") + "\n")
	return b.String()
}

// connectStep renders one line of the staged tune-in: a leading ◉ on-air glyph,
// the step label, and - once the step is reached - a trailing "ok". A step not yet
// reached is dim and shows the working "…"; the reached step glints the on-air red.
// state: 0 = pending, 1 = working (current), 2 = done.
func connectStep(state int, label, detail string) string {
	switch state {
	case 0: // pending - not yet revealed (dim, hollow)
		return "  " + stDim.Render(glyphOffAir+" "+label)
	case 1: // working - the live carrier glint + an animated ellipsis-feel "…"
		line := "  " + stRed.Render(glyphOnAir) + " " + stLive.Render(label)
		if detail != "" {
			line += stDim.Render(" · ") + stDim.Render(detail)
		}
		return line + stDim.Render(" …")
	default: // done
		line := "  " + stRed.Render(glyphOnAir) + " " + stDim.Render(label)
		if detail != "" {
			line += stDim.Render(" · ") + stDim.Render(detail)
		}
		return line + stDim.Render(" … ") + stLive.Render("ok")
	}
}

// connectingView renders the staged tune-in sequence (modeConnecting): the web's
// scan -> lock -> lineage handshake -> CHANNEL OPEN animation, finishing on the
// aligned BASE URL / API KEY / MODEL plate + "roger that." The steps reveal one at
// a time on the carrier beat (m.connectStage); under quiet the whole sequence is
// shown resolved at once. Each step uses the shared ◉ on-air glyph and the verified
// ◆; the only color is the one red glint on ◉ / ◆ and the selection.
func (m model) connectingView(w int) string {
	o := m.connected
	if o == nil {
		return ""
	}
	st := m.connectStage // 0..connectStageDone; a step at index i is "done" once stage>i
	stateOf := func(i int) int {
		switch {
		case st > i+1 || st >= connectStageDone:
			return 2 // done
		case st == i+1:
			return 2 // the step that just completed
		case st == i:
			return 1 // working (current)
		default:
			return 0 // pending
		}
	}
	narrow := m.narrow()
	var b strings.Builder
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("TUNE IN") +
		stDim.Render("   locking the channel") + "\n\n")

	// The lock detail (station · t/s · price) is the widest line; drop it to just the
	// callsign when narrow so the step still reads but never overflows.
	lockDetail := "@" + o.NodeID + " · " + tpsPlain(o.TPS, o.Online) + " · " + money(o.PriceOut) + " $/M"
	if narrow {
		lockDetail = "@" + o.NodeID
	}
	b.WriteString(connectStep(stateOf(0), "scanning stations", "") + "\n")
	b.WriteString(connectStep(stateOf(1), "locking strongest", lockDetail) + "\n")
	// The lineage-handshake step carries the verified ◆ + the signed triplet (the
	// triplet is dropped when narrow).
	hs := stateOf(2)
	triplet := " weights·shard·token"
	if narrow {
		triplet = ""
	}
	hsTriplet := stGold.Render(glyphLineage) + stDim.Render(triplet)
	switch hs {
	case 0:
		b.WriteString("  " + stDim.Render(glyphOffAir+" lineage handshake") + "\n")
	case 1:
		b.WriteString("  " + stRed.Render(glyphOnAir) + " " + stLive.Render("lineage handshake") + "  " + hsTriplet + stDim.Render(" …") + "\n")
	default:
		b.WriteString("  " + stRed.Render(glyphOnAir) + " " + stDim.Render("lineage handshake") + "  " + hsTriplet + stDim.Render(" … ") + stLive.Render("ok") + "\n")
	}
	// The terminal CHANNEL OPEN line: revealed once every prior step is done.
	if st >= connectStageDone {
		open := "  " + stRed.Render(glyphOnAir) + " " + stBrand.Render("CHANNEL OPEN") + "  " + stKey.Render(o.Model)
		if !narrow {
			mark := stGold.Render(glyphLineage + " lineage")
			if o != nil && o.Confidential {
				mark = stGold.Render(glyphConf + " confidential")
			}
			open += stDim.Render(" via @") + stSelText.Render(o.NodeID) + "  " + mark
		}
		b.WriteString(open + "\n")
		// The clean endpoint plate + the drop-in line (a shorter line when narrow).
		b.WriteString("\n" + m.endpointBlock(w) + "\n")
		dropIn := "drop-in, OpenAI-compatible - point any OpenAI tool here. "
		if narrow {
			dropIn = "drop-in. "
		}
		b.WriteString("  " + stDim.Render(dropIn) + stLive.Render("roger that.") + "\n")
	} else {
		b.WriteString("  " + stDim.Render(glyphOffAir+" CHANNEL OPEN") + "\n")
	}
	return b.String()
}

// endpointBlock renders the clean, aligned BASE URL / API KEY / MODEL spec plate -
// dim mono labels, bright mono values, lined up like the web's endpoint plate. It
// is the shared surface used by both the staged tune-in finale and the persistent
// endpoint panel, so the binary shows the same "spec plate" the site does.
func (m model) endpointBlock(w int) string {
	model := "-"
	if m.connected != nil {
		model = m.connected.Model
	}
	// A small fixed-width label column so the values align in one mono gutter.
	row := func(label, value string) string {
		return "  " + stDim.Render(pad(label, 9)) + stKey.Render(value)
	}
	// The full key never sits on screen (audit P0) - and the recovery hint only rides
	// where it fits (narrow terminals keep the masked key alone).
	keyHint := ""
	if w >= 70 {
		keyHint = stDim.Render("  (roger keys prints the full key)")
	}
	return row("BASE URL", m.endpoint) + "\n" +
		row("API KEY", maskKey(m.apikey)+keyHint) + "\n" +
		row("MODEL", model)
}

// overLimitView is the over-limit + inline edit-your-max screen (3.3).
func (m model) overLimitView(w int) string {
	q := m.q
	bd := q.b
	st := bd.cheapest
	gap := bd.minOut - q.limit.MaxOut
	pct := 0.0
	if q.limit.MaxOut > 0 {
		pct = gap / q.limit.MaxOut * 100
	}
	var b strings.Builder
	b.WriteString("\n" + stEmber.Render("  ⚠ the band is above your limit") + "       " + stSelText.Render(bd.model) + "\n\n")
	b.WriteString(stDim.Render("    cheapest on air   ") + stEmber.Render(money(bd.minOut)) + stDim.Render(" $/1M out   @"+st.NodeID+"  "+st.Region+"  ") + tpsCell(st.TPS, st.Online) + "\n")
	b.WriteString(stDim.Render("    your max          ") + stEmber.Render(money(q.limit.MaxOut)) + stDim.Render(" $/1M out") + "\n")
	b.WriteString(stDim.Render(fmt.Sprintf("    gap               +%.2f  (%.0f%% over)   you would pay ", gap, pct)+dollars(bd.minOut*float64(q.typical)/1e6)+" / reply") + "\n\n")
	// the inline edit row
	editShown := m.editBuf
	hint := stDim.Render("min " + money(bd.minOut))
	if v, err := strconv.ParseFloat(strings.TrimSpace(m.editBuf), 64); err == nil && v >= bd.minOut {
		hint = stLive.Render("▸ enough to tune in now")
	} else {
		hint = stEmber.Render("still below the band (" + money(bd.minOut) + ")")
	}
	b.WriteString(stDim.Render("    raise your max for "+bd.model+"   (was "+money(q.limit.MaxOut)+")") + "\n")
	b.WriteString("      $/1M out   " + stSelText.Render("▏"+editShown+"▏") + "   " + hint + "\n\n")
	b.WriteString("    " + stKey.Render("⏎ save & re-check") + stDim.Render("   ↑ +0.01   ↓ -0.01   ") + stDim.Render("w wait & notify   esc deny") + "\n")
	return b.String()
}

// bandOnAir reports whether the latest scan shows any online station for model.
// It also counts the user's own in-process /share when it serves that model, so a
// solo founder sharing + chatting their own node is never told "no station" on a
// stale scan (the share registered but a fresh /discover hasn't come back yet).
// connectedModel returns the model id of the currently-open channel, or "" when
// not connected. Used to MARK the connected band in the browse list (the lit
// "◉ connected" row) and to drive the from-the-list disconnect shortcut.
func (m model) connectedModel() string {
	if m.connected == nil {
		return ""
	}
	return m.connected.Model
}

// autoTuneCmd kicks the silent auto-tune off the AGENT [0] landing: decide immediately
// when a scan is already in hand, else fetch /discover first so a cold launch (AGENT
// before any BROWSE scan) still finds a band. There is NO retry loop - a single empty
// scan lands on the honest empty state (the founder's "spams no station" regression).
func autoTuneCmd(broker string, scanned bool) tea.Cmd {
	if scanned {
		return func() tea.Msg { return autoTuneMsg{} }
	}
	return fetchOffers(broker)
}

// endpointPanel is the persistent channel-open plate shown under the browse view
// while a channel is held: the ◉ on-air glyph + (when confidential) the verified
// ◆, then the shared aligned BASE URL / API KEY / MODEL block + the drop-in line.
// It is the same spec plate the staged tune-in finishes on (endpointBlock), inside
// a flat single-hairline border (no heavy/double box).
func (m model) endpointPanel(w int) string {
	lineage := stDim.Render("·")
	if m.connected != nil && m.connected.Confidential {
		lineage = stGold.Render(glyphConf + " confidential (TEE-verified)")
	}
	head := stRed.Render(glyphOnAir+" ") + stLive.Render("channel open") + "  " +
		stDim.Render("point your bots here") + "  " + lineage
	body := head + "\n" +
		m.endpointBlock(w) + "\n" +
		stDim.Render("  drop-in, OpenAI-compatible - point any OpenAI tool here. ") + stLive.Render("roger that.") + stDim.Render("  ·  /chat to test")
	return stPanel.Render(body)
}
