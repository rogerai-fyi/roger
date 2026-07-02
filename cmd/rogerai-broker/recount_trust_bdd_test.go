package main

// recount_trust_bdd_test.go makes features/trust/recount.feature EXECUTABLE, driving the
// REAL L1 token re-count + billing + earnings-hold path as a TRUST pillar: the broker bills
// the verified LESSER of (node claim, exact broker re-count) on BOTH axes (settleRecount /
// settleRecountPrompt), only an EXACT re-count can flag a discrepancy (a heuristic is an
// outlier gate, never a penalty), a discrepancy past the billing tolerance records a
// discrepancy AND holds the node's lots from promotion (observeRecount + SetNodeRecountHold),
// the HARD fail-closed byte floor clamps + strikes an arithmetically-impossible prompt claim
// even with NO sidecar, billing fails OPEN to the claim when the re-count is down/disabled,
// and a recount hold is recoverable (auto-expiry via recountHoldSweepOnce, admin clear via
// /admin/unhold). It reuses the sidecar stub + sentinel helpers from recount_billing_bdd_test.go
// (same package) and reads STORE state for the consequences - no mocks.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

type trState struct {
	b       *broker
	sidecar *httptest.Server
	node    string
	owner   string
	model   string

	claim, recount int
	billed         int
}

func (s *trState) reset() {
	if s.sidecar != nil {
		s.sidecar.Close()
	}
	// Sidecar stub: the re-count is encoded in the posted text via sentinel("t:<n>:<exact>").
	s.sidecar = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Model, Text string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		tok, exact, ok := parseSentinel(in.Text)
		if !ok {
			tok, exact = 0, false
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": tok, "exact": exact})
	}))
	os.Setenv("TOKENIZER_URL", s.sidecar.URL)
	os.Setenv("ROGERAI_RECOUNT_TOLERANCE", "")
	os.Setenv("ROGERAI_RECOUNT_STRIKE_TOLERANCE", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	s.b = buildBroker(store.NewMem(), priv, 0.30, 0, time.Hour)
	s.node, s.owner, s.model = "n1", "op1", "m"
	_ = s.b.db.BindNode(s.node, s.owner)
	_ = s.b.db.BindOwner(store.Owner{GitHubID: 1, Login: s.owner, Pubkey: s.owner})
	s.claim, s.recount, s.billed = 0, 0, 0
}

func (s *trState) held() bool {
	h, _ := s.b.db.RecountHeldNodes()
	return h[s.node]
}

func (s *trState) strikeCount() int {
	st, _ := s.b.db.StrikesByOwner(s.owner, 0)
	return len(st)
}

// --- Background ------------------------------------------------------------

func (s *trState) servedWithReceipt() error { return nil } // the suite drives the settle funcs directly
func (s *trState) sidecarAvailable() error  { return nil } // reset() wired the stub

// --- bill the verified lesser count ----------------------------------------

func (s *trState) claimsMoreCompletion() error {
	s.claim, s.recount = 1000, 400 // exact re-count below the claim
	return nil
}

func (s *trState) billedOnCompletionRecount() error {
	s.billed = s.b.settleRecount(s.node, "rq", s.model, sentinel(s.recount, true), s.claim)
	if s.billed != s.recount {
		return fmt.Errorf("completion billed %d, want the broker re-count %d (not the claim %d)", s.billed, s.recount, s.claim)
	}
	return nil
}

func (s *trState) claimsMorePrompt() error {
	s.claim, s.recount = 1000, 400
	return nil
}

func (s *trState) billedOnPromptRecount() error {
	// bodyLen large so the byte floor is not the binding constraint here.
	s.billed = s.b.settleRecountPrompt(s.node, "rq", s.model, sentinel(s.recount, true), s.claim, 100000)
	if s.billed != s.recount {
		return fmt.Errorf("prompt billed %d, want the broker re-count %d (not the claim %d)", s.billed, s.recount, s.claim)
	}
	return nil
}

// --- the input byte floor --------------------------------------------------

func (s *trState) claimsMoreThanBytes() error {
	s.claim = 20000 // far above the body bytes + the ban margin (8192)
	return nil
}

func (s *trState) clampedToByteFloorAndStruck() error {
	const bodyLen = 100
	before := s.strikeCount()
	s.billed = s.b.settleRecountPrompt(s.node, "rq-floor", s.model, "", s.claim, bodyLen)
	if s.billed != bodyLen {
		return fmt.Errorf("prompt billed %d, want clamp to the byte floor %d (no tokenizer emits more tokens than bytes)", s.billed, bodyLen)
	}
	if s.strikeCount() <= before {
		return fmt.Errorf("an arithmetically-impossible prompt claim must strike the owner; strikes %d -> %d", before, s.strikeCount())
	}
	return nil
}

