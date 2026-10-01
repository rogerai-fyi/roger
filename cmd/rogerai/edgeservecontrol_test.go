package main

// features/edge/remote_model.feature: changing what a member serves from across the Edge. The member
// side is driven directly (the face's certificate and grant checks are the invoke path's own, tested
// with maintenance); the caller side through its seam.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/runtime"
	"rogerai.fm/roger/v6/internal/store"
)

type serveRig struct {
	*modelRig
	spawned [][]string
}

func newServeRig(t *testing.T, ramMiB int) *serveRig {
	t.Helper()
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0")
	_, err := edgeDesignateCore(edgeAuthDir(), "jetson")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "jetson", "", "")
	require.NoError(t, err)
	r := &serveRig{modelRig: newModelRig(t, ramMiB)}
	oldSpawn, oldHost := modelSpawn, modelHost
	t.Cleanup(func() { modelSpawn, modelHost = oldSpawn, oldHost; modelBudgetCache.at = time.Time{} })
	modelBudgetCache.at = time.Time{}
	modelHost = func() runtime.Host { return runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42} }
	modelSpawn = func(args []string, logPath string) error { r.spawned = append(r.spawned, args); return nil }
	return r
}

func serveTask(model string, ctx int) json.RawMessage {
	b, _ := json.Marshal(edge.ModelTask{Action: edge.ModelServe, Model: model, Ctx: ctx})
	return b
}

func turnOn(t *testing.T) config {
	t.Helper()
	cfg := loadConfig()
	cfg.EdgeServeControl = true
	require.NoError(t, saveConfig(cfg))
	return loadConfig()
}

func TestAMemberThatNeverTurnedServeControlOnRefusesAndNothingMoves(t *testing.T) {
	r := newServeRig(t, 16384)
	cfg := loadConfig()
	require.False(t, edgeServeControlOn(cfg), "off by default")
	require.False(t, edgeSelfDeclares(edge.ServeControl))
	require.NotContains(t, edgeCapNames(edgeInstanceCapsFor(cfg, nil)), string(edge.ServeControl))

	_, refused := edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Tiny-GGUF:Q4_K_M", 0), time.Now())
	require.Equal(t, edge.ServeControlOffReason, refused)
	require.Contains(t, refused, "roger edge serve-control on", "the refusal names the switch and where it lives")
	require.Empty(t, r.spawned)
	_, ok := r.home.Job()
	require.False(t, ok, "nothing was fetched")

	// The owner turns it on HERE, and the capability is declared from then on.
	out, err := captureEdgeStdout(func() error { return cmdEdgeServeControl(loadConfig(), []string{"on"}) })
	require.NoError(t, err)
	require.Contains(t, out, "serve-control is ON")
	require.Contains(t, out, "never takes an address, a path or a program", "the owner is told what they consent to")
	require.True(t, edgeSelfDeclares(edge.ServeControl))
	require.Contains(t, edgeCapNames(edgeInstanceCapsFor(loadConfig(), nil)), string(edge.ServeControl))
	out, _ = captureEdgeStdout(func() error { return cmdEdgeServeControl(loadConfig(), nil) })
	require.Contains(t, out, "ON for this machine")
	out, _ = captureEdgeStdout(func() error { return cmdEdgeServeControl(loadConfig(), []string{"off"}) })
	require.Contains(t, out, "No member can change what it serves")
	require.Error(t, cmdEdgeServeControl(loadConfig(), []string{"maybe"}))
}

func TestTheWireHasNowhereToPutAnAddressEvenForAnAuthorisedCaller(t *testing.T) {
	r := newServeRig(t, 16384)
	cfg := turnOn(t)
	for _, raw := range []string{
		`{"action":"serve","model":"acme/Tiny-GGUF:Q4_K_M","url":"http://evil.example/x.gguf"}`,
		`{"action":"serve","model":"/tmp/x.gguf"}`,
		`{"action":"serve","model":"x","runtime":"/bin/sh"}`,
	} {
		_, refused := edgeModelRun(cfg, "n_desk", "desk", json.RawMessage(raw), time.Now())
		require.NotEmpty(t, refused, raw)
	}
	require.Empty(t, r.spawned)
	_, ok := r.home.Job()
	require.False(t, ok, "the member fetched nothing")
}

