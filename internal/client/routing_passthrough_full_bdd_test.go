package client

// routing_passthrough_full_bdd_test.go makes the scenarios of
// features/proxy/routing_passthrough.feature that are NOT tagged @slice0 EXECUTABLE (the
// @slice0 ones stay in routing_passthrough_bdd_test.go). It reuses that runner's state
// (rpNegState: the REAL Use, with its seams capturing the assembled ProxyOptions and handler,
// and the REAL ProxyHandlerLive) and replaces its broker stand-in with a richer recording one.
//
// The broker is a recording httptest stand-in (the approved seam for proxy specs: the broker
// is a separate binary, and the proxy's contract is the HTTP it emits and how it reacts). It:
//   - records every chat attempt (headers, body);
//   - answers /discover from a per-scenario offer list, /bands/resolve for the owner's band
//     code, /balance, and GET /v1/models with the scripted status;
//   - answers each chat attempt from a per-scenario script (transport error = the connection
//     is hijacked and closed; a station-attributed failure = 502 with X-RogerAI-Provider; a
//     band_cooling / no_match / 429 / 400 reply), else 200 (JSON, or SSE for a stream);
//   - plays the broker's DOCUMENTED refusals for three shapes only, stated so nothing is
//     hidden: an unknown key under roger/provider -> 400 unknown_routing_key; roger.params_b
//     with min > max or an unknown roger.pref -> 400 invalid_routing_value; provider not an
//     object -> 400 invalid_routing_value. The broker-side truth of those refusals is pinned
//     by cmd/rogerai-broker's request_shape runner; here they are what the proxy must relay.
//
// Owner flags the session options do not have yet (--max-cost --trust --region --only
// --exclude --models) are set by field name on UseOptions; a missing field fails the Given
// naming it, which is the RED for "the owner's ceiling" scenarios built on them.
// Profiles live in config.json under a temp XDG_CONFIG_HOME (contract §9: profiles are
// client-side config the proxy resolves).
//
// A chat step is sent lazily, just before the next Then (or the next chat), so the scripting
// steps written after it ("And the first relay attempt fails with a transport error") still
// apply to it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"
	"rogerai.fm/roger/v6/internal/operator"
)

type cf4pResp struct {
	cost       string // X-RogerAI-Cost on a 200 ("" = 0.0001)
	transport  bool
	status     int
	provider   string
	retryAfter string
	body       string
	sse        string
}

type cf4pState struct {
	*rpNegState

	stand        *httptest.Server
	offers       []map[string]any
	script       map[int]cf4pResp // by attempt index (1-based) within the scenario
	attemptN     int
	discoverHits int

	ownerExtra map[string]any // UseOptions fields by name
	quantRule  []string
	ownerFreq  string

	pending     string
	pendingSet  bool
	lastCarrier string
	lastBody    string
	mark        int // attempts index when the last chat was sent
	sentAt      time.Time
	elapsed     time.Duration
	spentBefore float64
	chatted     bool

	cfgDir    string
	cfgBefore []byte
	alerts    []string

	launch    operator.Launch
	launchErr error
}

func (s *cf4pState) reset(t *testing.T) {
	s.rpNegState = &rpNegState{t: t}
	s.rpNegState.reset()
	s.srv.Close()
	s.script, s.attemptN, s.discoverHits = map[int]cf4pResp{}, 0, 0
	s.ownerExtra, s.quantRule, s.ownerFreq = map[string]any{}, nil, ""
	s.pending, s.pendingSet, s.lastCarrier, s.lastBody = "", false, "", ""
	s.mark, s.elapsed, s.spentBefore, s.chatted, s.alerts = 0, 0, 0, false, nil
	// One feed for every scenario: n2 is the best-signal station (the re-pick whenever it
	// is not excluded); only n-cur and n-ok are tools-capable, and n-cur is curated, so n-ok is
	// the only re-pick that honors require tools + self_hosted_only; n3 serves another model.
	s.offers = []map[string]any{
		{"node_id": "n1", "model": "qwen3-32b-fp8", "online": true, "price_in": 0.1, "price_out": 0.5, "tps": 30.0, "signal": 90},
		{"node_id": "n2", "model": "qwen3-32b-fp8", "online": true, "price_in": 0.1, "price_out": 0.5, "tps": 28.0, "signal": 95},
		{"node_id": "n-cur", "model": "qwen3-32b-fp8", "online": true, "price_in": 0.1, "price_out": 0.4, "tps": 26.0, "signal": 80, "capabilities": []string{"tools"}, "curated": true, "curated_provider": "openrouter"},
		{"node_id": "n-plain", "model": "qwen3-32b-fp8", "online": true, "price_in": 0.1, "price_out": 0.3, "tps": 25.0, "signal": 70},
		{"node_id": "n-ok", "model": "qwen3-32b-fp8", "online": true, "price_in": 0.1, "price_out": 0.5, "tps": 20.0, "signal": 60, "capabilities": []string{"tools"}},
		{"node_id": "n3", "model": "llama-3.3-70b", "online": true, "price_in": 0.1, "price_out": 0.5, "tps": 30.0, "signal": 80},
	}
	s.cfgDir = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", s.cfgDir)
	s.stand = httptest.NewServer(http.HandlerFunc(s.serveStand))
	s.srv = s.stand
}

func (s *cf4pState) cleanup() {
	s.rpNegState.cleanup()
}

var cf4pRogerKeys = map[string]bool{"pref": true, "require": true, "params_b": true, "min_ctx": true, "min_tps": true, "max_ttft_ms": true,
	"trust_min": true, "self_hosted_only": true, "confidential": true, "region": true, "freq": true, "profile": true}
var cf4pProviderKeys = map[string]bool{"order": true, "only": true, "ignore": true, "allow_fallbacks": true, "sort": true,
	"quantizations": true, "max_price": true, "require_parameters": true}

func cf4pRefusal(body map[string]any) (string, string) {
	if p, ok := body["provider"]; ok {
		pm, isObj := p.(map[string]any)
		if !isObj {
			return "invalid_routing_value", "provider must be an object"
		}
		for k := range pm {
			if !cf4pProviderKeys[k] {
				return "unknown_routing_key", "unknown routing key provider." + k
			}
		}
	}
	if r, ok := body["roger"].(map[string]any); ok {
		for k := range r {
			if !cf4pRogerKeys[k] {
				return "unknown_routing_key", "unknown routing key roger." + k
			}
		}
		if pb, ok := r["params_b"].([]any); ok && len(pb) == 2 {
			a, _ := pb[0].(float64)
			b, _ := pb[1].(float64)
			if a > b {
				return "invalid_routing_value", "roger.params_b min > max"
			}
		}
		if p, ok := r["pref"].(string); ok {
			switch p {
			case "cheap", "balanced", "fast", "reliable":
			default:
				return "invalid_routing_value", "roger.pref " + p
			}
		}
	}
	return "", ""
}

