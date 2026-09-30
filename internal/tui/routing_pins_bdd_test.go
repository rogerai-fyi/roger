package tui

// routing_pins_bdd_test.go makes the @tui scenarios of features/routing/regression_pins.feature
// EXECUTABLE (slice 0 of the routing-expression set): the pref knob reaching every in-booth
// path, hidden curated supply travelling as a broker-side filter (roger.self_hosted_only)
// instead of an exclude list, and the quant rule travelling as provider.quantizations.
//
// THE SEAM. The TUI is REAL (the same model, key handling, agent runtime, in-channel chat and
// booth live proxy production runs); the broker is a RECORDING httptest server, the seam every
// TUI runner in this package uses. What the TUI can prove is what it EMITS: the request body
// and headers each in-booth path puts on the wire. Steps phrased in broker terms ("cur-1 is NOT
// a candidate", "n-human serves", "the failover plan contains n-h2", "the broker's routing pass
// ran with the fast profile") are therefore asserted HERE as "the request the TUI emitted
// carries the constraint that makes that true on the real broker" (roger.self_hosted_only /
// roger.pref / provider.quantizations), and the broker-side truth of those constraints is
// pinned by cmd/rogerai-broker/routing_regression_pins_bdd_test.go and
// features/routing/open_network_filters.feature. Nothing here fakes a broker's routing.
//
// GROUND TRUTH at 518c698b (why these go RED): the agent turn sends X-Roger-Exclude-Nodes from
// prefExcludes (agent.go:741), the in-channel chat from chatExcludes (update.go:312), the live
// proxy from routeExcludes (tui.go:2741); no path sends a body routing object; the limits
// editor has max $/1M out and min t/s columns and no pref (view_money.go:161); U only flips
// fNoCurated (update.go:571) and refuses curated-ONLY bands (agent.go:1543).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/client"
)

// pinReq is one request the recording broker saw.
type pinReq struct {
	headers http.Header
	body    map[string]any
}

type routingPinsBDD struct {
	t   *testing.T
	m   model
	srv *httptest.Server

	mu   sync.Mutex
	reqs []pinReq
	// script decides the stand-in's answer per request (nil = a plain 200 reply)
	script func(n int, w http.ResponseWriter)

	saveCalls   int // LimitStore.Save invocations (the only persistence the TUI owns)
	saved       map[string]Limit
	offers      []offer // the dial as seeded
	refusedLine string  // the refusal text a turn produced, if any
}

// --- fixtures -----------------------------------------------------------------------------

func (s *routingPinsBDD) startBroker() {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "offers": []any{}})
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.reqs = append(s.reqs, pinReq{headers: r.Header.Clone(), body: body})
		n := len(s.reqs)
		script := s.script
		s.mu.Unlock()
		if script != nil {
			script(n, w)
			return
		}
		w.Header().Set("X-RogerAI-Cost", "0.0001")
		w.Header().Set("X-RogerAI-Provider", "n-human")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "roger"}}},
		})
	}))
	s.t.Cleanup(s.srv.Close)
}

func (s *routingPinsBDD) seed() {
	var m tea.Model = New(s.srv.URL, "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.Update(offersMsg(append([]offer{}, s.offers...)))
	m, _ = m.Update(balanceMsg{balance: 42.17, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	mm := m.(model)
	if mm.limits == nil {
		mm.limits = &LimitStore{Models: map[string]Limit{}}
	}
	mm.limits.Save = func(models map[string]Limit, def Limit) {
		s.saveCalls++
		s.saved = models
	}
	s.m = mm
}

func (s *routingPinsBDD) addOffer(o offer) {
	o.Online = true
	if o.Region == "" {
		o.Region = "home"
	}
	s.offers = append(s.offers, o)
	s.seed()
}

func (s *routingPinsBDD) requests() []pinReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pinReq{}, s.reqs...)
}

func (s *routingPinsBDD) last() (pinReq, error) {
	rs := s.requests()
	if len(rs) == 0 {
		return pinReq{}, fmt.Errorf("no request reached the broker")
	}
	return rs[len(rs)-1], nil
}

// bodyPath walks a dotted key ("roger.pref", "provider.quantizations") through the body.
func bodyPath(body map[string]any, path string) (any, bool) {
	var cur any = body
	for _, k := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func describeRouting(r pinReq) string {
	var parts []string
	for _, h := range []string{"X-Roger-Exclude-Nodes", "X-Roger-Pref", "X-Roger-Confidential", "X-Roger-Max-Price-Out"} {
		if v := r.headers.Get(h); v != "" {
			parts = append(parts, h+"="+v)
		}
	}
	for _, k := range []string{"roger", "provider", "models"} {
		if v, ok := r.body[k]; ok {
			b, _ := json.Marshal(v)
			parts = append(parts, k+"="+string(b))
		}
	}
	if len(parts) == 0 {
		return "(no routing headers, no routing body keys)"
	}
	return strings.Join(parts, " ")
}

func (s *routingPinsBDD) pressU() error {
	if len(s.m.bands) == 0 {
		s.seed()
	}
	out, _ := s.m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'U'}})
	s.m = asModel(out)
	return nil
}

