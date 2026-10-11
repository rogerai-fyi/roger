package tower

import (
	"crypto/hmac"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"rogerai.fm/roger/v6/internal/protocol"
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
	// Curated marks a Station that PROXIES a commercial upstream API rather than serving
	// local hardware; CuratedProvider names it. The label is honesty, not billing: the
	// standalone plane stays free either way (a markup with no broker would be a toll
	// collected by nobody), but discovery and receipts must never blur a proxy into the
	// local network. Mirrors the public broker's curated identity rule.
	Curated         bool   `json:"curated,omitempty"`
	CuratedProvider string `json:"curated_provider,omitempty"`
	// Limit is what the owner allowed this Station to serve: the models typed when attaching it
	// by hand, or an invite's --models. AnyModel marks a Station that joined with an unlimited
	// invite. A record with neither (from before reports existed) is held to its current models,
	// so a report never widens what the owner set (SetStationModels).
	Limit    []string `json:"limit,omitempty"`
	AnyModel bool     `json:"any_model,omitempty"`
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
	// Curated labels an answer that a proxy Station served from the named commercial
	// upstream - honest routing on the receipt, with Cost still always 0: the standalone
	// plane never bills, curated or not.
	Curated         bool   `json:"curated,omitempty"`
	CuratedProvider string `json:"curated_provider,omitempty"`
}

// String renders the receipt for a human. The wording is part of the contract: it names
// the local network and claims nothing about RogerAI.
func (r LocalReceipt) String() string {
	if r.Curated {
		return fmt.Sprintf("request %s served by station %s (model %s, curated via %s) on local network %s - free, locally accounted",
			r.RequestID, r.StationID, r.Model, r.CuratedProvider, r.NetworkID)
	}
	return fmt.Sprintf("request %s served by station %s (model %s) on local network %s - free, locally accounted",
		r.RequestID, r.StationID, r.Model, r.NetworkID)
}

// AttachStation admits a local Station. It requires an admitted operator first: a
// network with no operator has nobody with the authority to attach anything.
func (s *State) AttachStation(id, keyHash string, models []string) (Station, error) {
	return s.attachStation(id, keyHash, models, "")
}

// AttachCuratedStation admits a Station that proxies the named commercial provider. The
// provider name renders in discovery and receipts, so it gets the same display sanitation
// the public broker applies at its register door (trim, strip control chars, bound).
func (s *State) AttachCuratedStation(id, keyHash string, models []string, provider string) (Station, error) {
	provider = protocol.CanonicalVariantText(provider)
	if provider == "" {
		return Station{}, errors.New("a curated Station needs a provider name: an unnamed proxy is the exact ambiguity the label exists to remove")
	}
	return s.attachStation(id, keyHash, models, provider)
}

func (s *State) attachStation(id, keyHash string, models []string, curatedProvider string) (Station, error) {
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
	st, err := s.attachLocked(bs, id, keyHash, models, curatedProvider)
	if err != nil {
		return Station{}, err
	}
	// The models the owner typed are what this Station may ever serve.
	bs.Stations[id].Limit = append([]string(nil), models...)
	st.Limit = bs.Stations[id].Limit
	if err := s.saveBootstrap(bs); err != nil {
		return Station{}, err
	}
	return st, nil
}

