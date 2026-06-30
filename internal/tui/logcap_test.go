package tui

import (
	"log"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// resetTUILog clears the shared session buffer so each test asserts deterministically.
func resetTUILog() {
	tuiLog.mu.Lock()
	tuiLog.lines = nil
	tuiLog.mu.Unlock()
}

func TestLogRingCapturesAndCaps(t *testing.T) {
	r := &logRing{max: 3}
	_, _ = r.Write([]byte("a\nb\n"))
	_, _ = r.Write([]byte("c\nd\n")) // overflow: oldest dropped
	if got := strings.Join(r.snapshot(), ","); got != "b,c,d" {
		t.Fatalf("ring should keep the newest 3 lines, got %q", got)
	}
}

// launchTUI must point the std logger at the in-memory ring for the program's life (so
// node/agent log lines never paint over the alt-screen TUI) and restore it on exit.
func TestLaunchTUIRedirectsAndRestoresStdLogger(t *testing.T) {
	resetTUILog()
	prevRun := runProgram
	defer func() { runProgram = prevRun }()
	runProgram = func(m tea.Model, opts ...tea.ProgramOption) error {
		log.Printf("registered with broker as node test")
		return nil
	}
	before := log.Writer()
	if err := launchTUI(nil); err != nil {
		t.Fatalf("launchTUI: %v", err)
	}
	if log.Writer() != before {
		t.Errorf("launchTUI must restore the std logger output after the program exits")
	}
	if got := strings.Join(tuiLog.snapshot(), "\n"); !strings.Contains(got, "registered with broker as node test") {
		t.Fatalf("the node log line should be captured in tuiLog (not the terminal); got %q", got)
	}
}

func TestLogViewEmptyAndPopulated(t *testing.T) {
	resetTUILog()
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	m.mode = modeLog
	if v := stripANSI(m.logView(100)); !strings.Contains(v, "no messages") {
		t.Fatalf("empty logView should show a no-messages placeholder, got:\n%s", v)
	}
	tuiLog.Write([]byte("broker restarted - re-registered node test\n"))
	v := stripANSI(m.View())
	if !strings.Contains(v, "LOG") || !strings.Contains(v, "re-registered node test") {
		t.Fatalf("logView should show the LOG header + the captured line, got:\n%s", v)
	}
}

func TestSlashLogOpensAndAnyKeyCloses(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	nm, _ := m.run("log")
	if asModel(nm).mode != modeLog {
		t.Fatalf("/log should open modeLog, got %v", asModel(nm).mode)
	}
	r, _ := nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if asModel(r).mode != modeBrowse {
		t.Errorf("any key in modeLog should close back to browse, got %v", asModel(r).mode)
	}
}
