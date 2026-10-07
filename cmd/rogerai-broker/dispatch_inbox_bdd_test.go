package main

// dispatch_inbox_bdd_test.go makes features/multinode/dispatch_inbox.feature EXECUTABLE.
//
// REAL DEPS, NO MOCKS: two (or, for the ring, two-shard) broker instances sharing a REAL
// Valkey (ROGERAI_TEST_REDIS_URL) or an in-process miniredis speaking the real protocol; each
// instance has its OWN client, as in production. Nodes are real long-poll loops driving the
// broker's own handlers (agentPoll / agentResult) with node-signed receipts; the money
// scenarios go through the full relay() on a funded consumer.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"rogerai.fm/roger/v6/internal/bddtest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/redis/go-redis/v9"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

const dqModel = "dq-paid"

// dqNode is one node (station) and its poll loops.
type dqNode struct {
	id    string
	pub   string
	priv  ed25519.PrivateKey
	token string
	delay time.Duration // serve time for an ordinary job

	mu       sync.Mutex
	served   map[string]int // job id -> times served
	servedBy map[string]int // job id -> instance index that handed it over
	cancels  [2][]context.CancelFunc
}

type dqOutcome struct {
	err     error
	status  int
	retry   string
	at      time.Time
	elapsed time.Duration
}

type dqState struct {
	t       *testing.T
	servers []*miniredis.Miniredis
	urls    []string
	db      store.Store
	priv    ed25519.PrivateKey
	inst    [2]*broker
	origin  *broker // a third instance, for the stale-route scenario
	nudges0 int64
	nodes   map[string]*dqNode

	release  chan struct{} // closes to end every "10 s" serve
	relOnce  sync.Once
	ctx      context.Context
	stopAll  context.CancelFunc
	hookHits atomic.Int64
	hookGate chan struct{}

	consumer       ed25519.PrivateKey
	wallet         string
	startBalance   float64
	singleCost     float64
	mu             sync.Mutex
	outcomes       map[string]dqOutcome
	firstDispatch  time.Time
	jobIDs         []string
	pendingJob     string
	pendingDone    chan dqOutcome
	pendingRelays  []chan dqOutcome
	relayStart     time.Time
	bounceBaseline int64
}

func (s *dqState) reset(t *testing.T) {
	s.t = t
	s.nodes = map[string]*dqNode{}
	s.outcomes = map[string]dqOutcome{}
	s.release = make(chan struct{})
	s.relOnce = sync.Once{}
	s.ctx, s.stopAll = context.WithCancel(context.Background())
	s.jobIDs = nil
	s.pendingRelays = nil
	s.hookGate = nil
	s.hookHits.Store(0)
	_, s.priv, _ = ed25519.GenerateKey(nil)
	s.db = xiStore(t)
}

func (s *dqState) cleanup() {
	s.releaseServes()
	if s.hookGate != nil {
		close(s.hookGate)
		s.hookGate = nil
	}
	dqAfterPop = nil
	s.stopAll()
	for _, b := range s.inst {
		if b != nil && b.shared != nil {
			_ = b.shared.(*valkeyStore).Close()
		}
	}
	s.inst = [2]*broker{}
	if s.origin != nil {
		_ = s.origin.shared.(*valkeyStore).Close()
		s.origin = nil
	}
}

func (s *dqState) releaseServes() { s.relOnce.Do(func() { close(s.release) }) }

