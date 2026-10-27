package main

// The model taxonomy on the public market (MODEL-CATEGORIES-DESIGN-2026-10-05):
// features/categories/discovery_taxonomy.feature and category_index.feature.
//
// Filters are a pass over the ONE cached view per endpoint (cachedJSON), never part of a
// cache key: a filter value that reached the key would let anyone force a full-market
// recompute per request by varying it.

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"

	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/protocol"
)

// taxonomyFilter is a parsed ?category=&subcategory=&tag=&policy=&source= query. Each value is
// canonicalized the way an offer's own value is, so "Vision" matches "vision". An unknown or
// invalid value matches NOTHING rather than being ignored: a typo must not silently widen a
// filtered view into the whole market. Empty values are no filter.
type taxonomyFilter struct {
	active   bool
	never    bool // some value can match no offer
	badSrc   bool // ?source= value matches no offer (a /discover-only filter)
	category string
	sub      string
	tags     []string        // all must be present
	policies map[string]bool // any may match; "" = no declared policy (?policy=none)
	source   string
}

func parseTaxonomyFilter(r *http.Request) taxonomyFilter {
	q := r.URL.Query()
	var f taxonomyFilter
	val := func(k string) string { return strings.ToLower(strings.TrimSpace(q.Get(k))) }
	if f.category = val("category"); f.category != "" {
		f.active = true
		f.never = f.never || !protocol.KnownCategory(f.category)
	}
	if f.sub = val("subcategory"); f.sub != "" {
		f.active = true
		f.never = f.never || !protocol.KnownSubcategory(f.category, f.sub)
	}
	for _, raw := range q["tag"] {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		f.active = true
		tag, ok := protocol.CanonicalTag(raw)
		f.never = f.never || !ok
		if ok && !slices.Contains(f.tags, tag) {
			f.tags = append(f.tags, tag)
		}
		// No station carries more than MaxTags, so requiring more matches nothing - and
		// stopping here bounds the work a long repeated query can ask for.
		if len(f.tags) > protocol.MaxTags {
			f.never = true
			break
		}
	}
	for _, raw := range q["policy"] {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		f.active = true
		if f.policies == nil {
			f.policies = map[string]bool{}
		}
		if p == "none" {
			f.policies[""] = true
		} else if g := protocol.CanonicalContentPolicy(p); g != "" {
			f.policies[g] = true
		} else {
			f.never = true
		}
	}
	if f.source = val("source"); f.source != "" {
		f.active = true
		f.badSrc = f.source != protocol.SourceDeclared && f.source != protocol.SourceInferred
	}
	return f
}

// match reports whether one station's (canonical) taxonomy passes the filter.
func (f taxonomyFilter) match(t protocol.Taxonomy) bool {
	return !f.badSrc && f.matchRow(t.Category, t.Subcategory, t.Tags, []string{t.ContentPolicy}) &&
		(f.source == "" || f.source == t.TaxonomySource)
}

// matchRow is match for an aggregated /market row, which carries every declared grade. A row
// is routed by model id, so ?policy= must hold for EVERY grade its stations declared (founder
// ruling 2026-10-05): otherwise "aligned" could route to an uncensored station of the model.
// /market rows carry no claim source, so ?source= does not apply to them.
func (f taxonomyFilter) matchRow(cat, sub string, tags, policies []string) bool {
	if f.never {
		return false
	}
	if (f.category != "" && f.category != cat) || (f.sub != "" && f.sub != sub) {
		return false
	}
	for _, t := range f.tags {
		if !slices.Contains(tags, t) {
			return false
		}
	}
	if f.policies == nil {
		return true
	}
	if len(policies) == 0 {
		policies = []string{""}
	}
	for _, p := range policies {
		if !f.policies[p] {
			return false
		}
	}
	return true
}

