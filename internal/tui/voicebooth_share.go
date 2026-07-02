package tui

// voicebooth_share.go is the SHARE-side VOICE BOOTH: the operator's voice-sharing wizard, reached
// via `p` on a `♪ tts` share row at the SAME depth as the chat price editor (founder DELTA §D2 —
// SHARE stays MODEL-FIRST; a voice is one tagged row among the operator's models, NOT elevated).
//
// The BOOTH edits the on-air DJ: dj-name, voice (via a picker over the ~33 Kokoro ids), a weighted
// BLEND (the blend string IS the shared voice — it rides on offer.Voice), speed (0.5–2.0),
// language, and price ($/1k chars or FREE). A LOCAL FREE preview (▶) synths a fixed line through
// the operator's OWN /v1/audio/speech at the current voice/blend/speed and plays it (reusing the
// cross-platform player in voice.go). This preview is FREE (the operator's own GPU, no broker
// relay, no confirm) — the deliberate asymmetry with the consumer's paid preview.
//
// On save, the result is stored on the shared *node.Controller (SetVoiceConfig), so when the row
// goes on air its offer carries the operator's Name/Voice/Speed/Language — a consumer gets the
// picked voice, not the raw local-server default.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/glyphs"
	"github.com/rogerai-fyi/roger/internal/node"
)

// sampleBoothText is the fixed line the LOCAL preview synthesizes (short — it's the operator's own
// GPU, but keep it snappy). Mirrors the consumer sampleVoiceText idiom.
const sampleBoothText = "You're on air with Roger."

// vbField identifies the focused field in the VOICE BOOTH editor (tab/↑↓ cycle).
const (
	vbFieldName  = iota // dj display name
	vbFieldVoice        // the voice picker field
	vbFieldBlend        // the weighted blend
	vbFieldSpeed        // playback speed 0.5–2.0
	vbFieldLang         // language label
	vbFieldPrice        // $/1k chars (FREE if 0)
	vbFieldCount        // number of fields (for modulo cycling)
)

// speed bounds + nudge step for the BOOTH speed field.
const (
	vbSpeedMin  = 0.5
	vbSpeedMax  = 2.0
	vbSpeedStep = 0.05
)

// vbFieldIndex maps a field label to its index (used by tests + the picker-return focus).
func vbFieldIndex(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dj name", "name":
		return vbFieldName
	case "voice":
		return vbFieldVoice
	case "blend":
		return vbFieldBlend
	case "speed":
		return vbFieldSpeed
	case "language", "lang":
		return vbFieldLang
	case "price":
		return vbFieldPrice
	}
	return vbFieldName
}

// blendVoice is one weighted component of a blend (id + weight). A single-voice DJ is one blendVoice
// with weight 1 (or the bare vbVoice when the blend is unset).
type blendVoice struct {
	id     string
	weight float64
}

// --- entry -------------------------------------------------------------------

// isTTSShareRow reports whether the share row at i is a tts (speak) model — the predicate that
// diverts `p` to the VOICE BOOTH (an stt row has no voice to pick, so it uses the ordinary price
// editor). Bounds-safe.
func (m model) isTTSShareRow(i int) bool {
	return i >= 0 && i < len(m.shareRows) && m.shareRows[i].modality == "tts"
}

// enterVoiceBooth opens the VOICE BOOTH for the selected tts row. Like the chat price editor it is
// login-gated (earning needs an account): an anonymous operator gets the same /login gate and the
// BOOTH does not open. It seeds every field from the stored VoiceConfig (so reopening shows what
// was set), defaulting an unset speed to 1.00 and language to en-US.
func (m *model) enterVoiceBooth() (tea.Model, tea.Cmd) {
	if len(m.shareRows) == 0 {
		return m, nil
	}
	if !m.loggedInState() {
		m.status = stEmber.Render("log in to earn - run ") + stKey.Render("/login") + stDim.Render("  (free sharing works without an account)")
		return m, nil
	}
	row := m.shareRows[m.shareCursor]
	m.vbModel = row.model
	vc := m.ctrl.VoiceConfigFor(row.model)
	m.vbName = vc.Name
	m.vbVoice = vc.Voice
	m.vbBlend = parseBlend(vc.Voice)
	if len(m.vbBlend) <= 1 {
		// A single (or empty) voice is edited via vbVoice, not the blend list.
		m.vbVoice = singleVoiceOf(vc.Voice)
		m.vbBlend = nil
	}
	m.vbSpeed = vc.Speed
	if m.vbSpeed == 0 {
		m.vbSpeed = 1.0
	}
	m.vbLang = vc.Language
	if m.vbLang == "" {
		m.vbLang = "en-US"
	}
	// The stored PriceIn is per-1M chars; the BOOTH field is $/1k, so seed it as In/1000.
	m.vbPrice = trimZero(m.pricingFor(row.model).In / 1000)
	m.vbField = vbFieldName
	m.vbErr = ""
	m.mode = modeShareVoice
	m.status = stDim.Render("VOICE BOOTH - tab field · type to set · " + glyphs.Fold("▶") + " spin (local · free) · ⏎ save + arm on-air · esc")
	return m, nil
}

