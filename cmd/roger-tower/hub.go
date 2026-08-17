package main

// hub.go mounts the Tower's DATA-PLANE HUB (Option C, Topology 2): the HTTP surface where
// consumers submit sealed jobs and this Tower's self-attached `roger share` nodes poll for
// them. The broker never touches the payload - it authorized the attempt (the grant) and will
// settle the receipt; everything between is this hub, and it is blind: it verifies only the
// grant's Core signature + metadata, and relays ciphertext it cannot read.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towerhub"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

// hubNodeRefresh is how often the hub re-fetches its node registrations from Core, picking up
// freshly self-attached nodes and dropping revoked ones.
const hubNodeRefresh = 30 * time.Second

// Settle-courier retry policy (audit H1). A completed job's receipt is the NODE'S PAY: if the
// one forward to Core fails - a deploy, a blip, a 503 at exactly the wrong moment - the hub
// path has no other collector, and once Core's settle window closes the hold is refunded and
// the work is unpaid forever. So the courier queues and retries until the window is over.
const (
	settleRetryEvery = 15 * time.Second
	// settleRetryWindow deliberately OVER-covers Core's settle window (grant lifetime + a
	// settle grace that is itself bounded under the hold TTL, ~9m at defaults): a few refused
	// retries past the close cost nothing, while giving up early costs a node its pay.
	settleRetryWindow = 10 * time.Minute
	settleQueueDepth  = 4096
	settleOverflowCap = 65536 // receipts parked while the queue is full; beyond this, ABANDONED loudly
	settleRetryCap    = 65536 // retry backlog bound; beyond this, ABANDONED loudly
	// settleAckGrace holds each first forward briefly so the CONSUMER'S acknowledgement can
	// reach Core ahead of the receipt. The consumer gets the answer at the same instant this
	// completion lands, and an ack that arrives before settlement corroborates it; one that
	// arrives after is stored but the settlement has already committed uncorroborated. With
	// no grace, the tower's own courier would beat every ack and the hub path's
	// corroboration RATE - the signal towers are judged on - would sit at zero by
	// construction. Two seconds is far above a real ack's latency and far below any human
	// notion of payment delay.
	settleAckGrace = 2 * time.Second
)

// pendingSettle is one receipt awaiting its ride to Core.
type pendingSettle struct {
	stationID string
	attemptID string
	receipt   []byte
	wireIn    int64     // sealed-request bytes this tower relayed (its own count; 0 = unknown)
	wireOut   int64     // sealed-result bytes this tower relayed
	notBefore time.Time // the ack-grace gate for the FIRST forward
	deadline  time.Time
}

