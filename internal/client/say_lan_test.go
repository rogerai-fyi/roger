package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// voiceSrv is a local voice server: "ok" answers audio, "fail" a 500 echoing the key, "cut" drops
// the connection partway through the audio.
func voiceSrv(t *testing.T, mode string, seen *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			_ = json.NewDecoder(r.Body).Decode(seen)
			(*seen)["auth"] = r.Header.Get("Authorization")
		}
		switch mode {
		case "fail":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "bad key "+r.Header.Get("Authorization"))
		case "cut":
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "RIFF part")
			w.(http.Flusher).Flush()
			c, _, _ := w.(http.Hijacker).Hijack()
			_ = c.Close()
		default:
			_, _ = io.WriteString(w, "RIFF "+mode)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSpeakLANMovesPastAFailureBeforeAudio(t *testing.T) {
	seen := map[string]any{}
	bad, good := voiceSrv(t, "fail", nil), voiceSrv(t, "ok", &seen)
	res, err := SpeakLAN(context.Background(), []LANVoice{
		{Band: "v", Source: "this machine", URL: bad.URL, Key: "sk-secret"},
		{Band: "v", Source: "desk", URL: good.URL, Key: "k2"},
	}, "hello", LANSpeechOptions{Speed: 1.25})
	require.NoError(t, err)
	require.Equal(t, "RIFF ok", string(res.Audio))
	require.Equal(t, "desk", res.Voice.Source)
	require.Equal(t, []string{"this machine: answered 500"}, res.Left, "the key in the error body is never repeated")
	require.Equal(t, "v", seen["model"])
	require.Equal(t, "hello", seen["input"])
	require.Equal(t, "wav", seen["response_format"])
	require.Equal(t, 1.25, seen["speed"])
	require.Equal(t, "Bearer k2", seen["auth"])
}

func TestSpeakLANNeverReSpeaksAPartialLine(t *testing.T) {
	cut, other := voiceSrv(t, "cut", nil), voiceSrv(t, "ok", nil)
	_, err := SpeakLAN(context.Background(), []LANVoice{
		{Band: "v", Source: "jetson", URL: cut.URL},
		{Band: "v", Source: "orin", URL: other.URL},
	}, "hello", LANSpeechOptions{})
	require.ErrorAs(t, err, new(ErrLANVoiceStopped))
	require.Contains(t, err.Error(), "the local voice on jetson stopped before finishing")
}

func TestSpeakLANNamesEveryFailure(t *testing.T) {
	bad := voiceSrv(t, "fail", nil)
	_, err := SpeakLAN(context.Background(), []LANVoice{
		{Band: "v", Source: "this machine", URL: bad.URL},
		{Band: "v", Source: "jetson", Peer: &EdgePeer{Addr: "127.0.0.1:1", Pin: "00"}},
		{Band: "v", Source: "broken", URL: "://bad"},
	}, "hello", LANSpeechOptions{})
	require.ErrorContains(t, err, "this machine: answered 500")
	require.ErrorContains(t, err, "jetson:")
	require.ErrorContains(t, err, "broken:")
	require.ErrorContains(t, err, "the network's voices were not tried")
}
