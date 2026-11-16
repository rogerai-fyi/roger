package main

// Executable spec: features/serving/probe_accounting.feature - Core's checks are counted as
// checks and cost as little as possible. Two real broker instances share one store; a real share
// node (internal/agent) serves a priced chat model behind a round-robin front; consumers pay
// through the real relay; Core's checks go through the real probe paths. The phone app's half
// of R1 lives in the iOS repo.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
)

const paModel = "acct-m"

// paFront is the load balancer in front of the two instances: round robin, and it counts and
// can fail the node's reads of its own record.
type paFront struct {
	targets []*httputil.ReverseProxy
	rr      atomic.Int64
	reads   atomic.Int64 // GET /nodes/served
	failRec atomic.Bool  // answer /nodes/served 503
}

func (f *paFront) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/nodes/served" {
		f.reads.Add(1)
		if f.failRec.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	f.targets[int(f.rr.Add(1))%len(f.targets)].ServeHTTP(w, r)
}

type paSent struct{ instance, node, kind string }

type paState struct {
	xi       *xiState
	front    *paFront
	lb       *httptest.Server
	upstream *httptest.Server
	upCalls  atomic.Int64
	sess     *agent.Session
	nodeID   string

	mu   sync.Mutex
	sent []paSent // checks Core let through (allowCheck)

	before   map[string]any
	beforeEa float64
	handled  int64
	clock    time.Time
}

func (s *paState) inst(n string) *broker { return s.xi.inst[n].b }

func (s *paState) reset(t *testing.T) {
	*s = paState{xi: &xiState{t: t, inst: map[string]*xiInst{}, nonce: xiNonce()}}
	_, s.xi.brokerPr, _ = ed25519.GenerateKey(nil)
	probeCheckSent = func(b *broker, node, kind string) {
		s.mu.Lock()
		s.sent = append(s.sent, paSent{b.instanceID, node, kind})
		s.mu.Unlock()
	}
	checkClock = time.Now
}

func (s *paState) sentChecks(kind string) []paSent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []paSent
	for _, c := range s.sent {
		if kind == "" || c.kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// --- background ----------------------------------------------------------------------------

func (s *paState) twoInstances() error {
	s.xi.db = xiStore(s.xi.t)
	s.xi.redisURL = xiRedisURL(s.xi.t)
	f := &paFront{}
	for _, n := range []string{"A", "B"} {
		inst := s.xi.newInstance(n, true)
		inst.b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
		u, _ := url.Parse(inst.url())
		f.targets = append(f.targets, httputil.NewSingleHostReverseProxy(u))
	}
	s.front = f
	s.lb = httptest.NewServer(f)
	s.xi.t.Cleanup(s.lb.Close)
	return nil
}

func (s *paState) nodeServing() error {
	s.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.upCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	s.xi.t.Cleanup(s.upstream.Close)
	s.xi.t.Setenv("XDG_CONFIG_HOME", s.xi.t.TempDir())
	// Logged in as its owner: the node's key is a GitHub-linked owner's, as after `roger login`.
	// A priced registration is signed with the CLI's user key (cached once per process), so that
	// is the key bound; its GitHub id is derived from it so a repeat bind is the same owner.
	pub := hex.EncodeToString(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey))
	sum := sha256.Sum256([]byte(pub))
	gid := int64(binary.BigEndian.Uint32(sum[:4]) >> 1)
	if err := s.xi.db.BindOwner(store.Owner{GitHubID: gid, Login: "acct-op-" + pub[:8], Pubkey: pub}); err != nil {
		return err
	}
	// Consumers pay with real credits: a spend from the free starter credit earns no operator
	// anything (the money specs), which would leave nothing to show as earned.
	if err := s.xi.fundedConsumer(); err != nil {
		return err
	}
	s.nodeID = "acct-" + xiNonce()
	sess, err := agent.Start(agent.Config{NodeID: s.nodeID, Broker: s.lb.URL, Upstream: s.upstream.URL,
		Model: paModel, Modality: "chat", Ctx: 4096, Parallel: 2, PriceIn: 1, PriceOut: 2})
	if err != nil {
		return err
	}
	s.sess = sess
	s.xi.t.Cleanup(sess.Stop)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range []string{"A", "B"} {
			s.inst(n).syncLivenessOnce()
		}
		if s.inst("A").nodeLive(s.nodeID) && s.inst("B").nodeLive(s.nodeID) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the node never came on air on both instances")
}

