package main

// Makes features/auth/logged_in_states.feature executable: each scenario runs the real pages
// through the tests that pin it (web/test/*.test.mjs guards and web/test/unit/*.test.js
// island component tests, comma-separated; skipped, not faked, when node is absent); the
// /account scenario runs its Go test directly.

import (
	"context"
	"os"
	"regexp"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

var lisTestRe = regexp.MustCompile(`# test: (\S+)`)

func TestLoggedInStatesBDD(t *testing.T) {
	feature, err := os.ReadFile("../../features/auth/logged_in_states.feature")
	if err != nil {
		t.Fatal(err)
	}
	// Every scenario names its pinning test; collect them per scenario in order.
	var perScenario [][]string
	for _, block := range strings.Split(string(feature), "  Scenario:")[1:] {
		m := lisTestRe.FindStringSubmatch(block)
		if m == nil {
			t.Fatalf("a scenario names no pinning test: %.60s", block)
		}
		perScenario = append(perScenario, strings.Split(m[1], ","))
	}
	idx := -1
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { idx++; return ctx, nil })
			// Every step of a scenario asserts the same thing: its pinning test passes.
			sc.Step(`^.*$`, func() error {
				for _, target := range perScenario[idx] {
					if strings.HasSuffix(target, "_test.go") {
						continue // run by `go test` itself in this package
					}
					if err := webSpecsPass(target); err != nil {
						return err
					}
				}
				return nil
			})
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/auth/logged_in_states.feature"},
			TestingT: t, Output: os.Stdout,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("logged-in-states scenarios failed (see godog output above)")
	}
}
