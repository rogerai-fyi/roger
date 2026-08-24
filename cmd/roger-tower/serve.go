package main

// serve.go holds the joined relay link open.
//
// Before this, `roger-tower serve` told the operator the link had not shipped. Core's routes
// existed and were exercised only by tests speaking HTTP directly - a protocol with one
// participant. This is the other one.
//
// WHAT SERVING MEANS TODAY, said plainly here so the command can say it plainly too: the
// Tower registers, opens a session, pushes a signed inventory of whatever its Stations have
// signed, heartbeats, and drains cleanly on shutdown. It does NOT carry customer traffic:
// dispatch is not built.
//
// The inventory it pushes is typically EMPTY now: self-attached `roger share --tower` nodes
// register their offers directly with Core at attach, and the legacy leaf-offer files (from
// the retired roger-station binary) have no producer. An inventory of zero leaves is the
// honest "I am here"; the offers directory remains read for byte-for-byte relay of any
// legacy files an operator still carries, and Core excludes what nothing can serve.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerjoin"
)

// serveJoined runs the link until the process is interrupted. It supplies the two things the
// loop cannot invent for itself - a real signal and a real clock - and then gets out of the
// way; everything that can go wrong is in runLink, where a test can reach it.
func serveJoined(st *tower.State, out io.Writer, relayPublic string, hub hubOptions) error {
	hub.Advertised = relayPublic
	// THE MODE CHECK COMES FIRST, BEFORE ANY LISTENER AND BEFORE ANY DIAL.
	//
	// runLink refuses a standalone Tower, and did so correctly - but it runs AFTER the hub, and
	// the hub's very first act is to fetch Roger Core's grant key and then this Tower's node
	// list from Core. So `roger-tower serve --hub` on a standalone data directory made two
	// public-network calls before being told it should not have. features/tower/modes.feature
	// says a standalone Tower "performs no RogerAI DNS lookup or network connection", and that
	// was true only of the flag combination nobody had tried.
	//
	// Signed hub polls make the hub's dependence on Core heavier still - the assertion keys it
	// verifies node polls against arrive on that same fetch - so the refusal moves ahead of it
	// rather than the fetch being made conditional. runLink keeps its own check: it is called
	// directly, and a guarantee this size should hold at both doors.
	if st.Mode != tower.ModeJoined {
		return errStandaloneCannotServeJoined
	}
	// THE HUB'S CERTIFICATE IS RESOLVED HERE, BEFORE EITHER THE LISTENER OR THE LINK, because
	// both halves of the change depend on it and they must not be able to disagree. The
	// listener presents these bytes; the link advertises their fingerprint to Core, which hands
	// it to every node and consumer routed here. Resolving it in one place, once, is what makes
	// "the pin Core published is the certificate this hub presents" true by construction rather
	// than by two code paths happening to read the same file.
	//
	// AFTER THE MODE CHECK, deliberately. A standalone Tower must not so much as mint a key on
	// the strength of a joined-mode flag; it is refused above with nothing written.
	relay := link.RelayPlane{Endpoint: relayPublic}
	if hub.Addr != "" && hub.tlsWanted() {
		mat, terr := hubTLS(st.Dir(), st.TowerID, hub.TLSCert, hub.TLSKey)
		if terr != nil {
			return terr
		}
		hub.cert = &mat.Cert
		relay.TLSSPKI = mat.Pin
		fmt.Fprintf(out, "hub: TLS certificate pin %s - Roger Core publishes this to every node "+
			"and consumer it routes here, and they accept no other certificate\n", mat.Pin)
	}
	if relay.TLSSPKI == "" && relay.Endpoint != "" {
		// SAID AT THE TOWER, NOT ONLY AT THE NODE. The node has always printed a plaintext
		// notice, but the operator who could fix it never saw it: they run this process, not
		// somebody else's `roger share`. One line, at the moment the tower decides to advertise
		// a plaintext data plane.
		fmt.Fprint(out, "NOTE: this tower advertises a PLAINTEXT hub. The sealed job and its "+
			"answer stay private either way, but every poll puts a Station's long-term "+
			"assertion public key - its payment identity - on the wire in the clear, and "+
			"nothing authenticates this hub's answers to a node. Pass --hub-tls to close both.\n")
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	// Adapt the signal channel to the loop's plain one, so runLink has no opinion about where
	// "stop" came from and a test does not have to send its own process a signal to exercise
	// the shutdown path. A test that did would be racing the Go runtime for the delivery.
	stopped := make(chan struct{})
	var once sync.Once
	windDown := func() { once.Do(func() { close(stopped) }) }
	go func() {
		<-stop
		windDown()
	}()

	// THE HUB - the data plane (Option C, Topology 2): the tower-hosted job queue consumers
	// submit sealed work
	// to and self-attached roger share nodes poll. Started before the link so a consumer's
	// very first submit after we advertise has somewhere to land; fails fast if Core's grant
	// key cannot be fetched.
	if hub.Addr != "" {
		waitForHub, herr := runHubInBackground(st, hub, out, stopped)
		if herr != nil {
			windDown()
			return herr
		}
		defer waitForHub()
	}
	// RENEWAL, alongside the link and the relay. Without it the certificate and the lease
	// both lapse in a day and the Tower is finished - re-enrollment through quarantine, for
	// an operator who did nothing wrong. It runs independently of the link for the same
	// reason the relay does: a control-plane blip must not also cost the credential.
	go towerjoin.KeepRenewed(st, out, stopped, realTicker)
	err := runLink(st, out, stopped, realTicker, relay)
	// THE LINK RETURNING WINDS EVERYTHING DOWN, error or not. Without this, a serve whose
	// link failed at startup - not registered, wrong mode - HUNG forever: the deferred
	// relay-wait was waiting on a stop signal only ctrl-c could send, over a loop whose
	// error had already been decided. Found by a test that timed out instead of failing.
	windDown()
	return err
}

// errStandaloneCannotServeJoined is the one refusal both doors give, so the two cannot drift
// into saying different things about the same rule.
var errStandaloneCannotServeJoined = errors.New(
	"this Tower is standalone: serve its own local network with `roger-tower-local --dir DIR` " +
		"(a separate, Core-free binary; loopback by default). `serve` here is for a JOINED " +
		"Tower - initialize a new data directory with --mode joined to join the public network")

func realTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// runLink is the link loop proper: open, push, heartbeat, re-open on refusal, drain on exit.
func runLink(st *tower.State, out io.Writer, stop <-chan struct{}, ticker func(time.Duration) (<-chan time.Time, func()), relay link.RelayPlane) error {
	if st.Mode != tower.ModeJoined {
		return errStandaloneCannotServeJoined
	}

	// The head we last had accepted. Quoting it on connect is what lets Core say "resume"
	// instead of demanding everything - when Core is in step with us.
	head := towerjoin.Head{}
	var revision int64

	sess, err := towerjoin.OpenSession(st, head, relay)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "linked to RogerAI as %s (session %s)\n", sess.TowerID, sess.SessionID)

	// Push once up front. Core told us whether it needs everything; today we only ever have
	// everything, because deltas need a fleet that changes and we have no Stations yet.
	revision, head, err = pushInventory(st, out, revision, head)
	if err != nil {
		return err
	}

	// DRAIN ON THE WAY OUT, on every exit path. Leaving without it means Core keeps offering
	// this Tower's Stations for a full freshness window after it has gone.
	defer func() {
		if cerr := sess.Close(st); cerr != nil {
			fmt.Fprintf(out, "warning: could not drain cleanly: %v\n", cerr)
			return
		}
		fmt.Fprintln(out, "drained: RogerAI has dropped this Tower's inventory")
	}()

	// The operator's first question is "am I approved?", and the answer changes without
	// a restart - so it is printed at link time and announced again the moment a
	// heartbeat reports a different state.
	lastState := ""
	announceState(out, &lastState, sess.State)
	beat := sess.Heartbeat
	if beat <= 0 {
		beat = 60 * time.Second
	}
	beats, stopBeats := ticker(beat)
	defer stopBeats()

	// AND THE INVENTORY REFRESH, which is not optional. A pushed revision EXPIRES, and once
	// it does Core has nothing routable for this Tower - while the heartbeats keep
	// succeeding and everything keeps looking healthy. Without this the loop pushed once and
	// went dark half an hour later, silently, in production only.
	refreshes, stopRefresh := ticker(inventoryRefresh)
	defer stopRefresh()

	fmt.Fprintf(out, "holding the link (heartbeat every %s, inventory refresh every %s) - "+
		"ctrl-c to drain and exit\n", beat, inventoryRefresh)

	for {
		select {
		case <-stop:
			fmt.Fprintln(out, "\nstopping")
			return nil
		case <-refreshes:
			// A refusal here is NOT fatal: the inventory we already pushed is good until it
			// expires, so there is time for the next refresh to succeed. Tearing the link
			// down over one bad push would turn a blip into an outage.
			next, nextHead, rerr := pushInventory(st, out, revision, head)
			if rerr != nil {
				fmt.Fprintf(out, "could not refresh the inventory (%v) - will retry\n", rerr)
				continue
			}
			revision, head = next, nextHead
		case <-beats:
			if state, err := sess.SendHeartbeat(st); err == nil {
				announceState(out, &lastState, state)
				continue
			} else if errors.Is(err, towerjoin.ErrUnreachable) {
				// Transport, not refusal: the freshness window is several heartbeats wide, so
				// one lost frame costs nothing and reconnecting immediately would be worse.
				fmt.Fprintf(out, "heartbeat did not reach RogerAI (%v) - will retry\n", err)
				continue
			}
			// Refused: the session is gone (Core restarted, or our lease lapsed). Re-open and
			// find out which, rather than heartbeating into nothing.
			fmt.Fprintln(out, "the session was refused - re-opening")
			sess, err = towerjoin.OpenSession(st, head, relay)
			if err != nil {
				return err
			}
			announceState(out, &lastState, sess.State)
			if sess.NeedFullInventory {
				revision, head, err = pushInventory(st, out, revision, head)
				if err != nil {
					return err
				}
			}
		}
	}
}

