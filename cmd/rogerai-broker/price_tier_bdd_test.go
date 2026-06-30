package main

// price_tier_bdd_test.go makes features/pricing/price_tier.feature EXECUTABLE, driving the
// REAL classifier (pricetier.go priceTier / renderPriceTier / assignPriceTiers) and the REAL
// external-reference seam (refprices.go refOut / refPrices / refPriceSeed / openRouterFetch),
// plus the single-source-of-truth integration: the SAME offer carries the SAME tier on the
// public /discover feed and as a private band resolved by frequency code. No mocks.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/pricetier"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type ptState struct {
	t *testing.T

	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string

	model   string // the model currently under discussion
	peers   map[string][]float64
	offered map[string]float64 // a price offered for a model (isolation scenarios)

	lastTier  int
	lastPrice float64

	privCode string // one-time band code from the integration private register
}

func (s *ptState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.b.refPrices = map[string]float64{}
	s.model = ""
	s.peers = map[string][]float64{}
	s.offered = map[string]float64{}
	s.lastTier, s.lastPrice = 0, 0
	s.privCode = ""
}

func ptF(s string) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return v }
func ptI(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func ptPrices(list string) []float64 {
	var out []float64
	for _, p := range strings.Split(list, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, ptF(t))
		}
	}
	return out
}

// tierFor classifies price for model exactly as the broker does: external ref (refOut)
// preferred, else the live online median over the model's peers.
func (s *ptState) tierFor(model string, price float64) int {
	ref, _ := s.b.refOut(model)
	return priceTier(price, ref, s.peers[model])
}

func (s *ptState) setRef(model, r string) error {
	s.model = model
	s.b.refMu.Lock()
	s.b.refPrices[normalizeModelName(model)] = ptF(r)
	s.b.refMu.Unlock()
	return nil
}

// --- Background -------------------------------------------------------------

func (s *ptState) liveMarket() error { return nil } // the broker is built in reset()

// --- external reference -----------------------------------------------------

func (s *ptState) externalRef(model, r string) error { return s.setRef(model, r) }

func (s *ptState) bandPriced(model, price string) error {
	s.model, s.lastPrice = model, ptF(price)
	s.lastTier = s.tierFor(model, s.lastPrice)
	return nil
}

func (s *ptState) hasTier(tier string) error {
	if s.lastTier != ptI(tier) {
		return fmt.Errorf("price_tier = %d, want %s (price %.4f, model %q)", s.lastTier, tier, s.lastPrice, s.model)
	}
	return nil
}

func (s *ptState) rendersGoodPriceChip() error {
	_, chip := pricetier.Render(s.lastTier, s.lastPrice)
	if chip != "good price" {
		return fmt.Errorf("tier %d rendered chip %q, want \"good price\"", s.lastTier, chip)
	}
	return nil
}

func (s *ptState) noNegativeWording() error {
	bars, chip := pricetier.Render(s.lastTier, s.lastPrice)
	for _, bad := range []string{"expensive", "overpriced", "rip-off", "pricey", "costly"} {
		if strings.Contains(strings.ToLower(bars+" "+chip), bad) {
			return fmt.Errorf("rendered band carries negative wording %q: bars=%q chip=%q", bad, bars, chip)
		}
	}
	// $$..$$$$ must be neutral: bars are only "$" glyphs, the chip is empty for tier>1.
	if s.lastTier > 1 && chip != "" {
		return fmt.Errorf("tier %d editorialized with chip %q (only tier 1 is)", s.lastTier, chip)
	}
	return nil
}

func (s *ptState) onlineBands(model, list string) error {
	s.model = model
	s.peers[model] = ptPrices(list)
	return nil
}

func (s *ptState) onlineBandsN(model, _n, list string) error { return s.onlineBands(model, list) }

func (s *ptState) bandPricedHasTier(price, tier string) error {
	got := s.tierFor(s.model, ptF(price))
	s.lastTier, s.lastPrice = got, ptF(price)
	if got != ptI(tier) {
		return fmt.Errorf("band priced %s (model %q) -> tier %d, want %s", price, s.model, got, tier)
	}
	return nil
}

func (s *ptState) lastKnownRef(model, r string) error { return s.setRef(model, r) }

func (s *ptState) latestSyncFailed() error {
	before := map[string]float64{}
	s.b.refMu.RLock()
	for k, v := range s.b.refPrices {
		before[k] = v
	}
	s.b.refMu.RUnlock()
	// Drive the REAL best-effort failure path: a failing fetch leaves the map untouched.
	orig := openRouterFetch
	openRouterFetch = func(context.Context) ([]byte, error) { return nil, fmt.Errorf("network down") }
	defer func() { openRouterFetch = orig }()
	if n := s.b.syncRefPricesOnce(context.Background()); n != 0 {
		return fmt.Errorf("a failed sync merged %d entries, want 0 (last-known must survive)", n)
	}
	s.b.refMu.RLock()
	defer s.b.refMu.RUnlock()
	for k, v := range before {
		if s.b.refPrices[k] != v {
			return fmt.Errorf("last-known ref for %q changed after a failed sync: %v -> %v", k, v, s.b.refPrices[k])
		}
	}
	return nil
}

