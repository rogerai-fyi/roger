package main

// orphan_holds_bdd_test.go makes features/money/orphan_holds.feature an EXECUTABLE Cucumber
// suite. It drives the REAL store (store.Mem) tracked-hold registry + ReleaseStaleHolds sweep,
// and a REAL net/http server for the graceful-drain scenario (no mocks). The invariant: an
// in-flight relay hold is NEVER orphaned by a rolling deploy - the drain finishes it before
// exit, and the sweep reclaims any hard-killed remnant, exactly once, restoring the EXACT
// held amount. feParseFloat/feApprox live in fee_splits_bdd_test.go (same package).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type orphanState struct {
	store   *store.Mem
	feeRate float64
	node    string

	holdAmt map[string]float64   // requestID -> held amount
	holdWho map[string]string    // requestID -> consumer wallet
	placedT map[string]time.Time // requestID -> t0 captured just before HoldFor (placedAt proxy)
	lastReq string

	swept  int // releases from the most recent sweep
	swept2 int // releases from the immediately-following sweep

	// graceful-drain scenario plumbing
	srv           *http.Server
	ln            net.Listener
	inflight      chan struct{} // closed once the handler has placed its hold and is "in flight"
	proceed       chan struct{} // closed to let the in-flight handler drain to completion
	handlerDone   chan struct{} // closed once the handler returns (hold released)
	drainWaited   bool
	drainReleased bool
}

func (s *orphanState) reset() {
	*s = orphanState{
		feeRate: 0.30,
		store:   store.NewMem(),
		holdAmt: map[string]float64{},
		holdWho: map[string]string{},
		placedT: map[string]time.Time{},
	}
}

func (s *orphanState) freshStore() error { return nil }

func (s *orphanState) feePct(p string) error {
	n, err := feParseFloat(p)
	if err != nil {
		return err
	}
	s.feeRate = n / 100
	return nil
}

func (s *orphanState) consumerCredits(name, v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	if _, _, err := s.store.CreditOnce("real:"+name, name, f); err != nil {
		return err
	}
	return nil
}

func (s *orphanState) nodeOwned(node, owner string) error {
	s.node = node
	return s.store.BindNode(node, owner)
}

func (s *orphanState) balanceIs(name, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	bal, err := s.store.BalanceOf(name, 0)
	if err != nil {
		return err
	}
	return feApprox(bal, want)
}

// --- tracked-hold + sweep steps ----------------------------------------

func (s *orphanState) placesTrackedHold(req, v, who string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.placedT[req] = time.Now()
	ok, err := s.store.HoldFor(who, req, amt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tracked hold %s of %g for %s was refused (insufficient funds)", req, amt, who)
	}
	s.holdAmt[req] = amt
	s.holdWho[req] = who
	s.lastReq = req
	return nil
}

func (s *orphanState) sweepPastTTL(string) error {
	n, err := s.store.ReleaseStaleHolds(time.Now().Add(time.Hour)) // cutoff in the future -> every hold is "stale"
	if err != nil {
		return err
	}
	s.swept = n
	return nil
}

func (s *orphanState) sweepWithinTTL(string) error {
	n, err := s.store.ReleaseStaleHolds(time.Now().Add(-time.Hour)) // cutoff in the past -> no hold is "stale"
	if err != nil {
		return err
	}
	s.swept = n
	return nil
}

func (s *orphanState) sweepRelativeToCutoff(age string) error {
	t0 := s.placedT[s.lastReq]
	var cutoff time.Time
	switch age {
	case "before-cutoff": // the hold was placed before the cutoff -> stale
		cutoff = t0.Add(time.Second)
	case "after-cutoff": // the hold was placed after the cutoff -> live
		cutoff = t0.Add(-time.Second)
	default:
		return fmt.Errorf("unknown age %q", age)
	}
	n, err := s.store.ReleaseStaleHolds(cutoff)
	if err != nil {
		return err
	}
	s.swept = n
	return nil
}

func (s *orphanState) sweepReleases(countStr string) error {
	n, err := feParseFloat(countStr)
	if err != nil {
		return err
	}
	if float64(s.swept) != n {
		return fmt.Errorf("sweep released %d holds, expected %g", s.swept, n)
	}
	return nil
}

func (s *orphanState) sweepRunsAgain() error {
	n, err := s.store.ReleaseStaleHolds(time.Now().Add(time.Hour))
	if err != nil {
		return err
	}
	s.swept2 = n
	return nil
}

func (s *orphanState) secondSweepReleases(countStr string) error {
	n, err := feParseFloat(countStr)
	if err != nil {
		return err
	}
	if float64(s.swept2) != n {
		return fmt.Errorf("second sweep released %d holds, expected %g", s.swept2, n)
	}
	return nil
}

func (s *orphanState) twoInstancesSweep(string) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	cutoff := time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, _ := s.store.ReleaseStaleHolds(cutoff)
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	s.swept = total
	return nil
}

func (s *orphanState) releasedAcrossInstances(countStr string) error {
	n, err := feParseFloat(countStr)
	if err != nil {
		return err
	}
	if float64(s.swept) != n {
		return fmt.Errorf("%d holds released across both instances, expected %g (double-release / drift)", s.swept, n)
	}
	return nil
}

func (s *orphanState) capturedViaFinalize(req, costStr, shareStr string) error {
	cost, err := feParseFloat(costStr)
	if err != nil {
		return err
	}
	share, err := feParseFloat(shareStr)
	if err != nil {
		return err
	}
	rec := protocol.UsageReceipt{RequestID: req, TS: time.Now().Unix()}
	_, err = s.store.Finalize(s.holdWho[req], s.node, s.holdAmt[req], cost, share, rec)
	return err
}

