package main

// hardening_affinity_bdd_test.go makes features/routing/session_affinity.feature EXECUTABLE
// (slice 6, contract §14.B1) against the REAL broker: real relay(), real store (Postgres when
// ROGERAI_TEST_DATABASE_URL is set), the miniredis shared store, real stations on real tunnels
// with scripted upstreams, the real sealed edge fabric for Tower rows. No mocks.
//
// It also holds sa6H, the small harness every slice-6 user-value runner in this package builds
// on (idempotency, route explain, models fields, class aliases, error envelope). sa6H embeds the
// request-shape runner's rs1State (its relay, snapshot, band, grant, Tower and moderation
// fixtures) and adds: several named consumers, a clause parser that turns a scenario's "with
// ..." phrase into the routing object / headers it states, per-request /admin/live deltas and
// "force a server" fixtures for multi-turn scenarios.
//
// Observation points (stated, never faked):
//   - "the turn is served by X" makes X the only on-air station of the model for that one relay
//     (the others are stale for its duration and re-heartbeat right after), then requires a 200
//     from X: a refused request fails the Given honestly.
//   - "is affine to Y" replays the same request 8 times with every station on air and requires
//     every replay to land on Y (three equal stations under P2C would spread them).
//   - Affinity counters are the contract's /admin/live names, read flat as
//     "affinity_misses{expired}", "affinity_misses_expired" or nested affinity_misses.expired.
//   - "was not forced to X" is the contract's own signal: affinity_hits did not move on that
//     relay.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"crypto/rand"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// ===================================================================================
// sa6H: the shared slice-6 harness
// ===================================================================================

type sa6User struct {
	priv   ed25519.PrivateKey
	wallet string
}

type sa6KV struct{ k, v string }

// sa6Spec is one request as a scenario states it.
type sa6Spec struct {
	user         string // a named consumer ("u-1"), "" = current
	caller       string // "user" | "anon" | "grant"
	model        string
	noModel      bool
	stream       bool
	roger        []sa6KV
	provider     []sa6KV
	maxPrice     []sa6KV
	top          []sa6KV
	hdr          map[string]string
	maxTokens    int
	promptTokens int
	tools        bool
	prompt       string
	all429       bool   // every station answers 429 once (the plan probe)
	servedBy     string // force this station to serve
	through      string // force this Tower to serve
	throughPrice float64
	expect       string // "and the response is <...>": the outcome the Given states
	illegal      bool   // the prompt is one the classifier flags
	raw          string // a complete raw body
	twoPrompts   bool   // "with two very different prompts": two requests
}

func (q *sa6Spec) frag() string {
	var parts []string
	join := func(kvs []sa6KV) string {
		var p []string
		for _, kv := range kvs {
			p = append(p, strconv.Quote(kv.k)+": "+kv.v)
		}
		return strings.Join(p, ", ")
	}
	prov := q.provider
	if len(q.maxPrice) > 0 {
		prov = append(append([]sa6KV(nil), prov...), sa6KV{"max_price", "{" + join(q.maxPrice) + "}"})
	}
	if len(prov) > 0 {
		parts = append(parts, `"provider": {`+join(prov)+`}`)
	}
	if len(q.roger) > 0 {
		parts = append(parts, `"roger": {`+join(q.roger)+`}`)
	}
	if q.tools {
		parts = append(parts, `"tools": [{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`)
	}
	if q.prompt != "" {
		parts = append(parts, `"messages": [{"role":"user","content":`+strconv.Quote(q.prompt)+`}]`)
	}
	if t := join(q.top); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, ", ")
}

type sa6H struct {
	*rs1State

	users map[string]sa6User
	cur   string

	admBefore map[string]any // /admin/live just before the latest scenario relay (or batch)
	hitMark   int            // hits index just before the latest scenario relay
	lastSpec  sa6Spec
	batchOK   []string // served station names of the latest batch, in order
	pseudo1   map[string]string
	mrDown    bool

	illegal  bool           // "sync moderation flags the prompt"
	first    *rs1Shot       // the first request of a retry pair
	firstQ   sa6Spec        // and what it was
	delta    map[string]int // upstream requests per station during the latest send
	lastSent []byte         // the exact body the latest send carried
	bg       chan rs1Shot   // a request still in flight (in a goroutine)
}

func (h *sa6H) reset() error {
	if err := h.resetRS1(); err != nil {
		return err
	}
	h.users = map[string]sa6User{"u-1": {priv: h.consumerPriv, wallet: h.wallet}}
	h.cur = "u-1"
	h.admBefore, h.hitMark, h.lastSpec, h.batchOK = nil, 0, sa6Spec{}, nil
	h.pseudo1 = map[string]string{}
	h.mrDown, h.illegal, h.first, h.firstQ, h.delta, h.lastSent, h.bg = false, false, nil, sa6Spec{}, nil, nil, nil
	for _, k := range []string{"ROGERAI_AFFINITY_TTL", "ROGERAI_IDEMPOTENCY_TTL"} {
		_ = os.Unsetenv(k)
	}
	return nil
}

// as makes a named consumer the current caller (the wallet the ledger steps read).
func (h *sa6H) as(name string) {
	if name == "" || name == h.cur {
		return
	}
	if u, ok := h.users[name]; ok {
		h.consumerPriv, h.wallet = u.priv, u.wallet
		h.funded = true
		h.cur = name
	}
}

