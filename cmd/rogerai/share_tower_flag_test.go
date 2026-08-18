package main

// share_tower_flag_test.go: `roger share --tower` must explain itself.
//
// The flag was removed because reaching the relay fabric is not a mode - see relayfabric.go -
// but removal does not reach the scripts, units, saved shell history and published articles
// that still pass it. Left to flag.ExitOnError, all of that gets "flag provided but not
// defined: -tower" and exit 2, which reads as a broken install rather than an out-of-date
// command.

import (
	"strings"
	"testing"
)

func TestShareExplainsTheRemovedTowerFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--tower"},
		{"-tower"},
		{"--tower=true"},
		{"gpt-oss-120b", "--tower"},        // after the positional model
		{"--tower", "--price-out", "0.30"}, // among other flags
	} {
		err := cmdShare(config{}, args)
		if err == nil {
			t.Fatalf("roger share %v was accepted; the flag no longer does anything and must not look like it does", args)
		}
		msg := err.Error()
		for _, want := range []string{"no longer", "roger share", "roger-tower"} {
			if !strings.Contains(msg, want) {
				t.Errorf("roger share %v: refusal %q does not mention %q", args, msg, want)
			}
		}
		if strings.Contains(msg, "not defined") {
			t.Errorf("roger share %v fell through to the flag package: %q", args, msg)
		}
	}
}

// It matches the FLAG, not the word. A model called "tower", a value that happens to be
// "tower", and the end-of-flags marker all have to get through untouched - a share refused
// for the wrong reason is worse than one that never checked.
func TestTheTowerFlagCheckDoesNotOverreach(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"tower"},             // a model named tower
		{"--model", "tower"},  // ...named by flag
		{"--node", "tower-1"}, // a station callsign
		{"--upstream", "http://tower/v1"},
		{"--", "--tower"}, // past the end-of-flags marker
	} {
		if err := refusedTowerFlag(args); err != nil {
			t.Errorf("roger share %v was refused as if it passed --tower: %v", args, err)
		}
	}
}
