package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/glyphs"
	"github.com/rogerai-fyi/roger/internal/operator"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// operator.go is the TUI glue for Guest Operators Phase 2 (THE DESK): the /operator
// command + picker, the staged PATCHING YOU THROUGH handoff via tea.ExecProcess, the
// return-to-the-desk summary, and the remote-control interlock hooks. Everything pure
// (registry / detection / config materialization) lives in internal/operator; this file
// keeps only command, picker, and exec glue. Specs: features/operator/*.feature
// (founder-approved 2026-07-07); design: rogerai-internal-docs/GUEST-OPERATORS.md.

// --- seams (package vars so the BDD drives the real model with no mocks) -------------

var (
	// operatorDetectEnv supplies the detection Env (real PATH by default); the picker's
	// r re-scan and the AGENT-entry scan both read it live.
	operatorDetectEnv = operator.DefaultEnv
	// operatorExec issues the child-process command (tea.ExecProcess in production; the
	// BDD records the composed *exec.Cmd instead of suspending the test terminal).
	operatorExec = func(c *exec.Cmd, fn func(error) tea.Msg) tea.Cmd {
		return tea.ExecProcess(c, fn)
	}
	// operatorTermOut receives the defensive terminal-reset preamble on return.
	operatorTermOut io.Writer = os.Stdout
	// operatorScratchRoot overrides where session scratch dirs are minted ("" = os.TempDir()).
	operatorScratchRoot = ""
	// operatorStageDelay is one beat of PATCHING YOU THROUGH: the staged frame is
	// GUARANTEED painted before the exec cmd is issued (anti-blank - the exec must never
	// cut from a stale screen to a foreign TUI).
	operatorStageDelay = 450 * time.Millisecond
)

// operatorStaleAge: scratch dirs older than this are crash leftovers, swept at the next
// desk scan (a crash of roger itself is the only path that leaks one).
const operatorStaleAge = 24 * time.Hour

// operatorResetSeq is the defensive terminal reset run on EVERY return from a guest
// (empirically needed - a guest TUI can leave any combination of modes on): pop the kitty
// keyboard protocol, disable all mouse reporting modes, exit bracketed paste. What the
// radio itself uses (mouse cell motion) is re-enabled AFTER this, only if the user has
// the mouse on (m.mouseOff is respected).
const operatorResetSeq = "\x1b[<u" + // pop the kitty keyboard protocol
	"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" + // all mouse reporting off
	"\x1b[?2004l" // exit bracketed paste

// --- messages -------------------------------------------------------------------------

type operatorDetectedMsg struct{ ds []operator.Detection } // an async desk scan landed
type operatorExecMsg struct{}                              // the staged paint elapsed; issue the exec
type operatorDoneMsg struct{ err error }                   // the ExecProcess return callback

// operatorHandoff is the live handoff state: staging (the PATCHING plate is up) until
// execing flips, then the guest has the terminal until operatorDoneMsg.
type operatorHandoff struct {
	det     operator.Detection
	launch  operator.Launch
	cleanup func() error
	start   time.Time
	execing bool
}

// operatorRow is one picker row: the resident DJ, a detected guest, or the single dim
// not-installed suggestion at the bottom (which the cursor skips).
type operatorRow struct {
	label      string
	det        operator.Detection
	isDJ       bool
	suggestion bool
	hint       string
}

// --- detection ------------------------------------------------------------------------

// operatorScanCmd scans the desk asynchronously (the onSharesDetected pattern): sweep
// stale crash leftovers, then LookPath + bounded version-probe every registry guest.
func operatorScanCmd() tea.Cmd {
	return func() tea.Msg {
		root := operatorScratchRoot
		if root == "" {
			root = os.TempDir()
		}
		operator.SweepStale(root, operatorStaleAge)
		return operatorDetectedMsg{ds: operator.Detect(operatorDetectEnv())}
	}
}

// onOperatorDetected folds an async desk scan into the model; an open picker re-derives
// its rows in place (the r re-scan) with the cursor clamped to a selectable row.
func (m model) onOperatorDetected(msg operatorDetectedMsg) (tea.Model, tea.Cmd) {
	m.operatorDetections = msg.ds
	if m.operatorPicker {
		m.operatorRows = m.buildOperatorRows()
		if m.operatorCursor >= len(m.operatorRows) {
			m.operatorCursor = 0
		}
		m.operatorCursor = operatorNearestSelectable(m.operatorRows, m.operatorCursor)
	}
	return m, nil
}

