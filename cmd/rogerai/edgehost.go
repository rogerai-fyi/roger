package main

// THE EDGE HOST: what makes a shipped `roger` show the operator's REAL fleet.
//
// internal/edge holds the fleet, the discovery engine and the LAN transport; internal/tui
// holds the [3] EDGE screen. Neither knows how to find the other, and this is the seam
// that joins them - the same one `roger edge` already stands on.
//
// ONE EDGE PER MACHINE. The CLI keeps the last-known Edge beside config.json (edge.json)
// and reads it through loadEdgeState; the TUI screen is given THAT fleet and THOSE
// candidates, and every write the screen makes goes back through the same save. There is
// no second source of truth to drift, so what the TUI adopts is what `roger edge list`
// prints in the next process.
//
// DISCOVERY IS NEVER ON THE PATH OF ANYTHING. It runs on its own goroutine, it is brought
// up there rather than at the call that starts it (so a wedged socket costs the launch
// nothing), a heartbeat is handed to the screen without ever blocking on it, and
// ROGERAI_EDGE_DISCOVERY=0 builds nothing at all - see the last section of
// features/edge/discovery.feature.

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/tui"
)

const (
	// edgeBeatBuffer bounds the heartbeats waiting for the screen. A run with no TUI (the
	// browser console alone) drains nothing at all, so this is a ring the host drops from
	// rather than a queue that grows.
	edgeBeatBuffer = 64
	// edgeStopGrace is how long a quit waits for the discovery goroutine. Cancelling the
	// context is what ends it; this bound is for the case where it is stuck in a socket
	// syscall, because nobody's quit should hang on the network.
	edgeStopGrace = 2 * time.Second
)

// edgeHost is this process's Edge: the fleet, the candidate list, the discovery engine
// that keeps both current, and the heartbeats the screen animates on.
type edgeHost struct {
	self  string
	st    *edgeState
	beats chan string

	disc   *edge.Discovery
	every  time.Duration
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	// face is this machine's LAN identity: the TLS describe listener a peer dials to
	// check who we are. It exists only once this machine has enrolled, because before
	// that there is nothing true to serve.
	//
	// auth is the LAN issuing service, on the ONE machine per Edge the owner
	// designated as its authority. Without it a plant network could form a root and
	// never enroll a second machine against it.
	//
	// BOTH are guarded by mu: the discovery goroutine brings them up (an enrollment can
	// happen while this process is already running) and a quit takes them down, and a
	// quit that timed out is a quit racing a pass that is still going.
	face   *http.Server
	faceLn net.Listener
	auth   *http.Server
	authLn net.Listener
	issuer *edgeauth.Issuer

	// mu guards the candidate list and the state-file write: the discovery goroutine
	// refreshes both while the UI goroutine reads them for a frame.
	mu sync.Mutex
}

// newEdgeHost loads this machine's Edge through the CLI's own loader, so the TUI and
// `roger edge` are looking at one file and one fleet.
func newEdgeHost(self string) (*edgeHost, error) {
	st, err := loadEdgeState()
	if err != nil {
		return nil, err
	}
	return &edgeHost{self: self, st: st, beats: make(chan string, edgeBeatBuffer)}, nil
}

// wire fills the Edge seams on the hooks the TUI is built from.
func (h *edgeHost) wire(hooks *tui.Hooks) {
	if h.self != "" {
		hooks.EdgeSelf = h.self
	}
	// An ENROLLED machine knows what it is called on its own graph: the name the owner
	// gave it when it joined. That beats the Station name, which is a supply-side label
	// and may not exist at all.
	if name := h.enrolledName(); name != "" {
		hooks.EdgeSelf = name
	}
	hooks.EdgeFleet = h.st.fleet
	hooks.EdgeSessions = h.st.sessions
	hooks.EdgeCandidates = h.candidates
	hooks.EdgeAdopt = h.adopt
	hooks.EdgeHeartbeats = h.beats
}

// candidates is what discovery has SEEN and nobody has adopted. They are handed over as a
// copy of their own list, never merged into the fleet: this machine holds no Edge
// authority until enrollment ships, so an unadopted peer has had nothing about it
// verified and must never be drawn as a member.
func (h *edgeHost) candidates() []store.EdgeNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]store.EdgeNode(nil), h.st.candidates...)
}

// adopt is the owner's explicit `a` on the Edge screen. It takes the SAME path as
// `roger edge adopt`: the certificate is checked before anything is enrolled, and the
// cache is written so the CLI prints what the screen just did.
func (h *edgeHost) adopt(id, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok, err := edgeResolve(h.st.candidates, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no candidate %q was seen on this network", id)
	}
	_, err = edgeAdoptCandidate(h.st, c, name, name)
	return err
}

