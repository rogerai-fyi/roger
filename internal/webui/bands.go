package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"rogerai.fm/roger/v5/internal/agent"
	"rogerai.fm/roger/v5/internal/client"
)

// PRIVATE BANDS IN THE CONSOLE.
//
// FOUNDER 2026-08-21: "lets make sure our webui has a way to configure/settings etc" ->
// the FULLER surface. The console could put a model on air, hide it onto a private band
// and price it - but from the moment a band existed it was unmanageable here. There was no
// list, no rename, no new code, no revoke. The one place with room for a table had none.
//
// Every handler is owner-scoped BY THE BROKER, not by this server: these proxy the same
// signed client calls the CLI and TUI make, so the console can never reach a band the
// operator's key cannot. That also means there is exactly one implementation of each rule
// (a revoked band cannot be rotated, a live one cannot be forgotten) and it lives at the
// broker, where it is enforceable.
//
// THE ONE-TIME CODE. A rotate returns a fresh secret. It is passed straight back to the
// browser and never stored, logged or re-fetchable - the same contract as a mint. The
// console shows it once and says so.

// bandView is one row of the console's band table. It carries NO secret: the display is
// the masked cosmetic dial the broker persists, which cannot resolve anything.
type bandView struct {
	ID      string `json:"id"`
	Display string `json:"display"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	NodeID  string `json:"node_id"`
	// Model is the model on THIS machine behind the band, or "" when the band lives
	// elsewhere. Resolved by comparing agent.ShareNodeID per row - never by splitting the
	// node id on "-", because a station callsign can itself contain hyphens and a wrong
	// guess would label a band with the wrong model.
	Model string `json:"model,omitempty"`
	// Here reports that the band belongs to THIS station even when no row matched it (its
	// server may simply be stopped). The remedy differs completely from a remote band's,
	// so the two must not look the same.
	Here bool `json:"here"`
}

// handleBands lists the operator's private bands, joined to this machine's models.
func (s *Server) handleBands(w http.ResponseWriter, r *http.Request) {
	if s.opts.Broker == "" {
		writeJSON(w, map[string]any{"bands": []bandView{}, "configured": false})
		return
	}
	rows, err := client.ListBands(s.opts.Broker)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	station := s.ctrl.Station()
	byNode := map[string]string{}
	for _, rv := range s.ctrl.Rows() {
		byNode[agent.ShareNodeID(station, rv.Model, 0)] = rv.Model
	}
	prefix := agent.ShareNodeID(station, "", 0) + "-"
	out := make([]bandView, 0, len(rows))
	for _, b := range rows {
		v := bandView{
			ID: b.ID, Display: b.Display, Label: b.Label, Status: b.Status, NodeID: b.NodeID,
			Here: strings.HasPrefix(b.NodeID, prefix),
		}
		if mdl, ok := byNode[b.NodeID]; ok {
			v.Model, v.Here = mdl, true
		}
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"bands": out, "configured": true})
}

// bandActionReq is the body every band mutation takes.
type bandActionReq struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Model string `json:"model"`
}

// handleBandAction routes the console's band mutations. The action rides in the URL so a
// rotate - which RETURNS A SECRET - has its own path and its own response shape, rather
// than a flag on a shared endpoint whose reply a caller might log wholesale.
func (s *Server) handleBandAction(w http.ResponseWriter, r *http.Request) {
	if s.opts.Broker == "" {
		http.Error(w, "the broker is not configured for this node", http.StatusServiceUnavailable)
		return
	}
	var req bandActionReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" {
		http.Error(w, "which band?", http.StatusBadRequest)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/bands/")
	switch action {
	case "label":
		if err := client.LabelBand(s.opts.Broker, req.ID, strings.TrimSpace(req.Label)); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case "rotate":
		code, display, err := client.RotateBand(s.opts.Broker, req.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// SHOWN ONCE. The broker keeps only the hash, so this is the only moment the code
		// exists anywhere - the console renders it and holds it in the DOM, nothing more.
		writeJSON(w, map[string]any{"ok": true, "code": code, "display": display})
	case "revoke":
		if err := client.RevokeBand(s.opts.Broker, req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// RECONCILE THE NODE, exactly as the TUI does. The band is gone broker-side, but
		// this machine may still be registered PRIVATE behind it - hidden from the market
		// and reachable by nobody - and the surviving private flag would make the next
		// toggle publish the model. Taking it off air is the honest resolution.
		if mdl := s.modelForNode(req.Model); mdl != "" {
			s.ctrl.BandRevoked(mdl)
		}
		writeJSON(w, map[string]any{"ok": true})
	case "forget":
		if err := client.ForgetBand(s.opts.Broker, req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "no such band action", http.StatusNotFound)
	}
}

// modelForNode maps a node id back to a model on THIS machine, or "" when the band points
// somewhere else. Compares against agent.ShareNodeID per row rather than splitting on "-",
// because a station callsign can contain hyphens and a wrong guess would take the WRONG
// model off air.
func (s *Server) modelForNode(node string) string {
	if node == "" {
		return ""
	}
	station := s.ctrl.Station()
	for _, rv := range s.ctrl.Rows() {
		if agent.ShareNodeID(station, rv.Model, 0) == node {
			return rv.Model
		}
	}
	return ""
}
