package main

// edgepair.go is an Edge authority approving a device the same way a Tower does
// (features/join/approve.feature): `roger join <authority address>` asks, the owner's screen shows
// the request with the matching phrase over the Edge's root, and y lets the device's key enroll.

import (
	"errors"
	"log"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/tower"
)

// edgeOwnerDir names the Edge authority among the places a join request waits; a Tower's is its
// data directory, an absolute path, so the two never collide.
const edgeOwnerDir = "edge:authority"

// edgePairing is the authority's request-and-approve: the phrase covers its root, and approving
// puts the device's key on the allow-list so its enrollment is issued.
func edgePairing(local *edgeauth.Local) *localplane.Server {
	root := edgeauth.RootFingerprint(local.Authority().Root())
	return localplane.NewPairing(edgeWhere(local.Descriptor()), func() (string, error) { return root, nil },
		func(r tower.PairRequest, _, pubHex string) (localplane.JoinAnswer, error) {
			if err := local.Allow(pubHex); err != nil {
				return localplane.JoinAnswer{}, err
			}
			log.Printf("edge: %s may enroll (approved on this screen)", r.Name)
			return localplane.JoinAnswer{Joined: "edge", Tower: edgeWhere(local.Descriptor())}, nil
		})
}

// edgeWhere is what the authority is called on the owner's screen and in a device's answer.
func edgeWhere(d edgeauth.Descriptor) string {
	if d.Where != "" {
		return d.Where
	}
	return "this Edge"
}

// ownerJoin adds this machine's Edge authority, while it serves, to where the owner answers join
// requests; everything else is o's.
func (h *edgeHost) ownerJoin(o *ownerjoin.Hooks) *ownerjoin.Hooks {
	w := *o
	w.Edge = func() (ownerjoin.Tower, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.pair == nil || h.authLn == nil {
			return ownerjoin.Tower{}, false
		}
		return ownerjoin.Tower{Dir: edgeOwnerDir, Label: edgeWhere(h.pairDesc), Addr: h.authLn.Addr().String(),
			FP: edgeauth.ShortFingerprint(h.pairDesc.Fingerprint)}, true
	}
	w.Requests = func(t ownerjoin.Tower) ([]tower.PairRequest, error) {
		if t.Dir != edgeOwnerDir {
			return o.Requests(t)
		}
		if pair := h.pairing(); pair != nil {
			return pair.PairRequests(), nil
		}
		return nil, errEdgeNotServing
	}
	w.Decide = func(dir, id string, approve bool) error {
		if dir != edgeOwnerDir {
			return o.Decide(dir, id, approve)
		}
		pair := h.pairing()
		switch {
		case pair == nil:
			return errEdgeNotServing
		case approve:
			return pair.ApprovePair(id)
		}
		return pair.RefusePair(id)
	}
	return &w
}

var errEdgeNotServing = errors.New("this machine's Edge authority is not serving")

func (h *edgeHost) pairing() *localplane.Server {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pair
}
