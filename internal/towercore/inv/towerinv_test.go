package inv

import (
	"crypto/ed25519"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towerobj"
)

// The two tables in features/tower/inventory_and_routing.feature are the contract this
// package exists to satisfy, and they are transcribed here row for row.
//
// Each row is written so it can only pass by reaching the control it names. That is not
// pedantry: an earlier version of this suite had three rows being caught by a check further
// up, so three controls were never exercised at all - and one of them did not exist.

const (
	towerA   = "tower-a"
	towerB   = "tower-b"
	stationA = "station-a"
	stationB = "station-b"
)

type fakePolicy struct {
	stations   map[string]Registration
	models     map[string]bool
	modalities map[string]bool
	bands      map[string][2]int64
}

func (p *fakePolicy) Station(id string) Registration { return p.stations[id] }
func (p *fakePolicy) ModelAllowed(m string) bool     { return p.models[m] }
func (p *fakePolicy) ModalityAllowed(m string) bool  { return p.modalities[m] }
func (p *fakePolicy) PriceBand(m string) (int64, int64, bool) {
	b, ok := p.bands[m]
	return b[0], b[1], ok
}

type recordedHead struct {
	tower    string
	revision int64
	hash     string
}

type harness struct {
	t   *testing.T
	set *Set
	pol *fakePolicy

	now time.Time

	towerKey  ed25519.PrivateKey
	otherKey  ed25519.PrivateKey
	stationKe map[string]ed25519.PrivateKey

	heads []recordedHead
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:         t,
		now:       time.Unix(1_700_000_000, 0).UTC(),
		stationKe: map[string]ed25519.PrivateKey{},
		pol: &fakePolicy{
			stations:   map[string]Registration{},
			models:     map[string]bool{"roger-1": true},
			modalities: map[string]bool{"text": true},
			bands:      map[string][2]int64{"roger-1": {100, 10_000}},
		},
	}
	_, h.towerKey, _ = ed25519.GenerateKey(nil)
	_, h.otherKey, _ = ed25519.GenerateKey(nil)

	h.set = New(Config{
		// Deliberately small ceilings so the limit rows are reachable without building a
		// ten-thousand-leaf fixture to prove a bounds check works.
		MaxCapabilities: 2,
		MaxBytes:        4096,
		Now:             func() time.Time { return h.now },
		RecordHead: func(tower string, rev int64, hash string) {
			h.heads = append(h.heads, recordedHead{tower, rev, hash})
		},
	}, h.pol)

	h.register(stationA)
	h.register(stationB)
	return h
}

// register gives a Station a key and a clean central record, the way attachment would.
func (h *harness) register(id string) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(h.t, err)
	h.stationKe[id] = priv
	h.pol.stations[id] = Registration{Known: true, Key: pub, OwnerPresent: true}
}

func (h *harness) towerPub() ed25519.PublicKey { return h.towerKey.Public().(ed25519.PublicKey) }

func (h *harness) unix(d time.Duration) string { return towerobj.FormatInt(h.now.Add(d).Unix()) }

// offerSpec is how a test describes a leaf, including the ways a Tower might tamper with
// one. pre runs before the Station signs; post runs after, which is the only way to model
// "the relay changed this after it was signed".
type offerSpec struct {
	pre      func(map[string]any)
	post     func(map[string]any)
	signer   ed25519.PrivateKey
	unsigned bool
}

func (h *harness) offer(station, offerID string, spec offerSpec) map[string]any {
	h.t.Helper()
	m := map[string]any{
		"network":      PublicNetwork,
		"tower_id":     towerA,
		"station_id":   station,
		"offer_id":     offerID,
		"model":        "roger-1",
		"modality":     "text",
		"price_in":     "1000",
		"price_out":    "2000",
		"earn_in":      "800",
		"earn_out":     "1600",
		"capacity":     "4",
		"capabilities": []any{"chat"},
		"expires":      h.unix(30 * time.Minute),
	}
	if spec.pre != nil {
		spec.pre(m)
	}
	var signed map[string]any
	if spec.unsigned {
		signed = decode(h.t, canon(h.t, m))
	} else {
		key := spec.signer
		if key == nil {
			key = h.stationKe[station]
		}
		signed = decode(h.t, signObj(h.t, key, TypeOffer, m, offerSigMbr))
	}
	if spec.post != nil {
		spec.post(signed)
	}
	return signed
}

// invSpec describes a full inventory revision.
type invSpec struct {
	revision int64
	prevHash string
	issued   time.Duration
	expires  time.Duration
	leaves   []map[string]any

	pre      func(map[string]any)
	post     func(map[string]any)
	rawPost  func([]byte) []byte
	signer   ed25519.PrivateKey
	unsigned bool
}

