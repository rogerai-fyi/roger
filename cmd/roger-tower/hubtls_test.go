package main

// hubtls_test.go is the tower's half of the change: a hub that actually serves TLS, and a
// fingerprint that actually describes the certificate it serves.
//
// The old --hub-tls-cert flags were not merely unused, they were a TRAP - a listener no node in
// the fleet could reach, because a Tower advertises bare host:port and every client concatenated
// "http://" onto it. An operator who went to the trouble of obtaining a certificate took their
// own tower off the air. So the tests here are about the JOIN between the two halves: what the
// listener presents must be exactly what the link advertises, or the trap is still open with a
// longer fuse.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// THE JOIN. The hub is started through the real runHubInBackground with a real minted
// certificate, and then dialled by a client pinned to the fingerprint hubTLS reported - and by
// one pinned to a different certificate, which must be refused.
//
// The second half is what makes this a test rather than a demonstration. Against a listener
// serving TLS and a client that skips verification, the first assertion passes and nothing has
// been proved; only the refusal distinguishes a verified channel from an encrypted one.
func TestTheHubServesTLSUnderExactlyTheFingerprintItAdvertises(t *testing.T) {
	core := newCoreStub(t)
	core.answerDispatchKey(t)
	core.answerHubNodes(`{"nodes":[]}`)
	st := servingTower(t)

	mat, err := hubTLS(st.Dir(), st.TowerID, "", "")
	require.NoError(t, err)
	require.True(t, towerhub.ValidPin(mat.Pin))

	addr := freeAddr(t)
	var out syncBuffer
	stop := make(chan struct{})
	wait, err := runHubInBackground(st, hubOptions{Addr: addr, TLS: true, cert: &mat.Cert,
		AllowLegacyBearer: true}, &out, stop)
	require.NoError(t, err)
	t.Cleanup(func() { close(stop); wait() })

	base, hc, err := towerhub.Reach(addr, mat.Pin, &http.Client{Timeout: 2 * time.Second})
	require.NoError(t, err)
	var last error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, rerr := hc.Get(base + towerhub.PathPoll)
		if rerr == nil {
			resp.Body.Close()
			last = nil
			break
		}
		last = rerr
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, last, "the advertised pin must be the certificate this listener presents")

	// A DIFFERENT CERTIFICATE'S PIN IS REFUSED. This is the on-path attacker and equally a
	// tower that swapped its key without telling Core.
	other, err := hubTLS(t.TempDir(), "tw-other", "", "")
	require.NoError(t, err)
	require.NotEqual(t, mat.Pin, other.Pin)
	_, wrong, err := towerhub.Reach(addr, other.Pin, &http.Client{Timeout: 2 * time.Second})
	require.NoError(t, err)
	_, err = wrong.Get(base + towerhub.PathPoll)
	require.ErrorIs(t, err, towerhub.ErrHubCertificateUnpinned)

	// AND NO HUB ROUTE IS SERVED IN PLAINTEXT, which is the other direction of the same
	// property: an operator who turns TLS on has not left the old door open beside the new one.
	// Go's TLS listener answers a plain-http request with a 400 rather than by hanging up, so
	// the assertion is on what came back and not merely on there being an error - a nil error
	// here is a connection, not a service.
	plainResp, perr := http.Get("http://" + addr + towerhub.PathPoll)
	require.NoError(t, perr)
	defer plainResp.Body.Close()
	require.Equal(t, http.StatusBadRequest, plainResp.StatusCode,
		"a TLS hub must not serve a hub route to a plaintext caller")
}

// A MINTED CERTIFICATE IS REMEMBERED. The pin is the tower's identity to every node attached to
// it, so a fresh key on every restart would take the whole fleet off the air on each redeploy
// until every node re-attached.
func TestAMintedHubCertificateSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	first, err := hubTLS(dir, "tw-1", "", "")
	require.NoError(t, err)
	second, err := hubTLS(dir, "tw-1", "", "")
	require.NoError(t, err)
	require.Equal(t, first.Pin, second.Pin, "a restart must not change what nodes are pinned to")

	if os.Geteuid() != 0 {
		info, serr := os.Stat(filepath.Join(dir, hubKeyFile))
		require.NoError(t, serr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
			"private material in the operator's data directory is owner-only")
	}
}

