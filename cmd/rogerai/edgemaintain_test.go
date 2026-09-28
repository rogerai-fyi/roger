package main

// Executable pins for features/edge/maintenance.feature, the host half: the owner's switch, the
// action, the refusals, and the two locks that must BOTH be the owner's.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// maintainRig is a machine enrolled on its own local Edge with one persistent agent that is not
// running, and the launch recorded rather than performed.
func maintainRig(t *testing.T) *bool {
	t.Helper()
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0")
	_, err := edgeDesignateCore(edgeAuthDir(), "shed")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "shed", "", "")
	require.NoError(t, err)
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "watcher", timeNowForTest()))
	prev := edgeSpawnRoger
	t.Cleanup(func() { edgeSpawnRoger = prev })
	launched := false
	edgeSpawnRoger = func(map[string]string) error { launched = true; return nil }
	return &launched
}

func maintainTask(action edge.MaintainAction, target string) json.RawMessage {
	b, _ := json.Marshal(edge.MaintainTask{Action: action, Target: target})
	return b
}

func TestMaintenanceIsOffUntilTheOwnerTurnsItOnHere(t *testing.T) {
	launched := maintainRig(t)
	cfg := loadConfig()
	require.False(t, edgeMaintenanceOn(cfg), "off by default")

	// The capability is not declared, so no member is even authorized to ask.
	require.NotContains(t, edgeCapNames(edgeInstanceCaps(nil, edgeMaintenanceOn(cfg))), string(edge.Maintain))
	require.False(t, edgeSelfDeclares(edge.Maintain))

	// And the action itself refuses, grant or not.
	_, refused := edgeMaintainRun(cfg, "n_desk", "desk", "shed", maintainTask(edge.MaintainStart, "watcher"), time.Now())
	require.Equal(t, edge.MaintainOffReason, refused)
	require.False(t, *launched, "nothing was started")

	// The owner turns it on HERE.
	out, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)
	require.Contains(t, out, "maintenance is ON")
	require.Contains(t, out, "CANNOT run a command") // the consent text is pinned in full by TestTheMaintenanceSwitchSaysEverythingItGrants
	cfg = loadConfig()
	require.True(t, edgeMaintenanceOn(cfg))
	require.Contains(t, edgeCapNames(edgeInstanceCaps(nil, edgeMaintenanceOn(cfg))), string(edge.Maintain), "declared from the next registration")
	require.True(t, edgeSelfDeclares(edge.Maintain))

	// ...and off again.
	out, err = captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"off"}) })
	require.NoError(t, err)
	require.Contains(t, out, "maintenance is OFF")
	require.False(t, edgeSelfDeclares(edge.Maintain))

	// The switch with no argument says where it stands and what could be started.
	out, err = captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), nil) })
	require.NoError(t, err)
	require.Contains(t, out, "maintenance: OFF")
	require.Contains(t, out, "watcher")

	_, err = captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"sideways"}) })
	require.Error(t, err)
}

func edgeCapNames(caps []store.EdgeCap) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, c.Name)
	}
	return out
}

func TestMaintenanceStartsAnAgentThatIsDownAndRecordsWho(t *testing.T) {
	launched := maintainRig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)
	now := time.Now()

	res, refused := edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", maintainTask(edge.MaintainStart, "watcher"), now)
	require.Equal(t, "", refused)
	require.True(t, *launched)
	require.True(t, res.Changed)
	require.Equal(t, edge.MaintainStart, res.Action)
	require.Equal(t, "watcher", res.Target)
	require.Contains(t, res.Says, "watcher")

	// This machine's own history says what was done and who asked.
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get(edgeThisNodeID())
	require.NoError(t, err)
	require.True(t, ok)
	found := false
	for _, e := range n.History {
		if e.What == "maintenance" {
			found = true
			require.Contains(t, e.Detail, "start watcher")
			require.Contains(t, e.Detail, "asked by desk")
		}
	}
	require.True(t, found, "the action is on the node's history: %+v", n.History)

	// Already running: nothing is launched, and that is not an error.
	_, err = edge.Register(edgeInstancesDir(), edgeThisNodeID(), edge.Instance{Name: "watcher", Agent: true}, timeNowForTest())
	require.NoError(t, err)
	*launched = false
	res, refused = edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", maintainTask(edge.MaintainStart, "watcher"), now)
	require.Equal(t, "", refused)
	require.False(t, *launched)
	require.False(t, res.Changed)
	require.Contains(t, res.Says, "already running")
}

