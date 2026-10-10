package main

// say.go is the `roger say` / `roger voices` CLI. `say` synthesizes a line through a shared voice
// and plays it locally; `voices` lists the on-air roster so a consumer can pick a --voice id. It
// SPENDS (TTS is char-metered), so it reuses the SAME signed spend-auth `roger use` does — via
// client.Speak, which signs the request; the broker bills the verified wallet. Playback runs
// through the shared internal/audio player (extracted from the TUI), with a save-to-file fallback
// when no system player exists.

import (
	"flag"
	"fmt"
	"strings"

	"rogerai.fm/roger/v6/internal/audio"
	"rogerai.fm/roger/v6/internal/client"
)

// sayPlayer is the injectable audio player seam (default the shared real player). A test points it
// at a stub so cmdSay's play path runs without a real audio device.
var sayPlayer audio.PlayerFn = audio.SystemPlayer

// cmdSay: roger say [--voice <voice>] [--voice-speed <n>] <text...>
//
// It joins the positional words into the line, signs + POSTs them to the broker's /v1/audio/speech
// (client.Speak), plays the returned WAV, and prints the char count + billed cost. --voice is
// REQUIRED: without it we error with a hint (never guess a voice, never spend). No text is a usage
// error. Every money/reachability failure surfaces the broker's own clear message (the 402/403/503
// gates, or a graceful "broker unreachable"), and nothing plays on an error.
func cmdSay(cfg config, args []string) error {
	fs := flag.NewFlagSet("say", flag.ContinueOnError)
	voice := fs.String("voice", "", "the text-to-speech (TTS) model to speak with: a model id, or the @station/name from `roger voices`")
	speed := fs.Float64("voice-speed", 0, "playback speed (0.5-2.0; 0 = the TTS model's default)")
	fs.Usage = func() {
		fmt.Print(`roger say - speak a line through a shared text-to-speech (TTS) model and play it locally

  roger say --voice <voice> "roger that"     synthesize + play
  roger voices                               list on-air text-to-speech (TTS) models (cheapest first)

  --voice <voice>       REQUIRED: a TTS model id, or the @station/name from ` + "`roger voices`" + `
  --voice-speed <n>     playback speed (0.5-2.0; default: the TTS model's own)

Text-to-speech is metered per character you speak, billed to your wallet (self/free is $0).
Browse the roster with ` + "`roger voices`" + ` or at rogerai.fm/voices.
`)
	}
	list := fs.Bool("list", false, "list the text-to-speech (TTS) models you can speak through (in LAN mode: only local ones)")
	words, err := parseInterspersed(fs, args) // `roger say hello --voice x` works as well as the flag first
	if err != nil {
		return err
	}
	if cfg.lan.LAN {
		// voice uses the same filter as text (features/lan/voice.feature): only a voice on this
		// machine or an Edge member, and nothing is asked of the network's /v1/audio/speech
		return sayLAN(cfg, *voice, strings.TrimSpace(strings.Join(words, " ")), *speed, *list)
	}
	if *list {
		return cmdVoices(cfg, nil)
	}
	if *voice == "" {
		return fmt.Errorf("which voice? pass --voice <voice> - list the on-air roster with `roger voices` (or browse rogerai.fm/voices)")
	}
	text := strings.TrimSpace(strings.Join(words, " "))
	if text == "" {
		return fmt.Errorf("nothing to say - usage: roger say --voice %s \"your text\"", *voice)
	}

	res, err := client.Speak(cfg.Broker, cfg.User, *voice, text, *speed)
	if err != nil {
		return sayError(err)
	}
	// Play the returned WAV (or save it when no player exists). A play error still yields the saved
	// path, so the user can retry the file — never a crash.
	path, played, perr := sayPlayer(res.Audio)
	fmt.Println(sayResultLine(text, res, played, path))
	if perr != nil && !played && path == "" {
		// The only genuinely unhappy case: could not play AND could not save. Surface it.
		return fmt.Errorf("could not play or save the audio: %w", perr)
	}
	return nil
}

// sayError wraps a client.Speak failure with an actionable next step where one helps: the anon-paid
// sign-in gate points at `roger login`; the broker's other messages (no-station, funds+topup hint,
// unreachable) are already clear and pass through verbatim.
func sayError(err error) error {
	if strings.Contains(err.Error(), "sign in to use this voice model") {
		return fmt.Errorf("%v - run `roger login` (or use a free voice)", err)
	}
	return err
}

// sayResultLine is the one-line outcome: `spoke N chars · $X` on a play (N = rune count, the cost
// via the canonical money renderer), or the saved path when no player was available.
func sayResultLine(text string, res client.SpeakResult, played bool, path string) string {
	n := len([]rune(text))
	if !played && path != "" {
		return fmt.Sprintf("no audio player found - saved the clip to %s  (%d chars · %s)", path, n, client.FormatUSD(res.Cost))
	}
	return fmt.Sprintf("spoke %d chars · %s", n, client.FormatUSD(res.Cost))
}

