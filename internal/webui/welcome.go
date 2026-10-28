package webui

import "net/http"

// handleWelcome is GET /api/welcome: whether the one-time welcome card was seen (in the TUI or
// here). With no flag wired the card counts as seen, so a console without a host never shows it.
func (s *Server) handleWelcome(w http.ResponseWriter, r *http.Request) {
	seen := s.opts.WelcomeSeen == nil || s.opts.WelcomeSeen()
	writeJSON(w, map[string]bool{"seen": seen})
}

// handleWelcomeSeen is POST /api/welcome/seen: the card was closed, so neither front-end shows
// it again. It writes welcome_seen and nothing else.
func (s *Server) handleWelcomeSeen(w http.ResponseWriter, r *http.Request) {
	if s.opts.SaveWelcomeSeen == nil {
		writeJSON(w, map[string]bool{"seen": true})
		return
	}
	if err := s.opts.SaveWelcomeSeen(); err != nil {
		http.Error(w, "the welcome choice could not be remembered: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"seen": true})
}
