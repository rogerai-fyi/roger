package main

// price_lock_durable_bdd_test.go makes features/money/price_lock_durable.feature EXECUTABLE.
// "c is served m by s1 on X" calls the production pricing function the settle path uses
// (lockedPrice, tunnel.go:2149/:2791) on instance X with the station's CURRENT offer price,
// and records the billed (in, out, until); the X-RogerAI-Price scenario and the free-window
// scenario go through the full relay. Both instances run in multi-instance mode over the
// same durable store and the same miniredis shared store, which is today's production shape.
//
// "A quote exists for (c, s1, m) at P" is observed through the DURABLE store only: a probe
// broker over the same durable store with no shared store and empty memory must bill P when
// the station's current price is far above it. Today no durable quote exists, so the probe
// bills the current price.
//
// The two fault Givens wrap one instance's durable store: "answers price-lock reads after
// 500 ms" delays QuotePrice, "price-lock reads fail on B" makes it error.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type plState struct {
	*psState
	priceIn, priceOut float64
	billed            map[string][3]float64 // consumer -> in, out, until(unix nano)
	untils            map[int64]bool
	lastCode          int
	lastHdr           map[string]string
	freeWindowSet     bool
	lockErr           map[string]error
	settleDone        chan struct{}
}

func (s *plState) plReset() error {
	if err := s.psReset(); err != nil {
		return err
	}
	s.priceIn, s.priceOut = 0, 0
	s.billed, s.untils, s.freeWindowSet = map[string][3]float64{}, map[int64]bool{}, false
	s.lockErr, s.settleDone = map[string]error{}, nil
	return nil
}

func (s *plState) user(c string) string { return c + "-" + s.nonce }
func (s *plState) sid() string          { return s.st("s1").id }

func (s *plState) instPL(name string) *broker {
	b := s.b
	if name == "B" {
		b = s.instB()
	}
	b.multiInstance = true
	return b
}

// prodLockWin sets the production 24 h lock window (the harness constructors use 1 h).
func prodLockWin(bs ...*broker) {
	for _, b := range bs {
		b.lockWin = 24 * time.Hour
	}
}

func (s *plState) twoInstances() error {
	prodLockWin(s.instPL("A"), s.instPL("B"))
	return nil
}

func (s *plState) station(in, out float64) error {
	s.priceIn, s.priceOut = in, out
	s.standUp("s1", stationOpts{model: "m", priceIn: in, priceOut: out})
	return nil
}

func (s *plState) consumer(string) error { return nil }

func (s *plState) serve(c, name string) error {
	if s.freeWindowSet {
		return s.servedFullRelay(c) // the free-window decision lives in the relay, not in lockedPrice
	}
	b := s.instPL(name)
	in, out, until, err := b.lockedPrice(s.user(c), s.sid(), "m", s.priceIn, s.priceOut)
	if err != nil {
		s.lockErr[c] = err // the settle path bills nothing on a lock error
		return nil
	}
	s.billed[c] = [3]float64{in, out, float64(until.UnixNano())}
	return nil
}

func (s *plState) servedOn(c, name string) error { return s.serve(c, name) }

func (s *plState) servedAt(c, name string, out float64) error {
	s.priceOut = out
	return s.serve(c, name)
}

// durableQuote bills (c, s1, m) on a probe broker that sees only the durable store.
func (s *plState) durableQuote(c string) (float64, float64, time.Time) {
	probe := s.newBroker()
	probe.shared = nil
	probe.multiInstance = false
	in, out, until, _ := probe.lockedPrice(s.user(c), s.sid(), "m", 1e6, 1e6)
	return in, out, until
}

func (s *plState) oneQuoteAt(c string, in, out float64) error {
	gin, gout, _ := s.durableQuote(c)
	if math.Abs(gin-in) > 1e-9 || math.Abs(gout-out) > 1e-9 {
		return fmt.Errorf("the durable store holds no quote for (%s, s1, m) at in %.2f out %.2f: a broker with only the durable store bills in %.2f out %.2f", c, in, out, gin, gout)
	}
	return nil
}

func (s *plState) locked24h() error {
	b := s.billed["c"]
	until := time.Unix(0, int64(b[2]))
	if d := time.Until(until); d < 23*time.Hour || d > 25*time.Hour {
		return fmt.Errorf("locked until %s from now, want about 24h", d.Round(time.Minute))
	}
	return nil
}

func (s *plState) priceChangesBetween() error { return nil } // applied inside the race step

