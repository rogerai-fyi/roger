package deviceauth

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Contract: features/auth/broker_mediated_login.feature.
//
// The property everything else rests on: the device code is bound to the requesting key
// AT ISSUE. Approval can only ever bind that key, and no later step accepts a key as
// input - so there is no point in the flow where a different key can be substituted.

const keyA = "pubkey-aaaa"
const keyB = "pubkey-bbbb"

func newTestFlow(t *testing.T) *Flow {
	t.Helper()
	return New(Config{TTL: 10 * time.Minute, Interval: 5 * time.Second, MaxWrongCodes: 5})
}

func TestStartIssuesACodePairBoundToTheRequestingKey(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)

	require.NotEmpty(t, p.DeviceCode)
	require.NotEmpty(t, p.UserCode)
	require.Equal(t, 5, p.IntervalSeconds)
	require.Positive(t, p.ExpiresInSeconds)
	// The CLI must never be handed a provider endpoint or client id.
	require.NotContains(t, strings.ToLower(p.VerificationURI), "github")
	require.NotContains(t, strings.ToLower(p.VerificationURI), "apple")
}

func TestUserCodeIsTypableAndUnambiguous(t *testing.T) {
	f := newTestFlow(t)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p, err := f.Start(keyA)
		require.NoError(t, err)
		require.False(t, seen[p.UserCode], "user codes must not repeat")
		seen[p.UserCode] = true

		require.LessOrEqual(t, len(p.UserCode), 10, "a user code must be typable from a phone screen")
		for _, r := range p.UserCode {
			require.Contains(t, userCodeAlphabet, string(r),
				"user codes must avoid characters people confuse when reading aloud")
		}
	}
}

func TestDeviceCodesAreUniqueUnderConcurrency(t *testing.T) {
	f := newTestFlow(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := f.Start(keyA)
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			require.False(t, seen[p.DeviceCode])
			seen[p.DeviceCode] = true
		}()
	}
	wg.Wait()
	require.Len(t, seen, 50)
}

// --- polling ---------------------------------------------------------------

func TestPollBeforeApprovalIsPending(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)

	f.advance(6 * time.Second)
	res, err := f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)
	require.Equal(t, StatusPending, res.Status)
	require.Empty(t, res.Account)
}

// The whole point of binding at issue: another key cannot poll this login, so an attacker
// who somehow learns a device code still cannot redeem it.
func TestPollByADifferentKeyIsRefusedAndDoesNotConsume(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Approve(p.UserCode, "alice"))

	f.advance(6 * time.Second)
	_, err = f.Poll(p.DeviceCode, keyB)
	require.Error(t, err, "only the key the code was issued to may poll it")

	// The legitimate holder is unaffected.
	res, err := f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)
	require.Equal(t, StatusApproved, res.Status)
	require.Equal(t, "alice", res.Account)
}

func TestPollingFasterThanTheIntervalSlowsDownWithoutFailing(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)

	f.advance(6 * time.Second)
	_, err = f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)

	res, err := f.Poll(p.DeviceCode, keyA) // immediately again
	require.NoError(t, err)
	require.Equal(t, StatusSlowDown, res.Status)
	require.Greater(t, res.IntervalSeconds, 5, "the interval must rise")

	// Still usable afterwards.
	f.advance(30 * time.Second)
	res, err = f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)
	require.Equal(t, StatusPending, res.Status)
}

func TestUnknownDeviceCodeIsRefused(t *testing.T) {
	f := newTestFlow(t)
	_, err := f.Poll("not-a-code", keyA)
	require.Error(t, err)
}

func TestExpiredLoginIsRefusedOnBothSides(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)

	f.advance(11 * time.Minute)
	res, err := f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, res.Status)

	require.Error(t, f.Approve(p.UserCode, "alice"), "an expired login cannot be approved")
}

// --- approval and denial ---------------------------------------------------

func TestApprovalBindsTheAccountToTheIssuingKey(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Approve(p.UserCode, "alice"))

	f.advance(6 * time.Second)
	res, err := f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)
	require.Equal(t, StatusApproved, res.Status)
	require.Equal(t, "alice", res.Account)
	require.Equal(t, keyA, res.BoundKey, "approval binds the key recorded at issue, never one supplied later")
}

func TestApprovalIsOneTimeAndTheCodeIsConsumed(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Approve(p.UserCode, "alice"))

	f.advance(6 * time.Second)
	_, err = f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)

	f.advance(6 * time.Second)
	_, err = f.Poll(p.DeviceCode, keyA)
	require.Error(t, err, "a consumed device code must not be reusable")
}

func TestDeniedLoginReportsDeniedAndCanNeverBeApproved(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Deny(p.UserCode, "alice"))

	f.advance(6 * time.Second)
	res, err := f.Poll(p.DeviceCode, keyA)
	require.NoError(t, err)
	require.Equal(t, StatusDenied, res.Status)

	require.Error(t, f.Approve(p.UserCode, "alice"), "a denied login can never be approved afterwards")
}

