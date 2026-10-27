package main

// edgescan.go - WHEN this process may browse the LAN (features/edge/scan_on_request.feature).
//
// Founder ruling 2026-10-07: a machine that has not joined an Edge, and is not an Edge's
// authority, scans the LAN only on request - while the owner is on the [3] EDGE screen, while
// the console's Edge view is open, for the run of `roger edge scan`, and (in LAN mode) while the
// LAN dial is open or a LAN command runs. It never browses in the background, so a fresh install
// keeps broadcast 014's promise: it opens no inbound port. A machine in an Edge is unchanged.
//
// edgeMayBrowse is the ONE place that question is answered; the Edge host and the LAN dial both
// ask it before they open a browse socket.

import (
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/edgeauth"
)

// scanKind names what a request is for: the Edge's own discovery, or LAN mode's advertised
// Towers. They are separate so opening the LAN dial does not start an Edge scan.
type scanKind string

const (
	scanEdge scanKind = "edge"
	scanLAN  scanKind = "lan"
)

// edgeConsoleLease is how long one read of a console view counts as "the view is open". The
// console polls every second while a view shows and stops when it is left, so a few seconds
// after the last read the scan ends.
var edgeConsoleLease = 5 * time.Second

// scanScopes is the set of open on-request scopes. A scope is either held (Begin until its end
// is called: a TUI screen, a CLI run) or leased (a console read, renewed by the next one).
type scanScopes struct {
	mu    sync.Mutex
	held  map[scanKind]int
	until map[scanKind]time.Time
	now   func() time.Time
	// wake is poked whenever a scope opens or closes, so the Edge host starts or stops its
	// browse at once rather than at its next pass.
	wake chan struct{}
}

func newScanScopes() *scanScopes {
	return &scanScopes{held: map[scanKind]int{}, until: map[scanKind]time.Time{},
		now: time.Now, wake: make(chan struct{}, 1)}
}

// edgeScans is this process's scopes.
var edgeScans = newScanScopes()

// Begin opens a held scope and returns the function that closes it. The close is idempotent.
func (s *scanScopes) Begin(k scanKind) func() {
	s.mu.Lock()
	s.held[k]++
	s.mu.Unlock()
	s.poke()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.held[k] > 0 {
				s.held[k]--
			}
			s.mu.Unlock()
			s.poke()
		})
	}
}

// Lease opens (or renews) a scope that closes by itself d from now.
func (s *scanScopes) Lease(k scanKind, d time.Duration) {
	s.mu.Lock()
	was := s.activeLocked(k)
	if t := s.now().Add(d); t.After(s.until[k]) {
		s.until[k] = t
	}
	s.mu.Unlock()
	if !was {
		s.poke()
	}
}

// Active reports whether any scope of kind k is open now.
func (s *scanScopes) Active(k scanKind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeLocked(k)
}

func (s *scanScopes) activeLocked(k scanKind) bool {
	return s.held[k] > 0 || s.now().Before(s.until[k])
}

func (s *scanScopes) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// edgeInAnEdge reports whether this machine has joined an Edge (it holds a member
// certificate) or is an Edge's authority (it holds a root). Either way it keeps the behavior it
// always had: it browses, and it advertises what it can prove.
func edgeInAnEdge() bool {
	if _, _, ok, err := edgeIdentityStore().LoadIdentity(); err == nil && ok {
		return true
	}
	if _, ok, err := edgeauth.OpenLocal(edgeAuthDir()); err == nil && ok {
		return true
	}
	return false
}

// edgeMayBrowse is THE decision: may this process browse the LAN for kind k now?
func edgeMayBrowse(k scanKind) bool {
	return edgeInAnEdge() || edgeScans.Active(k)
}
