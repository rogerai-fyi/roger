package main

// Executable pins for features/edge/logs.feature, the host and CLI halves: the tail as a maintenance
// action, the owner's switch in front of it, the redaction on the way out, and the arrow.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
)

func logsTask(lines int) json.RawMessage {
	b, _ := json.Marshal(edge.MaintainTask{Action: edge.MaintainLogs, Lines: lines})
	return b
}

func TestTheLogTailIsServedOnlyWhenMaintenanceIsOnHere(t *testing.T) {
	maintainRig(t)
	undo := edgeTeeLog()
	t.Cleanup(undo)
	log.Println("edge discovery: refused n_abc at 10.0.0.9:44121: unreachable")
	log.Println("starting share with key=hunter2 and ctx=90112")

	// Off by default: no lines, whatever the grant says.
	res, refused := edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", logsTask(0), time.Now())
	require.Equal(t, edge.MaintainOffReason, refused)
	require.Empty(t, res.Lines)

	_, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)

	res, refused = edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", logsTask(0), time.Now())
	require.Equal(t, "", refused)
	require.NotEmpty(t, res.Lines)
	require.Greater(t, res.Held, 0)
	require.Contains(t, res.Says, "secrets removed")

	joined := strings.Join(res.Lines, "\n")
	require.Contains(t, joined, "edge discovery: refused n_abc", "an ordinary line is readable")
	require.NotContains(t, joined, "hunter2", "the secret never left the process")
	require.Contains(t, joined, "[redacted]")

	// The read is on the machine's own history, and says it was a read, with no target.
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get(edgeThisNodeID())
	require.NoError(t, err)
	require.True(t, ok)
	found := false
	for _, e := range n.History {
		if e.What == "maintenance" && strings.Contains(e.Detail, "logs") {
			found = true
			require.Contains(t, e.Detail, "asked by desk")
			require.NotContains(t, e.Detail, "logs , ", "no target reads cleanly")
		}
	}
	require.True(t, found, "the read is on the history: %+v", n.History)
}

func TestTheLogTailClampsAndAnswersEmptily(t *testing.T) {
	maintainRig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)

	// Nothing logged yet on a fresh ring: an answer, not an error.
	fresh := edge.NewLogRing(0)
	lines, held := fresh.Tail(0)
	require.Empty(t, lines)
	require.Zero(t, held)

	undo := edgeTeeLog()
	t.Cleanup(undo)
	for i := 0; i < 30; i++ {
		log.Printf("line %d", i)
	}
	res, refused := edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", logsTask(5), time.Now())
	require.Equal(t, "", refused)
	require.Len(t, res.Lines, 5, "it asked for five")
	require.Contains(t, res.Lines[4], "line 29", "the newest five")

	res, refused = edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", logsTask(100000), time.Now())
	require.Equal(t, "", refused)
	require.LessOrEqual(t, len(res.Lines), edge.MaxLogLines, "never more than the ring holds")
}

func TestEdgeLogsCommand(t *testing.T) {
	maintainRig(t)
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	var sentTask json.RawMessage
	edgeDialInvokeTaskFn = func(_ context.Context, _ client.EdgePeer, _ *tls.Certificate, _ store.Grant, cap string, task json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		sentTask = task
		out, _ := json.Marshal(edge.MaintainResult{Action: edge.MaintainLogs, Held: 2,
			Lines: []string{"one line", "another with [redacted]"}, Says: "2 of 2 lines from shed, secrets removed"})
		return edge.InvokeResult{Output: out}, protocol.UsageReceipt{}, nil
	}

	// A node with no LAN face says that, rather than hinting at a grant.
	err := cmdEdgeLogs(loadConfig(), []string{"shed"})
	require.ErrorContains(t, err, "no LAN face")

	// Make it reachable, then the grant is what is missing, and it names the one to issue.
	st, err2 := loadEdgeState()
	require.NoError(t, err2)
	nodes, err2 := st.fleet.List()
	require.NoError(t, err2)
	for _, n := range nodes {
		if n.ID != edgeThisNodeID() {
			continue
		}
		n.Pin = "abab"
		n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:44198", Fingerprint: "abab"}}
		require.NoError(t, st.db.UpdateEdgeNode(n))
	}
	require.NoError(t, st.save())
	err = cmdEdgeLogs(loadConfig(), []string{"shed"})
	require.ErrorContains(t, err, "roger edge grant self maintain shed")

	// With a grant it prints the lines.
	_, _, err2 = edgeGrantIssueCore("self", "maintain", "shed", "1h", 0)
	require.NoError(t, err2)
	out, err := captureEdgeStdout(func() error { return cmdEdgeLogs(loadConfig(), []string{"shed", "--lines", "2"}) })
	require.NoError(t, err)
	require.Contains(t, out, "LOG · shed")
	require.Contains(t, out, "one line")
	require.Contains(t, out, "[redacted]")
	var task edge.MaintainTask
	require.NoError(t, json.Unmarshal(sentTask, &task))
	require.Equal(t, edge.MaintainLogs, task.Action)
	require.Equal(t, 2, task.Lines)

	// Refusals.
	err = cmdEdgeLogs(loadConfig(), []string{"shed", "--lines", "many"})
	require.Error(t, err)
	err = cmdEdgeLogs(loadConfig(), nil)
	require.Error(t, err)
}

// "." and "self" mean THIS machine wherever a node is named, so the trouble feed, an invoke, a
// maintenance action and a log tail cannot disagree about it.
func TestSelfAliasResolvesEverywhereANodeIsNamed(t *testing.T) {
	maintainRig(t)
	st, err := loadEdgeState()
	require.NoError(t, err)
	nodes, err := st.fleet.List()
	require.NoError(t, err)
	for _, n := range nodes {
		if n.ID != edgeThisNodeID() {
			continue
		}
		n.Pin = "abab"
		n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:44197", Fingerprint: "abab"}}
		require.NoError(t, st.db.UpdateEdgeNode(n))
	}
	require.NoError(t, st.save())

	for _, alias := range []string{".", "self", "shed", edgeThisNodeID()} {
		peer, id, err := edgeResolveNodeFace(alias)
		require.NoError(t, err, alias)
		require.Equal(t, edgeThisNodeID(), id, alias)
		require.Equal(t, "127.0.0.1:44197", peer.Addr, alias)
	}
	_, _, err = edgeResolveNodeFace("nosuch")
	require.Error(t, err)
}
