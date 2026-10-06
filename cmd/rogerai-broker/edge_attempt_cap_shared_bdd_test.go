package main

// edge_attempt_cap_shared_bdd_test.go makes features/tower/edge_attempt_cap_shared.feature
// EXECUTABLE. Two broker instances run over the same durable store and the same miniredis
// shared store; "restart" is a fresh broker over the same stores. Slot-level scenarios drive
// the production slot functions directly (edgeAccountReserve / edgeEnterInflight /
// edgeExitInflight - the same calls relayViaEdge and the settle handler make); request-level
// scenarios go through the real relay and a real sealed Tower fabric (liveSealedFabric), with
// both instances sharing one Tower subsystem as the shared Tower state does in production.
//
// "acct has N open attempts" is observed black-box: a probe instance over the same stores
// reserves slots until refused; open = cap - what the probe could take (the probe's slots are
// released again).
//
// Time: deadlines in the spec are scaled 1/30 (a "30 second" deadline is 1 s of real time)
// because edgeEnterInflight schedules its expiry on the real clock; GREEN's shared set trims
// on read and can use the broker clock seam instead.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/store"
)

type eaState struct {
	*psState
	acct     string
	attempts map[string][]string // instance -> attempt ids opened there
	last     bool
	results  []bool
	consumer ed25519.PrivateKey
	relays   []psResult
	holdsAt  int
	// pending is an attempt whose slot binding failed: its reservation token, id and deadline,
	// replayed by "the delayed count lands" exactly as adoptSlotUntilCounted would.
	pendTok, pendID string
	pendUntil       time.Time
}

func (s *eaState) eaReset() error {
	if err := s.psReset(); err != nil {
		return err
	}
	s.acct = "acct-" + s.nonce
	s.attempts = map[string][]string{}
	s.last, s.results, s.consumer, s.relays, s.holdsAt = false, nil, nil, nil, 0
	return nil
}

func (s *eaState) inst(name string) *broker {
	if name == "B" {
		return s.instB()
	}
	return s.b
}

func (s *eaState) twoInstances() error { s.instB(); return nil }
func (s *eaState) anAccount() error    { return nil }

func (s *eaState) openOn(n int, name string, deadline time.Duration) error {
	b := s.inst(name)
	for i := 0; i < n; i++ {
		if !b.edgeAccountReserve(s.acct) {
			return fmt.Errorf("could not open attempt %d of %d on %s", i+1, n, name)
		}
		id := fmt.Sprintf("att-%s-%s-%d-%d", name, s.nonce, len(s.attempts[name]), time.Now().UnixNano())
		b.edgeEnterInflight(id, "n-edge-"+s.nonce, s.acct, time.Now().Add(deadline))
		s.attempts[name] = append(s.attempts[name], id)
	}
	return nil
}

func (s *eaState) nOpen(n int, name string) error { return s.openOn(n, name, 10*time.Minute) }
func (s *eaState) nOpenDeadline(n int, name string, secs int) error {
	return s.openOn(n, name, time.Duration(secs)*time.Second/30)
}

func (s *eaState) reserveOn(name string) error {
	s.last = s.inst(name).edgeAccountReserve(s.acct)
	return nil
}

func (s *eaState) reserveOther(name string) error {
	s.last = s.inst(name).edgeAccountReserve("other-" + s.nonce)
	return nil
}

func (s *eaState) refused() error {
	if s.last {
		return fmt.Errorf("the reserve succeeded, want refused (the cap is %d across instances)", maxOpenEdgeAttemptsPerAccount)
	}
	return nil
}

func (s *eaState) succeeded() error {
	if !s.last {
		return fmt.Errorf("the reserve was refused, want success")
	}
	return nil
}

// openCount is how many of acct's slots are open across the fleet, seen from a probe.
func (s *eaState) openCount() int {
	probe := s.newBroker()
	took := 0
	for took < maxOpenEdgeAttemptsPerAccount+8 && probe.edgeAccountReserve(s.acct) {
		took++
	}
	for i := 0; i < took; i++ {
		probe.edgeAccountRelease(s.acct)
	}
	return maxOpenEdgeAttemptsPerAccount - took
}

func (s *eaState) hasOpen(n int) error {
	if got := s.openCount(); got != n {
		return fmt.Errorf("%q has %d open attempts seen from another instance, want %d", s.acct, got, n)
	}
	return nil
}