// AN OPERATOR'S OWN CERTIFICATE IS USED AS GIVEN, and pinned to just the same. Nothing here
// requires a self-signed one - what it requires is that whatever is served is what is
// advertised.
func TestAnOperatorSuppliedCertificateIsPinnedToo(t *testing.T) {
	dir := t.TempDir()
	mine, err := hubTLS(dir, "tw-1", "", "")
	require.NoError(t, err)

	elsewhere := t.TempDir()
	loaded, err := hubTLS(elsewhere, "tw-1", filepath.Join(dir, hubCertFile), filepath.Join(dir, hubKeyFile))
	require.NoError(t, err)
	require.Equal(t, mine.Pin, loaded.Pin)
	require.NoFileExists(t, filepath.Join(elsewhere, hubCertFile),
		"an operator who supplied a certificate must not also get a minted one")
}

// STANDALONE IS UNTOUCHED, AND THAT IS A REQUIREMENT RATHER THAN AN ACCIDENT.
//
// features/tower/modes.feature: a standalone Tower is a private local network with its own
// trust root, binds loopback, and performs no RogerAI DNS lookup or network connection. Its
// plaintext loopback hub is legitimate and stays legal. This asserts the stronger property that
// makes that safe to rely on: asked for TLS, in joined-mode terms, a standalone data directory
// is refused before ANY file is written and before Core is reached - so nothing about this
// change can be provoked into touching a standalone tower's state.
func TestAStandaloneTowerAskedForTLSWritesNothingAndReachesNothing(t *testing.T) {
	core := newCoreStub(t)
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "standalone"}, &b))
	st, release, err := openDir(dir)
	require.NoError(t, err)
	defer release()

	err = serveJoined(st, &b, "203.0.113.9:8444", hubOptions{Addr: "127.0.0.1:0", TLS: true,
		AllowLegacyBearer: true})
	require.ErrorIs(t, err, errStandaloneCannotServeJoined)
	require.Zero(t, core.reached(), "a standalone Tower called Roger Core before being refused")
	require.NoFileExists(t, filepath.Join(dir, hubKeyFile),
		"a standalone Tower minted a hub TLS key on the strength of a joined-mode flag")
}

// THE PIN REACHES CORE, which is the advertisement half. A certificate the fleet is never told
// about is a hub every client still dials in plaintext - the exact defect this change removes,
// reintroduced one layer up.
func TestTheAdvertisedHelloCarriesTheHubPin(t *testing.T) {
	core := newCoreStub(t)
	st := servingTower(t)

	mat, err := hubTLS(st.Dir(), st.TowerID, "", "")
	require.NoError(t, err)
	var b syncBuffer
	require.NoError(t, runLink(st, &b, closedStop(), manualTicker(nil),
		link.RelayPlane{Endpoint: "203.0.113.9:8444", TLSSPKI: mat.Pin}))

	var hello link.Hello
	require.NoError(t, json.Unmarshal(core.body("/tower/session"), &hello))
	require.Equal(t, "203.0.113.9:8444", hello.RelayEndpoint)
	require.Equal(t, mat.Pin, hello.RelayTLSSPKI,
		"Core cannot publish a pin it was never given, and a node cannot verify a hub without one")
}

// The flag combination an operator can get wrong is refused with the same shape the certificate
// flags already were: TLS on a tower with no hub is a listener that does not exist.
func TestHubTLSWithoutAHubIsRefused(t *testing.T) {
	var b bytes.Buffer
	err := cmdServe([]string{"--dir", t.TempDir(), "--hub-tls"}, &b)
	require.ErrorContains(t, err, "no hub listener")
}

