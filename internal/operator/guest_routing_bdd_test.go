package operator

// guest_routing_bdd_test.go makes features/operator/guest_routing.feature EXECUTABLE where
// its behavior lives: the REAL materializers of this package (Materialize, the byte-exact
// golden artifacts of config_test.go) and the REAL local proxy (client.ProxyHandlerLive over a
// ProxyOptionsHolder with the plate's ceiling as its budget), in front of a recording httptest
// broker (the approved seam for proxy specs).
//
// Scenarios tagged @tui (the plate's profile picker, the desk frame, the return summary, the
// desk strip) belong to internal/tui's runners. Scenarios tagged @broker assert what the REAL
// broker plans, logs or forwards to a station; a stand-in cannot observe a plan, so they belong
// to the broker runners.
//
// "The DJ chose profile <p> on the plate" is, at this seam, the model the plate hands the
// materializer: the spec pins the guest's model as `@profile/<p>` (the plate itself is TUI).
// Profiles live in config.json under a temp XDG_CONFIG_HOME, where the proxy resolves them
// (contract §9).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/client"
)

type cf4oAttempt struct {
	headers http.Header
	body    map[string]any
	raw     []byte
}

type cf4oState struct {
	t *testing.T

	broker  *httptest.Server
	mu      sync.Mutex
	hits    []cf4oAttempt
	nextSSE string // the next stream answer ("" = default)
	cost    string // X-RogerAI-Cost on the next JSON 200
	model   string // X-RogerAI-Model on the next 200 ("" = none)

	servedSSE string // the stream the stand-in last served

	baseURL, key, band string
	holder             *client.ProxyOptionsHolder
	handler            http.Handler

	code     int
	body     []byte
	hdr      http.Header
	mark     int
	calls0   int64
	spent0   float64
	extra    string // "opencode is configured to add ..."
	profile  string // the DJ's plate choice ("" = none)
	launches map[string]Launch
	lerr     map[string]error
	cfgDir   string
	home     string
}

func (s *cf4oState) reset(t *testing.T) {
	*s = cf4oState{t: t, launches: map[string]Launch{}, lerr: map[string]error{}}
	s.cfgDir = t.TempDir()
	s.home = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", s.cfgDir)
	t.Setenv("HOME", s.home)
	s.broker = httptest.NewServer(http.HandlerFunc(s.serve))
}

func (s *cf4oState) cleanup() {
	if s.broker != nil {
		s.broker.Close()
	}
}

