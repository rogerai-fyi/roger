package main

// station_test.go covers the JOINED Station lifecycle commands.
//
// The routes they call existed and nothing called them, so an operator could not attach a
// Station to the public network at all. These tests drive the commands against a stub Core
// and assert the two things an operator depends on: that the right key signs each call, and
// that the one-time secret is presented in a way they can actually act on.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// joinedRegisteredTower is a joined Tower that has already registered.
func joinedRegisteredTower(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "joined"}, &b))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "admission.json"),
		[]byte(`{"tower_id":"tw-1"}`), 0o600))
	return dir
}

func stationStubCore(t *testing.T) *coreStub {
	t.Helper()
	c := newCoreStub(t)
	c.reply["/tower/station/invite"] = func(w http.ResponseWriter, _ int) bool {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"invitation_id": "sinv-9", "station_id": "st-9", "tower_id": "tw-1",
			"secret": "one-time", "expires_in": 3600,
		})
		return true
	}
	c.reply["/tower/station/attach"] = func(w http.ResponseWriter, _ int) bool {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "station_id": "st-9", "state": "quarantine",
		})
		return true
	}
	return c
}

func TestStationUsageListsBothHalvesOfTheFlow(t *testing.T) {
	out, err := runCLI(t, "station")
	require.NoError(t, err)
	require.Contains(t, out, "invite")
	require.Contains(t, out, "attach")
	// The warning that decides where the keys live belongs where the operator is standing.
	require.Contains(t, out, "ON THE STATION")

	out, err = runCLI(t, "station", "help")
	require.NoError(t, err)
	require.Contains(t, out, "roger-station init")

	_, err = runCLI(t, "station", "frobnicate")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown station subcommand")
}

// The invite prints the secret AND the exact command that redeems it. An operator holding a
// value that is shown once and never again should not have to assemble the next step by
// hand while it is on screen.
func TestInvitingPrintsTheSecretAndTheCommandThatRedeemsIt(t *testing.T) {
	stationStubCore(t)
	dir := joinedRegisteredTower(t)

	out, err := runCLI(t, "station", "invite", "--dir", dir,
		"--assertion-key", "aa", "--session-key", "bb")
	require.NoError(t, err)
	require.Contains(t, out, "sinv-9")
	require.Contains(t, out, "one-time")
	require.Contains(t, out, "shown ONCE")
	require.Contains(t, out, "roger-tower station attach")
	require.Contains(t, out, "--invitation sinv-9")
	require.Contains(t, out, "--secret one-time")
}

// Attaching reports the state Core recorded, and says plainly that quarantine is expected -
// it is the single most likely thing for an operator to read as a failure and start
// debugging.
func TestAttachingReportsQuarantineAsExpectedRatherThanAsAFailure(t *testing.T) {
	stationStubCore(t)
	dir := joinedRegisteredTower(t)

	out, err := runCLI(t, "station", "attach", "--dir", dir,
		"--invitation", "sinv-9", "--secret", "one-time",
		"--station-id", "st-9", "--assertion-key", "aa", "--session-key", "bb")
	require.NoError(t, err)
	require.Contains(t, out, "attached station st-9")
	require.Contains(t, out, "Quarantine is the expected state")
	// And it names the next thing that has to happen, which is on a different machine.
	require.Contains(t, out, "roger-station offer")
}

// INVITE IS THE OPERATOR, ATTACH IS THE TOWER. Driven through the commands rather than the
// library, because the wiring is what an operator actually runs and a command that signed
// with the wrong key would be refused by Core with a message about something else.
func TestTheTwoCommandsSignWithDifferentKeys(t *testing.T) {
	core := stationStubCore(t)
	dir := joinedRegisteredTower(t)

	_, err := runCLI(t, "station", "invite", "--dir", dir,
		"--assertion-key", "aa", "--session-key", "bb")
	require.NoError(t, err)
	_, err = runCLI(t, "station", "attach", "--dir", dir,
		"--invitation", "sinv-9", "--secret", "one-time",
		"--assertion-key", "aa", "--session-key", "bb")
	require.NoError(t, err)

	core.mu.Lock()
	defer core.mu.Unlock()
	require.NotEqual(t, core.keys["/tower/station/invite"], core.keys["/tower/station/attach"],
		"the account key and the Tower identity key must not be the same key")
}

