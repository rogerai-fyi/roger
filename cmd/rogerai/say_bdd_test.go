package main

// say_bdd_test.go makes features/voice/say.feature EXECUTABLE, driving the REAL cmdSay / cmdVoices
// handlers + the REAL client.Speak / client.Voices against an httptest broker. It stubs ONLY the
// cross-platform audio player (the sayPlayer seam) — no domain mocks: the request is really signed
// (client.SignRequest) and the broker really parses the {model,input,response_format,speed} body,
// bills via X-RogerAI-Cost, and returns the uniform 503 / anon-paid 403 / 402 / down errors.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type sayState struct {
	t *testing.T

	// broker behaviour knobs.
	billCost   float64 // X-RogerAI-Cost the broker reports on a 200
	rejectCode int     // when >0, the broker rejects /v1/audio/speech with this status + rejectMsg
	rejectMsg  string
	unreach    bool    // point the client at a dead address (broker unreachable)
	voices     []voice // /voices roster to serve
	voicesSet  bool    // a voices table was supplied (even if empty)

	// player seam knobs.
	noPlayer bool // no audio player on this host -> save-to-file fallback

	// captured request state.
	gotBody   map[string]any // decoded /v1/audio/speech body
	gotSigned bool           // the speech POST carried a VALID signature
	postCount int            // how many /v1/audio/speech POSTs the broker saw
	played    bool           // the player was handed audio
	playedWAV []byte

	// outcome.
	out string
	err error
	srv *httptest.Server
}

// voice mirrors the /voices roster entry shape the broker emits (subset the CLI renders).
type voice struct {
	ID              string  `json:"id"`
	NamespacedID    string  `json:"namespaced_id,omitempty"`
	Operator        string  `json:"operator,omitempty"`
	Name            string  `json:"name,omitempty"`
	Language        string  `json:"language,omitempty"`
	PricePer1kChars float64 `json:"price_per_1k_chars"`
	Free            bool    `json:"free"`
}

func (s *sayState) reset(t *testing.T) {
	s.t = t
	s.billCost, s.rejectCode, s.rejectMsg = 0, 0, ""
	s.unreach, s.voices, s.voicesSet = false, nil, false
	s.noPlayer = false
	s.gotBody, s.gotSigned, s.postCount = nil, false, 0
	s.played, s.playedWAV = false, nil
	s.out, s.err = "", nil
	s.srv = nil
}

// startBroker spins the httptest broker for this scenario: /v1/audio/speech verifies the signature,
// records the body, and returns either the configured rejection or a 200 WAV + the cost header;
// /voices returns the configured roster.
func (s *sayState) startBroker() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		s.postCount++
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &s.gotBody)
		// Verify the client's signature end-to-end (the P0 spend-auth): a valid sig proves the
		// request was signed with the local user key over (method, path, body).
		ts, _ := strconv.ParseInt(r.Header.Get(protocol.HeaderTS), 10, 64)
		if _, ok := protocol.VerifyRequest(r.Header.Get(protocol.HeaderPubkey), r.Header.Get(protocol.HeaderSig), ts, r.Method, r.URL.Path, body); ok {
			s.gotSigned = true
		}
		if s.rejectCode > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.rejectCode)
			// The broker's REAL error shape (cmd/rogerai-broker/httputil.go jsonErr): nested
			// {"error":{"message":...}}. Emitting it verbatim keeps this stub honest — a wrong client
			// parser must fail here, not pass against a shape the broker never sends.
			eb, _ := json.Marshal(map[string]any{"error": map[string]string{"message": s.rejectMsg}})
			_, _ = w.Write(eb)
			return
		}
		w.Header().Set("X-RogerAI-Cost", strconv.FormatFloat(s.billCost, 'f', -1, 64))
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("RIFF....WAVEfake"))
	})
	mux.HandleFunc("/voices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(map[string]any{"voices": s.voices})
		_, _ = w.Write(b)
	})
	s.srv = httptest.NewServer(mux)
	s.t.Cleanup(s.srv.Close)
}

