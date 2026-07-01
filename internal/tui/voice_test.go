package tui

// voice_test.go is the executable spec for the VOICE PREVIEW PANEL (a9749755's money-gated
// sample player, preserved). Per the founder DELTA, voice bands are EXCLUDED from the top-level
// list and surfaced/cued only from THE DJ BOOTH (see voicebooth_test.go for the Booth/footnote/
// child-screen behaviors); these tests drive the preview panel itself via startVoicePreview (the
// SAME entry the Booth "cue"/"spin" uses). The MONEY rule: a PAID tts preview spends real
// char-metered credits, so it REQUIRES an explicit confirm before any POST /v1/audio/speech; a
// FREE voice previews immediately. Selecting a voice NEVER opens a chat channel (the original
// "504 no station is serving <voice>" bug). Audio playback is routed through an INJECTABLE player
// so the synth+play path is fully testable, with a save-to-file fallback when no system player
// exists (never crash). No over-mocking: the broker is a real httptest server and the money/spend
// gate is asserted by whether the endpoint was actually hit.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// voiceSeed builds a BROWSE model carrying a chat band, a paid tts band, a free tts band, and
// an stt band, logged-in, so the grouping + preview flows can be driven.
func voiceSeed(t *testing.T, broker string) model {
	t.Helper()
	var m tea.Model = New(broker, "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "chat-1", Model: "gpt-oss-20b", Modality: "chat", PriceIn: 0.20, PriceOut: 0.30, Online: true, TPS: 60},
		{NodeID: "tts-paid", Model: "eager-puma-54-voice", Modality: "tts", PriceIn: 12.0, PriceOut: 0, Online: true},
		{NodeID: "tts-free", Model: "free-crier-voice", Modality: "tts", PriceIn: 0, PriceOut: 0, Online: true, FreeNow: true},
		{NodeID: "stt-1", Model: "whisper-listener", Modality: "stt", PriceIn: 5.0, PriceOut: 0, Online: true},
	})
	m, _ = m.Update(balanceMsg{balance: 42.17, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	return m.(model)
}

// bandByModel finds the grouped band with the given model in m.bands (voice bands are NOT in the
// top-level visible list any more — they live in THE DJ BOOTH — so tests resolve them here).
func bandByModel(t *testing.T, m *model, model string) band {
	t.Helper()
	for _, b := range m.bands {
		if b.model == model {
			return b
		}
	}
	t.Fatalf("band %q not found in m.bands", model)
	return band{}
}

// openVoicePreview enters the voice preview for the named voice band via startVoicePreview — the
// SAME entry the DJ BOOTH "cue"/"spin" uses (voice.go onVoiceBoothKey) and the connect() safety
// divert use. Tests exercising the preview PANEL (the money gate, the stt info, the off-air note)
// go through here so they are independent of how the list surfaces the band.
func openVoicePreview(t *testing.T, m *model, model string) (tea.Model, tea.Cmd) {
	t.Helper()
	return m.startVoicePreview(bandByModel(t, m, model))
}

// --- Grouping: groupBands tags modality; voice bands are excluded from the top-level list ------

func TestGroupBandsTagsModality(t *testing.T) {
	bands := groupBands([]offer{
		{Model: "gpt-oss-20b", Modality: "chat", Online: true},
		{Model: "a-voice", Modality: "tts", Online: true},
		{Model: "a-ear", Modality: "stt", Online: true},
		{Model: "legacy", Modality: "", Online: true}, // pre-voice: back-compat chat
	}, nil)
	got := map[string]string{}
	for _, b := range bands {
		got[b.model] = b.modality
	}
	for model, want := range map[string]string{"gpt-oss-20b": "chat", "a-voice": "tts", "a-ear": "stt", "legacy": "chat"} {
		if got[model] != want {
			t.Errorf("band %q modality = %q, want %q", model, got[model], want)
		}
	}
}

func TestBandIsVoice(t *testing.T) {
	cases := map[string]bool{"chat": false, "": false, "tts": true, "stt": true}
	for mod, want := range cases {
		if got := (band{modality: mod}).isVoice(); got != want {
			t.Errorf("band{modality:%q}.isVoice() = %v, want %v", mod, got, want)
		}
	}
}

// SUPERSEDED by the founder DELTA (LLM primacy): voice bands are NOT grouped into the top-level
// list at all — they are EXCLUDED and live one drill-in deeper (THE DJ BOOTH). This regression
// pins the adversarial corner the old "group voices last" test guarded: a voice band handed a
// table-topping signal + the cheapest price must STILL never appear in the LLM band list.
// (The Booth-entry + footnote behaviors are asserted in voicebooth_test.go.)
func TestVisibleBandsExcludesVoicesEvenTopSignal(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	for i := range m.bands {
		if m.bands[i].model == "eager-puma-54-voice" {
			m.bands[i].minOut = 0.0
			hot := offer{NodeID: "hot", Model: "eager-puma-54-voice", Modality: "tts", Online: true, Signal: 100, TPS: 999}
			m.bands[i].all = []offer{hot}
			m.bands[i].cheapest = &m.bands[i].all[0]
		}
	}
	for _, b := range m.visibleBands() {
		if b.isVoice() {
			t.Fatalf("a voice band (%q) must NEVER appear in the top-level LLM list, even with a top signal", b.model)
		}
	}
}

// SUPERSEDED by the DELTA: there is no "VOICES" divider-SECTION in the list any more. Voice is a
// single dim FOOTNOTE at the foot of the LLM band list ("also on air: N voices ▸ [v]") that
// drills into the Booth. This regression pins that the old always-on divider-section is gone.
func TestBrowseViewHasFootnoteNotVoicesSection(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	out := m.browseView(100)
	if !strings.Contains(out, "also on air") {
		t.Errorf("browse view should carry the dim voice footnote; got:\n%s", out)
	}
	// The old VOICES divider-section (a ▌ VOICES header row with the speak/listen legend) is gone.
	if strings.Contains(out, "▌ "+"VOICES") || strings.Contains(strings.ToUpper(out), "SPEAK · ") {
		t.Errorf("the old VOICES divider-section must be gone; got:\n%s", out)
	}
}

// --- Behavior: selecting a voice band opens PREVIEW, not a chat channel --------

// A FREE tts band: Enter goes straight to the preview panel (mode), fires the synth, and NEVER
// opens a chat channel (connected stays nil, mode is never modeChat/modeConnectConfirm).
func TestSelectFreeVoiceOpensPreviewNotChat(t *testing.T) {
	var speechHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" {
			atomic.AddInt32(&speechHits, 1)
			w.Header().Set("X-RogerAI-Cost", "0")
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("ID3fake-mp3-bytes"))
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			t.Errorf("a voice preview must NEVER hit the chat relay")
		}
	}))
	defer srv.Close()

	m := voiceSeed(t, srv.URL)
	m.previewPlayer = func([]byte) (string, bool, error) { return "", true, nil } // stub: "played"

	// Cue the DJ from THE DJ BOOTH (the same startVoicePreview entry the Booth uses).
	tm, cmd := openVoicePreview(t, &m, "free-crier-voice")
	m = asModel(tm)
	if m.mode != modeVoicePreview {
		t.Fatalf("cueing a voice DJ should open modeVoicePreview, got mode %d", m.mode)
	}
	if m.connected != nil {
		t.Fatalf("a voice preview must NOT open a chat channel (connected=%v)", m.connected)
	}
	if cmd == nil {
		t.Fatal("a FREE voice preview should fire the synth Cmd immediately")
	}
	// Run the synth Cmd (real POST to the httptest broker) and feed the result back.
	msg := cmd()
	if _, ok := msg.(voicePreviewMsg); !ok {
		t.Fatalf("synth Cmd should yield a voicePreviewMsg, got %T", msg)
	}
	if atomic.LoadInt32(&speechHits) != 1 {
		t.Fatalf("free voice preview should POST /v1/audio/speech exactly once, got %d", speechHits)
	}
	tm, _ = m.Update(msg)
	m = asModel(tm)
	if m.mode != modeVoicePreview {
		t.Fatalf("after the sample lands, still in preview; got mode %d", m.mode)
	}
}

