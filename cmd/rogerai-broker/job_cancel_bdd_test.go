package main

// job_cancel_bdd_test.go makes features/multinode/job_cancel.feature EXECUTABLE (the scenarios not
// tagged @agent; those run in internal/agent/cancel_test.go). Two REAL broker instances share one
// store (xiStore + xiRedisURL, queue-only dispatch as in production); nodes are raw pollers that
// call the instances' real /agent/poll, /agent/stream, /agent/result and /agent/cancels handlers
// with or without X-Roger-Cancel, which is what lets a scenario be an old node or a new one
// deterministically. Consumers relay through the instances' real relay(). The end-to-end path with
// the REAL node agent is TestJobCancelEndToEndWithTheRealAgent below.
//
// Fixture scaling (production defaults in jobcancel.go): the capability lifetime and the buffer
// lifetime are shortened so "3 minutes" / "2 minutes" run in a fraction of a second, and the
// cancel/job poll holds are short.
//
// The money scenario's station is free (no GitHub-linked owner in this harness), so "billed $0"
// and "earns nothing" are checked against the real ledger but cannot fail on price; priced
// billing of a non-stream disconnect is pinned by routing_money_hardening.feature.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

type jcNode struct {
	id, token, model string
	capable          bool
}

type jcState struct {
	ack   *ackState
	nodes map[string]*jcNode

	consumer ed25519.PrivateKey

	cancelRelay context.CancelFunc
	relayDone   chan struct{}
	relayRW     *recWriter
	jobID       string
	gotJob      chan protocol.Job
	streamPW    *io.PipeWriter

	heldPoll chan jcPoll
	status   int

	prevCap, prevBuf, prevHold time.Duration
}

type jcPoll struct {
	code int
	ids  []string
}

func (s *jcState) reset(t *testing.T) {
	s.ack = &ackState{xi: &xiState{}, served: map[string]int{}}
	s.ack.xi.reset(t)
	s.nodes = map[string]*jcNode{}
	_, s.consumer, _ = ed25519.GenerateKey(nil)
	s.cancelRelay, s.relayDone, s.relayRW, s.jobID, s.gotJob, s.streamPW = nil, nil, nil, "", nil, nil
	s.heldPoll, s.status = nil, 0
	s.prevCap, s.prevBuf, s.prevHold = cancelCapTTL, cancelBufTTL, cancelPollHold
	cancelCapTTL, cancelBufTTL, cancelPollHold = 400*time.Millisecond, 400*time.Millisecond, 300*time.Millisecond
	agentPollHold = 300 * time.Millisecond
}

func (s *jcState) cleanup() {
	if s.cancelRelay != nil {
		s.cancelRelay()
	}
	if s.streamPW != nil {
		_ = s.streamPW.Close()
	}
	if s.relayDone != nil {
		select {
		case <-s.relayDone:
		case <-time.After(5 * time.Second):
		}
	}
	s.ack.xi.cleanup()
	cancelCapTTL, cancelBufTTL, cancelPollHold = s.prevCap, s.prevBuf, s.prevHold
}

func (s *jcState) inst(n string) *broker { return s.ack.inst(n) }

func (s *jcState) twoInstances() error { return s.ack.twoInstances() }

func (s *jcState) onAir(name, model string) error {
	n := &jcNode{id: name + "-" + xiNonce(), token: "tok-" + xiNonce(), model: model}
	for _, i := range s.ack.xi.inst {
		miRegisterNode(i.b, n.id, "00", n.token, []protocol.ModelOffer{{Model: model}})
	}
	s.nodes[name] = n
	return nil
}

func (s *jcState) node(name string) *jcNode {
	n, ok := s.nodes[name]
	if !ok {
		_ = s.onAir(name, "qwen3-32b")
		n = s.nodes[name]
	}
	return n
}

// pollOnce is one real /agent/poll on inst; it returns the job it was handed (if any).
func (s *jcState) pollOnce(inst string, n *jcNode, header bool) protocol.Job {
	r := httptest.NewRequest(http.MethodGet, "/agent/poll?node="+n.id, nil)
	r.Header.Set("Authorization", "Bearer "+n.token)
	if header {
		r.Header.Set(cancelHeader, "1")
	}
	w := httptest.NewRecorder()
	s.inst(inst).agentPoll(w, r)
	var job protocol.Job
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	return job
}

