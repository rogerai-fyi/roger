package webui

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// agent_test.go - the console's agent turn.

func agentPost(t *testing.T, s *Server, body string) (*http.Response, []agentEvent) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agent", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Roger-Token", s.Token())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	var out []agentEvent
	sc := bufio.NewScanner(rec.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e agentEvent
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return res, out
}

// A turn that spends the operator's money is a WRITE: token-gated and POST-only, the
// same rule every other write on this console follows. That is also why the stream is
// NDJSON over the POST response rather than EventSource, which is GET-only.
func TestAgentTurnIsAGuardedWrite(t *testing.T) {
	s := New(testCtrl(), Options{Broker: "http://127.0.0.1:1"})
	req := httptest.NewRequest(http.MethodGet, "/api/agent?t="+s.Token(), nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/agent = %d, want 405", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agent", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("untokened agent turn = %d, want 403", rec.Code)
	}
}

// THE WHOLE FEATURE: the model calls a tool, the tool runs, and the browser is streamed
// every step in order.
func TestAgentTurnStreamsItsToolSteps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/notes.md", []byte("hello from a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	step := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		if step == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"notes.md\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"the file says hello"}}]}`))
	}))
	defer up.Close()

	s := New(testCtrl(), Options{Broker: up.URL, User: "u"})
	res, events := agentPost(t, s, `{"model":"m1","message":"what is in notes.md"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "tool_call") || !strings.Contains(joined, "tool_result") {
		t.Fatalf("the browser must see the tool steps, got %q", joined)
	}
	// Order matters: a call is streamed before its result, or the box settles a row
	// that has not appeared yet.
	if strings.Index(joined, "tool_call") > strings.Index(joined, "tool_result") {
		t.Errorf("a call must stream before its result: %q", joined)
	}
	var callEv agentEvent
	for _, e := range events {
		if e.Kind == "tool_call" {
			callEv = e
		}
	}
	if callEv.Tool != "read_file" {
		t.Errorf("the call must name its tool, got %q", callEv.Tool)
	}
	// The ARG SUMMARY comes from the harness, so the browser shows what the terminal
	// shows rather than raw argument JSON.
	if callEv.Arg != "notes.md" {
		t.Errorf("the call must carry the shared arg summary, got %q", callEv.Arg)
	}
	// The stream ends with the RECEIPT now (the turn's billed total), so the answer is
	// the event before it. Both must be present and in that order: an answer with no
	// receipt hides what it cost, and a receipt with no answer is a bill for nothing.
	var final, receipt *agentEvent
	for i := range events {
		switch events[i].Kind {
		case "final":
			final = &events[i]
		case "receipt":
			receipt = &events[i]
		}
	}
	if final == nil || !strings.Contains(final.Text, "hello") {
		t.Fatalf("the turn must produce the answer, got %+v", events)
	}
	if receipt == nil {
		t.Fatal("...and the receipt with it")
	}
	if events[len(events)-1].Kind != "receipt" {
		t.Errorf("the receipt is last - it totals everything before it, got %q", events[len(events)-1].Kind)
	}
}

// READ-ONLY, deliberately. run_shell reachable from a browser is a materially bigger
// blast radius than one reachable from the terminal you are already typing in, and
// turning it on is a decision to make knowingly - not one to inherit because the
// toolset came along.
func TestConsoleAgentIsReadOnly(t *testing.T) {
	s := New(testCtrl(), Options{Broker: "http://127.0.0.1:1", User: "u"})
	l, err := s.agentLoop("m1")
	if err != nil {
		t.Fatalf("agentLoop: %v", err)
	}
	for _, tl := range l.Tools() {
		if tl.Mutating {
			t.Errorf("the console agent must not carry the mutating tool %q", tl.Name)
		}
	}
	// ...and it still has the useful read-only ones, or the feature is pointless.
	names := map[string]bool{}
	for _, tl := range l.Tools() {
		names[tl.Name] = true
	}
	// web_search is deliberately NOT here: it rides only when a search provider is
	// configured, because advertising a tool that can only dead-end is worse than not
	// offering it (features/answers/web_search.feature). These four are unconditional.
	for _, want := range []string{"read_file", "list_dir", "web_fetch", "delegate"} {
		if !names[want] {
			t.Errorf("the console agent should offer %q", want)
		}
	}
}

// Without a broker there is nothing to relay through, and saying so beats a transport
// error the operator has to decode.
func TestAgentWithoutBrokerSaysSo(t *testing.T) {
	s := New(testCtrl(), Options{})
	res, _ := agentPost(t, s, `{"model":"m1","message":"hi"}`)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", res.StatusCode)
	}
}

// BOTH SURFACES MUST DESCRIBE A REFUSAL THE SAME WAY. "denied" means the OPERATOR said
// no; a guard refusal is the harness applying a rule. Conflating them is what made a
// screen of tool calls read as a permissions problem nobody was ever asked about
// (founder screenshot 2026-08-21), and a console that still conflated them would
// reintroduce the same confusion on the other surface.
func TestConsoleDistinguishesRefusalFromDenial(t *testing.T) {
	js, err := os.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(js), "function chatResultHint")
	if i < 0 {
		t.Fatal("chatResultHint not found")
	}
	block := string(js)[i : i+900]
	if !strings.Contains(block, `if (e.denied) return "denied"`) {
		t.Error("an operator denial must still read as denied")
	}
	if !strings.Contains(block, `"refused · "`) {
		t.Error("a guard refusal must read as refused, not denied")
	}
	// ...and the model-facing guidance must not be dumped on the row. It stops at the
	// first SENTENCE END (". "), not at any period: cutting on a bare "." sliced URLs in
	// half, so "https://rogerai.fyi/..." became "https://rogerai" - the wrong host,
	// reading like a different refusal entirely.
	if !strings.Contains(block, `indexOf(". ")`) {
		t.Error("the refusal must stop at a sentence end, not inside a URL")
	}
}

// Both sides of an exchange are enclosed, or one reads as framed and the other as bare.
func TestConsoleEnclosesBothSidesOfAnExchange(t *testing.T) {
	css, err := os.ReadFile("assets/console.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".chat-turn--you .chat-body", ".chat-turn--reply .chat-body"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("both turns need a surface: missing %s", want)
		}
	}
	js, _ := os.ReadFile("assets/console.js")
	if !strings.Contains(string(js), `"chat-turn--reply"`) {
		t.Error("the reply must actually be given the class")
	}
}

// ── THE TURN RECEIPT ─────────────────────────────────────────────────────────
// A turn is many relayed calls now - one per model step, plus any subagent's - so the
// only honest total is their sum. The per-CALL renderer was removed rather than left
// showing one call's numbers as the turn's; this is what replaces it.

func TestTurnReceiptSumsTheWholeTurn(t *testing.T) {
	step := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		// Every relayed call reports its own billed numbers.
		w.Header().Set("X-RogerAI-Cost", "0.001")
		if step == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer up.Close()

	s := New(testCtrl(), Options{Broker: up.URL, User: "u"})
	_, events := agentPost(t, s, `{"model":"m1","message":"what is here"}`)

	var rc *agentEvent
	for i := range events {
		if events[i].Kind == "receipt" {
			rc = &events[i]
		}
	}
	if rc == nil {
		t.Fatal("a turn must end with a receipt - being able to see what a turn cost is why this console exists")
	}
	// TWO model calls were relayed, so the receipt counts two - not one.
	if rc.Calls != 2 {
		t.Errorf("the receipt must sum every relayed call, got %d", rc.Calls)
	}
	if rc.Steps < 2 {
		t.Errorf("steps = %d, want the turn's own steps", rc.Steps)
	}
	if rc.Incomplete {
		t.Error("a turn that finished must not read as incomplete")
	}
}

// A turn that FAILED still spent what it spent; dropping the receipt would understate
// the bill.
func TestFailedTurnStillReceipts(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"no node offers that model"}}`))
	}))
	defer up.Close()
	s := New(testCtrl(), Options{Broker: up.URL, User: "u"})
	_, events := agentPost(t, s, `{"model":"m1","message":"hi"}`)
	var sawErr, sawReceipt bool
	for _, e := range events {
		if e.Kind == "error" {
			sawErr = true
		}
		if e.Kind == "receipt" {
			sawReceipt = true
		}
	}
	if !sawErr {
		t.Error("the failure must be reported")
	}
	if !sawReceipt {
		t.Error("...and the receipt with it - a failed turn still spent what it spent")
	}
}

