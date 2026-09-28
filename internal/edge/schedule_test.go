package edge

// Executable pins for features/edge/schedule.feature: the jobs, when they are due, what a run records,
// and every refusal - including the cadence floor that stops a schedule becoming a hammer.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAJobIsRefusedWhenItCannotMeanWhatItSays(t *testing.T) {
	ok := Job{What: MaintainRestart, Agent: "watcher", Every: time.Hour}
	require.NoError(t, ValidateJob(ok))
	require.NoError(t, ValidateJob(Job{What: MaintainStart, Agent: "watcher", Once: true}))
	require.NoError(t, ValidateJob(Job{What: MaintainStop, Agent: "watcher", Every: MinCadence, Scope: TagScope("production")}))

	for _, c := range []struct {
		name string
		j    Job
		says string
	}{
		{"not an action", Job{What: "reboot", Agent: "watcher", Every: time.Hour}, "is not an action to schedule"},
		{"a log tail", Job{What: MaintainLogs, Agent: "watcher", Every: time.Hour}, "only a copy of somebody's log"},
		{"no agent", Job{What: MaintainRestart, Every: time.Hour}, "names the agent"},
		{"a bad agent name", Job{What: MaintainRestart, Agent: "not a name", Every: time.Hour}, "not an agent name"},
		{"no cadence", Job{What: MaintainRestart, Agent: "watcher"}, "needs a cadence"},
		{"both", Job{What: MaintainRestart, Agent: "watcher", Every: time.Hour, Once: true}, "not both"},
		{"under the floor", Job{What: MaintainRestart, Agent: "watcher", Every: time.Minute}, "shortest cadence"},
		{"a broken tag scope", Job{What: MaintainRestart, Agent: "w", Every: time.Hour, Scope: "tag:a b"}, "lowercase letters"},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.ErrorContains(t, ValidateJob(c.j), c.says)
		})
	}
	// The refusal names the actions that ARE schedulable, and the tail is refused with its own reason
	// rather than being lumped in with the unknown ones.
	err := ValidateJob(Job{What: "reboot", Agent: "w", Every: time.Hour})
	for _, a := range MaintainActs {
		require.Contains(t, err.Error(), string(a))
	}
}

func TestWhenAJobIsDue(t *testing.T) {
	now := time.Now()
	fresh := Job{What: MaintainRestart, Agent: "watcher", Every: time.Hour}
	require.True(t, fresh.Due(now), "a job that has never run is due at once")
	at, ok := fresh.NextRun(now)
	require.True(t, ok)
	require.Equal(t, now, at)

	ran := fresh
	ran.LastRun = now.Add(-59 * time.Minute)
	require.False(t, ran.Due(now), "fifty-nine minutes into an hour is not due")
	require.True(t, ran.Due(now.Add(time.Minute)), "and a minute later it is")
	at, ok = ran.NextRun(now)
	require.True(t, ok)
	require.Equal(t, ran.LastRun.Add(time.Hour), at)

	// A one-off runs once and is never due again, and the list retires it.
	once := Job{What: MaintainStart, Agent: "watcher", Once: true}
	require.True(t, once.Due(now))
	require.False(t, once.Spent())
	once.LastRun = now
	require.False(t, once.Due(now.Add(100*time.Hour)))
	require.True(t, once.Spent())
	_, ok = once.NextRun(now)
	require.False(t, ok, "it is never due again")

	// Due() picks the oldest run first, so a backlog is worked in order.
	set := JobSet{
		{ID: "b", What: MaintainRestart, Agent: "b", Every: time.Minute * 10, LastRun: now.Add(-time.Hour)},
		{ID: "a", What: MaintainRestart, Agent: "a", Every: time.Minute * 10, LastRun: now.Add(-2 * time.Hour)},
		{ID: "c", What: MaintainRestart, Agent: "c", Every: time.Hour, LastRun: now.Add(-time.Minute)},
	}
	due := set.Due(now)
	require.Len(t, due, 2)
	require.Equal(t, "a", due[0].ID, "the one waiting longest goes first")
	require.Equal(t, "b", due[1].ID)
}

