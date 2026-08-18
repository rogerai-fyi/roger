package main

// The relay plane's notice sink.
//
// `roger share` discards the relay plane's progress on purpose - the on-air line is already
// printed and relay chatter underneath it describes a plane the operator did not opt into. The
// defect was that ONE discard covered both progress and the errors that cost the operator money:
// a completion the hub never couriered, a served result that could not be handed back, audit
// failures, and the failure to pin Core's grant key. These are the rules that keep the second
// kind out of the bin without putting the first kind back in.

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestRelayNoticesSaysARepeatedThingOnce(t *testing.T) {
	var buf bytes.Buffer
	n := &relayNotices{out: &buf}
	err := errors.New("the hub accepted this completion but did not forward the receipt")
	for i := 0; i < 5; i++ {
		n.report(err)
	}
	if got := strings.Count(buf.String(), "did not forward the receipt"); got != 1 {
		t.Fatalf("a standing condition was printed %d times; these loops retry forever and it would "+
			"scroll the on-air line off the screen.\n%s", got, buf.String())
	}
	if !strings.HasPrefix(buf.String(), "  relay: ") {
		t.Fatalf("a notice must be recognisable as the relay plane's: %q", buf.String())
	}
}

// A distinct condition is a distinct notice - deduping must not collapse two different problems
// into one.
func TestRelayNoticesKeepsDistinctProblemsApart(t *testing.T) {
	var buf bytes.Buffer
	n := &relayNotices{out: &buf}
	n.report(errors.New("relay audit: the audit plane is unavailable"))
	n.report(errors.New("this attempt was served but its result could not be returned to the hub"))
	n.report(errors.New("relay audit: the audit plane is unavailable"))

	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.said) != 2 {
		t.Fatalf("expected two distinct notices, got %d: %v", len(n.said), n.said)
	}
	for msg := range n.said {
		if strings.TrimSpace(msg) == "" {
			t.Fatal("an empty notice was recorded")
		}
	}
}

// Concurrent reporters: the serve workers and the audit loop all report from their own
// goroutines, and a `roger share` runs with -race in CI.
func TestRelayNoticesIsSafeUnderConcurrentReporters(t *testing.T) {
	n := &relayNotices{out: io.Discard}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); n.report(errors.New("the same standing condition")) }()
	}
	wg.Wait()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.said) != 1 {
		t.Fatalf("expected one recorded notice, got %d", len(n.said))
	}
}

// A nil error is not a notice.
func TestRelayNoticesIgnoresNil(t *testing.T) {
	var buf bytes.Buffer
	n := &relayNotices{out: &buf}
	n.report(nil)
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.said) != 0 || buf.Len() != 0 {
		t.Fatalf("a nil error was reported as a notice")
	}
}
