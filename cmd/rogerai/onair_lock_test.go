package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// useTempConfigDir points configPath() (and thus onAirLockPath) at a throwaway
// dir so the test never touches a real ~/.config/rogerai/share-*.lock.
func useTempConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// TestOnAirLock_BlocksAnotherLiveProcess covers the core guard: a lock held by a
// DIFFERENT, still-alive process must block a fresh acquire. We stand in for that
// other daemon with our parent PID (alive for the duration of the test, and != us).
func TestOnAirLock_BlocksAnotherLiveProcess(t *testing.T) {
	useTempConfigDir(t)

	other := os.Getppid()
	if other == os.Getpid() || !processAlive(other) {
		t.Skip("no distinct live parent PID to simulate another daemon")
	}
	held := onAirInfo{PID: other, Station: "other-daemon", Model: "qwen3", Started: 1}
	b, _ := json.Marshal(held)
	if err := os.MkdirAll(filepath.Dir(onAirLockPath("brave-otter-qwen3")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onAirLockPath("brave-otter-qwen3"), b, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := acquireOnAirLock("brave-otter-qwen3", "brave-otter", "qwen3"); err == nil {
		t.Fatal("expected acquire to be blocked by another live process's lock")
	}
}

// TestOnAirLock_DistinctNodesDoNotCollide covers the per-node-id keying: a rig
// running two different models (=> two node ids) must hold both locks at once.
func TestOnAirLock_DistinctNodesDoNotCollide(t *testing.T) {
	useTempConfigDir(t)

	relA, err := acquireOnAirLock("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatalf("acquire model A failed: %v", err)
	}
	defer relA()

	relB, err := acquireOnAirLock("brave-otter-gptoss", "brave-otter", "gpt-oss")
	if err != nil {
		t.Fatalf("acquire model B (distinct node id) was wrongly blocked: %v", err)
	}
	defer relB()
}

// TestOnAirLock_ReleaseFreesAndReacquires covers the lifecycle: acquire writes the
// lock, release removes it (only while ours), and the next acquire then succeeds.
func TestOnAirLock_ReleaseFreesAndReacquires(t *testing.T) {
	useTempConfigDir(t)

	release, err := acquireOnAirLock("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if _, err := os.Stat(onAirLockPath("brave-otter-qwen3")); err != nil {
		t.Fatalf("lockfile not written: %v", err)
	}

	release()
	if _, err := os.Stat(onAirLockPath("brave-otter-qwen3")); !os.IsNotExist(err) {
		t.Fatalf("release did not remove the lockfile (stat err=%v)", err)
	}

	release2, err := acquireOnAirLock("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	release2()
}

// TestOnAirLock_ReclaimsStaleLock covers crash recovery: a lock whose owning PID is
// no longer alive must be reclaimed, not treated as a live session.
func TestOnAirLock_ReclaimsStaleLock(t *testing.T) {
	useTempConfigDir(t)

	// Write a lock owned by a PID that is essentially certain to be dead.
	stale := onAirInfo{PID: 2147483646, Station: "ghost-node", Model: "qwen3", Started: 1}
	b, _ := json.Marshal(stale)
	if err := os.MkdirAll(filepath.Dir(onAirLockPath("brave-otter-qwen3")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onAirLockPath("brave-otter-qwen3"), b, 0600); err != nil {
		t.Fatal(err)
	}

	release, err := acquireOnAirLock("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatalf("expected stale lock to be reclaimed, got: %v", err)
	}
	defer release()

	got, err := os.ReadFile(onAirLockPath("brave-otter-qwen3"))
	if err != nil {
		t.Fatal(err)
	}
	var cur onAirInfo
	if err := json.Unmarshal(got, &cur); err != nil {
		t.Fatal(err)
	}
	if cur.PID != os.Getpid() {
		t.Fatalf("stale lock not taken over: lock still owned by pid %d", cur.PID)
	}
}

// TestProcessAlive sanity-checks the platform probe both ways.
func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive said our own process is dead")
	}
	if processAlive(2147483646) {
		t.Fatal("processAlive said an almost-certainly-dead PID is alive")
	}
}
