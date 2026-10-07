package main

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

// paircool_test.go - the pair cooldown state (contract §14.2) on both backings: the shared
// store (two instances over one miniredis: what one records the other reads, idempotently)
// and the single-instance fallback (no shared store configured).

func pairBroker(t *testing.T, shared sharedStore, clock *time.Time) *broker {
	t.Helper()
	b := relayBroker(store.NewMem())
	b.shared = shared
	b.nowFn = func() time.Time { return *clock }
	return b
}

func TestPairCooldownSharedAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	vsA, err := newValkeyStore("redis://" + mr.Addr())
	require.NoError(t, err)
	vsB, err := newValkeyStore("redis://" + mr.Addr())
	require.NoError(t, err)
	now := time.Now()
	a, b := pairBroker(t, vsA, &now), pairBroker(t, vsB, &now)

	until := a.coolPair("n1", "m", "alice", 30)
	require.WithinDuration(t, now.Add(30*time.Second), until, time.Millisecond)
	got, ok := pairUntil(b.payerCooling("alice"), "n1", "m")
	require.True(t, ok, "instance B reads the pair instance A cooled")
	require.WithinDuration(t, until, got, time.Millisecond)
	_, ok = pairUntil(b.payerCooling("bob"), "n1", "m")
	require.False(t, ok, "another payer's pair is untouched")
	_, cooling := b.coolingUntil("n1")
	require.False(t, cooling, "one payer never cools the station")

	// Extend, never stack, never shorten - from either instance.
	require.Equal(t, until, b.coolPair("n1", "m", "alice", 5), "a shorter hint keeps the later expiry")
	longer := a.coolPair("n1", "m", "alice", 60)
	require.True(t, longer.After(until))

	// The payer window is one set: a repeat is the same payer; the third DISTINCT one cools
	// the station everywhere it is read.
	b.coolPair("n1", "m", "alice", 30)
	b.coolPair("n1", "m", "bob", 30)
	_, cooling = a.coolingUntil("n1")
	require.False(t, cooling, "two distinct payers are below the threshold")
	a.coolPair("n1", "m", "carol", 30)
	_, cooling = a.coolingUntil("n1")
	require.True(t, cooling, "the third distinct payer cools the station")

	// Lapsed pairs read as none.
	now = now.Add(2 * cooldownMax())
	require.Empty(t, b.payerCooling("alice"))
}

func TestPairCooldownWindowAndOutage(t *testing.T) {
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	require.NoError(t, err)
	now := time.Now()
	b := pairBroker(t, vs, &now)
	for _, p := range []string{"alice", "bob"} {
		b.coolPair("n1", "m", p, 10)
		now = now.Add(61 * time.Second) // each one leaves the 60 s window before the next
	}
	b.coolPair("n1", "m", "carol", 10)
	_, cooling := b.coolingUntil("n1")
	require.False(t, cooling, "payers outside the window do not add up")

	// An outage fails open: nothing recorded, nothing read, no station-wide cooldown.
	mr.SetError("ERR down")
	b.coolPair("n2", "m", "alice", 30)
	require.Empty(t, b.payerCooling("alice"))
	_, cooling = b.coolingUntil("n2")
	require.False(t, cooling)
	mr.SetError("")
	require.Empty(t, b.payerCooling("alice"), "the outage left no pair behind, locally or shared")
}

func TestPairCooldownSingleInstanceFallback(t *testing.T) {
	now := time.Now()
	b := pairBroker(t, newMemStore(), &now)
	until := b.coolPair("n1", "m", "alice", 20)
	got, ok := pairUntil(b.payerCooling("alice"), "n1", "m")
	require.True(t, ok)
	require.Equal(t, until, got)
	require.Equal(t, until, b.coolPair("n1", "m", "alice", 1), "extend, never shorten")
	b.coolPair("n1", "m", "bob", 20)
	b.coolPair("n1", "m", "carol", 20)
	_, cooling := b.coolingUntil("n1")
	require.True(t, cooling, "the threshold works without a shared store")
	now = now.Add(time.Hour)
	require.Empty(t, b.payerCooling("alice"), "lapsed pairs are dropped")
	require.Empty(t, b.payerCooling(""), "no payer, no pairs")
	b.coolPair("n9", "m", "", 5) // no payer identity: today's station-wide rule
	_, cooling = b.coolingUntil("n9")
	require.True(t, cooling)
}

func TestTPMShareKnob(t *testing.T) {
	require.False(t, overTPMShare(0, 1e9), "no declared tpm, no guard")
	require.True(t, overTPMShare(60000, 30001))
	require.False(t, overTPMShare(60000, 30000))
	t.Setenv("ROGERAI_TPM_REQUEST_SHARE", "25%")
	require.True(t, overTPMShare(60000, 15001))
	for _, v := range []string{"0", "-5", "150", "x"} {
		t.Setenv("ROGERAI_TPM_REQUEST_SHARE", v)
		require.Equal(t, 0.5, tpmRequestShare(), "%q falls back to 50%%", v)
	}
}
