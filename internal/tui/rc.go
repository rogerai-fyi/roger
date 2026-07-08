package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/glyphs"
	"github.com/rogerai-fyi/roger/internal/harness"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// rc.go is the TUI half of BASE STATION / remote control (v5.0.0). Two roles:
//   HOST — /remote-control inside [0] AGENT puts THIS machine's live agent on the air; the
//     TUI tees every agent event to the broker (rcEmit*), injects remote turns, and answers
//     remote tool-confirms through the SAME channels a local keypress uses.
//   BASE STATION — the [p] private section (modePrivate): the remote-session roster + private
//     bands, and modeRemoteSession to continue a session hosted elsewhere. Honest labels:
//     "your account only · relayed through the broker · not end-to-end encrypted".
// See docs-internal/REMOTE-CONTROL-DESIGN.md. All hooks are nil-safe (a labeled hint degrades).

// --- message types ---

type remoteEnabledMsg struct {
	bridge RemoteBridge
	info   RemoteInfo
	err    error
}
type remoteInboundMsg protocol.RCInbound // a remote turn/confirm/backfill reached the HOST
type remoteRosterMsg struct {            // BASE STATION roster fetch result
	sessions []RemoteSessionRow
	bands    []BandRow
	err      error
}
type remoteFrameMsg struct {
	gen int // the viewer-stream generation this frame belongs to (stale generations are ignored)
	f   protocol.RCFrame
}
type remoteHostEndMsg struct{}   // the HOST bridge stopped (remote revoke / quit)
type remoteViewerEndMsg struct { // the in-TUI VIEWER's stream ended
	gen int
}

// ==========================================================================
// HOST side: /remote-control
// ==========================================================================

// rcNote appends a '· ' sysline (optionally with an accented value) to the AGENT transcript.
func (m *model) rcNote(s string) {
	m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render(s))
}
func (m *model) rcNoteKey(label, val string) {
	m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render(label)+stKey.Render(val))
}

// runRemoteCommand handles /remote-control and /remote-control off inside [0] AGENT.
func (m model) runRemoteCommand(off bool) (tea.Model, tea.Cmd) {
	if off {
		if m.rcBridge == nil {
			m.rcNote("remote control is not on")
			return m, nil
		}
		_ = m.rcBridge.Disable()
		m.rcBridge = nil
		m.rcNote("remote control OFF - this session is off the air (it stays here)")
		return m, nil
	}
	if m.rcBridge != nil {
		m.rcNoteKey("already on the air - link a phone: ", m.rcInfo.LinkURL)
		return m, nil
	}
	if m.hooks.RCEnable == nil {
		m.agentLines = append(m.agentLines, stRed.Render("✕ ")+stEmber.Render("remote control needs a logged-in account - run `roger login`"))
		return m, nil
	}
	name := m.rcSessionName()
	m.rcNote("enabling remote control…")
	broker, enable := m.broker, m.hooks.RCEnable
	return m, func() tea.Msg {
		bridge, info, err := enable(broker, name)
		return remoteEnabledMsg{bridge: bridge, info: info, err: err}
	}
}

// rcSessionName auto-names the session "<station> · <cwd-basename>" (never a hostname — the
// repo deliberately never puts a hostname in an id; the station callsign is the identity).
func (m model) rcSessionName() string {
	station := strings.TrimSpace(m.hooks.Station)
	if station == "" {
		station = "roger"
	}
	dir := filepath.Base(agentRoot())
	if dir == "" || dir == "." || dir == "/" {
		return station
	}
	return station + " · " + dir
}

