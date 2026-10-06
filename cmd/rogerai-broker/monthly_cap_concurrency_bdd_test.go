package main

// monthly_cap_concurrency_bdd_test.go makes features/money/monthly_cap_concurrency.feature
// EXECUTABLE against the REAL relay (b.relay), the real store (Postgres when
// ROGERAI_TEST_DATABASE_URL is set) and a real shared store (miniredis), with real stations
// behind real tunnels. A second broker instance runs over the SAME durable and shared stores;
// a "restart" is a fresh broker over the same stores.
//
// Observation points: "dispatched" is the number of requests that reached the station's
// upstream (st.upstreamCount); "refused" is the relay's 402 with the approved message; holds
// are the ledger's hold rows (foState.ledgerRows). Requests that "hold while in flight" run
// against a station whose upstream blocks on a gate until the step releases it.
//
// "instance B's whole request runs between instance A's cap read and A's hold" uses the
// capHoldGapForTest seam (nil in production): A's request pauses after its cap pre-check,
// B's request runs to a station (or a refusal), then A places its capped hold.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type mcState struct {
	*psState
	cap        float64
	acctPriv   ed25519.PrivateKey
	acctWallet string

	results     []psResult
	last        psResult
	lastSent    bool
	dispatched  bool
	holdBefore  float64
	mismatches  []string
	stationName string
	bridgeModel string
	voice       bool

	voiceTun      *nodeTunnel
	voiceKey      ed25519.PrivateKey
	voiceNode     string
	voiceCalls    int
	bridgeSubmits int
	durableBroken bool
	interleave    bool
	notices       []string
	mailBodies    []string
	mailMu        *sync.Mutex

	// inflight tracks every request goroutine fire starts, so the After hook can drain the
	// ones a step leaves in flight (inFlightHolding) before the next scenario resets state;
	// resMu guards results across those goroutines and the steps.
	inflight sync.WaitGroup
	resMu    sync.Mutex
}

func (s *mcState) mcReset() error {
	if err := s.psReset(); err != nil {
		return err
	}
	s.resMu.Lock()
	s.results = nil
	s.resMu.Unlock()
	s.cap, s.last, s.lastSent, s.dispatched = 0, psResult{}, false, false
	s.notices, capNoticeHookForTest = nil, nil
	s.holdBefore, s.mismatches, s.stationName, s.bridgeModel, s.voice = 0, nil, "", "", false
	s.voiceTun, s.voiceKey, s.voiceNode, s.voiceCalls, s.bridgeSubmits, s.durableBroken = nil, nil, "", 0, 0, false
	s.interleave, capHoldGapForTest = false, nil
	s.acctPriv, s.acctWallet = s.consumerPriv, s.wallet
	return nil
}

func (s *mcState) now() time.Time { return time.Now() }

// --- Background ------------------------------------------------------------------------

func (s *mcState) fundedAcct(amount float64) error {
	if _, err := s.db.AddCredits(s.acctWallet, amount); err != nil {
		return err
	}
	s.funded = true
	return nil
}

func (s *mcState) capOf(cap float64) error {
	s.cap = cap
	return s.db.SetMonthlyCap(s.acctWallet, cap)
}

// stationAt stands up a station whose worst-case cost per request is exactly cost:
// in $0, out $(cost*10)/1M with max_tokens 100000.
func (s *mcState) stationAt(name, model string, cost float64) error {
	s.maxTokens = 100000
	st := s.standUp(name, stationOpts{model: model, priceIn: 0, priceOut: cost * 10, ctx: 1_000_000})
	gate := s.gate
	hold := s
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		if hold.holding {
			<-gate
		}
		utRealCompletion(w)
	})
	if s.stationName == "" {
		s.stationName = name
	}
	return nil
}

func (s *mcState) s1Station() error { return s.stationAt("s1", "m", 0.10) }
func (s *mcState) s2Station() error { return s.stationAt("s2", "m", 0.30) }

// --- spend fixtures -----------------------------------------------------------------------

func (s *mcState) spent(amount float64) error { return s.recordSpend(s.acctWallet, amount) }

func (s *mcState) counterSays(v float64) error {
	return s.vs.counterSet(capSpendKey(s.acctWallet, time.Now()), v, capCounterTTL)
}

