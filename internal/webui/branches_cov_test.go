package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/node"
)

// nonFlusherRecorder is an http.ResponseWriter that deliberately does NOT implement
// http.Flusher, so handleEvents takes its "streaming unsupported" 500 branch.
type nonFlusherRecorder struct {
	header http.Header
	code   int
	buf    strings.Builder
}

func (n *nonFlusherRecorder) Header() http.Header {
	if n.header == nil {
		n.header = http.Header{}
	}
	return n.header
}
func (n *nonFlusherRecorder) Write(b []byte) (int, error) { return n.buf.Write(b) }
func (n *nonFlusherRecorder) WriteHeader(code int)        { n.code = code }

// TestEventsStreamingUnsupported covers handleEvents' early 500 when the ResponseWriter
// is not an http.Flusher (cannot stream SSE).
func TestEventsStreamingUnsupported(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})
	rec := &nonFlusherRecorder{}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	s.handleEvents(rec, req)
	if rec.code != http.StatusInternalServerError {
		t.Fatalf("handleEvents without a Flusher = %d, want 500", rec.code)
	}
	if !strings.Contains(rec.buf.String(), "streaming unsupported") {
		t.Fatalf("body = %q, want 'streaming unsupported'", rec.buf.String())
	}
}

// failingFlusher is a Flusher whose Write always fails, so handleEvents' very first
// send() returns false and the handler returns immediately (no ticker loop).
type failingFlusher struct{ header http.Header }

func (f *failingFlusher) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *failingFlusher) Write([]byte) (int, error) { return 0, errors.New("client gone") }
func (f *failingFlusher) WriteHeader(int)           {}
func (f *failingFlusher) Flush()                    {}

// TestEventsInitialSendFails covers handleEvents' "if !send() { return }" branch: the
// first write fails, so the handler bails before starting the ticker.
func TestEventsInitialSendFails(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})
	fw := &failingFlusher{}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	// Returns promptly because the first send() fails; if it hung, the test would time out.
	s.handleEvents(fw, req)
	if ct := fw.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream (headers set before send)", ct)
	}
}

// TestNewTokenRandFailureFallback covers newToken's rand-failure fallback: a 32-char
// all-zero token rather than a panic.
func TestNewTokenRandFailureFallback(t *testing.T) {
	orig := randRead
	defer func() { randRead = orig }()
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	tok := newToken()
	if tok != strings.Repeat("0", 32) {
		t.Fatalf("fallback token = %q, want 32 zeros", tok)
	}
}

// TestLogoutErrorBranch covers handleLogout's 502 path when the local logout fails.
func TestLogoutErrorBranch(t *testing.T) {
	orig := logoutReturn
	defer func() { logoutReturn = orig }()
	logoutReturn = func() error { return errors.New("disk on fire") }

	s := New(node.New(node.Config{}), Options{})
	resp := webDo(t, s, http.MethodPost, "/api/account/logout", "{}")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("logout error = %d, want 502", resp.StatusCode)
	}
}

// TestLoginPollErrorBranch covers handleLoginPoll's 502 path: a device flow is in
// progress (begin succeeded) but the poll itself errors.
func TestLoginPollErrorBranch(t *testing.T) {
	origBegin, origPoll := loginBegin, loginPoll
	defer func() { loginBegin, loginPoll = origBegin, origPoll }()
	loginBegin = func(broker, clientID string) (client.Device, error) {
		return client.Device{VerificationURI: "https://gh/device", UserCode: "EFGH-5678", Handle: "h"}, nil
	}
	loginPoll = func(broker, clientID string, d client.Device) (string, error) {
		return "", errors.New("authorization_pending gave up")
	}
	ctrl := node.New(node.Config{})
	s := New(ctrl, Options{Broker: "http://broker.invalid"})

	rb := webDo(t, s, http.MethodPost, "/api/account/login/begin", "{}")
	rb.Body.Close()
	if rb.StatusCode != http.StatusOK {
		t.Fatalf("login/begin = %d, want 200", rb.StatusCode)
	}
	rp := webDo(t, s, http.MethodPost, "/api/account/login/poll", "{}")
	defer rp.Body.Close()
	if rp.StatusCode != http.StatusBadGateway {
		t.Fatalf("login/poll error = %d, want 502", rp.StatusCode)
	}
	if ctrl.LoggedIn() {
		t.Fatal("a failed poll must NOT mark the controller logged in")
	}
}

