package attempt

// attempt_test.go is features/tower/attempt_lifecycle.feature, scenario by scenario.
//
// The feature's first line is what every test here is really checking: "Tower, Station,
// client, and transport messages are EVIDENCE for Roger Core; none can write attempt state
// directly or revive a terminal attempt."

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/towerobj"
)

const network = "roger-public"

func newLedger(t *testing.T) (*Ledger, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// ATOMIC, because the ledger calls this from whichever goroutine is committing. A plain
	// `seq++` here is a data race the concurrency test finds immediately - and the same
	// mistake in a production sequencer would hand two attempts one ordering position.
	var seq atomic.Int64
	return New(Config{
		Network: network, Signer: priv,
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Sequence: func() int64 { return seq.Add(1) },
	}, nil), pub
}

func joinedSpec(attemptID string) IssueSpec {
	now := time.Unix(1_700_000_000, 0)
	return IssueSpec{
		Network: network, JobID: "job-1", RequestID: "req-1", AttemptID: attemptID,
		Origin: OriginJoined, GrantHash: "grant-hash", LeaseHash: "lease-hash",
		Hold: Hold{
			ID: "hold-1", Currency: "USD", Unit: "micro", Scale: 6, Amount: 1500,
			State: "reserved",
		},
		ReservationHash: "res-hash", ReservationSet: "res-set-hash",
		TowerRevision: 3, StationRevision: 4,
		Deadline: now.Add(time.Minute), FinalizationCeiling: now.Add(5 * time.Minute),
		LedgerIndex: 42,
	}
}

