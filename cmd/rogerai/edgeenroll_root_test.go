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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
)

// rootedAuthority is a real local authority answering over real HTTP through the production
// enrollhttp handler. strip removes the root proof from every answer, which is what an authority
// from before proofs (or a device in between that strips it) sends; mangle lets an impostor doctor
// a real answer. Every request and answer body is kept, for "what crossed the wire" checks.
type rootedAuthority struct {
	mu      sync.Mutex
	local   *edgeauth.Local
	dir     string
	url     string
	hits    atomic.Int32
	enrolls atomic.Int32 // requests to the enrollment or claim path: "the authority was asked"
	strip   atomic.Bool
	mangle  func(resp *edgeauth.Response, body []byte)
	wire    []string
	paths   []string // every path asked, in order
}

func newRootedAuthority(t *testing.T, where string) *rootedAuthority {
	t.Helper()
	return newRootedAuthorityAt(t, t.TempDir(), where)
}

// newRootedAuthorityAt designates an authority in dir (a machine's own Edge directory, when the
// scenario also drives that machine's CLI) and serves it.
func newRootedAuthorityAt(t *testing.T, dir, where string) *rootedAuthority {
	t.Helper()
	local, err := edgeauth.Designate(dir, where)
	require.NoError(t, err)
	a := &rootedAuthority{local: local, dir: dir}
	srv := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(srv.Close)
	a.url = srv.URL
	return a
}

func (a *rootedAuthority) serve(w http.ResponseWriter, r *http.Request) {
	a.hits.Add(1)
	if r.URL.Path == enrollhttp.Path || r.URL.Path == enrollhttp.ClaimPath {
		a.enrolls.Add(1)
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	a.mu.Lock()
	h, mangle := enrollhttp.Handler(a.local), a.mangle
	a.wire = append(a.wire, string(body))
	a.paths = append(a.paths, r.URL.Path)
	a.mu.Unlock()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	out := rec.Body.Bytes()
	answer := r.URL.Path == enrollhttp.Path || r.URL.Path == enrollhttp.ClaimPath
	if rec.Code == http.StatusOK && answer && (a.strip.Load() || mangle != nil) {
		var resp edgeauth.Response
		if json.Unmarshal(out, &resp) == nil {
			if mangle != nil {
				mangle(&resp, body)
			}
			var m map[string]any
			b, _ := json.Marshal(resp)
			_ = json.Unmarshal(b, &m)
			if a.strip.Load() {
				delete(m, "root_proof")
				delete(m, "root_proof_status")
			}
			out, _ = json.Marshal(m)
		}
	}
	a.mu.Lock()
	a.wire = append(a.wire, string(out))
	a.mu.Unlock()
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(out)
}

// restart reopens the authority from its directory, as a restarted process would.
func (a *rootedAuthority) restart(t *testing.T) {
	t.Helper()
	l, ok, err := edgeauth.OpenLocal(a.dir)
	require.NoError(t, err)
	require.True(t, ok)
	a.mu.Lock()
	a.local = l
	a.mu.Unlock()
}

func (a *rootedAuthority) setMangle(fn func(resp *edgeauth.Response, body []byte)) {
	a.mu.Lock()
	a.mangle = fn
	a.mu.Unlock()
}

func (a *rootedAuthority) wireLog() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.wire, "\n")
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
	start, late := time.Now(), false
	edgeNow = func() time.Time {
		if late {
			return start.Add(16 * time.Minute) // the owner types much later
		}
		return start // the answer arrives
	}
	edgeStdin = &flipReader{r: strings.NewReader(code + "\n"), flip: func() { late = true }}
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
	require.Equal(t, 2, exit, out, "refused before anything is sent")
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
	require.Contains(t, out, "roger join roger://join?kind=edge")  // features/join/invite.feature: one scheme, one verb
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

// flipReader runs flip the first time it is read: "the owner started typing", at a moment the
// test chooses.
type flipReader struct {
	r    io.Reader
	flip func()
	done bool
}

func (f *flipReader) Read(p []byte) (int, error) {
	if !f.done {
		f.done = true
		f.flip()
	}
	return f.r.Read(p)
}

// An empty answer, or the end of input, at the code prompt ends the join with nothing kept. It is
// never read as "ask again", which on a closed input would ask for ever.
func TestNoCodeTypedEndsTheJoinAndKeepsNothing(t *testing.T) {
	shed := newRootedAuthority(t, "shed")
	shed.allow(t, aJoiner(t))
	edgeIsInteractive = func() bool { return true }
	edgeStdin = strings.NewReader("")
	type result struct {
		out  string
		exit int
	}
	done := make(chan result, 1)
	go func() {
		out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", shed.url)
		done <- result{out, exit}
	}()
	select {
	case r := <-done:
		require.Equal(t, 1, r.exit, r.out)
		require.Contains(t, r.out, "no code was entered")
		keptNothing(t)
	case <-time.After(10 * time.Second):
		t.Fatal("the join kept asking for a code on a closed input")
	}
}

// "an impostor answering first is reported once, plainly": the line naming the address and the
// root it offered is said on the first refusal, and later retries of the same join say only that
// the code did not match.
func TestAnImpostorIsNamedOnceNotOnEveryRetry(t *testing.T) {
	impostor := newRootedAuthority(t, "shed")
	impostor.allow(t, aJoiner(t))
	edgeIsInteractive = func() bool { return true }
	edgeStdin = strings.NewReader("ZZZZZ-ZZZZZ\nYYYYY-YYYYY\nXXXXX-XXXXX\n\n")
	out, exit := edgeRun(t, "edge", "enroll", "bench", "--authority", impostor.url)
	require.Equal(t, 1, exit, out)
	addr := strings.TrimPrefix(impostor.url, "http://")
	require.Equal(t, 1, strings.Count(out, addr), "the address is named once:\n%s", out)
	require.Equal(t, 1, strings.Count(out, "is not the Edge that showed you this code"))
	require.Equal(t, 3, strings.Count(out, "does not match the Edge that answered"))
	keptNothing(t)
}