func (s *cf4pState) serveStand(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
		w.WriteHeader(s.modelsStatus)
		if s.modelsStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		}
	case r.URL.Path == "/discover":
		s.mu.Lock()
		s.discoverHits++
		offers := s.offers
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": offers})
	case r.URL.Path == "/bands/resolve":
		var req struct{ Freq string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if s.ownerFreq != "" && req.Freq == s.ownerFreq {
			_ = json.NewEncoder(w).Encode(map[string]any{"offers": []map[string]any{
				{"node_id": "nb", "model": s.model, "online": true, "price_in": 0.1, "price_out": 0.5, "tps": 30.0, "signal": 80}},
				"band": map[string]any{"display": "private band"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"offers":[]}`))
	case r.URL.Path == "/balance":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":10}`))
	case r.URL.Path == "/v1/chat/completions":
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.attempts = append(s.attempts, rpAttempt{at: time.Now(), headers: r.Header.Clone(), body: body, raw: raw})
		s.attemptN++
		resp, scripted := s.script[s.attemptN]
		s.mu.Unlock()
		if scripted {
			s.answer(w, resp)
			return
		}
		if code, msg := cf4pRefusal(body); code != "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-RogerAI-Cost", "0")
			w.WriteHeader(http.StatusBadRequest)
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": msg}})
			_, _ = w.Write(b)
			return
		}
		if stream, _ := body["stream"].(bool); stream {
			s.answer(w, cf4pResp{sse: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"})
			return
		}
		s.answer(w, cf4pResp{})
	default:
		http.NotFound(w, r)
	}
}

func (s *cf4pState) answer(w http.ResponseWriter, r cf4pResp) {
	if r.transport {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
				return
			}
		}
	}
	prov := r.provider
	if prov == "" {
		prov = "n1"
	}
	w.Header().Set("X-RogerAI-Provider", prov)
	if r.retryAfter != "" {
		w.Header().Set("Retry-After", r.retryAfter)
	}
	if r.sse != "" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-RogerAI-Cost", "0")
		_, _ = w.Write([]byte(r.sse))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.status == 0 || r.status == 200 {
		cost := r.cost
		if cost == "" {
			cost = "0.0001"
		}
		w.Header().Set("X-RogerAI-Cost", cost)
		b := r.body
		if b == "" {
			b = `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
		}
		_, _ = w.Write([]byte(b))
		return
	}
	w.Header().Set("X-RogerAI-Cost", "0")
	w.WriteHeader(r.status)
	b := r.body
	if b == "" {
		b = `{"error":{"message":"upstream failed"}}`
	}
	_, _ = w.Write([]byte(b))
}

// ── tune ───────────────────────────────────────────────────────────────────────────────

var cf4pFieldOf = map[string]string{
	"--max-cost": "MaxCost", "--trust": "Trust", "--region": "Region", "--only": "Only",
	"--exclude": "ExcludeNodes", "--models": "Models",
}

func (s *cf4pState) ownerOpts() (UseOptions, error) {
	o := UseOptions{Port: 1, MaxOut: s.ownerMaxOut, MaxIn: s.ownerMaxIn, MinTPS: s.ownerMinTPS, Yes: true, Freq: s.ownerFreq}
	if len(s.quantRule) > 0 {
		o.Quantizations = RuleQuantizations(s.quantRule)
	}
	if v, ok := s.ownerExtra["Confidential"]; ok {
		o.Confidential = v.(bool)
	}
	if v, ok := s.ownerExtra["SelfHostedOnly"]; ok {
		o.SelfHostedOnly = v.(bool)
	}
	rv := reflect.ValueOf(&o).Elem()
	for name, v := range s.ownerExtra {
		if name == "Confidential" || name == "SelfHostedOnly" {
			continue
		}
		f := rv.FieldByName(name)
		if !f.IsValid() {
			return o, fmt.Errorf("UseOptions has no %s field: the owner's flag is not a session option yet", name)
		}
		val := reflect.ValueOf(v)
		if !val.Type().ConvertibleTo(f.Type()) {
			return o, fmt.Errorf("UseOptions.%s is %s, cannot take %T", name, f.Type(), v)
		}
		f.Set(val.Convert(f.Type()))
	}
	return o, nil
}

func (s *cf4pState) tune() error {
	o, err := s.ownerOpts()
	if err != nil {
		return err
	}
	s.tuned = false
	if err := Use(s.srv.URL, "u", s.model, o); err != nil {
		return fmt.Errorf("Use: %v", err)
	}
	if !s.tuned {
		return fmt.Errorf("Use did not open the channel (no handler built)")
	}
	cur := s.holder.Get()
	cur.Alert = func(msg string) { s.alerts = append(s.alerts, msg) }
	s.holder.SetBand(cur)
	return nil
}

func (s *cf4pState) ensureTuned() error {
	if s.handler != nil {
		return nil
	}
	return s.tune()
}

// ── chats ──────────────────────────────────────────────────────────────────────────────

const cf4pMsgs = `[{"role":"user","content":"hi"}]`

var (
	cf4pModelAnd   = regexp.MustCompile(`^model "([^"]*)" and (.+)$`)
	cf4pModelOnly  = regexp.MustCompile(`^model "([^"]*)"$`)
	cf4pNoModelAnd = regexp.MustCompile(`^no model field and (.+)$`)
	cf4pCarrierAnd = regexp.MustCompile(`^("[^"]+":.+) and model "([^"]*)"$`)
)

// bodyFor builds the guest body for "a chat request arrives with ...".
func (s *cf4pState) bodyFor(text string) (string, error) {
	enc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	switch {
	case text == "no routing carrier":
		return `{"model":"anything","messages":` + cf4pMsgs + `}`, nil
	case text == "no routing carrier and the owner set only --max-out 2":
		s.ownerMinTPS = 0
		return `{"model":"anything","messages":` + cf4pMsgs + `}`, nil
	case text == "a carrier":
		s.lastCarrier = `"roger": {"pref": "fast"}`
		return `{"model":` + enc(s.model) + `,"messages":` + cf4pMsgs + `,"roger":{"pref":"fast"}}`, nil
	case strings.HasPrefix(text, "tools, tool_choice, response_format"):
		return `{"model":` + enc(s.model) + `,"messages":` + cf4pMsgs + `,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":"auto","response_format":{"type":"json_object"},"temperature":0.2,"stream":true,"roger":{"pref":"fast"}}`, nil
	}
	if m := cf4pNoModelAnd.FindStringSubmatch(text); m != nil {
		s.lastCarrier = m[1]
		return `{"messages":` + cf4pMsgs + `,` + m[1] + `}`, nil
	}
	if m := cf4pCarrierAnd.FindStringSubmatch(text); m != nil {
		s.lastCarrier = m[1]
		return `{"model":` + enc(m[2]) + `,"messages":` + cf4pMsgs + `,` + m[1] + `}`, nil
	}
	if m := cf4pModelAnd.FindStringSubmatch(text); m != nil {
		if m[2] == "no routing carrier" {
			return `{"model":` + enc(m[1]) + `,"messages":` + cf4pMsgs + `}`, nil
		}
		s.lastCarrier = m[2]
		return `{"model":` + enc(m[1]) + `,"messages":` + cf4pMsgs + `,` + m[2] + `}`, nil
	}
	if m := cf4pModelOnly.FindStringSubmatch(text); m != nil {
		return `{"model":` + enc(m[1]) + `,"messages":` + cf4pMsgs + `}`, nil
	}
	if strings.HasPrefix(text, `"`) {
		s.lastCarrier = text
		return `{"model":` + enc(s.model) + `,"messages":` + cf4pMsgs + `,` + text + `}`, nil
	}
	return "", fmt.Errorf("cannot build a chat body from %q", text)
}

