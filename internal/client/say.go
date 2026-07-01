package client

// say.go is the consumer side of TTS: Speak signs + POSTs a line to the broker's /v1/audio/speech
// (the SAME signed spend-auth the chat relay uses — the broker derives the billed wallet from the
// signature pubkey, not a header) and returns the WAV + the exact billed cost; Voices reads the
// public /voices roster. `roger say` / `roger voices` are the CLI front-ends. WAV is requested
// (response_format:"wav") so the returned audio is trivially playable cross-platform with no
// lame/ffmpeg (see internal/audio).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// SpeakResult is the outcome of one TTS synth: the returned audio bytes and the exact credits the
// broker billed (from the X-RogerAI-Cost meter header; 1 credit == $1, self/free == 0).
type SpeakResult struct {
	Audio []byte
	Cost  float64
}

// speakTimeout bounds one synth. TTS is a short, non-streaming request (a line of speech), so a
// modest ceiling is plenty while still tolerating a cold voice server.
const speakTimeout = 60 * time.Second

// speakAudioLimit caps the WAV we read back (a spoken line is small; this is a belt-and-suspenders
// guard against a runaway body).
const speakAudioLimit = 32 << 20

// Speak synthesizes `text` through the shared voice `model` and returns the audio + billed cost.
// The request is signed with the local user key (client.SignRequest) so the broker bills the
// verified wallet; the body is the OpenAI-shaped {model, input, response_format:"wav"[, speed]}.
// speed rides ONLY when > 0 (0 = the server/voice default). Errors map the broker's real statuses
// to clear, human messages: the uniform 503 no-station, the anon-paid 403 sign-in gate, the 402
// funds error (with the topup hint), and a transport failure -> "broker unreachable".
func Speak(broker, user, model, text string, speed float64) (SpeakResult, error) {
	payload := map[string]any{
		"model":           model,
		"input":           text,
		"response_format": "wav",
	}
	if speed > 0 {
		payload["speed"] = speed
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, broker+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return SpeakResult{}, fmt.Errorf("could not build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Signed spend-auth: the broker derives the billed wallet from the SIGNATURE pubkey, not from
	// any header (X-Roger-User is only a legacy, unauthenticated hint). Self/free stays $0.
	signRequest(req, body)
	if user != "" {
		req.Header.Set("X-Roger-User", user)
	}

	hc := &http.Client{Timeout: speakTimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return SpeakResult{}, fmt.Errorf("broker unreachable: %v", err)
	}
	defer resp.Body.Close()
	audio, _ := io.ReadAll(io.LimitReader(resp.Body, speakAudioLimit))
	if resp.StatusCode != http.StatusOK {
		// Reuse the chat relay's error parser: it reads the broker's NESTED {"error":{"message":...}}
		// shape (jsonErr), so the uniform 503 no-station / 403 sign-in-gate text passes through
		// verbatim, appends the topup hint on a 402 (WithTopupHint), and falls back to a terse status
		// summary — one source of truth for "turn a broker error body into a human line".
		return SpeakResult{}, parseChatError(audio, resp.StatusCode)
	}
	cost, _ := strconv.ParseFloat(resp.Header.Get("X-RogerAI-Cost"), 64)
	return SpeakResult{Audio: audio, Cost: cost}, nil
}

// Voice is one entry in the broker's /voices roster (the shape GET /voices emits). The CLI renders
// it as "Name · by @operator · language · $price/1k chars" (or FREE). ID is the raw model id (the
// broker routes on it); NamespacedID is the human-friendly @<station>/<name> alias when present. NO
// node address ever appears (the broker strips it).
type Voice struct {
	ID              string  `json:"id"`
	NamespacedID    string  `json:"namespaced_id,omitempty"`
	Operator        string  `json:"operator,omitempty"`
	Name            string  `json:"name,omitempty"`
	PricePer1kChars float64 `json:"price_per_1k_chars"`
	Free            bool    `json:"free"`
	Language        string  `json:"language,omitempty"`
	LatencyMs       int     `json:"latency_ms,omitempty"`
	SampleURL       string  `json:"sample_url,omitempty"`
}

// Voices reads the broker's public voice roster (GET /voices — anonymous, no auth), cheapest first
// (the broker sorts). A transport failure OR a non-2xx status is a graceful "broker unreachable";
// an empty roster is a clean empty slice (the CLI then prints the friendly "no voices" line).
func Voices(broker string) ([]Voice, error) {
	resp, err := http.Get(broker + "/voices")
	if err != nil {
		return nil, fmt.Errorf("broker unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("broker unreachable: status %d", resp.StatusCode)
	}
	var d struct {
		Voices []Voice `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("could not read the voice roster: %w", err)
	}
	return d.Voices, nil
}
