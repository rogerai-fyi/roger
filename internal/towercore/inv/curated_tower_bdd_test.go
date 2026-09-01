package inv

// Executable spec: features/curated/curated_tower.feature, the @joined scenario.
//
// A joined Tower's inventory may declare a Station as a CURATED proxy of a commercial
// provider (`curated_provider`). The upstream key itself never appears anywhere in the
// inventory schema - it stays on the Tower - and the curated pricing rule is enforced at
// admission: the earn rates ARE the upstream's list (pass-through), and the posted price
// must be exactly the list plus Core's routing fee, so a mispriced curated leaf never
// becomes routable.

import (
	"context"
	"fmt"
	"testing"

	"github.com/cucumber/godog"
)

type curJoinedState struct {
	t   *testing.T
	h   *harness
	res Result
}

func (s *curJoinedState) towerJoined() error {
	s.h = newHarness(s.t)
	return nil
}

func (s *curJoinedState) registersUpstreamModels() error {
	leaf := s.h.offer(stationA, "offer-cur", offerSpec{pre: func(m map[string]any) {
		m["curated_provider"] = "openrouter"
		// Pass-through: earn IS the upstream list; posted = list + the 30% routing fee.
		m["earn_in"], m["earn_out"] = "1000", "1600"
		m["price_in"], m["price_out"] = "1300", "2080"
	}})
	res, err := s.h.set.AcceptFull(towerA, s.h.towerPub(), s.h.inventory(invSpec{revision: 7, leaves: []map[string]any{leaf}}))
	if err != nil {
		return err
	}
	s.res = res
	return nil
}

func (s *curJoinedState) appearCuratedUnderTower() error {
	if s.res.Routable != 1 {
		return fmt.Errorf("the curated leaf was not admitted: routable=%d excluded=%v", s.res.Routable, s.res.Excluded)
	}
	rt := s.h.set.Routable(towerA)
	if len(rt) != 1 || rt[0].CuratedProvider != "openrouter" || rt[0].TowerID != towerA {
		return fmt.Errorf("not a curated station under the tower's identity: %+v", rt)
	}
	return nil
}

func (s *curJoinedState) pricingRuleApplies() error {
	rt := s.h.set.Routable(towerA)
	if len(rt) != 1 || rt[0].PriceIn != 1300 || rt[0].PriceOut != 2080 || rt[0].EarnIn != 1000 || rt[0].EarnOut != 1600 {
		return fmt.Errorf("the admitted leaf does not carry list + fee / pass-through: %+v", rt)
	}
	// And a curated leaf whose posted price is NOT the derivation is refused at the door
	// (a fresh harness: this is a first-revision admission question, not a chain one).
	s.h = newHarness(s.t)
	bad := s.h.offer(stationA, "offer-bad", offerSpec{pre: func(m map[string]any) {
		m["curated_provider"] = "openrouter"
		m["earn_in"], m["earn_out"] = "1000", "1600"
		m["price_in"], m["price_out"] = "1400", "2080" // above the derivation: a hidden margin
	}})
	res, err := s.h.set.AcceptFull(towerA, s.h.towerPub(), s.h.inventory(invSpec{revision: 7, leaves: []map[string]any{bad}}))
	if err != nil {
		return err
	}
	if res.Routable != 0 || len(res.Excluded) != 1 {
		return fmt.Errorf("a mispriced curated leaf was admitted: %+v", res)
	}
	return nil
}

func TestCuratedTowerJoinedFeature(t *testing.T) {
	st := &curJoinedState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.h, st.res = nil, Result{}
				return c, nil
			})
			sc.Step(`^a tower joined to the Core with an upstream key configured$`, st.towerJoined)
			sc.Step(`^it registers the upstream's models$`, st.registersUpstreamModels)
			sc.Step(`^they appear as curated stations under the tower's identity$`, st.appearCuratedUnderTower)
			sc.Step(`^the curated pricing rule applies to them$`, st.pricingRuleApplies)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@joined",
			Paths: []string{"../../../features/curated/curated_tower.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the @joined curated tower scenario failed")
	}
}
