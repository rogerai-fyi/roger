package main

import (
	"net/http"
	"sort"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/earnings"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/origin"
	"rogerai.fm/roger/v6/internal/towercore/reputation"
)

// Short aliases for the read types the detail view assembles, so the view builders read
// cleanly without qualifying every package.
type (
	admitTower           = admit.Tower
	reputationTally      = reputation.Tally
	earningsTowerTraffic = earnings.TowerTraffic
	originTally          = origin.Tally
	fleetStation         = fleet.Station
	originStoreIface     = origin.Store
	fleetStoreIface      = fleet.Store
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

	towers := ts.registry.All()
	out := make([]admitTowerRow, 0, len(towers))
	for _, tw := range towers {
		out = append(out, adminTowerRowOf(ts, tw))
	}
	writeJSON(w, http.StatusOK, map[string]any{"towers": out})
}

// adminTowerDetail handles GET /admin/tower?id=<towerID>: everything Core knows about ONE
// Tower, gathered for the dashboard's detail page - identity and lifecycle, the quality
// signals that decide whether it may carry traffic (with the thresholds it is judged
// against), the traffic it carried by model and the country demand came from, and the
// Stations serving behind it. It is a READ surface: the lifecycle controls are their own
// endpoints. It carries no keys, no tokens, and no consumer identity - the traffic and
// origin blocks answer "how much, on what, from where", never "who".
func (b *broker) adminTowerDetail(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	if b.requireAdmin(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "a tower id is required")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}

	var found *admitTowerRow
	for _, tw := range ts.registry.All() {
		if tw.ID == id {
			t := adminTowerRowOf(ts, tw)
			found = &t
			break
		}
	}
	if found == nil {
		jsonErr(w, http.StatusNotFound, "no such tower")
		return
	}

	// All-time window: the detail view shows a Tower's whole record. A dashboard that wants a
	// recent slice can pass its own window later; today the reads take a zero `since`.
	//
	// A failed read is surfaced, never swallowed: this is a money-and-quality view, and
	// rendering silent zeros for a Tally or a traffic total that failed to load would show an
	// operator a Tower that looks clean and idle when the truth is simply unknown - the exact
	// misread that leads to a wrong suspension or a missed one.
	var since time.Time
	tally, err := ts.outcomes.Tally(id, since)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not read the Tower's reputation")
		return
	}
	byStation, err := ts.outcomes.TallyByStation(id, since)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not read the Tower's per-station reputation")
		return
	}
	traffic, err := ts.earnings.TowerTraffic(id, since)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not read the Tower's traffic")
		return
	}
	origins, err := ts.origin.ByTower(id, since)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not read the Tower's traffic origin")
		return
	}
	stations, err := ts.routable.ByTower(id, time.Now())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not read the Tower's fleet")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tower_id":  found.TowerID,
		"owner":     found.Owner,
		"state":     found.State,
		"enrolled":  found.Enrolled,
		"link_live": found.LinkLive,
		"endpoint":  found.Endpoint,
		"quality":   towerQualityView(ts, tally, byStation),
		"traffic":   towerTrafficView(traffic),
		"origin":    towerOriginView(origins),
		"fleet":     towerFleetView(stations),
	})
}

// admitTowerRow is the identity-and-lifecycle summary both the queue and the detail view
// carry. No keys, no tokens - only what an approver needs to see.
type admitTowerRow struct {
	TowerID  string `json:"tower_id"`
	Owner    string `json:"owner"`
	State    string `json:"state"`
	Enrolled string `json:"enrolled"`
	LinkLive bool   `json:"link_live"`
	Endpoint string `json:"endpoint,omitempty"`
}