// --- key handling ------------------------------------------------------------

// onShareVoiceKey drives the VOICE BOOTH editor. tab/↑↓ cycle fields; typing edits the focused
// text field (name/language/price); ◀/▶ nudge speed (or open the picker on the voice field); b adds
// a blend voice, x clears it; ▶/p plays the LOCAL free preview; enter saves + arms the row; esc
// cancels. Mirrors onShareEditorKey's shape.
func (m *model) onShareVoiceKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeShare
		m.status = stDim.Render("cancelled - voice unchanged")
		return m, nil
	case "enter":
		if m.commitVoiceBooth() {
			m.mode = modeShare
		}
		return m, nil
	case "tab", "down":
		m.vbField = (m.vbField + 1) % vbFieldCount
		return m, nil
	case "shift+tab", "up":
		m.vbField = (m.vbField - 1 + vbFieldCount) % vbFieldCount
		return m, nil
	case "left":
		if m.vbField == vbFieldSpeed {
			m.nudgeSpeed(-1)
		}
		return m, nil
	case "right":
		if m.vbField == vbFieldSpeed {
			m.nudgeSpeed(+1)
		}
		return m, nil
	case " ":
		// Space on the voice field opens the picker; elsewhere it types a space into a text field.
		if m.vbField == vbFieldVoice {
			return m.openVoicePicker()
		}
		m.typeVoiceBoothRune(" ")
		return m, nil
	case "b", "B":
		// Add a blend voice (only meaningful on the voice/blend fields; harmless elsewhere).
		if m.vbField == vbFieldVoice || m.vbField == vbFieldBlend {
			m.addBlendVoice("")
			return m, nil
		}
		m.typeVoiceBoothRune(k.String())
		return m, nil
	case "x", "X":
		if m.vbField == vbFieldBlend || m.vbField == vbFieldVoice {
			m.clearBlend()
			return m, nil
		}
		m.typeVoiceBoothRune(k.String())
		return m, nil
	case "▶":
		// ▶ always plays the LOCAL free preview (never a broker relay).
		return m, m.playVoiceBoothPreview()
	case "p", "P":
		// p plays the preview EXCEPT on a text field (name/language), where it types the letter.
		if m.vbIsTextField() {
			m.typeVoiceBoothRune(k.String())
			return m, nil
		}
		return m, m.playVoiceBoothPreview()
	case "backspace":
		m.backspaceVoiceBoothField()
		return m, nil
	default:
		// Typing on the voice field opens the picker pre-filtered by the first keystroke; on a text
		// field (name/language/price) the runes are appended. Handle tea.KeyRunes by its Runes so
		// PASTED / multi-rune input lands whole (a single KeyRunes carrying "1950s Operator"),
		// mirroring onStationRenameKey.
		if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
			if m.vbField == vbFieldVoice {
				// openVoicePicker mutates the receiver (pointer) in place, so set the seed filter on m
				// AFTER it returns — no type assertion on its tea.Model result (which is a *model).
				_, cmd := m.openVoicePicker()
				m.vpFilter = string(k.Runes)
				return m, cmd
			}
			m.typeVoiceBoothRune(string(k.Runes))
		}
		return m, nil
	}
}

// vbIsTextField reports whether the focused BOOTH field is a free-text buffer (name/language/price)
// — where a printable rune should TYPE rather than trigger a command key (p/b/x).
func (m model) vbIsTextField() bool {
	return m.vbField == vbFieldName || m.vbField == vbFieldLang || m.vbField == vbFieldPrice
}

