package main

// SCHEDULES, the owner's side (features/edge/schedule.feature).
//
//	roger edge schedule                                                      what is standing
//	roger edge schedule add restart watcher --scope tag:production --every 24h
//	roger edge schedule add start watcher --scope shed --once
//	roger edge schedule rm <id>
//
// A run takes THE SAME PATH a hand-typed `roger edge maintain` takes - edgeMaintainInvoke, and through
// it the target's own maintenance switch, the grant, the pin, the face - so a schedule can never do
// anything its owner could not do by hand at that moment. It lives on the machine that runs it,
// because that is the machine holding the grant, and nothing is pushed to any member.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
)

func cmdEdgeSchedule(cfg config, args []string) error {
	leaf, _ := edgeLeaf("schedule")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	log := edgeAlertLog()
	if len(argv.pos) == 0 {
		return edgePrintSchedule(os.Stdout, time.Now())
	}
	switch argv.pos[0] {
	case "rm", "remove":
		if len(argv.pos) < 2 {
			return usagef("usage: roger edge schedule rm <id>  (`roger edge schedule` lists them)")
		}
		had, err := log.RemoveJob(argv.pos[1])
		if err != nil {
			return err
		}
		if !had {
			return fmt.Errorf("no schedule has the id %q - `roger edge schedule` lists them", argv.pos[1])
		}
		fmt.Printf("dropped %s; nothing runs for it again.\n", argv.pos[1])
		return nil
	case "add":
		if len(argv.pos) < 3 {
			return usagef("usage: roger edge schedule add <start|stop|restart> <agent> [--scope fleet|tag:<tag>|<node>] [--every <dur>|--once]")
		}
	default:
		return usagef("roger edge schedule takes add or rm, not %q", argv.pos[0])
	}

	j := edge.Job{What: edge.MaintainAction(strings.ToLower(argv.pos[1])), Agent: argv.pos[2], Once: argv.has("once")}
	if v, ok := argv.flags["every"]; ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return usagef("--every takes a duration like 24h or 30m, not %q", v)
		}
		j.Every = d
	}
	scope, where, err := edgeExpectScope(argv.flags["scope"])
	if err != nil {
		return err
	}
	j.Scope = scope
	kept, err := log.SetJob(j)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s on %s, %s\n", kept.ID, kept.Words(), where, kept.Cadence())
	fmt.Println("  it runs on this machine's own pass, through the same gate a typed `roger edge maintain` goes through:")
	fmt.Println("  the target's maintenance switch and a grant scoped to maintain on it. Neither is bypassed.")
	if at, ok := kept.NextRun(time.Now()); ok {
		fmt.Printf("  first run: %s\n", edgeWhenWords(at, time.Now()))
	}
	return nil
}

// edgeWhenWords is a time as an operator reads it: "now" or "in 24h".
func edgeWhenWords(at, now time.Time) string {
	if !at.After(now) {
		return "on the next pass"
	}
	return "in " + edge.Age(at.Sub(now))
}

func edgePrintSchedule(w io.Writer, now time.Time) error {
	jobs, err := edgeAlertLog().Jobs()
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Fprintln(w, "nothing is scheduled on this machine.")
		fmt.Fprintln(w, "  leave an action standing: roger edge schedule add restart <agent> --scope tag:<tag> --every 24h")
		return nil
	}
	fmt.Fprintf(w, "SCHEDULED on this machine · %d\n", len(jobs))
	names := edgeNodeNames()
	for _, j := range jobs {
		when := "never run"
		if !j.LastRun.IsZero() {
			when = edge.Age(now.Sub(j.LastRun)) + " ago"
		}
		next := "-"
		if at, ok := j.NextRun(now); ok {
			next = edgeWhenWords(at, now)
		}
		fmt.Fprintf(w, "%-12s %-22s %-24s %-14s last %-12s next %s\n", j.ID, j.Words(),
			edgeExpectScopeWords(j.Scope, names), j.Cadence(), when, next)
		for _, r := range lastRunsOf(j, 3) {
			fmt.Fprintf(w, "               %-18s %s\n", r.Node, r.Outcome)
		}
	}
	return nil
}

// lastRunsOf is the newest n outcomes of a job, newest first.
func lastRunsOf(j edge.Job, n int) []edge.JobRun {
	out := make([]edge.JobRun, 0, n)
	for i := len(j.Runs) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, j.Runs[i])
	}
	return out
}

// edgeScheduleRunning guards one run at a time on this machine: a slow run must not stack up behind
// the pass that comes round again while it is still going.
var edgeScheduleRunning sync.Mutex

// edgeRunScheduleFn is the schedule run the host pass makes, as a seam for a test to stall.
var edgeRunScheduleFn = edgeRunSchedule

