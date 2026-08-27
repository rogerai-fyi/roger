package node

// AUTO-START: put the models I chose back on air when roger launches.
//
// Two rules carry the weight. The default is opt-OUT, so sharing a model arms it - which
// is only safe if "never decided" and "decided no" are different states. And several
// roger instances must be able to launch with the same list without fighting, which the
// per-node-id on-air lock already provides; auto-start's job is to report that calmly
// rather than as a failure.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/onair"
)

func TestSharingAModelArmsItForNextLaunch(t *testing.T) {
	c := newCtrl(t, Config{})

	if on, set := c.AutoStartFor("free-1"); on || set {
		t.Fatalf("a fresh model should be undecided, got on=%v set=%v", on, set)
	}
	res := c.ToggleOnAir("free-1")
	if res.Err != nil {
		t.Fatalf("share: %v", res.Err)
	}

	on, set := c.AutoStartFor("free-1")
	if !on || !set {
		t.Fatalf("sharing should arm the model: on=%v set=%v", on, set)
	}
	if !res.AutoStartArmed {
		t.Error("the result must SAY it armed the model - silent arming is the surprise " +
			"this flag exists to prevent")
	}
}

// The sharp edge of an opt-out default: a model the operator explicitly disarmed must
// stay disarmed even when they put it on air for one session. Without the tri-state,
// every share would silently re-arm it and there would be no way to run something once.
func TestAnExplicitlyDisarmedModelIsNotReArmedBySharing(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetAutoStart("free-1", false)

	res := c.ToggleOnAir("free-1")
	if res.Err != nil {
		t.Fatalf("share: %v", res.Err)
	}
	if on, _ := c.AutoStartFor("free-1"); on {
		t.Fatal("sharing re-armed a model the operator had turned off")
	}
	if res.AutoStartArmed {
		t.Error("the result claims it armed a model it did not")
	}
}

// Armed models are seeded from config, and the launch order is stable so a rig that hits
// the on-air cap starts the SAME subset every boot rather than a different one each time.
func TestAutoStartModelsAreSeededAndStablyOrdered(t *testing.T) {
	c := newCtrl(t, Config{AutoStart: map[string]bool{
		"paid": true, "free-2": true, "free-1": false,
	}})

	got := c.AutoStartModels()
	if len(got) != 2 || got[0] != "free-2" || got[1] != "paid" {
		t.Fatalf("armed models = %v, want [free-2 paid] in stable order", got)
	}
}

func TestAutoStartAllPutsArmedModelsOnAir(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetAutoStart("free-1", true)
	c.SetAutoStart("free-2", true)

	rep := c.AutoStartAll()
	if len(rep.Started) != 2 {
		t.Fatalf("started = %v, want both armed models", rep.Started)
	}
	for _, m := range []string{"free-1", "free-2"} {
		if !c.IsOnAir(m) {
			t.Errorf("%s was reported started but is not on air", m)
		}
	}
}

// A priced model cannot go on air signed out, and auto-start must route that to its own
// bucket rather than to Failed - "log in" and "something broke" are different messages.
func TestAutoStartSeparatesTheLoginGateFromFailure(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetAutoStart("paid", true) // newCtrl prices "paid"

	rep := c.AutoStartAll()
	if len(rep.NeedsLogin) != 1 || rep.NeedsLogin[0] != "paid" {
		t.Fatalf("needs-login = %v, want [paid]", rep.NeedsLogin)
	}
	if len(rep.Failed) != 0 {
		t.Errorf("a login gate is not a failure: %v", rep.Failed)
	}
	if c.IsOnAir("paid") {
		t.Error("a priced model went on air with nobody signed in")
	}
}

// The soft cap wins over the armed list, and the models it excluded are NAMED: an
// operator who armed three and sees one on air must be able to learn why.
func TestAutoStartRespectsTheOnAirCapAndSaysWhatItSkipped(t *testing.T) {
	c := newCtrl(t, Config{MaxOnAir: 1})
	c.SetAutoStart("free-1", true)
	c.SetAutoStart("free-2", true)

	rep := c.AutoStartAll()
	if len(rep.Started) != 1 {
		t.Fatalf("started = %v, want exactly the cap", rep.Started)
	}
	if len(rep.AtLimit) != 1 {
		t.Fatalf("at-limit = %v, want the model the cap excluded", rep.AtLimit)
	}
	if !rep.Any() {
		t.Error("a launch that skipped a model has something to report")
	}
}