func (h *harness) inventory(spec invSpec) []byte {
	h.t.Helper()
	if spec.revision == 0 {
		spec.revision = 1
	}
	if spec.expires == 0 {
		spec.expires = 30 * time.Minute
	}
	leaves := make([]any, 0, len(spec.leaves))
	for _, l := range spec.leaves {
		leaves = append(leaves, l)
	}
	m := map[string]any{
		"network":        PublicNetwork,
		"tower_id":       towerA,
		"revision":       towerobj.FormatInt(spec.revision),
		"lease_head":     "lease-1",
		"lifecycle_head": "life-1",
		"issued":         h.unix(spec.issued),
		"expires":        h.unix(spec.expires),
		"leaves":         leaves,
	}
	// Always present, even on a cold start: prev_hash is a required member whose VALUE is
	// unverifiable before there is a head to compare it against.
	if spec.prevHash == "" {
		spec.prevHash = "genesis"
	}
	m["prev_hash"] = spec.prevHash
	return h.finish(m, TypeInventory, spec.pre, spec.post, spec.rawPost, spec.signer, spec.unsigned)
}

func (h *harness) finish(m map[string]any, typ string, pre, post func(map[string]any), rawPost func([]byte) []byte, signer ed25519.PrivateKey, unsigned bool) []byte {
	h.t.Helper()
	if pre != nil {
		pre(m)
	}
	var raw []byte
	if unsigned {
		raw = canon(h.t, m)
	} else {
		key := signer
		if key == nil {
			key = h.towerKey
		}
		raw = signObj(h.t, key, typ, m, sigMember)
	}
	if post != nil {
		o := decode(h.t, raw)
		post(o)
		raw = canon(h.t, o)
	}
	if rawPost != nil {
		raw = rawPost(raw)
	}
	return raw
}

// baseline installs a valid revision 40 carrying one good leaf, which is the state every
// rejection row must find untouched afterwards.
func (h *harness) baseline() (Result, map[string]any) {
	h.t.Helper()
	leaf := h.offer(stationA, "offer-1", offerSpec{})
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{revision: 40, leaves: []map[string]any{leaf}}))
	require.NoError(h.t, err)
	require.Equal(h.t, 1, res.Routable)
	return res, leaf
}

func signObj(t *testing.T, priv ed25519.PrivateKey, typ string, m map[string]any, sigM string) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	out, err := towerobj.Sign(priv, PublicNetwork, typ, Version, b, sigM)
	require.NoError(t, err)
	return out
}

func canon(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	c, err := towerobj.Canonical(b)
	require.NoError(t, err)
	return c
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// --- the happy path ---------------------------------------------------------

func TestAValidFullInventoryBecomesRoutingCandidates(t *testing.T) {
	h := newHarness(t)
	leaf := h.offer(stationA, "offer-1", offerSpec{})
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{revision: 7, leaves: []map[string]any{leaf}}))
	require.NoError(t, err)
	require.Equal(t, int64(7), res.Revision)
	require.Equal(t, 1, res.Routable)
	require.Empty(t, res.Excluded)
	require.True(t, res.Full)

	routable := h.set.Routable(towerA)
	require.Len(t, routable, 1)
	got := routable[0]
	require.Equal(t, stationA, got.StationID)
	require.Equal(t, towerA, got.TowerID, "each leaf is recorded with the Tower it came from")
	require.Equal(t, int64(1000), got.PriceIn)
	require.Equal(t, []string{"chat"}, got.Capabilities)

	// The exact signed offer, not our re-rendering of it: settlement has to be able to point
	// at the object the Station actually signed.
	require.Equal(t, canon(t, leaf), got.Offer)
	wantHash, err := towerobj.Hash(got.Offer)
	require.NoError(t, err)
	require.Equal(t, wantHash, got.OfferHash)
}

func TestTheAcceptedHeadIsPublishedForReconnects(t *testing.T) {
	// The whole reconnect economy rests on this: towerlink hands the head back, and a Tower
	// whose fleet has not changed answers with ~100 bytes instead of a full snapshot.
	h := newHarness(t)
	res, _ := h.baseline()

	rev, hash, ok := h.set.Head(towerA)
	require.True(t, ok)
	require.Equal(t, int64(40), rev)
	require.Equal(t, res.Hash, hash)

	require.Equal(t, []recordedHead{{towerA, 40, res.Hash}}, h.heads)

	_, _, ok = h.set.Head("tower-nobody")
	require.False(t, ok)
	require.Nil(t, h.set.Routable("tower-nobody"))
}

