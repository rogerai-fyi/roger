package edge

// The replay cache (noncecache.go, audit 2026-10-13): one member cannot spend the whole cache and lock
// the others out, a nonce is one node's, and expired nonces leave in expiry order.

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOneNodeCannotLockTheOthersOut(t *testing.T) {
	c := newNonceCache()
	now := int64(1_000)
	for i := 0; i < maxNoncesPerNode; i++ {
		require.True(t, c.admit("n_flood", fmt.Sprintf("%032d", i), now+300, now))
	}
	require.False(t, c.admit("n_flood", fmt.Sprintf("%032d", maxNoncesPerNode), now+300, now), "the flooding node stops at its quota")
	require.True(t, c.admit("n_phone", fmt.Sprintf("%032d", 1), now+300, now), "every other node is untouched")
}

func TestANonceIsSpentOncePerNode(t *testing.T) {
	c := newNonceCache()
	n := "00112233445566778899aabbccddeeff"
	require.True(t, c.admit("n_a", n, 400, 100))
	require.False(t, c.admit("n_a", n, 400, 100), "a replay")
	require.True(t, c.admit("n_b", n, 400, 100), "another node's nonce is its own")
	require.False(t, c.admit("n_a", "short", 400, 100), "too short to be random")
}

func TestExpiredNoncesLeaveAndFreeTheQuota(t *testing.T) {
	c := newNonceCache()
	for i := 0; i < maxNoncesPerNode; i++ {
		require.True(t, c.admit("n_a", fmt.Sprintf("%032d", i), int64(100+i), 50))
	}
	require.False(t, c.admit("n_a", fmt.Sprintf("%032d", 9999), 2000, 50))
	// At 150, the first fifty have expired, and only they.
	require.True(t, c.admit("n_a", fmt.Sprintf("%032d", 9999), 2000, 150))
	require.Len(t, c.seen, maxNoncesPerNode-50+1)
	require.False(t, c.admit("n_a", fmt.Sprintf("%032d", 60), 2000, 150), "a nonce still live is still spent")
}
