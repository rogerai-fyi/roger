package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// band_management_bdd_test.go makes features/sharing/band_management.feature EXECUTABLE,
// driving the REAL move/revoke/quota paths on a live broker (Mem store, real ed25519 keys,
// real handlers over httptest - no mocks): the MOVE (PATCH /bands/{id} -> store.MoveBand),
// the quota refusal copy, and the fail-closed invariant that a node which lost its band
// never reappears on the public market.

type bandMgmtState struct {
	t        *testing.T
	b        *broker
	owner    store.Owner
	userPriv ed25519.PrivateKey
	nodePriv ed25519.PrivateKey
	nodePub  string

	bandID   string
	moveCode int
	moveBody string
	refusal  string
	before   store.Band
	regCode  int
	regBody  map[string]any
	mintResp map[string]any // the FIRST register's response, which carries the one-time code
}

func (s *bandMgmtState) reset() {
	b, userPriv, nodePriv, nodePub := newBandBroker(s.t)
	s.b, s.userPriv, s.nodePriv, s.nodePub = b, userPriv, nodePriv, nodePub
	o, _, _ := b.db.OwnerByLogin("owner")
	s.owner = o
	// The band mutations verify a signature, so the scenarios must send real ones -
	// otherwise every "refused" step here would pass on a missing signature rather than on
	// the rule it names. See band_signature_test.go.
	rememberTestOwnerKey(o.Pubkey, userPriv)
	s.bandID, s.moveCode, s.moveBody, s.refusal = "", 0, "", ""
	s.before, s.regCode, s.regBody, s.mintResp = store.Band{}, 0, nil, nil
}

// createBand seeds a live band for the owner on nodeID.
func (s *bandMgmtState) createBand(id, nodeID, hash string) store.Band {
	bd := store.Band{
		ID: id, Owner: s.owner.Pubkey, CodeHash: hash,
		CodeDisplay: "145.225 MHz · ••••-••••", NodeID: nodeID, CreatedAt: 1000,
	}
	if err := s.b.db.CreateBand(bd); err != nil {
		s.t.Fatalf("CreateBand: %v", err)
	}
	return bd
}

// patchBand issues a signed PATCH /bands/{id} as the given pubkey.
func (s *bandMgmtState) patchBand(id, nodeID, asPubkey string) (int, string) {
	body := `{"node_id":"` + nodeID + `"}`
	r := httptest.NewRequest(http.MethodPatch, "/bands/"+id, strings.NewReader(body))
	r.Header.Set("X-Roger-Pubkey", asPubkey)
	signAsTestOwner(r, asPubkey, []byte(body))
	w := httptest.NewRecorder()
	s.b.bandsByID(w, r)
	return w.Code, w.Body.String()
}

// ---- the quota refusal ----------------------------------------------------

func (s *bandMgmtState) ownerHoldsBandOnAnotherModel() error {
	s.createBand("band_held", "roggentoo-gemma-4-31b", "h_held")
	return nil
}

func (s *bandMgmtState) triesASecondPrivateBand() error {
	_, _, s.refusal = s.b.mintBandForNode(s.owner, "eager-puma-54-qwen3-vl-8b")
	if s.refusal == "" {
		return fmt.Errorf("the second mint was NOT refused - the free quota is not being enforced")
	}
	return nil
}

func (s *bandMgmtState) refusalNamesTheModel() error {
	if !strings.Contains(s.refusal, "gemma-4-31b") {
		return fmt.Errorf("refusal does not name the blocking model: %q", s.refusal)
	}
	return nil
}

func (s *bandMgmtState) refusalNamesTheMachine() error {
	// The node id is printed whole, so the station ("roggentoo") rides along with it.
	if !strings.Contains(s.refusal, "roggentoo") {
		return fmt.Errorf("refusal does not say which machine holds the band: %q", s.refusal)
	}
	return nil
}

func (s *bandMgmtState) refusalOffersToMove() error {
	if !strings.Contains(strings.ToLower(s.refusal), "move") {
		return fmt.Errorf("refusal does not offer a move: %q", s.refusal)
	}
	return nil
}

