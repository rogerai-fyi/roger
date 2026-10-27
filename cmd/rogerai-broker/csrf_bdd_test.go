package main

// Makes features/security/csrf_cookie_mutations.feature executable: each scenario names its
// pinning Go test(s) in csrfguard_test.go; the scenario passes only if they all pass.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

var csrfTests = map[string]func(*testing.T){
	"TestCSRFGuardDecisionTable":                    TestCSRFGuardDecisionTable,
	"TestOnlyTheSessionCookieTriggersTheGuard":      TestOnlyTheSessionCookieTriggersTheGuard,
	"TestRealDestructiveRoutesAreProtectedEndToEnd": TestRealDestructiveRoutesAreProtectedEndToEnd,
}

var csrfTestRe = regexp.MustCompile(`# test: (\S+)`)

func TestCSRFBDD(t *testing.T) {
	feature, err := os.ReadFile("../../features/security/csrf_cookie_mutations.feature")
	if err != nil {
		t.Fatal(err)
	}
	var perScenario [][]string
	for _, block := range strings.Split(string(feature), "  Scenario:")[1:] {
		m := csrfTestRe.FindStringSubmatch(block)
		if m == nil {
			t.Fatalf("a scenario names no pinning test: %.60s", block)
		}
		perScenario = append(perScenario, strings.Split(m[1], ","))
	}
	idx := -1
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { idx++; return ctx, nil })
			sc.Step(`^.*$`, func() error {
				for _, name := range perScenario[idx] {
					fn, ok := csrfTests[name]
					if !ok {
						return fmt.Errorf("scenario names %q but no such test is registered", name)
					}
					if !t.Run(name, fn) {
						return fmt.Errorf("%s failed", name)
					}
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/security/csrf_cookie_mutations.feature"}, TestingT: t, Output: os.Stdout},
	}
	if suite.Run() != 0 {
		t.Fatal("csrf scenarios failed (see godog output above)")
	}
}