func (s *mcState) counterMissing() error {
	for _, k := range s.mr.Keys() {
		if strings.Contains(k, "cap:spend:"+s.acctWallet) {
			s.mr.Del(k)
		}
	}
	return nil
}

// --- relays --------------------------------------------------------------------------------

func (s *mcState) totalUpstream() int {
	n := 0
	for _, st := range s.stations {
		n += st.upstreamCount()
	}
	return n
}

// fire runs one request on broker b (in-flight requests block on the gate) and returns
// once it either finished or reached a station.
func (s *mcState) fire(b *broker, stream bool, wg *sync.WaitGroup, mu *sync.Mutex) {
	model := "m"
	if s.bridgeModel != "" {
		model = s.bridgeModel
	}
	wg.Add(1)
	s.inflight.Add(1)
	go func() {
		defer s.inflight.Done()
		defer wg.Done()
		var r psResult
		if s.voice {
			r = s.voiceOn(b)
		} else {
			r = s.relayOn(b, s.acctPriv, model, stream)
		}
		mu.Lock()
		s.resMu.Lock()
		s.results = append(s.results, r)
		s.resMu.Unlock()
		mu.Unlock()
	}()
}

// burst starts nA requests on A and nB on B at the same moment, holding every dispatched
// one in flight until all have either been refused or reached a station; then releases.
func (s *mcState) burst(nA, nB int) error {
	s.holding = true
	var wg sync.WaitGroup
	var mu sync.Mutex
	before := s.totalUpstream()
	for i := 0; i < nA; i++ {
		s.fire(s.b, false, &wg, &mu)
	}
	if nB > 0 {
		b2 := s.instB()
		for i := 0; i < nB; i++ {
			s.fire(b2, false, &wg, &mu)
		}
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(s.results)
		mu.Unlock()
		if done+s.totalUpstream()-before >= nA+nB {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.release()
	wg.Wait()
	return nil
}

func (s *mcState) tenHold(n int) error                 { return s.burst(n, 0) }
func (s *mcState) splitHold(a, b int) error            { return s.burst(a, b) }
func (s *mcState) interleaveBetweenReadAndHold() error { s.interleave = true; return nil }

// oneEach runs one request on each instance: at the same moment, or (interleave) with B's
// whole request inside A's gap between its cap read and its hold.
func (s *mcState) oneEach() error {
	if !s.interleave {
		return s.burst(1, 1)
	}
	s.holding = true
	b2 := s.instB()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var once sync.Once
	capHoldGapForTest = func() {
		once.Do(func() {
			before := s.totalUpstream()
			s.fire(b2, false, &wg, &mu)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				done := len(s.results)
				mu.Unlock()
				if done > 0 || s.totalUpstream() > before {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
	defer func() { capHoldGapForTest = nil }()
	s.fire(s.b, false, &wg, &mu)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(s.results)
		mu.Unlock()
		if done+s.totalUpstream() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.release()
	wg.Wait()
	return nil
}

func (s *mcState) inFlightHolding() error {
	s.holding = true
	var wg sync.WaitGroup
	var mu sync.Mutex
	before := s.totalUpstream()
	s.fire(s.b, false, &wg, &mu)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && s.totalUpstream() == before {
		time.Sleep(10 * time.Millisecond)
	}
	if s.totalUpstream() == before {
		return fmt.Errorf("the first request never reached the station")
	}
	return nil // released by the next step's wait or the scenario teardown
}

// arrives fires one more request and records whether it was dispatched or refused.
func (s *mcState) arrives(stream bool) error {
	before := s.totalUpstream()
	_, _, _, _, s.holdBefore, _ = s.ledgerRowsOf(s.acctWallet)
	var wg sync.WaitGroup
	var mu sync.Mutex
	n0 := len(s.results)
	s.fire(s.b, stream, &wg, &mu)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(s.results) > n0
		mu.Unlock()
		if got || s.totalUpstream() > before {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.dispatched = s.totalUpstream() > before
	s.release()
	wg.Wait()
	s.last, s.lastSent = s.results[len(s.results)-1], true
	return nil
}

func (s *mcState) arrivesM() error      { return s.arrives(false) }
func (s *mcState) arrivesStream() error { return s.arrives(true) }
func (s *mcState) arrivesFirstPickS1() error {
	s.landOrder()
	return s.arrives(false)
}

func (s *mcState) heldThenReleased() error {
	id := fmt.Sprintf("released-%d", time.Now().UnixNano())
	if ok, err := s.db.HoldFor(s.acctWallet, id, 0.10); err != nil || !ok {
		return fmt.Errorf("hold: ok=%v err=%v", ok, err)
	}
	_, err := s.db.ReleaseHoldFor(s.acctWallet, id)
	return err
}

func (s *mcState) heldThenSettled(held, cost float64) error {
	id := fmt.Sprintf("settled-%d", time.Now().UnixNano())
	if ok, err := s.db.HoldFor(s.acctWallet, id, held); err != nil || !ok {
		return fmt.Errorf("hold: ok=%v err=%v", ok, err)
	}
	_, err := s.db.Finalize(s.acctWallet, "settle-node", held, cost, 0, protocol.UsageReceipt{RequestID: id, NodeID: "settle-node", Model: "m"})
	return err
}

// --- Thens -------------------------------------------------------------------------------------

func (s *mcState) exactlyDispatched(n int) error {
	if got := s.totalUpstream(); got != n {
		return fmt.Errorf("%d requests were dispatched to a station, want exactly %d (cap $%.2f): codes %v", got, n, s.cap, s.codesOf())
	}
	return nil
}

func (s *mcState) exactlyOfDispatched(n, _ int) error { return s.exactlyDispatched(n) }

func (s *mcState) codesOf() []int {
	var c []int
	for _, r := range s.results {
		c = append(c, r.code)
	}
	return c
}

func (s *mcState) refused402(n int) error {
	got := 0
	for _, r := range s.results {
		if r.code == 402 && bodyHas(r.body, "monthly spend limit reached") {
			got++
		}
	}
	if got != n {
		return fmt.Errorf("%d requests were refused 402 \"monthly spend limit reached\", want %d: codes %v", got, n, s.codesOf())
	}
	return nil
}

func (s *mcState) otherRefused() error { return s.refused402(1) }

func (s *mcState) spendAtMost(max float64) error {
	got, err := s.db.MonthSpendOf(s.acctWallet, time.Now())
	if err != nil {
		return err
	}
	if got > max+1e-9 {
		return fmt.Errorf("month-to-date spend %.6f > cap %.2f", got, max)
	}
	return nil
}

func (s *mcState) itRefused() error {
	if !s.lastSent {
		return fmt.Errorf("no request was sent")
	}
	if s.last.code != 402 || !bodyHas(s.last.body, "monthly spend limit reached") {
		return fmt.Errorf("response %d %s, want 402 \"monthly spend limit reached\" (dispatched=%v)", s.last.code, s.last.body, s.dispatched)
	}
	return nil
}

func (s *mcState) itDispatched() error {
	if !s.dispatched {
		return fmt.Errorf("the request was not dispatched: %d %s", s.last.code, s.last.body)
	}
	return nil
}

func (s *mcState) atLimitHeaders() error {
	h := s.last.hdr
	if h.Get("X-RogerAI-Monthly-Cap") != "1" || h.Get("X-RogerAI-Monthly-Notice") == "" {
		return fmt.Errorf("monthly headers cap=%q notice=%q", h.Get("X-RogerAI-Monthly-Cap"), h.Get("X-RogerAI-Monthly-Notice"))
	}
	return nil
}

func (s *mcState) costZero() error {
	if got := s.last.hdr.Get("X-RogerAI-Cost"); got != "0" {
		return fmt.Errorf("X-RogerAI-Cost %q, want \"0\" on a refusal", got)
	}
	return nil
}

func (s *mcState) ledgerRowsOf(wallet string) (holds, releases, spends, voids int, holdAmt float64, err error) {
	saved := s.wallet
	s.wallet = wallet
	defer func() { s.wallet = saved }()
	return s.ledgerRows()
}

func (s *mcState) noOpenHold() error {
	holds, releases, spends, voids, _, err := s.ledgerRowsOf(s.acctWallet)
	if err != nil {
		return err
	}
	if open := holds - releases - spends - voids; open > 0 {
		return fmt.Errorf("%d hold(s) still open (holds %d releases %d spends %d voids %d)", open, holds, releases, spends, voids)
	}
	return nil
}

func (s *mcState) balanceStill(want float64) error {
	got, err := s.db.PeekBalance(s.acctWallet)
	if err != nil {
		return err
	}
	if math.Abs(got-want) > 1e-6 {
		return fmt.Errorf("balance %.6f, want %.6f", got, want)
	}
	return nil
}

func (s *mcState) holdPlaced(want, not float64) error {
	_, _, _, _, after, err := s.ledgerRowsOf(s.acctWallet)
	if err != nil {
		return err
	}
	got := after - s.holdBefore
	if math.Abs(got-want) > 1e-6 {
		return fmt.Errorf("hold placed $%.6f, want $%.2f (not $%.2f)", got, want, not)
	}
	return nil
}

func (s *mcState) dispatchedTo(name string) error {
	if !s.dispatched || s.st(name).upstreamCount() == 0 {
		return fmt.Errorf("the request did not reach %q (%d %s)", name, s.last.code, s.last.body)
	}
	return nil
}

func (s *mcState) holdsSumAtMost(max float64) error {
	holds, _, _, _, amt, err := s.ledgerRowsOf(s.acctWallet)
	if err != nil {
		return err
	}
	seeded := s.seededHolds()
	if amt-seeded > max+1e-9 {
		return fmt.Errorf("holds placed by the racing requests sum to $%.6f (%d hold rows), want at most $%.2f", amt-seeded, holds, max)
	}
	return nil
}

// seededHolds is the hold amount the spend fixtures placed (recordSpend holds = its spend).
func (s *mcState) seededHolds() float64 {
	got, _ := s.db.MonthSpendOf(s.acctWallet, time.Now())
	return got
}

func (s *mcState) reconcileRace() error {
	if _, err := s.db.AddCredits(s.acctWallet, 100); err != nil {
		return err
	}
	b2 := s.instB()
	now := time.Now()
	for i := 0; i < 200 && len(s.mismatches) == 0; i++ {
		_ = s.counterMissing()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.b.monthSpend(s.acctWallet, now) }()
		go func() {
			defer wg.Done()
			id := "race-" + strconv.Itoa(i) + "-" + s.nonce
			if ok, err := s.db.HoldFor(s.acctWallet, id, 0.10); err != nil || !ok {
				return
			}
			if _, err := s.db.Finalize(s.acctWallet, "race-node", 0.10, 0.10, 0, protocol.UsageReceipt{RequestID: id, NodeID: "race-node", Model: "m"}); err == nil {
				b2.recordMonthSpend(s.acctWallet, 0.10, now)
			}
		}()
		wg.Wait()
		got, found, err := s.vs.counterGet(capSpendKey(s.acctWallet, now))
		truth, _ := s.db.MonthSpendOf(s.acctWallet, now)
		if err == nil && found && math.Abs(got-truth) > 1e-6 {
			s.mismatches = append(s.mismatches, fmt.Sprintf("iteration %d: counter %.2f, ledger %.2f", i, got, truth))
		}
	}
	return nil
}

func (s *mcState) counterEqualsLedger() error {
	if len(s.mismatches) > 0 {
		return fmt.Errorf("a reconcile overwrote a peer's increment: %s", s.mismatches[0])
	}
	return nil
}

func (s *mcState) noFrame() error {
	if bodyHas(s.last.body, "data:") {
		return fmt.Errorf("a stream frame was written on a refused request")
	}
	return nil
}

// --- voice ---------------------------------------------------------------------------------

func (s *mcState) voiceStation() error {
	s.voice = true
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	id := "v1-" + s.nonce
	s.b.nodes[id] = protocol.NodeRegistration{NodeID: id, PubKey: hexOf(nodePub),
		Offers: []protocol.ModelOffer{{Model: "voice-" + s.nonce, Modality: protocol.ModalityTTS, PriceIn: 100}}}
	s.b.lastSeen[id] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 8), waiters: map[string]chan protocol.JobResult{}}
	s.b.tunnels[id] = tun
	if err := s.db.BindNode(id, "voice-op-"+s.nonce); err != nil {
		return err
	}
	s.voiceTun, s.voiceKey, s.voiceNode = tun, nodePriv, id
	go func() {
		for job := range tun.jobs {
			s.voiceCalls++
			rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: id, Model: "voice-" + s.nonce, TS: time.Now().Unix()}
			rec.SignNode(nodePriv)
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- protocol.JobResult{ID: job.ID, Status: 200, Body: []byte("~audio~"), Receipt: rec}
			}
		}
	}()
	return nil
}

func (s *mcState) voiceOn(b *broker) psResult {
	reqBody := []byte(fmt.Sprintf(`{"model":%q,"voice":%q,"input":%q,"response_format":"mp3"}`, "voice-"+s.nonce, "voice-"+s.nonce, strings.Repeat("a", 1000)))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(reqBody)))
	signReq(r, s.acctPriv, reqBody)
	w := httptest.NewRecorder()
	b.audioRelay(w, r)
	return psResult{code: w.Code, body: w.Body.Bytes(), hdr: w.Header()}
}