func (s *trState) holdsEvenWithoutSidecar() error {
	const bodyLen = 100
	s.b.recount.url = "" // sidecar gone: the byte floor is the fail-closed backstop
	if got := s.b.settleRecountPrompt(s.node, "rq-floor2", s.model, "", s.claim, bodyLen); got != bodyLen {
		return fmt.Errorf("with no sidecar the byte floor must still clamp to %d, got %d", bodyLen, got)
	}
	return nil
}

// --- exact vs heuristic ----------------------------------------------------

// trustOf reads the node's trust row UNDER metricsMu: settleRecount runs
// observeRecount on a goroutine that writes b.trust under that lock, so a bare map
// read here raced it (caught by `go test -race`).
func (s *trState) trustOf() trustState {
	s.b.metricsMu.Lock()
	defer s.b.metricsMu.Unlock()
	return s.b.trust[s.node]
}

func (s *trState) modelHeuristicOnly() error { s.claim, s.recount = 1000, 400; return nil }

func (s *trState) heuristicDiffersFromClaim() error {
	s.b.observeRecount(s.node, "", s.claim, s.recount, false) // exact=false => outlier gate only
	return nil
}

func (s *trState) noDiscrepancyRecorded() error {
	if s.trustOf().discrepancies != 0 {
		return fmt.Errorf("a heuristic re-count must not flag a discrepancy, got %d", s.trustOf().discrepancies)
	}
	if s.held() {
		return fmt.Errorf("a heuristic re-count must not hold the node's lots")
	}
	return nil
}

func (s *trState) exactWithinTolerance() error {
	s.b.recount.tolerance = 0.5   // a wide billing tolerance
	s.claim, s.recount = 110, 100 // over = 10% < 50%
	return nil
}

func (s *trState) clampedNoStrike() error {
	before := s.strikeCount()
	s.billed = s.b.settleRecount(s.node, "rq-tol", s.model, sentinel(s.recount, true), s.claim)
	s.b.observeRecount(s.node, "", s.claim, s.recount, true)
	if s.billed != s.recount {
		return fmt.Errorf("billed %d, want the verified count %d (the lesser)", s.billed, s.recount)
	}
	if s.trustOf().discrepancies != 0 || s.held() || s.strikeCount() != before {
		return fmt.Errorf("a within-tolerance variance must not record a discrepancy/hold/strike (disc=%d held=%v strikes=%d->%d)",
			s.trustOf().discrepancies, s.held(), before, s.strikeCount())
	}
	return nil
}

// --- a sustained discrepancy holds earnings --------------------------------

func (s *trState) exactBelowByMoreThanTolerance() error {
	s.b.recount.tolerance = 0.1    // 10% billing tolerance
	s.claim, s.recount = 1000, 400 // over = 150% >> 10%
	return nil
}

func (s *trState) discrepancyRecordedAndHeld() error {
	s.b.observeRecount(s.node, "", s.claim, s.recount, true)
	if s.trustOf().discrepancies != 1 {
		return fmt.Errorf("a past-tolerance exact over-report must record a discrepancy, got %d", s.trustOf().discrepancies)
	}
	if !s.held() {
		return fmt.Errorf("a past-tolerance discrepancy must HOLD the node's lots from promotion")
	}
	return nil
}

// --- fail-open billing -----------------------------------------------------

func (s *trState) sidecarUnavailable() error {
	s.sidecar.Close() // the stub now refuses connections => sidecarCount returns ok=false
	return nil
}

func (s *trState) completionBilledAtClaim() error {
	s.claim, s.recount = 1000, 400
	s.billed = s.b.settleRecount(s.node, "rq", s.model, sentinel(s.recount, true), s.claim)
	if s.billed != s.claim {
		return fmt.Errorf("with the sidecar down, completion must fail OPEN to the claim %d, got %d", s.claim, s.billed)
	}
	return nil
}

func (s *trState) byteFloorStillApplies() error {
	const bodyLen = 100
	if got := s.b.settleRecountPrompt(s.node, "rq-floor3", s.model, "", 20000, bodyLen); got != bodyLen {
		return fmt.Errorf("the byte floor must remain the fail-closed backstop (clamp to %d), got %d", bodyLen, got)
	}
	return nil
}

func (s *trState) recountNotConfigured() error { s.b.recount.url = ""; return nil }

func (s *trState) billedOnClaimedCounts() error {
	s.claim, s.recount = 1000, 400
	s.billed = s.b.settleRecount(s.node, "rq", s.model, sentinel(s.recount, true), s.claim)
	if s.billed != s.claim {
		return fmt.Errorf("with re-count disabled, billing must use the claim %d, got %d", s.claim, s.billed)
	}
	return nil
}

