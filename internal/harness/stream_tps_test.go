package harness

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
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

// TestDoneAfterACutToolCallIsNotSuccess: [DONE] closes the stream, but a tool call is whole
// only when the station said it finished; a cut tool call followed by [DONE] is an error,
// while a text reply that ends in [DONE] stays a success.
func TestDoneAfterACutToolCallIsNotSuccess(t *testing.T) {
	cut := readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"))
	require.Error(t, cut.streamError(), "a cut tool call is not a successful turn")

	whole := readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"))
	require.NoError(t, whole.streamError())

	text := readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	require.NoError(t, text.streamError(), "a text reply closed by [DONE] is complete")
}

// TestVoidedStreamIsNotASuccessfulTurn: the broker ends a stream it voided after content
// started with a usage chunk naming the void, then [DONE]; that is a cut reply, not a turn.
// A settle-failed void (the reply finished, the ledger refused the charge) is still a reply.
func TestVoidedStreamIsNotASuccessfulTurn(t *testing.T) {
	cut := readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"cost\":0,\"rogerai\":{\"node\":\"n\",\"model\":\"m\",\"void_reason\":\"" + protocol.VoidUpstreamError + "\"}}}\n\n" +
		"data: [DONE]\n\n"))
	require.ErrorContains(t, cut.streamError(), "not charged")
	settled := readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"all\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"cost\":0,\"rogerai\":{\"node\":\"n\",\"model\":\"m\",\"void_reason\":\"settle-failed\"}}}\n\n" +
		"data: [DONE]\n\n"))
	require.NoError(t, settled.streamError())
}

// TestVoidAfterAFinishedReplyIsNotCalledCut: a void that arrives after the station finished
// says the broker voided it (not charged), not that the reply was cut.
func TestVoidAfterAFinishedReplyIsNotCalledCut(t *testing.T) {
	st := readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"all\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"cost\":0,\"rogerai\":{\"node\":\"n\",\"model\":\"m\",\"void_reason\":\"" + protocol.VoidUpstreamError + "\"}}}\n\n" +
		"data: [DONE]\n\n"))
	err := st.streamError()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "cut")
	require.Contains(t, err.Error(), "voided this reply ("+protocol.VoidUpstreamError+")")
	require.Contains(t, err.Error(), "not charged")
}

// TestMidStreamVoidIsCalledCut: a void with no finish_reason before it (the broker's
// void chunk then [DONE] on a reply that broke off) is a cut reply, worded so.
func TestMidStreamVoidIsCalledCut(t *testing.T) {
	st := readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"cost\":0,\"rogerai\":{\"node\":\"n\",\"model\":\"m\",\"void_reason\":\"" + protocol.VoidUpstreamError + "\"}}}\n\n" +
		"data: [DONE]\n\n"))
	require.ErrorContains(t, st.streamError(), "cut")
}
