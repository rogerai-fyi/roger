package webui

// THE CONSOLE'S JOIN on the empty Edge (features/edge/empty_edge_simple.feature §6): the same
// one field as the TUI's j prompt, through the same host backend. POST {text} starts; an invite
// answers at once, an address answers "waiting" with the phrase and the wait runs here; GET polls
// the wait; POST {cancel:true} gives up. What was typed is handed to the backend and kept nowhere.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
)

type edgeJoinPromptRun struct {
	mu     sync.Mutex
	gen    int
	ask    *edge.JoinAsk
	state  string // "", waiting, joined, failed
	says   string
	phrase string
	until  time.Time
}

type edgeJoinPromptJSON struct {
	State  string `json:"state"`
	Says   string `json:"says,omitempty"`
	Phrase string `json:"phrase,omitempty"`
	Until  int64  `json:"until,omitempty"`
}

func (r *edgeJoinPromptRun) view() edgeJoinPromptJSON {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := edgeJoinPromptJSON{State: r.state, Says: r.says, Phrase: r.phrase}
	if !r.until.IsZero() {
		out.Until = r.until.Unix()
	}
	return out
}

func (s *Server) handleEdgeJoinPrompt(w http.ResponseWriter, r *http.Request) {
	run := &s.joinPrompt
	if r.Method == http.MethodGet {
		writeJSON(w, run.view())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jp := s.opts.Edge.JoinPrompt
	if jp == nil || jp.Start == nil {
		http.Error(w, "this build cannot join an Edge from the console: run roger join <invite or address> in a terminal", http.StatusNotImplemented)
		return
	}
	var req struct {
		Text   string `json:"text"`
		Cancel bool   `json:"cancel"`
	}
	if !decode(r, &req) {
		http.Error(w, "a text is needed", http.StatusBadRequest)
		return
	}
	run.mu.Lock()
	if run.ask != nil && run.ask.Cancel != nil {
		run.ask.Cancel() // one ask at a time: a new line or a cancel ends the old one
	}
	run.gen++
	gen := run.gen
	run.ask, run.state, run.says, run.phrase, run.until = nil, "", "", "", time.Time{}
	run.mu.Unlock()
	if req.Cancel || strings.TrimSpace(req.Text) == "" {
		writeJSON(w, run.view())
		return
	}
	ask, err := jp.Start(req.Text)
	run.mu.Lock()
	switch {
	case err != nil:
		run.state, run.says = "failed", err.Error()
	case ask == nil:
		run.state = "joined"
	default:
		run.ask, run.state, run.phrase, run.until = ask, "waiting", ask.Phrase, ask.Until
		go func() {
			werr := ask.Wait()
			run.mu.Lock()
			defer run.mu.Unlock()
			if run.gen != gen {
				return // cancelled or replaced meanwhile
			}
			run.ask = nil
			if werr != nil {
				run.state, run.says = "failed", werr.Error()
				return
			}
			run.state = "joined"
		}()
	}
	run.mu.Unlock()
	writeJSON(w, run.view())
}
