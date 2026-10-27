package main

// categories_bdd_test.go makes features/categories/discovery_taxonomy.feature and
// features/categories/category_index.feature EXECUTABLE. It drives the REAL public handlers
// (discover / market / categories) over httptest - filters, cache, CORS and method checks
// included - against a routeBroker with real node registrations.
//
// "No recompute" is observed without a test seam: computeDiscover and computeMarket both take
// b.mu, and a read served from the cache never does. So the scenario holds b.mu while it
// issues the reads; a read that tried to recompute would block, and is counted.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type catCounts struct {
	Name      string   `json:"name"`
	Providers int      `json:"providers"`
	Curated   int      `json:"curated_providers"`
	Models    int      `json:"models"`
	MinOut    *float64 `json:"min_out,omitempty"`
	BestTPS   *float64 `json:"best_tps,omitempty"`
}

type catEntry struct {
	catCounts
	Subcategories []catCounts `json:"subcategories"`
}

type categoriesBody struct {
	Version       int        `json:"version"`
	Categories    []catEntry `json:"categories"`
	Uncategorized catCounts  `json:"uncategorized"`
}

type catState struct {
	b          *broker
	now        time.Time
	seq        float64
	code       int
	hdr        http.Header
	raw        []byte
	offers     []offerView
	rawOffers  []map[string]any
	market     []marketView
	rawMarket  []map[string]any
	cats       categoriesBody
	rawCats    map[string]any
	bandOffers []offerView
	holdLock   bool // issue reads with b.mu held (any recompute would block)
	coldFirst  bool // the next read is the cold one; every read after it holds b.mu
	recomputes int  // reads that blocked on b.mu (i.e. tried to recompute)
}

func (s *catState) reset() {
	*s = catState{now: time.Now()}
	s.b = routeBroker(s.now, map[string]protocol.NodeRegistration{})
	s.b.db = store.NewMem()
	s.b.probeSched = map[string]*probeState{}
	s.b.localCache = map[string]localCacheEntry{}
	s.b.probe = probeConfig{}
	if s.b.banned == nil {
		s.b.banned = map[string]bool{}
	}
}

func unescCat(v string) string {
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(v, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return v
}

func splitNonEmpty(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(unescCat(v), ",")
}

// put registers one public, live, probed station. normalize mirrors the register path; a raw
// put models an offer that reached the node map without registration.
func (s *catState) put(id string, o protocol.ModelOffer, normalize bool, tps float64) {
	s.seq++
	if o.PriceIn == 0 {
		o.PriceIn, o.PriceOut = s.seq, s.seq
	}
	if normalize {
		o.Normalize()
	}
	s.b.nodes[id] = protocol.NodeRegistration{NodeID: id, Offers: []protocol.ModelOffer{o}}
	s.b.lastSeen[id] = s.now
	s.b.trust[id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.tps[id] = tps
}

func (s *catState) stations(t *godog.Table) error {
	head := t.Rows[0].Cells
	for _, row := range t.Rows[1:] {
		v := map[string]string{}
		for i, c := range row.Cells {
			v[head[i].Value] = c.Value
		}
		o := protocol.ModelOffer{Model: v["model"], Modality: v["modality"], Taxonomy: protocol.Taxonomy{
			Category: v["category"], Subcategory: v["subcategory"], Tags: splitNonEmpty(v["tags"]),
			ContentPolicy: v["policy"], TaxonomySource: v["source"],
		}}
		if p, ok := v["price_out"]; ok {
			f, _ := strconv.ParseFloat(p, 64)
			o.PriceIn, o.PriceOut = f, f
		}
		tps := 100.0
		if t, ok := v["tps"]; ok {
			tps, _ = strconv.ParseFloat(t, 64)
		}
		s.put(v["node"], o, true, tps)
	}
	return nil
}

func (s *catState) rawStation(id, model, cat, sub, tags, policy string) error {
	s.put(id, protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{
		Category: cat, Subcategory: sub, Tags: splitNonEmpty(tags), ContentPolicy: policy,
	}}, false, 100)
	return nil
}

func (s *catState) privateStation(id, model, cat, sub string) error {
	s.put(id, protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Category: cat, Subcategory: sub}}, true, 100)
	s.b.private[id] = true
	return nil
}

