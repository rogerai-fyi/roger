package main

// The joiner half of features/edge/enrollment_root_trust.feature, at the command line: a first
// enrollment keeps the authority's root only once the owner's code (or the root's full
// fingerprint) proves it. REAL local authorities on disk, served over REAL HTTP by the production
// enrollhttp handler; the REAL dispatch on the joiner. An "impostor" is simply another real
// authority with a root of its own.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
)

// rootedAuthority is a real local authority answering over real HTTP. strip removes the root
// proof from every answer, which is what an authority from before proofs (or a device in between
// that strips it) sends.
type rootedAuthority struct {
	local *edgeauth.Local
	url   string
	hits  atomic.Int32
	strip atomic.Bool
}

func newRootedAuthority(t *testing.T, where string) *rootedAuthority {
	t.Helper()
	local, err := edgeauth.Designate(t.TempDir(), where)
	require.NoError(t, err)
	a := &rootedAuthority{local: local}
	h := enrollhttp.Handler(local)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.hits.Add(1)
		if !a.strip.Load() || r.URL.Path != enrollhttp.Path {
			h.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var m map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		delete(m, "root_proof")
		delete(m, "root_proof_status")
		_ = json.NewEncoder(w).Encode(m)
	}))
	t.Cleanup(srv.Close)
	a.url = srv.URL
	return a
}

func (a *rootedAuthority) fingerprint() string { return a.local.Descriptor().Fingerprint }

// allow puts a user key on the allow-list and returns the code allow mints for it.
func (a *rootedAuthority) allow(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	pub := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	require.NoError(t, a.local.Allow(pub))
	code, err := a.local.MintKeyCode(pub)
	require.NoError(t, err)
	return edgeauth.FormatCode(code)
}

// aJoiner is a fresh machine with its own config directory and user key, holding no root.
func aJoiner(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	useTempConfig(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	edgeUserKey = func() ed25519.PrivateKey { return key }
	interactive, stdin, now := edgeIsInteractive, edgeStdin, edgeNow
	edgeIsInteractive = func() bool { return false }
	t.Cleanup(func() {
		edgeUserKey, edgeIsInteractive, edgeStdin, edgeNow = defaultEdgeUserKey, interactive, stdin, now
	})
	return key
}

// keptNothing asserts the joiner holds no Edge identity, root, account or fleet row.
func keptNothing(t *testing.T) {
	t.Helper()
	_, _, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.False(t, ok, "an identity was kept")
	for _, f := range []string{edgeauth.RootCertFile, edgeauth.NodeKeyFile, edgeauth.NodeCertFile, edgeauth.TrustFile} {
		_, err := os.Stat(filepath.Join(edgeAuthDir(), f))
		require.True(t, os.IsNotExist(err), "%s was kept", f)
	}
	st, err := loadEdgeState()
	require.NoError(t, err)
	list, err := st.fleet.List()
	require.NoError(t, err)
	require.Empty(t, list, "a fleet row was kept")
}

func heldRootFP(t *testing.T) string {
	t.Helper()
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok, "no identity was kept")
	return edgeauth.RootFingerprint(id.Root)
}

func TestAFirstJoinWithTheAuthoritysCodeKeepsItsRoot(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	code := shed.allow(t, aJoiner(t))
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", code)
	require.Equal(t, 0, exit, out)
	require.Equal(t, shed.fingerprint(), heldRootFP(t))
	require.Contains(t, out, edgeauth.ShortFingerprint(shed.fingerprint()), "the joiner shows the root it kept")
	require.Contains(t, out, "the code is what was checked")
}

func TestAFirstJoinWithNeitherCodeNorPinIsRefusedBeforeAnythingIsSent(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	shed.allow(t, aJoiner(t))
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url)
	require.Equal(t, 2, exit, out)
	require.Zero(t, shed.hits.Load(), "nothing may leave this machine")
	require.Contains(t, out, "--code")
	require.Contains(t, out, "--root sha256:")
	require.Contains(t, out, "allow or adopt")
	keptNothing(t)
}

func TestAMalformedCodeIsRefusedBeforeAnythingIsSent(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	shed.allow(t, aJoiner(t))
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", "not-a-code")
	require.Equal(t, 2, exit, out)
	require.Zero(t, shed.hits.Load())
	require.Contains(t, out, "XXXXX-XXXXX")
	keptNothing(t)
}