func TestAModelTheMemberDoesNotHaveIsFetchedOnlyByExactID(t *testing.T) {
	r := newServeRig(t, 16384)
	cfg := turnOn(t)

	// A bare name, a partial id, free text: refused, and no request reaches the index.
	for _, m := range []string{"tiny", "acme/Tiny-GGUF", "qwen3"} {
		_, refused := edgeModelRun(cfg, "n_desk", "desk", serveTask(m, 0), time.Now())
		require.Contains(t, refused, "searches nowhere", m)
	}
	_, ok := r.home.Job()
	require.False(t, ok)

	// The exact id: judged HERE, fetched from this machine's own source, then served once checked.
	res, refused := edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Tiny-GGUF:Q4_K_M", 4096), time.Now())
	require.Empty(t, refused)
	require.True(t, res.Changed)
	require.Contains(t, res.Says, "fits at 4K")
	require.Contains(t, res.Says, "fetching Tiny-Q4_K_M.gguf")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(r.spawned) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	require.Equal(t, [][]string{{"model", "serve", "acme/Tiny-GGUF:Q4_K_M", "--yes", "--ctx", "4096"}}, r.spawned,
		"it serves what it fetched, at the context it judged")
	m, err := r.home.Find("acme/Tiny-GGUF:Q4_K_M")
	require.NoError(t, err)
	require.True(t, m.Verified())

	// Its own history says what was asked and by whom.
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get(edgeThisNodeID())
	require.NoError(t, err)
	require.True(t, ok)
	var found bool
	for _, e := range n.History {
		if e.What == "model" && strings.Contains(e.Detail, "serve acme/Tiny-GGUF:Q4_K_M, asked by desk") {
			found = true
		}
	}
	require.True(t, found, "the change is on the node's history: %+v", n.History)

	_, refused = edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Tiny-GGUF:Q5_K_M", 0), time.Now())
	require.Contains(t, refused, "no servable Q5_K_M")
	_, refused = edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Locked-GGUF:Q4_K_M", 0), time.Now())
	require.Contains(t, refused, "HF_TOKEN")
}

func TestTheMembersOwnArithmeticDecides(t *testing.T) {
	r := newServeRig(t, 512)
	cfg := turnOn(t)
	_, refused := edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Tiny-GGUF:Q4_K_M", 40960), time.Now())
	require.Contains(t, refused, "this machine's own arithmetic")
	require.Contains(t, refused, "will not fit")
	require.Empty(t, r.spawned)
	_, ok := r.home.Job()
	require.False(t, ok, "a model that will not fit is never fetched")

	// A machine whose memory cannot be read does not fetch unasked.
	modelHW = func() detect.LocalHW { return detect.LocalHW{} }
	modelBudgetCache.at = time.Time{}
	_, refused = edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Tiny-GGUF:Q4_K_M", 0), time.Now())
	require.Contains(t, refused, "cannot judge it")
}

func TestAModelOnTheMembersDiskIsServedAndTheOldOneKeepsServing(t *testing.T) {
	r := newServeRig(t, 16384)
	cfg := turnOn(t)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Band: "older", ID: "x/older:Q8_0", Started: time.Now()})
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "serving.json"), b, 0o600))

	res, refused := edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Tiny-GGUF:Q4_K_M", 0), time.Now())
	require.Empty(t, refused)
	require.Contains(t, res.Says, "x/older:Q8_0 keeps serving until then")
	require.Len(t, r.spawned, 1)
	require.NotNil(t, res.Now)
	require.Equal(t, "older", res.Now.Band, "it still says what it serves NOW")

	// Status and stop.
	sb, _ := json.Marshal(edge.ModelTask{Action: edge.ModelStatus})
	res, refused = edgeModelRun(cfg, "n_desk", "desk", sb, time.Now())
	require.Empty(t, refused)
	require.Equal(t, "older", res.Now.Band)
	oldSig, oldIs := edgeSignal, edgeProcIsRoger
	t.Cleanup(func() { edgeSignal, edgeProcIsRoger = oldSig, oldIs })
	edgeProcIsRoger = func(int) (bool, bool) { return true, true }
	rec := filepath.Join(r.home.Dir, "serving.json")
	edgeSignal = func(int, syscall.Signal) error { return os.Remove(rec) }
	stop, _ := json.Marshal(edge.ModelTask{Action: edge.ModelStop})
	res, refused = edgeModelRun(cfg, "n_desk", "desk", stop, time.Now())
	require.Empty(t, refused)
	require.True(t, res.Changed)
	res, _ = edgeModelRun(cfg, "n_desk", "desk", stop, time.Now())
	require.Contains(t, res.Says, "nothing to stop")

	// A machine with no runtime says so, in its own words.
	modelHost = func() runtime.Host { return runtime.Host{OS: "linux", Arch: "arm"} }
	_, refused = edgeModelRun(cfg, "n_desk", "desk", serveTask("acme/Tiny-GGUF:Q4_K_M", 0), time.Now())
	require.Contains(t, refused, "not available here")
	require.Contains(t, edgeModelNow().CannotServe, "not available here")
}