// newInstance builds one broker instance over the given shared-store URLs (one = a single
// Valkey, several = a ring), in queue-only dispatch.
func (s *dqState) newInstance(urls []string) *broker {
	b := newMIBroker(s.t, s.priv, s.db, nil)
	// Production's pool: go-redis defaults to 10 x GOMAXPROCS and production instances have
	// one vCPU. A many-core test machine would otherwise get a pool so large that the
	// connection budget could not tell a fixed pool from a connection per poll.
	pooled := make([]string, len(urls))
	for i, u := range urls {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		pooled[i] = u + sep + "pool_size=10"
	}
	vs, err := newValkeyStoreTopology(valkeyTopology{urls: pooled})
	if err != nil {
		s.t.Fatalf("valkey: %v", err)
	}
	b.shared = vs
	b.multiInstance = true
	b.dispatchMode = dispatchViaQueueOnly
	b.instanceID = newInstanceID()
	b.peerInflight = map[string]int{}
	b.feeRate = 0.2
	return b
}

func (s *dqState) redisURL() string {
	if url := os.Getenv("ROGERAI_TEST_REDIS_URL"); url != "" {
		return xiRedisURL(s.t)
	}
	mr := miniredis.RunT(s.t)
	s.servers = append(s.servers, mr)
	return "redis://" + mr.Addr()
}

func (s *dqState) twoInstancesOneValkey() error {
	s.servers = nil
	s.urls = []string{s.redisURL()}
	s.inst[0], s.inst[1] = s.newInstance(s.urls), s.newInstance(s.urls)
	return nil
}

// ---- nodes and their poll loops ----------------------------------------------------------

func (s *dqState) addNode(name string) *dqNode {
	pub, priv, _ := ed25519.GenerateKey(nil)
	n := &dqNode{
		id: name + "-" + xiNonce(), pub: hex.EncodeToString(pub), priv: priv, token: "tok-" + xiNonce(),
		served: map[string]int{}, servedBy: map[string]int{},
	}
	offers := []protocol.ModelOffer{{Model: dqModel, PriceIn: 1, PriceOut: 2, Ctx: 4096, Modality: protocol.ModalityChat}}
	for _, b := range s.inst {
		b.mu.Lock() // other nodes' pollers are live on this broker
		miRegisterNode(b, n.id, n.pub, n.token, offers)
		b.mu.Unlock()
	}
	s.nodes[name] = n
	return n
}

func (s *dqState) startPollers(n *dqNode, inst, count int) {
	for i := 0; i < count; i++ {
		ctx, cancel := context.WithCancel(s.ctx)
		n.mu.Lock()
		n.cancels[inst] = append(n.cancels[inst], cancel)
		n.mu.Unlock()
		go s.pollLoop(ctx, n, inst)
	}
}

func (s *dqState) stopPollers(n *dqNode, inst int) {
	n.mu.Lock()
	cs := n.cancels[inst]
	n.cancels[inst] = nil
	n.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

// pollLoop mirrors internal/agent: poll, serve, post the signed result, poll again. The
// result goes to the OTHER instance, so every result travels back to its origin.
func (s *dqState) pollLoop(ctx context.Context, n *dqNode, inst int) {
	for ctx.Err() == nil {
		b := s.inst[inst]
		r := httptest.NewRequest(http.MethodGet, "/agent/poll?node="+n.id, nil).WithContext(ctx)
		r.Header.Set("Authorization", miNodeBearer(n.token))
		w := httptest.NewRecorder()
		b.agentPoll(w, r)
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			continue
		}
		var job protocol.Job
		if json.Unmarshal(w.Body.Bytes(), &job) != nil {
			continue
		}
		n.mu.Lock()
		n.served[job.ID]++
		n.servedBy[job.ID] = inst
		n.mu.Unlock()
		switch {
		case strings.HasPrefix(job.ID, "hold-"): // a "10 s" job: held until released
			select {
			case <-s.release:
			case <-ctx.Done():
				return
			}
		case strings.HasPrefix(job.ID, "keep-"): // in flight until the scenario ends
			<-ctx.Done()
			return
		default:
			select {
			case <-time.After(n.delay):
			case <-ctx.Done():
				return
			}
		}
		res := miSignedResult(job.ID, n.id, dqModel, "ok "+job.ID, n.priv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node="+n.id, strings.NewReader(string(rb)))
		rr.Header.Set("Authorization", miNodeBearer(n.token))
		s.inst[1-inst].agentResult(httptest.NewRecorder(), rr)
	}
}

func (s *dqState) servedCount(n *dqNode, id string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.served[id]
}

// localIdle is how many of the node's polls sit idle on an instance.
func localIdle(b *broker, node string) int {
	q := b.dqueue()
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.idle[node])
}

