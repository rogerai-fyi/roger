package main

// signedlatch.go persists the hub's "this Station's node SIGNS" latch across restarts.
//
// # WHAT IT IS FOR
//
// The hub accepts a pre-signature bearer token for one transition release, and the latch is what
// ends that tolerance PER STATION instead of per release: the first request the tower verifies
// as a genuine signature from a Station kills the token for that Station, from that instant.
//
// In memory, that guarantee ended at the process boundary. Core never rotates HubToken - it
// returns the same value on every re-attach, for the life of the attachment - so after every
// redeploy a bearer captured off the plaintext wire before a node upgraded opened that node's
// queue again. And not for one round trip: a node's first post-restart request carries the old
// hub epoch and is refused, so the latch closes on its SECOND request, and an on-path attacker
// who could keep the node signing for the wrong epoch could hold the window open at will. The
// honest statement was "the stolen bearer comes back every time the tower redeploys, for as long
// as somebody on the path wants it to".
//
// # WHY A DIRECTORY OF FILES
//
// The same reason the settle spool is one (spool.go): this is a tower's own local state, tiny,
// under its own data dir, and it must survive a process that dies without warning. A file per
// Station means an Add is one create with no read-modify-write, so two workers latching two
// Stations at the same moment cannot lose each other's write - which a single rewritten JSON
// file would make possible and would only show up as a bearer quietly working again.
//
// The filename is a hash of the Station id, so an id can never traverse or collide however it is
// spelled; the id itself is the file's contents, because Load has to give them back.
//
// # WHAT IT DELIBERATELY DOES NOT DO
//
// It never removes anything. The latch is set-only within a process for a reason recorded at
// length in towerhub's UnregisterNode - every event that used to clear it was a registration
// FLAP rather than evidence about the node, and un-latching on a flap hands the bearer back -
// and making it durable does not change that argument, it extends it. The set is bounded by
// Core's own fleet and each entry is a few dozen bytes; when the bearer path is deleted one
// release from now, this goes with it and the directory can be removed.

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const signedLatchDirName = "signed-stations"

// signedLatch is a towerhub.SignedLatchStore backed by a directory.
type signedLatch struct {
	dir string
	out io.Writer
	// warned keeps a failing disk from printing on every poll of every Station. The operator
	// needs to know once; a line per request would bury it.
	warned sync.Once
}

// newSignedLatch prepares the directory. A failure is returned rather than swallowed: the caller
// decides whether to run without persistence, and says so where an operator will see it.
func newSignedLatch(base string, out io.Writer) (*signedLatch, error) {
	dir := filepath.Join(base, signedLatchDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &signedLatch{dir: dir, out: out}, nil
}

func (s *signedLatch) path(stationID string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%x", sha256.Sum256([]byte(stationID))))
}

// Load reads back every Station id this tower has recorded a signature from.
//
// An unreadable ENTRY is skipped rather than failing the whole load: one corrupt file must not
// re-open the bearer for every other Station on the tower, which is the direction that costs
// operators their queues.
func (s *signedLatch) Load() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if rerr != nil {
			continue
		}
		if id := strings.TrimSpace(string(raw)); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// Add records one Station. Idempotent by construction - the same id writes the same file - and
// safe for concurrent use because each Station has its own path.
func (s *signedLatch) Add(stationID string) error {
	if err := os.WriteFile(s.path(stationID), []byte(stationID), 0o600); err != nil {
		s.warned.Do(func() {
			fmt.Fprintf(s.out, "hub: WARNING - cannot record that station %s signs (%v): "+
				"its legacy bearer token will be accepted again after this tower restarts, "+
				"until its node's next signed request closes the latch\n", stationID, err)
		})
		return err
	}
	return nil
}