func (s *mcState) voiceArrives() error {
	s.last, s.lastSent = s.voiceOn(s.b), true
	s.dispatched = s.voiceCalls > 0
	return nil
}

// --- the Tower bridge -------------------------------------------------------------------------

func (s *mcState) towerServes(model string, _ float64) error {
	if err := s.ensureTowerOn(s.b); err != nil {
		return err
	}
	_ = s.instB()
	s.b2.tower = s.b.tower
	s.bridgeModel = model
	if err := psRetireFabricOperator(s.foState); err != nil {
		return err
	}
	tw := liveSealedFabric(s.t, s.b, s.towerSrv, model)
	_ = tw
	// the bridge needs a signed-in account; the scenario's account becomes that consumer
	s.acctPriv = signedInConsumer(s.t, s.b)
	o, _, err := s.db.OwnerByPubkey(hexOf(s.acctPriv.Public().(ed25519.PublicKey)))
	if err != nil {
		return err
	}
	w, _ := accountWalletForOwner(o)
	s.acctWallet = w
	return s.db.SetMonthlyCap(w, s.cap)
}

func (s *mcState) towerHolds() int {
	holds, _, _, _, _, _ := s.ledgerRowsOf(s.acctWallet)
	return holds
}

func (s *mcState) bridgeArrives() error {
	before := s.towerHolds()
	s.last, s.lastSent = s.relayOn(s.b, s.acctPriv, s.bridgeModel, false), true
	s.dispatched = s.towerHolds() > before
	return nil
}

