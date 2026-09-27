package fleetview

// The Edge FLEET screen: this machine as the hub, the rogers running on it, the members it has
// adopted and the devices asking to be - drawn as a map with a readout for whatever is selected,
// and the actions that manage them in place (adopt, rename, forget, where a turn goes first).
// Actions are functions handed in by the caller; the screen never reaches into the Edge itself.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	lm "github.com/thebanri/limoni"

	"rogerai.fm/roger/v6/internal/edge"
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

// FleetItem is one line of the map with everything its readout needs.
type FleetItem struct {
	Kind     ItemKind
	ID       string // node id (hub, member, candidate)
	Name     string
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
	AuthPort      string
	// THE LOG: the unit's last hour, one cell a minute (oldest first), and how long it has been
	// in its current state.
	Log         []edge.UnitState
	Since       time.Duration
	SinceCapped bool // the run reaches the oldest sample: it began before the log did
	// The OP and JOB line: the jobs the rogers on this unit hold, and the sessions it served.
	Jobs   []string
	Served int
	// AlarmSince is the oldest standing call on this unit: TROUBLE has lasted at least that long.
	AlarmSince time.Duration
	// Lines are the rule thresholds in force for this unit by metric, so a meter draws its line
	// whether or not the unit is over it.
	Lines map[string]float64
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
		return "DARK", ash, since
	case edge.StateTrouble:
		return "TROUBLE", live, since
	}
	return "ON AIR", signal, since
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
	Rate     int // calls fired in the last ten minutes; ten is a PILE-UP
	SignOff  SignOffLine
	At       time.Time
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
}

// Fleet is the screen.
type Fleet struct {
	Source  FleetSource
	Actions FleetActions
	Every   time.Duration
	health  *Health // the HEALTH tab, when given

	snap     FleetSnapshot
	err      error
	readAt   time.Time
	sel      int
	gsel     int // the selected grant on the GRANTS tab
	asel     int // the selected call on the TROUBLE tab
	tab      int // see tabs(): fleet, andon, trouble, grants, sessions, health
	frame    int // advances each frame; the pulses breathe on it
	confirm  bool
	askID    string // the unit the forget question is about, fixed when it was asked
	revoke   bool   // the revoke question is up
	revokeID string
	binding  bool // the bind prompt is up (input holds the text)
	issuing  bool // the issue-grant prompt is up
	rename   bool
	input    []rune
	toast    string
	toastAt  time.Time
	toastErr bool
	last     string // the last action's outcome, kept in the readout until the next
	lastErr  bool
}

// NewFleet makes the screen over src, with health as its second tab (nil = no tab).
func NewFleet(src FleetSource, health *Health, acts FleetActions) *Fleet {
	f := &Fleet{Source: src, Actions: acts, Every: 2 * time.Second, health: health}
	if health != nil {
		health.BackHint = "1 fleet"
		health.Tabs, health.Tab = f.tabs(), tabHealth
	}
	return f
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
	if f.tab == tabHealth && f.health != nil {
		return f.health.Draw(fr, nil)
	}
	f.refresh()
	f.clamp()

	f.drawFrame(fr)
	return true
}

// The tabs, by index: ANDON is the whole rack in two seconds (level 1), FLEET the rack (level 2),
// TROUBLE the standing calls, GRANTS and SESSIONS the ledgers, HEALTH the instruments.
const (
	tabFleet = iota
	tabAndon
	tabTrouble
	tabGrants
	tabSessions
	tabHealth
)

// tabs is the tab strip; HEALTH only when a health screen was given.
func (f *Fleet) tabs() []string {
	t := []string{"fleet", "andon", "trouble", "grants", "sessions"}
	if f.health != nil {
		t = append(t, "health")
	}
	return t
}

