package main

// hardening_class_aliases_bdd_test.go makes features/routing/class_aliases.feature EXECUTABLE
// (slice 6, contract §14.B5) against the REAL broker (sa6H, hardening_affinity_bdd_test.go).
//
// Fixtures and observation points:
//   - every chat model in the Background table gets one station NAMED after the model, so "the
//     station for X" and "X goes off air" address it directly; a "declared" size is the offer's
//     params_b, an "estimated" one is left to the broker's id estimate, "no" carries neither.
//   - "the effective models" of a request are read from that request's /generation record
//     (`models`), where the contract puts the expanded list; the response's X-RogerAI-Model and
//     X-RogerAI-Class are read from the response.
//   - the plan ("contains only", "free offers", "price order") is the rs1 plan probe: the request
//     replayed with every station answering 429 once, in upstream-hit order.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

type ca6State struct {
	*sa6H
}

func (s *ca6State) model(name, params, declared, tools, in, out string) {
	st := s.onAir(name, name, rs1f(in), rs1f(out))
	s.setOffer(st.id, func(o *protocol.ModelOffer) {
		o.ParamsB = 0
		if declared == "yes" {
			o.ParamsB = rs1f(params)
		}
	})
	if tools == "yes" {
		s.b.recordToolProbe(st.id, name, true, false, true)
	}
}

func (s *ca6State) table(t *godog.Table) error {
	for _, r := range t.Rows[1:] {
		c := r.Cells
		s.model(c[0].Value, c[1].Value, strings.TrimSpace(c[2].Value), c[3].Value, c[4].Value, c[5].Value)
	}
	return nil
}

func (s *ca6State) edgeModel(name, size string) error {
	s.model(name, size, "yes", "yes", "0.03", "0.05")
	return nil
}

func (s *ca6State) manySmall(n, size string) error {
	for i := 0; i < rs1i(n); i++ {
		name := fmt.Sprintf("tiny-%c-%sb", 'a'+i, size)
		s.model(name, size, "yes", "no", fmt.Sprintf("0.0%d", i+1), fmt.Sprintf("0.0%d", i+1))
	}
	return nil
}

func (s *ca6State) samePrices(a, b, size string) error {
	s.model(a, size, "yes", "no", "0.01", "0.01")
	s.model(b, size, "yes", "no", "0.01", "0.01")
	return nil
}

// effective reads the request's /generation record.
func (s *ca6State) effectiveOf(sh rs1Shot) ([]string, string, error) {
	id := sh.hdr.Get("X-RogerAI-Request-Id")
	if sh.code != 200 && sh.code != 503 {
		return nil, "", fmt.Errorf("the request answered %d %.300s", sh.code, sh.body)
	}
	code, body, _ := s.get(s.b, "/generation?id="+id)
	if s.lastSpec.caller == "grant" {
		// A grant request's record is read with the grant token, as its holder would read it.
		r := httptest.NewRequest(http.MethodGet, "/generation?id="+id, nil)
		r.Header.Set("Authorization", "Bearer "+s.grantSecret)
		rr := httptest.NewRecorder()
		s.b.routes().ServeHTTP(rr, r)
		code, body = rr.Code, rr.Body.Bytes()
	}
	if code != 200 {
		return nil, "", fmt.Errorf("GET /generation = %d %.200s", code, body)
	}
	var rec struct {
		Models         []string `json:"models"`
		ModelRequested string   `json:"model_requested"`
	}
	_ = json.Unmarshal(body, &rec)
	return rec.Models, rec.ModelRequested, nil
}

// expanded: the record's list must be an EXPANSION (real model ids), never the class itself,
// or a negative claim ("X is not among") would hold vacuously.
func (s *ca6State) expanded(sh rs1Shot) ([]string, error) {
	got, _, err := s.effectiveOf(sh)
	if err != nil {
		return nil, err
	}
	for _, m := range got {
		if strings.HasPrefix(m, "@class/") {
			return nil, fmt.Errorf("the request was not expanded: its models are %v", got)
		}
	}
	return got, nil
}

func (s *ca6State) effective() ([]string, error) { return s.expanded(s.shot) }

func (s *ca6State) effectiveAre(list string) error {
	got, err := s.effective()
	if err != nil {
		return err
	}
	if want := mo6Quoted(list); strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("effective models %v, want %v", got, want)
	}
	return nil
}

