package webui

// THE CONSOLE'S REMAINING FLEET OPERATIONS - the three things an owner could do from the terminal
// and not from the browser: label a unit, decide what raises an alarm, and service a unit.
//
//   tags      features/edge/tags.feature   - the owner's words for what a unit IS. The console drew
//                                            them and could not change them; now it can, through the
//                                            SAME Fleet the TUI and CLI edit, so the history line and
//                                            the validation are one implementation.
//   rules     features/edge/health.feature, features/edge/manage_web.feature §3 - "setting an alert
//                                            rule from the dashboard stores it and takes effect".
//                                            The console showed firing alerts and could not say what
//                                            fires. Set / off / remove now go through the host.
//   maintain  features/edge/maintenance.feature, features/edge/logs.feature - start, stop, restart a
//                                            persistent agent on a member, and read its redacted log
//                                            tail. One hook, because they are ONE path: a maintenance
//                                            invoke, gated by the owner's switch on that machine and
//                                            an authority-signed grant carrying maintain. The console
//                                            adds no authorization of its own and no new path.
//
// Real dependencies throughout: a real Server over httptest with its real localhost+token guard, a
// real edge.Fleet over a real store. No mocks.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// fleetOpsRig is a console over a two-member Edge with every fleet-operation hook wired, and the
// record of what each hook was asked to do.
type fleetOpsCalls struct {
	rules      []edge.Rule
	setRule    []string // "metric|op|value|scope", the words as typed
	ruleOff    []string // "id=true"
	removeRule []string
	maintain   []string // "node|action|target|lines"
	result     edge.MaintainResult
	maintErr   error
}

func fleetOpsRig(t *testing.T) (*Server, *httptest.Server, *edge.Fleet, *fleetOpsCalls) {
	t.Helper()
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	for _, n := range []store.EdgeNode{
		{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified)},
		{ID: "n_shed", Name: "shed", Kind: "host", Presence: string(edge.PresenceVerified)},
	} {
		if _, err := fleet.Enroll(n); err != nil {
			t.Fatal(err)
		}
	}
	c := &fleetOpsCalls{
		rules: []edge.Rule{
			{ID: "cpu_temp-fleet", Metric: edge.MetricCPUTemp, Op: edge.OpGT, Value: 85, Default: true},
			{ID: "disk-tag-production", Metric: edge.MetricDisk, Op: edge.OpGT, Value: 80, Scope: "tag:production"},
			{ID: "disk-n_shed", Metric: edge.MetricDisk, Op: edge.OpGT, Value: 95, Scope: "n_shed", Off: true},
		},
		result: edge.MaintainResult{Action: edge.MaintainLogs, Says: "12 lines", Lines: []string{"a", "b"}, Held: 200},
	}
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Self:  "workshop",
		Fleet: fleet,
		Rules: func() ([]edge.Rule, error) { return c.rules, nil },
		SetRule: func(metric, op, value, scope string) (string, error) {
			// The host refuses a rule that cannot mean what it says, with the real edge validators -
			// the console must pass the words on and surface that refusal, not derive its own.
			if !edge.ValidMetric(edge.Metric(metric)) {
				return "", fmt.Errorf("unknown metric %q", metric)
			}
			if edge.Op(op) != edge.OpGT && edge.Op(op) != edge.OpLT {
				return "", fmt.Errorf("comparison must be gt or lt, not %q", op)
			}
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return "", fmt.Errorf("value must be a number, not %q", value)
			}
			c.setRule = append(c.setRule, strings.Join([]string{metric, op, value, scope}, "|"))
			return "rule disk-fleet: disk > 90 on every node - it takes effect on the next health read", nil
		},
		RuleOff: func(id string, off bool) error {
			c.ruleOff = append(c.ruleOff, id+"="+map[bool]string{true: "true", false: "false"}[off])
			return nil
		},
		RemoveRule: func(id string) error { c.removeRule = append(c.removeRule, id); return nil },
		Maintain: func(node string, tk edge.MaintainTask) (edge.MaintainResult, error) {
			c.maintain = append(c.maintain, node+"|"+string(tk.Action)+"|"+tk.Target+"|"+strconv.Itoa(tk.Lines))
			return c.result, c.maintErr
		},
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, fleet, c
}

