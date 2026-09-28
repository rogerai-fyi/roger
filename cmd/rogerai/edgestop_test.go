package main

// Executable pins for features/edge/maintenance.feature, the stop and restart half: what may be
// signalled, what may never be, and that a restart says which half did what.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// stopRig is a machine with a persistent agent "watcher" REGISTERED as if it were running, with the
// signal and the roger check recorded rather than performed.
func stopRig(t *testing.T, pid int) (*[]int, *edge.Registry) {
	t.Helper()
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0")
	_, err := edgeDesignateCore(edgeAuthDir(), "shed")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "shed", "", "")
	require.NoError(t, err)
	now := timeNowForTest()
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "watcher", now))
	reg, err := edge.Register(edgeInstancesDir(), edgeThisNodeID(),
		edge.Instance{Name: "watcher", Agent: true, PID: pid}, now)
	require.NoError(t, err)

	signalled := []int{}
	prevSig, prevProc, prevSpawn := edgeSignal, edgeProcIsRoger, edgeSpawnRoger
	t.Cleanup(func() { edgeSignal, edgeProcIsRoger, edgeSpawnRoger = prevSig, prevProc, prevSpawn })
	edgeSignal = func(pid int, sig syscall.Signal) error {
		require.Equal(t, syscall.SIGTERM, sig, "SIGTERM, never SIGKILL")
		signalled = append(signalled, pid)
		return nil
	}
	edgeProcIsRoger = func(int) (bool, bool) { return true, true }
	edgeSpawnRoger = func(map[string]string) error { return nil }
	return &signalled, reg
}

func TestStopSignalsARunningAgentWithSIGTERM(t *testing.T) {
	signalled, reg := stopRig(t, 424242)
	says, changed, refused := edgeStopAgent("watcher", time.Now())
	require.Equal(t, "", refused)
	require.True(t, changed)
	require.Equal(t, []int{424242}, *signalled)
	// The fake process never leaves the household, so the answer says exactly that, and says why it
	// is not forced.
	require.Contains(t, says, "had not gone yet")
	require.Contains(t, says, "SIGTERM, never SIGKILL")

	// Once it has gone, the same ask is not an error and signals nothing more.
	require.NoError(t, reg.Deregister())
	*signalled = nil
	says, changed, refused = edgeStopAgent("watcher", time.Now())
	require.Equal(t, "", refused)
	require.False(t, changed)
	require.Contains(t, says, "not running")
	require.Empty(t, *signalled)
}

func TestStopNeverSignalsAProcessItCannotVouchFor(t *testing.T) {
	t.Run("stale registration", func(t *testing.T) {
		signalled, reg := stopRig(t, 424242)
		// Age the registration past the reap window: the pid may be somebody else's now.
		old := time.Now().Add(-2 * edge.InstanceLife)
		require.NoError(t, os.Chtimes(regPath(t, reg), old, old))
		says, changed, refused := edgeStopAgent("watcher", time.Now())
		require.False(t, changed)
		require.Equal(t, "", refused)
		require.Contains(t, says, "not running",
			"a process that stopped beating reads as gone here, exactly as the fleet draws it DARK")
		require.Empty(t, *signalled, "nothing is signalled on a pid nobody can vouch for")
	})

	t.Run("its own pid", func(t *testing.T) {
		signalled, _ := stopRig(t, os.Getpid())
		_, changed, refused := edgeStopAgent("watcher", time.Now())
		require.False(t, changed)
		require.Contains(t, refused, "cannot stop itself")
		require.Empty(t, *signalled)
	})

	t.Run("not a roger any more", func(t *testing.T) {
		signalled, _ := stopRig(t, 424242)
		edgeProcIsRoger = func(int) (bool, bool) { return false, true }
		_, changed, refused := edgeStopAgent("watcher", time.Now())
		require.False(t, changed)
		require.Contains(t, refused, "not a roger any more")
		require.Empty(t, *signalled)
	})

	t.Run("no pid recorded", func(t *testing.T) {
		signalled, _ := stopRig(t, 0)
		_, changed, refused := edgeStopAgent("watcher", time.Now())
		require.False(t, changed)
		require.Contains(t, refused, "too old to record its process")
		require.Empty(t, *signalled)
	})

	t.Run("not persistent", func(t *testing.T) {
		signalled, _ := stopRig(t, 424242)
		_, err := edge.UnmarkPersistent(edgePersistDir(), "watcher")
		require.NoError(t, err)
		_, changed, refused := edgeStopAgent("watcher", time.Now())
		require.False(t, changed)
		require.Contains(t, refused, "no persistent agent")
		require.Empty(t, *signalled)
	})
}

