package admit

// The Tower governance record: an append-only list of attributed events per Tower.
//
// Contract: features/tower/governance_record.feature (approved 2026-11-13). Every
// assertion runs against BOTH stores, because two implementations of one interface are only
// interchangeable if the same assertions hold against both.

import (
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/pgtest"
)

// eachStore runs body against the in-process store and, when a test database is configured,
// the durable one.
func eachStore(t *testing.T, body func(t *testing.T, s Store)) {
	t.Run("mem", func(t *testing.T) { body(t, NewMemStore()) })
	t.Run("pg", func(t *testing.T) {
		s := pgStore(t)
		clearEventsForTest(t, s.(*PGStore).db)
		body(t, s)
	})
}

func enrolled(t *testing.T, r *Registry, owner, key string) Tower {
	t.Helper()
	tok, err := r.IssueToken(owner)
	require.NoError(t, err)
	tw, err := r.Enroll(tok, key)
	require.NoError(t, err)
	return tw
}

func TestEventsAreReadBackNewestFirstAndPerTower(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
		for i, kind := range []string{"session_opened", "session_closed", "session_opened"} {
			require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(time.Duration(i) * time.Minute),
				Kind: kind, Actor: ActorTower, Instance: "web.1"}))
		}
		require.NoError(t, r.Record(Event{TowerID: "tw-2", Kind: "session_opened", Actor: ActorTower}))

		got, err := r.Events("tw-1", 10)
		require.NoError(t, err)
		require.Len(t, got, 3, "another Tower's events are not this Tower's")
		require.Equal(t, base.Add(2*time.Minute), got[0].At.UTC(), "newest first")
		require.Equal(t, "session_closed", got[1].Kind)
		require.Equal(t, "web.1", got[2].Instance)

		limited, err := r.Events("tw-1", 2)
		require.NoError(t, err)
		require.Len(t, limited, 2)

		none, err := r.Events("tw-nope", 10)
		require.NoError(t, err)
		require.Empty(t, none)
	})
}

func TestARecordedEventKeepsEveryField(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		require.NoError(t, r.Record(Event{TowerID: "tw-1", Kind: "lifecycle", Actor: "admin:gh:luis",
			From: StateQuarantine, To: StateActive, Reason: "checked with operator", Instance: "web.2",
			Detail: map[string]any{"refusals": float64(3)}}))
		got, err := r.Events("tw-1", 1)
		require.NoError(t, err)
		require.Len(t, got, 1)
		ev := got[0]
		require.Equal(t, "admin:gh:luis", ev.Actor)
		require.Equal(t, StateQuarantine, ev.From)
		require.Equal(t, StateActive, ev.To)
		require.Equal(t, "checked with operator", ev.Reason)
		require.Equal(t, "web.2", ev.Instance)
		require.Equal(t, float64(3), ev.Detail["refusals"])
		require.False(t, ev.At.IsZero(), "Core stamps the time when the caller did not")
		require.WithinDuration(t, time.Now(), ev.At, time.Minute)
	})
}

func TestAnEventWithoutATowerKindOrActorIsRefused(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		require.Error(t, r.Record(Event{Kind: "x", Actor: ActorSystem}))
		require.Error(t, r.Record(Event{TowerID: "tw-1", Actor: ActorSystem}))
		require.Error(t, r.Record(Event{TowerID: "tw-1", Kind: "x"}))
	})
}

func TestTheReasonIsCleanedBeforeItIsStored(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		require.NoError(t, r.Record(Event{TowerID: "tw-1", Kind: "lifecycle", Actor: ActorSystem,
			Reason: "line one\nline two\x1b[31m"}))
		got, err := r.Events("tw-1", 1)
		require.NoError(t, err)
		require.Equal(t, "line one line two [31m", got[0].Reason)
	})
}

func TestCleanReason(t *testing.T) {
	long := strings.Repeat("a", 300)
	for _, tc := range []struct{ in, want string }{
		{"approved after email with operator", "approved after email with operator"},
		{"line one\nline two", "line one line two"},
		{long, long[:280]},
		{"", ""},
		{"   ", ""},
		{"tab\tand\x1bescape", "tab and escape"},
		{"  padded  ", "padded"},
		{"a\r\n\r\nb", "a b"},
		{strings.Repeat("é", 300), strings.Repeat("é", 280)},
	} {
		require.Equal(t, tc.want, CleanReason(tc.in), "%q", tc.in)
	}
}