func (s *bandMgmtState) refusalNeverSuggestsBuying() error {
	low := strings.ToLower(s.refusal)
	for _, forbidden := range []string{"buy", "purchase", "upgrade", "$5", "pack"} {
		if strings.Contains(low, forbidden) {
			return fmt.Errorf("refusal implies a purchase that does not exist (%q): %q", forbidden, s.refusal)
		}
	}
	return nil
}

// Every remedy a band error names must be reachable: move and revoke both exist now
// (PATCH and DELETE /bands/{id}), which is precisely what was untrue before this spec.
func (s *bandMgmtState) everyNamedRemedyIsReachable() error {
	s.createBand("band_held", "roggentoo-gemma-4-31b", "h_held")
	_, _, refusal := s.b.mintBandForNode(s.owner, "other-node")
	low := strings.ToLower(refusal)
	if strings.Contains(low, "revoke") {
		w := httptest.NewRecorder()
		s.b.bandsByID(w, ownerReq(http.MethodDelete, "/bands/band_held", s.owner.Pubkey))
		if w.Code != http.StatusOK {
			return fmt.Errorf("the refusal says 'revoke' but DELETE /bands/{id} answered %d", w.Code)
		}
	}
	if strings.Contains(low, "move") {
		s.createBand("band_2", "n-x", "h_2")
		if code, body := s.patchBand("band_2", "n-y", s.owner.Pubkey); code != http.StatusOK {
			return fmt.Errorf("the refusal says 'move' but PATCH /bands/{id} answered %d (%s)", code, body)
		}
	}
	return nil
}

// ---- moving --------------------------------------------------------------

func (s *bandMgmtState) ownerHoldsBandOnModelA() error {
	s.before = s.createBand("band_a", "station-model-a", "h_a")
	s.bandID = "band_a"
	return nil
}

func (s *bandMgmtState) movesItToModelB() error {
	s.moveCode, s.moveBody = s.patchBand(s.bandID, "station-model-b", s.owner.Pubkey)
	return nil
}

func (s *bandMgmtState) bandKeepsIdentity() error {
	if s.moveCode != http.StatusOK {
		return fmt.Errorf("move failed: %d %s", s.moveCode, s.moveBody)
	}
	got, ok, _ := s.b.db.BandByCodeHash("h_a")
	if !ok {
		return fmt.Errorf("the band no longer resolves by its ORIGINAL code hash - the code was rotated")
	}
	if got.ID != s.before.ID || got.CodeDisplay != s.before.CodeDisplay {
		return fmt.Errorf("identity changed: before=%+v after=%+v", s.before, got)
	}
	return nil
}

func (s *bandMgmtState) tunedInReachModelB() error {
	got, ok, _ := s.b.db.BandByNode("station-model-b")
	if !ok || got.ID != s.bandID {
		return fmt.Errorf("the code does not route to model B (ok=%v %+v)", ok, got)
	}
	// And the source stops resolving, or the old node would keep answering on it.
	if _, still, _ := s.b.db.BandByNode("station-model-a"); still {
		return fmt.Errorf("model A still carries the band after the move")
	}
	return nil
}

func (s *bandMgmtState) quotaUnchanged() error {
	n, _ := s.b.db.CountActiveBands(s.owner.Pubkey, time.Now())
	if n != 1 {
		return fmt.Errorf("active bands = %d after a move, want 1 (a move must never mint)", n)
	}
	return nil
}

func (s *bandMgmtState) modelBAlreadyCarriesABand() error {
	s.createBand("band_a", "station-model-a", "h_a")
	s.createBand("band_b", "station-model-b", "h_b")
	s.bandID = "band_a"
	return nil
}

func (s *bandMgmtState) triesToMoveOntoModelB() error {
	s.moveCode, s.moveBody = s.patchBand("band_a", "station-model-b", s.owner.Pubkey)
	return nil
}