func (s *jcState) cancelPoll(inst string, n *jcNode, token string) jcPoll {
	r := httptest.NewRequest(http.MethodGet, "/agent/cancels?node="+n.id, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.inst(inst).agentCancels(w, r)
	var out struct {
		IDs []string `json:"ids"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return jcPoll{code: w.Code, ids: out.IDs}
}

// ---- negotiation ----------------------------------------------------------------------------

func (s *jcState) pollsWithHeader(name string) error {
	n := s.node(name)
	n.capable = true
	s.pollOnce("A", n, true)
	return nil
}

func (s *jcState) pollsWithoutHeader(name string) error {
	s.node(name).capable = false
	return nil
}

func (s *jcState) capableEverywhere(name string) error {
	n := s.node(name)
	for _, i := range []string{"A", "B"} {
		if !s.inst(i).cancelCapable(n.id) {
			return fmt.Errorf("%s is not cancel-capable on instance %s after a poll with %s: 1", name, i, cancelHeader)
		}
	}
	return nil
}

func (s *jcState) ageOut(_ int, name string) error {
	time.Sleep(cancelCapTTL + 200*time.Millisecond)
	s.pollOnce("B", s.node(name), false)
	return nil
}

func (s *jcState) notCapable(name string) error {
	n := s.node(name)
	for _, i := range []string{"A", "B"} {
		if s.inst(i).cancelCapable(n.id) {
			return fmt.Errorf("%s is still cancel-capable on instance %s after it stopped advertising", name, i)
		}
	}
	return nil
}

func (s *jcState) isCapable(name string) error { return s.pollsWithHeader(name) }

// ---- relays ---------------------------------------------------------------------------------

func (s *jcState) startRelay(inst, name string, stream bool) error {
	n := s.node(name)
	s.gotJob = make(chan protocol.Job, 1)
	go func() { // the node's poll on the same instance (the queue routes the job to it)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if job := s.pollOnce(inst, n, n.capable); job.ID != "" {
				s.gotJob <- job
				return
			}
		}
	}()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stream":%t}`, n.model, stream)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelRelay = cancel
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	signReq(r, s.consumer, []byte(body))
	s.relayRW = &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.relayDone = make(chan struct{})
	go func() {
		defer close(s.relayDone)
		s.inst(inst).relay(s.relayRW, r)
	}()
	select {
	case job := <-s.gotJob:
		s.jobID = job.ID
		if stream {
			pr, pw := io.Pipe()
			s.streamPW = pw
			go func() {
				sr := httptest.NewRequest(http.MethodPost, "/agent/stream?node="+n.id+"&job="+job.ID, pr)
				sr.Header.Set("Authorization", "Bearer "+n.token)
				s.inst(inst).agentStream(httptest.NewRecorder(), sr)
			}()
			_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n"))
		}
		return nil
	case <-time.After(6 * time.Second):
		return fmt.Errorf("the job never reached %s's poll (relay status %d: %s)", name, s.relayRW.Code, s.relayRW.Body.String())
	}
}

func (s *jcState) nonStreamDispatched(_, name string) error { return s.startRelay("A", name, false) }
func (s *jcState) streamDispatched(_, name string) error    { return s.startRelay("A", name, true) }
func (s *jcState) relayedByA(_, name string) error          { return s.startRelay("A", name, false) }

func (s *jcState) disconnect() error {
	s.cancelRelay()
	select {
	case <-s.relayDone:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the relay did not return after the consumer left")
	}
	if s.streamPW != nil {
		_ = s.streamPW.Close()
	}
	return nil
}

func (s *jcState) disconnectAfterFirstFrame(string) error {
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(s.relayRW.Body.String(), "one") {
		if time.Now().After(deadline) {
			return fmt.Errorf("the first content frame never reached the consumer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The consumer leaves. The raw station here ignores cancels and keeps its stream open, so
	// the relay waits for the station's end (bounded by the stream idle timeout); a real agent
	// stops on the cancel (TestJobCancelEndToEndWithTheRealAgent). The claim under test is the
	// cancel's delivery, so the step does not wait for the relay to return.
	s.cancelRelay()
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (s *jcState) noCancelQueued(name string) error {
	n := s.node(name)
	for _, i := range []string{"A", "B"} {
		if p := s.cancelPoll(i, n, n.token); p.code != http.StatusNoContent {
			return fmt.Errorf("instance %s answered %d %v to %s's cancel poll; a node that never advertised cancels is never sent one", i, p.code, p.ids, name)
		}
	}
	return nil
}

func (s *jcState) billedZero(string) error {
	wallet := protocol.UserIDFromPubkey(hex.EncodeToString(s.consumer.Public().(ed25519.PublicKey)))
	spent, err := s.ack.xi.db.SpendOf(wallet)
	if err != nil {
		return err
	}
	if spent != 0 {
		return fmt.Errorf("the consumer was billed %.6f, want $0", spent)
	}
	return nil
}

// ---- cancel polls ---------------------------------------------------------------------------

func (s *jcState) nextCancelsJ(name string) error {
	n := s.node(name)
	p := s.cancelPoll("A", n, n.token)
	if p.code != http.StatusOK || len(p.ids) != 1 || p.ids[0] != s.jobID {
		return fmt.Errorf("%s's cancel poll answered %d %v, want 200 [%s]", name, p.code, p.ids, s.jobID)
	}
	return nil
}

func (s *jcState) secondIs204() error {
	for _, n := range s.nodes {
		if p := s.cancelPoll("B", n, n.token); p.code != http.StatusNoContent {
			return fmt.Errorf("a second cancel poll answered %d %v, want 204 (a cancel is delivered once)", p.code, p.ids)
		}
	}
	return nil
}

func (s *jcState) heldOnB(name string) error {
	n := s.node(name)
	if err := s.pollsWithHeader(name); err != nil {
		return err
	}
	cancelPollHold = 5 * time.Second
	s.heldPoll = make(chan jcPoll, 1)
	go func() { s.heldPoll <- s.cancelPoll("B", n, n.token) }()
	time.Sleep(150 * time.Millisecond) // the poll is held (subscribed) before the cancel is sent
	return nil
}

func (s *jcState) heldAnswersJ() error {
	select {
	case p := <-s.heldPoll:
		if p.code != http.StatusOK || len(p.ids) != 1 || p.ids[0] != s.jobID {
			return fmt.Errorf("the poll held on B answered %d %v, want 200 [%s]", p.code, p.ids, s.jobID)
		}
		return nil
	case <-time.After(6 * time.Second):
		return fmt.Errorf("the poll held on instance B never answered")
	}
}

func (s *jcState) noPollOpen(name string) error { return s.pollsWithHeader(name) }

func (s *jcState) opensLater(name string, secs int) error {
	time.Sleep(time.Duration(secs) * 100 * time.Millisecond) // scaled with the buffer lifetime
	n := s.node(name)
	p := s.cancelPoll("A", n, n.token)
	s.heldPoll = make(chan jcPoll, 1)
	s.heldPoll <- p
	return nil
}

func (s *jcState) thatPollJ() error { return s.heldAnswersJ() }

func (s *jcState) cancelQueued() error {
	for name, n := range s.nodes {
		_ = name
		s.jobID = "J-" + xiNonce()
		s.inst("A").sendCancel(n.id, s.jobID)
		return nil
	}
	return fmt.Errorf("no node in this scenario")
}

func (s *jcState) twoMinutes() error { time.Sleep(cancelBufTTL + 200*time.Millisecond); return nil }

func (s *jcState) nextIs204(name string) error {
	n := s.node(name)
	if p := s.cancelPoll("A", n, n.token); p.code != http.StatusNoContent {
		return fmt.Errorf("%s's cancel poll answered %d %v, want 204 (an undelivered cancel expires)", name, p.code, p.ids)
	}
	return nil
}

func (s *jcState) opensNothing(name string) error {
	n := s.node(name)
	start := time.Now()
	p := s.cancelPoll("A", n, n.token)
	s.status = p.code
	if p.code == http.StatusNoContent && time.Since(start) < cancelPollHold-50*time.Millisecond {
		return fmt.Errorf("the cancel poll answered 204 after %s, before its %s hold", time.Since(start), cancelPollHold)
	}
	return nil
}

func (s *jcState) answers204AfterHold() error {
	if s.status != http.StatusNoContent {
		return fmt.Errorf("the cancel poll answered %d, want 204", s.status)
	}
	return nil
}

func (s *jcState) wrongToken() error {
	n := s.node("n-1")
	_ = s.pollsWithHeader("n-1")
	s.jobID = "J-" + xiNonce()
	s.inst("A").sendCancel(n.id, s.jobID)
	s.status = s.cancelPoll("A", n, "wrong-token").code
	return nil
}

func (s *jcState) statusIs(code int) error {
	if s.status != code {
		return fmt.Errorf("status %d, want %d", s.status, code)
	}
	return nil
}

func (s *jcState) noneConsumed() error { return s.nextCancelsJ("n-1") }

func (s *jcState) bothCapable(a, b string) error {
	if err := s.pollsWithHeader(a); err != nil {
		return err
	}
	return s.pollsWithHeader(b)
}

func (s *jcState) queuedForK(name string) error {
	s.inst("A").sendCancel(s.node(name).id, "K-"+xiNonce())
	return nil
}

func (s *jcState) opensPoll(name string) error {
	n := s.node(name)
	s.status = s.cancelPoll("A", n, n.token).code
	return nil
}

func (s *jcState) answers204() error { return s.answers204AfterHold() }

func (s *jcState) earnsNothing(name string) error {
	got, err := s.ack.xi.db.EarningsOf(s.node(name).id)
	if err != nil {
		return err
	}
	if got != 0 {
		return fmt.Errorf("%s's operator earned %.6f for the cancelled job, want 0", name, got)
	}
	return nil
}

func (s *jcState) noStrike() error {
	for name, n := range s.nodes {
		acct, ok, _ := s.ack.xi.db.AccountOfNode(n.id)
		if !ok {
			continue // an unowned raw node cannot be struck; the relay path is what is under test
		}
		if rows, _ := s.ack.xi.db.StrikesByOwner(acct, 10); len(rows) != 0 {
			return fmt.Errorf("%d owner strike(s) recorded against %s's owner for a cancelled job", len(rows), name)
		}
	}
	return nil
}

func jcInit(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		st := &jcState{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			st.cleanup()
			return ctx, nil
		})
		sc.Step(`^a multi-instance broker of two instances sharing one store$`, st.twoInstances)
		sc.Step(`^node "([^"]+)" is on air for "([^"]+)"$`, st.onAir)
		sc.Step(`^"([^"]+)" polls with X-Roger-Cancel "1"$`, st.pollsWithHeader)
		sc.Step(`^"([^"]+)" polled with X-Roger-Cancel "1"$`, st.pollsWithHeader)
		sc.Step(`^"([^"]+)" is cancel-capable on every instance$`, st.capableEverywhere)
		sc.Step(`^(\d+) minutes pass with "([^"]+)" polling without the header$`, st.ageOut)
		sc.Step(`^"([^"]+)" is not cancel-capable$`, st.notCapable)
		sc.Step(`^"([^"]+)" polls without X-Roger-Cancel$`, st.pollsWithoutHeader)
		sc.Step(`^"([^"]+)"'s non-stream request is dispatched to "([^"]+)"(?: as job J)?$`, st.nonStreamDispatched)
		sc.Step(`^"([^"]+)"'s streaming request is dispatched to "([^"]+)" as job J$`, st.streamDispatched)
		sc.Step(`^"([^"]+)" disconnects before the result arrives$`, func(string) error { return st.disconnect() })
		sc.Step(`^"([^"]+)" disconnects after the first content frame$`, st.disconnectAfterFirstFrame)
		sc.Step(`^no cancel is queued for "([^"]+)"$`, st.noCancelQueued)
		sc.Step(`^"([^"]+)" is billed \$0$`, st.billedZero)
		sc.Step(`^"([^"]+)" is cancel-capable$`, st.isCapable)
		sc.Step(`^"([^"]+)"'s next GET /agent/cancels answers 200 with ids \[J\]$`, st.nextCancelsJ)
		sc.Step(`^a second GET /agent/cancels answers 204$`, st.secondIs204)
		sc.Step(`^"([^"]+)" is cancel-capable and its cancel poll is held on instance B$`, st.heldOnB)
		sc.Step(`^"([^"]+)"'s non-stream request is relayed by instance A to "([^"]+)" as job J$`, st.relayedByA)
		sc.Step(`^the cancel poll held on instance B answers 200 with ids \[J\]$`, st.heldAnswersJ)
		sc.Step(`^"([^"]+)" is cancel-capable and has no cancel poll open$`, st.noPollOpen)
		sc.Step(`^"([^"]+)" opens a cancel poll (\d+) seconds later$`, st.opensLater)
		sc.Step(`^that poll answers 200 with ids \[J\]$`, st.thatPollJ)
		sc.Step(`^a cancel for job J was queued$`, st.cancelQueued)
		sc.Step(`^2 minutes pass$`, st.twoMinutes)
		sc.Step(`^"([^"]+)"'s next GET /agent/cancels answers 204$`, st.nextIs204)
		sc.Step(`^"([^"]+)" opens a cancel poll and nothing is cancelled$`, st.opensNothing)
		sc.Step(`^the poll answers 204 after the hold$`, st.answers204AfterHold)
		sc.Step(`^a caller opens GET /agent/cancels\?node=n-1 with a wrong token$`, st.wrongToken)
		sc.Step(`^the status is (\d+)$`, st.statusIs)
		sc.Step(`^no cancel is consumed$`, st.noneConsumed)
		sc.Step(`^nodes "([^"]+)" and "([^"]+)" are cancel-capable$`, st.bothCapable)
		sc.Step(`^a cancel is queued for "([^"]+)"'s job K$`, st.queuedForK)
		sc.Step(`^"([^"]+)" opens a cancel poll$`, st.opensPoll)
		sc.Step(`^the poll answers 204$`, st.answers204)
		sc.Step(`^"([^"]+)"'s operator earns nothing for J$`, st.earnsNothing)
		sc.Step(`^no owner strike is recorded for J$`, st.noStrike)
	}
}

func TestJobCancelBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: jcInit(t),
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/multinode/job_cancel.feature"},
			Tags: "~@agent", TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("job_cancel.feature failed")
	}
}

// TestJobCancelEndToEndWithTheRealAgent: the REAL node agent behind the load balancer (polls and
// cancel polls land on either instance) serves a non-stream job from an upstream that never
// answers on its own; the consumer leaves; the agent's upstream request is aborted, and the agent
// then serves the next job normally.
func TestJobCancelEndToEndWithTheRealAgent(t *testing.T) {
	s := &jcState{}
	s.reset(t)
	defer s.cleanup()
	a := s.ack
	var mu sync.Mutex
	aborted, hit := map[string]bool{}, make(chan string, 4)
	a.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := body.Messages[0].Content
		hit <- id
		if id == "slow" {
			select {
			case <-r.Context().Done(): // only a cancel ends this request in time
			case <-time.After(10 * time.Second):
				return
			}
			mu.Lock()
			aborted[id] = true
			mu.Unlock()
			return
		}
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer a.upstream.Close()
	if err := a.twoInstances(); err != nil {
		t.Fatal(err)
	}
	defer a.lb.Close()
	if err := a.startNode(2); err != nil {
		t.Fatal(err)
	}
	defer a.sess.Stop()
	relay := func(content string, ctx context.Context) *recWriter {
		body := fmt.Sprintf(`{"model":"free-m","messages":[{"role":"user","content":%q}],"max_tokens":16}`, content)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
		signReq(r, s.consumer, []byte(body))
		rw := &recWriter{ResponseRecorder: httptest.NewRecorder()}
		s.inst("A").relay(rw, r)
		return rw
	}
	// The agent's first poll carrying the header makes it cancel-capable on both instances.
	deadline := time.Now().Add(5 * time.Second)
	for !(s.inst("A").cancelCapable(a.nodeID) && s.inst("B").cancelCapable(a.nodeID)) {
		if time.Now().After(deadline) {
			t.Fatal("the real agent was never recorded as cancel-capable on both instances")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *recWriter, 1)
	go func() { done <- relay("slow", ctx) }()
	select {
	case <-hit:
	case <-time.After(6 * time.Second):
		t.Fatal("the job never reached the agent's upstream")
	}
	cancel() // the consumer leaves
	left := time.Now()
	for {
		mu.Lock()
		ok := aborted["slow"]
		mu.Unlock()
		if ok {
			break
		}
		if time.Since(left) > 2*time.Second {
			t.Fatal("the agent's upstream request was not aborted within 2s of the consumer leaving")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the relay did not return after the consumer left")
	}
	if rw := relay("next", context.Background()); rw.Code != http.StatusOK {
		t.Fatalf("the next job was not served normally: %d %s", rw.Code, rw.Body.String())
	}
}