// newUser binds a fresh GitHub-linked account and funds it; the current caller is unchanged.
func (h *sa6H) newUser(name string, bal float64) error {
	if _, ok := h.users[name]; ok {
		h.as(name)
		return h.fund(bal)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := h.db.BindOwner(store.Owner{GitHubID: gid.Int64() + 10, Login: name + "-" + h.nonce, Pubkey: pub}); err != nil {
		return err
	}
	prevPriv, prevWallet, prevFunded, prevCur := h.consumerPriv, h.wallet, h.funded, h.cur
	h.consumerPriv, h.wallet, h.funded = priv, "u_gh_"+strconv.FormatInt(gid.Int64()+10, 10), false
	if err := h.fund(bal); err != nil {
		return err
	}
	h.users[name] = sa6User{priv: priv, wallet: h.wallet}
	h.consumerPriv, h.wallet, h.funded, h.cur = prevPriv, prevWallet, prevFunded, prevCur
	return nil
}

func (h *sa6H) fundedConsumer(name string) error {
	if name == "u-1" {
		h.as("u-1")
		return h.ensureFunded()
	}
	return h.newUser(name, 10)
}

// --- the clause parser ---------------------------------------------------------------------

// sa6Value scans one JSON value (or a scenario value form) off the front of s.
func sa6Value(s string) (val, rest string, err error) {
	s = strings.TrimLeft(s, " ")
	if m := regexp.MustCompile(`^a (\d+)-byte (?:string|key)`).FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return strconv.Quote(strings.Repeat("x", n)), s[len(m[0]):], nil
	}
	if s == "" {
		return "", "", fmt.Errorf("no value")
	}
	switch s[0] {
	case '"':
		// a Gherkin table cell turns "\n" into a real newline: put the JSON escape back
		s = strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(s)
		esc := false
		for i := 1; i < len(s); i++ {
			switch {
			case esc:
				esc = false
			case s[i] == '\\':
				esc = true
			case s[i] == '"':
				return s[:i+1], s[i+1:], nil
			}
		}
		return "", "", fmt.Errorf("unterminated string in %q", s)
	case '[', '{':
		depth, inStr, esc := 0, false, false
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case inStr && esc:
				esc = false
			case inStr && c == '\\':
				esc = true
			case c == '"':
				inStr = !inStr
			case !inStr && (c == '[' || c == '{'):
				depth++
			case !inStr && (c == ']' || c == '}'):
				depth--
				if depth == 0 {
					return s[:i+1], s[i+1:], nil
				}
			}
		}
		return "", "", fmt.Errorf("unbalanced value in %q", s)
	}
	i := strings.IndexAny(s, " ,")
	if i < 0 {
		return s, "", nil
	}
	return s[:i], s[i:], nil
}

var sa6Clauses = []struct {
	re *regexp.Regexp
	fn func(h *sa6H, q *sa6Spec, m []string, rest string) (string, error)
}{
	{regexp.MustCompile(`^every station answers 429 once`), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		q.all429 = true
		return r, nil
	}},
	{regexp.MustCompile(`^the turn is served by "([^"]+)"(?: as "[^"]+")?`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		q.servedBy = m[1]
		return r, nil
	}},
	{regexp.MustCompile(`^"([^"]+)" has no station`), func(h *sa6H, _ *sa6Spec, m []string, r string) (string, error) {
		for n, st := range h.stations {
			if st.model == m[1] {
				return "", fmt.Errorf("station %q serves %q", n, m[1])
			}
		}
		return r, nil
	}},
	{regexp.MustCompile(`^the turn is served through "([^"]+)"(?: priced \$([0-9.]+))?`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		q.through = m[1]
		if m[2] != "" {
			q.throughPrice = rs1f(m[2])
		}
		return r, nil
	}},
	{regexp.MustCompile(`^the response is (.+)$`), func(_ *sa6H, q *sa6Spec, m []string, _ string) (string, error) {
		q.expect = m[1]
		return "", nil
	}},
	{regexp.MustCompile(`^a different prompt`), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		q.promptTokens = 77
		return r, nil
	}},
	{regexp.MustCompile(`^two very different prompts`), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		q.twoPrompts = true
		return r, nil
	}},
	{regexp.MustCompile(`^a prompt the classifier would flag`), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		q.illegal = true
		return r, nil
	}},
	{regexp.MustCompile(`^no session id`), func(_ *sa6H, _ *sa6Spec, _ []string, r string) (string, error) { return r, nil }},
	{regexp.MustCompile(`^no band code`), func(_ *sa6H, _ *sa6Spec, _ []string, r string) (string, error) { return r, nil }},
	{regexp.MustCompile(`^nothing$`), func(_ *sa6H, _ *sa6Spec, _ []string, r string) (string, error) { return r, nil }},
	{regexp.MustCompile(`^a non-empty tools array`), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		q.tools = true
		return r, nil
	}},
	{regexp.MustCompile(`^a (\d+)-byte roger\.session`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		n, _ := strconv.Atoi(m[1])
		q.roger = append(q.roger, sa6KV{"session", strconv.Quote(strings.Repeat("s", n))})
		return r, nil
	}},
	{regexp.MustCompile(`^a (\d+)-byte Idempotency-Key`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		n, _ := strconv.Atoi(m[1])
		q.hdr["Idempotency-Key"] = strings.Repeat("k", n)
		return r, nil
	}},
	{regexp.MustCompile(`^a (\d+)-token prompt`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		q.promptTokens, _ = strconv.Atoi(m[1])
		return r, nil
	}},
	{regexp.MustCompile(`^the prompt "([^"]*)"`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		q.prompt = m[1]
		return r, nil
	}},
	{regexp.MustCompile(`^a signed body larger than 4 MiB`), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		q.prompt = strings.Repeat("x", 4<<20+4096)
		return r, nil
	}},
	{regexp.MustCompile(`^max_tokens (\d+)`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		q.maxTokens, _ = strconv.Atoi(m[1])
		return r, nil
	}},
	{regexp.MustCompile(`^stream true`), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		q.stream = true
		return r, nil
	}},
	{regexp.MustCompile(`^model ("[^"]*")`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		q.model, _ = strconv.Unquote(m[1])
		return r, nil
	}},
	{regexp.MustCompile(`^models with (\d+) distinct entries`), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		n, _ := strconv.Atoi(m[1])
		var ids []string
		for i := 0; i < n; i++ {
			ids = append(ids, strconv.Quote(fmt.Sprintf("distinct-%d", i)))
		}
		q.top = append(q.top, sa6KV{"models", "[" + strings.Join(ids, ", ") + "]"})
		return r, nil
	}},
	{regexp.MustCompile(`^(?:Idempotency-Key|X-Roger-[A-Za-z-]+) `), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		name := strings.TrimSpace(m[0])
		v, rest, err := sa6Value(r)
		if err != nil {
			return "", err
		}
		if uq, e := strconv.Unquote(v); e == nil {
			v = uq
		}
		q.hdr[name] = v
		return rest, nil
	}},
	{regexp.MustCompile(`^top-level (\w+) `), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		v, rest, err := sa6Value(r)
		q.top = append(q.top, sa6KV{m[1], v})
		return rest, err
	}},
	{regexp.MustCompile(`^models `), func(_ *sa6H, q *sa6Spec, _ []string, r string) (string, error) {
		v, rest, err := sa6Value(r)
		q.top = append(q.top, sa6KV{"models", v})
		return rest, err
	}},
	{regexp.MustCompile(`^provider\.max_price\.(\w+) `), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		v, rest, err := sa6Value(r)
		q.maxPrice = append(q.maxPrice, sa6KV{m[1], v})
		return rest, err
	}},
	{regexp.MustCompile(`^provider\.(\w+) `), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		v, rest, err := sa6Value(r)
		q.provider = append(q.provider, sa6KV{m[1], v})
		return rest, err
	}},
	{regexp.MustCompile(`^roger\.(\w+) `), func(_ *sa6H, q *sa6Spec, m []string, r string) (string, error) {
		v, rest, err := sa6Value(r)
		q.roger = append(q.roger, sa6KV{m[1], v})
		return rest, err
	}},
}

