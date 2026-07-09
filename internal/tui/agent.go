package tui

// [0] AGENT - the embedded, tool-capable agent harness (the v0.4.0 "harness" vision).
// A small, active, session-only agent driven by the dj.md persona: it runs a real
// OpenAI tool-use loop on the model on the current channel (relayed through the
// broker, dogfooding the marketplace), executes a bounded, confirm-gated set of
// built-in tools, and streams the turn into the AGENT transcript. NO persistent
// memory - just this conversation.
//
// Concurrency: the harness loop is blocking (a model call, then maybe a y/N confirm
// mid-loop), so it runs in a goroutine and talks to the single-threaded Bubble Tea
// model over channels. The loop emits Events onto `events`; a recurring tea.Cmd
// (waitAgentEvent) drains them into the model as agentEventMsg. A mutating tool's
// Confirmer sends an agentConfirm onto `confirmReq` and BLOCKS on `confirmResp`; the
// TUI shows a y/N and writes the answer back, so the loop never runs a side-effecting
// tool without an on-screen approval.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
)

// The AGENT runs on the TUNED-IN channel's model - never a stale config/default
// model. With nothing tuned in, the runtime model is empty and the agent shows an
// up-front "tune in / share" hint instead of silently 504-ing on a model the user
// never chose (the founder's "on gpt-oss-20b ... status 504 with no reply" dead end).

// agentRuntime owns the live harness loop + the channels that bridge the blocking
// loop goroutine to the Bubble Tea event loop. It is stored by pointer on the model
// so it survives Bubble Tea's by-value model copies.
type agentRuntime struct {
	loop  *harness.Loop
	model string // the TUNED-IN channel model the agent runs on ("" = nothing tuned in)
	// events carries streamed steps of the in-flight turn (assistant text, tool calls,
	// results, the final answer, errors). Buffered so the loop goroutine never blocks
	// on a slow UI frame.
	events chan harness.Event
	// confirmReq carries a pending mutating-tool confirm to the UI; confirmResp carries
	// the user's y/N answer back to the (blocked) loop goroutine.
	confirmReq  chan agentConfirm
	confirmResp chan bool
	// cancel aborts the in-flight turn (esc): it cancels the context threaded into the
	// harness loop's model call, so a hung/slow station call is dropped at once, no
	// further steps fire (no more billing), and input is handed back. nil between turns.
	cancel context.CancelFunc
	// running is true from the instant a turn's goroutine is launched until it returns
	// (after Send + close). It is SEPARATE from the UI's agentBusy: a force-stop (a second
	// esc) clears agentBusy to free the prompt immediately, but the goroutine may still be
	// unwinding (e.g. a run_shell that ignores ctx self-terminates at its own timeout).
	// Because that goroutine still owns the single shared loop, a new turn must NOT start
	// until running clears - submitAgentPrompt checks this and queues instead, so we never
	// race two turns on one loop. Written by the goroutine, read on the UI goroutine, so it
	// is atomic.
	running atomic.Bool
}

// agentConfirm is one pending confirm for a side-effecting tool, surfaced as a y/N
// prompt. resp is the channel the loop goroutine blocks on for the answer.
type agentConfirm struct {
	tool string
	args map[string]any
	resp chan bool
}

// summary renders the confirm as a single, obvious line (the tool + its key arg).
func (c agentConfirm) summary() string {
	switch c.tool {
	case "run_shell":
		return "run_shell: " + argStr(c.args["cmd"])
	case "write_file":
		return "write_file: " + argStr(c.args["path"]) + fmt.Sprintf(" (%d bytes)", len(argStr(c.args["content"])))
	default:
		return c.tool
	}
}

// agent message types (the goroutine -> Bubble Tea bridge).
type (
	// agentEventMsg delivers one streamed Event from the running loop.
	agentEventMsg harness.Event
	// agentConfirmMsg pauses the turn for a y/N on a mutating tool.
	agentConfirmMsg agentConfirm
	// agentDoneMsg marks the turn finished (the events channel closed), re-enabling input
	// and auto-sending the next queued prompt (if any).
	agentDoneMsg struct{}
	// agentCostMsg adds one model-call's BILLED result — cost + the broker's billed
	// prompt/completion token counts — to the running AGENT session totals (the cost
	// side-channel; see newAgentRuntime.costFn and waitAgentEvent).
	agentCostMsg struct {
		cost      float64
		tokensIn  int
		tokensOut int
		tps       float64 // the LATEST call's throughput (tokens/sec); not summed
	}
)

// resolveAgentModel picks the model the agent should run on, in priority order:
//
//	(a) the currently-open channel (m.connected.Model), else
//	(b) the LAST model tuned in this session (m.lastConnected.Model - the sticky band
//	    the disconnect fix keeps), so "esc out of the channel -> [0] AGENT" just reuses
//	    the model you were JUST on instead of dead-ending on "no model".
//
// "" means neither is available (truly nothing tuned in - the up-front hint / picker
// decide what to do next). It is a pure read of the current model; no mutation.
func (m model) resolveAgentModel() string {
	if m.connected != nil && m.connected.Model != "" {
		return m.connected.Model
	}
	if m.lastConnected != nil && m.lastConnected.Model != "" {
		return m.lastConnected.Model
	}
	return ""
}

// agentModelCandidates is the set of models the /model picker can choose from, in a
// stable, useful order with no duplicates: the currently-resolved model first, then
// the rest of this session's tuned-in models (the sticky last band + recent bands),
// then any other CHAT model currently ON AIR in the discover band list. This is "the
// model(s) I could plausibly point the agent at right now" - and the agent runs on
// the chat relay, so a voice (tts/stt) band is never offered as a brain (band.isVoice,
// the same canonical-modality read the band table groups by): picking one could only
// fail the next turn. The session legs are chat-only by construction - a voice band
// diverts to the preview and never opens a channel (voice.go).
func (m model) agentModelCandidates() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(m.resolveAgentModel())     // the model we'd use right now leads
	add(m.lastConnected.modelOr()) // the sticky last-tuned band
	for mdl := range m.recentBands {
		add(mdl) // every model tuned in this session
	}
	for _, b := range m.bands {
		if b.online && !b.isVoice() {
			add(b.model) // any CHAT band currently on air in the discover list
		}
	}
	return out
}

// modelOr is a nil-safe read of an *offer's model ("" when nil), so candidate
// gathering can fold in the sticky band without a guard at every call site.
func (o *offer) modelOr() string {
	if o == nil {
		return ""
	}
	return o.Model
}

// enterAgent opens the AGENT mode, building the runtime lazily on first entry. The
// agent runs on the resolved model (the open channel, else the LAST band tuned in this
// session); if neither is available it runs on no model and shows an up-front "tune in
// / share" hint (never a stale default that 504s). It loads the dj.md persona (writing
// the shipped default on first run if absent) and seeds a one-line welcome into the
// transcript. Re-entering keeps the existing session, but re-resolves the model so a
// channel tuned in AFTER first entry is picked up.
func (m model) enterAgent() (tea.Model, tea.Cmd) {
	m.mode = modeAgent
	if m.agent == nil {
		m.agent = m.newAgentRuntime()
		if m.agent.model != "" {
			m.agentLines = append(m.agentLines,
				stDim.Render("· ")+stDim.Render("AGENT on air - running on ")+stKey.Render(m.agent.model)+stDim.Render(" · dj.md persona · session-only (no memory)"),
				stDim.Render("· ")+stDim.Render("/model switches model · read/list/fetch run on their own · write/run ask first · files sandboxed to "+m.agent.loop.Root+" · run_shell runs there but is NOT sandboxed"),
			)
		} else if m.proxyHolder == nil {
			// FRESH: nothing has ever been tuned in this session (no endpoint bound). The
			// old behavior dropped into a dead ask box and spammed "no station on air" on
			// every turn. Instead: a calm welcome + a SILENT background auto-tune (R1/R6)
			// that finds a FREE band with no spend. The ask box stays focused (the DJ types
			// through); when the async desk scan lands GUESTS, THE DESK takes focus as the
			// selectable operator picker (R3, onOperatorDetected). The auto-tune outcome is
			// noted once, when it resolves - never a per-turn "no station" pile-up.
			m.agentLines = append(m.agentLines,
				stDim.Render("· ")+stDim.Render("AGENT ready · dj.md persona · session-only (no memory)"))
			m.autoTuneBeatLen = len(m.agentLines) // the beat below is swapped for the outcome
			m.agentLines = append(m.agentLines,
				agentFindingBandBeat())
			m.agentLandingLines = len(m.agentLines)
			m.autoTuning = true
			m.agentIn.Focus()
			m.status = stDim.Render("AGENT ready · esc exits")
			return m, tea.Batch(textinput.Blink, operatorScanCmd(), autoTuneCmd(m.broker, m.scanned))
		} else {
			// A proxy holder exists but no model resolves (a disconnected / oddly-seeded
			// session): keep the honest up-front hint - the turn is still allowed and falls
			// into the same actionable hint.
			m.agentLines = append(m.agentLines,
				stDim.Render("· ")+stDim.Render("AGENT ready · dj.md persona · session-only (no memory)"),
				stRed.Render("✕ ")+stEmber.Render("no model tuned in"),
				hintTuneOrShare(m.narrow()),
			)
		}
		// Snapshot the entry chrome length: the LANDING state (where THE DESK roster may
		// render) is "nothing in the transcript beyond these welcome lines". /clear resets
		// both, so the landing - and the roster - come back with a fresh session.
		m.agentLandingLines = len(m.agentLines)
	} else {
		// Re-entry: pick up a channel tuned in since we last built the runtime (or fall
		// back to the last band tuned in this session) so the agent never runs on a model
		// that no longer matches what the user just had.
		m.refreshAgentModel()
	}
	m.agentIn.Focus()
	// Set the generic "AGENT ready" only when a model IS tuned in (or a silent auto-tune is
	// in flight finding one): otherwise preserve the more-specific "no model tuned in" status
	// (refreshAgentModel sets it on re-entry; set it here too for the fresh no-model landing)
	// instead of clobbering it (finding 2026-07-08). The autoTuning guard keeps the status
	// from contradicting the still-up "finding a free band…" beat on a re-entry mid-tune.
	if m.agent.model != "" || m.autoTuning {
		m.status = stDim.Render("AGENT ready · esc exits")
	} else {
		m.status = agentNoModelStatus()
	}
	// Async desk scan (Guest Operators): LookPath + bounded version probes off the event
	// loop, landing as operatorDetectedMsg - the same pattern as onSharesDetected.
	return m, tea.Batch(textinput.Blink, operatorScanCmd())
}

