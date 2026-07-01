package tui

// voice.go differentiates VOICE bands (tts/stt) from chat bands in the browser. A voice band is
// grouped into a distinct "Voices" section (see visibleBands / browseView) and, when selected,
// opens a sample-play/PREVIEW panel — NEVER a chat channel — so a consumer can never tune a voice
// station as chat and hit "504 no station is serving <voice>".
//
// MONEY (founder #1): a tts preview hits POST /v1/audio/speech, which is CHAR-metered = real
// money for a PAID voice. So:
//   - a FREE voice (price 0)  -> synthesize the fixed sample immediately;
//   - a PAID voice            -> show the tiny sample cost and REQUIRE an explicit confirm
//                                keypress before any POST (never auto-spend).
// An stt band can't be previewed by chat, so it shows an informational panel (model + price +
// "send audio via the app/API"), not a dead chat.
//
// Audio playback runs through an INJECTABLE player (audioPlayerFn) detected at runtime, with a
// save-to-file fallback when no system player exists — so it never crashes and is fully testable.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/glyphs"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// sampleVoiceText is the fixed short line a tts preview synthesizes. Kept tiny (a handful of
// chars) so a PAID preview costs a fraction of a cent — the confirm gate still applies.
const sampleVoiceText = "Hello from RogerAI."

// preview stages (previewStage).
const (
	previewConfirm = iota // PAID tts: awaiting the explicit confirm keypress before spending
	previewSynth          // synth Cmd in flight (the sample is being fetched)
	previewDone           // a sample played / was saved (see previewPlayed / previewPath)
	previewInfoSTT        // stt: the informational panel (no chat preview possible)
	previewError          // the synth failed (previewErr carries why); the panel offers a retry
	previewOffline        // the voice station is off air right now (nothing to preview)
)

// canonModality normalizes a wire modality to its canonical form: an empty (pre-voice) value is
// the back-compat "chat" default, so a legacy offer is never mistaken for a voice band. Mirrors
// the broker's offerModality so the TUI reads modality identically to the server.
func canonModality(m string) string {
	if m == "" {
		return protocol.ModalityChat
	}
	return m
}

// isVoice reports whether the band is a VOICE band (tts or stt) — the ONE predicate that both
// groups it into the Voices section and diverts its selection to the preview instead of chat.
func (b band) isVoice() bool {
	return b.modality == protocol.ModalityTTS || b.modality == protocol.ModalitySTT
}

// isTTS / isSTT distinguish the two voice sub-kinds for the preview flow (tts synthesizes a
// sample; stt shows an info panel).
func (b band) isTTS() bool { return b.modality == protocol.ModalityTTS }
func (b band) isSTT() bool { return b.modality == protocol.ModalitySTT }

// voiceBadge is the tiny MONO modality glyph for a voice band: ♪ (tts, folds to >) / ▽ (stt,
// "into text", folds to v). Both are one-ink, fixed-width, and ASCII-foldable — the house rule.
// It is deliberately NOT the color emoji 🎤 (variable-width, no fold, breaks mono+red). Empty for
// a chat band. Used inside the DJ BOOTH / Listening Post drill-in, never in the top-level list.
func voiceBadge(b band) string {
	switch {
	case b.isTTS():
		return "♪"
	case b.isSTT():
		return "▽"
	default:
		return ""
	}
}

// sampleVoiceCost is the credit cost of ONE tts preview at the band's cheapest input price —
// the broker meters TTS by exact input CHARS (cost = chars * priceIn/1e6), so this is computed
// the SAME way the server will bill, making the disclosed cost honest. A free band is $0.
func sampleVoiceCost(b band) float64 {
	if !b.online {
		return 0
	}
	if b.free || b.minIn == 0 {
		return 0
	}
	return float64(len([]rune(sampleVoiceText))) * b.minIn / 1e6
}

// startVoicePreview opens the preview panel for a selected voice band. It NEVER opens a chat
// channel. The stage is chosen so the money gate holds: an offline band -> an off-air note; an
// stt band -> the info panel (no synth); a FREE tts band -> synth immediately; a PAID tts band
// -> the confirm-first state (no spend until the user opts in).
func (m model) startVoicePreview(bd band) (tea.Model, tea.Cmd) {
	m.mode = modeVoicePreview
	m.previewBand = bd
	m.previewErr = ""
	m.previewPath = ""
	m.previewPlayed = false
	m.previewCost = sampleVoiceCost(bd)
	switch {
	case !bd.online:
		m.previewStage = previewOffline
		m.status = stEmber.Render(noStationServing(bd.model)) + stDim.Render(" - no voice to preview right now")
		return m, nil
	case bd.isSTT():
		m.previewStage = previewInfoSTT
		m.status = stDim.Render("speech-to-text station - send audio via the app or API")
		return m, nil
	case m.previewCost > 0:
		// PAID tts: hold at the confirm gate. NO POST until the user presses y/⏎.
		m.previewStage = previewConfirm
		m.status = stEmber.Render("paid voice - ") + stDim.Render("press ") + stKey.Render("⏎/y") + stDim.Render(" to spend ") + stKey.Render(dollars(m.previewCost)) + stDim.Render(" on a sample")
		return m, nil
	default:
		// FREE tts: synthesize the sample straight away.
		m.previewStage = previewSynth
		m.status = stDim.Render("synthesizing a sample…")
		return m, m.synthVoiceSample()
	}
}