// A PAID tts band: Enter opens the preview in a CONFIRM state and does NOT spend — no POST to
// /v1/audio/speech until the user explicitly confirms. This is the money gate (founder #1).
func TestSelectPaidVoiceRequiresConfirmBeforeSpend(t *testing.T) {
	var speechHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" {
			atomic.AddInt32(&speechHits, 1)
			w.Header().Set("X-RogerAI-Cost", "0.000228")
			w.Write([]byte("ID3fake-mp3-bytes"))
			return
		}
	}))
	defer srv.Close()

	m := voiceSeed(t, srv.URL)
	m.previewPlayer = func([]byte) (string, bool, error) { return "", true, nil }

	tm, cmd := openVoicePreview(t, &m, "eager-puma-54-voice")
	m = asModel(tm)
	if m.mode != modeVoicePreview {
		t.Fatalf("paid voice should open modeVoicePreview, got %d", m.mode)
	}
	if !m.previewNeedsConfirm() {
		t.Fatal("a PAID voice preview must be in the confirm-first state (needs opt-in)")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m.Update(msg)
		}
	}
	if atomic.LoadInt32(&speechHits) != 0 {
		t.Fatalf("a PAID voice preview must NOT spend before confirm; /v1/audio/speech hit %d times", speechHits)
	}
	// The panel must disclose the (tiny) sample cost so the spend is transparent.
	if !strings.Contains(m.voicePreviewView(100), "$") {
		t.Errorf("paid preview panel should show the sample cost; got:\n%s", m.voicePreviewView(100))
	}

	// Now CONFIRM (y): the synth Cmd fires and the endpoint is hit exactly once.
	tm, cmd = m.onVoicePreviewKey(keyMsg("y"))
	m = asModel(tm)
	if cmd == nil {
		t.Fatal("confirming a paid preview should fire the synth Cmd")
	}
	msg := cmd()
	if _, ok := msg.(voicePreviewMsg); !ok {
		t.Fatalf("confirmed synth should yield voicePreviewMsg, got %T", msg)
	}
	if atomic.LoadInt32(&speechHits) != 1 {
		t.Fatalf("after confirm, /v1/audio/speech should be hit exactly once, got %d", speechHits)
	}
}

