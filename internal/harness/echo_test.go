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
