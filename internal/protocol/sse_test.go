package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompletionAsSSE(t *testing.T) {
	ev, ok := CompletionAsSSE([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"length"}]}`), "m")
	require.True(t, ok)
	require.True(t, strings.HasPrefix(string(ev), "data: ") && strings.HasSuffix(string(ev), "\n\n"))
	var chunk struct {
		Object, Model string
		Choices       []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(string(ev), "data: "), "\n\n")), &chunk))
	require.Equal(t, "chat.completion.chunk", chunk.Object)
	require.Equal(t, "m", chunk.Model, "the request's model fills in a missing one")
	require.Equal(t, "hi", chunk.Choices[0].Delta.Content)
	require.Equal(t, "length", chunk.Choices[0].FinishReason)

	ev, ok = CompletionAsSSE([]byte(`{"choices":[{"message":null}]}`), "m")
	require.True(t, ok)
	require.Contains(t, string(ev), `"finish_reason":"stop"`, "a missing finish reason reads as stop")

	ev, ok = CompletionAsSSE([]byte(`{"error":{"message":"boom"}}`), "m")
	require.True(t, ok)
	require.Contains(t, string(ev), `"error":{"message":"boom"}`)

	_, ok = CompletionAsSSE([]byte(`not json`), "m")
	require.False(t, ok)
}