// refreshAgentModel re-resolves the agent's model (open channel, else this session's
// last-tuned band). It is a no-op when the model already matches; on a change it
// updates the runtime and drops a one-line note into the transcript so the heading +
// the next turn run on the right model. It NEVER overrides a model the user picked
// explicitly via /model (unless a fresh channel is opened on top), and it only shows
// "no model" when there is genuinely none - the disconnect-then-[0] dead end is gone
// (lastConnected carries the model across the disconnect).
func (m *model) refreshAgentModel() {
	if m.agent == nil {
		return
	}
	// A model chosen explicitly in this session (via /model) stays put unless a fresh
	// channel is opened on top of it - the user's pick wins over auto-resolution.
	if m.agentPicked && m.connected == nil {
		return
	}
	want := m.resolveAgentModel()
	if want == m.agent.model {
		return
	}
	m.agent.model = want
	switch {
	case want != "":
		m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render("the agent now runs on ")+stKey.Render(want))
	default:
		// No model resolves - a STATUS note, not a transcript line, so re-entries / turns
		// never stack "no model tuned in" (founder spam regression). The actual failure,
		// if the user sends a turn anyway, still surfaces once via the deduped failureHint.
		m.status = agentNoModelStatus()
	}
}

// agentNoModelStatus is the status line shown when the AGENT has no model tuned in - the
// ONE place that copy lives, so refreshAgentModel and enterAgent never drift (enterAgent
// used to clobber a fresh no-model status with the generic "AGENT ready" on re-entry).
func agentNoModelStatus() string {
	return stRed.Render("✕ ") + stEmber.Render("no model tuned in") + stDim.Render(" · [1] tune in · [2] go on air")
}

// agentFindingBandBeat is the single "finding a free band…" transcript beat, shared by
// enterAgent's fresh landing and submitAgentPrompt's park path so the prefix never drifts
// (finding 2026-07-08: enterAgent used "· ", submitAgentPrompt used the on-air glyph).
func agentFindingBandBeat() string {
	return stDim.Render("· ") + stDim.Render("finding a free band…")
}

// pickAgentModel re-points the agent at the chosen model and notes the switch. It sets
// agentPicked so refreshAgentModel (which fires on every re-entry / turn) does not snap
// it back to the auto-resolved model - the user's explicit choice sticks for the rest
// of the session unless they open a new channel.
func (m *model) pickAgentModel(mdl string) {
	if m.agent == nil || mdl == "" {
		return
	}
	m.agentPicked = true
	if mdl == m.agent.model {
		m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render("already running on ")+stKey.Render(mdl))
		return
	}
	m.agent.model = mdl
	m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render("switched - the agent now runs on ")+stKey.Render(mdl))
}

// newAgentRuntime builds the harness loop + bridge channels. The completer relays
// through the broker (so the agent dogfoods the marketplace); the confirmer sends a
// pending confirm to the UI and blocks for the answer. costFn feeds per-turn relay
// cost back to the model via the events drain (a side channel on agentCostMsg).
func (m model) newAgentRuntime() *agentRuntime {
	// The open channel, else this session's last-tuned band (so "tune in -> esc ->
	// [0]" reuses the model you just had); "" only when truly nothing is/was tuned in.
	mdl := m.resolveAgentModel()
	rt := &agentRuntime{
		model:       mdl,
		events:      make(chan harness.Event, 32),
		confirmReq:  make(chan agentConfirm),
		confirmResp: make(chan bool),
	}
	// Cost + the broker's BILLED token counts are surfaced through the events channel as a
	// single sentinel ("<credits> <in> <out>") so the lone drain Cmd stays the only reader
	// (no second goroutine racing the model). waitAgentEvent parses the triple back out.
	costFn := func(credits float64, in, out int, tps float64) {
		rt.events <- harness.Event{Kind: eventCost, Text: fmt.Sprintf("%g %d %d %g", credits, in, out, tps)}
	}
	// The completer reads rt.model LIVE (not a captured value) so re-tuning a channel
	// after the runtime is built takes effect on the next turn without a rebuild.
	completer := func(ctx context.Context, messages []harness.Message, tools []map[string]any) (harness.Message, error) {
		if rt.model == "" {
			return harness.Message{}, fmt.Errorf("no station on air - no model is tuned in")
		}
		// Carry the user's explicit out-price cap for the live model (0 -> the default
		// consumer cap applies broker-side); the agent relay is bounded like `use`/chat.
		maxOut := m.limits.resolve(rt.model).MaxOut
		return harness.BrokerCompleter(m.broker, m.user, rt.model, m.confidentialOnly, maxOut, costFn)(ctx, messages, tools)
	}
	confirmer := func(tool string, args map[string]any) bool {
		c := agentConfirm{tool: tool, args: args, resp: make(chan bool, 1)}
		rt.confirmReq <- c // surfaced to the UI as agentConfirmMsg
		return <-c.resp    // blocks until the user answers y/N
	}
	persona := harness.LoadPersona(harness.PersonaPath())
	rt.loop = harness.NewLoop(agentRoot(), persona, completer, confirmer)
	return rt
}

// eventCost is a private EventKind sentinel used only on the in-process events
// channel to carry a relay cost without a second goroutine reading the channel. It is
// distinct from the harness.EventKind values (which start at 0) by being far out of
// their range, so a real harness event is never mistaken for a cost tick.
const eventCost = harness.EventKind(1000)

// bandForModel finds the discover band for a model id (false when it is not on the
// current dial - e.g. a session-recent model whose station has aged out).
func (m model) bandForModel(mdl string) (band, bool) {
	for _, b := range m.bands {
		if b.model == mdl {
			return b, true
		}
	}
	return band{}, false
}

// modelBadgeTail is the short flag tail the /model picker appends to a candidate row:
// the same agent-ready ⌁ (inferred ⌁~) / vision ◪ / FREE marks the band table shows, so
// picking a model is an informed choice. "" when the model is not on the current dial.
func (m model) modelBadgeTail(mdl string) string {
	b, ok := m.bandForModel(mdl)
	if !ok {
		return ""
	}
	var parts []string
	if tag := agentReadyTag(b); tag != "" {
		parts = append(parts, tag)
	}
	if b.vision {
		parts = append(parts, visionGlyph())
	}
	if b.free {
		parts = append(parts, "FREE")
	}
	return strings.Join(parts, " ")
}

// deskRowCount is the number of selectable desk rows when THE DESK has focus: the
// resident DJ (always row 0) plus one row per detected guest.
func (m model) deskRowCount() int {
	return 1 + len(deskGuests(m.operatorDetections))
}

// isPrintableKey reports whether a key press is a printable character (a rune or a
// space) - the class that falls THROUGH the focused desk into the ask box (R3). Nav /
// control keys (arrows, esc, enter, tab, ctrl+*, pgup) are not printable.
func isPrintableKey(k tea.KeyMsg) bool {
	return k.Type == tea.KeyRunes || k.Type == tea.KeySpace
}

