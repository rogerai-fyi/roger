package main

// Executable spec: the @client scenarios of features/tower/standalone_streaming.feature, and
// features/lan/routing_and_inference.feature scenario 102 (unblocked by Tower streaming).
//
// Real pieces only: the loopback proxy with the real LAN ladder, real standalone Towers
// (internal/localplane over a tower.State) each served by the real station loop, and station
// upstreams that stream. The broken one streams its first tokens and then drops the
// connection, so the break happens where it would on a real LAN: below the Tower.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
)

// streamingUpstream streams tokens as SSE; when breakAfter > 0 it sends that many tokens and
// then drops the connection without finishing.
func streamingUpstream(t *testing.T, name string, breakAfter int, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		tokens := []string{"from ", name, " part one", " part two"}
		for i, tok := range tokens {
			if breakAfter > 0 && i == breakAfter {
				// Give the station time to post what it has, then drop the connection mid-reply.
				time.Sleep(150 * time.Millisecond)
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, err := hj.Hijack()
					if err == nil {
						_ = conn.Close()
					}
				}
				return
			}
			b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": tok}}}})
			_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
			fl.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

type lanTowerStreamBDD struct {
	lanRouteBDD
	secondHits atomic.Int32
}

// streamTower stands a Tower at addr whose real station streams band from its upstream.
func (s *lanTowerStreamBDD) streamTower(addr, band string, breakAfter int, hits *atomic.Int32) {
	up := streamingUpstream(s.t, addr, breakAfter, hits)
	s.tower(addr).attachServing(s.t, "st-"+strings.ReplaceAll(band, ".", "-"), up.URL+"/v1/chat/completions", band)
	s.addTower(addr)
}

func (s *lanTowerStreamBDD) dispatchStream(band string) error {
	return s.dispatchBody(band, `{"model":"`+band+`","messages":[{"role":"user","content":"hi"}],"stream":true}`)
}

func (s *lanTowerStreamBDD) tokensThenError() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("status %d, body %s", s.code, s.body)
	}
	if !strings.Contains(s.body, "from ") {
		return fmt.Errorf("the tokens already sent did not reach the caller: %s", s.body)
	}
	if !strings.Contains(s.body, `"error"`) || !strings.Contains(s.body, "local") {
		return fmt.Errorf("the caller got no clear stream error: %s", s.body)
	}
	if !strings.HasSuffix(strings.TrimSpace(s.body), "data: [DONE]") {
		return fmt.Errorf("the broken stream did not end cleanly: %s", s.body)
	}
	if strings.Contains(s.body, "part two") {
		return fmt.Errorf("a second, complete reply reached the caller: %s", s.body)
	}
	return nil
}

func (s *lanTowerStreamBDD) notRestarted() error {
	if n := s.secondHits.Load(); n != 0 {
		return fmt.Errorf("the turn was restarted on another source (%d requests reached it)", n)
	}
	if s.selfHits.Load() != 0 {
		return fmt.Errorf("the turn was restarted on this machine")
	}
	return nil
}

func (s *lanTowerStreamBDD) steps(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		s.resetRoute()
		s.secondHits.Store(0)
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		s.restoreRoute()
		return ctx, nil
	})
	// features/tower/standalone_streaming.feature
	sc.Step(`^a standalone Tower serving its LAN with stations attached by key$`, func() error { return nil })
	sc.Step(`^an admitted client$`, func() error { return nil })
	sc.Step(`^an attached station "shed" that serves "qwen3-8b" from an upstream that can stream$`, func() error { return nil })
	sc.Step(`^LAN mode is on and the LAN Tower at (\S+) serves "([^"]*)" as a stream$`, func(addr, band string) error {
		s.lastTower, s.lastBand = addr, band
		return nil
	})
	sc.Step(`^LAN mode is on and two LAN Towers serve "([^"]*)"$`, func(band string) error {
		s.lastBand = band
		s.streamTower("192.168.1.21:8787", band, 0, &s.secondHits)
		return nil
	})
	sc.Step(`^the first answers 503 before any event$`, func() error {
		// the first Tower is tried first: it fails before any byte reaches the caller
		s.failingTower("192.168.1.20:8787", s.lastBand)
		c := loadConfig()
		c.LanTowers = []string{"192.168.1.20:8787", "192.168.1.21:8787"}
		return saveConfig(c)
	})
	sc.Step(`^a streamed turn is dispatched$`, func() error { return s.dispatchStream(s.lastBand) })
	sc.Step(`^the second Tower serves it$`, func() error {
		if s.code != http.StatusOK || !strings.Contains(s.body, `"content":"192.168.1.21:8787"`) {
			return fmt.Errorf("status %d, body %s", s.code, s.body)
		}
		if !strings.Contains(s.hdr.Get("Content-Type"), "text/event-stream") {
			return fmt.Errorf("the second Tower's reply was not a stream: %v", s.hdr)
		}
		return nil
	})
	// features/lan/routing_and_inference.feature:102
	sc.Step(`^LAN mode is on$`, func() error { return nil })
	sc.Step(`^the public broker is reachable from this machine$`, func() error { return nil })
	sc.Step(`^the LAN Tower at (\S+) serves "([^"]*)" as a stream$`, func(addr, band string) error {
		s.lastTower, s.lastBand = addr, band
		return nil
	})
	sc.Step(`^the stream breaks after the first tokens$`, func() error {
		// the broken Tower is first in the list; a second Tower serves the same band after it,
		// so a silent restart would have somewhere to go
		s.streamTower(s.lastTower, s.lastBand, 2, nil)
		s.streamTower("192.168.1.21:8787", s.lastBand, 0, &s.secondHits)
		return nil
	})
	sc.Step(`^the turn is in progress$`, func() error { return s.dispatchStream(s.lastBand) })
	sc.Step(`^the caller receives the tokens already sent and a clear stream error$`, s.tokensThenError)
	sc.Step(`^the turn is not restarted on an Edge member or another Tower$`, s.notRestarted)
	sc.Step(`^the turn is not silently restarted on another source$`, s.notRestarted)
}

func TestLANTowerStreamingBDD(t *testing.T) {
	s := &lanTowerStreamBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}
	suite := godog.TestSuite{
		ScenarioInitializer: s.steps,
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/tower/standalone_streaming.feature:223",
				"../../features/tower/standalone_streaming.feature:232",
				"../../features/lan/routing_and_inference.feature:102",
			},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("LAN Tower streaming failed")
	}
}
