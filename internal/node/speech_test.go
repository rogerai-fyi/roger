package node

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpeechURLIsBesideTheChatURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8790/v1/chat/completions": "http://127.0.0.1:8790/v1/audio/speech",
		"http://127.0.0.1:8790/v1":                  "http://127.0.0.1:8790/v1/audio/speech",
		"http://127.0.0.1:8790":                     "http://127.0.0.1:8790/v1/audio/speech",
	} {
		require.Equal(t, want, SpeechURL(in), in)
	}
}

// ServeSpeechFor resolves only an ON-AIR VOICE band (features/lan/voice.feature): a chat band, or a
// voice not on air, has no speech URL.
func TestServeSpeechForOnlyOnAirVoices(t *testing.T) {
	c := New(Config{Broker: fakeBroker(t), Station: "amber-fox"})
	c.SetRows([]ShareRow{
		{Model: "voice-1", Modality: "tts", Upstream: "http://127.0.0.1:0/v1/chat/completions", UpstreamKey: "sk-v"},
		{Model: "chat-1", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
	})
	_, _, ok := c.ServeSpeechFor("voice-1")
	require.False(t, ok, "not on air")
	require.NoError(t, c.ToggleOnAir("voice-1").Err)
	require.NoError(t, c.ToggleOnAir("chat-1").Err)
	u, k, ok := c.ServeSpeechFor("voice-1")
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:0/v1/audio/speech", u)
	require.Equal(t, "sk-v", k)
	_, _, ok = c.ServeSpeechFor("chat-1")
	require.False(t, ok, "a chat band is not a voice")
	_, _, ok = c.ServeSpeechFor("no-such-model")
	require.False(t, ok)
}
