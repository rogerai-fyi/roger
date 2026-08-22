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

	"rogerai.fm/roger/v6/internal/tower"
)

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