// onRemoteEnabled stores the bridge, prints the one-time enable block, and starts pumping.
func (m model) onRemoteEnabled(msg remoteEnabledMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.agentLines = append(m.agentLines, stRed.Render("✕ ")+stEmber.Render("remote control: "+msg.err.Error()))
		return m, nil
	}
	m.rcBridge = msg.bridge
	m.rcInfo = msg.info
	who := m.ghLogin
	if who == "" {
		who = "your account"
	} else {
		who = "@" + who
	}
	m.agentLines = append(m.agentLines,
		stRed.Render(glyphOnAir)+"  "+stBrand.Render("REMOTE CONTROL")+stDim.Render(" — this session is now on your BASE STATION"))
	m.rcNoteKey("session: ", msg.info.Name)
	m.rcNote("visible only to " + who + " — nobody else can see or join it")
	m.rcNote("continue it anywhere you're logged in: another terminal (press p on THE BAND), the web console, or the Roger app")
	m.rcNote("relayed through the broker over TLS · not end-to-end encrypted · tools still run on THIS machine and still ask before anything mutating")
	m.rcNoteKey("link a phone: ", msg.info.LinkURL)
	m.rcNote("/remote-control off takes it off the air (the session stays here)")
	msg.bridge.Run()
	return m, waitRemoteInbound(msg.bridge)
}

// waitRemoteInbound reads ONE inbound from the bridge and delivers it as a tea.Msg; it is
// re-armed after each so the host keeps draining remote turns/confirms/backfill. It also
// selects on the bridge's Done channel so that when the bridge is Stopped (e.g. a remote
// revoke-all 401'd the poll) the parked Cmd unblocks cleanly and the host is told the session
// ended — rather than the goroutine leaking on a never-closed inbound channel.
func waitRemoteInbound(b RemoteBridge) tea.Cmd {
	if b == nil {
		return nil
	}
	ch, done := b.Inbound(), b.Done()
	return func() tea.Msg {
		select {
		case in, ok := <-ch:
			if !ok {
				return remoteHostEndMsg{}
			}
			return remoteInboundMsg(in)
		case <-done:
			return remoteHostEndMsg{}
		}
	}
}

// onRemoteHostEnd handles the HOST bridge ending (a remote revoke-all 401'd the poll, or quit):
// clear the live-host state so the TUI stops showing LIVE and teeing to a dead session.
func (m model) onRemoteHostEnd() (tea.Model, tea.Cmd) {
	if m.rcBridge == nil {
		return m, nil
	}
	m.rcBridge = nil
	m.rcConfirmID = ""
	m.rcNote("remote control ended — this session is off the air (revoked or disconnected)")
	return m, nil
}

// onRemoteInbound dispatches a remote message on the HOST's UI goroutine. A turn is injected
// exactly like local typing; a confirm answers the pending gate; a backfill replies with the
// current transcript addressed to the asking viewer. Always re-arms the drain.
func (m model) onRemoteInbound(in protocol.RCInbound) (tea.Model, tea.Cmd) {
	rearm := waitRemoteInbound(m.rcBridge)
	switch in.Kind {
	case protocol.RCInTurn:
		if strings.TrimSpace(in.Text) == "" {
			return m, rearm
		}
		// Guest-operator staging guard (audit regression): from the moment a handoff is
		// staged until the exec callback returns, a remote turn is dropped with the
		// "guest has the mic" status auto-frame - the bridge itself parks only at exec
		// time, so this covers the staging window. Never queued, never replayed.
		if m.operatorHandoff != nil {
			// Staging window: the guest hasn't run yet (spend is $0 by definition - the
			// accumulator is only reset at exec, so the live figure here could still be a
			// PREVIOUS session's total and must not ride the frame). Model from the live holder.
			mdl := ""
			if m.proxyHolder != nil {
				mdl = m.proxyHolder.Get().Model
			}
			m.rcEmit(client.OperatorStatusFrame(m.operatorHandoff.det.Guest.Name, mdl, 0))
			return m, rearm
		}
		// A pre-launch plate is a LOCAL decision surface: a turn arriving while it is up
		// cancels the plate (never a blind exec under a busy DJ) and the turn proceeds.
		if m.operatorPlate != nil {
			m.operatorPlate = nil
			m.rcNote("the DJ picked up a turn - the hand-off plate was set aside · /operator to try again")
		}
		// Ensure the agent runtime exists (a remote turn can arrive before the local user
		// re-enters [0] AGENT). Inject through the SAME single-owner path local typing uses.
		if m.agent == nil {
			m.agent = m.newAgentRuntime()
		}
		if m.agentBusy || m.agent.running.Load() {
			// FIFO, drained when the turn ends. Tagged remote: at drain it is ALWAYS
			// submitted as a chat turn, never slash-dispatched - a remote "/operator"
			// (or /clear) must not control the host through the busy queue (ruling 7;
			// iteration-1 finding #1), exactly matching the idle path directly below.
			m.agentQueued = append(m.agentQueued, queuedPrompt{text: in.Text, remote: true})
			return m, rearm
		}
		nm, cmd := m.submitAgentPrompt(queuedPrompt{text: in.Text, remote: true})
		return nm, tea.Batch(cmd, rearm)
	case protocol.RCInConfirm:
		// Answer the pending confirm through its own resp channel (mirrors onAgentKey). The
		// answer MUST carry the id of the confirm it was shown for: a stale answer (for an
		// already-resolved confirm) is dropped so it can never resolve a DIFFERENT mutating
		// tool that became pending in the meantime. (An empty id is accepted for back-compat.)
		if c := m.agentPendingConfirm; c != nil && (in.ConfirmID == "" || in.ConfirmID == m.rcConfirmID) {
			m.agentPendingConfirm = nil
			m.rcConfirmID = ""
			verdict := "denied"
			if in.Approve {
				verdict = "approved"
			}
			m.agentLines = append(m.agentLines, "  "+stEmber.Render(glyphs.Fold("✓ "))+stDim.Render(verdict+" from "+in.Origin))
			m.rcEmitConfirmDone(in.Approve, in.Origin)
			c.resp <- in.Approve
			return m, tea.Batch(m.waitAgentEvent(), rearm)
		}
		return m, rearm
	case protocol.RCInBackfill:
		// Serve the transcript snapshot for a newly-attached viewer (content-blind: the host
		// owns the history; the broker never had it). Addressed to that ONE viewer.
		if m.rcBridge != nil {
			m.rcBridge.Emit(protocol.RCFrame{Kind: protocol.RCKindBackfill, Viewer: in.Viewer, Text: m.agentTranscriptText()})
		}
		return m, rearm
	default:
		return m, rearm
	}
}

