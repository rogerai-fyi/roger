package bddtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/require"
)

const feature = `Feature: f
  @keep
  Scenario: one
    Given a step
`

func suite(t *testing.T, tags string) godog.TestSuite {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.feature")
	require.NoError(t, os.WriteFile(p, []byte(feature), 0o600))
	return godog.TestSuite{
		Name:                "probe",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { sc.Step(`^a step$`, func() error { return nil }) },
		Options:             &godog.Options{Format: "progress", Paths: []string{p}, Tags: tags, Strict: true, Output: nopWriter{}},
	}
}

type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }

// TestRunFailsASuiteThatRanNothing: a tag that matches nothing passes plain godog (status 0)
// but fails through Run.
func TestRunFailsASuiteThatRanNothing(t *testing.T) {
	s := suite(t, "@nothing")
	require.Equal(t, 0, s.Run(), "plain godog passes a suite that ran nothing")
	ft := &testing.T{}
	s = suite(t, "@nothing")
	require.Equal(t, 1, Run(ft, &s))
	require.True(t, ft.Failed())
}

// TestRunPassesASuiteThatRanScenarios: a matching suite keeps godog's status.
func TestRunPassesASuiteThatRanScenarios(t *testing.T) {
	s := suite(t, "@keep")
	require.Equal(t, 0, Run(t, &s))
}
