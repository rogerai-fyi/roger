package tui

// voicebooth_test.go is the executable spec for the CONSUMER voice UX per the approved critic
// DELTA: LLM bands are THE product, and voice is a DIM, OPT-IN FOOTNOTE that drills into a CHILD
// screen — never a co-equal section, never on the dial, never the default landing.
//
// The invariants under test (each tied to a DELTA bullet):
//   - visibleBands (the top-level list) is LLM (chat) bands ONLY — a voice band never sits in it.
//   - browseView carries a single dim "also on air: N voices ▸ [v]" footnote IFF ≥1 voice is on
//     air; on a pure-LLM / empty-voice screen the footnote is ABSENT (zero voice affordance).
//   - `v` (and selecting the footnote) enters THE DJ BOOTH child screen; esc returns to THE BAND.
//   - The default landing is THE BAND (modeBrowse), at launch and across [1]/section switches.
//   - The Booth lists tts bands as DJs; its `▸ N transcribers` line drills into THE LISTENING
//     POST (stt info, no preview, no chat).
//   - The verb is "cue" (never "hire"/"tune in") for opening a voice endpoint; "spin" stays for
//     the ▶ sample preview.
//   - The modality badge is mono ▽ (stt, folds to v) / ♪ (tts) — NEVER the color emoji 🎤, and
//     never a rune with no ASCII fold, anywhere in the badge path.
//   - Compact/windowshade shows LLM band deck cells only (no voice cells); voices fold into the
//     header count.
//   - The money gate is REACHED FROM the Booth (cue a DJ) and still holds (default DENY, exactly
//     one POST after confirm) — the a9749755 guarantees are preserved.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/rogerai-fyi/roger/internal/glyphs"
)

// foldRune returns the ASCII fold of a single rune (glyphs.Fold under a forced-ASCII env), so a
// badge glyph can be asserted to degrade to plain ASCII on a legacy console.
func foldRune(r rune) string {
	old, had := os.LookupEnv("ROGERAI_ASCII")
	os.Setenv("ROGERAI_ASCII", "1")
	defer func() {
		if had {
			os.Setenv("ROGERAI_ASCII", old)
		} else {
			os.Unsetenv("ROGERAI_ASCII")
		}
	}()
	return glyphs.Fold(string(r))
}

// --- LLM primacy: voices are OUT of the top-level list -------------------------

// The top-level band list (visibleBands) is the LLM bands ONLY — a voice (tts/stt) band never
// appears inline, even one whose signal/price would otherwise top the sort. Voice lives one
// drill-in deeper (the Booth), so THE BAND stays pure LLM.
func TestVisibleBandsExcludesVoices(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// Hand a voice band a table-topping signal + the cheapest price; it must STILL be absent.
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
			t.Fatalf("voice band %q must NOT appear in the top-level LLM list", b.model)
		}
	}
	// And a chat band is still there (the list is not simply empty).
	sawChat := false
	for _, b := range m.visibleBands() {
		if !b.isVoice() {
			sawChat = true
		}
	}
	if !sawChat {
		t.Fatal("the top-level list should still show the LLM (chat) bands")
	}
}

// voiceBandsOnAir counts the on-air voice bands (drives the conditional footnote).
func TestVoiceBandsOnAirCount(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// The seed has one paid tts + one free tts on air (2), plus an on-air stt. The footnote
	// count is VOICES on air (tts+stt), since the Booth surfaces both (stt via its sub-line).
	if got := m.voiceBandsOnAir(); got != 3 {
		t.Fatalf("voiceBandsOnAir() = %d, want 3 (2 tts + 1 stt on air)", got)
	}
	// Take every voice band offline: none on air.
	for i := range m.bands {
		if m.bands[i].isVoice() {
			m.bands[i].online = false
		}
	}
	if got := m.voiceBandsOnAir(); got != 0 {
		t.Fatalf("with all voices offline, voiceBandsOnAir() = %d, want 0", got)
	}
}

// The top-level "N bands" headline counts LLM (chat) bands ONLY — it must NOT include the
// voice bands (which are excluded from the list and surfaced via the footnote). Otherwise the
// number above the list disagrees with the rows shown. Same for the on-air station count.
func TestHeadlineCountsExcludeVoices(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// The seed: 1 chat band + 3 voice bands (2 tts + 1 stt), all on air. The headline should
	// read the LLM bands only.
	chat := 0
	for _, b := range m.bands {
		if !b.isVoice() {
			chat++
		}
	}
	if chat != 1 {
		t.Fatalf("precondition: seed should have exactly 1 chat band, got %d", chat)
	}
	out := stripANSI(m.browseView(100))
	if !strings.Contains(out, "1 band") {
		t.Errorf("the headline should count the LLM bands only (1), not include voices; got:\n%s", out)
	}
	if strings.Contains(out, "4 bands") {
		t.Errorf("the headline must NOT count the 3 voice bands into 'models on air'; got:\n%s", out)
	}
	// The persistent ambient status ("N bands · M on air") also excludes voices.
	amb := stripANSI(m.ambientStatus())
	if strings.Contains(amb, "4 band") {
		t.Errorf("ambientStatus must not count voice bands; got: %q", amb)
	}
}

