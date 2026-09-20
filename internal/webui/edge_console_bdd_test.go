package webui

// Executable spec: features/edge/console_view.feature - the console's EDGE tab.
//
// REAL dependencies, no mocks:
//   - the REAL console Server (New + Handler), driven over httptest with the real token
//     and localhost guard in front, exactly as a browser reaches it;
//   - a REAL internal/edge.Fleet over the REAL internal/store Mem backend, and a REAL
//     internal/edge.Sessions ledger, both fed through their production APIs;
//   - the SHIPPED console.html / console.js / console.css, read from disk, for the rules
//     the browser half must keep (the same way the chat and browse suites pin theirs).
//
// The one fault injected is a store whose read fails: that is the failure path itself,
// exercised at the storage boundary, not a stand-in for behaviour.
//
// Clocks are injected (fleet.SetClock, sessions.SetClock, EdgeHooks.Now), never slept on.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// The wording the tab is specified in, written LITERALLY rather than imported: a spec
// that reads its answer out of the code under test cannot fail when the code changes
// its mind.
const (
	cEscalate = "escalate · right call"
	cGraphMax = 24
)

// failingStore is the storage boundary refusing a read. Everything else is the real Mem.
type failingStore struct {
	store.Store
	err error
}

func (f failingStore) EdgeNodesOfAccount(string) ([]store.EdgeNode, error) { return nil, f.err }

type consoleEdgeBDD struct {
	t *testing.T

	now      time.Time
	db       store.Store
	fleet    *edge.Fleet
	sessions *edge.Sessions
	cands    []store.EdgeNode
	adoptErr error
	noAdopt  bool
	self     *edge.SelfStatus // nil = no status hook wired
	noEdge   bool
	adopts   []string // "<id> <name>" per adopt hook call

	ids  map[string]string
	priv map[string][]byte

	srv  *Server
	http *httptest.Server
	up   *httptest.Server // a stub broker for the console-as-initiator scenarios

	// last read
	status int
	body   []byte
	snap   edgeSnap
	prev   *edgeSnap
	err    string

	// the shipped assets
	html, js, css string
}

func (s *consoleEdgeBDD) reset() {
	s.now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	s.db = store.NewMem()
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.sessions = edge.NewSessions("acct-1")
	s.sessions.SetClock(func() time.Time { return s.now })
	s.cands, s.adoptErr, s.noAdopt, s.noEdge, s.adopts, s.self = nil, nil, false, false, nil, nil
	s.ids, s.priv = map[string]string{}, map[string][]byte{}
	s.status, s.body, s.snap, s.prev, s.err = 0, nil, edgeSnap{}, nil, ""
	if s.http != nil {
		s.http.Close()
		s.http = nil
	}
	if s.up != nil {
		s.up.Close()
		s.up = nil
	}
	s.srv = nil
}

func (s *consoleEdgeBDD) build() {
	if s.srv != nil {
		return
	}
	opts := Options{}
	if s.up != nil {
		opts.Broker, opts.User = s.up.URL, "u"
	}
	if !s.noEdge {
		opts.Edge = EdgeHooks{
			Self:       "workshop",
			Fleet:      s.fleet,
			Candidates: func() []store.EdgeNode { return append([]store.EdgeNode(nil), s.cands...) },
			Sessions:   s.sessions,
			Now:        func() time.Time { return s.now },
		}
		if s.self != nil {
			st := s.self
			opts.Edge.Status = func() edge.SelfStatus { return *st }
		}
		if !s.noAdopt {
			opts.Edge.Adopt = func(id, name string) error {
				s.adopts = append(s.adopts, id+" "+name)
				if s.adoptErr != nil {
					return s.adoptErr
				}
				var c store.EdgeNode
				for _, x := range s.cands {
					if x.ID == id {
						c = x
					}
				}
				if c.ID == "" {
					return errors.New("not a candidate")
				}
				c.Presence, c.Account = string(edge.PresenceVerified), "acct-1"
				if _, err := s.fleet.Enroll(c); err != nil {
					return err
				}
				keep := s.cands[:0]
				for _, x := range s.cands {
					if x.ID != id {
						keep = append(keep, x)
					}
				}
				s.cands = keep
				return nil
			}
		}
	}
	s.srv = New(testCtrl(), opts)
	s.http = httptest.NewServer(s.srv.Handler())
}

func (s *consoleEdgeBDD) do(method, path, token, body string) {
	s.build()
	url := s.http.URL + path
	if token != "" {
		url += "?t=" + token
	}
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	s.status = res.StatusCode
	s.body, _ = io.ReadAll(res.Body)
	s.err = strings.TrimSpace(string(s.body))
	if s.status == http.StatusOK && strings.HasPrefix(path, "/api/edge") {
		if s.snap.Configured || len(s.snap.Nodes) > 0 {
			p := s.snap
			s.prev = &p
		}
		s.snap = edgeSnap{}
		if err := json.Unmarshal(s.body, &s.snap); err != nil {
			s.t.Fatalf("edge JSON: %v\n%s", err, s.body)
		}
	}
}

func (s *consoleEdgeBDD) readEdge() { s.do(http.MethodGet, "/api/edge", s.token(), "") }

func (s *consoleEdgeBDD) token() string { s.build(); return s.srv.Token() }

// ---- fixtures ------------------------------------------------------------

func lanTr(addr string) store.EdgeTransport {
	return store.EdgeTransport{Kind: "lan", Addr: addr, Fingerprint: strings.Repeat("ab", 32)}
}
func relayTr(via string) store.EdgeTransport { return store.EdgeTransport{Kind: "relay", Addr: via} }

func (s *consoleEdgeBDD) enroll(name string, ts []store.EdgeTransport, caps ...edge.Capability) store.EdgeNode {
	s.t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	n := edge.NewNode(pub, name, edge.Host, caps...)
	n.Transports = ts
	got, err := s.fleet.Enroll(n)
	if err != nil {
		s.t.Fatalf("enroll %q: %v", name, err)
	}
	s.ids[name] = got.ID
	s.priv[name] = priv
	return got
}

func (s *consoleEdgeBDD) candidate(name string) store.EdgeNode {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	n := edge.NewNode(pub, name, edge.Host)
	n.Presence = string(edge.PresenceCandidate)
	n.LastSeen = s.now.Unix()
	n.Transports = []store.EdgeTransport{lanTr("192.168.1.44")}
	s.ids[name], s.priv[name] = n.ID, priv
	s.cands = append(s.cands, n)
	return n
}

func (s *consoleEdgeBDD) receipt(req, station, model, void string) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: req, NodeID: station, Model: model, User: "acct-1", VoidReason: void}
}

func (s *consoleEdgeBDD) record(t edge.Traffic) {
	s.t.Helper()
	t.Account = "acct-1"
	if _, err := s.sessions.Record(t); err != nil {
		s.t.Fatalf("record: %v", err)
	}
}

func (s *consoleEdgeBDD) node(name string) (edgeNodeJSON, bool) {
	for _, n := range s.snap.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return edgeNodeJSON{}, false
}

func (s *consoleEdgeBDD) assets() {
	if s.html != "" {
		return
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			s.t.Fatal(err)
		}
		return string(b)
	}
	s.html, s.js, s.css = read("assets/console.html"), read("assets/console.js"), read("assets/console.css")
}

// edgeJS is the EDGE block of console.js, isolated by its markers so an assertion about
// "the Edge code" cannot be satisfied by some other tab's code.
func (s *consoleEdgeBDD) edgeJS() (string, error) {
	s.assets()
	a := strings.Index(s.js, "/* EDGE ---")
	b := strings.Index(s.js, "/* end EDGE */")
	if a < 0 || b < 0 || b < a {
		return "", fmt.Errorf("console.js has no EDGE block between '/* EDGE ---' and '/* end EDGE */'")
	}
	return s.js[a:b], nil
}

func want(cond bool, format string, args ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf(format, args...)
}

// ---- 1. the tab ------------------------------------------------------------

func (s *consoleEdgeBDD) aConsoleOverAFleet() error { return nil }

func (s *consoleEdgeBDD) shellServed() error {
	s.do(http.MethodGet, "/", "", "")
	s.assets()
	return want(s.status == 200 && strings.Contains(string(s.body), `data-tab="edge"`),
		"the served shell has no EDGE tab (status %d)", s.status)
}

