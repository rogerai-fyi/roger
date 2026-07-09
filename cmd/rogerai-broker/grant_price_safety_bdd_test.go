package main

// grant_price_safety_bdd_test.go makes features/grants/grant_price_safety.feature an
// EXECUTABLE Cucumber suite. It drives the REAL owner-auth grant HTTP path (b.grants ->
// grantCreate / grantByID PATCH, signed exactly like grants_http_test.go) plus the billing
// chokepoint (store.Grant.GrantPrice) and the settle math (store.HoldFor + Finalize) that a
// negative price would exploit. Every assertion reads STORE state back.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type gpsState struct {
	db     *store.Mem
	b      *broker
	priv   ed25519.PrivateKey
	pubHex string

	code    int    // last HTTP status
	body    string // last HTTP body
	grantID string

	// billing-chokepoint + settle regression
	billIn, billOut float64
	startBal        float64
	endBal          float64
	settledCost     float64
}

func (s *gpsState) reset() {
	s.db = store.NewMem()
	pub, priv, _ := ed25519.GenerateKey(nil)
	s.priv = priv
	s.pubHex = hex.EncodeToString(pub)
	s.b = &broker{db: s.db, priv: priv, grantRL: loadRateLimiter(), rl: loadRateLimiter(), pubOfUser: map[string]string{}}
	_ = s.db.BindOwner(store.Owner{GitHubID: 1, Login: "owner", Pubkey: s.pubHex})
	s.code, s.body, s.grantID = 0, "", ""
	s.billIn, s.billOut = 0, 0
	s.startBal, s.endBal, s.settledCost = 0, 0, 0
}

func parseF(v string) float64 { f, _ := strconv.ParseFloat(v, 64); return f }

// --- HTTP drivers -----------------------------------------------------------

// post signs and drives POST /grants with the given body map.
func (s *gpsState) post(body map[string]any) {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/grants", bytes.NewReader(raw))
	signReq(r, s.priv, raw)
	w := httptest.NewRecorder()
	s.b.grants(w, r)
	s.code, s.body = w.Code, w.Body.String()
}

// patch signs and drives PATCH /grants/{id}.
func (s *gpsState) patch(id string, body map[string]any) {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPatch, "/grants/"+id, bytes.NewReader(raw))
	signReq(r, s.priv, raw)
	w := httptest.NewRecorder()
	s.b.grants(w, r)
	s.code, s.body = w.Code, w.Body.String()
}

func (s *gpsState) onlyGrant() (store.Grant, bool) {
	gs, _ := s.db.GrantsByOwner(s.pubHex)
	if len(gs) != 1 {
		return store.Grant{}, false
	}
	return gs[0], true
}

// --- Given ------------------------------------------------------------------

func (s *gpsState) ownerAuthed() error { return nil } // reset() already bound the owner

func (s *gpsState) existingPricedGrant(in, out string) error {
	s.post(map[string]any{"name": "priced", "free": false, "price_in": parseF(in), "price_out": parseF(out)})
	if s.code != http.StatusOK {
		return fmt.Errorf("setup create = %d, want 200: %s", s.code, s.body)
	}
	g, ok := s.onlyGrant()
	if !ok {
		return fmt.Errorf("setup: expected exactly one grant")
	}
	s.grantID = g.ID
	return nil
}

// storedNegativeGrant plants a grant carrying a negative price DIRECTLY in the store,
// simulating a legacy/corrupt row so the billing-chokepoint clamp is exercised end to end.
// free/self select the early-return branches of GrantPrice (which precede the clamp).
func (s *gpsState) storedNegativeGrant(in, out string) error {
	return s.plantNegative(store.Grant{PriceIn: parseF(in), PriceOut: parseF(out)})
}
func (s *gpsState) storedNegativeFreeGrant(in, out string) error {
	return s.plantNegative(store.Grant{Free: true, PriceIn: parseF(in), PriceOut: parseF(out)})
}
func (s *gpsState) storedNegativeSelfGrant(in, out string) error {
	return s.plantNegative(store.Grant{Self: true, PriceIn: parseF(in), PriceOut: parseF(out)})
}