// Declining a paid preview (n / esc) cancels back to browse and never spends.
func TestPaidVoicePreviewDeclineCancels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" {
			t.Errorf("declining must never POST /v1/audio/speech")
		}
	}))
	defer srv.Close()
	m := voiceSeed(t, srv.URL)
	tm, _ := openVoicePreview(t, &m, "eager-puma-54-voice")
	m = asModel(tm)
	tm, _ = m.onVoicePreviewKey(keyMsg("n"))
	m = asModel(tm)
	if m.mode != modeBrowse {
		t.Fatalf("declining a paid preview should return to browse, got mode %d", m.mode)
	}
}

// An stt band cannot be previewed by chat: selecting it shows an INFORMATIONAL panel (model +
// price + "send audio via the app/API"), NEVER a chat channel and NEVER a speech POST.
func TestSelectSTTShowsInfoPanelNoChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" || r.URL.Path == "/v1/chat/completions" {
			t.Errorf("an stt preview must not hit %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	m := voiceSeed(t, srv.URL)
	tm, cmd := openVoicePreview(t, &m, "whisper-listener")
	m = asModel(tm)
	if m.mode != modeVoicePreview {
		t.Fatalf("previewing an stt band should open the preview panel, got mode %d", m.mode)
	}
	if m.connected != nil {
		t.Fatal("an stt band must not open a chat channel")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("an stt preview should not fire a synth Cmd, got %T", msg)
		}
	}
	view := m.voicePreviewView(100)
	if !strings.Contains(strings.ToLower(view), "audio") {
		t.Errorf("stt info panel should tell the user to send audio via the app/API; got:\n%s", view)
	}
}

// esc leaves the preview back to the band browser (no lingering state).
func TestVoicePreviewEscReturnsToBrowse(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	m.previewPlayer = func([]byte) (string, bool, error) { return "", true, nil }
	tm, _ := openVoicePreview(t, &m, "free-crier-voice")
	m = asModel(tm)
	tm, _ = m.onVoicePreviewKey(keyMsg("esc"))
	if asModel(tm).mode != modeBrowse {
		t.Fatalf("esc should return to browse, got mode %d", asModel(tm).mode)
	}
}

// NOTE: the cross-platform audio player (audioEnv/play/resolveAudioPlayer/writeTempWAV) was
// EXTRACTED into internal/audio (shared with `roger say`); its tests now live in
// internal/audio/audio_test.go. The TUI keeps only the preview-flow tests below, which stub the
// player via m.previewPlayer (audioPlayerFn == audio.PlayerFn).

