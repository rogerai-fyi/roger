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
