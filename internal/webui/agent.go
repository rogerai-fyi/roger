package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"rogerai.fm/roger/v5/internal/harness"
)

// agent.go - THE CONSOLE'S AGENT.
//
// The chat tab relayed ONE message and printed the reply: no tools, so nothing to read
// a file, list a directory or search. The founder asked for the harness-style chat, and
// that shape is agentic - the tool rows are most of what makes it useful.
//
// It runs its OWN loop, not the TUI's. Sharing one would mean a write approved in the
// browser waits for a y/N at a terminal nobody may be sitting at, and two transcripts
// disagreeing about whose turn is in flight. A separate session on the same working
// directory is the same situation as two terminals - understandable, and the operator's
// to manage.
//
// READ-ONLY, and that is a deliberate stopping point rather than an oversight. The
// console binds localhost behind a per-run token, which is the same trust boundary the
// TUI has, so a confirm gate here is entirely buildable. But `run_shell` reachable from
// a browser is a materially bigger blast radius than one reachable from the terminal
// you are already typing in, and turning that on is the founder's call to make
// knowingly - not something to inherit by default because the toolset happened to come
// along. Everything read-only auto-runs anyway and needs no gate, which is exactly the
// set that ships here.

// agentSession is the console's single agent conversation. Single because the console
// is single-operator by construction: localhost, one token, one browser.
type agentSession struct {
	mu    sync.Mutex
	loop  *harness.Loop
	model string

	// spend accumulates THIS TURN's relayed cost. A turn is many calls now - one per
	// model step, plus any subagent's - so the only honest turn total is their sum.
	// Reset when the turn starts; read when it ends.
	spend turnSpend
}

// turnSpend is one turn's billed total, summed over every relayed call it made.
type turnSpend struct {
	mu        sync.Mutex
	cost      float64
	tokensIn  int
	tokensOut int
	calls     int
}

func (s *turnSpend) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Zero the FIELDS, never the struct: `*s = turnSpend{}` overwrites the mutex with a
	// fresh one while it is held, so the deferred Unlock then releases a lock nobody
	// took - a panic, and one the race detector would not have caught either.
	s.cost, s.tokensIn, s.tokensOut, s.calls = 0, 0, 0, 0
}

// add is the CostFunc the relay calls per completed request. It runs from whichever
// goroutine made the call - subagents relay from inside overlapped tool bodies - so it
// is guarded.
func (s *turnSpend) add(credits float64, in, out int, _ float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cost += credits
	s.tokensIn += in
	s.tokensOut += out
	s.calls++
}

func (s *turnSpend) snapshot() (cost float64, in, out, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cost, s.tokensIn, s.tokensOut, s.calls
}

// agentReq is one turn from the browser.
type agentReq struct {
	Model   string `json:"model"`
	Message string `json:"message"`
}