// attachLocked records a Station in bs; the caller holds bootstrapMu and saves. A Station
// joining with the owner's invite needs no operator: the invite is the owner's authority.
func (s *State) attachLocked(bs *Snapshot, id, keyHash string, models []string, curatedProvider string) (Station, error) {
	if id == "" || keyHash == "" || len(models) == 0 {
		return Station{}, errors.New("a Station needs an id, a key hash, and at least one offered model")
	}
	if bs.Stations == nil {
		bs.Stations = map[string]*Station{}
	}
	// An existing id may only be updated by the SAME key, so a second Station cannot
	// take over the first's identity by re-attaching under it.
	if prev, ok := bs.Stations[id]; ok && !hmac.Equal([]byte(prev.KeyHash), []byte(keyHash)) {
		return Station{}, errors.New("that Station id is already attached under a different key")
	}
	// The broker's kind-flip guard, mirrored: an id that attached as a human Station
	// cannot re-attach as a curated proxy (or the reverse) even under the same key -
	// that is a new thing wearing an earned identity, so it must arrive as a new one.
	if prev, ok := bs.Stations[id]; ok && prev.Curated != (curatedProvider != "") {
		return Station{}, errors.New("that Station id is already attached as the other kind (human vs curated): retire it and attach a new id")
	}

	st := &Station{
		ID:              id,
		KeyHash:         keyHash,
		Models:          append([]string(nil), models...),
		NetworkID:       s.LocalNetworkID,
		AttachedAt:      time.Now().Unix(),
		Curated:         curatedProvider != "",
		CuratedProvider: curatedProvider,
	}
	bs.Stations[id] = st
	return *st, nil
}

// RevokeStation is a standalone Tower's kill switch for one of its own Stations: it is removed
// from the registry, so its next poll, completion or chunk is refused (the plane reads the
// registry on every station request). A joined Tower's Stations are revoked through Roger Core.
func (s *State) RevokeStation(id string) error {
	if s.Mode != ModeStandalone {
		return ErrNotStandalone
	}
	if id == "" {
		return errors.New("revoking needs the Station id")
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	bs, err := s.loadBootstrap()
	if err != nil {
		return err
	}
	if _, ok := bs.Stations[id]; !ok {
		return fmt.Errorf("no Station %q is attached to this Tower", id)
	}
	delete(bs.Stations, id)
	return s.saveBootstrap(bs)
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
		Cost:            0, // free and locally accounted in v1 - curated included
		At:              time.Now().Unix(),
		Curated:         chosen.Curated,
		CuratedProvider: chosen.CuratedProvider,
	}, nil
}

// SetStationModels records what a Station reports it serves now, kept within what the owner allowed
// it (features/join/invite.feature, "A station's model list follows what it actually serves"). It
// returns the Station as the Tower now routes it, and whether that changed (an unchanged report
// writes nothing).
func (s *State) SetStationModels(id string, reported []string) (Station, bool, error) {
	if err := checkStationModels(reported); err != nil {
		return Station{}, false, err
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return Station{}, false, err
	}
	st, ok := bs.Stations[id]
	if !ok {
		return Station{}, false, errors.New("no attached Station has that id")
	}
	allowed := st.Limit
	if len(allowed) == 0 && !st.AnyModel {
		allowed = st.Models // a record from before reports: held to what it serves today
	}
	var now []string
	for _, m := range reported {
		if m == "" || slices.Contains(now, m) || (!st.AnyModel && !slices.Contains(allowed, m)) {
			continue
		}
		now = append(now, m)
	}
	slices.Sort(now)
	cur := append([]string(nil), st.Models...)
	slices.Sort(cur)
	if slices.Equal(now, cur) {
		return *st, false, nil
	}
	if !st.AnyModel && len(st.Limit) == 0 {
		st.Limit = append([]string(nil), st.Models...) // remember the original limit before narrowing
	}
	st.Models = now
	if err := s.saveBootstrap(bs); err != nil {
		return Station{}, false, err
	}
	return *st, true, nil
}

// MaxStationModels bounds what one Station may report it serves (at a join and on every poll).
const MaxStationModels = 64

// maxStationModelID bounds one reported model id, as the routing contract bounds a model id.
const maxStationModelID = 256

// checkStationModels refuses a report that is too long or carries an id that is empty, padded,
// overlong or has control characters: a Station cannot grow the registry without limit.
func checkStationModels(models []string) error {
	if len(models) > MaxStationModels {
		return fmt.Errorf("a Station may report at most %d models", MaxStationModels)
	}
	for _, m := range models {
		if m == "" || m != strings.TrimSpace(m) || len(m) > maxStationModelID ||
			strings.IndexFunc(m, unicode.IsControl) >= 0 {
			return fmt.Errorf("%q is not a model id", m)
		}
	}
	return nil
}
