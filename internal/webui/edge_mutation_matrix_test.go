package webui

// THE EDGE MUTATION MATRIX. Every console write under /api/edge/* has the same four honest
// answers, and this file pins all four for each one:
//
//   501  the build has no hook for it - the console says so instead of pretending;
//   400  the body is missing what the action needs (an empty body never reads as a default);
//   502  the host refused - its own words come back, not a blank success;
//   200  the host did it - and the hook saw exactly what the owner asked for.
//
// Real dependencies: a real Server over httptest with its real token guard, a real edge.Fleet over a
// real store. The hooks record their arguments; nothing is mocked out of the request path.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

type matrixRig struct {
	s     *Server
	srv   *httptest.Server
	calls []string
	fail  error // returned by every hook when set
	sinkD edge.Delivery
	had   bool
}

func (m *matrixRig) rec(c string) error {
	m.calls = append(m.calls, c)
	return m.fail
}

func newMatrixRig(t *testing.T, wired bool) *matrixRig {
	t.Helper()
	db := store.NewMem()
	fleet := edge.NewFleet(db, "acct-1")
	n := edge.NewNode(nil, "attic-pi", edge.Host)
	n.ID = "n_attic"
	n.Presence = string(edge.PresenceVerified)
	_, err := fleet.Enroll(n)
	require.NoError(t, err)

	m := &matrixRig{had: true, sinkD: edge.Delivery{Sink: "s1", Words: "webhook · hooks.example · s1", OK: true}}
	h := EdgeHooks{Self: "workshop", Fleet: fleet}
	if wired {
		h.Candidates = func() []store.EdgeNode {
			return []store.EdgeNode{{ID: "c_new", Name: "new-pi"}}
		}
		h.Adopt = func(id, name string) error { return m.rec("adopt " + id + " " + name) }
		h.Name = func(id, name string) error { return m.rec("name " + id + "=" + name) }
		h.Forget = func(id string) error { return m.rec("forget " + id) }
		h.ConfirmActuate = func(id string) error { return m.rec("actuate " + id) }
		h.SetAgent = func(on bool) error {
			if on {
				return m.rec("agent on")
			}
			return m.rec("agent off")
		}
		h.AddAgent = func() error { return m.rec("add-agent") }
		h.SetPrefer = func(want string) error { return m.rec("prefer " + want) }
		h.SetAgentJob = func(j edge.AgentJob) error {
			return m.rec("job " + j.Name + " node=" + j.Node + " model=" + j.Model + " job=" + j.Job + " targets=" + strings.Join(j.Targets, ","))
		}
		h.ClearAgentJob = func(name string) error { return m.rec("job-clear " + name) }
		h.IssueGrant = func(caller, capability, callee, forDur string) (store.Grant, error) {
			return store.Grant{ID: "grant_1"}, m.rec("grant " + caller + "|" + capability + "|" + callee + "|" + forDur)
		}
		h.RevokeGrant = func(id string) error { return m.rec("revoke " + id) }
		h.AddSink = func(kind, url string) (edge.Sink, error) {
			return edge.Sink{ID: "s1"}, m.rec("sink-add " + kind + " " + url)
		}
		h.RemoveSink = func(id string) (bool, error) { return m.had, m.rec("sink-rm " + id) }
		h.TestSink = func(id string) (edge.Delivery, error) { return m.sinkD, m.rec("sink-test " + id) }
		h.ServeModel = func(node, id string) (string, error) {
			return "serving " + id, m.rec("serve " + node + " " + id)
		}
		h.Roger = func(rule, node string) error { return m.rec("roger " + rule + " " + node) }
		h.StandBy = func(rule, node string, d time.Duration) error {
			return m.rec("standby " + rule + " " + node + " " + d.String())
		}
	}
	m.s = New(testCtrl(), Options{Edge: h})
	m.srv = httptest.NewServer(m.s.Handler())
	t.Cleanup(m.srv.Close)
	return m
}

