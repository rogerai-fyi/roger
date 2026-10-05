package main

// key_guardrails_bdd_test.go makes features/auth/key_guardrails.feature EXECUTABLE (slice 5,
// RED): account keys - `Bearer rog-key_<secret>` credentials an account mints for itself, with
// a spend limit over a UTC window, an expiry, allow-lists, and a management surface under
// /account/keys (CONTRACT §11). It also carries the SHARED harness key_limits_bdd_test.go
// (features/relay/key_limits.feature) builds on.
//
// REAL DEPS, NO MOCKS. kg5State embeds the generation runner's state (gl3State -> sr3State ->
// rpState -> foState): the real relayBroker over the real store (Postgres when
// ROGERAI_TEST_DATABASE_URL is set, else the in-memory reference), the shared store over
// miniredis, real stations behind real tunnels with scripted upstreams, the real Tower fabric
// when a scenario needs one, and instanceB for a second broker over the same stores. Every
// management call goes through the broker's own mux (b.routes()), every relay through
// b.relay, signed with the account's bound device key, a web-session cookie, or a bearer.
//
// RED: no key object exists. POST /account/keys is not routed, so every Given that mints a key
// fails on its real assertion ("POST /account/keys = 404, want 201"), and the Thens - written
// against the contract's observable surface - are what GREEN must satisfy.
//
// OBSERVATION CHOICES (stated, so GREEN knows what the steps read):
//   - The list endpoint's envelope is not fixed by the contract; kg5Entries accepts a JSON
//     array, or an object holding the array under "keys" or "data".
//   - "Spent $X this window" is produced by REAL relays bearing the key against a dedicated
//     spender station (model "kg5-spend", $0 in / $100 per 1M out, a 4M-token window) that
//     claims exactly X*10^4 completion tokens, so the settled cost is exactly X.
//   - Window placement uses the broker's clock seam (b.nowFn = foState.now); a scenario that
//     says "yesterday" moves that clock before spending. The key window must be computed from
//     the same clock for these steps to hold.
//   - "The store holds sha256(secret) and never the secret" scans the whole store: every text
//     column of every table in the rogerai schema on Postgres, the full %#v dump of the
//     in-memory store otherwise, plus every miniredis key and value.
//   - Constant-work claims compare shared-store (miniredis) command counts between a miss and
//     a hit-that-refuses; no timing is asserted (CONTRACT §11).
//   - Scenarios that name a key with no Given minting it ("acct-a PATCHes /account/keys/k1 with
//     {}", "DELETEs k1 twice", the audit outline...) mint it implicitly first, so the step tests
//     what its sentence says; reported as a spec gap.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// ---- state ------------------------------------------------------------------------------------

type kg5Acct struct {
	label string
	alias string // the wallet name the feature uses ("u_gh_a")
	who   *gl3Who
	gid   int64
}

type kg5Key struct {
	label, id, secret, acct string
}

type kg5Resp struct {
	code  int
	hdr   http.Header
	body  []byte
	js    map[string]any
	reqID string
	snap  http.Header // the headers as committed at the first write (before any frame)
}

type kg5Req struct {
	b      *broker
	as     string
	method string
	path   string
	body   []byte
	hdr    map[string]string
}

type kg5State struct {
	ownServed      *kl5OwnServed  // /console both views: the relay acct-a's own station served
	consoleJS      map[string]any // the last /console payload read
	consoleRelayID string         // the relay request id captured before reading /console
	*gl3State

	accts   map[string]*kg5Acct
	keys    map[string]*kg5Key
	resp    kg5Resp
	resps   []kg5Resp
	lastReq *kg5Req
	signer  string // the account whose device key signs management requests

	unbound    ed25519.PrivateKey
	countMark  int                       // keys listed before a refused mint
	snaps      map[string]map[string]any // a key's entry before an action
	lastPosted map[string]any            // the body of the last mint, parsed
	lastLabel  string                    // the label of the last minted key
	codes      []int                     // outcomes of a sequence of relays
	promptEst  int                       // the prompt estimate the relay body is padded to (0 = short)
	maxTokens  int                       // max_tokens the relay body carries (0 = absent)
	spendTok   int                       // the spender station's claimed completion tokens

	gate     chan struct{} // releases a held station answer
	flight   chan kg5Resp  // the held relay's outcome
	mcounts  [2]int        // shared-store command counts for a constant-work pair
	savedTZ  *time.Location
	at0      time.Time // a moment a scenario names ("T")
	bandCode string
	heldP    int             // the held station's claimed prompt tokens (0 = 40)
	explicit map[string]bool // stations a scenario scripted a cost or claim for
	spendReq string          // the request id of the last fixture spend relay
	heldC    int             // the held station's claimed completion tokens (0 = 20)
}

func newKG5(t *testing.T) *kg5State {
	return &kg5State{gl3State: &gl3State{sr3State: &sr3State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}}}
}

func (k *kg5State) reset() error {
	if err := k.gl3State.reset(); err != nil {
		return err
	}
	k.accts, k.keys = map[string]*kg5Acct{}, map[string]*kg5Key{}
	k.resp, k.resps, k.lastReq, k.signer = kg5Resp{}, nil, nil, ""
	k.unbound, k.countMark, k.snaps, k.lastPosted, k.lastLabel = nil, -1, map[string]map[string]any{}, nil, ""
	k.codes, k.promptEst, k.maxTokens, k.spendTok = nil, 0, 0, 0
	k.gate, k.flight, k.mcounts, k.at0, k.bandCode = nil, nil, [2]int{}, time.Time{}, ""
	k.heldP, k.heldC = 0, 0
	k.ownServed, k.consoleJS, k.consoleRelayID = nil, nil, ""
	k.explicit = map[string]bool{}
	edgeCoinForTest = nil
	k.model = "qwen3-32b"
	return nil
}

func (k *kg5State) teardown() {
	if k.gate != nil {
		select {
		case <-k.gate:
		default:
			close(k.gate)
		}
		if k.flight != nil {
			select {
			case <-k.flight:
			case <-time.After(5 * time.Second):
			}
		}
		k.gate, k.flight = nil, nil
	}
	edgeCoinForTest = nil
	if k.savedTZ != nil {
		time.Local = k.savedTZ
		k.savedTZ = nil
	}
	k.gl3State.teardown()
}

// ---- accounts, clock --------------------------------------------------------------------------

func (k *kg5State) account(label, alias string, bal float64) error {
	w := k.boundAccount(label, bal)
	gid, err := strconv.ParseInt(strings.TrimPrefix(w.wallet, "u_gh_"), 10, 64)
	if err != nil {
		return fmt.Errorf("account %s: wallet %q is not a GitHub account wallet", label, w.wallet)
	}
	k.accts[label] = &kg5Acct{label: label, alias: alias, who: w, gid: gid}
	return nil
}

func (k *kg5State) acct(label string) (*kg5Acct, error) {
	a, ok := k.accts[label]
	if !ok {
		return nil, fmt.Errorf("no account %q in this scenario", label)
	}
	return a, nil
}

// wallet resolves a feature wallet name ("u_gh_a") or an account label to the real wallet id.
func (k *kg5State) walletOf(name string) (string, error) {
	for _, a := range k.accts {
		if a.alias == name || a.label == name {
			return a.who.wallet, nil
		}
	}
	return "", fmt.Errorf("no account has wallet %q in this scenario", name)
}

func (k *kg5State) at(ts time.Time) {
	k.clockMu.Lock()
	k.clockBase, k.clockOffset = ts, 0
	k.clockMu.Unlock()
}

func kg5Time(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, strings.TrimSpace(s))
}

// ---- management calls -------------------------------------------------------------------------

func (k *kg5State) authFor(as string) (func(r *http.Request, body []byte), error) {
	kind, label, _ := strings.Cut(as, ":")
	switch kind {
	case "acct":
		a, err := k.acct(label)
		if err != nil {
			return nil, err
		}
		return func(r *http.Request, body []byte) { signReq(r, a.who.priv, body) }, nil
	case "cookie", "cookie-foreign":
		a, err := k.acct(label)
		if err != nil {
			return nil, err
		}
		origin := pbOrigin
		if kind == "cookie-foreign" {
			origin = "https://attacker.example"
		}
		cookie := k.b.signSessionWallet(a.who.login, a.gid, a.who.wallet, time.Now().Add(time.Hour).Unix())
		return func(r *http.Request, _ []byte) {
			r.Header.Set("Origin", origin)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}, nil
	case "bearer":
		secret, err := k.secretOf(label)
		if err != nil {
			return nil, err
		}
		return func(r *http.Request, _ []byte) { r.Header.Set("Authorization", "Bearer "+secret) }, nil
	case "unsigned":
		a, err := k.acct(label)
		if err != nil {
			return nil, err
		}
		return func(r *http.Request, _ []byte) { r.Header.Set("X-Roger-User", a.who.wallet) }, nil
	case "unbound":
		if k.unbound == nil {
			_, k.unbound, _ = ed25519.GenerateKey(nil)
		}
		return func(r *http.Request, body []byte) { signReq(r, k.unbound, body) }, nil
	}
	return nil, fmt.Errorf("unknown caller %q", as)
}

// secretOf: a minted key's secret, a grant's secret, or a raw rog-key_ literal.
func (k *kg5State) secretOf(label string) (string, error) {
	if strings.HasPrefix(label, "rog-key_") || strings.HasPrefix(label, "rog-grant_") {
		return label, nil
	}
	if kk, ok := k.keys[label]; ok {
		return kk.secret, nil
	}
	if s, ok := k.grants[label]; ok {
		return s, nil
	}
	return "", fmt.Errorf("no key or grant %q in this scenario (was it minted?)", label)
}

func (k *kg5State) call(rq kg5Req) (kg5Resp, error) {
	if rq.b == nil {
		rq.b = k.b
	}
	auth, err := k.authFor(rq.as)
	if err != nil {
		return kg5Resp{}, err
	}
	r := httptest.NewRequest(rq.method, rq.path, bytes.NewReader(rq.body))
	if rq.body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for h, v := range rq.hdr {
		r.Header.Set(h, v)
	}
	auth(r, rq.body)
	rr := httptest.NewRecorder()
	rq.b.routes().ServeHTTP(rr, r)
	res := kg5Resp{code: rr.Code, hdr: rr.Header(), body: rr.Body.Bytes()}
	_ = json.Unmarshal(res.body, &res.js)
	cp := rq
	k.lastReq = &cp
	k.resp = res
	k.resps = append(k.resps, res)
	return res, nil
}

func (k *kg5State) do(as, method, path string, body []byte) error {
	_, err := k.call(kg5Req{as: as, method: method, path: path, body: body})
	return err
}

// keyID resolves a key label (or literal id) to the id a path carries.
func (k *kg5State) keyID(label string) string {
	if kk, ok := k.keys[label]; ok {
		return kk.id
	}
	return label
}

// ensureKey mints label for acct when no Given did (the scenario names it as existing).
func (k *kg5State) ensureKey(acct, label string) error {
	if _, ok := k.keys[label]; ok {
		return nil
	}
	return k.mint(acct, label, `{"name":"`+label+`"}`, nil)
}

func (k *kg5State) mint(acct, label, body string, hdr map[string]string) error {
	if strings.TrimSpace(body) == "" {
		body = `{}`
	}
	res, err := k.call(kg5Req{as: "acct:" + acct, method: http.MethodPost, path: "/account/keys", body: []byte(body), hdr: hdr})
	if err != nil {
		return err
	}
	if res.code != http.StatusCreated {
		return fmt.Errorf("POST /account/keys as %s = %d, want 201: %.300s", acct, res.code, res.body)
	}
	return k.recordMint(acct, label, res)
}

func (k *kg5State) recordMint(acct, label string, res kg5Resp) error {
	id, _ := res.js["id"].(string)
	secret, _ := res.js["secret"].(string)
	if !strings.HasPrefix(id, "key_") || !strings.HasPrefix(secret, "rog-key_") {
		return fmt.Errorf("mint answered 201 without a key_ id and a rog-key_ secret: %.300s", res.body)
	}
	k.keys[label] = &kg5Key{label: label, id: id, secret: secret, acct: acct}
	k.lastLabel = label
	return nil
}

// ownerOfKey is the account that minted label (acct-a unless recorded otherwise).
func (k *kg5State) ownerOfKey(label string) string {
	if kk, ok := k.keys[label]; ok {
		return kk.acct
	}
	return "acct-a"
}

// entries reads GET /account/keys as acct.
func (k *kg5State) entries(acct string) ([]map[string]any, error) {
	res, err := k.call(kg5Req{as: "acct:" + acct, method: http.MethodGet, path: "/account/keys"})
	if err != nil {
		return nil, err
	}
	if res.code != 200 {
		return nil, fmt.Errorf("GET /account/keys as %s = %d, want 200: %.300s", acct, res.code, res.body)
	}
	return kg5Entries(res.body)
}

func kg5Entries(body []byte) ([]map[string]any, error) {
	var arr []map[string]any
	if json.Unmarshal(body, &arr) == nil {
		return arr, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("the key list is not JSON: %.300s", body)
	}
	for _, f := range []string{"keys", "data"} {
		if raw, ok := obj[f]; ok {
			if err := json.Unmarshal(raw, &arr); err != nil {
				return nil, fmt.Errorf("the key list's %q is not a list: %.300s", f, raw)
			}
			return arr, nil
		}
	}
	return nil, fmt.Errorf("the key list has no array (top level, \"keys\" or \"data\"): %.300s", body)
}

// entry reads one key's entry by id as its owner.
func (k *kg5State) entry(label string) (map[string]any, error) {
	return k.entryOn(k.b, label)
}

func (k *kg5State) entryOn(b *broker, label string) (map[string]any, error) {
	owner := k.ownerOfKey(label)
	// A read for an assertion must not replace the scenario's last response (a later step
	// reads its status, cost header or request id).
	prev, prevs, prevReq := k.resp, k.resps, k.lastReq
	defer func() { k.resp, k.resps, k.lastReq = prev, prevs, prevReq }()
	res, err := k.call(kg5Req{b: b, as: "acct:" + owner, method: http.MethodGet, path: "/account/keys/" + k.keyID(label)})
	if err != nil {
		return nil, err
	}
	if res.code != 200 || res.js == nil {
		return nil, fmt.Errorf("GET /account/keys/%s as %s = %d, want 200: %.300s", k.keyID(label), owner, res.code, res.body)
	}
	if e, ok := res.js["key"].(map[string]any); ok {
		return e, nil
	}
	return res.js, nil
}

func (k *kg5State) count(acct string) (int, error) {
	es, err := k.entries(acct)
	return len(es), err
}

func (k *kg5State) markCount(acct string) {
	n, err := k.count(acct)
	if err != nil {
		n = -1
	}
	k.countMark = n
}

func (k *kg5State) snap(label string) {
	if e, err := k.entry(label); err == nil {
		k.snaps[label] = e
	}
}

func kg5Num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func kg5Near(a, b float64) bool { return math.Abs(a-b) < 5e-7 }