// Launching twice in one process is a no-op, not a double-start: the second pass sees the
// live session and leaves it alone.
func TestAutoStartIsIdempotentWithinAProcess(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetAutoStart("free-1", true)

	c.AutoStartAll()
	rep := c.AutoStartAll()

	if len(rep.Started) != 0 || len(rep.Failed) != 0 {
		t.Fatalf("a second launch should do nothing, got started=%v failed=%v",
			rep.Started, rep.Failed)
	}
	if !c.IsOnAir("free-1") {
		t.Error("the second launch took the model off air")
	}
}

// THE MULTI-INSTANCE CASE, against the real lock rather than a mock.
//
// A second roger launching with the same armed models must not fight the first. The
// per-node-id on-air lock already guarantees that - it exists because two broadcasters on
// one node id made the broker see a station flapping between upstreams, which scrambled
// earnings attribution (the eager-puma-54-voice incident). Auto-start's only job is to
// report it as HELD rather than as a failure: a second instance finding its models
// already on air is the system working, and calling it an error teaches operators to
// ignore real ones.
func TestAutoStartReportsAModelHeldByAnotherInstance(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)

	c := newCtrl(t, Config{Station: "amber-fox"})
	c.SetAutoStart("free-1", true)

	// Stand in for the OTHER instance by writing its lockfile directly, under a live PID
	// that is NOT ours. Calling Acquire here would not work: the lock is deliberately
	// re-entrant for its own holder (`prev.PID != os.Getpid()`), so a same-process acquire
	// is a takeover, not a collision. PID 1 is always alive, which is exactly what
	// Acquire's ProcessAlive check looks for.
	node := agent.ShareNodeID("amber-fox", "free-1", 0)
	path := onair.LockPath(node)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	held, _ := json.Marshal(onair.Info{PID: 1, Station: "someone-else", Model: "free-1"})
	if err := os.WriteFile(path, held, 0o600); err != nil {
		t.Fatal(err)
	}
	// The lock lives in the shared per-package config dir, not in this test's TempDir, so
	// leaving it behind would make every later test in this package fail to go on air.
	t.Cleanup(func() { os.Remove(path) })

	rep := c.AutoStartAll()

	if len(rep.Held) != 1 || rep.Held[0] != "free-1" {
		t.Fatalf("held = %v, want [free-1] - another live process has this node id", rep.Held)
	}
	if len(rep.Failed) != 0 {
		t.Errorf("a held node id is not a failure: %v", rep.Failed)
	}
	if len(rep.Started) != 0 {
		t.Error("auto-start broadcast a node id another process already holds")
	}
}

// THE SAVE HOOK MUST NOT RUN UNDER THE LOCK.
//
// ToggleOnAir holds c.mu for its whole body through a deferred Unlock registered first, so
// anything deferred later in that body runs BEFORE the unlock. The auto-start save started
// life there, which made it a deadlock waiting for a hook that reads the controller back -
// and the real hook does exactly that, since persisting means writing the model's price
// card alongside its auto-start bit.
//
// A deadlock does not fail a test, it hangs it, so this asserts on a timeout.
func TestTheAutoStartSaveHookRunsOutsideTheLock(t *testing.T) {
	c := newCtrl(t, Config{})

	done := make(chan bool, 1)
	c.hooks.SaveAutoStart = func(model string, on bool) {
		// Any controller read is enough: this one takes c.mu.
		c.AutoStartFor(model)
		done <- on
	}

	go func() {
		c.ToggleOnAir("free-1")
	}()

	select {
	case on := <-done:
		if !on {
			t.Errorf("the hook was told on=%v, want true - sharing arms the model", on)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the auto-start save hook deadlocked: it ran while ToggleOnAir still held " +
			"c.mu, so a hook that reads the controller can never return")
	}
}