// previewNeedsConfirm reports whether the preview is holding at the paid-confirm gate (nothing
// spent yet). Used by the view/footer + the money-gate test.
func (m model) previewNeedsConfirm() bool {
	return m.mode == modeVoicePreview && m.previewStage == previewConfirm
}

// onVoicePreviewKey drives the preview panel. esc/q/← always return to the browser. In the
// paid-confirm state, y/⏎ opt in and fire the synth (the ONLY path that spends); n/esc decline.
// In a played/error state, r replays/retries the sample (free bands only, or a re-confirm for
// paid — a re-synth of a paid band re-enters the confirm gate, never an auto-spend).
func (m model) onVoicePreviewKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "left", "h":
		m.mode = modeBrowse
		m.status = stDim.Render("closed the voice preview")
		return m, nil
	}
	switch m.previewStage {
	case previewConfirm:
		switch k.String() {
		case "enter", "y", "Y":
			m.previewStage = previewSynth
			m.status = stDim.Render("synthesizing a sample…")
			return m, m.synthVoiceSample()
		case "n", "N":
			m.mode = modeBrowse
			m.status = stDim.Render("declined - no sample synthesized, nothing spent")
			return m, nil
		}
	case previewDone, previewError:
		if k.String() == "r" || k.String() == "enter" {
			// Replay/retry: a paid band re-enters the confirm gate (never an auto-spend); a
			// free band re-synths immediately. startVoicePreview picks the right stage.
			return m.startVoicePreview(m.previewBand)
		}
	}
	return m, nil
}

// voicePreviewMsg carries a completed (or failed) sample synth back to the event loop: the
// broker-billed cost (from X-RogerAI-Cost), whether it played, the fallback save path (when no
// player), and any error.
type voicePreviewMsg struct {
	cost   float64
	played bool
	path   string
	err    string
}

// synthVoiceSample POSTs the fixed sample text to the broker's TTS relay (signed, like the chat
// relay, so the broker bills the signed wallet — self/free stays $0), reads the returned WAV +
// the X-RogerAI-Cost meter header, and plays the audio via the injected/real player off the
// event loop. It is the ONLY function that spends on a preview, and it is only ever reached AFTER
// the free/confirm gate in startVoicePreview / onVoicePreviewKey.
func (m model) synthVoiceSample() tea.Cmd {
	broker, model := m.broker, m.previewBand.model
	play := m.previewPlayer
	if play == nil {
		play = systemAudioPlayer // the real system player when not stubbed
	}
	return func() tea.Msg {
		// Body: {model, input, response_format:"wav"} and DELIBERATELY NO `voice` field. WAV is
		// requested for the LOCAL preview because it is universally + trivially playable (afplay /
		// .NET SoundPlayer play it built-in, no lame/ffmpeg). A Kokoro-style server 500s ("Voice X
		// not found") when handed OpenAI's "alloy" or the model id as a voice; with voice omitted
		// it uses its warm af_heart default blend and returns clean audio. Valid voice names (GET
		// /v1/audio/voices) are for the producer share wizard, not this consumer preview — never
		// invent/pass one here.
		body, _ := json.Marshal(map[string]any{"model": model, "input": sampleVoiceText, "response_format": "wav"})
		req, err := http.NewRequest(http.MethodPost, broker+"/v1/audio/speech", bytes.NewReader(body))
		if err != nil {
			return voicePreviewMsg{err: "could not build the request: " + err.Error()}
		}
		req.Header.Set("Content-Type", "application/json")
		// Signed spend-auth: the broker derives the billed wallet from the SIGNATURE pubkey, not
		// from any header, so no X-Roger-User hint is needed (it is ignored for wallet selection).
		client.SignRequest(req, body)
		hc := &http.Client{Timeout: 30 * time.Second}
		resp, err := hc.Do(req)
		if err != nil {
			return voicePreviewMsg{err: "broker unreachable"}
		}
		defer resp.Body.Close()
		audio, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if resp.StatusCode != http.StatusOK {
			return voicePreviewMsg{err: httpErrMessage(resp.StatusCode, audio)}
		}
		cost, _ := strconv.ParseFloat(resp.Header.Get("X-RogerAI-Cost"), 64)
		path, played, perr := play(audio)
		msg := voicePreviewMsg{cost: cost, played: played, path: path}
		if perr != nil {
			msg.err = "playback failed: " + perr.Error()
		}
		return msg
	}
}