func (s *consoleEdgeBDD) tabStripOrder() error {
	s.assets()
	re := regexp.MustCompile(`data-tab="([a-z]+)"`)
	var got []string
	for _, m := range re.FindAllStringSubmatch(s.html, -1) {
		got = append(got, m[1])
	}
	return want(strings.Join(got, ",") == "chat,share,account,browse,edge,settings",
		"tab order = %v", got)
}

func (s *consoleEdgeBDD) panelHiddenUntilChosen() error {
	s.assets()
	re := regexp.MustCompile(`<section[^>]*id="panel-edge"[^>]*>`)
	m := re.FindString(s.html)
	if err := want(m != "", "no panel-edge section"); err != nil {
		return err
	}
	if err := want(strings.Contains(m, "hidden"), "panel-edge is not hidden by default: %s", m); err != nil {
		return err
	}
	return want(strings.Contains(s.js, `"edge"`) && regexp.MustCompile(`TABS\s*=\s*\[[^\]]*"edge"`).MatchString(s.js),
		"console.js TABS does not include edge")
}

func (s *consoleEdgeBDD) hashOpensIt() error {
	s.assets()
	// #hash routing is TABS-driven; being in TABS is what makes #edge work.
	return want(regexp.MustCompile(`TABS\.indexOf\(h\)`).MatchString(s.js), "the hash router is not TABS-driven")
}

func (s *consoleEdgeBDD) shellFetchedNoToken() error { s.do(http.MethodGet, "/", "", ""); return nil }
func (s *consoleEdgeBDD) itIsServedNoNodeData() error {
	return want(s.status == 200 && !strings.Contains(string(s.body), "acct-1"), "shell status %d", s.status)
}
func (s *consoleEdgeBDD) edgeFetchedNoToken() error {
	s.do(http.MethodGet, "/api/edge", "", "")
	return nil
}
func (s *consoleEdgeBDD) edgeFetchedWrongToken() error {
	s.do(http.MethodGet, "/api/edge", "nope", "")
	return nil
}
func (s *consoleEdgeBDD) refusedWith(code int) error {
	return want(s.status == code, "status %d, want %d: %s", s.status, code, s.err)
}

func (s *consoleEdgeBDD) noOutsideScript() error {
	s.assets()
	for _, m := range regexp.MustCompile(`<script[^>]*src="([^"]+)"`).FindAllStringSubmatch(s.html, -1) {
		if !strings.HasPrefix(m[1], "/assets/") {
			return fmt.Errorf("the shell loads a script from outside the console: %s", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`<link[^>]*href="([^"]+)"`).FindAllStringSubmatch(s.html, -1) {
		if !strings.HasPrefix(m[1], "/assets/") {
			return fmt.Errorf("the shell loads a stylesheet from outside the console: %s", m[1])
		}
	}
	return nil
}

func (s *consoleEdgeBDD) inlineSVG() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	return want(strings.Contains(s.html, `<svg`) && strings.Contains(s.html, `id="edge-graph"`) &&
		strings.Contains(js, `createElementNS("http://www.w3.org/2000/svg"`),
		"the Edge graph is not inline SVG drawn by console.js")
}

// ---- 2. the read -----------------------------------------------------------

func (s *consoleEdgeBDD) nodesAndCandidate() error {
	s.enroll("shed", []store.EdgeTransport{lanTr("192.168.1.10")}, edge.Serve, edge.Relay)
	s.enroll("bench", []store.EdgeTransport{lanTr("192.168.1.11")}, edge.Sense)
	s.candidate("attic")
	return nil
}

func (s *consoleEdgeBDD) edgeRead() error { s.readEdge(); return nil } // the Then steps judge the status

func (s *consoleEdgeBDD) namesSelf() error {
	return want(s.snap.Configured && s.snap.Self == "workshop", "self = %q configured=%v", s.snap.Self, s.snap.Configured)
}

func names(ns []edgeNodeJSON) []string {
	out := []string{}
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return out
}

func (s *consoleEdgeBDD) listsExactlyNodes(a, b string) error {
	got := names(s.snap.Nodes)
	return want(len(got) == 2 && ((got[0] == a && got[1] == b) || (got[0] == b && got[1] == a)),
		"nodes = %v, want exactly %q and %q", got, a, b)
}

func (s *consoleEdgeBDD) listsExactlyCandidate(name string) error {
	got := names(s.snap.Candidates)
	if err := want(len(got) == 1 && got[0] == name, "candidates = %v", got); err != nil {
		return err
	}
	for _, n := range names(s.snap.Nodes) {
		if n == name {
			return fmt.Errorf("the candidate %q is also listed as a node", name)
		}
	}
	return nil
}

func (s *consoleEdgeBDD) carriesTheMoment() error {
	return want(s.snap.At == s.now.Unix(), "at = %d, want %d", s.snap.At, s.now.Unix())
}

func (s *consoleEdgeBDD) noPrivateKeyMaterial() error {
	body := string(s.body)
	for name, priv := range s.priv {
		if strings.Contains(body, hex.EncodeToString(priv)) || strings.Contains(body, hex.EncodeToString(priv[:32])) {
			return fmt.Errorf("%s's private key is in the Edge JSON", name)
		}
	}
	return nil
}

func (s *consoleEdgeBDD) edgeRequestedPOST() error {
	s.enroll("shed", []store.EdgeTransport{lanTr("192.168.1.10")}, edge.Serve)
	s.do(http.MethodPost, "/api/edge", s.token(), `{}`)
	return nil
}

func (s *consoleEdgeBDD) nothingChanged() error {
	list, err := s.fleet.List()
	if err != nil {
		return err
	}
	return want(len(list) == 1 && list[0].Name == "shed" && len(s.adopts) == 0, "the fleet changed: %v adopts=%v", names2(list), s.adopts)
}

func names2(ns []store.EdgeNode) []string {
	out := []string{}
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return out
}

func (s *consoleEdgeBDD) consoleNoEdge() error { s.noEdge = true; return nil }

func (s *consoleEdgeBDD) reportsNotConfigured() error {
	return want(s.status == 200 && !s.snap.Configured, "status %d configured=%v", s.status, s.snap.Configured)
}

func (s *consoleEdgeBDD) panelExplainsNoHost() error {
	s.assets()
	return want(strings.Contains(s.html, `id="edge-disabled"`) && strings.Contains(s.html, "no Edge host"),
		"the panel has no 'no Edge host' explanation")
}

func (s *consoleEdgeBDD) storeFailsOnRead() error {
	s.db = failingStore{Store: s.db, err: errors.New("edge store: disk on fire")}
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	return nil
}

func (s *consoleEdgeBDD) refusedWithStoreWords() error {
	return want(s.status == 502 && strings.Contains(s.err, "disk on fire"), "status %d body %q", s.status, s.err)
}

func (s *consoleEdgeBDD) panelShowsThatMessage() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	// The fetch's rejection text is what is rendered, and the empty state is NOT.
	return want(strings.Contains(js, "edge-error") && regexp.MustCompile(`catch\(function \(e\) \{[^}]*edge-error`).MatchString(js),
		"a failed read does not render the error into the panel")
}

func (s *consoleEdgeBDD) reachedOnlyThroughRelay(name, via string) error {
	s.enroll(via, []store.EdgeTransport{relayTr("relay.rogerai.fm")}, edge.Relay)
	s.enroll(name, []store.EdgeTransport{relayTr(via)}, edge.Actuate)
	return nil
}

func (s *consoleEdgeBDD) listedUnderRelay(name, via string) error {
	ns := names(s.snap.Nodes)
	for i, n := range ns {
		if n == via {
			if err := want(i+1 < len(ns) && ns[i+1] == name, "%q is not immediately under %q: %v", name, via, ns); err != nil {
				return err
			}
			c, _ := s.node(name)
			return want(c.Via == via && c.Child, "%q via=%q child=%v", name, c.Via, c.Child)
		}
	}
	return fmt.Errorf("%q not listed: %v", via, ns)
}

func (s *consoleEdgeBDD) flaggedRelay(name string) error {
	n, ok := s.node(name)
	return want(ok && n.Relay, "%q relay=%v", name, n.Relay)
}

func (s *consoleEdgeBDD) sameOrderAsTUI() error {
	list, err := s.fleet.List()
	if err != nil {
		return err
	}
	var wantOrder []string
	for _, r := range edge.Arrange(list) {
		wantOrder = append(wantOrder, r.Node.Name)
	}
	got := names(s.snap.Nodes)
	return want(strings.Join(got, ",") == strings.Join(wantOrder, ","), "console order %v, arrangement %v", got, wantOrder)
}

