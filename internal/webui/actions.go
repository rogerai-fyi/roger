package webui

import (
	"encoding/json"
	"net/http"

	"github.com/rogerai-fyi/roger/internal/node"
)

// actionResp is the uniform reply to every write action: the resulting node snapshot
// (so the browser re-renders immediately) plus a plain-text message and the same blocked
// flags the TUI surfaces. Code carries the one-time private-band frequency code, returned
// exactly once (on the private toggle that mints it) and never placed in a Snapshot.
type actionResp struct {
	OK          bool          `json:"ok"`
	Message     string        `json:"message,omitempty"`
	Code        string        `json:"code,omitempty"`
	BandDisplay string        `json:"band_display,omitempty"`
	LoginNeeded bool          `json:"login_needed,omitempty"`
	AtLimit     bool          `json:"at_limit,omitempty"`
	Snapshot    node.Snapshot `json:"snapshot"`
}

// action wraps a write handler with the token check AND a POST-only guard (a GET must
// never mutate state — it would be CSRF-reachable via an <img>/<script> tag).
func (s *Server) action(h http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	})
}

// decode reads a JSON body into v, treating an empty body as an empty object (so a
// no-arg re-scan POST is valid). Returns false only on a malformed non-empty body.
func decode(r *http.Request, v any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	return json.NewDecoder(r.Body).Decode(v) == nil
}

// handleOnAir toggles a model on/off air. Body: {"model":"..."}.
func (s *Server) handleOnAir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if !decode(r, &req) || req.Model == "" {
		http.Error(w, "model required", http.StatusBadRequest)
		return
	}
	res := s.ctrl.ToggleOnAir(req.Model)
	resp := actionResp{OK: res.Err == nil && !res.AtLimit && !res.LoginNeeded, Snapshot: s.ctrl.Snapshot(),
		LoginNeeded: res.LoginNeeded, AtLimit: res.AtLimit}
	switch {
	case res.WentOff:
		resp.Message = "off air — stopped sharing " + req.Model
	case res.AtLimit:
		resp.Message = "at the on-air limit — take one off air first, or raise share.max_on_air and restart"
	case res.LoginNeeded:
		resp.Message = "log in to earn (free sharing works without an account)"
	case res.Err != nil:
		resp.Message = "could not put " + req.Model + " on air: " + res.Err.Error()
	case res.Priced:
		resp.Message = "ON AIR — sharing " + req.Model + " priced"
	default:
		resp.Message = "ON AIR — sharing " + req.Model + " (FREE)"
	}
	writeJSON(w, resp)
}

// handlePrivate flips a model's private-band visibility. Body: {"model":"..."}.
func (s *Server) handlePrivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if !decode(r, &req) || req.Model == "" {
		http.Error(w, "model required", http.StatusBadRequest)
		return
	}
	res := s.ctrl.TogglePrivate(req.Model)
	resp := actionResp{OK: res.Err == nil && !res.AtLimit && !res.LoginNeeded, Snapshot: s.ctrl.Snapshot(),
		LoginNeeded: res.LoginNeeded, AtLimit: res.AtLimit, Code: res.Code, BandDisplay: res.Display}
	switch {
	case res.LoginNeeded:
		resp.Message = "log in to go private (a private band needs an account)"
	case res.AtLimit:
		resp.Message = "at the on-air limit — take one off air first"
	case res.Err != nil:
		resp.Message = "could not change " + req.Model + " visibility: " + res.Err.Error()
	case !res.NowPrivate:
		resp.Message = "back on the open market — " + req.Model + " is public again"
	default:
		resp.Message = "PRIVATE — " + req.Model + " is on a hidden band"
	}
	writeJSON(w, resp)
}

// handlePrice sets a model's price + schedule. Body:
// {"model":"...","in":0,"out":2,"windows":[{"start":"HH:MM","end":"HH:MM","in":0,"out":0,"free":true}]}.
func (s *Server) handlePrice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model   string             `json:"model"`
		In      float64            `json:"in"`
		Out     float64            `json:"out"`
		Windows []node.SchedWindow `json:"windows"`
	}
	if !decode(r, &req) || req.Model == "" {
		http.Error(w, "model required", http.StatusBadRequest)
		return
	}
	s.ctrl.SetPricing(req.Model, node.Pricing{In: req.In, Out: req.Out, Windows: req.Windows})
	writeJSON(w, actionResp{OK: true, Message: "price saved for " + req.Model, Snapshot: s.ctrl.Snapshot()})
}

// handleRename sets the station callsign. Body: {"station":"..."}.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Station string `json:"station"`
	}
	if !decode(r, &req) || req.Station == "" {
		http.Error(w, "station required", http.StatusBadRequest)
		return
	}
	s.ctrl.Rename(req.Station)
	writeJSON(w, actionResp{OK: true, Message: "station set to " + s.ctrl.Station(), Snapshot: s.ctrl.Snapshot()})
}

// handleDetect re-scans for local models (optionally verifying a pasted URL + key) and
// loads the result into the catalog. Body (all optional): {"url":"...","key":"..."}.
func (s *Server) handleDetect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
		Key string `json:"key"`
	}
	_ = decode(r, &req)
	found, needKey := s.ctrl.Detect(req.URL, req.Key)
	s.ctrl.LoadRows(found)
	resp := actionResp{OK: true, Snapshot: s.ctrl.Snapshot()}
	switch {
	case len(found) > 0:
		resp.Message = "detected local models"
	case len(needKey) > 0:
		resp.Message = needKey[0] + " needs an API key — paste it and re-scan"
	default:
		resp.Message = "nothing detected — start a local LLM or paste its URL"
	}
	writeJSON(w, resp)
}