func (s *mcState) noTowerAttempt() error {
	if s.dispatched {
		return fmt.Errorf("an attempt was submitted to the Tower (a hold was placed): %d %s", s.last.code, s.last.body)
	}
	return nil
}

func (s *mcState) bridgeBurst(n int) error {
	before := s.towerHolds()
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		s.fire(s.b, false, &wg, &mu)
	}
	wg.Wait()
	s.bridgeSubmits = s.towerHolds() - before - int(math.Round(s.seededHoldsCount()))
	return nil
}

func (s *mcState) seededHoldsCount() float64 { return 0 }

func (s *mcState) atMostTower(n int) error {
	if s.bridgeSubmits > n {
		return fmt.Errorf("%d bridged requests were submitted (holds placed), want at most %d", s.bridgeSubmits, n)
	}
	return nil
}

// --- free traffic --------------------------------------------------------------------------

func (s *mcState) ownsStation(name, model string) error {
	st := s.standUp(name, stationOpts{model: model, priceIn: 0.10, priceOut: 0.30, ownerPriv: s.acctPriv})
	_ = st
	return nil
}

func (s *mcState) servedByOwn(name, model string) error {
	s.maxTokens = 0
	before := s.st(name).upstreamCount()
	s.last, s.lastSent = s.relayOn(s.b, s.acctPriv, model, false), true
	s.dispatched = s.st(name).upstreamCount() > before
	return nil
}

