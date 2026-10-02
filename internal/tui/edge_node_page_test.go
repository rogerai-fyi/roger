package tui

// THE NODE PAGE is where an owner asks "what is this thing, and why is it like this". Everything
// the fleet manager knows about ONE unit belongs here: the owner's own words for it (tags), what
// it is running and whether that is behind the rest of the Edge (inventory), and what has happened
// to it lately (events). Before this, all three existed only in the CLI and the console, and the
// screen the owner actually sits in front of could not answer any of them.

import (
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

func nodePageModel(t *testing.T) (model, string) {
	t.Helper()
	m := mapReadableModel(t)
	now := time.Unix(100000, 0)
	for i := range m.edge.rows {
		n := &m.edge.rows[i].n
		if n.Name != "studio-mac" {
			continue
		}
		n.Tags = []string{"production", "upstairs"}
		n.Instances = []store.EdgeInstance{
			{Name: "desk", Software: &store.EdgeSoftware{Roger: "6.12.0", OS: "darwin", Arch: "arm64"}},
			{Name: "share", Software: &store.EdgeSoftware{Roger: "6.13.0"}},
		}
		n.History = []store.EdgeEvent{
			{At: now.Add(-3 * time.Hour).Unix(), What: "adopted", Detail: "by owner"},
			{At: now.Add(-2 * time.Hour).Unix(), What: "renamed", Detail: "mac -> studio-mac"},
			{At: now.Add(-30 * time.Minute).Unix(), What: "serving", Detail: "qwen3-8b on air"},
		}
	}
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "studio-mac" {
			m.edge.netSel, m.edge.netSelSet, m.edge.netDetail = i, true, true
		}
	}
	return m, stripANSI(m.edgeView(116))
}

func TestTheNodePageAnswersWhatItIsAndWhyItIsLikeThat(t *testing.T) {
	_, out := nodePageModel(t)
	for _, want := range []string{
		"TAGS", "production", "upstairs", // the owner's own words for it
		"RUNNING", "/desk", "roger 6.12.0", "behind 6.13.0", // what it runs, and whether it lags
		"LATELY", "serving", "qwen3-8b on air", "renamed", // what has happened to it
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the node page cannot answer for %q:\n%s", want, out)
		}
	}
	// Newest first: an owner reads the last thing that happened, not the first.
	serving, renamed := strings.Index(out, "qwen3-8b on air"), strings.Index(out, "mac -> studio-mac")
	if serving < 0 || renamed < 0 || serving > renamed {
		t.Errorf("the node's history is not newest first:\n%s", out)
	}
	// Only the newest few: the rest are `roger edge events`, and a page that scrolls is not a page.
	if strings.Count(out, "ago") > 6 {
		t.Errorf("the node page is drawing more history than it has room for:\n%s", out)
	}
}

// A node that reports none of this says nothing about it rather than inventing a blank row. An
// empty label is a lie in the same shape as a wrong one.
// Drift is the difference between watching a fleet and running one: the node page says where this
// unit is not what the owner said it should be (features/edge/drift.feature).
func TestTheNodePageSaysWhereAUnitHasDrifted(t *testing.T) {
	m, _ := nodePageModel(t)
	var id string
	for _, it := range m.edgeNetItems() {
		if it.member && it.name == "studio-mac" {
			id = it.id
		}
	}
	if id == "" {
		t.Fatal("no studio-mac in the rig")
	}
	m.hooks.EdgeDrift = func() map[string][]string {
		return map[string][]string{id: {"roger 6.13.0 · found 6.12.0"}}
	}
	out := stripANSI(m.edgeView(116))
	if !strings.Contains(out, "DRIFT") || !strings.Contains(out, "found 6.12.0") {
		t.Errorf("the node page does not say where the unit drifted:\n%s", out)
	}
	// A unit with no expectation on it gets no line: silence is not "in step".
	m.hooks.EdgeDrift = func() map[string][]string { return map[string][]string{"n_other": {"x"}} }
	if strings.Contains(stripANSI(m.edgeView(116)), "DRIFT") {
		t.Errorf("a unit with no expectation was given a drift line")
	}
	// A build that wired nothing draws nothing rather than guessing.
	m.hooks.EdgeDrift = nil
	if strings.Contains(stripANSI(m.edgeView(116)), "DRIFT") {
		t.Errorf("a build with no expectations wired still drew drift")
	}
}

func TestTheNodePageInventsNothing(t *testing.T) {
	m := mapReadableModel(t)
	// Enrolment itself is an event, so a node fresh off the fleet legitimately has history. This
	// test is about a record that reports NOTHING, which is what a node seen only over discovery
	// looks like.
	for i := range m.edge.rows {
		if m.edge.rows[i].n.Name == "pi-greenhouse" {
			m.edge.rows[i].n.History = nil
			m.edge.rows[i].n.Tags = nil
			m.edge.rows[i].n.Instances = nil
		}
	}
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "pi-greenhouse" {
			m.edge.netSel, m.edge.netSelSet, m.edge.netDetail = i, true, true
		}
	}
	out := stripANSI(m.edgeView(116))
	for _, never := range []string{"TAGS", "RUNNING", "LATELY"} {
		if strings.Contains(out, never) {
			t.Errorf("the page drew %q for a node that reports none:\n%s", never, out)
		}
	}
}

