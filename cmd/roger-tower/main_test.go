package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/tower"
)

// TestMain makes it impossible for a test in this package to reach a live service.
//
// towerjoin's broker base defaults to https://broker.rogerai.fm - PRODUCTION - whenever
// ROGER_BROKER is unset, so any test that exercises `register` without pinning it posts a
// real enrollment to the live network. Worse, such a test's result depends on whether the
// machine running it happens to have a route: it "passes" on a sealed CI box because the
// call fails, and fails on a connected laptop because the call SUCCEEDS.
//
// The default here is a loopback port nothing listens on, so forgetting to pin the broker
// costs an instant connection refusal instead of a call to production. Tests that want a
// broker stand one up with httptest and override this.
func TestMain(m *testing.M) {
	os.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	os.Exit(m.Run())
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := run(args, &b)
	return b.String(), err
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tower.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

const standaloneYAML = `apiVersion: tower.rogerai.fm/v1alpha1
kind: Tower
mode: standalone
`

const joinedYAML = `apiVersion: tower.rogerai.fm/v1alpha1
kind: Tower
mode: joined
joined:
  authority: https://broker.rogerai.fm
  enrollmentTokenFile: /run/secrets/enrollment-token
`

func TestUsageAndVersion(t *testing.T) {
	out, err := runCLI(t)
	require.NoError(t, err)
	require.Contains(t, out, "roger-tower")

	out, err = runCLI(t, "help")
	require.NoError(t, err)
	require.Contains(t, out, "standalone")
	// A command that ships and is not in the usage text may as well not exist. `serve` was
	// missing from it for exactly as long as it was unshipped, and stayed missing after.
	require.Contains(t, out, "serve", "every shipped command is discoverable")

	out, err = runCLI(t, "version")
	require.NoError(t, err)
	require.Equal(t, "dev\n", out)
}

func TestUnknownCommandFails(t *testing.T) {
	_, err := runCLI(t, "frobnicate")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown command")
}

// `serve` HOLDS THE RELAY LINK NOW. This test used to assert the opposite - that serve was
// unshipped and said so - and it is kept, inverted, because the inversion is the point: the
// link gaining a Tower-side participant is what changed, and a regression that took serve
// back to a message would otherwise pass silently.
//
// Asking to serve without a data directory must fail on the missing directory, not on a
// missing feature.
func TestServeShipsAndAsksForItsDataDirectory(t *testing.T) {
	_, err := runCLI(t, "serve")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--dir", "it asks for what it needs")
	require.NotContains(t, err.Error(), "not shipped", "serve ships")
}

// DRAIN, RESUME AND REVOKE SHIP TOO. This asserted the opposite for both drain and revoke,
// and is inverted rather than deleted because the inversion is the change: each now fails on
// its own missing arguments, not on being absent.
func TestTheOperatorLifecycleCommandsShip(t *testing.T) {
	for _, c := range []string{"drain", "resume"} {
		_, err := runCLI(t, c)
		require.Error(t, err, c)
		require.Contains(t, err.Error(), "--dir", "%s asks for what it needs", c)
		require.NotContains(t, err.Error(), "not shipped", "%s ships", c)
	}

	// Revoke is TERMINAL, so it refuses before it asks for anything else. An operator who
	// mistypes a command name must not retire their Tower by accident.
	_, err := runCLI(t, "revoke", "--dir", t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--yes")
	require.Contains(t, err.Error(), "cannot be un-revoked",
		"it says what is permanent about it before doing it")

	// And the usage lists all three, or an operator cannot find them.
	out, err := runCLI(t, "help")
	require.NoError(t, err)
	for _, c := range []string{"drain", "resume", "revoke"} {
		require.Contains(t, out, "roger-tower "+c, c)
	}
}

func TestInitAndStatusRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "t")

	out, err := runCLI(t, "init", "--dir", dir, "--mode", "standalone")
	require.NoError(t, err)
	require.Contains(t, out, "initialized standalone Tower")
	require.Contains(t, out, "local network: local-")

	out, err = runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "mode: standalone")
	require.Contains(t, out, "local network: local-")
}

