package policy

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v5/internal/towercore/attach"
)

// The property under test throughout is FAIL CLOSED.
//
// inv.Policy has no error returns, so a read that fails has nowhere to report itself.
// The zero value is safe for a bool ("not allowed") and catastrophic for a ban lookup ("not
// banned") - a database blink would re-admit every banned Station on the network at the
// exact moment nobody could see why. Each test below arranges one failure and asserts that
// nothing becomes routable because of it.

type fakeStations struct {
	at  attach.Attachment
	ok  bool
	err error
}

func (f *fakeStations) Station(string) (attach.Attachment, bool, error) {
	return f.at, f.ok, f.err
}

type fakeBans struct {
	owners, nodes map[string]string
	oerr, nerr    error
	calls         int
}

func (f *fakeBans) BannedOwners() (map[string]string, error) {
	f.calls++
	return f.owners, f.oerr
}
func (f *fakeBans) BannedNodes() (map[string]string, error) { return f.nodes, f.nerr }

type fakeOwners struct {
	owner Owner
	ok    bool
	err   error
}

func (f *fakeOwners) OwnerByPubkey(string) (Owner, bool, error) {
	return f.owner, f.ok, f.err
}

const (
	stationID = "st-1"
	ownerPub  = "owner_pub"
)

func liveAttachment(t *testing.T) (attach.Attachment, ed25519.PublicKey) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return attach.Attachment{
		StationID:    stationID,
		Owner:        ownerPub,
		AssertionKey: hex.EncodeToString(pub),
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: "tw-1"},
		State:        attach.StateQuarantine,
	}, pub
}

func newPolicy(t *testing.T, s Stations, b Bans, o Owners, mut ...func(*Config)) *Policy {
	t.Helper()
	cfg := Config{
		ModelAllowed:    func(string) bool { return true },
		ModalityAllowed: func(string) bool { return true },
		PriceBand:       func(string) (int64, int64, bool) { return 100, 10_000, true },
		Now:             func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	for _, m := range mut {
		m(&cfg)
	}
	return New(s, b, o, cfg)
}

func TestAHealthyStationIsReportedAsCoreRecordedIt(t *testing.T) {
	at, pub := liveAttachment(t)
	p := newPolicy(t,
		&fakeStations{at: at, ok: true},
		&fakeBans{owners: map[string]string{}, nodes: map[string]string{}},
		&fakeOwners{owner: Owner{}, ok: true})

	reg := p.Station(stationID)
	require.True(t, reg.Known)
	require.Equal(t, pub, reg.Key, "the key towerinv verifies against is the one Core recorded")
	require.False(t, reg.Banned)
	require.False(t, reg.KeyRevoked)
	require.True(t, reg.OwnerPresent)
	require.False(t, reg.OwnerSuspended)
}

// The heart of the package.
func TestAnUnreadableBanSetBansEverything(t *testing.T) {
	at, _ := liveAttachment(t)
	for _, tc := range []struct {
		name string
		bans *fakeBans
	}{
		{"the owner ban list fails", &fakeBans{oerr: errors.New("db down"), nodes: map[string]string{}}},
		{"the node ban list fails", &fakeBans{owners: map[string]string{}, nerr: errors.New("db down")}},
		{"there is no ban source at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b Bans
			if tc.bans != nil {
				b = tc.bans
			}
			p := newPolicy(t, &fakeStations{at: at, ok: true}, b, &fakeOwners{ok: true})

			reg := p.Station(stationID)
			require.True(t, reg.Unavailable,
				"a ban set we cannot read must refuse everything - the zero value here re-admits "+
					"every banned Station at the moment nobody can see why")
			require.False(t, reg.Known, "and nothing about it is asserted as known")
			require.False(t, reg.Banned,
				"but it must not ACCUSE the operator of a ban that did not happen")
		})
	}
}

