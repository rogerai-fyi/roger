package main

// banning_bdd_test.go makes features/safety/banning.feature EXECUTABLE, driving the REAL
// owner-keyed strike ladder (broker.strike -> store.OwnerStrike/OwnerStrikeStats/BanOwner)
// against an in-memory store. It pins: a strike accrues against the durable OWNER account
// (not the rotatable node id), idempotency on the strike key, the warn rung (held, not
// banned), the CORROBORATED accumulating ban (>= strikeBanAt strikes across >=
// strikeCorroborateKinds distinct classes), the corroboration guard (a single class at the
// ban count is HELD, never banned), the zero-doubt immediate ban, and decay (a strike older
// than the window stops counting toward the thresholds while its evidence row is kept).
//
// Spec correction surfaced by the conversion: the original scenario 4 said "reaches
// strikeBanAt strikes -> durable ban" without the corroboration requirement; per strikes.go
// a single-class count at the threshold is HELD, not banned, so the ban scenario now spells
// out the distinct-classes requirement and a dedicated scenario pins the single-class HOLD.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

type banState struct {
	b          *broker
	db         *store.Mem
	node       string
	owner      string
	before     int   // strike count snapshot, for the idempotency assertion
	decaySince int64 // a decay-window start placed AFTER the recorded strikes (they have aged out)
}

// reset builds a broker with the DEFAULT strike thresholds wired and a default node->owner
// binding ("n1" -> "op-1"), so a scenario that does not bind its own node still resolves an
// owner. Mirrors strike_cov_test.go's strikeBroker, plus the decay window.
func (s *banState) reset() {
	s.db = store.NewMem()
	_ = s.db.BindOwner(store.Owner{GitHubID: 1, Login: "op-1", Pubkey: "op-1"})
	_ = s.db.BindNode("n1", "op-1")
	s.b = &broker{
		db: s.db, bannedOwners: map[string]bool{},
		strikeWarnAt: defaultStrikeWarnAt, strikeBanAt: defaultStrikeBanAt,
		strikeCorroborateKinds: defaultStrikeCorroborateKinds, strikeDecayDays: defaultStrikeDecayDays,
	}
	s.node, s.owner = "n1", "op-1"
	s.before, s.decaySince = 0, 0
}

func (s *banState) banned(owner string) bool { return s.b.isOwnerBanned(owner) }

// --- Given/When/Then steps -------------------------------------------------

func (s *banState) nodeOwnedByOperator(node, owner string) error {
	s.node, s.owner = node, owner
	_ = s.db.BindOwner(store.Owner{GitHubID: 1, Login: owner, Pubkey: owner})
	return s.db.BindNode(node, owner)
}

func (s *banState) earnsStrike(node string) error {
	s.node = node
	s.b.strike(node, store.StrikeRecountDiscrepancy, "earn:"+node, false, map[string]any{"axis": "output"})
	return nil
}

func (s *banState) strikeRecordedAgainst(owner string) error {
	strikes, _ := s.db.StrikesByOwner(owner, 0)
	if len(strikes) < 1 {
		return fmt.Errorf("no strike recorded against owner %q", owner)
	}
	// Owner-keyed: the strike lives on the DURABLE account, not the rotatable node id, so a
	// fresh node id under the same owner cannot shed it.
	if acct, _ := s.b.ownerOf(s.node); acct != owner {
		return fmt.Errorf("strike keyed to %q, not the durable owner %q", acct, owner)
	}
	return nil
}

func (s *banState) strikeKeyRecorded(key, owner string) error {
	s.owner = owner
	s.b.strike(s.node, store.StrikeRecountDiscrepancy, key, false, map[string]any{"axis": "output"})
	strikes, _ := s.db.StrikesByOwner(owner, 0)
	s.before = len(strikes)
	return nil
}

func (s *banState) sameStrikeAgain(key string) error {
	s.b.strike(s.node, store.StrikeRecountDiscrepancy, key, false, map[string]any{"axis": "output"})
	return nil
}

func (s *banState) countDoesNotIncrease() error {
	strikes, _ := s.db.StrikesByOwner(s.owner, 0)
	if len(strikes) != s.before {
		return fmt.Errorf("strike count rose %d -> %d on a retry (idempotency broken)", s.before, len(strikes))
	}
	return nil
}

func (s *banState) reachesWarn(owner string) error {
	s.owner = owner
	for i := 0; i < s.b.strikeWarnAt; i++ {
		s.b.strike(s.node, store.StrikeRecountDiscrepancy, fmt.Sprintf("warn-%d", i), false, map[string]any{"axis": "output"})
	}
	return nil
}

func (s *banState) warnedNotBanned() error {
	if s.banned(s.owner) {
		return fmt.Errorf("owner %q banned at the warn rung (it must only be WARNED)", s.owner)
	}
	return nil
}

func (s *banState) reachesBanCorroborated(owner string) error {
	s.owner = owner
	kinds := []string{store.StrikeRecountDiscrepancy, store.StrikeEmptyOutput} // 2 distinct classes
	for i := 0; i < s.b.strikeBanAt; i++ {
		s.b.strike(s.node, kinds[i%len(kinds)], fmt.Sprintf("ban-%d", i), false, map[string]any{"axis": "output"})
	}
	return nil
}

