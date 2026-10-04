package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// The request-cost estimate (contract §14.7 and §14.11): one definition of how much output a
// request is expected to produce, used by the hold, by the default max_tokens a station is
// sent, and by price ranking, so the three can never disagree about a request.

// defaultWindow is the context window assumed for an offer that declares none (the hold's
// long-standing bound).
const defaultWindow = 8192

// defaultOutputTokens is ROGERAI_DEFAULT_OUTPUT_TOKENS (default 4096): the output budget of a
// request that states no limit of its own.
func defaultOutputTokens() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ROGERAI_DEFAULT_OUTPUT_TOKENS"))); err == nil && v > 0 {
		return v
	}
	return 4096
}

// statedOutputTokens is the output limit the request states: max_completion_tokens, else
// max_tokens; 0 when it states neither (or a non-positive value).
func statedOutputTokens(body []byte) int {
	var req struct {
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	_ = json.Unmarshal(body, &req)
	if req.MaxCompletionTokens > 0 {
		return req.MaxCompletionTokens
	}
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return 0
}

// expectedOutput is the output a request is expected to produce on one offer: what it states,
// else the default budget, never more than the window left after the prompt and never below 1.
// window <= 0 means the offer declared none.
func expectedOutput(stated, promptTokens, window int) int {
	if window <= 0 {
		window = defaultWindow
	}
	out := stated
	if out <= 0 {
		out = defaultOutputTokens()
	}
	if left := window - promptTokens; out > left {
		out = left
	}
	if out < 1 {
		out = 1
	}
	return out
}

// estRequestCost is a request's estimated cost in USD at one offer's $/1M prices: the measured
// prompt at the input price plus the expected output at the output price. It is what price
// ranks on (sort price, :floor and the default score's price term), so a station cannot win
// "cheapest" with a near-zero output price and an outsized input price.
func estRequestCost(promptTokens, outTokens int, in, out float64) float64 {
	return (float64(promptTokens)*in + float64(outTokens)*out) / 1e6
}

// withDefaultMaxTokens gives a body that states no output limit a max_tokens equal to the
// budget the hold was sized for (contract §14.11), so a station is never asked to generate
// more than the consumer is held for. A body that states a limit is returned as given.
// promptTokens is the request's measured estimate (approxPromptTokens), whose +1 is a floor
// against an empty prompt rather than a token, so it is not charged against the window.
func withDefaultMaxTokens(body []byte, promptTokens, window int) []byte {
	if statedOutputTokens(body) > 0 {
		return body
	}
	if promptTokens > 0 {
		promptTokens--
	}
	v, _ := json.Marshal(expectedOutput(0, promptTokens, window))
	return rewriteBody(body, false, map[string]json.RawMessage{"max_tokens": v})
}
