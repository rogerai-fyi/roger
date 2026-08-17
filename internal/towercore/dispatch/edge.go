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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"rogerai.fm/roger/v5/internal/towerobj"
)

// TypeEdgeGrant identifies the signed object.
const TypeEdgeGrant = "dispatch.edge_grant"

// maxEdgePriceMicros caps the per-token price a grant may pin: 1e10 micro-USD per 1M tokens
// ($10,000 / 1M). Sanity, not policy - the public band (checked at leaf admission AND re-checked
// at authorize) is orders of magnitude lower.
const maxEdgePriceMicros = 10_000_000_000

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
	// MaxTokIn and MaxTokOut are the TOKEN ceilings for the per-token billing path (Option C).
	// They sit ALONGSIDE the byte ceilings, not instead of them: bytes remain the hard cap a
	// Tower's byte-attestation enforces (a token is >= 1 byte), and tokens are the ceiling the
	// consumer's wallet hold is sized against. Optional and default 0 ("no token ceiling") so a
	// byte-only grant is unchanged; settlement treats 0 as "not token-bounded".
	MaxTokIn  int64
	MaxTokOut int64
	// AssertionKey is the key from the ATTACHMENT record, carried for the same reason as on
	// the relayed path: it is what the receipt is verified against.
	AssertionKey ed25519.PublicKey
	// ConsumerKey is the account the grant is issued TO. It is signed into the grant so that
	// the acknowledgement, which settlement corroborates against, can only come from the
	// consumer that was authorized - not any account that happens to learn the attempt id. A
	// security review found the ack unbound, letting a third party file one for somebody
	// else's attempt.
	ConsumerKey ed25519.PublicKey
	// ConsumerEnvKey is the consumer's static X25519 public key (Option C, Topology 2): the
	// serving node seals its RESULT to this so it travels back through a blind tower the node's
	// operator and the tower cannot read. Distinct from ConsumerKey (ed25519, which signs the
	// ack). Optional and 32 bytes when present; empty on the byte path, where nothing is sealed
	// to the consumer.
	ConsumerEnvKey []byte
	// PriceInMicros and PriceOutMicros PIN the consumer price into the grant, in MICRO-USD PER
	// 1,000,000 TOKENS, copied at authorize from the Station's signed, band-checked offer. The
	// price the consumer is billed at settlement is read from HERE - the same Core-signed
	// object as every other money bound - so a price hike between authorize and settle cannot
	// reprice an in-flight attempt, and neither the tower nor the node can feed settlement a
	// number the consumer never agreed to. Optional; 0/0 means unpriced-per-token, and the
	// byte tariff governs.
	PriceInMicros  int64
	PriceOutMicros int64
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
	// MaxTokIn and MaxTokOut are the optional TOKEN ceilings (Option C per-token billing).
	// 0 means "no token ceiling" (a byte-only grant), so old grants read back unchanged.
	MaxTokIn  int64
	MaxTokOut int64
	Deadline  time.Time
	Nonce     string
	// ConsumerKey is the account this grant was issued to, hex in the signed body. The
	// acknowledgement must come from it.
	ConsumerKey ed25519.PublicKey
	// ConsumerEnvKey is the consumer's X25519 key results are sealed to (Option C). Empty on a
	// byte-path grant; 32 bytes when present.
	ConsumerEnvKey []byte
	// PriceInMicros and PriceOutMicros are the pinned consumer price (micro-USD per 1M tokens);
	// 0/0 = not token-priced.
	PriceInMicros  int64
	PriceOutMicros int64
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
	case t.MaxTokIn < 0 || t.MaxTokOut < 0:
		// Token ceilings are OPTIONAL (0 = none), but a NEGATIVE one is a bug, not "unset":
		// mint-side validation stays symmetric with the parse side (which rejects it), so Core
		// never signs a grant whose token fields every reader will reject as dead-on-arrival.
		return EdgeGrant{}, errors.New("an edge grant's token ceilings cannot be negative")
	case len(t.AssertionKey) != ed25519.PublicKeySize:
		return EdgeGrant{}, errors.New("an edge grant needs the Station's attachment-recorded assertion key")
	case len(t.ConsumerKey) != ed25519.PublicKeySize:
		return EdgeGrant{}, errors.New("an edge grant is issued to a consumer, and none was named")
	case t.PriceInMicros < 0 || t.PriceOutMicros < 0:
		// Optional (0 = unpriced), but a negative price is a bug that would mint negative money.
		return EdgeGrant{}, errors.New("an edge grant's prices cannot be negative")
	case t.PriceInMicros > maxEdgePriceMicros || t.PriceOutMicros > maxEdgePriceMicros:
		// Defense in depth: the broker only ever pins band-checked prices, but this signer must
		// not be able to mint an absurd one if a caller slips. $10k per 1M tokens is far above
		// any real band ceiling and far below any arithmetic hazard.
		return EdgeGrant{}, errors.New("an edge grant's price is implausibly large")
	case len(t.ConsumerEnvKey) != 0 && len(t.ConsumerEnvKey) != 32:
		// OPTIONAL, but when present it must be a plausible X25519 public key: a grant carrying
		// a malformed sealing key would make every node that honors it fail to seal its result,
		// so the mint refuses to sign one rather than minting dead-on-arrival authorization.
		return EdgeGrant{}, errors.New("an edge grant's consumer envelope key must be 32 bytes when present")
	}

	now := r.cfg.Now()
	life := r.cfg.EdgeLifetime
	if life <= 0 {
		life = r.cfg.Lifetime
	}
	g := EdgeGrant{
		JobID:          "job-" + randomHex(12),
		AttemptID:      "att-" + randomHex(12),
		TowerID:        t.TowerID,
		StationID:      t.StationID,
		StationEpoch:   t.StationEpoch,
		Model:          t.Model,
		Modality:       t.Modality,
		RelayName:      t.RelayName,
		MaxIn:          t.MaxIn,
		MaxOut:         t.MaxOut,
		MaxTokIn:       t.MaxTokIn,
		MaxTokOut:      t.MaxTokOut,
		Deadline:       now.Add(life),
		Nonce:          randomHex(16),
		ConsumerKey:    t.ConsumerKey,
		ConsumerEnvKey: t.ConsumerEnvKey,
		PriceInMicros:  t.PriceInMicros,
		PriceOutMicros: t.PriceOutMicros,
	}
	body, err := json.Marshal(map[string]any{
		"network":          r.cfg.Network,
		"type":             TypeEdgeGrant,
		"version":          towerobj.FormatInt(Version),
		"job_id":           g.JobID,
		"attempt_id":       g.AttemptID,
		"tower_id":         g.TowerID,
		"station_id":       g.StationID,
		"station_epoch":    towerobj.FormatInt(g.StationEpoch),
		"model":            g.Model,
		"modality":         g.Modality,
		"relay_name":       g.RelayName,
		"max_in":           towerobj.FormatInt(g.MaxIn),
		"max_out":          towerobj.FormatInt(g.MaxOut),
		"max_tok_in":       towerobj.FormatInt(g.MaxTokIn),
		"max_tok_out":      towerobj.FormatInt(g.MaxTokOut),
		"deadline":         towerobj.FormatInt(g.Deadline.Unix()),
		"nonce":            g.Nonce,
		"consumer_key":     hex.EncodeToString(g.ConsumerKey),
		"consumer_env_key": hex.EncodeToString(g.ConsumerEnvKey), // "" when absent
		"price_in_micros":  towerobj.FormatInt(g.PriceInMicros),
		"price_out_micros": towerobj.FormatInt(g.PriceOutMicros),
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
		JobID          string `json:"job_id"`
		AttemptID      string `json:"attempt_id"`
		TowerID        string `json:"tower_id"`
		StationID      string `json:"station_id"`
		StationEpoch   string `json:"station_epoch"`
		Model          string `json:"model"`
		Modality       string `json:"modality"`
		RelayName      string `json:"relay_name"`
		MaxIn          string `json:"max_in"`
		MaxOut         string `json:"max_out"`
		MaxTokIn       string `json:"max_tok_in"`
		MaxTokOut      string `json:"max_tok_out"`
		Deadline       string `json:"deadline"`
		Nonce          string `json:"nonce"`
		ConsumerKey    string `json:"consumer_key"`
		ConsumerEnvKey string `json:"consumer_env_key"`
		PriceInMicros  string `json:"price_in_micros"`
		PriceOutMicros string `json:"price_out_micros"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return EdgeGrant{}, fmt.Errorf("this grant cannot be read: %w", err)
	}
	consumerKey, err := hex.DecodeString(obj.ConsumerKey)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's consumer key is unreadable")
	}
	// OPTIONAL sealing key: absent (old / byte-path grant) reads as nil; present must be a
	// plausible X25519 public key, refused otherwise - a node must never try to seal a result
	// to a malformed key and fall back to something readable.
	consumerEnvKey, err := hex.DecodeString(obj.ConsumerEnvKey)
	if err != nil || (len(consumerEnvKey) != 0 && len(consumerEnvKey) != 32) {
		return EdgeGrant{}, errors.New("this grant's consumer envelope key is unreadable")
	}
	if len(consumerEnvKey) == 0 {
		consumerEnvKey = nil
	}
	// Pinned prices are OPTIONAL like the token ceilings: absent -> 0 (byte tariff governs),
	// present must be a valid non-negative integer.
	priceIn, err := parseOptionalCeiling(obj.PriceInMicros)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's input price is not a number")
	}
	priceOut, err := parseOptionalCeiling(obj.PriceOutMicros)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's output price is not a number")
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
	// Token ceilings are OPTIONAL (Option C): an absent field on an old byte-only grant reads
	// as 0 ("no token ceiling"), a present one must be a valid non-negative integer.
	maxTokIn, err := parseOptionalCeiling(obj.MaxTokIn)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's input token ceiling is not a number")
	}
	maxTokOut, err := parseOptionalCeiling(obj.MaxTokOut)
	if err != nil {
		return EdgeGrant{}, errors.New("this grant's output token ceiling is not a number")
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
		MaxTokIn: maxTokIn, MaxTokOut: maxTokOut,
		Deadline: deadline, Nonce: obj.Nonce, ConsumerKey: consumerKey,
		ConsumerEnvKey: consumerEnvKey, PriceInMicros: priceIn, PriceOutMicros: priceOut,
		Signed: raw,
	}, nil
}

// parseOptionalCeiling reads an OPTIONAL non-negative integer ceiling from a signed grant:
// an absent field (old byte-only grant) is 0 ("no ceiling"), a present one must parse and be
// >= 0. A negative value is rejected rather than treated as unset, so a malformed ceiling
// cannot silently disable the bound.
func parseOptionalCeiling(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, errors.New("negative ceiling")
	}
	return v, nil
}

// EdgeGrantCeiling reads the usage bounds a Core-signed edge grant authorized.
//
// It exists for SETTLEMENT, which happens after the deadline and carries no request, so it
// cannot use ParseEdgeGrant (whose deadline and request-size checks are meaningless here and
// would reject a perfectly good settlement). It still verifies the signature - the ceiling is
// only meaningful because Core set it - and that the grant is an edge grant for this Station,
// so a substituted or wrong-Station grant cannot pass a bogus ceiling into the money path.
//
// WHY THE CEILING MATTERS AT SETTLEMENT: on the no-acknowledgement path the billable usage is
// the Station's own signed figure, and the Station's operator is the party being paid. Without
// this bound, an operator could sign a receipt claiming any amount and be owed it. The grant
// is the one number in the exchange that the payee did not choose; clamping billable to it is
// what keeps "computed from billable" from meaning "computed from a number the payee invented".
func EdgeGrantCeiling(raw []byte, coreKey ed25519.PublicKey, network, stationID string) (maxIn, maxOut int64, err error) {
	if err := towerobj.Verify(coreKey, network, TypeEdgeGrant, Version, raw, "core_sig"); err != nil {
		return 0, 0, fmt.Errorf("this grant is not signed by Roger Core: %w", err)
	}
	var obj struct {
		StationID string `json:"station_id"`
		MaxIn     string `json:"max_in"`
		MaxOut    string `json:"max_out"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, 0, fmt.Errorf("this grant cannot be read: %w", err)
	}
	if obj.StationID != stationID {
		return 0, 0, fmt.Errorf("this grant is for Station %q, not this one", obj.StationID)
	}
	maxIn, err = strconv.ParseInt(obj.MaxIn, 10, 64)
	if err != nil {
		return 0, 0, errors.New("this grant's input ceiling is not a number")
	}
	maxOut, err = strconv.ParseInt(obj.MaxOut, 10, 64)
	if err != nil {
		return 0, 0, errors.New("this grant's output ceiling is not a number")
	}
	if maxIn <= 0 || maxOut <= 0 {
		return 0, 0, errors.New("this grant carries no usable ceiling")
	}
	return maxIn, maxOut, nil
}

