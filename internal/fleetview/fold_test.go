package fleetview_test

// HEALTH FOLDED INTO FLEET (features/edge/fleet_window.feature, WHERE IT LIVES: "the Limoni FLEET
// tab absorbs HEALTH, because they are two lists of the same units today"). There is no HEALTH tab
// in the strip any more; the key that opened it (6) still works and lands on FLEET with the
// selected unit's health open; and every fact the HEALTH screen showed is on the FLEET tab, on the
// unit's row or on its health page. Ground truth for the facts: features/edge/health.feature.

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/thebanri/limoni/uitest"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/fleetview"
	"rogerai.fm/roger/v6/internal/store"
)

// foldNodes is the HEALTH screen's own fixture (fleet) plus a Pi short of power and a store box
// whose disk is nearly full, so every health fact has a unit that shows it.
func foldNodes(now time.Time) []edge.NodeHealth {
	word, wear := uint32(0x50005), 30.0
	storeTemp, storeLoad := 48.0, 0.4
	return append(fleet(now),
		edge.NodeHealth{ID: "n_pi", Name: "greenhouse", Kind: "board", Presence: string(edge.PresenceVerified), LastHeard: now,
			Readings: &store.EdgeResources{PiPower: &word, SDWearPct: &wear, ReadAt: now.Unix()}},
		edge.NodeHealth{ID: "n_store", Name: "storebox", Kind: "host", Presence: string(edge.PresenceVerified), LastHeard: now,
			Readings: &store.EdgeResources{CPUTempC: &storeTemp, CPULoad1: &storeLoad, RAMUsedGB: 8, RAMTotalGB: 16,
				Disks: []store.EdgeDisk{{Mount: "/srv", UsedGB: 920, TotalGB: 1000}}, ReadAt: now.Unix()}},
	)
}

var foldFiring = map[string][]string{"n_desk": {"cpu_temp > 85", "disk > 90"}, "n_ibex": {"offline > 5"}}

var foldRules = []fleetview.RuleLine{
	{ID: "cpu_temp", When: "cpu_temp > 85", Scope: "fleet", Kind: "default", State: "firing: RogGentooEdged"},
	{ID: "disk-fleet", When: "disk > 80", Scope: "fleet", Kind: "owner", State: "quiet"},
}

// foldSnap is the rack over the same units: the desk is the hub, the rest are members.
func foldSnap(now time.Time) fleetview.FleetSnapshot {
	nodes := foldNodes(now)
	snap := fleetview.FleetSnapshot{At: now, Self: fleetview.SelfFacts{Name: "RogGentooEdged", Root: "LOCAL ROOT", Authority: "authority · LOCAL"}}
	for i := range nodes {
		n := nodes[i]
		it := fleetview.FleetItem{Kind: fleetview.ItemMember, ID: n.ID, Name: n.Name, NodeKind: n.Kind, Dark: n.Dark,
			Presence: "online", LastSeen: n.LastHeard, Health: &n, Alerts: foldFiring[n.ID], Controllable: true}
		if i == 0 {
			it.Kind, it.LastSeen = fleetview.ItemHub, time.Time{}
		}
		if n.Dark {
			it.Presence = "offline 1d"
		}
		snap.Items = append(snap.Items, it)
	}
	return snap
}

type ruleRec struct{ set, toggled, removed []string }

func foldFleet(t *testing.T, w, h uint16, rec *ruleRec) (*fleetview.Fleet, *uitest.Page) {
	t.Helper()
	now := time.Now()
	hs := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: foldNodes(now), Firing: foldFiring, Rules: foldRules, At: now}, nil
	})
	if rec != nil {
		hs.Actions = fleetview.HealthActions{
			SetRule: func(metric, op, value, scope string) (string, error) {
				rec.set = append(rec.set, metric+" "+op+" "+value+" "+scope)
				return "rule " + metric + "-fleet set", nil
			},
			RuleOff: func(id string, off bool) error {
				rec.toggled = append(rec.toggled, fmt.Sprintf("%s=%v", id, off))
				return nil
			},
			RemoveRule: func(id string) error { rec.removed = append(rec.removed, id); return nil },
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return foldSnap(now), nil }, hs, (&recorder{}).actions())
	return f, uitest.Run(t, w, h, f.Draw)
}