// typeVoiceBoothRune appends a printable rune to the focused text field (name/language/price).
func (m *model) typeVoiceBoothRune(s string) {
	switch m.vbField {
	case vbFieldName:
		m.vbName += s
	case vbFieldLang:
		m.vbLang += s
	case vbFieldPrice:
		// Only digits + one dot for the price buffer.
		if s == "." || (s >= "0" && s <= "9") {
			m.vbPrice += s
		}
	}
}

// backspaceVoiceBoothField trims the last rune of the focused text field.
func (m *model) backspaceVoiceBoothField() {
	switch m.vbField {
	case vbFieldName:
		m.vbName = trimLastRune(m.vbName)
	case vbFieldLang:
		m.vbLang = trimLastRune(m.vbLang)
	case vbFieldPrice:
		m.vbPrice = trimLastRune(m.vbPrice)
	}
}

func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return string(r[:len(r)-1])
}

// nudgeSpeed steps the speed by dir*step, CLAMPED to [0.5, 2.0] so it can never escape the range.
func (m *model) nudgeSpeed(dir int) {
	m.vbSpeed += float64(dir) * vbSpeedStep
	m.vbSpeed = clampSpeed(m.vbSpeed)
}

func clampSpeed(v float64) float64 {
	if v < vbSpeedMin {
		return vbSpeedMin
	}
	if v > vbSpeedMax {
		return vbSpeedMax
	}
	// Round to 2dp so accumulated float error doesn't show 1.2500000001×.
	return float64(int(v*100+0.5)) / 100
}

// --- blend -------------------------------------------------------------------

// addBlendVoice adds id (or, if empty, the next un-added Kokoro voice) to the blend and
// re-normalizes to equal weights. Starting from a single vbVoice promotes it into the blend first,
// so `af_heart` + add `af_bella` becomes a 2-voice blend.
func (m *model) addBlendVoice(id string) {
	if len(m.vbBlend) == 0 && m.vbVoice != "" {
		m.vbBlend = []blendVoice{{id: m.vbVoice, weight: 1}}
	}
	if id == "" {
		id = m.nextUnusedVoice()
	}
	if id == "" {
		return
	}
	for _, bv := range m.vbBlend {
		if bv.id == id {
			return // already in the blend
		}
	}
	m.vbBlend = append(m.vbBlend, blendVoice{id: id, weight: 1})
	m.normalizeBlend()
}

// clearBlend collapses the blend back to a single voice (the first component, or the current
// vbVoice), so `x` returns to a plain single-voice DJ.
func (m *model) clearBlend() {
	if len(m.vbBlend) > 0 {
		m.vbVoice = m.vbBlend[0].id
	}
	m.vbBlend = nil
}

// normalizeBlend sets equal weights that sum to 1 across the blend components (the auto-normalize
// the founder wants — the operator's ratios are honored on typed edits, but the default add yields
// an even mix summing to 1).
func (m *model) normalizeBlend() {
	n := len(m.vbBlend)
	if n == 0 {
		return
	}
	w := 1.0 / float64(n)
	for i := range m.vbBlend {
		m.vbBlend[i].weight = w
	}
}

// setBlendFromString seeds the blend from a "a:0.7+b:0.3" string (used by tests + reopen).
func (m *model) setBlendFromString(s string) {
	m.vbBlend = parseBlend(s)
	if len(m.vbBlend) <= 1 {
		m.vbVoice = singleVoiceOf(s)
		m.vbBlend = nil
	}
}

// blendString renders the current blend as a wire string: "a:0.7+b:0.3" for a real blend, or the
// bare single id when there is no blend. Weights are formatted compactly. This IS offer.Voice.
func (m model) blendString() string {
	if len(m.vbBlend) == 0 {
		return m.vbVoice
	}
	parts := make([]string, len(m.vbBlend))
	for i, bv := range m.vbBlend {
		parts[i] = fmt.Sprintf("%s:%s", bv.id, trimFloat(bv.weight))
	}
	return strings.Join(parts, "+")
}

