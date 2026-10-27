package main

// broker_inference_bdd_test.go makes features/categories/broker_inference.feature EXECUTABLE:
// the REAL public handlers (discover, market, categories) over a routeBroker holding stations
// as an older node registers them (no taxonomy), and the stored registration read back.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
)

type brokerInferState struct {
	b      *broker
	offers []offerView
	market []marketView
	cats   map[string]any
	model  string
}

func (s *brokerInferState) reset() {
	*s = brokerInferState{}
	s.b = routeBroker(time.Now(), map[string]protocol.NodeRegistration{})
	s.b.db = store.NewMem()
	s.b.probeSched = map[string]*probeState{}
	s.b.localCache = map[string]localCacheEntry{}
	s.b.probe = probeConfig{}
}

func (s *brokerInferState) station(o protocol.ModelOffer, curated bool) {
	o.Normalize() // as register normalizes
	s.model = o.Model
	reg := protocol.NodeRegistration{NodeID: "st1", Offers: []protocol.ModelOffer{o}, Curated: curated}
	if curated {
		reg.CuratedProvider = "upstream"
	}
	s.b.nodes["st1"] = reg
	s.b.lastSeen["st1"] = time.Now()
	s.b.trust["st1"] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.tps["st1"] = 50
}

func (s *brokerInferState) bare(model, modality string) error {
	s.station(protocol.ModelOffer{Model: model, Modality: modality}, false)
	return nil
}
func (s *brokerInferState) withCaps(model, caps string) error {
	s.station(protocol.ModelOffer{Model: model, Capabilities: strings.Split(caps, ",")}, false)
	return nil
}
func (s *brokerInferState) declared(model, pair string) error {
	cat, sub := protocol.SplitCategory(pair)
	s.station(protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Category: cat, Subcategory: sub,
		TaxonomySource: protocol.SourceDeclared}}, false)
	return nil
}
func (s *brokerInferState) nodeInferred(model, pair string) error {
	cat, sub := protocol.SplitCategory(pair)
	s.station(protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Category: cat, Subcategory: sub,
		TaxonomySource: protocol.SourceInferred}}, false)
	return nil
}
func (s *brokerInferState) tagsOnly(model, tags string) error {
	s.station(protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Tags: strings.Split(tags, ","),
		TaxonomySource: protocol.SourceDeclared}}, false)
	return nil
}
func (s *brokerInferState) curatedBare(model string) error {
	s.station(protocol.ModelOffer{Model: model}, true)
	return nil
}
func (s *brokerInferState) curatedDeclared(model, pair string) error {
	cat, sub := protocol.SplitCategory(pair)
	s.station(protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Category: cat, Subcategory: sub,
		TaxonomySource: protocol.SourceDeclared}}, true)
	return nil
}

func (s *brokerInferState) get(path string, h http.HandlerFunc, v any) error {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		return fmt.Errorf("GET %s = %d", path, w.Code)
	}
	return json.Unmarshal(w.Body.Bytes(), v)
}
func (s *brokerInferState) discover() error {
	var v struct {
		Offers []offerView `json:"offers"`
	}
	err := s.get("/discover", s.b.discover, &v)
	s.offers = v.Offers
	return err
}
func (s *brokerInferState) allViews() error {
	var m struct {
		Market []marketView `json:"market"`
	}
	if err := s.get("/market", s.b.market, &m); err != nil {
		return err
	}
	s.market = m.Market
	if err := s.get("/categories", s.b.categories, &s.cats); err != nil {
		return err
	}
	var d struct {
		Offers []offerView `json:"offers"`
	}
	err := s.get("/discover?category=retrieval", s.b.discover, &d)
	s.offers = d.Offers
	return err
}

func (s *brokerInferState) offer() (offerView, error) {
	for _, o := range s.offers {
		if o.Model == s.model {
			return o, nil
		}
	}
	return offerView{}, fmt.Errorf("no offer for %q", s.model)
}
func (s *brokerInferState) has(cat, sub, src string) error {
	o, err := s.offer()
	if err != nil {
		return err
	}
	if o.Category != cat || o.Subcategory != sub || o.TaxonomySource != src {
		return fmt.Errorf("offer %s: %s/%s (%s), got %s/%s (%s)", s.model, cat, sub, src, o.Category, o.Subcategory, o.TaxonomySource)
	}
	return nil
}
func (s *brokerInferState) policy(p string) error {
	o, err := s.offer()
	if err != nil {
		return err
	}
	if o.ContentPolicy != p {
		return fmt.Errorf("content policy %q, got %q", p, o.ContentPolicy)
	}
	return nil
}
func (s *brokerInferState) storedBare() error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if t := s.b.nodes["st1"].Offers[0].Taxonomy; t.Declared() || t.TaxonomySource != "" {
		// A voice offer's normalized audio category is the broker's own derivation; the
		// scenario's model is a chat model, so any category here was written back.
		return fmt.Errorf("the stored registration carries a taxonomy: %+v", t)
	}
	return nil
}
func (s *brokerInferState) marketCat(model, cat string) error {
	for _, m := range s.market {
		if m.Model == model {
			if m.Category != cat {
				return fmt.Errorf("market row %s category %q, got %q", model, cat, m.Category)
			}
			return nil
		}
	}
	return fmt.Errorf("no market row %s", model)
}
func (s *brokerInferState) catProviders(cat string, n int) error {
	for _, c := range s.cats["categories"].([]any) {
		m := c.(map[string]any)
		if m["name"] == cat {
			if int(m["providers"].(float64)) != n {
				return fmt.Errorf("category %s providers %v, want %d", cat, m["providers"], n)
			}
			return nil
		}
	}
	return fmt.Errorf("no category %s", cat)
}
func (s *brokerInferState) filtered(want string) error {
	var got []string
	for _, o := range s.offers {
		got = append(got, o.Model)
	}
	if strings.Join(got, ",") != want {
		return fmt.Errorf("filtered offers %q, got %q", want, got)
	}
	return nil
}

func TestBrokerInferenceBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &brokerInferState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a public station on air serving `+q+` with modality `+q+` and no taxonomy$`, st.bare)
			sc.Step(`^a public station on air serving `+q+` with capabilities `+q+` and no taxonomy$`, st.withCaps)
			sc.Step(`^a public station on air serving `+q+` that declared `+q+`$`, st.declared)
			sc.Step(`^a public station on air serving `+q+` whose node inferred `+q+`$`, st.nodeInferred)
			sc.Step(`^a public station on air serving `+q+` that declared only the tags `+q+`$`, st.tagsOnly)
			sc.Step(`^a curated station on air serving `+q+` with no taxonomy$`, st.curatedBare)
			sc.Step(`^a curated station on air serving `+q+` that declared `+q+`$`, st.curatedDeclared)
			sc.Step(`^a consumer GETs /discover$`, st.discover)
			sc.Step(`^a consumer GETs /market and /categories and /discover\?category=retrieval$`, st.allViews)
			sc.Step(`^its offer has category `+q+`, subcategory `+q+` and source `+q+`$`, st.has)
			sc.Step(`^its offer has content policy `+q+`$`, st.policy)
			sc.Step(`^the station's stored registration still carries no taxonomy$`, st.storedBare)
			sc.Step(`^the market row for `+q+` has category `+q+`$`, st.marketCat)
			sc.Step(`^category `+q+` has (\d+) providers$`, st.catProviders)
			sc.Step(`^the filtered offers are `+q+`$`, st.filtered)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/broker_inference.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/broker_inference behavior scenarios failed (see godog output above)")
	}
}
