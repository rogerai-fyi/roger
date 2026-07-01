package tui

// voicebooth_share_cov_test.go exhaustively drives the SHARE VOICE BOOTH key handlers, the picker
// key handler + rendered popover, the commit validation branches, and the small parse/format
// helpers — the adversarial + niche corners the happy-path BDD does not reach. Real model, no
// mocks; the injectable player + a dead upstream stand in for the audio device / an unreachable
// server.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/node"
)

// TestBoothLocalPreviewSuccess drives the SUCCESS path of the local preview + audition against a
// live httptest server: the request carries the voice+speed, the stub player gets the wav, and the
// applyLocalVoices path replaces the bundled fallback with real ids.
func TestBoothLocalPreviewSuccess(t *testing.T) {
	var gotVoice string
	var gotSpeed float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/audio/voices") {
			w.Write([]byte(`["af_heart","am_onyx","bf_emma"]`))
			return
		}
		var body struct {
			Voice string  `json:"voice"`
			Speed float64 `json:"speed"`
		}
		_ = decodeJSON(r, &body)
		gotVoice, gotSpeed = body.Voice, body.Speed
		w.Header().Set("Content-Type", "audio/wav")
		w.Write([]byte("~wav~"))
	}))
	defer srv.Close()

	var handed []byte
	m := vbSeed(t)
	rows := m.shareRows
	rows[0].upstream = srv.URL + "/v1/chat/completions"
	m.setShareRows(rows)
	m.previewPlayer = func(wav []byte) (string, bool, error) { handed = append([]byte(nil), wav...); return "", true, nil }
	m.vbVoice = "af_heart:0.5+am_onyx:0.5"
	m.vbSpeed = 1.25

	// Preview.
	cmd := m.playVoiceBoothPreview()
	if cmd == nil {
		t.Fatal("preview should return a Cmd against a live server")
	}
	bp := cmd().(boothPreviewMsg)
	if bp.err != "" {
		t.Fatalf("live preview errored: %s", bp.err)
	}
	if gotVoice != "af_heart:0.5+am_onyx:0.5" || gotSpeed != 1.25 {
		t.Errorf("preview request voice/speed = %q/%v, want the blend + 1.25", gotVoice, gotSpeed)
	}
	if string(handed) != "~wav~" {
		t.Errorf("player got %q, want the wav bytes", handed)
	}

	// Voices fetch replaces the bundled fallback.
	m.openVoicePicker()
	vm := m.fetchLocalVoicesCmd()().(localVoicesMsg)
	got := m.applyLocalVoices(vm)
	if len(got.vpVoices) != 3 || got.vpVoices[0] != "af_heart" {
		t.Errorf("fetched voices = %v, want the 3 local ids", got.vpVoices)
	}

	// Audition the highlighted voice against the live server.
	got.vpCursor = 1 // am_onyx
	acmd := got.auditionPickerVoice()
	if acmd == nil {
		t.Fatal("audition should return a Cmd")
	}
	ab := acmd().(boothPreviewMsg)
	if ab.err != "" || gotVoice != "am_onyx" {
		t.Errorf("audition voice = %q (err %q), want am_onyx", gotVoice, ab.err)
	}
}

// TestBoothURLsEmptyCursor: the local URLs are empty when the cursor is out of range (defensive).
func TestBoothURLsEmptyCursor(t *testing.T) {
	m := vbSeed(t)
	m.shareCursor = 99
	if m.localSpeechURL() != "" || m.localVoicesURL() != "" {
		t.Error("an out-of-range cursor should yield empty local URLs")
	}
}

// vbSeed builds a logged-in model parked in the VOICE BOOTH for a tts "kokoro" row, wide-sized,
// with a stub player + a (by default) dead upstream so the local-preview error path is reachable.
func vbSeed(t *testing.T) *model {
	t.Helper()
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m := asModel(tm)
	m.loggedIn = true
	m.mode = modeShare
	m.setShareRows([]shareRow{{model: "kokoro", modality: "tts", ctx: 4096, upstream: "http://127.0.0.1:0/v1/chat/completions"}})
	m.shareCursor = 0
	m.previewPlayer = func(wav []byte) (string, bool, error) { return "", true, nil }
	m.enterVoiceBooth()
	return &m
}