func obj(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// "Attempt creation commits the complete issued authority atomically."
func TestIssuingCommitsRevisionOneWithNoPrior(t *testing.T) {
	l, pub := newLedger(t)
	c, e, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	require.Equal(t, int64(1), e.Revision)
	require.Equal(t, StateIssued, e.State)
	require.Equal(t, HoldReserved, e.Hold)
	require.NotEmpty(t, e.Hash)

	// Both objects verify under the attempt-state key.
	require.NoError(t, towerobj.Verify(pub, network, TypeEvent, Version, e.Signed, "sig"))
	require.NoError(t, towerobj.Verify(pub, network, TypeCommitment, Version, c.Signed, "sig"))

	// CANONICAL ISSUED ABSENCE: no prior member at all, rather than an empty one. The signed
	// bytes of "there is no prior" and "the prior hash is empty" must differ, or a first
	// event could be replayed as a successor.
	got := obj(t, e.Signed)
	require.NotContains(t, got, "prev_hash")
	require.NotContains(t, got, "evidence_hash", "issuing observes nothing")
	require.NotContains(t, got, "reason", "issued is not terminal")
	require.NotContains(t, got, "release_id")
}

// "AttemptIssueCommitmentV1 proves issuance without disclosing money to a Tower."
//
// THE MOST IMPORTANT TEST IN THIS FILE. A relay that could read the money would learn what
// every request is worth, whose account pays, and how much room is left on it.
func TestTheCommitmentDisclosesNoMoneyAndNoIdentity(t *testing.T) {
	l, _ := newLedger(t)
	s := joinedSpec("att-1")
	c, _, err := l.Issue(s)
	require.NoError(t, err)

	got := obj(t, c.Signed)
	for _, forbidden := range []string{
		"hold_id", "hold_amount", "hold_currency", "hold_unit", "hold_scale", "hold_state",
		"reservation", "reservation_set", "price", "client", "account", "consumer",
		"request_id", "compensation_snapshot",
	} {
		require.NotContains(t, got, forbidden,
			"the commitment reached a Tower carrying %q", forbidden)
	}

	// And not by accident of naming: no VALUE anywhere in it may be the hold amount or the
	// account, however the member happens to be spelled.
	raw := string(c.Signed)
	require.NotContains(t, raw, "1500", "the hold amount reached a Tower")
	require.NotContains(t, raw, "hold-1", "the hold id reached a Tower")
	require.NotContains(t, raw, "res-hash", "the funding source reached a Tower")

	// What it DOES carry is exactly what a Tower needs to know an attempt is real.
	require.Equal(t, "att-1", got["attempt_id"])
	require.Equal(t, "job-1", got["job_id"])
	require.Equal(t, "joined", got["origin"])
	require.Equal(t, "grant-hash", got["grant_hash"])
	require.Equal(t, "lease-hash", got["lease_hash"])
	require.Equal(t, "42", got["ledger_index"])
}

// The private event carries the money the commitment does not - both halves have to be true
// or the split proves nothing.
func TestTheEventCarriesTheMoneyTheCommitmentWithholds(t *testing.T) {
	l, _ := newLedger(t)
	_, e, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	got := obj(t, e.Signed)
	require.Equal(t, "hold-1", got["hold_id"])
	require.Equal(t, "1500", got["hold_amount"])
	require.Equal(t, "USD", got["hold_currency"])
	require.Equal(t, "res-hash", got["reservation"])
	require.Equal(t, "res-set-hash", got["reservation_set"])
	require.Equal(t, "req-1", got["request_id"])
}

// "commitment ID derives from strict JCS [AttemptIssueCommitmentV1-id-v1,network-ID,
// attempt-ID]" and the event id from the same with its revision. Derived, so two instances
// agree without coordinating - and unchoosable, so nobody can pick a chain position.
func TestIdentitiesAreDerivedAndNotChosen(t *testing.T) {
	l, _ := newLedger(t)
	c, e, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	wantCommitment, err := towerobj.HashList([]string{commitmentIDTag, network, "att-1"})
	require.NoError(t, err)
	require.Equal(t, wantCommitment, c.ID)

	wantEvent, err := towerobj.HashList([]string{eventIDTag, network, "att-1", "1"})
	require.NoError(t, err)
	require.Equal(t, wantEvent, e.ID)

	// A different attempt, a different network, or a different revision is a different id.
	other, err := EventID(network, "att-2", 1)
	require.NoError(t, err)
	require.NotEqual(t, e.ID, other)
	other, err = EventID("some-other-network", "att-1", 1)
	require.NoError(t, err)
	require.NotEqual(t, e.ID, other)
	other, err = EventID(network, "att-1", 2)
	require.NoError(t, err)
	require.NotEqual(t, e.ID, other)
}

// "Attempt event history has one canonical creation link": every later event binds the exact
// immediately prior complete hash.
func TestEachEventBindsTheOneBeforeIt(t *testing.T) {
	l, _ := newLedger(t)
	_, issued, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	leased, err := l.Commit("att-1", Observation{
		Kind: KindDispatchAccepted, EvidenceHash: "lease-accepted",
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), leased.Revision)
	require.Equal(t, StateLeased, leased.State)
	require.Equal(t, issued.Hash, obj(t, leased.Signed)["prev_hash"],
		"the chain link must be the hash of the event immediately before")

	executing, err := l.Commit("att-1", Observation{
		Kind: KindClaimObserved, EvidenceHash: "claim",
	})
	require.NoError(t, err)
	require.Equal(t, leased.Hash, obj(t, executing.Signed)["prev_hash"])
	require.Equal(t, int64(3), executing.Revision)
}

// "exact replay is idempotent" - a retried commit of identical bytes is the same fact stated
// twice, and refusing it would turn a lost response into a stuck attempt.
func TestAnExactReplayIsIdempotent(t *testing.T) {
	l, _ := newLedger(t)
	_, _, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	// Issuing the same attempt again is refused rather than duplicated.
	_, _, err = l.Issue(joinedSpec("att-1"))
	require.ErrorIs(t, err, ErrAlreadyIssued)

	// And the store accepts a byte-identical re-append at a taken revision.
	head, ok, err := l.store.Head("att-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, l.store.Append(head, 0), "an exact replay of the issuing event")

	// A CONFLICT is a DIFFERENT event at a revision already taken, and it belongs at a
	// successor: at revision 1 the honest answer is "already issued", which is what the
	// caller actually did.
	leased, err := l.Commit("att-1", Observation{Kind: KindDispatchAccepted, EvidenceHash: "e"})
	require.NoError(t, err)
	at2, ok, err := l.store.At("att-1", 2)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, leased.Hash, at2.Hash)

	require.NoError(t, l.store.Append(at2, 1), "the same event again is the same fact")

	conflicting := at2
	conflicting.EventID = "different"
	conflicting.Signed = append([]byte(nil), at2.Signed...)
	conflicting.Signed[0] = ' '
	require.ErrorIs(t, l.store.Append(conflicting, 1), ErrConflict)

	// And a revision that is not the next one fails before anything moves.
	skipped := at2
	skipped.Revision = 9
	require.ErrorIs(t, l.store.Append(skipped, 2), ErrRevision)
}

// "Nonterminal attempt transitions are exhaustive" - the whole outline, row by row.
func TestEveryApprovedTransitionAndItsHoldEffect(t *testing.T) {
	for _, tc := range []struct {
		from string
		kind Kind
		to   string
		hold HoldEffect
	}{
		{StateIssued, KindDispatchAccepted, StateLeased, HoldReserved},
		{StateIssued, KindDispatchFailed, StateFailed, HoldReleased},
		{StateIssued, KindDeadlineSwept, StateExpired, HoldReleased},
		{StateIssued, KindCancelSwept, StateCancelled, HoldReleased},
		{StateLeased, KindClaimObserved, StateExecuting, HoldReserved},
		{StateLeased, KindEvidenceObserved, StateEvidenceComplete, HoldReserved},
		{StateLeased, KindExecutionFailed, StateFailed, HoldReleased},
		{StateLeased, KindDeadlineSwept, StateExpired, HoldReleased},
		{StateLeased, KindCancelSwept, StateCancelled, HoldReleased},
		{StateExecuting, KindEvidenceObserved, StateEvidenceComplete, HoldReserved},
		{StateExecuting, KindExecutionFailed, StateFailed, HoldReleased},
		{StateExecuting, KindDeadlineSwept, StateExpired, HoldReleased},
		{StateExecuting, KindCancelSwept, StateCancelled, HoldReleased},
		{StateEvidenceComplete, KindSettlementCommitted, StateSettled, HoldCaptured},
		{StateEvidenceComplete, KindEvidenceInvalid, StateFailed, HoldReleased},
		{StateEvidenceComplete, KindFinalizationCeiling, StateFailed, HoldReleased},
	} {
		to, hold, ok := Next(tc.from, tc.kind)
		require.True(t, ok, "%s + %s is in the approved table", tc.from, tc.kind)
		require.Equal(t, tc.to, to, "%s + %s", tc.from, tc.kind)
		require.Equal(t, tc.hold, hold, "%s + %s hold effect", tc.from, tc.kind)
	}
}

// "Every unlisted attempt transition fails without authority", and the attempt is unchanged.
func TestUnlistedTransitionsAreRefusedAndChangeNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		path []Kind
		bad  Kind
	}{
		{"issued straight to executing without a dispatch", nil, KindClaimObserved},
		{"issued straight to settled", nil, KindSettlementCommitted},
		{"leased back to issued", []Kind{KindDispatchAccepted}, KindIssued},
		{"executing back to leased", []Kind{KindDispatchAccepted, KindClaimObserved}, KindDispatchAccepted},
		{"evidence_complete back to executing",
			[]Kind{KindDispatchAccepted, KindClaimObserved, KindEvidenceObserved}, KindClaimObserved},
		// "expired solely because settlement storage was temporarily unavailable after timely
		// evidence" - the evidence arrived in time, and our own storage being slow is not the
		// consumer's fault. It ends on the finalization ceiling instead, a different clock.
		{"evidence_complete expired by a deadline sweep",
			[]Kind{KindDispatchAccepted, KindClaimObserved, KindEvidenceObserved}, KindDeadlineSwept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := newLedger(t)
			_, _, err := l.Issue(joinedSpec("att-1"))
			require.NoError(t, err)
			for _, k := range tc.path {
				_, cerr := l.Commit("att-1", Observation{Kind: k, EvidenceHash: "e"})
				require.NoError(t, cerr)
			}
			before, rev, _, err := l.State("att-1")
			require.NoError(t, err)

			_, err = l.Commit("att-1", Observation{Kind: tc.bad, EvidenceHash: "e"})
			require.ErrorIs(t, err, ErrNotAllowed)

			after, revAfter, _, err := l.State("att-1")
			require.NoError(t, err)
			require.Equal(t, before, after, "the state is unchanged")
			require.Equal(t, rev, revAfter, "and so is the chain")
		})
	}
}

