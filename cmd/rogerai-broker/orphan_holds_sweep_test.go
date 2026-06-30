package main

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestReleaseStaleHoldsSweepOnce covers the per-iteration backstop: a tracked hold older
// than the cutoff is reclaimed (exact amount restored); a within-window hold is untouched.
func TestReleaseStaleHoldsSweepOnce(t *testing.T) {
	mem := store.NewMem()
	if _, err := mem.AddCredits("alice", 10); err != nil {
		t.Fatal(err)
	}
	if ok, _ := mem.HoldFor("alice", "req-1", 4); !ok {
		t.Fatal("HoldFor refused")
	}
	b := &broker{db: mem, holdTTL: 10 * time.Minute}

	// cutoff in the past -> the live hold is NOT reclaimed.
	b.releaseStaleHoldsSweepOnce(time.Now().Add(-time.Hour))
	if bal, _ := mem.BalanceOf("alice", 0); bal != 6 {
		t.Fatalf("within-window balance=%v, want 6", bal)
	}
	// cutoff in the future -> the stale hold is reclaimed, exact amount.
	b.releaseStaleHoldsSweepOnce(time.Now().Add(time.Hour))
	if bal, _ := mem.BalanceOf("alice", 0); bal != 10 {
		t.Fatalf("post-sweep balance=%v, want 10", bal)
	}
}

// TestReleaseStaleHoldsSweepLoop covers the ticker loop: with a tiny TTL the sweep fires on
// its own and reclaims a stranded hold, then close(stop) returns the goroutine.
func TestReleaseStaleHoldsSweepLoop(t *testing.T) {
	mem := store.NewMem()
	if _, err := mem.AddCredits("alice", 10); err != nil {
		t.Fatal(err)
	}
	if ok, _ := mem.HoldFor("alice", "req-1", 4); !ok {
		t.Fatal("HoldFor refused")
	}
	b := &broker{db: mem, holdTTL: 20 * time.Millisecond}
	stop := make(chan struct{})
	go b.releaseStaleHoldsSweep(stop)
	defer close(stop)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if bal, _ := mem.BalanceOf("alice", 0); bal == 10 {
			break
		}
		if time.Now().After(deadline) {
			bal, _ := mem.BalanceOf("alice", 0)
			t.Fatalf("sweep loop never reclaimed the stale hold (balance=%v, want 10)", bal)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestReleaseStaleHoldsSweepDisabled covers the no-op guards: TTL<=0 disables the sweep and
// a nil db returns immediately (both must not panic or block).
func TestReleaseStaleHoldsSweepDisabled(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&broker{db: store.NewMem(), holdTTL: 0}).releaseStaleHoldsSweep(make(chan struct{}))
		(&broker{db: nil, holdTTL: time.Minute}).releaseStaleHoldsSweep(make(chan struct{}))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled sweep should return immediately")
	}
}

// TestHoldSweepInterval covers the cadence clamp: half the TTL, capped at 1h, never <=0.
func TestHoldSweepInterval(t *testing.T) {
	if got := holdSweepInterval(10 * time.Minute); got != 5*time.Minute {
		t.Errorf("holdSweepInterval(10m)=%s, want 5m", got)
	}
	if got := holdSweepInterval(4 * time.Hour); got != time.Hour {
		t.Errorf("holdSweepInterval(4h)=%s, want 1h cap", got)
	}
	if got := holdSweepInterval(1 * time.Nanosecond); got <= 0 {
		t.Errorf("holdSweepInterval(1ns)=%s, want >0", got)
	}
}

// TestHoldTTLEnv covers the env override + default for the hold TTL.
func TestHoldTTLEnv(t *testing.T) {
	t.Setenv("ROGERAI_HOLD_TTL", "")
	if got := holdTTL(); got != defaultHoldTTL {
		t.Errorf("holdTTL() default=%s, want %s", got, defaultHoldTTL)
	}
	t.Setenv("ROGERAI_HOLD_TTL", "3m")
	if got := holdTTL(); got != 3*time.Minute {
		t.Errorf("holdTTL() env=%s, want 3m", got)
	}
	t.Setenv("ROGERAI_HOLD_TTL", "garbage")
	if got := holdTTL(); got != defaultHoldTTL {
		t.Errorf("holdTTL() bad env=%s, want default %s", got, defaultHoldTTL)
	}
}
