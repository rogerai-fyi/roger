package inv

import (
	"encoding/json"
	"fmt"
	"time"

	"rogerai.fm/roger/v6/internal/towerobj"
)

// leafMembers is the closed schema for a Station offer.
//
// There is no station_key member, and that absence is load-bearing. The key comes from
// Core's attachment record; if the leaf supplied it, "signed by the Station" would collapse
// into "signed by whoever wrote this leaf", and every downstream guarantee with it.
//
// There is also nowhere to declare geography, hardware, or any other operator claim. The
// spec requires such claims never be labeled measured, and the cheapest way to keep an
// unverifiable claim from being presented as fact is to leave it no way in.
var leafMembers = []string{
	"network", "tower_id", "station_id", "offer_id", "model", "modality",
	"price_in", "price_out", "earn_in", "earn_out", "capacity", "capabilities",
	"expires", offerSigMbr,
}

type identity struct {
	stationID string
	offerID   string
}

// leafIdentity reads just the two IDs the inventory needs before it can decide anything
// else. Uniqueness is an inventory-level property: a Station or offer appearing twice makes
// the revision ambiguous, and there is no correct way to pick a winner, so it cannot be
// resolved by dropping one leaf.
func leafIdentity(lo map[string]any) (identity, error) {
	o := obj(lo)
	station, err := o.str("station_id")
	if err != nil {
		return identity{}, err
	}
	offer, err := o.str("offer_id")
	if err != nil {
		return identity{}, err
	}
	return identity{stationID: station, offerID: offer}, nil
}

