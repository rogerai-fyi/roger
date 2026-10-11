package towercli

// owner.go is what this user's TUI and console do to a Tower they serve
// (features/join/surfaces.feature, approve.feature): mint a one-paste invite and see whether it
// was used, list and answer join requests, show the Tower's fingerprint. Each reaches the running
// serve through its owner-only admin socket, exactly as the CLI's administration commands do.

import (
	"time"

	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
)

// OwnerInviteTTL is how long an invite minted on an owner's screen stays valid (the CLI default).
const OwnerInviteTTL = 15 * time.Minute

// OwnerInvite is an invite as the owner's screens show it: what it is, the one next step, the
// line to run on the other machine, and when it stops working.
type OwnerInvite struct {
	ID        string `json:"id"`
	Heading   string `json:"heading"`
	Next      string `json:"next"`
	Line      string `json:"line"`
	ExpiresAt int64  `json:"expires_at"` // unix nanoseconds
}

// MintOwnerInvite mints a one-paste client invite on the Tower serving dir at servingAddr.
func MintOwnerInvite(dir, servingAddr string) (OwnerInvite, error) {
	addrs, _, err := localplane.InviteAddrs(servingAddr, inviteInterfaceAddrs)
	if err != nil {
		return OwnerInvite{}, err
	}
	minted, inv, err := mintJoin(toweradmin.Client{Dir: dir}, tower.JoinClient, joininvite.TowerClient, "", nil, OwnerInviteTTL, addrs)
	if err != nil {
		return OwnerInvite{}, err
	}
	return OwnerInvite{
		ID:        minted.ID,
		Heading:   tower.InviteHeading(tower.JoinClient, OwnerInviteTTL),
		Next:      tower.InviteNext(tower.JoinClient),
		Line:      joininvite.JoinLine(inv.String()),
		ExpiresAt: minted.ExpiresAt,
	}, nil
}

// OwnerInviteOutcome is whether the invite id was used: who joined and how to undo it, or that it
// ran out unused (over), or not over yet.
func OwnerInviteOutcome(dir, id string) (line string, over bool) { return inviteOutcome(dir, id) }

// OwnerFingerprint is the Tower's identity fingerprint, the one its invites carry.
func OwnerFingerprint(dir string) (string, error) {
	return toweradmin.Client{Dir: dir}.JoinFingerprint()
}

// OwnerRequests lists the join requests waiting for the owner.
func OwnerRequests(dir string) ([]tower.PairRequest, error) {
	return toweradmin.Client{Dir: dir}.PairRequests()
}

// OwnerDecide approves or refuses a waiting join request.
func OwnerDecide(dir, id string, approve bool) error {
	c := toweradmin.Client{Dir: dir}
	if approve {
		return c.ApprovePair(id)
	}
	return c.RefusePair(id)
}