func TestTheHeadHashIsTheCompleteSignedObject(t *testing.T) {
	// Hashing the object WITH its signature is what lets the next revision bind "this exact
	// signed thing" rather than "something that says the same".
	h := newHarness(t)
	leaf := h.offer(stationA, "offer-1", offerSpec{})
	raw := h.inventory(invSpec{revision: 1, leaves: []map[string]any{leaf}})
	res, err := h.set.AcceptFull(towerA, h.towerPub(), raw)
	require.NoError(t, err)

	want, err := towerobj.Hash(raw)
	require.NoError(t, err)
	require.Equal(t, want, res.Hash)
}

func TestAChainedFullRevisionIsAccepted(t *testing.T) {
	h := newHarness(t)
	first, _ := h.baseline()

	leaf := h.offer(stationA, "offer-2", offerSpec{})
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 41, prevHash: first.Hash, leaves: []map[string]any{leaf},
	}))
	require.NoError(t, err)
	require.Equal(t, int64(41), res.Revision)
	require.Len(t, h.set.Routable(towerA), 1)
	require.Equal(t, "offer-2", h.set.Routable(towerA)[0].OfferID)
}

func TestAColdStartTakesTheChainOnFaithExactlyOnce(t *testing.T) {
	// After a resync we have no history to compare against, so the first snapshot is
	// accepted at whatever revision the Tower is on. Everything after it is chained - which
	// this asserts by showing the NEXT revision is no longer free.
	h := newHarness(t)
	leaf := h.offer(stationA, "offer-1", offerSpec{})
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 900, prevHash: "whatever-they-say", leaves: []map[string]any{leaf},
	}))
	require.NoError(t, err)

	_, err = h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 901, prevHash: "whatever-they-say", leaves: []map[string]any{leaf},
	}))
	require.ErrorIs(t, err, ErrRejected)
}

// --- Scenario Outline: Invalid Tower inventory is rejected atomically -------

func TestInvalidTowerInventoryIsRejectedAtomically(t *testing.T) {
	// Every row asserts the same two things the spec asks for: none of the revision becomes
	// routable, and the last fully accepted revision stays authoritative. The second half
	// matters as much as the first - if a malformed push blanked the Tower, sending one
	// would be a way to knock a competitor's fleet out of routing.
	rows := []struct {
		defect string
		// want is the control this row must reach. Asserting only that it was rejected
		// would let a row pass on a check further up, which is exactly how three controls
		// in an earlier suite were never exercised.
		want string
		spec func(h *harness, base Result, good map[string]any) invSpec
	}{
		{"no Tower signature", "carries no sig", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, unsigned: true, leaves: []map[string]any{g}}
		}},
		{"a Tower signature by another Tower", "signature does not verify", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, signer: h.otherKey, leaves: []map[string]any{g}}
		}},
		{"a Tower ID different from the channel identity", "is not the channel identity", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{g},
				pre: func(m map[string]any) { m["tower_id"] = towerB }}
		}},
		{"an invalid canonical encoding", "not in canonical form", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{g},
				rawPost: func(raw []byte) []byte {
					// Parseable, same meaning, different bytes - and therefore a different
					// hash from the one the chain would bind.
					return []byte(strings.Replace(string(raw), `{"`, `{ "`, 1))
				}}
		}},
		{"an unknown required field", `unknown member "region"`, func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{g},
				pre: func(m map[string]any) { m["region"] = "us-east" }}
		}},
		{"a missing required field", `missing required member "lifecycle_head"`, func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{g},
				pre: func(m map[string]any) { delete(m, "lifecycle_head") }}
		}},
		{"a duplicate Station ID", "appears twice", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{
				h.offer(stationA, "offer-1", offerSpec{}),
				h.offer(stationA, "offer-2", offerSpec{}),
			}}
		}},
		{"a duplicate offer ID", "offer offer-1 appears twice", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{
				h.offer(stationA, "offer-1", offerSpec{}),
				h.offer(stationB, "offer-1", offerSpec{}),
			}}
		}},
		{"a revision equal to the accepted revision", "does not advance on the accepted 40", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 40, prevHash: b.Hash, leaves: []map[string]any{g}}
		}},
		{"a revision lower than the accepted revision", "does not advance on the accepted 40", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 39, prevHash: b.Hash, leaves: []map[string]any{g}}
		}},
		{"a revision that skips the accepted revision plus one", "skips 41", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 42, prevHash: b.Hash, leaves: []map[string]any{g}}
		}},
		{"a previous hash other than the accepted current head", "prev_hash is not the accepted head", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: "not-the-head", leaves: []map[string]any{g}}
		}},
		{"a revision that overflows the sequence", "overflows the sequence", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: math.MaxInt64, prevHash: b.Hash, leaves: []map[string]any{g}}
		}},
		{"an issued time in the future beyond skew", "beyond the allowed skew", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, issued: 10 * time.Minute, leaves: []map[string]any{g}}
		}},
		{"an expired inventory", "already expired", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, expires: -time.Minute, leaves: []map[string]any{g}}
		}},
		{"an expiry beyond the allowed lease", "beyond the allowed lease", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, expires: 2 * time.Hour, leaves: []map[string]any{g}}
		}},
		{"a public network ID mismatch", `network "roger-private" is not`, func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{g},
				pre: func(m map[string]any) { m["network"] = "roger-private" }}
		}},
		{"a capability count above the Tower's limit", "distinct capabilities is above the limit", func(h *harness, b Result, g map[string]any) invSpec {
			wide := h.offer(stationA, "offer-1", offerSpec{pre: func(m map[string]any) {
				m["capabilities"] = []any{"chat", "vision", "tools"}
			}})
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{wide}}
		}},
		{"total encoded bytes above the inventory limit", "encoded bytes is above the limit", func(h *harness, b Result, g map[string]any) invSpec {
			return invSpec{revision: 41, prevHash: b.Hash, leaves: []map[string]any{g},
				pre: func(m map[string]any) { m["lease_head"] = strings.Repeat("x", 5000) }}
		}},
	}

	for _, row := range rows {
		t.Run(row.defect, func(t *testing.T) {
			h := newHarness(t)
			base, good := h.baseline()
			before := h.set.Routable(towerA)

			_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(row.spec(h, base, good)))
			require.ErrorIs(t, err, ErrRejected, "defect %q must reject the whole revision", row.defect)
			require.Contains(t, err.Error(), row.want,
				"defect %q was caught by a different check than the one it names", row.defect)

			rev, hash, ok := h.set.Head(towerA)
			require.True(t, ok)
			require.Equal(t, int64(40), rev, "the accepted revision must not move")
			require.Equal(t, base.Hash, hash)
			require.Equal(t, before, h.set.Routable(towerA), "the last accepted revision stays authoritative")
			require.Len(t, h.heads, 1, "a rejected revision publishes no head")
		})
	}
}

