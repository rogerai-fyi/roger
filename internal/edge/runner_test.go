package edge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

// The runner is the callee side of the delegate arrow (features/edge/control.feature): it decodes
// the delegated task, runs it in THIS node's own harness (the injected AgentTurn), and shapes the
// outcome. A task that the receiver's own rules refuse (a sandbox write, a budget stop) comes back
// to the caller as a NAMED refusal in the result - never hidden, never a bare infra error.

func delegateTask(t *testing.T, prompt string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(InvokeTask{Prompt: prompt})
	require.NoError(t, err)
	return b
}

func TestRunnerRunsTheTurnAndReturnsItsOutput(t *testing.T) {
	var gotCaller, gotPrompt string
	run := NewRunner(func(ctx context.Context, callerID string, task InvokeTask) (string, error) {
		gotCaller, gotPrompt = callerID, task.Prompt
		return "the workstation did it", nil
	})
	res, err := run("n_caller", InvokeRequest{Capability: "operate", Task: delegateTask(t, "tidy the logs")})
	require.NoError(t, err)
	require.Empty(t, res.Refused)

	var out string
	require.NoError(t, json.Unmarshal(res.Output, &out))
	require.Equal(t, "the workstation did it", out)
	require.Equal(t, "n_caller", gotCaller, "the turn is told who delegated")
	require.Equal(t, "tidy the logs", gotPrompt)
}

func TestRunnerSurfacesATaskRefusalInTheResultNotHidden(t *testing.T) {
	// The receiver's own rules refuse a write outside its workdir. control.feature: "the refusal is
	// in the result the caller gets, not hidden" - so it lands in Refused, and the call itself is nil-error.
	run := NewRunner(func(ctx context.Context, callerID string, task InvokeTask) (string, error) {
		return "wrote nothing", &TaskRefused{Reason: "write outside the workdir is refused"}
	})
	res, err := run("n_caller", InvokeRequest{Capability: "operate", Task: delegateTask(t, "write /etc/passwd")})
	require.NoError(t, err, "a task refusal is not an infrastructure error")
	require.Equal(t, "write outside the workdir is refused", res.Refused)

	var out string
	require.NoError(t, json.Unmarshal(res.Output, &out))
	require.Equal(t, "wrote nothing", out, "what the task did so far is still returned")
}

func TestRunnerReturnsAnErrorWhenTheTurnFailsToRun(t *testing.T) {
	// An infrastructure failure (no model, harness could not start) is a real error - the face turns
	// it into a 502, distinct from a task that ran and refused.
	run := NewRunner(func(ctx context.Context, callerID string, task InvokeTask) (string, error) {
		return "", errors.New("no model is serving here")
	})
	_, err := run("n_caller", InvokeRequest{Capability: "operate", Task: delegateTask(t, "hi")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no model is serving here")
}

func TestRunnerRefusesAMalformedTask(t *testing.T) {
	run := NewRunner(func(ctx context.Context, callerID string, task InvokeTask) (string, error) {
		t.Fatal("the turn must not run on an unreadable task")
		return "", nil
	})
	res, err := run("n_caller", InvokeRequest{Capability: "operate", Task: json.RawMessage(`not json`)})
	require.NoError(t, err)
	require.Contains(t, res.Refused, "could not be read")
}

func TestRunnerRefusesAnEmptyPrompt(t *testing.T) {
	run := NewRunner(func(ctx context.Context, callerID string, task InvokeTask) (string, error) {
		t.Fatal("the turn must not run on an empty prompt")
		return "", nil
	})
	res, err := run("n_caller", InvokeRequest{Capability: "operate", Task: delegateTask(t, "   ")})
	require.NoError(t, err)
	require.Contains(t, res.Refused, "empty")
}

// A grant carrying a Self flag never debits: the runner leaves budget to the harness, but the
// task shape must round-trip the caller so a receipt attributes the work (control.feature).
func TestRunnerCarriesGrantContextToTheTurn(t *testing.T) {
	var sawGrant string
	run := NewRunnerWithGrant(func(ctx context.Context, callerID string, g store.Grant, task InvokeTask) (string, error) {
		sawGrant = g.ID
		return "done", nil
	})
	_, err := run("n_caller", InvokeRequest{Capability: "operate", Grant: store.Grant{ID: "g-77"}, Task: delegateTask(t, "go")})
	require.NoError(t, err)
	require.Equal(t, "g-77", sawGrant)
}
