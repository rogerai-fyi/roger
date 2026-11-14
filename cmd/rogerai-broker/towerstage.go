package main

// towerstage.go: a community Tower's trust stage (features/tower/tower_trust_ramp.feature).
//
// The stage is in the governance record: the "to" of the Tower's latest trust_stage event. The
// governance sweep computes it from the record and the health evidence and writes a new event
// only when it changes. Demotion does not wait for the sweep: the reader treats any unexcused
// disqualifier newer than the latest stage event as "new" at once, and the sweep then writes
// the demotion with its cause. An administrator can excuse one specific event, record an
// incident, or set a ceiling; none of them can raise a stage past the evidence.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

const (
	evTrustStage    = "trust_stage"
	evHealthFailing = "health_failing"
	evExcusal       = "excusal"
	evIncident      = "incident"
	evStageCeiling  = "stage_ceiling"
)

const (
	stageNew         = placement.StageNew
	stageProving     = placement.StageProving
	stageEstablished = placement.StageEstablished
)

// stageCap is what a stage allows: live community Stations (-1 = up to capacity), house
// Stations as failover (counted per owner), and the edge weight on the per-request coin.
type stageCap struct {
	community int
	house     int
	weight    float64
}

// stageCaps are the founder-approved numbers (2026-11-13).
var stageCaps = map[placement.Stage]stageCap{
	stageNew:         {community: 10, house: 0, weight: 0.25},
	stageProving:     {community: 50, house: 5, weight: 0.5},
	stageEstablished: {community: -1, house: 25, weight: 1.0},
}

// leaseLapseGrace is how long a lapsed lease may stay lapsed before it counts against the
// Tower: a lapse renewed within the hour is not a demotion.
const leaseLapseGrace = time.Hour

// stageRecordScan bounds how many recent events a stage computation reads.
const stageRecordScan = 500

// stageView is a Tower's stage as the record says, and the facts it was computed from.
type stageView struct {
	stage    placement.Stage
	last     admit.Event // the latest trust_stage event
	hasLast  bool
	entered  time.Time
	ceiling  placement.Stage
	disq     *admit.Event // the oldest unexcused disqualifier since the stage was entered
	excused  map[int64]bool
	approved time.Time // the first approval (quarantine to active)
	hasAppr  bool
}

