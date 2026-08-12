package dispatch

// edge.go is the grant for the EDGE path, where the consumer reaches the Station directly
// through a Tower and Roger Core never sees the request.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY IT CANNOT BE THE SAME OBJECT
//
// A Core-relayed grant commits to a digest of the request, because Core had the request in
// its hands. Here it does not have it and never will: the bytes go from the consumer into a
// TLS session the Tower cannot read and out at the Station. There is nothing to hash at
// issuing time.
//
// So an edge grant authorizes a BOUNDED SCOPE - one attempt, one Station, one model, a
// ceiling on input and output, a deadline, a one-use nonce - and the digest travels the
// other way, in the Station's receipt and the consumer's acknowledgement.
//
// # WHAT PROTECTS THE REQUEST, GIVEN THE GRANT NO LONGER DOES
//
// On the relayed path the digest is what stops a Tower pairing a real grant with a request
// of its own. Here that job belongs to TLS: the request is inside a session terminating at
// the Station, and the Tower splices ciphertext. A relay cannot substitute what it cannot
// read.
//
// What the digest check used to catch and this does NOT is a dishonest STATION - one that
// serves something other than what it was sent, or reports usage it did not spend. That is
// caught afterwards instead, by the consumer's acknowledgement disagreeing with the
// Station's receipt. Two claims from parties with opposing interests, and settlement takes
// the lower. This is a real reduction in what is caught BEFORE the fact, and it is the price
// of Core not carrying the payload.
//
// # A SEPARATE SIGNED TYPE, DELIBERATELY
//
// TypeEdgeGrant is not TypeGrant. If the two shared a type, a grant issued for the relayed
// path - where the Station is handed bytes by a Tower - could be presented on the edge path,
// where nothing checks a digest, and the check that binds the request would simply not run.
// Different object, different type, and towerobj binds the type into the signature.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"rogerai.fm/roger/v5/internal/towerobj"
)

// TypeEdgeGrant identifies the signed object.
const TypeEdgeGrant = "dispatch.edge_grant"

// EdgeTarget is what Core decides before it has seen anything.
type EdgeTarget struct {
	TowerID      string
	StationID    string
	StationEpoch int64
	Model        string
	Modality     string
	// RelayName is the public name the consumer connects to, under a domain CORE controls.
	// It is in the grant so the Station can refuse a grant minted for a name it does not
	// answer to, and so the consumer has somewhere to go without being told the Station's
	// address - reachability is what the Tower is providing.
	RelayName string
	// MaxIn and MaxOut bound the attempt in place of a digest. They are the only thing
	// standing between one authorization and an unbounded amount of work, so they are
	// required rather than defaulted: a zero ceiling that meant "no limit" would be one
	// forgotten field away from an unmetered Station.
	MaxIn  int64
	MaxOut int64
	// AssertionKey is the key from the ATTACHMENT record, carried for the same reason as on
	// the relayed path: it is what the receipt is verified against.
	AssertionKey ed25519.PublicKey
}

// EdgeGrant authorizes one edge attempt.
type EdgeGrant struct {
	JobID        string
	AttemptID    string
	TowerID      string
	StationID    string
	StationEpoch int64
	Model        string
	Modality     string
	RelayName    string
	MaxIn        int64
	MaxOut       int64
	Deadline     time.Time
	Nonce        string
	// Signed is the canonical signed object, handed to the CONSUMER rather than to a Tower.
	Signed []byte
}

// MintEdge builds and signs an edge grant.
//
// Like Mint, it does not make the attempt collectable: recording the attempt happens between
// minting and handing the grant out, so that an authorization nobody recorded can never be
// the one that gets used.
func (r *Registry) MintEdge(t EdgeTarget) (EdgeGrant, error) {
	switch {
	case t.TowerID == "" || t.StationID == "":
		return EdgeGrant{}, errors.New("an edge grant names exactly one Tower and one Station")
	case t.Model == "" || t.Modality == "":
		return EdgeGrant{}, errors.New("an edge grant names the model and modality it authorizes")
	case t.RelayName == "":
		return EdgeGrant{}, errors.New("an edge grant names the relay the consumer connects to")
	case t.MaxIn <= 0 || t.MaxOut <= 0:
		// REQUIRED, not defaulted. Without a digest these ceilings are the whole of what
		// bounds the attempt, and a zero meaning "unlimited" would make forgetting a field
		// indistinguishable from authorizing everything.
		return EdgeGrant{}, errors.New("an edge grant bounds input and output, and one of those bounds is missing")
	case len(t.AssertionKey) != ed25519.PublicKeySize:
		return EdgeGrant{}, errors.New("an edge grant needs the Station's attachment-recorded assertion key")
	}

	now := r.cfg.Now()
	g := EdgeGrant{
		JobID:        "job-" + randomHex(12),
		AttemptID:    "att-" + randomHex(12),
		TowerID:      t.TowerID,
		StationID:    t.StationID,
		StationEpoch: t.StationEpoch,
		Model:        t.Model,
		Modality:     t.Modality,
		RelayName:    t.RelayName,
		MaxIn:        t.MaxIn,
		MaxOut:       t.MaxOut,
		Deadline:     now.Add(r.cfg.Lifetime),
		Nonce:        randomHex(16),
	}
	body, err := json.Marshal(map[string]any{
		"network":       r.cfg.Network,
		"type":          TypeEdgeGrant,
		"version":       towerobj.FormatInt(Version),
		"job_id":        g.JobID,
		"attempt_id":    g.AttemptID,
		"tower_id":      g.TowerID,
		"station_id":    g.StationID,
		"station_epoch": towerobj.FormatInt(g.StationEpoch),
		"model":         g.Model,
		"modality":      g.Modality,
		"relay_name":    g.RelayName,
		"max_in":        towerobj.FormatInt(g.MaxIn),
		"max_out":       towerobj.FormatInt(g.MaxOut),
		"deadline":      towerobj.FormatInt(g.Deadline.Unix()),
		"nonce":         g.Nonce,
	})
	if err != nil {
		return EdgeGrant{}, err
	}
	signed, err := towerobj.Sign(r.cfg.Signer, r.cfg.Network, TypeEdgeGrant, Version, body, "core_sig")
	if err != nil {
		return EdgeGrant{}, err
	}
	g.Signed = signed
	return g, nil
}

