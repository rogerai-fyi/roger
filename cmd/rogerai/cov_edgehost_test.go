package main

// Coverage for the Edge host's owner actions as the console and the [3] screen call them: issuing and
// revoking grants, naming and forgetting nodes, choosing where a turn goes, the ADOPTING band, and the
// authority's access log. Every one goes through the real fleet, the real grant store and the real
// authority on disk.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/tui"
)

func TestEdgeHostIssuesAndRevokesGrantsOnlyForNodesOnThisEdge(t *testing.T) {
	self := newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board"})
	h, _ := edgeHostOff(t)

	_, err := h.issueGrant("self", "operate", self, "soon")
	require.EqualError(t, err, `bad grant duration "soon", want e.g. 7d or 2026-12-31`)
	_, err = h.issueGrant("self", "operate", "n_ghost", "")
	require.EqualError(t, err, "no such node on this Edge to grant against")
	_, err = h.issueGrant("n_ghost", "operate", self, "")
	require.EqualError(t, err, "the caller is not a node on this Edge")
	_, err = h.issueGrant("self", "teleport", self, "")
	require.ErrorContains(t, err, "is not an edge capability")

	g, err := h.issueGrant("-", "operate", self, "")
	require.NoError(t, err)
	require.True(t, g.Self, "self and - are a Self grant")
	require.Empty(t, g.Caller)
	require.InDelta(t, time.Now().Add(30*24*time.Hour).Unix(), g.ExpiresAt, 120, "no duration is thirty days")

	g2, err := h.issueGrant("n_attic", "sense", self, "7d")
	require.NoError(t, err)
	require.Equal(t, "n_attic", g2.Caller)

	gs, err := h.grants()
	require.NoError(t, err)
	require.Len(t, gs, 2)

	require.EqualError(t, h.revokeGrant("grant_nope"), `no such grant "grant_nope"`)
	require.NoError(t, h.revokeGrant(g.ID))
	got, ok, err := edgeGrantStore().Get(g.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, got.Revoked)
}

func TestEdgeHostIssuesNoGrantOffTheAuthority(t *testing.T) {
	useTempConfig(t)
	h, _ := edgeHostOff(t)
	_, err := h.issueGrant("self", "operate", "n_x", "1h")
	require.ErrorContains(t, err, "grants are issued where the root key lives")
	require.Nil(t, h.adopting(), "a machine with no root has nothing adopting")
}

func TestEdgeHostNamesAndForgetsNodes(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t,
		store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board"},
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "board"},
	)
	h, _ := edgeHostOff(t)

	require.EqualError(t, h.nameNode("n_ghost", "x"), "no such node on this Edge")
	require.EqualError(t, h.nameNode("n_attic", "shed"), `the name "shed" is already taken on this Edge`)
	require.NoError(t, h.nameNode("n_attic", "loft"))
	require.NoError(t, h.nameNode("n_attic", "loft"), "keeping its own name is not a collision")

	// Persisted: the next process reads the new name.
	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get("n_attic")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "loft", n.Name)

	require.EqualError(t, h.forgetNode("n_ghost"), "no such node on this Edge")
	require.NoError(t, h.forgetNode("n_shed"))
	st, err = loadEdgeState()
	require.NoError(t, err)
	_, ok, err = st.fleet.Get("n_shed")
	require.NoError(t, err)
	require.False(t, ok, "a forgotten node is gone from the file the CLI reads")
}

func TestEdgeHostSetPreferValidatesAndPersists(t *testing.T) {
	useTempConfig(t)
	h, _ := edgeHostOff(t)
	require.EqualError(t, h.setPrefer("anywhere"), `prefer takes local, market or local-only; "anywhere" is none of them`)
	require.Empty(t, loadConfig().EdgePrefer)
	require.NoError(t, h.setPrefer("local-only"))
	require.Equal(t, "local-only", loadConfig().EdgePrefer)
	h.mu.Lock()
	live := h.cfg.EdgePrefer
	h.mu.Unlock()
	require.Equal(t, "local-only", live, "the running host honors it at once")
}

func TestEdgeHostListsAClaimedDeviceAsAdoptingUntilItJoins(t *testing.T) {
	newFacedAuthority(t, "desk")
	h, _ := edgeHostOff(t)
	require.Nil(t, h.adopting(), "no claim granted, nothing adopting")

	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, local.GrantClaim("n_phone", "pocket"))
	require.NoError(t, local.GrantClaim("n_bare", ""))
	got := h.adopting()
	require.ElementsMatch(t, []store.EdgeNode{{ID: "n_phone", Name: "pocket"}, {ID: "n_bare", Name: "n_bare"}}, got,
		"a claim with no name is drawn by its id")

	// Once it is in the fleet it is a member, not adopting.
	h.mu.Lock()
	_, err = h.st.fleet.Enroll(store.EdgeNode{ID: "n_phone", Name: "pocket", Kind: "board"})
	h.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, []store.EdgeNode{{ID: "n_bare", Name: "n_bare"}}, h.adopting())
}