func TestTheFleetSeesWhatAMemberServesAndFetches(t *testing.T) {
	r := newServeRig(t, 16384)
	require.Nil(t, edgeModelNow(), "a roger serving nothing of its own reports nothing")
	j, _ := json.Marshal(localmodel.Job{Repo: "acme/Tiny-GGUF", File: "Tiny-Q8_0.gguf", Total: 4 << 30, Done: 1 << 30,
		State: localmodel.JobFetching, PID: os.Getpid()})
	require.NoError(t, os.MkdirAll(r.home.Dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "fetch.json"), j, 0o644))
	b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Ctx: 8192, Started: time.Now()})
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "serving.json"), b, 0o600))
	now := edgeModelNow()
	require.NotNil(t, now)
	line := edgeModelNowLine(now)
	require.Contains(t, line, `serving "tiny"`)
	require.Contains(t, line, "fetching Tiny-Q8_0.gguf: 1.0 GiB of 4.0 GiB", "a fetch shows while it still names what it serves NOW")

	now.Fetch.State = localmodel.JobInterrupted
	require.Contains(t, edgeModelNowLine(now), "never served half-done")
	now.Fetch.State, now.Fetch.Err = localmodel.JobFailed, "not enough free disk"
	require.Contains(t, edgeModelNowLine(now), "failed: not enough free disk")
	require.Equal(t, "serves nothing of its own", edgeModelNowLine(nil))
}

func TestTheSameCommandWorksAcrossTheEdgeByNamingTheNode(t *testing.T) {
	r := newServeRig(t, 16384)
	old := modelEdgeInvoke
	t.Cleanup(func() { modelEdgeInvoke = old })
	var sent []edge.ModelTask
	modelEdgeInvoke = func(cfg config, node string, task edge.ModelTask) (edge.ModelResult, string, error) {
		sent = append(sent, task)
		if node == "attic" {
			return edge.ModelResult{}, node, errors.New("no live grant lets desk change what attic serves - issue one on the authority with `roger edge grant self serve-control attic`")
		}
		return edge.ModelResult{Says: "starting", Now: &store.EdgeModel{Band: "tiny", ID: task.Model, Ctx: 8192}}, "jetson", nil
	}
	out, err := r.run(t, "serve", "acme/Tiny-GGUF:Q4_K_M", "--on", "jetson", "--ctx", "8192")
	require.NoError(t, err)
	require.Contains(t, out, "jetson: starting")
	require.Contains(t, out, `jetson serving "tiny"`)
	require.Equal(t, edge.ModelTask{Action: edge.ModelServe, Model: "acme/Tiny-GGUF:Q4_K_M", Ctx: 8192}, sent[0])

	_, err = r.run(t, "status", "--on=jetson")
	require.NoError(t, err)
	_, err = r.run(t, "stop", "--on", "jetson")
	require.NoError(t, err)
	require.Equal(t, edge.ModelStatus, sent[1].Action)
	require.Equal(t, edge.ModelStop, sent[2].Action)

	_, err = r.run(t, "serve", "x", "--on", "attic")
	require.ErrorContains(t, err, "roger edge grant self serve-control attic")
	_, err = r.run(t, "rm", "x", "--on", "jetson")
	require.ErrorContains(t, err, "on a member: serve, status, stop")
	_, err = r.run(t, "serve", "--on", "jetson")
	require.ErrorContains(t, err, "usage")
}

func TestTheCallerRefusesBeforeSendingWhatTheMemberWouldRefuse(t *testing.T) {
	newServeRig(t, 16384)
	_, _, err := edgeModelInvoke(loadConfig(), "jetson", edge.ModelTask{Action: edge.ModelServe, Model: "/tmp/x.gguf"})
	require.ErrorContains(t, err, "not a model id")
	_, _, err = edgeModelInvoke(loadConfig(), "nobody-here", edge.ModelTask{Action: edge.ModelStatus})
	require.Error(t, err)
}