func TestMaintenanceRefusals(t *testing.T) {
	launched := maintainRig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)
	cfg, now := loadConfig(), time.Now()

	cases := []struct {
		name string
		task json.RawMessage
		self string
		says string
	}{
		{"unknown agent", maintainTask(edge.MaintainStart, "nosuch"), "shed", "no persistent agent"},
		{"itself", maintainTask(edge.MaintainStart, "watcher"), "watcher", "cannot start, stop or restart itself"},
		{"not an action", maintainTask("frobnicate", "watcher"), "shed", "is not a maintenance action"},
		{"no target", maintainTask(edge.MaintainStart, ""), "shed", "names the agent"},
		{"bad name", maintainTask(edge.MaintainStart, "not a name"), "shed", "not an agent name"},
		{"unreadable", json.RawMessage(`{`), "shed", "could not be read"},
		{"reboot", maintainTask(edge.MaintainReboot, "shed"), "shed", "not built yet"},
		{"shutdown", maintainTask(edge.MaintainShutdown, "shed"), "shed", "not built yet"},
		// stop and restart exist now, and their own refusals are pinned in edgestop_test.go. Here they
		// only have to refuse a target that is not a persistent agent, like start does.
		{"stop an unknown agent", maintainTask(edge.MaintainStop, "nosuch"), "shed", "no persistent agent"},
		{"restart an unknown agent", maintainTask(edge.MaintainRestart, "nosuch"), "shed", "no persistent agent"},
		{"stop itself", maintainTask(edge.MaintainStop, "shed"), "shed", "cannot start, stop or restart itself"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			*launched = false
			_, refused := edgeMaintainRun(cfg, "n_desk", "desk", c.self, c.task, now)
			require.Contains(t, refused, c.says)
			require.False(t, *launched, "a refusal never starts anything")
		})
	}
}

// THE SECOND LOCK, at the face: the callee checks the grant's capability scope itself, so a grant
// for operate cannot be spent on maintenance even by a caller who builds the request by hand.
func TestMaintenanceNeedsBothOfTheOwnersLocks(t *testing.T) {
	maintainRig(t)
	_, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)

	h, err := newEdgeHost("roger-shed")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	id, key, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	sv, ok := h.peerServing(key, id.NodeID)
	require.True(t, ok)
	require.NotNil(t, sv.Authorize)

	// Issue two real grants from this machine's authority: one for operate, one for maintain.
	operate, _, err := edgeGrantIssueCore("self", "operate", "shed", "1h", 0)
	require.NoError(t, err)
	maintain, _, err := edgeGrantIssueCore("self", "maintain", "shed", "1h", 0)
	require.NoError(t, err)

	require.ErrorContains(t, sv.Authorize(operate, id.NodeID, edge.Maintain), "does not allow maintain",
		"a grant for operate never reaches maintenance")
	require.NoError(t, sv.Authorize(maintain, id.NodeID, edge.Maintain))

	// With maintenance off, even the right grant is refused: the target's own switch is the other
	// lock - and the refusal SAYS so, with the command that turns it on, rather than "no verified
	// capability for this", which an operator cannot act on.
	_, err = captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"off"}) })
	require.NoError(t, err)
	err = sv.Authorize(maintain, id.NodeID, edge.Maintain)
	require.Error(t, err)
	require.Equal(t, edge.MaintainOffReason, err.Error())
	require.Contains(t, err.Error(), "roger edge maintenance on")
}