func TestAJobsScopeDecidesWhatItTouches(t *testing.T) {
	nodes := []Node{
		{ID: "n_self", Name: "desk"},
		{ID: "n_shed", Name: "shed", Tags: []string{"production"}},
		{ID: "n_pi", Name: "quiet-pi", Tags: []string{"bench"}},
	}
	var set JobSet
	names := func(in []Node) []string {
		out := []string{}
		for _, n := range in {
			out = append(out, n.Name)
		}
		return out
	}
	// The fleet: every member, and NEVER this machine's own roger - it cannot act on itself through
	// its own face, and a fleet-wide job that tried would refuse on every pass.
	require.Equal(t, []string{"shed", "quiet-pi"},
		names(set.Targets(Job{Scope: ""}, nodes, "n_self")))
	// A group.
	require.Equal(t, []string{"shed"},
		names(set.Targets(Job{Scope: TagScope("production")}, nodes, "n_self")))
	// One node.
	require.Equal(t, []string{"quiet-pi"},
		names(set.Targets(Job{Scope: "n_pi"}, nodes, "n_self")))
	// A group nothing carries.
	require.Empty(t, set.Targets(Job{Scope: TagScope("winter")}, nodes, "n_self"))
}

func TestJobsAreKeptReplacedRecordedAndRemoved(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	got, err := l.Jobs()
	require.NoError(t, err)
	require.Empty(t, got)

	kept, err := l.SetJob(Job{What: MaintainRestart, Agent: "watcher", Every: 24 * time.Hour, Scope: TagScope("production")})
	require.NoError(t, err)
	require.NotEmpty(t, kept.ID)
	require.Equal(t, "restart watcher", kept.Words())
	require.Equal(t, "every 24h0m0s", kept.Cadence())

	// The same standing action replaces itself AND keeps the id the owner already knows.
	again, err := l.SetJob(Job{What: MaintainRestart, Agent: "watcher", Every: 12 * time.Hour, Scope: TagScope("production")})
	require.NoError(t, err)
	require.Equal(t, kept.ID, again.ID)
	got, _ = l.Jobs()
	require.Len(t, got, 1)
	require.Equal(t, 12*time.Hour, got[0].Every)

	// A different agent, or a different scope, is a different job.
	_, err = l.SetJob(Job{What: MaintainRestart, Agent: "other", Every: time.Hour})
	require.NoError(t, err)
	got, _ = l.Jobs()
	require.Len(t, got, 2)

	// An invalid one is refused and changes nothing.
	_, err = l.SetJob(Job{What: MaintainRestart, Agent: "watcher", Every: time.Second})
	require.Error(t, err)
	got, _ = l.Jobs()
	require.Len(t, got, 2)

	// A run is recorded whether it worked or not - that is what keeps a failing job from being retried
	// on every pass.
	now := time.Now()
	require.NoError(t, l.NoteRun(kept.ID, now, "shed", "asked \"watcher\" to stop, and it has gone.", true))
	require.NoError(t, l.NoteRun(kept.ID, now, "quiet-pi", "it is dark: no face to dial", false))
	got, _ = l.Jobs()
	for _, j := range got {
		if j.ID != kept.ID {
			continue
		}
		require.Equal(t, now.Unix(), j.LastRun.Unix())
		require.Len(t, j.Runs, 2)
		require.True(t, j.Runs[0].Changed)
		require.False(t, j.Runs[1].Changed)
		require.Contains(t, j.Runs[1].Outcome, "dark")
		require.False(t, j.Due(now), "it has just run: not due again until its cadence")
	}
	// Noting a run against a job that was dropped mid-flight is not an error.
	require.NoError(t, l.NoteRun("job_gone", now, "shed", "whatever", false))

	// Only the newest outcomes are kept.
	for i := 0; i < MaxJobRuns+5; i++ {
		require.NoError(t, l.NoteRun(kept.ID, now, "shed", "again", false))
	}
	got, _ = l.Jobs()
	for _, j := range got {
		if j.ID == kept.ID {
			require.Len(t, j.Runs, MaxJobRuns)
		}
	}

	had, err := l.RemoveJob(kept.ID)
	require.NoError(t, err)
	require.True(t, had)
	had, err = l.RemoveJob(kept.ID)
	require.NoError(t, err)
	require.False(t, had, "removing what is not there is not an error")
	got, _ = l.Jobs()
	require.Len(t, got, 1)
}

func TestASpentOneOffIsRetiredFromTheList(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	kept, err := l.SetJob(Job{What: MaintainStart, Agent: "watcher", Once: true})
	require.NoError(t, err)
	got, _ := l.Jobs()
	require.Len(t, got, 1, "before it runs it is listed")

	require.NoError(t, l.NoteRun(kept.ID, time.Now(), "shed", "started it", true))
	got, err = l.Jobs()
	require.NoError(t, err)
	require.Empty(t, got, "a one-off that has run is neither listed nor run again")
	require.Empty(t, got.Due(time.Now()))
}