// --- the /operator command -----------------------------------------------------------

// runOperatorCommand dispatches /operator (aliases /mic /guest /op): bare opens the
// picker (never a zero-row one - §3), a name direct-jumps like /model <name>. Unknown
// names are a local note, NEVER a chat turn (no spend from a typo).
func (m model) runOperatorCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		if len(m.operatorDetections) == 0 {
			m.rcNote("no guests at the desk - a guest operator is an agent CLI on your PATH (opencode · hermes · aider)")
			return m, nil
		}
		m.operatorPicker = true
		m.operatorRows = m.buildOperatorRows()
		m.operatorCursor = 0
		return m, nil
	}
	want := strings.ToLower(args[0])
	for _, d := range m.operatorDetections {
		if strings.ToLower(d.Guest.Name) == want {
			return m.startOperatorHandoff(d)
		}
	}
	for _, g := range operator.Registry() {
		if strings.ToLower(g.Name) == want {
			m.rcNote(g.Name + " is not at the desk · get it: " + g.InstallHint)
			return m, nil
		}
	}
	m.rcNote(args[0] + " is not a known operator - /operator lists the desk")
	return m, nil
}

// buildOperatorRows derives the picker rows: the resident DJ first, every detected guest
// in registry order, then AT MOST ONE dim not-installed suggestion at the bottom - and
// only while the desk is sparse (a single guest). A healthy desk (2+ guests) advertises
// nothing (operator_command.feature: with opencode+aider detected the rows are exactly
// DJ · opencode · aider).
func (m model) buildOperatorRows() []operatorRow {
	rows := []operatorRow{{label: "DJ", isDJ: true}}
	seen := map[string]bool{}
	for _, d := range m.operatorDetections {
		rows = append(rows, operatorRow{label: d.Guest.Name, det: d})
		seen[d.Guest.Name] = true
	}
	if len(m.operatorDetections) < 2 {
		for _, g := range operator.Registry() {
			if !seen[g.Name] {
				rows = append(rows, operatorRow{label: g.Name, suggestion: true, hint: g.InstallHint})
				break // at most ONE suggestion row
			}
		}
	}
	return rows
}

// operatorNearestSelectable clamps a cursor onto a non-suggestion row (preferring the
// row itself, then upward) - the cursor is NEVER on the suggestion row.
func operatorNearestSelectable(rows []operatorRow, i int) int {
	for j := i; j >= 0; j-- {
		if j < len(rows) && !rows[j].suggestion {
			return j
		}
	}
	return 0
}

// onOperatorPickerKey owns EVERY key while the picker is open (the /model modal
// contract): cursor rows skip the suggestion, enter picks, r re-scans, esc keeps the DJ.
func (m model) onOperatorPickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	closePicker := func() {
		m.operatorPicker = false
		m.operatorRows = nil
	}
	switch k.String() {
	case "up", "k":
		for i := m.operatorCursor - 1; i >= 0; i-- {
			if !m.operatorRows[i].suggestion {
				m.operatorCursor = i
				break
			}
		}
		return m, nil
	case "down", "j":
		for i := m.operatorCursor + 1; i < len(m.operatorRows); i++ {
			if !m.operatorRows[i].suggestion {
				m.operatorCursor = i
				break
			}
		}
		return m, nil
	case "r":
		// Re-scan the desk in place; the rows re-derive when the scan lands.
		return m, operatorScanCmd()
	case "enter":
		if m.operatorCursor < 0 || m.operatorCursor >= len(m.operatorRows) {
			return m, nil
		}
		row := m.operatorRows[m.operatorCursor]
		closePicker()
		switch {
		case row.isDJ:
			m.rcNote("the DJ keeps the mic")
			return m, nil
		case row.det.Guest.NeedsSetup:
			// Installed-but-not-configured (reserved for the future claude row): a setup
			// note, never an exec.
			note := row.det.Guest.SetupNote
			if note == "" {
				note = row.label + " needs setup before it can take the mic"
			}
			m.rcNote(note)
			return m, nil
		default:
			return m.startOperatorHandoff(row.det)
		}
	case "esc":
		closePicker()
		m.rcNote("the DJ keeps the mic")
		return m, nil
	default:
		return m, nil // the picker is modal - swallow everything else
	}
}

