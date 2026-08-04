package toweradmit

// The atomic admission bundle, per features/tower/public_enrollment.feature:
//
//	"the proof/token, lifecycle event, certificate, and quarantine lease scoped to that
//	 Tower ID commit atomically or none do"
//
// Enroll used to consume the token and then insert the Tower as two separate writes. That
// is fine until the process dies between them, and then the token is spent while no Tower
// exists - the operator holds a receipt for an admission that never happened and cannot
// retry. Admit does both in ONE transaction, so the pair either happens or does not.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sampleAdmission(id, key string) Tower {
	now := time.Now()
	return Tower{
		ID: id, Owner: "acct-1", KeyHash: key,
		TLSKeyHash: key + "-tls",
		State:      StateQuarantine,
		EnrolledAt: now, LeaseExpires: now.Add(time.Hour),
		// The bundle's other three members, recorded with the Tower rather than beside it:
		// one row means one commit, and there is no window where they disagree.
		LifecycleRevision: 1,
		LifecycleHash:     "lifecycle-hash-1",
		CertSerial:        "12345",
		LeaseSequence:     1,
		ProtocolVersion:   1,
		Capabilities:      []string{"relay"},
	}
}

func TestAdmitCommitsTheWholeBundleOrNoneOfIt(t *testing.T) {
	for name, newStore := range map[string]func(*testing.T) Store{
		"memory":   func(*testing.T) Store { return NewMemStore() },
		"postgres": func(t *testing.T) Store { return pgStore(t) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			require.NoError(t, s.PutToken(Token{ID: "tok-1", Owner: "acct-1", Expires: time.Now().Add(time.Hour)}))

			tw := sampleAdmission("tw-1", "key-1")
			ok, err := s.Admit("tok-1", tw)
			require.NoError(t, err)
			require.True(t, ok)

			// The token is gone AND the Tower exists.
			_, found, err := s.GetToken("tok-1")
			require.NoError(t, err)
			require.False(t, found, "the enrollment token is irreversibly consumed")

			got, found, err := s.TowerByID("tw-1")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "12345", got.CertSerial)
			require.EqualValues(t, 1, got.LifecycleRevision)
			require.Equal(t, "lifecycle-hash-1", got.LifecycleHash)
			require.EqualValues(t, 1, got.LeaseSequence)
			require.Equal(t, tw.TLSKeyHash, got.TLSKeyHash)
			require.EqualValues(t, 1, got.ProtocolVersion)
			require.Equal(t, []string{"relay"}, got.Capabilities)
		})
	}
}

func TestAdmitWithAnAlreadyConsumedTokenAdmitsNothing(t *testing.T) {
	for name, newStore := range map[string]func(*testing.T) Store{
		"memory":   func(*testing.T) Store { return NewMemStore() },
		"postgres": func(t *testing.T) Store { return pgStore(t) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			require.NoError(t, s.PutToken(Token{ID: "tok-1", Owner: "acct-1", Expires: time.Now().Add(time.Hour)}))

			ok, err := s.Admit("tok-1", sampleAdmission("tw-1", "key-1"))
			require.NoError(t, err)
			require.True(t, ok)

			// A second admission on the same token must leave NO trace: no partial identity
			// for a later attempt to adopt as real.
			ok, err = s.Admit("tok-1", sampleAdmission("tw-2", "key-2"))
			require.NoError(t, err)
			require.False(t, ok)

			_, found, err := s.TowerByID("tw-2")
			require.NoError(t, err)
			require.False(t, found, "no Tower may exist from a spent token")

			_, found, err = s.TowerByKey("key-2")
			require.NoError(t, err)
			require.False(t, found, "and no key is burned by an admission that did not happen")
		})
	}
}

func TestAdmitRollsBackWhenTheTowerCannotBeWritten(t *testing.T) {
	// A duplicate identity key fails the insert. The token must survive that, because the
	// operator's next attempt with a correct key is a legitimate one.
	for name, newStore := range map[string]func(*testing.T) Store{
		"memory":   func(*testing.T) Store { return NewMemStore() },
		"postgres": func(t *testing.T) Store { return pgStore(t) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			require.NoError(t, s.PutToken(Token{ID: "tok-1", Owner: "acct-1", Expires: time.Now().Add(time.Hour)}))
			require.NoError(t, s.PutTower(sampleAdmission("tw-existing", "key-taken")))

			_, err := s.Admit("tok-1", sampleAdmission("tw-new", "key-taken"))
			require.Error(t, err, "one key, one Tower")

			_, found, err := s.GetToken("tok-1")
			require.NoError(t, err)
			require.True(t, found, "a failed admission must not burn the token")

			_, found, err = s.TowerByID("tw-new")
			require.NoError(t, err)
			require.False(t, found, "and must leave no partial Tower behind")
		})
	}
}

func TestConcurrentAdmissionsOnOneTokenAdmitExactlyOne(t *testing.T) {
	for name, newStore := range map[string]func(*testing.T) Store{
		"memory":   func(*testing.T) Store { return NewMemStore() },
		"postgres": func(t *testing.T) Store { return pgStore(t) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			require.NoError(t, s.PutToken(Token{ID: "tok-1", Owner: "acct-1", Expires: time.Now().Add(time.Hour)}))

			const racers = 8
			type outcome struct {
				ok  bool
				err error
			}
			results := make(chan outcome, racers)
			start := make(chan struct{})
			for i := 0; i < racers; i++ {
				go func(i int) {
					<-start
					ok, err := s.Admit("tok-1", sampleAdmission(
						"tw-"+string(rune('a'+i)), "key-"+string(rune('a'+i))))
					results <- outcome{ok, err}
				}(i)
			}
			close(start)

			admitted := 0
			for i := 0; i < racers; i++ {
				o := <-results
				if o.err == nil && o.ok {
					admitted++
				}
			}
			require.Equal(t, 1, admitted, "one token admits exactly one Tower, whoever races")
		})
	}
}
