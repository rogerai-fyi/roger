package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Message is one entry in the OpenAI-style conversation the loop maintains. Role is
// one of system/user/assistant/tool. ToolCalls is set on an assistant turn that
// requests tools; ToolCallID + Name tie a tool-role result back to the call that
// produced it.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// Thought is the model's reasoning text when the visible content came back empty
	// (a thinking model that wrapped up inside its reasoning channel and never spoke).
	// Local-only: never serialized back to the API.
	Thought string `json:"-"`
	// Truncated marks a finish_reason=length reply: the completion budget ran out
	// (often mid-reasoning, which is one way content arrives empty). Local-only.
	Truncated bool `json:"-"`
}

// ToolCall is one OpenAI tool_call: an id, the function name, and the JSON-string
// arguments the model produced.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Completer turns the running conversation (+ the advertised tools) into the next
// assistant message. The default is BrokerCompleter (relays through the broker so
// the agent dogfoods the marketplace); tests inject a deterministic stub. tools is
// the OpenAI `tools` array (see ToolSchemas). ctx carries cancellation: when the user
// aborts an in-flight turn, ctx is cancelled and the completer must return promptly
// (BrokerCompleter passes it to the HTTP request so a hung station call is dropped).
type Completer func(ctx context.Context, messages []Message, tools []map[string]any) (Message, error)

// ConfirmPolicy decides whether a tool needs the operator's word before it runs. Nil means
// the default: exactly the mutating tools.
//
// It exists because "what a tool DOES" and "what an operator wants to be asked about" are
// different questions, and only the front-end can answer the second. The TUI widens this to
// include web_fetch: a fetch changes nothing on the machine, but it reaches OUT to an
// arbitrary host and pulls UNTRUSTED text back into the conversation, which is the
// prompt-injection path - and it sits beside write_file and run_shell in that toolset.
//
// Putting that in the tool's Mutating flag instead would have gated the fetch for every
// caller, headless ones included, which is a policy no terminal asked for. The flag stays a
// statement about the tool; this is a statement about the surface.
type ConfirmPolicy func(t Tool) bool

// Confirmer is asked to approve a side-effecting (mutating) tool call before it
// runs - the y/N gate. It returns true to run, false to deny (the loop then feeds a
// "user denied" result back to the model instead of running the tool). The TUI wires
// this to an on-screen confirm; a headless caller can auto-deny or auto-approve.
type Confirmer func(toolName string, args map[string]any) bool

// Event is a streamed step of one agent turn, surfaced to the UI as it happens so a
// long turn reads as a live broadcast (assistant text, a tool call, its result, the
// final answer) instead of a frozen wait.
type Event struct {
	Kind    EventKind
	Text    string         // assistant text / final answer / error text
	Tool    string         // tool name (ToolCall / ToolResult)
	Args    map[string]any // parsed tool args (ToolCall)
	Result  string         // tool result text (ToolResult)
	IsError bool           // the tool result is an error / a denied confirm
	Denied  bool           // a confirm was denied (ToolResult)
	// Thought marks an EventFinal whose Text is the model's REASONING, surfaced
	// because the spoken answer came back empty - the UI should render it as
	// thinking aloud, not as a normal answer.
	Thought bool
	// Truncated marks an EventFinal cut off by the completion budget
	// (finish_reason=length), so the UI can say WHY there is little or no text.
	Truncated bool
	// Step/MaxSteps identify the model iteration that produced this event. They are
	// presentation metadata for live progress surfaces; zero means unavailable.
	Step     int
	MaxSteps int
	// Agent attributes an event to a SUBAGENT ("" = the operator's own turn). A child
	// runs inside a tool body where nothing was previously visible, so the operator
	// watched a `delegate` card sit there with no sign of life. Forwarding the child's
	// events with its label is what lets a surface show what the delegation is doing -
	// and attribution is the same field a receipt is keyed on, so the live view and the
	// bill name the same agent.
	Agent string
	// AgentDone marks the child's last event, so a surface can retire its strip without
	// waiting for the tool result to land.
	AgentDone bool
}

// EventKind tags an Event.
type EventKind int

