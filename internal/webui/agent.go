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
	complete := harness.BrokerCompleter(s.opts.Broker, s.opts.User, model, false, 0, nil)
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

	out, rerr := loop.Send(r.Context(), req.Message, func(e harness.Event) {
		send(flattenEvent(e))
	})
	if rerr != nil {
		send(agentEvent{Kind: "error", Text: rerr.Error()})
		return
	}
	send(agentEvent{Kind: "final", Text: out})
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
