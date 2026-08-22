package tui

import (
	"strings"
	"testing"
)

// THE CHANNEL REMEMBERS (chat_history.go).
//
// FOUNDER 2026-08-21: asked a band for a fact, then asked "why is that?", and got "I'm not
// sure what you're referring to." TUNE-IN sent exactly ONE message per turn, so every
// answer arrived with no memory of the question before it - the failure client.ChatTurns
// was built to prevent, and which the browser console avoided from the start.

// chatRing returns a model whose ring holds a CHANNEL exchange plus turns from the OTHER
// surfaces, so a test can prove both that history travels and that nothing else does.
func chatRing(t *testing.T) model {
	t.Helper()
	m := privateTab(t)
	m.connected = &offer{Model: "grok-4.6", NodeID: "eager-puma-54-grok-4-6", Online: true}
	m.recordTurn("user", "can you tell me a fact", "user", nil, nil)
	m.recordTurn("assistant", "honey never spoils", m.channelAgent(), nil, nil)
	// Other surfaces, which must NOT travel into a chat.
	m.recordTurn("user", "AGENT SECRET: refactor the payouts module", agentSurfaceUser, nil, nil)
	m.recordTurn("assistant", "AGENT REPLY: done", agentSurfacePrefix, nil, nil)
	m.recordTurn("assistant", "GUEST REPLY: hello", "guest:opencode", nil, nil)
	return m
}

// THE HEADLINE: the prior exchange travels, in order.
func TestTheChannelSendsItsHistory(t *testing.T) {
	h := chatRing(t).chatHistory("grok-4.6")
	if len(h) != 2 {
		t.Fatalf("want the 2 prior channel turns, got %d: %+v", len(h), h)
	}
	if h[0].Role != "user" || !strings.Contains(h[0].Content, "tell me a fact") {
		t.Errorf("the question is missing or out of order: %+v", h)
	}
	if h[1].Role != "assistant" || !strings.Contains(h[1].Content, "honey") {
		t.Errorf("the answer is missing or out of order: %+v", h)
	}
}

// THE LEAK THAT MUST NOT HAPPEN. The ring is SHARED across surfaces: it holds AGENT turns
// and guest-operator turns too. A history built from the whole ring would feed the agent's
// working session into a chat with a stranger's band.
func TestChannelHistoryCarriesNoOtherSurface(t *testing.T) {
	h := chatRing(t).chatHistory("grok-4.6")
	var all strings.Builder
	for _, t := range h {
		all.WriteString(t.Content)
	}
	for _, leak := range []string{"AGENT SECRET", "AGENT REPLY", "GUEST REPLY"} {
		if strings.Contains(all.String(), leak) {
			t.Errorf("a %s turn leaked into the channel history: %s", leak, all.String())
		}
	}
}

// SIZED TO THE BAND. `foundation` is an 8k window; pouring a long history into it trades a
// memory bug for a context overflow, which is a worse bug - it refuses the turn outright.
func TestChannelHistoryIsBoundedByTheBandsWindow(t *testing.T) {
	m := privateTab(t)
	m.connected = &offer{Model: "foundation", Online: true}
	m.bands = []band{{model: "foundation", online: true, cheapest: &offer{Model: "foundation", Ctx: 8192}}}
	big := strings.Repeat("x", 4000)
	for i := 0; i < 40; i++ {
		m.recordTurn("user", big, "user", nil, nil)
		m.recordTurn("assistant", big, "roger:foundation", nil, nil)
	}
	h := m.chatHistory("foundation")
	total := 0
	for _, t := range h {
		total += len(t.Content)
	}
	// Half an 8k window at the harness's own bytes-per-token estimate.
	if want := 8192 * 4 / 2; total > want {
		t.Errorf("history is %d bytes for an 8k band, over the %d budget", total, want)
	}
	if len(h) == 0 {
		t.Error("the budget squeezed out the whole conversation - the recent turn must survive")
	}
}

// It keeps the RECENT turns. A bound that dropped from the end would remember the opening
// and forget the question just asked, which is the opposite of a conversation.
func TestChannelHistoryKeepsTheRecentTurns(t *testing.T) {
	m := privateTab(t)
	m.connected = &offer{Model: "grok-4.6", Online: true}
	for i := 0; i < 60; i++ {
		m.recordTurn("user", "old question", "user", nil, nil)
		m.recordTurn("assistant", "old answer", "roger:grok-4.6", nil, nil)
	}
	m.recordTurn("user", "THE RECENT ONE", "user", nil, nil)
	h := m.chatHistory("grok-4.6")
	if len(h) == 0 {
		t.Fatal("no history at all")
	}
	if h[len(h)-1].Content != "THE RECENT ONE" {
		t.Errorf("the most recent turn was dropped: %+v", h[len(h)-1])
	}
	if len(h) > chatHistoryMessages {
		t.Errorf("history is %d messages, over the %d cap - every turn re-sends and re-bills it",
			len(h), chatHistoryMessages)
	}
}

// A history must not OPEN on an assistant turn: starting mid-exchange reads as the model
// having spoken first, unprompted.
func TestChannelHistoryOpensOnAUserTurn(t *testing.T) {
	m := privateTab(t)
	m.connected = &offer{Model: "grok-4.6", Online: true}
	m.recordTurn("assistant", "an orphaned reply", "roger:grok-4.6", nil, nil)
	m.recordTurn("user", "a real question", "user", nil, nil)
	h := m.chatHistory("grok-4.6")
	if len(h) > 0 && h[0].Role != "user" {
		t.Errorf("history opens on a %s turn: %+v", h[0].Role, h)
	}
}

// An empty channel sends no history rather than an empty-content turn, which ChatTurns
// would have to reject.
func TestAFreshChannelSendsNoHistory(t *testing.T) {
	m := privateTab(t)
	m.connected = &offer{Model: "grok-4.6", Online: true}
	if h := m.chatHistory("grok-4.6"); len(h) != 0 {
		t.Errorf("a fresh channel invented %d turns: %+v", len(h), h)
	}
}