// --- HOST tee: mirror local agent activity to viewers ---

func (m model) rcEmit(f protocol.RCFrame) {
	if m.rcBridge != nil {
		m.rcBridge.Emit(f)
	}
}

// rcTeeEvent mirrors one streamed harness.Event out to viewers. Called from onAgentEvent.
func (m model) rcTeeEvent(e harness.Event) {
	if m.rcBridge == nil {
		return
	}
	switch e.Kind {
	case harness.EventAssistant:
		m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: e.Text})
	case harness.EventToolCall:
		args, _ := json.Marshal(e.Args)
		m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindToolCall, Tool: e.Tool, Args: string(args)})
	case harness.EventToolResult:
		m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindToolResult, Tool: e.Tool, Text: e.Result})
	case harness.EventFinal:
		m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindFinal, Text: e.Text})
	case harness.EventError:
		m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindError, Text: e.Text})
	}
}

// rcEmitLocalTurn echoes a LOCALLY-typed turn to viewers (a remote turn is already echoed by
// the broker's /rc/send, so callers pass local turns only).
func (m model) rcEmitLocalTurn(text string) {
	m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindUser, Origin: "local", Text: text})
}

// rcEmitConfirmReq mirrors a pending tool-confirm to viewers so any surface can answer it. The
// id correlates a viewer's answer to THIS confirm.
func (m model) rcEmitConfirmReq(c *agentConfirm, id string) {
	if m.rcBridge == nil || c == nil {
		return
	}
	args, _ := json.Marshal(c.args)
	m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindConfirmReq, Tool: c.tool, Args: string(args), ConfirmID: id})
}

// rcEmitCleared tells viewers the host reset the session (so a queued-then-dropped local turn
// doesn't dangle as an unanswered echo on their side).
func (m model) rcEmitCleared() {
	m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindError, Text: "— host cleared the session —"})
}

// rcEmitDJBack tells viewers the DJ holds the mic again - after a guest-operator return
// AND after any exec-time abort (the staging guard may have told a remote "guest has the
// mic"; without this corrective frame an aborted handoff strands that surface). Nil-safe.
func (m model) rcEmitDJBack() {
	m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindStatus, Text: "the DJ is back at the desk"})
}

