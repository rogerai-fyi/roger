package harness

import (
	"strings"
)

// echo.go - WHEN A MODEL READS ITS OWN PROMPT BACK TO YOU.
//
// FOUNDER SCREENSHOT 2026-08-21, on Apple's on-device `foundation` relayed through a
// station. The answer came back as:
//
//     · Never invent file contents, command output, or URLs...   <- our system prompt
//     · Keep the user in control...                              <- still our prompt
//
//     User:
//     what are we doing?                                         <- their own question
//     Assistant:
//     Roger, we're tuning into the open channel.                 <- the actual answer
//
// A model that does not really implement the chat format - or a shim that flattens
// messages into one prompt string - continues the transcript instead of answering it.
// The genuine reply is in there, wearing the whole prompt as a hat.
//
// THIS IS NOT COSMETIC, which is why it is worth code rather than a shrug. The message
// is appended to the conversation before anything reads it, so next turn we re-send the
// prompt AND its echo, and the model echoes THAT. The conversation roughly doubles per
// turn: three turns in, a small band is out of context. The founder's "the conversation
// outgrew foundation's context window" almost certainly started here.
//
// SO IT IS STRIPPED BEFORE THE MESSAGE ENTERS HISTORY - which fixes the display and the
// compounding at once.
//
// CONSERVATIVE BY CONSTRUCTION. Mangling a good answer is far worse than showing a
// scruffy one, so a strip needs TWO independent signals: a long verbatim run from the
// prompt we sent, AND transcript scaffolding. Either alone is left completely alone - a
// model may quote its instructions when asked about them, and prose may legitimately
// contain the word "Assistant:".

// A line has to be at least this long to count as recited. Short lines ("roger that.",
// a heading) appear in ordinary prose and prove nothing.
const echoLineMin = 40

// A single recited line this long is conclusive on its own; below it we want two.
const echoLineStrong = 100

// roleMarkers are the scaffolding a flattened transcript leaves behind. Matched only as
// a whole line, so prose that merely mentions one is untouched.
var roleMarkers = []string{"assistant:", "assistant :", "### assistant", "<|assistant|>"}

// stripPromptEcho returns the model's real reply with any recited prompt removed, and
// whether it removed anything.
//
// system is the prompt we sent; reply is what came back.
func stripPromptEcho(reply, system string) (string, bool) {
	if reply == "" || system == "" {
		return reply, false
	}
	if !recitesPrompt(reply, system) {
		return reply, false
	}
	// Signal two: the transcript scaffolding. The real answer is whatever follows the
	// LAST role marker - everything before it is the recital.
	lines := strings.Split(reply, "\n")
	last := -1
	for i, ln := range lines {
		t := strings.ToLower(strings.TrimSpace(ln))
		for _, m := range roleMarkers {
			if t == m {
				last = i
			}
		}
	}
	if last < 0 {
		// Reciting but no scaffolding to cut on. Refusing to guess is the right answer:
		// returning a fragment chosen by heuristic would be a worse failure than showing
		// the recital, because nobody could tell it had happened.
		return reply, false
	}
	tail := strings.TrimSpace(strings.Join(lines[last+1:], "\n"))
	if tail == "" {
		return reply, false // the marker was the last line: nothing to keep
	}
	return tail, true
}

// recitesPrompt reports whether the reply is reading our prompt back.
//
// It counts REPLY LINES that appear verbatim in the prompt, rather than looking for one
// long shared run. The first version looked for a run and missed the real case: the
// model skipped a bullet, so the lines it recited are not contiguous in the prompt and
// no single long run is shared by both. What is conclusive is not length but
// PROVENANCE - whole lines of ours turning up in its answer.
//
// Compared on COLLAPSED WHITESPACE, per line: the shim that causes this re-wraps and
// re-indents, so an exact test would miss the very case it exists for.
func recitesPrompt(reply, system string) bool {
	s := collapseSpace(system)
	if len(s) < echoLineMin {
		return false
	}
	hits := 0
	for _, raw := range strings.Split(reply, "\n") {
		ln := collapseSpace(raw)
		if len(ln) < echoLineMin {
			continue
		}
		if !strings.Contains(s, ln) {
			continue
		}
		if len(ln) >= echoLineStrong {
			return true // one long verbatim line of ours is not a coincidence
		}
		hits++
		if hits >= 2 {
			return true
		}
	}
	return false
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
