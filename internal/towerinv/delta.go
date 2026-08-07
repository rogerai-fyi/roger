package towerinv

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"rogerai.fm/roger/v5/internal/towerobj"
)

// Deltas exist for one reason: a Tower with a stable fleet should send nothing, and a Tower
// that changes one Station should send one Station - not a five-megabyte snapshot. That
// saving is only safe if we can always tell whether our view and theirs still agree.
//
// So the rule for a delta is stricter than the rule for a snapshot, not looser: ANY
// ambiguity about where this delta sits in the sequence costs a full resync. Never a guess,
// never a partial application. A resync is cheap and always correct; a delta applied to the
// wrong base is a silent divergence that nobody notices until a grant names a Station that
// is not there.
//
// The one thing a resync does NOT forgive is a wrong network or a wrong Tower - see
// errIdentity. Answering an attack by asking the attacker for more data is not a recovery.

const (
	opAdd     = "add"
	opReplace = "replace"
	opRemove  = "remove"
)

// AcceptDelta applies a signed hash-chained amendment to the accepted revision.
func (s *Set) AcceptDelta(channelTowerID string, towerKey ed25519.PublicKey, raw []byte) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.openSigned(channelTowerID, towerKey, TypeDelta, raw,
		"network", "tower_id", "base_revision", "revision", "prev_hash",
		"issued", "expires", "ops", sigMember)
	if err != nil {
		// A body we cannot parse, a schema we do not recognise, or a signature that does not
		// verify all leave us unable to place this delta - which is a resync, not a
		// rejection. Identity faults are the exception and stay rejections.
		if errors.Is(err, errIdentity) {
			return Result{}, err
		}
		return Result{}, resync(err)
	}

	prior, ok := s.towers[channelTowerID]
	if !ok {
		return Result{}, resync(errors.New("there is no accepted revision to amend"))
	}

	base, err := d.revisionNumber("base_revision")
	if err != nil {
		return Result{}, resync(err)
	}
	revision, err := d.revisionNumber("revision")
	if err != nil {
		return Result{}, resync(err)
	}
	if base != prior.revision {
		return Result{}, resync(fmt.Errorf("delta is based on revision %d, not the accepted %d", base, prior.revision))
	}
	if revision != base+1 {
		return Result{}, resync(fmt.Errorf("delta targets revision %d, not %d", revision, base+1))
	}
	prev, err := d.str("prev_hash")
	if err != nil {
		return Result{}, resync(err)
	}
	if prev != prior.hash {
		return Result{}, resync(errors.New("prev_hash is not the accepted head"))
	}

	expires, err := s.window(d)
	if err != nil {
		return Result{}, err
	}

	ops, err := d.list("ops")
	if err != nil {
		return Result{}, resync(err)
	}

	// Built on a copy, installed only at the end. Unchanged leaves keep their prior signed
	// offer and origin by construction, which is what the spec requires - we never re-derive
	// a leaf we were not told changed.
	next := prior.clone()
	touched := map[string]bool{}
	var excluded []Exclusion

	for i, ro := range ops {
		op, ok := ro.(map[string]any)
		if !ok {
			return Result{}, resync(fmt.Errorf("operation %d is not an object", i))
		}
		kind, err := obj(op).str("op")
		if err != nil {
			return Result{}, resync(err)
		}

		switch kind {
		case opRemove:
			if err := obj(op).closed("op", "station_id", "offer_id"); err != nil {
				return Result{}, resync(err)
			}
			ident, err := leafIdentity(op)
			if err != nil {
				return Result{}, resync(err)
			}
			if err := claim(touched, ident.offerID); err != nil {
				return Result{}, resync(err)
			}
			have, present := next.byOffer[ident.offerID]
			// Removing something we do not have means our views already differ. Treating it
			// as a no-op would paper over exactly the divergence deltas are risky for.
			if !present {
				return Result{}, resync(fmt.Errorf("removal names offer %s, which is not in the accepted revision", ident.offerID))
			}
			if have.StationID != ident.stationID {
				return Result{}, resync(fmt.Errorf("removal names Station %s for offer %s, which belongs to %s", ident.stationID, ident.offerID, have.StationID))
			}
			delete(next.byOffer, ident.offerID)

		case opAdd, opReplace:
			if err := obj(op).closed("op", "leaf"); err != nil {
				return Result{}, resync(err)
			}
			lv, ok := op["leaf"].(map[string]any)
			if !ok {
				return Result{}, resync(fmt.Errorf("operation %d has no leaf object", i))
			}
			ident, err := leafIdentity(lv)
			if err != nil {
				return Result{}, resync(err)
			}
			if err := claim(touched, ident.offerID); err != nil {
				return Result{}, resync(err)
			}
			_, present := next.byOffer[ident.offerID]
			// add-of-existing and replace-of-absent are both "our views differ about what is
			// already there", and neither has a safe interpretation.
			if kind == opAdd && present {
				return Result{}, resync(fmt.Errorf("add names offer %s, which is already accepted", ident.offerID))
			}
			if kind == opReplace && !present {
				return Result{}, resync(fmt.Errorf("replace names offer %s, which is not accepted", ident.offerID))
			}

			leaf, why := s.admitLeaf(channelTowerID, lv)
			if why != "" {
				// A replacement that is not admissible still retires the offer it replaced:
				// the operator has said that offer is gone, and keeping the old one alive
				// would route work at a price they have withdrawn.
				delete(next.byOffer, ident.offerID)
				excluded = append(excluded, Exclusion{StationID: ident.stationID, OfferID: ident.offerID, Reason: why})
				continue
			}
			next.byOffer[leaf.OfferID] = leaf

		default:
			// A shape we understand naming an operation we do not implement is a version
			// mismatch, not a lost place in the sequence. Resending it would not help.
			return Result{}, reject(fmt.Errorf("unknown operation %q", kind))
		}
	}

	// The ceilings apply to the RESULT, not to the amendment - otherwise a Tower could grow
	// past any limit one small delta at a time.
	if len(next.byOffer) > s.cfg.MaxLeaves {
		return Result{}, reject(fmt.Errorf("%d leaves is above the ceiling of %d", len(next.byOffer), s.cfg.MaxLeaves))
	}
	stations := map[string]bool{}
	caps := map[string]bool{}
	for _, l := range next.byOffer {
		if stations[l.StationID] {
			return Result{}, reject(fmt.Errorf("Station %s would appear twice", l.StationID))
		}
		stations[l.StationID] = true
		for _, c := range l.Capabilities {
			caps[c] = true
		}
	}
	if len(caps) > s.cfg.MaxCapabilities {
		return Result{}, reject(fmt.Errorf("%d distinct capabilities is above the limit of %d", len(caps), s.cfg.MaxCapabilities))
	}

	hash, err := towerobj.Hash(raw)
	if err != nil {
		return Result{}, resync(err)
	}
	next.revision, next.hash, next.expires = revision, hash, expires
	s.install(channelTowerID, next)

	return Result{Revision: revision, Hash: hash, Routable: len(next.byOffer), Excluded: excluded}, nil
}

// claim records that an operation touched a leaf, and refuses a second one. Two operations
// on one leaf have no defined order, so the result depends on which the peer applied first.
func claim(touched map[string]bool, offerID string) error {
	if touched[offerID] {
		return fmt.Errorf("more than one operation touches offer %s", offerID)
	}
	touched[offerID] = true
	return nil
}
