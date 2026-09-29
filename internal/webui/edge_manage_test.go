package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// The console's Name and Forget actions go through the SAME host operations the TUI and CLI use
// (features/edge/manage_web.feature): a rename is not a browser-only label, and a forget is never a
// silent delete. These drive the real Server over httptest with its token.

func manageRig(t *testing.T) (*Server, *httptest.Server, *edge.Fleet, *[]string, *[]string) {
	t.Helper()
	db := store.NewMem()
	fleet := edge.NewFleet(db, "acct-1")
	n := edge.NewNode(nil, "attic-pi", edge.Host)
	n.ID = "n_attic"
	n.Presence = string(edge.PresenceVerified)
	if _, err := fleet.Enroll(n); err != nil {
		t.Fatal(err)
	}
	named := []string{}
	forgot := []string{}
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Self:  "workshop",
		Fleet: fleet,
		Name: func(id, name string) error {
			named = append(named, id+"="+name)
			return fleet.Rename(id, name)
		},
		Forget: func(id string) error {
			forgot = append(forgot, id)
			return fleet.Forget(id)
		},
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, fleet, &named, &forgot
}

func postJSONTok(t *testing.T, srv *httptest.Server, s *Server, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+path+"?t="+s.Token(), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestConsoleNameRenamesThroughTheHost(t *testing.T) {
	s, srv, fleet, named, _ := manageRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/name", `{"id":"n_attic","name":"garage-pi"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("name = %d, want 200", resp.StatusCode)
	}
	if len(*named) != 1 || (*named)[0] != "n_attic=garage-pi" {
		t.Fatalf("host Name not called as expected: %v", *named)
	}
	got, _, _ := fleet.Get("n_attic")
	if got.Name != "garage-pi" {
		t.Fatalf("node name = %q, want garage-pi (renamed through the host, not a browser label)", got.Name)
	}
	// The fresh snapshot returns so the tab shows the change.
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
}

func TestConsoleForgetRemovesThroughTheHost(t *testing.T) {
	s, srv, fleet, _, forgot := manageRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/forget", `{"id":"n_attic"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forget = %d, want 200", resp.StatusCode)
	}
	if len(*forgot) != 1 || (*forgot)[0] != "n_attic" {
		t.Fatalf("host Forget not called: %v", *forgot)
	}
	if _, ok, _ := fleet.Get("n_attic"); ok {
		t.Fatalf("node still in fleet after forget")
	}
}