// applyVoicePreview folds a completed synth result into the model (the offersMsg-style handler
// hook, called from Update). It moves the panel to done (or error) and updates the cost actually
// billed so the panel can show the real charge.
func (m model) applyVoicePreview(msg voicePreviewMsg) model {
	m.previewCost = msg.cost
	m.previewPlayed = msg.played
	m.previewPath = msg.path
	if msg.err != "" {
		m.previewStage = previewError
		m.previewErr = msg.err
		m.status = stEmber.Render("! " + msg.err)
		return m
	}
	m.previewStage = previewDone
	switch {
	case msg.played:
		m.status = stLive.Render("played a sample") + stDim.Render(" · billed "+dollars(msg.cost))
	case msg.path != "":
		m.status = stDim.Render("no audio player found - sample saved to ") + stKey.Render(msg.path)
	default:
		m.status = stDim.Render("sample fetched")
	}
	return m
}

// --- audio playback (injectable; save-to-file fallback) -----------------------

// audioPlayerFn plays a WAV sample and reports the fallback save path (when it could not play,
// so the caller can tell the user where the file is), whether it played, and any error. Injected
// into the model (m.previewPlayer) so tests stub it without a real audio device.
type audioPlayerFn func(wav []byte) (savedPath string, played bool, err error)

// audioPlayTimeout bounds a preview so a wedged player can never block the UI indefinitely (a few
// seconds of speech + slack). On timeout the sample is already on disk (the user can replay it).
const audioPlayTimeout = 20 * time.Second

// audioEnv is the runtime environment for the real player, with the OS + exec seams injectable so
// the per-OS resolution + fallback are unit-testable without spawning a process.
type audioEnv struct {
	goos     string
	lookPath func(string) (string, error)            // exec.LookPath
	run      func(name string, args ...string) error // start + wait (bounded)
}

// systemAudioPlayer is the real player: it resolves a CLI audio player for the host OS and plays
// the sample, falling back to saving the wav when none exists.
func systemAudioPlayer(wav []byte) (string, bool, error) {
	return defaultAudioEnv().play(wav)
}

func defaultAudioEnv() audioEnv {
	return audioEnv{
		goos:     runtime.GOOS,
		lookPath: exec.LookPath,
		run: func(name string, args ...string) error {
			ctx, cancel := context.WithTimeout(context.Background(), audioPlayTimeout)
			defer cancel()
			return exec.CommandContext(ctx, name, args...).Run()
		},
	}
}