// rcEmitConfirmDone tells viewers a confirm was answered and by whom.
func (m model) rcEmitConfirmDone(approve bool, origin string) {
	if m.rcBridge == nil {
		return
	}
	a := approve
	m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindConfirmDone, Approve: &a, Origin: origin})
}

// ==========================================================================
// BASE STATION section (modePrivate)
// ==========================================================================

// privateFootnote is the DIM line at the foot of THE BAND: a live remote session earns the
// one red ◉ (it IS the LLM chat product); an idle base station stays fully dim. Absent when
// logged out or nothing to show.
func (m model) privateFootnote() string {
	if !m.loggedInState() {
		return ""
	}
	live := 0
	for _, s := range m.rcSessions {
		if s.Online && !s.Revoked {
			live++
		}
	}
	bands := len(m.rcBands)
	sessions := len(m.rcSessions)
	if m.rcBridge != nil && live == 0 {
		live = 1 // this machine is hosting even if the roster hasn't refreshed yet
	}
	if live == 0 && sessions == 0 && bands == 0 && m.rcBridge == nil {
		return ""
	}
	tail := glyphs.Fold("▸")
	if live > 0 {
		return "  " + stRed.Render(glyphOnAir+" live: "+plural(live, "remote session")) +
			stDim.Render(" · "+plural(bands, "private band")+" "+tail+" ") + stKey.Render("[p]")
	}
	return "  " + stDim.Render("base station: "+plural(bands, "private band")+" "+tail+" ") + stKey.Render("[p]")
}

// enterPrivate opens BASE STATION (a child screen of THE BAND). Login-gated.
func (m model) enterPrivate() (tea.Model, tea.Cmd) {
	if !m.loggedInState() {
		m.status = stDim.Render("base station needs an account - [L] to log in")
		return m, nil
	}
	m.rcPrevMode = m.mode
	m.mode = modePrivate
	m.rcCursor = 0
	m.status = stDim.Render("BASE STATION — your private side of the dial")
	return m, m.fetchRemoteRoster()
}

// fetchRemoteRoster loads the remote-session + private-band roster (both nil-safe).
func (m model) fetchRemoteRoster() tea.Cmd {
	broker := m.broker
	listRC, listBands := m.hooks.RCList, m.hooks.BandList
	return func() tea.Msg {
		var out remoteRosterMsg
		if listRC != nil {
			out.sessions, out.err = listRC(broker)
		}
		if listBands != nil {
			if bands, err := listBands(broker); err == nil {
				out.bands = bands
			}
		}
		return out
	}
}

func (m model) onRemoteRoster(msg remoteRosterMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.rcErr = msg.err.Error()
	} else {
		m.rcErr = ""
	}
	m.rcSessions = msg.sessions
	m.rcBands = msg.bands
	if m.rcCursor >= len(m.rcSessions) {
		m.rcCursor = len(m.rcSessions) - 1
	}
	if m.rcCursor < 0 {
		m.rcCursor = 0
	}
	return m, nil
}

// privateView renders BASE STATION: REMOTE SESSIONS (live first) then PRIVATE BANDS.
func (m model) privateView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString("  " + truncVisible(s, w-2) + "\n") }
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("BASE STATION") + stDim.Render("   your private side of the dial") + "\n")
	b.WriteString("  " + stRed.Render(glyphOnAir) + stDim.Render(" your account only · relayed through the broker · not end-to-end encrypted") + "\n\n")

	// REMOTE SESSIONS
	b.WriteString("  " + stKey.Render("REMOTE SESSIONS") + stDim.Render("   agent sessions live on your other machines · ⏎ continues") + "\n")
	if len(m.rcSessions) == 0 {
		line(stDim.Render("none yet — run /remote-control inside [0] AGENT on any machine"))
	}
	for i, s := range m.rcSessions {
		cursor := "  "
		if i == m.rcCursor {
			cursor = stSelText.Render("▸ ")
		}
		dot := stDim.Render("○")
		state := stDim.Render("offline")
		if s.Online && !s.Revoked {
			dot = stRed.Render(glyphOnAir)
			state = stLive.Render("live")
		} else if s.Revoked {
			state = stDim.Render("ended")
		}
		line(cursor + dot + " " + fmt.Sprintf("%-18s", trimName(s.Name)) + "  " + state)
	}

	// PRIVATE BANDS
	b.WriteString("\n  " + stKey.Render("PRIVATE BANDS") + stDim.Render("   hidden stations only a frequency code can tune") + "\n")
	if len(m.rcBands) == 0 {
		line(stDim.Render("none yet — roger share --private mints one (a one-time frequency code)"))
	}
	for _, bd := range m.rcBands {
		mark := stDim.Render("· ")
		if bd.Status == "active" {
			mark = stRed.Render(glyphOnAir + " ")
		}
		line(mark + fmt.Sprintf("%-16s", trimName(bd.Label)) + " " + stDim.Render(bd.Display))
	}
	b.WriteString("\n  " + stDim.Render("tune a code from elsewhere ") + stKey.Render("[~]") + "\n")
	if m.rcErr != "" {
		b.WriteString("  " + stRed.Render("✕ ") + stEmber.Render(m.rcErr) + "\n")
	}
	return b.String()
}