func (s *cf4pState) queue(body string) error {
	if s.pendingSet {
		if err := s.flush(); err != nil {
			return err
		}
	}
	s.pending, s.pendingSet = body, true
	return nil
}

func (s *cf4pState) flush() error {
	if !s.pendingSet {
		return nil
	}
	body := s.pending
	s.pending, s.pendingSet = "", false
	if err := s.ensureTuned(); err != nil {
		return err
	}
	s.mu.Lock()
	s.mark = len(s.attempts)
	s.mu.Unlock()
	s.spentBefore = s.holder.Spent()
	s.lastBody = body
	s.sentAt = time.Now()
	err := s.chat(body)
	s.elapsed = time.Since(s.sentAt)
	s.chatted = true
	return err
}

func (s *cf4pState) ensureChatted() error {
	if err := s.flush(); err != nil {
		return err
	}
	if !s.chatted {
		if err := s.queue(`{"model":"anything","messages":` + cf4pMsgs + `}`); err != nil {
			return err
		}
		return s.flush()
	}
	return nil
}

func (s *cf4pState) chatArrives(text string) error {
	b, err := s.bodyFor(text)
	if err != nil {
		return err
	}
	return s.queue(b)
}

func (s *cf4pState) bareChat() error {
	return s.queue(`{"model":"anything","messages":` + cf4pMsgs + `}`)
}

func (s *cf4pState) streamingChat(carrier string) error {
	enc, _ := json.Marshal(s.model)
	s.lastCarrier = carrier
	return s.queue(`{"model":` + string(enc) + `,"stream":true,"messages":` + cf4pMsgs + `,` + carrier + `}`)
}

func (s *cf4pState) bigChat() error {
	big := strings.Repeat("x", 4<<20+1024)
	return s.queue(`{"model":"qwen3-32b-fp8","messages":` + cf4pMsgs + `,"roger":{"pref":"fast","note":"` + big + `"}}`)
}

func (s *cf4pState) firstChatOfSession() error { return s.bareChat() }

// ── attempts ───────────────────────────────────────────────────────────────────────────

func (s *cf4pState) since() []rpAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mark > len(s.attempts) {
		return nil
	}
	return append([]rpAttempt(nil), s.attempts[s.mark:]...)
}

func (s *cf4pState) nth(n int) (rpAttempt, error) {
	a := s.since()
	if len(a) < n {
		return rpAttempt{}, fmt.Errorf("%d attempt(s) reached the broker, want at least %d (guest got %d: %s)", len(a), n, s.rec.Code, s.rec.Body.String())
	}
	return a[n-1], nil
}

// ── clause checks (the same reading the CLI runner uses) ───────────────────────────────

func cf4pWalk(m any, path string) (any, bool) {
	cur := m
	for _, seg := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func cf4pCanon(v any) string {
	b, _ := json.Marshal(v)
	var back any
	_ = json.Unmarshal(b, &back)
	c, _ := json.Marshal(back)
	return string(c)
}

func cf4pValue(text string) any {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &v) == nil {
		return v
	}
	return strings.TrimSpace(text)
}

var (
	cf4pClauseStart = regexp.MustCompile(`^(no |[a-z_][a-z0-9_.]*( =| "| \[| \{| true| false| -?[0-9]|$))`)
	cf4pParenTail   = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	cf4pAssign      = regexp.MustCompile(`^([a-z_][a-z0-9_.]*) = (.+)$`)
	cf4pBare        = regexp.MustCompile(`^([a-z_][a-z0-9_.]*) ("|\[|\{|true|false|-?[0-9])(.*)$`)
)

func cf4pClauses(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		for _, sep := range []string{", and ", ", ", " and "} {
			if strings.HasPrefix(text[i:], sep) && cf4pClauseStart.MatchString(text[i+len(sep):]) {
				out = append(out, strings.TrimSpace(text[start:i]))
				start = i + len(sep)
				i = start - 1
				break
			}
		}
	}
	return append(out, strings.TrimSpace(text[start:]))
}

func cf4pCheck(a rpAttempt, text string) error {
	text = cf4pParenTail.ReplaceAllString(strings.TrimSpace(text), "") // a trailing "(note)" is commentary
	for _, c := range cf4pClauses(text) {
		c = cf4pParenTail.ReplaceAllString(c, "")
		if c == "" {
			continue
		}
		var path, val string
		if m := cf4pAssign.FindStringSubmatch(c); m != nil {
			path, val = m[1], m[2]
		} else if m := cf4pBare.FindStringSubmatch(c); m != nil {
			path, val = m[1], m[2]+m[3]
		} else {
			return fmt.Errorf("cannot read clause %q", c)
		}
		want := cf4pValue(val)
		got, ok := cf4pWalk(a.body, path)
		if !ok {
			return fmt.Errorf("the broker received no %s (want %s); body %s", path, cf4pCanon(want), a.raw)
		}
		if cf4pCanon(got) != cf4pCanon(want) {
			return fmt.Errorf("the broker received %s = %s, want %s; body %s", path, cf4pCanon(got), cf4pCanon(want), a.raw)
		}
	}
	return nil
}

// subset: every leaf of the fragment is present, equal, in the body.
func cf4pSubset(frag map[string]any, body map[string]any, prefix string) error {
	for k, v := range frag {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		got, ok := body[k]
		if !ok {
			return fmt.Errorf("the broker received no %s", path)
		}
		if fm, isObj := v.(map[string]any); isObj && len(fm) > 0 {
			gm, ok := got.(map[string]any)
			if !ok {
				return fmt.Errorf("the broker received %s = %s, want an object", path, cf4pCanon(got))
			}
			if err := cf4pSubset(fm, gm, path); err != nil {
				return err
			}
			continue
		}
		if cf4pCanon(got) != cf4pCanon(v) {
			return fmt.Errorf("the broker received %s = %s, want %s", path, cf4pCanon(got), cf4pCanon(v))
		}
	}
	return nil
}