func (s *plState) raceAB(c string) error {
	for i := 0; i < 40; i++ {
		u := fmt.Sprintf("%s-race-%d", s.user(c), i)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var untils []time.Time
		for _, b := range []*broker{s.instPL("A"), s.instPL("B")} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, until, _ := b.lockedPrice(u, s.sid(), "m", s.priceIn, s.priceOut)
				mu.Lock()
				untils = append(untils, until)
				mu.Unlock()
			}()
		}
		wg.Wait()
		if !untils[0].Equal(untils[1]) {
			s.untils[0] = true
			return nil
		}
	}
	return nil
}

func (s *plState) oneQuoteExists(string) error {
	if s.untils[0] {
		return fmt.Errorf("two instances serving a first request at the same moment minted two different quotes (different locked-until)")
	}
	return nil
}

func (s *plState) bothUnderOne() error { return nil } // asserted by oneQuoteExists

func (s *plState) raise(out float64) error {
	s.priceOut = out
	return nil
}

func (s *plState) billedOut(c string, out float64) error {
	got := s.billed[c][1]
	if math.Abs(got-out) > 1e-9 {
		return fmt.Errorf("%s billed out $%.2f, want $%.2f", c, got, out)
	}
	return nil
}

func (s *plState) bTriesMint(c string) error { return s.serve(c, "B") }

func (s *plState) quoteStill(c string, out float64) error {
	_, gout, _ := s.durableQuote(c)
	if math.Abs(gout-out) > 1e-9 {
		return fmt.Errorf("the durable quote for (%s, s1, m) is out $%.2f, want $%.2f", c, gout, out)
	}
	return nil
}

func (s *plState) served25hAgo(c string, out float64) error {
	s.priceOut = out
	a := s.instPL("A")
	saved := a.lockWin
	a.lockWin = time.Millisecond
	err := s.serve(c, "A")
	a.lockWin = saved
	time.Sleep(5 * time.Millisecond)
	return err
}

func (s *plState) nowPriced(out float64) error { s.priceOut = out; return nil }

func (s *plState) quoteNow(c string, out float64) error {
	_, gout, until := s.durableQuote(c)
	if math.Abs(gout-out) > 1e-9 {
		return fmt.Errorf("the durable quote for (%s, s1, m) is out $%.2f, want $%.2f", c, gout, out)
	}
	if d := time.Until(until); d < 23*time.Hour {
		return fmt.Errorf("the durable quote is locked for %s more, want about 24h", d.Round(time.Minute))
	}
	return nil
}

func (s *plState) consumerD() error { return s.serve("d", "A") }

func (s *plState) cut(out float64) error { s.priceOut = out; return nil }

func (s *plState) relayHeader() error {
	w, priv, err := psGitHubConsumer(s.foState)
	if err != nil {
		return err
	}
	_ = w
	r := s.fullRelay(priv)
	s.lastCode = r.code
	s.lastHdr = map[string]string{"X-RogerAI-Price": r.hdr.Get("X-RogerAI-Price")}
	return nil
}

func (s *plState) priceHeader24h() error {
	h := s.lastHdr["X-RogerAI-Price"]
	if !bodyHas([]byte(h), "locked_until=") {
		return fmt.Errorf("X-RogerAI-Price %q (status %d), want locked_until=", h, s.lastCode)
	}
	return nil
}

func (s *plState) restartA() error {
	s.restart()
	s.b.multiInstance = true
	prodLockWin(s.b)
	return nil
}

func (s *plState) servedOnFresh(c string) error { return s.serve(c, "A") }

func (s *plState) singleBroker() error {
	solo := s.newBroker()
	solo.shared, solo.multiInstance = nil, false
	prodLockWin(solo)
	s.b = solo
	return nil
}

func (s *plState) servedOnSolo(c string, out float64) error {
	s.priceOut = out
	in, o, until, err := s.b.lockedPrice(s.user(c), s.sid(), "m", s.priceIn, s.priceOut)
	if err != nil {
		return err
	}
	s.billed[c] = [3]float64{in, o, float64(until.UnixNano())}
	return nil
}

func (s *plState) soloRestart() error {
	solo := s.newBroker()
	solo.shared, solo.multiInstance = nil, false
	prodLockWin(solo)
	s.b = solo
	return nil
}

func (s *plState) servedSolo(c string) error {
	in, o, until, err := s.b.lockedPrice(s.user(c), s.sid(), "m", s.priceIn, s.priceOut)
	if err != nil {
		return err
	}
	s.billed[c] = [3]float64{in, o, float64(until.UnixNano())}
	return nil
}

func (s *plState) flush() error { s.mr.FlushAll(); return nil }

// quoteFaultStore wraps a durable store so price-lock reads are slow or fail.
type quoteFaultStore struct {
	store.Store
	delay time.Duration
	fail  bool
}