// play writes the sample to a temp .wav and runs the resolved system player on it. WAV (not mp3)
// is requested for the LOCAL preview because it is universally + trivially playable with no
// lame/ffmpeg: darwin (afplay) and windows (.NET SoundPlayer) both play it built-in, guaranteed.
// With NO player available (only possible on linux/other) it degrades gracefully: the file is left
// on disk and (path, played=false) is returned so the caller surfaces the path — it NEVER crashes
// and (via the run timeout) NEVER blocks indefinitely. On a player error the path is still
// returned (the sample is on disk to retry).
//
// SHELL-OUT ONLY (no in-process oto/beep): roger ships CGO_ENABLED=0 static across linux/darwin/
// windows × amd64/arm64, so an in-process audio lib would break the cross-compiled release build.
func (e audioEnv) play(wav []byte) (string, bool, error) {
	path, err := writeTempWAV(wav)
	if err != nil {
		return "", false, err
	}
	name, args := resolveAudioPlayer(e.goos, e.lookPath, path)
	if name == "" {
		return path, false, nil // no player: saved for the user, no crash
	}
	if err := e.run(name, args...); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// resolveAudioPlayer returns the player command + full args to play `file` on goos, or ("",nil)
// when linux/other has NOTHING on PATH (-> the save-to-file fallback). darwin + windows always
// resolve to a GUARANTEED built-in player (afplay / .NET SoundPlayer via powershell), so they
// never hit the fallback. lookPath is injected so the linux chain is testable without a real PATH.
func resolveAudioPlayer(goos string, lookPath func(string) (string, error), file string) (string, []string) {
	switch goos {
	case "darwin":
		// afplay ships with macOS — always present, plays wav natively.
		return "afplay", []string{file}
	case "windows":
		// The built-in .NET SoundPlayer plays wav SYNCHRONOUSLY (blocks until done, no duration
		// math, no external deps) — always present on Windows. Args are split (never a raw string).
		ps := fmt.Sprintf("(New-Object System.Media.SoundPlayer '%s').PlaySync()", file)
		return "powershell", []string{"-NoProfile", "-Command", ps}
	default:
		// linux (and any other unix): first on PATH wins, then degrade.
		for _, p := range linuxAudioPlayers {
			if _, err := lookPath(p.cmd); err == nil {
				return p.cmd, append(append([]string{}, p.flags...), file)
			}
		}
		return "", nil
	}
}

// linuxAudioPlayers is the ordered candidate chain for linux/other (first available wins), with
// each player's quiet / no-video / auto-exit flags so the preview plays once and returns. paplay/
// aplay/play are common and play wav directly; mpv/ffplay are heavier but ubiquitous fallbacks.
var linuxAudioPlayers = []struct {
	cmd   string
	flags []string
}{
	{"paplay", nil},
	{"aplay", []string{"-q"}},
	{"play", []string{"-q"}}, // sox
	{"mpv", []string{"--no-video", "--really-quiet"}},
	{"ffplay", []string{"-nodisp", "-autoexit", "-loglevel", "quiet"}},
}

// writeTempWAV writes the sample bytes to a uniquely-named temp .wav and returns its path.
func writeTempWAV(wav []byte) (string, error) {
	f, err := os.CreateTemp("", "rogerai-voice-*.wav")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(wav); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// --- rendering ----------------------------------------------------------------

// voicePreviewView renders the preview panel for the selected voice band: a clear VOICES header,
// the station + price, and a stage-specific body (confirm-to-spend, synthesizing, played/saved,
// the stt info panel, an off-air note, or an error + retry).
func (m model) voicePreviewView(w int) string {
	b := m.previewBand
	var s bytes.Buffer
	kind := "voice"
	switch {
	case b.isTTS():
		kind = "text-to-speech " + voiceBadgeGlyph(b) // folded (♪→>) for a legacy console
	case b.isSTT():
		kind = "speech-to-text " + voiceBadgeGlyph(b) // folded (▽→v)
	}
	s.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("VOICES") + stDim.Render("  preview") + "\n\n")
	s.WriteString("  " + stKey.Render(b.model) + stDim.Render("  ·  "+kind) + "\n")
	priceLine := "  " + stDim.Render("price  ") + stKey.Render(voicePriceLabel(b))
	s.WriteString(priceLine + "\n\n")

	switch m.previewStage {
	case previewOffline:
		s.WriteString("  " + stEmber.Render("off air") + stDim.Render(" - no station is serving this voice right now. esc goes back; r re-checks.") + "\n")
	case previewInfoSTT:
		s.WriteString("  " + stDim.Render("This is a LISTEN (speech-to-text) station: it transcribes audio you send.") + "\n")
		s.WriteString("  " + stDim.Render("There is no chat preview - send audio via the RogerAI app or the ") + stKey.Render("/v1/audio/transcriptions") + stDim.Render(" API.") + "\n")
	case previewConfirm:
		s.WriteString("  " + stEmber.Render("paid voice") + stDim.Render(" - a sample synthesizes ") + stKey.Render(strconv.Itoa(len([]rune(sampleVoiceText)))) + stDim.Render(" characters and costs about ") + stKey.Render(dollars(m.previewCost)) + stDim.Render(".") + "\n")
		s.WriteString("  " + stDim.Render("press ") + stKey.Render("⏎/y") + stDim.Render(" to play the sample (spends ") + stKey.Render(dollars(m.previewCost)) + stDim.Render("), ") + stKey.Render("n/esc") + stDim.Render(" to skip.") + "\n")
	case previewSynth:
		s.WriteString("  " + stLive.Render("synthesizing") + stDim.Render(" a sample…") + "\n")
	case previewError:
		s.WriteString("  " + stEmber.Render("! "+m.previewErr) + "\n")
		s.WriteString("  " + stDim.Render("press ") + stKey.Render("r") + stDim.Render(" to try again, ") + stKey.Render("esc") + stDim.Render(" to go back.") + "\n")
	case previewDone:
		switch {
		case m.previewPlayed:
			s.WriteString("  " + stLive.Render("♪ played a sample") + stDim.Render(" · billed ") + stKey.Render(dollars(m.previewCost)) + "\n")
		case m.previewPath != "":
			s.WriteString("  " + stDim.Render("no system audio player found - the sample was saved to:") + "\n")
			s.WriteString("  " + stKey.Render(m.previewPath) + "\n")
		default:
			s.WriteString("  " + stDim.Render("sample fetched · billed ") + stKey.Render(dollars(m.previewCost)) + "\n")
		}
		s.WriteString("  " + stDim.Render("press ") + stKey.Render("r") + stDim.Render(" to play again, ") + stKey.Render("esc") + stDim.Render(" to go back.") + "\n")
	}
	return s.String()
}

// voicePriceLabel renders a voice band's price in the metering unit that actually bills — per-1M
// CHARS for tts, per-1M audio-BYTES for stt — so the preview is honest about what a real request
// costs. A free band reads "free".
func voicePriceLabel(b band) string {
	if !b.online {
		return "-"
	}
	if b.free || b.minIn == 0 {
		return "free"
	}
	unit := "/1M chars"
	if b.isSTT() {
		unit = "/1M audio-bytes"
	}
	return dollars(b.minIn) + unit
}

// voicePreviewFooter is the contextual footer for the preview panel (stage-aware key hints).
func (m model) voicePreviewFooter() string {
	switch m.previewStage {
	case previewConfirm:
		return stDim.Render("⏎/y play sample (") + stKey.Render(dollars(m.previewCost)) + stDim.Render(")  ·  n/esc skip")
	case previewInfoSTT, previewOffline:
		return stDim.Render("esc back  ·  send audio via the app / API")
	case previewSynth:
		return stDim.Render("synthesizing…  ·  esc back")
	default:
		return stDim.Render("r play again  ·  esc back")
	}
}

// httpErrMessage turns a non-200 audio response into a short user-facing line: it prefers the
// broker's JSON {"error":...}, else a terse status summary.
func httpErrMessage(status int, body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return fmt.Sprintf("station error (%d)", status)
}

// --- THE DJ BOOTH: voice as a DIM footnote off the LLM list, drilling into a CHILD screen ------
//
// Founder framing: LLM (chat) bands are THE product. Voice (tts/stt) is additive and must NEVER
// look co-equal. So voice is NOT a section on the dial — it is a single dim line at the FOOT of
// THE BAND ("also on air: N voices ▸ [v]"), present ONLY when a voice is actually on air, that
// drills into THE DJ BOOTH (a child screen; esc returns to THE BAND). The Booth is the tts DJ
// lineup; stt sits one step further inside it (a "▸ N transcribers" line) → THE LISTENING POST.

// voiceBands returns every VOICE (tts/stt) band, in the model's grouped order. These are excluded
// from the top-level list (visibleBands) so THE BAND stays pure LLM; the Booth is where they live.
func (m model) voiceBands() []band {
	out := make([]band, 0, 4)
	for _, b := range m.bands {
		if b.isVoice() {
			out = append(out, b)
		}
	}
	return out
}

// voiceBandsOnAir counts the ON-AIR voice bands (tts + stt). It gates the footnote: the "also on
// air" line appears IFF this is > 0, so a pure-LLM screen (no voice around) shows zero voice
// affordance at all — voice is invisible until a real voice band exists.
func (m model) voiceBandsOnAir() int {
	n := 0
	for _, b := range m.voiceBands() {
		if b.online {
			n++
		}
	}
	return n
}

// llmBands is the count of LLM (chat) bands — the umbrella "models" the top-level list shows.
// Voice bands are NOT models in the headline sense (they live in the Booth), so every top-level
// "N models / N bands" count reads THIS, not len(m.bands), or the number would disagree with the
// LLM-only rows rendered below it.
func (m model) llmBands() int {
	n := 0
	for _, b := range m.bands {
		if !b.isVoice() {
			n++
		}
	}
	return n
}

// llmBandsOnAir / llmStationsOnAir count the ON-AIR LLM (chat) bands and their stations — the
// honest "what's live to chat" figures for the header + ambient status, excluding voice.
func (m model) llmBandsOnAir() int {
	n := 0
	for _, b := range m.bands {
		if !b.isVoice() && b.online {
			n++
		}
	}
	return n
}

func (m model) llmStationsOnAir() int {
	n := 0
	for _, o := range m.offers {
		if o.Online && canonModality(o.Modality) == protocol.ModalityChat {
			n++
		}
	}
	return n
}

// boothDJs is the DJ BOOTH lineup: the ON-AIR tts voices (the ones a listener can cue/preview),
// ordered strongest-first so the booth reads like the band list. stt is NOT a DJ (it doesn't
// speak) — it lives in the Listening Post, reached from the Booth's transcriber line.
func (m model) boothDJs() []band {
	out := make([]band, 0, 4)
	for _, b := range m.voiceBands() {
		if b.isTTS() && b.online {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return bandSignal(out[i]) > bandSignal(out[j]) })
	return out
}

// boothTranscribers is the ON-AIR stt lineup surfaced by the Listening Post (info only).
func (m model) boothTranscribers() []band {
	out := make([]band, 0, 4)
	for _, b := range m.voiceBands() {
		if b.isSTT() && b.online {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return bandSignal(out[i]) > bandSignal(out[j]) })
	return out
}

// voiceFootnote is the ONE dim line at the foot of THE BAND: "also on air: N voices ▸ [v]". It is
// the quietest live line on the screen — stDim only, NO ◉ beacon, NO accent red — so voice can
// never visually rival the LLM bands above it. The caller (browseView) draws it ONLY when
// voiceBandsOnAir() > 0. `[v]` / ▸ drills into the Booth (a child screen), not a dial stop.
func (m model) voiceFootnote() string {
	n := m.voiceBandsOnAir()
	if n <= 0 {
		return ""
	}
	// plural() yields "1 voice" / "N voices" — the singular reads right for a lone voice.
	return "  " + stDim.Render("also on air: "+plural(n, "voice")+" "+glyphs.Fold("▸")+" ") + stKey.Render("[v]")
}

// enterBooth opens THE DJ BOOTH child screen (from the footnote / `v`). It is a NO-OP when no
// voice is on air (the affordance is absent then), so `v` never lands on an empty voice screen.
// The Booth is a CHILD of THE BAND: esc returns to modeBrowse, and [1]/section keys go to THE
// BAND — voice is never the default landing.
func (m model) enterBooth() (tea.Model, tea.Cmd) {
	if m.voiceBandsOnAir() <= 0 {
		return m, nil
	}
	m.mode = modeVoiceBooth
	m.boothCursor = 0
	m.status = stDim.Render("THE DJ BOOTH — voices on air · " + glyphs.Fold("▶") + " spin a sample · enter to cue")
	return m, nil
}

// onVoiceBoothKey drives the DJ BOOTH lineup. esc/←/q returns to THE BAND (this is a child
// screen). ↑/↓ move the cursor over the DJs. enter CUEs the selected DJ — opening the money-gated
// preview (startVoicePreview: FREE plays now, PAID holds at the confirm gate; NEVER a chat
// channel). ▶/space "spins" a sample (the same preview entry — spin=sample, cue=endpoint, both
// route through the preserved preview panel). `t` drills into THE LISTENING POST (stt info).
func (m model) onVoiceBoothKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	djs := m.boothDJs()
	switch k.String() {
	case "esc", "left", "h", "q":
		m.mode = modeBrowse
		m.status = stDim.Render("back to THE BAND")
		return m, nil
	case "1":
		// TUNE IN: THE BAND is the home of TUNE IN; leaving the Booth lands there, never voices.
		m.mode = modeBrowse
		return m, nil
	case "t", "T":
		// Drill one step further to the quieter stt lineup (info/how-to), if any transcriber is up.
		if len(m.boothTranscribers()) == 0 {
			m.status = stDim.Render("no transcribers on air right now")
			return m, nil
		}
		m.mode = modeListeningPost
		m.boothCursor = 0
		m.status = stDim.Render("THE LISTENING POST — transcribers (send audio, not chat)")
		return m, nil
	case "up", "k":
		if m.boothCursor > 0 {
			m.boothCursor--
		}
		return m, nil
	case "down", "j":
		if m.boothCursor < len(djs)-1 {
			m.boothCursor++
		}
		return m, nil
	case "enter", " ", "▶", "p", "P":
		// Cue (enter) or spin a sample (▶/space/p): both open the preview endpoint on the DJ. The
		// money gate lives inside startVoicePreview — a PAID DJ holds at the confirm before any
		// spend; a FREE DJ plays now. A DJ is NEVER chatted.
		if len(djs) == 0 {
			return m, nil
		}
		i := m.boothCursor
		if i < 0 || i >= len(djs) {
			i = 0
		}
		return m.startVoicePreview(djs[i])
	}
	return m, nil
}

