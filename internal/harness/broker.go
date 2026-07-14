package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
)

// CostFunc receives one model-call's BILLED result parsed from the relay's response
// headers: the cost in credits (1 cr = $1, X-RogerAI-Cost) plus the broker's BILLED
// prompt/completion token counts (X-RogerAI-Tokens-In / X-RogerAI-Tokens-Out) — the
// very counts that cost was computed from (min(claim, broker re-count) per axis). The
// TUI keeps running session totals from these to show an honest ↑in ↓out beside the
// cost. nil = ignore. This is DISPLAY of an already-settled value; it changes no billing.
type CostFunc func(credits float64, tokensIn, tokensOut int, tps float64)

// brokerTimeout matches the client's chat timeout: CPU MoE inference can take well
// over a minute, and a tool-use turn is a normal completion under the hood.
const brokerTimeout = 300 * time.Second

// PerCallCap is the per-model-call cap the TUI surfaces ("cap 300s") - so a slow turn
// reads as bounded, not bottomless, and the "is it stuck?" question has a concrete
// deadline. It is a SOFT ceiling when the caller supplies its own ctx deadline: the
// TUI threads an ExtendableTimeout so the user can grant a legitimately slow call more
// time at the cap (tab in the working line) instead of it being hard-killed. Callers
// that supply NO deadline still get the hard default (brokerHTTPTimeout), so every
// non-interactive path stays bounded exactly as before. Mirrors brokerTimeout.
const PerCallCap = brokerTimeout

// agentMaxTokens is the per-turn completion budget for the agent. It is the SAME shared
// ceiling the in-channel chat uses (client.MaxAnswerTokens) so the two surfaces never
// drift: deliberately generous (not the old 1024) because the channel's model is often a
// REASONING model (e.g. gpt-oss) whose hidden reasoning is billed into this same budget,
// and a low ceiling truncated the answer mid-word or returned it empty (the "list my home
// dir ... stopped at .gtk" bug). If a future relay surfaces a reasoning-effort hint,
// lowering effort would free even more answer budget - but raising the ceiling is the fix
// here, not a knob hunt.
const agentMaxTokens = client.MaxAnswerTokens

