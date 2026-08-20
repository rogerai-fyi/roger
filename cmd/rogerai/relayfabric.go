package main

// relayfabric.go puts a share on the relay fabric without making the operator ask.
//
// It replaces `roger share --tower`. That flag was a mode: it selected which of two serving
// fabrics the node lived on for the life of the process, before any consumer existed, and it
// read like an instruction to create a Tower rather than to be carried by one. Both are
// wrong. Which relay carries a request is a placement decision that belongs to Core at the
// moment it knows who is asking and from where - see docs/relay-selection-design.md.
//
// So `roger share` means "route me", and this is the half that offers the node to the relay
// fabric in addition to the broker's own long-poll.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"rogerai.fm/roger/v5/internal/agent"
	"rogerai.fm/roger/v5/internal/client"
)

// startRelayFabric is how a share puts itself on the relay fabric: on a goroutine, best effort,
// under the process-wide shutdown that Ctrl-C cancels. It is what `go joinRelayFabric(cfgRun)`
// used to be, and production behaviour is that line exactly.
//
// IT IS A VAR BECAUSE A BARE `go` CANNOT BE JOINED, and an unjoinable worker is not a style
// question here - it was a live defect in the suite. cmdShare returns as soon as the shareBlock
// seam returns, while this goroutine is still inside its first AttachTower, which opens (and
// creates) a Station directory under XDG_CONFIG_HOME. A test points XDG_CONFIG_HOME at its own
// t.TempDir(), so the TempDir cleanup's RemoveAll raced a live writer: one whole-package run in
// six here, two in six for the hunt that found it. And the loser of that race keeps going - the
// first attach retries on a 30-second backoff, five times - underneath every test that runs
// after it, resolving os.UserConfigDir() afresh each time it is scheduled, which is a
// PROCESS-GLOBAL environment variable some entirely unrelated test has since repointed at its
// own temporary directory. The observed whole-package failure was not in a share test at all.
//
// The fix a test needs is to WAIT for the worker, and waiting needs two things this seam
// supplies together: a context of the test's own to cancel (shareShutdown is process-wide and
// cancelling it is terminal - see TestEveryLongLivedPlaneWaitsOnTheSharedShutdown, which pays a
// subprocess for exactly that reason) and a handle on the goroutine. A test swaps in a spawner
// that calls the SAME joinRelayFabric under a cancellable child and a WaitGroup it can wait on;
// production keeps the line below, which is why the shape of the real share is unchanged.
var startRelayFabric = func(cfg agent.Config) { go joinRelayFabric(shareShutdown, cfg) }

// joinRelayFabric offers an already-registered, already-on-air node to the relay fabric.
//
// ROUTINE FAILURE IS SILENT AND HARMLESS BY CONSTRUCTION. The node is registered, discoverable,
// probed and serving before this is called, so "no relay is free right now" costs nothing and
// the operator must never be shown an error about a plane they did not ask to be on.
//
// AN ERROR THAT COSTS THEM MONEY OR BREAKS THEIR TRUST ASSUMPTIONS IS NOT ROUTINE, and this used
// to swallow those too. The whole call was `_ = agent.ServeTower(..., discardWriter{})`, one
// discard covering both kinds of output, so into the bin went: towerhub.ErrNotCarried (the hub
// accepted a completion and never couriered the receipt - the node computed and will not be
// paid), a served result that could not be handed back, every audit failure, transcripts evicted
// inside their audit window, and the failure to pin Core's grant key, which is the one error
// meaning this node cannot tell a real grant from one its relay forged. Before `--tower` was
// removed those went to os.Stdout; the flag's removal is what turned a mode's chatter into the
// default's silence.
//
// So there are two seams now (see agent.Notice): progress still goes to a discard, and notices
// go to stderr through relayNotices, once each.
//
// It is skipped entirely when the node has no signed-in owner: attaching is an account act
// (it is what makes a station's earnings attributable), and an anonymous free share is a
// perfectly ordinary thing to be. Nothing is printed in that case either - there is no
// problem to report.
func joinRelayFabric(ctx context.Context, cfg agent.Config) {
	// A PRIVATE BAND NEVER JOINS. Asserted here as well as inside agent.AttachTower, because
	// this is the seam a future caller reaches first and an early return is cheaper than a
	// refused network call. Belt and braces on a guarantee that used to be neither: before
	// this, the only thing keeping a private band off the public fabric was that the call to
	// this function happened to sit inside `if !*private {` in main.go.
	if cfg.Private {
		return
	}
	if client.LinkedLogin() == "" {
		return
	}
	confDir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	notices := &relayNotices{}
	// THE SHARED SHUTDOWN, not a signal notifier of our own - handed in by startRelayFabric
	// rather than reached for here, so the one caller that serves a real share and the one that
	// serves a test's are the same code under two different lifetimes. This used to call
	// signal.NotifyContext here, which looks local and is not: the first registration anywhere
	// in a program disables Go's default SIGINT-kills-the-process disposition for the whole
	// program. Cancelling only this context would then leave the main goroutine sitting in
	// select{} with the operator's Ctrl-C already spent - and it would do so ONLY on the happy
	// path, since a join that fails returns before the notifier matters. `roger share` already
	// has exactly one place that knows what Ctrl-C means (acquireOnAirLock, which clears the
	// on-air lock and exits 130); this rides that one instead of racing it.
	//
	// io.Discard for PROGRESS, not for everything: the ordinary share has already printed its
	// on-air line, and a second stream of relay chatter underneath it would describe a plane the
	// operator did not opt into and cannot act on. Notices are the other channel.
	err = agent.ServeTower(ctx, cfg, agent.NodeKey(), filepath.Join(confDir, "rogerai"),
		discardWriter{}, notices.report)
	// The RETURNED error is the startup one, and only one shape of it is worth a word. An attach
	// that was refused means no relay would take this node right now, which is the ordinary case
	// this whole path is best-effort for. A key-pinning failure is not that: it means the node
	// reached a relay and could not establish what a genuine grant looks like, so it cannot
	// distinguish Core's work from the relay's invention. That is a trust assumption, not an
	// availability blip.
	if errors.Is(err, agent.ErrCoreKeysUnpinned) {
		notices.report(err)
	}
}

