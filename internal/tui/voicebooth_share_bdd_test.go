package tui

// voicebooth_share_bdd_test.go makes features/voicebooth/voice_booth.feature +
// features/voicebooth/voice_picker.feature EXECUTABLE under godog, driving the REAL bubbletea model
// (New/Update/View) so the SHARE VOICE BOOTH editor + the voice picker fail red if they regress.
//
// Real deps, no domain mocks: the picker fetch + audition + the local preview run against a REAL
// httptest LOCAL server; the cross-platform player is stubbed via the injectable audioPlayerFn
// (m.previewPlayer) so no audio device is needed — the SAME seam the consumer voice preview uses.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
)

// bddErr is a readable godog assertion-failure error (the tui package's first godog test defines
// its own — mirrors internal/protocol + internal/agent).
type bddErr string

func (e bddErr) Error() string { return string(e) }
func errExpect(s string) error { return bddErr("expected " + s) }

// --- tiny test-only helpers (JSON shaping + parsing the gherkin lists) ---

func jsonStrArray(ss []string) string {
	b := &strings.Builder{}
	b.WriteByte('[')
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + s + `"`)
	}
	b.WriteByte(']')
	return b.String()
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// parseQuotedList turns a gherkin fragment like `"af_heart", "am_onyx"` into []string.
func parseQuotedList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// pickerContains reports whether the picker's current (possibly filtered) view lists id.
func pickerContains(m model, id string) bool {
	for _, v := range m.visiblePickerVoices() {
		if v == id {
			return true
		}
	}
	return false
}

// blendWeightSum sums the weights in a blend string "a:0.7+b:0.3" (a bare id counts as 1.0).
func blendWeightSum(blend string) float64 {
	if blend == "" {
		return 0
	}
	sum := 0.0
	for _, part := range strings.Split(blend, "+") {
		if i := strings.LastIndex(part, ":"); i >= 0 {
			w, _ := strconv.ParseFloat(strings.TrimSpace(part[i+1:]), 64)
			sum += w
		} else {
			sum += 1.0
		}
	}
	return sum
}

type boothState struct {
	m          model
	local      *httptest.Server
	broker     *httptest.Server // a SENTINEL broker: m.broker points here; any hit fails the wire-honesty Then
	localHits  int32
	lastVoice  string  // voice named in the last LOCAL speech request (audition/preview)
	lastSpeed  float64 // speed in the last LOCAL speech request
	playerGot  []byte  // wav bytes handed to the stub player
	brokerHits int32   // any hit to the sentinel BROKER (must stay 0 for a LOCAL audition/preview)
}

func (s *boothState) reset() {
	if s.local != nil {
		s.local.Close()
	}
	if s.broker != nil {
		s.broker.Close()
	}
	*s = boothState{}
	// A sentinel broker that records ANY request — the model's broker points here (set in newBooth),
	// so the "not relayed through the broker" Then can genuinely FAIL if a future refactor routes a
	// local preview/audition through the broker instead of the operator's own server.
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.brokerHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
}

// --- fresh model helpers ---

// newBooth builds a logged-in model on the SHARE table with the given rows, sized wide.
func (s *boothState) newBooth(rows []shareRow, loggedIn bool) {
	// Point the model's broker at the SENTINEL: a LOCAL preview/audition must NEVER touch it (it
	// uses the operator's own upstream), so brokerHits stays 0 — but the assertion can now fail.
	var tm tea.Model = New(s.broker.URL, "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m := asModel(tm)
	m.loggedIn = loggedIn
	m.mode = modeShare
	m.setShareRows(rows)
	// A stub player records the wav it is handed and reports it played (never touches a device).
	m.previewPlayer = func(wav []byte) (string, bool, error) {
		s.playerGot = append([]byte(nil), wav...)
		return "", true, nil
	}
	s.m = m
}

func (s *boothState) sendKey(k tea.KeyMsg) {
	tm, cmd := s.m.Update(k)
	s.m = asModel(tm)
	s.runCmd(cmd)
}

// runCmd drains a returned Cmd (the local synth runs off the event loop) and folds the resulting
// msg back into the model, so a preview/audition assertion sees the completed round-trip.
func (s *boothState) runCmd(cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return
		}
		tm, next := s.m.Update(msg)
		s.m = asModel(tm)
		cmd = next
	}
}

