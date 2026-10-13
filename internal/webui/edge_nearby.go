package webui

// edge_nearby.go is the console's NEARBY list (features/edge/empty_edge_simple.feature §7, founder
// ruling 2026-10-12): the devices pinging that are not on the Edge, each with Invite and Approve, the
// same two actions as the TUI's i and a, through the same host hooks. Invite mints an invite bound to
// that device's node id and answers with the join line and its QR; Approve grants a phone's claim, or
// for a roger device waits for it to ask and then shows the request's phrase, which the owner answers
// with the Towers card's own decide (POST /api/towers/decide). GET /api/edge/nearby/asked reads where
// an Approve stands without acting.

import (
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/look"
	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/qr"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/tower"
)

// edgeNearbyJSON is one NEARBY row in the TUI's words: the browser paints it and composes nothing.
type edgeNearbyJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Seen     string `json:"seen"`
	Stale    bool   `json:"stale"`
	KeyLabel string `json:"key_label"`
	Key      string `json:"key"`
}

// edgeNearbyHead is the list's eyebrow and its actions, in the TUI's words.
type edgeNearbyHead struct {
	Eyebrow string `json:"eyebrow"`
	Actions string `json:"actions"`
	Invite  bool   `json:"invite"`  // the Invite button would work in this build
	Approve bool   `json:"approve"` // the Approve button would work in this build
}

func (s *Server) edgeNearbyOf(cands []store.EdgeNode, now time.Time) []edgeNearbyJSON {
	out := []edgeNearbyJSON{}
	for _, c := range cands {
		row := edgeNearbyJSON{ID: c.ID, Name: edge.UnitName(c, cands), Kind: c.Kind}
		if c.LastSeen > 0 {
			since := now.Sub(time.Unix(c.LastSeen, 0))
			row.Seen, row.Stale = look.NearbySeen(since), since >= look.NearbyStaleAfter
		}
		row.KeyLabel, row.Key = look.NearbyKey(c.Pin, c.ID)
		out = append(out, row)
	}
	return out
}

// edgeNearbyFind is the pinging device with this id, or false.
func (s *Server) edgeNearbyFind(id string) (store.EdgeNode, bool) {
	if s.opts.Edge.Candidates == nil || id == "" {
		return store.EdgeNode{}, false
	}
	for _, c := range s.opts.Edge.Candidates() {
		if c.ID == id {
			return c, true
		}
	}
	return store.EdgeNode{}, false
}

// edgeNearbyInviteJSON is a bound invite as the console shows it.
type edgeNearbyInviteJSON struct {
	Head string `json:"head"`
	Next string `json:"next"`
	Line string `json:"line"` // the `roger join '...'` line, quoted for a shell
	QR   string `json:"qr"`
	Left string `json:"left"`
}

// handleEdgeNearbyInvite is POST /api/edge/nearby/invite {id}: Invite on a NEARBY row.
func (s *Server) handleEdgeNearbyInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "name the device to invite", http.StatusBadRequest)
		return
	}
	if s.opts.Edge.InviteFor == nil {
		http.Error(w, "this build cannot mint an invite from the console - use roger edge invite --for", http.StatusNotImplemented)
		return
	}
	n, ok := s.edgeNearbyFind(req.ID)
	if !ok {
		http.Error(w, "that device is not pinging on this network", http.StatusNotFound)
		return
	}
	invite, until, err := s.opts.Edge.InviteFor(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := edgeNearbyInviteJSON{Head: look.NearbyInviteHead(edge.UnitName(n, nil)), Next: look.NearbyInviteNext,
		Line: joininvite.JoinLine(invite), Left: tower.MinutesLeft(until.UnixNano(), s.edgeNow())}
	if code, err := qr.Encode([]byte(invite), qr.L); err == nil {
		out.QR = code.Blocks()
	}
	writeJSON(w, out)
}

// edgeNearbyApproveJSON is where Approve stands for one device: granted (a phone, by claim),
// waiting (a roger device that has not asked yet, with what it runs), or asked (its request, the
// phrase, and the request's id and place for the decide).
type edgeNearbyApproveJSON struct {
	State   string `json:"state"`
	Say     string `json:"say"`
	Run     string `json:"run,omitempty"`
	Phrase  string `json:"phrase,omitempty"`
	Hint    string `json:"hint,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Request string `json:"request,omitempty"`
}

// handleEdgeNearbyApprove is POST /api/edge/nearby/approve {id}: Approve on a row.
func (s *Server) handleEdgeNearbyApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decode(r, &req) {
		http.Error(w, "name the device to approve", http.StatusBadRequest)
		return
	}
	s.edgeNearbyApprove(w, req.ID, true)
}

// handleEdgeNearbyAsked is GET /api/edge/nearby/asked?id=: where Approve stands, changing nothing.
func (s *Server) handleEdgeNearbyAsked(w http.ResponseWriter, r *http.Request) {
	s.edgeNearbyApprove(w, r.URL.Query().Get("id"), false)
}

func (s *Server) edgeNearbyApprove(w http.ResponseWriter, id string, act bool) {
	n, ok := s.edgeNearbyFind(id)
	if !ok {
		http.Error(w, "that device is not pinging on this network", http.StatusNotFound)
		return
	}
	name := edge.UnitName(n, nil)
	if n.Pin == "" {
		// A phone cannot ask; it joins by claim. Approve grants the claim, as the TUI's a does.
		if !act {
			writeJSON(w, edgeNearbyApproveJSON{State: "waiting", Say: look.NearbyNoCert})
			return
		}
		if s.opts.Edge.Adopt == nil {
			http.Error(w, "this build cannot approve from the console - use roger edge approve", http.StatusNotImplemented)
			return
		}
		if err := s.opts.Edge.Adopt(n.ID, n.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, edgeNearbyApproveJSON{State: "granted", Say: name + " may claim its certificate now; it joins when it checks in"})
		return
	}
	t, serving := s.edgeAuthorityAnswering()
	if !serving {
		http.Error(w, look.NearbyNotServing, http.StatusConflict)
		return
	}
	out := edgeNearbyApproveJSON{State: "waiting", Say: look.NearbyWaitAsk(name), Run: look.NearbyRunLine(t.Addr)}
	if q, phrase, ok := s.edgeNearbyAsked(t, n.Name); ok {
		out = edgeNearbyApproveJSON{State: "asked", Say: q.Ask, Phrase: phrase, Hint: q.Hint, Dir: q.Dir, Request: q.ID}
	}
	writeJSON(w, out)
}

// edgeAuthorityAnswering is this machine's Edge authority while it serves requests to join.
func (s *Server) edgeAuthorityAnswering() (ownerjoin.Tower, bool) {
	if s.opts.Owner == nil || s.opts.Owner.Edge == nil {
		return ownerjoin.Tower{}, false
	}
	return s.opts.Owner.Edge()
}

// edgeNearbyAsked is the one waiting request asked with this name. A request carries only the name
// the device asked with, so two of one name match neither (the TUI's rule): the Towers card answers
// each with its own phrase.
func (s *Server) edgeNearbyAsked(t ownerjoin.Tower, name string) (ownerjoin.Request, string, bool) {
	if s.opts.Owner.Requests == nil {
		return ownerjoin.Request{}, "", false
	}
	reqs, err := s.opts.Owner.Requests(t)
	if err != nil {
		return ownerjoin.Request{}, "", false
	}
	var hit tower.PairRequest
	n := 0
	for _, q := range reqs {
		if q.Name == name {
			hit, n = q, n+1
		}
	}
	return ownerjoin.RequestFor(t, hit), hit.Phrase, n == 1
}
