package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v5/internal/agent"
)

// BAND MANAGEMENT (BASE STATION [p]). Bands were rendered here but not selectable, and
// nothing in the product could revoke or move one - while the broker's own quota error
// told operators to "revoke an existing band first". This file closes that loop.
//
// The two actions a band supports, and why only these two:
//   MOVE   repoints the band at another model, KEEPING the frequency code, so everyone
//          already tuned in keeps working. This is the action that makes a band a durable
//          identity rather than a side effect of whichever model happened to mint it.
//   REVOKE burns the code forever and frees the quota slot. Irreversible, so it is always
//          behind an explicit confirm that names what it breaks.
//
// There is deliberately NO "show the code again": the code is never stored (only its
// hash), so offering to reveal it would be a promise the system cannot keep. The remedy
// for a lost code is revoke + go private again, which mints a new one.
// Spec: features/sharing/band_management.feature.

// bandCursorIndex maps the shared BASE STATION cursor onto the bands list: the cursor runs
// over sessions first, then bands. It returns -1 when the cursor is on a session row (or
// nothing), so a session can never be mistaken for a band.
func (m model) bandCursorIndex() int {
	i := m.rcCursor - len(m.rcSessions)
	if i < 0 || i >= len(m.rcBands) {
		return -1
	}
	return i
}

// bandWhere renders WHERE a band lives: the node id verbatim, "<station>-<model>".
//
// It deliberately does NOT try to split the station from the model. A station callsign is
// usually three words ("eager-puma-54") but can be anything an operator chose - the
// founder's own is the single word "roggentoo" - so any split is a guess, and a wrong
// guess silently renames someone's model in the one place they look to identify it.
// Printing the id whole is always correct and still says both things at once.
func bandWhere(bd BandRow) string {
	if bd.NodeID == "" {
		return "(not bound to a model)"
	}
	return "on " + bd.NodeID
}

// bandName is the label column. Band.Label has never had a write path, so it is empty in
// practice; fall back to the band id rather than leaving the column blank.
func bandName(bd BandRow) string {
	if strings.TrimSpace(bd.Label) != "" {
		return bd.Label
	}
	return bd.ID
}

// bandQuotaHint is the one-line remedy shown when the broker refuses a mint over the free
// quota. It points at the surface that can actually fix it. It deliberately never mentions
// buying more bands: no purchase path exists, and inventing one in an error would be a lie.
func bandQuotaHint() string {
	return "manage your bands in BASE STATION [p] - move one to this model to keep its code"
}

func (m model) openBandManage(bd BandRow) model {
	m.mode = modeBandManage
	m.bandManageID, m.bandManageDisp, m.bandManageNode = bd.ID, bd.Display, bd.NodeID
	m.bandMoveCursor = 0
	return m
}

func (m model) openBandRevokeConfirm(bd BandRow) model {
	m.mode = modeBandRevokeConfirm
	m.bandManageID, m.bandManageDisp, m.bandManageNode = bd.ID, bd.Display, bd.NodeID
	return m
}

// bandManageActive reports whether the band the card is acting on is still live. A revoked
// band cannot be moved (its code is burnt), so the card must not offer it.
func (m model) bandManageActive() bool {
	for _, bd := range m.rcBands {
		if bd.ID == m.bandManageID {
			return bd.Status == "active"
		}
	}
	return false
}

// bandMoveTargets are the models on THIS machine a band can be moved onto - the share rows.
// The destination need not be on air: the band binds when that model next goes private.
func (m model) bandMoveTargets() []string {
	out := make([]string, 0, len(m.shareRows))
	for _, r := range m.shareRows {
		out = append(out, r.model)
	}
	return out
}

func (m model) onBandManageKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "left":
		m.mode = modePrivate
		return m, nil
	case "m", "M":
		if !m.bandManageActive() {
			m.status = stEmber.Render("a revoked band cannot be moved - its code is burnt")
			return m, nil
		}
		if len(m.bandMoveTargets()) == 0 {
			m.status = stEmber.Render("no models detected on this machine to move the band to")
			return m, nil
		}
		m.mode = modeBandMove
		m.bandMoveCursor = 0
		return m, nil
	case "x", "X":
		m.mode = modeBandRevokeConfirm
		return m, nil
	}
	return m, nil
}