const (
	// EventAssistant is interim assistant prose emitted alongside tool calls.
	EventAssistant EventKind = iota
	// EventToolCall is a tool the model decided to call (before it runs).
	EventToolCall
	// EventToolResult is the outcome of running (or denying) a tool call.
	EventToolResult
	// EventFinal is the model's final answer (no further tool calls).
	EventFinal
	// EventError is an unrecoverable loop error (e.g. the model call failed).
	EventError
	// EventNotice is something the harness DID on the turn's behalf that the operator
	// should know about but does not have to act on - today, auto-compaction. Not an
	// error (the turn continues) and not an answer, so it renders as a quiet line
	// rather than a red one.
	EventNotice
)

// Loop is the embedded agent. It owns the session-only conversation (NO persistent
// memory), the bounded built-in toolset, the model completer, and the confirm gate.
type Loop struct {
	Root       string // the cwd sandbox root (cleaned, absolute)
	Persona    string // the dj.md system prompt
	tools      []Tool
	toolByName map[string]Tool
	complete   Completer
	confirm    Confirmer
	// NeedsConfirm widens (never narrows) what the loop asks about. Nil = the mutating
	// tools, which is what every headless caller wants; a front-end with a confirm UI can
	// add to it. It cannot make a mutating tool auto-run: needsConfirm ORs with Mutating,
	// so no policy can talk the loop out of gating a write or a shell.
	NeedsConfirm ConfirmPolicy
	messages     []Message // session-only context (system + the live conversation)
	// MaxSteps bounds the tool-call iterations per user turn so a misbehaving model
	// can't loop forever (and run up the bill). A turn that hits the cap returns the
	// last assistant text as the final answer.
	MaxSteps int

	// MaxToolOutput caps the bytes ONE tool result may add to the conversation, sized to
	// the model's context window (see toolOutputBudget). It exists because the tools' own
	// 16 KiB clip is a rounding error on a 128K band and HALF THE WINDOW on an 8K one: an
	// Apple `foundation` turn died with "Exceeded model context window size" after a single
	// ~10KB web_fetch. Enforcing it HERE, where every tool result funnels through, means a
	// tool that forgets to clip internally - or one added later - still cannot blow the
	// window. Zero means unbounded, so callers that never set it behave exactly as before.
	MaxToolOutput int

	// Guards run between the confirm gate and the tool body. Each may only DENY (see
	// guards.go): a non-empty return refuses the call and becomes the result the model
	// reads. Nil means DefaultGuards(); an explicitly empty slice disables them, which
	// is what tests that exercise raw tool behaviour want.
	Guards []Guard

	// turnStart marks where the CURRENT turn begins in messages, so sources are derived
	// from this turn's retrievals only (a citation list must not accumulate across turns).
	turnStart int
	// turnCalls are this turn's earlier tool-call signatures, feeding the repeat guard.
	turnCalls []string
	// budget is THIS TURN's retrieval ceiling, SHARED with every subagent spawned
	// under it (budget.go). Attribution is per-agent; authority is per-turn.
	//
	// steps counts this agent's own model calls for its receipt; childReceipts holds
	// one per subagent it delegated to. Both reset with the turn - a receipt describes
	// one turn, and carrying them across would bill a question for the last one's work.
	budget        *turnBudget
	steps         int
	childReceipts []Receipt
	// emit is the CURRENT turn's event sink, held so a subagent running inside a tool
	// body can forward its own events up to the same surface (subagent.go). Guarded
	// because `delegate` is Concurrent: two children may emit at once, and the surface
	// was written for one sequential stream.
	emit      func(Event)
	emitMu    sync.Mutex
	receiptMu sync.Mutex
	// spill saves oversized tool results under the workspace root so the model can read
	// the part that did not fit, instead of being told it was truncated and left there
	// (spill.go). Lazily created; Reset cleans it up.
	spill *spillStore
	// observed is what this agent has actually looked at, so a write cannot destroy
	// content it never read or that changed underneath it (observe.go).
	observed observations
}

