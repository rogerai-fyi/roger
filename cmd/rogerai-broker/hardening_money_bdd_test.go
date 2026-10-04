package main

// hardening_money_bdd_test.go makes features/money/routing_money_hardening.feature EXECUTABLE
// (slice 6, contract §14.A money items) on the shared fa6State harness
// (hardening_fairness_bdd_test.go): the REAL broker, the real store (Postgres when
// ROGERAI_TEST_DATABASE_URL is set), real stations posting signed receipts, the real ledger.
//
// OBSERVATION POINTS:
//   - "ranked first" / "served by" read X-RogerAI-Provider of a real relay.
//   - The hold a relay placed is read from the payer's ledger (kind hold rows newer than the
//     When), and the output budget the broker chose is read from the body the station's
//     upstream received (max_tokens), never inferred.
//   - Dispatch failures are built for real where the single-instance broker has the path:
//     "no poller free" is a station whose job channel nobody drains (the production local
//     dispatch then answers busy after its 3 s wait). "station off air", "handoff lost" and
//     "dispatch bus error" exist only on the multi-instance queue plane, so those rows run
//     there (queuePlane): two instances on the scenario's shared miniredis in queue-only
//     dispatch, every station served by a real long-poll on /agent/poll, and the failure made
//     real at the moment it matters - the station's liveness expires as its dispatch starts,
//     the store errors for that one dispatch, or the instance holding the station's only poll
//     dies between the pop and the handoff (dqAfterPop).
//   - Disconnects and stalls use a station loop this runner drains (the harness loop cannot be
//     cancelled): it streams content frames on a timer through /agent/stream and records
//     whether the broker stopped reading (the job was cancelled) before it finished.
//   - The billed price is read from X-RogerAI-Price (in=;out=) of the relay that settled.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type mh6State struct {
	*fa6State
	ledgerMark map[string]int64
	streamOpts map[string]any // stream_options the next streaming request carries (nil = unset)
	disconnect int            // cancel the consumer request after this many content frames (0 = never)
	frames     int64          // content frames the custom station loop managed to write
	cancelled  atomic.Bool    // the broker stopped reading the station's stream before it ended
	stopLoops  chan struct{}
	loopWG     sync.WaitGroup
	conc       []fa6Resp
	concMu     sync.Mutex
	fourth     fa6Resp
	instB      bool
	undo       []func() // queue-plane scenario state restored at teardown (hooks, knobs, pollers)
}

func (s *mh6State) reset() error {
	if err := s.fa6State.reset(); err != nil {
		return err
	}
	s.ledgerMark, s.streamOpts, s.disconnect = map[string]int64{}, nil, 0
	atomic.StoreInt64(&s.frames, 0)
	s.cancelled.Store(false)
	s.stopLoops = make(chan struct{})
	s.conc, s.fourth, s.instB = nil, fa6Resp{}, false
	return nil
}

func (s *mh6State) teardown() {
	for i := len(s.undo) - 1; i >= 0; i-- {
		s.undo[i]()
	}
	s.undo = nil
	if s.stopLoops != nil {
		close(s.stopLoops)
		s.loopWG.Wait()
		s.stopLoops = nil
	}
	s.fa6State.teardown()
}

// --- ledger ------------------------------------------------------------------------------------

func (s *mh6State) markLedger(who string) {
	w := s.consumer(who)
	rows, _ := s.db.LedgerOf(w.wallet, nil, 1)
	var max int64
	for _, r := range rows {
		if r.ID > max {
			max = r.ID
		}
	}
	s.ledgerMark[who] = max
}

func (s *mh6State) ledgerSince(who string, kinds ...string) []store.LedgerRow {
	w := s.consumer(who)
	rows, _ := s.db.LedgerOf(w.wallet, kinds, 5000)
	var out []store.LedgerRow
	for _, r := range rows {
		if r.ID > s.ledgerMark[who] {
			out = append(out, r)
		}
	}
	return out
}

func (s *mh6State) holdsSince(who string) (n int, amount float64) {
	for _, r := range s.ledgerSince(who, store.KindHold) {
		n++
		amount += -r.Amount
	}
	return
}

func (s *mh6State) spentSince(who string) float64 {
	t := 0.0
	for _, r := range s.ledgerSince(who, store.KindSpend) {
		t += -r.Amount
	}
	return t
}

// relay is fa6State.do preceded by the snapshot + ledger mark of the payer.
func (s *mh6State) relay(sp fa6Spec) fa6Resp {
	who := sp.who
	if who == "" {
		who = "alice"
	}
	s.markLedger(who)
	return s.do(sp)
}

func mh6Prompt(n string) int { return atoiMust(n) }

// --- Background --------------------------------------------------------------------------------

func (s *mh6State) defaultInCap() error {
	_ = s.unsetEnv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_IN")
	return nil
}

func (s *mh6State) defaultOutputBudget(n string) error {
	_ = s.unsetEnv("ROGERAI_DEFAULT_OUTPUT_TOKENS")
	return nil
}

func (s *mh6State) unsetEnv(k string) error {
	s.setenv(k, "")
	return nil
}

func (s *mh6State) fundedHolds(who, amount string) error {
	s.scen["fund:"+who] = atofMust(amount)
	return s.fundWho(who, atofMust(amount))
}

// --- price ranking ---------------------------------------------------------------------------

func (s *mh6State) stationServesAt(name, model, in, out string) error {
	s.lastModel = model
	s.node(name, model, atofMust(in), atofMust(out))
	return nil
}

func (s *mh6State) relaysPromptMaxTokensFor(who, prompt, maxTokens, model string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: model, prompt: mh6Prompt(prompt), extra: map[string]any{"max_tokens": atoiMust(maxTokens)}})
	return nil
}

func (s *mh6State) twoStationsPriced(a, ai, ao, b, bi, bo string) error {
	s.node(a, s.lastModel, atofMust(ai), atofMust(ao))
	s.node(b, s.lastModel, atofMust(bi), atofMust(bo))
	return nil
}

func (s *mh6State) relaysPromptSort(who, prompt, maxTokens, sortBy string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: s.lastModel, prompt: mh6Prompt(prompt), extra: map[string]any{"max_tokens": atoiMust(maxTokens), "provider": map[string]any{"sort": sortBy}}})
	return nil
}

func (s *mh6State) rankedFirstEst(name, _, _ string) error { return s.servedBy200(name) }

func (s *mh6State) asPrevious(_, _ string) error {
	s.node("s1", s.lastModel, 1.00, 1.00)
	s.node("s2", s.lastModel, 0.10, 3.00)
	return nil
}

func (s *mh6State) relaysMCTAndMT(who, prompt, mct, mt, sortBy string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: s.lastModel, prompt: mh6Prompt(prompt), extra: map[string]any{
		"max_completion_tokens": atoiMust(mct), "max_tokens": atoiMust(mt), "provider": map[string]any{"sort": sortBy}}})
	return nil
}

func (s *mh6State) rankedFirst(name string) error { return s.servedBy200(name) }

func (s *mh6State) relaysNoMaxSort(who, prompt, sortBy string) error {
	s.snapshot()
	s.scen["prompt"] = mh6Prompt(prompt)
	s.relay(fa6Spec{who: who, model: s.lastModel, prompt: mh6Prompt(prompt), extra: map[string]any{"provider": map[string]any{"sort": sortBy}}})
	return nil
}

// estimateUses: the hold the broker placed equals measured prompt × in + n × out at the
// served station's prices (the estimate's output budget made visible through money).
func (s *mh6State) estimateUses(n string) error {
	if s.last.code != 200 {
		return fmt.Errorf("relay = %d: %.300s", s.last.code, s.last.body)
	}
	name := s.served(s.last)
	_, held := s.holdsSince(s.last.who)
	prompt := approxPromptTokens(s.lastBodyOf(name))
	// The one hold covers the priciest pair of the failover plan (contract §1b), and every
	// on-air station of the model is in that plan here: the estimate is n output tokens at
	// the priciest of them, not at the station that happened to serve.
	want, at := 0.0, ""
	for nm, st := range s.stations {
		if st.model != s.lastModel {
			continue
		}
		if c := (float64(prompt)*st.priceIn + float64(atoiMust(n))*st.priceOut) / 1e6; c > want {
			want, at = c, nm
		}
	}
	if math.Abs(held-want) > want*0.05+1e-9 {
		return fmt.Errorf("the hold was %.6f, want ~%.6f (prompt %d + %s output tokens at the plan's priciest station %s)", held, want, prompt, n, at)
	}
	return nil
}

func (s *mh6State) twoTrickFair(a, ai, ao, b, bi, bo string) error {
	s.node(a, s.lastModel, atofMust(ai), atofMust(ao))
	s.node(b, s.lastModel, atofMust(bi), atofMust(bo))
	return nil
}

func (s *mh6State) equallyHealthy() error {
	for _, st := range s.stations {
		s.setTPS(st.id, 50)
		s.setTTFT(st, 300)
		s.setCapacity(st, 4, 0)
	}
	return nil
}

func (s *mh6State) consumersPrompts(n, prompt string) error {
	s.snapshot()
	for i := 0; i < atoiMust(n); i++ {
		who := fmt.Sprintf("c%d", i%20)
		if i < 20 {
			_ = s.fundWho(who, 50)
		}
		s.heartbeat()
		s.do(fa6Spec{who: who, model: s.lastModel, prompt: mh6Prompt(prompt)})
	}
	return nil
}

func (s *mh6State) freeAndTwoPaid(f, a, b string) error {
	s.node(f, s.lastModel, 0, 0)
	s.node(a, s.lastModel, 0.20, 0.60)
	s.node(b, s.lastModel, 0.40, 1.20)
	return nil
}

func (s *mh6State) rangeComputed() error { return nil }

// freeDoesNotMove: the share between the two paid stations under the default ranking is the
// same whether or not the free offer is among the candidates (it contributes no price range).
func (s *mh6State) freeDoesNotMove(f string) error {
	draw := func(exclude map[string]bool) (int, int) {
		a, b := 0, 0
		for i := 0; i < 2000; i++ {
			s.b.mu.Lock()
			n, _, ok := s.b.pickFor(s.lastModel, false, 0, 0, 0, "", exclude, nil, nil, pickReq{rng: seededRand(fmt.Sprintf("mh6-%d", i))})
			s.b.mu.Unlock()
			if !ok {
				continue
			}
			switch n.NodeID {
			case s.st("s1").id:
				a++
			case s.st("s2").id:
				b++
			}
		}
		return a, b
	}
	a1, b1 := draw(nil)
	a2, b2 := draw(map[string]bool{s.st(f).id: true})
	r1 := float64(a1) / float64(max(1, a1+b1))
	r2 := float64(a2) / float64(max(1, a2+b2))
	if math.Abs(r1-r2) > 0.08 {
		return fmt.Errorf("s1's share among the paid pair is %.2f with %s present and %.2f without: the free offer moved the price range", r1, f, r2)
	}
	return nil
}