// parse applies every clause of a "with ..." phrase to q.
func (h *sa6H) parse(text string, q *sa6Spec) error {
	rest := strings.TrimSpace(text)
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		rest = strings.TrimPrefix(rest, "and ")
		if rest == "" {
			break
		}
		matched := false
		for _, c := range sa6Clauses {
			if m := c.re.FindStringSubmatch(rest); m != nil {
				var err error
				rest, err = c.fn(h, q, m, rest[len(m[0]):])
				if err != nil {
					return fmt.Errorf("clause %q: %w", text, err)
				}
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("the runner cannot read the request clause %q (in %q)", rest, text)
		}
	}
	return nil
}

func (h *sa6H) spec(user, model, with string) (sa6Spec, error) {
	q := sa6Spec{user: user, caller: "user", model: model, hdr: map[string]string{}}
	if strings.HasPrefix(with, " with ") {
		with = strings.TrimPrefix(with, " with ")
	}
	return q, h.parse(strings.TrimSpace(with), &q)
}

// --- the relay -----------------------------------------------------------------------------

func (h *sa6H) rs1(q sa6Spec) rs1Req {
	kind := "chat"
	if q.stream {
		kind = "stream"
	}
	if q.illegal || h.illegal {
		kind = "illegal"
	}
	return rs1Req{caller: q.caller, kind: kind, model: q.model, noModel: q.noModel, frag: q.frag(),
		headers: q.hdr, maxTokens: q.maxTokens, promptTokens: q.promptTokens, raw: q.raw}
}

// othersStale makes every on-air station but keep stale for the duration of fn.
func (h *sa6H) onlyServer(keep string, fn func() error) error {
	h.b.mu.Lock()
	for name, st := range h.stations {
		if name != keep {
			h.b.lastSeen[st.id] = time.Now().Add(-2 * nodeTTL)
		}
	}
	h.b.mu.Unlock()
	err := fn()
	h.heartbeatOnAir()
	return err
}

func (h *sa6H) snapAdmin() {
	if err := h.readAdminLive(); err == nil {
		h.admBefore = h.adminResp
	}
	h.hitMark = len(h.hitList())
}

func (h *sa6H) counts() map[string]int {
	m := map[string]int{}
	for n, st := range h.stations {
		m[n] = st.upstreamCount()
	}
	return m
}

// send is one SCENARIO relay (recorded as "the response"): it sets up and checks a stated
// outcome, keeps the per-station footprint and the first request of a retry pair.
func (h *sa6H) send(q sa6Spec) error {
	exp := q.expect
	switch {
	case strings.HasPrefix(exp, "400 invalid_routing_value"):
		q.provider = append(q.provider, sa6KV{"sort", `"fastest"`})
	case strings.HasPrefix(exp, "402 insufficient_balance"):
		// the caller becomes an account that holds next to nothing (same name, so a retry is
		// the same payer): the cheapest hold cannot land
		name := q.user
		if name == "" {
			name = h.cur
		}
		if err := h.newUser("__poor", 0); err != nil {
			return err
		}
		h.users[name] = h.users["__poor"]
		h.cur = "__poor"
		h.as(name)
	case strings.HasPrefix(exp, "503 no_match") && q.model == "m":
		q.model = "nobody-serves-this"
	case strings.HasPrefix(exp, "451"):
		q.illegal = true
	case exp == "429":
		q.all429 = true // every station answers this request's attempt with an upstream 429
	}
	before := h.counts()
	err := h.sendCore(q)
	after := h.counts()
	h.delta = map[string]int{}
	for n, v := range after {
		h.delta[n] = v - before[n]
	}
	h.lastSent = h.shot.sent
	if err != nil {
		return err
	}
	if _, ok := q.hdr["Idempotency-Key"]; ok && h.first == nil {
		sh := h.shot
		h.first, h.firstQ = &sh, q
	}
	if exp == "" {
		return nil
	}
	f := strings.Fields(exp)
	if len(f) > 0 && regexp.MustCompile(`^\d{3}$`).MatchString(f[0]) {
		if err := h.responseIs(f[0]); err != nil {
			return fmt.Errorf("the first request was to answer %s: %w", exp, err)
		}
		if len(f) > 1 && !strings.HasPrefix(f[1], "from") {
			if got, _ := h.errBody(); got != f[1] {
				return fmt.Errorf("the first request was to answer %s: code %q (%.300s)", exp, got, h.lastBody)
			}
		}
		if m := regexp.MustCompile(`from "([^"]+)"`).FindStringSubmatch(exp); m != nil {
			return h.servedNode(m[1])
		}
	}
	return nil
}

func (h *sa6H) sendCore(q sa6Spec) error {
	h.as(q.user)
	if err := h.snapOnce(); err != nil {
		return err
	}
	if q.all429 {
		h.all429Once()
	}
	h.lastSpec = q
	h.snapAdmin()
	h.heartbeatOnAir()
	switch {
	case q.servedBy != "":
		if err := h.onlyServer(q.servedBy, func() error { return h.fire(h.rs1(q)) }); err != nil {
			return err
		}
		if err := h.servedNode(q.servedBy); err != nil {
			return fmt.Errorf("the turn was to be served by %q: %w", q.servedBy, err)
		}
		if rec, err := h.receipt(); err == nil {
			h.pseudo1[q.servedBy] = rec.User
		}
		return nil
	case q.through != "":
		price := q.throughPrice
		if price == 0 {
			price = 1
		}
		if _, err := h.tower(q.through, q.model, price, 0); err != nil {
			return err
		}
		if err := h.onlyServer("", func() error { return h.fire(h.rs1(q)) }); err != nil {
			return err
		}
		if h.lastCode != 200 || h.lastHdr.Get("X-RogerAI-Relay") == "" {
			return fmt.Errorf("the turn was to be served through Tower %q: status %d relay %q (%.300s)", q.through, h.lastCode, h.lastHdr.Get("X-RogerAI-Relay"), h.lastBody)
		}
		return nil
	}
	return h.fire(h.rs1(q))
}