func (s *bandMgmtState) moveRefusedOneBandPerNode() error {
	if s.moveCode != http.StatusConflict {
		return fmt.Errorf("move onto an occupied node = %d, want 409", s.moveCode)
	}
	return nil
}

func (s *bandMgmtState) bothBandsUnchanged() error {
	if got, _, _ := s.b.db.BandByNode("station-model-a"); got.ID != "band_a" {
		return fmt.Errorf("the source band moved despite the refusal: %+v", got)
	}
	if got, _, _ := s.b.db.BandByNode("station-model-b"); got.ID != "band_b" {
		return fmt.Errorf("the destination band was displaced: %+v", got)
	}
	return nil
}

func (s *bandMgmtState) aStrangerTriesToMove() error {
	s.createBand("band_a", "station-model-a", "h_a")
	strangerPub, strangerPriv, _ := ed25519.GenerateKey(nil)
	stranger := hex.EncodeToString(strangerPub)
	_ = s.b.db.BindOwner(store.Owner{GitHubID: 99, Login: "stranger", Pubkey: stranger})
	// A real stranger holds a real key: the refusal must come from OWNERSHIP, not from a
	// missing signature, or this scenario would pass for the wrong reason.
	rememberTestOwnerKey(stranger, strangerPriv)
	s.moveCode, s.moveBody = s.patchBand("band_a", "their-node", stranger)
	return nil
}

func (s *bandMgmtState) refusedAndOpaque() error {
	if s.moveCode != http.StatusNotFound {
		return fmt.Errorf("a stranger's move = %d, want 404", s.moveCode)
	}
	// A band that does not exist must answer identically, or ids become enumerable.
	unknownCode, unknownBody := s.patchBand("band_does_not_exist", "their-node", s.owner.Pubkey)
	if unknownCode != s.moveCode || unknownBody != s.moveBody {
		return fmt.Errorf("a foreign band is distinguishable from a missing one:\n foreign: %d %s\n missing: %d %s",
			s.moveCode, s.moveBody, unknownCode, unknownBody)
	}
	if got, _, _ := s.b.db.BandByNode("station-model-a"); got.ID != "band_a" {
		return fmt.Errorf("a stranger moved the band")
	}
	return nil
}

func (s *bandMgmtState) destinationNotBroadcasting() error {
	s.createBand("band_a", "station-model-a", "h_a")
	s.bandID = "band_a"
	// "priv1" is the node id registerPrivate uses; nothing has registered it yet.
	if _, live := s.b.nodes["priv1"]; live {
		return fmt.Errorf("the destination is already registered - the premise does not hold")
	}
	return nil
}

func (s *bandMgmtState) movesToIt() error {
	s.moveCode, s.moveBody = s.patchBand("band_a", "priv1", s.owner.Pubkey)
	return nil
}

func (s *bandMgmtState) moveAccepted() error {
	if s.moveCode != http.StatusOK {
		return fmt.Errorf("moving to an off-air model = %d, want 200 (%s)", s.moveCode, s.moveBody)
	}
	return nil
}

// The payoff: when that model DOES go private, register reuses the moved band - no new
// code, no quota consumed. This is the seam the whole feature rides on.
func (s *bandMgmtState) bindsOnNextPrivateRegisterMintingNothing() error {
	resp, code := registerPrivate(s.t, s.b, s.nodePriv, s.nodePub, s.userPriv, true)
	if code != http.StatusOK {
		return fmt.Errorf("private register after a move = %d (%v)", code, resp)
	}
	if resp["band_id"] != "band_a" {
		return fmt.Errorf("register bound band_id=%v, want the MOVED band band_a", resp["band_id"])
	}
	if _, leaked := resp["band_code"]; leaked {
		return fmt.Errorf("register re-issued a secret code for an existing band")
	}
	n, _ := s.b.db.CountActiveBands(s.owner.Pubkey, time.Now())
	if n != 1 {
		return fmt.Errorf("active bands = %d, want 1 (binding must not mint)", n)
	}
	return nil
}

// ---- fail closed ----------------------------------------------------------