func kg5StrList(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		if v == nil {
			return nil, true
		}
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func (k *kg5State) fieldNum(e map[string]any, f string) (float64, error) {
	v, ok := e[f]
	if !ok {
		return 0, fmt.Errorf("the key entry has no %q: %v", f, e)
	}
	n, ok := kg5Num(v)
	if !ok {
		return 0, fmt.Errorf("the key entry's %q is %v (%T), not a number", f, v, v)
	}
	return n, nil
}

func (k *kg5State) wantNum(label, f string, want float64) error {
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	got, err := k.fieldNum(e, f)
	if err != nil {
		return err
	}
	if !kg5Near(got, want) {
		return fmt.Errorf("%s %s = %v, want %v (entry %v)", label, f, got, want, e)
	}
	return nil
}

// ---- response assertions ----------------------------------------------------------------------

func (k *kg5State) errCode() string {
	if e, ok := k.resp.js["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

func (k *kg5State) errMsg() string {
	if e, ok := k.resp.js["error"].(map[string]any); ok {
		if m, ok := e["message"].(string); ok {
			return m
		}
	}
	return ""
}

func (k *kg5State) status(want int) error {
	if k.resp.code != want {
		return fmt.Errorf("status %d, want %d: %.400s", k.resp.code, want, k.resp.body)
	}
	return nil
}

func (k *kg5State) statusCode(want int, code string) error {
	if err := k.status(want); err != nil {
		return err
	}
	if got := k.errCode(); got != code {
		return fmt.Errorf("error.code %q, want %q: %.400s", got, code, k.resp.body)
	}
	return nil
}

func (k *kg5State) statusMsg(want int, msg string) error {
	if err := k.status(want); err != nil {
		return err
	}
	if got := k.errMsg(); got != msg && !strings.Contains(got, msg) {
		return fmt.Errorf("error.message %q, want %q", got, msg)
	}
	return nil
}

func (k *kg5State) names(field string) error {
	if !strings.Contains(k.errMsg(), field) {
		return fmt.Errorf("error.message %q does not name %q: %.400s", k.errMsg(), field, k.resp.body)
	}
	return nil
}

// ---- stations and relays ----------------------------------------------------------------------

func (k *kg5State) ensureStation(name, model string, in, out float64) *fstation {
	if st, ok := k.stations[name]; ok {
		return st
	}
	saved := k.model
	k.model = model
	st := k.station(name, in, out)
	k.model = saved
	k.scriptJSON(name, 20, "", 0)
	return st
}

// ensurePriced: a paid station for the scenario model when none is on air yet.
func (k *kg5State) ensurePriced() {
	for _, st := range k.stations {
		if st.model == k.model && (st.priceIn > 0 || st.priceOut > 0) {
			return
		}
	}
	k.ensureStation("kg5-priced", k.model, 1, 2)
}

func (k *kg5State) spender() *fstation {
	if st, ok := k.stations["kg5-spender"]; ok {
		return st
	}
	st := k.standUp("kg5-spender", stationOpts{model: "kg5-spend", priceIn: 0, priceOut: 100, ctx: 4_000_000})
	k.b.mu.Lock()
	k.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	k.b.mu.Unlock()
	k.scripted["kg5-spender"] = true
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":0,"completion_tokens":%d}}`,
			sr3Marker+" spend", k.spendTok)
	})
	return st
}

// spend settles exactly amount against key label through a real relay on the spender.
func (k *kg5State) spend(label string, amount float64) error {
	return k.spendOn(k.b, label, amount)
}

func (k *kg5State) spendOn(b *broker, label string, amount float64) error {
	st := k.spender()
	if b != k.b && b.nodes[st.id].NodeID == "" {
		b.nodes[st.id] = k.b.nodes[st.id]
		b.lastSeen[st.id] = time.Now()
		b.tunnels[st.id] = k.b.tunnels[st.id]
		b.trust[st.id] = k.b.trust[st.id]
	}
	k.spendTok = int(math.Round(amount * 1e4))
	body, _ := json.Marshal(map[string]any{
		"model":      "kg5-spend",
		"messages":   []map[string]string{{"role": "user", "content": "spend"}},
		"max_tokens": k.spendTok,
	})
	// The spender is priced at $100/1M out, above the $10/1M default consumer out-cap a relay
	// with no cap gets (pricesafety.go), so the spend relay states its cap like a CLI does.
	res, err := k.relayRaw(b, label, body, map[string]string{"X-Roger-Max-Price-Out": "100"})
	if err != nil {
		return err
	}
	k.spendReq = res.reqID
	if res.code != 200 {
		return fmt.Errorf("spending $%.6f through %s = %d, want 200: %.300s", amount, label, res.code, res.body)
	}
	if c, _ := strconv.ParseFloat(res.hdr.Get("X-RogerAI-Cost"), 64); !kg5Near(c, amount) {
		return fmt.Errorf("spending $%.6f through %s settled X-RogerAI-Cost %q", amount, label, res.hdr.Get("X-RogerAI-Cost"))
	}
	return nil
}

// relayBody is the scenario relay body: model, a prompt padded so the hold estimate is exact
// when promptEst is set, max_tokens when set, and any extra keys.
func (k *kg5State) relayBody(model string, stream bool, extra map[string]any) []byte {
	// measured: the body the broker estimates from - the routing object stripped, the stream
	// usage option merged - so the padding is sized on THAT body, not the one sent.
	build := func(n int, measured bool) []byte {
		m := map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": strings.Repeat("w", n)}}}
		if k.maxTokens > 0 {
			m["max_tokens"] = k.maxTokens
		}
		if stream {
			m["stream"] = true
			if measured { // the broker merges this into a stream before it estimates (ensureStreamIncludeUsage)
				m["stream_options"] = map[string]any{"include_usage": true}
			}
		}
		for kk, v := range extra {
			if !measured || kk != "provider" {
				m[kk] = v
			}
		}
		b, _ := json.Marshal(m)
		return b
	}
	if k.promptEst <= 0 {
		return build(40, false)
	}
	want := (k.promptEst - 1) * 4
	n := 1
	for i := 0; i < 6; i++ {
		b := build(n, true)
		if len(b) == want {
			break
		}
		n += want - len(b)
		if n < 1 {
			n = 1
		}
	}
	return build(n, false)
}