func (s *cf4pState) brokerReceives(text string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	a, err := s.nth(1)
	if err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(text, " unchanged") || text == "it unchanged":
		frag := strings.TrimSuffix(text, " unchanged")
		if frag == "it" {
			frag = s.lastCarrier
		}
		var fm map[string]any
		if err := json.Unmarshal([]byte("{"+frag+"}"), &fm); err != nil {
			return fmt.Errorf("cannot read carrier %q: %v", frag, err)
		}
		if err := cf4pSubset(fm, a.body, ""); err != nil {
			return fmt.Errorf("%v (body %s)", err, a.raw)
		}
		return nil
	case text == "each of those fields byte-identical":
		var sent, got map[string]json.RawMessage
		_ = json.Unmarshal([]byte(s.lastBody), &sent)
		_ = json.Unmarshal(a.raw, &got)
		for _, k := range []string{"tools", "tool_choice", "response_format", "temperature", "stream"} {
			if !bytes.Equal(bytes.TrimSpace(sent[k]), bytes.TrimSpace(got[k])) {
				return fmt.Errorf("%s changed: sent %s, broker got %s", k, sent[k], got[k])
			}
		}
		return nil
	case text == "the owner's freq":
		body, hdr := string(a.raw), a.headers.Get("X-Roger-Freq")
		if hdr != s.ownerFreq && !strings.Contains(body, s.ownerFreq) {
			return fmt.Errorf("the broker did not receive the owner's freq (X-Roger-Freq %q, body %s)", hdr, a.raw)
		}
		if strings.Contains(body, "ZZZZ-ZZZZ") || strings.Contains(hdr, "ZZZZ-ZZZZ") {
			return fmt.Errorf("the guest's freq reached the broker (X-Roger-Freq %q, body %s)", hdr, a.raw)
		}
		return nil
	case text == "the owner's default caps":
		want := s.ownerMaxOut
		if want == 0 {
			want = ConsumerDefaultMaxOut
		}
		return cf4pCheck(a, fmt.Sprintf("provider.max_price.completion = %g", want))
	case strings.HasPrefix(text, "exactly ") && strings.HasSuffix(text, " as the routing object"):
		inner := strings.TrimSuffix(strings.TrimPrefix(text, "exactly "), " as the routing object")
		if err := cf4pCheck(a, inner); err != nil {
			return err
		}
		var leaves int
		for _, k := range []string{"models", "provider", "roger"} {
			if v, ok := a.body[k]; ok {
				leaves += cf4pLeafCount(v)
			}
		}
		if leaves != 1 {
			return fmt.Errorf("the routing object has %d leaves, want exactly %s: %s", leaves, inner, a.raw)
		}
		return nil
	}
	return cf4pCheck(a, text)
}

func cf4pLeafCount(v any) int {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		n := 0
		for _, vv := range m {
			n += cf4pLeafCount(vv)
		}
		return n
	}
	return 1
}

// ── Given ──────────────────────────────────────────────────────────────────────────────

var cf4pFlag = regexp.MustCompile(`--([a-z-]+)(?: ("[^"]*"|[^ -][^ ]*))?`)

func (s *cf4pState) ownerTunedWith(flags string) error {
	if flags == "no caps at all" {
		s.ownerMaxOut, s.ownerMaxIn, s.ownerMinTPS = 0, 0, 0
		return nil
	}
	for _, m := range cf4pFlag.FindAllStringSubmatch(flags, -1) {
		name, val := "--"+m[1], strings.Trim(m[2], `"`)
		switch name {
		case "--max-out":
			s.ownerMaxOut, _ = strconv.ParseFloat(val, 64)
		case "--min-tps":
			s.ownerMinTPS, _ = strconv.ParseFloat(val, 64)
		case "--max-in":
			s.ownerMaxIn, _ = strconv.ParseFloat(val, 64)
		case "--confidential":
			s.ownerExtra["Confidential"] = true
		case "--self-hosted":
			s.ownerExtra["SelfHostedOnly"] = true
		case "--freq":
			s.ownerFreq = val
		case "--max-cost":
			f, _ := strconv.ParseFloat(val, 64)
			s.ownerExtra[cf4pFieldOf[name]] = f
		case "--trust":
			s.ownerExtra[cf4pFieldOf[name]] = val
		case "--region", "--only", "--exclude", "--models":
			s.ownerExtra[cf4pFieldOf[name]] = strings.Split(val, ",")
		default:
			return fmt.Errorf("unknown owner flag %s", name)
		}
	}
	// A field the session options do not have fails here, at the owner's tune.
	_, err := s.ownerOpts()
	return err
}

func (s *cf4pState) ownerQuantRule(list string) error {
	return json.Unmarshal([]byte(list), &s.quantRule)
}

func (s *cf4pState) writeProfile(name string, prof map[string]any) error {
	p := filepath.Join(s.cfgDir, "rogerai", "config.json")
	m := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	profs, _ := m["profiles"].(map[string]any)
	if profs == nil {
		profs = map[string]any{}
	}
	profs[name] = prof
	m["profiles"] = profs
	_ = os.MkdirAll(filepath.Dir(p), 0700)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, b, 0600)
}

var cf4pAssignSplit = regexp.MustCompile(` and ([a-z_][a-z0-9_.]* = )`)

func (s *cf4pState) profileSets(name, text string) error {
	text = strings.TrimSpace(text)
	prof := map[string]any{}
	if strings.HasPrefix(text, "only ") && strings.HasPrefix(strings.TrimPrefix(text, "only "), `"`) {
		if err := json.Unmarshal([]byte("{"+strings.TrimPrefix(text, "only ")+"}"), &prof); err != nil {
			return err
		}
		return s.writeProfile(name, prof)
	}
	text = strings.TrimPrefix(text, "only ")
	parts := cf4pAssignSplit.Split(text, -1)
	keys := cf4pAssignSplit.FindAllStringSubmatch(text, -1)
	assigns := []string{parts[0]}
	for i, k := range keys {
		assigns = append(assigns, k[1]+parts[i+1])
	}
	for _, a := range assigns {
		m := cf4pAssign.FindStringSubmatch(strings.TrimSpace(a))
		if m == nil {
			return fmt.Errorf("cannot read profile assignment %q", a)
		}
		segs := strings.Split(m[1], ".")
		cur := prof
		for _, seg := range segs[:len(segs)-1] {
			next, _ := cur[seg].(map[string]any)
			if next == nil {
				next = map[string]any{}
				cur[seg] = next
			}
			cur = next
		}
		cur[segs[len(segs)-1]] = cf4pValue(m[2])
	}
	return s.writeProfile(name, prof)
}

func (s *cf4pState) budgetSpent(budget, spent string) error {
	if err := s.ensureTuned(); err != nil {
		return err
	}
	b, _ := strconv.ParseFloat(budget, 64)
	sp, _ := strconv.ParseFloat(spent, 64)
	s.holder.SetBudget(b)
	s.holder.addSpend(sp)
	// The served turn costs enough to cross the ceiling (the crossing turn completes; the next
	// one is refused), as plate_budget.feature describes.
	s.script[s.attemptN+1] = cf4pResp{cost: "0.05"}
	return nil
}

func (s *cf4pState) firstAttemptTransportAt(node string) error {
	// A transport error carries no station name; the proxy learns the failed station only from
	// a station-attributed failure, so "at station X" is played as a 502 naming X.
	s.script[s.attemptN+1] = cf4pResp{status: 502, provider: node}
	return nil
}

func (s *cf4pState) firstAttemptTransport() error {
	s.script[s.attemptN+1] = cf4pResp{transport: true}
	return nil
}

func (s *cf4pState) nodeFailsTransport(node string) error { return s.firstAttemptTransport() }

func (s *cf4pState) repicks(node string) error {
	return s.queue(`{"model":"anything","messages":` + cf4pMsgs + `}`)
}

