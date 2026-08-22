package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/protocol"
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
	// THE CONTROLLER FIRST. m.shareRows is what [2] SHARE detected and is what the node is
	// actually serving right now - it is authoritative, and it is already in memory.
	//
	// Deriving local models ONLY from m.localFound (the agent's own background port scan)
	// meant a model the operator had just put on air in SHARE was invisible to the AGENT
	// until a separate scan happened to land. The founder hit exactly that: they shared
	// grok-4.6 privately, chatted with it DIRECT from [1] TUNE IN, switched to [0] AGENT,
	// and got "no station is serving grok-4.6 (504)" - the agent had relayed to the broker
	// for a model sitting on the same machine.
	//
	// A row with no upstream is skipped: without an endpoint there is nothing to route to,
	// and offering it would trade a broker 504 for a local one.
	for _, r := range m.shareRows {
		if r.model == "" || seen[r.model] || r.upstream == "" {
			continue
		}
		if r.modality == protocol.ModalityTTS || r.modality == protocol.ModalitySTT {
			continue // a voice model cannot run a tool-use loop
		}
		seen[r.model] = true
		out = append(out, agentPickerRow{
			model: r.model, local: true, via: "this machine",
			chat: r.upstream, key: r.upstreamKey, ctx: r.ctx,
			band: m.sharePrivate[r.model],
		})
	}
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
	m.localScanning = false
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
		if seen[r.model] && !m.preferLocalFor(r.model) {
			// A model with the same id is already on air through the broker. Keep the band
			// row: it is the one the operator has been using, and silently re-pointing it at
			// a local server would change where their turns go without saying so.
			//
			// The exception is OUR OWN PRIVATE band (preferLocalFor): its only station is
			// this machine, so there is no other route to preserve - only a metered round
			// trip through the broker to reach ourselves.
			continue
		}
		if seen[r.model] {
			// Replace the band row in place, so the model appears ONCE and as the local
			// row it will actually use. Two rows for one model would be worse than either.
			for i := range out {
				if out[i].model == r.model {
					out[i] = r
					break
				}
			}
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

// preferLocalFor reports whether the AGENT should reach mdl DIRECTLY even though a broker
// band of the same name exists.
//
// The general rule stays as it was - keep the band row, because a public band may be served
// by other people's stations and silently re-pointing it would change where turns go. This
// is the one case where there is no such ambiguity: a model on OUR OWN PRIVATE band has
// exactly one station, and it is this machine. Relaying to the broker so it can route back
// here is a round trip to localhost that is metered on the way - and it needs a frequency
// code the operator may not even hold, which is precisely how it failed.
func (m model) preferLocalFor(mdl string) bool { return m.sharePrivate[mdl] }

// rowForModel finds the picker row for a model id, so a pick can recover its endpoint.
func (m model) rowForModel(mdl string) (agentPickerRow, bool) {
	// The three cases, in order, stated rather than fallen into. Taking whichever row came
	// first is what made bindAgentEndpoint find a BAND row for a model that lives on this
	// machine, leave localChat empty, and relay it through the broker.
	local, hasLocal := agentPickerRow{}, false
	for _, r := range m.localAgentRows() {
		if r.model == mdl {
			local, hasLocal = r, true
			break
		}
	}
	// 1. OUR OWN PRIVATE band: the only station is this machine, so local always wins.
	if hasLocal && m.preferLocalFor(mdl) {
		return local, true
	}
	// 2. Whatever the picker is showing (a band row, ordinarily).
	for _, r := range m.agentPickerRows {
		if r.model == mdl {
			return r, true
		}
	}
	// 3. A local row is the answer only when NO band carries this model. A public band CAN
	// be served by other people's stations, so answering with the local row here would
	// silently re-point the operator's turns at their own box - the exact thing the
	// keep-the-band-row rule exists to prevent, and it would do it invisibly because this
	// path runs whether or not the picker was ever opened.
	if hasLocal {
		if _, onBand := m.bandForModel(mdl); !onBand {
			return local, true
		}
	}
	return agentPickerRow{}, false
}

// agentFreqFor returns the tuned PRIVATE band's frequency code when THIS model is the one
// that band serves, and "" otherwise.
//
// The guard is the point. While a freq is tuned, m.bands holds ONLY that band's offers
// (freqResolvedMsg replaces the list), so "the model is in the current band list" is
// exactly "the model is served by the band we are tuned to". Sending the code for any
// other model would attach a private-band credential to a request that has nothing to do
// with it - a broadening of what the header authorises, for no benefit.
//
// A LOCAL row never reaches here: it is routed direct, and a direct call never touches the
// broker that the code is addressed to.
func (m model) agentFreqFor(mdl string) string {
	if m.tuneFreq == "" || mdl == "" {
		return ""
	}
	if _, ok := m.bandForModel(mdl); !ok {
		return ""
	}
	return m.tuneFreq
}
