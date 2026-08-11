package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/inv"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerobj"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := run(args, &b)
	return b.String(), err
}

func TestUsageAndVersion(t *testing.T) {
	out, err := runCLI(t)
	require.NoError(t, err)
	require.Contains(t, out, "roger-station")
	// The warning is the reason this binary exists rather than a roger-tower subcommand, so
	// it belongs where every operator sees it.
	require.Contains(t, out, "RUN THIS ON THE STATION")

	out, err = runCLI(t, "help")
	require.NoError(t, err)
	require.Contains(t, out, "offer")

	out, err = runCLI(t, "version")
	require.NoError(t, err)
	require.Equal(t, "dev\n", out)

	_, err = runCLI(t, "frobnicate")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown command")
}

// init prints exactly what the operator has to paste into the invitation, and nothing that
// must stay on this machine.
func TestInitPrintsTheIdentityAndNeverAPrivateKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	out, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "station id:")
	require.Contains(t, out, "assertion key:")
	require.Contains(t, out, "session key:")

	s, err := station.Open(dir)
	require.NoError(t, err)
	require.Contains(t, out, s.Assertion)
	require.Contains(t, out, s.Session)

	// The private halves are on disk and must not be on the terminal, in a scrollback
	// buffer, or in whatever the operator pastes into a support thread.
	for _, f := range []string{"assertion.key", "session.key"} {
		raw, rerr := os.ReadFile(filepath.Join(dir, f))
		require.NoError(t, rerr)
		require.NotContains(t, out, string(raw), "%s leaked to stdout", f)
	}
}

func TestKeysReprintsTheIdentityForAnInvitation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	out, err := runCLI(t, "keys", "--dir", dir)
	require.NoError(t, err)
	s, err := station.Open(dir)
	require.NoError(t, err)
	require.Contains(t, out, s.StationID)
	require.Contains(t, out, s.Assertion)
	require.Contains(t, out, s.Session)
}

// THE OFFER MUST VERIFY UNDER CORE'S OWN VERIFIER. Pretty-printing it for a human to read
// is the obvious way to break that, and the reason it does not is that the signature covers
// the CANONICAL form rather than these bytes. Asserting it here is what keeps that true.
func TestASignedOfferSurvivesBeingPrettyPrinted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	out, err := runCLI(t, "offer", "--dir", dir, "--tower", "tw-1", "--model", "m1",
		"--price-in", "10", "--price-out", "20", "--earn-in", "5", "--earn-out", "10",
		"--capacity", "4", "--caps", "chat,tools")
	require.NoError(t, err)
	require.Contains(t, out, "\n  ", "it is written for a human to read")

	s, err := station.Open(dir)
	require.NoError(t, err)
	require.NoError(t, towerobj.Verify(s.AssertionPub(), link.PublicNetwork,
		inv.TypeOffer, inv.Version, []byte(out), "station_sig"))

	var leaf map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &leaf))
	require.Equal(t, "tw-1", leaf["tower_id"])
	require.Equal(t, s.StationID, leaf["station_id"])
	require.Equal(t, []any{"chat", "tools"}, leaf["capabilities"])
	require.Equal(t, link.PublicNetwork, leaf["network"], "the network is not a flag")
}

func TestAnOfferCanBeWrittenStraightToAFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "offer.json")
	out, err := runCLI(t, "offer", "--dir", dir, "--tower", "tw-1", "--model", "m1",
		"--price-in", "10", "--price-out", "20", "--out", path)
	require.NoError(t, err)
	require.Contains(t, out, "written to")
	require.Contains(t, out, "byte for byte")

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	s, err := station.Open(dir)
	require.NoError(t, err)
	require.NoError(t, towerobj.Verify(s.AssertionPub(), link.PublicNetwork,
		inv.TypeOffer, inv.Version, raw, "station_sig"))
}

// An empty --caps is an empty LIST, not a missing member. A leaf without the member is one
// a relay could have stripped, so the distinction is load-bearing rather than cosmetic.
func TestNoCapabilitiesIsAnEmptyListNotAMissingMember(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	out, err := runCLI(t, "offer", "--dir", dir, "--tower", "tw-1", "--model", "m1")
	require.NoError(t, err)
	var leaf map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &leaf))
	require.Contains(t, leaf, "capabilities")
	require.Equal(t, []any{}, leaf["capabilities"])
}

