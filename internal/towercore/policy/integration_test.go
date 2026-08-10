package policy

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/inv"
	"rogerai.fm/roger/v5/internal/towerobj"
)

// THE CHAIN, END TO END, FOR THE FIRST TIME.
//
// Until this test the Tower pieces were three packages that had never met. towerinv could
// validate an inventory but had no Policy; towerpolicy could answer questions but nothing
// asked them; stationattach could record a Station but nothing read it. Each was green on
// its own, which is precisely the state in which an integration defect hides.
//
// Here a real Station is attached through the real registry, signs a real offer with the
// assertion key that attachment recorded, a Tower relays it in a real signed inventory, and
// towerinv admits it through the real Policy. Then the same leaf is refused for each of the
// central reasons - banned Station, banned owner, suspended owner, revoked key, unknown
// Station - proving the seam carries refusals as well as approvals.
//
// A compile-time assertion that *Policy satisfies inv.Policy is not enough: an
// implementation can satisfy an interface and still answer every question wrongly.
var _ inv.Policy = (*Policy)(nil)

const (
	netName = "roger-public"
	towerID = "tw-1"
)

type chain struct {
	t         *testing.T
	now       time.Time
	reg       *attach.Registry
	bans      *fakeBans
	owners    *fakeOwners
	policy    *Policy
	set       *inv.Set
	towerPriv ed25519.PrivateKey
	stPriv    ed25519.PrivateKey
	stationID string
}

func newChain(t *testing.T) *chain {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()

	// 1. A Station attaches, for real.
	astore := attach.NewMemStore()
	reg := attach.New(attach.Config{
		Network: netName, Now: func() time.Time { return now },
	}, astore)

	stPub, stPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, sessPub, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_ = sessPub
	sessKey := hex.EncodeToString([]byte("secure-session-key-distinct"))
	assertionKey := hex.EncodeToString(stPub)

	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: "auth-1", Network: netName, StationID: "st-1", Owner: ownerPub,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: assertionKey, SessionKey: sessKey,
	}, time.Hour, now.Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, astore.PutAuthorization(auth))
	_, err = reg.Admit(attach.Proof{
		AuthID: "auth-1", Secret: secret, Network: netName, StationID: "st-1", Owner: ownerPub,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: assertionKey, SessionKey: sessKey,
	})
	require.NoError(t, err)

	// 2. The real Policy over that registry.
	bans := &fakeBans{owners: map[string]string{}, nodes: map[string]string{}}
	owners := &fakeOwners{ok: true}
	pol := New(reg, bans, owners, Config{
		ModelAllowed:    func(m string) bool { return m == "roger-1" },
		ModalityAllowed: func(m string) bool { return m == "text" },
		PriceBand:       func(string) (int64, int64, bool) { return 100, 10_000, true },
		Now:             func() time.Time { return now },
		BanRefresh:      time.Millisecond, // so a ban in a test takes effect on the next read
	})

	// 3. The real inventory set, using that Policy.
	set := inv.New(inv.Config{
		Network: netName, Now: func() time.Time { return now },
	}, pol)

	_, towerPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	return &chain{t: t, now: now, reg: reg, bans: bans, owners: owners,
		policy: pol, set: set, towerPriv: towerPriv, stPriv: stPriv, stationID: "st-1"}
}

// inventory builds a genuinely signed Tower inventory carrying one Station-signed leaf.
func (c *chain) inventory() []byte {
	c.t.Helper()
	leaf := map[string]any{
		"network": netName, "tower_id": towerID, "station_id": c.stationID,
		"offer_id": "offer-1", "model": "roger-1", "modality": "text",
		"price_in": "1000", "price_out": "2000", "earn_in": "800", "earn_out": "1600",
		"capacity": "4", "capabilities": []any{"chat"},
		"expires": towerobj.FormatInt(c.now.Add(30 * time.Minute).Unix()),
	}
	raw, err := json.Marshal(leaf)
	require.NoError(c.t, err)
	signedLeaf, err := towerobj.Sign(c.stPriv, netName, inv.TypeOffer, inv.Version, raw, "station_sig")
	require.NoError(c.t, err)
	var leafObj map[string]any
	require.NoError(c.t, json.Unmarshal(signedLeaf, &leafObj))

	invObj := map[string]any{
		"network": netName, "tower_id": towerID, "revision": "1", "prev_hash": "genesis",
		"lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued":  towerobj.FormatInt(c.now.Unix()),
		"expires": towerobj.FormatInt(c.now.Add(30 * time.Minute).Unix()),
		"leaves":  []any{leafObj},
	}
	rawInv, err := json.Marshal(invObj)
	require.NoError(c.t, err)
	signedInv, err := towerobj.Sign(c.towerPriv, netName, inv.TypeInventory, inv.Version, rawInv, "sig")
	require.NoError(c.t, err)
	return signedInv
}

