package tui

import (
	"strings"
	"testing"
)

// TestChannelHelpListsAcceptedVerbs: the in-channel /help listing must match what
// runSession actually accepts, including the aliases that used to be omitted
// (/conf, /tune, /retune, /ep, /leave, /dc).
func TestChannelHelpListsAcceptedVerbs(t *testing.T) {
	m := browseSeed(100)
	mm, _ := m.runSession("/help")
	gm := asModel(mm)
	help := strings.ToLower(strings.Join(gm.transcript, "\n"))

	// The previously-missing aliases/commands must now appear in the listing.
	for _, want := range []string{"/conf", "/tune", "/retune", "/ep", "/leave", "/dc"} {
		if !strings.Contains(help, want) {
			t.Errorf("/help listing omits %q (runSession accepts it):\n%s", want, help)
		}
	}
	// And the core verbs stay listed.
	for _, want := range []string{"/model", "/clear", "/system", "/cost", "/confidential", "/endpoint", "/disconnect", "/quit"} {
		if !strings.Contains(help, want) {
			t.Errorf("/help listing omits core verb %q:\n%s", want, help)
		}
	}
}