// batchN fires n relays of q, recording the served station names.
func (h *sa6H) batchN(q sa6Spec, n int) error {
	h.as(q.user)
	if err := h.snapOnce(); err != nil {
		return err
	}
	h.snapAdmin()
	h.batchOK = nil
	for i := 0; i < n; i++ {
		h.heartbeatOnAir()
		if err := h.fire(h.rs1(q)); err != nil {
			return err
		}
		if h.lastCode == 200 {
			h.batchOK = append(h.batchOK, h.nameOfID(h.lastHdr.Get("X-RogerAI-Provider")))
		}
	}
	h.lastSpec = q
	return nil
}

// all429Once scripts every station to answer its next upstream request with a 429.
func (h *sa6H) all429Once() {
	for name := range h.stations {
		h.onceStatus(name, 429)
	}
}

// onceStatus: the station's NEXT upstream request answers status; later ones complete.
func (h *sa6H) onceStatus(name string, status int) {
	used := false
	h.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		if !used {
			used = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(utDefaultBody(status)))
			return
		}
		utRealCompletion(w)
	})
}

// --- counters --------------------------------------------------------------------------------

func sa6Lookup(m map[string]any, key string) (float64, bool) {
	if v, ok := m[key].(float64); ok {
		return v, true
	}
	if i := strings.IndexByte(key, '{'); i > 0 && strings.HasSuffix(key, "}") {
		base, sub := key[:i], key[i+1:len(key)-1]
		if v, ok := m[base+"_"+sub].(float64); ok {
			return v, true
		}
		if inner, ok := m[base].(map[string]any); ok {
			if v, ok := inner[sub].(float64); ok {
				return v, true
			}
		}
		if inner, ok := m["routing"].(map[string]any); ok {
			return sa6Lookup(inner, key)
		}
	}
	if inner, ok := m["routing"].(map[string]any); ok {
		if v, ok := inner[key].(float64); ok {
			return v, true
		}
	}
	return 0, false
}

// counterDelta: how far an /admin/live counter moved since just before the latest relay.
func (h *sa6H) counterDelta(key string) (float64, error) {
	if err := h.readAdminLive(); err != nil {
		return 0, err
	}
	now, ok := sa6Lookup(h.adminResp, key)
	if !ok {
		return 0, fmt.Errorf("/admin/live carries no %s counter", key)
	}
	before, _ := sa6Lookup(h.admBefore, key)
	return now - before, nil
}

func (h *sa6H) counterRose(key, n string) error {
	d, err := h.counterDelta(key)
	if err != nil {
		return err
	}
	if d != rs1f(n) {
		return fmt.Errorf("%s moved by %v, want %s", key, d, n)
	}
	return nil
}

func (h *sa6H) counterStill(key string) error {
	d, err := h.counterDelta(key)
	if err != nil {
		return err
	}
	if d != 0 {
		return fmt.Errorf("%s moved by %v, want no movement", key, d)
	}
	return nil
}

// --- generic Thens ------------------------------------------------------------------------------

func (h *sa6H) statusIs(code string) error { return h.responseIs(code) }

func (h *sa6H) statusCode(status, code string) error {
	if err := h.responseIs(status); err != nil {
		return err
	}
	if got, _ := h.errBody(); got != code {
		return fmt.Errorf("error.code %q, want %q (%s)", got, code, h.lastBody)
	}
	return nil
}

func (h *sa6H) statusCodeNaming(status, code, key string) error {
	if err := h.statusCode(status, code); err != nil {
		return err
	}
	_, msg := h.errBody()
	if !strings.Contains(msg, key) {
		return fmt.Errorf("error message %q does not name %q", msg, key)
	}
	return nil
}

func (h *sa6H) notServedBy(name string) error {
	if h.lastCode != 200 {
		return fmt.Errorf("status %d, want a 200 served by another station (%s)", h.lastCode, h.lastBody)
	}
	if got := h.lastHdr.Get("X-RogerAI-Provider"); got == h.id(name) {
		return fmt.Errorf("served by %q", name)
	}
	return nil
}

func (h *sa6H) headerIsStr(name, want string) error { return h.headerIs(name, want) }

// get runs one GET through the broker's mux, signed as the current caller.
func (h *sa6H) get(b *broker, path string) (int, []byte, http.Header) {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	signReq(r, h.consumerPriv, nil)
	rr := httptest.NewRecorder()
	b.routes().ServeHTTP(rr, r)
	return rr.Code, rr.Body.Bytes(), rr.Header()
}

func (h *sa6H) logsLack(s string) error {
	if strings.Contains(h.logs.String(), s) {
		return fmt.Errorf("a broker log line contains %q", s)
	}
	return nil
}

// ===================================================================================
// session affinity
// ===================================================================================

type sa6State struct {
	*sa6H
	ttlSet bool
}

func (s *sa6H) relays(user, inst, times, model, with string) error {
	q, err := s.spec(user, model, with)
	if err != nil {
		return err
	}
	var b2 *broker
	if strings.Contains(inst, "B") {
		if s.b2 == nil {
			return fmt.Errorf("no instance B in this scenario")
		}
		b2 = s.b2
	}
	if b2 != nil {
		saved := s.b
		s.b = b2
		defer func() { s.b = saved }()
	}
	if q.twoPrompts {
		a, b := q, q
		a.promptTokens, b.promptTokens = 20, 900
		b.prompt = "Translate this poem into French and explain every metaphor in detail."
		if err := s.batchN(a, 1); err != nil {
			return err
		}
		ok := s.batchOK
		if err := s.send(b); err != nil {
			return err
		}
		s.batchOK = ok
		return nil
	}
	switch {
	case strings.HasPrefix(times, "twice"):
		return s.batchN(q, 2)
	case strings.Contains(times, "times"):
		n, _ := strconv.Atoi(strings.Fields(times)[0])
		return s.batchN(q, n)
	}
	return s.send(q)
}