// brokerHTTPTimeout is the per-request timeout for the agent relay. It defaults to
// brokerTimeout and exists as a var ONLY so a test can shorten it to exercise the
// "no reply within ..." timeout branch; production is byte-for-byte unchanged (the
// default value is the constant).
var brokerHTTPTimeout = brokerTimeout

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
// maxOut is the consumer out-price cap ($/1M) the agent relay must carry so the
// [0] AGENT harness is bounded against overpay like every other consume path: 0 means
// "use the default consumer cap" (client.EffectiveMaxOut), a positive value is the
// user's explicit opt-in. Without this an agent turn could silently bind to an
// exorbitant band (the harness relay previously sent no max-out at all).
func BrokerCompleter(broker, user, model string, confidential bool, maxOut float64, onCost CostFunc) Completer {
	// No client-level Timeout: the per-call bound rides on the ctx, so an interactive
	// caller (the TUI) can extend it mid-call. A ctx that arrives with no deadline gets
	// the hard default below - non-interactive paths stay bounded exactly as before.
	httpClient := &http.Client{}
	return func(ctx context.Context, messages []Message, tools []map[string]any) (Message, error) {
		if _, has := ctx.Deadline(); !has {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, brokerHTTPTimeout)
			defer cancel()
		}
		reqBody, _ := json.Marshal(map[string]any{
			"model":    model,
			"messages": messages,
			"tools":    tools,
			// Let the model choose whether to call a tool (vs forcing one); a non-tool
			// model just ignores this field.
			"tool_choice": "auto",
			"max_tokens":  agentMaxTokens,
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, broker+"/v1/chat/completions", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		// Sign with the local user key so the broker derives the spending wallet from the
		// verified pubkey (the same P0-safe path the relay/Chat use). X-Roger-User is a
		// legacy hint only.
		client.SignRequest(req, reqBody)
		req.Header.Set("X-Roger-User", user)
		if confidential {
			req.Header.Set("X-Roger-Confidential", "1")
		}
		// Always carry an out-price cap (the caller's, or the default consumer ceiling
		// when none was set) so an agent turn is bounded against overpay exactly like
		// `roger use` and the in-channel chat - the harness is just another consume path.
		req.Header.Set("X-Roger-Max-Price-Out", fmt.Sprintf("%g", client.EffectiveMaxOut(maxOut)))
		resp, err := httpClient.Do(req)
		if err != nil {
			// User aborted the turn (esc): a clean cancellation, not a network failure. An
			// ExtendableTimeout that expired cancels with cause DeadlineExceeded - that is
			// a timeout, not an abort, so it falls through to the timeout branch below.
			if errors.Is(err, context.Canceled) && !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return Message{}, fmt.Errorf("turn cancelled")
			}
			timedOut := errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(context.Cause(ctx), context.DeadlineExceeded)
			if ne, ok := err.(interface{ Timeout() bool }); timedOut || (ok && ne.Timeout()) {
				return Message{}, fmt.Errorf("no reply from the station within %s (it may be slow or offline) - try again or re-tune", brokerTimeout)
			}
			return Message{}, fmt.Errorf("could not reach the broker: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

		if onCost != nil {
			// The cost, the BILLED token counts, and throughput settle together on the relay
			// and ride back as sibling headers; forward all four when any is present (a missing/
			// blank header parses to 0). A VOID turn emits cost=0 with no token headers and so
			// reports nothing. TPS is the LATEST call's throughput (not summed).
			c, _ := strconv.ParseFloat(resp.Header.Get("X-RogerAI-Cost"), 64)
			in, _ := strconv.Atoi(resp.Header.Get("X-RogerAI-Tokens-In"))
			out, _ := strconv.Atoi(resp.Header.Get("X-RogerAI-Tokens-Out"))
			tps, _ := strconv.ParseFloat(resp.Header.Get("X-RogerAI-TPS"), 64)
			if c > 0 || in > 0 || out > 0 || tps > 0 {
				onCost(c, in, out, tps)
			}
		}
		return parseCompletion(raw, resp.StatusCode)
	}
}

// parseCompletion turns a broker /v1/chat/completions response body into the next
// assistant Message (content + any tool_calls). It surfaces the broker/provider's own
// error text on an empty/error response so the agent names the real cause (no station,
// timeout, insufficient credits) instead of a blank turn - mirroring client.ChatDetailed.
func parseCompletion(raw []byte, status int) (Message, error) {
	var d struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
				// Thinking models return their reasoning under either key depending
				// on the backend: llama.cpp's reasoning-format emits
				// `reasoning_content` (DeepSeek/Qwen style), others use `reasoning`.
				// Missing the first one made thought-only replies read as empty.
				Reasoning        string     `json:"reasoning"`
				ReasoningContent string     `json:"reasoning_content"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &d)
	if len(d.Choices) == 0 {
		if d.Error.Message != "" {
			// A 402 (insufficient balance) gets the shared actionable topup hint appended,
			// mirroring client.ChatDetailed, so the agent surfaces a next step, not a dead end.
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
	msg := Message{
		Role:      "assistant",
		Content:   c.Content,
		ToolCalls: c.ToolCalls,
		Truncated: d.Choices[0].FinishReason == "length",
	}
	if msg.Content == "" && len(c.ToolCalls) == 0 {
		// Keep the reasoning OUT of Content: the loop surfaces it as a marked Thought
		// (thinking aloud), never as a spoken answer fed back into the conversation.
		if t := strings.TrimSpace(c.ReasoningContent); t != "" {
			msg.Thought = t
		} else {
			msg.Thought = strings.TrimSpace(c.Reasoning)
		}
	}
	return msg, nil
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