// adminTowerRowOf reads one Tower's identity row: who, when, what state, whether the link is
// live right now, and the endpoint it advertises when its link plane is known.
func adminTowerRowOf(ts *towerSubsystem, tw admitTower) admitTowerRow {
	row := admitTowerRow{
		TowerID:  tw.ID,
		Owner:    tw.Owner,
		State:    string(tw.State),
		Enrolled: tw.EnrolledAt.UTC().Format(time.RFC3339),
		LinkLive: ts.link.Live(tw.ID),
	}
	if p, has := ts.link.RelayPlane(tw.ID); has {
		row.Endpoint = p.Endpoint
	}
	return row
}

// towerQualityView renders the reputation tally alongside the thresholds it is judged
// against, so an approver sees not just the numbers but the Tower's distance from
// suspension. over_fail_threshold matches the quarantine condition Evaluate uses; when
// there are too few canaries the rate is not yet judgeable and no suspension can follow
// from it, so the two flags are reported independently.
func towerQualityView(ts *towerSubsystem, t reputationTally, byStation map[string]reputationTally) map[string]any {
	pol := ts.repPolicy
	canaries := t.CanaryPass + t.CanaryFail
	rate, rateKnown := t.CanaryFailRate()
	judgeable := canaries >= pol.MinCanaries
	overThreshold := rateKnown && judgeable && rate > pol.MaxCanaryFailRate

	stations := make([]map[string]any, 0, len(byStation))
	for sid, st := range byStation {
		stations = append(stations, map[string]any{
			"station_id":  sid,
			"canary_pass": st.CanaryPass,
			"canary_fail": st.CanaryFail,
			"total":       st.Total,
		})
	}
	sort.Slice(stations, func(i, j int) bool {
		return stations[i]["station_id"].(string) < stations[j]["station_id"].(string)
	})

	return map[string]any{
		"total":                t.Total,
		"canary_pass":          t.CanaryPass,
		"canary_fail":          t.CanaryFail,
		"corroborated":         t.Corroborated,
		"uncorroborated":       t.Uncorroborated,
		"audit_mismatch":       t.AuditMismatch,
		"station_fault":        t.StationFault,
		"min_canaries":         pol.MinCanaries,
		"max_canary_fail_rate": pol.MaxCanaryFailRate,
		"canary_fail_rate":     rate,
		"rate_judgeable":       judgeable,
		"over_fail_threshold":  overThreshold,
		"by_station":           stations,
	}
}

// towerTrafficView renders the per-model traffic rollup and its totals. Self-dealing is
// surfaced separately and never counted in what the Tower is owed. No consumer identity.
func towerTrafficView(tt earningsTowerTraffic) map[string]any {
	models := make([]map[string]any, 0, len(tt.Models))
	for _, m := range tt.Models {
		models = append(models, map[string]any{
			"model":          m.Model,
			"attempts":       m.Attempts,
			"corroborated":   m.Corroborated,
			"uncorroborated": m.Uncorroborated,
			"usage_in":       m.UsageIn,
			"usage_out":      m.UsageOut,
			"micros":         m.Micros,
			"self_dealt":     m.SelfDealt,
		})
	}
	return map[string]any{
		"attempts":   tt.Attempts,
		"usage_in":   tt.UsageIn,
		"usage_out":  tt.UsageOut,
		"micros":     tt.Micros,
		"self_dealt": tt.SelfDealt,
		"by_model":   models,
	}
}

// towerOriginView renders the coarse country demand map: attempts per country, country
// only, no address or identity.
func towerOriginView(tallies []originTally) []map[string]any {
	out := make([]map[string]any, 0, len(tallies))
	for _, t := range tallies {
		out = append(out, map[string]any{"country": t.Country, "attempts": t.Attempts})
	}
	return out
}

// towerFleetView renders the Stations serving behind the Tower, each with its model and
// advertised price.
func towerFleetView(stations []fleetStation) []map[string]any {
	out := make([]map[string]any, 0, len(stations))
	for _, s := range stations {
		out = append(out, map[string]any{
			"station_id": s.StationID,
			"model":      s.Model,
			"modality":   s.Modality,
			"price_in":   s.PriceIn,
			"price_out":  s.PriceOut,
		})
	}
	return out
}
