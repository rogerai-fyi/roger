package harness

import (
	"strings"
	"testing"
)

// The founder screenshotted the agent answering "how are things" by calling
// web_fetch on https://www.reddit.com/r/gpus/ - a URL nobody had mentioned, on a turn
// that needed no tool at all, which then blew the tiny `foundation` context window and
// ended the turn with "the conversation outgrew foundation's context window".
//
// Root cause: the persona listed the tools and said when to REACH for one, and never
// said when not to, nor where a web_fetch URL is allowed to come from. On a small
// model that reads as "you have a web tool, use it". These rules are the fix, so they
// are pinned - a persona edit that drops them brings the behaviour back.
func TestPersonaKeepsToolDiscipline(t *testing.T) {
	p := DefaultPersona
	for _, want := range []string{
		"DO NOT reach for a tool when the turn does not need one",
		"NEVER invent a URL",
		"context window may be small",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the persona must keep the rule %q - without it the agent fetches\nURLs nobody asked for and spends the window on them", want)
		}
	}
	// The conversational examples are load-bearing: an abstract rule ("do not use a
	// tool needlessly") does not move a small model the way named cases do.
	for _, ex := range []string{`"hi"`, `"how are things"`} {
		if !strings.Contains(p, ex) {
			t.Errorf("the persona should name %s as a no-tool turn", ex)
		}
	}
}