func vbKey(name string) tea.KeyMsg {
	switch name {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

// TestBoothFieldCycling: tab/↑↓ cycle forward, shift+tab/↑ cycle back, wrapping at the ends.
func TestBoothFieldCycling(t *testing.T) {
	m := vbSeed(t)
	if m.vbField != vbFieldName {
		t.Fatalf("BOOTH opens on the name field, got %d", m.vbField)
	}
	for i, want := range []int{vbFieldVoice, vbFieldBlend, vbFieldSpeed, vbFieldLang, vbFieldPrice, vbFieldName} {
		m.onShareVoiceKey(vbKey("tab"))
		if m.vbField != want {
			t.Fatalf("tab #%d -> field %d, want %d", i, m.vbField, want)
		}
	}
	m.onShareVoiceKey(vbKey("shift+tab"))
	if m.vbField != vbFieldPrice {
		t.Errorf("shift+tab from name should wrap to price, got %d", m.vbField)
	}
	m.onShareVoiceKey(vbKey("up"))
	if m.vbField != vbFieldLang {
		t.Errorf("up should step back to lang, got %d", m.vbField)
	}
}

// TestBoothTypingPerField: runes type into name/language/price (digits+dot only for price); b/x/p
// TYPE on a text field (never trigger commands) but ADD/CLEAR/PREVIEW off a text field.
func TestBoothTypingPerField(t *testing.T) {
	m := vbSeed(t)
	// name: full multi-rune + letters that are ALSO command keys (b, x, p) must land as text.
	m.vbField = vbFieldName
	m.onShareVoiceKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Bax Pop")})
	if m.vbName != "Bax Pop" {
		t.Errorf("name = %q, want %q (b/x/p must type on a text field)", m.vbName, "Bax Pop")
	}
	m.onShareVoiceKey(vbKey("backspace"))
	if m.vbName != "Bax Po" {
		t.Errorf("backspace name = %q, want %q", m.vbName, "Bax Po")
	}
	// language single-rune path.
	m.vbField = vbFieldLang
	m.vbLang = ""
	for _, r := range "en-GB" {
		m.onShareVoiceKey(vbKey(string(r)))
	}
	if m.vbLang != "en-GB" {
		t.Errorf("language = %q, want en-GB", m.vbLang)
	}
	// price: digits+dot only; letters ignored.
	m.vbField = vbFieldPrice
	m.vbPrice = ""
	for _, r := range "0.a2z0" {
		m.onShareVoiceKey(vbKey(string(r)))
	}
	if m.vbPrice != "0.20" {
		t.Errorf("price = %q, want 0.20 (letters filtered)", m.vbPrice)
	}
	m.onShareVoiceKey(vbKey("backspace"))
	if m.vbPrice != "0.2" {
		t.Errorf("backspace price = %q, want 0.2", m.vbPrice)
	}
}

// TestBoothSpaceAndBlendKeys: space on the voice field opens the picker; space elsewhere types; b
// adds a blend voice from the voice/blend field; x clears it.
func TestBoothSpaceBlendKeys(t *testing.T) {
	// space on the voice field opens the picker.
	m := vbSeed(t)
	m.vbField = vbFieldVoice
	nm, _ := m.onShareVoiceKey(vbKey("space"))
	if asModel(nm).mode != modeVoicePicker {
		t.Errorf("space on the voice field should open the picker")
	}
	// space on the name field types a space.
	m2 := vbSeed(t)
	m2.vbField = vbFieldName
	m2.vbName = "a"
	m2.onShareVoiceKey(vbKey("space"))
	if m2.vbName != "a " {
		t.Errorf("space on name = %q, want 'a '", m2.vbName)
	}
	// b on the blend field adds a voice; x clears back to single.
	m3 := vbSeed(t)
	m3.vbVoice = "af_heart"
	m3.vbField = vbFieldBlend
	m3.onShareVoiceKey(vbKey("b"))
	if len(m3.vbBlend) != 2 {
		t.Fatalf("b should add a blend voice, got %d components", len(m3.vbBlend))
	}
	m3.onShareVoiceKey(vbKey("x"))
	if len(m3.vbBlend) != 0 {
		t.Errorf("x should clear the blend, got %d", len(m3.vbBlend))
	}
}

// TestBoothVoiceFieldTypingOpensPicker: typing a non-command rune on the voice field opens the
// picker pre-filtered by the keystroke.
func TestBoothVoiceFieldTypingOpensPicker(t *testing.T) {
	m := vbSeed(t)
	m.vbField = vbFieldVoice
	nm, _ := m.onShareVoiceKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	got := asModel(nm)
	if got.mode != modeVoicePicker || got.vpFilter != "a" {
		t.Errorf("typing 'a' on the voice field should open the picker filtered by 'a'; mode=%v filter=%q", got.mode, got.vpFilter)
	}
}

// TestBoothPreviewErrorNoServer: ▶ / p preview against a row with a dead upstream surfaces a local
// error and never crashes; the preview is FREE (previewCost stays 0).
func TestBoothPreviewError(t *testing.T) {
	m := vbSeed(t) // upstream 127.0.0.1:0 (dead)
	m.vbVoice = "af_heart"
	_, cmd := m.onShareVoiceKey(vbKey("▶"))
	if cmd == nil {
		t.Fatal("▶ should return a preview Cmd")
	}
	msg := cmd()
	bp, ok := msg.(boothPreviewMsg)
	if !ok {
		t.Fatalf("preview msg type = %T, want boothPreviewMsg", msg)
	}
	if bp.err == "" {
		t.Error("a dead local server should yield a preview error")
	}
	got := m.applyBoothPreview(bp)
	if got.previewCost != 0 {
		t.Errorf("the LOCAL preview must be FREE, previewCost = %v", got.previewCost)
	}
	if !strings.Contains(strings.ToLower(got.status+got.vbErr), "local") {
		t.Errorf("the error should mention the local server, got %q / %q", got.status, got.vbErr)
	}
}

// TestBoothPreviewNoUpstream: with NO upstream on the row, ▶ returns no Cmd and sets an inline
// error (never a nil-deref).
func TestBoothPreviewNoUpstream(t *testing.T) {
	m := vbSeed(t)
	rows := m.shareRows
	rows[0].upstream = ""
	m.setShareRows(rows)
	_, cmd := m.onShareVoiceKey(vbKey("▶"))
	if cmd != nil {
		t.Error("a row with no upstream should not fire a preview Cmd")
	}
	if m.vbErr == "" {
		t.Error("a row with no upstream should set an inline error")
	}
}

// TestBoothApplyPreviewSaved / played: the two non-error outcomes fold into a status line.
func TestBoothApplyPreviewOutcomes(t *testing.T) {
	m := vbSeed(t)
	played := m.applyBoothPreview(boothPreviewMsg{played: true})
	if !strings.Contains(stripANSI(played.status), "played") {
		t.Errorf("played outcome status = %q", stripANSI(played.status))
	}
	saved := m.applyBoothPreview(boothPreviewMsg{path: "/tmp/x.wav"})
	if !strings.Contains(saved.status, "/tmp/x.wav") {
		t.Errorf("saved outcome should name the path, got %q", stripANSI(saved.status))
	}
	fetched := m.applyBoothPreview(boothPreviewMsg{})
	if !strings.Contains(stripANSI(fetched.status), "fetched") {
		t.Errorf("fetched outcome status = %q", stripANSI(fetched.status))
	}
}

// TestBoothCommitPriceValidation: a bad price blocks the save (inline error, stays in the BOOTH);
// an over-ceiling price is rejected; a clean commit stores the config + price and returns.
func TestBoothCommitPriceValidation(t *testing.T) {
	// unparseable price blocks.
	m := vbSeed(t)
	m.vbPrice = "12x"
	if m.commitVoiceBooth() {
		t.Error("an unparseable price should BLOCK the commit")
	}
	if m.vbErr == "" {
		t.Error("a blocked commit should set an inline error")
	}
	// over-ceiling blocks.
	m2 := vbSeed(t)
	m2.vbPrice = "9999"
	if m2.commitVoiceBooth() {
		t.Error("an over-ceiling price should BLOCK the commit")
	}
	// clean commit stores name+voice+price.
	m3 := vbSeed(t)
	m3.vbName = "DJ Test"
	m3.vbVoice = "am_onyx"
	m3.vbPrice = "0.02"
	if !m3.commitVoiceBooth() {
		t.Fatalf("a clean commit should succeed; err=%q", m3.vbErr)
	}
	vc := m3.ctrl.VoiceConfigFor("kokoro")
	if vc.Name != "DJ Test" || vc.Voice != "am_onyx" {
		t.Errorf("commit stored %+v, want name/voice set", vc)
	}
	// The field is $/1k chars; the stored PriceIn is per-1M chars, so "0.02" -> 20 per-1M.
	if got := m3.pricingFor("kokoro").In; got != 20.0 {
		t.Errorf("commit stored price %v per-1M, want 20 (0.02 $/1k)", got)
	}
}

// TestBoothCommitBlendNormalizes: a save with a >1-summing blend normalizes the stored voice string
// to weights that sum to 1.
func TestBoothCommitBlendNormalizes(t *testing.T) {
	m := vbSeed(t)
	m.vbVoice = "af_heart"
	m.addBlendVoice("af_bella") // -> even 0.5/0.5
	m.addBlendVoice("af_sky")   // -> even thirds
	if !m.commitVoiceBooth() {
		t.Fatalf("commit failed: %q", m.vbErr)
	}
	voice := m.ctrl.VoiceConfigFor("kokoro").Voice
	if !strings.Contains(voice, "+") {
		t.Fatalf("expected a blend string, got %q", voice)
	}
	sum := blendWeightSum(voice)
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("normalized blend %q weights sum to %v, want 1", voice, sum)
	}
}

// TestPickerKeyMatrix drives the picker key handler: filter typing + backspace, cursor moves
// (up/down/left/right clamped), enter picks (clears a blend), esc returns to the BOOTH, ▶ auditions.
func TestPickerKeyMatrix(t *testing.T) {
	m := vbSeed(t)
	m.vbField = vbFieldVoice
	nm, _ := m.openVoicePicker()
	mm := asModel(nm)
	mm.vpVoices = []string{"af_heart", "af_bella", "am_onyx"}
	// filter down, then backspace back up.
	mm.onVoicePickerKey(vbKey("a"))
	mm.onVoicePickerKey(vbKey("f"))
	if mm.vpFilter != "af" {
		t.Errorf("filter = %q, want af", mm.vpFilter)
	}
	mm.onVoicePickerKey(vbKey("backspace"))
	if mm.vpFilter != "a" {
		t.Errorf("backspace filter = %q, want a", mm.vpFilter)
	}
	// cursor down then up, clamped at the ends.
	mm.vpFilter = ""
	mm.vpCursor = 0
	mm.onVoicePickerKey(vbKey("down"))
	if mm.vpCursor != 1 {
		t.Errorf("down cursor = %d, want 1", mm.vpCursor)
	}
	mm.onVoicePickerKey(vbKey("up"))
	mm.onVoicePickerKey(vbKey("up")) // clamp at 0
	if mm.vpCursor != 0 {
		t.Errorf("up cursor should clamp at 0, got %d", mm.vpCursor)
	}
	mm.onVoicePickerKey(vbKey("right"))
	if mm.vpCursor != 1 {
		t.Errorf("right cursor = %d, want 1", mm.vpCursor)
	}
	// enter picks the highlighted voice, clears any blend, returns to the BOOTH.
	mm.vbBlend = []blendVoice{{id: "af_heart", weight: 0.5}, {id: "af_bella", weight: 0.5}}
	pk, _ := mm.onVoicePickerKey(vbKey("enter"))
	got := asModel(pk)
	if got.mode != modeShareVoice {
		t.Errorf("enter should return to the BOOTH")
	}
	if got.vbVoice != "af_bella" || len(got.vbBlend) != 0 {
		t.Errorf("enter should pick af_bella + clear the blend; voice=%q blend=%d", got.vbVoice, len(got.vbBlend))
	}
	// esc from the picker returns to the BOOTH on the voice field.
	ek, _ := got.onVoicePickerKey(vbKey("esc"))
	if e := asModel(ek); e.mode != modeShareVoice || e.vbField != vbFieldVoice {
		t.Errorf("esc should return to the BOOTH voice field")
	}
}

// TestPickerAuditionNoServer: ▶ audition with a dead upstream sets an inline error, no crash, no
// Cmd fired past the guard.
func TestPickerAuditionNoServer(t *testing.T) {
	m := vbSeed(t)
	rows := m.shareRows
	rows[0].upstream = ""
	m.setShareRows(rows)
	m.openVoicePicker()
	m.vpVoices = []string{"af_heart"}
	m.vpCursor = 0
	_, cmd := m.onVoicePickerKey(vbKey("▶"))
	if cmd != nil {
		t.Error("audition with no upstream should not fire a Cmd")
	}
	if m.vbErr == "" {
		t.Error("audition with no upstream should set an inline error")
	}
}

// TestPickerViewRenders: the popover renders its header, grouped rows, the highlighted cursor mark,
// a filter echo, and the empty-filter case. Width-safe.
func TestPickerViewRenders(t *testing.T) {
	m := vbSeed(t)
	m.openVoicePicker()
	m.vpVoices = []string{"af_heart", "am_onyx", "bf_emma", "bm_george"}
	m.vpCursor = 0
	out := stripANSI(m.voicePickerView(100))
	for _, want := range []string{"PICK A VOICE", "American female", "af_heart", "British male", "audition"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker view missing %q:\n%s", want, out)
		}
	}
	// filter echo + no-match case.
	m.vpFilter = "zzz"
	out2 := stripANSI(m.voicePickerView(100))
	if !strings.Contains(out2, "no voices match") {
		t.Errorf("empty-filter view should say no match:\n%s", out2)
	}
	// footer renders.
	if !strings.Contains(stripANSI(m.voicePickerFooter()), "audition") {
		t.Error("picker footer should mention audition")
	}
	// booth footer renders (wide + narrow).
	if !strings.Contains(stripANSI(m.shareVoiceFooter()), "save") {
		t.Error("booth footer should mention save")
	}
	m.width = 40
	if stripANSI(m.shareVoiceFooter()) == "" {
		t.Error("narrow booth footer should render")
	}
}

