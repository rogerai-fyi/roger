package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
