package store

// idem_parity_test.go: the Idempotency-Key claim (features/routing/idempotency.feature, contract
// §14.B2) on BOTH backends - in-memory and a real Postgres (skipped without
// ROGERAI_TEST_DATABASE_URL). The claim is the money guard: one (payer, key) inside its window is
// claimed exactly once, so a retry can never start a second job, hold or settle.

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdempotencyClaimParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			pfx := fmt.Sprintf("idem-%s-", name)
			c := IdemClaim{Payer: pfx + "u1", Key: "k-1", Fingerprint: "fp-1", RequestID: "r-1", State: IdemInFlight, Deadline: 2_000, Created: 1_000}

			// First claim wins and reads back.
			got, claimed, err := s.ClaimIdempotency(c, 0, 1000)
			require.NoError(t, err)
			require.True(t, claimed)
			require.Equal(t, c, got)

			// A second claim inside the window does not win and returns the live claim.
			again := c
			again.RequestID, again.Fingerprint = "r-2", "fp-2"
			got, claimed, err = s.ClaimIdempotency(again, 500, 1000)
			require.NoError(t, err)
			require.False(t, claimed)
			require.Equal(t, c, got)

			// Finishing moves the state; only the holder (its request id) can finish it.
			require.NoError(t, s.FinishIdempotency(c.Payer, c.Key, "r-other", IdemDone))
			got, claimed, _ = s.ClaimIdempotency(again, 500, 1000)
			require.False(t, claimed)
			require.Equal(t, IdemInFlight, got.State, "a request that does not hold the claim cannot finish it")
			require.NoError(t, s.FinishIdempotency(c.Payer, c.Key, c.RequestID, IdemDone))
			got, _, _ = s.ClaimIdempotency(again, 500, 1000)
			require.Equal(t, IdemDone, got.State)

			// Another payer's identical key is unrelated.
			other := c
			other.Payer = pfx + "u2"
			_, claimed, err = s.ClaimIdempotency(other, 0, 1000)
			require.NoError(t, err)
			require.True(t, claimed)

			// Past the window (created before since) the key is claimed afresh.
			fresh := again
			fresh.Created = 5_000
			got, claimed, err = s.ClaimIdempotency(fresh, 1_001, 1000)
			require.NoError(t, err)
			require.True(t, claimed)
			require.Equal(t, fresh, got)

			// At most max live keys per payer: the oldest is evicted, the new claim is kept.
			for i := 0; i < 3; i++ {
				k := IdemClaim{Payer: pfx + "u3", Key: fmt.Sprintf("b-%d", i), Fingerprint: "f", RequestID: fmt.Sprintf("rb-%d", i), State: IdemDone, Created: int64(10 + i)}
				_, claimed, err := s.ClaimIdempotency(k, 0, 2)
				require.NoError(t, err)
				require.True(t, claimed)
			}
			_, claimed, err = s.ClaimIdempotency(IdemClaim{Payer: pfx + "u3", Key: "b-0", Fingerprint: "f", RequestID: "rb-x", State: IdemInFlight, Created: 20}, 0, 2)
			require.NoError(t, err)
			require.True(t, claimed, "the oldest key fell out and is claimable again")
			got, claimed, _ = s.ClaimIdempotency(IdemClaim{Payer: pfx + "u3", Key: "b-2", Fingerprint: "f", RequestID: "rb-y", Created: 21}, 0, 2)
			require.False(t, claimed, "the newest key stays live")
			require.Equal(t, "rb-2", got.RequestID)
		})
	}
}

// Two concurrent first claims of one key: exactly one wins (the race the 409 relies on).
func TestIdempotencyClaimRace(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			var wg sync.WaitGroup
			wins := make(chan string, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					c := IdemClaim{Payer: "idem-race-" + name, Key: "k", Fingerprint: "f", RequestID: fmt.Sprintf("r-%d", i), State: IdemInFlight, Created: 1}
					if _, claimed, err := s.ClaimIdempotency(c, 0, 1000); err == nil && claimed {
						wins <- c.RequestID
					}
				}(i)
			}
			wg.Wait()
			close(wins)
			require.Len(t, wins, 1)
		})
	}
}

// slice-6 review 2026-10-06 (B1): a request that wrote nothing gives its claim back, so a retry
// with the same key is served fresh. Only the holder can release it.
func TestIdempotencyReleaseParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			pfx := fmt.Sprintf("idrel-%s-", name)
			c := IdemClaim{Payer: pfx + "u1", Key: "k", Fingerprint: "fp", RequestID: "r-1", State: IdemInFlight, Deadline: 2_000, Created: 1_000}
			_, claimed, err := s.ClaimIdempotency(c, 0, 1000)
			require.NoError(t, err)
			require.True(t, claimed)

			require.NoError(t, s.ReleaseIdempotency(c.Payer, c.Key, "r-other"))
			again := c
			again.RequestID = "r-2"
			got, claimed, err := s.ClaimIdempotency(again, 500, 1000)
			require.NoError(t, err)
			require.False(t, claimed, "a request that does not hold the claim cannot release it")
			require.Equal(t, "r-1", got.RequestID)

			require.NoError(t, s.ReleaseIdempotency(c.Payer, c.Key, "r-1"))
			got, claimed, err = s.ClaimIdempotency(again, 500, 1000)
			require.NoError(t, err)
			require.True(t, claimed, "after the holder released it, the retry claims the key afresh")
			require.Equal(t, "r-2", got.RequestID)
		})
	}
}

