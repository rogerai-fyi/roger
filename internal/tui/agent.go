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
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// agentDoneMsg marks the turn finished (the events channel closed), re-enabling input.
	agentDoneMsg struct{}
	// agentCostMsg adds a per-turn relay cost to the running AGENT session total.
	agentCostMsg float64
)

// enterAgent opens the AGENT mode, building the runtime lazily on first entry. The
// agent runs on the model of the OPEN channel if one is tuned in; if NOTHING is tuned
// in it runs on no model and shows an up-front "tune in / share" hint (never a stale
// default that 504s). It loads the dj.md persona (writing the shipped default on first
// run if absent) and seeds a one-line welcome into the transcript. Re-entering keeps
// the existing session, but re-resolves the model so a channel tuned in AFTER first
// entry is picked up.
func (m model) enterAgent() (tea.Model, tea.Cmd) {
	m.mode = modeAgent
	if m.agent == nil {
		m.agent = m.newAgentRuntime()
		if m.agent.model != "" {
			m.agentLines = append(m.agentLines,
				stDim.Render("· ")+stDim.Render("AGENT on air - running on ")+stKey.Render(m.agent.model)+stDim.Render(" · dj.md persona · session-only (no memory)"),
				stDim.Render("· ")+stDim.Render("read/list/fetch run on their own · write/run ask first · sandboxed to "+m.agent.loop.Root),
			)
		} else {
			// Nothing tuned in: be honest up front and point at the two moves. The turn
			// itself is still allowed (it falls into the same actionable hint), but the
			// user should not have to send one to learn there is no model.
			m.agentLines = append(m.agentLines,
				stDim.Render("· ")+stDim.Render("AGENT ready · dj.md persona · session-only (no memory)"),
				stRed.Render("✕ ")+stEmber.Render("no model tuned in"),
				hintTuneOrShare(m.narrow()),
			)
		}
	} else {
		// Re-entry: pick up a channel tuned in since we last built the runtime (or note
		// one was dropped) so the agent never runs on a model that no longer matches the
		// open channel.
		m.refreshAgentModel()
	}
	m.agentIn.Focus()
	m.status = stDim.Render("AGENT ready · esc exits")
	return m, textinput.Blink
}

// refreshAgentModel re-resolves the agent's model from the currently open channel. It
// is a no-op when the model already matches; on a change it updates the runtime and
// drops a one-line note into the transcript so the heading + the next turn run on the
// right model.
func (m *model) refreshAgentModel() {
	if m.agent == nil {
		return
	}
	want := ""
	if m.connected != nil {
		want = m.connected.Model
	}
	if want == m.agent.model {
		return
	}
	m.agent.model = want
	switch {
	case want != "":
		m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render("tuned in - the agent now runs on ")+stKey.Render(want))
	default:
		m.agentLines = append(m.agentLines,
			stRed.Render("✕ ")+stEmber.Render("no model tuned in"),
			hintTuneOrShare(m.narrow()))
	}
}