// The per-turn retrieval budget (founder-approved 2026-07-27). It bounds the tokens a
// single answer can pull in, the fan-out a hostile page can provoke, and the wall-clock a
// turn can spend on the network. Exceeding it is INFORMATION fed back to the model, not an
// error: the turn still answers, with whatever it gathered.
// NOTE on the interaction with MaxSteps: a model that calls tools one at a time is bounded
// by MaxSteps first. These budgets bind when a model BATCHES tool calls in one assistant
// message - which is exactly the shape a hostile page provokes - so they are the ceiling
// that survives the adversarial case.
const (
	maxSearchesPerTurn = 3
	maxFetchesPerTurn  = 8
)

// needsConfirm reports whether this tool must be approved before it runs. It ORs with
// Mutating rather than replacing it: a policy can add to the gate, never open it.
func (l *Loop) needsConfirm(t Tool) bool {
	if t.Mutating {
		return true
	}
	return l.NeedsConfirm != nil && l.NeedsConfirm(t)
}

// NewLoop builds an agent loop rooted at root, with the given persona, completer,
// and confirm gate. The persona seeds the system message; the conversation is
// otherwise empty (session-only - no history is loaded from disk).
func NewLoop(root, persona string, complete Completer, confirm Confirmer) *Loop {
	tools := BuiltinTools()
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	l := &Loop{
		Root:       root,
		spill:      newSpillStore(root),
		Persona:    persona,
		tools:      tools,
		toolByName: byName,
		complete:   complete,
		confirm:    confirm,
		MaxSteps:   8,
	}
	// DELEGATE is registered on the ROOT loop only. newSubagent builds its child's
	// toolset by filtering this one, and drops delegate along with the mutating tools -
	// so depth is capped at one by construction rather than by a counter someone has to
	// remember to check (subagent.go).
	l.tools = append(l.tools, l.delegateTool())
	l.toolByName["delegate"] = l.tools[len(l.tools)-1]
	if persona != "" {
		l.messages = append(l.messages, Message{Role: "system", Content: persona})
	}
	return l
}

