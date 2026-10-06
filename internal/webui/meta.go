package webui

import (
	"errors"
	"net/http"

	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/protocol"
)

// handleMeta serves /api/share/meta. Validation is the controller's (SetMeta), so the console
// refuses exactly what the TUI and `roger share` refuse, with the same reason.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model         string   `json:"model"`
		Category      string   `json:"category"` // "cat" or "cat/sub", as operators type it
		Tags          []string `json:"tags"`
		Tuning        string   `json:"tuning"`
		BaseModel     string   `json:"base_model"`
		ContentPolicy string   `json:"content_policy"`
		License       string   `json:"license"`
	}
	// ONE source of the model per method: the query for GET/DELETE, the body for POST.
	var err error
	switch r.Method {
	case http.MethodGet:
		req.Model = r.URL.Query().Get("model")
	case http.MethodPost:
		if !decode(r, &req) {
			http.Error(w, "malformed body", http.StatusBadRequest)
			return
		}
		cat, sub := protocol.SplitCategory(req.Category)
		err = s.ctrl.SetMeta(req.Model, protocol.Taxonomy{Category: cat, Subcategory: sub, Tags: req.Tags,
			Tuning: req.Tuning, BaseModel: req.BaseModel, ContentPolicy: req.ContentPolicy, License: req.License})
	case http.MethodDelete:
		req.Model = r.URL.Query().Get("model")
		err = s.ctrl.ClearMeta(req.Model)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case errors.Is(err, node.ErrNotServed):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	declared, inferred, ok := s.ctrl.MetaFor(req.Model)
	if !ok {
		http.Error(w, node.ErrNotServed.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"declared": declared, "inferred": inferred})
}