func (s *consoleEdgeBDD) bothTransports(name string) error {
	s.enroll(name, []store.EdgeTransport{lanTr("192.168.1.11"), relayTr("relay.rogerai.fm")}, edge.Sense)
	return nil
}

func (s *consoleEdgeBDD) appearsOnce(name string) error {
	c := 0
	for _, n := range names(s.snap.Nodes) {
		if n == name {
			c++
		}
	}
	return want(c == 1, "%q appears %d times", name, c)
}

func (s *consoleEdgeBDD) reportedLANNoRelay(name string) error {
	n, ok := s.node(name)
	return want(ok && n.LAN && n.Via == "", "%q lan=%v via=%q", name, n.LAN, n.Via)
}

func (s *consoleEdgeBDD) relayCycle(a, b string) error {
	s.enroll(a, []store.EdgeTransport{relayTr(b)}, edge.Relay)
	s.enroll(b, []store.EdgeTransport{relayTr(a)}, edge.Relay)
	return nil
}

func (s *consoleEdgeBDD) bothListed(a, b string) error {
	_, oka := s.node(a)
	_, okb := s.node(b)
	return want(oka && okb, "nodes = %v", names(s.snap.Nodes))
}

func (s *consoleEdgeBDD) eachExactlyOnce() error {
	seen := map[string]int{}
	for _, n := range names(s.snap.Nodes) {
		seen[n]++
	}
	for n, c := range seen {
		if c != 1 {
			return fmt.Errorf("%q listed %d times", n, c)
		}
	}
	return nil
}

func (s *consoleEdgeBDD) notHeardPastDark(name string) error {
	s.enroll(name, []store.EdgeTransport{lanTr("192.168.1.11")}, edge.Sense)
	if err := s.fleet.RecordProbe(s.ids[name], edge.Sense, true); err != nil {
		return err
	}
	s.now = s.now.Add(6 * time.Minute)
	n, err := s.fleet.Sweep(2 * time.Minute)
	if err != nil {
		return err
	}
	return want(n == 1, "the sweep darkened %d", n)
}

func (s *consoleEdgeBDD) stillListed(name string) error {
	_, ok := s.node(name)
	return want(ok, "%q dropped: %v", name, names(s.snap.Nodes))
}

func (s *consoleEdgeBDD) darkWithAge() error {
	for _, n := range s.snap.Nodes {
		if n.Presence == string(edge.PresenceDark) {
			return want(n.Dark && regexp.MustCompile(`^\d+[smhd]$`).MatchString(n.Age), "dark=%v age=%q", n.Dark, n.Age)
		}
	}
	return fmt.Errorf("no DARK node in %+v", s.snap.Nodes)
}

func (s *consoleEdgeBDD) declaresThreeCaps(name string) error {
	s.enroll(name, []store.EdgeTransport{lanTr("192.168.1.11")}, edge.Serve, edge.Sense, edge.Actuate)
	return s.fleet.RecordProbe(s.ids[name], edge.Serve, true)
}

func (s *consoleEdgeBDD) carriesThreeCaps(name string) error {
	n, ok := s.node(name)
	if !ok {
		return fmt.Errorf("%q missing", name)
	}
	st := map[string]string{}
	for _, c := range n.Caps {
		st[c.Name] = c.State
	}
	return want(st["serve"] == "VERIFIED" && st["sense"] == "CLAIMED" && st["actuate"] == "PENDING CONFIRMATION",
		"caps = %v", st)
}

func (s *consoleEdgeBDD) unverifiedNamesMethod() error {
	for _, n := range s.snap.Nodes {
		for _, c := range n.Caps {
			if c.State == "VERIFIED" {
				if err := want(c.Verify == "", "a verified cap still says how it will be verified: %+v", c); err != nil {
					return err
				}
				continue
			}
			if err := want(c.Verify == edge.Capability(c.Name).VerificationMethod(), "%s verify=%q", c.Name, c.Verify); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *consoleEdgeBDD) nNodes(n int) error {
	for i := 0; i < n; i++ {
		s.enroll(fmt.Sprintf("node-%02d", i), []store.EdgeTransport{lanTr(fmt.Sprintf("192.168.1.%d", 10+i))}, edge.Sense)
	}
	return nil
}

func (s *consoleEdgeBDD) saysTooLarge() error {
	return want(s.snap.TooMany && s.snap.GraphMax == cGraphMax, "too_many=%v graph_max=%d", s.snap.TooMany, s.snap.GraphMax)
}

func (s *consoleEdgeBDD) listsAllN(n int) error {
	return want(len(s.snap.Nodes) == n, "listed %d of %d", len(s.snap.Nodes), n)
}

func (s *consoleEdgeBDD) panelRendersList() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	return want(strings.Contains(js, "too_many") && strings.Contains(js, "edge-list"), "the panel has no list fallback keyed on too_many")
}

func (s *consoleEdgeBDD) relayNotOnEdge(name, via string) error {
	s.enroll(name, []store.EdgeTransport{relayTr(via)}, edge.Sense)
	return nil
}

func (s *consoleEdgeBDD) ownRelayedEdgeNoVia(name string) error {
	n, ok := s.node(name)
	return want(ok && !n.LAN && n.Via == "" && !n.Child, "%q lan=%v via=%q child=%v", name, n.LAN, n.Via, n.Child)
}

func (s *consoleEdgeBDD) noViaOutsideSnapshot() error {
	in := map[string]bool{}
	for _, n := range s.snap.Nodes {
		in[n.Name] = true
	}
	for _, n := range s.snap.Nodes {
		if n.Via != "" && !in[n.Via] {
			return fmt.Errorf("%q is reached through %q, which is not in the snapshot", n.Name, n.Via)
		}
	}
	return nil
}

// ---- 3./4./5. the drawing, the animation, selection (pinned on the assets) ----

func (s *consoleEdgeBDD) jsHas(what string, pats ...string) error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	for _, p := range pats {
		if !regexp.MustCompile(p).MatchString(js) {
			return fmt.Errorf("%s: the EDGE block lacks /%s/", what, p)
		}
	}
	return nil
}

func (s *consoleEdgeBDD) cssHas(what string, pats ...string) error {
	s.assets()
	for _, p := range pats {
		if !regexp.MustCompile(p).MatchString(s.css) {
			return fmt.Errorf("%s: console.css lacks /%s/", what, p)
		}
	}
	return nil
}

func (s *consoleEdgeBDD) htmlHas(what string, subs ...string) error {
	s.assets()
	for _, sub := range subs {
		if !strings.Contains(s.html, sub) {
			return fmt.Errorf("%s: console.html lacks %q", what, sub)
		}
	}
	return nil
}

func (s *consoleEdgeBDD) selfAtCentre() error {
	return s.jsHas("self at the centre", `edge-self`, `cx\b|centre|center`)
}

func (s *consoleEdgeBDD) lanSolid() error {
	return s.cssHas("solid LAN stroke", `\.edge-link--lan\s*\{[^}]*stroke-dasharray:\s*none`)
}
func (s *consoleEdgeBDD) relayDashedThroughRelay() error {
	if err := s.cssHas("dashed relay stroke", `\.edge-link--relay\s*\{[^}]*stroke-dasharray:\s*[0-9]`); err != nil {
		return err
	}
	// A relayed node's line goes to its RELAY's point, not to self.
	return s.jsHas("edge through the relay", `via`, `edge-link--relay`)
}
func (s *consoleEdgeBDD) darkBrokenKept() error {
	if err := s.cssHas("broken dark stroke", `\.edge-link--dark\s*\{[^}]*stroke-dasharray:\s*[0-9]`, `\.edge-node--dark\s*\{[^}]*opacity`); err != nil {
		return err
	}
	return s.jsHas("dark nodes drawn", `edge-node--dark`)
}

func (s *consoleEdgeBDD) notColourAlone() error {
	// The three link classes differ in dasharray, which is what the solid/dashed/broken
	// scenario pinned; here we pin that none of them is distinguished ONLY by colour.
	s.assets()
	for _, cls := range []string{"lan", "relay", "dark"} {
		re := regexp.MustCompile(`\.edge-link--` + cls + `\s*\{[^}]*stroke-dasharray`)
		if !re.MatchString(s.css) {
			return fmt.Errorf(".edge-link--%s carries no stroke pattern", cls)
		}
	}
	return s.jsHas("verified caps by case", `toUpperCase\(\)`)
}

