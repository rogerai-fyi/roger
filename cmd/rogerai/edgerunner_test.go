package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func opTask(t *testing.T, prompt, model string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(edge.InvokeTask{Prompt: prompt, Model: model})
	require.NoError(t, err)
	return b
}

// The delegate arrow: an authorized operate task runs the receiver's turn and returns its output.
func TestEdgeInvokeHandlerRunsAnOperateTask(t *testing.T) {
	var sawURL, sawModel, sawPrompt string
	h := edgeInvokeHandler(edgeTurnDeps{
		band: func(want string) (string, string, string, bool) {
			return "http://127.0.0.1:9/v1/chat/completions", "k", "gpt-oss-120b", true
		},
		runLoop: func(ctx context.Context, chatURL, key, model, prompt string) (string, error) {
			sawURL, sawModel, sawPrompt = chatURL, model, prompt
			return "the workstation summarized the logs", nil
		},
	})
	res, err := h("n_caller", edge.InvokeRequest{Capability: string(edge.Operate), Task: opTask(t, "summarize the logs", "")})
	require.NoError(t, err)
	require.Empty(t, res.Refused)
	var out string
	require.NoError(t, json.Unmarshal(res.Output, &out))
	require.Equal(t, "the workstation summarized the logs", out)
	require.Equal(t, "gpt-oss-120b", sawModel)
	require.Equal(t, "summarize the logs", sawPrompt)
	require.NotEmpty(t, sawURL)
}

// A device capability is refused by name - this binary runs an agent, it is not a board.
func TestEdgeInvokeHandlerRefusesDeviceCapabilities(t *testing.T) {
	ran := false
	h := edgeInvokeHandler(edgeTurnDeps{
		band:    func(string) (string, string, string, bool) { ran = true; return "", "", "", true },
		runLoop: func(context.Context, string, string, string, string) (string, error) { ran = true; return "", nil },
	})
	for _, cap := range []string{string(edge.Actuate), string(edge.Sense)} {
		res, err := h("n_caller", edge.InvokeRequest{Capability: cap, Task: opTask(t, "act", "")})
		require.NoError(t, err)
		require.Contains(t, res.Refused, cap)
		require.Contains(t, res.Refused, "device")
	}
	require.False(t, ran, "a device capability never reaches the harness")
}

// Authorized to operate, but nothing on air to run the turn: a real infra failure, not a refusal.
func TestEdgeInvokeHandlerErrorsWhenNothingIsServing(t *testing.T) {
	h := edgeInvokeHandler(edgeTurnDeps{
		band:    func(string) (string, string, string, bool) { return "", "", "", false },
		runLoop: func(context.Context, string, string, string, string) (string, error) { return "x", nil },
	})
	_, err := h("n_caller", edge.InvokeRequest{Capability: string(edge.Operate), Task: opTask(t, "go", "")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no model is serving")
}

// A task-level refusal from the turn (the receiver's own rules) reaches the caller as Refused.
func TestEdgeInvokeHandlerSurfacesATurnRefusal(t *testing.T) {
	h := edgeInvokeHandler(edgeTurnDeps{
		band: func(string) (string, string, string, bool) { return "u", "k", "m", true },
		runLoop: func(context.Context, string, string, string, string) (string, error) {
			return "did nothing", &edge.TaskRefused{Reason: "the receiver refuses shell here"}
		},
	})
	res, err := h("n_caller", edge.InvokeRequest{Capability: string(edge.Operate), Task: opTask(t, "rm -rf /", "")})
	require.NoError(t, err)
	require.Equal(t, "the receiver refuses shell here", res.Refused)
}

// A model named in the task is passed through to band resolution.
func TestEdgeInvokeHandlerPassesTheRequestedModel(t *testing.T) {
	var want string
	h := edgeInvokeHandler(edgeTurnDeps{
		band: func(w string) (string, string, string, bool) { want = w; return "u", "k", w, true },
		runLoop: func(context.Context, string, string, string, string) (string, error) { return "ok", nil },
	})
	_, err := h("n_caller", edge.InvokeRequest{Capability: string(edge.Operate), Grant: store.Grant{ID: "g"}, Task: opTask(t, "hi", "qwen3-coder")})
	require.NoError(t, err)
	require.Equal(t, "qwen3-coder", want)
}
