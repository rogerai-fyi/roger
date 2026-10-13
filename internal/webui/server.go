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
	"rogerai.fm/roger/v6/internal/ownerjoin"
	"strings"
	"sync"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/netbind"
	"rogerai.fm/roger/v6/internal/node"
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
	// ReadLimits / WriteLimit expose the operator's PER-BAND spend caps to the console.
	// They are wired to the SAME store the TUI's [4] CONFIG edits: two copies would let
	// the browser and the terminal disagree about what the operator is willing to pay,
	// and the one that loses is whichever wrote first. Nil = the node cannot edit them
	// (the console then shows the table as unavailable rather than empty, which would
	// read as "you have no caps").
	ReadLimits func() map[string]SpendLimit
	WriteLimit func(model string, l SpendLimit)
	// Edge is this machine's Edge, for the EDGE tab (edge.go): the same fleet, candidates,
	// adopt path and session ledger the TUI's [3] EDGE screen holds. Zero = no Edge host.
	Edge EdgeHooks
	// Models is the MODELS tab (models.go): `roger model` from the browser, through the same code.
	Models ModelHooks
	// LANDial is the merged LAN dial in LAN mode (the same function the TUI's dial reads), nil
	// otherwise. With it set, BROWSE lists the LAN and never asks the broker.
	LANDial func() client.LANDial
	// Owner is the owner's side of joining (towers.go), the same hooks the TUI uses. Nil = the
	// console shows no Towers.
	Owner *ownerjoin.Hooks
	// WelcomeSeen / SaveWelcomeSeen are the one-time welcome card's flag, the same welcome_seen
	// the TUI's card writes (features/onboarding/welcome_card.feature): seeing it in either
	// front-end counts for both. Nil = the console shows no card.
	WelcomeSeen     func() bool
	SaveWelcomeSeen func() error
}

// Server is the node console HTTP server. It is safe for concurrent requests: all live
// state lives behind the controller's mutex; the in-flight login device has its own lock.
type Server struct {
	ctrl  *node.Controller
	token string
	mux   *http.ServeMux
	opts  Options

	// joinPrompt is the empty state's ask by address in flight, if any (edge_joinprompt.go).
	joinPrompt edgeJoinPromptRun

	loginMu     sync.Mutex
	loginDevice *client.Device // the in-flight device-flow login between begin and poll

	// agentSess is the console's own agent conversation (agent.go). Its own, not the
	// TUI's: sharing one would mean a write approved in the browser waits for a y/N at
	// a terminal nobody may be sitting at.
	agentSess agentSession
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
	s.mux.HandleFunc("/api/welcome", s.auth(s.handleWelcome))
	s.mux.HandleFunc("/api/welcome/seen", s.action(s.handleWelcomeSeen))
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
	s.mux.HandleFunc("/api/chat", s.action(s.handleChat))
	s.mux.HandleFunc("/api/agent", s.action(s.handleAgent))
	s.mux.HandleFunc("/api/payout", s.auth(s.handlePayout))
	s.mux.HandleFunc("/api/payout/onboard", s.action(s.handlePayoutOnboard))
	s.mux.HandleFunc("/api/payout/request", s.action(s.handlePayoutRequest))
	s.mux.HandleFunc("/api/payout/history", s.auth(s.handlePayoutHistory))
	// SETTINGS: the fuller surface. Per-band spend caps and private-band management were
	// TUI-only, so an operator working in the browser could neither see the limits
	// bounding their spend nor manage a band once it existed.
	s.mux.HandleFunc("/api/limits", s.auth(s.handleLimits)) // GET reads, POST sets one band
	s.mux.HandleFunc("/api/bands", s.auth(s.handleBands))
	s.mux.HandleFunc("/api/bands/", s.action(s.handleBandAction))
	s.mux.HandleFunc("/api/grants", s.auth(s.handleGrants)) // GET lists, POST creates
	// EDGE: the fleet snapshot (read, GET only) and the owner's adopt (POST only).
	s.modelRoutes()
	s.mux.HandleFunc("/api/towers", s.auth(s.handleTowers))
	s.mux.HandleFunc("/api/towers/invite", s.action(s.handleTowerInvite))
	s.mux.HandleFunc("/api/towers/invite/outcome", s.auth(s.handleTowerInviteOutcome))
	s.mux.HandleFunc("/api/towers/decide", s.action(s.handleTowerDecide))
	s.mux.HandleFunc("/api/edge", s.auth(s.handleEdge))
	s.mux.HandleFunc("/api/edge/adopt", s.action(s.handleEdgeAdopt))
	s.mux.HandleFunc("/api/edge/nearby/invite", s.action(s.handleEdgeNearbyInvite))
	s.mux.HandleFunc("/api/edge/nearby/approve", s.action(s.handleEdgeNearbyApprove))
	s.mux.HandleFunc("/api/edge/nearby/asked", s.auth(s.handleEdgeNearbyAsked))
	s.mux.HandleFunc("/api/edge/join", s.action(s.handleEdgeJoin))
	s.mux.HandleFunc("/api/edge/joinprompt", s.auth(s.handleEdgeJoinPrompt))
	s.mux.HandleFunc("/api/edge/name", s.action(s.handleEdgeName))
	s.mux.HandleFunc("/api/edge/forget", s.action(s.handleEdgeForget))
	s.mux.HandleFunc("/api/edge/agent", s.action(s.handleEdgeAgent))
	s.mux.HandleFunc("/api/edge/agent/add", s.action(s.handleEdgeAddAgent))
	s.mux.HandleFunc("/api/edge/prefer", s.action(s.handleEdgePrefer))
	s.mux.HandleFunc("/api/edge/job", s.action(s.handleEdgeJob))
	s.mux.HandleFunc("/api/edge/job/clear", s.action(s.handleEdgeJobClear))
	s.mux.HandleFunc("/api/edge/grant", s.action(s.handleEdgeGrantIssue))
	s.mux.HandleFunc("/api/edge/grant/revoke", s.action(s.handleEdgeGrantRevoke))
	s.mux.HandleFunc("/api/edge/tags", s.action(s.handleEdgeTags))
	s.mux.HandleFunc("/api/edge/rule", s.action(s.handleEdgeRule))
	s.mux.HandleFunc("/api/edge/rule/off", s.action(s.handleEdgeRuleOff))
	s.mux.HandleFunc("/api/edge/rule/remove", s.action(s.handleEdgeRuleRemove))
	s.mux.HandleFunc("/api/edge/maintain", s.action(s.handleEdgeMaintain))
	s.mux.HandleFunc("/api/edge/actuate", s.action(s.handleEdgeActuate))
	s.mux.HandleFunc("/api/edge/serve-model", s.action(s.handleEdgeServeModel))
	s.mux.HandleFunc("/api/edge/sink", s.action(s.handleEdgeSink))
	s.mux.HandleFunc("/api/edge/roger", s.action(s.handleEdgeRoger))
	s.mux.HandleFunc("/api/edge/standby", s.action(s.handleEdgeStandBy))
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

// Listen binds a free localhost port at/after addr (netbind.ListenFree: one bind, scanning
// upward when the port is taken) and returns the listener plus the URL (with the access token)
// to open. addr like "127.0.0.1:4180".
func (s *Server) Listen(addr string) (net.Listener, string, error) {
	ln, err := netbind.ListenFree(addr)
	if err != nil {
		return nil, "", err
	}
	return ln, "http://" + ln.Addr().String() + "/?t=" + s.token, nil
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