// The compact header's band count also excludes voices (the deck shows LLM bands only).
func TestCompactHeaderCountExcludesVoices(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	m.compact = true
	hdr := stripANSI(m.compactHeader(120))
	// 1 chat band on air; the "N bands" figure must be the LLM count, not 4.
	if strings.Contains(hdr, "4 bands") {
		t.Errorf("compact header must not count voice bands into 'N bands'; got: %q", hdr)
	}
}

// --- The footnote is DIM + CONDITIONAL ----------------------------------------

// With ≥1 voice on air, THE BAND carries a single "also on air: N voices ▸ [v]" footnote.
func TestBrowseViewShowsVoiceFootnoteWhenPresent(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	out := m.browseView(100)
	if !strings.Contains(out, "also on air") {
		t.Errorf("browse view should carry the 'also on air: N voices' footnote; got:\n%s", out)
	}
	if !strings.Contains(out, "voices") {
		t.Errorf("the footnote should count voices; got:\n%s", out)
	}
	// It must NOT reintroduce the old VOICES divider-section header.
	if strings.Contains(strings.ToUpper(out), "▌ VOICES") || strings.Contains(out, "speak · 🎤") {
		t.Errorf("the old VOICES divider-section must be gone; got:\n%s", out)
	}
}

// EMPTY-STATE / pure-LLM: with NO voice on air, there is ZERO voice affordance — no footnote,
// no "voices" mention at all. Voice is invisible until a real voice band appears.
func TestBrowseViewNoVoiceFootnoteWhenEmpty(t *testing.T) {
	// A browse model with ONLY chat bands.
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	tm, _ = tm.Update(offersMsg{
		{NodeID: "c1", Model: "gpt-oss-20b", Modality: "chat", PriceIn: 0.2, PriceOut: 0.3, Online: true, TPS: 60},
		{NodeID: "c2", Model: "llama-3.3-70b", Modality: "chat", PriceIn: 0.2, PriceOut: 0.6, Online: true, TPS: 40},
	})
	tm, _ = tm.Update(balanceMsg{balance: 10, loggedIn: true})
	tm, _ = tm.Update(tickMsg{})
	m := tm.(model)
	out := m.browseView(100)
	if strings.Contains(strings.ToLower(out), "also on air") || strings.Contains(strings.ToLower(out), "voice") {
		t.Errorf("a pure-LLM screen must show NO voice affordance at all; got:\n%s", out)
	}
}

// The footnote drops when every voice band goes OFFLINE (only on-air voices count).
func TestVoiceFootnoteGoneWhenAllVoicesOffline(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	for i := range m.bands {
		if m.bands[i].isVoice() {
			m.bands[i].online = false
		}
	}
	out := m.browseView(100)
	if strings.Contains(strings.ToLower(out), "also on air") {
		t.Errorf("with all voices offline the footnote must be absent; got:\n%s", out)
	}
}

// A single voice on air reads "1 voice" (singular), not "1 voices".
func TestVoiceFootnoteSingularPlural(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// Leave exactly one voice on air.
	seenOne := false
	for i := range m.bands {
		if m.bands[i].isVoice() {
			if !seenOne {
				seenOne = true
				m.bands[i].online = true
			} else {
				m.bands[i].online = false
			}
		}
	}
	line := stripANSI(m.voiceFootnote())
	if !strings.Contains(line, "1 voice") {
		t.Errorf("one voice should read '1 voice', got: %q", line)
	}
	if strings.Contains(line, "voices") {
		t.Errorf("one voice should read singular 'voice', not 'voices', got: %q", line)
	}
}

// --- The footnote / v DRILLS INTO the Booth (child screen) ---------------------

// `v` from THE BAND enters the DJ BOOTH child screen.
func TestVKeyEntersBoothFromBand(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	if m.mode != modeBrowse {
		t.Fatalf("precondition: should start in modeBrowse, got %d", m.mode)
	}
	tm, _ := m.Update(keyMsg("v"))
	got := asModel(tm)
	if got.mode != modeVoiceBooth {
		t.Fatalf("v should enter the DJ BOOTH (modeVoiceBooth), got mode %d", got.mode)
	}
}

// `v` with NO voice on air is a NO-OP (there is nothing to drill into; the affordance is absent).
func TestVKeyNoopWhenNoVoices(t *testing.T) {
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	tm, _ = tm.Update(offersMsg{{NodeID: "c1", Model: "gpt-oss-20b", Modality: "chat", PriceIn: 0.2, PriceOut: 0.3, Online: true}})
	tm, _ = tm.Update(tickMsg{})
	tm, _ = tm.Update(keyMsg("v"))
	if asModel(tm).mode != modeBrowse {
		t.Fatalf("v with no voices should stay in modeBrowse, got %d", asModel(tm).mode)
	}
}

// esc from the Booth returns to THE BAND (the parent LLM list).
func TestBoothEscReturnsToBand(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	if m.mode != modeVoiceBooth {
		t.Fatalf("precondition: expected the Booth, got %d", m.mode)
	}
	tm, _ = m.Update(keyMsg("esc"))
	if asModel(tm).mode != modeBrowse {
		t.Fatalf("esc from the Booth should return to THE BAND (modeBrowse), got %d", asModel(tm).mode)
	}
}

