package main

// hub.go mounts the Tower's DATA-PLANE HUB (Option C, Topology 2): the HTTP surface where
// consumers submit sealed jobs and this Tower's self-attached `roger share` nodes poll for
// them. The broker never touches the payload - it authorized the attempt (the grant) and will
// settle the receipt; everything between is this hub, and it is blind: it verifies only the
// grant's Core signature + metadata, and relays ciphertext it cannot read.

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
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

// maxHubConns caps concurrent connections to the data plane. See where it is applied.
const maxHubConns = 512

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

// hubOptions is how `roger-tower serve` is configured to run its data plane. It is a struct
// because the fourth of these is a security posture rather than an address, and a bool trailing
// three strings in a positional call is how a posture gets flipped by accident.
type hubOptions struct {
	// Addr is the listen address; empty means no hub at all.
	Addr string
	// TLS serves the hub over https. It is implied by TLSCert, and on its own it means "mint
	// and keep a self-signed certificate" - see hubtls.go for why that is a complete answer
	// rather than a development shortcut.
	//
	// IT USED TO BE A TRAP. The flags behind TLSCert have existed for a while and no node
	// could ever use them: a Tower advertises its data plane as bare host:port, both ingress
	// points parse that with net.SplitHostPort, and the node's base-URL builder therefore had
	// only one reachable branch - "http://" + endpoint. An operator who went to the trouble of
	// obtaining a certificate got a TLS listener that every node connected to in plaintext and
	// failed against. The pin advertised beside the endpoint (link.Hello.RelayTLSSPKI) is what
	// makes it real.
	TLS     bool
	TLSCert string
	TLSKey  string
	// cert is the resolved certificate, loaded by serveJoined before either the listener or
	// the link starts. Unexported: it is not a configuration knob, it is the ONE loaded copy
	// whose fingerprint was advertised to Core, and a second load could disagree with it.
	cert *tls.Certificate
	// AllowLegacyBearer keeps accepting the pre-signature bearer token from nodes that have
	// not updated. Default ON (the flag's default), because the promise made when signatures
	// landed was that an already-released provider keeps earning while they update - but an
	// operator who knows their own fleet can end it early, and one release from now it goes
	// altogether. Note that ON is not the same as "a token opens a queue": a Station that has
	// signed to this hub refuses its own token from then on (internal/towerhub/nodeauth.go).
	AllowLegacyBearer bool
}

// tlsWanted reports whether this hub should terminate TLS. A certificate implies it, so an
// operator who was already passing --hub-tls-cert does not have to learn a second flag to keep
// what they had.
func (o hubOptions) tlsWanted() bool { return o.TLS || o.TLSCert != "" }

