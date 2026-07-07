package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/glyphs"
	"github.com/rogerai-fyi/roger/internal/operator"
	"github.com/rogerai-fyi/roger/internal/pricetier"
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
	// operatorWorkdir resolves the workdir a guest is confirmed into on the pre-launch
	// plate and execed with (agentRoot - the process cwd - in production; the BDD points
	// it at scenario sandboxes so a plate never shows the developer's real cwd).
	operatorWorkdir = agentRoot
)

// operatorCtxFloor is the agent-ready hard floor (design doc §6): a coding agent on a
// sub-16k window fails on its FIRST prompts - context overflow read as "RogerAI is
// broken" - so the handoff is refused BEFORE any spend. The gate reads the OPEN
// CHANNEL's station (m.connected), because that is the station the guest is actually
// patched into. An UNKNOWN window (ctx 0) warns on the plate instead of blocking
// (ruling G2: real /discover feeds carry offers without ctx, and blocking on missing
// metadata would gate healthy 70B bands off the desk).
const operatorCtxFloor = 16384

// operatorBudgetLadder is the plate's preset spend ceilings (ruling B1): the $2.00
// default -> $5.00 -> $10.00 -> uncapped (holder Budget 0, the Phase 1 semantic),
// wrapping back to the default. Non-sticky (ruling B2): every fresh plate starts at
// index 0, and the choice arms the holder ONLY on an explicit accept.
var operatorBudgetLadder = []float64{client.DefaultSessionBudget, 5, 10, 0}

// operatorBudgetLabel renders a ladder value ("$2.00" / "uncapped").
func operatorBudgetLabel(v float64) string {
	if v <= 0 {
		return "uncapped"
	}
	return dollars(v)
}

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
	"\x1b[?1004l" + // focus reporting off (a guest can leave it spraying ESC[I/ESC[O)
	"\x1b[?2004l" // exit bracketed paste (re-armed via tea.EnableBracketedPaste after)

// --- messages -------------------------------------------------------------------------

type operatorDetectedMsg struct{ ds []operator.Detection } // an async desk scan landed
type operatorExecMsg struct{}                              // the staged paint elapsed; issue the exec
type operatorDoneMsg struct{ err error }                   // the ExecProcess return callback

// operatorHandoff is the live handoff state: staging (the PATCHING plate is up) until
// execing flips, then the guest has the terminal until operatorDoneMsg. budget and
// workdir carry the pre-launch plate's confirmed choices into the exec.
type operatorHandoff struct {
	det     operator.Detection
	budget  float64 // the plate-armed spend ceiling (0 = uncapped, the Phase 1 semantic)
	workdir string  // the plate-confirmed workdir the guest is execed in
	launch  operator.Launch
	cleanup func() error
	start   time.Time
	execing bool
}

// operatorPlate is the Phase 3 pre-launch confirm plate: everything the user is
// deciding on, captured at open time. NOTHING is armed until the explicit y - cancel
// leaves no trace (no scratch, no budget change, no spend).
type operatorPlate struct {
	det        operator.Detection
	workdir    string // resolved absolute workdir captured at open (operatorWorkdir seam)
	budgetIdx  int    // index into operatorBudgetLadder (non-sticky: a fresh plate is 0)
	homeGate   bool   // the exactly-$HOME second gate is up (ruling W1: double-y)
	fromPicker bool   // n/esc returns to the picker (cursor restored), not the prompt
}

// operatorRow is one picker row: the resident DJ, a detected guest (possibly disabled
// by the agent-ready band gate), or the single dim not-installed suggestion at the
// bottom (which the cursor skips).
type operatorRow struct {
	label      string
	det        operator.Detection
	isDJ       bool
	suggestion bool
	hint       string
	disabled   bool   // the band gate is shut for this guest (enter prints the reason)
	reason     string // the honest disabled reason ("needs a 16k+ band")
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
	// AGENT [0] DESK entry (R3): on the FRESH landing (nothing tuned in, nothing typed,
	// no modal, empty transcript beyond the entry chrome), a scan that lands GUESTS turns
	// THE DESK into the focused, selectable operator picker - the ask box hands focus over
	// until the user types through. Zero guests keeps the ask focused (nothing to pick).
	if m.deskEntryEligible() && len(deskGuests(m.operatorDetections)) > 0 {
		m.deskFocused = true
		m.deskCursor = 0
		m.agentIn.Blur()
	}
	return m, nil
}