// nextUnusedVoice returns the first bundled voice not already in the blend (so `b` adds a fresh
// one), preferring same-prefix voices for a coherent blend.
func (m model) nextUnusedVoice() string {
	used := map[string]bool{}
	for _, bv := range m.vbBlend {
		used[bv.id] = true
	}
	all := m.pickerSource()
	// Prefer a voice sharing the first component's prefix.
	if len(m.vbBlend) > 0 {
		pfx := prefixOf(m.vbBlend[0].id)
		for _, v := range all {
			if !used[v] && prefixOf(v) == pfx {
				return v
			}
		}
	}
	for _, v := range all {
		if !used[v] {
			return v
		}
	}
	return ""
}

// --- LOCAL preview (free; reuses the cross-platform player) -------------------

// localSpeechURL derives the operator's LOCAL /v1/audio/speech URL from the selected row's chat
// upstream (the SAME base-swap serve() does), so the preview hits the operator's own server — never
// the broker. Empty when the row has no upstream.
func (m model) localSpeechURL() string {
	if m.shareCursor < 0 || m.shareCursor >= len(m.shareRows) {
		return ""
	}
	up := m.shareRows[m.shareCursor].upstream
	if up == "" {
		return ""
	}
	return strings.TrimSuffix(up, "/chat/completions") + "/audio/speech"
}

// playVoiceBoothPreview synthesizes the fixed sample through the operator's LOCAL speech server at
// the current voice/blend/speed and plays it via the injected player. It is FREE (local GPU, no
// broker, no billing) and never crashes: an unreachable/erroring server surfaces a dim error.
func (m *model) playVoiceBoothPreview() tea.Cmd {
	url := m.localSpeechURL()
	voice := m.blendString()
	speed := m.vbSpeed
	play := m.previewPlayer
	if play == nil {
		play = systemAudioPlayer
	}
	if url == "" {
		m.vbErr = "no local voice server on this row"
		m.status = stEmber.Render("! local voice server not found for this row")
		return nil
	}
	return func() tea.Msg {
		return boothPreviewMsg(synthLocalSpeech(url, voice, speed, sampleBoothText, play))
	}
}

// boothPreviewMsg carries a completed LOCAL preview back to Update (played / saved / error). It is
// a distinct type from voicePreviewMsg so the SHARE preview never touches the consumer money path.
type boothPreviewMsg struct {
	played bool
	path   string
	err    string
}

// synthLocalSpeech POSTs {model?, input, voice, speed, response_format:"wav"} to the LOCAL speech
// URL (NOT signed, NOT the broker — it's the operator's own server), reads the WAV, and plays it.
// `voice` is the resolved single id OR blend string. Returns played/path/err.
func synthLocalSpeech(url, voice string, speed float64, text string, play audioPlayerFn) boothPreviewMsg {
	body := map[string]any{"input": text, "response_format": "wav"}
	if voice != "" {
		body["voice"] = voice
	}
	if speed != 0 {
		body["speed"] = speed
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return boothPreviewMsg{err: "could not build the local request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return boothPreviewMsg{err: "local voice server didn't answer - is it running?"}
	}
	defer resp.Body.Close()
	audio, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return boothPreviewMsg{err: fmt.Sprintf("local voice server error (%d)", resp.StatusCode)}
	}
	path, played, perr := play(audio)
	msg := boothPreviewMsg{played: played, path: path}
	if perr != nil {
		msg.err = "playback failed: " + perr.Error()
	}
	return msg
}

// applyBoothPreview folds a completed LOCAL preview result into the model (called from Update). The
// preview is FREE, so previewCost is never touched here (stays 0 — asserted by the money test).
func (m model) applyBoothPreview(msg boothPreviewMsg) model {
	if msg.err != "" {
		m.vbErr = msg.err
		m.status = stEmber.Render("! " + msg.err)
		return m
	}
	m.vbErr = ""
	switch {
	case msg.played:
		m.status = stLive.Render(glyphs.Fold("♪")+" played a local preview") + stDim.Render(" · free (your GPU)")
	case msg.path != "":
		m.status = stDim.Render("no audio player found - preview saved to ") + stKey.Render(msg.path)
	default:
		m.status = stDim.Render("local preview fetched")
	}
	return m
}

// --- the voice PICKER popover -------------------------------------------------

// pickerSource is the id list the picker draws from: the LOCAL-fetched voices when we have them,
// else the bundled fallback (so the picker never blanks).
func (m model) pickerSource() []string {
	if len(m.vpVoices) > 0 {
		return m.vpVoices
	}
	return bundledKokoroVoices()
}

