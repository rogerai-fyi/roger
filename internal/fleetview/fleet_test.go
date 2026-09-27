package fleetview_test

// The Edge FLEET screen in Limoni: the map of this machine, its rogers, the members and the
// discovered devices - and the actions that manage them (adopt, rename, forget, prefer), driven
// by key in-process and asserted on the frame. The actions are functions handed in, so the test
// sees exactly what the owner's key would do to the Edge.

import (
	"errors"
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
				Caps: []fleetview.Cap{{"serve", "VERIFIED"}}, Bands: []string{"gpt-oss-20b"}, LastSeen: now.Add(-20 * time.Second)},
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
	must(t, s, "ROGER", "EDGE", "ON AIR", "RogGentooEdged", "prefers local")
	must(t, s, "roggentooedged", "this one", "roggentooedged-2", "qwen3.8-27b", "watcher", "offline")
	must(t, s, "ON THE RACK", "nas", "gentle-ibex-14", "offline 1d", "ON THE BENCH", "blue-fox-3", "adopt")
	must(t, s, "▲ 1", "▲ 2", "scanning this network")
	// The hub is selected first: its readout names its role, its alert on the temperature meter, its health.
	must(t, s, "this machine", "▲ cpu_temp > 85", "95.5")
	// A long fact wraps in the readout; it is never cut at the pane edge.
	must(t, s, "look now")
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

	// Up to nas: rename it in place.
	page.Press("Up")
	page.Press("n")
	must(t, page.Screen(), "new name")
	page.Type("nas-2")
	page.Press("Enter")
	if len(rec.renamed) != 1 || rec.renamed[0] != "n_nas=nas-2" {
		t.Fatalf("renamed = %v", rec.renamed)
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
	page.Press("4")
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
	page.Press("2")
	must(t, page.Screen(), "GRANTS", "grant_ab12cd", "workstation", "may operate", "RogGentooEdged", "REVOKED")
	page.Press("x")
	must(t, page.Screen(), "revoke grant_ab12cd?")
	page.Press("y")
	if len(revoked) != 1 || revoked[0] != "grant_ab12cd" {
		t.Fatalf("revoked = %v", revoked)
	}
	page.Press("3")
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
