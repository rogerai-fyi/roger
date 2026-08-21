package harness

import (
	"strings"
	"testing"
)

// echo_test.go - stripping a recited prompt.
//
// From the founder's screenshot: Apple's on-device `foundation`, relayed through a
// station, answered by continuing the transcript instead of replying to it.

// theRealScreenshot is the shape that came back, persona tail and all.
func theRealScreenshot() (system, reply string) {
	system = DefaultPersona
	reply = `- Never invent file contents, command output, or URLs. Use a tool or say you cannot.
- Keep the user in control. This session has no long-term memory - it is just this
  conversation. roger that.

User:
what are we doing?
Assistant:
Roger, we're tuning into the open channel. What's on the playlist?`
	return system, reply
}

func TestTheRecitedPromptIsStripped(t *testing.T) {
	system, reply := theRealScreenshot()
	got, stripped := stripPromptEcho(reply, system)
	if !stripped {
		t.Fatalf("the founder's actual case must be recognised:\n%s", reply)
	}
	if got != "Roger, we're tuning into the open channel. What's on the playlist?" {
		t.Errorf("only the real answer should survive, got %q", got)
	}
	// The whole point: none of our prompt is left to be re-sent next turn.
	if strings.Contains(got, "Never invent file contents") {
		t.Error("the recital must not enter history - that is what doubles the conversation")
	}
	if strings.Contains(got, "what are we doing?") {
		t.Error("the echoed question must go too")
	}
}

// TWO SIGNALS REQUIRED. Mangling a good answer is far worse than showing a scruffy
// one, so neither signal alone may trigger a strip.

// A model asked about its own instructions may legitimately quote them.
func TestQuotingTheInstructionsAloneIsNotAnEcho(t *testing.T) {
	system := DefaultPersona
	reply := "You asked what my rules are. One of them is: " +
		strings.Split(strings.SplitN(system, "## Stance", 2)[1], "\n")[2] +
		" I follow that."
	if got, stripped := stripPromptEcho(reply, system); stripped {
		t.Errorf("a quote with no transcript scaffolding must be left alone, got %q", got)
	}
}

// Prose may mention the word, and a code block may contain it.
func TestScaffoldingAloneIsNotAnEcho(t *testing.T) {
	system := DefaultPersona
	for _, reply := range []string{
		"The API has two roles.\nassistant:\nis the one you want.",
		"Here is the JSON:\nAssistant:\n{\"role\":\"assistant\"}",
	} {
		if got, stripped := stripPromptEcho(reply, system); stripped {
			t.Errorf("scaffolding without a recital must be left alone: %q -> %q", reply, got)
		}
	}
}

// Reciting with NO scaffolding to cut on: refuse to guess. A fragment chosen by
// heuristic is a worse failure than a visible recital, because nobody can tell it
// happened.
func TestRecitalWithNoMarkerIsLeftIntact(t *testing.T) {
	system := DefaultPersona
	reply := system[:400] + "\n\nand that is what I do."
	got, stripped := stripPromptEcho(reply, system)
	if stripped {
		t.Errorf("with nothing safe to cut on it must not guess, got %q", got)
	}
	if got != reply {
		t.Error("an untouched reply must come back byte-identical")
	}
}

// The shim that causes this re-wraps and re-indents, so detection compares collapsed
// whitespace - an exact-substring test would miss the very case it exists for.
func TestRecitalIsFoundThroughRewrapping(t *testing.T) {
	system := DefaultPersona
	// Take two real persona lines and re-indent them the way a flattening shim does -
	// same words, different leading whitespace. (The first version broke one line to a
	// word per line, which no shim produces and which is under the length a line needs
	// to count as recited at all.)
	var lines []string
	for _, l := range strings.Split(system, "\n") {
		if c := collapseSpace(l); len(c) >= echoLineMin {
			lines = append(lines, c)
		}
		if len(lines) == 2 {
			break
		}
	}
	if len(lines) < 2 {
		t.Fatal("the persona should have two substantial lines to recite")
	}
	reply := "   " + lines[0] + "\n      " + lines[1] + "\nAssistant:\nthe answer"
	got, stripped := stripPromptEcho(reply, system)
	if !stripped {
		t.Fatal("a re-wrapped recital must still be recognised")
	}
	if got != "the answer" {
		t.Errorf("got %q", got)
	}
}

