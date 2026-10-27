package edge

// THE RUNNER - the callee side of the delegate arrow (features/edge/control.feature). Once the face
// has authorized an invoke, SOMETHING has to run the task, and it must run in the RECEIVER's own
// harness, with the receiver's own tools and the receiver's own permissions - a delegated task is
// not a second way in. That harness is heavy and host-specific (it needs a model, a workdir, a
// confirm policy), so it is INJECTED as an AgentTurn; this file is the thin, testable boundary that
// decodes the task, hands it to that turn, and shapes the outcome the wire promises:
//
//   - the turn ran and produced output          -> InvokeResult{Output}
//   - the turn ran but the receiver's rules or a
//     budget refused it                          -> InvokeResult{Output: what it did, Refused: why}
//   - the turn could not run at all (no model)   -> a real error (the face answers 502)
//
// The middle case is the one that matters: control.feature says a refused delegated task must reach
// the caller "in the result the caller gets, not hidden". A refusal is data, not an error.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"rogerai.fm/roger/v7/internal/store"
)

// InvokeTask is the delegated work a caller sends: a prompt the receiver's agent runs in its own
// harness. It is the payload inside InvokeRequest.Task; keeping it a named type lets both ends agree
// on the shape without the face knowing what a task means.
type InvokeTask struct {
	Prompt string `json:"prompt"`
	// Model optionally names which band on the receiver to run the turn against, when the receiver
	// serves more than one. Empty means "the receiver's own default band".
	Model string `json:"model,omitempty"`
}

// TaskRefused is how an AgentTurn signals that the task RAN under the receiver's rules and those
// rules (a sandbox boundary, an actuate contract, a budget) refused it. The runner turns it into
// InvokeResult.Refused so the caller is told why, with whatever the task did first still returned.
// It is deliberately distinct from a plain error, which means the turn could not run at all.
type TaskRefused struct {
	Reason string
}

func (e *TaskRefused) Error() string { return e.Reason }

// AgentTurn runs one delegated turn in this node's own harness and returns its text output. A
// *TaskRefused error means the receiver's rules refused it (returned to the caller as Refused); any
// other error means the turn could not run (returned to the caller as an infrastructure failure).
type AgentTurn func(ctx context.Context, callerID string, task InvokeTask) (string, error)

// GrantedTurn is AgentTurn plus the authority-signed grant the task carries, for a harness that
// scopes budget or onward delegation by the grant. Most callers want the simpler AgentTurn.
type GrantedTurn func(ctx context.Context, callerID string, grant store.Grant, task InvokeTask) (string, error)

// NewRunner builds a Serving.Invoke from an AgentTurn.
func NewRunner(turn AgentTurn) func(callerID string, req InvokeRequest) (InvokeResult, error) {
	return NewRunnerWithGrant(func(ctx context.Context, callerID string, _ store.Grant, task InvokeTask) (string, error) {
		return turn(ctx, callerID, task)
	})
}

// NewRunnerWithGrant builds a Serving.Invoke from a GrantedTurn, giving the harness the grant.
func NewRunnerWithGrant(turn GrantedTurn) func(callerID string, req InvokeRequest) (InvokeResult, error) {
	return func(callerID string, req InvokeRequest) (InvokeResult, error) {
		var task InvokeTask
		if err := json.Unmarshal(req.Task, &task); err != nil {
			// A malformed task never reaches the harness; it is refused, not an infra error.
			return InvokeResult{Refused: "that task could not be read"}, nil
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return InvokeResult{Refused: "that task is empty"}, nil
		}
		out, err := turn(context.Background(), callerID, req.Grant, task)
		if err != nil {
			var refused *TaskRefused
			if errors.As(err, &refused) {
				// The task ran and the receiver's own rules refused it - name it, keep what it did.
				return InvokeResult{Output: mustJSON(out), Refused: refused.Reason}, nil
			}
			return InvokeResult{}, err
		}
		return InvokeResult{Output: mustJSON(out)}, nil
	}
}

func mustJSON(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil { // a Go string always marshals; this is belt-and-braces
		b = []byte(fmt.Sprintf("%q", s))
	}
	return b
}
