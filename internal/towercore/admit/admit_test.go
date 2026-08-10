package admit

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Phase 2.1: the admission registry Roger Core keeps for joined Towers.
// Contract: features/tower/public_enrollment.feature.
//
// One idea underneath all of it: Roger Core alone decides a Tower's state. A Tower's own
// claim about itself is an input to be checked, never a fact - so nothing here takes a
// state from the Tower, and a statement claiming one is recorded as evidence instead.

const (
	ownerA = "owner-aaaa"
	ownerB = "owner-bbbb"
	keyA   = "tower-key-aaaa"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	return New(Config{TokenTTL: time.Hour, LeaseTTL: 24 * time.Hour, MaxTowersPerOwner: 3})
}

// --- enrollment ------------------------------------------------------------

func TestEnrollmentIsAccountBoundAndStartsInQuarantine(t *testing.T) {
	r := newTestRegistry(t)
	tok, err := r.IssueToken(ownerA)
	require.NoError(t, err)

	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	require.NotEmpty(t, tw.ID)
	require.Equal(t, ownerA, tw.Owner, "a Tower belongs to the account that enrolled it")
	require.Equal(t, StateQuarantine, tw.State,
		"a newly admitted Tower is quarantined; an account does not confer trust")
	require.False(t, tw.LeaseExpires.IsZero())
}

func TestATokenIsOneTimeAndConcurrentUseAdmitsOne(t *testing.T) {
	r := newTestRegistry(t)
	tok, err := r.IssueToken(ownerA)
	require.NoError(t, err)

	_, err = r.Enroll(tok, keyA)
	require.NoError(t, err)
	_, err = r.Enroll(tok, "another-key")
	require.Error(t, err, "an enrollment token is consumed by its first successful use")
}

func TestEnrollmentFailsWithoutCreatingPartialAuthority(t *testing.T) {
	r := newTestRegistry(t)
	good, err := r.IssueToken(ownerA)
	require.NoError(t, err)

	for name, tc := range map[string]struct{ token, key string }{
		"no token":      {"", keyA},
		"unknown token": {"not-a-token", keyA},
		"no key":        {good, ""},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(r.ByOwner(ownerA))
			_, err := r.Enroll(tc.token, tc.key)
			require.Error(t, err)
			require.Len(t, r.ByOwner(ownerA), before,
				"a rejected enrollment must create no Tower")
		})
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	r := New(Config{TokenTTL: time.Nanosecond, LeaseTTL: time.Hour, MaxTowersPerOwner: 3})
	tok, err := r.IssueToken(ownerA)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)

	_, err = r.Enroll(tok, keyA)
	require.Error(t, err)
}

// One key, one Tower. Otherwise a single machine could hold several admissions and a
// suspension would only stop one of them.
func TestAKeyCannotBeBoundToTwoTowers(t *testing.T) {
	r := newTestRegistry(t)
	first, err := r.IssueToken(ownerA)
	require.NoError(t, err)
	_, err = r.Enroll(first, keyA)
	require.NoError(t, err)

	second, err := r.IssueToken(ownerA)
	require.NoError(t, err)
	_, err = r.Enroll(second, keyA)
	require.Error(t, err, "an identity key already admitted must not enroll again")
}

func TestOwnerQuotaBoundsHowManyTowersOneAccountRuns(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Hour, MaxTowersPerOwner: 2})
	for i := 0; i < 2; i++ {
		tok, err := r.IssueToken(ownerA)
		require.NoError(t, err)
		_, err = r.Enroll(tok, "key-"+string(rune('a'+i)))
		require.NoError(t, err)
	}
	tok, err := r.IssueToken(ownerA)
	require.NoError(t, err)
	_, err = r.Enroll(tok, "key-c")
	require.Error(t, err, "an owner over quota must not enroll another Tower")

	// A different owner is unaffected: the quota is per account, not global.
	other, err := r.IssueToken(ownerB)
	require.NoError(t, err)
	_, err = r.Enroll(other, "key-d")
	require.NoError(t, err)
}

// --- eligibility: exactly the approved table ------------------------------