func (s *orphanState) operatorEarned(op, v string) error {
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	e, err := s.store.EarningsOf(s.node)
	if err != nil {
		return err
	}
	return feApprox(e, want)
}

func (s *orphanState) releasedViaDeferred(req string) error {
	_, err := s.store.ReleaseHoldFor(s.holdWho[req], req)
	return err
}

// --- graceful-drain steps ----------------------------------------------

func (s *orphanState) inflightRequestHolds(v, who string) error {
	amt, err := feParseFloat(v)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.inflight = make(chan struct{})
	s.proceed = make(chan struct{})
	s.handlerDone = make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		// Simulate an in-flight priced relay: place the hold, then on every exit path
		// release it (the relay's deferred ReleaseHoldFor). Block until allowed to drain
		// so the test can assert Shutdown WAITS for this handler before the process exits.
		req := "inflight-1"
		ok, herr := s.store.HoldFor(who, req, amt)
		if herr != nil || !ok {
			http.Error(w, "hold failed", http.StatusPaymentRequired)
			return
		}
		defer func() {
			_, _ = s.store.ReleaseHoldFor(who, req)
			close(s.handlerDone)
		}()
		close(s.inflight)
		<-s.proceed
		_, _ = w.Write([]byte("ok"))
	})
	s.srv = &http.Server{Handler: mux}
	go func() { _ = s.srv.Serve(ln) }()
	go func() { _, _ = http.Get("http://" + ln.Addr().String() + "/relay") }()
	select {
	case <-s.inflight:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("the in-flight relay never placed its hold")
	}
}

func (s *orphanState) toldToShutDownInflight() error {
	shutdownReturned := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
		close(shutdownReturned)
	}()
	// Shutdown MUST block while the relay is still in flight (the whole point of the drain).
	select {
	case <-shutdownReturned:
		return errors.New("Shutdown returned before the in-flight relay finished - it did NOT drain")
	case <-time.After(50 * time.Millisecond):
	}
	// Let the in-flight relay drain to completion; Shutdown then returns.
	close(s.proceed)
	select {
	case <-shutdownReturned:
	case <-time.After(3 * time.Second):
		return errors.New("Shutdown did not return after the in-flight relay drained")
	}
	s.drainWaited = true
	select {
	case <-s.handlerDone:
		s.drainReleased = true
	case <-time.After(time.Second):
		return errors.New("the in-flight relay did not finish (release its hold)")
	}
	return nil
}

func (s *orphanState) shutdownWaited() error {
	if !s.drainWaited {
		return errors.New("the shutdown did not wait for the in-flight relay to finish")
	}
	return nil
}

func (s *orphanState) inflightReleased() error {
	if !s.drainReleased {
		return errors.New("the in-flight relay did not release its hold as it drained")
	}
	return nil
}

func TestOrphanHoldsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &orphanState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.srv != nil {
					_ = st.srv.Close()
				}
				return ctx, nil
			})
			sc.Step(`^a fresh ledger-backed store$`, st.freshStore)
			sc.Step(`^the platform fee rate is (\d+)%$`, st.feePct)
			sc.Step(`^consumer "([^"]*)" has ([\d.]+) in real credits$`, st.consumerCredits)
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^(\w+)'s balance is (?:still )?(-?[\d.]+)$`, st.balanceIs)

			sc.Step(`^request "([^"]*)" places a tracked hold of ([\d.]+) for (\w+)$`, st.placesTrackedHold)
			sc.Step(`^the stale-hold sweep runs for holds older than the TTL and ([\w-]+) is past its TTL$`, st.sweepPastTTL)
			sc.Step(`^the stale-hold sweep runs for holds older than the TTL and ([\w-]+) is within its TTL$`, st.sweepWithinTTL)
			sc.Step(`^the stale-hold sweep runs with the hold placed (before-cutoff|after-cutoff) relative to the cutoff$`, st.sweepRelativeToCutoff)
			sc.Step(`^the sweep releases (\d+) holds?$`, st.sweepReleases)
			sc.Step(`^the stale-hold sweep runs again for holds older than the TTL$`, st.sweepRunsAgain)
			sc.Step(`^the second sweep releases (\d+) holds?$`, st.secondSweepReleases)
			sc.Step(`^two instances run the stale-hold sweep concurrently with ([\w-]+) past its TTL$`, st.twoInstancesSweep)
			sc.Step(`^exactly (\d+) hold is released across both instances in total$`, st.releasedAcrossInstances)
			sc.Step(`^request (\S+) is captured via Finalize with cost ([\d.]+) and owner share ([\d.]+)$`, st.capturedViaFinalize)
			sc.Step(`^operator "([^"]*)" has earned ([\d.]+)$`, st.operatorEarned)
			sc.Step(`^request (\S+) is released via the deferred relay release$`, st.releasedViaDeferred)

			sc.Step(`^an in-flight priced request has placed a ([\d.]+) hold for (\w+)$`, st.inflightRequestHolds)
			sc.Step(`^the server is told to shut down while that request is still in flight$`, st.toldToShutDownInflight)
			sc.Step(`^the shutdown waits for the in-flight request to finish before returning$`, st.shutdownWaited)
			sc.Step(`^the in-flight request releases its hold as it drains$`, st.inflightReleased)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/orphan_holds.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/orphan_holds behavior scenarios failed (see godog output above)")
	}
}
