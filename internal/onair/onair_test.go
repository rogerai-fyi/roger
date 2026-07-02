package onair

// The lock semantics ported from cmd/rogerai's approved on-air lock spec (the CLI
// keeps wrapper-level tests): live-holder refusal, per-node-id keying, release
// lifecycle, stale reclaim, and the release-only-while-ours guard.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useTempConfig isolates os.UserConfigDir to a throwaway dir on EVERY platform and
// verifies it took (the CLI's `b.example` lesson).
func useTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	if got := LockPath("probe"); !strings.HasPrefix(got, dir) {
		t.Fatalf("config isolation FAILED: LockPath()=%q not under %q", got, dir)
	}
	return dir
}

// TestAcquireBlocksAnotherLiveProcess: a lock held by a DIFFERENT, still-alive
// process must block a fresh acquire, and the error must name the holder + the
// lock path (the operator's escape hatch).
func TestAcquireBlocksAnotherLiveProcess(t *testing.T) {
	useTempConfig(t)
	other := os.Getppid()
	if other == os.Getpid() || !ProcessAlive(other) {
		t.Skip("no distinct live parent PID to simulate another daemon")
	}
	held := Info{PID: other, Station: "other-daemon", Model: "qwen3", Started: 1}
	b, _ := json.Marshal(held)
	if err := os.MkdirAll(filepath.Dir(LockPath("brave-otter-qwen3")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LockPath("brave-otter-qwen3"), b, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Acquire("brave-otter-qwen3", "brave-otter", "qwen3")
	if err == nil {
		t.Fatal("expected acquire to be blocked by another live process's lock")
	}
	for _, want := range []string{"already on air", `"other-daemon"`, LockPath("brave-otter-qwen3")} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
}

// TestAcquireDistinctNodesDoNotCollide: per-node-id keying - a rig running two
// models holds both locks at once.
func TestAcquireDistinctNodesDoNotCollide(t *testing.T) {
	useTempConfig(t)
	relA, err := Acquire("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatalf("acquire model A failed: %v", err)
	}
	defer relA()
	relB, err := Acquire("brave-otter-gptoss", "brave-otter", "gpt-oss")
	if err != nil {
		t.Fatalf("acquire model B (distinct node id) was wrongly blocked: %v", err)
	}
	defer relB()
}

// TestAcquireReleaseLifecycle: acquire writes the lock with OUR identity, release
// removes it, and the next acquire then succeeds.
func TestAcquireReleaseLifecycle(t *testing.T) {
	useTempConfig(t)
	release, err := Acquire("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	b, err := os.ReadFile(LockPath("brave-otter-qwen3"))
	if err != nil {
		t.Fatalf("lockfile not written: %v", err)
	}
	var info Info
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	if info.PID != os.Getpid() || info.Station != "brave-otter" || info.Model != "qwen3" || info.Started == 0 {
		t.Fatalf("lock content %+v, want our pid/station/model + a start stamp", info)
	}
	release()
	release() // idempotent (sync.Once)
	if _, err := os.Stat(LockPath("brave-otter-qwen3")); !os.IsNotExist(err) {
		t.Fatalf("release did not remove the lockfile (stat err=%v)", err)
	}
	release2, err := Acquire("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	release2()
}

// TestAcquireReclaimsStaleLock: crash recovery - a lock whose owning PID is no
// longer alive is reclaimed, not treated as a live session. A malformed lock file
// is reclaimed the same way (it can never prove a live holder).
func TestAcquireReclaimsStaleLock(t *testing.T) {
	useTempConfig(t)
	for name, content := range map[string]string{
		"dead pid":  `{"pid":2147483646,"station":"ghost-node","model":"qwen3","started":1}`,
		"malformed": `{not json`,
		"zero pid":  `{"pid":0}`,
	} {
		if err := os.MkdirAll(filepath.Dir(LockPath("brave-otter-qwen3")), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(LockPath("brave-otter-qwen3"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		release, err := Acquire("brave-otter-qwen3", "brave-otter", "qwen3")
		if err != nil {
			t.Fatalf("%s: expected the stale lock to be reclaimed, got: %v", name, err)
		}
		var cur Info
		b, _ := os.ReadFile(LockPath("brave-otter-qwen3"))
		if json.Unmarshal(b, &cur) != nil || cur.PID != os.Getpid() {
			t.Fatalf("%s: stale lock not taken over (now %+v)", name, cur)
		}
		release()
	}
}

// TestReleaseNeverDeletesANewerBroadcastersLock: a slow shutdown's release must not
// remove a lock that a FRESH process already reclaimed from us.
func TestReleaseNeverDeletesANewerBroadcastersLock(t *testing.T) {
	useTempConfig(t)
	release, err := Acquire("brave-otter-qwen3", "brave-otter", "qwen3")
	if err != nil {
		t.Fatal(err)
	}
	// A newer broadcaster (different PID) takes the lock over underneath us.
	newer := Info{PID: os.Getpid() + 1, Station: "fresh", Model: "qwen3", Started: 2}
	nb, _ := json.Marshal(newer)
	if err := os.WriteFile(LockPath("brave-otter-qwen3"), nb, 0600); err != nil {
		t.Fatal(err)
	}
	release()
	b, err := os.ReadFile(LockPath("brave-otter-qwen3"))
	if err != nil {
		t.Fatalf("our stale release deleted the newer broadcaster's lock: %v", err)
	}
	var cur Info
	if json.Unmarshal(b, &cur) != nil || cur.PID != newer.PID {
		t.Fatalf("newer lock clobbered: %+v", cur)
	}
}

// TestLockSlugKeepsNodeIDsFilesystemSafe: the filename slug maps anything outside
// [a-zA-Z0-9._-] to '-', and LockPath keys on it.
func TestLockSlugKeepsNodeIDsFilesystemSafe(t *testing.T) {
	useTempConfig(t)
	for in, want := range map[string]string{
		"brave-otter-qwen3": "brave-otter-qwen3",
		"a b/c:d":           "a-b-c-d", // moved from cmd/rogerai TestLockSlugSanitizes

		"st/../../etc/passwd":   "st-..-..-etc-passwd",
		"weird id:with*chars?":  "weird-id-with-chars-",
		"UPPER.case_ok-123":     "UPPER.case_ok-123",
		"emoji\U0001F600model":  "emoji-model",
		"space station-model x": "space-station-model-x",
	} {
		if got := lockSlug(in); got != want {
			t.Fatalf("lockSlug(%q) = %q, want %q", in, got, want)
		}
		p := LockPath(in)
		if filepath.Base(p) != "share-"+lockSlug(in)+".lock" {
			t.Fatalf("LockPath(%q) = %q, want a share-<slug>.lock basename", in, p)
		}
		if filepath.Dir(p) != filepath.Join(mustConfigDir(t), "rogerai") {
			t.Fatalf("LockPath(%q) escaped the rogerai config dir: %q", in, p)
		}
	}
}

func mustConfigDir(t *testing.T) string {
	t.Helper()
	d, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestAcquireStationlessHolderNamesThisMachine: a live holder whose lock recorded
// no station is still named clearly ("this machine"), never an empty %q.
func TestAcquireStationlessHolderNamesThisMachine(t *testing.T) {
	useTempConfig(t)
	other := os.Getppid()
	if other == os.Getpid() || !ProcessAlive(other) {
		t.Skip("no distinct live parent PID to simulate another daemon")
	}
	held := Info{PID: other, Model: "m1", Started: 1} // no station recorded
	b, _ := json.Marshal(held)
	if err := os.MkdirAll(filepath.Dir(LockPath("brave-otter-m1")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LockPath("brave-otter-m1"), b, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Acquire("brave-otter-m1", "brave-otter", "m1")
	if err == nil || !strings.Contains(err.Error(), `"this machine"`) {
		t.Fatalf("stationless refusal = %v, want it to name %q", err, "this machine")
	}
}

// TestAcquireSurfacesFilesystemErrors: an un-creatable config dir (path under a
// regular FILE) and an un-writable one both fail the acquire loudly instead of
// pretending the lock is held.
func TestAcquireSurfacesFilesystemErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// UserConfigDir resolves under the FILE: MkdirAll must fail (ENOTDIR).
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(blocker, "sub"))
	t.Setenv("HOME", filepath.Join(blocker, "sub"))
	t.Setenv("AppData", filepath.Join(blocker, "sub"))
	if _, err := Acquire("brave-otter-m1", "brave-otter", "m1"); err == nil {
		t.Fatal("Acquire under an un-creatable config dir must error")
	}

	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only dir does not block writes")
	}
	rw := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", rw)
	t.Setenv("HOME", rw)
	t.Setenv("AppData", rw)
	locked := filepath.Dir(LockPath("brave-otter-m1"))
	if err := os.MkdirAll(locked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0500); err != nil { // dir exists but is read-only
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0700) })
	if _, err := Acquire("brave-otter-m1", "brave-otter", "m1"); err == nil {
		t.Fatal("Acquire into a read-only config dir must error")
	}
}

// TestProcessAlive sanity-checks the platform probe both ways, plus the
// exists-but-not-ours (EPERM) reading: PID 1 is alive whoever asks.
func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("ProcessAlive said our own process is dead")
	}
	if ProcessAlive(2147483646) {
		t.Fatal("ProcessAlive said an almost-certainly-dead PID is alive")
	}
	if !ProcessAlive(1) {
		t.Fatal("ProcessAlive said PID 1 (init) is dead - the EPERM reading is broken")
	}
}
