package fleetview_test

// The Edge FLEET screen in Limoni: the map of this machine, its rogers, the members and the
// discovered devices - and the actions that manage them (adopt, rename, forget, prefer), driven
// by key in-process and asserted on the frame. The actions are functions handed in, so the test
// sees exactly what the owner's key would do to the Edge.

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/thebanri/limoni/uitest"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/fleetview"
	"rogerai.fm/roger/v6/internal/store"
)

func fleetSnap(now time.Time) fleetview.FleetSnapshot {
	temp := 95.5
	return fleetview.FleetSnapshot{
		Self: fleetview.SelfFacts{Name: "RogGentooEdged", Root: "LOCAL ROOT", Authority: "authority · LOCAL",
			Prefer: "prefers local", Discovery: "scanning this network every 30s · the last pass found nothing new · run roger edge scan to look now", Enrolled: true},
		Items: []fleetview.FleetItem{
			{Kind: fleetview.ItemHub, ID: "n_desk", Name: "RogGentooEdged", NodeKind: "host", Presence: "online",
				Alerts: []string{"cpu_temp > 85"},
				Health: &edge.NodeHealth{Readings: &store.EdgeResources{CPUTempC: &temp, RAMUsedGB: 80, RAMTotalGB: 125}}},
			{Kind: fleetview.ItemInstance, Name: "roggentooedged", Self: true, Caps: []fleetview.Cap{{"operate", "VERIFIED"}}},
			{Kind: fleetview.ItemAgent, Name: "roggentooedged-2", Bands: []string{"qwen3.8-27b"}, Job: "serve qwen3.8-27b",
				Caps: []fleetview.Cap{{"serve", "VERIFIED"}, {"operate", "CLAIMED"}}},
			{Kind: fleetview.ItemAgent, Name: "watcher", Dark: true},
			{Kind: fleetview.ItemMember, ID: "n_nas", Name: "nas", NodeKind: "host", Presence: "online",
				Caps: []fleetview.Cap{{"serve", "VERIFIED"}}, Bands: []string{"gpt-oss-20b"}, LastSeen: now.Add(-20 * time.Second), Controllable: true},
			{Kind: fleetview.ItemMember, ID: "n_ibex", Name: "gentle-ibex-14", NodeKind: "mobile", Presence: "offline 1d", Dark: true,
				Alerts: []string{"offline > 5"}, LastSeen: now.Add(-26 * time.Hour)},
			{Kind: fleetview.ItemCandidate, ID: "n_fox", Name: "blue-fox-3", NodeKind: "mobile", Presence: "discovered"},
		},
		At: now,
	}
}

type recorder struct {
	adopted, forgot, renamed, prefer []string
	fail                             error
}

func (r *recorder) actions() fleetview.FleetActions {
	return fleetview.FleetActions{
		Adopt: func(id, name string) (string, error) {
			r.adopted = append(r.adopted, id)
			return "adopted " + name, r.fail
		},
		Forget:    func(id string) error { r.forgot = append(r.forgot, id); return r.fail },
		Rename:    func(id, name string) error { r.renamed = append(r.renamed, id+"="+name); return r.fail },
		SetPrefer: func(want string) error { r.prefer = append(r.prefer, want); return r.fail },
	}
}

func newFleet(t *testing.T, rec *recorder) (*fleetview.Fleet, *uitest.Page) {
	t.Helper()
	now := time.Now()
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return fleetSnap(now), nil }, nil, rec.actions())
	return f, uitest.Run(t, 130, 44, f.Draw)
}

func TestFleetScreenDrawsTheWholeEdgeWithItsFacts(t *testing.T) {
	_, page := newFleet(t, &recorder{})
	s := page.Screen()
	must(t, s, "ROGER", "EDGE", "RogGentooEdged", "go local first")
	must(t, s, "roggentooedged", "this one", "roggentooedged-2", "qwen3.8-27b", "watcher", "offline")
	must(t, s, "ON THE RACK", "nas", "gentle-ibex-14", "DARK 1d", "ON THE BENCH", "blue-fox-3", "adopt")
	must(t, s, "▲ 1", "▲ 2", "scanning this network")
	// The hub is selected first: its readout names its role, its alert on the temperature meter, its health.
	must(t, s, "this machine", "▲ cpu_temp > 85", "95.5")
	// A long fact wraps in the readout; it is never cut at the pane edge.
	must(t, s, "scanning this network every 30s")
}