func TestConsoleNameForgetNeedTheToken(t *testing.T) {
	_, srv, _, _, _ := manageRig(t)
	// A write endpoint without the token is refused (CSRF guard), like every other action.
	for _, path := range []string{"/api/edge/name", "/api/edge/forget"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{"id":"n_attic","name":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s without token = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestConsoleNameForgetGETRefused(t *testing.T) {
	s, srv, _, _, _ := manageRig(t)
	// A GET (CSRF-reachable) to a write endpoint is refused even WITH the token.
	for _, path := range []string{"/api/edge/name", "/api/edge/forget"} {
		resp, err := http.Get(srv.URL + path + "?t=" + s.Token())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("GET %s = 200, want a refusal (write endpoints are POST-only)", path)
		}
	}
}

func TestConsoleNameForgetDisabledWhenUnwired(t *testing.T) {
	// No Name/Forget hooks: the endpoints say so rather than pretending.
	s := New(testCtrl(), Options{Edge: EdgeHooks{Fleet: edge.NewFleet(store.NewMem(), "acct-1")}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	for _, path := range []string{"/api/edge/name", "/api/edge/forget"} {
		resp := postJSONTok(t, srv, s, path, `{"id":"n_x","name":"y"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s unwired = %d, want 501", path, resp.StatusCode)
		}
	}
}

// The console's self-controls - agent on/off, add agent, prefer - go through the same host operations
// the TUI [3] panel does (features/edge/agents.feature, mode.feature).

func selfRig(t *testing.T) (*Server, *httptest.Server, *[]string) {
	t.Helper()
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	calls := []string{}
	agentOn := false
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Self:        "workshop",
		Fleet:       fleet,
		SetAgent:    func(on bool) error { agentOn = on; calls = append(calls, "agent="+boolStr(on)); return nil },
		AddAgent:    func() error { calls = append(calls, "add"); return nil },
		AgentActive: func() bool { return agentOn },
		SetPrefer:   func(want string) error { calls = append(calls, "prefer="+want); return nil },
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, &calls
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func TestConsoleAgentToggleThroughHost(t *testing.T) {
	s, srv, calls := selfRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/agent", `{"on":true}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent on = %d, want 200", resp.StatusCode)
	}
	// The snapshot now reports the role active.
	resp2 := postJSONTok(t, srv, s, "/api/edge/agent", `{"on":false}`)
	var snap edgeSnap
	json.NewDecoder(resp2.Body).Decode(&snap)
	resp2.Body.Close()
	if len(*calls) != 2 || (*calls)[0] != "agent=on" || (*calls)[1] != "agent=off" {
		t.Fatalf("SetAgent calls = %v", *calls)
	}
}

func TestConsoleAddAgentThroughHost(t *testing.T) {
	s, srv, calls := selfRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/agent/add", `{}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add agent = %d, want 200", resp.StatusCode)
	}
	if len(*calls) != 1 || (*calls)[0] != "add" {
		t.Fatalf("AddAgent calls = %v", *calls)
	}
}

func TestConsolePreferThroughHost(t *testing.T) {
	s, srv, calls := selfRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/prefer", `{"prefer":"local-only"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prefer = %d, want 200", resp.StatusCode)
	}
	if len(*calls) != 1 || (*calls)[0] != "prefer=local-only" {
		t.Fatalf("SetPrefer calls = %v", *calls)
	}
}

func TestConsoleSelfControlsNeedTokenAndPost(t *testing.T) {
	s, srv, _ := selfRig(t)
	for _, path := range []string{"/api/edge/agent", "/api/edge/agent/add", "/api/edge/prefer"} {
		// no token
		resp, _ := http.Post(srv.URL+path, "application/json", strings.NewReader(`{}`))
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s no token = %d, want 403", path, resp.StatusCode)
		}
		// GET with token
		g, _ := http.Get(srv.URL + path + "?t=" + s.Token())
		g.Body.Close()
		if g.StatusCode == http.StatusOK {
			t.Fatalf("GET %s = 200, want a refusal", path)
		}
	}
}