// slice-6 review 2026-10-06 (M1): the per-payer bound never evicts an in-flight claim, or a
// retry of that request would win a second claim and run a second job and hold.
func TestIdempotencyEvictionSparesInflightParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			payer := fmt.Sprintf("idev-%s-u1", name)
			claim := func(key string, created int64, state string) {
				c := IdemClaim{Payer: payer, Key: key, Fingerprint: "fp", RequestID: "r-" + key, State: IdemInFlight, Deadline: created + 1000, Created: created}
				_, ok, err := s.ClaimIdempotency(c, 0, 2)
				require.NoError(t, err)
				require.True(t, ok)
				if state != IdemInFlight {
					require.NoError(t, s.FinishIdempotency(payer, key, "r-"+key, state))
				}
			}
			claim("old-inflight", 1, IdemInFlight)
			claim("old-done", 2, IdemDone)
			claim("new-1", 3, IdemInFlight)
			claim("new-2", 4, IdemInFlight)

			held := func(key string) bool {
				c := IdemClaim{Payer: payer, Key: key, Fingerprint: "fp", RequestID: "probe", State: IdemInFlight, Deadline: 9, Created: 9}
				cur, ok, err := s.ClaimIdempotency(c, 0, 1000)
				require.NoError(t, err)
				return !ok && cur.RequestID == "r-"+key
			}
			require.True(t, held("old-inflight"), "the oldest claim is in flight: it survives the bound")
			require.False(t, held("old-done"), "a finished claim is what the bound evicts")
		})
	}
}

// slice-6 audit 2026-10-06: an in-flight claim past its deadline (its instance died) is taken
// over by the next claim, so the key does not answer 409 for the whole window; inside its
// deadline it is not.
func TestIdempotencyDeadInflightTakeoverParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			payer := fmt.Sprintf("iddead-%s-u1", name)
			c := IdemClaim{Payer: payer, Key: "k", Fingerprint: "fp", RequestID: "r-1", State: IdemInFlight, Deadline: 2_000, Created: 1_000}
			_, ok, err := s.ClaimIdempotency(c, 0, 1000)
			require.NoError(t, err)
			require.True(t, ok)

			early := c
			early.RequestID, early.Created, early.Deadline = "r-early", 1_500, 3_000
			got, ok, err := s.ClaimIdempotency(early, 0, 1000)
			require.NoError(t, err)
			require.False(t, ok, "inside its deadline the claim holds")
			require.Equal(t, "r-1", got.RequestID)

			late := c
			late.RequestID, late.Created, late.Deadline = "r-late", 2_500, 4_000
			got, ok, err = s.ClaimIdempotency(late, 0, 1000)
			require.NoError(t, err)
			require.True(t, ok, "past its deadline an in-flight claim is taken over")
			require.Equal(t, "r-late", got.RequestID)

			// A finished claim is never taken over before the window ends, deadline or not.
			require.NoError(t, s.FinishIdempotency(payer, "k", "r-late", IdemDone))
			after := c
			after.RequestID, after.Created = "r-after", 9_000
			got, ok, err = s.ClaimIdempotency(after, 0, 1000)
			require.NoError(t, err)
			require.False(t, ok)
			require.Equal(t, "r-late", got.RequestID)
		})
	}
}

// slice-6 audit 2026-10-06: a finished claim past the window is swept on the next claim, not
// kept until the per-payer count bound reaches it.
func TestIdempotencyExpiredSweepParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			payer := fmt.Sprintf("idsweep-%s-u1", name)
			old := IdemClaim{Payer: payer, Key: "old", Fingerprint: "fp", RequestID: "r-old", State: IdemInFlight, Deadline: 200, Created: 100}
			_, ok, err := s.ClaimIdempotency(old, 0, 1000)
			require.NoError(t, err)
			require.True(t, ok)
			require.NoError(t, s.FinishIdempotency(payer, "old", "r-old", IdemDone))

			fresh := IdemClaim{Payer: payer, Key: "new", Fingerprint: "fp", RequestID: "r-new", State: IdemInFlight, Deadline: 9_000, Created: 5_000}
			_, ok, err = s.ClaimIdempotency(fresh, 1_000, 1000) // the window starts at 1000
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, 1, idemRowsOf(t, s, payer), "the expired finished claim was swept")
		})
	}
}

// idemRowsOf counts the claim rows a payer holds, on either backend.
func idemRowsOf(t *testing.T, s Store, payer string) int {
	t.Helper()
	switch x := s.(type) {
	case *Mem:
		x.mu.Lock()
		defer x.mu.Unlock()
		n := 0
		for _, c := range x.idemClaims {
			if c.Payer == payer {
				n++
			}
		}
		return n
	case *Postgres:
		var n int
		require.NoError(t, x.DB().QueryRow(`SELECT count(*) FROM rogerai.idempotency_claims WHERE payer=$1`, payer).Scan(&n))
		return n
	}
	t.Fatalf("unknown store %T", s)
	return 0
}
