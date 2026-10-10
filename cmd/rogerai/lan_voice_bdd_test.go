package main

// Executable spec: features/lan/voice.feature (@cli, @client @edge). `roger --lan say` runs
// in-process against REAL local voice servers (httptest TTS stubs as the upstream audio source),
// this machine's REAL owner-only share records, and Edge members' mTLS faces pinned in the REAL
// fleet record. A recording broker proves the network was never asked.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type lanVoiceBDD struct {
	lanDialBDD
	selfHits  atomic.Int32
	selfMode  string // "" audio, "refuse" a 500 before any audio, "echo" a 401 echoing the key
	selfKey   string
	faces     map[string]*edgeFace
	played    [][]byte
	receipts  []protocol.UsageReceipt
	shareFile string
	out       string
	err       error
	dialRead  atomic.Int32
}

func (s *lanVoiceBDD) resetVoice() {
	s.lanDialBDD.reset()
	s.selfHits.Store(0)
	s.selfMode, s.selfKey, s.shareFile, s.out, s.err = "", "", "", "", nil
	s.faces, s.played, s.receipts = map[string]*edgeFace{}, nil, nil
	s.dialRead.Store(0)
	sayPlayer = func(wav []byte) (string, bool, error) { s.played = append(s.played, wav); return "", true, nil }
	sayLANReceipt = func() func(protocol.UsageReceipt) {
		return func(r protocol.UsageReceipt) { s.receipts = append(s.receipts, r) }
	}
	// Every Tower read goes through the one LAN dial: a say that reads it would ask a Tower.
	lanDial = func(cfg config) client.LANDial { s.dialRead.Add(1); return prodLANDial(cfg) }
}

var prodLANDial = lanDial

func (s *lanVoiceBDD) restoreVoice() {
	s.lanDialBDD.restore()
	sayPlayer = prodSayPlayer
	sayLANReceipt = edgeUseRecorder
	lanDial = prodLANDial
	for _, sh := range onair.Shares() {
		_ = os.Remove(onair.ShareFile(sh.PID))
	}
}

var prodSayPlayer = sayPlayer

