package harness

import (
	"fmt"
	"net/url"
	"strings"
)

// guards.go - the tool-call GUARD chain.
//
// WHY THIS EXISTS. The founder screenshotted the agent answering "what are some
// things i can do today" by fetching https://www.reddit.com/r/askreddit/top/all/, and
// "question 2" by fetching https://rogerai.com/docs/getting-started/ - neither URL had
// ever been mentioned by anyone. The first fix was a persona rule ("NEVER invent a
// URL"). It did not hold: on a small band like Apple's `foundation`, a prompt rule is
// advice the model may ignore, and it ignored it.
//
// A guard is not advice. It runs between the confirm gate and the tool body, sees the
// actual arguments, and its denial becomes the tool result the model reads - so the
// model learns why instead of silently succeeding at the wrong thing.
//
// MONOTONIC BY DESIGN, borrowed from the DeepSeek Harness's ToolGuard: a guard can
// only return a DENIAL REASON or nothing. There is deliberately no "allow" return, so
// no ordering of guards can turn a denial back into permission, and adding a guard can
// never widen what the agent may do. That property is the whole reason to prefer this
// shape over a general pre-execute hook.

// Guard inspects one accepted tool call. Returning a non-empty string DENIES it, and
// that string is fed back to the model as the call's result. Returning "" leaves the
// call alone - a guard cannot approve, only refuse.
type Guard func(name string, args map[string]any, conv ConversationView) string

// ConversationView is the read-only slice of session state a guard may consult. It is
// deliberately narrow: a guard that could read everything would be a guard nobody can
// reason about.
type ConversationView struct {
	// UserText is every user-authored message this session, joined. A URL a guard finds
	// here was typed or pasted by the operator.
	UserText string
	// Retrieved holds URLs a previous search or fetch legitimately surfaced this turn.
	Retrieved []string
	// PriorCalls are this turn's earlier calls as "name(canonical args)", oldest first.
	PriorCalls []string
}

// DefaultGuards are the stateless guards every Loop runs unless a caller replaces them.
// The loop adds its own stateful ones on top (see Loop.guards) - they need to consult
// what this agent has observed, which a package-level function cannot.
func DefaultGuards() []Guard { return []Guard{GuardFetchProvenance, GuardRepeatCall} }

// GuardFetchProvenance refuses a web_fetch of a URL nobody put in front of the model.
//
// A URL is allowed when its HOST appears in what the user wrote, or when the exact URL
// came back from a search or fetch this turn. Host-level rather than exact-match on the
// user side on purpose: an operator who says "check rogerai.fm/models" and a model that
// fetches the same page with a trailing slash are the same intent, and a guard that
// refuses that reads as broken. An invented host - reddit.com when nobody said reddit -
// has nowhere to come from and is refused.
func GuardFetchProvenance(name string, args map[string]any, conv ConversationView) string {
	if name != "web_fetch" {
		return ""
	}
	raw := strings.TrimSpace(argString(args, "url"))
	if raw == "" {
		return ""
	}
	if isURLGrounded(raw, conv) {
		return ""
	}
	return fmt.Sprintf(
		"refused: %s was not given to you. web_fetch may only follow a URL the user "+
			"wrote or one a search returned. Use web_search to find a real page, or "+
			"answer without fetching.", raw)
}

// isURLGrounded reports whether raw traces back to the user or to a real retrieval.
func isURLGrounded(raw string, conv ConversationView) bool {
	for _, got := range conv.Retrieved {
		if strings.EqualFold(strings.TrimRight(got, "/"), strings.TrimRight(raw, "/")) {
			return true
		}
	}
	host := urlHost(raw)
	if host == "" {
		return false
	}
	low := strings.ToLower(conv.UserText)
	// The bare host, and the host without a leading "www.", so "rogerai.fm" in the
	// user's sentence grounds "https://www.rogerai.fm/models".
	for _, h := range []string{host, strings.TrimPrefix(host, "www.")} {
		if h != "" && strings.Contains(low, h) {
			return true
		}
	}
	return false
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// GuardRepeatCall refuses a call byte-identical to one already made this turn.
//
// A small model that gets an empty or unhelpful result often re-issues the exact same
// call, which cannot produce a different answer and spends context the band may not
// have (the same window the founder watched `foundation` run out of). The denial says
// what to do instead, so this ends the loop rather than just blocking it.
func GuardRepeatCall(name string, args map[string]any, conv ConversationView) string {
	sig := callSignature(name, args)
	for _, prior := range conv.PriorCalls {
		if prior == sig {
			return "refused: this exact call already ran this turn and returned what it " +
				"returned. Repeating it cannot give a different answer - use the result " +
				"you have, try a different call, or answer without it."
		}
	}
	return ""
}

// callSignature canonicalizes a call for comparison: the tool name plus its arguments
// with keys sorted, so map iteration order can never make two identical calls look
// different (a non-determinism that would make the repeat guard fire at random).
func callSignature(name string, args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('(')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%v", k, args[k])
	}
	b.WriteByte(')')
	return b.String()
}

// sortStrings is an insertion sort - the arg maps here are a handful of keys, and this
// keeps guards.go free of a sort import for one call site.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}