// THE LIFECYCLE CHANGE AND ITS RECORD COMMIT TOGETHER, or neither does.
func TestTransitionAsRecordsTheChangeWithItsActor(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		tw := enrolled(t, r, "acct-1", "key-1")

		require.NoError(t, r.TransitionAs(tw.ID, StateActive,
			Event{Kind: "lifecycle", Actor: "admin:broker-key", Reason: "ok", Instance: "web.1"}))

		got, _ := r.Get(tw.ID)
		require.Equal(t, StateActive, got.State)
		evs, err := r.Events(tw.ID, 10)
		require.NoError(t, err)
		require.Len(t, evs, 1)
		require.Equal(t, StateQuarantine, evs[0].From, "the prior state comes from the record, not the caller")
		require.Equal(t, StateActive, evs[0].To)
		require.Equal(t, "admin:broker-key", evs[0].Actor)
		require.Equal(t, tw.ID, evs[0].TowerID)
	})
}

func TestARefusedTransitionAsChangesNothingAndRecordsNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		tw := enrolled(t, r, "acct-1", "key-1")

		err := r.TransitionAs(tw.ID, StateQuarantine, Event{Kind: "lifecycle", Actor: ActorSystem})
		require.ErrorIs(t, err, ErrAlreadyInState, "a same-state request is named as such")
		err = r.TransitionAs(tw.ID, StateDraining, Event{Kind: "lifecycle", Actor: ActorSystem})
		require.ErrorIs(t, err, ErrIllegalTransition)
		require.Error(t, r.TransitionAs("tw-nope", StateActive, Event{Kind: "lifecycle", Actor: ActorSystem}))

		evs, err := r.Events(tw.ID, 10)
		require.NoError(t, err)
		require.Empty(t, evs, "the caller records refusals; the registry records only what happened")
	})
}

func TestATransitionWhoseRecordCannotBeWrittenDoesNotHappen(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		tw := enrolled(t, r, "acct-1", "key-1")

		// No actor: the record is refused, so the change must be too.
		require.Error(t, r.TransitionAs(tw.ID, StateActive, Event{Kind: "lifecycle"}))
		got, _ := r.Get(tw.ID)
		require.Equal(t, StateQuarantine, got.State, "a change Core cannot record is a change Core did not make")
	})
}

func TestTransitionAsRefusesToOverwriteAConcurrentDecision(t *testing.T) {
	inner := NewMemStore()
	r := NewWithStore(Config{}, inner)
	tw := enrolled(t, r, "acct-1", "key-1")
	r2 := NewWithStore(Config{}, &interposingStore{Store: inner, after: func() {
		moveTowerBehindTheCaller(t, inner, tw.ID, StateRevoked)
	}})
	require.Error(t, r2.TransitionAs(tw.ID, StateActive, Event{Kind: "lifecycle", Actor: ActorSystem}))
	evs, err := r.Events(tw.ID, 10)
	require.NoError(t, err)
	require.Empty(t, evs, "the losing write records nothing")
}

func TestTheDurableRecordSurvivesARestartAndCannotBeEdited(t *testing.T) {
	s := pgStore(t)
	db := s.(*PGStore).db
	clearEventsForTest(t, db)
	require.NoError(t, NewWithStore(Config{}, s).Record(Event{TowerID: "tw-1", Kind: "enrolled", Actor: "operator:acct-1"}))

	evs, err := NewWithStore(Config{}, s).Events("tw-1", 10)
	require.NoError(t, err)
	require.Len(t, evs, 1, "a redeploy does not forget the record")

	_, err = db.Exec(`UPDATE rogerai.tower_events SET actor = 'someone else'`)
	require.Error(t, err, "the record is append-only in the database, not only in the code")
	_, err = db.Exec(`DELETE FROM rogerai.tower_events`)
	require.Error(t, err)
}

