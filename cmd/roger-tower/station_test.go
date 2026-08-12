package main

// station_test.go covers the JOINED Station lifecycle commands.
//
// The routes they call existed and nothing called them, so an operator could not attach a
// Station to the public network at all. These tests drive the commands against a stub Core
// and assert the two things an operator depends on: that the right key signs each call, and
// that the one-time secret is presented in a way they can actually act on.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
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

// --- pausing, resuming and retiring this Tower ------------------------------

func TestDrainingAndResumingTellCoreAndSayWhatChanged(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)
	var sent []string
	core.reply["/tower/self/lifecycle"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"ok":true}`))
		return true
	}

	out, err := runCLI(t, "drain", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "no new work")
	require.Contains(t, out, "In-flight", "it says what happens to work already running")
	require.Contains(t, out, "resume", "and how to undo it")

	out, err = runCLI(t, "resume", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "back in service")

	core.mu.Lock()
	sent = append(sent, core.seen...)
	core.mu.Unlock()
	require.Equal(t, 2, countOf(sent, "/tower/self/lifecycle"))
}

// REVOKING IS TERMINAL, so it refuses without an explicit confirmation and says what is
// permanent about it BEFORE doing it - there is no path back from revoked, and a replacement
// enrolls as an entirely new Tower.
func TestRevokingRefusesWithoutConfirmationAndReachesNothing(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)

	_, err := runCLI(t, "revoke", "--dir", dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--yes")
	require.Contains(t, err.Error(), "NEW Tower")
	require.Zero(t, core.reached(), "an unconfirmed revoke reaches nothing at all")
}

func TestRevokingWithConfirmationRetiresTheTower(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)
	core.reply["/tower/self/lifecycle"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"ok":true}`))
		return true
	}
	out, err := runCLI(t, "revoke", "--dir", dir, "--yes")
	require.NoError(t, err)
	require.Contains(t, out, "retired")
	require.Contains(t, out, "init")
	require.Equal(t, 1, core.called("/tower/self/lifecycle"))
}

// Each needs a data directory and a registered Tower, and a refusal from Core reaches the
// operator as the sentence Core wrote.
func TestTheLifecycleCommandsFailUsefully(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)

	for _, c := range []string{"drain", "resume"} {
		_, err := runCLI(t, c)
		require.Error(t, err, c)
		_, err = runCLI(t, c, "--wat")
		require.Error(t, err, c)
	}
	_, err := runCLI(t, "revoke", "--wat")
	require.Error(t, err)

	core.reply["/tower/self/lifecycle"] = func(w http.ResponseWriter, _ int) bool {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"moving it from quarantine is an administrator's decision"}}`))
		return true
	}
	_, err = runCLI(t, "resume", "--dir", dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "administrator")

	// And an unregistered Tower is told which command fixes it.
	fresh := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", fresh, "--mode", "joined"}, &b))
	_, err = runCLI(t, "drain", "--dir", fresh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")
}

func countOf(all []string, want string) int {
	n := 0
	for _, s := range all {
		if s == want {
			n++
		}
	}
	return n
}

// The edge-cert command validates its arguments and reads either PEM or DER before it goes
// near the network.
func TestStationEdgeCertValidatesArguments(t *testing.T) {
	_, err := runCLI(t, "station", "edge-cert")
	require.ErrorContains(t, err, "--station-id")
	_, err = runCLI(t, "station", "edge-cert", "--station-id", "st-1")
	require.ErrorContains(t, err, "--csr")
	_, err = runCLI(t, "station", "edge-cert", "--station-id", "st-1", "--csr", "/no/such/file")
	require.Error(t, err)
}

// It reads a PEM CSR, submits it, and writes the returned certificate.
func TestStationEdgeCertFetchesAndWrites(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tower/station/edge-cert" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"station_id": "st-1", "relay_name": "st-1.relay.example",
				"certificate": base64.StdEncoding.EncodeToString([]byte("issued-cert-der")),
				"ca":          base64.StdEncoding.EncodeToString([]byte("ca-der")),
				"not_after":   1,
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("ROGER_BROKER", srv.URL)

	// A PEM CSR blob, as `roger-station csr` prints.
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("der")})
	csrPath := filepath.Join(t.TempDir(), "req.pem")
	require.NoError(t, os.WriteFile(csrPath, csrPEM, 0o644))
	outPath := filepath.Join(t.TempDir(), "cert.pem")

	out, err := runCLI(t, "station", "edge-cert", "--station-id", "st-1", "--csr", csrPath, "--out", outPath)
	require.NoError(t, err)
	require.Contains(t, out, "st-1.relay.example")
	written, err := os.ReadFile(outPath)
	require.NoError(t, err)
	require.Contains(t, string(written), "BEGIN CERTIFICATE")
}

func TestStationUsageListsEdgeCert(t *testing.T) {
	out, err := runCLI(t, "station")
	require.NoError(t, err)
	require.Contains(t, out, "edge-cert")
}
