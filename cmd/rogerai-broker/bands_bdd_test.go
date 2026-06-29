package main

// bands_bdd_test.go makes features/discovery/bands.feature EXECUTABLE, driving the REAL
// private-band path on a live broker (Mem store, no mocks): mint (register --private ->
// mintBandForNode), resolve (resolveFreqAllow / bandResolve), the owner listing
// (GET /bands -> bandView), and market invisibility (discover / market). It pins the two
// just-landed fixes as permanent regressions:
//
//	(a) an over-ceiling --private registration is REJECTED - the global price ceiling binds
//	    every band; --private is a DISCOVERY choice, not a price-bypass. (Cross-references the
//	    full public/private/confidential matrix in features/pricing/price_ceiling.feature.)
//	(b) the PERSISTED cosmetic display is MASKED + non-recoverable, so it cannot reconstruct
//	    or resolve the band - only the one-time code does. (The resolve-side proof lives in
//	    features/security/band_code_secrecy.feature; here we pin the stored-state property.)

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
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

type bandsState struct {
	t *testing.T

	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string

	model   string // model shared on the band
	code    string // the one-time full band code (with the secret tail)
	display string // the persisted, masked cosmetic display

	// resolve outcomes
	allow   map[string]bool
	band    store.Band
	present bool
	rStatus int    // last bandResolve / listing HTTP status
	rBody   string // last bandResolve / listing body

	// public-feed bodies
	discBody string
	mktBody  string

	// register-ceiling outcome
	over    protocol.ModelOffer
	regCode int
	regMsg  string
}

func (s *bandsState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.model, s.code, s.display = "", "", ""
	s.allow, s.present, s.band = nil, false, store.Band{}
	s.rStatus, s.rBody = 0, ""
	s.discBody, s.mktBody = "", ""
	s.over, s.regCode, s.regMsg = protocol.ModelOffer{}, 0, ""
}

// sharePrivate registers node "priv1" on a private band offering model, signed by both the
// node (proof of possession) and the owner (login-to-go-private), and captures the one-time
// code + the persisted masked display from the broker's response.
func (s *bandsState) sharePrivate(model string) error {
	s.model = model
	resp, err := s.registerPriv(model)
	if err != nil {
		return err
	}
	s.code, _ = resp["band_code"].(string)
	s.display, _ = resp["band_display"].(string)
	return nil
}

