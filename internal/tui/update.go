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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/session"
)

// Update wraps the message dispatch with a transcript-scroll refresh, so any handler
// that appends to the CHANNEL or AGENT transcript (a reply, an agent event, a system
// line) re-sizes + re-feeds its viewport and auto-sticks to the bottom (only when the
// user is already there) without every return site having to remember to.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Textarea geometry must live on the editable models, not only on temporary View
	// copies. Bubbles uses the stored width while processing bursts/pastes; leaving its
	// default width in place can preserve Value() yet paint an empty viewport.
	m = m.syncComposerGeometry()
	prevStatus := m.status
	tm, cmd := m.update(msg)
	if mm, ok := tm.(model); ok {
		// Stamp the frame whenever the status line CHANGES, so the tick can auto-dismiss it as
		// a transient toast (A.6.6) - this central stamp avoids touching the ~50 assignment
		// sites. A cleared status ("") needs no stamp.
		if mm.status != prevStatus && mm.status != "" {
			mm.statusFrame = mm.frame
		}
		mm = mm.syncComposerGeometry()
		return mm.refreshScroll(), cmd
	}
	return tm, cmd
}

// enterPingWorld stashes the current mode and drops into the fullscreen Ping World
// screensaver - the very same world `roger --ping` runs (pingWorldModel). After the first
// frame it advances on the calm pingWorldTick (worldTickMs), not the interactive tick, and
// any key wakes back to prevMode (onKey's intercept).
func (m model) enterPingWorld() (tea.Model, tea.Cmd) {
	m.prevMode = m.mode
	m.mode = modePingWorld
	// Blur the active text input so its blink Cmd-chain stops firing into the dropped-msg
	// void while the screensaver owns the tick; the wake re-focuses it to re-arm the blink.
	// Blurring both is harmless - only the focused one was animating.
	m.chatIn.Blur()
	m.cmd.Blur()
	m.world = pingWorldModel{w: m.width, h: m.height, seed: int(time.Now().UnixNano() & 0x7fffffff),
		debut: true, data: buildWorldData(m.bands)} // seed the LIVE signal towers from the current on-air bands
	return m, m.kickTick() // one fresh chain; the tickMsg handler switches it to pingWorldTick
}