func TestARejectedRevisionCanBeFollowedByACorrectedOne(t *testing.T) {
	// The rejection is of the revision, not of the Tower. An operator who fixes their push
	// must be able to land it without a reconnect.
	h := newHarness(t)
	base, good := h.baseline()

	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 41, prevHash: "wrong", leaves: []map[string]any{good},
	}))
	require.ErrorIs(t, err, ErrRejected)

	_, err = h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 41, prevHash: base.Hash, leaves: []map[string]any{good},
	}))
	require.NoError(t, err)
}

// --- Scenario Outline: An invalid leaf is excluded --------------------------

func TestAnInvalidLeafIsExcludedWithoutSinkingTheRevision(t *testing.T) {
	// A bad leaf is dropped; the rest of the fleet keeps earning. Failing the whole
	// revision instead would hand any single misbehaving Station a way to take an entire
	// operator offline.
	rows := []struct {
		defect string
		// want, as in the inventory table, pins the row to the control it names.
		want string
		reg  func(*Registration)
		spec func(h *harness) offerSpec
	}{
		{defect: "no Station signature", want: "carries no station_sig", spec: func(h *harness) offerSpec {
			return offerSpec{unsigned: true}
		}},
		{defect: "a Station signature by another key", want: "signature does not verify", spec: func(h *harness) offerSpec {
			return offerSpec{signer: h.stationKe[stationB]}
		}},
		{defect: "Station ID inconsistent with its registered key", want: "not consistent with any registered key", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["station_id"] = "station-unknown" }}
		}},
		{defect: "owner missing for public admission", want: "has no owner", reg: func(r *Registration) { r.OwnerPresent = false }},
		{defect: "suspended owner", want: "owner is suspended", reg: func(r *Registration) { r.OwnerSuspended = true }},
		{defect: "banned Station", want: "is banned", reg: func(r *Registration) { r.Banned = true }},
		{defect: "key revoked", want: "key is revoked", reg: func(r *Registration) { r.KeyRevoked = true }},
		{defect: "unsupported model", want: "not supported on the public network", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["model"] = "roger-unreleased" }}
		}},
		{defect: "price below the public floor", want: "below the public floor", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["price_in"] = "1" }}
		}},
		{defect: "price above the public ceiling", want: "above the public ceiling", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["price_out"] = "99999" }}
		}},
		{defect: "Station-earning rate above the matching consumer rate", want: "above the matching consumer rate", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["earn_in"] = "1001" }}
		}},
		{defect: "a negative price", want: "price_in is negative", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["price_in"] = "-1" }}
		}},
		{defect: "a non-finite price", want: `member "price_in"`, spec: func(h *harness) offerSpec {
			// There are no JSON numbers in this format, so an infinity or a float is simply
			// not a well-formed integer string. That is the point of banning numbers.
			return offerSpec{pre: func(m map[string]any) { m["price_in"] = "1.5" }}
		}},
		{defect: "zero capacity", want: "capacity is not positive", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["capacity"] = "0" }}
		}},
		{defect: "negative capacity", want: "capacity is not positive", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["capacity"] = "-4" }}
		}},
		{defect: "unsupported modality", want: `modality "hologram" is not supported`, spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["modality"] = "hologram" }}
		}},
		{defect: "expired offer", want: "the offer has expired", spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["expires"] = h.unix(-time.Minute) }}
		}},
		{defect: "capabilities omitted from the signed leaf bytes", want: "Station signature", spec: func(h *harness) offerSpec {
			// The Station signed a leaf with no capabilities; the Tower added them on the way
			// through. The signature is what catches it.
			return offerSpec{
				pre:  func(m map[string]any) { delete(m, "capabilities") },
				post: func(m map[string]any) { m["capabilities"] = []any{"tools"} },
			}
		}},
		{defect: "offer bound to a different Tower", want: `bound to Tower "tower-b"`, spec: func(h *harness) offerSpec {
			return offerSpec{pre: func(m map[string]any) { m["tower_id"] = towerB }}
		}},
		{defect: "an unknown member on the leaf", want: `unknown member "gpu"`, spec: func(h *harness) offerSpec {
			// The closed schema is how an operator-declared claim is kept from riding along
			// and later being read as something we measured.
			return offerSpec{pre: func(m map[string]any) { m["gpu"] = "H100" }}
		}},
	}

	for _, row := range rows {
		t.Run(row.defect, func(t *testing.T) {
			h := newHarness(t)
			if row.reg != nil {
				reg := h.pol.stations[stationA]
				row.reg(&reg)
				h.pol.stations[stationA] = reg
			}
			spec := offerSpec{}
			if row.spec != nil {
				spec = row.spec(h)
			}

			bad := h.offer(stationA, "offer-bad", spec)
			good := h.offer(stationB, "offer-good", offerSpec{})

			res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
				revision: 1, leaves: []map[string]any{bad, good},
			}))
			require.NoError(t, err, "one bad leaf must not sink the revision")
			require.Equal(t, 1, res.Routable)
			require.Len(t, res.Excluded, 1)
			require.Equal(t, "offer-bad", res.Excluded[0].OfferID)
			require.Contains(t, res.Excluded[0].Reason, row.want,
				"defect %q was caught by a different check than the one it names", row.defect)

			routable := h.set.Routable(towerA)
			require.Len(t, routable, 1)
			require.Equal(t, "offer-good", routable[0].OfferID,
				"defect %q left the wrong leaf routable", row.defect)
		})
	}
}