func (c *chain) accept() (inv.Result, error) {
	return c.set.AcceptFull(towerID, c.towerPriv.Public().(ed25519.PublicKey), c.inventory())
}

func TestAnAttachedStationsOfferBecomesRoutable(t *testing.T) {
	c := newChain(t)
	c.owners.owner, c.owners.ok = Owner{}, true

	res, err := c.accept()
	require.NoError(t, err)
	require.Equal(t, 1, res.Routable,
		"an attached Station's signed offer, relayed by its Tower, must be routable: %+v", res.Excluded)
	require.Empty(t, res.Excluded)

	routable := c.set.Routable(towerID)
	require.Len(t, routable, 1)
	require.Equal(t, c.stationID, routable[0].StationID)
	require.Equal(t, int64(1000), routable[0].PriceIn)
}

// Central state overrides a cryptographically perfect offer - the reason Policy exists.
func TestCentralStateRefusesAPerfectlySignedOffer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		arm   func(c *chain)
		wants string
	}{
		{"the Station is banned", func(c *chain) {
			c.bans.nodes = map[string]string{"st-1": "abuse"}
		}, "banned"},
		{"the owner is banned", func(c *chain) {
			c.bans.owners = map[string]string{ownerPub: "chargebacks"}
		}, "banned"},
		{"the owner is suspended", func(c *chain) {
			c.owners.owner = Owner{Suspended: true}
		}, "suspended"},
		{"the Station's key was revoked", func(c *chain) {
			_, err := c.reg.Revoke("st-1")
			require.NoError(c.t, err)
		}, "revoked"},
		// Refused, but for an HONEST reason: Core could not read its own state. Telling the
		// operator their Station is unregistered would send them to re-attach a healthy one.
		{"the ban set cannot be read", func(c *chain) {
			c.bans.oerr = errors.New("db down")
		}, "temporarily unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newChain(t)
			tc.arm(c)

			res, err := c.accept()
			require.NoError(t, err, "one refused leaf must not sink the whole revision")
			require.Zero(t, res.Routable, "the offer must not be routable")
			require.Len(t, res.Excluded, 1)
			require.Contains(t, res.Excluded[0].Reason, tc.wants,
				"the exclusion must name the reason it was actually refused for")
			require.Empty(t, c.set.Routable(towerID))
		})
	}
}

// A Station nobody attached is the case the whole registry exists for: the Tower signs the
// collection perfectly, and the leaf still cannot be believed.
func TestAnUnattachedStationIsNeverRoutable(t *testing.T) {
	c := newChain(t)
	c.stationID = "st-never-attached"

	res, err := c.accept()
	require.NoError(t, err)
	require.Zero(t, res.Routable)
	require.Len(t, res.Excluded, 1)
	require.Contains(t, res.Excluded[0].Reason, "not consistent with any registered key")
}

// The Tower relays the offer; it does not get to sign it. A leaf signed by the TOWER's key
// rather than the Station's is refused even though the Tower is legitimate.
func TestATowerCannotSignItsStationsOffer(t *testing.T) {
	c := newChain(t)
	c.stPriv = c.towerPriv // the Tower signs the leaf itself

	res, err := c.accept()
	require.NoError(t, err)
	require.Zero(t, res.Routable)
	require.Len(t, res.Excluded, 1)
	require.Contains(t, res.Excluded[0].Reason, "signature")
}

// Attachment alone is not eligibility: a quarantined Station is attached and verifiable, and
// promotion is a separate, evidence-driven decision. Here it is already routable at the
// inventory layer, which is correct - quarantine is enforced by the promotion ladder, not by
// refusing to record the leaf. Pinned so a later change to that division is deliberate.
func TestQuarantineIsNotEnforcedAtTheInventoryLayer(t *testing.T) {
	c := newChain(t)
	at, ok, err := c.reg.Station("st-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, attach.StateQuarantine, at.State)

	res, err := c.accept()
	require.NoError(t, err)
	require.Equal(t, 1, res.Routable,
		"the inventory records a quarantined Station's leaf; the promotion ladder decides "+
			"whether it receives traffic")
}