func TestFleetScreenActionsGoThroughTheHandedInFunctions(t *testing.T) {
	rec := &recorder{}
	_, page := newFleet(t, rec)
	// Walk to the candidate (last item) and adopt it.
	for i := 0; i < 6; i++ {
		page.Press("Down")
	}
	must(t, page.Screen(), "blue-fox-3", "on the bench", "a adopt")
	page.Press("a")
	if len(rec.adopted) != 1 || rec.adopted[0] != "n_fox" {
		t.Fatalf("adopted = %v", rec.adopted)
	}
	must(t, page.Screen(), "adopted blue-fox-3")

	// The rack is worst first (fleet_window.feature): the dark phone sits above the healthy nas.
	// Up to nas: rename it in place.
	page.Press("Up")
	page.Press("n")
	must(t, page.Screen(), "new name")
	page.Type("nas-2")
	page.Press("Enter")
	if len(rec.renamed) != 1 || rec.renamed[0] != "n_nas=nas-2" {
		t.Fatalf("renamed = %v", rec.renamed)
	}

	// Up to the dark phone: forget asks first; n cancels, y forgets.
	page.Press("Up")
	must(t, page.Screen(), "gentle-ibex-14", "patched in", "f forget")
	page.Press("f")
	must(t, page.Screen(), "forget gentle-ibex-14?")
	page.Press("n")
	if len(rec.forgot) != 0 {
		t.Fatalf("n must cancel, forgot = %v", rec.forgot)
	}
	page.Press("f")
	page.Press("y")
	if len(rec.forgot) != 1 || rec.forgot[0] != "n_ibex" {
		t.Fatalf("forgot = %v", rec.forgot)
	}

	// p cycles where a turn goes first, through the handed-in setter.
	page.Press("p")
	if len(rec.prefer) != 1 || rec.prefer[0] != "market" {
		t.Fatalf("prefer = %v (local -> market first)", rec.prefer)
	}
	must(t, page.Screen(), "market")
}

func TestFleetScreenShowsAFailedActionInsteadOfPretending(t *testing.T) {
	rec := &recorder{fail: errors.New("the authority refused: certificate already revoked")}
	_, page := newFleet(t, rec)
	for i := 0; i < 6; i++ {
		page.Press("Down")
	}
	page.Press("a")
	must(t, page.Screen(), "certificate already revoked")
}

func TestFleetScreenHasAHealthTabAndComesBack(t *testing.T) {
	now := time.Now()
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: fleet(now), At: now}, nil
	})
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return fleetSnap(now), nil }, h, (&recorder{}).actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("6")
	must(t, page.Screen(), "UNIT", "TEMP", "CARRIER", "quiet-pi")
	page.Press("1")
	must(t, page.Screen(), "ON THE RACK", "ON THE BENCH")
	page.Press("q")
	page.ExpectExit()
}

func TestFleetScreenShowsASourceError(t *testing.T) {
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) {
		return fleetview.FleetSnapshot{}, errors.New("edge.json is unreadable")
	}, nil, fleetview.FleetActions{})
	page := uitest.Run(t, 100, 30, f.Draw)
	must(t, page.Screen(), "could not read the Edge", "edge.json is unreadable")
	if strings.Contains(page.Screen(), "ON THE BENCH") {
		t.Fatal("no fleet is drawn over an error")
	}
}

// GRANTS and SESSIONS are tabs of the same screen: the standing permissions and the traffic the
// Edge really carried, read from the same snapshot; a grant can be revoked in place.
func TestFleetScreenGrantsAndSessionsTabs(t *testing.T) {
	now := time.Now()
	var revoked []string
	snap := fleetSnap(now)
	snap.Grants = []fleetview.GrantLine{
		{ID: "grant_ab12cd", Caller: "workstation", Caps: "operate", Callee: "RogGentooEdged", Until: "2026-10-03", State: "live"},
		{ID: "grant_zz99xx", Caller: "laptop", Caps: "operate", Callee: "workstation", Until: "2026-09-20", State: "REVOKED"},
	}
	snap.Sessions = []fleetview.SessionLine{
		{Who: "workstation/agent", Band: "qwen3.8-27b", Route: "local", Station: "nas", Outcome: "served", Count: 3, Age: "2m"},
		{Who: "laptop/agent", Band: "qwen3.8-27b", Route: "market", Outcome: "REFUSED (no grant)", Count: 1, Age: "9m"},
	}
	acts := (&recorder{}).actions()
	acts.RevokeGrant = func(id string) error { revoked = append(revoked, id); return nil }
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, acts)
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("4")
	must(t, page.Screen(), "GRANTS", "grant_ab12cd", "workstation", "may operate", "RogGentooEdged", "REVOKED")
	page.Press("x")
	must(t, page.Screen(), "revoke grant_ab12cd?")
	page.Press("y")
	if len(revoked) != 1 || revoked[0] != "grant_ab12cd" {
		t.Fatalf("revoked = %v", revoked)
	}
	page.Press("5")
	must(t, page.Screen(), "SESSIONS", "workstation/agent", "qwen3.8-27b", "served", "REFUSED (no grant)", "×3")
	page.Press("1")
	must(t, page.Screen(), "ON THE RACK")
}

