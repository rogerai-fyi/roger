package main

// towermobility.go: moving idle Stations between Towers (features/tower/station_mobility.feature;
// docs/relay-selection-design.md sections 6.3b and 6.3c, founder rulings 2026-08-20).
//
// A move is a dormant write and a nudge. The mobility pass finds Stations that are due - their
// Tower is gone, failing, demoted below them, or a strictly better Tower has held up over three
// fresh observations - waits until Core can see the Station idle, sets it dormant only if it is
// still the row it read, and tells its node to attach again. The node re-attaches with its own
// Station id and keys; placement puts it on the intended Tower and Admit's revival raises the
// epoch, which stays Admit's alone to write. Anything the move caught is voided by the built
// epoch fence. A move whose node never comes back is undone, never left dark.

import (
	"encoding/json"
	"sort"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/mobility"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

const (
	evStationMoveStarted   = "station_move_started"
	evStationMoved         = "station_moved"
	evStationMovedAway     = "station_moved_away"
	evStationMoveReturned  = "station_move_returned"
	evStationMoveAbandoned = "station_move_abandoned"
)

// Triggers, as recorded.
const (
	triggerGone    = "tower_gone"
	triggerFailing = "tower_failing"
	triggerBetter  = "better_tower"
	triggerCap     = "stage_cap"
)

// The founder-approved numbers (2026-11-13) and the spec's pacing.
const (
	betterObservations    = 3
	movesPerTowerPerPass  = 5
	movesIntoTowerPerPass = 5
	movesPerPass          = 20
	moveNudgeEvery        = 30 * time.Second
	moveNudges            = 3
	moveSkipFor           = time.Hour
)

// observationStaleAfter discards a streak whose observations have stopped: twice the
// health-check cadence, derived rather than chosen.
const observationStaleAfter = 2 * canaryInterval

// moveStreak is one Station's run of fresh observations favoring one target Tower.
type moveStreak struct {
	target string
	count  int
	lastAt time.Time
}

// movePace counts moves started in one pass.
type movePace struct {
	from, into map[string]int
	total      int
}

func (p *movePace) allow(from, into string) bool {
	if p.total >= movesPerPass || p.from[from] >= movesPerTowerPerPass ||
		(into != "" && p.into[into] >= movesIntoTowerPerPass) {
		return false
	}
	return true
}

func (p *movePace) note(from, into string) {
	p.total++
	p.from[from]++
	if into != "" {
		p.into[into]++
	}
}

// nudgeable reports whether a Station's node can be told to re-attach: it declared the nudge
// at attach and holds the cancel long-poll the nudge rides on.
func (b *broker) nudgeable(at attach.Attachment) bool {
	f, ok, err := b.tower.mobility.Facts(at.StationID)
	return err == nil && ok && f.Nudge && at.NodeID != "" && b.cancelCapable(at.NodeID)
}

// mobilityPassOnce runs one pass: finish or undo moves in progress, then start new ones.
func (b *broker) mobilityPassOnce(now time.Time) {
	ts := b.tower
	if ts == nil || ts.mobility == nil {
		return
	}
	if b.holdsMobilityLease() {
		b.mobilityPending(now)
	}
	pace := &movePace{from: map[string]int{}, into: map[string]int{}}
	for _, tw := range ts.registry.All() {
		atts, err := ts.stationStore.ByTower(tw.ID)
		if err != nil || len(atts) == 0 {
			continue
		}
		var live []attach.Attachment
		for _, at := range atts {
			if at.Live() {
				live = append(live, at)
			}
		}
		if len(live) == 0 {
			continue
		}
		if !ts.link.Live(tw.ID) || !ts.registry.MayTakeWork(tw.ID) {
			// Gone: no observation count and no idle gate - work through a Tower that is gone
			// cannot complete.
			for _, at := range live {
				if pace.allow(tw.ID, "") && b.startMove(at, "", triggerGone, now) {
					pace.note(tw.ID, "")
				}
			}
			continue
		}
		if !ts.link.HoldsLocally(tw.ID) {
			continue // the instance holding the link runs this Tower's streaks
		}
		b.mobilityTower(tw, live, pace, now)
	}
}

// mobilityTower decides which of one live Tower's Stations are due, on this instance.
func (b *broker) mobilityTower(tw admit.Tower, live []attach.Attachment, pace *movePace, now time.Time) {
	ts := b.tower
	sum, _ := ts.health.Summary(tw.ID, now)
	fresh := !sum.LastAt.IsZero() && now.Sub(sum.LastAt) <= observationStaleAfter
	b.moveMu.Lock()
	if b.towerObsAt == nil {
		b.towerObsAt, b.moveStreaks = map[string]time.Time{}, map[string]moveStreak{}
	}
	newObs := fresh && sum.LastAt.After(b.towerObsAt[tw.ID])
	if newObs {
		b.towerObsAt[tw.ID] = sum.LastAt
	}
	b.moveMu.Unlock()
	failing := fresh && sum.FailStreak >= 2
	overCap := b.overStageCap(tw, live)

	for _, at := range live {
		trigger, target := "", ""
		switch {
		case failing:
			trigger = triggerFailing
		case overCap[at.StationID]:
			trigger = triggerCap
		case newObs:
			target = b.observeBetter(at, tw.ID, sum.LastAt)
			if target != "" {
				trigger = triggerBetter
			}
		}
		if trigger == "" {
			continue
		}
		// A Station whose last move was undone because its node never came back is not moved
		// again while that move's skip lasts: its node is not answering, and a new move would
		// only churn it dormant and back.
		if _, until, err := ts.mobility.Skip(at.StationID); err == nil && now.Before(until) {
			continue
		}
		if (trigger == triggerBetter || trigger == triggerCap) && !b.nudgeable(at) {
			continue // an old node is moved only when its Tower is gone or failing
		}
		if ok, _ := b.stationQuiescent(at.NodeID); !ok {
			continue // still due; checked again next pass
		}
		if pace.allow(tw.ID, target) && b.startMove(at, target, trigger, now) {
			pace.note(tw.ID, target)
		}
	}
}

// overStageCap names the newest Stations above a community Tower's caps for its stage.
func (b *broker) overStageCap(tw admit.Tower, live []attach.Attachment) map[string]bool {
	out := map[string]bool{}
	if b.isHouseOwner(tw.Owner) {
		return out
	}
	caps := stageCaps[b.towerStage(tw.ID)]
	var comm, house []attach.Attachment
	for _, at := range live {
		if b.isHouseOwner(at.Owner) {
			house = append(house, at)
		} else {
			comm = append(comm, at)
		}
	}
	mark := func(group []attach.Attachment, limit int) {
		if limit < 0 || len(group) <= limit {
			return
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].AttachedAt.Before(group[j].AttachedAt) })
		for _, at := range group[limit:] {
			out[at.StationID] = true
		}
	}
	mark(comm, caps.community)
	mark(house, caps.house)
	return out
}