func TestAWrongCodeIsOneAttemptAndKeepsNothing(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	shed.allow(t, aJoiner(t))
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", "ZZZZZ-ZZZZZ")
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "does not match the Edge that answered")
	require.Contains(t, out, "mint a fresh one")
	keptNothing(t)
}

func TestAnImpostorAnsweringFirstIsRefusedAndNothingIsKept(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	impostor := newRootedAuthority(t, "shed")
	key := aJoiner(t)
	code := shed.allow(t, key)
	impostor.allow(t, key) // it lets anyone in, and has a code of its own the owner never saw
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", impostor.url, "--code", code)
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "is not the Edge that showed you this code")
	require.Contains(t, out, edgeauth.ShortFingerprint(impostor.fingerprint()), "it names the root it was offered")
	require.Contains(t, out, strings.TrimPrefix(impostor.url, "http://"), "it names the address that answered")
	require.NotContains(t, out, "different code")
	keptNothing(t)
}

func TestAPinnedJoinKeepsTheRootThatMatchesAndRefusesAnyOther(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	impostor := newRootedAuthority(t, "shed")
	key := aJoiner(t)
	shed.allow(t, key)
	impostor.allow(t, key)

	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", impostor.url, "--root", "sha256:"+shed.fingerprint())
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "does not match the fingerprint you gave")
	require.Contains(t, out, shed.fingerprint())
	require.Contains(t, out, impostor.fingerprint())
	keptNothing(t)

	out, exit = edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--root", "sha256:"+strings.ToUpper(shed.fingerprint()))
	require.Equal(t, 0, exit, out)
	require.Equal(t, shed.fingerprint(), heldRootFP(t))
}

func TestAPrefixOfTheFingerprintIsNotAPin(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	shed.allow(t, aJoiner(t))
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--root", "sha256:"+shed.fingerprint()[:16])
	require.Equal(t, 2, exit, out)
	require.Contains(t, out, "too short to be a pin")
	require.Zero(t, shed.hits.Load())
}

func TestAnAuthorityThatSendsNoProofFailsACodeJoinClosedButHonoursThePin(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	shed.strip.Store(true)
	key := aJoiner(t)
	code := shed.allow(t, key)
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", code)
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "does not prove its root")
	require.Contains(t, out, "--root")
	keptNothing(t)

	out, exit = edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--root", shed.fingerprint())
	require.Equal(t, 0, exit, out)
}

func TestASpentCodeIsSaidToBeUsedOrExpired(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	key := aJoiner(t)
	code := shed.allow(t, key)
	_, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", code)
	require.Equal(t, 0, exit)
	// A second machine under the same allowed key, with the same (now spent) code.
	aJoinerWithKey(t, key)
	out, exit := edgeRun(t, "edge", "enroll", "bench2", "--authority", shed.url, "--code", code)
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "already used")
	require.Contains(t, out, "expired")
	keptNothing(t)
}

func aJoinerWithKey(t *testing.T, key ed25519.PrivateKey) {
	t.Helper()
	useTempConfig(t)
	edgeUserKey = func() ed25519.PrivateKey { return key }
}

func TestAnInteractiveJoinHoldsTheAnswerThenAsksAndForgivesATypo(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	code := shed.allow(t, aJoiner(t))
	edgeIsInteractive = func() bool { return true }
	edgeStdin = strings.NewReader("ZZZZZ-ZZZZZ\n" + strings.ToLower(code) + "\n")
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url)
	require.Equal(t, 0, exit, out)
	require.Contains(t, out, "enter the enrollment code")
	require.Contains(t, out, "does not match the Edge that answered")
	require.Contains(t, out, "4 attempts left")
	require.Equal(t, int32(2), shed.hits.Load(), "one enrollment request and one revocation refresh; the code was typed against the held answer")
	require.Equal(t, shed.fingerprint(), heldRootFP(t))
}

func TestFiveWrongCodesDiscardTheHeldAnswer(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	shed.allow(t, aJoiner(t))
	edgeIsInteractive = func() bool { return true }
	edgeStdin = strings.NewReader(strings.Repeat("ZZZZZ-ZZZZZ\n", 6))
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url)
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "did not match 5 times")
	require.Contains(t, out, "impersonating")
	require.Equal(t, 5, strings.Count(out, "enter the enrollment code"), "a sixth entry is not asked for")
	keptNothing(t)
}