func (s *routingPinsBDD) tuneTo(mdl, quant string) error {
	for _, b := range s.m.bands {
		if b.model == mdl && b.quant == quant {
			var o offer
			if b.cheapest != nil {
				o = *b.cheapest
			} else {
				o = offer{NodeID: "station", Model: mdl, Quant: quant, Online: true}
			}
			o.Quant = quant
			s.m.connected = &o
			s.m.q = quote{b: b, limit: s.m.limits.resolve(mdl)}
			return nil
		}
	}
	return fmt.Errorf("the dial has no %q row for %q (rows: %v)", quant, mdl, s.rowsOf(mdl))
}

func (s *routingPinsBDD) rowsOf(mdl string) []string {
	var out []string
	for _, b := range s.m.bands {
		if b.model == mdl {
			out = append(out, fmt.Sprintf("%q", b.quant))
		}
	}
	return out
}

// runAgentTurn drives the REAL submit path (the curated-only refusal lives there) and, when a
// turn actually started, pumps the drain to completion exactly as the lifecycle harness does.
func (s *routingPinsBDD) runAgentTurn(mdl string) error {
	if err := s.ensureTuned(mdl); err != nil {
		return err
	}
	nm, _ := s.m.enterAgent()
	s.m = asModel(nm)
	s.m.refreshAgentModel()
	before := len(s.requests())
	m2, _ := s.m.submitAgentPrompt(queuedPrompt{text: "hello"})
	s.m = m2
	if !s.m.agentBusy {
		s.refusedLine = stripANSI(strings.Join(s.m.agentLines, "\n"))
		return nil
	}
	s.m.startAgentTurn("hello")()
	s.drain(200)
	if len(s.requests()) == before {
		return fmt.Errorf("the agent turn ran but no request reached the broker")
	}
	return nil
}

// ensureTuned resolves "the band" of a scenario that names none: the sole model on the dial.
func (s *routingPinsBDD) ensureTuned(mdl string) error {
	if mdl == "" && s.m.connected == nil {
		seen := map[string]bool{}
		for _, b := range s.m.bands {
			seen[b.model] = true
			mdl = b.model
		}
		if len(seen) != 1 {
			return fmt.Errorf("no band is tuned and the dial has %d models; the scenario must tune one first", len(seen))
		}
	}
	if mdl != "" && (s.m.connected == nil || s.m.connected.Model != mdl) {
		return s.tuneTo(mdl, s.firstQuant(mdl))
	}
	if s.m.connected == nil {
		return fmt.Errorf("no band is tuned; the scenario must tune one first")
	}
	return nil
}

func (s *routingPinsBDD) firstQuant(mdl string) string {
	for _, b := range s.m.bands {
		if b.model == mdl {
			return b.quant
		}
	}
	return ""
}

