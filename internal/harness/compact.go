package harness

import (
	"fmt"
	"strings"
)

// compact.go - AUTOMATIC COMPACTION on a context-window overflow.
//
// FOUNDER 2026-08-20: "shouldn't we automatically trigger a compaction or something
// like that?" Yes. Until now a turn that outgrew the band's window simply died, and the
// TUI told the operator to run /clear or switch models - correct advice, and a dead end
// they had to act on by hand for a condition the harness could see coming and fix.
//
// WHAT GETS DROPPED, AND WHY IT IS THE RIGHT THING. The conversation is mostly TOOL
// RESULTS: a fetched page, a directory listing, a file. Those are the largest messages
// by far and the least valuable to keep verbatim once they have been read and acted on
// - the model already summarized what mattered into its own reply, and that reply is
// kept. So compaction prunes tool results, OLDEST FIRST, and never touches a user
// message, a system message, or an assistant reply. What the operator said and what the
// agent concluded both survive intact; only the raw material is let go.
//
// MODEL-FREE and deterministic, like the DeepSeek Harness's own pruner: no summarizing
// model call, so compaction cannot itself fail, cost money, or invent something that was
// never in the transcript. A pruned result is REPLACED by an honest marker naming the
// tool and the size that went, so the model can see that it once had that material and
// ask for it again rather than being quietly gaslit about what it read.

// IsContextOverflow spots a station saying the CONVERSATION no longer fits the model's
// window. Apple's on-device foundation model says "Exceeded model context window size";
// llama.cpp / vLLM / OpenAI-compatible servers phrase it as "context length exceeded",
// "maximum context length", "too many tokens", or a full "kv cache".
//
// Lives HERE, beside the thing that acts on it, and is exported because the TUI needs
// the same judgement to choose its remedy line. One spelling list, one answer - two
// copies would drift and the harness would compact on a shape the TUI still explained
// away, or the reverse.
func IsContextOverflow(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "context window") ||
		strings.Contains(low, "context length") ||
		strings.Contains(low, "context_length_exceeded") ||
		strings.Contains(low, "maximum context") ||
		strings.Contains(low, "too many tokens") ||
		strings.Contains(low, "kv cache") ||
		IsRequestTooLarge(low)
}

// IsRequestTooLarge spots THE SAME WALL MEASURED IN BYTES.
//
// A station can refuse an oversized conversation at the HTTP layer instead of the
// tokenizer, and then it never says "context" at all - llama.cpp/Ollama answer 413 with
// "Maximum request body size 1048576 exceeded, actual body size 1050714", and a proxy in
// front says "Request Entity Too Large" or "Payload Too Large". None of those match the
// spellings above, so the turn used to stop dead and ask the operator to "retry the turn
// or fix the error" - advice that cannot work, because retrying sends the same oversized
// body again and there is nothing for a human to fix.
//
// The cause is identical to a token overflow (the conversation no longer fits) and so is
// the remedy: drop the raw tool output and send it again. Compaction frees bytes exactly
// as it frees tokens.
//
// Deliberately narrow. It matches the SIZE-OF-REQUEST shapes only, never a bare "413",
// which could appear in a model's own answer or in a tool result being relayed back.
func IsRequestTooLarge(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "request body size") ||
		strings.Contains(low, "payload too large") ||
		strings.Contains(low, "entity too large") ||
		strings.Contains(low, "body size exceeded")
}

// prunedMarker is what a dropped tool result leaves behind. It names the tool and the
// byte count so the record stays honest: the model is told the material existed and is
// gone, never left to believe the call returned nothing.
func prunedMarker(tool string, n int) string {
	if tool == "" {
		tool = "tool"
	}
	return fmt.Sprintf("[%d bytes of %s output were dropped to fit the context window - "+
		"call it again if you still need them]", n, tool)
}

// prunable reports whether a message is raw material compaction may drop.
func prunable(m Message) bool {
	return m.Role == "tool" && !strings.HasPrefix(m.Content, prunedPrefix)
}

const prunedPrefix = "["

// compactForWindow drops tool results, oldest first, until at least want bytes have
// been freed. It returns how many bytes went and how many messages it touched.
//
// It stops before the CURRENT turn's messages: pruning what the model just fetched, in
// the same turn it fetched it, would strand the turn mid-thought and is very likely to
// send it straight back to re-fetch the same page - trading an overflow for a loop.
// Earlier turns are fair game; their conclusions are already in the assistant replies
// that follow them.
func (l *Loop) compactForWindow(want int) (freed, dropped int) {
	for i := 0; i < l.turnStart && i < len(l.messages); i++ {
		if freed >= want {
			break
		}
		m := l.messages[i]
		if !prunable(m) {
			continue
		}
		n := len(m.Content)
		if n == 0 {
			continue
		}
		l.messages[i].Content = prunedMarker(m.Name, n)
		freed += n - len(l.messages[i].Content)
		dropped++
	}
	return freed, dropped
}

// minCompactionGain is the least a compaction must be able to free to be worth a retry.
//
// FOUNDER SCREENSHOT 2026-08-21: "compacted the session: dropped 0 KB of tool output
// from 1 earlier tool call". Two bugs in one line. compactableBytes counted the SIZE of
// prunable results, but pruning replaces each one with a marker of its own - so a 200
// byte result frees about a hundred bytes, and a handful of small results frees
// effectively nothing. We spent a billed model call to re-send a conversation that had
// barely changed, and then told the operator we had freed 0 KB, which is both useless
// and slightly insulting.
//
// The floor makes the decision honest: unless there is real material to drop, the
// overflow is not coming from tool output and compaction is not the answer - /clear or
// a roomier band is, which is what the error already says.
const minCompactionGain = 4 << 10 // 4 KiB

// compactableBytes is how much compaction could actually free right now: the size of
// every prunable tool result before this turn, MINUS the marker each one leaves behind.
// The caller uses it to decide whether a retry is worth attempting at all - freeing
// nothing and re-sending the same conversation just spends another billed call to fail
// the same way.
func (l *Loop) compactableBytes() int {
	total := 0
	for i := 0; i < l.turnStart && i < len(l.messages); i++ {
		if m := l.messages[i]; prunable(m) {
			// The NET gain, not the gross size: what is dropped is the content, what is
			// added back is the marker naming it.
			if gain := len(m.Content) - len(prunedMarker(m.Name, len(m.Content))); gain > 0 {
				total += gain
			}
		}
	}
	return total
}