func (m *matrixRig) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(m.srv.URL+path+"?t="+m.s.Token(), "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

// edgeWrites is every mutation with a body that satisfies it and the call the host must see.
var edgeWrites = []struct {
	path, good, call, emptyWant string
}{
	{"/api/edge/adopt", `{"id":"c_new"}`, "adopt c_new new-pi", "candidate id required"},
	{"/api/edge/name", `{"id":"n_attic","name":"garage-pi"}`, "name n_attic=garage-pi", "node id and name required"},
	{"/api/edge/forget", `{"id":"n_attic"}`, "forget n_attic", "node id required"},
	{"/api/edge/actuate", `{"id":"n_attic"}`, "actuate n_attic", "node id required"},
	{"/api/edge/agent", `{"on":true}`, "agent on", "on (bool) required"},
	{"/api/edge/prefer", `{"prefer":"local"}`, "prefer local", "prefer (local|market|local-only) required"},
	{"/api/edge/job", `{"name":"scout","model":"m1","job":"watch","targets":["attic-pi"]}`, "job scout node= model=m1 job=watch targets=attic-pi", "agent name required"},
	{"/api/edge/job/clear", `{"name":"scout"}`, "job-clear scout", "agent name required"},
	{"/api/edge/grant", `{"caller":"self","capability":"observe","callee":"n_attic","for":"1h"}`, "grant self|observe|n_attic|1h", "capability and callee required"},
	{"/api/edge/grant/revoke", `{"id":"grant_1"}`, "revoke grant_1", "grant id required"},
	{"/api/edge/roger", `{"rule":"r1","node":"n_attic"}`, "roger r1 n_attic", "rule and node required"},
	{"/api/edge/standby", `{"rule":"r1","node":"n_attic","hours":2}`, "standby r1 n_attic 2h0m0s", "rule and node required"},
}

func TestEdgeWritesUnwiredAreHonestly501(t *testing.T) {
	m := newMatrixRig(t, false)
	paths := []string{"/api/edge/agent/add", "/api/edge/sink", "/api/edge/serve-model"}
	for _, w := range edgeWrites {
		paths = append(paths, w.path)
	}
	for _, p := range paths {
		code, body := m.post(t, p, `{"id":"n_attic"}`)
		require.Equal(t, http.StatusNotImplemented, code, "%s unwired", p)
		require.Contains(t, body, "this build cannot", "%s must say why", p)
	}
	require.Empty(t, m.calls)
}

func TestEdgeWritesDoWhatTheOwnerAsked(t *testing.T) {
	for _, w := range edgeWrites {
		t.Run(w.path, func(t *testing.T) {
			m := newMatrixRig(t, true)
			code, body := m.post(t, w.path, w.good)
			require.Equal(t, http.StatusOK, code, body)
			require.Equal(t, []string{w.call}, m.calls)
			require.Contains(t, body, `"self"`, "the fresh snapshot comes back so the tab shows the change")
		})
	}
}

func TestEdgeWritesRefuseAnIncompleteBody(t *testing.T) {
	for _, w := range edgeWrites {
		t.Run(w.path, func(t *testing.T) {
			m := newMatrixRig(t, true)
			for _, body := range []string{`{}`, `not json`} {
				code, got := m.post(t, w.path, body)
				require.Equal(t, http.StatusBadRequest, code, "%s body %q", w.path, body)
				require.Contains(t, got, w.emptyWant)
			}
			require.Empty(t, m.calls, "nothing reaches the host from a body that does not say what to do")
		})
	}
}

func TestEdgeWritesSurfaceTheHostsRefusal(t *testing.T) {
	for _, w := range edgeWrites {
		t.Run(w.path, func(t *testing.T) {
			m := newMatrixRig(t, true)
			m.fail = errors.New("the host says no")
			code, body := m.post(t, w.path, w.good)
			// prefer and grant are the owner's words being judged (400); the rest are the host failing (502).
			want := http.StatusBadGateway
			if w.path == "/api/edge/prefer" || w.path == "/api/edge/grant" {
				want = http.StatusBadRequest
			}
			require.Equal(t, want, code)
			require.Contains(t, body, "the host says no")
		})
	}
}

func TestEdgeAdoptRefusesWhatIsNotACandidate(t *testing.T) {
	m := newMatrixRig(t, true)
	code, body := m.post(t, "/api/edge/adopt", `{"id":"c_stranger"}`)
	require.Equal(t, http.StatusNotFound, code)
	require.Contains(t, body, "not a candidate on this network")
	require.Empty(t, m.calls, "a node the scan did not see is never enrolled")
}

func TestEdgeAgentOffIsDistinctFromAnEmptyBody(t *testing.T) {
	m := newMatrixRig(t, true)
	code, _ := m.post(t, "/api/edge/agent", `{"on":false}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"agent off"}, m.calls)
}

func TestEdgeAddAgent(t *testing.T) {
	m := newMatrixRig(t, true)
	code, _ := m.post(t, "/api/edge/agent/add", ``)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"add-agent"}, m.calls)

	m.fail = errors.New("no room for another agent")
	code, body := m.post(t, "/api/edge/agent/add", ``)
	require.Equal(t, http.StatusBadGateway, code)
	require.Contains(t, body, "no room for another agent")
}

func TestEdgeJobNodeIsNeverTheBrowsers(t *testing.T) {
	m := newMatrixRig(t, true)
	code, _ := m.post(t, "/api/edge/job", `{"name":"scout","node":"n_somewhere_else","model":"m1"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"job scout node= model=m1 job= targets="}, m.calls,
		"the host fills in this machine; a node named by the browser is dropped")
}

func TestEdgeStandByIsBoundedToOneToEightHours(t *testing.T) {
	for _, tc := range []struct {
		hours string
		want  string
	}{
		{`0`, "1h0m0s"}, {`-3`, "1h0m0s"}, {`0.5`, "30m0s"}, {`8`, "8h0m0s"}, {`72`, "8h0m0s"},
	} {
		m := newMatrixRig(t, true)
		code, _ := m.post(t, "/api/edge/standby", `{"rule":"r1","node":"n_attic","hours":`+tc.hours+`}`)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []string{"standby r1 n_attic " + tc.want}, m.calls, "hours=%s", tc.hours)
	}
}