// A TERMINAL ATTEMPT IS NOT REVIVABLE, by anyone - including Core.
func TestNoTerminalAttemptCanBeRevived(t *testing.T) {
	for _, end := range []struct {
		name string
		path []Kind
		obs  Observation
	}{
		{"settled", []Kind{KindDispatchAccepted, KindClaimObserved, KindEvidenceObserved},
			Observation{Kind: KindSettlementCommitted, EvidenceHash: "receipt", Reason: "settled"}},
		{"failed", nil,
			Observation{Kind: KindDispatchFailed, EvidenceHash: "e", Reason: "dispatch failed",
				ReleaseID: "rel-1", ReleaseIndex: 7}},
		{"expired", nil,
			Observation{Kind: KindDeadlineSwept, EvidenceHash: "sweep", Reason: "deadline",
				ReleaseID: "rel-1", ReleaseIndex: 7}},
		{"cancelled", nil,
			Observation{Kind: KindCancelSwept, EvidenceHash: "sweep", Reason: "cancelled",
				ReleaseID: "rel-1", ReleaseIndex: 7}},
	} {
		t.Run(end.name, func(t *testing.T) {
			l, _ := newLedger(t)
			_, _, err := l.Issue(joinedSpec("att-1"))
			require.NoError(t, err)
			for _, k := range end.path {
				_, cerr := l.Commit("att-1", Observation{Kind: k, EvidenceHash: "e"})
				require.NoError(t, cerr)
			}
			_, err = l.Commit("att-1", end.obs)
			require.NoError(t, err)

			state, rev, _, err := l.State("att-1")
			require.NoError(t, err)
			require.True(t, Terminal(state))

			// EVERY event is refused from here, not merely the obviously wrong ones.
			for _, k := range []Kind{
				KindDispatchAccepted, KindClaimObserved, KindEvidenceObserved,
				KindSettlementCommitted, KindExecutionFailed, KindDeadlineSwept,
				KindCancelSwept, KindEvidenceInvalid, KindFinalizationCeiling,
			} {
				_, cerr := l.Commit("att-1", Observation{Kind: k, EvidenceHash: "e", Reason: "r"})
				require.ErrorIs(t, cerr, ErrTerminal, "%s revived a %s attempt", k, state)
			}
			after, revAfter, _, err := l.State("att-1")
			require.NoError(t, err)
			require.Equal(t, state, after)
			require.Equal(t, rev, revAfter)
		})
	}
}