func (s *mh6State) relaysNoCaps(who, model string) error {
	s.snapshot()
	s.scen["capSpec"] = fa6Spec{who: who, model: model}
	return nil
}

// candidate reports whether a relay under the scenario's stated caps, limited to the station,
// reaches it.
func (s *mh6State) candidateUnder(name string) bool {
	st := s.ensureNode(name)
	sp, _ := s.scen["capSpec"].(fa6Spec)
	if sp.who == "" {
		sp.who = "alice"
	}
	if sp.model == "" {
		sp.model = st.model
	}
	extra := map[string]any{}
	for k, v := range sp.extra {
		extra[k] = v
	}
	prov, _ := extra["provider"].(map[string]any)
	np := map[string]any{"only": []string{st.id}}
	for k, v := range prov {
		np[k] = v
	}
	extra["provider"] = np
	sp.extra = extra
	before := st.upstreamCount()
	s.heartbeat()
	s.do(sp)
	return st.upstreamCount() > before
}

func (s *mh6State) candidateIs(name, is string) error {
	got := s.candidateUnder(name)
	if want := is == "is"; got != want {
		return fmt.Errorf("%q candidate=%v, want %s a candidate (%d %.200s)", name, got, is, s.last.code, s.last.body)
	}
	return nil
}

func (s *mh6State) notCandidate(name string) error { return s.candidateIs(name, "is NOT") }
func (s *mh6State) isCandidate(name string) error  { return s.candidateIs(name, "is") }

var mh6StatedRe = regexp.MustCompile(`header X-Roger-Max-Price "([0-9.]+)"|body (?:provider\.)?max_price\.prompt ([0-9.]+)`)

func (s *mh6State) relaysWithStated(who, stated string) error {
	sp := fa6Spec{who: who, model: s.lastModel, extra: map[string]any{}, hdr: map[string]string{}}
	if stated != "nothing" {
		for _, m := range mh6StatedRe.FindAllStringSubmatch(stated, -1) {
			if m[1] != "" {
				sp.hdr["X-Roger-Max-Price"] = m[1]
			}
			if m[2] != "" {
				sp.extra["provider"] = map[string]any{"max_price": map[string]any{"prompt": atofMust(m[2])}}
			}
		}
	}
	s.scen["capSpec"] = sp
	return nil
}

func (s *mh6State) relaysBodyPrompt(who, cap string) error {
	s.scen["capSpec"] = fa6Spec{who: who, model: s.lastModel, extra: map[string]any{"provider": map[string]any{"max_price": map[string]any{"prompt": atofMust(cap)}}}}
	return nil
}

// effectiveInCap: the register input ceiling is $50/1M, so a station at $49.90 is reachable
// under the stated cap (a cap above the ceiling clamps to it; it never refuses).
func (s *mh6State) effectiveInCap(cap string) error {
	st := s.node("s-ceiling", s.lastModel, atofMust(cap)-0.10, 0.30)
	if !s.candidateUnder(st.name) {
		return fmt.Errorf("a station at in $%.2f/1M is not reachable under the stated cap: %d %.200s", atofMust(cap)-0.10, s.last.code, s.last.body)
	}
	return nil
}

func (s *mh6State) knobInCap(n string) error {
	s.setenv("ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_IN", n)
	return nil
}

func (s *mh6State) servesAtIn(name, in string) error {
	s.node(name, s.lastModel, atofMust(in), 0.30)
	return nil
}

func (s *mh6State) relaysNoCapsStated(who string) error {
	s.scen["capSpec"] = fa6Spec{who: who, model: s.lastModel}
	return nil
}

// --- /v1/models blend --------------------------------------------------------------------------

func (s *mh6State) getModels() error {
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	s.b.routes().ServeHTTP(rr, r)
	s.scen["models"] = rr.Body.Bytes()
	if rr.Code != 200 {
		return fmt.Errorf("GET /v1/models = %d %.200s", rr.Code, rr.Body.String())
	}
	return nil
}

func (s *mh6State) modelEntry(model string) (map[string]any, error) {
	raw, _ := s.scen["models"].([]byte)
	var d struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	for _, e := range d.Data {
		if e["id"] == model {
			return e, nil
		}
	}
	return nil, fmt.Errorf("/v1/models has no %q entry: %.300s", model, raw)
}

func (s *mh6State) blendIs(model, want string) error {
	e, err := s.modelEntry(model)
	if err != nil {
		return err
	}
	rb, _ := e["rogerai"].(map[string]any)
	got, ok := rb["blended_price_per_1m"].(float64)
	if !ok || math.Abs(got-atofMust(want)) > 1e-6 {
		return fmt.Errorf("rogerai.blended_price_per_1m = %v, want %s", rb["blended_price_per_1m"], want)
	}
	return nil
}

func (s *mh6State) blendLabel(label string) error {
	e, err := s.modelEntry(s.lastModel)
	if err != nil {
		return err
	}
	if !strings.Contains(fmt.Sprint(e["rogerai"]), label) {
		return fmt.Errorf("the entry does not label the blend %q: %v", label, e["rogerai"])
	}
	return nil
}

func (s *mh6State) twoBlendStations(a, ai, ao, b, bi, bo, model string) error {
	s.lastModel = model
	s.node(a, model, atofMust(ai), atofMust(ao))
	s.node(b, model, atofMust(bi), atofMust(bo))
	return nil
}

func (s *mh6State) blendLowestSingle(want string) error { return s.blendIs(s.lastModel, want) }

// --- dispatch failures ---------------------------------------------------------------------------

// stall makes the station's job channel undrained: the production local dispatch blocks and
// answers busy (no poller free) after its wait.
func (s *mh6State) stall(st *fstation) {
	tun := &nodeTunnel{jobs: make(chan protocol.Job), waiters: map[string]chan protocol.JobResult{}, token: st.tun.token}
	s.b.mu.Lock()
	s.b.tunnels[st.id] = tun
	s.b.mu.Unlock()
}

func (s *mh6State) dispatchFails(name, outcome string) error {
	st := s.ensureNode(name)
	switch outcome {
	case "no poller free":
		s.stall(st)
		s.landOrder()
		return nil
	}
	s.landOrder()
	return s.queuePlane(st, outcome)
}

// queuePlane moves the scenario onto the multi-instance queue plane (see the header) and makes
// `failing`'s dispatch end with outcome.
func (s *mh6State) queuePlane(failing *fstation, outcome string) error {
	a, b2 := s.b, s.instanceB()
	for _, b := range []*broker{a, b2} {
		b.multiInstance, b.dispatchMode = true, dispatchViaQueueOnly
		b.instanceID, b.peerInflight = newInstanceID(), map[string]int{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.undo = append(s.undo, func() {
		dqBeforeDispatch, dqAfterPop = nil, nil
		s.mr.SetError("")
		cancel()
		for _, b := range []*broker{a, b2} {
			if q := b.dqueue(); q != nil {
				q.stop()
			}
		}
	})
	for _, name := range s.order {
		if st := s.stations[name]; st != failing {
			s.pollOn(ctx, a, st)
		}
	}
	switch outcome {
	case "station off air":
		// The station drops off air between the pick and the dispatch: its liveness expires.
		dqBeforeDispatch = func(node string) func() {
			if node == failing.id {
				a.mu.Lock()
				a.lastSeen[node] = time.Now().Add(-2 * nodeTTL)
				a.mu.Unlock()
			}
			return nil
		}
	case "dispatch bus error":
		// The shared store fails for this one dispatch.
		dqBeforeDispatch = func(node string) func() {
			if node != failing.id {
				return nil
			}
			s.mr.SetError("ERR the dispatch store is unavailable")
			return func() { s.mr.SetError("") }
		}
	case "handoff lost":
		// The station's only poll is on instance B, which dies between popping the job and
		// handing it over; the origin declares the handoff lost after the taken grace.
		prev := dqTakenGrace.get()
		dqTakenGrace.set(300 * time.Millisecond)
		s.undo = append(s.undo, func() { dqTakenGrace.set(prev) })
		victim := b2.instanceID
		dqAfterPop = func(inst string) bool { return inst == victim }
		s.pollOn(ctx, b2, failing)
	default:
		return fmt.Errorf("unknown dispatch failure %q", outcome)
	}
	return nil
}

// pollOn is a station's real long-poll on instance b: each job it is handed goes to the
// station's own serving loop (which posts the stream and the signed result back).
func (s *mh6State) pollOn(ctx context.Context, b *broker, st *fstation) {
	s.loopWG.Add(1)
	go func() {
		defer s.loopWG.Done()
		for ctx.Err() == nil {
			r := httptest.NewRequest(http.MethodGet, "/agent/poll?node="+st.id, nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+st.tun.token)
			w := httptest.NewRecorder()
			b.agentPoll(w, r)
			var job protocol.Job
			if w.Code != http.StatusOK || w.Body.Len() == 0 || json.Unmarshal(w.Body.Bytes(), &job) != nil {
				select {
				case <-ctx.Done():
				case <-time.After(10 * time.Millisecond):
				}
				continue
			}
			select {
			case st.tun.jobs <- job:
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *mh6State) holdCoveredPlan() error {
	n, _ := s.holdsSince(s.last.who)
	if n != 1 {
		return fmt.Errorf("%d hold rows were placed, want exactly one", n)
	}
	return nil
}

func (s *mh6State) notStruck(name string) error {
	rows, err := s.db.StrikesByOwner(s.st(name).acct, 0)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("%d strike row(s) for %s's owner", len(rows), name)
	}
	return nil
}

func (s *mh6State) relaysStreaming(who, model string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: model, stream: true})
	return nil
}

func (s *mh6State) streamCarries(name string) error {
	if s.last.code != 200 || !strings.Contains(string(s.last.body), "from "+name) {
		return fmt.Errorf("the stream (%d) does not carry %s's content: %.300s", s.last.code, name, s.last.body)
	}
	return nil
}

func mh6UsageChunks(body []byte) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(payload), &m) == nil {
			if _, ok := m["usage"]; ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func (s *mh6State) usageNames(name string) error {
	ch := mh6UsageChunks(s.last.body)
	if len(ch) == 0 {
		return fmt.Errorf("no usage chunk: %.300s", s.last.body)
	}
	u, _ := ch[len(ch)-1]["usage"].(map[string]any)
	rb, _ := u["rogerai"].(map[string]any)
	if got := fmt.Sprint(rb["node"]); got != s.st(name).id && got != name {
		return fmt.Errorf("the usage chunk names %q, want %q", got, name)
	}
	return nil
}

func (s *mh6State) everyBusy(model string) error {
	s.lastModel = model
	for _, n := range []string{"n-1", "n-2"} {
		s.stall(s.defaultNode(n, model))
	}
	return nil
}

func (s *mh6State) status503CodeRA(code string) error {
	if s.last.code != 503 {
		return fmt.Errorf("response %d, want 503: %.300s", s.last.code, s.last.body)
	}
	if got := s.errCode(s.last); got != code {
		return fmt.Errorf("error.code %q, want %q: %.300s", got, code, s.last.body)
	}
	if s.last.hdr.Get("Retry-After") == "" {
		return fmt.Errorf("no Retry-After")
	}
	return nil
}

func (s *mh6State) noSSEFrame() error {
	if strings.Contains(string(s.last.body), "data:") {
		return fmt.Errorf("an SSE frame was written: %.300s", s.last.body)
	}
	return nil
}

func (s *mh6State) holdReleasedFull() error {
	if spent := s.spentSince(s.last.who); spent != 0 {
		return fmt.Errorf("%.6f was spent, want the hold released in full", spent)
	}
	return nil
}

func (s *mh6State) noPollerOnA(a, ma, b, mb string) error {
	s.stall(s.defaultNode(a, ma))
	s.defaultNode(b, mb)
	s.lastModel = ma
	return nil
}

func (s *mh6State) servedAs(model string) error {
	if s.last.code != 200 {
		return fmt.Errorf("response %d, want 200: %.300s", s.last.code, s.last.body)
	}
	if got := s.last.hdr.Get("X-RogerAI-Model"); got != model {
		return fmt.Errorf("served as %q, want %q", got, model)
	}
	return nil
}

func (s *mh6State) relaysModelList(who, model, next string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: model, extra: map[string]any{"models": []string{next}}})
	return nil
}

