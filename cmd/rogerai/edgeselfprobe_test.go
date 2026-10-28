package main

// Executable pins for features/edge/serving_probe.feature, the host half: the machine's own
// household earns VERIFIED serve through the SAME probe a peer would run - against the server behind
// this face's upstream map - never by declaring it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
)

func TestHostVerifiesItsOwnServeThroughItsFace(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen"}}})
	}))
	t.Cleanup(upstream.Close)
	// One band on air, and this face can route it. (An instance advertising a band the face CANNOT
	// route stays CLAIMED as a whole - every band must answer - which is the next case.)
	edgeAttachOnAir(func() []string { return []string{"qwen"} })
	edgeAttachUpstream(func(band string) (string, string, bool) {
		if band == "qwen" {
			return upstream.URL + "/v1/chat/completions", "", true
		}
		return "", "", false
	})
	t.Cleanup(func() { edgeOnAir, edgeUpstreamOf = nil, nil })

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok)
	h.registerInstance(h.cfg)
	id, _, _, _ := edgeIdentityStore().LoadIdentity()

	h.observeSelf(time.Now())

	after, _, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.Len(t, after.Instances, 1)
	mine := after.Instances[0]
	require.Equal(t, []string{"qwen"}, mine.Bands)
	state := ""
	for _, c := range mine.Caps {
		if c.Name == string(edge.Serve) {
			state = c.State
		}
	}
	require.Equal(t, string(edge.Verified), state, "qwen answered through the face's upstream: the probe passed")

	// The rung exists now: ServersOf routes on it, and the TUI's Ask has a model to name.
	who, err := h.st.fleet.ServersOf("qwen")
	require.NoError(t, err)
	require.Len(t, who, 1)
	require.True(t, who[0].Serves())
}

func TestHostDoesNotVerifyWhatItsFaceCannotRoute(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)
	edgeAttachOnAir(func() []string { return []string{"mistral"} })
	edgeAttachUpstream(func(string) (string, string, bool) { return "", "", false })
	t.Cleanup(func() { edgeOnAir, edgeUpstreamOf = nil, nil })

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok)
	h.registerInstance(h.cfg)
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	h.observeSelf(time.Now())
	after, _, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.Len(t, after.Instances, 1)
	for _, c := range after.Instances[0].Caps {
		require.NotEqual(t, string(edge.Verified), c.State, "nothing answered: nothing is verified")
	}
}

// The host does not re-probe what it has already proven, whatever order the bands come back in: an
// unstable order made every pass look like a change, so the probe ran and re-recorded itself on
// every pass (the events timeline filled with it).
func TestTheHostDoesNotReprobeWhatItHasProven(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)

	probes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		probes++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen"}, {"id": "mistral"}}})
	}))
	t.Cleanup(upstream.Close)
	// Two bands, handed back in a DIFFERENT order each call, as a map iteration would.
	flip := false
	edgeAttachOnAir(func() []string {
		flip = !flip
		if flip {
			return []string{"qwen", "mistral"}
		}
		return []string{"mistral", "qwen"}
	})
	edgeAttachUpstream(func(band string) (string, string, bool) {
		if band == "qwen" || band == "mistral" {
			return upstream.URL + "/v1/chat/completions", "", true
		}
		return "", "", false
	})
	t.Cleanup(func() { edgeOnAir, edgeUpstreamOf = nil, nil })

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok)
	h.registerInstance(h.cfg)
	id, _, _, _ := edgeIdentityStore().LoadIdentity()

	h.observeSelf(time.Now())
	first := probes
	require.Greater(t, first, 0, "the first pass proves it")

	for range 5 {
		h.beatInstance(h.cfg)
		h.observeSelf(time.Now())
	}
	require.Equal(t, first, probes, "five more passes ask the server nothing: it is already proven")

	after, _, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.Len(t, after.Instances, 1)
	require.Equal(t, []string{"mistral", "qwen"}, after.Instances[0].Bands, "the bands are recorded sorted")
	probeEvents := 0
	for _, e := range after.History {
		if e.What == "probe" {
			probeEvents++
		}
	}
	require.Equal(t, 1, probeEvents, "one crossing, one event, however many passes: %+v", after.History)
}

// An instance that advertises a band this face cannot route is not marked as serving: proving the
// one that answers would let the fleet route the one that does not
// (features/edge/serving_probe.feature).
func TestHostDoesNotVerifyAMixedBandSet(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen"}}})
	}))
	t.Cleanup(upstream.Close)
	edgeAttachOnAir(func() []string { return []string{"qwen", "mistral"} })
	edgeAttachUpstream(func(band string) (string, string, bool) {
		if band == "qwen" {
			return upstream.URL + "/v1/chat/completions", "", true
		}
		return "", "", false // on air in name only: this face cannot route it
	})
	t.Cleanup(func() { edgeOnAir, edgeUpstreamOf = nil, nil })

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok)
	h.registerInstance(h.cfg)
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	h.observeSelf(time.Now())

	after, _, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.Len(t, after.Instances, 1)
	for _, c := range after.Instances[0].Caps {
		if c.Name == string(edge.Serve) {
			require.Equal(t, string(edge.Claimed), c.State, "one band answered, the other did not: not proven")
		}
	}
	who, err := h.st.fleet.ServersOf("qwen")
	require.NoError(t, err)
	for _, l := range who {
		require.False(t, l.Serves(), "the ladder gets no rung from an unproven instance")
	}
}

// The flap that filled the timeline, end to end on the host: a band comes and goes, and after the
// first pass nothing is probed and nothing is recorded, because the record keeps the PROVEN bands
// (features/edge/serving_probe.feature).
func TestTheHostRidesOutAFlappingBandSet(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	out, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)

	probes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		probes++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen"}, {"id": "shim"}}})
	}))
	t.Cleanup(upstream.Close)
	both := true
	edgeAttachOnAir(func() []string {
		both = !both
		if both {
			return []string{"qwen", "shim"}
		}
		return []string{"qwen"} // the shim band comes and goes, as it does on the real machine
	})
	edgeAttachUpstream(func(band string) (string, string, bool) {
		if band == "qwen" || band == "shim" {
			return upstream.URL + "/v1/chat/completions", "", true
		}
		return "", "", false
	})
	t.Cleanup(func() { edgeOnAir, edgeUpstreamOf = nil, nil })

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok)
	h.registerInstance(h.cfg)
	id, _, _, _ := edgeIdentityStore().LoadIdentity()

	// Pass until the wider set is the one on air, so both bands get proven.
	for range 3 {
		h.beatInstance(h.cfg)
		h.observeSelf(time.Now())
	}
	settled := probes
	require.Greater(t, settled, 0)

	for range 10 {
		h.beatInstance(h.cfg)
		h.observeSelf(time.Now())
	}
	require.Equal(t, settled, probes, "ten flaps and the server is asked nothing more")

	after, _, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.Len(t, after.Instances, 1)
	require.ElementsMatch(t, []string{"qwen", "shim"}, after.Instances[0].ServeProven)
	probeEvents := 0
	for _, e := range after.History {
		if e.What == "probe" {
			probeEvents++
		}
	}
	require.LessOrEqual(t, probeEvents, 2, "at most the one crossing, however many flaps: %+v", after.History)

	// And the ladder routes both proven bands.
	for _, band := range []string{"qwen", "shim"} {
		who, err := h.st.fleet.ServersOf(band)
		require.NoError(t, err)
		require.NotEmpty(t, who)
		require.True(t, who[0].ServesBand(band), band)
	}
}
