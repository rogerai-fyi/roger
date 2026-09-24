package main

// THE HOST-SIDE RUNNER for /edge/invoke (features/edge/control.feature, the delegate arrow). The
// edge package owns the thin, tested boundary (edge.NewRunner: decode the task, run it, shape a
// task-refusal into the result); THIS file wires the heavy, host-specific harness that actually
// runs a delegated turn - in THIS roger's own tools and permissions, with no human at the keyboard.
//
// Two rules from control.feature shape it:
//   - "a delegated task is not a second way in": the turn runs HEADLESS, so any tool that would
//     need a human confirm (write_file, run_shell) is DENIED, exactly as it would be for a headless
//     local turn. The confirmer answers no; the loop's gate ORs with Mutating, so nothing
//     side-effecting slips through.
//   - "an invoke is bounded in time": the turn carries a deadline, so the callee is never left
//     holding a task nobody is waiting for.
//
// This roger fulfils the DELEGATE arrow (it runs an agent: capability operate). The act-to-device
// arrows (sense/actuate) belong to board nodes running the device firmware, not this binary, so an
// invoke for one is refused with a sentence that names why - never silently, never as if it ran.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/harness"
)

// edgeDelegateTimeout bounds a delegated turn (control.feature: an invoke is bounded in time).
const edgeDelegateTimeout = 5 * time.Minute

// edgeTurnDeps are the receiver-side pieces a delegated turn needs. They are injected so the
// dispatch and its refusals are testable without a live model or a real harness.
type edgeTurnDeps struct {
	// band resolves where a delegated turn runs on THIS node: the OpenAI-compatible URL, the key and
	// the model of a band this process has on air. ok is false when nothing is serving here.
	band func(want string) (chatURL, key, model string, ok bool)
	// runLoop runs one headless agent turn against that band and returns its text output. Injected so
	// the live harness stays out of the unit tests.
	runLoop func(ctx context.Context, chatURL, key, model, prompt string) (string, error)
}

// edgeInvokeHandler builds Serving.Invoke from the receiver-side deps: it dispatches on capability,
// then runs an operate task in this node's headless harness.
func edgeInvokeHandler(d edgeTurnDeps) func(callerID string, req edge.InvokeRequest) (edge.InvokeResult, error) {
	run := edge.NewRunner(func(ctx context.Context, callerID string, task edge.InvokeTask) (string, error) {
		chatURL, key, model, ok := d.band(task.Model)
		if !ok {
			// Authorized to operate, but nothing is serving here to run the turn: a real infra failure
			// (the face answers 502), distinct from a task that ran and refused.
			return "", fmt.Errorf("no model is serving on this node right now")
		}
		ctx, cancel := context.WithTimeout(ctx, edgeDelegateTimeout)
		defer cancel()
		return d.runLoop(ctx, chatURL, key, model, task.Prompt)
	})
	return func(callerID string, req edge.InvokeRequest) (edge.InvokeResult, error) {
		if req.Capability != string(edge.Operate) {
			// This roger runs an agent; it is not a device. Name it, do not pretend it ran.
			return edge.InvokeResult{Refused: fmt.Sprintf(
				"this node runs an agent; it does not %s (that is a device capability, for a board node)",
				req.Capability)}, nil
		}
		return run(callerID, req)
	}
}

// edgeLiveTurnDeps are the real receiver-side pieces, backed by what this process has on air and the
// live harness loop. This is the glue edgeInvokeHandler is fed in production.
func edgeLiveTurnDeps() edgeTurnDeps {
	return edgeTurnDeps{
		band: func(want string) (string, string, string, bool) {
			if edgeUpstreamOf == nil {
				return "", "", "", false
			}
			bands := edgeBandsOnAir()
			model := want
			if model == "" {
				if len(bands) == 0 {
					return "", "", "", false
				}
				model = bands[0]
			}
			u, k, ok := edgeUpstreamOf(model)
			if !ok {
				return "", "", "", false
			}
			return u, k, model, true
		},
		runLoop: edgeRunDelegatedTurn,
	}
}

// edgeRunDelegatedTurn runs one headless agent turn in this roger's own harness. It is deliberately
// spare: the receiver's persona, its default workdir, and a confirmer that never says yes - a
// delegated task cannot answer a gate, so a tool that needs one is denied, which is the receiver's
// own rule applied. The loop's own guards (writes stay under root) run underneath, unchanged.
func edgeRunDelegatedTurn(ctx context.Context, chatURL, key, model, prompt string) (string, error) {
	completer := harness.LocalCompleter(chatURL, key, model)
	persona := harness.LoadPersona(harness.PersonaPath())
	root := edgeDelegateRoot()
	confirmer := func(string, map[string]any) bool { return false } // headless: no human to confirm
	loop := harness.NewLoop(root, persona, completer, confirmer)
	defer loop.Close()
	return loop.Send(ctx, prompt, func(harness.Event) {})
}

// edgeDelegateRoot is the sandbox a delegated turn runs in: a dedicated directory under the config
// dir, never the daemon's arbitrary cwd. Mutating tools are denied headless anyway, so this mostly
// bounds what a delegated turn can READ; keeping it its own dir means a delegated task never sees
// the operator's working tree by accident. Falls back to "." only if the dir cannot be created.
func edgeDelegateRoot() string {
	dir := filepath.Join(filepath.Dir(configPath()), "edge-delegate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "."
	}
	return dir
}
