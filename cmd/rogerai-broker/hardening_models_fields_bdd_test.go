package main

// hardening_models_fields_bdd_test.go makes features/discovery/models_openrouter_fields.feature
// EXECUTABLE (slice 6, contract §14.B4) against the REAL broker: stations stand up through the
// sa6H harness (hardening_affinity_bdd_test.go) and /v1/models, /v1/models/{id} and /discover are
// read through the broker's own mux, exactly as a client reads them.
//
// Notes on the fixtures:
//   - "an estimated window" marks the offer's ctx CtxEstimated (the broker's own flag).
//   - "a free window active now" declares a time-of-use free window spanning the real clock.
//   - "the class ... currently expands to ..." states the market the Background already built;
//     the runner does not compute the expansion (the broker must).
//   - an expired tools verdict is produced by advancing the broker clock and the shared store
//     past toolsVerifiedTTL after a real canary verdict.

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

type mo6State struct {
	*sa6H
	data      []map[string]any
	one       map[string]any
	code      int
	lastNamed string
	lastList  []string
}

func (s *mo6State) gets(path string) error {
	code, body, _ := s.get(s.b, path)
	s.code, s.data, s.one = code, nil, nil
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if strings.HasPrefix(path, "/v1/models/") {
		_ = json.Unmarshal(body, &s.one)
		return nil
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("GET %s = %d %.200s", path, code, body)
	}
	s.data = env.Data
	return nil
}

func (s *mo6State) entry(id string) (map[string]any, error) {
	s.lastNamed = id
	if s.one != nil && s.one["id"] == id {
		return s.one, nil
	}
	for _, e := range s.data {
		if e["id"] == id {
			return e, nil
		}
	}
	return nil, fmt.Errorf("no %q entry (status %d)", id, s.code)
}

// --- Given ---------------------------------------------------------------------------------------

func (s *mo6State) declaredWindow(name, model, in, out, ctx string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = rs1i(ctx), false })
	return nil
}

func (s *mo6State) estimatedWindow(name, model, in, out string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.CtxEstimated = true })
	return nil
}

func (s *mo6State) estimatedOf(name, model, ctx string) error {
	st := s.onAir(name, model, 0.10, 0.30)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = rs1i(ctx), true })
	return nil
}

func (s *mo6State) freeWindowNow(name, model, in, out string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	now := time.Now().UTC()
	min := now.Hour()*60 + now.Minute()
	s.setOffer(st.id, func(o *protocol.ModelOffer) {
		o.Schedule = []protocol.PriceWindow{{Start: rs1hhmm(min - 60), End: rs1hhmm(min + 60), Free: true}}
	})
	return nil
}

func (s *mo6State) tie(a, b, model string) error {
	s.onAir(a, model, 0.10, 0.30) // blended 0.10*3+0.30 = 0.60
	s.onAir(b, model, 0.05, 0.45) // blended 0.05*3+0.45 = 0.60
	return nil
}

func (s *mo6State) curated(name, model, in, out string) error {
	st := s.onAir(name, model, rs1f(in), rs1f(out))
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Curated, reg.CuratedProvider = true, "openrouter"
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *mo6State) privateBand(name, model, in, out string) error {
	st, err := s.band("B", "B-code", name, model, nil)
	if err != nil {
		return err
	}
	_ = st
	s.setPrice(name, rs1f(in), rs1f(out))
	return nil
}

func (s *mo6State) declaredToolsOnly(name string) error {
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Capabilities = append(o.Capabilities, protocol.CapTools) })
	return nil
}

func (s *mo6State) toolsExpired(name, model string) error {
	if err := s.rs1EarnedTools(name, model); err != nil {
		return err
	}
	s.advance(toolsVerifiedTTL + time.Minute)
	return nil
}

func (s *mo6State) declaresVision(name string) error {
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Capabilities = append(o.Capabilities, protocol.CapVision) })
	return nil
}

func (s *mo6State) noCaps(name, model string) error {
	st := s.onAir(name, model, 0.10, 0.30)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Capabilities = nil })
	return nil
}

func (s *mo6State) voice(model string) error {
	st := s.onAir("voice-1", model, 0.10, 0.30)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Modality = protocol.ModalityTTS })
	return nil
}

func (s *mo6State) classExpands(string, string) error { return nil } // the Background built that market

// --- Then ---------------------------------------------------------------------------------------

func mo6Num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func (s *mo6State) contextLength(id, n string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	if v, ok := mo6Num(e["context_length"]); !ok || v != rs1f(n) {
		return fmt.Errorf("%q context_length %v, want %s", id, e["context_length"], n)
	}
	return nil
}