func TestAHeldAnswerIsNotTrustedAfterFifteenMinutes(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	code := shed.allow(t, aJoiner(t))
	edgeIsInteractive = func() bool { return true }
	start := time.Now()
	calls := 0
	edgeNow = func() time.Time {
		calls++
		if calls == 1 {
			return start // the answer arrives
		}
		return start.Add(16 * time.Minute) // the owner types much later
	}
	edgeStdin = strings.NewReader(code + "\n")
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url)
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "run the join again")
	keptNothing(t)
}

func TestTheCorePathRefusesABrokerThatIsNotAuthenticated(t *testing.T) {
	for _, tc := range []struct {
		broker string
		ok     bool
	}{
		{"http://192.168.1.5:8080", false},
		{"http://broker.example:80", false},
		{"https://broker.rogerai.fm", true},
		{"http://127.0.0.1:1", true},
		{"http://localhost:1", true},
		{"http://[::1]:1", true},
	} {
		t.Run(tc.broker, func(t *testing.T) {
			require.Equal(t, tc.ok, edgeBrokerAuthenticated(tc.broker))
		})
	}
	aJoiner(t)
	t.Setenv("ROGER_BROKER", "http://192.168.1.5:8080")
	edgeLogin(t, "owner")
	out, exit := edgeRun(t, "edge", "enroll", "bench")
	require.Equal(t, 1, exit, out)
	require.Contains(t, out, "needs an https broker")
	require.Contains(t, out, "local authority")
	keptNothing(t)
}

// edgeLogin leaves the auth record the CLI reads.
func edgeLogin(t *testing.T, who string) {
	t.Helper()
	dir := filepath.Dir(configPath())
	require.NoError(t, os.MkdirAll(dir, 0o700))
	b, _ := json.Marshal(map[string]any{"github_login": who, "github_id": 42, "bound_at": time.Now().Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600))
}

func TestADifferentRootOnReEnrollNeedsItsCodeReplaceRootAndAYes(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	barn := newRootedAuthority(t, "barn")
	key := aJoiner(t)
	_, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", shed.allow(t, key))
	require.Equal(t, 0, exit)
	barnCode := barn.allow(t, key)

	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", barn.url, "--code", barnCode)
	require.NotEqual(t, 0, exit, out)
	require.Contains(t, out, "--replace-root")
	require.NotContains(t, out, "authority local", "a member is never told to make itself an authority")
	require.Equal(t, shed.fingerprint(), heldRootFP(t))

	out, exit = edgeRun(t, "edge", "enroll", "bench", "--authority", barn.url, "--replace-root")
	require.Equal(t, 2, exit, out)
	require.Contains(t, out, "needs the code from its authority")
	require.Equal(t, shed.fingerprint(), heldRootFP(t))

	out, exit = edgeRun(t, "edge", "enroll", "bench", "--authority", barn.url, "--code", "ZZZZZ-ZZZZZ", "--replace-root", "--yes")
	require.Equal(t, 1, exit, out)
	require.Equal(t, shed.fingerprint(), heldRootFP(t), "a wrong code keeps the old membership")

	out, exit = edgeRun(t, "edge", "enroll", "bench", "--authority", barn.url, "--code", barnCode, "--replace-root")
	require.Equal(t, 2, exit, out)
	require.Contains(t, out, "must be confirmed")
	require.Equal(t, shed.fingerprint(), heldRootFP(t))

	barnCode = barn.allow(t, key) // that refusal held the answer; a fresh code for the real move
	out, exit = edgeRun(t, "edge", "enroll", "bench", "--authority", barn.url, "--code", barnCode, "--replace-root", "--yes")
	require.Equal(t, 0, exit, out)
	require.Contains(t, out, edgeauth.ShortFingerprint(shed.fingerprint()))
	require.Contains(t, out, edgeauth.ShortFingerprint(barn.fingerprint()))
	require.Equal(t, barn.fingerprint(), heldRootFP(t))
}

