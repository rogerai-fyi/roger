package main

// runtime_store_outage_bdd_test.go makes features/multinode/runtime_store_outage.feature
// EXECUTABLE: two REAL broker instances (queue-only dispatch, as production runs) over one REAL
// miniredis, the real durable store (Postgres when ROGERAI_TEST_DATABASE_URL is set), and real
// stations that long-poll an instance through agentPoll and post their results / stream chunks
// through agentResult / agentStream. The outage is real: the miniredis server is closed (and
// restarted for recovery). No mocks.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

// rsoBackdate records that node last long-polled b at `at` (the broker's own record of where a
// station polls). Wired to production once that record exists.
var rsoBackdate func(b *broker, node string, at time.Time)

type rsoStation struct {
	name       string
	id         string
	priv       ed25519.PrivateKey
	token      string
	pollInst   int
	resultInst int
	handed     atomic.Int64
	cancel     context.CancelFunc
	done       chan struct{}
}

type rsoOut struct {
	code     int
	body     string
	provider string
	cost     string
	retry    string
}

type rsoState struct {
	dq       *dqState
	stations map[string]*rsoStation
	outs     []rsoOut
	balance0 float64
	earned0  map[string]float64
}

func (s *rsoState) reset(t *testing.T) {
	s.dq = &dqState{}
	s.dq.reset(t)
	s.stations = map[string]*rsoStation{}
	s.outs = nil
	s.earned0 = map[string]float64{}
}

func (s *rsoState) cleanup() {
	for _, st := range s.stations {
		st.cancel()
		<-st.done
	}
	s.dq.cleanup()
}

// addStation registers a station (one model, priced) on BOTH instances; it polls nowhere yet.
func (s *rsoState) addStation(name string) *rsoStation {
	pub, priv, _ := ed25519.GenerateKey(nil)
	st := &rsoStation{name: name, id: name + "-" + xiNonce(), priv: priv, token: "tok-" + xiNonce(), done: make(chan struct{})}
	offers := []protocol.ModelOffer{{Model: dqModel, PriceIn: 1, PriceOut: 2, Ctx: 4096, Modality: protocol.ModalityChat}}
	for _, b := range s.dq.inst {
		b.mu.Lock()
		miRegisterNode(b, st.id, hex.EncodeToString(pub), st.token, offers)
		b.mu.Unlock()
	}
	s.stations[name] = st
	close(st.done) // not running until startPolling
	st.cancel = func() {}
	return st
}

// startPolling runs the station's poll loop against one instance, posting results to another.
func (s *rsoState) startPolling(st *rsoStation, pollInst, resultInst int) {
	st.pollInst, st.resultInst = pollInst, resultInst
	ctx, cancel := context.WithCancel(s.dq.ctx)
	st.cancel, st.done = cancel, make(chan struct{})
	go func() {
		defer close(st.done)
		for ctx.Err() == nil {
			b := s.dq.inst[pollInst]
			r := httptest.NewRequest(http.MethodGet, "/agent/poll?node="+st.id, nil).WithContext(ctx)
			r.Header.Set("Authorization", miNodeBearer(st.token))
			w := httptest.NewRecorder()
			b.agentPoll(w, r)
			if w.Code != http.StatusOK || w.Body.Len() == 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
				continue
			}
			var job protocol.Job
			if json.Unmarshal(w.Body.Bytes(), &job) != nil {
				continue
			}
			st.handed.Add(1)
			target := s.dq.inst[resultInst]
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(job.Body, &req)
			if req.Stream {
				pr, pw := io.Pipe()
				go func() {
					for _, c := range []string{
						"data: {\"choices\":[{\"delta\":{\"content\":\"served \"}}]}\n\n",
						"data: {\"choices\":[{\"delta\":{\"content\":\"" + st.name + "\"}}]}\n\n",
						"data: [DONE]\n\n",
					} {
						_, _ = pw.Write([]byte(c))
					}
					_ = pw.Close()
				}()
				sr := httptest.NewRequest(http.MethodPost, "/agent/stream?node="+st.id+"&job="+job.ID, pr)
				sr.Header.Set("Authorization", miNodeBearer(st.token))
				target.agentStream(httptest.NewRecorder(), sr)
			}
			res := miSignedResult(job.ID, st.id, dqModel, "served "+st.name, st.priv, 200)
			rb, _ := json.Marshal(res)
			rr := httptest.NewRequest(http.MethodPost, "/agent/result?node="+st.id, strings.NewReader(string(rb)))
			rr.Header.Set("Authorization", miNodeBearer(st.token))
			target.agentResult(httptest.NewRecorder(), rr)
		}
	}()
	// Wait until the station is actually holding a poll on that instance.
	deadline := time.Now().Add(3 * time.Second)
	for localIdle(s.dq.inst[pollInst], st.id) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- Background ---------------------------------------------------------------------------

