package main

// Where Core measures a Tower's hub to be. Contract: features/tower/placement_locality.feature.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// withHubResolver swaps the hub resolver for the test.
func withHubResolver(t *testing.T, f func(ctx context.Context, host string) ([]string, error)) {
	t.Helper()
	prev := hubResolve.Load()
	hubResolve.Store(&hubResolver{lookup: f, timeout: prev.timeout})
	t.Cleanup(func() { hubResolve.Store(prev) })
}

func staticResolver(addrs ...string) func(context.Context, string) ([]string, error) {
	return func(context.Context, string) ([]string, error) { return addrs, nil }
}

func testNetTable(t *testing.T) *netTable {
	t.Helper()
	nt, err := parseNetTable([]byte("203.0.113.0/24 NA\n198.51.100.0/24 EU\n192.0.2.0/24 AF\n"))
	require.NoError(t, err)
	return nt
}

func TestATowersLocalityIsResolvedFromItsHubWhenTheSessionOpens(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	var asked atomic.Value
	withHubResolver(t, func(_ context.Context, host string) ([]string, error) {
		asked.Store(host)
		return []string{"203.0.113.7"}, nil
	})
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	h := helloFor(lt)
	h.RelayEndpoint = "hub.example:7443"
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, h), nil)
	require.Equal(t, http.StatusOK, code, raw)

	require.Eventually(t, func() bool { _, ok := b.tower.link.Locality(lt.id); return ok }, 2*time.Second, 5*time.Millisecond)
	got, _ := b.tower.link.Locality(lt.id)
	require.Equal(t, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, got)
	require.Equal(t, "hub.example", asked.Load(), "the HUB host is resolved, by Core")
}

func TestATowerWithNoHubHasAKnownEmptyLocality(t *testing.T) {
	b, srv := towerTestBroker(t)
	withHubResolver(t, func(context.Context, string) ([]string, error) {
		t.Error("nothing to resolve")
		return nil, nil
	})
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	openSessionOf(t, srv.URL, lt, call)
	require.Eventually(t, func() bool { _, ok := b.tower.link.Locality(lt.id); return ok }, 2*time.Second, 5*time.Millisecond)
	got, _ := b.tower.link.Locality(lt.id)
	require.Equal(t, placement.Locality{}, got)
}

func TestTheHubAddressDecidesAndAConflictWithTheLinkIsUnknown(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)
	waitMeasured(t, b, lt.id)

	withHubResolver(t, staticResolver("203.0.113.7"))
	var buf bytes.Buffer
	defer captureLog(&buf)()
	b.measureTowerLocality(lt.id, acc.SessionID, "hub.example:7443", "198.51.100.9")
	got, ok := b.tower.link.Locality(lt.id)
	require.True(t, ok)
	require.Equal(t, placement.Locality{}, got, "hub in NA, link from EU: unknown")
	require.Contains(t, buf.String(), "tower-locality conflict tower="+lt.id+" hub=NA link=EU")
	require.NotContains(t, buf.String(), "203.0.113", "never an address or a bucket in the log")
	require.NotContains(t, buf.String(), "198.51.100")

	// The link agreeing (or unknown) keeps the hub's.
	b.measureTowerLocality(lt.id, acc.SessionID, "hub.example:7443", "203.0.113.200")
	got, _ = b.tower.link.Locality(lt.id)
	require.Equal(t, placement.Locality{Bucket: "203.0.113.0/24", Continent: "NA"}, got)
}

func TestHubAddressesThatSayNothingAboutPlaceGiveNoLocality(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)
	waitMeasured(t, b, lt.id)
	for _, addrs := range [][]string{
		{"127.0.0.1"}, {"10.1.2.3"}, {"fd00::1"}, {"fe80::1"}, {"0.0.0.0"}, {"100.64.0.1"},
		{"203.0.113.7", "198.51.100.7"}, // across continents
	} {
		withHubResolver(t, staticResolver(addrs...))
		b.measureTowerLocality(lt.id, acc.SessionID, "hub.example:7443", "")
		got, ok := b.tower.link.Locality(lt.id)
		require.True(t, ok)
		require.Equal(t, "", got.Continent, "%v", addrs)
		if len(addrs) == 1 && addrs[0] != "100.64.0.1" {
			require.Equal(t, "", got.Bucket, "%v", addrs)
		}
	}
	// A literal IP endpoint needs no lookup and is measured the same way.
	withHubResolver(t, func(context.Context, string) ([]string, error) { return nil, errors.New("must not be asked") })
	b.measureTowerLocality(lt.id, acc.SessionID, "192.0.2.10:7443", "")
	got, _ := b.tower.link.Locality(lt.id)
	require.Equal(t, placement.Locality{Bucket: "192.0.2.0/24", Continent: "AF"}, got)
}

func TestASlowOrFailedLookupLeavesTheLocalityUnmeasuredAndTheSweepRetriesOnce(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	prev := hubResolve.Load()
	hubResolve.Store(&hubResolver{timeout: 20 * time.Millisecond, lookup: func(ctx context.Context, _ string) ([]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	t.Cleanup(func() { hubResolve.Store(prev) })
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	h := helloFor(lt)
	h.RelayEndpoint = "hub.example:7443"
	start := time.Now()
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, h), nil)
	require.Equal(t, http.StatusOK, code, raw)
	require.Less(t, time.Since(start), time.Second, "the session opens without waiting on DNS")
	time.Sleep(60 * time.Millisecond)
	_, ok := b.tower.link.Locality(lt.id)
	require.False(t, ok, "a lookup that failed is not an answer")

	var calls atomic.Int32
	withHubResolver(t, func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"198.51.100.4"}, nil
	})
	b.towerGovSweepOnce(time.Now())
	require.EqualValues(t, 1, calls.Load(), "at most once per sweep")
	got, ok := b.tower.link.Locality(lt.id)
	require.True(t, ok)
	require.Equal(t, "EU", got.Continent)
	b.towerGovSweepOnce(time.Now())
	require.EqualValues(t, 1, calls.Load(), "a measured Tower is not looked up again")
}