// workingBrokerCtrl builds a controller wired to a broker that accepts registration, so
// real on-air sessions start; cap is the soft on-air limit.
func workingBrokerCtrl(t *testing.T, cap int) *node.Controller {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "node_id": "n1"})
	}))
	t.Cleanup(srv.Close)
	c := node.New(node.Config{Broker: srv.URL, Station: "amber-fox", MaxOnAir: cap})
	c.SetRows([]node.ShareRow{
		{Model: "m1", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
		{Model: "m2", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
	})
	return c
}

// failingBrokerCtrl builds a controller whose broker rejects registration, so any
// startLocked (on-air / private) fails — exercising the handlers' Err branches.
func failingBrokerCtrl(t *testing.T) *node.Controller {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "register rejected", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := node.New(node.Config{Broker: srv.URL, Station: "amber-fox"})
	c.SetRows([]node.ShareRow{{Model: "m1", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"}})
	return c
}

// TestOnAirAtLimitMessage covers handleOnAir's AtLimit branch: with a soft cap of 1, a
// second on-air toggle is refused with the limit message and OK=false.
func TestOnAirAtLimitMessage(t *testing.T) {
	c := workingBrokerCtrl(t, 1)
	s := New(c, Options{})

	on := webDo(t, s, http.MethodPost, "/api/share/onair", `{"model":"m1"}`)
	on.Body.Close()
	if c.OnAirCount() != 1 {
		t.Fatalf("first onair: count = %d, want 1", c.OnAirCount())
	}
	resp := webDo(t, s, http.MethodPost, "/api/share/onair", `{"model":"m2"}`)
	defer resp.Body.Close()
	var ar actionResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	if !ar.AtLimit || ar.OK {
		t.Fatalf("at-limit onair: at_limit=%v ok=%v, want true/false", ar.AtLimit, ar.OK)
	}
	if !strings.Contains(ar.Message, "on-air limit") {
		t.Fatalf("message = %q, want on-air limit text", ar.Message)
	}
	if c.OnAirCount() != 1 {
		t.Fatalf("count after refused onair = %d, want still 1", c.OnAirCount())
	}
	c.StopAll()
}

// TestOnAirErrorMessage covers handleOnAir's res.Err branch: registration fails so the
// session cannot start, and the handler reports the could-not-put message.
func TestOnAirErrorMessage(t *testing.T) {
	c := failingBrokerCtrl(t)
	s := New(c, Options{})
	resp := webDo(t, s, http.MethodPost, "/api/share/onair", `{"model":"m1"}`)
	defer resp.Body.Close()
	var ar actionResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	if ar.OK {
		t.Fatalf("onair against a failing broker: ok=true, want false")
	}
	if !strings.Contains(ar.Message, "could not put m1 on air") {
		t.Fatalf("message = %q, want could-not-put text", ar.Message)
	}
	if c.OnAirCount() != 0 {
		t.Fatalf("failed onair left a session: count = %d, want 0", c.OnAirCount())
	}
}

// TestPrivateAtLimitMessage covers handlePrivate's AtLimit branch: logged in, soft cap
// 1, one model on air, then a private toggle on a second (not-on) model is refused.
func TestPrivateAtLimitMessage(t *testing.T) {
	c := workingBrokerCtrl(t, 1)
	c.SetLoggedIn(true)
	s := New(c, Options{})

	on := webDo(t, s, http.MethodPost, "/api/share/onair", `{"model":"m1"}`)
	on.Body.Close()
	resp := webDo(t, s, http.MethodPost, "/api/share/private", `{"model":"m2"}`)
	defer resp.Body.Close()
	var ar actionResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	if !ar.AtLimit || ar.OK {
		t.Fatalf("at-limit private: at_limit=%v ok=%v, want true/false", ar.AtLimit, ar.OK)
	}
	if !strings.Contains(ar.Message, "on-air limit") {
		t.Fatalf("message = %q, want on-air limit text", ar.Message)
	}
	c.StopAll()
}

// TestPrivateErrorMessage covers handlePrivate's res.Err branch: logged in but the
// broker rejects registration, so the private (re)start fails.
func TestPrivateErrorMessage(t *testing.T) {
	c := failingBrokerCtrl(t)
	c.SetLoggedIn(true)
	s := New(c, Options{})
	resp := webDo(t, s, http.MethodPost, "/api/share/private", `{"model":"m1"}`)
	defer resp.Body.Close()
	var ar actionResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	if ar.OK {
		t.Fatalf("private against a failing broker: ok=true, want false")
	}
	if !strings.Contains(ar.Message, "could not change m1 visibility") {
		t.Fatalf("message = %q, want could-not-change text", ar.Message)
	}
}

// TestBrowseWithoutBroker covers handleBrowse's brokerReady 503 guard.
func TestBrowseWithoutBroker(t *testing.T) {
	s := New(node.New(node.Config{}), Options{}) // no broker
	resp := webDo(t, s, http.MethodGet, "/api/browse", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("browse without broker = %d, want 503", resp.StatusCode)
	}
}

// TestAccountSyncsLoggedIn covers handleAccount's bal.LoggedIn branch: a broker that
// reports the wallet as logged in raises the shared controller's login state.
func TestAccountSyncsLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/balance" {
			_, _ = w.Write([]byte(`{"balance":12.5,"logged_in":true,"monthly_cap":100,"monthly_spend":3}`))
			return
		}
		_, _ = w.Write([]byte(`{"connected":false,"status":"none"}`))
	}))
	t.Cleanup(broker.Close)

	ctrl := node.New(node.Config{})
	if ctrl.LoggedIn() {
		t.Fatal("precondition: controller should start logged out")
	}
	s := New(ctrl, Options{Broker: broker.URL, User: "u_gh_1"})

	resp := webDo(t, s, http.MethodGet, "/api/account", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("account = %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["balance"] != 12.5 {
		t.Fatalf("balance = %v, want 12.5", out["balance"])
	}
	if out["logged_in"] != true {
		t.Fatalf("logged_in = %v, want true", out["logged_in"])
	}
	if !ctrl.LoggedIn() {
		t.Fatal("handleAccount should have raised the controller's login state from the broker truth")
	}
}