func (m model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// SCREENSAVER WAKE: while the Ping World is up, ANY key (even ctrl+c) just wakes us back
	// to where we came from - it never quits RogerAI or leaks the keystroke into the prior
	// mode. A real quit then takes a second ctrl+c from the restored view (the on-air guard).
	if m.mode == modePingWorld {
		m.mode = m.prevMode
		m.status = stDim.Render("welcome back - the band's still here")
		// Re-focus + re-arm the cursor blink for whichever input we woke back into (the
		// blink Cmd-chain died while the world owned the tick), batched with the normal beat.
		switch m.prevMode {
		case modeChat:
			return m, tea.Batch(m.kickTick(), m.chatIn.Focus())
		case modeCommand:
			return m, tea.Batch(m.kickTick(), m.cmd.Focus())
		}
		return m, m.kickTick() // resume the normal beat (fresh chain; the pingWorld chain dies)
	}
	// The quit-confirm modal owns every key while open (answer the on-air guard).
	if m.mode == modeQuitConfirm {
		switch k.String() {
		case "y", "Y", "enter":
			return m.quitNow()
		default: // n/N/esc/anything else - stay on air, return to where we were
			m.mode = m.quitReturn
			m.status = stDim.Render("still on air - kept sharing")
			return m, nil
		}
	}
	// Ctrl+C is a global quit, intercepted everywhere so the on-air guard can fire
	// (otherwise a text-input mode would swallow it). q/esc stay mode-specific below.
	if k.String() == "ctrl+c" {
		return m.requestQuit()
	}
	// alt+m is the typing-SAFE global minimize: it toggles the dense compact "windowshade"
	// (the 2000s-MP3-player feel) from ANY mode - including chat / AGENT / the command palette
	// / numeric editors, where plain m is a literal character. Plain m still toggles compact on
	// the nav screens via presetForKey; alt+m (and /compact) make it reachable "from anywhere".
	if k.String() == "alt+m" {
		return m.toggleCompact(), nil
	}
	switch m.mode {
	case modeCommand:
		switch k.String() {
		case "up":
			// Recall a prior run command (Up = older), stashing the in-progress line.
			if v, ok := m.cmdHist.prev(m.cmd.Value()); ok {
				m.cmd.SetValue(v)
				m.cmd.CursorEnd()
			}
			return m, nil
		case "down":
			// Newer command; past the newest restores the stashed in-progress line.
			if v, ok := m.cmdHist.next(); ok {
				m.cmd.SetValue(v)
				m.cmd.CursorEnd()
			}
			return m, nil
		case "enter":
			cmd := strings.TrimSpace(m.cmd.Value())
			m.cmd.SetValue("")
			m.mode = modeBrowse
			// Recorded WITHOUT its secret. `/freq <code>` carries a band's frequency
			// code, and the palette's history is written to disk (history.go) and
			// recalled with ↑ - so typing it here put the secret in a file and left it
			// one keypress from anyone at that terminal, flatly contradicting the
			// promise that a code is shown once and never stored. The verb is kept so
			// recall still works; only the secret is dropped.
			m.cmdHist.add(scrubSecretArgs(cmd))
			return m.run(cmd)
		case "esc":
			m.cmd.SetValue("")
			m.mode = modeBrowse
			return m, nil
		}
		var c tea.Cmd
		m.cmd, c = m.cmd.Update(k)
		return m, c
	case modeChat:
		switch k.String() {
		case "esc":
			// esc DISCONNECTS: drop the channel and return to the band browser. This is
			// "leave this channel", NOT "quit RogerAI" - quitting is a deliberate q from
			// BROWSE (or the on-air guard). tab is the non-destructive peek (below).
			return m.disconnect()
		case "tab":
			// tab is a NON-destructive switch to BROWSE - the channel + endpoint stay
			// live so you can tab back. (esc disconnects; this just looks away.)
			m.mode = modeBrowse
			m.chatIn.Blur()
			m.status = stDim.Render("peeking at the band - the channel stays open · tab/c to return · esc here disconnects")
			return m, nil
		case "shift+tab":
			// shift+tab opens THIS tuned-in model in the [0] AGENT (tool-calling) - the easy,
			// discoverable bridge from TUNE-IN (basic chat) to AGENT the founder asked for, so
			// you don't have to know `/agent`/[0]. The channel stays open underneath.
			m.chatIn.Blur()
			return m.enterAgent()
		case "pgup":
			m.chatVP.PageUp()
			return m, nil
		case "pgdown":
			m.chatVP.PageDown()
			return m, nil
		case "ctrl+u":
			m.chatVP.HalfPageUp()
			return m, nil
		case "ctrl+d":
			m.chatVP.HalfPageDown()
			return m, nil
		case "up":
			if textareaCanMoveUp(m.chatIn) {
				var c tea.Cmd
				m.chatIn, c = m.chatIn.Update(k)
				return m, c
			}
			// Shell-style recall first (the wheel scrolls as REAL mouse events now, so
			// arrows are free to mean history); with nothing to recall, scroll.
			if v, ok := m.chatHist.prev(m.chatIn.Value()); ok {
				m.chatIn.SetValue(v)
				m.chatIn.CursorEnd()
			} else {
				m.chatVP.ScrollUp(1)
			}
			return m, nil
		case "down":
			if textareaCanMoveDown(m.chatIn) {
				var c tea.Cmd
				m.chatIn, c = m.chatIn.Update(k)
				return m, c
			}
			if v, ok := m.chatHist.next(); ok {
				m.chatIn.SetValue(v)
				m.chatIn.CursorEnd()
			} else {
				m.chatVP.ScrollDown(1)
			}
			return m, nil
		case "end":
			m.chatVP.GotoBottom()
			return m, nil
		case "ctrl+p":
			// The PERMS key (founder respec 2026-07-14) - but tool approvals live in
			// the AGENT, not the channel. Point there; Up/Down still recall history.
			m.status = stDim.Render("tool approvals live in the AGENT - shift+tab opens it, then ctrl+p cycles /perms")
			return m, nil
		case "ctrl+n":
			// Recall a NEWER sent message; past the newest it restores the draft.
			if v, ok := m.chatHist.next(); ok {
				m.chatIn.SetValue(v)
				m.chatIn.CursorEnd()
			}
			return m, nil
		case "ctrl+y":
			// Yank the last station reply to the clipboard (OSC 52 + local tool). Plain `y`
			// would type into the channel, so copy is on ctrl+y (and /copy).
			if m.lastReply == "" {
				m.status = stDim.Render("nothing to copy yet · shift+drag to select text")
				return m, nil
			}
			m.status = copiedToast("the last reply") + stDim.Render("  ·  /copy all for the whole session")
			return m, clipboardWrite(m.lastReply)
		case "ctrl+o":
			// Toggle mouse ownership: OFF lets the terminal do native click-drag select+copy
			// (mouse capture and native selection are mutually exclusive); ON restores wheel
			// scrolling + smart drag-copy. Either direction drops any live selection.
			m.mouseOff = !m.mouseOff
			m.smartSel = smartSelState{}
			m.status = mouseStatusLine(m.mouseOff)
			if m.mouseOff {
				return m, tea.DisableMouse
			}
			return m, tea.EnableMouseCellMotion
		case "enter":
			p := strings.TrimSpace(m.chatIn.Value())
			if p == "" || m.connected == nil {
				return m, nil
			}
			m.chatIn.SetValue("")
			// Record the sent line in the recall history (raw text, not the sysPrompt-
			// prefixed turn). Empty sends are filtered above; add() also collapses a repeat
			// of the previous entry and resets the Up/Down cursor to the bottom.
			m.chatHist.add(p)
			// A leading / in-session is a slash command, not a chat turn.
			if strings.HasPrefix(p, "/") {
				return m.runSession(p)
			}
			turn := p
			if m.sysPrompt != "" {
				turn = m.sysPrompt + "\n\n" + p
			}
			m.transcript = append(m.transcript, chatUserBlock(p))
			// Pre-flight: if no station for this band is on air right now, say so in the
			// transcript immediately instead of firing a request the broker will bounce
			// with a 503 the user might never see. (Best-effort: a stale scan still falls
			// through to the real request + its inline error.)
			// A LOCAL channel skips this pre-flight entirely: the check asks the broker's
			// band list whether a STATION is on air, and a direct channel has no station -
			// its model is the local server, which /discover has never heard of. Running it
			// would refuse every turn on a channel that works.
			if m.chatLocalChat == "" && !m.bandOnAir(m.connected.Model) {
				m.transcript = append(m.transcript,
					stRed.Render("✕ ")+stEmber.Render(noStationServing(m.connected.Model)),
					hintTuneOrShare(m.narrow()))
				return m, nil
			}
			m.relaying = true
			m.relayStart = time.Now()
			// Record the user turn into the per-turn context ring (Q4) before it is sent,
			// so an operator handoff can carry the conversation. The flat transcript above
			// stays the render source.
			// THE CONVERSATION SO FAR, taken BEFORE this question joins the ring - so the
			// history is exactly the prior turns and needs no trimming afterwards. (An
			// earlier cut computed it after recordTurn and dropped the last element, which
			// is correct only while that element is guaranteed to be the question: one
			// empty-content filter away from silently dropping a real turn instead.)
			hist := m.chatHistory(m.connected.Model)
			m.recordTurn("user", p, "user", nil, nil)
			// Carry the user's explicit out-price cap for this model (0 -> the default
			// consumer cap applies broker-side); keeps the in-channel chat bounded like use.
			if m.chatLocalChat != "" {
				msgs := make([]harness.Message, 0, len(hist)+1)
				for _, t := range hist {
					msgs = append(msgs, harness.Message{Role: t.Role, Content: t.Content})
				}
				return m, sendChatLocal(m.chatLocalChat, m.chatLocalKey, m.connected.Model, turn, msgs)
			}
			// The tuned row IS a quant, so the stations running a different one are named
			// as exclusions - the broker groups by model alone and would otherwise route
			// this turn to weights the operator did not choose. Same rule the proxy path
			// applies in liveProxyOpts; the booth's own chat used to skip it.
			//
			// Derived from the CONNECTED band, not from m.q - the quote is whatever row
			// was last priced, so an over-limit quote the operator esc'd on another row
			// would otherwise exclude stations that serve this one perfectly well.
			return m, sendChat(m.broker, m.user, m.connected.Model, turn, m.confidentialOnly, m.limits.resolve(m.connected.Model).MaxOut, m.tuneFreq, hist, m.chatExcludes())
		}
		var c tea.Cmd
		m.chatIn, c = m.chatIn.Update(k)
		return m, c
	case modeLog:
		// /log is read-only; any key closes it back to the band browser.
		m.mode = modeBrowse
		return m, nil
	case modeHelp:
		// HELP is a pager now (audit P0: it was taller than most terminals and any
		// key exited, so the top - the part a new user needs - was unreachable).
		// Scroll keys page it; esc/q/enter/? go back; a preset key still jumps.
		switch k.String() {
		case "up", "k":
			m.helpVP.ScrollUp(1)
			return m, nil
		case "down", "j":
			m.helpVP.ScrollDown(1)
			return m, nil
		case "pgup", "ctrl+u":
			m.helpVP.HalfPageUp()
			return m, nil
		case "pgdown", "ctrl+d", " ":
			m.helpVP.HalfPageDown()
			return m, nil
		case "home":
			m.helpVP.GotoTop()
			return m, nil
		case "end":
			m.helpVP.GotoBottom()
			return m, nil
		case "esc", "q", "enter", "?":
			m.mode = modeBrowse
			return m, nil
		}
		if nm, cmd, ok := m.presetForKey(k.String()); ok {
			return nm, cmd
		}
		return m, nil
	case modeConnectConfirm:
		switch k.String() {
		case "enter", "y", "Y":
			return m.openChannel()
		case "d", "D": // toggle the detail block (default screen stays minimal)
			m.showDetail = !m.showDetail
			return m, nil
		default: // esc, n, N, anything else - default DENY
			m.mode = modeBrowse
			m.status = stDim.Render("denied - no channel opened")
			return m, nil
		}
	case modeConnecting:
		// The staged tune-in is brief and self-completing; a key lets an impatient
		// operator skip straight to the channel (enter/space) or back out (esc).
		switch k.String() {
		case "esc", "n", "N":
			m.mode = modeBrowse
			m.status = stDim.Render("cancelled - the endpoint stays bound, no channel opened")
			return m, nil
		default:
			return m.finishConnect()
		}
	case modeOverLimit:
		return m.onOverLimitKey(k)
	case modeLimits:
		return m.onLimitsKey(k)
	case modeShare:
		return m.onShareKey(k)
	case modeBandCard:
		return m.onBandCardKey(k)
	case modeShareEditor:
		return m.onShareEditorKey(k)
	case modeShareSetup:
		return m.onShareSetupKey(k)
	case modeAgent:
		return m.onAgentKey(k)
	case modeLogin:
		return m.onLoginKey(k)
	case modeVoicePreview:
		return m.onVoicePreviewKey(k)
	case modeVoiceBooth:
		return m.onVoiceBoothKey(k)
	case modeListeningPost:
		return m.onListeningPostKey(k)
	case modeShareVoice:
		return m.onShareVoiceKey(k)
	case modeVoicePicker:
		return m.onVoicePickerKey(k)
	case modePrivate:
		return m.onPrivateKey(k)
	case modeBandManage:
		return m.onBandManageKey(k)
	case modeBandMove:
		return m.onBandMoveKey(k)
	case modeBandRevokeConfirm:
		return m.onBandRevokeConfirmKey(k)
	case modeBandRotateConfirm:
		return m.onBandRotateConfirmKey(k)
	case modeBandConfig:
		return m.onBandConfigKey(k)
	case modeBandLabel:
		return m.onBandLabelKey(k)
	case modeBandQuants:
		return m.onBandQuantsKey(k)
	case modeRemoteSession:
		return m.onRemoteSessionKey(k)
	case modeBandDetail:
		// The expanded station log: esc/←/h/i close it back to the list; enter tunes in to
		// the band (the cheapest station), matching the browse Enter. r re-scans.
		switch k.String() {
		case "esc", "left", "h", "i", "q":
			m.mode = modeBrowse
			return m, nil
		case "enter":
			m.mode = modeBrowse
			return m.connect()
		case "r":
			m.status = "re-scanning the band…"
			m.scanErr, m.scanned = false, false
			return m, fetchOffers(m.broker)
		}
		return m, nil
	case modeFreqEntry:
		// PRIVATE FREQUENCY entry: a small input to type/paste a frequency code. enter
		// resolves it off the event loop (the SAME constant-work client.ResolveBand the
		// `roger use --freq` path uses); esc cancels back to the browser. A wrong /
		// nonexistent / empty / off-air code is INDISTINGUISHABLE from "no bands on this
		// freq" - the broker returns the uniform "no station" reply and the freqResolvedMsg
		// handler shows the SAME message for every negative case (no enumeration oracle,
		// no distinct success-vs-miss tell beyond the band list actually populating).
		switch k.String() {
		case "esc":
			m.mode = modeBrowse
			m.freqIn.Blur()
			m.status = stDim.Render("cancelled")
			return m, nil
		case "enter":
			code := strings.TrimSpace(m.freqIn.Value())
			m.freqIn.Blur()
			m.mode = modeBrowse
			// Always resolve through the constant-work path - even an EMPTY code, which the
			// broker hashes to a non-match and answers with the same uniform "no station"
			// reply. We deliberately do NOT short-circuit empty to a "type something" hint:
			// that would be a tell (empty != wrong). Every negative reads identically.
			return m.resolveFreq(code)
		}
		var c tea.Cmd
		m.freqIn, c = m.freqIn.Update(k)
		return m, c
	default: // browse
		// FILTER ENTRY owns every key while open: typing edits the live name filter, esc
		// clears + closes, enter keeps it applied and returns to the list. Handled BEFORE
		// presetForKey + the browse keys so f, m, l, 0, etc. are NEVER stolen mid-filter
		// (the founder's "guard f so it isn't stolen elsewhere"). The filter is also never
		// reachable from the command palette / chat / editors, which own their own keys
		// and don't fall through to this browse default.
		if m.filterMode {
			switch k.String() {
			case "esc":
				// esc clears + closes the filter (back to the full list).
				m.filterMode = false
				m.filterIn.Blur()
				m.filterIn.SetValue("")
				m.filterApplied = ""
				m.clampBrowse()
				m.status = stDim.Render("filter cleared")
				return m, nil
			case "enter":
				// enter keeps the filter applied and returns to the list (cursor navigable).
				m.filterMode = false
				m.filterIn.Blur()
				m.filterApplied = strings.TrimSpace(m.filterIn.Value())
				m.clampBrowse()
				return m, nil
			}
			// Any other key edits the buffer; the filter applies LIVE as you type.
			var c tea.Cmd
			m.filterIn, c = m.filterIn.Update(k)
			m.filterApplied = strings.TrimSpace(m.filterIn.Value())
			m.clampBrowse()
			return m, c
		}
		// The preset bank: 1 TUNE IN · 2 SHARE · 3 CONFIG · L LOGIN · ? HELP. Handled
		// first so the always-visible top bar's buttons jump straight to their mode.
		if nm, cmd, ok := m.presetForKey(k.String()); ok {
			return nm, cmd
		}
		// The PRIVATE half of [1] TUNE IN owns its own movement + enter (tune_private.go).
		// It hands back ok=false for the keys that belong to the whole TUI, so those keep
		// working from here; the market-only keys stop at its door.
		if m.tuneTab == tabPrivate {
			if nm, cmd, ok := m.onPrivateTabKey(k); ok {
				return nm, cmd
			}
		}
		switch k.String() {
		case "b", "B":
			// THE BAND CARD: every setting for the band under the cursor, in one place.
			// The same key on every list that shows a band, so it is learned once.
			if bd, ok := m.selectedBand(); ok {
				return m.openBandConfig(bd.model, modeBrowse)
			}
			return m, nil
		case "t", "T":
			// t = switch halves of the dial: OPEN MARKET ⇄ your own PRIVATE bands. The
			// founder's ask - a private band was invisible to the operator who minted it,
			// because /discover hides private nodes with no owner exemption.
			return m.enterPrivateTab()
		case "q":
			return m.requestQuit()
		case "z":
			// z = zone out: drop into the fullscreen Ping World screensaver (any key wakes).
			return m.enterPingWorld()
		case "w":
			// w = web console: open this run's browser node console on demand - it no
			// longer auto-opens at launch (founder respec 2026-07-14).
			m.status = stDim.Render(m.openConsole())
			return m, nil
		case "/", ":":
			m.mode = modeCommand
			m.cmd.Focus()
			return m, textinput.Blink
		case "f":
			// f opens the live name filter (the headline scale fix). It seeds from any
			// already-applied filter so f re-opens to edit, not to clear.
			m.filterMode = true
			m.filterIn.SetValue(m.filterApplied)
			m.filterIn.CursorEnd()
			m.filterIn.Focus()
			return m, textinput.Blink
		case "s", "S":
			// s/S BOTH cycle the sort dial (strongest / cheapest / fastest / most-stations),
			// mirroring the /bands web page. (s used to jump to SHARE, but that's confusing
			// next to [2]/the SHARE page - per the founder, s is just sort now.) The sticky
			// cursor keeps the selected band put across the re-sort.
			m.sortMode = (m.sortMode + 1) % sortCount
			m.clampBrowse()
			m.status = stDim.Render("sort: " + sortLabel(m.sortMode))
			return m, nil
		case "F":
			// quick toggle: only bands with a FREE-now station.
			m.fFree = !m.fFree
			m.clampBrowse()
			return m, nil
		case "C":
			// quick toggle: only confidential / verified (lineage) bands.
			m.fConf = !m.fConf
			m.clampBrowse()
			return m, nil
		case "O":
			// quick toggle: only bands with a station on air.
			m.fOn = !m.fOn
			m.clampBrowse()
			return m, nil
		case "U":
			// HIDE CURATED: one keypress, joining the F/C/O family. U for Upstream - the
			// mark it hides is »provider, proxied commercial supply. Shown by default
			// (founder ruling); while hidden, nothing may silently route to a proxy.
			m.fNoCurated = !m.fNoCurated
			m.clampBrowse()
			// The ambient footer is a tick-time snapshot; refresh it NOW or the count line
			// still advertises the supply the operator just hid, until the next tick.
			m.status = m.ambientStatus()
			return m, nil
		case "Q":
			// CYCLE the quant filter: off -> each quant on the dial -> off. It joins the
			// F/C/O family deliberately - one keypress, no input box - because splitting
			// bands by quant made the list longer and the cost has to be payable with a
			// key an operator already has a finger on.
			return m.cycleQuantFilter(), nil
		case "~":
			// PRIVATE FREQUENCY entry. `~` is the dial-tune mnemonic (a radio dial sweep),
			// deliberately NOT `f` (the name-filter) so the two never collide. It opens a
			// small dedicated input (modeFreqEntry) to ENTER a frequency code; this is the
			// discoverable affordance taught in the footer hint ("~ private freq"). On a
			// valid private band the header flips to PRIVATE FREQ; esc returns to OPEN MARKET.
			m.mode = modeFreqEntry
			m.freqIn.SetValue("")
			m.freqIn.CursorEnd()
			m.freqIn.Focus()
			m.status = stDim.Render("private freq · esc cancels")
			return m, textinput.Blink
		case "v", "V":
			// v = drill into THE DJ BOOTH (the shared voices lineup), the same target as the dim
			// "also on air: N voices ▸ [v]" footnote. The Booth is a CHILD screen of THE BAND
			// (esc returns); voice never sits on the dial as a peer of the LLM bands. NO-OP when
			// no voice is on air (the footnote/affordance is absent then), so `v` never lands on
			// an empty voice screen.
			return m.enterBooth()
		case "p", "P":
			// p = drill into BASE STATION (your private side of the dial: remote agent
			// sessions + private bands), the same target as the "base station ▸ [p]" footnote.
			// A CHILD screen of THE BAND (esc returns), login-gated. Mirrors [v] the DJ BOOTH.
			return m.enterPrivate()
		case "esc":
			// esc clears a tuned PRIVATE frequency back to OPEN MARKET (re-scan the public
			// band). With no freq tuned it is a harmless no-op (browse has no other esc use).
			if m.tuneFreq != "" {
				m.tuneFreq, m.tuneFreqLabel = "", ""
				m.status = stDim.Render("back to ") + stKey.Render("OPEN MARKET")
				return m, fetchOffers(m.broker)
			}
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.caratFrame = m.frame // ease the cursor in (caratGutter)
			}
			m.syncSelected() // remember the band, so a re-sort keeps the cursor on it
			m.scrollBrowse()
			return m, m.kickTick() // ONE fresh chain so rapid up/down never stacks parallel loops (dial glides)
		case "down", "j":
			if m.cursor < len(m.visibleBands())-1 { // navigate the FILTERED + SORTED view
				m.cursor++
				m.caratFrame = m.frame // ease the cursor in (caratGutter)
			}
			m.syncSelected() // remember the band, so a re-sort keeps the cursor on it
			m.scrollBrowse()
			return m, m.kickTick() // ONE fresh chain so rapid up/down never stacks parallel loops (dial glides)
		case "enter":
			// Enter on the band you are ALREADY connected to jumps straight into the open
			// channel (no re-tune, no staged sequence) - the connected row is a toggle:
			// Enter opens it, d (below) disconnects it. Enter on any other band tunes in.
			if m.connected != nil && m.cursorOnConnected() {
				m.mode = modeChat
				m.chatIn.Focus()
				m.status = stGold.Render(channelGlyph(m.connected)+" ") + stLive.Render("back on channel ") + m.connected.NodeID
				return m, textinput.Blink
			}
			return m.connect()
		case "i":
			// Expanded per-station view (the QSL equivalent): every station's real metrics
			// + the signal-term breakdown for the band under the cursor. esc/i closes.
			// i is the ONE inspect key: right/l were removed so arrow-right stays section
			// navigation (the preset cycle), not a surprise panel-open for newcomers.
			vis := m.visibleBands()
			if len(vis) == 0 {
				return m, nil
			}
			cur := m.cursor
			if cur < 0 {
				cur = 0
			}
			if cur >= len(vis) {
				cur = len(vis) - 1
			}
			m.detailBand = vis[cur]
			m.mode = modeBandDetail
			m.status = stDim.Render("station log - every station on ") + stKey.Render(m.detailBand.model) + stDim.Render(" · esc/← back · enter tunes in")
			return m, nil
		case "d":
			// Disconnect FROM THE LIST: if connected, d drops the channel right here so the
			// user can see + toggle what is connected without entering it first (the
			// founder's "disconnect should be doable from the tune-in list"). The band stays
			// in the list as a tunable station (sticky), so Enter re-tunes it.
			if m.connected != nil {
				return m.disconnect()
			}
			m.status = stDim.Render("nothing connected to disconnect - enter tunes in")
			return m, nil
		case "c", "tab":
			if m.connected != nil {
				m.mode = modeChat
				m.chatIn.Focus()
				return m, textinput.Blink
			}
		case "?":
			m.mode = modeHelp
			m.helpVP.GotoTop()
		case "r":
			m.status = "re-scanning the band…"
			m.scanErr, m.scanned = false, false // back to the loading pose while we retune
			return m, fetchOffers(m.broker)
		case "u", "x":
			// The update banner's keys (upgrade now / restart / hide) - only when a
			// notice is showing; otherwise the keys stay free for future browse use.
			if nm, cmd, handled := m.onUpgradeKey(k.String()); handled {
				return nm, cmd
			}
		}
	}
	return m, nil
}