// strip is the brand band's first line: the tab strip and the facts.
func strip(screen string) string {
	line, _, _ := strings.Cut(screen, "\n")
	return line
}

func TestTheTabStripHasNoHealthTabAndFitsEighty(t *testing.T) {
	for _, w := range []uint16{80, 100, 130, 160} {
		t.Run(fmt.Sprintf("%d columns", w), func(t *testing.T) {
			_, page := foldFleet(t, w, 44, nil)
			head := strip(page.Screen())
			require.Contains(t, head, "FLEET", "the tab in force reads in full")
			require.NotContains(t, strings.ToLower(head), "health", "HEALTH is folded into FLEET, not a tab")
			require.NotContains(t, head, "hea ", "not even shortened")
			require.Contains(t, head, "▲ 3", "the alert count survives")
			require.LessOrEqual(t, utf8.RuneCountInString(head), int(w), "the header fits")
		})
	}
}

// 6 opened HEALTH; it still works, from every tab, and lands on FLEET with the selected unit's
// health open. The other digits keep their meaning (7 is still EVENTS), and Tab never stops on a
// health tab that is not there.
func TestTheHealthKeyIsAnAliasThatLandsOnFleet(t *testing.T) {
	for _, from := range []struct{ name, key string }{
		{"fleet", "1"}, {"andon", "2"}, {"trouble", "3"}, {"grants", "4"}, {"sessions", "5"}, {"events", "7"},
	} {
		t.Run("from "+from.name, func(t *testing.T) {
			_, page := foldFleet(t, 130, 44, nil)
			page.Press(from.key)
			page.Press("6")
			s := page.Screen()
			require.Contains(t, strip(s), "FLEET", "6 lands on FLEET")
			require.NotContains(t, strings.ToLower(strip(s)), "health")
			require.Contains(t, s, "esc back to the rack", "with the health page open")
			require.Contains(t, s, "/mnt/train", "of the selected unit")
			require.False(t, page.Exited())
		})
	}
	_, page := foldFleet(t, 130, 44, nil)
	page.Press("7")
	require.Contains(t, strip(page.Screen()), "EVENTS", "7 is still EVENTS: nobody's digits moved")
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		page.Press("tab")
		for _, w := range strings.Fields(strip(page.Screen())) {
			if w == strings.ToUpper(w) && len(w) > 3 {
				seen[w] = true
			}
		}
	}
	require.False(t, seen["HEALTH"], "Tab never lands on a HEALTH tab: %v", seen)
}

// Every fact the HEALTH screen showed, unit by unit, is on the FLEET tab's health page.
func TestEveryHealthFactIsOnTheFleetTab(t *testing.T) {
	for _, c := range []struct {
		unit  string
		wants []string
		never []string
	}{
		{"n_desk", []string{
			"RogGentooEdged", "95.5°C", "▲ cpu_temp > 85", // temperature, and the rule firing on it
			"3.20",                  // load
			"79.2 of 125 GB", "63%", // memory
			"/mnt/train", "92%", "▲ over 90", "/", "30%", // every disk with its mount, and the one over the line
			"RTX PRO 4500", "43% memory", // the GPU
			"readings 18s ago", "last heard", "up 14d", // every reading has its age, and the uptime
			"TROUBLE",
		}, nil},
		{"n_quiet", []string{"quiet-pi", "board", "no health readings"}, []string{"0.0°C", "0 / 0 GB"}},
		{"n_ibex", []string{"gentle-ibex-14", "phone", "DARK 1d", "last heard", "▲ offline > 5"}, nil},
		{"n_pi", []string{"greenhouse", "POWER", "under-voltage, throttled NOW", "30% worn"}, nil},
		{"n_store", []string{"storebox", "/srv", "92%", "48.0°C", "8.0 of 16 GB"}, nil},
	} {
		t.Run(c.unit, func(t *testing.T) {
			f, page := foldFleet(t, 130, 44, nil)
			f.SelectUnit(c.unit)
			page.Press("6")
			s := page.Screen()
			for _, w := range c.wants {
				require.Contains(t, s, w, "screen:\n%s", s)
			}
			for _, w := range c.never {
				require.NotContains(t, s, w, "no reading is drawn as a zero:\n%s", s)
			}
			// The rules in force and where alerts go, on every unit's health.
			for _, w := range []string{"RULES", "cpu_temp > 85", "default", "firing on RogGentooEdged", "disk > 80", "owner", "none configured"} {
				require.Contains(t, s, w, "screen:\n%s", s)
			}
		})
	}
}

