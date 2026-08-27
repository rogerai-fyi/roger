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
	"sync"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	"rogerai.fm/roger/v6/internal/glyphs"
)

// alertBox is a tiny thread-safe mailbox: the relay's failover callback (running
// in the proxy goroutine) drops a line in, and the Bubble Tea tick loop drains it
// onto the status line. Pointer-shared so the model copy on each Update sees it.
type alertBox struct {
	mu  sync.Mutex
	msg string
}

// loginView renders the confirmable [L] panel: the clean GitHub device-flow panel
// while waiting for authorization (#2), the log-out confirm when logged in (#5),
// or the press-enter login prompt when logged out (#5). All forms are left-aligned,
// the device code is rendered in the mono key style, and the panel is width /
// NO_COLOR / narrow safe (it wraps no fixed-width art; the bordered plate degrades
// to plain text when color is stripped).
func (m model) loginView(w int) string {
	pulse := beaconPulse()

	// IN FLIGHT: the device flow started - the tidy left-aligned panel (#2/#3).
	if m.loginWaiting && m.loginDevice.UserCode != "" {
		note := m.loginNote
		if note == "" {
			note = "opened in your browser (or copy the link above)"
		}
		body := stKey.Render("GITHUB LOGIN") + "\n\n" +
			stDim.Render("  1 · open   ") + stLive.Render(m.loginDevice.VerificationURI) + "\n" +
			stDim.Render("  2 · code   ") + stKey.Render(m.loginDevice.UserCode) + "\n\n" +
			stGold.Render("  "+pulse) + stDim.Render(" waiting for authorization...") + "\n" +
			stDim.Render("  "+note) + "\n\n" +
			stDim.Render("  esc backs out (you can /login again any time)")
		return "\n" + panelFit(body, w) + "\n"
	}

	// LOGGED IN -> the log-out confirm (#5). Never auto-logs-out.
	if m.loggedInState() {
		who := "@" + m.ghLogin
		if m.ghLogin == "" {
			who = "your account"
		}
		body := stKey.Render("ACCOUNT") + "\n\n" +
			stGold.Render("  "+glyphLineage+" ") + stDim.Render("logged in as ") + stSelText.Render(who)
		if m.haveBal {
			body += stDim.Render(" · ") + stEmber.Render(dollars(m.balance))
		}
		body += "\n\n" +
			"  " + stDim.Render("log out? ") + stEmber.Render("[y/N]") + "\n\n" +
			stDim.Render("  y logs out (clears this session) · n / esc keeps you logged in")
		return "\n" + panelFit(body, w) + "\n"
	}

	// LOGGED OUT -> press enter to start the GitHub device flow (#5).
	body := stKey.Render("GITHUB LOGIN") + "\n\n" +
		stDim.Render("  log in with GitHub to use your wallet + earn as a provider") + "\n\n" +
		"  " + stDim.Render("press ") + stKey.Render("enter") + stDim.Render(" to start (opens your browser) · esc cancels")
	return "\n" + panelFit(body, w) + "\n"
}