func (s *mcState) dispatchedAtZero() error {
	if !s.dispatched || s.last.code != 200 {
		return fmt.Errorf("the request was not served: %d %s", s.last.code, s.last.body)
	}
	if c := s.last.hdr.Get("X-RogerAI-Cost"); c != "" && c != "0" {
		return fmt.Errorf("X-RogerAI-Cost %q, want $0", c)
	}
	return nil
}

func (s *mcState) freeStation(name, model string) error {
	s.standUp(name, stationOpts{model: model})
	return nil
}

func (s *mcState) freeArrives(model string) error {
	s.maxTokens = 0
	name := s.order[len(s.order)-1]
	return s.servedByOwn(name, model)
}

// --- restart and outage ------------------------------------------------------------------------

func (s *mcState) restartA() error {
	s.restart()
	return nil
}

func (s *mcState) arrivesFresh() error { return s.arrives(false) }

func (s *mcState) durableDown() error {
	if s.pg != nil {
		return s.pg.Close()
	}
	s.durableBroken = true
	s.b.db = failingStore{Store: s.db}
	return nil
}

func (s *mcState) refused5xx() error {
	if s.last.code < 500 || bodyHas(s.last.body, "data:") {
		return fmt.Errorf("response %d %s, want a 5xx with no stream frame", s.last.code, s.last.body)
	}
	return nil
}

