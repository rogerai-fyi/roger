package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	messages   []Message // session-only context (system + the live conversation)
	// MaxSteps bounds the tool-call iterations per user turn so a misbehaving model
	// can't loop forever (and run up the bill). A turn that hits the cap returns the
	// last assistant text as the final answer.
	MaxSteps int

	// turnStart marks where the CURRENT turn begins in messages, so sources are derived
	// from this turn's retrievals only (a citation list must not accumulate across turns).
	turnStart int
	// searches / fetches are this turn's retrieval spend against the per-turn budget.
	// They reset on every Send: the budget bounds one answer, not a session.
	searches int
	fetches  int
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
		Persona:    persona,
		tools:      tools,
		toolByName: byName,
		complete:   complete,
		confirm:    confirm,
		MaxSteps:   8,
	}
	if persona != "" {
		l.messages = append(l.messages, Message{Role: "system", Content: persona})
	}
	return l
}

// Tools exposes the toolset (for the UI to describe the available capabilities).
func (l *Loop) Tools() []Tool { return l.tools }

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
	// A new turn: its own citation window and its own retrieval budget.
	l.turnStart = len(l.messages)
	l.searches, l.fetches = 0, 0
	l.messages = append(l.messages, Message{Role: "user", Content: userText})

	for step := 0; step < l.MaxSteps; step++ {
		// Stop promptly if the turn was cancelled between steps (e.g. after a tool round)
		// so an aborted turn never fires another billed model call.
		if ctx.Err() != nil {
			emit(Event{Kind: EventError, Text: "turn cancelled"})
			return "", ctx.Err()
		}
		msg, err := l.complete(ctx, l.messages, ToolSchemas(l.tools))
		if err != nil {
			// A cancelled context surfaces as a clean "cancelled", not a scary network error.
			if ctx.Err() != nil {
				emit(Event{Kind: EventError, Text: "turn cancelled"})
				return "", ctx.Err()
			}
			emit(Event{Kind: EventError, Text: err.Error()})
			return "", err
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
				emit(Event{Kind: EventFinal, Text: thought, Thought: true, Truncated: msg.Truncated})
				return thought, nil
			}
			final = l.withSources(final)
			emit(Event{Kind: EventFinal, Text: final, Truncated: msg.Truncated})
			return final, nil
		}

		// The model wants tools. Any interim prose rides along first.
		if t := strings.TrimSpace(msg.Content); t != "" {
			emit(Event{Kind: EventAssistant, Text: t})
		}
		for _, call := range msg.ToolCalls {
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
				l.cancelRemaining(call, emit)
				continue
			}
			l.runOne(ctx, call, emit)
		}
		// Loop: feed the tool results (appended below in runOne) back to the model.
	}

	// Hit the step cap: return the last assistant text we have as the final answer.
	last := l.withSources(l.lastAssistantText())
	emit(Event{Kind: EventFinal, Text: last})
	return last, nil
}

// runOne executes a single tool call: it parses the args, confirm-gates a mutating
// tool, runs it (or records a denial), emits the call + result events, and appends a
// tool-role result message so the next model turn sees the outcome.
func (l *Loop) runOne(ctx context.Context, call ToolCall, emit func(Event)) {
	name := call.Function.Name
	args := parseArgs(call.Function.Arguments)
	emit(Event{Kind: EventToolCall, Tool: name, Args: args})

	tool, ok := l.toolByName[name]
	if !ok {
		res := fmt.Sprintf("unknown tool %q", name)
		emit(Event{Kind: EventToolResult, Tool: name, Result: res, IsError: true})
		l.appendToolResult(call, res)
		return
	}

	// SAFETY MODEL: read-only tools auto-run; mutating tools (write_file, run_shell)
	// REQUIRE an explicit y/N confirm (default DENY). A denied confirm never runs the
	// tool - it feeds a clear "user denied" result back so the model can adapt.
	if tool.Mutating {
		approved := l.confirm != nil && l.confirm(name, args)
		if !approved {
			res := "user denied this " + name + " call - it was not run"
			emit(Event{Kind: EventToolResult, Tool: name, Result: res, IsError: true, Denied: true})
			l.appendToolResult(call, res)
			return
		}
	}

	// RETRIEVAL BUDGET: charged BEFORE the tool runs, so an exhausted budget costs no
	// network round trip - which is also what makes it useless as an injection lever.
	if over := l.chargeRetrieval(name); over != "" {
		// Not IsError: an exhausted budget is information the model acts on, not a failure
		// (features/answers/answers_mode.feature - "budget-exhausted is information").
		emit(Event{Kind: EventToolResult, Tool: name, Result: over})
		l.appendToolResult(call, over)
		return
	}

	out, err := tool.Run(ctx, l.Root, args)
	if err != nil {
		res := "error: " + err.Error()
		emit(Event{Kind: EventToolResult, Tool: name, Result: res, IsError: true})
		l.appendToolResult(call, res)
		return
	}
	emit(Event{Kind: EventToolResult, Tool: name, Result: out})
	l.appendToolResult(call, out)
}

// cancelRemaining records a queued call the turn was cancelled before reaching: nothing
// runs, nothing is confirmed, but the call gets its result so the transcript stays
// well-formed for the next turn.
func (l *Loop) cancelRemaining(call ToolCall, emit func(Event)) {
	res := "turn cancelled by the user - this " + call.Function.Name + " call was not run"
	emit(Event{Kind: EventToolResult, Tool: call.Function.Name, Result: res, IsError: true})
	l.appendToolResult(call, res)
}

// chargeRetrieval charges one retrieval against this turn's budget, returning "" when the
// call may proceed or the refusal to feed back when the budget is spent.
func (l *Loop) chargeRetrieval(name string) string {
	switch name {
	case "web_search":
		if l.searches >= maxSearchesPerTurn {
			return fmt.Sprintf("retrieval budget for this turn is used up (%d searches) - answer with what you already have", maxSearchesPerTurn)
		}
		l.searches++
	case "web_fetch":
		if l.fetches >= maxFetchesPerTurn {
			return fmt.Sprintf("retrieval budget for this turn is used up (%d fetches) - answer with what you already have", maxFetchesPerTurn)
		}
		l.fetches++
	}
	return ""
}

// appendToolResult records a tool-role message tying result back to the originating
// call id, the OpenAI contract for feeding a tool outcome to the next turn.
func (l *Loop) appendToolResult(call ToolCall, result string) {
	l.messages = append(l.messages, Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Name:       call.Function.Name,
		Content:    result,
	})
}

// lastAssistantText returns the most recent assistant message's text (used when the
// step cap is hit without a clean final answer).
func (l *Loop) lastAssistantText() string {
	for i := len(l.messages) - 1; i >= 0; i-- {
		if l.messages[i].Role == "assistant" {
			return strings.TrimSpace(l.messages[i].Content)
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
}

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
