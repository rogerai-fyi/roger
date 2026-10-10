package main

import (
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// voiceRecord puts a voice on air on this machine the way a separate `roger share` does.
func voiceRecord(t *testing.T, band string) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "RIFF here")
	}))
	t.Cleanup(up.Close)
	stop, err := onair.WriteShare(onair.Share{PID: os.Getpid(), Band: band, ChatURL: up.URL + "/v1/chat/completions",
		Modality: protocol.ModalityTTS, SpeechURL: up.URL + "/v1/audio/speech"})
	require.NoError(t, err)
	t.Cleanup(stop)
	return up
}

func TestLANSpeakLineSpeaksOrSaysGone(t *testing.T) {
	useTempConfig(t)
	_, err := lanSpeakLine("studio-voice", "hi")
	require.ErrorContains(t, err, "the local text-to-speech (TTS) model studio-voice is no longer on air")

	voiceRecord(t, "studio-voice")
	audio, err := lanSpeakLine("studio-voice", "hi")
	require.NoError(t, err)
	require.Equal(t, "RIFF here", string(audio))
	require.Equal(t, []string{"studio-voice"}, lanVoiceNames())
}

func TestAVoiceIsNotAChatTurnsSource(t *testing.T) {
	useTempConfig(t)
	voiceRecord(t, "studio-voice")
	u, _ := prodLANSelfServe("studio-voice")
	require.Empty(t, u, "a chat turn is never sent to a voice server")
	_, _, ok := localSpeechOf("studio-voice")
	require.True(t, ok)
	_, _, ok = localSpeechOf("other")
	require.False(t, ok)
}

// A member's voice is used only when the serving probe proved the band, it is LAN-direct, and it
// is not this machine; a voice the record declares beyond its bands is never kept.
func TestEdgeVoicePeersAreProvenMembersOnly(t *testing.T) {
	useTempConfig(t)
	s := &lanDialBDD{t: t}
	require.NoError(t, s.enroll())
	st, err := loadEdgeState()
	require.NoError(t, err)
	observe := func(node string, bands, voices []string, prove bool) {
		id := "n_" + node + strings.Repeat("0", 24-len(node))
		_, err := st.fleet.Observe(id, edge.Observation{Name: node, Kind: "host", Addr: "192.168.1.5:9", Fingerprint: "ab",
			Instances: []edge.Instance{{Name: "serve", Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Claimed)}},
				Bands: bands, Voices: voices}}})
		require.NoError(t, err)
		if prove {
			require.NoError(t, st.fleet.RecordInstanceProbe(id, "serve", edge.Serve, true))
			require.NoError(t, st.fleet.RecordServeProbe(id, "serve", bands))
		}
	}
	observe("proven", []string{"studio-voice"}, []string{"studio-voice"}, true)
	observe("claimed", []string{"studio-voice"}, []string{"studio-voice"}, false)
	observe("liar", []string{"qwen3-8b"}, []string{"studio-voice"}, true)
	require.NoError(t, st.save())

	got := edgeVoicePeers("")
	require.Len(t, got, 1)
	require.Equal(t, "proven", got[0].Source)
	require.Equal(t, "192.168.1.5:9", got[0].Peer.Addr)
	require.Empty(t, edgeVoicePeers("qwen3-8b"), "a chat band is never a voice")
}

func TestParseInterspersedTakesFlagsAnywhere(t *testing.T) {
	fs := flag.NewFlagSet("say", flag.ContinueOnError)
	v := fs.String("voice", "", "")
	words, err := parseInterspersed(fs, []string{"hello", "--voice", "x", "there"})
	require.NoError(t, err)
	require.Equal(t, []string{"hello", "there"}, words)
	require.Equal(t, "x", *v)
	_, err = parseInterspersed(fs, []string{"--nope"})
	require.Error(t, err)
}

func TestSayLANBranches(t *testing.T) {
	useTempConfig(t)
	out, err := captureEdgeStdout(func() error { return run([]string{"--lan", "say", "--list"}, loadConfig()) })
	require.NoError(t, err)
	require.Contains(t, out, "no local text-to-speech (TTS) model is on air")

	voiceRecord(t, "studio-voice")
	_, err = captureEdgeStdout(func() error { return run([]string{"--lan", "say"}, loadConfig()) })
	require.ErrorContains(t, err, "nothing to say")

	// Saved when no player exists: the clip's path and that it was free.
	prev := sayPlayer
	t.Cleanup(func() { sayPlayer = prev })
	sayPlayer = func([]byte) (string, bool, error) { return "/tmp/clip.wav", false, nil }
	out, err = captureEdgeStdout(func() error { return run([]string{"--lan", "say", "hi"}, loadConfig()) })
	require.NoError(t, err)
	require.Contains(t, out, "saved the clip to /tmp/clip.wav")
	require.Contains(t, out, "free")
	sayPlayer = func([]byte) (string, bool, error) { return "", false, io.ErrClosedPipe }
	_, err = captureEdgeStdout(func() error { return run([]string{"--lan", "say", "hi"}, loadConfig()) })
	require.ErrorContains(t, err, "could not play or save")
}

// Outside LAN mode, --list is the network roster (the one place `say` asks the broker for voices).
func TestSayListOutsideLANIsTheRoster(t *testing.T) {
	useTempConfig(t)
	hit := false
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path == "/voices"
		_, _ = io.WriteString(w, `{"voices":[]}`)
	}))
	defer b.Close()
	t.Setenv("ROGER_BROKER", b.URL)
	_, err := captureEdgeStdout(func() error { return run([]string{"say", "--list"}, loadConfig()) })
	require.NoError(t, err)
	require.True(t, hit)
}
