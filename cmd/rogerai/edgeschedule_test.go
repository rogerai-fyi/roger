package main

// Executable pins for features/edge/schedule.feature, the owner's side: the CLI, and the runner that
// takes a due job through the ORDINARY maintenance path - never around it.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// scheduleSeed is an Edge with two members, one tagged, both reachable, and a maintain grant for each.
func scheduleSeed(t *testing.T) {
	t.Helper()
	offline(t)
	// A locally-rooted Edge, so this machine can issue the grants a run needs.
	_, err := edgeDesignateCore(edgeAuthDir(), "desk")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "desk", "", "")
	require.NoError(t, err)
	edgeSeed(t,
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "host", Presence: string(edge.PresenceVerified),
			Tags: []string{"production"}, Pin: "abab",
			Transports: []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.5:44121", Fingerprint: "abab"}}},
		store.EdgeNode{ID: "n_pi", Name: "quiet-pi", Kind: "host", Presence: string(edge.PresenceVerified),
			Pin: "cdcd", Transports: []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.6:44121", Fingerprint: "cdcd"}}},
		store.EdgeNode{ID: "n_dark", Name: "dark-box", Kind: "host", Presence: string(edge.PresenceDark)},
	)
}

func TestEdgeScheduleAddListAndRemove(t *testing.T) {
	scheduleSeed(t)

	out, code := edgeRun(t, "edge", "schedule")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing is scheduled on this machine")
	require.Contains(t, out, "--every 24h", "it says how to leave one")

	out, code = edgeRun(t, "edge", "schedule", "add", "restart", "watcher", "--scope", "tag:production", "--every", "24h")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "restart watcher on every node tagged production, every 24h")
	require.Contains(t, out, "Neither is bypassed.", "it says what gate a run still goes through")
	require.Contains(t, out, "first run: on the next pass")

	out, code = edgeRun(t, "edge", "schedule")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "SCHEDULED on this machine · 1")
	require.Contains(t, out, "restart watcher")
	require.Contains(t, out, "tag production")
	require.Contains(t, out, "never run", "a job that has not run says so rather than showing an empty outcome")

	// A one-off says it runs once.
	out, code = edgeRun(t, "edge", "schedule", "add", "start", "watcher", "--scope", "quiet-pi", "--once")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, ", once")

	// Dropping it, by the id the list showed.
	jobs, err := edgeAlertLog().Jobs()
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	out, code = edgeRun(t, "edge", "schedule", "rm", jobs[0].ID)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing runs for it again")
	jobs, _ = edgeAlertLog().Jobs()
	require.Len(t, jobs, 1)

	out, code = edgeRun(t, "edge", "schedule", "rm", "job_nope")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "no schedule has the id")
}

func TestEdgeScheduleRefusals(t *testing.T) {
	scheduleSeed(t)
	for _, args := range [][]string{
		{"edge", "schedule", "add", "reboot", "watcher", "--every", "24h"},
		{"edge", "schedule", "add", "logs", "watcher", "--every", "24h"},
		{"edge", "schedule", "add", "restart", "--every", "24h"},
		{"edge", "schedule", "add", "restart", "watcher"},
		{"edge", "schedule", "add", "restart", "watcher", "--every", "1m"},
		{"edge", "schedule", "add", "restart", "watcher", "--every", "soon"},
		{"edge", "schedule", "add", "restart", "watcher", "--every", "24h", "--once"},
		{"edge", "schedule", "add", "restart", "watcher", "--every", "24h", "--scope", "nosuchnode"},
		{"edge", "schedule", "sideways"},
		{"edge", "schedule", "rm"},
	} {
		out, code := edgeRun(t, args...)
		require.NotEqual(t, 0, code, "%v must be refused: %s", args, out)
	}
	// The log tail is refused with its OWN reason, not lumped in with the unknown actions.
	out, _ := edgeRun(t, "edge", "schedule", "add", "logs", "watcher", "--every", "24h")
	require.Contains(t, out, "only a copy of somebody's log")
	jobs, _ := edgeAlertLog().Jobs()
	require.Empty(t, jobs, "not one refusal left a job behind")
}