func TestInitJoinedReportsThePublicNetwork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "t")
	_, err := runCLI(t, "init", "--dir", dir, "--mode", "joined")
	require.NoError(t, err)

	out, err := runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "mode: joined")
	require.Contains(t, out, "network: RogerAI public")
	require.NotContains(t, out, "local network")
}

func TestInitRequiresDirAndValidMode(t *testing.T) {
	_, err := runCLI(t, "init", "--mode", "standalone")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--dir is required")

	_, err = runCLI(t, "init", "--dir", t.TempDir(), "--mode", "hybrid")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--mode")

	_, err = runCLI(t, "init", "--dir", filepath.Join(t.TempDir(), "x"), "--mode", "")
	require.Error(t, err)
}

func TestStatusRequiresAnInitializedDirectory(t *testing.T) {
	_, err := runCLI(t, "status", "--dir", t.TempDir())
	require.Error(t, err)

	_, err = runCLI(t, "status")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--dir is required")
}

func TestConfigValidate(t *testing.T) {
	p := writeConfig(t, standaloneYAML)
	out, err := runCLI(t, "config", "validate", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "valid for standalone mode")
}

func TestConfigValidateRejectsCrossModeFields(t *testing.T) {
	p := writeConfig(t, standaloneYAML+"joined:\n  authority: https://broker.rogerai.fm\n")
	_, err := runCLI(t, "config", "validate", "--config", p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "standalone mode accepts no joined configuration")
}

func TestConfigPrintIsAlwaysRedacted(t *testing.T) {
	p := writeConfig(t, joinedYAML)
	out, err := runCLI(t, "config", "print", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "mode: joined")
	require.Contains(t, out, "contents not read")

	// There is deliberately no way to ask for an unredacted dump.
	_, err = runCLI(t, "config", "print", "--config", p, "--redact=false")
	require.Error(t, err)
	require.Contains(t, err.Error(), "always printed secret-safe")
}

func TestConfigSubcommandErrors(t *testing.T) {
	_, err := runCLI(t, "config")
	require.Error(t, err)

	p := writeConfig(t, standaloneYAML)
	_, err = runCLI(t, "config", "explode", "--config", p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown config subcommand")

	_, err = runCLI(t, "config", "validate")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--config is required")

	_, err = runCLI(t, "config", "validate", "--config", filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err)
}

func TestDoctorReportsLocalStandalone(t *testing.T) {
	p := writeConfig(t, standaloneYAML+"hub:\n  address: 127.0.0.1:8444\n")
	out, err := runCLI(t, "doctor", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "mode: standalone")
	require.Contains(t, out, "no connection")
	require.Contains(t, strings.ToLower(out), "loopback only")
	require.Contains(t, out, "doctor: OK")
}

// A Tower that declares no data plane is told it relays nothing - and is NOT told its
// listeners are safely on loopback, because it has none and the question was never asked.
func TestDoctorSaysWhenThereIsNothingToRelay(t *testing.T) {
	p := writeConfig(t, standaloneYAML)
	out, err := runCLI(t, "doctor", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "listeners: none")
	require.Contains(t, out, "takes no load off Roger Core")
	require.NotContains(t, strings.ToLower(out), "loopback only")
}

// Every control this build ignores is named, at the command line an operator actually runs.
func TestDoctorNamesIgnoredControls(t *testing.T) {
	p := writeConfig(t, standaloneYAML+"limits:\n  maxInflight: 8\n")
	out, err := runCLI(t, "doctor", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "IGNORED: limits.maxInflight")
}

func TestDoctorReportsJoinedReachability(t *testing.T) {
	p := writeConfig(t, joinedYAML)
	out, err := runCLI(t, "doctor", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "connects to https://broker.rogerai.fm")
}

func TestDoctorFlagsANonLoopbackBind(t *testing.T) {
	p := writeConfig(t, standaloneYAML+"hub:\n  address: 0.0.0.0:8444\n")
	out, err := runCLI(t, "doctor", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "NOT loopback")
	require.Contains(t, out, "reachable from other hosts")
}

func TestDoctorRequiresConfig(t *testing.T) {
	_, err := runCLI(t, "doctor")
	require.Error(t, err)
}

// --- bootstrap ------------------------------------------------------------

func initStandalone(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "t")
	_, err := runCLI(t, "init", "--dir", dir, "--mode", "standalone")
	require.NoError(t, err)
	return dir
}