// onAgentKey handles keys while in AGENT mode. A pending mutating-tool confirm owns
// every key (y runs, n/esc denies - default DENY). Otherwise it is a text-entry mode:
// enter submits a turn (a leading / is a local command), esc exits to BROWSE, and all
// other keys feed the prompt input. Because this owns its keys (and never consults
// presetForKey), a typed `0` is a literal digit, NEVER a re-entry into AGENT.
func (m model) onAgentKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A live guest-operator handoff (staging or execing) owns the terminal: no key
	// reaches the TUI until the exec callback returns the desk.
	if m.operatorHandoff != nil {
		return m, nil
	}
	// The pre-launch plate owns every key while up (Phase 3): y/enter accepts (twice on
	// exactly-$HOME), n/esc cancels, b cycles the ceiling - deny is the default, and the
	// accept can ONLY come from this local keyboard (the RC money-confirm invariant).
	if m.operatorPlate != nil {
		return m.onOperatorPlateKey(k)
	}
	// The /operator picker owns every key while open (same modal contract as /model).
	if m.operatorPicker {
		return m.onOperatorPickerKey(k)
	}
	// The /model picker owns every key while open (arrow + enter to choose, esc to
	// cancel) so a digit/preset/left-right is NEVER stolen out from under it.
	if m.agentPicker {
		switch k.String() {
		case "up", "k":
			if m.agentPickerCursor > 0 {
				m.agentPickerCursor--
			}
			return m, nil
		case "down", "j":
			if m.agentPickerCursor < len(m.agentPickerRows)-1 {
				m.agentPickerCursor++
			}
			return m, nil
		case "enter":
			if m.agentPickerCursor >= 0 && m.agentPickerCursor < len(m.agentPickerRows) {
				m.pickAgentModel(m.agentPickerRows[m.agentPickerCursor])
			}
			m.agentPicker = false
			m.agentPickerRows = nil
			return m, nil
		case "esc":
			m.agentPicker = false
			m.agentPickerRows = nil
			m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render("kept the current model"))
			return m, nil
		default:
			return m, nil // swallow everything else - the picker is modal
		}
	}
	// A pending confirm modal: answer the y/N gate for the side-effecting tool.
	if c := m.agentPendingConfirm; c != nil {
		switch k.String() {
		case "y", "Y":
			m.agentLines = append(m.agentLines, "  "+stLive.Render("✓ ")+stDim.Render("approved · running "+c.tool))
			m.agentPendingConfirm = nil
			m.rcConfirmID = "" // BASE STATION: this confirm is resolved; a late remote answer is now stale
			m.rcEmitConfirmDone(true, "local")
			c.resp <- true
			return m, m.waitAgentEvent()
		default: // n / N / esc / anything else - default DENY
			m.agentLines = append(m.agentLines, "  "+stRed.Render("✕ ")+stEmber.Render("denied · "+c.tool+" was not run"))
			m.agentPendingConfirm = nil
			m.rcConfirmID = ""
			m.rcEmitConfirmDone(false, "local")
			c.resp <- false
			return m, m.waitAgentEvent()
		}
	}
	// THE DESK has focus (the [0] landing with nothing tuned in, R3): arrows move the
	// operator cursor, Enter on the DJ focuses the ask box, Enter on a guest opens the
	// pre-launch plate (auto-tuning first if there is no channel). ANY printable rune
	// falls through to the ask box and de-focuses the desk (the DJ-still-types-through
	// path); esc / scroll / control keys fall through to the normal handling below.
	if m.deskFocused {
		switch k.String() {
		case "up":
			if m.deskCursor > 0 {
				m.deskCursor--
			}
			return m, nil
		case "down":
			if m.deskCursor < m.deskRowCount()-1 {
				m.deskCursor++
			}
			return m, nil
		case "enter":
			if m.deskCursor <= 0 {
				// The resident DJ: hand focus to the ask box.
				m.deskFocused = false
				m.agentIn.Focus()
				m.status = stDim.Render(djHasMicStatus)
				return m, textinput.Blink
			}
			ds := deskGuests(m.operatorDetections)
			if idx := m.deskCursor - 1; idx >= 0 && idx < len(ds) {
				m.deskFocused = false
				return m.startOperatorHandoff(ds[idx], false)
			}
			return m, nil
		}
		if isPrintableKey(k) {
			// Type-through: the DJ is implied. De-focus the desk and let the rune land in
			// the ask box via the text-entry Update below. Clear the focused-desk hint so the
			// status line stops advertising arrow-selection (mirrors the enter-on-DJ path).
			m.deskFocused = false
			m.agentIn.Focus()
			m.status = stDim.Render(djHasMicStatus)
		}
	}
	// Text-entry mode: enter submits (or QUEUES while a turn runs - see the enter case),
	// esc cancels/leaves, the scroll/recall/copy keys below act, and everything else feeds
	// the prompt input - typable even mid-turn so the next ask can be composed + queued.
	// Any key but Tab ends a slash-completion cycle first: the strip then re-derives
	// from what is actually typed (the tab case below steps the SAME candidate set).
	if k.String() != "tab" {
		m.agentTabPrefix, m.agentTabIdx = "", 0
	}
	switch k.String() {
	case "esc":
		// While a turn is in flight, esc CANCELS it; when idle, esc leaves to BROWSE. This is
		// the fix for "the agent hung on a slow station and I couldn't get out or stop the
		// spend", in two presses so a lagging/wedged turn can NEVER trap the user:
		//   1st esc - graceful: abort the model call + stop further steps/billing, and wait a
		//             beat for the loop to unwind cleanly (the EventError + agentDoneMsg that
		//             follow re-enable the prompt on their own).
		//   2nd esc - force: hand the prompt back NOW even if the goroutine's HTTP abort lags
		//             or a tool ignores ctx; the loop unwinds in the background (rt.running
		//             keeps the next turn from racing the shared loop). No more "cancelling…"
		//             dead end.
		if m.agentBusy {
			if m.agent != nil && m.agent.cancel != nil {
				m.agent.cancel() // idempotent; make sure the abort is in flight on either press
			}
			if !m.agentCanceling {
				m.agentCanceling = true
				m.status = stDim.Render("cancelling the turn… (esc again to force-stop)")
				return m, nil
			}
			// Second esc: force the UI back to a usable prompt immediately.
			m.agentBusy = false
			m.agentCanceling = false
			m.agentTurnState = poseWaiting
			m.agentLines = append(m.agentLines, stRed.Render("✕ ")+stEmber.Render("turn stopped"))
			m.status = stDim.Render("turn stopped · ask again, or esc to leave AGENT")
			return m, nil
		}
		m.agentIn.Blur()
		// Clear the DESK focus on the way out, so a re-entry never lands in a dual-focus
		// state (the ask box focused AND the desk focused). enterAgent re-focuses the ask
		// box and any fresh scan re-arms the desk from a known-clean base.
		m.deskFocused = false
		// Tear down any in-flight silent auto-tune and drop the prompts parked while no band
		// was tuned: otherwise the async /discover result lands AFTER we left - binding a band
		// and firing a phantom parked turn outside AGENT (audit finding). Mirror the clean
		// disarm (clearFindingBeat + flush).
		m.autoTuning = false
		m.clearFindingBeat()
		m.flushPendingPrompts()
		m.mode = modeBrowse
		m.status = stDim.Render("left AGENT - the session is kept · [0] returns")
		return m, nil
	case "ctrl+y":
		// Yank the agent transcript to the clipboard (OSC 52 + local tool), with the same
		// prominent "✓ Copied to clipboard" toast as the channel. Plain `y` types into the
		// prompt, so copy is ctrl+y (and /copy). Works mid-turn too.
		txt := m.agentTranscriptText()
		if strings.TrimSpace(txt) == "" {
			m.status = stDim.Render("nothing to copy yet · drag to select text")
			return m, nil
		}
		m.status = copiedToast("the agent transcript")
		return m, clipboardWrite(txt)
	case "pgup":
		// Scroll the transcript - works even while a turn streams, so a long answer or
		// tool dump can be read back without losing the live turn.
		m.agentVP.PageUp()
		return m, nil
	case "pgdown":
		m.agentVP.PageDown()
		return m, nil
	case "ctrl+u":
		m.agentVP.HalfPageUp()
		return m, nil
	case "ctrl+d":
		m.agentVP.HalfPageDown()
		return m, nil
	case "up":
		// Shell-style recall on the AGENT prompt: Up walks to an OLDER sent prompt
		// (stashing the live draft on the first Up). Distinct from the chat's history.
		// The /model picker (which uses up/k) returns earlier, so this only fires while
		// the prompt itself is focused. With nothing to recall (or while a turn streams,
		// when edits are ignored) Up scrolls the transcript up a line instead.
		if !m.agentBusy {
			if v, ok := m.agentHist.prev(m.agentIn.Value()); ok {
				m.agentIn.SetValue(v)
				m.agentIn.CursorEnd()
				return m, nil
			}
		}
		m.agentVP.ScrollUp(1)
		return m, nil
	case "down":
		// Down walks to a NEWER sent prompt; past the newest it restores the draft. With
		// nothing to recall (or while busy) it scrolls the transcript down a line.
		if !m.agentBusy {
			if v, ok := m.agentHist.next(); ok {
				m.agentIn.SetValue(v)
				m.agentIn.CursorEnd()
				return m, nil
			}
		}
		m.agentVP.ScrollDown(1)
		return m, nil
	case "tab":
		// Slash-command autocomplete (see agentCommands): a unique prefix match fills
		// the input + a trailing space (word done - ready for args or enter); several
		// matches CYCLE Minecraft-style on repeated Tab, completing against the
		// ORIGINALLY typed prefix (agentTabPrefix) so each press steps candidates
		// instead of locking onto the filled word. Outside a completable slash word
		// Tab stays the no-op it always was (nothing else in AGENT binds it).
		src := m.agentIn.Value()
		if m.agentTabPrefix != "" {
			src = m.agentTabPrefix
		}
		cands := agentSlashCandidates(src)
		if len(cands) == 0 {
			return m, nil
		}
		if len(cands) == 1 {
			m.agentIn.SetValue(cands[0] + " ") // complete + space: the strip hides itself
			m.agentIn.CursorEnd()
			return m, nil
		}
		if m.agentTabPrefix == "" {
			m.agentTabPrefix = src // start the cycle on the first match (idx already 0)
		} else {
			m.agentTabIdx = (m.agentTabIdx + 1) % len(cands) // step, wrapping around
		}
		m.agentIn.SetValue(cands[m.agentTabIdx])
		m.agentIn.CursorEnd()
		return m, nil
	case "enter":
		p := strings.TrimSpace(m.agentIn.Value())
		if p == "" {
			return m, nil
		}
		m.agentIn.SetValue("")
		// Record the sent prompt in the AGENT recall history (collapses a repeat of the
		// previous entry, resets the Up/Down cursor). Both chat turns and /commands count.
		m.agentHist.add(p)
		// QUEUE-WHILE-BUSY (founder: "queue like Claude"): a turn is already running, so this
		// prompt is parked and auto-sent (FIFO) when the current turn finishes. The input
		// stays typable throughout, so the next ask can be written without waiting.
		// BASE STATION: echo a LOCALLY-typed chat turn to any attached viewers (a slash
		// command is a local control action, not a chat turn; a remote turn is echoed by the
		// broker's /rc/send, so this fires ONLY for local typing).
		if !strings.HasPrefix(p, "/") {
			m.rcEmitLocalTurn(p)
		}
		if m.agentBusy {
			m.agentQueued = append(m.agentQueued, queuedPrompt{text: p})
			m.agentLines = append(m.agentLines, stDim.Render("⏳ queued · ")+stDim.Render(clipLine(p)))
			m.status = stDim.Render(plural(len(m.agentQueued), "queued msg") + " · sends when the turn finishes · esc cancels")
			return m, nil
		}
		if strings.HasPrefix(p, "/") {
			return m.runAgentCommand(p)
		}
		nm, cmd := m.submitAgentPrompt(queuedPrompt{text: p})
		return nm, cmd
	}
	// Input stays typable even while a turn runs, so the user can compose + queue the next
	// ask (the enter handler above parks it). Only the modal sub-states (picker / confirm,
	// handled earlier) own the keys.
	var c tea.Cmd
	m.agentIn, c = m.agentIn.Update(k)
	return m, c
}

