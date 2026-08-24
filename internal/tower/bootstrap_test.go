package tower

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Phase 1.3: the standalone bootstrap flow — how the FIRST local client becomes the
// local operator. Contract: features/tower/modes.feature.
//
// This is the one moment a standalone network hands out authority, so the properties
// are strict: the code is high-entropy and shown once, only an HMAC verifier is stored,
// consumption is one-time and atomic, a wrong binding never burns the legitimate
// invitation, and guessing is durably budgeted rather than free.

func newBootstrapStore(t *testing.T) (*State, string) {
	t.Helper()
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)
	return st, dir
}

const testClientKey = "client-pubkey-hash-aaaa"

func TestInvitationCodeHasCryptographicEntropy(t *testing.T) {
	st, _ := newBootstrapStore(t)

	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
		require.NoError(t, err)
		require.NotEmpty(t, inv.ID)
		// At least 128 bits, base32-ish encoded: 26+ characters of alphabet.
		require.GreaterOrEqual(t, len(code), 26, "a bootstrap code must carry at least 128 bits")
		require.False(t, seen[code], "codes must not repeat")
		seen[code] = true
	}
}

// The durable record must never contain the plaintext code. If it did, reading the data
// directory would be equivalent to holding the invitation.
func TestPlaintextCodeIsNeverPersisted(t *testing.T) {
	st, dir := newBootstrapStore(t)
	_, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)

	err = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		require.NotContains(t, string(b), code, "%s must not contain the plaintext bootstrap code", p)
		return nil
	})
	require.NoError(t, err)
}

func TestInvitationIsShownOnceAndNeverReturnedAgain(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	require.NotEmpty(t, code)

	got, err := st.Invitation(inv.ID)
	require.NoError(t, err)
	require.NotContains(t, got.String(), code, "an invitation record must never re-expose its code")
}

// --- consumption ----------------------------------------------------------

func TestFirstValidUseIssuesTheSingletonOperator(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)

	cred, err := st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)
	require.Equal(t, RoleLocalOperator, cred.Role)
	require.Equal(t, testClientKey, cred.ClientKeyHash)
	require.Equal(t, st.LocalNetworkID, cred.NetworkID)
	require.NotEmpty(t, cred.RootFingerprint, "an admitted client pins the offline root")

	op, err := st.LocalOperator()
	require.NoError(t, err)
	require.Equal(t, cred.ClientKeyHash, op.ClientKeyHash)
}

func TestExactReplayAfterSuccessIsRejected(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)

	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)

	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.Error(t, err, "a bootstrap code is one-time")
}

func TestUseAfterExpiryIsRejected(t *testing.T) {
	st, _ := newBootstrapStore(t)
	// A positive-but-tiny window: minting an already-expired invitation is now itself
	// rejected, so expiry has to be reached rather than declared.
	inv, code, err := st.CreateInvitation(testClientKey, time.Nanosecond, 5)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)

	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.Error(t, err)
}

// Only one client can win an invitation, even if two race for it.
func TestConcurrentUseAdmitsExactlyOne(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 10)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := st.ConsumeInvitation(inv.ID, code, testClientKey); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, 1, wins, "exactly one concurrent client may consume an invitation")
}

// --- binding mismatch never burns the legitimate invitation ---------------

func TestBindingMismatchDoesNotConsumeTheInvitation(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 10)
	require.NoError(t, err)

	// Correct secret, wrong client key: must fail AND leave the invitation usable.
	_, err = st.ConsumeInvitation(inv.ID, code, "some-other-client-key")
	require.Error(t, err)

	cred, err := st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err, "the legitimate holder must still be able to consume it")
	require.Equal(t, testClientKey, cred.ClientKeyHash)
}

func TestWrongInvitationIDIsRejected(t *testing.T) {
	st, _ := newBootstrapStore(t)
	_, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)

	_, err = st.ConsumeInvitation("not-an-invitation", code, testClientKey)
	require.Error(t, err)
}