// The hub toggles this roger's agent role; a dark persistent agent resumes or is removed - the
// TUI's [3] self-management, here through handed-in functions.
func TestFleetScreenAgentActions(t *testing.T) {
	now := time.Now()
	var setAgent []bool
	var resumed, removed []string
	snap := fleetSnap(now)
	snap.Self.AgentActive = false
	acts := (&recorder{}).actions()
	acts.SetAgent = func(on bool) error { setAgent = append(setAgent, on); return nil }
	acts.ResumeAgent = func(name string) error { resumed = append(resumed, name); return nil }
	acts.RemoveAgent = func(name string) error { removed = append(removed, name); return nil }
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, acts)
	page := uitest.Run(t, 130, 44, f.Draw)
	must(t, page.Screen(), "g agent on")
	page.Press("g")
	if len(setAgent) != 1 || setAgent[0] != true {
		t.Fatalf("setAgent = %v", setAgent)
	}
	page.Press("Down")
	page.Press("Down")
	page.Press("Down") // watcher, the dark agent
	must(t, page.Screen(), "u resume", "x remove")
	page.Press("u")
	page.Press("x")
	if len(resumed) != 1 || resumed[0] != "watcher" || len(removed) != 1 || removed[0] != "watcher" {
		t.Fatalf("resumed = %v removed = %v", resumed, removed)
	}
}

// At 100 columns the screens fold rather than cut: the kind column and the strip's carrier meter
// go (the eye tells presence), names elide, and the facts that matter stay whole.
func TestScreensFoldAtAHundredColumns(t *testing.T) {
	now := time.Now()
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: fleet(now), Firing: map[string][]string{"n_desk": {"cpu_temp > 85", "disk > 90"}, "n_ibex": {"offline > 5"}}, At: now}, nil
	})
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return fleetSnap(now), nil }, h, (&recorder{}).actions())
	page := uitest.Run(t, 100, 30, f.Draw)
	s := page.Screen()
	must(t, s, "▲ 2", "DARK 1d", "found · a adopt", "go local first")
	for _, cut := range []string{"onlin ", "offlin ", "▲ ove", "qwen3.8-│"} {
		if strings.Contains(s, cut) {
			t.Fatalf("a fact was cut instead of folded (%q):\n%s", cut, s)
		}
	}
	page.Press("6")
	s = page.Screen()
	must(t, s, "UNIT", "TEMP", "DARK 1d", "▲ 2", "▲ 1", "▲ over 90")
	if strings.Contains(s, "CARRIER") {
		t.Fatalf("at 100 columns the strip drops the carrier meter (the eye tells presence):\n%s", s)
	}
	for _, cut := range []string{"onlin ", "offli "} {
		if strings.Contains(s, cut) {
			t.Fatalf("a fact was cut instead of folded (%q):\n%s", cut, s)
		}
	}
}