// queuedPrompt is one parked prompt plus its ORIGIN. Origin matters at drain time: a
// LOCAL "/command" runs inline exactly as if typed when idle, but a REMOTE-queued "/..."
// is ALWAYS submitted as a chat turn - the same treatment the idle path gives a remote
// turn (rc.go injects via submitAgentPrompt, never runAgentCommand). Ruling 7: no v1
// remote handoff or host-side control, and the busy queue must not be a back door to it
// (iteration-1 finding #1: a remote-queued "/operator opencode" used to exec a guest on
// the HOST terminal at drain).
type queuedPrompt struct {
	text   string
	remote bool
	// echoed marks a prompt whose "▸ …" ask line is ALREADY in the transcript (a prompt
	// parked before the auto-tune landed, echoed at park time). drainPendingPrompts sets it
	// on the entries it requeues so submitAgentPrompt does not echo them a SECOND time at
	// drain (audit finding: the 2nd+ parked prompt was double-echoed).
	echoed bool
}

// submitAgentPrompt starts ONE agent turn for prompt q: it echoes the ask, re-resolves
// the model, flips the busy/streaming state, and launches the loop goroutine + the drain.
// It assumes q is a chat turn (not a slash command - those are handled by the caller / by
// startQueuedPrompt). If a previous (force-stopped) turn's goroutine is still unwinding it
// CANNOT start safely on the shared loop, so the prompt is re-queued to run when that
// goroutine finally exits (agentDoneMsg) - this is what makes force-stop race-free. The
// re-queue keeps q's origin, so a re-queued remote "/..." still never slash-dispatches.
func (m model) submitAgentPrompt(q queuedPrompt) (model, tea.Cmd) {
	p := q.text
	if m.agent != nil && m.agent.running.Load() {
		m.agentQueued = append([]queuedPrompt{q}, m.agentQueued...) // jump the queue: it was next
		m.agentLines = append(m.agentLines, stDim.Render("⏳ queued · ")+stDim.Render(clipLine(p))+stDim.Render(" (previous turn still wrapping up)"))
		return m, nil
	}
	// No model tuned in: don't fire a doomed turn (the "no station on air" spam). Echo the
	// ask, park it, and kick a SILENT auto-tune; runAutoTune sends it the moment a free
	// band lands, or flushes it with a single deduped failureHint if none is available. A
	// REMOTE-drained prompt is NEVER parked (it must resolve as a chat turn immediately -
	// the busy-queue remote-handoff guard); only locally-typed asks park.
	if m.agent != nil && m.agent.model == "" && !q.remote {
		m.agentLines = append(m.agentLines, stSelText.Render("▸ ")+p)
		m.agentPending = append(m.agentPending, q)
		if !m.autoTuning {
			m.autoTuning = true
			m.autoTuneBeatLen = len(m.agentLines)
			m.agentLines = append(m.agentLines, agentFindingBandBeat())
			return m, autoTuneCmd(m.broker, m.scanned)
		}
		return m, nil
	}
	if !q.echoed {
		// Skip the echo for a prompt already shown at park time (drainPendingPrompts requeued
		// it); otherwise echo the ask now.
		m.agentLines = append(m.agentLines, stSelText.Render("▸ ")+p)
	}
	// Re-resolve to the currently open channel so a model tuned in mid-session is used; if
	// still nothing is tuned in, the turn fails into the same actionable hint rather than
	// 504-ing on a phantom model.
	m.refreshAgentModel()
	m.agentBusy = true
	m.agentCanceling = false
	m.agentTurnState = poseThinking // turn sent, no tokens yet
	now := time.Now()
	m.agentStart = now
	m.agentLastEvent = now // reset the stall clock; the first event re-stamps it
	return m, tea.Batch(m.startAgentTurn(p), m.waitAgentEvent())
}

// startParkedTurn starts a turn for a prompt that was PARKED while no model was tuned
// (auto-tune has since bound a band). It is submitAgentPrompt without the echo (the ask
// was already echoed at park time) and without the model=="" park (a band is now bound).
func (m model) startParkedTurn(q queuedPrompt) (model, tea.Cmd) {
	p := q.text
	if m.agent != nil && m.agent.running.Load() {
		// A turn is still running: park this already-echoed prompt onto the busy queue. Mark it
		// echoed so the drain (submitAgentPrompt) does not re-echo the "▸ …" ask line - it was
		// echoed once at park time (audit finding: the same double-echo class fixed for rest[]).
		q.echoed = true
		m.agentQueued = append([]queuedPrompt{q}, m.agentQueued...)
		return m, nil
	}
	m.refreshAgentModel()
	m.agentBusy = true
	m.agentCanceling = false
	m.agentTurnState = poseThinking
	now := time.Now()
	m.agentStart = now
	m.agentLastEvent = now
	return m, tea.Batch(m.startAgentTurn(p), m.waitAgentEvent())
}

// startQueuedPrompt sends one dequeued item: a LOCALLY-typed slash-command runs inline
// (it starts no turn), anything else starts a turn - so a locally queued /clear or /model
// behaves the same as if typed when idle. A REMOTE-origin entry NEVER slash-dispatches:
// it is always submitted as a chat turn, matching the idle-path treatment of remote turns
// (iteration-1 finding #1 - the busy queue must not remote-exec host commands).
func (m model) startQueuedPrompt(q queuedPrompt) (model, tea.Cmd) {
	if !q.remote && strings.HasPrefix(q.text, "/") {
		nm, c := m.runAgentCommand(q.text)
		if mm, ok := nm.(model); ok { // runAgentCommand always returns a model value
			return mm, c
		}
		return m, c
	}
	return m.submitAgentPrompt(q)
}

// dequeueAgentPrompts drains queued prompts FIFO when a turn finishes: it runs leading
// slash-commands inline and starts the first chat turn it finds (the rest then wait for
// THAT turn's done). It stops early if a force-stopped turn's goroutine is still alive
// (rt.running) so it never races the shared loop - those items run when that goroutine
// exits (its agentDoneMsg re-enters here).
func (m model) dequeueAgentPrompts() (model, tea.Cmd) {
	var cmds []tea.Cmd
	for len(m.agentQueued) > 0 {
		if m.agent != nil && m.agent.running.Load() {
			break
		}
		next := m.agentQueued[0]
		m.agentQueued = m.agentQueued[1:]
		var c tea.Cmd
		m, c = m.startQueuedPrompt(next)
		if c != nil {
			cmds = append(cmds, c)
		}
		if m.agentBusy {
			break // a turn started; the remaining queue waits for its done
		}
	}
	return m, tea.Batch(cmds...)
}

// agentCommands is the ONE canonical registry of AGENT slash commands - the same set
// the switch in runAgentCommand (directly below) dispatches and the /help output
// describes. The `ask ›` Tab-autocomplete strip suggests from THIS list, so a new
// command is added HERE alongside its switch case (one place, kept in lock-step by
// TestAgentCommandRegistrySeam: every entry must dispatch, never "unknown:").
// Sorted; slash-prefixed canonical names only - short aliases (/dj /y /rc /h) stay
// typable but are not suggested.
var agentCommands = []string{"/clear", "/commands", "/copy", "/help", "/model", "/operator", "/persona", "/remote-control"}

