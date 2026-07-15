package tui

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestInTUIUpgradeFlow drives the whole banner lifecycle with a stubbed installer:
// offer -> u installs -> restart offer -> u quits with the restart flag; and the
// failure branch keeps the CLI fallback visible. x hides the banner.
func TestInTUIUpgradeFlow(t *testing.T) {
	orig := runUpgrade
	defer func() { runUpgrade = orig; wantRestart = false }()

	m := browseSeed(120)
	m.updateLine = "update available v5.3.7 -> v5.3.8 · run 'roger upgrade'"

	// The idle banner offers both keys.
	if b := stripANSI(m.upgradeBanner()); !strings.Contains(b, "u") || !strings.Contains(b, "upgrade now") || !strings.Contains(b, "hide") {
		t.Fatalf("idle banner = %q", b)
	}

	// u starts the install (state flips to running; the returned cmd is the worker).
	calls := 0
	runUpgrade = func(cur string, w io.Writer) error { calls++; fmt.Fprintln(w, "ok"); return nil }
	nm, cmd, handled := m.onUpgradeKey("u")
	if !handled || nm.upg != upgRunning || cmd == nil {
		t.Fatalf("u should start the upgrade (handled=%v state=%v cmd=%v)", handled, nm.upg, cmd)
	}
	if !strings.Contains(stripANSI(nm.upgradeBanner()), "upgrading") {
		t.Errorf("running banner = %q", stripANSI(nm.upgradeBanner()))
	}
	msg := cmd()
	if calls != 1 {
		t.Fatalf("the worker should call the installer once, got %d", calls)
	}
	done, ok := msg.(upgradeDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("worker msg = %#v", msg)
	}

	// Success: the restart offer; u quits with the restart flag set.
	nm.upg = upgDone
	if b := stripANSI(nm.upgradeBanner()); !strings.Contains(b, "restarts now") {
		t.Errorf("done banner = %q", b)
	}
	nm2, cmd2, handled := nm.onUpgradeKey("u")
	if !handled || cmd2 == nil || !wantRestart {
		t.Fatalf("u after done should quit with wantRestart (handled=%v cmd=%v want=%v)", handled, cmd2, wantRestart)
	}
	_ = nm2

	// Failure branch names the fallback.
	wantRestart = false
	runUpgrade = func(cur string, w io.Writer) error { return errors.New("boom") }
	m3 := m
	m3.upg = upgIdle
	m3n, cmd3, _ := m3.onUpgradeKey("u")
	if done := cmd3().(upgradeDoneMsg); done.err == nil {
		t.Fatal("failure should surface err")
	}
	m3n.upg = upgFailed
	if b := stripANSI(m3n.upgradeBanner()); !strings.Contains(b, "roger upgrade") {
		t.Errorf("failed banner should point at the CLI, got %q", b)
	}

	// x hides the banner entirely.
	mx, _, handled := m3n.onUpgradeKey("x")
	if !handled || mx.updateLine != "" || stripANSI(mx.upgradeBanner()) != "" {
		t.Fatalf("x should hide the banner (handled=%v line=%q)", handled, mx.updateLine)
	}

	// A running install ignores x (no half-finished dismissals).
	mr := m
	mr.upg = upgRunning
	if _, _, handled := mr.onUpgradeKey("x"); handled {
		t.Error("x during a running install must be ignored")
	}
	_ = tea.Quit
}