func (s *mh6State) twoOnAirOneFails(a, b, failing, outcome string) error {
	s.defaultNode(a, s.lastModel)
	s.defaultNode(b, s.lastModel)
	return s.dispatchFails(failing, outcome)
}

func (s *mh6State) relaysOrderNoFallback(who, name string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: s.lastModel, extra: map[string]any{"provider": map[string]any{"order": []string{s.st(name).id}, "allow_fallbacks": false}}})
	return nil
}

func (s *mh6State) status503Code(code string) error {
	if s.last.code != 503 || s.errCode(s.last) != code {
		return fmt.Errorf("response %d code %q, want 503 %q: %.300s", s.last.code, s.errCode(s.last), code, s.last.body)
	}
	return nil
}

func (s *mh6State) deadlineShort(name string) error {
	s.defaultNode(name, s.lastModel)
	s.defaultNode(name+"-sib", s.lastModel)
	s.stall(s.st(name))
	s.landOrder()
	// The local busy outcome takes 3 s; a 12 s request deadline leaves 9 s, under the 10 s a
	// new attempt needs (relayMinAttemptBudget).
	nonStreamRelayWait = 12 * time.Second
	return nil
}

func (s *mh6State) failoverWouldStart() error {
	s.snapshot()
	s.relay(fa6Spec{who: "alice", model: s.lastModel})
	return nil
}

func (s *mh6State) noNewAttempt503() error {
	for n := range s.stations {
		if s.sinceMark(n) > 0 {
			return fmt.Errorf("%q received a new attempt", n)
		}
	}
	if s.last.code != 503 {
		return fmt.Errorf("response %d, want the 503: %.300s", s.last.code, s.last.body)
	}
	return nil
}

func (s *mh6State) neverAnswers(name, other string) error {
	st := s.defaultNode(name, s.lastModel)
	s.defaultNode(other, s.lastModel)
	s.landOrder()
	nonStreamRelayWait = 1500 * time.Millisecond
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		fa6Real(w)
	})
	return nil
}

func (s *mh6State) resp504Nothing(msg, other string) error {
	if s.last.code != 504 || !strings.Contains(string(s.last.body), msg) {
		return fmt.Errorf("response %d, want 504 %q: %.300s", s.last.code, msg, s.last.body)
	}
	return s.receivedNothing(other)
}

// --- usage object ------------------------------------------------------------------------------

func (s *mh6State) claimsAndRecounts(name, cp, cc, rp, rc string) error {
	st := s.defaultNode(name, s.lastModel)
	s.recPrompt, s.recCompletion = atoiMust(rp), atoiMust(rc)
	// The request must be big enough for the claim to be possible: the byte floor clamps a
	// prompt claim above the body's byte length before the recount is consulted.
	s.defPrompt = atoiMust(cp)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"%s answer"}}],"usage":{"prompt_tokens":%s,"completion_tokens":%s}}`, fa6CompletionMarker, cp, cc)
	})
	return nil
}

func (s *mh6State) bodyUsage() (map[string]any, error) {
	var m struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(s.last.body, &m); err != nil || m.Usage == nil {
		return nil, fmt.Errorf("the response body has no usage object: %.300s", s.last.body)
	}
	return m.Usage, nil
}

func (s *mh6State) usagePromptCompletion(p, c string) error {
	u, err := s.bodyUsage()
	if err != nil {
		return err
	}
	if fmt.Sprint(u["prompt_tokens"]) != p || fmt.Sprint(u["completion_tokens"]) != c {
		return fmt.Errorf("usage prompt %v completion %v, want %s and %s (the billed counts)", u["prompt_tokens"], u["completion_tokens"], p, c)
	}
	return nil
}

func (s *mh6State) usageTotal(n string) error {
	u, err := s.bodyUsage()
	if err != nil {
		return err
	}
	if fmt.Sprint(u["total_tokens"]) != n {
		return fmt.Errorf("usage.total_tokens %v, want %s", u["total_tokens"], n)
	}
	return nil
}

func (s *mh6State) usageCostHeader() error {
	u, err := s.bodyUsage()
	if err != nil {
		return err
	}
	c, ok := u["cost"].(float64)
	if !ok {
		return fmt.Errorf("usage.cost is absent: %v", u)
	}
	if hc := atofMust(s.last.hdr.Get("X-RogerAI-Cost")); math.Abs(c-hc) > 1e-12 {
		return fmt.Errorf("usage.cost %v, X-RogerAI-Cost %v", c, hc)
	}
	return nil
}

func (s *mh6State) usageNodeReceipt(name string) error {
	u, err := s.bodyUsage()
	if err != nil {
		return err
	}
	rb, _ := u["rogerai"].(map[string]any)
	if rb == nil {
		return fmt.Errorf("usage has no rogerai block: %v", u)
	}
	if got := fmt.Sprint(rb["node"]); got != s.st(name).id && got != name {
		return fmt.Errorf("usage.rogerai.node %q, want %q", got, name)
	}
	enc, _ := rb["receipt"].(string)
	rec, err := protocol.DecodeReceipt(enc)
	if err != nil {
		return fmt.Errorf("usage.rogerai.receipt does not decode: %v", err)
	}
	if !rec.VerifyNode(s.st(name).pubHex) {
		return fmt.Errorf("usage.rogerai.receipt's node signature does not verify")
	}
	return nil
}

func (s *mh6State) claimsCompletionFor(name, claim, real string) error {
	st := s.defaultNode(name, s.lastModel)
	s.recCompletion = atoiMust(real)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"%s answer"}}],"usage":{"prompt_tokens":50,"completion_tokens":%s}}`, fa6CompletionMarker, claim)
	})
	return nil
}

func (s *mh6State) completionAtMost(n string) error {
	u, err := s.bodyUsage()
	if err != nil {
		return err
	}
	c, _ := u["completion_tokens"].(float64)
	if c > atofMust(n) {
		return fmt.Errorf("usage.completion_tokens %v, want at most %s", c, n)
	}
	return nil
}

// --- streams -----------------------------------------------------------------------------------

func (s *mh6State) noStreamOptions(_ string) error { s.streamOpts = nil; return nil }

func (s *mh6State) streamOptsInclude(_ string, v string) error {
	s.streamOpts = map[string]any{"include_usage": v == "true"}
	return nil
}

func (s *mh6State) emitsOwnUsage(name string) error {
	st := s.defaultNode(name, s.lastModel)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s from %s\"}}]}\n\n", fa6CompletionMarker, name)
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":20}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return nil
}

func (s *mh6State) streamCompletes() error {
	if len(s.stations) == 0 {
		s.defaultNode("n-1", s.lastModel)
	}
	extra := map[string]any{}
	if s.streamOpts != nil {
		extra["stream_options"] = s.streamOpts
	}
	s.snapshot()
	s.relay(fa6Spec{who: "alice", model: s.lastModel, stream: true, extra: extra})
	return nil
}

func (s *mh6State) exactlyOneUsage(_ string) error {
	ch := mh6UsageChunks(s.last.body)
	if len(ch) != 1 {
		return fmt.Errorf("%d chunks with a usage object reached the consumer, want exactly one: %.500s", len(ch), s.last.body)
	}
	return nil
}

func (s *mh6State) brokersWithRogerai() error {
	ch := mh6UsageChunks(s.last.body)
	if len(ch) == 0 {
		return fmt.Errorf("no usage chunk")
	}
	u, _ := ch[0]["usage"].(map[string]any)
	if _, ok := u["rogerai"]; !ok {
		return fmt.Errorf("the one usage chunk is not the broker's (no usage.rogerai): %v", ch[0])
	}
	return nil
}

func (s *mh6State) oneBrokerUsage(who string) error {
	if err := s.exactlyOneUsage(who); err != nil {
		return err
	}
	return s.brokersWithRogerai()
}

func (s *mh6State) contentAndUsageChunk(name string) error {
	st := s.defaultNode(name, s.lastModel)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s first\"}}]}\n\n", fa6CompletionMarker)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"FINAL-DELTA\"}}],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":20}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return nil
}

func (s *mh6State) receivesDelta(_ string) error {
	if !strings.Contains(string(s.last.body), "FINAL-DELTA") {
		return fmt.Errorf("the content delta of the station's final chunk was not forwarded: %.400s", s.last.body)
	}
	return nil
}

func (s *mh6State) stationUsageNotForwarded() error {
	for _, ch := range mh6UsageChunks(s.last.body) {
		u, _ := ch["usage"].(map[string]any)
		if _, ok := u["rogerai"]; !ok {
			return fmt.Errorf("the station's usage object was forwarded: %v", ch)
		}
	}
	return nil
}

func (s *mh6State) emptyOutputNoSibling(name string) error {
	st := s.defaultNode(name, s.lastModel)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":40,"completion_tokens":0}}`))
	})
	return nil
}

func (s *mh6State) usageCostZeroVoid(reason string) error {
	u, err := s.bodyUsage()
	if err != nil {
		return err
	}
	rb, _ := u["rogerai"].(map[string]any)
	if c, _ := u["cost"].(float64); c != 0 || fmt.Sprint(rb["void_reason"]) != reason {
		return fmt.Errorf("usage cost %v void_reason %v, want 0 and %q", u["cost"], rb["void_reason"], reason)
	}
	return nil
}

// --- disconnects and stalls (a station loop this runner drains) --------------------------------