func TestConsoleSelfControlsDisabledWhenUnwired(t *testing.T) {
	s := New(testCtrl(), Options{Edge: EdgeHooks{Fleet: edge.NewFleet(store.NewMem(), "acct-1")}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	for _, tc := range []struct{ path, body string }{
		{"/api/edge/agent", `{"on":true}`}, {"/api/edge/agent/add", `{}`}, {"/api/edge/prefer", `{"prefer":"local"}`},
	} {
		resp := postJSONTok(t, srv, s, tc.path, tc.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s unwired = %d, want 501", tc.path, resp.StatusCode)
		}
	}
}

// The console snapshot carries the same fleet state the TUI shows: adopting nodes, durable agents,
// and each agent's assignment (features/edge/agents.feature, jobs.feature) - so the two surfaces show
// one fleet, not two.
func TestConsoleSnapshotCarriesAgentsAndJobs(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	adopting := store.EdgeNode{ID: "n_phone", Name: "gentle-ibex", Kind: "mobile", Presence: string(edge.PresenceVerified)}
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Self:             "workshop",
		Fleet:            fleet,
		Adopting:         func() []store.EdgeNode { return []store.EdgeNode{adopting} },
		PersistentAgents: func() []edge.PersistentAgent { return []edge.PersistentAgent{{Name: "watcher", Node: "n_self"}} },
		AgentJobs: func() []edge.AgentJob {
			return []edge.AgentJob{{Name: "watcher", Node: "n_self", Model: "qwen3-8b", Share: true, Job: "serve"}}
		},
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Adopting) != 1 || snap.Adopting[0].Name != "gentle-ibex" {
		t.Fatalf("adopting = %v, want the phone", snap.Adopting)
	}
	if len(snap.PersistentAgents) != 1 || snap.PersistentAgents[0].Name != "watcher" {
		t.Fatalf("persistent agents = %v", snap.PersistentAgents)
	}
	if len(snap.AgentJobs) != 1 || snap.AgentJobs[0].Model != "qwen3-8b" || snap.AgentJobs[0].Job != "serve" {
		t.Fatalf("agent jobs = %v", snap.AgentJobs)
	}
}

func TestConsoleJobBindAndClearThroughHost(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	var setJobs []edge.AgentJob
	var cleared []string
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Self:          "workshop",
		Fleet:         fleet,
		SetAgentJob:   func(j edge.AgentJob) error { setJobs = append(setJobs, j); return nil },
		ClearAgentJob: func(name string) error { cleared = append(cleared, name); return nil },
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp := postJSONTok(t, srv, s, "/api/edge/job",
		`{"name":"watcher","model":"qwen3-8b","share":true,"job":"serve","targets":["pi","attic"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job = %d, want 200", resp.StatusCode)
	}
	if len(setJobs) != 1 {
		t.Fatalf("SetAgentJob calls = %d, want 1", len(setJobs))
	}
	j := setJobs[0]
	if j.Name != "watcher" || j.Model != "qwen3-8b" || !j.Share || j.Job != "serve" || len(j.Targets) != 2 {
		t.Fatalf("job not carried through: %+v", j)
	}

	resp2 := postJSONTok(t, srv, s, "/api/edge/job/clear", `{"name":"watcher"}`)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("job clear = %d, want 200", resp2.StatusCode)
	}
	if len(cleared) != 1 || cleared[0] != "watcher" {
		t.Fatalf("ClearAgentJob calls = %v", cleared)
	}
}

func TestConsoleJobNeedsTokenAndName(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Fleet:         fleet,
		SetAgentJob:   func(edge.AgentJob) error { return nil },
		ClearAgentJob: func(string) error { return nil },
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	// no token
	resp, _ := http.Post(srv.URL+"/api/edge/job", "application/json", strings.NewReader(`{"name":"x"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job no token = %d, want 403", resp.StatusCode)
	}
	// missing name
	r2 := postJSONTok(t, srv, s, "/api/edge/job", `{"model":"m"}`)
	r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Fatalf("job no name = %d, want 400", r2.StatusCode)
	}
}

// The console's Grants surface issues, lists and revokes authority-signed grants through the host -
// the invoke fabric's grant surface (features/edge/control.feature).
func TestConsoleGrantIssueListRevoke(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	var issued [][4]string
	revoked := []string{}
	live := []store.Grant{}
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Self:   "workshop",
		Fleet:  fleet,
		Grants: func() ([]store.Grant, error) { return live, nil },
		IssueGrant: func(caller, capability, callee, forDur string) (store.Grant, error) {
			issued = append(issued, [4]string{caller, capability, callee, forDur})
			g := store.Grant{ID: "grant_x", Caller: caller, Nodes: []string{callee}, Caps: []string{capability}}
			live = append(live, g)
			return g, nil
		},
		RevokeGrant: func(id string) error { revoked = append(revoked, id); return nil },
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp := postJSONTok(t, srv, s, "/api/edge/grant",
		`{"caller":"self","capability":"operate","callee":"n_ws","for":"7d"}`)
	var snap edgeSnap
	json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issue = %d, want 200", resp.StatusCode)
	}
	if len(issued) != 1 || issued[0] != [4]string{"self", "operate", "n_ws", "7d"} {
		t.Fatalf("IssueGrant call = %v", issued)
	}
	if len(snap.Grants) != 1 || snap.Grants[0].Caps != "operate" || snap.Grants[0].Callee != "n_ws" {
		t.Fatalf("snapshot grants = %+v", snap.Grants)
	}

	resp2 := postJSONTok(t, srv, s, "/api/edge/grant/revoke", `{"id":"grant_x"}`)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || len(revoked) != 1 || revoked[0] != "grant_x" {
		t.Fatalf("revoke: status=%d revoked=%v", resp2.StatusCode, revoked)
	}
}

func TestConsoleGrantIssueNeedsFields(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Fleet:       fleet,
		IssueGrant:  func(string, string, string, string) (store.Grant, error) { return store.Grant{}, nil },
		RevokeGrant: func(string) error { return nil },
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	// missing callee
	r := postJSONTok(t, srv, s, "/api/edge/grant", `{"capability":"operate"}`)
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("issue missing callee = %d, want 400", r.StatusCode)
	}
	// no token
	r2, _ := http.Post(srv.URL+"/api/edge/grant", "application/json", strings.NewReader(`{"capability":"operate","callee":"n"}`))
	r2.Body.Close()
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("issue no token = %d, want 403", r2.StatusCode)
	}
}