func (s *catState) stationPolicy(id, model, policy string) error {
	s.put(id, protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{ContentPolicy: policy}}, true, 100)
	return nil
}
func (s *catState) stationCat(id, model, cat, sub string) error {
	s.put(id, protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Category: cat, Subcategory: sub}}, true, 100)
	return nil
}
func (s *catState) curatedCat(id, model, cat, sub string) error {
	s.put(id, protocol.ModelOffer{Model: model, Taxonomy: protocol.Taxonomy{Category: cat, Subcategory: sub}}, true, 100)
	n := s.b.nodes[id]
	n.Curated, n.CuratedProvider = true, "upstream"
	s.b.nodes[id] = n
	return nil
}
func (s *catState) manyTagStations(n int, model string) error {
	for i := 0; i < n; i++ {
		s.put(fmt.Sprintf("tag-%02d", i), protocol.ModelOffer{Model: model,
			Taxonomy: protocol.Taxonomy{Tags: []string{fmt.Sprintf("t%02d", i)}}}, true, 100)
	}
	return nil
}
func (s *catState) stale(id string) error {
	s.b.lastSeen[id] = s.now.Add(-2 * nodeTTL)
	return nil
}
func (s *catState) banned(id string) error { s.b.banned[id] = true; return nil }

// --- reads -----------------------------------------------------------------------------

func (s *catState) handlerFor(path string) http.HandlerFunc {
	switch {
	case strings.HasPrefix(path, "/discover"):
		return s.b.discover
	case strings.HasPrefix(path, "/market"):
		return s.b.market
	default:
		return s.b.categories
	}
}

func (s *catState) do(method, path string) {
	w := httptest.NewRecorder()
	h := s.handlerFor(path)
	r := httptest.NewRequest(method, path, nil)
	if !s.holdLock {
		h(w, r)
	} else {
		s.b.mu.Lock()
		done := make(chan struct{})
		go func() { h(w, r); close(done) }()
		select {
		case <-done:
			s.b.mu.Unlock()
		case <-time.After(300 * time.Millisecond):
			s.recomputes++
			s.b.mu.Unlock()
			<-done
		}
	}
	if s.coldFirst {
		s.coldFirst, s.holdLock = false, true
	}
	s.code, s.hdr, s.raw = w.Code, w.Header(), w.Body.Bytes()
	s.decode(path)
}

func (s *catState) decode(path string) {
	if s.code != http.StatusOK {
		return
	}
	switch {
	case strings.HasPrefix(path, "/discover"):
		var typed struct {
			Offers []offerView `json:"offers"`
		}
		var raw struct {
			Offers []map[string]any `json:"offers"`
		}
		_ = json.Unmarshal(s.raw, &typed)
		_ = json.Unmarshal(s.raw, &raw)
		s.offers, s.rawOffers = typed.Offers, raw.Offers
	case strings.HasPrefix(path, "/market"):
		var typed struct {
			Market []marketView `json:"market"`
		}
		var raw struct {
			Market []map[string]any `json:"market"`
		}
		_ = json.Unmarshal(s.raw, &typed)
		_ = json.Unmarshal(s.raw, &raw)
		s.market, s.rawMarket = typed.Market, raw.Market
	default:
		s.cats = categoriesBody{}
		_ = json.Unmarshal(s.raw, &s.cats)
		_ = json.Unmarshal(s.raw, &s.rawCats)
	}
}

func (s *catState) get(path string) error     { s.do(http.MethodGet, path); return nil }
func (s *catState) getQ(q string) error       { return s.get("/discover?" + q) }
func (s *catState) getMarketQ(q string) error { return s.get("/market?" + q) }
func (s *catState) getDiscover() error        { return s.get("/discover") }
func (s *catState) getMarket() error          { return s.get("/market") }
func (s *catState) getCategories() error      { return s.get("/categories") }
func (s *catState) postCategories() error     { s.do(http.MethodPost, "/categories"); return nil }
func (s *catState) getCategoriesN(n int) error {
	for i := 0; i < n; i++ {
		s.do(http.MethodGet, "/categories")
	}
	return nil
}