// agentSlashCandidates returns the agentCommands entries the input's command word
// prefix-matches (case-insensitive, PREFIX-only), in registry (sorted) order - the
// suggestion strip + Tab completion source. It returns nil once the strip should
// hide: the input is not a slash command (any leading text means a chat turn), or
// the command word is already terminated by a space (args are being typed). Leading
// spaces are tolerated exactly like the enter handler's TrimSpace.
func agentSlashCandidates(input string) []string {
	s := strings.TrimLeft(input, " ")
	if !strings.HasPrefix(s, "/") || strings.Contains(s, " ") {
		return nil
	}
	want := strings.ToLower(s) // registry entries are lowercase (pinned by the seam test)
	var out []string
	for _, c := range agentCommands {
		if strings.HasPrefix(c, want) {
			out = append(out, c)
		}
	}
	return out
}

// agentSlashStrip renders the one-line autocomplete hint for the `ask ›` prompt, or
// "" when it must hide. House footer treatment: dim commands, " · " separators, the
// current Tab-cycle pick carated + red (stSelText) exactly like the picker cursor
// row (the carat carries the selection under NO_COLOR). While a cycle is live the
// strip keeps showing the ORIGINAL prefix's candidate set, so repeated Tab visibly
// steps the same choices instead of collapsing onto the filled word.
func (m model) agentSlashStrip() string {
	src, cycling := m.agentIn.Value(), false
	if m.agentTabPrefix != "" {
		src, cycling = m.agentTabPrefix, true
	}
	cands := agentSlashCandidates(src)
	if len(cands) == 0 {
		return ""
	}
	parts := make([]string, len(cands))
	for i, c := range cands {
		if cycling && i == m.agentTabIdx {
			parts[i] = stSelText.Render("▸ " + c)
		} else {
			parts[i] = stDim.Render(c)
		}
	}
	return strings.Join(parts, stDim.Render(" · "))
}

// runAgentCommand handles the small set of in-AGENT slash commands (no chat turn):
// /clear resets the session, /persona shows where dj.md lives + its first lines,
// /help lists them. Anything else is a hint (never sent as a turn).
func (m model) runAgentCommand(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	cmd := strings.TrimPrefix(fields[0], "/")
	note := func(s string) {
		m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render(s))
	}
	switch cmd {
	case "clear":
		if m.agent != nil {
			m.agent.loop.Reset()
		}
		m.agentLines = nil
		m.agentCost = 0
		m.agentTokensIn = 0 // a fresh session zeroes the running ↑↓ token totals too
		m.agentTokensOut = 0
		m.agentTPS = 0
		m.agentQueued = nil // drop any parked prompts too - a fresh start means fresh
		// Also disarm any in-flight auto-tune and drop the prompts parked while no band was
		// tuned. Without this a prompt parked before /clear fired as a phantom turn (its echo
		// already wiped by the clear) when the auto-tune landed (audit finding, MAJOR).
		m.agentPending = nil
		m.autoTuning = false
		m.autoTuneBeatLen = 0
		m.rcEmitCleared() // BASE STATION: tell viewers, so a dropped queued turn doesn't dangle
		note("session cleared - the agent starts fresh (still no long-term memory)")
		// A cleared session IS the landing again: its one note is the new entry chrome,
		// so THE DESK roster returns (desk_view: "/clear returns the landing").
		m.agentLandingLines = len(m.agentLines)
		return m, nil
	case "persona", "dj":
		note("persona: " + harness.PersonaPath() + " (editable - keeps getting updated)")
		head := strings.SplitN(harness.LoadPersona(harness.PersonaPath()), "\n", 2)
		note(strings.TrimSpace(head[0]))
		return m, nil
	case "model", "models":
		// `/model <name>` jumps straight to a candidate by (case-insensitive) name; bare
		// `/model` opens the picker: one candidate auto-selects (no needless prompt), many
		// show the arrow+enter list to re-point the agent.
		if len(fields) >= 2 {
			want := strings.ToLower(strings.Join(fields[1:], " "))
			for _, c := range m.agentModelCandidates() {
				if strings.ToLower(c) == want {
					m.pickAgentModel(c)
					return m, nil
				}
			}
			note("no candidate model matches " + strings.Join(fields[1:], " ") + " - /model lists what you can pick")
			return m, nil
		}
		return m.openAgentModelPicker()
	case "copy", "y":
		txt := m.agentTranscriptText()
		if strings.TrimSpace(txt) == "" {
			note("nothing to copy yet")
			return m, nil
		}
		note("✓ copied the agent transcript to the clipboard")
		m.status = copiedToast("the agent transcript")
		return m, clipboardWrite(txt)
	case "operator", "mic", "guest", "op":
		// Hand the mic to a guest operator (an installed agent CLI) on the open channel
		// (Guest Operators Phase 2). Aliases /mic /guest /op are typable, never suggested.
		return m.runOperatorCommand(fields[1:])
	case "remote-control", "remote", "rc":
		// Put THIS session on the air (BASE STATION) — continue it from another surface
		// logged into your account. `/remote-control off` takes it back off the air.
		off := len(fields) >= 2 && strings.EqualFold(fields[1], "off")
		return m.runRemoteCommand(off)
	case "help", "h", "commands":
		// "commands" matches the CHANNEL view's alias (tui.go) so the autocomplete
		// strip's /commands pick dispatches here instead of falling to unknown.
		note("/model switches model · /clear resets · /copy yanks the transcript (⌃y) · /persona shows dj.md · esc exits")
		note("the agent can read_file / list_dir / web_fetch on its own · write_file / run_shell ask first")
		note("/remote-control puts this session on your BASE STATION (continue it from any logged-in surface)")
		note("/operator hands the mic to a guest CLI at the desk (opencode · hermes · aider) on your open channel")
		return m, nil
	default:
		note("unknown: /" + cmd + " · /help for AGENT commands")
		return m, nil
	}
}

// openAgentModelPicker resolves the candidate models and either auto-selects (exactly
// one - the obvious choice, no needless prompt) or opens the modal picker (several -
// arrow + enter). With NO candidate at all it shows the actionable tune-in / share
// hint rather than an empty picker. The candidate set is the recent / last-tuned
// model(s) plus any band currently on air in the discover list (agentModelCandidates).
func (m model) openAgentModelPicker() (tea.Model, tea.Cmd) {
	cands := m.agentModelCandidates()
	switch len(cands) {
	case 0:
		m.agentLines = append(m.agentLines,
			stRed.Render("✕ ")+stEmber.Render("no model tuned in"),
			hintTuneOrShare(m.narrow()))
		return m, nil
	case 1:
		// Exactly one candidate: just use it (obvious - no prompt).
		m.pickAgentModel(cands[0])
		return m, nil
	default:
		m.agentPicker = true
		m.agentPickerRows = cands
		m.agentPickerCursor = 0
		// Start the cursor on the model we are already running on, if it is in the list,
		// so enter-without-moving is a no-op rather than a surprise switch.
		for i, c := range cands {
			if m.agent != nil && c == m.agent.model {
				m.agentPickerCursor = i
				break
			}
		}
		return m, nil
	}
}

// startAgentTurn runs one user turn through the harness loop in a background
// goroutine, streaming each step onto the runtime's events channel and closing it
// when the turn ends. The returned Cmd does not itself read the channel - the
// recurring waitAgentEvent drain does (keeping a single reader).
func (m model) startAgentTurn(prompt string) tea.Cmd {
	rt := m.agent
	// A cancellable context per turn: esc (while busy) calls rt.cancel to abort the
	// in-flight model call and stop any further steps. Stored on the runtime so the key
	// handler can reach it.
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	// Mark the goroutine alive synchronously (on the UI goroutine, before the Cmd runs) so a
	// next prompt processed before the goroutine even starts still sees running==true and
	// queues rather than racing the shared loop.
	rt.running.Store(true)
	return func() tea.Msg {
		go func() {
			_, _ = rt.loop.Send(ctx, prompt, func(e harness.Event) {
				rt.events <- e
			})
			cancel() // release the context's resources on any exit path
			// Clear running BEFORE closing events so that by the time the drain observes the
			// close (agentDoneMsg) and tries to dequeue, the next turn can start cleanly.
			rt.running.Store(false)
			close(rt.events)
		}()
		return nil
	}
}

// waitAgentEvent is the single drain: it blocks on the runtime's events channel (and
// the confirm-request channel) and returns the next thing to render. A closed events
// channel yields agentDoneMsg (turn finished). It is re-issued from Update after each
// event so the stream keeps flowing without a busy poll.
func (m model) waitAgentEvent() tea.Cmd {
	rt := m.agent
	if rt == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case c := <-rt.confirmReq:
			return agentConfirmMsg(c)
		case e, ok := <-rt.events:
			if !ok {
				// Channel closed: the turn is done. Re-arm it for the next turn so the
				// runtime is reusable across turns within the session.
				rt.events = make(chan harness.Event, 32)
				return agentDoneMsg{}
			}
			if e.Kind == eventCost {
				var c, tps float64
				var in, out int
				fmt.Sscanf(e.Text, "%g %d %d %g", &c, &in, &out, &tps)
				return agentCostMsg{cost: c, tokensIn: in, tokensOut: out, tps: tps}
			}
			return agentEventMsg(e)
		}
	}
}