func (s *consoleEdgeBDD) onlyNode() error { return nil }

func (s *consoleEdgeBDD) panelSaysOnlyNode() error {
	return s.htmlHas("empty state", `id="edge-empty"`, "is the only node on the Edge")
}
func (s *consoleEdgeBDD) panelSaysDrawsOnlySeen() error {
	return s.htmlHas("empty state", "draws only what it has seen")
}
func (s *consoleEdgeBDD) panelSaysHowToAdd() error {
	return s.htmlHas("empty state", "run RogerAI on another machine on this network", "adopt")
}

func (s *consoleEdgeBDD) labelClipped() error { return s.jsHas("clipped label", `edgeClip\(`) }
func (s *consoleEdgeBDD) fullNameAvailable() error {
	return s.jsHas("full name on the node", `"title"`)
}

func (s *consoleEdgeBDD) pulseOnlyOnAdvance() error {
	return s.jsHas("pulse on last_seen advance", `last_seen\s*>\s*prev\.last_seen[^\n]*\n[^\n]*edgePulse\(|prev\.last_seen[^\n]*last_seen[\s\S]{0,80}edgePulse\(`)
}

func (s *consoleEdgeBDD) noBareTimerPulse() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	// Every call site of edgePulse( must sit under the last_seen comparison; a timer
	// callback that pulses would show up as a call outside that branch.
	calls := regexp.MustCompile(`edgePulse\(`).FindAllStringIndex(js, -1)
	if len(calls) < 2 { // the definition + at least one call
		return fmt.Errorf("edgePulse is never called")
	}
	for _, c := range calls[1:] {
		window := js[max(0, c[0]-240):c[0]]
		if !strings.Contains(window, "last_seen") {
			return fmt.Errorf("edgePulse is called outside the last_seen comparison:\n%s", js[max(0, c[0]-240):c[1]])
		}
	}
	for _, m := range regexp.MustCompile(`setInterval\(([A-Za-z_]+)`).FindAllStringSubmatch(js, -1) {
		if m[1] != "edgeTick" {
			return fmt.Errorf("the EDGE block runs a timer other than the read tick: %s", m[0])
		}
	}
	return nil
}

func (s *consoleEdgeBDD) relayedPulsePath() error {
	return s.jsHas("pulse through the relay", `function edgePath\(`, `via`)
}

func (s *consoleEdgeBDD) pollsOnlyWhileShown() error {
	if err := s.jsHas("visibility", `document\.hidden`, `function edgeStart\(`, `function edgeStop\(`); err != nil {
		return err
	}
	s.assets()
	return want(regexp.MustCompile(`if \(name === "edge"\) edgeStart\(\); else edgeStop\(\);`).MatchString(s.js),
		"setTab does not start/stop the Edge tab")
}

func (s *consoleEdgeBDD) leavingStopsBoth() error {
	return s.jsHas("stop clears both", `function edgeStop\(\)[\s\S]{0,400}clearInterval\([\s\S]{0,400}cancelAnimationFrame\(`)
}

func (s *consoleEdgeBDD) reducedMotionStill() error {
	if err := s.jsHas("reduced motion", `prefers-reduced-motion: reduce`, `edge-beat`); err != nil {
		return err
	}
	return s.cssHas("still beat mark", `\.edge-beat\b`)
}

func (s *consoleEdgeBDD) markOnlyOnHeartbeat() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	// The reduced-motion branch lives INSIDE edgePulse, so it inherits the one trigger.
	return want(regexp.MustCompile(`function edgePulse\([\s\S]{0,600}edgeReduced`).MatchString(js),
		"the still mark is not gated on the same heartbeat as the pulse")
}

func (s *consoleEdgeBDD) choosingOpensDetail() error {
	return s.jsHas("select opens detail", `function edgeSelect\(`, `edge-detail`)
}
func (s *consoleEdgeBDD) detailCarries() error {
	for _, k := range []string{`"id"`, `"kind"`, `"capabilities"`, `"transports"`, `"presence"`, `"pin"`, `"history"`} {
		if err := s.jsHas("detail field", regexp.QuoteMeta(k)); err != nil {
			return err
		}
	}
	return s.jsHas("last seen in detail", `last_seen|\.age`)
}
func (s *consoleEdgeBDD) relayedDetailNamesRelay() error {
	return s.jsHas("relay in detail", `"reached through"`)
}
func (s *consoleEdgeBDD) selectionByID() error {
	return s.jsHas("sticky selection", `var edgeSel\b`, `edgeSel === n\.id|n\.id === edgeSel`)
}
func (s *consoleEdgeBDD) selectionNotMoved() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	// The render must never reassign the selection to the first row when the selected id
	// is absent: an absent selection stays absent (the TUI's clampEdgeSel rule).
	return want(!regexp.MustCompile(`edgeSel = [a-z.]*nodes\[0\]`).MatchString(js), "the render moves the selection onto the first node")
}
func (s *consoleEdgeBDD) candidatesOwnBlock() error {
	return s.htmlHas("candidate block", `id="edge-candidates"`)
}
func (s *consoleEdgeBDD) candidateDetailAdopt() error {
	return s.jsHas("candidate detail", `not on your Edge`, `edge-adopt`)
}

// ---- 6. adopt --------------------------------------------------------------

func (s *consoleEdgeBDD) aCandidate(name string) error { s.candidate(name); return nil }

func (s *consoleEdgeBDD) ownerAdopts(name string) error {
	s.do(http.MethodPost, "/api/edge/adopt", s.token(), `{"id":"`+s.ids[name]+`"}`)
	return nil
}

func (s *consoleEdgeBDD) adoptHookCalledWith() error {
	return want(len(s.adopts) == 1 && s.adopts[0] == s.ids["attic"]+" attic", "adopts = %v", s.adopts)
}

func (s *consoleEdgeBDD) nextReadListsAsNode(name string) error {
	s.readEdge()
	_, isNode := s.node(name)
	for _, c := range names(s.snap.Candidates) {
		if c == name {
			return fmt.Errorf("%q is still a candidate", name)
		}
	}
	return want(isNode && s.status == 200, "%q not a node after adopt: %v", name, names(s.snap.Nodes))
}

func (s *consoleEdgeBDD) adoptRequestedGET() error {
	s.do(http.MethodGet, "/api/edge/adopt", s.token(), "")
	return nil
}
func (s *consoleEdgeBDD) adoptRequestedNoToken() error {
	s.do(http.MethodPost, "/api/edge/adopt", "", `{"id":"x"}`)
	return nil
}
func (s *consoleEdgeBDD) noAdoptCalled() error {
	return want(len(s.adopts) == 0, "adopts = %v", s.adopts)
}

func (s *consoleEdgeBDD) adoptsNotACandidate() error {
	s.enroll("shed", []store.EdgeTransport{lanTr("192.168.1.10")}, edge.Serve)
	s.do(http.MethodPost, "/api/edge/adopt", s.token(), `{"id":"`+s.ids["shed"]+`"}`)
	return nil
}

func (s *consoleEdgeBDD) adoptFailsWith(name, msg string) error {
	s.candidate(name)
	s.adoptErr = errors.New(msg)
	return nil
}

func (s *consoleEdgeBDD) refusedWithThatMessage() error {
	return want(s.status == 502 && strings.Contains(s.err, "certificate does not match"), "status %d body %q", s.status, s.err)
}

func (s *consoleEdgeBDD) stillACandidate(name string) error {
	s.readEdge()
	for _, c := range names(s.snap.Candidates) {
		if c == name {
			return nil
		}
	}
	return fmt.Errorf("%q is no longer a candidate after a failed adopt", name)
}

func (s *consoleEdgeBDD) fleetNoAdopt() error { s.noAdopt = true; s.candidate("attic"); return nil }

func (s *consoleEdgeBDD) refused501CannotAdopt() error {
	return want(s.status == 501 && strings.Contains(s.err, "cannot adopt"), "status %d body %q", s.status, s.err)
}