// The browser must not print a zero as though it were a measurement: a free local band
// and a turn whose cost never arrived are not the same thing.
func TestConsoleOmitsZeroesFromTheReceipt(t *testing.T) {
	js, err := os.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(js), "function chatShowReceipt")
	if i < 0 {
		t.Fatal("chatShowReceipt not found")
	}
	block := string(js)[i : i+1200]
	for _, guard := range []string{"if (e.calls)", "if (e.cost)", "if (e.delegated)"} {
		if !strings.Contains(block, guard) {
			t.Errorf("the receipt must guard %s - a printed zero reads as a claim", guard)
		}
	}
	if !strings.Contains(block, "incomplete") {
		t.Error("a partial tree must say it is a lower bound")
	}
}

// THE ANSWER IS SENT ONCE (founder 2026-08-21: "i'm seeing two replies").
//
// The loop emits an `assistant` event per model step, and the LAST of those already
// carries the answer. Sending it again as `final` made a plain question - one step, no
// tools - render its reply TWICE in the browser, while the receipt honestly said
// "1 call · 1 step". The duplicate was in the transport, not the model.
func TestTheAnswerIsNotSentTwice(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Roger that."}}]}`))
	}))
	defer up.Close()

	s := New(testCtrl(), Options{Broker: up.URL, User: "u"})
	_, events := agentPost(t, s, `{"model":"m1","message":"hi"}`)

	replies, kinds := 0, []string{}
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		if (e.Kind == "assistant" || e.Kind == "final") && strings.TrimSpace(e.Text) == "Roger that." {
			replies++
		}
	}
	if replies != 1 {
		t.Errorf("the answer was streamed %d times, want 1 (kinds=%v)", replies, kinds)
	}
	// And the browser must still have SOMETHING to render - suppressing the duplicate
	// must not have suppressed the answer.
	if replies == 0 {
		t.Error("no answer reached the browser at all")
	}
}