// operatorPickerView renders the hand-the-mic modal (clones the /model picker shape:
// cursor carat row, pad() cells, truncVisible clamp, dim hint footer).
func (m model) operatorPickerView(w int) string {
	var b strings.Builder
	mdl := ""
	if m.proxyHolder != nil {
		mdl = m.proxyHolder.Get().Model
	}
	tail := ""
	if mdl != "" {
		tail = " - the guest runs on " + mdl + ", through your open channel"
	}
	b.WriteString("\n" + truncVisible("  "+stSelText.Render("hand the mic")+stDim.Render(tail), w) + "\n")
	for i, row := range m.operatorRows {
		switch {
		case row.suggestion:
			b.WriteString(truncVisible("  "+stDim.Render("   "+pad(row.label, 12)+" not at the desk · get it: "+row.hint), w) + "\n")
		case i == m.operatorCursor:
			b.WriteString(truncVisible("  "+stSelText.Render(" ▸ "+pad(row.label, 12))+" "+operatorRowGlyph(row)+stDim.Render(operatorRowDetail(row)), w) + "\n")
		default:
			b.WriteString(truncVisible("  "+stDim.Render("   "+pad(row.label, 12)+" ")+operatorRowGlyph(row)+stDim.Render(operatorRowDetail(row)), w) + "\n")
		}
	}
	hint := "↑↓ pick · ⏎ hand the mic · r re-scan the desk · esc keep the DJ"
	if m.narrow() {
		hint = "↑↓ · ⏎ · r · esc"
	}
	b.WriteString(truncVisible("  "+stDim.Render(hint), w) + "\n")
	return b.String()
}

// operatorBrandBlock renders a guest's optional brand plate for the PATCHING screen.
// The finished plates ride the Guest.Brand data seam (GUEST-OPERATOR-PLATES.md,
// "ONE HUE, ONE BEAT"); the legacy single-accent BrandPlate string stays supported.
// "" when the registry carries no plate - the seam costs nothing until the art lands.
func operatorBrandBlock(g operator.Guest, w int) string {
	if g.Brand != nil {
		return operatorBrandArtBlock(*g.Brand, w)
	}
	if g.BrandPlate == "" {
		return ""
	}
	st := operatorBrandStyle(g.BrandAccent)
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(g.BrandPlate, "\n"), "\n") {
		b.WriteString(truncVisible("  "+st.Render(line), w) + "\n")
	}
	return b.String()
}

// operatorBrandArtBlock renders a BrandArt plate per the doc's §7 fallback matrix:
// full styled art on a capable terminal; the one-line text lockup under
// ROGERAI_ASCII (never a folded/garbled wordmark - aider's pure-ASCII plate is the
// one that survives intact) or when the terminal is too narrow (shipped brand art
// is never cropped or re-wrapped, it is SWAPPED). NO_COLOR needs no branch here:
// lipgloss strips the SGR from the same art.
func operatorBrandArtBlock(art operator.BrandArt, w int) string {
	if (glyphs.ASCII() && !art.ASCIIArt) || w < art.Width+2 {
		lockup := art.Lockup
		lockup.Text = glyphs.Fold(lockup.Text) // · and … fold rune-for-rune; spans stay column-true
		return truncVisible("  "+operatorBrandRow(lockup), w) + "\n"
	}
	var b strings.Builder
	for _, row := range art.Rows {
		b.WriteString(truncVisible("  "+operatorBrandRow(row), w) + "\n")
	}
	return b.String()
}

// operatorBrandRow inks one plate row: whole-row Ink when it has no spans,
// otherwise each [From,To) rune span in its ink with uncovered columns plain
// (they are spaces in every shipped plate).
func operatorBrandRow(row operator.BrandRow) string {
	if len(row.Spans) == 0 {
		return operatorInkStyle(row.Ink).Render(row.Text)
	}
	runes := []rune(row.Text)
	var b strings.Builder
	col := 0
	for _, sp := range row.Spans {
		// Clamp BOTH bounds (defense in depth, pre-push audit minor): the shipped
		// data is golden-pinned in range, but a hand-edited plate must degrade to
		// plain text - never panic the PATCHING screen.
		from, to := sp.From, sp.To
		if from > len(runes) {
			from = len(runes)
		}
		if to > len(runes) {
			to = len(runes)
		}
		if from > col {
			b.WriteString(string(runes[col:from]))
			col = from
		}
		if to > from {
			b.WriteString(operatorInkStyle(sp.Ink).Render(string(runes[from:to])))
			col = to
		}
	}
	if col < len(runes) {
		b.WriteString(string(runes[col:]))
	}
	return b.String()
}

