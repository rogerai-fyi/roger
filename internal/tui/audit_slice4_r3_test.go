package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAgentTurnReadsLiveRouting: the F/C toggles flipped AFTER the agent runtime was built
// reach the next agent turn's request body (the completer must not run on the routing
// captured when the runtime was created).
func TestAgentTurnReadsLiveRouting(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		http.Error(w, `{"error":{"code":"no_match","message":"none"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	m := browseSeed(120)
	m.broker = srv.URL
	m.agent = m.newAgentRuntime()
	m.agent.model = "m1"
	m.agent.localChat = ""

	// Toggled after the runtime exists, as the booth does mid-session.
	m.fFree, m.fConf = true, true

	cmd := m.startAgentTurn("hello")
	cmd()
	select {
	case <-m.agent.turnDone:
	case <-time.After(10 * time.Second):
		t.Fatal("agent turn did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, bodies, "the turn reached the broker")
	require.Equal(t, "m1:free", bodies[0]["model"], "F on: the agent turn asks for the free variant")
	rg, _ := bodies[0]["roger"].(map[string]any)
	require.Equal(t, true, rg["confidential"], "C on: the agent turn is confidential-only")
}

// TestRoutingProfileMaxCostComposesStricter: a profile's max cost never loosens the band's.
func TestRoutingProfileMaxCostComposesStricter(t *testing.T) {
	m := auditProfileModel(t, map[string]any{"provider": map[string]any{"max_price": map[string]any{"request": 1.0}}})
	m.limits.Models = map[string]Limit{"m": {MaxCost: 0.02}}
	require.InDelta(t, 0.02, m.routing("m", "").MaxReq, 1e-12, "the band's tighter max cost holds")
	m.limits.Models = map[string]Limit{"m": {MaxCost: 5}}
	require.InDelta(t, 1.0, m.routing("m", "").MaxReq, 1e-12, "the profile's tighter max cost holds")
}