// relayNotices is the notice sink: stderr, prefixed, and each distinct message ONCE.
//
// Once matters. These loops retry forever by design - the audit poll every 45 seconds, the serve
// workers on a two-second backoff - so a standing condition (a hub that is down, a plaintext
// link, a transcript store that is too small) would otherwise scroll a `roger share` terminal
// off the screen and bury the on-air line the operator actually needs. Saying it once keeps it
// unmissable, which is the entire point of not discarding it.
//
// stderr, not stdout, so it never lands in the middle of anything a script is parsing out of a
// share's output.
type relayNotices struct {
	mu   sync.Mutex
	said map[string]bool
	// out is where a notice lands; nil means os.Stderr. A field rather than a hardcoded
	// os.Stderr so a test can assert what an operator actually reads, which is the whole
	// property this type exists for.
	out io.Writer
}

func (n *relayNotices) report(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	n.mu.Lock()
	if n.said == nil {
		n.said = map[string]bool{}
	}
	if n.said[msg] {
		n.mu.Unlock()
		return
	}
	n.said[msg] = true
	w := n.out
	n.mu.Unlock()
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "  relay: %s\n", msg)
}

// discardWriter swallows the relay path's routine PROGRESS output. Declared here rather than
// reaching for io.Discard so the reason travels with it - and so it is visibly the progress
// seam, not the only seam.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// refusedTowerFlag explains `roger share --tower` instead of letting the flag package answer
// it with "flag provided but not defined: -tower".
//
// The flag is gone and its absence IS the feature - a share reaches the relay fabric on its
// own - but the flag is still in scripts, in systemd units, in older docs and in at least one
// published article. Those operators get one line of output, and "not defined" tells them
// their roger is broken rather than that their command is out of date. The same care already
// went into `roger tower`, which is a word this product uses for a real thing and so answers
// with an explanation rather than "unknown command"; this is the same courtesy for a flag
// that used to work.
//
// It stays an ERROR. Silently accepting it would leave every one of those scripts passing a
// flag forever, and the operator never learning that the mode it selected no longer exists.
func refusedTowerFlag(args []string) error {
	for _, a := range args {
		if a == "--" {
			return nil // everything after this is positional, by convention
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if (strings.HasPrefix(a, "-") && a != "-") && name == "tower" {
			return fmt.Errorf("`roger share --tower` is no longer a thing, and you do not need it:\n" +
				"    roger share          # already offers your node to the relay fabric\n\n" +
				"reaching the fabric was never a mode to pick. Which relay carries a request - one of\n" +
				"ours or an operator's - is decided when a consumer tunes in, not by the provider hours\n" +
				"earlier. Drop the flag and the share is the same share, plus that reach.\n\n" +
				"To RUN a Tower (the relay itself) you want the separate roger-tower binary.")
		}
	}
	return nil
}