func readSnap(t *testing.T, s *Server, srv *httptest.Server) edgeSnap {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func decodeSnap(t *testing.T, resp *http.Response) edgeSnap {
	t.Helper()
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

// ---------------------------------------------------------------- tags -----

// tags.feature: "+word adds, -word removes, a bare list replaces, --clear empties". The console
// edits through the SAME Fleet methods the terminal uses, so the validation, the sort and the
// history line are not written twice.
func TestConsoleTagsAddAndRemoveThroughTheFleet(t *testing.T) {
	s, srv, fleet, _ := fleetOpsRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/tags", `{"id":"n_desk","add":["Production","  gpu "],"remove":["bench"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tags = %d, want 200", resp.StatusCode)
	}
	n, _, _ := fleet.Get("n_desk")
	if len(n.Tags) != 2 || n.Tags[0] != "gpu" || n.Tags[1] != "production" {
		t.Fatalf("tags = %v, want gpu + production, normalised and sorted", n.Tags)
	}
	// Every change is on the node's history (tags.feature).
	last := n.History[len(n.History)-1]
	if last.What != "tagged" {
		t.Fatalf("history = %+v, want a tagged line", n.History)
	}
	// The fresh snapshot returns, as every other console action does.
	snap := decodeSnap(t, resp)
	for _, got := range snap.Nodes {
		if got.ID == "n_desk" && len(got.Tags) != 2 {
			t.Fatalf("snapshot node = %+v, want the new tags", got)
		}
	}
}

func TestConsoleTagsReplaceAndClear(t *testing.T) {
	s, srv, fleet, _ := fleetOpsRig(t)
	r1 := postJSONTok(t, srv, s, "/api/edge/tags", `{"id":"n_desk","set":["bench","lab"]}`)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("set = %d, want 200", r1.StatusCode)
	}
	n, _, _ := fleet.Get("n_desk")
	if len(n.Tags) != 2 || n.Tags[0] != "bench" || n.Tags[1] != "lab" {
		t.Fatalf("tags = %v, want exactly bench + lab", n.Tags)
	}
	r2 := postJSONTok(t, srv, s, "/api/edge/tags", `{"id":"n_desk","clear":true}`)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("clear = %d, want 200", r2.StatusCode)
	}
	n, _, _ = fleet.Get("n_desk")
	if len(n.Tags) != 0 {
		t.Fatalf("tags = %v, want none after clear", n.Tags)
	}
}

// A word that cannot be a tag is refused in the fleet's own words, and nothing is written - the
// console must not invent its own validation, nor half-apply a list.
func TestConsoleTagsRefuseWhatIsNotATag(t *testing.T) {
	s, srv, fleet, _ := fleetOpsRig(t)
	for _, body := range []string{
		`{"id":"n_desk","add":["tag:prod"]}`,
		`{"id":"n_desk","add":["a b"]}`,
		`{"id":"n_desk","set":["ÜBER"]}`,
		`{"id":"n_desk","add":["gpu"],"set":["bench"]}`, // mixing an edit with a replace is refused, never guessed at
		`{"id":"n_desk","set":["bench"],"clear":true}`,
		`{"id":"n_desk"}`, // says nothing at all
	} {
		resp := postJSONTok(t, srv, s, "/api/edge/tags", body)
		msg, _ := readBodyString(resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", body, resp.StatusCode)
		}
		if strings.TrimSpace(msg) == "" {
			t.Fatalf("%s refused with no reason", body)
		}
	}
	if n, _, _ := fleet.Get("n_desk"); len(n.Tags) != 0 {
		t.Fatalf("tags = %v, want nothing written by a refused edit", n.Tags)
	}
}

func TestConsoleTagsRefuseAnUnknownNode(t *testing.T) {
	s, srv, _, _ := fleetOpsRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/tags", `{"id":"n_nope","add":["gpu"]}`)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("tagging an unknown node = 200, want a refusal")
	}
}

// ---------------------------------------------------------------- rules ----

// health.feature: the rules in force, each in the words the terminal prints, with the scope said in
// the owner's terms and whether it is off. manage_web.feature §3: the dashboard can set one.
func TestConsoleSnapshotCarriesTheAlertRules(t *testing.T) {
	s, srv, _, _ := fleetOpsRig(t)
	snap := readSnap(t, s, srv)
	if !snap.RulesManage {
		t.Fatalf("rules_manage = false, want true when the rule hooks are wired")
	}
	if len(snap.Rules) != 3 {
		t.Fatalf("rules = %+v, want the three in force", snap.Rules)
	}
	if snap.Rules[0].Words != "cpu_temp > 85" || snap.Rules[0].Of != "every node" {
		t.Fatalf("fleet rule = %+v, want its words and every node", snap.Rules[0])
	}
	if snap.Rules[1].Of != "every node tagged production" {
		t.Fatalf("tag rule = %+v, want the tag said in the owner's terms", snap.Rules[1])
	}
	// A node-scoped rule names the NODE, not its id: the console prints what the owner called it.
	if snap.Rules[2].Of != "shed" || !snap.Rules[2].Off {
		t.Fatalf("node rule = %+v, want shed and off", snap.Rules[2])
	}
	if snap.Rules[0].ID != "cpu_temp-fleet" || !snap.Rules[0].Default {
		t.Fatalf("rule identity lost: %+v", snap.Rules[0])
	}
}

