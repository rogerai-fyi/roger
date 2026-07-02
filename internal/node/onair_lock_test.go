package node

// onair_lock_test.go - the controller side of features/sharing/on_air_lock.feature
// (the 2026-07-02 eager-puma-54-voice double-broadcast): EVERY controller start path
// must hold the per-node-id ON-AIR lock, refuse while another LIVE process holds it,
// and release it on every stop path. The lock's ON-DISK layout is spelled out here
// deliberately (not imported), pinning that the controller and the headless CLI agree
// on the same file - that agreement IS the cross-front-end guard.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateConfig gives ONE test its own throwaway config dir (on top of the package
// TestMain baseline): the lock tests plant foreign-PID locks that must never leak
// into a sibling test's dir.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	if got, err := os.UserConfigDir(); err != nil || !strings.HasPrefix(got, dir) {
		t.Fatalf("config isolation FAILED: UserConfigDir=%q err=%v not under %q", got, err, dir)
	}
}

// lockPathFor mirrors the shared lock layout: <UserConfigDir>/rogerai/share-<node>.lock.
// Deliberately re-derived (not calling production code) so a drift in either side's
// path breaks this spec loudly.
func lockPathFor(t *testing.T, nodeID string) string {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	return filepath.Join(dir, "rogerai", "share-"+nodeID+".lock")
}

// lockInfo is the on-disk lock shape (the CLI's onAirInfo contract).
type lockInfo struct {
	PID     int    `json:"pid"`
	Station string `json:"station"`
	Model   string `json:"model"`
	Started int64  `json:"started"`
}

