package bddrun

import (
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

func suite(paths []string, tags string) godog.TestSuite {
	return godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Step(`^a step$`, func() error { return nil })
			sc.Step(`^a (\d+) step$`, func(string) error { return nil })
		},
		Options: &godog.Options{Format: "progress", Paths: paths, Tags: tags, Strict: true},
	}
}

// TestRunRefusesVacuousSelections pins the guard: a line that is not a Scenario or Scenario
// Outline keyword line is refused before running (godog would select nothing), and a selection
// that executes nothing fails; real selections pass.
func TestRunRefusesVacuousSelections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		tags  string
		ok    bool
		why   string
	}{
		{"scenario line", []string{"testdata/sample.feature:3"}, "", true, ""},
		{"outline line", []string{"testdata/sample.feature:6"}, "", true, ""},
		{"examples header", []string{"testdata/sample.feature:9"}, "", false, "line 9 is not a Scenario"},
		{"examples row", []string{"testdata/sample.feature:12"}, "", false, "line 12 is not a Scenario"},
		{"the feature line", []string{"testdata/sample.feature:1"}, "", false, "line 1 is not a Scenario"},
		{"whole file", []string{"testdata/sample.feature"}, "", true, ""},
		{"a step line", []string{"testdata/sample.feature:4"}, "", false, "line 4 is not a Scenario"},
		{"a blank line", []string{"testdata/sample.feature:5"}, "", false, "line 5 is not a Scenario"},
		{"past the end", []string{"testdata/sample.feature:99"}, "", false, "line 99 is not a Scenario"},
		{"the table header", []string{"testdata/sample.feature:10"}, "", false, "line 10 is not a Scenario"},
		{"a missing file", []string{"testdata/nope.feature:3"}, "", false, "nope.feature"},
		{"a tag that matches nothing", []string{"testdata/sample.feature"}, "@nothing", false, "executed no scenario"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var msgs []string
			code := run(suite(tc.paths, tc.tags), func(m string) { msgs = append(msgs, m) })
			if tc.ok {
				if code != 0 || len(msgs) > 0 {
					t.Fatalf("code %d, messages %v; want a clean run", code, msgs)
				}
				return
			}
			if code == 0 {
				t.Fatalf("a vacuous selection passed")
			}
			if len(msgs) == 0 || !strings.Contains(strings.Join(msgs, "\n"), tc.why) {
				t.Fatalf("messages %v do not say %q", msgs, tc.why)
			}
		})
	}
}

// TestRunWithoutTestingTReportsOnStderr covers the exported entry point: a clean suite returns
// 0, and a vacuous one returns non-zero (reported on stderr when no testing.T is wired).
func TestRunWithoutTestingTReportsOnStderr(t *testing.T) {
	if code := Run(suite([]string{"testdata/sample.feature:3"}, "")); code != 0 {
		t.Fatalf("a clean suite returned %d", code)
	}
	if code := Run(suite([]string{"testdata/sample.feature:4"}, "")); code == 0 {
		t.Fatal("a vacuous selection passed through Run")
	}
	if code := Run(godog.TestSuite{Options: &godog.Options{Format: "progress", Paths: []string{"testdata/sample.feature"}, Tags: "@nothing"}}); code == 0 {
		t.Fatal("a suite with no initializer and no matching scenario passed")
	}
}