// TestBoothViewBlendAndError: the BOOTH view renders the blend display when a blend is set and the
// inline error when one is present.
func TestBoothViewBlendAndError(t *testing.T) {
	m := vbSeed(t)
	m.vbVoice = "af_heart"
	m.addBlendVoice("af_bella")
	m.vbErr = "weights sum to 1.10"
	out := stripANSI(m.shareVoiceView(100))
	if !strings.Contains(out, "af_heart") || !strings.Contains(out, "af_bella") {
		t.Errorf("booth view should show the blend components:\n%s", out)
	}
	if !strings.Contains(out, "weights sum to 1.10") {
		t.Errorf("booth view should show the inline error:\n%s", out)
	}
	// blendDisplay single-voice case.
	m2 := vbSeed(t)
	if got := m2.blendDisplay(); got != "(single voice)" {
		t.Errorf("blendDisplay with no blend = %q, want (single voice)", got)
	}
}

// TestBoothEnterAndEscKeys: enter on a clean BOOTH commits + returns to the table; esc cancels.
func TestBoothEnterEscKeys(t *testing.T) {
	m := vbSeed(t)
	m.vbName = "DJ"
	m.vbVoice = "am_onyx"
	m.vbPrice = "0"
	nm, _ := m.onShareVoiceKey(vbKey("enter"))
	if asModel(nm).mode != modeShare {
		t.Error("enter on a clean BOOTH should return to the SHARE table")
	}
	m2 := vbSeed(t)
	em, _ := m2.onShareVoiceKey(vbKey("esc"))
	if asModel(em).mode != modeShare {
		t.Error("esc should return to the SHARE table")
	}
}