// A failed attempt must not tell an attacker WHICH part was wrong.
func TestFailureRepliesAreUniform(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 10)
	require.NoError(t, err)

	msgs := map[string]bool{}
	for _, tc := range []struct{ id, code, key string }{
		{"unknown-id", code, testClientKey},
		{inv.ID, "WRONGCODEWRONGCODEWRONGCODE", testClientKey},
		{inv.ID, code, "wrong-client-key"},
		{inv.ID, "", testClientKey},
		{inv.ID, strings.Repeat("A", 5000), testClientKey},
	} {
		_, err := st.ConsumeInvitation(tc.id, tc.code, tc.key)
		require.Error(t, err)
		msgs[err.Error()] = true
	}
	require.Len(t, msgs, 1, "every rejection must read identically: %v", msgs)
}

// --- attempt budget --------------------------------------------------------

func TestAttemptBudgetLocksOutAfterTheSignedMaximum(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 3)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := st.ConsumeInvitation(inv.ID, "WRONGCODEWRONGCODEWRONGCODE", testClientKey)
		require.Error(t, err)
	}
	// Budget exhausted: even the CORRECT code is now refused.
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.Error(t, err, "an exhausted attempt budget locks the invitation, correct code or not")
}

// The budget must survive a restart, or an attacker just restarts the process.
func TestAttemptBudgetIsDurableAcrossProcesses(t *testing.T) {
	st, dir := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 3)
	require.NoError(t, err)

	_, err = st.ConsumeInvitation(inv.ID, "WRONGCODEWRONGCODEWRONGCODE", testClientKey)
	require.Error(t, err)
	_, err = st.ConsumeInvitation(inv.ID, "WRONGCODEWRONGCODEWRONGCODE", testClientKey)
	require.Error(t, err)

	// A "new process" reopens the same directory.
	reopened, err := Open(dir)
	require.NoError(t, err)
	_, err = reopened.ConsumeInvitation(inv.ID, "WRONGCODEWRONGCODEWRONGCODE", testClientKey)
	require.Error(t, err)

	_, err = reopened.ConsumeInvitation(inv.ID, code, testClientKey)
	require.Error(t, err, "the attempt budget must not reset on restart")
}

func TestAttemptsAgainstAnUnknownInvitationAreAlsoBudgeted(t *testing.T) {
	st, _ := newBootstrapStore(t)
	for i := 0; i < 50; i++ {
		_, err := st.ConsumeInvitation("unknown-id", "WRONGCODEWRONGCODEWRONGCODE", testClientKey)
		require.Error(t, err)
	}
	// The global limiter must now refuse even a legitimate new invitation attempt from
	// this Tower, rather than letting an attacker probe IDs for free.
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.Error(t, err, "the global anonymous-attempt limiter must apply")
}

// --- operator is the first admitted; more clients may follow -----------------

// The approved standalone spec (features/tower/standalone_consumer_plane.feature) makes a
// private network multi-client: a second invitation for a DIFFERENT client admits that
// client too. The OPERATOR role stays the first admitted client, so there is still exactly
// one admin - what changed is that additional clients are now admissible beside it.
func TestSecondInvitationAdmitsAnAdditionalClientNotASecondOperator(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)

	inv2, code2, err := st.CreateInvitation("second-client-key", time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv2.ID, code2, "second-client-key")
	require.NoError(t, err, "a second client IS admitted now (multi-client plane)")

	require.True(t, st.IsAdmitted(testClientKey))
	require.True(t, st.IsAdmitted("second-client-key"))

	// The operator stays the FIRST admitted client - still exactly one admin.
	op, err := st.LocalOperator()
	require.NoError(t, err)
	require.Equal(t, testClientKey, op.ClientKeyHash)
}

func TestNoOperatorBeforeBootstrap(t *testing.T) {
	st, _ := newBootstrapStore(t)
	_, err := st.LocalOperator()
	require.Error(t, err)
}

// A joined Tower has no local bootstrap: its clients are admitted by Roger Core.
func TestJoinedModeHasNoLocalBootstrap(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeJoined)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)

	_, _, err = st.CreateInvitation(testClientKey, time.Hour, 5)
	require.Error(t, err, "a joined Tower must not mint local admission credentials")
}

// --- display, failure paths, and boundaries -------------------------------

func TestInvitationStringReportsEveryState(t *testing.T) {
	now := time.Now()
	for want, inv := range map[string]Invitation{
		"open":     {ID: "a", Budget: 5, ExpiresAt: now.Add(time.Hour).UnixNano()},
		"consumed": {ID: "b", Budget: 5, Consumed: true, ExpiresAt: now.Add(time.Hour).UnixNano()},
		"locked":   {ID: "c", Budget: 2, Attempts: 2, ExpiresAt: now.Add(time.Hour).UnixNano()},
		"expired":  {ID: "d", Budget: 5, ExpiresAt: now.Add(-time.Hour).UnixNano()},
	} {
		require.Contains(t, inv.String(), "state="+want)
	}
}