// The code is printed exactly once, with a warning that says so. An operator who loses
// it must mint a new invitation, not go looking for the old one.
func TestInviteShowsTheCodeOnceWithAWarning(t *testing.T) {
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir, "--client", "k1")
	require.NoError(t, err)
	require.Contains(t, out, "invitation: ")
	require.Contains(t, out, "code: ")
	require.Contains(t, out, "shown once")
}

func TestInviteThenAdmitRoundTrip(t *testing.T) {
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir, "--client", "k1")
	require.NoError(t, err)
	id, code := parseInvite(t, out)

	out, err = runCLI(t, "admit", "--dir", dir, "--client", "k1", "--id", id, "--code", code)
	require.NoError(t, err)
	require.Contains(t, out, "admitted as local_operator")
	require.Contains(t, out, "pinned offline-root fingerprint:")
}

func TestAdmitRejectsAWrongCodeUniformly(t *testing.T) {
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir, "--client", "k1")
	require.NoError(t, err)
	id, code := parseInvite(t, out)

	_, wrongCode := runCLI(t, "admit", "--dir", dir, "--client", "k1", "--id", id, "--code", "WRONGWRONGWRONGWRONGWRONGWR")
	require.Error(t, wrongCode)
	_, wrongClient := runCLI(t, "admit", "--dir", dir, "--client", "k2", "--id", id, "--code", code)
	require.Error(t, wrongClient)
	require.Equal(t, wrongCode.Error(), wrongClient.Error(), "rejections must be indistinguishable")
}

func TestInviteRequiresStandaloneAndADirectory(t *testing.T) {
	joined := filepath.Join(t.TempDir(), "j")
	_, err := runCLI(t, "init", "--dir", joined, "--mode", "joined")
	require.NoError(t, err)

	_, err = runCLI(t, "invite", "--dir", joined, "--client", "k1")
	require.Error(t, err, "a joined Tower's clients are admitted by Roger Core")

	_, err = runCLI(t, "invite", "--client", "k1")
	require.Error(t, err)
	_, err = runCLI(t, "admit", "--client", "k1")
	require.Error(t, err)
}

func parseInvite(t *testing.T, out string) (id, code string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "invitation: "); ok {
			id = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "code: "); ok {
			code = strings.TrimSpace(v)
		}
	}
	require.NotEmpty(t, id)
	require.NotEmpty(t, code)
	return id, code
}

// --- local stations and routing -------------------------------------------

func bootstrappedDir(t *testing.T) string {
	t.Helper()
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir, "--client", "alice")
	require.NoError(t, err)
	id, code := parseInvite(t, out)
	_, err = runCLI(t, "admit", "--dir", dir, "--client", "alice", "--id", id, "--code", code)
	require.NoError(t, err)
	return dir
}

func TestAttachListAndRoute(t *testing.T) {
	dir := bootstrappedDir(t)

	out, err := runCLI(t, "attach", "--dir", dir, "--station", "st-1", "--key", "sk1", "--models", "llama-8b, qwen")
	require.NoError(t, err)
	require.Contains(t, out, "attached station st-1")
	require.Contains(t, out, "local network local-")

	out, err = runCLI(t, "stations", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "st-1")
	require.Contains(t, out, "llama-8b,qwen")

	out, err = runCLI(t, "route", "--dir", dir, "--client", "alice", "--model", "llama-8b")
	require.NoError(t, err)
	require.Contains(t, out, "served by station st-1")
	require.Contains(t, out, "local network local-")
	// The receipt must never read as RogerAI-verified.
	require.NotContains(t, strings.ToLower(out), "rogerai")
}

func TestStationsIsEmptyBeforeAnyAttach(t *testing.T) {
	dir := bootstrappedDir(t)
	out, err := runCLI(t, "stations", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "no stations attached")
}

func TestRouteRefusesAnUnadmittedClientAndUnknownModel(t *testing.T) {
	dir := bootstrappedDir(t)
	_, err := runCLI(t, "attach", "--dir", dir, "--station", "st-1", "--key", "sk1", "--models", "m")
	require.NoError(t, err)

	_, err = runCLI(t, "route", "--dir", dir, "--client", "mallory", "--model", "m")
	require.Error(t, err, "a standalone Tower is not an open relay")

	_, err = runCLI(t, "route", "--dir", dir, "--client", "alice", "--model", "not-offered")
	require.Error(t, err)
}