// The CALLER side end to end through the real grant store: no face says so, no grant names the one to
// issue, a grant carries the task as the member will read it, and a member's refusal reaches the owner.
func TestServeControlFromTheCallerUsesItsOwnGrant(t *testing.T) {
	newServeRig(t, 16384)
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	var sentCap string
	var sentTask json.RawMessage
	refuse := ""
	edgeDialInvokeTaskFn = func(_ context.Context, _ client.EdgePeer, _ *tls.Certificate, _ store.Grant, capability string, task json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		sentCap, sentTask = capability, task
		if refuse != "" {
			return edge.InvokeResult{Refused: refuse}, protocol.UsageReceipt{}, nil
		}
		out, _ := json.Marshal(edge.ModelResult{Action: edge.ModelServe, Changed: true, Says: "fetching",
			Now: &store.EdgeModel{Fetch: &store.EdgeFetch{File: "Tiny-Q4_K_M.gguf", Done: 1, Total: 4, State: localmodel.JobFetching}}})
		return edge.InvokeResult{Output: out}, protocol.UsageReceipt{}, nil
	}
	task := edge.ModelTask{Action: edge.ModelServe, Model: "acme/Tiny-GGUF:Q4_K_M"}
	_, _, err := edgeModelInvoke(loadConfig(), "jetson", task)
	require.ErrorContains(t, err, "no LAN face")

	st, err := loadEdgeState()
	require.NoError(t, err)
	nodes, err := st.fleet.List()
	require.NoError(t, err)
	for _, n := range nodes {
		if n.ID == edgeThisNodeID() {
			n.Pin = "abab"
			n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:44197", Fingerprint: "abab"}}
			require.NoError(t, st.db.UpdateEdgeNode(n))
		}
	}
	require.NoError(t, st.save())
	_, _, err = edgeModelInvoke(loadConfig(), "jetson", task)
	require.ErrorContains(t, err, "roger edge grant self serve-control jetson")
	require.Empty(t, sentCap, "nothing was sent without a grant")

	// A maintain grant does not reach serve-control.
	_, _, err = edgeGrantIssueCore("self", "maintain", "jetson", "1h", 0)
	require.NoError(t, err)
	_, _, err = edgeModelInvoke(loadConfig(), "jetson", task)
	require.ErrorContains(t, err, "serve-control")

	_, _, err = edgeGrantIssueCore("self", "serve-control", "jetson", "1h", 0)
	require.NoError(t, err)
	res, name, err := edgeModelInvoke(loadConfig(), "jetson", task)
	require.NoError(t, err)
	require.Equal(t, "jetson", name)
	require.Equal(t, string(edge.ServeControl), sentCap)
	got, refused := edge.ParseModelTask(sentTask)
	require.Empty(t, refused, "what was sent is what the member reads")
	require.Equal(t, task, got)
	require.Contains(t, edgeModelNowLine(res.Now), "fetching Tiny-Q4_K_M.gguf")

	refuse = edge.ServeControlOffReason
	_, _, err = edgeModelInvoke(loadConfig(), "jetson", task)
	require.ErrorContains(t, err, "jetson refused: remote serving control is off")
}

// THE TWO LOCKS at the real face: a grant for anything else never reaches serve-control, the right
// grant is refused while the owner's switch is off (saying so), and passes once it is on.
func TestServeControlNeedsBothOfTheOwnersLocksAtTheFace(t *testing.T) {
	newServeRig(t, 16384)
	h, err := newEdgeHost("roger-jetson")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	id, key, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	sv, ok := h.peerServing(key, id.NodeID)
	require.True(t, ok)

	maintain, _, err := edgeGrantIssueCore("self", "maintain", "jetson", "1h", 0)
	require.NoError(t, err)
	serve, _, err := edgeGrantIssueCore("self", "serve-control", "jetson", "1h", 0)
	require.NoError(t, err)

	require.ErrorContains(t, sv.Authorize(maintain, id.NodeID, edge.ServeControl), "does not allow serve-control")
	err = sv.Authorize(serve, id.NodeID, edge.ServeControl)
	require.Error(t, err, "the switch alone is the other lock")
	require.Equal(t, edge.ServeControlOffReason, err.Error())

	turnOn(t)
	require.NoError(t, sv.Authorize(serve, id.NodeID, edge.ServeControl))
}

// The runner routes serve-control to the model path, one change at a time.
func TestRunnerRoutesServeControlOneAtATime(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	handler := edgeInvokeHandler(edgeTurnDeps{
		models: func(callerID string, task json.RawMessage) (edge.ModelResult, string) {
			if callerID == "n_refused" {
				return edge.ModelResult{}, edge.ServeControlOffReason
			}
			entered <- struct{}{}
			<-release
			return edge.ModelResult{Action: edge.ModelServe, Changed: true, Says: "starting"}, ""
		},
	})
	req := edge.InvokeRequest{Capability: string(edge.ServeControl), Task: serveTask("acme/Tiny-GGUF:Q4_K_M", 0)}
	done := make(chan edge.InvokeResult, 1)
	go func() {
		res, _ := handler("n_desk", req)
		done <- res
	}()
	<-entered
	busy, err := handler("n_desk", req)
	require.NoError(t, err)
	require.Contains(t, busy.Refused, "already being made on this machine")
	close(release)
	first := <-done
	var got edge.ModelResult
	require.NoError(t, json.Unmarshal(first.Output, &got))
	require.Equal(t, "starting", got.Says)
	refused, err := handler("n_refused", req)
	require.NoError(t, err)
	require.Equal(t, edge.ServeControlOffReason, refused.Refused)
}