// inventoryRefresh is how often the fleet is re-pushed.
//
// DERIVED from the lifetime, deliberately: a hardcoded interval beside it is how the two
// drift apart when one changes, and the failure that produces is invisible - every heartbeat
// still succeeds while Core quietly has nothing routable. A third leaves room for one push
// to fail and the next to still land inside the window.
const inventoryRefresh = towerjoin.InventoryLifetime / 3

// pushInventory sends the current fleet and returns the new chain position.
func pushInventory(st *tower.State, out io.Writer, revision int64, head towerjoin.Head) (int64, towerjoin.Head, error) {
	leaves, err := localOffers(st, out)
	if err != nil {
		return revision, head, err
	}
	next := revision + 1
	prev := head.Hash
	if prev == "" {
		prev = "genesis"
	}
	res, err := towerjoin.PushFullInventory(st, next, prev, leaves)
	switch {
	case errors.Is(err, towerjoin.ErrNeedFullInventory):
		// We only ever send full snapshots today, so being asked for one again means our
		// chain position is not Core's. Start from one rather than guessing.
		res, err = towerjoin.PushFullInventory(st, 1, "genesis", leaves)
		if err != nil {
			return revision, head, err
		}
		next = 1
	case err != nil:
		return revision, head, err
	}

	if len(leaves) == 0 {
		fmt.Fprintf(out, "inventory revision %d accepted: no Stations attached yet, so this "+
			"Tower is on the network and carrying nothing\n", res.Revision)
	} else {
		fmt.Fprintf(out, "inventory revision %d accepted: %d of %d Station offer(s) eligible\n",
			res.Revision, res.Routable, len(leaves))
	}
	// The exclusions are the answer to "why is my Station idle", and they are the only place
	// an operator can get it.
	for _, ex := range res.Excluded {
		fmt.Fprintf(out, "  · %s is not eligible: %s\n", ex.StationID, ex.Reason)
	}
	return next, towerjoin.Head{Revision: res.Revision, Hash: res.Hash}, nil
}