func (s *consoleEdgeBDD) adoptOnlyOnClick() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	if n := strings.Count(js, `"/api/edge/adopt"`); n != 1 {
		return fmt.Errorf("adopt is posted from %d places, want exactly 1", n)
	}
	if !regexp.MustCompile(`function edgeAdopt\([\s\S]{0,400}"/api/edge/adopt"`).MatchString(js) {
		return fmt.Errorf("the adopt POST does not live in edgeAdopt")
	}
	for _, m := range regexp.MustCompile(`[^\n]*\bedgeAdopt\([^\n]*`).FindAllString(js, -1) {
		if strings.Contains(m, "function edgeAdopt(") {
			continue
		}
		if !strings.Contains(m, `addEventListener("click"`) && !strings.Contains(m, "onclick") {
			return fmt.Errorf("edgeAdopt is called outside a click handler: %s", strings.TrimSpace(m))
		}
	}
	return nil
}

func (s *consoleEdgeBDD) noPollCallsAdopt() error {
	js, err := s.edgeJS()
	if err != nil {
		return err
	}
	for _, fn := range []string{"edgeTick", "loadEdge", "edgeRender", "edgeStart"} {
		re := regexp.MustCompile(`function ` + fn + `\([\s\S]*?\n  \}`)
		if body := re.FindString(js); strings.Contains(body, "edgeAdopt(") || strings.Contains(body, "/api/edge/adopt") {
			return fmt.Errorf("%s calls adopt", fn)
		}
	}
	return nil
}

// ---- 7. sessions -------------------------------------------------------------

func (s *consoleEdgeBDD) sessionsEmpty() error {
	return want(len(s.snap.Sessions) == 0, "sessions = %+v", s.snap.Sessions)
}
func (s *consoleEdgeBDD) panelSaysQuiet() error {
	return s.htmlHas("quiet state", `id="edge-sessions"`, "quiet")
}

func (s *consoleEdgeBDD) guestTurn(who, band, station string) error {
	s.record(edge.Traffic{Request: "r1", Kind: edge.FromGuest, Who: who,
		Receipts: []protocol.UsageReceipt{s.receipt("r1", station, band, "")}})
	return nil
}

func (s *consoleEdgeBDD) oneSessionListed(who, band, station, outcome string) error {
	if len(s.snap.Sessions) != 1 {
		return fmt.Errorf("sessions = %+v", s.snap.Sessions)
	}
	x := s.snap.Sessions[0]
	return want(x.Who == who && x.Band == band && x.Station == station && x.Outcome == outcome,
		"session = %+v", x)
}

func (s *consoleEdgeBDD) turnSecondsAgo(secs int) error {
	s.record(edge.Traffic{Request: "r1", Kind: edge.FromUse,
		Receipts: []protocol.UsageReceipt{s.receipt("r1", "house-or-1", "gpt-oss-20b", "")}})
	s.now = s.now.Add(time.Duration(secs) * time.Second)
	return nil
}

func (s *consoleEdgeBDD) failoverTurn(left, served string) error {
	s.record(edge.Traffic{Request: "r1", Kind: edge.FromUse, Receipts: []protocol.UsageReceipt{
		s.receipt("r1", left, "qwen-3.8-27b", "upstream_429"),
		s.receipt("r1", served, "qwen-3.8-27b", ""),
	}})
	return nil
}

func (s *consoleEdgeBDD) namesLeftAndStation(left, station string) error {
	x := s.snap.Sessions[0]
	return want(x.Left == left && x.Station == station, "session = %+v", x)
}

func (s *consoleEdgeBDD) relayedTurn(via, station string) error {
	s.record(edge.Traffic{Request: "r1", Kind: edge.FromAgent, Via: via,
		Receipts: []protocol.UsageReceipt{s.receipt("r1", station, "gpt-oss-120b", "")}})
	return nil
}

func (s *consoleEdgeBDD) namesViaAndStation(via, station string) error {
	x := s.snap.Sessions[0]
	return want(x.Via == via && x.Station == station, "session = %+v", x)
}

func (s *consoleEdgeBDD) panelDrawsPathInOrder() error {
	return s.jsHas("path order", `\[\s*s\.who,\s*s\.via,\s*s\.station\s*\]|who[\s\S]{0,60}via[\s\S]{0,60}station`)
}

func (s *consoleEdgeBDD) boardEscalated(who, class, labels string) error {
	var ls []string
	for _, l := range strings.Split(labels, ",") {
		ls = append(ls, strings.TrimSpace(l))
	}
	s.record(edge.Traffic{Request: "r1", Kind: edge.FromDevice, Who: who, Escalate: true,
		Contract: edge.Contract{Class: class, Labels: ls, Framing: "You are a gate camera classifier."},
		Answer:   "blocked",
		Receipts: []protocol.UsageReceipt{s.receipt("r1", "house-cb-1", "qwen-3.8-27b", "")}})
	return nil
}

func (s *consoleEdgeBDD) markedEscalate(outcome string) error {
	x := s.snap.Sessions[0]
	return want(x.Escalate && x.Outcome == outcome && x.Outcome == cEscalate, "session = %+v", x)
}

func (s *consoleEdgeBDD) carriesContract() error {
	x := s.snap.Sessions[0]
	return want(x.Contract.Class == "gate" && strings.Join(x.Contract.Labels, ",") == "open,closed,blocked",
		"contract = %+v", x.Contract)
}

func (s *consoleEdgeBDD) escalationStyledPositive() error {
	if err := s.cssHas("escalate style", `\.sess--escalate\s*\{`); err != nil {
		return err
	}
	s.assets()
	blk := regexp.MustCompile(`\.sess--escalate\s*\{[^}]*\}`).FindString(s.css)
	if strings.Contains(blk, "--live") || strings.Contains(blk, "--err") || strings.Contains(blk, "red") {
		return fmt.Errorf("an escalation is styled as an error: %s", blk)
	}
	return s.jsHas("escalate class", `sess--escalate`)
}

func (s *consoleEdgeBDD) refusedTurn(reason string) error {
	s.record(edge.Traffic{Request: "r1", Kind: edge.FromUse, Band: "gpt-oss-120b",
		Receipts: []protocol.UsageReceipt{s.receipt("r1", "house-or-1", "gpt-oss-120b", reason)}})
	return nil
}

func (s *consoleEdgeBDD) outcomeReads(text string) error {
	x := s.snap.Sessions[0]
	return want(x.Outcome == text, "outcome = %q", x.Outcome)
}

func (s *consoleEdgeBDD) manyIdentical(n int, who1 string, m int, who2 string) error {
	for i := 0; i < n; i++ {
		req := "a" + strconv.Itoa(i)
		s.record(edge.Traffic{Request: req, Kind: edge.FromGuest, Who: who1,
			Receipts: []protocol.UsageReceipt{s.receipt(req, "house-or-1", "gpt-oss-120b", "")}})
	}
	for i := 0; i < m; i++ {
		req := "b" + strconv.Itoa(i)
		s.record(edge.Traffic{Request: req, Kind: edge.FromGuest, Who: who2,
			Receipts: []protocol.UsageReceipt{s.receipt(req, "house-or-1", "gpt-oss-120b", "")}})
	}
	return nil
}

func (s *consoleEdgeBDD) groupedIntoRows(n int) error {
	return want(len(s.snap.Sessions) == n, "%d rows: %+v", len(s.snap.Sessions), s.snap.Sessions)
}

func (s *consoleEdgeBDD) rowFirstWithCount(who string, n int) error {
	x := s.snap.Sessions[0]
	return want(x.Who == who && x.Count == n, "first row = %+v", x)
}

func (s *consoleEdgeBDD) otherAccountTraffic() error {
	_, err := s.sessions.Record(edge.Traffic{Account: "acct-2", Request: "z1", Kind: edge.FromUse,
		Receipts: []protocol.UsageReceipt{s.receipt("z1", "house-or-9", "secret-band", "")}})
	if !errors.Is(err, edge.ErrOtherAccount) {
		return fmt.Errorf("the ledger accepted another account's traffic: %v", err)
	}
	return nil
}

func (s *consoleEdgeBDD) noSessionForIt() error {
	for _, x := range s.snap.Sessions {
		if x.Request == "z1" || x.Band == "secret-band" {
			return fmt.Errorf("another account's session is listed: %+v", x)
		}
	}
	return nil
}

func (s *consoleEdgeBDD) nothingInferable() error {
	b := string(s.body)
	return want(!strings.Contains(b, "acct-2") && !strings.Contains(b, "house-or-9") && !strings.Contains(b, "secret-band"),
		"another account leaks into the snapshot: %s", b)
}

