package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/towerpin"
)

// features/join/tower_identity.feature: "The dial marks pinned Towers as verified". A Tower this
// machine pinned (it joined by invite) is read over TLS to exactly its key and its rows are
// verified, like an Edge member's; a Tower reached over plain http is not.
func TestAPinnedTowerSourceIsVerified(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cfg, err := towerpin.ServerTLS(key)
	require.NoError(t, err)
	tw := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": []Offer{{Model: "qwen3-8b", Online: true}}})
	}))
	tw.TLS = cfg
	tw.StartTLS()
	t.Cleanup(tw.Close)
	plain := fakeTowerDiscover(t, []Offer{{Model: "llama-3.3-70b", Online: true}}, true)

	lanmode.SetPins(map[string]string{tw.Listener.Addr().String(): towerpin.Fingerprint(key.Public().(ed25519.PublicKey))})
	t.Cleanup(func() { lanmode.SetPins(nil) })

	bands, status := LANDiscover(context.Background(), LANInputs{
		Towers: []string{tw.Listener.Addr().String(), plain.Listener.Addr().String()},
		Key:    lanKey(t),
	})
	require.Len(t, status, 2)
	verified := map[string]bool{}
	for _, b := range bands {
		require.Len(t, b.Sources, 1)
		verified[b.Model] = b.Sources[0].Verified
	}
	require.Equal(t, map[string]bool{"qwen3-8b": true, "llama-3.3-70b": false}, verified)
}
