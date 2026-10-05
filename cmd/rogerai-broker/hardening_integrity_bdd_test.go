package main

// hardening_integrity_bdd_test.go makes features/security/routing_integrity.feature EXECUTABLE
// (slice 6, contract §14.B7) on the shared fa6State harness (hardening_fairness_bdd_test.go):
// the REAL broker, the real store (Postgres when ROGERAI_TEST_DATABASE_URL is set), real
// stations posting signed receipts, and the real Tower fabric for the bridged scenarios.
//
// OBSERVATION POINTS:
//   - A probe is the broker's own canary sender (probeNode / probeToolCall) dispatching to a
//     real station; what the station received is read back from what it posted (the receipt's
//     "user", the result "id") and from the body its upstream received.
//   - Organic contradictions are produced for real: recount strikes come from relays where the
//     station over-claims and the broker's recount sidecar disagrees; "verified" is judged by
//     relaying with roger.trust_min "verified" limited to the station.
//   - Pick and body-decode counts have no observation point today: the steps read a
//     "picks=N" / "body_decodes=N" field off the relay's log line and fail naming the missing
//     field when it is absent.
//   - Region contradiction uses the broker's own network bucket (coarseNetBucket of the
//     station's address): North America 3.208.0.1, Europe 52.28.0.1, no continent 203.0.113.5.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

type ri6State struct {
	*fa6State
	resMark  map[string]int // results each station had posted at the last mark
	bodyMark map[string]int
	logMark  int
	jobUsers []string
	genCode  int
	genBody  []byte
	discover []map[string]any
	admin    []byte
}

func (s *ri6State) reset() error {
	if err := s.fa6State.reset(); err != nil {
		return err
	}
	s.resMark, s.bodyMark, s.logMark, s.jobUsers = map[string]int{}, map[string]int{}, 0, nil
	s.genCode, s.genBody, s.discover, s.admin = 0, nil, nil, nil
	s.lastModel = "m"
	return nil
}

// --- marks ---------------------------------------------------------------------------------

func (s *ri6State) results(st *fstation) []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]map[string]any(nil), st.results...)
}

func (s *ri6State) mark() {
	s.snapshot()
	for n, st := range s.stations {
		s.resMark[n] = len(s.results(st))
		s.bodyMark[n] = len(s.bodies[n])
	}
	s.logMark = len(s.logs.String())
}

// newResults are the JobResults the station posted since the mark.
func (s *ri6State) newResults(name string) []map[string]any {
	r := s.results(s.st(name))
	if s.resMark[name] > len(r) {
		return nil
	}
	return r[s.resMark[name]:]
}

// newBodies are the bodies the station's upstream received since the mark, without the
// tool-call canary (its own probe kind).
func (s *ri6State) newBodies(name string, withTool bool) [][]byte {
	var out [][]byte
	bs := s.bodies[name]
	for i := s.bodyMark[name]; i < len(bs); i++ {
		if !withTool && bytes.Contains(bs[i], []byte(toolCanaryFn)) {
			continue
		}
		out = append(out, bs[i])
	}
	return out
}

func resultUser(r map[string]any) string {
	rec, _ := r["receipt"].(map[string]any)
	u, _ := rec["user"].(string)
	return u
}

func resultID(r map[string]any) string { id, _ := r["id"].(string); return id }

// --- Background ----------------------------------------------------------------------------

func (s *ri6State) stationPriced(name, model, in, out string) error {
	s.lastModel = model
	s.node(name, model, atofMust(in), atofMust(out))
	return nil
}

func (s *ri6State) fundedConsumer(who string) error {
	s.scen["fund:"+who] = 50.0
	return s.fundWho(who, 50)
}

func (s *ri6State) onAir(name, model string) error {
	s.lastModel = model
	s.defaultNode(name, model)
	return nil
}

// --- #2 probes -------------------------------------------------------------------------------

func (s *ri6State) reg(st *fstation) protocol.NodeRegistration {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.nodes[st.id]
}

// canary runs the broker's liveness canary against the station (the production sender).
func (s *ri6State) canary(st *fstation) {
	s.heartbeat()
	round := atomic.AddUint64(&s.b.probe.round, 1) - 1
	s.b.probeNode(s.reg(st), st.model, nextCanary(round))
}

func (s *ri6State) probeSends(name string) error {
	s.mark()
	st := s.ensureNode(name)
	s.canary(st)
	return s.collectUsers(name)
}

func (s *ri6State) collectUsers(names ...string) error {
	s.jobUsers = nil
	for _, n := range names {
		rs := s.newResults(n)
		if len(rs) == 0 {
			return fmt.Errorf("%q received no canary job", n)
		}
		for _, r := range rs {
			s.jobUsers = append(s.jobUsers, resultUser(r))
		}
	}
	return nil
}

func (s *ri6State) userNotProbe() error {
	for _, u := range s.jobUsers {
		if u == "probe" {
			return fmt.Errorf("the canary job's user is %q", u)
		}
	}
	return nil
}

var ri6Pseudonym = regexp.MustCompile(`^u_[0-9a-f]{16}$`)

func (s *ri6State) pseudonymShape() error {
	if len(s.jobUsers) == 0 {
		return fmt.Errorf("no canary job was observed")
	}
	for _, u := range s.jobUsers {
		if !ri6Pseudonym.MatchString(u) {
			return fmt.Errorf("the canary job's user %q is not shaped like a real pseudonym (u_ + 16 hex)", u)
		}
	}
	return nil
}

func (s *ri6State) toolCanary(name, model string) error {
	s.mark()
	st := s.ensureNode(name)
	s.heartbeat()
	s.b.probeToolCall(s.reg(st), model, true)
	return s.collectUsers(name)
}

func (s *ri6State) probeSendsTwo(a, b string) error {
	s.mark()
	s.canary(s.ensureNode(a))
	s.canary(s.ensureNode(b))
	return s.collectUsers(a, b)
}

func (s *ri6State) differentPseudonyms() error {
	if len(s.jobUsers) < 2 {
		return fmt.Errorf("observed %d canary jobs, want 2", len(s.jobUsers))
	}
	if s.jobUsers[0] == s.jobUsers[1] {
		return fmt.Errorf("both stations' canary jobs carry the same user %q", s.jobUsers[0])
	}
	return nil
}

func (s *ri6State) probeSendsN(n, name string) error {
	s.mark()
	st := s.ensureNode(name)
	for i := 0; i < atoiMust(n); i++ {
		s.canary(st)
	}
	s.scen["canaryNode"] = name
	return nil
}

func ri6PromptOf(body []byte) string {
	var m struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &m)
	var sb strings.Builder
	for _, x := range m.Messages {
		sb.WriteString(fmt.Sprint(x.Content))
		sb.WriteString("\n")
	}
	return sb.String()
}

func (s *ri6State) canaryPrompts() []string {
	name, _ := s.scen["canaryNode"].(string)
	var out []string
	for _, b := range s.newBodies(name, false) {
		out = append(out, ri6PromptOf(b))
	}
	return out
}

func (s *ri6State) distinctPrompts(n string) error {
	seen := map[string]bool{}
	ps := s.canaryPrompts()
	for _, p := range ps {
		seen[p] = true
	}
	if len(seen) < atoiMust(n) {
		return fmt.Errorf("%d distinct prompts over %d canaries, want at least %s", len(seen), len(ps), n)
	}
	return nil
}