func TestApprovingTwiceIsRefused(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Approve(p.UserCode, "alice"))
	require.Error(t, f.Approve(p.UserCode, "mallory"), "a pending login is approved once")
}

// --- guessing --------------------------------------------------------------

func TestWrongUserCodesLockOutTheSubmitterWhoGuessed(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		require.Error(t, f.Approve("WRONGCODE", "mallory"))
	}
	// Locked out: even the CORRECT code is now refused from THAT submitter.
	require.Error(t, f.Approve(p.UserCode, "mallory"), "wrong-code attempts must lock that submitter")
}

// The budget must be per submitter. A single global counter would turn an anti-guessing
// control into a denial of service: one attacker burns it and nobody can sign in.
func TestOneAttackerCannotLockOutEveryoneElse(t *testing.T) {
	f := newTestFlow(t)
	victim, err := f.Start(keyA)
	require.NoError(t, err)

	for i := 0; i < 20; i++ {
		require.Error(t, f.Approve("GUESSING", "mallory"))
	}
	require.NoError(t, f.Approve(victim.UserCode, "alice"),
		"one attacker exhausting their own budget must not block anyone else's sign-in")
}

// A rejection must not reveal whether the submitted code exists.
func TestApprovalFailuresAreIndistinguishable(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Deny(p.UserCode, "alice"))

	unknown := f.Approve("NOSUCHCODE", "bob")
	denied := f.Approve(p.UserCode, "bob")
	require.Error(t, unknown)
	require.Error(t, denied)
	require.Equal(t, unknown.Error(), denied.Error(),
		"a rejection must not tell an attacker whether a code exists")
}

// --- what the approval screen must be able to show ------------------------

func TestPendingLoginExposesWhatTheApproverNeedsToJudge(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)

	info, ok := f.Describe(p.UserCode, "alice")
	require.True(t, ok)
	require.Equal(t, p.UserCode, info.UserCode)
	require.False(t, info.RequestedAt.IsZero(), "the approver must see when the request was made")
	// The approver must never be shown anything that would let them impersonate the CLI.
	require.Empty(t, info.DeviceCode, "the device code is the CLI's secret, not the approver's")
}

func TestDescribeAnUnknownCodeRevealsNothing(t *testing.T) {
	f := newTestFlow(t)
	_, ok := f.Describe("NOSUCHCODE", "alice")
	require.False(t, ok)
}

// --- config floors and remaining paths ------------------------------------

// A zero Config must still be safe: policy this important cannot depend on the caller
// remembering to set it.
func TestZeroConfigGetsSafeDefaults(t *testing.T) {
	f := New(Config{})
	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.Positive(t, p.IntervalSeconds)
	require.Positive(t, p.ExpiresInSeconds)
	require.Contains(t, p.VerificationURI, "rogerai")

	// The guessing budget defaults to something finite, not unlimited.
	for i := 0; i < 200; i++ {
		_ = f.Approve("WRONGCODE", "alice")
	}
	require.Error(t, f.Approve(p.UserCode, "alice"), "the default guess budget must be finite")
}

func TestStartRequiresARequestingKey(t *testing.T) {
	f := newTestFlow(t)
	_, err := f.Start("")
	require.Error(t, err, "an unsigned start would issue a code bound to nobody")
}

func TestDenyRejectsUnknownConsumedAndNonPendingLogins(t *testing.T) {
	f := newTestFlow(t)
	require.Error(t, f.Deny("NOSUCHCODE", "alice"))

	p, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Approve(p.UserCode, "alice"))
	require.Error(t, f.Deny(p.UserCode, "alice"), "an approved login cannot then be denied")
}

func TestDenyOnAnExpiredLoginIsRefused(t *testing.T) {
	f := newTestFlow(t)
	p, err := f.Start(keyA)
	require.NoError(t, err)
	f.advance(11 * time.Minute)
	require.Error(t, f.Deny(p.UserCode, "alice"))
}

func TestDescribeHidesConsumedApprovedAndExpiredLogins(t *testing.T) {
	f := newTestFlow(t)

	approved, err := f.Start(keyA)
	require.NoError(t, err)
	require.NoError(t, f.Approve(approved.UserCode, "alice"))
	_, ok := f.Describe(approved.UserCode, "alice")
	require.False(t, ok, "an already-approved login is not awaiting a decision")

	expired, err := f.Start(keyA)
	require.NoError(t, err)
	f.advance(11 * time.Minute)
	_, ok = f.Describe(expired.UserCode, "alice")
	require.False(t, ok)
}

func TestRandomHelpersProduceDistinctValues(t *testing.T) {
	a, err := randomToken(32)
	require.NoError(t, err)
	b, err := randomToken(32)
	require.NoError(t, err)
	require.NotEqual(t, a, b)
	require.Len(t, a, 64)

	c, err := randomUserCode()
	require.NoError(t, err)
	require.Len(t, c, userCodeLen)
}
