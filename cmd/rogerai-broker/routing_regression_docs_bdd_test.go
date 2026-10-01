package main

// routing_regression_docs_bdd_test.go makes the @docs scenarios of
// features/routing/regression_pins.feature EXECUTABLE: the doc-drift pins. They read the REAL
// artefacts a consumer or a maintainer reads - cmd/rogerai-broker/openapi.yaml (parsed as YAML,
// not grepped), web/src/manual.html, features/curated/curated_routing.feature, and the two TUI
// sources whose comments contradict each other today - and assert the contract's wording is
// there. Nothing is generated or mocked; a pin fails until the document actually says it.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"
)

type docState struct {
	api    map[string]any
	manual string
}

func (s *docState) reset() error {
	raw, err := os.ReadFile("openapi.yaml")
	if err != nil {
		return err
	}
	s.api = map[string]any{}
	if err := yaml.Unmarshal(raw, &s.api); err != nil {
		return fmt.Errorf("openapi.yaml does not parse: %w", err)
	}
	m, err := os.ReadFile("../../web/src/manual.html")
	if err != nil {
		return err
	}
	s.manual = string(m)
	return nil
}

// --- yaml navigation --------------------------------------------------------------

func dig(m any, path ...string) any {
	cur := m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = mm[k]
		if !ok {
			return nil
		}
	}
	return cur
}

func (s *docState) chatPost() map[string]any {
	m, _ := dig(s.api, "paths", "/v1/chat/completions", "post").(map[string]any)
	return m
}

func (s *docState) chatParam(name string) map[string]any {
	params, _ := s.chatPost()["parameters"].([]any)
	for _, p := range params {
		pm, _ := p.(map[string]any)
		if pm != nil && pm["in"] == "header" && pm["name"] == name {
			return pm
		}
	}
	return nil
}