// --- Default landing is ALWAYS the LLM bands ----------------------------------

// A fresh model lands on modeBrowse (THE BAND), never the Booth.
func TestDefaultLandingIsTheBand(t *testing.T) {
	m := New("http://broker.local", "tester")
	if m.mode != modeBrowse {
		t.Fatalf("launch should land on THE BAND (modeBrowse), got mode %d", m.mode)
	}
}

// [1] TUNE IN from the Booth returns to THE BAND (never re-lands on voices).
func TestPreset1FromBoothLandsOnBand(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v")) // into the Booth
	m = asModel(tm)
	if m.mode != modeVoiceBooth {
		t.Fatalf("precondition: expected the Booth, got %d", m.mode)
	}
	tm, _ = m.Update(keyMsg("1")) // TUNE IN
	if asModel(tm).mode != modeBrowse {
		t.Fatalf("[1] from the Booth should land on THE BAND, got mode %d", asModel(tm).mode)
	}
}

// The section badge stays the two-word "TUNE IN [s]" while in the Booth — voice never appears in
// the top-level section indicator (it is a child screen, not a peer section).
func TestBoothSectionBadgeStaysTuneIn(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	badge := stripANSI(m.sectionBadge())
	if !strings.Contains(badge, "TUNE IN") {
		t.Errorf("the section badge in the Booth should still read TUNE IN, got %q", badge)
	}
	if strings.Contains(strings.ToUpper(badge), "BOOTH") || strings.Contains(strings.ToUpper(badge), "VOICE") || strings.Contains(strings.ToUpper(badge), "DJ") {
		t.Errorf("voice must not surface in the top-level section badge, got %q", badge)
	}
}

// --- The Booth is the DJ lineup (tts); STT drills further to the Listening Post -

// The Booth view lists on-air tts bands as DJs and carries a `▸ N transcribers` drill-line for stt.
func TestBoothViewListsDJsAndTranscriberLine(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	out := m.voiceBoothView(100)
	up := strings.ToUpper(out)
	if !strings.Contains(up, "BOOTH") {
		t.Errorf("the Booth should title itself THE DJ BOOTH; got:\n%s", out)
	}
	// The tts DJ names appear.
	if !strings.Contains(out, "eager-puma-54-voice") || !strings.Contains(out, "free-crier-voice") {
		t.Errorf("the Booth should list the on-air tts DJs; got:\n%s", out)
	}
	// A chat band must NOT appear in the Booth.
	if strings.Contains(out, "gpt-oss-20b") {
		t.Errorf("a chat band must never appear in the DJ BOOTH; got:\n%s", out)
	}
	// The stt drill-line (transcribers -> the Listening Post).
	if !strings.Contains(strings.ToLower(out), "transcriber") {
		t.Errorf("the Booth should carry a '▸ N transcribers' line into the Listening Post; got:\n%s", out)
	}
}

// The Booth verb is "cue" (never "hire"/"tune in"); the sample verb "spin" is present for ▶.
func TestBoothVerbIsCueNotHire(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	out := strings.ToLower(m.voiceBoothView(100)) + " " + strings.ToLower(stripANSI(m.footer(100)))
	if !strings.Contains(out, "cue") {
		t.Errorf("the Booth should use the verb 'cue'; got:\n%s", out)
	}
	if strings.Contains(out, "hire") {
		t.Errorf("the verb 'hire' must be gone from the Booth; got:\n%s", out)
	}
	if !strings.Contains(out, "spin") {
		t.Errorf("the ▶ sample verb 'spin' should still be present; got:\n%s", out)
	}
}

// The Listening Post is reached from the Booth (its transcriber drill-line), is INFO-only (no
// preview, no chat), and esc returns to the Booth.
func TestListeningPostFromBoothIsInfoOnly(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v")) // Booth
	m = asModel(tm)
	// The drill key into the Listening Post.
	tm, _ = m.Update(keyMsg("t"))
	m = asModel(tm)
	if m.mode != modeListeningPost {
		t.Fatalf("the transcriber line should drill into the Listening Post (modeListeningPost), got %d", m.mode)
	}
	out := strings.ToLower(m.listeningPostView(100))
	if !strings.Contains(out, "audio") || !strings.Contains(out, "transcri") {
		t.Errorf("the Listening Post should explain audio->text transcription; got:\n%s", out)
	}
	if strings.Contains(out, "chat") && !strings.Contains(out, "not") {
		t.Errorf("the Listening Post must not offer a chat; got:\n%s", out)
	}
	if m.connected != nil {
		t.Fatal("the Listening Post must not open a chat channel")
	}
	// esc returns to the Booth (the parent of this drill).
	tm, _ = m.Update(keyMsg("esc"))
	if asModel(tm).mode != modeVoiceBooth {
		t.Fatalf("esc from the Listening Post should return to the Booth, got %d", asModel(tm).mode)
	}
}

// --- Badge is MONO (no color emoji, ASCII-foldable) ---------------------------