// "failed/expired/cancelled terminal events alone may name a release transition and settled
// never may" - settled captures the cost and releases the remainder through settlement.
func TestOnlyAReleasedTerminalNamesAReleaseTransition(t *testing.T) {
	l, _ := newLedger(t)
	_, _, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)
	for _, k := range []Kind{KindDispatchAccepted, KindClaimObserved, KindEvidenceObserved} {
		_, cerr := l.Commit("att-1", Observation{Kind: k, EvidenceHash: "e"})
		require.NoError(t, cerr)
	}
	_, err = l.Commit("att-1", Observation{
		Kind: KindSettlementCommitted, EvidenceHash: "receipt", Reason: "settled",
		ReleaseID: "rel-1", ReleaseIndex: 3,
	})
	require.Error(t, err, "a settled attempt may not name a release")

	// The same attempt settles without one.
	e, err := l.Commit("att-1", Observation{
		Kind: KindSettlementCommitted, EvidenceHash: "receipt", Reason: "settled",
	})
	require.NoError(t, err)
	require.Equal(t, HoldCaptured, e.Hold)
	require.NotContains(t, obj(t, e.Signed), "release_id")
}

// A terminal event records WHY, and a nonterminal one may not pretend to.
func TestATerminalEventRecordsItsReasonAndOthersDoNot(t *testing.T) {
	l, _ := newLedger(t)
	_, _, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	_, err = l.Commit("att-1", Observation{Kind: KindDispatchFailed, EvidenceHash: "e"})
	require.Error(t, err, "a terminal attempt must say why it ended")

	_, err = l.Commit("att-1", Observation{
		Kind: KindDispatchAccepted, EvidenceHash: "e", Reason: "not terminal",
	})
	require.Error(t, err, "a nonterminal event has no terminal reason")
}

// An event after issue records the EVIDENCE Core observed. Without it a state change is Core
// asserting rather than observing, which is the one thing this ledger exists to prevent.
func TestAnEventAfterIssueNamesItsEvidence(t *testing.T) {
	l, _ := newLedger(t)
	_, _, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	_, err = l.Commit("att-1", Observation{Kind: KindDispatchAccepted})
	require.Error(t, err)
	require.Contains(t, err.Error(), "evidence")
}