// ---- view ----
func (m model) View() string {
	// The in-TUI Ping World screensaver paints fullscreen - no header/preset/footer chrome,
	// just the world (any key wakes back to prevMode; see onKey).
	if m.mode == modePingWorld {
		return m.world.View()
	}
	w := m.effWidth()
	var b strings.Builder
	// COMPACT (the "windowshade"): no expanded preset bar and no spacer - the dense
	// one-line header carries the section + counts + account + the `m:expand` hint, so
	// the whole top collapses to a single strip + a hairline rule.
	if m.compact {
		b.WriteString(m.compactHeader(w) + "\n")
	} else {
		// A blank spacer line sets the preset bar apart from the brand lockup below it, so
		// the [1] TUNE IN ... bar and the ▟▄▙ R O G E R · A I ((•)) logo read as two
		// distinct rows instead of one cramped block. A single line keeps it tight on a
		// short terminal; an empty line is inherently NO_COLOR / narrow-safe.
		b.WriteString(m.presetBar(w) + "\n\n")
		b.WriteString(m.header(w) + "\n")
	}
	switch m.mode {
	case modeHelp:
		content := m.helpView()
		budget := m.height - 8
		if budget < 6 {
			budget = 6
		}
		// No measured height (tests / pipes) renders the whole thing - the pager only
		// engages when we KNOW the content will not fit.
		if m.height <= 0 || lineRows(content) <= budget {
			b.WriteString(content)
		} else {
			m.helpVP.Width = w
			m.helpVP.Height = budget
			m.helpVP.SetContent(content)
			b.WriteString(m.helpVP.View() + "\n")
			pct := int(m.helpVP.ScrollPercent() * 100)
			b.WriteString("  " + stDim.Render(fmt.Sprintf("── %d%% · ↑↓ / pgdn scroll · esc back ──", pct)) + "\n")
		}
	case modeLog:
		b.WriteString(m.logView(w))
	case modeChat:
		b.WriteString(m.chatView(w))
	case modeConnectConfirm:
		b.WriteString(m.confirmView(w))
	case modeConnecting:
		b.WriteString(m.connectingView(w))
	case modeOverLimit:
		b.WriteString(m.overLimitView(w))
	case modeLimits:
		b.WriteString(m.limitsView(w))
	case modeShare:
		b.WriteString(m.shareView(w))
	case modeBandCard:
		b.WriteString(m.bandCardView(w))
	case modeShareEditor:
		b.WriteString(m.shareEditorView(w))
	case modeShareSetup:
		b.WriteString(m.shareSetupView(w))
	case modeQuitConfirm:
		b.WriteString(m.quitConfirmView(w))
	case modeAgent:
		b.WriteString(m.agentView(w))
	case modeLogin:
		b.WriteString(m.loginView(w))
	case modeBandDetail:
		b.WriteString(m.bandDetailView(w))
	case modeVoicePreview:
		b.WriteString(m.voicePreviewView(w))
	case modeVoiceBooth:
		b.WriteString(m.voiceBoothView(w))
	case modeListeningPost:
		b.WriteString(m.listeningPostView(w))
	case modeShareVoice:
		b.WriteString(m.shareVoiceView(w))
	case modeVoicePicker:
		b.WriteString(m.voicePickerView(w))
	case modePrivate:
		b.WriteString(m.privateView(w))
	case modeBandManage:
		b.WriteString(m.bandManageView(w))
	case modeBandMove:
		b.WriteString(m.bandMoveView(w))
	case modeBandRevokeConfirm:
		b.WriteString(m.bandRevokeConfirmView(w))
	case modeBandRotateConfirm:
		b.WriteString(m.bandRotateConfirmView(w))
	case modeBandConfig:
		b.WriteString(m.bandConfigView(w))
	case modeBandLabel:
		b.WriteString(m.bandLabelView(w))
	case modeBandQuants:
		b.WriteString(m.bandQuantsView(w))
	case modeRemoteSession:
		b.WriteString(m.remoteSessionView(w))
	case modeFreqEntry:
		// The PRIVATE FREQUENCY input rides ABOVE the live band browser (the list stays
		// visible behind it), mirroring the filter strip: a small focused input to enter a
		// frequency code, then enter resolves it. esc returns to the open market browser.
		b.WriteString(m.freqEntryView(w) + "\n")
		b.WriteString(m.browseView(w))
	default:
		b.WriteString(m.browseView(w))
	}
	if m.connected != nil && m.mode != modeChat && m.mode != modeConnectConfirm && m.mode != modeConnecting && m.mode != modeOverLimit && m.mode != modeLimits && m.mode != modeAgent && m.mode != modeLogin && !m.inShareSection() {
		// COMPACT drops the bordered endpoint plate (a "compact-on-connect extra") to a
		// single terse status line - the load-bearing endpoint stays one /endpoint away.
		if m.compact {
			b.WriteString("\n" + truncVisible("  "+stRed.Render(glyphOnAir+" ")+stLive.Render("channel open")+stDim.Render(" · ")+stKey.Render(m.endpoint)+stDim.Render(" · /chat"), w))
		} else {
			b.WriteString("\n" + m.endpointPanel(w))
		}
	}
	// The ON AIR provider panel rides under the browse view whenever /share is live.
	// COMPACT drops the bordered panel to a one-line status (density + width-safety).
	if m.onAir && m.share != nil && (m.mode == modeBrowse || m.mode == modeCommand) {
		if m.compact {
			b.WriteString("\n" + m.compactOnAirLine(w))
		} else {
			b.WriteString("\n" + m.onAirPanel(w))
		}
	}
	// The command prompt is always present in browse/command mode so it is never a
	// mystery WHERE to type: a labeled `rog ›` line that echoes every keystroke
	// live (its textinput View() is re-rendered each Update). modeChat owns its own
	// always-live prompt inside chatView.
	if m.mode == modeCommand {
		// progressive disclosure: the live-filtered command palette above the prompt.
		b.WriteString("\n" + m.paletteView(w))
	}
	if m.mode == modeBrowse || m.mode == modeCommand {
		b.WriteString("\n" + m.promptLine(w))
	}
	b.WriteString("\n" + m.footer(w))
	// Alt-screen: pad a short frame with blank lines up to the terminal height so it
	// fully overwrites a TALLER previous frame (e.g. a long model list that overflowed
	// a small terminal) rather than leaving ghost remnants of the old frame - the
	// duplicated brand/header/"scanning…" the founder hit after going on-air. Guarded
	// on height>0 so headless tests (no WindowSizeMsg) keep their exact, unpadded output.
	out := b.String()
	// THE BOTTOM PIN: agentView marks where its slack belongs (agentPinMark); spend it
	// HERE, where the whole frame - chrome, footer, status - has actually been built
	// and can be counted. Padding inside the view instead only knows that view's row
	// budget, which is a ceiling with approximate chrome accounting, and overshoots.
	// Always resolved, even with no slack, so the marker can never reach a terminal.
	if i := strings.Index(out, agentPinMark+"\n"); i >= 0 {
		out = strings.Replace(out, agentPinMark+"\n", "", 1)
		if m.height > 0 {
			rows := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1
			if slack := m.height - rows; slack > 0 {
				out = out[:i] + strings.Repeat("\n", slack) + out[i:]
			}
		}
	}
	if m.height > 0 {
		out = strings.TrimRight(out, "\n")
		if n := strings.Count(out, "\n") + 1; n < m.height {
			out += strings.Repeat("\n", m.height-n)
		}
	}
	// A live smart-mode selection paints reverse-video over its cells (restyle
	// only - the frame's text is untouched).
	out = m.overlaySelection(out)
	// THE DECK GROUND goes on LAST, over the finished frame including the selection
	// overlay: it fills the cells nothing else claimed, and solidBackground re-arms it
	// after every nested style's reset. Painting earlier would let each later styled
	// span punch a hole straight back through to the terminal's own ground.
	return paintDeck(out, w)
}

// paletteCmd is one entry in the `/` command palette (A.5 progressive disclosure): a runnable
// /command, a plain one-liner, and its key shortcut. Kept in lock-step with run()'s verbs so
// nothing listed here is a dead command.
type paletteCmd struct{ name, desc, key string }

var paletteCmds = []paletteCmd{
	{"/search", "re-scan the band for stations", "r"},
	{"/connect", "tune in to the selected station", "⏎"},
	{"/share", "put a local model on air (earn or free)", "2"},
	{"/limits", "your per-model spend caps", "3"},
	{"/login", "link GitHub (needed to earn)", "L"},
	{"/balance", "wallet balance", ""},
	{"/topup", "add funds", ""},
	{"/grant", "private free keys for bots/family", ""},
	{"/confidential", "route only to TEE-attested nodes", "C"},
	{"/endpoint", "the OpenAI-compatible endpoint + key", ""},
	{"/config", "broker + identity", ""},
	{"/compact", "minimize to the dense windowshade", "m · alt+m"},
	{"/ping", "the Ping World screensaver", "z"},
	{"/webui", "open the browser node console", "w · ⌃w"},
	{"/support", "rogerai.fm - community + Discord", ""},
	{"/help", "the full operating manual", "?"},
	{"/log", "node + broker messages", ""},
	{"/quit", "quit RogerAI", "q"},
}