// voiceBadge is mono: ♪ for tts, ▽ for stt — and NEVER the color emoji 🎤. Every rune it can
// emit must have an ASCII fold (glyphs.Fold turns it into a plain-ASCII, non-emoji token).
func TestVoiceBadgeIsMonoNoEmoji(t *testing.T) {
	tts := voiceBadge(band{modality: "tts"})
	stt := voiceBadge(band{modality: "stt"})
	if tts != "♪" {
		t.Errorf("tts badge should be ♪, got %q", tts)
	}
	if stt != "▽" {
		t.Errorf("stt badge should be the mono ▽ (folds to v), got %q", stt)
	}
	for _, badge := range []string{tts, stt} {
		if strings.Contains(badge, "🎤") {
			t.Fatalf("the color emoji 🎤 must be gone from the badge, got %q", badge)
		}
		// Every rune must fold to pure ASCII (no emoji / non-ASCII survives glyphs.Fold).
		for _, r := range badge {
			folded := foldRune(r)
			for _, fr := range folded {
				if fr > unicode.MaxASCII {
					t.Fatalf("badge rune %q folds to non-ASCII %q — every glyph needs an ASCII fold", string(r), folded)
				}
			}
		}
	}
	// stt folds to a plain "v" (the footnote key), tts to ">" (the established ♪ fold).
	if got := foldRune('▽'); got != "v" {
		t.Errorf("▽ should fold to v, got %q", got)
	}
}

// The Booth rows and footnote never emit the color emoji anywhere.
func TestBoothAndFootnoteNoEmoji(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	foot := m.voiceFootnote()
	tm, _ := m.Update(keyMsg("v"))
	booth := asModel(tm).voiceBoothView(100)
	for name, s := range map[string]string{"footnote": foot, "booth": booth} {
		if strings.Contains(s, "🎤") {
			t.Errorf("%s must not contain the color emoji 🎤; got:\n%s", name, s)
		}
	}
}

// --- Red restraint: the ♪/▽ modality badge is NOT red -------------------------

// stBadge is the SINGLE style for the ♪/▽ modality badge, and it must NOT resolve to the reserved
// on-air/verified red (cRed). The TUI reserves red for the ◉ on-air beacon + ✓/◆ verified marks —
// red = a live/verified SIGNAL, never model KIND. The badge marks kind, so it renders dim, not red.
// (Mirrors pingworld_color_test.go's TestToneStyleNeverRed: the one-red law is locked at the STYLE
// level via GetForeground, not by inspecting rendered ANSI — which strips to plain off a TTY.)
func TestModalityBadgeStyleNeverRed(t *testing.T) {
	if got := stBadge.GetForeground(); got == cRed {
		t.Fatalf("the modality badge style resolves to the reserved on-air RED (cRed) — red is for the ◉ beacon / ✓◆ verified marks, never model KIND")
	}
	// It must also NOT be bold-red by any other route: the badge is a quiet dim mark.
	if stBadge.GetForeground() == stGold.GetForeground() {
		t.Fatalf("the badge must not share stGold's red foreground — stGold stays for the verified/peak glint")
	}
	// Pin the chosen restraint: the badge is the dim ink (stDim), the quietest label color.
	if stBadge.GetForeground() != stDim.GetForeground() {
		t.Errorf("the modality badge should render in stDim (dim ink), got foreground %v", stBadge.GetForeground())
	}
}

// Every badge RENDER SITE emits the badge NOT wrapped in the reserved red glint: the consumer DJ
// BOOTH rows + the Listening Post rows (voice.go). This is the render-path belt to the style-level
// lock above. We force a TrueColor profile so lipgloss actually emits SGR (off a TTY every style
// strips to plain), then assert the rendered view does NOT contain the badge glyph wrapped in the
// red stGold styling, and DOES contain it wrapped in the non-red stBadge styling. The comparison is
// self-computing (stGold.Render(glyph) IS the red-wrapped token) so it needs no hard-coded escape.
func TestBadgeRenderSitesNotRed(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	old := r.ColorProfile()
	r.SetColorProfile(termenv.TrueColor) // emit SGR so red is distinguishable from dim
	defer r.SetColorProfile(old)

	m := voiceSeed(t, "http://broker.local")
	// DJ BOOTH (tts DJ rows) and the Listening Post (stt rows) both render a modality badge.
	tm, _ := m.Update(keyMsg("v"))
	bm := asModel(tm)
	booth := bm.voiceBoothView(100)
	tm, _ = bm.Update(keyMsg("t"))
	post := asModel(tm).listeningPostView(100)

	ttsGlyph := voiceBadgeGlyph(band{modality: "tts", online: true})
	sttGlyph := voiceBadgeGlyph(band{modality: "stt", online: true})
	for _, c := range []struct {
		name, view, glyph string
	}{
		{"DJ BOOTH", booth, ttsGlyph},
		{"LISTENING POST", post, sttGlyph},
	} {
		if red := stGold.Render(c.glyph); strings.Contains(c.view, red) {
			t.Errorf("%s renders the modality badge in reserved RED (stGold) — red is for the ◉ beacon / verified marks only:\n%s", c.name, c.view)
		}
		if quiet := stBadge.Render(c.glyph); !strings.Contains(c.view, quiet) {
			t.Errorf("%s should render the modality badge via the non-red stBadge style; not found in:\n%s", c.name, c.view)
		}
	}
}