func (s *consoleEdgeBDD) consoleChatWithReceipt() error {
	s.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RogerAI-Provider", "house-or-1")
		w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(protocol.UsageReceipt{
			RequestID: "req-console-1", NodeID: "house-or-1", Model: "gpt-oss-120b", User: "u", CompletionTokens: 7}))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	}))
	s.do(http.MethodPost, "/api/chat", s.token(), `{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hello"}]}`)
	return want(s.status == 200, "chat = %d: %s", s.status, s.err)
}

func (s *consoleEdgeBDD) sessionAttributedConsole(who string) error {
	live := s.sessions.Live()
	if len(live) != 1 {
		return fmt.Errorf("sessions = %+v", live)
	}
	return want(live[0].Kind == edge.FromConsole && live[0].Attribution() == who && live[0].Request == "req-console-1",
		"session = %+v", live[0])
}

func (s *consoleEdgeBDD) namesBandAndStation() error {
	x := s.sessions.Live()[0]
	return want(x.Band == "gpt-oss-120b" && x.Station == "house-or-1", "session = %+v", x)
}

func (s *consoleEdgeBDD) consoleChatFails() error {
	s.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no node offers gpt-oss-120b", http.StatusBadGateway)
	}))
	s.do(http.MethodPost, "/api/chat", s.token(), `{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hello"}]}`)
	return want(s.status != 200, "a failing broker produced status %d", s.status)
}

func (s *consoleEdgeBDD) noSessionRecorded() error {
	return want(s.sessions.Len() == 0, "sessions = %+v", s.sessions.Live())
}

func (s *consoleEdgeBDD) readManyTimes() error {
	s.record(edge.Traffic{Request: "r1", Kind: edge.FromUse,
		Receipts: []protocol.UsageReceipt{s.receipt("r1", "house-or-1", "gpt-oss-20b", "")}})
	for i := 0; i < 20; i++ {
		s.readEdge()
	}
	return nil
}

func (s *consoleEdgeBDD) noTurnDispatched() error {
	// No broker is configured on this console, and the ledger can only grow through a
	// receipt: a read that dispatched would have failed loudly or grown it.
	return want(s.up == nil && s.sessions.Len() == 1, "the read dispatched something: %d sessions", s.sessions.Len())
}

func (s *consoleEdgeBDD) ledgerUnchanged() error {
	live := s.sessions.Live()
	return want(len(live) == 1 && live[0].Request == "r1", "ledger = %+v", live)
}

// ---- 8. the node snapshot ---------------------------------------------------