func writeLock(t *testing.T, nodeID string, info lockInfo) {
	t.Helper()
	p := lockPathFor(t, nodeID)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(info)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func readLock(t *testing.T, nodeID string) (lockInfo, bool) {
	t.Helper()
	b, err := os.ReadFile(lockPathFor(t, nodeID))
	if errors.Is(err, os.ErrNotExist) {
		return lockInfo{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var info lockInfo
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	return info, true
}

// TestToggleRefusedWhileAnotherLiveProcessHoldsNodeID is the incident: the node id is
// live in ANOTHER process (stood in for by our parent PID, alive and != us), so the
// controller toggle MUST refuse with the headless path's error text and start nothing.
func TestToggleRefusedWhileAnotherLiveProcessHoldsNodeID(t *testing.T) {
	isolateConfig(t)
	other := os.Getppid()
	if other == os.Getpid() || other <= 1 {
		t.Skip("no distinct live parent PID to simulate another daemon")
	}
	c := newCtrl(t, Config{})
	writeLock(t, "amber-fox-free-1", lockInfo{PID: other, Station: "systemd-unit", Model: "free-1", Started: 1})

	res := c.ToggleOnAir("free-1")
	if res.Err == nil {
		t.Fatal("toggle went on air over another live process's lock - the double-broadcast enabler")
	}
	for _, want := range []string{"already on air", `"systemd-unit"`} {
		if !strings.Contains(res.Err.Error(), want) {
			t.Fatalf("refusal %q does not name %q (must mirror the headless error text)", res.Err, want)
		}
	}
	if c.OnAirCount() != 0 {
		t.Fatalf("on-air count = %d after a refused toggle, want 0", c.OnAirCount())
	}
	// The holder's lock must be untouched.
	if info, ok := readLock(t, "amber-fox-free-1"); !ok || info.PID != other {
		t.Fatalf("the other process's lock was clobbered: %+v ok=%v", info, ok)
	}

	// TogglePrivate is the SAME start path and must refuse identically.
	c.SetLoggedIn(true)
	pres := c.TogglePrivate("free-1")
	if pres.Err == nil || !strings.Contains(pres.Err.Error(), "already on air") {
		t.Fatalf("private flip over a foreign live lock must refuse, got %+v", pres)
	}
	if c.OnAirCount() != 0 {
		t.Fatal("private flip started a session over a foreign live lock")
	}
}

// TestToggleAcquiresAndReleasesLock: on-air writes OUR lock; off-air removes it.
func TestToggleAcquiresAndReleasesLock(t *testing.T) {
	isolateConfig(t)
	c := newCtrl(t, Config{})
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("toggle on: %v", res.Err)
	}
	info, ok := readLock(t, "amber-fox-free-1")
	if !ok {
		t.Fatal("no on-air lock written for amber-fox-free-1 - a headless daemon would double-broadcast it")
	}
	if info.PID != os.Getpid() || info.Model != "free-1" || info.Station != "amber-fox" {
		t.Fatalf("lock content %+v, want our pid/station/model", info)
	}
	if res := c.ToggleOnAir("free-1"); !res.WentOff {
		t.Fatalf("toggle off: %+v", res)
	}
	if _, ok := readLock(t, "amber-fox-free-1"); ok {
		t.Fatal("lock survived the off-air toggle - the node id stays blocked forever")
	}
}

// TestStaleLockReclaimedByToggle: a crashed daemon's lock (dead PID) must not block
// the console - the toggle reclaims it and the lock becomes ours.
func TestStaleLockReclaimedByToggle(t *testing.T) {
	isolateConfig(t)
	c := newCtrl(t, Config{})
	writeLock(t, "amber-fox-free-1", lockInfo{PID: 2147483646, Station: "ghost", Model: "free-1", Started: 1})
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("stale lock not reclaimed: %v", res.Err)
	}
	if info, ok := readLock(t, "amber-fox-free-1"); !ok || info.PID != os.Getpid() {
		t.Fatalf("lock not taken over from the dead PID: %+v ok=%v", info, ok)
	}
}

// TestTogglePrivateKeepsLockAcrossRestart: the private flip stops + restarts the same
// node id in-process; it must neither self-collide nor leak the lock.
func TestTogglePrivateKeepsLockAcrossRestart(t *testing.T) {
	isolateConfig(t)
	c := newCtrl(t, Config{})
	c.SetLoggedIn(true)
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("toggle on: %v", res.Err)
	}
	pres := c.TogglePrivate("free-1")
	if pres.Err != nil || !pres.NowPrivate {
		t.Fatalf("private flip failed: %+v", pres)
	}
	if info, ok := readLock(t, "amber-fox-free-1"); !ok || info.PID != os.Getpid() {
		t.Fatalf("lock not held after the private restart: %+v ok=%v", info, ok)
	}
	// Flip back public, then off: the lock must release exactly once at the end.
	if pres = c.TogglePrivate("free-1"); pres.Err != nil || pres.NowPrivate {
		t.Fatalf("flip back public failed: %+v", pres)
	}
	if res := c.ToggleOnAir("free-1"); !res.WentOff {
		t.Fatalf("toggle off: %+v", res)
	}
	if _, ok := readLock(t, "amber-fox-free-1"); ok {
		t.Fatal("lock leaked after off-air")
	}
}

// TestStopAllReleasesEveryLock: the clean-exit path frees every held node id.
func TestStopAllReleasesEveryLock(t *testing.T) {
	isolateConfig(t)
	c := newCtrl(t, Config{})
	for _, m := range []string{"free-1", "free-2"} {
		if res := c.ToggleOnAir(m); res.Err != nil {
			t.Fatalf("toggle %s: %v", m, res.Err)
		}
	}
	c.StopAll()
	for _, id := range []string{"amber-fox-free-1", "amber-fox-free-2"} {
		if _, ok := readLock(t, id); ok {
			t.Fatalf("lock %s survived StopAll", id)
		}
	}
}

// TestFailedStartReleasesLock: agent.Start failing (dead broker) must not leave the
// node id locked - the operator retries without hand-deleting a file.
func TestFailedStartReleasesLock(t *testing.T) {
	isolateConfig(t)
	c := newCtrl(t, Config{Broker: "http://127.0.0.1:1"}) // refused port
	if res := c.ToggleOnAir("free-1"); res.Err == nil {
		t.Fatal("toggle against a dead broker unexpectedly succeeded")
	}
	if _, ok := readLock(t, "amber-fox-free-1"); ok {
		t.Fatal("lock leaked after a failed start")
	}
}