// runSession dispatches an in-CHANNEL slash command (the pi.dev-style session
// harness). It is a clean dispatch so deeper agentic tool-use can be added later;
// for now it covers re-tune, transcript, system prompt, cost, privacy, endpoint,
// help, and leave. Anything unrecognized is echoed as a hint, never sent as chat.
func (m model) runSession(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	cmd := strings.TrimPrefix(fields[0], "/")
	arg := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	sysLine := func(s string) {
		m.transcript = append(m.transcript, stDim.Render("· ")+stDim.Render(s))
	}
	switch cmd {
	case "model", "tune", "retune":
		// re-tune: drop back to the band browser to pick a new channel.
		m.mode = modeBrowse
		m.chatIn.Blur()
		m.status = stDim.Render("pick a band, enter to re-tune (the channel stays open until you do)")
		return m, nil
	case "clear":
		m.transcript = nil
		m.lastReply = ""                 // cleared transcript -> nothing left to copy
		m.msgInFrom, m.msgInFrame = 0, 0 // drop any pending message-in reveal
		m.sessCost = 0
		m.sessTokensIn, m.sessTokensOut = 0, 0 // a cleared transcript zeroes the running ↑↓ totals too
		sysLine("transcript cleared")
		return m, nil
	case "save":
		// save is a labeled local action: the transcript already lives in-memory;
		// we surface where it would write (no disk I/O from the TUI by design).
		sysLine("session has " + fmt.Sprintf("%d", len(m.transcript)) + " lines (kept in-memory this session)")
		return m, nil
	case "system":
		if arg == "" {
			if m.sysPrompt == "" {
				sysLine("no system prompt set · /system <prompt> to set one")
			} else {
				sysLine("system: " + m.sysPrompt)
			}
			return m, nil
		}
		m.sysPrompt = arg
		sysLine("system prompt set · prepended to each turn")
		return m, nil
	case "cost":
		sysLine("session cost so far: " + dollars(m.sessCost) + " · balance " + m.balDollars())
		return m, nil
	case "stats", "detail":
		// Toggle the verbose per-turn footer: subsequent replies also show the locked
		// price in/out alongside the always-on tokens/t-s/latency/cost line.
		m.showStats = !m.showStats
		if m.showStats {
			sysLine("stats ON · new replies show price in/out under the tokens · t/s · time · cost line")
		} else {
			sysLine("stats off · replies show the compact tokens · t/s · time · cost line")
		}
		return m, nil
	case "confidential", "conf":
		m.confidentialOnly = !m.confidentialOnly
		if m.confidentialOnly {
			sysLine("confidential-only ON · routing only to TEE-attested nodes")
		} else {
			sysLine("confidential-only off")
		}
		return m, nil
	case "endpoint", "ep":
		if m.endpoint == "" {
			sysLine("no endpoint yet")
			return m, nil
		}
		sysLine("endpoint " + m.endpoint + " · key " + m.apikey + " · model " + m.connected.Model)
		sysLine("/connect for paste-ready opencode/env snippets (auto-copied)")
		return m, nil
	case "connect", "conn":
		if m.endpoint == "" || m.connected == nil {
			sysLine("no endpoint yet - tune into a channel first")
			return m, nil
		}
		base, key, mdl := m.endpoint, m.apikey, m.connected.Model
		sysLine("CONNECT - point any OpenAI-compatible agent (opencode, a local bot) at this channel:")
		sysLine("    base url   " + base)
		sysLine("    api key    " + key)
		sysLine("    model      " + mdl)
		sysLine("    opencode   OPENAI_BASE_URL=" + base + " OPENAI_API_KEY=" + key + " opencode")
		sysLine("    ✓ export block copied to your clipboard")
		return m, clipboardWrite(connectExport(base, key, mdl))
	case "copy", "y":
		target, label := m.lastReply, "the last reply"
		if strings.EqualFold(arg, "all") {
			target, label = m.transcriptText(), "the transcript"
		}
		if strings.TrimSpace(target) == "" {
			sysLine("nothing to copy yet")
			return m, nil
		}
		sysLine("✓ copied " + label + " to the clipboard")
		m.status = copiedToast(label) // the same prominent toast as ctrl+y
		return m, clipboardWrite(target)
	case "mouse":
		m.mouseOff = !m.mouseOff
		m.smartSel = smartSelState{}
		sysLine(ansi.Strip(mouseStatusLine(m.mouseOff)))
		if m.mouseOff {
			return m, tea.DisableMouse
		}
		return m, tea.EnableMouseCellMotion
	case "agent":
		// /agent: jump straight to the AGENT on THIS channel's model (a shortcut - enterAgent
		// resolves the open channel, so the agent runs on the band you're tuned in to). esc
		// returns; [0] also opens it.
		return m.enterAgent()
	case "ping", "zen":
		// /ping (alias /zen): drop into the fullscreen Ping World screensaver - the very
		// same world `roger --ping` runs. Any key wakes back to this channel.
		return m.enterPingWorld()
	case "compact", "min", "minimize":
		// /compact (/min): minimize to the dense windowshade from a channel without losing
		// your typing - the same toggle as alt+m / m. Run it again (or m) to expand.
		return m.toggleCompact(), nil
	case "support":
		// Opens the site (community + Discord); self-gated on an interactive TTY, URL
		// printed as the fallback.
		openURL(supportURL)
		sysLine("support: " + supportURL + " · community + Discord on the site")
		return m, nil
	case "webui", "console":
		// /webui: open this run's browser node console on demand (same as `w` in BROWSE).
		sysLine(m.openConsole())
		return m, nil
	case "help", "h", "?", "commands":
		// Keep this listing in lock-step with what runSession actually accepts (incl. the
		// aliases), so no real command is hidden from /? (the short help; /help + /commands alias it).
		sysLine("/agent (run the agent on this model) · /model (/tune /retune) · /clear · /save · /system <p> · /cost · /stats (/detail) · /confidential (/conf)")
		sysLine("/connect (/conn) · /endpoint (/ep) · /copy (/y) [all] · /mouse · /compact (/min · alt+m) · /ping (/zen) · /webui (/console) · /support · /disconnect (/leave /dc) · /quit (/q) · /? (/help /h /commands)")
		sysLine("copy: DRAG to select any text (native) · ctrl+y last reply · /copy all  ·  scroll: PgUp/PgDn · arrows · ctrl+o for wheel")
		sysLine("esc or /disconnect leaves this channel · /quit exits RogerAI · tab peeks at the band")
		return m, nil
	case "disconnect", "leave", "dc":
		// Explicit "leave this channel" - same as esc. Returns to the band browser.
		return m.disconnect()
	case "quit", "q":
		// /quit in a CHANNEL means leave the CHANNEL (disconnect), not quit the whole
		// app - quitting RogerAI is a deliberate q from BROWSE / the on-air guard. If a
		// share is live, fall through to the quit path so the on-air guard can fire.
		if m.onAirCount() > 0 {
			return m.requestQuit()
		}
		return m.disconnect()
	default:
		sysLine("unknown: /" + cmd + " · /? for commands")
		return m, nil
	}
}