func TestEdgeHostConfirmActuateAndRecordOnNode(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_relay", Name: "relay", Kind: "board",
		Caps: []store.EdgeCap{{Name: string(edge.Actuate), State: string(edge.PendingConfirmation)}}})
	h, _ := edgeHostOff(t)

	require.Error(t, h.confirmActuate("n_ghost"))
	require.NoError(t, h.confirmActuate("n_relay"))
	h.recordOnNode("n_relay", "schedule", "ran restart")
	h.recordOnNode("n_ghost", "schedule", "nowhere") // an unknown node records nothing and does not panic

	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get("n_relay")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, string(edge.Verified), n.Caps[0].State)
	require.Equal(t, edgeOperatorName(), n.Caps[0].ConfirmedBy, "the host records who, never the screen")
	var whats []string
	for _, e := range n.History {
		whats = append(whats, e.What+":"+e.Detail)
	}
	require.Contains(t, whats, "schedule:ran restart")
}

func TestEdgeHostWiresTheAlertFloorDriftAndAgentHooks(t *testing.T) {
	self := newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board", Instances: []store.EdgeInstance{{Name: "roger"}}})
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	var hooks tui.Hooks
	t.Setenv(edge.EnvDiscovery, "0")
	h.wire(&hooks)

	require.Equal(t, "desk", hooks.EdgeSelf, "an enrolled machine is called by its Edge name")
	require.True(t, hooks.EdgeHealthLive)

	now := time.Now()
	samples, calls, rate, _ := hooks.EdgeFloor(now)
	require.Empty(t, samples)
	require.Empty(t, calls)
	require.Zero(t, rate)
	require.Empty(t, hooks.EdgeReads())
	require.ErrorContains(t, hooks.EdgeRoger("offline", self), "no standing call offline", "nothing to answer is said")
	require.ErrorContains(t, hooks.EdgeStandBy("offline", self, time.Hour), "no standing call offline")

	require.Empty(t, hooks.EdgeDrift(), "no expectations, no drift")
	require.NoError(t, edgeAlertLog().SetExpectation(edge.Expectation{What: edge.ExpectServes, Value: "tiny"}))
	drift := hooks.EdgeDrift()
	require.Len(t, drift["n_attic"], 1, "a node that does not serve what the owner expects drifts")
	require.Contains(t, drift["n_attic"][0], "tiny")
	require.Contains(t, drift["n_attic"][0], "does not serve it")
	require.NotContains(t, drift, self, "a unit that has not reported is unknown, never drift")

	require.Empty(t, hooks.EdgePersistentAgents())
	require.False(t, hooks.EdgeAgentActive())
	require.Empty(t, hooks.EdgeAgentJobs())
	require.NotNil(t, hooks.EdgeSetup)
}

func TestEdgeAuthAccessLogWritesOneLinePerRequest(t *testing.T) {
	useTempConfig(t)
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(http.StatusTeapot) })

	// Default path: under the edge dir.
	require.NoError(t, os.MkdirAll(edgeAuthDir(), 0o700))
	rr := httptest.NewRecorder()
	edgeAuthAccessLog(next).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/edge/claim", nil))
	require.Equal(t, http.StatusTeapot, rr.Code, "the request still reaches the authority")
	b, err := os.ReadFile(filepath.Join(edgeAuthDir(), "access.log"))
	require.NoError(t, err)
	require.Contains(t, string(b), "POST /edge/claim from 192.0.2.1:1234")

	// An explicit file, appended to.
	p := filepath.Join(t.TempDir(), "auth.log")
	t.Setenv("ROGERAI_EDGE_AUTHLOG_FILE", p)
	h := edgeAuthAccessLog(next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/a", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/b", nil))
	b, err = os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(b), "\n"))
	require.Contains(t, string(b), "GET /b")
	require.Equal(t, 3, called)
}

func TestEdgeAuthorityServesWithTheAccessLogAndFallsBackOffATakenPort(t *testing.T) {
	newFacedAuthority(t, "desk")
	t.Setenv("ROGERAI_EDGE_AUTHLOG", "1")
	logPath := filepath.Join(t.TempDir(), "auth.log")
	t.Setenv("ROGERAI_EDGE_AUTHLOG_FILE", logPath)

	// Hold a port, and pin the authority to it: the run falls back to an ephemeral port.
	busy := httptest.NewServer(http.NotFoundHandler())
	defer busy.Close()
	busyAddr := strings.TrimPrefix(busy.URL, "http://")
	t.Setenv(envAuthorityBind, busyAddr)

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	h.startAuthority()
	t.Cleanup(h.closeListeners)
	addr := h.authorityAddr()
	require.NotEmpty(t, addr)
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	_, busyPort, _ := net.SplitHostPort(busyAddr)
	require.NotEqual(t, busyPort, port, "a taken port is not reused")
	require.Zero(t, edgeSavedAuthorityPort(), "a fallback port is never persisted, or enrolled devices would be orphaned")

	resp, err := http.Get("http://127.0.0.1:" + port + "/nothing-here")
	require.NoError(t, err)
	resp.Body.Close()
	b, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(b), "GET /nothing-here", "the authority's requests are logged when asked")
}