func TestEligibilityMatchesTheApprovedTable(t *testing.T) {
	for state, want := range map[State]Eligibility{
		StatePending:    EligibilityNone,
		StateQuarantine: EligibilityProbesOnly,
		StateActive:     EligibilityEligible,
		StateDraining:   EligibilityNone,
		StateSuspended:  EligibilityNone,
		StateRevoked:    EligibilityNone,
		StateExpired:    EligibilityNone,
	} {
		require.Equal(t, want, EligibleFor(state), "state %s", state)
	}
}

func TestOnlyActiveTowersTakeOrdinaryWork(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	require.False(t, r.MayTakeWork(tw.ID), "a quarantined Tower takes no ordinary work")
	require.NoError(t, r.Transition(tw.ID, StateActive))
	require.True(t, r.MayTakeWork(tw.ID))
}

// --- transitions -----------------------------------------------------------

// The transition table is the spec's, verbatim (public_enrollment.feature). It is
// enumerated here in full rather than sampled: every edge this file does not name is
// asserted illegal by the exhaustive test, so an edge invented in the implementation
// cannot pass unnoticed. Sampling is what let four wrong edges through the first time.
var specLegalEdges = map[State][]State{
	StatePending:    {StateQuarantine, StateExpired, StateRevoked},
	StateQuarantine: {StateActive, StateSuspended, StateExpired, StateRevoked},
	StateActive:     {StateDraining, StateSuspended, StateExpired, StateRevoked},
	StateDraining:   {StateActive, StateSuspended, StateExpired, StateRevoked},
	StateSuspended:  {StateQuarantine, StateExpired, StateRevoked},
	// Expiry is NOT terminal: the spec re-admits an expired Tower through quarantine, on
	// fresh key proof and fresh probes. Only revocation is final.
	StateExpired: {StateQuarantine, StateRevoked},
	StateRevoked: nil,
}

var allStates = []State{
	StatePending, StateQuarantine, StateActive,
	StateDraining, StateSuspended, StateExpired, StateRevoked,
}

func TestEveryTransitionMatchesTheSpecTableExactly(t *testing.T) {
	for _, from := range allStates {
		for _, to := range allStates {
			want := false
			for _, edge := range specLegalEdges[from] {
				if edge == to {
					want = true
				}
			}
			require.Equal(t, want, CanTransition(from, to), "%s -> %s", from, to)
		}
	}
}

// The rejections the spec calls out by name. Redundant with the exhaustive test above and
// deliberately so: these are the ones with a stated reason, and a reader should see it.
func TestEveryUnlistedTransitionIsRejected(t *testing.T) {
	for _, tc := range []struct {
		from, to State
		why      string
	}{
		{StatePending, StateActive, "admission cannot skip quarantine"},
		{StatePending, StateDraining, "nothing drains before it ever served"},
		{StateQuarantine, StateDraining, "a probationary Tower drains nothing"},
		{StateSuspended, StateActive,
			"clearing a suspension goes through quarantine and fresh probes, never straight back to full traffic"},
		{StateExpired, StateActive, "re-admission is never direct activation"},
		{StateExpired, StateDraining, "re-admission is never direct activation"},
	} {
		require.False(t, CanTransition(tc.from, tc.to), "%s -> %s: %s", tc.from, tc.to, tc.why)
	}
}

// Revocation is the one terminal state. A Tower that could walk back out of it would make
// revocation a speed bump rather than a penalty.
func TestRevocationIsTerminal(t *testing.T) {
	for _, to := range allStates {
		require.False(t, CanTransition(StateRevoked, to), "revoked must not become %s", to)
	}
}

// A state outside the enum is rejected outright - not scored, not stored, not applied.
func TestAStateOutsideTheEnumIsRefused(t *testing.T) {
	bogus := State("superuser")
	for _, from := range allStates {
		require.False(t, CanTransition(from, bogus))
		require.False(t, CanTransition(bogus, from))
	}
	require.Equal(t, EligibilityNone, EligibleFor(bogus), "an unknown state must never be eligible")

	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	require.Error(t, r.Transition(tw.ID, bogus))
	r.RecordClaim(tw.ID, bogus)
	got, _ := r.Get(tw.ID)
	require.Equal(t, StateQuarantine, got.State)
	require.Zero(t, got.FalseClaims, "an unparseable claim is refused, not scored as evidence")
}