// offersDir is where a Tower looks for legacy Station-signed offer files. The producer (the
// roger-station binary) is retired; the directory is still read so an operator's existing
// files surface as explicit Core-side exclusions rather than silently vanishing.
const offersDir = "offers"

// localOffers reads the Station-signed offers this Tower is relaying.
//
// RELAYED VERBATIM. The bytes are passed through untouched and are never decoded and
// re-encoded, because a Station signs its own offers with an assertion key this Tower does
// not hold and must never hold. Re-encoding one would invalidate the signature at best and,
// at worst, quietly change what the Station said. That is also why this reads FILES rather
// than building offers from the Tower's own configuration: there is no configuration a Tower
// could hold that would let it produce a leaf Core accepts, and that is by design.
//
// A file that is not JSON is REPORTED AND SKIPPED rather than fatal. One bad file should not
// take a whole fleet off the network - but a silent skip is how an operator ends up staring
// at a Station that never appears, so it is named on the way past. Core applies its own
// nineteen-row rejection table to every leaf that does get through and reports what it
// excluded, which is the answer to "why is my Station idle".
func localOffers(st *tower.State, out io.Writer) ([]json.RawMessage, error) {
	dir := filepath.Join(st.Dir(), offersDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		// Not an error, and not silent either: a Tower with no offers directory is the
		// ordinary state of one whose Stations have not been set up yet.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the offers directory %s: %w", dir, err)
	}
	var leaves []json.RawMessage
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			fmt.Fprintf(out, "warning: skipping %s: %v\n", e.Name(), rerr)
			continue
		}
		if !json.Valid(raw) {
			fmt.Fprintf(out, "warning: skipping %s: it is not valid JSON\n", e.Name())
			continue
		}
		leaves = append(leaves, json.RawMessage(raw))
	}
	return leaves, nil
}

