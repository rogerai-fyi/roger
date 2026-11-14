package main

// towerstage.go: a community Tower's trust stage (features/tower/tower_trust_ramp.feature).
// The stage a Tower is in is the "to" of its latest trust_stage event; a community Tower with
// none yet is "new".



type trustStage string

const (
	stageNew         trustStage = "new"
	stageProving     trustStage = "proving"
	stageEstablished trustStage = "established"
)

const evTrustStage = "trust_stage"

// stageCap is what a stage allows: live community Stations (-1 = up to capacity), house
// Stations as failover, and the edge weight on the per-request coin.
type stageCap struct {
	community int
	house     int
	weight    float64
}

// stageCaps are the founder-approved numbers (2026-11-13).
var stageCaps = map[trustStage]stageCap{
	stageNew:         {community: 10, house: 0, weight: 0.25},
	stageProving:     {community: 50, house: 5, weight: 0.5},
	stageEstablished: {community: -1, house: 25, weight: 1.0},
}

// towerStage reads a Tower's current stage from its record. An unreadable record is "new":
// a stage is earned, never assumed.
func (b *broker) towerStage(towerID string) trustStage {
	ev, ok, err := b.tower.registry.FindEvent(towerID, []string{evTrustStage}, false)
	if err != nil || !ok {
		return stageNew
	}
	if _, known := stageCaps[trustStage(ev.To)]; known {
		return trustStage(ev.To)
	}
	return stageNew
}