// openVoicePicker opens the picker popover over the voice field, seeded with the bundled list
// immediately (so it is never blank even before the local fetch returns) and highlighting the
// current voice. The caller pairs this with fetchLocalVoicesCmd to refine the list from the server.
func (m *model) openVoicePicker() (tea.Model, tea.Cmd) {
	if len(m.vpVoices) == 0 {
		m.vpVoices = bundledKokoroVoices()
		m.vpSourceLocal = false
	}
	m.vpFilter = ""
	m.vpCursor = 0
	cur := m.vbVoice
	if cur == "" && len(m.vbBlend) > 0 {
		cur = m.vbBlend[0].id
	}
	for i, v := range m.visiblePickerVoices() {
		if v == cur {
			m.vpCursor = i
			break
		}
	}
	m.mode = modeVoicePicker
	m.status = stDim.Render("PICK A VOICE - type to filter · " + glyphs.Fold("▶") + " spin (local · free) · ⏎ pick · esc")
	return m, m.fetchLocalVoicesCmd()
}

// localVoicesMsg carries the LOCAL /v1/audio/voices fetch result back to Update (empty ids => keep
// the bundled fallback).
type localVoicesMsg struct {
	ids []string
	err string
}

// fetchLocalVoicesCmd fetches the operator's LOCAL GET /v1/audio/voices off the event loop and
// parses it into ids. A miss (unreachable / non-200 / unparseable) yields no ids, so the picker
// keeps its bundled fallback — it never blanks.
func (m model) fetchLocalVoicesCmd() tea.Cmd {
	url := m.localVoicesURL()
	if url == "" {
		return func() tea.Msg { return localVoicesMsg{} }
	}
	return func() tea.Msg {
		hc := &http.Client{Timeout: 10 * time.Second}
		resp, err := hc.Get(url)
		if err != nil {
			return localVoicesMsg{err: "local voice server didn't answer"}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return localVoicesMsg{err: fmt.Sprintf("local voice list unavailable (%d)", resp.StatusCode)}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return localVoicesMsg{ids: parseVoicesResponse(body)}
	}
}

// localVoicesURL derives the LOCAL GET /v1/audio/voices URL from the selected row's chat upstream.
func (m model) localVoicesURL() string {
	if m.shareCursor < 0 || m.shareCursor >= len(m.shareRows) {
		return ""
	}
	up := m.shareRows[m.shareCursor].upstream
	if up == "" {
		return ""
	}
	return strings.TrimSuffix(up, "/chat/completions") + "/audio/voices"
}

// applyLocalVoices folds a completed voices fetch into the model: real ids REPLACE the bundled
// fallback; a miss keeps the bundle (and, in the picker, notes the error dimly).
func (m model) applyLocalVoices(msg localVoicesMsg) model {
	if len(msg.ids) > 0 {
		m.vpVoices = msg.ids
		m.vpSourceLocal = true
		return m
	}
	if len(m.vpVoices) == 0 {
		m.vpVoices = bundledKokoroVoices()
	}
	m.vpSourceLocal = false
	if msg.err != "" {
		m.vbErr = msg.err
	}
	return m
}

// visiblePickerVoices is the picker list filtered by the typed substring (case-insensitive), over
// the current source (local or bundled). Empty filter = the full source.
func (m model) visiblePickerVoices() []string {
	src := m.pickerSource()
	f := strings.ToLower(strings.TrimSpace(m.vpFilter))
	if f == "" {
		return src
	}
	var out []string
	for _, v := range src {
		if strings.Contains(strings.ToLower(v), f) {
			out = append(out, v)
		}
	}
	return out
}

// onVoicePickerKey drives the picker popover. Typing filters; backspace trims; ↑↓/←→ move the
// cursor over the (grouped) visible list; ▶ auditions the highlighted voice (LOCAL + free); enter
// picks it (sets vbVoice, collapses any blend to that single voice) and returns to the BOOTH; esc
// cancels back to the BOOTH.
func (m *model) onVoicePickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	vis := m.visiblePickerVoices()
	switch k.String() {
	case "esc":
		m.mode = modeShareVoice
		m.vbField = vbFieldVoice
		m.status = stDim.Render("back to the VOICE BOOTH")
		return m, nil
	case "enter":
		if len(vis) > 0 {
			i := m.vpCursor
			if i < 0 || i >= len(vis) {
				i = 0
			}
			m.vbVoice = vis[i]
			m.vbBlend = nil // picking a single voice clears a blend
		}
		m.mode = modeShareVoice
		m.vbField = vbFieldVoice
		m.status = stDim.Render("voice set to ") + stKey.Render(m.vbVoice)
		return m, nil
	case "up", "left":
		if m.vpCursor > 0 {
			m.vpCursor--
		}
		return m, nil
	case "down", "right":
		if m.vpCursor < len(vis)-1 {
			m.vpCursor++
		}
		return m, nil
	case "▶":
		return m, m.auditionPickerVoice()
	case "backspace":
		m.vpFilter = trimLastRune(m.vpFilter)
		m.vpCursor = 0
		return m, nil
	default:
		// A printable rune extends the live filter (auditioning is ▶ only, handled above).
		if s := k.String(); len(s) == 1 {
			m.vpFilter += s
			m.vpCursor = 0
		}
		return m, nil
	}
}