// brokerURL returns the address cmdSay/cmdVoices should hit: a dead one for the unreachable
// scenario, else the live httptest broker.
func (s *sayState) brokerURL() string {
	if s.unreach {
		return "http://127.0.0.1:1" // nothing listens here -> a clean transport failure
	}
	if s.srv == nil {
		s.startBroker()
	}
	return s.srv.URL
}

// installPlayer points the sayPlayer seam at a stub that records the audio it was handed (or, when
// noPlayer, the save-to-file fallback: no play, return a path). Restored on cleanup.
func (s *sayState) installPlayer() {
	orig := sayPlayer
	s.t.Cleanup(func() { sayPlayer = orig })
	if s.noPlayer {
		sayPlayer = func(wav []byte) (string, bool, error) {
			p, werr := writeStubWAV(wav)
			return p, false, werr
		}
		return
	}
	sayPlayer = func(wav []byte) (string, bool, error) {
		s.played, s.playedWAV = true, wav
		return "", true, nil
	}
}

// runSay drives the real cmdSay with argv, capturing stdout and the returned error.
func (s *sayState) runSay(argv []string) {
	s.installPlayer()
	cfg := config{Broker: s.brokerURL(), User: "u_gh_1"}
	s.out = captureStdout(s.t, func() { s.err = cmdSay(cfg, argv) })
}

func (s *sayState) runVoices() {
	cfg := config{Broker: s.brokerURL(), User: "u_gh_1"}
	s.out = captureStdout(s.t, func() { s.err = cmdVoices(cfg, nil) })
}

// --- Given -------------------------------------------------------------------

func (s *sayState) signedFundedConsumer() error      { writeAuth(s.t); return nil }
func (s *sayState) voiceOnAir(_ string, _ int) error { return nil } // shape only; the broker stub owns billing
func (s *sayState) billsCost(c float64) error        { s.billCost = c; return nil }
func (s *sayState) brokerUnreachable() error         { s.unreach = true; return nil }

func (s *sayState) rejectsWith(code int, msg string) error {
	s.rejectCode, s.rejectMsg = code, msg
	return nil
}

func (s *sayState) noAudioPlayer() error { s.noPlayer = true; return nil }

func (s *sayState) listsVoicesTable(tbl *godog.Table) error {
	s.voicesSet = true
	// row 0 is the header; columns: id operator name language price_per_1k_chars free
	col := map[string]int{}
	for i, c := range tbl.Rows[0].Cells {
		col[c.Value] = i
	}
	for _, row := range tbl.Rows[1:] {
		price, _ := strconv.ParseFloat(row.Cells[col["price_per_1k_chars"]].Value, 64)
		free := row.Cells[col["free"]].Value == "true"
		s.voices = append(s.voices, voice{
			ID:              row.Cells[col["id"]].Value,
			Operator:        row.Cells[col["operator"]].Value,
			Name:            row.Cells[col["name"]].Value,
			Language:        row.Cells[col["language"]].Value,
			PricePer1kChars: price,
			Free:            free,
		})
	}
	return nil
}

func (s *sayState) listsNoVoices() error { s.voicesSet = true; s.voices = nil; return nil }

// --- When --------------------------------------------------------------------

func (s *sayState) runsArgs(args string) error { s.runSay(strings.Fields(args)); return nil }
func (s *sayState) runsVoiceText(v, text string) error {
	s.runSay([]string{"--voice", v, text})
	return nil
}
func (s *sayState) runsVoices() error { s.runVoices(); return nil }

// --- Then --------------------------------------------------------------------

func (s *sayState) postForModelInput(model, input string) error {
	if s.gotBody == nil {
		return errSay("a speech POST was made")
	}
	if s.gotBody["model"] != model {
		return errSay("model " + strconv.Quote(model) + ", got " + strconv.Quote(str(s.gotBody["model"])))
	}
	if s.gotBody["input"] != input {
		return errSay("input " + strconv.Quote(input) + ", got " + strconv.Quote(str(s.gotBody["input"])))
	}
	return nil
}

