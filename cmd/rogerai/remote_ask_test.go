package main

// remote_ask_test.go - the CLI viewer's side of ask_operator.
//
// A question is not a confirm. A confirm's answer is a bare y/n, so the input loop can
// pattern-match it and leave everything else as a turn; an ANSWER can be any words at all,
// which is why a pending question has to take the whole line. Sending it as a turn instead
// would queue the answer behind the very turn that is blocked waiting for it, and the host
// would hold the question forever.

import (
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/protocol"
)

func TestAskGateTakesTheLineOnlyWhileAQuestionIsPending(t *testing.T) {
	g := &askGate{}

	// Nothing pending: the line is not an answer, so it stays an ordinary turn.
	if pending, _, _ := g.take("some words"); pending {
		t.Error("with no question pending, a typed line must remain a turn")
	}

	g.set("ask_1", nil)
	pending, ans, id := g.take("the answer, in my own words")
	if !pending || ans != "the answer, in my own words" || id != "ask_1" {
		t.Errorf("a free-text answer must pass through verbatim, got pending=%v ans=%q id=%q", pending, ans, id)
	}
	// One question, one answer: a second line is a turn again.
	if pending, _, _ := g.take("and this"); pending {
		t.Error("the gate must close after answering, or the next line is eaten too")
	}
}

func TestAskGateSendsTheOptionNotTheDigit(t *testing.T) {
	g := &askGate{}
	g.set("ask_2", []string{"alpha", "beta", "gamma"})

	// The agent asked in words and has to be answered in them: "2" would arrive where
	// "beta" was meant, leaving the model to guess which list it indexes.
	pending, ans, id := g.take("2")
	if !pending || ans != "beta" || id != "ask_2" {
		t.Errorf("a digit must resolve to its option, got pending=%v ans=%q id=%q", pending, ans, id)
	}
}

func TestAskGatePassesADigitThroughWhenItIsNotAnOption(t *testing.T) {
	g := &askGate{}
	g.set("ask_3", []string{"only one"})

	// "2" is out of range here, so it is an ANSWER that happens to be a digit, not a pick.
	// Silently dropping it, or picking something else, would answer for the operator.
	pending, ans, _ := g.take("2")
	if !pending || ans != "2" {
		t.Errorf("an out-of-range digit is a literal answer, got pending=%v ans=%q", pending, ans)
	}

	g.set("ask_4", nil)
	if pending, ans, _ := g.take("7"); !pending || ans != "7" {
		t.Errorf("with no options offered a digit is just an answer, got pending=%v ans=%q", pending, ans)
	}
}

func TestAskGateClearsWhenTheHostClosesTheQuestion(t *testing.T) {
	g := &askGate{}
	g.set("ask_5", []string{"a"})
	g.clear() // an ask_done frame, e.g. because another surface answered first
	if pending, _, _ := g.take("a"); pending {
		t.Error("after the host closed the question, a typed line must go back to being a turn")
	}
}

func TestRenderAskFrameShowsTheQuestionAndArmsTheGate(t *testing.T) {
	asks := &askGate{}
	out := renderAskFrame(protocol.RCFrame{
		Kind:    protocol.RCKindAskReq,
		Text:    "which branch should this land on?",
		Options: []string{"main", "a release branch"},
		AskID:   "ask_9",
	}, asks)

	for _, want := range []string{"which branch should this land on?", "1 · main", "2 · a release branch", "type an answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered question is missing %q:\n%s", want, out)
		}
	}
	// Rendering ARMS the gate: without this the next line typed goes out as a new turn,
	// queued behind the very turn that is waiting for the answer.
	pending, ans, id := asks.take("2")
	if !pending || ans != "a release branch" || id != "ask_9" {
		t.Errorf("the frame must arm the gate with its options and id, got %v %q %q", pending, ans, id)
	}
}

func TestRenderAskFrameClosesTheQuestion(t *testing.T) {
	asks := &askGate{}
	asks.set("ask_9", []string{"a"})

	out := renderAskFrame(protocol.RCFrame{Kind: protocol.RCKindAskDone, Answer: "main", Origin: "phone"}, asks)
	if !strings.Contains(out, "main") || !strings.Contains(out, "phone") {
		t.Errorf("a closed question should say what was answered and by whom:\n%s", out)
	}
	if pending, _, _ := asks.take("a"); pending {
		t.Error("ask_done must disarm the gate, or a later line is eaten as an answer")
	}
}

func TestRenderAskFrameNamesAnUnansweredQuestion(t *testing.T) {
	asks := &askGate{}
	// Answered by nobody - the operator pressed esc, or the turn ended underneath it. An
	// empty line here would read as "something happened and I cannot tell you what".
	out := renderAskFrame(protocol.RCFrame{Kind: protocol.RCKindAskDone}, asks)
	if !strings.Contains(out, "(not answered)") {
		t.Errorf("an unanswered question must say so:\n%s", out)
	}
	if !strings.Contains(out, "the host") {
		t.Errorf("with no origin the answer is attributed to the host:\n%s", out)
	}
}
