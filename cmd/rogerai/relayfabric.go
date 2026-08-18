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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rogerai.fm/roger/v5/internal/agent"
	"rogerai.fm/roger/v5/internal/client"
)

// joinRelayFabric offers an already-registered, already-on-air node to the relay fabric.
//
// EVERY failure here is silent and harmless by construction. The node is registered,
// discoverable, probed and serving before this is called, so the worst case is that it keeps
// doing all of that over the broker's long-poll alone. A provider must never lose a working
// share because a relay was unavailable, and must never be shown an error about a plane they
// did not ask to be on.
//
// It is skipped entirely when the node has no signed-in owner: attaching is an account act
// (it is what makes a station's earnings attributable), and an anonymous free share is a
// perfectly ordinary thing to be. Nothing is printed in that case either - there is no
// problem to report.
func joinRelayFabric(cfg agent.Config) {
	if client.LinkedLogin() == "" {
		return
	}
	confDir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	// THE SHARED SHUTDOWN, not a signal notifier of our own. This used to call
	// signal.NotifyContext here, which looks local and is not: the first registration anywhere
	// in a program disables Go's default SIGINT-kills-the-process disposition for the whole
	// program. Cancelling only this context would then leave the main goroutine sitting in
	// select{} with the operator's Ctrl-C already spent - and it would do so ONLY on the happy
	// path, since a join that fails returns before the notifier matters. `roger share` already
	// has exactly one place that knows what Ctrl-C means (acquireOnAirLock, which clears the
	// on-air lock and exits 130); this rides that one instead of racing it.
	//
	// io.Discard, not os.Stdout: the ordinary share has already printed its on-air line, and
	// a second stream of relay chatter underneath it would describe a plane the operator did
	// not opt into and cannot act on.
	_ = agent.ServeTower(shareShutdown, cfg, agent.NodeKey(), filepath.Join(confDir, "rogerai"), discardWriter{})
}

// discardWriter swallows the relay path's progress output. Declared here rather than reaching
// for io.Discard so the reason travels with it.
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