// agentEvent is one streamed step, flattened for the browser. It mirrors the TUI's
// toolRun fields on purpose: the two surfaces render the same call from the same facts,
// so a tool card cannot come to mean different things in the terminal and the browser.
type agentEvent struct {
	Kind    string `json:"kind"` // assistant | tool_call | tool_result | final | error | notice
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Arg     string `json:"arg,omitempty"`
	Result  string `json:"result,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Denied  bool   `json:"denied,omitempty"`
	Agent   string `json:"agent,omitempty"` // a subagent's label, "" for the main turn
	Step    int    `json:"step,omitempty"`

	// Receipt fields, set only on the final "receipt" event: the whole turn's billed
	// spend, summed over every relayed call including any subagent's. Never one call's
	// numbers presented as the turn's - that understates, which is the one direction
	// this console must not round.
	Cost       float64 `json:"cost,omitempty"`
	TokensIn   int     `json:"tokens_in,omitempty"`
	TokensOut  int     `json:"tokens_out,omitempty"`
	Calls      int     `json:"calls,omitempty"`
	Steps      int     `json:"steps,omitempty"`
	Delegated  int     `json:"delegated,omitempty"`
	Incomplete bool    `json:"incomplete,omitempty"`
}

// readOnlyTools is the console's toolset: everything that runs without a confirm.
func readOnlyTools(all []harness.Tool) []harness.Tool {
	out := make([]harness.Tool, 0, len(all))
	for _, t := range all {
		if !t.Mutating {
			out = append(out, t)
		}
	}
	return out
}

// agentLoop lazily builds the console's loop, bound to the model the browser named.
func (s *Server) agentLoop(model string) (*harness.Loop, error) {
	s.agentSess.mu.Lock()
	defer s.agentSess.mu.Unlock()
	if s.opts.Broker == "" {
		return nil, fmt.Errorf("no broker configured - the agent needs one to reach a band")
	}
	if s.agentSess.loop != nil && s.agentSess.model == model {
		return s.agentSess.loop, nil
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve a working directory: %w", err)
	}
	// The SAME completer the TUI relays through, so failover, the price cap, billing and
	// the receipts are shared rather than reimplemented. A second relay path here would
	// be a second set of bugs and, worse, a second set of receipts.
	// The cost hook is what makes a turn receipt possible at all: the relay reports each
	// call's billed cost and token counts, and the turn's total is their sum.
	complete := harness.BrokerCompleter(s.opts.Broker, s.opts.User, model, false, 0, s.agentSess.spend.add)
	l := harness.NewLoop(root, harness.LoadPersona(harness.PersonaPath()), complete, nil)
	l.SetTools(readOnlyTools(l.Tools()))
	s.agentSess.loop, s.agentSess.model = l, model
	return l, nil
}

// handleAgent runs one agent turn and streams its steps back as newline-delimited JSON.
//
// NDJSON over the POST response rather than EventSource: EventSource is GET-only, and a
// turn that spends money on the operator's key must not be reachable by a GET - that is
// the same rule every other write on this console follows.
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	var req agentReq
	if !decode(r, &req) {
		writeChatErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeChatErr(w, http.StatusBadRequest, "pick a model first")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeChatErr(w, http.StatusBadRequest, "nothing to send")
		return
	}
	loop, err := s.agentLoop(req.Model)
	if err != nil {
		writeChatErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeChatErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	var sendMu sync.Mutex // the loop emits from tool goroutines (parallel + subagents)
	send := func(e agentEvent) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if enc.Encode(e) == nil {
			flusher.Flush()
		}
	}

	// One turn at a time. The loop holds a single conversation, and two turns
	// interleaving into it would corrupt the transcript for both.
	s.agentSess.mu.Lock()
	defer s.agentSess.mu.Unlock()

	s.agentSess.spend.reset()
	// THE ANSWER MUST BE SENT ONCE (founder: "i'm seeing two replies").
	//
	// The HARNESS already emits the model's answer as EventFinal - that is what
	// EventFinal is - and loop.Send then RETURNS the same text. This handler streamed the
	// event and then sent the return as a second `final`, so a plain question (one step,
	// no tools) rendered its reply twice in the browser while the receipt honestly said
	// "1 call · 1 step". The duplicate was in the transport, not the model.
	//
	// The trailing send still earns its place: loop.Send's return is the authoritative
	// answer and can legitimately differ from anything streamed - a step-capped or
	// recovered turn ends with text the stream never carried, and dropping it wholesale
	// would lose those answers. So it goes out only when it actually adds something.
	var lastText string
	out, rerr := loop.Send(r.Context(), req.Message, func(e harness.Event) {
		ev := flattenEvent(e)
		// Both kinds carry model prose. A THOUGHT final carries reasoning rather than the
		// answer, and its text differs from the answer anyway, so comparing text alone is
		// enough - no need to reason about which kind a client is looking at.
		if (ev.Kind == "final" || ev.Kind == "assistant") && strings.TrimSpace(ev.Text) != "" {
			lastText = strings.TrimSpace(ev.Text)
		}
		send(ev)
	})
	if rerr != nil {
		send(agentEvent{Kind: "error", Text: rerr.Error()})
		// The receipt still goes out: a turn that failed part-way still spent what it
		// spent, and dropping it would understate the bill.
		send(s.turnReceipt(loop))
		return
	}
	if trimmed := strings.TrimSpace(out); trimmed != "" && trimmed != lastText {
		send(agentEvent{Kind: "final", Text: out})
	}
	send(s.turnReceipt(loop))
}

// flattenEvent turns a harness event into the browser's shape.
func flattenEvent(e harness.Event) agentEvent {
	out := agentEvent{
		Text: e.Text, Tool: e.Tool, Result: e.Result,
		IsError: e.IsError, Denied: e.Denied, Agent: e.Agent, Step: e.Step,
	}
	switch e.Kind {
	case harness.EventAssistant:
		out.Kind = "assistant"
	case harness.EventToolCall:
		out.Kind = "tool_call"
		out.Arg = harness.ToolArgSummary(e.Tool, e.Args)
	case harness.EventToolResult:
		out.Kind = "tool_result"
	case harness.EventFinal:
		out.Kind = "final"
	case harness.EventNotice:
		out.Kind = "notice"
	default:
		out.Kind = "error"
	}
	return out
}

// turnReceipt is the whole turn's spend, for the browser. The rollup - not the root's
// own numbers - is the turn total: the root's spend excludes its subagents and would
// understate. Incomplete rides along so a partial tree reads as a lower bound rather
// than a final figure.
func (s *Server) turnReceipt(l *harness.Loop) agentEvent {
	cost, in, out, calls := s.agentSess.spend.snapshot()
	rc := l.TurnReceipt()
	return agentEvent{
		Kind: "receipt", Cost: cost, TokensIn: in, TokensOut: out, Calls: calls,
		Steps: rc.Steps, Delegated: len(rc.Children), Incomplete: !rc.Complete,
	}
}
