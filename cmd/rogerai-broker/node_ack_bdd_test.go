package main

// node_ack_bdd_test.go makes features/multinode/node_ack.feature EXECUTABLE with the REAL node
// agent (internal/agent) against two REAL broker instances (full route table over HTTP, one
// shared store, real miniredis). Between them sits a small round-robin proxy standing in for
// the load balancer; it can delay or lose an ack, lose a job on its way to the node, or answer
// /agent/ack with 404 the way an older broker does. The upstream model is an HTTP stub that
// counts how often each job is served. No mocks of broker or node.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"rogerai.fm/roger/v6/internal/bddtest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
)

// ackProxy is the load balancer in front of the brokers.
type ackProxy struct {
	targets map[string]*url.URL
	rr      atomic.Int64

	mu        sync.Mutex
	pollsTo   string        // "" = round robin
	acksTo    string        // "" = round robin
	ackDelay  time.Duration // delay every ack
	dropAcks  int           // lose this many acks (-1 = all)
	dropJobs  int           // lose this many job deliveries (-1 = all)
	noAck     bool          // answer /agent/ack 404, like a broker without acks
	registers int
	acks      []int // status of each ack answered
	results   int
	jobsLost  int
	delivered int // job deliveries that reached the node
}

func (p *ackProxy) pick(path string) *url.URL {
	p.mu.Lock()
	to := ""
	switch path {
	case "/agent/poll":
		to = p.pollsTo
	case "/agent/ack":
		to = p.acksTo
	}
	p.mu.Unlock()
	if to != "" {
		return p.targets[to]
	}
	if len(p.targets) == 1 {
		for _, u := range p.targets {
			return u
		}
	}
	if p.rr.Add(1)%2 == 0 {
		return p.targets["A"]
	}
	return p.targets["B"]
}

func (p *ackProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	switch r.URL.Path {
	case "/nodes/register":
		p.registers++
	case "/agent/result":
		p.results++
	}
	isAck := r.URL.Path == "/agent/ack"
	delay, noAck := p.ackDelay, p.noAck
	lose := false
	if isAck && !noAck && p.dropAcks != 0 {
		lose = true
		if p.dropAcks > 0 {
			p.dropAcks--
		}
	}
	p.mu.Unlock()
	if isAck {
		if delay > 0 {
			time.Sleep(delay)
		}
		status := 0
		switch {
		case noAck:
			status = http.StatusNotFound
			http.Error(w, `{"error":"not found"}`, status)
		case lose:
			status = http.StatusBadGateway
			http.Error(w, "lost", status)
		}
		if status != 0 {
			p.mu.Lock()
			p.acks = append(p.acks, status)
			p.mu.Unlock()
			return
		}
	}
	target := p.pick(r.URL.Path)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) { pr.SetURL(target) },
		ModifyResponse: func(resp *http.Response) error {
			if isAck {
				p.mu.Lock()
				p.acks = append(p.acks, resp.StatusCode)
				p.mu.Unlock()
			}
			if r.URL.Path != "/agent/poll" || resp.StatusCode != http.StatusOK {
				return nil
			}
			p.mu.Lock()
			drop := p.dropJobs != 0
			if drop {
				p.jobsLost++
				if p.dropJobs > 0 {
					p.dropJobs--
				}
			}
			if !drop {
				p.delivered++
			}
			p.mu.Unlock()
			if drop { // the job is lost on its way: the node sees a plain re-poll
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				resp.StatusCode, resp.Status = http.StatusNoContent, "204 No Content"
				resp.Body = io.NopCloser(strings.NewReader(""))
				resp.ContentLength = 0
				resp.Header.Del("Content-Length")
			}
			return nil
		},
		FlushInterval: -1,
	}
	rp.ServeHTTP(w, r)
}

type ackState struct {
	xi       *xiState
	proxy    *ackProxy
	lb       *httptest.Server
	upstream *httptest.Server
	sess     *agent.Session
	nodeID   string

	mu     sync.Mutex
	served map[string]int // job id -> upstream calls
	slow   time.Duration  // upstream serve time

	tk        *dispatchTicket
	tkErr     error
	jobID     string
	oldPolled chan protocol.Job
	status    int
	prevGrace time.Duration
}

func (s *ackState) inst(name string) *broker { return s.xi.inst[name].b }