func (s *ca6State) among(name, is string) error {
	got, err := s.effective()
	if err != nil {
		return err
	}
	in := false
	for _, m := range got {
		in = in || m == name
	}
	if in != (strings.TrimSpace(is) == "is") {
		return fmt.Errorf("effective models %v; %q %s among them", got, name, is)
	}
	if len(got) == 0 {
		return fmt.Errorf("the request carried no effective models")
	}
	return nil
}

func (s *ca6State) notAmong(name string) error { return s.among(name, "is not") }
func (s *ca6State) isAmong(name string) error  { return s.among(name, "is") }

func (s *ca6State) countIs(n string) error {
	got, err := s.effective()
	if err != nil {
		return err
	}
	if len(got) != rs1i(n) {
		return fmt.Errorf("effective models %v, want %s entries", got, n)
	}
	return nil
}

func (s *ca6State) blended(name string) float64 {
	st := s.st(name)
	return st.priceIn*3 + st.priceOut
}

func (s *ca6State) fiveCheapest() error {
	got, err := s.effective()
	if err != nil {
		return err
	}
	// Every small-class model competes, the Background's two (phi-4-mini 3.8B, llama-3.1-8b 8B)
	// included: the class is over the whole market, not only the models this Given added.
	var all []string
	for n := range s.stations {
		if strings.HasPrefix(n, "tiny-") || n == "phi-4-mini" || n == "llama-3.1-8b" {
			all = append(all, n)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if s.blended(all[i]) != s.blended(all[j]) {
			return s.blended(all[i]) < s.blended(all[j])
		}
		return all[i] < all[j]
	})
	if strings.Join(got, ",") != strings.Join(all[:5], ",") {
		return fmt.Errorf("effective models %v, want the 5 cheapest %v", got, all[:5])
	}
	return nil
}

func (s *ca6State) precedes(a, b string) error {
	got, err := s.effective()
	if err != nil {
		return err
	}
	ia, ib := -1, -1
	for i, m := range got {
		if m == a {
			ia = i
		}
		if m == b {
			ib = i
		}
	}
	if ia < 0 || ib < 0 || ia > ib {
		return fmt.Errorf("effective models %v: %q must precede %q", got, a, b)
	}
	return nil
}

func (s *ca6State) everySame() error {
	if len(s.shots) < 2 {
		return fmt.Errorf("fewer than two requests")
	}
	var first []string
	for i, sh := range s.shots {
		got, err := s.expanded(sh)
		if err != nil {
			return err
		}
		if len(got) == 0 {
			return fmt.Errorf("request %d carried no effective models", i+1)
		}
		if i == 0 {
			first = got
			continue
		}
		if strings.Join(got, ",") != strings.Join(first, ",") {
			return fmt.Errorf("request %d had %v, request 1 had %v", i+1, got, first)
		}
	}
	return nil
}

func (s *ca6State) classHeader(want string) error { return s.headerIs("X-RogerAI-Class", want) }

func (s *ca6State) receiptServed(model string) error {
	rec, err := s.receipt()
	if err != nil {
		return err
	}
	if rec.ServedModel() != model {
		return fmt.Errorf("receipt served model %q, want %q", rec.ServedModel(), model)
	}
	return nil
}

func (s *ca6State) stationGot(model string) error {
	served := s.nameOfID(s.lastHdr.Get("X-RogerAI-Provider"))
	s.bodiesMu.Lock()
	body := s.bodies[served]
	s.bodiesMu.Unlock()
	if s.lastCode != 200 || body == nil {
		return fmt.Errorf("no station served (%d %.200s)", s.lastCode, s.lastBody)
	}
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	if m.Model != model {
		return fmt.Errorf("the station received model %q, want %q", m.Model, model)
	}
	return nil
}

func (s *ca6State) answers429(model string) error { s.onceStatus(model, 429); return nil }

func (s *ca6State) ok200Model(model string) error {
	if err := s.statusIs("200"); err != nil {
		return err
	}
	return s.headerIs("X-RogerAI-Model", model)
}

func (s *ca6State) oneHold() error {
	if s.shot.holdN != 1 {
		return fmt.Errorf("%d hold(s) placed, want exactly one", s.shot.holdN)
	}
	return nil
}

func (s *ca6State) priciestPair() error {
	got, err := s.effective()
	if err != nil {
		return err
	}
	if len(got) == 0 {
		return fmt.Errorf("no effective models")
	}
	priciest := got[0]
	for _, m := range got {
		if s.st(m).priceOut > s.st(priciest).priceOut {
			priciest = m
		}
	}
	// The broker prices the body it would forward to the priciest pair (model = that pair's
	// model): the class id never appears in a priced or forwarded body.
	var doc struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(s.shot.base, &doc); err != nil {
		return err
	}
	from, _ := json.Marshal(doc.Model)
	to, _ := json.Marshal(priciest)
	s.shot.base = bytes.Replace(s.shot.base, from, to, 1)
	return s.holdEqualsPriciest(priciest)
}