func TestAnUnreadableOrUnknownStationIsNotKnown(t *testing.T) {
	clean := &fakeBans{owners: map[string]string{}, nodes: map[string]string{}}
	for _, tc := range []struct {
		name string
		st   *fakeStations
	}{
		{"the registry read fails", &fakeStations{err: errors.New("db down")}},
		{"the Station was never attached", &fakeStations{ok: false}},
		{"the stored key is not hex", &fakeStations{ok: true, at: attach.Attachment{
			StationID: stationID, Owner: ownerPub, AssertionKey: "not-hex"}}},
		{"the stored key is the wrong length", &fakeStations{ok: true, at: attach.Attachment{
			StationID: stationID, Owner: ownerPub, AssertionKey: hex.EncodeToString([]byte("short"))}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPolicy(t, tc.st, clean, &fakeOwners{ok: true})
			reg := p.Station(stationID)
			require.False(t, reg.Known, "nothing may be admitted on a key Core cannot produce")
			require.Nil(t, reg.Key)
		})
	}
}

func TestBansAreReportedFromEitherSet(t *testing.T) {
	at, _ := liveAttachment(t)
	t.Run("the Station is banned", func(t *testing.T) {
		p := newPolicy(t, &fakeStations{at: at, ok: true},
			&fakeBans{owners: map[string]string{}, nodes: map[string]string{stationID: "abuse"}},
			&fakeOwners{ok: true})
		require.True(t, p.Station(stationID).Banned)
	})
	t.Run("the owner is banned", func(t *testing.T) {
		p := newPolicy(t, &fakeStations{at: at, ok: true},
			&fakeBans{owners: map[string]string{ownerPub: "chargebacks"}, nodes: map[string]string{}},
			&fakeOwners{ok: true})
		require.True(t, p.Station(stationID).Banned,
			"banning an operator must reach every Station they run")
	})
}

func TestLifecycleStatesMapOntoTheFieldsTowerinvHas(t *testing.T) {
	clean := &fakeBans{owners: map[string]string{}, nodes: map[string]string{}}
	for _, tc := range []struct {
		state              string
		revoked, unroutabe bool
	}{
		{attach.StateQuarantine, false, false},
		{attach.StateActive, false, false},
		{attach.StateRevoked, true, false},
		// Detached is NOT revoked - the key is not burnt - but it is not serving either, so it
		// must not be routable. Banned is the honest mapping onto the fields available.
		{attach.StateDetached, false, true},
	} {
		t.Run(tc.state, func(t *testing.T) {
			at, _ := liveAttachment(t)
			at.State = tc.state
			p := newPolicy(t, &fakeStations{at: at, ok: true}, clean, &fakeOwners{ok: true})
			reg := p.Station(stationID)
			require.Equal(t, tc.revoked, reg.KeyRevoked)
			require.Equal(t, tc.unroutabe, reg.Banned)
		})
	}
}

func TestOwnerStateIsRefusedRatherThanAssumed(t *testing.T) {
	at, _ := liveAttachment(t)
	clean := &fakeBans{owners: map[string]string{}, nodes: map[string]string{}}

	t.Run("an unreadable owner is not present", func(t *testing.T) {
		p := newPolicy(t, &fakeStations{at: at, ok: true}, clean,
			&fakeOwners{err: errors.New("db down")})
		reg := p.Station(stationID)
		require.False(t, reg.OwnerPresent,
			"an account we cannot check is an account whose suspension we cannot see")
	})
	t.Run("a missing owner is not present", func(t *testing.T) {
		p := newPolicy(t, &fakeStations{at: at, ok: true}, clean, &fakeOwners{ok: false})
		require.False(t, p.Station(stationID).OwnerPresent)
	})
	t.Run("a suspended owner is reported", func(t *testing.T) {
		p := newPolicy(t, &fakeStations{at: at, ok: true}, clean,
			&fakeOwners{ok: true, owner: Owner{Suspended: true}})
		reg := p.Station(stationID)
		require.True(t, reg.OwnerPresent)
		require.True(t, reg.OwnerSuspended)
	})
	t.Run("no owner source configured refuses", func(t *testing.T) {
		p := newPolicy(t, &fakeStations{at: at, ok: true}, clean, nil)
		require.False(t, p.Station(stationID).OwnerPresent)
	})
}

func TestAnUnconfiguredPolicyRefusesEverything(t *testing.T) {
	// A zero Config is safe but useless. "No allow-list configured" must never read as
	// "allow anything" - that is how a misconfiguration becomes an open network.
	p := New(&fakeStations{}, &fakeBans{}, &fakeOwners{}, Config{})
	require.False(t, p.ModelAllowed("roger-1"))
	require.False(t, p.ModalityAllowed("text"))
	_, _, ok := p.PriceBand("roger-1")
	require.False(t, ok)
}

