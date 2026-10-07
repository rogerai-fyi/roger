package agent

// cancel_test.go: the @agent scenarios of features/multinode/job_cancel.feature. The REAL agent
// loops (pollLoop + cancelLoop) run against a scripted broker, the agent package's established
// seam (as in ack_test.go); the broker side of the protocol is pinned against the real broker
// handlers by cmd/rogerai-broker/job_cancel_bdd_test.go.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

// cancelBroker is the scripted broker: it hands out queued jobs on /agent/poll, answers the
// cancel poll with queued cancels (or a fixed status), and records results and registers.
type cancelBroker struct {
	srv          *httptest.Server
	jobs         chan protocol.Job
	cancels      chan []string
	cancelStatus int // 0 = the real protocol; else every cancel poll answers this status

	mu          sync.Mutex
	results     []protocol.JobResult
	polls       int
	pollsHeader int
	cancelPolls []time.Time
	registers   int
}

func newCancelBroker() *cancelBroker {
	b := &cancelBroker{jobs: make(chan protocol.Job, 8), cancels: make(chan []string, 8)}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/poll":
			b.mu.Lock()
			b.polls++
			if r.Header.Get(cancelHeader) == "1" {
				b.pollsHeader++
			}
			b.mu.Unlock()
			select {
			case j := <-b.jobs:
				_ = json.NewEncoder(w).Encode(j)
			case <-time.After(100 * time.Millisecond):
				w.WriteHeader(http.StatusNoContent)
			case <-r.Context().Done():
			}
		case "/agent/cancels":
			b.mu.Lock()
			b.cancelPolls = append(b.cancelPolls, time.Now())
			st := b.cancelStatus
			b.mu.Unlock()
			if st != 0 {
				http.Error(w, "404 page not found", st)
				return
			}
			select {
			case ids := <-b.cancels:
				_ = json.NewEncoder(w).Encode(map[string][]string{"ids": ids})
			case <-time.After(100 * time.Millisecond):
				w.WriteHeader(http.StatusNoContent)
			case <-r.Context().Done():
			}
		case "/agent/result":
			var res protocol.JobResult
			_ = json.NewDecoder(r.Body).Decode(&res)
			b.mu.Lock()
			b.results = append(b.results, res)
			b.mu.Unlock()
		case "/nodes/register":
			b.mu.Lock()
			b.registers++
			b.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	return b
}

func (b *cancelBroker) resultFor(id string) (protocol.JobResult, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var got protocol.JobResult
	n := 0
	for _, r := range b.results {
		if r.ID == id {
			got, n = r, n+1
		}
	}
	return got, n
}

type agentCancelState struct {
	broker   *cancelBroker
	upstream *httptest.Server
	upDelay  time.Duration
	aborted  sync.Map // job id -> time the upstream saw the request aborted
	served   sync.Map // job id -> true once the upstream answered it in full
	sess     *Session
	job      string
	sentAt   time.Time
	cancelAt time.Time
	n        atomic.Int64
	oldBack  time.Duration
}