// cmdVoices: roger voices - list the on-air voice roster (GET /voices), cheapest first, as
// `Name · by @operator · language · $price/1k chars` (or FREE), with the id to pass to --voice.
func cmdVoices(cfg config, _ []string) error {
	if cfg.lan.LAN {
		// the roster follows the LAN filter like every band list: only voices on this machine
		// or an Edge member, and the network's /voices is never asked (features/lan/voice.feature)
		return sayLAN(cfg, "", "", 0, true)
	}
	voices, err := client.Voices(cfg.Broker)
	if err != nil {
		return err
	}
	if len(voices) == 0 {
		fmt.Println("no text-to-speech (TTS) models on air right now - run `roger share` on a box with a local TTS server, or check rogerai.fm/voices.")
		return nil
	}
	fmt.Printf("%d text-to-speech (TTS) model(s) on air (cheapest first) - speak with `roger say --voice <model> \"...\"`:\n\n", len(voices))
	for _, v := range voices {
		fmt.Println(voiceRosterLine(v))
	}
	return nil
}

// voiceRosterLine renders one roster row. Price is in $/1k chars (how tts bills), FREE for a
// free/zero-price voice. The --voice handle (the namespaced alias when present, else the raw id) is
// shown so a consumer can copy exactly what to pass.
func voiceRosterLine(v client.Voice) string {
	name := v.Name
	if name == "" {
		name = v.ID
	}
	parts := []string{name}
	if v.Operator != "" {
		parts = append(parts, "by @"+v.Operator)
	}
	if v.Language != "" {
		parts = append(parts, v.Language)
	}
	if v.Free || v.PricePer1kChars == 0 {
		parts = append(parts, "FREE")
	} else {
		parts = append(parts, "$"+trimAmt(v.PricePer1kChars)+"/1k chars")
	}
	return fmt.Sprintf("  %s\n      --voice %s", strings.Join(parts, " · "), voiceHandle(v))
}

// voiceHandle is the id a consumer passes to --voice: the human-friendly @station/name alias when the
// broker emitted one, else the raw model id (both route at the broker).
func voiceHandle(v client.Voice) string {
	if v.NamespacedID != "" {
		return v.NamespacedID
	}
	return v.ID
}

// parseInterspersed parses fs over args, allowing flags after positional words, and returns the
// positional words in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return words, nil
		}
		words = append(words, args[0])
		args = args[1:]
	}
}

// lanTTSStartHint says how to put a local text-to-speech model on air.
const lanTTSStartHint = "start one with `roger model serve <tts model>`, or `roger share` on a machine running a TTS server"

// sayLAN is `roger say` in LAN mode (features/lan/voice.feature): a voice on this machine, then on
// an Edge member, or a refusal. Nothing is sent to the network's /v1/audio/speech, and no balance
// is read: LAN speech is free. A LAN Tower is never asked, because it has no audio route.
func sayLAN(cfg config, voice, text string, speed float64, list bool) error {
	voices := lanVoices(voice)
	if list {
		if len(voices) == 0 {
			fmt.Println("no local text-to-speech (TTS) model is on air (LAN mode is on, so only TTS models on this machine or your Edge are listed) - " + lanTTSStartHint)
			return nil
		}
		fmt.Printf("%d local text-to-speech (TTS) model(s) - speak with `roger --lan say --voice <model> \"...\"`:\n\n", len(voices))
		for _, v := range voices {
			fmt.Printf("  %-28s %s  · free\n", v.Band, v.Source)
		}
		return nil
	}
	if len(voices) == 0 {
		why := "no local text-to-speech (TTS) model is on air"
		if voice != "" {
			why = voice + " is not on air on this LAN"
		}
		msg := "LAN mode is on: " + why + " - " + lanTTSStartHint + " (this machine or an Edge member), or run without --lan to use the network's TTS models"
		if len(cfg.LanTowers) > 0 {
			msg += " (LAN Towers do not serve text-to-speech)"
		}
		return fmt.Errorf("%s", msg)
	}
	if text == "" {
		return fmt.Errorf("nothing to say - usage: roger --lan say \"your text\"")
	}
	band := voices[0].Band // this machine first, then Edge members: deterministic
	var note string
	if voice == "" && len(lanVoiceBands(voices)) > 1 {
		note = "using the text-to-speech (TTS) model " + band + " on " + voices[0].Source + " - pick another with --voice <model> (roger --lan say --list)"
	}
	var same []client.LANVoice
	for _, v := range voices {
		if v.Band == band {
			same = append(same, v)
		}
	}
	res, err := speakLAN(same, text, speed)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Println(note)
	}
	for _, l := range res.Left {
		fmt.Println("moved past " + l)
	}
	path, played, perr := sayPlayer(res.Audio)
	n := len([]rune(text))
	where := res.Voice.Band + " on " + res.Voice.Source
	if !played && path != "" {
		fmt.Printf("no audio player found - saved the clip to %s  (%d chars · spoken locally by %s · free)\n", path, n, where)
	} else {
		fmt.Printf("spoke %d chars · spoken locally by %s · free\n", n, where)
	}
	if perr != nil && !played && path == "" {
		return fmt.Errorf("could not play or save the audio: %w", perr)
	}
	return nil
}

// lanVoiceBands is the distinct voice bands among voices.
func lanVoiceBands(voices []client.LANVoice) map[string]bool {
	out := map[string]bool{}
	for _, v := range voices {
		out[v.Band] = true
	}
	return out
}