func (s *mo6State) noContextLength(id string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	if v, ok := e["context_length"]; ok {
		return fmt.Errorf("%q carries context_length %v, want none", id, v)
	}
	if _, ok := e["pricing"]; !ok {
		return fmt.Errorf("%q carries no pricing either: the OpenRouter fields are absent altogether", id)
	}
	return nil
}

func (s *mo6State) ctxAgrees() error {
	n := 0
	for _, e := range s.data {
		cl, ok1 := mo6Num(e["context_length"])
		rb, _ := e["rogerai"].(map[string]any)
		cm, ok2 := mo6Num(rb["ctx_max"])
		if ok1 && ok2 {
			n++
			if cl != cm {
				return fmt.Errorf("%v: context_length %v, ctx_max %v", e["id"], cl, cm)
			}
		}
	}
	if n == 0 {
		return fmt.Errorf("no entry carries both context_length and rogerai.ctx_max")
	}
	return nil
}

func (s *mo6State) pricing(id string) (string, string, error) {
	e, err := s.entry(id)
	if err != nil {
		return "", "", err
	}
	p, ok := e["pricing"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("%q carries no pricing object (%v)", id, e["pricing"])
	}
	a, _ := p["prompt"].(string)
	b, _ := p["completion"].(string)
	if a == "" || b == "" {
		return "", "", fmt.Errorf("%q pricing %v: prompt and completion must be strings", id, p)
	}
	return a, b, nil
}

func (s *mo6State) pricingIs(id, prompt, completion string) error {
	a, b, err := s.pricing(id)
	if err != nil {
		return err
	}
	if a != prompt || b != completion {
		return fmt.Errorf("%q pricing prompt %q completion %q, want %q / %q", id, a, b, prompt, completion)
	}
	return nil
}

func (s *mo6State) pricingIsNot(id, prompt, completion string) error {
	a, b, err := s.pricing(id)
	if err != nil {
		return err
	}
	if a == prompt && b == completion {
		return fmt.Errorf("%q pricing pairs prompt %q with completion %q", id, a, b)
	}
	return nil
}

var mo6Decimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)

func (s *mo6State) plainDecimals() error {
	if len(s.data) == 0 {
		return fmt.Errorf("no entries")
	}
	for _, e := range s.data {
		a, b, err := s.pricing(fmt.Sprint(e["id"]))
		if err != nil {
			return err
		}
		for _, v := range []string{a, b} {
			if !mo6Decimal.MatchString(v) {
				return fmt.Errorf("%v pricing value %q is not a plain decimal string", e["id"], v)
			}
		}
	}
	return nil
}

func (s *mo6State) noExponent() error {
	for _, e := range s.data {
		a, b, err := s.pricing(fmt.Sprint(e["id"]))
		if err != nil {
			return err
		}
		if strings.ContainsAny(a+b, "eE") {
			return fmt.Errorf("%v pricing %q/%q uses exponent notation", e["id"], a, b)
		}
	}
	return nil
}

// perToken is the expected per-token string, computed exactly: the $/1M figure's shortest
// decimal divided by 1e6 as a rational (a float division adds noise such as
// 0.1/1e6 = "0.00000010000000000000001", which the literal scenarios rule out).
func perToken(v float64) string {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(v, 'f', -1, 64))
	s := strings.TrimRight(r.Quo(r, big.NewRat(1_000_000, 1)).FloatString(30), "0")
	return strings.TrimSuffix(s, ".")
}

func (s *mo6State) reflects(id, name string) (bool, error) {
	a, b, err := s.pricing(id)
	if err != nil {
		return false, err
	}
	st := s.st(name)
	return a == perToken(st.priceIn) && b == perToken(st.priceOut), nil
}

func (s *mo6State) stillReflects(id, name string) error {
	ok, err := s.reflects(id, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%q pricing does not reflect %q", id, name)
	}
	return nil
}

func (s *mo6State) notReflects(id, name string) error {
	ok, err := s.reflects(id, name)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("%q pricing reflects the private %q", id, name)
	}
	return nil
}

func (s *mo6State) firstOnDiscover(id, a, b string) error {
	code, body, _ := s.get(s.b, "/discover")
	if code != 200 {
		return fmt.Errorf("/discover = %d", code)
	}
	var env struct {
		Offers []map[string]any `json:"offers"`
	}
	_ = json.Unmarshal(body, &env)
	for _, o := range env.Offers {
		if o["model"] == id {
			return s.stillReflects(id, s.nameOfID(fmt.Sprint(o["node_id"])))
		}
	}
	return fmt.Errorf("no %q offer on /discover", id)
}

