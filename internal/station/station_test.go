package station

// station_test.go is the spec for the Station's half of the joined network.
//
// A Station is the machine that actually serves work behind a Tower. It holds TWO keys and
// the Tower holds NEITHER: the assertion key signs its offers, and the secure-session key
// terminates its end of the inner channel. That separation is the whole reason a joined
// Tower can be untrusted - if the Tower could sign for a Station, "signed by the Station"
// would mean "signed by whoever is relaying", and every guarantee downstream of it would be
// worth nothing.
//
// Until this package existed there was no Station-side software at all: no way to generate
// those keys, and no way to produce a signed offer. A joined Tower therefore pushed a valid
// inventory of ZERO leaves, forever. Core's whole leaf-verification path - nineteen rejection
// rows, price bands, quarantine, origin fencing - had nothing to verify.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/inv"
	"rogerai.fm/roger/v5/internal/towerobj"
)

func TestInitMintsTwoDifferentKeysAndKeepsThemPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	s, err := Init(dir)
	require.NoError(t, err)

	require.NotEmpty(t, s.StationID)
	require.Contains(t, s.StationID, "st-")
	require.NotEmpty(t, s.AssertionPub())
	require.NotEmpty(t, s.SessionPub())

	// TWO KEYS, NOT ONE USED TWICE. attach refuses an invitation naming the same key for
	// both, and it is right to: one key doing both jobs means compromising the channel
	// hands over the ability to sign offers as well.
	require.NotEqual(t, s.AssertionPub(), s.SessionPub())

	// Private material is 0600 and never in the readable state file.
	for _, name := range []string{assertionKeyFile, sessionKeyFile} {
		info, serr := os.Stat(filepath.Join(dir, name))
		require.NoError(t, serr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE")
}

// A data directory is initialized ONCE. Re-initializing over a live Station would mint new
// keys, and the attachment Core recorded names the OLD ones - the Station would be
// cryptographically unable to prove it is itself, with no way back.
func TestInitRefusesToOverwriteAnExistingStation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := Init(dir)
	require.NoError(t, err)
	_, err = Init(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already")
}

func TestOpenReadsBackWhatInitWrote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	made, err := Init(dir)
	require.NoError(t, err)

	got, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, made.StationID, got.StationID)
	require.Equal(t, made.AssertionPub(), got.AssertionPub())
	require.Equal(t, made.SessionPub(), got.SessionPub())

	_, err = Open(filepath.Join(t.TempDir(), "nothing"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an initialized Station")
}

// THE OFFER IS THE POINT. It must verify against the Station's registered assertion key
// under Core's own verifier - not a re-implementation of it here, which would let the two
// drift and would prove nothing.
func TestASignedOfferVerifiesUnderCoresOwnVerifier(t *testing.T) {
	s := mustStation(t)
	raw, err := s.SignOffer(Offer{
		Network: "rogerai-public", TowerID: "tw-1", Model: "m1", Modality: "text",
		PriceIn: 10, PriceOut: 20, EarnIn: 5, EarnOut: 10, Capacity: 4,
		Capabilities: []string{"chat"}, TTL: 30 * time.Minute,
	}, time.Now())
	require.NoError(t, err)

	require.NoError(t, towerobj.Verify(s.AssertionPub(), "rogerai-public",
		inv.TypeOffer, inv.Version, raw, "station_sig"))
}

// The leaf carries EXACTLY the members Core's closed schema names. A missing one is a
// refusal; an extra one is also a refusal, and an extra one is the more dangerous of the
// two - it is how an unverifiable operator claim gets smuggled alongside a valid signature.
func TestTheOfferCarriesExactlyTheClosedSchema(t *testing.T) {
	s := mustStation(t)
	raw, err := s.SignOffer(validOffer(), time.Now())
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	want := map[string]bool{
		"network": true, "tower_id": true, "station_id": true, "offer_id": true,
		"model": true, "modality": true, "price_in": true, "price_out": true,
		"earn_in": true, "earn_out": true, "capacity": true, "capabilities": true,
		"expires": true, "station_sig": true,
	}
	for k := range got {
		require.True(t, want[k], "offer carries %q, which the closed schema does not name", k)
	}
	for k := range want {
		require.Contains(t, got, k)
	}
}

// NO JSON NUMBERS. Every integer travels as a bounded base-10 string, because a JSON number
// does not survive a round trip identically across languages and a canonical form that
// depends on the parser is not canonical.
func TestEveryIntegerIsAStringNotAJSONNumber(t *testing.T) {
	s := mustStation(t)
	raw, err := s.SignOffer(validOffer(), time.Now())
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	for _, k := range []string{"price_in", "price_out", "earn_in", "earn_out", "capacity", "expires"} {
		_, isString := got[k].(string)
		require.True(t, isString, "%s must be a base-10 string, got %T", k, got[k])
	}
}

// Two offers from one Station are two DIFFERENT offers. An inventory holding the same offer
// id twice is ambiguous and Core refuses the whole revision, so a Station that reused an id
// would take its own fleet off the network.
func TestEveryOfferGetsItsOwnID(t *testing.T) {
	s := mustStation(t)
	a, err := s.SignOffer(validOffer(), time.Now())
	require.NoError(t, err)
	b, err := s.SignOffer(validOffer(), time.Now())
	require.NoError(t, err)
	require.NotEqual(t, offerID(t, a), offerID(t, b))
}

// An offer is short-lived on purpose: it is how a Station that vanishes stops being offered
// without anything having to notice it went.
func TestAnOfferExpires(t *testing.T) {
	s := mustStation(t)
	now := time.Unix(1_700_000_000, 0)
	raw, err := s.SignOffer(validOffer(), now)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, towerobj.FormatInt(now.Add(30*time.Minute).Unix()), got["expires"])
}