// paletteMatch returns the palette entries whose name contains the (case-insensitive) query;
// an empty query lists them all. Pure - the filter behind the live `/` palette.
func paletteMatch(query string) []paletteCmd {
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "/")))
	out := make([]paletteCmd, 0, len(paletteCmds))
	for _, c := range paletteCmds {
		if q == "" || strings.Contains(strings.TrimPrefix(c.name, "/"), q) {
			out = append(out, c)
		}
	}
	return out
}

// paletteView renders the live-filtered command palette shown while typing in modeCommand: a
// compact, calm list (command · description · shortcut), capped so it never floods a short
// terminal. The list filters as you type; enter still runs whatever is in the prompt.
func (m model) paletteView(w int) string {
	matches := paletteMatch(m.cmd.Value())
	if len(matches) == 0 {
		return "  " + stDim.Render("no command matches - esc to cancel")
	}
	const maxRows = 8
	more := 0
	if len(matches) > maxRows {
		more, matches = len(matches)-maxRows, matches[:maxRows]
	}
	// Each row is clamped to w (ANSI-safe) so the palette never wraps on a narrow terminal.
	clamp := func(s string) string { return truncVisible(s, w) }
	var b strings.Builder
	b.WriteString(clamp("  "+stDim.Render("commands")+stTag.Render("  type to filter · ⏎ run · esc close")) + "\n")
	for _, c := range matches {
		key := ""
		if c.key != "" {
			key = stTag.Render("  " + c.key)
		}
		b.WriteString(clamp("   "+stKey.Render(fmt.Sprintf("%-14s", c.name))+stDim.Render(c.desc)+key) + "\n")
	}
	if more > 0 {
		b.WriteString(clamp("   "+stTag.Render(fmt.Sprintf("+%d more - keep typing to narrow", more))) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// promptLine renders the always-visible command prompt. It shows the live
// textinput View() (cursor + echoed text) when focused, or a calm hint to press
// `/` when idle, so the user always sees a clear, labeled place to type.
func (m model) promptLine(w int) string {
	if m.mode == modeCommand {
		return stPrompt.Render("  rog › ") + m.cmd.View()
	}
	hint := "press / to type a command  ·  enter to tune in"
	if m.narrow() {
		hint = "/ command · ⏎ tune in"
	}
	return stPrompt.Render("  rog › ") + stDim.Render(hint)
}

func (m model) quitConfirmView(w int) string {
	n := m.onAirCount()
	body := stRed.Render(glyphOnAir+" ON AIR") + stDim.Render(" - you are sharing ") +
		stKey.Render(fmt.Sprintf("%d model(s)", n)) + "\n\n" +
		"  You are ON AIR sharing " + stKey.Render(fmt.Sprintf("%d model(s)", n)) +
		stDim.Render(" - quit and go off air? ") + stEmber.Render("[y/N]") + "\n\n" +
		stDim.Render("  y quits + goes off air cleanly · n / esc keeps you on air")
	return "\n" + panelFit(body, w) + "\n"
}

func (m model) browseView(w int) string {
	// The PRIVATE half is checked FIRST, ahead of the empty-market branch: a private band
	// is hidden from /discover by design, so m.bands can be empty at the exact moment the
	// operator has bands to show. Falling through would print "no stations on air" over a
	// list of their own models.
	if m.tuneTab == tabPrivate {
		return m.privateTabView(w)
	}
	if len(m.bands) == 0 {
		// ASYNC LOADING: the initial /discover (and any r re-scan) runs off the Bubble
		// Tea event loop, so until the first offers land we show the SAME ((•)) scanning
		// indicator the SHARE provider table uses - a clear "scanning the band…" pose, not
		// a frozen empty list. loadedOnce flips true on the first offersMsg; scanned tracks
		// every scan so a manual r re-scan (which resets scanned) shows it again too.
		loading := !m.scanned && !m.scanErr
		// COMPACT: no Ping art (it animates and eats rows) - a single static status
		// line in the calm windowshade voice.
		if m.compact {
			switch {
			case m.scanErr:
				return "  " + stEmber.Render(glyphs.Fold("(○) ...static")) + stDim.Render(" - broker off air · r to retune") + "\n"
			case loading:
				return "  " + m.transmitLineFor(0) + stDim.Render("  scanning the band…") + "\n"
			default:
				return "  " + stDim.Render(beaconDot()+" no stations on air - press [2] to share a model and put one up · r to re-scan") + "\n"
			}
		}
		// Three empty cases: the broker dropped -> Ping "...static"; still scanning (no
		// fetch back yet) -> the ((•)) scanning indicator (mirrors SHARE); scanned but
		// quiet -> ONE static actionable line (audit #10). The empty band no longer runs a
		// rotating motivational carousel (it read as "loading forever" to a newcomer who
		// just needs the next move) - just the live signal-bar shimmer (kept, the
		// informative "live, not frozen" cue) over a single clear CTA.
		switch {
		case m.scanErr:
			return "\n" + pingPose(pingStatic, m.frame, w, "…static. the broker went off air - press r to retune") + "\n"
		case loading:
			return "\n  " + m.transmitLineFor(0) + "\n  " + stDim.Render("scanning the band…") + "\n"
		default:
			shimmer := tintSignal(signalBarsRaw(m.frame, 55, 0, true, 0, 0), 55, 0, true)
			if m.narrow() {
				// Slim: stack the shimmer above the trimmed CTA so neither overflows the
				// real width (the empty-band line is not width-clamped).
				return "\n  " + shimmer + "\n  " + emptyBandCTA(true) + "\n"
			}
			return "\n  " + shimmer + "  " + emptyBandCTA(false) + "\n"
		}
	}
	var b strings.Builder
	// SCALE: render the FILTERED + SORTED view, not raw m.bands, and only the visible
	// window of it (virtualized). vis is the derived list the cursor + window index.
	vis := m.visibleBands()
	total := len(m.bands)
	matched := len(vis)
	// COMPACT windowshade: an at-a-glance deck of ON-AIR bands only - 2-up, name + a static
	// signal bar, no column grid / offline rows / prices / flags. The calm minimal view (the
	// founder's "true windowshade"); the counts live in the compact header.
	if m.compact {
		return m.compactBandList(w, vis, total)
	}
	// Section heading, manual-style: a thin tab + a count, like the web's §-markers.
	// COMPACT drops the prose count to a terse "N" and (below) the column-header row,
	// so more bands fit per screen - the windowshade density. The sort label rides in
	// the heading so the active dial (strongest / cheapest / fastest / most-stations)
	// is always visible (S cycles it; mirrors the /bands web page).
	sortTag := stDim.Render(" · sort " + sortLabel(m.sortMode))
	if m.narrow() {
		// Narrow: drop the sort tag from the heading (it would overflow the slim width);
		// the footer still teaches S, and the filter line carries the active state.
		sortTag = ""
	}
	// Frequency / mode indicator: OPEN MARKET by default (dim ink), PRIVATE FREQ <code>
	// when a private band is tuned. The private label is rendered in the ONE accent red
	// (with the ◉ on-air mark) so it is a DISTINCT mode signal - it is unmistakable that
	// you have left the public marketplace for a hidden channel. esc returns to OPEN
	// MARKET. Always present so the user always knows which mode they are in.
	// On narrow/compact widths the default OPEN MARKET label is dropped (it would
	// overflow the slim heading); a tuned PRIVATE FREQ is always shown since it is
	// load-bearing state, and the status line also carries it on tune-in.
	freqTag := ""
	switch {
	case m.tuneFreq != "" && (m.narrow() || m.compact):
		// Narrow: the "PRIVATE FREQ <code>" label would overflow the slim heading - show a
		// bare accented ◉ marker. The red glyph alone still signals "off the open market"
		// (it is the same accent the full label uses); the status line + the freq-only band
		// list carry the code.
		freqTag = stDim.Render(" · ") + stRed.Render(glyphOnAir)
	case m.tuneFreq != "":
		freqTag = stDim.Render(" · ") + stRed.Render(glyphOnAir+" PRIVATE FREQ "+freqLabelShort(m.tuneFreqLabel))
	case !m.narrow() && !m.compact:
		freqTag = stDim.Render(" · ") + stDim.Render("OPEN MARKET")
	}
	if m.compact {
		b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("BAND") +
			stDim.Render(fmt.Sprintf("  %d", matched)) + sortTag + freqTag + "\n")
	} else {
		// "N models on air" counts LLM (chat) bands only — voice bands live in THE DJ BOOTH (the
		// footnote), so counting them here would disagree with the LLM-only rows below.
		b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("THE BAND") +
			stDim.Render("   "+plural(m.llmBands(), "band")) + sortTag + freqTag + "\n")
	}
	// FILTER line: shown while the live filter input is open (f) OR when a filter /
	// toggle is applied. It carries the active name filter, the quick toggles, and the
	// match count (e.g. "filter: qwen  (3/240)") so it's always clear what is narrowing
	// the list. esc clears + closes, enter keeps it applied and returns to the list.
	if m.filterMode || m.filtersActive() {
		b.WriteString(m.filterLine(matched, total) + "\n")
	}
	// No band matches the active filter / toggles: a clear note (not a blank list),
	// with the keys to widen back out. Mirrors the /bands web page's empty state.
	if matched == 0 {
		return b.String() + "  " + stEmber.Render("no bands match") +
			stDim.Render(" - esc clears the filter, S re-sorts, the toggles widen it") + "\n"
	}
	// The TUNING DIAL (catalog #3): a ◆ pointer scrubbing across the band detents,
	// gliding (harmonica) toward the tuned band as you move the cursor. Wide only (the
	// narrow layout drops the extra chrome); the ◆ lights the dial-blue, the scale dims.
	if !m.compact && !m.narrow() {
		dw := m.dialWidth()
		px := int(m.dialPos + 0.5)
		if !m.dialInit { // before the first tick seeds it, park the ◆ on the tuned band
			px = int(m.dialTargetX() + 0.5)
		}
		strip := dialStrip(px, dialDetents(matched, dw), dw)
		var sb strings.Builder
		for _, r := range strip {
			if r == '◆' {
				sb.WriteString(lampStyle(roleDial).Render(string(r)))
			} else {
				sb.WriteString(stDim.Render(string(r)))
			}
		}
		b.WriteString("  " + sb.String() + "\n")
	}
	// Narrow (< 64 col): a slim three-column table (band · on air · price), dropping
	// the signal + flags columns so nothing overflows the real width. Wide: the full
	// fixed grid (band · on air · range · signal · flags). (TUI-V2-CRITIQUE A.)
	nameW := 20
	// The ctx + t/s columns ride ONLY when the terminal is wide enough to add them
	// without overflowing the fixed 80-col grid (the default wide layout at w=80 stays
	// exactly as it was). The expanded station log [i] always carries per-station ctx +
	// t/s regardless of width. t/s appears a touch earlier than ctx (it is the more
	// load-bearing headline metric and the web row shows it). The signal meter still
	// encodes throughput at narrower wide widths, so dropping the explicit t/s column
	// there is honest, not lossy.
	showTPS := !m.narrow() && w >= 88
	showCtx := !m.narrow() && w >= 90
	if m.narrow() {
		nameW = 14
		if !m.compact {
			b.WriteString("  " + stDim.Render(fmt.Sprintf("%-14s  %-9s  %s", "band", "on air", "$/1M out")) + "\n")
		}
	} else {
		// Column header, tabular. Widths match the body cells exactly so price + t/s +
		// signal columns line up under a fixed grid (lipgloss width, not eyeballed
		// spacing). COMPACT omits the header row entirely (denser; cells stay self-evident).
		if !m.compact {
			tpsHdr := ""
			if showTPS {
				tpsHdr = "  " + fmt.Sprintf("%-5s", "t/s")
			}
			ctxHdr := ""
			if showCtx {
				ctxHdr = "  " + fmt.Sprintf("%-6s", "ctx")
			}
			b.WriteString("  " + stDim.Render(fmt.Sprintf("%-20s  %-9s  %-17s%s%s  %-11s  %s",
				"band", "on air", "$/1M in·out", ctxHdr, tpsHdr, "signal", "flags")) + "\n")
			// The S-scale legend, aligned under the SIGNAL column via the SAME template
			// (blank labels), shown once. The 13-col legend overhangs the 11-col cell into
			// the empty flags gap - harmless, this line carries no flags.
			ctxBlank, tpsBlank := "", ""
			if showCtx {
				ctxBlank = "  " + fmt.Sprintf("%-6s", "")
			}
			if showTPS {
				tpsBlank = "  " + fmt.Sprintf("%-5s", "")
			}
			b.WriteString("  " + stDim.Render(fmt.Sprintf("%-20s  %-9s  %-17s%s%s  %-11s  %s",
				"", "", "", ctxBlank, tpsBlank, "1 3 5 7 9 +20", "")) + "\n")
		}
	}
	// Table width for the k9s reverse-video selection bar (spans the whole row).
	tableW := w - 2
	if tableW < 20 {
		tableW = 20
	}
	connModel := m.connectedModel()
	// VIRTUALIZE: render only the window of rows that fit the terminal height. The
	// cursor is clamped into vis, the window scrolls to keep it in view, and a
	// position indicator (e.g. "12-24 of 340") + top/bottom "more" hints orient the
	// user. We deliberately iterate ONLY [top:end), never the whole list, so the
	// frame cost is O(window) at thousands of bands. browseTop is recomputed each
	// frame from the (already-clamped) cursor, so it stays correct at both edges,
	// with a filter applied (window over the filtered set), and for the sticky band.
	cur := m.cursor
	if cur >= matched {
		cur = matched - 1
	}
	if cur < 0 {
		cur = 0
	}
	rows := m.browseRows()
	top, end := windowFor(m.browseTop, cur, rows, matched)
	// Top "more" hint: rows scrolled off above.
	if top > 0 {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("↑ %d more above", top)) + "\n")
	}
	for i := top; i < end; i++ {
		bd := vis[i]
		sel := i == cur
		connected := connModel != "" && bd.model == connModel
		// An offline band (no station on air - incl. a sticky recent band whose node aged
		// out of /discover) reads "offline" in the on-air column, not a bare "-", so it is
		// obvious you cannot connect to it until a station is up. The status line + the
		// connect attempt carry the fuller "no station is serving <model> right now".
		stationsLbl := "offline"
		if bd.online {
			stationsLbl = fmt.Sprintf("%d on", bd.stations)
		}
		// The band you are on the channel with reads "connected" in the on-air column
		// (a lit row), so the open channel's station is obvious at a glance even when
		// its node has briefly aged out of /discover (the sticky offline band).
		if connected {
			stationsLbl = "connected"
		}
		if m.narrow() {
			free := ""
			if bd.free {
				free = "  FREE"
			}
			// PLAIN row for the reverse-video bar; the selected row is one accent bar.
			plain := fmt.Sprintf("%s  %s  %s%s", pad(bd.model, nameW), pad(stationsLbl, 9), rangeStr(bd), free)
			if connected {
				plain = glyphOnAir + " " + fmt.Sprintf("%s  %s  %s", pad(bd.model, nameW-2), pad(stationsLbl, 9), rangeStr(bd))
			}
			if sel {
				b.WriteString(m.caratGutter() + rowSel(true, plain, tableW) + "\n")
				continue
			}
			// Unselected: dim band, tinted price + FREE tag. A connected row leads with the
			// lit ◉ marker and a red "connected" label so it stands out in the list.
			if connected {
				b.WriteString(selCarat(false) + " " + stRed.Render(glyphOnAir) + " " + stKey.Render(pad(bd.model, nameW-2)) + "  " +
					stRed.Render(pad(stationsLbl, 9)) + "  " + stEmber.Render(rangeStr(bd)) + bandTierSuffix(bd) + "\n")
				continue
			}
			freeTag := ""
			if bd.free {
				freeTag = "  " + stLive.Render("FREE")
			}
			b.WriteString(selCarat(false) + " " + stDim.Render(pad(bd.model, nameW)) + "  " +
				stDim.Render(pad(stationsLbl, 9)) + "  " + stEmber.Render(rangeStr(bd)) + bandTierSuffix(bd) + freeTag + "\n")
			continue
		}
		// Signal from the cheapest station: the broker's 0..100 signal drives the
		// meter LEVEL (so an on-air band with no traffic still reads non-blank), with tps
		// as the legacy fallback. The band's summed in-flight count drives the meter's
		// ANIMATION (idle band steady, busy band scans). Fixed 5-cell equalizer.
		var sigTPS float64
		var sigSignal int
		online := bd.online
		sigInFlight := bd.inFlight
		if bd.cheapest != nil {
			sigTPS = bd.cheapest.TPS
			sigSignal = bd.cheapest.Signal
		}
		bctx, bctxEst := bandCtx(bd)
		ctxPlain := "-"
		if bctx > 0 {
			ctxPlain = fmtCtx(bctx)
			if bctxEst {
				ctxPlain = "~" + ctxPlain
			}
		}
		ctxSelCell := ""
		ctxRowCell := ""
		if showCtx {
			ctxSelCell = "  " + pad(ctxPlain, 6)
			// ctx cell: detected solid, estimated dim + "~" (a guess, labeled). Padded to 6.
			styled := stDim.Render(pad(ctxPlain, 6))
			if bctx > 0 && !bctxEst {
				styled = stEmber.Render(pad(ctxPlain, 6))
			}
			ctxRowCell = "  " + styled
		}
		// tok/s cell: the band's best (fastest) measured throughput across online
		// stations - the same headline t/s the web /models row shows. Honest "-" when no
		// station has reported throughput yet (never a fabricated rate). Wide-only so the
		// 80-col grid never overflows.
		tpsPlain := "-"
		if online {
			if bt := bandBestTPS(bd); bt > 0 {
				tpsPlain = strconv.Itoa(int(bt + 0.5))
			}
		}
		tpsSelCell := ""
		tpsRowCell := ""
		if showTPS {
			tpsSelCell = "  " + pad(tpsPlain, 5)
			styled := stDim.Render(pad(tpsPlain, 5))
			if tpsPlain != "-" {
				styled = stEmber.Render(pad(tpsPlain, 5))
			}
			tpsRowCell = "  " + styled
		}
		if sel {
			// k9s-style: the cursor row is one unmistakable reverse-video bar. We use
			// the raw (uncolored) signal glyphs so the single accent style governs the
			// whole row (a colored cell inside an accent bg reads as noise).
			rawSig := m.bandSMeter(m.sigFrame(), sigSignal, sigTPS, online, sigInFlight, bd.stations, true)
			plain := fmt.Sprintf("%s  %s  %s%s%s  %s  %s",
				bandNameCell(bd, nameW), pad(stationsLbl, 9), pad(priceInOutTier(bd, 17), 17), ctxSelCell, tpsSelCell, rawSig, plainBandBadge(bd, m.limits, connected))
			b.WriteString(m.caratGutter() + rowSel(true, plain, tableW) + "\n")
			continue
		}
		rng := stEmber.Render(pad(priceInOutTier(bd, 17), 17))
		sig := m.bandSMeter(m.sigFrame(), sigSignal, sigTPS, online, sigInFlight, bd.stations, false)
		nameCell := stDim.Render(bandNameCell(bd, nameW))
		statCell := stDim.Render(pad(stationsLbl, 9))
		if connected {
			// The connected band's name + on-air cell light up so the open channel is
			// obvious in the list (the "◉ connected" badge is in the flags cell too).
			nameCell = stKey.Render(bandNameCell(bd, nameW))
			statCell = stRed.Render(pad(stationsLbl, 9))
		}
		b.WriteString(selCarat(false) + " " + nameCell + "  " +
			statCell + "  " + rng + ctxRowCell + tpsRowCell + "  " + sig + "  " + bandBadge(bd, m.limits, connected) + "\n")
	}
	// Bottom "more" hint: rows scrolled off below.
	if end < matched {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("↓ %d more below", matched-end)) + "\n")
	}
	// Position indicator: which slice of the (filtered) list is on screen, e.g.
	// "12-24 of 340". Only shown when the list does not all fit (windowing is live),
	// so a short list stays uncluttered.
	if matched > rows {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("%d-%d of %d", top+1, end, matched)) + "\n")
	}
	// BADGE LEGEND: one dim key line, shown only when a visible band actually carries a
	// non-self-describing glyph (agent-ready / vision) - a plain-text-flags list needs no
	// legend. Full view only (compact folds flags away). Sits directly under the table.
	if !m.compact {
		legend := false
		for i := top; i < end; i++ {
			bd := vis[i]
			if ready, _ := bandAgentReady(bd); ready || bd.vision {
				legend = true
				break
			}
		}
		if legend {
			b.WriteString(truncVisible(bandBadgeLegend(), w) + "\n")
		}
	}
	// VOICE FOOTNOTE (LLM primacy): one DIM line at the FOOT of the LLM band list —
	// "also on air: N voices ▸ [v]" — shown ONLY when a voice band is actually on air. It is the
	// quietest live line on the screen (no ◉, no accent), drilling into THE DJ BOOTH (a child
	// screen), so voice is additive and can never rival the LLM bands above it. Absent on a
	// pure-LLM screen. Not drawn while a name filter is active (the filtered LLM view is the
	// focus) or in compact (voices fold into the header count, never a deck cell).
	if !m.compact && !m.filterMode && strings.TrimSpace(m.filterApplied) == "" {
		if foot := m.voiceFootnote(); foot != "" {
			b.WriteString(foot + "\n")
		}
		// BASE STATION footnote (below voices): your private side of the dial. A live remote
		// session earns the one red ◉ (it IS the LLM chat product); otherwise fully dim.
		if foot := m.privateFootnote(); foot != "" {
			b.WriteString(foot + "\n")
		}
	}
	return b.String()
}