// health.feature §5: a firing rule shows on the console's fleet surface too - the snapshot carries
// each node's firing alerts by rule, and nothing else (no thresholds invented in the browser).
func TestConsoleSnapshotCarriesFiringAlerts(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	if _, err := fleet.Enroll(store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified)}); err != nil {
		t.Fatal(err)
	}
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "workshop", Fleet: fleet,
		Alerts: func() map[string][]string { return map[string][]string{"n_nas": {"disk > 75"}} }}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 1 || len(snap.Nodes[0].Alerts) != 1 || snap.Nodes[0].Alerts[0] != "disk > 75" {
		t.Fatalf("nodes = %+v, want nas carrying its firing rule", snap.Nodes)
	}
}

// The floor on the console, the same facts the TUI's ANDON and TROUBLE show: each unit's state
// in words with time in state, its airtime and log strip; the standing trouble calls with why/do;
// the pile-up rate and the sign-off line. Roger and stand by act through the host, token-gated,
// and are honestly absent when unwired.
func TestConsoleSnapshotCarriesTheFloorAndActsOnCalls(t *testing.T) {
	now := time.Unix(1790000000, 0)
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	if _, err := fleet.Enroll(store.EdgeNode{ID: "n_nas", Name: "nas", Kind: "host", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := fleet.Enroll(store.EdgeNode{ID: "n_ibex", Name: "gentle-ibex-14", Kind: "mobile", Presence: string(edge.PresenceDark), LastSeen: now.Add(-26 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	var samples []edge.StateSample
	for i := 30; i > 0; i-- {
		st := edge.StateAir
		if i <= 6 {
			st = edge.StateTrouble
		}
		samples = append(samples, edge.StateSample{At: now.Add(-time.Duration(i) * time.Minute).Unix(),
			States: map[string]edge.UnitState{"n_nas": st, "n_ibex": edge.StateDark}})
	}
	var rogered, stoodBy []string
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "workshop", Fleet: fleet, Now: func() time.Time { return now },
		Alerts: func() map[string][]string {
			return map[string][]string{"n_nas": {"disk > 90"}, "n_ibex": {"offline > 5"}}
		},
		Floor: func(at time.Time) EdgeFloor {
			return EdgeFloor{Samples: samples, Rate: 2, Reads: map[string]int64{"n_ibex": now.Add(-90 * time.Second).Unix()},
				Calls: []edge.TroubleCall{
					{RuleID: "disk", NodeID: "n_nas", Node: "nas", Metric: edge.MetricDisk, When: "disk > 90", Value: 92, Since: 6 * time.Minute},
					{RuleID: "offline", NodeID: "n_ibex", Node: "gentle-ibex-14", Metric: edge.MetricOffline, When: "offline > 5", Value: 1560, Since: 26 * time.Hour, Acked: true, AckBy: "luis"},
					{RuleID: "cpu_temp", NodeID: "n_nas", Node: "nas", Metric: edge.MetricCPUTemp, When: "cpu_temp > 85", Value: 90, Since: time.Hour, Shelved: true, StandByUntil: now.Add(time.Hour), StandByBy: "gentle-ibex-14"},
				},
				SignOff: edge.SignOffReport{Raised: 3, Cleared: 1, Loudest: "nas", LoudestCount: 2, DeadAir: map[string]time.Duration{"n_ibex": 8 * time.Hour}}}
		},
		Roger: func(ruleID, nodeID string) error { rogered = append(rogered, ruleID+"@"+nodeID); return nil },
		StandBy: func(ruleID, nodeID string, d time.Duration) error {
			stoodBy = append(stoodBy, ruleID+"@"+nodeID+" "+d.String())
			return nil
		},
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.Floor == nil || !snap.FloorManage {
		t.Fatalf("the floor and its actions should be on the snapshot: %+v", snap.Floor)
	}
	units := map[string]edgeUnitJSON{}
	for _, u := range snap.Floor.Units {
		units[u.ID] = u
	}
	if u := units["n_nas"]; u.State != "TROUBLE" || u.SinceS < 5*60 || u.Airtime != 100 || len(u.Log) != 60 || u.Log[59] != "trouble" {
		t.Fatalf("nas = %+v", u)
	}
	if u := units["n_ibex"]; u.State != "DARK" || u.SinceS < 25*3600 || u.Airtime != 0 || u.LastRead != now.Add(-90*time.Second).Unix() {
		t.Fatalf("ibex = %+v (dark since its last-seen, airtime 0, and when its app last read the feed)", u)
	}
	if len(snap.Floor.Calls) != 3 || snap.Floor.Calls[0].Priority != "HI" || snap.Floor.Calls[0].Why == "" || snap.Floor.Calls[0].Do == "" {
		t.Fatalf("calls = %+v (priority first, each with why and do)", snap.Floor.Calls)
	}
	if last := snap.Floor.Calls[2]; !last.Shelved || last.StandBy != "gentle-ibex-14" {
		t.Fatalf("a call on stand by sorts last and names who quieted it: %+v", last)
	}
	if snap.Floor.Calls[1].AckBy != "luis" || snap.Floor.Rate != 2 || snap.Floor.SignOff.Raised != 3 || snap.Floor.SignOff.DeadAir[0].Node != "gentle-ibex-14" {
		t.Fatalf("floor = %+v", snap.Floor)
	}

	r2 := postJSONTok(t, srv, s, "/api/edge/roger", `{"rule":"disk","node":"n_nas"}`)
	if r2.StatusCode != http.StatusOK || len(rogered) != 1 || rogered[0] != "disk@n_nas" {
		t.Fatalf("roger: %d %v", r2.StatusCode, rogered)
	}
	r3 := postJSONTok(t, srv, s, "/api/edge/standby", `{"rule":"disk","node":"n_nas","hours":1}`)
	if r3.StatusCode != http.StatusOK || len(stoodBy) != 1 || stoodBy[0] != "disk@n_nas 1h0m0s" {
		t.Fatalf("stand by: %d %v", r3.StatusCode, stoodBy)
	}
	// No token, no action.
	r4, _ := http.Post(srv.URL+"/api/edge/roger", "application/json", strings.NewReader(`{"rule":"disk","node":"n_nas"}`))
	if r4.StatusCode == http.StatusOK || len(rogered) != 1 {
		t.Fatalf("an untokened roger must be refused: %d", r4.StatusCode)
	}

	// Unwired: the snapshot says so and the endpoints refuse.
	s2 := New(testCtrl(), Options{Edge: EdgeHooks{Self: "workshop", Fleet: fleet}})
	srv2 := httptest.NewServer(s2.Handler())
	t.Cleanup(srv2.Close)
	if r := postJSONTok(t, srv2, s2, "/api/edge/roger", `{"rule":"disk","node":"n_nas"}`); r.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unwired roger = %d, want 501", r.StatusCode)
	}
}

// An empty body is not "agent off": the toggle needs an explicit on.
func TestConsoleAgentToggleNeedsAnExplicitValue(t *testing.T) {
	var got []bool
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "workshop", Fleet: edge.NewFleet(store.NewMem(), "acct-1"),
		SetAgent: func(on bool) error { got = append(got, on); return nil }, AddAgent: func() error { return nil }, SetPrefer: func(string) error { return nil }}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	if r := postJSONTok(t, srv, s, "/api/edge/agent", `{}`); r.StatusCode != http.StatusBadRequest || len(got) != 0 {
		t.Fatalf("empty body = %d, got %v", r.StatusCode, got)
	}
	if r := postJSONTok(t, srv, s, "/api/edge/agent", `{"on":false}`); r.StatusCode != http.StatusOK || len(got) != 1 || got[0] {
		t.Fatalf("explicit off = %d, got %v", r.StatusCode, got)
	}
}

// The console card carries the inventory line and the BEHIND mark the TUI carries, worded on the
// server (features/edge/inventory.feature).
func TestConsoleSnapshotCarriesSoftwareAndWhoIsBehind(t *testing.T) {
	db := store.NewMem()
	fleet := edge.NewFleet(db, "acct-1")
	for _, n := range []store.EdgeNode{
		{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified),
			Instances: []edge.Instance{{Name: "desk", Software: &store.EdgeSoftware{Roger: "6.11.2", OS: "Gentoo Linux", Arch: "amd64"}}}},
		{ID: "n_shed", Name: "shed", Kind: "host", Presence: string(edge.PresenceVerified),
			Instances: []edge.Instance{{Name: "shed", Software: &store.EdgeSoftware{Roger: "6.9.0", OS: "Debian GNU/Linux 12", Arch: "arm64", Machine: "Raspberry Pi 5"}}, {Name: "shed-2"}}},
	} {
		if _, err := fleet.Enroll(n); err != nil {
			t.Fatal(err)
		}
	}
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "desk", Fleet: fleet}})
	snap, err := s.edgeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]edgeSoftwareJSON{}
	for _, n := range snap.Nodes {
		got[n.Name] = n.Software
	}
	if len(got["desk"]) != 1 || got["desk"][0].Words != "roger 6.11.2 · Gentoo Linux · amd64" || got["desk"][0].Behind != "" {
		t.Fatalf("desk: %+v", got["desk"])
	}
	if len(got["shed"]) != 1 || got["shed"][0].Instance != "shed" || got["shed"][0].Behind != "6.11.2" ||
		got["shed"][0].Words != "roger 6.9.0 · Debian GNU/Linux 12 · arm64 · Raspberry Pi 5" {
		t.Fatalf("shed (one line: shed-2 has no record): %+v", got["shed"])
	}
	js, _ := os.ReadFile("assets/console.js")
	if !strings.Contains(string(js), `"   behind " + sw.behind`) || !strings.Contains(string(js), "sw.instance !== i.name") {
		t.Fatal("console.js does not draw the software line under its instance with the behind mark")
	}
}