// newAgentRuntime builds the harness loop + bridge channels. The completer relays
// through the broker (so the agent dogfoods the marketplace); the confirmer sends a
// pending confirm to the UI and blocks for the answer. costFn feeds per-turn relay
// cost back to the model via the events drain (a side channel on agentCostMsg).
func (m model) newAgentRuntime() *agentRuntime {
	mdl := "" // the TUNED-IN model only; "" when nothing is tuned in
	if m.connected != nil && m.connected.Model != "" {
		mdl = m.connected.Model
	}
	rt := &agentRuntime{
		model:       mdl,
		events:      make(chan harness.Event, 32),
		confirmReq:  make(chan agentConfirm),
		confirmResp: make(chan bool),
	}
	// Cost is surfaced through the events channel as a sentinel so the single drain Cmd
	// stays the only reader (no second goroutine racing the model).
	costFn := func(credits float64) {
		rt.events <- harness.Event{Kind: eventCost, Text: fmt.Sprintf("%g", credits)}
	}
	// The completer reads rt.model LIVE (not a captured value) so re-tuning a channel
	// after the runtime is built takes effect on the next turn without a rebuild.
	completer := func(messages []harness.Message, tools []map[string]any) (harness.Message, error) {
		if rt.model == "" {
			return harness.Message{}, fmt.Errorf("no station on air - no model is tuned in")
		}
		return harness.BrokerCompleter(m.broker, m.user, rt.model, m.confidentialOnly, costFn)(messages, tools)
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

// onAgentKey handles keys while in AGENT mode. A pending mutating-tool confirm owns
// every key (y runs, n/esc denies - default DENY). Otherwise it is a text-entry mode:
// enter submits a turn (a leading / is a local command), esc exits to BROWSE, and all
// other keys feed the prompt input. Because this owns its keys (and never consults
// presetForKey), a typed `0` is a literal digit, NEVER a re-entry into AGENT.
func (m model) onAgentKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A pending confirm modal: answer the y/N gate for the side-effecting tool.
	if c := m.agentPendingConfirm; c != nil {
		switch k.String() {
		case "y", "Y":
			m.agentLines = append(m.agentLines, "  "+stLive.Render("✓ ")+stDim.Render("approved · running "+c.tool))
			m.agentPendingConfirm = nil
			c.resp <- true
			return m, m.waitAgentEvent()
		default: // n / N / esc / anything else - default DENY
			m.agentLines = append(m.agentLines, "  "+stRed.Render("✕ ")+stEmber.Render("denied · "+c.tool+" was not run"))
			m.agentPendingConfirm = nil
			c.resp <- false
			return m, m.waitAgentEvent()
		}
	}
	// A turn is running: ignore typed keys (the loop owns the conversation) except esc,
	// which leaves the AGENT view (the turn keeps running in the background and its
	// result still lands in the transcript when we return).
	switch k.String() {
	case "esc":
		m.agentIn.Blur()
		m.mode = modeBrowse
		m.status = stDim.Render("left AGENT - the session is kept · [0] returns")
		return m, nil
	case "enter":
		if m.agentBusy {
			return m, nil
		}
		p := strings.TrimSpace(m.agentIn.Value())
		if p == "" {
			return m, nil
		}
		m.agentIn.SetValue("")
		if strings.HasPrefix(p, "/") {
			return m.runAgentCommand(p)
		}
		m.agentLines = append(m.agentLines, stSelText.Render("▸ ")+p)
		// Re-resolve to the currently open channel so a model tuned in mid-session is
		// used; if still nothing is tuned in, the turn fails into the same actionable
		// hint rather than 504-ing on a phantom model.
		m.refreshAgentModel()
		m.agentBusy = true
		m.agentStart = time.Now()
		return m, tea.Batch(m.startAgentTurn(p), m.waitAgentEvent())
	}
	if m.agentBusy {
		return m, nil // don't edit the prompt while a turn streams
	}
	var c tea.Cmd
	m.agentIn, c = m.agentIn.Update(k)
	return m, c
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
		note("session cleared - the agent starts fresh (still no long-term memory)")
		return m, nil
	case "persona", "dj":
		note("persona: " + harness.PersonaPath() + " (editable - keeps getting updated)")
		head := strings.SplitN(harness.LoadPersona(harness.PersonaPath()), "\n", 2)
		note(strings.TrimSpace(head[0]))
		return m, nil
	case "help", "h":
		note("/clear resets the session · /persona shows dj.md · esc exits AGENT")
		note("the agent can read_file / list_dir / web_fetch on its own · write_file / run_shell ask first")
		return m, nil
	default:
		note("unknown: /" + cmd + " · /help for AGENT commands")
		return m, nil
	}
}

// startAgentTurn runs one user turn through the harness loop in a background
// goroutine, streaming each step onto the runtime's events channel and closing it
// when the turn ends. The returned Cmd does not itself read the channel - the
// recurring waitAgentEvent drain does (keeping a single reader).
func (m model) startAgentTurn(prompt string) tea.Cmd {
	rt := m.agent
	return func() tea.Msg {
		go func() {
			_, _ = rt.loop.Send(prompt, func(e harness.Event) {
				rt.events <- e
			})
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
				var c float64
				fmt.Sscanf(e.Text, "%g", &c)
				return agentCostMsg(c)
			}
			return agentEventMsg(e)
		}
	}
}

