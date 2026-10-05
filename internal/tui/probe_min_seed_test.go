package tui

// probe_min_seed_test.go: Hooks.SavedProbeMin (the config.json share_prices probe_min
// entries) seeds the shared node controller, so a TUI or auto-started share registers the
// operator's declared minimum probe interval. Real controller, real agent.Start against an
// httptest broker that captures the signed registration; no mocks.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/protocol"
)

func TestSavedProbeMinReachesTheRegistration(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	var mu sync.Mutex
	regs := map[string]protocol.NodeRegistration{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/nodes/register") {
			b, _ := io.ReadAll(r.Body)
			var reg protocol.NodeRegistration
			_ = json.Unmarshal(b, &reg)
			mu.Lock()
			regs[reg.Offers[0].Model] = reg
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(srv.Close)

	ctrl := NewController(srv.URL, Hooks{Station: "swift-fox",
		SavedProbeMin: map[string]time.Duration{"claude-code": 6 * time.Hour}})
	ctrl.SetRows([]node.ShareRow{
		{Model: "claude-code", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
		{Model: "local-gpu", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
	})
	t.Cleanup(ctrl.StopAll)
	for _, m := range []string{"claude-code", "local-gpu"} {
		require.NoError(t, ctrl.ToggleOnAir(m).Err)
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 21600, regs["claude-code"].ProbeMinSeconds)
	require.Zero(t, regs["local-gpu"].ProbeMinSeconds)
}
