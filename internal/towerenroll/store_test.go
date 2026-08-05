package towerenroll

// Enrollment state that outlives the process.
//
// The challenge and the committed-transaction record were both process-local maps, which
// is the same defect fixed twice already this week - and here the second one is the more
// serious:
//
//   - A CHALLENGE issued by instance A is unknown to instance B, so enrollment behind a
//     load balancer fails unless both calls happen to land on the same process.
//   - The COMMITTED map is what makes the spec's "response was lost" retry work. Losing it
//     means the token has already been consumed while nothing remembers the outcome, so
//     the operator's retry is refused as a spent token and their Tower identity is
//     unreachable. That is the exact situation idempotency exists to prevent, and it is
//     unrecoverable without an administrator.

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/keypurpose"
	"rogerai.fm/roger/v5/internal/toweradmit"
	"rogerai.fm/roger/v5/internal/towercert"
)

// twoInstances builds two enrollers over ONE shared state, which is what a two-instance
// deployment is.
func twoInstances(t *testing.T) (*Enroller, *Enroller, *toweradmit.Registry, Store) {
	t.Helper()
	reg := toweradmit.NewWithStore(toweradmit.Config{}, toweradmit.NewMemStore())
	auth, err := towercert.NewAuthority(towercert.Config{TTL: time.Hour})
	require.NoError(t, err)
	shared := NewMemStore()

	mk := func() *Enroller {
		e, err := New(Config{
			Registry: reg, Authority: auth, Policy: &stubPolicy{refuse: map[string]error{}},
			MinVersion: 1, MaxVersion: 2, MaxSkew: 5 * time.Minute, Store: shared,
		})
		require.NoError(t, err)
		return e
	}
	return mk(), mk(), reg, shared
}

func requestFor(t *testing.T, reg *toweradmit.Registry, ch Challenge, owner, txn string, k towerKeys) Request {
	t.Helper()
	return Request{
		Operator: owner, TokenID: ch.TokenID, TransactionID: txn,
		Nonce: ch.Nonce, IdentityKey: k.identityPub,
		Signature: ed25519.Sign(k.identityPriv, ch.SigningInput()),
		CSR:       csrFor(t, k.tls), ProtocolVersion: 1,
		Realm: keypurpose.RealmTower, Now: time.Now(), Capabilities: []string{"relay"},
	}
}

func TestAChallengeIssuedOnOneInstanceIsAnsweredOnAnother(t *testing.T) {
	a, b, reg, _ := twoInstances(t)
	k := newTowerKeys(t)

	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := a.Challenge(token)
	require.NoError(t, err)

	res, err := b.Enroll(requestFor(t, reg, ch, "acct-1", "txn-1", k))
	require.NoError(t, err, "enrollment behind a load balancer must not depend on hitting one process")
	require.NotEmpty(t, res.TowerID)
}

func TestAChallengeIsStillSpentOnlyOnceAcrossInstances(t *testing.T) {
	// The one-time nonce is what stops a replay. Sharing it must not weaken that.
	a, b, reg, _ := twoInstances(t)
	k := newTowerKeys(t)

	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := a.Challenge(token)
	require.NoError(t, err)

	_, err = a.Enroll(requestFor(t, reg, ch, "acct-1", "txn-1", k))
	require.NoError(t, err)

	other := newTowerKeys(t)
	_, err = b.Enroll(requestFor(t, reg, ch, "acct-1", "txn-2", other))
	require.Error(t, err, "the other instance must see the nonce as spent")
}

func TestARetryReachesTheOriginalOutcomeFromAnyInstance(t *testing.T) {
	// The lost-response case, which is why idempotency exists. The retry may land anywhere.
	a, b, reg, _ := twoInstances(t)
	k := newTowerKeys(t)

	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := a.Challenge(token)
	require.NoError(t, err)

	first, err := a.Enroll(requestFor(t, reg, ch, "acct-1", "txn-1", k))
	require.NoError(t, err)

	// Same transaction, same key proof, different instance.
	retry := requestFor(t, reg, ch, "acct-1", "txn-1", k)
	second, err := b.Enroll(retry)
	require.NoError(t, err, "the operator must be able to recover their identity, not be told the token is spent")
	require.Equal(t, first.TowerID, second.TowerID)
	require.Equal(t, first.Certificate.SerialNumber, second.Certificate.SerialNumber)
	require.Len(t, reg.ByOwner("acct-1"), 1, "and no second Tower exists")
}

