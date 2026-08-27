package tui

// The launch pass, from the operator's side of the screen.

import (
	"strings"
	"testing"
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
	m.runAutoStart()             // a re-scan comes in

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