// TestLimitGrantsBadBody covers handleLimit's and handleGrants' decode-fail 400 on a
// malformed POST body (the broker is reached only after a clean decode).
func TestLimitGrantsBadBody(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := New(node.New(node.Config{}), Options{Broker: fakeBrokerAll(t), User: "u"})

	rl := webDo(t, s, http.MethodPost, "/api/account/limit", `{not json`)
	defer rl.Body.Close()
	if rl.StatusCode != http.StatusBadRequest {
		t.Fatalf("limit POST bad body = %d, want 400", rl.StatusCode)
	}
	rg := webDo(t, s, http.MethodPost, "/api/grants", `{not json`)
	defer rg.Body.Close()
	if rg.StatusCode != http.StatusBadRequest {
		t.Fatalf("grants POST bad body = %d, want 400", rg.StatusCode)
	}
}

// TestLocalhostOnlyPortlessRemoteAddr covers localhostOnly's SplitHostPort-error
// fallback: a RemoteAddr with no port is treated as the bare host (loopback -> pass).
func TestLocalhostOnlyPortlessRemoteAddr(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})
	var reached bool
	h := s.localhostOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1" // no :port -> SplitHostPort errors, host falls back to RemoteAddr
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !reached || w.Code != http.StatusOK {
		t.Fatalf("portless loopback addr: reached=%v code=%d, want pass/200", reached, w.Code)
	}
}

// TestListenBadAddrFallsBack covers listenFreePort's SplitHostPort-error path: an addr
// with no port still binds (defaults host/port) and returns a localhost token URL.
func TestListenBadAddrFallsBack(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})
	ln, url, err := s.Listen("no-port-here")
	if err != nil {
		t.Fatalf("Listen with a portless addr should fall back, got %v", err)
	}
	defer ln.Close()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("Listen url = %q, want a 127.0.0.1 URL", url)
	}
	if !strings.Contains(url, "?t="+s.Token()) {
		t.Fatalf("Listen url = %q, want it to carry the token", url)
	}
}
