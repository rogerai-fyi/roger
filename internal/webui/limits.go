package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// PER-BAND SPEND LIMITS IN THE CONSOLE.
//
// The console could set a MONTHLY cap and nothing else. The per-band caps - the most you
// will pay for a turn, and the slowest station you will accept - lived only in the TUI's
// [3] CONFIG, so an operator who worked in the browser could not see or change the limits
// that were actually bounding their spend.
//
// ONE STORE, TWO FRONT-ENDS. These read and write the SAME LimitStore the TUI edits,
// handed in by the host. Giving the console its own copy would let the two disagree about
// what the operator is willing to pay - and the one that loses is whichever wrote first.

// SpendLimit is one band's caps. Zero means UNSET (no cap), never "refuse everything":
// a zero max-out that blocked every station would be a silent denial of service the
// operator never asked for.
type SpendLimit struct {
	MaxOut float64 `json:"max_out"`
	MinTPS float64 `json:"min_tps"`
}

// limitRow is one row of the console's spend table.
type limitRow struct {
	Model  string  `json:"model"`
	MaxOut float64 `json:"max_out"`
	MinTPS float64 `json:"min_tps"`
	// OnAir marks a model this machine is serving. It is here so the table can say which
	// rows are yours-to-provide vs yours-to-consume, the distinction that made the TUI's
	// version confusing enough to be worth a signpost.
	OnAir bool `json:"on_air"`
}

// handleLimits GETs every band's caps and POSTs one band's.
func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadLimits == nil {
		writeJSON(w, map[string]any{"limits": []limitRow{}, "configured": false})
		return
	}
	if r.Method == http.MethodPost {
		s.setLimit(w, r)
		return
	}
	set := s.opts.ReadLimits()
	// The rows are the union of "has a cap" and "this machine serves it", so a band the
	// operator has already bounded never disappears from the list just because it went off
	// air - losing sight of a live cap is how an unexplained refusal happens later.
	seen := map[string]bool{}
	out := make([]limitRow, 0, len(set))
	for mdl, l := range set {
		seen[mdl] = true
		out = append(out, limitRow{Model: mdl, MaxOut: l.MaxOut, MinTPS: l.MinTPS})
	}
	for _, rv := range s.ctrl.Snapshot().Rows {
		if seen[rv.Model] {
			for i := range out {
				if out[i].Model == rv.Model {
					out[i].OnAir = rv.OnAir
				}
			}
			continue
		}
		out = append(out, limitRow{Model: rv.Model, OnAir: rv.OnAir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	writeJSON(w, map[string]any{"limits": out, "configured": true})
}

// setLimit writes one band's caps. A NEGATIVE value is refused rather than clamped: a
// negative cap is not a smaller cap, it is a value the spend path would have to interpret,
// and silently rewriting what the operator typed is worse than telling them.
func (s *Server) setLimit(w http.ResponseWriter, r *http.Request) {
	if s.opts.WriteLimit == nil {
		http.Error(w, "spend limits are not editable on this node", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Model  string  `json:"model"`
		MaxOut float64 `json:"max_out"`
		MinTPS float64 `json:"min_tps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		http.Error(w, "which band?", http.StatusBadRequest)
		return
	}
	if req.MaxOut < 0 || req.MinTPS < 0 {
		http.Error(w, "a cap cannot be negative - use 0 for no cap", http.StatusBadRequest)
		return
	}
	s.opts.WriteLimit(model, SpendLimit{MaxOut: req.MaxOut, MinTPS: req.MinTPS})
	writeJSON(w, map[string]any{"ok": true})
}