// cmdServe is the `roger-tower serve` entry point.
func cmdServe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir, cfg := dirAndConfig(fs)
	relayPublic := fs.String("relay-public", "", "the PUBLIC host:port consumers reach this tower's data plane (the hub) at, advertised to Roger Core")
	hubAddr := fs.String("hub", "", "address to serve the data-plane HUB on, e.g. :8444 - where consumers submit sealed work and this tower's self-attached nodes poll")
	hubTLSOn := fs.Bool("hub-tls", false, "serve the hub over TLS. With no --hub-tls-cert, this tower mints and keeps a self-signed certificate and Roger Core publishes its fingerprint to every node and consumer it routes here, so no publicly-trusted certificate and no domain name are needed")
	hubCert := fs.String("hub-tls-cert", "", "TLS certificate (PEM) for the hub listener - implies --hub-tls, and with --hub-tls-key the hub serves https")
	hubKey := fs.String("hub-tls-key", "", "TLS private key (PEM) for the hub listener")
	// THE TRANSITION SWITCH, and it is a real switch. A node released before signed hub polls
	// authenticates with a bearer token, and this tower keeps accepting one for a release so
	// that provider keeps earning while they update. An operator who knows every node on their
	// tower has updated can end that early; one release from now the flag and the code behind
	// it both go. It had been documented as "default true" while being settable from nowhere
	// but a test, which is a claim an operator cannot act on.
	legacyBearer := fs.Bool("hub-legacy-bearer", true, "accept the pre-signature bearer token from nodes that have not updated yet (a station that has signed to this hub always refuses its own token from then on); -hub-legacy-bearer=false requires a signature from every node")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The hub's payload is sealed end-to-end, but the grant metadata and every node's
	// long-term assertion PUBLIC KEY ride the transport in the clear - so TLS here is real
	// protection, not ceremony (audit M1). It is no longer the polling token: a current node
	// signs each request and transmits nothing reusable. What TLS buys now is that an observer
	// cannot tie a station's payment identity to an address. Half a key pair is a mistake, not
	// a mode.
	if (*hubCert == "") != (*hubKey == "") {
		return fmt.Errorf("--hub-tls-cert and --hub-tls-key must be given together")
	}
	if (*hubCert != "" || *hubTLSOn) && *hubAddr == "" {
		return fmt.Errorf("--hub-tls without --hub: there is no hub listener to protect")
	}
	// Checked BEFORE the data directory is touched when there is no config to fill the gap:
	// a flag mistake should be reported as a flag mistake, not as whatever the directory
	// happens to complain about first.
	if *relayPublic != "" {
		resolved, note, aerr := resolveAdvertised(*relayPublic)
		if aerr != nil {
			return aerr
		}
		*relayPublic = resolved
		if note != "" {
			fmt.Fprintln(out, note)
		}
	}
	if *cfg == "" && *relayPublic != "" && *hubAddr == "" {
		return fmt.Errorf("--relay-public advertises a data plane, but no --hub is serving one")
	}
	// serve takes --config for the same reason the state commands do, and it matters MORE
	// here: an operator whose `attach` wrote to the database while `serve` read local disk
	// would be relaying an inventory that does not describe their fleet.
	st, release, err := openDirWith(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	// THE CONFIG IS NOT DECORATION. A data plane declared in the file and then ignored
	// because the operator did not also pass flags is the exact failure an audit found
	// across the rest of the schema; flags win when both are given, because a flag is the
	// more deliberate of the two.
	if *cfg != "" {
		c, cerr := loadConfig(*cfg)
		if cerr != nil {
			return cerr
		}
		for _, u := range c.Unenforced() {
			fmt.Fprintf(out, "IGNORED: %s\n", u)
		}
		if *relayPublic == "" && c.Relay != nil {
			*relayPublic = c.Relay.Public
		}
		if c.Hub != nil {
			if *hubAddr == "" {
				*hubAddr = c.Hub.Address
			}
			if *hubCert == "" && *hubKey == "" {
				*hubCert, *hubKey = c.Hub.TLSCert, c.Hub.TLSKey
			}
			// The file can only turn TLS ON. A config saying tls:false while the operator typed
			// --hub-tls would be the file quietly downgrading the more deliberate of the two,
			// and the thing it would be downgrading is the channel's confidentiality.
			if c.Hub.TLS {
				*hubTLSOn = true
			}
			// The config can only turn the tolerance OFF, and only when the flag was left at
			// its default. A file that said "true" while the operator typed
			// -hub-legacy-bearer=false would be the config quietly overriding the more
			// deliberate of the two, which is the rule this whole block exists to keep.
			if c.Hub.AllowLegacyBearer != nil && !fsSet(fs, "hub-legacy-bearer") {
				*legacyBearer = *c.Hub.AllowLegacyBearer
			}
		}
	}
	// The PUBLIC address is what Core hands to consumers and self-attaching nodes, and the
	// listen address is very often not it - ":8444" is not dialable by anyone. A hub with no
	// public address still serves whoever was told about it some other way, but Core will
	// not route anyone new here, and the operator should know that is what they asked for.
	if *relayPublic != "" && *hubAddr == "" {
		return fmt.Errorf("--relay-public advertises a data plane, but no --hub is serving one")
	}
	if *hubAddr != "" && *relayPublic == "" {
		fmt.Fprint(out, "NOTE: the hub has no --relay-public address, so Roger Core will not "+
			"route edge consumers or self-attaching nodes to this Tower.\n")
	}
	if !*legacyBearer {
		fmt.Fprint(out, "hub: pre-signature bearer tokens are REFUSED on this tower - a node "+
			"older than signed hub polls will not be served here.\n")
	}
	return serveJoined(st, out, *relayPublic, hubOptions{
		Addr: *hubAddr, TLS: *hubTLSOn, TLSCert: *hubCert, TLSKey: *hubKey,
		AllowLegacyBearer: *legacyBearer,
	})
}