func (s *cf4pState) retryBodyCarries(text string) error {
	if !s.chatted && !s.pendingSet {
		// Given form: a session whose retry already carries that order (attempt 1 failed at n1).
		s.script[1] = cf4pResp{status: 502, provider: "n1"}
		return nil
	}
	if err := s.ensureChatted(); err != nil {
		return err
	}
	a, err := s.nth(2)
	if err != nil {
		return err
	}
	return cf4pCheck(a, text)
}

func (s *cf4pState) retryNoNoFallback() error {
	a, err := s.nth(2)
	if err != nil {
		return err
	}
	if v, ok := cf4pWalk(a.body, "provider.allow_fallbacks"); ok && v == false {
		return fmt.Errorf("the retry carries provider.allow_fallbacks false: %s", a.raw)
	}
	return nil
}

func (s *cf4pState) noNodeHeader() error {
	for _, a := range s.since() {
		if v := a.headers.Get("X-Roger-Node"); v != "" {
			return fmt.Errorf("an attempt carries X-Roger-Node %q", v)
		}
	}
	return nil
}

func (s *cf4pState) n2429n3Eligible(a, b string) error {
	s.script[2] = cf4pResp{provider: b}
	return s.queue(`{"model":"anything","messages":` + cf4pMsgs + `}`)
}

func (s *cf4pState) brokerServesFrom(node string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if s.rec.Code != 200 {
		return fmt.Errorf("the guest saw %d: %s", s.rec.Code, s.rec.Body.String())
	}
	if got := s.rec.Header().Get("X-RogerAI-Provider"); got != node {
		return fmt.Errorf("served by %q, want %q", got, node)
	}
	return nil
}

func (s *cf4pState) providerNames(node string) error { return s.brokerServesFrom(node) }

func (s *cf4pState) noRepick() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if n := len(s.since()); n != 1 {
		return fmt.Errorf("%d attempts reached the broker, want exactly 1 (no re-pick)", n)
	}
	return nil
}

func (s *cf4pState) guestLastErrorOpenAI() error {
	if s.rec.Code < 400 {
		return fmt.Errorf("guest status %d, want the broker's last error", s.rec.Code)
	}
	var e struct {
		Error struct{ Message string } `json:"error"`
	}
	if json.Unmarshal(s.rec.Body.Bytes(), &e) != nil || e.Error.Message == "" {
		return fmt.Errorf("the error is not OpenAI-shaped: %s", s.rec.Body.String())
	}
	return nil
}

func (s *cf4pState) brokerAnswersCode(status, code, ra string) error {
	st, _ := strconv.Atoi(status)
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": code}})
	s.script[s.attemptN+1] = cf4pResp{status: st, retryAfter: ra, body: string(body)}
	return nil
}

func (s *cf4pState) brokerAnswersBandCooling(code, ra string) error {
	return s.brokerAnswersCode("503", code, ra)
}

func (s *cf4pState) brokerAnswersNoMatch(code string) error {
	return s.brokerAnswersCode("503", code, "")
}

func (s *cf4pState) brokerAnswers429(ra string) error {
	// The broker's 429 body is an OpenAI rate_limit_error (approved retry_after.feature).
	s.script[s.attemptN+1] = cf4pResp{status: 429, retryAfter: ra, body: `{"error":{"message":"rate limited","type":"rate_limit_error"}}`}
	return nil
}

func (s *cf4pState) brokerAnswers400(msg string) error {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
	s.script[s.attemptN+1] = cf4pResp{status: 400, body: string(b)}
	return nil
}

func (s *cf4pState) proxyWaitsRetries(secs string) error {
	return s.queue(`{"model":"anything","messages":` + cf4pMsgs + `}`)
}

func (s *cf4pState) brokerThenServes() error { return nil } // attempt 2 answers 200 by default

func (s *cf4pState) guestSees200() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if s.rec.Code != 200 {
		return fmt.Errorf("guest saw %d: %s", s.rec.Code, s.rec.Body.String())
	}
	return nil
}

func (s *cf4pState) guestNeverSaw503() error {
	if s.rec.Code == 503 {
		return fmt.Errorf("the guest saw the 503")
	}
	a := s.since()
	if len(a) != 2 {
		return fmt.Errorf("%d attempts, want the 503 then exactly one retry", len(a))
	}
	if gap := a[1].at.Sub(a[0].at); gap < 2900*time.Millisecond {
		return fmt.Errorf("the retry came %v after the 503, want the 3 s wait", gap)
	}
	return nil
}

func (s *cf4pState) guestReceivesStatusRA(status, ra string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if strconv.Itoa(s.rec.Code) != status {
		return fmt.Errorf("guest status %d, want %s: %s", s.rec.Code, status, s.rec.Body.String())
	}
	if got := s.rec.Header().Get("Retry-After"); ra != "" && got != ra {
		return fmt.Errorf("Retry-After %q, want %q", got, ra)
	}
	var e struct {
		Error struct {
			Message, Type string
		} `json:"error"`
	}
	if json.Unmarshal(s.rec.Body.Bytes(), &e) != nil || e.Error.Message == "" {
		return fmt.Errorf("not OpenAI-shaped: %s", s.rec.Body.String())
	}
	return nil
}

func (s *cf4pState) guest503RA(ra string) error { return s.guestReceivesStatusRA("503", ra) }

func (s *cf4pState) guest429RA(ra string) error {
	if err := s.guestReceivesStatusRA("429", ra); err != nil {
		return err
	}
	if !strings.Contains(s.rec.Body.String(), "rate_limit_error") {
		return fmt.Errorf("the 429 is not a rate_limit_error: %s", s.rec.Body.String())
	}
	return nil
}

func (s *cf4pState) proxyDoesNotWait() error {
	if s.elapsed > time.Second {
		return fmt.Errorf("the proxy took %v (it waited)", s.elapsed)
	}
	return nil
}

func (s *cf4pState) guest503Immediately() error {
	if err := s.guestReceivesStatusRA("503", ""); err != nil {
		return err
	}
	if n := len(s.since()); n != 1 {
		return fmt.Errorf("%d attempts, want 1 (no retry)", n)
	}
	return s.proxyDoesNotWait()
}

func (s *cf4pState) discoverMatches(model string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	a, err := s.nth(2)
	if err != nil {
		return err
	}
	order, _ := cf4pWalk(a.body, "provider.order")
	l, _ := order.([]any)
	if len(l) == 0 {
		return fmt.Errorf("the retry names no re-picked station: %s", a.raw)
	}
	id, _ := l[0].(string)
	for _, o := range s.offers {
		if o["node_id"] == id {
			if o["model"] != model {
				return fmt.Errorf("the re-pick %s serves %v, not %q", id, o["model"], model)
			}
			return nil
		}
	}
	return fmt.Errorf("the re-pick %s is not on /discover", id)
}

