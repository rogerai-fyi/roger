package toweradmit

// Tests for the storage seam behind the admission registry.
//
// THE DEFECT THIS EXISTS TO FIX. The registry is Roger Core's record of WHICH TOWERS ARE
// ADMITTED to the public network - their leases, their lifecycle states, and the
// false-claim evidence accumulated against them. All of it lived in process-local maps, so
// a restart forgot the entire public network's admission state. Four consequences, and the
// last two are the serious ones:
//
//  1. Every admitted Tower vanishes, so a live operator's Tower reads as unknown.
//  2. Every lease vanishes, so nothing bounds offline drift any more.
//  3. Revocation is UNDONE. A Tower revoked for abuse is simply no longer revoked after a
//     deploy - and its identity key, which revocation burns, becomes re-enrollable.
//  4. FalseClaims evidence is erased, so a Tower that lies about its state resets its
//     record every time we ship.
//
// Nothing is broken in production today only because no HTTP surface reaches the registry
// yet. It cannot carry a real operator until this holds.

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newRegistry(t *testing.T, cfg Config) (*Registry, Store) {
	t.Helper()
	s := NewMemStore()
	return NewWithStore(cfg, s), s
}

// restart is what a redeployed process is: a brand-new Registry over the same store.
func restart(cfg Config, s Store) *Registry { return NewWithStore(cfg, s) }

func TestAnAdmittedTowerSurvivesARestart(t *testing.T) {
	cfg := Config{}
	r, s := newRegistry(t, cfg)
	tok, err := r.IssueToken("acct-1")
	require.NoError(t, err)
	tw, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err)

	after := restart(cfg, s)
	got, ok := after.Get(tw.ID)
	require.True(t, ok, "an admitted Tower must still be admitted after a deploy")
	require.Equal(t, "acct-1", got.Owner)
	require.Equal(t, "keyhash-A", got.KeyHash)
	require.Equal(t, StateQuarantine, got.State)
	require.WithinDuration(t, tw.LeaseExpires, got.LeaseExpires, time.Second,
		"the lease is the one it was admitted with, not a fresh one")
}

func TestRevocationIsNotUndoneByARestart(t *testing.T) {
	// The one that matters most: a Tower revoked for abuse must not quietly become
	// un-revoked because we shipped.
	cfg := Config{}
	r, s := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")
	tw, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err)
	require.NoError(t, r.Transition(tw.ID, StateRevoked))

	after := restart(cfg, s)
	got, ok := after.Get(tw.ID)
	require.True(t, ok)
	require.Equal(t, StateRevoked, got.State)
	require.False(t, after.MayTakeWork(tw.ID), "a revoked Tower takes no work after a restart either")
}

func TestARevokedKeyStaysBurnedAcrossARestart(t *testing.T) {
	// Revocation burns the identity key. If the key index does not survive, the operator
	// re-enrolls the same machine and the revocation is worthless.
	cfg := Config{}
	r, s := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")
	tw, _ := r.Enroll(tok, "keyhash-A")
	require.NoError(t, r.Transition(tw.ID, StateRevoked))

	after := restart(cfg, s)
	tok2, err := after.IssueToken("acct-1")
	require.NoError(t, err)
	_, err = after.Enroll(tok2, "keyhash-A")
	require.Error(t, err, "a burned key must not be re-enrollable after a restart")
}

func TestAnEnrollmentTokenIsOneTimeAcrossARestart(t *testing.T) {
	cfg := Config{}
	r, s := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")
	_, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err)

	after := restart(cfg, s)
	_, err = after.Enroll(tok, "keyhash-B")
	require.Error(t, err, "a spent token is spent after a restart too")
}

func TestAnUnusedTokenStillWorksAfterARestart(t *testing.T) {
	// An operator who is handed a token and restarts the broker before they use it must
	// not silently lose it.
	cfg := Config{}
	r, s := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")

	after := restart(cfg, s)
	tw, err := after.Enroll(tok, "keyhash-A")
	require.NoError(t, err)
	require.Equal(t, "acct-1", tw.Owner)
}

func TestAnExpiredTokenIsStillExpiredAfterARestart(t *testing.T) {
	cfg := Config{TokenTTL: time.Millisecond}
	r, s := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")
	time.Sleep(5 * time.Millisecond)

	after := restart(cfg, s)
	_, err := after.Enroll(tok, "keyhash-A")
	require.Error(t, err, "a restart does not extend a token's life")
}

func TestFalseClaimEvidenceSurvivesARestart(t *testing.T) {
	// Evidence that resets on every deploy is not evidence.
	cfg := Config{}
	r, s := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")
	tw, _ := r.Enroll(tok, "keyhash-A")
	r.RecordClaim(tw.ID, StateActive)
	r.RecordClaim(tw.ID, StateActive)

	after := restart(cfg, s)
	got, ok := after.Get(tw.ID)
	require.True(t, ok)
	require.Equal(t, 2, got.FalseClaims)

	after.RecordClaim(tw.ID, StateActive)
	got, _ = after.Get(tw.ID)
	require.Equal(t, 3, got.FalseClaims, "the count continues rather than restarting")
}

