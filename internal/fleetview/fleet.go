package fleetview

// The Edge FLEET screen: this machine as the hub, the rogers running on it, the members it has
// adopted and the devices asking to be - drawn as a map with a readout for whatever is selected,
// and the actions that manage them in place (adopt, rename, forget, where a turn goes first).
// Actions are functions handed in by the caller; the screen never reaches into the Edge itself.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	lm "github.com/thebanri/limoni"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// ItemKind is what a line on the map is.
type ItemKind int

const (
	ItemHub       ItemKind = iota // this machine
	ItemInstance                  // a roger running here
	ItemAgent                     // an agent roger running here (or a persistent one, dark)
	ItemMember                    // another machine on this Edge
	ItemCandidate                 // a discovered device not yet on this Edge
)

// Cap is one capability as a node declares it: CLAIMED or VERIFIED.
type Cap struct{ Name, State string }

// label is what to print for this item: the short label when the record was never named, else the
// name itself.
func (it FleetItem) label() string {
	if it.Label != "" {
		return it.Label
	}
	return it.Name
}

// FleetItem is one line of the map with everything its readout needs.
type FleetItem struct {
	Kind ItemKind
	ID   string // node id (hub, member, candidate)
	// Name is the RECORD name: what identity, an adopt, a rule scope and an agent action key off.
	// Label is what to PRINT, and it differs only for a record that was never named by its owner and
	// carries its own id as a name - a 48-character id is not a name (edge.Label). Draw the label,
	// act on the name.
	Name     string
	Label    string
	NodeKind string // host | board | mobile
	Presence string // "online", "offline 1d", "discovered"
	Dark     bool
	Self     bool // the roger you are looking at
	Bands    []string
	Job      string
	Caps     []Cap
	Via      string
	LastSeen time.Time
	Health   *edge.NodeHealth
	Alerts   []string
	// The re-join story, told on ONE unit: Asking is a dark member back on the network
	// advertising itself (a re-admits it); ClaimStanding is a claim grant waiting for the device
	// to fetch its certificate at the authority's AuthPort.
	Asking        bool
	ClaimStanding bool
	ClaimAt       time.Time // when the claim was issued: the adoption's first stage
	AuthPort      string
	// THE LOG: the unit's last hour, one cell a minute (oldest first), and how long it has been
	// in its current state.
	Log []edge.UnitState
	// The last hour of its readings, one value a minute, NaN where it did not report or was dark.
	TempHour, LoadHour, MemHour []float64
	Since                       time.Duration
	SinceCapped                 bool // the run reaches the oldest sample: it began before the log did
	// The OP and JOB line: the jobs the rogers on this unit hold, and the sessions it served.
	Jobs   []string
	Served int
	// AlarmSince is the oldest standing call on this unit: TROUBLE has lasted at least that long.
	AlarmSince time.Duration
	// LastRead is when this unit last read the trouble feed (a phone's Roger app hearing the Edge).
	LastRead time.Time
	// Lines are the rule thresholds in force for this unit by metric, so a meter draws its line
	// whether or not the unit is over it.
	Lines map[string]float64
	// Software is the unit's inventory line (features/edge/inventory.feature): "roger 6.9.0 · Debian
	// GNU/Linux 12 · 6.1.0 · arm64 · Raspberry Pi 5"; Behind is the newest roger on the Edge when this
	// unit runs an older one, "" when it is current or reports none. A member with several rogers
	// carries one line per roger, each led by its name.
	Software []string
	Behind   string
	// Tags are the OWNER's words for this unit (features/edge/tags.feature): the groups a rule can
	// cover. Empty on a unit the owner has not labelled, and on a candidate, which is not theirs yet.
	Tags []string
	// Drift is what this unit does not match, one line per expectation (features/edge/drift.feature).
	// Empty on a unit in order, and on one whose facts are not known - an unknown is not a wrong.
	Drift []string
	// Maintain is true when the unit declares the maintain capability - its owner turned maintenance
	// on there, so a member holding a maintain grant can start one of its agents
	// (features/edge/maintenance.feature). False says nothing at all, not "refused".
	Maintain bool
	// Controllable is whether this machine has a face it can dial for the unit (edge.Controllable).
	// A member heard but not controllable reads HEARD, not ON AIR: what can be done to it differs.
	Controllable bool
	// Model is what the unit serves of its own and is fetching, in edge.ModelLine's words; Serving is
	// the same in the few words a rack row has room for.
	Model   string
	Serving string
	// Phone is a phone's own signed vitals (battery, charging, thermal, app in front); nil otherwise.
	Phone *store.EdgePhone
	// Codes is what the member said, when it joined, about checking this Edge's code (ruling D9):
	// "checks" when it said, with a signature, that it verifies the root proof, "old" when it joined without saying so (a
	// roger or app from before root proofs), "" when this machine never heard it join.
	Codes string
}

// crew is the unit's CREW line: who runs the Edge and what the unit served. jobLine is what the
// rogers on it hold, one line of its own so neither is cut.
func (it FleetItem) crew(op string) []lm.Span {
	var out []lm.Span
	if op != "" {
		out = append(out, eyebrow("op ", muted), span(op, ink))
	}
	if it.Served > 0 {
		out = append(out, span(fmt.Sprintf("  %d session%s served", it.Served, plural(it.Served)), muted))
	}
	return out
}

func (it FleetItem) jobLine() []lm.Span {
	if len(it.Jobs) == 0 {
		return nil
	}
	return []lm.Span{span(strings.Join(it.Jobs, " · "), ink)}
}

// state is the unit's state in the station's words, and how long: ON AIR 3h12m, DARK 41m,
// TROUBLE 7m. From the log when there is one; from what is known otherwise.
func (it FleetItem) state(now time.Time) (string, lm.Color, time.Duration) {
	if it.ClaimStanding && !it.Asking && !it.ClaimAt.IsZero() {
		return "ADOPTING", glow, now.Sub(it.ClaimAt) // adopted, its certificate not yet fetched
	}
	st := edge.StateUnknown
	if n := len(it.Log); n > 0 {
		st = it.Log[n-1]
	}
	if st == edge.StateUnknown {
		switch {
		case it.Dark:
			st = edge.StateDark
		case len(it.Alerts) > 0:
			st = edge.StateTrouble
		default:
			st = edge.StateAir
		}
	}
	since := it.Since
	if it.Dark && !it.LastSeen.IsZero() && now.Sub(it.LastSeen) > since {
		since = now.Sub(it.LastSeen) // the log began after it went dark; last-seen knows better
	}
	if st == edge.StateTrouble && it.AlarmSince > since {
		since = it.AlarmSince // the call has stood longer than the log has watched
	}
	switch st {
	case edge.StateDark:
		return edge.WordDark, ash, since
	case edge.StateTrouble:
		return edge.WordTrouble, live, since
	}
	if p := it.Phone; it.Kind == ItemMember && p != nil && p.Foreground != nil && !*p.Foreground &&
		now.Sub(time.Unix(p.ReadAt, 0)) <= edge.DefaultTTL {
		return edge.WordAsleep, muted, since // the system put its app in the background: not broken
	}
	if it.Kind == ItemMember && !it.Controllable {
		return edge.WordHeard, glow, since // heard, with no face this machine can dial
	}
	return edge.WordOnAir, signal, since
}

// sinceWords is the duration in state, with "≥" when the log began inside the run (it cannot say
// when the run really started).
func (it FleetItem) sinceWords(now time.Time) string {
	_, _, d := it.state(now)
	if d <= 0 {
		return ""
	}
	if it.SinceCapped && !(it.Dark && !it.LastSeen.IsZero()) && it.AlarmSince <= it.Since {
		return "≥" + edge.Age(d)
	}
	return edge.Age(d)
}

// stateWords is the state and its duration as one span pair.
func (it FleetItem) stateWords(now time.Time) []lm.Span {
	w, c, _ := it.state(now)
	if d := it.sinceWords(now); d != "" {
		return []lm.Span{span(w+" ", c), span(d, c)}
	}
	return []lm.Span{span(w, c)}
}

// SelfFacts is this machine's place on its Edge, worded by the caller (the same lines the CLI
// and the console print).
type SelfFacts struct {
	Name, Root, Authority, Prefer, PreferValue, Machine, Discovery, Join string
	AuthorityLines                                                       []string // the readout's full account
	Enrolled                                                             bool
	AgentActive                                                          bool   // this roger runs as an agent
	Op                                                                   string // the operator: the owner's handle
}

// AlarmLine is one standing trouble call as the TROUBLE tab lists it.
type AlarmLine struct {
	RuleID, NodeID, Node, Metric, When, Value string
	Since                                     time.Duration
	Acked                                     bool
	AckBy                                     string
	Shelved                                   bool // on STAND BY until StandByUntil
	StandByUntil                              time.Time
	StandByBy                                 string // who quieted it
}

// Stale is a call standing over a day: the rule, not the unit, is probably wrong.
func (a AlarmLine) Stale() bool { return a.Since > 24*time.Hour }

// Priority is the call's rank - edge.Priority, so every surface ranks alike.
func (a AlarmLine) Priority() string { return edge.Priority(edge.Metric(a.Metric)) }

// GrantLine is one authority-signed grant as the GRANTS tab lists it: its facts, never a secret.
type GrantLine struct{ ID, Caller, Caps, Callee, Until, State string }

// SessionLine is one session (grouped) as the SESSIONS tab lists it: the traffic this Edge carried.
type SessionLine struct {
	Who, Band, Route, Station, Outcome, Age string
	Count                                   int
}

// FleetSnapshot is one read of the whole Edge.
type FleetSnapshot struct {
	Self     SelfFacts
	Items    []FleetItem // draw order: hub, its rogers, members, candidates
	Grants   []GrantLine
	Sessions []SessionLine
	Alarms   []AlarmLine
	// Events is the merged timeline (features/edge/events.feature), newest first: what happened on
	// this Edge, from the node histories and the alert crossings.
	Events  []EventLine
	Rate    int // calls fired in the last ten minutes; ten is a PILE-UP
	SignOff SignOffLine
	At      time.Time
}

// SignOffLine is the shift in one line: raised, cleared, the loudest unit, who was dark and how long.
type SignOffLine struct {
	Raised, Cleared int
	Loudest         string
	LoudestCount    int
	DeadAir         []DeadAir
}

// DeadAir is one unit's dark time this shift.
type DeadAir struct {
	Node string
	For  time.Duration
}

// signOffSpans words the shift; empty when nothing happened.
func (f *Fleet) signOffSpans() []lm.Span {
	so := f.snap.SignOff
	if so.Raised == 0 && so.Cleared == 0 && len(so.DeadAir) == 0 {
		return nil
	}
	out := []lm.Span{span("   ", dim), eyebrow("sign-off", muted), span("  this shift  ", dim)}
	parts := []string{}
	if so.Raised > 0 {
		parts = append(parts, fmt.Sprintf("%d raised", so.Raised))
	}
	if so.Cleared > 0 {
		parts = append(parts, fmt.Sprintf("%d cleared", so.Cleared))
	}
	if len(parts) == 0 {
		parts = append(parts, "no calls")
	}
	out = append(out, span(strings.Join(parts, " · "), ink))
	if so.Loudest != "" {
		out = append(out, span(" · ", dim), span(fmt.Sprintf("loudest %s (%d)", so.Loudest, so.LoudestCount), ink))
	}
	if len(so.DeadAir) > 0 {
		parts := make([]string, 0, len(so.DeadAir))
		for _, d := range so.DeadAir {
			parts = append(parts, d.Node+" "+edge.Age(d.For))
		}
		out = append(out, span(" · ", dim), span("dead air ", ash), span(strings.Join(parts, ", "), ash))
	}
	return out
}