func randWord() string { return fmt.Sprintf("r%08x", rand.Uint32()) }

func (s *catState) randomTags(view string, n int) error {
	for i := 0; i < n; i++ {
		s.do(http.MethodGet, "/"+view+"?tag="+randWord())
	}
	return nil
}
func (s *catState) randomParam(view string, n int, param string) error {
	for i := 0; i < n; i++ {
		s.do(http.MethodGet, "/"+view+"?"+param+"="+randWord())
	}
	return nil
}

func (s *catState) warm() error {
	s.do(http.MethodGet, "/discover")
	s.do(http.MethodGet, "/market")
	s.holdLock = true
	return nil
}
func (s *catState) cold() error {
	s.b.localCache = map[string]localCacheEntry{}
	s.coldFirst = true
	return nil
}

func (s *catState) resolveBand() error {
	s.bandOffers, _ = s.b.bandOffers(store.Band{NodeID: "p1"}, true, time.Now())
	return nil
}

// --- assertions --------------------------------------------------------------------------

func (s *catState) offer(node string) (offerView, int, error) {
	for i, o := range s.offers {
		if o.NodeID == node {
			return o, i, nil
		}
	}
	return offerView{}, -1, fmt.Errorf("no /discover offer from %q (status %d)", node, s.code)
}

func cmpTax(what string, t protocol.Taxonomy, cat, sub, tags, policy, src string) error {
	got := fmt.Sprintf("%s|%s|%s|%s|%s", t.Category, t.Subcategory, strings.Join(t.Tags, ","), t.ContentPolicy, t.TaxonomySource)
	want := fmt.Sprintf("%s|%s|%s|%s|%s", cat, sub, tags, policy, src)
	if got != want {
		return fmt.Errorf("%s: taxonomy %q, got %q", what, want, got)
	}
	return nil
}

func (s *catState) offerHas(node, cat, sub, tags, policy, src string) error {
	o, _, err := s.offer(node)
	if err != nil {
		return err
	}
	return cmpTax("offer "+node, o.Taxonomy, cat, sub, tags, policy, src)
}
func (s *catState) offerCatSub(node, cat, sub string) error {
	o, _, err := s.offer(node)
	if err != nil {
		return err
	}
	if o.Category != cat || o.Subcategory != sub {
		return fmt.Errorf("offer %s: %s/%s, got %s/%s", node, cat, sub, o.Category, o.Subcategory)
	}
	return nil
}

func noKeys(what string, m map[string]any, keys string) error {
	for _, k := range strings.Split(keys, ",") {
		if _, ok := m[k]; ok {
			return fmt.Errorf("%s carries key %q", what, k)
		}
	}
	return nil
}

func (s *catState) offerNoKeys(node, keys string) error {
	_, i, err := s.offer(node)
	if err != nil {
		return err
	}
	return noKeys("offer "+node, s.rawOffers[i], keys)
}

func (s *catState) bandHas(node, cat, sub string) error {
	for _, o := range s.bandOffers {
		if o.NodeID == node {
			if o.Category != cat || o.Subcategory != sub {
				return fmt.Errorf("band offer %s: %s/%s, got %s/%s", node, cat, sub, o.Category, o.Subcategory)
			}
			return nil
		}
	}
	return fmt.Errorf("no band offer from %q", node)
}

func (s *catState) notListed(node string) error {
	if _, _, err := s.offer(node); err == nil {
		return fmt.Errorf("%q is listed on /discover", node)
	}
	return nil
}

func sortedCSV(v []string) string {
	sort.Strings(v)
	return strings.Join(v, ",")
}

func (s *catState) listed(want string) error {
	var got []string
	for _, o := range s.offers {
		got = append(got, o.NodeID)
	}
	w := splitNonEmpty(want)
	if sortedCSV(got) != sortedCSV(w) {
		return fmt.Errorf("listed nodes %q, got %q (status %d)", sortedCSV(w), sortedCSV(got), s.code)
	}
	return nil
}

func (s *catState) ascending() error {
	for i := 1; i < len(s.offers); i++ {
		if s.offers[i].In < s.offers[i-1].In {
			return fmt.Errorf("offers out of input-price order at %d", i)
		}
	}
	if len(s.offers) < 2 {
		return fmt.Errorf("need at least 2 offers to check order, got %d", len(s.offers))
	}
	return nil
}