// THE SAFETY ARGUMENT, pinned: a run goes through edgeMaintainInvoke, which is the same path a typed
// `roger edge maintain` takes. The dial is stubbed here, so what is asserted is WHAT WAS SENT and to
// whom - the gate itself is pinned by the maintenance tests.
func TestAScheduledRunTakesTheOrdinaryPath(t *testing.T) {
	scheduleSeed(t)
	_, _, err := edgeGrantIssueCore("self", "maintain", "shed", "1h", 0)
	require.NoError(t, err)

	type sent struct {
		addr string
		cap  string
		task edge.MaintainTask
	}
	var got []sent
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	edgeDialInvokeTaskFn = func(_ context.Context, p client.EdgePeer, _ *tls.Certificate, _ store.Grant, cap string, raw json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		var task edge.MaintainTask
		_ = json.Unmarshal(raw, &task)
		got = append(got, sent{addr: p.Addr, cap: cap, task: task})
		out, _ := json.Marshal(edge.MaintainResult{Action: task.Action, Target: task.Target, Changed: true,
			Says: "asked \"watcher\" to stop, and it has gone."})
		return edge.InvokeResult{Output: out}, protocol.UsageReceipt{}, nil
	}

	_, code := edgeRun(t, "edge", "schedule", "add", "restart", "watcher", "--scope", "tag:production", "--every", "1h")
	require.Equal(t, 0, code)

	now := time.Now()
	edgeRunSchedule(loadConfig(), now)

	require.Len(t, got, 1, "only the tagged unit")
	require.Equal(t, "10.0.0.5:44121", got[0].addr, "the unit's own face")
	require.Equal(t, string(edge.Maintain), got[0].cap, "under the maintain capability, so the grant's scope is checked")
	require.Equal(t, edge.MaintainRestart, got[0].task.Action)
	require.Equal(t, "watcher", got[0].task.Target)

	// It is recorded, so the next pass does not run it again.
	jobs, err := edgeAlertLog().Jobs()
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.False(t, jobs[0].LastRun.IsZero())
	require.False(t, jobs[0].Due(now))
	require.Len(t, jobs[0].Runs, 1)
	require.Equal(t, "shed", jobs[0].Runs[0].Node)
	require.True(t, jobs[0].Runs[0].Changed)

	// And the owner's own view of that unit carries it, so the events timeline shows scheduled runs.
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get("n_shed")
	require.NoError(t, err)
	require.True(t, ok)
	found := false
	for _, e := range n.History {
		if e.What == "scheduled" {
			found = true
			require.Contains(t, e.Detail, "restart watcher")
			require.Contains(t, e.Detail, "has gone")
		}
	}
	require.True(t, found, "the run is on the timeline: %+v", n.History)

	// The pass comes round again and nothing is sent, because it is not due.
	got = nil
	edgeRunSchedule(loadConfig(), now.Add(time.Minute))
	require.Empty(t, got)
	// ...and when it IS due, it goes again.
	edgeRunSchedule(loadConfig(), now.Add(2*time.Hour))
	require.Len(t, got, 1)
}

