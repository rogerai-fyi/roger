package tui

// operator_bdd_test.go - the godog harness for the Guest Operators Phase 2 spec set
// (features/operator/*.feature, founder-approved 2026-07-07). GREEN wiring: step
// definitions split by ownership - detection/config scenarios bind against
// internal/operator (pure package, real filesystem), command/picker/handoff/interlock
// scenarios drive the REAL bubbletea model (the rc_confirm_bdd_test.go pattern) with a
// REAL ProxyOptionsHolder + hardened proxy over a stub billing broker and a REAL
// client.RCBridge polling a stub RC broker. The only test seams are the operator.go
// package seams (exec, terminal writer, scratch root, detect env, stage delay) - the
// production paths run for real underneath them, no mocks.

import (
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/operator"
)

func TestGuestOperatorBDD(t *testing.T) {
	// Sandbox the process HOME/config for the duration: the real money path signs relay
	// requests with the local user key (LoadOrCreateUserKey) and must never touch the
	// developer's real config.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	st := &opBDD{}

	// Rebind the operator seams at the suite boundary and restore after: the exec seam
	// records the composed child command instead of suspending the test terminal; the
	// terminal writer captures the reset preamble; the detect env resolves each
	// scenario's declared PATH; the stage delay shrinks so picker-enter round-trips fast.
	saveExec, saveTerm, saveEnv, saveRoot, saveDelay, saveWorkdir :=
		operatorExec, operatorTermOut, operatorDetectEnv, operatorScratchRoot, operatorStageDelay, operatorWorkdir
	operatorExec = st.seamExec
	operatorTermOut = opTermTap{st}
	operatorDetectEnv = func() operator.Env { return st.tuiDetectEnv() }
	operatorStageDelay = 5 * time.Millisecond
	// Phase 3: the plate's workdir resolves each scenario's declared sandbox (a project
	// dir by default; the sandbox home for the $HOME double-confirm scenarios).
	operatorWorkdir = func() string { return st.launchWorkdir }
	t.Cleanup(func() {
		operatorExec, operatorTermOut, operatorDetectEnv, operatorScratchRoot, operatorStageDelay, operatorWorkdir =
			saveExec, saveTerm, saveEnv, saveRoot, saveDelay, saveWorkdir
		st.closeServers()
	})

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			initializeOperatorScenarios(t, st, sc)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/operator"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("guest-operator scenarios failed (see godog output above)")
	}
}
