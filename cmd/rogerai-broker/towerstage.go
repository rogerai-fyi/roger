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
	"sort"
	"strings"
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

// stageScanLimit bounds one kind-filtered read of the record. Disqualifiers and excusals are
// rare by nature (a few a month at most); the bound only keeps a runaway record from becoming
// a runaway query.
const stageScanLimit = 1000

// disqualifierKinds are the kinds that can count against "clean" by themselves; a lifecycle
// event counts only when it moved the Tower to suspended or revoked, and is read by that state
// so a Tower's own drain/resume toggling can never fill a bounded read.
var (
	disqualifierKinds  = []string{evIncident, evHealthFailing, evLeaseLapsed}
	disqualifierStates = []admit.State{admit.StateSuspended, admit.StateRevoked}
)

// stageView is a Tower's stage as the record says, and the facts it was computed from.
type stageView struct {
	stage    placement.Stage
	last     admit.Event // the latest trust_stage event
	hasLast  bool
	entered  time.Time
	ceiling  placement.Stage
	disq     []admit.Event // unexcused disqualifiers since the latest stage event, oldest first
	excused  map[int64]bool
	approved time.Time // the latest approval (quarantine to active)
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

// eventIDs reads a list of event ids back from a detail value (int64s in memory, float64s
// after a JSON round trip).
func eventIDs(v any) []int64 {
	var out []int64
	switch l := v.(type) {
	case []int64:
		out = append(out, l...)
	case []any:
		for _, x := range l {
			if id := eventID(x); id != 0 {
				out = append(out, id)
			}
		}
	}
	return out
}

func timeOf(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// readStage reads what the stage depends on with kind-filtered queries, so no number of
// unrelated events can push the stage, the ceiling or a disqualifier out of view.
func (b *broker) readStage(towerID string, now time.Time) (stageView, error) {
	reg := b.tower.registry
	v := stageView{stage: stageNew, excused: map[int64]bool{}}
	var err error
	if v.last, v.hasLast, err = reg.FindEvent(towerID, []string{evTrustStage}, false); err != nil {
		return v, err
	}
	ceil, hasCeil, err := reg.FindEvent(towerID, []string{evStageCeiling}, false)
	if err != nil {
		return v, err
	}
	if hasCeil {
		v.ceiling = placement.Stage(ceil.To)
	}
	appr, hasAppr, err := reg.FindTransition(towerID, admit.StateQuarantine, admit.StateActive)
	if err != nil {
		return v, err
	}
	v.approved, v.hasAppr = appr.At, hasAppr
	excusals, err := reg.EventsOfKinds(towerID, []string{evExcusal}, time.Time{}, stageScanLimit)
	if err != nil {
		return v, err
	}
	for _, ex := range excusals {
		if id := eventID(ex.Detail["event_id"]); id != 0 {
			v.excused[id] = true
		}
	}
	since, sinceID := v.approved, int64(0)
	listed := map[int64]bool{} // causes a demotion already names
	if v.hasLast {
		if placement.ValidStage(placement.Stage(v.last.To)) {
			v.stage = placement.Stage(v.last.To)
		}
		v.entered = v.last.At
		if t, ok := timeOf(v.last.Detail["entered_at"]); ok {
			v.entered = t
		}
		// Counted from when the stage was ENTERED, not from when its event was written, so a
		// disqualifier that landed between the sweep's read and its write is still seen. A
		// demotion's entered_at is its latest cause, and the causes it names are not counted again.
		since, sinceID = v.entered, 0
		for _, c := range eventIDs(v.last.Detail["cause_events"]) {
			listed[c] = true
		}
	}
	if !v.hasLast && !v.hasAppr {
		return v, nil
	}
	from := since.Add(-time.Nanosecond)
	// A lease lapse is judged an hour after it happens, so one that landed within the hour
	// before the stage was entered is still read: entering a stage in that hour must not
	// excuse it if the lease was never renewed.
	cands, err := reg.EventsOfKinds(towerID, disqualifierKinds, from.Add(-leaseLapseGrace), stageScanLimit)
	if err != nil {
		return v, err
	}
	moved, err := reg.EventsMovingTo(towerID, evLifecycle, disqualifierStates, from, stageScanLimit)
	if err != nil {
		return v, err
	}
	cands = append(cands, moved...)
	sort.SliceStable(cands, func(i, j int) bool {
		if !cands[i].At.Equal(cands[j].At) {
			return cands[i].At.Before(cands[j].At)
		}
		return cands[i].ID < cands[j].ID
	})
	for _, ev := range cands {
		// After the stage was entered: later in time, or at the same instant but later in the
		// record, and never a cause the demotion already names.
		after := ev.At.After(since) || (ev.At.Equal(since) && ev.ID > sinceID) ||
			(ev.Kind == evLeaseLapsed && ev.At.After(since.Add(-leaseLapseGrace)))
		if !after || v.excused[ev.ID] || listed[ev.ID] {
			continue
		}
		dq, err := b.disqualifies(towerID, ev, now)
		if err != nil {
			return v, err
		}
		if dq {
			v.disq = append(v.disq, ev)
		}
	}
	return v, nil
}

// disqualifies reports whether one recorded event counts against "clean".
func (b *broker) disqualifies(towerID string, ev admit.Event, now time.Time) (bool, error) {
	switch ev.Kind {
	case evIncident, evHealthFailing:
		return true, nil
	case evLifecycle:
		return ev.To == admit.StateSuspended || ev.To == admit.StateRevoked, nil
	case evLeaseLapsed:
		if now.Sub(ev.At) < leaseLapseGrace {
			return false, nil
		}
		after, err := b.tower.registry.EventsOfKinds(towerID, []string{evCertRenewed, evLifecycle}, ev.At, stageScanLimit)
		if err != nil {
			return false, err
		}
		for _, o := range after {
			// Only a real restoration of the lease counts: a renewal, or Core re-admitting the
			// Tower from quarantine or an expired lease. An operator's own resume from a drain
			// does not touch the lease and recovers nothing.
			readmitted := o.Kind == evLifecycle && o.To == admit.StateActive &&
				(o.From == admit.StateQuarantine || o.From == admit.StateExpired) &&
				!strings.HasPrefix(o.Actor, "operator:")
			recovered := o.Kind == evCertRenewed || readmitted
			if recovered && !o.At.After(ev.At.Add(leaseLapseGrace)) {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

// disqualifyReason names a disqualifier in the demotion event.
func disqualifyReason(ev admit.Event) string {
	if ev.Kind == evLifecycle {
		return string(ev.To)
	}
	return ev.Kind
}

// stageCacheTTL is how long a computed stage is reused on the request path. A write on THIS
// instance that can change a stage drops the entry at once; a peer instance's write is seen
// within the TTL.
const stageCacheTTL = 15 * time.Second

// stageFailCacheTTL is how long an unreadable stage is remembered as "new".
const stageFailCacheTTL = 5 * time.Second

type cachedStage struct {
	stage placement.Stage
	at    time.Time
}

// stageKinds are the event kinds whose write drops a cached stage.
var stageKinds = map[string]bool{evTrustStage: true, evStageCeiling: true, evExcusal: true,
	evIncident: true, evHealthFailing: true, evLifecycle: true, evLeaseLapsed: true}

// dropStageCache forgets a Tower's cached stage when an event that can change it is written.
func (b *broker) dropStageCache(ev admit.Event) {
	if !stageKinds[ev.Kind] {
		return
	}
	b.stageMu.Lock()
	delete(b.stageCache, ev.TowerID)
	if b.stageGen == nil {
		b.stageGen = map[string]uint64{}
	}
	b.stageGen[ev.TowerID]++ // a read in flight from before this write may not cache its answer
	b.stageMu.Unlock()
	b.stageFlight.Forget(ev.TowerID)
}

// towerStage is a Tower's effective stage: the recorded stage, "new" at once on an unexcused
// disqualifier, never above an administrator's ceiling. An unreadable record is "new": a
// stage is earned, never assumed. Cached for stageCacheTTL.
func (b *broker) towerStage(towerID string) placement.Stage {
	now := time.Now()
	b.stageMu.Lock()
	if c, ok := b.stageCache[towerID]; ok && now.Sub(c.at) < stageCacheTTL {
		b.stageMu.Unlock()
		return c.stage
	}
	b.stageMu.Unlock()
	// One read per Tower however many requests miss the cache at once.
	v, _, _ := b.stageFlight.Do(towerID, func() (any, error) {
		// A caller that queued behind a finished flight finds the fresh entry here.
		b.stageMu.Lock()
		if c, ok := b.stageCache[towerID]; ok && time.Since(c.at) < stageCacheTTL {
			b.stageMu.Unlock()
			return c.stage, nil
		}
		gen := b.stageGen[towerID]
		b.stageMu.Unlock()
		st, at := stageNew, now
		if v, err := b.readStage(towerID, now); err == nil {
			st = v.stage
			if len(v.disq) > 0 {
				st = stageNew
			}
			st = placement.MinStage(st, v.ceiling)
		} else {
			// A store that cannot answer is answered "new" for a short while rather than asked
			// again by every request.
			at = now.Add(stageFailCacheTTL - stageCacheTTL)
		}
		b.stageMu.Lock()
		if b.stageCache == nil {
			b.stageCache = map[string]cachedStage{}
		}
		if b.stageGen[towerID] == gen { // nothing that could change the stage was written meanwhile
			b.stageCache[towerID] = cachedStage{stage: st, at: at}
		}
		b.stageMu.Unlock()
		return st, nil
	})
	return v.(placement.Stage)
}

// towerEdgeWeight is the weight a Tower's side carries on the per-request coin: 1.0 for a
// house Tower, its stage's weight for a community one.
func (b *broker) towerEdgeWeight(towerID string) float64 {
	if tw, ok := b.tower.registry.Get(towerID); ok && b.isHouseOwner(tw.Owner) {
		return 1.0
	}
	return stageCaps[b.towerStage(towerID)].weight
}

// sweepStage is the governance sweep's stage pass for one Tower. Every write goes through
// RecordIfLast on the trust_stage kind, so two instances computing the same change write it
// once.
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
	lastID := int64(0)
	if v.hasLast {
		lastID = v.last.ID
	}
	write := func(from, to placement.Stage, reason string, detail map[string]any) {
		b.towerRecordIfLast(admit.Event{TowerID: tw.ID, Kind: evTrustStage, Actor: admit.ActorSystem,
			From: admit.State(from), To: admit.State(to), Reason: reason, Detail: detail},
			[]string{evTrustStage}, lastID)
	}
	if !v.hasLast {
		write("", stageNew, "approved", map[string]any{"entered_at": v.approved.UTC().Format(time.RFC3339Nano)})
		return
	}
	// A demotion all of whose causes have since been excused is undone: back to the stage it
	// took away, counted from when that stage was first entered.
	if causes := eventIDs(v.last.Detail["cause_events"]); len(causes) > 0 && len(v.disq) == 0 {
		all := true
		for _, c := range causes {
			all = all && v.excused[c]
		}
		if prior := placement.Stage(fmt.Sprint(v.last.Detail["prior"])); all && placement.ValidStage(prior) {
			write(v.stage, prior, "excused", map[string]any{"entered_at": v.last.Detail["prior_entered"], "excused_events": causes})
			return
		}
	}
	if len(v.disq) > 0 {
		ids := make([]int64, len(v.disq))
		for i, d := range v.disq {
			ids[i] = d.ID
		}
		latest := v.disq[len(v.disq)-1]
		// prior is the stage the record held before these causes; a demotion of a demotion
		// keeps pointing at the stage that was actually earned.
		prior, priorEntered := v.stage, v.entered.UTC().Format(time.RFC3339Nano)
		if eventIDs(v.last.Detail["cause_events"]) != nil {
			prior = placement.Stage(fmt.Sprint(v.last.Detail["prior"]))
			if pe, ok := v.last.Detail["prior_entered"].(string); ok {
				priorEntered = pe
			}
			ids = append(eventIDs(v.last.Detail["cause_events"]), ids...)
		}
		write(v.stage, stageNew, disqualifyReason(latest), map[string]any{
			"cause_events": ids, "prior": string(prior), "prior_entered": priorEntered,
			"entered_at": latest.At.UTC().Format(time.RFC3339Nano)})
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
		"days": int(now.Sub(v.entered).Hours() / 24),
		// The new stage starts when the evidence was READ, so anything after that read counts.
		"entered_at": now.UTC().Format(time.RFC3339Nano)})
}

// noteHealthStreak records health_failing once per run of failures, as soon as the run reaches
// two. Keyed on when the run began rather than on its length, so two instances recording at
// once (the count may skip from one to three) still write it, and write it once.
func (b *broker) noteHealthStreak(towerID string) {
	sum, err := b.tower.health.Summary(towerID, time.Now())
	if err != nil || sum.FailStreak < 2 || sum.StreakSince.IsZero() {
		return
	}
	last, found, err := b.tower.registry.FindEvent(towerID, []string{evHealthFailing}, false)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	lastID := int64(0)
	if found {
		if !last.At.Before(sum.StreakSince) {
			return // this run is already recorded
		}
		lastID = last.ID
	}
	b.towerRecordIfLast(admit.Event{TowerID: towerID, Kind: evHealthFailing, Actor: admit.ActorSystem,
		Reason: "two health checks failed in a row"}, []string{evHealthFailing}, lastID)
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
	evs, err := b.tower.registry.EventsOfKinds(tw.ID, disqualifierKinds, time.Time{}, stageScanLimit)
	if err == nil {
		var moved []admit.Event
		moved, err = b.tower.registry.EventsMovingTo(tw.ID, evLifecycle, disqualifierStates, time.Time{}, stageScanLimit)
		evs = append(evs, moved...)
	}
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
	if tw.State == admit.StateRevoked {
		return nil // revocation ends the stage with the Tower
	}
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
	st := placement.MinStage(v.stage, v.ceiling)
	if len(v.disq) > 0 {
		st = stageNew
	}
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