// admitLeaf decides whether one leaf becomes routable. A non-empty reason means it does
// not, and the rest of the revision is unaffected.
//
// The ordering below is the rejection table's ordering, and it is deliberate: each check
// must be reachable on its own, or a row of the table is being satisfied by an earlier
// check and the control it names is never exercised.
func (s *Set) admitLeaf(channelTowerID string, lo map[string]any) (Leaf, string) {
	o := obj(lo)
	if err := o.closed(leafMembers...); err != nil {
		return Leaf{}, err.Error()
	}

	network, err := o.str("network")
	if err != nil {
		return Leaf{}, err.Error()
	}
	if network != s.cfg.Network {
		return Leaf{}, fmt.Sprintf("offer network %q is not %q", network, s.cfg.Network)
	}

	// An offer names the Tower it may be relayed through. Without this, a Tower could
	// re-use another Tower's signed leaves and inherit a fleet it never attached.
	towerID, err := o.str("tower_id")
	if err != nil {
		return Leaf{}, err.Error()
	}
	if towerID != channelTowerID {
		return Leaf{}, fmt.Sprintf("offer is bound to Tower %q, not %q", towerID, channelTowerID)
	}

	ident, err := leafIdentity(lo)
	if err != nil {
		return Leaf{}, err.Error()
	}

	// Core's own record, consulted before any signature: a signature is only meaningful
	// once we know which key it must be by.
	reg := s.policy.Station(ident.stationID)
	// Checked BEFORE Known: an unreadable central state is not an unregistered Station, and
	// saying so would send the operator to fix something that is not broken.
	if reg.Unavailable {
		return Leaf{}, "central state for this Station is temporarily unavailable"
	}
	if !reg.Known || len(reg.Key) == 0 {
		return Leaf{}, "Station ID is not consistent with any registered key"
	}

	canon, err := canonicalLeaf(lo)
	if err != nil {
		return Leaf{}, fmt.Sprintf("offer encoding: %v", err)
	}
	// Verified against the REGISTERED key. This one call answers three rows of the table:
	// a missing signature, a signature by another key, and capabilities (or anything else)
	// that the Tower altered after the Station signed.
	if err := towerobj.Verify(reg.Key, s.cfg.Network, TypeOffer, Version, canon, offerSigMbr); err != nil {
		return Leaf{}, fmt.Sprintf("Station signature: %v", err)
	}

	// Central state overrides a cryptographically perfect offer. The Tower signing the
	// collection does not make any of these claims true, which is the whole point of
	// checking them here rather than trusting the relay.
	switch {
	case reg.KeyRevoked:
		return Leaf{}, "the Station's key is revoked"
	case reg.Banned:
		return Leaf{}, "the Station is banned"
	case !reg.OwnerPresent:
		return Leaf{}, "the Station has no owner, which public admission requires"
	case reg.OwnerSuspended:
		return Leaf{}, "the Station's owner is suspended"
	case reg.Quarantined:
		// Not a fault, and worth saying so plainly: the operator has done everything right
		// and is waiting on evidence Core has to gather itself.
		return Leaf{}, "the Station is in quarantine and not yet eligible for public work"
	}

	// One Station has one active origin. A Station already being relayed by a live Tower
	// cannot also appear behind this one: two origins for one machine means its capacity is
	// counted twice and dispatched to concurrently. Moving a Station between Towers is the
	// fenced rehome flow, not something an inventory push may do unilaterally.
	if holder, held := s.origins[ident.stationID]; held && holder != channelTowerID {
		if st, live := s.towers[holder]; live && s.cfg.Now().Before(st.expires) {
			return Leaf{}, fmt.Sprintf("Station %s is already active behind Tower %s", ident.stationID, holder)
		}
	}

	model, err := o.str("model")
	if err != nil {
		return Leaf{}, err.Error()
	}
	if !s.policy.ModelAllowed(model) {
		return Leaf{}, fmt.Sprintf("model %q is not supported on the public network", model)
	}
	modality, err := o.str("modality")
	if err != nil {
		return Leaf{}, err.Error()
	}
	if !s.policy.ModalityAllowed(modality) {
		return Leaf{}, fmt.Sprintf("modality %q is not supported", modality)
	}

	// Prices are bounded base-10 integer strings, so "non-finite" cannot even be written -
	// a float or an infinity fails to parse. Negative is expressible, and refused.
	rates := map[string]int64{}
	for _, name := range []string{"price_in", "price_out", "earn_in", "earn_out"} {
		v, err := o.integer(name)
		if err != nil {
			return Leaf{}, err.Error()
		}
		if v < 0 {
			return Leaf{}, fmt.Sprintf("%s is negative", name)
		}
		rates[name] = v
	}

	floor, ceiling, ok := s.policy.PriceBand(model)
	if !ok {
		return Leaf{}, fmt.Sprintf("model %q has no public price band", model)
	}
	for _, name := range []string{"price_in", "price_out"} {
		if rates[name] < floor {
			return Leaf{}, fmt.Sprintf("%s is below the public floor", name)
		}
		if rates[name] > ceiling {
			return Leaf{}, fmt.Sprintf("%s is above the public ceiling", name)
		}
	}
	// A Station earning more than the consumer pays is money out of Core's pocket on every
	// token, and it is the arithmetic an operator is most likely to try.
	if rates["earn_in"] > rates["price_in"] || rates["earn_out"] > rates["price_out"] {
		return Leaf{}, "the Station-earning rate is above the matching consumer rate"
	}

	capacity, err := o.integer("capacity")
	if err != nil {
		return Leaf{}, err.Error()
	}
	if capacity <= 0 {
		return Leaf{}, "capacity is not positive"
	}

	// Required, not optional: an absent capabilities member is how a Tower would strip the
	// field from the signed bytes and then assert capabilities out of band.
	caps, err := o.strings("capabilities")
	if err != nil {
		return Leaf{}, err.Error()
	}

	expiresUnix, err := o.integer("expires")
	if err != nil {
		return Leaf{}, err.Error()
	}
	expires := time.Unix(expiresUnix, 0)
	if !s.cfg.Now().Before(expires) {
		return Leaf{}, "the offer has expired"
	}

	hash, err := towerobj.Hash(canon)
	if err != nil {
		return Leaf{}, fmt.Sprintf("offer hash: %v", err)
	}

	return Leaf{
		TowerID:      towerID,
		StationID:    ident.stationID,
		OfferID:      ident.offerID,
		Model:        model,
		Modality:     modality,
		PriceIn:      rates["price_in"],
		PriceOut:     rates["price_out"],
		EarnIn:       rates["earn_in"],
		EarnOut:      rates["earn_out"],
		Capacity:     capacity,
		Capabilities: caps,
		Expires:      expires,
		Offer:        canon,
		OfferHash:    hash,
	}, ""
}

// canonicalLeaf renders the nested leaf back to the bytes the Station signed. The enclosing
// inventory was required to be canonical, so re-emitting a member of it canonically
// reproduces exactly what was there - this is a re-render, not a normalisation.
func canonicalLeaf(lo map[string]any) ([]byte, error) {
	b, err := json.Marshal(lo)
	if err != nil {
		return nil, err
	}
	return towerobj.Canonical(b)
}
