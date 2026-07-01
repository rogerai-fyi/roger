package tui

// The windowshade canon: plain `m` toggles COMPACT on every non-text-entry nav screen
// (browse / SHARE / limits / help). The three voice views are nav screens with no text
// entry, so `m` must work there too — the 2026-07-01 launch audit caught it silently
// dead in all three (alt+m rescued it, but the canon is plain `m` everywhere).

import "testing"

// `m` in THE DJ BOOTH toggles compact and STAYS in the Booth.
func TestBoothPlainMTogglesWindowshade(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	if m.mode != modeVoiceBooth {
		t.Fatalf("precondition: expected the Booth, got %d", m.mode)
	}
	tm, _ = m.Update(keyMsg("m"))
	got := asModel(tm)
	if !got.compact {
		t.Fatalf("m in the Booth should enter compact (windowshade)")
	}
	if got.mode != modeVoiceBooth {
		t.Fatalf("m should stay in the Booth, got mode %d", got.mode)
	}
	tm, _ = got.Update(keyMsg("m"))
	if asModel(tm).compact {
		t.Fatalf("m again should expand back out of compact")
	}
}

// `m` in THE LISTENING POST toggles compact and stays put.
func TestListeningPostPlainMTogglesWindowshade(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	tm, _ = asModel(tm).Update(keyMsg("t"))
	m = asModel(tm)
	if m.mode != modeListeningPost {
		t.Fatalf("precondition: expected the Listening Post, got %d", m.mode)
	}
	tm, _ = m.Update(keyMsg("m"))
	got := asModel(tm)
	if !got.compact {
		t.Fatalf("m in the Listening Post should enter compact (windowshade)")
	}
	if got.mode != modeListeningPost {
		t.Fatalf("m should stay in the Listening Post, got mode %d", got.mode)
	}
}

// `m` in the VOICE PREVIEW toggles compact WITHOUT touching the money gate: the stage
// stays at confirm and nothing is spent (only y/enter may ever fire the synth).
func TestPreviewPlainMTogglesWindowshade(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := openVoicePreview(t, &m, "eager-puma-54-voice") // paid band -> confirm gate
	m = asModel(tm)
	if m.mode != modeVoicePreview || m.previewStage != previewConfirm {
		t.Fatalf("precondition: expected the paid confirm gate, got mode %d stage %d", m.mode, m.previewStage)
	}
	tm, _ = m.Update(keyMsg("m"))
	got := asModel(tm)
	if !got.compact {
		t.Fatalf("m in the preview should enter compact (windowshade)")
	}
	if got.mode != modeVoicePreview || got.previewStage != previewConfirm {
		t.Fatalf("m must NOT move the money gate: got mode %d stage %d", got.mode, got.previewStage)
	}
}
