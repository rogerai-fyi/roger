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
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/client"
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

// voiceBadge is the tiny modality glyph shown on a voice band row (♪ speak / 🎤 listen), so a
// voice station is unmistakable in the list. Empty for a chat band.
func voiceBadge(b band) string {
	switch {
	case b.isTTS():
		return "♪"
	case b.isSTT():
		return "🎤"
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
		kind = "text-to-speech " + voiceBadge(b)
	case b.isSTT():
		kind = "speech-to-text " + voiceBadge(b)
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
