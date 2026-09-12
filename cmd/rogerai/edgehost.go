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
	"fmt"
	"log"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
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
	hooks.EdgeFleet = h.st.fleet
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

// arm builds the discovery engine from the environment's knobs, and reports whether there
// is one. ROGERAI_EDGE_DISCOVERY=0 builds NOTHING: no engine, no socket, no goroutine.
// edge.New touches no network, so this is safe on the launch path.
func (h *edgeHost) arm() bool {
	opts := edgeDiscoveryOptions(h.st.fleet)
	if !opts.Config.Enabled {
		return false
	}
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
	if err := h.disc.Start(ctx); err != nil {
		log.Println("edge discovery did not start:", err, "- serving and relaying are unaffected")
		return
	}
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
	})
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
