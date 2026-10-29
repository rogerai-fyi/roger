package edge

import "time"

// JoinPrompt is the backend of the empty Edge's j prompt (features/edge/empty_edge_simple.feature
// §4): one line of text that is either an invite or an authority's address, handled with the
// same rules as `roger join`. The host fills it; the TUI and the console only show what it says.
type JoinPrompt struct {
	// Start takes what was pasted or typed. A roger:// string is an invite and joins at once
	// (nil ask, nil error = this machine is a member now). Anything else is an address to ask:
	// the returned ask carries the matching phrase and waits for the owner there. An error is
	// the one line to show (the JoinSays words in internal/look).
	Start func(text string) (*JoinAsk, error)
}

// JoinAsk is an ask by address in flight: the phrase both screens must show, when it gives up,
// Wait (blocks until approved, refused or timed out; nil = member) and Cancel.
type JoinAsk struct {
	Phrase string
	Until  time.Time
	Wait   func() error
	Cancel func()
}