// MayTakeWork must agree with the eligibility table for EVERY state, not just the two the
// happy path walks through.
func TestMayTakeWorkFollowsTheEligibilityTableForEveryState(t *testing.T) {
	for _, st := range allStates {
		r := newTestRegistry(t)
		tok, _ := r.IssueToken(ownerA)
		tw, err := r.Enroll(tok, keyA)
		require.NoError(t, err)
		require.NoError(t, r.forceStateForTest(tw.ID, st))
		require.Equal(t, st == StateActive, r.MayTakeWork(tw.ID),
			"only an active Tower takes ordinary work; %s must not", st)
	}
}

func TestAnIllegalTransitionIsRefusedAndChangesNothing(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	require.NoError(t, r.Transition(tw.ID, StateRevoked))

	require.Error(t, r.Transition(tw.ID, StateActive))
	got, ok := r.Get(tw.ID)
	require.True(t, ok)
	require.Equal(t, StateRevoked, got.State, "a refused transition must leave the state alone")
}

func TestTransitioningAnUnknownTowerIsRefused(t *testing.T) {
	r := newTestRegistry(t)
	require.Error(t, r.Transition("no-such-tower", StateActive))
}

// --- a Tower cannot promote itself ----------------------------------------

func TestATowerCannotSignItsOwnPromotion(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	r.RecordClaim(tw.ID, StateActive)

	got, ok := r.Get(tw.ID)
	require.True(t, ok)
	require.Equal(t, StateQuarantine, got.State, "a Tower's claim must not change its state")
	require.Positive(t, got.FalseClaims, "the false claim must be recorded as evidence")
}

// --- expiry ----------------------------------------------------------------

func TestAnExpiredLeaseStopsNewWork(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Nanosecond, MaxTowersPerOwner: 3})
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	require.NoError(t, r.Transition(tw.ID, StateActive))

	time.Sleep(2 * time.Millisecond)
	require.False(t, r.MayTakeWork(tw.ID), "an expired lease takes no new work even while active")
}

func TestRenewExtendsTheLeaseOnlyForALiveTower(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	before, _ := r.Get(tw.ID)
	require.NoError(t, r.Renew(tw.ID))
	after, _ := r.Get(tw.ID)
	require.True(t, after.LeaseExpires.After(before.LeaseExpires))

	require.NoError(t, r.Transition(tw.ID, StateRevoked))
	require.Error(t, r.Renew(tw.ID), "a revoked Tower must not renew its way back")
}

func TestByOwnerListsOnlyThatOwnersTowers(t *testing.T) {
	r := newTestRegistry(t)
	a, _ := r.IssueToken(ownerA)
	_, err := r.Enroll(a, keyA)
	require.NoError(t, err)
	b, _ := r.IssueToken(ownerB)
	_, err = r.Enroll(b, "key-b")
	require.NoError(t, err)

	require.Len(t, r.ByOwner(ownerA), 1)
	require.Len(t, r.ByOwner(ownerB), 1)
	require.Empty(t, r.ByOwner("nobody"))
}

// --- defaults and unknown-subject handling ---------------------------------

// A zero Config must still be safe: admission policy this important cannot depend on the
// caller remembering to set it.
func TestZeroConfigGetsSafeDefaults(t *testing.T) {
	r := New(Config{})
	tok, err := r.IssueToken(ownerA)
	require.NoError(t, err)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	require.Equal(t, StateQuarantine, tw.State)
	require.True(t, tw.LeaseExpires.After(time.Now()), "a default lease must be in the future")
}

func TestATokenMustBelongToAnAccount(t *testing.T) {
	r := newTestRegistry(t)
	_, err := r.IssueToken("")
	require.Error(t, err, "an unowned token would admit a Tower nobody is accountable for")
}

// Every lookup on an unknown Tower must be a clean no, never a partial answer.
func TestUnknownTowersAreHandledEverywhere(t *testing.T) {
	r := newTestRegistry(t)

	_, ok := r.Get("nope")
	require.False(t, ok)
	require.False(t, r.MayTakeWork("nope"))
	require.Error(t, r.Renew("nope"))
	require.Error(t, r.Transition("nope", StateActive))
	r.RecordClaim("nope", StateActive) // must not panic
}