// TestEnterVoiceBoothSeeding: opening the BOOTH seeds fields from a stored VoiceConfig (incl. a
// blend), and defaults speed to 1.0 / language to en-US when unset.
func TestEnterVoiceBoothSeeding(t *testing.T) {
	m := vbSeed(t)
	// Seed a stored config with a blend, reopen, and assert the fields hydrate.
	m.ctrl.SetVoiceConfig("kokoro", node.VoiceConfig{Name: "Prev DJ", Voice: "af_heart:0.6+af_bella:0.4", Speed: 1.5, Language: "en-GB"})
	m.mode = modeShare
	m.enterVoiceBooth()
	if m.vbName != "Prev DJ" || m.vbSpeed != 1.5 || m.vbLang != "en-GB" {
		t.Errorf("reopen did not hydrate name/speed/lang: %+v", *m)
	}
	if len(m.vbBlend) != 2 {
		t.Errorf("reopen should hydrate a 2-voice blend, got %d", len(m.vbBlend))
	}
	// A single-voice stored config hydrates vbVoice, not the blend.
	m.ctrl.SetVoiceConfig("kokoro", node.VoiceConfig{Voice: "am_onyx"})
	m.mode = modeShare
	m.enterVoiceBooth()
	if m.vbVoice != "am_onyx" || len(m.vbBlend) != 0 {
		t.Errorf("single-voice reopen should set vbVoice, not a blend: voice=%q blend=%d", m.vbVoice, len(m.vbBlend))
	}
	if m.vbSpeed != 1.0 || m.vbLang != "en-US" {
		t.Errorf("unset speed/lang should default to 1.0/en-US, got %v/%q", m.vbSpeed, m.vbLang)
	}
}