// run handles a slash command.
func (m model) run(cmd string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return m, nil
	}
	switch fields[0] {
	case "search", "s":
		m.status = "re-scanning the band…"
		m.scanErr, m.scanned = false, false
		return m, fetchOffers(m.broker)
	case "connect", "tune":
		return m.connect()
	case "chat":
		if m.connected != nil {
			m.mode = modeChat
			m.chatIn.Focus()
			return m, textinput.Blink
		}
		m.status = "tune in to a station first (Enter)"
	case "balance", "bal":
		if !m.loggedInState() {
			m.status = stDim.Render("not logged in - ") + stKey.Render("type /login") + stDim.Render(" to use your wallet")
			return m, nil
		}
		if m.haveBal && m.balance <= 0 {
			m.status = stEmber.Render("balance empty") + stDim.Render(" - ") + stKey.Render("/topup") + stDim.Render(" to add funds")
		}
		return m, fetchBalance(m.broker, m.user)
	case "limits", "limit":
		m.enterLimits()
		return m, nil
	case "config", "cfg":
		m.status = fmt.Sprintf("broker %s · user %s  (roger config set broker <url>)", m.broker, m.user)
	case "confidential", "conf":
		m.confidentialOnly = !m.confidentialOnly
		if m.confidentialOnly {
			m.status = stGold.Render("◆ confidential-only ON") + " - routing only to TEE-attested nodes"
		} else {
			m.status = "confidential-only off"
		}
	case "freq", "f":
		// /freq <code> tunes the band browser to a PRIVATE frequency (esc returns to
		// OPEN MARKET). Bare /freq with an active freq clears it; bare with none prompts.
		// NOTE: /freq, not the f filter key - the filter stays on its own key.
		return m.doFreq(strings.TrimSpace(strings.TrimPrefix(cmd, fields[0])))
	case "share":
		return m.doShare(fields[1:])
	case "login", "logout":
		// Both open the same confirmable [L] panel: logged out it offers the login
		// prompt, logged in it offers the logout confirm. Neither acts on its own.
		return m.doLogin()
	case "topup", "add":
		return m.doTopup(fields[1:])
	case "grant":
		return m.doGrant(fields[1:])
	case "endpoint", "ep":
		if m.connected == nil {
			m.status = "tune in first to get an endpoint"
		}
	case "help", "h":
		m.mode = modeHelp
		m.helpVP.GotoTop()
	case "log", "logs":
		m.mode = modeLog
	case "support":
		// Opens the site (where the Discord/community link lives). openURL self-gates on
		// an interactive TTY, so this never hijacks a browser headless; the URL is shown
		// either way as the fallback.
		openURL(supportURL)
		m.status = stDim.Render("support: ") + stKey.Render(supportURL) + stDim.Render(" - community + Discord on the site")
	case "webui", "console":
		// /webui: open this run's browser node console on demand (same as the `w` key).
		m.status = stDim.Render(m.openConsole())
	case "ping", "zen":
		// fullscreen Ping World screensaver from the command palette (any key wakes).
		return m.enterPingWorld()
	case "compact", "min", "minimize":
		// minimize to the dense windowshade from the palette (same as alt+m / m).
		return m.toggleCompact(), nil
	case "quit", "q":
		return m.requestQuit()
	default:
		m.status = "unknown: /" + fields[0] + "  (try /help)"
	}
	return m, nil
}

// doShare opens the k9s-style provider table (modeShare) instead of silently
// auto-committing a share - the founder's "it just auto-selected and I couldn't
// tell which model" complaint. It detects the local models, lists them with an
// ON-AIR / OFF-AIR status + price + live metrics, and lets the user flip any model
// on/off air from a highly visible cursor. `/share off` still stops everything;
// `/share <model>` is a quick shortcut that flips one model on air directly.
func (m model) doShare(args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 && (args[0] == "off" || args[0] == "stop") {
		m.stopAllShares()
		m.status = stDim.Render("off air - you stopped sharing")
		return m, nil
	}
	// ASYNC: enter the provider table in a LOADING pose IMMEDIATELY and fire detection
	// off the event loop. detectShares used to run synchronously here and block every
	// keystroke for seconds on a busy host (120+ open ports to probe); now the user
	// sees the scanning indicator at once and the sharesDetectedMsg lands the rows.
	m.mode = modeShare
	// RE-ENTRY KEEPS THE TABLE. The rows from the last scan live in the shared controller
	// and are still perfectly good to look at; only their freshness is in question. So a
	// return visit renders them at once and re-detects BEHIND them, folding changes in
	// when the result lands - the loud full-screen scan is only honest on the first open,
	// when there is genuinely nothing to show.
	if len(m.shareRows) > 0 {
		m.shareLoading = false
		m.shareRefreshing = true
		m.setupOnEmpty = false // rows exist; an empty re-detect must not yank to the wizard
		m.shareRescan = false
		m.setupHint = ""
		m.sharePending = ""
		if len(args) > 0 {
			m.sharePending = args[0]
		}
		m.status = stDim.Render("refreshing local models…")
		return m, detectSharesCmd(m.shareUp, m.shareKey)
	}
	m.shareLoading = true
	m.setupOnEmpty = true // the initial open: an empty scan drops into the guided wizard
	m.shareRescan = false
	m.setupHint = ""
	m.sharePending = ""
	if len(args) > 0 {
		m.sharePending = args[0] // `/share <model>` shortcut: flip it on air after detect
	}
	m.status = stDim.Render("scanning the band for local models…")
	return m, detectSharesCmd(m.shareUp, m.shareKey)
}

// onSharesDetected folds an async detection result into the provider table: it
// clears the loading pose, builds the rows, applies a pending `/share <model>`
// shortcut, and - only on the initial open (setupOnEmpty) - drops into the guided
// setup wizard when nothing was found. An empty re-detect from inside the table
// (setupOnEmpty=false) stays on the table with a clear note rather than yanking the
// user into the wizard mid-list.
func (m model) onSharesDetected(found []detect.Found, needKey []string) (tea.Model, tea.Cmd) {
	// Was this a LOUD scan (the pose on screen) or a QUIET refresh behind a live table?
	// The fold differs: a quiet result must not move the operator anywhere, must not
	// reset their cursor, and an empty one must not erase rows that were on screen.
	quiet := m.shareRefreshing && !m.shareLoading
	m.shareLoading = false
	m.shareRefreshing = false
	if quiet && len(found) == 0 {
		// Nothing answered THIS probe. The rows on screen are the last good scan, and
		// blanking them over a transient miss would be exactly the abrupt clear this
		// path exists to avoid. Say it quietly; r re-scans loudly if the operator cares.
		m.status = stDim.Render("re-scan found nothing new - keeping the last scan (r re-scans)")
		return m, nil
	}
	if len(found) == 0 {
		if m.setupOnEmpty {
			// GUIDED FALLBACK: nothing usable detected -> the in-TUI setup wizard (pick a
			// tool for a one-liner, or paste a URL we verify), not a dead-end status line.
			// When a server IS there but key-protected (401/403), drop straight onto the
			// paste row with its URL pre-filled and ask for the key - the most likely fix.
			nm := m.enterShareSetup()
			if len(needKey) > 0 {
				nm.setupCursor = len(setupOptions) - 1 // the "Other - paste a URL" row
				nm.setupPaste = needKey[0]
				nm.setupAwaitKey = true
				nm.status = stDim.Render(needKey[0] + " needs an API key - type it and press enter")
				return nm, nil
			}
			if m.shareRescan {
				note := m.setupHint
				if note == "" {
					note = "still nothing on the defaults / your open ports - give it a moment, or paste the URL below"
				}
				nm.setupErr = note
			}
			return nm, nil
		}
		m.status = stEmber.Render("! still nothing on the defaults / your open ports - press r to re-scan, or start a local LLM")
		return m, nil
	}
	// KEEP THE OPERATOR'S PLACE. The catalog rebuild can insert or drop rows above the
	// cursor; re-finding the model it sat on is what makes a refresh feel like a diff
	// rather than a reset.
	curModel := ""
	if m.shareCursor >= 0 && m.shareCursor < len(m.shareRows) {
		curModel = m.shareRows[m.shareCursor].model
	}
	m.loadShareRows(found)
	if curModel != "" {
		for i, r := range m.shareRows {
			if r.model == curModel {
				m.shareCursor = i
				break
			}
		}
	}
	// The catalog exists now, so the armed models can finally be resolved to rows. Once
	// per launch - a later re-scan must not re-start a model the operator took off air.
	m.runAutoStart()
	// `/share <model>` shortcut: flip that exact model on air, then show the table.
	if m.sharePending != "" {
		want := m.sharePending
		m.sharePending = ""
		for i, r := range m.shareRows {
			if r.model == want {
				m.shareCursor = i
				// AUTO-START MAY HAVE BEATEN US TO IT. toggleShareAt is a TOGGLE, so on a
				// session whose first detect comes from `/share <armed-model>`, auto-start
				// puts the model on air and this would immediately turn it back off - the
				// explicit request ending off air, which is the opposite of what was asked.
				// Selecting the row is enough when it is already broadcasting.
				if m.ctrl != nil && m.ctrl.IsOnAir(want) {
					break
				}
				mm := &m
				mm.toggleShareAt(i)
				m = *mm
				break
			}
		}
	}
	// A LOUD scan lands on the table - that is what the operator sat waiting for. A
	// QUIET one changes nothing about where they are: they may have hopped to another
	// screen while it ran, and a background result that teleports them back is a bug
	// wearing a feature's clothes.
	if !quiet {
		m.mode = modeShare
	}
	if len(m.shareRows) == 0 {
		m.status = stEmber.Render("! the local server reported no models - check it serves /v1/models")
	} else {
		m.status = stDim.Render("provider table - ↑↓ select, enter/a toggle ON-AIR, esc done")
	}
	// What auto-start did outranks the generic hint: it is the only account of why the rig
	// is (or is not) broadcasting, and it is gone from the screen after the next keypress.
	if as := m.autoStartStatus(); as != "" {
		m.status = as
	}
	return m, nil
}

// toggleShareAt flips the on-air state of the provider-table row at index i: a
// model that is off air goes ON AIR (starts an in-process agent.Session against
// the local upstream at the saved/free price), one that is on air goes off. It
// keeps m.share / m.onAir pointing at the headline (any-live) session so the
// existing ON-AIR panel + header indicator still work.
func (m *model) toggleShareAt(i int) {
	if i < 0 || i >= len(m.shareRows) {
		return
	}
	if m.namelessVoiceBlocks(i) {
		return
	}
	model := m.shareRows[i].model
	res := m.ctrl.ToggleOnAir(model)
	m.syncShareCache()
	switch {
	case res.WentOff:
		m.status = stDim.Render("off air - stopped sharing ") + stKey.Render(model)
	case res.AtLimit:
		// SOFT local on-air cap (share.max_on_air): take one off air to free a slot.
		m.status = m.onAirLimitMsg()
	case res.LoginNeeded:
		// Share-to-EARN needs an account (the broker 403s a priced node from an unlinked
		// owner). Free sharing stays open to anyone, no login.
		m.status = stEmber.Render("log in to earn - run ") + stKey.Render("/login") + stDim.Render(" (free sharing works without an account)")
	case res.Err != nil:
		m.status = stEmber.Render("! could not put " + model + " on air: " + res.Err.Error())
	default:
		kind := "FREE"
		if res.Priced {
			kind = dollars(res.PriceOut) + "/1M out"
		}
		m.status = stRed.Render(glyphOnAir+" ON AIR ") + stDim.Render("- sharing ") + stKey.Render(model) + stDim.Render(" ("+kind+")")
	}
}

// togglePrivateAt flips the PRIVATE-band state of the row at index i. Going private is
// EARNING-adjacent (a per-owner resource) so it is LOGIN-GATED: an anonymous user gets
// the same /login flash as the price editor. On enable it (re)starts that row's session
// with Private:true and, when the broker mints a fresh code, opens the one-time code
// card (modeBandCard). On disable it restarts the row as a public share. It returns the
// new mode so the caller can route to the card. Mirrors toggleShareAt's start logic.
func (m *model) togglePrivateAt(i int) {
	if i < 0 || i >= len(m.shareRows) {
		return
	}
	// A nameless/voiceless tts row can't go on air the PRIVATE-band way either - the broker
	// 400s a nameless voice offer, so we BLOCK it here with the same VOICE BOOTH prompt
	// toggleShareAt uses, before firing a doomed register.
	if m.namelessVoiceBlocks(i) {
		return
	}
	model := m.shareRows[i].model
	res := m.ctrl.TogglePrivate(model)
	m.syncShareCache()
	switch {
	case res.LoginNeeded:
		// Login-gated: flash the existing /login line (same copy as the price editor).
		m.status = stEmber.Render("log in to go private - run ") + stKey.Render("/login") + stDim.Render("  (a private band needs an account)")
	case res.AtLimit:
		m.status = m.onAirLimitMsg()
	case res.Err != nil:
		// Lead with the broker's OWN reason (node.ErrReason drops the "register with
		// <url>: broker rejected registration (403):" frames that used to eat the whole
		// status line), then say what the row is doing NOW - an operator who just failed
		// to go private most needs to know whether they are still broadcasting.
		state := stEmber.Render(" - " + model + " went off air")
		if res.Restored {
			state = stDim.Render(" - " + model + " is still on air, unchanged")
		}
		reason := node.ErrReason(res.Err)
		// A quota refusal is the one failure the operator can fix themselves, so point at
		// the surface that can fix it rather than leaving them at a dead end.
		if strings.Contains(strings.ToLower(reason), "band limit reached") {
			// OFFER THE FIX HERE. A refusal that names another screen is still a dead
			// end; the operator wanted this model on a private band, and moving their
			// existing one does exactly that while keeping its code, so everyone already
			// tuned in keeps working.
			m.offerBandMove(model)
			state = bandQuotaOffer(model)
		}
		m.status = stEmber.Render("! "+reason) + state
	case !res.NowPrivate:
		m.status = stDim.Render("back on the OPEN MARKET - ") + stKey.Render(model) + stDim.Render(" is public again")
	case res.Code != "":
		// Private: surface the one-time frequency code on a card (only when freshly minted;
		// a re-register returns no code, only the cosmetic display).
		m.bandCardCode, m.bandCardDisp, m.bandCardModel = res.Code, res.Display, model
		m.mode = modeBandCard
		m.status = stRed.Render(glyphOnAir+" PRIVATE ") + stDim.Render("- ") + stKey.Render(model) + stDim.Render(" is on a hidden band")
	default:
		// No fresh code (already had a band): just mark it private, note the display.
		m.bandCardDisp = res.Display
		m.status = stRed.Render(glyphOnAir+" PRIVATE ") + stDim.Render("- ") + stKey.Render(model) + stDim.Render(" on band "+res.Display)
	}
}

