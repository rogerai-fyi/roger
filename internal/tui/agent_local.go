package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v5/internal/detect"
	"rogerai.fm/roger/v5/internal/protocol"
)

// LOCAL MODELS IN THE AGENT'S /model PICKER.
//
// FOUNDER ASK (2026-08-07): "use my own models on the TUI agent without having to share
// them". Before this, the only way to reach your own model from the agent was to put it ON
// AIR - register it with the broker and let every turn relay back to your own box. Even a
// PRIVATE band is not an offline mode: features/discovery/bands.feature is explicit that
// --private is a DISCOVERY choice, so it still registers, still binds to your account, and
// still obeys the global price ceiling.
//
// A local row therefore routes STRAIGHT to the local server (harness.LocalCompleter):
// nothing registers, nothing is metered, no wallet is touched, and the weights never leave
// the machine. That also means a local row must never show a price - there is none, and
// printing one would be a false claim about money.
//
// The rows come from internal/detect, the same discovery SHARE uses (ollama, llama.cpp,
// vLLM, LM Studio, +8 more, plus real listening-port enumeration). Detection runs in the
// BACKGROUND: it probes ~12 ports at 1.5s each, and /model is instant today precisely
// because opening it touches nothing but memory.

// agentPickerRow is one selectable model in the /model picker. Until local models existed
// the picker carried a bare model id and looked everything else up from the band list;
// a local model is on no band, so the row has to carry its own endpoint and window.
type agentPickerRow struct {
	model string
	local bool   // served from THIS machine - routed direct, never through the broker
	via   string // the local server's friendly name ("ollama", "llama.cpp"), local rows only
	chat  string // full local chat-completions URL (detect.Found.Chat), local rows only
	key   string // bearer for a key-protected local server, local rows only
	ctx   int    // context window when known (sizes the tool-output budget)
	// band marks a local row that is ALSO on one of your private bands right now, so the
	// picker can say so. It is read from the controller (m.sharePrivate), not from the
	// broker's /bands: local state is always known, so the badge is present every time the
	// picker opens rather than only after a roster fetch happened to land. A badge that
	// blinks in and out teaches the operator nothing.
	band bool
}

// localModelsMsg carries a finished background scan back into the model.
type localModelsMsg struct{ found []detect.Found }

// localModelsCmd scans for OpenAI-compatible servers on this machine, off the UI thread.
// Batched on entering the agent (never on picker-open), mirroring operatorScanCmd.
func localModelsCmd() tea.Cmd {
	return func() tea.Msg {
		found, _ := detectShares()
		return localModelsMsg{found: found}
	}
}

// localAgentRows turns the last scan into picker rows. Only CHAT models are offered: a
// TTS/STT model cannot run a tool-use loop, and listing one would be an invitation to a
// turn that can only fail. De-duplicated by model id, first server wins.
func (m model) localAgentRows() []agentPickerRow {
	var out []agentPickerRow
	seen := map[string]bool{}
	for _, f := range m.localFound {
		for _, mdl := range f.Models {
			if mdl == "" || seen[mdl] {
				continue
			}
			// Default (missing) modality is chat - detect only fills the map for models it
			// classified, so an unlabelled model is an ordinary chat model.
			if md := f.Modality[mdl]; md == protocol.ModalityTTS || md == protocol.ModalitySTT {
				continue
			}
			seen[mdl] = true
			out = append(out, agentPickerRow{
				model: mdl, local: true, via: f.Name,
				chat: f.Chat, key: f.Key, ctx: f.Ctx[mdl],
				band: m.sharePrivate[mdl],
			})
		}
	}
	return out
}

// onLocalModels folds a landed scan in. If the picker is open its rows are re-derived in
// place so the new models simply appear, with the cursor clamped so it can never point
// past the end of a list that just changed under the operator.
func (m model) onLocalModels(msg localModelsMsg) (tea.Model, tea.Cmd) {
	m.localFound = msg.found
	if m.agentPicker {
		m.agentPickerRows = m.agentPickerCandidates()
		if m.agentPickerCursor >= len(m.agentPickerRows) {
			m.agentPickerCursor = len(m.agentPickerRows) - 1
		}
		if m.agentPickerCursor < 0 {
			m.agentPickerCursor = 0
		}
	}
	return m, nil
}

// agentPickerCandidates is the full row set: the broker bands first (the marketplace is
// still the default), then this machine's own models under their own heading.
func (m model) agentPickerCandidates() []agentPickerRow {
	var out []agentPickerRow
	seen := map[string]bool{}
	for _, mdl := range m.agentModelCandidates() {
		seen[mdl] = true
		out = append(out, agentPickerRow{model: mdl, ctx: m.ctxForModel(mdl)})
	}
	for _, r := range m.localAgentRows() {
		if seen[r.model] {
			// A model with the same id is already on air through the broker. Keep the band
			// row: it is the one the operator has been using, and silently re-pointing it at
			// a local server would change where their turns go without saying so.
			continue
		}
		out = append(out, r)
	}
	return out
}

// ctxForModel is the band's reported context window for a broker model (0 when unknown).
func (m model) ctxForModel(mdl string) int {
	if b, ok := m.bandForModel(mdl); ok && b.cheapest != nil {
		return b.cheapest.Ctx
	}
	return 0
}

// rowForModel finds the picker row for a model id, so a pick can recover its endpoint.
func (m model) rowForModel(mdl string) (agentPickerRow, bool) {
	for _, r := range m.agentPickerRows {
		if r.model == mdl {
			return r, true
		}
	}
	for _, r := range m.localAgentRows() {
		if r.model == mdl {
			return r, true
		}
	}
	return agentPickerRow{}, false
}