// --- R1 ------------------------------------------------------------------------------------

func (s *paState) reg(n string) protocol.NodeRegistration {
	b := s.inst(n)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nodes[s.nodeID]
}

func (s *paState) paid(n int) error {
	for i := 0; i < n; i++ {
		if err := s.xi.relayHTTP(s.xi.inst[[]string{"A", "B"}[i%2]], paModel, "consumer", false); err != nil {
			return err
		}
		if s.xi.relayCode != http.StatusOK {
			return fmt.Errorf("paid request %d answered %d %s", i, s.xi.relayCode, s.xi.relayBody)
		}
	}
	return nil
}

func (s *paState) checks(n int) error {
	for i := 0; i < n; i++ {
		s.inst("A").probeNode(s.reg("A"), paModel, nextCanary(uint64(i)))
	}
	return nil
}

func (s *paState) servesPaidAndChecks(paid, checks int) error {
	if err := s.paid(paid); err != nil {
		return err
	}
	if err := s.checks(checks); err != nil {
		return err
	}
	s.handled = int64(paid + checks)
	return nil
}

// coreNumbers waits for the share screen's read of Core's record, as the TUI does by reading it.
func (s *paState) coreNumbers() (served int64, earned float64, err error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		served, _ = s.sess.Served()
		earned = s.sess.Earnings()
		if s.sess.CoreCurrent() {
			return served, earned, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, 0, fmt.Errorf("the share screen never got Core's numbers")
}

func (s *paState) cliShowsServed(n int) error {
	served, _, err := s.coreNumbers()
	if err != nil {
		return err
	}
	if served != int64(n) {
		return fmt.Errorf("the share screen shows %d served, want %d", served, n)
	}
	return nil
}

func (s *paState) earnedIsSettledShare(int) error {
	_, earned, err := s.coreNumbers()
	if err != nil {
		return err
	}
	want, err := s.xi.db.EarningsOf(s.nodeID)
	if err != nil {
		return err
	}
	if want <= 0 || fmt.Sprintf("%.6f", earned) != fmt.Sprintf("%.6f", want) {
		return fmt.Errorf("the share screen shows %.6f earned, Core's record holds %.6f", earned, want)
	}
	return nil
}

func (s *paState) readFromCore() error {
	if s.front.reads.Load() == 0 {
		return fmt.Errorf("the share screen never read GET /nodes/served")
	}
	return nil
}

func (s *paState) oneCheckAtOwnRate() error {
	before, err := s.xi.db.EarningsOf(s.nodeID)
	if err != nil {
		return err
	}
	s.beforeEa = before
	return s.checks(1)
}

func (s *paState) noEarningsRise() error {
	after, err := s.xi.db.EarningsOf(s.nodeID)
	if err != nil {
		return err
	}
	if after != s.beforeEa {
		return fmt.Errorf("Core's record of earnings (the web console's and /nodes/served's source) rose from %v to %v", s.beforeEa, after)
	}
	_, earned, err := s.coreNumbers()
	if err != nil {
		return err
	}
	if earned != 0 {
		return fmt.Errorf("the CLI shows %v earned after only a check", earned)
	}
	return nil
}

var paPseudonym = regexp.MustCompile(`^u_[0-9a-f]{16}$`)

func (s *paState) checkLooksLikeCustomer() error {
	b := s.inst("A")
	a, c := b.probePseudonym(s.nodeID), b.probePseudonym(s.nodeID)
	if !paPseudonym.MatchString(a) || a == c {
		return fmt.Errorf("check pseudonyms %q and %q are not fresh customer-style pseudonyms", a, c)
	}
	for _, ty := range []reflect.Type{reflect.TypeOf(protocol.Job{}), reflect.TypeOf(protocol.JobResult{})} {
		for i := 0; i < ty.NumField(); i++ {
			name := strings.ToLower(ty.Field(i).Name + " " + ty.Field(i).Tag.Get("json"))
			for _, w := range []string{"probe", "canary", "check", "billed"} {
				if strings.Contains(name, w) {
					return fmt.Errorf("%s has a field %q that could mark a check", ty.Name(), name)
				}
			}
		}
	}
	return nil
}

func (s *paState) noAnswerSaysCheck() error {
	// The node's own record read is aggregates only: nothing in it names a request.
	raw := s.recordRaw(s.lb.URL)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		return fmt.Errorf("record: %v %s", err, raw)
	}
	for k := range got {
		switch k {
		case "node", "since", "served", "earned", "tokens", "capped", "as_of":
		default:
			return fmt.Errorf("the node's record carries %q, more than aggregates", k)
		}
	}
	return nil
}