// noSharedMarker: no word of 5+ characters appears in every canary prompt.
func (s *ri6State) noSharedMarker() error {
	ps := s.canaryPrompts()
	if len(ps) == 0 {
		return fmt.Errorf("no canary prompt was observed")
	}
	words := func(p string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(p), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
			if len(w) >= 5 {
				m[w] = true
			}
		}
		return m
	}
	common := words(ps[0])
	for _, p := range ps[1:] {
		ws := words(p)
		for w := range common {
			if !ws[w] {
				delete(common, w)
			}
		}
	}
	if len(common) > 0 {
		var l []string
		for w := range common {
			l = append(l, w)
		}
		return fmt.Errorf("every canary prompt shares the marker(s) %v", l)
	}
	return nil
}

func (s *ri6State) organicStream(pct, model string) error {
	s.lastModel = model
	n := atoiMust(pct) / 10
	for i := 0; i < 10; i++ {
		s.heartbeat()
		s.do(fa6Spec{who: "u-1", model: model, stream: i < n})
	}
	return nil
}

func (s *ri6State) notVerifiedYet(name string) error {
	st := s.st(name)
	s.b.metricsMu.Lock()
	delete(s.b.trust, st.id)
	s.b.metricsMu.Unlock()
	if s.verifiedNow(st) {
		return fmt.Errorf("precondition: %q is still verified", name)
	}
	return nil
}

func (s *ri6State) earnedVerified(name string) error {
	if !s.verifiedNow(s.st(name)) {
		return fmt.Errorf("%q did not earn verified from the streamed canary", name)
	}
	return nil
}

func (s *ri6State) canaryStreams() error {
	bs := s.newBodies("s1", false)
	if len(bs) == 0 {
		return fmt.Errorf("no canary body was observed")
	}
	if !bytes.Contains(bs[len(bs)-1], []byte(`"stream":true`)) {
		return fmt.Errorf("the canary body is not a stream: %.300s", bs[len(bs)-1])
	}
	return nil
}

var ri6Tool = []map[string]any{{"type": "function", "function": map[string]any{
	"name": "lookup_order", "description": "Look up an order by id.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
}}}

func (s *ri6State) organicTools(model, pct, lo, hi string) error {
	s.lastModel = model
	n := atoiMust(pct) / 10
	span := atoiMust(hi) - atoiMust(lo)
	for i := 0; i < 10; i++ {
		extra := map[string]any{}
		if i < n {
			extra["tools"] = ri6Tool
		}
		s.heartbeat()
		s.do(fa6Spec{who: "u-1", model: model, prompt: atoiMust(lo) + span*i/10, extra: extra})
	}
	return nil
}

func (s *ri6State) someTools() error {
	for _, b := range s.newBodies("s1", false) {
		if bytes.Contains(b, []byte(`"tools"`)) {
			return nil
		}
	}
	return fmt.Errorf("no liveness canary carried a tools array (%d canaries)", len(s.newBodies("s1", false)))
}

func (s *ri6State) somePromptsInBand(lo, hi string) error {
	for _, b := range s.newBodies("s1", false) {
		if n := approxPromptTokens(b); n >= atoiMust(lo) && n <= atoiMust(hi) {
			return nil
		}
	}
	return fmt.Errorf("no canary prompt fell in the %s..%s token band", lo, hi)
}

func (s *ri6State) verifiedNow(st *fstation) bool {
	s.b.metricsMu.Lock()
	tq := s.b.trust[st.id]
	s.b.metricsMu.Unlock()
	return tq.verifiedServing()
}

func (s *ri6State) passesCanaries(name string) error {
	st := s.ensureNode(name)
	s.real(st)
	s.canary(st)
	if !s.verifiedNow(st) {
		return fmt.Errorf("precondition: %q did not earn verified from a passing canary", name)
	}
	return nil
}

