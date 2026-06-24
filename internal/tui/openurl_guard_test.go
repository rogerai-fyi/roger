package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// withFakeTTY swaps the TTY probes + the browser launcher with test fakes, runs
// fn, and restores everything. It returns nothing; the caller reads its own
// counter closed over by the launcher it installs.
func withFakeTTY(t *testing.T, stdin, stdout bool, launch func(string)) func() {
	t.Helper()
	origIn, origOut, origExec := stdinIsTTY, stdoutIsTTY, openURLExec
	stdinIsTTY = func() bool { return stdin }
	stdoutIsTTY = func() bool { return stdout }
	openURLExec = launch
	return func() {
		stdinIsTTY, stdoutIsTTY, openURLExec = origIn, origOut, origExec
	}
}

// TestOpenURLNotInteractiveNeverOpens: in any non-TTY combination (piped stdout,
// no stdin, fully headless) OpenURL/openURL must NOT spawn the launcher. This is
// the founder bug: a background / headless rogerai was hijacking the browser.
func TestOpenURLNotInteractiveNeverOpens(t *testing.T) {
	cases := []struct {
		name           string
		stdin, stdout  bool
		wantOpenCalled bool
	}{
		{"both-tty", true, true, true},
		{"stdout-not-tty (piped)", true, false, false},
		{"stdin-not-tty (no controlling terminal)", false, true, false},
		{"fully-headless (service/daemon)", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opened := 0
			restore := withFakeTTY(t, c.stdin, c.stdout, func(string) { opened++ })
			defer restore()

			OpenURL("https://github.com/login/device/select_account")
			openURL("https://github.com/login/device/select_account")

			if c.wantOpenCalled && opened == 0 {
				t.Fatalf("%s: expected browser open, got none", c.name)
			}
			if !c.wantOpenCalled && opened != 0 {
				t.Fatalf("%s: browser opened %d time(s) in a non-interactive context - must never happen", c.name, opened)
			}
		})
	}
}

// TestLoginStartedOpensOncePerFlow: a single device-flow start opens the browser
// exactly once (interactive), and the poll Cmd it returns NEVER opens anything -
// even across many poll/restart iterations.
func TestLoginStartedOpensOncePerFlow(t *testing.T) {
	opened := 0
	var openedURLs []string
	restore := withFakeTTY(t, true, true, func(u string) {
		opened++
		openedURLs = append(openedURLs, u)
	})
	defer restore()

	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	const uri = "https://github.com/login/device/select_account"
	dev := LoginDevice{VerificationURI: uri, UserCode: "FD9D-8F33"}

	// One flow start.
	_, cmd := m.Update(loginStartedMsg(dev))
	if opened != 1 {
		t.Fatalf("flow start should open the browser exactly once, opened %d times", opened)
	}
	if len(openedURLs) != 1 || openedURLs[0] != uri {
		t.Fatalf("opened the wrong URL: %v", openedURLs)
	}

	// The poll Cmd must not open anything. Drain it (it lands a login/flow msg).
	if cmd != nil {
		_ = cmd() // executes the poll closure off the event loop
	}
	if opened != 1 {
		t.Fatalf("the poll loop opened the browser - it must never open (opened=%d)", opened)
	}
}

// TestLoginPanelEntryNeverOpens: opening the [L] login panel (doLogin) must not
// open anything - the browser only opens once the flow actually STARTS (an
// explicit enter -> loginStartedMsg), never on panel entry.
func TestLoginPanelEntryNeverOpens(t *testing.T) {
	opened := 0
	restore := withFakeTTY(t, true, true, func(string) { opened++ })
	defer restore()

	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Enter the login panel via the [L] key.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	if opened != 0 {
		t.Fatalf("entering the login panel opened the browser %d time(s) - it must wait for the flow to start", opened)
	}
}