func (s *dqState) waitIdle(n *dqNode, want [2]int) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if localIdle(s.inst[0], n.id) == want[0] && localIdle(s.inst[1], n.id) == want[1] {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("idle pollers = %d/%d, want %d/%d", localIdle(s.inst[0], n.id), localIdle(s.inst[1], n.id), want[0], want[1])
}

func (s *dqState) kokoro4() error {
	n := s.addNode("kokoro")
	s.startPollers(n, 0, 2)
	s.startPollers(n, 1, 2)
	return s.waitIdle(n, [2]int{2, 2})
}

// ---- dispatch -----------------------------------------------------------------------------

// dispatch sends one job from an instance and records its outcome when it resolves.
func (s *dqState) dispatch(from int, n *dqNode, job protocol.Job) chan dqOutcome {
	return s.dispatchVia(s.inst[from], n, job)
}

func (s *dqState) dispatchVia(origin *broker, n *dqNode, job protocol.Job) chan dqOutcome {
	out := make(chan dqOutcome, 1)
	start := time.Now()
	go func() {
		tk, err := origin.dispatchRemote(context.Background(), n.id, job, false)
		if err != nil {
			out <- dqOutcome{err: err, at: time.Now(), elapsed: time.Since(start)}
			return
		}
		defer tk.close()
		_, werr := tk.awaitResult(time.Now().Add(30 * time.Second))
		o := dqOutcome{err: werr, at: time.Now(), elapsed: time.Since(start)}
		s.mu.Lock()
		s.outcomes[job.ID] = o
		s.mu.Unlock()
		out <- o
	}()
	return out
}

func (s *dqState) burst(count int, name string) error {
	n := s.nodes[name]
	s.firstDispatch = time.Now()
	var chans []chan dqOutcome
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("burst-%d-%s", i, xiNonce())
		s.jobIDs = append(s.jobIDs, id)
		chans = append(chans, s.dispatch(i%2, n, protocol.Job{ID: id, Body: []byte(`{}`)}))
	}
	if elapsed := time.Since(s.firstDispatch); elapsed > 10*time.Millisecond {
		return fmt.Errorf("the %d dispatches took %s to issue, want within 10 ms", count, elapsed)
	}
	for _, c := range chans {
		<-c
	}
	return nil
}

func (s *dqState) burst4(name string) error { return s.burst(4, name) }
func (s *dqState) burst8(name string) error { return s.burst(8, name) }

func (s *dqState) serveTime300() error {
	for _, n := range s.nodes {
		n.delay = 300 * time.Millisecond
	}
	return nil
}

func (s *dqState) allServedOnce(want int) error {
	n := s.nodes["kokoro"]
	if len(s.jobIDs) != want {
		return fmt.Errorf("%d jobs dispatched, want %d", len(s.jobIDs), want)
	}
	time.Sleep(100 * time.Millisecond) // a duplicate serve would land here
	for _, id := range s.jobIDs {
		s.mu.Lock()
		o := s.outcomes[id]
		s.mu.Unlock()
		if o.err != nil {
			return fmt.Errorf("job %s failed: %v", id, o.err)
		}
		if c := s.servedCount(n, id); c != 1 {
			return fmt.Errorf("job %s was served %d times, want exactly 1", id, c)
		}
	}
	return nil
}

func (s *dqState) all4Served() error { return s.allServedOnce(4) }
func (s *dqState) all8Served() error { return s.allServedOnce(8) }