func trimName(s string) string { return truncVisible(s, 18) }

// onPrivateKey drives BASE STATION. j/k move; ⏎ opens a session; x revokes; ~ freq entry;
// esc returns to THE BAND. Unmatched keys fall through to the preset bank (windowshade + jumps).
func (m model) onPrivateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "left", "h", "q":
		m.mode = modeBrowse
		return m, nil
	case "1":
		m.mode = modeBrowse
		return m, nil
	case "up", "k":
		if m.rcCursor > 0 {
			m.rcCursor--
		}
		return m, nil
	case "down", "j":
		if m.rcCursor < len(m.rcSessions)-1 {
			m.rcCursor++
		}
		return m, nil
	case "r", "R":
		return m, m.fetchRemoteRoster() // refresh
	case "~":
		m.mode = modeFreqEntry
		m.freqIn.SetValue("")
		m.freqIn.Focus()
		m.status = stDim.Render("private freq · esc cancels")
		return m, textinput.Blink
	case "enter":
		if m.rcCursor >= 0 && m.rcCursor < len(m.rcSessions) {
			return m.enterRemoteSession(m.rcSessions[m.rcCursor])
		}
		return m, nil
	case "x", "X":
		if m.rcCursor >= 0 && m.rcCursor < len(m.rcSessions) {
			return m, m.revokeRemoteSession(m.rcSessions[m.rcCursor].ID)
		}
		return m, nil
	}
	if nm, cmd, ok := m.presetForKey(k.String()); ok {
		return nm, cmd
	}
	return m, nil
}

func (m model) revokeRemoteSession(id string) tea.Cmd {
	broker, revoke := m.broker, m.hooks.RCRevoke
	if revoke == nil {
		return nil
	}
	return func() tea.Msg {
		_ = revoke(broker, id)
		// re-list after revoke
		var out remoteRosterMsg
		if m.hooks.RCList != nil {
			out.sessions, out.err = m.hooks.RCList(broker)
		}
		if m.hooks.BandList != nil {
			out.bands, _ = m.hooks.BandList(broker)
		}
		return out
	}
}

// ==========================================================================
// modeRemoteSession: the in-TUI VIEWER of a session hosted elsewhere
// ==========================================================================

// remoteAttachedMsg carries the owner-join result (an attach token for one of MY sessions).
type remoteAttachedMsg struct {
	gen   int
	row   RemoteSessionRow
	token string
	err   error
}

// enterRemoteSession opens the in-TUI viewer for one of MY sessions hosted elsewhere. Because
// the roster carries no link code (the code is shown once on the host), an OWNER attaches to
// their OWN session by id (same-account is sufficient — the code is only for linking a
// NOT-logged-in device). RCJoin mints the attach token; then the SSE stream opens.
func (m model) enterRemoteSession(row RemoteSessionRow) (tea.Model, tea.Cmd) {
	if m.rcBridge != nil && m.rcBridge.SessionID() == row.ID {
		m.status = stDim.Render("this session is hosted HERE — it's your [0] AGENT")
		return m, nil
	}
	if m.hooks.RCJoin == nil || m.hooks.RCStream == nil {
		m.status = stDim.Render("continue this session from `roger remote attach <code>` or the web console")
		return m, nil
	}
	m.rsRow = row
	m.rsLines = nil
	m.rsSeq = 0
	m.rsAttach = ""
	m.rsPendingConfirm = false
	m.rsConfirmID = ""
	m.rsGen++ // a new session generation; frames/ends from an older one are ignored
	gen := m.rsGen
	m.rsVP = viewport.New(m.effWidth(), 10)
	ti := textinput.New()
	ti.Placeholder = "ask from here — it runs on the host"
	ti.Focus()
	m.rsIn = ti
	m.rcPrevMode = modePrivate
	m.mode = modeRemoteSession
	m.status = stRed.Render(glyphOnAir+" LIVE") + stDim.Render(" · attaching…")
	broker, join := m.broker, m.hooks.RCJoin
	return m, func() tea.Msg {
		token, err := join(broker, row.ID)
		return remoteAttachedMsg{gen: gen, row: row, token: token, err: err}
	}
}

