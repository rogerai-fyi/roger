package webui

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/node"
)

// fakeBrokerAll answers every broker endpoint the account/payout/grant proxies call
// with one permissive JSON blob (each client decoder reads only its own field), so the
// webui proxy handlers can be driven to their success path.
func fakeBrokerAll(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"url":"https://pay.example/checkout",
			"cap":0,"monthly_cap":0,"spend":0,
			"connected":false,"status":"none",
			"payout":{"id":1,"amount":0,"state":"pending"},
			"payouts":[],
			"grants":[],
			"grant":{"id":"grant_1","name":"petlings"},
			"secret":"rog-grant_abc123",
			"device_code":"dev123","user_code":"WXYZ-1234",
			"verification_uri":"https://github.com/login/device","interval":1,"expires_in":900
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// webGet/webPost drive a console endpoint with the access token.
func webDo(t *testing.T, s *Server, method, path string, body string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, srv.URL+path+"?t="+s.Token(), rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// TestAccountProxyHandlers covers the broker-proxying account/payout/grant handlers'
// success paths against a fake broker.
func TestAccountProxyHandlers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // signed client calls mint a node key here
	broker := fakeBrokerAll(t)
	s := New(node.New(node.Config{}), Options{Broker: broker, User: "u_gh_1"})

	cases := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/account/topup", `{"usd":10}`},
		{http.MethodGet, "/api/account/limit", ""},
		{http.MethodPost, "/api/account/limit", `{"cap":50}`},
		{http.MethodGet, "/api/payout", ""},
		{http.MethodPost, "/api/payout/onboard", "{}"},
		{http.MethodPost, "/api/payout/request", "{}"},
		{http.MethodGet, "/api/payout/history", ""},
		{http.MethodGet, "/api/grants", ""},
	}
	for _, c := range cases {
		resp := webDo(t, s, c.method, c.path, c.body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s = %d, want 200", c.method, c.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestAccountHandlersWithoutBroker covers the brokerReady 503 guard on every proxy
// handler when no broker is configured.
func TestAccountHandlersWithoutBroker(t *testing.T) {
	s := New(node.New(node.Config{}), Options{}) // no broker
	for _, p := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/account/topup"},
		{http.MethodGet, "/api/account/limit"},
		{http.MethodGet, "/api/payout"},
		{http.MethodPost, "/api/payout/onboard"},
		{http.MethodPost, "/api/payout/request"},
		{http.MethodGet, "/api/payout/history"},
		{http.MethodGet, "/api/grants"},
		{http.MethodPost, "/api/account/login/begin"},
	} {
		resp := webDo(t, s, p.method, p.path, "")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s without broker = %d, want 503", p.method, p.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestLocalActionHandlers covers the handlers that act on the local node (no broker):
// logout, private toggle, and detect.
func TestLocalActionHandlers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := New(node.New(node.Config{}), Options{})

	// logout: no local auth file -> clean ok.
	r1 := webDo(t, s, http.MethodPost, "/api/account/logout", "{}")
	if r1.StatusCode != http.StatusOK {
		t.Errorf("logout = %d, want 200", r1.StatusCode)
	}
	r1.Body.Close()

	// private toggle on an unknown model -> 200 (no-op result, public again).
	r2 := webDo(t, s, http.MethodPost, "/api/share/private", `{"model":"m1"}`)
	if r2.StatusCode != http.StatusOK {
		t.Errorf("private = %d, want 200", r2.StatusCode)
	}
	r2.Body.Close()

	// private with no model -> 400.
	r3 := webDo(t, s, http.MethodPost, "/api/share/private", `{}`)
	if r3.StatusCode != http.StatusBadRequest {
		t.Errorf("private no-model = %d, want 400", r3.StatusCode)
	}
	r3.Body.Close()

	// detect with no upstream -> 200 ("nothing detected").
	r4 := webDo(t, s, http.MethodPost, "/api/share/detect", `{}`)
	if r4.StatusCode != http.StatusOK {
		t.Errorf("detect = %d, want 200", r4.StatusCode)
	}
	r4.Body.Close()
}

// TestAccountAndGrantsCreate covers handleAccount (balance + payout snapshot) and the
// grants POST (create) path against the fake broker.
func TestAccountAndGrantsCreate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := New(node.New(node.Config{}), Options{Broker: fakeBrokerAll(t), User: "u_gh_1"})

	r := webDo(t, s, http.MethodGet, "/api/account", "")
	if r.StatusCode != http.StatusOK {
		t.Errorf("account = %d, want 200", r.StatusCode)
	}
	r.Body.Close()

	rg := webDo(t, s, http.MethodPost, "/api/grants", `{"name":"petlings","free":true}`)
	if rg.StatusCode != http.StatusOK {
		t.Errorf("grants create = %d, want 200", rg.StatusCode)
	}
	rg.Body.Close()

	// grants create with no name -> 400.
	rb := webDo(t, s, http.MethodPost, "/api/grants", `{}`)
	if rb.StatusCode != http.StatusBadRequest {
		t.Errorf("grants create no-name = %d, want 400", rb.StatusCode)
	}
	rb.Body.Close()
}

// TestShareActionHandlers covers the local share actions: onair, price, rename — each
// success + the missing-field 400.
func TestShareActionHandlers(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})
	cases := []struct {
		path, okBody, badBody string
	}{
		{"/api/share/onair", `{"model":"m1"}`, `{}`},
		{"/api/share/price", `{"model":"m1","out":2}`, `{}`},
		{"/api/share/rename", `{"station":"amber-fox"}`, `{}`},
	}
	for _, c := range cases {
		ok := webDo(t, s, http.MethodPost, c.path, c.okBody)
		if ok.StatusCode != http.StatusOK {
			t.Errorf("%s ok = %d, want 200", c.path, ok.StatusCode)
		}
		ok.Body.Close()
		bad := webDo(t, s, http.MethodPost, c.path, c.badBody)
		if bad.StatusCode != http.StatusBadRequest {
			t.Errorf("%s bad = %d, want 400", c.path, bad.StatusCode)
		}
		bad.Body.Close()
	}
}