// The refusals. Each one is something Core would reject anyway - saying so HERE saves an
// operator a round trip through a Tower and a rejection reason they cannot see.
func TestSigningRefusesAnOfferThatCoreWouldReject(t *testing.T) {
	s := mustStation(t)
	for name, mut := range map[string]func(*Offer){
		"no tower":          func(o *Offer) { o.TowerID = "" },
		"no network":        func(o *Offer) { o.Network = "" },
		"no model":          func(o *Offer) { o.Model = "" },
		"no modality":       func(o *Offer) { o.Modality = "" },
		"no capacity":       func(o *Offer) { o.Capacity = 0 },
		"negative capacity": func(o *Offer) { o.Capacity = -1 },
		"negative price":    func(o *Offer) { o.PriceIn = -1 },
		"no ttl":            func(o *Offer) { o.TTL = 0 },
		// The one an operator is most likely to try, and the one that costs Core money on
		// every single token if it gets through.
		"earning more than the consumer pays": func(o *Offer) { o.EarnOut = o.PriceOut + 1 },
	} {
		o := validOffer()
		mut(&o)
		_, err := s.SignOffer(o, time.Now())
		require.Error(t, err, name)
	}
}

// Capabilities are always present, even when empty. An absent member is how a relay would
// strip the field from the signed bytes and then assert capabilities out of band.
func TestCapabilitiesAreAlwaysPresentEvenWhenEmpty(t *testing.T) {
	s := mustStation(t)
	o := validOffer()
	o.Capabilities = nil
	raw, err := s.SignOffer(o, time.Now())
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	caps, ok := got["capabilities"].([]any)
	require.True(t, ok, "capabilities must be present as a list")
	require.Empty(t, caps)
}

func mustStation(t *testing.T) *Station {
	t.Helper()
	s, err := Init(filepath.Join(t.TempDir(), "st"))
	require.NoError(t, err)
	return s
}

func validOffer() Offer {
	return Offer{
		Network: "rogerai-public", TowerID: "tw-1", Model: "m1", Modality: "text",
		PriceIn: 10, PriceOut: 20, EarnIn: 5, EarnOut: 10, Capacity: 4,
		Capabilities: []string{"chat"}, TTL: 30 * time.Minute,
	}
}

func offerID(t *testing.T, raw []byte) string {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	return got["offer_id"].(string)
}

// A half-written data directory must not open as a working Station. Each of these is a real
// way a directory ends up broken - a truncated write, a partial copy, a restore that missed
// a file - and every one of them has to fail LOUDLY. A Station that opened with an
// unreadable key would sign nothing and report nothing wrong.
func TestOpenRefusesAHalfWrittenStation(t *testing.T) {
	for name, break_ := range map[string]func(t *testing.T, dir string){
		"unreadable state": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte("{nope"), 0o600))
		},
		"missing assertion key": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, assertionKeyFile)))
		},
		"missing session key": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, sessionKeyFile)))
		},
		"assertion key is not a key": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, assertionKeyFile), []byte("zz"), 0o600))
		},
		"session key is the wrong length": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, sessionKeyFile), []byte("abcd"), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "st")
			_, err := Init(dir)
			require.NoError(t, err)
			break_(t, dir)
			_, err = Open(dir)
			require.Error(t, err)
		})
	}
}

// Init cannot write where it cannot write, and says so rather than returning a Station whose
// keys are not on disk.
func TestInitReportsADirectoryItCannotCreate(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(parent, "file"), []byte("x"), 0o600))
	// A path under a regular file can never be a directory.
	_, err := Init(filepath.Join(parent, "file", "st"))
	require.Error(t, err)
}

func TestAStationKnowsItsOwnDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	s, err := Init(dir)
	require.NoError(t, err)
	require.Equal(t, dir, s.Dir())

	got, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, dir, got.Dir())
}

// A data directory the Station cannot write to must fail at INIT, not later. A Station whose
// keys were never persisted would work for exactly as long as the process lived and then be
// gone, taking its attachment with it.
func TestInitRefusesADirectoryItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	dir := filepath.Join(t.TempDir(), "st")
	require.NoError(t, os.MkdirAll(dir, 0o500)) // exists, not writable
	_, err := Init(dir)
	require.Error(t, err)
}