// --- Compact/windowshade shows NO voice deck cells ----------------------------

// The compact band deck lists on-air LLM bands only — no voice cells take a slot.
func TestCompactDeckNoVoiceCells(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	m.compact = true
	// visibleBands (which the compact deck renders) must carry no voice band.
	for _, b := range m.visibleBands() {
		if b.isVoice() {
			t.Fatalf("the compact deck must not include voice band %q", b.model)
		}
	}
	deck := m.compactBandList(100, m.visibleBands(), len(m.bands))
	if strings.Contains(deck, "eager-puma-54-voice") || strings.Contains(deck, "whisper-listener") {
		t.Errorf("no voice band should occupy a compact deck cell; got:\n%s", deck)
	}
}

// --- The MONEY GATE still holds, reached from the Booth ------------------------

// Cueing a FREE DJ from the Booth opens the preview and fires the synth — never a chat channel.
func TestBoothCueFreeDJOpensPreview(t *testing.T) {
	var speechHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" {
			atomic.AddInt32(&speechHits, 1)
			w.Header().Set("X-RogerAI-Cost", "0")
			w.Write([]byte("RIFFwav"))
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			t.Errorf("cueing a DJ must never hit the chat relay")
		}
	}))
	defer srv.Close()
	m := voiceSeed(t, srv.URL)
	m.previewPlayer = func([]byte) (string, bool, error) { return "", true, nil }
	tm, _ := m.Update(keyMsg("v")) // Booth
	m = asModel(tm)
	selectDJByModel(t, &m, "free-crier-voice")
	tm, cmd := m.Update(keyMsg("enter")) // cue
	m = asModel(tm)
	if m.mode != modeVoicePreview {
		t.Fatalf("cueing a free DJ should open modeVoicePreview, got %d", m.mode)
	}
	if m.connected != nil {
		t.Fatal("cueing a DJ must not open a chat channel")
	}
	if cmd == nil {
		t.Fatal("a FREE DJ cue should fire the synth Cmd")
	}
	if _, ok := cmd().(voicePreviewMsg); !ok {
		t.Fatal("the synth Cmd should yield a voicePreviewMsg")
	}
	if atomic.LoadInt32(&speechHits) != 1 {
		t.Fatalf("a free DJ cue should POST /v1/audio/speech exactly once, got %d", speechHits)
	}
}

// Cueing a PAID DJ from the Booth holds at the confirm gate and does NOT spend before confirm;
// exactly one POST after confirm (the a9749755 money gate, preserved through the Booth entry).
func TestBoothCuePaidDJMoneyGate(t *testing.T) {
	var speechHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" {
			atomic.AddInt32(&speechHits, 1)
			w.Header().Set("X-RogerAI-Cost", "0.000228")
			w.Write([]byte("RIFFwav"))
		}
	}))
	defer srv.Close()
	m := voiceSeed(t, srv.URL)
	m.previewPlayer = func([]byte) (string, bool, error) { return "", true, nil }
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	selectDJByModel(t, &m, "eager-puma-54-voice")
	tm, cmd := m.Update(keyMsg("enter")) // cue (paid)
	m = asModel(tm)
	if m.mode != modeVoicePreview || !m.previewNeedsConfirm() {
		t.Fatalf("cueing a PAID DJ must hold at the confirm gate (mode %d, needsConfirm %v)", m.mode, m.previewNeedsConfirm())
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m.Update(msg)
		}
	}
	if atomic.LoadInt32(&speechHits) != 0 {
		t.Fatalf("a PAID DJ must NOT spend before confirm; /v1/audio/speech hit %d", speechHits)
	}
	// Confirm: exactly one POST.
	tm, cmd = m.onVoicePreviewKey(keyMsg("y"))
	if cmd == nil {
		t.Fatal("confirming should fire the synth Cmd")
	}
	cmd()
	if atomic.LoadInt32(&speechHits) != 1 {
		t.Fatalf("after confirm, exactly one POST, got %d", speechHits)
	}
}

// --- Booth navigation + rendering branches (exhaustive) -----------------------

// ↑/↓ move the Booth cursor and clamp at both ends; the cursor is what enter/spin acts on.
func TestBoothCursorNavClamps(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	n := len(m.boothDJs())
	if n < 2 {
		t.Fatalf("need ≥2 DJs to test nav, got %d", n)
	}
	// Up at the top is a clamp (stays 0).
	tm, _ = m.Update(keyMsg("up"))
	if asModel(tm).boothCursor != 0 {
		t.Fatalf("up at top should clamp to 0, got %d", asModel(tm).boothCursor)
	}
	// Down moves; down past the end clamps to n-1.
	m = asModel(tm)
	for i := 0; i < n+3; i++ {
		tm, _ = m.Update(keyMsg("down"))
		m = asModel(tm)
	}
	if m.boothCursor != n-1 {
		t.Fatalf("down past the end should clamp to %d, got %d", n-1, m.boothCursor)
	}
	// Back up one.
	tm, _ = m.Update(keyMsg("up"))
	if asModel(tm).boothCursor != n-2 {
		t.Fatalf("up should move to %d, got %d", n-2, asModel(tm).boothCursor)
	}
}