func (m model) onBandMoveKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	targets := m.bandMoveTargets()
	switch k.String() {
	case "esc", "q", "left":
		m.mode = modeBandManage
		return m, nil
	case "up", "k":
		if m.bandMoveCursor > 0 {
			m.bandMoveCursor--
		}
		return m, nil
	case "down", "j":
		if m.bandMoveCursor < len(targets)-1 {
			m.bandMoveCursor++
		}
		return m, nil
	case "enter":
		if m.bandMoveCursor < 0 || m.bandMoveCursor >= len(targets) {
			return m, nil
		}
		return m, m.moveBandTo(m.bandManageID, targets[m.bandMoveCursor])
	}
	return m, nil
}

func (m model) onBandRevokeConfirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "y", "Y":
		return m, m.revokeBand(m.bandManageID)
	// Anything else backs out: an irreversible action must never be a slip away.
	default:
		m.mode = modeBandManage
		return m, nil
	}
}

// moveBandTo repoints the band at a local model. The node id MUST be built with the same
// helper the share path uses, or the band would bind to an id no node ever registers.
func (m model) moveBandTo(bandID, model string) tea.Cmd {
	// The station comes from the CONTROLLER, the same source startLocked uses to build the
	// node id it registers. m.station merely mirrors it (syncShareCache), so reading the
	// controller directly removes any chance of moving a band onto an id no node will ever
	// register - which would strand the band silently.
	broker, move := m.broker, m.hooks.BandMove
	nodeID := agent.ShareNodeID(m.ctrl.Station(), model, 0)
	return func() tea.Msg {
		if move == nil {
			return bandActionMsg{err: "band management is unavailable in this build"}
		}
		if err := move(broker, bandID, nodeID); err != nil {
			return bandActionMsg{err: err.Error()}
		}
		return bandActionMsg{moved: true, model: model}
	}
}

func (m model) revokeBand(bandID string) tea.Cmd {
	broker, revoke := m.broker, m.hooks.BandRevoke
	return func() tea.Msg {
		if revoke == nil {
			return bandActionMsg{err: "band management is unavailable in this build"}
		}
		if err := revoke(broker, bandID); err != nil {
			return bandActionMsg{err: err.Error()}
		}
		return bandActionMsg{revoked: true}
	}
}

// bandActionMsg carries the outcome of a move/revoke back into the model.
type bandActionMsg struct {
	moved   bool
	revoked bool
	model   string
	err     string
}

func (m model) bandManageView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(truncVisibleTail("  "+s, w) + "\n") }
	b.WriteString("\n" + stHeadRule.Render(strings.Repeat("─", w)) + "\n")
	line(stKey.Render("BAND") + stDim.Render("  ") + stKey.Render(m.bandManageDisp))
	line(stDim.Render("on ") + stKey.Render(m.bandManageNode))
	b.WriteString("\n")
	if m.bandManageActive() {
		line(stKey.Render("m") + stDim.Render(" move it to another model ") +
			stDim.Render("- keeps this frequency code, nobody tuned in is cut off"))
	} else {
		line(stDim.Render("this band is revoked - its code is burnt and cannot be moved"))
	}
	line(stKey.Render("x") + stDim.Render(" revoke it ") + stDim.Render("- burns the code forever, frees your slot"))
	b.WriteString("\n")
	line(stDim.Render("the code itself was shown once and never stored - it cannot be shown again"))
	line(stDim.Render("esc returns"))
	return b.String()
}