func (s *routingPinsBDD) drain(limit int) {
	for i := 0; i < limit; i++ {
		cmd := s.m.waitAgentEvent()
		if cmd == nil {
			return
		}
		got := make(chan tea.Msg, 1)
		go func() { got <- cmd() }()
		var msg tea.Msg
		select {
		case msg = <-got:
		case <-time.After(20 * time.Second):
			s.t.Fatalf("the agent drain blocked after %d message(s)", i)
		}
		nm, _ := s.m.Update(msg)
		s.m = asModel(nm)
		if _, ok := msg.(agentDoneMsg); ok {
			return
		}
	}
	s.t.Fatal("the agent turn never reported done")
}

// sendChatTurn drives the in-channel chat's real key path (update.go:312) and runs the Cmd.
func (s *routingPinsBDD) sendChatTurn(mdl string) error {
	if err := s.ensureTuned(mdl); err != nil {
		return err
	}
	s.m.mode = modeChat
	s.m.chatIn.Focus()
	s.m.chatIn.SetValue("hello")
	out, cmd := s.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	s.m = asModel(out)
	if cmd == nil {
		s.refusedLine = stripANSI(strings.Join(s.m.transcript, "\n") + "\n" + s.m.status)
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				if m := c(); m != nil {
					nm, _ := s.m.Update(m)
					s.m = asModel(nm)
				}
			}
		}
		return nil
	}
	if msg != nil {
		nm, _ := s.m.Update(msg)
		s.m = asModel(nm)
	}
	return nil
}

// sendGuestViaLiveProxy binds the booth's REAL live proxy for the tuned band (liveProxyOpts +
// ProxyHandlerLive, exactly what bindChannel does) and relays one guest request through it.
func (s *routingPinsBDD) sendGuestViaLiveProxy(mdl string) error {
	if err := s.ensureTuned(mdl); err != nil {
		return err
	}
	s.m.proxyKey = client.NewSessionKey()
	holder := client.NewProxyOptionsHolder(s.m.liveProxyOpts(*s.m.connected, s.m.alert))
	h := client.ProxyHandlerLive(holder)
	body := `{"model":"anything","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.m.proxyKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 500 {
		return fmt.Errorf("the live proxy answered %d: %s", rec.Code, rec.Body.String())
	}
	return nil
}

// --- Background --------------------------------------------------------------------------

func (s *routingPinsBDD) emptyBroker() error {
	s.startBroker()
	s.offers = nil
	s.seed()
	return nil
}

// The fee, the consumer default cap and the register ceiling are BROKER knobs. The stand-in
// records requests and has no ledger; these Givens are satisfied by the seam itself (the
// broker runner sets the real knobs). They are not assertions.
func (s *routingPinsBDD) brokerKnob(string) error { return nil }

// --- Givens ------------------------------------------------------------------------------

func (s *routingPinsBDD) limitsEditorSetsPref(pref, mdl string) error {
	// The REAL editor: open [3] CONFIG on the band and look for a pref field to set. There is
	// none today (view_money.go:161 renders "max $/1M out" and "min t/s" only), so this Given
	// fails for the right reason instead of smuggling a value in through a struct field the
	// editor never exposes.
	s.addOffer(offer{NodeID: "n-1", Model: mdl, PriceOut: 0.5, TPS: 40})
	s.m.mode = modeLimits
	v := stripANSI(s.m.View())
	if !strings.Contains(strings.ToLower(v), "pref") {
		return fmt.Errorf("the limits editor has no pref field to set %q on %q:\n%s", pref, mdl, v)
	}
	return fmt.Errorf("the limits editor shows a pref field but this harness has no key path to set it yet")
}

func (s *routingPinsBDD) nodeOnAirAtOut(node, mdl string, out float64) error {
	s.addOffer(offer{NodeID: node, Model: mdl, PriceOut: out, PriceIn: out / 2, TPS: 40})
	return nil
}

func (s *routingPinsBDD) curatedOnAirAtOut(node, mdl string, out float64) error {
	s.addOffer(offer{NodeID: node, Model: mdl, PriceOut: out, PriceIn: out / 2, TPS: 90,
		Curated: true, CuratedProvider: "openrouter", Region: "openrouter", UpstreamOut: out / 1.3, UpstreamIn: out / 2.6})
	return nil
}

func (s *routingPinsBDD) mixedBand(mdl, human, curated string) error {
	_ = s.nodeOnAirAtOut(human, mdl, 0.5)
	return s.curatedOnAirAtOut(curated, mdl, 0.2)
}

func (s *routingPinsBDD) pressedU() error { return s.pressU() }

func (s *routingPinsBDD) pressedUTwice() error {
	if err := s.pressU(); err != nil {
		return err
	}
	return s.pressU()
}

func (s *routingPinsBDD) tunedToBand(mdl string) error { return s.tuneTo(mdl, s.firstQuant(mdl)) }

func (s *routingPinsBDD) tuiClosed() error {
	// Closing the booth persists nothing of its own: the only channel to disk is
	// LimitStore.Save, which this harness captures. The model is dropped like a quit.
	s.m = model{}
	return nil
}

func (s *routingPinsBDD) curatedOnlyBand(node, mdl string) error {
	return s.curatedOnAirAtOut(node, mdl, 0.2)
}

func (s *routingPinsBDD) humansAndCurated(h1, h2, cur, mdl string) error {
	_ = s.nodeOnAirAtOut(h1, mdl, 0.4)
	_ = s.nodeOnAirAtOut(h2, mdl, 0.5)
	return s.curatedOnAirAtOut(cur, mdl, 0.2)
}

func (s *routingPinsBDD) nodeAnswers429(node string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("X-RogerAI-Provider", node)
			w.Header().Set("Retry-After", "5")
			http.Error(w, `{"error":{"message":"rate limited upstream"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("X-RogerAI-Cost", "0.0001")
		w.Header().Set("X-RogerAI-Provider", "n-h2")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "roger"}}},
		})
	}
	return nil
}

