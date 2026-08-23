package main

import (
	"net/http"
	"time"
)

// adminTowers handles GET /admin/towers: the approval queue the dashboard reads. Every
// Tower on the registry, the waiting ones first, each row carrying what the approver
// needs - who, when, what state, whether its link is up right now, and the endpoint it
// advertises - and nothing they do not: no keys, no tokens, no session ids.
func (b *broker) adminTowers(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	if b.requireAdmin(w, r) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}

	type row struct {
		TowerID  string `json:"tower_id"`
		Owner    string `json:"owner"`
		State    string `json:"state"`
		Enrolled string `json:"enrolled"`
		LinkLive bool   `json:"link_live"`
		Endpoint string `json:"endpoint,omitempty"`
	}
	towers := ts.registry.All()
	out := make([]row, 0, len(towers))
	for _, tw := range towers {
		rw := row{
			TowerID:  tw.ID,
			Owner:    tw.Owner,
			State:    string(tw.State),
			Enrolled: tw.EnrolledAt.UTC().Format(time.RFC3339),
			LinkLive: ts.link.Live(tw.ID),
		}
		if p, has := ts.link.RelayPlane(tw.ID); has {
			rw.Endpoint = p.Endpoint
		}
		out = append(out, rw)
	}
	writeJSON(w, http.StatusOK, map[string]any{"towers": out})
}
