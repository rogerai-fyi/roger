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

// The broker's usage chunk carries locked_until as a unix-seconds INTEGER (tunnel.go, the
// usage chunk's rogerai block); an older shape sent a string. Both must decode, so the chunk
// is never dropped (which would zero the token counts and skip OnServed).
func TestReadStreamDecodesTheBrokersIntegerLockedUntil(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"cost\":0.01,\"rogerai\":{\"node\":\"n1\",\"model\":\"m\",\"locked_until\":1790000000}}}\n" +
		"data: [DONE]\n"
	st := readStream(strings.NewReader(sse))
	if st.in != 7 || st.out != 3 || st.cost != 0.01 {
		t.Fatalf("the integer locked_until dropped the usage chunk: in=%d out=%d cost=%v", st.in, st.out, st.cost)
	}
	if st.served.Node != "n1" || st.served.LockedUntil != "2026-09-21T14:13:20Z" {
		t.Errorf("served = %+v, want node n1 and the lock as RFC 3339", st.served)
	}
	st = readStream(strings.NewReader(strings.Replace(sse, "1790000000", "0", 1)))
	if st.served.LockedUntil != "" || st.in != 7 {
		t.Errorf("a zero lock is no lock: %+v", st.served)
	}
}

// A stream that ends without [DONE] or a finish_reason was cut (cancelled, reset, or the
// reader's size cap): its partial content must never pass as a successful turn.
func TestReadStreamRejectsATruncatedStream(t *testing.T) {
	cut := "data: {\"choices\":[{\"delta\":{\"content\":\"half an ans\",\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"write\",\"arguments\":\"{\\\"p\\\":\"}}]}}]}\n"
	if err := readStream(strings.NewReader(cut)).streamError(); err == nil {
		t.Fatal("a stream cut before [DONE] or a finish_reason passed as a successful turn")
	}
	// [DONE] completes a text reply; a tool call is whole only with a finish_reason, so the
	// cut tool call above stays an error even when [DONE] follows it.
	done := "data: {\"choices\":[{\"delta\":{\"content\":\"half an ans\"}}]}\ndata: [DONE]\n"
	if err := readStream(strings.NewReader(done)).streamError(); err != nil {
		t.Errorf("a text stream that ends with [DONE] is complete: %v", err)
	}
	if err := readStream(strings.NewReader(cut + "data: [DONE]\n")).streamError(); err == nil {
		t.Error("a cut tool call followed by [DONE] passed as a successful turn")
	}
	finished := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n"
	if err := readStream(strings.NewReader(finished)).streamError(); err != nil {
		t.Errorf("a finish_reason marks the reply complete: %v", err)
	}
	if err := readStream(&errReader{data: cut}).streamError(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a read error must surface, got %v", err)
	}
}

type errReader struct {
	data string
	done bool
}

func (e *errReader) Read(p []byte) (int, error) {
	if !e.done {
		e.done = true
		return copy(p, e.data), nil
	}
	return 0, fmt.Errorf("boom: connection reset")
}