// observeBetter advances a Station's streak on one fresh observation and returns the target
// once a strictly better Tower has held for betterObservations in a row.
func (b *broker) observeBetter(at attach.Attachment, current string, obsAt time.Time) string {
	f, _, _ := b.tower.mobility.Facts(at.StationID)
	alt, ok := b.placeStation(at.Owner, f.Locality, map[string]bool{current: true})
	better := ok && placement.StrictlyBetter(alt.cand, b.towerCandidate(current, f.Locality), b.isHouseOwner(at.Owner))
	b.moveMu.Lock()
	defer b.moveMu.Unlock()
	st := b.moveStreaks[at.StationID]
	switch {
	case !better:
		delete(b.moveStreaks, at.StationID)
		return ""
	case st.target != alt.towerID || obsAt.Sub(st.lastAt) > observationStaleAfter:
		st = moveStreak{target: alt.towerID, count: 1, lastAt: obsAt}
	default:
		st.count++
		st.lastAt = obsAt
	}
	b.moveStreaks[at.StationID] = st
	if st.count >= betterObservations {
		return st.target
	}
	return ""
}

// startMove sets a live Station dormant for a move, only if it is still the row the pass read,
// and records and announces it. It reports whether the move started.
func (b *broker) startMove(at attach.Attachment, target, trigger string, now time.Time) bool {
	ts := b.tower
	ok, err := ts.stationStore.DormantForMove(at.StationID, at.Origin.TowerID, at.Epoch)
	if err != nil || !ok {
		return false // a concurrent change won: nothing written, nothing said
	}
	started, err := ts.mobility.StartMove(mobility.Move{StationID: at.StationID, From: at.Origin.TowerID,
		To: target, Trigger: trigger, Epoch: at.Epoch, PriorState: at.State, Started: now})
	if err != nil || !started {
		_, _ = ts.stationStore.UndoMove(at.StationID, at.Origin.TowerID, at.Epoch, at.State)
		return false
	}
	b.moveMu.Lock()
	delete(b.moveStreaks, at.StationID)
	b.moveMu.Unlock()
	b.metricsMu.Lock()
	load := map[string]any{"inflight": b.inflight[at.NodeID], "edge_load": b.edgeLoad[at.NodeID],
		"peer_inflight": b.peerInflight[at.NodeID], "peer_edge_load": b.peerEdgeLoad[at.NodeID]}
	b.metricsMu.Unlock()
	detail := map[string]any{"station": at.StationID, "to": target, "epoch": at.Epoch}
	for k, v := range load {
		detail[k] = v
	}
	b.towerEvent(admit.Event{TowerID: at.Origin.TowerID, Kind: evStationMoveStarted, Actor: admit.ActorSystem,
		Reason: trigger, Detail: detail})
	b.nudgeMove(at, trigger, now)
	b.publishRoutable(at.Origin.TowerID)
	return true
}