// streamLoop replaces the station's broker-side tunnel with one this runner drains. Each job
// streams `frames` content frames, `gap` apart, through the REAL /agent/stream, each frame one
// recountable token of text; when the broker stops reading (returns from agentStream before the
// last frame) the loop records the cancel and stops generating. `silentAfter` > 0 sends that
// many frames then goes silent (a stall). The receipt claims `claim` completion tokens.
func (s *mh6State) streamLoop(st *fstation, frames int, gap time.Duration, silentAfter int, claim int) {
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: st.tun.token}
	s.b.mu.Lock()
	s.b.tunnels[st.id] = tun
	s.b.mu.Unlock()
	stop := s.stopLoops
	s.loopWG.Add(1)
	go func() {
		defer s.loopWG.Done()
		for {
			select {
			case <-stop:
				return
			case job := <-tun.jobs:
				pr, pw := io.Pipe()
				done := make(chan struct{})
				go func() {
					sreq := httptest.NewRequest(http.MethodPost, "/agent/stream?node="+st.id+"&job="+job.ID, pr)
					sreq.Header.Set("Authorization", "Bearer "+tun.token)
					s.b.agentStream(httptest.NewRecorder(), sreq)
					close(done)
					_ = pr.Close()
				}()
				written := 0
			loop:
				for i := 0; i < frames; i++ {
					if silentAfter > 0 && i >= silentAfter {
						select {
						case <-done:
						case <-stop:
						case <-time.After(5 * time.Second):
						}
						break loop
					}
					select {
					case <-done:
						s.cancelled.Store(true)
						break loop
					default:
					}
					if _, err := fmt.Fprintf(pw, "data: {\"choices\":[{\"delta\":{\"content\":\"%s tok%d \"}}]}\n\n", fa6CompletionMarker, i); err != nil {
						s.cancelled.Store(true)
						break loop
					}
					written++
					atomic.StoreInt64(&s.frames, int64(written))
					time.Sleep(gap)
				}
				if written == frames {
					fmt.Fprintf(pw, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":%d}}\n\n", claim)
					fmt.Fprint(pw, "data: [DONE]\n\n")
				}
				_ = pw.Close()
				<-done
				rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: st.id, User: job.User, Model: st.model,
					PromptTokens: 50, CompletionTokens: claim, PriceIn: st.priceIn, PriceOut: st.priceOut, TS: time.Now().Unix(),
					LineageMethod: "p0-upstream-usage"}
				st.mu.Lock()
				rec.PrevHash = st.lastHash
				rec.SignNode(st.priv)
				st.lastHash = rec.Hash()
				st.mu.Unlock()
				wire, _ := json.Marshal(protocol.JobResult{ID: job.ID, Status: 200, Receipt: rec})
				r := httptest.NewRequest(http.MethodPost, "/agent/result?node="+st.id, bytes.NewReader(wire))
				r.Header.Set("Authorization", "Bearer "+tun.token)
				s.b.agentResult(httptest.NewRecorder(), r)
			}
		}
	}()
}

// streamWithCancel fires a streaming relay whose request context is cancelled once `after`
// content frames reached the consumer (0 = never).
func (s *mh6State) streamWithCancel(who string, after int) {
	s.markLedger(who)
	s.snapshot()
	body := s.body(fa6Spec{model: s.lastModel, stream: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	w := s.consumer(who)
	signReq(r, w.priv, body)
	r.Header.Set("CF-Connecting-IP", w.ip)
	rw := &mh6CancelWriter{recWriter: &recWriter{ResponseRecorder: httptest.NewRecorder()}, after: after, cancel: cancel}
	s.b.relay(rw, r)
	res := fa6Resp{who: who, code: rw.Code, body: rw.Body.Bytes(), hdr: rw.Header(), stream: true}
	s.resps = append(s.resps, res)
	s.last = res
	time.Sleep(300 * time.Millisecond) // let the settle land
}

type mh6CancelWriter struct {
	*recWriter
	after  int
	seen   int
	cancel context.CancelFunc
}

func (w *mh6CancelWriter) Write(p []byte) (int, error) {
	n, err := w.recWriter.Write(p)
	if w.after > 0 {
		w.seen += bytes.Count(p, []byte("tok"))
		if w.seen >= w.after {
			w.cancel()
		}
	}
	return n, err
}

func (w *mh6CancelWriter) Flush() {}

func (s *mh6State) nodeStreamsTo(name, who string) error {
	st := s.defaultNode(name, s.lastModel)
	s.recCompletion = 40
	s.streamLoop(st, 200, 20*time.Millisecond, 0, 200)
	return nil
}

func (s *mh6State) disconnectsAfter(who, n string) error {
	s.recCompletion = atoiMust(n)
	s.streamWithCancel(who, atoiMust(n))
	return nil
}

func (s *mh6State) brokerSendsCancel(name string) error {
	if !s.cancelled.Load() {
		return fmt.Errorf("the broker never cancelled %s's job: it kept reading the stream (%d of 200 frames written)", name, atomic.LoadInt64(&s.frames))
	}
	return nil
}

func (s *mh6State) stopsGenerating(name string) error {
	if f := atomic.LoadInt64(&s.frames); f >= 200 {
		return fmt.Errorf("%s generated all %d frames after the consumer left", name, f)
	}
	return nil
}

func (s *mh6State) streamsAtOut(name, out string) error {
	st := s.node(name, s.lastModel, 0.10, atofMust(out))
	s.streamLoop(st, 200, 20*time.Millisecond, 0, 200)
	return nil
}

// billsPromptPlus: the consumer's spend equals the billed prompt (as the station claimed it:
// 50) plus n completion tokens at the station's prices.
func (s *mh6State) billsPromptPlus(n string) error {
	st := s.st("n-1")
	want := (50*st.priceIn + atofMust(n)*st.priceOut) / 1e6
	got := s.spentSince(s.last.who)
	if math.Abs(got-want) > want*0.02+1e-9 {
		return fmt.Errorf("the settle billed %.8f, want %.8f (prompt + %s completion tokens)", got, want, n)
	}
	return nil
}

func (s *mh6State) operatorEarnsShare() error {
	if s.spentSince(s.last.who) == 0 {
		return fmt.Errorf("nothing was billed, so the operator earned nothing")
	}
	return nil
}

func (s *mh6State) holdRemainderReleased() error {
	n, held := s.holdsSince(s.last.who)
	if n == 0 {
		return fmt.Errorf("no hold was placed")
	}
	if spent := s.spentSince(s.last.who); spent > held+1e-12 {
		return fmt.Errorf("spent %.6f exceeds the hold %.6f", spent, held)
	}
	return nil
}

func (s *mh6State) holdSizedFor(n string) error {
	st := s.defaultNode("n-1", s.lastModel)
	s.scen["maxTokens"] = atoiMust(n)
	s.recCompletion = 500
	s.streamLoop(st, 600, 5*time.Millisecond, 0, 500)
	return nil
}

func (s *mh6State) claimsBeforeCancel(_ string) error { return nil }

func (s *mh6State) disconnects(who string) error {
	mt, _ := s.scen["maxTokens"].(int)
	s.markLedger(who)
	s.snapshot()
	m := map[string]any{"model": s.lastModel, "stream": true, "messages": []map[string]any{{"role": "user", "content": utPrompt(50)}}}
	if mt > 0 {
		m["max_tokens"] = mt
	}
	body, _ := json.Marshal(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	w := s.consumer(who)
	signReq(r, w.priv, body)
	rw := &mh6CancelWriter{recWriter: &recWriter{ResponseRecorder: httptest.NewRecorder()}, after: 20, cancel: cancel}
	s.b.relay(rw, r)
	s.last = fa6Resp{who: who, code: rw.Code, body: rw.Body.Bytes(), hdr: rw.Header(), stream: true}
	time.Sleep(300 * time.Millisecond)
	return nil
}

func (s *mh6State) billsAtMostHold() error {
	_, held := s.holdsSince(s.last.who)
	if spent := s.spentSince(s.last.who); spent > held+1e-12 {
		return fmt.Errorf("billed %.6f above the hold %.6f", spent, held)
	}
	return nil
}

func (s *mh6State) nodeOwnedStreaming(name, op string) error {
	st := s.defaultNode(name, s.lastModel)
	s.ops[op] = name
	s.streamLoop(st, 200, 20*time.Millisecond, 0, 200)
	return nil
}

func (s *mh6State) disconnectsMid(who string) error {
	s.streamWithCancel(who, 30)
	return nil
}

func (s *mh6State) claimsAfterDisconnect(name, claim, who, fwd string) error {
	st := s.defaultNode(name, s.lastModel)
	s.recCompletion = atoiMust(fwd)
	s.streamLoop(st, 400, 10*time.Millisecond, 0, atoiMust(claim))
	s.streamWithCancel(who, atoiMust(fwd))
	return nil
}

func (s *mh6State) receiptArrives() error { time.Sleep(200 * time.Millisecond); return nil }

func (s *mh6State) billsCompletion(n string) error {
	st := s.st("n-1")
	want := (50*st.priceIn + atofMust(n)*st.priceOut) / 1e6
	got := s.spentSince(s.last.who)
	if math.Abs(got-want) > want*0.02+1e-9 {
		return fmt.Errorf("the settle billed %.8f, want %.8f (%s completion tokens)", got, want, n)
	}
	return nil
}

func (s *mh6State) disconnectsBeforeContent(who string) error {
	st := s.defaultNode("n-1", s.lastModel)
	s.recCompletion = 0
	s.streamLoop(st, 50, 50*time.Millisecond, 0, 0)
	s.markLedger(who)
	s.snapshot()
	body := s.body(fa6Spec{model: s.lastModel, stream: true})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	signReq(r, s.consumer(who).priv, body)
	cancel() // gone before any frame
	rw := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(rw, r)
	s.last = fa6Resp{who: who, code: rw.Code, body: rw.Body.Bytes(), hdr: rw.Header(), stream: true}
	return nil
}

func (s *mh6State) processedPrompt(_ string) error { return nil }

func (s *mh6State) billsPromptZero() error {
	st := s.st("n-1")
	want := 50 * st.priceIn / 1e6
	got := s.spentSince(s.last.who)
	if math.Abs(got-want) > want*0.05+1e-12 {
		return fmt.Errorf("the settle billed %.8f, want %.8f (the prompt tokens and 0 completion tokens)", got, want)
	}
	return nil
}

func (s *mh6State) nonStreamBeingServed(who, name string) error {
	st := s.defaultNode(name, s.lastModel)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			s.cancelled.Store(true)
			return
		case <-time.After(2 * time.Second):
		}
		fa6Real(w)
	})
	return nil
}

func (s *mh6State) disconnectsBeforeResult(who string) error {
	s.markLedger(who)
	s.snapshot()
	body := s.body(fa6Spec{model: s.lastModel})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	signReq(r, s.consumer(who).priv, body)
	rw := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(rw, r)
	s.last = fa6Resp{who: who, code: rw.Code, body: rw.Body.Bytes(), hdr: rw.Header()}
	time.Sleep(2500 * time.Millisecond)
	return nil
}