// An offer Core would reject is refused HERE, with the reason, rather than disappearing
// silently out of a revision the Tower relayed on the operator's behalf.
func TestAnUnservableOfferIsRefusedWithItsReason(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	_, err = runCLI(t, "offer", "--dir", dir, "--model", "m1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Tower")

	_, err = runCLI(t, "offer", "--dir", dir, "--tower", "tw-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "model")

	// The one that costs Core money on every token.
	_, err = runCLI(t, "offer", "--dir", dir, "--tower", "tw-1", "--model", "m1",
		"--price-out", "10", "--earn-out", "11")
	require.Error(t, err)
	require.Contains(t, err.Error(), "earn more than the consumer pays")
}

func TestStatusReportsLocalStateAndDoesNotGuessAtCores(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	out, err := runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "station id:")
	require.Contains(t, out, dir)
	// A local binary that made no connection must not imply it knows the network's answer.
	require.Contains(t, out, "local state only")
}

// Every command needs a data directory, and one that is not a Station says so.
func TestEveryCommandNeedsAStationDirectory(t *testing.T) {
	for _, args := range [][]string{
		{"init"}, {"keys"}, {"status"}, {"offer", "--tower", "tw-1", "--model", "m1"},
	} {
		_, err := runCLI(t, args...)
		require.Error(t, err, args[0])
		require.Contains(t, err.Error(), "--dir is required", args[0])
	}

	empty := t.TempDir()
	for _, args := range [][]string{
		{"keys", "--dir", empty}, {"status", "--dir", empty},
		{"offer", "--dir", empty, "--tower", "tw-1", "--model", "m1"},
	} {
		_, err := runCLI(t, args...)
		require.Error(t, err, args[0])
		require.Contains(t, err.Error(), "not an initialized Station", args[0])
	}

	// And a bad flag is a flag error, not a panic.
	for _, name := range []string{"init", "keys", "status", "offer"} {
		_, err := runCLI(t, name, "--wat")
		require.Error(t, err, name)
	}
}

// Init refuses to run twice: new keys would leave the attachment Core recorded naming keys
// this Station no longer holds.
func TestInitWillNotOverwriteAStation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)
	_, err = runCLI(t, "init", "--dir", dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already")
}

func TestAnUnwritableOfferDestinationIsReported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	_, err = runCLI(t, "offer", "--dir", dir, "--tower", "tw-1", "--model", "m1",
		"--out", filepath.Join(t.TempDir(), "nope", "offer.json"))
	require.Error(t, err)
}

func TestSplitCapsIgnoresBlanksAndPadding(t *testing.T) {
	require.Equal(t, []string{}, splitCaps(""))
	require.Equal(t, []string{}, splitCaps(" , , "))
	require.Equal(t, []string{"a", "b"}, splitCaps(" a , b "))
	require.Equal(t, []string{"a"}, splitCaps(strings.Join([]string{"a", ""}, ",")))
}

// AN OFFER EXPIRES, AND A FILE DOES NOT REFRESH ITSELF.
//
// The Tower re-reads its offers directory on every push, so a fresh file is picked up
// automatically - but nothing was writing one. A Station published once and dropped off the
// network when its offer's TTL passed, with the Tower still relaying the stale file and Core
// still excluding it for "the offer has expired". Every part looked healthy.
//
// --refresh is the other half: it rewrites the file on an interval derived from the TTL, so
// what the Tower relays is always current.
func TestRefreshingRewritesTheOfferBeforeItExpires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "offer.json")

	ticks := make(chan time.Time, 1)
	stop := make(chan struct{})
	var b syncOut
	done := make(chan error, 1)
	go func() {
		done <- refreshOffers(offerJob{
			station: mustOpen(t, dir), path: path,
			offer: station.Offer{
				Network: "roger-public", TowerID: "tw-1", Model: "m1", Modality: "text",
				Capacity: 1, TTL: 30 * time.Minute,
			},
		}, &b, stop, func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} })
	}()

	require.Eventually(t, func() bool { _, serr := os.Stat(path); return serr == nil },
		2*time.Second, 5*time.Millisecond, "the first offer is written immediately")
	first, err := os.ReadFile(path)
	require.NoError(t, err)

	ticks <- time.Now()
	require.Eventually(t, func() bool {
		got, rerr := os.ReadFile(path)
		return rerr == nil && string(got) != string(first)
	}, 2*time.Second, 5*time.Millisecond, "the offer must be re-signed before it expires")

	close(stop)
	require.NoError(t, <-done)

	// And what it wrote last is still a valid signed offer, not a truncated one.
	last, err := os.ReadFile(path)
	require.NoError(t, err)
	s := mustOpen(t, dir)
	require.NoError(t, towerobj.Verify(s.AssertionPub(), link.PublicNetwork,
		inv.TypeOffer, inv.Version, last, "station_sig"))
}

