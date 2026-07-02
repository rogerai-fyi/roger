package main

// audio_content_type_bdd_test.go makes features/voice/tts_content_type.feature
// EXECUTABLE: a 200 from /v1/audio/speech must carry the content type of the audio
// ACTUALLY returned (bytes first, then the requested response_format, then the
// audio/mpeg default) - the 2026-07-02 incident was Kokoro's WAV default served
// under a static audio/mpeg header. Harness mirrors audio_speech_bdd_test.go (the
// REAL audioRelay/transcribeRelay money path + a stub station returning controlled
// bytes; no mocks).

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// wavClip is a minimal RIFF/WAVE header + a little payload (what Kokoro's wav
// default actually starts with); mp3Clip carries the ID3 tag; junkClip is neither.
var (
	wavClip  = append([]byte("RIFF\x24\x08\x00\x00WAVE"), []byte("fmt ~pcm bytes~")...)
	mp3Clip  = append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), []byte("~frames~")...)
	junkClip = []byte("~audio bytes~")
)

type ctState struct {
	modality  string // the station's modality (tts | stt)
	resBytes  []byte // what the station returns
	gotCT     string
	gotStatus int
}

func (s *ctState) reset() { s.modality, s.resBytes, s.gotCT, s.gotStatus = "", nil, "", 0 }

// run drives ONE request through the real relay handler against a stub station
// returning s.resBytes, capturing status + Content-Type.
func (s *ctState) run(path string, reqBody []byte) error {
	mem := store.NewMem()
	b := relayBroker(mem)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["v1"] = protocol.NodeRegistration{
		NodeID: "v1", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "kokoro", Modality: s.modality, PriceIn: 15}},
	}
	b.lastSeen["v1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["v1"] = tun
	if err := mem.BindNode("v1", "op1"); err != nil {
		return err
	}
	go func() {
		job, ok := <-tun.jobs
		if !ok {
			return
		}
		rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: "v1", Model: "kokoro", TS: time.Now().Unix()}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: s.resBytes, Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: userPubHex}); err != nil {
		return err
	}
	if _, err := mem.AddCredits("u_gh_7", 100); err != nil {
		return err
	}

	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(reqBody)))
	signReq(r, userPriv, reqBody)
	w := httptest.NewRecorder()
	if s.modality == protocol.ModalitySTT {
		b.transcribeRelay(w, r)
	} else {
		b.audioRelay(w, r)
	}
	s.gotStatus = w.Code
	s.gotCT = w.Header().Get("Content-Type")
	return nil
}

// --- Given -------------------------------------------------------------------------

func (s *ctState) ttsStationReturning(kind string) error {
	s.modality = protocol.ModalityTTS
	switch kind {
	case "a WAV clip":
		s.resBytes = wavClip
	case "an MP3 clip":
		s.resBytes = mp3Clip
	case "unrecognizable":
		s.resBytes = junkClip
	default:
		return fmt.Errorf("unknown station result kind %q", kind)
	}
	return nil
}

func (s *ctState) sttStation() error {
	s.modality = protocol.ModalitySTT
	s.resBytes = []byte(`{"text":"hello from the transcription"}`)
	return nil
}

// --- When --------------------------------------------------------------------------

func (s *ctState) requestsSpeech(format string) error {
	return s.run("/v1/audio/speech",
		[]byte(fmt.Sprintf(`{"model":"kokoro","input":"say this","response_format":%q}`, format)))
}

func (s *ctState) requestsSpeechNoFormat() error {
	return s.run("/v1/audio/speech", []byte(`{"model":"kokoro","input":"say this"}`))
}

func (s *ctState) postsTranscription() error {
	return s.run("/v1/audio/transcriptions?model=kokoro", []byte("RIFFxxxxWAVE~audio~"))
}

// --- Then --------------------------------------------------------------------------

func (s *ctState) responseHasContentType(want string) error {
	if s.gotStatus != http.StatusOK {
		return fmt.Errorf("status = %d, want 200", s.gotStatus)
	}
	if s.gotCT != want {
		return fmt.Errorf("Content-Type = %q, want %q (the header must describe the audio actually returned)", s.gotCT, want)
	}
	return nil
}

func TestAudioContentTypeBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &ctState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a tts station whose result bytes are (a WAV clip|an MP3 clip|unrecognizable)$`, st.ttsStationReturning)
			sc.Step(`^an stt station whose result is a transcription$`, st.sttStation)
			sc.Step(`^a funded consumer requests speech with response_format "([^"]*)"$`, st.requestsSpeech)
			sc.Step(`^a funded consumer requests speech with no response_format$`, st.requestsSpeechNoFormat)
			sc.Step(`^a funded consumer posts audio for transcription$`, st.postsTranscription)
			sc.Step(`^the response is 200 with content type "([^"]*)"$`, st.responseHasContentType)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voice/tts_content_type.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("tts content-type scenarios failed (see godog output above)")
	}
}