// onAgentEvent renders one streamed loop step into the transcript and re-arms the
// drain so the next step flows. The tool-call / result lines use the shared
// iconography (◉ a tool firing, with a clear ok / error / denied outcome).
func (m model) onAgentEvent(e agentEventMsg) (tea.Model, tea.Cmd) {
	// Every streamed step is proof of life: stamp it so the working line can tell
	// STILL-RECEIVING from STALLED (agentWorkingLine) - the founder's "be smarter about
	// detecting working vs hung".
	m.agentLastEvent = time.Now()
	// Drive the reactive corner Ping off the same event stream: interim/final prose is
	// the answer coming over the wire (transmitting); a tool call is "working the dial";
	// a tool result hands back to the model to reason on (thinking again).
	switch e.Kind {
	case harness.EventAssistant:
		m.agentTurnState = poseStreaming
		if t := strings.TrimSpace(e.Text); t != "" {
			m.agentLines = append(m.agentLines, stLive.Render("◂ ")+t)
		}
	case harness.EventToolCall:
		m.agentTurnState = poseTool
		m.agentLines = append(m.agentLines, "  "+stSelText.Render(glyphOnAir+" ")+stKey.Render(e.Tool)+stDim.Render(": ")+stDim.Render(toolArgSummary(e.Tool, e.Args)))
	case harness.EventToolResult:
		m.agentTurnState = poseThinking // result is back; the model reasons on it next
		var mark, tail string
		switch {
		case e.Denied:
			mark, tail = stRed.Render("  ✕ "), stEmber.Render("denied")
		case e.IsError:
			mark, tail = stRed.Render("  ✕ "), stEmber.Render(firstLine(e.Result))
		default:
			mark, tail = stLive.Render("  ✓ "), stDim.Render("ok"+resultHint(e.Result))
		}
		m.agentLines = append(m.agentLines, mark+stDim.Render(e.Tool+" · ")+tail)
		// Show the user the ACTUAL output, not just "ok · N bytes": a short preview of
		// the result is the real UX gap behind a truncated answer (the user could never
		// see the listing the model summarized). Read-only tools (the listing / file /
		// page the user asked to see) and run_shell get the preview; a denied or errored
		// result keeps just the line above (its error text already rode in the tail). In
		// compact mode the summary line is enough.
		if !m.compact && !e.Denied && !e.IsError && previewableTool(e.Tool) {
			m.agentLines = append(m.agentLines, resultPreview(e.Result)...)
		}
	case harness.EventFinal:
		m.agentTurnState = poseStreaming
		t := strings.TrimSpace(e.Text)
		if t == "" {
			t = stDim.Render("(the agent finished with no text)")
		} else {
			t = stLive.Render("◂ ") + t
		}
		m.agentLines = append(m.agentLines, t)
		// Per-turn session footer: the honest running ↑in ↓out (broker billed re-count) + cost,
		// via the SHARED sessionFooter so the AGENT + CHANNEL money surfaces never drift.
		if f := sessionFooter(m.agentTokensIn, m.agentTokensOut, m.agentCost); f != "" {
			m.agentLines = append(m.agentLines, "   "+f)
		}
	case harness.EventError:
		// A failed turn is a dead end unless we say what to do next. Replace the bare
		// "status NNN / no reply" with a tight two-liner: the short cause (naming the
		// model when no station is serving it) + the actionable [1] tune in / [2] share
		// hint. The model name turns "504" into "no station is serving <model> right now".
		mdl := ""
		if m.agent != nil {
			mdl = m.agent.model
		}
		m.agentTurnState = poseWaiting // the turn failed; the corner Ping stands back by
		m.agentLines = append(m.agentLines, failureHint(e.Text, mdl, m.narrow())...)
	}
	m.rcTeeEvent(harness.Event(e)) // BASE STATION: mirror this step to any attached viewers
	return m, m.waitAgentEvent()
}

// toolArgSummary renders a tool call's key argument inline (the cmd, the path, the
// url) so the transcript reads "◉ run_shell: ls -la" at a glance.
func toolArgSummary(tool string, args map[string]any) string {
	switch tool {
	case "run_shell":
		return clipLine(argStr(args["cmd"]))
	case "write_file":
		return argStr(args["path"])
	case "read_file":
		return argStr(args["path"])
	case "list_dir":
		p := argStr(args["path"])
		if p == "" {
			p = "."
		}
		return p
	case "web_fetch":
		return clipLine(argStr(args["url"]))
	}
	return ""
}

// resultHint adds a terse size hint after a successful tool ("ok · 412 bytes") so the
// outcome is legible without dumping the whole result into the transcript.
func resultHint(s string) string {
	n := len(strings.TrimSpace(s))
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" · %d bytes", n)
}

// firstLine returns the first line of s (for a one-line error in the transcript).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clipLine(s)
}

// wrapPlain soft-wraps a plain (no-ANSI) string to width n, returning the lines. It is
// used to show a FULL run_shell command in the confirm gate without truncation: a long
// command spills onto extra lines instead of being clipped, so it can never be approved
// blind. Newlines in the input are preserved as line breaks; over-long unbroken runs are
// hard-broken at n. n < 1 collapses to a single line (no width to wrap to).
func wrapPlain(s string, n int) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if n < 1 {
		return []string{strings.ReplaceAll(s, "\n", " ")}
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		r := []rune(line)
		if len(r) == 0 {
			out = append(out, "")
			continue
		}
		for len(r) > n {
			out = append(out, string(r[:n]))
			r = r[n:]
		}
		out = append(out, string(r))
	}
	return out
}

// clipLine trims a value to a single, bounded line for the transcript.
func clipLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 80
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// previewableTool reports whether a tool's output is worth previewing under its result
// line. The read-only tools (list_dir / read_file / web_fetch) show the user what they
// asked to see; run_shell previews its captured output too. The mutating write_file
// only returns a short "wrote N bytes" confirmation, so its existing summary line is
// enough (no preview).
func previewableTool(tool string) bool {
	switch tool {
	case "list_dir", "read_file", "web_fetch", "run_shell":
		return true
	}
	return false
}

// previewMaxLines / previewMaxChars bound the inlined preview of a tool result so even a
// 16 KiB file or a huge listing shows just the head, with a "... +N more lines" marker.
const (
	previewMaxLines = 8
	previewMaxChars = 600
	previewLineCols = 100 // per-line clamp before agentView's width clamp; keeps long lines tidy
)

// resultPreview renders a short, dim, indented preview of a tool's raw output as a
// SLICE of transcript lines (one entry per line so agentView's per-line truncVisible
// keeps every line width-safe). It shows the first previewMaxLines lines (and at most
// previewMaxChars), each clipped to a single bounded line, and appends a
// "... +N more lines" marker when the output is longer. An empty/whitespace result
// yields no preview (the summary line above already said "ok" with no bytes). It is
// NO_COLOR-safe (it leans on stDim, which strips color under NO_COLOR) and never emits
// a multi-line string in a single entry.
func resultPreview(result string) []string {
	// Normalize line endings and drop a trailing blank so the line count is honest.
	s := strings.ReplaceAll(result, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	// Cap the scanned text first so a giant blob doesn't get split into a giant slice.
	clipped := false
	if len(s) > previewMaxChars {
		s = s[:previewMaxChars]
		clipped = true
	}
	all := strings.Split(s, "\n")
	total := len(all)
	shown := all
	if len(shown) > previewMaxLines {
		shown = shown[:previewMaxLines]
	}
	out := make([]string, 0, len(shown)+1)
	for _, ln := range shown {
		out = append(out, "    "+stDim.Render(previewClip(ln)))
	}
	// A "... +N more lines" marker when we truncated by line count OR by char budget.
	more := total - len(shown)
	if more > 0 {
		out = append(out, "    "+stDim.Render("... +"+plural(more, "more line")))
	} else if clipped {
		out = append(out, "    "+stDim.Render("... (more)"))
	}
	return out
}

// previewClip turns one raw output line into a single, tab-expanded, bounded preview
// line. It strips control characters that would corrupt the transcript and clamps to
// previewLineCols (agentView then clamps again to the real terminal width).
func previewClip(s string) string {
	s = strings.ReplaceAll(s, "\t", "    ")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') {
			return -1
		}
		return r
	}, s)
	if len([]rune(s)) > previewLineCols {
		s = string([]rune(s)[:previewLineCols]) + "…"
	}
	if s == "" {
		// A now-empty (control-only) line still occupies a row; keep it visible.
		return " "
	}
	return s
}