// ▶ / space "spins" a sample — the same money-gated preview entry as enter/cue (spin=sample,
// cue=endpoint). A FREE DJ plays now; a PAID DJ still holds at the confirm gate.
func TestBoothSpinOpensPreview(t *testing.T) {
	for _, key := range []string{" ", "▶", "p"} {
		m := voiceSeed(t, "http://broker.local")
		m.previewPlayer = func([]byte) (string, bool, error) { return "", true, nil }
		tm, _ := m.Update(keyMsg("v"))
		m = asModel(tm)
		selectDJByModel(t, &m, "eager-puma-54-voice") // paid → confirm gate
		tm, _ = m.Update(keyMsg(key))
		got := asModel(tm)
		if got.mode != modeVoicePreview {
			t.Fatalf("spin key %q should open the preview, got mode %d", key, got.mode)
		}
		if !got.previewNeedsConfirm() {
			t.Fatalf("spin key %q on a PAID DJ should hold at the confirm gate", key)
		}
	}
}

// The Booth renders in a narrow terminal (the slim dj·on air·$/1k ch grid) without panic, and the
// selected row is drawn (reverse-video path). Both the wide and narrow selected paths are covered.
func TestBoothViewNarrowAndSelectedRows(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	m.boothCursor = 0 // select the first DJ (exercises the selected-row branch)
	narrow := m.voiceBoothView(50)
	if !strings.Contains(narrow, "$/1k ch") {
		t.Errorf("narrow Booth should still show the $/1k ch column; got:\n%s", narrow)
	}
	wide := m.voiceBoothView(120)
	if !strings.Contains(strings.ToLower(wide), "lang") || !strings.Contains(strings.ToLower(wide), "sample") {
		t.Errorf("wide Booth should show the lang + sample columns; got:\n%s", wide)
	}
	// A FREE DJ reads FREE in the price cell (both widths).
	if !strings.Contains(narrow, "FREE") {
		t.Errorf("the free DJ should read FREE in the Booth; got:\n%s", narrow)
	}
}

// With no DJ on air, the Booth reads "no DJs on air" and cueing/nav is a safe no-op.
func TestBoothEmptyStateNoDJs(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// Take the tts DJs offline but keep an stt transcriber on air (so the Booth is still reachable
	// via voiceBandsOnAir > 0, but boothDJs is empty).
	for i := range m.bands {
		if m.bands[i].isTTS() {
			m.bands[i].online = false
		}
	}
	tm, _ := m.enterBooth()
	m = asModel(tm)
	if m.mode != modeVoiceBooth {
		t.Fatalf("the Booth should still open when only a transcriber is on air, got %d", m.mode)
	}
	out := m.voiceBoothView(100)
	if !strings.Contains(strings.ToLower(out), "no djs on air") {
		t.Errorf("an empty Booth should say 'no DJs on air'; got:\n%s", out)
	}
	// enter/nav on an empty Booth are no-ops (no panic, stays in the Booth).
	tm, cmd := m.Update(keyMsg("enter"))
	if asModel(tm).mode != modeVoiceBooth || cmd != nil {
		t.Errorf("enter on an empty Booth should be a no-op, got mode %d", asModel(tm).mode)
	}
	tm, _ = m.Update(keyMsg("down"))
	if asModel(tm).boothCursor != 0 {
		t.Errorf("down on an empty Booth should stay at 0, got %d", asModel(tm).boothCursor)
	}
}

// `t` with NO transcriber on air is a no-op (stays in the Booth) — the drill-line is absent then.
func TestBoothTKeyNoopWhenNoTranscribers(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	for i := range m.bands {
		if m.bands[i].isSTT() {
			m.bands[i].online = false
		}
	}
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	// The transcriber drill-line must be absent.
	if strings.Contains(strings.ToLower(m.voiceBoothView(100)), "transcriber") {
		t.Errorf("with no transcriber on air the drill-line must be absent; got:\n%s", m.voiceBoothView(100))
	}
	tm, _ = m.Update(keyMsg("t"))
	if asModel(tm).mode != modeVoiceBooth {
		t.Fatalf("t with no transcribers should stay in the Booth, got %d", asModel(tm).mode)
	}
}

// A non-action key in the Booth is a harmless no-op.
func TestBoothUnknownKeyNoop(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	tm, cmd := m.Update(keyMsg("x"))
	if asModel(tm).mode != modeVoiceBooth || cmd != nil {
		t.Errorf("an unrelated key in the Booth should be a no-op, got mode %d", asModel(tm).mode)
	}
}