// voiceServer is this machine's voice: a TTS server whose answer depends on selfMode.
func (s *lanVoiceBDD) voiceServer() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.selfHits.Add(1)
		_, _ = io.ReadAll(r.Body)
		switch s.selfMode {
		case "refuse":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "echo":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"bad key `+r.Header.Get("Authorization")+`"}`)
			return
		}
		if s.selfKey != "" && r.Header.Get("Authorization") != "Bearer "+s.selfKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = io.WriteString(w, "RIFF voice of this machine")
	}))
	s.t.Cleanup(srv.Close)
	return srv
}

// selfVoice puts band on air on this machine as a separate `roger share` does: an owner-only
// record pointing at a real voice server.
func (s *lanVoiceBDD) selfVoice(band string) error {
	up := s.voiceServer()
	_, err := onair.WriteShare(onair.Share{PID: os.Getpid(), Band: band, ChatURL: up.URL + "/v1/chat/completions",
		Key: s.selfKey, Modality: protocol.ModalityTTS, SpeechURL: up.URL + "/v1/audio/speech"})
	s.shareFile = onair.ShareFile(os.Getpid())
	return err
}

// memberVoice records an Edge member serving band as a VOICE, VERIFIED, LAN-direct, at a real
// mTLS face.
func (s *lanVoiceBDD) memberVoice(node, band string) error {
	f := newEdgeFace(s.t, node, 0)
	s.faces[node] = f
	if err := s.enroll(); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	id := "n_" + node + strings.Repeat("0", 24-len(node))
	obs := edge.Observation{Name: node, Kind: "host", Addr: f.addr(), Fingerprint: f.pin, Instances: []edge.Instance{{Name: "serve",
		Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Claimed)}}, Bands: []string{band}, Voices: []string{band}}}}
	if _, err := st.fleet.Observe(id, obs); err != nil {
		return err
	}
	if err := st.fleet.RecordInstanceProbe(id, "serve", edge.Serve, true); err != nil {
		return err
	}
	if err := st.fleet.RecordServeProbe(id, "serve", []string{band}); err != nil {
		return err
	}
	return st.save()
}

func (s *lanVoiceBDD) say(line string) error {
	args := strings.Fields(line)
	s.out, s.err = captureEdgeStdout(func() error { return run(args, loadConfig()) })
	return nil
}

func (s *lanVoiceBDD) spoke(who string) error {
	if s.err != nil {
		return fmt.Errorf("refused: %v (out %s)", s.err, s.out)
	}
	if len(s.played) != 1 || !strings.Contains(string(s.played[0]), "voice of "+who) {
		return fmt.Errorf("played %q, want the voice of %s (out %s)", s.played, who, s.out)
	}
	return nil
}

func (s *lanVoiceBDD) refused(parts ...string) error {
	if s.err == nil {
		return fmt.Errorf("not refused: %s", s.out)
	}
	for _, p := range parts {
		if !strings.Contains(s.err.Error(), p) {
			return fmt.Errorf("refusal lacks %q: %v", p, s.err)
		}
	}
	if len(s.played) != 0 {
		return fmt.Errorf("a refused line still played audio")
	}
	return nil
}

func (s *lanVoiceBDD) noBroker() error {
	if n := s.broker.count(); n != 0 {
		return fmt.Errorf("the broker was asked: %v", s.broker.paths)
	}
	return nil
}

func TestLANVoiceBDD(t *testing.T) {
	s := &lanVoiceBDD{lanDialBDD: lanDialBDD{t: t}}
	var paths []string
	for _, l := range []int{43, 52, 58, 67, 76, 87, 129, 139, 148, 155, 166, 174, 183, 195, 221, 231} {
		paths = append(paths, fmt.Sprintf("../../features/lan/voice.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { s.resetVoice(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restoreVoice()
				return c, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step("^this machine has the TTS band \"([^\"]*)\" on air through `roger share`$", s.selfVoice)
			sc.Step(`^they run "roger ([^"]*)"$`, s.say)
			sc.Step(`^audio is produced by this machine's voice upstream$`, func() error { return s.spoke("this machine") })
			sc.Step(`^the output says it was spoken locally and was free$`, func() error {
				if !strings.Contains(s.out, "spoken locally") || !strings.Contains(s.out, "free") {
					return fmt.Errorf("output does not say local and free: %s", s.out)
				}
				return nil
			})
			sc.Step(`^no request was made to the public broker$`, s.noBroker)
			sc.Step(`^no request was made to the public broker, including its voices list$`, s.noBroker)
			sc.Step(`^LAN mode is on and exactly one local TTS band is on air$`, func() error { return s.selfVoice("roggentoo-voice") })
			sc.Step(`^that voice speaks$`, func() error { return s.spoke("this machine") })
			sc.Step(`^LAN mode is on and this machine and the Edge member "([^"]*)" each have a TTS band on air$`, func(n string) error {
				if err := s.selfVoice("roggentoo-voice"); err != nil {
					return err
				}
				return s.memberVoice(n, "studio-voice")
			})
			sc.Step(`^this machine's voice speaks$`, func() error { return s.spoke("this machine") })
			sc.Step(`^the output names the voice used and how to pick another with --voice$`, func() error {
				if !strings.Contains(s.out, "roggentoo-voice") || !strings.Contains(s.out, "--voice") {
					return fmt.Errorf("output does not name the voice and --voice: %s", s.out)
				}
				return nil
			})
			sc.Step("^a separate `roger --lan share` process has a TTS band on air$", func() error { return s.selfVoice("roggentoo-voice") })
			sc.Step(`^that process's voice speaks$`, func() error { return s.spoke("this machine") })
			sc.Step(`^the record that pointed to it is owner-only \(0600\) and never sent to a peer$`, func() error {
				fi, err := os.Stat(s.shareFile)
				if err != nil {
					return err
				}
				if fi.Mode().Perm() != 0o600 {
					return fmt.Errorf("the share record is %o, want 0600", fi.Mode().Perm())
				}
				// What a peer receives about this machine's bands is the instance record: band
				// names and which are voices, never the upstream's address or key.
				for _, v := range voicesOf(edgeBandsOnAir()) {
					if strings.Contains(v, "http") || strings.Contains(v, "127.0.0.1") {
						return fmt.Errorf("a voice name carries an address: %q", v)
					}
				}
				if len(voicesOf(edgeBandsOnAir())) != 1 {
					return fmt.Errorf("the instance record declares voices %v", voicesOf(edgeBandsOnAir()))
				}
				return nil
			})
			sc.Step(`^this machine's voice upstream requires a key$`, func() error { s.selfKey = "sk-local-secret-123"; return nil })
			sc.Step(`^the upstream echoes the key in an error body$`, func() error {
				s.selfMode = "echo"
				return s.selfVoice("roggentoo-voice")
			})
			sc.Step(`^the key does not appear in any output$`, func() error {
				all := s.out + fmt.Sprint(s.err)
				if strings.Contains(all, "sk-local-secret-123") {
					return fmt.Errorf("the key was echoed: %s", all)
				}
				if s.selfHits.Load() == 0 {
					return fmt.Errorf("the voice server was never asked, so nothing was proven")
				}
				return nil
			})
			sc.Step(`^the enrolled Edge member "([^"]*)" has the TTS band "([^"]*)" on air$`, s.memberVoice)
			sc.Step(`^the request goes to "([^"]*)"'s Edge face over mutual TLS$`, func(n string) error {
				f := s.faces[n]
				if f.speechHits.Load() != 1 || f.mutualTLS.Load() == 0 {
					return fmt.Errorf("speech hits %d, with the caller's certificate %d", f.speechHits.Load(), f.mutualTLS.Load())
				}
				return nil
			})
			sc.Step(`^audio is returned$`, func() error { return s.spoke("jetson") })
			sc.Step(`^"([^"]*)" signs a local receipt for the speech$`, func(n string) error {
				if len(s.receipts) != 1 || s.receipts[0].NodeID != n || !s.receipts[0].Local {
					return fmt.Errorf("receipts %+v", s.receipts)
				}
				if !s.receipts[0].VerifyNode(fmt.Sprintf("%x", []byte(s.faces[n].pub))) {
					return fmt.Errorf("the receipt is not signed by %s", n)
				}
				return nil
			})
			sc.Step(`^no local text-to-speech \(TTS\) model is on air$`, func() error { return nil })
			sc.Step(`^the LAN Tower at ([0-9.:]+) is configured$`, func(a string) error { s.addTower(a); return nil })
			sc.Step(`^it is refused, saying no local text-to-speech \(TTS\) model is on air and LAN Towers do not serve text-to-speech$`, func() error {
				return s.refused("no local text-to-speech (TTS) model is on air", "LAN Towers do not serve text-to-speech")
			})
			sc.Step(`^no request was made to ([0-9.:]+)$`, func(string) error {
				if n := s.dialRead.Load(); n != 0 {
					return fmt.Errorf("the LAN dial (every Tower read) was read %d time(s)", n)
				}
				return nil
			})
			sc.Step(`^no request was made to the public broker's (/\S+)$`, func(p string) error { return brokerNotAsked(s.broker, p) })
			sc.Step(`^LAN mode is on and no voice is on air on this machine or any Edge member$`, func() error { return nil })
			sc.Step(`^it is refused, saying no local text-to-speech \(TTS\) model is on air$`, func() error { return s.refused("no local text-to-speech (TTS) model is on air") })
			sc.Step(`^it says how to put one on air or how to use the network's TTS models without --lan$`, func() error {
				return s.refused("roger model serve", "roger share", "run without --lan to use the network's TTS models")
			})
			sc.Step(`^LAN mode is on and "([^"]*)" is on the public market but not on this LAN$`, func(string) error { return nil })
			sc.Step(`^it is refused, saying "([^"]*)" is not on air on this LAN$`, func(v string) error { return s.refused(v + " is not on air on this LAN") })
			sc.Step(`^LAN mode is on, this machine has one voice on air and the market has many$`, func() error { return s.selfVoice("roggentoo-voice") })
			sc.Step(`^only this machine's voice is listed, with its source$`, func() error {
				if s.err != nil || !strings.Contains(s.out, "roggentoo-voice") || !strings.Contains(s.out, "this machine") || strings.Contains(s.out, "market-band") {
					return fmt.Errorf("list: err=%v out=%s", s.err, s.out)
				}
				if strings.Count(s.out, "· free") != 1 {
					return fmt.Errorf("listed %d voices, want 1: %s", strings.Count(s.out, "· free"), s.out)
				}
				return nil
			})
			sc.Step(`^LAN mode is on and the Edge member "([^"]*)" is speaking a line for this machine$`, func(n string) error {
				return s.memberVoice(n, "studio-voice")
			})
			sc.Step(`^"([^"]*)" goes away before the audio is complete$`, func(n string) error {
				s.faces[n].speechMode = "cut"
				// a second member with the same voice: proof the line is not re-spoken by it
				if err := s.memberVoice("orin", "studio-voice"); err != nil {
					return err
				}
				return s.say("--lan say hello")
			})
			sc.Step(`^it fails, saying the local voice stopped before finishing$`, func() error {
				return s.refused("stopped before finishing")
			})
			sc.Step(`^no partial audio is played as if it were complete$`, func() error {
				if len(s.played) != 0 {
					return fmt.Errorf("played %q", s.played)
				}
				return nil
			})
			sc.Step(`^the line is not re-spoken by a different voice$`, func() error {
				if n := s.faces["orin"].speechHits.Load(); n != 0 {
					return fmt.Errorf("another voice was asked %d time(s)", n)
				}
				return nil
			})
			sc.Step(`^LAN mode is on and both this machine and "([^"]*)" have the same TTS band on air$`, func(n string) error {
				if err := s.selfVoice("studio-voice"); err != nil {
					return err
				}
				return s.memberVoice(n, "studio-voice")
			})
			sc.Step(`^this machine's voice upstream refuses the request before any audio$`, func() error { s.selfMode = "refuse"; return nil })
			sc.Step(`^"([^"]*)" speaks it$`, func(n string) error { return s.spoke(n) })
			sc.Step(`^the output lists that local voice$`, func() error {
				if s.err != nil || !strings.Contains(s.out, "roggentoo-voice") || !strings.Contains(s.out, "local text-to-speech (TTS) model") {
					return fmt.Errorf("the roster did not list the local TTS model: %q (%v)", s.out, s.err)
				}
				return nil
			})
			// Edge-only through the environment alone (no --lan): it implies LAN mode.
			sc.Step(`^edge-only is on and the Edge member "([^"]*)" has a voice on air$`, func(n string) error {
				s.t.Setenv("ROGER_LAN_EDGE_ONLY", "1")
				return s.memberVoice(n, "roggentoo-voice")
			})
			sc.Step(`^the output names that it moved past this machine's voice and why$`, func() error {
				if !strings.Contains(s.out, "moved past this machine") || !strings.Contains(s.out, "500") {
					return fmt.Errorf("output does not name the move: %s", s.out)
				}
				return nil
			})
			sc.Step(`^LAN mode is on and this machine and "([^"]*)" have voices on air$`, func(n string) error {
				if err := s.selfVoice("studio-voice"); err != nil {
					return err
				}
				return s.memberVoice(n, "studio-voice")
			})
			sc.Step(`^both fail$`, func() error {
				s.selfMode = "refuse"
				s.faces["jetson"].speechMode = "fail"
				return nil
			})
			sc.Step(`^it is refused, naming each local voice and how it failed$`, func() error {
				return s.refused("this machine: answered 500", "jetson:", "every local voice failed")
			})
			sc.Step(`^LAN mode is on and a local voice is on air$`, func() error { return s.selfVoice("roggentoo-voice") })
			sc.Step(`^no balance was read and no price or hold was shown$`, func() error {
				if err := brokerNotAsked(s.broker, "/balance"); err != nil {
					return err
				}
				for _, w := range []string{"$", "hold", "balance"} {
					if strings.Contains(s.out, w) {
						return fmt.Errorf("output shows %q: %s", w, s.out)
					}
				}
				return nil
			})
			sc.Step(`^the output says the speech was free$`, func() error {
				if !strings.Contains(s.out, "free") {
					return fmt.Errorf("output: %s", s.out)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("voice.feature (@cli) failed")
	}
}