// runHubInBackground starts the hub server and its node-registration refresher, returning a
// waiter that blocks until both have wound down. It fails fast (before serving anything) if
// Core's grant key cannot be fetched - a hub that cannot verify grants would either refuse
// everything or, worse, be tempted to skip the check.
func runHubInBackground(st *tower.State, addr, tlsCert, tlsKey string, out io.Writer, stop <-chan struct{}) (func(), error) {
	// THE GRANT CHECK CONTRACT (from the security audit): a REAL clock, so expired grants are
	// refused, and THIS tower's own ID, so a grant minted for another tower is refused here.
	coreKey, err := towerjoin.DispatchKey()
	if err != nil {
		return nil, fmt.Errorf("cannot fetch Roger Core's grant key: %w", err)
	}
	hub := towerhub.New()
	server := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, coreKey, link.PublicNetwork,
			st.TowerID, time.Now())
		return att, station, gerr
	}, 0, 0)
	// THE SETTLE COURIER: every completed result's receipt is forwarded to Core, tower-signed,
	// so the node is paid without holding its own line to Core. Opaque both ways; Core's
	// one-use settlement makes a duplicate forward a harmless 409. A failed forward is
	// QUEUED AND RETRIED until Core's settle window has certainly closed (audit H1) - the
	// receipt is the node's pay, and this hub is its only ride.
	// THE SPOOL: receipts persist to disk from the moment they are queued, so a tower crash
	// or redeploy mid-window cannot unbank a node (in-memory queues die with the process).
	spool, sperr := newSettleSpool(st.Dir())
	if sperr != nil {
		fmt.Fprintf(out, "hub: WARNING - settle spool unavailable (%v): receipts queued for Core survive only in memory until this is fixed\n", sperr)
		spool = nil
	}
	settleQ := make(chan pendingSettle, settleQueueDepth)
	// The overflow shares the retry backlog rather than making a doomed inline attempt: the
	// queue only fills when Core is already unreachable, which is exactly when one more
	// immediate forward would also fail (audit M-1).
	var overflowMu sync.Mutex
	var overflow []pendingSettle
	server.OnComplete = func(stationID string, res towerhub.Result) {
		p := pendingSettle{stationID: stationID, attemptID: res.AttemptID,
			receipt: res.Receipt, wireIn: int64(res.WireIn), wireOut: int64(len(res.Envelope)),
			notBefore: time.Now().Add(settleAckGrace),
			deadline:  time.Now().Add(settleRetryWindow)}
		if perr := spool.put(p); perr != nil {
			fmt.Fprintf(out, "hub: could not spool settle for %s: %v\n", p.attemptID, perr)
		}
		select {
		case settleQ <- p:
		default:
			overflowMu.Lock()
			if len(overflow) < settleOverflowCap {
				overflow = append(overflow, p)
			} else {
				fmt.Fprintf(out, "hub: settle for %s ABANDONED - the courier's queue and overflow are both full\n", p.attemptID)
			}
			overflowMu.Unlock()
		}
	}
	serveDone := make(chan struct{})    // closed when the listener returns (Shutdown makes this fire IMMEDIATELY)
	shutdownDone := make(chan struct{}) // closed only after Shutdown has finished draining handlers
	courierDone := make(chan struct{})
	go func() {
		defer close(courierDone)
		// Keyed by attempt id: a node retrying /complete re-fires OnComplete, and one receipt
		// deserves one backlog slot, not N (audit L-1). Core 409s duplicates regardless.
		retries := map[string]pendingSettle{}
		// Receipts a previous run of this tower queued but never delivered rejoin the
		// backlog; the expired ones were already discarded by load.
		for _, p := range spool.load(time.Now()) {
			retries[p.attemptID] = p
			fmt.Fprintf(out, "hub: recovered spooled settle for %s from a previous run\n", p.attemptID)
		}
		t := time.NewTicker(settleRetryEvery)
		defer t.Stop()
		forward := func(p pendingSettle, final bool) bool {
			err := towerjoin.SettleEdgeReceipt(st, p.stationID, p.attemptID, p.receipt, p.wireIn, p.wireOut)
			switch {
			case err == nil:
				spool.drop(p.attemptID)
				return true
			case errors.Is(err, towerjoin.ErrSettlePermanent):
				// Core judged the receipt itself invalid; retrying cannot fix it (audit L-2).
				fmt.Fprintf(out, "hub: settle for %s ABANDONED - %v\n", p.attemptID, err)
				spool.drop(p.attemptID)
				return true
			case final:
				fmt.Fprintf(out, "hub: settle for %s ABANDONED at shutdown: %v\n", p.attemptID, err)
			default:
				fmt.Fprintf(out, "hub: settle forward for %s failed (will retry): %v\n", p.attemptID, err)
			}
			return false
		}
		admit := func(p pendingSettle) {
			if len(retries) >= settleRetryCap {
				fmt.Fprintf(out, "hub: settle for %s ABANDONED - the retry backlog is full\n", p.attemptID)
				return
			}
			retries[p.attemptID] = p
		}
		drainOverflow := func() {
			overflowMu.Lock()
			ov := overflow
			overflow = nil
			overflowMu.Unlock()
			for _, p := range ov {
				admit(p)
			}
		}
		for {
			select {
			case p := <-settleQ:
				// The ack grace: wait out the remainder before the first forward, unless we
				// are shutting down (then the receipt matters more than the corroboration).
				if wait := time.Until(p.notBefore); wait > 0 {
					select {
					case <-time.After(wait):
					case <-stop:
					}
				}
				if !forward(p, false) {
					admit(p)
				}
			case <-t.C:
				drainOverflow()
				for id, p := range retries {
					if time.Now().After(p.deadline) {
						fmt.Fprintf(out, "hub: settle for %s ABANDONED - the settle window closed before Core answered\n", id)
						spool.drop(id)
						delete(retries, id)
						continue
					}
					if time.Now().Before(p.notBefore) {
						continue // the ack grace applies on the overflow path too (audit L-D)
					}
					if forward(p, false) {
						delete(retries, id)
					}
				}
			case <-stop:
				// FINAL DRAIN, sequenced AFTER Shutdown has finished draining handlers (audit
				// H-B: ListenAndServe returns the instant Shutdown is called, while handlers -
				// a /complete mid-body - keep running; serveDone is the WRONG signal). A
				// receipt enqueued by a draining handler must not vanish into a buffer nobody
				// reads. The quiet-window loop then catches OnComplete goroutines scheduled
				// but not yet run when Shutdown returned; anything that still slips through
				// is in the SPOOL for the next run.
				<-shutdownDone
				drainOverflow()
			finalDrain:
				for {
					select {
					case p := <-settleQ:
						_ = forward(p, true)
					case <-time.After(250 * time.Millisecond):
						break finalDrain
					}
				}
				drainOverflow()
				for _, p := range retries {
					_ = forward(p, true)
				}
				return
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, server.Submit)
	mux.HandleFunc(towerhub.PathPoll, server.Poll)
	mux.HandleFunc(towerhub.PathComplete, server.Complete)
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// Slow-loris bounds (the audit's mount-site contract). ReadTimeout covers the whole
		// request read - generous enough for a 16MB sealed submit on a slow uplink, small
		// enough that a trickled body cannot hold a connection all day. Poll responses are
		// long-held WRITES, which these do not bound.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
	}

	// The refresher: keep the hub's node registrations in step with Core's attachment
	// registry. RegisterNode also rotates tokens, and nodes that disappear are unregistered
	// so a revoked node's token stops polling within one refresh.
	var refreshMu sync.Mutex
	known := map[string]bool{}
	lastAttempt := time.Time{}
	// debounce=true is the on-demand path: re-checked UNDER the lock (audit M-2, the old
	// check-then-act let a burst stampede Core), and the attempt time is stamped even on
	// failure so a down Core is asked at most once a second, not once per waiting consumer.
	refresh := func(debounce bool) {
		refreshMu.Lock()
		defer refreshMu.Unlock()
		if debounce && time.Since(lastAttempt) < time.Second {
			return
		}
		lastAttempt = time.Now()
		nodes, nerr := towerjoin.HubNodes(st)
		if nerr != nil {
			fmt.Fprintf(out, "hub: could not refresh node registrations: %v\n", nerr)
			return
		}
		seen := map[string]bool{}
		for _, n := range nodes {
			server.RegisterNode(n.StationID, n.HubToken)
			seen[n.StationID] = true
		}
		for id := range known {
			if !seen[id] {
				server.UnregisterNode(id)
			}
		}
		known = seen
	}
	// FETCH-ON-UNKNOWN-STATION (audit M3): a consumer can arrive inside the up-to-30s window
	// between a node's self-attach and the next periodic refresh. An unknown-Station submit
	// triggers an immediate re-fetch (rate-limited; registration stays Core-authoritative),
	// closing the window to roughly one round trip.
	server.OnUnknownStation = func(string) { refresh(true) }
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		refresh(false)
		t := time.NewTicker(hubNodeRefresh)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refresh(false)
			case <-stop:
				return
			}
		}
	}()

	go func() {
		defer close(serveDone)
		if tlsCert != "" {
			fmt.Fprintf(out, "hub: serving the data plane on %s (TLS)\n", addr)
			if serr := httpSrv.ListenAndServeTLS(tlsCert, tlsKey); serr != nil && serr != http.ErrServerClosed {
				fmt.Fprintf(out, "hub: server stopped: %v\n", serr)
			}
			return
		}
		fmt.Fprintf(out, "hub: serving the data plane on %s - PLAINTEXT; pass --hub-tls-cert/--hub-tls-key (or front with TLS) before real traffic\n", addr)
		if serr := httpSrv.ListenAndServe(); serr != nil && serr != http.ErrServerClosed {
			fmt.Fprintf(out, "hub: server stopped: %v\n", serr)
		}
	}()
	go func() {
		defer close(shutdownDone)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if serr := httpSrv.Shutdown(ctx); serr != nil {
			fmt.Fprintf(out, "hub: shutdown drain incomplete (%v) - any receipt still in a live handler is in the spool for the next run\n", serr)
		}
	}()

	return func() {
		<-serveDone
		<-refreshDone
		<-courierDone
	}, nil
}