// fsSet reports whether a flag was actually typed, as opposed to sitting at its default. It is
// what lets configuration lower a default without ever overriding an explicit flag - the rule
// the rest of cmdServe follows by checking for an empty string, which a bool cannot do.
func fsSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// announceState prints the admission state when it CHANGES, in the operator's language.
// Quarantine is the state every Tower is admitted into, so it reads as the waiting room
// it is, not as a fault; approval reads as the green light it is; anything this binary
// does not recognise is shown verbatim rather than hidden, because an old CLI meeting a
// new Core should say something true.
func announceState(out io.Writer, last *string, state string) {
	if state == "" || state == *last {
		return
	}
	*last = state
	switch state {
	case "quarantine":
		fmt.Fprint(out, "state: QUARANTINE - pending approval by the network admin.\n"+
			"  Nothing is broken: every new Tower waits here, and approval flips this\n"+
			"  automatically - this terminal will announce it. Meanwhile the link stays up\n"+
			"  and `roger-tower status` shows the same answer. Learn more: https://rogerai.fm/tower\n")
	case "active":
		fmt.Fprint(out, "state: ACTIVE - approved and ready to carry traffic.\n"+
			"  Stations can now attach, and every carried job will print here as it settles.\n")
	case "draining":
		fmt.Fprint(out, "state: DRAINING - taking no new work; existing jobs finish.\n")
	case "suspended":
		fmt.Fprint(out, "state: SUSPENDED - taking no work pending review by the network admin.\n")
	case "revoked":
		fmt.Fprint(out, "state: REVOKED - this Tower's credential has been permanently retired.\n")
	default:
		fmt.Fprintf(out, "state: %s\n", state)
	}
}