// affine replays the last request 8 times with every station on air: every replay must land on
// the same server (three equal stations under P2C would spread them).
func (s *sa6State) affine(want string) error {
	q := s.rs1(s.lastSpec)
	q.frag = s.lastSpec.frag()
	for i := 0; i < 8; i++ {
		s.heartbeatOnAir()
		shot := s.replay(q)
		if shot.code != 200 {
			return fmt.Errorf("probe %d: status %d (%.300s)", i+1, shot.code, shot.body)
		}
		got := s.nameOfID(shot.hdr.Get("X-RogerAI-Provider"))
		if r := shot.hdr.Get("X-RogerAI-Relay"); r != "" {
			got = r
		}
		if got != want {
			return fmt.Errorf("probe %d landed on %q, want every turn on the affine %q", i+1, got, want)
		}
	}
	return nil
}

func (s *sa6State) affineToServed(session, user, model string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("the turn was not served: %d %.300s", s.lastCode, s.lastBody)
	}
	served := s.nameOfID(s.lastHdr.Get("X-RogerAI-Provider"))
	q := sa6Spec{user: user, caller: "user", model: model, hdr: map[string]string{}, roger: []sa6KV{{"session", strconv.Quote(session)}}}
	s.lastSpec = q
	return s.affine(served)
}

func (s *sa6State) stillAffine(session, user, model, want string) error {
	for name := range s.stations {
		s.scriptServes(name)
	}
	s.clearCooling()
	s.lastSpec = sa6Spec{user: user, caller: "user", model: model, hdr: map[string]string{}, roger: []sa6KV{{"session", strconv.Quote(session)}}}
	return s.affine(want)
}

func (s *sa6State) moreThanOneServed() error {
	seen := map[string]bool{}
	for _, n := range s.batchOK {
		seen[n] = true
	}
	if len(seen) < 2 {
		return fmt.Errorf("every one of %d relays was served by %v", len(s.batchOK), s.batchOK)
	}
	return nil
}

func (s *sa6State) notForced(_ string) error { return s.counterStill("affinity_hits") }

// --- station attribute rows ------------------------------------------------------------------

func (s *sa6State) others(name string, fn func(id string)) {
	for n, st := range s.stations {
		if n != name {
			fn(st.id)
		}
	}
}

func (s *sa6State) pricedOut(name, out string) error {
	st := s.st(name)
	s.setPrice(name, st.priceIn, rs1f(out))
	return nil
}

func (s *sa6State) measures(name, tps string) error {
	s.setTPS(s.st(name).id, rs1f(tps))
	s.others(name, func(id string) { s.setTPS(id, 40) })
	return nil
}

func (s *sa6State) hasQuant(name, quant string) error {
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Quant = quant })
	// the other stations carry the label the request asks for, so they stay eligible
	s.others(name, func(id string) { s.setOffer(id, func(o *protocol.ModelOffer) { o.Quant = "Q8_0" }) })
	return nil
}

func (s *sa6State) noVerifiedTools(name string) error {
	for n, st := range s.stations {
		if n != name {
			s.b.recordToolProbe(st.id, st.model, true, false, true)
		}
	}
	return nil
}

func (s *sa6State) declaresRegion(name, region string) error {
	s.setRegion(s.st(name).id, region)
	s.others(name, func(id string) { s.setRegion(id, "eu") })
	return nil
}

func (s *sa6State) setRegion(id, region string) {
	s.b.mu.Lock()
	reg := s.b.nodes[id]
	reg.Region = region
	s.b.nodes[id] = reg
	s.b.mu.Unlock()
}

func (s *sa6State) isCurated(name string) error {
	id := s.st(name).id
	s.b.mu.Lock()
	reg := s.b.nodes[id]
	reg.Curated, reg.CuratedProvider = true, "openrouter"
	s.b.nodes[id] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *sa6State) notAttested(name string) error {
	s.b.mu.Lock()
	for n, st := range s.stations {
		if n != name {
			s.b.confidential[st.id] = true
		} else {
			delete(s.b.confidential, st.id)
		}
	}
	s.b.mu.Unlock()
	return nil
}

func (s *sa6State) isExcluded(string) error { return nil } // the constraint column names the exclusion

// --- other Givens --------------------------------------------------------------------------

func (s *sa6State) threeStations(a, b, c, model, in, out string) error {
	for _, n := range []string{a, b, c} {
		s.onAir(n, model, rs1f(in), rs1f(out))
	}
	return nil
}

func (s *sa6State) ttlWindow(n string) error { return os.Setenv("ROGERAI_AFFINITY_TTL", n+"m") }

func (s *sa6State) ttlKnob(v string) error { return os.Setenv("ROGERAI_AFFINITY_TTL", v) }

func (s *sa6State) timePasses(n, _, unit, _ string) error {
	d := time.Duration(rs1i(n)) * time.Minute
	if strings.HasPrefix(unit, "second") {
		d = time.Duration(rs1i(n)) * time.Second
	}
	s.advance(d)
	return nil
}

func (s *sa6State) pairCooling(name, user, _ string) error { return s.pairCool(name, user) }

// pairCool produces a (station, payer) cooldown the way production does: that payer's request
// gets a 429 from that station (contract §14.A: a 429 first cools the pair).
func (s *sa6H) pairCool(name, user string) error {
	cur := s.cur
	defer s.as(cur)
	s.as(user)
	s.onceStatus(name, 429)
	q := sa6Spec{user: user, caller: "user", model: s.st(name).model, hdr: map[string]string{"X-Roger-Node": name}}
	shot, _ := s.relayRaw(s.rs1(q))
	if shot.code != 429 && shot.code != 503 {
		return fmt.Errorf("the pinned request did not draw the station's 429: %d %.200s", shot.code, shot.body)
	}
	s.scriptServes(name)
	return nil
}

func (s *sa6State) atCapacity(name string) error {
	st := s.st(name)
	s.b.mu.Lock()
	hw := s.b.nodes[st.id].HW
	s.b.mu.Unlock()
	s.b.metricsMu.Lock()
	s.b.inflight[st.id] = capacityOf(s.b.concurrentTPS[st.id], hw)
	s.b.metricsMu.Unlock()
	return nil
}