// A dark member that is back on the network asking to re-join is ONE unit, not a member and a
// candidate: its row says so, a re-admits it, and the readout explains what happens next. A unit
// that was adopted but has not claimed yet says it is waiting, and where.
func TestFleetScreenTellsTheReJoinStoryOnOneUnit(t *testing.T) {
	now := time.Now()
	rec := &recorder{}
	snap := fleetSnap(now)
	for i := range snap.Items {
		if snap.Items[i].Name == "gentle-ibex-14" {
			snap.Items[i].Asking = true
			snap.Items[i].ClaimStanding = true
			snap.Items[i].AuthPort = "39009"
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, rec.actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	f.SelectUnit("n_ibex") // by id: the rack is worst first, so position is not identity
	page.Press("r")
	s := page.Screen()
	must(t, s, "asking to re-join", "a re-admit", "waiting for it to claim", "39009")
	page.Press("a")
	if len(rec.adopted) != 1 || rec.adopted[0] != "n_ibex" {
		t.Fatalf("a on an asking member re-admits it through Adopt: %v", rec.adopted)
	}
	// The outcome stays in the readout, not only in a passing toast.
	must(t, page.Screen(), "adopted gentle-ibex-14")
}

// THE LOG on every unit: the last hour as a strip, and the state in words with how long.
func TestFleetScreenShowsTheLogAndTimeInState(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		switch snap.Items[i].Name {
		case "nas":
			log := make([]edge.UnitState, 60)
			for j := range log {
				log[j] = edge.StateAir
			}
			for j := 50; j < 57; j++ {
				log[j] = edge.StateTrouble
			}
			snap.Items[i].Log, snap.Items[i].Since = log, 3*time.Minute
		case "gentle-ibex-14":
			log := make([]edge.UnitState, 60)
			for j := range log {
				log[j] = edge.StateDark
			}
			snap.Items[i].Log, snap.Items[i].Since = log, 26*time.Hour
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	s := page.Screen()
	must(t, s, "ON AIR 3m", "DARK 1d")
	f.SelectUnit("n_nas") // by id: the rack is worst first, so position is not identity
	page.Press("r")
	s = page.Screen()
	must(t, s, "THE LOG", "▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁", "on air 3m")
}

// TROUBLE tab: the standing calls sorted priority then oldest first, each with why it matters and
// what to do; k says ROGER - the row loses its color, not its place.
func TestFleetScreenTroubleTabAndRoger(t *testing.T) {
	now := time.Now()
	var rogered []string
	snap := fleetSnap(now)
	snap.Alarms = []fleetview.AlarmLine{
		{RuleID: "offline", NodeID: "n_ibex", Node: "gentle-ibex-14", Metric: "offline", When: "offline > 5", Value: "26h", Since: 26 * time.Hour},
		{RuleID: "cpu_temp", NodeID: "n_desk", Node: "RogGentooEdged", Metric: "cpu_temp", When: "cpu_temp > 85", Value: "95.5°C", Since: 14 * time.Minute},
		{RuleID: "disk", NodeID: "n_desk", Node: "RogGentooEdged", Metric: "disk", When: "disk > 90", Value: "92%", Since: 3 * time.Hour, Acked: true, AckBy: "luis"},
	}
	acts := (&recorder{}).actions()
	acts.Roger = func(ruleID, nodeID string) error { rogered = append(rogered, ruleID+"@"+nodeID); return nil }
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, acts)
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("3")
	s := page.Screen()
	must(t, s, "TROUBLE", "3 standing", "HI", "MED", "cpu_temp > 85", "95.5°C", "WHY IT MATTERS", "free space", "ROGER luis")
	// Priority first (HI before MED), then the oldest first among equals: disk (3h) before cpu_temp (14m).
	if strings.Index(s, "disk > 90") > strings.Index(s, "cpu_temp > 85") || strings.Index(s, "cpu_temp > 85") > strings.Index(s, "offline > 5") {
		t.Fatalf("calls must sort priority then age:\n%s", s)
	}
	page.Press("Down") // the second row: cpu_temp, not yet acknowledged
	page.Press("k")
	if len(rogered) != 1 || rogered[0] != "cpu_temp@n_desk" {
		t.Fatalf("k should say roger to the selected call: %v", rogered)
	}
}

// ANDON: one tile per unit - one word, one color, one number - so the whole rack is read in two
// seconds; the counter line at the foot says how many are on air, in trouble, dark.
func TestFleetScreenAndonTiles(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		log := make([]edge.UnitState, 60)
		for j := range log {
			log[j] = edge.StateAir
		}
		switch snap.Items[i].Name {
		case "gentle-ibex-14":
			for j := range log {
				log[j] = edge.StateDark
			}
			snap.Items[i].Log, snap.Items[i].Since = log, 26*time.Hour
		case "nas":
			for j := 54; j < 60; j++ {
				log[j] = edge.StateTrouble
			}
			snap.Items[i].Log, snap.Items[i].Since = log, 6*time.Minute
			snap.Items[i].Alerts = []string{"disk > 90"}
		case "RogGentooEdged":
			snap.Items[i].Log, snap.Items[i].Since = log, 15*24*time.Hour
			snap.Items[i].Alerts = nil
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("2")
	s := page.Screen()
	must(t, s, "ANDON", "ON AIR", "TROUBLE", "DARK", "AIRTIME", "100%", "0%", "1 on air", "1 trouble", "1 dark")
}

// b on a roger binds a model and a job in place (the TUI's Bind Model), c clears it; the TROUBLE
// header shows the rate and calls out a pile-up; a shelved call reads STAND BY and a stale one
// STALE.
func TestFleetScreenBindsJobsAndTellsStandByStaleAndPileUp(t *testing.T) {
	now := time.Now()
	var bound []string
	var cleared []string
	var standBy []string
	snap := fleetSnap(now)
	snap.Alarms = []fleetview.AlarmLine{
		{RuleID: "cpu_temp", NodeID: "n_desk", Node: "RogGentooEdged", Metric: "cpu_temp", When: "cpu_temp > 85", Value: "95.5°C", Since: 30 * time.Hour},
		{RuleID: "disk", NodeID: "n_desk", Node: "RogGentooEdged", Metric: "disk", When: "disk > 90", Value: "92%", Since: 3 * time.Hour, Shelved: true, StandByUntil: now.Add(time.Hour), StandByBy: "gentle-ibex-14"},
	}
	snap.Rate = 12
	acts := (&recorder{}).actions()
	acts.BindJob = func(name, model, job string) error { bound = append(bound, name+"="+model+"/"+job); return nil }
	acts.ClearJob = func(name string) error { cleared = append(cleared, name); return nil }
	acts.StandBy = func(ruleID, nodeID string, d time.Duration) error {
		standBy = append(standBy, ruleID+"@"+nodeID+" "+d.String())
		return nil
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, acts)
	page := uitest.Run(t, 130, 44, f.Draw)

	page.Press("Down")
	page.Press("Down") // roggentooedged-2, the agent
	must(t, page.Screen(), "b bind", "c clear")
	page.Press("b")
	must(t, page.Screen(), "bind", "model", "job")
	page.Type("qwen3-8b serve")
	page.Press("Enter")
	if len(bound) != 1 || bound[0] != "roggentooedged-2=qwen3-8b/serve" {
		t.Fatalf("bound = %v", bound)
	}
	page.Press("c")
	if len(cleared) != 1 || cleared[0] != "roggentooedged-2" {
		t.Fatalf("cleared = %v", cleared)
	}

	page.Press("3")
	s := page.Screen()
	must(t, s, "PILE-UP", "12 in 10 min", "STALE", "STAND BY", "· gentle-ibex-14", "s stand by")
	page.Press("s") // the first call (cpu_temp, HI, oldest) goes on stand by for an hour
	if len(standBy) != 1 || standBy[0] != "cpu_temp@n_desk 1h0m0s" {
		t.Fatalf("standBy = %v", standBy)
	}
}

// The SIGN-OFF line at the foot of ANDON and TROUBLE: the shift in one line.
func TestFleetScreenSignOffLine(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.SignOff = fleetview.SignOffLine{Raised: 4, Cleared: 2, Loudest: "RogGentooEdged", LoudestCount: 3,
		DeadAir: []fleetview.DeadAir{{Node: "gentle-ibex-14", For: 8 * time.Hour}}}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("2")
	must(t, page.Screen(), "SIGN-OFF", "4 raised", "2 cleared", "loudest RogGentooEdged", "dead air", "gentle-ibex-14 8h")
	page.Press("3")
	must(t, page.Screen(), "SIGN-OFF", "4 raised")
}

// Every unit carries who runs it and what it is doing: the OP and JOB line on the andon tile and
// in the readout, with the sessions it served.
func TestFleetScreenShowsOpAndJobOnUnits(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.Self.Op = "luis"
	for i := range snap.Items {
		switch snap.Items[i].Name {
		case "RogGentooEdged":
			snap.Items[i].Jobs = []string{"roggentooedged-2 serve qwen3.8-27b"}
			snap.Items[i].Served = 3
			snap.Items[i].Alerts = nil // a firing call would take the tile's line instead, rightly
		case "nas":
			snap.Items[i].Served = 12
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	must(t, page.Screen(), "OP luis", "roggentooedged-2 serve qwen3.8-27b", "3 sessions served")
	page.Press("2")
	must(t, page.Screen(), "OP luis", "JOB roggentooedged-2 serve", "12 sessions")
}

// A grant is issued from the GRANTS tab in place: i opens the prompt, <caller|self> <capability>
// <callee> [for], enter issues through the handed-in function; the outcome stays in view.
func TestFleetScreenIssuesAGrantInPlace(t *testing.T) {
	now := time.Now()
	var issued []string
	acts := (&recorder{}).actions()
	acts.IssueGrant = func(caller, capability, callee, forDur string) (string, error) {
		issued = append(issued, caller+" "+capability+" "+callee+" "+forDur)
		return "issued grant_new1: self may operate nas until 2026-10-03", nil
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return fleetSnap(now), nil }, nil, acts)
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("4")
	must(t, page.Screen(), "i issue")
	page.Press("i")
	must(t, page.Screen(), "issue", "caller")
	page.Type("self operate nas 7d")
	page.Press("Enter")
	if len(issued) != 1 || issued[0] != "self operate nas 7d" {
		t.Fatalf("issued = %v", issued)
	}
	must(t, page.Screen(), "issued grant_new1")
	// A default duration when none is typed.
	page.Press("i")
	page.Type("self sense nas")
	page.Press("Enter")
	if len(issued) != 2 || issued[1] != "self sense nas 30d" {
		t.Fatalf("issued = %v (30d is the default, as the CLI's)", issued)
	}
}

// A phone that reads the trouble feed is heard: its row and readout say so, with when.
func TestFleetScreenSaysWhenAPhoneLastReadTheFeed(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		if snap.Items[i].Name == "gentle-ibex-14" {
			snap.Items[i].Dark, snap.Items[i].Presence, snap.Items[i].Alerts = false, "online", nil
			snap.Items[i].LastSeen = now
			snap.Items[i].LastRead = now.Add(-90 * time.Second)
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	f.SelectUnit("n_ibex") // by id: the rack is worst first, so position is not identity
	page.Press("r")
	must(t, page.Screen(), "hears the Edge", "read the feed 1m ago")
}

// Cursors never leave their lists, whatever the keys: Up above the first call, then k, is nothing
// - not a panic; a prompt on HEALTH owns q and the tab numbers while it is open; ANDON takes no
// action keys.
func TestFleetScreenCursorsAndPromptsAreSafe(t *testing.T) {
	now := time.Now()
	rec := &recorder{}
	snap := fleetSnap(now)
	snap.Alarms = []fleetview.AlarmLine{{RuleID: "cpu_temp", NodeID: "n_desk", Node: "RogGentooEdged", Metric: "cpu_temp", When: "cpu_temp > 85", Value: "95.5°C", Since: 14 * time.Minute}}
	var rogered []string
	acts := rec.actions()
	acts.Roger = func(ruleID, nodeID string) error { rogered = append(rogered, ruleID); return nil }
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: fleet(now), At: now}, nil
	})
	var set []string
	h.Actions = fleetview.HealthActions{SetRule: func(metric, op, value, scope string) (string, error) { set = append(set, metric); return "ok", nil }}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, h, acts)
	page := uitest.Run(t, 130, 44, f.Draw)
	page.Press("3")
	page.Press("Up")
	page.Press("Up")
	page.Press("k")
	if len(rogered) != 1 {
		t.Fatalf("k on the first call after over-scrolling up still acknowledges it once: %v", rogered)
	}
	page.Press("4")
	page.Press("Up")
	page.Press("x") // no grants: nothing, no panic
	page.Press("2")
	page.Press("Down")
	page.Press("f") // ANDON: no forget question
	if strings.Contains(page.Screen(), "forget ") {
		t.Fatal("ANDON must not arm the forget question")
	}
	page.Press("6")
	page.Press("a")
	page.Type("memory gt 90 q1") // q and 1 are letters of the rule here, not quit and a tab
	page.Press("Enter")
	if len(set) != 1 || set[0] != "memory" {
		t.Fatalf("the prompt owned its keys: %v", set)
	}
	if page.Exited() {
		t.Fatal("typing q inside the prompt must not quit")
	}
}

// A terminal narrower than the brand band's heavy rule (an SSH window shrunk to a sliver) draws
// what fits; it never panics the frame (audit 2026-09-27: strings.Repeat with a negative count).
func TestFleetScreenSurvivesASliverOfATerminal(t *testing.T) {
	f, _ := newFleet(t, &recorder{})
	for _, w := range []uint16{8, 20, 24, 25} {
		uitest.Run(t, w, 6, f.Draw)
	}
	now := time.Now()
	h := fleetview.NewHealth(func() (fleetview.HealthSnapshot, error) {
		return fleetview.HealthSnapshot{Nodes: fleet(now), At: now}, nil
	})
	for _, w := range []uint16{8, 20, 24, 25} {
		uitest.Run(t, w, 6, h.Draw)
	}
}

// The inventory line and the BEHIND mark on the readout (features/edge/inventory.feature).
func TestReadoutCarriesTheSoftwareLineAndWhoIsBehind(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		switch snap.Items[i].Name {
		case "nas":
			snap.Items[i].Software = []string{"roger 6.9.0 · Debian GNU/Linux 12 · 6.1.0 · arm64 · Raspberry Pi 5"}
			snap.Items[i].Behind = "6.11.2"
		case "roggentooedged-2":
			snap.Items[i].Software = []string{"roger 6.11.2 · Gentoo Linux · 6.18.31 · amd64"}
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 160, 44, f.Draw)
	f.SelectUnit("n_nas") // by id: the rack is worst first, so position is not identity
	page.Press("r")
	must(t, page.Screen(), "SOFTWARE", "roger 6.9.0 · Debian GNU/Linux 12 · 6.1.0", "arm64 · Raspberry Pi 5", "UPDATE", "behind 6.11.2")
	for range 3 { // up past the dark phone, which the worst-first rack puts above nas
		page.Press("k")
	}
	must(t, page.Screen(), "roger 6.11.2 · Gentoo Linux")
	if strings.Contains(page.Screen(), "behind") {
		t.Fatal("a current roger is not marked behind")
	}
}

// The owner's words on the readout: drawn where they exist, and no empty row where they do not
// (features/edge/tags.feature).
func TestReadoutCarriesTheOwnersTags(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		if snap.Items[i].Name == "nas" {
			snap.Items[i].Tags = []string{"production", "gpu"}
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 150, 44, f.Draw)
	f.SelectUnit("n_nas") // by id: the rack is worst first, so position is not identity
	page.Press("r")
	must(t, page.Screen(), "TAGS", "production · gpu")
	f.SelectUnit("n_ibex") // the phone carries none
	page.Press("r")
	if strings.Contains(page.Screen(), "TAGS") {
		t.Fatal("a unit with no tags draws no tags row")
	}
}

// A unit whose owner turned maintenance on says so; one that did not says nothing
// (features/edge/maintenance.feature).
func TestReadoutSaysWhenAUnitCanBeMaintained(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		if snap.Items[i].Name == "nas" {
			snap.Items[i].Maintain = true
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 160, 44, f.Draw)
	f.SelectUnit("n_nas") // by id: the rack is worst first, so position is not identity
	page.Press("r")
	must(t, page.Screen(), "SERVICE", "maintenance is on there")
	f.SelectUnit("n_ibex")
	page.Press("r")
	if strings.Contains(page.Screen(), "maintenance is on") {
		t.Fatal("a unit that does not declare maintain says nothing about it")
	}
}

// The EVENTS tab: the timeline, newest first, a crossing marked and a node event quiet; and an
// empty timeline says so rather than drawing nothing (features/edge/events.feature).
func TestEventsTabDrawsTheTimeline(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.Events = []fleetview.EventLine{
		{Age: "2m ago", Node: "desk", What: "raised cpu_temp", Detail: "95.5°C, over 85°C", Alarm: true},
		{Age: "1h ago", Node: "shed", What: "tagged", Detail: "production"},
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 150, 44, f.Draw)
	page.Press("6")
	must(t, page.Screen(), "EVENTS", "what happened on this Edge", "raised cpu_temp", "95.5°C, over 85°C", "tagged", "production", "re-read")
	page.Press("1")
	must(t, page.Screen(), "ON THE RACK")

	empty := fleetSnap(now)
	e := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return empty, nil }, nil, (&recorder{}).actions())
	p2 := uitest.Run(t, 150, 44, e.Draw)
	p2.Press("6")
	must(t, p2.Screen(), "nothing has happened on this Edge yet")
}

