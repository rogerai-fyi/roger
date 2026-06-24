package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
)

// CostFunc receives the per-turn cost in credits (1 cr = $1) parsed from the relay's
// X-RogerAI-Cost header, so the TUI can keep a running session total. nil = ignore.
type CostFunc func(credits float64)

// brokerTimeout matches the client's chat timeout: CPU MoE inference can take well
// over a minute, and a tool-use turn is a normal completion under the hood.
const brokerTimeout = 300 * time.Second

// agentMaxTokens is the per-turn completion budget for the agent. It is the SAME shared
// ceiling the in-channel chat uses (client.MaxAnswerTokens) so the two surfaces never
// drift: deliberately generous (not the old 1024) because the channel's model is often a
// REASONING model (e.g. gpt-oss) whose hidden reasoning is billed into this same budget,
// and a low ceiling truncated the answer mid-word or returned it empty (the "list my home
// dir ... stopped at .gtk" bug). If a future relay surfaces a reasoning-effort hint,
// lowering effort would free even more answer budget - but raising the ceiling is the fix
// here, not a knob hunt.
const agentMaxTokens = client.MaxAnswerTokens

// BrokerCompleter returns a Completer that relays one completion through the broker's
// OpenAI-compatible endpoint (POST {broker}/v1/chat/completions), exactly like the
// TUI's plain chat - but it sends the `tools` array AND parses any `tool_calls` back.
//
// This dogfoods the marketplace: the agent runs on the model on the current channel
// (or any chosen/default model), billed + metered like any other relay. The broker
// passes the request body through to the node verbatim (it only reads model/stream)
// and returns the node's response body verbatim, so tools/tool_calls round-trip
// untouched - no broker change is needed. If the model on the channel is NOT
// tool-capable it simply returns content with no tool_calls, and the loop degrades to
// plain chat.
func BrokerCompleter(broker, user, model string, confidential bool, onCost CostFunc) Completer {
	httpClient := &http.Client{Timeout: brokerTimeout}
	return func(messages []Message, tools []map[string]any) (Message, error) {
		reqBody, _ := json.Marshal(map[string]any{
			"model":    model,
			"messages": messages,
			"tools":    tools,
			// Let the model choose whether to call a tool (vs forcing one); a non-tool
			// model just ignores this field.
			"tool_choice": "auto",
			"max_tokens":  agentMaxTokens,
		})
		req, _ := http.NewRequest(http.MethodPost, broker+"/v1/chat/completions", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		// Sign with the local user key so the broker derives the spending wallet from the
		// verified pubkey (the same P0-safe path the relay/Chat use). X-Roger-User is a
		// legacy hint only.
		client.SignRequest(req, reqBody)
		req.Header.Set("X-Roger-User", user)
		if confidential {
			req.Header.Set("X-Roger-Confidential", "1")
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				return Message{}, fmt.Errorf("no reply from the station within %s (it may be slow or offline) - try again or re-tune", brokerTimeout)
			}
			return Message{}, fmt.Errorf("could not reach the broker: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

		if onCost != nil {
			if c, perr := strconv.ParseFloat(resp.Header.Get("X-RogerAI-Cost"), 64); perr == nil && c > 0 {
				onCost(c)
			}
		}
		return parseCompletion(raw, resp.StatusCode)
	}
}

// parseCompletion turns a broker /v1/chat/completions response body into the next
// assistant Message (content + any tool_calls). It surfaces the broker/provider's own
// error text on an empty/error response so the agent names the real cause (no station,
// timeout, insufficient credits) instead of a blank turn - mirroring client.Chat.
func parseCompletion(raw []byte, status int) (Message, error) {
	var d struct {
		Choices []struct {
			Message struct {
				Role      string     `json:"role"`
				Content   string     `json:"content"`
				Reasoning string     `json:"reasoning"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &d)
	if len(d.Choices) == 0 {
		if d.Error.Message != "" {
			// A 402 (insufficient balance) gets the shared actionable topup hint appended,
			// mirroring client.Chat, so the agent surfaces a next step, not a dead end.
			return Message{}, fmt.Errorf("%s", client.WithTopupHint(status, d.Error.Message))
		}
		if status >= 400 {
			if msg := string(bytesTrim(raw)); msg != "" && len(msg) < 300 {
				return Message{}, fmt.Errorf("%s (status %d)", client.WithTopupHint(status, msg), status)
			}
			if status == http.StatusPaymentRequired {
				return Message{}, fmt.Errorf("%s", client.WithTopupHint(status, ""))
			}
			return Message{}, fmt.Errorf("the station returned status %d with no reply", status)
		}
		return Message{}, fmt.Errorf("the station sent an empty response (status %d)", status)
	}
	c := d.Choices[0].Message
	content := c.Content
	if content == "" && len(c.ToolCalls) == 0 {
		content = c.Reasoning
	}
	return Message{Role: "assistant", Content: content, ToolCalls: c.ToolCalls}, nil
}

// bytesTrim trims ASCII whitespace from a byte slice (small local helper to avoid an
// extra import just for the error-body trim).
func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && isSpace(b[i]) {
		i++
	}
	for j > i && isSpace(b[j-1]) {
		j--
	}
	return b[i:j]
}