func TestAnIncoherentPriceBandAdmitsNothing(t *testing.T) {
	for _, tc := range []struct {
		name           string
		floor, ceiling int64
		ok             bool
	}{
		{"the source says no band", 0, 0, false},
		{"a negative floor", -1, 100, true},
		{"a ceiling below the floor", 500, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPolicy(t, &fakeStations{}, &fakeBans{}, &fakeOwners{}, func(c *Config) {
				c.PriceBand = func(string) (int64, int64, bool) { return tc.floor, tc.ceiling, tc.ok }
			})
			_, _, ok := p.PriceBand("roger-1")
			require.False(t, ok, "a misconfigured band must not admit an offer at any price")
		})
	}
}

// Ten thousand leaves must not become ten thousand ban queries - the relay-link design
// keeps the database off that path deliberately.
func TestTheBanSetIsCachedAcrossLeaves(t *testing.T) {
	at, _ := liveAttachment(t)
	bans := &fakeBans{owners: map[string]string{}, nodes: map[string]string{}}
	now := time.Unix(1_700_000_000, 0)
	p := newPolicy(t, &fakeStations{at: at, ok: true}, bans, &fakeOwners{ok: true},
		func(c *Config) { c.Now = func() time.Time { return now }; c.BanRefresh = time.Minute })

	for i := 0; i < 500; i++ {
		p.Station(stationID)
	}
	require.Equal(t, 1, bans.calls, "the ban set is read once per refresh window, not per leaf")
}

func TestAStaleBanSetIsRefreshed(t *testing.T) {
	at, _ := liveAttachment(t)
	bans := &fakeBans{owners: map[string]string{}, nodes: map[string]string{}}
	now := time.Unix(1_700_000_000, 0)
	p := newPolicy(t, &fakeStations{at: at, ok: true}, bans, &fakeOwners{ok: true},
		func(c *Config) { c.Now = func() time.Time { return now }; c.BanRefresh = time.Minute })

	require.False(t, p.Station(stationID).Banned)
	require.Equal(t, 1, bans.calls)

	// A ban lands, and the window passes.
	bans.nodes = map[string]string{stationID: "abuse"}
	now = now.Add(2 * time.Minute)
	require.True(t, p.Station(stationID).Banned, "a refreshed set must see the new ban")
	require.Equal(t, 2, bans.calls)
}

// A ban must not have to wait out the refresh interval.
func TestInvalidateMakesABanEffectiveImmediately(t *testing.T) {
	at, _ := liveAttachment(t)
	bans := &fakeBans{owners: map[string]string{}, nodes: map[string]string{}}
	p := newPolicy(t, &fakeStations{at: at, ok: true}, bans, &fakeOwners{ok: true},
		func(c *Config) { c.BanRefresh = time.Hour })

	require.False(t, p.Station(stationID).Banned)
	bans.nodes = map[string]string{stationID: "abuse"}
	require.False(t, p.Station(stationID).Banned, "still inside the window")

	p.Invalidate()
	require.True(t, p.Station(stationID).Banned, "a fresh ban must not wait out the cache")
}

// A recovered database ends the refusal: fail-closed must not be fail-forever.
func TestARecoveredBanSourceStopsRefusing(t *testing.T) {
	at, _ := liveAttachment(t)
	bans := &fakeBans{oerr: errors.New("db down"), nodes: map[string]string{}}
	p := newPolicy(t, &fakeStations{at: at, ok: true}, bans, &fakeOwners{ok: true})

	require.True(t, p.Station(stationID).Unavailable)

	bans.oerr, bans.owners = nil, map[string]string{}
	reg := p.Station(stationID)
	require.False(t, reg.Unavailable,
		"once the source answers again the network must come back on its own")
	require.False(t, reg.Banned)
	require.True(t, reg.Known, "and the Station is readable again")
}

func TestAnEmptyStationIDIsNotBanned(t *testing.T) {
	// An empty id reaches here only from a malformed leaf, which towerinv refuses on its own
	// grounds. It must not be treated as a ban-set miss and it must not force a refresh.
	p := newPolicy(t, &fakeStations{}, &fakeBans{}, &fakeOwners{})
	banned, ok := p.isBanned("")
	require.False(t, banned)
	require.True(t, ok)
}