// relayRaw fires one relay on b bearing label ("" = no bearer, signed as the consumer when
// sign is set via hdr["sign"]), safe for concurrent use.
func (k *kg5State) relayRaw(b *broker, label string, body []byte, hdr map[string]string) (kg5Resp, error) {
	if b == nil {
		b = k.b
	}
	k.heartbeat()
	if k.b2 != nil {
		k.b.mu.Lock()
		ids := make([]string, 0, len(k.b.nodes))
		for id := range k.b.nodes {
			ids = append(ids, id)
		}
		k.b.mu.Unlock()
		k.b2.mu.Lock() // concurrent relays share this fixture: the heartbeat map needs b2's lock
		for _, id := range ids {
			k.b2.lastSeen[id] = time.Now()
		}
		k.b2.mu.Unlock()
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if label != "" {
		secret, err := k.secretOf(label)
		if err != nil {
			return kg5Resp{}, err
		}
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	for h, v := range hdr {
		switch h {
		case "sign":
			a, err := k.acct(v)
			if err != nil {
				return kg5Resp{}, err
			}
			signReq(r, a.who.priv, body)
		case "remote":
			r.RemoteAddr = v
		default:
			r.Header.Set(h, v)
		}
	}
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	b.relay(w, r)
	res := kg5Resp{code: w.Code, hdr: w.Header(), body: w.Body.Bytes(), reqID: w.Header().Get("X-RogerAI-Request-Id"), snap: w.Result().Header}
	_ = json.Unmarshal(res.body, &res.js)
	return res, nil
}

// keyRelay fires one relay for the scenario model bearing label and records it.
func (k *kg5State) keyRelay(label string, stream bool, extra map[string]any, hdr map[string]string) error {
	return k.keyRelayOn(k.b, label, k.model, stream, extra, hdr)
}

func (k *kg5State) keyRelayOn(b *broker, label, model string, stream bool, extra map[string]any, hdr map[string]string) error {
	res, err := k.relayRaw(b, label, k.relayBody(model, stream, extra), hdr)
	if err != nil {
		return err
	}
	k.resp = res
	k.resps = append(k.resps, res)
	k.codes = append(k.codes, res.code)
	k.lastCode, k.lastBody, k.lastHdr = res.code, res.body, res.hdr
	k.reqID = res.reqID
	return nil
}

// pricedRelay: a priced relay for the scenario model bearing label.
func (k *kg5State) pricedRelay(label string) error {
	k.ensurePriced()
	return k.keyRelay(label, false, nil, nil)
}

// heldRelay starts a relay bearing label whose station answer is held until release(); the
// outcome lands on k.flight.
func (k *kg5State) heldRelay(label string, stream bool) error {
	if err := k.ensureKey("acct-a", label); err != nil {
		return err
	}
	st := k.ensureStation("kg5-slow", k.model, 1, 2)
	k.gate = make(chan struct{})
	gate := k.gate
	entered := make(chan struct{}, 1)
	k.scripted["kg5-slow"] = true
	hp, hc := k.heldP, k.heldC
	if hp == 0 && hc == 0 {
		hp, hc = 40, 20
	}
	st.set(func(_ int, w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		if bytes.Contains(buf.Bytes(), []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", sr3Marker+" held")
			fmt.Fprint(w, sr3Usage(hp, hc)+"\n\n")
			fmt.Fprint(w, sr3Done+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, sr3Marker+" held", hp, hc)
	})
	pin := map[string]string{"X-Roger-Node": st.id}
	body := k.relayBody(k.model, stream, nil)
	k.flight = make(chan kg5Resp, 1)
	flight := k.flight
	go func() {
		res, err := k.relayRaw(k.b, label, body, pin)
		if err != nil {
			res = kg5Resp{code: -1, body: []byte(err.Error())}
		}
		flight <- res
	}()
	select {
	case <-entered:
		return nil
	case res := <-flight:
		k.flight = nil
		return fmt.Errorf("the relay bearing %s never reached the station (it returned %d: %.300s)", label, res.code, res.body)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("the relay bearing %s never reached the station in 10s", label)
	}
}

func (k *kg5State) release() (kg5Resp, error) {
	if k.gate == nil || k.flight == nil {
		return kg5Resp{}, fmt.Errorf("no relay is in flight in this scenario")
	}
	close(k.gate)
	select {
	case res := <-k.flight:
		k.gate, k.flight = nil, nil
		k.resp = res
		k.reqID = res.reqID
		return res, nil
	case <-time.After(15 * time.Second):
		return kg5Resp{}, fmt.Errorf("the held relay did not finish within 15s of release")
	}
}

// settledOK: the released relay was served 200 and settled a spend row on wallet.
func (k *kg5State) settledOK(res kg5Resp, wallet string) error {
	if res.code != 200 {
		return fmt.Errorf("the in-flight relay ended %d, want 200: %.300s", res.code, res.body)
	}
	es, err := k.db.RecentByUser(wallet, 2000)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID != "" && strings.HasPrefix(e.RequestID, res.reqID) && res.reqID != "" {
			return nil
		}
	}
	return fmt.Errorf("the in-flight relay (request %q) left no settled row on %s", res.reqID, wallet)
}

// payerOf is the wallet the last relay settled against (from its stored entry).
func (k *kg5State) settledOn(wallet, reqID string) (bool, error) {
	es, err := k.db.RecentByUser(wallet, 2000)
	if err != nil {
		return false, err
	}
	for _, e := range es {
		if reqID != "" && strings.HasPrefix(e.RequestID, reqID) {
			return true, nil
		}
	}
	return false, nil
}

// storeDump is every byte of persisted state the scenario can reach.
func (k *kg5State) storeDump() (string, error) {
	var sb strings.Builder
	if k.pg != nil {
		rows, err := k.pg.DB().Query(`SELECT table_name, column_name FROM information_schema.columns
			WHERE table_schema='rogerai' AND data_type IN ('text','character varying','jsonb','json','bytea')`)
		if err != nil {
			return "", err
		}
		type tc struct{ t, c string }
		var cols []tc
		for rows.Next() {
			var x tc
			if err := rows.Scan(&x.t, &x.c); err != nil {
				rows.Close()
				return "", err
			}
			cols = append(cols, x)
		}
		rows.Close()
		for _, c := range cols {
			r2, err := k.pg.DB().Query(fmt.Sprintf(`SELECT %q::text FROM rogerai.%q`, c.c, c.t))
			if err != nil {
				continue
			}
			for r2.Next() {
				var v *string
				if r2.Scan(&v) == nil && v != nil {
					sb.WriteString(*v)
					sb.WriteByte('\n')
				}
			}
			r2.Close()
		}
	} else {
		fmt.Fprintf(&sb, "%#v", k.mem)
	}
	if k.mr != nil {
		for _, key := range k.mr.Keys() {
			sb.WriteString(key)
			sb.WriteByte('\n')
			if v, err := k.mr.Get(key); err == nil {
				sb.WriteString(v)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String(), nil
}

func kg5Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// keyEvents are the key_event audit rows on wallet.
func (k *kg5State) keyEvents(wallet string) ([]store.LedgerRow, error) {
	return k.db.LedgerOf(wallet, []string{"key_event"}, 2000)
}

// cmds counts shared-store commands one call makes.
func (k *kg5State) cmds(f func()) int {
	if k.mr == nil {
		f()
		return 0
	}
	before := k.mr.CommandCount()
	f()
	return k.mr.CommandCount() - before
}

// ---- Givens with a guardrail spec --------------------------------------------------------------

var (
	kg5ClLimit    = regexp.MustCompile(`^limit_usd ([0-9.]+)$`)
	kg5ClReset    = regexp.MustCompile(`^reset "([^"]*)"$`)
	kg5ClModels   = regexp.MustCompile(`^allowed_models (\[.*\])$`)
	kg5ClNodes    = regexp.MustCompile(`^allowed_nodes (\[.*\])$`)
	kg5ClExpires  = regexp.MustCompile(`^expires_at "?([0-9TZ:-]+)"?$`)
	kg5ClSpentWin = regexp.MustCompile(`^(?:it has spent )?\$([0-9.]+) spent(?: this (?:window|month|week))?$|^it has spent \$([0-9.]+) this window$`)
	kg5ClLifetime = regexp.MustCompile(`^\$([0-9.]+) lifetime spend across past days$`)
	kg5ClDisabled = regexp.MustCompile(`^disabled (true|false)$`)
)

// splitClauses splits "a, b and c" into clauses, keeping JSON arrays intact.
func kg5SplitClauses(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		case ' ':
			if depth == 0 && strings.HasPrefix(s[i:], " and ") {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + len(" and ")
				i = start - 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// nodeList translates scenario station names in a JSON list to the station ids they run as.
func (k *kg5State) nodeList(raw string) ([]string, error) {
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, fmt.Errorf("allowed_nodes %s is not a JSON list of strings", raw)
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if st, ok := k.stations[n]; ok {
			out = append(out, st.id)
		} else {
			out = append(out, n)
		}
	}
	return out, nil
}

func (k *kg5State) mintWith(acct, label, spec string) error {
	body := map[string]any{"name": label}
	var after []func() error
	for _, cl := range kg5SplitClauses(spec) {
		switch {
		case cl == "defaults" || cl == "":
		case kg5ClLimit.MatchString(cl):
			v, _ := strconv.ParseFloat(kg5ClLimit.FindStringSubmatch(cl)[1], 64)
			body["limit_usd"] = v
		case kg5ClReset.MatchString(cl):
			body["reset"] = kg5ClReset.FindStringSubmatch(cl)[1]
		case kg5ClModels.MatchString(cl):
			var v []string
			if err := json.Unmarshal([]byte(kg5ClModels.FindStringSubmatch(cl)[1]), &v); err != nil {
				return fmt.Errorf("allowed_models clause %q: %v", cl, err)
			}
			body["allowed_models"] = v
		case kg5ClNodes.MatchString(cl):
			v, err := k.nodeList(kg5ClNodes.FindStringSubmatch(cl)[1])
			if err != nil {
				return err
			}
			body["allowed_nodes"] = v
		case kg5ClExpires.MatchString(cl):
			body["expires_at"] = kg5ClExpires.FindStringSubmatch(cl)[1]
		case kg5ClDisabled.MatchString(cl):
			body["disabled"] = kg5ClDisabled.FindStringSubmatch(cl)[1] == "true"
		case kg5ClLifetime.MatchString(cl):
			amt, _ := strconv.ParseFloat(kg5ClLifetime.FindStringSubmatch(cl)[1], 64)
			after = append(after, func() error { return k.spendAcrossDays(label, amt) })
		case kg5ClSpentWin.MatchString(cl):
			m := kg5ClSpentWin.FindStringSubmatch(cl)
			v := m[1]
			if v == "" {
				v = m[2]
			}
			amt, _ := strconv.ParseFloat(v, 64)
			after = append(after, func() error { return k.spend(label, amt) })
		default:
			return fmt.Errorf("unrecognised key clause %q in %q", cl, spec)
		}
	}
	raw, _ := json.Marshal(body)
	if err := k.mint(acct, label, string(raw), nil); err != nil {
		return err
	}
	for _, f := range after {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

// spendAcrossDays settles amount in two halves on the two UTC days before today.
func (k *kg5State) spendAcrossDays(label string, amount float64) error {
	now := k.now()
	day := now.UTC().Truncate(24 * time.Hour)
	k.at(day.Add(-36 * time.Hour))
	if err := k.spend(label, amount/2); err != nil {
		return err
	}
	k.at(day.Add(-12 * time.Hour))
	if err := k.spend(label, amount/2); err != nil {
		return err
	}
	k.at(now)
	return nil
}

func (k *kg5State) keyWithSpec(label, spec string) error { return k.mintWith("acct-a", label, spec) }

// ---- guardrails: steps --------------------------------------------------------------------------

func (k *kg5State) postBody(acct, body string) error {
	body = strings.ReplaceAll(body, "<a string of 129 chars>", strings.Repeat("a", 129))
	// An Examples cell's "\n" reaches the step as a real newline; the scenario means a name
	// containing a line break, which JSON spells as the escape.
	body = strings.ReplaceAll(body, "\n", `\n`)
	k.markCount(acct)
	res, err := k.call(kg5Req{as: "acct:" + acct, method: http.MethodPost, path: "/account/keys", body: []byte(body)})
	if err != nil {
		return err
	}
	k.lastPosted = nil
	_ = json.Unmarshal([]byte(body), &k.lastPosted)
	if res.code == http.StatusCreated {
		if err := k.recordMint(acct, fmt.Sprintf("_minted%d", len(k.keys)), res); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) lastEntry() (map[string]any, error) {
	if k.lastLabel == "" {
		return nil, fmt.Errorf("no key was minted in this scenario (last response %d: %.300s)", k.resp.code, k.resp.body)
	}
	return k.entry(k.lastLabel)
}

func (k *kg5State) created201IDSecret() error {
	if err := k.status(201); err != nil {
		return err
	}
	id, _ := k.resp.js["id"].(string)
	sec, _ := k.resp.js["secret"].(string)
	if !strings.HasPrefix(id, "key_") || !strings.HasPrefix(sec, "rog-key_") {
		return fmt.Errorf("201 body lacks an id starting key_ and a secret starting rog-key_: %.300s", k.resp.body)
	}
	return nil
}

func (k *kg5State) storeHoldsHashOnly() error {
	kk, ok := k.keys[k.lastLabel]
	if !ok {
		return fmt.Errorf("no key was minted")
	}
	dump, err := k.storeDump()
	if err != nil {
		return err
	}
	if strings.Contains(dump, kk.secret) {
		return fmt.Errorf("the store holds the secret %s in the clear", kk.secret[:12]+"...")
	}
	if !strings.Contains(dump, kg5Hash(kk.secret)) {
		return fmt.Errorf("the store does not hold sha256(secret)")
	}
	return nil
}

func (k *kg5State) laterListShows(name string) error {
	es, err := k.entries("acct-a")
	if err != nil {
		return err
	}
	for _, e := range es {
		if e["name"] == name {
			if _, has := e["secret"]; has {
				return fmt.Errorf("the listed %q carries a secret field: %v", name, e)
			}
			return nil
		}
	}
	return fmt.Errorf("GET /account/keys does not list %q: %v", name, es)
}

func (k *kg5State) defaultsUnlimited() error {
	e, err := k.lastEntry()
	if err != nil {
		return err
	}
	if n, _ := kg5Num(e["limit_usd"]); n != 0 {
		return fmt.Errorf("limit_usd %v, want 0: %v", e["limit_usd"], e)
	}
	if e["reset"] != "none" {
		return fmt.Errorf("reset %v, want \"none\"", e["reset"])
	}
	if v, ok := e["expires_at"]; ok && v != nil && v != "" {
		return fmt.Errorf("expires_at %v, want none", v)
	}
	for _, f := range []string{"allowed_models", "allowed_nodes"} {
		l, ok := kg5StrList(e[f])
		if !ok || len(l) != 0 {
			return fmt.Errorf("%s %v, want []", f, e[f])
		}
	}
	if e["disabled"] != false {
		return fmt.Errorf("disabled %v, want false", e["disabled"])
	}
	return nil
}

func (k *kg5State) echoesEverything() error {
	if err := k.status(201); err != nil {
		return err
	}
	e, err := k.lastEntry()
	if err != nil {
		return err
	}
	for f, sent := range k.lastPosted {
		got, ok := e[f]
		if !ok {
			return fmt.Errorf("the entry has no %q (sent %v): %v", f, sent, e)
		}
		sj, _ := json.Marshal(sent)
		gj, _ := json.Marshal(got)
		if string(sj) != string(gj) {
			return fmt.Errorf("%s reads back %s, sent %s", f, gj, sj)
		}
	}
	return nil
}

func (k *kg5State) hintOnly(label, _ string) error {
	kk, ok := k.keys[label]
	if !ok {
		return fmt.Errorf("no key %q", label)
	}
	es, err := kg5Entries(k.resp.body)
	if err != nil {
		return fmt.Errorf("GET /account/keys = %d: %v", k.resp.code, err)
	}
	last4 := kk.secret[len(kk.secret)-4:]
	for _, e := range es {
		if e["id"] != kk.id {
			continue
		}
		if e["hint"] != "..."+last4 {
			return fmt.Errorf("hint %v, want \"...%s\" (the minted secret's last four characters)", e["hint"], last4)
		}
		raw, _ := json.Marshal(e)
		head := kk.secret[:len(kk.secret)-4]
		for i := 0; i+8 <= len(head); i++ {
			if bytes.Contains(raw, []byte(head[i:i+8])) {
				return fmt.Errorf("the entry shows more of the secret than its hint: %s", raw)
			}
		}
		return nil
	}
	return fmt.Errorf("%s is not listed", label)
}

func (k *kg5State) relayAuthWith(label string) error {
	k.ensurePriced()
	return k.keyRelay(label, false, nil, nil)
}

func (k *kg5State) payerIs(alias string) error {
	wallet, err := k.walletOf(alias)
	if err != nil {
		return err
	}
	if k.resp.code != 200 {
		return fmt.Errorf("the relay = %d, want 200 settled to %s: %.300s", k.resp.code, alias, k.resp.body)
	}
	ok, err := k.settledOn(wallet, k.resp.reqID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("request %q settled no row on %s (%s)", k.resp.reqID, alias, wallet)
	}
	return nil
}

func (k *kg5State) bearerPOSTsKeys(label string) error {
	k.markCount(k.ownerOfKey(label))
	return k.do("bearer:"+label, http.MethodPost, "/account/keys", []byte(`{"name":"x"}`))
}

func (k *kg5State) noKeyCreated() error {
	owner := "acct-a"
	n, err := k.count(owner)
	if err != nil {
		return err
	}
	if k.countMark < 0 {
		return fmt.Errorf("the key count before the attempt could not be read; now %d", n)
	}
	if n != k.countMark {
		return fmt.Errorf("%s holds %d keys after the refused mint, %d before", owner, n, k.countMark)
	}
	return nil
}

func (k *kg5State) bearerGETsKeys(label string) error {
	return k.do("bearer:"+label, http.MethodGet, "/account/keys", nil)
}

func (k *kg5State) bearerPATCHes(label, target, body string) error {
	k.snap(target)
	return k.do("bearer:"+label, http.MethodPatch, "/account/keys/"+k.keyID(target), []byte(body))
}

func (k *kg5State) bearerDELETEs(label, target string) error {
	k.snap(target)
	return k.do("bearer:"+label, http.MethodDelete, "/account/keys/"+k.keyID(target), nil)
}

func (k *kg5State) unchanged(label string) error {
	before, ok := k.snaps[label]
	if !ok {
		return fmt.Errorf("no snapshot of %s was taken (the read before the action failed)", label)
	}
	saved := k.resp
	now, err := k.entry(label)
	k.resp = saved
	if err != nil {
		return err
	}
	bj, _ := json.Marshal(before)
	nj, _ := json.Marshal(now)
	if string(bj) != string(nj) {
		return fmt.Errorf("%s changed: before %s, after %s", label, bj, nj)
	}
	return nil
}

func (k *kg5State) code403Unchanged(code, label string) error {
	if err := k.statusCode(403, code); err != nil {
		return err
	}
	return k.unchanged(label)
}

func (k *kg5State) code403StillExists(code, label string) error {
	if err := k.statusCode(403, code); err != nil {
		return err
	}
	saved := k.resp
	_, err := k.entry(label)
	k.resp = saved
	return err
}

func (k *kg5State) unboundPOSTs(body string) error {
	return k.do("unbound:", http.MethodPost, "/account/keys", []byte(body))
}

func (k *kg5State) askLogin() error {
	if err := k.status(401); err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(k.errMsg()), "log in") {
		return fmt.Errorf("the 401 does not ask to log in: %q", k.errMsg())
	}
	return nil
}

func (k *kg5State) unsignedPOST() error {
	return k.do("unsigned:acct-a", http.MethodPost, "/account/keys", []byte(`{"name":"x"}`))
}

func (k *kg5State) webCookie(acct string) error { _, err := k.acct(acct); return err }

func (k *kg5State) browserPOSTs(body string) error {
	return k.do("cookie:acct-a", http.MethodPost, "/account/keys", []byte(body))
}

func (k *kg5State) foreignOriginPOST() error {
	k.markCount("acct-a")
	return k.do("cookie-foreign:acct-a", http.MethodPost, "/account/keys", []byte(`{"name":"evil"}`))
}

func (k *kg5State) code401NoKey() error {
	if err := k.status(401); err != nil {
		return err
	}
	return k.noKeyCreated()
}

func (k *kg5State) ownerGrant(owner, label string) error {
	st := k.ensureStation(owner+"-node", k.model, 1, 2)
	_ = st
	return k.mintGrant(label, owner+"-node", true)
}

func (k *kg5State) postIdem(acct, idem, body string) error {
	_, err := k.call(kg5Req{as: "acct:" + acct, method: http.MethodPost, path: "/account/keys", body: []byte(body), hdr: map[string]string{"Idempotency-Key": idem}})
	if err != nil {
		return err
	}
	if k.resp.code != 201 {
		return fmt.Errorf("the first idempotent mint = %d, want 201: %.300s", k.resp.code, k.resp.body)
	}
	return k.recordMint(acct, "first", k.resp)
}

func (k *kg5State) replay() error {
	if k.lastReq == nil {
		return fmt.Errorf("no request to replay")
	}
	k.advance(5 * time.Minute)
	rq := *k.lastReq
	_, err := k.call(rq)
	return err
}

func (k *kg5State) alreadyMinted() error {
	if err := k.statusCode(409, "already_minted"); err != nil {
		return err
	}
	if !strings.Contains(string(k.resp.body), k.keys["first"].id) {
		return fmt.Errorf("the 409 does not name the first key %s: %.300s", k.keys["first"].id, k.resp.body)
	}
	return nil
}

func (k *kg5State) noSecondKey() error {
	if bytes.Contains(k.resp.body, []byte("rog-key_")) {
		return fmt.Errorf("the replay returned a secret: %.300s", k.resp.body)
	}
	n, err := k.count("acct-a")
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("acct-a holds %d keys after the replay, want 1", n)
	}
	return nil
}

func (k *kg5State) mintedIdem(acct, name, idem string) error {
	return k.postIdem(acct, idem, `{"name":"`+name+`"}`)
}

func (k *kg5State) sameBodyIdem(acct, idem string) error {
	rq := *k.lastReq
	rq.hdr = map[string]string{"Idempotency-Key": idem}
	_, err := k.call(rq)
	return err
}

func (k *kg5State) differentID() error {
	if err := k.status(201); err != nil {
		return err
	}
	if id, _ := k.resp.js["id"].(string); id == "" || id == k.keys["first"].id {
		return fmt.Errorf("the second mint's id %q is not a new id (first %s)", id, k.keys["first"].id)
	}
	return nil
}

func (k *kg5State) postTwice(acct, body string) error {
	for i := 0; i < 2; i++ {
		if err := k.mint(acct, fmt.Sprintf("twice%d", i), body, nil); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) twoNamed(name string) error {
	es, err := k.entries("acct-a")
	if err != nil {
		return err
	}
	n := 0
	for _, e := range es {
		if e["name"] == name {
			n++
		}
	}
	if n != 2 {
		return fmt.Errorf("%d keys named %q, want 2", n, name)
	}
	return nil
}

func (k *kg5State) holdsN(acct string, n int) error {
	for i := 0; i < n; i++ {
		if err := k.mint(acct, fmt.Sprintf("bulk%d", i), fmt.Sprintf(`{"name":"bulk%d"}`, i), nil); err != nil {
			return err
		}
		// A Given's N keys are state, not a burst: the management limiter is the subject of
		// its own scenario, so the setup mints are not charged against it.
		lim := k.b.keyMgmtLimiter()
		lim.mu.Lock()
		lim.buckets = map[string]*tokenBucket{}
		lim.mu.Unlock()
		if !k.mrClosed { // and the shared bucket every instance draws from
			for _, key := range k.mr.Keys() {
				if strings.HasPrefix(key, keyPrefix+"rl:acctkeys:") {
					k.mr.Del(key)
				}
			}
		}
	}
	return nil
}

func (k *kg5State) code400Msg(code, msg string) error {
	if err := k.statusCode(400, code); err != nil {
		return err
	}
	if k.errMsg() != msg {
		return fmt.Errorf("message %q, want %q", k.errMsg(), msg)
	}
	return nil
}

func (k *kg5State) holdsNDeletesOne(acct string, n int) error {
	if err := k.holdsN(acct, n); err != nil {
		return err
	}
	if err := k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID("bulk0"), nil); err != nil {
		return err
	}
	return k.status(204)
}

func (k *kg5State) code400Names(code, field string) error {
	if err := k.statusCode(400, code); err != nil {
		return err
	}
	return k.names(field)
}

func (k *kg5State) code400NamesNoKey(code, field string) error {
	if err := k.code400Names(code, field); err != nil {
		return err
	}
	return k.noKeyCreated()
}

func (k *kg5State) unlimited201() error {
	if err := k.status(201); err != nil {
		return err
	}
	return k.wantNum(k.lastLabel, "limit_usd", 0)
}

func (k *kg5State) limitReadsBack201(v float64) error {
	if err := k.status(201); err != nil {
		return err
	}
	return k.wantNum(k.lastLabel, "limit_usd", v)
}

func (k *kg5State) limitReadsBack(v float64) error {
	if _, err := k.lastEntry(); err != nil {
		return err
	}
	return k.wantNum(k.lastLabel, "limit_usd", v)
}

func (k *kg5State) unlimitedWithReset(reset string) error {
	if err := k.unlimited201(); err != nil {
		return err
	}
	e, err := k.lastEntry()
	if err != nil {
		return err
	}
	if e["reset"] != reset {
		return fmt.Errorf("reset %v, want %q", e["reset"], reset)
	}
	return nil
}

func kg5IDs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%02d", prefix, i)
	}
	return out
}

func (k *kg5State) post64(acct string) error {
	b, _ := json.Marshal(map[string]any{"allowed_models": kg5IDs("model", 64), "allowed_nodes": kg5IDs("node", 64)})
	return k.postBody(acct, string(b))
}

func (k *kg5State) post65(acct, list string) error {
	b, _ := json.Marshal(map[string]any{list: kg5IDs("id", 65)})
	return k.postBody(acct, string(b))
}

func (k *kg5State) modelsReadBack201(raw string) error {
	if err := k.status(201); err != nil {
		return err
	}
	e, err := k.lastEntry()
	if err != nil {
		return err
	}
	gj, _ := json.Marshal(e["allowed_models"])
	var want []string
	_ = json.Unmarshal([]byte(raw), &want)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		return fmt.Errorf("allowed_models reads back %s, want %s", gj, wj)
	}
	return nil
}

func (k *kg5State) relayAsksFor(label, model string) error {
	// A station is stood up for the model only when the scenario put none on air (a scenario
	// that names its station, like the $0 "nf", must be served by it, not by a priced extra).
	onAir := false
	for _, st := range k.stations {
		onAir = onAir || st.model == model
	}
	if !onAir {
		k.ensureStation("kg5-"+model, model, 1, 2)
	}
	return k.keyRelayOn(k.b, label, model, false, nil, nil)
}

func (k *kg5State) code403Paren(code string) error { return k.statusCode(403, code) }

func (k *kg5State) asksAnyModel(label string) error {
	k.ensurePriced()
	return k.keyRelay(label, false, nil, nil)
}

func (k *kg5State) noDenialPossible() error {
	if c := k.errCode(); c == "key_model_denied" || c == "key_node_denied" {
		return fmt.Errorf("an empty allow-list refused with %s", c)
	}
	return k.status(200)
}

func (k *kg5State) expiresReadsBack(ts string) error {
	if err := k.status(201); err != nil {
		return err
	}
	e, err := k.lastEntry()
	if err != nil {
		return err
	}
	if e["expires_at"] != ts {
		return fmt.Errorf("expires_at reads back %v, want %q", e["expires_at"], ts)
	}
	return nil
}

func (k *kg5State) clockReads(ts string) error {
	t, err := kg5Time(ts)
	if err != nil {
		return err
	}
	k.at(t)
	return nil
}

func (k *kg5State) code400NamesField(field string) error {
	if err := k.status(400); err != nil {
		return err
	}
	return k.names(field)
}

func (k *kg5State) post5KiB(acct string) error {
	body := `{"name":"` + strings.Repeat("a", 5*1024) + `"}`
	return k.postBody(acct, body)
}

// patching

func (k *kg5State) acctPATCHes(acct, label, body string) error {
	path := "/account/keys/" + k.keyID(label)
	if label == "k1" || label == "kb" {
		if _, ok := k.keys[label]; !ok && label == "k1" {
			if err := k.ensureKey(acct, label); err != nil {
				return err
			}
			path = "/account/keys/" + k.keyID(label)
		}
	}
	k.snap(label)
	return k.do("acct:"+acct, http.MethodPatch, path, []byte(body))
}

func (k *kg5State) acctPATCHesQuoted(acct, label, body string) error {
	return k.acctPATCHes(acct, label, body)
}

func (k *kg5State) hasNameLimitResetModels(label, name string, limit float64, reset, models string) error {
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	if e["name"] != name {
		return fmt.Errorf("name %v, want %q", e["name"], name)
	}
	if n, _ := kg5Num(e["limit_usd"]); !kg5Near(n, limit) {
		return fmt.Errorf("limit_usd %v, want %v", e["limit_usd"], limit)
	}
	if e["reset"] != reset {
		return fmt.Errorf("reset %v, want %q", e["reset"], reset)
	}
	gj, _ := json.Marshal(e["allowed_models"])
	var want []string
	_ = json.Unmarshal([]byte(models), &want)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		return fmt.Errorf("allowed_models %s, want %s", gj, wj)
	}
	return nil
}

var kg5Defaults = map[string]any{"name": "k1", "limit_usd": 0.0, "reset": "none", "expires_at": nil, "allowed_models": []any{}, "allowed_nodes": []any{}, "disabled": false}

func (k *kg5State) patchReadsBack(label, patch string) error {
	if err := k.status(200); err != nil {
		return err
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(patch), &p); err != nil {
		return err
	}
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	for f, def := range kg5Defaults {
		want := def
		if v, ok := p[f]; ok {
			want = v
		}
		got := e[f]
		if f == "expires_at" && (got == "" || got == nil) && want == nil {
			continue
		}
		wj, _ := json.Marshal(want)
		gj, _ := json.Marshal(got)
		if f == "limit_usd" {
			a, _ := kg5Num(got)
			b, _ := kg5Num(want)
			if kg5Near(a, b) {
				continue
			}
		}
		if string(wj) != string(gj) {
			return fmt.Errorf("%s %s = %s, want %s", label, f, gj, wj)
		}
	}
	return nil
}

func (k *kg5State) allowsAllModels(label string) error {
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	l, ok := kg5StrList(e["allowed_models"])
	if !ok || len(l) != 0 {
		return fmt.Errorf("allowed_models %v, want [] (all)", e["allowed_models"])
	}
	return nil
}

func (k *kg5State) stillEmptyModels() error { return k.allowsAllModels("k1") }

func (k *kg5State) neverExpires(label string) error {
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	if v := e["expires_at"]; v != nil && v != "" {
		return fmt.Errorf("expires_at %v, want none", v)
	}
	return nil
}

func (k *kg5State) nextPricedIs402(label, source string) error {
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.is402Source(source)
}

func (k *kg5State) is402Source(source string) error {
	if err := k.status(402); err != nil {
		return err
	}
	md, _ := k.resp.js["error"].(map[string]any)
	meta, _ := md["metadata"].(map[string]any)
	if meta["limit_source"] != source {
		return fmt.Errorf("limit_source %v, want %q: %.400s", meta["limit_source"], source, k.resp.body)
	}
	return nil
}

func (k *kg5State) notReversed(amount float64) error {
	wallet, err := k.walletOf("acct-a")
	if err != nil {
		return err
	}
	rows, err := k.db.LedgerOf(wallet, []string{store.KindSpend, store.KindRefund, store.KindChargeback}, 5000)
	if err != nil {
		return err
	}
	spent := 0.0
	for _, r := range rows {
		switch r.Kind {
		case store.KindSpend:
			spent += -r.Amount
		case store.KindRefund, store.KindChargeback:
			return fmt.Errorf("a %s row appeared after the PATCH: %+v", r.Kind, r)
		}
	}
	if spent+1e-9 < amount {
		return fmt.Errorf("settled spend on acct-a is $%.6f, want at least $%.2f still there", spent, amount)
	}
	return nil
}

func (k *kg5State) streamUnderLimit(label string, limit float64) error {
	if err := k.mintWith("acct-a", label, fmt.Sprintf("limit_usd %v", limit)); err != nil {
		return err
	}
	return k.heldRelay(label, true)
}

func (k *kg5State) patchLimit(acct, label string, v float64) error {
	return k.acctPATCHes(acct, label, fmt.Sprintf(`{"limit_usd":%v}`, v))
}

func (k *kg5State) streamCompletesOriginalHold() error {
	res, err := k.release()
	if err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	return k.settledOK(res, wallet)
}

func (k *kg5State) nextRequestIs(label string, code int) error {
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.status(code)
}

func (k *kg5State) code403LimitStill(code string, v float64) error {
	if err := k.statusCode(403, code); err != nil {
		return err
	}
	saved := k.resp
	defer func() { k.resp = saved }()
	return k.wantNum("k1", "limit_usd", v)
}

func (k *kg5State) relayIs402Source(label, source string) error {
	return k.nextPricedIs402(label, source)
}

func (k *kg5State) nextServed(label string) error {
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) patchReset(acct, label, reset string) error {
	// The fixture clock is frozen, so the PATCH is moved one second past the steps before it
	// (a Given's spend happened BEFORE the change; the window opens AT the change).
	k.advance(time.Second)
	return k.acctPATCHes(acct, label, `{"reset":"`+reset+`"}`)
}

func (k *kg5State) windowSincePatch(lifetime float64) error {
	e, err := k.entry("k1")
	if err != nil {
		return err
	}
	lim, _ := kg5Num(e["limit_usd"])
	rem, err := k.fieldNum(e, "limit_remaining")
	if err != nil {
		return err
	}
	if !kg5Near(rem, lim) {
		return fmt.Errorf("limit_remaining %v after re-anchoring, want the full %v (window usage $0.00)", rem, lim)
	}
	u, err := k.fieldNum(e, "usage")
	if err != nil {
		return err
	}
	if !kg5Near(u, lifetime) {
		return fmt.Errorf("lifetime usage %v, want %v", u, lifetime)
	}
	return nil
}

func (k *kg5State) remainingLifetime(v float64) error { return k.wantNum("k1", "limit_remaining", v) }

func (k *kg5State) uniform404() error {
	if err := k.status(404); err != nil {
		return err
	}
	if !strings.Contains(string(k.resp.body), "no such key") {
		return fmt.Errorf("the 404 body is not the uniform \"no such key\": %.300s", k.resp.body)
	}
	return nil
}

func (k *kg5State) uniform404Identical() error {
	if err := k.uniform404(); err != nil {
		return err
	}
	foreign := k.resp
	rq := *k.lastReq
	rq.path = "/account/keys/key_doesnotexist"
	unknown, err := k.call(rq)
	k.resp = foreign
	if err != nil {
		return err
	}
	if unknown.code != foreign.code || !bytes.Equal(unknown.body, foreign.body) {
		return fmt.Errorf("foreign-id answer %d %q differs from unknown-id answer %d %q", foreign.code, foreign.body, unknown.code, unknown.body)
	}
	return nil
}

func (k *kg5State) code400(code string) error { return k.statusCode(400, code) }

func (k *kg5State) disableThenEnable(acct, label string) error {
	if err := k.acctPATCHes(acct, label, `{"disabled":true}`); err != nil {
		return err
	}
	if err := k.status(200); err != nil {
		return err
	}
	if err := k.acctPATCHes(acct, label, `{"disabled":false}`); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) remaining(v float64) error { return k.wantNum("k1", "limit_remaining", v) }

// listing

func (k *kg5State) mintedTwoForList(acct, a, b string) error {
	if err := k.mintWith(acct, a, `limit_usd 5, reset "weekly"`); err != nil {
		return err
	}
	if err := k.spend(a, 1.25); err != nil {
		return err
	}
	return k.mint(acct, b, `{"name":"`+b+`"}`, nil)
}

func (k *kg5State) eachEntryHas(fields string) error {
	es, err := kg5Entries(k.resp.body)
	if err != nil {
		return err
	}
	if len(es) == 0 {
		return fmt.Errorf("the list is empty")
	}
	var want []string
	for _, f := range strings.Split(fields, ",") {
		want = append(want, strings.TrimSpace(f))
	}
	for _, e := range es {
		for _, f := range want {
			if _, ok := e[f]; !ok {
				return fmt.Errorf("entry %v has no %q", e["id"], f)
			}
		}
	}
	return nil
}

func (k *kg5State) remainingAndNull(a string, v float64, b string) error {
	ea, err := k.entry(a)
	if err != nil {
		return err
	}
	if r, err := k.fieldNum(ea, "limit_remaining"); err != nil || !kg5Near(r, v) {
		return fmt.Errorf("%s limit_remaining %v, want %v (%v)", a, ea["limit_remaining"], v, err)
	}
	eb, err := k.entry(b)
	if err != nil {
		return err
	}
	if v, ok := eb["limit_remaining"]; !ok || v != nil {
		return fmt.Errorf("%s limit_remaining %v (present %v), want null", b, v, ok)
	}
	return nil
}

func (k *kg5State) noSecretFields() error {
	es, err := k.entries("acct-a")
	if err != nil {
		return err
	}
	for _, e := range es {
		for _, f := range []string{"secret", "secret_hash"} {
			if _, ok := e[f]; ok {
				return fmt.Errorf("entry %v carries %q", e["id"], f)
			}
		}
	}
	return nil
}

func (k *kg5State) settledAcrossWindows(label string) error {
	today := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) // a Wednesday
	if err := k.mint("acct-a", label, `{"name":"`+label+`"}`, nil); err != nil {
		return err
	}
	for _, p := range []struct {
		at  time.Time
		amt float64
	}{
		{time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), 8},  // last month
		{time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), 4}, // last week (and last month)
		{today.Add(-24 * time.Hour), 1},                    // yesterday, same week and month
		{today, 2},
	} {
		k.at(p.at)
		if err := k.spend(label, p.amt); err != nil {
			return err
		}
	}
	k.at(today)
	return nil
}