// FleetSource reads a snapshot.
type FleetSource func() (FleetSnapshot, error)

// FleetActions are what the keys do. A nil action is not offered.
type FleetActions struct {
	Adopt       func(id, name string) (string, error) // returns the line to show
	Forget      func(id string) error
	Rename      func(id, name string) error
	SetPrefer   func(want string) error
	RevokeGrant func(id string) error
	SetAgent    func(on bool) error
	ResumeAgent func(name string) error
	RemoveAgent func(name string) error
	Roger       func(ruleID, nodeID string) error                  // acknowledge a standing trouble call
	StandBy     func(ruleID, nodeID string, d time.Duration) error // quiet a call for a time
	BindJob     func(name, model, job string) error                // bind a model and a job to a roger here
	ClearJob    func(name string) error
	IssueGrant  func(caller, capability, callee, forDur string) (string, error) // returns the line to show
	AskNow      func(id string) (string, error)                                 // ask a unit to report now (fleet_window.feature)
}

// Fleet is the screen.
type Fleet struct {
	Source  FleetSource
	Actions FleetActions
	Every   time.Duration
	// health is the read behind a unit's health page: the rules in force, where alerts go, the
	// session's history of readings, and the rule edits. HEALTH is not a tab of its own any more -
	// it is folded into FLEET (fleet_window.feature, WHERE IT LIVES) - so nil only means the page
	// draws each unit's own readings with no rules pane.
	health *Health

	snap     FleetSnapshot
	err      error
	readAt   time.Time
	sel      int
	gsel     int  // the selected grant on the GRANTS tab
	asel     int  // the selected call on the TROUBLE tab
	tab      int  // see tabs(): fleet, andon, trouble, grants, sessions, events
	page     bool // FLEET with the selected unit's health open (6, or enter on a unit)
	frame    int  // advances each frame; the pulses breathe on it
	confirm  bool
	askID    string // the unit the forget question is about, fixed when it was asked
	revoke   bool   // the revoke question is up
	revokeID string
	binding  bool // the bind prompt is up (input holds the text)
	issuing  bool // the issue-grant prompt is up
	rename   bool
	input    []rune
	// The rack as the owner chose to see it (order.go): all is the snapshot's own items, and
	// snap.Items is what is drawn - in order, filtered.
	all       []FleetItem
	order     rackOrder
	query     string
	filtering bool   // the / prompt is up (input holds the text)
	want      string // a unit to select once it is on the rack (SelectUnit)
	help      bool   // ? is up: every key, in place of the readout
	toast     string
	toastAt   time.Time
	toastErr  bool
	last      string // the last action's outcome, kept in the readout until the next
	lastErr   bool
}

// NewFleet makes the screen over src. health, when given, is what a unit's health page reads its
// rules, sinks and session history from, and the 6 that once opened the HEALTH tab opens that page.
func NewFleet(src FleetSource, health *Health, acts FleetActions) *Fleet {
	return &Fleet{Source: src, Actions: acts, Every: 2 * time.Second, health: health}
}

// Run opens the screen full-screen until q/esc or ctx ends.
func (f *Fleet) Run(ctx context.Context) error {
	return lm.RunWithContext(ctx, f.Draw, lm.WithTitle("roger edge"), lm.WithFPS(6))
}

// Draw is one frame.
func (f *Fleet) Draw(fr *lm.Frame, ev *lm.Event) bool {
	f.frame++
	if ev != nil && ev.Type == lm.EventKey {
		if !f.onKey(ev) {
			return false
		}
	}
	f.refresh()
	f.clamp()
	if f.page && f.health != nil {
		f.health.refresh() // read only while the page is open: the monitor stays cheap
	}

	f.drawFrame(fr)
	return true
}

// The tabs, by index: ANDON is the whole rack in two seconds (level 1), FLEET the rack (level 2),
// TROUBLE the standing calls, GRANTS and SESSIONS the ledgers. tabHealth is a KEY, not a tab: the
// HEALTH tab was folded into FLEET, and its key now opens FLEET with the selected unit's health.
const (
	tabFleet = iota
	tabAndon
	tabTrouble
	tabGrants
	tabSessions
	tabHealth
	tabEvents
)

// keyOrder is what the digit keys select, in order. The HEALTH slot stays where it was when a
// health read was given (6), so folding the tab into FLEET renumbered nothing an operator already
// learned: 6 opens a unit's health on FLEET and 7 is still EVENTS. Keys map through here rather
// than being cast from the digit, and a new tab is appended at the end.
func (f *Fleet) keyOrder() []int {
	t := []int{tabFleet, tabAndon, tabTrouble, tabGrants, tabSessions}
	if f.health != nil {
		t = append(t, tabHealth)
	}
	return append(t, tabEvents)
}

// stripOrder is the tab strip: every key's tab except the HEALTH slot, which is FLEET.
func (f *Fleet) stripOrder() []int {
	var out []int
	for _, t := range f.keyOrder() {
		if t != tabHealth {
			out = append(out, t)
		}
	}
	return out
}

// tabs is the tab strip, in the same order as stripOrder.
func (f *Fleet) tabs() []string {
	names := map[int]string{tabFleet: "fleet", tabAndon: "andon", tabTrouble: "trouble",
		tabGrants: "grants", tabSessions: "sessions", tabEvents: "events"}
	order := f.stripOrder()
	out := make([]string, 0, len(order))
	for _, t := range order {
		out = append(out, names[t])
	}
	return out
}

// tabAt is the strip position of the tab in force, for the strip's own highlight.
func (f *Fleet) tabAt() int {
	for i, t := range f.stripOrder() {
		if t == f.tab {
			return i
		}
	}
	return 0
}