// TestServeAndListen covers Listen (bind a free localhost port + token URL) and Serve
// (run the console over a listener, answer one request, shut down cleanly).
func TestServeAndListen(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})

	ln, url, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if !strings.Contains(url, "/?t=") {
		t.Errorf("Listen url missing token: %q", url)
	}

	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ln) }()

	// The static shell needs no token; a live request proves Serve is up.
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET / over Serve: %v", err)
	}
	resp.Body.Close()

	_ = ln.Close() // Serve returns once the listener closes
	if err := <-errc; err != nil && !strings.Contains(err.Error(), "closed") {
		t.Errorf("Serve returned %v, want a clean close", err)
	}
	var _ net.Listener = ln
}

// TestOnAirPrivateWithRows drives the local share actions against a controller that has
// a model catalog + a fake broker, covering the on-air/off-air and private message
// branches (free, went-off, login-gated priced/private).
func TestOnAirPrivateWithRows(t *testing.T) {
	nodeBroker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer nodeBroker.Close()

	ctrl := node.New(node.Config{Station: "amber-fox", Broker: nodeBroker.URL})
	ctrl.SetRows([]node.ShareRow{
		{Model: "free-1", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
		{Model: "paid", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
	})
	ctrl.SetPricing("paid", node.Pricing{Out: 2})
	s := New(ctrl, Options{})

	// free model on air -> off air (covers the FREE + went-off messages).
	on := webDo(t, s, http.MethodPost, "/api/share/onair", `{"model":"free-1"}`)
	on.Body.Close()
	off := webDo(t, s, http.MethodPost, "/api/share/onair", `{"model":"free-1"}`)
	off.Body.Close()

	// priced model without login -> login-needed branch.
	pr := webDo(t, s, http.MethodPost, "/api/share/onair", `{"model":"paid"}`)
	pr.Body.Close()

	// private toggle login-gated, then logged-in flips it private.
	pv := webDo(t, s, http.MethodPost, "/api/share/private", `{"model":"free-1"}`)
	pv.Body.Close()
	ctrl.SetLoggedIn(true)
	pv2 := webDo(t, s, http.MethodPost, "/api/share/private", `{"model":"free-1"}`)
	if pv2.StatusCode != http.StatusOK {
		t.Errorf("private toggle = %d, want 200", pv2.StatusCode)
	}
	pv2.Body.Close()
}

// TestEventsTickerBranch covers handleEvents' periodic-push loop: with a tiny interval,
// the stream emits the initial snapshot AND at least one ticker-driven update before the
// client disconnects. Also covers decode's empty-body path via a no-body detect.
func TestEventsTickerBranch(t *testing.T) {
	orig := eventInterval
	eventInterval = 10 * time.Millisecond
	defer func() { eventInterval = orig }()

	s := New(node.New(node.Config{}), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events?t="+s.Token(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer resp.Body.Close()

	frames := 0
	sc := bufio.NewScanner(resp.Body)
	done := make(chan int, 1)
	go func() {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				frames++
				if frames >= 2 { // initial + at least one ticker push
					break
				}
			}
		}
		done <- frames
	}()
	select {
	case n := <-done:
		if n < 2 {
			t.Errorf("SSE frames = %d, want >=2 (initial + ticker)", n)
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for SSE ticker frames")
	}
	cancel()

	// decode's empty-body branch: a no-body detect is accepted (returns true).
	r := webDo(t, s, http.MethodPost, "/api/share/detect", "")
	if r.StatusCode != http.StatusOK {
		t.Errorf("detect with empty body = %d, want 200", r.StatusCode)
	}
	r.Body.Close()
}

// TestLoginFlowStubbed covers handleLoginBegin + handleLoginPoll success by stubbing
// the device-flow client calls (which otherwise reach github.com): begin returns the
// code and stashes the device; poll authorizes and marks the controller logged in.
func TestLoginFlowStubbed(t *testing.T) {
	origBegin, origPoll := loginBegin, loginPoll
	defer func() { loginBegin, loginPoll = origBegin, origPoll }()
	loginBegin = func(broker, clientID string) (client.Device, error) {
		return client.Device{VerificationURI: "https://gh/device", UserCode: "ABCD-1234", Handle: "h"}, nil
	}
	loginPoll = func(broker, clientID string, d client.Device) (string, error) {
		return "octocat", nil
	}

	ctrl := node.New(node.Config{})
	s := New(ctrl, Options{Broker: "http://broker.invalid"})

	rb := webDo(t, s, http.MethodPost, "/api/account/login/begin", "{}")
	if rb.StatusCode != http.StatusOK {
		t.Fatalf("login/begin = %d, want 200", rb.StatusCode)
	}
	rb.Body.Close()

	rp := webDo(t, s, http.MethodPost, "/api/account/login/poll", "{}")
	if rp.StatusCode != http.StatusOK {
		t.Fatalf("login/poll = %d, want 200", rp.StatusCode)
	}
	rp.Body.Close()
	if !ctrl.LoggedIn() {
		t.Error("a successful poll should mark the controller logged in")
	}
}

// TestTopupValidation covers handleTopup's usd<=0 rejection.
func TestTopupValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := New(node.New(node.Config{}), Options{Broker: fakeBrokerAll(t), User: "u"})
	resp := webDo(t, s, http.MethodPost, "/api/account/topup", `{"usd":0}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("topup usd=0 = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestListenFreePortScansUp covers listenFreePort's scan-upward path: with a port
// already taken, Listen binds the next free one rather than failing.
func TestListenFreePortScansUp(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	addr := taken.Addr().String() // an address whose port is in use

	s := New(node.New(node.Config{}), Options{})
	ln, url, err := s.Listen(addr)
	if err != nil {
		t.Fatalf("Listen should scan up from a taken port, got %v", err)
	}
	defer ln.Close()
	if ln.Addr().String() == addr {
		t.Error("Listen returned the already-taken port")
	}
	if !strings.Contains(url, "127.0.0.1:") {
		t.Errorf("Listen url = %q, want a localhost url", url)
	}
}

// TestLoginPollNoFlow covers handleLoginPoll's "no login in progress" 400 path.
func TestLoginPollNoFlow(t *testing.T) {
	s := New(node.New(node.Config{}), Options{Broker: "http://127.0.0.1:0"})
	resp := webDo(t, s, http.MethodPost, "/api/account/login/poll", "{}")
	// No in-flight device flow -> 400 bad request.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("login/poll with no flow = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestProxyHandlerErrorBranches drives the broker-proxy handlers against a broker that
// fails every call, covering their 502 (bad gateway) error branches.
func TestProxyHandlerErrorBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	errBroker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "broker boom", http.StatusInternalServerError)
	}))
	t.Cleanup(errBroker.Close)
	s := New(node.New(node.Config{}), Options{Broker: errBroker.URL, User: "u_gh_1"})

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/account/topup", `{"usd":10}`},
		{http.MethodGet, "/api/account/limit", ""},
		{http.MethodPost, "/api/account/limit", `{"cap":5}`},
		{http.MethodGet, "/api/payout", ""},
		{http.MethodPost, "/api/payout/onboard", "{}"},
		{http.MethodPost, "/api/payout/request", "{}"},
		{http.MethodGet, "/api/payout/history", ""},
		{http.MethodPost, "/api/grants", `{"name":"x"}`},
		{http.MethodGet, "/api/browse", ""},
		{http.MethodPost, "/api/account/login/begin", "{}"}, // no clientID -> LoginBegin errors
	} {
		resp := webDo(t, s, c.method, c.path, c.body)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s %s against a failing broker = 200, want an error status", c.method, c.path)
		}
		resp.Body.Close()
	}
}

// TestLocalhostOnly covers the loopback guard middleware: a loopback peer passes; a
// non-loopback peer is rejected 403.
func TestLocalhostOnly(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})
	var reached bool
	h := s.localhostOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))

	// Loopback -> passes.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !reached {
		t.Error("loopback peer should pass localhostOnly")
	}

	// Non-loopback -> 403.
	reached = false
	w := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "8.8.8.8:5555"
	h.ServeHTTP(w, req2)
	if reached || w.Code != http.StatusForbidden {
		t.Errorf("non-loopback peer = reached %v / code %d, want blocked 403", reached, w.Code)
	}
}

// TestDecodeBadJSON covers decode()'s failure branch via an action handler: a malformed
// body is a 400.
func TestDecodeBadJSON(t *testing.T) {
	s := New(node.New(node.Config{}), Options{})
	resp := webDo(t, s, http.MethodPost, "/api/share/onair", `{not json`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed action body = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
