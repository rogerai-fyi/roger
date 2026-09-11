package store

// rc_prune_test.go covers PruneRCSessions on BOTH backends (Mem always, the real Postgres
// when ROGERAI_TEST_DATABASE_URL is set). Prune is the roster's self-clean: an "ended"
// (revoked) session must actually DISAPPEAR rather than linger, and a session whose host has
// been silent since before the idle cutoff (RCIdleGC) ages out. What it must NEVER take is a
// live session, a recently-offline one, or another owner's row - so those are asserted too.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPruneRCSessionsParity(t *testing.T) {
	const (
		now    = int64(1_800_000_000)
		cutoff = now - 7*24*3600 // RCIdleGC ago
	)
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			seed := func(id, wallet string, lastSeen int64, revoked bool) {
				s := rcSeed(id, wallet, "code-"+id, "host-"+id)
				s.LastHostSeen = lastSeen
				s.Revoked = revoked
				require.NoError(t, db.CreateRCSession(s))
				require.NoError(t, db.PutRCAttachToken(RCAttachToken{
					Hash: "attach-" + id, SessionID: id, DeviceLabel: "web (Chrome)", CreatedAt: now,
				}))
			}
			// live: polling right now. offline: silent for a minute but well inside the GC
			// window. ended: revoked though its host is current. stale: silent since before
			// the cutoff. other: another owner's ENDED session, which is not ours to reap.
			seed("rcs_live", "u_gh_1", now, false)
			seed("rcs_offline", "u_gh_1", now-60, false)
			seed("rcs_ended", "u_gh_1", now, true)
			seed("rcs_stale", "u_gh_1", cutoff-1, false)
			seed("rcs_other", "u_gh_2", now, true)

			n, err := db.PruneRCSessions("u_gh_1", cutoff)
			require.NoError(t, err)
			require.Equal(t, 2, n, "prune takes exactly the ended + the stale row")

			for _, id := range []string{"rcs_ended", "rcs_stale"} {
				_, ok, err := db.RCSessionByID(id)
				require.NoError(t, err)
				require.False(t, ok, "%s must be hard-deleted, not left as a tombstone", id)
				_, ok, err = db.RCSessionByCodeHash("code-" + id)
				require.NoError(t, err)
				require.False(t, ok, "%s's code hash must stop resolving", id)
				_, ok, err = db.RCAttachTokenByHash("attach-" + id)
				require.NoError(t, err)
				require.False(t, ok, "%s's attach token must go with the session", id)
			}
			for _, id := range []string{"rcs_live", "rcs_offline", "rcs_other"} {
				_, ok, err := db.RCSessionByID(id)
				require.NoError(t, err)
				require.True(t, ok, "%s must survive the prune", id)
				_, ok, err = db.RCAttachTokenByHash("attach-" + id)
				require.NoError(t, err)
				require.True(t, ok, "%s's attach token must survive with it", id)
			}
			roster, err := db.RCSessionsByOwner("u_gh_1")
			require.NoError(t, err)
			require.Len(t, roster, 2, "the owner's roster is down to the two living rows")

			// Idempotent: a second prune finds nothing left to take.
			n, err = db.PruneRCSessions("u_gh_1", cutoff)
			require.NoError(t, err)
			require.Zero(t, n)

			// An owner with no rows at all prunes cleanly (the roster-list path runs this on
			// every load, including for an account that never enabled remote control).
			n, err = db.PruneRCSessions("u_gh_nobody", cutoff)
			require.NoError(t, err)
			require.Zero(t, n)
		})
	}
}
