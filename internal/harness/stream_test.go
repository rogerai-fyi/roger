package harness

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadStreamAssemblesTheTurn(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"ro"}}]}`,
		`data: {"choices":[{"delta":{"content":"ger","tool_calls":[{"index":1,"id":"c2","function":{"name":"read","arguments":"{\"p\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"list","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"a\"}"}}]},"finish_reason":"length"}]}`,
		`: keepalive`,
		`data: not json`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.0123,"rogerai":{"node":"n2","model":"l","locked_until":"T"}}}`,
		`: rogerai-cost=0.0123`,
		`data: [DONE]`,
	}, "\n")
	st := readStream(strings.NewReader(sse))
	if st.msg.Content != "roger" || !st.msg.Truncated || st.cost != 0.0123 || st.in != 10 || st.out != 5 {
		t.Fatalf("stream reassembled wrong: %+v", st)
	}
	if st.served != (Served{Model: "l", Node: "n2", LockedUntil: "T"}) {
		t.Errorf("served = %+v", st.served)
	}
	if len(st.msg.ToolCalls) != 2 || st.msg.ToolCalls[0].Function.Name != "list" || st.msg.ToolCalls[1].Function.Arguments != `{"p":"a"}` ||
		st.msg.ToolCalls[1].Type != "function" {
		t.Errorf("tool calls = %+v", st.msg.ToolCalls)
	}
	if err := st.streamError(); err != nil {
		t.Error(err)
	}

	// the comment alone meters an old broker; reasoning-only is a thought, never content
	st = readStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hm\"}}]}\n: rogerai-cost=0.004\n"))
	if st.cost != 0.004 || st.msg.Thought != "hm" || st.msg.Content != "" {
		t.Errorf("comment-only stream: %+v", st)
	}
	if err := readStream(strings.NewReader(`data: {"error":{"message":"no_match"}}`)).streamError(); err == nil || err.Error() != "no_match" {
		t.Errorf("an error event is the turn's error, got %v", err)
	}
	if err := readStream(strings.NewReader("")).streamError(); err == nil {
		t.Error("an empty stream is an error")
	}
}

func TestBrokerCompleterReadsAStreamedTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-RogerAI-TPS", "12")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"cost\":0.5,\"rogerai\":{\"node\":\"n\",\"model\":\"m\"}}}\n\n")
		fmt.Fprint(w, ": rogerai-cost=0.5\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var cost float64
	var served Served
	c := BrokerCompleterRoute(BrokerRoute{Broker: srv.URL, User: "u", Model: "m",
		OnCost:   func(c float64, in, out int, tps float64) { cost += c },
		OnServed: func(s Served) { served = s }})
	msg, err := c(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil || msg.Content != "hi" {
		t.Fatalf("msg=%+v err=%v", msg, err)
	}
	if cost != 0.5 {
		t.Errorf("the meter counted %g, want the chunk's 0.5 once", cost)
	}
	if served.Node != "n" {
		t.Errorf("served = %+v", served)
	}
}