func (k *kg5State) getsListToday(acct string) error {
	_, err := k.entries(acct)
	return err
}

func (k *kg5State) usageWindows(label string, d, w, m, u float64) error {
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	for f, want := range map[string]float64{"usage_daily": d, "usage_weekly": w, "usage_monthly": m, "usage": u} {
		got, err := k.fieldNum(e, f)
		if err != nil {
			return err
		}
		if !kg5Near(got, want) {
			return fmt.Errorf("%s %s = %v, want %v", label, f, got, want)
		}
	}
	return nil
}

func (k *kg5State) settledThenChargeback(label string) error {
	if err := k.mint("acct-a", label, `{"name":"`+label+`"}`, nil); err != nil {
		return err
	}
	if err := k.spend(label, 2); err != nil {
		return err
	}
	if err := k.spend(label, 1); err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	// The $1.00 spend relay is the disputed request (the spend Given records no scenario
	// response, so k.resp is not it).
	_, err := k.db.Chargeback("dp_"+k.nonce, wallet, k.spendReq, 1, time.Now())
	return err
}

func (k *kg5State) usageIs(label string, v float64) error { return k.wantNum(label, "usage", v) }

func (k *kg5State) lastRequest402At(label string) error {
	if err := k.mintWith("acct-a", label, "limit_usd 0.01"); err != nil {
		return err
	}
	if err := k.spend(label, 0.01); err != nil {
		return err
	}
	k.at0 = k.now()
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.status(402)
}

func (k *kg5State) lastUsedT(label string) error {
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	switch v := e["last_used"].(type) {
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return fmt.Errorf("last_used %q is not RFC 3339", v)
		}
		if t.Unix() != k.at0.Unix() {
			return fmt.Errorf("last_used %v, want %v", t, k.at0)
		}
	case float64:
		if int64(v) != k.at0.Unix() {
			return fmt.Errorf("last_used %v, want %v", v, k.at0.Unix())
		}
	default:
		return fmt.Errorf("last_used %v (%T), want the time of the refused request", e["last_used"], e["last_used"])
	}
	return nil
}

func (k *kg5State) twoAcctsMinted(a, ka, b, kb string) error {
	if err := k.mint(a, ka, `{"name":"`+ka+`"}`, nil); err != nil {
		return err
	}
	return k.mint(b, kb, `{"name":"`+kb+`"}`, nil)
}

func (k *kg5State) onlyListed(label string) error {
	es, err := kg5Entries(k.resp.body)
	if err != nil {
		return err
	}
	if len(es) != 1 || es[0]["id"] != k.keyID(label) {
		return fmt.Errorf("the list is %v, want only %s", es, k.keyID(label))
	}
	return nil
}

func (k *kg5State) acctGETsByID(acct, label string) error {
	if label == "k1" {
		if err := k.ensureKey(acct, label); err != nil {
			return err
		}
	}
	return k.do("acct:"+acct, http.MethodGet, "/account/keys/"+k.keyID(label), nil)
}

func (k *kg5State) sameAsList() error {
	if err := k.status(200); err != nil {
		return err
	}
	single := k.resp.js
	es, err := k.entries("acct-a")
	if err != nil {
		return err
	}
	if e, ok := single["key"].(map[string]any); ok {
		single = e
	}
	sj, _ := json.Marshal(single)
	for _, e := range es {
		if e["id"] == single["id"] {
			ej, _ := json.Marshal(e)
			if string(ej) != string(sj) {
				return fmt.Errorf("by-id %s differs from list entry %s", sj, ej)
			}
			return nil
		}
	}
	return fmt.Errorf("the by-id entry %v is not in the list", single["id"])
}

func (k *kg5State) mintedSimple(acct, label string) error {
	return k.mint(acct, label, `{"name":"`+label+`"}`, nil)
}

func (k *kg5State) identical404ToUnknown() error {
	if err := k.status(404); err != nil {
		return err
	}
	foreign := k.resp
	unknown, err := k.call(kg5Req{as: "acct:acct-a", method: http.MethodGet, path: "/account/keys/key_doesnotexist"})
	k.resp = foreign
	if err != nil {
		return err
	}
	if !bytes.Equal(foreign.body, unknown.body) || foreign.code != unknown.code {
		return fmt.Errorf("foreign %d %q != unknown %d %q", foreign.code, foreign.body, unknown.code, unknown.body)
	}
	return nil
}

func (k *kg5State) guess1000(acct string) error {
	// The scenario compares ids that exist under another account against ids that do not;
	// with no Given minting one, acct-b mints "kb" here.
	if err := k.ensureKey("acct-b", "kb"); err != nil {
		return err
	}
	k.resps = nil
	ids := []string{}
	if kb, ok := k.keys["kb"]; ok {
		ids = append(ids, kb.id)
	}
	for i := len(ids); i < 1000; i++ {
		ids = append(ids, fmt.Sprintf("key_%016x", i*7919))
	}
	for _, id := range ids {
		if _, err := k.call(kg5Req{as: "acct:" + acct, method: http.MethodGet, path: "/account/keys/" + id}); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) everyMissSame() error {
	if len(k.resps) == 0 {
		return fmt.Errorf("no guesses were made")
	}
	first := k.resps[0]
	for i, r := range k.resps {
		if r.code != 404 {
			return fmt.Errorf("guess %d = %d, want 404: %.200s", i, r.code, r.body)
		}
		if !bytes.Equal(r.body, first.body) {
			return fmt.Errorf("guess %d's 404 body %q differs from guess 0's %q", i, r.body, first.body)
		}
	}
	return nil
}

func (k *kg5State) sameLookupPath() error {
	kb, ok := k.keys["kb"]
	if !ok {
		return fmt.Errorf("no foreign key kb to compare against")
	}
	foreign := k.cmds(func() {
		_, _ = k.call(kg5Req{as: "acct:acct-a", method: http.MethodGet, path: "/account/keys/" + kb.id})
	})
	unknown := k.cmds(func() {
		_, _ = k.call(kg5Req{as: "acct:acct-a", method: http.MethodGet, path: "/account/keys/key_0000000000000000"})
	})
	if foreign != unknown || foreign == 0 {
		return fmt.Errorf("a foreign id ran %d shared-store commands and an unknown id %d; the same lookup-then-compare path runs both (and at least one lookup)", foreign, unknown)
	}
	return nil
}

func (k *kg5State) mintedAndDeleted(acct, label string) error {
	if err := k.mintedSimple(acct, label); err != nil {
		return err
	}
	if err := k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID(label), nil); err != nil {
		return err
	}
	return k.status(204)
}

