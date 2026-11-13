package main

// tower_renew_e2e_test.go drives the REAL Tower CLI renewal (towerjoin) against the REAL
// broker routes. Each half had its own tests and both were green while the pair could not
// work: the CLI signed renewals with the operator's account key, the broker (correctly)
// demanded the Tower's identity key, and every joined Tower's lease lapsed 24 hours after
// enrolling. Found by the first outside operator on 2026-11-13. This is the test that
// would have caught it: one process enrolls, renews, and reads the lease Core recorded.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towerjoin"
)

func TestTheTowerCLIRenewsAgainstTheRealBroker(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("ROGER_CONFIG_DIR", filepath.Join(cfg, "roger"))
	b, srv := towerTestBroker(t)
	t.Setenv("ROGER_BROKER", srv.URL)

	// The operator is this machine's CLI key, bound to an account, as `roger tower login`
	// leaves it.
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: client.UserPubHex(), Login: "nora", Email: "nora@rogerai.fm",
		EmailVerifiedAt: time.Now().Unix(),
	}))

	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeJoined)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	require.NoError(t, towerjoin.Register(st, towerjoin.Account{Login: "nora"}))

	adm, ok := towerjoin.LoadAdmission(dir)
	require.True(t, ok)
	before, ok := b.tower.registry.Get(adm.TowerID)
	require.True(t, ok)

	// Far enough into the certificate's life that renewal is due.
	did, err := towerjoin.RenewIfDue(st, adm.NotAfter)
	require.NoError(t, err, "the real broker must accept the Tower's own renewal")
	require.True(t, did)

	after, ok := b.tower.registry.Get(adm.TowerID)
	require.True(t, ok)
	require.True(t, after.LeaseExpires.After(before.LeaseExpires), "the lease Core records must move out")
	require.NotEqual(t, before.CertSerial, after.CertSerial, "Core must have issued a new certificate")
}
