package towerjoin

// station.go attaches a Station to the public network.
//
// THESE ROUTES HAD NO CLIENT. /tower/station/invite and /tower/station/attach were built and
// exercised from the server's side only; nothing in any binary called them, so an operator
// following the documentation could not attach a Station at all. That made every joined
// Tower inert: attachment is what records the key each offer is verified against, and Core
// refuses a leaf whose Station it has no record of. An empty attach registry is an empty
// network, however healthy every other part looks.
//
// TWO CALLS, TWO DIFFERENT SIGNERS, and the split is the authorization model rather than an
// accident of who happens to be running which command:
//
//	invite  signed by the OPERATOR's account key. Authorizing a machine to serve under your
//	        account is an account decision, and the account is what a ban or a suspension
//	        acts on.
//	attach  signed by the TOWER's identity key. Redeeming is the relay proving it is the
//	        origin the Station is attached behind. Core takes the origin from WHO SIGNED and
//	        never from the body, so a Tower cannot attach a Station onto another Tower's
//	        origin even holding a perfectly valid invitation.
//
// The invitation's secret exists exactly once, in the reply to the invite. It is not stored
// and cannot be re-read: a lost invitation is re-issued, never recovered.

import (
	"encoding/json"
	"errors"
	"fmt"

	"rogerai.fm/roger/v5/internal/client"
	"rogerai.fm/roger/v5/internal/tower"
)

// StationKeys is the identity an invitation authorizes: a Station and its two public keys,
// as `roger-station init` prints them.
type StationKeys struct {
	// StationID may be empty, in which case Core allocates one. A Station that has already
	// initialized has its own, and using it keeps the two sides naming the same thing.
	StationID string
	// AssertionKey signs this Station's offers and receipts.
	AssertionKey string
	// SessionKey terminates its end of the inner channel.
	SessionKey string
}

func (k StationKeys) check() error {
	switch {
	case k.AssertionKey == "" || k.SessionKey == "":
		return errors.New("a Station is authorized with BOTH its keys: " +
			"run `roger-station keys` on the Station and pass --assertion-key and --session-key")
	case k.AssertionKey == k.SessionKey:
		// Core refuses this too. Saying it here saves a round trip, and the reason is worth
		// stating: one key doing both jobs means compromising the channel also hands over
		// the ability to sign offers.
		return errors.New("the assertion and session keys must be different keys")
	}
	return nil
}

// Invitation is a one-use authorization for a Station to attach.
type Invitation struct {
	InvitationID string `json:"invitation_id"`
	StationID    string `json:"station_id"`
	TowerID      string `json:"tower_id"`
	// Secret is shown ONCE, by the invite call that created it.
	Secret    string `json:"secret"`
	ExpiresIn int    `json:"expires_in"`

	// Keys is what the invitation authorized, carried so the redeeming call can present the
	// same pair. Core matches them against what was recorded; a mismatch is a refusal.
	Keys StationKeys `json:"-"`
}

// Attachment is what Core recorded.
type Attachment struct {
	StationID string `json:"station_id"`
	State     string `json:"state"`
}

// InviteStation authorizes a Station to attach behind THIS Tower, as the operator.
func InviteStation(st *tower.State, keys StationKeys) (Invitation, error) {
	adm, ok := LoadAdmission(st.Dir())
	if !ok || adm.TowerID == "" {
		return Invitation{}, errors.New("this Tower is not registered yet - run `roger-tower register` first")
	}
	if err := keys.check(); err != nil {
		return Invitation{}, err
	}
	body, err := json.Marshal(map[string]string{
		// The Tower comes from our own admission record, not from a flag. An operator naming
		// the wrong Tower would authorize a Station behind a relay they meant nothing by,
		// and Core cannot tell that apart from a deliberate choice.
		"tower_id":      adm.TowerID,
		"station_id":    keys.StationID,
		"assertion_key": keys.AssertionKey,
		"session_key":   keys.SessionKey,
	})
	if err != nil {
		return Invitation{}, err
	}
	var out Invitation
	if err := signedPost(brokerBase()+"/tower/station/invite", nil, body, &out); err != nil {
		return Invitation{}, err
	}
	out.Keys = keys
	if out.Keys.StationID == "" {
		out.Keys.StationID = out.StationID
	}
	return out, nil
}