func (s *trState) byteFloorStillApplies2() error { return s.byteFloorStillApplies() }

// --- recount hold is recoverable -------------------------------------------

func (s *trState) nodeUnderHold() error {
	if err := s.b.db.SetNodeRecountHold(s.node, true); err != nil {
		return err
	}
	if !s.held() {
		return fmt.Errorf("setup: node should be under a recount hold")
	}
	return nil
}

func (s *trState) windowElapsesNoFreshDiscrepancy() error {
	// One sweep with a cutoff AFTER the hold's placement (the window has elapsed) auto-expires
	// a hold no fresh discrepancy has re-armed.
	s.b.recountHoldSweepOnce(time.Now().Add(time.Hour))
	return nil
}

func (s *trState) holdExpiredLotsPromote() error {
	if s.held() {
		return fmt.Errorf("the hold must auto-expire past the review window with no fresh discrepancy")
	}
	return nil
}

func (s *trState) adminClearsHold() error {
	s.b.adminKey = "admin-key"
	body := []byte(`{"node":"` + s.node + `"}`)
	r := httptest.NewRequest(http.MethodPost, "/admin/unhold", bytes.NewReader(body))
	r.Header.Set("X-Roger-Admin", s.b.adminKey)
	w := httptest.NewRecorder()
	s.b.adminUnhold(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("adminUnhold = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	return nil
}

func (s *trState) lotsPromoteNextSweep() error {
	if s.held() {
		return fmt.Errorf("an admin-cleared hold must be lifted so the lots promote again")
	}
	return nil
}

func TestTrustRecountBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &trState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.sidecar != nil {
					st.sidecar.Close()
				}
				return ctx, nil
			})
			sc.Step(`^a served request with a node-signed receipt$`, st.servedWithReceipt)
			sc.Step(`^the L1 re-count sidecar is available$`, st.sidecarAvailable)
			sc.Step(`^the node claims more completion tokens than the broker's exact re-count$`, st.claimsMoreCompletion)
			sc.Step(`^the request is billed on the broker re-count, not the claim$`, st.billedOnCompletionRecount)
			sc.Step(`^the node claims more prompt tokens than the broker's exact re-count$`, st.claimsMorePrompt)
			sc.Step(`^the input is billed on the broker re-count, not the claim$`, st.billedOnPromptRecount)
			sc.Step(`^the node claims more prompt tokens than the request body has bytes$`, st.claimsMoreThanBytes)
			sc.Step(`^the prompt is clamped to the byte floor and the node is struck$`, st.clampedToByteFloorAndStruck)
			sc.Step(`^this holds even when the sidecar is unavailable$`, st.holdsEvenWithoutSidecar)
			sc.Step(`^the model has no exact tokenizer so the re-count is heuristic$`, st.modelHeuristicOnly)
			sc.Step(`^the heuristic re-count differs from the claim$`, st.heuristicDiffersFromClaim)
			sc.Step(`^no discrepancy is recorded \(the heuristic is an outlier gate only\)$`, st.noDiscrepancyRecorded)
			sc.Step(`^an exact re-count that differs from the claim within the billing tolerance$`, st.exactWithinTolerance)
			sc.Step(`^billing is clamped to tolerance and no discrepancy strike is recorded$`, st.clampedNoStrike)
			sc.Step(`^an exact re-count below the claim by more than the tolerance$`, st.exactBelowByMoreThanTolerance)
			sc.Step(`^a discrepancy is recorded and the node's earning lots are held from promotion$`, st.discrepancyRecordedAndHeld)
			sc.Step(`^the re-count sidecar is unavailable$`, st.sidecarUnavailable)
			sc.Step(`^the completion is billed at the node's claim$`, st.completionBilledAtClaim)
			sc.Step(`^the input byte floor still applies as the fail-closed backstop$`, st.byteFloorStillApplies)
			sc.Step(`^L1 re-count is not configured$`, st.recountNotConfigured)
			sc.Step(`^the request is billed on the node's claimed counts$`, st.billedOnClaimedCounts)
			sc.Step(`^the input byte floor still applies$`, st.byteFloorStillApplies2)
			sc.Step(`^a node under a recount hold$`, st.nodeUnderHold)
			sc.Step(`^more than the recount-hold window elapses with no fresh discrepancy$`, st.windowElapsesNoFreshDiscrepancy)
			sc.Step(`^the hold auto-expires and the held lots promote again$`, st.holdExpiredLotsPromote)
			sc.Step(`^an admin clears the hold$`, st.adminClearsHold)
			sc.Step(`^the held lots promote again on the next sweep$`, st.lotsPromoteNextSweep)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/trust/recount.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("trust/recount behavior scenarios failed (see godog output above)")
	}
}
