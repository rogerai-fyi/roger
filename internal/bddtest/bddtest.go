// Package bddtest runs godog suites so a runner can never pass by running nothing: a suite
// whose paths, tags or line selectors match no scenario (a shifted line, a renamed tag)
// reports godog's "No scenarios" and status 0, which would read as green.
package bddtest

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"
)

// Run runs s and returns godog's status, or 1 when the suite executed zero scenarios.
func Run(t testing.TB, s *godog.TestSuite) int {
	t.Helper()
	var ran atomic.Int64
	init := s.ScenarioInitializer
	s.ScenarioInitializer = func(sc *godog.ScenarioContext) {
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			ran.Add(1)
			return ctx, nil
		})
		if init != nil {
			init(sc)
		}
	}
	status := s.Run()
	if status == 0 && ran.Load() == 0 {
		t.Errorf("godog suite %q ran zero scenarios: its paths, tags or line selectors match nothing", s.Name)
		return 1
	}
	return status
}