// A minted certificate is TLS 1.3-usable Ed25519 material, which is what both ends of this
// connection require. A certificate the listener cannot negotiate with is a hub that fails at
// the handshake for every client at once.
func TestAMintedCertificateNegotiatesTLS13(t *testing.T) {
	mat, err := hubTLS(t.TempDir(), "tw-1", "", "")
	require.NoError(t, err)
	require.NotNil(t, mat.Cert.Leaf)
	cfg, err := towerhub.PinnedTLSConfig(mat.Pin)
	require.NoError(t, err)
	require.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
}

// A minting failure must not leave a half-identity behind. The existence check that decides
// "reuse or mint" looks at the CERTIFICATE, so the invariant that keeps a broken mint
// recoverable is: no certificate unless its key was written first. A certificate with no key
// is what every later run would try to load and fail on, forever; a key with no certificate
// re-mints harmlessly.
func TestAFailedMintLeavesNoCertificateBehind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := mintHubCert(filepath.Join(dir, "hub.crt"), filepath.Join(dir, "hub.key"), "tw-1")
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "hub.crt"))
	require.True(t, os.IsNotExist(statErr),
		"a certificate without its key was left behind: every later run will load it and fail")
}

func TestHubTLSNeedsBothHalvesOfAnOperatorPair(t *testing.T) {
	// One flag without the other is an operator mistake, and honouring half of it would mint
	// a fresh key under a certificate they did not supply - a hub that presents their
	// certificate and cannot complete a handshake with it.
	_, err := hubTLS(t.TempDir(), "tw-1", "/some/cert.pem", "")
	require.ErrorContains(t, err, "both")
	_, err = hubTLS(t.TempDir(), "tw-1", "", "/some/key.pem")
	require.ErrorContains(t, err, "both")
}

func TestAGarbageCertificateOnDiskIsAnErrorNotAServe(t *testing.T) {
	// The minted path is reused WITHOUT QUESTION - including expired - so the one thing that
	// must stop the serve is bytes that cannot be a certificate at all. Discovering that at
	// handshake time would be after the pin was already advertised to Core.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hub.crt"), []byte("not pem"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hub.key"), []byte("not pem"), 0o600))
	_, err := loadHubTLS(filepath.Join(dir, "hub.crt"), filepath.Join(dir, "hub.key"))
	require.Error(t, err)
}

func TestTheMintedIdentityIsStableAcrossServes(t *testing.T) {
	// The pin Core publishes is derived from this certificate, and every node it routes here
	// accepts no other. A hub that re-minted on each start would strand its whole fleet on
	// every restart - so the second resolution must load what the first minted, byte for byte.
	dir := t.TempDir()
	first, err := hubTLS(dir, "tw-1", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, first.Pin)
	second, err := hubTLS(dir, "tw-1", "", "")
	require.NoError(t, err)
	require.Equal(t, first.Pin, second.Pin, "a restart changed the pin: every routed node is now stranded")
}

// The failure edges of the hub's TLS material: half a pair is refused by name, an
// unwritable directory cannot mint, and a minted pair is REUSED rather than re-minted -
// re-minting on every start would rotate the SPKI pin under every attached node.
func TestHubTLSEdges(t *testing.T) {
	_, err := hubTLS(t.TempDir(), "tw-x", "cert-only.pem", "")
	require.Error(t, err, "half a TLS pair must be refused, not guessed at")

	ro := filepath.Join(t.TempDir(), "ro")
	require.NoError(t, os.Mkdir(ro, 0o500))
	_, err = hubTLS(ro, "tw-x", "", "")
	require.Error(t, err, "an unwritable identity directory cannot mint a certificate")

	dir := t.TempDir()
	first, err := hubTLS(dir, "tw-x", "", "")
	require.NoError(t, err)
	again, err := hubTLS(dir, "tw-x", "", "")
	require.NoError(t, err)
	require.Equal(t, first.Pin, again.Pin,
		"a restart must reuse the minted pair: re-minting rotates the pin under every node")
}