func (s *routingPinsBDD) configQuants(mdl string, list string) error {
	var quants []string
	if err := json.Unmarshal([]byte(list), &quants); err != nil {
		return fmt.Errorf("bad quants list %s: %v", list, err)
	}
	if s.m.limits == nil {
		s.m.limits = &LimitStore{Models: map[string]Limit{}}
	}
	s.m.limits.set(mdl, Limit{Quants: quants})
	if len(s.offers) == 0 {
		// a rule needs a band to bind on; give it the model's rows the scenario names later
		s.addOffer(offer{NodeID: "n-rule", Model: mdl, PriceOut: 0.5, TPS: 40})
		s.m.limits.set(mdl, Limit{Quants: quants})
	}
	return nil
}

func (s *routingPinsBDD) noRowTunedByQuant() error {
	if s.m.connected != nil {
		s.m.connected.Quant = ""
	}
	return nil
}

func (s *routingPinsBDD) dialGroupsRows(mdl, q1, q2 string) error {
	s.addOffer(offer{NodeID: "n-" + strings.ToLower(q1), Model: mdl, Quant: q1, PriceOut: 0.3, TPS: 40})
	s.addOffer(offer{NodeID: "n-" + strings.ToLower(q2), Model: mdl, Quant: q2, PriceOut: 0.6, TPS: 30})
	if len(s.rowsOf(mdl)) != 2 {
		return fmt.Errorf("expected two rows for %q, got %v", mdl, s.rowsOf(mdl))
	}
	return nil
}

func (s *routingPinsBDD) tunesRow(quant string) error { return s.tunesRowOf(quant, "qwen3-32b") }

func (s *routingPinsBDD) tunesRowOf(quant, mdl string) error {
	if !s.hasRow(mdl, quant) {
		s.addOffer(offer{NodeID: "n-" + strings.ToLower(quant), Model: mdl, Quant: quant, PriceOut: 0.4, TPS: 40})
	}
	return s.tuneTo(mdl, quant)
}

func (s *routingPinsBDD) hasRow(mdl, quant string) bool {
	for _, b := range s.m.bands {
		if b.model == mdl && b.quant == quant {
			return true
		}
	}
	return false
}

func (s *routingPinsBDD) nodeNoQuant(node, mdl string) error {
	s.addOffer(offer{NodeID: node, Model: mdl, PriceOut: 0.3, TPS: 40})
	return nil
}

// --- Whens -------------------------------------------------------------------------------