func TestTheDurableRecordReportsAnOutage(t *testing.T) {
	dsn := pgtest.DSN(t)
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	s, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	r := NewWithStore(Config{}, s)
	require.ErrorIs(t, r.Record(Event{TowerID: "tw-1", Kind: "x", Actor: ActorSystem}), ErrUnavailable)
	_, err = r.Events("tw-1", 1)
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestFindEventReturnsTheNewestOrOldestOfTheGivenKinds(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
		for i, kind := range []string{"session_opened", "canary", "session_closed", "session_opened", "canary"} {
			require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(time.Duration(i) * time.Minute),
				Kind: kind, Actor: ActorTower}))
		}
		link := []string{"session_opened", "session_closed", "session_stale", "session_resumed"}

		newest, ok, err := r.FindEvent("tw-1", link, false)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "session_opened", newest.Kind)
		require.Equal(t, base.Add(3*time.Minute), newest.At.UTC())

		oldest, ok, err := r.FindEvent("tw-1", []string{"session_opened"}, true)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, base, oldest.At.UTC(), "the first connection is the oldest session_opened")

		_, ok, err = r.FindEvent("tw-1", []string{"lease_lapsed"}, false)
		require.NoError(t, err)
		require.False(t, ok)
		_, ok, err = r.FindEvent("tw-2", link, false)
		require.NoError(t, err)
		require.False(t, ok)
	})
}

// ONE "went quiet" EVENT ACROSS INSTANCES. Both brokers sweep; only the one whose view of the
// latest event is still current may write.
func TestRecordIfLastWritesOnlyWhenTheLatestEventIsTheOneTheCallerSaw(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		link := []string{"session_opened", "session_stale"}
		require.NoError(t, r.Record(Event{TowerID: "tw-1", Kind: "session_opened", Actor: ActorTower}))
		seen, ok, err := r.FindEvent("tw-1", link, false)
		require.NoError(t, err)
		require.True(t, ok)

		stale := Event{TowerID: "tw-1", Kind: "session_stale", Actor: ActorSystem}
		won, err := r.RecordIfLast(stale, link, seen.ID)
		require.NoError(t, err)
		require.True(t, won)

		won, err = r.RecordIfLast(stale, link, seen.ID)
		require.NoError(t, err)
		require.False(t, won, "the second instance saw a latest event that is no longer the latest")

		evs, err := r.Events("tw-1", 10)
		require.NoError(t, err)
		require.Len(t, evs, 2)

		// No prior event of those kinds is expressed as 0.
		won, err = r.RecordIfLast(Event{TowerID: "tw-2", Kind: "lease_lapsed", Actor: ActorSystem},
			[]string{"lease_lapsed"}, 0)
		require.NoError(t, err)
		require.True(t, won)
		require.Error(t, func() error {
			_, err := r.RecordIfLast(Event{TowerID: "tw-2", Kind: "lease_lapsed"}, []string{"lease_lapsed"}, 0)
			return err
		}(), "an unattributed event is refused here too")
	})
}

func TestRecordIfLastHoldsUnderConcurrentSweeps(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		link := []string{"session_opened", "session_stale"}
		require.NoError(t, r.Record(Event{TowerID: "tw-1", Kind: "session_opened", Actor: ActorTower}))
		seen, _, err := r.FindEvent("tw-1", link, false)
		require.NoError(t, err)

		var wins atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				won, err := r.RecordIfLast(Event{TowerID: "tw-1", Kind: "session_stale", Actor: ActorSystem}, link, seen.ID)
				if err == nil && won {
					wins.Add(1)
				}
			}()
		}
		wg.Wait()
		require.Equal(t, int64(1), wins.Load(), "exactly one instance records it")
	})
}

func TestExpireLeaseAsEndsTheLeaseAndRecordsItTogether(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		tw := enrolled(t, r, "acct-1", "key-1")
		require.NoError(t, r.ExpireLeaseAs(tw.ID, Event{Kind: "lease_expired", Actor: "admin:broker-key", Reason: "x"}))
		got, _ := r.Get(tw.ID)
		require.True(t, got.LeaseExpires.Before(time.Now()))
		evs, err := r.Events(tw.ID, 5)
		require.NoError(t, err)
		require.Len(t, evs, 1)
		require.Equal(t, "admin:broker-key", evs[0].Actor)

		// Unrecordable: refused, and the lease stands.
		tw2 := enrolled(t, r, "acct-1", "key-2")
		require.Error(t, r.ExpireLeaseAs(tw2.ID, Event{Kind: "lease_expired"}))
		got2, _ := r.Get(tw2.ID)
		require.True(t, got2.LeaseExpires.After(time.Now()))
		require.Error(t, r.ExpireLeaseAs("tw-nope", Event{Kind: "lease_expired", Actor: ActorSystem}))
	})
}