func eventID(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func timeOf(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// readStage reads the record once and works out what the reader and the sweep need.
func (b *broker) readStage(towerID string, now time.Time) (stageView, error) {
	evs, err := b.tower.registry.Events(towerID, stageRecordScan)
	if err != nil {
		return stageView{stage: stageNew}, err
	}
	v := stageView{stage: stageNew, excused: map[int64]bool{}}
	for _, ev := range evs { // newest first: the first of each kind is the latest
		switch ev.Kind {
		case evTrustStage:
			if !v.hasLast {
				v.last, v.hasLast = ev, true
			}
		case evExcusal:
			if id := eventID(ev.Detail["event_id"]); id != 0 {
				v.excused[id] = true
			}
		case evStageCeiling:
			if v.ceiling == "" {
				v.ceiling = placement.Stage(ev.To)
			}
		case evLifecycle:
			if ev.From == admit.StateQuarantine && ev.To == admit.StateActive {
				v.approved, v.hasAppr = ev.At, true // keeps overwriting: the oldest wins
			}
		}
	}
	since, sinceID := v.approved, int64(0)
	if v.hasLast {
		if placement.ValidStage(placement.Stage(v.last.To)) {
			v.stage = placement.Stage(v.last.To)
		}
		v.entered = v.last.At
		if t, ok := timeOf(v.last.Detail["entered_at"]); ok {
			v.entered = t
		}
		since, sinceID = v.last.At, v.last.ID
	}
	for i := len(evs) - 1; i >= 0; i-- { // oldest first
		ev := evs[i]
		// After the stage was entered: later in time, or at the same instant but later in the
		// record.
		after := ev.At.After(since) || (ev.At.Equal(since) && ev.ID > sinceID)
		if !after || v.excused[ev.ID] {
			continue
		}
		if disqualifies(ev, evs, now) {
			e := ev
			v.disq = &e
			break
		}
	}
	return v, nil
}

// disqualifies reports whether one recorded event counts against "clean".
func disqualifies(ev admit.Event, all []admit.Event, now time.Time) bool {
	switch ev.Kind {
	case evIncident, evHealthFailing:
		return true
	case evLifecycle:
		return ev.To == admit.StateSuspended || ev.To == admit.StateRevoked
	case evLeaseLapsed:
		if now.Sub(ev.At) < leaseLapseGrace {
			return false
		}
		for _, o := range all {
			recovered := o.Kind == evCertRenewed || (o.Kind == evLifecycle && o.To == admit.StateActive)
			if recovered && o.At.After(ev.At) && !o.At.After(ev.At.Add(leaseLapseGrace)) {
				return false
			}
		}
		return true
	}
	return false
}

// disqualifyReason names a disqualifier in the demotion event.
func disqualifyReason(ev admit.Event) string {
	if ev.Kind == evLifecycle {
		return string(ev.To)
	}
	return ev.Kind
}

// towerStage is a Tower's effective stage: the recorded stage, "new" at once on an unexcused
// disqualifier, never above an administrator's ceiling. An unreadable record is "new": a
// stage is earned, never assumed.
func (b *broker) towerStage(towerID string) placement.Stage {
	v, err := b.readStage(towerID, time.Now())
	if err != nil {
		return stageNew
	}
	st := v.stage
	if v.disq != nil {
		st = stageNew
	}
	return placement.MinStage(st, v.ceiling)
}

// towerEdgeWeight is the weight a Tower's side carries on the per-request coin: 1.0 for a
// house Tower, its stage's weight for a community one.
func (b *broker) towerEdgeWeight(towerID string) float64 {
	if tw, ok := b.tower.registry.Get(towerID); ok && b.isHouseOwner(tw.Owner) {
		return 1.0
	}
	return stageCaps[b.towerStage(towerID)].weight
}

// sweepStage is the governance sweep's stage pass for one Tower.
func (b *broker) sweepStage(tw admit.Tower, now time.Time) {
	if b.isHouseOwner(tw.Owner) || tw.State == admit.StateRevoked {
		return
	}
	v, err := b.readStage(tw.ID, now)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	if !v.hasLast && !v.hasAppr {
		return // never approved: no stage yet
	}
	write := func(from, to placement.Stage, reason string, detail map[string]any) {
		b.towerEvent(admit.Event{TowerID: tw.ID, Kind: evTrustStage, Actor: admit.ActorSystem,
			From: admit.State(from), To: admit.State(to), Reason: reason, Detail: detail})
	}
	if !v.hasLast {
		write("", stageNew, "approved", map[string]any{"entered_at": v.approved.UTC().Format(time.RFC3339Nano)})
		return
	}
	// A demotion whose cause has since been excused is undone: back to the stage it took
	// away, counted from when that stage was first entered.
	if cause := eventID(v.last.Detail["cause_event"]); cause != 0 && v.excused[cause] && v.disq == nil {
		if prior := placement.Stage(fmt.Sprint(v.last.Detail["prior"])); placement.ValidStage(prior) {
			write(v.stage, prior, "excused", map[string]any{"entered_at": v.last.Detail["prior_entered"], "excused_event": cause})
			return
		}
	}
	if v.disq != nil {
		write(v.stage, stageNew, disqualifyReason(*v.disq), map[string]any{
			"cause_event": v.disq.ID, "prior": string(v.stage),
			"prior_entered": v.entered.UTC().Format(time.RFC3339Nano),
			"entered_at":    v.disq.At.UTC().Format(time.RFC3339Nano)})
		return
	}
	sum, err := b.tower.health.Summary(tw.ID, v.entered)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	// The ceiling is applied by the reader, not written: the sweep records only what the
	// evidence earned, one step at a time.
	next, _ := placement.NextStage(placement.Evidence{Now: now, EnteredAt: v.entered, Current: v.stage,
		Passed: sum.Passed, Failed: sum.Failed})
	if next == v.stage {
		return
	}
	write(v.stage, next, "promoted", map[string]any{
		"passed": sum.Passed, "failed": sum.Failed, "pass_rate": fmt.Sprintf("%.3f", sum.Rate()),
		"days": int(now.Sub(v.entered).Hours() / 24)})
}

// noteHealthStreak records health_failing once per streak, when a Tower's health checks have
// just failed twice in a row.
func (b *broker) noteHealthStreak(towerID string) {
	sum, err := b.tower.health.Summary(towerID, time.Now())
	if err != nil || sum.FailStreak != 2 {
		return
	}
	b.towerEvent(admit.Event{TowerID: towerID, Kind: evHealthFailing, Actor: admit.ActorSystem,
		Reason: "two health checks failed in a row"})
}

// adminStageRequest is the body of the three admin stage routes.
type adminStageRequest struct {
	TowerID string          `json:"tower_id"`
	EventID int64           `json:"event_id"`
	Stage   placement.Stage `json:"stage"`
	Reason  string          `json:"reason"`
}

// adminStagePreamble is the shared start of the admin stage routes: admin only, a Tower that
// exists, and a reason, always.
func (b *broker) adminStagePreamble(w http.ResponseWriter, r *http.Request) (adminStageRequest, admit.Tower, string, bool) {
	var req adminStageRequest
	if corsCredsPreflight(w, r) || !allow(w, r, http.MethodPost) {
		return req, admit.Tower{}, "", false
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return req, admit.Tower{}, "", false
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return req, admit.Tower{}, "", false
	}
	if err := json.Unmarshal(readTowerBody(r), &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return req, admit.Tower{}, "", false
	}
	req.Reason = admit.CleanReason(req.Reason)
	if req.TowerID == "" || req.Reason == "" {
		jsonErr(w, http.StatusBadRequest, "tower_id and a reason are required")
		return req, admit.Tower{}, "", false
	}
	tw, ok := ts.registry.Get(req.TowerID)
	if !ok {
		jsonErr(w, http.StatusNotFound, "no such Tower")
		return req, admit.Tower{}, "", false
	}
	return req, tw, "admin:" + b.adminActor(r), true
}

// towerExcuse handles POST /tower/excuse: excuse one specific recorded event.
func (b *broker) towerExcuse(w http.ResponseWriter, r *http.Request) {
	req, tw, actor, ok := b.adminStagePreamble(w, r)
	if !ok {
		return
	}
	if req.EventID == 0 {
		jsonErr(w, http.StatusBadRequest, "event_id must name one recorded event")
		return
	}
	evs, err := b.tower.registry.Events(tw.ID, stageRecordScan)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "the record is unavailable - try again shortly")
		return
	}
	for _, ev := range evs {
		if ev.ID == req.EventID {
			b.towerEvent(admit.Event{TowerID: tw.ID, Kind: evExcusal, Actor: actor, Reason: req.Reason,
				Detail: map[string]any{"event_id": ev.ID, "event_kind": ev.Kind}})
			writeJSON(w, http.StatusOK, map[string]any{"excused": ev.ID})
			return
		}
	}
	jsonErr(w, http.StatusNotFound, "no such event on this Tower's record")
}