func (s *paState) recordRaw(base string) []byte {
	req, _ := http.NewRequest(http.MethodGet, base+"/nodes/served?node="+url.QueryEscape(s.nodeID)+"&since=0", nil)
	req.Header.Set("Authorization", "Bearer "+s.sess.BridgeToken())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []byte(err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw
}

func (s *paState) handledAndPaid(handled, paid int) error {
	return s.servesPaidAndChecks(paid, handled-paid)
}

func (s *paState) headlineServed(n int) error { return s.cliShowsServed(n) }

func (s *paState) networkChecks(n int) error {
	if _, _, err := s.coreNumbers(); err != nil {
		return err
	}
	reqs, _ := s.sess.ProbeStats()
	if reqs != int64(n) {
		return fmt.Errorf("the share screen shows %d network checks, want %d", reqs, n)
	}
	return nil
}

func (s *paState) recordDown() error {
	if err := s.servesPaidAndChecks(1, 1); err != nil {
		return err
	}
	if _, _, err := s.coreNumbers(); err != nil {
		return err
	}
	s.front.failRec.Store(true)
	s.sess.ExpireCoreForTest()
	_ = s.paid(1)
	return nil
}

func (s *paState) lastNumbersNotCurrent() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		served, _ := s.sess.Served()
		if !s.sess.CoreCurrent() {
			if served != 1 {
				return fmt.Errorf("the share screen shows %d served while Core cannot answer; the last number Core gave was 1", served)
			}
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the share screen still says its numbers are current while Core cannot answer")
}

func (s *paState) neverLocalCount() error {
	served, _ := s.sess.Served()
	if h, _ := s.sess.Handled(); served == h {
		return fmt.Errorf("the share screen fell back to the node's own count (%d)", h)
	}
	return nil
}

func (s *paState) readSparingly() error {
	before := s.front.reads.Load()
	for i := 0; i < 200; i++ {
		s.sess.Served()
		s.sess.Earnings()
		time.Sleep(2 * time.Millisecond)
	}
	if n := s.front.reads.Load() - before; n > 1 {
		return fmt.Errorf("the share screen read Core's record %d times in under a second", n)
	}
	return nil
}

func (s *paState) coreAnswers429() error {
	// Straight at one instance, twice: the second inside 30 s is refused.
	base := s.xi.inst["A"].url()
	_ = s.recordRaw(base)
	req, _ := http.NewRequest(http.MethodGet, base+"/nodes/served?node="+url.QueryEscape(s.nodeID)+"&since=0", nil)
	req.Header.Set("Authorization", "Bearer "+s.sess.BridgeToken())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		return fmt.Errorf("a second read inside 30 s answered %d (Retry-After %q)", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	return nil
}

func (s *paState) notWhileClosed() error {
	// Nothing reads the session here: a share running with no screen open reads nothing.
	before := s.front.reads.Load()
	time.Sleep(300 * time.Millisecond)
	if s.front.reads.Load() != before {
		return fmt.Errorf("a share nobody is looking at read Core's record")
	}
	return nil
}

// --- R2 ------------------------------------------------------------------------------------

func (s *paState) roundDueOnBoth() error {
	for _, n := range []string{"A", "B"} {
		b := s.inst(n)
		b.metricsMu.Lock()
		b.probeSchedLocked()[s.nodeID] = &probeState{nextDue: time.Now().Add(-time.Second)}
		b.metricsMu.Unlock()
	}
	var wg sync.WaitGroup
	for _, n := range []string{"A", "B"} {
		wg.Add(1)
		go func(b *broker) { defer wg.Done(); b.probeOnce() }(s.inst(n))
	}
	wg.Wait()
	return s.waitChecks("canary", 1)
}

func (s *paState) waitChecks(kind string, n int) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.sentChecks(kind)) >= n {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("%d %s check(s) were sent, want %d", len(s.sentChecks(kind)), kind, n)
}

func (s *paState) exactlyOneInstance() error {
	time.Sleep(probeJitter + time.Second) // the other instance's round would have fired by now
	canaries := s.sentChecks("canary")
	if len(canaries) != 1 {
		return fmt.Errorf("%d canaries were sent for one round, want 1", len(canaries))
	}
	for _, c := range s.sentChecks("") {
		if c.instance != canaries[0].instance {
			return fmt.Errorf("both instances sent checks: %+v", s.sentChecks(""))
		}
	}
	return nil
}

func (s *paState) otherSendsNothing() error { return nil } // proven with the step above

func (s *paState) onePasses() error {
	if err := s.roundDueOnBoth(); err != nil {
		return err
	}
	// The round's prober finishes measuring.
	prober := s.sentChecks("canary")[0].instance
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range []string{"A", "B"} {
			b := s.inst(n)
			if b.instanceID != prober {
				continue
			}
			b.metricsMu.Lock()
			tq := b.trust[s.nodeID]
			b.metricsMu.Unlock()
			if tq.probed && tq.probeOK {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("the prober never recorded a pass")
}

func (s *paState) bothFresh() error {
	var readings []string
	for _, n := range []string{"A", "B"} {
		b := s.inst(n)
		b.probeOnce() // the other instance takes the shared result on its next tick
		b.metricsMu.Lock()
		tq := b.trust[s.nodeID]
		st := b.probeSchedLocked()[s.nodeID]
		b.metricsMu.Unlock()
		if !tq.probed || !tq.probeOK || st == nil || time.Since(st.lastMeasured) > time.Minute {
			return fmt.Errorf("instance %s does not read the node as freshly measured (%+v)", n, tq)
		}
		readings = append(readings, fmt.Sprintf("%.0f", tq.ttftMs))
	}
	if readings[0] != readings[1] {
		return fmt.Errorf("the instances read different latencies: %v", readings)
	}
	return nil
}

func (s *paState) routingUsesIt() error {
	for _, n := range []string{"A", "B"} {
		b := s.inst(n)
		b.metricsMu.Lock()
		stale := b.measurementStalenessLocked(s.nodeID, time.Now())
		b.metricsMu.Unlock()
		if stale < 1.0 {
			return fmt.Errorf("instance %s routes on a stale reading (%.2f)", n, stale)
		}
	}
	return nil
}

func (s *paState) storeDown() error {
	for _, n := range []string{"A", "B"} {
		s.inst(n).shared = &downStore{memStore: newMemStore()}
	}
	return nil
}

// downStore is a shared store whose every call fails: Core's store is down.
type downStore struct{ *memStore }

func (d *downStore) healthy() bool { return false }

func (s *paState) eachProbesAsToday() error {
	for _, n := range []string{"A", "B"} {
		b := s.inst(n)
		b.multiInstance = false // no bus without a store: each probes its own pollers
		b.metricsMu.Lock()
		b.probeSchedLocked()[s.nodeID] = &probeState{nextDue: time.Now().Add(-time.Second)}
		b.metricsMu.Unlock()
		if !b.allowCheck(s.nodeID, "canary") {
			return fmt.Errorf("instance %s refused a check because the store is down", n)
		}
		before := time.Now()
		b.probeOnce()
		b.metricsMu.Lock()
		st := b.probeSchedLocked()[s.nodeID]
		b.metricsMu.Unlock()
		if st == nil || st.nextDue.Sub(before) < b.probe.interval-time.Second {
			return fmt.Errorf("instance %s scheduled its next probe sooner than today's floor", n)
		}
	}
	return nil
}

func (s *paState) noFaster() error { return nil } // proven with the step above

// --- R3 ------------------------------------------------------------------------------------

func (s *paState) lightRig() *broker {
	b := probeMinBroker()
	b.db = store.NewMem() // so a stale node really is pruned
	b.toolsOK = map[string]bool{}
	addNode(b, "n", 0)
	return b
}

func (s *paState) toolBackoff(n int, after string) error {
	b := s.lightRig()
	d, err := time.ParseDuration(strings.NewReplacer(" minutes", "m", " hours", "h").Replace(after))
	if err != nil {
		return err
	}
	t0 := time.Now()
	b.metricsMu.Lock()
	b.toolFails = map[string]toolBackoffState{toolKey("n", "m"): {streak: n, last: t0}}
	b.metricsMu.Unlock()
	if b.toolProbeDue("n", "m", t0.Add(d-time.Second)) {
		return fmt.Errorf("after %d failures a check was due before %s", n, after)
	}
	if !b.toolProbeDue("n", "m", t0.Add(d)) {
		return fmt.Errorf("after %d failures no check was due at %s", n, after)
	}
	return nil
}

func (s *paState) toolFailedFour() error {
	b := s.lightRig()
	s.before = map[string]any{"b": b}
	b.metricsMu.Lock()
	b.toolFails = map[string]toolBackoffState{toolKey("n", "m"): {streak: 4, last: time.Now()}}
	b.metricsMu.Unlock()
	return nil
}

func (s *paState) nextPasses() error {
	b := s.before["b"].(*broker)
	b.applyToolVerdict("n", "m", protocol.JobResult{Status: http.StatusOK, Body: []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"` + toolCanaryFn + `_x","arguments":"{\"token\":\"x\"}"}}]}}]}`)}, true, "x")
	return nil
}

func (s *paState) earnsAndThrottled() error {
	b := s.before["b"].(*broker)
	b.metricsMu.Lock()
	ok, st := b.toolsOK[toolKey("n", "m")], b.toolFails[toolKey("n", "m")]
	b.metricsMu.Unlock()
	if !ok || st.streak != 0 {
		return fmt.Errorf("a pass left tools=%v streak=%d", ok, st.streak)
	}
	now := time.Now()
	if !b.toolProbeDue("n", "m", now) || b.toolProbeDue("n", "m", now.Add(toolProbeEvery-time.Second)) {
		return fmt.Errorf("a verified model is not on the verified throttle")
	}
	return nil
}

func (s *paState) backedOff80() error {
	b := s.lightRig()
	s.before = map[string]any{"b": b, "t0": time.Now()}
	b.metricsMu.Lock()
	b.toolFails = map[string]toolBackoffState{toolKey("n", "m"): {streak: 3, last: time.Now()}}
	b.metricsMu.Unlock()
	return nil
}

func (s *paState) reconnects() error {
	b := s.before["b"].(*broker)
	b.lastSeen["n"] = time.Now().Add(-24 * time.Hour) // gone long enough to be pruned
	b.pruneStaleNodes(time.Now())
	addNode(b, "n", 0)
	return nil
}

func (s *paState) backoffContinues() error {
	b := s.before["b"].(*broker)
	t0 := s.before["t0"].(time.Time)
	if b.toolProbeDue("n", "m", t0.Add(79*time.Minute)) {
		return fmt.Errorf("a reconnect restarted the tool-call back-off")
	}
	return nil
}

// --- R4 ------------------------------------------------------------------------------------

func (s *paState) idleDayBrowsed() error {
	b := s.inst("A")
	t0 := time.Now()
	var allowed []time.Time
	for i := 0; i < 24*120; i++ { // every 30 s for a day, as a browsed model's demand would ask
		at := t0.Add(time.Duration(i) * 30 * time.Second)
		checkClock = func() time.Time { return at }
		for _, kind := range []string{"canary", "tools", "tower"} {
			if b.allowCheck("budget-"+s.xi.nonce, kind) {
				allowed = append(allowed, at)
			}
		}
	}
	checkClock = time.Now
	s.before = map[string]any{"allowed": allowed}
	return nil
}

func (s *paState) withinBudget() error {
	allowed := s.before["allowed"].([]time.Time)
	if len(allowed) > checkBudgetDay {
		return fmt.Errorf("%d checks in a day, budget %d", len(allowed), checkBudgetDay)
	}
	for i := range allowed {
		n := 0
		for j := i; j < len(allowed) && allowed[j].Sub(allowed[i]) < time.Hour; j++ {
			n++
		}
		if n > checkBudgetHour {
			return fmt.Errorf("%d checks within an hour from %s, budget %d", n, allowed[i].Format(time.Kitchen), checkBudgetHour)
		}
	}
	if len(allowed) == 0 {
		return fmt.Errorf("the budget let nothing through")
	}
	return nil
}

func (s *paState) allKindsAllInstances() error {
	// The same window answers both instances: B is refused once A spent it.
	key := "shared-" + s.xi.nonce
	for i := 0; i < checkBudgetHour; i++ {
		if !s.inst("A").allowCheck(key, "canary") {
			return fmt.Errorf("instance A was refused check %d of %d", i+1, checkBudgetHour)
		}
	}
	for _, kind := range []string{"canary", "tools", "tower"} {
		if s.inst("B").allowCheck(key, kind) {
			return fmt.Errorf("instance B was allowed a %s check past the hourly budget A spent", kind)
		}
	}
	return nil
}

func (s *paState) budgetSpent() error {
	for i := 0; i < checkBudgetHour; i++ {
		s.inst("A").allowCheck(s.nodeID, "canary")
	}
	s.mu.Lock()
	s.sent = nil
	s.mu.Unlock()
	s.before = map[string]any{"calls": s.upCalls.Load()}
	b := s.inst("A")
	b.metricsMu.Lock()
	s.before["trust"] = b.trust[s.nodeID]
	b.metricsMu.Unlock()
	return s.checks(1)
}

func (s *paState) skippedKeepsReading() error {
	if n := s.upCalls.Load() - s.before["calls"].(int64); n != 0 {
		return fmt.Errorf("the node ran %d job(s) past its budget", n)
	}
	b := s.inst("A")
	b.metricsMu.Lock()
	tq := b.trust[s.nodeID]
	b.metricsMu.Unlock()
	if !reflect.DeepEqual(tq, s.before["trust"].(trustState)) {
		return fmt.Errorf("a refused check changed the node's reading")
	}
	return nil
}

func (s *paState) servesPaid() error {
	b := s.inst("A")
	b.metricsMu.Lock()
	b.probeSchedLocked()[s.nodeID] = &probeState{nextDue: time.Now()}
	b.metricsMu.Unlock()
	return s.paid(2)
}

func (s *paState) paidIsMeasurement() error {
	for _, n := range []string{"A", "B"} {
		b := s.inst(n)
		b.metricsMu.Lock()
		st := b.probeSchedLocked()[s.nodeID]
		b.metricsMu.Unlock()
		if st != nil && !st.lastMeasured.IsZero() && st.nextDue.After(time.Now()) {
			return nil
		}
	}
	return fmt.Errorf("a paid request did not count as a measurement on either instance")
}

// --- R5 ------------------------------------------------------------------------------------

func (s *paState) batteryNode() error {
	b := s.lightRig()
	reg := b.nodes["n"]
	reg.Battery = true
	b.nodes["n"] = reg
	b.trust["n"] = trustState{}
	s.before = map[string]any{"b": b}
	return nil
}

func (s *paState) comesOnAir() error {
	b := s.before["b"].(*broker)
	b.probeOnce()
	s.before["first"] = lastProbeOf(b, "n")
	b.recordProbe("n", probePass, 100, 10, true, true) // its first-sight canary passes
	return nil
}

func (s *paState) oneAtFirstSight() error {
	if s.before["first"].(time.Time).IsZero() {
		return fmt.Errorf("no first-sight canary")
	}
	return nil
}

func (s *paState) ceilingAfter() error {
	b := s.before["b"].(*broker)
	first := s.before["first"].(time.Time)
	for i := 0; i < 50; i++ {
		_ = b.computeMarket() // consumers browse
		b.mu.Lock()
		_ = b.enrichOffersForNode(nil, b.nodes["n"], time.Now(), nil, true)
		b.mu.Unlock()
		b.probeOnce()
	}
	if got := lastProbeOf(b, "n"); !got.Equal(first) {
		return fmt.Errorf("a battery node was checked again %s after first sight", got.Sub(first))
	}
	b.metricsMu.Lock()
	due := b.probeSched["n"].nextDue
	b.metricsMu.Unlock()
	if due.Sub(first) < b.probe.ceiling-time.Second {
		return fmt.Errorf("a battery node's next check is due %s after first sight, before the ceiling", due.Sub(first))
	}
	return nil
}

func (s *paState) browsedOften() error {
	b := s.lightRig()
	s.before = map[string]any{"b": b}
	key := "browse-" + s.xi.nonce
	n := 0
	t0 := time.Now()
	for i := 0; i < 1200; i++ { // an hour of browsing every 3 s
		at := t0.Add(time.Duration(i) * 3 * time.Second)
		checkClock = func() time.Time { return at }
		b.metricsMu.Lock()
		b.demandProbeSoonLocked("n", at)
		b.metricsMu.Unlock()
		if s.inst("A").allowCheck(key, "canary") {
			n++
		}
	}
	checkClock = time.Now
	s.before["n"] = n
	return nil
}

func (s *paState) withinCadenceAndBudget() error {
	if n := s.before["n"].(int); n > checkBudgetHour {
		return fmt.Errorf("an hour of browsing cost the node %d checks, budget %d", n, checkBudgetHour)
	}
	return nil
}

func TestProbeAccountingBDD(t *testing.T) {
	s := &paState{}
	prevSent, prevClock := probeCheckSent, checkClock
	t.Cleanup(func() { probeCheckSent, checkClock = prevSent, prevClock })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if s.sess != nil {
					s.sess.Stop()
				}
				return ctx, nil
			})
			sc.Step(`^Roger Core runs two instances sharing one store$`, s.twoInstances)
			sc.Step(`^a node serving one chat model, logged in as its owner$`, s.nodeServing)

			sc.Step(`^the node serves (\d+) paid requests and Core sends it (\d+) checks$`, s.servesPaidAndChecks)
			sc.Step(`^the CLI share screen and the phone app show (\d+) served$`, s.cliShowsServed)
			sc.Step(`^they show the owner share of the (\d+) settled requests as earned$`, s.earnedIsSettledShare)
			sc.Step(`^both numbers are read from Core's record of this node's settled requests \(GET /nodes/served\)$`, s.readFromCore)
			sc.Step(`^Core sends the node a canary priced at the node's own rate$`, s.oneCheckAtOwnRate)
			sc.Step(`^no earnings figure on the CLI, the phone app or the web console rises$`, s.noEarningsRise)
			sc.Step(`^a check job carries a fresh customer-style pseudonym and no field marking it as a check$`, s.checkLooksLikeCustomer)
			sc.Step(`^no answer to the node, before or after it serves the job, says it was a check$`, s.noAnswerSaysCheck)
			sc.Step(`^the node handled (\d+) jobs and Core's record shows (\d+) paid$`, s.handledAndPaid)
			sc.Step(`^the share screen shows "(\d+) served" as the headline$`, s.headlineServed)
			sc.Step(`^it may show "(\d+) network checks" as the difference, labelled as Core checking the node$`, s.networkChecks)
			sc.Step(`^GET /nodes/served does not answer$`, s.recordDown)
			sc.Step(`^the share screen shows the last numbers Core gave, marked as not current$`, s.lastNumbersNotCurrent)
			sc.Step(`^it never falls back to the node's own count as "served"$`, s.neverLocalCount)
			sc.Step(`^the share screen reads GET /nodes/served at most once every 30 seconds while it is open$`, s.readSparingly)
			sc.Step(`^Core answers a node that asks more often with 429 and Retry-After$`, s.coreAnswers429)
			sc.Step(`^not at all while it is closed or the app is in the background$`, s.notWhileClosed)

			sc.Step(`^a probe round comes due for the node on both instances$`, s.roundDueOnBoth)
			sc.Step(`^exactly one instance sends the check, holding a shared lease on that node for the round$`, s.exactlyOneInstance)
			sc.Step(`^the other instance sends nothing$`, s.otherSendsNothing)
			sc.Step(`^one instance's check of the node passes$`, s.onePasses)
			sc.Step(`^both instances read the node as freshly measured, with the same latency and pass reading$`, s.bothFresh)
			sc.Step(`^routing on either instance uses that reading$`, s.routingUsesIt)
			sc.Step(`^the shared store does not answer$`, s.storeDown)
			sc.Step(`^each instance probes on its own schedule, as today, and no instance probes faster than today$`, s.eachProbesAsToday)

			sc.Step(`^the model has failed or not answered (\d+) tool-call checks in a row$`, func(n int) error { s.before = map[string]any{"n": n}; return nil })
			sc.Step(`^its next tool-call check is due (.+) after the last one$`, func(after string) error { return s.toolBackoff(s.before["n"].(int), after) })
			sc.Step(`^the model has failed 4 tool-call checks in a row$`, s.toolFailedFour)
			sc.Step(`^the next one passes$`, s.nextPasses)
			sc.Step(`^the model earns "tools" and is re-checked on the verified throttle, as today$`, s.earnsAndThrottled)
			sc.Step(`^the model is backed off to 80 minutes$`, s.backedOff80)
			sc.Step(`^the node reconnects with the same node id and model$`, s.reconnects)
			sc.Step(`^the back-off continues where it was$`, s.backoffContinues)

			sc.Step(`^a node comes on air and stays idle for a day, with consumers browsing its model all day$`, s.idleDayBrowsed)
			sc.Step(`^it receives at most 12 Core checks in any hour and at most 60 in the day$`, s.withinBudget)
			sc.Step(`^that counts canaries, tool-call checks and Tower-carried canaries together, on all instances$`, s.allKindsAllInstances)
			sc.Step(`^a probe comes due and the node's hourly budget is spent$`, s.budgetSpent)
			sc.Step(`^no check is sent and the node keeps its current reading$`, s.skippedKeepsReading)
			sc.Step(`^the node serves paid requests$`, s.servesPaid)
			sc.Step(`^each one counts as a fresh measurement and pushes the next check out, as today$`, s.paidIsMeasurement)

			sc.Step(`^the phone app registers its node as running on battery$`, s.batteryNode)
			sc.Step(`^the node comes on air$`, s.comesOnAir)
			sc.Step(`^it receives one canary at first sight$`, s.oneAtFirstSight)
			sc.Step(`^after that at most one check per probe ceiling \(30 minutes\), whatever consumers browse$`, s.ceilingAfter)
			sc.Step(`^an idle node with one verified chat model$`, func() error { return nil })
			sc.Step(`^its canaries come once per probe ceiling \(30 minutes\) and its tool support is fully re-checked once a day$`, func() error {
				if defaultProbeCeiling != 30*time.Minute || toolReverifyEvery != 24*time.Hour {
					return fmt.Errorf("ceiling %s, tool re-check %s", defaultProbeCeiling, toolReverifyEvery)
				}
				return nil
			})
			sc.Step(`^that is within its daily budget$`, func() error {
				if n := int(24*time.Hour/defaultProbeCeiling) + 1; n > checkBudgetDay {
					return fmt.Errorf("an idle node needs %d checks a day, budget %d", n, checkBudgetDay)
				}
				return nil
			})
			sc.Step(`^each passed canary re-asserts its verified tools mark, so neither "verified" nor "tools" ages out between checks$`, s.canaryReassertsTools)
			sc.Step(`^consumers browse /discover or /market for the node's model every few seconds$`, s.browsedOften)
			sc.Step(`^the node's checks stay within its cadence and its budget$`, s.withinCadenceAndBudget)
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t,
			Paths: []string{"../../features/serving/probe_accounting.feature"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/serving/probe_accounting.feature failed")
	}
}