func (s *rsoState) twoInstances() error { return s.dq.twoInstancesOneValkey() }

func (s *rsoState) fundedConsumer() error {
	if err := s.dq.fundConsumer(); err != nil {
		return err
	}
	s.balance0, _ = s.dq.db.BalanceOf(s.dq.wallet, 0)
	return nil
}

// ---- Givens -------------------------------------------------------------------------------

func (s *rsoState) pollsAndPostsHere(name string, inst, resInst int) error {
	st := s.addStation(name)
	s.startPolling(st, inst-1, resInst-1)
	s.earned0[st.id], _ = s.dq.db.EarningsOf(st.id)
	return nil
}

func (s *rsoState) pollsOnly(name string, inst int) error {
	st := s.addStation(name)
	// A station polling only the peer posts its results there too (the station's own instance).
	s.startPolling(st, inst-1, inst-1)
	return nil
}

func (s *rsoState) betterScored(name string) error {
	// Every other station is demoted to Tier B (a measured success average under the 0.55
	// gate), so with the shared store up the pick on instance 1 always lands on `name`.
	for _, b := range s.dq.inst {
		b.metricsMu.Lock()
		for n, st := range s.stations {
			if n != name {
				b.success[st.id] = 0.3
			}
		}
		b.metricsMu.Unlock()
	}
	return nil
}

func (s *rsoState) lastPolledLongAgo(name string, inst int) error {
	st := s.addStation(name)
	s.startPolling(st, inst-1, inst-1)
	st.cancel()
	<-st.done
	if rsoBackdate == nil {
		return fmt.Errorf("the broker records no time of a station's last poll on this instance")
	}
	rsoBackdate(s.dq.inst[inst-1], st.id, time.Now().Add(-31*time.Second))
	return nil
}

func (s *rsoState) capReached() error {
	spend, err := s.dq.db.MonthSpendOf(s.dq.wallet, time.Now())
	if err != nil {
		return err
	}
	if spend <= 0 {
		return fmt.Errorf("the consumer has no month spend to cap at (%v)", spend)
	}
	return s.dq.db.SetMonthlyCap(s.dq.wallet, spend)
}

func (s *rsoState) storeDown() error {
	for _, mr := range s.dq.servers {
		mr.Close()
	}
	return s.waitHealthy(0, false)
}

func (s *rsoState) storeBack() error {
	for _, mr := range s.dq.servers {
		// miniredis quirk: Close cancels its Ctx and Start never renews it, so after a Restart
		// every blocking command (the inbox XREAD) hangs server side until the client's read
		// deadline. Real Valkey has no such state; renew it so the restart is a clean one.
		mr.Lock()
		mr.Ctx, mr.CtxCancel = context.WithCancel(context.Background())
		mr.Unlock()
		if err := mr.Restart(); err != nil {
			return err
		}
	}
	if err := s.waitHealthy(0, true); err != nil {
		return err
	}
	if err := s.waitHealthy(1, true); err != nil {
		return err
	}
	return nil
}