func (s *cf4oState) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/models":
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	case "/discover":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"offers":[{"node_id":"n1","model":"` + s.band + `","online":true,"price_out":0.5,"signal":90}]}`))
	case "/v1/chat/completions":
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		s.mu.Lock()
		s.hits = append(s.hits, cf4oAttempt{headers: r.Header.Clone(), body: b, raw: raw})
		sse, cost, model := s.nextSSE, s.cost, s.model
		s.nextSSE, s.cost, s.model = "", "", ""
		s.mu.Unlock()
		w.Header().Set("X-RogerAI-Provider", "n1")
		if model != "" {
			w.Header().Set("X-RogerAI-Model", model)
		}
		if stream, _ := b["stream"].(bool); stream {
			if sse == "" {
				sse = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
			}
			s.mu.Lock()
			s.servedSSE = sse
			s.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-RogerAI-Cost", "0")
			_, _ = w.Write([]byte(sse))
			return
		}
		if cost == "" {
			cost = "0.0001"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RogerAI-Cost", cost)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	default:
		http.NotFound(w, r)
	}
}

// ── Background ─────────────────────────────────────────────────────────────────────────

func (s *cf4oState) liveSession(base, key, band string) error {
	s.baseURL, s.key, s.band = base, key, band
	s.holder = client.NewProxyOptionsHolder(client.ProxyOptions{
		Broker: s.broker.URL, User: "u", Model: band, SessionKey: key,
		MaxPriceOut: client.ConsumerDefaultMaxOut,
	})
	s.handler = client.ProxyHandlerLive(s.holder)
	return nil
}

func (s *cf4oState) bodyAccepted() error { return nil } // the stand-in answers GET /v1/models 200

func (s *cf4oState) plateCeiling(usd string) error {
	f, err := strconv.ParseFloat(usd, 64)
	if err != nil {
		return err
	}
	s.holder.SetBudget(f)
	return nil
}

// ── profiles ───────────────────────────────────────────────────────────────────────────

func (s *cf4oState) writeProfile(name string, prof map[string]any) error {
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
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

var (
	cf4oAssignSplit = regexp.MustCompile(` and ([a-z_][a-z0-9_.]* = )`)
	cf4oAssign      = regexp.MustCompile(`^([a-z_][a-z0-9_.]*) = (.+)$`)
)

func cf4oValue(text string) any {
	var v any
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &v) == nil {
		return v
	}
	return strings.TrimSpace(text)
}

func (s *cf4oState) profileSets(name, text string) error {
	text = strings.TrimSuffix(strings.TrimSpace(text), " and no model")
	parts := cf4oAssignSplit.Split(text, -1)
	keys := cf4oAssignSplit.FindAllStringSubmatch(text, -1)
	assigns := []string{parts[0]}
	for i, k := range keys {
		assigns = append(assigns, k[1]+parts[i+1])
	}
	prof := map[string]any{}
	for _, a := range assigns {
		m := cf4oAssign.FindStringSubmatch(strings.TrimSpace(a))
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
		cur[segs[len(segs)-1]] = cf4oValue(m[2])
	}
	return s.writeProfile(name, prof)
}

func (s *cf4oState) djChose(name string) error { s.profile = name; return nil }

// ── guest requests ─────────────────────────────────────────────────────────────────────

const cf4oMsgs = `[{"role":"user","content":"hi"}]`

func (s *cf4oState) send(body string) error {
	body = strings.ReplaceAll(body, "[...]", cf4oMsgs)
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return fmt.Errorf("bad guest body %q: %v", body, err)
	}
	if _, ok := obj["messages"]; !ok {
		var m any
		_ = json.Unmarshal([]byte(cf4oMsgs), &m)
		obj["messages"] = m
	}
	b, _ := json.Marshal(obj)
	s.mu.Lock()
	s.mark = len(s.hits)
	s.mu.Unlock()
	s.calls0, s.spent0 = s.holder.Calls(), s.holder.Spent()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b)).WithContext(context.Background())
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	s.code, s.body, s.hdr = rec.Code, rec.Body.Bytes(), rec.Header()
	return nil
}

func (s *cf4oState) guestSends(body string) error { return s.send(body) }

func (s *cf4oState) namedGuestSends(_ string, body string) error { return s.send(body) }

func (s *cf4oState) opencodeConfigured(extra string) error { s.extra = extra; return nil }

func (s *cf4oState) opencodeSendsThat() error {
	var x map[string]any
	if err := json.Unmarshal([]byte(s.extra), &x); err != nil {
		return err
	}
	x["model"] = s.band
	b, _ := json.Marshal(x)
	return s.send(string(b))
}

func (s *cf4oState) last() (cf4oAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hits) <= s.mark {
		return cf4oAttempt{}, fmt.Errorf("nothing reached the broker (guest got %d: %s)", s.code, s.body)
	}
	return s.hits[len(s.hits)-1], nil
}

// clause reading shared in shape with the proxy and CLI runners
var (
	cf4oClauseStart = regexp.MustCompile(`^(no |[a-z_][a-z0-9_.]*( =| "| \[| \{| true| false| -?[0-9]|$))`)
	cf4oBare        = regexp.MustCompile(`^([a-z_][a-z0-9_.]*) ("|\[|\{|true|false|-?[0-9])(.*)$`)
	cf4oParenTail   = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
)

func cf4oClauses(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		for _, sep := range []string{", and ", ", ", " and "} {
			if strings.HasPrefix(text[i:], sep) && cf4oClauseStart.MatchString(text[i+len(sep):]) {
				out = append(out, strings.TrimSpace(text[start:i]))
				start = i + len(sep)
				i = start - 1
				break
			}
		}
	}
	return append(out, strings.TrimSpace(text[start:]))
}

