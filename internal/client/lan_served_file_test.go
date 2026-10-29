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
	"rogerai.fm/roger/v7/internal/servedfile"
	"rogerai.fm/roger/v7/internal/towerpin"
)

// features/models/short_names_nearby.feature: a station on a PINNED Tower may say which file a band
// is, as its own word; an unpinned Tower is plain http and defines no file; an older Tower names none.
func TestOnlyAPinnedTowersStationReportNamesAFile(t *testing.T) {
	f := servedfile.File{Repo: "acme/Tiny-GGUF", File: "Tiny-Q4_K_M.gguf", Quant: "Q4_K_M",
		SHA256: "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cfg, err := towerpin.ServerTLS(key)
	require.NoError(t, err)
	tw := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": []Offer{
			{Model: "tiny", Online: true, File: &f, FileBy: "station"},
			{Model: "old", Online: true},
		}})
	}))
	tw.TLS = cfg
	tw.StartTLS()
	t.Cleanup(tw.Close)
	plain := fakeTowerDiscover(t, []Offer{{Model: "plain-tiny", Online: true, File: &f, FileBy: "station"}}, true)
	lanmode.SetPins(map[string]string{tw.Listener.Addr().String(): towerpin.Fingerprint(key.Public().(ed25519.PublicKey))})
	t.Cleanup(func() { lanmode.SetPins(nil) })

	bands, _ := LANDiscover(context.Background(), LANInputs{
		Towers: []string{tw.Listener.Addr().String(), plain.Listener.Addr().String()},
		Key:    lanKey(t),
	})
	files := map[string]*servedfile.File{}
	for _, b := range bands {
		require.Len(t, b.Sources, 1)
		files[b.Model] = b.Sources[0].File
	}
	require.NotNil(t, files["tiny"])
	require.Equal(t, f, *files["tiny"])
	require.Nil(t, files["old"], "an older Tower names no file")
	require.Nil(t, files["plain-tiny"], "an unpinned Tower's report never defines a file")
}

// An Edge member's own record names the file behind its band; a claimed or dark band maps nothing.
func TestAnEdgeMembersRecordNamesItsFile(t *testing.T) {
	f := servedfile.File{Repo: "acme/Tiny-GGUF", File: "Tiny-Q4_K_M.gguf", Quant: "Q4_K_M",
		SHA256: "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"}
	bands, _ := LANDiscover(context.Background(), LANInputs{Edge: []LANEdgeBand{
		{Node: "atlas", Band: "tiny", Verified: true, LANDirect: true, File: &f},
		{Node: "atlas", Band: "plain", Verified: true, LANDirect: true},
	}})
	got := map[string]*servedfile.File{}
	for _, b := range bands {
		got[b.Model] = b.Sources[0].File
	}
	require.Equal(t, f, *got["tiny"])
	require.Nil(t, got["plain"], "an older member names no file")
}
