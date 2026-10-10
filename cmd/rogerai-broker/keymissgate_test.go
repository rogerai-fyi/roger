package main

import (
	"testing"
	"time"
)

// TestKeyMissGateWaitsThenRefusesWhenNothingComesBack: with the bucket empty and a read in
// flight, a reservation waits; if that read was an unknown key (no refund), it is refused once
// nothing is in flight, and a stuck read can hold it at most keyMissWait.
func TestKeyMissGateWaitsThenRefusesWhenNothingComesBack(t *testing.T) {
	saved := keyMissWait
	keyMissWait = 200 * time.Millisecond
	defer func() { keyMissWait = saved }()
	rl := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	g := newKeyMissGate()
	if ok, _ := g.reserve(rl, "a"); !ok {
		t.Fatal("first reservation refused")
	}
	// A stuck read: the in-flight reservation is never released; the waiter gives up at the cap.
	st := time.Now()
	if ok, retry := g.reserve(rl, "a"); ok || retry < 1 {
		t.Fatalf("reserve on an empty bucket with a stuck read = %v (retry %d), want refused with a hint", ok, retry)
	}
	if d := time.Since(st); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("waited %v, want about keyMissWait", d)
	}
	// The read ends as an unknown key: nothing in flight, bucket empty, refused at once.
	g.release(rl, "a", false)
	st = time.Now()
	if ok, _ := g.reserve(rl, "a"); ok {
		t.Fatal("reservation passed an empty bucket with nothing in flight")
	}
	if d := time.Since(st); d > 50*time.Millisecond {
		t.Fatalf("refusal with nothing in flight waited %v", d)
	}
}

// TestKeyMissGateWakesWhenALiveKeyGivesItsTokenBack: a waiter proceeds as soon as an in-flight
// read releases as a live key.
func TestKeyMissGateWakesWhenALiveKeyGivesItsTokenBack(t *testing.T) {
	rl := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	g := newKeyMissGate()
	if ok, _ := g.reserve(rl, "a"); !ok {
		t.Fatal("first reservation refused")
	}
	got := make(chan bool, 1)
	go func() { ok, _ := g.reserve(rl, "a"); got <- ok }()
	time.Sleep(20 * time.Millisecond)
	g.release(rl, "a", true)
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("the waiter was refused although the in-flight read gave its token back")
		}
	case <-time.After(time.Second):
		t.Fatal("the waiter never woke")
	}
	g.release(rl, "a", true)
	if len(g.pending) != 0 {
		t.Fatalf("pending not cleaned up: %v", g.pending)
	}
}