func (k *kg5State) absent(label string) error {
	es, err := kg5Entries(k.resp.body)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e["id"] == k.keyID(label) {
			return fmt.Errorf("%s is still listed", label)
		}
	}
	return nil
}

func (k *kg5State) mintedN(acct string, n int) error { return k.holdsN(acct, n) }

func (k *kg5State) newestFirst(n int) error {
	es, err := kg5Entries(k.resp.body)
	if err != nil {
		return err
	}
	if len(es) != n {
		return fmt.Errorf("one page holds %d keys, want %d", len(es), n)
	}
	for i := 0; i < n; i++ {
		if want := k.keyID(fmt.Sprintf("bulk%d", n-1-i)); es[i]["id"] != want {
			return fmt.Errorf("position %d is %v, want %s (newest first)", i, es[i]["id"], want)
		}
	}
	return nil
}

// deleting

func (k *kg5State) acctDELETEs(acct, label string) error {
	if label == "k1" {
		if err := k.ensureKey(acct, label); err != nil {
			return err
		}
	}
	return k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID(label), nil)
}

func (k *kg5State) nextBearer401(label, code string) error {
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.statusCode(401, code)
}

func (k *kg5State) twoInstancesShare() error {
	k.instanceB()
	return nil
}

func (k *kg5State) deleteOnA(acct, label string) error {
	if err := k.ensureKey(acct, label); err != nil {
		return err
	}
	if k.b2 == nil {
		k.instanceB()
	}
	if err := k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID(label), nil); err != nil {
		return err
	}
	return k.status(204)
}

func (k *kg5State) bearerOnB(label string) error {
	if k.b2 == nil {
		return fmt.Errorf("no instance B in this scenario")
	}
	k.ensurePriced()
	for id, n := range k.b.nodes {
		k.b2.nodes[id] = n
		k.b2.tunnels[id] = k.b.tunnels[id]
		k.b2.trust[id] = k.b.trust[id]
	}
	return k.keyRelayOn(k.b2, label, k.model, false, nil, nil)
}

func (k *kg5State) code401Paren(code string) error { return k.statusCode(401, code) }

func (k *kg5State) streamMidFlight(label string) error { return k.heldRelay(label, true) }

func (k *kg5State) streamSettlesAgainst(alias string) error {
	res, err := k.release()
	if err != nil {
		return err
	}
	wallet, err := k.walletOf(alias)
	if err != nil {
		return err
	}
	if err := k.settledOK(res, wallet); err != nil {
		return err
	}
	if _, _, err := k.storedReceipt(res.reqID + "-1"); err != nil {
		if _, _, err2 := k.storedReceipt(res.reqID); err2 != nil {
			return fmt.Errorf("the stream wrote no receipt: %v", err)
		}
	}
	return nil
}

func (k *kg5State) newRequest401(label, code string) error { return k.nextBearer401(label, code) }

func (k *kg5State) nonStreamDispatched(label string) error { return k.heldRelay(label, false) }

func (k *kg5State) deleteBeforeReturn(acct, label string) error {
	if err := k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID(label), nil); err != nil {
		return err
	}
	return k.status(204)
}

func (k *kg5State) settlesNormally200() error {
	res, err := k.release()
	if err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	return k.settledOK(res, wallet)
}

func (k *kg5State) deleteTwice(acct, label string) error {
	if err := k.ensureKey(acct, label); err != nil {
		return err
	}
	k.resps = nil
	for i := 0; i < 2; i++ {
		if err := k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID(label), nil); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) firstThenUniform() error {
	if len(k.resps) < 2 {
		return fmt.Errorf("%d deletes recorded, want 2", len(k.resps))
	}
	if k.resps[0].code != 204 {
		return fmt.Errorf("first delete = %d, want 204", k.resps[0].code)
	}
	second := k.resps[1]
	unknown, err := k.call(kg5Req{as: "acct:acct-a", method: http.MethodDelete, path: "/account/keys/key_doesnotexist"})
	if err != nil {
		return err
	}
	if second.code != 404 || !bytes.Equal(second.body, unknown.body) {
		return fmt.Errorf("second delete %d %q, want the never-minted id's %d %q", second.code, second.body, unknown.code, unknown.body)
	}
	return nil
}

func (k *kg5State) code404StillAuth(label string) error {
	if err := k.status(404); err != nil {
		return err
	}
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	if k.resp.code == 401 || k.resp.code == 503 {
		return fmt.Errorf("%s no longer authenticates: %d %.300s", label, k.resp.code, k.resp.body)
	}
	return nil
}

func (k *kg5State) settledThenDeleted(label string, amt float64) error {
	if err := k.mintedSimple("acct-a", label); err != nil {
		return err
	}
	if err := k.spend(label, amt); err != nil {
		return err
	}
	if err := k.do("acct:acct-a", http.MethodDelete, "/account/keys/"+k.keyID(label), nil); err != nil {
		return err
	}
	return k.status(204)
}

func (k *kg5State) getsUsage(acct string) error {
	return k.do("acct:"+acct, http.MethodGet, "/usage", nil)
}

func (k *kg5State) attributedToKey(amt float64, label string) error {
	if err := k.status(200); err != nil {
		return err
	}
	id := k.keyID(label)
	var rows []map[string]any
	var obj map[string]any
	_ = json.Unmarshal(k.resp.body, &obj)
	for _, f := range []string{"recent", "rows", "requests"} {
		if raw, ok := obj[f].([]any); ok {
			for _, x := range raw {
				if m, ok := x.(map[string]any); ok {
					rows = append(rows, m)
				}
			}
		}
	}
	sum := 0.0
	for _, r := range rows {
		if r["key_id"] == id {
			c, _ := kg5Num(r["cost"])
			sum += c
		}
	}
	if !kg5Near(sum, amt) {
		return fmt.Errorf("/usage attributes $%.6f to key %s, want $%.2f: %.400s", sum, id, amt, k.resp.body)
	}
	return nil
}

func (k *kg5State) balanceAndCap(acct string, bal, cap float64) error {
	wallet, err := k.walletOf(acct)
	if err != nil {
		return err
	}
	cur, err := k.db.PeekBalance(wallet)
	if err != nil {
		return err
	}
	if d := bal - cur; math.Abs(d) > 1e-9 {
		if _, err := k.db.AddCredits(wallet, d); err != nil {
			return err
		}
	}
	return k.db.SetMonthlyCap(wallet, cap)
}

func (k *kg5State) balanceCapAre(bal, cap float64) error {
	wallet, _ := k.walletOf("acct-a")
	got, err := k.db.PeekBalance(wallet)
	if err != nil {
		return err
	}
	if !kg5Near(got, bal) {
		return fmt.Errorf("balance %v, want %v", got, bal)
	}
	c, err := k.db.MonthlyCapOf(wallet)
	if err != nil {
		return err
	}
	if !kg5Near(c, cap) {
		return fmt.Errorf("monthly cap %v, want %v", c, cap)
	}
	return nil
}

// reset windows

func (k *kg5State) spentAt(label string, amt float64, ts string) error {
	t, err := kg5Time(ts)
	if err != nil {
		return err
	}
	k.at(t)
	return k.spend(label, amt)
}

func (k *kg5State) pricedAt(label, ts string) error {
	t, err := kg5Time(ts)
	if err != nil {
		return err
	}
	k.at(t)
	return k.pricedRelay(label)
}

func (k *kg5State) itIs402Source(source string) error { return k.is402Source(source) }

func (k *kg5State) servedWindowZero() error { return k.status(200) }

func (k *kg5State) weeklySunday(label string) error {
	if err := k.mintWith("acct-a", label, `limit_usd 5, reset "weekly"`); err != nil {
		return err
	}
	return k.spentAt(label, 5, "2026-10-04T23:00:00Z")
}

func (k *kg5State) pricedArrivesTS(ts string) error {
	return k.pricedAt(k.anyKey(), ts)
}

// anyKey is the scenario's key ("k1" when present).
func (k *kg5State) anyKey() string {
	if _, ok := k.keys["k1"]; ok {
		return "k1"
	}
	for l := range k.keys {
		return l
	}
	return "k1"
}

func (k *kg5State) windowZero() error {
	if err := k.status(200); err != nil {
		return err
	}
	e, err := k.entry(k.anyKey())
	if err != nil {
		return err
	}
	lim, _ := kg5Num(e["limit_usd"])
	rem, err := k.fieldNum(e, "limit_remaining")
	if err != nil {
		return err
	}
	// k.resp is still the relay (an entry read never replaces the scenario response).
	if c, _ := strconv.ParseFloat(k.resp.hdr.Get("X-RogerAI-Cost"), 64); !kg5Near(rem, lim-c) {
		return fmt.Errorf("limit_remaining %v; the window opened at $0.00 so it should be %v less this request's $%v", rem, lim, c)
	}
	return nil
}

func (k *kg5State) monthlyLastSecond(label string) error {
	if err := k.mintWith("acct-a", label, `limit_usd 5, reset "monthly"`); err != nil {
		return err
	}
	return k.spentAt(label, 5, "2026-10-31T23:59:59Z")
}

func (k *kg5State) novemberHolds() error {
	cost, _ := strconv.ParseFloat(k.resp.hdr.Get("X-RogerAI-Cost"), 64)
	k.at(time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC))
	if err := k.wantNum(k.anyKey(), "limit_remaining", 5-cost); err != nil {
		return fmt.Errorf("on Nov 30 the window should still hold November's $%v: %w", cost, err)
	}
	return nil
}

func (k *kg5State) decemberZero() error {
	k.at(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	return k.wantNum(k.anyKey(), "limit_remaining", 5)
}

func (k *kg5State) keyMonthly(label string) error {
	return k.mintWith("acct-a", label, `limit_usd 5, reset "monthly"`)
}

func (k *kg5State) crossLeap() error {
	if err := k.spentAt(k.anyKey(), 5, "2028-02-29T12:00:00Z"); err != nil {
		return err
	}
	k.codes = nil
	if err := k.pricedAt(k.anyKey(), "2028-02-29T23:59:59Z"); err != nil {
		return err
	}
	return k.pricedAt(k.anyKey(), "2028-03-01T00:00:00Z")
}

func (k *kg5State) resetsOnceAtCrossing() error {
	if len(k.codes) != 2 || k.codes[0] != 402 || k.codes[1] != 200 {
		return fmt.Errorf("outcomes across the crossing %v, want [402 200]", k.codes)
	}
	return nil
}

func (k *kg5State) dstKey(label string) error {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	k.savedTZ = time.Local
	time.Local = ny
	return k.mintWith("acct-a", label, `limit_usd 5, reset "daily"`)
}

func (k *kg5State) dstShift() error {
	// 2026-11-01 06:00Z is 02:00 EDT -> 01:00 EST: the local clock repeats an hour.
	if err := k.spentAt(k.anyKey(), 5, "2026-11-01T05:30:00Z"); err != nil {
		return err
	}
	k.codes = nil
	if err := k.pricedAt(k.anyKey(), "2026-11-01T23:59:59Z"); err != nil {
		return err
	}
	return k.pricedAt(k.anyKey(), "2026-11-02T00:00:00Z")
}

func (k *kg5State) boundaryStillMidnight() error { return k.resetsOnceAtCrossing() }

func (k *kg5State) noneSpent2026(label string) error {
	if err := k.mintWith("acct-a", label, `limit_usd 5, reset "none"`); err != nil {
		return err
	}
	return k.spentAt(label, 5, "2026-10-01T12:00:00Z")
}

func (k *kg5State) pricedIn2027() error { return k.pricedAt(k.anyKey(), "2027-03-01T12:00:00Z") }

func (k *kg5State) twoInstancesSettled(label string) error {
	if err := k.mintWith("acct-a", label, `limit_usd 10, reset "monthly"`); err != nil {
		return err
	}
	k.instanceB()
	if err := k.spendOn(k.b, label, 2); err != nil {
		return err
	}
	return k.spendOn(k.b2, label, 2)
}

func (k *kg5State) eitherComputes() error { return nil } // the Then reads both instances

func (k *kg5State) readsSum(v float64) error {
	for _, b := range []*broker{k.b, k.b2} {
		if b == nil {
			return fmt.Errorf("no second instance")
		}
		e, err := k.entryOn(b, k.anyKey())
		if err != nil {
			return err
		}
		lim, _ := kg5Num(e["limit_usd"])
		rem, err := k.fieldNum(e, "limit_remaining")
		if err != nil {
			return err
		}
		if !kg5Near(lim-rem, v) {
			return fmt.Errorf("an instance reads window usage $%v, want $%v", lim-rem, v)
		}
	}
	return nil
}

// expiry and disabled

func (k *kg5State) keyExpiresAt(label, ts string) error {
	t, err := kg5Time(ts)
	if err != nil {
		return err
	}
	k.at(t.Add(-48 * time.Hour))
	return k.mintWith("acct-a", label, `expires_at "`+ts+`"`)
}

func (k *kg5State) bearerArrivesAt(label, ts string) error { return k.pricedAt(label, ts) }

func (k *kg5State) itIs401(code string) error { return k.statusCode(401, code) }

func (k *kg5State) itAuthenticates() error {
	if k.resp.code == 401 || k.resp.code == 403 || k.resp.code == 503 {
		return fmt.Errorf("the key did not authenticate: %d %.300s", k.resp.code, k.resp.body)
	}
	return nil
}

func (k *kg5State) expiredYesterday(label string) error {
	now := k.now()
	exp := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	k.at(now.Add(-72 * time.Hour))
	if err := k.mintWith("acct-a", label, `expires_at "`+exp+`"`); err != nil {
		return err
	}
	k.at(now)
	return nil
}

func (k *kg5State) authenticatesAgain(label string) error {
	if err := k.status(200); err != nil {
		return fmt.Errorf("the PATCH did not land: %w", err)
	}
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.itAuthenticates()
}

func (k *kg5State) listedExpired(label string) error {
	es, err := kg5Entries(k.resp.body)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e["id"] != k.keyID(label) {
			continue
		}
		s, _ := e["expires_at"].(string)
		t, err := time.Parse(time.RFC3339, s)
		if err != nil || !t.Before(k.now()) {
			return fmt.Errorf("%s expires_at %v, want a past time", label, e["expires_at"])
		}
		return nil
	}
	return fmt.Errorf("%s is not listed", label)
}

func (k *kg5State) keyDisabled(label string) error {
	return k.mintWith("acct-a", label, "disabled true")
}

func (k *kg5State) bearerArrives(label string) error {
	if strings.HasPrefix(label, "rog-key_") {
		return k.keyRelay(label, false, nil, nil)
	}
	return k.pricedRelay(label)
}

func (k *kg5State) patchDisabled(acct, label string) error {
	return k.acctPATCHes(acct, label, `{"disabled":true}`)
}

func (k *kg5State) streamCompletesNext401(code string) error {
	res, err := k.release()
	if err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	if err := k.settledOK(res, wallet); err != nil {
		return err
	}
	return k.nextBearer401(k.anyKey(), code)
}

func (k *kg5State) precedence(label, revoked, expired, disabled string) error {
	spec := []string{}
	if disabled == "disabled" {
		spec = append(spec, "disabled true")
	}
	if expired == "expired" {
		now := k.now()
		k.at(now.Add(-72 * time.Hour))
		spec = append(spec, `expires_at "`+now.Add(-24*time.Hour).UTC().Format(time.RFC3339)+`"`)
		defer k.at(now)
	}
	if err := k.mintWith("acct-a", label, strings.Join(spec, ", ")); err != nil {
		return err
	}
	if revoked == "revoked" {
		if err := k.do("acct:acct-a", http.MethodDelete, "/account/keys/"+k.keyID(label), nil); err != nil {
			return err
		}
		return k.status(204)
	}
	return nil
}