func (s *consoleEdgeBDD) stateAndEventsRead() error {
	s.enroll("shed", []store.EdgeTransport{lanTr("192.168.1.10")}, edge.Serve)
	s.do(http.MethodGet, "/api/state", s.token(), "")
	if s.status != 200 {
		return fmt.Errorf("state = %d", s.status)
	}
	var m map[string]any
	if err := json.Unmarshal(s.body, &m); err != nil {
		return err
	}
	for _, k := range []string{"nodes", "candidates", "sessions", "edge"} {
		if _, ok := m[k]; ok {
			return fmt.Errorf("/api/state carries %q", k)
		}
	}
	if strings.Contains(string(s.body), "shed") {
		return fmt.Errorf("/api/state names an Edge node")
	}
	// The first SSE frame is the same snapshot.
	req, _ := http.NewRequest(http.MethodGet, s.http.URL+"/api/events?t="+s.token(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	buf := make([]byte, 64<<10)
	n, _ := res.Body.Read(buf)
	frame := string(buf[:n])
	return want(strings.HasPrefix(frame, "data: ") && !strings.Contains(frame, "shed") && !strings.Contains(frame, `"sessions"`),
		"the event stream grew an Edge: %.200s", frame)
}

func (s *consoleEdgeBDD) neitherCarriesEdge() error { return nil }

func (s *consoleEdgeBDD) readsOwnEndpointAtCadence() error {
	return s.jsHas("own endpoint at the stream's cadence", `apiGet\("/api/edge"\)`, `setInterval\(edgeTick,\s*1000\)`)
}

// ---- suite -----------------------------------------------------------------

// init registers every console Edge step; both the console_view suite and the empty_edge
// (@console) suite are built from it.
func (st *consoleEdgeBDD) init(sc *godog.ScenarioContext) {
	sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
	sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if st.http != nil {
			st.http.Close()
			st.http = nil
		}
		if st.up != nil {
			st.up.Close()
			st.up = nil
		}
		return c, nil
	})
	// 1. the tab
	sc.Step(`^a console over a fleet of nodes$`, st.aConsoleOverAFleet)
	sc.Step(`^the console shell is served$`, st.shellServed)
	sc.Step(`^the tab strip reads CHAT, SHARE, ACCOUNT, BROWSE, EDGE, SETTINGS in that order$`, st.tabStripOrder)
	sc.Step(`^there is a panel for EDGE hidden until its tab is chosen$`, st.panelHiddenUntilChosen)
	sc.Step(`^the hash #edge opens it, like every other tab's hash$`, st.hashOpensIt)
	sc.Step(`^the console shell is fetched with no token$`, st.shellFetchedNoToken)
	sc.Step(`^it is served, because it carries no node data$`, st.itIsServedNoNodeData)
	sc.Step(`^the Edge data is fetched with no token$`, st.edgeFetchedNoToken)
	sc.Step(`^the Edge data is fetched with the wrong token$`, st.edgeFetchedWrongToken)
	sc.Step(`^it is refused with (\d+)$`, st.refusedWith)
	sc.Step(`^it loads no script from outside the console$`, st.noOutsideScript)
	sc.Step(`^the Edge graph is drawn as inline SVG the console ships$`, st.inlineSVG)
	// 2. the read
	sc.Step(`^nodes "shed" and "bench" on the Edge and a candidate "attic"$`, st.nodesAndCandidate)
	sc.Step(`^the Edge data is read$`, st.edgeRead)
	sc.Step(`^it names this instance as self$`, st.namesSelf)
	sc.Step(`^it lists exactly "([^"]*)" and "([^"]*)" as nodes$`, st.listsExactlyNodes)
	sc.Step(`^it lists exactly "([^"]*)" as a candidate, outside the nodes$`, st.listsExactlyCandidate)
	sc.Step(`^it carries the moment the snapshot was taken, so every age is measured from one clock$`, st.carriesTheMoment)
	sc.Step(`^no node's private key material appears anywhere in it$`, st.noPrivateKeyMaterial)
	sc.Step(`^the Edge data is requested with POST$`, st.edgeRequestedPOST)
	sc.Step(`^nothing about the fleet changed$`, st.nothingChanged)
	sc.Step(`^a console built with no Edge wired$`, st.consoleNoEdge)
	sc.Step(`^it reports the Edge as not configured$`, st.reportsNotConfigured)
	sc.Step(`^the panel explains that this build has no Edge host, rather than showing an empty graph$`, st.panelExplainsNoHost)
	sc.Step(`^the fleet store fails on read$`, st.storeFailsOnRead)
	sc.Step(`^it is refused with 502 and the store's own words$`, st.refusedWithStoreWords)
	sc.Step(`^the panel shows that message, not "the only node"$`, st.panelShowsThatMessage)
	sc.Step(`^"([^"]*)" is reached only through the relay "([^"]*)", which is on the Edge$`, st.reachedOnlyThroughRelay)
	sc.Step(`^"([^"]*)" is listed immediately under "([^"]*)" and names "[^"]*" as the relay it is reached through$`, st.listedUnderRelay)
	sc.Step(`^"([^"]*)" is flagged as a relay$`, st.flaggedRelay)
	sc.Step(`^that is the same order the TUI's Edge screen draws them in$`, st.sameOrderAsTUI)
	sc.Step(`^"([^"]*)" has a LAN transport and a relay transport$`, st.bothTransports)
	sc.Step(`^"([^"]*)" appears once$`, st.appearsOnce)
	sc.Step(`^it is reported as LAN-direct, with no relay named$`, func() error { return st.reportedLANNoRelay("bench") })
	sc.Step(`^"([^"]*)" names "([^"]*)" as its relay and "[^"]*" names "[^"]*" as its relay$`, st.relayCycle)
	sc.Step(`^both "([^"]*)" and "([^"]*)" are listed$`, st.bothListed)
	sc.Step(`^each is listed exactly once$`, st.eachExactlyOnce)
	sc.Step(`^"([^"]*)" has not been heard from past the dark threshold$`, st.notHeardPastDark)
	sc.Step(`^"([^"]*)" is still listed$`, st.stillListed)
	sc.Step(`^its presence is DARK and its last-seen age is carried$`, st.darkWithAge)
	sc.Step(`^"([^"]*)" declares serve verified, sense declared and actuate unconfirmed$`, st.declaresThreeCaps)
	sc.Step(`^"([^"]*)" carries all three capabilities with their states$`, st.carriesThreeCaps)
	sc.Step(`^an unverified capability names how it will be verified, as the TUI's detail does$`, st.unverifiedNamesMethod)
	sc.Step(`^(\d+) nodes on the Edge$`, st.nNodes)
	sc.Step(`^it says the graph is too large to draw$`, st.saysTooLarge)
	sc.Step(`^it still lists all (\d+) nodes$`, st.listsAllN)
	sc.Step(`^the panel renders the list, not a broken picture$`, st.panelRendersList)
	sc.Step(`^"([^"]*)" names a relay "([^"]*)" that is not on the Edge$`, st.relayNotOnEdge)
	sc.Step(`^"([^"]*)" is listed on its own relayed edge with no relay named$`, st.ownRelayedEdgeNoVia)
	sc.Step(`^no node in the snapshot is reached through a name that is not in the snapshot$`, st.noViaOutsideSnapshot)
	// 3. the drawing
	sc.Step(`^the Edge graph places self at the centre and every node in relation to it$`, st.selfAtCentre)
	sc.Step(`^a LAN-direct node is drawn with a solid stroke$`, st.lanSolid)
	sc.Step(`^a relayed node is drawn with a dashed stroke to its relay, and the relay is drawn as its own node$`, st.relayDashedThroughRelay)
	sc.Step(`^a dark node is drawn dim with a broken stroke, and it is kept$`, st.darkBrokenKept)
	sc.Step(`^live, relayed and dark differ by stroke pattern, not only by colour$`, st.notColourAlone)
	sc.Step(`^a verified capability differs from a declared one by case, as it does in the terminal$`, func() error { return st.jsHas("case", `toUpperCase\(\)`) })
	sc.Step(`^this instance is the only node$`, st.onlyNode)
	sc.Step(`^the panel says this instance is the only node on the Edge$`, st.panelSaysOnlyNode)
	sc.Step(`^it says the screen draws only what it has seen$`, st.panelSaysDrawsOnlySeen)
	sc.Step(`^it says how to add a node: run RogerAI on another machine on this network, and adopt it$`, st.panelSaysHowToAdd)
	sc.Step(`^a node label is clipped to its cell$`, st.labelClipped)
	sc.Step(`^the full name is still available on the node$`, st.fullNameAvailable)
	// 4. the animation
	sc.Step(`^a pulse is started only when a node's last-seen advanced since the previous read$`, st.pulseOnlyOnAdvance)
	sc.Step(`^nothing starts a pulse on a bare timer$`, st.noBareTimerPulse)
	sc.Step(`^a pulse for a relayed node runs along its edge to the relay and then the relay's edge to self$`, st.relayedPulsePath)
	sc.Step(`^the Edge tab polls and animates only while it is the shown tab and the page is visible$`, st.pollsOnlyWhileShown)
	sc.Step(`^leaving the tab stops both$`, st.leavingStopsBoth)
	sc.Step(`^under prefers-reduced-motion a heartbeat is shown as a still mark on the node, not a travelling pulse$`, st.reducedMotionStill)
	sc.Step(`^the mark still appears only on a real heartbeat$`, st.markOnlyOnHeartbeat)
	// 5. selection
	sc.Step(`^choosing a node opens its detail beside the graph$`, st.choosingOpensDetail)
	sc.Step(`^the detail carries id, kind, capabilities with their states, transports, presence with last-seen, pin and history$`, st.detailCarries)
	sc.Step(`^a relayed node's detail names its relay$`, st.relayedDetailNamesRelay)
	sc.Step(`^the selection is kept by node id across reads$`, st.selectionByID)
	sc.Step(`^a node arriving, going dark or being forgotten does not move it onto a different node$`, st.selectionNotMoved)
	sc.Step(`^candidates are drawn in their own block, with no edge to self$`, st.candidatesOwnBlock)
	sc.Step(`^a candidate's detail says it is not on your Edge and offers adopt$`, st.candidateDetailAdopt)
	// 6. adopt
	sc.Step(`^a candidate "([^"]*)"$`, st.aCandidate)
	sc.Step(`^the owner adopts "([^"]*)" from the console$`, st.ownerAdopts)
	sc.Step(`^the adopt hook is called with the candidate's id and name$`, st.adoptHookCalledWith)
	sc.Step(`^the next Edge read lists "([^"]*)" as a node and no longer as a candidate$`, st.nextReadListsAsNode)
	sc.Step(`^adopt is requested with GET$`, st.adoptRequestedGET)
	sc.Step(`^adopt is requested with no token$`, st.adoptRequestedNoToken)
	sc.Step(`^no adopt hook was called$`, st.noAdoptCalled)
	sc.Step(`^the owner adopts an id that is not a candidate$`, st.adoptsNotACandidate)
	sc.Step(`^adopting "([^"]*)" fails with "([^"]*)"$`, st.adoptFailsWith)
	sc.Step(`^it is refused with 502 and that message$`, st.refusedWithThatMessage)
	sc.Step(`^"([^"]*)" is still a candidate$`, st.stillACandidate)
	sc.Step(`^a console built with a fleet but no adopt hook$`, st.fleetNoAdopt)
	sc.Step(`^it is refused with 501 and says this build cannot adopt$`, st.refused501CannotAdopt)
	sc.Step(`^adopt happens only on the owner's click$`, st.adoptOnlyOnClick)
	sc.Step(`^no read, poll or timer calls adopt$`, st.noPollCallsAdopt)
	// 7. sessions
	sc.Step(`^the sessions list is empty$`, st.sessionsEmpty)
	sc.Step(`^the panel says the Edge is quiet rather than drawing an empty table$`, st.panelSaysQuiet)
	sc.Step(`^a receipted turn from a guest "([^"]*)" against "([^"]*)" served by "([^"]*)"$`, st.guestTurn)
	sc.Step(`^one session is listed, attributed to "([^"]*)", band "([^"]*)", station "([^"]*)", outcome "([^"]*)"$`, st.oneSessionListed)
	sc.Step(`^a receipted turn (\d+) seconds ago$`, st.turnSecondsAgo)
	sc.Step(`^no session is listed$`, st.sessionsEmpty)
	sc.Step(`^a turn that left "([^"]*)" and was served by "([^"]*)"$`, st.failoverTurn)
	sc.Step(`^the session names "([^"]*)" as left and "([^"]*)" as station$`, st.namesLeftAndStation)
	sc.Step(`^a turn relayed through "([^"]*)" and served by "([^"]*)"$`, st.relayedTurn)
	sc.Step(`^the session names "([^"]*)" as via and "([^"]*)" as station$`, st.namesViaAndStation)
	sc.Step(`^the panel draws the path participant, relay, station in that order$`, st.panelDrawsPathInOrder)
	sc.Step(`^a board "([^"]*)" that escalated a reading under contract "([^"]*)" with labels "([^"]*)"$`, st.boardEscalated)
	sc.Step(`^the session is marked escalate with outcome "([^"]*)"$`, st.markedEscalate)
	sc.Step(`^it carries the contract's class and labels$`, st.carriesContract)
	sc.Step(`^the panel styles an escalation as a positive outcome, never as an error$`, st.escalationStyledPositive)
	sc.Step(`^a turn refused for "([^"]*)"$`, st.refusedTurn)
	sc.Step(`^the session's outcome reads "([^"]*)"$`, st.outcomeReads)
	sc.Step(`^(\d+) identical receipted turns from "([^"]*)" and (\d+) from "([^"]*)"$`, st.manyIdentical)
	sc.Step(`^the sessions are grouped into (\d+) rows$`, st.groupedIntoRows)
	sc.Step(`^the sessions are grouped into two rows$`, func() error { return st.groupedIntoRows(2) })
	sc.Step(`^the "([^"]*)" row comes first with a count of (\d+)$`, st.rowFirstWithCount)
	sc.Step(`^traffic belonging to a different account$`, st.otherAccountTraffic)
	sc.Step(`^no session for it is listed$`, st.noSessionForIt)
	sc.Step(`^nothing about it is inferable from the snapshot$`, st.nothingInferable)
	sc.Step(`^the console relays a chat turn that returns a receipt$`, st.consoleChatWithReceipt)
	sc.Step(`^a session attributed to "([^"]*)" is recorded from that receipt$`, st.sessionAttributedConsole)
	sc.Step(`^it names the band and the station the receipt names$`, st.namesBandAndStation)
	sc.Step(`^the console relays a chat turn that fails$`, st.consoleChatFails)
	sc.Step(`^no session is recorded$`, st.noSessionRecorded)
	sc.Step(`^the Edge data is read many times$`, st.readManyTimes)
	sc.Step(`^no turn was dispatched$`, st.noTurnDispatched)
	sc.Step(`^the session ledger is unchanged$`, st.ledgerUnchanged)
	// 8. the node snapshot
	sc.Step(`^the node state and the event stream are read$`, st.stateAndEventsRead)
	sc.Step(`^neither carries fleet, candidate or session data$`, st.neitherCarriesEdge)
	sc.Step(`^the Edge tab reads its own endpoint, while shown, at the stream's cadence$`, st.readsOwnEndpointAtCadence)
	// instances.feature (@console)
	sc.Step(`^each node carries its instances with their names and capabilities$`, st.nodesCarryInstances)
	sc.Step(`^the panel draws them under the node, and the detail lists them$`, st.panelDrawsInstances)
	// mode.feature (@console)
	sc.Step(`^a console over an Edge rooted at the designated machine "([^"]*)"$`, st.consoleRootedLocal)
	sc.Step(`^it carries the root as LOCAL and the authority "([^"]*)"$`, st.carriesRootLocal)
	sc.Step(`^the panel shows the badge in the header$`, st.panelShowsBadge)
	// empty_edge.feature (@console)
	sc.Step(`^a console over a machine that is not enrolled, rooted at Core, scanning every (\d+) seconds$`, st.machineFresh)
	sc.Step(`^it carries a self status with enrolled false, authority "([^"]*)", discovery "([^"]*)" and an interval of (\d+) seconds$`, st.selfStatusIs)
	sc.Step(`^the empty state has a slot for THIS MACHINE, AUTHORITY and DISCOVERY$`, st.emptySlots)
	sc.Step(`^the Edge code fills them from the self status$`, st.fillsFromStatus)
	sc.Step(`^it names both ways to add a node$`, st.bothWays)
	sc.Step(`^a console over a machine whose discovery is off$`, st.machineDiscoveryOff)
	sc.Step(`^the self status says discovery "([^"]*)"$`, st.selfDiscovery)
	sc.Step(`^a console over a machine whose Edge record cannot be read$`, st.machineRecordUnread)
	sc.Step(`^the self status carries the error$`, st.selfStatusErr)
	sc.Step(`^the nodes are still listed$`, st.nodesStillListed)
}