// The words go to the host AS TYPED: the rule id and the scope are derived once, where `roger edge
// alert set` derives them, so the browser and the terminal cannot make two different rules.
func TestConsoleSetRuleSendsTheWordsToTheHost(t *testing.T) {
	s, srv, _, c := fleetOpsRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/rule", `{"metric":"disk","op":"gt","value":"90","scope":"tag:production"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := readBodyString(resp)
		t.Fatalf("rule = %d (%s), want 200", resp.StatusCode, msg)
	}
	if len(c.setRule) != 1 || c.setRule[0] != "disk|gt|90|tag:production" {
		t.Fatalf("host SetRule = %v, want the words as typed", c.setRule)
	}
	// The host's own sentence comes back with the fresh snapshot, so the browser says what the
	// terminal says rather than composing a second version of it.
	var out struct {
		Says string   `json:"says"`
		Edge edgeSnap `json:"edge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Says, "takes effect on the next health read") {
		t.Fatalf("says = %q, want the host's sentence", out.Says)
	}
	if len(out.Edge.Rules) != 3 {
		t.Fatalf("the fresh snapshot did not come back: %+v", out.Edge.Rules)
	}
}

func TestConsoleSetRuleRefusesWhatCannotMeanWhatItSays(t *testing.T) {
	s, srv, _, c := fleetOpsRig(t)
	for _, body := range []string{
		`{"op":"gt","value":"90"}`,                     // no metric
		`{"metric":"disk","value":"90"}`,               // no comparison
		`{"metric":"disk","op":"gt"}`,                  // no value
		`{"metric":"disk","op":"gt","value":"ninety"}`, // not a number
		`{"metric":"disk","op":"near","value":"90"}`,   // not a comparison
		`{"metric":"weather","op":"gt","value":"90"}`,  // not a metric
	} {
		resp := postJSONTok(t, srv, s, "/api/edge/rule", body)
		msg, _ := readBodyString(resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", body, resp.StatusCode)
		}
		if strings.TrimSpace(msg) == "" {
			t.Fatalf("%s refused with no reason", body)
		}
	}
	if len(c.setRule) != 0 {
		t.Fatalf("host SetRule called for a refused rule: %v", c.setRule)
	}
}

func TestConsoleRuleOffAndRemoveThroughTheHost(t *testing.T) {
	s, srv, _, c := fleetOpsRig(t)
	r1 := postJSONTok(t, srv, s, "/api/edge/rule/off", `{"id":"cpu_temp-fleet","off":true}`)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("off = %d, want 200", r1.StatusCode)
	}
	if len(c.ruleOff) != 1 || c.ruleOff[0] != "cpu_temp-fleet=true" {
		t.Fatalf("host RuleOff = %v", c.ruleOff)
	}
	r2 := postJSONTok(t, srv, s, "/api/edge/rule/off", `{"id":"cpu_temp-fleet","off":false}`)
	r2.Body.Close()
	if len(c.ruleOff) != 2 || c.ruleOff[1] != "cpu_temp-fleet=false" {
		t.Fatalf("host RuleOff = %v, want the on half too", c.ruleOff)
	}
	r3 := postJSONTok(t, srv, s, "/api/edge/rule/remove", `{"id":"disk-n_shed"}`)
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("remove = %d, want 200", r3.StatusCode)
	}
	if len(c.removeRule) != 1 || c.removeRule[0] != "disk-n_shed" {
		t.Fatalf("host RemoveRule = %v", c.removeRule)
	}
	// An id is required: a body with none must not remove "the first one".
	r4 := postJSONTok(t, srv, s, "/api/edge/rule/remove", `{}`)
	r4.Body.Close()
	if r4.StatusCode != http.StatusBadRequest || len(c.removeRule) != 1 {
		t.Fatalf("remove with no id = %d, calls %v", r4.StatusCode, c.removeRule)
	}
	r5 := postJSONTok(t, srv, s, "/api/edge/rule/off", `{"id":""}`)
	r5.Body.Close()
	if r5.StatusCode != http.StatusBadRequest {
		t.Fatalf("off with no id = %d, want 400", r5.StatusCode)
	}
}

// ------------------------------------------------------------- maintain ----

// logs.feature: the tail is a MAINTENANCE ACTION, not a path of its own, and the lines arrive
// already redacted on the serving side. The console asks for it exactly as the CLI does.
func TestConsoleLogTailRidesTheMaintenancePath(t *testing.T) {
	s, srv, _, c := fleetOpsRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/maintain", `{"node":"shed","action":"logs","lines":20}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := readBodyString(resp)
		t.Fatalf("maintain logs = %d (%s), want 200", resp.StatusCode, msg)
	}
	if len(c.maintain) != 1 || c.maintain[0] != "shed|logs||20" {
		t.Fatalf("host Maintain = %v, want the log task for shed", c.maintain)
	}
	var out edge.MaintainResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 2 || out.Held != 200 || out.Says != "12 lines" {
		t.Fatalf("result = %+v, want the callee's lines, its ring size and its words", out)
	}
}