// TurnReceipt is this turn's spend, rolled up over every subagent the turn delegated
// to. This - not the root's own numbers - is what a UI should show as the turn's total:
// the root's own spend excludes its children and would understate.
func (l *Loop) TurnReceipt() Rollup {
	searches, fetches := 0, 0
	if l.budget != nil {
		searches, fetches = l.budget.spent()
	}
	// The root's OWN retrieval spend is the turn's total minus what the children
	// charged - they share one budget, so the shared counter already includes them.
	for _, c := range l.childReceipts {
		searches -= c.Searches
		fetches -= c.Fetches
	}
	own := Receipt{Steps: l.steps, Searches: max0(searches), Fetches: max0(fetches), Complete: true}
	return NewRollup(own, l.childReceipts)
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// RestoreConversation seeds a newly constructed loop with completed semantic user/assistant
// turns. It deliberately refuses system prompts, tool messages/calls, and local runtime flags:
// resuming history must never replay a tool or replace the current dj.md persona.
func (l *Loop) RestoreConversation(history []Message) error {
	restored := make([]Message, 0, len(history))
	for i, msg := range history {
		if msg.Role != "user" && msg.Role != "assistant" {
			return fmt.Errorf("restore message %d has unsupported role %q", i, msg.Role)
		}
		if len(msg.ToolCalls) > 0 || msg.ToolCallID != "" || msg.Name != "" || msg.Thought != "" || msg.Truncated {
			return fmt.Errorf("restore message %d contains live tool or runtime state", i)
		}
		restored = append(restored, Message{Role: msg.Role, Content: msg.Content})
	}
	l.Reset()
	l.messages = append(l.messages, restored...)
	return nil
}

// Tools exposes the toolset (for the UI to describe the available capabilities).
func (l *Loop) Tools() []Tool { return l.tools }

// guards resolves the chain: nil means the defaults, an explicitly empty (non-nil)
// slice means none. Callers that want raw tool behaviour set Guards to []Guard{}.
func (l *Loop) guards() []Guard {
	// The write guard is ALWAYS on, even when a caller replaces or empties the chain.
	// The others shape behaviour; this one prevents losing someone's file, and a test
	// or a surface that wanted the raw tools was never asking to be allowed to clobber
	// an unread file. Deny-only like the rest, so adding it can only narrow.
	base := l.Guards
	if base == nil {
		base = DefaultGuards()
	}
	return append([]Guard{l.GuardWriteNeedsRead}, base...)
}

// conversationView assembles the narrow read-only slice guards may consult. Built per
// call rather than cached: a guard must see what is true NOW, including a URL that
// arrived from a search earlier in this same turn.
func (l *Loop) conversationView() ConversationView {
	var user strings.Builder
	for _, m := range l.messages {
		if m.Role == "user" {
			user.WriteString(m.Content)
			user.WriteByte('\n')
		}
	}
	from := l.turnStart
	if from < 0 || from > len(l.messages) {
		from = 0
	}
	// Grounded URLs are BOTH halves of a retrieval:
	//   - what a web_search returned, which is the whole point of searching first, and
	//   - what a fetch already followed, so a re-read of a page is not refused.
	// Search results were the half I nearly left out, and leaving them out would have
	// broken the one flow the fetch guard is meant to encourage: search, then read a
	// result. A guard that refuses the behaviour it is asking for is worse than none.
	var urls []string
	for _, m := range l.messages[from:] {
		if m.Role == "tool" && m.Name == "web_search" {
			for u := range titlesFromResults(m.Content) {
				urls = append(urls, u)
			}
		}
	}
	for _, s := range sourcesFrom(l.messages[from:]) {
		urls = append(urls, s.URL)
	}
	return ConversationView{
		UserText:   user.String(),
		Retrieved:  urls,
		PriorCalls: l.turnCalls,
	}
}

// sources returns the citations for the CURRENT (most recent) turn, derived from what was
// actually retrieved. See sources.go for why this is the only derivation.
func (l *Loop) sources() []source {
	if l.turnStart < 0 || l.turnStart > len(l.messages) {
		return nil
	}
	return sourcesFrom(l.messages[l.turnStart:])
}

// withSources appends the citation block to an answer. Presentation only - the block is
// never written back into the conversation.
func (l *Loop) withSources(answer string) string {
	block := sourcesBlock(l.sources())
	if block == "" {
		return answer
	}
	if strings.TrimSpace(answer) == "" {
		return block
	}
	return answer + "\n\n" + block
}

// Send runs one user turn through the agent loop and streams each step to emit. It
// appends the user message, then repeatedly: asks the model for the next assistant
// message, and if that message requests tool calls, executes them (confirm-gating
// mutating tools), feeds the results back, and loops - until the model returns an
// answer with no tool calls (the final answer) or MaxSteps is hit. emit may be nil.
//
// DEGRADE-TO-CHAT: if the model returns no tool_calls (e.g. the channel's model is
// not tool-capable, or the relay strips tools), this is exactly the terminal case -
// the assistant text is the final answer. So the loop is a strict superset of plain
// chat and works on any model.
func (l *Loop) Send(ctx context.Context, userText string, emit func(Event)) (string, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A new turn: its own citation window, retrieval budget, and call history. The
	// repeat guard is scoped to a TURN on purpose - asking the same question again
	// later is a legitimate thing for an operator to do, and re-running the call that
	// answers it is the right response.
	l.turnStart = len(l.messages)
	if l.budget == nil {
		l.budget = &turnBudget{}
	}
	l.budget.reset()
	l.steps = 0
	l.childReceipts = nil
	l.emit = emit
	l.turnCalls = l.turnCalls[:0]
	compacted := false // auto-compaction fires at most once per turn (see below)
	l.messages = append(l.messages, Message{Role: "user", Content: userText})

	for step := 0; step < l.MaxSteps; step++ {
		emitStep := func(e Event) {
			e.Step, e.MaxSteps = step+1, l.MaxSteps
			emit(e)
		}
		// Stop promptly if the turn was cancelled between steps (e.g. after a tool round)
		// so an aborted turn never fires another billed model call.
		if ctx.Err() != nil {
			emitStep(Event{Kind: EventError, Text: "turn cancelled"})
			return "", ctx.Err()
		}
		l.steps++
		msg, err := l.complete(ctx, l.messages, ToolSchemas(l.tools))
		if err != nil && IsContextOverflow(err.Error()) && !compacted {
			// AUTO-COMPACTION (founder 2026-08-20). The conversation outgrew the band's
			// window. Rather than ending the turn and telling the operator to run /clear
			// by hand, drop the oldest raw tool material - model-free, deterministic, and
			// never touching what anyone SAID - and try the call once more.
			//
			// ONCE per turn, and only when there is something to free. A second overflow
			// after a successful prune means the conversation is too big for this band
			// even without its raw material, and re-sending it would spend another billed
			// call to fail the same way; the operator's /clear or /model is the real fix
			// and the error now says so honestly.
			if have := l.compactableBytes(); have >= minCompactionGain {
				compacted = true
				freed, dropped := l.compactForWindow(have)
				emitStep(Event{Kind: EventNotice, Text: fmt.Sprintf(
					"compacted the session: dropped %s of tool output from %d earlier tool %s to fit the window",
					humanBytes(freed), dropped, map[bool]string{true: "call", false: "calls"}[dropped == 1])})
				continue
			} else {
				// SAY WHY IT DID NOT (founder: "why didn't it auto compact"). Compaction
				// declining silently looks identical to compaction being broken, and the
				// operator is left to guess which.
				//
				// It only drops EARLIER turns' tool output. It never touches what anyone
				// said - the questions and the answers are the session - and never this
				// turn's own material, because pruning what the model just fetched strands
				// the turn mid-thought. So when the window fills with conversation rather
				// than with old tool results, there is genuinely nothing it may drop, and
				// /clear or a roomier band is the real answer.
				compacted = true // do not re-check on a later step of the same turn
				emitStep(Event{Kind: EventNotice, Text: "nothing to compact - the window is full of " +
					"conversation, not old tool output, and compaction never drops what was said"})
			}
		}
		if err != nil {
			// A cancelled context surfaces as a clean "cancelled", not a scary network error.
			if ctx.Err() != nil {
				emitStep(Event{Kind: EventError, Text: "turn cancelled"})
				return "", ctx.Err()
			}
			emitStep(Event{Kind: EventError, Text: err.Error()})
			return "", err
		}
		// STRIP A RECITED PROMPT before the message enters history (echo.go). A model
		// that reads its own prompt back is not just ugly: the echo is appended, re-sent
		// next turn, and echoed again, so the conversation roughly doubles per turn and a
		// small band runs out of context in three. Cleaning it here fixes the display and
		// the compounding at once.
		if cleaned, stripped := stripPromptEcho(msg.Content, l.Persona); stripped {
			msg.Content = cleaned
			emitStep(Event{Kind: EventNotice, Text: "trimmed a recited prompt from the reply - " +
				"this band echoes its instructions back, which fills its own context window"})
		}
		l.messages = append(l.messages, msg)

		if len(msg.ToolCalls) == 0 {
			// Final answer (or a plain-chat model that ignored the tools).
			final := strings.TrimSpace(msg.Content)
			if strings.TrimSpace(msg.Content) == "" && msg.Thought != "" {
				// A thinking model that never spoke: surface the reasoning, marked as
				// thought so the UI renders it as thinking aloud (the founder's "the
				// agent finished with no text" dead end had the words sitting right
				// here in reasoning_content).
				thought := l.withSources(msg.Thought)
				emitStep(Event{Kind: EventFinal, Text: thought, Thought: true, Truncated: msg.Truncated})
				return thought, nil
			}
			final = l.withSources(final)
			emitStep(Event{Kind: EventFinal, Text: final, Truncated: msg.Truncated})
			return final, nil
		}

		// The model wants tools. Any interim prose rides along first.
		if t := strings.TrimSpace(msg.Content); t != "" {
			emitStep(Event{Kind: EventAssistant, Text: t})
		}
		for i := 0; i < len(msg.ToolCalls); {
			// Cancellation is checked per CALL, not just per step: one assistant message can
			// queue several tool calls, and a hostile page's whole play is to provoke exactly
			// that churn. Without this, esc still ran (and still confirm-prompted for) every
			// remaining call in the batch.
			//
			// The cancelled calls are RECORDED, not skipped: an assistant message carrying
			// tool_calls with no matching tool result is a shape strict OpenAI-compatible
			// stations reject, and the TUI keeps this session across turns - so simply
			// breaking out would poison every later turn until /clear.
			if ctx.Err() != nil {
				l.cancelRemaining(msg.ToolCalls[i], emitStep)
				i++
				continue
			}
			// A run of consecutive read-only calls overlaps its BODIES; anything else runs
			// alone. Either path decides and settles in the model's order, so the resulting
			// conversation is byte-identical to the serial one (parallel.go).
			if n := l.concurrentGroup(msg.ToolCalls, i); n > 1 {
				l.runGroup(ctx, msg.ToolCalls[i:i+n], emitStep)
				i += n
				continue
			}
			l.runOne(ctx, msg.ToolCalls[i], emitStep)
			i++
		}
		// Loop: feed the tool results (appended below in runOne) back to the model.
	}

	// Hit the step cap: return the last assistant text FROM THIS TURN as the answer. If
	// the turn produced none - every step spent on tools, nothing ever said - say that
	// plainly rather than reaching back to an older turn for something to show.
	last := l.lastAssistantText()
	if last == "" {
		last = "I used up this turn's steps without reaching an answer. Ask again, or ask for a narrower step."
	}
	last = l.withSources(last)
	emit(Event{Kind: EventFinal, Text: last, Step: l.MaxSteps, MaxSteps: l.MaxSteps})
	return last, nil
}

// plannedCall is one call after the DECIDE phase: either settled already (refused by a
// guard, denied at the confirm, out of budget, unknown tool) or cleared to run.
type plannedCall struct {
	call    ToolCall
	tool    Tool
	root    string
	args    map[string]any
	settled bool   // decided without running; result holds what to report
	result  string // the decided result text
	isError bool
	denied  bool
}

// decide runs everything that determines WHETHER a call happens, in the model's order:
// tool lookup, the confirm gate, the guard chain, the retrieval budget. It emits the
// EventToolCall so the operator sees the call appear in the order the model asked for
// it, and never runs the tool body.
//
// Order-dependent by nature: guards read the calls before them, the budget is a running
// counter, and a confirm is a question to a human. Racing any of it would make refusals
// depend on scheduling.
func (l *Loop) decide(call ToolCall, emit func(Event)) plannedCall {
	name := call.Function.Name
	args := parseArgs(call.Function.Arguments)
	emit(Event{Kind: EventToolCall, Tool: name, Args: args})

	tool, ok := l.toolByName[name]
	if !ok {
		return plannedCall{call: call, args: args, settled: true, isError: true,
			result: fmt.Sprintf("unknown tool %q", name)}
	}
	p := plannedCall{call: call, tool: tool, root: l.Root, args: args}

	// SAFETY MODEL: read-only tools auto-run; tools that need the operator's word
	// (write_file, run_shell, plus whatever the front-end adds) REQUIRE an explicit y/N
	// confirm (default DENY). A denied confirm never runs the tool - it feeds a clear
	// "user denied" result back so the model can adapt.
	if l.needsConfirm(tool) {
		if approved := l.confirm != nil && l.confirm(name, args); !approved {
			p.settled, p.isError, p.denied = true, true, true
			p.result = "user denied this " + name + " call - it was not run"
			return p
		}
	}

	// GUARDS: the last word before the tool body. Deny-only and monotonic, so no
	// ordering of them can widen what the agent may do (guards.go). A denial is fed
	// back as the tool result - the model reads WHY and can adapt.
	conv := l.conversationView()
	for _, g := range l.guards() {
		if reason := g(name, args, conv); reason != "" {
			// REFUSED BY A GUARD, not denied by the operator. Those are different things
			// and the card must not say the same word for both: the founder read a screen
			// of "denied" tool calls as a permissions problem and waited for a prompt that
			// was never coming, because nothing had asked them anything. A guard refusal
			// is an error WITH A REASON, and the reason is what the card should show.
			// RECORD IT ANYWAY. The signature was only appended after the guards passed,
			// so a refused call left no trace - and the repeat guard, which reads that
			// list, could never see the model re-issuing it. The founder screenshotted
			// the same refused web_fetch three times in one turn, burning steps on a call
			// that could not succeed. A refusal is still a call that happened.
			l.turnCalls = append(l.turnCalls, callSignature(name, args))
			p.settled, p.isError, p.result = true, true, reason
			return p
		}
	}
	l.turnCalls = append(l.turnCalls, callSignature(name, args))

	// RETRIEVAL BUDGET: charged BEFORE the tool runs, so an exhausted budget costs no
	// network round trip - which is also what makes it useless as an injection lever.
	if over := l.chargeRetrieval(name); over != "" {
		// Not IsError: an exhausted budget is information the model acts on, not a failure
		// (features/answers/answers_mode.feature - "budget-exhausted is information").
		p.settled, p.result = true, over
		return p
	}
	return p
}

// settle appends one call's result to the conversation and emits its EventToolResult.
// Called in the model's order regardless of what order the bodies finished in, so the
// transcript and the tool_call_id sequence read exactly as they would have serially.
func (l *Loop) settle(p plannedCall, out string, err error, emit func(Event)) {
	name := p.call.Function.Name
	switch {
	case p.settled:
		res := l.appendToolResult(p.call, p.result)
		emit(Event{Kind: EventToolResult, Tool: name, Result: res, IsError: p.isError, Denied: p.denied})
	case err != nil:
		res := l.appendToolResult(p.call, "error: "+err.Error())
		emit(Event{Kind: EventToolResult, Tool: name, Result: res, IsError: true})
	default:
		// A SUCCESSFUL read or write is what the agent has now observed (observe.go).
		// Recorded HERE, in the ordered settle phase, so nothing is ever recorded for a
		// call that failed, was denied, or was refused by a guard - an observation of a
		// read that did not happen would license a write that must not.
		l.noteObserved(name, p.args)
		l.noteWritten(name, p.args)
		// Clip to the model's budget BEFORE it enters the conversation. The UI still emits
		// the clipped text, so what the operator sees is what the model saw - a result that
		// silently differed between the two would make a truncation-caused answer
		// impossible to explain.
		res := l.appendToolResult(p.call, out)
		emit(Event{Kind: EventToolResult, Tool: name, Result: res})
	}
}

// runOne is the serial path: decide, run, settle, for exactly one call.
func (l *Loop) runOne(ctx context.Context, call ToolCall, emit func(Event)) {
	p := l.decide(call, emit)
	if p.settled {
		l.settle(p, "", nil, emit)
		return
	}
	out, err := runWithTimeout(ctx, p)
	l.settle(p, out, err, emit)
}

// runWithTimeout runs one tool body under its declared deadline, if it has one.
//
// The deadline is COOPERATIVE: the context is cancelled and the call is reported
// failed, which is what the model and the operator need. Go cannot preempt a goroutine
// that ignores its context, so a tool that never checks ctx keeps running in the
// background - reporting the timeout anyway is still right, because the alternative is
// a turn that hangs forever on a call nobody can see the end of.
//
// The error names the tool and the bound so the model can act on it rather than
// guessing what went wrong: a shell command that needs longer than its budget is a
// different problem from a command that failed.
func runWithTimeout(ctx context.Context, p plannedCall) (string, error) {
	if p.tool.Timeout <= 0 {
		return p.tool.Run(ctx, p.root, p.args)
	}
	tctx, cancel := context.WithTimeout(ctx, p.tool.Timeout)
	defer cancel()
	out, err := p.tool.Run(tctx, p.root, p.args)
	// Only OUR deadline turns into a timeout. A parent cancellation (esc) reaching the
	// same call is a cancellation and must keep saying so, or an interrupted turn would
	// report a timeout that never happened.
	if tctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return "", fmt.Errorf("%s took longer than %s and was stopped - try a smaller step, or a narrower command",
			p.tool.Name, p.tool.Timeout)
	}
	return out, err
}