func TestEdgeConsoleViewFeature(t *testing.T) {
	st := &consoleEdgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: st.init,
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/console_view.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the console Edge scenarios failed")
	}
}

// ---- empty_edge.feature (@console) ----------------------------------------------

func (s *consoleEdgeBDD) machineFresh(every int) error {
	s.self = &edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: int64(every)}
	return nil
}

func (s *consoleEdgeBDD) selfStatusIs(authority, discovery string, every int) error {
	st := s.snap.SelfStatus
	if st == nil {
		return fmt.Errorf("no self_status in the snapshot: %s", s.body)
	}
	return want(!st.Enrolled && st.Authority == authority && st.Discovery == discovery && st.IntervalS == int64(every),
		"self_status = %+v", *st)
}

func (s *consoleEdgeBDD) emptySlots() error {
	return s.htmlHas("empty-state slots", `id="edge-fact-machine"`, `id="edge-fact-authority"`, `id="edge-fact-discovery"`,
		"THIS MACHINE", "AUTHORITY", "DISCOVERY")
}

func (s *consoleEdgeBDD) fillsFromStatus() error {
	return s.jsHas("facts from the status", `d\.facts`, `edge-fact-machine`, `edge-fact-authority`, `edge-fact-discovery`)
}

func (s *consoleEdgeBDD) bothWays() error {
	if err := s.htmlHas("both ways", "run RogerAI on another machine on this network", "adopt"); err != nil {
		return err
	}
	return s.jsHas("enroll-against line", `edge-fact-enroll`, `enroll_against`)
}

func (s *consoleEdgeBDD) machineDiscoveryOff() error {
	s.self = &edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryOff}
	return nil
}

func (s *consoleEdgeBDD) selfDiscovery(want_ string) error {
	st := s.snap.SelfStatus
	return want(st != nil && st.Discovery == want_, "self_status = %+v", st)
}

func (s *consoleEdgeBDD) machineRecordUnread() error {
	s.self = &edge.SelfStatus{Err: "edge record: permission denied"}
	s.enroll("shed", []store.EdgeTransport{lanTr("192.168.1.10")}, edge.Serve)
	return nil
}

func (s *consoleEdgeBDD) selfStatusErr() error {
	st := s.snap.SelfStatus
	return want(st != nil && strings.Contains(st.Err, "permission denied"), "self_status = %+v", st)
}

func (s *consoleEdgeBDD) nodesStillListed() error {
	_, ok := s.node("shed")
	return want(ok && s.status == 200, "nodes = %v (status %d)", names(s.snap.Nodes), s.status)
}

func TestEmptyEdgeFeatureConsole(t *testing.T) {
	st := &consoleEdgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: st.init,
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/edge/empty_edge.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the empty-Edge console scenarios failed")
	}
}

// ---- instances.feature (@console) ------------------------------------------------

func (s *consoleEdgeBDD) nodesCarryInstances() error {
	// a node whose face reported two rogers: what the console must carry
	s.enroll("workshop", []store.EdgeTransport{lanTr("192.168.1.10")}, edge.Serve)
	_, err := s.fleet.Observe(s.ids["workshop"], edge.Observation{Name: "workshop", Kind: "host",
		Addr: "192.168.1.10:1", Fingerprint: strings.Repeat("ab", 32),
		Instances: []edge.Instance{
			{Name: "desk", Caps: []store.EdgeCap{{Name: "operate", State: "CLAIMED"}}},
			{Name: "share", Caps: []store.EdgeCap{{Name: "serve", State: "VERIFIED"}}, Bands: []string{"gpt-oss-20b"}},
		}})
	if err != nil {
		return err
	}
	s.readEdge()
	n, ok := s.node("workshop")
	if !ok || len(n.Instances) != 2 {
		return fmt.Errorf("node = %+v", n)
	}
	for _, in := range n.Instances {
		if in.Name == "" || len(in.Caps) == 0 {
			return fmt.Errorf("instance = %+v", in)
		}
	}
	if s.snap.SelfInstances == nil {
		return fmt.Errorf("self_instances absent: %s", s.body)
	}
	return nil
}

func (s *consoleEdgeBDD) panelDrawsInstances() error {
	return s.jsHas("instances drawn", `n\.instances`, `edge-insts`, `edge-inst-row`, `"instances"`)
}

func TestInstancesFeatureConsole(t *testing.T) {
	st := &consoleEdgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: st.init,
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/edge/instances.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the instances console scenarios failed")
	}
}

// ---- mode.feature (@console) ---------------------------------------------------

func (s *consoleEdgeBDD) consoleRootedLocal(where string) error {
	s.self = &edge.SelfStatus{Root: edge.RootLocal, Authority: where, AuthorityLocal: true, Discovery: edge.DiscoveryScanning, IntervalS: 30}
	return nil
}

func (s *consoleEdgeBDD) carriesRootLocal(where string) error {
	st := s.snap.SelfStatus
	if st == nil || st.Root != edge.RootLocal || st.Authority != where {
		return fmt.Errorf("self_status = %+v", st)
	}
	if s.snap.Facts == nil || !strings.Contains(s.snap.Facts.Root, "LOCAL ROOT") || !strings.Contains(s.snap.Facts.Root, where) {
		return fmt.Errorf("facts = %+v", s.snap.Facts)
	}
	return nil
}

func (s *consoleEdgeBDD) panelShowsBadge() error {
	if err := s.htmlHas("mode badge", `id="edge-mode"`); err != nil {
		return err
	}
	return s.jsHas("badge from the facts", `f\.root`, `f\.prefer`, `edge-mode`)
}

func TestModeFeatureConsole(t *testing.T) {
	st := &consoleEdgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: st.init,
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/edge/mode.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the mode console scenarios failed")
	}
}