// The console card carries the owner's words, from the snapshot, and draws no row without them.
func TestConsoleSnapshotCarriesTags(t *testing.T) {
	db := store.NewMem()
	fleet := edge.NewFleet(db, "acct-1")
	for _, n := range []store.EdgeNode{
		{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified)},
		{ID: "n_pi", Name: "quiet-pi", Kind: "host", Presence: string(edge.PresenceVerified)},
	} {
		if _, err := fleet.Enroll(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := fleet.SetTags("n_desk", []string{"production", "gpu"}); err != nil {
		t.Fatal(err)
	}
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "desk", Fleet: fleet}})
	snap, err := s.edgeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range snap.Nodes {
		switch n.Name {
		case "desk":
			if len(n.Tags) != 2 || n.Tags[0] != "gpu" || n.Tags[1] != "production" {
				t.Fatalf("desk tags: %v", n.Tags)
			}
		case "quiet-pi":
			if len(n.Tags) != 0 {
				t.Fatalf("quiet-pi carries no tags, got %v", n.Tags)
			}
		}
	}
	js, _ := os.ReadFile("assets/console.js")
	if !strings.Contains(string(js), `if ((n.tags || []).length) edgeKV(card, "tags"`) {
		t.Fatal("console.js does not draw the tags row only when there are tags")
	}
}