func (s *ptState) noSyncYet() error { return nil } // refPrices stays empty -> the seed is used

func (s *ptState) seedRef(model, r string) error {
	got, ok := refPriceSeed[normalizeModelName(model)]
	if !ok || got != ptF(r) {
		return fmt.Errorf("seed reference for %q = (%v, %v), want %s", model, got, ok, r)
	}
	return nil
}

// --- internal median --------------------------------------------------------

func (s *ptState) noExternalRef(model string) error {
	s.model = model
	if v, ok := s.b.refOut(model); ok {
		return fmt.Errorf("model %q unexpectedly has an external ref %.4f", model, v)
	}
	return nil
}

func (s *ptState) onlineMedian(model, med string) error {
	s.model = model
	m := ptF(med)
	s.peers[model] = []float64{m, m, m} // >= minMarketDepth peers whose median is m
	return nil
}

func (s *ptState) rawPriceNoTierNoChip(price string) error {
	bars, chip := pricetier.Render(0, ptF(price))
	if bars != "" || chip != "" {
		return fmt.Errorf("tier-0 priced band rendered bars=%q chip=%q, want empty (raw price only)", bars, chip)
	}
	return nil
}

func (s *ptState) onlineBandN1(model, _n, price string) error {
	s.model = model
	s.peers[model] = ptPrices(price)
	return nil
}

func (s *ptState) offlineBands(model, _n, _list string) error {
	s.model = model // offline bands do NOT count toward the median -> not added to peers
	return nil
}

func (s *ptState) onlineBandPricedHasTier(price, tier string) error {
	return s.bandPricedHasTier(price, tier)
}

// --- FREE -------------------------------------------------------------------

func (s *ptState) bandFree(model, price string) error { return s.bandPriced(model, price) }

func (s *ptState) freeBandTier(tier string) error { return s.hasTier(tier) }

func (s *ptState) freeBandRendersFREE() error {
	bars, _ := pricetier.Render(s.lastTier, s.lastPrice)
	if bars != "FREE" {
		return fmt.Errorf("free band rendered %q, want the FREE badge", bars)
	}
	return nil
}

func (s *ptState) bandActiveFreeWindow(model, price string) error { return s.bandPriced(model, price) }

func (s *ptState) tierAndRendersFREE(tier string) error {
	if err := s.hasTier(tier); err != nil {
		return err
	}
	return s.freeBandRendersFREE()
}

// --- per-model isolation ----------------------------------------------------

func (s *ptState) offeredFor(price, model string) error {
	s.offered[model] = ptF(price)
	return nil
}

func (s *ptState) modelBandTier(model, tier string) error {
	got := s.tierFor(model, s.offered[model])
	if got != ptI(tier) {
		return fmt.Errorf("%q band (priced %.4f) -> tier %d, want %s", model, s.offered[model], got, tier)
	}
	return nil
}

// --- caps -------------------------------------------------------------------

func (s *ptState) bandAtCap(model, price string) error { return s.bandPriced(model, price) }

func (s *ptState) thatBandTier(tier string) error { return s.hasTier(tier) }

// --- single source of truth (integration) -----------------------------------

func (s *ptState) publicBand(model, price string) error {
	s.model = model
	code, msg := registerWith(s.t, s.b, "pub1", s.nodePriv, s.nodePubHex, s.userPriv, true,
		protocol.ModelOffer{Model: model, Ctx: 4096, PriceOut: ptF(price)}, false, false)
	if code != http.StatusOK {
		return fmt.Errorf("public priced register = %d; msg=%q", code, msg)
	}
	return nil
}

func (s *ptState) privateBand(model, price string) error {
	s.model = model
	reg := protocol.NodeRegistration{
		NodeID: "priv1", PubKey: s.nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: model, Ctx: 4096, PriceOut: ptF(price)}}, Private: true,
	}
	reg.SignRegistration(s.nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("private priced register = %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if s.privCode, _ = resp["band_code"].(string); s.privCode == "" {
		return fmt.Errorf("private register returned no band_code")
	}
	return nil
}

func (s *ptState) publicTierOnDiscover(tier string) error {
	blob, _ := json.Marshal(s.b.computeDiscover())
	var d struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal(blob, &d)
	for _, o := range d.Offers {
		if o.NodeID == "pub1" && o.Model == s.model {
			if o.PriceTier != ptI(tier) {
				return fmt.Errorf("/discover price_tier = %d, want %s", o.PriceTier, tier)
			}
			return nil
		}
	}
	return fmt.Errorf("public band for %q not found on /discover", s.model)
}