// The Listening Post: [1] jumps to THE BAND; an unrelated key is a no-op; the footer is info-only.
func TestListeningPostKeysAndFooter(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	tm, _ = m.Update(keyMsg("t")) // into the Post
	m = asModel(tm)
	if m.mode != modeListeningPost {
		t.Fatalf("precondition: expected the Listening Post, got %d", m.mode)
	}
	// The info-only footer names the send-audio action, never a preview/cue.
	foot := strings.ToLower(stripANSI(m.listeningPostFooter()))
	if !strings.Contains(foot, "audio") {
		t.Errorf("the Listening Post footer should mention sending audio; got %q", foot)
	}
	if strings.Contains(foot, "cue") || strings.Contains(foot, "spin") {
		t.Errorf("the Listening Post is info-only — no cue/spin in its footer; got %q", foot)
	}
	// An unrelated key is a no-op (stays in the Post).
	tm, cmd := m.Update(keyMsg("x"))
	if asModel(tm).mode != modeListeningPost || cmd != nil {
		t.Errorf("an unrelated key in the Post should be a no-op, got mode %d", asModel(tm).mode)
	}
	// [1] jumps to THE BAND (never re-lands on voices).
	tm, _ = m.Update(keyMsg("1"))
	if asModel(tm).mode != modeBrowse {
		t.Fatalf("[1] from the Listening Post should land on THE BAND, got %d", asModel(tm).mode)
	}
}

// The Listening Post shows a paid transcriber's estimated ~$/min (marked as an estimate) and a
// free one as FREE.
func TestListeningPostPriceEstimate(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// The seed's stt band "whisper-listener" has PriceIn 5.0 (paid) → a "*"-marked per-min est.
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	tm, _ = m.Update(keyMsg("t"))
	out := asModel(tm).listeningPostView(100)
	if !strings.Contains(out, "*") {
		t.Errorf("a paid transcriber should show a *-marked per-minute estimate; got:\n%s", out)
	}
	if !strings.Contains(out, "whisper-listener") {
		t.Errorf("the Post should list the on-air transcriber; got:\n%s", out)
	}
}

// Regression: the Booth / Post price cells carry exactly ONE "$" (dollars() already prepends it;
// a leading "$"+dollars() double-signed to "$$0.02" in an early cut).
func TestBoothPriceSingleDollarSign(t *testing.T) {
	if got := boothPricePer1k(band{online: true, minIn: 20}); strings.Count(got, "$") != 1 {
		t.Errorf("a paid DJ price should have exactly one $, got %q", got)
	}
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	if strings.Contains(stripANSI(m.voiceBoothView(120)), "$$") {
		t.Errorf("the Booth must not render a double-$ price; got:\n%s", stripANSI(m.voiceBoothView(120)))
	}
	tm, _ = m.Update(keyMsg("t"))
	if strings.Contains(stripANSI(asModel(tm).listeningPostView(100)), "$$") {
		t.Errorf("the Listening Post must not render a double-$ price; got:\n%s", stripANSI(asModel(tm).listeningPostView(100)))
	}
}

// The Booth + Listening Post MODALITY glyphs (the ♪ DJ mark, the ▽ transcriber mark) and the —
// sample dash must ASCII-FOLD on a legacy console. This pins the DELTA's "every badge glyph needs
// an ASCII fold" rule at the RENDER site: under ROGERAI_ASCII=1 the modality/sample glyphs
// degrade (♪→>, ▽→v, —→-) rather than emit a raw rune. (Structural chrome like ▌/·/→ follows the
// house convention and is out of scope — the DELTA's rule is about the badge, whose old 🎤 had no
// fold at all; NO emoji ever appears here.) The check: the Booth/Post carry no 🎤, and the modal
// glyphs ♪/▽/— never survive as raw runes in the rendered rows.
func TestBoothPostBadgeAndDashFold(t *testing.T) {
	old, had := os.LookupEnv("ROGERAI_ASCII")
	os.Setenv("ROGERAI_ASCII", "1")
	defer func() {
		if had {
			os.Setenv("ROGERAI_ASCII", old)
		} else {
			os.Unsetenv("ROGERAI_ASCII")
		}
	}()
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	booth := stripANSI(m.voiceBoothView(120))
	tm, _ = m.Update(keyMsg("t"))
	post := stripANSI(asModel(tm).listeningPostView(100))
	for name, s := range map[string]string{"booth": booth, "post": post} {
		if strings.Contains(s, "🎤") {
			t.Errorf("%s must never contain the color emoji 🎤", name)
		}
		// The modality badge + sample glyphs must be folded (not raw) under ASCII.
		for _, raw := range []string{"♪", "▽", "—"} {
			if strings.Contains(s, raw) {
				t.Errorf("%s emits raw %q under ROGERAI_ASCII=1 — route it through glyphs.Fold (♪→>, ▽→v, —→-); line: %s",
					name, raw, firstLineWith(s, []rune(raw)[0]))
			}
		}
	}
}

// firstLineWith returns the first line of s containing rune r (for a readable failure).
func firstLineWith(s string, r rune) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.ContainsRune(ln, r) {
			return ln
		}
	}
	return ""
}

// The Booth footer teaches "cue" (endpoint) and "spin" (sample) in both wide + narrow forms.
func TestBoothFooterWideAndNarrow(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	tm, _ := m.Update(keyMsg("v"))
	m = asModel(tm)
	wide := strings.ToLower(stripANSI(m.voiceBoothFooter()))
	if !strings.Contains(wide, "cue") || !strings.Contains(wide, "spin") {
		t.Errorf("wide Booth footer should teach cue + spin; got %q", wide)
	}
	m.width = 40 // force narrow
	narrow := strings.ToLower(stripANSI(m.voiceBoothFooter()))
	if !strings.Contains(narrow, "cue") || !strings.Contains(narrow, "spin") {
		t.Errorf("narrow Booth footer should still teach cue + spin; got %q", narrow)
	}
}