func (s *ackState) upstreamStub() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := ""
		if len(body.Messages) > 0 {
			id = body.Messages[0].Content
		}
		s.mu.Lock()
		s.served[id]++
		slow := s.slow
		s.mu.Unlock()
		time.Sleep(slow)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok %s"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`, id)
	}))
}

func (s *ackState) servedCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served[id]
}

func (s *ackState) twoInstances() error {
	s.xi.db = xiStore(s.xi.t)
	s.xi.redisURL = xiRedisURL(s.xi.t)
	for _, n := range []string{"A", "B"} {
		s.xi.newInstance(n, true).b.dispatchMode = dispatchViaQueueOnly
	}
	s.proxy = &ackProxy{targets: map[string]*url.URL{}}
	for n, i := range s.xi.inst {
		u, _ := url.Parse(i.url())
		s.proxy.targets[n] = u
	}
	s.lb = httptest.NewServer(s.proxy)
	return nil
}

// startNode starts the real node agent behind the load balancer.
func (s *ackState) startNode(parallel int) error {
	s.xi.t.Setenv("XDG_CONFIG_HOME", s.xi.t.TempDir()) // the node key lives here
	s.nodeID = "kokoro-" + xiNonce()
	sess, err := agent.Start(agent.Config{
		NodeID: s.nodeID, Broker: s.lb.URL, Upstream: s.upstream.URL, Model: "free-m",
		Modality: "chat", Ctx: 4096, Parallel: parallel,
	})
	if err != nil {
		return err
	}
	s.sess = sess
	// Both instances know it's live (heartbeats and polls land on either).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range []string{"A", "B"} {
			if b := s.xi.inst[n]; b != nil {
				b.b.syncLivenessOnce()
			}
		}
		if s.live("A") && s.live("B") && s.idle() >= 2 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the node never came on air on both instances (idle polls %d)", s.idle())
}

func (s *ackState) live(inst string) bool {
	b := s.xi.inst[inst]
	return b != nil && b.b.nodeLive(s.nodeID)
}

func (s *ackState) idle() int {
	n := 0
	for _, i := range s.xi.inst {
		n += localIdle(i.b, s.nodeID)
	}
	return n
}

func (s *ackState) ackNode() error { return s.startNode(4) }

// dispatch sends one job from inst; the job's content is its id (the upstream counts it).
func (s *ackState) dispatch(inst, node string) (*dispatchTicket, string, error) {
	id := "ack-" + xiNonce()
	body := fmt.Sprintf(`{"model":"free-m","messages":[{"role":"user","content":%q}]}`, id)
	tk, err := s.inst(inst).dispatchRemote(context.Background(), node, protocol.Job{ID: id, Body: []byte(body)}, false)
	return tk, id, err
}

// ---- A1 -------------------------------------------------------------------------------------

func (s *ackState) delayAck() error {
	s.proxy.mu.Lock()
	s.proxy.ackDelay = time.Second
	s.proxy.mu.Unlock()
	return nil
}

func (s *ackState) dispatchFrom(inst string) error {
	var err error
	s.tk, s.jobID, err = s.dispatch(inst, s.nodeID)
	return err
}

func (s *ackState) notTakenBeforeAck() error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.proxy.mu.Lock()
		got := s.proxy.delivered
		s.proxy.mu.Unlock()
		if got > 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the node never received the job")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // well inside the 1 s the ack is held back
	if s.tk.isTaken() {
		return fmt.Errorf("the origin saw the job taken before the (delayed) ack")
	}
	return nil
}

func (s *ackState) takenAfterAck() error {
	select {
	case <-s.tk.taken:
		return nil
	case <-time.After(3 * time.Second):
		return fmt.Errorf("the origin never saw the job taken after the ack")
	}
}

func (s *ackState) pollsAckSplit() error {
	s.proxy.mu.Lock()
	s.proxy.pollsTo, s.proxy.acksTo = "A", "B"
	s.proxy.mu.Unlock()
	return s.waitIdleOn("A", 1)
}