func (s *routingPinsBDD) agentTurnOn(mdl string) error { return s.runAgentTurn(mdl) }
func (s *routingPinsBDD) agentTurn() error             { return s.runAgentTurn("") }
func (s *routingPinsBDD) chatOn(mdl string) error      { return s.sendChatTurn(mdl) }
func (s *routingPinsBDD) chat() error                  { return s.sendChatTurn("") }
func (s *routingPinsBDD) liveProxyOn(mdl string) error { return s.sendGuestViaLiveProxy(mdl) }
func (s *routingPinsBDD) liveProxy() error             { return s.sendGuestViaLiveProxy("") }

func (s *routingPinsBDD) agentTurnPickedFirst(node string) error {
	if err := s.runAgentTurn(""); err != nil {
		return err
	}
	rs := s.requests()
	if len(rs) == 0 {
		return fmt.Errorf("no request reached the broker")
	}
	return nil
}

// "the operator runs `roger use ...`" after the booth closed: the standalone CLI lives in
// cmd/rogerai and reads config.json, which the booth never wrote (see tuiClosed). The step
// asserts the only thing that could carry the hide across: nothing was persisted.
func (s *routingPinsBDD) operatorRunsUse(args string) error {
	if strings.Contains(args, "--") {
		// A flagged `roger use` is the standalone CLI's behaviour (cmd/rogerai); this package
		// cannot run it. The scenario is @tui by tagging only; it belongs to the @cli runner.
		return fmt.Errorf("standalone `roger use %s` is not runnable from the TUI package (see cmd/rogerai/routing_flags_bdd_test.go)", args)
	}
	if s.saveCalls != 0 {
		return fmt.Errorf("pressing U persisted limits (%d Save call(s)): %v", s.saveCalls, s.saved)
	}
	return nil
}

func (s *routingPinsBDD) chatViaLocalProxy() error {
	if s.m.connected == nil && len(s.m.bands) == 0 {
		return nil // the booth is closed; nothing to relay (asserted by the Thens)
	}
	return s.sendGuestViaLiveProxy("")
}

func (s *routingPinsBDD) routesWithSelfHosted() error {
	// A booth request under the hide is the only way the TUI emits this constraint.
	return s.runAgentTurn("")
}

// --- Thens -------------------------------------------------------------------------------

func (s *routingPinsBDD) bodyPref(pref string) error {
	r, err := s.last()
	if err != nil {
		return err
	}
	v, ok := bodyPath(r.body, "roger.pref")
	if !ok || v != pref {
		return fmt.Errorf("the request body carries no roger.pref %q; wire was %s", pref, describeRouting(r))
	}
	return nil
}

func (s *routingPinsBDD) bodyPrefAndPass(pref string) error { return s.bodyPref(pref) }

func (s *routingPinsBDD) bodySelfHostedTrue() error {
	r, err := s.last()
	if err != nil {
		return err
	}
	v, ok := bodyPath(r.body, "roger.self_hosted_only")
	if !ok || v != true {
		return fmt.Errorf("the request body carries no roger.self_hosted_only true; wire was %s", describeRouting(r))
	}
	return nil
}

func (s *routingPinsBDD) noSelfHostedKey() error {
	rs := s.requests()
	if len(rs) == 0 {
		return nil
	}
	r := rs[len(rs)-1]
	if _, ok := bodyPath(r.body, "roger.self_hosted_only"); ok {
		return fmt.Errorf("the request carries roger.self_hosted_only although curated is shown; wire was %s", describeRouting(r))
	}
	return nil
}

// Broker-side facts observed at the seam: the constraint that makes them true on the real
// broker must be on the wire of EVERY request this scenario emitted.
func (s *routingPinsBDD) curatedNotCandidate(node string) error {
	rs := s.requests()
	if len(rs) == 0 {
		return fmt.Errorf("no request reached the broker")
	}
	for i, r := range rs {
		if v, ok := bodyPath(r.body, "roger.self_hosted_only"); !ok || v != true {
			return fmt.Errorf("request %d carries no roger.self_hosted_only, so the broker could still pick %q; wire was %s", i+1, node, describeRouting(r))
		}
	}
	return nil
}