// A run that cannot be made is recorded as what it is, and WAITED OUT - never retried on every pass.
func TestAScheduledRunThatCannotBeMadeIsRecordedAndWaitedOut(t *testing.T) {
	scheduleSeed(t)
	calls := 0
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	edgeDialInvokeTaskFn = func(context.Context, client.EdgePeer, *tls.Certificate, store.Grant, string, json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		calls++
		return edge.InvokeResult{Refused: edge.MaintainOffReason}, protocol.UsageReceipt{}, nil
	}

	// A DARK unit has no face: that is a fact about the unit, not a failure of the job.
	_, code := edgeRun(t, "edge", "schedule", "add", "restart", "watcher", "--scope", "dark-box", "--every", "1h")
	require.Equal(t, 0, code)
	now := time.Now()
	edgeRunSchedule(loadConfig(), now)
	require.Zero(t, calls, "nothing was dialled: there is no face")
	jobs, _ := edgeAlertLog().Jobs()
	require.Len(t, jobs[0].Runs, 1)
	require.Contains(t, jobs[0].Runs[0].Outcome, "dark")
	require.False(t, jobs[0].Runs[0].Changed)
	require.False(t, jobs[0].Due(now), "a run that could not be made is still a run: it waits out its cadence")

	// A unit with no grant: said, recorded, waited out.
	_, code = edgeRun(t, "edge", "schedule", "add", "restart", "watcher", "--scope", "quiet-pi", "--every", "1h")
	require.Equal(t, 0, code)
	edgeRunSchedule(loadConfig(), now)
	jobs, _ = edgeAlertLog().Jobs()
	for _, j := range jobs {
		if j.Scope != "n_pi" {
			continue
		}
		require.Len(t, j.Runs, 1)
		require.Contains(t, j.Runs[0].Outcome, "no live grant")
		require.False(t, j.Due(now))
	}

	// A face that refuses: the refusal is recorded in the words the face used.
	_, _, err := edgeGrantIssueCore("self", "maintain", "quiet-pi", "1h", 0)
	require.NoError(t, err)
	edgeRunSchedule(loadConfig(), now.Add(2*time.Hour))
	jobs, _ = edgeAlertLog().Jobs()
	for _, j := range jobs {
		if j.Scope != "n_pi" {
			continue
		}
		last := j.Runs[len(j.Runs)-1]
		require.Contains(t, last.Outcome, "maintenance is off on that machine")
		require.False(t, last.Changed)
	}
	require.Equal(t, 1, calls, "one dial, for the one unit that had a grant and a face")
}

// A one-off runs once and then retires itself.
func TestAScheduledOneOffRunsOnceAndRetires(t *testing.T) {
	scheduleSeed(t)
	_, _, err := edgeGrantIssueCore("self", "maintain", "shed", "1h", 0)
	require.NoError(t, err)
	calls := 0
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	edgeDialInvokeTaskFn = func(context.Context, client.EdgePeer, *tls.Certificate, store.Grant, string, json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		calls++
		out, _ := json.Marshal(edge.MaintainResult{Changed: true, Says: "started it"})
		return edge.InvokeResult{Output: out}, protocol.UsageReceipt{}, nil
	}
	_, code := edgeRun(t, "edge", "schedule", "add", "start", "watcher", "--scope", "shed", "--once")
	require.Equal(t, 0, code)

	now := time.Now()
	edgeRunSchedule(loadConfig(), now)
	require.Equal(t, 1, calls)
	jobs, _ := edgeAlertLog().Jobs()
	require.Empty(t, jobs, "a spent one-off is retired from the list")

	edgeRunSchedule(loadConfig(), now.Add(100*time.Hour))
	require.Equal(t, 1, calls, "and never runs again")

	out, code := edgeRun(t, "edge", "schedule")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing is scheduled")
}

// A fleet-wide job never targets this machine's own roger: it cannot act on itself through its own
// face, and one that tried would refuse on every pass.
func TestAFleetWideScheduleSkipsThisMachine(t *testing.T) {
	scheduleSeed(t)
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	var dialled []string
	edgeDialInvokeTaskFn = func(_ context.Context, p client.EdgePeer, _ *tls.Certificate, _ store.Grant, _ string, _ json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		dialled = append(dialled, p.Node)
		out, _ := json.Marshal(edge.MaintainResult{Says: "ok"})
		return edge.InvokeResult{Output: out}, protocol.UsageReceipt{}, nil
	}
	for _, n := range []string{"shed", "quiet-pi"} {
		_, _, err := edgeGrantIssueCore("self", "maintain", n, "1h", 0)
		require.NoError(t, err)
	}
	_, code := edgeRun(t, "edge", "schedule", "add", "stop", "watcher", "--every", "1h")
	require.Equal(t, 0, code)

	edgeRunSchedule(loadConfig(), time.Now())
	require.ElementsMatch(t, []string{"shed", "quiet-pi"}, dialled, "every member, and the dark one recorded without a dial")
	for _, d := range dialled {
		require.NotEqual(t, edgeThisNodeID(), d)
	}
	jobs, _ := edgeAlertLog().Jobs()
	require.Len(t, jobs[0].Runs, 3, "shed, quiet-pi and the dark one")
	require.True(t, strings.Contains(jobs[0].Runs[2].Outcome, "dark") ||
		strings.Contains(jobs[0].Runs[0].Outcome, "dark") ||
		strings.Contains(jobs[0].Runs[1].Outcome, "dark"))
}