// recountStrikes: three distinct payers each relay once while the station over-claims its
// completion and the broker's recount says otherwise.
func (s *ri6State) recountStrikes(name, n string) error {
	st := s.ensureNode(name)
	s.recCompletion = 20
	s.scriptOn(st, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"%s answer"}}],"usage":{"prompt_tokens":50,"completion_tokens":5000}}`, fa6CompletionMarker)
	})
	for i := 0; i < atoiMust(n); i++ {
		who := fmt.Sprintf("payer-%d", i)
		_ = s.fundWho(who, 50)
		s.heartbeat()
		s.do(fa6Spec{who: who, model: st.model, extra: map[string]any{"provider": map[string]any{"only": []string{st.id}}}})
	}
	time.Sleep(200 * time.Millisecond)
	rows, _ := s.db.StrikesByOwner(st.acct, 0)
	got := 0
	for _, r := range rows {
		if r.Kind == store.StrikeRecountDiscrepancy {
			got++
		}
	}
	s.real(st)
	if got < atoiMust(n) {
		return fmt.Errorf("precondition: %d recount strike(s) recorded for %s, want %s", got, name, n)
	}
	return nil
}

func (s *ri6State) evalTrustMin(name string) error {
	st := s.ensureNode(name)
	s.mark()
	s.heartbeat()
	s.do(fa6Spec{who: "u-1", model: st.model, extra: map[string]any{
		"provider": map[string]any{"only": []string{st.id}},
		"roger":    map[string]any{"trust_min": "verified"},
	}})
	s.scen["verified:"+name] = s.sinceMark(name) > 0
	return nil
}

func (s *ri6State) notVerified(name string) error {
	if v, _ := s.scen["verified:"+name].(bool); v {
		return fmt.Errorf("%q still served a trust_min \"verified\" request", name)
	}
	return nil
}

func (s *ri6State) verifiedAgain(name string) error {
	if err := s.evalTrustMin(name); err != nil {
		return err
	}
	if v, _ := s.scen["verified:"+name].(bool); !v {
		return fmt.Errorf("%q did not serve a trust_min \"verified\" request: %d %.200s", name, s.last.code, s.last.body)
	}
	return nil
}

func (s *ri6State) readDiscover() error {
	raw, err := json.Marshal(s.b.computeDiscover())
	if err != nil {
		return err
	}
	var feed struct {
		Offers []map[string]any `json:"offers"`
	}
	if err := json.Unmarshal(raw, &feed); err != nil {
		return err
	}
	s.discover = feed.Offers
	s.scen["discoverRaw"] = raw
	return nil
}

func (s *ri6State) offerOf(name string) (map[string]any, error) {
	if s.discover == nil {
		if err := s.readDiscover(); err != nil {
			return nil, err
		}
	}
	id := s.st(name).id
	for _, o := range s.discover {
		if o["node_id"] == id {
			return o, nil
		}
	}
	return nil, fmt.Errorf("/discover has no offer for %q", name)
}

func (s *ri6State) discoverVerifiedFalse(name string) error {
	s.discover = nil
	o, err := s.offerOf(name)
	if err != nil {
		return err
	}
	if v, _ := o["verified"].(bool); v {
		return fmt.Errorf("/discover shows %q verified true", name)
	}
	return nil
}

func (s *ri6State) organicSuccess(name, rate string) error {
	st := s.ensureNode(name)
	s.b.metricsMu.Lock()
	s.b.success[st.id] = atofMust(rate)
	s.b.metricsMu.Unlock()
	return nil
}

func (s *ri6State) lostVerified(name string) error {
	if err := s.passesCanaries(name); err != nil {
		return err
	}
	if err := s.recountStrikes(name, "3"); err != nil {
		return err
	}
	if err := s.evalTrustMin(name); err != nil {
		return err
	}
	if v, _ := s.scen["verified:"+name].(bool); v {
		return fmt.Errorf("precondition: %q still serves trust_min \"verified\" after 3 organic recount strikes", name)
	}
	return nil
}

func (s *ri6State) windowPassesClean() error {
	s.advance(61 * time.Minute)
	st := s.st("s1")
	s.real(st)
	for i := 0; i < 3; i++ {
		s.heartbeat()
		s.do(fa6Spec{who: "u-1", model: st.model})
	}
	return nil
}

func (s *ri6State) nextCanaryPasses() error {
	s.canary(s.st("s1"))
	return nil
}

func (s *ri6State) noWalletDebited() error {
	want, _ := s.scen["fund:u-1"].(float64)
	if got := s.balanceOf("u-1"); got != want {
		return fmt.Errorf("u-1's balance moved from %.6f to %.6f", want, got)
	}
	for _, u := range s.jobUsers {
		rows, _ := s.db.LedgerOf(u, nil, 10)
		if len(rows) != 0 {
			return fmt.Errorf("the probe identity %q has %d ledger row(s)", u, len(rows))
		}
	}
	return nil
}

func (s *ri6State) noLineage() error {
	ids := map[string]bool{}
	for _, r := range s.newResults("s1") {
		ids[resultID(r)] = true
	}
	es, _ := s.db.RecentByUser(s.consumer("u-1").wallet, 500)
	for _, e := range es {
		if ids[e.RequestID] {
			return fmt.Errorf("u-1's lineage shows the canary %s", e.RequestID)
		}
	}
	return nil
}

// --- #12 pick budget ---------------------------------------------------------------------------

func (s *ri6State) nStations(n, model string) error {
	s.lastModel = model
	for i := 2; i <= atoiMust(n); i++ {
		s.defaultNode(fmt.Sprintf("s%d", i), model)
	}
	return nil
}

func (s *ri6State) relayMaxRequest(who, model, cap string) error {
	s.mark()
	s.heartbeat()
	v, _ := strconv.ParseFloat(cap, 64)
	s.do(fa6Spec{who: who, model: model, extra: map[string]any{"provider": map[string]any{"max_price": map[string]any{"request": v}}}})
	return nil
}

func (s *ri6State) responseIs(code string) error {
	if s.last.code != atoiMust(code) {
		return fmt.Errorf("response %d, want %s: %.300s", s.last.code, code, s.last.body)
	}
	return nil
}

var ri6LogField = map[string]*regexp.Regexp{}

func (s *ri6State) logCount(field string) (int, error) {
	re, ok := ri6LogField[field]
	if !ok {
		re = regexp.MustCompile(`\b` + field + `=(\d+)`)
		ri6LogField[field] = re
	}
	logs := s.logs.String()
	if s.logMark <= len(logs) {
		logs = logs[s.logMark:]
	}
	max, found := 0, false
	for _, m := range re.FindAllStringSubmatch(logs, -1) {
		found = true
		if v := atoiMust(m[1]); v > max {
			max = v
		}
	}
	if !found {
		return 0, fmt.Errorf("no observation point: the relay's log carries no %s= field", field)
	}
	return max, nil
}

func (s *ri6State) atMostPicks(n string) error {
	got, err := s.logCount("picks")
	if err != nil {
		return err
	}
	if got > atoiMust(n) {
		return fmt.Errorf("the routing pass made %d pickFor calls, want at most %s", got, n)
	}
	return nil
}

func (s *ri6State) pickBudget(n string) error {
	s.setenv("ROGERAI_PICK_BUDGET", n)
	return nil
}

func (s *ri6State) nExcludedAfterPick(n, model string) error {
	s.lastModel = model
	for i := 2; i <= atoiMust(n); i++ {
		s.node(fmt.Sprintf("s%d", i), model, 10, 10)
	}
	return nil
}

func ri6Models(list string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(list, -1) {
		out = append(out, m[1])
	}
	return out
}

func (s *ri6State) relayCapModels(who, model, list string) error {
	s.mark()
	s.scen["holdMark"] = s.holdRows(who)
	s.heartbeat()
	s.do(fa6Spec{who: who, model: model, extra: map[string]any{
		"models":   ri6Models(list),
		"provider": map[string]any{"max_price": map[string]any{"request": 1e-12}},
	}})
	return nil
}

func (s *ri6State) holdRows(who string) int {
	rows, _ := s.db.LedgerOf(s.consumer(who).wallet, []string{store.KindHold}, 5000)
	return len(rows)
}

func (s *ri6State) resp503Code(code string) error {
	if s.last.code != 503 || s.errCode(s.last) != code {
		return fmt.Errorf("response %d code %q, want 503 %q: %.300s", s.last.code, s.errCode(s.last), code, s.last.body)
	}
	return nil
}

func (s *ri6State) noHold() error {
	before, _ := s.scen["holdMark"].(int)
	if got := s.holdRows("u-1"); got != before {
		return fmt.Errorf("%d hold row(s) were placed", got-before)
	}
	return nil
}

func (s *ri6State) noStationReceived() error {
	for n := range s.stations {
		if s.sinceMark(n) > 0 {
			return fmt.Errorf("%q received %d request(s)", n, s.sinceMark(n))
		}
	}
	return nil
}

func (s *ri6State) nWithDropped(n, model, dropped string) error {
	s.lastModel = model
	for i := 2; i <= atoiMust(n); i++ {
		s.node(fmt.Sprintf("s%d", i), model, 10, 10)
	}
	// "s1" (Background, $0.10/$0.30) is the one the cap affords; make it the cheapest by far.
	s.b.mu.Lock()
	reg := s.b.nodes[s.st("s1").id]
	reg.Offers[0].PriceIn, reg.Offers[0].PriceOut = 0.01, 0.01
	s.b.nodes[reg.NodeID] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *ri6State) relayThatCap(who, model string) error {
	return s.relayMaxRequest(who, model, "0.01")
}

func (s *ri6State) servedByAffordable() error { return s.servedBy200("s1") }

func (s *ri6State) relayBigBody(who, model string) error {
	s.mark()
	s.heartbeat()
	s.do(fa6Spec{who: who, model: model, prompt: 500_000})
	return nil
}

func (s *ri6State) decodedAtMostTwice() error {
	got, err := s.logCount("body_decodes")
	if err != nil {
		return err
	}
	if got > 2 {
		return fmt.Errorf("the body was JSON-decoded %d times, want at most 2", got)
	}
	return nil
}

func (s *ri6State) relaysFor(who, model string) error {
	s.mark()
	s.heartbeat()
	s.do(fa6Spec{who: who, model: model})
	return nil
}

// --- #13 job ids -----------------------------------------------------------------------------------

func (s *ri6State) relayWithR(who, model string) error {
	if err := s.relaysFor(who, model); err != nil {
		return err
	}
	s.scen["R"] = s.last.hdr.Get("X-RogerAI-Request-Id")
	if s.scen["R"] == "" {
		return fmt.Errorf("the response carries no X-RogerAI-Request-Id (%d)", s.last.code)
	}
	return nil
}

func (s *ri6State) jobIDOf(name string) (string, error) {
	rs := s.newResults(name)
	if len(rs) == 0 {
		return "", fmt.Errorf("%q received no job", name)
	}
	return resultID(rs[len(rs)-1]), nil
}

func (s *ri6State) jobNotR(name string) error {
	id, err := s.jobIDOf(name)
	if err != nil {
		return err
	}
	r, _ := s.scen["R"].(string)
	if id == r || strings.Contains(id, r) {
		return fmt.Errorf("%q received job id %q, which carries the request id %q", name, id, r)
	}
	return nil
}

func (s *ri6State) answersNext429(name string) error {
	st := s.ensureNode(name)
	s.answerFrom(st, false, 429, utDefaultBody(429), nil)
	s.landOrder()
	return nil
}

func (s *ri6State) relayAndServes(who, model, name string) error {
	if err := s.relayWithR(who, model); err != nil {
		return err
	}
	return s.servedBy200(name)
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func (s *ri6State) noCommonPrefix(a, b string) error {
	ia, err := s.jobIDOf(a)
	if err != nil {
		return err
	}
	ib, err := s.jobIDOf(b)
	if err != nil {
		return err
	}
	s.scen["ids"] = [2]string{ia, ib}
	if p := commonPrefix(ia, ib); p > len("att_") {
		return fmt.Errorf("job ids %q and %q share the %d-character prefix %q", ia, ib, p, ia[:p])
	}
	return nil
}

func (s *ri6State) notDerivable() error {
	ids, _ := s.scen["ids"].([2]string)
	r, _ := s.scen["R"].(string)
	for _, id := range ids {
		if !strings.HasPrefix(id, "att_") || len(id) != len("att_")+24 {
			return fmt.Errorf("job id %q is not an att_ + 24-hex per-attempt id", id)
		}
		if strings.Contains(id, r) {
			return fmt.Errorf("job id %q carries the request id", id)
		}
	}
	if strings.Contains(ids[0], ids[1]) || strings.Contains(ids[1], ids[0]) {
		return fmt.Errorf("one job id contains the other: %v", ids)
	}
	return nil
}

func (s *ri6State) deriveTwice() error {
	s.scen["d1"], s.scen["d2"] = s.b.attemptID("R", 2), s.b.attemptID("R", 2)
	return nil
}

func (s *ri6State) derivationsEqual() error {
	if s.scen["d1"] != s.scen["d2"] {
		return fmt.Errorf("the derivations differ: %v vs %v", s.scen["d1"], s.scen["d2"])
	}
	return nil
}

func (s *ri6State) receiptBinds(name string) error {
	id, err := s.jobIDOf(name)
	if err != nil {
		return err
	}
	es, _ := s.db.RecentByUser(s.consumer("u-1").wallet, 500)
	for _, e := range es {
		if e.RequestID == id {
			return nil
		}
	}
	return fmt.Errorf("no settled receipt binds to the job id %q %s received", id, name)
}

func (s *ri6State) settleRecordsR() error {
	r, _ := s.scen["R"].(string)
	if r == "" {
		r = s.last.hdr.Get("X-RogerAI-Request-Id")
		s.scen["R"] = r
	}
	s.getGen(s.consumer("u-1").priv, r)
	if s.genCode != 200 {
		return fmt.Errorf("GET /generation for the request id %q = %d %.200s", r, s.genCode, s.genBody)
	}
	return nil
}

// --- founder ruling 2026-10-05: X-RogerAI-Attempt-Id and the consumer's lineage row ---------------

func (s *ri6State) streamsFor(who, model string) error {
	s.mark()
	s.heartbeat()
	s.do(fa6Spec{who: who, model: model, stream: true})
	return nil
}

func (s *ri6State) attemptHeaderIs(name string) error {
	id, err := s.jobIDOf(name)
	if err != nil {
		return err
	}
	got := s.last.hdr.Get("X-RogerAI-Attempt-Id")
	if got != id {
		return fmt.Errorf("X-RogerAI-Attempt-Id = %q, want the job id %q %s received (%d)", got, id, name, s.last.code)
	}
	s.scen["attempt"] = id
	return nil
}

func (s *ri6State) receiptNamesAttempt() error {
	rec, err := protocol.DecodeReceipt(s.last.hdr.Get("X-RogerAI-Receipt"))
	if err != nil {
		return fmt.Errorf("X-RogerAI-Receipt does not decode: %v", err)
	}
	if want, _ := s.scen["attempt"].(string); rec.RequestID != want {
		return fmt.Errorf("the receipt names %q, want the attempt id %q", rec.RequestID, want)
	}
	return nil
}

func (s *ri6State) consumerRowCarries(name string) error {
	id, err := s.jobIDOf(name)
	if err != nil {
		return err
	}
	s.scen["attempt"], s.scen["attemptNode"] = id, s.st(name).id
	r, _ := s.scen["R"].(string)
	es, err := s.db.RecentByUser(s.consumer("u-1").wallet, 500)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID == id {
			if e.RelayRequestID != r {
				return fmt.Errorf("the consumer's row for %q carries request id %q, want %q", id, e.RelayRequestID, r)
			}
			return nil
		}
	}
	return fmt.Errorf("the consumer has no lineage row for the attempt %q", id)
}

func (s *ri6State) ownerRowCarriesNone() error {
	id, _ := s.scen["attempt"].(string)
	node, _ := s.scen["attemptNode"].(string)
	es, err := s.db.RecentByNode(node, 500)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID == id {
			if e.RelayRequestID != "" {
				return fmt.Errorf("the owner's row for %q carries the request id %q", id, e.RelayRequestID)
			}
			return nil
		}
	}
	return fmt.Errorf("the owner has no lineage row for the attempt %q", id)
}

// --- /generation --------------------------------------------------------------------------------

func (s *ri6State) getGen(priv []byte, id string) {
	r := httptest.NewRequest(http.MethodGet, "/generation?id="+id, nil)
	signReq(r, priv, nil)
	r.Header.Set("CF-Connecting-IP", "198.51.100.77")
	w := httptest.NewRecorder()
	s.b.generation(w, r)
	s.genCode, s.genBody = w.Code, w.Body.Bytes()
}

func (s *ri6State) relayedThroughKey(who, model, _ string, name string) error {
	// No API keys exist on this line (slice 5): the request is a signed relay, so the record's
	// key_id is null; the owner view must still not carry the field at all.
	return s.relayedServed(who, model, name)
}

func (s *ri6State) relayedServed(who, model, name string) error {
	if err := s.relayWithR(who, model); err != nil {
		return err
	}
	return s.servedBy200(name)
}

func (s *ri6State) ownerGets(name string) error {
	st := s.st(name)
	r, _ := s.scen["R"].(string)
	s.getGen(st.ownerPriv, r)
	return nil
}

func (s *ri6State) genRecord() (map[string]any, error) {
	if s.genCode != 200 {
		return nil, fmt.Errorf("GET /generation = %d %.300s", s.genCode, s.genBody)
	}
	var m map[string]any
	if err := json.Unmarshal(s.genBody, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *ri6State) ownerViewLean() error {
	m, err := s.genRecord()
	if err != nil {
		return err
	}
	for _, k := range []string{"key_id", "models", "moderation"} {
		if _, has := m[k]; has {
			return fmt.Errorf("the owner view carries %q: %.300s", k, s.genBody)
		}
	}
	return nil
}

func (s *ri6State) onlyOwnAttempt() error {
	m, err := s.genRecord()
	if err != nil {
		return err
	}
	at, _ := m["attempts"].([]any)
	if len(at) != 1 {
		return fmt.Errorf("the owner view shows %d attempts, want 1", len(at))
	}
	a, _ := at[0].(map[string]any)
	if a["node"] != s.st("s1").id {
		return fmt.Errorf("the attempt shown is %v, not s1's", a["node"])
	}
	return nil
}

func (s *ri6State) consumerGets(who string) error {
	r, _ := s.scen["R"].(string)
	s.getGen(s.consumer(who).priv, r)
	return nil
}

func (s *ri6State) carriesModelsModeration() error {
	m, err := s.genRecord()
	if err != nil {
		return err
	}
	for _, k := range []string{"models", "moderation"} {
		if _, has := m[k]; !has {
			return fmt.Errorf("the consumer view lacks %q: %.300s", k, s.genBody)
		}
	}
	return nil
}

func (s *ri6State) exhaustRelayBucket(who string) error {
	s.realLimiters()
	if err := s.relayWithR(who, s.lastModel); err != nil {
		return err
	}
	w := s.consumer(who)
	for i := 0; i < 100000; i++ {
		if ok, _ := s.b.rl.allow(w.wallet); !ok {
			return nil
		}
	}
	return fmt.Errorf("the relay rate bucket never ran out")
}

func (s *ri6State) getsEarlier(who string) error {
	r, _ := s.scen["R"].(string)
	s.getGen(s.consumer(who).priv, r)
	s.last = fa6Resp{who: who, code: s.genCode, body: s.genBody}
	return nil
}

func (s *ri6State) exhaustGenBucket(who string) error {
	s.realLimiters()
	if err := s.relayWithR(who, s.lastModel); err != nil {
		return err
	}
	r, _ := s.scen["R"].(string)
	for i := 0; i < 2000; i++ {
		s.getGen(s.consumer(who).priv, r)
		if s.genCode == http.StatusTooManyRequests {
			return nil
		}
	}
	return fmt.Errorf("the /generation bucket never ran out")
}

// --- #19 Tower streams --------------------------------------------------------------------------

// tower stands up an approved Tower serving `model` whose upstream runs `h` (a copy of
// rpState.standUpTower with a configurable upstream).
func (s *ri6State) tower(name, model string, h http.HandlerFunc) (*rpTower, error) {
	if err := s.ensureTower(); err != nil {
		return nil, err
	}
	if tw, ok := s.towers[name]; ok {
		return tw, nil
	}
	t, b, srv := s.t, s.b, s.towerSrv
	tw := &rpTower{name: name}
	upstream := httptest.NewServer(h)
	tw.closers = append(tw.closers, upstream.Close)
	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tw.closers = append(tw.closers, func() { _ = hubLn.Close() })
	lt := liveEdgeTower(t, b, srv, name+"-op-"+s.nonce, hubLn.Addr().String())
	tw.id = lt.id
	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, lt.id, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv, SubmitTTL: 90 * time.Second, PollTTL: 500 * time.Millisecond}) // the hub never caps a slow answer here
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()
	nodeOp := signedInOperator(t, b, name+"-node-"+s.nonce)
	shareNodeID := registerShareNode(t, b, nodeOp)
	ctx, cancel := context.WithCancel(context.Background())
	tw.closers = append(tw.closers, cancel)
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{
			NodeID: shareNodeID, Broker: srv.URL, Model: model, Modality: "chat",
			Upstream: upstream.URL, Parallel: 1,
		}, nodeOp.priv, t.TempDir(), io.Discard, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	var stationID string
	for {
		ats, aerr := b.tower.stations.ByTower(lt.id)
		if aerr == nil && len(ats) > 0 {
			hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
			stationID = ats[0].StationID
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("tower %s: the share node never attached", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Priced like the direct stations (an unpriced row is a legacy offer with no data plane).
	routableEdgePriced(t, b, lt.id, stationID, model, liveEndpointOf(t, b, lt.id), 100_000, 300_000)
	rows, err := b.tower.routable.ByTower(lt.id, time.Now())
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("tower %s: no routable row (%v)", name, err)
	}
	tw.nodeID = rows[0].NodeID
	s.towers[name] = tw
	s.b.mu.Lock()
	s.b.trust[tw.nodeID] = trustState{probed: true, probeOK: true, ttftMs: 200} // Tier A, like every station here
	s.b.mu.Unlock()
	_ = s.fundWho("u-1", 400)
	return tw, nil
}

func ri6TowerAnswer(delay time.Duration, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		time.Sleep(delay)
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream overloaded"}}`))
			return
		}
		if bytes.Contains(body, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tower-answer\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"tower-answer"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}
}

