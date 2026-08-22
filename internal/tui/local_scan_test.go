package tui

import (
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/detect"
)

// THE BACKGROUND SCAN MUST BE VISIBLE (founder 2026-08-22: "/model ... should list all my
// models locally which it does, but not at first ... i thought something was wrong but
// after trying 3-4 times my local list showed up").
//
// Detecting this machine's model servers probes ~12 ports and takes seconds, so it runs in
// the background. The bug was that its ABSENCE was indistinguishable from its RESULT: the
// picker showed only the broker bands and said nothing, so an operator who knew they had
// local models read it as the app being wrong.

func scanningAgent(t *testing.T) model {
	t.Helper()
	m := browseSeed(100)
	m.agent = m.newAgentRuntime()
	m.bands = []band{{model: "grok-4.6", online: true, cheapest: &offer{Model: "grok-4.6"}}}
	m.localScanning = true
	return m
}

// THE TRAP: with the scan in flight there is often exactly ONE candidate, and /model
// silently bound it and closed - so the command looked like it had done nothing, and the
// local models appeared only if the scan happened to land first.
func TestModelPickerOpensWhileTheScanIsStillRunning(t *testing.T) {
	m := scanningAgent(t)
	if got := len(m.agentPickerCandidates()); got != 1 {
		t.Fatalf("this lock needs exactly one candidate, got %d", got)
	}
	out, _ := m.openAgentModelPicker()
	gm := asModel(out)
	if !gm.agentPicker {
		t.Fatal("/model auto-bound the only candidate while the scan was still running - the set was known to be incomplete")
	}
	view := stripANSI(gm.agentView(100))
	if !strings.Contains(view, "still scanning") {
		t.Errorf("the picker must say the list is still growing:\n%s", view)
	}
}

// NEGATIVE HALF: once the scan has landed, one candidate is genuinely obvious and must
// still auto-select. Otherwise the fix trades a silent bind for a pointless prompt.
func TestOneCandidateStillAutoSelectsAfterTheScan(t *testing.T) {
	m := scanningAgent(t)
	m.localScanning = false
	out, _ := m.openAgentModelPicker()
	gm := asModel(out)
	if gm.agentPicker {
		t.Error("a settled single candidate opened a picker with nothing to choose")
	}
	if gm.agent.model != "grok-4.6" {
		t.Errorf("the only candidate was not bound: %q", gm.agent.model)
	}
}

// A LANDED SCAN clears the flag and folds its models in - and if the picker is open it
// updates in place, so the rows the operator is looking at become the real set.
func TestALandedScanClearsTheNoticeAndAddsItsModels(t *testing.T) {
	m := scanningAgent(t)
	out, _ := m.openAgentModelPicker()
	gm := asModel(out)

	landed, _ := gm.onLocalModels(localModelsMsg{found: []detect.Found{{
		Name: "ollama", Chat: "http://127.0.0.1:11434/v1/chat/completions",
		Models: []string{"qwen3-8b"},
	}}})
	lm := asModel(landed)

	if lm.localScanning {
		t.Error("a landed scan left the scanning notice up")
	}
	if !strings.Contains(stripANSI(lm.agentView(100)), "qwen3-8b") {
		t.Error("the picker did not pick up the models the scan found")
	}
	if strings.Contains(stripANSI(lm.agentView(100)), "still scanning") {
		t.Error("the notice survived the scan it was describing")
	}
}