// onBandCardKey drives the one-time frequency-code card (modeBandCard): `c` copies the
// code to the OS clipboard (best-effort; if no clipboard tool is present the code stays
// shown for manual select), any other key returns to the SHARE table. The secret is
// CLEARED from the model when leaving so it is never re-rendered after this one view.
func (m *model) onBandCardKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "c":
		if copyToClipboard(m.bandCardCode) {
			m.status = copiedToast("frequency code")
		} else {
			m.status = stDim.Render("no clipboard tool found - select the code above to copy it")
		}
		return m, nil
	default:
		// Leave the card: clear the secret so it is shown exactly once.
		m.bandCardCode = ""
		m.bandCardModel = ""
		m.mode = modeShare
		if m.bandCardReturnSet {
			m.mode = m.bandCardReturn
			m.bandCardReturn, m.bandCardReturnSet = 0, false
		}
		return m, nil
	}
}

// onAirCount is how many models are currently ON AIR (live shares). Drives the
// quit-guard: quitting while > 0 must confirm going off air first.
func (m model) onAirCount() int {
	n := m.sharesOnAir()
	if n == 0 && m.onAir && m.share != nil {
		n = 1 // a legacy single-share session not tracked in the shares map
	}
	return n
}

// onShareKey drives the k9s-style provider table: up/down (j/k) move the
// reverse-video cursor, enter/a/space toggle the selected model on/off air, p
// opens the per-model price + schedule editor (login-gated), r re-detects, esc/q
// leaves (shares keep running in the background), s returns to TUNE IN.
func (m *model) onShareKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// RENAME mode owns every keystroke: `n` started a station rename, so we build the
	// edit buffer char-by-char until enter (commit + persist) or esc (cancel). This is
	// checked FIRST so the preset bank / table keys never steal the typing.
	if m.renaming {
		return m.onStationRenameKey(k)
	}
	// Preset bank: 1 TUNE IN · 3 CONFIG · L LOGIN · ? HELP jump straight out of the
	// table. (2 SHARE is the current screen, so it is a no-op pressed-state and falls
	// through to the table keys below; `a`/`enter` toggle on-air as before.)
	if k.String() != "2" {
		if nm, cmd, ok := m.presetForKey(k.String()); ok {
			return nm, cmd
		}
	}
	switch k.String() {
	case "esc", "q", "s":
		m.mode = modeBrowse
		m.status = stDim.Render("TUNE IN - browse the band, enter to tune in")
		return m, nil
	case "up", "k":
		if m.shareCursor > 0 {
			m.shareCursor--
		}
	case "down", "j":
		if m.shareCursor < len(m.shareRows)-1 {
			m.shareCursor++
		}
	case "enter", "a", " ", "space":
		m.toggleShareAt(m.shareCursor)
	case "h":
		// HIDE / PRIVATE: toggle the selected row onto a hidden frequency band
		// (login-gated). A fresh mint routes into the one-time code card (modeBandCard).
		m.togglePrivateAt(m.shareCursor)
	case "y":
		// Accept a standing quota offer: move the existing band onto the model the
		// operator just tried to hide.
		//
		// `y`, not `m`: m already toggles the compact windowshade everywhere, and
		// shadowing a global key inside one view is how an operator learns not to trust
		// their own muscle memory. y also matches every other confirm on this screen -
		// the offer is a question, and y is what answers one.
		//
		// Inert without an offer standing, so it stays free the rest of the time.
		if m.bandMoveOffer != "" {
			cmd := m.acceptBandMove()
			m.status = stDim.Render("moving your band to ") + stKey.Render(m.bandMoveOffer) + stDim.Render("…")
			m.bandMoveOffer = ""
			return m, cmd
		}
	case "n":
		// RENAME the station callsign (the friendly, non-sensitive broadcast name shown in
		// /discover). Opens the inline editor seeded with the current station; commit
		// persists + re-derives every band's node id on its next on-air.
		m.renaming = true
		m.stationEdit = m.station
		m.status = stDim.Render("rename station - type a callsign, ") + stKey.Render("enter") + stDim.Render(" save · ") + stKey.Render("esc") + stDim.Render(" cancel")
		return m, nil
	case "b", "B":
		// THE BAND CARD for this row: on air, visibility, band, price and spend caps
		// together, instead of the four screens they were split across.
		if m.shareCursor < len(m.shareRows) {
			return m.openBandConfig(m.shareRows[m.shareCursor].model, modeShare)
		}
		return m, nil
	case "p", "e":
		// Open the pricing editor for the selected model. A VOICE (tts) row opens the VOICE BOOTH
		// (pick voice/blend/speed + set a $/1k price) instead of the token-price editor — at the
		// SAME depth (founder DELTA §D2: model-first, no elevation). A chat row opens the ordinary
		// price + time-of-use schedule editor. Both are EARNING, so login-gated inside their entry.
		if m.isTTSShareRow(m.shareCursor) {
			return m.enterVoiceBooth()
		}
		return m.enterShareEditor()
	case "r":
		// ASYNC re-detect: stay on the table in the loading pose and probe off the event
		// loop (a busy host's port scan must never freeze the table). An empty result
		// keeps us on the table with a note (setupOnEmpty stays false) rather than yanking
		// into the wizard mid-list.
		// The rows on screen STAY on screen while the re-scan runs - the same no-abrupt-
		// clear rule as re-entry. The loud pose only when there is nothing to keep.
		if len(m.shareRows) > 0 {
			m.shareLoading = false
			m.shareRefreshing = true
		} else {
			m.shareLoading = true
		}
		m.setupOnEmpty = false
		m.shareRescan = true
		m.setupHint = ""
		m.sharePending = ""
		m.status = stDim.Render("re-scanning the band for local models…")
		return m, detectSharesCmd(m.shareUp, m.shareKey)
	}
	return m, nil
}

// onStationRenameKey drives the inline station-callsign rename (entered with `n` on the
// SHARE table): printable runes + backspace build the buffer, enter commits, esc/ctrl+c
// cancels. On commit the typed name is slugged (so it matches the node id exactly) and,
// if non-empty, becomes the live station + is persisted via Hooks.SaveStation; the new
// callsign takes effect on each band's NEXT on-air (or restart the row). An empty/blank
// commit keeps the current station rather than blanking it.
func (m *model) onStationRenameKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.renaming = false
		m.stationEdit = ""
		m.status = stDim.Render("rename cancelled - station stays ") + stKey.Render(m.station)
		return m, nil
	case tea.KeyEnter:
		m.renaming = false
		slug := agent.SlugStation(m.stationEdit)
		m.stationEdit = ""
		if slug == "" {
			m.status = stEmber.Render("station unchanged - ") + stKey.Render(m.station) + stDim.Render(" (a callsign needs at least one letter or digit)")
			return m, nil
		}
		m.ctrl.Rename(slug) // sets + persists via Hooks.SaveStation; shared with the web console
		m.syncShareCache()
		m.status = stLive.Render("station set to ") + stKey.Render(m.station) + stDim.Render(" - applies on the next on-air (re-toggle a row to apply now)")
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		if n := len(m.stationEdit); n > 0 {
			m.stationEdit = m.stationEdit[:n-1]
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		m.stationEdit += string(k.Runes)
		return m, nil
	}
	return m, nil
}

// enterShareSetup opens the in-TUI guided fallback when no local model was
// detected: a small wizard to pick a tool (for a start one-liner) or paste an
// endpoint we verify with detect.ProbeKey. Mirrors the CLI guidedUpstream flow.
func (m model) enterShareSetup() model {
	m.mode = modeShareSetup
	m.setupCursor = 0
	m.setupPaste = ""
	m.setupErr = ""
	m.setupAwaitKey = false
	m.setupKey = ""
	m.status = stDim.Render("no local model found - pick what you're running, or paste a URL")
	return m
}

// onShareSetupKey drives the guided fallback: up/down move, enter picks; a named
// tool shows its one-liner + offers a re-scan; the "Other" row turns the row into
// a URL input we verify on enter. esc/s leaves.
func (m *model) onShareSetupKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	pasting := m.setupCursor == len(setupOptions)-1
	// Preset bank jumps - but NOT while pasting a URL (those keystrokes are the URL),
	// and not for `2`/SHARE which is the current section.
	if !pasting && k.String() != "2" {
		if nm, cmd, ok := m.presetForKey(k.String()); ok {
			return nm, cmd
		}
	}
	switch k.String() {
	case "esc", "s":
		m.mode = modeBrowse
		m.status = stDim.Render("TUNE IN - browse the band")
		return m, nil
	case "up", "k":
		if m.setupCursor > 0 {
			m.setupCursor--
		}
		m.setupErr = ""
		m.setupAwaitKey = false
		m.setupKey = "" // leaving the key step: drop any typed key so it can't be reused on another URL
		return m, nil
	case "down", "j":
		if m.setupCursor < len(setupOptions)-1 {
			m.setupCursor++
		}
		m.setupErr = ""
		m.setupAwaitKey = false
		m.setupKey = ""
		return m, nil
	case "r":
		// Re-scan (after the user started their tool in another terminal). ASYNC: enter
		// the loading table and probe off the event loop; an empty result returns to the
		// wizard with a note (setupOnEmpty=true), a found result lands the table.
		m.mode = modeShare
		m.shareLoading = true
		m.setupOnEmpty = true
		m.shareRescan = true
		m.setupHint = ""
		m.sharePending = ""
		m.setupErr = ""
		m.status = stDim.Render("re-scanning the band for local models…")
		return m, detectSharesCmd(m.shareUp, m.shareKey)
	case "enter":
		if pasting {
			url := strings.TrimSpace(m.setupPaste)
			if url == "" {
				m.setupErr = "paste your endpoint, e.g. http://127.0.0.1:8081"
				return m, nil
			}
			// Verify with the typed key ONLY when we are in the key-entry step. On the first
			// pass (no key step yet) we probe with NO key — a key-protected server flips into
			// the key step rather than failing, and only the next enter re-verifies with the
			// typed key. This stops a stale key (typed for a previous URL) being sent as a
			// Bearer to a different pasted URL. loadShareRows then carries the verified key.
			key := ""
			if m.setupAwaitKey {
				key = strings.TrimSpace(m.setupKey)
			}
			f, st := detect.ProbeKey(url, key)
			switch st {
			case detect.Reachable:
				m.shareUp = normalizeUpstream(f.Chat)
				m.loadShareRows([]detect.Found{f})
				m.mode = modeShare
				m.setupAwaitKey = false
				m.setupKey = ""
				m.status = stLive.Render("verified " + f.BaseURL + " - " + plural(len(m.shareRows), "model") + " ready")
				return m, nil
			case detect.NeedsKey:
				m.setupAwaitKey = true
				m.setupErr = ""
				m.status = stDim.Render(url + " needs an API key - type it and press enter")
				return m, nil
			default:
				m.setupErr = "no OpenAI-compatible server at " + url + " (no /v1/models) - check it and try again"
				return m, nil
			}
		}
		// A named tool: ASYNC re-detect (maybe it's already up). If nothing comes back we
		// return to the wizard with this tool's start one-liner; a found result lands the
		// table. Detection runs off the event loop so the pick never freezes the wizard.
		m.mode = modeShare
		m.shareLoading = true
		m.setupOnEmpty = true
		m.shareRescan = true
		m.sharePending = ""
		m.setupHint = "start it, then press r to re-scan:  " + setupOptions[m.setupCursor].oneLiner
		m.status = stDim.Render("checking for " + setupOptions[m.setupCursor].label + "…")
		return m, detectSharesCmd(m.shareUp, m.shareKey)
	case "backspace":
		if pasting {
			if m.setupAwaitKey {
				if m.setupKey != "" {
					m.setupKey = m.setupKey[:len(m.setupKey)-1]
				}
			} else if m.setupPaste != "" {
				m.setupPaste = m.setupPaste[:len(m.setupPaste)-1]
			}
		}
		return m, nil
	default:
		if pasting {
			if s := k.String(); len(s) == 1 {
				if m.setupAwaitKey {
					m.setupKey += s
				} else {
					m.setupPaste += s
				}
			}
		}
		return m, nil
	}
}