func (s *ri6State) towerSlow(name, model, secs string) error {
	s.lastModel = model
	_, err := s.tower(name, model, ri6TowerAnswer(time.Duration(atoiMust(secs))*time.Second, 200))
	return err
}

func (s *ri6State) towerKeepaliveFail(name, model string) error {
	s.lastModel = model
	_, err := s.tower(name, model, ri6TowerAnswer(12*time.Second, 503))
	return err
}

func (s *ri6State) idOf(name string) string {
	if tw, ok := s.towers[name]; ok {
		return tw.id // a Tower is named by its Tower id, like rpState.idOf
	}
	return s.st(name).id
}

func (s *ri6State) streamsOrder(who, model, list string) error {
	var ids []string
	for _, n := range ri6Models(list) {
		ids = append(ids, s.idOf(n))
	}
	s.mark()
	s.heartbeat()
	s.do(fa6Spec{who: who, model: model, stream: true, extra: map[string]any{"provider": map[string]any{"order": ids}}})
	return nil
}

func (s *ri6State) keepalivesBefore(n string) error {
	body := string(s.last.body)
	ans := strings.Index(body, "tower-answer")
	if ans < 0 {
		return fmt.Errorf("the Tower's answer never arrived (%d): %.300s", s.last.code, body)
	}
	if got := strings.Count(body[:ans], ": rogerai keepalive"); got < atoiMust(n) {
		return fmt.Errorf("%d keepalive comment(s) arrived before the answer, want at least %s", got, n)
	}
	return nil
}