func (s *agentCancelState) reset() {
	s.oldBack = cancelBackoff
	cancelBackoff = 600 * time.Millisecond // fixture scaling of the 1 min backoff
	s.broker = newCancelBroker()
	s.upDelay = 0
	s.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Job string `json:"job"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := body.Job
		select {
		case <-time.After(s.upDelay):
			s.served.Store(id, true)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
		case <-r.Context().Done():
			s.aborted.Store(id, time.Now())
		}
	}))
}

func (s *agentCancelState) close() {
	if s.sess != nil {
		close(s.sess.stop)
		s.sess = nil
	}
	s.upstream.Close()
	s.broker.srv.Close()
	cancelBackoff = s.oldBack
}

// startAgent runs the real poll and cancel loops for one node.
func (s *agentCancelState) startAgent() {
	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{Broker: s.broker.srv.URL, Upstream: s.upstream.URL, NodeID: "n-1", Model: "qwen3-32b"}
	reg := protocol.NodeRegistration{NodeID: "n-1", BridgeToken: "tok"}
	s.sess = &Session{cfg: cfg, stop: make(chan struct{}), rereg: newReregistrar(cfg.Broker, reg, priv)}
	go pollLoop(cfg, protocol.ModelOffer{Model: cfg.Model}, priv, s.sess)
	go cancelLoop(cfg, s.sess)
}

func (s *agentCancelState) dispatch(id string) {
	s.job = id
	s.sentAt = time.Now()
	s.broker.jobs <- protocol.Job{ID: id, Body: json.RawMessage(fmt.Sprintf(`{"model":"qwen3-32b","job":%q}`, id))}
}

func eventually(d time.Duration, ok func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if ok() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ok()
}

// --- steps ------------------------------------------------------------------------------

func (s *agentCancelState) background() error { return nil } // the broker is scripted here

func (s *agentCancelState) agentWithSlowUpstream(_ string, secs int) error {
	s.upDelay = time.Duration(secs) * time.Second
	s.startAgent()
	return nil
}

func (s *agentCancelState) dispatchedAsJ() error { s.dispatch("J"); return nil }

func (s *agentCancelState) disconnectsAfter(ms int) error {
	// The consumer leaving is what makes a real broker queue the cancel; here it is queued
	// directly, `ms` after the dispatch, once the upstream is busy with the job.
	time.Sleep(time.Until(s.sentAt.Add(time.Duration(ms) * time.Millisecond)))
	s.cancelAt = time.Now()
	s.broker.cancels <- []string{s.job}
	return nil
}

func (s *agentCancelState) upstreamAbortedWithin(secs int) error {
	ok := eventually(time.Duration(secs)*time.Second, func() bool { _, a := s.aborted.Load(s.job); return a })
	if !ok {
		return fmt.Errorf("the upstream request for %s was not aborted within %ds of the cancel", s.job, secs)
	}
	return nil
}

func (s *agentCancelState) posts499NoCompletion() error {
	if !eventually(2*time.Second, func() bool { _, n := s.broker.resultFor(s.job); return n > 0 }) {
		return fmt.Errorf("no result was posted for %s", s.job)
	}
	res, n := s.broker.resultFor(s.job)
	if n != 1 || res.Status != 499 || len(res.Body) != 0 || res.Receipt.CompletionTokens != 0 {
		return fmt.Errorf("results for %s: n=%d status=%d body=%q completion=%d, want one 499 with no completion",
			s.job, n, res.Status, res.Body, res.Receipt.CompletionTokens)
	}
	return nil
}

func (s *agentCancelState) keepsServing() error {
	s.upDelay = 0
	s.dispatch("K")
	if !eventually(3*time.Second, func() bool { r, n := s.broker.resultFor("K"); return n == 1 && r.Status == 200 }) {
		r, n := s.broker.resultFor("K")
		return fmt.Errorf("the next job was not served normally: n=%d status=%d", n, r.Status)
	}
	if r, _ := s.broker.resultFor("K"); r.Receipt.CompletionTokens != 2 {
		return fmt.Errorf("the next job's receipt reports %d completion tokens, want 2", r.Receipt.CompletionTokens)
	}
	return nil
}

func (s *agentCancelState) cancelCapableAgent() error { s.startAgent(); return nil }

func (s *agentCancelState) itPolls() error {
	if !eventually(2*time.Second, func() bool { s.broker.mu.Lock(); defer s.broker.mu.Unlock(); return s.broker.polls >= 3 }) {
		return fmt.Errorf("the agent did not poll")
	}
	return nil
}

func (s *agentCancelState) pollCarriesHeader() error {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	if s.broker.pollsHeader != s.broker.polls || s.broker.polls == 0 {
		return fmt.Errorf("%d of %d polls carried X-Roger-Cancel: 1", s.broker.pollsHeader, s.broker.polls)
	}
	return nil
}

func (s *agentCancelState) brokerWithout() error {
	s.broker.cancelStatus = http.StatusNotFound
	s.startAgent()
	return nil
}

func (s *agentCancelState) opensCancelPoll() error {
	if !eventually(2*time.Second, func() bool { s.broker.mu.Lock(); defer s.broker.mu.Unlock(); return len(s.broker.cancelPolls) >= 1 }) {
		return fmt.Errorf("the agent never opened its cancel poll")
	}
	return nil
}

func (s *agentCancelState) backsOff() error {
	time.Sleep(cancelBackoff + 400*time.Millisecond)
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	cp := s.broker.cancelPolls
	if len(cp) < 2 {
		return fmt.Errorf("the agent never tried its cancel poll again (%d attempts)", len(cp))
	}
	if gap := cp[1].Sub(cp[0]); gap < cancelBackoff {
		return fmt.Errorf("the agent retried after %v, want a back-off of at least %v", gap, cancelBackoff)
	}
	return nil
}

func (s *agentCancelState) noReregister() error {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	if s.broker.registers != 0 {
		return fmt.Errorf("the agent re-registered %d times because of the 404", s.broker.registers)
	}
	return nil
}

func (s *agentCancelState) servesNormally() error { return s.keepsServing() }

func (s *agentCancelState) finishedJ() error {
	s.startAgent()
	s.dispatch("J")
	if !eventually(3*time.Second, func() bool { _, n := s.broker.resultFor("J"); return n == 1 }) {
		return fmt.Errorf("the agent did not finish J")
	}
	return nil
}

func (s *agentCancelState) cancelArrives() error {
	s.broker.cancels <- []string{s.job}
	// The cancel is consumed by the agent's held poll.
	if !eventually(2*time.Second, func() bool { return len(s.broker.cancels) == 0 }) {
		return fmt.Errorf("the agent never collected the cancel")
	}
	time.Sleep(300 * time.Millisecond)
	return nil
}

func (s *agentCancelState) nothingAborted() error {
	if _, a := s.aborted.Load(s.job); a {
		return fmt.Errorf("an upstream request was aborted for the finished job")
	}
	if r, n := s.broker.resultFor(s.job); n != 1 || r.Status != 200 {
		return fmt.Errorf("results for %s: n=%d last status=%d, want exactly the one 200", s.job, n, r.Status)
	}
	return nil
}

func TestJobCancelAgentBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &agentCancelState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.close()
				return ctx, nil
			})
			sc.Step(`^a multi-instance broker of two instances sharing one store$`, st.background)
			sc.Step(`^node "n-1" is on air for "qwen3-32b"$`, st.background)
			sc.Step(`^a cancel-capable agent serving "([^"]+)" from an upstream that takes (\d+) seconds$`, st.agentWithSlowUpstream)
			sc.Step(`^"alice"'s non-stream request is dispatched to it as job J$`, st.dispatchedAsJ)
			sc.Step(`^"alice" disconnects after (\d+) ms$`, st.disconnectsAfter)
			sc.Step(`^the agent's upstream request for J is aborted within (\d+) seconds$`, st.upstreamAbortedWithin)
			sc.Step(`^the agent posts a result for J with status 499 and no completion$`, st.posts499NoCompletion)
			sc.Step(`^the agent keeps serving the next job normally$`, st.keepsServing)
			sc.Step(`^a cancel-capable agent$`, st.cancelCapableAgent)
			sc.Step(`^it polls the broker$`, st.itPolls)
			sc.Step(`^the poll carries X-Roger-Cancel "1"$`, st.pollCarriesHeader)
			sc.Step(`^an agent connected to a broker that answers 404 on /agent/cancels$`, st.brokerWithout)
			sc.Step(`^the agent opens its cancel poll$`, st.opensCancelPoll)
			sc.Step(`^it backs off before trying again$`, st.backsOff)
			sc.Step(`^it does not re-register$`, st.noReregister)
			sc.Step(`^it serves jobs normally$`, st.servesNormally)
			sc.Step(`^a cancel-capable agent that finished job J$`, st.finishedJ)
			sc.Step(`^a cancel for J arrives$`, st.cancelArrives)
			sc.Step(`^nothing is aborted and no extra result is posted$`, st.nothingAborted)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/job_cancel.feature"},
			Tags:     "@agent",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/job_cancel @agent scenarios failed (see godog output above)")
	}
}