// onKey handles one key; false ends the screen.
func (f *Fleet) onKey(ev *lm.Event) bool {
	k := ev.Key
	switch {
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
				f.report(f.Actions.Forget(it.ID), "forgot "+it.Name)
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
	// Tabs, from anywhere.
	switch {
	case k.Ch == 'q' || k.Ch == 'Q' || k.Type == lm.KeyEsc:
		return false
	case k.Ch >= '1' && k.Ch <= '6' && int(k.Ch-'1') < len(f.tabs()):
		f.tab = int(k.Ch - '1')
		return true
	case k.Type == lm.KeyTab:
		f.tab = (f.tab + 1) % len(f.tabs())
		return true
	}
	switch f.tab {
	case tabHealth:
		f.health.Draw(nil, ev) // keys only; q was handled above, so the tab never ends the screen
		return true
	case tabTrouble:
		calls := f.sortedAlarms()
		switch {
		case k.Type == lm.KeyDown || k.Ch == 'j':
			f.asel++
		case k.Type == lm.KeyUp:
			f.asel--
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
			f.gsel++
		case k.Type == lm.KeyUp || k.Ch == 'k':
			f.gsel--
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
	case tabSessions:
		if k.Ch == 'r' {
			f.readAt = time.Time{}
		}
		return true
	}
	switch {
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
			f.report(f.Actions.ClearJob(it.Name), "cleared the job on "+it.Name)
		}
	case k.Ch == 'u':
		if it := f.selected(); it.Kind == ItemAgent && it.Dark && f.Actions.ResumeAgent != nil {
			f.report(f.Actions.ResumeAgent(it.Name), "relaunching "+it.Name)
		}
	case k.Ch == 'x':
		if it := f.selected(); it.Kind == ItemAgent && it.Dark && f.Actions.RemoveAgent != nil {
			f.report(f.Actions.RemoveAgent(it.Name), "forgot agent "+it.Name)
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
		f.snap = snap
		if first {
			f.signOn()
		}
	}
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
	brandBand(fr, rows[0], f.tabs(), f.tab, right, alerts, hot, f.frame)
	switch {
	case f.err == nil && f.tab == tabGrants:
		f.drawGrants(fr, rows[1])
		actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
		if f.revoke && f.revokeID != "" {
			confirmBox(fr, "revoke "+f.revokeID+"?", "every node stops honoring it at once; a revocation is final", "y revoke", "n keep it")
		}
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
		confirmBox(fr, "forget "+it.Name+"?", "it leaves this Edge and its certificate is revoked", "y forget", "n keep it")
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
	seenMember := false
	for i, it := range f.snap.Items {
		switch it.Kind {
		case ItemHub:
			lv := carrierLevel(it.heard(now), false, now)
			top = append(top, lm.NewLine())
			l := lm.NewLine(span(" ", dim), rail(i, dial), span(" ", dim))
			l.Spans = append(l.Spans, eye(lv, false, false, len(it.Alerts) > 0, f.frame, i)...)
			l.Spans = append(l.Spans, span(" ", dim), bold(it.Name, paper), span("   this machine", muted))
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
				span(pad(elide(it.Name, nameW-2), nameW-2), nameC(i, it.Dark)), span(elide(it.rogerWords(), wordsW), muted)))
		case ItemMember:
			if !seenMember {
				seenMember = true
				top = append(top, lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("on the rack", muted),
					span(fmt.Sprintf("  %d patched in", members), dim)), lm.NewLine())
			}
			lv := carrierLevel(it.LastSeen, it.Dark, now)
			l := lm.NewLine(span(" ", dim), rail(i, dial), span(" ", dim))
			l.Spans = append(l.Spans, eye(lv, it.Dark, false, len(it.Alerts) > 0, f.frame, i)...)
			l.Spans = append(l.Spans, span(" ", dim), span(pad(elide(it.Name, nameW), nameW), nameC(i, it.Dark)))
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
			l.Spans = append(l.Spans, span(" ", dim), span(pad(elide(it.Name, nameW), nameW), nameC(i, false)))
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

// drawReadout is the selected unit in full: a headline, one line in the operator's voice, its
// instruments (the only rows with an eyebrow), then its facts as plain lines. It returns how many
// rows it drew.
func (f *Fleet) drawReadout(fr *lm.Frame, area lm.Rect) int {
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
	switch it.Kind {
	case ItemHub:
		lv := carrierLevel(it.heard(now), false, now)
		root, _, _ := strings.Cut(f.snap.Self.Root, " · ")
		head(lv, false, false, it.Name, "this machine · "+strings.ToLower(root))
		prose("the transmitter of this Edge: it holds the root key and signs every member's certificate", ink)
		air()
		if area.Width < 56 { // narrow: the preference on its own row rather than cut
			row("carrier", append(meter(lv, true, -1, f.frame, true), span(" "+sUnit(lv), muted))...)
			row("turns", span("go "+preferWord(f.snap.Self.PreferValue)+" first", dial))
		} else {
			row("carrier", append(meter(lv, true, -1, f.frame, true), span(" "+sUnit(lv)+"   ", muted), span("turns go "+preferWord(f.snap.Self.PreferValue)+" first", dial))...)
		}
	case ItemInstance, ItemAgent:
		role := "roger on this machine"
		if it.Kind == ItemAgent {
			role = "agent on this machine"
		}
		g, gc := it.glyph()
		lines = append(lines, lm.NewLine(), lm.NewLine(span(" ", dim), bold(g, gc), span("  ", dim), bold(it.Name, ink), span("   "+role, dim)))
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
	case ItemMember:
		lv := carrierLevel(it.LastSeen, it.Dark, now)
		head(lv, it.Dark, it.Asking, it.Name, kindLabel(it.NodeKind)+" · patched in")
		switch {
		case it.Asking && it.ClaimStanding:
			prose("back on this network and asking to re-join. its claim is granted: it is waiting for it to claim its certificate "+atAuthority(it.AuthPort)+" - nothing has arrived yet. a re-admits it again (a fresh grant, its old certificate revoked)", ink)
		case it.Asking:
			prose("back on this network and asking to re-join. a re-admits it: a fresh claim is granted and it fetches a new certificate", ink)
		case it.ClaimStanding:
			prose("adopted: waiting for it to claim its certificate "+atAuthority(it.AuthPort)+" - nothing has arrived yet", ink)
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
		if it.Via != "" {
			row("reached", span("through "+it.Via, ink))
		}
	case ItemCandidate:
		head(0, false, true, it.Name, kindLabel(it.NodeKind)+" · on the bench")
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
			span("  (was "+it.Name+") · enter to rename · esc to cancel", muted)))
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
	case tabSessions:
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
		if it.Asking && f.Actions.Adopt != nil {
			out = append(out, action{"a", "re-admit"})
		}
		if f.Actions.Rename != nil {
			out = append(out, action{"n", "rename"})
		}
		if f.Actions.Forget != nil {
			out = append(out, action{"f", "forget"})
		}
	case ItemHub:
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
	out = append(out, action{"2", "andon"}, action{"3", "trouble"}, action{"4", "grants"}, action{"5", "sessions"})
	if f.health != nil {
		out = append(out, action{"6", "health"})
	}
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
			ack = span("STAND BY until "+c.StandByUntil.Local().Format("15:04"), muted)
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
			row0 = append(row0, span(" ", dim), bold(pad(elide(it.Name, 26), 27), nameC))
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