// onKey handles one key; false ends the screen.
func (f *Fleet) onKey(ev *lm.Event) bool {
	k := ev.Key
	switch {
	case f.filtering:
		switch {
		case k.Type == lm.KeyEsc:
			f.filtering, f.query = false, ""
			f.applyView(f.selected())
		case k.Type == lm.KeyEnter:
			f.filtering = false
			f.query = strings.TrimSpace(string(f.input))
			f.applyView(f.selected())
		case k.Type == lm.KeyBackspace:
			if len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
			}
		case k.Type == lm.KeySpace:
			f.input = append(f.input, ' ')
		case k.Ch != 0:
			f.input = append(f.input, k.Ch)
		}
		return true
	case f.issuing:
		switch {
		case k.Type == lm.KeyEsc:
			f.issuing = false
		case k.Type == lm.KeyEnter:
			f.issuing = false
			words := strings.Fields(string(f.input))
			if len(words) < 3 {
				f.say("a grant needs a caller, a capability and a callee: self operate nas 7d", true)
				return true
			}
			forDur := "30d"
			if len(words) > 3 {
				forDur = words[3]
			}
			if f.Actions.IssueGrant != nil {
				msg, err := f.Actions.IssueGrant(words[0], words[1], words[2], forDur)
				if err != nil {
					f.say(err.Error(), true)
				} else {
					f.say(msg, false)
				}
				f.readAt = time.Time{}
			}
		case k.Type == lm.KeyBackspace:
			if len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
			}
		case k.Type == lm.KeySpace:
			f.input = append(f.input, ' ')
		case k.Ch != 0:
			f.input = append(f.input, k.Ch)
		}
		return true
	case f.binding:
		switch {
		case k.Type == lm.KeyEsc:
			f.binding = false
		case k.Type == lm.KeyEnter:
			it := f.selected()
			f.binding = false
			words := strings.Fields(string(f.input))
			model, job := "", ""
			if len(words) > 0 && words[0] != "-" {
				model = words[0]
			}
			if len(words) > 1 {
				job = words[1]
			}
			if (model != "" || job != "") && f.Actions.BindJob != nil {
				f.report(f.Actions.BindJob(it.Name, model, job), "bound "+strings.TrimSpace(model+" "+job)+" to "+it.Name)
			}
		case k.Type == lm.KeyBackspace:
			if len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
			}
		case k.Type == lm.KeySpace:
			f.input = append(f.input, ' ')
		case k.Ch != 0:
			f.input = append(f.input, k.Ch)
		}
		return true
	case f.rename:
		switch {
		case k.Type == lm.KeyEsc:
			f.rename = false
		case k.Type == lm.KeyEnter:
			name := strings.TrimSpace(string(f.input))
			it := f.selected()
			f.rename = false
			if name != "" && f.Actions.Rename != nil {
				f.report(f.Actions.Rename(it.ID, name), "renamed to "+name)
			}
		case k.Type == lm.KeyBackspace:
			if len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
			}
		case k.Type == lm.KeySpace:
			f.input = append(f.input, ' ')
		case k.Ch != 0:
			f.input = append(f.input, k.Ch)
		}
		return true
	case f.confirm:
		if k.Ch == 'y' || k.Ch == 'Y' {
			it, still := f.itemByID(f.askID) // the unit the question was about, not whatever is under the cursor now
			f.confirm = false
			switch {
			case !still:
				f.say(f.askID+" is no longer on the rack - nothing forgotten", true)
			case f.Actions.Forget != nil:
				f.report(f.Actions.Forget(it.ID), "forgot "+it.label())
			}
		} else {
			f.confirm = false
			f.say("left alone", false)
		}
		return true
	case f.revoke:
		f.revoke = false
		if (k.Ch == 'y' || k.Ch == 'Y') && f.revokeID != "" && f.Actions.RevokeGrant != nil {
			f.report(f.Actions.RevokeGrant(f.revokeID), "revoked "+f.revokeID)
		} else {
			f.say("left standing", false)
		}
		return true
	}
	onPage := f.page && f.tab == tabFleet
	if onPage && f.health != nil && f.health.Busy() {
		f.health.onKey(ev) // a rule prompt owns every key, q and the tab numbers included
		f.readAt = time.Time{}
		return true
	}
	if onPage && k.Type == lm.KeyEsc {
		f.page = false // esc on a unit's health comes back to the rack, on the same unit
		return true
	}
	// Tabs, from anywhere.
	switch {
	case k.Ch == 'q' || k.Ch == 'Q' || k.Type == lm.KeyEsc:
		return false
	case k.Ch >= '1' && k.Ch <= '9' && int(k.Ch-'1') < len(f.keyOrder()):
		f.tab, f.page = f.keyOrder()[int(k.Ch-'1')], false
		if f.tab == tabHealth { // the folded HEALTH tab: FLEET, with the selected unit's health open
			f.tab = tabFleet
			f.openPage()
		}
		return true
	case k.Type == lm.KeyTab:
		order := f.stripOrder()
		f.tab, f.page = order[(f.tabAt()+1)%len(order)], false
		return true
	}
	if onPage {
		f.pageKey(ev)
		return true
	}
	switch f.tab {
	case tabTrouble:
		calls := f.sortedAlarms()
		switch {
		case k.Type == lm.KeyDown || k.Ch == 'j':
			f.asel = clampIndex(f.asel+1, len(calls))
		case k.Type == lm.KeyUp:
			f.asel = clampIndex(f.asel-1, len(calls))
		case k.Ch == 'k':
			if f.asel < len(calls) && f.Actions.Roger != nil && !calls[f.asel].Acked {
				c := calls[f.asel]
				f.report(f.Actions.Roger(c.RuleID, c.NodeID), "roger · "+c.When+" on "+c.Node)
			}
		case k.Ch == 's':
			if f.asel < len(calls) && f.Actions.StandBy != nil && !calls[f.asel].Shelved {
				c := calls[f.asel]
				f.report(f.Actions.StandBy(c.RuleID, c.NodeID, time.Hour), "stand by an hour · "+c.When+" on "+c.Node)
			}
		case k.Ch == 'r':
			f.readAt = time.Time{}
		}
		return true
	case tabGrants:
		switch {
		case k.Type == lm.KeyDown || k.Ch == 'j':
			f.gsel = clampIndex(f.gsel+1, len(f.snap.Grants))
		case k.Type == lm.KeyUp || k.Ch == 'k':
			f.gsel = clampIndex(f.gsel-1, len(f.snap.Grants))
		case k.Ch == 'x':
			if f.gsel < len(f.snap.Grants) && f.Actions.RevokeGrant != nil && f.snap.Grants[f.gsel].State == "live" {
				f.revoke, f.revokeID = true, f.snap.Grants[f.gsel].ID
			}
		case k.Ch == 'i':
			if f.Actions.IssueGrant != nil {
				f.issuing, f.input = true, nil
			}
		case k.Ch == 'r':
			f.readAt = time.Time{}
		}
		return true
	case tabSessions, tabEvents: // ledgers: read, re-read, nothing to work
		if k.Ch == 'r' {
			f.readAt = time.Time{}
		}
		return true
	case tabAndon: // the board is read, not worked: move between units, re-read, nothing else
		switch {
		case k.Type == lm.KeyDown || k.Ch == 'j':
			f.sel++
		case k.Type == lm.KeyUp || k.Ch == 'k':
			f.sel--
		case k.Ch == 'r':
			f.readAt = time.Time{}
		}
		f.clamp()
		return true
	}
	switch {
	case k.Type == lm.KeyEnter:
		if it := f.selected(); it.Kind == ItemHub || it.Kind == ItemMember {
			f.page = true
		}
	case k.Ch == '/':
		f.filtering, f.input = true, []rune(f.query)
	case k.Ch == '?':
		f.help = !f.help
	case k.Ch == 'h':
		if it := f.selected(); it.Kind == ItemMember && f.Actions.AskNow != nil {
			switch {
			case it.Dark:
				f.say(it.label()+" is dark: there is nothing to ask until it is heard again", true)
			case !it.Controllable:
				f.say(it.label()+" has no face this machine can dial: it can be heard, not asked", true)
			default:
				msg, err := f.Actions.AskNow(it.ID)
				f.report(err, msg)
				f.readAt = time.Time{} // draw what it just said
			}
		}
	case k.Ch == 's':
		f.order = nextOrder(f.order)
		f.applyView(f.selected())
		f.say("the rack is "+orderWords[f.order], false)
	case k.Ch == 'g':
		if it := f.selected(); it.Kind == ItemHub && f.Actions.SetAgent != nil {
			on := !f.snap.Self.AgentActive
			if err := f.Actions.SetAgent(on); err != nil {
				f.say(err.Error(), true)
			} else {
				f.snap.Self.AgentActive = on
				if on {
					f.say("this roger is an agent now", false)
				} else {
					f.say("this roger is no longer an agent", false)
				}
			}
		}
	case k.Ch == 'b':
		if it := f.selected(); (it.Kind == ItemAgent || it.Kind == ItemInstance) && !it.Dark && f.Actions.BindJob != nil {
			f.binding, f.input = true, nil
		}
	case k.Ch == 'c':
		if it := f.selected(); (it.Kind == ItemAgent || it.Kind == ItemInstance) && it.Job != "" && f.Actions.ClearJob != nil {
			f.report(f.Actions.ClearJob(it.Name), "cleared the job on "+it.label())
		}
	case k.Ch == 'u':
		if it := f.selected(); it.Kind == ItemAgent && it.Dark && f.Actions.ResumeAgent != nil {
			f.report(f.Actions.ResumeAgent(it.Name), "relaunching "+it.label())
		}
	case k.Ch == 'x':
		if it := f.selected(); it.Kind == ItemAgent && it.Dark && f.Actions.RemoveAgent != nil {
			f.report(f.Actions.RemoveAgent(it.Name), "forgot agent "+it.label())
		}
	case k.Ch == 'r':
		f.readAt = time.Time{}
	case k.Type == lm.KeyDown || k.Ch == 'j':
		f.sel++
	case k.Type == lm.KeyUp || k.Ch == 'k':
		f.sel--
	case k.Ch == 'a':
		if it := f.selected(); (it.Kind == ItemCandidate || (it.Kind == ItemMember && it.Asking)) && f.Actions.Adopt != nil {
			msg, err := f.Actions.Adopt(it.ID, it.Name)
			if err != nil {
				f.say(err.Error(), true)
			} else {
				f.say(msg, false)
			}
			f.readAt = time.Time{}
		}
	case k.Ch == 'f':
		if it := f.selected(); it.Kind == ItemMember && f.Actions.Forget != nil {
			f.confirm, f.askID = true, it.ID
		}
	case k.Ch == 'n':
		if it := f.selected(); it.Kind == ItemMember && f.Actions.Rename != nil {
			f.rename, f.input = true, nil // typed fresh; the old name stays in view as the hint
		}
	case k.Ch == 'p':
		if f.Actions.SetPrefer != nil {
			next := nextPrefer(f.snap.Self.PreferValue)
			if err := f.Actions.SetPrefer(next); err != nil {
				f.say(err.Error(), true)
			} else {
				f.snap.Self.PreferValue, f.snap.Self.Prefer = next, "prefers "+next
				f.say("turns go "+next+" first", false)
			}
		}
	}
	return true
}

// openPage opens the selected unit's health; on a roger or a bench device, which report no health
// of their own, it opens on the nearest unit instead.
func (f *Fleet) openPage() {
	f.page = true
	f.refresh()
	f.clamp()
	if it := f.selected(); it.Kind != ItemHub && it.Kind != ItemMember {
		if !f.stepUnit(-1) {
			f.stepUnit(1)
		}
	}
}

// stepUnit moves the selection to the next unit (the hub or a member) in direction d, skipping the
// rogers and the bench; false when there is none that way.
func (f *Fleet) stepUnit(d int) bool {
	for i := f.sel + d; i >= 0 && i < len(f.snap.Items); i += d {
		if k := f.snap.Items[i].Kind; k == ItemHub || k == ItemMember {
			f.sel = i
			return true
		}
	}
	return false
}

// pageKey is a key on a unit's health page: the arrows walk the units, r re-reads, and the rule keys
// edit the rules in place through the health read's own actions.
func (f *Fleet) pageKey(ev *lm.Event) {
	k := ev.Key
	switch {
	case k.Type == lm.KeyDown || k.Ch == 'j':
		f.stepUnit(1)
	case k.Type == lm.KeyUp || k.Ch == 'k':
		f.stepUnit(-1)
	case k.Ch == 'r':
		f.readAt = time.Time{}
		if f.health != nil {
			f.health.readAt = time.Time{}
		}
	case f.health != nil && (k.Ch == 'a' || k.Ch == '[' || k.Ch == ']' || k.Ch == 'o' || k.Ch == 'x'):
		f.health.onKey(ev)
		f.readAt = time.Time{} // a rule changed: the rack's lines and calls follow it
	}
}

// preferWord is the preference as a word; the default is local.
func preferWord(v string) string {
	if v == "" {
		return "local"
	}
	return v
}

func nextPrefer(cur string) string {
	switch cur {
	case "market":
		return "local-only"
	case "local-only":
		return "local"
	default:
		return "market"
	}
}

func (f *Fleet) report(err error, ok string) {
	if err != nil {
		f.say(err.Error(), true)
	} else {
		f.say(ok, false)
	}
	f.readAt = time.Time{}
}

func (f *Fleet) say(s string, isErr bool) {
	f.toast, f.toastAt, f.toastErr = s, time.Now(), isErr
	f.last, f.lastErr = s, isErr
}

func (f *Fleet) refresh() {
	if !f.readAt.IsZero() && time.Since(f.readAt) < f.Every {
		return
	}
	first := f.snap.At.IsZero()
	snap, err := f.Source()
	f.readAt, f.err = time.Now(), err
	if err == nil {
		was := f.selected()
		f.snap = snap
		f.all = snap.Items
		f.applyView(was)
		if first {
			f.signOn()
		}
	}
}

// SelectUnit puts the cursor on a unit by id, now if it is on the rack or as soon as it arrives -
// what `roger edge fleet --select` and the [3] screen's o use to open the window on a unit.
func (f *Fleet) SelectUnit(id string) {
	f.want = id
	if len(f.all) > 0 {
		f.applyView(f.selected())
	}
}

// OpenHealth opens the window on FLEET with the selected unit's health open - what
// `roger edge fleet --health` and the [3] screen's h use, since HEALTH is folded into FLEET.
func (f *Fleet) OpenHealth() { f.tab = tabFleet; f.openPage() }

// applyView re-draws the rack in the owner's order and filter, keeping the cursor on the same unit
// when it is still there - a unit never slides out from under a reader's eye.
func (f *Fleet) applyView(was FleetItem) {
	f.snap.Items = arrange(f.all, f.order, f.query, f.snap.At)
	if f.want != "" {
		for i, it := range f.snap.Items {
			if it.ID == f.want {
				f.sel, f.want = i, ""
				return
			}
		}
	}
	for i, it := range f.snap.Items {
		if it.Kind == was.Kind && it.ID == was.ID && it.Name == was.Name {
			f.sel = i
			return
		}
	}
	f.clamp()
}

// signOn is the greeting when the screen opens: since when this machine has been on air, and to
// whom - six seconds in the action bar, then gone.
func (f *Fleet) signOn() {
	for _, it := range f.snap.Items {
		if it.Kind != ItemHub || it.Health == nil || it.Health.Readings == nil || it.Health.Readings.UptimeS <= 0 {
			continue
		}
		up := f.snap.At.Add(-time.Duration(it.Health.Readings.UptimeS) * time.Second)
		msg := "on air since " + up.Format("15:04 Jan 2")
		if op := f.snap.Self.Op; op != "" {
			msg += " · good " + partOfDay(f.snap.At) + ", " + op
		}
		f.toast, f.toastAt, f.toastErr = msg, time.Now(), false
		return
	}
}

func partOfDay(t time.Time) string {
	switch h := t.Hour(); {
	case h < 12:
		return "morning"
	case h < 18:
		return "afternoon"
	}
	return "evening"
}

func (f *Fleet) clamp() {
	if f.sel >= len(f.snap.Items) {
		f.sel = len(f.snap.Items) - 1
	}
	if f.sel < 0 {
		f.sel = 0
	}
}

// itemByID finds a unit by id; false when it has left the rack.
func (f *Fleet) itemByID(id string) (FleetItem, bool) {
	for _, it := range f.snap.Items {
		if it.ID == id && id != "" {
			return it, true
		}
	}
	return FleetItem{}, false
}

func (f *Fleet) selected() FleetItem {
	if len(f.snap.Items) == 0 {
		return FleetItem{}
	}
	return f.snap.Items[f.sel]
}

// --- drawing: the rack ---------------------------------------------------------------------

