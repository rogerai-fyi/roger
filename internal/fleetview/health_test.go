package fleetview_test

// The Edge Health screen (features/edge/health.feature §2, §5, §6) as a Limoni view, tested the
// Playwright way: drive it by key in-process and assert on what the frame shows - no terminal, no
// ANSI scraping. Same honesty rules as the CLI: a metric a node does not report is a dash, never a
// zero; an offline node says for how long; a firing rule is named on the node that fires it.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thebanri/limoni/uitest"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/fleetview"
	"rogerai.fm/roger/v7/internal/store"
)

func fleet(now time.Time) []edge.NodeHealth {
	temp, load := 95.5, 3.2
	return []edge.NodeHealth{
		{ID: "n_desk", Name: "RogGentooEdged", Kind: "host", Presence: string(edge.PresenceVerified), LastHeard: now,
			Readings: &store.EdgeResources{CPUTempC: &temp, CPULoad1: &load, RAMUsedGB: 79.2, RAMTotalGB: 125, UptimeS: 14 * 86400,
				GPU: "RTX PRO 4500", GPUMemUsed: 0.43,
				Disks:  []store.EdgeDisk{{Mount: "/", UsedGB: 120, TotalGB: 400}, {Mount: "/mnt/train", UsedGB: 886, TotalGB: 960}},
				ReadAt: now.Add(-18 * time.Second).Unix()}, ReadAge: 18 * time.Second},
		{ID: "n_quiet", Name: "quiet-pi", Kind: "board", Presence: string(edge.PresenceVerified), LastHeard: now},
		{ID: "n_ibex", Name: "gentle-ibex-14", Kind: "mobile", Presence: string(edge.PresenceDark), Dark: true,
			DarkFor: 26 * time.Hour, LastHeard: now.Add(-26 * time.Hour)},
	}
}

func TestHealthScreenShowsTheFleetHonestlyAndFollowsTheSelection(t *testing.T) {
	now := time.Now()
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{
			Nodes:  fleet(now),
			Firing: map[string][]string{"n_desk": {"cpu_temp > 85", "disk > 90"}, "n_ibex": {"offline > 5"}},
			Rules:  []fleetview.RuleLine{{ID: "cpu_temp", When: "cpu_temp > 85", Scope: "fleet", Kind: "default", State: "firing: RogGentooEdged"}},
			At:     now,
		}, nil
	})
	page := uitest.Run(t, 120, 40, h.Draw)

	screen := page.Screen()
	must(t, screen, "RogGentooEdged", "95.5", "79", "125", "92%", "14d", "quiet-pi", "gentle-ibex-14", "DARK 1d")
	if strings.Contains(screen, "0.0°C") {
		t.Fatalf("a node that reports no temperature must never show zero degrees:\n%s", screen)
	}
	// The first node is selected: its readout names every reading with its age, and its alerts.
	must(t, screen, "/mnt/train", "18s ago", "▲ cpu_temp > 85", "▲ over 90", "RTX PRO 4500")
	// The rules pane names the rules in force and where alerts go (nowhere, by default).
	must(t, screen, "cpu_temp > 85", "default", "none configured")

	// Down walks to the quiet board: it reports nothing, and the readout says so - no zeros.
	page.Press("Down")
	screen = page.Screen()
	must(t, screen, "quiet-pi   board", "no health readings")
	if strings.Contains(screen, "0 / 0 GB") || strings.Contains(screen, "0%") {
		t.Fatalf("no readings must not be drawn as zeros:\n%s", screen)
	}

	// Down to the dark phone: offline for how long, when last heard, and its offline alert.
	page.Press("Down")
	screen = page.Screen()
	must(t, screen, "gentle-ibex-14   phone", "DARK 1d", "last heard", "▲ offline > 5")

	// Down at the end stays put; Up walks back; q quits.
	page.Press("Down")
	page.Press("Up")
	must(t, page.Screen(), "quiet-pi   board")
	page.Press("q")
	page.ExpectExit()
}

func TestHealthScreenShowsASourceErrorInsteadOfAStaleOrEmptyFleet(t *testing.T) {
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{}, errors.New("the fleet store is locked by another roger")
	})
	page := uitest.Run(t, 100, 30, h.Draw)
	must(t, page.Screen(), "could not read the fleet", "locked by another roger")
}

func TestHealthScreenRefreshesFromItsSourceOnATimerNotOnlyOnKeys(t *testing.T) {
	now := time.Now()
	calls := 0
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		calls++
		n := fleet(now)[:1]
		if calls > 1 {
			n[0].Name = "RogGentooEdged-renamed"
		}
		return fleetview.HealthSnapshot{Nodes: n, At: now}, nil
	})
	h.Every = 0 // re-read on every frame, so the test needs no clock
	page := uitest.Run(t, 120, 40, h.Draw)
	must(t, page.Screen(), "RogGentooEdged")
	page.Press("r") // any frame after the first re-reads
	must(t, page.Screen(), "RogGentooEdged-renamed")
}