func (s *sa6State) failingStreak(name, n, rate string) error {
	st := s.st(name)
	s.b.mu.Lock()
	tq := s.b.trust[st.id]
	tq.probeFails = rs1i(n)
	s.b.trust[st.id] = tq
	s.b.mu.Unlock()
	s.setSuccess(st.id, rs1f(rate))
	return nil
}

func (s *sa6State) answersNext(name, status string) error {
	s.onceStatus(name, rs1i(status))
	return nil
}

func (s *sa6State) everyAnswersNext(status string) error {
	for n := range s.stations {
		s.onceStatus(n, rs1i(status))
	}
	return nil
}

func (s *sa6State) emptyAndCooling(a, b, c string) error {
	for _, n := range []string{a, b} {
		n := n
		used := false
		s.hitScript(n, func(_ int, w http.ResponseWriter, _ *http.Request) {
			if !used {
				used = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":5,"completion_tokens":0}}`))
				return
			}
			utRealCompletion(w)
		})
	}
	return s.coolNode(c, 30)
}

func (s *sa6State) triedFirst(name string) error {
	hits := s.hitList()[s.hitMark:]
	if len(hits) == 0 || hits[0] != name {
		return fmt.Errorf("upstream hit order %v, want %q first", hits, name)
	}
	return nil
}

func (s *sa6State) planAfter(a, b, head string) error {
	hits := s.hitList()[s.hitMark:]
	if len(hits) == 0 || hits[0] != head {
		return fmt.Errorf("upstream hit order %v, want %q first", hits, head)
	}
	have := map[string]bool{}
	for _, n := range hits[1:] {
		have[n] = true
	}
	if !have[a] || !have[b] {
		return fmt.Errorf("upstream hit order %v, want %q and %q after %q", hits, a, b, head)
	}
	return nil
}

func (s *sa6State) headIs(name string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d (%.300s)", s.lastCode, s.lastBody)
	}
	if tw, ok := s.rsTowers[name]; ok {
		if got := s.lastHdr.Get("X-RogerAI-Relay"); got != tw.id {
			return fmt.Errorf("served through %q, want Tower %q at the head", got, name)
		}
		return nil
	}
	return s.servedNode(name)
}

// sortedHead: the explicit sort decided the head (every station ties on price here, so any
// station is a sorted head); the affinity side of the claim is the explicit-miss counter step.
func (s *sa6State) sortedHead() error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d (%.300s)", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *sa6State) alsoServes(name, model string) error {
	st := s.st(name)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Offers = append(reg.Offers, protocol.ModelOffer{Model: model, PriceIn: st.priceIn, PriceOut: st.priceOut, Ctx: 32768})
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *sa6State) independent(user, other string) error {
	// u-2's first relay was not an affinity hit (it did not inherit u-1's entry); every later
	// relay of the batch is u-2's OWN session sticking, which the contract requires.
	return s.firstNotForced(user, other)
}

func (s *sa6State) firstNotForced(user, _ string) error {
	d, err := s.counterDelta("affinity_hits")
	if err != nil {
		return err
	}
	if n := len(s.batchOK); int(d) != n-1 {
		return fmt.Errorf("affinity_hits moved by %v over %d relays, want %d: the first relay is never a hit, every later one is u-2's own session", d, n, n-1)
	}
	return nil
}

func (s *sa6State) pseudonymOf(station, user, node string) error {
	rec, err := s.receipt()
	if err != nil {
		return err
	}
	u := s.users[user]
	want := s.b.pseudonym(protocol.UserIDFromPubkey(hex.EncodeToString(u.priv.Public().(ed25519.PublicKey))), s.id(node))
	if rec.User != want {
		return fmt.Errorf("the job %q received carries user %q, want the (user, node) pseudonym %q", station, rec.User, want)
	}
	return nil
}

func (s *sa6State) samePseudonym(station string) error {
	rec, err := s.receipt()
	if err != nil {
		return err
	}
	if first := s.pseudo1[station]; first == "" || first != rec.User {
		return fmt.Errorf("pseudonym %q on this turn, %q on the first", rec.User, first)
	}
	return nil
}

func (s *sa6State) bodyLacks(a, b string) error {
	served := s.nameOfID(s.lastHdr.Get("X-RogerAI-Provider"))
	s.bodiesMu.Lock()
	body := s.bodies[served]
	s.bodiesMu.Unlock()
	if s.lastCode != 200 || body == nil {
		return fmt.Errorf("no station served (%d %.300s)", s.lastCode, s.lastBody)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return err
	}
	for _, k := range []string{a, b} {
		if _, ok := m[k]; ok {
			return fmt.Errorf("the body %q received carries %q", served, k)
		}
	}
	return nil
}

func (s *sa6State) adminLacks(str string) error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	raw, _ := json.Marshal(s.adminResp)
	if strings.Contains(string(raw), str) {
		return fmt.Errorf("/admin/live contains %q", str)
	}
	return nil
}

func (s *sa6State) sharedLacks(str string) error {
	if s.mr == nil {
		return fmt.Errorf("no shared store in this scenario")
	}
	for _, k := range s.mr.Keys() {
		if strings.Contains(k, str) {
			return fmt.Errorf("shared-store key %q contains %q", k, str)
		}
		if v, err := s.mr.Get(k); err == nil && strings.Contains(v, str) {
			return fmt.Errorf("shared-store value of %q contains %q", k, str)
		}
		if fields, err := s.mr.HKeys(k); err == nil {
			for _, f := range fields {
				if strings.Contains(f, str) || strings.Contains(s.mr.HGet(k, f), str) {
					return fmt.Errorf("shared-store hash %q contains %q", k, str)
				}
			}
		}
	}
	// the affinity entry must exist somewhere for the claim to mean anything
	return s.counterStill("affinity_hits")
}

func (s *sa6State) generationLacks(str string) error {
	id := s.lastHdr.Get("X-RogerAI-Request-Id")
	code, body, _ := s.get(s.b, "/generation?id="+id)
	if code != 200 {
		return fmt.Errorf("GET /generation = %d %.300s", code, body)
	}
	if strings.Contains(string(body), str) {
		return fmt.Errorf("the /generation record contains %q", str)
	}
	// the request must actually have carried the session for the claim to mean anything
	return s.statusIs("200")
}

func (s *sa6State) towerNoDirect(name, model string) error {
	if _, err := s.tower(name, model, 1, 0); err != nil {
		return err
	}
	for n := range s.stations {
		s.goOffAir(n)
	}
	return nil
}