func (s *ri6State) answerUsageDone() error {
	body := string(s.last.body)
	a, u, d := strings.Index(body, "tower-answer"), strings.Index(body, `"usage"`), strings.Index(body, "[DONE]")
	if a < 0 || u < a || d < u {
		return fmt.Errorf("answer at %d, usage at %d, [DONE] at %d: not in order: %.400s", a, u, d, body)
	}
	return nil
}

func (s *ri6State) directOnAir(name, model string) error { return s.onAir(name, model) }

func (s *ri6State) failsOverBeforeContent(name string) error {
	if s.last.code != 200 || s.sinceMark(name) == 0 {
		return fmt.Errorf("the stream (%d) did not fail over to %s: %.300s", s.last.code, name, s.last.body)
	}
	if strings.Contains(string(s.last.body), "tower-answer") {
		return fmt.Errorf("Tower content reached the consumer")
	}
	return nil
}

func (s *ri6State) towerTTFTTotal(name, model, ttft, total string) error {
	s.lastModel = model
	tw, err := s.tower(name, model, ri6TowerAnswer(0, 200))
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	tq := s.b.trust[tw.nodeID]
	tq.ttftMs = atofMust(ttft)
	s.b.trust[tw.nodeID] = tq
	s.b.mu.Unlock()
	// No measurement surface holds a total latency today; the scenario's figure is kept so a
	// GREEN seam can feed it.
	s.scen["total:"+name] = atoiMust(total)
	return nil
}

