package main

// towerhubcheck.go: Core's Station-free hub check (features/tower/tower_trust_ramp.feature,
// "the hub check"). Each live Tower with a hub is checked by the one instance holding its
// link, at most once per canary interval, and every result lands in the shared health store
// the trust ramp counts and placement reads. Station canaries land there too.

import (
	"context"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// healthRetention is how long health-check evidence is kept: three times the longest window
// the trust ramp counts over.
const healthRetention = 90 * 24 * time.Hour

// hubCheckTimeout bounds one check: a hub that does not answer within it is unreachable.
const hubCheckTimeout = 5 * time.Second

// towerHubCheckOnce checks every Tower this instance holds whose last check is at least a
// canary interval old.
func (b *broker) towerHubCheckOnce(now time.Time) {
	ts := b.tower
	if ts == nil {
		return
	}
	for _, tw := range ts.registry.All() {
		if !ts.link.HoldsLocally(tw.ID) {
			continue
		}
		plane, has := ts.link.RelayPlane(tw.ID)
		if !has || plane.Endpoint == "" {
			// No hub, no check - and no Stations either: placement takes only Towers that
			// advertise a hub, so a Tower without one has nothing a health tier would protect.
			continue
		}
		b.hubCheckMu.Lock()
		last := b.hubCheckAt[tw.ID]
		due := last.IsZero() || now.Sub(last) >= canaryInterval
		if due {
			if b.hubCheckAt == nil {
				b.hubCheckAt = map[string]time.Time{}
			}
			b.hubCheckAt[tw.ID] = now
		}
		b.hubCheckMu.Unlock()
		if due {
			b.hubCheck(tw, plane.Endpoint, plane.TLSSPKI, now)
		}
	}
}

func (b *broker) hubCheck(tw admit.Tower, endpoint, pin string, now time.Time) {
	start := time.Now()
	reason, remote, rtt := "unreachable", "", time.Duration(0)
	base, hc, err := towerhub.ReachVetted(endpoint, pin, b.canaryVet)
	if err == nil {
		hc.Timeout = hubCheckTimeout
		ctx, cancel := context.WithTimeout(context.Background(), hubCheckTimeout)
		rtt, remote, reason = towerhub.CheckHub(ctx, hc, base, tw.ID, tw.KeyHash)
		cancel()
		if tr, ok := hc.Transport.(interface{ CloseIdleConnections() }); ok {
			tr.CloseIdleConnections()
		}
	}
	// Stamped when the check FINISHED (the pass's time plus how long this check took), so a
	// slow check never lands as older than a canary that finished before it.
	done := now.Add(time.Since(start))
	b.recordHealthCheck(tw.ID, health.Check{OK: reason == "", At: done, Source: health.SourceHubCheck, RTT: rtt,
		Slot: b.slotFor(now)})
	if reason == "" && remote != "" {
		// Where the hub is, from the address that just proved the Tower's identity - not from
		// what a name resolved to once, which a Tower controls (placement_locality.feature).
		b.relocateFromProof(tw.ID, remote)
	}
	if reason != "" {
		b.towerEvent(admit.Event{TowerID: tw.ID, Kind: evHubCheckFailed, Actor: admit.ActorSystem, Reason: reason})
	}
}

// recordTowerHealth files a Station canary's verdict as a health check of the Tower that
// carried it. A Station's own fault, or an aborted probe, says nothing about the Tower.
//
// It counts toward the fail streak like a hub check, at most once per canary interval with it
// (health.Check.Slot), so a hub check and a canary failing in the same sweep are one outage.
func (b *broker) recordTowerHealth(towerID string, outcome reputation.Outcome) {
	switch outcome {
	case reputation.CanaryPass, reputation.CanaryFail:
		now := time.Now()
		slot := b.sweepSlot.Load() // the sweep this canary belongs to
		if slot == 0 {
			slot = healthSlot(now)
		}
		b.recordHealthCheck(towerID, health.Check{OK: outcome == reputation.CanaryPass, At: now,
			Source: health.SourceCanary, Slot: slot})
	}
}

// slotFor is the current sweep's slot, or now's when no sweep is running.
func (b *broker) slotFor(now time.Time) int64 {
	if s := b.sweepSlot.Load(); s != 0 {
		return s
	}
	return healthSlot(now)
}

// healthSlot is the canary interval a check belongs to: the fail streak counts one failure per
// slot, whatever its source.
func healthSlot(t time.Time) int64 { return t.UnixNano() / int64(canaryInterval) }

// recordHealthCheck files one health check with its source and round trip.
func (b *broker) recordHealthCheck(towerID string, c health.Check) {
	if err := b.tower.health.RecordCheck(towerID, c); err != nil {
		b.towerGovUnavailable(err)
		return
	}
	if !c.OK {
		b.noteHealthStreak(towerID)
	}
}