// regPath is where a registration was written, so a test can age it.
func regPath(t *testing.T, reg *edge.Registry) string {
	t.Helper()
	entries, err := os.ReadDir(edgeInstancesDir())
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	return edgeInstancesDir() + "/" + entries[0].Name()
}

func TestRestartSaysWhichHalfDidWhat(t *testing.T) {
	signalled, reg := stopRig(t, 424242)
	launched := 0
	edgeSpawnRoger = func(map[string]string) error { launched++; return nil }

	// Running, and it does not go: it is NOT started on top of itself, and the answer says so rather
	// than reporting a restart that did not happen.
	says, changed, refused := edgeRestartAgent(loadConfig(), "watcher", time.Now())
	require.Equal(t, "", refused)
	require.True(t, changed)
	require.Equal(t, []int{424242}, *signalled)
	require.Contains(t, says, "asked \"watcher\" to stop")
	require.Contains(t, says, "NOT started again")
	require.Zero(t, launched, "nothing is launched on top of a roger that is still there")

	// Gone: the stop took, and the start half runs.
	require.NoError(t, reg.Deregister())
	*signalled, launched = nil, 0
	says, changed, refused = edgeRestartAgent(loadConfig(), "watcher", time.Now())
	require.Equal(t, "", refused)
	require.True(t, changed)
	require.Empty(t, *signalled)
	require.Equal(t, 1, launched)
	require.Contains(t, says, "launched a roger")

	// A stop that refuses stops the whole thing: nothing is launched on top of a live process.
	_, reg2 := stopRig(t, os.Getpid())
	_ = reg2
	launched = 0
	_, _, refused = edgeRestartAgent(loadConfig(), "watcher", time.Now())
	require.Contains(t, refused, "cannot stop itself")
	require.Zero(t, launched, "nothing is launched on top of a process that is still there")
}

// The whole way in: stop and restart are refused unless BOTH of the owner's locks are open, and
// reboot and shutdown are still refused by name.
func TestStopAndRestartGoThroughTheMaintenanceGate(t *testing.T) {
	signalled, _ := stopRig(t, 424242)
	task := func(a edge.MaintainAction) json.RawMessage {
		b, _ := json.Marshal(edge.MaintainTask{Action: a, Target: "watcher"})
		return b
	}
	now := time.Now()

	// Maintenance off: refused, nothing signalled.
	for _, a := range []edge.MaintainAction{edge.MaintainStop, edge.MaintainRestart} {
		_, refused := edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", task(a), now)
		require.Equal(t, edge.MaintainOffReason, refused, string(a))
	}
	require.Empty(t, *signalled)

	_, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)

	res, refused := edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", task(edge.MaintainStop), now)
	require.Equal(t, "", refused)
	require.True(t, res.Changed)
	require.Equal(t, []int{424242}, *signalled)

	// The action is on the machine's own history, naming who asked.
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get(edgeThisNodeID())
	require.NoError(t, err)
	require.True(t, ok)
	found := false
	for _, e := range n.History {
		if e.What == "maintenance" && strings.Contains(e.Detail, "stop watcher") {
			found = true
			require.Contains(t, e.Detail, "asked by desk")
		}
	}
	require.True(t, found, "the stop is on the history: %+v", n.History)

	// Reboot and shutdown are still refused BY NAME.
	for _, a := range []edge.MaintainAction{edge.MaintainReboot, edge.MaintainShutdown} {
		_, refused := edgeMaintainRun(loadConfig(), "n_desk", "desk", "shed", task(a), now)
		require.Contains(t, refused, "not built yet", string(a))
	}
}