func (s *ri6State) directTTFTTotal(name, ttft, total string) error {
	s.setTTFT(s.ensureNode(name), atoiMust(ttft))
	s.scen["total:"+name] = atoiMust(total)
	return nil
}

func (s *ri6State) relaySort(who, model, sortBy string) error {
	s.mark()
	s.heartbeat()
	s.do(fa6Spec{who: who, model: model, extra: map[string]any{"provider": map[string]any{"sort": sortBy}}})
	return nil
}

func (s *ri6State) planHead(name string) error {
	if _, tower := s.towers[name]; tower {
		if s.last.hdr.Get("X-RogerAI-Relay") == "" {
			return fmt.Errorf("the plan head is a direct station, want %s", name)
		}
		return nil
	}
	if s.last.hdr.Get("X-RogerAI-Relay") != "" {
		return fmt.Errorf("the plan head was the Tower (%s), want %s", s.last.hdr.Get("X-RogerAI-Relay"), name)
	}
	return s.servedBy200(name)
}

func (s *ri6State) servedThroughTower(name string) error {
	if s.last.code != 200 || s.last.hdr.Get("X-RogerAI-Relay") == "" || !strings.Contains(string(s.last.body), "tower-answer") {
		return fmt.Errorf("the stream (%d, relay %q) was not served through %s: %.300s", s.last.code, s.last.hdr.Get("X-RogerAI-Relay"), name, s.last.body)
	}
	return nil
}

// bridgedDeadline reads the window the relay logged for the bridged stream attempt
// ("window_s=N") and compares it with a direct stream attempt's (the stream idle window).
func (s *ri6State) bridgedDeadline() error {
	got, err := s.logCount("window_s")
	if err != nil {
		return err
	}
	if want := int(s.b.streamIdle().Seconds()); got != want {
		return fmt.Errorf("the bridged attempt's window is %ds, a direct stream attempt's is %ds", got, want)
	}
	return nil
}

// --- #21 attribute sources ---------------------------------------------------------------------

func (s *ri6State) mutateReg(name string, f func(*protocol.NodeRegistration)) {
	st := s.ensureNode(name)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	f(&reg)
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
}

func (s *ri6State) declaresAll(name, region, quant, params, ctx string) error {
	s.mutateReg(name, func(r *protocol.NodeRegistration) {
		r.Region = region
		r.Offers[0].Quant = quant
		r.Offers[0].ParamsB = atofMust(params)
		r.Offers[0].Ctx = atoiMust(ctx)
		r.Offers[0].CtxEstimated = false
	})
	return nil
}

func (s *ri6State) getDiscover() error { s.discover = nil; return s.readDiscover() }

var ri6Pair = regexp.MustCompile(`([a-z_]+) "([^"]+)"`)

func (s *ri6State) attributeSources(name, spec string) error {
	o, err := s.offerOf(name)
	if err != nil {
		return err
	}
	src, ok := o["attribute_sources"].(map[string]any)
	if !ok {
		return fmt.Errorf("the %q offer carries no attribute_sources: %v", name, o)
	}
	for _, m := range ri6Pair.FindAllStringSubmatch(spec, -1) {
		if got := fmt.Sprint(src[m[1]]); got != m[2] {
			return fmt.Errorf("attribute_sources.%s = %q, want %q", m[1], got, m[2])
		}
	}
	return nil
}

func (s *ri6State) onAirNoParams(name, model string) error {
	s.lastModel = model
	s.defaultNode(name, model)
	s.mutateReg(name, func(r *protocol.NodeRegistration) { r.Offers[0].ParamsB = 0 })
	return nil
}

func (s *ri6State) toolsVision(name, model string) error {
	st := s.ensureNode(name)
	s.mutateReg(name, func(r *protocol.NodeRegistration) { r.Offers[0].Capabilities = []string{"vision"} })
	// Earned the real way: a passing tool-call canary verdict, mirrored to the shared store
	// like production's (writing toolsOK alone skips the shared verdict multi-instance reads).
	s.b.recordToolProbe(st.id, model, true, false, true)
	return nil
}

func (s *ri6State) getModels() error {
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	s.b.routes().ServeHTTP(w, r)
	s.scen["models"] = w.Body.Bytes()
	if w.Code != 200 {
		return fmt.Errorf("GET /v1/models = %d", w.Code)
	}
	return nil
}

func (s *ri6State) modelsAttrSources(model string) error {
	raw, _ := s.scen["models"].([]byte)
	var d struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &d)
	for _, e := range d.Data {
		if e["id"] == model {
			rb, _ := e["rogerai"].(map[string]any)
			if _, ok := rb["attribute_sources"].(map[string]any); !ok {
				return fmt.Errorf("the %q entry's rogerai block carries no attribute_sources: %v", model, rb)
			}
			return nil
		}
	}
	return fmt.Errorf("/v1/models has no %q entry", model)
}

const (
	ri6NA      = "3.208.0.1"
	ri6Europe  = "52.28.0.1"
	ri6Unknown = "203.0.113.5"
)

func (s *ri6State) setAddr(name, ip string) {
	st := s.ensureNode(name)
	s.b.mu.Lock()
	if s.b.netBucket == nil {
		s.b.netBucket = map[string]string{}
	}
	s.b.netBucket[st.id] = coarseNetBucket(ip)
	s.b.mu.Unlock()
	s.scen["ip:"+name] = ip
}

func (s *ri6State) declaresRegion(name, region string) error {
	s.mutateReg(name, func(r *protocol.NodeRegistration) { r.Region = region })
	return nil
}

func (s *ri6State) addrNA(name string) error { s.setAddr(name, ri6NA); return nil }

func (s *ri6State) relayRegion(who, model, region string) error {
	s.mark()
	s.heartbeat()
	s.do(fa6Spec{who: who, model: model, extra: map[string]any{"roger": map[string]any{"region": []string{region}}}})
	return nil
}

func (s *ri6State) notCandidate(name string) error {
	if s.sinceMark(name) > 0 {
		return fmt.Errorf("%q received the request (%d)", name, s.last.code)
	}
	return nil
}

func (s *ri6State) isCandidate(name string) error {
	if s.sinceMark(name) == 0 {
		return fmt.Errorf("%q did not receive the request: %d %.200s", name, s.last.code, s.last.body)
	}
	return nil
}

func ri6Find(v any, key string) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		if got, ok := x[key]; ok {
			return got, true
		}
		for _, c := range x {
			if got, ok := ri6Find(c, key); ok {
				return got, true
			}
		}
	case []any:
		for _, c := range x {
			if got, ok := ri6Find(c, key); ok {
				return got, true
			}
		}
	}
	return nil, false
}

func (s *ri6State) adminRegionMismatch(name string) error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	v, ok := ri6Find(s.adminResp, "region_mismatch")
	if !ok {
		return fmt.Errorf("/admin/live carries no region_mismatch")
	}
	if !strings.Contains(fmt.Sprint(v), s.st(name).id) {
		return fmt.Errorf("/admin/live region_mismatch does not list %q: %v", name, v)
	}
	return nil
}

