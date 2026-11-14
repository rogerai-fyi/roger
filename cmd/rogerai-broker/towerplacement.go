package main

// towerplacement.go: ranked placement of a self-attaching Station
// (features/tower/placement_ranking.feature). The broker gathers each live Tower's facts -
// house or community, trust-stage caps, health tier, locality class, load - and
// towercore/placement chooses.

import (
	"crypto/rand"
	"encoding/binary"
	"log"
	"sort"
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

// isHouseAccount reports whether an account key is on the house list Core keeps for the
// per-owner cap. Station owners are account keys, so this is a map read.
func (b *broker) isHouseAccount(key string) bool {
	return key != "" && b.stationLimitExempt[strings.ToLower(key)]
}

// houseOwnerTTL is how long a Tower owner's house-ness is reused: a Tower owner is a login,
// which takes a store read to resolve to an account key.
const houseOwnerTTL = time.Minute

type houseOwnerEntry struct {
	house bool
	at    time.Time
}

// isHouseOwner reports whether an owner - an account key, or a Tower operator's login - is on
// the house list. A login's answer is cached for houseOwnerTTL.
func (b *broker) isHouseOwner(owner string) bool {
	if owner == "" || len(b.stationLimitExempt) == 0 {
		return false
	}
	if b.isHouseAccount(owner) {
		return true
	}
	now := time.Now()
	b.houseMu.Lock()
	if e, ok := b.houseOwners[owner]; ok && now.Sub(e.at) < houseOwnerTTL {
		b.houseMu.Unlock()
		return e.house
	}
	b.houseMu.Unlock()
	o, found, err := b.db.OwnerByLogin(owner)
	if err != nil {
		return false // not cached: an unreadable store is asked again next time
	}
	house := found && b.isHouseAccount(b.accountKeyOf(o))
	b.houseMu.Lock()
	if b.houseOwners == nil {
		b.houseOwners = map[string]houseOwnerEntry{}
	}
	b.houseOwners[owner] = houseOwnerEntry{house: house, at: now}
	b.houseMu.Unlock()
	return house
}

// towerTier is a Tower's health tier: B when its last two health checks both failed or its
// reputation verdict over the window is not clean, A otherwise. fleet is the fleet tally over
// the window, read once by the caller.
func (b *broker) towerTier(towerID string, now time.Time, fleet *reputation.Tally) placement.Tier {
	ts := b.tower
	if sum, err := ts.health.Summary(towerID, now); err == nil && sum.BothLastFailed() {
		return placement.TierB
	}
	if ts.outcomes != nil && fleet != nil {
		if tower, err := ts.outcomes.Tally(towerID, now.Add(-reputationWindow)); err == nil &&
			ts.repPolicy.Evaluate(tower, fleet.Without(tower)) != reputation.Clean {
			return placement.TierB
		}
	}
	return placement.TierA
}

// fleetTally is the fleet's reputation tally over the window, or nil when it cannot be read.
func (b *broker) fleetTally(now time.Time) *reputation.Tally {
	if b.tower.outcomes == nil {
		return nil
	}
	f, err := b.tower.outcomes.FleetTally(now.Add(-reputationWindow))
	if err != nil {
		return nil
	}
	return &f
}

// placed is where a Station is going and why.
type placed struct {
	towerID string
	plane   link.RelayPlane
	detail  map[string]any
	cand    placement.Candidate
}

// towerFacts is one live Tower as placement sees it, before any one Station is considered.
type towerFacts struct {
	cand       placement.Candidate // Class is filled per Station
	plane      link.RelayPlane
	loc        placement.Locality
	owner      string
	stage      placement.Stage
	house      int // house Stations on this Tower
	comm       int // community Stations on this Tower
	ownerHouse int // house Stations on ALL of this owner's Towers, eligible or not
	full       bool
}

// placementSnapshot reads every live Tower's facts once: the candidates for any number of
// placement decisions in one pass. House Stations are counted per owner over every Tower the
// owner has, so a Tower that is full or not live still counts against the owner's failover.
func (b *broker) placementSnapshot() []towerFacts {
	ts := b.tower
	now := time.Now()
	towers, err := ts.link.LiveTowersChecked()
	if err != nil && b.towerLogPace.allow("placement-mirror") {
		log.Printf("placement degraded: mirror unavailable (%v); placing among this instance's own links", err)
	}
	live := map[string]bool{}
	for _, id := range towers {
		live[id] = true
	}
	capacity := towerStationCapacity()
	fleet := b.fleetTally(now)
	houseByOwner := map[string]int{}
	var facts []towerFacts
	for _, tw := range ts.registry.All() {
		atts, aerr := ts.stationStore.ByTower(tw.ID)
		if aerr != nil {
			continue // a Tower whose load cannot be read is not placed on blind
		}
		f := towerFacts{owner: tw.Owner}
		total := 0
		for _, at := range atts {
			if !at.Live() {
				continue
			}
			total++
			if b.isHouseAccount(at.Owner) {
				f.house++
			} else {
				f.comm++
			}
		}
		houseByOwner[tw.Owner] += f.house
		if !live[tw.ID] || !ts.registry.MayTakeWork(tw.ID) {
			continue
		}
		plane, has := ts.link.RelayPlane(tw.ID)
		if !has || plane.Endpoint == "" {
			continue
		}
		f.plane = plane
		f.full = total >= capacity
		f.cand = placement.Candidate{TowerID: tw.ID, House: b.isHouseOwner(tw.Owner),
			Load: float64(total) / float64(capacity), Tier: b.towerTier(tw.ID, now, fleet)}
		if !f.cand.House {
			f.stage = b.towerStage(tw.ID)
		}
		f.loc, _ = ts.link.Locality(tw.ID)
		facts = append(facts, f)
	}
	for i := range facts {
		facts[i].ownerHouse = houseByOwner[facts[i].owner]
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].cand.TowerID < facts[j].cand.TowerID })
	return facts
}