// runHubInBackground starts the hub server and its node-registration refresher, returning a
// waiter that blocks until both have wound down. It fails fast (before serving anything) if
// Core's grant key cannot be fetched - a hub that cannot verify grants would either refuse
// everything or, worse, be tempted to skip the check.
func runHubInBackground(st *tower.State, opt hubOptions, out io.Writer, stop <-chan struct{}) (func(), error) {
	// FAIL CLOSED ON A TLS POSTURE WITH NOTHING BEHIND IT. serveJoined resolves the certificate
	// before it calls this, so a nil one here means a caller asked for TLS and did not supply
	// it - and the only alternative to stopping is to serve plaintext on a listener the operator
	// believes is protected, with Core publishing a pin for a certificate that will never be
	// presented. That is the trap this whole change removes, one layer up.
	if opt.tlsWanted() && opt.cert == nil {
		return nil, errors.New("the hub was asked to serve TLS with no certificate resolved: " +
			"refusing to serve plaintext under a TLS posture")
	}
	// THE GRANT CHECK CONTRACT (from the security audit): a REAL clock, so expired grants are
	// refused, and THIS tower's own ID, so a grant minted for another tower is refused here.
	coreKey, err := towerjoin.DispatchKey()
	if err != nil {
		return nil, fmt.Errorf("cannot fetch Roger Core's grant key: %w", err)
	}
	// THIS TOWER'S ADMITTED IDENTITY KEY, which the hub uses to PROVE its process epoch to a
	// polling node. The epoch rides in the node's signed target, and it is published on an
	// unauthenticated 401 over a plaintext link - so without a proof, anyone on the path could
	// answer a poll with an epoch of their choosing and collect a genuine signature over it
	// (see internal/towerhub/nodeauth.go, HubKeyHeader). The node checks that proof against
	// this key's fingerprint, which Core hands it at attach.
	//
	// FAIL FAST, like the grant key above and for the same reason: a hub that cannot prove its
	// epoch is a hub every current node refuses to sign for, and discovering that as "no
	// station ever serves" is worse than not starting.
	identity, err := st.IdentityKey()
	if err != nil {
		return nil, fmt.Errorf("cannot read this tower's identity key (the hub proves its epoch with it): %w", err)
	}
	// THE SIGNED LATCH, ON DISK. Without it every redeploy re-opened the pre-signature bearer
	// for nodes that upgraded long ago, because Core never rotates the token - see
	// towerhub.SignedLatchStore. Best effort with a loud line, exactly like the settle spool
	// beside it: a tower that cannot write here still works, and the operator is told what they
	// have lost rather than left to find out from an audit.
	latch, lerr := newSignedLatch(st.Dir(), out)
	if lerr != nil {
		fmt.Fprintf(out, "hub: WARNING - signed-station latch unavailable (%v): a stolen legacy "+
			"bearer token will work again after every restart of this tower, until each node's "+
			"next signed request closes its own latch\n", lerr)
		latch = nil
	}
	hub := towerhub.New()
	server := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, coreKey, link.PublicNetwork,
			st.TowerID, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{
		// THIS TOWER'S NAME, SIGNED INTO EVERY REQUEST. Without it a node's signature captured
		// here was presentable at any other hub process holding the same Station - a second
		// instance behind one endpoint, or this one after a redeploy inside the skew window.
		// See internal/towerhub/nodeauth.go.
		TowerID:           st.TowerID,
		AllowLegacyBearer: opt.AllowLegacyBearer,
		EpochKey:          identity,
		SignedLatch:       latchStore(latch),
	})
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
	// THE AUDIT COURIER: a node's answered audit is forwarded to Core tower-signed. Fire and
	// forget with a log line - an audit that misses simply times out at Core's deadline as a
	// soft/hard miss by its own rules; unlike the settle courier, no one's PAY rides on it.
	server.OnTranscript = func(stationID string, reply towerhub.TranscriptReply) {
		if err := towerjoin.ForwardAuditTranscript(st, reply.AttemptID, reply.Available,
			reply.SealedBundle, reply.Transcript, reply.Request, reply.Response); err != nil {
			fmt.Fprintf(out, "hub: audit forward for %s failed: %v\n", reply.AttemptID, err)
		}
	}
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
	mux.HandleFunc(towerhub.PathAuditWanted, server.AuditWanted)
	mux.HandleFunc(towerhub.PathAuditTranscript, server.AuditTranscript)
	httpSrv := &http.Server{
		Addr:    opt.Addr,
		Handler: mux,
		// Slow-loris bounds (the audit's mount-site contract). ReadTimeout covers the whole
		// request read - generous enough for a 16MB sealed submit on a slow uplink, small
		// enough that a trickled body cannot hold a connection all day. Poll responses are
		// long-held WRITES, which these do not bound.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
	}

	// THE LISTENER IS BOUNDED NOW, and that bound is what turns every remaining pre-auth cost on
	// this hub from unbounded into arithmetic.
	//
	// Two of them are unavoidable by construction. /complete and /audit/transcript must READ the
	// body before they can authenticate it, because the signature covers a digest of the bytes
	// that arrived - so a caller admitted at the door gets up to 16MB buffered before authNode
	// sees it. And every node-facing answer carries an Ed25519 proof of this hub's epoch, so a
	// request that reaches a handler costs a signature. Neither can be removed; both are
	// per-connection, and until now nothing bounded connections at all. With no cap and a
	// two-minute read timeout, one machine could hold as many of both as it cared to open.
	//
	// A cap makes the worst case a number an operator can reason about: at most maxHubConns
	// concurrent bodies in memory, at most maxHubConns concurrent verifies. Excess connections
	// WAIT in the accept queue rather than being refused, which is the right direction for a
	// serving node - a poll that queues for a moment is a poll; a poll refused is an operator
	// not earning.
	//
	// It is generous on purpose. A tower's real concurrency is its Stations' poll workers plus
	// consumer submits, a few per Station; 512 is far above any tower this design contemplates
	// and far below what an unbounded listener hands an attacker.
	ln, lerr := net.Listen("tcp", opt.Addr)
	if lerr != nil {
		return nil, fmt.Errorf("cannot listen on %s: %w", opt.Addr, lerr)
	}
	ln = limitConns(ln, maxHubConns)

	// The refresher: keep the hub's node registrations in step with Core's attachment
	// registry. RegisterNode also rotates a node's credential, and nodes that disappear are
	// unregistered so a revoked node stops polling within one refresh.
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
		seen := registerHubNodes(server, nodes, out)
		for id := range known {
			if !seen[id] {
				server.UnregisterNode(id)
			}
		}
		known = seen
		// The AUDIT WANTED lists ride the same refresh: Core's per-station wants are grouped
		// and handed to the hub, where each node's own poll picks them up.
		if wanted, werr := towerjoin.WantedAudits(st); werr != nil {
			fmt.Fprintf(out, "hub: could not refresh the audit wanted lists: %v\n", werr)
		} else {
			byStation := map[string][]string{}
			for _, wa := range wanted {
				byStation[wa.StationID] = append(byStation[wa.StationID], wa.AttemptID)
			}
			for id := range seen {
				server.SetWanted(id, byStation[id])
			}
		}
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
		if opt.cert != nil {
			// TLS 1.3 ONLY, matching towerhub.PinnedTLSConfig on the other end. Both ends of
			// this connection are this codebase, so there is no compatibility to trade away -
			// and under 1.2 the server's certificate crosses the wire in the clear, which
			// would hand a passive observer the very fingerprint that identifies which tower a
			// node is attached to.
			httpSrv.TLSConfig = &tls.Config{
				MinVersion:   tls.VersionTLS13,
				Certificates: []tls.Certificate{*opt.cert},
			}
			fmt.Fprintf(out, "hub: serving the data plane on %s (TLS, pinned by fingerprint)\n", ln.Addr())
			// EMPTY PATHS ON PURPOSE: ServeTLS uses TLSConfig.Certificates when it is given no
			// files, and the loaded certificate is the one whose fingerprint Core is already
			// publishing. Re-reading the files here would let a certificate replaced on disk
			// since startup be served under the advertised pin, which is an outage that looks
			// exactly like an attack.
			if serr := httpSrv.ServeTLS(ln, "", ""); serr != nil && serr != http.ErrServerClosed {
				fmt.Fprintf(out, "hub: server stopped: %v\n", serr)
			}
			return
		}
		fmt.Fprintf(out, "hub: serving the data plane on %s - PLAINTEXT; pass --hub-tls (or front with TLS) before real traffic\n", ln.Addr())
		if serr := httpSrv.Serve(ln); serr != nil && serr != http.ErrServerClosed {
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

// registerHubNodes turns Core's answer to /tower/hub/nodes into hub registrations. It is the
// ONLY production path from Core's JSON to a towerhub.NodeAuth, and it lives out here as a
// function rather than inside the refresher's closure so a test can reach it: the e2e test in
// cmd/rogerai-broker proves the attachment CARRIES a hex assertion key and then registers the
// node itself from a helper of its own, which proves nothing whatever about this code. A field
// name that did not match, or a key Core sent base64 while this read hex, would have shipped
// green. See hub_test.go.
//
// It returns the set of Station ids Core listed, which is what the caller diffs against the
// previous answer to decide who to unregister.
func registerHubNodes(server *towerhub.Server, nodes []towerjoin.HubNode, out io.Writer) map[string]bool {
	seen := map[string]bool{}
	for _, n := range nodes {
		// The ASSERTION KEY is what a signed poll is verified against; Core sends it hex on the
		// same call that already carried the token. A key that will not decode is dropped
		// rather than registered short: a truncated key would refuse every one of that node's
		// polls, and saying so once beats a silent 401 loop the operator sees only as a station
		// that never serves.
		seen[n.StationID] = true
		var pub ed25519.PublicKey
		if n.AssertionKey != "" {
			raw, derr := hex.DecodeString(n.AssertionKey)
			if derr != nil || len(raw) != ed25519.PublicKeySize {
				// AND THE TOKEN GOES WITH IT. This used to register the bearer anyway, on the
				// reading that "no usable key" describes a node too old to sign. It does not:
				// an EMPTY key describes that node (Core older than signed polls, or an
				// attachment that predates them), and it is handled below. A key that is
				// PRESENT and unusable describes a Station whose assertion key Core has, and
				// mangled - corruption, not a version skew - because every self-attached
				// Station is admitted with a hex assertion key and it is immutable thereafter.
				//
				// Registering a bearer for that Station would open its queue, on a plaintext
				// wire, to a string an on-path observer already has, for a Station that can no
				// longer authenticate any other way. So this registration is not applied at
				// all: whatever the hub already holds for the Station stays (an earlier good
				// answer keeps a working node working), and a Station with nothing held is
				// simply not servable until Core sends something usable. Fail closed, and say
				// so once.
				fmt.Fprintf(out, "hub: station %s has an unusable assertion key from Core - "+
					"it cannot make a signed poll here until that is fixed, and its legacy "+
					"bearer token is NOT registered against a key this tower cannot check\n", n.StationID)
				continue
			}
			pub = ed25519.PublicKey(raw)
		}
		server.RegisterNode(n.StationID, towerhub.NodeAuth{AssertionKey: pub, LegacyToken: n.HubToken})
	}
	return seen
}

// limitConns bounds how many connections a listener will hand out at once.
//
// It is written here rather than pulled in (golang.org/x/net/netutil has one) because it is
// twenty lines and this repository does not otherwise depend on x/net - a dependency added for a
// semaphore is a dependency to keep patched forever.
//
// EXCESS CONNECTIONS WAIT, THEY ARE NOT REFUSED. Accept blocks until a slot frees, so a burst
// queues in the kernel's backlog and is served a moment later. Refusing would be the wrong
// direction on a hub whose entire purpose is to let providers earn: a poll delayed is a poll, a
// poll refused is a node that stopped serving.
func limitConns(inner net.Listener, max int) net.Listener {
	return &limitedListener{Listener: inner, slots: make(chan struct{}, max)}
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	l.slots <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: l.release}, nil
}

// release is idempotent per connection: Close can be called more than once (net/http does), and
// a double release would hand out a slot that is still in use.
func (l *limitedListener) release() { <-l.slots }

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// latchStore turns a possibly-nil *signedLatch into a possibly-nil interface value, which is not
// the same thing: a nil *signedLatch stored in an interface is a NON-nil interface holding a nil
// pointer, and every call on it would panic on the serving path. The two-line conversion is here
// rather than inline because that distinction is exactly the kind that reads as noise until it
// takes a tower down.
func latchStore(l *signedLatch) towerhub.SignedLatchStore {
	if l == nil {
		return nil
	}
	return l
}