// onRemoteAttached starts the SSE stream once the owner-join returns an attach token.
func (m model) onRemoteAttached(msg remoteAttachedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.rsGen || m.mode != modeRemoteSession {
		return m, nil // the user navigated away before the attach returned
	}
	if msg.err != nil {
		m.status = stRed.Render("✕ ") + stEmber.Render("could not attach: "+msg.err.Error())
		return m, nil
	}
	m.rsAttach = msg.token
	m.status = stRed.Render(glyphOnAir+" LIVE") + stDim.Render(" · "+msg.row.Name)
	frames := make(chan protocol.RCFrame, 64)
	ctx, cancel := context.WithCancel(context.Background())
	m.rsFrames = frames
	m.rsCancel = cancel
	gen := m.rsGen
	broker, stream := m.broker, m.hooks.RCStream
	sid, attach, since := m.rsRow.ID, m.rsAttach, m.rsSeq
	go func() {
		_ = stream(ctx, broker, sid, attach, since, func(f protocol.RCFrame) {
			select {
			case frames <- f:
			case <-ctx.Done():
			}
		})
		close(frames)
	}()
	return m, waitRemoteFrame(frames, gen)
}

// reArmRemoteStream reads the next streamed frame from the live viewer channel.
func (m model) reArmRemoteStream() tea.Cmd {
	if m.rsFrames == nil {
		return nil
	}
	return waitRemoteFrame(m.rsFrames, m.rsGen)
}

func waitRemoteFrame(ch chan protocol.RCFrame, gen int) tea.Cmd {
	return func() tea.Msg {
		f, ok := <-ch
		if !ok {
			return remoteViewerEndMsg{gen: gen}
		}
		return remoteFrameMsg{gen: gen, f: f}
	}
}