func TestALeafOnAForeignNetworkIsExcluded(t *testing.T) {
	h := newHarness(t)
	bad := h.offer(stationA, "offer-bad", offerSpec{pre: func(m map[string]any) {
		m["network"] = "roger-private"
	}})
	good := h.offer(stationB, "offer-good", offerSpec{})
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{leaves: []map[string]any{bad, good}}))
	require.NoError(t, err)
	require.Equal(t, 1, res.Routable)
	require.Contains(t, res.Excluded[0].Reason, "network")
}

func TestALeafWithoutIdentityRejectsTheRevision(t *testing.T) {
	// Identity is the one leaf-level defect that IS fatal to the revision: without a
	// Station or offer ID there is no way to check uniqueness, so the revision cannot be
	// shown to be unambiguous at all.
	h := newHarness(t)
	bad := h.offer(stationA, "offer-1", offerSpec{pre: func(m map[string]any) { delete(m, "offer_id") }})
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{leaves: []map[string]any{bad}}))
	require.ErrorIs(t, err, ErrRejected)
}

func TestALeafThatIsNotAnObjectRejectsTheRevision(t *testing.T) {
	// The substitution happens BEFORE signing. Doing it after would only prove the
	// signature check works, which is a different test - this one has to reach the leaf
	// walk itself.
	h := newHarness(t)
	raw := h.inventory(invSpec{
		pre: func(m map[string]any) { m["leaves"] = []any{"not-a-leaf"} },
	})
	_, err := h.set.AcceptFull(towerA, h.towerPub(), raw)
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "leaf 0 is not an object")
}

