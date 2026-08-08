package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v5/internal/detect"
)

// LOCAL MODELS IN /model. Founder ask 2026-08-07: run the TUI agent on your own models
// without sharing them. A private band is NOT this (it still registers with the broker);
// these rows route straight to the local server via harness.LocalCompleter.
// Detection must be BACKGROUND: detect probes ~12 ports at 1.5s each, and /model is fast
// today precisely because it reads only in-memory state.

func agentWithLocals(t *testing.T) model {
	t.Helper()
	m := browseSeed(100)
	m.bands = []band{
		{model: "grok-4.3", online: true, cheapest: &offer{Model: "grok-4.3", Ctx: 128000}},
	}
	m.agent = m.newAgentRuntime()
	m.localFound = []detect.Found{{
		Name: "llama.cpp", BaseURL: "http://127.0.0.1:8080/v1",
		Chat:   "http://127.0.0.1:8080/v1/chat/completions",
		Models: []string{"qwen3-vl-8b"},
		Ctx:    map[string]int{"qwen3-vl-8b": 32768},
	}}
	return m
}

// Opening the picker must not block on a port scan.
func TestModelPickerDoesNotScanOnOpen(t *testing.T) {
	m := agentWithLocals(t)
	scanned := false
	prev := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) { scanned = true; return nil, nil }
	t.Cleanup(func() { detectShares = prev })

	_, _ = m.openAgentModelPicker()
	if scanned {
		t.Error("opening /model ran a port scan - the picker must read memory only")
	}
}

// Local models appear as their own rows, labelled, alongside the broker bands.
func TestPickerListsLocalModelsLabelled(t *testing.T) {
	m0 := agentWithLocals(t)
	pm, _ := m0.openAgentModelPicker()
	m := asModel(pm)

	var local *agentPickerRow
	for i := range m.agentPickerRows {
		if m.agentPickerRows[i].model == "qwen3-vl-8b" {
			local = &m.agentPickerRows[i]
		}
	}
	if local == nil {
		t.Fatalf("the local model is missing from the picker: %+v", m.agentPickerRows)
	}
	if !local.local {
		t.Error("the local row must be marked local")
	}
	if local.chat != "http://127.0.0.1:8080/v1/chat/completions" {
		t.Errorf("the row must carry the local chat URL, got %q", local.chat)
	}
	if local.ctx != 32768 {
		t.Errorf("the row must carry the local context window, got %d", local.ctx)
	}

	// The broker band is still there and is NOT marked local.
	for _, r := range m.agentPickerRows {
		if r.model == "grok-4.3" && r.local {
			t.Error("a broker band must not be marked local")
		}
	}

	// The rendered picker must say which is which, and must not price a local model.
	m.mode, m.agentPicker = modeAgent, true
	out := stripANSI(m.View())
	if !strings.Contains(strings.ToUpper(out), "LOCAL") {
		t.Errorf("the picker must label local rows, got:\n%s", out)
	}
	if strings.Contains(out, "$") && strings.Contains(out, "qwen3-vl-8b") {
		// A local model has no price; showing one would be a false claim about money.
		line := ""
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "qwen3-vl-8b") {
				line = l
			}
		}
		if strings.Contains(line, "$") {
			t.Errorf("a local model must not be priced: %q", line)
		}
	}
}

// A local model that is not a CHAT model (a TTS/STT voice) cannot run an agent loop.
func TestPickerSkipsLocalVoiceModels(t *testing.T) {
	m := agentWithLocals(t)
	m.localFound[0].Models = []string{"qwen3-vl-8b", "kokoro-tts", "whisper-1"}
	m.localFound[0].Modality = map[string]string{"kokoro-tts": "tts", "whisper-1": "stt"}
	pm, _ := m.openAgentModelPicker()

	for _, r := range asModel(pm).agentPickerRows {
		if r.model == "kokoro-tts" || r.model == "whisper-1" {
			t.Errorf("a voice model must not be offered to the agent: %q", r.model)
		}
	}
}