// freqEntryView renders the PRIVATE FREQUENCY input strip (modeFreqEntry): a small,
// clearly accented prompt the user types/pastes a frequency code into, then enter
// resolves it. The accent red flags that this is the gateway OFF the open market onto
// a hidden channel. It carries no "does this code exist" feedback - resolution is
// uniform (see resolveFreq), so the strip never leaks whether a code is real.
func (m model) freqEntryView(w int) string {
	// The accented label is fixed; the input echoes after it. Narrow shortens the label
	// so the input still has room. The help line is width-clamped (truncVisible) so it
	// never overflows a slim terminal.
	label := stRed.Render(glyphOnAir + " PRIVATE FREQ ▸ ")
	help := "enter a private band's frequency code · ⏎ tunes in · esc returns to OPEN MARKET"
	if m.narrow() {
		label = stRed.Render(glyphOnAir + " FREQ ▸ ")
		help = "type a freq code · ⏎ tune · esc cancels"
	}
	return "  " + label + m.freqIn.View() + "\n" +
		"  " + stDim.Render(truncVisible(help, w-2))
}

// filterLine renders the active filter strip under the band heading: the live
// name-filter input (while open), the applied substring + match count (e.g.
// "filter: qwen  (3/240)"), and the lit quick toggles (free / conf / on-air). It
// is the band browser's mirror of the /bands web tuner chips so the CLI + web
// narrow the same way. matched/total drive the "(n/total)" count.
func (m model) filterLine(matched, total int) string {
	var parts []string
	if m.filterMode {
		// The live input: typing filters as you go. The label + the textinput View()
		// (cursor + echoed text) so it is obvious WHERE the filter text lands.
		parts = append(parts, stKey.Render("filter ▸ ")+m.filterIn.View())
	} else if q := strings.TrimSpace(m.filterApplied); q != "" {
		parts = append(parts, stDim.Render("filter: ")+stKey.Render(q))
	}
	// Lit quick toggles (only the on ones, to stay tight).
	var toggles []string
	if m.fFree {
		toggles = append(toggles, stLive.Render("free-now"))
	}
	if m.fConf {
		toggles = append(toggles, stGold.Render("conf"))
	}
	if m.fOn {
		toggles = append(toggles, stRed.Render("on-air"))
	}
	if m.fQuant != "" {
		// Named, not a bare "quant" lamp: WHICH one is the whole content of this filter,
		// and an operator who cannot see it cannot tell a narrowed dial from an empty one.
		toggles = append(toggles, stKey.Render(m.fQuant))
	}
	if len(toggles) > 0 {
		parts = append(parts, stDim.Render("["+strings.Join(toggles, " ")+"]"))
	}
	// The match count, always, so it is clear how much the filter narrowed the list.
	parts = append(parts, stDim.Render(fmt.Sprintf("(%d/%d)", matched, total)))
	return "  " + strings.Join(parts, "  ")
}

