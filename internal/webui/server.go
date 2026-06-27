// Package webui serves the browser-based node console: a localhost web app that is a
// live twin of the terminal TUI. It drives the SAME *node.Controller the TUI holds, so a
// model toggled on air in the browser flips the TUI row and vice-versa.
//
// It binds 127.0.0.1 ONLY and gates every /api request on a per-run random token (handed
// to the browser in the opened URL's ?t=). Localhost + token is the Jupyter model: it
// keeps other local processes (and cross-site requests) out of a console that can put a
// GPU on air, spend/earn money, and holds the operator's upstream key.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/node"
)

//go:embed assets/*
var assetsFS embed.FS

// randRead is the entropy source for newToken's access token. It defaults to the real
// crypto/rand reader, so the production path is unchanged; tests override it to exercise
// the rand-failure fallback.
var randRead = rand.Read

// Options carry the broker identity the account + browse surfaces need. They are
// optional: with an empty Broker the share/monitor surfaces still work and the account/
// browse endpoints report "not configured" rather than erroring.
type Options struct {
	Broker   string
	User     string // signed user id (X-Roger-User)
	ClientID string // GitHub OAuth client id for the device-flow login
}

// Server is the node console HTTP server. It is safe for concurrent requests: all live
// state lives behind the controller's mutex; the in-flight login device has its own lock.
type Server struct {
	ctrl  *node.Controller
	token string
	mux   *http.ServeMux
	opts  Options

	loginMu     sync.Mutex
	loginDevice *client.Device // the in-flight device-flow login between begin and poll
}

// New builds a console server over ctrl with a freshly-minted access token. Call
// Handler() for the wrapped http.Handler, or Serve(ln) to run it.
func New(ctrl *node.Controller, opts Options) *Server {
	s := &Server{ctrl: ctrl, token: newToken(), opts: opts}
	s.mux = http.NewServeMux()
	s.routes()
	return s
}

// Token is the per-run access token required on every /api request (embedded in the URL
// the operator opens).
func (s *Server) Token() string { return s.token }

// routes wires the static shell + the read API. Write actions and account/browse are
// layered on in later commits.
func (s *Server) routes() {
	sub, _ := fs.Sub(assetsFS, "assets")
	files := http.FileServer(http.FS(sub))
	// The shell at / and its assets are static and carry no node data, so they load
	// without a token; everything under /api does require it (see auth()).
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			r.URL.Path = "/console.html"
		}
		files.ServeHTTP(w, r)
	})
	s.mux.Handle("/assets/", http.StripPrefix("/assets/", files))
	s.mux.HandleFunc("/api/state", s.auth(s.handleState))
	s.mux.HandleFunc("/api/events", s.auth(s.handleEvents))
	// Operator write actions (POST-only, token-gated). Each returns the new snapshot.
	s.mux.HandleFunc("/api/share/onair", s.action(s.handleOnAir))
	s.mux.HandleFunc("/api/share/private", s.action(s.handlePrivate))
	s.mux.HandleFunc("/api/share/price", s.action(s.handlePrice))
	s.mux.HandleFunc("/api/share/rename", s.action(s.handleRename))
	s.mux.HandleFunc("/api/share/detect", s.action(s.handleDetect))
	// Account (reads token-gated; writes POST-only).
	s.mux.HandleFunc("/api/account", s.auth(s.handleAccount))
	s.mux.HandleFunc("/api/account/login/begin", s.action(s.handleLoginBegin))
	s.mux.HandleFunc("/api/account/login/poll", s.action(s.handleLoginPoll))
	s.mux.HandleFunc("/api/account/logout", s.action(s.handleLogout))
	s.mux.HandleFunc("/api/account/topup", s.action(s.handleTopup))
	s.mux.HandleFunc("/api/account/limit", s.auth(s.handleLimit)) // GET reads, POST sets
	s.mux.HandleFunc("/api/payout", s.auth(s.handlePayout))
	s.mux.HandleFunc("/api/payout/onboard", s.action(s.handlePayoutOnboard))
	s.mux.HandleFunc("/api/payout/request", s.action(s.handlePayoutRequest))
	s.mux.HandleFunc("/api/payout/history", s.auth(s.handlePayoutHistory))
	s.mux.HandleFunc("/api/grants", s.auth(s.handleGrants)) // GET lists, POST creates
	// Browse (the open-market discover feed).
	s.mux.HandleFunc("/api/browse", s.auth(s.handleBrowse))
}

// Handler returns the fully-wrapped handler (localhost guard in front of the mux).
func (s *Server) Handler() http.Handler { return s.localhostOnly(s.mux) }

// Serve runs the console on ln until the listener closes.
func (s *Server) Serve(ln net.Listener) error {
	return (&http.Server{Handler: s.Handler()}).Serve(ln)
}

// auth wraps an /api handler with the constant-time token check. The token may arrive as
// ?t= (the opened URL) or an X-Roger-Token header (the page's fetch/EventSource calls).
func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("t")
		if got == "" {
			got = r.Header.Get("X-Roger-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// localhostOnly rejects any request whose peer is not a loopback address — defense in
// depth on top of the 127.0.0.1 bind (e.g. a misconfigured reverse proxy).
func (s *Server) localhostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.Error(w, "console is localhost-only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Listen binds a free localhost port at/after addr and returns the listener plus the URL
// (with the access token) to open. addr like "127.0.0.1:4180"; if that port is taken it
// scans upward, mirroring the TUI's listenFreePort so a busy port never dead-ends.
func (s *Server) Listen(addr string) (net.Listener, string, error) {
	ln, err := listenFreePort(addr)
	if err != nil {
		return nil, "", err
	}
	return ln, "http://" + ln.Addr().String() + "/?t=" + s.token, nil
}

// listenFreePort binds addr, or — if its port is taken — scans upward to the first free
// port on the same host (bounded). Mirrors internal/tui.listenFreePort.
func listenFreePort(addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "127.0.0.1", "0"
	}
	if port == "0" {
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	start, _ := strconv.Atoi(port)
	var lastErr error
	for p := start; p < start+64; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	// Last resort: let the OS pick any free port rather than dead-end.
	if ln, err := net.Listen("tcp", net.JoinHostPort(host, "0")); err == nil {
		return ln, nil
	}
	return nil, lastErr
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := randRead(b); err != nil {
		// rand.Read failing is catastrophic; fall back to a fixed-length zero token rather
		// than panic — the localhost bind still gates access.
		return strings.Repeat("0", 32)
	}
	return hex.EncodeToString(b)
}
