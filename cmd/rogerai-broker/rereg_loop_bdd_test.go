package main

// rereg_loop_bdd_test.go makes the BROKER and END-TO-END scenarios of
// features/multinode/rereg_loop.feature executable. The node-only scenarios (recover keeps the
// token, pacing, single flight, confidential re-attestation, a fresh process mints a fresh
// token) run against the real reregistrar in internal/agent/rereg_loop_bdd_test.go; godog runs
// each half by scenario line, so every scenario in the approved file runs exactly once.
//
// Same world as cross_instance_bdd_test.go: real broker instances over real HTTP running the
// full route table, a real Valkey protocol (miniredis unless ROGERAI_TEST_REDIS_URL is set),
// one shared store, real signed registrations. No mocks: the one wrapper (countingShared)
// forwards every call to the real store and only counts reads.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
)

// rereg_loop.feature scenario lines that run HERE (broker + end to end). The rest
// (136-170) run in internal/agent.
var rrBrokerLines = []int{46, 64, 71, 85, 91, 98, 106, 112, 116, 122, 128, 179, 186, 193}

func TestReregLoopBDD(t *testing.T) {
	paths := make([]string, 0, len(rrBrokerLines))
	for _, l := range rrBrokerLines {
		paths = append(paths, "../../features/multinode/rereg_loop.feature:"+strconv.Itoa(l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: rrInitScenarios(t),
		Options:             &godog.Options{Format: "pretty", Paths: paths, TestingT: t, Strict: true},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("multinode/rereg_loop broker scenarios failed (see godog output above)")
	}
}

// countingShared is the REAL shared store with its per-node registry reads counted.
type countingShared struct {
	sharedStore
	reads atomic.Int64
}

func (c *countingShared) getNode(id string) ([]byte, bool, error) {
	c.reads.Add(1)
	return c.sharedStore.getNode(id)
}

func (c *countingShared) getPrivateNode(id string) ([]byte, bool, error) {
	c.reads.Add(1)
	return c.sharedStore.getPrivateNode(id)
}

type rrState struct {
	*xiState
	tokens  map[string]string // T0, T1, T2, Told, Tnew -> concrete token
	status  map[string]int    // last answer per instance
	answers []int             // bulk answers (token guessing)
	elapsed time.Duration
	counter *countingShared
	stale   []byte // an in-flight sync's earlier read of the shared registry
}

func (s *rrState) tok(name string) string {
	if v, ok := s.tokens[name]; ok {
		return v
	}
	v := "tok-" + strings.ToLower(name) + "-" + s.nonce
	s.tokens[name] = v
	return v
}

// direct sends req through inst's real route table IN PROCESS. Used where the bytes of the
// Authorization header matter: a real HTTP server strips trailing whitespace from header
// values before any handler sees them (RFC 9110 field values exclude it), so a trailing-space
// token can only reach the broker's comparison this way.
func (s *rrState) direct(inst string, req *http.Request) int {
	rec := httptest.NewRecorder()
	streamSafeHandler(s.inst[inst].b.routes()).ServeHTTP(rec, req)
	return rec.Code
}

func (s *rrState) heartbeatReq(inst, node, bearer string) *http.Request {
	body := `{"node_id":"` + node + `"}`
	req := httptest.NewRequest(http.MethodPost, s.inst[inst].url()+"/nodes/heartbeat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	return req
}

// call sends one node-facing request over real HTTP presenting tok and records the answer.
func (s *rrState) call(endpoint, inst, tok string) error {
	base := s.inst[inst].url()
	id := s.node.id
	var req *http.Request
	switch endpoint {
	case "heartbeat":
		req, _ = http.NewRequest(http.MethodPost, base+"/nodes/heartbeat", strings.NewReader(`{"node_id":"`+id+`"}`))
		req.Header.Set("Content-Type", "application/json")
	case "poll":
		req, _ = http.NewRequest(http.MethodGet, base+"/agent/poll?node="+id, nil)
	case "result":
		res, _ := json.Marshal(protocol.JobResult{ID: "job-rl-" + s.nonce, Status: http.StatusOK})
		req, _ = http.NewRequest(http.MethodPost, base+"/agent/result?node="+id, strings.NewReader(string(res)))
		req.Header.Set("Content-Type", "application/json")
	case "stream":
		req, _ = http.NewRequest(http.MethodPost, base+"/agent/stream?node="+id+"&job=job-rl-"+s.nonce, strings.NewReader(""))
	default:
		return fmt.Errorf("unknown endpoint %q", endpoint)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	start := time.Now()
	resp, err := s.node.hc.Do(req)
	s.elapsed = time.Since(start)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	s.status[inst] = resp.StatusCode
	return nil
}

func (s *rrState) holds(inst, name string) error {
	b := s.inst[inst].b
	b.mu.Lock()
	t := b.tunnels[s.node.id]
	got := ""
	if t != nil {
		got = t.token
	}
	b.mu.Unlock()
	if want := s.tok(name); got != want {
		return fmt.Errorf("instance %s holds token %q for the node, want %s (%q)", inst, got, name, want)
	}
	return nil
}

// --- Given ------------------------------------------------------------------------------

func (s *rrState) twoBrokers() error { return s.xiState.twoBrokers("ON") }

func (s *rrState) mk(name string, private bool) *xiNode {
	n := s.mkNode(name, "free-m", 0, private) // a private band HARD-requires an owner
	n.private = private
	return n
}

func (s *rrState) registerAs(inst, tokName string, ts int64) error {
	s.node.mu.Lock()
	s.node.token = s.tok(tokName)
	s.node.ts = ts
	s.node.mu.Unlock()
	return s.node.register(s.t, s.inst[inst])
}

func (s *rrState) learn(inst, tokName string) error {
	if err := s.call("heartbeat", inst, s.tok(tokName)); err != nil {
		return err
	}
	if s.status[inst] != http.StatusOK {
		return fmt.Errorf("%s did not learn %s: heartbeat answered %d", inst, tokName, s.status[inst])
	}
	return nil
}

func (s *rrState) registeredT0LearnedB(name string) error {
	s.mk(name, false)
	if err := s.registerAs("A", "T0", 0); err != nil {
		return err
	}
	return s.learn("B", "T0")
}

func (s *rrState) privateRegisteredT0LearnedB(name string) error {
	s.mk(name, true)
	if err := s.registerAs("A", "T0", 0); err != nil {
		return err
	}
	return s.learn("B", "T0")
}

func (s *rrState) reregisteredT1OnB() error { return s.registerAs("B", "T1", 0) }

func (s *rrState) registeredT0AThenT1B(name string) error {
	s.mk(name, false)
	if err := s.registerAs("A", "T0", 0); err != nil {
		return err
	}
	return s.registerAs("B", "T1", 0)
}

func (s *rrState) noSyncOnA() error { return nil } // marker: A runs no sync tick

func (s *rrState) syncInsideGrace() error {
	if err := s.insideGrace("A"); err != nil {
		return err
	}
	s.inst["A"].b.syncLivenessOnce()
	return nil
}

func (s *rrState) syncAfterGrace() error {
	if err := s.graceLapsedOn("A"); err != nil {
		return err
	}
	s.inst["A"].b.syncLivenessOnce()
	return nil
}

func (s *rrState) insideGrace(inst string) error {
	b := s.inst[inst].b
	b.mu.Lock()
	at, ok := b.localRegAt[s.node.id]
	b.mu.Unlock()
	if !ok || time.Since(at) >= syncLocalRegisterGrace {
		return fmt.Errorf("instance %s is not inside its register grace for the node", inst)
	}
	return nil
}

func (s *rrState) aAdoptedT1() error {
	if err := s.syncAfterGrace(); err != nil {
		return err
	}
	return s.holds("A", "T1")
}

func (s *rrState) newerThanShared(name string) error {
	s.mk(name, false)
	if err := s.registerAs("A", "T1", 0); err != nil {
		return err
	}
	if err := s.poisonSharedRegistry(name); err != nil { // strictly older (an hour) copy, token tok-STALE
		return err
	}
	s.tokens["STALE"] = "tok-STALE-" + s.nonce
	return nil
}

func (s *rrState) sharedUnreachableFromA() error {
	// A store that ACCEPTS and never answers (the slow failure, not a fast refusal): connect a
	// real client, then put a silent listener on the same address.
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		return err
	}
	addr := mr.Addr()
	vs, err := newValkeyStore("redis://" + addr)
	if err != nil {
		mr.Close()
		return err
	}
	mr.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	s.vsClose = append(s.vsClose, func() {
		_ = vs.Close()
		_ = ln.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})
	s.inst["A"].b.shared = vs
	return nil
}

func (s *rrState) registeredT0OnA(name string) error {
	s.mk(name, false)
	return s.registerAs("A", "T0", 0)
}

func (s *rrState) registeredAtS(name string) error {
	s.mk(name, false)
	return s.registerAs("A", "T0", time.Now().Unix()-10)
}

func (s *rrState) reregisteredT1OnBAtS1() error { return s.registerAs("B", "T1", time.Now().Unix()-9) }

func (s *rrState) registeredT1AtS1(name string) error {
	s.mk(name, false)
	return s.registerAs("A", "T1", time.Now().Unix()-9)
}

func (s *rrState) inFlightReadT0AtS() error {
	reg := protocol.NodeRegistration{
		NodeID: s.node.id, PubKey: s.node.pub, BridgeToken: s.tok("T0"), TS: time.Now().Unix() - 10,
		Offers: []protocol.ModelOffer{{Model: s.node.model, Ctx: 4096}},
	}
	reg.SignRegistration(s.node.priv)
	s.stale, _ = json.Marshal(reg)
	return nil
}

func (s *rrState) thatSyncApplies() error {
	s.inst["A"].b.applyRegistry(map[string][]byte{s.node.id: s.stale}, nil)
	return nil
}

func (s *rrState) olderRegisterArrives() error {
	err := s.registerAs("A", "T0", time.Now().Unix()-10)
	if err == nil {
		s.status["A"] = http.StatusOK
	} else {
		s.status["A"] = -1
	}
	return nil // the verdict is the Then: whatever the answer, the held token must not regress
}

func (s *rrState) onAirLongPastGrace(name string) error {
	s.mk(name, false)
	if err := s.registerAs("A", "Told", 0); err != nil {
		return err
	}
	if err := s.learn("B", "Told"); err != nil {
		return err
	}
	s.sweepAll()
	return s.graceLapsed()
}

func (s *rrState) restartsOnA() error {
	if err := s.registerAs("A", "Tnew", 0); err != nil {
		return err
	}
	s.node.mu.Lock()
	s.node.beats, s.node.reregs = nil, 0
	s.node.mu.Unlock()
	return nil
}

func (s *rrState) alternateWithSync() error {
	for i := 0; i < 10; i++ {
		inst := "B"
		if i%2 == 1 {
			inst = "A"
		}
		s.node.beat(s.t, s.inst[inst])
		if i%2 == 1 {
			s.sweepAll()
		}
	}
	return nil
}

func (s *rrState) bothGraced(name string) error {
	s.mk(name, false)
	if err := s.registerAs("A", "T1", 0); err != nil {
		return err
	}
	if err := s.registerAs("B", "T2", 0); err != nil {
		return err
	}
	if err := s.insideGrace("A"); err != nil {
		return err
	}
	return s.insideGrace("B")
}

func (s *rrState) liveTokenT2() error {
	s.node.mu.Lock()
	s.node.token = s.tok("T2")
	s.node.beats, s.node.reregs = nil, 0
	s.node.mu.Unlock()
	return nil
}

func (s *rrState) alternateNoGraceLapse() error {
	for i := 0; i < 10; i++ {
		inst := "A"
		if i%2 == 1 {
			inst = "B"
		}
		s.node.beat(s.t, s.inst[inst])
	}
	return nil
}

func (s *rrState) forgottenEverywhere(name string) error {
	s.mk(name, false)
	if err := s.registerAs("A", "T0", 0); err != nil {
		return err
	}
	for _, i := range s.inst {
		i.b.mu.Lock()
		delete(i.b.tunnels, s.node.id)
		delete(i.b.nodes, s.node.id)
		delete(i.b.localRegAt, s.node.id)
		i.b.mu.Unlock()
	}
	if err := s.inst["A"].b.shared.dropSharedNode(s.node.id); err != nil {
		return err
	}
	s.node.mu.Lock()
	s.node.beats, s.node.reregs = nil, 0
	s.node.mu.Unlock()
	return nil
}

// --- When -------------------------------------------------------------------------------

func (s *rrState) nodeCalls(endpoint, tokName string) error {
	return s.call(endpoint, "A", s.tok(tokName))
}

func (s *rrState) heartbeatsAWith(tokName string) error {
	return s.call("heartbeat", "A", s.tok(tokName))
}

func (s *rrState) callerHeartbeatsWith(inst, which string) error {
	var bearer string
	switch which {
	case "a forged token":
		bearer = "Bearer forged-" + s.nonce
	case "the superseded token T0":
		bearer = "Bearer " + s.tok("T0")
	case "an empty bearer":
		bearer = "Bearer "
	case "T1 with different letter case":
		bearer = "Bearer " + strings.ToUpper(s.tok("T1"))
	case "T1 with a trailing space":
		bearer = "Bearer " + s.tok("T1") + " "
	default:
		return fmt.Errorf("unknown token kind %q", which)
	}
	s.status[inst] = s.direct(inst, s.heartbeatReq(inst, s.node.id, bearer))
	return nil
}

func (s *rrState) presentsSharedCopy() error { return s.call("heartbeat", "A", s.tok("STALE")) }

func (s *rrState) guessing(n int) error {
	c := &countingShared{sharedStore: s.inst["A"].b.shared}
	s.counter = c
	s.inst["A"].b.shared = c
	answers := make([]int, n)
	var wg sync.WaitGroup
	work := make(chan int)
	start := time.Now()
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				req, _ := http.NewRequest(http.MethodPost, s.inst["A"].url()+"/nodes/heartbeat",
					strings.NewReader(`{"node_id":"`+s.node.id+`"}`))
				req.Header.Set("Authorization", "Bearer guess-"+strconv.Itoa(i)+"-"+xiNonce())
				resp, err := s.node.hc.Do(req)
				if err != nil {
					answers[i] = -1
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				answers[i] = resp.StatusCode
			}
		}()
	}
	for i := 0; i < n; i++ {
		work <- i
	}
	close(work)
	wg.Wait()
	s.elapsed = time.Since(start)
	s.answers = answers
	return nil
}

func (s *rrState) heartbeatsUnknown(name string) error {
	id := name + "-" + s.nonce
	s.status["A"] = s.direct("A", s.heartbeatReq("A", id, "Bearer tok-whatever-"+s.nonce))
	return nil
}

func (s *rrState) syncTickInsideGrace() error {
	if err := s.insideGrace("A"); err != nil {
		return err
	}
	s.inst["A"].b.syncLivenessOnce()
	return nil
}

func (s *rrState) nodeHeartbeatsA() error {
	s.status["A"] = s.node.beat(s.t, s.inst["A"]) // with the node's recover policy
	return nil
}

// --- Then -------------------------------------------------------------------------------

func (s *rrState) answered(inst string, code int) error {
	if got := s.status[inst]; got != code {
		return fmt.Errorf("instance %s answered %d, want %d", inst, got, code)
	}
	return nil
}

func (s *rrState) notBlocked() error {
	if limit := sharedOpTimeout + time.Second; s.elapsed > limit {
		return fmt.Errorf("the heartbeat took %s with the shared registry down (store timeout %s)", s.elapsed, sharedOpTimeout)
	}
	return nil
}

func (s *rrState) keepsNewer() error {
	if err := s.holds("A", "T1"); err != nil {
		return err
	}
	b := s.inst["A"].b
	b.mu.Lock()
	ts := b.nodes[s.node.id].TS
	b.mu.Unlock()
	if time.Since(time.Unix(ts, 0)) > time.Minute {
		return fmt.Errorf("A's registration regressed to TS %d", ts)
	}
	return nil
}

func (s *rrState) sharedHealed() error {
	raw, ok, err := s.inst["A"].b.shared.getNode(s.node.id)
	if err != nil || !ok {
		return fmt.Errorf("shared registry has no entry for the node (ok=%v err=%v)", ok, err)
	}
	var reg protocol.NodeRegistration
	if err := json.Unmarshal(raw, &reg); err != nil {
		return err
	}
	if reg.BridgeToken != s.tok("T1") {
		return fmt.Errorf("shared registry still holds %q, want A's newer T1", reg.BridgeToken)
	}
	return nil
}

func (s *rrState) every401() error {
	for i, c := range s.answers {
		if c != http.StatusUnauthorized {
			return fmt.Errorf("guess #%d answered %d, want 401", i, c)
		}
	}
	return nil
}

func (s *rrState) boundedReads(name string) error {
	reads := s.counter.reads.Load()
	allowed := int64(s.elapsed/tokenReadThroughInterval) + 1
	if reads > allowed {
		return fmt.Errorf("%d shared reads for %s in %s, want at most %d (one per %s)", reads, name, s.elapsed, allowed, tokenReadThroughInterval)
	}
	return nil
}

func (s *rrState) lastAnswered200() error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if n := len(s.node.beats); n == 0 || s.node.beats[n-1] != http.StatusOK {
		return fmt.Errorf("last request not answered 200 (beats %v)", s.node.beats)
	}
	return nil
}

