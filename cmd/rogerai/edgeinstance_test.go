package main

// Unit spec for this roger as an instance of its node (edgeinstance.go): the host registers
// on an enrolled machine and not otherwise, keeps the registration true to what is on air,
// honours a name the owner chose, and leaves cleanly. Resources are read, never invented.

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeInstanceNameAndCaps(t *testing.T) {
	require.Equal(t, "workshop", edgeInstanceName(config{}, "workshop", nil))
	require.Equal(t, "workshop-2", edgeInstanceName(config{}, "workshop", []string{"workshop"}))
	require.Equal(t, "desk", edgeInstanceName(config{EdgeInstance: "desk"}, "workshop", nil))
	require.Equal(t, "workshop", edgeInstanceName(config{EdgeInstance: "Not Valid"}, "workshop", nil), "an unusable choice falls back to the default")

	caps := edgeInstanceCaps(nil)
	require.Len(t, caps, 1)
	require.Equal(t, "operate", caps[0].Name)
	caps = edgeInstanceCaps([]string{"gpt-oss-20b"})
	require.Len(t, caps, 2)
	require.Equal(t, "serve", caps[1].Name)
}

func TestEdgeReadResourcesInventsNothing(t *testing.T) {
	r := edgeReadResources(time.Now())
	if runtime.GOOS != "linux" {
		return // nothing to assert about a kernel we did not read
	}
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		require.Nil(t, r)
		return
	}
	require.NotNil(t, r)
	require.Greater(t, r.RAMTotalGB, 0.0)
	require.NotZero(t, r.ReadAt)
	if r.GPU == "" {
		require.Zero(t, r.GPUMemUsed, "no GPU read means no GPU number")
	}
}

func TestEdgeHostRegistersOnlyWhenEnrolled(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	h, err := newEdgeHost("gentle-mongoose-93")
	require.NoError(t, err)

	// Not enrolled: nothing registers, and the household is empty.
	h.registerInstance(config{})
	require.Nil(t, h.reg)
	require.Empty(t, edge.Household(edgeInstancesDir(), time.Now()))

	// Enrol this machine (locally rooted: no network), then the same host registers.
	runEdgeCLI(t, "edge", "authority", "local", "workshop")
	runEdgeCLI(t, "edge", "enroll", "workshop")
	h.st, err = loadEdgeState()
	require.NoError(t, err)
	h.registerInstance(config{})
	require.NotNil(t, h.reg)
	house := edge.Household(edgeInstancesDir(), time.Now())
	require.Len(t, house, 1)
	require.Equal(t, "workshop", house[0].Name, "the default name is the node's")
	require.Equal(t, "operate", house[0].Caps[0].Name)
	insts, trunc := h.household()
	require.Len(t, insts, 1)
	require.False(t, trunc)

	// A model goes on air: the next beat says so, and serve joins the capabilities.
	edgeAttachOnAir(func() []string { return []string{"gpt-oss-20b"} })
	t.Cleanup(func() { edgeOnAir = nil })
	h.beatInstance(config{})
	house = edge.Household(edgeInstancesDir(), time.Now())
	require.Equal(t, []string{"gpt-oss-20b"}, house[0].Bands)
	require.Len(t, house[0].Caps, 2)

	// The owner names this roger: the next beat renames it.
	h.beatInstance(config{EdgeInstance: "desk"})
	house = edge.Household(edgeInstancesDir(), time.Now())
	require.Equal(t, "desk", house[0].Name)

	// A second host on the same machine takes the next default, never the same name.
	h2, err := newEdgeHost("gentle-mongoose-93")
	require.NoError(t, err)
	h2.registerInstance(config{})
	require.NotNil(t, h2.reg)
	house = edge.Household(edgeInstancesDir(), time.Now())
	require.Len(t, house, 2)

	// Leaving is clean and idempotent.
	h.deregisterInstance()
	h.deregisterInstance()
	h2.deregisterInstance()
	require.Empty(t, edge.Household(edgeInstancesDir(), time.Now()))
}

// The CLI's instance branches: a peer's instance described through the fleet, this
// machine's own household on its row or on its own line, and the refusals of `name .`.
func TestEdgeInstanceCLIBranches(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	edgeStdin = strings.NewReader("")

	// Not enrolled: `roger edge` has no "this machine" line to print, and describe of an
	// instance address that does not exist is the one refusal.
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge"}) })
	require.NoError(t, err)
	require.NotContains(t, out, "this machine:")
	_, err = captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge", "describe", "nowhere/none"}) })
	require.ErrorIs(t, err, edge.ErrNoSuchInstance)

	// Enrolled, nothing running: the household line says so.
	runEdgeCLI(t, "edge", "authority", "local", "workshop")
	runEdgeCLI(t, "edge", "enroll", "workshop")
	out = runEdgeCLI(t, "edge")
	require.Contains(t, out, "workshop")

	// A running roger, described by "./name" and by "workshop/name".
	reg, err := edge.Register(edgeInstancesDir(), nodeIDOf(t), edge.Instance{Name: "desk", Caps: edgeInstanceCaps(nil),
		Resources: &store.EdgeResources{GPU: "Orin", GPUMemUsed: 0.5, RAMUsedGB: 8, RAMTotalGB: 32, ReadAt: time.Now().Unix()}}, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Deregister() })
	out = runEdgeCLI(t, "edge", "describe", "./desk")
	require.Contains(t, out, "gpu")
	require.Contains(t, out, "ram")
	require.Contains(t, out, "nothing on air")
	out = runEdgeCLI(t, "edge", "describe", "workshop/desk")
	require.Contains(t, out, "workshop/desk")

	// `name .` refuses an unusable name and a sibling's name, and accepts a free one.
	_, err = captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge", "name", ".", "Not Valid"}) })
	require.Error(t, err)
	_, err = captureEdgeStdout(func() error { return dispatch(loadConfig(), []string{"edge", "name", ".", "desk"}) })
	require.ErrorIs(t, err, edge.ErrInstanceNameTaken)
	out = runEdgeCLI(t, "edge", "name", ".", "bench")
	require.Contains(t, out, `"bench"`)
	require.Equal(t, "bench", loadConfig().EdgeInstance)

	// A peer's instance, reached through the fleet: the record a face reported.
	st, err := loadEdgeState()
	require.NoError(t, err)
	_, err = st.fleet.Observe("n_peer000000000000000000", edge.Observation{Name: "jetson", Kind: "host", Addr: "192.168.1.20:1",
		Fingerprint: strings.Repeat("cd", 32), Instances: []edge.Instance{{Name: "serve", Caps: edgeInstanceCaps([]string{"qwen-3.8-27b"}), Bands: []string{"qwen-3.8-27b"}, Started: time.Now().Unix()}}})
	require.NoError(t, err)
	require.NoError(t, st.save())
	out = runEdgeCLI(t, "edge", "describe", "jetson/serve")
	require.Contains(t, out, "serving       qwen-3.8-27b")
	out = runEdgeCLI(t, "edge", "list")
	require.Contains(t, out, "/serve")
	require.Contains(t, out, "serves qwen-3.8-27b")
}

func nodeIDOf(t *testing.T) string {
	t.Helper()
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	return id.NodeID
}