func (s *gpsState) plantNegative(g store.Grant) error {
	g.ID, g.SecretHash, g.Owner, g.Label = "grant_neg", "h_neg", s.pubHex, "neg"
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	s.grantID = g.ID
	return nil
}

func (s *gpsState) ownerStartingBalance(v string) error {
	s.startBal = parseF(v)
	// Fund the owner's sponsor wallet (their unified account wallet).
	w := s.b.ownerSponsorWallet(s.pubHex)
	_, err := s.db.AddCredits(w, s.startBal)
	return err
}

func (s *gpsState) ownerRunsNegativeGrant() error {
	// A custom-priced grant with a NEGATIVE out-price, owned by this owner, bound to their node.
	g := store.Grant{
		ID: "grant_exploit", SecretHash: "h_exploit", Owner: s.pubHex, Label: "exploit",
		Free: false, PriceIn: 0, PriceOut: -1000,
	}
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	s.grantID = g.ID
	return nil
}

// --- When -------------------------------------------------------------------

func (s *gpsState) mints(in, out string) error {
	s.post(map[string]any{"name": "m", "free": false, "price_in": parseF(in), "price_out": parseF(out)})
	return nil
}

func (s *gpsState) patchesOut(v string) error {
	s.patch(s.grantID, map[string]any{"price_out": parseF(v)})
	return nil
}
func (s *gpsState) patchesIn(v string) error {
	s.patch(s.grantID, map[string]any{"price_in": parseF(v)})
	return nil
}
func (s *gpsState) patchesRevoked() error {
	s.patch(s.grantID, map[string]any{"revoked": true})
	return nil
}

func (s *gpsState) readsBillingPrice() error {
	g, ok := s.onlyGrant()
	if !ok {
		// stored-negative grant path plants a single grant too
		gs, _ := s.db.GrantsByOwner(s.pubHex)
		if len(gs) != 1 {
			return fmt.Errorf("expected one grant to read the billing price of")
		}
		g = gs[0]
	}
	s.billIn, s.billOut = g.GrantPrice()
	return nil
}

// settlesThroughGrant models exactly what the relay does for a custom-priced grant: the
// OWNER's sponsor wallet is the payer, cost = tokens/1e6 * grant out-price, and the money is
// captured via HoldFor + Finalize. 1,000,000 completion tokens keeps the math a clean multiple.
func (s *gpsState) settlesThroughGrant() error {
	gs, _ := s.db.GrantsByOwner(s.pubHex)
	var g store.Grant
	for _, cand := range gs {
		if cand.ID == s.grantID {
			g = cand
		}
	}
	_, out := g.GrantPrice()
	const outTokens = 1_000_000.0
	s.settledCost = (outTokens / 1e6) * out
	payer := s.b.ownerSponsorWallet(s.pubHex)
	rec := protocol.UsageReceipt{RequestID: "req_exploit", Model: "m", CompletionTokens: int(outTokens)}
	// Reserve a hold (an estimate the relay places up front), then finalize at the real cost.
	held := 1.0
	if _, err := s.db.HoldFor(payer, rec.RequestID, held); err != nil {
		return err
	}
	bal, err := s.db.Finalize(payer, "node1", held, s.settledCost, 0, rec)
	if err != nil {
		return err
	}
	s.endBal = bal
	return nil
}

// --- Then -------------------------------------------------------------------

func (s *gpsState) rejectedWith(msg string) error {
	if s.code != http.StatusBadRequest {
		return fmt.Errorf("status = %d, want 400 (%q): %s", s.code, msg, s.body)
	}
	if !strings.Contains(s.body, msg) {
		return fmt.Errorf("body %q does not contain %q", s.body, msg)
	}
	return nil
}

func (s *gpsState) noGrantCreated() error {
	gs, _ := s.db.GrantsByOwner(s.pubHex)
	if len(gs) != 0 {
		return fmt.Errorf("expected 0 grants, found %d", len(gs))
	}
	return nil
}