func (s *ri6State) regionContradicted(name string) (bool, error) {
	s.discover = nil
	o, err := s.offerOf(name)
	if err != nil {
		return false, err
	}
	src, _ := o["attribute_sources"].(map[string]any)
	return o["region"] == "contradicted" || fmt.Sprint(src["region"]) == "contradicted" || o["region_contradicted"] == true, nil
}

func (s *ri6State) discoverContradicted(name string) error {
	c, err := s.regionContradicted(name)
	if err != nil {
		return err
	}
	if !c {
		return fmt.Errorf("/discover does not mark %q's region contradicted", name)
	}
	return nil
}

func (s *ri6State) regionEurope(name, region string) error {
	_ = s.declaresRegion(name, region)
	s.setAddr(name, ri6Europe)
	return nil
}

func (s *ri6State) regionUnknown(name, region string) error {
	_ = s.declaresRegion(name, region)
	s.setAddr(name, ri6Unknown)
	return nil
}

func (s *ri6State) contradicted(name string) error {
	_ = s.declaresRegion(name, "eu")
	s.setAddr(name, ri6NA)
	return nil
}

func (s *ri6State) curatedRegion(name, provider string) error {
	st := s.defaultNode(name, s.lastModel)
	s.curate(st, provider)
	_ = s.declaresRegion(name, provider)
	s.setAddr(name, ri6NA)
	return nil
}

func (s *ri6State) notMarkedContradicted(name string) error {
	c, err := s.regionContradicted(name)
	if err != nil {
		return err
	}
	if c {
		return fmt.Errorf("%q's region is marked contradicted", name)
	}
	return nil
}

func (s *ri6State) discoverAndAdmin() error {
	if err := s.getDiscover(); err != nil {
		return err
	}
	if err := s.readAdminLive(); err != nil {
		return err
	}
	s.admin, _ = json.Marshal(s.adminResp)
	return nil
}

func (s *ri6State) noAddressShown(name string) error {
	ip, _ := s.scen["ip:"+name].(string)
	bucket := coarseNetBucket(ip)
	raw, _ := s.scen["discoverRaw"].([]byte)
	for what, blob := range map[string][]byte{"/discover": raw, "/admin/live": s.admin} {
		for _, needle := range []string{ip, bucket, strings.TrimSuffix(bucket, "/24")} {
			if needle != "" && bytes.Contains(blob, []byte(needle)) {
				return fmt.Errorf("%s shows %q", what, needle)
			}
		}
	}
	return nil
}

// --- runner -----------------------------------------------------------------------------------------

func TestRoutingIntegrityBDD(t *testing.T) {
	logs := &utLog{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &ri6State{fa6State: &fa6State{rpState: &rpState{foState: &foState{t: t, logs: logs}}}}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.reset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})
			ri6Register(sc, st)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/security/routing_integrity.feature"},
			Tags: "~@cli && ~@tui && ~@proxy && ~@docs && ~@later", TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing_integrity.feature failed")
	}
}

