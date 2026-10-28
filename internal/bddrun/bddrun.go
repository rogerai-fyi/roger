// Package bddrun runs a godog suite and refuses a vacuous one. A runner that selects scenarios
// by line ("file.feature:42") or by tag passes silently when the selection matches nothing (godog
// prints "No scenarios"), so an edit that shifts a feature's lines can switch a runner off without
// anyone noticing. Run fails when a path:line selection does not point at a Scenario or Scenario
// Outline line (godog matches nothing else), and when the run executes no scenario at all.
package bddrun

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/cucumber/godog"
)

// Run is suite.Run with the vacuity checks; a non-zero result fails the caller as before.
func Run(suite godog.TestSuite) int {
	report := func(m string) { fmt.Fprintln(os.Stderr, "bddrun: "+m) }
	if suite.Options != nil && suite.Options.TestingT != nil {
		t := suite.Options.TestingT
		report = func(m string) { t.Helper(); t.Error("bddrun: " + m) }
	}
	return run(suite, report)
}

func run(suite godog.TestSuite, report func(string)) int {
	bad := false
	if suite.Options != nil {
		for _, p := range suite.Options.Paths {
			if msg := checkPath(p); msg != "" {
				report(msg)
				bad = true
			}
		}
	}
	if bad {
		return 2
	}
	var executed atomic.Int64
	inner := suite.ScenarioInitializer
	suite.ScenarioInitializer = func(sc *godog.ScenarioContext) {
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			executed.Add(1)
			return ctx, nil
		})
		if inner != nil {
			inner(sc)
		}
	}
	code := suite.Run()
	if executed.Load() == 0 {
		var sel []string
		if suite.Options != nil {
			sel = suite.Options.Paths
			if suite.Options.Tags != "" {
				sel = append(sel, "tags "+suite.Options.Tags)
			}
		}
		report(fmt.Sprintf("the suite executed no scenario (selection %v): a shifted line or a tag that matches nothing switches a runner off silently", sel))
		if code == 0 {
			code = 2
		}
	}
	return code
}

// checkPath validates one "file.feature:N" selection (a path without a line is left to godog).
func checkPath(p string) string {
	i := strings.LastIndex(p, ":")
	if i < 0 {
		return ""
	}
	n, err := strconv.Atoi(p[i+1:])
	if err != nil {
		return ""
	}
	file := p[:i]
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Sprintf("%s: %v", p, err)
	}
	lines := strings.Split(string(b), "\n")
	if !selectable(lines, n) {
		return fmt.Sprintf("%s: line %d is not a Scenario or Scenario Outline line (godog selects nothing else; did the feature's lines shift?)", file, n)
	}
	return ""
}

func selectable(lines []string, n int) bool {
	if n < 1 || n > len(lines) {
		return false
	}
	// godog matches a line only against a scenario's own keyword line (internal/parser:
	// sc.Location.Line); an Examples header or row, a Feature or Rule line selects nothing.
	l := strings.TrimSpace(lines[n-1])
	for _, kw := range []string{"Scenario:", "Scenario Outline:", "Scenario Template:", "Example:"} {
		if strings.HasPrefix(l, kw) {
			return true
		}
	}
	return false
}