// canaryReassertsTools: on the node's poll host, a passed canary refreshes the shared tools mark
// of its verified model, and the reading stays fresh for the whole ceiling.
func (s *paState) canaryReassertsTools() error {
	if defaultProbeCeiling >= toolsVerifiedTTL {
		return fmt.Errorf("canaries every %s cannot keep a mark that lasts %s", defaultProbeCeiling, toolsVerifiedTTL)
	}
	vs, mr := testValkeyShared(s.xi.t)
	b := probeMinBroker()
	b.probe.ceiling = defaultProbeCeiling
	b.shared, b.toolsOK = vs, map[string]bool{toolKey("n", "m"): true}
	addNode(b, "n", 0)
	b.localPollAt = map[string]time.Time{"n": time.Now()} // this instance hosts the node's polls
	b.recordProbe("n", probePass, 120, 50, true, true)
	if v := mr.HGet(toolsKey(), "n\x00m"); v == "" {
		return fmt.Errorf("a passed canary did not re-assert the verified tools mark")
	}
	b.metricsMu.Lock()
	stale := b.measurementStalenessLocked("n", time.Now().Add(defaultProbeCeiling-time.Minute))
	b.metricsMu.Unlock()
	if stale < 1 {
		return fmt.Errorf("a reading went stale inside the ceiling (%.2f)", stale)
	}
	return nil
}

// Audit finding: the battery clamp held after a FAILED first check too, stranding the node
// unverified for the ceiling. A failed first check keeps the adaptive retry.
func TestABatteryNodeWhoseFirstCheckFailedIsRetriedSoon(t *testing.T) {
	b := probeMinBroker()
	b.db = store.NewMem()
	addNode(b, "n", 0)
	reg := b.nodes["n"]
	reg.Battery = true
	b.nodes["n"] = reg
	b.trust["n"] = trustState{}
	b.probeOnce()
	b.recordProbe("n", probeDead, 0, 0, false, false)
	b.metricsMu.Lock()
	due := b.probeSched["n"].nextDue
	b.metricsMu.Unlock()
	if due.Sub(time.Now()) > time.Minute {
		t.Fatalf("a battery node whose first check failed waits %s for a retry", due.Sub(time.Now()))
	}
}