func TestStationCommandsRequireADirectory(t *testing.T) {
	for _, args := range [][]string{
		{"attach", "--station", "s", "--key", "k", "--models", "m"},
		{"stations"},
		{"route", "--client", "c", "--model", "m"},
	} {
		_, err := runCLI(t, args...)
		require.Error(t, err, "%v must require --dir", args)
	}
}

// --- accounts: standalone needs none, joined requires one -----------------

func TestStandaloneRefusesLoginBecauseItWouldDoNothing(t *testing.T) {
	dir := initStandalone(t)
	_, err := runCLI(t, "login", "--dir", dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "needs no account")
	require.Contains(t, err.Error(), "leaves this machine")
}

func TestStandaloneCannotRegister(t *testing.T) {
	dir := initStandalone(t)
	_, err := runCLI(t, "register", "--dir", dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "standalone")
	require.Contains(t, err.Error(), "--mode joined")
}

func TestJoinedRegisterRequiresSigningInFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	_, err := runCLI(t, "init", "--dir", dir, "--mode", "joined")
	require.NoError(t, err)

	_, err = runCLI(t, "register", "--dir", dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sign in first")
	require.Contains(t, err.Error(), "accountable")
}

func TestLogoutIsHarmlessAndPreservesIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	_, err := runCLI(t, "init", "--dir", dir, "--mode", "joined")
	require.NoError(t, err)

	out, err := runCLI(t, "logout", "--dir", dir)
	require.NoError(t, err, "signing out when not signed in must be harmless")
	require.Contains(t, out, "identity and data directory are untouched")

	out, err = runCLI(t, "status", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "mode: joined")
}

func TestAccountCommandsRequireADirectory(t *testing.T) {
	for _, c := range []string{"login", "logout", "register"} {
		_, err := runCLI(t, c)
		require.Error(t, err, "%s must require --dir", c)
	}
}

// The usage must tell a newcomer which mode they want, and say plainly that the
// try-it-now path costs no account.
func TestUsageExplainsTheAccountLine(t *testing.T) {
	out, err := runCLI(t, "help")
	require.NoError(t, err)
	require.Contains(t, out, "Standalone needs NO account")
	require.Contains(t, out, "accountable")
}

// A joined Tower signs in through the broker and records the account. No provider name
// appears anywhere in the path - that is the point of the brokered flow.
func TestJoinedLoginSignsInAndPointsAtRegister(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	_, err := runCLI(t, "init", "--dir", dir, "--mode", "joined")
	require.NoError(t, err)

	prev := deviceLogin
	deviceLogin = func(string) (string, error) { return "alice", nil }
	t.Cleanup(func() { deviceLogin = prev })

	out, err := runCLI(t, "login", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "signed in as @alice")
	require.Contains(t, out, "register --dir")

	// Now signed in, register really attempts enrollment. The point is that the path is
	// WIRED: a real request goes to the broker's real endpoint, and the error a person sees
	// describes the broker's real answer rather than a feature that does not exist.
	//
	// The broker here is a local stub, and that is the whole fix. This assertion used to run
	// against whatever the broker base defaulted to, which is production. It "passed" only
	// where the broker was unreachable; on a connected machine the enrollment SUCCEEDED, so
	// the expected error never came - a unit test that called the live network and whose
	// verdict depended on the network conditions of whoever ran it.
	var hit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"this account may not run a Tower"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)

	_, err = runCLI(t, "register", "--dir", dir)
	require.Error(t, err)
	require.Equal(t, "/tower/token", hit,
		"registration must actually call the broker - an error from anywhere else would satisfy "+
			"the assertion below without the path being wired at all")
	require.NotContains(t, err.Error(), "not implemented",
		"registration is implemented; a failure here must describe the real problem")
}

func TestJoinedLoginSurfacesAFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	_, err := runCLI(t, "init", "--dir", dir, "--mode", "joined")
	require.NoError(t, err)

	prev := deviceLogin
	deviceLogin = func(string) (string, error) { return "", errLoginRefused }
	t.Cleanup(func() { deviceLogin = prev })

	_, err = runCLI(t, "login", "--dir", dir)
	require.ErrorIs(t, err, errLoginRefused)

	// A failed sign-in stores nothing, so register still refuses.
	_, err = runCLI(t, "register", "--dir", dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sign in first")
}

var errLoginRefused = errors.New("the sign-in was denied")

func TestEnvOrPrefersTheEnvironment(t *testing.T) {
	require.Equal(t, "fallback", envOr("ROGER_TOWER_TEST_UNSET_VAR", "fallback"))
	t.Setenv("ROGER_TOWER_TEST_SET_VAR", "chosen")
	require.Equal(t, "chosen", envOr("ROGER_TOWER_TEST_SET_VAR", "fallback"))
}

// Two processes must not both be admitted as the single local operator. The lock is what
// makes that true across processes - an in-process mutex cannot see another process.
func TestASecondProcessCannotOwnTheSameDataDirectory(t *testing.T) {
	dir := initStandalone(t)

	st, err := tower.Open(dir)
	require.NoError(t, err)
	release, err := st.Lock()
	require.NoError(t, err)

	_, err = runCLI(t, "invite", "--dir", dir, "--client", "k1")
	require.Error(t, err, "a command must not run while another process owns the directory")
	require.Contains(t, err.Error(), "already owns")

	require.NoError(t, release())
	_, err = runCLI(t, "invite", "--dir", dir, "--client", "k1")
	require.NoError(t, err, "once released, the directory is usable again")
}

// --- durable startup preflight --------------------------------------------

func TestReadyWarnsLoudlyOnTheDevelopmentProfile(t *testing.T) {
	p := writeConfig(t, standaloneYAML)
	out, err := runCLI(t, "ready", "--config", p)
	require.NoError(t, err, "a development Tower is usable")
	require.Contains(t, out, "NOT DURABLE")
	require.Contains(t, out, "READY")
}

// The point of the preflight: it must FAIL, not warn, when a durable Tower's state
// cannot be kept - and it must say what to do about it.
func TestReadyFailsClosedWithARepairInstruction(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-volume")
	p := writeConfig(t, standaloneYAML+"storage:\n  profile: durable\nidentity:\n  dir: "+missing+"\n")

	out, err := runCLI(t, "ready", "--config", p)
	require.Error(t, err, "a durable Tower with no identity volume must refuse to serve")
	require.Contains(t, out, "NOT READY")
	require.Contains(t, out, "identity volume")
	require.Contains(t, out, "repair:")
}

func TestReadyPassesForACompleteDurableTower(t *testing.T) {
	dir := initStandalone(t)
	p := writeConfig(t, standaloneYAML+"storage:\n  profile: durable\nidentity:\n  dir: "+dir+"\n")

	out, err := runCLI(t, "ready", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "profile: durable")
	require.Contains(t, out, "READY")
	require.NotContains(t, out, "NOT READY")
}

func TestReadyRequiresAConfig(t *testing.T) {
	_, err := runCLI(t, "ready")
	require.Error(t, err)
}

// --- store selection -------------------------------------------------------

func TestStoreForUsesTheDataDirectoryWhenNoDatabaseIsConfigured(t *testing.T) {
	dir := initStandalone(t)
	st, err := tower.Open(dir)
	require.NoError(t, err)

	c, err := tower.ParseConfig([]byte(standaloneYAML))
	require.NoError(t, err)

	got, closeFn, err := storeFor(c, st)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NoError(t, closeFn())
}

func TestStoreForRefusesAnUnreadableDatabaseSecret(t *testing.T) {
	dir := initStandalone(t)
	st, err := tower.Open(dir)
	require.NoError(t, err)

	c, err := tower.ParseConfig([]byte(standaloneYAML + "storage:\n  urlFile: /nonexistent/db-url\n"))
	require.NoError(t, err)

	_, _, err = storeFor(c, st)
	require.Error(t, err, "a Tower must not start against a database secret it cannot read")
	require.Contains(t, err.Error(), "database URL file")
}