func must(t *testing.T, screen string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(screen, w) {
			t.Fatalf("expected the screen to show %q; screen:\n%s", w, screen)
		}
	}
}

// The rules are edited on the HEALTH screen: a adds one (<metric> <gt|lt> <value> [fleet|<unit>]),
// [ ] walk the rules, o turns the chosen one off or on, x removes an owner rule - each through the
// handed-in functions, never a rule invented on screen.
func TestHealthScreenEditsRulesInPlace(t *testing.T) {
	now := time.Now()
	var set, toggled, removed []string
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: fleet(now), At: now, Rules: []fleetview.RuleLine{
			{ID: "cpu_temp", When: "cpu_temp > 85", Scope: "fleet", Kind: "default", State: "quiet"},
			{ID: "disk-fleet", When: "disk > 80", Scope: "fleet", Kind: "owner", State: "quiet"},
		}}, nil
	})
	h.Actions = fleetview.HealthActions{
		SetRule: func(metric, op, value, scope string) (string, error) {
			set = append(set, metric+" "+op+" "+value+" "+scope)
			return "rule " + metric + "-fleet set", nil
		},
		RuleOff:    func(id string, off bool) error { toggled = append(toggled, fmt.Sprintf("%s=%v", id, off)); return nil },
		RemoveRule: func(id string) error { removed = append(removed, id); return nil },
	}
	page := uitest.Run(t, 130, 44, h.Draw)
	must(t, page.Screen(), "a add rule")
	page.Press("a")
	must(t, page.Screen(), "add rule", "metric")
	page.Type("memory gt 90 fleet")
	page.Press("Enter")
	if len(set) != 1 || set[0] != "memory gt 90 fleet" {
		t.Fatalf("set = %v", set)
	}
	must(t, page.Screen(), "rule memory-fleet set")
	page.Press("o") // the first rule (the default cpu_temp) goes off
	if len(toggled) != 1 || toggled[0] != "cpu_temp=true" {
		t.Fatalf("toggled = %v", toggled)
	}
	page.Press("]") // to the owner rule
	page.Press("x")
	if len(removed) != 1 || removed[0] != "disk-fleet" {
		t.Fatalf("removed = %v", removed)
	}
	page.Press("[")
	page.Press("x") // a default cannot be removed: nothing happens, the screen says so
	if len(removed) != 1 {
		t.Fatalf("a default is never removed: %v", removed)
	}
	must(t, page.Screen(), "a default is turned off, not removed")
}

// features/edge/health.feature: a throttled or undervolted Pi shows the flag ON THE NODE, in words.
func TestHealthScreenShowsAPiShortOfPower(t *testing.T) {
	now := time.Now()
	word, wear := uint32(0x50005), 30.0
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: []edge.NodeHealth{{ID: "n_pi", Name: "greenhouse", Kind: "board",
			Presence: string(edge.PresenceVerified), LastHeard: now,
			Readings: &store.EdgeResources{PiPower: &word, SDWearPct: &wear, ReadAt: now.Unix()}}}, At: now}, nil
	})
	page := uitest.Run(t, 120, 40, h.Draw)
	must(t, page.Screen(), "POWER", "under-voltage, throttled NOW", "30% worn")
}

// features/edge/node_identity.feature: the HEALTH screen calls a unit what the rack calls it (the
// snapshot's Names), never its id - not even a cut of one.
func TestHealthScreenNamesAnUnnamedUnitNeverByID(t *testing.T) {
	now := time.Now()
	id := "n_ebab8e241f058e18234af760ad2a1ca976647af7583e0029"
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: []edge.NodeHealth{{ID: id, Name: id, Kind: "board", LastHeard: now}},
			Names: map[string]string{id: "Pi 4"}, At: now,
			Rules: []fleetview.RuleLine{{ID: "disk-own", When: "disk > 70", Scope: "Pi 4", ScopeID: id, Kind: "owner", State: "firing: Pi 4"}},
			Sinks: "sinks: read by Pi 4 (last read 4d ago)"}, nil
	})
	page := uitest.Run(t, 120, 30, h.Draw)
	s := page.Screen()
	if strings.Contains(s, "n_eb") {
		t.Fatalf("an id was drawn as a name:\n%s", s)
	}
	must(t, s, "Pi 4   board", "firing on Pi 4", "read by Pi 4")
}