// deskEntryEligible reports whether the AGENT is on the FRESH landing where THE DESK may
// take focus: AGENT mode, no channel/model, the ask box empty (nothing typed), the
// landing transcript untouched, no turn running, and no modal / plate / handoff up.
func (m model) deskEntryEligible() bool {
	if m.mode != modeAgent || m.deskFocused {
		return false
	}
	if m.proxyHolder != nil || m.connected != nil || m.resolveAgentModel() != "" {
		return false // a channel is (or was) up - not a fresh landing
	}
	if strings.TrimSpace(m.agentIn.Value()) != "" || m.agentBusy || (m.agent != nil && m.agent.running.Load()) {
		return false
	}
	if len(m.agentLines) != m.agentLandingLines {
		return false
	}
	return !m.operatorPicker && !m.agentPicker && m.agentPendingConfirm == nil && m.operatorPlate == nil && m.operatorHandoff == nil
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
			// Direct-jump parity with the picker row detail: an unverified guest gets
			// the same dim unproven-version disclosure before the handoff (it still
			// hands off - unproven is honesty, not a block, same as picker enter).
			if d.Unverified {
				v := d.Version
				if v == "" {
					v = "unknown"
				}
				m.rcNote(d.Guest.Name + " · version " + v + " unproven")
			}
			return m.startOperatorHandoff(d, false)
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
	gateShut := m.operatorBandTooSmall() // the DJ row above is NEVER gated
	for _, d := range m.operatorDetections {
		r := operatorRow{label: d.Guest.Name, det: d}
		if gateShut && !d.Guest.NeedsSetup {
			// The agent-ready band gate (§6): still listed - the desk is honest about who
			// exists - but disabled with the real reason; enter prints it, never a plate.
			r.disabled, r.reason = true, "needs a 16k+ band"
		}
		rows = append(rows, r)
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
		if row.isDJ {
			m.rcNote("the DJ keeps the mic")
			return m, nil
		}
		// Every guest pick funnels through startOperatorHandoff: it owns the setup-note
		// path (ONE gate for the picker AND the direct-jump - iteration-1 finding #5),
		// the channel/DJ-idle preconditions, the agent-ready band gate (a disabled
		// row's enter prints the honest refusal there), and the pre-launch plate.
		return m.startOperatorHandoff(row.det, true)
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
	// Honest header: only claim an open channel when one is actually tuned. Disconnected, the
	// model string survives in the holder but a select would refuse (Phase 1 ruling 5) - so
	// point the user to tune in first instead of promising a channel that is not there (#5).
	tail := ""
	switch {
	case m.proxyHolder == nil || !m.proxyHolder.Connected():
		tail = " - tune in first · a guest runs on your open channel"
	case mdl != "":
		tail = " - the guest runs on " + mdl + ", through your open channel"
	}
	b.WriteString("\n" + truncVisible("  "+stSelText.Render("hand the mic")+stDim.Render(tail), w) + "\n")
	// R2 (amends GUEST-OPERATOR-PLATES.md §6 "no brand art in the picker"): the SELECTED
	// operator's marquee plate, in its one canonical hue - the same renderer THE DESK uses.
	// The list rows below stay mono+red. The cursor never lands on a suggestion row.
	if m.operatorCursor >= 0 && m.operatorCursor < len(m.operatorRows) {
		if row := m.operatorRows[m.operatorCursor]; row.isDJ {
			b.WriteString(operatorBrandArtBlock(djBrandArt(), w))
		} else if !row.suggestion {
			b.WriteString(deskMarqueeForGuest(row.det.Guest, w))
		}
	}
	for i, row := range m.operatorRows {
		switch {
		case row.suggestion:
			b.WriteString(truncVisible("  "+stDim.Render("   "+pad(row.label, 12)+" not at the desk · get it: "+row.hint), w) + "\n")
		case row.disabled && i == m.operatorCursor:
			// Gated by the agent-ready floor: still cursor-able (enter prints the honest
			// reason), rendered dim with the refusal so the row explains itself.
			b.WriteString(truncVisible("  "+stSelText.Render(" ▸ "+pad(row.label, 12))+" "+stDim.Render("✕ "+row.reason+" - this channel's window is too small"), w) + "\n")
		case row.disabled:
			b.WriteString(truncVisible("  "+stDim.Render("   "+pad(row.label, 12)+" ✕ "+row.reason), w) + "\n")
		case i == m.operatorCursor:
			b.WriteString(truncVisible("  "+stSelText.Render(" ▸ "+pad(row.label, 12))+" "+stDim.Render(operatorRowDetail(row)), w) + "\n")
		default:
			b.WriteString(truncVisible("  "+stDim.Render("   "+pad(row.label, 12)+" ")+stDim.Render(operatorRowDetail(row)), w) + "\n")
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
// "ONE HUE, ONE BEAT"). "" when the registry carries no plate.
func operatorBrandBlock(g operator.Guest, w int) string {
	if g.Brand != nil {
		return operatorBrandArtBlock(*g.Brand, w)
	}
	return ""
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
		if from < 0 {
			from = 0
		}
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

// --- the agent-ready band gate (Phase 3, design doc §6) --------------------------------

// operatorChannelCtx reads the OPEN CHANNEL's station window (m.connected - the station
// the guest is actually patched into), never the band's best station. (0, false) when
// nothing is connected or the station reports no window.
func (m model) operatorChannelCtx() (ctx int, estimated bool) {
	if m.connected == nil {
		return 0, false
	}
	return m.connected.Ctx, m.connected.CtxEstimated
}

// operatorCtxLabel renders a window with the house ~ estimate honesty ("8k" / "~8k").
// It TRUNCATES to the familiar window name (spec-pinned: 32768 -> "32k", 131072 ->
// "131k") where the band-table fmtCtx rounds (32768 -> "33k") - the desk speaks the
// name users know their models by.
func operatorCtxLabel(ctx int, est bool) string {
	label := "-"
	switch {
	case ctx >= 1000:
		label = fmt.Sprintf("%dk", ctx/1000)
	case ctx > 0:
		label = fmt.Sprintf("%d", ctx)
	}
	if est && ctx > 0 {
		return "~" + label
	}
	return label
}

// operatorBandTooSmall: the gate is shut only when the window is KNOWN (detected or
// estimated) and under the floor. Unknown (ctx 0) is a plate warn, never a block (G2).
func (m model) operatorBandTooSmall() bool {
	ctx, _ := m.operatorChannelCtx()
	return ctx > 0 && ctx < operatorCtxFloor
}

// operatorWindowLabel names a window for a refusal, with the ~ honesty - and with the
// 16000-16383 corner named EXACTLY: truncation would collapse it onto "16k", the floor's
// own name, and a refusal must never read "the window is 16k, needs 16k+" (review
// regression, band_gate.feature "Boundary honesty").
func operatorWindowLabel(ctx int, est bool) string {
	label := operatorCtxLabel(ctx, est)
	if strings.TrimPrefix(label, "~") == "16k" && ctx < operatorCtxFloor {
		label = fmt.Sprintf("%d tokens", ctx)
		if est {
			label = "~" + label
		}
	}
	return label
}

// operatorRefuseSmallBand prints the honest refusal (the adversarial pin: blame the
// BAND, never the radio): name the window and the floor, point at re-tuning to a larger
// band - a local note, never a chat turn, and never the word "error".
func (m *model) operatorRefuseSmallBand() {
	ctx, est := m.operatorChannelCtx()
	m.agentLines = append(m.agentLines,
		stRed.Render("✕ ")+stEmber.Render("this band is too small for a guest - the window is "+operatorWindowLabel(ctx, est)+", a coding agent needs 16k+"),
		stDim.Render("· ")+stDim.Render("re-tune to a larger band: press ")+stKey.Render("[1]")+stDim.Render(" to work the dial, then hand off again with ")+stKey.Render("/operator"))
}

// --- the handoff ----------------------------------------------------------------------

// startOperatorHandoff runs every desk-side precondition (setup, channel up, DJ idle,
// the agent-ready band gate) and opens the PRE-LAUNCH PLATE (Phase 3). Staging - and
// with it any scratch config, budget change, or spend - begins only when the plate is
// accepted with an explicit local y.
func (m model) startOperatorHandoff(d operator.Detection, fromPicker bool) (tea.Model, tea.Cmd) {
	// Installed-but-not-configured (reserved for the future claude row): a setup note on
	// EVERY path to the desk - never a plate, never an exec. THE one NeedsSetup gate -
	// it covers the picker's enter AND the /operator <name> direct-jump (iteration-1
	// finding #5: the direct-jump used to skip the picker's copy of this check).
	if d.Guest.NeedsSetup {
		note := d.Guest.SetupNote
		if note == "" {
			note = d.Guest.Name + " needs setup before it can take the mic"
		}
		m.rcNote(note)
		return m, nil
	}
	// No band tuned: a disconnected proxy REFUSES to spend (Phase 1 ruling 5), so a launch
	// now would only hand the guest a wall of 502s. Rather than dead-end, try a SILENT
	// auto-tune to a FREE band first (R1 - never a paid auto-spend); land the plate on
	// success, a SINGLE honest refusal on failure.
	if m.proxyHolder == nil || !m.proxyHolder.Connected() {
		pick := pickAutoBand(m.bands, m.loggedInState())
		if pick == nil || !pick.free || pick.cheapest == nil {
			m.agentLines = append(m.agentLines,
				stRed.Render("✕ ")+stEmber.Render("no channel to patch into - a guest runs on your open channel"),
				stDim.Render("· ")+stDim.Render("tune in first: press ")+stKey.Render("[1]")+stDim.Render(", ⏎ on a band opens the channel · then come back with ")+stKey.Render("[0]"))
			return m, nil
		}
		o := *pick.cheapest
		if _, err := m.bindChannel(o); err != nil {
			// The local endpoint failed to bind: refuse rather than open a plate over an
			// unbound channel that would hand the guest a wall of 502s.
			m.agentLines = append(m.agentLines,
				stRed.Render("✕ ")+stEmber.Render("could not open a channel: "+err.Error()),
				stDim.Render("· ")+stDim.Render("tune in manually with ")+stKey.Render("[1]")+stDim.Render(", then hand off again with ")+stKey.Render("/operator"))
			return m, nil
		}
		m.noteOnce(stDim.Render("· ") + stDim.Render("auto-tuned to ") + stKey.Render(o.Model) + stDim.Render(" (free) for the handoff"))
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
	// The agent-ready band gate (§6): a known window under the 16k floor is refused
	// BEFORE any plate or staging - never fail on prompt one.
	if m.operatorBandTooSmall() {
		m.operatorPicker = false
		m.operatorRows = nil
		m.operatorRefuseSmallBand()
		return m, nil
	}
	m.operatorPicker = false
	m.operatorRows = nil
	m.operatorPlate = &operatorPlate{det: d, workdir: operatorWorkdir(), fromPicker: fromPicker}
	m.status = stDim.Render("hand-off check · y patches " + d.Guest.Name + " through · n keeps the DJ")
	return m, nil
}

// operatorWorkdirIsHome reports whether dir is EXACTLY the user's home directory,
// honoring the LIVE HOME env (ruling W1: the boundary is exactly $HOME - a child dir
// like ~/ai/proj single-confirms).
func operatorWorkdirIsHome(dir string) bool {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return false
	}
	return filepath.Clean(dir) == filepath.Clean(h)
}

// onOperatorPlateKey owns EVERY key while the pre-launch plate is up (the house
// confirm idiom, DENY default): y/enter accepts (twice on exactly-$HOME), n/esc
// cancels back to where the pick came from, b cycles the budget ladder, everything
// else is swallowed - a stray key never accepts, and mode keys never leak underneath.
func (m model) onOperatorPlateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.operatorPlate
	switch k.String() {
	case "y", "enter":
		if operatorWorkdirIsHome(p.workdir) && !p.homeGate {
			// The scariest default: a bare $HOME workdir. The first y opens the ember
			// second gate instead of patching (W1 double-y); only a second explicit y runs.
			p.homeGate = true
			return m, nil
		}
		return m.acceptOperatorPlate()
	case "n", "esc":
		m.operatorPlate = nil
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		if p.fromPicker {
			// Back to the picker, cursor restored onto the guest that was being considered.
			m.operatorPicker = true
			m.operatorRows = m.buildOperatorRows()
			m.operatorCursor = 0
			for i, r := range m.operatorRows {
				if r.label == p.det.Guest.Name {
					m.operatorCursor = i
					break
				}
			}
			return m, nil
		}
		m.rcNote("the DJ keeps the mic")
		return m, nil
	case "b":
		// Ruling B1: b cycles the preset ceilings $2 -> $5 -> $10 -> uncapped -> $2.
		// Display-only until y (B2 non-sticky: cancel discards the choice).
		if !p.homeGate {
			p.budgetIdx = (p.budgetIdx + 1) % len(operatorBudgetLadder)
		}
		return m, nil
	default:
		return m, nil // the plate is modal - deny stays the default
	}
}

// acceptOperatorPlate turns the confirmed plate into a staged handoff: ONE PATCHING
// YOU THROUGH paint, then the exec. The plate's budget and workdir ride the handoff;
// the holder is armed at exec time (nothing is spent if staging aborts).
func (m model) acceptOperatorPlate() (tea.Model, tea.Cmd) {
	p := m.operatorPlate
	m.operatorPlate = nil
	m.operatorHandoff = &operatorHandoff{det: p.det, budget: operatorBudgetLadder[p.budgetIdx], workdir: p.workdir}
	m.status = stDim.Render("patching you through to " + p.det.Guest.Name + "…")
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
		m.rcEmitDJBack()
		return m, nil
	}
	// Re-check the DJ-idle preconditions AT EXEC TIME (audit regression): the bridge
	// parks only now, so a turn injected during the staging beat would otherwise run -
	// and bill - under the suspended TUI, into the guest's freshly reset accumulator.
	// The mode check covers global keys (ctrl+c quit-confirm, alt+m, a preset) pulling
	// the TUI off AGENT mid-staging - never exec the guest under another modal.
	// Every abort below rcEmitDJBack()s: the staging guard answered remote turns with
	// "guest has the mic", so an abort must correct the record or the remote surface is
	// stranded on a guest that never took the mic (iteration-1 finding #4).
	if m.mode != modeAgent || m.agentBusy || (m.agent != nil && m.agent.running.Load()) || len(m.agentQueued) > 0 {
		m.operatorHandoff = nil
		switch {
		case m.mode != modeAgent:
			m.rcNote("handoff aborted - you left the desk mid-patch · /operator from AGENT to try again")
		case len(m.agentQueued) > 0:
			// A turn is WAITING in the queue, not one the DJ picked up - say so honestly.
			m.rcNote("handoff aborted - a queued turn is waiting · /operator once the desk is clear")
		default:
			m.rcNote("handoff aborted - the DJ picked up a turn while patching · /operator again once it finishes")
		}
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		m.rcEmitDJBack()
		return m, nil
	}
	// Re-check the CHANNEL at exec time too (iteration-1 finding #3): the desk gate ran
	// before the 450ms staging beat, and a band drop inside it would launch the guest
	// into a wall of 502/503s (a disconnected proxy refuses to spend - Phase 1 ruling 5).
	if !m.proxyHolder.Connected() {
		m.operatorHandoff = nil
		m.agentLines = append(m.agentLines,
			stRed.Render("✕ ")+stEmber.Render("the channel dropped while patching - no band to carry the guest"),
			stDim.Render("· ")+stDim.Render("tune back in: press ")+stKey.Render("[1]")+stDim.Render(", ⏎ on a band opens the channel · then /operator again"))
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		m.rcEmitDJBack()
		return m, nil
	}
	// Agent-ready gate re-check AT EXEC TIME (the Phase 1 live-options discipline): a
	// re-tune during the staging beat can put a too-small station on the channel; the
	// exec is aborted with the honest reason instead of failing on prompt one.
	if m.operatorBandTooSmall() {
		ctx, est := m.operatorChannelCtx()
		m.operatorHandoff = nil
		m.agentLines = append(m.agentLines,
			stRed.Render("✕ ")+stEmber.Render("the band changed under the patch - the channel window is now "+operatorWindowLabel(ctx, est)+", too small for a guest (needs 16k+)"))
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		m.rcEmitDJBack() // an abort branch like every other - never strand "guest has the mic"
		return m, nil
	}
	// LIVE options at exec time - never the options frozen at first bind. The workdir is
	// the one the user confirmed on the plate.
	opts := m.proxyHolder.Get()
	wd := h.workdir
	if wd == "" {
		wd = operatorWorkdir() // defensive: a handoff always carries the plate's workdir
	}
	sess := operator.Session{
		BaseURL: m.endpoint, SessionKey: opts.SessionKey, Model: opts.Model,
		Workdir: wd, ScratchRoot: operatorScratchRoot,
	}
	launch, cleanup, err := operator.Materialize(h.det.Guest, sess)
	if err != nil {
		m.operatorHandoff = nil
		m.agentLines = append(m.agentLines, stRed.Render("✕ ")+stEmber.Render("couldn't hand the mic off: "+err.Error()))
		m.status = stDim.Render("back at the desk · the DJ is standing by")
		m.rcEmitDJBack()
		return m, nil
	}
	// Fresh money state per handoff (ruling 4): the PLATE-ARMED ceiling (the $2 default
	// unless b raised it; 0 = uncapped, ruling B1), zero spend, zero calls - the summary
	// and the 402 ceiling are THIS guest's numbers. The bearer key is NOT rotated (a
	// re-tune mid-session keeps the guest's config working).
	m.proxyHolder.SetBudget(h.budget)
	m.proxyHolder.ResetSpend()
	m.proxyHolder.ResetCalls()
	h.launch, h.cleanup, h.start, h.execing = launch, cleanup, time.Now(), true
	// BASE STATION interlock: announce the handoff, then PARK the bridge BEFORE the exec
	// cmd is returned - inbound remote turns are dropped at the bridge with a status
	// auto-frame (never queued, never replayed), backfill is answered from this snapshot.
	if m.rcBridge != nil {
		// Enrichment from the LIVE holder (rc_enrichment.feature): the exec-time model and
		// the freshly-reset spend ($0 - ResetSpend just ran); the bridge keeps the live
		// Spent reader so parked auto-frames report the guest's spend so far at emit time.
		m.rcEmit(client.OperatorStatusFrame(h.det.Guest.Name, opts.Model, m.proxyHolder.Spent()))
		m.rcBridge.Park(h.det.Guest.Name, m.agentTranscriptText(), opts.Model, m.proxyHolder.Spent)
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
		m.rcEmitDJBack()
	}
	guest := h.det.Guest.Name
	// The defensive reset just wrote ESC[?2004l AFTER bubbletea's RestoreTerminal had
	// re-enabled paste, so bracketed paste must be re-armed here or it stays dead for
	// the rest of the radio session (iteration-1 finding #2). The radio always runs
	// with paste on - unconditional, unlike the m.mouseOff-gated mouse restore.
	cmds := []tea.Cmd{fetchBalance(m.broker, m.user), tea.EnableBracketedPaste}
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
	// detail carries its OWN leading separator (the doc §3d mock varies them: a "  " gap on
	// mic-to/on-band, a " - " on wire) so each row reads exactly like the approved mockup.
	step := func(label, val, detail string) {
		line := "  " + stRed.Render(glyphOnAir) + " " + stDim.Render(pad(label, 8)) + stKey.Render(val)
		if detail != "" {
			line += stDim.Render(detail)
		}
		b.WriteString(truncVisibleTail(line, w) + "\n")
	}
	// PER-BRAND PLATE: each operator's wordmark rides the data-only Guest.Brand registry
	// field; nil falls back to the text-only house style (the name on the mic-to line below).
	if brand := operatorBrandBlock(h.det.Guest, w); brand != "" {
		b.WriteString(brand + "\n")
	}
	step("mic to", h.det.Guest.Name, "  (guest operator)")
	// on band: name the station (via @<node>) and keep the "·" separators (doc §3d).
	onBand := "  "
	if m.connected != nil && m.connected.NodeID != "" {
		onBand += "via @" + m.connected.NodeID + " · "
	}
	onBand += "your open channel · usual relay pricing"
	step("on band", mdl, onBand)
	step("wire", "config generated in a scratch dir", " - your own setup is untouched")
	row := func(label, value string) string {
		return "      " + stDim.Render(pad(label, 9)) + stKey.Render(value)
	}
	b.WriteString("\n" + truncVisibleTail(row("BASE URL", m.endpoint), w) + "\n")
	b.WriteString(truncVisibleTail(row("MODEL", mdl), w) + "\n\n")
	b.WriteString(truncVisibleTail("  "+stDim.Render("the radio steps aside while the guest is on the mic · exit the guest to come back"), w) + "\n")
	return b.String()
}

// --- the pre-launch plate (Phase 3, design doc §6) --------------------------------------

// operatorPlateView renders the ONE confirm plate between picking a guest and PATCHING
// YOU THROUGH. Every figure comes from its real source (detection / live proxy options /
// the open channel's station offer / the fetched balance) - never fabricated. The same
// accept/deny idiom as the TUNE IN cost confirm: [ enter / y ] accepts, [ esc / n ]
// denies, DENY is the default. NO_COLOR / narrow safe (shared styles + per-line clamp).
func (m model) operatorPlateView(w int) string {
	p := m.operatorPlate
	if p == nil {
		return ""
	}
	guest := p.det.Guest.Name
	mdl := ""
	if m.proxyHolder != nil {
		mdl = m.proxyHolder.Get().Model
	}
	var b strings.Builder
	b.WriteString("\n" + truncVisible("  "+stSelBar.Render("▌")+" "+stBrand.Render("HAND-OFF CHECK")+stDim.Render(" · confirm before "+guest+" takes the mic"), w) + "\n")
	row := func(label, val, detail string) {
		line := "  " + stRed.Render(glyphOnAir) + " " + stDim.Render(pad(label, 9)) + stKey.Render(val)
		if detail != "" {
			line += stDim.Render("  " + detail)
		}
		// graceful clip (#6): a narrow terminal ends a cut row in "…", never a mid-word hard cut.
		b.WriteString(truncVisibleTail(line, w) + "\n")
	}
	warn := func(s string) {
		b.WriteString(truncVisibleTail("  "+stEmber.Render("! ")+stEmber.Render(s), w) + "\n")
	}
	// guest - the Detection (name + probed version).
	gv := guest
	if p.det.Version != "" {
		gv += " " + p.det.Version
	}
	row("guest", gv, "takes the mic on your open channel")
	// band - the live proxy options model + the open channel's station callsign.
	bandDetail := ""
	if m.connected != nil && m.connected.NodeID != "" {
		bandDetail = "via @" + m.connected.NodeID
	}
	row("band", mdl, bandDetail)
	// t/s · ctx · price · tier - the open channel's station offer (~ = estimated; the
	// tier reads through the shared canonical pricetier renderer).
	if o := m.connected; o != nil {
		sig := fmt.Sprintf("%.0f t/s", o.TPS) + " · ctx " + operatorCtxLabel(o.Ctx, o.CtxEstimated) +
			" · " + dollars(o.PriceIn) + "·" + dollars(o.PriceOut) + " /1M"
		tier := ""
		if bars, chip := pricetier.Render(o.PriceTier, o.PriceOut); bars != "" && bars != "FREE" {
			tier = bars
			if chip != "" {
				tier += " " + chip
			}
		}
		row("signal", sig, tier)
	}
	// balance - the fetched figure; unknown renders an honest dim "-", never $0.00.
	if m.haveBal {
		row("balance", dollars(m.balance), "")
	} else {
		b.WriteString(truncVisibleTail("  "+stRed.Render(glyphOnAir)+" "+stDim.Render(pad("balance", 9))+stDim.Render("-"), w) + "\n")
	}
	// budget - the plate-cycled ceiling (ruling B1); "no ceiling" is impossible to miss.
	bv := operatorBudgetLadder[p.budgetIdx]
	row("budget", "session budget "+operatorBudgetLabel(bv), "b raises the ceiling")
	if bv <= 0 {
		warn("no ceiling - the guest can spend your whole balance")
	} else if m.haveBal && bv > m.balance {
		warn("this ceiling is above your balance (" + dollars(m.balance) + ")")
	}
	// workdir - the resolved absolute directory the guest reads and writes in.
	row("workdir", p.workdir, "")
	if operatorWorkdirIsHome(p.workdir) {
		warn("the workdir is your home directory - accepting asks twice")
	}
	// Honesty warns: unknown window (G2), the missing tool-call signal (G1 - unknown on
	// every band today), an unproven guest version, aider's pinned git safety.
	if ctx, _ := m.operatorChannelCtx(); ctx <= 0 {
		warn("context window unknown on this band - the guest may hit the wall mid-task")
	}
	warn("tool-call support unproven on this band - the guest may fall back to plain text")
	if p.det.Unverified {
		v := p.det.Version
		if v == "" {
			v = "unknown"
		}
		warn(guest + " version " + v + " is unproven at this desk - the wiring may have drifted")
	}
	if p.det.Guest.Name == "aider" {
		b.WriteString(truncVisibleTail("  "+stDim.Render("· ")+stDim.Render("aider runs with --no-auto-commits pinned - it never commits to your git on its own"), w) + "\n")
	}
	// The expectation line (ruling P1, exact copy): the guest runs on the BAND's model -
	// its brand never implies its vendor's quality.
	b.WriteString(truncVisibleTail("  "+stDim.Render("heads up · "+guest+" runs on "+mdl+" here - community band quality, not "+guest+"'s house models"), w) + "\n")
	// The y/N gate - or the ember $HOME second gate (W1) once the first y landed.
	if p.homeGate {
		b.WriteString("\n" + truncVisibleTail("  "+stEmber.Render("? ")+stEmber.Render("this is your whole home directory - hand "+guest+" the keys to all of it?"), w) + "\n")
		b.WriteString(truncVisibleTail("  "+stKey.Render("[ enter / y ]")+stDim.Render(" yes, work in "+p.workdir+"   ")+stKey.Render("[ esc / n ]")+stDim.Render(" back out   deny=default"), w) + "\n")
	} else {
		b.WriteString("\n" + truncVisibleTail("  "+stKey.Render("[ enter / y ]")+stDim.Render(" patch "+guest+" through   ")+stKey.Render("[ esc / n ]")+stDim.Render(" keep the DJ   ")+stKey.Render("b")+stDim.Render(" budget   deny=default"), w) + "\n")
	}
	return b.String()
}

// --- THE DESK on the AGENT landing (Phase 3, design doc §3a/§3f) -------------------------

// deskGuests returns the detections in DESK display order: registry order first, then
// any non-registry detections (the future claude row) in detection order.
func deskGuests(ds []operator.Detection) []operator.Detection {
	if len(ds) == 0 {
		return nil
	}
	out := make([]operator.Detection, 0, len(ds))
	used := make([]bool, len(ds))
	for _, g := range operator.Registry() {
		for i := range ds {
			if !used[i] && ds[i].Guest.Name == g.Name {
				out = append(out, ds[i])
				used[i] = true
			}
		}
	}
	for i := range ds {
		if !used[i] {
			out = append(out, ds[i])
		}
	}
	return out
}

// deskStripLine is the one-line reminder under the AGENT heading (§3a line 2):
//
//	◉ the DJ has the mic  ·  at the desk: opencode · aider  ·  /operator hands off
//
// It renders ONLY when >=1 guest is detected - the zero-guest screen stays byte-identical
// (the permanent regression) - and SURVIVES the transcript filling up (ruling S1): once
// the roster collapses, this line is what says /operator exists. Returns the exact
// inserted substring (one clamped line + newline), or "".
func (m model) deskStripLine(w int) string {
	ds := deskGuests(m.operatorDetections)
	if len(ds) == 0 {
		return ""
	}
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.Guest.Name
	}
	line := "  " + stRed.Render(glyphOnAir) + " " + stDim.Render("the DJ has the mic  ·  at the desk: ") +
		stKey.Render(strings.Join(names, " · ")) + stDim.Render("  ·  ") + stKey.Render("/operator") + stDim.Render(" hands off")
	return truncVisible(line, w) + "\n"
}

// deskCompactCount is the windowshade fold of the strip (§3f): the bare " · N at the
// desk" segment appended to the compact AGENT heading. "" with zero guests, so the
// compact heading too stays byte-identical.
func (m model) deskCompactCount() string {
	n := len(m.operatorDetections)
	if n == 0 {
		return ""
	}
	return stDim.Render(" · ") + stDim.Render(fmt.Sprintf("%d at the desk", n))
}

// deskRosterBlock is the LANDING wrapper for THE DESK (§3a): it gates on the landing
// state (empty transcript, no turn running, no modal up, full view) and then renders the
// roster via deskRosterView. When the AGENT lands with nothing tuned in the desk is
// FOCUSED and selectable (the [0] redesign, R3: deskFocused); when a band is already
// tuned it stays the STATIC PREVIEW it always was (no carat, no marquee) - the desk_view
// bytes are unchanged. Returns the inserted substring (clamped lines), or "".
func (m model) deskRosterBlock(w int) string {
	ds := deskGuests(m.operatorDetections)
	if len(ds) == 0 || m.compact {
		return "" // the zero-guest byte-identical invariant: no guests, no desk chrome
	}
	if len(m.agentLines) != m.agentLandingLines || m.agentBusy || (m.agent != nil && m.agent.running.Load()) {
		return "" // any line beyond the entry chrome = the conversation started
	}
	if m.operatorPicker || m.agentPicker || m.agentPendingConfirm != nil || m.operatorPlate != nil || m.operatorHandoff != nil {
		return ""
	}
	return m.deskRosterView(w, m.deskCursor, m.deskFocused)
}

// deskRosterView renders THE DESK roster: the header, the SELECTED operator's marquee
// plate (focused only, R2 - the one hue), and the operator rows. When focused the cursor
// row carries a red carat; the row bodies stay mono+red (R2). The SAME renderer (via the
// marquee) feeds the /operator picker, so the modal gets the marquee too.
func (m model) deskRosterView(w, cursor int, focused bool) string {
	ds := deskGuests(m.operatorDetections)
	mdl := ""
	if m.proxyHolder != nil && m.proxyHolder.Connected() {
		mdl = m.proxyHolder.Get().Model
	}
	sub := "who can take the mic"
	if mdl != "" {
		sub += " on " + mdl
	}
	var b strings.Builder
	b.WriteString("\n" + truncVisible("  "+stSelBar.Render("▌")+" "+stBrand.Render("THE DESK")+"    "+stDim.Render(sub), w) + "\n")
	// R2: the selected operator's plate as a marquee, in its ONE canonical hue. Focused
	// only - the static preview stays byte-identical (no marquee, no carat).
	if focused {
		b.WriteString(m.deskMarquee(w, cursor))
	}
	b.WriteString(truncVisible("    "+stDim.Render(pad("operator", 13)+pad("wire", 11)+"status"), w) + "\n")
	// The resident DJ row is always first (index 0), with the red on-air mark.
	b.WriteString(truncVisible(deskGutter(focused && cursor == 0)+stRed.Render(glyphOnAir)+" "+stKey.Render(pad("DJ", 12))+" "+stDim.Render(pad("in the TUI", 10)+" resident · dj.md persona · read/list auto, write/run confirm"), w) + "\n")
	for i, d := range ds {
		status := "guest · on PATH · patches into your open channel"
		if d.Guest.NeedsSetup {
			status = "guest · needs a key first - /operator " + d.Guest.Name + " shows how"
		} else if d.Unverified {
			v := d.Version
			if v == "" {
				v = "unknown"
			}
			status += " · version " + v + " unproven"
		}
		b.WriteString(truncVisible(deskGutter(focused && cursor == i+1)+"  "+stKey.Render(pad(d.Guest.Name, 12))+" "+stDim.Render(pad("hands off", 10)+" "+status), w) + "\n")
	}
	// At most ONE dim not-installed suggestion, at the bottom, only while the desk is
	// sparse - a healthy desk advertises nothing (the buildOperatorRows rule). Never
	// selectable (the cursor never lands on it).
	if len(ds) < 2 {
		seen := map[string]bool{}
		for _, d := range ds {
			seen[d.Guest.Name] = true
		}
		for _, g := range operator.Registry() {
			if !seen[g.Name] {
				b.WriteString(truncVisible("    "+stDim.Render(pad(g.Name, 12)+" "+pad("-", 10)+" not at the desk · get it: "+g.InstallHint), w) + "\n")
				break
			}
		}
	}
	return b.String()
}

// deskGutter is the 2-cell row gutter: a red carat on the selected (focused) row, two
// spaces otherwise - so the un-focused static preview keeps its exact leading spacing.
func deskGutter(selected bool) string {
	if selected {
		return stSelText.Render("▸ ")
	}
	return "  "
}

// deskMarquee renders the SELECTED operator's brand plate (R2): the DJ house plate at
// cursor 0, else the detected guest's shipped plate (or a plain name lockup when a guest
// ships none). One hue per the plate; the list rows below stay mono+red.
func (m model) deskMarquee(w, cursor int) string {
	ds := deskGuests(m.operatorDetections)
	if cursor <= 0 {
		return operatorBrandArtBlock(djBrandArt(), w)
	}
	if idx := cursor - 1; idx >= 0 && idx < len(ds) {
		return deskMarqueeForGuest(ds[idx].Guest, w)
	}
	return ""
}

// deskMarqueeForGuest renders one guest's marquee plate: its shipped BrandArt when it
// has one, else a plain (mono) name lockup so the marquee is never blank.
func deskMarqueeForGuest(g operator.Guest, w int) string {
	if g.Brand != nil {
		return operatorBrandArtBlock(*g.Brand, w)
	}
	return truncVisible("  "+stBrand.Render(g.Name), w) + "\n"
}

// djBrandArt is the resident DJ's house plate: a TUI-side mono+red ROGER·AI · DJ lockup
// built from the corner-Ping operator pose (the ((•)) beacon + the R body). It lives
// here, NOT in internal/operator/brand.go, which stays guests-only (data-free of the
// house). One red beat on the beacon dot + the DJ tag; the wordmark in house brand ink.
func djBrandArt() operator.BrandArt {
	red := operator.BrandInk{Token: operator.InkRedBold}
	brand := operator.BrandInk{Token: operator.InkBrand}
	dim := operator.BrandInk{Token: operator.InkDim}
	return operator.BrandArt{
		Rows: []operator.BrandRow{
			{Text: "((•))", Spans: []operator.BrandSpan{{From: 2, To: 3, Ink: red}}},
			{Text: " \\(R)/   ROGER·AI · DJ", Spans: []operator.BrandSpan{
				{From: 3, To: 4, Ink: red},    // the R body
				{From: 9, To: 17, Ink: brand}, // ROGER·AI
				{From: 20, To: 22, Ink: red},  // DJ
			}},
			{Text: " ╰───╯   resident · the house agent · dj.md", Ink: dim},
		},
		Width:  43,
		Lockup: operator.BrandRow{Text: "ROGER·AI · DJ", Spans: []operator.BrandSpan{{From: 0, To: 8, Ink: brand}, {From: 11, To: 13, Ink: red}}},
	}
}