// AttachStation redeems an invitation, as the TOWER.
//
// The owner is this account's public key, and that is deliberate rather than incidental:
// Core recorded the invitation against the account PUBKEY, because that is what the policy
// layer resolves when it asks whether an owner is present and in good standing. Sending the
// login name instead produces an attachment that verifies perfectly and is then refused for
// "no owner, which public admission requires" - a leaf rejected for a reason that has
// nothing to do with the leaf. That bug has already been fixed once, on the other side.
func AttachStation(st *tower.State, invite Invitation) (Attachment, error) {
	adm, ok := LoadAdmission(st.Dir())
	if !ok || adm.TowerID == "" {
		return Attachment{}, errors.New("this Tower is not registered yet - run `roger-tower register` first")
	}
	if invite.InvitationID == "" || invite.Secret == "" {
		return Attachment{}, errors.New("redeeming needs the invitation id and its one-time secret")
	}
	stationID := invite.Keys.StationID
	if stationID == "" {
		stationID = invite.StationID
	}
	body, err := json.Marshal(map[string]string{
		"tower_id":      adm.TowerID,
		"invitation_id": invite.InvitationID,
		"secret":        invite.Secret,
		"station_id":    stationID,
		"owner":         client.UserPubHex(),
		"assertion_key": invite.Keys.AssertionKey,
		"session_key":   invite.Keys.SessionKey,
	})
	if err != nil {
		return Attachment{}, err
	}
	var out Attachment
	// Signed as the TOWER: towerPost uses the identity key, which is what makes Core record
	// this Tower as the Station's origin.
	if err := towerPost(st, "/tower/station/attach", body, &out); err != nil {
		return Attachment{}, fmt.Errorf("%w", err)
	}
	return out, nil
}

// TowerStatus is what Core believes about one Tower.
//
// It is the only trustworthy answer to "what state am I in". A Tower's own admission file
// records what it was TOLD at enrollment and goes stale the instant an administrator
// promotes, suspends or revokes it - the state lives on Core, and asking is the only way to
// know it.
type TowerStatus struct {
	TowerID           string `json:"tower_id"`
	State             string `json:"state"`
	MayTakeWork       bool   `json:"may_take_work"`
	LinkLive          bool   `json:"link_live"`
	LeaseExpires      int64  `json:"lease_expires"`
	InventoryRevision int64  `json:"inventory_revision"`
	// CarriesTraffic is Core saying whether Tower-backed routing is shipped. False is
	// INFORMATION, not a fault: it is the difference between "my Station is broken" and
	// "this part is not built yet", and an operator who cannot tell those apart will spend
	// an afternoon on the wrong one.
	CarriesTraffic bool   `json:"carries_traffic"`
	Note           string `json:"note"`
	Routable       []struct {
		StationID string `json:"station_id"`
		OfferID   string `json:"offer_id"`
		Model     string `json:"model"`
		Modality  string `json:"modality"`
		Capacity  int64  `json:"capacity"`
	} `json:"routable"`
}

// FetchStatus asks Core what it believes about this account's Towers.
func FetchStatus(st *tower.State) ([]TowerStatus, error) {
	var out struct {
		Towers []TowerStatus `json:"towers"`
	}
	if err := signedGet(brokerBase()+"/tower/status", &out); err != nil {
		return nil, err
	}
	return out.Towers, nil
}

// RevokeStation retires a Station identity, as the operator.
//
// Signed by the ACCOUNT rather than the Tower: retiring an identity is an account decision,
// and an operator must be able to make it when the Tower itself is the thing that has gone
// wrong - a revocation that required a healthy relay to perform would be unavailable in
// exactly the situation it exists for.
func RevokeStation(st *tower.State, stationID string) error {
	if stationID == "" {
		return errors.New("revoking needs the Station id")
	}
	if _, ok := LoadAdmission(st.Dir()); !ok {
		return errors.New("this Tower is not registered yet - run `roger-tower register` first")
	}
	body, err := json.Marshal(map[string]string{"station_id": stationID})
	if err != nil {
		return err
	}
	return signedPost(brokerBase()+"/tower/station/revoke", nil, body, nil)
}

// SetOwnState asks Core to drain, resume or retire a Tower this account owns.
//
// Signed by the ACCOUNT, not the Tower. Retiring hardware has to work when the Tower itself
// is the thing that has gone wrong, and a control that needed a healthy relay would be
// unavailable in exactly the situation it exists for.
func SetOwnState(st *tower.State, state string) error {
	adm, ok := LoadAdmission(st.Dir())
	if !ok || adm.TowerID == "" {
		return errors.New("this Tower is not registered yet - run `roger-tower register` first")
	}
	body, err := json.Marshal(map[string]string{"tower_id": adm.TowerID, "state": state})
	if err != nil {
		return err
	}
	return signedPost(brokerBase()+"/tower/self/lifecycle", nil, body, nil)
}