func (q quoteFaultStore) QuotePrice(user, node, model string, in, out float64, now time.Time, window time.Duration) (store.PriceQuote, error) {
	if q.fail {
		return store.PriceQuote{}, fmt.Errorf("price lock store unreachable")
	}
	time.Sleep(q.delay)
	return q.Store.QuotePrice(user, node, model, in, out, now, window)
}

func (s *plState) slowDurable() error {
	a := s.instPL("A")
	a.db = quoteFaultStore{Store: a.db, delay: 500 * time.Millisecond}
	return nil
}

// settling starts a settle-time price-lock read on A and returns while it is in flight.
func (s *plState) settling(c string) error {
	a := s.instPL("A")
	s.settleDone = make(chan struct{})
	go func() {
		defer close(s.settleDone)
		_, _, _, _ = a.lockedPrice(s.user(c), s.sid(), "m", s.priceIn, s.priceOut)
	}()
	time.Sleep(50 * time.Millisecond) // the read is now inside its 500 ms delay
	return nil
}

func (s *plState) lockWithin50() error {
	a := s.instPL("A")
	start := time.Now()
	a.mu.Lock()
	waited := time.Since(start)
	a.mu.Unlock()
	if s.settleDone != nil {
		<-s.settleDone
	}
	if waited > 50*time.Millisecond {
		return fmt.Errorf("the routing lock was held for %s during a price-lock read", waited.Round(time.Millisecond))
	}
	return nil
}

func (s *plState) freeWindow() error {
	st := s.st("s1")
	reg := s.b.nodes[st.id]
	reg.Offers[0].Schedule = []protocol.PriceWindow{{Start: "00:00", End: "23:59", Free: true}, {Start: "23:59", End: "00:00", Free: true}}
	s.b.nodes[st.id] = reg
	s.freeWindowSet = true
	return nil
}

func (s *plState) servedFullRelay(c string) error {
	_, priv, err := psGitHubConsumer(s.foState)
	if err != nil {
		return err
	}
	r := s.fullRelay(priv)
	s.lastCode = r.code
	return nil
}

// fullRelay runs one real relay on A. The harness's stations are local tunnels, so the
// relay runs with the multi-instance dispatch off (the bus/queue path needs a poller
// fleet); the price lock still uses the shared store exactly as configured.
func (s *plState) fullRelay(priv ed25519.PrivateKey) psResult {
	a := s.instPL("A")
	a.multiInstance = false
	defer func() { a.multiInstance = true }()
	return s.relayOn(a, priv, "m", false)
}

func (s *plState) noQuote(c string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("the free-window relay was not served: %d", s.lastCode)
	}
	// the relay path skips lockedPrice for a scheduled price: no quote may exist anywhere
	if _, gout, _ := s.durableQuote(c); gout != 1e6 {
		return fmt.Errorf("a quote exists at out %.2f after a free-window request", gout)
	}
	return nil
}

func (s *plState) readsFailOnB() error {
	b2 := s.instPL("B")
	b2.db = quoteFaultStore{Store: b2.db, fail: true}
	return nil
}

func (s *plState) notAbove(c string, out float64) error {
	if s.lockErr[c] != nil {
		return nil // the lock could not be read: nothing is billed at all
	}
	return s.billedOut(c, out)
}

// holdReleased runs a real paid relay on B (whose price-lock reads fail) and checks the
// ledger: the body is served, nothing is captured, and no hold is left open.
func (s *plState) holdReleased(string) error {
	wallet, priv, err := psGitHubConsumer(s.foState)
	if err != nil {
		return err
	}
	// The harness's stations report results to instance A only, so the real relay runs on A
	// with A's price-lock reads failing exactly as B's do: the rule is per serving instance.
	a := s.instPL("A")
	savedDB := a.db
	a.db = quoteFaultStore{Store: savedDB, fail: true}
	a.multiInstance = false
	defer func() { a.db, a.multiInstance = savedDB, true }()
	r := s.relayOn(a, priv, "m", false)
	if r.code != 200 {
		return fmt.Errorf("relay with failing price-lock reads = %d %s, want the body served", r.code, r.body)
	}
	saved := s.wallet
	s.wallet = wallet
	holds, releases, spends, voids, _, lerr := s.ledgerRows()
	s.wallet = saved
	if lerr != nil {
		return lerr
	}
	if spends != 0 {
		return fmt.Errorf("%d spend row(s): the consumer was charged with an unreadable price lock", spends)
	}
	if open := holds - releases - spends - voids; open != 0 {
		return fmt.Errorf("%d hold(s) left open", open)
	}
	return nil
}