// Both commands need a data directory, and both refuse before reaching the network when the
// Station's keys are missing or are one key used twice.
func TestTheStationCommandsRefuseBeforeReachingTheNetwork(t *testing.T) {
	core := stationStubCore(t)
	dir := joinedRegisteredTower(t)

	for _, args := range [][]string{
		{"station", "invite"},
		{"station", "attach", "--invitation", "i", "--secret", "s"},
	} {
		_, err := runCLI(t, args...)
		require.Error(t, err, args[1])
		require.Contains(t, err.Error(), "--dir is required", args[1])
	}

	_, err := runCLI(t, "station", "invite", "--dir", dir, "--assertion-key", "aa")
	require.Error(t, err)
	require.Contains(t, err.Error(), "BOTH its keys")

	_, err = runCLI(t, "station", "invite", "--dir", dir,
		"--assertion-key", "aa", "--session-key", "aa")
	require.Error(t, err)
	require.Contains(t, err.Error(), "different keys")

	_, err = runCLI(t, "station", "attach", "--dir", dir, "--secret", "s")
	require.Error(t, err)

	require.Zero(t, core.called("/tower/station/invite"))
	require.Zero(t, core.called("/tower/station/attach"))

	// A bad flag is a flag error, not a panic.
	for _, sub := range []string{"invite", "attach"} {
		_, err := runCLI(t, "station", sub, "--wat")
		require.Error(t, err, sub)
	}
}

// A Tower that has not registered cannot authorize anything, and is told which command fixes
// it rather than being handed a transport error.
func TestTheStationCommandsNeedARegisteredTower(t *testing.T) {
	stationStubCore(t)
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "joined"}, &b))

	_, err := runCLI(t, "station", "invite", "--dir", dir,
		"--assertion-key", "aa", "--session-key", "bb")
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")
}

// A refusal from Core reaches the operator as the sentence Core wrote.
func TestARefusedInvitationSaysWhy(t *testing.T) {
	core := stationStubCore(t)
	dir := joinedRegisteredTower(t)
	core.reply["/tower/station/invite"] = func(w http.ResponseWriter, _ int) bool {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no such Tower on this account"}}`))
		return true
	}
	_, err := runCLI(t, "station", "invite", "--dir", dir,
		"--assertion-key", "aa", "--session-key", "bb")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such Tower on this account")
}

// `status` on a joined Tower reports what CORE believes, not what the Tower's own files
// remember. The distinction is the whole value of it: the local record is what this Tower
// was told at enrollment, and it goes stale the moment an administrator changes anything.
func TestStatusReportsCoresViewOfAJoinedTower(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)
	core.reply["/tower/status"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"towers":[{
			"tower_id":"tw-1","state":"active","may_take_work":true,"link_live":true,
			"inventory_revision":3,"carries_traffic":false,
			"note":"routing Tower-backed work is not shipped yet",
			"routable":[{"station_id":"st-9","model":"m1","modality":"text","capacity":4}]
		}]}`))
		return true
	}

	out, err := runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "Roger Core says")
	require.Contains(t, out, "state:         active")
	require.Contains(t, out, "may take work: true")
	require.Contains(t, out, "revision 3")
	require.Contains(t, out, "st-9 m1")
	// The answer to "everything looks right and nothing is happening".
	require.Contains(t, out, "not shipped yet")
}

// Quarantine is named as expected rather than left to be read as a failure.
func TestStatusSaysQuarantineIsNotAFault(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)
	core.reply["/tower/status"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"towers":[{"tower_id":"tw-1","state":"quarantine"}]}`))
		return true
	}
	out, err := runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "not a fault")
	require.Contains(t, out, "routable:      none")
}

// A Core that cannot be reached must not fail the command: the local half is still worth
// having, and `status` erroring out because the network is down is the opposite of useful
// at the moment somebody runs it.
func TestStatusStillWorksWhenCoreCannotBeReached(t *testing.T) {
	dir := joinedRegisteredTower(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")

	out, err := runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "mode: joined", "the local half still reports")
	require.Contains(t, out, "could not ask RogerAI")
}

// And an unregistered joined Tower is told which command fixes it, rather than being shown
// an empty report it has to interpret.
func TestStatusTellsAnUnregisteredTowerToRegister(t *testing.T) {
	newCoreStub(t)
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "joined"}, &b))

	out, err := runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "not registered yet")
	require.Contains(t, out, "roger-tower register")
}

// A standalone Tower asks Core nothing at all - it has no account and no Core to ask.
func TestStatusOnAStandaloneTowerReachesNothing(t *testing.T) {
	core := newCoreStub(t)
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "standalone"}, &b))

	out, err := runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "local network:")
	require.NotContains(t, out, "Roger Core says")
	require.Zero(t, core.reached())
}

// Revoking is the operator's, signed by the account, and it says what to do next - the leaf
// only leaves the inventory when the Tower next pushes without it.
func TestRevokingAStationSaysWhatElseToDo(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)
	core.reply["/tower/station/revoke"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"ok":true,"revoked":true}`))
		return true
	}
	out, err := runCLI(t, "station", "revoke", "--dir", dir, "--station-id", "st-9")
	require.NoError(t, err)
	require.Contains(t, out, "revoked station st-9")
	require.Contains(t, out, "offers directory")

	_, err = runCLI(t, "station", "revoke", "--dir", dir)
	require.Error(t, err, "revoking needs a Station")
	_, err = runCLI(t, "station", "revoke")
	require.Error(t, err)
	_, err = runCLI(t, "station", "revoke", "--wat")
	require.Error(t, err)
}