func TestMalformedInventoryMembersAreRejected(t *testing.T) {
	// The shape checks the happy path never reaches. Each one is a way for a Tower to send
	// something that parses as canonical JSON but is not an inventory.
	rows := []struct {
		name string
		want string
		pre  func(map[string]any)
	}{
		{"a missing network", `missing required member "network"`, func(m map[string]any) { delete(m, "network") }},
		{"a missing tower ID", `missing required member "tower_id"`, func(m map[string]any) { delete(m, "tower_id") }},
		{"a missing lease head", `missing required member "lease_head"`, func(m map[string]any) { delete(m, "lease_head") }},
		{"a missing issued time", `missing required member "issued"`, func(m map[string]any) { delete(m, "issued") }},
		{"a missing expiry", `missing required member "expires"`, func(m map[string]any) { delete(m, "expires") }},
		{"a missing leaves array", `missing required member "leaves"`, func(m map[string]any) { delete(m, "leaves") }},
		{"a non-string lease head", `member "lease_head" is not a string`, func(m map[string]any) { m["lease_head"] = []any{"a"} }},
		{"an empty lease head", `member "lease_head" is empty`, func(m map[string]any) { m["lease_head"] = "" }},
		{"a non-array leaves member", `member "leaves" is not an array`, func(m map[string]any) { m["leaves"] = "none" }},
		{"a non-integer revision", `member "revision"`, func(m map[string]any) { m["revision"] = "forty-one" }},
		{"a padded revision", `member "revision"`, func(m map[string]any) { m["revision"] = "041" }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newHarness(t)
			_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{pre: row.pre}))
			require.ErrorIs(t, err, ErrRejected)
			require.Contains(t, err.Error(), row.want)
		})
	}
}

func TestAFullSnapshotMissingItsChainLinkIsRejected(t *testing.T) {
	// prev_hash is only required once there is a head to chain from - and then it is not
	// optional, or a Tower could opt out of the chain by omission.
	h := newHarness(t)
	h.baseline()
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 41, leaves: []map[string]any{h.offer(stationA, "offer-1", offerSpec{})},
		pre: func(m map[string]any) { delete(m, "prev_hash") },
	}))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), `missing required member "prev_hash"`)

	// And on a cold start too, where its value cannot be checked. Found by the pre-push
	// audit: the member was read only inside the chained branch, so the very first snapshot
	// from a Tower could omit it entirely and the closed schema quietly stopped being
	// complete.
	fresh := newHarness(t)
	_, err = fresh.set.AcceptFull(towerA, fresh.towerPub(), fresh.inventory(invSpec{
		pre: func(m map[string]any) { delete(m, "prev_hash") },
	}))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), `missing required member "prev_hash"`)
}

func TestADuplicateOfferHidingBehindAnExcludedLeafStillSinksTheRevision(t *testing.T) {
	// Found by the pre-push audit. Uniqueness was checked against the ADMITTED leaves, so a
	// first occurrence that got excluded never registered - and the second claim on the
	// same offer ID sailed through as unique. The revision then carried two claims on one
	// offer while the exclusion list simultaneously reported that offer as dropped.
	h := newHarness(t)
	banned := h.pol.stations[stationA]
	banned.Banned = true
	h.pol.stations[stationA] = banned

	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{leaves: []map[string]any{
		h.offer(stationA, "offer-x", offerSpec{}), // excluded: banned Station
		h.offer(stationB, "offer-x", offerSpec{}), // and yet the offer ID is taken
	}}))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "offer offer-x appears twice")
	require.Nil(t, h.set.Routable(towerA))
}