// Ordinary replies are never touched - the common path must be exactly as it was.
func TestOrdinaryRepliesAreUntouched(t *testing.T) {
	system := DefaultPersona
	for _, reply := range []string{
		"10",
		"Roger that. The file has 42 lines.",
		"",
		"Here is a list:\n1. one\n2. two",
	} {
		got, stripped := stripPromptEcho(reply, system)
		if stripped || got != reply {
			t.Errorf("%q must pass through untouched, got %q (stripped=%v)", reply, got, stripped)
		}
	}
}

// A marker as the LAST line leaves nothing to keep - returning "" would turn a bad
// answer into no answer.
func TestMarkerWithNothingAfterItKeepsTheReply(t *testing.T) {
	system := DefaultPersona
	reply := system[:400] + "\nAssistant:"
	if got, stripped := stripPromptEcho(reply, system); stripped || got != reply {
		t.Errorf("nothing to keep means keep everything, got %q (stripped=%v)", got, stripped)
	}
}

// ── THE STEP-CAP FALLBACK ────────────────────────────────────────────────────
// Founder screenshot 2026-08-21: two different questions came back with the SAME
// answer, and the second did not fit its question - it was the first turn's reply.
// lastAssistantText scanned the whole session, so a turn that burned its steps on
// tools without ever producing prose walked back past its own beginning.
//
// That is the worst kind of wrong: not an error, not a blank, but a confident answer
// to a question nobody asked, indistinguishable from a real one.

func TestStepCapNeverReturnsAnEarlierTurnsAnswer(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.messages = []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "what model are you?"},
		{Role: "assistant", Content: "Roger that! I'm running on the latest model."},
		{Role: "user", Content: "what is rogerai?"},
		// This turn spent every step on tools and never spoke.
		{Role: "assistant", Content: ""},
		{Role: "tool", Name: "web_fetch", Content: "some page"},
	}
	l.turnStart = 3 // where "what is rogerai?" began
	if got := l.lastAssistantText(); got != "" {
		t.Errorf("a turn with nothing to say must say nothing, got %q - that is the previous turn's answer", got)
	}
}

func TestStepCapReturnsThisTurnsOwnProse(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.messages = []Message{
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "new"},
		{Role: "assistant", Content: "partial thinking about the new one"},
		{Role: "tool", Name: "read_file", Content: "x"},
	}
	l.turnStart = 2
	if got := l.lastAssistantText(); got != "partial thinking about the new one" {
		t.Errorf("this turn's own prose is the right fallback, got %q", got)
	}
}

// ── A GUARD REFUSAL IS NOT A DENIAL ──────────────────────────────────────────
// Founder screenshot: a screen of "denied" tool calls read as a permissions problem,
// and the operator waited for a prompt that was never coming because nothing had asked
// them anything. Those were GUARD refusals - the harness applying a rule - and they
// must not wear the word that means "the operator said no".
func TestGuardRefusalIsNotReportedAsUserDenial(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.Guards = []Guard{func(string, map[string]any, ConversationView) string { return "refused: because" }}

	var c ToolCall
	c.ID = "1"
	c.Function.Name = "web_fetch"
	c.Function.Arguments = `{"url":"https://example.com"}`

	var got Event
	p := l.decide(c, func(e Event) {})
	l.settle(p, "", nil, func(e Event) {
		if e.Kind == EventToolResult {
			got = e
		}
	})
	if !got.IsError {
		t.Error("a refusal is an error - the call did not happen")
	}
	if got.Denied {
		t.Error("Denied means the OPERATOR said no; a guard refusal must not claim they did")
	}
	if !strings.Contains(got.Result, "refused") {
		t.Errorf("the reason must reach the model: %q", got.Result)
	}
}

// A real user denial still reports as one - the distinction only helps if both halves
// are true.
func TestUserDenialStillReportsAsDenied(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, func(string, map[string]any) bool { return false })
	var c ToolCall
	c.ID = "1"
	c.Function.Name = "write_file"
	c.Function.Arguments = `{"path":"a.txt","content":"x"}`

	var got Event
	p := l.decide(c, func(e Event) {})
	l.settle(p, "", nil, func(e Event) {
		if e.Kind == EventToolResult {
			got = e
		}
	})
	if !got.Denied {
		t.Error("a denied confirm must still report as denied")
	}
}
