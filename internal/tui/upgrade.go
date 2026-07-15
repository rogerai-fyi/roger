package tui

// In-TUI upgrade (the founder's "opencode told me there was an update in a banner,
// asked upgrade or skip, and did it right there"): the passive update notice is an
// ACTIONABLE banner in BROWSE. `u` downloads, checksum-verifies and atomically
// installs the new binary via the same update.Upgrade the CLI uses; `x` hides the
// banner for this session. A finished upgrade offers `u` again as a one-key restart:
// the TUI exits cleanly and the caller re-execs the freshly installed binary.

import (
	"bytes"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/update"
)

// upgState is the banner's lifecycle: an offer, a running install, a restart offer,
// or a failure (which keeps the CLI path as the fallback).
type upgState int

const (
	upgIdle    upgState = iota // notice present: offer u upgrade / x hide
	upgRunning                 // download + verify + install in flight
	upgDone                    // installed: offer u restart / x later
	upgFailed                  // install failed: name the error, point at `roger upgrade`
)

// upgradeDoneMsg is the background install's completion (err nil = installed).
type upgradeDoneMsg struct{ err error }

// runUpgrade is a seam over update.Upgrade so tests drive the whole banner flow
// without network access or binary replacement.
var runUpgrade = update.Upgrade

// ErrRestart is returned by RunWithController when the user chose "restart now"
// after an in-TUI upgrade: the caller (cmd/rogerai) re-execs the new binary.
var ErrRestart = errors.New("restart into the upgraded binary")

// wantRestart carries the restart choice across the Bubble Tea exit (the framework
// returns the final model by value; a package var is the plain channel out).
var wantRestart bool

// startUpgrade launches the install in the background; the buffer swallows the CLI
// progress prose (the banner carries the state instead).
func startUpgrade(version string) tea.Cmd {
	return func() tea.Msg {
		var buf bytes.Buffer
		return upgradeDoneMsg{err: runUpgrade(version, &buf)}
	}
}

// upgradeBanner renders the update row for the current state ("" when there is no
// notice at all). It rides in the status area, one row, same as the old notice.
func (m model) upgradeBanner() string {
	if m.updateLine == "" {
		return ""
	}
	switch m.upg {
	case upgRunning:
		return stEmber.Render("⇪ upgrading … downloading + verifying (a few seconds)")
	case upgDone:
		return stLive.Render("✓ upgraded · ") + stKey.Render("u") + stLive.Render(" restarts now · ") +
			stKey.Render("x") + stLive.Render(" later (next launch is the new version)")
	case upgFailed:
		return stEmber.Render("✕ upgrade failed - try `roger upgrade` in a terminal · x hides")
	}
	// Idle: the notice + the two keys. The keys act in BROWSE (typing views keep them).
	return stEmber.Render("⇪ "+strings.TrimSuffix(m.updateLine, " · run 'roger upgrade'")) +
		stDim.Render(" · ") + stKey.Render("u") + stDim.Render(" upgrade now · ") +
		stKey.Render("x") + stDim.Render(" hide")
}

// onUpgradeKey handles the banner keys from BROWSE. handled=false when the key is
// not the banner's to take (no notice, or a state that ignores it).
func (m model) onUpgradeKey(key string) (model, tea.Cmd, bool) {
	if m.updateLine == "" {
		return m, nil, false
	}
	switch {
	case key == "u" && m.upg == upgIdle:
		m.upg = upgRunning
		return m, startUpgrade(helpVersion), true
	case key == "u" && m.upg == upgDone:
		wantRestart = true
		return m, tea.Quit, true
	case key == "x" && m.upg != upgRunning:
		m.updateLine = ""
		m.upg = upgIdle
		return m, nil, true
	}
	return m, nil, false
}