// serveFiltered serves the cached public view for key, filtered by the request's taxonomy
// query. An unfiltered read writes the cached bytes untouched.
func (b *broker) serveFiltered(w http.ResponseWriter, r *http.Request, key string, compute func() any) {
	f := parseTaxonomyFilter(r)
	if !f.active {
		b.serveCachedJSON(w, key, publicMarketTTL, compute)
		return
	}
	body := b.cachedJSON(key, publicMarketTTL, compute)
	if key == marketCacheKey {
		rows := b.decodedView(body).market
		out := make([]marketView, 0, len(rows))
		for _, m := range rows {
			if f.matchRow(m.Category, m.Subcategory, m.Tags, m.ContentPolicies) {
				out = append(out, m)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"market": out})
		return
	}
	offers := b.cachedOffers(body)
	out := make([]offerView, 0, len(offers))
	for _, o := range offers {
		if f.match(o.Taxonomy) {
			out = append(out, o)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// cachedOffers is the cached /discover view, decoded.
func (b *broker) cachedOffers(body []byte) []offerView {
	if body == nil {
		body = b.cachedJSON(discoverCacheKey, publicMarketTTL, b.computeDiscover)
	}
	return b.decodedView(body).offers
}

// decodedPublicView is one cached public view, decoded once. Filtered reads arrive far more
// often than the view changes (once per publicMarketTTL), so they share the decode instead
// of each paying for the whole market - the cost /discover's no-rate-limit posture assumes.
type decodedPublicView struct {
	offers []offerView
	market []marketView
}

// decodedView decodes body, reusing the last decode while the cached bytes are unchanged.
// Callers only read the result, never modify it.
func (b *broker) decodedView(body []byte) decodedPublicView {
	b.decodedMu.Lock()
	defer b.decodedMu.Unlock()
	if v, ok := b.decoded[string(body)]; ok {
		return v
	}
	var v struct {
		Offers []offerView  `json:"offers"`
		Market []marketView `json:"market"`
	}
	_ = json.Unmarshal(body, &v)
	d := decodedPublicView{offers: v.Offers, market: v.Market}
	// Two views (discover, market), each replaced once per window: keep only the latest few.
	if len(b.decoded) >= 4 {
		b.decoded = nil
	}
	if b.decoded == nil {
		b.decoded = map[string]decodedPublicView{}
	}
	b.decoded[string(body)] = d
	return d
}

// publishedTaxonomy is the taxonomy an offer is published with: its own, canonicalized at
// emission; or, when it carries no claim at all (a station on a roger release older than the
// taxonomy), the broker's own guess by the node's rule (detect.InferTaxonomy), labelled
// "inferred". A view only - never stored or mirrored as the station's claim
// (features/categories/broker_inference.feature). Curated proxies are never guessed: their
// ids name a vendor's product, which the id hints misread (founder ruling 2026-10-27).
func publishedTaxonomy(o protocol.ModelOffer, curated bool) protocol.Taxonomy {
	modality := offerModality(o.Modality)
	t := o.Taxonomy.Canonical(modality)
	if t.TaxonomySource != "" || curated {
		return t
	}
	return detect.InferTaxonomy(o.Model, modality, protocol.CanonicalCapabilities(o.Capabilities))
}

// taxonomyVotes accumulates what one model's online stations say it is for (/market).
// Category votes come from HUMAN stations ("providers" is the market's human-supply claim);
// curated proxies only label a model no human station has categorized, and never count toward
// category_providers (founder ruling 2026-10-05). Tags and content-policy grades come from
// every station: a curated station's grade is as real for routing as anyone's.
type taxonomyVotes struct {
	pairs, curatedPairs map[[2]string]int
	tags                map[string]int
	policies            map[string]bool
}

func (v *taxonomyVotes) add(t protocol.Taxonomy, curated bool) {
	if v.pairs == nil {
		v.pairs, v.curatedPairs = map[[2]string]int{}, map[[2]string]int{}
		v.tags, v.policies = map[string]int{}, map[string]bool{}
	}
	if t.Category != "" {
		if curated {
			v.curatedPairs[[2]string{t.Category, t.Subcategory}]++
		} else {
			v.pairs[[2]string{t.Category, t.Subcategory}]++
		}
	}
	for _, tag := range t.Tags {
		v.tags[tag]++
	}
	if t.ContentPolicy != "" {
		v.policies[t.ContentPolicy] = true
	}
}

// into sets the row's taxonomy: the pair MOST stations declare (ties alphabetical) and how
// many back it, the tag union most-shared first (capped like an offer's), and every grade.
func (v *taxonomyVotes) into(m *marketView) {
	pairs := v.pairs
	if len(pairs) == 0 {
		pairs = v.curatedPairs
	}
	best := 0
	for p, n := range pairs {
		if n > best || (n == best && p[0]+"/"+p[1] < m.Category+"/"+m.Subcategory) {
			m.Category, m.Subcategory, best = p[0], p[1], n
		}
	}
	if len(v.pairs) > 0 {
		m.CategoryProviders = best
	}
	for t := range v.tags {
		m.Tags = append(m.Tags, t)
	}
	sort.Slice(m.Tags, func(i, j int) bool {
		a, b := m.Tags[i], m.Tags[j]
		return v.tags[a] > v.tags[b] || (v.tags[a] == v.tags[b] && a < b)
	})
	if len(m.Tags) > protocol.MaxTags {
		m.Tags = m.Tags[:protocol.MaxTags]
	}
	for p := range v.policies {
		m.ContentPolicies = append(m.ContentPolicies, p)
	}
	sort.Strings(m.ContentPolicies)
}

// supply is one category's (or subcategory's) live supply on /categories.
type supply struct {
	Name      string   `json:"name,omitempty"`
	Providers int      `json:"providers"`                   // online HUMAN stations
	Curated   int      `json:"curated_providers,omitempty"` // online curated proxies, counted apart
	Models    int      `json:"models"`
	MinOut    *float64 `json:"min_out,omitempty"`  // cheapest active output price; absent when empty
	BestTPS   *float64 `json:"best_tps,omitempty"` // fastest measured tok/s; absent when unmeasured

	nodes, curated, models map[string]bool
}

func (s *supply) add(o offerView) {
	if s.nodes == nil {
		s.nodes, s.curated, s.models = map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	if o.Curated {
		s.curated[o.NodeID] = true
	} else {
		s.nodes[o.NodeID] = true
	}
	s.models[o.Model] = true
	s.Providers, s.Curated, s.Models = len(s.nodes), len(s.curated), len(s.models)
	if s.MinOut == nil || o.Out < *s.MinOut {
		out := o.Out
		s.MinOut = &out
	}
	if o.TPS > 0 && (s.BestTPS == nil || o.TPS > *s.BestTPS) {
		tps := o.TPS
		s.BestTPS = &tps
	}
}

type categorySupply struct {
	supply
	Subcategories []*supply `json:"subcategories"`
}

// categories handles GET /categories: the WHOLE category list (empty aisles included) with
// live supply per category and subcategory, built from the cached /discover view so it costs
// no extra recompute and never sees a private or banned station.
func (b *broker) categories(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w)
	// Cached like the views it is built from, so a burst of reads decodes the market once
	// per window (computeCategories takes no b.mu: it reads the cached /discover view).
	b.serveCachedJSON(w, "categories", publicMarketTTL, b.computeCategories)
}

func (b *broker) computeCategories() any {
	defs := protocol.Categories()
	cats := make([]*categorySupply, len(defs))
	byName := map[string]*categorySupply{}
	for i, d := range defs {
		c := &categorySupply{supply: supply{Name: d.Name}, Subcategories: make([]*supply, len(d.Subcategories))}
		for j, sub := range d.Subcategories {
			c.Subcategories[j] = &supply{Name: sub}
		}
		cats[i], byName[d.Name] = c, c
	}
	var uncategorized supply
	for _, o := range b.cachedOffers(nil) {
		if !o.Online {
			continue
		}
		c := byName[o.Category]
		if c == nil {
			uncategorized.add(o)
			continue
		}
		c.add(o)
		for _, s := range c.Subcategories {
			if s.Name == o.Subcategory {
				s.add(o)
			}
		}
	}
	return map[string]any{
		"version": protocol.TaxonomyVersion, "categories": cats, "uncategorized": uncategorized,
	}
}