// After a FREE sample plays, r replays it (re-synths) — a paid band would re-enter the confirm
// gate instead (startVoicePreview picks the stage), never an auto-spend.
func TestVoicePreviewReplay(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" {
			atomic.AddInt32(&hits, 1)
			w.Header().Set("X-RogerAI-Cost", "0")
			w.Write([]byte("RIFFwav"))
		}
	}))
	defer srv.Close()
	m := voiceSeed(t, srv.URL)
	m.previewPlayer = func([]byte) (string, bool, error) { return "", true, nil }
	tm, cmd := openVoicePreview(t, &m, "free-crier-voice")
	m = asModel(tm)
	m = asModel(func() tea.Model { t, _ := m.Update(cmd()); return t }()) // land the first sample
	if m.previewStage != previewDone {
		t.Fatalf("expected done after the first sample, got stage %d", m.previewStage)
	}
	// r replays: a free band re-synths straight away (a fresh Cmd).
	tm, cmd = m.onVoicePreviewKey(keyMsg("r"))
	m = asModel(tm)
	if cmd == nil {
		t.Fatal("r on a played FREE preview should re-synth (fire a Cmd)")
	}
	cmd()
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("replay should POST a second time, got %d hits", hits)
	}
}

// A non-action key on the stt info panel is a harmless no-op (stays in the preview).
func TestVoicePreviewInfoKeyNoop(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := openVoicePreview(t, &m, "whisper-listener")
	m = asModel(tm)
	tm, cmd := m.onVoicePreviewKey(keyMsg("x"))
	if asModel(tm).mode != modeVoicePreview || cmd != nil {
		t.Errorf("an unrelated key on the info panel should be a no-op, got mode %d cmd %v", asModel(tm).mode, cmd)
	}
}

// An OFFLINE voice band opens the preview in the off-air stage (nothing to synth), never chat.
func TestSelectOfflineVoiceShowsOffAir(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// Add an offline voice band directly (groupBands from an offline offer).
	m.bands = append(m.bands, band{model: "sleepy-voice", modality: "tts", online: false})
	tm, cmd := openVoicePreview(t, &m, "sleepy-voice")
	m = asModel(tm)
	if m.mode != modeVoicePreview || m.previewStage != previewOffline {
		t.Fatalf("offline voice should open the off-air preview, got mode %d stage %d", m.mode, m.previewStage)
	}
	if cmd != nil {
		t.Error("an offline voice preview must not fire a synth Cmd")
	}
	if !strings.Contains(strings.ToLower(m.voicePreviewView(80)), "off air") {
		t.Errorf("off-air panel should say so; got:\n%s", m.voicePreviewView(80))
	}
}

// applyVoicePreview folds each synth outcome (played / saved-to-file / plain-fetch / error) into
// the right panel stage + status.
func TestApplyVoicePreviewOutcomes(t *testing.T) {
	base := voiceSeed(t, "http://broker.local")
	base.mode = modeVoicePreview

	played := base.applyVoicePreview(voicePreviewMsg{cost: 0.0002, played: true})
	if played.previewStage != previewDone || !played.previewPlayed {
		t.Errorf("played outcome should be done+played, got stage %d played %v", played.previewStage, played.previewPlayed)
	}
	if !strings.Contains(played.voicePreviewView(80), "played") {
		t.Errorf("played panel should say played; got:\n%s", played.voicePreviewView(80))
	}

	saved := base.applyVoicePreview(voicePreviewMsg{played: false, path: "/tmp/x.wav"})
	if saved.previewStage != previewDone || saved.previewPath != "/tmp/x.wav" {
		t.Errorf("saved outcome should carry the path, got %+v", saved)
	}
	if !strings.Contains(saved.voicePreviewView(80), "/tmp/x.wav") {
		t.Errorf("saved panel should show the file path; got:\n%s", saved.voicePreviewView(80))
	}

	fetched := base.applyVoicePreview(voicePreviewMsg{played: false, path: ""})
	if fetched.previewStage != previewDone {
		t.Errorf("plain fetch should be done, got %d", fetched.previewStage)
	}

	errd := base.applyVoicePreview(voicePreviewMsg{err: "boom"})
	if errd.previewStage != previewError || errd.previewErr != "boom" {
		t.Errorf("error outcome should be the error stage, got stage %d err %q", errd.previewStage, errd.previewErr)
	}
	if !strings.Contains(errd.voicePreviewView(80), "boom") {
		t.Errorf("error panel should show the message; got:\n%s", errd.voicePreviewView(80))
	}
}

// A late voicePreviewMsg (user already left the preview) is ignored by Update — no panic, no
// state change.
func TestLateVoicePreviewMsgIgnored(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	m.mode = modeBrowse // not in the preview
	tm, _ := m.Update(voicePreviewMsg{cost: 1, played: true})
	if asModel(tm).mode != modeBrowse {
		t.Errorf("a late preview msg must not change mode, got %d", asModel(tm).mode)
	}
}