// "Behind" is measured against what this Edge HAS on it, never against a release feed: the screen
// makes no claim about a version it has not seen on a unit of its own.
func TestBehindIsMeasuredAgainstThisEdgeOnly(t *testing.T) {
	rows := []edgeRow{
		{n: store.EdgeNode{Instances: []store.EdgeInstance{{Name: "a", Software: &store.EdgeSoftware{Roger: "6.11.0"}}}}},
		{n: store.EdgeNode{Instances: []store.EdgeInstance{{Name: "b", Software: &store.EdgeSoftware{Roger: "6.13.0"}}}}},
	}
	if got := edgeNewestRogerOn(rows); got != "6.13.0" {
		t.Errorf("newest on this Edge = %q, want 6.13.0", got)
	}
	if got := edgeNewestRogerOn(nil); got != "" {
		t.Errorf("an Edge with nothing reported claims a newest version: %q", got)
	}
}

func TestOnlyTheNewestFewEventsAreDrawn(t *testing.T) {
	var in []store.EdgeEvent
	for i := 0; i < 10; i++ {
		in = append(in, store.EdgeEvent{At: int64(i), What: "x"})
	}
	got := edgeLastEvents(in, 3)
	if len(got) != 3 || got[0].At != 9 || got[2].At != 7 {
		t.Errorf("last events = %+v, want the newest three newest-first", got)
	}
	if got := edgeLastEvents(nil, 3); len(got) != 0 {
		t.Errorf("a node with no history produced %d events", len(got))
	}
}

// A BOARD SAYS WHAT IT IS. The founder put a Raspberry Pi on the Edge; "MACHINE · on your Edge"
// tells them nothing they did not know, and the node already reports its hardware model. The card
// shows which board, the page shows the model exactly as reported.
func TestACardSaysWhichBoardItIs(t *testing.T) {
	m := mapReadableModel(t)
	for i := range m.edge.rows {
		if m.edge.rows[i].n.Name == "pi-greenhouse" {
			// A Raspberry Pi runs roger as a host: it reads MACHINE, and WHICH machine comes from its
			// hardware word (edge.KindWord; "board" is for microcontrollers).
			m.edge.rows[i].n.Kind = "host"
			m.edge.rows[i].n.Instances = []store.EdgeInstance{{Name: "share",
				Software: &store.EdgeSoftware{Roger: "6.13.0", OS: "Debian GNU/Linux 12",
					Arch: "arm64", Machine: "Raspberry Pi 4 Model B Rev 1.5"}}}
		}
	}
	out := stripANSI(m.edgeView(116))
	if !strings.Contains(out, "MACHINE · Pi 4") {
		t.Errorf("the card does not say which machine it is:\n%s", out)
	}
	// And the whole model, unshortened, on the node's own page.
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "pi-greenhouse" {
			m.edge.netSel, m.edge.netSelSet, m.edge.netDetail = i, true, true
		}
	}
	page := stripANSI(m.edgeView(116))
	for _, want := range []string{"HARDWARE", "Raspberry Pi 4 Model B Rev 1.5", "arm64"} {
		if !strings.Contains(page, want) {
			t.Errorf("the node page is missing %q:\n%s", want, page)
		}
	}
	// A node that has not said what it is says nothing rather than something invented.
	m2 := mapReadableModel(t)
	if strings.Contains(stripANSI(m2.edgeView(116)), "HARDWARE") {
		t.Errorf("a node that reported no model was given hardware anyway")
	}
}

// features/edge/health.feature: a throttled or undervolted Pi shows the flag ON THE NODE. The page
// takes the freshest reading any of its rogers reported, and says nothing for a machine that is not
// a Pi.
func TestANodePageSaysWhenAPiIsShortOfPower(t *testing.T) {
	m := mapReadableModel(t)
	old, fresh := uint32(0), uint32(0x50005)
	for i := range m.edge.rows {
		if m.edge.rows[i].n.Name == "pi-greenhouse" {
			m.edge.rows[i].n.Instances = []store.EdgeInstance{
				{Name: "old", Resources: &store.EdgeResources{PiPower: &old, ReadAt: 100}},
				{Name: "new", Resources: &store.EdgeResources{PiPower: &fresh, ReadAt: 200}},
			}
		}
	}
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "pi-greenhouse" {
			m.edge.netSel, m.edge.netSelSet, m.edge.netDetail = i, true, true
		}
	}
	page := stripANSI(m.edgeView(116))
	if !strings.Contains(page, "POWER") || !strings.Contains(page, "under-voltage, throttled NOW") {
		t.Errorf("the node page does not say the Pi is short of power:\n%s", page)
	}
	if strings.Contains(stripANSI(mapReadableModel(t).edgeView(116)), "POWER") {
		t.Errorf("a machine that reports no power word was given one")
	}
}

// Who asked for something is kept as a node id for audit and said by name; a caller renamed since is
// said as it was, with what it is called now (the remote-model audit ruling, 2026-10-02).
func TestTheNodePageSaysARenamedCallerByItsNameNow(t *testing.T) {
	m := mapReadableModel(t)
	callerID := ""
	for i := range m.edge.rows {
		if m.edge.rows[i].n.Name == "pi-greenhouse" {
			callerID = m.edge.rows[i].n.ID
		}
	}
	if callerID == "" {
		t.Fatal("no pi-greenhouse in the rig")
	}
	for i := range m.edge.rows {
		n := &m.edge.rows[i].n
		if n.Name == "studio-mac" {
			n.History = []store.EdgeEvent{{At: time.Now().Unix(), What: "model", Detail: "load qwen, asked by gh", By: callerID}}
		}
	}
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "studio-mac" {
			m.edge.netSel, m.edge.netSelSet, m.edge.netDetail = i, true, true
		}
	}
	out := stripANSI(m.edgeView(140))
	if !strings.Contains(out, "load qwen, asked by gh (now pi-greenhouse)") {
		t.Errorf("a renamed caller is not said by its name now:\n%s", out)
	}
	if strings.Contains(out, callerID) {
		t.Errorf("the caller's id is on the page:\n%s", out)
	}
}