// The allowlist applies to the database too: a Tower pointed at a hosted database is no
// longer the standalone thing it claims to be.
func TestStoreForRefusesAPublicDatabase(t *testing.T) {
	dir := initStandalone(t)
	st, err := tower.Open(dir)
	require.NoError(t, err)

	secret := filepath.Join(t.TempDir(), "db-url")
	require.NoError(t, os.WriteFile(secret, []byte("postgres://u:p@db.example.com:5432/tower"), 0o600))

	c, err := tower.ParseConfig([]byte(standaloneYAML + "storage:\n  urlFile: " + secret + "\n"))
	require.NoError(t, err)

	_, _, err = storeFor(c, st)
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowlist")
}

// --- durable storage is actually WIRED IN ----------------------------------
//
// storeFor was written, tested, and called by nothing. A Tower configured with the durable
// storage profile silently kept its state in the data directory - the one deployment shape
// the profile exists for, a node whose disk is not durable - and nothing failed. The tests
// below are the ones that were missing: they exercise the COMMANDS, not the helper.

// commandsThatTouchState lists every command that reads or writes local-admission state,
// with enough arguments to get past its own flag parsing. A new one that forgets --config
// fails here rather than in a deployment.
func commandsThatTouchState(dir, cfg string) [][]string {
	return [][]string{
		{"invite", "--dir", dir, "--config", cfg, "--client", "abc"},
		{"admit", "--dir", dir, "--config", cfg, "--client", "abc", "--id", "i", "--code", "c"},
		{"attach", "--dir", dir, "--config", cfg, "--station", "s1", "--key", "k", "--models", "m"},
		{"stations", "--dir", dir, "--config", cfg},
		{"route", "--dir", dir, "--config", cfg, "--client", "abc", "--model", "m"},
		{"serve", "--dir", dir, "--config", cfg},
	}
}

// Every state-touching command must FAIL CLOSED when the configured database secret cannot
// be read. Falling back to the data directory is what made this bug invisible: the operator
// gets a Tower that looks configured and loses its operator credential, verifier secret and
// Station registry the first time the node is replaced.
func TestEveryStateCommandRefusesAnUnreadableDatabaseSecret(t *testing.T) {
	dir := initStandalone(t)
	cfg := writeConfig(t, standaloneYAML+"storage:\n  urlFile: /nonexistent/db-url\n")

	for _, args := range commandsThatTouchState(dir, cfg) {
		out, err := runCLI(t, args...)
		require.Error(t, err, "%s ignored --config", args[0])
		require.Contains(t, err.Error(), "database URL file",
			"%s failed for the wrong reason", args[0])
		require.NotContains(t, out, "invitation:", "%s did work before failing", args[0])
	}
}

// The same commands must refuse a database that is not local. A Tower pointed at a hosted
// database is not the standalone thing it claims to be, and the check is worthless if the
// commands never reach it.
func TestEveryStateCommandRefusesAPublicDatabase(t *testing.T) {
	dir := initStandalone(t)
	secret := filepath.Join(t.TempDir(), "db-url")
	require.NoError(t, os.WriteFile(secret, []byte("postgres://u:p@db.example.com:5432/tower"), 0o600))
	cfg := writeConfig(t, standaloneYAML+"storage:\n  urlFile: "+secret+"\n")

	for _, args := range commandsThatTouchState(dir, cfg) {
		_, err := runCLI(t, args...)
		require.Error(t, err, "%s ignored --config", args[0])
		require.Contains(t, err.Error(), "allowlist", "%s failed for the wrong reason", args[0])
	}
}

// Without --config nothing changes: the data directory is still the store, which is correct
// for a single node and is what every existing deployment does.
func TestWithoutAConfigTheDataDirectoryIsStillTheStore(t *testing.T) {
	dir := initStandalone(t)
	out, err := runCLI(t, "invite", "--dir", dir, "--client", "abc")
	require.NoError(t, err)
	require.Contains(t, out, "invitation:")
}