func (s *routingPinsBDD) humanServes(string) error { return s.curatedNotCandidate("") }

func (s *routingPinsBDD) humanCandidate(node string) error {
	r, err := s.last()
	if err != nil {
		return err
	}
	if ex := r.headers.Get("X-Roger-Exclude-Nodes"); strings.Contains(ex, node) {
		return fmt.Errorf("the hide excluded the human station %q via X-Roger-Exclude-Nodes=%s", node, ex)
	}
	if v, ok := bodyPath(r.body, "provider.ignore"); ok {
		if b, _ := json.Marshal(v); strings.Contains(string(b), node) {
			return fmt.Errorf("the hide excluded the human station %q via provider.ignore", node)
		}
	}
	return nil
}

func (s *routingPinsBDD) bothCandidates(a, b string) error {
	if err := s.humanCandidate(a); err != nil {
		return err
	}
	if err := s.humanCandidate(b); err != nil {
		return err
	}
	return s.noSelfHostedKey()
}

func (s *routingPinsBDD) excludeDoesNotName(node string) error {
	r, err := s.last()
	if err != nil {
		return err
	}
	if ex := r.headers.Get("X-Roger-Exclude-Nodes"); strings.Contains(ex, node) {
		return fmt.Errorf("the hide travelled as an exclude list naming %q (X-Roger-Exclude-Nodes=%s), not as a broker filter", node, ex)
	}
	return nil
}

func (s *routingPinsBDD) lateCuratedStillExcluded() error {
	// A station that registered after the last dial scan is unknown to m.bands, so an
	// exclude list can never name it; only the body filter binds it.
	return s.curatedNotCandidate("a late curated station")
}

func (s *routingPinsBDD) turnRefusedBeforeRequest() error {
	if len(s.requests()) != 0 {
		return fmt.Errorf("a request reached the broker although the turn should have been refused")
	}
	if s.m.agentBusy {
		return fmt.Errorf("a turn started on a band the operator hid")
	}
	return nil
}

func (s *routingPinsBDD) refusalNamesU() error {
	v := s.refusedLine
	if !strings.Contains(v, "curated") || !strings.Contains(v, "hidden") || !strings.Contains(v, "U") {
		return fmt.Errorf("the refusal must say curated supply is hidden and name U:\n%s", v)
	}
	return nil
}

func (s *routingPinsBDD) failoverPlanHumanOnly(h2, cur string) error {
	// The plan is the broker's (upstream_failover.feature); what the TUI can guarantee is that
	// every attempt it emitted - the first and any retry - carries the hide, so the broker's
	// plan can never contain the curated station.
	rs := s.requests()
	if len(rs) == 0 {
		return fmt.Errorf("no request reached the broker")
	}
	for i, r := range rs {
		if v, ok := bodyPath(r.body, "roger.self_hosted_only"); !ok || v != true {
			return fmt.Errorf("attempt %d carries no roger.self_hosted_only, so the broker's plan could contain %q; wire was %s", i+1, cur, describeRouting(r))
		}
		if ex := r.headers.Get("X-Roger-Exclude-Nodes"); strings.Contains(ex, h2) {
			return fmt.Errorf("attempt %d excluded the human station %q", i+1, h2)
		}
	}
	return nil
}

func (s *routingPinsBDD) noSelfHostedOnWire() error {
	rs := s.requests()
	for i, r := range rs {
		if _, ok := bodyPath(r.body, "roger.self_hosted_only"); ok {
			return fmt.Errorf("request %d carries roger.self_hosted_only after the booth closed", i+1)
		}
	}
	return nil
}

func (s *routingPinsBDD) configNoSelfHosted() error {
	if s.saveCalls != 0 {
		return fmt.Errorf("the booth persisted limits on U (%d Save call(s))", s.saveCalls)
	}
	return nil
}

