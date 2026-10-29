package edge

// SCHEDULES (features/edge/schedule.feature): an action the owner wants taken again and again without
// being there.
//
// A schedule is ONLY A CLOCK in front of what the owner could already do by hand, and never a way
// round it. A run on ANOTHER machine takes exactly the path a hand-typed `roger edge maintain` takes -
// that machine's own maintenance switch, an authority-signed grant scoped to maintain and that node,
// the mutual TLS, the caller matched to its certificate, the revocation check, the replay cache. A run
// on THIS machine is done locally, with no face and no grant, because it is the owner's own machine and
// it is the same thing `roger edge agent resume` does; the gate exists for a remote caller.
//
// This file owns the jobs, when they are due and what a run recorded; the running itself is the host's.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"
)

// MinCadence is the floor on how often a job may run. A schedule must not be able to become a hammer,
// and no maintenance action is worth taking more often than this.
const MinCadence = 5 * time.Minute

// Job is one standing action.
type Job struct {
	ID    string         `json:"id"`
	What  MaintainAction `json:"what"`            // start | stop | restart
	Agent string         `json:"agent"`           // the agent it acts on, by name
	Scope string         `json:"scope,omitempty"` // "" = the fleet, "tag:x" = the group, else a node id
	// Every is the cadence; zero with Once set is a job that runs at the next opportunity and retires.
	Every time.Duration `json:"every,omitempty"`
	Once  bool          `json:"once,omitempty"`
	// LastRun is when this job last ran at all, whatever came of it: a failed run is waited out to the
	// next cadence rather than retried in a tight loop.
	LastRun time.Time `json:"last_run,omitempty"`
	Runs    []JobRun  `json:"runs,omitempty"` // the newest few outcomes, per unit
}

// JobRun is what one run did to one unit.
type JobRun struct {
	At      time.Time `json:"at"`
	Node    string    `json:"node"`
	Outcome string    `json:"outcome"` // what happened, in the words the face or the action used
	Changed bool      `json:"changed"` // false when nothing moved: it was already so, or it could not be
}

// MaxJobRuns is how many outcomes one job keeps. Enough to see what has been happening, few enough
// that the file stays small.
const MaxJobRuns = 20

// Key identifies a job by what it does and where: one per action per agent per scope, so leaving the
// same thing standing twice replaces it.
func (j Job) Key() string { return string(j.What) + "|" + j.Agent + "|" + j.Scope }

// Words is the job as the owner wrote it: "restart watcher".
func (j Job) Words() string { return string(j.What) + " " + j.Agent }

// Cadence is the cadence in words: "every 24h" or "once".
func (j Job) Cadence() string {
	if j.Once {
		return "once"
	}
	return "every " + j.Every.String()
}

// Due reports whether this job should run now. A job that has never run is due at once; a one-off that
// has run is never due again.
func (j Job) Due(now time.Time) bool {
	if j.LastRun.IsZero() {
		return true
	}
	if j.Once {
		return false
	}
	return !now.Before(j.LastRun.Add(j.Every))
}

// NextRun is when this job is next due, and false when it never is again (a spent one-off).
func (j Job) NextRun(now time.Time) (time.Time, bool) {
	if j.LastRun.IsZero() {
		return now, true
	}
	if j.Once {
		return time.Time{}, false
	}
	return j.LastRun.Add(j.Every), true
}

// Spent reports whether a one-off has had its run, so the list can retire it.
func (j Job) Spent() bool { return j.Once && !j.LastRun.IsZero() }

// ValidateJob refuses a job that cannot mean what it says, where it is made.
func ValidateJob(j Job) error {
	acts := false
	for _, a := range MaintainActs {
		acts = acts || j.What == a
	}
	if j.What == MaintainLogs {
		return errors.New("a log tail is not worth scheduling: a tail nobody reads is only a copy of somebody's log lying around")
	}
	if !acts {
		return fmt.Errorf("%q is not an action to schedule; there is: %s", string(j.What), maintainActList())
	}
	if j.Agent == "" {
		return errors.New("a scheduled action names the agent it acts on")
	}
	if err := ValidInstanceName(j.Agent); err != nil {
		return fmt.Errorf("that is not an agent name: %w", err)
	}
	switch {
	case j.Once && j.Every != 0:
		return errors.New("a job runs either once or on a cadence, not both")
	case !j.Once && j.Every == 0:
		return errors.New("a job needs a cadence (--every 24h) or --once")
	case !j.Once && j.Every < MinCadence:
		return fmt.Errorf("the shortest cadence is %s, so a schedule cannot become a hammer", MinCadence)
	}
	if tag, ok := ScopeTag(j.Scope); ok {
		return ValidTag(tag)
	}
	return nil
}