func (s *catState) statusIs(code int) error {
	if s.code != code {
		return fmt.Errorf("status %d, got %d: %s", code, s.code, s.raw)
	}
	return nil
}

func (s *catState) row(model string) (marketView, int, error) {
	for i, m := range s.market {
		if m.Model == model {
			return m, i, nil
		}
	}
	return marketView{}, -1, fmt.Errorf("no /market row for %q", model)
}

func (s *catState) rowCat(model, cat, sub string, n int) error {
	m, _, err := s.row(model)
	if err != nil {
		return err
	}
	if m.Category != cat || m.Subcategory != sub || m.CategoryProviders != n {
		return fmt.Errorf("row %s: %s/%s x%d, got %s/%s x%d", model, cat, sub, n, m.Category, m.Subcategory, m.CategoryProviders)
	}
	return nil
}
func (s *catState) rowProviders(model string, n int) error {
	m, _, err := s.row(model)
	if err != nil {
		return err
	}
	if m.Providers != n {
		return fmt.Errorf("row %s: providers %d, got %d", model, n, m.Providers)
	}
	return nil
}
func (s *catState) rowTags(model, tags string) error {
	m, _, err := s.row(model)
	if err != nil {
		return err
	}
	if got := strings.Join(m.Tags, ","); got != tags {
		return fmt.Errorf("row %s: tags %q, got %q", model, tags, got)
	}
	return nil
}
func (s *catState) rowTagCount(model string, n int) error {
	m, _, err := s.row(model)
	if err != nil {
		return err
	}
	if len(m.Tags) != n {
		return fmt.Errorf("row %s: %d tags, got %d", model, n, len(m.Tags))
	}
	return nil
}
func (s *catState) rowPolicies(model, p string) error {
	m, _, err := s.row(model)
	if err != nil {
		return err
	}
	if got := strings.Join(m.ContentPolicies, ","); got != p {
		return fmt.Errorf("row %s: content policies %q, got %q", model, p, got)
	}
	return nil
}
func (s *catState) rowNoKeys(model, keys string) error {
	_, i, err := s.row(model)
	if err != nil {
		return err
	}
	return noKeys("market row "+model, s.rawMarket[i], keys)
}
func (s *catState) marketModels(want string) error {
	var got []string
	for _, m := range s.market {
		got = append(got, m.Model)
	}
	if sortedCSV(got) != sortedCSV(splitNonEmpty(want)) {
		return fmt.Errorf("market models %q, got %q (status %d)", want, sortedCSV(got), s.code)
	}
	return nil
}