// resolveAdvertised turns the operator's --relay-public into the address Core will hand
// to nodes and consumers, and says anything worth saying about it.
//
// An empty host (":8444") means "this machine": it resolves to the machine's own
// outbound address and PRINTS the choice, because advertising the literal ":8444" made
// every node dial itself - silent nonsense. A loopback host is accepted and named for
// what it is: a same-machine test rig the public network cannot reach. Neither is an
// error; both are the operator's business - said out loud.
func resolveAdvertised(endpoint string) (addr, note string, err error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("--relay-public must be a dialable host:port, got %q", endpoint)
	}
	parsed := net.ParseIP(host)
	// An empty host (":8444") or an UNSPECIFIED one ("0.0.0.0", "::") is a BIND wildcard,
	// not a reachable address - the thing you pass to --hub to listen on every interface,
	// mistaken for the thing you advertise. You cannot dial 0.0.0.0. Both mean "this
	// machine", so resolve to the machine's own outbound address and say what happened.
	if host == "" || (parsed != nil && parsed.IsUnspecified()) {
		ip, derr := outboundIP()
		if derr != nil {
			return "", "", fmt.Errorf("--relay-public %q is a bind wildcard, not a reachable address, "+
				"and this machine's own address could not be determined (%v) - pass the address explicitly", endpoint, derr)
		}
		resolved := net.JoinHostPort(ip, port)
		why := "had no host"
		if host != "" {
			why = fmt.Sprintf("was %s, a bind wildcard nothing can dial", host)
		}
		return resolved, fmt.Sprintf("relay-public %s: advertising this machine's address, %s", why, resolved), nil
	}
	if parsed != nil {
		return endpoint, classifyAdvertised(host, []net.IP{parsed}, false), nil
	}
	// A NAME - "roggentoo", "hub.example.net". Resolve it here, on the operator's own
	// machine, and say what it points at: a LAN name is a first-class home-lab tier, and
	// the operator deserves to know which tier they just advertised - and that the name
	// must resolve on every device that will dial it, which an /etc/hosts entry on this
	// box alone does not give them.
	rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, rerr := lookupIPFn(rctx, "ip", host)
	if rerr != nil || len(ips) == 0 {
		return "", "", fmt.Errorf("--relay-public names %q, which does not resolve on this machine (%v). "+
			"Every device dialing the hub must be able to resolve it - use the IP address if unsure", host, rerr)
	}
	return endpoint, classifyAdvertised(host, ips, true), nil
}

// lookupIPFn is the advert's resolver, a seam so tests classify names without DNS.
var lookupIPFn = net.DefaultResolver.LookupIP

// classifyAdvertised names the tier an advert lands in, in the operator's language:
// loopback is a same-machine test rig, a private address is the home-lab LAN tier, and a
// public one says nothing because nothing needs saying.
func classifyAdvertised(host string, ips []net.IP, named bool) string {
	loop, private := false, false
	shown := ips[0].String()
	for _, ip := range ips {
		switch {
		case ip.IsLoopback():
			loop = true
		case ip.IsPrivate() || ip.IsLinkLocalUnicast():
			private = true
		}
	}
	switch {
	case loop:
		return "relay-public is loopback: only THIS machine can reach the hub. " +
			"Fine for testing; the public network (and Core's canary) cannot reach it."
	case private && named:
		return fmt.Sprintf("relay-public %q resolves to %s - a LOCAL network address. Devices on "+
			"your LAN can reach the hub; the public network (and Core's canary) cannot. "+
			"Note: every device dialing the hub must resolve %q itself - if the name lives only "+
			"in this machine's hosts file, advertise %s instead.", host, shown, host, shown)
	case private:
		return fmt.Sprintf("relay-public %s is a LOCAL network address. Devices on your LAN can "+
			"reach the hub; the public network (and Core's canary) cannot.", host)
	}
	return ""
}

// outboundIP is the address this machine uses to reach the world: a UDP "dial" that
// sends nothing and asks the kernel which source address it would pick.
func outboundIP() (string, error) {
	conn, err := net.Dial("udp", "203.0.113.1:9")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP.IsUnspecified() {
		return "", fmt.Errorf("no outbound address")
	}
	return la.IP.String(), nil
}