// maintainActList is the schedulable actions as a sentence, for a refusal that has to name them.
func maintainActList() string {
	out := make([]string, 0, len(MaintainActs))
	for _, a := range MaintainActs {
		out = append(out, string(a))
	}
	return joinWords(out)
}

func joinWords(in []string) string {
	switch len(in) {
	case 0:
		return ""
	case 1:
		return in[0]
	}
	return fmt.Sprintf("%s or %s", joinComma(in[:len(in)-1]), in[len(in)-1])
}

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// NewJobID mints an id an owner can type back.
func NewJobID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "job_" + hex.EncodeToString(b)
}

// JobSet is the owner's schedules.
type JobSet []Job

// Due is the jobs that should run now, oldest-run first so a backlog is worked in order.
func (js JobSet) Due(now time.Time) JobSet {
	var out JobSet
	for _, j := range js {
		if j.Due(now) {
			out = append(out, j)
		}
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].LastRun.Before(out[k].LastRun) })
	return out
}

// Targets resolves a job's scope to the units it acts on. THIS MACHINE IS A TARGET like any other:
// acting on a roger here needs no face, no grant and no switch, because it is the owner's own machine
// and the same thing `roger edge agent resume` does - the gate exists for a REMOTE caller. A run on a
// unit that is dark is a target too: the run records that it was dark, which is the fact worth having.
func (js JobSet) Targets(j Job, nodes []Node, selfID string) []Node {
	var out []Node
	for _, n := range nodes {
		switch {
		case j.Scope == "":
		case j.Scope == n.ID:
		default:
			t, ok := ScopeTag(j.Scope)
			if !ok || !HasTag(n.Tags, t) {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// Node is the little a schedule needs to know about a unit: who it is, what the owner called it, and
// the words that group it.
type Node struct {
	ID   string
	Name string
	Tags []string
}

// RecordRun adds an outcome to a job and marks it as having run, keeping the newest MaxJobRuns.
func (j *Job) RecordRun(at time.Time, node, outcome string, changed bool) {
	j.LastRun = at
	j.Runs = append(j.Runs, JobRun{At: at, Node: node, Outcome: outcome, Changed: changed})
	if len(j.Runs) > MaxJobRuns {
		j.Runs = append([]JobRun(nil), j.Runs[len(j.Runs)-MaxJobRuns:]...)
	}
}

// Jobs are the owner's schedules, kept beside the rules on the machine that runs them.
func (l AlertLog) Jobs() (JobSet, error) {
	var set JobSet
	if err := l.readJSON("schedule.json", &set); err != nil {
		return nil, err
	}
	// A spent one-off is retired on the read, so it is neither run nor listed again.
	out := make(JobSet, 0, len(set))
	for _, j := range set {
		if !j.Spent() {
			out = append(out, j)
		}
	}
	return out, nil
}

// SetJob keeps one job, replacing any with the same action, agent and scope. It returns the job as
// kept, so a caller can name its id.
func (l AlertLog) SetJob(j Job) (Job, error) {
	if err := ValidateJob(j); err != nil {
		return Job{}, err
	}
	if j.ID == "" {
		j.ID = NewJobID()
	}
	err := l.withLock(func() error {
		var set JobSet
		if err := l.readJSON("schedule.json", &set); err != nil {
			return err
		}
		out := make(JobSet, 0, len(set)+1)
		for _, had := range set {
			if had.Key() == j.Key() {
				j.ID = had.ID // the same standing action keeps the id the owner already knows
				continue
			}
			out = append(out, had)
		}
		return l.writeJSON("schedule.json", append(out, j))
	})
	return j, err
}

// RemoveJob drops one by id, and says whether there was one.
func (l AlertLog) RemoveJob(id string) (bool, error) {
	had := false
	err := l.withLock(func() error {
		var set JobSet
		if err := l.readJSON("schedule.json", &set); err != nil {
			return err
		}
		out := make(JobSet, 0, len(set))
		for _, j := range set {
			if j.ID == id {
				had = true
				continue
			}
			out = append(out, j)
		}
		if !had {
			return nil
		}
		return l.writeJSON("schedule.json", out)
	})
	return had, err
}

// NoteRun records what one run did, under the lock, against the job it belongs to. A run is recorded
// whether it worked or not: that is what keeps a failing job from being retried on every pass.
func (l AlertLog) NoteRun(id string, at time.Time, node, outcome string, changed bool) error {
	return l.withLock(func() error {
		var set JobSet
		if err := l.readJSON("schedule.json", &set); err != nil {
			return err
		}
		found := false
		for i := range set {
			if set[i].ID != id {
				continue
			}
			set[i].RecordRun(at, node, outcome, changed)
			found = true
		}
		if !found {
			return nil // it was dropped while the run was in flight: nothing to record it against
		}
		return l.writeJSON("schedule.json", set)
	})
}