// The health page walks the units with the arrows (the rack's own selection), and esc goes back
// to the rack with the same unit selected rather than quitting; q still quits.
func TestTheHealthPageWalksTheRackAndEscComesBack(t *testing.T) {
	f, page := foldFleet(t, 130, 44, nil)
	f.SelectUnit("n_pi")
	page.Press("6")
	require.Contains(t, page.Screen(), "under-voltage, throttled NOW")
	page.Press("esc")
	s := page.Screen()
	require.False(t, page.Exited(), "esc on the health page goes back, it does not quit")
	require.Contains(t, s, "ON THE RACK", "back on the rack")
	require.NotContains(t, s, "esc back to the rack")
	require.Contains(t, s, "greenhouse", "the same unit is still selected")
	page.Press("enter") // enter opens the selected unit's health from the rack too
	require.Contains(t, page.Screen(), "under-voltage, throttled NOW", "enter opens the same unit's health")
	page.Press("down")
	require.NotContains(t, page.Screen(), "under-voltage, throttled NOW", "the arrows walk to the next unit")
	page.Press("1")
	require.NotContains(t, page.Screen(), "esc back to the rack", "1 is the rack itself")
	page.Press("q")
	page.ExpectExit()
}

// The rules are edited where they are shown: a adds, [ ] walk, o turns off or on, x removes an
// owner rule and refuses a default - through the handed-in functions, as on the HEALTH screen.
func TestTheRulesAreEditedOnTheFleetTab(t *testing.T) {
	rec := &ruleRec{}
	_, page := foldFleet(t, 130, 44, rec)
	page.Press("6")
	s := page.Screen()
	require.Contains(t, s, "a add rule")
	page.Press("a")
	page.Type("memory gt 90 q1") // q and 1 belong to the prompt, not quit and a tab
	page.Press("enter")
	require.Equal(t, []string{"memory gt 90 q1"}, rec.set)
	require.False(t, page.Exited())
	require.Contains(t, page.Screen(), "rule memory-fleet set")
	page.Press("o")
	require.Equal(t, []string{"cpu_temp=true"}, rec.toggled)
	page.Press("]")
	page.Press("x")
	require.Equal(t, []string{"disk-fleet"}, rec.removed)
	page.Press("[")
	page.Press("x")
	require.Equal(t, []string{"disk-fleet"}, rec.removed, "a default is never removed")
	require.Contains(t, page.Screen(), "a default is turned off, not removed")
}

// A unit's row carries its disk as well as its temperature and memory, where the row has room.
func TestARackRowCarriesTheDiskBesideTemperatureAndMemory(t *testing.T) {
	_, page := foldFleet(t, 200, 44, nil)
	var row string
	for _, ln := range strings.Split(page.Screen(), "\n") {
		if strings.Contains(ln, "storebox") {
			row = ln
		}
	}
	require.NotEmpty(t, row)
	require.Contains(t, row, "48°")
	require.Contains(t, row, "50%", "memory")
	require.Contains(t, row, "92%", "disk")
}