// Two writers racing produce two identical proposals and exactly one lands, so an attempt
// can never be at two revisions.
func TestConcurrentCommitsAppendExactlyOne(t *testing.T) {
	l, _ := newLedger(t)
	_, _, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)

	var wg sync.WaitGroup
	won := make(chan struct{}, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, cerr := l.Commit("att-1", Observation{
				Kind: KindDispatchAccepted, EvidenceHash: "lease",
			}); cerr == nil {
				won <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(won)
	require.Len(t, won, 1, "exactly one writer may advance the chain")

	state, rev, _, err := l.State("att-1")
	require.NoError(t, err)
	require.Equal(t, StateLeased, state)
	require.Equal(t, int64(2), rev)
}

// A direct attempt has NO lease and says so by omitting the member; a joined one must name
// its lease. An empty string would be a value, and a schema accepting one accepts a lease
// hash of nothing.
func TestOriginDecidesWhetherALeaseIsNamed(t *testing.T) {
	l, _ := newLedger(t)

	direct := joinedSpec("att-direct")
	direct.Origin = OriginDirect
	direct.LeaseHash = ""
	c, e, err := l.Issue(direct)
	require.NoError(t, err)
	require.NotContains(t, obj(t, c.Signed), "lease_hash")
	require.NotContains(t, obj(t, e.Signed), "lease_hash")

	withLease := joinedSpec("att-bad")
	withLease.Origin = OriginDirect
	_, _, err = l.Issue(withLease)
	require.Error(t, err, "a direct attempt cannot name a lease")

	noLease := joinedSpec("att-bad2")
	noLease.LeaseHash = ""
	_, _, err = l.Issue(noLease)
	require.Error(t, err, "a joined attempt is dispatched under a lease")

	unknown := joinedSpec("att-bad3")
	unknown.Origin = "sideways"
	_, _, err = l.Issue(unknown)
	require.Error(t, err)
}

// Issuing refuses what it cannot anchor.
func TestIssuingRefusesAnUnanchoredAttempt(t *testing.T) {
	l, _ := newLedger(t)
	for name, mut := range map[string]func(*IssueSpec){
		"no attempt id": func(s *IssueSpec) { s.AttemptID = "" },
		"no job":        func(s *IssueSpec) { s.JobID = "" },
		"no grant":      func(s *IssueSpec) { s.GrantHash = "" },
		"other network": func(s *IssueSpec) { s.Network = "somewhere-else" },
	} {
		s := joinedSpec("att-x")
		mut(&s)
		_, _, err := l.Issue(s)
		require.Error(t, err, name)
	}
}

// Committing against an attempt that does not exist is refused rather than creating one.
func TestCommittingNeedsAnAttempt(t *testing.T) {
	l, _ := newLedger(t)
	_, err := l.Commit("att-nobody", Observation{Kind: KindDispatchAccepted, EvidenceHash: "e"})
	require.ErrorIs(t, err, ErrNotFound)

	_, _, ok, err := l.State("att-nobody")
	require.NoError(t, err)
	require.False(t, ok)
}

// Every event's bytes are the ones its hash covers, and the hash is what the next revision
// binds - so a tampered event breaks the chain rather than only its own signature.
func TestTamperingWithAnEventBreaksTheChain(t *testing.T) {
	l, pub := newLedger(t)
	_, issued, err := l.Issue(joinedSpec("att-1"))
	require.NoError(t, err)
	leased, err := l.Commit("att-1", Observation{Kind: KindDispatchAccepted, EvidenceHash: "e"})
	require.NoError(t, err)

	altered := obj(t, issued.Signed)
	altered["hold_amount"] = "1"
	raw, err := json.Marshal(altered)
	require.NoError(t, err)
	require.Error(t, towerobj.Verify(pub, network, TypeEvent, Version, raw, "sig"),
		"an altered event does not verify")

	rehashed, err := towerobj.Hash(raw)
	require.NoError(t, err)
	require.NotEqual(t, obj(t, leased.Signed)["prev_hash"], rehashed,
		"and the successor no longer names it, so the chain shows the gap")
}

// A compensation snapshot is carried when there is one and omitted when there is not - the
// canonical absence again, in the member that decides what an operator is owed.
func TestTheCompensationSnapshotIsCarriedOrCanonicallyAbsent(t *testing.T) {
	l, _ := newLedger(t)
	s := joinedSpec("att-comp")
	s.CompensationSnapshot = "snapshot-hash"
	_, e, err := l.Issue(s)
	require.NoError(t, err)
	require.Equal(t, "snapshot-hash", obj(t, e.Signed)["compensation_snapshot"])
	require.NotContains(t, obj(t, e.Signed), "compensation_snapshot_absent",
		"absence is an omitted member, never a member saying it is absent")

	_, plain, err := l.Issue(joinedSpec("att-plain"))
	require.NoError(t, err)
	require.NotContains(t, obj(t, plain.Signed), "compensation_snapshot")
}

// The builder refuses each malformed shape directly, so a future caller assembling an event
// by hand cannot produce one the chain would accept but the spec would not.
func TestTheEventBuilderRefusesEveryMalformedShape(t *testing.T) {
	s := joinedSpec("att-1")
	for name, ev := range map[string]eventFields{
		"a successor with no prior":      {revision: 2, state: StateLeased, evidenceHash: "e"},
		"a first event naming a prior":   {revision: 1, state: StateIssued, prevHash: "h"},
		"a successor with no evidence":   {revision: 2, state: StateLeased, prevHash: "h"},
		"issuing that names evidence":    {revision: 1, state: StateIssued, evidenceHash: "e"},
		"a terminal with no reason":      {revision: 2, state: StateFailed, prevHash: "h", evidenceHash: "e"},
		"a nonterminal naming a reason":  {revision: 2, state: StateLeased, prevHash: "h", evidenceHash: "e", reason: "why"},
		"a nonterminal naming a release": {revision: 2, state: StateLeased, prevHash: "h", evidenceHash: "e", releaseID: "r"},
		"a settled naming a release":     {revision: 2, state: StateSettled, prevHash: "h", evidenceHash: "e", reason: "ok", releaseID: "r"},
	} {
		_, err := buildEvent(s, ev)
		require.Error(t, err, name)
	}

	// And the shape that IS legal: a released terminal naming its release transition.
	got, err := buildEvent(s, eventFields{
		id: "e-1", revision: 2, state: StateFailed, kind: KindDispatchFailed,
		hold: HoldReleased, prevHash: "h", evidenceHash: "e", reason: "dispatch failed",
		releaseID: "rel-1", releaseIndex: 9,
	})
	require.NoError(t, err)
	require.Equal(t, "rel-1", got["release_id"])
	require.Equal(t, "9", got["release_index"])
}

// A ledger built with no clock and no sequencer still works: a zero Config must not panic on
// the first attempt, and the ordering it assigns must still advance.
func TestAZeroConfigStillAssignsAClockAndAnOrdering(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	l := New(Config{Network: network, Signer: priv}, nil)

	_, first, err := l.Issue(joinedSpec("att-a"))
	require.NoError(t, err)
	_, second, err := l.Issue(joinedSpec("att-b"))
	require.NoError(t, err)

	require.NotEmpty(t, obj(t, first.Signed)["committed"])
	require.NotEqual(t, obj(t, first.Signed)["sequence"], obj(t, second.Signed)["sequence"],
		"two attempts must not share an ordering position")
}

// The network defaults to the ledger's own, so a caller cannot omit it into ambiguity.
func TestAnUnsetNetworkTakesTheLedgersOwn(t *testing.T) {
	l, _ := newLedger(t)
	s := joinedSpec("att-1")
	s.Network = ""
	_, e, err := l.Issue(s)
	require.NoError(t, err)
	require.Equal(t, network, obj(t, e.Signed)["network"])
}

// Appending a successor for an attempt that was never issued creates nothing. A store that
// accepted it would have a chain with no beginning.
func TestAppendingASuccessorToNothingIsRefused(t *testing.T) {
	s := NewMemStore()
	require.ErrorIs(t, s.Append(Record{AttemptID: "att-1", Revision: 2}, 1), ErrNotFound)

	_, ok, err := s.Head("att-1")
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = s.At("att-1", 1)
	require.NoError(t, err)
	require.False(t, ok)
}

// A signer that is not a key fails at the first object rather than producing something that
// will not verify much later.
func TestALedgerWithoutAUsableSignerFailsAtOnce(t *testing.T) {
	l := New(Config{Network: network, Signer: ed25519.PrivateKey("too short")}, nil)
	require.Panics(t, func() { _, _, _ = l.Issue(joinedSpec("att-1")) },
		"ed25519 panics on a malformed key; catching it here names the cause")
}
