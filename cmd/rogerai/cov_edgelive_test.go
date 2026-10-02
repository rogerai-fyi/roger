package main

// Coverage for the [3] panel's in-place agent actions, the face's serving closures (names, trust,
// membership, the upstream and the callee-side grant gate), and `roger edge readmit`. Real
// registrations on disk, a real authority, real signed grants; only the detached spawn is a seam.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

func stubSpawnRoger(t *testing.T) *[]map[string]string {
	t.Helper()
	var got []map[string]string
	prev := edgeSpawnRoger
	t.Cleanup(func() { edgeSpawnRoger = prev })
	edgeSpawnRoger = func(env map[string]string) error { got = append(got, env); return nil }
	return &got
}

func TestEdgeHostInPlaceAgentActions(t *testing.T) {
	newFacedAuthority(t, "desk")
	t.Setenv("ROGER_EDGE_INSTANCE", "")
	spawned := stubSpawnRoger(t)
	h, _ := edgeHostOff(t)

	require.Empty(t, h.selfInstanceName(), "nothing registered yet")
	require.NoError(t, h.renameSelfLive("bench"), "an unregistered roger has nothing to rename")
	h.registerInstance(loadConfig())
	t.Cleanup(h.deregisterInstance)
	me := h.selfInstanceName()
	require.NotEmpty(t, me)

	// Rename, in place: refused when the name is not one, effective at once when it is.
	require.Error(t, h.renameSelfLive("not a name!"))
	require.NoError(t, h.renameSelfLive("bench"))
	require.Equal(t, "bench", h.selfInstanceName())

	// The agent role, on and off, persisted AND marked durable.
	require.NoError(t, h.setAgentRoleLive(true))
	require.True(t, loadConfig().EdgeAgent)
	name := edgeThisAgentName(loadConfig())
	require.True(t, edge.IsPersistent(edgePersistDir(), name), "on makes a persistent agent")
	require.NoError(t, h.setAgentRoleLive(false))
	require.False(t, loadConfig().EdgeAgent)
	require.False(t, edge.IsPersistent(edgePersistDir(), name), "off stops being one")

	// Add Agent spawns a plain roger with the agent role and a fresh name.
	require.NoError(t, h.addAgentLive())
	require.Len(t, *spawned, 1)
	require.Equal(t, "1", (*spawned)[0]["ROGER_EDGE_AGENT"])
	require.NotEmpty(t, (*spawned)[0]["ROGER_EDGE_INSTANCE"])
	require.NotEqual(t, "bench", (*spawned)[0]["ROGER_EDGE_INSTANCE"], "never a name already taken here")

	// Resume: unknown is refused, a running one is left alone, a dark one is relaunched.
	require.EqualError(t, h.resumeAgentLive("ghost"), `no persistent agent named "ghost"`)
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "bench", time.Now()))
	require.NoError(t, h.resumeAgentLive("bench"))
	require.Len(t, *spawned, 1, "an agent already running is not launched twice")
	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "night", time.Now()))
	require.NoError(t, h.resumeAgentLive("night"))
	require.Len(t, *spawned, 2)
	require.Equal(t, "night", (*spawned)[1]["ROGER_EDGE_INSTANCE"])
	require.NoError(t, h.removeAgentLive("night"))
	require.False(t, edge.IsPersistent(edgePersistDir(), "night"))

	// A job bound from the panel lands on this node by default, and clears.
	require.NoError(t, h.setAgentJobLive(edge.AgentJob{Name: "bench", Model: "tiny"}))
	jobs := edge.AgentJobs(edgeJobsDir())
	require.Len(t, jobs, 1)
	require.Equal(t, edgeThisNodeID(), jobs[0].Node)
	require.NoError(t, h.clearAgentJobLive("bench"))
	require.Empty(t, edge.AgentJobs(edgeJobsDir()))
}