func TestCreateInvitationRequiresAClientBinding(t *testing.T) {
	st, _ := newBootstrapStore(t)
	_, _, err := st.CreateInvitation("", time.Hour, 5)
	require.Error(t, err, "an unbound invitation could be consumed by anyone who learns the code")
}

func TestInvitationLookupOfAnUnknownIDIsRejected(t *testing.T) {
	st, _ := newBootstrapStore(t)
	_, err := st.Invitation("nope")
	require.Error(t, err)
}

func TestCorruptBootstrapStateIsNotSilentlyAccepted(t *testing.T) {
	st, dir := newBootstrapStore(t)
	_, _, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, bootstrapFile), []byte("{broken"), keyPerm))
	_, err = st.Invitation("anything")
	require.Error(t, err)
	_, err = st.LocalOperator()
	require.Error(t, err)
	_, _, err = st.CreateInvitation(testClientKey, time.Hour, 5)
	require.Error(t, err, "a Tower must not mint credentials from unreadable admission state")
}

// A consume against corrupt state must fail closed - and uniformly, like every other
// rejection, so it is not an oracle either.
func TestConsumeAgainstCorruptStateFailsClosed(t *testing.T) {
	st, dir := newBootstrapStore(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, bootstrapFile), []byte("{broken"), keyPerm))
	_, err := st.ConsumeInvitation("id", "code", testClientKey)
	require.Error(t, err)
	require.Equal(t, errBootstrapRejected.Error(), err.Error())
}

func TestRootFingerprintIsEmptyWithoutAnOfflineRoot(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeJoined) // joined mints no offline root
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)
	_, err = st.rootFingerprint()
	require.Error(t, err, "a Tower with no offline root must refuse to pin an empty fingerprint")
}

func TestSaveBootstrapFailsOnAReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	st, dir := newBootstrapStore(t)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, dirPerm) })

	_, _, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.Error(t, err, "an invitation that cannot be persisted must not be handed out")
}

func TestJoinedModeCannotConsumeAnInvitationEither(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeJoined)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)

	_, err = st.ConsumeInvitation("id", "code", testClientKey)
	require.ErrorIs(t, err, ErrNotStandalone)
}

func TestCreateInvitationRejectsABudgetOrWindowThatCannotWork(t *testing.T) {
	st, _ := newBootstrapStore(t)
	_, _, err := st.CreateInvitation(testClientKey, time.Hour, 0)
	require.Error(t, err, "a zero budget mints an invitation that is born locked")
	_, _, err = st.CreateInvitation(testClientKey, 0, 5)
	require.Error(t, err, "a zero window mints an invitation that is born expired")
}

// The probe budget must DECAY. A permanent counter is not a limiter but a brick: enough
// bogus ids and the network could never be bootstrapped at all.
func TestGlobalProbeBudgetDecays(t *testing.T) {
	st, dir := newBootstrapStore(t)
	for i := 0; i < globalAttemptBudget; i++ {
		_, _ = st.ConsumeInvitation("unknown-id", "WRONGCODEWRONGCODEWRONGCODE", testClientKey)
	}
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.Error(t, err, "the budget is exhausted right now")

	// Wind the recorded window back past its expiry, as a later run would see it.
	b, err := os.ReadFile(filepath.Join(dir, bootstrapFile))
	require.NoError(t, err)
	var bs map[string]any
	require.NoError(t, json.Unmarshal(b, &bs))
	bs["global_attempts_since"] = time.Now().Add(-2 * globalAttemptWindow).Unix()
	nb, err := json.Marshal(bs)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, bootstrapFile), nb, keyPerm))

	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err, "once the window has passed the network must be bootstrappable again")
}

// An invitation record handed to a caller must not carry the HMAC verifier.
func TestInvitationRecordWithholdsTheVerifier(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, _, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	got, err := st.Invitation(inv.ID)
	require.NoError(t, err)
	require.Empty(t, got.Verifier, "the verifier is secret and must not be returned")
}