func (s *sayState) bodyResponseFormat(want string) error {
	if str(s.gotBody["response_format"]) != want {
		return errSay("response_format " + strconv.Quote(want))
	}
	return nil
}

func (s *sayState) bodySpeed(want float64) error {
	v, ok := s.gotBody["speed"].(float64)
	if !ok || v != want {
		return errSay("speed to be present and equal to the given value")
	}
	return nil
}

func (s *sayState) bodyNoSpeed() error {
	if _, ok := s.gotBody["speed"]; ok {
		return errSay("no speed field in the body")
	}
	return nil
}

func (s *sayState) requestSigned() error {
	if !s.gotSigned {
		return errSay("the request to carry a valid client signature")
	}
	return nil
}

func (s *sayState) failsNaming(sub string) error {
	if s.err == nil || !strings.Contains(s.err.Error(), sub) {
		return errSay("an error naming " + strconv.Quote(sub) + ", got " + errStr(s.err))
	}
	return nil
}

func (s *sayState) errorNames(sub string) error { return s.failsNaming(sub) }

func (s *sayState) failsUsage() error {
	if s.err == nil {
		return errSay("a usage error, got nil")
	}
	return nil
}

func (s *sayState) noSpeechPost() error {
	if s.postCount != 0 {
		return errSay("no speech POST, but the broker saw one")
	}
	return nil
}

func (s *sayState) failsContaining(sub string) error {
	if s.err == nil || !strings.Contains(s.err.Error(), sub) {
		return errSay("an error containing " + strconv.Quote(sub) + ", got " + errStr(s.err))
	}
	return nil
}

func (s *sayState) audioHandedToPlayer() error {
	if !s.played {
		return errSay("the audio to be handed to the player")
	}
	return nil
}

func (s *sayState) noAudioHanded() error {
	if s.played {
		return errSay("no audio handed to the player, but it was")
	}
	return nil
}

func (s *sayState) outputReads(want string) error {
	if !strings.Contains(s.out, want) {
		return errSay("output containing " + strconv.Quote(want) + ", got " + strconv.Quote(s.out))
	}
	return nil
}

func (s *sayState) outputNamesSavedPath() error {
	if !strings.Contains(s.out, ".wav") {
		return errSay("the output to name a saved .wav path, got " + strconv.Quote(s.out))
	}
	return nil
}

func (s *sayState) succeeds() error {
	if s.err != nil {
		return errSay("success, got error " + errStr(s.err))
	}
	return nil
}

func (s *sayState) errorNamesTopup() error {
	if s.err == nil || !strings.Contains(s.err.Error(), "topup") {
		return errSay("the error to name the topup step, got " + errStr(s.err))
	}
	return nil
}

func (s *sayState) listsByOperator(name, op string) error {
	if !strings.Contains(s.out, name) || !strings.Contains(s.out, op) {
		return errSay("output listing " + strconv.Quote(name) + " by " + strconv.Quote(op) + ", got " + strconv.Quote(s.out))
	}
	return nil
}

func (s *sayState) showsFree(name string) error {
	// the FREE marker must sit on the same rendered line as the voice name.
	for _, line := range strings.Split(s.out, "\n") {
		if strings.Contains(line, name) && strings.Contains(line, "FREE") {
			return nil
		}
	}
	return errSay("output showing " + strconv.Quote(name) + " as FREE, got " + strconv.Quote(s.out))
}

func (s *sayState) freeBeforePaid() error {
	fi := strings.Index(s.out, "Kiosk")
	pi := strings.Index(s.out, "Operator")
	if fi < 0 || pi < 0 || fi > pi {
		return errSay("the free voice listed before the paid one, got " + strconv.Quote(s.out))
	}
	return nil
}

func (s *sayState) outputNames(sub string) error {
	if !strings.Contains(s.out, sub) {
		return errSay("output naming " + strconv.Quote(sub) + ", got " + strconv.Quote(s.out))
	}
	return nil
}

