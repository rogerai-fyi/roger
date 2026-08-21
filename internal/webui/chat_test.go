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

// ── LARGE PASTES, in the console ─────────────────────────────────────────────
// The same bug the TUI had (founder 2026-08-21): a browser textarea auto-grows, so a
// 300-line paste fills 40% of the viewport and pushes what you were typing off screen.

func chatJS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The THRESHOLDS must match the TUI's. Two surfaces of one product that disagree about
// what counts as a big paste is a worse bug than either getting the number wrong, and
// the numbers live in two languages so nothing but a test can hold them together.
func TestConsolePasteThresholdsMatchTheTUI(t *testing.T) {
	js := chatJS(t)
	for _, want := range []string{
		"CHAT_PASTE_MIN_LINES = 4",
		"CHAT_PASTE_MIN_BYTES = 400",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the console must use the TUI's threshold (%s)", want)
		}
	}
	// Cross-check against the Go side so a change there fails HERE too.
	tui, err := os.ReadFile("../tui/paste.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pasteMinLines = 4", "pasteMinBytes = 400"} {
		if !strings.Contains(string(tui), want) {
			t.Errorf("the TUI threshold moved (%s) - the console's copy in console.js must move with it", want)
		}
	}
}

// A small paste must land as itself: a URL or a short snippet is something you want to
// SEE before sending.
func TestConsoleSmallPasteIsNotIntercepted(t *testing.T) {
	js := chatJS(t)
	i := strings.Index(js, "function chatOnPaste")
	if i < 0 {
		t.Fatal("chatOnPaste not found")
	}
	block := js[i : i+600]
	if !strings.Contains(block, "if (!text || !chatBigPaste(text)) return") {
		t.Error("small pastes must fall through untouched, before preventDefault")
	}
	// preventDefault must come AFTER that guard, or every paste would be intercepted.
	if strings.Index(block, "preventDefault") < strings.Index(block, "chatBigPaste") {
		t.Error("the size guard must run before preventDefault")
	}
}

// Held text is expanded before the message is sent, and a chip with nothing behind it
// survives verbatim - substituting there would silently delete what the user wrote.
func TestConsoleExpandsHeldPastesOnSend(t *testing.T) {
	js := chatJS(t)
	if !strings.Contains(js, "var text = chatExpandPastes(input.value") {
		t.Error("the send path must expand held pastes, or the model gets a placeholder")
	}
	i := strings.Index(js, "function chatExpandPastes")
	block := js[i : i+500]
	if !strings.Contains(block, "chatPastes[i - 1] : ref") {
		t.Error("an unbacked chip must be returned as written")
	}
	if !strings.Contains(js, "chatPastes = []") {
		t.Error("held blocks must be released once sent")
	}
}

// The console tells the user what it is holding. The chip inside the textarea is text
// they can edit or delete; the note is what will actually be sent.
func TestConsoleShowsWhatItIsHolding(t *testing.T) {
	html, err := os.ReadFile("assets/console.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `id="chat-held"`) {
		t.Error("the composer needs somewhere to report held blocks")
	}
	if !strings.Contains(chatJS(t), "function chatRenderHeld") {
		t.Error("...and something to fill it")
	}
}