// A collapsed run reads as one line with its count on the EVENTS tab.
func TestEventsTabShowsACollapsedRun(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.Events = []fleetview.EventLine{
		{Age: "30s ago", Node: "desk", What: "probe", Detail: "serve -> VERIFIED   ×12 over 11m"},
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 150, 44, f.Draw)
	page.Press("6")
	must(t, page.Screen(), "×12 over 11m")
}

// A unit that does not match what the owner expects carries a DRIFT row, one line per thing; a unit
// in order carries none at all (features/edge/drift.feature).
func TestReadoutSaysWhereAUnitHasDrifted(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		if snap.Items[i].Name == "nas" {
			snap.Items[i].Drift = []string{"roger 6.13.0 · runs 6.9.0", "agent · runs no agent"}
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 160, 44, f.Draw)
	f.SelectUnit("n_nas") // by id: the rack is worst first, so position is not identity
	page.Press("r")
	must(t, page.Screen(), "DRIFT", "roger 6.13.0 · runs 6.9.0", "agent · runs no agent")
	f.SelectUnit("n_ibex")
	page.Press("r")
	if strings.Contains(page.Screen(), "DRIFT") {
		t.Fatal("a unit in order carries no drift row")
	}
}

// A record whose name IS its own id was never named by its owner. The screen PRINTS a short label
// for it and still ACTS on the record name, because that is what an adopt, a rule scope and an agent
// action key off. Drawing 48 characters as somebody's name is what made the [3] map unreadable.
func TestTheScreenPrintsALabelAndActsOnTheName(t *testing.T) {
	id := "n_" + strings.Repeat("ab", 24)
	now := time.Now()
	rec := &recorder{}
	snap := func() (fleetview.FleetSnapshot, error) {
		return fleetview.FleetSnapshot{At: now,
			Self: fleetview.SelfFacts{Name: "desk", Root: "LOCAL ROOT"},
			Items: []fleetview.FleetItem{
				{Kind: fleetview.ItemHub, ID: "n_self", Name: "desk", NodeKind: "host"},
				{Kind: fleetview.ItemCandidate, ID: id, Name: id, Label: "n_abababab", NodeKind: "mobile", Presence: "discovered"},
			}}, nil
	}
	f := fleetview.NewFleet(snap, nil, rec.actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	screen := page.Screen()
	if !strings.Contains(screen, "n_abababab") {
		t.Errorf("the short label is not drawn:\n%s", screen)
	}
	if strings.Contains(screen, id) {
		t.Errorf("the whole 48-character id is drawn as a name:\n%s", screen)
	}
	// Adopting it acts on the RECORD, not on the label.
	page.Press("Down")
	page.Press("a")
	if len(rec.adopted) != 1 || rec.adopted[0] != id {
		t.Fatalf("adopt acted on %v, want the record id", rec.adopted)
	}
}

// features/edge/fleet_window.feature §2: a member heard with no face to dial reads HEARD, not ON AIR.
func TestFleetScreenSaysHeardForAUnitItCannotControl(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.Items = append(snap.Items, fleetview.FleetItem{Kind: fleetview.ItemMember, ID: "n_nina", Name: "nina", NodeKind: "mobile",
		Presence: "online", LastSeen: now.Add(-9 * time.Second)})
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 130, 44, f.Draw)
	must(t, page.Screen(), "nina", "HEARD")
}