func (s *mcState) nothingDispatched() error {
	if s.dispatched {
		return fmt.Errorf("a request was dispatched while the durable store was down")
	}
	return nil
}

// failingStore is the in-memory store with every money call failing, standing in for an
// unreachable durable store when the scenario runs without Postgres.
type failingStore struct{ store.Store }

var errDurableDown = fmt.Errorf("durable store unreachable")

func (failingStore) HoldFor(string, string, float64) (bool, error)   { return false, errDurableDown }
func (failingStore) MonthlyCapOf(string) (float64, error)            { return 0, errDurableDown }
func (failingStore) MonthSpendOf(string, time.Time) (float64, error) { return 0, errDurableDown }
func (failingStore) BalanceOf(string, float64) (float64, error)      { return 0, errDurableDown }
func (failingStore) PeekBalance(string) (float64, error)             { return 0, errDurableDown }
func (failingStore) AccountOfNode(string) (string, bool, error)      { return "", false, errDurableDown }
func (failingStore) Healthy() error                                  { return errDurableDown }

func TestMonthlyCapConcurrencyBDD(t *testing.T) {
	fo := &foState{t: t, logs: &utLog{}}
	st := &mcState{psState: newPSState(fo)}
	prev := log.Writer()
	log.SetOutput(fo.logs)
	t.Cleanup(func() { log.SetOutput(prev); fo.teardown() })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.mcReset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.release()
				st.inflight.Wait() // no request goroutine outlives its scenario
				if st.towerSrv != nil {
					st.towerSrv.Close()
					st.towerSrv = nil
				}
				fo.teardown()
				return ctx, nil
			})
			sc.Step(`^a funded account "acct" with a balance of \$([0-9.]+)$`, st.fundedAcct)
			sc.Step(`^"acct" has a monthly cap of \$([0-9.]+)$`, st.capOf)
			sc.Step(`^a station "s1" is on air for "m" whose worst-case cost per request is \$0\.10$`, st.s1Station)
			sc.Step(`^a station "s2" is on air for "m" whose worst-case cost per request is \$0\.30$`, st.s2Station)
			sc.Step(`^"acct" has spent \$([0-9.]+) this month(?: in the ledger)?$`, st.spent)
			sc.Step(`^(\d+) requests from "acct" for "m" start at the same moment and hold while in flight$`, st.tenHold)
			sc.Step(`^exactly (\d+) of them are dispatched$`, st.exactlyDispatched)
			sc.Step(`^the other (\d+) are refused 402 "monthly spend limit reached"$`, st.refused402)
			sc.Step(`^the month-to-date spend after every request settles is at most \$([0-9.]+)$`, st.spendAtMost)
			sc.Step(`^a second broker instance over the same durable and shared stores$`, func() error { st.instB(); return nil })
			sc.Step(`^(\d+) requests from "acct" start on instance A and (\d+) on instance B at the same moment$`, st.splitHold)
			sc.Step(`^exactly (\d+) of the (\d+) (?:is|are) dispatched$`, st.exactlyOfDispatched)
			sc.Step(`^instance B's whole request runs between instance A's cap read and A's hold$`, st.interleaveBetweenReadAndHold)
			sc.Step(`^one request from "acct" runs on each instance(?: at the same moment)?$`, st.oneEach)
			sc.Step(`^the other is refused 402 "monthly spend limit reached"$`, st.otherRefused)
			sc.Step(`^one request from "acct" is in flight holding \$0\.10$`, st.inFlightHolding)
			sc.Step(`^another request from "acct" for "m" arrives$`, st.arrivesM)
			sc.Step(`^it is refused 402 "monthly spend limit reached"$`, st.itRefused)
			sc.Step(`^one request from "acct" held \$0\.10 and then failed before any work, releasing the hold$`, st.heldThenReleased)
			sc.Step(`^one request from "acct" held \$([0-9.]+) and settled at \$([0-9.]+)$`, st.heldThenSettled)
			sc.Step(`^it is dispatched$`, st.itDispatched)
			sc.Step(`^the refusal message names \$0\.10 held by requests still in progress$`, st.refusalNamesPending)
			sc.Step(`^the response carries X-RogerAI-Monthly-Pending "([^"]+)"$`, st.pendingHeader)
			sc.Step(`^the response carries no X-RogerAI-Monthly-Pending header$`, st.noPendingHeader)
			sc.Step(`^"acct" has a verified email for notices$`, st.recordNotices)
			sc.Step(`^"acct"'s cap notices are delivered to a test mailbox$`, st.mailbox)
			sc.Step(`^"acct" spends another \$([0-9.]+) this month$`, st.spent)
			sc.Step(`^exactly (\d+) 100% cap notices? (?:was|were) delivered for "acct"$`, st.delivered100)
			sc.Step(`^no 100% cap notice was sent for "acct"$`, func() error { return st.noticeSent(false) })
			sc.Step(`^a 100% cap notice was sent for "acct"$`, func() error { return st.noticeSent(true) })
			sc.Step(`^one request from "acct" for "m" arrives$`, st.arrivesM)
			sc.Step(`^the response carries X-RogerAI-Monthly-Cap "1" and X-RogerAI-Monthly-Notice$`, st.atLimitHeaders)
			sc.Step(`^the response carries X-RogerAI-Cost "0"$`, st.costZero)
			sc.Step(`^"acct" has no open hold$`, st.noOpenHold)
			sc.Step(`^"acct"'s balance is still \$([0-9.]+)$`, st.balanceStill)
			sc.Step(`^one request from "acct" for "m" arrives and its plan's first pick is "s1"$`, st.arrivesFirstPickS1)
			sc.Step(`^the hold placed is \$([0-9.]+), not \$([0-9.]+)$`, st.holdPlaced)
			sc.Step(`^the request is dispatched to "([^"]+)"$`, st.dispatchedTo)
			sc.Step(`^the sum of the holds placed is at most \$([0-9.]+)$`, st.holdsSumAtMost)
			sc.Step(`^the shared spend counter for "acct" this month says \$([0-9.]+)$`, st.counterSays)
			sc.Step(`^the shared spend counter for "acct" this month is missing$`, st.counterMissing)
			sc.Step(`^instance A reconciles the counter while instance B records a \$0\.10 settle$`, st.reconcileRace)
			sc.Step(`^the shared counter equals the ledger's month-to-date spend$`, st.counterEqualsLedger)
			sc.Step(`^the shared store is unreachable$`, func() error { st.sharedDown(); return nil })
			sc.Step(`^one streaming request from "acct" for "m" arrives$`, st.arrivesStream)
			sc.Step(`^no stream frame was written$`, st.noFrame)
			sc.Step(`^a voice station is on air whose cost per request is \$0\.10$`, st.voiceStation)
			sc.Step(`^one voice request from "acct" arrives$`, st.voiceArrives)
			sc.Step(`^no direct station serves "([^"]+)" and an approved Tower serves "[^"]+" at a worst-case cost of \$([0-9.]+)$`, st.towerServes)
			sc.Step(`^one request from "acct" for "tm" arrives$`, st.bridgeArrives)
			sc.Step(`^no attempt was submitted to the Tower$`, st.noTowerAttempt)
			sc.Step(`^(\d+) requests from "acct" for "tm" start at the same moment$`, st.bridgeBurst)
			sc.Step(`^at most (\d+) are submitted to the Tower$`, st.atMostTower)
			sc.Step(`^"acct" owns a station "([^"]+)" on air for "([^"]+)"$`, st.ownsStation)
			sc.Step(`^one request from "acct" for "m" is served by "([^"]+)"$`, func(n string) error { return st.servedByOwn(n, "m") })
			sc.Step(`^it is dispatched at \$0$`, st.dispatchedAtZero)
			sc.Step(`^a free station "([^"]+)" is on air for "([^"]+)"$`, st.freeStation)
			sc.Step(`^one request from "acct" for "fm" arrives$`, func() error { return st.freeArrives("fm") })
			sc.Step(`^one request from "acct" is in flight holding \$0\.10 on instance A$`, st.inFlightHolding)
			sc.Step(`^instance A restarts as a fresh broker over the same stores$`, st.restartA)
			sc.Step(`^another request from "acct" for "m" arrives at the fresh broker$`, st.arrivesFresh)
			sc.Step(`^the durable store is unreachable$`, st.durableDown)
			sc.Step(`^it is refused with a 5xx and no stream frame$`, st.refused5xx)
			sc.Step(`^nothing was dispatched$`, st.nothingDispatched)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/money/monthly_cap_concurrency.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("monthly_cap_concurrency.feature failed")
	}
}