// Picking a local model must route the turn to the LOCAL server, never the broker.
func TestPickingLocalRoutesAwayFromTheBroker(t *testing.T) {
	m0 := agentWithLocals(t)
	pm, _ := m0.openAgentModelPicker()
	m := asModel(pm)

	m.pickAgentModel("qwen3-vl-8b")
	if m.agent.localChat != "http://127.0.0.1:8080/v1/chat/completions" {
		t.Fatalf("the runtime did not bind the local endpoint, got %q", m.agent.localChat)
	}
	// The tool budget must follow the LOCAL model's window too.
	if m.agent.loop.MaxToolOutput == 0 {
		t.Error("a local pick must still size the tool-output budget")
	}

	// Switching back to a broker band must clear the local binding, or turns would keep
	// going to the local server under a broker model's name.
	m.pickAgentModel("grok-4.3")
	if m.agent.localChat != "" {
		t.Errorf("switching back to a broker band left a local endpoint bound: %q", m.agent.localChat)
	}
}

// A background scan landing while the picker is open folds its rows in without disturbing
// the operator's cursor position.
func TestLocalScanFoldsInWhilePickerOpen(t *testing.T) {
	m0 := agentWithLocals(t)
	m0.localFound = nil
	// Two broker bands, so the picker OPENS with no locals yet (one candidate would
	// auto-select instead of opening, and there would be no open picker to fold into).
	m0.bands = append(m0.bands, band{model: "foundation", online: true,
		cheapest: &offer{Model: "foundation", Ctx: 8192}})
	pm, _ := m0.openAgentModelPicker()
	m := asModel(pm)
	before := len(m.agentPickerRows)

	var tm tea.Model = m
	tm, _ = tm.Update(localModelsMsg{found: []detect.Found{{
		Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1",
		Chat: "http://127.0.0.1:11434/v1/chat/completions", Models: []string{"llama-local"},
	}}})
	gm := asModel(tm)
	if len(gm.agentPickerRows) <= before {
		t.Errorf("a landing scan did not add rows (%d -> %d)", before, len(gm.agentPickerRows))
	}
	if gm.agentPickerCursor < 0 || gm.agentPickerCursor >= len(gm.agentPickerRows) {
		t.Errorf("the cursor left the row range after a scan landed: %d", gm.agentPickerCursor)
	}
}

// PICK LOCAL, THEN TUNE A CHANNEL: the local endpoint must let go.
//
// Found by the pre-push audit. pickAgentModel calls bindAgentEndpoint, so pick->pick was
// safe and TestPickingLocalRoutesAwayFromTheBroker covered it. refreshAgentModel - the path
// taken when a channel is tuned on top of an explicit pick - reassigned agent.model and
// called only applyToolBudget. The local binding survived, so every turn kept going to
// 127.0.0.1 while the UI, the transcript and the receipts all named the broker band.
//
// That is precisely the invariant agentRuntime documents at agent.go:48-56: localChat is
// "CLEARED whenever a broker band is picked, so a turn can never silently keep going to a
// local server under a broker model's name". It held for one of the two paths.
func TestTuningAChannelOverALocalPickReleasesTheLocalEndpoint(t *testing.T) {
	m0 := agentWithLocals(t)
	pm, _ := m0.openAgentModelPicker()
	m := asModel(pm)

	m.pickAgentModel("qwen3-vl-8b")
	if m.agent.localChat == "" {
		t.Fatal("precondition: the local pick must bind a local endpoint")
	}

	// Now a channel is tuned on top of the pick - a different one, so refreshAgentModel
	// follows it rather than letting the pick win.
	m.connected = &offer{NodeID: "roggentoo", Model: "grok-4.3", Online: true}
	m.refreshAgentModel()

	if m.agent.model != "grok-4.3" {
		t.Fatalf("the agent should have followed the new channel, got %q", m.agent.model)
	}
	if m.agent.localChat != "" || m.agent.localKey != "" {
		t.Errorf("tuning a broker band left the local endpoint bound (chat=%q key=%q) - "+
			"turns would go to the local server under the band's name",
			m.agent.localChat, m.agent.localKey)
	}
}