// The synth Cmd surfaces a clean error (not a panic) when the broker is unreachable or returns a
// non-200 — and never reports played.
func TestSynthVoiceSampleErrors(t *testing.T) {
	// Unreachable broker.
	m := voiceSeed(t, "http://127.0.0.1:1") // nothing listening
	m.previewBand = band{model: "v", modality: "tts", online: true, minIn: 1}
	m.previewPlayer = func([]byte) (string, bool, error) {
		t.Fatal("player must not run on a fetch error")
		return "", false, nil
	}
	msg := m.synthVoiceSample()()
	vm, ok := msg.(voicePreviewMsg)
	if !ok || vm.err == "" {
		t.Fatalf("unreachable broker should yield a voicePreviewMsg with an error, got %#v", msg)
	}

	// Non-200 with a broker JSON error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"no station on air for v"}`))
	}))
	defer srv.Close()
	m2 := voiceSeed(t, srv.URL)
	m2.previewBand = band{model: "v", modality: "tts", online: true, minIn: 1}
	msg2 := m2.synthVoiceSample()()
	vm2 := msg2.(voicePreviewMsg)
	if !strings.Contains(vm2.err, "no station") {
		t.Errorf("non-200 should surface the broker error message, got %q", vm2.err)
	}
}

// httpErrMessage prefers the broker's JSON error, else a terse status summary.
func TestHTTPErrMessage(t *testing.T) {
	if got := httpErrMessage(503, []byte(`{"error":"nope"}`)); got != "nope" {
		t.Errorf("JSON error should win, got %q", got)
	}
	if got := httpErrMessage(500, []byte("not json")); !strings.Contains(got, "500") {
		t.Errorf("non-JSON should fall back to a status summary, got %q", got)
	}
}

// voicePriceLabel renders the metering unit that bills (chars for tts, audio-bytes for stt) and
// "free" for a free band.
func TestVoicePriceLabel(t *testing.T) {
	if got := voicePriceLabel(band{modality: "tts", online: true, minIn: 12}); !strings.Contains(got, "chars") {
		t.Errorf("tts label should be per-chars, got %q", got)
	}
	if got := voicePriceLabel(band{modality: "stt", online: true, minIn: 5}); !strings.Contains(got, "audio-bytes") {
		t.Errorf("stt label should be per-audio-bytes, got %q", got)
	}
	if got := voicePriceLabel(band{modality: "tts", online: true, free: true}); got != "free" {
		t.Errorf("free band should read free, got %q", got)
	}
	if got := voicePriceLabel(band{modality: "tts", online: false}); got != "-" {
		t.Errorf("offline band should read -, got %q", got)
	}
}

// The preview footer is stage-aware (confirm shows the cost, synth shows synthesizing, etc.).
func TestVoicePreviewFooterStages(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	m.previewCost = 0.0002
	m.previewStage = previewConfirm
	if !strings.Contains(m.voicePreviewFooter(), "play sample") {
		t.Errorf("confirm footer should offer to play the sample; got %q", m.voicePreviewFooter())
	}
	m.previewStage = previewInfoSTT
	if !strings.Contains(strings.ToLower(m.voicePreviewFooter()), "audio") {
		t.Errorf("stt footer should mention audio; got %q", m.voicePreviewFooter())
	}
	m.previewStage = previewSynth
	if !strings.Contains(m.voicePreviewFooter(), "synthesizing") {
		t.Errorf("synth footer; got %q", m.voicePreviewFooter())
	}
	m.previewStage = previewDone
	if !strings.Contains(m.voicePreviewFooter(), "play again") {
		t.Errorf("done footer should offer replay; got %q", m.voicePreviewFooter())
	}
}

// sampleVoiceCost is $0 for an offline or free band, and the char-metered cost otherwise.
func TestSampleVoiceCost(t *testing.T) {
	if c := sampleVoiceCost(band{online: false, minIn: 100}); c != 0 {
		t.Errorf("offline band sample cost should be 0, got %v", c)
	}
	if c := sampleVoiceCost(band{online: true, free: true, minIn: 100}); c != 0 {
		t.Errorf("free band sample cost should be 0, got %v", c)
	}
	paid := sampleVoiceCost(band{online: true, minIn: 1_000_000}) // 1 credit / char
	if paid != float64(len([]rune(sampleVoiceText))) {
		t.Errorf("paid sample cost = %v, want %d (one credit per char)", paid, len([]rune(sampleVoiceText)))
	}
}