func span(s string, c lm.Color) lm.Span { return lm.NewSpan(s, lm.Fg(c)) }
func bold(s string, c lm.Color) lm.Span { return lm.NewSpan(s, lm.Fg(c).Bold()) }

func (f *Fleet) drawFrame(fr *lm.Frame) {
	rows := lm.SplitVertical(fr.Area(), lm.Fixed(2), lm.Fill(), lm.Fixed(1))
	units, alerts := 0, 0
	for _, it := range f.snap.Items {
		if it.Kind == ItemHub || it.Kind == ItemMember {
			units++
		}
		alerts += len(it.Alerts)
	}
	right := clock(f.snap.At) + " · " + unitsWord(units)
	hot := false
	for _, a := range f.snap.Alarms {
		if a.Priority() == "HI" && !a.Acked && !a.Shelved {
			hot = true
		}
	}
	brandBand(fr, rows[0], f.tabs(), f.tabAt(), right, alerts, hot, f.frame)
	switch {
	case f.err == nil && f.tab == tabGrants:
		f.drawGrants(fr, rows[1])
		actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
		if f.revoke && f.revokeID != "" {
			confirmBox(fr, "revoke "+f.revokeID+"?", "every node stops honoring it at once; a revocation is final", "y revoke", "n keep it")
		}
		return
	case f.err == nil && f.tab == tabEvents:
		f.drawEvents(fr, rows[1])
		actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
		return
	case f.err == nil && f.tab == tabSessions:
		f.drawSessions(fr, rows[1])
		actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
		return
	case f.err == nil && f.tab == tabTrouble:
		f.drawTrouble(fr, rows[1])
		actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
		return
	case f.err == nil && f.tab == tabAndon:
		f.drawAndon(fr, rows[1])
		actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
		return
	case f.err == nil && f.page:
		f.drawHealthPage(fr, rows[1])
		toast, toastErr, toastAt := f.toast, f.toastErr, f.toastAt
		if h := f.health; h != nil && h.saidAt.After(toastAt) { // a rule edit's outcome
			toast, toastErr, toastAt = h.last, h.lastErr, h.saidAt
		}
		actionBar(fr, rows[2], f.actions(), toast, toastErr, toastAt)
		return
	}
	if f.err != nil {
		fr.RenderWidget(lm.NewText(
			lm.NewLine(),
			lm.NewLine(span("  ", dim), bold("could not read the Edge", live)),
			lm.NewLine(span("  ", dim), span(f.err.Error(), ink)),
		).WithWrap(false), rows[1])
	} else {
		cols := lm.SplitHorizontal(rows[1], lm.Percentage(54), lm.Fixed(1), lm.Fill())
		n := f.drawRack(fr, cols[0])
		if m := f.drawReadout(fr, cols[2]); m > n {
			n = m
		}
		hairline(fr, cols[1], n+1)
	}
	actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
	if f.confirm {
		it, _ := f.itemByID(f.askID)
		confirmBox(fr, "forget "+it.label()+"?", "it leaves this Edge and its certificate is revoked", "y forget", "n keep it")
	}
}

// glyph is a roger's mark under the hub: ◆ in the dial's amber, ◇ when it is dark.
func (it FleetItem) glyph() (string, lm.Color) {
	if it.Dark {
		return "◇", ash
	}
	return "◆", glow
}

// heard is when the unit was last heard: a member's last-seen, the hub's own reading time.
func (it FleetItem) heard(at time.Time) time.Time {
	if !it.LastSeen.IsZero() {
		return it.LastSeen
	}
	if it.Kind == ItemHub && it.Health != nil {
		if !it.Health.LastHeard.IsZero() {
			return it.Health.LastHeard
		}
	}
	if it.Kind == ItemHub {
		return at
	}
	return time.Time{}
}

// rogerWords is what a roger on this machine is doing, in a few words.
func (it FleetItem) rogerWords() string {
	switch {
	case it.Dark:
		return "agent · offline"
	case it.Job != "":
		return "agent · " + it.Job
	case it.Kind == ItemAgent && len(it.Bands) > 0:
		return "agent · " + fewBands(it.Bands)
	case it.Kind == ItemAgent:
		return "agent · idle"
	case it.Self:
		return "this one"
	case len(it.Bands) > 0:
		return "serves " + fewBands(it.Bands)
	}
	return "roger"
}

// fewBands names the first two models and counts the rest, so a line stays a line.
func fewBands(b []string) string {
	if len(b) <= 2 {
		return strings.Join(b, " ")
	}
	return strings.Join(b[:2], " ") + fmt.Sprintf(" +%d", len(b)-2)
}

func alertChip(n int) []lm.Span {
	if n == 0 {
		return nil
	}
	return []lm.Span{span("  ", dim), bold(fmt.Sprintf("▲ %d", n), live)}
}

// drawRack is the left column: the hub as the transmitter, its rogers under it, the units
// patched in; the bench pinned at the bottom, where a rack's loose units sit. It returns how
// many rows it drew, so the hairline beside it is as tall as the content.
func (f *Fleet) drawRack(fr *lm.Frame, area lm.Rect) int {
	now := f.snap.At
	narrow := area.Width < 66
	nameW, kindW := 22, 8
	if narrow {
		nameW, kindW = 16, 0 // the eye and the glyph tell the kind
	}
	// wordsW is what is left on a roger's line for its words, and on a unit's line after the meter.
	wordsW := int(area.Width) - 12 - nameW
	if wordsW < 8 {
		wordsW = 8
	}
	rail := func(i int, c lm.Color) lm.Span {
		if i == f.sel {
			return bold("▌", live)
		}
		return span("▌", c)
	}
	nameC := func(i int, dark bool) lm.Color {
		switch {
		case i == f.sel:
			return paper
		case dark:
			return ash
		}
		return ink
	}
	members, cands := 0, 0
	for _, it := range f.snap.Items {
		switch it.Kind {
		case ItemMember:
			members++
		case ItemCandidate:
			cands++
		}
	}
	var top, bench []lm.Line
	top = append(top, f.summaryLine())
	if vl := f.viewLine(); vl != nil {
		top = append(top, *vl)
	}
	if f.filtering {
		top = append(top, lm.NewLine(span("   / ", live), span(string(f.input), ink), span("_", live),
			span("   name, kind:phone, tag:outdoor, state:dark · ⏎ apply · esc clear", dim)))
	}
	seenMember := false
	for i, it := range f.snap.Items {
		switch it.Kind {
		case ItemHub:
			lv := carrierLevel(it.heard(now), false, now)
			top = append(top, lm.NewLine())
			l := lm.NewLine(span(" ", dim), rail(i, dial), span(" ", dim))
			l.Spans = append(l.Spans, eye(lv, false, false, len(it.Alerts) > 0, f.frame, i)...)
			l.Spans = append(l.Spans, span(" ", dim), bold(it.label(), paper), span("   this machine", muted))
			top = append(top, l)
			top = append(top, lm.NewLine(span(" ", dim), rail(i, dial), span("    ", dim), span(strings.ToLower(f.snap.Self.Authority), muted)))
			l = lm.NewLine(span(" ", dim), rail(i, dial), span("    ", dim))
			l.Spans = append(l.Spans, meter(lv, true, -1, f.frame, true)...)
			l.Spans = append(l.Spans, span(" "+sUnit(lv)+"  ", muted))
			l.Spans = append(l.Spans, it.stateWords(now)...)
			if it.Health != nil && it.Health.Readings != nil && it.Health.Readings.UptimeS > 0 {
				l.Spans = append(l.Spans, span(" · up "+edge.Age(time.Duration(it.Health.Readings.UptimeS)*time.Second), muted))
			}
			l.Spans = append(l.Spans, alertChip(len(it.Alerts))...)
			top = append(top, l)
		case ItemInstance, ItemAgent:
			branch := "├"
			if i+1 >= len(f.snap.Items) || (f.snap.Items[i+1].Kind != ItemInstance && f.snap.Items[i+1].Kind != ItemAgent) {
				branch = "╰"
			}
			g, gc := it.glyph()
			if i == f.sel {
				gc = live
			}
			top = append(top, lm.NewLine(span(" ", dim), rail(i, dim), span("      "+branch+" ", dim), bold(g+" ", gc),
				span(pad(elide(it.label(), nameW-2), nameW-2), nameC(i, it.Dark)), span(elide(it.rogerWords(), wordsW), muted)))
		case ItemMember:
			if !seenMember {
				seenMember = true
				top = append(top, lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("on the rack", muted),
					span(fmt.Sprintf("  %d patched in", members), dim)), lm.NewLine())
			}
			lv := carrierLevel(it.LastSeen, it.Dark, now)
			l := lm.NewLine(span(" ", dim), rail(i, dial), span(" ", dim))
			l.Spans = append(l.Spans, eye(lv, it.Dark, false, len(it.Alerts) > 0, f.frame, i)...)
			l.Spans = append(l.Spans, span(" ", dim), span(pad(elide(it.label(), nameW), nameW), nameC(i, it.Dark)))
			if kindW > 0 {
				l.Spans = append(l.Spans, span(pad(kindLabel(it.NodeKind), kindW), dim))
			}
			l.Spans = append(l.Spans, meter(lv, true, -1, f.frame, !it.Dark)...)
			if it.Asking { // the ask is the news; dark is implied by the eye and the flat meter
				l.Spans = append(l.Spans, span(" ", dim), span("asking to re-join", glow))
			} else {
				l.Spans = append(l.Spans, span(" ", dim))
				l.Spans = append(l.Spans, it.stateWords(now)...)
			}
			l.Spans = append(l.Spans, alertChip(len(it.Alerts))...)
			if area.Width >= 76 {
				l.Spans = append(l.Spans, it.rowFacts(lineWidth(l), int(area.Width), now)...)
			}
			top = append(top, l)
		case ItemCandidate:
			if len(bench) == 0 {
				bc := glow // the bench wakes when a device asks: it breathes amber
				if motion && (f.frame/6)%2 == 1 {
					bc = dim
				}
				bench = append(bench, lm.NewLine(span("   ", dim), eyebrow("on the bench", bc),
					span(fmt.Sprintf("  %d asking to be adopted", cands), dim)), lm.NewLine())
			}
			l := lm.NewLine(span(" ", dim), rail(i, glow), span(" ", dim))
			l.Spans = append(l.Spans, eye(0, false, true, false, f.frame, i)...)
			l.Spans = append(l.Spans, span(" ", dim), span(pad(elide(it.label(), nameW), nameW), nameC(i, false)))
			if kindW > 0 {
				l.Spans = append(l.Spans, span(pad(kindLabel(it.NodeKind), kindW), dim))
			}
			l.Spans = append(l.Spans, meter(0, false, -1, 0, false)...)
			l.Spans = append(l.Spans, span(" found · a adopt", glow))
			bench = append(bench, l)
		}
	}
	if !seenMember {
		top = append(top, lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("on the rack", muted), span("  nothing patched in yet", dim)))
	}
	if len(bench) == 0 {
		bench = append(bench, lm.NewLine(span("   ", dim), eyebrow("on the bench", dim), span("  nothing asking", dim)))
		if f.snap.Self.Join != "" {
			bench = append(bench, lm.NewLine(span("   on another machine   ", dim), span(f.snap.Self.Join, ink)))
		}
		bench = append(bench, lm.NewLine(span("   or scan the LAN      ", dim), span("roger edge scan", ink)))
	}
	// A tall rack pins the bench to the floor, where a rack's loose units sit; a short rack lets
	// it follow after two rows of air - the remaining air is then honest, not a hole.
	benchH := uint16(len(bench) + 1)
	if uint16(len(top)) < area.Height/2 || benchH+uint16(len(top))+2 > area.Height {
		all := append(append(top, lm.NewLine(), lm.NewLine()), bench...)
		fr.RenderWidget(lm.NewText(all...).WithWrap(false), area)
		return len(all)
	}
	parts := lm.SplitVertical(area, lm.Fill(), lm.Fixed(benchH))
	fr.RenderWidget(lm.NewText(top...).WithWrap(false), parts[0])
	fr.RenderWidget(lm.NewText(append([]lm.Line{lm.NewLine()}, bench...)...).WithWrap(false), parts[1])
	return int(area.Height)
}