// runGroup is the overlapped path: decide every call in order, run their bodies
// together, then settle in order. See parallel.go for why the phases are split this way.
func (l *Loop) runGroup(ctx context.Context, calls []ToolCall, emit func(Event)) {
	plans := make([]plannedCall, 0, len(calls))
	for _, c := range calls {
		plans = append(plans, l.decide(c, emit))
	}
	outs, errs := l.runBodies(ctx, plans)
	for i, p := range plans {
		l.settle(p, outs[i], errs[i], emit)
	}
}

// cancelRemaining records a queued call the turn was cancelled before reaching: nothing
// runs, nothing is confirmed, but the call gets its result so the transcript stays
// well-formed for the next turn.
func (l *Loop) cancelRemaining(call ToolCall, emit func(Event)) {
	res := l.appendToolResult(call, "turn cancelled by the user - this "+call.Function.Name+" call was not run")
	emit(Event{Kind: EventToolResult, Tool: call.Function.Name, Result: res, IsError: true})
}

// chargeRetrieval charges one retrieval against this turn's budget, returning "" when the
// call may proceed or the refusal to feed back when the budget is spent.
func (l *Loop) chargeRetrieval(name string) string {
	if l.budget == nil {
		l.budget = &turnBudget{}
	}
	if refusal := l.budget.charge(name); refusal != "" {
		return refusal
	}
	return ""
}

