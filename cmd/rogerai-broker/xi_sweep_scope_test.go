package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestXISweepScopedToScenarioPayer pins the "stale-hold sweep finds no orphaned hold" step
// to the scenario's own money: a hold another suite left in a shared Postgres must not
// fail it, while a hold this scenario's consumer left behind still must.
func TestXISweepScopedToScenarioPayer(t *testing.T) {
	db := xiStore(t)
	if c, ok := db.(io.Closer); ok {
		t.Cleanup(func() { _ = c.Close() })
	}
	nonce := xiNonce()
	s := &xiState{t: t, db: db, nonce: nonce, consumerWallet: "u_gh_xisweep_" + nonce}
	foreign := "u_gh_xisweep_foreign_" + nonce
	for _, u := range []string{s.consumerWallet, foreign} {
		_, err := db.AddCredits(u, 10)
		require.NoError(t, err)
	}

	// Another suite's leftover hold on the shared store.
	ok, err := db.HoldFor(foreign, "xisweep-foreign-"+nonce, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.sweepFindsNoOrphan(), "a foreign suite's leftover hold must not fail this scenario's sweep step")

	// This scenario's own orphan still fails it.
	ok, err = db.HoldFor(s.consumerWallet, "xisweep-own-"+nonce, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Error(t, s.sweepFindsNoOrphan(), "the scenario's own orphaned hold must still fail the step")

	// A step with no scenario payer cannot vouch for anything.
	require.Error(t, (&xiState{t: t, db: db}).sweepFindsNoOrphan())
}
