package tui

import (
	"strings"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/harness"
)

// THE CHANNEL REMEMBERS.
//
// FOUNDER 2026-08-21: asked a band for a fact, then asked "why is that?", and got "I'm not
// sure what you're referring to."
//
// The TUNE-IN channel sent exactly ONE message per turn - []ChatTurn{{Role:"user", ...}} -
// so every answer arrived with no memory of the question before it. That is the failure
// client.ChatTurns was BUILT to prevent; its own doc says so, and the browser console has
// used it with history from the start. The terminal never did.
//
// The conversation was already being recorded: recordTurn writes both sides into m.ring,
// the per-turn context ring the operator-handoff capsule reads. It was there all along and
// simply never sent.
//
// WHAT MUST NOT LEAK. The ring is SHARED across surfaces - it holds AGENT turns and guest
// operator turns too - so a channel history built from the whole ring would feed the
// agent's working session into a chat, and a chat's contents into whatever read it next.
// Only CHANNEL turns are included, keyed on the same x_roger.agent tags the recorder
// writes.

const (
	// chatHistoryMessages bounds how far back a channel looks. It is a count, not just a
	// byte budget, because a long history costs money on every turn: the whole thing is
	// re-sent and re-billed as input tokens, so an unbounded window would quietly make
	// each turn more expensive than the last.
	chatHistoryMessages = 24
	// chatHistoryBytes is the fallback budget when the band's context window is unknown.
	chatHistoryBytes = 6 << 10
)

// channelTurn reports whether a ring message belongs to the CHANNEL surface.
//
// The recorder tags a channel user turn "user" and an assistant turn "roger" /
// "roger:<model>" (channelAgent). Everything else - "user:agent", "roger-agent…",
// "guest:…" - is another surface and must not travel into a chat.
func channelTurn(agent string) bool {
	return agent == "user" || agent == "roger" || strings.HasPrefix(agent, "roger:")
}

// chatHistory builds the conversation to send with the next channel turn: the recent
// CHANNEL messages, oldest first, bounded by count and by a budget sized to the band.
//
// The current prompt is NOT included - the caller appends it, because it may carry the
// system prompt prepended to it and the ring deliberately stores the clean text.
func (m model) chatHistory(model string) []client.ChatTurn {
	budget := chatHistoryBytes
	// SIZE IT TO THE BAND. `foundation` is an 8k window; pouring a long history into it
	// would push the turn straight into a context overflow, trading a memory bug for a
	// refusal. Half the window, at the harness's own bytes-per-token estimate, leaves
	// room for the persona, the question and the answer.
	if ctx := m.ctxForModel(model); ctx > 0 {
		if b := ctx * harness.BytesPerToken / 2; b > 0 {
			budget = b
		}
	}

	// Walk BACKWARDS: when the budget runs out it is the OLDEST turns that go, which is
	// what "remembers the recent conversation" means. Taking from the front would keep
	// the opening and drop the question just asked.
	var picked []client.ChatTurn
	used := 0
	for i := len(m.ring) - 1; i >= 0; i-- {
		msg := m.ring[i]
		if !channelTurn(msg.XRoger.Agent) {
			continue
		}
		if msg.Role != "user" && msg.Role != "assistant" {
			continue // ChatTurns rejects an unknown role rather than forwarding it
		}
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if len(picked) >= chatHistoryMessages || used+len(msg.Content) > budget {
			break
		}
		used += len(msg.Content)
		picked = append(picked, client.ChatTurn{Role: msg.Role, Content: msg.Content})
	}
	// Reverse into chronological order - the model reads a conversation forwards.
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	// A history that would open on an ASSISTANT turn is dropped to the next user turn:
	// starting mid-exchange reads as the model having spoken first, unprompted.
	for len(picked) > 0 && picked[0].Role != "user" {
		picked = picked[1:]
	}
	return picked
}

// chatMessages is chatHistory in the harness's shape, for the DIRECT (local) channel.
func (m model) chatMessages(model string) []harness.Message {
	hist := m.chatHistory(model)
	out := make([]harness.Message, 0, len(hist)+1)
	for _, t := range hist {
		out = append(out, harness.Message{Role: t.Role, Content: t.Content})
	}
	return out
}