// The roger that may never be a target is the one THIS process REGISTERED - not the name a new roger
// would take. Guessing it from the config refused a start for the very agent the owner named.
func TestTheSelfTargetIsTheRegisteredNameNotAGuess(t *testing.T) {
	signalled, _ := stopRig(t, 424242)
	deps := edgeLiveTurnDeps(func() string { return "roger" }) // what this process registered
	handler := edgeInvokeHandler(deps)
	_, err := captureEdgeStdout(func() error { return cmdEdgeMaintenance(loadConfig(), []string{"on"}) })
	require.NoError(t, err)

	ask := func(action edge.MaintainAction, target string) edge.InvokeResult {
		b, _ := json.Marshal(edge.MaintainTask{Action: action, Target: target})
		res, err := handler("n_desk", edge.InvokeRequest{Capability: string(edge.Maintain), Task: b})
		require.NoError(t, err)
		return res
	}

	// "watcher" is NOT this process, so it may be acted on.
	res := ask(edge.MaintainStop, "watcher")
	require.Equal(t, "", res.Refused)
	require.Equal(t, []int{424242}, *signalled)

	// The registered name is refused, whichever action asks.
	for _, a := range edge.MaintainActs {
		res = ask(a, "roger")
		require.Contains(t, res.Refused, "cannot start, stop or restart itself", string(a))
		require.Contains(t, res.Refused, "several rogers", "the refusal says why it can happen")
		require.Contains(t, res.Refused, "<node>/<roger>", "and names the address that does reach it")
	}

	// A process that registered nothing is nobody's target: the check cannot match an empty name.
	none := edgeInvokeHandler(edgeLiveTurnDeps(func() string { return "" }))
	b, _ := json.Marshal(edge.MaintainTask{Action: edge.MaintainStop, Target: "watcher"})
	res, err = none("n_desk", edge.InvokeRequest{Capability: string(edge.Maintain), Task: b})
	require.NoError(t, err)
	require.Equal(t, "", res.Refused)
}

// A start says whether the agent really JOINED, not merely that it was launched: a spawned roger
// that cannot stand up its face exits at once (features/edge/maintenance.feature).
func TestStartSaysWhetherTheAgentReallyJoined(t *testing.T) {
	_, reg := stopRig(t, 424242)
	require.NoError(t, reg.Deregister()) // "watcher" is marked persistent and not running

	// It joins: the answer says so.
	edgeSpawnRoger = func(map[string]string) error {
		_, err := edge.Register(edgeInstancesDir(), edgeThisNodeID(),
			edge.Instance{Name: "watcher", Agent: true, PID: 515151}, time.Now())
		return err
	}
	says, changed, refused := edgeStartAgent(loadConfig(), "watcher", time.Now())
	require.Equal(t, "", refused)
	require.True(t, changed)
	require.Contains(t, says, "it is on your Edge")

	// Already running: nothing is launched, and that is not an error.
	launched := 0
	edgeSpawnRoger = func(map[string]string) error { launched++; return nil }
	says, changed, refused = edgeStartAgent(loadConfig(), "watcher", time.Now())
	require.Equal(t, "", refused)
	require.False(t, changed)
	require.Contains(t, says, "already running")
	require.Zero(t, launched)

	// It dies at once: the answer says it did not join, and where to look.
	for _, in := range edge.LiveHousehold(edgeInstancesDir(), time.Now()) {
		_ = in
	}
	entries, err := os.ReadDir(edgeInstancesDir())
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, os.Remove(edgeInstancesDir()+"/"+e.Name()))
	}
	says, changed, refused = edgeStartAgent(loadConfig(), "watcher", time.Now())
	require.Equal(t, "", refused)
	require.True(t, changed, "a roger was launched, even though it did not stay")
	require.Contains(t, says, "had not joined your Edge")
	require.Contains(t, says, "roger edge logs")
}

// A spawned agent gets its OWN face: inheriting a pinned face port guarantees it cannot bind and
// exits at once, which is what happened on a machine running with the port pinned.
func TestASpawnedAgentDoesNotInheritThePinnedFacePort(t *testing.T) {
	useTempConfig(t)
	t.Setenv(envFaceBind, ":44121")
	t.Setenv("ROGER_EDGE_KEEP", "yes-please")
	var gotEnv []string
	prevDetach := edgeDetachFn
	t.Cleanup(func() { edgeDetachFn = prevDetach })
	edgeDetachFn = func(c *exec.Cmd) error { gotEnv = c.Env; return errStubbedDetach }

	err := edgeSpawnRoger(map[string]string{"ROGER_EDGE_AGENT": "1", "ROGER_EDGE_INSTANCE": "watcher"})
	require.ErrorIs(t, err, errStubbedDetach)
	joined := strings.Join(gotEnv, "\n")
	require.NotContains(t, joined, envFaceBind+"=", "the child must not inherit the pinned face port")
	require.Contains(t, joined, "ROGER_EDGE_KEEP=yes-please", "every other variable is passed through")
	require.Contains(t, joined, "ROGER_EDGE_INSTANCE=watcher")
	require.Contains(t, joined, "ROGER_EDGE_AGENT=1")
}