// Console parity for the timeline and the maintenance mark: the browser draws what the terminal's
// EVENTS tab draws, worded on this side (features/edge/events.feature, maintenance.feature).
func TestConsoleCarriesTheTimelineAndTheMaintenanceMark(t *testing.T) {
	db := store.NewMem()
	fleet := edge.NewFleet(db, "acct-1")
	for _, n := range []store.EdgeNode{
		{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified),
			Caps: []store.EdgeCap{{Name: string(edge.Maintain), State: string(edge.Claimed)}}},
		{ID: "n_pi", Name: "quiet-pi", Kind: "host", Presence: string(edge.PresenceVerified)},
	} {
		if _, err := fleet.Enroll(n); err != nil {
			t.Fatal(err)
		}
	}
	asked := 0
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "desk", Fleet: fleet,
		Events: func(now time.Time, limit int) []EdgeEvent {
			asked = limit
			return []EdgeEvent{
				{Age: "2m ago", Node: "desk", What: "raised cpu_temp", Detail: "95.5°C, over 85°C", Alarm: true},
				{Age: "1h ago", Node: "quiet-pi", What: "tagged", Detail: "production"},
			}
		}}})
	snap, err := s.edgeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if asked != edgeConsoleEvents {
		t.Fatalf("the tab asks for %d lines, got %d", edgeConsoleEvents, asked)
	}
	if len(snap.Events) != 2 || snap.Events[0].What != "raised cpu_temp" || !snap.Events[0].Alarm {
		t.Fatalf("events: %+v", snap.Events)
	}
	if snap.Events[1].Alarm {
		t.Error("a node event is not an alarm")
	}
	byName := map[string]bool{}
	for _, n := range snap.Nodes {
		byName[n.Name] = n.Maintain
	}
	if !byName["desk"] {
		t.Error("a node that declares maintain says so")
	}
	if byName["quiet-pi"] {
		t.Error("one that does not declare it says nothing")
	}

	// With no hook wired the tab simply has no list, and says so rather than breaking.
	bare := New(testCtrl(), Options{Edge: EdgeHooks{Self: "desk", Fleet: fleet}})
	snap, err = bare.edgeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Events == nil || len(snap.Events) != 0 {
		t.Fatalf("an unwired hook is an empty list, never nil: %+v", snap.Events)
	}

	js, _ := os.ReadFile("assets/console.js")
	html, _ := os.ReadFile("assets/console.html")
	for _, want := range []string{"function edgeRenderEvents(d)", "edgeRenderEvents(d);", `$("edge-events")`,
		"Nothing has happened on this Edge yet.", `if (n.maintain) edgeKV(card, "service"`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("console.js is missing %q", want)
		}
	}
	if !strings.Contains(string(html), `id="edge-events"`) {
		t.Error("console.html has no events list")
	}
}