// --- tiny test-only helpers --------------------------------------------------

// sayBDDErr is a readable godog assertion failure (mirrors the other packages' bddErr).
type sayBDDErr string

func (e sayBDDErr) Error() string { return string(e) }
func errSay(want string) error    { return sayBDDErr("expected " + want) }

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func errStr(err error) string {
	if err == nil {
		return "nil"
	}
	return strconv.Quote(err.Error())
}

// writeStubWAV writes the sample to a temp .wav so the no-player fallback returns a real path the
// command can print (the actual file writer lives in internal/audio; the stub mirrors its shape).
func writeStubWAV(wav []byte) (string, error) {
	f, err := os.CreateTemp("", "rogerai-say-*.wav")
	if err != nil {
		return "", err
	}
	_, _ = f.Write(wav)
	_ = f.Close()
	return f.Name(), nil
}

func TestSayBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &sayState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				st.reset(t)
				return ctx, nil
			})
			// Given
			sc.Step(`^a signed-in consumer with a funded wallet$`, st.signedFundedConsumer)
			sc.Step(`^a broker with a tts voice "([^"]*)" on air at (\d+) credits per 1M chars$`, st.voiceOnAir)
			sc.Step(`^the broker bills ([0-9.]+) for the request$`, st.billsCost)
			sc.Step(`^no audio player is available on this host$`, st.noAudioPlayer)
			sc.Step(`^the broker rejects the request with (\d+) "([^"]*)"$`, st.rejectsWith)
			sc.Step(`^the broker is unreachable$`, st.brokerUnreachable)
			sc.Step(`^the broker lists voices:$`, st.listsVoicesTable)
			sc.Step(`^the broker lists no voices$`, st.listsNoVoices)
			// When
			sc.Step(`^the consumer runs say with args "([^"]*)"$`, st.runsArgs)
			sc.Step(`^the consumer runs say with a voice "([^"]*)" and text "([^"]*)"$`, st.runsVoiceText)
			sc.Step(`^the consumer runs voices$`, st.runsVoices)
			// Then
			sc.Step(`^the broker receives a speech POST for model "([^"]*)" with input "([^"]*)"$`, st.postForModelInput)
			sc.Step(`^the request body response_format is "([^"]*)"$`, st.bodyResponseFormat)
			sc.Step(`^the request body speed is ([0-9.]+)$`, st.bodySpeed)
			sc.Step(`^the request body has no speed field$`, st.bodyNoSpeed)
			sc.Step(`^the request carries a valid client signature$`, st.requestSigned)
			sc.Step(`^the command fails with an error naming "([^"]*)"$`, st.failsNaming)
			sc.Step(`^the error names "([^"]*)"$`, st.errorNames)
			sc.Step(`^the command fails with a usage error$`, st.failsUsage)
			sc.Step(`^no speech POST is ever made to the broker$`, st.noSpeechPost)
			sc.Step(`^the command fails with an error containing "([^"]*)"$`, st.failsContaining)
			sc.Step(`^the audio is handed to the player$`, st.audioHandedToPlayer)
			sc.Step(`^no audio is handed to the player$`, st.noAudioHanded)
			sc.Step(`^the output reads "([^"]*)"$`, st.outputReads)
			sc.Step(`^the output names the saved file path$`, st.outputNamesSavedPath)
			sc.Step(`^the command succeeds$`, st.succeeds)
			sc.Step(`^the error names the topup step$`, st.errorNamesTopup)
			sc.Step(`^the output lists "([^"]*)" by "([^"]*)"$`, st.listsByOperator)
			sc.Step(`^the output shows "([^"]*)" as FREE$`, st.showsFree)
			sc.Step(`^the free voice is listed before the paid voice$`, st.freeBeforePaid)
			sc.Step(`^the output names "([^"]*)"$`, st.outputNames)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voice/say.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("roger say behavior scenarios failed (see godog output above)")
	}
}