func (s *rrState) everyInstanceHoldsT2() error {
	for name := range s.inst {
		if err := s.holds(name, "T2"); err != nil {
			return err
		}
	}
	return nil
}

func (s *rrState) reregisteredOnceSameToken() error {
	s.node.mu.Lock()
	reregs, beats := s.node.reregs, append([]int(nil), s.node.beats...)
	s.node.mu.Unlock()
	if reregs != 1 {
		return fmt.Errorf("re-registers = %d, want 1 (beats %v)", reregs, beats)
	}
	return s.holds("A", "T0") // what A took from the re-register is the node's current token
}

func (s *rrState) nextOnB200() error {
	if c := s.node.beat(s.t, s.inst["B"]); c != http.StatusOK {
		return fmt.Errorf("heartbeat on B = %d, want 200", c)
	}
	return nil
}

func rrInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		st := &rrState{xiState: &xiState{}}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			st.reset(t)
			st.tokens, st.status = map[string]string{}, map[string]int{}
			st.answers, st.counter, st.stale, st.elapsed = nil, nil, nil, 0
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			st.cleanup()
			return ctx, nil
		})

		sc.Step(`^two broker processes A and B sharing one registry and one store$`, st.twoBrokers)

		// Given
		sc.Step(`^node "([^"]*)" registered token T0 on A and B learned T0$`, st.registeredT0LearnedB)
		sc.Step(`^node "([^"]*)" registered token T0 on A as a private band and B learned T0$`, st.privateRegisteredT0LearnedB)
		sc.Step(`^the node re-registered token T1 on B$`, st.reregisteredT1OnB)
		sc.Step(`^A has not run a sync tick since$`, st.noSyncOnA)
		sc.Step(`^A ran a sync tick inside its own register grace$`, st.syncInsideGrace)
		sc.Step(`^A ran a sync tick after its register grace lapsed$`, st.syncAfterGrace)
		sc.Step(`^node "([^"]*)" registered token T0 on A and the node re-registered token T1 on B$`, st.registeredT0AThenT1B)
		sc.Step(`^A has adopted T1$`, st.aAdoptedT1)
		sc.Step(`^node "([^"]*)" holds a registration on A with a newer timestamp than the shared copy$`, st.newerThanShared)
		sc.Step(`^the shared registry is unreachable from A$`, st.sharedUnreachableFromA)
		sc.Step(`^node "([^"]*)" registered token T0 on A$`, st.registeredT0OnA)
		sc.Step(`^node "([^"]*)" registered token T0 on A at second S$`, st.registeredAtS)
		sc.Step(`^the node re-registered token T1 on B at second S\+1$`, st.reregisteredT1OnBAtS1)
		sc.Step(`^node "([^"]*)" registered token T1 on A at second S\+1$`, st.registeredT1AtS1)
		sc.Step(`^an in-flight sync on A read the shared registry when it still held T0 at second S$`, st.inFlightReadT0AtS)
		sc.Step(`^node "([^"]*)" has been on air with token Told on A and B long past every grace$`, st.onAirLongPastGrace)
		sc.Step(`^node "([^"]*)" registered token T1 on A and token T2 on B within the same 15 seconds$`, st.bothGraced)
		sc.Step(`^the share's live token is T2$`, st.liveTokenT2)
		sc.Step(`^the shared registry and both instances have forgotten node "([^"]*)"$`, st.forgottenEverywhere)

		// When
		sc.Step(`^the node calls (heartbeat|poll|result|stream) on A with token (T\d)$`, st.nodeCalls)
		sc.Step(`^the node heartbeats A with token (T\d)$`, st.heartbeatsAWith)
		sc.Step(`^a caller heartbeats ([AB]) with (a forged token|the superseded token T0|an empty bearer|T1 with different letter case|T1 with a trailing space)$`, st.callerHeartbeatsWith)
		sc.Step(`^a caller heartbeats A with token (T\d)$`, st.heartbeatsAWith)
		sc.Step(`^a caller presents the shared copy's token to A$`, st.presentsSharedCopy)
		sc.Step(`^a caller heartbeats A (\d+) times in one second with random tokens$`, st.guessing)
		sc.Step(`^a caller heartbeats A for node "([^"]*)"$`, st.heartbeatsUnknown)
		sc.Step(`^A runs one sync tick inside its register grace$`, st.syncTickInsideGrace)
		sc.Step(`^that sync applies$`, st.thatSyncApplies)
		sc.Step(`^a register for the same node with token T0 at second S arrives on A$`, st.olderRegisterArrives)
		sc.Step(`^the share restarts and registers a new token on A$`, st.restartsOnA)
		sc.Step(`^its requests alternate B, A, B, A with a sync tick after every pair, for ten requests$`, st.alternateWithSync)
		sc.Step(`^the share's requests alternate A, B for ten requests with no grace lapse$`, st.alternateNoGraceLapse)
		sc.Step(`^the node heartbeats A$`, st.nodeHeartbeatsA)

		// Then
		sc.Step(`^([AB]) answers (\d+)$`, st.answered)
		sc.Step(`^A (?:now |still )?holds token (T\d) for the node$`, func(tk string) error { return st.holds("A", tk) })
		sc.Step(`^the node is absent from A's public discovery$`, func() error { return st.absentFromDiscover("A") })
		sc.Step(`^A does not crash or block longer than the store timeout$`, st.notBlocked)
		sc.Step(`^A keeps its newer registration$`, st.keepsNewer)
		sc.Step(`^the shared registry is healed to A's newer registration$`, st.sharedHealed)
		sc.Step(`^every answer is 401$`, st.every401)
		sc.Step(`^A reads the shared registry for "([^"]*)" at most once per read-through interval$`, st.boundedReads)
		sc.Step(`^the node re-registers at most once$`, st.atMostOneReregister)
		sc.Step(`^the last request is answered 200$`, st.lastAnswered200)
		sc.Step(`^every instance holds T2$`, st.everyInstanceHoldsT2)
		sc.Step(`^the node re-registers once with its current token$`, st.reregisteredOnceSameToken)
		sc.Step(`^its next heartbeat on B answers 200$`, st.nextOnB200)
	}
}