func ri6Register(sc *godog.ScenarioContext, st *ri6State) {
	sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
	sc.Step(`^the fee rate is 30%$`, st.feeRate30)
	sc.Step(`^station "([^"]+)" is on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M$`, st.stationPriced)
	sc.Step(`^"([^"]+)" is a funded consumer$`, st.fundedConsumer)

	// #2
	sc.Step(`^the probe sends its canary to "([^"]+)"$`, st.probeSends)
	sc.Step(`^the job's user is not "probe"$`, st.userNotProbe)
	sc.Step(`^the job's user matches the pattern of a real pseudonym "u_" followed by 16 hex characters$`, st.pseudonymShape)
	sc.Step(`^the tool-call canary is sent to "([^"]+)" for "([^"]+)"$`, st.toolCanary)
	sc.Step(`^it matches the pattern of a real pseudonym$`, st.pseudonymShape)
	sc.Step(`^station "([^"]+)" is on air for "([^"]+)"$`, st.onAir)
	sc.Step(`^the probe sends its canary to "([^"]+)" and to "([^"]+)"$`, st.probeSendsTwo)
	sc.Step(`^the two jobs carry different pseudonyms$`, st.differentPseudonyms)
	sc.Step(`^the probe sends (\d+) canaries to "([^"]+)"$`, st.probeSendsN)
	sc.Step(`^at least (\d+) distinct prompts were used$`, st.distinctPrompts)
	sc.Step(`^no prompt contains a fixed marker string shared by all of them$`, st.noSharedMarker)
	sc.Step(`^(\d+)% of recent organic traffic for "([^"]+)" was stream:true$`, st.organicStream)
	sc.Step(`^the canary job's body has stream true$`, st.canaryStreams)
	sc.Step(`^"([^"]+)" has not been verified yet$`, st.notVerifiedYet)
	sc.Step(`^"([^"]+)" earned verified from that canary$`, st.earnedVerified)
	sc.Step(`^recent organic traffic for "([^"]+)" carries tools in (\d+)% of requests with prompts of (\d+) to (\d+) tokens$`, st.organicTools)
	sc.Step(`^some canaries carry a tools array$`, st.someTools)
	sc.Step(`^some canary prompts fall in the (\d+) to (\d+) token band$`, st.somePromptsInBand)
	sc.Step(`^"([^"]+)" passes every canary$`, st.passesCanaries)
	sc.Step(`^"([^"]+)" accrues (\d+) recount strikes from organic relays within 1 hour$`, st.recountStrikes)
	sc.Step(`^routing evaluates trust_min "verified" for "([^"]+)"$`, st.evalTrustMin)
	sc.Step(`^"([^"]+)" is not verified$`, st.notVerified)
	sc.Step(`^/discover shows "([^"]+)" verified false$`, st.discoverVerifiedFalse)
	sc.Step(`^"([^"]+)"'s organic success rate over the last hour is ([0-9.]+)$`, st.organicSuccess)
	sc.Step(`^"([^"]+)" lost verified because of organic contradictions$`, st.lostVerified)
	sc.Step(`^the contradiction window passes with clean organic relays$`, st.windowPassesClean)
	sc.Step(`^the next canary passes$`, st.nextCanaryPasses)
	sc.Step(`^"([^"]+)" is verified again$`, st.verifiedAgain)
	sc.Step(`^no wallet was debited$`, st.noWalletDebited)
	sc.Step(`^no consumer's /console lineage shows the canary$`, st.noLineage)

	// #12
	sc.Step(`^(\d+) stations are on air for "([^"]+)"$`, st.nStations)
	sc.Step(`^"([^"]+)" relays for "([^"]+)" with provider\.max_price\.request ([0-9.]+)$`, st.relayMaxRequest)
	sc.Step(`^the response is (\d+)$`, st.responseIs)
	sc.Step(`^the routing pass made at most (\d+) pickFor calls$`, st.atMostPicks)
	sc.Step(`^ROGERAI_PICK_BUDGET is "(\d+)"$`, st.pickBudget)
	sc.Step(`^(\d+) stations are on air for "([^"]+)", each excluded by the per-request cap only after being picked$`, st.nExcludedAfterPick)
	sc.Step(`^"([^"]+)" relays for "([^"]+)" with a per-request cap and models \[(.+)\]$`, st.relayCapModels)
	sc.Step(`^the response is 503 with error code "([^"]+)"$`, st.resp503Code)
	sc.Step(`^no hold was placed$`, st.noHold)
	sc.Step(`^no station received anything$`, st.noStationReceived)
	sc.Step(`^(\d+) stations are on air for "([^"]+)" and (\d+) are dropped by the per-request cap$`, st.nWithDropped)
	sc.Step(`^"([^"]+)" relays for "([^"]+)" with that per-request cap$`, st.relayThatCap)
	sc.Step(`^the response is 200 from the one station the cap affords$`, st.servedByAffordable)
	sc.Step(`^"([^"]+)" relays for "([^"]+)" with a 2 MiB body$`, st.relayBigBody)
	sc.Step(`^the request body was JSON-decoded at most twice \(routing object and token estimate\)$`, st.decodedAtMostTwice)
	sc.Step(`^"([^"]+)" relays for "([^"]+)"$`, st.relaysFor)

	// #13
	sc.Step(`^"([^"]+)" relays for "([^"]+)" and the response carries X-RogerAI-Request-Id "R"$`, st.relayWithR)
	sc.Step(`^the job id "([^"]+)" received is not "R" and does not contain "R"$`, st.jobNotR)
	sc.Step(`^"([^"]+)" answers the next request with an upstream 429$`, st.answersNext429)
	sc.Step(`^"([^"]+)" relays for "([^"]+)" and "([^"]+)" serves$`, st.relayAndServes)
	sc.Step(`^the job ids received by "([^"]+)" and "([^"]+)" share no common prefix longer than "att_"$`, st.noCommonPrefix)
	sc.Step(`^neither job id is derivable from the other without the broker secret$`, st.notDerivable)
	sc.Step(`^the broker derives the job id for request "R" attempt 2 twice$`, st.deriveTwice)
	sc.Step(`^the response's X-RogerAI-Attempt-Id is the job id "([^"]+)" received$`, st.attemptHeaderIs)
	sc.Step(`^the receipt in X-RogerAI-Receipt names that attempt id$`, st.receiptNamesAttempt)
	sc.Step(`^"([^"]+)" streams for "([^"]+)"$`, st.streamsFor)
	sc.Step(`^the consumer's lineage row for the job id "([^"]+)" received carries request id "R"$`, st.consumerRowCarries)
	sc.Step(`^the owner's lineage row for that job id carries no request id$`, st.ownerRowCarriesNone)
	sc.Step(`^both derivations are equal$`, st.derivationsEqual)
	sc.Step(`^the receipt binds to the per-attempt job id "([^"]+)" received$`, st.receiptBinds)
	sc.Step(`^the settle records the request id "R" for the consumer$`, st.settleRecordsR)
	sc.Step(`^"([^"]+)" relayed for "([^"]+)" through key "([^"]+)" and "([^"]+)" served$`, st.relayedThroughKey)
	sc.Step(`^the owner of "([^"]+)" GETs /generation for that request$`, st.ownerGets)
	sc.Step(`^the record has no "key_id", no "models" and no "moderation"$`, st.ownerViewLean)
	sc.Step(`^it shows only the owner's own attempt$`, st.onlyOwnAttempt)
	sc.Step(`^"([^"]+)" relayed for "([^"]+)" and "([^"]+)" served$`, st.relayedServed)
	sc.Step(`^"([^"]+)" GETs /generation for that request$`, st.consumerGets)
	sc.Step(`^the record carries "models" and "moderation"$`, st.carriesModelsModeration)
	sc.Step(`^"([^"]+)" has exhausted its relay rate bucket$`, st.exhaustRelayBucket)
	sc.Step(`^"([^"]+)" GETs /generation for an earlier request$`, st.getsEarlier)
	sc.Step(`^"([^"]+)" has exhausted its /generation rate bucket$`, st.exhaustGenBucket)

	// #19
	sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)" and takes (\d+) seconds to answer$`, st.towerSlow)
	sc.Step(`^"([^"]+)" streams for "([^"]+)" with provider\.order \[(.+)\]$`, st.streamsOrder)
	sc.Step(`^at least (\d+) ": rogerai keepalive" comments arrive before the answer$`, st.keepalivesBefore)
	sc.Step(`^the answer, the usage chunk and "\[DONE\]" follow in order$`, st.answerUsageDone)
	sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)", sends keepalives, then fails with 503$`, st.towerKeepaliveFail)
	sc.Step(`^direct station "([^"]+)" is on air for "([^"]+)"$`, st.directOnAir)
	sc.Step(`^the stream fails over to "([^"]+)" before any content frame$`, st.failsOverBeforeContent)
	sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)" with TTFT (\d+) ms and total latency (\d+) s$`, st.towerTTFTTotal)
	sc.Step(`^direct station "([^"]+)" has TTFT (\d+) ms and total latency (\d+) s$`, st.directTTFTTotal)
	sc.Step(`^"([^"]+)" relays for "([^"]+)" with provider\.sort "([^"]+)"$`, st.relaySort)
	sc.Step(`^the plan head is "([^"]+)"$`, st.planHead)
	sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)" and answers in (\d+) seconds$`, st.towerSlow)
	sc.Step(`^the stream is served through "([^"]+)"$`, st.servedThroughTower)
	sc.Step(`^the bridged attempt's deadline equals a direct stream attempt's$`, st.bridgedDeadline)

	// #21
	sc.Step(`^"([^"]+)" declares region "([^"]+)", quant "([^"]+)", params_b (\d+) and a context window of (\d+)$`, st.declaresAll)
	sc.Step(`^a consumer GETs /discover$`, st.getDiscover)
	sc.Step(`^the "([^"]+)" offer has attribute_sources (.+)$`, st.attributeSources)
	sc.Step(`^station "([^"]+)" is on air for "([^"]+)" with no declared params_b$`, st.onAirNoParams)
	sc.Step(`^"([^"]+)" earned verified "tools" for "([^"]+)" and declares "vision"$`, st.toolsVision)
	sc.Step(`^a consumer GETs /v1/models$`, st.getModels)
	sc.Step(`^the "([^"]+)" entry's rogerai block carries attribute_sources$`, st.modelsAttrSources)
	sc.Step(`^"([^"]+)" declares region "([^"]+)"$`, st.declaresRegion)
	sc.Step(`^"([^"]+)"'s address falls in a network bucket that maps to North America$`, st.addrNA)
	sc.Step(`^"([^"]+)" relays for "([^"]+)" with roger\.region \["([^"]+)"\]$`, st.relayRegion)
	sc.Step(`^"([^"]+)" is not a candidate$`, st.notCandidate)
	sc.Step(`^/admin/live region_mismatch lists "([^"]+)"$`, st.adminRegionMismatch)
	sc.Step(`^/discover marks "([^"]+)"'s region "contradicted"$`, st.discoverContradicted)
	sc.Step(`^"([^"]+)" declares region "([^"]+)" and its address maps to Europe$`, st.regionEurope)
	sc.Step(`^"([^"]+)" is a candidate$`, st.isCandidate)
	sc.Step(`^"([^"]+)" declares region "([^"]+)" and its address maps to no known continent$`, st.regionUnknown)
	sc.Step(`^"([^"]+)"'s declared region is contradicted by its network$`, st.contradicted)
	sc.Step(`^curated station "([^"]+)" whose region is its provider "([^"]+)"$`, st.curatedRegion)
	sc.Step(`^"([^"]+)"'s region is not marked "contradicted"$`, st.notMarkedContradicted)
	sc.Step(`^a consumer GETs /discover and /admin/live$`, st.discoverAndAdmin)
	sc.Step(`^neither shows "([^"]+)"'s IP address or network bucket$`, st.noAddressShown)
}