func TestARetryAfterARestartStillReturnsTheIdentity(t *testing.T) {
	// A restart is a new Enroller over the same state.
	reg := toweradmit.NewWithStore(toweradmit.Config{}, toweradmit.NewMemStore())
	auth, err := towercert.NewAuthority(towercert.Config{TTL: time.Hour})
	require.NoError(t, err)
	shared := NewMemStore()
	mk := func() *Enroller {
		e, err := New(Config{
			Registry: reg, Authority: auth, Policy: &stubPolicy{refuse: map[string]error{}},
			MinVersion: 1, MaxVersion: 2, MaxSkew: 5 * time.Minute, Store: shared,
		})
		require.NoError(t, err)
		return e
	}
	k := newTowerKeys(t)

	before := mk()
	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := before.Challenge(token)
	require.NoError(t, err)
	first, err := before.Enroll(requestFor(t, reg, ch, "acct-1", "txn-1", k))
	require.NoError(t, err)

	after := mk()
	second, err := after.Enroll(requestFor(t, reg, ch, "acct-1", "txn-1", k))
	require.NoError(t, err, "a deploy between the commit and the retry must not strand the operator")
	require.Equal(t, first.TowerID, second.TowerID)
}

func TestARetryWithADifferentKeyIsStillRefusedAcrossInstances(t *testing.T) {
	a, b, reg, _ := twoInstances(t)
	k := newTowerKeys(t)

	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	ch, err := a.Challenge(token)
	require.NoError(t, err)
	_, err = a.Enroll(requestFor(t, reg, ch, "acct-1", "txn-1", k))
	require.NoError(t, err)

	attacker := newTowerKeys(t)
	forged := requestFor(t, reg, ch, "acct-1", "txn-1", attacker)
	_, err = b.Enroll(forged)
	require.Error(t, err,
		"a transaction id seen on the wire must not have somebody else's Tower re-issued to your key")
}

func TestAnExpiredChallengeIsReapedFromSharedState(t *testing.T) {
	s := NewMemStore()
	now := time.Now()
	require.NoError(t, s.PutChallenge(Challenge{
		Nonce: "n1", TokenID: "tok-1", Expires: now.Add(-time.Minute),
	}))
	require.NoError(t, s.PutChallenge(Challenge{
		Nonce: "n2", TokenID: "tok-1", Expires: now.Add(time.Minute),
	}))

	require.NoError(t, s.Reap(now))

	_, ok, err := s.TakeChallenge("n1")
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = s.TakeChallenge("n2")
	require.NoError(t, err)
	require.True(t, ok, "a live challenge survives the reap")
}

func TestTakeChallengeIsOneTime(t *testing.T) {
	s := NewMemStore()
	require.NoError(t, s.PutChallenge(Challenge{
		Nonce: "n1", TokenID: "tok-1", Expires: time.Now().Add(time.Minute),
	}))

	_, ok, err := s.TakeChallenge("n1")
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = s.TakeChallenge("n1")
	require.NoError(t, err)
	require.False(t, ok, "taking a nonce removes it, whoever takes it")
}

func TestCommittedOutcomesRoundTrip(t *testing.T) {
	s := NewMemStore()
	_, ok, err := s.Committed("txn-unknown")
	require.NoError(t, err)
	require.False(t, ok)

	rec := Committed{TowerID: "tw-1", KeyHash: "kh", CertDER: []byte{1, 2, 3}}
	require.NoError(t, s.PutCommitted("txn-1", rec))

	got, ok, err := s.Committed("txn-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tw-1", got.TowerID)
	require.Equal(t, []byte{1, 2, 3}, got.CertDER)
}

func TestAStoreOutageRefusesRatherThanIssuingAChallenge(t *testing.T) {
	// A challenge we cannot record is one nobody can answer: the Tower signs it, sends it,
	// and is told it is unknown.
	reg := toweradmit.NewWithStore(toweradmit.Config{}, toweradmit.NewMemStore())
	auth, err := towercert.NewAuthority(towercert.Config{TTL: time.Hour})
	require.NoError(t, err)
	e, err := New(Config{
		Registry: reg, Authority: auth, Policy: &stubPolicy{refuse: map[string]error{}},
		MinVersion: 1, MaxVersion: 2, Store: &brokenEnrollStore{Store: NewMemStore(), down: true},
	})
	require.NoError(t, err)

	token, err := reg.IssueToken("acct-1")
	require.NoError(t, err)
	_, err = e.Challenge(token)
	require.Error(t, err)
}

type brokenEnrollStore struct {
	Store
	down bool
}

func (b *brokenEnrollStore) PutChallenge(c Challenge) error {
	if b.down {
		return ErrUnavailable
	}
	return b.Store.PutChallenge(c)
}