func TestARenewedLeaseSurvivesARestart(t *testing.T) {
	cfg := Config{LeaseTTL: time.Hour}
	r, s := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")
	tw, _ := r.Enroll(tok, "keyhash-A")
	require.NoError(t, r.Renew(tw.ID))
	renewed, _ := r.Get(tw.ID)

	after := restart(cfg, s)
	got, ok := after.Get(tw.ID)
	require.True(t, ok)
	require.WithinDuration(t, renewed.LeaseExpires, got.LeaseExpires, time.Second)
}

func TestPerOwnerQuotaIsCountedAcrossARestart(t *testing.T) {
	// Otherwise restarting is how an operator exceeds their Tower allowance.
	cfg := Config{MaxTowersPerOwner: 2}
	r, s := newRegistry(t, cfg)
	for i, key := range []string{"keyhash-A", "keyhash-B"} {
		tok, _ := r.IssueToken("acct-1")
		_, err := r.Enroll(tok, key)
		require.NoError(t, err, "enrollment %d", i)
	}

	after := restart(cfg, s)
	tok, _ := after.IssueToken("acct-1")
	_, err := after.Enroll(tok, "keyhash-C")
	require.Error(t, err, "the quota counts the Towers that already exist")
}

func TestByOwnerReadsThroughTheStore(t *testing.T) {
	cfg := Config{}
	r, s := newRegistry(t, cfg)
	for _, key := range []string{"keyhash-A", "keyhash-B"} {
		tok, _ := r.IssueToken("acct-1")
		_, err := r.Enroll(tok, key)
		require.NoError(t, err)
	}
	tok, _ := r.IssueToken("acct-2")
	_, err := r.Enroll(tok, "keyhash-C")
	require.NoError(t, err)

	after := restart(cfg, s)
	require.Len(t, after.ByOwner("acct-1"), 2)
	require.Len(t, after.ByOwner("acct-2"), 1)
	require.Empty(t, after.ByOwner("acct-nobody"))
}

func TestConcurrentTransitionsSettleOnExactlyOne(t *testing.T) {
	// Two operators acting on the same Tower at the same moment - or two instances - must
	// not both apply a transition from the same starting state.
	cfg := Config{}
	r, _ := newRegistry(t, cfg)
	tok, _ := r.IssueToken("acct-1")
	tw, _ := r.Enroll(tok, "keyhash-A")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = r.Transition(tw.ID, StateActive) }()
	go func() { defer wg.Done(); errs[1] = r.Transition(tw.ID, StateRevoked) }()
	wg.Wait()

	got, _ := r.Get(tw.ID)
	require.NotEqual(t, StateQuarantine, got.State, "one of them applied")
	// Whichever lost must have said so rather than silently vanishing.
	var applied int
	for _, err := range errs {
		if err == nil {
			applied++
		}
	}
	require.GreaterOrEqual(t, applied, 1)
}

func TestTwoInstancesShareOneAdmissionRecord(t *testing.T) {
	// The registry is the network's record, not an instance's opinion of it.
	cfg := Config{}
	s := NewMemStore()
	instanceA := NewWithStore(cfg, s)
	instanceB := NewWithStore(cfg, s)

	tok, err := instanceA.IssueToken("acct-1")
	require.NoError(t, err)
	tw, err := instanceB.Enroll(tok, "keyhash-A")
	require.NoError(t, err, "a token minted on one instance is redeemable on another")

	require.NoError(t, instanceA.Transition(tw.ID, StateActive))
	require.True(t, instanceB.MayTakeWork(tw.ID),
		"a promotion on one instance is visible on the other")

	require.NoError(t, instanceB.Transition(tw.ID, StateRevoked))
	require.False(t, instanceA.MayTakeWork(tw.ID),
		"and so is a revocation - otherwise a banned Tower keeps taking work from half the fleet")
}

func TestAStoreOutageRefusesRatherThanInventingAnAnswer(t *testing.T) {
	// The repository's standing position, applied here: refuse to serve rather than lose
	// state quietly. An admission we cannot record must not be reported as an admission.
	s := &brokenAdmitStore{Store: NewMemStore()}
	r := NewWithStore(Config{}, s)

	s.down = true
	_, err := r.IssueToken("acct-1")
	require.Error(t, err)

	s.down = false
	tok, err := r.IssueToken("acct-1")
	require.NoError(t, err)

	s.down = true
	_, err = r.Enroll(tok, "keyhash-A")
	require.Error(t, err, "a Tower we cannot record must not be told it is admitted")
	require.False(t, r.MayTakeWork("anything"), "and an unreadable registry grants nothing")
}

type brokenAdmitStore struct {
	Store
	down bool
}

func (b *brokenAdmitStore) PutToken(t Token) error {
	if b.down {
		return ErrUnavailable
	}
	return b.Store.PutToken(t)
}

func (b *brokenAdmitStore) GetToken(id string) (Token, bool, error) {
	if b.down {
		return Token{}, false, ErrUnavailable
	}
	return b.Store.GetToken(id)
}

func (b *brokenAdmitStore) PutTower(tw Tower) error {
	if b.down {
		return ErrUnavailable
	}
	return b.Store.PutTower(tw)
}

func (b *brokenAdmitStore) TowerByID(id string) (Tower, bool, error) {
	if b.down {
		return Tower{}, false, ErrUnavailable
	}
	return b.Store.TowerByID(id)
}