// auditionPickerVoice plays the highlighted voice through the LOCAL speech server (free), so the
// operator scans the booth by ear. Same local, unsigned, no-broker path as the BOOTH preview.
func (m *model) auditionPickerVoice() tea.Cmd {
	vis := m.visiblePickerVoices()
	if len(vis) == 0 {
		return nil
	}
	i := m.vpCursor
	if i < 0 || i >= len(vis) {
		i = 0
	}
	voice := vis[i]
	url := m.localSpeechURL()
	speed := m.vbSpeed
	if speed == 0 {
		speed = 1.0
	}
	play := m.previewPlayer
	if play == nil {
		play = systemAudioPlayer
	}
	if url == "" {
		m.vbErr = "no local voice server on this row"
		m.status = stEmber.Render("! local voice server not found for this row")
		return nil
	}
	return func() tea.Msg {
		return boothPreviewMsg(synthLocalSpeech(url, voice, speed, sampleBoothText, play))
	}
}

// --- commit ------------------------------------------------------------------

// commitVoiceBooth validates + stores the BOOTH result on the shared controller (SetVoiceConfig),
// so the next on-air toggle carries the operator's Name/Voice(/blend)/Speed/Language onto the
// offer. A bad price BLOCKS the save with an inline error (like the chat editor). The blend string
// is normalized on save (blendString already renders normalized weights).
func (m *model) commitVoiceBooth() bool {
	perK, perr := parsePrice(m.vbPrice)
	if perr != "" {
		m.vbErr = perr
		return false
	}
	// The BOOTH price field is $/1k CHARS (the friendly unit); the offer bills per-1M chars, so the
	// stored PriceIn is perK*1000. The public ceiling (editorMaxPriceIn) is a per-1M figure.
	perM := perK * 1000
	if perM > editorMaxPriceIn {
		m.vbErr = fmt.Sprintf("price $%s/1k chars is over the $%.0f/1M public ceiling - lower it, or share PRIVATE", trimFloat(perK), editorMaxPriceIn)
		return false
	}
	m.vbErr = ""
	voice := m.blendString()
	// The BOOTH has no sample field (sample_url is config-file-set, share_voices); a BOOTH
	// save must not clobber it, so carry the stored value through.
	m.ctrl.SetVoiceConfig(m.vbModel, node.VoiceConfig{Name: m.vbName, Voice: voice, Speed: m.vbSpeed, Language: m.vbLang,
		SampleURL: m.ctrl.VoiceConfigFor(m.vbModel).SampleURL})
	// Persist the price (per-1M input chars) through the same pricing store the chat editor uses,
	// so the SHARE row shows $/1k and the offer bills correctly. FREE when 0.
	m.ctrl.SetPricing(m.vbModel, Pricing{In: perM})
	m.syncShareCache()
	label := "FREE"
	if perK > 0 {
		label = dollars(perK) + "/1k ch"
	}
	who := m.vbName
	if who == "" {
		who = voice
	}
	m.status = stLive.Render("armed ") + stKey.Render(who) + stDim.Render(" · ") + stEmber.Render(label) + stDim.Render(" · re-toggle on air to apply")
	return true
}

// --- rendering ----------------------------------------------------------------