// appendToolResult records a tool-role message tying result back to the originating
// call id, the OpenAI contract for feeding a tool outcome to the next turn.
// It is also the ONE place a result is clipped to the model's budget. The cap used to live
// on the success path only, so the unknown-tool, denied, budget-exhausted, tool-error and
// cancelled paths each appended whatever they had built - and two of those interpolate
// attacker-influenced text (the tool NAME the model chose, and a tool's error, which can
// carry an upstream body). Clipping here means a future sixth path cannot forget it. The
// clipped text is RETURNED so the caller emits exactly what was recorded: a result that
// differed between the operator's screen and the model's context would make a
// truncation-caused answer impossible to explain.
func (l *Loop) appendToolResult(call ToolCall, result string) string {
	result = l.clipOrSpill(call.Function.Name, result)
	l.messages = append(l.messages, Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Name:       call.Function.Name,
		Content:    result,
	})
	return result
}

// lastAssistantText returns the most recent assistant text FROM THIS TURN, used when
// the step cap is hit without a clean final answer.
//
// FOUNDER SCREENSHOT 2026-08-21: two different questions came back with the same
// answer, and the second one did not even fit its question - it was the FIRST turn's
// reply, presented as the second's. This scanned the whole session backwards, so a turn
// that burned its steps on tool calls without ever producing prose walked back past its
// own beginning and returned a previous turn's answer as its own.
//
// That is the worst kind of wrong: not an error, not a blank, but a confident answer to
// a question nobody asked, indistinguishable from a real one. Bounded to this turn now,
// and a turn with genuinely nothing to say says so.
func (l *Loop) lastAssistantText() string {
	from := l.turnStart
	if from < 0 || from > len(l.messages) {
		from = 0
	}
	for i := len(l.messages) - 1; i >= from; i-- {
		if l.messages[i].Role == "assistant" {
			if t := strings.TrimSpace(l.messages[i].Content); t != "" {
				return t
			}
		}
	}
	return ""
}