func mo6Quoted(list string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(list, -1) {
		out = append(out, m[1])
	}
	return out
}

func (s *mo6State) params(e map[string]any) (map[string]bool, error) {
	sp, ok := e["supported_parameters"].([]any)
	if !ok {
		return nil, fmt.Errorf("%v carries no supported_parameters array", e["id"])
	}
	m := map[string]bool{}
	for _, v := range sp {
		m[fmt.Sprint(v)] = true
	}
	return m, nil
}

func (s *mo6State) everyIncludes(list string) error {
	want := mo6Quoted(list)
	if len(s.data) == 0 {
		return fmt.Errorf("no entries")
	}
	for _, e := range s.data {
		m, err := s.params(e)
		if err != nil {
			return err
		}
		for _, w := range want {
			if !m[w] {
				return fmt.Errorf("%v supported_parameters lack %q", e["id"], w)
			}
		}
	}
	return nil
}

func (s *mo6State) includes(id, list string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	m, err := s.params(e)
	if err != nil {
		return err
	}
	s.lastList = mo6Quoted(list)
	for _, w := range s.lastList {
		if !m[w] {
			return fmt.Errorf("%q supported_parameters lack %q", id, w)
		}
	}
	return nil
}

func (s *mo6State) includesNone(id string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	m, err := s.params(e)
	if err != nil {
		return err
	}
	for _, w := range s.lastList {
		if m[w] {
			return fmt.Errorf("%q supported_parameters include %q", id, w)
		}
	}
	return nil
}

func (s *mo6State) excludes(id, p string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	m, err := s.params(e)
	if err != nil {
		return err
	}
	if m[p] {
		return fmt.Errorf("%q supported_parameters include %q", id, p)
	}
	return nil
}

func (s *mo6State) agreesWithCaps(p string) error {
	if len(s.data) == 0 {
		return fmt.Errorf("no entries")
	}
	for _, e := range s.data {
		m, err := s.params(e)
		if err != nil {
			return err
		}
		rb, _ := e["rogerai"].(map[string]any)
		caps, _ := rb["capabilities"].([]any)
		has := false
		for _, c := range caps {
			has = has || fmt.Sprint(c) == p
		}
		if m[p] != has {
			return fmt.Errorf("%v: %q in supported_parameters %v, in capabilities %v", e["id"], p, m[p], has)
		}
	}
	return nil
}

func (s *mo6State) modalities(e map[string]any, key string) ([]string, error) {
	a, ok := e["architecture"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%v carries no architecture", e["id"])
	}
	l, ok := a[key].([]any)
	if !ok {
		return nil, fmt.Errorf("%v architecture has no %s", e["id"], key)
	}
	var out []string
	for _, v := range l {
		out = append(out, fmt.Sprint(v))
	}
	return out, nil
}

func (s *mo6State) textText(id string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	for _, k := range []string{"input_modalities", "output_modalities"} {
		l, err := s.modalities(e, k)
		if err != nil {
			return err
		}
		if strings.Join(l, ",") != "text" {
			return fmt.Errorf("%q %s %v, want [text]", id, k, l)
		}
	}
	return nil
}

func (s *mo6State) inputIs(id, list string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	l, err := s.modalities(e, "input_modalities")
	if err != nil {
		return err
	}
	if want := mo6Quoted(list); strings.Join(l, ",") != strings.Join(want, ",") {
		return fmt.Errorf("%q input_modalities %v, want %v", id, l, want)
	}
	return nil
}

func (s *mo6State) noEntry(id string) error {
	if s.code != 200 {
		return fmt.Errorf("GET = %d", s.code)
	}
	for _, e := range s.data {
		if e["id"] == id {
			return fmt.Errorf("an entry has id %q", id)
		}
	}
	return nil
}

func (s *mo6State) singleEqualsList() error {
	if s.one == nil {
		return fmt.Errorf("no single entry read")
	}
	id := fmt.Sprint(s.one["id"])
	one := s.one
	if err := s.gets("/v1/models"); err != nil {
		return err
	}
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	for _, k := range []string{"context_length", "pricing", "supported_parameters", "architecture"} {
		a, _ := json.Marshal(one[k])
		b, _ := json.Marshal(e[k])
		if _, ok := one[k]; !ok {
			return fmt.Errorf("the single entry carries no %s", k)
		}
		if string(a) != string(b) {
			return fmt.Errorf("%s: single %s, list %s", k, a, b)
		}
	}
	return nil
}

func (s *mo6State) itsPricingFrom(name string) error { return s.stillReflects(s.lastNamed, name) }

