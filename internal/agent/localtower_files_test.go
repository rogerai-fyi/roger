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

	"rogerai.fm/roger/v6/internal/servedfile"
)

// features/models/short_names_nearby.feature "a station on a pinned Tower reports what it serves,
// as its own word": the poll names the exact file behind each model this station serves itself.
func TestAStationPollNamesTheFileBehindEachModel(t *testing.T) {
	var got struct {
		Models []string `json:"models"`
		Files  []struct {
			Model string `json:"model"`
			servedfile.File
		} `json:"files"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := servedfile.File{Repo: "acme/Tiny-GGUF", File: "Tiny-Q4_K_M.gguf", Quant: "Q4_K_M",
		SHA256: "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"}
	cfg := Config{
		StationModels: func() []string { return []string{"tiny", "other"} },
		StationFiles:  func() map[string]servedfile.File { return map[string]servedfile.File{"tiny": f} },
	}
	_, _, err := pollLocalJob(context.Background(), srv.Client(), srv.URL, priv, cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"tiny", "other"}, got.Models)
	require.Len(t, got.Files, 1, "only a model with a known file is named")
	require.Equal(t, "tiny", got.Files[0].Model)
	require.Equal(t, f, got.Files[0].File)
}