// shareVoiceView renders the SHARE VOICE BOOTH editor: a section-tab heading, ▌-focus field rows
// with ▏value▏ boxes, the LOCAL free preview line, the live "right now you would broadcast as …"
// line, and an inline ⚠ validation error. Mirrors shareEditorView's look. (Distinct from the
// CONSUMER voiceBoothView in voice.go — this is the operator's SHARE-side wizard.)
func (m model) shareVoiceView(w int) string {
	var b strings.Builder
	narrow := m.narrow()
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("VOICE BOOTH") +
		stDim.Render("   ") + stKey.Render(m.vbModel) + stDim.Render("   ·   build your on-air DJ") + "\n\n")

	field := func(idx int, label, val, tail string) string {
		cur := "  "
		nameSt := stDim
		if m.vbField == idx {
			cur = stSelText.Render("▌ ")
			nameSt = stSelText
		}
		shown := val
		if shown == "" {
			shown = " "
		}
		box := "▏" + shown + "▏"
		if m.vbField == idx {
			box = stSelText.Render("▏" + shown + "▏")
		} else {
			box = stEmber.Render(box)
		}
		t := ""
		if !narrow && tail != "" {
			t = stDim.Render("   " + tail)
		}
		return cur + nameSt.Render(pad(label, 14)) + box + t + "\n"
	}

	b.WriteString(field(vbFieldName, "dj name", m.vbName, "how listeners hear you"))
	b.WriteString(field(vbFieldVoice, "voice", m.vbVoice, "enter/space browse "+strconv.Itoa(len(m.pickerSource()))+" · "+prefixHint(m.vbVoice)))
	b.WriteString(field(vbFieldBlend, "blend", m.blendDisplay(), "b add · x clear"))
	b.WriteString(field(vbFieldSpeed, "speed", trimFloat(m.vbSpeed)+"×", glyphs.Fold("◀ ▶")+" 0.5-2.0"))
	b.WriteString(field(vbFieldLang, "language", m.vbLang, ""))
	b.WriteString(field(vbFieldPrice, "price", m.vbPrice, "$ / 1k chars  ·  FREE if 0"))

	// Local free preview line.
	b.WriteString("\n  " + stBadge.Render(glyphs.Fold("♪")) + " " + stDim.Render("preview   ") +
		stKey.Render(glyphs.Fold("▶")+" spin") + stDim.Render(" (local · free)   ") + stDim.Render("\""+sampleBoothText+"\"") + "\n")

	// The live "right now you would broadcast as …" line.
	b.WriteString("\n  " + stDim.Render("right now you would broadcast as  ") + boothBroadcastLine(m) + "\n")

	if m.vbErr != "" {
		b.WriteString("\n  " + stEmber.Render("! "+m.vbErr) + "\n")
	}
	return b.String()
}

// blendDisplay renders the blend for the editor row: the weighted string, or "(single voice)" when
// there is no blend.
func (m model) blendDisplay() string {
	if len(m.vbBlend) == 0 {
		return "(single voice)"
	}
	parts := make([]string, len(m.vbBlend))
	for i, bv := range m.vbBlend {
		parts[i] = fmt.Sprintf("%s %s", bv.id, trimFloat(bv.weight))
	}
	return strings.Join(parts, " + ")
}

// boothBroadcastLine is the "broadcast as NAME · $price · speed×" summary (the on-air identity at a
// glance). Exposed for the tests.
func boothBroadcastLine(m model) string {
	who := m.vbName
	if who == "" {
		who = m.blendString()
	}
	if who == "" {
		who = "(unnamed)"
	}
	// vbPrice is already in $/1k chars (the field's unit), so show it directly.
	perK, _ := parsePrice(m.vbPrice)
	priceLabel := "FREE"
	if perK > 0 {
		priceLabel = dollars(perK) + "/1k ch"
	}
	return stKey.Render(who) + stDim.Render("  ·  ") + stEmber.Render(priceLabel) + stDim.Render("  ·  ") + stKey.Render(trimFloat(m.vbSpeed)+"×")
}

// shareVoiceFooter is the SHARE VOICE BOOTH key-hint footer (distinct from the consumer
// voiceBoothFooter in voice.go).
func (m model) shareVoiceFooter() string {
	if m.narrow() {
		return stDim.Render("tab · " + glyphs.Fold("▶") + " spin · ⏎ save · esc")
	}
	return stDim.Render("tab field  ·  type to set  ·  " + glyphs.Fold("▶") + "/p spin (local · free)  ·  ⏎ save + arm on-air  ·  esc cancel")
}