// --- audit fix 2026-10-04: refusals caused by open holds -----------------------------------

func (s *mcState) refusalNamesPending() error {
	if !bodyHas(s.last.body, "$0.10 held by requests still in progress") {
		return fmt.Errorf("refusal %s does not name the $0.10 held by requests still in progress", s.last.body)
	}
	return nil
}

func (s *mcState) pendingHeader(want string) error {
	if got := s.last.hdr.Get("X-RogerAI-Monthly-Pending"); got != want {
		return fmt.Errorf("X-RogerAI-Monthly-Pending %q, want %q", got, want)
	}
	return nil
}

func (s *mcState) noPendingHeader() error {
	if got := s.last.hdr.Get("X-RogerAI-Monthly-Pending"); got != "" {
		return fmt.Errorf("X-RogerAI-Monthly-Pending %q on a refusal at the cap on captured spend alone", got)
	}
	return nil
}

// recordNotices observes the broker's notice DECISION. Delivery (resolving the account's
// address) is covered by features/ops/cap_notice_emails.feature; here it only matters
// whether the 100% notice was asked for.
func (s *mcState) recordNotices() error {
	var mu sync.Mutex
	s.notices = nil
	capNoticeHookForTest = func(holder, threshold string) {
		mu.Lock()
		defer mu.Unlock()
		if holder == s.acctWallet {
			s.notices = append(s.notices, threshold)
		}
	}
	return nil
}