func cf4oWalk(m any, path string) (any, bool) {
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

func cf4oCanon(v any) string {
	b, _ := json.Marshal(v)
	var back any
	_ = json.Unmarshal(b, &back)
	c, _ := json.Marshal(back)
	return string(c)
}

func (s *cf4oState) brokerReceives(text string) error {
	a, err := s.last()
	if err != nil {
		return err
	}
	text = cf4oParenTail.ReplaceAllString(strings.TrimSpace(text), "")
	for _, c := range cf4oClauses(text) {
		var path, val string
		if m := cf4oAssign.FindStringSubmatch(c); m != nil {
			path, val = m[1], m[2]
		} else if m := cf4oBare.FindStringSubmatch(c); m != nil {
			path, val = m[1], m[2]+m[3]
		} else {
			return fmt.Errorf("cannot read clause %q", c)
		}
		got, ok := cf4oWalk(a.body, path)
		want := cf4oValue(val)
		if !ok || cf4oCanon(got) != cf4oCanon(want) {
			return fmt.Errorf("the broker received %s = %s, want %s; body %s", path, cf4oCanon(got), cf4oCanon(want), a.raw)
		}
	}
	return nil
}

func (s *cf4oState) openAIShapedAsBefore() error {
	if s.code != 200 {
		return fmt.Errorf("guest status %d: %s", s.code, s.body)
	}
	var r struct {
		Object  string `json:"object"`
		Choices []any  `json:"choices"`
	}
	if json.Unmarshal(s.body, &r) != nil || r.Object != "chat.completion" || len(r.Choices) == 0 {
		return fmt.Errorf("guest response is not the OpenAI chat completion it was: %s", s.body)
	}
	return nil
}

func (s *cf4oState) guest400(msg string) error {
	if s.code != 400 {
		return fmt.Errorf("guest status %d, want 400: %s", s.code, s.body)
	}
	var e struct {
		Error struct{ Type, Message string } `json:"error"`
	}
	if json.Unmarshal(s.body, &e) != nil || e.Error.Type == "" {
		return fmt.Errorf("the 400 is not OpenAI-shaped: %s", s.body)
	}
	if !strings.Contains(e.Error.Message, msg) {
		return fmt.Errorf("400 message %q lacks %q", e.Error.Message, msg)
	}
	return nil
}

func (s *cf4oState) callCounterSame() error {
	if c := s.holder.Calls(); c != s.calls0 {
		return fmt.Errorf("the plate's call counter moved %d -> %d", s.calls0, c)
	}
	return nil
}

func (s *cf4oState) keptAsSent() error { return s.brokerReceives(`model "` + s.band + `"`) }

func (s *cf4oState) gpt4oNoMatch() error {
	// The stand-in serves only the band; a relayed "gpt-4o" would be the broker's 503 no_match.
	// Whatever the guest sees must be an OpenAI-shaped refusal, and the request must have
	// reached the broker with the model the guest named.
	a, err := s.last()
	if err != nil {
		return err
	}
	if a.body["model"] != "gpt-4o" {
		return fmt.Errorf("the broker received model %v, want the guest's own \"gpt-4o\"", a.body["model"])
	}
	return nil
}

func (s *cf4oState) ownerMaxOut(v string) error {
	f, _ := strconv.ParseFloat(v, 64)
	o := s.holder.Get()
	o.MaxPriceOut = f
	s.holder.SetBand(o)
	return nil
}

// ── materialization ────────────────────────────────────────────────────────────────────

func (s *cf4oState) guestByName(name string) (Guest, error) {
	for _, g := range Registry() {
		if g.Name == name {
			return g, nil
		}
	}
	return Guest{}, fmt.Errorf("no registered guest %q", name)
}

func (s *cf4oState) pinnedModel() string {
	if s.profile != "" {
		return "@profile/" + s.profile
	}
	return s.band
}

func (s *cf4oState) materialize(name string) error {
	g, err := s.guestByName(name)
	if err != nil {
		return err
	}
	l, cleanup, err := Materialize(g, Session{BaseURL: s.baseURL, SessionKey: s.key, Model: s.pinnedModel(), ScratchRoot: s.cfgDir})
	if err != nil {
		s.lerr[name] = err
		return fmt.Errorf("materialize %s with model %q: %v", name, s.pinnedModel(), err)
	}
	if cleanup != nil {
		s.t.Cleanup(func() { _ = cleanup() })
	}
	s.launches[name] = l
	return nil
}

func (s *cf4oState) launchMaterialized(name string) error { return s.materialize(name) }

func (s *cf4oState) launchNoProfile(name string) error {
	s.profile = ""
	return s.materialize(name)
}

func (s *cf4oState) eachLaunch() error {
	for _, n := range []string{"opencode", "hermes", "aider"} {
		if err := s.materialize(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *cf4oState) openAndHermes() error {
	for _, n := range []string{"opencode", "hermes"} {
		if err := s.materialize(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *cf4oState) lastLaunch() (Launch, string, error) {
	for _, n := range []string{"opencode", "hermes", "aider"} {
		if l, ok := s.launches[n]; ok {
			return l, n, nil
		}
	}
	return Launch{}, "", fmt.Errorf("no launch was materialized")
}

func (s *cf4oState) argvExactly(want string) error {
	l, _, err := s.lastLaunch()
	if err != nil {
		return err
	}
	if got := strings.Join(l.Argv, " "); got != want {
		return fmt.Errorf("argv %q, want exactly %q", got, want)
	}
	return nil
}

func (s *cf4oState) argvPins(want string) error {
	l, _, err := s.lastLaunch()
	if err != nil {
		return err
	}
	if !strings.Contains(strings.Join(l.Argv, " "), want) {
		return fmt.Errorf("argv %q does not pin %q", l.Argv, want)
	}
	return nil
}

func (s *cf4oState) opencodeJSON() (map[string]any, []byte, error) {
	l, ok := s.launches["opencode"]
	if !ok {
		return nil, nil, fmt.Errorf("no opencode launch")
	}
	b, err := os.ReadFile(filepath.Join(l.Dir, "opencode.json"))
	if err != nil {
		return nil, nil, err
	}
	var m map[string]any
	return m, b, json.Unmarshal(b, &m)
}

func (s *cf4oState) opencodeModelIs(want string) error {
	m, _, err := s.opencodeJSON()
	if err != nil {
		return err
	}
	if m["model"] != want {
		return fmt.Errorf("opencode.json model %v, want %q", m["model"], want)
	}
	return nil
}

func (s *cf4oState) opencodeModelsList(a, b string) error {
	m, raw, err := s.opencodeJSON()
	if err != nil {
		return err
	}
	models, _ := cf4oWalk(m, "provider.roger.models")
	mm, _ := models.(map[string]any)
	for _, id := range []string{a, b} {
		if _, ok := mm[id]; !ok {
			return fmt.Errorf("opencode.json's models block lacks %q: %s", id, raw)
		}
	}
	return nil
}

func (s *cf4oState) hermesDefault(want string) error {
	l, ok := s.launches["hermes"]
	if !ok {
		return fmt.Errorf("no hermes launch")
	}
	b, err := os.ReadFile(filepath.Join(l.Dir, "hermes-home", "config.yaml"))
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`(?m)^\s+default:\s*"?` + regexp.QuoteMeta(want) + `"?\s*$`).Match(b) {
		return fmt.Errorf("config.yaml's model.default is not %q:\n%s", want, b)
	}
	return nil
}

func (s *cf4oState) noFileForAider() error {
	l, ok := s.launches["aider"]
	if !ok {
		return fmt.Errorf("no aider launch")
	}
	if l.Dir != "" {
		return fmt.Errorf("aider got a scratch dir %s", l.Dir)
	}
	return nil
}

func (s *cf4oState) goldenOpencode() error {
	_, raw, err := s.opencodeJSON()
	if err != nil {
		return err
	}
	if string(raw) != goldenOpencode {
		return fmt.Errorf("opencode.json differs from the approved golden:\n%s", raw)
	}
	return nil
}

func (s *cf4oState) codeNowhere(code string) error {
	for name, l := range s.launches {
		for _, a := range l.Argv {
			if strings.Contains(a, code) {
				return fmt.Errorf("%s argv carries the code", name)
			}
		}
		for _, e := range l.Env {
			if strings.Contains(e, code) {
				return fmt.Errorf("%s env carries the code", name)
			}
		}
		if l.Dir != "" {
			err := filepath.Walk(l.Dir, func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(code)) {
						return fmt.Errorf("%s writes the code into %s", name, p)
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *cf4oState) brokerReceivesFreqEach() error {
	if err := s.send(`{"model": "` + s.pinnedModel() + `"}`); err != nil {
		return err
	}
	a, err := s.last()
	if err != nil {
		return err
	}
	if _, ok := cf4oWalk(a.body, "roger.freq"); !ok && a.headers.Get("X-Roger-Freq") == "" {
		return fmt.Errorf("the relayed request carries no band code (body %s)", a.raw)
	}
	return nil
}

func (s *cf4oState) userConfigNeverWritten() error {
	for _, p := range []string{".config/opencode", ".hermes/config.yaml", ".aider.conf.yml", ".aider"} {
		if _, err := os.Stat(filepath.Join(s.home, p)); err == nil {
			return fmt.Errorf("the user's %s was written", p)
		}
	}
	return nil
}

func (s *cf4oState) keyInNoFile() error {
	return s.codeNowhere(s.key)
}

// ── cost meter ─────────────────────────────────────────────────────────────────────────

func (s *cf4oState) streamsSettling(cost string) error {
	s.mu.Lock()
	s.nextSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6,\"cost\":" + cost + ",\"rogerai\":{\"receipt\":\"rcpt\"}}}\n\n" +
		": rogerai-cost=" + cost + "\n\n" + "data: [DONE]\n\n"
	s.mu.Unlock()
	return s.send(`{"model": "` + s.band + `", "stream": true}`)
}

func (s *cf4oState) streamsTurn() error {
	s.mu.Lock()
	if s.nextSSE == "" {
		s.nextSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"id\":\"c1\",\"model\":\"" + s.band + "\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6,\"cost\":0.002,\"rogerai\":{\"receipt\":\"rcpt\",\"node\":\"n1\"}}}\n\n" +
			": rogerai-cost=0.002\n\n" + "data: [DONE]\n\n"
	}
	s.mu.Unlock()
	return s.send(`{"model": "` + s.band + `", "stream": true}`)
}

func (s *cf4oState) oldBrokerCommentOnly() error {
	s.mu.Lock()
	s.nextSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n: rogerai-cost=0.004\n\ndata: [DONE]\n\n"
	s.mu.Unlock()
	return nil
}

func (s *cf4oState) finalChunkCarries(cost string) error {
	want, _ := strconv.ParseFloat(cost, 64)
	sc := bufio.NewScanner(bytes.NewReader(s.body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var c struct {
			Usage *struct {
				Cost    float64        `json:"cost"`
				RogerAI map[string]any `json:"rogerai"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &c) == nil && c.Usage != nil {
			if c.Usage.Cost != want || c.Usage.RogerAI["receipt"] == nil {
				return fmt.Errorf("final chunk usage %+v, want cost %v with a receipt", c.Usage, want)
			}
			return nil
		}
	}
	return fmt.Errorf("the guest received no usage chunk:\n%s", s.body)
}

func (s *cf4oState) spendIncreases(cost string) error {
	want, _ := strconv.ParseFloat(cost, 64)
	if d := s.holder.Spent() - s.spent0; d < want-1e-9 || d > want+1e-9 {
		return fmt.Errorf("plate spend increased by %v, want exactly %v", d, want)
	}
	return nil
}

func (s *cf4oState) commentNotCountedTwice(cost string) error { return s.spendIncreases(cost) }

func (s *cf4oState) spendFromComment() error { return s.spendIncreases("0.004") }

func (s *cf4oState) ceilingSpent(ceiling, spent string) error {
	if err := s.plateCeiling(ceiling); err != nil {
		return err
	}
	// Spend it the way a guest does: one served turn costing that much.
	s.mu.Lock()
	s.cost = spent
	s.mu.Unlock()
	if err := s.send(`{"model": "` + s.band + `"}`); err != nil {
		return err
	}
	if s.code != 200 {
		return fmt.Errorf("the seeding turn was refused: %d %s", s.code, s.body)
	}
	return nil
}

func (s *cf4oState) servedViaFallback(cost string) error {
	s.mu.Lock()
	s.cost, s.model = cost, "llama-3.3-70b"
	s.mu.Unlock()
	return s.send(`{"model": "` + s.band + `"}`)
}

func (s *cf4oState) crossingTurnServed() error {
	if s.code != 200 {
		return fmt.Errorf("the crossing turn was refused: %d %s", s.code, s.body)
	}
	return nil
}

func (s *cf4oState) nextRefused402() error {
	if err := s.send(`{"model": "` + s.band + `"}`); err != nil {
		return err
	}
	if s.code != http.StatusPaymentRequired || !strings.Contains(string(s.body), "budget_exceeded") {
		return fmt.Errorf("the next turn got %d: %s, want 402 budget_exceeded", s.code, s.body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hits) != s.mark {
		return fmt.Errorf("the refused turn reached the broker")
	}
	return nil
}

func (s *cf4oState) usageChunkUnchanged() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := ""
	for _, l := range strings.Split(s.lastSSE(), "\n") {
		if strings.Contains(l, `"usage"`) {
			want = l
		}
	}
	if want == "" {
		return fmt.Errorf("the stand-in sent no usage chunk")
	}
	got := string(s.body)
	i, j := strings.Index(got, want), strings.Index(got, "data: [DONE]")
	if i < 0 {
		return fmt.Errorf("the guest did not receive the usage chunk unchanged:\n%s", got)
	}
	if j >= 0 && i > j {
		return fmt.Errorf("the usage chunk arrived after [DONE]")
	}
	return nil
}

// lastSSE is the stream the stand-in last served (kept for the byte comparison).
func (s *cf4oState) lastSSE() string { return s.servedSSE }

func (s *cf4oState) ignoresUnknownFields() error {
	sc := bufio.NewScanner(bytes.NewReader(s.body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &c); err != nil {
			return fmt.Errorf("a chunk does not decode as an OpenAI chunk: %v: %s", err, line)
		}
	}
	return nil
}

func TestGuestRoutingBDD(t *testing.T) {
	st := &cf4oState{}
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
			sc.Step(`^a live proxy session at "([^"]+)" with session key "([^"]+)" and band model "([^"]+)"$`, st.liveSession)
			sc.Step(`^the broker accepts the routing body object$`, st.bodyAccepted)
			sc.Step(`^the plate ceiling is \$([0-9.]+)$`, st.plateCeiling)

			sc.Step(`^profile "([^"]+)" sets (.+)$`, st.profileSets)
			sc.Step(`^the DJ chose profile "([^"]+)"(?: on the plate)?$`, st.djChose)
			sc.Step(`^opencode is configured to add (\{.*\}) to its request bodies \(.*\)$`, st.opencodeConfigured)
			sc.Step(`^the proxy owner tuned with --max-out ([0-9.]+)$`, st.ownerMaxOut)
			sc.Step(`^an old broker that sends only the ": rogerai-cost=" comment$`, st.oldBrokerCommentOnly)
			sc.Step(`^the ceiling is \$([0-9.]+) and \$([0-9.]+) is spent$`, st.ceilingSpent)

			sc.Step(`^the guest sends (\{.*\})$`, st.guestSends)
			sc.Step(`^(opencode|hermes|aider) sends (\{.*\})$`, st.namedGuestSends)
			sc.Step(`^opencode sends a chat request with that body$`, st.opencodeSendsThat)
			sc.Step(`^the (opencode|hermes|aider) launch is materialized$`, st.launchMaterialized)
			sc.Step(`^the (opencode|hermes|aider) launch is materialized with no profile chosen$`, st.launchNoProfile)
			sc.Step(`^each guest launch is materialized$`, st.eachLaunch)
			sc.Step(`^any guest launch is materialized$`, st.eachLaunch)
			sc.Step(`^the opencode and hermes launches are materialized$`, st.openAndHermes)
			sc.Step(`^the guest streams a turn that settles at cost ([0-9.]+)$`, st.streamsSettling)
			sc.Step(`^the guest streams a turn$`, st.streamsTurn)
			sc.Step(`^the guest's next turn is served via a fallback model at cost ([0-9.]+)$`, st.servedViaFallback)

			sc.Step(`^the broker receives (.+)$`, st.brokerReceives)
			sc.Step(`^the guest's response is OpenAI-shaped exactly as before$`, st.openAIShapedAsBefore)
			sc.Step(`^the guest receives an OpenAI-shaped 400 "([^"]+)"$`, st.guest400)
			sc.Step(`^the plate's call counter does not increase$`, st.callCounterSame)
			sc.Step("^the guest's `model` is kept as sent because the body carries a routing carrier$", st.keptAsSent)
			sc.Step(`^the broker answers 503 no_match if no station serves "gpt-4o", which the guest sees OpenAI-shaped$`, st.gpt4oNoMatch)
			sc.Step(`^the argv is exactly "([^"]+)"$`, st.argvExactly)
			sc.Step(`^the argv pins "([^"]+)"$`, st.argvPins)
			sc.Step(`^opencode\.json's "model" is "([^"]+)"$`, st.opencodeModelIs)
			sc.Step(`^opencode\.json's models block lists "([^"]+)" alongside "([^"]+)"$`, st.opencodeModelsList)
			sc.Step(`^config\.yaml's model\.default is "([^"]+)"$`, st.hermesDefault)
			sc.Step(`^no file is created for aider$`, st.noFileForAider)
			sc.Step(`^opencode\.json equals the approved golden artifact$`, st.goldenOpencode)
			sc.Step(`^"([^"]+)" appears in no generated file, no argv, and no env value$`, st.codeNowhere)
			sc.Step(`^the broker receives roger\.freq on each relayed request$`, st.brokerReceivesFreqEach)
			sc.Step(`^the user's real ~/\.config/opencode, ~/\.hermes/config\.yaml and aider files are never opened for writing$`, st.userConfigNeverWritten)
			sc.Step(`^the session key appears in no generated file$`, st.keyInNoFile)
			sc.Step(`^the broker's final chunk carries usage\.cost = ([0-9.]+) and usage\.rogerai\.receipt$`, st.finalChunkCarries)
			sc.Step(`^the plate's spend increases by exactly ([0-9.]+)$`, st.spendIncreases)
			sc.Step(`^the trailing ": rogerai-cost=([0-9.]+)" comment is not counted a second time$`, st.commentNotCountedTwice)
			sc.Step(`^the plate's spend increases from the comment exactly as approved$`, st.spendFromComment)
			sc.Step(`^that turn is served \(the crossing turn completes\)$`, st.crossingTurnServed)
			sc.Step(`^the next turn is refused 402 budget_exceeded before any relay$`, st.nextRefused402)
			sc.Step(`^the guest receives the broker's final usage chunk unchanged before \[DONE\]$`, st.usageChunkUnchanged)
			sc.Step(`^a guest that ignores unknown chunk fields keeps working$`, st.ignoresUnknownFields)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/operator/guest_routing.feature"},
			Tags:     "~@tui && ~@broker && ~@cli && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice5",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("features/operator/guest_routing.feature: failing scenarios")
	}
}