func (s *routingPinsBDD) bodyQuantizations(list string) error {
	var want []string
	if err := json.Unmarshal([]byte(list), &want); err != nil {
		return fmt.Errorf("bad list %s: %v", list, err)
	}
	r, err := s.last()
	if err != nil {
		return err
	}
	v, ok := bodyPath(r.body, "provider.quantizations")
	if !ok {
		return fmt.Errorf("the request body carries no provider.quantizations (want %v); wire was %s", want, describeRouting(r))
	}
	got, _ := v.([]any)
	if len(got) != len(want) {
		return fmt.Errorf("provider.quantizations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("provider.quantizations = %v, want %v", got, want)
		}
	}
	return nil
}

func (s *routingPinsBDD) noQuantExclude() error {
	r, err := s.last()
	if err != nil {
		return err
	}
	if ex := r.headers.Get("X-Roger-Exclude-Nodes"); ex != "" {
		return fmt.Errorf("the request still carries a quant-derived X-Roger-Exclude-Nodes=%s", ex)
	}
	return nil
}

func (s *routingPinsBDD) refusedNamingQuantRule() error {
	if len(s.requests()) != 0 {
		return fmt.Errorf("a request reached the broker although the row is outside the quant rule")
	}
	if !strings.Contains(strings.ToLower(s.refusedLine), "quant") {
		return fmt.Errorf("the refusal does not name the quant rule:\n%s", s.refusedLine)
	}
	return nil
}

func (s *routingPinsBDD) noRequestReached() error {
	if n := len(s.requests()); n != 0 {
		return fmt.Errorf("%d request(s) reached the broker", n)
	}
	return nil
}

func (s *routingPinsBDD) nodeNotCandidate(node string) error {
	// An unlabeled station is bound only by the body filter (an exclude list could name it,
	// but that is the mechanism this pin retires): the row's one-entry list must be on the wire.
	r, err := s.last()
	if err != nil {
		return err
	}
	if _, ok := bodyPath(r.body, "provider.quantizations"); !ok {
		return fmt.Errorf("no provider.quantizations on the wire, so the broker could pick %q; wire was %s", node, describeRouting(r))
	}
	return nil
}