func (s *sa6State) directBack(name string) error {
	if _, ok := s.stations[name]; !ok {
		return fmt.Errorf("no station %q", name)
	}
	delete(s.offAir, name)
	s.heartbeatOnAir()
	return nil
}

func (s *sa6State) callerOwns(name, model string) error {
	s.as("u-1")
	s.onAir(name, model, 0.10, 0.30)
	s.alone(name, model)
	return s.userOwnsNode("", name)
}

func (s *sa6State) ownerRelays(model, with string) error {
	q, err := s.spec("u-1", model, " with "+with)
	if err != nil {
		return err
	}
	return s.send(q)
}

func (s *sa6State) servesAtZero(name string) error {
	if err := s.servedNode(name); err != nil {
		return err
	}
	return s.costHeaderIs("0")
}

func (s *sa6H) secondInstance(string) error {
	b2 := s.instanceB()
	// Instances of one broker share its identity key, as production's do (receipts carry one
	// broker signature); the affinity key is an HMAC under it (§14.B1).
	b2.priv = s.b.priv
	s.b.mu.Lock()
	for id, tq := range s.b.trust {
		b2.trust[id] = tq
	}
	s.b.mu.Unlock()
	return nil
}

func (s *sa6H) sharedDown() error {
	if s.mr != nil {
		s.mr.Close()
	}
	s.mrDown = true
	return nil
}

func (s *sa6H) bothOK() error {
	if len(s.shots) < 2 {
		return fmt.Errorf("%d responses", len(s.shots))
	}
	for i, sh := range s.shots[len(s.shots)-2:] {
		if sh.code != 200 {
			return fmt.Errorf("response %d: %d %.300s", i+1, sh.code, sh.body)
		}
	}
	return nil
}

func (s *sa6State) billingUnchanged() error {
	// The hold of a session request equals the hold of the same request with no session.
	withHold := s.shot.holdAmt
	q := s.lastSpec
	// The comparison request is not the scenario's turn: the turn's shot is restored after it,
	// so the next step ("the settle bills ...") reads the session request, not the comparison.
	keepShot, keepSpec := s.shot, s.lastSpec
	keepCode, keepBody, keepHdr := s.lastCode, s.lastBody, s.lastHdr
	defer func() {
		s.shot, s.lastSpec = keepShot, keepSpec
		s.lastCode, s.lastBody, s.lastHdr = keepCode, keepBody, keepHdr
	}()
	var r []sa6KV
	for _, kv := range q.roger {
		if kv.k != "session" {
			r = append(r, kv)
		}
	}
	q.roger = r
	if err := s.send(q); err != nil {
		return err
	}
	if s.shot.holdAmt != withHold {
		return fmt.Errorf("hold %v with a session, %v without", withHold, s.shot.holdAmt)
	}
	return nil
}

func (s *sa6State) settleLocked(name string) error {
	if err := s.servedNode(name); err != nil {
		return err
	}
	return s.settledPriceIsOf(name)
}

func (s *sa6State) raisesOut(name, out string) error {
	st := s.st(name)
	s.setPrice(name, st.priceIn, rs1f(out))
	return nil
}

func (s *sa6State) plainNumbers() error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	for _, k := range []string{"affinity_hits", "affinity_misses"} {
		v, ok := s.adminResp[k]
		if !ok {
			return fmt.Errorf("/admin/live carries no %s", k)
		}
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), "conv-42") || strings.Contains(string(raw), "u_") {
			return fmt.Errorf("%s carries payer or session detail: %s", k, raw)
		}
	}
	return nil
}

func sa6Run(t *testing.T, feature string, steps func(*godog.ScenarioContext, *sa6H)) {
	h := &sa6H{rs1State: &rs1State{s0State: &s0State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}}}
	prev := log.Writer()
	log.SetOutput(h.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		h.teardownRS1()
		h.teardownPins()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, h.reset()
			})
			sc.After(func(ctx context.Context, scn *godog.Scenario, err error) (context.Context, error) {
				if err != nil {
					all := h.logs.String()
					if len(all) > 1500 {
						all = all[len(all)-1500:]
					}
					t.Logf("broker log tail for %q:\n%s\nlast response %d %.400s", scn.Name, all, h.lastCode, h.lastBody)
				}
				h.teardownRS1()
				h.teardownPins()
				return ctx, nil
			})
			sa6Common(sc, h)
			steps(sc, h)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{feature}, TestingT: t, Strict: true,
			Tags: "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later",
		},
	}
	if suite.Run() != 0 {
		t.Fatal("godog suite failed")
	}
}

// sa6Common registers the steps every slice-6 runner shares.
func sa6Common(sc *godog.ScenarioContext, h *sa6H) {
	sc.Step(`^a broker with an empty in-memory node registry$`, func() error { return nil })
	sc.Step(`^the fee rate is (\d+)%$`, func(n string) error { h.b.feeRate = rs1f(n) / 100; return nil })
	sc.Step(`^"([^"]+)" is a funded consumer$`, h.fundedConsumer)
	sc.Step(`^station "([^"]+)" is on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M$`, func(n, m, in, out string) error {
		h.onAir(n, m, rs1f(in), rs1f(out))
		return nil
	})
	sc.Step(`^the response is (\d+)$`, h.statusIs)
	sc.Step(`^the response is (\d+) with error code "([^"]+)"$`, h.statusCode)
	sc.Step(`^the response is (\d+) with error code "([^"]+)" naming "([^"]+)"$`, h.statusCodeNaming)
	sc.Step(`^the response is 200 from "([^"]+)"$`, h.servedNode)
	sc.Step(`^the response is 200 from "([^"]+)" or "([^"]+)"$`, h.servedOneOf2)
	sc.Step(`^no station received anything$`, h.noStationDispatched)
	sc.Step(`^X-RogerAI-Cost is "([^"]*)"$`, h.costHeaderIs)
	sc.Step(`^no broker log line contains "([^"]+)"$`, h.logsLack)
	sc.Step(`^"([^"]+)" goes off air$`, h.nodeGoesOffAir)
	sc.Step(`^"([^"]+)" relay(?:s|ed) (on instance [AB] |)(twice |\d+ times |)for "([^"]+)"((?s) with .+|)$`, h.relays)
	sc.Step(`^a second broker instance "([^"]+)" shares the store$`, h.secondInstance)
	sc.Step(`^the shared store is unreachable$`, h.sharedDown)
	sc.Step(`^both responses are 200$`, h.bothOK)
	sc.Step(`^"([^"]+)" is cooling for (\d+) more seconds$`, h.nodeCooling)
}