// placeStation chooses the Tower for a Station owned by owner, observed at loc. exclude names
// Towers that may not be chosen (the one a move is leaving, or a failed move's target).
func (b *broker) placeStation(owner string, loc placement.Locality, exclude map[string]bool) (placed, bool) {
	return b.placeStationPrefer(owner, loc, exclude, "")
}

// placeStationPrefer is placeStation with a preferred Tower - a move's intended target - taken
// whenever it is still an eligible candidate.
func (b *broker) placeStationPrefer(owner string, loc placement.Locality, exclude map[string]bool, prefer string) (placed, bool) {
	return b.chooseFrom(b.placementSnapshot(), owner, loc, exclude, prefer, placementRand())
}

// chooseFrom places one Station among a snapshot's Towers. A nil rng chooses deterministically.
func (b *broker) chooseFrom(snap []towerFacts, owner string, loc placement.Locality, exclude map[string]bool, prefer string, rng *mrand.Rand) (placed, bool) {
	houseStation := b.isHouseAccount(owner)
	var cands []placement.Candidate
	byID := map[string]towerFacts{}
	for _, f := range snap {
		if exclude[f.cand.TowerID] || f.full {
			continue
		}
		c := f.cand
		c.Class = placement.ClassOf(loc, f.loc)
		if !c.House {
			caps := stageCaps[f.stage]
			if !houseStation && caps.community >= 0 && f.comm >= caps.community {
				continue
			}
			// House failover room is counted per OWNER, so many Towers under one account do
			// not multiply it.
			c.HouseAllowed = caps.house > 0 && f.ownerHouse < caps.house
		}
		f.cand = c
		cands = append(cands, c)
		byID[c.TowerID] = f
	}
	choice, reason, ok := placement.Choose(cands, houseStation, rng)
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
// is judged against - from a snapshot, or from the store when the Tower is not in it.
func (b *broker) towerCandidate(snap []towerFacts, towerID string, loc placement.Locality) placement.Candidate {
	for _, f := range snap {
		if f.cand.TowerID == towerID {
			c := f.cand
			c.Class = placement.ClassOf(loc, f.loc)
			return c
		}
	}
	c := placement.Candidate{TowerID: towerID, Tier: b.towerTier(towerID, time.Now(), b.fleetTally(time.Now()))}
	if tw, ok := b.tower.registry.Get(towerID); ok {
		c.House = b.isHouseOwner(tw.Owner)
	}
	tloc, _ := b.tower.link.Locality(towerID)
	c.Class = placement.ClassOf(loc, tloc)
	return c
}
