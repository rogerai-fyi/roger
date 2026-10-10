package tui

// Executable spec: features/lan/voice.feature (@tui). The REAL TUI model in LAN mode, its dial read
// through the REAL lanOffers conversion, and its voice preview speaking through the REAL client
// LAN voice ladder to a REAL local TTS server; a recording broker proves the network is never asked.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
)

type lanVoiceTUI struct {
	t          *testing.T
	brokerHits atomic.Int32
	voiceHits  atomic.Int32
	broker     *httptest.Server
	voice      *httptest.Server
	onAir      bool
	m          model
	played     int
	view       string
}

func (s *lanVoiceTUI) reset() {
	s.brokerHits.Store(0)
	s.voiceHits.Store(0)
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.brokerHits.Add(1)
		http.NotFound(w, r)
	}))
	s.voice = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.voiceHits.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = io.WriteString(w, "RIFF local voice")
	}))
	s.onAir, s.played, s.view = true, 0, ""
	SetLANDial(func() client.LANDial {
		return client.LANDial{Bands: []client.LANBand{{Model: "studio-voice", Voice: true,
			Sources: []client.LANSource{{Kind: client.LANSelf, Verified: true}}}}}
	})
	// The CLI installs the same shape: this machine's voice while it is on air, else a refusal.
	SetLANSpeak(func(band, text string) ([]byte, error) {
		if !s.onAir {
			return nil, fmt.Errorf("the local text-to-speech (TTS) model %s is no longer on air", band)
		}
		res, err := client.SpeakLAN(context.Background(), []client.LANVoice{{Band: band, Source: "this machine",
			URL: s.voice.URL + "/v1/audio/speech"}}, text, client.LANSpeechOptions{})
		return res.Audio, err
	})
}

func (s *lanVoiceTUI) close() {
	SetLANDial(nil)
	SetLANSpeak(nil)
	s.broker.Close()
	s.voice.Close()
}

// dialShowsVoice brings the TUI up on the LAN dial.
func (s *lanVoiceTUI) dialShowsVoice() error {
	var m tea.Model = New(s.broker.URL, "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(lanOffers())
	s.m = m.(model)
	s.m.previewPlayer = func([]byte) (string, bool, error) { s.played++; return "", true, nil }
	for _, b := range s.m.bands {
		if b.model == "studio-voice" && b.isTTS() {
			return nil
		}
	}
	return fmt.Errorf("the LAN dial does not show studio-voice as a voice band")
}

func (s *lanVoiceTUI) preview() error {
	var bd band
	for _, b := range s.m.bands {
		if b.model == "studio-voice" {
			bd = b
		}
	}
	tm, cmd := s.m.startVoicePreview(bd)
	s.m = asModel(tm)
	if cmd == nil {
		return fmt.Errorf("a local voice must synthesize at once (no confirm gate): stage %d, status %s", s.m.previewStage, s.m.status)
	}
	tm, _ = s.m.Update(cmd())
	s.m = asModel(tm)
	s.view = stripANSI(s.m.voicePreviewView(100)) + " " + stripANSI(s.m.status)
	return nil
}

func TestLANVoiceTUI(t *testing.T) {
	s := &lanVoiceTUI{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) { s.close(); return c, nil })
			sc.Step(`^LAN mode is on and the TUI dial shows a local TTS band$`, s.dialShowsVoice)
			sc.Step(`^LAN mode is on and the TUI dial showed a local TTS band that has since gone off air$`, func() error {
				if err := s.dialShowsVoice(); err != nil {
					return err
				}
				s.onAir = false
				return nil
			})
			sc.Step(`^the voice preview is started on that band$`, s.preview)
			sc.Step(`^the sample is synthesized by that local voice$`, func() error {
				if s.voiceHits.Load() != 1 || s.played != 1 {
					return fmt.Errorf("voice asked %d time(s), played %d: %s", s.voiceHits.Load(), s.played, s.view)
				}
				return nil
			})
			sc.Step(`^no request was made to the public broker$`, func() error {
				if n := s.brokerHits.Load(); n != 0 {
					return fmt.Errorf("the broker was asked %d time(s)", n)
				}
				return nil
			})
			sc.Step(`^no price or sample cost is shown$`, func() error {
				for _, w := range []string{"$", "billed", "/1M"} {
					if strings.Contains(s.view, w) {
						return fmt.Errorf("the preview shows %q: %s", w, s.view)
					}
				}
				if !strings.Contains(s.view, "free") {
					return fmt.Errorf("the preview does not say free: %s", s.view)
				}
				return nil
			})
			sc.Step(`^the preview says the local text-to-speech \(TTS\) model is no longer on air$`, func() error {
				if !strings.Contains(s.view, "text-to-speech (TTS) model") && strings.Contains(s.view, "no longer on air") || s.played != 0 {
					return fmt.Errorf("preview: %s (played %d)", s.view, s.played)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/lan/voice.feature:202", "../../features/lan/voice.feature:210"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("voice.feature (@tui) failed")
	}
}