// enrolledName is what this machine is called on its own Edge, or "" if it has not
// joined one.
func (h *edgeHost) enrolledName() string {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return ""
	}
	if n, found, err := h.st.fleet.Get(id.NodeID); err == nil && found && n.Name != "" {
		return n.Name
	}
	return id.NodeID
}

// arm builds the discovery engine from the environment's knobs, and reports whether there
// is one. ROGERAI_EDGE_DISCOVERY=0 builds NOTHING: no engine, no socket, no goroutine.
// edge.New touches no network, so this is safe on the launch path.
func (h *edgeHost) arm() bool {
	opts := edgeDiscoveryOptions(h.st.fleet)
	if !opts.Config.Enabled {
		return false
	}
	// THIS IS THE PAYOFF OF ENROLLMENT. Until a machine had a certificate it could only
	// browse, because an advertisement it could not back up is noise. Now it serves its
	// own describe endpoint over its own certificate and advertises the fingerprint of
	// it, so the owner's other machines can dial, check, and see it as a member.
	if self, ok := h.startFace(); ok {
		opts.Self = self
	}
	h.startAuthority()
	// The daemon paces its own passes here (see serve), because the host needs each
	// pass's report - to beat the screen, refresh the candidates and write the cache -
	// and the engine's own loop reports to nobody.
	opts.Config.ManualPasses = true
	if opts.Log == nil {
		opts.Log = func(line string) { log.Println(line) }
	}
	h.disc, h.every = edge.New(opts), opts.Config.Interval
	return true
}

// startFace stands this machine's LAN identity up: a TLS listener whose certificate IS
// its identity, and the advertisement that commits it to that certificate before any
// peer dials. It reports the advertisement, and false when this machine has not
// enrolled and so has nothing true to advertise.
func (h *edgeHost) startFace() (edge.Advert, bool) {
	if h.serving() {
		return edge.Advert{}, false
	}
	id, key, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return edge.Advert{}, false
	}
	srv := edge.NewServer(edge.Describe{
		NodeID: id.NodeID, Account: id.Account, Kind: string(edge.Host),
	}, tls.Certificate{Certificate: [][]byte{id.Cert.Raw}, PrivateKey: key})
	ln, err := net.Listen("tcp", edgeFaceBind())
	if err != nil {
		log.Println("edge: could not open this machine's LAN face:", err)
		return edge.Advert{}, false
	}
	face := &http.Server{Handler: srv.Handler(), TLSConfig: srv.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second}
	h.mu.Lock()
	h.faceLn, h.face = ln, face
	h.mu.Unlock()
	go func() { _ = face.ServeTLS(ln, "", "") }()
	return srv.Advert(ln.Addr().(*net.TCPAddr).Port), true
}

// serving reports whether this machine's LAN face is already up.
func (h *edgeHost) serving() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.face != nil
}

// startAuthority serves enrollment for the ONE machine the owner designated. It is not
// started anywhere else: a machine that holds no root has nothing to issue with.
func (h *edgeHost) startAuthority() {
	h.mu.Lock()
	already := h.auth != nil
	h.mu.Unlock()
	if already {
		return // re-arming discovery must not bind a second socket
	}
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		return
	}
	ln, err := net.Listen("tcp", edgeAuthorityBind())
	if err != nil {
		log.Println("edge: could not open the Edge authority's LAN service:", err)
		return
	}
	auth := &http.Server{Handler: enrollhttp.Handler(local), ReadHeaderTimeout: 10 * time.Second}
	h.mu.Lock()
	h.authLn, h.issuer, h.auth = ln, local.Issuer(), auth
	h.mu.Unlock()
	go func() { _ = auth.Serve(ln) }()
}

// EnvFaceBind and EnvAuthorityBind let an operator pin the ports. Both default to an
// ephemeral port on every interface: the advertisement carries the port, so nothing has
// to agree on a number in advance.
const (
	envFaceBind      = "ROGERAI_EDGE_BIND"
	envAuthorityBind = "ROGERAI_EDGE_AUTHORITY_BIND"
)

func edgeFaceBind() string {
	if v := os.Getenv(envFaceBind); v != "" {
		return v
	}
	return ":0"
}

func edgeAuthorityBind() string {
	if v := os.Getenv(envAuthorityBind); v != "" {
		return v
	}
	return ":0"
}

// authorityAddr is where this machine's Edge authority answers, or "" if it is not one.
func (h *edgeHost) authorityAddr() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.authLn == nil {
		return ""
	}
	return h.authLn.Addr().String()
}

// authorityIssuer is the issuer behind that service, for the surfaces that report on it.
func (h *edgeHost) authorityIssuer() *edgeauth.Issuer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.issuer
}

// running reports whether this host has a discovery engine at all.
func (h *edgeHost) running() bool { return h.disc != nil }