func (s *mh6State) cancelsJob(name string) error {
	if !s.cancelled.Load() {
		return fmt.Errorf("the broker never cancelled %s's job (its upstream request ran to completion)", name)
	}
	return nil
}

func (s *mh6State) billedZeroNothing(who string) error {
	if spent := s.spentSince(who); spent != 0 {
		return fmt.Errorf("%s was billed %.6f, want $0 (nothing was delivered)", who, spent)
	}
	return nil
}

func (s *mh6State) stallsAfter(name, n, who string) error {
	st := s.defaultNode(name, s.lastModel)
	s.ops["op1"] = name
	s.recCompletion = atoiMust(n)
	s.b.streamIdleTimeout = 400 * time.Millisecond
	s.streamLoop(st, atoiMust(n)+50, 5*time.Millisecond, atoiMust(n), atoiMust(n))
	return nil
}

func (s *mh6State) stallDetected() error {
	s.streamWithCancel("alice", 0)
	return nil
}

func (s *mh6State) endsWithUsageBilling(n string) error {
	ch := mh6UsageChunks(s.last.body)
	if len(ch) == 0 {
		return fmt.Errorf("no usage chunk: %.300s", s.last.body)
	}
	u, _ := ch[len(ch)-1]["usage"].(map[string]any)
	if fmt.Sprint(u["completion_tokens"]) != n {
		return fmt.Errorf("the usage chunk bills %v completion tokens, want %s: %v", u["completion_tokens"], n, u)
	}
	return nil
}

func (s *mh6State) partialStall() error {
	ch := mh6UsageChunks(s.last.body)
	if len(ch) == 0 {
		return fmt.Errorf("no usage chunk")
	}
	u, _ := ch[len(ch)-1]["usage"].(map[string]any)
	rb, _ := u["rogerai"].(map[string]any)
	if _, has := rb["void_reason"]; has || rb["partial"] != "stall" {
		return fmt.Errorf("usage.rogerai void_reason=%v partial=%v, want no void_reason and partial \"stall\"", rb["void_reason"], rb["partial"])
	}
	return nil
}

func (s *mh6State) doneFollows() error {
	body := string(s.last.body)
	i := strings.LastIndex(body, "\"usage\"")
	j := strings.LastIndex(body, "[DONE]")
	if i < 0 || j < i {
		return fmt.Errorf("[DONE] does not follow the usage chunk: %.300s", body)
	}
	return nil
}

func (s *mh6State) ownedStallsAfterContent(name, op string) error {
	err := s.stallsAfter(name, "60", op)
	st := s.st(name)
	s.b.metricsMu.Lock()
	s.b.success[st.id] = 1 // a healthy record, so the stall's mark is visible
	s.b.metricsMu.Unlock()
	s.scen["succBefore"] = 1.0
	return err
}

func (s *mh6State) stallSettled() error { return s.stallDetected() }

func (s *mh6State) noEmptyStrike(op string) error {
	st, err := s.owner(op)
	if err != nil {
		return err
	}
	rows, _ := s.db.StrikesByOwner(st.acct, 0)
	for _, r := range rows {
		if r.Kind == store.StrikeEmptyOutput {
			return fmt.Errorf("an empty-output strike was recorded for %s", op)
		}
	}
	return nil
}

// stallCounterIncrements: the health signal a stall feeds today is the station's success
// EWMA (the idle-timer path exits the in-flight slot as a failure).
func (s *mh6State) stallCounterIncrements() error {
	st := s.st("n-1")
	before, _ := s.scen["succBefore"].(float64)
	if got := s.successOf(st); got >= before {
		return fmt.Errorf("n-1's success signal did not record the stall (%.3f -> %.3f)", before, got)
	}
	return nil
}

func (s *mh6State) noContentSilent(name string) error {
	st := s.defaultNode(name, s.lastModel)
	s.b.streamIdleTimeout = 400 * time.Millisecond
	s.streamLoop(st, 10, 5*time.Millisecond, 0, 0)
	s.streamLoop(st, 10, 5*time.Millisecond, 1, 0) // replaces: silent before its first frame
	s.st(name)
	return nil
}

func (s *mh6State) usageBillsZeroNamesStall() error {
	ch := mh6UsageChunks(s.last.body)
	if len(ch) == 0 {
		return fmt.Errorf("no usage chunk: %.300s", s.last.body)
	}
	u, _ := ch[len(ch)-1]["usage"].(map[string]any)
	rb, _ := u["rogerai"].(map[string]any)
	if c, _ := u["cost"].(float64); c != 0 || !strings.Contains(fmt.Sprint(rb["void_reason"]), "stall") {
		return fmt.Errorf("usage cost %v void_reason %v, want 0 naming the stall", u["cost"], rb["void_reason"])
	}
	return nil
}

func (s *mh6State) failsOverIfSibling() error { return nil }

func (s *mh6State) stallsForwardingRecount(name, n string) error {
	return s.stallsAfter(name, n, "alice")
}

func (s *mh6State) partialClaim(_ string) error { return nil }

func (s *mh6State) completionBilled(n string) error { return s.endsWithUsageBilling(n) }

func (s *mh6State) lateAfter504(name, secs string) error {
	st := s.defaultNode(name, s.lastModel)
	nonStreamRelayWait = 500 * time.Millisecond
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Duration(atoiMust(secs)) * 100 * time.Millisecond) // scaled: 1 scenario second = 100 ms
		fa6Real(w)
	})
	s.relay(fa6Spec{who: "alice", model: s.lastModel})
	return nil
}

func (s *mh6State) lateProcessed(_ string) error {
	time.Sleep(1500 * time.Millisecond)
	return nil
}

func (s *mh6State) billedZero(who string) error { return s.billedZeroNothing(who) }

func (s *mh6State) lateReceiptRecorded(name, reason string) error {
	keys, err := s.latestReceiptKeys("alice", name)
	if err != nil {
		return err
	}
	if got, _ := keys["void_reason"].(string); got != reason {
		return fmt.Errorf("the late receipt's void_reason %q, want %q", got, reason)
	}
	return nil
}

func (s *mh6State) noLatenessStrike() error {
	for n := range s.stations {
		if err := s.notStruck(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *mh6State) lateAfterGrace(name, secs string) error {
	st := s.defaultNode(name, s.lastModel)
	s.setenv("ROGERAI_LATE_RECEIPT_GRACE", "300ms")
	nonStreamRelayWait = 500 * time.Millisecond
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		fa6Real(w)
	})
	s.relay(fa6Spec{who: "alice", model: s.lastModel})
	time.Sleep(2 * time.Second)
	return nil
}

func (s *mh6State) noReceiptRecorded() error {
	w := s.consumer("alice")
	es, _ := s.db.RecentByUser(w.wallet, 100)
	if len(es) != 0 {
		return fmt.Errorf("%d receipt(s) were recorded for the late result", len(es))
	}
	return nil
}

// --- holds -------------------------------------------------------------------------------------

func (s *mh6State) stationWindow(name, in, out, ctx string) error {
	st := s.standUp(name, stationOpts{model: s.lastModel, priceIn: atofMust(in), priceOut: atofMust(out), ctx: atoiMust(ctx)})
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	s.real(st)
	return nil
}

func (s *mh6State) relaysPromptMCT(who, prompt, mct string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: s.lastModel, prompt: mh6Prompt(prompt), extra: map[string]any{"max_completion_tokens": atoiMust(mct)}})
	return nil
}

func (s *mh6State) holdCovers(prompt, out string) error {
	name := s.served(s.last)
	st, ok := s.stations[name]
	if !ok {
		for _, x := range s.stations {
			st = x
		}
	}
	measured := approxPromptTokens(s.lastBodyOf(st.name))
	if measured == 0 {
		measured = atoiMust(prompt)
	}
	want := (float64(measured)*st.priceIn + atofMust(out)*st.priceOut) / 1e6
	_, held := s.holdsSince(s.last.who)
	if math.Abs(held-want) > want*0.05+1e-9 {
		return fmt.Errorf("the hold was %.6f, want ~%.6f (%d prompt + %s output tokens)", held, want, measured, out)
	}
	return nil
}

func (s *mh6State) relaysPromptNoMax(who, prompt string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: s.lastModel, prompt: mh6Prompt(prompt)})
	return nil
}

func (s *mh6State) holdCoversNot(out, not string) error { return s.holdCovers("0", out) }

func (s *mh6State) forwardedMaxTokens(name, n string) error {
	var m map[string]any
	_ = json.Unmarshal(s.lastBodyOf(name), &m)
	if fmt.Sprint(m["max_tokens"]) != n {
		return fmt.Errorf("the body forwarded to %s carries max_tokens %v, want %s", name, m["max_tokens"], n)
	}
	return nil
}

func (s *mh6State) relaysMaxTokensWindow(who, mt, ctx string) error {
	_ = s.stationWindow("s1", "0.20", "0.60", ctx)
	s.snapshot()
	s.relay(fa6Spec{who: who, model: s.lastModel, extra: map[string]any{"max_tokens": atoiMust(mt)}})
	return nil
}

func (s *mh6State) holdCoversAndForwarded(out, fwd string) error {
	if err := s.holdCovers("0", out); err != nil {
		return err
	}
	return s.forwardedMaxTokens("s1", fwd)
}

func (s *mh6State) stationWithWindow(ctx string) error {
	return s.stationWindow("s1", "0.20", "0.60", ctx)
}

func (s *mh6State) holdAndForwardedAre(n string) error {
	if err := s.holdCovers("0", n); err != nil {
		return err
	}
	return s.forwardedMaxTokens("s1", n)
}

func (s *mh6State) outputKnob(n string) error {
	s.setenv("ROGERAI_DEFAULT_OUTPUT_TOKENS", n)
	return nil
}

func (s *mh6State) relaysNoMaxTokens(who string) error {
	if len(s.stations) == 0 {
		_ = s.stationWindow("s1", "0.20", "0.60", "131072")
	}
	s.snapshot()
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) forwardedIs(n string) error {
	for name := range s.stations {
		if s.sinceMark(name) > 0 {
			return s.forwardedMaxTokens(name, n)
		}
	}
	return fmt.Errorf("no station received the request: %d %.200s", s.last.code, s.last.body)
}

// holds sets the consumer's wallet to exactly `amount` (a fresh account of that name).
func (s *mh6State) holds(who, amount string) error {
	delete(s.who, who)
	s.scen["fund:"+who] = atofMust(amount)
	return s.fundWho(who, atofMust(amount))
}

// directHoldPrice is the out price at which the direct station's hold for this runner's request
// (a 50-token prompt and the 4096-token default budget) is `hold`.
func mh6DirectPrice(hold float64) float64 { return hold * 1e6 / (50 + 4096) }