func TestExpireLeaseAsRefusesToOverwriteAConcurrentDecision(t *testing.T) {
	inner := NewMemStore()
	r := NewWithStore(Config{}, inner)
	tw := enrolled(t, r, "acct-1", "key-1")
	r2 := NewWithStore(Config{}, &interposingStore{Store: inner, after: func() {
		moveTowerBehindTheCaller(t, inner, tw.ID, StateRevoked)
	}})
	require.Error(t, r2.ExpireLeaseAs(tw.ID, Event{Kind: "lease_expired", Actor: ActorSystem}))
}

// THE REFUSAL MESSAGE IS UNCHANGED by the new classification: callers and operators already
// read "a quarantine Tower cannot become draining".
func TestARefusedTransitionKeepsItsMessage(t *testing.T) {
	r := NewWithStore(Config{}, NewMemStore())
	tw := enrolled(t, r, "acct-1", "key-1")
	err := r.Transition(tw.ID, StateDraining)
	require.EqualError(t, err, "a quarantine Tower cannot become draining")
	require.ErrorIs(t, err, ErrIllegalTransition)
	require.EqualError(t, r.Transition(tw.ID, "bogus"), `"bogus" is not a Tower state`)
}

// The store itself refuses an unattributed event, not only the registry in front of it.
func TestEveryStoreRefusesAnUnattributedEvent(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		tw := enrolled(t, r, "acct-1", "key-1")
		bad := Event{TowerID: tw.ID, Kind: "x"} // no actor
		require.Error(t, s.AppendEvent(bad))
		_, err := s.CASTowerEvent(tw, bad)
		require.Error(t, err)
		_, err = s.AppendEventIfLast(bad, []string{"x"}, 0)
		require.Error(t, err)
	})
}

func TestCASTowerEventLosesToAStaleRevisionAndWritesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		tw := enrolled(t, r, "acct-1", "key-1")
		stale := tw
		moveTowerBehindTheCaller(t, s, tw.ID, StateSuspended)
		stale.State = StateActive
		won, err := s.CASTowerEvent(stale, Event{TowerID: tw.ID, Kind: "lifecycle", Actor: ActorSystem, At: time.Now()})
		require.NoError(t, err)
		require.False(t, won)
		evs, err := s.Events(tw.ID, 5)
		require.NoError(t, err)
		require.Empty(t, evs, "a lost CAS writes no event")
	})
}

func TestEventsAtTheSameInstantAreOrderedByID(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		at := time.Now().UTC().Truncate(time.Microsecond)
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: at, Kind: "a", Actor: ActorSystem}))
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: at, Kind: "b", Actor: ActorSystem}))
		evs, err := r.Events("tw-1", 5)
		require.NoError(t, err)
		require.Equal(t, "b", evs[0].Kind, "the later write wins a tie")
		first, _, err := r.FindEvent("tw-1", []string{"a", "b"}, true)
		require.NoError(t, err)
		require.Equal(t, "a", first.Kind)
	})
}

func TestEveryEventPathReportsAnOutage(t *testing.T) {
	dsn := pgtest.DSN(t)
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	s, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	r := NewWithStore(Config{}, s)
	ev := Event{Kind: "x", Actor: ActorSystem}

	_, _, err = r.FindEvent("tw-1", []string{"x"}, false)
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = r.RecordIfLast(Event{TowerID: "tw-1", Kind: "x", Actor: ActorSystem}, []string{"x"}, 0)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, r.TransitionAs("tw-1", StateActive, ev), ErrUnavailable)
	require.ErrorIs(t, r.ExpireLeaseAs("tw-1", ev), ErrUnavailable)
	_, err = s.CASTowerEvent(Tower{ID: "tw-1"}, Event{TowerID: "tw-1", Kind: "x", Actor: ActorSystem})
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = s.AppendEventIfLast(Event{TowerID: "tw-1", Kind: "x", Actor: ActorSystem}, []string{"x"}, 0)
	require.ErrorIs(t, err, ErrUnavailable)
}

// A store whose event write fails AFTER the Tower row was written must not leave the row
// changed: that is the whole point of one transaction.
func TestTheDurableTransitionRollsBackWhenItsEventCannotBeWritten(t *testing.T) {
	s := pgStore(t)
	clearEventsForTest(t, s.(*PGStore).db)
	r := NewWithStore(Config{}, s)
	tw := enrolled(t, r, "acct-1", "key-1")

	// Detail that cannot be encoded as JSON fails the insert inside the transaction.
	err := r.TransitionAs(tw.ID, StateActive, Event{Kind: "lifecycle", Actor: ActorSystem,
		Detail: map[string]any{"bad": make(chan int)}})
	require.Error(t, err)
	got, _ := r.Get(tw.ID)
	require.Equal(t, StateQuarantine, got.State, "the row change rolled back with the event")
}