// ParseEdgeGrant is the STATION's side: verify Core wrote it, then read it.
//
// One definition of what an edge grant is and what makes it valid, for the same reason
// ParseGrant is one definition: two implementations of "is this authorization good" will
// eventually disagree about a field, and that disagreement looks like an attack from both
// ends.
//
// The order is the order that makes each check meaningful. The SIGNATURE first, because
// every field below is worthless until we know Core wrote it. Then that the grant is for
// THIS Station - a valid grant for another machine is somebody else's authorization, and
// pointing it here is exactly what a relay is positioned to do. Then the deadline. Then the
// input ceiling, which is the only thing bounding the work now that no digest does.
func ParseEdgeGrant(raw []byte, coreKey ed25519.PublicKey, network, stationID string,
	request []byte, now time.Time) (EdgeGrant, error) {

	if err := towerobj.Verify(coreKey, network, TypeEdgeGrant, Version, raw, "core_sig"); err != nil {
		return EdgeGrant{}, fmt.Errorf("this grant is not signed by Roger Core: %w", err)
	}
	// No network comparison below: towerobj.Verify binds the network into the signature, so
	// a grant for another network has already failed. A second check here would be a branch
	// no input can reach - protection that reads as protection and protects nothing.
	var obj struct {
		JobID        string `json:"job_id"`
		AttemptID    string `json:"attempt_id"`
		TowerID      string `json:"tower_id"`
		StationID    string `json:"station_id"`
		StationEpoch string `json:"station_epoch"`
		Model        string `json:"model"`
		Modality     string `json:"modality"`
		RelayName    string `json:"relay_name"`
		MaxIn        string `json:"max_in"`
		MaxOut       string `json:"max_out"`
		Deadline     string `json:"deadline"`
		Nonce        string `json:"nonce"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return EdgeGrant{}, fmt.Errorf("this grant cannot be read: %w", err)
	}
	if obj.StationID != stationID {
		return EdgeGrant{}, fmt.Errorf("this grant is for Station %q, not this one", obj.StationID)
	}
	epoch, err := strconv.ParseInt(obj.StationEpoch, 10, 64)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's Station epoch is not a number")
	}
	maxIn, err := strconv.ParseInt(obj.MaxIn, 10, 64)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's input ceiling is not a number")
	}
	maxOut, err := strconv.ParseInt(obj.MaxOut, 10, 64)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's output ceiling is not a number")
	}
	unix, err := strconv.ParseInt(obj.Deadline, 10, 64)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's deadline is not a time")
	}
	deadline := time.Unix(unix, 0)
	if !now.Before(deadline) {
		return EdgeGrant{}, ErrExpired
	}
	// THE CEILING IS WHAT THE DIGEST USED TO BE. On the relayed path an oversized body was
	// caught by not matching the digest; here nothing else stands between one authorization
	// and as much work as the caller cares to ask for.
	if int64(len(request)) > maxIn {
		return EdgeGrant{}, fmt.Errorf("this request is %d bytes and the grant allows %d",
			len(request), maxIn)
	}
	return EdgeGrant{
		JobID: obj.JobID, AttemptID: obj.AttemptID, TowerID: obj.TowerID,
		StationID: obj.StationID, StationEpoch: epoch, Model: obj.Model,
		Modality: obj.Modality, RelayName: obj.RelayName, MaxIn: maxIn, MaxOut: maxOut,
		Deadline: deadline, Nonce: obj.Nonce, Signed: raw,
	}, nil
}
