package webui

// towers.go is the owner's side of joining in the console (features/join/approve.feature,
// invite.feature, surfaces.feature, tower_identity.feature): the Towers this user serves and their
// fingerprints, waiting join requests answered with Approve or Refuse, a one-paste invite with its
// QR that says who joined, and the Towers this machine joined. The host's ownerjoin.Hooks are the
// same ones the TUI uses, so both screens say the same sentences. Every call is token-gated, and
// an action may only name a Tower this user is serving right now.

import (
	"net/http"
	"strconv"
	"time"

	"rogerai.fm/roger/v7/internal/ownerjoin"
	"rogerai.fm/roger/v7/internal/qr"
	"rogerai.fm/roger/v7/internal/tower"
)

// towersView is GET /api/towers. Every line is the sentence the TUI shows (internal/ownerjoin).
type towersView struct {
	Towers   []shownTower        `json:"towers"`
	Requests []ownerjoin.Request `json:"requests"`
	Pinned   []shownPinned       `json:"pinned"`
}

type shownTower struct {
	ownerjoin.Tower
	Line string `json:"line"`
}

type shownPinned struct {
	ownerjoin.Pinned
	Line string `json:"line"`
}

func (s *Server) ownerTowers() []ownerjoin.Tower {
	if s.opts.Owner == nil || s.opts.Owner.Towers == nil {
		return nil
	}
	return s.opts.Owner.Towers()
}

// servedTower is the served Tower named by dir; ok=false when this user serves no such Tower.
func (s *Server) servedTower(dir string) (ownerjoin.Tower, bool) {
	for _, t := range s.ownerTowers() {
		if t.Dir == dir {
			return t, true
		}
	}
	return ownerjoin.Tower{}, false
}

// answering reports whether dir is a place this user answers join requests: a served Tower, or this
// machine's Edge authority.
func (s *Server) answering(dir string) bool {
	if s.opts.Owner == nil {
		return false
	}
	for _, t := range s.opts.Owner.Answering(s.ownerTowers()) {
		if t.Dir == dir {
			return true
		}
	}
	return false
}

func (s *Server) handleTowers(w http.ResponseWriter, r *http.Request) {
	v := towersView{Towers: []shownTower{}, Requests: []ownerjoin.Request{}, Pinned: []shownPinned{}}
	h := s.opts.Owner
	if h != nil {
		towers := s.ownerTowers()
		for _, t := range towers {
			v.Towers = append(v.Towers, shownTower{Tower: t, Line: ownerjoin.TowerLine(t)})
		}
		if h.Requests != nil {
			for _, t := range h.Answering(towers) {
				reqs, err := h.Requests(t)
				if err != nil {
					continue
				}
				for _, q := range reqs {
					v.Requests = append(v.Requests, ownerjoin.RequestFor(t, q))
				}
			}
		}
		if h.Pinned != nil {
			for _, p := range h.Pinned() {
				v.Pinned = append(v.Pinned, shownPinned{Pinned: p, Line: ownerjoin.PinnedLine(p)})
			}
		}
	}
	writeJSON(w, v)
}

// shownInvite is an invite as the console shows it: the TUI's sentences, the minutes left and the
// QR as text.
type shownInvite struct {
	ownerjoin.Invite
	Left string `json:"left"`
	QR   string `json:"qr"`
}

// handleTowerInvite is POST /api/towers/invite {dir}: mint a one-paste invite on that Tower.
func (s *Server) handleTowerInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir string `json:"dir"`
	}
	if !decode(r, &req) {
		http.Error(w, "name the Tower to invite to", http.StatusBadRequest)
		return
	}
	t, ok := s.servedTower(req.Dir)
	if !ok || s.opts.Owner.Invite == nil {
		http.Error(w, "you are not serving that Tower", http.StatusNotFound)
		return
	}
	inv, err := s.opts.Owner.Invite(t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	shown := shownInvite{Invite: inv, Left: tower.MinutesLeft(inv.ExpiresAt, time.Now())}
	if code, err := qr.Encode([]byte(inv.Line), qr.L); err == nil {
		shown.QR = code.Blocks() // the console gives it its white ground
	}
	writeJSON(w, shown)
}

// handleTowerInviteOutcome is GET /api/towers/invite/outcome?dir=&id=: who joined with the invite,
// or that it ran out unused, once it is over.
func (s *Server) handleTowerInviteOutcome(w http.ResponseWriter, r *http.Request) {
	dir, id := r.URL.Query().Get("dir"), r.URL.Query().Get("id")
	if _, ok := s.servedTower(dir); !ok || s.opts.Owner.Outcome == nil || id == "" {
		http.Error(w, "you are not serving that Tower", http.StatusNotFound)
		return
	}
	line, over := s.opts.Owner.Outcome(dir, id)
	left := ""
	if exp, err := strconv.ParseInt(r.URL.Query().Get("expires_at"), 10, 64); err == nil {
		left = tower.MinutesLeft(exp, time.Now())
	}
	writeJSON(w, map[string]any{"line": line, "over": over, "left": left})
}

// handleTowerDecide is POST /api/towers/decide {dir, id, approve}: the owner's Approve or Refuse.
func (s *Server) handleTowerDecide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir     string `json:"dir"`
		ID      string `json:"id"`
		Approve bool   `json:"approve"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "name the request to answer", http.StatusBadRequest)
		return
	}
	if !s.answering(req.Dir) || s.opts.Owner.Decide == nil {
		http.Error(w, "you are not serving that Tower", http.StatusNotFound)
		return
	}
	if err := s.opts.Owner.Decide(req.Dir, req.ID, req.Approve); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.handleTowers(w, r)
}