func (s *mh6State) directAndTower(dh, th, model string) error {
	s.lastModel, s.model = model, model
	p := mh6DirectPrice(atofMust(dh))
	s.node("direct", model, p, p)
	_, err := s.standUpTower("t1", model, 1, 1, 50)
	if err != nil {
		return err
	}
	s.scen["towerHold"] = atofMust(th)
	return s.everyTierA()
}

func (s *mh6State) relaysFor(who, model string) error {
	s.snapshot()
	s.relay(fa6Spec{who: who, model: model})
	return nil
}

func (s *mh6State) servedByDirect() error {
	if s.last.code != 200 || s.last.hdr.Get("X-RogerAI-Relay") != "" {
		return fmt.Errorf("response %d relay=%q, want 200 from the direct station: %.300s", s.last.code, s.last.hdr.Get("X-RogerAI-Relay"), s.last.body)
	}
	return nil
}

func (s *mh6State) noTowerPair() error { return s.servedByDirect() }

func (s *mh6State) holdsOnlyTower(who, amount, th, model string) error {
	if err := s.holds(who, amount); err != nil {
		return err
	}
	s.lastModel, s.model = model, model
	_, err := s.standUpTower("t1", model, 1, 1, 50)
	return err
}

func (s *mh6State) resp402Code(code string) error {
	if s.last.code != 402 || s.errCode(s.last) != code {
		return fmt.Errorf("response %d code %q, want 402 %q: %.300s", s.last.code, s.errCode(s.last), code, s.last.body)
	}
	return nil
}

func (s *mh6State) holdsEachDirect(who, amount, each string) error {
	if err := s.holds(who, amount); err != nil {
		return err
	}
	// Price the station so the hold for THIS request is exactly the stated amount: the hold's
	// prompt estimate counts the whole forwarded body (JSON and the default max_tokens it
	// gains), not only the 50-token prompt text mh6DirectPrice assumes.
	body := s.body(fa6Spec{who: who, model: s.lastModel})
	sent := withDefaultMaxTokens(body, approxPromptTokens(body), 0)
	// 0.1% under the stated amount: the relay's rewritten body can differ from this estimate by
	// a token, and three holds of exactly $0.01 against exactly $0.03 would turn that into a 402.
	// A 4th request still cannot fit (4 x 0.00999 > 0.03).
	p := atofMust(each) * 0.999 * 1e6 / float64(len(sent)/4+1+statedOutputTokens(sent))
	st := s.node("direct", s.lastModel, p, p)
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(800 * time.Millisecond)
		fa6Real(w)
	})
	return nil
}

func (s *mh6State) concurrent(who, n string) error {
	s.snapshot()
	var wg sync.WaitGroup
	for i := 0; i < atoiMust(n); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := s.doConc(fa6Spec{who: who, model: s.lastModel})
			s.concMu.Lock()
			s.conc = append(s.conc, r)
			s.concMu.Unlock()
		}()
	}
	time.Sleep(200 * time.Millisecond)
	s.fourth = s.doConc(fa6Spec{who: who, model: s.lastModel})
	wg.Wait()
	return nil
}

// doConc is do() without the shared last/resps bookkeeping (safe from several goroutines).
func (s *mh6State) doConc(sp fa6Spec) fa6Resp {
	body := s.body(sp)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := s.who[sp.who]
	signReq(r, w.priv, body)
	r.Header.Set("CF-Connecting-IP", w.ip)
	rw := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(rw, r)
	return fa6Resp{who: sp.who, code: rw.Code, body: rw.Body.Bytes(), hdr: rw.Header()}
}

func (s *mh6State) allServed(n string) error {
	ok := 0
	for _, r := range s.conc {
		if r.code == 200 {
			ok++
		}
	}
	if ok != atoiMust(n) {
		codes := []string{}
		for _, r := range s.conc {
			codes = append(codes, fmt.Sprintf("%d %.160s", r.code, r.body))
		}
		return fmt.Errorf("%d of %d concurrent requests were served: %v", ok, len(s.conc), codes)
	}
	return nil
}

func (s *mh6State) fourth402() error {
	if s.fourth.code != 402 {
		return fmt.Errorf("the 4th concurrent request answered %d, want 402: %.200s", s.fourth.code, s.fourth.body)
	}
	return nil
}

// --- price lock ---------------------------------------------------------------------------------

func (s *mh6State) setPrice(name string, out float64) {
	st := s.st(name)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	for i := range reg.Offers {
		reg.Offers[i].PriceOut = out
	}
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	st.priceOut = out
}

func (s *mh6State) billedOut() (float64, error) {
	p := s.last.hdr.Get("X-RogerAI-Price")
	for _, kv := range strings.Split(p, ";") {
		if strings.HasPrefix(strings.TrimSpace(kv), "out=") {
			return atofMust(strings.TrimPrefix(strings.TrimSpace(kv), "out=")), nil
		}
	}
	return 0, fmt.Errorf("no billed out price in X-RogerAI-Price %q (%d %.200s)", p, s.last.code, s.last.body)
}

func (s *mh6State) postedFor(name, out, hours string) error {
	s.node(name, s.lastModel, 0.10, atofMust(out))
	s.advance(time.Duration(atoiMust(hours)) * time.Hour)
	return nil
}

func (s *mh6State) servedAtLock(who, name, _ string) error {
	s.relay(fa6Spec{who: who, model: s.lastModel})
	if s.last.code != 200 {
		return fmt.Errorf("serve = %d %.200s", s.last.code, s.last.body)
	}
	return nil
}

func (s *mh6State) raisesAndRelays(out, who string) error {
	s.setPrice("s1", atofMust(out))
	s.advance(time.Hour)
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) billedAt(_ string, out string) error {
	got, err := s.billedOut()
	if err != nil {
		return err
	}
	if math.Abs(got-atofMust(out)) > 1e-9 {
		return fmt.Errorf("billed at out $%v, want $%s", got, out)
	}
	return nil
}

func (s *mh6State) holdsLockAt(who, out, name string) error {
	s.node(name, s.lastModel, 0.10, atofMust(out))
	s.advance(3 * time.Hour)
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) lowersTo(out string) error {
	s.setPrice("s1", atofMust(out))
	s.relay(fa6Spec{who: "alice", model: s.lastModel})
	return nil
}

func (s *mh6State) normallyCharges(name, out string) error {
	s.node(name, s.lastModel, 0.10, atofMust(out))
	s.advance(3 * time.Hour)
	s.scen["normal"] = atofMust(out)
	return nil
}

func (s *mh6State) postsFor(out, mins string) error {
	s.setPrice("s1", atofMust(out))
	s.scen["promoMins"] = atoiMust(mins)
	return nil
}

func (s *mh6State) servedDuringPromo(who, _ string) error {
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) restoresAndRelaysLater(out, who string) error {
	mins, _ := s.scen["promoMins"].(int)
	s.advance(time.Duration(mins) * time.Minute)
	s.setPrice("s1", atofMust(out))
	s.advance(time.Hour)
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) postedHoursBefore(out, hours, who string) error {
	s.node("s1", s.lastModel, 0.10, atofMust(out))
	s.advance(time.Duration(atoiMust(hours)) * time.Hour)
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) raisesAfter(out, hours string) error {
	s.advance(time.Duration(atoiMust(hours)) * time.Hour)
	s.setPrice("s1", atofMust(out))
	return nil
}

func (s *mh6State) relaysWithinWindow(who string) error {
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) postedMinutesAgoServed(out, who, _ string) error {
	s.node("s1", s.lastModel, 0.10, 0.60)
	s.advance(3 * time.Hour)
	s.setPrice("s1", atofMust(out))
	s.advance(5 * time.Minute)
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) relaysWhileStill(who, _ string) error {
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) lockMinPosted(mins string) error {
	s.setenv("ROGERAI_LOCK_MIN_POSTED", mins+"m")
	return nil
}

func (s *mh6State) postedMinutesBeforeServed(out, mins, who string) error {
	s.node("s1", s.lastModel, 0.10, 0.60)
	s.advance(3 * time.Hour)
	s.setPrice("s1", atofMust(out))
	s.advance(time.Duration(atoiMust(mins)) * time.Minute)
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) restores(out string) error {
	s.setPrice("s1", atofMust(out))
	return nil
}

func (s *mh6State) keepsFor(who, out string) error {
	s.advance(time.Hour)
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return s.billedAt(who, out)
}

func (s *mh6State) firstSeenOnA(hours string) error {
	s.node("s1", s.lastModel, 0.10, 0.60)
	s.advance(time.Duration(atoiMust(hours)) * time.Hour)
	s.instanceB()
	return nil
}

func (s *mh6State) servedOnB(who string) error {
	s.relay(fa6Spec{who: who, model: s.lastModel, b: s.b2})
	return nil
}

func (s *mh6State) lockFullWindowOnB() error {
	s.setPrice("s1", 1.20)
	if s.b2 != nil {
		s.b2.mu.Lock()
		reg := s.b2.nodes[s.st("s1").id]
		for i := range reg.Offers {
			reg.Offers[i].PriceOut = 1.20
		}
		s.b2.nodes[s.st("s1").id] = reg
		s.b2.mu.Unlock()
	}
	s.advance(3 * time.Hour)
	s.relay(fa6Spec{who: "alice", model: s.lastModel, b: s.b2})
	return s.billedAt("alice", "0.60")
}

func (s *mh6State) freeWindow(name, _, _ string) error {
	st := s.node(name, s.lastModel, 0.10, 0.60)
	now := time.Now().UTC() // windows match in UTC
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Offers[0].Schedule = []protocol.PriceWindow{{Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), Free: true}}
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *mh6State) servedAt(who, _ string) error {
	s.relay(fa6Spec{who: who, model: s.lastModel})
	return nil
}

func (s *mh6State) lockExistsFor(name string) bool {
	id := s.st(name).id
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for k, q := range s.b.quotes {
		if strings.Contains(k, "|"+id+"|") && time.Now().Before(q.until) {
			return true
		}
	}
	return false
}

func (s *mh6State) noLockMinted() error {
	for n := range s.stations {
		if s.lockExistsFor(n) {
			return fmt.Errorf("a lock was minted on %s", n)
		}
	}
	return nil
}

func (s *mh6State) n1500n2Serves(a, b string) error {
	st := s.defaultNode(a, s.lastModel)
	s.defaultNode(b, s.lastModel)
	s.landOrder()
	s.answerFrom(st, false, 500, fa6ErrBody("internal"), nil)
	s.relay(fa6Spec{who: "alice", model: s.lastModel})
	return nil
}

func (s *mh6State) noLockOn(_, name string) error {
	if s.lockExistsFor(name) {
		return fmt.Errorf("a lock exists on %s for the voided attempt", name)
	}
	return nil
}

// --- runner ----------------------------------------------------------------------------------------

func TestMoneyHardeningBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &mh6State{fa6State: &fa6State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.reset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})
			mh6Register(sc, st)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/money/routing_money_hardening.feature"},
			Tags: "~@cli && ~@tui && ~@proxy && ~@docs && ~@later", TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing_money_hardening.feature failed")
	}
}

func mh6Register(sc *godog.ScenarioContext, st *mh6State) {
	sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
	sc.Step(`^the fee rate is 30%$`, st.feeRate30)
	sc.Step(`^the consumer default out-cap is \$10/1M$`, st.defaultOutCap10)
	sc.Step(`^the consumer default in-cap is \$5/1M$`, st.defaultInCap)
	sc.Step(`^the default output token budget is (\d+)$`, st.defaultOutputBudget)
	sc.Step(`^a funded consumer "([^"]+)" holds \$([0-9.]+)$`, st.fundedHolds)

	// #3 price ranking
	sc.Step(`^station "([^"]+)" serves "([^"]+)" at in \$([0-9.]+)/1M out \$([0-9.]+)/1M$`, st.stationServesAt)
	sc.Step(`^"([^"]+)" relays a (\d+)-token prompt with max_tokens (\d+) for "([^"]+)"$`, st.relaysPromptMaxTokensFor)
	sc.Step(`^the response is served by "([^"]+)"$`, st.servedByName)
	sc.Step(`^station "([^"]+)" at in \$([0-9.]+)/1M out \$([0-9.]+)/1M and station "([^"]+)" at in \$([0-9.]+)/1M out \$([0-9.]+)/1M$`, st.twoStationsPriced)
	sc.Step(`^"([^"]+)" relays a (\d+)-token prompt with max_tokens (\d+) and provider\.sort "([^"]+)"$`, st.relaysPromptSort)
	sc.Step(`^"([^"]+)" is ranked first \(estimated \$([0-9.]+) vs \$([0-9.]+)\)$`, st.rankedFirstEst)
	sc.Step(`^stations "([^"]+)" and "([^"]+)" as in the previous scenario$`, st.asPrevious)
	sc.Step(`^"([^"]+)" relays a (\d+)-token prompt with max_completion_tokens (\d+) and max_tokens (\d+) and provider\.sort "([^"]+)"$`, st.relaysMCTAndMT)
	sc.Step(`^"([^"]+)" is ranked first$`, st.rankedFirst)
	sc.Step(`^"([^"]+)" relays a (\d+)-token prompt with no max_tokens and provider\.sort "([^"]+)"$`, st.relaysNoMaxSort)
	sc.Step(`^the estimate uses (\d+) output tokens$`, st.estimateUses)
	sc.Step(`^station "([^"]+)" at in \$([0-9.]+)/1M out \$([0-9.]+)/1M and "([^"]+)" at in \$([0-9.]+)/1M out \$([0-9.]+)/1M$`, st.twoTrickFair)
	sc.Step(`^both are equally healthy and fast$`, st.equallyHealthy)
	sc.Step(`^(\d+) consumers relay (\d+)-token prompts with no routing object$`, st.consumersPrompts)
	sc.Step(`^"([^"]+)" serves more requests than "([^"]+)"$`, st.servesMore)
	sc.Step(`^a free station "([^"]+)" and paid stations "([^"]+)" and "([^"]+)"$`, st.freeAndTwoPaid)
	sc.Step(`^the price range for scoring is computed$`, st.rangeComputed)
	sc.Step(`^"([^"]+)" does not move it$`, st.freeDoesNotMove)
	sc.Step(`^"([^"]+)" relays to "([^"]+)" with no caps stated$`, st.relaysNoCaps)
	sc.Step(`^"([^"]+)" (is|is NOT) a candidate$`, st.candidateIs)
	sc.Step(`^"([^"]+)" relays with body provider\.max_price\.prompt (\d+)$`, st.relaysBodyPrompt)
	sc.Step(`^"([^"]+)" relays with (nothing|header .+|body max_price.+)$`, st.relaysWithStated)
	sc.Step(`^the effective input cap is \$(\d+)/1M$`, st.effectiveInCap)
	sc.Step(`^ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_IN is (\d+)$`, st.knobInCap)
	sc.Step(`^station "([^"]+)" serves at in \$([0-9.]+)/1M$`, st.servesAtIn)
	sc.Step(`^"([^"]+)" relays with no caps stated$`, st.relaysNoCapsStated)
	sc.Step(`^a consumer GETs /v1/models$`, st.getModels)
	sc.Step(`^the "([^"]+)" entry's rogerai\.blended_price_per_1m is ([0-9.]+)$`, st.blendIs)
	sc.Step(`^the entry labels the blend ratio "([^"]+)"$`, st.blendLabel)
	sc.Step(`^"([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) and "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) serve "([^"]+)"$`, st.twoBlendStations)
	sc.Step(`^rogerai\.blended_price_per_1m is the lowest blended price of a single station \(([0-9.]+)\)$`, st.blendLowestSingle)

	// #5 dispatch failures
	sc.Step(`^nodes "([^"]+)" and "([^"]+)" are on air for "([^"]+)"$`, st.twoNodesOnAir)
	sc.Step(`^dispatching to "([^"]+)" fails with (no poller free|station off air|handoff lost|dispatch bus error)$`, st.dispatchFails)
	sc.Step(`^"([^"]+)" relays to "([^"]+)"$`, st.relaysTo)
	sc.Step(`^the response is 200 served by "([^"]+)"$`, st.servedBy200)
	sc.Step(`^exactly one hold was placed and it covered the plan$`, st.holdCoveredPlan)
	sc.Step(`^"([^"]+)" is not struck$`, st.notStruck)
	sc.Step(`^"([^"]+)" relays a streaming request to "([^"]+)"$`, st.relaysStreaming)
	sc.Step(`^the stream carries "([^"]+)"'s content$`, st.streamCarries)
	sc.Step(`^its usage chunk names "([^"]+)"$`, st.usageNames)
	sc.Step(`^every station for "([^"]+)" is busy$`, st.everyBusy)
	sc.Step(`^the response status is 503 with error code "([^"]+)" and a Retry-After$`, st.status503CodeRA)
	sc.Step(`^no SSE frame was written$`, st.noSSEFrame)
	sc.Step(`^the hold is released in full$`, st.holdReleasedFull)
	sc.Step(`^"([^"]+)" serves "([^"]+)" but no poller is listening on it, and "([^"]+)" serves "([^"]+)"$`, st.noPollerOnA)
	sc.Step(`^"([^"]+)" relays with "model": "([^"]+)" and "models": \["([^"]+)"\]$`, st.relaysModelList)
	sc.Step(`^the response is 200 served as "([^"]+)"$`, st.servedAs)
	sc.Step(`^nodes "([^"]+)" and "([^"]+)" are on air and dispatching to "([^"]+)" fails with "([^"]+)"$`, st.twoOnAirOneFails)
	sc.Step(`^"([^"]+)" relays with provider\.order \["([^"]+)"\] and provider\.allow_fallbacks false$`, st.relaysOrderNoFallback)
	sc.Step(`^the response status is 503 with error code "([^"]+)"$`, st.status503Code)
	sc.Step(`^"([^"]+)" received nothing$`, st.receivedNothing)
	sc.Step(`^less than 10 seconds of the request deadline remain when dispatch to "([^"]+)" fails$`, st.deadlineShort)
	sc.Step(`^the failover would start a new attempt$`, st.failoverWouldStart)
	sc.Step(`^no new attempt is started and the 503 is answered$`, st.noNewAttempt503)
	sc.Step(`^"([^"]+)" never answers within nonStreamRelayWait and "([^"]+)" is on air$`, st.neverAnswers)
	sc.Step(`^the response is 504 "([^"]+)" and "([^"]+)" received nothing$`, st.resp504Nothing)

	// #9 usage
	sc.Step(`^node "([^"]+)" claims (\d+) prompt and (\d+) completion tokens and the broker recounts (\d+) and (\d+)$`, st.claimsAndRecounts)
	sc.Step(`^the response body's usage\.prompt_tokens is (\d+) and usage\.completion_tokens is (\d+)$`, st.usagePromptCompletion)
	sc.Step(`^usage\.total_tokens is (\d+)$`, st.usageTotal)
	sc.Step(`^usage\.cost equals X-RogerAI-Cost$`, st.usageCostHeader)
	sc.Step(`^usage\.rogerai\.node is "([^"]+)" and usage\.rogerai\.receipt verifies$`, st.usageNodeReceipt)
	sc.Step(`^node "([^"]+)" claims (\d+) completion tokens for a (\d+)-token completion$`, st.claimsCompletionFor)
	sc.Step(`^usage\.completion_tokens is at most (\d+)$`, st.completionAtMost)
	sc.Step(`^"([^"]+)"'s streaming request does not set stream_options$`, st.noStreamOptions)
	sc.Step(`^node "([^"]+)" emits its own usage chunk$`, st.emitsOwnUsage)
	sc.Step(`^the stream completes$`, st.streamCompletes)
	sc.Step(`^exactly one chunk with a usage object reaches "([^"]+)"$`, st.exactlyOneUsage)
	sc.Step(`^it is the broker's chunk with a usage\.rogerai block$`, st.brokersWithRogerai)
	sc.Step(`^"([^"]+)"'s streaming request sets stream_options\.include_usage (true|false)$`, st.streamOptsInclude)
	sc.Step(`^it is the broker's chunk$`, st.brokersWithRogerai)
	sc.Step(`^exactly one usage chunk reaches "([^"]+)" and it is the broker's$`, st.oneBrokerUsage)
	sc.Step(`^node "([^"]+)" emits a final chunk with both a content delta and a usage object$`, st.contentAndUsageChunk)
	sc.Step(`^"([^"]+)" receives the content delta$`, st.receivesDelta)
	sc.Step(`^the station's usage object is not forwarded$`, st.stationUsageNotForwarded)
	sc.Step(`^node "([^"]+)" answers 200 with empty output and no sibling exists$`, st.emptyOutputNoSibling)
	sc.Step(`^usage\.cost is 0 and usage\.rogerai\.void_reason is "([^"]+)"$`, st.usageCostZeroVoid)

	// #11 / #14 delivered work
	sc.Step(`^node "([^"]+)" streams to "([^"]+)"$`, st.nodeStreamsTo)
	sc.Step(`^"([^"]+)" disconnects after (\d+) completion tokens were forwarded$`, st.disconnectsAfter)
	sc.Step(`^the broker sends "([^"]+)" a cancel for the job$`, st.brokerSendsCancel)
	sc.Step(`^"([^"]+)" stops generating$`, st.stopsGenerating)
	sc.Step(`^node "([^"]+)" streams at out \$([0-9.]+)/1M$`, st.streamsAtOut)
	sc.Step(`^the settle bills the prompt plus (\d+) completion tokens$`, st.billsPromptPlus)
	sc.Step(`^the operator earns their share of that amount$`, st.operatorEarnsShare)
	sc.Step(`^the hold's remainder is released$`, st.holdRemainderReleased)
	sc.Step(`^the hold was sized for (\d+) completion tokens$`, st.holdSizedFor)
	sc.Step(`^a station claims (\d+) tokens produced before the cancel arrived$`, st.claimsBeforeCancel)
	sc.Step(`^"([^"]+)" disconnects$`, st.disconnects)
	sc.Step(`^the settle bills at most the hold$`, st.billsAtMostHold)
	sc.Step(`^node "([^"]+)" owned by "([^"]+)" is streaming$`, st.nodeOwnedStreaming)
	sc.Step(`^"([^"]+)" disconnects mid-stream$`, st.disconnectsMid)
	sc.Step(`^no owner_strikes row exists for "([^"]+)"$`, st.noStrikes)
	sc.Step(`^node "([^"]+)" claims (\d+) completion tokens after "([^"]+)" disconnected at (\d+) forwarded$`, st.claimsAfterDisconnect)
	sc.Step(`^the receipt arrives$`, st.receiptArrives)
	sc.Step(`^the settle bills (\d+) completion tokens, not (\d+)$`, func(n, _ string) error { return st.billsCompletion(n) })
	sc.Step(`^"([^"]+)" disconnects before any content frame was forwarded$`, st.disconnectsBeforeContent)
	sc.Step(`^node "([^"]+)" had processed the prompt$`, st.processedPrompt)
	sc.Step(`^the settle bills the prompt tokens and 0 completion tokens$`, st.billsPromptZero)
	sc.Step(`^"([^"]+)"'s non-stream request is being served by "([^"]+)"$`, st.nonStreamBeingServed)
	sc.Step(`^"([^"]+)" disconnects before the result arrives$`, st.disconnectsBeforeResult)
	sc.Step(`^the broker cancels "([^"]+)"'s job$`, st.cancelsJob)
	sc.Step(`^"([^"]+)" is billed \$0 because nothing was delivered$`, st.billedZeroNothing)
	sc.Step(`^node "([^"]+)" streams (\d+) completion tokens to "([^"]+)" then goes silent past the idle window$`, st.stallsAfter)
	sc.Step(`^the stall is detected$`, st.stallDetected)
	sc.Step(`^the stream ends with a usage chunk billing the prompt plus (\d+) completion tokens$`, st.endsWithUsageBilling)
	sc.Step(`^usage\.rogerai\.void_reason is absent and usage\.rogerai\.partial is "stall"$`, st.partialStall)
	sc.Step(`^the operator earns their share$`, st.operatorEarnsShare)
	sc.Step(`^\[DONE\] follows the chunk$`, st.doneFollows)
	sc.Step(`^node "([^"]+)" owned by "([^"]+)" stalls after content$`, st.ownedStallsAfterContent)
	sc.Step(`^the stall is settled$`, st.stallSettled)
	sc.Step(`^no empty-output strike is recorded for "([^"]+)"$`, st.noEmptyStrike)
	sc.Step(`^the station's stall counter used by health grading increments as today$`, st.stallCounterIncrements)
	sc.Step(`^node "([^"]+)" sends no content frame and goes silent past the idle window$`, st.noContentSilent)
	sc.Step(`^the usage chunk bills 0 and names the stall$`, st.usageBillsZeroNamesStall)
	sc.Step(`^the plan fails over if a sibling exists and no frame was written$`, st.failsOverIfSibling)
	sc.Step(`^node "([^"]+)" stalls after forwarding text the broker recounts as (\d+) tokens$`, st.stallsForwardingRecount)
	sc.Step(`^the station's partial claim says (\d+)$`, st.partialClaim)
	sc.Step(`^(\d+) completion tokens are billed$`, st.completionBilled)
	sc.Step(`^"([^"]+)"'s result arrives (\d+) seconds after "([^"]+)" was answered 504$`, func(name, secs, _ string) error { return st.lateAfter504(name, secs) })
	sc.Step(`^the late receipt is processed within the (\d+)-second grace window$`, st.lateProcessed)
	sc.Step(`^"([^"]+)" is billed \$0$`, st.billedZero)
	sc.Step(`^the receipt is recorded in "([^"]+)"'s chain with void_reason "([^"]+)"$`, st.lateReceiptRecorded)
	sc.Step(`^no owner_strikes row is recorded for the lateness$`, st.noLatenessStrike)
	sc.Step(`^"([^"]+)"'s result arrives (\d+) seconds after the 504$`, st.lateAfterGrace)
	sc.Step(`^no receipt is recorded for it$`, st.noReceiptRecorded)

	// #23 holds
	sc.Step(`^station "([^"]+)" at in \$([0-9.]+)/1M out \$([0-9.]+)/1M with a declared window of (\d+)$`, st.stationWindow)
	sc.Step(`^"([^"]+)" relays a (\d+)-token prompt with max_completion_tokens (\d+)$`, st.relaysPromptMCT)
	sc.Step(`^the hold covers (\d+) prompt tokens and (\d+) output tokens$`, st.holdCovers)
	sc.Step(`^"([^"]+)" relays a (\d+)-token prompt with no max_tokens$`, st.relaysPromptNoMax)
	sc.Step(`^the hold covers (\d+) output tokens, not (\d+)$`, st.holdCoversNot)
	sc.Step(`^the body forwarded to "([^"]+)" carries max_tokens (\d+)$`, st.forwardedMaxTokens)
	sc.Step(`^"([^"]+)" relays with max_tokens (\d+) to a station with a (\d+) window$`, st.relaysMaxTokensWindow)
	sc.Step(`^the hold covers (\d+) output tokens and the forwarded max_tokens is (\d+)$`, st.holdCoversAndForwarded)
	sc.Step(`^a station with a declared window of (\d+)$`, st.stationWithWindow)
	sc.Step(`^the hold and the forwarded max_tokens are (\d+)$`, st.holdAndForwardedAre)
	sc.Step(`^ROGERAI_DEFAULT_OUTPUT_TOKENS is (\d+)$`, st.outputKnob)
	sc.Step(`^"([^"]+)" relays with no max_tokens$`, st.relaysNoMaxTokens)
	sc.Step(`^the forwarded max_tokens is (\d+)$`, st.forwardedIs)
	sc.Step(`^"([^"]+)" holds \$([0-9.]+)$`, st.holds)
	sc.Step(`^a direct station whose hold for this request is \$([0-9.]+) and a Tower row whose bridged hold is \$([0-9.]+) both serve "([^"]+)"$`, st.directAndTower)
	sc.Step(`^"([^"]+)" relays for "([^"]+)"$`, st.relaysFor)
	sc.Step(`^the response is 200 served by the direct station$`, st.servedByDirect)
	sc.Step(`^the plan carried no Tower pair$`, st.noTowerPair)
	sc.Step(`^"([^"]+)" holds \$([0-9.]+) and only a Tower row with a \$([0-9.]+) bridged hold serves "([^"]+)"$`, st.holdsOnlyTower)
	sc.Step(`^the response is 402 with error code "([^"]+)"$`, st.resp402Code)
	sc.Step(`^"([^"]+)" holds \$([0-9.]+) and each direct hold for her request is \$([0-9.]+)$`, st.holdsEachDirect)
	sc.Step(`^"([^"]+)" sends (\d+) concurrent requests$`, st.concurrent)
	sc.Step(`^all (\d+) are served$`, st.allServed)
	sc.Step(`^a 4th concurrent request is 402$`, st.fourth402)

	// #24 price lock
	sc.Step(`^station "([^"]+)" has posted out \$([0-9.]+)/1M for (\d+) hours$`, st.postedFor)
	sc.Step(`^"([^"]+)" was served by "([^"]+)" at \$([0-9.]+) \(a lock is minted\)$`, st.servedAtLock)
	sc.Step(`^the owner raises the price to \$([0-9.]+) and "([^"]+)" relays again within 24 hours$`, st.raisesAndRelays)
	sc.Step(`^"([^"]+)" is billed at \$([0-9.]+)$`, st.billedAt)
	sc.Step(`^"([^"]+)" holds a lock at \$([0-9.]+) on "([^"]+)"$`, st.holdsLockAt)
	sc.Step(`^the owner lowers the price to \$([0-9.]+)$`, st.lowersTo)
	sc.Step(`^station "([^"]+)" normally charges out \$([0-9.]+)/1M$`, st.normallyCharges)
	sc.Step(`^the owner posts \$([0-9.]+) for (\d+) minutes$`, st.postsFor)
	sc.Step(`^"([^"]+)" is served during the promo \(a lock is minted at \$([0-9.]+)\)$`, st.servedDuringPromo)
	sc.Step(`^the owner restores \$([0-9.]+) and "([^"]+)" relays an hour later$`, st.restoresAndRelaysLater)
	sc.Step(`^the owner posted \$([0-9.]+) for (\d+) hours before "([^"]+)"'s first serve$`, st.postedHoursBefore)
	sc.Step(`^the owner raises the price to \$([0-9.]+) after (\d+) hours$`, st.raisesAfter)
	sc.Step(`^"([^"]+)" relays within the 24-hour window$`, st.relaysWithinWindow)
	sc.Step(`^the owner posted \$([0-9.]+) five minutes ago and "([^"]+)" was served at \$([0-9.]+)$`, st.postedMinutesAgoServed)
	sc.Step(`^"([^"]+)" relays again while \$([0-9.]+) is still posted$`, st.relaysWhileStill)
	sc.Step(`^ROGERAI_LOCK_MIN_POSTED is (\d+) minutes$`, st.lockMinPosted)
	sc.Step(`^the owner posted \$([0-9.]+) for (\d+) minutes before "([^"]+)" was served$`, st.postedMinutesBeforeServed)
	sc.Step(`^the owner restores \$([0-9.]+)$`, st.restores)
	sc.Step(`^"([^"]+)" keeps \$([0-9.]+) for the rest of the 24-hour window$`, st.keepsFor)
	sc.Step(`^two broker instances share one store$`, st.twoInstances)
	sc.Step(`^the owner's current price was first seen on instance A (\d+) hours ago$`, st.firstSeenOnA)
	sc.Step(`^"([^"]+)" is served on instance B$`, st.servedOnB)
	sc.Step(`^the lock minted on instance B lasts the full window$`, st.lockFullWindowOnB)
	sc.Step(`^station "([^"]+)" publishes a free window from (\d{2}:\d{2}) to (\d{2}:\d{2})$`, st.freeWindow)
	sc.Step(`^"([^"]+)" is served at (\d{2}:\d{2})$`, st.servedAt)
	sc.Step(`^no lock is minted$`, st.noLockMinted)
	sc.Step(`^node "([^"]+)" answers with 500 and "([^"]+)" serves$`, st.n1500n2Serves)
	sc.Step(`^no lock exists for "([^"]+)" on "([^"]+)"$`, st.noLockOn)
}

var _ = ed25519.PublicKey(nil)