// towerIncident handles POST /tower/incident: an attributed incident, which demotes.
func (b *broker) towerIncident(w http.ResponseWriter, r *http.Request) {
	req, tw, actor, ok := b.adminStagePreamble(w, r)
	if !ok {
		return
	}
	b.towerEvent(admit.Event{TowerID: tw.ID, Kind: evIncident, Actor: actor, Reason: req.Reason})
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
}

// towerStageCeiling handles POST /tower/stage-ceiling: hold a Tower at or below a stage.
func (b *broker) towerStageCeiling(w http.ResponseWriter, r *http.Request) {
	req, tw, actor, ok := b.adminStagePreamble(w, r)
	if !ok {
		return
	}
	if !placement.ValidStage(req.Stage) {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("%q is not a trust stage", req.Stage))
		return
	}
	b.towerEvent(admit.Event{TowerID: tw.ID, Kind: evStageCeiling, Actor: actor, To: admit.State(req.Stage), Reason: req.Reason})
	writeJSON(w, http.StatusOK, map[string]any{"ceiling": req.Stage})
}

// stageReport is a Tower's stage and the evidence behind it, for the admin detail and (in
// part) the operator's own status. nil for a house Tower or one never approved.
func (b *broker) stageReport(tw admit.Tower, now time.Time) map[string]any {
	if b.isHouseOwner(tw.Owner) {
		return map[string]any{"stage": "house"}
	}
	v, err := b.readStage(tw.ID, now)
	if err != nil || (!v.hasLast && !v.hasAppr) {
		return nil
	}
	entered := v.entered
	if !v.hasLast {
		entered = v.approved
	}
	st := b.towerStage(tw.ID)
	sum, _ := b.tower.health.Summary(tw.ID, entered)
	if st != v.stage {
		entered, sum = now, health.Summary{} // just demoted: the new stage starts now
	}
	_, next := placement.NextStage(placement.Evidence{Now: now, EnteredAt: entered, Current: st,
		Passed: sum.Passed, Failed: sum.Failed, Ceiling: v.ceiling})
	caps := stageCaps[st]
	out := map[string]any{
		"stage": string(st), "entered_at": entered.UTC().Format(time.RFC3339),
		"passed": sum.Passed, "failed": sum.Failed, "pass_rate": fmt.Sprintf("%.3f", sum.Rate()),
		"days": int(now.Sub(entered).Hours() / 24), "next": next,
		"community_stations": caps.community, "house_stations": caps.house,
	}
	if v.ceiling != "" {
		out["ceiling"] = string(v.ceiling)
	}
	if len(v.excused) > 0 {
		out["excusals"] = len(v.excused)
	}
	return out
}

// operatorStageView is the part of the stage report a Tower's operator sees: the stage, its
// caps and what is left. The governance record itself stays admin only (ruled 2026-11-13).
func operatorStageView(rep map[string]any) map[string]any {
	if rep == nil {
		return nil
	}
	out := map[string]any{}
	for _, k := range []string{"stage", "next", "community_stations", "house_stations"} {
		if v, ok := rep[k]; ok {
			out[k] = v
		}
	}
	return out
}

// adminStageReport is stageReport for the admin detail, by Tower id.
func (b *broker) adminStageReport(towerID string) map[string]any {
	tw, ok := b.tower.registry.Get(towerID)
	if !ok {
		return nil
	}
	return b.stageReport(tw, time.Now())
}