func (s *gpsState) grantCreated() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("status = %d, want 200: %s", s.code, s.body)
	}
	return nil
}

func (s *gpsState) grantUpdated() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("status = %d, want 200: %s", s.code, s.body)
	}
	return nil
}

func (s *gpsState) storedHasPrice(in, out string) error {
	g, ok := s.onlyGrant()
	if !ok {
		return fmt.Errorf("expected exactly one stored grant")
	}
	if g.PriceIn != parseF(in) || g.PriceOut != parseF(out) {
		return fmt.Errorf("stored price = %v/%v, want %s/%s", g.PriceIn, g.PriceOut, in, out)
	}
	return nil
}

func (s *gpsState) billingInIs(v string) error {
	if s.billIn != parseF(v) {
		return fmt.Errorf("billing in = %v, want %s", s.billIn, v)
	}
	return nil
}

func (s *gpsState) billingOutIs(v string) error {
	if s.billOut != parseF(v) {
		return fmt.Errorf("billing out = %v, want %s", s.billOut, v)
	}
	return nil
}

func (s *gpsState) balanceNotIncreased() error {
	if s.endBal > s.startBal+1e-9 {
		return fmt.Errorf("balance rose from %v to %v through the settle (credit minted)", s.startBal, s.endBal)
	}
	return nil
}

func (s *gpsState) costNeverNegative() error {
	if s.settledCost < 0 {
		return fmt.Errorf("settled cost = %v, want >= 0", s.settledCost)
	}
	return nil
}

func TestGrantPriceSafetyBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &gpsState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an owner authenticated to manage grants$`, st.ownerAuthed)
			sc.Step(`^an existing grant with price_in (\S+) and price_out (\S+)$`, st.existingPricedGrant)
			sc.Step(`^a grant whose stored price_in is (\S+) and price_out is (\S+)$`, st.storedNegativeGrant)
			sc.Step(`^a free grant whose stored price_in is (\S+) and price_out is (\S+)$`, st.storedNegativeFreeGrant)
			sc.Step(`^a self grant whose stored price_in is (\S+) and price_out is (\S+)$`, st.storedNegativeSelfGrant)
			sc.Step(`^an owner with a starting balance of (\S+) credits$`, st.ownerStartingBalance)
			sc.Step(`^that owner runs a negative-priced grant against their own node$`, st.ownerRunsNegativeGrant)

			sc.Step(`^the owner mints a grant with price_in (\S+) and price_out (\S+)$`, st.mints)
			sc.Step(`^the owner patches price_out to (\S+)$`, st.patchesOut)
			sc.Step(`^the owner patches price_in to (\S+)$`, st.patchesIn)
			sc.Step(`^the owner patches only revoked to true$`, st.patchesRevoked)
			sc.Step(`^the billing price of the grant is read$`, st.readsBillingPrice)
			sc.Step(`^a request settles through that grant$`, st.settlesThroughGrant)

			sc.Step(`^the request is rejected with "([^"]*)"$`, st.rejectedWith)
			sc.Step(`^no grant is created for that owner$`, st.noGrantCreated)
			sc.Step(`^the grant is created$`, st.grantCreated)
			sc.Step(`^the grant is updated$`, st.grantUpdated)
			sc.Step(`^the stored grant has price_in (\S+) and price_out (\S+)$`, st.storedHasPrice)
			sc.Step(`^the stored grant still has price_in (\S+) and price_out (\S+)$`, st.storedHasPrice)
			sc.Step(`^the billing input price is (\S+)$`, st.billingInIs)
			sc.Step(`^the billing output price is (\S+)$`, st.billingOutIs)
			sc.Step(`^the owner's balance is not increased by the settle$`, st.balanceNotIncreased)
			sc.Step(`^the settled cost is never negative$`, st.costNeverNegative)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/grants/grant_price_safety.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("grant price-safety scenarios failed (see godog output above)")
	}
}
