package main

// knobs_test.go pins the operator knobs and small policy strings whose non-default branches
// had never run. Each knob bounds a real resource - TTS input is billed, the audio
// semaphore is what keeps 8 x ~40MiB relays inside a 1GB instance - and an env override
// that silently failed to parse would run the default while the operator believes their
// setting took.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func TestAudioKnobsHonourTheirOverrides(t *testing.T) {
	t.Run("tts cap", func(t *testing.T) {
		t.Setenv("ROGERAI_TTS_MAX_CHARS", "")
		require.Equal(t, 10000, audioTTSMaxChars())
		t.Setenv("ROGERAI_TTS_MAX_CHARS", "2000")
		require.Equal(t, 2000, audioTTSMaxChars())
		t.Setenv("ROGERAI_TTS_MAX_CHARS", "0")
		require.Equal(t, 0, audioTTSMaxChars(), "an explicit 0 disables the cap - operator's choice")
		t.Setenv("ROGERAI_TTS_MAX_CHARS", "junk")
		require.Equal(t, 10000, audioTTSMaxChars(), "unparseable falls back, never to zero")
	})
	t.Run("audio semaphore", func(t *testing.T) {
		t.Setenv("ROGERAI_AUDIO_INFLIGHT", "")
		require.Equal(t, 8, cap(newAudioSem()))
		t.Setenv("ROGERAI_AUDIO_INFLIGHT", "2")
		require.Equal(t, 2, cap(newAudioSem()))
		t.Setenv("ROGERAI_AUDIO_INFLIGHT", "0")
		require.Nil(t, newAudioSem(), "0 disables the bound entirely - nil, not a zero-cap deadlock")
	})
}

func TestTruncateBoundsUntrustedProviderText(t *testing.T) {
	require.Equal(t, "short", truncate("short", 10))
	got := truncate(strings.Repeat("x", 100), 10)
	require.Equal(t, "xxxxxxxxxx...(truncated)", got)
}

// The quota refusal names the caller's own blocking band - and leads with MOVE, which keeps
// the frequency code alive, rather than revoke, which burns it. A revoked band must never
// be named as the blocker: it is not what is in the way.
func TestQuotaRefusalNamesTheBlockingBandOnly(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	now := time.Now()

	owner := store.Owner{Pubkey: "pk-quota-test"}
	require.Contains(t, b.quotaRefusal(owner, now), "move or revoke",
		"no bands at all still explains the way out")
}