// EdgeGrantTokenCeiling reads the TOKEN usage bounds a Core-signed edge grant authorized, the
// per-token (Option C) counterpart to EdgeGrantCeiling. Same signature + Station-binding
// checks, so a substituted or wrong-Station grant cannot pass a bogus ceiling into the money
// path. Unlike the byte ceiling, a token ceiling is OPTIONAL: an old byte-only grant (or a
// grant minted with no token ceiling) returns 0, 0 with a nil error, and settlement reads 0 as
// "not token-bounded" (so the byte cap + audit still apply). A present ceiling must be a valid
// non-negative integer.
func EdgeGrantTokenCeiling(raw []byte, coreKey ed25519.PublicKey, network, stationID string) (maxTokIn, maxTokOut int64, err error) {
	if err := towerobj.Verify(coreKey, network, TypeEdgeGrant, Version, raw, "core_sig"); err != nil {
		return 0, 0, fmt.Errorf("this grant is not signed by Roger Core: %w", err)
	}
	var obj struct {
		StationID string `json:"station_id"`
		MaxTokIn  string `json:"max_tok_in"`
		MaxTokOut string `json:"max_tok_out"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, 0, fmt.Errorf("this grant cannot be read: %w", err)
	}
	if obj.StationID != stationID {
		return 0, 0, fmt.Errorf("this grant is for Station %q, not this one", obj.StationID)
	}
	if maxTokIn, err = parseOptionalCeiling(obj.MaxTokIn); err != nil {
		return 0, 0, errors.New("this grant's input token ceiling is not a number")
	}
	if maxTokOut, err = parseOptionalCeiling(obj.MaxTokOut); err != nil {
		return 0, 0, errors.New("this grant's output token ceiling is not a number")
	}
	return maxTokIn, maxTokOut, nil
}

// EdgeGrantPricing reads the PINNED consumer price (micro-USD per 1M tokens) out of a
// Core-signed edge grant, for SETTLEMENT. Same signature + Station-binding gates as the
// ceiling readers, so a substituted or wrong-Station grant cannot pass a bogus price into the
// money path. 0/0 with a nil error means the grant is not token-priced (the byte tariff
// governs); a present price must be a valid non-negative integer.
func EdgeGrantPricing(raw []byte, coreKey ed25519.PublicKey, network, stationID string) (inMicros, outMicros int64, err error) {
	if err := towerobj.Verify(coreKey, network, TypeEdgeGrant, Version, raw, "core_sig"); err != nil {
		return 0, 0, fmt.Errorf("this grant is not signed by Roger Core: %w", err)
	}
	var obj struct {
		StationID      string `json:"station_id"`
		PriceInMicros  string `json:"price_in_micros"`
		PriceOutMicros string `json:"price_out_micros"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, 0, fmt.Errorf("this grant cannot be read: %w", err)
	}
	if obj.StationID != stationID {
		return 0, 0, fmt.Errorf("this grant is for Station %q, not this one", obj.StationID)
	}
	if inMicros, err = parseOptionalCeiling(obj.PriceInMicros); err != nil {
		return 0, 0, errors.New("this grant's input price is not a number")
	}
	if outMicros, err = parseOptionalCeiling(obj.PriceOutMicros); err != nil {
		return 0, 0, errors.New("this grant's output price is not a number")
	}
	return inMicros, outMicros, nil
}

