package main

// Coverage for the console's Edge seams: the alarm, sink, rule, maintenance, events, intent and floor
// hooks the browser reads, each from the same log and the same words the terminal uses.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/tui"
)

func TestEdgeHooksForAnUnwiredEdgeAreEmpty(t *testing.T) {
	require.Nil(t, edgeHooksFor(config{}, nil).Fleet)
	require.Nil(t, edgeHooksFor(config{}, &tui.Hooks{}).Rules, "an Edge the TUI never wired leaves the tab disabled")
	require.Nil(t, edgeFloorHook(&tui.Hooks{}), "no floor hook, no floor")
}

func TestEdgeHooksForTheConsoleShareTheTerminalsLog(t *testing.T) {
	self := newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board", Instances: []store.EdgeInstance{{Name: "roger"}}})
	_, th := edgeHostOff(t)
	w := edgeHooksFor(loadConfig(), &th)
	require.Equal(t, "desk", w.Self)
	require.NotNil(t, w.Fleet)

	// Rules: set through the console, read back by the terminal's log.
	rules, err := w.Rules()
	require.NoError(t, err)
	before := len(rules)
	_, err = w.SetRule("nonsense", "gt", "1", "")
	require.ErrorContains(t, err, `unknown metric "nonsense"`)
	said, err := w.SetRule("cpu_temp", "gt", "70", "attic")
	require.NoError(t, err)
	id := "cpu_temp-n_attic"
	require.Contains(t, said, "rule "+id+": ")
	require.Contains(t, said, "takes effect on the next health read")
	find := func() (edge.Rule, bool) {
		rs, err := edgeAlertLog().Rules()
		require.NoError(t, err)
		for _, r := range rs {
			if r.ID == id {
				return r, true
			}
		}
		return edge.Rule{}, false
	}
	r, ok := find()
	require.True(t, ok, "the console's rule is the terminal's rule")
	require.Equal(t, "n_attic", r.Scope)
	require.Equal(t, 70.0, r.Value)
	require.NoError(t, w.RuleOff(id, true))
	r, _ = find()
	require.True(t, r.Off)
	require.NoError(t, w.RemoveRule(id))
	_, ok = find()
	require.False(t, ok)
	rules, _ = w.Rules()
	require.Len(t, rules, before)

	// Sinks: the URL goes in and never comes back out.
	sinks, err := w.Sinks()
	require.NoError(t, err)
	require.Empty(t, sinks)
	s, err := w.AddSink(string(edge.SinkWebhook), "http://192.168.1.50/hook/secret-token")
	require.NoError(t, err)
	sinks, err = w.Sinks()
	require.NoError(t, err)
	require.Len(t, sinks, 1)
	require.Empty(t, sinks[0].URL, "a read never carries the secret")
	require.NotContains(t, sinks[0].Words(), "secret-token")
	_, err = w.TestSink("sink_nope")
	require.Error(t, err)
	had, err := w.RemoveSink(s.ID)
	require.NoError(t, err)
	require.True(t, had)
	had, err = w.RemoveSink(s.ID)
	require.NoError(t, err)
	require.False(t, had)

	// Maintenance goes through the typed path: a node with no grant is refused before anything is sent.
	_, err = w.Maintain("ghost", edge.MaintainTask{Action: edge.MaintainRestart, Target: "roger"})
	require.Error(t, err)

	// The floor: the four values of the TUI hook in one struct, with the reads.
	f := w.Floor(time.Now())
	require.Empty(t, f.Calls)
	require.Zero(t, f.Rate)
	require.NotNil(t, f.Reads)

	// Events: a run of identical events is one line with its count.
	now := time.Now().Unix()
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, _, err := st.fleet.Get("n_attic")
	require.NoError(t, err)
	for i := int64(3); i > 0; i-- {
		n.History = append(n.History, store.EdgeEvent{At: now - i*60, What: "schedule", Detail: "restart roger"})
	}
	require.NoError(t, st.db.UpdateEdgeNode(n))
	require.NoError(t, st.save())
	evs := w.Events(time.Now(), 50)
	var line string
	for _, e := range evs {
		if e.What == "schedule" {
			line = e.Node + " " + e.Detail + " " + e.Age
		}
	}
	require.Contains(t, line, "attic restart roger")
	require.Contains(t, line, "×3 over 2m")
	require.True(t, strings.HasSuffix(line, " ago"))

	// Intent: what the owner expects, where the fleet differs, and what is left standing.
	in := w.Intent(time.Now())
	require.Empty(t, in.Expect)
	require.Empty(t, in.Schedule)
	require.NoError(t, edgeAlertLog().SetExpectation(edge.Expectation{What: edge.ExpectServes, Value: "tiny"}))
	_, err = captureEdgeStdout(func() error {
		return cmdEdgeSchedule(loadConfig(), []string{"add", "restart", "roger", "--scope", "attic", "--every", "24h"})
	})
	require.NoError(t, err)
	in = w.Intent(time.Now())
	require.Len(t, in.Expect, 1)
	require.Contains(t, in.Expect[0].Expected, "tiny")
	require.Equal(t, 1, in.Expect[0].Differ, "attic reports what it runs and does not serve tiny")
	var attic, me bool
	for _, d := range in.Drift {
		switch d.Node {
		case "attic":
			attic = !d.Unknown && d.Found == "does not serve it"
		case "desk":
			me = d.Unknown
		}
	}
	require.True(t, attic, "%+v", in.Drift)
	require.True(t, me, "a unit that has not reported is unknown, not wrong: %+v", in.Drift)
	require.Len(t, in.Schedule, 1)
	require.Equal(t, "never run", in.Schedule[0].Last)
	require.Equal(t, "attic", in.Schedule[0].Of)
	require.NotEqual(t, "-", in.Schedule[0].Next)
	_ = self
}
