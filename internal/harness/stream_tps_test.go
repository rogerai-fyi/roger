package harness

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStreamedTurnReportsTPSFromTheUsageChunk: a streamed turn's throughput comes from the
// broker's usage chunk (usage.rogerai.tps); a stream carries no X-RogerAI-TPS header.
func TestStreamedTurnReportsTPSFromTheUsageChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"cost\":0.001,\"rogerai\":{\"node\":\"n1\",\"model\":\"m\",\"tps\":42.5}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var tps float64
	c := BrokerCompleterRoute(BrokerRoute{Broker: srv.URL, User: "u", Model: "m",
		OnCost: func(_ float64, _, _ int, v float64) { tps = v }})
	_, err := c(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	require.NoError(t, err)
	require.InDelta(t, 42.5, tps, 1e-9, "the agent t/s meter gets the stream's rate")
}
