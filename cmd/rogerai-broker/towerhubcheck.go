package main

// towerhubcheck.go: Core's Station-free hub check (features/tower/tower_trust_ramp.feature,
// "the hub check"). Each live Tower with a hub is checked by the one instance holding its
// link, at most once per canary interval, and every result lands in the shared health store
// the trust ramp counts and placement reads. Station canaries land there too.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/placement"
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
		pulls, class := ts.link.Pulls(tw.ID), ts.link.HubClass(tw.ID)
		if !pulls && class != placement.HubPublic {
			// Core never dials a private (or not yet classed) hub, and a Tower that cannot pull
			// is placed nowhere, so there is nothing to check and nothing to fail
			// (private_hub_placement.feature).
			continue
		}
		b.hubCheckMu.Lock()
		last := b.hubCheckAt[tw.ID]
		// Never more often than the interval (tower_trust_ramp.feature). A tick that lands a
		// moment early skips one sweep; health.RunStaleAfter leaves an interval of slack for that.
		due := last.IsZero() || now.Sub(last) >= canaryInterval
		if due {
			if b.hubCheckAt == nil {
				b.hubCheckAt = map[string]time.Time{}
			}
			b.hubCheckAt[tw.ID] = now
		}
		b.hubCheckMu.Unlock()
		if due {
			b.hubCheck(tw, plane, pulls, class, now)
		}
	}
}

func (b *broker) hubCheck(tw admit.Tower, plane link.RelayPlane, pulls bool, class placement.HubClass, now time.Time) {
	start := time.Now()
	reason, remote, rtt := "", "", time.Duration(0)
	source := health.SourceHubCheck
	if pulls && b.shared != nil && !b.shared.healthy() {
		return // Core's own store is down: a pull check now would blame the Tower for it
	}
	if pulls {
		// Delivered by pull, like any other work for this Tower (tower_pull_dispatch.feature).
		base, hc := b.pullClient(tw.ID)
		rec := &errRecorder{rt: hc.Transport}
		hc.Transport = rec
		ctx, cancel := context.WithTimeout(context.Background(), hubCheckTimeout)
		rtt, _, reason = towerhub.CheckHub(ctx, hc, base, tw.ID, tw.KeyHash)
		cancel()
		if errors.Is(rec.err, errPullUnavailable) {
			return // Core's own store failed the check: nothing is known about the Tower
		}
		if class != placement.HubPublic {
			source = health.SourcePull
		}
	}
	if class == placement.HubPublic && reason == "" {
		// A public hub is also dialed from outside, because remote Stations depend on reaching
		// it: a failure there counts even when the pull check passed.
		var dialRTT time.Duration
		reason, remote, dialRTT = "unreachable", "", 0
		base, hc, err := towerhub.ReachVetted(plane.Endpoint, plane.TLSSPKI, b.canaryVet)
		if err == nil {
			hc.Timeout = hubCheckTimeout
			ctx, cancel := context.WithTimeout(context.Background(), hubCheckTimeout)
			dialRTT, remote, reason = towerhub.CheckHub(ctx, hc, base, tw.ID, tw.KeyHash)
			cancel()
			if tr, ok := hc.Transport.(interface{ CloseIdleConnections() }); ok {
				tr.CloseIdleConnections()
			}
		}
		if !pulls {
			rtt = dialRTT
		}
	}
	// Stamped when the check FINISHED (the pass's time plus how long this check took), so a
	// slow check never lands as older than a canary that finished before it.
	done := now.Add(time.Since(start))
	b.recordHealthCheck(tw.ID, health.Check{OK: reason == "", At: done, Source: source, RTT: rtt,
		Slot: b.slotFor(now), MinGap: healthMinGap})
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
			Source: health.SourceCanary, Slot: slot, MinGap: healthMinGap})
	}
}

// slotFor is the current sweep's slot, or now's when no sweep is running.
func (b *broker) slotFor(now time.Time) int64 {
	if s := b.sweepSlot.Load(); s != 0 {
		return s
	}
	return healthSlot(now)
}

// healthMinGap is the least time between two counted failures of one kind of check: half a
// canary interval, so two broker instances recording one blip moments apart count it once while
// the next real interval still counts.
const healthMinGap = canaryInterval / 2

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

// errRecorder keeps the last transport error, which towerhub.CheckHub folds into "unreachable".
type errRecorder struct {
	rt  http.RoundTripper
	err error
}

func (e *errRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := e.rt.RoundTrip(r)
	e.err = err
	return resp, err
}
