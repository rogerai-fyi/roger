package webui

import (
	"encoding/json"
	"net/http"
	"time"
)

// eventInterval is how often the SSE stream pushes a fresh snapshot. ~1 Hz is plenty for
// a human-watched dashboard (the live counters change at human speed) and keeps the
// stream cheap; it mirrors the cadence the TUI re-renders at.
var eventInterval = time.Second

// handleState returns a one-shot JSON snapshot of the node (the same Snapshot the SSE
// stream pushes). Never includes the upstream key — see node.Snapshot.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.ctrl.Snapshot())
}

// handleEvents streams the node snapshot as Server-Sent Events: one frame immediately,
// then one every eventInterval, until the client disconnects. The browser console renders
// each frame, so a change made in the terminal TUI shows up here within ~1s (and a change
// made here shows up in the TUI on its next tick).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")

	send := func() bool {
		blob, err := json.Marshal(s.ctrl.Snapshot())
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return false
		}
		if _, err := w.Write(blob); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	ticker := time.NewTicker(eventInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