// enterShareEditor opens the per-model price + time-of-use schedule editor for the
// row at the cursor. EARNING requires an account, so this is login-gated: an
// anonymous user is shown "log in to earn - run /login" instead of being allowed
// to set a price that could never pay out. Free sharing stays open to anyone, so
// the table itself (and toggling FREE on/off air) never needs login.
func (m model) enterShareEditor() (tea.Model, tea.Cmd) {
	if len(m.shareRows) == 0 {
		return m, nil
	}
	if !m.loggedInState() {
		m.status = stEmber.Render("log in to earn - run ") + stKey.Render("/login") + stDim.Render("  (free sharing works without an account)")
		return m, nil
	}
	row := m.shareRows[m.shareCursor]
	m.edModel = row.model
	p := m.pricingFor(row.model)
	m.edPriceIn = trimZero(p.In)
	m.edPriceOut = trimZero(p.Out)
	m.edWindows = append([]SchedWindow(nil), p.Windows...)
	m.edField = edFieldOut // out-price is the headline knob
	m.edWinSub = winSubStart
	m.edErr = ""
	m.mode = modeShareEditor
	m.status = stDim.Render("tab field · ←→ window start/end/in/out · a add · d del · f free · ⏎ save · esc")
	return m, nil
}

// onShareEditorKey drives the pricing + schedule editor. tab/↑↓ move between
// fields (in, out, add-window, each window), digits edit the focused price, a adds
// a window, d deletes the focused window, f flips a window FREE, enter saves +
// returns to the provider table, esc cancels.
func (m *model) onShareEditorKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	nFields := edFieldFirstWin + len(m.edWindows)
	switch k.String() {
	case "esc":
		m.mode = modeShare
		m.status = stDim.Render("cancelled - price unchanged")
		return m, nil
	case "enter":
		// Validation failures (bad HH:MM, unparseable price, over the public ceiling)
		// BLOCK the save and keep the editor open with an inline error, instead of
		// silently persisting a window that never matches or a stale price. Only a clean
		// commit returns to the provider table.
		if m.commitShareEditor() {
			m.mode = modeShare
		}
		return m, nil
	case "tab", "down":
		m.edField = (m.edField + 1) % nFields
		m.edWinSub = winSubStart // each row starts on its Start sub-field
		m.syncWinBuf()
		return m, nil
	case "shift+tab", "up":
		m.edField = (m.edField - 1 + nFields) % nFields
		m.edWinSub = winSubStart
		m.syncWinBuf()
		return m, nil
	case "right", "left":
		// Cycle the sub-field WITHIN the focused window (Start/End/In/Out) so all of
		// its values are editable. No-op outside a window row.
		if m.edField >= edFieldFirstWin {
			if k.String() == "right" {
				m.edWinSub = (m.edWinSub + 1) % winSubCount
			} else {
				m.edWinSub = (m.edWinSub - 1 + winSubCount) % winSubCount
			}
			m.syncWinBuf()
		}
		return m, nil
	case "a":
		// Add a time-of-use window (ChargePoint-style): a default evening peak the
		// user then edits. Focus jumps to the new window.
		m.edWindows = append(m.edWindows, SchedWindow{Start: "18:00", End: "22:00", In: 0, Out: 0})
		m.edField = edFieldFirstWin + len(m.edWindows) - 1
		m.edWinSub = winSubStart
		m.syncWinBuf()
		return m, nil
	case "d":
		if m.edField >= edFieldFirstWin {
			i := m.edField - edFieldFirstWin
			if i >= 0 && i < len(m.edWindows) {
				m.edWindows = append(m.edWindows[:i], m.edWindows[i+1:]...)
				if m.edField >= edFieldFirstWin+len(m.edWindows) {
					m.edField = edFieldOut
				}
			}
		}
		return m, nil
	case "f":
		if m.edField >= edFieldFirstWin {
			i := m.edField - edFieldFirstWin
			if i >= 0 && i < len(m.edWindows) {
				m.edWindows[i].Free = !m.edWindows[i].Free
			}
		}
		return m, nil
	case "backspace":
		m.editShareField(func(s string) string {
			if len(s) > 0 {
				return s[:len(s)-1]
			}
			return s
		})
		return m, nil
	default:
		ch := k.String()
		// Price fields take digits/dot; window fields take digits + ':' (HH:MM).
		if d := digitsDot(ch); d != "" || ch == ":" {
			add := d
			if ch == ":" {
				add = ":"
			}
			m.editShareField(func(s string) string { return s + add })
		}
		return m, nil
	}
}

// doLogin opens the confirmable [L] panel - it NEVER acts on its own, because
// arrow-nav across the preset bank can land on [L]. Logged in it offers a log-out
// confirm; logged out it offers a press-enter-to-log-in prompt. The device flow
// only starts on an explicit ENTER inside the panel (startLogin), and logout only
// on an explicit y (see onLoginKey). The panel returns to the mode it was opened
// from on dismiss.
func (m model) doLogin() (tea.Model, tea.Cmd) {
	if m.mode != modeLogin {
		m.loginReturn = m.mode
	}
	m.mode = modeLogin
	m.loginNote = ""
	// Re-arming the panel never carries over a stale in-flight device flow.
	m.loginWaiting = false
	m.loginDevice = LoginDevice{}
	if m.loggedInState() {
		m.status = stDim.Render("log out? y confirms · n / esc keeps you logged in")
	} else {
		m.status = stDim.Render("log in with GitHub - press enter · esc cancels")
	}
	return m, nil
}

// onLoginKey owns every key while the [L] login/logout panel is open, so the
// y / n / enter here are NEVER stolen by the preset bank or the arrow-cycle. The
// panel is always dismissible (esc / n / arrowing away keep the current session).
func (m model) onLoginKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the device flow is in flight, only allow dismissing the panel (the poll
	// keeps running in the background and still lands its loginMsg). No key restarts
	// the flow, so there is never a surprise second code.
	switch k.String() {
	case "esc", "left", "right":
		// Dismiss: keep the current login state exactly as it is. Arrowing away (the
		// preset cycle keys) must NOT start a flow or log anyone out - it just leaves.
		m.mode = m.loginReturn
		m.status = stDim.Render("")
		return m, nil
	}
	if m.loggedInState() {
		// LOGGED IN -> a logout confirm. y logs out; everything else keeps the session.
		switch k.String() {
		case "y", "Y":
			return m.startLogout()
		case "n", "N":
			m.mode = m.loginReturn
			m.status = stDim.Render("still logged in")
			return m, nil
		}
		return m, nil
	}
	// LOGGED OUT -> press enter to start the device flow (+ auto-open browser).
	if !m.loginWaiting {
		switch k.String() {
		case "enter":
			return m.startLogin()
		}
	}
	return m, nil
}

// doTopup opens checkout (async; the URL lands as a topupMsg).
func (m model) doTopup(args []string) (tea.Model, tea.Cmd) {
	if m.hooks.TopupURL == nil {
		m.status = stDim.Render("top-up unavailable in this build - run `roger balance --topup`")
		return m, nil
	}
	// The amount is read by the SAME parser the CLI uses (client.ParseTopupAmount).
	// This was a third private copy, with the original bug: `/topup $25` failed
	// ParseFloat and silently opened checkout for $10.
	usd, err := client.ParseTopupAmount(args)
	if err != nil {
		// stEmber + "! ", the same shape every other flow failure takes (flowErrMsg).
		// stDim is the hint style used for "opening checkout…" a line below, and a
		// refusal on a money path must not read as ambient chatter.
		m.status = stEmber.Render("! " + err.Error())
		return m, nil
	}
	broker, user, topup := m.broker, m.user, m.hooks.TopupURL
	m.status = stDim.Render("opening checkout…")
	return m, func() tea.Msg {
		url, err := topup(broker, user, usd)
		if err != nil {
			return flowErrMsg("top-up failed: " + err.Error())
		}
		return topupMsg(url)
	}
}

// doGrant creates or lists owner grant keys in-TUI. `/grant create <name>` mints a
// FREE key (shown once); `/grant` or `/grant list` lists them.
func (m model) doGrant(args []string) (tea.Model, tea.Cmd) {
	if len(args) >= 1 && (args[0] == "create" || args[0] == "new") {
		if m.hooks.GrantCreate == nil {
			m.status = stDim.Render("grants unavailable in this build - run `roger grant create`")
			return m, nil
		}
		name := "my-bots"
		if len(args) >= 2 {
			name = args[1]
		}
		broker, create := m.broker, m.hooks.GrantCreate
		m.status = stDim.Render("creating free grant " + name + "…")
		return m, func() tea.Msg {
			secret, err := create(broker, name, true)
			if err != nil {
				return flowErrMsg("grant create failed: " + err.Error())
			}
			return grantMsg{secret: secret}
		}
	}
	// default: list
	if m.hooks.GrantList == nil {
		m.status = stDim.Render("grants unavailable in this build - run `roger grant list`")
		return m, nil
	}
	broker, list := m.broker, m.hooks.GrantList
	return m, func() tea.Msg {
		rows, err := list(broker)
		if err != nil {
			return flowErrMsg("grant list failed: " + err.Error())
		}
		return grantListMsg(rows)
	}
}

// doFreq tunes the band browser to a PRIVATE frequency. A bare /freq with an active
// freq clears back to OPEN MARKET; a bare /freq with none prompts. A code resolves
// off the event loop (freqResolvedMsg) so the UI never blocks; on success the browse
// list shows ONLY that band, the header reads FREQ <display>, and esc returns to OPEN
// MARKET. A wrong / off-air code gets the uniform "no station on that frequency".
func (m model) doFreq(arg string) (tea.Model, tea.Cmd) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if m.tuneFreq != "" {
			// Clear: return to OPEN MARKET and re-scan the public band.
			m.tuneFreq, m.tuneFreqLabel = "", ""
			m.status = stDim.Render("back to ") + stKey.Render("OPEN MARKET")
			return m, fetchOffers(m.broker)
		}
		m.status = stDim.Render("usage: ") + stKey.Render("/freq <code>") + stDim.Render("  e.g. /freq \"147.520 MHz 8F3K-9M2Q\"")
		return m, nil
	}
	return m.resolveFreq(arg)
}

