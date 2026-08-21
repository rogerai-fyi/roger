package harness

import (
	"strings"
	"testing"
)

// guards_test.go - the deny-only tool-call guard chain.
//
// These exist because a PERSONA rule did not hold. The founder screenshotted the agent
// fetching https://www.reddit.com/r/askreddit/top/all/ to answer "what are some things
// i can do today", and https://rogerai.com/docs/getting-started/ for "question 2" -
// neither URL mentioned by anyone. The first fix was a prompt rule; on a small band it
// was ignored. A guard is not advice.

func conv(user string, retrieved, prior []string) ConversationView {
	return ConversationView{UserText: user, Retrieved: retrieved, PriorCalls: prior}
}

// THE REPORTED BUG. An invented URL is refused, and the refusal says what to do.
func TestFetchGuardRefusesAnInventedURL(t *testing.T) {
	for _, u := range []string{
		"https://www.reddit.com/r/askreddit/top/all/",
		"https://rogerai.com/docs/getting-started/",
	} {
		got := GuardFetchProvenance("web_fetch", map[string]any{"url": u}, conv("what are some things i can do today", nil, nil))
		if got == "" {
			t.Errorf("%s was invented and must be refused", u)
			continue
		}
		if !strings.Contains(got, "web_search") {
			t.Errorf("the refusal must name the way forward, got %q", got)
		}
	}
}

// A URL the operator typed is theirs to fetch. Host-level, so a trailing slash or a
// www. prefix does not turn a real request into a refusal.
func TestFetchGuardAllowsWhatTheUserAskedFor(t *testing.T) {
	cases := []struct{ user, url string }{
		{"read rogerai.fm/models for me", "https://rogerai.fm/models"},
		{"check rogerai.fm/models", "https://www.rogerai.fm/models/"},
		{"what does https://example.com/a say", "https://example.com/a"},
	}
	for _, c := range cases {
		if got := GuardFetchProvenance("web_fetch", map[string]any{"url": c.url}, conv(c.user, nil, nil)); got != "" {
			t.Errorf("user said %q, fetching %s must be allowed: %s", c.user, c.url, got)
		}
	}
}

// THE FLOW THE GUARD IS ASKING FOR. Search, then read a result. If this were refused
// the guard would be telling the model to do something the guard then blocks - worse
// than having no guard at all.
func TestFetchGuardAllowsASearchResult(t *testing.T) {
	found := []string{"https://some-blog.example/post-42"}
	if got := GuardFetchProvenance("web_fetch", map[string]any{"url": found[0]}, conv("what is new in llama.cpp", found, nil)); got != "" {
		t.Errorf("a searched URL must be fetchable: %s", got)
	}
	// ...but only that one. A search does not open the whole web.
	other := "https://unrelated.example/anything"
	if got := GuardFetchProvenance("web_fetch", map[string]any{"url": other}, conv("what is new in llama.cpp", found, nil)); got == "" {
		t.Error("a search result must not ground an unrelated URL")
	}
}

// The guard is scoped to web_fetch and to a url argument it can actually read.
func TestFetchGuardStaysOutOfTheWay(t *testing.T) {
	if got := GuardFetchProvenance("read_file", map[string]any{"path": "notes.md"}, conv("", nil, nil)); got != "" {
		t.Errorf("the fetch guard must ignore other tools: %s", got)
	}
	if got := GuardFetchProvenance("web_fetch", map[string]any{}, conv("", nil, nil)); got != "" {
		t.Errorf("a missing url is the tool's error to report, not the guard's: %s", got)
	}
}

// A byte-identical repeat cannot produce a different answer, and on a small band it
// spends a window the band may not have.
func TestRepeatGuardRefusesTheSameCallTwice(t *testing.T) {
	args := map[string]any{"path": "notes.md"}
	sig := callSignature("read_file", args)
	if got := GuardRepeatCall("read_file", args, conv("", nil, nil)); got != "" {
		t.Errorf("the first call must run: %s", got)
	}
	if got := GuardRepeatCall("read_file", args, conv("", nil, []string{sig})); got == "" {
		t.Error("an identical repeat must be refused")
	}
	// A DIFFERENT call is not a repeat.
	if got := GuardRepeatCall("read_file", map[string]any{"path": "other.md"}, conv("", nil, []string{sig})); got != "" {
		t.Errorf("a different argument is a different call: %s", got)
	}
}

// The signature must not depend on Go's map iteration order, or the repeat guard would
// fire at random - the worst possible failure mode for a guard.
func TestCallSignatureIsStableAcrossKeyOrder(t *testing.T) {
	a := map[string]any{"url": "https://x.example", "depth": 2, "raw": true}
	first := callSignature("web_fetch", a)
	for i := 0; i < 50; i++ {
		if got := callSignature("web_fetch", map[string]any{"raw": true, "depth": 2, "url": "https://x.example"}); got != first {
			t.Fatalf("signature is order-dependent: %q vs %q", got, first)
		}
	}
}

// MONOTONICITY - the property the whole shape exists for. A Guard returns a denial or
// nothing; there is no allow value, so no ordering of guards can turn a denial back
// into permission, and adding a guard can only ever narrow what the agent may do.
func TestGuardsCanOnlyDeny(t *testing.T) {
	// A permissive guard cannot un-deny a strict one, whichever order they run in.
	permissive := func(string, map[string]any, ConversationView) string { return "" }
	strict := func(string, map[string]any, ConversationView) string { return "no" }
	for _, chain := range [][]Guard{{permissive, strict}, {strict, permissive}} {
		denied := false
		for _, g := range chain {
			if g("web_fetch", nil, ConversationView{}) != "" {
				denied = true
				break
			}
		}
		if !denied {
			t.Error("a denial must survive any ordering - that is the whole design")
		}
	}
}

// The defaults are on unless a caller explicitly opts out. Nil means "the defaults";
// an empty non-nil slice means "none", which is what the suites that test below this
// layer use.
// AMENDED 2026-08-21: the loop prepends its own stateful write guard, which is ALWAYS
// on - even for a caller that empties the chain. The others shape behaviour; that one
// prevents losing someone's file, and a test or surface asking for the raw tools was
// never asking to be allowed to clobber an unread file.
func TestLoopGuardsDefaultOn(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	if got, want := len(l.guards()), len(DefaultGuards())+1; got != want {
		t.Errorf("a fresh Loop carries the defaults plus the write guard: got %d, want %d", got, want)
	}
	l.Guards = []Guard{}
	if len(l.guards()) != 1 {
		t.Error("emptying the chain must still leave the write guard - it protects files, not behaviour")
	}
}