// features/edge/fleet_window.feature §1, §2, §5, §7: the summary first, the instruments in the row,
// / and s from the keyboard, and the unit's page naming its control path, its model and its power.
func TestTheFleetWindowRowsSummaryFilterAndSort(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	hot, word := 88.0, uint32(0x5)
	for i := range snap.Items {
		if snap.Items[i].Name == "nas" {
			snap.Items[i].Health = &edge.NodeHealth{Readings: &store.EdgeResources{CPUTempC: &hot, RAMUsedGB: 8, RAMTotalGB: 16, PiPower: &word}}
			snap.Items[i].Alerts = []string{"cpu_temp > 85"}
			snap.Items[i].Serving = "qwen3-4b"
			snap.Items[i].Model = `serving "qwen3-4b" (unsloth/Qwen3-4B-GGUF:Q4_K_M, 8192 context)`
		}
	}
	snap.Items = append(snap.Items, fleetview.FleetItem{Kind: fleetview.ItemMember, ID: "n_nina", Name: "nina", NodeKind: "mobile",
		Presence: "online", LastSeen: now.Add(-9 * time.Second)})
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 160, 44, f.Draw)
	s := page.Screen()
	must(t, s, "3 on your Edge", "0 on air", "1 trouble", "1 dark", "1 heard only", "1 new")
	must(t, s, "▲88°", "50%", "⚡", "qwen3-4b", "┈┈┈")
	if strings.Contains(s, "gpt-oss-20b") && !strings.Contains(s, "old)") {
		t.Fatalf("a dark unit's model is drawn as current:\n%s", s)
	}

	// / narrows by what an owner would type, and says what it hides.
	page.Press("/")
	page.Type("kind:phone")
	page.Press("Enter")
	s = page.Screen()
	must(t, s, "nina", "gentle-ibex-14", "/kind:phone hides 1") // only nas: the bench's blue-fox-3 is a phone too
	if strings.Contains(s, " nas ") {
		t.Fatalf("the filter did not hide nas:\n%s", s)
	}
	page.Press("/")
	page.Press("Esc")
	must(t, page.Screen(), " nas ")

	// s cycles the owner's order and says which.
	page.Press("s")
	must(t, page.Screen(), "the rack is by name")

	// The unit's page: its control path, its model, its power.
	f.SelectUnit("n_nina")
	page.Press("r")
	must(t, page.Screen(), "CONTROL", "no face this machine can dial")
	f.SelectUnit("n_nas")
	page.Press("r")
	must(t, page.Screen(), "MODEL", "unsloth/Qwen3-4B-GGUF:Q4_K_M", "POWER", "under-voltage, throttled NOW")
}

