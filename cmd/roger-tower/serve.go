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

	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

// serveJoined runs the link until the process is interrupted. It supplies the two things the
// loop cannot invent for itself - a real signal and a real clock - and then gets out of the
// way; everything that can go wrong is in runLink, where a test can reach it.
func serveJoined(st *tower.State, out io.Writer, relayPublic, hubAddr, hubCert, hubKey string) error {
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
	if hubAddr != "" {
		waitForHub, herr := runHubInBackground(st, hubAddr, hubCert, hubKey, out, stopped)
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
	err := runLink(st, out, stopped, realTicker, relayPublic)
	// THE LINK RETURNING WINDS EVERYTHING DOWN, error or not. Without this, a serve whose
	// link failed at startup - not registered, wrong mode - HUNG forever: the deferred
	// relay-wait was waiting on a stop signal only ctrl-c could send, over a loop whose
	// error had already been decided. Found by a test that timed out instead of failing.
	windDown()
	return err
}

func realTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// runLink is the link loop proper: open, push, heartbeat, re-open on refusal, drain on exit.
func runLink(st *tower.State, out io.Writer, stop <-chan struct{}, ticker func(time.Duration) (<-chan time.Time, func()), relayEndpoint string) error {
	if st.Mode != tower.ModeJoined {
		return errors.New(
			"this Tower is standalone: it serves its own local network and needs nothing from " +
				"RogerAI. `serve` here is for a joined Tower - initialize a new data directory " +
				"with --mode joined to join the public network")
	}

	// The head we last had accepted. Quoting it on connect is what lets Core say "resume"
	// instead of demanding everything - when Core is in step with us.
	head := towerjoin.Head{}
	var revision int64

	sess, err := towerjoin.OpenSession(st, head, relayEndpoint)
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
			if err := sess.SendHeartbeat(st); err == nil {
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
			sess, err = towerjoin.OpenSession(st, head, relayEndpoint)
			if err != nil {
				return err
			}
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
	hubCert := fs.String("hub-tls-cert", "", "TLS certificate (PEM) for the hub listener - with --hub-tls-key, the hub serves https")
	hubKey := fs.String("hub-tls-key", "", "TLS private key (PEM) for the hub listener")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The hub's payload is sealed end-to-end, but the node polling TOKEN and grant metadata
	// ride the transport - so TLS here is real protection, not ceremony (audit M1). Half a
	// key pair is a mistake, not a mode.
	if (*hubCert == "") != (*hubKey == "") {
		return fmt.Errorf("--hub-tls-cert and --hub-tls-key must be given together")
	}
	if *hubCert != "" && *hubAddr == "" {
		return fmt.Errorf("--hub-tls-cert without --hub: there is no hub listener to protect")
	}
	// Checked BEFORE the data directory is touched when there is no config to fill the gap:
	// a flag mistake should be reported as a flag mistake, not as whatever the directory
	// happens to complain about first.
	if *relayPublic != "" {
		if _, _, aerr := net.SplitHostPort(*relayPublic); aerr != nil {
			return fmt.Errorf("--relay-public must be a dialable host:port, got %q", *relayPublic)
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
	return serveJoined(st, out, *relayPublic, *hubAddr, *hubCert, *hubKey)
}