func (s *dqState) no503() error {
	for id, o := range s.outcomes {
		if o.err != nil {
			return fmt.Errorf("job %s answered %v", id, o.err)
		}
	}
	return nil
}

func (s *dqState) lastWithin900() error {
	var last time.Time
	for _, o := range s.outcomes {
		if o.at.After(last) {
			last = o.at
		}
	}
	if d := last.Sub(s.firstDispatch); d > 900*time.Millisecond {
		return fmt.Errorf("the last result arrived %s after the first dispatch, want within 900 ms", d)
	}
	return nil
}

// ---- connection budget ---------------------------------------------------------------------

func (s *dqState) manyNodes() error {
	for i := 0; i < 200; i++ {
		n := s.addNode(fmt.Sprintf("n%03d", i))
		s.startPollers(n, 0, 2)
		s.startPollers(n, 1, 2)
	}
	for name, n := range s.nodes {
		if name == "kokoro" {
			continue
		}
		if err := s.waitIdle(n, [2]int{2, 2}); err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
	}
	return nil
}

func (s *dqState) jobsInFlight() error {
	var names []string
	for name := range s.nodes {
		if name != "kokoro" {
			names = append(names, name)
		}
	}
	for i := 0; i < 400; i++ {
		n := s.nodes[names[i%len(names)]]
		s.dispatch(i%2, n, protocol.Job{ID: fmt.Sprintf("keep-%d-%s", i, xiNonce()), Body: []byte(`{}`)})
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		taken := 0
		for _, n := range s.nodes {
			n.mu.Lock()
			taken += len(n.served)
			n.mu.Unlock()
		}
		if taken >= 400 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("not all 400 jobs reached a poller")
}

func (s *dqState) connectionBudget() error {
	pool := s.inst[0].shared.(*valkeyStore).poolSize
	limit := 2 * (pool + 1)
	var got int
	if len(s.servers) == 1 {
		got = s.servers[0].CurrentConnectionCount()
	} else {
		opt, _ := redis.ParseURL(s.urls[0])
		c := redis.NewClient(opt)
		defer c.Close()
		info, err := c.Info(context.Background(), "clients").Result()
		if err != nil {
			return err
		}
		_, _ = fmt.Sscanf(info[strings.Index(info, "connected_clients:")+len("connected_clients:"):], "%d", &got)
		got-- // this probe
	}
	if got > limit {
		return fmt.Errorf("the brokers hold %d Valkey connections with 800 polls and 400 jobs in flight, want at most %d", got, limit)
	}
	return nil
}

// ---- stale route ----------------------------------------------------------------------------
//
// Staged with a THIRD instance as the origin (it holds no poller): instance 1's route claims
// more idle pollers than instance 2's, so the origin wakes instance 1 first; instance 1 has
// none and bounces; the origin must then wake instance 2 itself. Nothing else frees a poller,
// so the job is served only if the bounce handling works.

func (s *dqState) staleRouteClaims(name string) error {
	n := s.nodes[name]
	s.origin = s.newInstance(s.urls)
	s.origin.mu.Lock()
	miRegisterNode(s.origin, n.id, n.pub, n.token, []protocol.ModelOffer{{Model: dqModel, PriceIn: 1, PriceOut: 2, Ctx: 4096, Modality: protocol.ModalityChat}})
	s.origin.mu.Unlock()
	s.stopPollers(n, 0) // instance 1's pollers leave...
	if err := s.waitIdle(n, [2]int{0, 2}); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond) // ...and their own route write (idle 0) has landed
	// ...but the route still says instance 1 holds idle pollers (more than instance 2).
	ctx, cancel := opCtx()
	defer cancel()
	val := fmt.Sprintf("5:5:%d", time.Now().UnixMilli())
	return s.inst[0].shared.(*valkeyStore).rdb.HSet(ctx, dqRoutePrefix+n.id, s.inst[0].instanceID, val).Err()
}