// mailbox routes "acct"'s cap notices through the real mailer (its once-a-month dedupe
// included) to a counting transport, with an owner on file for the account's address.
func (s *mcState) mailbox() error {
	if err := s.db.BindOwner(store.Owner{GitHubID: time.Now().UnixNano(), Login: "cap-mailbox",
		Pubkey: s.acctWallet, Email: "acct@example.com"}); err != nil {
		return err
	}
	var mu sync.Mutex
	s.mailBodies = nil
	s.b.mail = enabledMailer(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		s.mailBodies = append(s.mailBodies, string(body))
		mu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	s.mailMu = &mu
	return nil
}

// delivered100 counts the 100% notices the mailbox received. Delivery is queued, so it waits
// for at least the expected count, then holds briefly so a late duplicate is still seen.
func (s *mcState) delivered100(want int) error {
	count := func() int {
		s.mailMu.Lock()
		defer s.mailMu.Unlock()
		n := 0
		for _, b := range s.mailBodies {
			if !strings.Contains(b, "Monthly spend at 80%") && strings.Contains(b, "spend limit") {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(3 * time.Second)
	for count() < want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got := count(); got != want {
		return fmt.Errorf("%d 100%% cap notices delivered, want %d", got, want)
	}
	return nil
}

func (s *mcState) noticeSent(want bool) error {
	got := false
	for _, th := range s.notices {
		if th == "100" {
			got = true
		}
	}
	if got != want {
		return fmt.Errorf("100%% cap notice sent=%v, want %v (notices %v)", got, want, s.notices)
	}
	return nil
}