// One action at a time, and the runner routes maintain to the maintenance path rather than the
// agent-turn path.
func TestRunnerRoutesMaintenanceAndRunsOneAtATime(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	handler := edgeInvokeHandler(edgeTurnDeps{
		maintain: func(callerID string, task json.RawMessage) (edge.MaintainResult, string) {
			entered <- struct{}{}
			<-release
			return edge.MaintainResult{Action: edge.MaintainStart, Target: "watcher", Changed: true, Says: "launched"}, ""
		},
	})
	req := edge.InvokeRequest{Capability: string(edge.Maintain), Task: maintainTask(edge.MaintainStart, "watcher")}
	done := make(chan edge.InvokeResult, 1)
	go func() {
		res, _ := handler("n_desk", req)
		done <- res
	}()
	<-entered
	busy, err := handler("n_desk", req)
	require.NoError(t, err)
	require.Contains(t, busy.Refused, "already running on this machine")
	close(release)
	first := <-done
	require.Equal(t, "", first.Refused)
	var got edge.MaintainResult
	require.NoError(t, json.Unmarshal(first.Output, &got))
	require.Equal(t, "launched", got.Says)

	// A capability this roger is not is still named, not pretended.
	other, err := handler("n_desk", edge.InvokeRequest{Capability: string(edge.Actuate)})
	require.NoError(t, err)
	require.Contains(t, other.Refused, "it does not actuate")
}

// The CALLER side, `roger edge maintain <node> start <agent>`: an action that cannot mean what it
// says is refused before anything is sent, and a missing grant names the grant to issue.
func TestEdgeMaintainCommandRefusesBeforeItSends(t *testing.T) {
	maintainRig(t)
	sent := false
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	edgeDialInvokeTaskFn = func(context.Context, client.EdgePeer, *tls.Certificate, store.Grant, string, json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		sent = true
		return edge.InvokeResult{}, protocol.UsageReceipt{}, nil
	}

	// An action that is not built, and one that does not exist: refused here, nothing sent.
	err := cmdEdgeMaintain(loadConfig(), []string{"shed", "reboot", "shed"})
	require.ErrorContains(t, err, "not built yet")
	err = cmdEdgeMaintain(loadConfig(), []string{"shed", "frobnicate", "watcher"})
	require.ErrorContains(t, err, "is not a maintenance action")
	err = cmdEdgeMaintain(loadConfig(), []string{"shed", "start", "not a name"})
	require.ErrorContains(t, err, "not an agent name")
	require.False(t, sent, "a refusal never reaches the network")

	// Too few arguments is a usage error.
	err = cmdEdgeMaintain(loadConfig(), []string{"shed", "start"})
	require.Error(t, err)

	// A node that is not on this Edge is named as such.
	err = cmdEdgeMaintain(loadConfig(), []string{"nosuch", "start", "watcher"})
	require.Error(t, err)
	require.False(t, sent)

	// A node with no LAN face cannot be invoked at all, and says so rather than hinting at a grant.
	err = cmdEdgeMaintain(loadConfig(), []string{"shed", "start", "watcher"})
	require.ErrorContains(t, err, "no LAN face")
	require.False(t, sent)

	// Once it is reachable, the missing grant is what stands in the way, and it names the one to issue.
	st, err2 := loadEdgeState()
	require.NoError(t, err2)
	require.NoError(t, st.fleet.RepinNode(edgeThisNodeID(), "abab"))
	nodes, err2 := st.fleet.List()
	require.NoError(t, err2)
	for _, n := range nodes {
		if n.ID != edgeThisNodeID() {
			continue
		}
		n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:44199", Fingerprint: "abab"}}
		require.NoError(t, st.db.UpdateEdgeNode(n))
	}
	require.NoError(t, st.save())
	err = cmdEdgeMaintain(loadConfig(), []string{"shed", "start", "watcher"})
	require.ErrorContains(t, err, "roger edge grant self maintain shed")
	require.False(t, sent)
}