// waitHealthy waits until instance i's shared store reports want (the /ready "degraded" signal),
// nudging it with a cheap read the way the broker's own periodic sync would.
func (s *rsoState) waitHealthy(i int, want bool) error {
	vs := s.dq.inst[i].shared.(*valkeyStore)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, _, _ = vs.cacheGet("rso:probe")
		if vs.healthy() == want {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("instance %d never reported healthy=%v", i+1, want)
}

// ---- Whens --------------------------------------------------------------------------------

func (s *rsoState) relayOnce(inst int, stream bool) rsoOut {
	body := `{"model":"` + dqModel + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	if stream {
		body = `{"model":"` + dqModel + `","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	}
	r := miSignedRelayReq(s.dq.t, s.dq.consumer, []byte(body), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	s.dq.inst[inst-1].relay(w, r)
	o := rsoOut{code: w.Code, body: w.Body.String(), provider: w.Header().Get("X-RogerAI-Provider"),
		cost: w.Header().Get("X-RogerAI-Cost"), retry: w.Header().Get("Retry-After")}
	s.outs = append(s.outs, o)
	return o
}

func (s *rsoState) relaysThrough(inst int) error { s.relayOnce(inst, false); return nil }
func (s *rsoState) relaysNTimes(inst, n int) error {
	for i := 0; i < n; i++ {
		s.relayOnce(inst, false)
	}
	return nil
}
func (s *rsoState) streamsThrough(inst int) error { s.relayOnce(inst, true); return nil }

// ---- Thens --------------------------------------------------------------------------------

func (s *rsoState) last() (rsoOut, error) {
	if len(s.outs) == 0 {
		return rsoOut{}, fmt.Errorf("no relay was made")
	}
	return s.outs[len(s.outs)-1], nil
}

func (s *rsoState) servedBy(name string) error {
	o, err := s.last()
	if err != nil {
		return err
	}
	st := s.stations[name]
	if o.code != http.StatusOK || o.provider != st.id {
		return fmt.Errorf("response %d provider %q, want 200 served by %q (%s): %.200s", o.code, o.provider, name, st.id, o.body)
	}
	return nil
}

func (s *rsoState) everyServedBy(name string) error {
	st := s.stations[name]
	for i, o := range s.outs {
		if o.code != http.StatusOK || o.provider != st.id {
			return fmt.Errorf("relay %d: %d provider %q, want 200 served by %q: %.200s", i+1, o.code, o.provider, name, o.body)
		}
	}
	return nil
}

func (s *rsoState) neverHanded(name string) error {
	if n := s.stations[name].handed.Load(); n != 0 {
		return fmt.Errorf("%q was handed %d job(s), want none", name, n)
	}
	return nil
}

func (s *rsoState) billedExactly() error {
	o, err := s.last()
	if err != nil {
		return err
	}
	var cost float64
	if _, err := fmt.Sscanf(o.cost, "%g", &cost); err != nil || cost <= 0 {
		if strings.Contains(o.body, "rogerai-cost=") { // a stream carries its cost in the meter comment
			i := strings.Index(o.body, "rogerai-cost=")
			_, err = fmt.Sscanf(o.body[i+len("rogerai-cost="):], "%g", &cost)
		}
		if err != nil || cost <= 0 {
			return fmt.Errorf("no positive cost reported (header %q): %.300s", o.cost, o.body)
		}
	}
	bal, err := s.dq.db.BalanceOf(s.dq.wallet, 0)
	if err != nil {
		return err
	}
	if math.Abs((s.balance0-bal)-cost) > 1e-9 {
		return fmt.Errorf("balance moved by %v, want exactly the reported cost %v (no hold left)", s.balance0-bal, cost)
	}
	return nil
}

func (s *rsoState) operatorEarned() error {
	for _, st := range s.stations {
		e, err := s.dq.db.EarningsOf(st.id)
		if err != nil {
			return err
		}
		if e > s.earned0[st.id] {
			return nil
		}
	}
	return fmt.Errorf("no station operator earned for the served reply")
}

func (s *rsoState) streamCarries() error {
	o, err := s.last()
	if err != nil {
		return err
	}
	if o.code != http.StatusOK || !strings.Contains(o.body, "served ") || !strings.Contains(o.body, "[DONE]") {
		return fmt.Errorf("stream %d did not carry the station's content and [DONE]: %.300s", o.code, o.body)
	}
	return nil
}

func (s *rsoState) is503Code(code string) error {
	o, err := s.last()
	if err != nil {
		return err
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(o.body), &env)
	if o.code != http.StatusServiceUnavailable || env.Error.Code != code {
		return fmt.Errorf("response %d code %q, want 503 %q: %.200s", o.code, env.Error.Code, code, o.body)
	}
	return nil
}

func (s *rsoState) retryAndZeroCost() error {
	o, err := s.last()
	if err != nil {
		return err
	}
	if o.retry == "" || o.cost != "0" {
		return fmt.Errorf("Retry-After %q, X-RogerAI-Cost %q, want a Retry-After and cost 0", o.retry, o.cost)
	}
	return nil
}

func (s *rsoState) balanceUnchanged() error {
	bal, err := s.dq.db.BalanceOf(s.dq.wallet, 0)
	if err != nil {
		return err
	}
	if math.Abs(bal-s.balance0) > 1e-12 {
		return fmt.Errorf("balance %v, want unchanged %v (a hold was left or a charge made)", bal, s.balance0)
	}
	return nil
}

func (s *rsoState) is402() error {
	o, err := s.last()
	if err != nil {
		return err
	}
	if o.code != http.StatusPaymentRequired {
		return fmt.Errorf("response %d, want 402: %.200s", o.code, o.body)
	}
	return nil
}

func rsoInit(t *testing.T) func(*godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		s := &rsoState{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			s.reset(t)
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			s.cleanup()
			return ctx, nil
		})
		sc.Step(`^a multi-instance broker of two instances sharing one Valkey$`, s.twoInstances)
		sc.Step(`^a funded consumer$`, s.fundedConsumer)
		sc.Step(`^station "([^"]+)" long-polls instance (\d) and posts its results to instance (\d)$`, s.pollsAndPostsHere)
		sc.Step(`^station "([^"]+)" long-polls only instance (\d)$`, s.pollsOnly)
		sc.Step(`^"([^"]+)" is the better-scored station$`, s.betterScored)
		sc.Step(`^station "([^"]+)" last long-polled instance (\d) more than 30 seconds ago$`, s.lastPolledLongAgo)
		sc.Step(`^the consumer's monthly cap is already reached$`, s.capReached)
		sc.Step(`^the shared store stops answering and instance 1 has noticed$`, s.storeDown)
		sc.Step(`^the shared store answers again and instance 1 has noticed$`, s.storeBack)
		sc.Step(`^the consumer relays through instance (\d)$`, s.relaysThrough)
		sc.Step(`^the consumer relays through instance (\d) (\d+) times$`, s.relaysNTimes)
		sc.Step(`^the consumer streams through instance (\d)$`, s.streamsThrough)
		sc.Step(`^the response is 200 served by "([^"]+)"$`, s.servedBy)
		sc.Step(`^every response is 200 served by "([^"]+)"$`, s.everyServedBy)
		sc.Step(`^"([^"]+)" was never handed a job$`, s.neverHanded)
		sc.Step(`^the consumer was billed exactly the station's price for the reply$`, s.billedExactly)
		sc.Step(`^the station operator earned for it$`, s.operatorEarned)
		sc.Step(`^the stream carries the station's content and ends with \[DONE\]$`, s.streamCarries)
		sc.Step(`^the response is 503 with error code "([^"]+)"$`, s.is503Code)
		sc.Step(`^the response carries a Retry-After and X-RogerAI-Cost "0"$`, s.retryAndZeroCost)
		sc.Step(`^no hold is left for the consumer and the balance is unchanged$`, s.balanceUnchanged)
		sc.Step(`^the response is 402$`, s.is402)
	}
}

func TestRuntimeStoreOutageBDD(t *testing.T) {
	t.Setenv("ROGERAI_RATE_BURST", "1000")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	suite := godog.TestSuite{
		ScenarioInitializer: rsoInit(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/runtime_store_outage.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/runtime_store_outage scenarios failed (see godog output above)")
	}
}

func init() {
	rsoBackdate = func(b *broker, node string, at time.Time) {
		b.mu.Lock()
		if b.localPollAt == nil {
			b.localPollAt = map[string]time.Time{}
		}
		b.localPollAt[node] = at
		b.mu.Unlock()
	}
}
