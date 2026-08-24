package localplane

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
)

func TestReplayGuardRefusesASecondUseOfANonce(t *testing.T) {
	now := time.Now()
	g := newReplayGuard(func() time.Time { return now }, replayWindow)
	require.False(t, g.used("nonce-a"), "first use of a nonce is accepted")
	require.True(t, g.used("nonce-a"), "the same nonce again is a replay")
	require.False(t, g.used("nonce-b"), "a different nonce is fine")
}

func TestReplayGuardForgetsPastTheWindow(t *testing.T) {
	now := time.Now()
	g := newReplayGuard(func() time.Time { return now }, replayWindow)
	require.False(t, g.used("n"))
	now = now.Add(replayWindow + time.Second)
	require.False(t, g.used("n"), "a nonce past the window is forgotten (and its signature would be stale anyway)")
	g.mu.Lock()
	size := len(g.seen)
	g.mu.Unlock()
	require.Equal(t, 1, size, "the pruned entry did not linger")
}

func TestReplayGuardUsesTheInjectedClock(t *testing.T) {
	frozen := time.Unix(1000, 0)
	g := newReplayGuard(func() time.Time { return frozen }, replayWindow)
	require.False(t, g.used("x"))
	require.True(t, g.used("x"), "with a frozen clock the entry never expires within the window")
}

// The window must cover a signature's WHOLE validity span, not just SigMaxSkew from receipt.
// A future-skewed request stays valid until ts+SigMaxSkew but can first be seen at ts-SigMaxSkew,
// so the nonce must be remembered for 2*SigMaxSkew - or a replay slips through after the nonce
// expires but before the signature goes stale.
func TestReplayWindowCoversTheFullSkewSpan(t *testing.T) {
	require.Equal(t, 2*protocol.SigMaxSkew, replayWindow, "the nonce must outlive the widest signature validity")

	now := time.Now()
	clock := &now
	g := newReplayGuard(func() time.Time { return *clock }, replayWindow)
	require.False(t, g.used("n"))

	// Just before the window closes, the nonce is still remembered (a replay is caught) - so a
	// signature received early and replayed just before it goes stale cannot get through.
	*clock = now.Add(replayWindow - time.Second)
	require.True(t, g.used("n"), "the nonce is still remembered up to the full window")

	// Past the window (where the signature is stale anyway), it is forgotten - bounded memory.
	*clock = now.Add(replayWindow + time.Second)
	require.False(t, g.used("n"))
}

// Checking for a replay must NOT itself record the nonce - that separation is what lets the
// handler check before the rate limiter (a replay drains no rate) and record only after it (a
// throttled request records nothing, so memory stays bounded and a legitimate retry of a 429'd
// request is not misread as a replay).
func TestReplayGuardCheckDoesNotRecord(t *testing.T) {
	now := time.Now()
	g := newReplayGuard(func() time.Time { return now }, replayWindow)
	require.False(t, g.isReplay("k"))
	require.False(t, g.isReplay("k"), "a bare check never records, so a second check is still not a replay")
	g.record("k")
	require.True(t, g.isReplay("k"), "after record it is a replay")
}