// The interval is DERIVED from the TTL rather than set beside it. Two numbers that have to
// agree and are written down separately are two numbers that will eventually disagree, and
// the failure is invisible: the file is there, the Tower relays it, Core drops it.
func TestTheRefreshIntervalLeavesRoomForARetry(t *testing.T) {
	for _, ttl := range []time.Duration{time.Minute, 30 * time.Minute, 24 * time.Hour} {
		got := refreshEvery(ttl)
		require.Less(t, got, ttl, "a refresh at or after the expiry is not a refresh")
		require.LessOrEqual(t, got*2, ttl, "one missed refresh must not be enough to expire")
		require.Positive(t, got)
	}
}

// --refresh needs somewhere to write. Refreshing to stdout would scroll a signed object past
// an operator every few minutes and leave the Tower reading nothing.
func TestRefreshingRequiresAnOutputFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	_, err = runCLI(t, "offer", "--dir", dir, "--tower", "tw-1", "--model", "m1", "--refresh")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--out")
}

func mustOpen(t *testing.T, dir string) *station.Station {
	t.Helper()
	s, err := station.Open(dir)
	require.NoError(t, err)
	return s
}

// syncOut is a locked writer: the refresh loop runs in its own goroutine.
type syncOut struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncOut) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// A refresh it cannot write is reported and the loop KEEPS GOING: the offer already on disk
// is good until it expires, so there is time for the next attempt. Exiting would guarantee
// the outage this whole mechanism exists to prevent.
func TestAFailedRefreshIsReportedAndTheLoopContinues(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	// A path whose parent does not exist: every write fails, including the first.
	job := offerJob{
		station: mustOpen(t, dir),
		path:    filepath.Join(t.TempDir(), "nope", "offer.json"),
		offer: station.Offer{
			Network: "roger-public", TowerID: "tw-1", Model: "m1", Modality: "text",
			Capacity: 1, TTL: 30 * time.Minute,
		},
	}
	ticks := make(chan time.Time, 1)
	var b syncOut
	// The FIRST write failing is fatal - there is nothing on disk yet, so continuing would
	// leave a "keeping it fresh" message next to a Station that published nothing.
	err = refreshOffers(job, &b, closedStop(),
		func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} })
	require.Error(t, err)

	// But once one write has landed, a later failure only warns.
	good := filepath.Join(t.TempDir(), "offer.json")
	job.path = good
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- refreshOffers(job, &b, stop, func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} })
	}()
	require.Eventually(t, func() bool { _, serr := os.Stat(good); return serr == nil },
		2*time.Second, 5*time.Millisecond)

	require.NoError(t, os.Chmod(filepath.Dir(good), 0o500)) // no more writes here
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(good), 0o700) })
	if os.Geteuid() != 0 {
		ticks <- time.Now()
		require.Eventually(t, func() bool { return strings.Contains(b.read(), "could not re-sign") },
			2*time.Second, 5*time.Millisecond)
	}
	close(stop)
	require.NoError(t, <-done)
}

// The production clock and signal wiring, which nothing else reaches.
func TestTheRealTickerAndInterruptChannelAreWiredUp(t *testing.T) {
	c, stop := realTicker(time.Millisecond)
	defer stop()
	select {
	case <-c:
	case <-time.After(2 * time.Second):
		t.Fatal("the production ticker never fired")
	}

	// The interrupt channel exists and is NOT already closed - a stop signal that fires on
	// its own would end the refresh loop the moment it started.
	sig := waitForInterrupt()
	select {
	case <-sig:
		t.Fatal("the interrupt channel fired without a signal")
	default:
	}
}

// The usage says an offer expires and what to do about it. An operator who reads only the
// help text must not end up publishing once and wondering where their Station went.
func TestUsageWarnsThatOffersExpire(t *testing.T) {
	out, err := runCLI(t)
	require.NoError(t, err)
	require.Contains(t, out, "AN OFFER EXPIRES")
	require.Contains(t, out, "--refresh")
}

func closedStop() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func (s *syncOut) read() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