func (s *docState) chatResponse(code string) map[string]any {
	m, _ := dig(s.chatPost(), "responses", code).(map[string]any)
	return m
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// --- Then: openapi ------------------------------------------------------------------

func (s *docState) listsRequestHeader(name string) error {
	if s.chatParam(name) == nil {
		return fmt.Errorf("openapi.yaml does not list the request header %s on POST /v1/chat/completions", name)
	}
	return nil
}

func (s *docState) prefDescriptionNamesValues() error {
	p := s.chatParam("X-Roger-Pref")
	if p == nil {
		return fmt.Errorf("X-Roger-Pref is not documented")
	}
	d := str(p["description"])
	for _, v := range []string{"cheap", "balanced", "fast", "reliable"} {
		if !strings.Contains(d, v) {
			return fmt.Errorf("X-Roger-Pref description does not name %q: %q", v, d)
		}
	}
	return nil
}

func (s *docState) maxOutDescribesDefault() error {
	p := s.chatParam("X-Roger-Max-Price-Out")
	if p == nil {
		return fmt.Errorf("X-Roger-Max-Price-Out is not documented")
	}
	d := str(p["description"])
	if !strings.Contains(d, "$10") && !strings.Contains(d, "10/1M") {
		return fmt.Errorf("X-Roger-Max-Price-Out description does not name the $10/1M default consumer cap: %q", d)
	}
	if !strings.Contains(strings.ToLower(d), "default") {
		return fmt.Errorf("X-Roger-Max-Price-Out description does not say the cap is a default: %q", d)
	}
	return nil
}

func (s *docState) maxOutDescribesClamp() error {
	p := s.chatParam("X-Roger-Max-Price-Out")
	if p == nil {
		return fmt.Errorf("X-Roger-Max-Price-Out is not documented")
	}
	d := strings.ToLower(str(p["description"]))
	if !strings.Contains(d, "clamp") || !strings.Contains(d, "ceiling") {
		return fmt.Errorf("X-Roger-Max-Price-Out description does not say a value above the ceiling is clamped: %q", d)
	}
	return nil
}

func (s *docState) listsResponseHeader(name string) error {
	r := s.chatResponse("200")
	if r == nil {
		return fmt.Errorf("no 200 response documented on /v1/chat/completions")
	}
	if hdrs, _ := r["headers"].(map[string]any); hdrs != nil {
		if _, ok := hdrs[name]; ok {
			return nil
		}
	}
	if strings.Contains(str(r["description"]), "`"+name+"`") {
		return nil
	}
	return fmt.Errorf("openapi.yaml does not list the response header %s on the 200 of /v1/chat/completions", name)
}

func (s *docState) streamDescriptionNamesUsageChunk() error {
	d := strings.ToLower(str(s.chatResponse("200")["description"]))
	if !strings.Contains(d, "usage chunk") {
		return fmt.Errorf("the 200 description does not name the final usage chunk for stream:true: %q", d)
	}
	return nil
}

func (s *docState) streamDescriptionNamesCostComment() error {
	d := strings.ToLower(str(s.chatResponse("200")["description"]))
	if !strings.Contains(d, "rogerai-cost=") || !strings.Contains(d, "deprecated") {
		return fmt.Errorf("the 200 description does not name the `: rogerai-cost=` comment as deprecated-but-present: %q", d)
	}
	return nil
}

func (s *docState) noCheapestActivePrice() error {
	raw, _ := yaml.Marshal(s.chatPost())
	if strings.Contains(string(raw), "cheapest active price") {
		return fmt.Errorf("/v1/chat/completions still describes selection as \"cheapest active price\"")
	}
	return nil
}

func (s *docState) describesFiltersAndScoring() error {
	d := strings.ToLower(str(s.chatPost()["description"]))
	if !strings.Contains(d, "eligib") || !strings.Contains(d, "scor") {
		return fmt.Errorf("the endpoint description does not name the eligibility filters and the scored selection: %q", d)
	}
	return nil
}

func (s *docState) chatRequestHasRoutingProps() error {
	props, _ := dig(s.api, "components", "schemas", "ChatRequest", "properties").(map[string]any)
	if props == nil {
		return fmt.Errorf("ChatRequest schema has no properties")
	}
	for _, k := range []string{"models", "provider", "roger"} {
		if _, ok := props[k]; !ok {
			return fmt.Errorf("ChatRequest schema lacks the %q property", k)
		}
	}
	return nil
}

// routingKeyFacets is the contract's type AND range per routing key (§1a): the schema facet
// each key must carry beyond its type. A boolean's range is its default; a list's is its
// bound; an enum's is its value set; a number's is its minimum; a free string (freq,
// profile) has no range and must be a string.
var routingKeyFacets = map[string]map[string]string{
	"provider": {
		"order": "maxItems", "only": "maxItems", "ignore": "maxItems", "quantizations": "maxItems",
		"allow_fallbacks": "default", "sort": "enum", "max_price": "properties", "require_parameters": "default",
	},
	"roger": {
		"pref": "enum", "require": "items.enum", "params_b": "minItems", "min_ctx": "minimum", "min_tps": "minimum",
		"max_ttft_ms": "minimum", "trust_min": "enum", "self_hosted_only": "default", "confidential": "default",
		"region": "maxItems", "freq": "type", "profile": "type",
	},
}

func (s *docState) routingKeysTyped() error {
	props, _ := dig(s.api, "components", "schemas", "ChatRequest", "properties").(map[string]any)
	if props == nil {
		return fmt.Errorf("ChatRequest schema has no properties")
	}
	for obj, keys := range routingKeyFacets {
		sub, _ := dig(props, obj, "properties").(map[string]any)
		if sub == nil {
			return fmt.Errorf("ChatRequest.%s has no properties", obj)
		}
		for k, facet := range keys {
			km, _ := sub[k].(map[string]any)
			if km == nil {
				return fmt.Errorf("ChatRequest.%s.%s is not documented", obj, k)
			}
			if str(km["type"]) == "" && km["$ref"] == nil && km["oneOf"] == nil {
				return fmt.Errorf("ChatRequest.%s.%s has no type", obj, k)
			}
			var have any = km[facet]
			if facet == "items.enum" {
				have = dig(km, "items", "enum")
			}
			if have == nil {
				return fmt.Errorf("ChatRequest.%s.%s documents no range (%s)", obj, k, facet)
			}
		}
	}
	m, _ := dig(props, "models").(map[string]any)
	if str(m["type"]) != "array" || m["maxItems"] == nil {
		return fmt.Errorf("ChatRequest.models is not an array with a maxItems bound")
	}
	for _, k := range []string{"prompt", "completion", "request"} {
		if dig(props, "provider", "properties", "max_price", "properties", k, "minimum") == nil {
			return fmt.Errorf("ChatRequest.provider.max_price.%s documents no minimum", k)
		}
	}
	return nil
}

func (s *docState) responseDocumentsCodes(code string, codes ...string) error {
	r := s.chatResponse(code)
	if r == nil {
		return fmt.Errorf("no %s response documented on /v1/chat/completions", code)
	}
	raw, _ := yaml.Marshal(r)
	for _, c := range codes {
		if !strings.Contains(string(raw), c) {
			return fmt.Errorf("the %s response does not document error code %q", code, c)
		}
	}
	return nil
}

func (s *docState) doc400Codes() error {
	return s.responseDocumentsCodes("400", "unknown_routing_key", "invalid_routing_value", "conflicting_routing_keys")
}

func (s *docState) doc503Codes() error {
	return s.responseDocumentsCodes("503", "no_match", "band_cooling")
}

var discoveryFilterParams = []string{"model", "min_tps", "max_price_out", "params_min", "params_max", "min_ctx", "region", "quant", "capability"}

func (s *docState) discoveryDescribesFilters(path string) error {
	get, _ := dig(s.api, "paths", path, "get").(map[string]any)
	if get == nil {
		return fmt.Errorf("no GET %s documented", path)
	}
	raw, _ := yaml.Marshal(get)
	for _, p := range discoveryFilterParams {
		if !strings.Contains(string(raw), p) {
			return fmt.Errorf("GET %s does not name the filter param %q it honors", path, p)
		}
	}
	return nil
}

// --- Then: the manual ---------------------------------------------------------------

// manualRoutingHeading is a heading whose text BEGINS with "Routing" (so "Resilient by design:
// transparent re-routing" is not a routing section), or any element carrying id="routing".
var manualRoutingHeading = regexp.MustCompile(`(?i)<h[1-3][^>]*>\s*routing\b|id="routing"`)

// routingSection returns the manual's routing section: from its heading to the next <h2>.
func (s *docState) routingSection() string {
	loc := manualRoutingHeading.FindStringIndex(s.manual)
	if loc == nil {
		return ""
	}
	rest := s.manual[loc[0]:]
	if i := strings.Index(rest[1:], "<h2"); i >= 0 {
		rest = rest[:i+1]
	}
	return rest
}

func (s *docState) manualHasRoutingSection() error {
	if s.routingSection() == "" {
		return fmt.Errorf("web/src/manual.html has no routing section (a heading starting with \"Routing\" or id=\"routing\")")
	}
	return nil
}

func (s *docState) manualListsBodyKeys() error {
	sec := s.routingSection()
	for _, k := range []string{"models", "provider", "roger"} {
		if !strings.Contains(sec, "<code>"+k+"</code>") && !strings.Contains(sec, "<code>\""+k+"\"</code>") {
			return fmt.Errorf("the manual's routing section does not list the %q routing key", k)
		}
	}
	return nil
}

func (s *docState) manualListsRequestHeaders() error {
	sec := s.routingSection()
	for _, h := range []string{"X-Roger-Confidential", "X-Roger-Min-TPS", "X-Roger-Max-Price", "X-Roger-Max-Price-Out", "X-Roger-Pref", "X-Roger-Node", "X-Roger-Exclude-Nodes", "X-Roger-Freq"} {
		if !strings.Contains(sec, h) {
			return fmt.Errorf("the manual's routing section does not list the request header %s", h)
		}
	}
	return nil
}

// --- Then: source and spec files -----------------------------------------------------

func (s *docState) curatedRoutingHasFilterScenario() error {
	raw, err := os.ReadFile("../../features/curated/curated_routing.feature")
	if err != nil {
		return err
	}
	text := string(raw)
	i := strings.Index(text, "# --- the filter")
	if i < 0 {
		return fmt.Errorf("curated_routing.feature has no filter section")
	}
	if !strings.Contains(text[i:], "Scenario") {
		return fmt.Errorf("the filter section of curated_routing.feature has no scenario")
	}
	return nil
}

func (s *docState) curatedRoutingCitesSelfHosted() error {
	raw, err := os.ReadFile("../../features/curated/curated_routing.feature")
	if err != nil {
		return err
	}
	if !strings.Contains(string(raw), "roger.self_hosted_only") {
		return fmt.Errorf("curated_routing.feature does not cite roger.self_hosted_only")
	}
	return nil
}

func (s *docState) limitQuantsDocSaysBoth() error {
	raw, err := os.ReadFile("../../internal/tui/view_money.go")
	if err != nil {
		return err
	}
	text := string(raw)
	i := strings.Index(text, "Quants []string")
	if i < 0 {
		return fmt.Errorf("Limit.Quants not found in view_money.go")
	}
	// The doc comment is the block immediately above the field.
	block := text[max(0, i-1500):i]
	if !strings.Contains(block, "roger use") || !strings.Contains(block, "TUI") {
		return fmt.Errorf("the Limit.Quants doc comment does not say the rule binds in the TUI AND in `roger use`")
	}
	return nil
}

func (s *docState) quantRouteNoteGone() error {
	raw, err := os.ReadFile("../../internal/tui/quant_route.go")
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), "does NOT yet apply") {
		return fmt.Errorf("quant_route.go still carries the \"does NOT yet apply\" note")
	}
	return nil
}