func (k *kg5State) code401Msg(code, msg string) error {
	if err := k.statusCode(401, code); err != nil {
		return err
	}
	if k.errMsg() != msg {
		return fmt.Errorf("message %q, want %q", k.errMsg(), msg)
	}
	return nil
}

// samePathAsLive compares the shared-store work of an unknown secret against a key that
// stops at the same decision point (a disabled key: looked up, then refused).
func (k *kg5State) samePathAsLive() error {
	if err := k.mintWith("acct-a", "kprobe", "disabled true"); err != nil {
		return err
	}
	body := k.relayBody(k.model, false, nil)
	miss := k.cmds(func() { _, _ = k.relayRaw(k.b, "rog-key_notarealsecret", body, nil) })
	hit := k.cmds(func() { _, _ = k.relayRaw(k.b, "kprobe", body, nil) })
	if miss != hit || miss == 0 {
		return fmt.Errorf("an unknown secret ran %d shared-store commands, a looked-up key %d: the same sha256-then-lookup path runs for both", miss, hit)
	}
	return nil
}

func (k *kg5State) bearerPlusSig(label, other string) error {
	if err := k.ensureKey("acct-a", label); err != nil {
		return err
	}
	_, err := k.acct(other)
	return err
}

func (k *kg5State) resolvesIdentity() error {
	k.ensurePriced()
	return k.keyRelay(k.anyKey(), false, nil, map[string]string{"sign": "acct-b"})
}

func (k *kg5State) bearerWins(label, acct string) error {
	a, err := k.acct(acct)
	if err != nil {
		return err
	}
	if err := k.status(200); err != nil {
		return err
	}
	ok, err := k.settledOn(a.who.wallet, k.resp.reqID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the request did not settle as %s of %s", label, acct)
	}
	b, _ := k.walletOf("acct-b")
	if ok, _ := k.settledOn(b, k.resp.reqID); ok {
		return fmt.Errorf("the signature's account was billed")
	}
	return nil
}

func (k *kg5State) acctOwnsNode(acct, node string) error {
	a, err := k.acct(acct)
	if err != nil {
		return err
	}
	owner := &fstation{ownerPriv: a.who.priv, acct: hex.EncodeToString(a.who.priv.Public().(ed25519.PublicKey))}
	saved := k.model
	st := k.standUp(node, stationOpts{model: k.model, priceIn: 1, priceOut: 2, owner: owner})
	k.model = saved
	k.b.mu.Lock()
	k.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	k.b.mu.Unlock()
	k.scriptJSON(node, 20, "", 0)
	return k.ensureKey(acct, "k1")
}

func (k *kg5State) servedBy(label, node string) error {
	st, ok := k.stations[node]
	if !ok {
		return fmt.Errorf("no station %q", node)
	}
	return k.keyRelay(label, false, nil, map[string]string{"X-Roger-Node": st.id})
}

func (k *kg5State) billedAtOffer() error {
	if err := k.status(200); err != nil {
		return err
	}
	if c, _ := strconv.ParseFloat(k.resp.hdr.Get("X-RogerAI-Cost"), 64); c <= 0 {
		return fmt.Errorf("X-RogerAI-Cost %q: a key request on the account's own node must be billed", k.resp.hdr.Get("X-RogerAI-Cost"))
	}
	return nil
}

// multi-instance

func (k *kg5State) patchModelsOnA(acct, label, models string) error {
	if err := k.ensureKey(acct, label); err != nil {
		return err
	}
	if err := k.acctPATCHes(acct, label, `{"allowed_models":`+models+`}`); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) nextOnBFor(label, model, code string) error {
	k.ensureStation("kg5-"+model, model, 1, 2)
	if err := k.bearerOnBModel(label, model); err != nil {
		return err
	}
	return k.statusCode(403, code)
}

func (k *kg5State) bearerOnBModel(label, model string) error {
	if k.b2 == nil {
		k.instanceB()
	}
	for id, n := range k.b.nodes {
		k.b2.nodes[id] = n
		k.b2.tunnels[id] = k.b.tunnels[id]
		k.b2.trust[id] = k.b.trust[id]
	}
	return k.keyRelayOn(k.b2, label, model, false, nil, nil)
}

func (k *kg5State) mintsOnA(acct, label string) error {
	if k.b2 == nil {
		k.instanceB()
	}
	return k.mintedSimple(acct, label)
}

func (k *kg5State) authOnB(label string) error {
	if err := k.bearerOnB(label); err != nil {
		return err
	}
	return k.itAuthenticates()
}

func (k *kg5State) storeDownNoCache() error {
	if err := k.ensureKey("acct-a", "k1"); err != nil {
		return err
	}
	k.instanceB() // a fresh instance: nothing in its local cache
	k.b = k.b2
	k.mr.Close()
	k.mrClosed = true
	return nil
}

func (k *kg5State) code503Paren(msg string) error { return k.statusMsg(503, msg) }

// audit

func (k *kg5State) performs(acct, method, path, body string) error {
	if strings.Contains(path, "k1") {
		if err := k.ensureKey(acct, "k1"); err != nil {
			return err
		}
		path = strings.ReplaceAll(path, "k1", k.keyID("k1"))
	}
	var b []byte
	if body != "" {
		b = []byte(body)
	} else if method == http.MethodPost {
		b = []byte(`{"name":"audited"}`)
	}
	if err := k.do("acct:"+acct, method, path, b); err != nil {
		return err
	}
	if k.resp.code >= 300 {
		return fmt.Errorf("%s %s = %d: %.300s", method, path, k.resp.code, k.resp.body)
	}
	if method == http.MethodPost {
		return k.recordMint(acct, "audited", k.resp)
	}
	return nil
}

func (k *kg5State) auditRow(alias, action string) error {
	wallet, err := k.walletOf(alias)
	if err != nil {
		return err
	}
	rows, err := k.keyEvents(wallet)
	if err != nil {
		return err
	}
	keyID := k.keyID("k1")
	if action == "mint" {
		keyID = k.keyID("audited")
	}
	for _, r := range rows {
		if strings.Contains(r.Ref, keyID) && strings.Contains(r.Ref, action) {
			if action == "patch" && !strings.Contains(r.Ref, "limit_usd") {
				return fmt.Errorf("the patch audit row does not name the changed field limit_usd: %+v", r)
			}
			return nil
		}
	}
	return fmt.Errorf("no key_event row for %s %s on %s: %+v", action, keyID, alias, rows)
}

func (k *kg5State) rowCarriesNothing() error {
	wallet, _ := k.walletOf("acct-a")
	rows, err := k.keyEvents(wallet)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no key_event rows")
	}
	for _, r := range rows {
		if r.Amount != 0 {
			return fmt.Errorf("a key_event row moves money: %+v", r)
		}
		for _, kk := range k.keys {
			if strings.Contains(r.Ref, kk.secret) || strings.Contains(r.Ref, kg5Hash(kk.secret)) {
				return fmt.Errorf("a key_event row carries a secret or its hash: %+v", r)
			}
		}
		if strings.Contains(r.Ref, `"limit_usd":2`) || strings.Contains(r.Ref, "limit_usd=2") {
			return fmt.Errorf("a key_event row carries a field value: %+v", r)
		}
	}
	return nil
}

func (k *kg5State) noKeyNoAudit() error {
	if err := k.status(400); err != nil {
		return err
	}
	n, err := k.count("acct-a")
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("%d keys exist after a refused mint", n)
	}
	wallet, _ := k.walletOf("acct-a")
	rows, err := k.keyEvents(wallet)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("a refused mint wrote audit rows: %+v", rows)
	}
	return nil
}

func (k *kg5State) bearerPATCHesNoBody(label, target string) error {
	if err := k.ensureKey("acct-a", label); err != nil {
		return err
	}
	return k.bearerPATCHes(label, target, `{"name":"x"}`)
}

func (k *kg5State) deniedAudit() error {
	wallet, _ := k.walletOf("acct-a")
	rows, err := k.keyEvents(wallet)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if strings.Contains(r.Ref, "denied") && strings.Contains(r.Ref, k.keyID("k1")) {
			return nil
		}
	}
	return fmt.Errorf("no \"denied\" key_event row naming %s: %+v", k.keyID("k1"), rows)
}

func (k *kg5State) mintedAndDeletedKeys(acct string) error {
	if err := k.mintedSimple(acct, "e1"); err != nil {
		return err
	}
	if err := k.mintedSimple(acct, "e2"); err != nil {
		return err
	}
	if err := k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID("e2"), nil); err != nil {
		return err
	}
	return k.status(204)
}

// getsExport: the export endpoint serves POST behind a web session (account.go accountExport).
func (k *kg5State) getsExport(acct string) error {
	return k.do("cookie:"+acct, http.MethodPost, "/account/export", nil)
}

func (k *kg5State) exportLists() error {
	if err := k.status(200); err != nil {
		return err
	}
	body := string(k.resp.body)
	for _, l := range []string{"e1", "e2"} {
		kk := k.keys[l]
		if !strings.Contains(body, kk.id) || !strings.Contains(body, `"`+l+`"`) {
			return fmt.Errorf("the export does not list key %s (%s) by id and name", l, kk.id)
		}
		if strings.Contains(body, kk.secret) || strings.Contains(body, kg5Hash(kk.secret)) {
			return fmt.Errorf("the export carries a secret or hash")
		}
	}
	if !strings.Contains(body, "key_event") {
		return fmt.Errorf("the export has no key_event rows")
	}
	return nil
}

func (k *kg5State) postsAccountDelete(acct string) error {
	if err := k.ensureKey(acct, "k1"); err != nil {
		return err
	}
	if err := k.ensureKey(acct, "k2"); err != nil {
		return err
	}
	return k.do("cookie:"+acct, http.MethodPost, "/account/delete", []byte(`{}`))
}

func (k *kg5State) everyKeyRevoked(acct string) error {
	if err := k.status(200); err != nil {
		return fmt.Errorf("account delete: %w", err)
	}
	for _, l := range []string{"k1", "k2"} {
		if err := k.nextBearer401(l, "key_revoked"); err != nil {
			return err
		}
	}
	wallet, _ := k.walletOf(acct)
	rows, err := k.keyEvents(wallet)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("key_event rows still name the deleted account's wallet: %d", len(rows))
	}
	return nil
}

// privacy

func (k *kg5State) mintAndServe(acct, label, _ string) error {
	k.logMark = len(k.logs.String())
	if err := k.mintedSimple(acct, label); err != nil {
		return err
	}
	k.ensurePriced()
	if err := k.keyRelay(label, false, nil, nil); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) logsNameIDsOnly() error {
	logs := k.logs.String()[k.logMark:]
	kk := k.keys[k.anyKey()]
	if strings.Contains(logs, "rog-key_") || strings.Contains(logs, kg5Hash(kk.secret)) {
		return fmt.Errorf("a log line carries a secret or its sha256")
	}
	if !strings.Contains(logs, kk.id) {
		return fmt.Errorf("no log line names %s", kk.id)
	}
	return nil
}

func (k *kg5State) bearerRefused(raw string) error { return k.keyRelay(raw, false, nil, nil) }

func (k *kg5State) bodyNoEcho(s string) error {
	if err := k.status(401); err != nil {
		return err
	}
	if bytes.Contains(k.resp.body, []byte(s)) {
		return fmt.Errorf("the 401 body echoes the bearer: %s", k.resp.body)
	}
	return nil
}

func (k *kg5State) bearerServed(label string) error {
	if err := k.ensureKey("acct-a", label); err != nil {
		return err
	}
	k.ensurePriced()
	if err := k.keyRelay(label, false, nil, nil); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) lineageAndGeneration(label string) error {
	id := k.keyID(label)
	reqID := k.resp.reqID
	con, err := k.call(kg5Req{as: "acct:acct-a", method: http.MethodGet, path: "/console"})
	if err != nil {
		return err
	}
	if !bytes.Contains(con.body, []byte(`"key_id":"`+id+`"`)) {
		return fmt.Errorf("the console lineage does not carry key_id %s: %.300s", id, con.body)
	}
	gen, err := k.call(kg5Req{as: "acct:acct-a", method: http.MethodGet, path: "/generation?id=" + reqID})
	if err != nil {
		return err
	}
	if gen.js["key_id"] != id {
		return fmt.Errorf("GET /generation key_id %v, want %s: %.300s", gen.js["key_id"], id, gen.body)
	}
	return nil
}

func (k *kg5State) keyIDNotWallet() error {
	if err := k.ensureKey("acct-a", "k1"); err != nil {
		return err
	}
	id := k.keyID("k1")
	k.ensurePriced()
	if err := k.keyRelay("", false, nil, map[string]string{"X-Roger-User": id}); err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	if ok, _ := k.settledOn(wallet, k.resp.reqID); ok {
		return fmt.Errorf("an X-Roger-User of %s billed the key's account", id)
	}
	if reservedID(id) == false && k.resp.code == 200 {
		return fmt.Errorf("%s was accepted as an X-Roger-User identity (%d)", id, k.resp.code)
	}
	return nil
}

// adversarial

func (k *kg5State) privateAllowed(label string) error {
	st := k.ensureStation("node-private", k.model, 1, 2)
	k.b.private[st.id] = true
	code := "147.520 MHz · KGFV-" + strings.ToUpper(k.nonce[:4])
	k.bandCode = code
	if err := k.db.CreateBand(store.Band{ID: "band_" + k.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: st.acct, NodeID: st.id, CreatedAt: time.Now().Unix()}); err != nil {
		return err
	}
	return k.mintWith("acct-a", label, `allowed_nodes ["node-private"]`)
}

func (k *kg5State) arrivesNoFreq(label string) error { return k.keyRelay(label, false, nil, nil) }

func (k *kg5State) is503NoMatchScope() error { return k.statusCode(503, "no_match") }

func (k *kg5State) withBandCode() error {
	return k.keyRelay(k.anyKey(), false, nil, map[string]string{"X-Roger-Freq": k.bandCode})
}

func (k *kg5State) servedOn(node string) error {
	if err := k.status(200); err != nil {
		return err
	}
	if got := k.resp.hdr.Get("X-RogerAI-Provider"); got != k.st(node).id {
		return fmt.Errorf("served by %q, want %s", got, k.st(node).id)
	}
	return nil
}

func (k *kg5State) grantDenies(owner, label, model string) error {
	st := k.ensureStation(owner+"-node", k.model, 1, 2)
	secret := "rog-grant_" + label + "_" + k.nonce
	gr := store.Grant{ID: "grant_" + label + "_" + k.nonce, SecretHash: kg5Hash(secret), Owner: st.acct, Label: label,
		Models: []string{"some-other-model"}, Free: true, CreatedAt: time.Now().Unix()}
	if err := k.db.CreateGrant(gr); err != nil {
		return err
	}
	if err := rs1GrantWalletRow(k.db, gr); err != nil {
		return err
	}
	k.grants[label] = secret
	_ = model
	return nil
}

func (k *kg5State) grantRequest403(label, model, msg string) error {
	k.ensureStation("kg5-"+model, model, 1, 2)
	if err := k.keyRelayOn(k.b, label, model, false, nil, nil); err != nil {
		return err
	}
	return k.statusMsg(403, msg)
}

func (k *kg5State) keyAllowsNodeA(label string) error {
	k.ensureStation("node-a", k.model, 1, 2)
	k.ensureStation("node-b", k.model, 0.5, 1)
	return k.mintWith("acct-a", label, `allowed_nodes ["node-a"]`)
}