func (s *eaState) concurrentAB() error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	s.results = nil
	for _, name := range []string{"A", "B"} {
		b := s.inst(name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok := b.edgeAccountReserve(s.acct)
			mu.Lock()
			s.results = append(s.results, ok)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return nil
}

func (s *eaState) exactlyOneSucceeds() error { return s.exactlyNSucceed(1) }

func (s *eaState) concurrentMany(a, bN int) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	s.results = nil
	for _, pair := range []struct {
		name string
		n    int
	}{{"A", a}, {"B", bN}} {
		b := s.inst(pair.name)
		for i := 0; i < pair.n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok := b.edgeAccountReserve(s.acct)
				mu.Lock()
				s.results = append(s.results, ok)
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	return nil
}

func (s *eaState) exactlyNSucceed(n int) error {
	got := 0
	for _, ok := range s.results {
		if ok {
			got++
		}
	}
	if got != n {
		return fmt.Errorf("%d reserves succeeded, want exactly %d", got, n)
	}
	return nil
}

func (s *eaState) oneSettlesOn(name string) error {
	ids := s.attempts["A"]
	if len(ids) == 0 {
		return fmt.Errorf("no attempt to settle")
	}
	s.inst(name).edgeExitInflight(ids[0])
	return nil
}

// openUnbound opens one more attempt on name whose promotion to its slot failed: the
// reservation is held, the attempt is open locally, and the count is left to the retry.
func (s *eaState) openUnbound(name string) error {
	b := s.inst(name)
	if !b.edgeAccountReserve(s.acct) {
		return fmt.Errorf("could not reserve the extra slot on %s", name)
	}
	s.pendTok = b.takeSlotToken(s.acct) // the promotion never happens: its token is the retry's
	if s.pendTok == "" {
		return fmt.Errorf("no reservation token on %s", name)
	}
	s.pendID = fmt.Sprintf("att-unbound-%s-%d", s.nonce, time.Now().UnixNano())
	s.pendUntil = time.Now().Add(10 * time.Minute)
	b.edgeEnterInflight(s.pendID, "n-edge-"+s.nonce, s.acct, s.pendUntil) // no token left: no promote
	return nil
}

func (s *eaState) pendingSettlesOn(name string) error {
	s.inst(name).edgeExitInflight(s.pendID)
	return nil
}

// delayedCountLands replays the retry's store call (adoptSlotUntilCounted's adopt), which can
// land after the attempt closed: on another instance, or on this one after its local check.
func (s *eaState) delayedCountLands(name string) error {
	_ = s.inst(name).shared.edgeSlotAdopt(s.acct, s.pendTok, s.pendID, s.pendUntil)
	return nil
}

func (s *eaState) aReleasesOne() error {
	ids := s.attempts["A"]
	s.b.edgeExitInflight(ids[0])
	return nil
}

func (s *eaState) secondsPass(n int) error {
	time.Sleep(time.Duration(n)*time.Second/30 + 100*time.Millisecond)
	return nil
}

func (s *eaState) settleTwice() error {
	id := s.attempts["A"][0]
	s.instB().edgeExitInflight(id)
	s.b.edgeExitInflight(id)
	return nil
}

func (s *eaState) unknownSettles() error {
	s.instB().edgeExitInflight("att-never-opened-" + s.nonce)
	return nil
}

func (s *eaState) restartA() error { s.restart(); return nil }

func (s *eaState) reserveFresh() error {
	s.last = s.b.edgeAccountReserve(s.acct)
	return nil
}

func (s *eaState) settlesOnFresh() error {
	s.b.edgeExitInflight(s.attempts["A"][0])
	return nil
}

// --- request level -------------------------------------------------------------------------

func (s *eaState) towerFor(model string) error {
	if err := s.ensureTowerOn(s.b); err != nil {
		return err
	}
	if err := psRetireFabricOperator(s.foState); err != nil {
		return err
	}
	liveSealedFabric(s.t, s.b, s.towerSrv, model)
	s.instB().tower = s.b.tower
	// A funded GitHub-bound consumer: an email-login account cannot spend on a paid DIRECT
	// station on origin/main (a separate, already-specified finding), which would make the
	// soft-bridge scenario fail for the wrong reason.
	w, priv, err := psGitHubConsumer(s.foState)
	if err != nil {
		return err
	}
	s.consumer, s.acct = priv, w
	return nil
}

func (s *eaState) towerOnly() error { return s.towerFor("tm") }

func (s *eaState) towerAndDirect() error {
	if err := s.towerFor("m"); err != nil {
		return err
	}
	s.standUp("d1", stationOpts{model: "m", priceIn: 0.10, priceOut: 0.30})
	return nil
}

// holdCount counts the holds placed for Tower attempts (a Tower grant's attempt id is the
// hold's request id and always starts "att-", dispatch.go); a direct relay's hold is not a
// Tower attempt, so the soft-bridge scenario can be served directly and still assert none.
func (s *eaState) holdCount() int {
	rows, err := s.db.LedgerOf(s.acct, []string{store.KindHold}, 5000)
	if err != nil {
		s.t.Fatalf("ledger of %s: %v", s.acct, err)
	}
	n := 0
	for _, r := range rows {
		if strings.HasPrefix(r.Ref, "att-") {
			n++
		}
	}
	return n
}

func (s *eaState) relaysOn(model, name string, n int) error {
	s.holdsAt = s.holdCount()
	s.relays = nil
	for i := 0; i < n; i++ {
		s.heartbeat(s.inst(name))
		s.relays = append(s.relays, s.relayOn(s.inst(name), s.consumer, model, false))
	}
	return nil
}

// heartbeat marks every registered station seen now, as its heartbeat would. The harness
// registers stations once; with the shared store unreachable each relay pays its op timeout,
// so twenty relays outlast nodeTTL and the direct station would age out for a reason that has
// nothing to do with the cap.
func (s *eaState) heartbeat(b *broker) {
	b.mu.Lock()
	for id := range b.lastSeen {
		b.lastSeen[id] = time.Now()
	}
	b.mu.Unlock()
}

func (s *eaState) relaysTmOn(name string) error { return s.relaysOn("tm", name, 1) }
func (s *eaState) relaysTmOnA() error           { return s.relaysOn("tm", "A", 1) }
func (s *eaState) relaysM20() error             { return s.relaysOn("m", "A", 20) }

func (s *eaState) is429() error {
	r := s.relays[0]
	if r.code != 429 || !bodyHas(r.body, "too many edge attempts open on this account at once") {
		return fmt.Errorf("response %d %s, want 429 \"too many edge attempts open on this account at once\"", r.code, r.body)
	}
	return nil
}

func (s *eaState) retryAfter5() error {
	if got := s.relays[0].hdr.Get("Retry-After"); got != "5" {
		return fmt.Errorf("Retry-After %q, want \"5\"", got)
	}
	return nil
}

func (s *eaState) noTowerAttempt() error {
	if got := s.holdCount(); got > s.holdsAt {
		return fmt.Errorf("%d attempt(s) were submitted to the Tower (holds placed)", got-s.holdsAt)
	}
	return nil
}

func (s *eaState) is503Unavailable() error {
	r := s.relays[0]
	if r.code != 503 || errCode(r.body) != "shared_store_unavailable" {
		return fmt.Errorf("response %d %s, want 503 code \"shared_store_unavailable\"", r.code, r.body)
	}
	return nil
}

func (s *eaState) allDirect() error {
	for i, r := range s.relays {
		if r.hdr.Get("X-RogerAI-Relay") != "" {
			return fmt.Errorf("relay %d rode the Tower (X-RogerAI-Relay %q) while the shared store was unreachable", i+1, r.hdr.Get("X-RogerAI-Relay"))
		}
		if r.code != 200 {
			return fmt.Errorf("relay %d = %d %s, want served by the direct station", i+1, r.code, r.body)
		}
	}
	return nil
}

func (s *eaState) singleNoShared() error {
	solo := s.newBroker()
	solo.shared = nil
	s.b = solo
	return nil
}

func (s *eaState) nOnIt(n int) error { return s.openOn(n, "A", 10*time.Minute) }
func (s *eaState) reserveOnIt() error {
	s.last = s.b.edgeAccountReserve(s.acct)
	return nil
}

func TestEdgeAttemptCapSharedBDD(t *testing.T) {
	fo := &foState{t: t, logs: &utLog{}}
	st := &eaState{psState: newPSState(fo)}
	prev := log.Writer()
	log.SetOutput(fo.logs)
	t.Cleanup(func() { log.SetOutput(prev); fo.teardown() })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.eaReset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.towerSrv != nil {
					st.towerSrv.Close()
				}
				fo.teardown()
				return ctx, nil
			})
			sc.Step(`^two broker instances "A" and "B" over the same durable and shared stores$`, st.twoInstances)
			sc.Step(`^an account "acct"$`, st.anAccount)
			sc.Step(`^"acct" has (\d+) open attempts opened on "(A|B)"$`, st.nOpen)
			sc.Step(`^"acct" has (\d+) open attempts opened on "(A|B)" with deadlines (\d+) seconds away$`, st.nOpenDeadline)
			sc.Step(`^"acct" reserves a slot on "(A|B)"$`, st.reserveOn)
			sc.Step(`^the reserve is refused$`, st.refused)
			sc.Step(`^the reserve succeeds$`, st.succeeded)
			sc.Step(`^"acct" has (\d+) open attempts$`, st.hasOpen)
			sc.Step(`^"acct" reserves a slot on "A" and on "B" at the same moment$`, st.concurrentAB)
			sc.Step(`^exactly one reserve succeeds$`, st.exactlyOneSucceeds)
			sc.Step(`^"acct" reserves (\d+) slots on "A" and (\d+) on "B" at the same moment$`, st.concurrentMany)
			sc.Step(`^exactly (\d+) reserves succeed$`, st.exactlyNSucceed)
			sc.Step(`^account "other" reserves a slot on "(A|B)"$`, st.reserveOther)
			sc.Step(`^one of those attempts settles on "(A|B)"$`, st.oneSettlesOn)
			sc.Step(`^"A" releases one of them before dispatch$`, st.aReleasesOne)
			sc.Step(`^(\d+) seconds pass$`, st.secondsPass)
			sc.Step(`^one attempt settles on "B" and the same attempt settles again on "A"$`, st.settleTwice)
			sc.Step(`^an unknown attempt id settles on "B"$`, st.unknownSettles)
			sc.Step(`^"(A|B)" opens one more attempt whose slot binding fails and is retried later$`, st.openUnbound)
			sc.Step(`^that attempt settles on "(A|B)" before the retry lands$`, st.pendingSettlesOn)
			sc.Step(`^the delayed count lands on "(A|B)"$`, st.delayedCountLands)
			sc.Step(`^"A" restarts as a fresh broker over the same stores$`, st.restartA)
			sc.Step(`^"acct" reserves a slot on the fresh "A"$`, st.reserveFresh)
			sc.Step(`^one of those attempts settles on the fresh "A"$`, st.settlesOnFresh)
			sc.Step(`^an approved Tower serves "tm" and no direct station serves it$`, st.towerOnly)
			sc.Step(`^a funded "acct" relays to "tm" on "B"$`, func() error { return st.relaysTmOn("B") })
			sc.Step(`^the response is 429 "too many edge attempts open on this account at once"$`, st.is429)
			sc.Step(`^it carries Retry-After "5"$`, st.retryAfter5)
			sc.Step(`^no attempt was submitted to the Tower$`, st.noTowerAttempt)
			sc.Step(`^the shared store is unreachable$`, func() error { st.sharedDown(); return nil })
			sc.Step(`^a funded "acct" relays to "tm" on "A"$`, st.relaysTmOnA)
			sc.Step(`^the response is 503 with error code "shared_store_unavailable"$`, st.is503Unavailable)
			sc.Step(`^an approved Tower serves "m" and a direct station also serves "m"$`, st.towerAndDirect)
			sc.Step(`^a funded "acct" relays to "m" on "A" 20 times$`, st.relaysM20)
			sc.Step(`^every relay is served by the direct station$`, st.allDirect)
			sc.Step(`^a single broker with no shared store configured$`, st.singleNoShared)
			sc.Step(`^"acct" has (\d+) open attempts on it$`, st.nOnIt)
			sc.Step(`^"acct" reserves a slot on it$`, st.reserveOnIt)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/tower/edge_attempt_cap_shared.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("edge_attempt_cap_shared.feature failed")
	}
}