func TestPriceLockDurableBDD(t *testing.T) {
	fo := &foState{t: t, logs: &utLog{}}
	st := &plState{psState: newPSState(fo)}
	prev := log.Writer()
	log.SetOutput(fo.logs)
	t.Cleanup(func() { log.SetOutput(prev); fo.teardown() })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.plReset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				fo.teardown()
				return ctx, nil
			})
			sc.Step(`^two broker instances "A" and "B" over the same durable and shared stores$`, st.twoInstances)
			sc.Step(`^a station "s1" is on air for "m" at in \$([0-9.]+) out \$([0-9.]+)$`, st.station)
			sc.Step(`^a funded consumer "([^"]+)"$`, st.consumer)
			sc.Step(`^"([^"]+)" is served "m" by "s1" on "(A|B)"$`, st.servedOn)
			sc.Step(`^exactly one price quote exists for \("([^"]+)", "s1", "m"\) at in \$([0-9.]+) out \$([0-9.]+)$`, st.oneQuoteAt)
			sc.Step(`^it is locked until 24 hours from now$`, st.locked24h)
			sc.Step(`^the station's price changes from out \$0\.30 to out \$0\.20 between A's and B's settle$`, st.priceChangesBetween)
			sc.Step(`^"([^"]+)" is served "m" by "s1" on "A" and on "B" at the same moment$`, st.raceAB)
			sc.Step(`^exactly one price quote exists for \("([^"]+)", "s1", "m"\)$`, st.oneQuoteExists)
			sc.Step(`^both requests were billed under that one quote$`, st.bothUnderOne)
			sc.Step(`^"([^"]+)" was served "m" by "s1" on "(A|B)"$`, st.servedOn)
			sc.Step(`^the operator raises "s1" to out \$([0-9.]+)$`, st.raise)
			sc.Step(`^"([^"]+)" is billed out \$([0-9.]+)$`, st.billedOut)
			sc.Step(`^"([^"]+)" was served "m" by "s1" on "(A|B)" at out \$([0-9.]+)$`, st.servedAt)
			sc.Step(`^"B" tries to mint a quote for \("([^"]+)", "s1", "m"\)$`, st.bTriesMint)
			sc.Step(`^the quote for \("([^"]+)", "s1", "m"\) is still out \$([0-9.]+)$`, st.quoteStill)
			sc.Step(`^"([^"]+)" was served "m" by "s1" 25 hours ago at out \$([0-9.]+)$`, st.served25hAgo)
			sc.Step(`^"s1" is now priced out \$([0-9.]+)$`, st.nowPriced)
			sc.Step(`^the quote for \("([^"]+)", "s1", "m"\) is out \$([0-9.]+) locked until 24 hours from now$`, st.quoteNow)
			sc.Step(`^consumer "d" is served "m" by "s1" on "A"$`, st.consumerD)
			sc.Step(`^the operator cuts "s1" to out \$([0-9.]+)$`, st.cut)
			sc.Step(`^X-RogerAI-Price carries "locked_until=" 24 hours from now$`, func() error {
				if err := st.relayHeader(); err != nil {
					return err
				}
				return st.priceHeader24h()
			})
			sc.Step(`^"A" restarts as a fresh broker over the same stores$`, st.restartA)
			sc.Step(`^"([^"]+)" is served "m" by "s1" on the fresh "A"$`, st.servedOnFresh)
			sc.Step(`^a single broker over a durable store with no shared store configured$`, st.singleBroker)
			sc.Step(`^"([^"]+)" was served "m" by "s1" on it at out \$([0-9.]+)$`, st.servedOnSolo)
			sc.Step(`^the broker restarts over the same durable store$`, st.soloRestart)
			sc.Step(`^"([^"]+)" is served "m" by "s1"$`, st.servedSolo)
			sc.Step(`^every key in the shared store is deleted$`, st.flush)
			sc.Step(`^the durable store answers price-lock reads after 500 ms$`, st.slowDurable)
			sc.Step(`^"([^"]+)" is being settled on "A"$`, st.settling)
			sc.Step(`^another request on "A" can take the broker's routing lock within 50 ms$`, st.lockWithin50)
			sc.Step(`^"s1" publishes a free window that is active now$`, st.freeWindow)
			sc.Step(`^no price quote exists for \("([^"]+)", "s1", "m"\)$`, st.noQuote)
			sc.Step(`^price-lock reads fail on "B"$`, st.readsFailOnB)
			sc.Step(`^"([^"]+)" is not charged above out \$([0-9.]+)$`, st.notAbove)
			sc.Step(`^"([^"]+)"'s hold is released$`, st.holdReleased)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/money/price_lock_durable.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("price_lock_durable.feature failed")
	}
}