// onAgentEvent renders one streamed loop step into the transcript and re-arms the
// drain so the next step flows. The tool-call / result lines use the shared
// iconography (◉ a tool firing, with a clear ok / error / denied outcome).
func (m model) onAgentEvent(e agentEventMsg) (tea.Model, tea.Cmd) {
	switch e.Kind {
	case harness.EventAssistant:
		if t := strings.TrimSpace(e.Text); t != "" {
			m.agentLines = append(m.agentLines, stLive.Render("◂ ")+t)
		}
	case harness.EventToolCall:
		m.agentLines = append(m.agentLines, "  "+stSelText.Render(glyphOnAir+" ")+stKey.Render(e.Tool)+stDim.Render(": ")+stDim.Render(toolArgSummary(e.Tool, e.Args)))
	case harness.EventToolResult:
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
	case harness.EventFinal:
		t := strings.TrimSpace(e.Text)
		if t == "" {
			t = stDim.Render("(the agent finished with no text)")
		} else {
			t = stLive.Render("◂ ") + t
		}
		m.agentLines = append(m.agentLines, t)
		if m.agentCost > 0 {
			m.agentLines = append(m.agentLines, stDim.Render("   session "+dollars(m.agentCost)))
		}
	case harness.EventError:
		// A failed turn is a dead end unless we say what to do next. Replace the bare
		// "status NNN / no reply" with a tight two-liner: the short cause + the
		// actionable [1] tune in / [2] share hint.
		m.agentLines = append(m.agentLines, failureHint(e.Text, m.narrow())...)
	}
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

// clipLine trims a value to a single, bounded line for the transcript.
func clipLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 80
	if len(s) > max {
		return s[:max] + "…"
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
	var b strings.Builder
	mdl := ""
	root := "."
	if m.agent != nil {
		mdl = m.agent.model
		root = m.agent.loop.Root
	}
	// With nothing tuned in the heading names the gap (not a stale default model) so
	// the screen and the up-front hint agree. "on <model>" reads naturally when tuned
	// in; with no model it drops the "on" and just states the gap.
	mdlCell := stDim.Render(" on ") + stKey.Render(mdl)
	if mdl == "" {
		mdlCell = stDim.Render(" ") + stEmber.Render("no model tuned in")
	}
	if m.compact {
		head := "  " + stSelBar.Render("▌") + " " + stBrand.Render("AGENT") +
			stDim.Render(" ") + mdlCell + stDim.Render(" · ") + stEmber.Render(dollars(m.agentCost))
		b.WriteString(truncVisible(head, w) + "\n")
	} else {
		head := "  " + stSelBar.Render("▌") + " " + stBrand.Render("AGENT") +
			stDim.Render("  ") + mdlCell + stDim.Render(" · sandbox ") + stKey.Render(shortPath(root)) +
			stDim.Render("   cost ") + stEmber.Render(dollars(m.agentCost))
		b.WriteString(truncVisible(head, w) + "\n")
	}
	// Scrollable transcript: keep the tail that fits the pane.
	lines := m.agentLines
	max := m.height - 8
	if m.compact {
		max = m.height - 6
	}
	if max < 6 {
		max = 12
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	for _, l := range lines {
		b.WriteString(truncVisible("  "+l, w) + "\n")
	}
	// A pending mutating-tool confirm: an obvious y/N gate (default DENY). The footer is
	// rendered by View(); agentView only draws the prompt body.
	if c := m.agentPendingConfirm; c != nil {
		prompt := "run this side-effecting tool? "
		if m.narrow() {
			prompt = "run it? "
		}
		b.WriteString("\n" + truncVisible("  "+stEmber.Render("? ")+stKey.Render(c.summary()), w) + "\n")
		b.WriteString(truncVisible("  "+stDim.Render(prompt)+stEmber.Render("[y/N]")+stDim.Render("  deny=default"), w) + "\n")
		return b.String()
	}
	// While a turn runs, a one-line working readout (radio voice), with elapsed secs.
	if m.agentBusy {
		elapsed := 0
		if !m.agentStart.IsZero() {
			elapsed = int(time.Since(m.agentStart).Seconds())
		}
		b.WriteString("  " + m.transmitLineFor(elapsed) + "\n")
	}
	// The always-live prompt: `ask ›` + the input view (cursor + echoed text). Clipped
	// to width so a long placeholder / echoed line never overflows.
	b.WriteString("\n" + truncVisible("  "+stPrompt.Render("ask › ")+m.agentIn.View(), w) + "\n")
	if !m.compact {
		help := "enter asks  ·  esc exits AGENT  ·  /clear  ·  /persona  ·  read/list auto · write/run confirm"
		if m.narrow() {
			help = "enter ask · esc exit · /clear · /persona"
		}
		b.WriteString(truncVisible("  "+stDim.Render(help), w) + "\n")
	}
	return b.String()
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
		return ".../" + strings.Join(segs[len(segs)-2:], "/")
	}
	return p[len(p)-max:]
}

// hintTuneOrShare is the actionable next-step line shown under EVERY relay/turn
// failure (and the AGENT no-model ready-state): tune in a live model, or share your
// own and use it. The founder's "status 504 with no reply" was a dead end - this
// turns it into two moves the user can actually make. Width-aware: it shortens to a
// terse `[1] tune in · [2] share` when narrow so it never overflows. Rendered in the
// dim style (the error line above carries the red beacon).
func hintTuneOrShare(narrow bool) string {
	if narrow {
		return stDim.Render("    ") + stKey.Render("[1]") + stDim.Render(" tune in · ") + stKey.Render("[2]") + stDim.Render(" share")
	}
	return stDim.Render("    tune in a live model with ") + stKey.Render("[1]") + stDim.Render(", or ") + stKey.Render("[2]") + stDim.Render(" share yours and use it")
}

// failureHint shortens a raw relay/loop error into a concise, human first clause and
// pairs it with the actionable [1]/[2] hint as a tight two-liner. It is the shared
// error surface for BOTH the AGENT turn and the CHANNEL chat: instead of a bare
// "the station returned status 504 with no reply", the user sees
//
//	✕ no station answered (504)
//	  tune in a live model with [1], or [2] share yours and use it
//
// raw is the underlying error text (it may already mention a status / timeout / no
// station). The first line uses the inline-error red style; the second is the dim
// actionable hint. narrow trims the hint to fit a small terminal.
func failureHint(raw string, narrow bool) []string {
	return []string{
		stRed.Render("✕ ") + stEmber.Render(shortFailure(raw)),
		hintTuneOrShare(narrow),
	}
}

// shortFailure maps a raw relay error to a tight, plain first clause. It recognises
// the common shapes the broker/completer return (a 5xx with no reply, a timeout, an
// unreachable broker, an empty response, "no station / no node") and collapses each to
// a short phrase; anything else is passed through (clipped) so we never hide the real
// cause.
func shortFailure(raw string) string {
	s := strings.TrimSpace(raw)
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "no station") || strings.Contains(low, "no node") || strings.Contains(low, "not on air"):
		return "no station on air" + statusSuffix(s)
	case strings.Contains(low, "no reply") || strings.Contains(low, "within ") && strings.Contains(low, "slow or offline"):
		return "no station answered" + statusSuffix(s)
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timed out"):
		return "the station timed out" + statusSuffix(s)
	case strings.Contains(low, "could not reach the broker") || strings.Contains(low, "broker unreachable") || strings.Contains(low, "connection refused") || strings.Contains(low, "connection reset"):
		return "could not reach the broker"
	case strings.Contains(low, "empty response") || strings.Contains(low, "no text"):
		return "the station sent no reply" + statusSuffix(s)
	}
	return clipLine(s)
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