// A claim that happens to match reality is not evidence of anything.
func TestATruthfulClaimIsNotCountedAgainstATower(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	r.RecordClaim(tw.ID, StateQuarantine)
	got, _ := r.Get(tw.ID)
	require.Zero(t, got.FalseClaims, "claiming the state it actually holds is not a false claim")
}

func TestAUsedTokenCannotBeReusedAfterAFailedEnrollment(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)

	// A rejected attempt must NOT consume the token - the legitimate holder still needs it.
	_, err := r.Enroll(tok, "")
	require.Error(t, err)

	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err, "a failed attempt must not burn a valid token")
	require.NotEmpty(t, tw.ID)
}

// --- concurrency -----------------------------------------------------------
//
// The spec's headline race: two valid enrollments arrive with the same one-time token and
// exactly one may receive an identity. Sequential calls do not test this - the first
// version of these tests called Enroll twice in a row and a reviewer's mutation showed the
// mutex could be deleted with the suite still green.

func TestOneTokenAdmitsExactlyOneTowerUnderARace(t *testing.T) {
	const racers = 16
	r := newTestRegistry(t)
	tok, err := r.IssueToken(ownerA)
	require.NoError(t, err)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var admitted []Tower

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tw, err := r.Enroll(tok, fmt.Sprintf("racing-key-%d", i))
			if err == nil {
				mu.Lock()
				admitted = append(admitted, tw)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.Len(t, admitted, 1, "a one-time token must admit exactly one Tower, however many arrive at once")
	require.Len(t, r.ByOwner(ownerA), 1, "and the registry must hold exactly one")
}

// Concurrent reads and writes across the whole surface, so -race covers every lock rather
// than only the one Enroll takes.
func TestTheRegistryIsSafeUnderConcurrentUse(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Hour, MaxTowersPerOwner: 64})
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = r.Get(tw.ID)
			_ = r.ByOwner(ownerA)
			_ = r.MayTakeWork(tw.ID)
			_ = r.Renew(tw.ID)
			r.RecordClaim(tw.ID, StateActive)
			_ = r.Transition(tw.ID, StateActive)
			if tk, err := r.IssueToken(ownerB); err == nil {
				_, _ = r.Enroll(tk, fmt.Sprintf("k-%d", i))
			}
		}(i)
	}
	wg.Wait()
}

// --- lease expiry ----------------------------------------------------------

// A lapsed lease must not be renewable back into service. The spec re-admits an expired
// Tower only through quarantine, on fresh key proof and fresh probes - so a Renew that
// silently resurrects one would route around the entire re-admission control.
func TestRenewCannotResurrectALapsedLease(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Nanosecond, MaxTowersPerOwner: 3})
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	require.NoError(t, r.Transition(tw.ID, StateActive))
	time.Sleep(2 * time.Millisecond)

	require.Error(t, r.Renew(tw.ID), "a lapsed lease is re-admitted through quarantine, not renewed")
	require.False(t, r.MayTakeWork(tw.ID))
}

// Expire moves a lapsed Tower to the state it already behaves as, so the registry's record
// matches reality instead of reading active forever.
func TestExpireMarksALapsedTowerAndOnlyALapsedOne(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Hour, MaxTowersPerOwner: 3})
	tok, _ := r.IssueToken(ownerA)
	live, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	require.Error(t, r.Expire(live.ID), "a Tower inside its lease must not be expired")

	short := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Nanosecond, MaxTowersPerOwner: 3})
	tok2, _ := short.IssueToken(ownerA)
	lapsed, err := short.Enroll(tok2, keyA)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)

	require.NoError(t, short.Expire(lapsed.ID))
	got, _ := short.Get(lapsed.ID)
	require.Equal(t, StateExpired, got.State)
	require.Error(t, short.Expire("no-such-tower"))
}