// voiceBadge is empty for a chat band (the default/non-voice case).
func TestVoiceBadgeEmptyForChat(t *testing.T) {
	if got := voiceBadge(band{modality: "chat"}); got != "" {
		t.Errorf("a chat band should have no voice badge, got %q", got)
	}
	if got := voiceBadge(band{modality: ""}); got != "" {
		t.Errorf("a legacy (empty-modality) band should have no voice badge, got %q", got)
	}
}

// boothDJs / boothTranscribers sort strongest-first (exercises the signal comparator with ≥2
// entries): a weaker DJ handed a lower signal sorts AFTER a stronger one.
func TestBoothLineupSortsBySignal(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	// Give the two on-air tts DJs distinct signals so the order is deterministic.
	for i := range m.bands {
		switch m.bands[i].model {
		case "eager-puma-54-voice":
			hot := offer{NodeID: "a", Model: "eager-puma-54-voice", Modality: "tts", Online: true, Signal: 90}
			m.bands[i].all = []offer{hot}
			m.bands[i].cheapest = &m.bands[i].all[0]
		case "free-crier-voice":
			cold := offer{NodeID: "b", Model: "free-crier-voice", Modality: "tts", Online: true, Signal: 10}
			m.bands[i].all = []offer{cold}
			m.bands[i].cheapest = &m.bands[i].all[0]
		}
	}
	djs := m.boothDJs()
	if len(djs) != 2 || djs[0].model != "eager-puma-54-voice" {
		t.Fatalf("DJs should sort strongest-first (eager-puma first), got %v", func() []string {
			out := []string{}
			for _, d := range djs {
				out = append(out, d.model)
			}
			return out
		}())
	}
	// Two transcribers with distinct signals also sort strongest-first.
	m.bands = append(m.bands,
		band{model: "ears-weak", modality: "stt", online: true, stations: 1, all: []offer{{NodeID: "w", Model: "ears-weak", Modality: "stt", Online: true, Signal: 5}}},
		band{model: "ears-strong", modality: "stt", online: true, stations: 1, all: []offer{{NodeID: "s", Model: "ears-strong", Modality: "stt", Online: true, Signal: 95}}},
	)
	for i := range m.bands {
		if m.bands[i].model == "ears-weak" || m.bands[i].model == "ears-strong" {
			m.bands[i].cheapest = &m.bands[i].all[0]
		}
	}
	posts := m.boothTranscribers()
	if len(posts) < 2 || posts[0].model != "ears-strong" {
		t.Fatalf("transcribers should sort strongest-first (ears-strong first), got first=%q (n=%d)", func() string {
			if len(posts) > 0 {
				return posts[0].model
			}
			return ""
		}(), len(posts))
	}
}

// The stt badge folds to a plain "v" via glyphs.Fold — the same key the footnote uses (regression
// for the DELTA's "every glyph needs an ASCII fold" rule; the old 🎤 had none).
func TestSTTBadgeFoldsToV(t *testing.T) {
	if got := foldRune('▽'); got != "v" {
		t.Fatalf("the stt badge ▽ must fold to v, got %q", got)
	}
	if got := foldRune('♪'); got != ">" {
		t.Fatalf("the tts badge ♪ should fold to > (already in-code), got %q", got)
	}
}

// STRUCTURAL safety invariant (the original 504 bug can never recur): selectedBand() — which is
// what connect() acts on — can NEVER return a voice band, because visibleBands() excludes them. So
// there is no path from the band list to chatting a voice station. A voice is cued only from the
// Booth (→ startVoicePreview), never openChannel/modeChat. Even a voice band handed a top signal
// stays out of the connectable list.
func TestVoiceBandNeverConnectable(t *testing.T) {
	m := voiceSeed(t, "http://broker.local")
	for i := range m.bands {
		if m.bands[i].isVoice() {
			m.bands[i].all = []offer{{NodeID: "hot", Model: m.bands[i].model, Modality: m.bands[i].modality, Online: true, Signal: 100, TPS: 999}}
			m.bands[i].cheapest = &m.bands[i].all[0]
		}
	}
	// The band selectable at EVERY cursor position is a chat band; none is a voice.
	for i := 0; i < len(m.visibleBands()); i++ {
		m.cursor = i
		bd, ok := m.selectedBand()
		if ok && bd.isVoice() {
			t.Fatalf("cursor %d resolved to a voice band %q — voices must never be connectable", i, bd.model)
		}
	}
}

// --- helpers ------------------------------------------------------------------

// selectDJByModel points the Booth cursor at the named tts DJ.
func selectDJByModel(t *testing.T, m *model, model string) {
	t.Helper()
	djs := m.boothDJs()
	for i, b := range djs {
		if b.model == model {
			m.boothCursor = i
			return
		}
	}
	t.Fatalf("DJ %q not in the Booth lineup (%d DJs)", model, len(djs))
}