var errStubbedDetach = errors.New("detach stubbed out")

// node/instance addresses ONE roger rather than the machine, because a node's recorded face is
// whichever roger observed itself last (features/edge/maintenance.feature).
func TestAnInstanceAddressDialsThatRogersOwnFace(t *testing.T) {
	stopRig(t, 424242)
	st, err := loadEdgeState()
	require.NoError(t, err)
	id := edgeThisNodeID()
	nodes, err := st.fleet.List()
	require.NoError(t, err)
	for _, n := range nodes {
		if n.ID != id {
			continue
		}
		n.Pin = "abab"
		n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.5:44121", Fingerprint: "abab"}}
		n.Instances = []edge.Instance{
			{Name: "roger", Port: 44121},
			{Name: "roger-3", Port: 51000},
			{Name: "ancient"}, // written before instance addressing: no face of its own
		}
		require.NoError(t, st.db.UpdateEdgeNode(n))
	}
	require.NoError(t, st.save())

	// The machine: its recorded face.
	peer, gotID, err := edgeResolveNodeFace("shed")
	require.NoError(t, err)
	require.Equal(t, id, gotID)
	require.Equal(t, "10.0.0.5:44121", peer.Addr)
	require.Equal(t, "", peer.Instance)

	// One roger on it: the node's host, that roger's port, and the NODE's pin - nothing new trusted.
	peer, gotID, err = edgeResolveNodeFace("shed/roger-3")
	require.NoError(t, err)
	require.Equal(t, id, gotID)
	require.Equal(t, "10.0.0.5:51000", peer.Addr)
	require.Equal(t, "abab", peer.Pin)
	require.Equal(t, "roger-3", peer.Instance)

	// A roger the node does not run, and one with no face of its own.
	_, _, err = edgeResolveNodeFace("shed/nosuch")
	require.ErrorContains(t, err, "runs no roger called")
	require.ErrorContains(t, err, "roger edge describe")
	_, _, err = edgeResolveNodeFace("shed/ancient")
	require.ErrorContains(t, err, "reports no face of its own")
	require.ErrorContains(t, err, "ask shed instead")

	// "." still means this machine, and it means it as the NODE half of an instance address too -
	// "./roger" is how an owner addresses one roger on the machine they are sitting at.
	peer, _, err = edgeResolveNodeFace(".")
	require.NoError(t, err)
	require.Equal(t, "10.0.0.5:44121", peer.Addr)
	peer, _, err = edgeResolveNodeFace("./roger-3")
	require.NoError(t, err)
	require.Equal(t, "10.0.0.5:51000", peer.Addr)
	require.Equal(t, "roger-3", peer.Instance)
	peer, _, err = edgeResolveNodeFace("self/roger")
	require.NoError(t, err)
	require.Equal(t, "10.0.0.5:44121", peer.Addr)
}

// The host records its OWN face port, which is what makes the address above dialable.
func TestTheHostRecordsItsOwnFacePort(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	require.Zero(t, h.facePort(), "before the face is up there is no port to report")
	_, ok := h.startFace()
	require.True(t, ok)
	port := h.facePort()
	require.Greater(t, port, 0)

	h.registerInstance(h.cfg)
	require.NotNil(t, h.reg)
	name := h.reg.Instance().Name
	found := false
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		if in.Name == name {
			found = true
			require.Equal(t, port, in.Port, "its own face, on its own registration")
			require.Equal(t, os.Getpid(), in.PID)
		}
	}
	require.True(t, found)

	// And a beat keeps it, so a registration written before the face was up gains it.
	require.NoError(t, h.reg.Update(edgeThisNodeID(), func(i *edge.Instance) { i.Port = 0 }))
	h.beatInstance(h.cfg)
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		if in.Name == name {
			require.Equal(t, port, in.Port, "the beat restores it")
		}
	}
}