func (s *cf4pState) repickToolsNotCurated() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	a, err := s.nth(2)
	if err != nil {
		return err
	}
	order, _ := cf4pWalk(a.body, "provider.order")
	l, _ := order.([]any)
	if len(l) == 0 {
		return fmt.Errorf("the retry names no re-picked station: %s", a.raw)
	}
	for _, o := range s.offers {
		if o["node_id"] == l[0] {
			caps, _ := o["capabilities"].([]string)
			tools := false
			for _, c := range caps {
				tools = tools || c == "tools"
			}
			if !tools || o["curated"] == true {
				return fmt.Errorf("the re-pick %v is not a tools-capable, non-curated station (tools=%v curated=%v): %s", l[0], tools, o["curated"], a.raw)
			}
			return nil
		}
	}
	return fmt.Errorf("the re-pick %v is not on /discover", l[0])
}

func (s *cf4pState) failsThenServedBy(node string) error {
	s.script[1] = cf4pResp{status: 502, provider: "n1"}
	s.script[2] = cf4pResp{provider: node}
	return nil
}

func (s *cf4pState) alertReads(want string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	for _, a := range s.alerts {
		if a == want {
			return nil
		}
	}
	return fmt.Errorf("alerts %q, want %q", s.alerts, want)
}

func (s *cf4pState) routingDefaultsHeader(want string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if got := s.rec.Header().Get("X-Roger-Routing-Defaults"); got != want {
		return fmt.Errorf("X-Roger-Routing-Defaults = %q, want %q", got, want)
	}
	return nil
}

func (s *cf4pState) guest413Nothing() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if s.rec.Code != http.StatusRequestEntityTooLarge {
		return fmt.Errorf("guest status %d, want 413: %.200s", s.rec.Code, s.rec.Body.String())
	}
	return s.nothingReached()
}

func (s *cf4pState) bodyOneObject() error {
	a, err := s.nth(1)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(a.raw, &m); err != nil {
		return fmt.Errorf("the body sent to the broker is not one JSON object: %v", err)
	}
	return nil
}

func (s *cf4pState) carriesBodyObject() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	a, err := s.nth(1)
	if err != nil {
		return err
	}
	if _, ok := a.body["roger"]; !ok {
		if _, ok := a.body["provider"]; !ok {
			return fmt.Errorf("the first relay carries no routing body object: %s", a.raw)
		}
	}
	return nil
}

func (s *cf4pState) carriesNoHeaders(list string) error {
	a, err := s.nth(1)
	if err != nil {
		return err
	}
	for _, h := range regexp.MustCompile(`X-Roger-[A-Za-z-]+`).FindAllString(list, -1) {
		if v := a.headers.Get(h); v != "" {
			return fmt.Errorf("the first relay carries %s = %q", h, v)
		}
	}
	return nil
}

func (s *cf4pState) stillMaxPriceOut() error {
	a, err := s.nth(1)
	if err != nil {
		return err
	}
	if a.headers.Get("X-Roger-Max-Price-Out") == "" {
		return fmt.Errorf("X-Roger-Max-Price-Out is missing")
	}
	return nil
}

func (s *cf4pState) sessionInBodyMode() error {
	if err := s.ensureTuned(); err != nil {
		return err
	}
	if s.holder.Get().HeaderRouting {
		return fmt.Errorf("the session is in header mode")
	}
	return nil
}

func (s *cf4pState) guest400ModeUnchanged() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if s.rec.Code != 400 {
		return fmt.Errorf("guest status %d, want 400", s.rec.Code)
	}
	if s.holder.Get().HeaderRouting {
		return fmt.Errorf("the 400 flipped the session to header mode")
	}
	return nil
}

func (s *cf4pState) cfgPath() string { return filepath.Join(s.cfgDir, "rogerai", "config.json") }

func (s *cf4pState) negotiatedHeaderMode() error {
	s.cfgBefore, _ = os.ReadFile(s.cfgPath())
	s.modelsStatus = http.StatusNotFound
	return s.tune()
}

func (s *cf4pState) configUnchanged() error {
	after, _ := os.ReadFile(s.cfgPath())
	if !bytes.Equal(after, s.cfgBefore) {
		return fmt.Errorf("config.json changed:\nbefore %s\nafter  %s", s.cfgBefore, after)
	}
	return nil
}

func (s *cf4pState) tunedBandAny() error { return nil }

func (s *cf4pState) probeModels() error {
	if err := s.ensureTuned(); err != nil {
		return err
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+s.sessionKey)
	s.rec = httptest.NewRecorder()
	s.handler.ServeHTTP(s.rec, req)
	return nil
}

func (s *cf4pState) oneEntryList() error {
	if s.rec.Code != 200 {
		return fmt.Errorf("GET /v1/models = %d", s.rec.Code)
	}
	var l struct {
		Data []struct{ ID string } `json:"data"`
	}
	_ = json.Unmarshal(s.rec.Body.Bytes(), &l)
	if len(l.Data) != 1 || l.Data[0].ID != s.model {
		return fmt.Errorf("the proxy's list is %+v, want exactly [%s]", l.Data, s.model)
	}
	return nil
}

func (s *cf4pState) brokers400Returned(code string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if s.rec.Code != 400 {
		return fmt.Errorf("guest status %d, want the broker's 400: %s", s.rec.Code, s.rec.Body.String())
	}
	var e struct {
		Error struct {
			Message string
			Code    any
		} `json:"error"`
	}
	if json.Unmarshal(s.rec.Body.Bytes(), &e) != nil || e.Error.Message == "" {
		return fmt.Errorf("the 400 is not OpenAI-shaped: %s", s.rec.Body.String())
	}
	if code != "" && !strings.Contains(s.rec.Body.String(), code) {
		return fmt.Errorf("the 400 does not carry %s: %s", code, s.rec.Body.String())
	}
	return nil
}

func (s *cf4pState) brokers400NoCode() error { return s.brokers400Returned("") }

func (s *cf4pState) noHeaderFallback() error {
	if n := len(s.since()); n != 1 {
		return fmt.Errorf("%d attempts: a header fallback was attempted", n)
	}
	return nil
}

func (s *cf4pState) brokers400SpendUnchanged() error {
	if err := s.brokers400NoCode(); err != nil {
		return err
	}
	if sp := s.holder.Spent(); sp != s.spentBefore {
		return fmt.Errorf("session spend moved %v -> %v on a 400", s.spentBefore, sp)
	}
	return nil
}

func (s *cf4pState) requestServed() error { return s.guestSees200() }

func (s *cf4pState) nextRefused402() error {
	before := len(s.attempts)
	if err := s.queue(`{"model":"anything","messages":` + cf4pMsgs + `}`); err != nil {
		return err
	}
	if err := s.flush(); err != nil {
		return err
	}
	if s.rec.Code != http.StatusPaymentRequired || !strings.Contains(s.rec.Body.String(), "budget_exceeded") {
		return fmt.Errorf("the next request got %d: %s, want 402 budget_exceeded", s.rec.Code, s.rec.Body.String())
	}
	if len(s.attempts) != before {
		return fmt.Errorf("the refused request reached the broker")
	}
	return nil
}