// END TO END against a real database: state written with --config lands THERE, and is not
// in the data directory. Asserting both halves is the point - a test that only checked the
// happy path would pass just as well against the silent file-store fallback this fixes.
func TestDurableStorageKeepsStateInTheDatabaseAndNotOnDisk(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ROGERAI_TEST_DATABASE_URL to exercise the durable store")
	}
	dir := initStandalone(t)
	secret := filepath.Join(t.TempDir(), "db-url")
	require.NoError(t, os.WriteFile(secret, []byte(privateDSN(t, dsn)), 0o600))
	cfg := writeConfig(t, standaloneYAML+"storage:\n  urlFile: "+secret+"\n")

	// Bootstrap the local operator first - a network with none may not attach Stations. Doing
	// it through the CLI is deliberate: it means the invitation, its one-use consumption and
	// the Station all have to survive the round trip through the database, which is three
	// separate read-modify-write cycles rather than one.
	out, err := runCLI(t, "invite", "--dir", dir, "--config", cfg, "--client", "kh-op")
	require.NoError(t, err)
	id, code := parseInvite(t, out)

	_, err = runCLI(t, "admit", "--dir", dir, "--config", cfg,
		"--client", "kh-op", "--id", id, "--code", code)
	require.NoError(t, err)

	_, err = runCLI(t, "attach", "--dir", dir, "--config", cfg,
		"--station", "st-durable", "--key", "kh-1", "--models", "m1")
	require.NoError(t, err)

	out, err = runCLI(t, "stations", "--dir", dir, "--config", cfg)
	require.NoError(t, err)
	require.Contains(t, out, "st-durable", "the database did not keep what we wrote")

	// And the data directory knows nothing about it. If this passes with the Station also on
	// disk, the command is writing both and the durable profile is decorative.
	out, err = runCLI(t, "stations", "--dir", dir)
	require.NoError(t, err)
	require.NotContains(t, out, "st-durable", "the Station was written to local disk as well")
}

// privateDSN redirects THIS package's Postgres test to its own database.
//
// `go test ./...` runs PACKAGES in parallel against the one shared
// ROGERAI_TEST_DATABASE_URL, and a Tower snapshot written into the shared database is a row
// some other package's suite is not expecting. internal/store and internal/towercore/attach
// both hit this and solved it the same way; it cost a long diagnosis the first time, because
// the failure surfaces in the OTHER package as something inexplicable.
//
// A DSN that does not parse as a URL keeps the shared-database behaviour.
var privateDBOnce sync.Once

func privateDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_rogertower"
	privateDBOnce.Do(func() {
		admin, aerr := sql.Open("pgx", dsn)
		if aerr != nil {
			t.Fatalf("private db: open admin: %v", aerr)
		}
		defer admin.Close()
		// DROP FIRST, so every run starts on a clean network.
		//
		// This used to only CREATE and tolerate "already exists", which is fine on CI - a
		// fresh Postgres service per job - and wrong anywhere the database outlives the
		// run. A Tower's bootstrap is ONE-TIME per network and it is DURABLE by the very
		// nature of what this test asserts, so the second `go test` against the same
		// server found an operator already admitted and failed with "bootstrap rejected".
		//
		// That is worse than a flake: it makes the release gate pass once and fail
		// afterwards, so the person running it twice before a push cannot tell a real
		// break from their own leftovers. Clean-slate semantics per run, matching what
		// internal/store's private database already does.
		if _, derr := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `"`); derr != nil {
			t.Fatalf("private db: drop %s: %v", name, derr)
		}
		if _, cerr := admin.Exec(`CREATE DATABASE "` + name + `"`); cerr != nil &&
			!strings.Contains(cerr.Error(), "already exists") {
			t.Fatalf("private db: create %s: %v", name, cerr)
		}
	})
	u.Path = "/" + name
	return u.String()
}

// Every subcommand refuses a flag it does not define, and the refusal reaches the caller as
// an error rather than being swallowed. One test, every door: these branches are identical
// by construction, and each was individually uncovered - which is exactly the kind of gap
// that invites "handle Parse's error later" on the next subcommand added.
func TestEverySubcommandRefusesAnUnknownFlag(t *testing.T) {
	for _, cmd := range []string{
		"init", "doctor", "ready", "invite", "admit", "attach", "stations", "route",
		"login", "logout", "register", "probe", "status", "earnings", "serve",
		"drain", "resume", "revoke",
	} {
		t.Run(cmd, func(t *testing.T) {
			var b bytes.Buffer
			err := run([]string{cmd, "--no-such-flag"}, &b)
			require.Error(t, err, "%s accepted a flag it does not define", cmd)
		})
	}
	// The config group parses one level down.
	for _, sub := range []string{"validate", "print"} {
		var b bytes.Buffer
		require.Error(t, run([]string{"config", sub, "--no-such-flag"}, &b))
	}
}