// Console parity for what the owner asked of this Edge and what they left standing: the browser draws
// what the terminal shows, worded on this side (features/edge/drift.feature, schedule.feature).
func TestConsoleCarriesTheIntentAndTheSchedules(t *testing.T) {
	db := store.NewMem()
	fleet := edge.NewFleet(db, "acct-1")
	if _, err := fleet.Enroll(store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host",
		Presence: string(edge.PresenceVerified)}); err != nil {
		t.Fatal(err)
	}
	asked := false
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "desk", Fleet: fleet,
		Intent: func(now time.Time) EdgeFleetIntent {
			asked = true
			return EdgeFleetIntent{
				Expect: []EdgeExpectLine{
					{Expected: "roger 6.13.0", Of: "the fleet", Differ: 1},
					{Expected: "agent", Of: "tag production"},
				},
				Drift: []EdgeDriftLine{
					{Node: "shed", Expected: "roger 6.13.0", Found: "runs 6.9.0", Dark: true},
					{Node: "unseen", Expected: "roger 6.13.0", Found: "it has not reported which roger it runs", Unknown: true},
				},
				Schedule: []EdgeScheduleLine{
					{ID: "job_1", What: "restart watcher", Of: "tag production", Cadence: "every 24h",
						Last: "2m ago", Next: "in 23h", Runs: []string{"shed: asked it to stop, and it has gone."}},
				},
			}
		}}})
	snap, err := s.edgeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatal("the tab never asked for the intent")
	}
	if len(snap.Expect) != 2 || snap.Expect[0].Differ != 1 || snap.Expect[1].Differ != 0 {
		t.Fatalf("expect: %+v", snap.Expect)
	}
	if len(snap.Drift) != 2 || !snap.Drift[0].Dark || !snap.Drift[1].Unknown {
		t.Fatalf("drift: %+v", snap.Drift)
	}
	if len(snap.Schedule) != 1 || snap.Schedule[0].ID != "job_1" || len(snap.Schedule[0].Runs) != 1 {
		t.Fatalf("schedule: %+v", snap.Schedule)
	}

	// With no hook wired the sections are simply empty, never nil.
	bare := New(testCtrl(), Options{Edge: EdgeHooks{Self: "desk", Fleet: fleet}})
	snap, err = bare.edgeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]int{"expect": len(snap.Expect), "drift": len(snap.Drift), "schedule": len(snap.Schedule)} {
		if got != 0 {
			t.Errorf("%s should be empty with no hook, got %d", name, got)
		}
	}
	if snap.Expect == nil || snap.Drift == nil || snap.Schedule == nil {
		t.Error("empty, never nil: the browser iterates them")
	}

	js, _ := os.ReadFile("assets/console.js")
	html, _ := os.ReadFile("assets/console.html")
	for _, want := range []string{"function edgeRenderIntent(d)", "edgeRenderIntent(d);",
		`$("edge-expect")`, `$("edge-drift")`, `$("edge-schedule")`, "This Edge is as you asked."} {
		if !strings.Contains(string(js), want) {
			t.Errorf("console.js is missing %q", want)
		}
	}
	// An unknown must be marked as unknown rather than painted as a wrong.
	if !strings.Contains(string(js), `x.unknown ? "" : " live"`) {
		t.Error("console.js paints an unknown as a drift")
	}
	for _, id := range []string{`id="edge-expect"`, `id="edge-drift"`, `id="edge-schedule"`} {
		if !strings.Contains(string(html), id) {
			t.Errorf("console.html has no %s", id)
		}
	}
}