// ctxCell renders a context window honoring the estimated flag: a detected window is
// solid ("131k"), the estimated default is dim + "~" ("~32k") - a guess, labeled as one.
func ctxCell(ctx int, estimated bool) string {
	if ctx <= 0 {
		return stDim.Render("-")
	}
	if estimated {
		return stDim.Render("~" + fmtCtx(ctx))
	}
	return stEmber.Render(fmtCtx(ctx))
}

// successCell renders a station's success rate: the REAL EWMA as "NN%" when SEEN,
// else an honest "no data" - never a fabricated percentage (matches the web's rule).
func successCell(rate float64, seen bool) string {
	if !seen {
		return stDim.Render("no data")
	}
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return fmt.Sprintf("%d%%", int(rate*100+0.5))
}

// regionCell renders a coarse region or a dim "-" when absent (mirrors the web's
// em-dash for a missing region; never "??").
func regionCell(region string) string {
	if cr := coarseRegion(region); cr != "" {
		return cr
	}
	return "-"
}

// renderComposer builds an isolated render model because bubbles/textarea copies
// share a private viewport pointer. Calling geometry methods on a View-time copy
// therefore mutates the live editor; using the live viewport directly can also
// retain a stale scroll offset after wrapping. This fresh model is observational.
func renderComposer(input textarea.Model, placeholder, lead string, leadWidth, width, height int) string {
	render := textarea.New()
	render.Prompt = ""
	render.Placeholder = placeholder
	render.ShowLineNumbers = false
	render.SetPromptFunc(leadWidth, func(line int) string {
		if line == 0 {
			return lead
		}
		return strings.Repeat(" ", leadWidth)
	})
	render.FocusedStyle = input.FocusedStyle
	render.BlurredStyle = input.BlurredStyle
	// The isolated model renders the full logical draft; Roger slices the
	// cap-sized cursor window below. Leaving MaxHeight at six would make
	// Bubbles hide the tail before we can select the correct window.
	render.MaxHeight = 0
	render.CharLimit = input.CharLimit
	render.SetWidth(max(leadWidth+1, width))
	contentWidth := max(1, width-leadWidth)
	render.SetHeight(max(1, composerVisualRows(input.Value(), contentWidth)))
	render.SetValue(input.Value())

	line := input.Line()
	col := input.LineInfo().StartColumn + input.LineInfo().ColumnOffset
	for render.Line() > line {
		render.CursorUp()
	}
	render.SetCursor(col)
	render.Cursor.SetMode(cursor.CursorStatic)
	if input.Focused() {
		render.Focus()
	} else {
		render.Blur()
	}
	lines := strings.Split(strings.TrimSuffix(render.View(), "\n"), "\n")
	cursorRow := composerCursorVisualRow(input, contentWidth)
	start := max(0, cursorRow-height+1)
	if start+height > len(lines) {
		start = max(0, len(lines)-height)
	}
	end := min(len(lines), start+max(1, height))
	return strings.Join(lines[start:end], "\n")
}