func (s *ptState) privateTierOnResolve(tier string) error {
	body, _ := json.Marshal(map[string]string{"freq": s.privCode})
	w := httptest.NewRecorder()
	s.b.bandResolve(w, httptest.NewRequest(http.MethodPost, "/bands/resolve", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		return fmt.Errorf("band resolve = %d; body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	for _, o := range out.Offers {
		if o.Model == s.model {
			if o.PriceTier != ptI(tier) {
				return fmt.Errorf("private-band resolve price_tier = %d, want %s", o.PriceTier, tier)
			}
			return nil
		}
	}
	return fmt.Errorf("private band offer for %q not found on resolve", s.model)
}

func TestPriceTierBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &ptState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a broker with a live market$`, st.liveMarket)
			// external reference
			sc.Step(`^model "([^"]*)" has an external reference out-price of (\S+) \$/1M$`, st.externalRef)
			sc.Step(`^a band for "([^"]*)" is priced (\S+) \$/1M out$`, st.bandPriced)
			sc.Step(`^it has price_tier (\d+)$`, st.hasTier)
			sc.Step(`^it renders a "good price" chip$`, st.rendersGoodPriceChip)
			sc.Step(`^the rendered band carries no "expensive" or otherwise negative wording$`, st.noNegativeWording)
			sc.Step(`^model "([^"]*)" has online bands priced (.+) \$/1M out$`, st.onlineBands)
			sc.Step(`^the band priced (\S+) has price_tier (\d+)$`, st.bandPricedHasTier)
			sc.Step(`^model "([^"]*)" has a last-known external reference out-price of (\S+) \$/1M$`, st.lastKnownRef)
			sc.Step(`^the latest external price sync failed$`, st.latestSyncFailed)
			sc.Step(`^no external sync has run yet$`, st.noSyncYet)
			sc.Step(`^the seed reference out-price for "([^"]*)" is (\S+) \$/1M$`, st.seedRef)
			// internal median
			sc.Step(`^model "([^"]*)" has no external reference price$`, st.noExternalRef)
			sc.Step(`^model "([^"]*)" has an online median of (\S+) \$/1M out$`, st.onlineMedian)
			sc.Step(`^the band priced (\S+) renders its raw price with no \$ tier and no chip$`, st.rawPriceNoTierNoChip)
			sc.Step(`^model "([^"]*)" has (\d+) online band priced (\S+) \$/1M out$`, st.onlineBandN1)
			sc.Step(`^model "([^"]*)" has (\d+) online bands priced (.+) \$/1M out$`, st.onlineBandsN)
			sc.Step(`^model "([^"]*)" has (\d+) offline bands priced (.+) \$/1M out$`, st.offlineBands)
			sc.Step(`^the online band priced (\S+) has price_tier (\d+)$`, st.onlineBandPricedHasTier)
			// FREE
			sc.Step(`^a band for "([^"]*)" priced (\S+) \$/1M out \(free\)$`, st.bandFree)
			sc.Step(`^the free band has price_tier (\d+)$`, st.freeBandTier)
			sc.Step(`^the free band renders the FREE badge, not a \$ tier$`, st.freeBandRendersFREE)
			sc.Step(`^a band for "([^"]*)" whose active price right now is (\S+) \(a free window\)$`, st.bandActiveFreeWindow)
			sc.Step(`^that band has price_tier (\d+) and renders FREE$`, st.tierAndRendersFREE)
			// per-model isolation
			sc.Step(`^a band priced (\S+) \$/1M out is offered for "([^"]*)"$`, st.offeredFor)
			sc.Step(`^the "([^"]*)" band has price_tier (\d+)$`, st.modelBandTier)
			// caps
			sc.Step(`^a band for "([^"]*)" priced (\S+) \$/1M out \(the cap\)$`, st.bandAtCap)
			sc.Step(`^that band has price_tier (\d+)$`, st.thatBandTier)
			sc.Step(`^the rendered band carries no negative wording$`, st.noNegativeWording)
			// single source of truth
			sc.Step(`^a public band for "([^"]*)" priced (\S+) \$/1M out$`, st.publicBand)
			sc.Step(`^a private band \(frequency-code only\) for "([^"]*)" priced (\S+) \$/1M out$`, st.privateBand)
			sc.Step(`^the public band has price_tier (\d+) on /discover$`, st.publicTierOnDiscover)
			sc.Step(`^the private band has price_tier (\d+) when resolved by frequency code$`, st.privateTierOnResolve)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/pricing/price_tier.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("price-tier behavior scenarios failed (see godog output above)")
	}
}
