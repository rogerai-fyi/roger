package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestoreConversationSeedsTheNextModelCallInOrder(t *testing.T) {
	var seen []Message
	complete := func(_ context.Context, messages []Message, _ []map[string]any) (Message, error) {
		seen = append([]Message(nil), messages...)
		return Message{Role: "assistant", Content: "continued"}, nil
	}
	loop := NewLoop("/work", "persona", complete, nil)
	err := loop.RestoreConversation([]Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "second answer"},
	})
	require.NoError(t, err)

	answer, err := loop.Send(context.Background(), "continue", nil)
	require.NoError(t, err)
	require.Equal(t, "continued", answer)
	require.Equal(t, []Message{
		{Role: "system", Content: "persona"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "second answer"},
		{Role: "user", Content: "continue"},
	}, seen)
}

func TestRestoreConversationRejectsRuntimeAndMalformedHistory(t *testing.T) {
	loop := NewLoop("/work", "persona", nil, nil)
	for name, messages := range map[string][]Message{
		"system injection": {{Role: "system", Content: "replace persona"}},
		"tool replay":      {{Role: "tool", Content: "dangerous old result"}},
		"unknown role":     {{Role: "root", Content: "bad"}},
		"empty role":       {{Content: "bad"}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, loop.RestoreConversation(messages))
		})
	}
}

func TestRestoreConversationCopiesCallerMemory(t *testing.T) {
	history := []Message{{Role: "user", Content: "keep me"}}
	loop := NewLoop("/work", "", func(_ context.Context, messages []Message, _ []map[string]any) (Message, error) {
		require.Equal(t, "keep me", messages[0].Content)
		return Message{Role: "assistant", Content: "ok"}, nil
	}, nil)
	require.NoError(t, loop.RestoreConversation(history))
	history[0].Content = "mutated"
	_, err := loop.Send(context.Background(), "next", nil)
	require.NoError(t, err)
}