// THE LIMIT APPLIES AFTER ORDERING, in both stores: events written out of time order must
// still come back as the newest N.
func TestEventsLimitTheNewestEvenWhenWrittenOutOfOrder(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		base := time.Now().UTC().Truncate(time.Microsecond)
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(time.Hour), Kind: "newest", Actor: ActorSystem}))
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base, Kind: "oldest", Actor: ActorSystem}))
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: base.Add(time.Minute), Kind: "middle", Actor: ActorSystem}))
		evs, err := r.Events("tw-1", 2)
		require.NoError(t, err)
		require.Equal(t, []string{"newest", "middle"}, []string{evs[0].Kind, evs[1].Kind})
	})
}

func TestCountEventsCountsAKindAndReasonSinceATime(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		now := time.Now().UTC().Truncate(time.Microsecond)
		for i, reason := range []string{"a", "a", "b"} {
			require.NoError(t, r.Record(Event{TowerID: "tw-1", At: now.Add(-time.Duration(i) * time.Minute),
				Kind: "request_refused", Actor: ActorTower, Reason: reason}))
		}
		require.NoError(t, r.Record(Event{TowerID: "tw-1", At: now.Add(-2 * time.Hour), Kind: "request_refused", Actor: ActorTower, Reason: "a"}))
		require.NoError(t, r.Record(Event{TowerID: "tw-2", At: now, Kind: "request_refused", Actor: ActorTower, Reason: "a"}))

		n, err := r.CountEvents("tw-1", "request_refused", "", now.Add(-time.Hour))
		require.NoError(t, err)
		require.Equal(t, 3, n, "any reason, inside the hour, this Tower only")
		n, err = r.CountEvents("tw-1", "request_refused", "a", now.Add(-time.Hour))
		require.NoError(t, err)
		require.Equal(t, 2, n)
		n, err = r.CountEvents("tw-1", "session_opened", "", now.Add(-time.Hour))
		require.NoError(t, err)
		require.Zero(t, n)
	})
}

// ONE READ FOR THE WHOLE LIST: the admin Towers list must not run a handful of queries per
// Tower.
func TestLinkSummariesReadEveryTowersLinkHistoryAtOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		r := NewWithStore(Config{}, s)
		base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
		rec := func(tw, kind string, min int) {
			require.NoError(t, r.Record(Event{TowerID: tw, At: base.Add(time.Duration(min) * time.Minute), Kind: kind,
				Actor: ActorTower, Detail: map[string]any{"last_seen": "x"}}))
		}
		rec("tw-1", "session_opened", 0)
		rec("tw-1", "session_closed", 5)
		rec("tw-1", "session_opened", 10)
		rec("tw-1", "session_stale", 20)
		rec("tw-1", "canary", 25) // not a link event
		rec("tw-2", "session_opened", 3)

		sums, err := r.LinkSummaries([]string{"session_opened", "session_resumed", "session_closed", "session_stale"})
		require.NoError(t, err)
		one := sums["tw-1"]
		require.Equal(t, base, one.First.At.UTC(), "the first session_opened")
		require.Equal(t, "session_stale", one.Latest.Kind)
		require.Equal(t, base.Add(10*time.Minute), one.LastOf["session_opened"].At.UTC())
		require.Equal(t, base.Add(5*time.Minute), one.LastOf["session_closed"].At.UTC())
		require.Equal(t, "x", one.Latest.Detail["last_seen"])
		require.Equal(t, "session_opened", sums["tw-2"].Latest.Kind)
		_, has := sums["tw-3"]
		require.False(t, has)
	})
}

func TestTheDurableRecordCannotBeTruncatedByTheApp(t *testing.T) {
	s := pgStore(t)
	db := s.(*PGStore).db
	_, err := db.Exec(`TRUNCATE rogerai.tower_events`)
	require.Error(t, err, "TRUNCATE skips row triggers, so it has its own guard")
}

// clearEventsForTest empties the record between tests. The table refuses TRUNCATE by
// trigger, so a test (as the database superuser) suspends triggers for one transaction.
func clearEventsForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback() //nolint:errcheck
	_, err = tx.Exec(`SET LOCAL session_replication_role = replica`)
	require.NoError(t, err)
	_, err = tx.Exec(`TRUNCATE rogerai.tower_events`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}