// operatorInkStyle maps a registry ink to a house style: named tokens hit the
// shared palette (InkRed is deliberately cRed NON-bold - a glint, not a surface),
// custom hues become adaptive dark/light pairs, the zero ink renders plain.
func operatorInkStyle(ink operator.BrandInk) lipgloss.Style {
	switch ink.Token {
	case operator.InkDim:
		return stDim
	case operator.InkBrand:
		return stBrand
	case operator.InkKey:
		return stKey
	case operator.InkRed:
		return lipgloss.NewStyle().Foreground(cRed)
	case operator.InkRedBold:
		return stRed
	}
	if ink.Dark == "" {
		return lipgloss.NewStyle()
	}
	light := ink.Light
	if light == "" {
		light = ink.Dark
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: light, Dark: ink.Dark})
	if ink.Bold {
		st = st.Bold(true)
	}
	return st
}

// operatorBrandStyle maps a registry accent to a render style (the house stKey when "").
// Accents are data ("#fab387" / an ANSI-256 index); NO_COLOR stripping still applies.
func operatorBrandStyle(accent string) lipgloss.Style {
	if accent == "" {
		return stKey
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(accent))
}

// operatorRowGlyph is the optional per-brand picker-row glyph (one cell + a space), "".
func operatorRowGlyph(row operatorRow) string {
	if row.det.Guest.BrandGlyph == "" {
		return ""
	}
	return operatorBrandStyle(row.det.Guest.BrandAccent).Render(row.det.Guest.BrandGlyph) + " "
}

// operatorRowDetail is the dim descriptor cell for a selectable picker row.
func operatorRowDetail(row operatorRow) string {
	if row.isDJ {
		return "resident · the house agent · stays in the TUI"
	}
	d := "guest · patches into your open channel · billed as usual"
	if row.det.Guest.NeedsSetup {
		d = "needs setup first - pick it to see how"
	} else if row.det.Unverified {
		v := row.det.Version
		if v == "" {
			v = "unknown"
		}
		d += " · version " + v + " unproven"
	}
	return d
}

// --- the handoff ----------------------------------------------------------------------

// startOperatorHandoff gates the handoff on the channel actually carrying it, then puts
// up ONE staged PATCHING YOU THROUGH paint before the exec is issued (anti-blank).
func (m model) startOperatorHandoff(d operator.Detection) (tea.Model, tea.Cmd) {
	// No band tuned: a disconnected proxy REFUSES to spend (Phase 1 ruling 5), so a
	// launch now would only hand the guest a wall of 502s - block it at the desk.
	if m.proxyHolder == nil || !m.proxyHolder.Connected() {
		m.agentLines = append(m.agentLines,
			stRed.Render("✕ ")+stEmber.Render("no channel to patch into - a guest runs on your open channel"),
			stDim.Render("· ")+stDim.Render("tune in first: press ")+stKey.Render("[1]")+stDim.Render(", ⏎ on a band opens the channel · then come back with ")+stKey.Render("[0]"))
		return m, nil
	}
	// The DJ's in-flight turn owns the completer and the terminal; a queued prompt
	// would drain into a new turn against a suspended TUI - both block the handoff.
	if m.agentBusy || (m.agent != nil && m.agent.running.Load()) {
		m.rcNote("the DJ is mid-turn - let it finish (esc cancels), then hand off")
		return m, nil
	}
	if len(m.agentQueued) > 0 {
		m.rcNote("prompts are queued for the DJ - let the queue drain, then hand off")
		return m, nil
	}
	m.operatorPicker = false
	m.operatorRows = nil
	m.operatorHandoff = &operatorHandoff{det: d}
	m.status = stDim.Render("patching you through to " + d.Guest.Name + "…")
	// One beat of staging: the plate paints, THEN operatorExecMsg issues the exec.
	return m, tea.Tick(operatorStageDelay, func(time.Time) tea.Msg { return operatorExecMsg{} })
}