// onListeningPostKey drives THE LISTENING POST (stt info/how-to). It is INFO ONLY — there is no
// sample to spin and an stt band is never chatted. esc/← returns to the Booth (its parent).
func (m model) onListeningPostKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "left", "h", "q":
		m.mode = modeVoiceBooth
		m.boothCursor = 0
		m.status = stDim.Render("back to THE DJ BOOTH")
		return m, nil
	case "1":
		m.mode = modeBrowse
		return m, nil
	}
	return m, nil
}

// boothPricePer1k renders a tts DJ's price in its REAL headline unit — $/1k chars — because that
// is how tts bills (per input char; minIn is the per-1M-char rate). NOT $/1M-out (meaningless for
// a voice). FREE for a free/free-now DJ.
func boothPricePer1k(b band) string {
	if b.free || b.minIn == 0 {
		return "FREE"
	}
	return dollars(b.minIn / 1000) // dollars() already prepends "$"
}

// voiceBadgeGlyph is the DJ/transcriber modality mark ROUTED through the single voiceBadge source
// and folded for a legacy console (♪→>, ▽→v) — so the Booth/Post rows use one badge definition and
// obey the ASCII-fold rule (the DELTA's hard requirement after the un-foldable 🎤).
func voiceBadgeGlyph(b band) string { return glyphs.Fold(voiceBadge(b)) }

