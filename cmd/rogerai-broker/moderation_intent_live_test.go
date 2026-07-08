package main

// moderation_intent_live_test.go is the @live layer of the intent-not-capability spec: it
// runs the REAL Groq safeguard model (the same screenGroq the relay uses) against an
// on-disk golden corpus and checks the ALLOW/BLOCK decision. This is the ONLY layer that can
// actually prove a PROMPT refinement removes the capability-description false positives while
// keeping the red-team set blocked - a stubbed verdict proves plumbing, not judgement.
//
// It is SKIPPED when MODERATION_GROQ_KEY (or GROQ_API_KEY) is unset, exactly like the
// Postgres ledger tests skip without ROGERAI_TEST_DATABASE_URL, so `go test` stays hermetic
// and free. CI / the founder runs it with the key set as the pre-ship red-team gate.
//
// Corpus layout (testdata/moderation/corpus/):
//   pass/*.txt   - relay bodies (OpenAI chat-completions JSON) that MUST return ALLOW.
//                  Includes a representative hermes-style tool-heavy prompt (the real
//                  ~4749-char capture is owed by founder/T&S before this gate releases the
//                  hold), opencode/aider schemas, and lone capability-description prompts.
//   block/*.txt  - relay bodies that MUST return 451 (non-CSAM red-team + disguised-as-tool).
//   csam/*.txt   - relay bodies that MUST return 451 AND modResult.csam==true. Curated and
//                  access-controlled by trust-and-safety; the file may be absent in a clone.
//
// Each *.txt is a full request body; the test screens promptText(body) so it exercises the
// exact concat the relay screens (all roles), not a hand-picked substring.

import (
	"os"
	"path/filepath"
	"testing"
)

// liveModeration builds the real Groq-backed screen from the environment (require=1 so a
// transport failure is a hard error, never a silent fail-open that masks a corpus regression).
func liveModeration(t *testing.T) moderation {
	t.Helper()
	if firstNonEmpty(os.Getenv("MODERATION_GROQ_KEY"), os.Getenv("GROQ_API_KEY")) == "" {
		t.Skip("MODERATION_GROQ_KEY / GROQ_API_KEY not set; skipping the live safeguard red-team pass")
	}
	t.Setenv("MODERATION_PROVIDER", "groq")
	t.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	return loadModeration()
}

// corpusBodies reads every *.txt request body in a corpus subdir; returns nil if the dir is
// absent (the csam/ dir is curated separately and may not be present in a given checkout).
func corpusBodies(t *testing.T, sub string) map[string][]byte {
	t.Helper()
	dir := filepath.Join("testdata", "moderation", "corpus", sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read corpus %s: %v", dir, err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = b
	}
	return out
}

// TestLiveIntentPassSetAllowed: every body in corpus/pass MUST screen ALLOW. RED against the
// CURRENT policy (the capability-description false positives), GREEN once the policy is refined.
func TestLiveIntentPassSetAllowed(t *testing.T) {
	m := liveModeration(t)
	bodies := corpusBodies(t, "pass")
	if len(bodies) == 0 {
		t.Skip("no pass corpus present (testdata/moderation/corpus/pass/*.txt)")
	}
	for name, body := range bodies {
		res := m.screen(promptText(body))
		if !res.allow() {
			t.Errorf("pass corpus %s: got status %d (%s), want ALLOW - false positive not removed", name, res.status, res.msg)
		}
	}
}

// TestLiveRedTeamBlockSetBlocked: every body in corpus/block MUST screen 451. Guards against a
// safety REGRESSION - the carveout must not let a genuine harmful request (incl. one disguised
// as a tool/system description) through.
func TestLiveRedTeamBlockSetBlocked(t *testing.T) {
	m := liveModeration(t)
	bodies := corpusBodies(t, "block")
	if len(bodies) == 0 {
		t.Skip("no block corpus present (testdata/moderation/corpus/block/*.txt)")
	}
	for name, body := range bodies {
		res := m.screen(promptText(body))
		if res.allow() {
			t.Errorf("block corpus %s: got ALLOW, want 451 - safety REGRESSION (harmful request slipped through)", name)
		}
	}
}

// TestLiveCSAMBlockSetPreserved: every body in corpus/csam MUST screen 451 AND set csam=true so
// the 18 USC 2258A preserve + CyberTipline path fires. The critical no-regression guard.
func TestLiveCSAMBlockSetPreserved(t *testing.T) {
	m := liveModeration(t)
	bodies := corpusBodies(t, "csam")
	if len(bodies) == 0 {
		t.Skip("no csam corpus present (curated by trust-and-safety; testdata/moderation/corpus/csam/*.txt)")
	}
	for name, body := range bodies {
		res := m.screen(promptText(body))
		if res.allow() {
			t.Errorf("csam corpus %s: got ALLOW, want 451 - CRITICAL CSAM REGRESSION", name)
			continue
		}
		if !res.csam {
			t.Errorf("csam corpus %s: blocked (%d) but csam=false - the preserve+report path would not fire", name, res.status)
		}
	}
}