// agentView renders the AGENT screen: a CHANNEL-style heading (the model + persona +
// session cost), the streamed transcript (you ▸ / tool ◉ / result ✓✕ / answer ◂), a
// pending-confirm prompt when a mutating tool waits, the working line while a turn
// runs, and the always-live `ask ›` prompt. Compact-mode aware; NO_COLOR / narrow
// safe (it leans on the shared styles, which strip color under NO_COLOR, and clips
// every line to width).
func (m model) agentView(w int) string {
	// A guest-operator handoff in staging owns the whole screen: the ONE staged
	// PATCHING YOU THROUGH paint before the exec (anti-blank, operator.go).
	if m.operatorHandoff != nil {
		return m.operatorPatchView(w)
	}
	var b strings.Builder
	mdl := ""
	root := "."
	if m.agent != nil {
		mdl = m.agent.model
		root = m.agent.loop.Root
	}
	// With a model resolved the heading reads "on <model> · /model to switch"; with
	// nothing tuned in it names the gap (not a stale default model) so the screen and
	// the up-front hint agree. The "/model to switch" affordance rides the full heading
	// only (dropped under narrow / compact so the heading never overflows).
	var mdlCell string
	if mdl == "" {
		mdlCell = stDim.Render(" ") + stEmber.Render("no model tuned in")
	} else {
		mdlCell = stDim.Render(" on ") + stKey.Render(mdl)
		// The open channel's agent-ready marker: "⌁" VERIFIED (probed tool-calls) or "⌁~"
		// INFERRED (window qualifies, tools unproven). Silent when too-small/unknown (the
		// refusal + window warn carry those). Reads m.connected, the station patched in.
		if tag := m.operatorChannelAgentTag(); tag != "" {
			mdlCell += stDim.Render(" ") + stKey.Render(tag)
		}
	}
	// MODE CLARITY: AGENT (tool-calling) keeps the RED accent bar + a "· tools" tag, so it
	// reads as visibly distinct from the mono-barred TUNE-IN (basic chat) view that shares
	// this shape - red bar + "tools" = "this mode can run tools (read/list auto, write/run
	// confirm)", at a glance.
	if m.compact {
		// The windowshade folds the desk strip to a bare count (§3f) - "" with zero
		// guests, so the zero-guest compact heading stays byte-identical.
		head := "  " + stSelBar.Render("▌") + " " + stBrand.Render("AGENT") + stDim.Render(" · tools") +
			stDim.Render(" ") + mdlCell + stDim.Render(" · ") + stEmber.Render(dollars(m.agentCost)) + m.deskCompactCount()
		b.WriteString(truncVisible(head, w) + "\n")
	} else {
		if mdl != "" && !m.narrow() {
			mdlCell += stDim.Render(" · ") + stKey.Render("/model") + stDim.Render(" to switch")
		}
		head := "  " + stSelBar.Render("▌") + " " + stBrand.Render("AGENT") + stDim.Render(" · tools") +
			stDim.Render("  ") + mdlCell + stDim.Render(" · files ") + stKey.Render(shortPath(root)) +
			stDim.Render("   cost ") + stEmber.Render(dollars(m.agentCost))
		b.WriteString(truncVisible(head, w) + "\n")
		// The desk strip (§3a line 2): who is at the desk + how to hand off. "" with zero
		// guests - the zero-guest screen is byte-identical (permanent regression).
		b.WriteString(m.deskStripLine(w))
	}
	// Reactive corner Ping: a small operator at the desk that reacts to the live turn
	// state (standing by / thinking / on air / working the dial). ONLY when a model is
	// active - hidden entirely otherwise so the no-model screen stays a clean hint. It
	// reserves a small top region (a 3-line head, or one status line under narrow /
	// compact) and never overlaps the transcript or the prompt. The frame counter drives
	// the animation; quiet (NO_COLOR / non-TTY / reduced-motion) freezes it to one pose.
	cornerRows := 0
	if mdl != "" {
		// live = the animation clock is advancing (a turn is in flight); when idle the frame
		// is frozen, so the corner shows the open-eye standing-by frame, never a stuck blink.
		corner := agentCornerPing(m.agentTurnState, anim(m.frame), m.narrow(), m.compact, m.agentBusy)
		for _, l := range corner {
			b.WriteString(truncVisible("  "+l, w) + "\n")
		}
		cornerRows = len(corner)
	}
	// Scrollable transcript: an independent viewport (minus the corner region) the user
	// can page through (PgUp/PgDn, Ctrl+U/D, mouse wheel, arrows) even while a turn
	// streams, so a long answer or tool dump can be read back. Sized to min(content,
	// budget); the persisted scroll position + auto-stick-to-bottom live in refreshScroll.
	content := transcriptContent(m.agentLines)
	m.agentVP.Width = w
	m.agentVP.Height = clampRows(lineRows(content), m.agentTranscriptRows(cornerRows))
	m.agentVP.SetContent(content)
	if m.agentVP.Height > 0 {
		b.WriteString(m.agentVP.View() + "\n")
	}
	// The pre-launch plate (Phase 3): the ONE confirm between picking a guest and
	// PATCHING YOU THROUGH - modal, so it renders instead of everything below.
	if m.operatorPlate != nil {
		b.WriteString(m.operatorPlateView(w))
		return b.String()
	}
	// THE DESK roster (Phase 3): the static landing preview of who can take the mic;
	// deskRosterBlock returns "" off the landing state (and always with zero guests).
	b.WriteString(m.deskRosterBlock(w))
	// The /operator hand-the-mic picker (Guest Operators Phase 2): same modal shape as
	// the /model picker directly below.
	if m.operatorPicker {
		b.WriteString(m.operatorPickerView(w))
		return b.String()
	}
	// The /model picker: a small modal list of selectable models (recent / last-tuned +
	// on-air bands). The cursor row is reverse-video with a carat, matching the band /
	// share tables. Only opens with 2+ candidates (one auto-selects), so it is always a
	// real choice. NO_COLOR / narrow safe (shared styles + per-line clip).
	if m.agentPicker {
		b.WriteString("\n" + truncVisible("  "+stSelText.Render("pick a model")+stDim.Render(" - the agent will run on it"), w) + "\n")
		for i, mdl := range m.agentPickerRows {
			row := pad(mdl, 28)
			tail := m.modelBadgeTail(mdl)
			if i == m.agentPickerCursor {
				line := " ▸ " + row
				if tail != "" {
					line += "  " + tail // plain: one accent bar governs the reverse-video row
				}
				b.WriteString(truncVisible("  "+stSelText.Render(line), w) + "\n")
			} else {
				line := stDim.Render("   " + row)
				if tail != "" {
					line += "  " + stKey.Render(tail)
				}
				b.WriteString(truncVisible("  "+line, w) + "\n")
			}
		}
		hint := "↑↓ pick · ⏎ select · esc keep current"
		if m.narrow() {
			hint = "↑↓ · ⏎ · esc"
		}
		b.WriteString(truncVisible("  "+stDim.Render(hint), w) + "\n")
		return b.String()
	}
	// A pending mutating-tool confirm: an obvious y/N gate (default DENY). The footer is
	// rendered by View(); agentView only draws the prompt body.
	if c := m.agentPendingConfirm; c != nil {
		prompt := "run this side-effecting tool? "
		if m.narrow() {
			prompt = "run it? "
		}
		b.WriteString("\n")
		if c.tool == "run_shell" {
			// Show the FULL command, soft-wrapped across lines, so a long/obfuscated command
			// is never approved blind on a single truncated line. The cmd is also NOT
			// sandboxed (only the cwd is set), so the approver must see exactly what runs.
			b.WriteString(truncVisible("  "+stEmber.Render("? ")+stKey.Render("run_shell")+stDim.Render(" (runs in cwd, NOT sandboxed):"), w) + "\n")
			for _, ln := range wrapPlain(argStr(c.args["cmd"]), w-4) {
				b.WriteString("    " + stKey.Render(ln) + "\n")
			}
		} else {
			b.WriteString(truncVisible("  "+stEmber.Render("? ")+stKey.Render(c.summary()), w) + "\n")
		}
		b.WriteString(truncVisible("  "+stDim.Render(prompt)+stEmber.Render("[y/N]")+stDim.Render("  deny=default"), w) + "\n")
		return b.String()
	}
	// While a turn runs, a one-line working readout (radio voice): elapsed secs + an honest
	// receiving-vs-stalled state and the per-call cap (see agentWorkingLine).
	if m.agentBusy {
		elapsed, sinceLast := 0, 0
		if !m.agentStart.IsZero() {
			elapsed = int(time.Since(m.agentStart).Seconds())
		}
		if !m.agentLastEvent.IsZero() {
			sinceLast = int(time.Since(m.agentLastEvent).Seconds())
		}
		b.WriteString("  " + m.agentWorkingLine(elapsed, sinceLast) + "\n")
	}
	// SLASH STRIP: the passive autocomplete hint for the command word being typed -
	// every prefix match (ALL commands on a bare "/"), the Tab-cycled pick carated.
	// One footer-styled line directly ABOVE the input; agentSlashStrip returns ""
	// outside a live command word (chat text / args typing), so nothing is drawn then.
	if strip := m.agentSlashStrip(); strip != "" {
		b.WriteString("\n" + truncVisible("  "+strip, w)) // the prompt's \n ends this line
	}
	// The always-live prompt: `ask ›` + the input view (cursor + echoed text). Clipped
	// to width so a long placeholder / echoed line never overflows.
	b.WriteString("\n" + truncVisible("  "+stPrompt.Render("ask › ")+m.agentIn.View(), w) + "\n")
	if !m.compact {
		// Busy-aware help: while a turn streams, the one thing the user needs is how to
		// STOP it (esc), so lead with that instead of the idle command list.
		help := "enter asks  ·  /model switches  ·  /operator hands the mic  ·  ⌃y copy  ·  esc exits AGENT  ·  /clear  ·  read/list auto · write/run confirm"
		switch {
		case m.agentBusy && m.narrow():
			help = "type queues · esc cancels (2× force)"
		case m.agentBusy:
			help = "type + enter queues the next ask  ·  esc cancels (esc again force-stops)  ·  ⌃c quits"
		case m.narrow():
			help = "enter ask · /model · ⌃y copy · esc exit"
		}
		b.WriteString(truncVisible("  "+stDim.Render(help), w) + "\n")
	}
	return b.String()
}

