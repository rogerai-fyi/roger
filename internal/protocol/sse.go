package protocol

import "encoding/json"

// CompletionAsSSE turns one whole OpenAI-shaped chat completion into a single SSE chunk event
// ("data: {chat.completion.chunk}\n\n"), so a reply that arrived whole can be handed to a
// caller who asked for a stream (features/tower/standalone_streaming.feature). An error answer
// becomes an error event. It reports false for bytes that are not a JSON object.
func CompletionAsSSE(answer []byte, model string) ([]byte, bool) {
	var c struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Index        int             `json:"index"`
			Message      json.RawMessage `json:"message"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(answer, &c) != nil {
		return nil, false
	}
	if len(c.Error) > 0 && string(c.Error) != "null" {
		b, _ := json.Marshal(map[string]any{"error": c.Error})
		return []byte("data: " + string(b) + "\n\n"), true
	}
	if c.Model == "" {
		c.Model = model
	}
	choices := make([]any, 0, len(c.Choices))
	for _, ch := range c.Choices {
		delta := json.RawMessage(`{}`)
		if len(ch.Message) > 0 && string(ch.Message) != "null" {
			delta = ch.Message
		}
		reason := "stop"
		if ch.FinishReason != nil {
			reason = *ch.FinishReason
		}
		choices = append(choices, map[string]any{"index": ch.Index, "delta": delta, "finish_reason": reason})
	}
	b, err := json.Marshal(map[string]any{"id": c.ID, "object": "chat.completion.chunk", "model": c.Model, "choices": choices})
	if err != nil {
		return nil, false
	}
	return []byte("data: " + string(b) + "\n\n"), true
}