// emDash is the folded "none"/absent mark (—, folds to - on a legacy console). Used for the
// Booth's not-yet-populated lang/sample cells so they degrade like every other glyph.
func emDash() string { return glyphs.Fold("—") }

// boothSampleGlyph is ♪ when the DJ published a broker-hosted sample clip, else — (none). The TUI
// offer does not yet carry sample_url from /discover (that is producer-side plumbing, a separate
// pass), so today this reads — for every DJ; the column + glyph are in place for when it lands.
func boothSampleGlyph(b band) string { return emDash() }

// voiceBoothView renders THE DJ BOOTH: a k9s table of the on-air tts DJs
// (dj · on air · $/1k ch · lang · lat · sample · signal) with the reverse-video cursor row + the
// honest signal tower, and a dim "▸ N transcribers" line into the Listening Post. It makes its
// SUBORDINATE, drill-in nature explicit ("esc → THE BAND") so it never reads as a peer section.
func (m model) voiceBoothView(w int) string {
	var b strings.Builder
	djs := m.boothDJs()
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("THE DJ BOOTH") +
		stDim.Render(fmt.Sprintf("   %s on air", plural(len(djs), "voice"))) +
		stDim.Render("   ·   esc "+glyphs.Fold("→")+" THE BAND") + "\n")
	b.WriteString("  " + stDim.Render("shared voices you can cue "+emDash()+" point your app/CLI at a DJ to speak your text (you never chat it)") + "\n\n")

	if len(djs) == 0 {
		b.WriteString("  " + stDim.Render("no DJs on air right now.") + "\n")
	} else {
		wide := !m.narrow() && w >= 88
		if wide {
			b.WriteString("  " + stDim.Render(fmt.Sprintf("%-22s  %-8s  %-9s  %-6s  %-6s  %-6s  %s",
				"dj", "on air", "$/1k ch", "lang", "lat", "sample", "signal")) + "\n")
		} else {
			b.WriteString("  " + stDim.Render(fmt.Sprintf("%-18s  %-8s  %-9s", "dj", "on air", "$/1k ch")) + "\n")
		}
		tableW := w - 2
		if tableW < 20 {
			tableW = 20
		}
		cur := m.boothCursor
		if cur >= len(djs) {
			cur = len(djs) - 1
		}
		if cur < 0 {
			cur = 0
		}
		for i, dj := range djs {
			sel := i == cur
			onair := fmt.Sprintf("%d on", dj.stations)
			price := boothPricePer1k(dj)
			nameW, priceW := 18, 9
			if wide {
				nameW = 22
			}
			var sigSignal int
			var sigTPS float64
			if dj.cheapest != nil {
				sigSignal = dj.cheapest.Signal
				sigTPS = dj.cheapest.TPS
			}
			if wide {
				lat := "-"
				if dj.cheapest != nil {
					lat = fmtTtft(dj.cheapest.TTFTMs)
				}
				if sel {
					rawSig := pad(signalBarsRaw(m.sigFrame(), sigSignal, sigTPS, dj.online, dj.inFlight, dj.stations), 6)
					plain := fmt.Sprintf("%s  %s  %s  %s  %s  %s  %s",
						pad(dj.model, nameW), pad(onair, 8), pad(price, priceW), pad(emDash(), 6), pad(lat, 6), pad(boothSampleGlyph(dj), 6), rawSig)
					b.WriteString(m.caratGutter() + rowSel(true, plain, tableW) + "\n")
					continue
				}
				sig := tintSignal(pad(signalBarsRaw(m.sigFrame(), sigSignal, sigTPS, dj.online, dj.inFlight, dj.stations), 6), sigSignal, sigTPS, dj.online)
				priceCell := stEmber.Render(pad(price, priceW))
				if price == "FREE" {
					priceCell = stLive.Render(pad(price, priceW))
				}
				b.WriteString(selCarat(false) + " " + stGold.Render(voiceBadgeGlyph(dj)) + " " + stKey.Render(pad(dj.model, nameW-2)) + "  " +
					stDim.Render(pad(onair, 8)) + "  " + priceCell + "  " + stDim.Render(pad(emDash(), 6)) + "  " +
					stDim.Render(pad(lat, 6)) + "  " + stDim.Render(pad(boothSampleGlyph(dj), 6)) + "  " + sig + "\n")
				continue
			}
			// Narrow: dj · on air · $/1k ch only (mirrors the chat narrow grid).
			if sel {
				plain := fmt.Sprintf("%s  %s  %s", pad(dj.model, nameW), pad(onair, 8), pad(price, priceW))
				b.WriteString(m.caratGutter() + rowSel(true, plain, tableW) + "\n")
				continue
			}
			priceCell := stEmber.Render(pad(price, priceW))
			if price == "FREE" {
				priceCell = stLive.Render(pad(price, priceW))
			}
			b.WriteString(selCarat(false) + " " + stGold.Render(voiceBadgeGlyph(dj)) + " " + stKey.Render(pad(dj.model, nameW-2)) + "  " +
				stDim.Render(pad(onair, 8)) + "  " + priceCell + "\n")
		}
	}

	// The stt lineup is quieter still: a single dim drill-line into the Listening Post, present
	// only when a transcriber is on air. stt gets NO top-line affordance of its own.
	if nt := len(m.boothTranscribers()); nt > 0 {
		b.WriteString("\n  " + stDim.Render(glyphs.Fold("▸")+" "+plural(nt, "transcriber")+" "+glyphs.Fold("▽")+" listen "+emDash()+" ") +
			stKey.Render("[t]") + stDim.Render(" the listening post (send audio, not chat)") + "\n")
	}
	b.WriteString("\n  " + stDim.Render("rog "+glyphs.Fold("›")+" "+glyphs.Fold("▶")+" spin a sample  ·  enter to cue this DJ (open a voice endpoint)") + "\n")
	return b.String()
}