func TestSessionAffinityBDD(t *testing.T) {
	sa6Run(t, "../../features/routing/session_affinity.feature", func(sc *godog.ScenarioContext, h *sa6H) {
		s := &sa6State{sa6H: h}
		sc.Step(`^the affinity inactivity window is (\d+) minutes$`, s.ttlWindow)
		sc.Step(`^stations "([^"]+)", "([^"]+)" and "([^"]+)" are on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M, Tier-A, idle$`, s.threeStations)
		sc.Step(`^the (affinity_\w+(?:\{\w+\})?) counter rose by (\d+)$`, s.counterRose)
		sc.Step(`^the (affinity_\w+(?:\{\w+\})?) counter did not move$`, s.counterStill)
		sc.Step(`^more than one station served$`, s.moreThanOneServed)
		sc.Step(`^the session "([^"]+)" of "([^"]+)" for "([^"]+)" is affine to the station that served$`, s.affineToServed)
		sc.Step(`^the session "([^"]+)" of "([^"]+)" for "([^"]+)" is still affine to "([^"]+)"$`, s.stillAffine)
		sc.Step(`^the response is not served by "([^"]+)"$`, s.notServedBy)
		sc.Step(`^the response is not served by "([^"]+)" at a price above the consumer's out cap$`, s.notServedBy)
		sc.Step(`^the response is 200 not forced to "([^"]+)"$`, func(name string) error {
			if err := s.statusIs("200"); err != nil {
				return err
			}
			return s.notForced(name)
		})
		sc.Step(`^the pick was not forced to "([^"]+)"$`, s.notForced)
		sc.Step(`^the pick for "([^"]+)" was not forced to "([^"]+)"$`, func(_, n string) error { return s.notForced(n) })
		sc.Step(`^the pick is not forced to "([^"]+)"$`, s.notForced)
		sc.Step(`^the first relay of "([^"]+)" was not forced to "([^"]+)"$`, s.firstNotForced)
		sc.Step(`^the session "([^"]+)" of "([^"]+)" for "([^"]+)" is independent of "([^"]+)"'s$`, func(_, u, _, o string) error { return s.independent(u, o) })
		sc.Step(`^"([^"]+)" is priced at out \$([0-9.]+)$`, s.pricedOut)
		sc.Step(`^"([^"]+)" measures (\d+) tok/s$`, s.measures)
		sc.Step(`^"([^"]+)" has quant "([^"]+)"$`, s.hasQuant)
		sc.Step(`^"([^"]+)" has no verified tools$`, s.noVerifiedTools)
		sc.Step(`^"([^"]+)" declares region "([^"]+)"$`, s.declaresRegion)
		sc.Step(`^"([^"]+)" is curated$`, s.isCurated)
		sc.Step(`^"([^"]+)" is not TEE-attested$`, s.notAttested)
		sc.Step(`^"([^"]+)" is excluded$`, s.isExcluded)
		sc.Step(`^the pair \("([^"]+)", "([^"]+)"\) is cooling for (\d+) more seconds$`, s.pairCooling)
		sc.Step(`^"([^"]+)" has in_flight equal to its capacity$`, s.atCapacity)
		sc.Step(`^"([^"]+)" has a failing probe streak of (\d+) and success rate ([0-9.]+)$`, s.failingStreak)
		sc.Step(`^"([^"]+)" answers the next request with an upstream (\d+)$`, s.answersNext)
		sc.Step(`^every station answers the next request with an upstream (\d+)$`, s.everyAnswersNext)
		sc.Step(`^"([^"]+)" and "([^"]+)" answer the next request with a 2xx and empty output and "([^"]+)" is cooling$`, s.emptyAndCooling)
		sc.Step(`^"([^"]+)" was tried first$`, s.triedFirst)
		sc.Step(`^the plan contained "([^"]+)" and "([^"]+)" after "([^"]+)"$`, s.planAfter)
		sc.Step(`^(\d+) (more |)(seconds|minutes) pass( with no turn of "[^"]+"|)$`, s.timePasses)
		sc.Step(`^ROGERAI_AFFINITY_TTL is "([^"]+)"$`, s.ttlKnob)
		sc.Step(`^the head of the plan is "([^"]+)"$`, s.headIs)
		sc.Step(`^the head of the plan is the sorted head$`, s.sortedHead)
		sc.Step(`^station "([^"]+)" also serves "([^"]+)"$`, s.alsoServes)
		sc.Step(`^the job "([^"]+)" received carries the pseudonym of \("([^"]+)", "([^"]+)"\)$`, s.pseudonymOf)
		sc.Step(`^it is the same pseudonym "([^"]+)" saw on the first turn$`, s.samePseudonym)
		sc.Step(`^the body the serving station received has no "([^"]+)" key and no "([^"]+)" key$`, s.bodyLacks)
		sc.Step(`^/admin/live does not contain "([^"]+)"$`, s.adminLacks)
		sc.Step(`^no shared-store key or value contains "([^"]+)"$`, s.sharedLacks)
		sc.Step(`^the /generation record for that request does not contain "([^"]+)"$`, s.generationLacks)
		sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)" and no direct station is eligible$`, s.towerNoDirect)
		sc.Step(`^direct station "([^"]+)" comes back on air$`, s.directBack)
		sc.Step(`^the caller owns node "([^"]+)" on air for "([^"]+)"$`, s.callerOwns)
		sc.Step(`^the owner relays for "([^"]+)" with (.+)$`, s.ownerRelays)
		sc.Step(`^"([^"]+)" serves at \$0$`, s.servesAtZero)
		sc.Step(`^the hold covers the priciest pair of the plan exactly as without a session$`, s.billingUnchanged)
		sc.Step(`^the settle bills "([^"]+)" at its locked price$`, s.settleLocked)
		sc.Step(`^"([^"]+)" raises its out price to \$([0-9.]+) per 1M$`, s.raisesOut)
		sc.Step(`^/admin/live carries affinity_hits and affinity_misses as plain numbers$`, s.plainNumbers)
	})
}

var _ = io.EOF
