package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// LocalCompleter runs an agent turn DIRECTLY against a model on the operator's own machine,
// never through the broker.
//
// FOUNDER ASK (2026-08-07): "use my own models on the TUI agent without having to share
// them". The only way to reach your own model used to be to put it ON AIR - register it
// with the broker and let turns relay back to your own box. Even a PRIVATE band is a
// discovery choice, not an offline one (features/discovery/bands.feature): it still
// registers, still binds to your account, still obeys the price ceiling. Nothing offered a
// model that simply stays home.
//
// So this is deliberately the BrokerCompleter minus the marketplace:
//   - no client.SignRequest: there is no wallet to derive and nobody to authenticate to;
//   - no X-Roger-Max-Price-Out: nothing is being billed, so a price cap would be theatre;
//   - no X-Roger-User / X-Roger-Confidential: no broker is reading them;
//   - no onCost: the cost is genuinely zero - it is the operator's own hardware, and
//     reporting a fabricated number would be worse than reporting none.
//
// What it KEEPS is the part that matters: the tools array goes out and tool_calls come
// back (parsed by the same parseCompletion the relay uses), so the agent loop works
// identically on a local model - and ctx cancellation still aborts the turn on esc.
//
// chatURL is the FULL chat-completions URL (detect.Found.Chat, i.e. ".../v1/chat/completions"),
// not a base: local servers are discovered with their own paths and must not be rewritten.
// key is the upstream bearer detect found for a key-protected server (vLLM --api-key, a
// LiteLLM master key, LM Studio's API-key toggle); empty sends no Authorization header.
func LocalCompleter(chatURL, key, model string) Completer {
	httpClient := &http.Client{} // no client timeout: the per-call bound rides on ctx
	return func(ctx context.Context, messages []Message, tools []map[string]any) (Message, error) {
		reqBody, _ := json.Marshal(map[string]any{
			"model":       model,
			"messages":    messages,
			"tools":       tools,
			"tool_choice": "auto",
			"max_tokens":  agentMaxTokens,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(reqBody))
		if err != nil {
			return Message{}, fmt.Errorf("local model %s: %v", model, err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return Message{}, fmt.Errorf("turn cancelled")
			}
			// Name the LOCAL server as the thing that failed. A broker-shaped error would
			// send the operator to put a station on air, when the remedy is to start the
			// server on their own machine.
			return Message{}, fmt.Errorf("could not reach your local model server at %s (is it still running?): %v", chatURL, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return parseCompletion(raw, resp.StatusCode)
	}
}