func TestRoutingPinsTUI(t *testing.T) {
	st := &routingPinsBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = routingPinsBDD{t: t}
				return c, nil
			})
			// Background
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyBroker)
			sc.Step(`^the fee rate is (.+)$`, st.brokerKnob)
			sc.Step(`^the consumer default out-cap is (.+)$`, st.brokerKnob)
			sc.Step(`^the register out-price ceiling is (.+)$`, st.brokerKnob)
			// Givens
			sc.Step(`^the TUI limits editor sets pref "([^"]*)" for "([^"]*)"$`, st.limitsEditorSetsPref)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at out-price \$([0-9.]+)/1M$`, st.nodeOnAirAtOut)
			sc.Step(`^curated station "([^"]*)" is on air for "([^"]*)" at out-price \$([0-9.]+)/1M$`, st.curatedOnAirAtOut)
			sc.Step(`^a mixed band for "([^"]*)" with human "([^"]*)" and curated "([^"]*)"$`, st.mixedBand)
			sc.Step(`^the TUI operator has pressed U \(curated hidden\)$`, st.pressedU)
			sc.Step(`^the TUI operator has pressed U twice \(curated shown\)$`, st.pressedUTwice)
			sc.Step(`^the operator is tuned to the "([^"]*)" band$`, st.tunedToBand)
			sc.Step(`^the TUI has been closed$`, st.tuiClosed)
			sc.Step(`^curated station "([^"]*)" is the only station on air for "([^"]*)"$`, st.curatedOnlyBand)
			sc.Step(`^human nodes "([^"]*)" and "([^"]*)" and curated "([^"]*)" are on air for "([^"]*)"$`, st.humansAndCurated)
			sc.Step(`^"([^"]*)" answers the next request with an upstream 429$`, st.nodeAnswers429)
			sc.Step(`^the config has limits\.models\."([^"]*)"\.quants (\[.*\])$`, st.configQuants)
			sc.Step(`^no row is tuned by quant$`, st.noRowTunedByQuant)
			sc.Step(`^the dial groups "([^"]*)" into a "([^"]*)" row and a "([^"]*)" row$`, st.dialGroupsRows)
			sc.Step(`^the operator tunes the "([^"]*)" row$`, st.tunesRow)
			sc.Step(`^the operator tunes the "([^"]*)" row of "([^"]*)"$`, st.tunesRowOf)
			sc.Step(`^node "([^"]*)" is on air for "([^"]*)" with no quant label$`, st.nodeNoQuant)
			// Whens
			sc.Step(`^an agent turn runs on "([^"]*)" inside the TUI$`, st.agentTurnOn)
			sc.Step(`^an agent turn runs on "([^"]*)"$`, st.agentTurnOn)
			sc.Step(`^an agent turn runs and "([^"]*)" is picked first$`, st.agentTurnPickedFirst)
			sc.Step(`^an agent turn runs$`, st.agentTurn)
			sc.Step(`^an in-channel chat message is sent on "([^"]*)"$`, st.chatOn)
			sc.Step(`^an in-channel chat message is sent$`, st.chat)
			sc.Step(`^the live proxy relays a guest request on "([^"]*)"$`, st.liveProxyOn)
			sc.Step(`^a guest operator sends a request through the live proxy$`, st.liveProxy)
			sc.Step("^the operator runs `roger use ([^`]*)`$", st.operatorRunsUse)
			sc.Step(`^a chat request goes through the local proxy$`, st.chatViaLocalProxy)
			sc.Step(`^a request routes with roger\.self_hosted_only true$`, st.routesWithSelfHosted)
			// Thens
			sc.Step(`^the request body carries roger\.pref "([^"]*)" and the broker's routing pass ran with the \w+ profile$`, st.bodyPrefAndPass)
			sc.Step(`^the request body carries roger\.pref "([^"]*)"$`, st.bodyPref)
			sc.Step(`^the request carries body roger\.self_hosted_only true$`, st.bodySelfHostedTrue)
			sc.Step(`^the request carries no roger\.self_hosted_only key$`, st.noSelfHostedKey)
			sc.Step(`^the request carries no roger\.self_hosted_only$`, st.noSelfHostedOnWire)
			sc.Step(`^config\.json carries no self_hosted_only key$`, st.configNoSelfHosted)
			sc.Step(`^"([^"]*)" is NOT a candidate$`, st.curatedNotCandidateOrNode)
			sc.Step(`^"([^"]*)" serves$`, st.humanServes)
			sc.Step(`^"([^"]*)" is a candidate$`, st.humanCandidate)
			sc.Step(`^both "([^"]*)" and "([^"]*)" are candidates$`, st.bothCandidates)
			sc.Step(`^the request's X-Roger-Exclude-Nodes header does not name "([^"]*)"$`, st.excludeDoesNotName)
			sc.Step(`^curated stations that register AFTER the last dial scan are still excluded$`, st.lateCuratedStillExcluded)
			sc.Step(`^the turn is refused before any request$`, st.turnRefusedBeforeRequest)
			sc.Step(`^the refusal says curated supply is hidden and names U to show it$`, st.refusalNamesU)
			sc.Step(`^the failover plan contains "([^"]*)" and not "([^"]*)"$`, st.failoverPlanHumanOnly)
			sc.Step(`^the request carries body provider\.quantizations (\[.*\])$`, st.bodyQuantizations)
			sc.Step(`^the request body carries provider\.quantizations (\[.*\])$`, st.bodyQuantizations)
			sc.Step(`^the request carries no X-Roger-Exclude-Nodes derived from quant$`, st.noQuantExclude)
			sc.Step(`^the turn is refused with a message naming the quant rule$`, st.refusedNamingQuantRule)
			sc.Step(`^no request reaches the broker$`, st.noRequestReached)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/routing/regression_pins.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing regression pin (TUI) scenarios failed")
	}
}

// curatedNotCandidateOrNode routes the shared "X is NOT a candidate" step: a curated station
// is bound by the self_hosted_only filter, any other station by the quant filter.
func (s *routingPinsBDD) curatedNotCandidateOrNode(node string) error {
	for _, o := range s.offers {
		if o.NodeID == node && o.Curated {
			return s.curatedNotCandidate(node)
		}
	}
	return s.nodeNotCandidate(node)
}