func TestMalformedLeafMembersExcludeTheLeaf(t *testing.T) {
	// Same idea one level down, and the consequence is different: a malformed leaf is
	// dropped, not fatal, because one bad Station must not take an operator offline.
	rows := []struct {
		name string
		want string
		pre  func(m map[string]any)
	}{
		{"a missing network", `missing required member "network"`, func(m map[string]any) { delete(m, "network") }},
		{"a missing tower ID", `missing required member "tower_id"`, func(m map[string]any) { delete(m, "tower_id") }},
		{"a missing model", `missing required member "model"`, func(m map[string]any) { delete(m, "model") }},
		{"a missing modality", `missing required member "modality"`, func(m map[string]any) { delete(m, "modality") }},
		{"a missing rate", `missing required member "earn_out"`, func(m map[string]any) { delete(m, "earn_out") }},
		{"a missing capacity", `missing required member "capacity"`, func(m map[string]any) { delete(m, "capacity") }},
		{"missing capabilities", `missing required member "capabilities"`, func(m map[string]any) { delete(m, "capabilities") }},
		{"a missing expiry", `missing required member "expires"`, func(m map[string]any) { delete(m, "expires") }},
		{"a non-integer expiry", `member "expires"`, func(m map[string]any) { m["expires"] = "soon" }},
		{"non-array capabilities", `member "capabilities" is not an array`, func(m map[string]any) { m["capabilities"] = "chat" }},
		{"a non-string capability", `member "capabilities" element 0`, func(m map[string]any) { m["capabilities"] = []any{true} }},
		{"an empty capability", `member "capabilities" element 0`, func(m map[string]any) { m["capabilities"] = []any{""} }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newHarness(t)
			bad := h.offer(stationA, "offer-bad", offerSpec{pre: row.pre})
			good := h.offer(stationB, "offer-good", offerSpec{})
			res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{leaves: []map[string]any{bad, good}}))
			require.NoError(t, err)
			require.Equal(t, 1, res.Routable)
			require.Contains(t, res.Excluded[0].Reason, row.want)
		})
	}
}

func TestTheLeafCeilingIsEnforced(t *testing.T) {
	h := newHarness(t)
	h.set.cfg.MaxLeaves = 1
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{leaves: []map[string]any{
		h.offer(stationA, "offer-1", offerSpec{}),
		h.offer(stationB, "offer-2", offerSpec{}),
	}}))
	require.ErrorIs(t, err, ErrRejected)
}

func TestAModelWithNoPublicBandIsNotRoutableAtAnyPrice(t *testing.T) {
	h := newHarness(t)
	h.pol.models["roger-bandless"] = true
	bad := h.offer(stationA, "offer-bad", offerSpec{pre: func(m map[string]any) { m["model"] = "roger-bandless" }})
	good := h.offer(stationB, "offer-good", offerSpec{})
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{leaves: []map[string]any{bad, good}}))
	require.NoError(t, err)
	require.Equal(t, 1, res.Routable)
	require.Contains(t, res.Excluded[0].Reason, "price band")
}

// --- one Station, one active origin ----------------------------------------

func TestAStationBehindTwoTowersDoesNotGetCountedTwice(t *testing.T) {
	// The cheapest way to oversell a fleet is to advertise the same machine behind two
	// Towers. Both inventories are perfectly signed; only the first origin holds.
	h := newHarness(t)
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		leaves: []map[string]any{h.offer(stationA, "offer-1", offerSpec{})},
	}))
	require.NoError(t, err)

	viaB := h.offer(stationA, "offer-2", offerSpec{pre: func(m map[string]any) { m["tower_id"] = towerB }})
	rawB := h.finish(map[string]any{
		"network": PublicNetwork, "tower_id": towerB, "revision": "1",
		"prev_hash": "genesis", "lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued": h.unix(0), "expires": h.unix(30 * time.Minute),
		"leaves": []any{viaB},
	}, TypeInventory, nil, nil, nil, h.towerKey, false)

	res, err := h.set.AcceptFull(towerB, h.towerPub(), rawB)
	require.NoError(t, err)
	require.Zero(t, res.Routable)
	require.Contains(t, res.Excluded[0].Reason, "already active behind")
	require.Len(t, h.set.Routable(towerA), 1, "the first origin keeps the Station")
}

func TestForgettingATowerReleasesItsStations(t *testing.T) {
	h := newHarness(t)
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		leaves: []map[string]any{h.offer(stationA, "offer-1", offerSpec{})},
	}))
	require.NoError(t, err)
	h.set.Forget(towerA)

	_, _, ok := h.set.Head(towerA)
	require.False(t, ok)
	require.Nil(t, h.set.Routable(towerA))

	viaB := h.offer(stationA, "offer-2", offerSpec{pre: func(m map[string]any) { m["tower_id"] = towerB }})
	rawB := h.finish(map[string]any{
		"network": PublicNetwork, "tower_id": towerB, "revision": "1",
		"prev_hash": "genesis", "lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued": h.unix(0), "expires": h.unix(30 * time.Minute),
		"leaves": []any{viaB},
	}, TypeInventory, nil, nil, nil, h.towerKey, false)
	res, err := h.set.AcceptFull(towerB, h.towerPub(), rawB)
	require.NoError(t, err)
	require.Equal(t, 1, res.Routable, "a released Station may attach elsewhere")
}