func TestRoutingRegressionDocsBDD(t *testing.T) {
	st := &docState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.reset()
			})
			// Background (no broker needed for a file check; the steps are shared wording)
			sc.Step(`^a broker with an empty in-memory node registry$`, func() error { return nil })
			sc.Step(`^the fee rate is 30%$`, func() error { return nil })
			sc.Step(`^the consumer default out-cap is \$10/1M$`, func() error { return nil })
			sc.Step(`^the register out-price ceiling is \$100/1M$`, func() error { return nil })
			// openapi
			sc.Step(`^openapi\.yaml lists the request header "([^"]*)" on /v1/chat/completions$`, st.listsRequestHeader)
			sc.Step(`^its description names cheap, balanced, fast and reliable$`, st.prefDescriptionNamesValues)
			sc.Step(`^the X-Roger-Max-Price-Out description names the default consumer cap$`, st.maxOutDescribesDefault)
			sc.Step(`^it says a value above the ceiling is clamped, not rejected$`, st.maxOutDescribesClamp)
			sc.Step(`^openapi\.yaml lists the response header "([^"]*)" on the 200 of /v1/chat/completions$`, st.listsResponseHeader)
			sc.Step(`^the 200 description for stream:true names the final usage chunk$`, st.streamDescriptionNamesUsageChunk)
			sc.Step("^it names the `: rogerai-cost=` comment as deprecated-but-present$", st.streamDescriptionNamesCostComment)
			sc.Step(`^openapi\.yaml contains no "cheapest active price" under /v1/chat/completions$`, st.noCheapestActivePrice)
			sc.Step(`^the description names the eligibility filters and the scored selection$`, st.describesFiltersAndScoring)
			sc.Step(`^the ChatRequest schema has properties "models", "provider" and "roger"$`, st.chatRequestHasRoutingProps)
			sc.Step(`^each routing key in the contract appears with its type and range$`, st.routingKeysTyped)
			sc.Step(`^the 400 response documents unknown_routing_key, invalid_routing_value and conflicting_routing_keys$`, st.doc400Codes)
			sc.Step(`^the 503 response documents no_match and band_cooling$`, st.doc503Codes)
			sc.Step(`^the openapi\.yaml description of (/discover|/market) names the filter params it honors$`, st.discoveryDescribesFilters)
			// manual
			sc.Step(`^web/src/manual\.html has a routing section$`, st.manualHasRoutingSection)
			sc.Step(`^it lists the "models", "provider" and "roger" keys$`, st.manualListsBodyKeys)
			sc.Step(`^it lists every X-Roger-\* request header the relay reads$`, st.manualListsRequestHeaders)
			// spec + source
			sc.Step(`^features/curated/curated_routing\.feature has a scenario under its filter section$`, st.curatedRoutingHasFilterScenario)
			sc.Step(`^it cites roger\.self_hosted_only$`, st.curatedRoutingCitesSelfHosted)
			sc.Step("^the Limit\\.Quants documentation says the rule binds in the TUI AND in `roger use`$", st.limitQuantsDocSaysBoth)
			sc.Step(`^quant_route\.go no longer carries the "does NOT yet apply" note$`, st.quantRouteNoteGone)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/regression_pins.feature"},
			Tags:     "@docs",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing/regression_pins @docs scenarios failed (see godog output above)")
	}
}