// onOverLimitKey drives the over-limit screen (3.3): inline numeric edit of your
// max, up/down nudge by 0.01, enter = save & re-check, esc/N = deny, w = wait.
func (m *model) onOverLimitKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "n", "N":
		m.mode = modeBrowse
		m.status = stDim.Render("denied - no channel opened")
		return m, nil
	case "w":
		// "wait & notify when it dips under" - stubbed as a labeled no-op: watch the
		// band, the offers tick drops a status line if it dips under (real notify P1).
		m.watching = m.q.b.model
		m.mode = modeBrowse
		m.status = stDim.Render("waiting - will flag " + m.q.b.model + " when it dips under " + money(m.q.limit.MaxOut))
		return m, nil
	case "up":
		m.editBuf = nudge(m.editBuf, +0.01)
		return m, nil
	case "down":
		m.editBuf = nudge(m.editBuf, -0.01)
		return m, nil
	case "backspace":
		if len(m.editBuf) > 0 {
			m.editBuf = m.editBuf[:len(m.editBuf)-1]
		}
		return m, nil
	case "enter":
		nv, err := strconv.ParseFloat(strings.TrimSpace(m.editBuf), 64)
		if err != nil || nv < m.q.b.minOut {
			// still below the band - keep blocked (validation), leave the user here.
			m.status = stEmber.Render("still below the band (" + money(m.q.b.minOut) + ") - raise it or esc")
			return m, nil
		}
		// persist the new per-model max, then re-run the connect check.
		lim := m.limits.resolve(m.q.b.model)
		lim.MaxOut = nv
		m.limits.set(m.q.b.model, lim)
		m.bands = m.mergeStickyBand(groupBands(m.offers, m.limits))
		m.mode = modeBrowse
		return m.connect()
	default:
		if d := digitsDot(k.String()); d != "" {
			m.editBuf += d
		}
		return m, nil
	}
}

// enterLimits builds the model list for the limits view (3.4): every band with a
// set limit, unioned with the bands currently on air, sorted.
func (m *model) enterLimits() {
	seen := map[string]bool{}
	var models []string
	if m.limits != nil {
		// Snapshot, not a direct range over Models: the browser console writes the same
		// store from its HTTP goroutine, and iterating the live map here would race it.
		for mdl := range m.limits.Snapshot() {
			if !seen[mdl] {
				seen[mdl] = true
				models = append(models, mdl)
			}
		}
	}
	for _, b := range m.bands {
		if !seen[b.model] {
			seen[b.model] = true
			models = append(models, b.model)
		}
	}
	sort.Strings(models)
	m.limModels = models
	if m.limCursor >= len(models) {
		m.limCursor = 0
	}
	m.editBuf = ""
	m.editField = -1 // not editing yet
	m.mode = modeLimits
}

// onBudgetRow reports whether the spend-limits cursor sits on the wallet's monthly-budget
// row; editingBudget, whether that row's editor is open. Named accessors because the BDD
// suite drives the screen exactly as an operator does and asserts THESE, not internals.
func (m model) onBudgetRow() bool   { return m.limOnBudget }
func (m model) editingBudget() bool { return m.limEditBudget }

// parseBudgetInput reads the budget editor's draft: the CLI's clearing spellings
// (0/off/none/unlimited, and an emptied field) clear the cap; otherwise a dollar amount,
// with a stray leading $ tolerated because people type what the row shows.
func parseBudgetInput(s string) (float64, error) {
	raw := strings.TrimSpace(s)
	t := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "$")))
	// Only a value that SAYS clear, clears. A bare "$" stripped to nothing here and fell
	// into the clearing arm - so a slip of the finger removed a money protection. Emptied
	// entirely is deliberate (the operator deleted the number); "$" alone is not an amount
	// and is refused like any other non-number.
	if raw == "$" {
		return 0, fmt.Errorf("%q is not a dollar amount - a number like 25, or 0/off for no cap", s)
	}
	switch t {
	case "", "0", "off", "none", "unlimited":
		return 0, nil
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%q is not a dollar amount - a number like 25, or 0/off for no cap", s)
	}
	return v, nil
}

// budgetSavedMsg carries the broker's reply to a monthly-cap change (or its refusal).
type budgetSavedMsg struct {
	cap, spend float64
	err        error
}

// commitBudgetEdit validates the draft IN PLACE - a value that is not money never reaches
// the broker - then saves asynchronously. The row updates from the broker's reply rather
// than optimistically: this is a MONEY control, and showing a cap the broker has not
// accepted would be showing protection that does not exist.
func (m *model) commitBudgetEdit() (tea.Model, tea.Cmd) {
	cap, err := parseBudgetInput(m.editBuf)
	if err != nil {
		m.status = stEmber.Render(err.Error())
		return m, nil // stay editing: the draft is theirs to fix
	}
	m.limEditBudget = false
	broker, user := m.broker, m.user
	return m, func() tea.Msg {
		info, err := client.SetMonthlyLimit(broker, user, cap)
		if err != nil {
			return budgetSavedMsg{err: err}
		}
		return budgetSavedMsg{cap: info.Cap, spend: info.Spend}
	}
}

// onLimitsKey drives the per-model limits view (3.4): up/down move, enter edits
// (Tab between out-price and min-tps), d clears, esc done.
func (m *model) onLimitsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// THE BUDGET EDITOR takes the keys while it is open. Kept apart from the band-field
	// editor below: it commits to the BROKER (an account setting), not to the local store.
	if m.limEditBudget {
		switch k.String() {
		case "esc":
			m.limEditBudget = false
			return m, nil
		case "enter":
			return m.commitBudgetEdit()
		case "backspace":
			if len(m.editBuf) > 0 {
				m.editBuf = m.editBuf[:len(m.editBuf)-1]
			}
			return m, nil
		}
		if r := k.Runes; len(r) == 1 {
			m.editBuf += string(r)
		}
		return m, nil
	}
	editing := m.editField >= 0
	if !editing {
		// Preset bank jumps (only when NOT editing a numeric field, so a typed digit in
		// the editor is never stolen). 3 CONFIG is the current screen -> no-op.
		if k.String() != "3" {
			if nm, cmd, ok := m.presetForKey(k.String()); ok {
				return nm, cmd
			}
		}
		switch k.String() {
		case "esc", "q":
			// Back to whoever opened this. The BAND CARD routes here for a single field;
			// dropping the operator on the full spend table afterwards would be a screen
			// they never asked for.
			m.mode = modeBrowse
			if m.limReturnSet {
				m.mode = m.limReturn
				m.limReturn, m.limReturnSet = 0, false
			}
			return m, nil
		case "b", "B":
			// THE BAND CARD: everything about the band under the cursor, in one place.
			if m.limCursor < len(m.limModels) {
				return m.openBandConfig(m.limModels[m.limCursor], modeLimits)
			}
		case "up", "k":
			if m.limCursor > 0 {
				m.limCursor--
			} else if m.loggedInState() {
				// Up off the top of the table lands on the wallet's monthly-budget row -
				// the same "the thing you are looking at is the thing you edit" rule as
				// every band row. Logged out there is nothing to edit up there.
				m.limOnBudget = true
			}
		case "down", "j":
			if m.limOnBudget {
				m.limOnBudget = false
			} else if m.limCursor < len(m.limModels)-1 {
				m.limCursor++
			}
		case "d":
			if m.limCursor < len(m.limModels) {
				m.limits.clear(m.limModels[m.limCursor])
				m.enterLimits()
			}
		case "enter":
			if m.limOnBudget {
				if !m.loggedInState() {
					m.status = stDim.Render("log in to set a monthly spend limit")
					return m, nil
				}
				m.limEditBudget = true
				// Prefilled with the CURRENT cap, so changing $25 to $30 is an edit
				// rather than a retype; empty when no cap is set.
				m.editBuf = ""
				if m.monthlyCap > 0 {
					m.editBuf = trimZero(m.monthlyCap)
				}
				return m, nil
			}
			if m.limCursor < len(m.limModels) {
				lim := m.limits.resolve(m.limModels[m.limCursor])
				m.editField = 0
				m.editBuf = trimZero(lim.MaxOut)
			}
		}
		return m, nil
	}
	// editing a field
	switch k.String() {
	case "esc":
		m.editField = -1
		return m, nil
	case "tab":
		m.commitLimitField()
		m.editField = (m.editField + 1) % 2
		lim := m.limits.resolve(m.limModels[m.limCursor])
		if m.editField == 0 {
			m.editBuf = trimZero(lim.MaxOut)
		} else {
			m.editBuf = trimZero(lim.MinTPS)
		}
		return m, nil
	case "enter":
		m.commitLimitField()
		m.editField = -1
		// A card-initiated edit is DONE when the field is saved: the operator came to
		// change one number, not to browse the table.
		if m.limReturnSet {
			m.mode = m.limReturn
			m.limReturn, m.limReturnSet = 0, false
		}
		return m, nil
	case "up", "down":
		// NUDGE THE VALUE (founder 2026-08-21). Up and down did nothing while editing,
		// so setting a price meant typing every digit - and up/down are the first thing
		// anyone tries in a numeric field.
		//
		// A cent for the price and one for min t/s: those are the units the field is
		// actually denominated in, and a step that moves by a unit is the one nobody has
		// to think about. DOWN FLOORS AT ZERO rather than going negative - a negative
		// cap is not a smaller cap, it is a nonsense the commit would have to reject.
		m.editBuf = nudgeLimit(m.editBuf, m.editField == 0, k.String() == "up")
		return m, nil
	case "backspace":
		if len(m.editBuf) > 0 {
			m.editBuf = m.editBuf[:len(m.editBuf)-1]
		}
		return m, nil
	default:
		if d := digitsDot(k.String()); d != "" {
			m.editBuf += d
		}
		return m, nil
	}
}

// presetForKey maps a top-level key press to its preset action, returning the new
// model + cmd and true when the key was a preset jump (so onKey can short-circuit).
// It is the keyboard half of the preset bank: 1 -> TUNE IN, 2 -> SHARE, 3 -> CONFIG
// (limits), L -> LOGIN, ? -> HELP. It is only consulted from non-text-entry modes
// (browse / a SHARE sub-screen / limits / help) so it never steals a typed digit in
// the command palette, the chat input, or a numeric price/limit editor.
// toggleCompact flips the windowshade compact mode and persists the choice via the
// host SaveCompact hook (nil = session-only). It also clears the connected-header
// `minimized` sub-toggle so the two header collapses never fight: expanding out of
// compact returns to the full header, and compact subsumes the thin-bar minimize.
func (m model) toggleCompact() model {
	m.compact = !m.compact
	if m.compact {
		m.status = stDim.Render("compact - calm, dense, animation-free · m expands")
	} else {
		m.minimized = false
		m.status = stDim.Render("expanded - the full operating manual · m compacts")
	}
	if m.hooks.SaveCompact != nil {
		m.hooks.SaveCompact(m.compact)
	}
	return m
}

// onAirPulse returns the breathing ON-AIR beacon in a FIXED-width cell so the
// header's right edge never jitters as the arcs grow/shrink. The eye is the one
// live-red on-air beacon (cRed/cLive: #C8391A light / #FF5636 dark) matching the
// web's --live carrier; the arcs are mono ink. Cadence is gated on a slow phase so it
// reads as a calm breath, not a flicker. eyeStyle lets callers pass the beacon
// style (the beacon and Ping's eye now share the same one red).
func onAirPulse(frame int) string { return pulseWith(frame, stRed) }