// onRemoteFrame renders a streamed frame into the viewer transcript. A frame from a STALE
// generation (an older session whose stream is still tearing down) is ignored.
func (m model) onRemoteFrame(msg remoteFrameMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.rsGen {
		return m, nil
	}
	f := msg.f
	if f.Seq > m.rsSeq {
		m.rsSeq = f.Seq
	}
	switch f.Kind {
	case protocol.RCKindUser:
		who := f.Origin
		if who == "" {
			who = "someone"
		}
		m.rsLines = append(m.rsLines, stSelText.Render("▸ ")+stDim.Render("("+who+") ")+f.Text)
	case protocol.RCKindAssistant, protocol.RCKindFinal:
		if strings.TrimSpace(f.Text) != "" {
			m.rsLines = append(m.rsLines, stLive.Render("◂ ")+f.Text)
		}
	case protocol.RCKindToolCall:
		m.rsLines = append(m.rsLines, "  "+stKey.Render(glyphOnAir+" "+f.Tool))
	case protocol.RCKindToolResult:
		m.rsLines = append(m.rsLines, "  "+stDim.Render("✓ "+f.Tool))
	case protocol.RCKindConfirmReq:
		// A real pending-confirm flag (+ its id) gates the y/n keys — not a fragile string match.
		m.rsPendingConfirm = true
		m.rsConfirmID = f.ConfirmID
		m.rsLines = append(m.rsLines, "  "+stEmber.Render("? "+f.Tool)+stDim.Render("  [y] approve · [n] deny (runs on the host)"))
	case protocol.RCKindConfirmDone:
		m.rsPendingConfirm = false
		v := "denied"
		if f.Approve != nil && *f.Approve {
			v = "approved"
		}
		m.rsLines = append(m.rsLines, "  "+stDim.Render("✓ "+v+" from "+f.Origin))
	case protocol.RCKindStatus:
		// A guest-operator handoff (or the DJ-back return) - render it so the viewer never
		// sees the stream go dead mid-handoff. Operator-aware + content-blind: only the guest
		// name plus the model/spend metadata ride the frame, matching the web console. The ONE
		// shared client.OperatorStatusLine formatter keeps this copy from drifting between the
		// TUI, the `roger remote` CLI, and (mirrored) the web console - the enriched piecewise
		// line "<op> has the mic on <model> · $<spend>" degrading to the bare handoff line, then
		// the plain DJ-back text. glyphOnAir is this surface's on-air marker.
		if line := client.OperatorStatusLine(f, glyphOnAir); strings.TrimSpace(line) != "" {
			m.rsLines = append(m.rsLines, stDim.Render(line))
		}
	case protocol.RCKindBackfill:
		if strings.TrimSpace(f.Text) != "" {
			m.rsLines = append([]string{stDim.Render(f.Text)}, m.rsLines...)
		}
	case protocol.RCKindError:
		m.rsLines = append(m.rsLines, stRed.Render("✕ ")+stEmber.Render(f.Text))
	case protocol.RCKindEnded:
		m.rsPendingConfirm = false
		m.rsLines = append(m.rsLines, stDim.Render("— session ended on the host —"))
		m.status = stDim.Render("session ended · esc back")
		return m, nil
	}
	m.rsVP.SetContent(strings.Join(m.rsLines, "\n"))
	m.rsVP.GotoBottom()
	return m, nil
}

func (m model) remoteSessionView(w int) string {
	var b strings.Builder
	title := "REMOTE SESSION"
	if m.rsRow.Name != "" {
		title += " · " + m.rsRow.Name
	}
	dot := stRed.Render(glyphOnAir + " LIVE")
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render(title) + "   " + dot + "\n")
	b.WriteString("  " + stRed.Render(glyphOnAir) + stDim.Render(" PRIVATE · your account only · broker relay · tools run on the host") + "\n\n")
	m.rsVP.Width, m.rsVP.Height = w-2, max(6, m.height-10)
	m.rsVP.SetContent(strings.Join(m.rsLines, "\n"))
	b.WriteString(m.rsVP.View() + "\n\n")
	b.WriteString("  " + stSelText.Render("▸ ") + m.rsIn.View() + "\n")
	return b.String()
}

// onRemoteSessionKey drives the viewer: ⏎ sends a turn, y/n answer a pending confirm, esc back.
func (m model) onRemoteSessionKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		if m.rsCancel != nil {
			m.rsCancel() // stop the viewer SSE goroutine
			m.rsCancel = nil
		}
		m.rsFrames = nil
		m.mode = modePrivate
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.rsIn.Value())
		if text == "" {
			return m, nil
		}
		m.rsIn.SetValue("")
		return m, m.sendRemoteTurn(protocol.RCInbound{Kind: protocol.RCInTurn, Text: text})
	case "y", "Y", "n", "N":
		// y/n answers a confirm ONLY while one is actually pending (a real flag set by the
		// last confirm_req frame, cleared by confirm_done); otherwise the letter is typed into
		// the input (so a user can write words containing y/n). The answer carries the confirm
		// id so a stale answer can never resolve a different confirm on the host.
		if m.rsPendingConfirm {
			approve := k.String() == "y" || k.String() == "Y"
			m.rsPendingConfirm = false
			return m, m.sendRemoteTurn(protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: approve, ConfirmID: m.rsConfirmID})
		}
	}
	var cmd tea.Cmd
	m.rsIn, cmd = m.rsIn.Update(k)
	return m, cmd
}

func (m model) sendRemoteTurn(in protocol.RCInbound) tea.Cmd {
	broker, send := m.broker, m.hooks.RCSend
	sid, attach := m.rsRow.ID, m.rsAttach
	if send == nil {
		return nil
	}
	return func() tea.Msg {
		_ = send(broker, sid, attach, in)
		return nil
	}
}