func (s *ackState) waitIdleOn(inst string, n int) error {
	deadline := time.Now().Add(30 * time.Second) // polls already held elsewhere re-poll within the hold
	for localIdle(s.inst(inst), s.nodeID) < n {
		if time.Now().After(deadline) {
			return fmt.Errorf("no poll reached instance %s", inst)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (s *ackState) originSeesTaken() error { return s.takenAfterAck() }

// ---- A2 -------------------------------------------------------------------------------------

func (s *ackState) nextJobLost() error {
	s.proxy.mu.Lock()
	s.proxy.dropJobs = 1
	s.proxy.mu.Unlock()
	return nil
}

func (s *ackState) everyJobLost() error {
	s.proxy.mu.Lock()
	s.proxy.dropJobs = -1
	s.proxy.mu.Unlock()
	return nil
}

func (s *ackState) dispatchAny() error { return s.dispatchFrom("A") }

func (s *ackState) deliveredAgain() error {
	deadline := time.Now().Add(10 * time.Second)
	for s.inst("A").stats.dqRedeliver.Load() == 0 {
		if time.Now().After(deadline) {
			st := func(n string) string {
				b := s.inst(n)
				return fmt.Sprintf("%s: handoff=%d local=%d redeliver=%d lost=%d busy=%d", n, b.stats.dqHandoff.Load(),
					b.stats.localDispatch.Load(), b.stats.dqRedeliver.Load(), b.stats.dqLost.Load(), b.stats.dqBusy.Load())
			}
			s.proxy.mu.Lock()
			jl := s.proxy.jobsLost
			s.proxy.mu.Unlock()
			return fmt.Errorf("the lost job was never delivered again (%s; %s; jobs lost %d; taken %v; done %v)",
				st("A"), st("B"), jl, s.tk.isTaken(), s.tk.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (s *ackState) servedOnce() error {
	if _, err := s.tk.awaitResult(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("no result: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if c := s.servedCount(s.jobID); c != 1 {
		return fmt.Errorf("the job was served %d times, want exactly 1", c)
	}
	return nil
}

func (s *ackState) consumerGetsResult() error {
	select {
	case <-s.tk.done:
		return fmt.Errorf("the consumer got %v instead of the result", s.tk.Err())
	default:
		return nil // servedOnce already received the result
	}
}

func (s *ackState) atMost3() error {
	select {
	case <-s.tk.done:
	case <-time.After(20 * time.Second):
		return fmt.Errorf("the never-acked job was never failed")
	}
	s.proxy.mu.Lock()
	lost := s.proxy.jobsLost
	s.proxy.mu.Unlock()
	if lost < 1 || lost > dqMaxDeliveries {
		return fmt.Errorf("the job was delivered %d times, want between 1 and %d", lost, dqMaxDeliveries)
	}
	return nil
}

func (s *ackState) retryable503() error {
	if s.tk.Err() != errHandoffLost {
		return fmt.Errorf("the job failed with %v, want a handoff failure", s.tk.Err())
	}
	w := httptest.NewRecorder()
	s.inst("A").writeDispatchFailure(w, s.inst("A").dispatchErrOutcome(s.tk.Err()))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		return fmt.Errorf("the consumer answer is %d (Retry-After %q), want a retryable 503", w.Code, w.Header().Get("Retry-After"))
	}
	return nil
}

func (s *ackState) noReceipt() error {
	if c := s.servedCount(s.jobID); c != 0 {
		return fmt.Errorf("a job the node never received was served %d times", c)
	}
	select {
	case <-s.tk.res:
		return fmt.Errorf("a result arrived for a job never received")
	default:
		return nil
	}
}

// ---- A3 -------------------------------------------------------------------------------------

func (s *ackState) ackLost() error {
	s.proxy.mu.Lock()
	s.proxy.dropAcks = 2 // the ack and the node's one retry
	s.proxy.mu.Unlock()
	s.mu.Lock()
	s.slow = 3 * dqTakenGrace.get() // a long generation: still serving when the job is re-delivered
	s.mu.Unlock()
	var err error
	s.tk, s.jobID, err = s.dispatch("A", s.nodeID)
	return err
}

func (s *ackState) deliveredAgainAfterLostAck() error { return s.deliveredAgain() }

func (s *ackState) ackedWithoutServing() error {
	select {
	case <-s.tk.taken:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("the re-delivered job was never acked")
	}
	return nil
}

func (s *ackState) oneResultOneReceipt() error {
	if _, err := s.tk.awaitResult(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("no result: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if c := s.servedCount(s.jobID); c != 1 {
		return fmt.Errorf("the job was served %d times, want exactly 1", c)
	}
	s.proxy.mu.Lock()
	results := s.proxy.results
	s.proxy.mu.Unlock()
	if results != 1 {
		return fmt.Errorf("%d results were posted, want exactly 1", results)
	}
	return nil
}

// ---- A4 -------------------------------------------------------------------------------------

func (s *ackState) oldNode(name string) error {
	pub := "00"
	for _, i := range s.xi.inst {
		miRegisterNode(i.b, name, pub, "old-tok", []protocol.ModelOffer{{Model: "free-m"}})
	}
	s.oldPolled = make(chan protocol.Job, 1)
	go func() {
		r := httptest.NewRequest(http.MethodGet, "/agent/poll?node="+name, nil)
		r.Header.Set("Authorization", "Bearer old-tok") // no ack header: an old node
		w := httptest.NewRecorder()
		s.inst("A").agentPoll(w, r)
		var job protocol.Job
		if json.Unmarshal(w.Body.Bytes(), &job) == nil && job.ID != "" {
			s.oldPolled <- job
		}
	}()
	return s.waitIdleOnNode("A", name)
}

func (s *ackState) waitIdleOnNode(inst, node string) error {
	deadline := time.Now().Add(3 * time.Second)
	for localIdle(s.inst(inst), node) < 1 {
		if time.Now().After(deadline) {
			return fmt.Errorf("the old node's poll never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func (s *ackState) dispatchToOld(name string) error {
	var err error
	s.tk, s.jobID, err = s.dispatch("B", name)
	return err
}

func (s *ackState) takenOnWrite() error {
	select {
	case <-s.oldPolled:
	case <-time.After(3 * time.Second):
		return fmt.Errorf("the old node never received the job")
	}
	select {
	case <-s.tk.taken:
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("the origin did not see the job taken on write for a node without acks")
	}
}

func (s *ackState) noAckExpected() error {
	if n := s.inst("B").stats.dqRedeliver.Load(); n != 0 {
		return fmt.Errorf("the origin re-delivered a job to a node without acks")
	}
	return nil
}

func (s *ackState) brokerWithoutAck() error {
	s.proxy.mu.Lock()
	s.proxy.noAck = true
	s.proxy.mu.Unlock()
	return nil
}

func (s *ackState) newNodePollsAndAcks() error {
	var err error
	s.tk, s.jobID, err = s.dispatch("A", s.nodeID)
	return err
}

func (s *ackState) ack404Ignored() error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.proxy.mu.Lock()
		acks := append([]int(nil), s.proxy.acks...)
		s.proxy.mu.Unlock()
		if len(acks) > 0 {
			for _, c := range acks {
				if c != http.StatusNotFound {
					return fmt.Errorf("an ack was answered %d, want 404 from a broker without acks", c)
				}
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the node never posted an ack")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *ackState) servesAndPosts() error { return s.servedOnce() }

func (s *ackState) noReregister() error {
	s.proxy.mu.Lock()
	n := s.proxy.registers
	s.proxy.mu.Unlock()
	if n != 1 {
		return fmt.Errorf("the node registered %d times, want only its first registration", n)
	}
	return nil
}

func (s *ackState) singleInstance() error {
	s.xi.db = xiStore(s.xi.t)
	s.xi.redisURL = xiRedisURL(s.xi.t)
	b := s.xi.newInstance("S", false).b
	b.shared = nil // single-instance: no shared store at all
	u, _ := url.Parse(s.xi.inst["S"].url())
	s.proxy = &ackProxy{targets: map[string]*url.URL{"S": u}}
	s.lb = httptest.NewServer(s.proxy)
	return nil
}

func (s *ackState) singleNodeAcks() error {
	s.xi.t.Setenv("XDG_CONFIG_HOME", s.xi.t.TempDir())
	s.nodeID = "solo-" + xiNonce()
	sess, err := agent.Start(agent.Config{NodeID: s.nodeID, Broker: s.lb.URL, Upstream: s.upstream.URL,
		Model: "free-m", Modality: "chat", Ctx: 4096, Parallel: 2})
	if err != nil {
		return err
	}
	s.sess = sess
	b := s.xi.inst["S"].b
	b.mu.Lock()
	t := b.tunnels[s.nodeID]
	b.mu.Unlock()
	if t == nil {
		return fmt.Errorf("the node did not register")
	}
	s.jobID = "solo-job-" + xiNonce()
	body := fmt.Sprintf(`{"model":"free-m","messages":[{"role":"user","content":%q}]}`, s.jobID)
	resCh, unreg := t.await(s.jobID)
	defer unreg()
	res, _, outcome := b.dispatchAwait(context.Background(), t, s.nodeID, protocol.Job{ID: s.jobID, Body: []byte(body)}, resCh, time.Now().Add(10*time.Second))
	if outcome != dispatchResult || res.Status != 200 {
		return fmt.Errorf("single-instance dispatch = %v status %d", outcome, res.Status)
	}
	return nil
}

func (s *ackState) ack200() error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.proxy.mu.Lock()
		acks := append([]int(nil), s.proxy.acks...)
		s.proxy.mu.Unlock()
		if len(acks) > 0 {
			if acks[0] != http.StatusOK {
				return fmt.Errorf("the ack was answered %d, want 200", acks[0])
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the node never acked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *ackState) singleServedOnce() error {
	time.Sleep(300 * time.Millisecond)
	if c := s.servedCount(s.jobID); c != 1 {
		return fmt.Errorf("the job was served %d times, want exactly 1", c)
	}
	return nil
}

// ---- A5 -------------------------------------------------------------------------------------

func (s *ackState) ackOutline(job, node, token string, status int, effect string) error {
	// No job may reach the node's polls here: the table's acks are the only acks.
	s.proxy.mu.Lock()
	s.proxy.dropJobs = -1
	s.proxy.mu.Unlock()
	for _, i := range s.xi.inst {
		i.b.mu.Lock()
		if _, ok := i.b.nodes["other"]; !ok {
			miRegisterNode(i.b, "other", "00", "other-tok", []protocol.ModelOffer{{Model: "free-m"}})
		}
		i.b.mu.Unlock()
	}
	s.inst("A").mu.Lock()
	kTok := s.inst("A").tunnels[s.nodeID].token
	s.inst("A").mu.Unlock()

	var tk *dispatchTicket
	jobID := "never-" + xiNonce()
	switch job {
	case `a job sent to "kokoro"`:
		t, id, err := s.dispatch("A", s.nodeID)
		if err != nil {
			return err
		}
		tk, jobID = t, id
	case `a job sent to "other"`:
		t, id, err := s.dispatch("A", "other")
		if err != nil {
			return err
		}
		tk, jobID = t, id
	}
	if tk != nil {
		defer tk.close()
	}
	ackNode := map[string]string{`"unknown"`: "unknown", `"kokoro"`: s.nodeID}[node]
	ackTok := map[string]string{"any token": "x", "a wrong token": "wrong", "kokoro's token": kTok}[token]
	req, _ := http.NewRequest(http.MethodPost, s.xi.inst["B"].url()+"/agent/ack?node="+url.QueryEscape(ackNode)+"&job="+url.QueryEscape(jobID), nil)
	req.Header.Set("Authorization", "Bearer "+ackTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != status {
		return fmt.Errorf("the ack was answered %d, want %d", resp.StatusCode, status)
	}
	time.Sleep(300 * time.Millisecond) // the ack's notice reaches the origin
	switch effect {
	case "the job is not marked taken", "the other job is not marked taken":
		if tk.isTaken() {
			return fmt.Errorf("the ack marked a job taken that it must not")
		}
	case "the job is marked taken":
		if !tk.isTaken() {
			return fmt.Errorf("a valid ack did not mark the job taken")
		}
	case "nothing changes":
	default:
		return fmt.Errorf("unknown effect %q", effect)
	}
	return nil
}

func ackInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		s := &ackState{xi: &xiState{}}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			s.xi.reset(t)
			s.served, s.slow = map[string]int{}, 0
			s.sess, s.tk, s.proxy = nil, nil, nil
			s.upstream = s.upstreamStub()
			s.prevGrace = dqTakenGrace.get()
			dqTakenGrace.set(2 * time.Second) // the ack normally lands in milliseconds
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			if s.tk != nil {
				s.tk.close()
			}
			if s.sess != nil {
				s.sess.Stop()
			}
			if s.lb != nil {
				s.lb.CloseClientConnections()
				s.lb.Close()
			}
			s.upstream.Close()
			for _, i := range s.xi.inst {
				i.srv.CloseClientConnections()
			}
			s.xi.cleanup()
			dqTakenGrace.set(s.prevGrace)
			return ctx, nil
		})
		sc.Step(`^a multi-instance broker of two instances sharing one store$`, s.twoInstances)
		sc.Step(`^an ack-capable node "kokoro" with 4 pollers split 2 and 2 across the instances$`, s.ackNode)
		sc.Step(`^the node delays its ack by 1 s$`, s.delayAck)
		sc.Step(`^a job is dispatched to "kokoro" from instance (\w)$`, s.dispatchFrom)
		sc.Step(`^the origin does not see the job taken before the ack$`, s.notTakenBeforeAck)
		sc.Step(`^it sees it taken right after the ack$`, s.takenAfterAck)
		sc.Step(`^the node's polls are on instance A and its acks go to instance B$`, s.pollsAckSplit)
		sc.Step(`^the origin sees the job taken$`, s.originSeesTaken)
		sc.Step(`^the next job written to one of the node's polls is lost on the way$`, s.nextJobLost)
		sc.Step(`^every job written to the node's polls is lost on the way$`, s.everyJobLost)
		sc.Step(`^a job is dispatched to "kokoro"$`, s.dispatchAny)
		sc.Step(`^the job is delivered again to another poll$`, s.deliveredAgain)
		sc.Step(`^it is served exactly once$`, s.servedOnce)
		sc.Step(`^the consumer gets its result, not a 503$`, s.consumerGetsResult)
		sc.Step(`^it is delivered at most 3 times$`, s.atMost3)
		sc.Step(`^the consumer gets a retryable 503$`, s.retryable503)
		sc.Step(`^no receipt settles for it$`, s.noReceipt)
		sc.Step(`^the node receives a job and its ack is lost$`, s.ackLost)
		sc.Step(`^the job is delivered again$`, s.deliveredAgainAfterLostAck)
		sc.Step(`^the node acks it again without serving it$`, s.ackedWithoutServing)
		sc.Step(`^exactly one result and one receipt exist for it$`, s.oneResultOneReceipt)
		sc.Step(`^a node "([^"]*)" that polls without the ack header$`, s.oldNode)
		sc.Step(`^a job is dispatched to "(old)"$`, s.dispatchToOld)
		sc.Step(`^the origin sees the job taken as soon as it is written to the poll$`, s.takenOnWrite)
		sc.Step(`^no ack is expected from "old"$`, s.noAckExpected)
		sc.Step(`^a broker that has no /agent/ack$`, s.brokerWithoutAck)
		sc.Step(`^a new node polls, receives a job and posts its ack$`, s.newNodePollsAndAcks)
		sc.Step(`^the ack is answered 404 and ignored$`, s.ack404Ignored)
		sc.Step(`^the node serves the job and posts its result$`, s.servesAndPosts)
		sc.Step(`^the node does not re-register$`, s.noReregister)
		sc.Step(`^the broker runs in single-instance mode$`, s.singleInstance)
		sc.Step(`^an ack-capable node receives a job and acks it$`, s.singleNodeAcks)
		sc.Step(`^the ack is answered 200$`, s.ack200)
		sc.Step(`^the job is served exactly once$`, s.singleServedOnce)
		sc.Step(`^an ack for (a job sent to "kokoro"|a job sent to "other"|an id never dispatched) arrives from ("unknown"|"kokoro") with (any token|a wrong token|kokoro's token)$`,
			func(job, node, token string) error { s.jobID, s.status = job+"\x00"+node+"\x00"+token, 0; return nil })
		sc.Step(`^it is answered (\d+)$`, func(status int) error { s.status = status; return nil })
		sc.Step(`^(the job is not marked taken|the other job is not marked taken|nothing changes|the job is marked taken)$`, func(effect string) error {
			parts := strings.SplitN(s.jobID, "\x00", 3)
			return s.ackOutline(parts[0], parts[1], parts[2], s.status, effect)
		})
	}
}

func TestNodeAckBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: ackInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/node_ack.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("multinode/node_ack scenarios failed (see godog output above)")
	}
}