func (k *kg5State) sendsOnly(label string) error {
	k.resps = nil
	only := []string{k.st("node-a").id, k.st("node-b").id}
	for i := 0; i < 8; i++ {
		if err := k.keyRelay(label, false, map[string]any{"provider": map[string]any{"only": only}}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) intersectionOnly() error {
	for i, r := range k.resps {
		if r.code != 200 {
			return fmt.Errorf("relay %d = %d: %.300s", i, r.code, r.body)
		}
		if got := r.hdr.Get("X-RogerAI-Provider"); got != k.st("node-a").id {
			return fmt.Errorf("relay %d served by %q, outside the key's allow-list {node-a}", i, got)
		}
	}
	return nil
}

func (k *kg5State) alsoSendsUser(label, user string) error {
	if err := k.ensureKey("acct-a", label); err != nil {
		return err
	}
	w, err := k.walletOf(user)
	if err != nil {
		return err
	}
	k.ensurePriced()
	return k.keyRelay(label, false, nil, map[string]string{"X-Roger-User": w})
}

func (k *kg5State) payerIgnoringHeader(alias string) error { return k.payerIs(alias) }

func (k *kg5State) mintBurst(acct string, n int) error {
	k.resps = nil
	for i := 0; i < n; i++ {
		if _, err := k.call(kg5Req{as: "acct:" + acct, method: http.MethodPost, path: "/account/keys", body: []byte(fmt.Sprintf(`{"name":"burst%d"}`, i))}); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) limiterRefuses() error {
	created, limited := 0, 0
	for _, r := range k.resps {
		switch r.code {
		case 201:
			created++
		case 429:
			limited++
			if r.hdr.Get("Retry-After") == "" {
				return fmt.Errorf("a 429 without Retry-After")
			}
		default:
			return fmt.Errorf("a burst mint answered %d: %.300s", r.code, r.body)
		}
	}
	if limited == 0 {
		return fmt.Errorf("none of %d mints was rate-limited", len(k.resps))
	}
	n, err := k.count("acct-a")
	if err != nil {
		return err
	}
	if n != created {
		return fmt.Errorf("%d keys exist, %d mints succeeded", n, created)
	}
	return nil
}

func (k *kg5State) leakedUsed(label string) error {
	if err := k.ensureKey("acct-a", label); err != nil {
		return err
	}
	k.ensurePriced()
	if err := k.keyRelay(label, false, nil, map[string]string{"remote": "203.0.113.9:4444"}); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) nextUse401Audit(code string) error {
	usedReq := k.resps[len(k.resps)-2].reqID
	if err := k.nextBearer401(k.anyKey(), code); err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	rows, err := k.keyEvents(wallet)
	if err != nil {
		return err
	}
	var mint, del bool
	for _, r := range rows {
		if strings.Contains(r.Ref, "mint") {
			mint = true
		}
		if strings.Contains(r.Ref, "delete") {
			del = true
		}
	}
	if !mint || !del {
		return fmt.Errorf("the audit stream shows mint=%v delete=%v: %+v", mint, del, rows)
	}
	if ok, _ := k.settledOn(wallet, usedReq); !ok {
		return fmt.Errorf("the leaked use %q is not in the lineage", usedReq)
	}
	return nil
}

func (k *kg5State) bearerPOSTsGrants(label string) error {
	if err := k.ensureKey("acct-a", label); err != nil {
		return err
	}
	return k.do("bearer:"+label, http.MethodPost, "/grants", []byte(`{"name":"via-key","free":true}`))
}

func (k *kg5State) grants401() error { return k.status(401) }

func (k *kg5State) unknownAndRevoked() error {
	if err := k.mintedSimple("acct-a", "krev"); err != nil {
		return err
	}
	if err := k.do("acct:acct-a", http.MethodDelete, "/account/keys/"+k.keyID("krev"), nil); err != nil {
		return err
	}
	body := k.relayBody(k.model, false, nil)
	k.mcounts[0] = k.cmds(func() { _, _ = k.relayRaw(k.b, "rog-key_unknown"+k.nonce, body, nil) })
	k.mcounts[1] = k.cmds(func() { _, _ = k.relayRaw(k.b, "krev", body, nil) })
	return nil
}

func (k *kg5State) bothLookup() error {
	if k.mcounts[0] != k.mcounts[1] {
		return fmt.Errorf("an unknown secret ran %d shared-store commands and a revoked one %d", k.mcounts[0], k.mcounts[1])
	}
	return nil
}

func (k *kg5State) neitherEarly() error {
	if k.mcounts[0] == 0 || k.mcounts[1] == 0 {
		return fmt.Errorf("a refusal returned before any lookup (%v commands)", k.mcounts)
	}
	return nil
}

// ---- background ----------------------------------------------------------------------------------

func (k *kg5State) wired() error {
	if k.db == nil || k.b.shared == nil {
		return fmt.Errorf("the broker has no money store or no shared store")
	}
	return nil
}

func (k *kg5State) loggedIn(label, alias string, bal float64) error {
	return k.account(label, alias, bal)
}

func (k *kg5State) signsWithDevice(label string) error {
	a, err := k.acct(label)
	if err != nil {
		return err
	}
	pub := hex.EncodeToString(a.who.priv.Public().(ed25519.PublicKey))
	if _, ok, err := k.db.OwnerByPubkey(pub); err != nil || !ok {
		return fmt.Errorf("%s's device key is not bound to an account (%v)", label, err)
	}
	k.signer = label
	return nil
}

func (k *kg5State) docString(acct string, doc *godog.DocString) error {
	return k.postBody(acct, strings.ReplaceAll(doc.Content, "\n", " "))
}

// ---- registration ----------------------------------------------------------------------------------

func (k *kg5State) registerCommon(sc *godog.ScenarioContext) {
	sc.Step(`^a broker with the money store and the shared store wired$`, k.wired)
	sc.Step(`^account "([^"]+)" is logged in \(wallet "([^"]+)"\) with balance \$([0-9.]+)$`, k.loggedIn)
	k.registerStateAudit(sc)
}

func (k *kg5State) registerGuardrails(sc *godog.ScenarioContext) {
	k.registerCommon(sc)
	sc.Step(`^"([^"]+)" signs its management requests with its bound device key$`, k.signsWithDevice)

	// minting
	sc.Step(`^"([^"]+)" POSTs /account/keys with (\{(?s:.*)\})$`, k.postBody)
	sc.Step(`^"([^"]+)" POSTs /account/keys with:$`, k.docString)
	sc.Step(`^the response is 201 with "id" starting "key_" and "secret" starting "rog-key_"$`, k.created201IDSecret)
	sc.Step(`^the store holds sha256\(secret\) and never the secret$`, k.storeHoldsHashOnly)
	sc.Step(`^a later GET /account/keys shows "([^"]+)" with no "secret" field$`, k.laterListShows)
	sc.Step(`^the response is (\d+)$`, k.status)
	sc.Step(`^the key has limit_usd 0 \(unlimited\), reset "none", no expiry, empty allow-lists, disabled false$`, k.defaultsUnlimited)
	sc.Step(`^the response is 201 and GET /account/keys echoes every field as sent$`, k.echoesEverything)
	sc.Step(`^"([^"]+)" minted key "([^"]+)" whose secret ends in "([^"]+)"$`, func(a, l, _ string) error { return k.mintedSimple(a, l) })
	sc.Step(`^"([^"]+)" GETs /account/keys$`, func(a string) error { return k.do("acct:"+a, http.MethodGet, "/account/keys", nil) })
	sc.Step(`^"([^"]+)" shows "hint":"\.\.\.([^"]+)" and no other part of the secret$`, k.hintOnly)
	sc.Step(`^"([^"]+)" minted key "([^"]+)"$`, k.mintedSimple)
	sc.Step(`^a relay authenticates with "([^"]+)"$`, k.relayAuthWith)
	sc.Step(`^the payer wallet is "([^"]+)"$`, k.payerIs)
	sc.Step(`^a request bearing "([^"]+)" POSTs /account/keys$`, k.bearerPOSTsKeys)
	sc.Step(`^the response is 403 with code "([^"]+)"$`, func(c string) error { return k.statusCode(403, c) })
	sc.Step(`^no key was created$`, k.noKeyCreated)
	sc.Step(`^"([^"]+)" minted keys "([^"]+)" and "([^"]+)"$`, func(a, x, y string) error {
		if err := k.mintedSimple(a, x); err != nil {
			return err
		}
		return k.mintedSimple(a, y)
	})
	sc.Step(`^a request bearing "([^"]+)" GETs /account/keys$`, k.bearerGETsKeys)
	sc.Step(`^a request bearing "([^"]+)" PATCHes /account/keys/(\S+) with (\{.*\})$`, k.bearerPATCHes)
	sc.Step(`^the response is 403 with code "([^"]+)" and "([^"]+)" is unchanged$`, k.code403Unchanged)
	sc.Step(`^a request bearing "([^"]+)" DELETEs /account/keys/(\S+)$`, k.bearerDELETEs)
	sc.Step(`^the response is 403 with code "([^"]+)" and "([^"]+)" still exists$`, k.code403StillExists)
	sc.Step(`^a device keypair that never logged in$`, func() error { _, k.unbound, _ = ed25519.GenerateKey(nil); return nil })
	sc.Step(`^it POSTs /account/keys with (\{.*\})$`, k.unboundPOSTs)
	sc.Step(`^the response is 401 asking to log in \(the same gate as /account/limit\)$`, k.askLogin)
	sc.Step(`^an unsigned POST /account/keys arrives with an X-Roger-User header$`, k.unsignedPOST)
	sc.Step(`^the response is 401 "([^"]+)"$`, func(m string) error { return k.statusMsg(401, m) })
	sc.Step(`^"([^"]+)" holds a valid web session cookie$`, k.webCookie)
	sc.Step(`^the browser POSTs /account/keys with (\{.*\}) from an allowlisted Origin$`, k.browserPOSTs)
	sc.Step(`^a POST /account/keys arrives with that cookie from an unlisted Origin$`, k.foreignOriginPOST)
	sc.Step(`^the response is 401 and no key was created$`, k.code401NoKey)
	sc.Step(`^owner "([^"]+)" minted grant "([^"]+)"$`, k.ownerGrant)
	sc.Step(`^"([^"]+)" POSTs /account/keys with header "Idempotency-Key: ([^"]+)" and (\{.*\})$`, k.postIdem)
	sc.Step(`^the same request is replayed within 10 minutes$`, k.replay)
	sc.Step(`^the response is 409 with code "already_minted" naming the first key's id$`, k.alreadyMinted)
	sc.Step(`^no second key exists and no secret is returned \(the secret is shown once, ever\)$`, k.noSecondKey)
	sc.Step(`^"([^"]+)" minted "([^"]+)" with "Idempotency-Key: ([^"]+)"$`, k.mintedIdem)
	sc.Step(`^"([^"]+)" POSTs the same body with "Idempotency-Key: ([^"]+)"$`, k.sameBodyIdem)
	sc.Step(`^the response is 201 with a different id$`, k.differentID)
	sc.Step(`^"([^"]+)" POSTs (\{.*\}) twice with no Idempotency-Key$`, k.postTwice)
	sc.Step(`^two keys named "([^"]+)" exist \(names are labels, not identifiers\)$`, k.twoNamed)
	sc.Step(`^"([^"]+)" holds (\d+) non-deleted keys$`, k.holdsN)
	sc.Step(`^the response is 400 with code "([^"]+)" and message "([^"]+)"$`, k.code400Msg)
	sc.Step(`^"([^"]+)" holds (\d+) keys and deletes one$`, k.holdsNDeletesOne)

	// validation
	sc.Step(`^the response is 400 with code "([^"]+)" and the message names "([^"]+)"$`, k.code400Names)
	sc.Step(`^the response is 201 and the key is unlimited \(limit_usd 0\)$`, k.unlimited201)
	sc.Step(`^the response is 201 and limit_usd reads back ([0-9.]+)$`, k.limitReadsBack201)
	sc.Step(`^limit_usd reads back ([0-9.]+)$`, k.limitReadsBack)
	sc.Step(`^the response is 201 and the key is unlimited with reset "([^"]+)" \(nothing to reset\)$`, k.unlimitedWithReset)
	sc.Step(`^"([^"]+)" POSTs /account/keys with 64 allowed_models and 64 allowed_nodes$`, k.post64)
	sc.Step(`^"([^"]+)" POSTs /account/keys with 65 entries in "([^"]+)"$`, k.post65)
	sc.Step(`^the response is 400 with code "([^"]+)" naming "([^"]+)"$`, k.code400Names)
	sc.Step(`^the response is 201 and allowed_models reads back (\[.*\])$`, k.modelsReadBack201)
	sc.Step(`^"([^"]+)" minted key "([^"]+)" with (.+)$`, k.mintWith)
	sc.Step(`^a relay bearing "([^"]+)" asks for "([^"]+)"$`, k.relayAsksFor)
	sc.Step(`^the response is 403 with code "([^"]+)" \(no case folding, no prefix match\)$`, k.code403Paren)
	sc.Step(`^a relay bearing "([^"]+)" asks for any on-air model$`, k.asksAnyModel)
	sc.Step(`^no key_model_denied or key_node_denied is possible for it$`, k.noDenialPossible)
	sc.Step(`^the response is 201 and expires_at reads back "([^"]+)"$`, k.expiresReadsBack)
	sc.Step(`^the clock reads ([0-9TZ:-]+)$`, k.clockReads)
	sc.Step(`^the response is 400 naming "([^"]+)"$`, k.code400NamesField)
	sc.Step(`^"([^"]+)" POSTs /account/keys with a 5 KiB body$`, k.post5KiB)

	// patching
	sc.Step(`^"([^"]+)" PATCHes /account/keys/(\S+) with (\{.*\})$`, k.acctPATCHes)
	sc.Step(`^"([^"]+)" has name "([^"]+)", limit_usd ([0-9.]+), reset "([^"]+)", allowed_models (\[.*\])$`, k.hasNameLimitResetModels)
	sc.Step(`^the response is 200 and "([^"]+)" reads back (\{.*\}) with every other field at its default$`, k.patchReadsBack)
	sc.Step(`^"([^"]+)" allows all models$`, k.allowsAllModels)
	sc.Step(`^allowed_models is still \[\] \(a missing field never resets anything\)$`, k.stillEmptyModels)
	sc.Step(`^"([^"]+)" never expires$`, k.neverExpires)
	sc.Step(`^the next priced relay bearing "([^"]+)" is 402 with limit_source "([^"]+)"$`, k.nextPricedIs402)
	sc.Step(`^the \$([0-9.]+) already settled is not reversed$`, k.notReversed)
	sc.Step(`^a stream bearing "([^"]+)" is mid-flight under a \$([0-9.]+) limit$`, k.streamUnderLimit)
	sc.Step(`^"([^"]+)" PATCHes "([^"]+)" to limit_usd ([0-9.]+)$`, k.patchLimit)
	sc.Step(`^the stream completes and settles under its original hold$`, k.streamCompletesOriginalHold)
	sc.Step(`^the next request bearing "([^"]+)" is (\d+)$`, k.nextRequestIs)
	sc.Step(`^the response is 403 with code "([^"]+)" and the limit is still ([0-9.]+)$`, k.code403LimitStill)
	sc.Step(`^a relay bearing "([^"]+)" is 402 with limit_source "([^"]+)"$`, k.relayIs402Source)
	sc.Step(`^the next relay bearing "([^"]+)" is served$`, k.nextServed)
	sc.Step(`^"([^"]+)" PATCHes "([^"]+)" to reset "([^"]+)"$`, k.patchReset)
	sc.Step(`^the window usage shown is the spend since the PATCH \(\$0\.00\), and the \$([0-9.]+) stays in lifetime usage$`, k.windowSincePatch)
	sc.Step(`^limit_remaining reads \$([0-9.]+) \(lifetime spend counts\)$`, k.remainingLifetime)
	sc.Step(`^the response is 404 with the uniform "no such key" body$`, k.uniform404)
	sc.Step(`^the response is 404 with the uniform "no such key" body, byte-identical to the unknown-id case$`, k.uniform404Identical)
	sc.Step(`^"([^"]+)" is unchanged$`, k.unchanged)
	sc.Step(`^the response is 400 with code "([^"]+)"$`, k.code400)
	sc.Step(`^"([^"]+)" PATCHes "([^"]+)" to disabled true, then disabled false$`, k.disableThenEnable)
	sc.Step(`^limit_remaining reads \$([0-9.]+)$`, k.remaining)

	// listing
	sc.Step(`^"([^"]+)" minted keys "([^"]+)" \(limit 5 weekly, \$1\.25 spent this week\) and "([^"]+)" \(unlimited\)$`, k.mintedTwoForList)
	sc.Step(`^each entry has (.+)$`, k.eachEntryHas)
	sc.Step(`^"([^"]+)" shows limit_remaining ([0-9.]+) and "([^"]+)" shows limit_remaining null \(unlimited\)$`, k.remainingAndNull)
	sc.Step(`^no entry has a "secret" or "secret_hash" field$`, k.noSecretFields)
	sc.Step(`^key "([^"]+)" settled \$1\.00 yesterday \(UTC\), \$2\.00 today, \$4\.00 last week, \$8\.00 last month$`, k.settledAcrossWindows)
	sc.Step(`^"([^"]+)" GETs /account/keys on a day in the same week and month as "today"$`, k.getsListToday)
	sc.Step(`^"([^"]+)" shows usage_daily ([0-9.]+), usage_weekly ([0-9.]+), usage_monthly ([0-9.]+), usage ([0-9.]+)$`, k.usageWindows)
	sc.Step(`^key "([^"]+)" settled \$3\.00 and one \$1\.00 row was later reversed by a chargeback$`, k.settledThenChargeback)
	sc.Step(`^"([^"]+)" shows usage ([0-9.]+) \(the same rule as month spend, features/money/caps\.feature\)$`, k.usageIs)
	sc.Step(`^key "([^"]+)" last made a request that was refused 402 at T$`, k.lastRequest402At)
	sc.Step(`^"([^"]+)" shows last_used T$`, k.lastUsedT)
	sc.Step(`^"([^"]+)" minted "([^"]+)" and "([^"]+)" minted "([^"]+)"$`, k.twoAcctsMinted)
	sc.Step(`^only "([^"]+)" is listed$`, k.onlyListed)
	sc.Step(`^"([^"]+)" GETs /account/keys/(\S+)$`, k.acctGETsByID)
	sc.Step(`^the response is the same entry the list shows$`, k.sameAsList)
	sc.Step(`^"([^"]+)" minted "([^"]+)"$`, k.mintedSimple)
	sc.Step(`^the response is 404, byte-identical to GET /account/keys/key_doesnotexist$`, k.identical404ToUnknown)
	sc.Step(`^"([^"]+)" GETs /account/keys/<id> for 1000 guessed ids$`, k.guess1000)
	sc.Step(`^every miss is the same 404 body, byte-identical whether the id exists under another account or not at all$`, k.everyMissSame)
	sc.Step(`^both cases run the same lookup-then-owner-compare path \(constant-work structure, as the band-code resolve; no timing claim is made\)$`, k.sameLookupPath)
	sc.Step(`^"([^"]+)" minted "([^"]+)" and deleted it$`, k.mintedAndDeleted)
	sc.Step(`^"([^"]+)" is absent$`, k.absent)
	sc.Step(`^"([^"]+)" minted (\d+) keys$`, k.mintedN)
	sc.Step(`^all (\d+) come back newest-first in one page \(the per-account cap is below the page size\)$`, k.newestFirst)

	// deleting
	sc.Step(`^"([^"]+)" DELETEs /account/keys/(\S+)$`, k.acctDELETEs)
	sc.Step(`^the next request bearing "([^"]+)" is 401 with code "([^"]+)"$`, k.nextBearer401)
	sc.Step(`^two broker instances share the store$`, k.twoInstancesShare)
	sc.Step(`^"([^"]+)" DELETEs "([^"]+)" on instance A$`, k.deleteOnA)
	sc.Step(`^a request bearing "([^"]+)" reaches instance B immediately after$`, k.bearerOnB)
	sc.Step(`^it is 401 with code "([^"]+)" \(the hash lookup is shared; any cache is invalidated on delete\)$`, k.code401Paren)
	sc.Step(`^a stream bearing "([^"]+)" is mid-flight$`, k.streamMidFlight)
	sc.Step(`^"([^"]+)" DELETEs "([^"]+)"$`, k.acctDELETEs)
	sc.Step(`^the stream runs to completion, settles against "([^"]+)", writes its receipt$`, k.streamSettlesAgainst)
	sc.Step(`^a new request bearing "([^"]+)" is 401 "([^"]+)"$`, k.newRequest401)
	sc.Step(`^a non-stream relay bearing "([^"]+)" has been dispatched$`, k.nonStreamDispatched)
	sc.Step(`^"([^"]+)" DELETEs "([^"]+)" before the response returns$`, k.deleteBeforeReturn)
	sc.Step(`^the relay settles normally and the response is 200$`, k.settlesNormally200)
	sc.Step(`^"([^"]+)" DELETEs "([^"]+)" twice$`, k.deleteTwice)
	sc.Step(`^the first is 204 and the second is 404 uniform \(a deleted key is indistinguishable from a never-minted id\)$`, k.firstThenUniform)
	sc.Step(`^the response is 404 and "([^"]+)" still authenticates$`, k.code404StillAuth)
	sc.Step(`^"([^"]+)" settled \$([0-9.]+) then was deleted$`, k.settledThenDeleted)
	sc.Step(`^"([^"]+)" GETs /usage$`, k.getsUsage)
	sc.Step(`^the \$([0-9.]+) is still attributed to key id "([^"]+)"$`, k.attributedToKey)
	sc.Step(`^"([^"]+)" has balance \$([0-9.]+) and monthly cap \$([0-9.]+)$`, k.balanceAndCap)
	sc.Step(`^balance is \$([0-9.]+) and the monthly cap is \$([0-9.]+)$`, k.balanceCapAre)

	// reset windows
	sc.Step(`^"([^"]+)" spent \$([0-9.]+) at ([0-9TZ:-]+)$`, k.spentAt)
	sc.Step(`^a priced relay bearing "([^"]+)" arrives at ([0-9TZ:-]+)$`, k.pricedAt)
	sc.Step(`^it is 402 with limit_source "([^"]+)"$`, k.itIs402Source)
	sc.Step(`^it is served \(window usage is \$0\.00\)$`, k.servedWindowZero)
	sc.Step(`^key "([^"]+)" with reset "weekly" spent \$5\.00 on Sunday 2026-10-04T23:00:00Z$`, k.weeklySunday)
	sc.Step(`^a priced relay arrives Monday ([0-9TZ:-]+)$`, k.pricedArrivesTS)
	sc.Step(`^the window usage is \$0\.00$`, k.windowZero)
	sc.Step(`^key "([^"]+)" with reset "monthly" spent \$5\.00 on 2026-10-31T23:59:59Z$`, k.monthlyLastSecond)
	sc.Step(`^a priced relay arrives ([0-9]{4}-[0-9TZ:-]+)$`, k.pricedArrivesTS)
	sc.Step(`^on 2026-11-30T23:59:59Z the window still holds November's spend$`, k.novemberHolds)
	sc.Step(`^on 2026-12-01T00:00:00Z it is \$0\.00 again$`, k.decemberZero)
	sc.Step(`^key "([^"]+)" with reset "monthly"$`, k.keyMonthly)
	sc.Step(`^the clock crosses 2028-02-29T23:59:59Z to 2028-03-01T00:00:00Z$`, k.crossLeap)
	sc.Step(`^the window resets exactly once, at the crossing$`, k.resetsOnceAtCrossing)
	sc.Step(`^key "([^"]+)" with reset "daily" and the broker host in a DST zone$`, k.dstKey)
	sc.Step(`^the local clock skips or repeats an hour$`, k.dstShift)
	sc.Step(`^the window boundary is still 00:00:00Z$`, k.boundaryStillMidnight)
	sc.Step(`^key "([^"]+)" with limit_usd 5 and reset "none" spent \$5\.00 in 2026$`, k.noneSpent2026)
	sc.Step(`^a priced relay arrives in 2027$`, k.pricedIn2027)
	sc.Step(`^two instances each settled \$2\.00 for key "([^"]+)" this window$`, k.twoInstancesSettled)
	sc.Step(`^either instance computes the window usage$`, k.eitherComputes)
	sc.Step(`^it reads \$([0-9.]+) \(a sum over the shared ledger, like monthSpend\)$`, k.readsSum)

	// expiry and disabled
	sc.Step(`^key "([^"]+)" with expires_at ([0-9TZ:-]+)$`, k.keyExpiresAt)
	sc.Step(`^a request bearing "([^"]+)" arrives at ([0-9TZ:-]+)$`, k.bearerArrivesAt)
	sc.Step(`^it is 401 with code "([^"]+)"$`, k.itIs401)
	sc.Step(`^it authenticates$`, k.itAuthenticates)
	sc.Step(`^key "([^"]+)" expired yesterday$`, k.expiredYesterday)
	sc.Step(`^"([^"]+)" PATCHes "([^"]+)" with (\{.*\})$`, k.acctPATCHesQuoted)
	sc.Step(`^"([^"]+)" authenticates again$`, k.authenticatesAgain)
	sc.Step(`^"([^"]+)" is listed with expires_at in the past \(the account decides whether to delete it\)$`, k.listedExpired)
	sc.Step(`^key "([^"]+)" with disabled true$`, k.keyDisabled)
	sc.Step(`^a request bearing "([^"]+)" arrives$`, k.bearerArrives)
	sc.Step(`^"([^"]+)" PATCHes "([^"]+)" to disabled true$`, k.patchDisabled)
	sc.Step(`^the stream completes and settles; the next request is 401 "([^"]+)"$`, k.streamCompletesNext401)
	sc.Step(`^key "([^"]+)" is (revoked|live), (expired|live), (disabled|live)$`, k.precedence)
	sc.Step(`^it is 401 with code "([^"]+)" and message "([^"]+)"$`, k.code401Msg)
	sc.Step(`^the sha256-then-lookup path is the same one a live key takes$`, k.samePathAsLive)
	sc.Step(`^a request bearing "([^"]+)" that also carries a valid X-Roger-Pubkey/TS/Sig of "([^"]+)"$`, k.bearerPlusSig)
	sc.Step(`^the broker resolves identity$`, k.resolvesIdentity)
	sc.Step(`^the request is "([^"]+)" of "([^"]+)"; the signature is ignored \(one credential per request, the bearer wins as it does for grants\)$`, k.bearerWins)
	sc.Step(`^"([^"]+)" owns node "([^"]+)"$`, k.acctOwnsNode)
	sc.Step(`^a relay bearing "([^"]+)" is served by "([^"]+)"$`, k.servedBy)
	sc.Step(`^it is billed at the offer price \(self-use \$0 requires the signed device path, not a key\)$`, k.billedAtOffer)

	// multi-instance
	sc.Step(`^two instances share the store$`, k.twoInstancesShare)
	sc.Step(`^"([^"]+)" PATCHes "([^"]+)" to allowed_models (\[.*\]) on A$`, k.patchModelsOnA)
	sc.Step(`^the next relay bearing "([^"]+)" on B for "([^"]+)" is 403 "([^"]+)"$`, k.nextOnBFor)
	sc.Step(`^"([^"]+)" mints "([^"]+)" on A$`, k.mintsOnA)
	sc.Step(`^a relay bearing "([^"]+)" on B authenticates$`, k.authOnB)
	sc.Step(`^the shared store is down and the key is not in the local cache$`, k.storeDownNoCache)
	sc.Step(`^it is 503 "([^"]+)" \(never a silent allow, never a 401 that reads as revoked\)$`, k.code503Paren)

	// audit
	sc.Step(`^"([^"]+)" performs (POST|PATCH|DELETE) (\S+)(?: (\{.*\}))?$`, k.performs)
	sc.Step(`^a ledger audit row of kind "key_event" exists with account "([^"]+)", key id, action "([^"]+)", and the changed field names$`, k.auditRow)
	sc.Step(`^the row carries \$0 money and no secret, hash, or field values$`, k.rowCarriesNothing)
	sc.Step(`^no key exists and no audit row is written \(nothing changed\)$`, k.noKeyNoAudit)
	sc.Step(`^a request bearing "([^"]+)" PATCHes /account/keys/(\S+)$`, k.bearerPATCHesNoBody)
	sc.Step(`^an audit row "denied" is written with the key id \(a key trying to manage keys is worth seeing\)$`, k.deniedAudit)
	sc.Step(`^"([^"]+)" minted and deleted keys$`, k.mintedAndDeletedKeys)
	sc.Step(`^"([^"]+)" POSTs /account/export$`, k.getsExport)
	sc.Step(`^the export lists the key ids, names, and key_event rows, and no secrets or hashes$`, k.exportLists)
	sc.Step(`^"([^"]+)" POSTs /account/delete$`, k.postsAccountDelete)
	sc.Step(`^every key of "([^"]+)" is revoked immediately and its key_event rows are anonymized like the rest of the account$`, k.everyKeyRevoked)

	// privacy
	sc.Step(`^"([^"]+)" mints "([^"]+)" and a relay bearing "([^"]+)" is served$`, k.mintAndServe)
	sc.Step(`^every log line about it names "key_\.\.\." and contains neither "rog-key_" nor the sha256$`, k.logsNameIDsOnly)
	sc.Step(`^a request bearing "([^"]+)" is refused$`, k.bearerRefused)
	sc.Step(`^the 401 body does not contain "([^"]+)"$`, k.bodyNoEcho)
	sc.Step(`^a relay bearing "([^"]+)" is served$`, k.bearerServed)
	sc.Step(`^the console lineage row and GET /generation\?id= carry "key_id":"([^"]+)"$`, k.lineageAndGeneration)
	sc.Step(`^"key_<rand>" is outside the reserved id namespaces \(u_, u_gh_, g_\) and can never be presented as an X-Roger-User$`, k.keyIDNotWallet)

	// adversarial
	sc.Step(`^key "([^"]+)" with allowed_nodes \["node-private"\] where "node-private" is band-only$`, k.privateAllowed)
	sc.Step(`^a relay bearing "([^"]+)" arrives with no freq$`, k.arrivesNoFreq)
	sc.Step(`^it is 503 no_match \(an allow-list is scope, not admission; the band stays hidden\)$`, k.is503NoMatchScope)
	sc.Step(`^the same relay carries the band's code$`, k.withBandCode)
	sc.Step(`^it is served on "([^"]+)"$`, k.servedOn)
	sc.Step(`^owner "([^"]+)" minted grant "([^"]+)" that denies "([^"]+)"$`, k.grantDenies)
	sc.Step(`^a request bearing "([^"]+)" for "([^"]+)" is 403 "([^"]+)" \(a key's allow-list is irrelevant to a grant request\)$`, k.grantRequest403)
	sc.Step(`^key "([^"]+)" with allowed_nodes \["node-a"\]$`, k.keyAllowsNodeA)
	sc.Step(`^a relay bearing "([^"]+)" sends provider\.only \["node-a","node-b"\]$`, k.sendsOnly)
	sc.Step(`^the effective allow set is \{node-a\} \(intersection\), never \{node-a,node-b\}$`, k.intersectionOnly)
	sc.Step(`^a relay bearing "([^"]+)" also sends X-Roger-User "([^"]+)"$`, k.alsoSendsUser)
	sc.Step(`^the payer is "([^"]+)" and the header is ignored \(features/relay/spend\.feature\)$`, k.payerIgnoringHeader)
	sc.Step(`^"([^"]+)" POSTs /account/keys (\d+) times in one minute$`, k.mintBurst)
	sc.Step(`^the requests past the management limiter are 429 with Retry-After and no key is created for them$`, k.limiterRefuses)
	sc.Step(`^"([^"]+)" leaked and was used from a new IP$`, k.leakedUsed)
	sc.Step(`^the next use is 401 "([^"]+)" and the audit stream shows mint, uses \(in lineage\), and delete$`, k.nextUse401Audit)
	sc.Step(`^a request bearing "([^"]+)" POSTs /grants$`, k.bearerPOSTsGrants)
	sc.Step(`^it is 401 \(grants need the signed owner identity; a key is a consumer credential\)$`, k.grants401)
	sc.Step(`^a request bearing an unknown secret and one bearing a revoked secret arrive$`, k.unknownAndRevoked)
	sc.Step(`^both run sha256 over the bearer and one shared-store lookup before any decision$`, k.bothLookup)
	sc.Step(`^neither returns before the lookup completes$`, k.neitherEarly)
}

// kg5Run runs one feature file over a fresh kg5State with its step set.
func kg5Run(t *testing.T, name, path string, register func(k *kg5State, sc *godog.ScenarioContext)) {
	k := newKG5(t)
	prev := log.Writer()
	log.SetOutput(k.logs)
	var once sync.Once
	t.Cleanup(func() {
		log.SetOutput(prev)
		once.Do(k.teardown)
	})
	suite := godog.TestSuite{
		Name: name,
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, k.reset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				k.teardown()
				return ctx, nil
			})
			register(k, sc)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{path},
			Tags: "~@cli && ~@web && ~@docs && ~@later", TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatalf("%s scenarios failed", name)
	}
}

func TestKeyGuardrailsBDD(t *testing.T) {
	kg5Run(t, "key_guardrails", "../../features/auth/key_guardrails.feature", func(k *kg5State, sc *godog.ScenarioContext) {
		k.registerGuardrails(sc)
	})
}