func (s *catState) noRecompute() error {
	if s.recomputes != 0 {
		return fmt.Errorf("%d read(s) blocked on the market lock, i.e. recomputed the market", s.recomputes)
	}
	return nil
}
func (s *catState) discoverOnce() error {
	if err := s.noRecompute(); err != nil {
		return err
	}
	n := 0
	for k := range s.b.localCache {
		if strings.HasPrefix(k, "discover") {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("want one cached discover view, got %d", n)
	}
	return nil
}

// --- /categories -------------------------------------------------------------------------

func (s *catState) version(v int) error {
	if s.cats.Version != v {
		return fmt.Errorf("taxonomy version %d, got %d (status %d: %s)", v, s.cats.Version, s.code, s.raw)
	}
	return nil
}
func (s *catState) catNames(want string) error {
	var got []string
	for _, c := range s.cats.Categories {
		got = append(got, c.Name)
	}
	if g := strings.Join(got, ","); g != want {
		return fmt.Errorf("categories %q, got %q", want, g)
	}
	return nil
}
func (s *catState) cat(name string) (catEntry, int, error) {
	for i, c := range s.cats.Categories {
		if c.Name == name {
			return c, i, nil
		}
	}
	return catEntry{}, -1, fmt.Errorf("no category %q (status %d)", name, s.code)
}
func (s *catState) sub(path string) (catCounts, error) {
	cn, sn, _ := strings.Cut(path, "/")
	c, _, err := s.cat(cn)
	if err != nil {
		return catCounts{}, err
	}
	for _, x := range c.Subcategories {
		if x.Name == sn {
			return x, nil
		}
	}
	return catCounts{}, fmt.Errorf("no subcategory %q", path)
}
func (s *catState) subNames(cn, want string) error {
	c, _, err := s.cat(cn)
	if err != nil {
		return err
	}
	var got []string
	for _, x := range c.Subcategories {
		got = append(got, x.Name)
	}
	if g := strings.Join(got, ","); g != want {
		return fmt.Errorf("%s subcategories %q, got %q", cn, want, g)
	}
	return nil
}
func counts(what string, c catCounts, p, m int) error {
	if c.Providers != p || c.Models != m {
		return fmt.Errorf("%s: %d providers/%d models, got %d/%d", what, p, m, c.Providers, c.Models)
	}
	return nil
}
func (s *catState) catCounts(cn string, p, m int) error {
	c, _, err := s.cat(cn)
	if err != nil {
		return err
	}
	return counts("category "+cn, c.catCounts, p, m)
}
func (s *catState) subCounts(path string, p, m int) error {
	c, err := s.sub(path)
	if err != nil {
		return err
	}
	return counts("subcategory "+path, c, p, m)
}
func priceSpeed(what string, c catCounts, price, tps float64) error {
	if c.MinOut == nil || c.BestTPS == nil || *c.MinOut != price || *c.BestTPS != tps {
		return fmt.Errorf("%s: min_out %v best_tps %v, got %v %v", what, price, tps, c.MinOut, c.BestTPS)
	}
	return nil
}
func (s *catState) catPrice(cn string, price, tps float64) error {
	c, _, err := s.cat(cn)
	if err != nil {
		return err
	}
	return priceSpeed("category "+cn, c.catCounts, price, tps)
}
func (s *catState) subPrice(path string, price, tps float64) error {
	c, err := s.sub(path)
	if err != nil {
		return err
	}
	return priceSpeed("subcategory "+path, c, price, tps)
}
func (s *catState) catNoKeys(cn, keys string) error {
	_, i, err := s.cat(cn)
	if err != nil {
		return err
	}
	list, _ := s.rawCats["categories"].([]any)
	m, _ := list[i].(map[string]any)
	return noKeys("category "+cn, m, keys)
}
func (s *catState) catCurated(cn string, n int) error {
	c, _, err := s.cat(cn)
	if err != nil {
		return err
	}
	if c.Curated != n {
		return fmt.Errorf("category %s: %d curated providers, got %d", cn, n, c.Curated)
	}
	return nil
}
func (s *catState) subCurated(path string, n int) error {
	c, err := s.sub(path)
	if err != nil {
		return err
	}
	if c.Curated != n {
		return fmt.Errorf("subcategory %s: %d curated providers, got %d", path, n, c.Curated)
	}
	return nil
}
func (s *catState) uncategorized(p, m int) error {
	return counts("uncategorized", s.cats.Uncategorized, p, m)
}
func (s *catState) cors() error {
	if s.hdr.Get("Access-Control-Allow-Origin") != "*" {
		return fmt.Errorf("no cross-origin read header (status %d)", s.code)
	}
	return nil
}

func TestCategoriesBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &catState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a broker with these public stations on air:$`, st.stations)
			sc.Step(`^station `+q+` reached the node map without registration carrying model `+q+`, category `+q+`, subcategory `+q+`, tags `+q+` and content policy `+q+`$`, st.rawStation)
			sc.Step(`^a private band station `+q+` serving `+q+` with category `+q+` and subcategory `+q+`$`, st.privateStation)
			sc.Step(`^station `+q+` is on air for `+q+` with content policy `+q+`$`, st.stationPolicy)
			sc.Step(`^station `+q+` is on air for `+q+` with category `+q+` and subcategory `+q+`$`, st.stationCat)
			sc.Step(`^curated station `+q+` is on air for `+q+` with category `+q+` and subcategory `+q+`$`, st.curatedCat)
			sc.Step(`^category `+q+` has (\d+) curated providers$`, st.catCurated)
			sc.Step(`^subcategory `+q+` has (\d+) curated providers$`, st.subCurated)
			sc.Step(`^(\d+) stations on air for `+q+` each declaring a different tag$`, st.manyTagStations)
			sc.Step(`^station `+q+` has not heartbeat within nodeTTL$`, st.stale)
			sc.Step(`^station `+q+` is banned$`, st.banned)
			sc.Step(`^the public market cache is warm$`, st.warm)
			sc.Step(`^the public market cache is cold$`, st.cold)

			sc.Step(`^a consumer GETs /discover$`, st.getDiscover)
			sc.Step(`^a consumer GETs /discover\?(.+)$`, st.getQ)
			sc.Step(`^a consumer GETs /market$`, st.getMarket)
			sc.Step(`^a consumer GETs /market\?(.+)$`, st.getMarketQ)
			sc.Step(`^a consumer GETs /categories$`, st.getCategories)
			sc.Step(`^a consumer GETs /categories (\d+) times$`, st.getCategoriesN)
			sc.Step(`^a consumer POSTs /categories$`, st.postCategories)
			sc.Step(`^a consumer GETs /(discover|market) with (\d+) different random tag filters$`, st.randomTags)
			sc.Step(`^a consumer GETs /(discover|market) with (\d+) different random `+q+` values$`, st.randomParam)
			sc.Step(`^the band is resolved by its frequency code$`, st.resolveBand)

			sc.Step(`^the offer from `+q+` has category `+q+`, subcategory `+q+`, tags `+q+`, content policy `+q+` and source `+q+`$`, st.offerHas)
			sc.Step(`^the offer from `+q+` has category `+q+` and subcategory `+q+`$`, st.offerCatSub)
			sc.Step(`^the JSON offer from `+q+` has none of the keys `+q+`$`, st.offerNoKeys)
			sc.Step(`^the band offer from `+q+` has category `+q+` and subcategory `+q+`$`, st.bandHas)
			sc.Step(`^`+q+` is not listed$`, st.notListed)
			sc.Step(`^the listed nodes are `+q+`$`, st.listed)
			sc.Step(`^the listed offers are in ascending input-price order$`, st.ascending)
			sc.Step(`^the response status is (\d+)$`, st.statusIs)
			sc.Step(`^the market row for `+q+` has category `+q+`, subcategory `+q+` and category providers (\d+)$`, st.rowCat)
			sc.Step(`^the market row for `+q+` has providers (\d+)$`, st.rowProviders)
			sc.Step(`^the market row for `+q+` has tags `+q+`$`, st.rowTags)
			sc.Step(`^the market row for `+q+` has (\d+) tags$`, st.rowTagCount)
			sc.Step(`^the market row for `+q+` has content policies `+q+`$`, st.rowPolicies)
			sc.Step(`^the JSON market row for `+q+` has none of the keys `+q+`$`, st.rowNoKeys)
			sc.Step(`^the market models are `+q+`$`, st.marketModels)
			sc.Step(`^the full-market recompute ran 0 more times$`, st.noRecompute)
			sc.Step(`^the discover recompute ran exactly once$`, st.discoverOnce)

			sc.Step(`^the taxonomy version is (\d+)$`, st.version)
			sc.Step(`^the categories are `+q+`$`, st.catNames)
			sc.Step(`^category `+q+` lists subcategories `+q+`$`, st.subNames)
			sc.Step(`^category `+q+` has (\d+) providers and (\d+) models$`, st.catCounts)
			sc.Step(`^subcategory `+q+` has (\d+) providers and (\d+) models$`, st.subCounts)
			sc.Step(`^category `+q+` has min output price ([\d.]+) and best tps ([\d.]+)$`, st.catPrice)
			sc.Step(`^subcategory `+q+` has min output price ([\d.]+) and best tps ([\d.]+)$`, st.subPrice)
			sc.Step(`^the JSON for category `+q+` has none of the keys `+q+`$`, st.catNoKeys)
			sc.Step(`^the uncategorized count is (\d+) providers and (\d+) models$`, st.uncategorized)
			sc.Step(`^the response allows cross-origin reads$`, st.cors)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths: []string{
				"../../features/categories/discovery_taxonomy.feature",
				"../../features/categories/category_index.feature",
			},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories discovery/index behavior scenarios failed (see godog output above)")
	}
}