func TestNoTableMeansOnlyABucketIsKnown(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)
	waitMeasured(t, b, lt.id)
	withHubResolver(t, staticResolver("203.0.113.7"))
	b.measureTowerLocality(lt.id, acc.SessionID, "hub.example:7443", "")
	got, _ := b.tower.link.Locality(lt.id)
	require.Equal(t, placement.Locality{Bucket: "203.0.113.0/24"}, got)
}

func TestAStationsLocalityComesFromItsOwnAddress(t *testing.T) {
	b := &broker{netTable: testNetTable(t)}
	require.Equal(t, placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}, b.addrLocality("198.51.100.20"))
	require.Equal(t, placement.Locality{}, b.addrLocality("192.168.1.5"))
	require.Equal(t, placement.Locality{}, b.addrLocality("not an address"))
	require.True(t, strings.HasPrefix(b.addrLocality("2001:db8::5").Bucket, "2001:db8::"))
}

func TestTheAdminDetailShowsTheContinentButNeverTheBucket(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	b.netTable = testNetTable(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	d := adminTowerDetailOf(t, srv, lt.id)
	require.Equal(t, map[string]any{"measured": false}, d["locality"], "never connected, never measured")
	acc := openSessionOf(t, srv.URL, lt, call)
	waitMeasured(t, b, lt.id)

	withHubResolver(t, staticResolver("203.0.113.7"))
	b.measureTowerLocality(lt.id, acc.SessionID, "hub.example:7443", "")
	d = adminTowerDetailOf(t, srv, lt.id)
	require.Equal(t, map[string]any{"measured": true, "continent": "NA", "has_bucket": true}, d["locality"])
	raw := string(jsonOf(t, d))
	require.NotContains(t, raw, "203.0.113", "the bucket is never shown")
}

// waitMeasured waits for the measurement a session open starts, so a test's own call is the
// last write rather than racing it.
func waitMeasured(t *testing.T, b *broker, towerID string) {
	t.Helper()
	require.Eventually(t, func() bool { _, ok := b.tower.link.Locality(towerID); return ok }, 2*time.Second, time.Millisecond)
}

// REVIEW FINDING: locality measured once from DNS, which the Tower controls. A passing hub
// check re-measures from the address that actually proved the Tower's identity.
func TestAPassingHubCheckRelocatesTheHubToTheAddressThatProvedIt(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	withHubResolver(t, staticResolver("203.0.113.7")) // DNS told Core: NA
	openWithHub(t, b, srv, lt, "hub.example:"+portOf(t, realHubFor(t, lt)))
	waitMeasured(t, b, lt.id)
	got, _ := b.tower.link.Locality(lt.id)
	require.Equal(t, "NA", got.Continent)
	// The real hub answers on loopback; once it proves itself there, that is where it is.
	b.tower.link.SetLocality(lt.id, mustCurrent(t, b, lt.id), got) // unchanged so far
	b.relocateFromProof(lt.id, "192.0.2.10:7443")
	got, _ = b.tower.link.Locality(lt.id)
	require.Equal(t, placement.Locality{Bucket: "192.0.2.0/24", Continent: "AF"}, got)
}

func TestAHubCheckFeedsTheProvenAddressBack(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	openWithHub(t, b, srv, lt, realHubFor(t, lt))
	waitMeasured(t, b, lt.id)
	b.towerHubCheckOnce(time.Now())
	got, ok := b.tower.link.Locality(lt.id)
	require.True(t, ok)
	require.Equal(t, placement.Locality{}, got, "a loopback hub proves itself but says nothing about place")
}

// REVIEW FINDING: the sweep's retry skipped the link cross-check.
func TestARetryStillCrossChecksTheLinkContinent(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)
	waitMeasured(t, b, lt.id)
	// The first lookup "failed" but the link's continent was recorded with the session.
	require.True(t, b.tower.link.SetLinkContinent(lt.id, acc.SessionID, "EU"))
	withHubResolver(t, staticResolver("203.0.113.7")) // NA
	var buf bytes.Buffer
	defer captureLog(&buf)()
	b.measureTowerLocality(lt.id, acc.SessionID, "hub.example:7443", "") // the retry's call
	got, _ := b.tower.link.Locality(lt.id)
	require.Equal(t, placement.Locality{}, got)
	require.Contains(t, buf.String(), "tower-locality conflict tower="+lt.id+" hub=NA link=EU")
}

func portOf(t *testing.T, hostport string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(hostport)
	require.NoError(t, err)
	return p
}

// AUDIT FINDING: a failed first lookup returned before the link continent was kept.
func TestAFailedFirstLookupStillKeepsTheLinkContinent(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)
	waitMeasured(t, b, lt.id)
	withHubResolver(t, func(context.Context, string) ([]string, error) { return nil, errors.New("the Tower made its DNS fail") })
	b.measureTowerLocality(lt.id, acc.SessionID, "hub.example:7443", "198.51.100.9")
	require.Equal(t, "EU", b.tower.link.LinkContinent(lt.id))
}
