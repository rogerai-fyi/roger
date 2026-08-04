package toweradmit

// The store contract and the lost-CAS paths, driven deterministically.
//
// The concurrency tests elsewhere reach these branches only when the scheduler cooperates,
// which makes them both flaky as coverage and weak as evidence. Here the losing write is
// arranged rather than raced: something else moves the Tower between our read and our
// write, exactly as another instance would.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemStoreTokenLifecycle(t *testing.T) {
	s := NewMemStore()

	_, ok, err := s.GetToken("absent")
	require.NoError(t, err)
	require.False(t, ok, "an absent token is a miss, not an error")

	spent, err := s.ConsumeToken("absent")
	require.NoError(t, err)
	require.False(t, spent, "consuming what is not there reports no write")

	tok := Token{ID: "t1", Owner: "acct-1", Expires: time.Now().Add(time.Hour)}
	require.NoError(t, s.PutToken(tok))

	got, ok, err := s.GetToken("t1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "acct-1", got.Owner)

	spent, err = s.ConsumeToken("t1")
	require.NoError(t, err)
	require.True(t, spent)

	spent, err = s.ConsumeToken("t1")
	require.NoError(t, err)
	require.False(t, spent, "one token, one redemption")
}

func TestMemStoreReapLeavesLiveTokens(t *testing.T) {
	s := NewMemStore()
	now := time.Now()
	require.NoError(t, s.PutToken(Token{ID: "live", Expires: now.Add(time.Hour)}))
	require.NoError(t, s.PutToken(Token{ID: "dead", Expires: now.Add(-time.Hour)}))

	require.NoError(t, s.ReapTokens(now))

	_, ok, err := s.GetToken("dead")
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = s.GetToken("live")
	require.NoError(t, err)
	require.True(t, ok, "reaping removes what cannot be redeemed and nothing else")
}

func TestMemStoreTowerIndexesAndCAS(t *testing.T) {
	s := NewMemStore()

	_, ok, err := s.TowerByID("absent")
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = s.TowerByKey("absent")
	require.NoError(t, err)
	require.False(t, ok)

	won, err := s.CASTower(Tower{ID: "absent", Rev: 1})
	require.NoError(t, err)
	require.False(t, won, "CAS against a record that does not exist reports no write")

	tw := Tower{ID: "tw1", Owner: "acct-1", KeyHash: "key-1", State: StateQuarantine,
		EnrolledAt: time.Now(), LeaseExpires: time.Now().Add(time.Hour)}
	require.NoError(t, s.PutTower(tw))

	byKey, ok, err := s.TowerByKey("key-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tw1", byKey.ID)

	owned, err := s.TowersByOwner("acct-1")
	require.NoError(t, err)
	require.Len(t, owned, 1)

	none, err := s.TowersByOwner("acct-nobody")
	require.NoError(t, err)
	require.Empty(t, none)
}

// moveTowerBehindTheCaller simulates another instance changing a Tower between our read
// and our write.
func moveTowerBehindTheCaller(t *testing.T, s Store, id string, to State) {
	t.Helper()
	cur, ok, err := s.TowerByID(id)
	require.NoError(t, err)
	require.True(t, ok)
	cur.State = to
	won, err := s.CASTower(cur)
	require.NoError(t, err)
	require.True(t, won)
}

// interposingStore runs a hook after each TowerByID, which is precisely the window a
// concurrent writer occupies.
type interposingStore struct {
	Store
	after func()
	once  bool
}

func (i *interposingStore) TowerByID(id string) (Tower, bool, error) {
	tw, ok, err := i.Store.TowerByID(id)
	if i.after != nil && !i.once {
		i.once = true
		i.after()
	}
	return tw, ok, err
}

func TestTransitionRefusesToOverwriteAConcurrentDecision(t *testing.T) {
	// The decision being overwritten might be a revocation, which is why losing has to be
	// reported rather than retried silently.
	base := NewMemStore()
	inter := &interposingStore{Store: base}
	r := NewWithStore(Config{}, inter)

	tok, err := r.IssueToken("acct-1")
	require.NoError(t, err)
	tw, err := r.Enroll(tok, "key-1")
	require.NoError(t, err)

	inter.after = func() { moveTowerBehindTheCaller(t, base, tw.ID, StateRevoked) }
	err = r.Transition(tw.ID, StateActive)
	require.Error(t, err, "a write against a superseded read must be refused")

	got, _ := r.Get(tw.ID)
	require.Equal(t, StateRevoked, got.State, "the decision made first stands")
}