func TestAnInteractiveRootChangeAsksAndANoChangesNothing(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	barn := newRootedAuthority(t, "barn")
	key := aJoiner(t)
	_, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", shed.allow(t, key))
	require.Equal(t, 0, exit)
	edgeIsInteractive = func() bool { return true }
	edgeStdin = strings.NewReader("n\n")
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", barn.url, "--code", barn.allow(t, key), "--replace-root")
	require.Equal(t, 0, exit, out)
	require.Contains(t, out, "[y/N]")
	require.Contains(t, out, "left alone")
	require.Equal(t, shed.fingerprint(), heldRootFP(t))
}

func TestReplaceRootAgainstTheSameRootReplacesNothing(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	key := aJoiner(t)
	_, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--code", shed.allow(t, key))
	require.Equal(t, 0, exit)
	before, _, _, _ := edgeIdentityStore().LoadIdentity()
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url, "--replace-root")
	require.Equal(t, 0, exit, out)
	require.Contains(t, out, "root is unchanged")
	after, _, _, _ := edgeIdentityStore().LoadIdentity()
	require.Equal(t, before.NodeID, after.NodeID)
}

func TestAllowPrintsTheCodeTheCommandTheInviteAndTheWarning(t *testing.T) {
	offline(t)
	_, exit := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, exit)
	out, exit := edgeRun(t, "edge", "authority", "allow", strings.Repeat("ab", 32))
	require.Equal(t, 0, exit, out)
	m := regexp.MustCompile(`[0-9A-Z]{5}-[0-9A-Z]{5}`).FindString(out)
	require.NotEmpty(t, m, "allow prints a code shaped XXXXX-XXXXX:\n%s", out)
	require.Contains(t, out, "15 minutes")
	require.Contains(t, out, "one join")
	require.Contains(t, out, "--code "+m)
	require.Contains(t, out, "roger edge enroll <name> --invite rogerai-edge://join?")
	require.Contains(t, out, "before root proofs")
	d := edgeDescriptor()
	require.Contains(t, out, edgeauth.ShortFingerprint(d.Fingerprint))

	// allowing again voids the old code
	out2, _ := edgeRun(t, "edge", "authority", "allow", strings.Repeat("ab", 32))
	m2 := regexp.MustCompile(`[0-9A-Z]{5}-[0-9A-Z]{5}`).FindString(out2)
	require.NotEqual(t, m, m2)
}

func TestAuthorityPrintsTheFullFingerprintInTheFormRootTakes(t *testing.T) {
	offline(t)
	_, exit := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, exit)
	out, exit := edgeRun(t, "edge", "authority")
	require.Equal(t, 0, exit, out)
	fp := edgeDescriptor().Fingerprint
	require.Contains(t, out, "sha256:"+fp)
	require.Contains(t, out, edgeauth.ShortFingerprint(fp))
}

func TestAnInviteCarriesTheAddressThePinAndTheCode(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	code := shed.allow(t, aJoiner(t))
	inv := edgeInvite(strings.TrimPrefix(shed.url, "http://"), shed.fingerprint(), code)
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--invite", inv)
	require.Equal(t, 0, exit, out)
	require.Equal(t, shed.fingerprint(), heldRootFP(t))
}

func TestAnInviteWhosePinAndCodeDisagreeIsRefused(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	other := newRootedAuthority(t, "other")
	code := shed.allow(t, aJoiner(t))
	inv := edgeInvite(strings.TrimPrefix(shed.url, "http://"), other.fingerprint(), code)
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--invite", inv)
	require.Equal(t, 1, exit, out)
	keptNothing(t)
	out, exit = edgeRun(t, "edge", "enroll", "bench", "--invite", inv, "--authority", shed.url)
	require.Equal(t, 2, exit, out, "an invite already names its address")
	_, exit = edgeRun(t, "edge", "enroll", "bench", "--invite", "https://example.com/not-an-invite")
	require.Equal(t, 2, exit)
}

func TestEnrollHelpListsEveryTrustFlagAndNoEscapeHatch(t *testing.T) {
	leaf, ok := edgeLeaf("enroll")
	require.True(t, ok)
	for _, f := range []string{"--code", "--root", "--invite", "--replace-root", "--yes"} {
		require.Contains(t, leaf.args, f)
	}
	for name := range edgeFlags {
		require.NotContains(t, name, "trust", "no flag may accept an unproven root")
		require.NotContains(t, name, "insecure")
		require.NotContains(t, name, "tofu")
	}
}

var _ = io.Discard
