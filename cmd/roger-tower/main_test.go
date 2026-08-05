package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/tower"
)

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

	out, err = runCLI(t, "version")
	require.NoError(t, err)
	require.Equal(t, "dev\n", out)
}

func TestUnknownCommandFails(t *testing.T) {
	_, err := runCLI(t, "frobnicate")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown command")
}

// Commands that need the joined RELAY LINK must say so, not fail obscurely or pretend to
// work - and they must not imply that registration is missing too, because it is not.
// Telling an operator "not implemented" about the whole of joined mode would send them
// looking for a workaround they do not need.
func TestJoinedOnlyCommandsNameTheMissingHalf(t *testing.T) {
	for _, c := range []string{"serve", "drain", "revoke"} {
		_, err := runCLI(t, c)
		require.Error(t, err)
		require.Contains(t, err.Error(), "relay link", "it names what is missing")
		require.Contains(t, err.Error(), "register", "and what already works")
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
	p := writeConfig(t, standaloneYAML)
	out, err := runCLI(t, "doctor", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "mode: standalone")
	require.Contains(t, out, "no connection")
	require.Contains(t, strings.ToLower(out), "loopback only")
	require.Contains(t, out, "doctor: OK")
}

func TestDoctorReportsJoinedReachability(t *testing.T) {
	p := writeConfig(t, joinedYAML)
	out, err := runCLI(t, "doctor", "--config", p)
	require.NoError(t, err)
	require.Contains(t, out, "connects to https://broker.rogerai.fm")
}

func TestDoctorFlagsANonLoopbackBind(t *testing.T) {
	p := writeConfig(t, standaloneYAML+"stationListener:\n  address: 0.0.0.0:7070\n")
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

	// Now signed in, register really attempts enrollment against the broker. With no broker
	// reachable it fails on the CALL rather than on a placeholder - which is the point: the
	// path is wired, and the error a person sees is about the network, not about a feature
	// that does not exist.
	_, err = runCLI(t, "register", "--dir", dir)
	require.Error(t, err)
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