// adoptStall is how long a stage may stand open before the window says what it is waiting on.
const adoptStall = 2 * time.Minute

type stage struct {
	mark, words string
	color       lm.Color
}

// adoptionStages are the steps of an adoption as they have been SEEN, each with its age, and - when
// one has stood open past adoptStall - which one, and what that unit must do (fleet_window.feature).
func adoptionStages(it FleetItem, now time.Time) []stage {
	issued := "claim issued"
	if !it.ClaimAt.IsZero() {
		issued += " " + edge.Age(now.Sub(it.ClaimAt)) + " ago"
	}
	out := []stage{{"✓", issued, signal}}
	waiting := now.Sub(it.ClaimAt)
	if !it.ClaimAt.IsZero() && waiting > adoptStall {
		out = append(out, stage{"…", "certificate not fetched after " + edge.Age(waiting) + ": it must reach this authority " +
			atAuthority(it.AuthPort) + " - check that machine can see this one, then run roger edge enroll there", live})
	} else {
		out = append(out, stage{"…", "certificate: waiting for it to fetch one", glow})
	}
	return append(out, stage{"·", "first presence: not yet", dim})
}

// sparkLevels draw a reading's hour; a space is a minute with no reading.
var sparkLevels = []rune("▁▂▃▄▅▆▇█")

// hourLine draws values (oldest first) in at most w cells, each cell the highest reading of the
// minutes it covers. ok is false when there is nothing to draw.
func hourLine(vals []float64, w int) (line string, lo, hi float64, ok bool) {
	if len(vals) == 0 || w < 8 {
		return "", 0, 0, false
	}
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		if !math.IsNaN(v) {
			lo, hi, ok = math.Min(lo, v), math.Max(hi, v), true
		}
	}
	if !ok {
		return "", 0, 0, false
	}
	cells := min(w, len(vals))
	per := float64(len(vals)) / float64(cells)
	out := make([]rune, cells)
	for c := 0; c < cells; c++ {
		best := math.NaN()
		for i := int(float64(c) * per); i < int(float64(c+1)*per) && i < len(vals); i++ {
			if !math.IsNaN(vals[i]) && (math.IsNaN(best) || vals[i] > best) {
				best = vals[i]
			}
		}
		switch {
		case math.IsNaN(best):
			out[c] = ' '
		case hi == lo:
			out[c] = sparkLevels[len(sparkLevels)/2]
		default:
			out[c] = sparkLevels[int((best-lo)/(hi-lo)*float64(len(sparkLevels)-1)+0.5)]
		}
	}
	return string(out), lo, hi, true
}

// allKeys is every key the fleet window answers to, what it does and where (fleet_window.feature:
// "question mark lists them all"). The footer shows only those that do something right now.
var allKeys = []struct{ key, what string }{
	{"↑↓ j k", "move between units"},
	{"/", "filter: a name, kind:phone, tag:outdoor, state:dark · esc clears"},
	{"s", "order the rack: worst first, name, kind, last heard, hottest"},
	{"h", "hear now: ask a unit to report at once (a unit with a face to dial)"},
	{"a", "adopt a unit on the bench, or re-admit one asking to re-join"},
	{"n", "rename a unit"},
	{"f", "forget a unit - asked first, and only y says yes"},
	{"p", "on this machine: where a turn goes first"},
	{"g", "on this machine: make this roger an agent, or stop"},
	{"b c u x", "on a roger: bind a model, clear its job, resume, remove"},
	{"⏎ 6", "a unit's health: every reading with its age, the rules · esc back to the rack"},
	{"a [ ] o x", "on a unit's health: add a rule, walk the rules, turn one off or on, remove it"},
	{"1 to 7", "fleet, andon, trouble, grants, sessions, 6 health on fleet, events"},
	{"r", "re-read now"},
	{"?", "this list, again to close it"},
	{"q", "quit"},
}