// Reset clears the conversation back to just the persona (session-only - a fresh
// start, no disk history). Used when the user clears the agent transcript.
func (l *Loop) Reset() {
	l.messages = l.messages[:0]
	if l.Persona != "" {
		l.messages = append(l.messages, Message{Role: "system", Content: l.Persona})
	}
	// The spilled files belong to the conversation that produced them. /clear throws
	// that conversation away, so leaving a directory of its tool output behind in
	// someone's project would be both untidy and a small privacy leak.
	l.spill.cleanup()
}

// Close releases session-scoped resources - today the spill directory. Safe to call
// more than once, and safe to skip: a leftover directory is untidy, never harmful.
func (l *Loop) Close() { l.spill.cleanup() }

// parseArgs decodes a tool_call's JSON-string arguments into a map. A malformed or
// empty arguments string yields an empty map (the tool's own validation then reports
// the missing field back to the model) rather than crashing the loop.
func parseArgs(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// SetPersona swaps the system prompt for the CURRENT conversation, in place.
//
// It rewrites the leading system message rather than appending one: a conversation with
// two system messages sends both, which on the band this exists for (a tight window)
// would cost more than the swap saves. If the conversation has no system message yet -
// a loop built without a persona - one is inserted.
func (l *Loop) SetPersona(p string) {
	if p == "" || p == l.Persona {
		return
	}
	l.Persona = p
	for i := range l.messages {
		if l.messages[i].Role == "system" {
			l.messages[i].Content = p
			return
		}
	}
	l.messages = append([]Message{{Role: "system", Content: p}}, l.messages...)
}