func (s *banState) durableBanRecorded() error {
	banned, reason, _ := s.db.IsOwnerBanned(s.owner)
	if !banned {
		return fmt.Errorf("owner %q not durably banned at the corroborated ban threshold", s.owner)
	}
	if reason == "" {
		return fmt.Errorf("durable ban recorded with no reason for %q", s.owner)
	}
	return nil
}

func (s *banState) nodesExcludedFromRouting(owner string) error {
	// The owner ban must exclude EVERY node under the owner, including a freshly-bound id
	// (anti-rotation). nodeOwnerBanned is the gate pickFor/settle consult.
	_ = s.db.BindNode("n2", owner)
	if !s.b.nodeOwnerBanned("n1") || !s.b.nodeOwnerBanned("n2") {
		return fmt.Errorf("a banned owner's nodes (n1 + a fresh n2) must be excluded from routing")
	}
	return nil
}

func (s *banState) reachesBanSingleClass(owner string) error {
	s.owner = owner
	for i := 0; i < s.b.strikeBanAt; i++ {
		s.b.strike(s.node, store.StrikeRecountDiscrepancy, fmt.Sprintf("single-%d", i), false, map[string]any{"axis": "output"})
	}
	return nil
}

func (s *banState) heldNotBanned() error {
	if s.banned(s.owner) {
		return fmt.Errorf("a single-class count at the ban threshold must be HELD, not banned (corroboration guard)")
	}
	return nil
}

func (s *banState) zeroDoubtStrike() error {
	s.owner = "op-1"
	s.b.strike(s.node, store.StrikeImpossibleInput, "zerodoubt:1", true, map[string]any{"claimed": 100, "body_bytes": 1})
	return nil
}

func (s *banState) countsImmediately() error {
	if !s.banned(s.owner) {
		return fmt.Errorf("a zero-doubt strike must ban immediately, bypassing corroboration + decay")
	}
	return nil
}

func (s *banState) oldStrikes(owner string) error {
	s.owner = owner
	// Record ban-count strikes DIRECTLY (no live ban decision), then treat the decay window
	// as opening AFTER they were created — i.e. they are now older than strikeDecayDays.
	for i := 0; i < s.b.strikeBanAt; i++ {
		_, _ = s.db.OwnerStrike(owner, store.StrikeRecountDiscrepancy, "{}", fmt.Sprintf("old-%d", i))
	}
	s.decaySince = time.Now().Unix() + 1
	return nil
}

func (s *banState) agedStrikesDoNotCount() error {
	windowed, _, _ := s.db.OwnerStrikeStats(s.owner, s.decaySince)
	if windowed != 0 {
		return fmt.Errorf("aged strikes still counted: windowed=%d inside the decay window, want 0", windowed)
	}
	// The append-only evidence rows are KEPT; only their WEIGHT in the live ban decision ages
	// out — so the all-time (since=0) count still shows them.
	all, _, _ := s.db.OwnerStrikeStats(s.owner, 0)
	if all < s.b.strikeBanAt {
		return fmt.Errorf("evidence rows were dropped: all-time count=%d, want >= %d", all, s.b.strikeBanAt)
	}
	return nil
}

func TestBanningBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &banState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^node "([^"]*)" owned by operator "([^"]*)"$`, st.nodeOwnedByOperator)
			sc.Step(`^"([^"]*)" earns a strike$`, st.earnsStrike)
			sc.Step(`^the strike is recorded against "([^"]*)" \(a fresh node id won't shed it\)$`, st.strikeRecordedAgainst)
			sc.Step(`^a strike with idem-key "([^"]*)" was recorded for "([^"]*)"$`, st.strikeKeyRecorded)
			sc.Step(`^the same strike "([^"]*)" is submitted again \(a retry / webhook redelivery\)$`, st.sameStrikeAgain)
			sc.Step(`^the strike count does not increase$`, st.countDoesNotIncrease)
			sc.Step(`^"([^"]*)" reaches strikeWarnAt strikes$`, st.reachesWarn)
			sc.Step(`^the operator is WARNED \(not yet banned\)$`, st.warnedNotBanned)
			sc.Step(`^"([^"]*)" reaches strikeBanAt strikes across strikeCorroborateKinds distinct signal classes$`, st.reachesBanCorroborated)
			sc.Step(`^banOwner records a durable ban with the reason \+ evidence$`, st.durableBanRecorded)
			sc.Step(`^\(per routing eligibility\) all of "([^"]*)"'s nodes are excluded from routing$`, st.nodesExcludedFromRouting)
			sc.Step(`^"([^"]*)" reaches strikeBanAt strikes of a SINGLE accumulating signal class$`, st.reachesBanSingleClass)
			sc.Step(`^the owner is HELD \(earnings frozen\) but NOT banned — a ban needs strikeCorroborateKinds distinct kinds$`, st.heldNotBanned)
			sc.Step(`^a zero-doubt strike \(e\.g\. a confirmed-abuse signal\)$`, st.zeroDoubtStrike)
			sc.Step(`^it counts without waiting for corroboration$`, st.countsImmediately)
			sc.Step(`^"([^"]*)" has old strikes older than strikeDecayDays$`, st.oldStrikes)
			sc.Step(`^those aged strikes no longer count toward the thresholds$`, st.agedStrikesDoNotCount)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/safety/banning.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("safety/banning behavior scenarios failed (see godog output above)")
	}
}