func TestAnOperatorMayDropAndReaddTheirOwnStation(t *testing.T) {
	// The origin index must not fence a Tower against itself, or removing an offer would
	// permanently retire the Station behind it.
	h := newHarness(t)
	first, _ := h.baseline()
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 41, prevHash: first.Hash, leaves: []map[string]any{},
	}))
	require.NoError(t, err)
	second, _, _ := h.set.Head(towerA)
	require.Equal(t, int64(41), second)

	_, hash, _ := h.set.Head(towerA)
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 42, prevHash: hash, leaves: []map[string]any{h.offer(stationA, "offer-9", offerSpec{})},
	}))
	require.NoError(t, err)
	require.Equal(t, 1, res.Routable)
}

// --- expiry -----------------------------------------------------------------

func TestInventoryExpiryRemovesEveryStaleLeafFromNewRouting(t *testing.T) {
	// Nothing polls and nothing has to notice a disconnect: the object says how long it is
	// good for, and routing simply stops seeing it.
	h := newHarness(t)
	h.baseline()
	require.Len(t, h.set.Routable(towerA), 1)

	h.now = h.now.Add(31 * time.Minute)
	require.Empty(t, h.set.Routable(towerA), "no leaf from an expired inventory receives new work")

	// The revision is still recorded, because its head is what a later delta must chain from.
	rev, _, ok := h.set.Head(towerA)
	require.True(t, ok)
	require.Equal(t, int64(40), rev)
}

func TestAnOfferExpiringBeforeItsInventoryLeavesRoutingAlone(t *testing.T) {
	h := newHarness(t)
	short := h.offer(stationA, "offer-short", offerSpec{pre: func(m map[string]any) {
		m["expires"] = h.unix(5 * time.Minute)
	}})
	long := h.offer(stationB, "offer-long", offerSpec{})
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{leaves: []map[string]any{short, long}}))
	require.NoError(t, err)
	require.Len(t, h.set.Routable(towerA), 2)

	h.now = h.now.Add(6 * time.Minute)
	routable := h.set.Routable(towerA)
	require.Len(t, routable, 1)
	require.Equal(t, "offer-long", routable[0].OfferID)
}

func TestNoOtherTowerCanRefreshAnExpiredInventory(t *testing.T) {
	// "No heartbeat fabricated by another Tower refreshes it" - refreshing means producing a
	// newer revision signed by THAT Tower's key, for THAT Tower's channel identity, and
	// there is no path from one Tower's channel to another Tower's inventory.
	h := newHarness(t)
	h.baseline()
	h.now = h.now.Add(31 * time.Minute)

	refresh := h.inventory(invSpec{revision: 41, leaves: []map[string]any{h.offer(stationA, "offer-1", offerSpec{})}})
	_, err := h.set.AcceptFull(towerB, h.towerPub(), refresh)
	require.ErrorIs(t, err, ErrRejected, "the object names Tower A, so Tower B's channel cannot carry it")
	require.Empty(t, h.set.Routable(towerA))
}

// --- defaults ---------------------------------------------------------------

func TestAZeroConfigStillHasEveryBound(t *testing.T) {
	// A zero Config must be safe: a missing ceiling is an unbounded one, and this package's
	// ceilings are the only thing standing between one Tower and every instance's memory.
	s := New(Config{}, &fakePolicy{})
	require.Equal(t, PublicNetwork, s.cfg.Network)
	require.Positive(t, s.cfg.Skew)
	require.Positive(t, s.cfg.MaxLifetime)
	require.Equal(t, 10000, s.cfg.MaxLeaves)
	require.Positive(t, s.cfg.MaxCapabilities)
	require.Positive(t, s.cfg.MaxBytes)
	require.NotNil(t, s.cfg.Now)
}

func TestAnAcceptedRevisionWithoutARecordHeadHookIsFine(t *testing.T) {
	h := newHarness(t)
	h.set.cfg.RecordHead = nil
	_, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		leaves: []map[string]any{h.offer(stationA, "offer-1", offerSpec{})},
	}))
	require.NoError(t, err)
}

func TestGarbageBytesAreRejectedNotPanicked(t *testing.T) {
	h := newHarness(t)
	for _, raw := range [][]byte{nil, []byte("{"), []byte(`"a string"`), []byte(`{"a":1}`), []byte(`{"a":null}`)} {
		_, err := h.set.AcceptFull(towerA, h.towerPub(), raw)
		require.ErrorIs(t, err, ErrRejected)
	}
}

func TestAnOversizedObjectIsRefusedBeforeItIsParsed(t *testing.T) {
	// The byte ceiling has to come first, or the defence against a Tower pushing arbitrary
	// memory is a defence that runs after we have already allocated it.
	h := newHarness(t)
	_, err := h.set.AcceptFull(towerA, h.towerPub(), []byte(strings.Repeat("x", 5000)))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "above the limit")
}