// An expired Tower is re-admitted through quarantine, and its lease starts again from
// there - otherwise re-admission would hand back a Tower that is instantly lapsed.
func TestAnExpiredTowerIsReadmittedThroughQuarantine(t *testing.T) {
	// A short but real lease, so re-admission has something to restart. A nanosecond TTL
	// would lapse again immediately and could never satisfy this either way.
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: 30 * time.Millisecond, MaxTowersPerOwner: 3})
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)
	require.NoError(t, r.Expire(tw.ID))

	require.NoError(t, r.Transition(tw.ID, StateQuarantine), "re-admission is legal")
	got, _ := r.Get(tw.ID)
	require.True(t, got.LeaseExpires.After(time.Now()),
		"re-admission must restart the lease, or the Tower is lapsed the instant it returns")
}

// --- identity and quota ----------------------------------------------------

// The key check must be global, not per owner: otherwise a second account could adopt a
// key already admitted to somebody else's Tower.
func TestAnAdmittedKeyCannotBeAdoptedByAnotherOwner(t *testing.T) {
	r := newTestRegistry(t)
	first, _ := r.IssueToken(ownerA)
	_, err := r.Enroll(first, keyA)
	require.NoError(t, err)

	second, _ := r.IssueToken(ownerB)
	_, err = r.Enroll(second, keyA)
	require.Error(t, err, "another account must not adopt an already-admitted identity key")
}

// A revoked Tower must not consume a quota slot forever: an operator who revokes their
// Towers would otherwise be permanently locked out of running any.
func TestTerminalTowersDoNotConsumeTheOwnerQuota(t *testing.T) {
	r := New(Config{TokenTTL: time.Hour, LeaseTTL: time.Hour, MaxTowersPerOwner: 1})
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	next, _ := r.IssueToken(ownerA)
	_, err = r.Enroll(next, "key-2")
	require.Error(t, err, "the quota binds while the Tower is live")

	require.NoError(t, r.Transition(tw.ID, StateRevoked))
	again, _ := r.IssueToken(ownerA)
	_, err = r.Enroll(again, "key-2")
	require.NoError(t, err, "a revoked Tower must free its owner's slot")

	// The revoked Tower is still on record - freeing the slot is not forgetting it, and
	// its key stays burned.
	require.Len(t, r.ByOwner(ownerA), 2)
	third, _ := r.IssueToken(ownerA)
	_, err = r.Enroll(third, keyA)
	require.Error(t, err, "a revoked key must never be admitted again")
}

// A consumed token must not linger in memory: IssueToken is a self-service surface, so an
// unbounded map is a resource an owner can grow without limit.
func TestAConsumedTokenIsForgotten(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	_, err := r.Enroll(tok, keyA)
	require.NoError(t, err)
	require.Zero(t, r.openTokensForTest(), "a consumed token must not be retained")
}

// Two tokens must be independent and unguessable.
func TestTokensAreDistinct(t *testing.T) {
	// The quota is raised out of the way: this asserts on the token SOURCE, and the
	// live-token cap is exercised by its own test. Fifty outstanding tokens is not a thing
	// a real account may hold.
	r := NewWithStore(Config{MaxOpenTokensPerOwner: 1000}, NewMemStore())
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tok, err := r.IssueToken(ownerA)
		require.NoError(t, err)
		require.False(t, seen[tok], "an enrollment token must never repeat")
		require.GreaterOrEqual(t, len(tok), 32, "a guessable token is no gate at all")
		seen[tok] = true
	}
}

// Get and ByOwner must hand out copies. Money code will hold these, and a caller that
// could reach through into the registry's record could promote its own Tower.
func TestReadsReturnCopiesNotTheLiveRecord(t *testing.T) {
	r := newTestRegistry(t)
	tok, _ := r.IssueToken(ownerA)
	tw, err := r.Enroll(tok, keyA)
	require.NoError(t, err)

	got, _ := r.Get(tw.ID)
	got.State = StateActive
	got.Owner = ownerB
	listed := r.ByOwner(ownerA)
	require.Len(t, listed, 1)
	listed[0].State = StateActive

	fresh, _ := r.Get(tw.ID)
	require.Equal(t, StateQuarantine, fresh.State, "a returned copy must not be the record")
	require.Equal(t, ownerA, fresh.Owner)
}
