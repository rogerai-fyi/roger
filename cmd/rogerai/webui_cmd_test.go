package main

import (
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/tui"
)

// `roger webui` - the console on its own. The command BLOCKS on a signal by design (it is
// a server), so what is testable is everything up to the serve: the flag parsing, the
// refusals, and the help. Those are also where a user actually gets stuck.

func TestWebuiHelpNamesWhatTheConsoleDoes(t *testing.T) {
	out := captureStdout(t, func() {
		if err := cmdWebui(config{}, []string{"--help"}); err != nil {
			t.Fatalf("webui --help: %v", err)
		}
	})
	for _, want := range []string{"--no-open", "--port=", "127.0.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help must mention %q:\n%s", want, out)
		}
	}
	// It must say the console binds LOCALHOST behind a token - that is the security
	// property someone reads this help to check.
	if !strings.Contains(out, "token") {
		t.Errorf("the help must say the console is token-gated:\n%s", out)
	}
}

// An unknown flag is refused by NAME rather than silently ignored: a typo'd --port would
// otherwise start a console on a port the operator is not watching.
func TestWebuiRefusesAnUnknownFlag(t *testing.T) {
	err := cmdWebui(config{}, []string{"--prot=8391"})
	if err == nil {
		t.Fatal("an unknown flag was accepted")
	}
	if !strings.Contains(err.Error(), "--prot=8391") {
		t.Errorf("the refusal must name the flag it did not understand, got %q", err)
	}
}

// help is reachable by every spelling a user is likely to try.
func TestWebuiHelpSpellings(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		out := captureStdout(t, func() {
			if err := cmdWebui(config{}, []string{arg}); err != nil {
				t.Fatalf("webui %s: %v", arg, err)
			}
		})
		if !strings.Contains(out, "roger webui") {
			t.Errorf("%q did not print the help:\n%s", arg, out)
		}
	}
}

// withWebuiSeams swaps the two seams for the duration of a test and restores them, so a
// case can drive cmdWebui to completion without binding a port or blocking on a signal.
func withWebuiSeams(t *testing.T, url string, stopped *bool) {
	t.Helper()
	oldFor, oldWait := webConsoleFor, waitForStop
	webConsoleFor = func(config, *node.Controller, string, *tui.LimitStore) string { return url }
	waitForStop = func() {
		if stopped != nil {
			*stopped = true
		}
	}
	t.Cleanup(func() { webConsoleFor, waitForStop = oldFor, oldWait })
}

// The happy path: it serves, reports, and waits to be stopped rather than returning
// immediately - a console that exits the moment it starts takes every shared model off air.
func TestWebuiServesAndWaits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stopped := false
	withWebuiSeams(t, "http://127.0.0.1:8391/?t=abc", &stopped)

	out := captureStdout(t, func() {
		if err := cmdWebui(config{}, []string{"--no-open"}); err != nil {
			t.Fatalf("webui: %v", err)
		}
	})
	if !stopped {
		t.Error("the command returned without ever waiting - the console would die instantly")
	}
	if !strings.Contains(out, "serving") || !strings.Contains(out, "stopped") {
		t.Errorf("the command must say it started and that it stopped:\n%s", out)
	}
}

// A BIND FAILURE is an error, not a silent success. Reporting nothing would leave the
// operator waiting on a console that was never listening.
func TestWebuiReportsABindFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withWebuiSeams(t, "", nil)

	err := cmdWebui(config{}, []string{"--no-open", "--port=1"})
	if err == nil {
		t.Fatal("a failed bind reported success")
	}
	if !strings.Contains(err.Error(), "could not bind") {
		t.Errorf("the error must say the bind failed, got %q", err)
	}
}

// --port is threaded through to the listener rather than swallowed.
func TestWebuiPassesThePortThrough(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var gotPort string
	oldFor, oldWait := webConsoleFor, waitForStop
	webConsoleFor = func(_ config, _ *node.Controller, port string, _ *tui.LimitStore) string {
		gotPort = port
		return "http://127.0.0.1:8391/?t=abc"
	}
	waitForStop = func() {}
	t.Cleanup(func() { webConsoleFor, waitForStop = oldFor, oldWait })

	captureStdout(t, func() { _ = cmdWebui(config{}, []string{"--no-open", "--port=8391"}) })
	if gotPort != "8391" {
		t.Errorf("--port reached the listener as %q, want 8391", gotPort)
	}
}