func (s *ca6State) planOnly(name string) error {
	order, _, _ := s.probePlan()
	if len(order) == 0 {
		return fmt.Errorf("the plan probe hit no station (%d %.200s)", s.lastCode, s.lastBody)
	}
	for _, n := range order {
		if n != name {
			return fmt.Errorf("plan %v, want only %q", order, name)
		}
	}
	return nil
}

func (s *ca6State) alsoFree(model string) error {
	s.onAir(model+"-free", model, 0, 0)
	return nil
}

func (s *ca6State) onlyFree() error {
	order, _, _ := s.probePlan()
	if len(order) == 0 {
		return fmt.Errorf("the plan probe hit no station (%d %.200s)", s.lastCode, s.lastBody)
	}
	for _, n := range order {
		if st := s.st(n); st.priceIn != 0 || st.priceOut != 0 {
			return fmt.Errorf("plan %v contains the priced %q", order, n)
		}
	}
	return nil
}

func (s *ca6State) priceOrder() error {
	order, _, _ := s.probePlan()
	if len(order) < 2 {
		return fmt.Errorf("plan %v: too short to show an order (%d %.200s)", order, s.lastCode, s.lastBody)
	}
	for i := 1; i < len(order); i++ {
		if s.st(order[i]).priceOut < s.st(order[i-1]).priceOut {
			return fmt.Errorf("plan %v is not in price order", order)
		}
	}
	return nil
}

func (s *ca6State) grantOwner(_, model string) error { return s.mint(s.st(model), nil, 0) }

func (s *ca6State) grantRelays(model string) error {
	return s.send(sa6Spec{caller: "grant", model: model, hdr: map[string]string{}})
}

func (s *ca6State) privateBand(model, size string) error {
	st, err := s.band("P", "P-code", model, model, nil)
	if err != nil {
		return err
	}
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.ParamsB = rs1f(size) })
	s.b.recordToolProbe(st.id, model, true, false, true)
	return nil
}

func (s *ca6State) bandWithCode(band, code, model, size string) error {
	st, err := s.band(band, code, model, model, nil)
	if err != nil {
		return err
	}
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.ParamsB = rs1f(size) })
	s.b.recordToolProbe(st.id, model, true, false, true)
	return nil
}

func (s *ca6State) noLarge() error {
	for n, st := range s.stations {
		var pb float64
		s.b.mu.Lock()
		for _, o := range s.b.nodes[st.id].Offers {
			pb = o.ParamsB
		}
		s.b.mu.Unlock()
		if pb >= 60 || strings.Contains(n, "70b") {
			s.goOffAir(n)
		}
	}
	return nil
}

func (s *ca6State) recordModels(list string) error {
	got, _, err := s.effectiveOf(s.shot)
	if err != nil {
		return err
	}
	if want := mo6Quoted(list); strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("record models %v, want %v", got, want)
	}
	return nil
}

func (s *ca6State) requested(want string) error {
	_, got, err := s.effectiveOf(s.shot)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("model_requested %q, want %q", got, want)
	}
	return nil
}

func (s *ca6State) dryRun(user, model string) error {
	return s.send(sa6Spec{user: user, caller: "user", model: model, hdr: map[string]string{}, roger: []sa6KV{{"dry_run", "true"}}})
}

func (s *ca6State) explainModels(list string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d %.300s", s.lastCode, s.lastBody)
	}
	var d struct {
		Models []string `json:"models"`
		Plan   any      `json:"plan"`
	}
	_ = json.Unmarshal(s.lastBody, &d)
	if d.Plan == nil {
		return fmt.Errorf("not an explain document: %.300s", s.lastBody)
	}
	if want := mo6Quoted(list); strings.Join(d.Models, ",") != strings.Join(want, ",") {
		return fmt.Errorf("explain models %v, want %v", d.Models, want)
	}
	return nil
}

func (s *ca6State) modelsExpandsTo(id, list string) error {
	m := &mo6State{sa6H: s.sa6H}
	if err := m.gets("/v1/models"); err != nil {
		return err
	}
	if _, err := m.entry(id); err != nil {
		return err
	}
	return m.expandsTo(list)
}