// The health page draws a unit's readings over the session, as the HEALTH screen did: a new
// reading is a new point, a re-read of the same reading is not.
func TestTheHealthPageKeepsTheSessionHistory(t *testing.T) {
	now := time.Now()
	calls := 0
	hs := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		calls++
		n := foldNodes(now)
		v := 90.0 + float64(calls)
		n[0].Readings.CPUTempC = &v
		n[0].Readings.ReadAt = now.Unix() + int64(calls)
		return fleetview.HealthSnapshot{Nodes: n, At: now}, nil
	})
	hs.Every = 0
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return foldSnap(now), nil }, hs, fleetview.FleetActions{})
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("6")
	page.Press("r")
	page.Press("r")
	require.Contains(t, page.Screen(), "reads")
}

// With no health read handed in, a unit's health still opens with enter, from the rack's own
// readings: the values, the ages, the last hour, the alarms - and no rules pane it cannot read.
// 6 then keeps its old meaning, EVENTS, because there was never a HEALTH tab to fold.
func TestAUnitsHealthOpensFromTheRacksOwnReadings(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.Items[0].TempHour = []float64{70, 80, 95}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, fleetview.FleetActions{})
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("enter")
	s := page.Screen()
	for _, w := range []string{"esc back to the rack", "RogGentooEdged", "95.5°C", "▲ cpu_temp > 85", "80.0 of 125 GB", "TEMP 1H", "70° to 95°"} {
		require.Contains(t, s, w, "screen:\n%s", s)
	}
	require.NotContains(t, s, "RULES", "no rules are drawn that were never read")
	page.Press("down") // past the rogers, to the dark phone: dark since it was last heard
	s = page.Screen()
	require.Contains(t, s, "gentle-ibex-14")
	require.Contains(t, s, "DARK 1d")
	require.Contains(t, s, "▲ offline > 5")
	page.Press("down")
	page.Press("down") // past the last unit: it stays put on nas, the bench is not a unit
	require.Contains(t, page.Screen(), "nas")
	require.Contains(t, page.Screen(), "no health readings")

	g := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return fleetSnap(now), nil }, nil, fleetview.FleetActions{})
	p2 := uitest.Run(t, 130, 44, g.Draw)
	p2.Press("6")
	require.Contains(t, strip(p2.Screen()), "EVENTS")
	p2.Press("1")
	p2.Press("down") // a roger: enter opens nothing, a roger reports no health of its own
	p2.Press("enter")
	require.NotContains(t, p2.Screen(), "esc back to the rack")
}

// 6 on a roger or the bench opens the nearest unit's health, never a roger's (it has none); a rack
// with no unit at all says nothing reports yet.
func TestTheHealthKeyOnARogerOpensTheNearestUnit(t *testing.T) {
	now := time.Now()
	snap := foldSnap(now)
	snap.Items = append(snap.Items[:1], append([]fleetview.FleetItem{{Kind: fleetview.ItemInstance, Name: "roggentooedged"}}, snap.Items[1:]...)...)
	hs := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: foldNodes(now), At: now}, nil
	})
	g := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, hs, fleetview.FleetActions{})
	page := uitest.Run(t, 130, 44, g.Draw)
	page.Press("down") // the roger under the hub
	page.Press("6")
	require.Contains(t, page.Screen(), "/mnt/train", "the hub, the nearest unit")

	bench := fleetview.FleetSnapshot{At: now, Items: []fleetview.FleetItem{{Kind: fleetview.ItemCandidate, ID: "n_fox", Name: "blue-fox-3", NodeKind: "mobile"}}}
	b := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return bench, nil }, hs, fleetview.FleetActions{})
	p3 := uitest.Run(t, 130, 44, b.Draw)
	p3.Press("6")
	require.Contains(t, p3.Screen(), "nothing on this Edge reports its health yet")
}