func (f *Fleet) drawKeys(fr *lm.Frame, area lm.Rect) int {
	lines := []lm.Line{lm.NewLine(), lm.NewLine(span("  ", dim), eyebrow("keys", muted), span("   ? closes", dim)), lm.NewLine()}
	for _, k := range allKeys {
		lines = append(lines, lm.NewLine(span("  ", dim), bold(pad(k.key, 9), ink), span(k.what, muted)))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
	return len(lines)
}

// summaryLine is what needs the owner before any detail: how many units, how many on air, in
// trouble, dark, heard only and waiting - and what the filter hides, so nothing is hidden silently.
func (f *Fleet) summaryLine() lm.Line {
	var units, air, trouble, dark, heard, waiting int
	for _, it := range f.all {
		switch it.Kind {
		case ItemMember:
			units++
			switch {
			case it.Dark:
				dark++
			case len(it.Alerts) > 0:
				trouble++
			case !it.Controllable:
				heard++
			default:
				air++
			}
		case ItemCandidate:
			waiting++
		}
	}
	parts := []lm.Span{span("   ", dim), span(fmt.Sprintf("%d on your Edge", units), ink), span(fmt.Sprintf(" · %d on air", air), signal)}
	if trouble > 0 {
		parts = append(parts, bold(fmt.Sprintf(" · %d trouble", trouble), live))
	}
	if dark > 0 {
		parts = append(parts, span(fmt.Sprintf(" · %d dark", dark), ash))
	}
	if heard > 0 {
		parts = append(parts, span(fmt.Sprintf(" · %d heard only", heard), glow))
	}
	if waiting > 0 {
		parts = append(parts, span(fmt.Sprintf(" · %d new", waiting), glow)) // asking to be adopted, on the bench
	}
	return lm.NewLine(parts...)
}

// viewLine says how the rack is narrowed and ordered when it is not the default, so nothing is
// hidden or moved silently. Nil when it is the default view.
func (f *Fleet) viewLine() *lm.Line {
	var parts []lm.Span
	if f.query != "" {
		parts = append(parts, span(fmt.Sprintf("   /%s hides %d", f.query, hiddenBy(f.all, f.query, f.snap.At)), muted),
			span(" · / then esc shows all", dim))
	}
	if f.order != orderWorst {
		parts = append(parts, span("   "+orderWords[f.order], muted), span(" · s again for the next order", dim))
	}
	if len(parts) == 0 {
		return nil
	}
	l := lm.NewLine(parts...)
	return &l
}

func lineWidth(l lm.Line) int {
	n := 0
	for _, sp := range l.Spans {
		n += utf8.RuneCountInString(sp.Text)
	}
	return n
}

// rowFacts are the unit's instruments in a rack row: temperature, memory and what it serves. A dark
// unit's last values are not drawn here as if current - a rail says "no live reading", and its page
// shows the old values with their age.
func (it FleetItem) rowFacts(used, width int, now time.Time) []lm.Span {
	gap := 48 - used
	if gap < 2 {
		gap = 2
	}
	out := []lm.Span{span(strings.Repeat(" ", gap), dim)}
	facts := 12
	// The disk is the first column to go: what a unit serves is worth more of a narrow row
	// (fleet_window.feature §1 names temperature, memory and serving; the disk is on its page).
	withDisk := width-used-gap-17 >= 2+utf8.RuneCountInString(it.Serving)
	switch {
	case it.Dark:
		// No live readings to draw: the eye and DARK already say so, and the room goes to what it
		// served when last heard.
		facts = 0
	case it.Phone != nil && it.Phone.BatteryPct != nil:
		// A phone has no processor figures to give; its battery is the number that matters.
		out = append(out, span(pad(fmt.Sprintf("batt %d%%", *it.Phone.BatteryPct), 10), ink))
	case edge.Kind(it.NodeKind) == edge.Mobile || it.Phone != nil:
		// A phone with no battery reading yet: its battery column is drawn absent. It never gets the
		// temperature and memory columns, because iOS gives no such figures (fleet_window.feature).
		out = append(out, span(pad("batt ┈┈", 10), ash))
	case it.Health == nil || it.Health.Readings == nil:
		out = append(out, span("┈┈┈   ┈┈┈ ", ash))
		if withDisk {
			out = append(out, span(" ┈┈┈ ", ash))
			facts = 17
		}
	default:
		r := it.Health.Readings
		if r.CPUTempC != nil {
			c := ink
			w := fmt.Sprintf("%3.0f°", *r.CPUTempC)
			if len(alarm(it.Alerts, "cpu_temp")) > 0 {
				c, w = live, "▲"+strings.TrimSpace(w)
			}
			out = append(out, span(pad(w, 6), c))
		} else {
			out = append(out, span("┈┈┈   ", ash))
		}
		if l, ok := it.Health.MemoryLevel(); ok {
			out = append(out, span(pad(fmt.Sprintf("%.0f%%", 100*l), 5), ink))
		} else {
			out = append(out, span("┈┈┈  ", ash))
		}
		if l, ok := it.Health.DiskLevel(); withDisk && ok {
			c, w := ink, fmt.Sprintf("%.0f%%", 100*l)
			if len(alarm(it.Alerts, "disk")) > 0 {
				c, w = live, "▲"+w
			}
			out = append(out, span(pad(w, 5), c))
			facts = 17
		} else if withDisk {
			out = append(out, span("┈┈┈  ", ash))
			facts = 17
		}
		if r.PiPower != nil {
			if now, _ := edge.PowerWords(*r.PiPower); len(now) > 0 {
				out = append(out, bold("⚡", live))
			}
		}
	}
	if room := width - used - gap - facts - 2; it.Serving != "" && room >= 4 { // 2: the air before it
		if it.Dark && !it.LastSeen.IsZero() {
			// What it served when last heard, said as old - never drawn as serving now.
			out = append(out, span("  "+elide(it.Serving+" ("+edge.Age(now.Sub(it.LastSeen))+" old)", room), ash))
		} else {
			out = append(out, span("  "+elide(it.Serving, room), dial))
		}
	}
	return out
}

// drawReadout is the selected unit in full: a headline, one line in the operator's voice, its
// instruments (the only rows with an eyebrow), then its facts as plain lines. It returns how many
// rows it drew.
func (f *Fleet) drawReadout(fr *lm.Frame, area lm.Rect) int {
	if f.help {
		return f.drawKeys(fr, area)
	}
	it := f.selected()
	now := f.snap.At
	width := int(area.Width) - 4
	if width < 20 {
		width = 20
	}
	var lines []lm.Line
	row := func(k string, v ...lm.Span) {
		l := lm.NewLine(span("  ", dim), eyebrow(pad(k, 9), muted))
		l.Spans = append(l.Spans, v...)
		lines = append(lines, l)
	}
	prose := func(s string, c lm.Color) {
		for _, part := range wrapWords(s, width) {
			lines = append(lines, lm.NewLine(span("  ", dim), span(part, c)))
		}
	}
	head := func(lv float64, dark, asking bool, name, role string) {
		l := lm.NewLine(span(" ", dim))
		l.Spans = append(l.Spans, eye(lv, dark, asking, len(it.Alerts) > 0, f.frame, 99)...)
		l.Spans = append(l.Spans, span(" ", dim), bold(name, ink), span("   "+role, dim))
		lines = append(lines, lm.NewLine(), l)
	}
	air := func() { lines = append(lines, lm.NewLine()) }
	// software is the inventory line, and the BEHIND mark in the alarm colour when the unit runs an
	// older roger than the newest on the Edge (features/edge/inventory.feature).
	// tags are the owner's words: drawn where they exist, never as an empty row.
	tagRow := func() {
		if len(it.Tags) > 0 {
			row("tags", span(strings.Join(it.Tags, " · "), dial))
		}
		if it.Maintain {
			row("service", span("maintenance is on there · a member with a grant can start one of its agents", glow))
		}
		for k, line := range it.Drift {
			label := ""
			if k == 0 {
				label = "drift"
			}
			row(label, bold(line, live))
		}
	}
	software := func() {
		if it.Behind != "" {
			row("update", bold("behind "+it.Behind, live), span("  the newest roger on this Edge", muted))
		}
		label := "software"
		for _, line := range it.Software {
			// Wrapped at the separators, so a long OS name and a machine name each stay whole.
			for _, piece := range wrapDots(line, 46) {
				row(label, span(piece, ink))
				label = ""
			}
		}
	}
	switch it.Kind {
	case ItemHub:
		lv := carrierLevel(it.heard(now), false, now)
		root, _, _ := strings.Cut(f.snap.Self.Root, " · ")
		head(lv, false, false, it.label(), "this machine · "+strings.ToLower(root))
		prose("the transmitter of this Edge: it holds the root key and signs every member's certificate", ink)
		air()
		if area.Width < 56 { // narrow: the preference on its own row rather than cut
			row("carrier", append(meter(lv, true, -1, f.frame, true), span(" "+sUnit(lv), muted))...)
			row("turns", span("go "+preferWord(f.snap.Self.PreferValue)+" first", dial))
		} else {
			row("carrier", append(meter(lv, true, -1, f.frame, true), span(" "+sUnit(lv)+"   ", muted), span("turns go "+preferWord(f.snap.Self.PreferValue)+" first", dial))...)
		}
		tagRow()
	case ItemInstance, ItemAgent:
		role := "roger on this machine"
		if it.Kind == ItemAgent {
			role = "agent on this machine"
		}
		g, gc := it.glyph()
		lines = append(lines, lm.NewLine(), lm.NewLine(span(" ", dim), bold(g, gc), span("  ", dim), bold(it.label(), ink), span("   "+role, dim)))
		switch {
		case it.Dark:
			prose("offline: its process is not running. u relaunches it here, x forgets it", ink)
		case it.Kind == ItemAgent:
			prose("an agent: it can act on other nodes of the Edge under a grant, and serve what it has on air", ink)
		case len(it.Bands) > 0:
			prose("serves a model to the Edge", ink)
		default:
			prose("on the Edge, not an agent, not serving", ink)
		}
		if it.Job != "" {
			air()
			row("job", bold(it.Job, glow))
		}
		software()
	case ItemMember:
		lv := carrierLevel(it.LastSeen, it.Dark, now)
		head(lv, it.Dark, it.Asking, it.label(), kindLabel(it.NodeKind)+" · patched in")
		switch {
		case it.Asking && it.ClaimStanding:
			prose("back on this network and asking to re-join. its claim is granted: it is waiting for it to claim its certificate "+atAuthority(it.AuthPort)+" - nothing has arrived yet. a re-admits it again (a fresh grant, its old certificate revoked)", ink)
		case it.Asking:
			prose("back on this network and asking to re-join. a re-admits it: a fresh claim is granted and it fetches a new certificate", ink)
		case it.ClaimStanding:
			prose("adopted: waiting for it to claim its certificate "+atAuthority(it.AuthPort)+" - nothing has arrived yet", ink)
			for _, st := range adoptionStages(it, now) {
				for k, part := range wrapWords(st.words, width-6) {
					mark := "  "
					if k == 0 {
						mark = st.mark + " "
					}
					lines = append(lines, lm.NewLine(span("    ", dim), span(mark, st.color), span(part, st.color)))
				}
			}
		case it.Dark:
			prose("dark: the authority has not heard from it past the liveness window. it re-admits itself when it pings again", ink)
		default:
			prose("a member of this Edge, verified by the certificate this authority issued it", ink)
		}
		air()
		cs := append(meter(lv, true, -1, f.frame, !it.Dark), span("   ", dim))
		if it.Dark {
			cs = append(cs, span(it.Presence, ash))
		} else {
			cs = append(cs, span(sUnit(lv), muted), span("   heard "+edge.Age(now.Sub(it.LastSeen))+" ago", ink))
		}
		row("carrier", cs...)
		if !it.Dark && !it.Controllable {
			row("control", span("heard, with no face this machine can dial: actions that need it (serve model, maintenance, ask) cannot reach it", glow))
		}
		if it.Via != "" {
			row("reached", span("through "+it.Via, ink))
		}
		if it.Model != "" {
			row("model", span(it.Model, dial))
		}
		if pl := edge.PhoneLine(it.Phone); pl != "" {
			label := "phone"
			for _, piece := range wrapDots(pl, 46) {
				row(label, span(piece, ink))
				label = ""
			}
			row("", span(edge.Age(now.Sub(time.Unix(it.Phone.ReadAt, 0)))+" ago", muted))
		}
		if !it.LastRead.IsZero() {
			row("hears", span("hears the Edge · read the feed "+edge.Age(now.Sub(it.LastRead))+" ago", signal))
		}
		switch it.Codes {
		case "checks":
			row("joined", span("said it checks codes when it joined", ink))
		case "old":
			row("joined", span("did not say it checks codes: a roger or app from before root proofs. update it", glow))
		}
		tagRow()
		software()
	case ItemCandidate:
		head(0, false, true, it.label(), kindLabel(it.NodeKind)+" · on the bench")
		prose("seen on this network, not on your Edge. adopting it grants a claim; it fetches its certificate and patches in", ink)
		air()
		row("status", bold("not on your Edge", glow), span(" · press a to adopt it", muted))
	}
	// Instruments, where the unit reports them.
	if h := it.Health; h != nil && h.Readings != nil {
		r := h.Readings
		lineFor := func(metric string, scale float64) float64 {
			if th, ok := it.Lines[metric]; ok {
				return th / scale
			}
			if th, ok := thresholdIn(it.Alerts, metric); ok {
				return th / scale
			}
			return -1
		}
		if r.CPUTempC != nil {
			from := lineFor("cpu_temp", 110)
			row("temp", append(append(meterN(*r.CPUTempC/110, from, 0, false, meterCells), span(fmt.Sprintf(" %.1f°C", *r.CPUTempC), ink)), alarm(it.Alerts, "cpu_temp")...)...)
		}
		if l, ok := h.MemoryLevel(); ok {
			row("memory", append(meterN(l, lineFor("memory", 100), 0, false, meterCells), span(fmt.Sprintf(" %s of %s GB", fmtGB(r.RAMUsedGB), fmtGB(r.RAMTotalGB)), ink))...)
		}
		if r.PiPower != nil {
			pw := ink
			if now, _ := edge.PowerWords(*r.PiPower); len(now) > 0 {
				pw = live
			}
			row("power", span(edge.PowerLine(*r.PiPower), pw))
		}
		if l, ok := h.DiskLevel(); ok {
			from := lineFor("disk", 100)
			row("disk", append(append(meterN(l, from, 0, false, meterCells), span(fmt.Sprintf(" %.0f%%", 100*l), ink)), alarm(it.Alerts, "disk")...)...)
		}
	}
	if it.Kind == ItemHub || it.Kind == ItemMember {
		if c := it.crew(f.snap.Self.Op); len(c) > 0 {
			row("crew", c...)
		}
		if j := it.jobLine(); len(j) > 0 {
			row("job", j...)
		}
	}
	for _, h := range it.hourRows(width - 34) {
		row(h.name, h.spans...)
	}
	if len(it.Log) > 0 && (it.Kind == ItemHub || it.Kind == ItemMember) {
		w, c, _ := it.state(now)
		row("the log", append(logStrip(it.Log, width-22), span("  "+strings.ToLower(w)+" "+it.sinceWords(now), c))...)
	}
	if len(it.Bands) > 0 {
		row("on air", span(strings.Join(it.Bands, "  "), dial))
	}
	if len(it.Caps) > 0 {
		row("can do", chips(it.Caps)...)
	}
	for _, a := range it.Alerts {
		if strings.HasPrefix(a, "cpu_temp ") || strings.HasPrefix(a, "disk ") {
			continue
		}
		row("alarm", bold("▲ "+a, live))
	}
	if it.Kind == ItemHub {
		air()
		if d := f.snap.Self.Discovery; d != "" {
			first, _, _ := strings.Cut(d, " · ") // the one clause that matters here
			prose(first, muted)
		}
		if f.snap.Self.Join != "" {
			prose("another machine joins with  "+f.snap.Self.Join, muted)
		}
	}
	if f.rename {
		air()
		lines = append(lines, lm.NewLine(span("  ", dim), span("new name  ", paper), bold(string(f.input)+"▏", paper),
			span("  (was "+it.label()+") · enter to rename · esc to cancel", muted)))
	}
	if f.binding {
		air()
		lines = append(lines, lm.NewLine(span("  ", dim), span("bind  ", paper), bold(string(f.input)+"▏", paper)))
		prose("a model to use, then a job: <model> [serve|watch|relay] · enter binds · esc cancels", muted)
	}
	if f.last != "" {
		air()
		c := signal
		if f.lastErr {
			c = live
		}
		for i, part := range wrapWords(f.last, width) {
			k := "▸ "
			if i > 0 {
				k = "  "
			}
			lines = append(lines, lm.NewLine(span("  "+k, c), span(part, c)))
		}
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
	return len(lines)
}

// hourRow is one of a unit's last-hour lines, ready for a row.
type hourRow struct {
	name  string
	spans []lm.Span
}

// hourRows are THE LAST HOUR as lines, so a spike reads as a spike and a sustained climb as a
// climb; a gap where it was dark stays a gap (fleet_window.feature). w is the cells the line has.
func (it FleetItem) hourRows(w int) []hourRow {
	var out []hourRow
	for _, h := range []struct {
		name string
		vals []float64
		unit func(float64) string
	}{
		{"temp 1h", it.TempHour, func(v float64) string { return fmt.Sprintf("%.0f°", v) }},
		{"load 1h", it.LoadHour, func(v float64) string { return fmt.Sprintf("%.2f", v) }},
		{"mem 1h", it.MemHour, func(v float64) string { return fmt.Sprintf("%.0f%%", 100*v) }},
	} {
		if line, lo, hi, ok := hourLine(h.vals, w); ok {
			out = append(out, hourRow{h.name, []lm.Span{span(line, dial), span("  "+h.unit(lo)+" to "+h.unit(hi), muted)}})
		}
	}
	return out
}

// nodeOf is the unit's health as the page draws it, with the rules firing on it: the health read's
// own record when it has the unit (the same read the HEALTH screen drew), else the rack's.
func (f *Fleet) nodeOf(it FleetItem) (edge.NodeHealth, []string) {
	if f.health != nil {
		for _, n := range f.health.snap.Nodes {
			if n.ID == it.ID && it.ID != "" {
				return n, f.health.snap.Firing[n.ID]
			}
		}
	}
	now := f.snap.At
	var n edge.NodeHealth
	if it.Health != nil {
		n = *it.Health
	}
	n.ID, n.Name, n.Kind, n.Dark = it.ID, it.Name, it.NodeKind, it.Dark
	if n.LastHeard.IsZero() {
		n.LastHeard = it.heard(now)
	}
	if n.Dark && n.DarkFor == 0 && !n.LastHeard.IsZero() {
		n.DarkFor = now.Sub(n.LastHeard)
	}
	return n, it.Alerts
}

// drawHealthPage is the FLEET tab with the selected unit's health open - what the HEALTH tab was
// before it was folded in: every reading with its age and its meter, the session's history and
// the last hour, the alarms standing on it, then the rules in force, editable in place.
func (f *Fleet) drawHealthPage(fr *lm.Frame, area lm.Rect) {
	it := f.selected()
	now := f.snap.At
	lines := []lm.Line{lm.NewLine(), lm.NewLine(span("  ", dim), eyebrow(pad("health", 8), muted),
		span("↑↓ the next unit · esc back to the rack", dim)), lm.NewLine()}
	if it.Kind == ItemHub || it.Kind == ItemMember {
		n, firing := f.nodeOf(it)
		var rules []RuleLine
		var hs *history
		if f.health != nil {
			rules, hs = f.health.snap.Rules, f.health.hist[it.ID]
		}
		lines = append(lines, instrumentStack(n, it.label(), firing, rules, it.Lines, hs, now, int(area.Width), f.frame)...)
		for _, h := range it.hourRows(int(area.Width) - 40) {
			l := lm.NewLine(span("  ", dim), eyebrow(pad(h.name, 8), muted))
			l.Spans = append(l.Spans, h.spans...)
			lines = append(lines, l)
		}
	} else {
		lines = append(lines, lm.NewLine(span("  nothing on this Edge reports its health yet", muted)))
	}
	if f.health != nil {
		lines = append(lines, lm.NewLine())
		lines = append(lines, f.health.rulesPane()...)
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// wrapWords breaks s on spaces into lines no wider than w (a single word wider than w stands alone).
func wrapWords(s string, w int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" || len(out) == 0 {
		out = append(out, line)
	}
	return out
}

func pad(s string, n int) string {
	if len([]rune(s)) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len([]rune(s)))
}

type action struct{ key, label string }

// actions is what the selected unit offers, in order.
func (f *Fleet) actions() []action {
	it := f.selected()
	switch f.tab {
	case tabGrants:
		out := []action{{"↑↓", "select"}}
		if f.Actions.IssueGrant != nil {
			out = append(out, action{"i", "issue"})
		}
		if f.Actions.RevokeGrant != nil {
			out = append(out, action{"x", "revoke"})
		}
		return append(out, action{"1", "fleet"}, action{"r", "re-read"}, action{"q", "quit"})
	case tabSessions, tabEvents:
		return []action{{"1", "fleet"}, {"r", "re-read"}, {"q", "quit"}}
	case tabTrouble:
		out := []action{{"↑↓", "select"}}
		if f.Actions.Roger != nil {
			out = append(out, action{"k", "roger"})
		}
		if f.Actions.StandBy != nil {
			out = append(out, action{"s", "stand by"})
		}
		return append(out, action{"1", "fleet"}, action{"r", "re-read"}, action{"q", "quit"})
	case tabAndon:
		return []action{{"↑↓", "select"}, {"1", "fleet"}, {"3", "trouble"}, {"r", "re-read"}, {"q", "quit"}}
	}
	if f.page {
		out := []action{{"↑↓", "unit"}}
		if f.health != nil {
			out = append(out, f.health.ruleActions()...)
		}
		return append(out, action{"r", "re-read"}, action{"esc", "rack"}, action{"q", "quit"})
	}
	out := []action{{"↑↓", "select"}}
	switch it.Kind {
	case ItemAgent, ItemInstance:
		if it.Dark && f.Actions.ResumeAgent != nil {
			out = append(out, action{"u", "resume"})
		}
		if it.Dark && f.Actions.RemoveAgent != nil {
			out = append(out, action{"x", "remove"})
		}
		if !it.Dark && f.Actions.BindJob != nil {
			out = append(out, action{"b", "bind"})
		}
		if !it.Dark && it.Job != "" && f.Actions.ClearJob != nil {
			out = append(out, action{"c", "clear"})
		}
	case ItemCandidate:
		if f.Actions.Adopt != nil {
			out = append(out, action{"a", "adopt"})
		}
	case ItemMember:
		out = append(out, action{"⏎", "health"})
		if it.Asking && f.Actions.Adopt != nil {
			out = append(out, action{"a", "re-admit"})
		}
		// Shown either way, saying why when it cannot work now: nothing is hidden only because it
		// cannot work right now, so the owner learns what exists (fleet_window.feature §6).
		if f.Actions.AskNow != nil {
			switch {
			case it.Dark:
				out = append(out, action{"h", "hear now (dark)"})
			case !it.Controllable:
				out = append(out, action{"h", "hear now (no face to dial)"})
			default:
				out = append(out, action{"h", "hear now"})
			}
		}
		if f.Actions.Rename != nil {
			out = append(out, action{"n", "rename"})
		}
		if f.Actions.Forget != nil {
			out = append(out, action{"f", "forget"})
		}
	case ItemHub:
		out = append(out, action{"⏎", "health"})
		if f.Actions.SetPrefer != nil {
			out = append(out, action{"p", "prefer"})
		}
		if f.Actions.SetAgent != nil {
			if f.snap.Self.AgentActive {
				out = append(out, action{"g", "agent off"})
			} else {
				out = append(out, action{"g", "agent on"})
			}
		}
	}
	out = append(out, action{"/", "filter"}, action{"s", "sort"}, action{"?", "keys"})
	out = append(out, action{"2", "andon"}, action{"3", "trouble"}, action{"4", "grants"}, action{"5", "sessions"})
	return append(out, action{"r", "re-read"}, action{"q", "quit"})
}

// drawGrants is the GRANTS tab: every authority-signed grant, live, expired or revoked (a revoked
// one is drawn, never hidden), the selected one on a red rail.
func (f *Fleet) drawGrants(fr *lm.Frame, area lm.Rect) {
	if f.gsel >= len(f.snap.Grants) {
		f.gsel = len(f.snap.Grants) - 1
	}
	if f.gsel < 0 {
		f.gsel = 0
	}
	lines := []lm.Line{lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("grants", muted),
		span("  what one node may ask another to do, signed by the authority", dim)), lm.NewLine()}
	if f.issuing {
		lines = append(lines, lm.NewLine(span("   ", dim), span("issue  ", paper), bold(string(f.input)+"▏", paper)),
			lm.NewLine(span("   caller (self, or a unit) · capability · callee · for (7d; 30d when left out) · enter issues · esc cancels", muted)),
			lm.NewLine())
	}
	if f.last != "" && f.tab == tabGrants {
		c := signal
		if f.lastErr {
			c = live
		}
		lines = append(lines, lm.NewLine(span("   ▸ ", c), span(f.last, c)), lm.NewLine())
	}
	if len(f.snap.Grants) == 0 {
		lines = append(lines, lm.NewLine(span("   no grants issued yet   ", muted), span("roger edge grant <caller> <capability> <callee> --for 7d", ink)))
	}
	for i, g := range f.snap.Grants {
		rail := span("▌", dim)
		nc := ink
		if i == f.gsel {
			rail, nc = bold("▌", live), paper
		}
		dot, dc := "●", signal
		switch g.State {
		case "REVOKED":
			dot, dc = "○", live
		case "expired":
			dot, dc = "○", dim
		}
		lines = append(lines, lm.NewLine(span(" ", dim), rail, span(" ", dim), bold(dot+" ", dc), span(pad(g.ID, 16), nc),
			span(pad(g.Caller, 18), ink), span("may ", muted), bold(pad(g.Caps, 10), capColor(g.Caps)), span("on ", muted), span(pad(g.Callee, 18), ink),
			span(pad("until "+g.Until, 18), muted), span(g.State, dc)))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// drawSessions is the SESSIONS tab: the traffic this Edge carried, as its own receipts describe
// it - a refusal is drawn as a refusal.
func (f *Fleet) drawSessions(fr *lm.Frame, area lm.Rect) {
	lines := []lm.Line{lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("sessions", muted),
		span("  the traffic this Edge really carried, from its own receipts", dim)), lm.NewLine()}
	if len(f.snap.Sessions) == 0 {
		lines = append(lines, lm.NewLine(span("   nothing carried yet", muted)))
	}
	for _, x := range f.snap.Sessions {
		oc := signal
		if strings.Contains(x.Outcome, "REFUSED") || strings.Contains(x.Outcome, "failed") {
			oc = live
		}
		count := ""
		if x.Count > 1 {
			count = fmt.Sprintf(" ×%d", x.Count)
		}
		l := lm.NewLine(span("   ", dim), span(pad(x.Who, 22), ink), span(pad(x.Band, 18), dial), span(pad(x.Route, 8), muted))
		if x.Station != "" {
			l.Spans = append(l.Spans, span("via "+pad(x.Station, 14), muted))
		} else {
			l.Spans = append(l.Spans, span(pad("", 18), dim))
		}
		l.Spans = append(l.Spans, span(x.Outcome, oc), span(count, muted), span("   "+x.Age+" ago", dim))
		lines = append(lines, l)
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// logStrip draws a unit's last hour, newest at the right, at most w cells: ▁ on air, ▒ dark,
// ▲ trouble (red), · not judged.
func logStrip(log []edge.UnitState, w int) []lm.Span {
	if w < 12 {
		w = 12
	}
	if len(log) > w {
		log = log[len(log)-w:]
	}
	var out []lm.Span
	var b strings.Builder
	cur, started := dim, false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, span(b.String(), cur))
			b.Reset()
		}
	}
	for _, st := range log {
		r, c := '·', dim
		switch st {
		case edge.StateAir:
			r, c = '▁', signal
		case edge.StateDark:
			r, c = '▒', ash
		case edge.StateTrouble:
			r, c = '▁', live // red air: the hour reads red without thirty-four alarms
		}
		if !started || c != cur {
			flush()
			cur, started = c, true
		}
		b.WriteRune(r)
	}
	flush()
	return out
}

// sortedAlarms is the standing calls the ISA-18.2 way: priority first, then the oldest first.
func (f *Fleet) sortedAlarms() []AlarmLine {
	out := append([]AlarmLine(nil), f.snap.Alarms...)
	sort.SliceStable(out, func(i, j int) bool { // the same order edge.SortCalls gives every surface
		if out[i].Shelved != out[j].Shelved {
			return !out[i].Shelved
		}
		pi, pj := edge.PriorityRank(out[i].Priority()), edge.PriorityRank(out[j].Priority())
		if pi != pj {
			return pi < pj
		}
		return out[i].Since > out[j].Since
	})
	return out
}

// drawTrouble is the TROUBLE tab: the standing calls, each with why it matters and what to do;
// an acknowledged call keeps its row and loses its color.
func (f *Fleet) drawTrouble(fr *lm.Frame, area lm.Rect) {
	calls := f.sortedAlarms()
	if f.asel >= len(calls) {
		f.asel = len(calls) - 1
	}
	if f.asel < 0 {
		f.asel = 0
	}
	hi := 0
	for _, c := range calls {
		if c.Priority() == "HI" && !c.Acked {
			hi++
		}
	}
	head := lm.NewLine(span("   ", dim), eyebrow("trouble", muted), span(fmt.Sprintf("  %d standing", len(calls)), dim))
	if hi > 0 {
		head.Spans = append(head.Spans, span(" · ", dim), bold(fmt.Sprintf("%d high, not yet acknowledged", hi), live))
	}
	switch {
	case f.snap.Rate >= 10:
		head.Spans = append(head.Spans, span(" · ", dim), bold(fmt.Sprintf("PILE-UP · %d in 10 min", f.snap.Rate), live))
	case f.snap.Rate > 0:
		head.Spans = append(head.Spans, span(fmt.Sprintf(" · %d in 10 min", f.snap.Rate), dim))
	}
	lines := []lm.Line{lm.NewLine(), head, lm.NewLine()}
	if len(calls) == 0 {
		lines = append(lines, lm.NewLine(span("   ", dim), bold("ALL CLEAR", signal), span("  nothing is over its line", muted)))
	} else {
		lines = append(lines, lm.NewLine(span("     ", dim), eyebrow(pad("pri", 7), dim), eyebrow(pad("age", 8), dim), eyebrow(pad("unit", 20), dim),
			eyebrow(pad("call", 20), dim), eyebrow(pad("now", 10), dim), eyebrow("acknowledged", dim)))
	}
	for i, c := range calls {
		rail := span("▌", dim)
		nc := ink
		if i == f.asel {
			rail, nc = bold("▌", live), paper
		}
		mark, mc := "▲ "+c.Priority(), live
		switch {
		case c.Acked || c.Shelved:
			mc = muted
		case c.Priority() == "MED":
			mc = glow
		case c.Priority() == "LOW":
			mc = muted
		}
		ack := span("-", dim)
		switch {
		case c.Shelved:
			by := ""
			if c.StandByBy != "" {
				by = " · " + edge.Handle(c.StandByBy)
			}
			ack = span("STAND BY until "+c.StandByUntil.Local().Format("15:04")+by, muted)
		case c.Acked:
			ack = span("ROGER "+edge.Handle(c.AckBy), signal)
		}
		if c.Stale() {
			ack.Text += "  STALE: standing over a day - review the rule"
		}
		lines = append(lines, lm.NewLine(span(" ", dim), rail, span(" ", dim), bold(pad(mark, 7), mc), span(pad(edge.Age(c.Since), 8), muted),
			span(pad(elide(c.Node, 19), 20), nc), span(pad(c.When, 20), nc), span(pad(c.Value, 10), ink), ack))
		if i == f.asel {
			why, do := edge.WhyAndDo(edge.Metric(c.Metric))
			lines = append(lines, lm.NewLine(span("     ", dim), eyebrow(pad("why it matters", 16), muted), span(why, ink)))
			lines = append(lines, lm.NewLine(span("     ", dim), eyebrow(pad("do", 16), muted), span(do, ink)), lm.NewLine())
		}
	}
	if so := f.signOffSpans(); so != nil {
		lines = append(lines, lm.NewLine(), lm.NewLine(so...))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// airtime is the share of the last hour a unit was on air, over the minutes that were judged.
func airtime(log []edge.UnitState) (int, bool) {
	air, known := 0, 0
	for _, st := range log {
		if st == edge.StateUnknown {
			continue
		}
		known++
		if st == edge.StateAir || st == edge.StateTrouble { // airtime is availability: a troubled unit is still on air
			air++
		}
	}
	if known == 0 {
		return 0, false
	}
	return int(float64(air)/float64(known)*100 + 0.5), true
}

// drawAndon is the ANDON tab: one tile per unit - one word, one color, one number - the rack in
// two seconds; the counter line at the foot.
func (f *Fleet) drawAndon(fr *lm.Frame, area lm.Rect) {
	now := f.snap.At
	var units []FleetItem
	var unitIdx []int
	for i, it := range f.snap.Items {
		if it.Kind == ItemHub || it.Kind == ItemMember {
			units = append(units, it)
			unitIdx = append(unitIdx, i)
		}
	}
	const tileW, tileH = 34, 6
	perRow := int(area.Width-2) / (tileW + 1)
	if perRow < 1 {
		perRow = 1
	}
	on, trouble, dark := 0, 0, 0
	var lines []lm.Line
	lines = append(lines, lm.NewLine())
	for start := 0; start < len(units); start += perRow {
		end := start + perRow
		if end > len(units) {
			end = len(units)
		}
		tile := make([][]lm.Span, tileH)
		for ti := start; ti < end; ti++ {
			it := units[ti]
			word, c, _ := it.state(now)
			switch word {
			case "ON AIR":
				on++
			case "TROUBLE":
				trouble++
			default:
				dark++
			}
			railC := dim
			if unitIdx[ti] == f.sel {
				railC = live
			}
			pct, ok := airtime(it.Log)
			pctS := "-"
			if ok {
				pctS = fmt.Sprintf("%d%%", pct)
			}
			row0 := append([]lm.Span{span(" ", dim), bold("▌", railC), span(" ", dim)}, eye(carrierLevel(it.heard(now), it.Dark, now), it.Dark, it.Asking, len(it.Alerts) > 0, f.frame, ti)...)
			nameC := paper
			if word == "DARK" {
				nameC = ash
			}
			row0 = append(row0, span(" ", dim), bold(pad(elide(it.label(), 26), 27), nameC))
			row1 := []lm.Span{span(" ", dim), bold("▌", railC), span("   ", dim), bold(pad(word, 10), c), span(pad(it.sinceWords(now), 19), c)}
			row2 := []lm.Span{span(" ", dim), bold("▌", railC), span("   ", dim), eyebrow("airtime ", muted), bold(pad(pctS, 5), ink)}
			row2 = append(row2, logStrip(it.Log, 16)...)
			row2 = append(row2, span(" ", dim))
			row3 := []lm.Span{span(" ", dim), bold("▌", railC), span("   ", dim)}
			row4 := []lm.Span{span(" ", dim), bold("▌", railC), span("   ", dim)}
			switch {
			case len(it.Alerts) > 0:
				row3 = append(row3, span(pad(elide("▲ "+it.Alerts[0], 28), 29), live))
				if len(it.Jobs) > 0 {
					row4 = append(row4, eyebrow("job ", muted), span(pad(elide(it.Jobs[0], 24), 25), ink))
				} else {
					row4 = append(row4, span(pad(elide(tileWhat(it), 28), 29), muted))
				}
			case len(it.Jobs) > 0:
				row3 = append(row3, eyebrow("job ", muted), span(pad(elide(it.Jobs[0], 24), 25), ink))
				row4 = append(row4, tileCrew(it)...)
			default:
				row3 = append(row3, span(pad(elide(tileWhat(it), 28), 29), muted))
				row4 = append(row4, tileCrew(it)...)
			}
			for r, sp := range [][]lm.Span{row0, row1, row2, row3, row4, {span(pad("", tileW), dim)}} {
				// Every row of a tile is exactly tileW wide, whatever it holds: a short row (an empty
				// airtime strip on a unit with no log yet) pulled every later tile out of its column.
				if w := lineWidth(lm.NewLine(sp...)); w < tileW {
					sp = append(sp, span(strings.Repeat(" ", tileW-w), dim))
				}
				tile[r] = append(tile[r], sp...)
				tile[r] = append(tile[r], span(" ", dim))
			}
		}
		for r := 0; r < tileH; r++ {
			lines = append(lines, lm.NewLine(tile[r]...))
		}
	}
	count := func(n int, word string, c lm.Color) lm.Span {
		if n == 0 {
			return span(fmt.Sprintf("%d %s", n, word), dim) // a zero is quiet, never a bold green nothing
		}
		return bold(fmt.Sprintf("%d %s", n, word), c)
	}
	foot := lm.NewLine(span("   ", dim), eyebrow("the rack", muted), span(fmt.Sprintf("  %d units", len(units)), dim))
	if f.snap.Self.Op != "" {
		foot.Spans = append(foot.Spans, span(" · ", dim), eyebrow("op ", muted), span(f.snap.Self.Op, ink))
	}
	foot.Spans = append(foot.Spans, span("   ", dim), count(on, "on air", signal), span("   ", dim), count(trouble, "trouble", live),
		span("   ", dim), count(dark, "dark", ash))
	lines = append(lines, lm.NewLine(), foot) // the foot follows the tiles; the air below is honest
	if so := f.signOffSpans(); so != nil {
		lines = append(lines, lm.NewLine(so...))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// atAuthority words where a device claims: the port when one is known, else the plain fact that
// nothing is listening until the TUI or the console is open.
func atAuthority(port string) string {
	if port == "" {
		return "at this authority, which is not listening right now (open the TUI or the console)"
	}
	return "at this authority's port " + port
}

// tileWhat is the tile's third line when nothing fires: what the unit has on air, or what it is.
func tileWhat(it FleetItem) string {
	switch {
	case len(it.Bands) > 0:
		return "on air " + fewBands(it.Bands)
	case it.Kind == ItemHub:
		return "the authority"
	}
	return kindLabel(it.NodeKind)
}

// tileCrew is the tile's last line: what the unit served (the operator is named once, in the foot).
func tileCrew(it FleetItem) []lm.Span {
	s := ""
	if it.Served > 0 {
		s = fmt.Sprintf("%d session%s served", it.Served, plural(it.Served))
	}
	return []lm.Span{span(pad(s, 29), muted)}
}

// clampIndex keeps a cursor inside [0, n).
func clampIndex(i, n int) int {
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

// wrapDots breaks a " · "-separated line into pieces no longer than width, cutting only at the
// separators; one piece longer than width stands alone rather than being cut mid-word.
func wrapDots(line string, width int) []string {
	parts := strings.Split(line, " · ")
	var out []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case len([]rune(cur))+3+len([]rune(p)) <= width:
			cur += " · " + p
		default:
			out = append(out, cur)
			cur = p
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// EventLine is one line of the EVENTS tab: already worded, so the screen only draws it.
type EventLine struct {
	Age    string
	Node   string
	What   string
	Detail string
	Alarm  bool // a crossing in the alert log, drawn in the alarm colour; a node event is quiet
}

// drawEvents is the EVENTS tab: what happened on this Edge, newest first. It is read, not worked.
func (f *Fleet) drawEvents(fr *lm.Frame, area lm.Rect) {
	lines := []lm.Line{lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("events", muted),
		span("  what happened on this Edge, newest first", dim)), lm.NewLine()}
	if len(f.snap.Events) == 0 {
		lines = append(lines, lm.NewLine(span("   nothing has happened on this Edge yet", muted)))
	}
	for _, e := range f.snap.Events {
		mark, wc := span("  ", dim), ink
		if e.Alarm {
			mark, wc = bold(" \u25b2", live), live
		}
		lines = append(lines, lm.NewLine(span("  ", dim), mark, span(" ", dim),
			span(pad(e.Age, 8), muted), span(pad(e.Node, 18), ink), span(pad(e.What, 22), wc), span(e.Detail, dim)))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}
