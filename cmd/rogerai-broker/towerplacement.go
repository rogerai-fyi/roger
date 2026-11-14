package main

// towerplacement.go: ranked placement of a self-attaching Station
// (features/tower/placement_ranking.feature). The broker gathers each live Tower's facts -
// house or community, trust-stage caps, health tier, locality class, load - and
// towercore/placement chooses.

import (
	"crypto/rand"
	"encoding/binary"
	"log"
	mrand "math/rand"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/placement"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
)

const evStationPlaced = "station_placed"

// hubConnCap is the hub's concurrent-connection cap (internal/towercli maxHubConns) and
// stationHubWorkers the worker connections one Station holds: a node runs its default of 4
// unless told otherwise, and Core is not told, so the default is what capacity is sized on.
const (
	hubConnCap        = 512
	stationHubWorkers = 4
)

// towerStationCapacity is how many live Stations one Tower may hold (a seam for tests).
var towerStationCapacity = func() int { return hubConnCap / stationHubWorkers }

// isHouseOwner reports whether an owner - an account key, or a Tower operator's login - is on
// the house list Core keeps for the per-owner cap.
func (b *broker) isHouseOwner(owner string) bool {
	if owner == "" || len(b.stationLimitExempt) == 0 {
		return false
	}
	if b.stationLimitExempt[strings.ToLower(owner)] {
		return true
	}
	o, found, err := b.db.OwnerByLogin(owner)
	return err == nil && found && b.stationLimitExempt[strings.ToLower(b.accountKeyOf(o))]
}

// towerTier is a Tower's health tier: B when its last two health checks both failed or its
// reputation verdict over the window is not clean, A otherwise.
func (b *broker) towerTier(towerID string, now time.Time) placement.Tier {
	ts := b.tower
	if sum, err := ts.health.Summary(towerID, now); err == nil && sum.BothLastFailed() {
		return placement.TierB
	}
	if ts.outcomes != nil {
		since := now.Add(-reputationWindow)
		tower, terr := ts.outcomes.Tally(towerID, since)
		fleetT, ferr := ts.outcomes.FleetTally(since)
		if terr == nil && ferr == nil && ts.repPolicy.Evaluate(tower, fleetT.Without(tower)) != reputation.Clean {
			return placement.TierB
		}
	}
	return placement.TierA
}

// placed is where a Station is going and why.
type placed struct {
	towerID string
	plane   link.RelayPlane
	detail  map[string]any
	cand    placement.Candidate
}

type towerFacts struct {
	cand              placement.Candidate
	plane             link.RelayPlane
	owner             string
	stage             placement.Stage
	live, house, comm int
}

// placeStation chooses the Tower for a Station owned by owner, observed at loc. exclude names
// Towers that may not be chosen (the one a move is leaving, or a failed move's target).
func (b *broker) placeStation(owner string, loc placement.Locality, exclude map[string]bool) (placed, bool) {
	return b.placeStationPrefer(owner, loc, exclude, "")
}

// placeStationPrefer is placeStation with a preferred Tower - a move's intended target - taken
// whenever it is still an eligible candidate.
func (b *broker) placeStationPrefer(owner string, loc placement.Locality, exclude map[string]bool, prefer string) (placed, bool) {
	ts := b.tower
	now := time.Now()
	towers, err := ts.link.LiveTowersChecked()
	if err != nil && b.towerLogPace.allow("placement-mirror") {
		log.Printf("placement degraded: mirror unavailable (%v); placing among this instance's own links", err)
	}
	houseStation := b.isHouseOwner(owner)
	capacity := towerStationCapacity()

	var facts []towerFacts
	houseByOwner := map[string]int{}
	for _, id := range towers {
		if exclude[id] || !ts.registry.MayTakeWork(id) {
			continue
		}
		plane, has := ts.link.RelayPlane(id)
		if !has || plane.Endpoint == "" {
			continue
		}
		tw, ok := ts.registry.Get(id)
		if !ok {
			continue
		}
		f := towerFacts{plane: plane, owner: tw.Owner}
		f.cand = placement.Candidate{TowerID: id, House: b.isHouseOwner(tw.Owner)}
		atts, aerr := ts.stationStore.ByTower(id)
		if aerr != nil {
			continue // a Tower whose load cannot be read is not placed on blind
		}
		for _, at := range atts {
			if !at.Live() {
				continue
			}
			f.live++
			if b.isHouseOwner(at.Owner) {
				f.house++
			} else {
				f.comm++
			}
		}
		if f.live >= capacity {
			continue
		}
		if !f.cand.House {
			f.stage = b.towerStage(id)
			houseByOwner[tw.Owner] += f.house
		}
		f.cand.Load = float64(f.live) / float64(capacity)
		f.cand.Tier = b.towerTier(id, now)
		tloc, _ := ts.link.Locality(id)
		f.cand.Class = placement.ClassOf(loc, tloc)
		facts = append(facts, f)
	}

	var cands []placement.Candidate
	byID := map[string]towerFacts{}
	for _, f := range facts {
		if !f.cand.House {
			caps := stageCaps[f.stage]
			if !houseStation && caps.community >= 0 && f.comm >= caps.community {
				continue
			}
			// House failover room is counted per OWNER, so many Towers under one account do
			// not multiply it.
			f.cand.HouseAllowed = caps.house > 0 && houseByOwner[f.owner] < caps.house
		}
		cands = append(cands, f.cand)
		byID[f.cand.TowerID] = f
	}
	choice, reason, ok := placement.Choose(cands, houseStation, placementRand())
	if pf, has := byID[prefer]; has && (!houseStation || pf.cand.House || pf.cand.HouseAllowed) {
		choice, reason, ok = pf.cand, "move", true
	}
	if !ok {
		return placed{}, false
	}
	f := byID[choice.TowerID]
	group := "community"
	if houseStation {
		group = "house"
	}
	tier := "A"
	if choice.Tier == placement.TierB {
		tier = "B"
	}
	return placed{towerID: choice.TowerID, plane: f.plane, cand: choice, detail: map[string]any{
		"class": choice.Class.String(), "tier": tier, "group": group, "reason": string(reason),
		"candidates": len(cands),
	}}, true
}

// placementRand is a per-placement generator seeded from the system's randomness, never from
// anything a node sends.
func placementRand() *mrand.Rand {
	var seed [8]byte
	_, _ = rand.Read(seed[:])
	return mrand.New(mrand.NewSource(int64(binary.LittleEndian.Uint64(seed[:]))))
}

// towerCandidate is how placement sees one Tower for a Station at loc - the comparison a move
// is judged against.
func (b *broker) towerCandidate(towerID string, loc placement.Locality) placement.Candidate {
	c := placement.Candidate{TowerID: towerID, Tier: b.towerTier(towerID, time.Now())}
	if tw, ok := b.tower.registry.Get(towerID); ok {
		c.House = b.isHouseOwner(tw.Owner)
	}
	tloc, _ := b.tower.link.Locality(towerID)
	c.Class = placement.ClassOf(loc, tloc)
	return c
}