// nudgeMove counts one nudge attempt and sends it when the node can hear it.
func (b *broker) nudgeMove(at attach.Attachment, trigger string, now time.Time) {
	_, _ = b.tower.mobility.NoteNudge(at.StationID, now)
	if b.nudgeable(at) {
		b.sendReattach(at.NodeID, trigger)
	}
}

// towerMobilityInterval paces the mobility pass: under half the nudge retry, so a lost nudge is
// resent close to on time.
const towerMobilityInterval = 15 * time.Second

// towerMobility runs the mobility pass on its own ticker.
func (b *broker) towerMobility(stop <-chan struct{}, every time.Duration) {
	if b.tower == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			b.mobilityPassOnce(now)
		}
	}
}

// mobilityLease is held by one instance per pass for the fleet-wide half of mobility (moves in
// progress), so two instances do not both re-nudge or undo the same move.
const mobilityLeaseKey = "rogerai:mobility:pending"

func (b *broker) holdsMobilityLease() bool {
	if !b.multiInstance || b.shared == nil {
		return true
	}
	set, err := b.shared.setIfAbsent(mobilityLeaseKey, b.instanceID, towerMobilityInterval-time.Second)
	return err == nil && set
}

// mobilityPending re-nudges moves whose node has not come back, and undoes the ones that never
// will. A move off a Tower that is gone is never undone back onto it.
func (b *broker) mobilityPending(now time.Time) {
	ts := b.tower
	moves, err := ts.mobility.Pending()
	if err != nil {
		return
	}
	for _, mv := range moves {
		at, found, err := ts.stationStore.ByStation(mv.StationID)
		if err != nil {
			continue
		}
		if !found || at.State != attach.StateDormant || at.Epoch != mv.Epoch {
			_ = ts.mobility.FinishMove(mv.StationID, "", time.Time{}) // it moved, or ended
			continue
		}
		last := mv.LastNudge
		if last.IsZero() {
			last = mv.Started
		}
		if now.Sub(last) < moveNudgeEvery {
			continue
		}
		if mv.Nudges < moveNudges {
			b.nudgeMove(at, mv.Trigger, now)
			continue
		}
		if mv.Trigger == triggerGone {
			_ = ts.mobility.FinishMove(mv.StationID, "", time.Time{})
			b.towerEvent(admit.Event{TowerID: mv.From, Kind: evStationMoveAbandoned, Actor: admit.ActorSystem,
				Reason: "no re-attach", Detail: map[string]any{"station": mv.StationID, "left": "dormant"}})
			continue
		}
		if ok, uerr := ts.stationStore.UndoMove(mv.StationID, mv.From, mv.Epoch, mv.PriorState); uerr != nil || !ok {
			continue
		}
		_ = ts.mobility.FinishMove(mv.StationID, mv.To, now.Add(moveSkipFor))
		b.towerEvent(admit.Event{TowerID: mv.From, Kind: evStationMoveAbandoned, Actor: admit.ActorSystem,
			Reason: "no re-attach", Detail: map[string]any{"station": mv.StationID, "to": mv.To, "epoch": mv.Epoch}})
		b.publishRoutable(mv.From)
	}
}