// start puts discovery on its own goroutine and RETURNS. Everything that can be slow -
// opening the multicast plane, the first browse - happens over there, so a machine with a
// hostile network launches exactly as fast as one with none.
func (h *edgeHost) start(ctx context.Context) {
	if !h.arm() {
		return
	}
	ctx, h.cancel = context.WithCancel(ctx)
	h.done = make(chan struct{})
	go func() { defer close(h.done); h.serve(ctx) }()
}

// serve brings the engine up and paces the passes. A pass that fails is a pass: it is
// retried on the NEXT interval, never in a tight loop.
func (h *edgeHost) serve(ctx context.Context) {
	// Start never blocks and never fails: an unusable network is one log line from the
	// engine itself, not an error, and the pass is simply retried on the next interval.
	_ = h.disc.Start(ctx)
	defer h.disc.Stop()
	t := time.NewTicker(h.every)
	defer t.Stop()
	for {
		h.runPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runPass is one discovery pass and everything the host does with it: the engine updates
// the fleet itself, the candidates it saw are merged into the list the CLI reads, the
// result is written to the cache, and every VERIFIED sighting becomes a heartbeat.
func (h *edgeHost) runPass(ctx context.Context) edge.Report {
	h.adoptEnrollment(ctx)
	rep := h.disc.RunOnce(ctx)
	h.mu.Lock()
	h.st.candidates = edgeMergeCandidates(h.st.candidates, h.disc.Candidates())
	err := h.st.save()
	h.mu.Unlock()
	if err != nil {
		log.Println("edge: could not write the last-known Edge:", err)
	}
	// A VERIFIED sighting is a node this instance really heard from, over its own
	// certificate. Nothing else beats: a candidate was not dialed, and a refusal is not
	// a heartbeat.
	for _, s := range rep.Verified {
		h.beat(s.NodeID)
	}
	return rep
}

// adoptEnrollment picks up an enrollment that happened while this process was ALREADY
// running - `roger edge enroll` in another terminal with the TUI open. Without it a
// machine would join its Edge and then stay silent until the next launch, which is
// precisely the "nothing advertises" state enrollment exists to end. It costs one cheap
// file check per pass and does nothing at all on a machine that has not enrolled.
func (h *edgeHost) adoptEnrollment(ctx context.Context) {
	if h.disc == nil || h.serving() {
		return
	}
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return
	}
	// Joining an Edge can move which fleet this machine belongs to - an Edge rooted at
	// a designated machine has an account of its own, not the Core login's. Discovery
	// filters on that account, so it has to be reloaded here or every peer of the Edge
	// this machine just joined would be silently discarded as somebody else's.
	if id.Account != "" && id.Account != h.st.account {
		if st, err := loadEdgeState(); err == nil {
			h.mu.Lock()
			h.st = st
			h.mu.Unlock()
			log.Println("edge: this machine enrolled into", id.Account+
				"; the EDGE screen draws that Edge from the next launch")
		}
	}
	h.disc.Stop()
	if !h.arm() {
		return
	}
	_ = h.disc.Start(ctx)
}

// beat hands the screen one real heartbeat and NEVER blocks. A run with no TUI drains
// nothing; a busy frame drains late. Neither may stall discovery, and a dropped pulse
// costs an animation frame, not a fact.
func (h *edgeHost) beat(id string) {
	select {
	case h.beats <- id:
	default:
	}
}

// stop ends discovery. It is idempotent, and it does not wait forever: a quit that hangs
// on a socket is worse than a pass that never finished.
func (h *edgeHost) stop() {
	h.once.Do(func() {
		if h.cancel == nil {
			return
		}
		h.cancel()
		select {
		case <-h.done:
			close(h.beats) // no sender is left, so the screen's drain ends cleanly
		case <-time.After(edgeStopGrace):
		}
		h.closeListeners()
	})
}

// closeListeners takes the LAN face and the authority service down. A quit that leaves
// a socket open leaves a machine advertising an identity nothing is behind.
func (h *edgeHost) closeListeners() {
	h.mu.Lock()
	face, auth := h.face, h.auth
	h.face, h.auth = nil, nil
	h.mu.Unlock()
	for _, srv := range []*http.Server{face, auth} {
		if srv != nil {
			_ = srv.Close()
		}
	}
}

// startEdge gives the TUI this machine's ONE Edge - the same fleet, cache and candidates
// `roger edge` reads - and starts LAN discovery behind it. It returns the stop the launch
// path defers.
func startEdge(hooks *tui.Hooks) func() {
	h, err := newEdgeHost(hooks.Station)
	if err != nil {
		// An Edge that could not be READ is not an empty Edge. Say so once and leave the
		// screen honestly unwired rather than drawing a fleet of none.
		log.Println("edge: could not read this machine's Edge:", err, "- the EDGE screen is unwired this run")
		return func() {}
	}
	h.wire(hooks)
	h.start(context.Background())
	return h.stop
}