func TestConsoleMaintainStartsStopsAndRestartsANamedAgent(t *testing.T) {
	s, srv, _, c := fleetOpsRig(t)
	for _, act := range []string{"start", "stop", "restart"} {
		resp := postJSONTok(t, srv, s, "/api/edge/maintain", `{"node":"shed","action":"`+act+`","target":"watcher"}`)
		msg, _ := readBodyString(resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("maintain %s = %d (%s), want 200", act, resp.StatusCode, msg)
		}
	}
	want := []string{"shed|start|watcher|0", "shed|stop|watcher|0", "shed|restart|watcher|0"}
	for i, w := range want {
		if i >= len(c.maintain) || c.maintain[i] != w {
			t.Fatalf("host Maintain = %v, want %v", c.maintain, want)
		}
	}
}

// maintenance.feature: reboot and shutdown are refused BY NAME until they are built, and an action
// that acts on an agent must name one. Refused before anything leaves this machine.
func TestConsoleMaintainRefusesWhatIsNotAMaintenanceAction(t *testing.T) {
	s, srv, _, c := fleetOpsRig(t)
	for _, body := range []string{
		`{"node":"shed","action":"reboot"}`,
		`{"node":"shed","action":"shutdown"}`,
		`{"node":"shed","action":"rm -rf /"}`,
		`{"node":"shed","action":"start"}`, // no agent named
		`{"node":"shed","action":"start","target":"../etc"}`,
		`{"node":"shed"}`,   // no action
		`{"action":"logs"}`, // no node
	} {
		resp := postJSONTok(t, srv, s, "/api/edge/maintain", body)
		msg, _ := readBodyString(resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", body, resp.StatusCode)
		}
		if strings.TrimSpace(msg) == "" {
			t.Fatalf("%s refused with no reason", body)
		}
	}
	if len(c.maintain) != 0 {
		t.Fatalf("a refused task reached the host: %v", c.maintain)
	}
}

// The host's refusal is the console's refusal: the missing grant, the owner's switch, the node with
// no LAN face. The browser must show the fleet's own words, never a shrug.
func TestConsoleMaintainShowsTheHostsRefusal(t *testing.T) {
	s, srv, _, c := fleetOpsRig(t)
	c.maintErr = fmt.Errorf("%s", edge.MaintainOffReason)
	resp := postJSONTok(t, srv, s, "/api/edge/maintain", `{"node":"shed","action":"logs"}`)
	msg, _ := readBodyString(resp)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a refused maintain = 200, want an error")
	}
	if !strings.Contains(msg, edge.MaintainOffReason) {
		t.Fatalf("body = %q, want the host's own words", msg)
	}
}

// --------------------------------------------------- the guards, on all -----