// onOperatorExec materializes the wiring from the LIVE proxy options, arms the fresh
// per-handoff budget, parks the remote-control bridge, and issues the exec command.
func (m model) onOperatorExec() (tea.Model, tea.Cmd) {
	h := m.operatorHandoff
	if h == nil || h.execing {
		return m, nil
	}
	if m.proxyHolder == nil { // defensive: staging outlived the proxy
		m.operatorHandoff = nil
		return m, nil
	}
	// Re-check the DJ-idle preconditions AT EXEC TIME (audit regression): the bridge
	// parks only now, so a turn injected during the staging beat would otherwise run -
	// and bill - under the suspended TUI, into the guest's freshly reset accumulator.
	// The mode check covers global keys (ctrl+c quit-confirm, alt+m, a preset) pulling
	// the TUI off AGENT mid-staging - never exec the guest under another modal.
	if m.mode != modeAgent || m.agentBusy || (m.agent != nil && m.agent.running.Load()) || len(m.agentQueued) > 0 {
		m.operatorHandoff = nil
		if m.mode != modeAgent {
			m.rcNote("handoff aborted - you left the desk mid-patch · /operator from AGENT to try again")
		} else {
			m.rcNote("handoff aborted - the DJ picked up a turn while patching · /operator again once it finishes")
		}
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		return m, nil
	}
	// LIVE options at exec time - never the options frozen at first bind.
	opts := m.proxyHolder.Get()
	sess := operator.Session{
		BaseURL: m.endpoint, SessionKey: opts.SessionKey, Model: opts.Model,
		Workdir: agentRoot(), ScratchRoot: operatorScratchRoot,
	}
	launch, cleanup, err := operator.Materialize(h.det.Guest, sess)
	if err != nil {
		m.operatorHandoff = nil
		m.agentLines = append(m.agentLines, stRed.Render("✕ ")+stEmber.Render("couldn't hand the mic off: "+err.Error()))
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		return m, nil
	}
	// Fresh money state per handoff (ruling 4): the $2 default budget, zero spend, zero
	// calls - the summary and the 402 ceiling are THIS guest's numbers. The bearer key
	// is NOT rotated (a re-tune mid-session keeps the guest's config working).
	m.proxyHolder.SetBudget(client.DefaultSessionBudget)
	m.proxyHolder.ResetSpend()
	m.proxyHolder.ResetCalls()
	h.launch, h.cleanup, h.start, h.execing = launch, cleanup, time.Now(), true
	// BASE STATION interlock: announce the handoff, then PARK the bridge BEFORE the exec
	// cmd is returned - inbound remote turns are dropped at the bridge with a status
	// auto-frame (never queued, never replayed), backfill is answered from this snapshot.
	if m.rcBridge != nil {
		m.rcEmit(client.OperatorStatusFrame(h.det.Guest.Name))
		m.rcBridge.Park(h.det.Guest.Name, m.agentTranscriptText())
	}
	c := operator.Command(launch, h.det.Path, sess.Workdir, os.Environ())
	return m, operatorExec(c, func(err error) tea.Msg { return operatorDoneMsg{err: err} })
}

// onOperatorDone is the return to the desk - it runs for EVERY child outcome: defensive
// terminal reset, scratch cleanup, bridge unpark + status frame, balance refresh, and the
// honest one-line summary read from the proxy accumulator (never the child's claims).
func (m model) onOperatorDone(msg operatorDoneMsg) (tea.Model, tea.Cmd) {
	h := m.operatorHandoff
	m.operatorHandoff = nil
	if h == nil {
		return m, nil
	}
	if h.cleanup != nil {
		_ = h.cleanup() // every return path cleans the scratch config
	}
	// Defensive terminal reset FIRST (the guest may have left kitty-kbd / mouse /
	// bracketed-paste modes on), then re-enable only what the radio uses (below).
	_, _ = io.WriteString(operatorTermOut, operatorResetSeq)
	// Unpark the bridge and announce the DJ is back. Nil-safe and dead-bridge-safe: a
	// revoke-all mid-handoff Stops the bridge; Unpark/Emit are no-ops then.
	if m.rcBridge != nil {
		m.rcBridge.Unpark()
		m.rcEmit(protocol.RCFrame{Kind: protocol.RCKindStatus, Text: "the DJ is back at the desk"})
	}
	guest := h.det.Guest.Name
	cmds := []tea.Cmd{fetchBalance(m.broker, m.user)}
	if !m.mouseOff {
		cmds = append(cmds, tea.EnableMouseCellMotion)
	}
	// The summary numbers come from the proxy accumulator (duration is measured here),
	// and the interactive TUI goes back to UNCAPPED (Phase 1: Budget 0 for the hands-on
	// flow) on EVERY return path - including a spawn failure (audit regression: the
	// early return used to leave the DJ session parked at the guest's $2 cap).
	var spend float64
	var calls int64
	budget := 0.0
	if m.proxyHolder != nil {
		spend, calls = m.proxyHolder.Spent(), m.proxyHolder.Calls()
		budget = m.proxyHolder.Get().Budget
		m.proxyHolder.SetBudget(0)
	}
	// A spawn failure (the exec never started) is the one true error note.
	var ee *exec.ExitError
	if msg.err != nil && !errors.As(msg.err, &ee) {
		m.agentLines = append(m.agentLines, stRed.Render("✕ ")+stEmber.Render("couldn't hand the mic off to "+guest+": "+msg.err.Error()))
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		return m, tea.Batch(cmds...)
	}
	summary := guest + " had the mic for " + operatorFmtDur(time.Since(h.start)) +
		" · " + plural(int(calls), "call") + " · " + fmt.Sprintf("$%.2f", spend)
	if ee != nil {
		// A guest quitting (Ctrl-C = 130, any non-zero, or a signal) is NORMAL radio
		// traffic: the calm house ✕, never a scary escalation.
		drop := "the guest dropped off - back at the desk"
		if code := ee.ExitCode(); code >= 0 {
			drop = fmt.Sprintf("the guest dropped off (exit %d) - back at the desk", code)
		}
		m.agentLines = append(m.agentLines, stRed.Render("✕ ")+stEmber.Render(drop))
		m.rcNote(summary)
	} else {
		m.agentLines = append(m.agentLines, stDim.Render("· ")+stDim.Render("back at the desk · ")+stDim.Render(summary)+stDim.Render(" · the DJ is standing by"))
	}
	if budget > 0 && spend >= budget-1e-9 {
		// "The guest went quiet" must never be a mystery: the ceiling was the reason.
		m.agentLines = append(m.agentLines, stDim.Render("· ")+stEmber.Render("the session budget was reached")+stDim.Render(" - the proxy answered 402 past "+fmt.Sprintf("$%.2f", budget)))
	}
	m.status = stDim.Render("back at the desk · the DJ is standing by")
	return m, tea.Batch(cmds...)
}