func (m model) helpView() string {
	// Lead with the few things a new user needs - the two-way radio in one breath.
	start := [][2]string{
		{"0", "AGENT: a small tool-capable agent (dj.md persona) - reads files, runs commands (you confirm)"},
		{"←/→", "switch section: cycle the [0] AGENT … [?] HELP bar (same as pressing its number)"},
		{"↑↓ then enter", "TUNE IN: pick a band, open a channel, chat"},
		{"f", "FILTER the band by name (live) - esc clears, enter keeps it applied"},
		{"t", "YOUR BANDS: switch the dial between OPEN MARKET and your own PRIVATE bands. A private band is hidden from the public list - including from you - so this is where you find it. ⏎ on one whose model runs here opens a DIRECT channel (no broker, no meter); n mints a new code, f clears a revoked row"},
		{"~", "PRIVATE FREQ: enter SOMEONE ELSE'S frequency code to tune onto their hidden band - esc returns to OPEN MARKET"},
		{"b", "BAND CARD: every setting for the band under the cursor in one place - on air, public/private, its dial, its price, what was detected about it, your spend caps"},
		{"s", "SORT cycle (strongest / cheapest / fastest / most-stations)"},
		{"F/C/O", "filters: free-now / confidential / on-air"},
		{"Q", "QUANT filter: show only one compression label (Q4_K_M, IQ4_XS, 4bit…). Two stations serving the same model name are not serving the same weights - only labels actually on air are offered"},
		{"m  ·  alt+m", "MINIMIZE to the dense compact windowshade · alt+m (or /compact) works from anywhere, even mid-chat"},
		{"z", "SCREENSAVER: zone out to Ping's world (fullscreen, any key wakes) · also /ping"},
		{"w", "WEB CONSOLE: open this station's browser console (it no longer auto-opens at launch)"},
		{"esc (in a channel)", "disconnect - leave the channel, back to the band"},
		{"q (browsing)", "quit RogerAI"},
	}
	cmds := [][2]string{
		{"/search", "re-scan the band for stations (CLI: roger search)"},
		{"/connect (enter)", "tune in to the selected station (CLI: roger use)"},
		{"/chat (c · tab)", "open the CHANNEL session with the connected model"},
		{"/share [off]", "SHARE: the provider table - flip your models on/off air"},
		{"/login", "link GitHub - only needed to EARN (CLI: roger login)"},
		{"/balance · /topup", "your wallet balance · add funds (CLI: roger balance)"},
		{"/limits", "see + edit your per-model spend maxes"},
		{"/grant [create <name>]", "private free keys for your bots/family"},
		{"/confidential", "toggle: route only to TEE-attested nodes"},
		{"/endpoint · /config", "endpoint + key · broker/identity"},
		{"/support", "open rogerai.fm - community + Discord (CLI: roger support)"},
		{"/ping (/zen · z)", "SCREENSAVER: Ping's world fullscreen (CLI: roger --ping) - any key wakes"},
		{"/help · /quit", "this · quit RogerAI"},
	}
	var b strings.Builder
	// Ping rests here, on air and standing by - an intentional home for the mascot
	// (not just empty/error states). Body volt, the eye the one live-red glyph. COMPACT
	// freezes Ping to the canonical standing-by pose (no bob) per reduced-motion.
	pf := anim(m.frame)
	if m.compact {
		pf = frozenFrame
	}
	ping := renderPing(pingIdleFrames[pf%len(pingIdleFrames)], "•")
	b.WriteString("\n" + indentBlock(ping, "    ") + "\n")
	b.WriteString("    " + stPingDim.Render("Ping · on air, go ahead") + "\n\n")
	b.WriteString(stBrand.Render("  start here") + stDim.Render("  (a two-way radio for Local Models)") + "\n\n")
	for _, c := range start {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-20s", c[0])) + stDim.Render(c[1]) + "\n")
	}
	b.WriteString("\n" + stBrand.Render("  all commands") + stDim.Render("  (each is also a `roger <cmd>` you can script)") + "\n\n")
	for _, c := range cmds {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-24s", c[0])) + stDim.Render(c[1]) + "\n")
	}
	b.WriteString("\n  " + stDim.Render("in CHANNEL: /model /clear /save /system <p> /cost /endpoint /support /disconnect /quit") + "\n")
	b.WriteString("  " + stDim.Render("sections: ") + stKey.Render("←/→") + stDim.Render(" switch section (cycle the [0]…[?] bar) · ") +
		stKey.Render("[2]") + stDim.Render(" SHARE · ") +
		stKey.Render("tab") + stDim.Render(" peeks at the band from a channel") + "\n")
	b.WriteString("  " + stDim.Render("view: ") + stKey.Render("m") +
		stDim.Render(" toggles COMPACT - the calm, dense windowshade · ") + stKey.Render("alt+m") +
		stDim.Render(" (or ") + stKey.Render("/compact") + stDim.Render(") minimizes from anywhere, even mid-chat") + "\n")
	b.WriteString("  " + stDim.Render("vim extras (also work): ") + stKey.Render("j/k") + stDim.Render(" move · ") +
		stKey.Render("c") + stDim.Render(" channel · ") + stKey.Render("l/h") + stDim.Render(" inspect/back") + "\n")

	// GLOSSARY (audit #6): the radio identity stays - this teaches it in plain language
	// instead of renaming anything. The jargon map first, then one plain line per signal
	// factor so the raw "signal 82 = supply 15 · speed 14 · …" breakdown is interpretable.
	glossary := [][2]string{
		{"band", "a model (e.g. gpt-oss-20b) - one band groups every station serving it"},
		{"station", "a provider: someone's machine serving that model"},
		{"on air", "serving right now (a station is live + taking requests)"},
		{"confidential", "hardware-private (TEE): route only to attested secure nodes"},
		{"frequency code", "a private-band key - tune onto a hidden band instead of the open market"},
	}
	signalGloss := [][2]string{
		{"supply", "how many healthy stations are on the band"},
		{"speed", "tokens/sec throughput"},
		{"latency", "response time (lower is better)"},
		{"verified", "stations passing the broker's live serving probe"},
		{"success", "historical share of requests that completed"},
		{"trust", "operator reputation"},
	}
	b.WriteString("\n" + stBrand.Render("  glossary") + stDim.Render("  (the radio words, in plain language)") + "\n\n")
	for _, g := range glossary {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-16s", g[0])) + stDim.Render(g[1]) + "\n")
	}
	b.WriteString("\n  " + stDim.Render("signal X/100 breaks down into six factors:") + "\n")
	for _, g := range signalGloss {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-16s", g[0])) + stDim.Render(g[1]) + "\n")
	}

	lockup := "rogerai"
	if helpVersion != "" {
		lockup += " " + helpVersion
	}
	b.WriteString("\n  " + stDim.Render(lockup+" · ↑↓ scroll · esc back") + "\n")
	return b.String()
}