func TestEdgePeerServingClosuresAnswerFromTheRealFleet(t *testing.T) {
	self := newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board"})
	h, _ := edgeHostOff(t)
	_, key, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	s, ok := h.peerServing(key, self)
	require.True(t, ok, "an enrolled machine serves its peers")

	require.Equal(t, "attic", s.Names("n_attic"))
	require.Empty(t, s.Names("n_ghost"))
	require.NotNil(t, s.TrustNow())
	require.True(t, s.Member(self))
	require.True(t, s.Member("n_attic"))
	require.False(t, s.Member("n_ghost"), "a stranger is not admitted")

	_, _, _, up := s.Upstream("tiny")
	require.False(t, up, "a roger that shares nothing has no upstream")
	prev := edgeUpstreamOf
	t.Cleanup(func() { edgeUpstreamOf = prev })
	edgeAttachUpstream(func(band string) (string, string, bool) {
		return "http://127.0.0.1:8080/v1/chat/completions", "k", band == "tiny"
	})
	_, _, _, up = s.Upstream("other")
	require.False(t, up)
	u, k, inst, up := s.Upstream("tiny")
	require.True(t, up)
	require.Equal(t, "http://127.0.0.1:8080/v1/chat/completions", u)
	require.Equal(t, "k", k)
	require.Empty(t, inst, "not registered, so no instance name")

	// The callee-side gate: a forged grant, a grant for someone else, a grant that does not carry
	// the capability, and the owner's switches each refuse, in words.
	g, _, err := edgeGrantIssueCore("n_attic", "maintain", self, "1h", 0)
	require.NoError(t, err)
	require.EqualError(t, s.Authorize(g, "n_shed", edge.Maintain), "that grant is for a different caller")
	require.EqualError(t, s.Authorize(g, "n_attic", edge.ServeControl), "that grant does not allow serve-control")
	require.EqualError(t, s.Authorize(g, "n_attic", edge.Maintain), edge.MaintainOffReason)
	forged := g
	forged.Caps = []string{"maintain", "serve-control"}
	require.Error(t, s.Authorize(forged, "n_attic", edge.ServeControl), "an edited grant no longer verifies")

	sc, _, err := edgeGrantIssueCore("self", "serve-control", self, "1h", 0)
	require.NoError(t, err)
	require.EqualError(t, s.Authorize(sc, "n_attic", edge.ServeControl), edge.ServeControlOffReason)
	op, _, err := edgeGrantIssueCore("self", "operate", self, "1h", 0)
	require.NoError(t, err)
	require.ErrorIs(t, s.Authorize(op, "n_attic", edge.Operate), edge.ErrNotVerified, "operate needs the agent role here")
	cfg := loadConfig()
	cfg.EdgeAgent = true
	require.NoError(t, saveConfig(cfg))
	require.NoError(t, s.Authorize(op, "n_attic", edge.Operate))

	// An unreadable grant store refuses: fail closed.
	require.NoError(t, os.Remove(edgeGrantsPath()))
	require.NoError(t, os.MkdirAll(edgeGrantsPath(), 0o700))
	require.ErrorContains(t, s.Authorize(op, "n_attic", edge.Operate), "could not read this Edge's grant store")

	// No trust at all: nothing verifies.
	require.NoError(t, os.Remove(filepath.Join(edgeAuthDir(), edgeauth.RootCertFile)))
	require.Nil(t, s.TrustNow())
	require.EqualError(t, s.Authorize(op, "n_attic", edge.Operate), "this node cannot verify grants right now")
	_, ok = h.peerServing(key, self)
	require.False(t, ok, "a machine holding no trust answers describe only")
}

func TestEdgeReadmitAsksResolvesAndReAdmits(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board"})
	prevIn := edgeStdin
	t.Cleanup(func() { edgeStdin = prevIn })

	// Alone, it lists what can be re-admitted (nothing has been forgotten yet), and that is not a mistake.
	out, code := edgeRun(t, "edge", "readmit")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "no forgotten devices on this Edge")

	// Asked first; no leaves it alone.
	edgeStdin = strings.NewReader("n\n")
	out, code = edgeRun(t, "edge", "readmit", "attic")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "re-admit attic? its previous certificate is revoked")
	require.Contains(t, out, "left alone.")
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.False(t, local.ClaimGranted("n_attic"))

	// A fleet member by name.
	out, code = edgeRun(t, "edge", "readmit", "attic", "--yes")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "re-admitted attic - its old certificate is revoked")
	require.True(t, local.ClaimGranted("n_attic"), "it may claim a fresh certificate")

	// A raw id for a device not seen right now, confirmed at the prompt.
	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "readmit", "n_gone")
	require.Equal(t, 0, code, out)
	// An id this Edge has no record of is quoted as what the owner typed, never used as a name
	// (features/edge/node_identity.feature, RE-ADMITTING).
	require.Contains(t, out, `re-admit the device you typed as "n_gone"? this Edge has no record of it`)
	require.Contains(t, out, `re-admitted the device you typed as "n_gone" - any certificate it held is revoked`)
	require.NotContains(t, out, "re-admitted n_gone")

	// A candidate on the network is resolved first, and leaves the bench once re-admitted.
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = append(st.candidates, store.EdgeNode{ID: "n_phone", Name: "pocket"})
	require.NoError(t, st.save())
	out, code = edgeRun(t, "edge", "readmit", "pocket", "--yes")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "re-admitted pocket - its old certificate is revoked")
	st, err = loadEdgeState()
	require.NoError(t, err)
	for _, c := range st.candidates {
		require.NotEqual(t, "n_phone", c.ID)
	}
}

func TestEdgeReadmitIsOnlyForTheAuthority(t *testing.T) {
	useTempConfig(t)
	out, code := edgeRun(t, "edge", "readmit", "n_x", "--yes")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "only the machine that roots this Edge can re-admit a device")
}