// registerPriv POSTs a signed private registration for node "priv1" and returns the parsed
// response (band_code is present only on the FIRST mint for the owner's node).
func (s *bandsState) registerPriv(model string) (map[string]any, error) {
	reg := protocol.NodeRegistration{
		NodeID: "priv1", PubKey: s.nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: model, Ctx: 4096}}, Private: true,
	}
	reg.SignRegistration(s.nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	if w.Code != http.StatusOK {
		return nil, fmt.Errorf("private register = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp, nil
}

func (s *bandsState) givenPrivateBand() error { return s.sharePrivate("gpt-oss-20b") }

func (s *bandsState) codeShownOnce() error {
	if s.code == "" {
		return fmt.Errorf("mint returned no one-time band_code")
	}
	// "never retrievable again": a re-register of the SAME node returns band_id but NO code.
	resp, err := s.registerPriv(s.model)
	if err != nil {
		return err
	}
	if c, _ := resp["band_code"].(string); c != "" {
		return fmt.Errorf("re-register leaked the one-time code again: %q", c)
	}
	if resp["band_id"] == nil {
		return fmt.Errorf("re-register missing band_id (should still identify the existing band)")
	}
	return nil
}

func (s *bandsState) onlyHashPersisted() error {
	bnd, found, err := s.b.db.BandByCodeHash(protocol.BandCodeHash(s.code))
	if err != nil || !found {
		return fmt.Errorf("band not found by sha256(code): found=%v err=%v", found, err)
	}
	if bnd.CodeHash != protocol.BandCodeHash(s.code) {
		return fmt.Errorf("persisted code_hash %q != sha256(code) %q", bnd.CodeHash, protocol.BandCodeHash(s.code))
	}
	if bnd.CodeHash == s.code {
		return fmt.Errorf("persisted code_hash equals the raw code - the secret is stored verbatim")
	}
	if tail := protocol.CanonicalBandTail(s.code); tail != "" && strings.Contains(bnd.CodeDisplay, tail) {
		return fmt.Errorf("persisted display %q contains the secret tail %q", bnd.CodeDisplay, tail)
	}
	return nil
}

func (s *bandsState) maskedDisplayStored() error {
	if s.display == "" {
		return fmt.Errorf("mint returned no cosmetic band_display")
	}
	if s.display == s.code {
		return fmt.Errorf("persisted display equals the one-time code %q - the secret would be stored", s.code)
	}
	if tail := protocol.CanonicalBandTail(s.display); tail != "" {
		return fmt.Errorf("persisted display %q canonicalizes to a recoverable tail %q - it must be masked", s.display, tail)
	}
	bnd, found, _ := s.b.db.BandByCodeHash(protocol.BandCodeHash(s.code))
	if !found || bnd.CodeDisplay != s.display {
		return fmt.Errorf("persisted CodeDisplay %q != returned display %q", bnd.CodeDisplay, s.display)
	}
	return nil
}

func (s *bandsState) presentCorrectCode() error {
	s.allow, s.band, s.present = s.b.resolveFreqAllow(s.code, time.Now())
	return nil
}

func (s *bandsState) allowSetIsOnlyThatNode() error {
	if !s.present {
		return fmt.Errorf("resolveFreqAllow present=false for the valid code")
	}
	if len(s.allow) != 1 || !s.allow["priv1"] {
		return fmt.Errorf("allow set = %v, want exactly {priv1}", s.allow)
	}
	if s.band.NodeID != "priv1" {
		return fmt.Errorf("resolved band node = %q, want priv1", s.band.NodeID)
	}
	return nil
}

func (s *bandsState) routingRestrictedToNode() error {
	// Without the freq allow-set, the private node is invisible on the public market path.
	s.b.mu.Lock()
	_, _, okPub := s.b.pickFor(s.model, false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	s.b.mu.Unlock()
	if okPub {
		return fmt.Errorf("private node picked on the OPEN MARKET path (no freq) - it must be hidden")
	}
	// With the resolved allow-set, pick admits exactly that node.
	s.b.mu.Lock()
	node, _, ok := s.b.pickFor(s.model, false, 0, 0, 0, "", nil, nil, s.allow, pickReq{})
	s.b.mu.Unlock()
	if !ok || node.NodeID != "priv1" {
		return fmt.Errorf("freq-admitted pick = %+v ok=%v, want priv1", node, ok)
	}
	return nil
}

func (s *bandsState) presentWrongCode() error {
	const wrong = "147.520 MHz · ZZZZ-ZZZZ" // valid FORM, never a minted tail
	s.allow, s.band, s.present = s.b.resolveFreqAllow(wrong, time.Now())
	body, _ := json.Marshal(map[string]string{"freq": wrong})
	w := httptest.NewRecorder()
	s.b.bandResolve(w, httptest.NewRequest(http.MethodPost, "/bands/resolve", bytes.NewReader(body)))
	s.rStatus, s.rBody = w.Code, w.Body.String()
	return nil
}

func (s *bandsState) nothingResolves() error {
	if s.rStatus != http.StatusNotFound {
		return fmt.Errorf("wrong-code resolve = %d, want 404; body=%s", s.rStatus, s.rBody)
	}
	if !strings.Contains(s.rBody, `"offers":[]`) {
		return fmt.Errorf("wrong-code body = %s, want the uniform {\"offers\":[]} negative", s.rBody)
	}
	if !s.present {
		return fmt.Errorf("resolveFreqAllow present=false; want present (uniform) with an empty allow")
	}
	if len(s.allow) != 0 {
		return fmt.Errorf("wrong code reached nodes %v, want none (no hidden node reachable)", s.allow)
	}
	return nil
}

func (s *bandsState) anyoneGetsPublicFeeds() error {
	dw := httptest.NewRecorder()
	s.b.discover(dw, httptest.NewRequest(http.MethodGet, "/discover", nil))
	s.discBody = dw.Body.String()
	mw := httptest.NewRecorder()
	s.b.market(mw, httptest.NewRequest(http.MethodGet, "/market", nil))
	s.mktBody = mw.Body.String()
	return nil
}

func (s *bandsState) nodeNeverAppears() error {
	var disc struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal([]byte(s.discBody), &disc)
	for _, o := range disc.Offers {
		if o.NodeID == "priv1" {
			return fmt.Errorf("/discover leaked the private node priv1")
		}
	}
	var mkt struct {
		Market []marketView `json:"market"`
	}
	_ = json.Unmarshal([]byte(s.mktBody), &mkt)
	for _, mv := range mkt.Market {
		if mv.Model == s.model && mv.Providers > 0 {
			return fmt.Errorf("/market counted the private node as a public provider for %q", s.model)
		}
	}
	return nil
}

func (s *bandsState) metadataFetched() error {
	if s.code == "" { // this scenario has no Given - mint one to fetch
		if err := s.sharePrivate("gpt-oss-20b"); err != nil {
			return err
		}
	}
	// Drive the REAL owner listing endpoint: GET /bands -> bandView per band.
	r := httptest.NewRequest(http.MethodGet, "/bands", nil)
	signReq(r, s.userPriv, nil)
	w := httptest.NewRecorder()
	s.b.bands(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("GET /bands = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	s.rStatus, s.rBody = w.Code, w.Body.String()
	return nil
}

func (s *bandsState) maskedDisplayNonSecretOnly() error {
	var out struct {
		Bands []map[string]any `json:"bands"`
	}
	if err := json.Unmarshal([]byte(s.rBody), &out); err != nil {
		return fmt.Errorf("decode /bands: %v; body=%s", err, s.rBody)
	}
	if len(out.Bands) != 1 {
		return fmt.Errorf("GET /bands returned %d bands, want 1", len(out.Bands))
	}
	bv := out.Bands[0]
	if d, _ := bv["display"].(string); d != s.display {
		return fmt.Errorf("listing display = %q, want the masked display %q", d, s.display)
	}
	for _, k := range []string{"code", "code_hash", "freq", "tail", "secret"} {
		if _, ok := bv[k]; ok {
			return fmt.Errorf("listing leaked a secret-bearing field %q: %v", k, bv)
		}
	}
	return nil
}

func (s *bandsState) noCodeOrHashInUsableForm() error {
	if strings.Contains(s.rBody, s.code) {
		return fmt.Errorf("listing body contains the full one-time code")
	}
	if h := protocol.BandCodeHash(s.code); strings.Contains(s.rBody, h) {
		return fmt.Errorf("listing body contains the code_hash %q", h)
	}
	if tail := protocol.CanonicalBandTail(s.display); tail != "" {
		return fmt.Errorf("listed display canonicalizes to a usable tail %q", tail)
	}
	return nil
}

func (s *bandsState) ceilingWouldReject() error {
	s.over = protocol.ModelOffer{Model: "gpt-oss-20b", Ctx: 4096, PriceOut: 250} // > $100/1M out
	return nil
}

func (s *bandsState) shareWithPrivateInstead() error {
	s.regCode, s.regMsg = registerWith(s.t, s.b, "priv1", s.nodePriv, s.nodePubHex, s.userPriv, true, s.over, true, false)
	return nil
}

func (s *bandsState) stillRejectedByCeiling() error {
	if s.regCode != http.StatusBadRequest {
		return fmt.Errorf("over-ceiling --private register = %d, want 400 (--private must NOT bypass the ceiling); msg=%q", s.regCode, s.regMsg)
	}
	if !strings.Contains(s.regMsg, "ceiling") {
		return fmt.Errorf("rejection missing the ceiling copy: %q", s.regMsg)
	}
	return nil
}

func TestBandsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &bandsState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			// mint
			sc.Step(`^an operator shares "([^"]*)" with --private$`, func(model string) error { return st.sharePrivate(model) })
			sc.Step(`^a frequency code is generated and shown ONCE \(never retrievable again\)$`, st.codeShownOnce)
			sc.Step(`^only sha256\(code\) \(code_hash\) is persisted, never the full code itself$`, st.onlyHashPersisted)
			sc.Step(`^a MASKED, non-recoverable cosmetic display is stored for the owner's re-display$`, st.maskedDisplayStored)
			// resolve (correct)
			sc.Step(`^a node is on a private band with a known frequency code$`, st.givenPrivateBand)
			sc.Step(`^a consumer presents that code$`, st.presentCorrectCode)
			sc.Step(`^resolveFreqAllow returns the allow set containing only that node$`, st.allowSetIsOnlyThatNode)
			sc.Step(`^routing for the model is restricted to that node$`, st.routingRestrictedToNode)
			// resolve (wrong)
			sc.Step(`^a private band exists$`, st.givenPrivateBand)
			sc.Step(`^a consumer presents a code that doesn't match any code_hash$`, st.presentWrongCode)
			sc.Step(`^no band resolves and no hidden node is reachable$`, st.nothingResolves)
			// market invisibility
			sc.Step(`^a node shares ONLY on a private band$`, st.givenPrivateBand)
			sc.Step(`^anyone GETs /discover or /market$`, st.anyoneGetsPublicFeeds)
			sc.Step(`^the band's node never appears \(privacy: code-holders only\)$`, st.nodeNeverAppears)
			// bandView / listing never leaks
			sc.Step(`^the band's metadata is fetched$`, st.metadataFetched)
			sc.Step(`^it returns the masked cosmetic display \+ non-secret fields only$`, st.maskedDisplayNonSecretOnly)
			sc.Step(`^never the frequency code or its hash in a usable form$`, st.noCodeOrHashInUsableForm)
			// global ceiling binds private (regression pin a)
			sc.Step(`^the global price ceiling would reject a high price$`, st.ceilingWouldReject)
			sc.Step(`^the operator shares with --private instead$`, st.shareWithPrivateInstead)
			sc.Step(`^the registration is still rejected by the ceiling \(--private is not a price-bypass\)$`, st.stillRejectedByCeiling)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/discovery/bands.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("private-bands behavior scenarios failed (see godog output above)")
	}
}