// A running `serve` holds the data directory's EXCLUSIVE lock for its whole lifetime. That
// lock exists to keep two WRITERS (two servers, or a server and an attach) from corrupting
// one identity's session and registry - it must not also lock out an operator who only
// wants to READ what is attached. Before the fix, `stations` went through the same
// exclusive-lock open path as `attach`, so `roger-tower stations` printed "another Tower
// process already owns ..." the instant a Tower was actually serving - exactly when an
// operator most wants to look.
func TestReadOnlyCommandsRunWhileServeHoldsTheLock(t *testing.T) {
	dir := bootstrappedDir(t)
	_, err := runCLI(t, "attach", "--dir", dir, "--station", "st-1", "--key", "sk1", "--models", "llama-8b")
	require.NoError(t, err)

	// Simulate a running serve: hold the exclusive data-directory lock for the rest of the test.
	st, err := tower.Open(dir)
	require.NoError(t, err)
	release, err := st.Lock()
	require.NoError(t, err, "precondition: the lock is free before serve takes it")
	defer func() { _ = release() }()

	// READ-ONLY: stations must succeed and show the attachment, not the lock error.
	out, err := runCLI(t, "stations", "--dir", dir)
	require.NoError(t, err, "a read-only command must not be blocked by serve's lock")
	require.NotContains(t, out, "already owns")
	require.Contains(t, out, "st-1")

	// WRITER: attach must STILL fail-fast while the lock is held - the guarantee the lock exists for.
	_, err = runCLI(t, "attach", "--dir", dir, "--station", "st-2", "--key", "sk2", "--models", "m")
	require.Error(t, err, "a writer must still be refused while another process owns the directory")
	require.Contains(t, err.Error(), "already owns")
}

// The lock fix must cover EVERY locally-read-only command an operator runs against a
// Tower that is actively serving, not just `stations`. `drain`, `resume`, `revoke` and
// `station revoke` only POST to Core (they write nothing locally), and `route` computes a
// receipt without persisting - yet all took the exclusive lock, so each printed "already
// owns" against a running Tower. `drain`'s whole purpose is to quiet a SERVING Tower, and
// `route` is how a standalone client is served WHILE serve runs, so this was a real defect.
func TestReadOnlyLifecycleCommandsRunWhileServeHoldsTheLock(t *testing.T) {
	// route: standalone, local, served while serve holds the lock.
	t.Run("route", func(t *testing.T) {
		dir := bootstrappedDir(t)
		_, err := runCLI(t, "attach", "--dir", dir, "--station", "st-1", "--key", "sk1", "--models", "llama-8b")
		require.NoError(t, err)
		st, err := tower.Open(dir)
		require.NoError(t, err)
		release, err := st.Lock()
		require.NoError(t, err)
		defer func() { _ = release() }()

		out, err := runCLI(t, "route", "--dir", dir, "--client", "alice", "--model", "llama-8b")
		require.NoError(t, err, "a standalone client must be routable while serve holds the lock")
		require.NotContains(t, out, "already owns")
		require.Contains(t, out, "served by station st-1")
	})

	// drain: joined, POST-only to Core, run against a serving Tower.
	t.Run("drain", func(t *testing.T) {
		core := newCoreStub(t)
		dir := joinedRegisteredTower(t)
		core.reply["/tower/self/lifecycle"] = func(w http.ResponseWriter, _ int) bool {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "state": "draining"})
			return true
		}
		st, err := tower.Open(dir)
		require.NoError(t, err)
		release, err := st.Lock()
		require.NoError(t, err)
		defer func() { _ = release() }()

		out, err := runCLI(t, "drain", "--dir", dir)
		require.NoError(t, err, "drain must reach Core while serve holds the lock - draining a serving Tower is its purpose")
		require.NotContains(t, out, "already owns")
		require.Contains(t, core.seen, "/tower/self/lifecycle", "drain must actually reach Core")
	})
}
