package main

import (
	"errors"
	"strings"
	"testing"
)

// Two commands carry the word "tower" and they do opposite jobs: `roger-tower serve` RUNS
// a relay, `roger share --tower` makes your node serve THROUGH somebody else's. Guessing
// `roger tower` is the natural mistake, and "unknown command" is a useless reply to it.
func TestRogerTowerPointsAtBothRealCommands(t *testing.T) {
	cfg := config{}
	for _, verb := range []string{"tower", "roger-tower"} {
		err := dispatch(cfg, []string{verb})
		if err == nil {
			t.Fatalf("dispatch(%q) succeeded; it is not a real command", verb)
		}
		// Still an unknown command - the hint must not make it look like it ran.
		if !errors.Is(err, errUnknownCommand) {
			t.Errorf("dispatch(%q) = %v, want wrapped errUnknownCommand", verb, err)
		}
		msg := err.Error()
		for _, want := range []string{
			"ROGERAI_COMPONENT=tower", // how to get the binary
			"roger-tower init",        // how to start a relay
			"roger share --tower",     // how to join one
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("dispatch(%q) error omits %q:\n%s", verb, want, msg)
			}
		}
		// The distinction is the whole point of the message, so it must be stated.
		if !strings.Contains(msg, "THROUGH") {
			t.Errorf("dispatch(%q) does not distinguish running from joining:\n%s", verb, msg)
		}
	}
}
