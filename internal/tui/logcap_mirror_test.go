package tui

// The Edge's log tail needs the same lines the terminal captures. For the life of a TUI this package
// owns the standard logger, so without a mirror the Edge ring sees nothing at all while the terminal
// is up - which is precisely when an operator is looking (features/edge/logs.feature).

import (
	"log"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// mirrorSink is somewhere to send the mirrored lines, safe for the logger's own goroutines.
type mirrorSink struct {
	mu    sync.Mutex
	lines []string
}

func (m *mirrorSink) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lines = append(m.lines, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (m *mirrorSink) all() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.lines, "\n")
}

func TestLogCaptureMirrorsToAnExtraDestination(t *testing.T) {
	resetTUILog()
	sink := &mirrorSink{}
	SetLogMirror(sink)
	t.Cleanup(func() { SetLogMirror(nil) })

	prevRun := runProgram
	t.Cleanup(func() { runProgram = prevRun })
	runProgram = func(tea.Model, ...tea.ProgramOption) error {
		log.Println("registered with broker as roggentooedged")
		return nil
	}

	before := log.Writer()
	if err := launchTUI(nil); err != nil {
		t.Fatalf("launchTUI: %v", err)
	}
	if log.Writer() != before {
		t.Error("launchTUI must still restore the std logger, mirror or not")
	}
	if got := sink.all(); !strings.Contains(got, "registered with broker") {
		t.Fatalf("the mirror saw nothing, so the Edge log tail would be empty: %q", got)
	}
	if got := strings.Join(tuiLog.snapshot(), "\n"); !strings.Contains(got, "registered with broker") {
		t.Fatalf("/log must still get the lines too: %q", got)
	}
}

func TestLogCaptureWithoutAMirrorIsUnchanged(t *testing.T) {
	resetTUILog()
	SetLogMirror(nil)
	prevRun := runProgram
	t.Cleanup(func() { runProgram = prevRun })
	runProgram = func(tea.Model, ...tea.ProgramOption) error {
		log.Println("broker restarted - re-registered node test")
		return nil
	}
	if err := launchTUI(nil); err != nil {
		t.Fatalf("launchTUI: %v", err)
	}
	if got := strings.Join(tuiLog.snapshot(), "\n"); !strings.Contains(got, "re-registered node test") {
		t.Fatalf("with no mirror the buffer must capture exactly as it did: %q", got)
	}
}