// edgeRunSchedule runs whatever is due, once. It is called from the host's ordinary pass, so a machine
// that is not running a roger runs nothing - which is correct: the schedule belongs to the machine that
// holds the grant, and a machine that is off is not holding anything.
//
// ONE RUNNER PER MACHINE. Every roger on a machine makes this pass, and if each ran the schedule they
// would race for it and take turns being the target of their own jobs (a live run did exactly that: the
// roger that ran "restart roger" was that roger). The runner is the first live instance BY NAME, which
// is deterministic, always exactly one, and needs no election.
// record is how a run reaches the owner's own view of that unit. It MUST be the host's state, not a
// fresh read: the host saves its own copy at the end of the same pass, and a separate write was simply
// overwritten - the run never appeared on the timeline, which is how this was found.
func edgeRunSchedule(cfg config, now time.Time, record func(nodeID, what, detail string)) {
	if !edgeScheduleRunsHereFn(now) {
		return
	}
	if !edgeScheduleRunning.TryLock() {
		return // a run is still going; the next pass will find whatever is still due
	}
	defer edgeScheduleRunning.Unlock()

	log := edgeAlertLog()
	jobs, err := log.Jobs()
	if err != nil {
		return
	}
	due := jobs.Due(now)
	if len(due) == 0 {
		return
	}
	st, err := loadEdgeState()
	if err != nil {
		return
	}
	nodes, err := st.list()
	if err != nil {
		return
	}
	units := make([]edge.Node, 0, len(nodes))
	for _, n := range nodes {
		units = append(units, edge.Node{ID: n.ID, Name: n.Name, Tags: n.Tags})
	}
	self := edgeThisNodeID()
	for _, j := range due {
		targets := jobs.Targets(j, units, self)
		if len(targets) == 0 {
			// Nothing matched. Say so rather than coming back to it every pass: a job whose scope
			// matches no unit is a job the owner should hear about.
			_ = log.NoteRun(j.ID, now, "-", "no unit matched this scope", false)
			continue
		}
		for _, target := range targets {
			outcome, changed := edgeRunScheduledOn(cfg, j, target, self)
			_ = log.NoteRun(j.ID, now, target.Name, outcome, changed)
			// The owner's own view of that unit records what their schedule did to it, so the events
			// timeline shows scheduled runs beside everything else.
			if record != nil {
				record(target.ID, "scheduled", j.Words()+": "+outcome)
			}
		}
	}
}

// edgeScheduleRunsHereFn is the one-runner rule, as a seam: a test that is exercising what a RUN does
// sets it rather than arranging instance names to come out in the right order. The rule itself is
// pinned directly.
var edgeScheduleRunsHereFn = edgeScheduleRunsHere

// edgeScheduleRunsHere reports whether THIS process is the roger that runs this machine's schedules:
// the first live instance by name. Every roger passes here, and only one may act.
func edgeScheduleRunsHere(now time.Time) bool {
	me := edgeMyInstanceName()
	if me == "" {
		return false // not registered: this process is not one of the machine's rogers
	}
	first := ""
	for _, in := range edge.LiveHousehold(edgeInstancesDir(), now) {
		if first == "" || in.Name < first {
			first = in.Name
		}
	}
	return first == me
}

// edgeMyInstanceName is the name THIS process registered, or "" before it has. It is set once the host
// registers and read on every pass.
var edgeMyInstanceName = func() string { return "" }

// edgeRunScheduledOn takes one job to one unit. THIS MACHINE is acted on LOCALLY - no face, no grant,
// no switch, because it is the owner's own machine and this is the same thing `roger edge agent resume`
// does; the gate exists for a REMOTE caller, and a roger cannot reach its own face anyway. Every other
// unit goes through the ordinary maintenance path, and one with no face is DARK, which is a fact about
// the unit and not a failure of the job.
func edgeRunScheduledOn(cfg config, j edge.Job, target edge.Node, selfID string) (outcome string, changed bool) {
	if target.ID == selfID {
		return edgeRunScheduledHere(cfg, j)
	}
	res, err := edgeMaintainInvoke(cfg, target.ID, edge.MaintainTask{Action: j.What, Target: j.Agent})
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "no LAN face") {
			return "dark: no face to dial", false
		}
		return msg, false
	}
	return res.Says, res.Changed
}

// edgeRunScheduledHere runs the action on THIS machine, through the same functions the face's own
// handler runs. edgeStopAgent still refuses to signal this very process, so a roger cannot stop
// itself even here.
func edgeRunScheduledHere(cfg config, j edge.Job) (outcome string, changed bool) {
	now := time.Now()
	var says, refused string
	defer func() {
		if refused != "" {
			// Say WHICH roger tried, so a self-refusal reads as the fact it is: the schedule is run by
			// one roger on this machine, and it cannot be its own target.
			outcome = refused + " (the schedule is run here by " + edgeMyInstanceName() + ")"
		}
	}()
	switch j.What {
	case edge.MaintainStop:
		says, changed, refused = edgeStopAgent(j.Agent, now)
	case edge.MaintainRestart:
		says, changed, refused = edgeRestartAgent(cfg, j.Agent, now)
	default:
		says, changed, refused = edgeStartAgent(cfg, j.Agent, now)
	}
	if refused != "" {
		return refused, false
	}
	return says, changed
}