func TestEdgeGrantIssueAllowsAnEmptyCaller(t *testing.T) {
	m := newMatrixRig(t, true)
	code, _ := m.post(t, "/api/edge/grant", `{"capability":"observe","callee":"n_attic"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"grant |observe|n_attic|"}, m.calls)
}

func TestEdgeSinkEveryBranch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		fail     error
		had      bool
		notOK    bool
		want     int
		wantBody string
		call     string
	}{
		{"add", `{"do":"add","kind":"webhook","url":"https://hooks.example/x?k=secret"}`, nil, true, false, 200, `"self"`, "sink-add webhook https://hooks.example/x?k=secret"},
		{"add refused", `{"do":"add","kind":"carrier-pigeon","url":"x"}`, errors.New("not a sink kind"), true, false, 400, "not a sink kind", "sink-add carrier-pigeon x"},
		{"rm", `{"do":"rm","id":"s1"}`, nil, true, false, 200, `"self"`, "sink-rm s1"},
		{"rm without id", `{"do":"rm"}`, nil, true, false, 400, "sink id required", ""},
		{"rm unknown", `{"do":"rm","id":"s9"}`, nil, false, false, 404, "no sink has that id", "sink-rm s9"},
		{"rm host fails", `{"do":"rm","id":"s1"}`, errors.New("store down"), true, false, 502, "store down", "sink-rm s1"},
		{"test", `{"do":"test","id":"s1"}`, nil, true, false, 200, `"self"`, "sink-test s1"},
		{"test without id", `{"do":"test"}`, nil, true, false, 400, "sink id required", ""},
		{"test host fails", `{"do":"test","id":"s1"}`, errors.New("no such sink"), true, false, 502, "no such sink", "sink-test s1"},
		{"test not delivered", `{"do":"test","id":"s1"}`, nil, true, true, 502, "did not take it: HTTP 500", "sink-test s1"},
		{"unknown verb", `{"do":"explode","id":"s1"}`, nil, true, false, 400, "sink takes add, rm or test", ""},
		{"bad json", `not json`, nil, true, false, 400, "bad request", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMatrixRig(t, true)
			m.fail, m.had = tc.fail, tc.had
			if tc.notOK {
				m.sinkD.OK, m.sinkD.Says = false, "HTTP 500"
			}
			code, body := m.post(t, "/api/edge/sink", tc.body)
			require.Equal(t, tc.want, code, body)
			require.Contains(t, body, tc.wantBody)
			require.NotContains(t, body, "k=secret", "a sink URL never travels back to the browser")
			if tc.call == "" {
				require.Empty(t, m.calls)
			} else {
				require.Equal(t, []string{tc.call}, m.calls)
			}
		})
	}
}

func TestEdgeServeModelNeedsTheOwnersYes(t *testing.T) {
	m := newMatrixRig(t, true)
	code, body := m.post(t, "/api/edge/serve-model", `{"id":"n_attic"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body, "node id and model id required")

	code, body = m.post(t, "/api/edge/serve-model", `{"id":"n_attic","model":"qwen"}`)
	require.Equal(t, http.StatusPreconditionRequired, code)
	require.Contains(t, body, "needs the owner's yes")
	require.Empty(t, m.calls, "nothing changes on a member without the yes")

	code, body = m.post(t, "/api/edge/serve-model", `{"id":"n_attic","model":"qwen","confirm":true}`)
	require.Equal(t, http.StatusOK, code)
	require.JSONEq(t, `{"says":"serving qwen"}`, body)

	m.fail = errors.New("the member declined")
	code, body = m.post(t, "/api/edge/serve-model", `{"id":"n_attic","model":"qwen","confirm":true}`)
	require.Equal(t, http.StatusBadGateway, code)
	require.Contains(t, body, "the member declined")
}

func TestEdgeReadIsGETOnly(t *testing.T) {
	m := newMatrixRig(t, true)
	code, _ := m.post(t, "/api/edge", `{}`)
	require.Equal(t, http.StatusMethodNotAllowed, code)
}
