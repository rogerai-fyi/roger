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
// The offers it pushes come from FILES its Stations produced (`roger-station offer`), and
// are relayed byte for byte. A Tower cannot make one up, because it does not hold a Station's
// assertion key and must never hold one - if it could sign for a Station, "signed by the
// Station" would mean "signed by the relay". A Tower with nothing in its offers directory
// pushes a valid inventory of zero leaves, which is the honest "I am here and I have
// nothing".

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

// serveJoined runs the link until the process is interrupted. It supplies the two things the
// loop cannot invent for itself - a real signal and a real clock - and then gets out of the
// way; everything that can go wrong is in runLink, where a test can reach it.
func serveJoined(st *tower.State, out io.Writer) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	// Adapt the signal channel to the loop's plain one, so runLink has no opinion about where
	// "stop" came from and a test does not have to send its own process a signal to exercise
	// the shutdown path. A test that did would be racing the Go runtime for the delivery.
	stopped := make(chan struct{})
	go func() {
		<-stop
		close(stopped)
	}()

	return runLink(st, out, stopped, realTicker)
}

// realTicker is the clock in production. A test passes one that fires on demand, which is
// what makes the heartbeat path testable without a test that sleeps - and a test that sleeps
// is a test that is flaky on a loaded machine.
func realTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// runLink is the link loop proper: open, push, heartbeat, re-open on refusal, drain on exit.
func runLink(st *tower.State, out io.Writer, stop <-chan struct{}, ticker func(time.Duration) (<-chan time.Time, func())) error {
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

	sess, err := towerjoin.OpenSession(st, head)
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
			sess, err = towerjoin.OpenSession(st, head)
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

// offersDir is where a Tower looks for the offers its Stations signed. One file per offer,
// produced by `roger-station offer` ON THE STATION and copied here by whatever the operator
// already trusts to move a file.
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	// serve takes --config for the same reason the state commands do, and it matters MORE
	// here: an operator whose `attach` wrote to the database while `serve` read local disk
	// would be relaying an inventory that does not describe their fleet.
	st, release, err := openDirWith(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()
	return serveJoined(st, out)
}