// runAutoTune folds a silent auto-tune outcome into the model (R1/R6): a FREE pick is
// connected on the spot at $0 and the agent binds to it; a PAID pick (logged-in
// cheapest-paid) lands on the honest paid state, NEVER a spend; nothing available lands
// on the honest empty state. It respects a channel opened since entry and a
// deliberately-tuned band (no override). It is a no-op unless an auto-tune is armed.
func (m *model) runAutoTune() tea.Cmd {
	if !m.autoTuning || m.agent == nil {
		return nil
	}
	// The auto-tune is an AGENT-landing affordance. If the user has since LEFT AGENT (esc to
	// BROWSE during the cold /discover fetch), its effects - binding a channel, stomping the
	// status, firing a parked turn - must NOT land outside AGENT. Disarm and bail, dropping
	// any parked prompt (there is no landing to send it to). Audit finding.
	if m.mode != modeAgent {
		m.autoTuning = false
		m.clearFindingBeat()
		m.flushPendingPrompts()
		return nil
	}
	m.autoTuning = false
	// A channel opened / a band deliberately tuned since we armed: never override it.
	if m.connected != nil || m.resolveAgentModel() != "" {
		m.clearFindingBeat()
		// Mirror the free-pick branch's guard (the f6c5be7 ruling): if the user is mid-pick
		// on the FOCUSED desk, an already-connected auto-tune must NOT yank them to the ask
		// box. Only grab focus when the desk isn't holding it.
		if !m.deskFocused {
			m.agentIn.Focus()
		}
		m.refreshAgentModel()
		return m.drainPendingPrompts()
	}
	// The FILTERED view, not raw bands: an operator who hid curated (or narrowed the dial
	// any other way) must never be silently bound to a band they asked not to see.
	pick := pickAutoBand(m.visibleBands(), m.loggedInState())
	// R1 money-safety: bind the band's genuinely-FREE station (FreeNow / zero-priced), NEVER
	// pick.cheapest - the min-PRICE station across ALL stations, which can be a PAID station
	// even when the band is flagged free (a FreeNow promo beside a cheaper paid one). If no
	// free station exists (a stale/mixed free flag, or only paid), fall to the honest paid
	// state below - a silent bind is only ever a $0 station.
	var freeSt *offer
	if pick != nil {
		freeSt = bestFreeStation(*pick)
	}
	switch {
	case freeSt != nil:
		o := *freeSt
		m.clearFindingBeat()
		if _, err := m.bindChannel(o); err != nil {
			// The local endpoint failed to bind: never claim a channel that is not there.
			// Fall to the honest empty state (deduped) and drop any parked prompt silently.
			m.noteOnce(
				stRed.Render("✕ ")+stEmber.Render("no station on air right now"),
				hintTuneOrShare(m.narrow()))
			m.agentLandingLines = len(m.agentLines)
			m.status = stEmber.Render("! endpoint bind failed: " + err.Error())
			m.flushPendingPrompts()
			return nil
		}
		m.agent.model = o.Model
		// Same rule as refreshAgentModel: the endpoint follows the model, or an earlier
		// local pick keeps swallowing turns under this band's name.
		m.bindAgentEndpoint(o.Model)
		m.agentPicked = false
		m.agentPickedOver = ""
		// Keep focus where it is: if the user is on the FOCUSED desk (a guest scan landed
		// first), a silent auto-tune must not yank them to the ask box mid-pick. Otherwise
		// the ask box takes focus so a turn can be typed straight away.
		if !m.deskFocused {
			m.agentIn.Focus()
		}
		m.noteOnce(stDim.Render("· ") + stDim.Render("auto-tuned to ") + stKey.Render(o.Model) + stDim.Render(" (free) · the agent runs on it"))
		m.agentLandingLines = len(m.agentLines)
		m.status = stRed.Render(glyphOnAir+" ") + stDim.Render("auto-tuned to ") + stKey.Render(o.Model) + stDim.Render(" · type to ask")
		return m.drainPendingPrompts()
	case pick != nil: // a paid pick, OR a free-flagged band with no genuinely-free station -
		// either way the honest paid state, never a silent spend (R1: never auto-spend)
		m.clearFindingBeat()
		m.noteOnce(stDim.Render("· ") + stDim.Render("no free band on air - ") + stKey.Render("[1]") + stDim.Render(" picks a paid band (the usual cost confirm applies)"))
		m.agentLandingLines = len(m.agentLines)
		m.status = stDim.Render("no free band on air · [1] to pick a paid band · esc exits")
		m.flushPendingPrompts()
	default: // nothing to land on - the honest empty state
		m.clearFindingBeat()
		anyOnline := false
		for _, b := range m.bands {
			if b.online && !b.isVoice() {
				anyOnline = true
				break
			}
		}
		if anyOnline && !m.loggedInState() {
			// Paid-only market, logged out: name the honest move (log in) without naming a
			// band it cannot reach.
			m.noteOnce(stDim.Render("· ") + stDim.Render("no free band on air - ") + stKey.Render("/login") + stDim.Render(", then ") + stKey.Render("[1]") + stDim.Render(" picks a paid band"))
			m.status = stDim.Render("no free band on air · /login for paid bands · esc exits")
		} else {
			m.noteOnce(
				stRed.Render("✕ ")+stEmber.Render("no station on air right now"),
				hintTuneOrShare(m.narrow()))
			m.status = stDim.Render("nothing on air · [1] tune in · [2] go on air · esc exits")
		}
		m.agentLandingLines = len(m.agentLines)
		m.flushPendingPrompts()
	}
	return nil
}

// dollars renders a money value with Groq-style adaptive precision: balances and
// "big" amounts at 2dp ($12.34), but tiny per-reply / per-token costs keep enough
// significant digits to never collapse to $0.00 (e.g. $0.000123). 1 credit = $1,
// so this is a pure display relabel of the credit unit. Display only - settlement
// math is untouched.
// dollars renders money through the ONE canonical formatter (client.FormatUSD) so the TUI
// and the CLI read identically - no second copy of the rule to drift. See client.FormatUSD:
// 0 -> "$0.00"; a sub-cent value -> ~3 significant figures (e.g. $0.00000036) so a real charge
// never reads as free; >= $0.01 -> two decimals.
func dollars(v float64) string {
	return client.FormatUSD(v)
}

// onAirMaxRows caps how many live bands the ON AIR panel lists in full before it
// folds the remainder into a "+K more" line, so a founder on air with a large
// fleet keeps the panel inside a reasonable height (the TOTALS line still sums
// EVERY band, listed or folded).
const onAirMaxRows = 8

// onAirPanel renders the live ON AIR provider instrument: ONE compact row per live
// band (model, node, price, served requests + out tokens, earnings) plus a TOTALS
// line summing across EVERY band, and the `/share off` footer (which stops them
// all). The header beacon reflects the truthful aggregate link state (a genuine ON
// AIR only while at least one band's heartbeats are acknowledged; RECONNECTING when
// none are). Many bands fold past onAirMaxRows into a "+K more". NO_COLOR / narrow
// safe: the plain words carry it, color + glyphs are decoration; each row is
// truncated to the panel width.
func (m model) onAirPanel(w int) string {
	live := m.liveShares()
	if len(live) == 0 {
		return ""
	}
	// Aggregate link state for the beacon: ON AIR if ANY band's broker link is live,
	// else the worst-case (RECONNECTING) so we never falsely claim on-air.
	anyOnAir, anyReconnecting := false, false
	for _, s := range live {
		switch s.Link() {
		case agent.LinkOnAir:
			anyOnAir = true
		case agent.LinkReconnecting:
			anyReconnecting = true
		}
	}
	var badge string
	switch {
	case anyOnAir:
		badge = stRed.Render(glyphOnAir + " ON AIR")
	case anyReconnecting:
		badge = stEmber.Render(glyphOffAir+" RECONNECTING") + stDim.Render(" - broker not acknowledging")
	default:
		badge = stDim.Render(glyphOffAir + " connecting…")
	}

	n := len(live)
	bands := "bands"
	if n == 1 {
		bands = "band"
	}
	head := badge + "  " + stDim.Render(fmt.Sprintf("sharing %d %s", n, bands))
	inner := w - 4 // stPanel border (2) + padding (2)
	if inner < 8 {
		inner = 8
	}

	// Totals sum EVERY live band, listed or folded.
	var totReqs, totToks int64
	var totEarn float64
	for _, s := range live {
		r, t := s.Served()
		totReqs += r
		totToks += t
		totEarn += s.Earnings()
	}
	// Per-band rows (compact), capped at onAirMaxRows with a "+K more" fold.
	shown := live
	folded := 0
	if len(live) > onAirMaxRows {
		shown = live[:onAirMaxRows]
		folded = len(live) - onAirMaxRows
	}
	// Elide long node ids so a row stays on one line at narrow widths.
	nodeCap := 18
	if inner < 64 {
		nodeCap = 10
	}
	rows := make([]string, 0, len(shown)+1)
	for _, s := range shown {
		in, out := s.Price()
		reqs, toks := s.Served()
		price := stLive.Render("FREE")
		if in > 0 || out > 0 {
			price = stEmber.Render(dollars(out) + "/1M out")
		}
		dot := stRed.Render(glyphOnAir)
		if s.Link() != agent.LinkOnAir {
			dot = stEmber.Render(glyphOffAir)
		}
		row := "  " + dot + " " + stKey.Render(s.Model()) +
			stDim.Render(" · ") + stSelText.Render(elide(s.Node(), nodeCap)) +
			stDim.Render(" · ") + price +
			stDim.Render(fmt.Sprintf(" · %d req · %d out · ", reqs, toks)) + stEmber.Render(dollars(s.Earnings()))
		rows = append(rows, row)
	}
	if folded > 0 {
		rows = append(rows, stDim.Render(fmt.Sprintf("  +%d more on air", folded)))
	}
	totals := stDim.Render("  TOTALS    ") +
		stLive.Render(fmt.Sprintf("%d", totReqs)) +
		stDim.Render(fmt.Sprintf(" requests · %d out tokens · ", totToks)) +
		stEmber.Render(dollars(totEarn)) + stDim.Render("  (settles on the broker)")

	lines := []string{head}
	lines = append(lines, rows...)
	lines = append(lines, totals)
	// Cash-out hint (KYC / payable): only when there's something actionable. Width-safe
	// + NO_COLOR-safe (the plain text carries it). When there is nothing actionable yet
	// (fresh provider, nothing payable), still point them at where earnings show up so
	// they are never left wondering where their money lands - one tasteful line either way.
	if hint := m.payoutHint(); hint != "" {
		lines = append(lines, "  "+hint)
	} else {
		lines = append(lines, stDim.Render("  earnings: ")+stKey.Render("rogerai.fm/dashboard.html")+stDim.Render("  (or: roger payout status)"))
	}
	lines = append(lines, stDim.Render("  ")+stKey.Render("/share off")+stDim.Render(" to go off air (stops all)"))
	// Every line is truncated to the inner content width so the bordered plate never
	// overflows the terminal, at any width and any band count.
	for i, ln := range lines {
		lines[i] = truncVisible(ln, inner)
	}
	rendered := stPanel.Render(strings.Join(lines, "\n"))
	if anyOnAir && !paletteMono && canTint(lipgloss.DefaultRenderer().ColorProfile()) {
		return solidBackground(rendered, cLiveSurface)
	}
	return rendered
}

// RunWithController launches the TUI over an EXISTING shared controller (so the host can
// stand up the browser web console over the SAME node before launching the TUI), with a
// spend-limit store (nil = no caps / no persistence), a pre-computed "update available"
// notice line (empty = none; the host owns the cached async check so the TUI never does
// network at startup), and the host-supplied hooks that make the in-TUI /share, /login,
// /topup, /grant flows real actions. This is the single entry point (cmd/rogerai wires it
// as runTUI); the thin Run/RunWith/RunWithNotice/RunWithHooks defaults-only wrappers were
// removed - a caller passes the explicit values (Hooks{} / "" / nil / NewController).
func RunWithController(broker, user string, limits *LimitStore, notice string, hooks Hooks, ctrl *node.Controller) error {
	m := NewWithHooksController(broker, user, limits, hooks, ctrl)
	m.updateLine = notice
	// Smart selection owns transcript drags at startup so mouse release can copy and
	// report an honest character count. /mouse or ctrl+o restores native selection.
	wantRestart = false
	if err := launchTUI(m, tea.WithAltScreen(), tea.WithMouseCellMotion()); err != nil {
		return err
	}
	if wantRestart {
		return ErrRestart
	}
	return nil
}

// RunResumedWithController is RunWithController with a durable local AGENT snapshot
// restored before the first frame.
func RunResumedWithController(
	broker, user string,
	limits *LimitStore,
	notice string,
	hooks Hooks,
	ctrl *node.Controller,
	item session.Snapshot,
) error {
	m, err := NewResumedWithHooksController(broker, user, limits, hooks, ctrl, item)
	if err != nil {
		return err
	}
	m.updateLine = notice
	wantRestart = false
	if err := launchTUI(m, tea.WithAltScreen(), tea.WithMouseCellMotion()); err != nil {
		return err
	}
	if wantRestart {
		return ErrRestart
	}
	return nil
}

// runProgram launches a Bubble Tea program and returns its exit error. It is a
// behaviour-preserving seam: a package-level var that defaults to the REAL
// tea.NewProgram(...).Run() so production is byte-for-byte unchanged, and the only
// reason it exists is so the Run* entry points + PingWalk can be exercised in tests
// without standing up a real terminal program (a test swaps it for a no-op / driver
// and restores it). Do NOT add logic here - keep it a thin pass-through.
var runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
	_, err := tea.NewProgram(m, opts...).Run()
	return err
}