// mo6RogeraiFields is the rogerai block as it stood before §14.B4, including the blended-price
// pair slice 6 part A added (db745146, §14.7) after this runner's ground-truth snapshot, and the
// attribute_sources labels §14.B7 adds (features/security/routing_integrity.feature).
var mo6RogeraiFields = map[string]bool{"blended_price_per_1m": true, "blend_ratio": true, "attribute_sources": true, "providers": true, "min_price_in": true, "min_price_out": true, "best_tps": true,
	"ctx_max": true, "params_b": true, "params_b_min": true, "params_b_max": true, "params_estimated": true, "quants": true,
	"capabilities": true, "verified": true, "free_now": true, "confidential": true, "curated": true, "cooling": true, "cooling_until": true}

func (s *mo6State) rogeraiUnchanged() error {
	if len(s.data) == 0 {
		return fmt.Errorf("no entries")
	}
	for _, e := range s.data {
		if strings.HasPrefix(fmt.Sprint(e["id"]), "@class/") {
			continue
		}
		rb, ok := e["rogerai"].(map[string]any)
		if !ok {
			return fmt.Errorf("%v has no rogerai block", e["id"])
		}
		for k := range rb {
			if !mo6RogeraiFields[k] {
				return fmt.Errorf("%v rogerai block gained %q", e["id"], k)
			}
		}
		if _, ok := e["pricing"]; !ok {
			return fmt.Errorf("%v carries no top-level pricing: the new fields are not there to compare against", e["id"])
		}
	}
	return nil
}

func (s *mo6State) openAIList() error { return s.gets("/v1/models") }

func (s *mo6State) readsIDs() error {
	if len(s.data) == 0 {
		return fmt.Errorf("no entries")
	}
	for _, e := range s.data {
		raw, _ := json.Marshal(e)
		var oa struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		}
		if err := json.Unmarshal(raw, &oa); err != nil || oa.ID == "" {
			return fmt.Errorf("an OpenAI client cannot read %s: %v", raw, err)
		}
	}
	return nil
}

func (s *mo6State) openRouterReads(id, n string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(e)
	var or struct {
		ContextLength int `json:"context_length"`
		Pricing       struct {
			Prompt string `json:"prompt"`
		} `json:"pricing"`
	}
	if err := json.Unmarshal(raw, &or); err != nil {
		return err
	}
	if or.ContextLength != rs1i(n) {
		return fmt.Errorf("context_length %d, want %s", or.ContextLength, n)
	}
	if _, err := strconv.ParseFloat(or.Pricing.Prompt, 64); err != nil {
		return fmt.Errorf("pricing.prompt %q does not parse as a number: %v", or.Pricing.Prompt, err)
	}
	return nil
}

func (s *mo6State) classEntry(id, owner string) error {
	e, err := s.entry(id)
	if err != nil {
		return err
	}
	if e["owned_by"] != owner {
		return fmt.Errorf("%q owned_by %v, want %q", id, e["owned_by"], owner)
	}
	return nil
}

func (s *mo6State) expandsTo(list string) error {
	e, err := s.entry(s.lastNamed)
	if err != nil {
		return err
	}
	rb, _ := e["rogerai"].(map[string]any)
	got, _ := rb["expands_to"].([]any)
	var g []string
	for _, v := range got {
		g = append(g, fmt.Sprint(v))
	}
	if want := mo6Quoted(list); strings.Join(g, ",") != strings.Join(want, ",") {
		return fmt.Errorf("%q expands_to %v, want %v", s.lastNamed, g, want)
	}
	return nil
}

func (s *mo6State) samePricingAs(a, b string) error {
	pa1, pa2, err := s.pricing(a)
	if err != nil {
		return err
	}
	pb1, pb2, err := s.pricing(b)
	if err != nil {
		return err
	}
	if pa1 != pb1 || pa2 != pb2 {
		return fmt.Errorf("%q pricing %s/%s, %q %s/%s", a, pa1, pa2, b, pb1, pb2)
	}
	return nil
}

