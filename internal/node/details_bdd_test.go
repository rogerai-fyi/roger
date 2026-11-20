package node

// details_bdd_test.go makes the @node scenarios of features/details/offer_details.feature
// EXECUTABLE against the REAL Controller: a details-only declaration keeps the inferred
// taxonomy on air and still carries its words. Reuses declState (declaration_bdd_test.go).

import (
	"context"
	"fmt"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
)

func (s *declState) declareOnlySummary(model, sum string) error {
	return s.declare(model, protocol.Taxonomy{Summary: sum})
}
func (s *declState) declareCatSummary(model, cat, sum string) error {
	c, sub := protocol.SplitCategory(cat)
	return s.declare(model, protocol.Taxonomy{Category: c, Subcategory: sub, Summary: sum})
}
func (s *declState) onAirSummary(model, sum string) error {
	if got := s.cfgs[model].Taxonomy.Summary; got != sum {
		return fmt.Errorf("on-air summary %q, got %q", sum, got)
	}
	return nil
}
func (s *declState) onAirDetailsSource(model, src string) error {
	if got := s.cfgs[model].Taxonomy.DetailsSource; got != src {
		return fmt.Errorf("on-air details source %q, got %q", src, got)
	}
	return nil
}
func (s *declState) onAirNoDetails(model string) error {
	if t := s.cfgs[model].Taxonomy; t.DetailsDeclared() {
		return fmt.Errorf("on-air details %q / %q / %q, want none", t.Summary, t.BestAt, t.NotFor)
	}
	return nil
}

func TestDetailsNodeBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &declState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = declState{}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a node registering an offer with the broker$`, func() error { return nil })
			sc.Step(`^a share controller with a detected model `+q+` inferred as `+q+`$`, st.background)
			sc.Step(`^the operator declares for `+q+` only summary `+q+`$`, st.declareOnlySummary)
			sc.Step(`^the operator declares for `+q+` category `+q+` and summary `+q+`$`, st.declareCatSummary)
			sc.Step(`^the operator clears the declaration for `+q+`$`, st.clear)
			sc.Step(`^the operator puts `+q+` on air$`, st.onAir)
			sc.Step(`^the on-air taxonomy for `+q+` is category `+q+`, subcategory `+q+` and source `+q+`$`, st.onAirTax)
			sc.Step(`^the on-air summary for `+q+` is `+q+`$`, st.onAirSummary)
			sc.Step(`^the on-air details source for `+q+` is `+q+`$`, st.onAirDetailsSource)
			sc.Step(`^the on-air offer for `+q+` has no summary, best_at or not_for$`, st.onAirNoDetails)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/details/offer_details.feature"},
			Tags:     "@node",
			TestingT: t,
			Strict:   true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("details @node behavior scenarios failed (see godog output above)")
	}
}