func (s *dqState) waitServed(n *dqNode, want int) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		got := len(n.served)
		n.mu.Unlock()
		if got >= want {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("pollers took fewer than %d jobs", want)
}

func (s *dqState) pollerJustLeft() error {
	if got := localIdle(s.inst[0], s.nodes["kokoro"].id); got != 0 {
		return fmt.Errorf("instance 1 still holds %d idle pollers", got)
	}
	return nil
}

func (s *dqState) jobSentToInstance1() error {
	n := s.nodes["kokoro"]
	s.bounceBaseline = s.inst[0].stats.dqBounce.Load()
	s.nudges0 = s.origin.stats.dqNudge.Load()
	s.pendingJob = "stale-" + xiNonce()
	s.jobIDs = []string{s.pendingJob}
	s.pendingDone = s.dispatchVia(s.origin, n, protocol.Job{ID: s.pendingJob, Body: []byte(`{}`)})
	return nil
}

func (s *dqState) instance1Bounces() error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.inst[0].stats.dqBounce.Load() > s.bounceBaseline {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("instance 1 never bounced the wake-up back to the origin")
}

func (s *dqState) originResendsToInstance2() error {
	select {
	case o := <-s.pendingDone:
		if o.err != nil {
			return fmt.Errorf("the job failed: %v", o.err)
		}
		if o.elapsed > 900*time.Millisecond {
			// the origin's 1 s re-check would also re-wake; the bounce must be what did it
			return fmt.Errorf("served after %s: the bounce did not re-send the wake-up", o.elapsed)
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the job was never served after the bounce")
	}
	if got := s.origin.stats.dqNudge.Load() - s.nudges0; got != 2 {
		return fmt.Errorf("the origin sent %d wake-ups, want 2 (instance 1, then instance 2)", got)
	}
	n := s.nodes["kokoro"]
	n.mu.Lock()
	by := n.servedBy[s.pendingJob]
	n.mu.Unlock()
	if by != 1 {
		return fmt.Errorf("the job was handed over by instance %d, want instance 2", by+1)
	}
	return nil
}

func (s *dqState) servedExactlyOnce() error {
	time.Sleep(200 * time.Millisecond)
	if c := s.servedCount(s.nodes["kokoro"], s.pendingJob); c != 1 {
		return fmt.Errorf("the job was served %d times, want exactly 1", c)
	}
	return nil
}

// ---- queue deadline --------------------------------------------------------------------------

func (s *dqState) allPollersBusy10s(name string) error {
	n := s.nodes[name]
	for i := 0; i < 4; i++ {
		s.dispatch(i%2, n, protocol.Job{ID: fmt.Sprintf("hold-%d-%s", i, xiNonce()), Body: []byte(`{}`)})
	}
	return s.waitServed(n, 4)
}

func (s *dqState) audioJob(name string) error {
	n := s.nodes[name]
	s.pendingJob = "audio-" + xiNonce()
	s.pendingDone = s.dispatch(0, n, protocol.Job{ID: s.pendingJob, Body: []byte(`{}`), Path: "/v1/audio/speech"})
	return nil
}

func (s *dqState) busyAfter3s() error {
	select {
	case o := <-s.pendingDone:
		if o.err != errStationBusy {
			return fmt.Errorf("answered %v, want station busy", o.err)
		}
		if o.elapsed < 2900*time.Millisecond || o.elapsed > 4500*time.Millisecond {
			return fmt.Errorf("answered after %s, want after about 3 s", o.elapsed)
		}
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("no answer")
	}
}

func (s *dqState) neverServedAfter() error {
	s.releaseServes() // every poller comes free and polls again
	time.Sleep(time.Second)
	if c := s.servedCount(s.nodes["kokoro"], s.pendingJob); c != 0 {
		return fmt.Errorf("the withdrawn job was served %d times", c)
	}
	return nil
}

// ---- money: relays through the full relay() -----------------------------------------------

func (s *dqState) fundConsumer() error {
	_, priv, _ := ed25519.GenerateKey(nil)
	s.consumer = priv
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid := rand.Int63n(1<<31) + 1
	if err := s.db.BindOwner(store.Owner{GitHubID: gid, Login: "dq-buyer-" + xiNonce(), Pubkey: pubHex}); err != nil {
		return err
	}
	s.wallet = fmt.Sprintf("u_gh_%d", gid)
	if _, err := s.db.BalanceOf(s.wallet, 100); err != nil { // the one-time free seed
		return err
	}
	// Spend the seed down first (seed-funded spend earns operators nothing, by design), so
	// the relays under test are real-money-funded and the earnings assertion is exact.
	nonce := xiNonce()
	if _, err := s.db.Settle(s.wallet, "dq-seed-sink-"+nonce, 100, 1,
		protocol.UsageReceipt{RequestID: "dq-seed-burn-" + nonce, Model: "seed-burn", TS: time.Now().Unix()}); err != nil {
		return err
	}
	if _, err := s.db.AddCredits(s.wallet, 100); err != nil {
		return err
	}
	s.startBalance, _ = s.db.BalanceOf(s.wallet, 0)
	return nil
}

func (s *dqState) relay(from int) chan dqOutcome {
	out := make(chan dqOutcome, 1)
	go func() {
		start := time.Now()
		body := []byte(`{"model":"` + dqModel + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
		r := miSignedRelayReq(s.t, s.consumer, body, nil)
		w := httptest.NewRecorder()
		s.inst[from].relay(w, r)
		out <- dqOutcome{status: w.Code, retry: w.Header().Get("Retry-After"), at: time.Now(), elapsed: time.Since(start)}
	}()
	return out
}

func (s *dqState) queuedOnInstance2(name string) error {
	if err := s.fundConsumer(); err != nil {
		return err
	}
	n := s.nodes[name]
	s.stopPollers(n, 0) // only instance 2 polls, so instance 2 takes the jobs
	if err := s.waitIdle(n, [2]int{0, 2}); err != nil {
		return err
	}
	gate := make(chan struct{})
	s.hookGate = gate
	victim := s.inst[1].instanceID
	dqAfterPop = func(inst string) bool {
		if inst != victim {
			return false
		}
		s.hookHits.Add(1)
		<-gate      // instance 2 dies here, between taking the job and handing it over
		return true // a dead process does nothing more with it
	}
	s.relayStart = time.Now()
	for i := 0; i < 2; i++ {
		s.pendingRelays = append(s.pendingRelays, s.relay(0))
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.hookHits.Load() < 2 {
		if time.Now().After(deadline) {
			return fmt.Errorf("instance 2 took %d of the 2 jobs", s.hookHits.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func (s *dqState) instance2Dies() error {
	s.inst[1].dqueue().stop()
	s.stopPollers(s.nodes["kokoro"], 1)
	close(s.hookGate) // the stuck handoffs end as a crashed process: nothing written, nothing handed back
	s.hookGate = nil
	return nil
}

func (s *dqState) retryable503Within15() error {
	for i, c := range s.pendingRelays {
		select {
		case o := <-c:
			if o.status != http.StatusServiceUnavailable || o.retry == "" {
				return fmt.Errorf("job %d answered %d (Retry-After %q), want a retryable 503", i, o.status, o.retry)
			}
			if o.elapsed > 15*time.Second {
				return fmt.Errorf("job %d answered after %s, want within 15 s", i, o.elapsed)
			}
		case <-time.After(16*time.Second - time.Since(s.relayStart)):
			return fmt.Errorf("job %d had no answer within 15 s", i)
		}
	}
	return nil
}

func (s *dqState) noReceiptSettles() error {
	time.Sleep(200 * time.Millisecond)
	bal, err := s.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	if math.Abs(bal-s.startBalance) > 1e-9 {
		return fmt.Errorf("the consumer was charged %v for jobs never served", s.startBalance-bal)
	}
	earned, _ := s.db.EarningsOf(s.nodes["kokoro"].id)
	if earned != 0 {
		return fmt.Errorf("the station earned %v for jobs never served", earned)
	}
	return nil
}

func (s *dqState) hundredRelays(name string) error {
	if err := s.fundConsumer(); err != nil {
		return err
	}
	// One relay first, alone, to price a single job exactly.
	if o := <-s.relay(0); o.status != http.StatusOK {
		return fmt.Errorf("the pricing relay answered %d", o.status)
	}
	bal, _ := s.db.BalanceOf(s.wallet, 0)
	s.singleCost = s.startBalance - bal
	s.startBalance = bal
	var chans []chan dqOutcome
	for i := 0; i < 100; i++ {
		chans = append(chans, s.relay(rand.Intn(2)))
	}
	for i, c := range chans {
		if o := <-c; o.status != http.StatusOK {
			return fmt.Errorf("relay %d answered %d", i, o.status)
		}
	}
	return nil
}

func (s *dqState) everyJobOnce() error {
	time.Sleep(200 * time.Millisecond)
	total := 0
	for _, n := range s.nodes {
		n.mu.Lock()
		for id, c := range n.served {
			if c != 1 {
				n.mu.Unlock()
				return fmt.Errorf("job %s was served %d times, want exactly 1", id, c)
			}
			total++
		}
		n.mu.Unlock()
	}
	if want := len(s.jobIDs); want > 0 && total != want {
		return fmt.Errorf("%d jobs served, want %d", total, want)
	}
	return nil
}

func (s *dqState) oneReceiptPerJob() error {
	bal, err := s.db.BalanceOf(s.wallet, 0)
	if err != nil {
		return err
	}
	charged := s.startBalance - bal
	if s.singleCost <= 0 {
		return fmt.Errorf("a single job cost %v; the relay did not charge", s.singleCost)
	}
	if math.Abs(charged-100*s.singleCost) > 1e-6 {
		return fmt.Errorf("100 jobs charged %v, want exactly 100 x %v (one settle each)", charged, s.singleCost)
	}
	earned, _ := s.db.EarningsOf(s.nodes["kokoro"].id)
	want := (charged + s.singleCost) * (1 - s.inst[0].feeRate)
	if math.Abs(earned-want) > 1e-6 {
		return fmt.Errorf("the station earned %v, want %v (one settle per job)", earned, want)
	}
	return nil
}

// ---- the ring -----------------------------------------------------------------------------

func (s *dqState) ringOfTwo() error {
	s.cleanupInstances()
	s.servers = nil
	s.urls = []string{s.redisURL2(), s.redisURL2()}
	s.inst[0], s.inst[1] = s.newInstance(s.urls), s.newInstance(s.urls)
	s.nodes = map[string]*dqNode{}
	return s.kokoro4()
}

// redisURL2 always uses in-process servers: a ring needs two independent Valkeys.
func (s *dqState) redisURL2() string {
	mr := miniredis.RunT(s.t)
	s.servers = append(s.servers, mr)
	return "redis://" + mr.Addr()
}

func (s *dqState) cleanupInstances() {
	s.stopAll()
	for _, b := range s.inst {
		if b != nil {
			_ = b.shared.(*valkeyStore).Close()
		}
	}
	s.ctx, s.stopAll = context.WithCancel(context.Background())
}

func (s *dqState) hundredFromBoth() error {
	n := s.nodes["kokoro"]
	var chans []chan dqOutcome
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("ring-%d-%s", i, xiNonce())
		s.jobIDs = append(s.jobIDs, id)
		chans = append(chans, s.dispatch(i%2, n, protocol.Job{ID: id, Body: []byte(`{}`)}))
	}
	for i, c := range chans {
		if o := <-c; o.err != nil {
			return fmt.Errorf("job %d failed: %v", i, o.err)
		}
	}
	// The ring really spread the plane: both Valkeys hold dispatch keys.
	for i, mr := range s.servers {
		found := false
		for _, k := range mr.Keys() {
			if strings.HasPrefix(k, keyPrefix+"dq:") {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("ring shard %d holds no dispatch keys: the ring did not spread", i)
		}
	}
	return nil
}

func dqInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		s := &dqState{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			s.reset(t)
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			s.cleanup()
			return ctx, nil
		})
		sc.Step(`^a multi-instance broker of two instances sharing one Valkey$`, s.twoInstancesOneValkey)
		sc.Step(`^a node "kokoro" with 4 pollers split 2 and 2 across the instances$`, s.kokoro4)
		sc.Step(`^4 jobs are dispatched to "([^"]*)" within 10 ms$`, s.burst4)
		sc.Step(`^8 jobs are dispatched to "([^"]*)" within 10 ms$`, s.burst8)
		sc.Step(`^all 4 are served, each by exactly one poller$`, s.all4Served)
		sc.Step(`^all 8 are served$`, s.all8Served)
		sc.Step(`^no job is answered 503$`, s.no503)
		sc.Step(`^each job takes 300 ms to serve$`, s.serveTime300)
		sc.Step(`^the last result arrives within 900 ms of the first dispatch$`, s.lastWithin900)
		sc.Step(`^200 nodes with 4 pollers each spread over both instances$`, s.manyNodes)
		sc.Step(`^400 jobs are in flight$`, s.jobsInFlight)
		sc.Step(`^Valkey reports at most 2 × \(pool size \+ 1\) client connections from the brokers$`, s.connectionBudget)
		sc.Step(`^instance 1's route says it holds a poller for "([^"]*)"$`, s.staleRouteClaims)
		sc.Step(`^that poller has just left$`, s.pollerJustLeft)
		sc.Step(`^a job is sent to instance 1$`, s.jobSentToInstance1)
		sc.Step(`^instance 1 bounces it to the origin$`, s.instance1Bounces)
		sc.Step(`^the origin re-sends it to instance 2$`, s.originResendsToInstance2)
		sc.Step(`^the job is served exactly once$`, s.servedExactlyOnce)
		sc.Step(`^every poller of "([^"]*)" is serving a job that takes 10 s$`, s.allPollersBusy10s)
		sc.Step(`^an audio job is dispatched to "([^"]*)"$`, s.audioJob)
		sc.Step(`^after 3 s it is answered 503 "station busy"$`, s.busyAfter3s)
		sc.Step(`^no poller serves it after that$`, s.neverServedAfter)
		sc.Step(`^jobs for "([^"]*)" are queued on instance 2$`, s.queuedOnInstance2)
		sc.Step(`^instance 2 dies$`, s.instance2Dies)
		sc.Step(`^each of those jobs is answered a retryable 503 within 15 s$`, s.retryable503Within15)
		sc.Step(`^no receipt settles for them$`, s.noReceiptSettles)
		sc.Step(`^100 jobs are dispatched to "([^"]*)" from both instances at random$`, s.hundredRelays)
		sc.Step(`^every job is served exactly once$`, s.everyJobOnce)
		sc.Step(`^exactly one receipt settles per job$`, s.oneReceiptPerJob)
		sc.Step(`^the broker is configured with a ring of two Valkeys$`, s.ringOfTwo)
		sc.Step(`^100 jobs are dispatched from both instances$`, s.hundredFromBoth)
	}
}

func TestDispatchInboxBDD(t *testing.T) {
	// One test consumer fires 101 relays at once; the per-identity limiter is not under test.
	t.Setenv("ROGERAI_RATE_BURST", "1000")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	suite := godog.TestSuite{
		ScenarioInitializer: dqInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/dispatch_inbox.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("multinode/dispatch_inbox scenarios failed (see godog output above)")
	}
}
