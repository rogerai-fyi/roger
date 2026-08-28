package tui

// The launch pass, from the operator's side of the screen.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/detect"
)

// A restart must put the armed models back on air without anyone pressing anything -
// the whole point. Before this, every restart silently dropped a rig off the market.
func TestLaunchPutsArmedModelsBackOnAir(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"m1": true},
	})
	m.setShareRows(freeRows(2))

	m.runAutoStart()

	if !m.ctrl.IsOnAir("m1") {
		t.Fatal("an armed model did not come back on air at launch")
	}
	if m.ctrl.IsOnAir("m2") {
		t.Error("a model nobody armed was put on air")
	}
}

// A model the operator explicitly disarmed stays off, and the seed has to carry that -
// if only the true entries were seeded, a disarmed model would read as undecided and
// the next share would silently re-arm it.
func TestADisarmedModelIsNotStartedAtLaunch(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"m1": false},
	})
	m.setShareRows(freeRows(2))

	m.runAutoStart()

	if m.ctrl.IsOnAir("m1") {
		t.Fatal("launch started a model the operator had turned off")
	}
	if on, set := m.ctrl.AutoStartFor("m1"); !set || on {
		t.Errorf("the explicit 'no' was lost across the seed: on=%v set=%v", on, set)
	}
}

// Once per launch. A re-scan arrives on the same path, and re-running would drag a model
// the operator had just taken off air back on air - the surface silently overriding them.
func TestAutoStartRunsOnlyOncePerLaunch(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"m1": true},
	})
	m.setShareRows(freeRows(2))

	m.runAutoStart()
	m.ctrl.ToggleOnAir("m1") // the operator takes it off air
	m.runAutoStart()         // a re-scan comes in

	if m.ctrl.IsOnAir("m1") {
		t.Fatal("a second auto-start pass overrode the operator and re-started the model")
	}
}

// A quiet launch says nothing. The report exists to explain a surprise, not to fill the
// status line on every start.
func TestNothingArmedReportsNothing(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	m.setShareRows(freeRows(2))

	m.runAutoStart()

	if got := m.autoStartStatus(); got != "" {
		t.Errorf("a launch with nothing armed still said %q", got)
	}
}

// Every model that did NOT start is NAMED. A count would tell an operator something was
// skipped without telling them what, which is the worse half of saying nothing at all.
func TestTheLaunchReportNamesWhatItSkipped(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"paid": true},
	})
	m.setShareRows(freeRows(2))
	m.runAutoStart()

	// "paid" is not in the catalog at all, so it lands in Failed rather than starting.
	got := stripANSI(m.autoStartStatus())
	if !strings.Contains(got, "paid") {
		t.Errorf("the skipped model is not named in the launch report: %q", got)
	}
}

// THE IGNITION, not just the mechanism.
//
// Every test above calls m.runAutoStart() directly, which proves the machinery works and
// says NOTHING about whether anything ever calls it. It did not: runAutoStart's only caller
// was onSharesDetected, and detection ran only on an operator action - opening SHARE, typing
// /share, a wizard re-scan. So the armed models came back on air the moment you opened the
// SHARE table and not one moment before, while the release notes promised they returned "by
// themselves". A green suite hid it completely.
func TestLaunchItselfKicksTheAutoStartDetect(t *testing.T) {
	// Stub detection: without this the launch command runs a real port scan (20s) and the
	// test measures the network rather than the wiring.
	old := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) {
		return []detect.Found{{Name: "t", Chat: "http://x/v1/chat/completions", Models: []string{"m1"}}}, nil
	}
	t.Cleanup(func() { detectShares = old })

	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"m1": true},
	})

	// Run what Init actually returns and look for the launch message. Asserting the helper
	// alone would stay green if the Init wiring were deleted, which is the exact shape of
	// the bug: a correct mechanism nothing calls.
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned no commands at all")
	}
	if !yieldsAutoStartDetect(cmd) {
		t.Fatal("Init kicks no auto-start detect, so armed models only go on air when the " +
			"operator opens SHARE - which is not 'by themselves'")
	}
}

// The launch pass must not hijack the screen. An operator who launched into the tuner did
// not ask to be moved to the provider table.
func TestTheLaunchPassDoesNotChangeMode(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"m1": true},
	})
	m.setShareRows(freeRows(2))
	before := m.mode

	got, _ := m.Update(autoStartDetectedMsg{found: nil})
	after := got.(model)

	if after.mode != before {
		t.Errorf("the launch pass moved the operator from mode %v to %v; it must leave them "+
			"where they were", before, after.mode)
	}
	if !after.ctrl.IsOnAir("m1") {
		t.Error("the launch pass did not put the armed model on air")
	}
}

// A rig with nothing armed must not pay for a port scan it did not ask for.
func TestLaunchDoesNotScanWhenNothingIsArmed(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	if m.autoStartArmedAtLaunch() {
		t.Error("launch kicked a detect with no armed models - a rig that has never shared " +
			"anything should not probe its own ports at startup")
	}
}

// `/share <model>` on an ARMED model must end ON air, not off.
//
// toggleShareAt is a TOGGLE. On a session whose first detect comes from `/share
// <armed-model>`, auto-start starts it and the pending toggle then turned it straight back
// off - an explicit request to share ending with the model dark, and nothing saying why.
func TestSharePendingDoesNotToggleOffWhatAutoStartJustStarted(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"m1": true},
	})
	m.sharePending = "m1"

	found := []detect.Found{{Name: "t", Chat: "http://127.0.0.1:8010/v1/chat/completions", Models: []string{"m1"}}}
	got, _ := m.onSharesDetected(found, nil)
	after := got.(model)

	if !after.ctrl.IsOnAir("m1") {
		t.Fatal("/share on an armed model left it OFF air: auto-start started it and the " +
			"pending toggle flipped it back")
	}
}

// yieldsAutoStartDetect runs a (possibly batched) command and reports whether any branch of
// it produces the launch detect. tea.Batch returns a BatchMsg of child commands, so this
// walks one level down rather than assuming Init returns the detect directly.
func yieldsAutoStartDetect(cmd tea.Cmd) bool {
	switch msg := cmd().(type) {
	case autoStartDetectedMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if c == nil {
				continue
			}
			if _, ok := c().(autoStartDetectedMsg); ok {
				return true
			}
		}
	}
	return false
}

// ROGER USUALLY STARTS BEFORE THE MODEL SERVER DOES.
//
// The launch detect then finds nothing, and if that empty pass counted as the one
// auto-start attempt, the re-scan that finally finds the models would be refused and the
// rig would sit dark for the whole session - having already announced ON AIR. The guard is
// only spent once there was actually a catalog to work with.
func TestAnEmptyLaunchScanDoesNotSpendTheAutoStartAttempt(t *testing.T) {
	srv := fakeBroker(t)
	m := NewWithHooks(srv.URL, "tester", nil, Hooks{
		SavedAutoStart: map[string]bool{"m1": true},
	})

	// Launch: nothing detected yet.
	m.runAutoStart()
	if m.autoStartStatus() != "" {
		t.Errorf("a launch that detected nothing still reported something: %q", stripANSI(m.autoStartStatus()))
	}

	// The model server comes up and a re-scan finds it.
	m.setShareRows(freeRows(2))
	m.runAutoStart()

	if !m.ctrl.IsOnAir("m1") {
		t.Fatal("the armed model never came on air: the empty first scan consumed the " +
			"once-per-launch attempt, so the scan that actually found it was refused")
	}
}
