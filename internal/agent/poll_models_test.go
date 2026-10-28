package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// features/join/invite.feature, "A station's model list follows what it actually serves": every
// poll says what this station serves now (Config.StationModels), so the Tower routes that.
func TestAStationPollSaysWhatItServesNow(t *testing.T) {
	var got struct {
		Streams bool     `json:"streams"`
		Models  []string `json:"models"`
	}
	tw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(tw.Close)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	serving := []string{"gpt-oss-20b"}
	_, _, err := pollLocalJob(context.Background(), tw.Client(), tw.URL, priv, Config{StationModels: func() []string { return serving }})
	require.NoError(t, err)
	require.True(t, got.Streams)
	require.Equal(t, []string{"gpt-oss-20b"}, got.Models)

	// With no way to know (an older caller), the poll names nothing and the Tower keeps its list.
	got.Models = nil
	_, _, err = pollLocalJob(context.Background(), tw.Client(), tw.URL, priv, Config{})
	require.NoError(t, err)
	require.Nil(t, got.Models)
}
