package tower

import (
	"crypto/hmac"
	"errors"
	"fmt"
	"sort"
	"time"
)

// The local Station registry. In standalone v1 a Tower routes ONLY to Stations it
// admitted itself: there is no public directory to consult and no RogerAI credit,
// hold, or payout involved. Routing is free and locally accounted.
//
// The receipt a local route produces is deliberately, visibly local. It carries the
// standalone network id and says "local network" in plain words, so a receipt from a
// self-hosted Tower can never be mistaken for one RogerAI verified.

// Station is a locally attached inference provider.
type Station struct {
	ID         string   `json:"id"`
	KeyHash    string   `json:"key_hash"`
	Models     []string `json:"models"`
	NetworkID  string   `json:"network_id"`
	AttachedAt int64    `json:"attached_at"`
}

// LocalReceipt records one locally routed request. It is NOT a RogerAI settlement
// receipt and must never be presented as one - Cost is always zero in v1 because
// standalone routing is free and locally accounted.
type LocalReceipt struct {
	RequestID       string `json:"request_id"`
	ClientKeyHash   string `json:"client_key_hash"`
	StationID       string `json:"station_id"`
	Model           string `json:"model"`
	NetworkID       string `json:"network_id"`
	RootFingerprint string `json:"root_fingerprint"`
	Cost            int    `json:"cost"`
	At              int64  `json:"at"`
}

// String renders the receipt for a human. The wording is part of the contract: it names
// the local network and claims nothing about RogerAI.
func (r LocalReceipt) String() string {
	return fmt.Sprintf("request %s served by station %s (model %s) on local network %s - free, locally accounted",
		r.RequestID, r.StationID, r.Model, r.NetworkID)
}

// AttachStation admits a local Station. It requires an admitted operator first: a
// network with no operator has nobody with the authority to attach anything.
func (s *State) AttachStation(id, keyHash string, models []string) (Station, error) {
	if s.Mode != ModeStandalone {
		return Station{}, ErrNotStandalone
	}
	if id == "" || keyHash == "" || len(models) == 0 {
		return Station{}, errors.New("a Station needs an id, a key hash, and at least one offered model")
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	bs, err := s.loadBootstrap()
	if err != nil {
		return Station{}, err
	}
	if bs.Operator == nil {
		return Station{}, errors.New("this network has no local operator yet: consume a bootstrap invitation before attaching Stations")
	}
	if bs.Stations == nil {
		bs.Stations = map[string]*Station{}
	}
	// An existing id may only be updated by the SAME key, so a second Station cannot
	// take over the first's identity by re-attaching under it.
	if prev, ok := bs.Stations[id]; ok && !hmac.Equal([]byte(prev.KeyHash), []byte(keyHash)) {
		return Station{}, errors.New("that Station id is already attached under a different key")
	}

	st := &Station{
		ID:         id,
		KeyHash:    keyHash,
		Models:     append([]string(nil), models...),
		NetworkID:  s.LocalNetworkID,
		AttachedAt: time.Now().Unix(),
	}
	bs.Stations[id] = st
	if err := s.saveBootstrap(bs); err != nil {
		return Station{}, err
	}
	return *st, nil
}

// Stations lists the attached Stations, ordered by id so output is stable.
func (s *State) Stations() ([]Station, error) {
	if s.Mode != ModeStandalone {
		return nil, ErrNotStandalone
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	bs, err := s.loadBootstrap()
	if err != nil {
		return nil, err
	}
	out := make([]Station, 0, len(bs.Stations))
	for _, st := range bs.Stations {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Route selects an attached Station offering the model and records a local receipt.
//
// The caller must be AN admitted local client - any of them, not only the operator: a
// standalone Tower is not an open relay, so an unknown client is refused before any Station
// is considered.
func (s *State) Route(clientKeyHash, model string) (LocalReceipt, error) {
	if s.Mode != ModeStandalone {
		return LocalReceipt{}, ErrNotStandalone
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	bs, err := s.loadBootstrap()
	if err != nil {
		return LocalReceipt{}, err
	}
	if !clientAdmitted(bs, clientKeyHash) {
		return LocalReceipt{}, errors.New("only this network's admitted local client may route requests")
	}

	var chosen *Station
	for _, st := range bs.Stations {
		for _, m := range st.Models {
			if m == model {
				if chosen == nil || st.ID < chosen.ID { // deterministic pick
					chosen = st
				}
				break
			}
		}
	}
	if chosen == nil {
		return LocalReceipt{}, fmt.Errorf("no attached Station offers %q", model)
	}

	reqID, err := randomHex(8)
	if err != nil {
		return LocalReceipt{}, err
	}
	fp, err := s.rootFingerprint()
	if err != nil {
		return LocalReceipt{}, err
	}
	return LocalReceipt{
		RequestID:       reqID,
		ClientKeyHash:   clientKeyHash,
		StationID:       chosen.ID,
		Model:           model,
		NetworkID:       s.LocalNetworkID,
		RootFingerprint: fp,
		Cost:            0, // free and locally accounted in v1
		At:              time.Now().Unix(),
	}, nil
}