func (s *ca6State) classCounter(name, n string) error {
	return s.counterRose("class_requests{"+name+"}", n)
}

func (s *ca6State) bothSame() error { return s.everySame() }

func TestClassAliasesBDD(t *testing.T) {
	sa6Run(t, "../../features/routing/class_aliases.feature", func(sc *godog.ScenarioContext, h *sa6H) {
		s := &ca6State{sa6H: h}
		sc.Step(`^these chat models are on air with one station each:$`, s.table)
		sc.Step(`^a chat model "([^"]+)" with declared params_b (\d+) and verified tools is on air$`, s.edgeModel)
		sc.Step(`^(\d+) chat models of (\d+)B each are on air$`, s.manySmall)
		sc.Step(`^chat models "([^"]+)" and "([^"]+)" of (\d+)B are on air at the same prices$`, s.samePrices)
		sc.Step(`^the effective models are \[(.*)\]$`, s.effectiveAre)
		sc.Step(`^"([^"]+)" (is|is not) among the effective models$`, s.among)
		sc.Step(`^the effective models has (\d+) entries$`, s.countIs)
		sc.Step(`^they are the 5 cheapest by blended cost$`, s.fiveCheapest)
		sc.Step(`^"([^"]+)" precedes "([^"]+)" in the effective models$`, s.precedes)
		sc.Step(`^every request had the same effective models$`, s.everySame)
		sc.Step(`^X-RogerAI-Class is "([^"]+)"$`, s.classHeader)
		sc.Step(`^X-RogerAI-Model is "([^"]+)"$`, func(m string) error { return s.headerIs("X-RogerAI-Model", m) })
		sc.Step(`^the receipt's served model is "([^"]+)"$`, s.receiptServed)
		sc.Step(`^the body the serving station received has "model" "([^"]+)"$`, s.stationGot)
		sc.Step(`^the station for "([^"]+)" answers the next request with an upstream 429$`, s.answers429)
		sc.Step(`^the response is 200 with X-RogerAI-Model "([^"]+)"$`, s.ok200Model)
		sc.Step(`^exactly one hold was placed$`, s.oneHold)
		sc.Step(`^it covers the priciest \(model, station\) pair of the expanded plan$`, s.priciestPair)
		sc.Step(`^the effective plan contains only "([^"]+)"$`, s.planOnly)
		sc.Step(`^"([^"]+)" also has a free station$`, s.alsoFree)
		sc.Step(`^the plan contains only free offers$`, s.onlyFree)
		sc.Step(`^the plan follows price order across the expanded models$`, s.priceOrder)
		sc.Step(`^a free grant "([^"]+)" owned by the operator of the "([^"]+)" station only$`, s.grantOwner)
		sc.Step(`^the grant holder relays for "([^"]+)"$`, s.grantRelays)
		sc.Step(`^a private band whose only station serves "([^"]+)" \((\d+)B, tools verified\)$`, s.privateBand)
		sc.Step(`^a private band "([^"]+)" with code "([^"]+)" whose station serves "([^"]+)" \((\d+)B, tools verified\)$`, s.bandWithCode)
		sc.Step(`^the message names "([^"]+)"$`, s.errorMessageContains)
		sc.Step(`^no chat model of at least 60B is on air$`, s.noLarge)
		sc.Step(`^a station registers model "([^"]+)"$`, s.stationRegistersOffer)
		sc.Step(`^the registration is rejected$`, func() error {
			if s.regCode >= 200 && s.regCode < 300 {
				return fmt.Errorf("the registration of an @class/ model id was accepted (%d %.200s)", s.regCode, s.regBody)
			}
			return nil
		})
		sc.Step(`^the /generation record's models are \[(.*)\]$`, s.recordModels)
		sc.Step(`^its model_requested is "([^"]+)"$`, s.requested)
		sc.Step(`^"([^"]+)" posts a chat completion for "([^"]+)" with roger\.dry_run true$`, s.dryRun)
		sc.Step(`^the explain document's models are \[(.*)\]$`, s.explainModels)
		sc.Step(`^a consumer GETs /v1/models$`, func() error { return nil })
		sc.Step(`^the "([^"]+)" entry's rogerai\.expands_to is \[(.*)\]$`, s.modelsExpandsTo)
		sc.Step(`^/admin/live class_requests\{(\w+)\} rose by (\d+)$`, s.classCounter)
		sc.Step(`^both requests had the same effective models$`, s.bothSame)
	})
}