// TestEnterVoiceBoothLoginGate: an anonymous operator gets the login gate; the BOOTH does not open.
func TestEnterVoiceBoothLoginGate(t *testing.T) {
	var tm tea.Model = New("http://broker.local", "")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m := asModel(tm)
	m.loggedIn = false
	m.mode = modeShare
	m.setShareRows([]shareRow{{model: "kokoro", modality: "tts", ctx: 4096}})
	m.shareCursor = 0
	m.enterVoiceBooth()
	if m.mode == modeShareVoice {
		t.Error("an anonymous operator must not open the BOOTH")
	}
	if !strings.Contains(stripANSI(m.status), "log in to earn") {
		t.Errorf("anon BOOTH attempt should show the login gate, got %q", stripANSI(m.status))
	}
	// enterVoiceBooth with no rows is a safe no-op.
	m.shareRows = nil
	m.enterVoiceBooth()
}

// TestBoothHelperFns covers the small parse/format helpers directly at their edges.
func TestBoothHelperFns(t *testing.T) {
	if vbFieldIndex("language") != vbFieldLang || vbFieldIndex("???") != vbFieldName {
		t.Error("vbFieldIndex mapping/fallback")
	}
	if !(model{vbField: vbFieldName}).vbIsTextField() || (model{vbField: vbFieldSpeed}).vbIsTextField() {
		t.Error("vbIsTextField: name is text, speed is not")
	}
	if singleVoiceOf("af_heart:0.7") != "af_heart" || singleVoiceOf("a+b") != "" || singleVoiceOf("") != "" {
		t.Error("singleVoiceOf edges")
	}
	if prefixOf("af_heart") != "af_" || prefixOf("nounderscore") != "" {
		t.Error("prefixOf edges")
	}
	if v, e := parsePrice(""); v != 0 || e != "" {
		t.Error("empty price is free")
	}
	if _, e := parsePrice("-3"); e == "" {
		t.Error("negative price should error")
	}
	if trimLastRune("") != "" || trimLastRune("añ") != "a" {
		t.Error("trimLastRune edges")
	}
	// nextUnusedVoice prefers a same-prefix voice, then any.
	m := vbSeed(t)
	m.vbBlend = []blendVoice{{id: "af_heart", weight: 1}}
	if got := m.nextUnusedVoice(); !strings.HasPrefix(got, "af_") {
		t.Errorf("nextUnusedVoice after af_heart = %q, want an af_ voice", got)
	}
	// clearBlend from an empty blend keeps the single voice.
	m.vbBlend = nil
	m.vbVoice = "am_onyx"
	m.clearBlend()
	if m.vbVoice != "am_onyx" {
		t.Errorf("clearBlend with no blend should keep vbVoice, got %q", m.vbVoice)
	}
	// setBlendFromString single-voice path.
	m.setBlendFromString("af_sky")
	if m.vbVoice != "af_sky" || len(m.vbBlend) != 0 {
		t.Errorf("setBlendFromString single = voice %q blend %d", m.vbVoice, len(m.vbBlend))
	}
	// normalizeBlend on empty is a no-op.
	m.vbBlend = nil
	m.normalizeBlend()
}
