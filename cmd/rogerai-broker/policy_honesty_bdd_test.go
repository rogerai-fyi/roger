package main

// policy_honesty_bdd_test.go makes features/categories/policy_honesty.feature EXECUTABLE: a
// station's declared content policy (and any tags it chooses) must never change the broker's
// pre-dispatch screen. It reuses the off-path screening harness (opsState: a real broker, a
// real tunnel, a fake on-air station loop, and the Groq safeguard stub) and only stamps the
// scenario's model and taxonomy onto the registered offer before the relay.
// MODEL-CATEGORIES-DESIGN-2026-10-05.

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/protocol"
)

type honestyState struct {
	*opsState
	model string
}

func (s *honestyState) brokerRequired(mode string) error {
	s.require, s.mode = true, mode
	return s.stubRecording()
}

func (s *honestyState) stationServing(model string) error { s.model = model; return nil }

// declare builds the broker (lazily, as relay would) and rewrites station n1's offer to the
// scenario's model carrying the declared taxonomy, normalized the way register normalizes.
func (s *honestyState) declare(tx protocol.Taxonomy) error {
	s.build()
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	reg := s.b.nodes["n1"]
	o := reg.Offers[0]
	o.Model, o.Taxonomy = s.model, tx
	o.Normalize()
	reg.Offers = []protocol.ModelOffer{o}
	s.b.nodes["n1"] = reg
	return nil
}
func (s *honestyState) declaresPolicy(p string) error {
	return s.declare(protocol.Taxonomy{ContentPolicy: p})
}
func (s *honestyState) declaresPolicyTags(p, tags string) error {
	return s.declare(protocol.Taxonomy{ContentPolicy: p, Tags: strings.Split(tags, ",")})
}

func (s *honestyState) verdict(v string) error { s.stub.set(stubStep{verdict: v}); return nil }

func (s *honestyState) sendsChat(model string) error {
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 64,
		"messages": []map[string]any{{"role": "user", "content": "a prompt for the screen"}}})
	s.relay(1, body)
	return nil
}

func (s *honestyState) statusIs(code int) error {
	if s.last.code != code {
		return fmtErr("status %d, got %d: %s", code, s.last.code, s.last.body)
	}
	return nil
}
func (s *honestyState) stationJobs() int {
	s.stMu.Lock()
	defer s.stMu.Unlock()
	return len(s.stTimes)
}
func (s *honestyState) neverReceives() error {
	if n := s.stationJobs(); n != 0 {
		return fmtErr("the station received %d request(s); a blocked prompt must never be dispatched", n)
	}
	return nil
}
func (s *honestyState) receives() error {
	if n := s.stationJobs(); n != 1 {
		return fmtErr("the station received %d request(s), want 1: %d %s", n, s.last.code, s.last.body)
	}
	return nil
}
func (s *honestyState) preservedQueued() error {
	if n := len(s.csamRows()); n != 1 {
		return fmtErr("want 1 preserved CSAM incident queued for report, got %d", n)
	}
	return nil
}
func (s *honestyState) screenedOnce() error {
	if n := s.stub.count(); n != 1 {
		return fmtErr("the prompt was screened %d times, want exactly 1", n)
	}
	return nil
}

func (s *honestyState) flagged() error {
	if n := len(s.flags(s.last)); n != 1 {
		return fmtErr("want 1 moderation flag for the request, got %d", n)
	}
	return nil
}

func TestPolicyHonestyBDD(t *testing.T) {
	for _, k := range []string{"MODERATION_PROVIDER", "MODERATION_URL", "MODERATION_GROQ_KEY", "GROQ_API_KEY",
		"ROGERAI_REQUIRE_MODERATION", "ROGERAI_CSAM_CATEGORIES", "MODERATION_MODEL", "ROGERAI_MODERATION_MODE"} {
		t.Setenv(k, "")
	}
	st := &honestyState{opsState: &opsState{t: t, logs: &lockedBuffer{}}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.logOut, st.logFl = log.Writer(), log.Flags()
				log.SetOutput(st.logs)
				log.SetFlags(0)
				st.reset()
				st.model = ""
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.closeAll()
				log.SetOutput(st.logOut)
				log.SetFlags(st.logFl)
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a broker with moderation required in `+q+` mode$`, st.brokerRequired)
			sc.Step(`^a station serving model `+q+` on air$`, st.stationServing)
			sc.Step(`^the offer for `+q+` declares content policy `+q+`$`, func(_, p string) error { return st.declaresPolicy(p) })
			sc.Step(`^the offer for `+q+` declares content policy `+q+` and tags `+q+`$`, func(_, p, tags string) error { return st.declaresPolicyTags(p, tags) })
			sc.Step(`^a moderation backend that flags the prompt as `+q+`$`, st.verdict)
			sc.Step(`^a moderation backend that returns `+q+`$`, st.verdict)
			sc.Step(`^a consumer sends a chat request to `+q+`$`, st.sendsChat)
			sc.Step(`^the response status is (\d+)$`, st.statusIs)
			sc.Step(`^the station never receives the request$`, st.neverReceives)
			sc.Step(`^the station receives the request$`, st.receives)
			sc.Step(`^the incident is preserved and a report is queued$`, st.preservedQueued)
			sc.Step(`^the prompt was screened exactly once$`, st.screenedOnce)
			sc.Step(`^off-path screening drains$`, st.drain)
			sc.Step(`^a moderation flag is recorded for the request$`, st.flagged)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/policy_honesty.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/policy_honesty behavior scenarios failed (see godog output above)")
	}
}