func (s *cf4pState) streamSettles(cost string) error {
	sse := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6,"cost":` + cost + `,"rogerai":{"receipt":"r"}}}` + "\n\n" +
		": rogerai-cost=" + cost + "\n\n" + "data: [DONE]\n\n"
	s.script[s.attemptN+1] = cf4pResp{sse: sse}
	enc, _ := json.Marshal(s.model)
	return s.queue(`{"model":` + string(enc) + `,"stream":true,"messages":` + cf4pMsgs + `}`)
}

func (s *cf4pState) spendIncreasesOnce(cost string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	want, _ := strconv.ParseFloat(cost, 64)
	if d := s.holder.Spent() - s.spentBefore; d < want-1e-9 || d > want+1e-9 {
		return fmt.Errorf("session spend increased by %v, want exactly %v", d, want)
	}
	return nil
}

func (s *cf4pState) commentNotDoubled() error { return s.spendIncreasesOnce("0.0123") }

func (s *cf4pState) guest400(msg string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if s.rec.Code != http.StatusBadRequest {
		return fmt.Errorf("status = %d, want 400; body=%s", s.rec.Code, s.rec.Body.String())
	}
	var e struct {
		Error struct{ Type, Message string } `json:"error"`
	}
	if json.Unmarshal(s.rec.Body.Bytes(), &e) != nil || e.Error.Type == "" {
		return fmt.Errorf("400 body is not OpenAI-shaped: %s", s.rec.Body.String())
	}
	if !strings.Contains(e.Error.Message, msg) {
		return fmt.Errorf("400 message %q does not contain %q", e.Error.Message, msg)
	}
	return nil
}

func (s *cf4pState) nothingReached() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	if n := len(s.since()); n != 0 {
		return fmt.Errorf("%d chat request(s) reached the broker, want none", n)
	}
	return nil
}

func (s *cf4pState) primaryStaysBand() error {
	a, err := s.nth(1)
	if err != nil {
		return err
	}
	if a.body["model"] != s.model {
		return fmt.Errorf("the primary is %v, want the band model %s", a.body["model"], s.model)
	}
	return nil
}

func (s *cf4pState) guestCodeNeverLogged() error {
	if strings.Contains(s.logBuf.String(), "ZZZZ-ZZZZ") {
		return fmt.Errorf("the guest's code was logged")
	}
	return nil
}

func (s *cf4pState) neverEmptyQuant() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.attempts {
		if q, ok := cf4pWalk(a.body, "provider.quantizations"); ok {
			if l, _ := q.([]any); len(l) == 0 {
				return fmt.Errorf("the proxy sent provider.quantizations = []: %s", a.raw)
			}
		}
	}
	return nil
}

func (s *cf4pState) guestNoError() error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	return s.rpNegState.guestNoError()
}

func (s *cf4pState) logLineSays(want string) error {
	if err := s.ensureChatted(); err != nil {
		return err
	}
	return s.rpNegState.logLineSays(want)
}

// ── guest launches (internal/operator's real materializers) ────────────────────────────

func (s *cf4pState) materialize(name, model string) error {
	var g operator.Guest
	for _, x := range operator.Registry() {
		if x.Name == name {
			g = x
		}
	}
	if g.Name == "" {
		return fmt.Errorf("no registered guest %q", name)
	}
	l, cleanup, err := operator.Materialize(g, operator.Session{
		BaseURL: "http://127.0.0.1:44017/v1", SessionKey: s.sessionKey, Model: model, ScratchRoot: s.cfgDir,
	})
	if err != nil {
		return fmt.Errorf("materialize %s with model %q: %v", name, model, err)
	}
	if cleanup != nil {
		s.t.Cleanup(func() { _ = cleanup() })
	}
	s.launch = l
	return nil
}

func (s *cf4pState) launchMaterialized(name string) error { return s.materialize(name, s.model) }

func (s *cf4pState) launchMaterializedWithModel(name, model string) error {
	return s.materialize(name, model)
}

// The spec marks the opencode option for extra body fields UNVERIFIED against the binary; it
// is an assumption about the guest, not something this repo can observe, so the step states
// it and checks only that a launch exists to carry it.
func (s *cf4pState) opencodePassesExtra() error {
	if len(s.launch.Argv) == 0 {
		return fmt.Errorf("no opencode launch was materialized")
	}
	return nil
}

func (s *cf4pState) guestSends(_ string, body string) error {
	return s.queue(strings.ReplaceAll(body, "[...]", cf4pMsgs))
}

func (s *cf4pState) argvStillPins(want string) error {
	for _, a := range s.launch.Argv {
		if a == want {
			return nil
		}
	}
	return fmt.Errorf("the guest argv %q does not pin %q", s.launch.Argv, want)
}

func (s *cf4pState) claudeGuest() error {
	return s.materialize("claude", s.model)
}

func (s *cf4pState) noRelayNoRouting() error {
	s.mu.Lock()
	n := len(s.attempts)
	s.mu.Unlock()
	if n != 0 {
		return fmt.Errorf("%d chat request(s) were relayed for a context-only guest", n)
	}
	for _, e := range s.launch.Env {
		if strings.Contains(e, "127.0.0.1:44017") || strings.HasPrefix(e, operator.SessionKeyEnv+"=") {
			return fmt.Errorf("the context-only guest was wired to the proxy: %s", e)
		}
	}
	return nil
}

func TestRoutingPassthroughFullBDD(t *testing.T) {
	st := &cf4pState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.cleanup()
				return ctx, nil
			})
			sc.StepContext().Before(func(ctx context.Context, step *godog.Step) (context.Context, error) {
				if step.Type == messages.PickleStepType_OUTCOME {
					return ctx, st.flush()
				}
				return ctx, nil
			})

			// Background
			sc.Step(`^a tuned band whose model is "([^"]*)"$`, func(m string) error { st.model = m; return nil })
			sc.Step(`^the local proxy is bound to that band with session key "([^"]*)"$`, func(k string) error { st.sessionKey = k; return nil })
			sc.Step(`^the proxy owner (?:also )?tuned with (.+)$`, st.ownerTunedWith)
			sc.Step(`^the broker accepts the routing body object$`, func() error { st.modelsStatus = http.StatusOK; return nil })

			// Given
			sc.Step(`^profile "([^"]+)" sets (.+)$`, st.profileSets)
			sc.Step(`^the proxy owner's limit for the band has quants (\[.*\])$`, st.ownerQuantRule)
			sc.Step(`^the session budget is \$([0-9.]+) and \$([0-9.]+) is spent$`, st.budgetSpent)
			sc.Step(`^the first attempt fails with a transport error at station "([^"]+)"$`, st.firstAttemptTransportAt)
			sc.Step(`^the retry body carries (provider\..+)$`, st.retryBodyCarries)
			sc.Step(`^the broker answers (\d+) error\.code "([^"]+)" with Retry-After: (\d+)$`, st.brokerAnswersCode)
			sc.Step(`^the broker answers 503 error\.code "([^"]+)"$`, st.brokerAnswersNoMatch)
			sc.Step(`^the broker answers 429 with Retry-After: (\d+)$`, st.brokerAnswers429)
			sc.Step(`^the broker answers 400 "([^"]+)"$`, st.brokerAnswers400)
			sc.Step(`^the first attempt fails and the retry is served by "([^"]+)"$`, st.failsThenServedBy)
			sc.Step(`^the session is in body mode$`, st.sessionInBodyMode)
			sc.Step(`^a tuned band$`, st.tunedBandAny)
			sc.Step(`^the (opencode|hermes|aider) launch materialized per features/operator/config_[a-z]+\.feature$`, st.launchMaterialized)
			sc.Step(`^the (opencode|hermes|aider) launch materialized per features/operator/config_[a-z]+\.feature with default model "([^"]+)"$`, st.launchMaterializedWithModel)
			sc.Step(`^opencode's roger provider passes extra body fields per model \(UNVERIFIED.*\)$`, st.opencodePassesExtra)
			sc.Step(`^the claude guest per features/operator \(context-only, no proxy relay\)$`, st.claudeGuest)
			sc.Step(`^(opencode|hermes|aider) sends (\{.*\})$`, st.guestSends)
			sc.Step(`^hermes's argv still pins "([^"]+)" \(the -m pin is the guest's, the proxy resolves it\)$`, st.argvStillPins)
			sc.Step(`^no chat request is relayed and no routing object is built$`, st.noRelayNoRouting)

			// When
			sc.Step(`^a chat request arrives with (.+)$`, st.chatArrives)
			sc.Step(`^a chat request arrives$`, st.bareChat)
			sc.Step(`^a streaming chat request arrives with (.+)$`, st.streamingChat)
			sc.Step(`^a chat request arrives over 4 MiB carrying a large routing object$`, st.bigChat)
			sc.Step(`^the first chat request of the session is relayed$`, st.firstChatOfSession)
			sc.Step(`^the first relay attempt fails with a transport error$`, st.firstAttemptTransport)
			sc.Step(`^the first attempt fails with a transport error$`, st.firstAttemptTransport)
			sc.Step(`^the first attempt fails with a transport error at "([^"]+)"$`, st.firstAttemptTransportAt)
			sc.Step(`^"([^"]+)" fails with a transport error$`, st.nodeFailsTransport)
			sc.Step(`^the proxy re-picks "([^"]+)" from /discover$`, st.repicks)
			sc.Step(`^"([^"]+)" answers an upstream 429 and "([^"]+)" is eligible$`, st.n2429n3Eligible)
			sc.Step(`^the proxy waits (\d+) seconds and retries once$`, st.proxyWaitsRetries)
			sc.Step(`^the broker then serves 200$`, st.brokerThenServes)
			sc.Step(`^a streaming request settles with a final usage chunk carrying cost ([0-9.]+)$`, st.streamSettles)
			sc.Step(`^the session negotiated header mode$`, st.negotiatedHeaderMode)
			sc.Step(`^GET /v1/models is probed on the proxy$`, st.probeModels)

			// Then
			sc.Step(`^the broker receives (.+)$`, st.brokerReceives)
			sc.Step(`^the broker's 400 ([a-z_]+) is returned to the guest OpenAI-shaped$`, st.brokers400Returned)
			sc.Step(`^the broker's 400 is returned to the guest$`, st.brokers400NoCode)
			sc.Step(`^the broker's 400 is returned and the session spend is unchanged$`, st.brokers400SpendUnchanged)
			sc.Step(`^no header fallback is attempted for a 400 whose message names a key other than the carrier itself$`, st.noHeaderFallback)
			sc.Step(`^the guest's response carries no error$`, st.guestNoError)
			sc.Step(`^one proxy log line says "([^"]*)"$`, st.logLineSays)
			sc.Step(`^the guest receives an OpenAI-shaped 400 "([^"]*)"$`, st.guest400)
			sc.Step(`^nothing reaches the broker$`, st.nothingReached)
			sc.Step(`^the primary stays the band model$`, st.primaryStaysBand)
			sc.Step(`^the guest's code is never logged$`, st.guestCodeNeverLogged)
			sc.Step(`^the proxy never sends provider\.quantizations = \[\]$`, st.neverEmptyQuant)
			sc.Step(`^the /discover re-pick matches "([^"]+)"(?:, not the band model)?$`, st.discoverMatches)
			sc.Step(`^the request is served$`, st.requestServed)
			sc.Step(`^the next request is refused 402 budget_exceeded before any relay$`, st.nextRefused402)
			sc.Step(`^the session spend increases by exactly ([0-9.]+) once$`, st.spendIncreasesOnce)
			sc.Step(`^the trailing ": rogerai-cost=" comment is not double-counted$`, st.commentNotDoubled)
			sc.Step(`^the retry body carries no provider\.allow_fallbacks = false$`, st.retryNoNoFallback)
			sc.Step(`^no X-Roger-Node header is set$`, st.noNodeHeader)
			sc.Step(`^the broker serves from "([^"]+)" and the guest sees 200$`, st.brokerServesFrom)
			sc.Step(`^X-RogerAI-Provider names "([^"]+)"$`, st.providerNames)
			sc.Step(`^the proxy does not re-pick another station$`, st.noRepick)
			sc.Step(`^the guest receives the broker's last error OpenAI-shaped$`, st.guestLastErrorOpenAI)
			sc.Step(`^the guest sees 200$`, st.guestSees200)
			sc.Step(`^the guest never saw the 503$`, st.guestNeverSaw503)
			sc.Step(`^the guest receives the 503 OpenAI-shaped with Retry-After: (\d+)$`, st.guest503RA)
			sc.Step(`^the proxy does not wait$`, st.proxyDoesNotWait)
			sc.Step(`^the guest receives the 429 OpenAI-shaped rate_limit_error with Retry-After: (\d+)$`, st.guest429RA)
			sc.Step(`^the guest receives the 503 OpenAI-shaped immediately$`, st.guest503Immediately)
			sc.Step(`^the /discover re-pick considers only stations with the tools capability that are not curated$`, st.repickToolsNotCurated)
			sc.Step(`^the alert reads "([^"]+)"$`, st.alertReads)
			sc.Step(`^the response to the guest carries X-Roger-Routing-Defaults: "([^"]*)"$`, st.routingDefaultsHeader)
			sc.Step(`^the guest receives the OpenAI-shaped 413 and nothing reaches the broker$`, st.guest413Nothing)
			sc.Step(`^the body sent to the broker parses as one JSON object$`, st.bodyOneObject)
			sc.Step(`^it carries the body object$`, st.carriesBodyObject)
			sc.Step(`^it carries no (X-Roger-.+)$`, st.carriesNoHeaders)
			sc.Step(`^it still carries X-Roger-Max-Price-Out for one release$`, st.stillMaxPriceOut)
			sc.Step(`^the guest receives the 400 and the session mode is unchanged$`, st.guest400ModeUnchanged)
			sc.Step(`^config\.json is unchanged$`, st.configUnchanged)
			sc.Step(`^the approved one-entry list is returned unchanged \(features/proxy/models\.feature\)$`, st.oneEntryList)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/proxy/routing_passthrough.feature"},
			Tags:     "~@slice0 && ~@cli && ~@tui && ~@harness && ~@docs && ~@later && ~@slice5",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("features/proxy/routing_passthrough.feature (full): failing scenarios")
	}
}
