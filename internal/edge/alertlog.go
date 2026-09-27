package edge

// The ALERT LOG (health.feature §5): rules, what is firing, and the history, on this machine's
// disk. With no sink configured this is where alerts live - visible on the fleet surfaces, sent
// nowhere. Files are written whole through a temp+rename so a crash never leaves a half rule set.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AlertLog is the alert store rooted at Dir.
type AlertLog struct{ Dir string }

// alertRulesFile is what the owner set: their own rules, and which defaults they turned off.
type alertRulesFile struct {
	Rules []Rule   `json:"rules"`
	Off   []string `json:"off,omitempty"`
}

func (l AlertLog) path(name string) string { return filepath.Join(l.Dir, name) }

func (l AlertLog) readJSON(name string, v any) error {
	b, err := os.ReadFile(l.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(b, v)
}

func (l AlertLog) writeJSON(name string, v any) error {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path(name + ".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path(name))
}

func (l AlertLog) rulesFile() (alertRulesFile, error) {
	var f alertRulesFile
	err := l.readJSON("rules.json", &f)
	return f, err
}

// Rules is the rule set in force: the defaults (with the owner's off-switches applied) and then
// the owner's own rules.
func (l AlertLog) Rules() (RuleSet, error) {
	f, err := l.rulesFile()
	if err != nil {
		return nil, err
	}
	out := RuleSet(DefaultRules())
	for _, id := range f.Off {
		out = out.Off(id)
	}
	return append(out, f.Rules...), nil
}

// SetRule stores an owner rule, replacing one with the same id.
func (l AlertLog) SetRule(r Rule) error {
	if err := ValidateRule(r); err != nil {
		return err
	}
	for _, d := range DefaultRules() {
		if d.ID == r.ID {
			return fmt.Errorf("%q is a default rule: turn it off, or add your own under another id", r.ID)
		}
	}
	f, err := l.rulesFile()
	if err != nil {
		return err
	}
	r.Default, r.Off = false, false
	kept := f.Rules[:0]
	for _, x := range f.Rules {
		if x.ID != r.ID {
			kept = append(kept, x)
		}
	}
	f.Rules = append(kept, r)
	return l.writeJSON("rules.json", f)
}

// SetOff turns a rule off or back on. Defaults are only ever turned off, never removed.
func (l AlertLog) SetOff(id string, off bool) error {
	f, err := l.rulesFile()
	if err != nil {
		return err
	}
	found := false
	for i := range f.Rules {
		if f.Rules[i].ID == id {
			f.Rules[i].Off, found = off, true
		}
	}
	if !found {
		isDefault := false
		for _, d := range DefaultRules() {
			isDefault = isDefault || d.ID == id
		}
		if !isDefault {
			return fmt.Errorf("no rule %q", id)
		}
		kept := f.Off[:0]
		for _, x := range f.Off {
			if x != id {
				kept = append(kept, x)
			}
		}
		f.Off = kept
		if off {
			f.Off = append(f.Off, id)
		}
	}
	return l.writeJSON("rules.json", f)
}

// Remove deletes an owner rule. A default cannot be removed - SetOff it.
func (l AlertLog) Remove(id string) error {
	for _, d := range DefaultRules() {
		if d.ID == id {
			return fmt.Errorf("%q is a default rule: `alert off %s` turns it off", id, id)
		}
	}
	f, err := l.rulesFile()
	if err != nil {
		return err
	}
	kept := f.Rules[:0]
	found := false
	for _, x := range f.Rules {
		if x.ID == id {
			found = true
			continue
		}
		kept = append(kept, x)
	}
	if !found {
		return fmt.Errorf("no rule %q", id)
	}
	f.Rules = kept
	return l.writeJSON("rules.json", f)
}

// State is what was firing at the last evaluation.
func (l AlertLog) State() (AlertState, error) {
	st := AlertState{}
	err := l.readJSON("state.json", &st)
	return st, err
}

// withLock runs fn under the log's file lock: every read-modify-write of the log's files goes
// through here - Judge, Roger, StandBy - so the host judging every pass and a screen in another
// process can never lose each other's writes.
func (l AlertLog) withLock(fn func() error) error {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path("lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flockExclusive(f); err != nil {
		return err
	}
	defer func() { _ = flockUnlock(f) }()
	return fn()
}

// Judge is the one way the log advances: under the log's lock it reads what was firing, judges
// the fleet against the rules in force, records the new state and appends each change. The lock
// is what makes a crossing ONE event when the host judges every pass and a CLI read judges
// beside it in another process.
func (l AlertLog) Judge(hs []NodeHealth, now time.Time) (st AlertState, alerts []Alert, err error) {
	err = l.withLock(func() error {
		rules, err := l.Rules()
		if err != nil {
			return err
		}
		prev, err := l.State()
		if err != nil {
			return err
		}
		st, alerts = Evaluate(rules, hs, prev, now)
		if err := l.Record(st, alerts); err != nil {
			return err
		}
		return l.recordStates(hs, st, now)
	})
	if err != nil {
		return nil, nil, err
	}
	return st, alerts, nil
}

// Record stores the new state and appends each alert to the history; acknowledgements of calls
// that cleared go with them.
func (l AlertLog) Record(st AlertState, alerts []Alert) error {
	if err := l.writeJSON("state.json", st); err != nil {
		return err
	}
	if err := l.clearAcks(st); err != nil {
		return err
	}
	if err := l.clearStandBys(st); err != nil {
		return err
	}
	if len(alerts) == 0 {
		return nil
	}
	f, err := os.OpenFile(l.path("log.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, a := range alerts {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return l.capHistory()
}

const (
	historyKeep = 10000 // lines kept when the history is trimmed
	historyTrim = 12000 // lines past which it is trimmed
)

// capHistory keeps the history from growing without end: past historyTrim lines the oldest go,
// leaving historyKeep. Trimmed the same way states.jsonl is, whole and atomically.
func (l AlertLog) capHistory() error {
	b, err := os.ReadFile(l.path("log.jsonl"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) <= historyTrim {
		return nil
	}
	keep := strings.Join(lines[len(lines)-historyKeep:], "\n") + "\n"
	tmp := l.path("log.jsonl.tmp")
	if err := os.WriteFile(tmp, []byte(keep), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path("log.jsonl"))
}

// Recent is the last n alerts, newest last.
func (l AlertLog) Recent(n int) ([]Alert, error) {
	b, err := os.ReadFile(l.path("log.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var out []Alert
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		if a, ok := parseAlertLine(ln); ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// parseAlertLine reads one history line, including the shape an older binary wrote (last_heard
// as a time string, dark_for in nanoseconds): the history outlives any one build.
func parseAlertLine(ln string) (Alert, bool) {
	var a Alert
	if json.Unmarshal([]byte(ln), &a) == nil {
		return a, true
	}
	var w struct {
		Alert
		LastHeard json.RawMessage `json:"last_heard"`
		DarkFor   json.RawMessage `json:"dark_for"`
	}
	if json.Unmarshal([]byte(ln), &w) != nil {
		return Alert{}, false
	}
	a = w.Alert
	var ts time.Time
	if json.Unmarshal(w.LastHeard, &ts) == nil && !ts.IsZero() {
		a.LastHeard = ts.Unix()
	}
	var ns int64
	if json.Unmarshal(w.DarkFor, &ns) == nil && ns > 0 {
		a.DarkForS = ns / int64(time.Second)
	}
	return a, true
}

// THE LOG (health.feature §2, the station's log): each judgement records every unit's state -
// on air, dark, trouble - once per minute in states.jsonl, so a surface can draw the last hour
// and say how long a unit has been as it is, whether or not anyone was watching.

// UnitState is a unit's state at a minute.
type UnitState string

const (
	StateUnknown UnitState = ""
	StateAir     UnitState = "air"
	StateDark    UnitState = "dark"
	StateTrouble UnitState = "trouble"
)

// StateSample is every unit's state at one minute.
type StateSample struct {
	At     int64                `json:"at"`
	States map[string]UnitState `json:"states"`
}

const statesKeep = 24 * time.Hour

// stateOf is a unit's state from its health and what is firing on it.
func stateOf(h NodeHealth, st AlertState) UnitState {
	switch {
	case h.Dark:
		return StateDark
	case len(st.FiringFor(h.ID)) > 0:
		return StateTrouble
	}
	return StateAir
}

// recordStates appends this minute's sample, once per minute; older than a day is dropped.
func (l AlertLog) recordStates(hs []NodeHealth, st AlertState, now time.Time) error {
	minute := now.Truncate(time.Minute).Unix()
	prev, err := l.States(now.Add(-2 * time.Minute))
	if err != nil {
		return err
	}
	if len(prev) > 0 && prev[len(prev)-1].At == minute {
		return nil
	}
	s := StateSample{At: minute, States: map[string]UnitState{}}
	for _, h := range hs {
		s.States[h.ID] = stateOf(h, st)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if minute%(30*60) == 0 { // twice an hour, drop what is older than a day
		if err := l.pruneStates(now.Add(-statesKeep)); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(l.path("states.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func (l AlertLog) pruneStates(since time.Time) error {
	keep, err := l.States(since)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, s := range keep {
		line, _ := json.Marshal(s)
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := l.path("states.jsonl.tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path("states.jsonl"))
}

// States is the samples since a time, oldest first.
func (l AlertLog) States(since time.Time) ([]StateSample, error) {
	b, err := os.ReadFile(l.path("states.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []StateSample
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var s StateSample
		if json.Unmarshal([]byte(ln), &s) == nil && s.At >= since.Unix() {
			out = append(out, s)
		}
	}
	return out, nil
}

// LogOf is one unit's last hour as 60 cells (oldest first, unknown where nothing was judged)
// and how long it has been in its current state, from the samples. capped says the run reaches
// the oldest sample the unit has: it began before the log did, so the duration is a floor.
func LogOf(samples []StateSample, nodeID string, now time.Time) (cells []UnitState, since time.Duration, capped bool) {
	cells = make([]UnitState, 60)
	end := now.Truncate(time.Minute)
	var cur UnitState
	var curSince, first, prevAt time.Time
	for _, s := range samples {
		st, ok := s.States[nodeID]
		if !ok {
			continue
		}
		at := time.Unix(s.At, 0)
		if first.IsZero() {
			first = at
		}
		if i := 59 - int(end.Sub(at)/time.Minute); i >= 0 && i < 60 {
			cells[i] = st
		}
		if !prevAt.IsZero() && at.Sub(prevAt) > 2*time.Minute {
			cur = StateUnknown // nobody judged in between: the run before the gap is not this run
		}
		if st != cur {
			cur, curSince = st, at
		}
		prevAt = at
	}
	if curSince.IsZero() {
		return cells, 0, false
	}
	return cells, now.Sub(curSince), curSince.Equal(first)
}

// TROUBLE CALLS (ISA-18.2 by way of the station): what is standing, since when, and whether the
// operator has said ROGER to it. An acknowledgement quiets a call, never removes it; the call
// leaves when its condition clears, and its acknowledgement with it, so a fresh call is never
// pre-acknowledged.

// Ack is one acknowledgement.
type Ack struct {
	Who string `json:"who"`
	At  int64  `json:"at"`
}

// TroubleCall is one standing alert with its history.
type TroubleCall struct {
	RuleID, NodeID, Node string
	Metric               Metric
	When                 string // the rule's wording, "cpu_temp > 85"
	Value, Threshold     float64
	FiredAt              time.Time
	Since                time.Duration
	Acked                bool
	AckBy                string
	AckAt                time.Time
	Shelved              bool // on STAND BY until StandByUntil
	StandByUntil         time.Time
	Stale                bool // standing past StaleAfter
}

func (l AlertLog) acks() (map[string]Ack, error) {
	out := map[string]Ack{}
	err := l.readJSON("acks.json", &out)
	return out, err
}

// Standing is every call firing now, oldest first, with when it fired and its acknowledgement.
func (l AlertLog) Standing(now time.Time) ([]TroubleCall, error) {
	hist, err := l.Recent(1 << 20)
	if err != nil {
		return nil, err
	}
	return l.standingFrom(hist, now)
}

func (l AlertLog) standingFrom(hist []Alert, now time.Time) ([]TroubleCall, error) {
	st, err := l.State()
	if err != nil {
		return nil, err
	}
	rules, err := l.Rules()
	if err != nil {
		return nil, err
	}
	acks, err := l.acks()
	if err != nil {
		return nil, err
	}
	sb, err := l.standBys()
	if err != nil {
		return nil, err
	}
	var out []TroubleCall
	for k, firing := range st {
		if !firing {
			continue
		}
		ruleID, nodeID, _ := strings.Cut(k, "|")
		c := TroubleCall{RuleID: ruleID, NodeID: nodeID, Node: nodeID}
		for _, r := range rules {
			if r.ID == ruleID {
				c.Metric, c.When, c.Threshold = r.Metric, r.Key(), r.Value
			}
		}
		for i := len(hist) - 1; i >= 0; i-- { // the LAST time it fired is when this call began
			a := hist[i]
			if a.RuleID == ruleID && a.NodeID == nodeID && a.Firing {
				c.FiredAt, c.Node, c.Value = a.At, a.Node, a.Value
				if c.Metric == "" {
					c.Metric = a.Metric
				}
				break
			}
		}
		if !c.FiredAt.IsZero() {
			c.Since = now.Sub(c.FiredAt)
		}
		if a, ok := acks[k]; ok {
			c.Acked, c.AckBy, c.AckAt = true, a.Who, time.Unix(a.At, 0)
		}
		if until, ok := sb[k]; ok && until > now.Unix() {
			c.Shelved, c.StandByUntil = true, time.Unix(until, 0)
		}
		c.Stale = c.Since > StaleAfter
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since != out[j].Since {
			return out[i].Since > out[j].Since
		}
		return out[i].RuleID+out[i].NodeID < out[j].RuleID+out[j].NodeID
	})
	return out, nil
}

// Roger acknowledges a standing call. A call that is not standing cannot be acknowledged.
func (l AlertLog) Roger(ruleID, nodeID, who string, now time.Time) error {
	return l.withLock(func() error {
		st, err := l.State()
		if err != nil {
			return err
		}
		if !st.Firing(ruleID, nodeID) {
			return fmt.Errorf("no standing call %s on %s", ruleID, nodeID)
		}
		acks, err := l.acks()
		if err != nil {
			return err
		}
		acks[stateKey(ruleID, nodeID)] = Ack{Who: who, At: now.Unix()}
		return l.writeJSON("acks.json", acks)
	})
}

// clearAcks drops the acknowledgements of calls that are no longer standing.
func (l AlertLog) clearAcks(st AlertState) error {
	acks, err := l.acks()
	if err != nil || len(acks) == 0 {
		return err
	}
	changed := false
	for k := range acks {
		if !st[k] {
			delete(acks, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return l.writeJSON("acks.json", acks)
}

// STAND BY (ISA-18.2 shelving): the operator quiets a call for a set time, and it comes back on
// its own. Stand-bys of calls that cleared go with them, like acknowledgements.

func (l AlertLog) standBys() (map[string]int64, error) {
	out := map[string]int64{}
	err := l.readJSON("standby.json", &out)
	return out, err
}

// StandBy quiets a standing call until a time.
func (l AlertLog) StandBy(ruleID, nodeID string, until time.Time) error {
	return l.withLock(func() error {
		st, err := l.State()
		if err != nil {
			return err
		}
		if !st.Firing(ruleID, nodeID) {
			return fmt.Errorf("no standing call %s on %s", ruleID, nodeID)
		}
		sb, err := l.standBys()
		if err != nil {
			return err
		}
		sb[stateKey(ruleID, nodeID)] = until.Unix()
		return l.writeJSON("standby.json", sb)
	})
}

func (l AlertLog) clearStandBys(st AlertState) error {
	sb, err := l.standBys()
	if err != nil || len(sb) == 0 {
		return err
	}
	changed := false
	for k := range sb {
		if !st[k] {
			delete(sb, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return l.writeJSON("standby.json", sb)
}

// StaleAfter is how long a call may stand before it is STALE - a sign the rule is wrong.
const StaleAfter = 24 * time.Hour

// Rate is how many calls fired in the window ending now - the PILE-UP figure.
func (l AlertLog) Rate(now time.Time, window time.Duration) (int, error) {
	hist, err := l.Recent(1 << 20)
	if err != nil {
		return 0, err
	}
	return rateFrom(hist, now, window), nil
}

// The SIGN-OFF report: the shift in one line - calls raised and cleared, the loudest unit, each
// unit's dead air - from the history and the state log.
type SignOffReport struct {
	Raised, Cleared int
	Loudest         string // the unit that raised the most calls
	LoudestCount    int
	DeadAir         map[string]time.Duration // node id -> minutes dark this shift
}

// SignOff sums the window ending now.
func (l AlertLog) SignOff(now time.Time, window time.Duration) (SignOffReport, error) {
	hist, err := l.Recent(1 << 20)
	if err != nil {
		return SignOffReport{}, err
	}
	return l.signOffFrom(hist, now, window)
}

func (l AlertLog) signOffFrom(hist []Alert, now time.Time, window time.Duration) (SignOffReport, error) {
	r := SignOffReport{DeadAir: map[string]time.Duration{}}
	since := now.Add(-window)
	byNode := map[string]int{}
	names := map[string]string{}
	for _, a := range hist {
		if a.At.Before(since) || a.At.After(now) {
			continue
		}
		if a.Firing {
			r.Raised++
			byNode[a.NodeID]++
			names[a.NodeID] = a.Node
		} else {
			r.Cleared++
		}
	}
	for id, n := range byNode {
		if n > r.LoudestCount || (n == r.LoudestCount && names[id] < r.Loudest) {
			r.Loudest, r.LoudestCount = names[id], n
		}
	}
	samples, err := l.States(since)
	if err != nil {
		return r, err
	}
	// Each sample stands for the minutes until the next one (a minute at most, so a gap in
	// judging is not counted as dead air nobody saw).
	for i, s := range samples {
		span := time.Minute
		if i+1 < len(samples) {
			if d := time.Duration(samples[i+1].At-s.At) * time.Second; d < span {
				span = d
			}
		}
		for id, st := range s.States {
			if st == StateDark {
				r.DeadAir[id] += span
			}
		}
	}
	return r, nil
}

// Report is Standing, Rate (ten minutes) and SignOff (eight hours) from ONE read of the history,
// for a surface that redraws every couple of seconds.
func (l AlertLog) Report(now time.Time) ([]TroubleCall, int, SignOffReport, error) {
	hist, err := l.Recent(1 << 20)
	if err != nil {
		return nil, 0, SignOffReport{}, err
	}
	calls, err := l.standingFrom(hist, now)
	if err != nil {
		return nil, 0, SignOffReport{}, err
	}
	so, err := l.signOffFrom(hist, now, 8*time.Hour)
	return calls, rateFrom(hist, now, 10*time.Minute), so, err
}

func rateFrom(hist []Alert, now time.Time, window time.Duration) int {
	n := 0
	for _, a := range hist {
		if a.Firing && !a.At.Before(now.Add(-window)) && !a.At.After(now) {
			n++
		}
	}
	return n
}