func (m model) bandMoveView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(truncVisibleTail("  "+s, w) + "\n") }
	b.WriteString("\n" + stHeadRule.Render(strings.Repeat("─", w)) + "\n")
	line(stKey.Render("MOVE THE BAND") + stDim.Render("  ") + stDim.Render(m.bandManageDisp))
	line(stDim.Render("the frequency code stays the same - everyone tuned in keeps working"))
	b.WriteString("\n")
	for i, t := range m.bandMoveTargets() {
		if i == m.bandMoveCursor {
			line(stSelText.Render("▸ " + t))
			continue
		}
		line(stDim.Render("  " + t))
	}
	b.WriteString("\n")
	line(stDim.Render("↑↓ pick · ⏎ move · esc back"))
	return b.String()
}

func (m model) bandRevokeConfirmView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(truncVisibleTail("  "+s, w) + "\n") }
	b.WriteString("\n" + stHeadRule.Render(strings.Repeat("─", w)) + "\n")
	line(stRed.Render("REVOKE ") + stKey.Render(m.bandManageDisp) + stDim.Render("?"))
	b.WriteString("\n")
	line(stEmber.Render("this code stops working immediately and can never be revived."))
	line(stEmber.Render("everyone tuned in to it is cut off."))
	b.WriteString("\n")
	line(stDim.Render("to keep the code and just change the model, move it instead (esc, then ") +
		stKey.Render("m") + stDim.Render(")"))
	b.WriteString("\n")
	line(stKey.Render("y") + stDim.Render(" revoke · any other key cancels"))
	return b.String()
}

// ── THE QUOTA OFFER ──────────────────────────────────────────────────────────
// FOUNDER 2026-08-21: hitting the private-band limit on the SHARE screen produced a
// refusal and a signpost - "manage your bands in BASE STATION [p]" - and the operator
// wanted to be ASKED whether to put the band here instead. A dead end that names
// another screen is still a dead end; the fix is to offer the action where the refusal
// happens.
//
// The offer is unambiguous on the free plan, which allows exactly ONE band: there is no
// choosing which to move. On a plan with several this would need a picker, so the offer
// only appears when the list holds one - otherwise BASE STATION, which already has that
// picker, remains the right place.

// offerBandMove records that a quota refusal just happened for this model, so the share
// screen can take a single key and act on it.
func (m *model) offerBandMove(model string) { m.bandMoveOffer = model }

// bandQuotaOffer is what a quota refusal says: the ACTION, not a signpost. It names the
// key, the model, and the reason moving beats revoking - the code survives, so everyone
// already tuned in keeps working - and still points at BASE STATION for anything else.
func bandQuotaOffer(model string) string {
	return stDim.Render(" - press ") + stKey.Render("y") +
		stDim.Render(" to move your band to "+model+" (keeps its code), or ") +
		stKey.Render("p") + stDim.Render(" to manage bands")
}

// acceptBandMove fetches the operator's bands and moves the only one onto this model,
// keeping its frequency code - everyone already tuned in keeps working, which is the
// whole reason to move rather than revoke and re-mint.
func (m model) acceptBandMove() tea.Cmd {
	broker, list, move := m.broker, m.hooks.BandList, m.hooks.BandMove
	model := m.bandMoveOffer
	station := m.ctrl.Station()
	return func() tea.Msg {
		if list == nil || move == nil {
			return bandActionMsg{err: "band management is unavailable in this build"}
		}
		bands, err := list(broker)
		if err != nil {
			return bandActionMsg{err: err.Error()}
		}
		switch len(bands) {
		case 0:
			// The refusal said the quota was full, and the list says otherwise. Report
			// that honestly rather than inventing a band to move.
			return bandActionMsg{err: "no band to move - try going private again"}
		case 1:
			// The node id MUST come from the same helper the share path registers with,
			// or the band binds to an id no node ever announces and is silently stranded.
			if err := move(broker, bands[0].ID, agent.ShareNodeID(station, model, 0)); err != nil {
				return bandActionMsg{err: err.Error()}
			}
			return bandActionMsg{moved: true, model: model}
		default:
			return bandActionMsg{err: "you have several bands - pick one in BASE STATION [p]"}
		}
	}
}
