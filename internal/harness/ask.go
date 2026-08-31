package harness

// ask.go - ask_operator: the agent puts a QUESTION to the person watching.
//
// The only channel to the operator before this was the mutating-tool y/N gate, which can
// express exactly one thing: may I run this. An agent that reaches a genuine fork - two
// reasonable designs, an ambiguous instruction, a destructive step worth naming out loud -
// had no way to ask. It guessed, or it stopped and handed the turn back.
//
// It is deliberately NOT the confirm gate wearing a hat, and the difference is the whole
// design. A confirm is a PERMISSION, so a permissive session (`/perms all`, `--yolo`)
// auto-approves it, and that is right: the operator said run without asking. A QUESTION is
// not a permission, and auto-answering one would be answering on the operator's behalf. So
// this tool is Mutating:false and never passes through the approval gate at all - which
// means no permission mode can resolve it, by construction rather than by a check someone
// has to remember.

import (
	"context"
	"fmt"
	"strings"
)

// Asker puts a question to the operator and blocks until it is answered. A front end
// without a person attached (headless, a subagent) leaves it nil, and the tool then fails
// honestly rather than inventing an answer.
type Asker func(ctx context.Context, question string, options []string) (string, error)

// strList coerces a JSON array argument to []string, ignoring anything that is not a
// string. A model that sends a single string instead of an array gets that one option
// rather than an error, because the shape of the argument is not what the question is about.
func strList(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(t) != "" {
			return []string{t}
		}
	}
	return nil
}

func (l *Loop) askTool() Tool {
	return Tool{
		Name: "ask_operator",
		Description: "Ask the person watching a question and wait for their answer. Use it at " +
			"a real fork - an ambiguous instruction, two reasonable designs, a destructive step " +
			"worth naming - instead of guessing. Optionally offer options to choose from. It is " +
			"NOT a permission prompt: the operator answers in their own words, and no approval " +
			"mode answers it for them. Do not use it for things you can find out yourself by " +
			"reading.",
		Mutating: false,
		// NOT Concurrent: a person answers one question at a time, and two prompts racing
		// onto one screen is not a thing to design for.
		Concurrent: false,
		// No Timeout. The operator takes as long as they take; the turn's own cancellation
		// is what ends a wait nobody is going to answer.
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "The question, in plain words."},
				"options": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Optional choices to offer. The operator may still answer freely.",
				},
			},
			"required": []any{"question"},
		},
		Run: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			q := strings.TrimSpace(str(args["question"]))
			if q == "" {
				return "", fmt.Errorf("question is empty: say what you want to know")
			}
			if l.ask == nil {
				return "", fmt.Errorf("nobody is watching this session, so there is no one to ask. " +
					"Decide with what you have, or say what you would have asked and stop")
			}
			return l.ask(ctx, q, strList(args["options"]))
		},
	}
}

// rootOnlyTools are the tools registered on the ROOT loop rather than in BuiltinTools(),
// and stripped from every subagent. Named here so a test can say "the builtins plus the
// root's own" instead of carrying a number that quietly goes stale the next time one is
// added - which is exactly how the toolset-width guard broke when ask_operator arrived.
var rootOnlyTools = []string{"delegate", "ask_operator"}

// isRootOnly reports whether a tool is in that set. newSubagent filters with THIS rather
// than naming the tools again, so a future root-only tool cannot leak into subagents by
// being added to the list but not to the filter.
func isRootOnly(name string) bool {
	for _, n := range rootOnlyTools {
		if n == name {
			return true
		}
	}
	return false
}

// SetAsker attaches the operator channel. A front end with a person on it calls this; one
// without leaves it unset, and ask_operator then refuses rather than hanging on a question
// nobody will see.
func (l *Loop) SetAsker(a Asker) { l.ask = a }