func (s *bandMgmtState) privateNodeLostItsBand() error {
	// The node registers privately and mints its band...
	if _, code := registerPrivate(s.t, s.b, s.nodePriv, s.nodePub, s.userPriv, true); code != http.StatusOK {
		return fmt.Errorf("initial private register = %d", code)
	}
	bd, ok, _ := s.b.db.BandByNode("priv1")
	if !ok {
		return fmt.Errorf("no band was minted for the node")
	}
	// ...then the owner moves that band to a different model.
	if code, body := s.patchBand(bd.ID, "station-elsewhere", s.owner.Pubkey); code != http.StatusOK {
		return fmt.Errorf("moving the band away = %d (%s)", code, body)
	}
	return nil
}

func (s *bandMgmtState) thatNodeReRegisters() error {
	s.regBody, s.regCode = registerPrivate(s.t, s.b, s.nodePriv, s.nodePub, s.userPriv, true)
	return nil
}

func (s *bandMgmtState) notOnThePublicMarket() error {
	// The register must be REFUSED rather than succeeding as a public node: a station that
	// was private must never become publicly discoverable because its band moved.
	if s.regCode == http.StatusOK {
		return fmt.Errorf("the node re-registered successfully after losing its band (%v) - it must fail closed", s.regBody)
	}
	w := httptest.NewRecorder()
	s.b.discover(w, httptest.NewRequest(http.MethodGet, "/discover", nil))
	if strings.Contains(w.Body.String(), "priv1") {
		return fmt.Errorf("the node leaked onto the public market: %s", w.Body.String())
	}
	return nil
}

func (s *bandMgmtState) noReplacementBandMinted() error {
	n, _ := s.b.db.CountActiveBands(s.owner.Pubkey, time.Now())
	if n != 1 {
		return fmt.Errorf("active bands = %d, want 1 (the node must not mint itself a replacement)", n)
	}
	if bd, ok, _ := s.b.db.BandByNode("priv1"); ok {
		return fmt.Errorf("the node minted a replacement band %s", bd.ID)
	}
	return nil
}

func (s *bandMgmtState) operatorToldWhereItWent() error {
	msg := fmt.Sprint(s.regBody)
	if !strings.Contains(msg, "station-elsewhere") {
		return fmt.Errorf("the refusal does not say which model now holds the band: %s", msg)
	}
	return nil
}

// ---- carried-over invariants ---------------------------------------------

func (s *bandMgmtState) privateStaysInvisible() error {
	// Keep the mint response: it carries the one-time code the next step checks. The quota
	// is 1, so a later step cannot simply mint another band to inspect.
	resp, code := registerPrivate(s.t, s.b, s.nodePriv, s.nodePub, s.userPriv, true)
	if code != http.StatusOK {
		return fmt.Errorf("private register = %d", code)
	}
	s.mintResp = resp
	for _, path := range []string{"/discover", "/market"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if path == "/discover" {
			s.b.discover(w, r)
		} else {
			s.b.market(w, r)
		}
		if strings.Contains(w.Body.String(), "priv1") {
			return fmt.Errorf("a private node appeared on %s: %s", path, w.Body.String())
		}
	}
	return nil
}

func (s *bandMgmtState) codeShownOnlyAtMint() error {
	first := s.mintResp
	if first == nil {
		first, _ = registerPrivate(s.t, s.b, s.nodePriv, s.nodePub, s.userPriv, true)
	}
	if first["band_code"] == nil || first["band_code"] == "" {
		return fmt.Errorf("the mint did not return the one-time code")
	}
	again, _ := registerPrivate(s.t, s.b, s.nodePriv, s.nodePub, s.userPriv, true)
	if _, leaked := again["band_code"]; leaked {
		return fmt.Errorf("a re-register re-issued the secret code")
	}
	// And a move must not re-issue it either.
	bd, _, _ := s.b.db.BandByNode("priv1")
	_, body := s.patchBand(bd.ID, "station-somewhere", s.owner.Pubkey)
	if strings.Contains(body, "band_code") {
		return fmt.Errorf("a move re-issued the secret code: %s", body)
	}
	return nil
}

