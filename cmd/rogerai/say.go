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

	"github.com/rogerai-fyi/roger/internal/audio"
	"github.com/rogerai-fyi/roger/internal/client"
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
	voice := fs.String("voice", "", "the voice to speak in: a model id, or the @login/name from `roger voices`")
	speed := fs.Float64("voice-speed", 0, "playback speed (0.5-2.0; 0 = the voice's default)")
	fs.Usage = func() {
		fmt.Print(`roger say - speak a line through a shared voice and play it locally

  roger say --voice <voice> "roger that"     synthesize + play
  roger voices                               list on-air voices (cheapest first)

  --voice <voice>       REQUIRED: a voice model id, or the @login/name from ` + "`roger voices`" + `
  --voice-speed <n>     playback speed (0.5-2.0; default: the voice's own)

Voices are metered per character you speak, billed to your wallet (self/free is $0).
Browse the roster with ` + "`roger voices`" + ` or at rogerai.fyi/voices.
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *voice == "" {
		return fmt.Errorf("which voice? pass --voice <voice> - list the on-air roster with `roger voices` (or browse rogerai.fyi/voices)")
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
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
	voices, err := client.Voices(cfg.Broker)
	if err != nil {
		return err
	}
	if len(voices) == 0 {
		fmt.Println("no voices on air right now - run `roger share` on a box with a local voice server, or check rogerai.fyi/voices.")
		return nil
	}
	fmt.Printf("%d voice(s) on air (cheapest first) - speak with `roger say --voice <voice> \"...\"`:\n\n", len(voices))
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

// voiceHandle is the id a consumer passes to --voice: the human-friendly @login/name alias when the
// broker emitted one, else the raw model id (both route at the broker).
func voiceHandle(v client.Voice) string {
	if v.NamespacedID != "" {
		return v.NamespacedID
	}
	return v.ID
}