// voiceBoothFooter is the DJ BOOTH key-hint footer: cue is the endpoint verb, spin is the sample.
func (m model) voiceBoothFooter() string {
	if m.narrow() {
		return stDim.Render("↑↓ · " + glyphs.Fold("▶") + " spin · ⏎ cue · t post · esc " + glyphs.Fold("→") + " band")
	}
	return stDim.Render("↑↓ pick  ·  " + glyphs.Fold("▶") + "/p spin a sample  ·  ⏎ cue (open endpoint)  ·  t listening post  ·  esc " + glyphs.Fold("→") + " THE BAND")
}

// listeningPostView renders THE LISTENING POST: an INFO/how-to panel for the on-air stt
// transcribers. A transcriber turns AUDIO INTO TEXT — it does NOT chat and has no sample to spin,
// so this is deliberately not a preview surface. esc returns to the Booth.
func (m model) listeningPostView(w int) string {
	var b strings.Builder
	posts := m.boothTranscribers()
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("THE LISTENING POST") +
		stDim.Render(fmt.Sprintf("   %s on air", plural(len(posts), "transcriber"))) +
		stDim.Render("   ·   esc "+glyphs.Fold("→")+" THE DJ BOOTH") + "\n\n")
	b.WriteString("  " + stDim.Render("A transcriber turns AUDIO INTO TEXT. It does not chat and has no sample to spin "+emDash()) + "\n")
	b.WriteString("  " + stDim.Render("send it an audio file and get a transcript back.") + "\n\n")

	if len(posts) == 0 {
		b.WriteString("  " + stDim.Render("no transcribers on air right now.") + "\n")
	} else {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("%-22s  %-8s  %s", "transcriber", "on air", "~$/min*")) + "\n")
		for _, p := range posts {
			price := "FREE"
			if !p.free && p.minIn > 0 {
				// stt bills by audio-BYTES (minIn is per-1M-bytes); a per-minute figure is an
				// ESTIMATE at a nominal bitrate — marked * so it reads as a friendly guess, not a
				// billed rate. ~128kbps ≈ 960,000 bytes/min. dollars() already prepends "$".
				price = dollars(p.minIn*960000/1e6) + "*"
			}
			b.WriteString("  " + stGold.Render(voiceBadgeGlyph(p)) + " " + stKey.Render(pad(p.model, 20)) + "  " +
				stDim.Render(pad(fmt.Sprintf("%d on", p.stations), 8)) + "  " + stEmber.Render(price) + "\n")
		}
		b.WriteString("  " + stDim.Render("* billed by uploaded audio bytes; per-minute is an estimate at ~128kbps") + "\n")
	}
	b.WriteString("\n  " + stDim.Render("how to use "+emDash()+" POST your audio to the endpoint (the app records + uploads; the CLI takes a file):") + "\n")
	b.WriteString("  " + stKey.Render("roger transcribe --model <name> path/to/audio.m4a") + "\n")
	return b.String()
}

// listeningPostFooter is the how-to footer for THE LISTENING POST (no preview keys).
func (m model) listeningPostFooter() string {
	return stDim.Render("send audio via the app / API  ·  esc " + glyphs.Fold("→") + " THE DJ BOOTH")
}