func TestRenewRefusesToOverwriteAConcurrentDecision(t *testing.T) {
	base := NewMemStore()
	inter := &interposingStore{Store: base}
	r := NewWithStore(Config{LeaseTTL: time.Hour}, inter)

	tok, _ := r.IssueToken("acct-1")
	tw, err := r.Enroll(tok, "key-1")
	require.NoError(t, err)

	inter.after = func() { moveTowerBehindTheCaller(t, base, tw.ID, StateRevoked) }
	require.Error(t, r.Renew(tw.ID), "a revoked Tower must not have its lease extended by a stale renew")
}

func TestExpireRefusesToOverwriteAConcurrentDecision(t *testing.T) {
	base := NewMemStore()
	inter := &interposingStore{Store: base}
	r := NewWithStore(Config{LeaseTTL: time.Millisecond}, inter)

	tok, _ := r.IssueToken("acct-1")
	tw, err := r.Enroll(tok, "key-1")
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	inter.after = func() { moveTowerBehindTheCaller(t, base, tw.ID, StateRevoked) }
	require.Error(t, r.Expire(tw.ID))
}

func TestRecordClaimIgnoresAnUnknownTowerAndAnUnparseableState(t *testing.T) {
	r, _ := newRegistry(t, Config{})
	r.RecordClaim("no-such-tower", StateActive) // must not panic

	tok, _ := r.IssueToken("acct-1")
	tw, err := r.Enroll(tok, "key-1")
	require.NoError(t, err)

	// A value outside the enum is unparseable input, not evidence. Counting it would let
	// noise accumulate into a penalty nobody earned.
	r.RecordClaim(tw.ID, State("not-a-state"))
	got, _ := r.Get(tw.ID)
	require.Zero(t, got.FalseClaims)
}

func TestEnrollRefusesWhenTheTowerCannotBeRecorded(t *testing.T) {
	// Admission is one transaction, so a Tower that cannot be written takes the token
	// consumption down with it. The operator's next attempt uses the SAME token, rather
	// than needing a fresh one for a failure that was ours.
	s := &admitFailingStore{Store: NewMemStore()}
	r := NewWithStore(Config{}, s)

	tok, err := r.IssueToken("acct-1")
	require.NoError(t, err)

	s.fail = true
	_, err = r.Enroll(tok, "key-1")
	require.ErrorIs(t, err, ErrUnavailable)

	_, ok, err := s.TowerByKey("key-1")
	require.NoError(t, err)
	require.False(t, ok, "nothing partial is left behind for a later attempt to adopt as real")

	_, ok, err = s.GetToken(tok)
	require.NoError(t, err)
	require.True(t, ok, "and the token rolls back with the Tower")

	s.fail = false
	tw, err := r.Enroll(tok, "key-1")
	require.NoError(t, err, "so the retry succeeds on the same token")
	require.NotEmpty(t, tw.ID)
}

type admitFailingStore struct {
	Store
	fail bool
}

func (a *admitFailingStore) Admit(tokenID string, tw Tower) (bool, error) {
	if a.fail {
		return false, ErrUnavailable
	}
	return a.Store.Admit(tokenID, tw)
}

func TestIssueTokenRefusesWhenItCannotBeRecorded(t *testing.T) {
	// Handing an operator a token we did not record is handing them a guaranteed failure
	// later instead of an error now.
	s := &tokenPutFailingStore{Store: NewMemStore()}
	r := NewWithStore(Config{}, s)
	_, err := r.IssueToken("acct-1")
	require.ErrorIs(t, err, ErrUnavailable)
}

type tokenPutFailingStore struct {
	Store
}

func (t *tokenPutFailingStore) PutToken(Token) error { return ErrUnavailable }

func TestIssueTokenRequiresAnAccount(t *testing.T) {
	r, _ := newRegistry(t, Config{})
	_, err := r.IssueToken("")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnavailable, "a bad request is not an outage")
}

func TestReapFailureStopsTokenIssuance(t *testing.T) {
	s := &reapFailingStore{Store: NewMemStore()}
	r := NewWithStore(Config{}, s)
	_, err := r.IssueToken("acct-1")
	require.ErrorIs(t, err, ErrUnavailable)
}

type reapFailingStore struct{ Store }

func (r *reapFailingStore) ReapTokens(time.Time) error { return ErrUnavailable }
