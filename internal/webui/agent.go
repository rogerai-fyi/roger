package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/protocol"
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
	// local records HOW the cached loop reaches its model: straight at a server on this
	// machine, or relayed through the broker. It is part of the cache key, not decoration -
	// a model id can exist on both sides of that line (the founder's box serves grok-4.3
	// locally AND the market lists bands), and reusing a broker loop for a LOCAL pick would
	// send the turn to exactly the place the pick was made to avoid.
	local bool

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
//
// Local says the picker's LOCAL group supplied this model: route it straight at the server
// on this machine, never through the broker. The browser sends the flag rather than the
// endpoint - the endpoint (and, more to the point, the bearer key it may need) is resolved
// here from the node's own catalog, so no credential is ever handed to a page.
//
// The flag is needed because a model id alone is ambiguous: the same name can be a band on
// the market and a server on this box, and guessing which one the operator clicked is how a
// turn silently ends up somewhere they did not choose.
type agentReq struct {
	Model   string `json:"model"`
	Message string `json:"message"`
	Local   bool   `json:"local"`
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
	// Hint is the actionable second line of an error event: WHAT TO DO, in a surface where
	// the raw cause said nothing a user could act on. The TUI pairs every failed turn with
	// one ("put one on air with [2], or tune in [1]"); the console's moves are different, so
	// the phrasing is, but the shape is the same and the first line is literally shared code
	// (harness.ShortFailure).
	Hint string `json:"hint,omitempty"`

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
//
// ask_operator is dropped despite being non-mutating. It is not a read - it BLOCKS on a
// person answering, and this surface has no way to put a question on screen or send an
// answer back. Left in, every call failed with "nobody is watching this session" while the
// persona told the model to reach for it, which is worse than not having it: the model
// spends a step discovering the tool is a lie. It goes for the same reason a subagent does
// not get one.
func readOnlyTools(all []harness.Tool) []harness.Tool {
	out := make([]harness.Tool, 0, len(all))
	for _, t := range all {
		if !t.Mutating && t.Name != "ask_operator" {
			out = append(out, t)
		}
	}
	return out
}

// localRow resolves model to a row in THIS node's own catalog that a turn can be sent
// straight to. It is the console's twin of the TUI's rowForModel/bindAgentEndpoint pair.
//
// Two guards, and both are the difference between routing and 504-ing:
//
//	an EMPTY UPSTREAM has nothing to send to, so the row is not offerable - taking it
//	would trade a broker timeout for a local one;
//	a VOICE model (tts/stt) cannot run a tool-use loop at all, so it is not a chat
//	band no matter where it is served from.
//
// Same two rules the TUI's localAgentRows applies, for the same reason.
func (s *Server) localRow(model string) (node.ShareRow, bool) {
	for _, r := range s.ctrl.Rows() {
		if r.Model != model || r.Upstream == "" {
			continue
		}
		if r.Modality == protocol.ModalityTTS || r.Modality == protocol.ModalitySTT {
			continue
		}
		return r, true
	}
	return node.ShareRow{}, false
}

// agentLoop lazily builds the console's loop, bound to the model the browser named and to
// the ROUTE the picker chose.
//
// LOCAL runs direct (harness.LocalCompleter), exactly as the TUI's agent does for a model
// on this machine: nothing registers, nothing is metered, no wallet is touched, and the
// weights never leave the box. It also needs no broker - requiring one here would refuse a
// conversation with a model sitting on the same disk because a remote service was
// unreachable.
//
// OPEN MARKET relays through the broker, on the SAME completer the TUI uses, so failover,
// the price cap, billing and the receipts are shared rather than reimplemented.
func (s *Server) agentLoop(model string, local bool) (*harness.Loop, error) {
	s.agentSess.mu.Lock()
	defer s.agentSess.mu.Unlock()
	if s.agentSess.loop != nil && s.agentSess.model == model && s.agentSess.local == local {
		return s.agentSess.loop, nil
	}
	var complete harness.Completer
	if local {
		row, ok := s.localRow(model)
		if !ok {
			// Refuse rather than fall back to the broker. A LOCAL pick that quietly became a
			// relayed one would spend the operator's money on a route they did not choose,
			// and would then fail with a market error about a model that is not on the
			// market - which is the shape of the bug this whole change exists to remove.
			return nil, fmt.Errorf("%s is not served by any local server right now - re-detect on SHARE, or pick an open-market band", model)
		}
		complete = harness.LocalCompleter(row.Upstream, row.UpstreamKey, model)
	} else {
		if s.opts.Broker == "" {
			return nil, fmt.Errorf("no broker configured - the agent needs one to reach a band")
		}
		// The cost hook is what makes a turn receipt possible at all: the relay reports each
		// call's billed cost and token counts, and the turn's total is their sum. A LOCAL
		// turn has no such hook because it has no cost - and a receipt that printed $0.0000
		// would be a claim, not an absence.
		complete = harness.BrokerCompleter(s.opts.Broker, s.opts.User, model, false, 0, s.agentSess.spend.add)
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve a working directory: %w", err)
	}
	l := harness.NewLoop(root, harness.LoadPersona(harness.PersonaPath()), complete, nil)
	l.SetTools(readOnlyTools(l.Tools()))
	s.agentSess.loop, s.agentSess.model, s.agentSess.local = l, model, local
	return l, nil
}

// agentFailure turns a raw turn error into the two lines the console shows: the concise
// cause, and what to do about it.
//
// The first line is harness.ShortFailure - the SAME mapping the TUI uses, so "the station
// returned status 504 with no reply" reads as "no station is serving grok-4.3 right now
// (504)" in both places. The founder saw the raw string in the browser only because that
// mapping was terminal-only.
//
// The second line is the console's own, because the moves are: the TUI can say "[2] go on
// air", and a browser has tabs. A LOCAL turn gets a different remedy for the same reason
// the TUI's localFailureHint exists - sending someone to the marketplace to fix their own
// localhost is a dead end dressed as advice.
func agentFailure(raw, model string, local bool) (cause, hint string) {
	cause = harness.ShortFailure(raw, model)
	switch {
	case harness.IsContextOverflow(strings.ToLower(raw)):
		// The band is healthy and answering; the conversation simply outgrew it. Neither
		// picking another station nor putting one on air changes that.
		return cause, "the conversation outgrew the window - reload the tab to start a fresh one, or pick a roomier model"
	case local:
		return cause, "this ran DIRECT on your machine, not through the broker - check that the model server is still up, or re-detect on SHARE"
	}
	return cause, "pick another station in the picker, or put one of your own on air on SHARE - a band can go off air mid-conversation"
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
	loop, err := s.agentLoop(req.Model, req.Local)
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
	var lastText, lastErr string
	out, rerr := loop.Send(r.Context(), req.Message, func(e harness.Event) {
		ev := flattenEvent(e)
		// Both kinds carry model prose. A THOUGHT final carries reasoning rather than the
		// answer, and its text differs from the answer anyway, so comparing text alone is
		// enough - no need to reason about which kind a client is looking at.
		if (ev.Kind == "final" || ev.Kind == "assistant") && strings.TrimSpace(ev.Text) != "" {
			lastText = strings.TrimSpace(ev.Text)
		}
		// A mid-stream failure is the same failure, and gets the same two lines. Mapping
		// only the terminal error would leave the raw "status 504 with no reply" reachable
		// by whichever path happened to emit it - which is how it survived here in the
		// first place.
		if ev.Kind == "error" && strings.TrimSpace(ev.Text) != "" {
			ev.Text, ev.Hint = agentFailure(ev.Text, req.Model, req.Local)
			lastErr = ev.Text
		}
		send(ev)
	})
	if rerr != nil {
		// THE FAILURE MUST BE SHOWN ONCE. The harness emits the failure as an event AND
		// loop.Send returns it, so a dead band painted the same red line twice - which
		// reads as two separate failures. Same reasoning as the duplicate-answer fix
		// below it: the duplicate was in the transport, not the turn. The terminal send
		// still earns its place when it says something the stream never carried.
		cause, hint := agentFailure(rerr.Error(), req.Model, req.Local)
		if cause != lastErr {
			send(agentEvent{Kind: "error", Text: cause, Hint: hint})
		}
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