// operatorFmtDur renders a mic-time duration radio-style: 42s / 14m / 1h05m.
func operatorFmtDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// operatorPatchView is the ONE staged PATCHING YOU THROUGH paint (the connectingView
// staging discipline): mic-to / on-band / wire lines + the live BASE URL / MODEL plate,
// painted before the exec so the cut to the guest TUI is never from a stale screen.
func (m model) operatorPatchView(w int) string {
	h := m.operatorHandoff
	if h == nil {
		return ""
	}
	mdl := ""
	if m.proxyHolder != nil {
		mdl = m.proxyHolder.Get().Model
	}
	// The windowshade keeps the handoff to ONE static line (plates doc §1b: compact
	// is prefers-reduced-motion; at one line the guest's name IS the brand). Shared
	// template for every guest; truncVisible clamps so the band name truncates first.
	if m.compact {
		line := "  " + stRed.Render(beaconDot()) + " " + stDim.Render("patching ") +
			stKey.Render(h.det.Guest.Name) + stDim.Render(glyphs.Fold(" through on "+mdl+"…"))
		return truncVisible(line, w) + "\n"
	}
	var b strings.Builder
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("AGENT") + stDim.Render(" · handing off") +
		"      " + stRed.Render(glyphs.Fold("((•))")) + "  " + stBrand.Render("PATCHING YOU THROUGH…") + "\n\n")
	step := func(label, val, detail string) {
		line := "  " + stRed.Render(glyphOnAir) + " " + stDim.Render(pad(label, 8)) + stKey.Render(val)
		if detail != "" {
			line += stDim.Render("  " + detail)
		}
		b.WriteString(truncVisible(line, w) + "\n")
	}
	// PER-BRAND PLATE SEAM: the design pass lands each operator's wordmark as data-only
	// registry changes (Guest.BrandPlate/BrandAccent); the default is the text-only house
	// style (the name on the mic-to line below carries it until the art exists).
	if brand := operatorBrandBlock(h.det.Guest, w); brand != "" {
		b.WriteString(brand + "\n")
	}
	step("mic to", h.det.Guest.Name, "(guest operator)")
	step("on band", mdl, "your open channel · usual relay pricing")
	step("wire", "config generated in a scratch dir", "your own setup is untouched")
	row := func(label, value string) string {
		return "      " + stDim.Render(pad(label, 9)) + stKey.Render(value)
	}
	b.WriteString("\n" + truncVisible(row("BASE URL", m.endpoint), w) + "\n")
	b.WriteString(truncVisible(row("MODEL", mdl), w) + "\n\n")
	b.WriteString(truncVisible("  "+stDim.Render("the radio steps aside while the guest is on the mic · exit the guest to come back"), w) + "\n")
	return b.String()
}
