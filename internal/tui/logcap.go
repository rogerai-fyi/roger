package tui

import (
	"io"
	"log"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// ── log capture ───────────────────────────────────────────────────────────────
// The share node + agent log via the STANDARD logger (e.g. agent.go's "registered
// with broker …" / "broker restarted - re-registered node …"). On the alt-screen TUI
// those writes land straight on the terminal and paint OVER the render, corrupting it
// (the founder saw log lines stomping the band list + the Ping screensaver). So for the
// life of any TUI program we point the std logger at this in-memory ring instead, and
// surface the captured lines on demand via /log (modeLog) - hidden by default, readable
// when you ask. Concurrency-safe: the agent logs from its own goroutines.

type logRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ln := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if ln != "" {
			r.lines = append(r.lines, ln)
		}
	}
	if r.max > 0 && len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	return len(p), nil
}

// snapshot returns a copy of the captured lines (oldest first), safe on the UI goroutine.
func (r *logRing) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// tuiLog is the session log buffer (capped), shown by /log.
var tuiLog = &logRing{max: 500}

// logMirror is an EXTRA destination for the captured lines, set by the binary. The Edge's log tail
// needs the same lines (features/edge/logs.feature), and for the life of a TUI this package owns the
// standard logger - so without a mirror the Edge ring would see nothing at all while the terminal is
// up, which is precisely when an operator is looking. Guarded by its own mutex: it is set once at
// start-up and read on every log write.
var (
	mirrorMu  sync.Mutex
	logMirror io.Writer
)

// SetLogMirror sends every captured log line to w as well as to the /log buffer. Pass nil to stop.
func SetLogMirror(w io.Writer) {
	mirrorMu.Lock()
	logMirror = w
	mirrorMu.Unlock()
}

// mirror returns the extra destination, or nil.
func mirror() io.Writer {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	return logMirror
}

// launchTUI runs a Bubble Tea program with the std logger redirected into tuiLog for the
// program's lifetime (restored on exit), so node/agent log lines never corrupt the
// alt-screen render. The run itself goes through the runProgram seam (swappable in tests).
func launchTUI(m tea.Model, opts ...tea.ProgramOption) error {
	prev := log.Writer()
	out := io.Writer(tuiLog)
	if m := mirror(); m != nil {
		out = io.MultiWriter(tuiLog, m) // the Edge's log tail reads the same lines
	}
	log.SetOutput(out)
	defer log.SetOutput(prev)
	return runProgram(m, opts...)
}

// logView renders the captured node/broker log buffer (modeLog, opened with /log). The
// std-logger output is redirected here while the TUI runs (launchTUI) so it never
// corrupts the render; this is where the operator actually reads it. Newest at the
// bottom; only the lines that fit the terminal height are shown.
func (m model) logView(w int) string {
	var b strings.Builder
	b.WriteString("  " + stBrand.Render("LOG") + stDim.Render("   node + broker messages · ") +
		stKey.Render("esc") + stDim.Render(" close") + "\n\n")
	lines := tuiLog.snapshot()
	if len(lines) == 0 {
		b.WriteString("  " + stDim.Render("(no messages yet)"))
		return b.String()
	}
	if max := m.height - 5; max > 0 && len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	for _, ln := range lines {
		b.WriteString("  " + stDim.Render(truncVisible(ln, w-2)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
