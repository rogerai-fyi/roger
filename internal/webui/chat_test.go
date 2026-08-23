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

// AMENDED 2026-08-21: the chat tab is AGENTIC now, so a turn is many relayed calls -
// one per model step, plus any subagent's. The old per-call receipt renderer would have
// shown one call's numbers as the turn's cost, which understates: the one direction
// this console must never round. It is gone until the server streams the rollup the
// harness already computes (Loop.TurnReceipt).
//
// The guarantee it protected is still worth holding, so it moves up a level: the page
// must not display a turn cost it cannot substantiate.
func TestConsoleDoesNotClaimAnUnsubstantiatedTurnCost(t *testing.T) {
	js, err := os.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if strings.Contains(src, "function chatReceipt") {
		t.Error("the per-CALL receipt renderer must not return without the turn rollup behind it")
	}
	// The reason is written down where the next reader will look for it.
	if !strings.Contains(src, "would understate") {
		t.Error("the removal must carry its reason, or someone re-adds the wrong number")
	}
	// The /api/chat receipt fields still exist server-side and are still tested; this is
	// about what the PAGE claims.
	if strings.Contains(src, `"$" + Number(r.cost)`) {
		t.Error("a per-call cost must not be presented as the turn's cost")
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

// ── THE PICKER: TWO GROUPS, ONLY WHAT IS ONLINE ──────────────────────────────
// Founder 2026-08-22: "it should use the local models or list them in a category as
// local, and in another category showing the open market models, and it should only show
// the ones online, it should maybe show more detail."

// AMENDED 2026-08-23: the comment this replaces claimed "the model list is the bands this
// node can actually reach, so the picker can never offer something there is no way to send
// to". That was FALSE - the list was /api/browse verbatim, `online` and all, which is how
// picking grok-4.3 returned a 504 every time. A comment that is a promise has to be one
// the code keeps, so the promise is now a test.
func TestChatPickerOnlyOffersWhatIsOnline(t *testing.T) {
	js := chatJS(t)
	if strings.Contains(js, "so the picker can\n  // never offer something there is no way to send to") {
		t.Error("the false 'can never offer something unreachable' claim must not come back unearned")
	}
	// The market half is filtered on the field the feed has carried all along.
	if !strings.Contains(js, "o.online === true") {
		t.Error("the market half of the picker must filter on `online` - an off-air band is a 504 with extra steps")
	}
	// The local half must be routable AND able to hold a conversation.
	if !strings.Contains(js, "return r.model && r.upstream && !chatIsVoice(r.modality)") {
		t.Error("a local row with no upstream (nothing to send to) or a voice modality (cannot chat) must not be offered")
	}
	if !strings.Contains(js, `modality === "tts" || modality === "stt"`) {
		t.Error("voice models must be excluded from a CHAT picker on both sides")
	}
}

// Two groups, named for the thing that actually differs about them: where the turn goes.
func TestChatPickerGroupsLocalAndMarket(t *testing.T) {
	js := chatJS(t)
	if !strings.Contains(js, `el("optgroup")`) {
		t.Error("the picker must use <optgroup> - two flat lists in one dropdown is not two categories")
	}
	for _, want := range []string{
		"LOCAL · this machine · direct, not through the broker",
		"OPEN MARKET · relayed through the broker",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the picker needs the group heading %q", want)
		}
	}
	// A LOCAL pick has to be ROUTED locally, or the group is a lie that 504s. The flag
	// rides the request; the endpoint and its key are resolved server-side (agent.go).
	if !strings.Contains(js, "local: !!entry.local") {
		t.Error("the turn must tell the server which group the pick came from")
	}
	if strings.Contains(js, "upstream: e.upstream, key:") {
		t.Error("the page must never carry an upstream KEY - the server resolves it")
	}
}

// An empty picker explains itself. Filtering to "only online" can empty the list, and a
// blank dropdown beside "pick a band first" is an instruction the user cannot follow.
func TestChatPickerSaysWhyItIsEmpty(t *testing.T) {
	js := chatJS(t)
	if !strings.Contains(js, "function chatEmptyReason") {
		t.Fatal("an empty picker must carry a reason")
	}
	i := strings.Index(js, "function chatEmptyReason")
	block := js[i : i+900]
	for _, want := range []string{"off air right now", "detection is still running"} {
		if !strings.Contains(block, want) {
			t.Errorf("the empty reason must distinguish the cases (%q missing)", want)
		}
	}
}

// THE HONESTY RAIL, in the picker. Absent renders as absent; a printed zero must never
// read as a measurement. `ttft_ms: 0` means nobody timed the band, not that it answered
// instantly, and `ctx_estimated` means the window is a default rather than a detection.
func TestChatPickerNeverPrintsAnUnmeasuredZero(t *testing.T) {
	js := chatJS(t)
	i := strings.Index(js, "function chatBandDetail")
	if i < 0 {
		t.Fatal("chatBandDetail not found")
	}
	block := js[i : i+1600]
	for _, guarded := range []string{
		`slot("tok/s", e.tps ? Math.round(e.tps) : "—")`,
		`slot("ttft", e.ttft ? Math.round(e.ttft) + "ms" : "—")`,
	} {
		if !strings.Contains(block, guarded) {
			t.Errorf("an unmeasured number must render as the absence glyph, not as 0: missing %s", guarded)
		}
	}
	// The estimated-context marker is the same ≈ the SHARE table and `roger detect` use.
	if !strings.Contains(js, `Math.round(ctx / 1024) + "k" + (estimated ? "≈" : "")`) {
		t.Error("an ESTIMATED context window must be marked as one, not printed as a detected number")
	}
	// A local model has no price, so it must never be given one.
	if strings.Contains(js, `if (entry.local) {`) {
		lo := strings.Index(js, "function chatOptionLabel")
		lb := js[lo : lo+900]
		local := lb[strings.Index(lb, "if (entry.local) {"):strings.Index(lb, "} else {")]
		// The FIELDS, not the word: the branch's comment says why there is no price here.
		if strings.Contains(local, "entry.priceOut") || strings.Contains(local, "entry.free") {
			t.Error("a LOCAL row must never show a price - there is none, and printing one is a false claim about money")
		}
	}
}

// A failed turn gets a remedy, not just a cause. This is the founder's dead end: "the
// station returned status 504 with no reply" told them nothing to do.
func TestChatErrorCarriesItsRemedy(t *testing.T) {
	js := chatJS(t)
	if !strings.Contains(js, "function chatAppendError") {
		t.Fatal("a failed turn must render cause AND remedy")
	}
	if !strings.Contains(js, "chatAppendError(e.text, e.hint)") {
		t.Error("the streamed error event's hint must reach the page")
	}
	css, err := os.ReadFile("assets/console.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".chat-err-hint") {
		t.Error("the remedy line needs its own style, or it reads as more of the error")
	}
	// Design tokens only - the page has to work in both themes.
	i := strings.Index(string(css), ".chat-err-hint")
	if strings.Contains(string(css)[i:i+200], "#") {
		t.Error("no hardcoded colors: the console paints from tokens so both themes work")
	}
}