// voicePickerView renders the PICK A VOICE popover: a dense grouped grid (American ♀/♂, British
// ♀/♂, Other), the typed filter, a red reverse-video cursor row, and the audition hint.
func (m model) voicePickerView(w int) string {
	var b strings.Builder
	vis := m.visiblePickerVoices()
	src := "bundled"
	if m.vpSourceLocal {
		src = "local"
	}
	filt := ""
	if m.vpFilter != "" {
		filt = stDim.Render(" · filter ") + stKey.Render(m.vpFilter)
	}
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("PICK A VOICE") +
		stDim.Render(fmt.Sprintf("   %s · %d voices (%s)", m.vbModel, len(vis), src)) + filt +
		stDim.Render("   ·   esc") + "\n\n")

	if len(vis) == 0 {
		b.WriteString("  " + stDim.Render("no voices match "+strconv.Quote(m.vpFilter)) + "\n")
		return b.String()
	}
	// Cursor id (so the group render can mark it) — from the flat visible list.
	cur := ""
	if m.vpCursor >= 0 && m.vpCursor < len(vis) {
		cur = vis[m.vpCursor]
	}
	for _, g := range groupVoices(vis) {
		row := "  " + stDim.Render(pad(g.label, 16))
		for _, id := range g.ids {
			cell := " " + id
			if id == cur {
				cell = stSelText.Render(" > " + id)
			} else {
				cell = stKey.Render(cell)
			}
			row += cell
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n  " + stBadge.Render(glyphs.Fold("♪")) + " " + stDim.Render(glyphs.Fold("▶")+" spin the highlighted voice (local · free)") + "\n")
	return b.String()
}

// voicePickerFooter is the popover footer.
func (m model) voicePickerFooter() string {
	return stDim.Render("↑↓←→ move  ·  ⏎ pick  ·  " + glyphs.Fold("▶") + " spin  ·  type to filter  ·  esc")
}

// --- blend/price parsing helpers ---------------------------------------------

// parseBlend parses a blend into weighted components, accepting BOTH the WIRE form
// ("af_heart:0.7+af_bella:0.3", what rides offer.Voice) and the DISPLAY form
// ("af_heart 0.7 + af_bella 0.3", what the editor shows). Components split on '+'; within a
// component the id and weight split on ':' or whitespace. A bare id yields weight 1; a malformed
// weight defaults to 1 (then normalizeBlend evens it out on add).
func parseBlend(s string) []blendVoice {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "+")
	out := make([]blendVoice, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Split id / weight on ':' first, else on whitespace.
		id, wStr := p, ""
		if i := strings.IndexAny(p, ": \t"); i >= 0 {
			id, wStr = strings.TrimSpace(p[:i]), strings.TrimSpace(p[i+1:])
		}
		bv := blendVoice{id: id, weight: 1}
		if wStr != "" {
			if w, err := strconv.ParseFloat(wStr, 64); err == nil {
				bv.weight = w
			}
		}
		if bv.id != "" {
			out = append(out, bv)
		}
	}
	return out
}

// singleVoiceOf returns the bare id of a single-voice string (strips any ":weight"), or "" for a
// real multi-voice blend / empty.
func singleVoiceOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "+") {
		return ""
	}
	id, _, _ := strings.Cut(s, ":")
	return strings.TrimSpace(id)
}

// parsePrice parses the BOOTH price buffer (in the field's $/1k-chars unit) — empty/"." = free (0).
// Returns an inline error string for an unparseable or negative value. Unit-agnostic: the caller
// converts to the offer's per-1M unit.
func parsePrice(s string) (float64, string) {
	s = strings.TrimSpace(s)
	if s == "" || s == "." {
		return 0, ""
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, "price must be a non-negative number ($/1k chars)"
	}
	return v, ""
}

// trimFloat renders a float compactly (1 → "1", 1.25 → "1.25", 0.5 → "0.5").
func trimFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func prefixOf(id string) string {
	if i := strings.Index(id, "_"); i >= 0 {
		return id[:i+1]
	}
	return ""
}