func TestModelsOpenRouterFieldsBDD(t *testing.T) {
	sa6Run(t, "../../features/discovery/models_openrouter_fields.feature", func(sc *godog.ScenarioContext, h *sa6H) {
		s := &mo6State{sa6H: h}
		sc.Step(`^station "([^"]+)" is on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M with a declared window of (\d+)$`, s.declaredWindow)
		sc.Step(`^station "([^"]+)" is on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M with an estimated window$`, s.estimatedWindow)
		sc.Step(`^station "([^"]+)" is on air for "([^"]+)" with an estimated window of (\d+)$`, s.estimatedOf)
		sc.Step(`^station "([^"]+)" is the only station for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M with a free window active now$`, s.freeWindowNow)
		sc.Step(`^stations "([^"]+)" and "([^"]+)" are on air for "([^"]+)" at the same blended cost with different in and out prices$`, s.tie)
		sc.Step(`^curated station "([^"]+)" is on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M$`, s.curated)
		sc.Step(`^a private band whose only station "([^"]+)" serves "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M$`, s.privateBand)
		sc.Step(`^"([^"]+)" earned verified "tools" for "([^"]+)"$`, s.rs1EarnedTools)
		sc.Step(`^"([^"]+)" earned verified "tools" for "([^"]+)" and the verdict has expired$`, s.toolsExpired)
		sc.Step(`^"([^"]+)" registered declaring "tools" but never passed a tool-call canary$`, s.declaredToolsOnly)
		sc.Step(`^"([^"]+)" declares "vision"$`, s.declaresVision)
		sc.Step(`^station "([^"]+)" is on air for "([^"]+)" declaring no capabilities$`, s.noCaps)
		sc.Step(`^a voice station is on air for "([^"]+)"$`, s.voice)
		sc.Step(`^the class "([^"]+)" currently expands to (.+)$`, s.classExpands)
		sc.Step(`^a consumer GETs (\S+)$`, s.gets)
		sc.Step(`^an OpenAI-shaped client lists models$`, s.openAIList)
		sc.Step(`^an OpenRouter-shaped client lists models$`, s.openAIList)
		sc.Step(`^the "([^"]+)" entry has context_length (\d+)$`, s.contextLength)
		sc.Step(`^the "([^"]+)" entry has no context_length$`, s.noContextLength)
		sc.Step(`^every entry's context_length equals its rogerai\.ctx_max when both are present$`, s.ctxAgrees)
		sc.Step(`^the "([^"]+)" entry has pricing prompt "([^"]+)" and completion "([^"]+)"$`, s.pricingIs)
		sc.Step(`^the "([^"]+)" pricing is not prompt "([^"]+)" with completion "([^"]+)"$`, s.pricingIsNot)
		sc.Step(`^every pricing value matches the pattern of a plain decimal string$`, s.plainDecimals)
		sc.Step(`^no pricing value contains "e" or "E"$`, s.noExponent)
		sc.Step(`^the "([^"]+)" pricing comes from whichever of "([^"]+)", "([^"]+)" is first on /discover$`, s.firstOnDiscover)
		sc.Step(`^the "([^"]+)" entry's pricing still reflects "([^"]+)"$`, s.stillReflects)
		sc.Step(`^the "([^"]+)" pricing does not reflect "([^"]+)"$`, s.notReflects)
		sc.Step(`^every entry's supported_parameters include (.+)$`, s.everyIncludes)
		sc.Step(`^the "([^"]+)" supported_parameters include none of them$`, s.includesNone)
		sc.Step(`^the "([^"]+)" supported_parameters include (".+)$`, s.includes)
		sc.Step(`^the "([^"]+)" supported_parameters do not include "([^"]+)"$`, s.excludes)
		sc.Step(`^"([^"]+)" is in supported_parameters exactly when "[^"]+" is in rogerai\.capabilities$`, s.agreesWithCaps)
		sc.Step(`^the "([^"]+)" architecture is input_modalities \["text"\] and output_modalities \["text"\]$`, s.textText)
		sc.Step(`^the "([^"]+)" architecture input_modalities is \[(.*)\]$`, s.inputIs)
		sc.Step(`^no entry has id "([^"]+)"$`, s.noEntry)
		sc.Step(`^the entry carries context_length, pricing, supported_parameters and architecture equal to the list entry's$`, s.singleEqualsList)
		sc.Step(`^its pricing comes from "([^"]+)"$`, s.itsPricingFrom)
		sc.Step(`^each entry's rogerai block has exactly the fields it had before this change$`, s.rogeraiUnchanged)
		sc.Step(`^it reads every entry's id without error$`, s.readsIDs)
		sc.Step(`^it reads "([^"]+)" context_length (\d+) and a prompt price as a number parsed from a string$`, s.openRouterReads)
		sc.Step(`^an entry with id "([^"]+)" exists with owned_by "([^"]+)"$`, s.classEntry)
		sc.Step(`^its rogerai\.expands_to is \[(.*)\]$`, s.expandsTo)
		sc.Step(`^the "([^"]+)" entry has the pricing of the "([^"]+)" entry$`, s.samePricingAs)
	})
}
