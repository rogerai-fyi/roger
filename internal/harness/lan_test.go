package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
)

// An agent turn in LAN mode goes through the local ladder and never to the broker
// (features/lan/routing_and_inference.feature, regressions.feature).
func TestBrokerCompleterInLANModeUsesTheLocalLadder(t *testing.T) {
	var brokerHits atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { brokerHits.Add(1) }))
	defer broker.Close()
	self := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"local answer"}}]}`))
	}))
	defer self.Close()
	client.SetLANRelay(&client.ProxyOptions{LANMode: true,
		LANSelf: func(string) (string, string) { return self.URL + "/v1/chat/completions", "" }})
	defer client.SetLANRelay(nil)
	c := BrokerCompleterRoute(BrokerRoute{Broker: broker.URL, User: "u", Model: "qwen3-8b"})
	msg, err := c(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	require.Equal(t, "local answer", msg.Content)
	require.Zero(t, brokerHits.Load())
}