func (s *bandMgmtState) ceilingStillBindsPrivate() error {
	reg := protocol.NodeRegistration{
		NodeID: "priv2", PubKey: s.nodePub, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers:  []protocol.ModelOffer{{Model: "m", Ctx: 4096, PriceOut: 1e9}},
		Private: true,
	}
	reg.SignRegistration(s.nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", strings.NewReader(string(body)))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	if w.Code == http.StatusOK {
		return fmt.Errorf("an over-ceiling PRIVATE registration was accepted - --private became a price bypass")
	}
	return nil
}

func TestBandManagementBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &bandMgmtState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			// the quota refusal
			sc.Step(`^an owner already holds their one free band on another model$`, st.ownerHoldsBandOnAnotherModel)
			sc.Step(`^they try to put a second model on a private band$`, st.triesASecondPrivateBand)
			sc.Step(`^the refusal names the model that band is currently on$`, st.refusalNamesTheModel)
			sc.Step(`^it names the machine that model is on, because it may not be this one$`, st.refusalNamesTheMachine)
			sc.Step(`^it offers to MOVE that band to this model instead$`, st.refusalOffersToMove)
			sc.Step(`^it never suggests buying more bands, because no purchase path exists$`, st.refusalNeverSuggestsBuying)
			sc.Step(`^no band error tells the owner to revoke unless a revoke key is reachable from the surface showing that error$`, st.everyNamedRemedyIsReachable)
			// moving
			sc.Step(`^an owner holds a band on model A$`, st.ownerHoldsBandOnModelA)
			sc.Step(`^they move it to model B$`, st.movesItToModelB)
			sc.Step(`^the band keeps its id, its code, and its masked display$`, st.bandKeepsIdentity)
			sc.Step(`^everyone already tuned in to that code reaches model B without being re-told anything$`, st.tunedInReachModelB)
			sc.Step(`^no new band is minted, so the quota is unchanged$`, st.quotaUnchanged)
			sc.Step(`^model B already carries a band$`, st.modelBAlreadyCarriesABand)
			sc.Step(`^the owner tries to move another band onto model B$`, st.triesToMoveOntoModelB)
			sc.Step(`^the move is refused, because one node carries at most one band$`, st.moveRefusedOneBandPerNode)
			sc.Step(`^both bands are left exactly as they were$`, st.bothBandsUnchanged)
			sc.Step(`^anyone other than the issuing owner tries to move a band$`, st.aStrangerTriesToMove)
			sc.Step(`^it is refused, and the refusal does not reveal whether that band exists$`, st.refusedAndOpaque)
			sc.Step(`^the destination model is not currently broadcasting$`, st.destinationNotBroadcasting)
			sc.Step(`^the owner moves the band to it$`, st.movesToIt)
			sc.Step(`^the move is accepted$`, st.moveAccepted)
			sc.Step(`^the band binds when that model next goes on air privately, minting nothing new$`, st.bindsOnNextPrivateRegisterMintingNothing)
			// fail closed
			sc.Step(`^a private node whose band has been moved away$`, st.privateNodeLostItsBand)
			sc.Step(`^that node re-registers$`, st.thatNodeReRegisters)
			sc.Step(`^it does NOT appear on the public market$`, st.notOnThePublicMarket)
			sc.Step(`^it does not quietly mint a replacement band to stay reachable$`, st.noReplacementBandMinted)
			sc.Step(`^its operator is told the band moved, and which model holds it now$`, st.operatorToldWhereItWent)
			// carried over
			sc.Step(`^a private band stays invisible to /discover and /market$`, st.privateStaysInvisible)
			sc.Step(`^the one-time code is still shown only at mint$`, st.codeShownOnlyAtMint)
			sc.Step(`^the global price ceiling still binds a private band$`, st.ceilingStillBindsPrivate)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/sharing/band_management.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("band-management scenarios failed (see godog output above)")
	}
}