// moveOnReattach is the attach handler's half of a move: for a dormant Station with a move in
// progress, the Tower to prefer and the Towers to exclude.
func (b *broker) moveOnReattach(stationID string, now time.Time) (mobility.Move, bool, map[string]bool) {
	exclude := map[string]bool{}
	if tw, until, err := b.tower.mobility.Skip(stationID); err == nil && tw != "" && now.Before(until) {
		exclude[tw] = true
	}
	mv, moving, err := b.tower.mobility.Move(stationID)
	if err != nil || !moving {
		return mobility.Move{}, false, exclude
	}
	exclude[mv.From] = true
	return mv, true, exclude
}

// finishMoveOnReattach records where a moving Station landed and ends the move.
func (b *broker) finishMoveOnReattach(mv mobility.Move, stationID, towerID string, epoch int64) {
	_ = b.tower.mobility.FinishMove(stationID, "", time.Time{})
	if towerID == mv.From {
		b.towerEvent(admit.Event{TowerID: towerID, Kind: evStationMoveReturned, Actor: admit.ActorSystem,
			Reason: mv.Trigger, Detail: map[string]any{"station": stationID, "epoch": epoch}})
		return
	}
	detail := map[string]any{"station": stationID, "from": mv.From, "epoch": epoch, "intended": mv.To}
	if mv.To != "" && mv.To != towerID {
		detail["intended_unavailable"] = true // the intended Tower was no longer eligible
	}
	b.towerEvent(admit.Event{TowerID: towerID, Kind: evStationMoved, Actor: admit.ActorSystem, Reason: mv.Trigger,
		Detail: detail})
	b.towerEvent(admit.Event{TowerID: mv.From, Kind: evStationMovedAway, Actor: admit.ActorSystem, Reason: mv.Trigger,
		Detail: map[string]any{"station": stationID, "to": towerID, "epoch": epoch}})
}

// sendReattach queues a re-attach nudge on the node's cancel long-poll. It names no Tower: the
// node's only action is to attach to Core again over its pinned address.
func (b *broker) sendReattach(node, reason string) {
	e := cancelEntry{Reattach: reason, Exp: time.Now().Add(cancelBufTTL).UnixMilli()}
	if b.shared != nil {
		raw, _ := json.Marshal(e)
		_ = b.shared.cancelPush(node, raw, cancelBufTTL)
		return
	}
	b.cancels.mu.Lock()
	b.cancels.init()
	b.cancels.buf[node] = append(b.cancels.buf[node], e)
	close(b.cancels.wakeCh(node))
	delete(b.cancels.wake, node)
	b.cancels.mu.Unlock()
}

// recordStationFacts writes where a Station attached from and whether its node can hear the
// nudge, whole, on every attach.
func (b *broker) recordStationFacts(stationID string, loc placement.Locality, nudge bool) {
	if err := b.tower.mobility.PutFacts(stationID, mobility.Facts{Locality: loc, Nudge: nudge}); err != nil {
		b.towerGovUnavailable(err)
	}
}