func TestADarkUnitsModelIsDrawnAsOldNeverAsCurrent(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	for i := range snap.Items {
		if snap.Items[i].Name == "gentle-ibex-14" {
			snap.Items[i].Serving = "pico-293m"
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 160, 44, f.Draw)
	must(t, page.Screen(), "pico-293m (1d old)")
}

func TestHearNowAsksAControllableUnitAndSaysWhatItAnswered(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.Items = append(snap.Items, fleetview.FleetItem{Kind: fleetview.ItemMember, ID: "n_nina", Name: "nina", NodeKind: "mobile",
		Presence: "online", LastSeen: now.Add(-9 * time.Second)})
	var asked []string
	acts := (&recorder{}).actions()
	acts.AskNow = func(id string) (string, error) { asked = append(asked, id); return "nas answered in 12 ms", nil }
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, acts)
	page := uitest.Run(t, 160, 44, f.Draw)
	f.SelectUnit("n_nas")
	page.Press("r")
	must(t, page.Screen(), "h hear now")
	page.Press("h")
	must(t, page.Screen(), "nas answered in 12 ms")
	if len(asked) != 1 || asked[0] != "n_nas" {
		t.Fatalf("asked = %v", asked)
	}
	// A unit with no face to dial is not offered it, and h does nothing to it.
	f.SelectUnit("n_nina")
	page.Press("r")
	if strings.Contains(page.Screen(), "h hear now") {
		t.Fatal("hear now offered on a unit with no control path")
	}
	page.Press("h")
	if len(asked) != 1 {
		t.Fatalf("a heard-only unit was dialed: %v", asked)
	}
}