func TestConsoleFleetOpsAreGatedLikeEveryOtherWrite(t *testing.T) {
	s, srv, _, _ := fleetOpsRig(t)
	paths := []string{"/api/edge/tags", "/api/edge/rule", "/api/edge/rule/off", "/api/edge/rule/remove", "/api/edge/maintain"}
	for _, p := range paths {
		// No token: refused, and nothing ran.
		r1, err := http.Post(srv.URL+p, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		r1.Body.Close()
		if r1.StatusCode != http.StatusForbidden {
			t.Fatalf("%s without token = %d, want 403", p, r1.StatusCode)
		}
		// A GET is CSRF-reachable, so it is refused even WITH the token.
		r2, err := http.Get(srv.URL + p + "?t=" + s.Token())
		if err != nil {
			t.Fatal(err)
		}
		r2.Body.Close()
		if r2.StatusCode == http.StatusOK {
			t.Fatalf("GET %s = 200, want a refusal (write endpoints are POST-only)", p)
		}
	}
}

// A build that wired none of these says so, so the browser can leave the action out rather than
// offering a dead button (agents.feature: a build that cannot perform an action shows no dead button).
func TestConsoleFleetOpsDisabledWhenUnwired(t *testing.T) {
	s := New(testCtrl(), Options{Edge: EdgeHooks{Fleet: edge.NewFleet(store.NewMem(), "acct-1")}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	snap := readSnap(t, s, srv)
	if snap.RulesManage || snap.MaintainManage {
		t.Fatalf("manage flags = %+v, want both false with no hooks wired", snap)
	}
	if snap.Rules == nil {
		t.Fatalf("rules = nil, want an empty list so the browser draws no rules rather than crashing")
	}
	for _, p := range []string{"/api/edge/rule", "/api/edge/rule/off", "/api/edge/rule/remove", "/api/edge/maintain"} {
		resp := postJSONTok(t, srv, s, p, `{"id":"x","node":"y","action":"logs","metric":"disk","op":"gt","value":"90","off":true}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s unwired = %d, want 501", p, resp.StatusCode)
		}
	}
}

// Tags need only the fleet the tab already reads, so a console that lists a fleet can label it.
func TestConsoleTagsNeedNoHookBeyondTheFleet(t *testing.T) {
	s := New(testCtrl(), Options{Edge: EdgeHooks{}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp := postJSONTok(t, srv, s, "/api/edge/tags", `{"id":"n_desk","add":["gpu"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("tags with no fleet at all = %d, want 501", resp.StatusCode)
	}
}

func readBodyString(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// A host that refuses an action says why, and the console passes that through rather than pretending
// the click worked.
func TestConsoleRuleActionsSurfaceTheHostsFailure(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	boom := fmt.Errorf("the alert log could not be written")
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "workshop", Fleet: fleet,
		SetRule:    func(_, _, _, _ string) (string, error) { return "", nil },
		RuleOff:    func(string, bool) error { return boom },
		RemoveRule: func(string) error { return boom },
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	for _, tc := range []struct{ path, body string }{
		{"/api/edge/rule/off", `{"id":"disk","off":true}`},
		{"/api/edge/rule/remove", `{"id":"disk"}`},
	} {
		resp := postJSONTok(t, srv, s, tc.path, tc.body)
		msg, _ := readBodyString(resp)
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(msg, boom.Error()) {
			t.Fatalf("%s = %d (%s), want 502 with the host's words", tc.path, resp.StatusCode, msg)
		}
	}
}

// A rules read that fails must not blank the Edge tab: the fleet, the floor and the sessions still
// matter, exactly as they do when the grants read fails. The section simply shows no rules.
func TestConsoleARuleReadFailureDoesNotBlankTheTab(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	if _, err := fleet.Enroll(store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified)}); err != nil {
		t.Fatal(err)
	}
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "workshop", Fleet: fleet,
		Rules: func() ([]edge.Rule, error) { return nil, fmt.Errorf("unreadable") }}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	snap := readSnap(t, s, srv)
	if len(snap.Rules) != 0 {
		t.Fatalf("rules = %+v, want none from a failed read", snap.Rules)
	}
	if len(snap.Nodes) != 1 {
		t.Fatalf("nodes = %+v, want the fleet still drawn", snap.Nodes)
	}
}

// A rule scoped to a node that is no longer on the Edge still lists, with its scope as written: the
// console never hides a rule it cannot name, and never invents a name for it.
func TestConsoleARuleScopedToAGoneNodeStillLists(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	s := New(testCtrl(), Options{Edge: EdgeHooks{Self: "workshop", Fleet: fleet,
		Rules: func() ([]edge.Rule, error) {
			return []edge.Rule{{ID: "disk-n_gone", Metric: edge.MetricDisk, Op: edge.OpGT, Value: 90, Scope: "n_gone"}}, nil
		}}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	snap := readSnap(t, s, srv)
	if len(snap.Rules) != 1 || snap.Rules[0].Of != "n_gone" {
		t.Fatalf("rules = %+v, want the rule kept with its scope as written", snap.Rules)
	}
}