// EdgeGrantMeta verifies a grant came from Roger Core and reads only its PUBLIC metadata - the
// attempt id, the Station it authorizes, and its deadline. It reads no ceiling and needs no
// request, so a Tower can use it to authorize a consumer's submit (grant is Core-signed, names
// this Station, bound to this Tower, not expired) WITHOUT ever touching the sealed request the
// grant protects - the property that lets the Tower gate abuse while staying blind. `now` checks
// the deadline; pass a zero time to skip the expiry check (e.g. a settlement-time read).
// `towerID`, when non-empty, must equal the grant's Tower - so a grant minted for one Tower
// cannot be replayed at another that happens to serve the same Station id; pass "" to skip.
func EdgeGrantMeta(raw []byte, coreKey ed25519.PublicKey, network, towerID string, now time.Time) (attemptID, stationID string, deadline time.Time, err error) {
	if verr := towerobj.Verify(coreKey, network, TypeEdgeGrant, Version, raw, "core_sig"); verr != nil {
		return "", "", time.Time{}, fmt.Errorf("this grant is not signed by Roger Core: %w", verr)
	}
	var obj struct {
		AttemptID string `json:"attempt_id"`
		StationID string `json:"station_id"`
		TowerID   string `json:"tower_id"`
		Deadline  string `json:"deadline"`
	}
	if uerr := json.Unmarshal(raw, &obj); uerr != nil {
		return "", "", time.Time{}, fmt.Errorf("this grant cannot be read: %w", uerr)
	}
	if obj.AttemptID == "" || obj.StationID == "" {
		return "", "", time.Time{}, errors.New("this grant names no attempt or Station")
	}
	if towerID != "" && obj.TowerID != towerID {
		return "", "", time.Time{}, fmt.Errorf("this grant is for Tower %q, not this one", obj.TowerID)
	}
	unix, perr := strconv.ParseInt(obj.Deadline, 10, 64)
	if perr != nil {
		return "", "", time.Time{}, errors.New("this grant's deadline is not a time")
	}
	deadline = time.Unix(unix, 0)
	if !now.IsZero() && !now.Before(deadline) {
		return "", "", time.Time{}, ErrExpired
	}
	return obj.AttemptID, obj.StationID, deadline, nil
}
