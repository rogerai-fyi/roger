package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// chat_test.go - the console's CHAT tab (founder 2026-08-20).

func chatPost(t *testing.T, s *Server, body string) (*http.Response, chatResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555" // the console binds loopback only (localhostOnly)
	req.Header.Set("X-Roger-Token", s.Token())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out chatResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Result(), out
}

// Chat spends money on the operator's key, so it is a WRITE even though it mutates no
// node state: no token, no turn - and never reachable by a GET, which would be
// CSRF-triggerable from any page the operator happens to have open.
func TestChatRequiresTokenAndPost(t *testing.T) {
	s := New(testCtrl(), Options{Broker: "http://127.0.0.1:1"})

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("untokened chat = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/chat?t="+s.Token(), nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET chat = %d, want 405", rec.Code)
	}
}

// Every refusal has to say WHICH thing was missing. Collapsing these to one "bad
// request" is how a user ends up retrying the case that was never going to work.
func TestChatRefusalsAreSpecific(t *testing.T) {
	s := New(testCtrl(), Options{Broker: "http://127.0.0.1:1"})
	cases := []struct{ body, want string }{
		{`{"messages":[{"role":"user","content":"hi"}]}`, "pick a model"},
		{`{"model":"m1","messages":[]}`, "nothing to send"},
		{`{"model":"m1","messages":[` + strings.Repeat(`{"role":"user","content":"x"},`, chatMaxTurns) + `{"role":"user","content":"x"}]}`, "too long"},
		{`{not json`, "malformed"},
	}
	for _, c := range cases {
		res, out := chatPost(t, s, c.body)
		if res.StatusCode < 400 {
			t.Errorf("body %.30s: status %d, want an error", c.body, res.StatusCode)
		}
		if !strings.Contains(out.Error, c.want) {
			t.Errorf("body %.30s: error %q, want it to mention %q", c.body, out.Error, c.want)
		}
		// The shared console api() helper reads .message; without the mirror the real
		// cause would be dropped and the browser would show a bare status line.
		if out.Message != out.Error {
			t.Errorf("body %.30s: message %q must mirror error %q", c.body, out.Message, out.Error)
		}
	}
}

// With no broker there is nothing to relay through, and saying so beats a transport
// error the operator has to decode.
func TestChatWithoutBrokerSaysSo(t *testing.T) {
	s := New(testCtrl(), Options{})
	res, out := chatPost(t, s, `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", res.StatusCode)
	}
	if !strings.Contains(out.Error, "no broker") {
		t.Errorf("error %q should name the missing broker", out.Error)
	}
}

// A relay failure is surfaced VERBATIM, exactly as the TUI does it.
func TestChatRelayFailureIsSurfaced(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"no node offers that model"}}`)
	}))
	defer up.Close()
	s := New(testCtrl(), Options{Broker: up.URL, User: "u"})
	res, out := chatPost(t, s, `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d, want 502", res.StatusCode)
	}
	if out.OK || out.Error == "" {
		t.Fatalf("a failed relay must not report ok: %+v", out)
	}
}

// The happy path carries the RECEIPT. That is the console's whole claim over a plain
// chat window: which machine served the turn, and what it cost.
func TestChatAnswerCarriesItsReceipt(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The whole conversation must reach the broker, not just the last line, or
		// every answer would arrive with no memory of the question before it.
		if !strings.Contains(string(body), "first question") {
			t.Errorf("history did not reach the broker: %s", body)
		}
		w.Header().Set("X-RogerAI-Provider", "amber-fox")
		w.Header().Set("X-RogerAI-TPS", "42")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"the answer"}}]}`)
	}))
	defer up.Close()
	s := New(testCtrl(), Options{Broker: up.URL, User: "u"})
	res, out := chatPost(t, s, `{"model":"m1","messages":[
		{"role":"user","content":"first question"},
		{"role":"assistant","content":"first answer"},
		{"role":"user","content":"follow up"}]}`)
	if res.StatusCode != http.StatusOK || !out.OK {
		t.Fatalf("status %d out %+v", res.StatusCode, out)
	}
	if out.Reply != "the answer" {
		t.Errorf("reply = %q", out.Reply)
	}
	if out.Provider != "amber-fox" {
		t.Errorf("the receipt must name the serving machine, got %q", out.Provider)
	}
	if out.TPS != 42 {
		t.Errorf("the receipt must carry throughput, got %v", out.TPS)
	}
	// Billed token counts ride the SIGNED receipt (X-RogerAI-Receipt), not a plain
	// header, so a stub broker that does not sign one legitimately reports zero. That
	// is the honest behaviour and the console depends on it: chatReceipt() omits a
	// zero rather than printing it, because a printed 0 reads as a claim that the turn
	// cost nothing. This asserts the pass-through does not invent a number.
	if out.TokensIn != 0 || out.TokensOut != 0 {
		t.Errorf("unsigned turn must not report billed tokens, got %d/%d", out.TokensIn, out.TokensOut)
	}
}

// The console must never print a zero as though it were a measurement.
func TestChatReceiptOmitsMissingNumbers(t *testing.T) {
	js, _ := os.ReadFile("assets/console.js")
	src := string(js)
	i := strings.Index(src, "function chatReceipt")
	if i < 0 {
		t.Fatal("chatReceipt not found")
	}
	block := src[i : i+900]
	for _, guard := range []string{"if (r.cost)", "if (r.tps)", "if (r.provider)"} {
		if !strings.Contains(block, guard) {
			t.Errorf("the receipt must guard %s - a printed zero reads as a claim", guard)
		}
	}
}

// The console holds the operator's upstream key. A model's reply is untrusted input,
// so the chat surface must never hand it to innerHTML.
func TestChatUIDoesNotInjectReplyAsHTML(t *testing.T) {
	js, err := os.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	start := strings.Index(src, "/* CHAT ---")
	end := strings.Index(src, "/* TABS ---")
	if start < 0 || end < 0 || end < start {
		t.Fatal("could not isolate the chat block in console.js")
	}
	for _, banned := range []string{"innerHTML =", "insertAdjacentHTML", "outerHTML"} {
		if strings.Contains(src[start:end], banned) {
			t.Errorf("the chat block must not use %s - a reply is untrusted input", banned)
		}
	}
}

// The CHAT tab has to exist in the shell, be reachable by hash, and carry the
// Spectrum tokens it paints the carrier with.
func TestChatTabIsWiredIntoTheShell(t *testing.T) {
	html, _ := os.ReadFile("assets/console.html")
	if !strings.Contains(string(html), `data-tab="chat"`) || !strings.Contains(string(html), `id="panel-chat"`) {
		t.Error("the shell needs a CHAT tab and its panel")
	}
	js, _ := os.ReadFile("assets/console.js")
	if !strings.Contains(string(js), "tabFromHash") {
		t.Error("tabs must be deep-linkable by hash")
	}
	tokens, _ := os.ReadFile("assets/tokens.css")
	for _, tier := range []string{"--tier-pico: #b23a2a", "--tier-exa:  #5b3fbf"} {
		if !strings.Contains(string(tokens), tier) {
			t.Errorf("the console palette must carry the Wave Spectrum (%s) - it is the same ladder the site and the TUI paint", tier)
		}
	}
}