func (s *boothState) typeRunes(str string) {
	s.sendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(str)})
}

// --- local server ---

func (s *boothState) startLocal(listVoices []string, speechOK bool) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/voices", func(w http.ResponseWriter, r *http.Request) {
		if listVoices == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"voices":` + jsonStrArray(listVoices) + `}`))
	})
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.localHits, 1)
		var body struct {
			Voice string  `json:"voice"`
			Speed float64 `json:"speed"`
		}
		_ = decodeJSON(r, &body)
		s.lastVoice = body.Voice
		s.lastSpeed = body.Speed
		if !speechOK {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("~wav~"))
	})
	s.local = httptest.NewServer(mux)
	// Point every row's upstream at the local server (the BOOTH derives the speech path from it).
	rows := s.m.shareRows
	for i := range rows {
		rows[i].upstream = s.local.URL + "/v1/chat/completions"
	}
	s.m.setShareRows(rows)
}

// ======================= GIVEN =======================

func (s *boothState) onShareTableWithModel(model, modality string) error {
	s.newBooth([]shareRow{{model: model, modality: modality, ctx: 4096}}, true)
	return nil
}
func (s *boothState) loggedInShareModelSelected(model, modality string) error {
	s.newBooth([]shareRow{{model: model, modality: modality, ctx: 4096}}, true)
	s.m.shareCursor = 0
	return nil
}
func (s *boothState) anonShareModelSelected(model, modality string) error {
	s.newBooth([]shareRow{{model: model, modality: modality, ctx: 4096}}, false)
	s.m.shareCursor = 0
	return nil
}
func (s *boothState) boothOpenFor(model string) error {
	s.newBooth([]shareRow{{model: model, modality: "tts", ctx: 4096}}, true)
	s.m.shareCursor = 0
	s.m.enterVoiceBooth()
	return nil
}
func (s *boothState) boothOpenForWithVoice(model, voice string) error {
	if err := s.boothOpenFor(model); err != nil {
		return err
	}
	s.m.vbVoice = voice
	return nil
}
func (s *boothState) boothOpenForWithVoiceSpeed(model, voice string, speed float64) error {
	if err := s.boothOpenForWithVoice(model, voice); err != nil {
		return err
	}
	s.m.vbSpeed = speed
	return nil
}
func (s *boothState) boothOpenForWithBlend(model, blend string) error {
	if err := s.boothOpenFor(model); err != nil {
		return err
	}
	s.m.setBlendFromString(blend)
	return nil
}
func (s *boothState) fieldFocused(name string) error {
	s.m.vbField = vbFieldIndex(name)
	return nil
}
func (s *boothState) localSpeechServerWav() error {
	// Self-sufficient: a scenario may open with this Given (the picker-audition scenarios), so
	// build a BOOTH model first if none exists.
	if s.m.ctrl == nil {
		if err := s.boothOpenForWithVoice("kokoro", "af_heart"); err != nil {
			return err
		}
	}
	s.startLocal(nil, true)
	return nil
}
func (s *boothState) noReachableSpeech() error {
	// Self-sufficient: build a BOOTH model if a scenario opens with this Given.
	if s.m.ctrl == nil {
		if err := s.boothOpenForWithVoice("kokoro", "af_heart"); err != nil {
			return err
		}
	}
	// A closed server URL: unreachable. Point rows at a dead address.
	s.startLocal(nil, true)
	dead := s.local.URL
	s.local.Close()
	s.local = nil
	rows := s.m.shareRows
	for i := range rows {
		rows[i].upstream = dead + "/v1/chat/completions"
	}
	s.m.setShareRows(rows)
	return nil
}
func (s *boothState) pickerOpenAgainstLocal(voices []string) error {
	if s.m.mode != modeShareVoice {
		if err := s.boothOpenFor("kokoro"); err != nil {
			return err
		}
	}
	s.startLocal(voices, true)
	s.m.openVoicePicker()
	s.runCmd(s.m.fetchLocalVoicesCmd())
	return nil
}
func (s *boothState) pickerHighlight(id string) error {
	for i, v := range s.m.vpVoices {
		if v == id {
			s.m.vpCursor = i
			return nil
		}
	}
	return errExpect("highlightable voice " + id)
}

// ======================= WHEN =======================

func (s *boothState) pressP() error               { s.typeRunes("p"); return nil }
func (s *boothState) pressEsc() error             { s.sendKey(tea.KeyMsg{Type: tea.KeyEsc}); return nil }
func (s *boothState) typesName(name string) error { s.typeRunes(name); return nil }
func (s *boothState) setBlendVoice(a string) error {
	s.m.vbVoice = a
	return nil
}
func (s *boothState) addToBlend(id string) error {
	s.m.addBlendVoice(id)
	return nil
}
func (s *boothState) saveBooth() error {
	tm, cmd := s.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(tm)
	s.runCmd(cmd)
	return nil
}
func (s *boothState) clearBlend() error {
	s.m.clearBlend()
	return nil
}
func (s *boothState) nudgeSpeedDownFully() error {
	for i := 0; i < 60; i++ {
		s.m.nudgeSpeed(-1)
	}
	return nil
}
func (s *boothState) nudgeSpeedUpFully() error {
	for i := 0; i < 60; i++ {
		s.m.nudgeSpeed(+1)
	}
	return nil
}
func (s *boothState) setPrice(p string) error {
	s.m.vbPrice = p
	return nil
}
func (s *boothState) playLocalPreview() error {
	s.runCmd(s.m.playVoiceBoothPreview())
	return nil
}
func (s *boothState) auditionHighlighted() error {
	s.runCmd(s.m.auditionPickerVoice())
	return nil
}
func (s *boothState) typeFilter(f string) error {
	for _, r := range f {
		s.sendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return nil
}
func (s *boothState) openPickerNoServer() error {
	if err := s.boothOpenFor("kokoro"); err != nil {
		return err
	}
	s.m.openVoicePicker()
	s.runCmd(s.m.fetchLocalVoicesCmd()) // no local server started -> fetch fails -> bundled fallback
	return nil
}

// ======================= THEN =======================

func (s *boothState) rowTaggedVoice(model, kind string) error {
	out := stripANSI(s.m.shareView(100))
	if !strings.Contains(out, model) {
		return errExpect("row for " + model)
	}
	badge := "tts"
	if kind == "stt" {
		badge = "stt"
	}
	// The row line for `model` must carry the modality tag word.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, model) {
			if !strings.Contains(line, badge) {
				return errExpect(model + " row tagged " + badge + ", got: " + line)
			}
			return nil
		}
	}
	return errExpect("a row line containing " + model)
}
func (s *boothState) rowNotTaggedVoice(model string) error {
	out := stripANSI(s.m.shareView(100))
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, model) {
			if strings.Contains(line, " tts") || strings.Contains(line, " stt") {
				return errExpect(model + " NOT tagged as a voice, got: " + line)
			}
			return nil
		}
	}
	return errExpect("a row line containing " + model)
}
func (s *boothState) tagFolds() error {
	// The badge glyph must have an ASCII fold (♪→>, ▽→v). Assert the raw badge glyph folds.
	if foldRune('♪') == "♪" || foldRune('▽') == "▽" {
		return errExpect("the tts/stt badge glyphs to fold to ASCII")
	}
	return nil
}
func (s *boothState) rowPromptsSetVoice(model string) error {
	out := stripANSI(s.m.shareView(100))
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, model) && strings.Contains(strings.ToLower(line), "set voice") {
			return nil
		}
	}
	return errExpect(model + " row to prompt 'set voice'")
}
func (s *boothState) boothOpened(model string) error {
	if s.m.mode != modeShareVoice {
		return errExpect("mode modeShareVoice, got a different mode")
	}
	if s.m.vbModel != model {
		return errExpect("BOOTH for " + model + ", got " + s.m.vbModel)
	}
	return nil
}
func (s *boothState) priceEditorOpened(model string) error {
	if s.m.mode != modeShareEditor {
		return errExpect("the price+schedule editor (modeShareEditor)")
	}
	if s.m.edModel != model {
		return errExpect("price editor for " + model)
	}
	return nil
}
func (s *boothState) loginGateShown() error {
	if !strings.Contains(stripANSI(s.m.View()), "log in to earn") {
		return errExpect("the login-to-earn gate")
	}
	return nil
}
func (s *boothState) boothNotOpen() error {
	if s.m.mode == modeShareVoice {
		return errExpect("the BOOTH NOT to open")
	}
	return nil
}
func (s *boothState) boothShowsField(label string) error {
	out := stripANSI(s.m.shareVoiceView(100))
	if !strings.Contains(strings.ToLower(out), strings.ToLower(label)) {
		return errExpect("the BOOTH to show a " + label + " field")
	}
	return nil
}
func (s *boothState) nameReads(name string) error {
	if s.m.vbName != name {
		return errExpect("dj name " + name + ", got " + s.m.vbName)
	}
	return nil
}
func (s *boothState) broadcastNames(name string) error {
	if !strings.Contains(stripANSI(s.m.shareVoiceView(100)), name) {
		return errExpect("the broadcast-as line to name " + name)
	}
	return nil
}
func (s *boothState) broadcastFree() error {
	line := boothBroadcastLine(s.m)
	if !strings.Contains(strings.ToUpper(line), "FREE") {
		return errExpect("the broadcast-as line to read FREE, got: " + line)
	}
	return nil
}
func (s *boothState) broadcastPerK() error {
	line := boothBroadcastLine(s.m)
	if !strings.Contains(line, "1k") {
		return errExpect("the broadcast-as line to show a per-1k price, got: " + line)
	}
	return nil
}
func (s *boothState) speedNotBelow(min float64) error {
	if s.m.vbSpeed < min {
		return errExpect("speed >= min")
	}
	return nil
}
func (s *boothState) speedNotAbove(max float64) error {
	if s.m.vbSpeed > max {
		return errExpect("speed <= max")
	}
	return nil
}
func (s *boothState) blendIsWeightedMix(a, b string) error {
	got := s.m.blendString()
	if !strings.Contains(got, a) || !strings.Contains(got, b) {
		return errExpect("a weighted blend of " + a + " and " + b + ", got: " + got)
	}
	return nil
}
func (s *boothState) blendWeightsSumToOne() error {
	vc := s.m.ctrl.VoiceConfigFor(s.m.vbModel)
	sum := blendWeightSum(vc.Voice)
	if sum < 0.999 || sum > 1.001 {
		return errExpect("saved blend weights to sum to 1, got " + vc.Voice)
	}
	return nil
}
func (s *boothState) blendSingleVoice(id string) error {
	if len(s.m.vbBlend) > 1 {
		return errExpect("a single voice, got a blend")
	}
	if s.m.vbVoice != id {
		return errExpect("the single voice " + id + ", got " + s.m.vbVoice)
	}
	return nil
}
func (s *boothState) postToLocalSpeech() error {
	if atomic.LoadInt32(&s.localHits) == 0 {
		return errExpect("a POST to the LOCAL /v1/audio/speech")
	}
	return nil
}
func (s *boothState) requestNamesVoice(v string) error {
	if s.lastVoice != v {
		return errExpect("the request to name voice " + v + ", got " + s.lastVoice)
	}
	return nil
}
func (s *boothState) requestCarriesSpeed(sp float64) error {
	if s.lastSpeed != sp {
		return errExpect("the request to carry speed as set")
	}
	return nil
}
func (s *boothState) notRelayedThroughBroker() error {
	if atomic.LoadInt32(&s.brokerHits) != 0 {
		return errExpect("NO broker relay")
	}
	return nil
}
func (s *boothState) nothingBilled() error {
	if s.m.previewCost != 0 {
		return errExpect("nothing billed for the LOCAL preview")
	}
	return nil
}
func (s *boothState) stubGotWav() error {
	if string(s.playerGot) != "~wav~" {
		return errExpect("the stub player to receive the wav bytes")
	}
	return nil
}
func (s *boothState) boothLocalError() error {
	if !strings.Contains(strings.ToLower(s.m.status+s.m.vbErr), "local") {
		return errExpect("a local-server error surfaced")
	}
	return nil
}
func (s *boothState) boothNoCrash() error { return nil } // reaching here without a panic is the assertion
func (s *boothState) pendingOfferName(model, name string) error {
	if vc := s.m.ctrl.VoiceConfigFor(model); vc.Name != name {
		return errExpect("pending offer name " + name + ", got " + vc.Name)
	}
	return nil
}
func (s *boothState) pendingOfferVoice(model, voice string) error {
	if vc := s.m.ctrl.VoiceConfigFor(model); vc.Voice != voice {
		return errExpect("pending offer voice " + voice + ", got " + vc.Voice)
	}
	return nil
}
func (s *boothState) rowReadyOnAir(model string) error {
	out := stripANSI(s.m.shareView(100))
	// Once configured, the row no longer says "set voice"; it shows a price cell (FREE or $/1k).
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, model) {
			if strings.Contains(strings.ToLower(line), "set voice") {
				return errExpect(model + " to be armed (not 'set voice'), got: " + line)
			}
			return nil
		}
	}
	return errExpect("a row for " + model)
}
func (s *boothState) shareTableShown() error {
	if s.m.mode != modeShare {
		return errExpect("the SHARE table")
	}
	return nil
}
func (s *boothState) rowNotArmed(model string) error {
	if vc := s.m.ctrl.VoiceConfigFor(model); vc.Voice != "" {
		return errExpect(model + " NOT armed with a voice, got " + vc.Voice)
	}
	return nil
}

// --- picker THENs ---

func (s *boothState) pickerLists(id string) error {
	if !pickerContains(s.m, id) {
		return errExpect("the picker to list " + id)
	}
	return nil
}
func (s *boothState) pickerNotLists(id string) error {
	if pickerContains(s.m, id) {
		return errExpect("the picker NOT to list " + id)
	}
	return nil
}
func (s *boothState) pickerGrouped(id, group string) error {
	for _, g := range groupVoices(s.m.vpVoices) {
		if g.label == group {
			for _, v := range g.ids {
				if v == id {
					return nil
				}
			}
		}
	}
	return errExpect(id + " grouped under " + group)
}
func (s *boothState) pickerNotEmpty() error {
	if len(s.m.vpVoices) == 0 {
		return errExpect("the picker to be non-empty")
	}
	return nil
}
func (s *boothState) pickerAtLeast(n int) error {
	if len(s.m.vpVoices) < n {
		return errExpect("the picker to list at least the bundled count")
	}
	return nil
}
func (s *boothState) bundledContains(id string) error {
	for _, v := range bundledKokoroVoices() {
		if v == id {
			return nil
		}
	}
	return errExpect("the bundled list to contain " + id)
}
func (s *boothState) pickerLocalError() error {
	if !strings.Contains(strings.ToLower(s.m.status+s.m.vbErr), "local") {
		return errExpect("a local-server error in the picker")
	}
	return nil
}
func (s *boothState) pickerNoCrash() error { return nil }

// pickerOpenedWithHighlight opens the picker (bundled) and highlights id — for the audition steps.
func (s *boothState) pickerOpenedHighlight(id string) error {
	if err := s.boothOpenForWithVoice("kokoro", id); err != nil {
		return err
	}
	s.startLocal([]string{"af_heart", "am_onyx", "bf_emma"}, true)
	s.m.openVoicePicker()
	s.runCmd(s.m.fetchLocalVoicesCmd())
	return s.pickerHighlight(id)
}
func (s *boothState) pickerOpenedHighlightNoSpeech(id string) error {
	// Picker open, voices from bundled, but NO reachable speech server for the audition.
	if err := s.boothOpenForWithVoice("kokoro", id); err != nil {
		return err
	}
	s.m.openVoicePicker()
	s.m.vpVoices = bundledKokoroVoices()
	if err := s.noReachableSpeech(); err != nil {
		return err
	}
	return s.pickerHighlight(id)
}

// the bundled list step (a pure Given).
func (s *boothState) theBundledList() error { return nil }

func TestVoiceBoothShareBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &boothState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.local != nil {
					st.local.Close()
					st.local = nil
				}
				if st.broker != nil {
					st.broker.Close()
					st.broker = nil
				}
				return ctx, nil
			})
			// GIVEN
			sc.Step(`^the operator is on the SHARE table with a detected "([^"]*)" tts model$`, func(m string) error { return st.onShareTableWithModel(m, "tts") })
			sc.Step(`^the operator is on the SHARE table with a detected "([^"]*)" stt model$`, func(m string) error { return st.onShareTableWithModel(m, "stt") })
			sc.Step(`^the operator is on the SHARE table with a detected "([^"]*)" chat model$`, func(m string) error { return st.onShareTableWithModel(m, "chat") })
			sc.Step(`^a logged-in operator on the SHARE table with a detected "([^"]*)" tts model selected$`, func(m string) error { return st.loggedInShareModelSelected(m, "tts") })
			sc.Step(`^a logged-in operator on the SHARE table with a detected "([^"]*)" chat model selected$`, func(m string) error { return st.loggedInShareModelSelected(m, "chat") })
			sc.Step(`^an anonymous operator on the SHARE table with a detected "([^"]*)" tts model selected$`, func(m string) error { return st.anonShareModelSelected(m, "tts") })
			sc.Step(`^the operator has the VOICE BOOTH open for "([^"]*)" with voice "([^"]*)" and speed ([0-9.]+)$`, st.boothOpenForWithVoiceSpeed)
			sc.Step(`^the operator has the VOICE BOOTH open for "([^"]*)" with voice "([^"]*)"$`, st.boothOpenForWithVoice)
			sc.Step(`^the operator has the VOICE BOOTH open for "([^"]*)" with a blend "([^"]*)"$`, st.boothOpenForWithBlend)
			sc.Step(`^(?:a logged-in operator has|the operator has) the VOICE BOOTH open for "([^"]*)"$`, st.boothOpenFor)
			sc.Step(`^the (dj name|voice|blend|speed|language|price) field is focused$`, st.fieldFocused)
			sc.Step(`^a local speech server that returns wav audio$`, st.localSpeechServerWav)
			sc.Step(`^no reachable local speech server$`, st.noReachableSpeech)
			sc.Step(`^a stub audio player that records what it was handed$`, func() error { return nil }) // set up in newBooth
			sc.Step(`^the operator set the dj name to "([^"]*)" and the voice to "([^"]*)"$`, func(n, v string) error {
				st.m.vbName = n
				st.m.vbVoice = v
				return nil
			})
			// picker GIVENs
			sc.Step(`^a local server that lists voices (.+)$`, func(list string) error { return st.pickerOpenAgainstLocal(parseQuotedList(list)) })
			sc.Step(`^the operator opened the voice picker against that local server$`, func() error { return nil }) // already opened by the lists step
			sc.Step(`^a local server that returns 404 for GET /v1/audio/voices$`, func() error { return st.pickerOpenAgainstLocal(nil) })
			sc.Step(`^no reachable local server$`, func() error { return st.openPickerNoServer() })
			sc.Step(`^the bundled Kokoro voice list$`, st.theBundledList)
			sc.Step(`^the operator opened the voice picker with "([^"]*)" highlighted$`, st.pickerOpenedHighlight)

			// WHEN
			sc.Step(`^the operator presses "p"$`, st.pressP)
			sc.Step(`^the operator presses "esc"$`, st.pressEsc)
			sc.Step(`^the operator types "([^"]*)"$`, st.typesName)
			sc.Step(`^the operator adds "([^"]*)" to the blend$`, st.addToBlend)
			sc.Step(`^the operator saves the BOOTH$`, st.saveBooth)
			sc.Step(`^the operator clears the blend$`, st.clearBlend)
			sc.Step(`^the operator nudges the speed all the way down$`, st.nudgeSpeedDownFully)
			sc.Step(`^the operator nudges the speed all the way up$`, st.nudgeSpeedUpFully)
			sc.Step(`^the operator sets the price to "([^"]*)"$`, st.setPrice)
			sc.Step(`^the operator plays the local preview$`, st.playLocalPreview)
			sc.Step(`^the operator opens the voice picker against that local server$`, func() error { return nil })
			sc.Step(`^the operator opens the voice picker$`, func() error { return nil })
			sc.Step(`^the operator types "([^"]*)" into the picker filter$`, st.typeFilter)
			sc.Step(`^the operator auditions the highlighted voice$`, st.auditionHighlighted)

			// THEN
			sc.Step(`^the SHARE row for "([^"]*)" is tagged as a tts voice$`, func(m string) error { return st.rowTaggedVoice(m, "tts") })
			sc.Step(`^the SHARE row for "([^"]*)" is tagged as an stt voice$`, func(m string) error { return st.rowTaggedVoice(m, "stt") })
			sc.Step(`^the SHARE row for "([^"]*)" is not tagged as a voice$`, st.rowNotTaggedVoice)
			sc.Step(`^the tag glyph folds to ASCII on a legacy console$`, st.tagFolds)
			sc.Step(`^the SHARE row for "([^"]*)" prompts the operator to set a voice$`, st.rowPromptsSetVoice)
			sc.Step(`^the VOICE BOOTH editor opens for "([^"]*)"$`, st.boothOpened)
			sc.Step(`^the price and schedule editor opens for "([^"]*)"$`, st.priceEditorOpened)
			sc.Step(`^the login-to-earn gate is shown$`, st.loginGateShown)
			sc.Step(`^the VOICE BOOTH does not open$`, st.boothNotOpen)
			sc.Step(`^the BOOTH shows a dj name field$`, func() error { return st.boothShowsField("dj name") })
			sc.Step(`^the BOOTH shows a voice field$`, func() error { return st.boothShowsField("voice") })
			sc.Step(`^the BOOTH shows a blend field$`, func() error { return st.boothShowsField("blend") })
			sc.Step(`^the BOOTH shows a speed field$`, func() error { return st.boothShowsField("speed") })
			sc.Step(`^the BOOTH shows a language field$`, func() error { return st.boothShowsField("language") })
			sc.Step(`^the BOOTH shows a price field$`, func() error { return st.boothShowsField("price") })
			sc.Step(`^the dj name reads "([^"]*)"$`, st.nameReads)
			sc.Step(`^the live broadcast-as line names "([^"]*)"$`, st.broadcastNames)
			sc.Step(`^the live broadcast-as line reads FREE$`, st.broadcastFree)
			sc.Step(`^the live broadcast-as line shows the per-1k-chars price$`, st.broadcastPerK)
			sc.Step(`^the speed is not below ([0-9.]+)$`, st.speedNotBelow)
			sc.Step(`^the speed is not above ([0-9.]+)$`, st.speedNotAbove)
			sc.Step(`^the blend reads a weighted mix of "([^"]*)" and "([^"]*)"$`, st.blendIsWeightedMix)
			sc.Step(`^the saved blend weights sum to 1$`, st.blendWeightsSumToOne)
			sc.Step(`^the blend is a single voice "([^"]*)"$`, st.blendSingleVoice)
			sc.Step(`^a POST is made to the local server's /v1/audio/speech$`, st.postToLocalSpeech)
			sc.Step(`^the preview request names voice "([^"]*)"$`, st.requestNamesVoice)
			sc.Step(`^the audition request names voice "([^"]*)"$`, st.requestNamesVoice)
			sc.Step(`^the preview request carries speed ([0-9.]+)$`, st.requestCarriesSpeed)
			sc.Step(`^the preview is not relayed through the broker$`, st.notRelayedThroughBroker)
			sc.Step(`^the audition is not relayed through the broker$`, st.notRelayedThroughBroker)
			sc.Step(`^nothing is billed for the preview$`, st.nothingBilled)
			sc.Step(`^the stub player received the wav bytes$`, st.stubGotWav)
			sc.Step(`^the BOOTH shows a local-server error$`, st.boothLocalError)
			sc.Step(`^the BOOTH does not crash$`, st.boothNoCrash)
			sc.Step(`^the pending offer for "([^"]*)" has name "([^"]*)"$`, st.pendingOfferName)
			sc.Step(`^the pending offer for "([^"]*)" has default voice "([^"]*)"$`, st.pendingOfferVoice)
			sc.Step(`^the SHARE table shows "([^"]*)" as ready to go on air$`, st.rowReadyOnAir)
			sc.Step(`^the SHARE table is shown$`, st.shareTableShown)
			sc.Step(`^"([^"]*)" is not armed with a voice$`, st.rowNotArmed)
			// picker THENs
			sc.Step(`^the picker lists "([^"]*)"$`, st.pickerLists)
			sc.Step(`^the picker does not list "([^"]*)"$`, st.pickerNotLists)
			sc.Step(`^"([^"]*)" is grouped under "([^"]*)"$`, st.pickerGrouped)
			sc.Step(`^the picker is not empty$`, st.pickerNotEmpty)
			sc.Step(`^the picker lists at least (\d+) voices$`, st.pickerAtLeast)
			sc.Step(`^it contains "([^"]*)"$`, st.bundledContains)
			sc.Step(`^the picker shows a local-server error$`, st.pickerLocalError)
			sc.Step(`^the picker does not crash$`, st.pickerNoCrash)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voicebooth/voice_booth.feature", "../../features/voicebooth/voice_picker.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("voicebooth share behavior scenarios failed (see godog output above)")
	}
}