// agentStallSec is how long the turn may go with NO event from the STATION before the
// working line stops reassuring and flags that it may be stuck. It is deliberately HIGH:
// the relay is non-streaming, so within one model call there are no intermediate events,
// and a CPU-MoE reply legitimately "takes well over a minute" (see harness.brokerTimeout);
// a run_shell tool is bounded at 60s. So only a silence well past those - genuinely
// suspect, and still hard-capped at harness.PerCallCap - earns the warning + the esc out.
// (A tool actually running is exempted in agentWorkingLine: that silence is expected.)
const agentStallSec = 120

// agentWorkingLine is the AGENT in-turn readout, smarter than a bare spinner. It always
// surfaces the per-call cap so the wait reads as BOUNDED (not a bottomless hang), and:
//   - while a tool runs (poseTool) it says so and never cries "stuck" — the tool is local
//     and self-bounded (run_shell <=60s, web_fetch <=20s), so the silence is EXPECTED
//     (flagging it was a false-alarm source);
//   - while waiting on / receiving from the station it reads "working…"/"receiving…", and
//     only a long silence (>= agentStallSec) flips to an honest "may be stuck · esc".
//
// The spinner is compact/quiet-aware (a static glyph when motion is frozen).
//
// elapsedSec is seconds since the turn began; sinceLastSec is seconds since the last event.
// Both are passed in (not read off the clock) so the render is a pure function of state.
func (m model) agentWorkingLine(elapsedSec, sinceLastSec int) string {
	// The signal-sweep meter rides beneath the status line under full motion; narrow /
	// compact / quiet collapse to the single status line (the reduced-motion form).
	withBar := !m.compact && !quiet && !m.narrow()
	// Beacon-only spinner (the pulsing on-air dot, NO rotating phrase): the precise static
	// state label below is the SINGLE source of "what's happening" text, so the old
	// phrase+label stutter (e.g. "Receiving… receiving…") is gone. Reduced-motion freezes
	// the beacon to a static dot.
	spin := pulseWith(m.frame, stPingEye)
	if !withBar {
		spin = stPingEye.Render(beaconDot())
	}
	capSec := int(harness.PerCallCap / time.Second)
	// status line: spinner + state, then a dim meta tail — elapsed within the per-call
	// cap, and the honest running session telemetry once there is any: ↑in ↓out (the
	// broker's BILLED token re-count) + cost (dust-safe via dollars()). The token half is
	// part of the always-shown status line, so reduced-motion (quiet/compact/narrow) drops
	// only the animated sweep, never the readout.
	withMeta := func(s string) string {
		line := spin + stLive.Render("  "+s)
		meta := ""
		if elapsedSec >= 2 {
			meta += fmt.Sprintf("  %ds (cap %ds)", elapsedSec, capSec)
		}
		if tot := meterTotals(m.agentTokensIn, m.agentTokensOut, m.agentCost); tot != "" {
			meta += "  · " + tot
		}
		if m.agentTPS > 0 {
			meta += "  · " + fmt.Sprintf("%.0f t/s", m.agentTPS) // latest call's throughput
		}
		if meta != "" {
			line += stDim.Render(meta)
		}
		return line
	}
	// STALLED: no event from the station for a genuinely long time — flag it with the out
	// and DROP the sweep (a moving bar must never imply liveness that isn't there). A tool
	// running locally is exempt: its silence is expected + bounded, never a station stall.
	if sinceLastSec >= agentStallSec && m.agentTurnState != poseTool {
		return spin + stEmber.Render(fmt.Sprintf("  no response for %ds — may be stuck · esc to cancel (cap %ds)", sinceLastSec, capSec))
	}
	// RECEIVING vs WORKING vs TOOL: prose arriving = the answer; a tool = local work;
	// otherwise the model is thinking.
	var label string
	switch m.agentTurnState {
	case poseTool:
		label = "running the tool…"
	case poseStreaming:
		label = "receiving…"
	default:
		label = "working…"
	}
	line := withMeta(label)
	if withBar {
		line += "\n  " + tintBar(meterSweep(m.frame, meterWidth), stLive)
	}
	return line
}

// agentRoot is the cwd sandbox root the agent's filesystem tools are confined to. It
// is the process working directory (where the user launched rogerai), cleaned to an
// absolute path. A failure to resolve falls back to ".", which the tools' own
// in-root guard still treats as the sandbox.
func agentRoot() string {
	d, err := os.Getwd()
	if err != nil || d == "" {
		return "."
	}
	return filepath.Clean(d)
}

// shortPath shortens a sandbox path for the heading: it abbreviates the home prefix
// to ~ and, if still long, keeps the last two path segments behind an ellipsis so the
// heading stays width-friendly without losing the leaf the user cares about.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	const max = 32
	if len(p) <= max {
		return p
	}
	segs := strings.Split(p, string(filepath.Separator))
	if len(segs) >= 2 {
		return "..." + string(filepath.Separator) + strings.Join(segs[len(segs)-2:], string(filepath.Separator))
	}
	return p[len(p)-max:]
}

// hintTuneOrShare is the actionable next-step line shown under EVERY relay/turn
// failure (and the AGENT no-model ready-state): put a station on air, or tune in a live
// one. The founder's "status 504 with no reply" was a dead end - this turns it into the
// two moves the user can actually make (and with the market currently empty, [2] put
// one on air is the one that unblocks them). Width-aware: it shortens to a terse
// `[2] go on air · [1] tune in` when narrow so it never overflows. Rendered in the dim
// style (the error line above carries the red beacon).
func hintTuneOrShare(narrow bool) string {
	if narrow {
		return stDim.Render("    ") + stKey.Render("[2]") + stDim.Render(" go on air · ") + stKey.Render("[1]") + stDim.Render(" tune in")
	}
	return stDim.Render("    put one on air with ") + stKey.Render("[2]") + stDim.Render(", or tune in ") + stKey.Render("[1]")
}

// failureHint shortens a raw relay/loop error into a concise, human first clause and
// pairs it with the actionable [1]/[2] hint as a tight two-liner. It is the shared
// error surface for BOTH the AGENT turn and the CHANNEL chat: instead of a bare
// "the station returned status 504 with no reply", the user sees
//
//	✕ no station is serving gpt-oss-20b right now
//	  put one on air with [2], or tune in [1]
//
// raw is the underlying error text (it may already mention a status / timeout / no
// station). model is the bound model the turn ran on ("" when unknown); it lets the
// no-station shape name the model so a bare 504 becomes "no station is serving <model>
// right now". The first line uses the inline-error red style; the second is the dim
// actionable hint. narrow trims the hint to fit a small terminal.
func failureHint(raw, model string, narrow bool) []string {
	return []string{
		stRed.Render("✕ ") + stEmber.Render(shortFailure(raw, model)),
		hintTuneOrShare(narrow),
	}
}

// shortFailure maps a raw relay error to a tight, plain first clause. It recognises
// the common shapes the broker/completer return (a 5xx with no reply, a timeout, an
// unreachable broker, an empty response, "no station / no node") and collapses each to
// a short phrase; anything else is passed through (clipped) so we never hide the real
// cause. model (when known) names the band in the no-station / no-reply / empty-reply
// shapes so the user sees WHICH model has nobody on air, not a bare status code.
func shortFailure(raw, model string) string {
	s := strings.TrimSpace(raw)
	low := strings.ToLower(s)
	// A 504 / 503 / 502 with no usable body is, in practice, "no station is serving this
	// model right now" - the broker had nobody to relay to. Name the model so the bare
	// code becomes an actionable sentence.
	switch {
	case strings.Contains(low, "no station") || strings.Contains(low, "no node") || strings.Contains(low, "not on air") || strings.Contains(low, "no model is tuned in"):
		return noStationServing(model) + statusSuffix(s)
	case strings.Contains(low, "no reply") || strings.Contains(low, "within ") && strings.Contains(low, "slow or offline"):
		return noStationServing(model) + statusSuffix(s)
	case strings.Contains(low, "with no reply") || strings.Contains(low, "empty response") || strings.Contains(low, "no text"):
		return noStationServing(model) + statusSuffix(s)
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timed out"):
		return "the station timed out" + statusSuffix(s)
	case strings.Contains(low, "could not reach the broker") || strings.Contains(low, "broker unreachable") || strings.Contains(low, "connection refused") || strings.Contains(low, "connection reset"):
		return "could not reach the broker"
	}
	return clipLine(s)
}

// noStationServing is the no-station phrase, naming the model when we know it: "no
// station is serving gpt-oss-20b right now" (vs the generic "no station is on air right
// now" when the model is unknown). It is the human face of a relay 504 with nobody on
// the other end - the founder's confusing bare-504 dead end.
func noStationServing(model string) string {
	if model == "" {
		return "no station is on air right now"
	}
	return "no station is serving " + model + " right now"
}

// statusSuffix pulls a trailing "(NNN)" out of a raw error that named an HTTP status
// (e.g. "... status 504 ...") so the short phrase can carry the code: "no station
// answered (504)". Empty when no 3-digit status is present.
func statusSuffix(s string) string {
	low := strings.ToLower(s)
	i := strings.Index(low, "status ")
	if i < 0 {
		return ""
	}
	rest := s[i+len("status "):]
	n := 0
	for n < len(rest) && n < 3 && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if n == 0 {
		return ""
	}
	return " (" + rest[:n] + ")"
}

// argStr coerces a JSON-decoded tool arg to a string for display (mirrors the
// harness package's own coercion; nil -> "").
func argStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