// features/edge/fleet_window.feature §4: adoption shows its stages with their ages, and a stall says
// which stage it waits on and what that unit must do.
func TestAdoptionShowsItsStagesAndAStallSaysWhy(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	snap.Items = append(snap.Items, fleetview.FleetItem{Kind: fleetview.ItemMember, ID: "n_pi", Name: "raspberrypi", NodeKind: "host",
		Presence: "online", ClaimStanding: true, ClaimAt: now.Add(-12 * time.Second), AuthPort: "39009"})
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 160, 44, f.Draw)
	f.SelectUnit("n_pi")
	page.Press("r")
	must(t, page.Screen(), "ADOPTING 12s", "✓ claim issued 12s ago", "certificate: waiting for it to fetch one", "first presence: not yet")

	snap.Items[len(snap.Items)-1].ClaimAt = now.Add(-5 * time.Minute)
	page.Press("r")
	must(t, page.Screen(), "certificate not fetched after 5m", "39009", "roger edge enroll there")
}

// features/edge/fleet_window.feature §6: the page draws the last hour as a line, with a gap where
// the unit was dark - never interpolated across it.
func TestTheUnitsPageDrawsItsLastHourWithGaps(t *testing.T) {
	now := time.Now()
	snap := fleetSnap(now)
	hour := make([]float64, 60)
	for i := range hour {
		hour[i] = math.NaN()
	}
	for i := 30; i < 60; i++ {
		hour[i] = 50 + float64(i-30)
	}
	for i := 40; i < 45; i++ {
		hour[i] = math.NaN() // dark for five minutes
	}
	for i := range snap.Items {
		if snap.Items[i].Name == "nas" {
			snap.Items[i].TempHour = hour
		}
	}
	f := fleetview.NewFleet(func() (fleetview.FleetSnapshot, error) { return snap, nil }, nil, (&recorder{}).actions())
	page := uitest.Run(t, 160, 44, f.Draw)
	f.SelectUnit("n_nas")
	page.Press("r")
	s := page.Screen()
	must(t, s, "TEMP 1H", "50° to 79°")
	i := strings.Index(s, "TEMP 1H")
	row := s[i : i+strings.Index(s[i:], "\n")]
	if !strings.Contains(row, "▁") || !strings.Contains(row, "█") {
		t.Fatalf("the hour is not drawn as a line: %q", row)
	}
	if !strings.Contains(strings.TrimRight(row[strings.Index(row, "▁"):], " "), "  ") {
		t.Fatalf("the dark minutes are not a gap: %q", row)
	}
}
