package main

// key_limits_bdd_test.go makes features/relay/key_limits.feature EXECUTABLE (slice 5, RED):
// what an account key does to a relay - the per-key spend limit at the hold gate, its UTC
// window, the atomic key reserve beside the wallet hold, the 402 body and key headers, and the
// key's allow-lists - over the shared kg5State harness (key_guardrails_bdd_test.go): the real
// relayBroker, the real store (Postgres when ROGERAI_TEST_DATABASE_URL is set), the shared
// store over miniredis, real stations behind real tunnels with scripted upstreams.
//
// RED: the Background mints "k1"; POST /account/keys is not routed today, so every scenario
// stops there on its real assertion. The steps below are written against the contract.
//
// OBSERVATION CHOICES (stated, so GREEN knows what the steps read):
//   - Station "n1" ($1 in / $2 out per 1M) and "n2" ($2 / $4) stand behind the Background; a
//     relay body is padded so the broker's own estimate (estimateMaxCost) is exactly $0.003 on
//     n1 and $0.006 on n2 with max_tokens 1000 - the Background asserts that, so the numbers
//     in the scenarios are the broker's, not ours.
//   - "Spent $X this window" is real spend through the key on the spender station (see the
//     shared harness); a settle of an exact amount is a scripted claim on n1 (prompt 0,
//     completion c at $2/1M -> c*2e-6).
//   - "The key window" is read from GET /account/keys/<id> as limit_usd - limit_remaining.
//   - The harness rate limiter is unlimited unless a scenario sets it (foState.newBroker), so
//     concurrency scenarios measure the key reserve, not the per-identity limiter; the
//     stolen-key scenario sets the production 120 rpm / burst 40 explicitly.
//   - "No key lookup on a non-key request" compares the shared-store work of a signed relay by
//     an account that holds keys against one by an account that holds none.

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// ---- helpers ------------------------------------------------------------------------------------

func (k *kg5State) kl5Window(label string) (spent, limit float64, err error) {
	e, err := k.entry(label)
	if err != nil {
		return 0, 0, err
	}
	limit, _ = kg5Num(e["limit_usd"])
	rem, err := k.fieldNum(e, "limit_remaining")
	if err != nil {
		return 0, 0, err
	}
	return limit - rem, limit, nil
}

// kl5Cost scripts station name to claim exactly cost at its out price (prompt 0).
func (k *kg5State) kl5Cost(name string, cost float64) {
	st := k.st(name)
	c := int(math.Round(cost * 1e6 / st.priceOut))
	k.kl5Claim(name, 0, c)
}

func (k *kg5State) kl5Claim(name string, p, c int) {
	st := k.st(name)
	k.scripted[name] = true
	st.set(func(_ int, w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		if bytes.Contains(buf.Bytes(), []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", sr3Marker+" from "+name)
			fmt.Fprint(w, sr3Usage(p, c)+"\n\n")
			fmt.Fprint(w, sr3Done+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, sr3Marker+" from "+name, p, c)
	})
}

func (k *kg5State) kl5Status(name string, code int) {
	st := k.st(name)
	k.scripted[name] = true
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(utDefaultBody(code)))
	})
}

// kl5Pinned fires a relay bearing label pinned to station name (stream optional).
func (k *kg5State) kl5Pinned(label, name string, stream bool) error {
	return k.keyRelay(label, stream, nil, map[string]string{"X-Roger-Node": k.st(name).id})
}

// kl5HoldOf names the station whose hold is amount (n1 $0.003, n2 $0.006).
func (k *kg5State) kl5HoldOf(amount float64) string {
	for _, n := range []string{"n1", "n2"} {
		if st, ok := k.stations[n]; ok {
			if kg5Near(estimateMaxCost(k.relayBody(k.model, false, nil), st.priceIn, st.priceOut, 32768), amount) {
				return n
			}
		}
	}
	return "n1"
}

func (k *kg5State) kl5Holds() (holds, releases int, err error) {
	wallet, err := k.walletOf("acct-a")
	if err != nil {
		return 0, 0, err
	}
	rows, err := k.db.LedgerOf(wallet, []string{store.KindHold, store.KindHoldRelease}, 5000)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		if r.Kind == store.KindHold {
			holds++
		} else {
			releases++
		}
	}
	return
}

func (k *kg5State) kl5HoldFor(reqID string) (float64, error) {
	wallet, _ := k.walletOf("acct-a")
	rows, err := k.db.LedgerOf(wallet, []string{store.KindHold}, 5000)
	if err != nil {
		return 0, err
	}
	sum := 0.0
	for _, r := range rows {
		if reqID != "" && strings.HasPrefix(r.Ref, reqID) {
			sum += -r.Amount
		}
	}
	return sum, nil
}

// kl5Usage is the usage chunk's object of a streamed response.
func kl5Usage(body []byte) (map[string]any, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var last map[string]any
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m) == nil {
			if u, ok := m["usage"].(map[string]any); ok {
				last = u
			}
		}
	}
	if last == nil {
		return nil, fmt.Errorf("the stream carries no usage chunk: %.400s", body)
	}
	return last, nil
}

func (k *kg5State) kl5ModServer() {
	if k.modSrv != nil {
		return
	}
	k.modSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		k.mailMu.Lock()
		k.mails = append(k.mails, "moderation@"+strconv.FormatInt(time.Now().UnixNano(), 10))
		k.mailMu.Unlock()
		if bytes.Contains(b, []byte("kl5-illegal")) {
			_, _ = w.Write([]byte(`{"results":[{"flagged":true,"categories":{"S1":true}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
	k.b.mod = moderation{provider: "url", url: k.modSrv.URL, client: k.modSrv.Client(), csamCats: loadCSAMCategories(""), mode: modeSync}
}

// kl5SpendSigned settles amount on acct's wallet through a SIGNED (non-key) relay.
func (k *kg5State) kl5SpendSigned(acct string, amount float64) error {
	k.spender()
	k.spendTok = int(math.Round(amount * 1e4))
	body, _ := json.Marshal(map[string]any{"model": "kg5-spend", "messages": []map[string]string{{"role": "user", "content": "spend"}}, "max_tokens": k.spendTok})
	res, err := k.relayRaw(k.b, "", body, map[string]string{"sign": acct})
	if err != nil {
		return err
	}
	if res.code != 200 {
		return fmt.Errorf("a signed spend of $%.6f by %s = %d: %.300s", amount, acct, res.code, res.body)
	}
	return nil
}

func (k *kg5State) kl5SetBalance(acct string, bal float64) error {
	wallet, err := k.walletOf(acct)
	if err != nil {
		return err
	}
	cur, err := k.db.PeekBalance(wallet)
	if err != nil {
		return err
	}
	if d := bal - cur; math.Abs(d) > 1e-12 {
		if _, err := k.db.AddCredits(wallet, d); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) kl5Upstream(name string) int {
	st := k.st(name)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reqN
}

func (k *kg5State) kl5Hdr(h string) string { return k.resp.hdr.Get(h) }

func (k *kg5State) kl5HdrNum(h string, want float64) error {
	v := k.kl5Hdr(h)
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || !kg5Near(f, want) {
		return fmt.Errorf("%s = %q, want %v", h, v, want)
	}
	return nil
}

// kl5Name replaces scenario station names in s with the ids they run as.
func (k *kg5State) kl5Name(s string) string {
	for n, st := range k.stations {
		s = strings.ReplaceAll(s, " "+n, " "+st.id)
	}
	return s
}

// ---- background ----------------------------------------------------------------------------------

func (k *kg5State) kl5Fee10() error { k.b.feeRate = 0.10; return nil }

func (k *kg5State) kl5NoCap(label, alias string, bal float64) error {
	if err := k.account(label, alias, bal); err != nil {
		return err
	}
	wallet, _ := k.walletOf(label)
	return k.db.SetMonthlyCap(wallet, 0)
}

func (k *kg5State) kl5Node(name, model string, in, out float64) error {
	k.ensureStation(name, model, in, out)
	return nil
}

func (k *kg5State) kl5BackgroundHolds(model string, maxTok int, h1 float64, n1 string, h2 float64, n2 string) error {
	k.model, k.maxTokens, k.promptEst = model, maxTok, 1000
	body := k.relayBody(model, false, nil)
	for _, c := range []struct {
		n string
		h float64
	}{{n1, h1}, {n2, h2}} {
		st := k.st(c.n)
		if got := estimateMaxCost(body, st.priceIn, st.priceOut, 32768); !kg5Near(got, c.h) {
			return fmt.Errorf("the broker's hold estimate on %s is $%.6f, the Background says $%.3f", c.n, got, c.h)
		}
	}
	return nil
}

// ---- gate position --------------------------------------------------------------------------------

func (k *kg5State) kl5SyncMod() error { k.kl5ModServer(); return nil }

func (k *kg5State) kl5OrderObserved() error {
	if err := k.status(200); err != nil {
		return err
	}
	if len(k.mails) == 0 || !strings.HasPrefix(k.mails[0], "moderation@") {
		return fmt.Errorf("the sync screen never ran before dispatch")
	}
	h, err := k.kl5HoldFor(k.resp.reqID)
	if err != nil {
		return err
	}
	if h <= 0 {
		return fmt.Errorf("no hold was placed for request %s", k.resp.reqID)
	}
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if c, _ := strconv.ParseFloat(k.kl5Hdr("X-RogerAI-Cost"), 64); !kg5Near(spent, c) {
		return fmt.Errorf("key window $%v after a $%v relay: the key-limit gate did not capture it", spent, c)
	}
	return nil
}

func (k *kg5State) kl5Illegal451() error {
	h0, _, err := k.kl5Holds()
	if err != nil {
		return err
	}
	if err := k.keyRelay("k1", false, map[string]any{"messages": []map[string]string{{"role": "user", "content": "kl5-illegal"}}}, nil); err != nil {
		return err
	}
	if err := k.status(451); err != nil {
		return err
	}
	h1, _, _ := k.kl5Holds()
	if h1 != h0 {
		return fmt.Errorf("the 451 placed %d holds", h1-h0)
	}
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if c, _ := strconv.ParseFloat(k.resps[len(k.resps)-2].hdr.Get("X-RogerAI-Cost"), 64); !kg5Near(spent, c) {
		return fmt.Errorf("key window $%v moved on the 451 (expected only the earlier $%v)", spent, c)
	}
	return nil
}

func (k *kg5State) kl5HasSpent(label string, amt float64) error {
	if err := k.spend(label, amt); err != nil {
		return err
	}
	k.snap(label)
	return nil
}

func (k *kg5State) kl5BeforeHold(bal float64) error {
	if err := k.is402Source("key_limit"); err != nil {
		return err
	}
	if h, _ := k.kl5HoldFor(k.resp.reqID); h != 0 {
		return fmt.Errorf("the refused relay placed a $%v hold", h)
	}
	wallet, _ := k.walletOf("acct-a")
	got, err := k.db.PeekBalance(wallet)
	if err != nil {
		return err
	}
	if !kg5Near(got, bal) {
		return fmt.Errorf("balance $%v, want $%v", got, bal)
	}
	return nil
}

func (k *kg5State) kl5FreeServed() error {
	if err := k.status(200); err != nil {
		return err
	}
	if v := k.kl5Hdr("X-RogerAI-Cost"); v != "0" {
		return fmt.Errorf("X-RogerAI-Cost %q, want 0", v)
	}
	return nil
}

func (k *kg5State) kl5UsageStaysCount(label string, amt float64) error {
	before := k.snaps[label]
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	if u, _ := kg5Num(e["usage"]); !kg5Near(u, amt) {
		return fmt.Errorf("usage %v, want %v", e["usage"], amt)
	}
	rb, _ := kg5Num(before["requests"])
	ra, ok := kg5Num(e["requests"])
	if !ok || ra != rb+1 {
		return fmt.Errorf("requests %v -> %v, want an increment", before["requests"], e["requests"])
	}
	return nil
}

func (k *kg5State) kl5EveryStationFails(_ string) error {
	for n := range k.stations {
		k.kl5Status(n, 502)
	}
	return nil
}

func (k *kg5State) kl5RelayReturnsError() error {
	if err := k.keyRelay("k1", false, nil, nil); err != nil {
		return err
	}
	if k.resp.code == 200 {
		return fmt.Errorf("the relay was served")
	}
	return nil
}

func (k *kg5State) kl5ReleasedUnchanged(label string) error {
	h, r, err := k.kl5Holds()
	if err != nil {
		return err
	}
	if h != r {
		return fmt.Errorf("%d holds, %d releases: a hold is dangling", h, r)
	}
	spent, _, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	if spent != 0 {
		return fmt.Errorf("%s window $%v after a relay that never served", label, spent)
	}
	return nil
}

// ---- ceiling ----------------------------------------------------------------------------------

func (k *kg5State) kl5RelayHolding(label string, amt float64) error {
	return k.kl5Pinned(label, k.kl5HoldOf(amt), false)
}

func (k *kg5State) kl5InPractice(cost float64) error {
	k.kl5Cost("n1", cost)
	return nil
}

func (k *kg5State) kl5ArrivesHolding(amt float64) error { return k.kl5RelayHolding("k1", amt) }

func (k *kg5State) kl5SpentRemaining(label string, amt, _ float64) error {
	return k.kl5HasSpent(label, amt)
}

func (k *kg5State) kl5PlansN1N2(label string) error {
	return k.keyRelay(label, false, map[string]any{"provider": map[string]any{"order": []string{k.st("n1").id, k.st("n2").id}}}, nil)
}

func (k *kg5State) kl5HoldCoversN1() error {
	if err := k.status(200); err != nil {
		return err
	}
	if got := k.kl5Hdr("X-RogerAI-Provider"); got != k.st("n1").id {
		return fmt.Errorf("served by %q, want n1", got)
	}
	h, err := k.kl5HoldFor(k.resp.reqID)
	if err != nil {
		return err
	}
	if !kg5Near(h, 0.003) {
		return fmt.Errorf("hold $%v, want n1's $0.003 alone (n2 trimmed)", h)
	}
	return nil
}

func (k *kg5State) kl5NoCeilingNotice() error {
	if v := k.kl5Hdr("X-RogerAI-Key-Notice"); strings.Contains(v, "reached") {
		return fmt.Errorf("the ceiling probe raised an at-limit notice %q", v)
	}
	for _, m := range k.mails {
		if strings.Contains(m, k.keyID("k1")) {
			return fmt.Errorf("the ceiling probe sent a key email")
		}
	}
	return nil
}

func (k *kg5State) kl5HasRemaining(label string, rem float64) error {
	_, limit, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	return k.kl5HasSpent(label, limit-rem)
}

func (k *kg5State) kl5CheapestHolds(label string, amt float64) error {
	return k.kl5RelayHolding(label, amt)
}

func (k *kg5State) kl5Is402() error { return k.is402Source("key_limit") }

func (k *kg5State) kl5CapAndBalance(acct string, capRem, bal float64) error {
	wallet, err := k.walletOf(acct)
	if err != nil {
		return err
	}
	ms, err := k.db.MonthSpendOf(wallet, k.now())
	if err != nil {
		return err
	}
	if err := k.db.SetMonthlyCap(wallet, ms+capRem); err != nil {
		return err
	}
	return k.kl5SetBalance(acct, bal)
}

func (k *kg5State) kl5Outcome(outcome, source string) error {
	if outcome == "served" {
		return k.status(200)
	}
	return k.is402Source(source)
}

func (k *kg5State) kl5TenThousand(label string) error {
	k.codes = nil
	k.kl5Cost("n1", 0.0001)
	for i := 0; i < 10000; i++ {
		res, err := k.relayRaw(k.b, label, k.relayBody(k.model, false, nil), map[string]string{"X-Roger-Node": k.st("n1").id})
		if err != nil {
			return err
		}
		k.codes = append(k.codes, res.code)
		if res.code == 402 && strings.Contains(string(res.body), "key_limit") {
			k.resp = res
			return nil
		}
	}
	return nil
}

func (k *kg5State) kl5NoKeyLimit402() error {
	for _, c := range k.codes {
		if c == 402 && strings.Contains(string(k.resp.body), `"key_limit"`) {
			return fmt.Errorf("an unlimited key was refused for key_limit: %.300s", k.resp.body)
		}
	}
	if len(k.codes) != 10000 {
		return fmt.Errorf("%d relays fired, want 10000", len(k.codes))
	}
	return nil
}

func (k *kg5State) kl5MonthlyCapSet(acct string, cap, spent float64) error {
	if err := k.kl5SpendSigned(acct, spent); err != nil {
		return err
	}
	wallet, _ := k.walletOf(acct)
	return k.db.SetMonthlyCap(wallet, cap)
}

func (k *kg5State) kl5RelayRemaining(label string, _ float64) error { return k.pricedRelay(label) }

func (k *kg5State) kl5MonthlyExisting() error {
	if err := k.is402Source("monthly_cap"); err != nil {
		return err
	}
	if !strings.HasPrefix(k.errMsg(), "monthly spend limit reached: ") {
		return fmt.Errorf("message %q is not the existing monthly message", k.errMsg())
	}
	return nil
}

// ---- 402 body -------------------------------------------------------------------------------------

func (k *kg5State) kl5RefusedBecause(source string) error {
	k.at(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	switch source {
	case "key_limit":
		if err := k.spend("k1", 5); err != nil {
			return err
		}
	case "monthly_cap":
		if err := k.kl5SetBalance("acct-a", 120); err != nil {
			return err
		}
		if err := k.kl5SpendSigned("acct-a", 50); err != nil {
			return err
		}
		wallet, _ := k.walletOf("acct-a")
		if err := k.db.SetMonthlyCap(wallet, 50); err != nil {
			return err
		}
	case "credits":
		if err := k.kl5SetBalance("acct-a", 0.001); err != nil {
			return err
		}
	}
	return k.pricedRelay("k1")
}

func (k *kg5State) kl5BodyIs(want string) error {
	want = strings.ReplaceAll(want, "key_x", k.keyID("k1"))
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		return fmt.Errorf("the expected body is not JSON: %v", err)
	}
	if err := json.Unmarshal(k.resp.body, &g); err != nil {
		return fmt.Errorf("the 402 body is not JSON: %.300s", k.resp.body)
	}
	wj, _ := json.Marshal(w)
	gj, _ := json.Marshal(g)
	if string(wj) != string(gj) {
		return fmt.Errorf("body %s, want %s", gj, wj)
	}
	return nil
}

func (k *kg5State) kl5SignedMonthly() error {
	if err := k.kl5SetBalance("acct-a", 60); err != nil {
		return err
	}
	if err := k.kl5SpendSigned("acct-a", 10); err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	if err := k.db.SetMonthlyCap(wallet, 10); err != nil {
		return err
	}
	k.ensurePriced()
	return k.keyRelay("", false, nil, map[string]string{"sign": "acct-a"})
}

func (k *kg5State) kl5TodaysMessage(tmpl string) error {
	if err := k.status(402); err != nil {
		return err
	}
	parts := strings.Split(tmpl, "$X of $Y")
	if len(parts) != 2 {
		return fmt.Errorf("template %q", tmpl)
	}
	msg := k.errMsg()
	if !strings.HasPrefix(msg, parts[0]+"$") || !strings.HasSuffix(msg, parts[1]) {
		return fmt.Errorf("message %q is not today's %q", msg, tmpl)
	}
	return nil
}

func (k *kg5State) kl5AdditionallyCarries() error {
	if err := k.statusCode(402, "monthly_cap_reached"); err != nil {
		return err
	}
	return k.is402Source("monthly_cap")
}

func (k *kg5State) kl5NoneMessage(msg string) error {
	if err := k.status(402); err != nil {
		return err
	}
	if k.errMsg() != msg {
		return fmt.Errorf("message %q, want %q", k.errMsg(), msg)
	}
	return nil
}

func (k *kg5State) kl5Relay402(label string) error {
	if err := k.spend(label, 5); err != nil {
		return err
	}
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.is402Source("key_limit")
}

func (k *kg5State) kl5NoReceipt() error {
	if v := k.kl5Hdr("X-RogerAI-Cost"); v != "0" {
		return fmt.Errorf("X-RogerAI-Cost %q, want 0", v)
	}
	for _, h := range []string{"X-RogerAI-Receipt", "X-RogerAI-Provider"} {
		if k.kl5Hdr(h) != "" {
			return fmt.Errorf("the 402 carries %s", h)
		}
	}
	return nil
}

func (k *kg5State) kl5Refused(label string) error { return k.pricedRelay(label) }

func (k *kg5State) kl5KeyHeadersNotice(limit, spend float64, pct int, notice string) error {
	if err := k.kl5HdrNum("X-RogerAI-Key-Limit", limit); err != nil {
		return err
	}
	if err := k.kl5HdrNum("X-RogerAI-Key-Spend", spend); err != nil {
		return err
	}
	if err := k.kl5HdrNum("X-RogerAI-Key-Pct", float64(pct)); err != nil {
		return err
	}
	if got := k.kl5Hdr("X-RogerAI-Key-Notice"); got != notice {
		return fmt.Errorf("X-RogerAI-Key-Notice %q, want %q", got, notice)
	}
	return nil
}

func (k *kg5State) kl5Served(label string) error {
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5KeyHeadersNoNotice(limit, spend float64, pct int) error {
	if err := k.kl5HdrNum("X-RogerAI-Key-Limit", limit); err != nil {
		return err
	}
	if err := k.kl5HdrNum("X-RogerAI-Key-Spend", spend); err != nil {
		return err
	}
	if err := k.kl5HdrNum("X-RogerAI-Key-Pct", float64(pct)); err != nil {
		return err
	}
	if v := k.kl5Hdr("X-RogerAI-Key-Notice"); v != "" {
		return fmt.Errorf("X-RogerAI-Key-Notice %q, want absent", v)
	}
	return nil
}

func (k *kg5State) kl5NoticeIs(notice string) error {
	if got := k.kl5Hdr("X-RogerAI-Key-Notice"); got != notice {
		return fmt.Errorf("X-RogerAI-Key-Notice %q, want %q", got, notice)
	}
	return nil
}

func (k *kg5State) kl5NoticePresence(p string) error {
	v := k.kl5Hdr("X-RogerAI-Key-Notice")
	if (p == "present") != (v != "") {
		return fmt.Errorf("X-RogerAI-Key-Notice %q, want %s", v, p)
	}
	return nil
}

func (k *kg5State) kl5KeyLimit0(label string) error {
	return k.mintWith("acct-a", label, "limit_usd 0")
}

func (k *kg5State) kl5NoKeyHeaders() error {
	for h := range k.resp.hdr {
		if strings.HasPrefix(http.CanonicalHeaderKey(h), "X-Rogerai-Key-") {
			return fmt.Errorf("header %s is set", h)
		}
	}
	return nil
}

func (k *kg5State) kl5MonthlyAndKey(acct string, cap, spent float64, label string, keySpent float64) error {
	if err := k.kl5SetBalance(acct, 100); err != nil {
		return err
	}
	if err := k.kl5SpendSigned(acct, spent-keySpent); err != nil {
		return err
	}
	if err := k.spend(label, keySpent); err != nil {
		return err
	}
	wallet, _ := k.walletOf(acct)
	return k.db.SetMonthlyCap(wallet, cap)
}

func (k *kg5State) kl5MonthlyPctKeyPct(mp, kp int) error {
	if err := k.kl5HdrNum("X-RogerAI-Monthly-Pct", float64(mp)); err != nil {
		return err
	}
	if k.kl5Hdr("X-RogerAI-Monthly-Notice") == "" {
		return fmt.Errorf("no monthly notice at %d%%", mp)
	}
	if err := k.kl5HdrNum("X-RogerAI-Key-Pct", float64(kp)); err != nil {
		return err
	}
	if v := k.kl5Hdr("X-RogerAI-Key-Notice"); v != "" {
		return fmt.Errorf("a key notice %q at %d%%", v, kp)
	}
	return nil
}

func (k *kg5State) kl5StreamServed(label string) error {
	k.kl5Claim("n1", 40, 20)
	if err := k.kl5Pinned(label, "n1", true); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5HeadersBeforeFrame() error {
	for _, h := range []string{"X-RogerAI-Key-Limit", "X-RogerAI-Key-Spend", "X-RogerAI-Key-Pct"} {
		if k.resp.snap.Get(h) == "" {
			return fmt.Errorf("%s was not committed with the first frame (headers then: %v)", h, k.resp.snap)
		}
	}
	return nil
}

func (k *kg5State) kl5SpendBefore() error {
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	u, err := kl5Usage(k.resp.body)
	if err != nil {
		return err
	}
	cost, _ := kg5Num(u["cost"])
	before := spent - cost
	return func() error {
		v, _ := strconv.ParseFloat(k.resp.snap.Get("X-RogerAI-Key-Spend"), 64)
		if !kg5Near(v, before) {
			return fmt.Errorf("X-RogerAI-Key-Spend %v, want the window spend before this request %v", v, before)
		}
		return nil
	}()
}

func (k *kg5State) kl5StreamSettles(label string, cost, before float64) error {
	k.at(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if err := k.spend(label, before); err != nil {
		return err
	}
	k.kl5Cost("n1", cost)
	if err := k.kl5Pinned(label, "n1", true); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5ChunkKeyState(label string, limit, spend float64, pct int, reset string) error {
	u, err := kl5Usage(k.resp.body)
	if err != nil {
		return err
	}
	ro, ok := u["rogerai"].(map[string]any)
	if !ok {
		return fmt.Errorf("the usage chunk has no rogerai object: %v", u)
	}
	if ro["key_id"] != k.keyID(label) {
		return fmt.Errorf("key_id %v, want %s", ro["key_id"], k.keyID(label))
	}
	for f, want := range map[string]float64{"key_limit": limit, "key_spend": spend, "key_pct": float64(pct)} {
		if got, _ := kg5Num(ro[f]); !kg5Near(got, want) {
			return fmt.Errorf("%s %v, want %v", f, ro[f], want)
		}
	}
	if ro["key_reset_at"] != reset {
		return fmt.Errorf("key_reset_at %v, want %q", ro["key_reset_at"], reset)
	}
	return nil
}

func (k *kg5State) kl5NonStreamSettles(label string, cost float64) error {
	k.kl5Cost("n1", cost)
	if err := k.kl5Pinned(label, "n1", false); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5SpendHeaderGate(h, after float64) error {
	if err := k.kl5HdrNum("X-RogerAI-Key-Spend", h); err != nil {
		return err
	}
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if !kg5Near(spent, after) {
		return fmt.Errorf("/account/keys shows $%v afterwards, want $%v", spent, after)
	}
	return nil
}

func (k *kg5State) kl5SignedServed() error {
	k.ensurePriced()
	if err := k.keyRelay("", false, nil, map[string]string{"sign": "acct-a"}); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5HeadersClean() error {
	if k.resp.code == 0 {
		if err := k.pricedRelay("k1"); err != nil {
			return err
		}
	}
	found := false
	for h, vs := range k.resp.hdr {
		if !strings.HasPrefix(http.CanonicalHeaderKey(h), "X-Rogerai-Key-") {
			continue
		}
		found = true
		for _, v := range vs {
			if strings.Contains(v, "rog-key_") || strings.Contains(v, "u_gh_") {
				return fmt.Errorf("%s = %q carries a secret or wallet id", h, v)
			}
			if _, err := strconv.ParseFloat(v, 64); err != nil && !strings.Contains(v, "key limit") && !strings.Contains(v, "of your") {
				return fmt.Errorf("%s = %q is neither a number nor the notice sentence", h, v)
			}
		}
	}
	if !found {
		return fmt.Errorf("no X-RogerAI-Key-* header was set to inspect")
	}
	return nil
}

// ---- settle and recount ----------------------------------------------------------------------------

func (k *kg5State) kl5HoldsSettles(label string, hold, cost float64) error {
	k.kl5Cost(k.kl5HoldOf(hold), cost)
	if err := k.kl5RelayHolding(label, hold); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5WindowRemainder(label string, want, remainder float64) error {
	spent, _, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	if !kg5Near(spent, want) {
		return fmt.Errorf("%s window $%v, want $%v", label, spent, want)
	}
	h, err := k.kl5HoldFor(k.resp.reqID)
	if err != nil {
		return err
	}
	c, _ := strconv.ParseFloat(k.kl5Hdr("X-RogerAI-Cost"), 64)
	if !kg5Near(h-c, remainder) {
		return fmt.Errorf("hold $%v - cost $%v = $%v released, want $%v", h, c, h-c, remainder)
	}
	return nil
}

func (k *kg5State) kl5Claims10x() error {
	k.kl5Claim("n1", 0, 10*k.maxTokens)
	return nil
}

func (k *kg5State) kl5RelaySettles(label string) error {
	return k.kl5Pinned(label, "n1", false)
}

func (k *kg5State) kl5CappedAtHold() error {
	if err := k.status(200); err != nil {
		return err
	}
	c, _ := strconv.ParseFloat(k.kl5Hdr("X-RogerAI-Cost"), 64)
	if c > 0.003+1e-9 {
		return fmt.Errorf("cost $%v exceeds the $0.003 hold", c)
	}
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if spent > 0.003+1e-9 {
		return fmt.Errorf("key window rose $%v, above the hold", spent)
	}
	return nil
}

func (k *kg5State) kl5RecountDown() error {
	k.kl5Claim("n1", 40, 1000)
	k.recCompletion = 500
	return nil
}

func (k *kg5State) kl5RelaySettlesAny() error { return k.kl5Pinned("k1", "n1", false) }

func (k *kg5State) kl5RecountedOnly() error {
	if err := k.status(200); err != nil {
		return err
	}
	c, _ := strconv.ParseFloat(k.kl5Hdr("X-RogerAI-Cost"), 64)
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if !kg5Near(spent, c) || c > 0.003 {
		return fmt.Errorf("key window $%v, cost $%v (hold $0.003)", spent, c)
	}
	if c >= 1000*2e-6 {
		return fmt.Errorf("cost $%v is the claim, not the recount", c)
	}
	return nil
}

func (k *kg5State) kl5EmptyThenFail() error {
	st := k.st("n1")
	k.scripted["n1"] = true
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":40,"completion_tokens":0}}`))
	})
	k.kl5Status("n2", 502)
	return nil
}

func (k *kg5State) kl5RelayEnds() error { return k.keyRelay("k1", false, nil, nil) }

func (k *kg5State) kl5BothReleased(label string) error { return k.kl5ReleasedUnchanged(label) }

func (k *kg5State) kl5RemainingPlan(label string, rem float64) error {
	return k.kl5HasRemaining(label, rem)
}

func (k *kg5State) kl5N1429N2Serves(cost float64) error {
	k.kl5Status("n1", 429)
	k.kl5Cost("n2", cost)
	return k.kl5PlansN1N2("k1")
}

func (k *kg5State) kl5RisesRest(rise, reserve float64) error {
	if err := k.status(200); err != nil {
		return err
	}
	before, _ := kg5Num(k.snaps["k1"]["limit_remaining"])
	_, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	e, err := k.entry("k1")
	if err != nil {
		return err
	}
	after, _ := kg5Num(e["limit_remaining"])
	if !kg5Near(before-after, rise) {
		return fmt.Errorf("key spend rose $%v, want $%v", before-after, rise)
	}
	h, _ := k.kl5HoldFor(k.resp.reqID)
	if !kg5Near(h, reserve) {
		return fmt.Errorf("the plan held $%v, want the $%v ceiling", h, reserve)
	}
	return nil
}

func (k *kg5State) kl5ListsTwo(_ string) error {
	k.ensureStation("kl5-llama", "llama-3.3-70b", 1, 2)
	return nil
}

func (k *kg5State) kl5FirstNoneSecondServes() error {
	k.b.mu.Lock()
	for _, n := range []string{"n1", "n2"} {
		delete(k.b.nodes, k.st(n).id)
	}
	k.b.mu.Unlock()
	return k.keyRelayOn(k.b, "k1", "qwen3-32b", false, map[string]any{"models": []string{"llama-3.3-70b"}}, nil)
}

func (k *kg5State) kl5RisesOnce() error {
	if err := k.status(200); err != nil {
		return err
	}
	c, _ := strconv.ParseFloat(k.kl5Hdr("X-RogerAI-Cost"), 64)
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if !kg5Near(spent, c) {
		return fmt.Errorf("key window $%v, the served pair cost $%v", spent, c)
	}
	return nil
}

// ---- concurrency ------------------------------------------------------------------------------------

func (k *kg5State) kl5Concurrent(b []*broker, label string, n int) ([]kg5Resp, error) {
	body := k.relayBody(k.model, false, nil)
	pin := map[string]string{"X-Roger-Node": k.st("n1").id}
	start := make(chan struct{})
	out := make([]kg5Resp, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i], errs[i] = k.relayRaw(b[i%len(b)], label, body, pin)
		}(i)
	}
	close(start)
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

func (k *kg5State) kl5TwoRacing(label string, _ float64) error {
	res, err := k.kl5Concurrent([]*broker{k.b}, label, 2)
	k.resps = res
	return err
}

func (k *kg5State) kl5OneEach() error {
	served, refused := 0, 0
	for _, r := range k.resps {
		switch {
		case r.code == 200:
			served++
		case r.code == 402 && strings.Contains(string(r.body), `"key_limit"`):
			refused++
		default:
			return fmt.Errorf("a racing relay answered %d: %.300s", r.code, r.body)
		}
	}
	if served != 1 || refused != 1 {
		return fmt.Errorf("%d served, %d refused for key_limit; want exactly one each", served, refused)
	}
	return nil
}

func (k *kg5State) kl5AtMost(v float64) error {
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if spent > v+1e-9 {
		return fmt.Errorf("window spend $%v exceeds $%v", spent, v)
	}
	return nil
}

func (k *kg5State) kl5NConcurrent(n int, label string, _ float64) error {
	res, err := k.kl5Concurrent([]*broker{k.b}, label, n)
	k.resps = res
	return err
}

func (k *kg5State) kl5ExactlyPass(pass, refused int) error {
	p, r := 0, 0
	for _, x := range k.resps {
		switch {
		case x.code == 200:
			p++
		case x.code == 402 && strings.Contains(string(x.body), `"key_limit"`):
			r++
		}
	}
	if p != pass || (refused >= 0 && r != refused) {
		return fmt.Errorf("%d passed, %d refused for key_limit; want %d and %d", p, r, pass, refused)
	}
	return nil
}

func (k *kg5State) kl5TwoInstancesRemaining(label string, rem float64) error {
	k.instanceB()
	return k.kl5HasRemaining(label, rem)
}

func (k *kg5State) kl5SplitInstances(na, nb int, _ float64) error {
	if k.b2 == nil {
		k.instanceB()
	}
	for id, n := range k.b.nodes {
		k.b2.nodes[id] = n
		k.b2.tunnels[id] = k.b.tunnels[id]
		k.b2.trust[id] = k.b.trust[id]
	}
	brokers := make([]*broker, 0, na+nb)
	for i := 0; i < na+nb; i++ {
		if i%2 == 0 && i/2 < na || i/2 >= nb {
			brokers = append(brokers, k.b)
		} else {
			brokers = append(brokers, k.b2)
		}
	}
	res, err := k.kl5Concurrent(brokers, "k1", na+nb)
	k.resps = res
	return err
}

func (k *kg5State) kl5ExactlyTotal(n int) error { return k.kl5ExactlyPass(n, -1) }

func (k *kg5State) kl5ReserveThenHoldFails() error { return k.kl5SetBalance("acct-a", 0.002) }

func (k *kg5State) kl5Returns402Credits() error {
	if err := k.kl5RelayHolding("k1", 0.003); err != nil {
		return err
	}
	return k.is402Source("credits")
}

func (k *kg5State) kl5NoDangling(label string) error {
	spent, limit, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	if spent != 0 {
		return fmt.Errorf("%s shows $%v reserved/spent of $%v after a refused relay", label, spent, limit)
	}
	return nil
}

func (k *kg5State) kl5Killed(label string, _ float64) error {
	return k.heldRelay(label, false)
}

func (k *kg5State) kl5Sweep() error {
	_, err := k.db.ReleaseStaleHolds(time.Now().Add(time.Hour))
	return err
}

func (k *kg5State) kl5ReleasedByRequest() error {
	h, r, err := k.kl5Holds()
	if err != nil {
		return err
	}
	if h != r {
		return fmt.Errorf("%d holds, %d releases after the sweep", h, r)
	}
	return k.kl5NoDangling("k1")
}

func (k *kg5State) kl5StoreDownAtGate(label string) error {
	if err := k.pricedRelay(label); err != nil {
		return err
	}
	if err := k.status(200); err != nil {
		return fmt.Errorf("warming the key: %w", err)
	}
	k.mr.Close()
	k.mrClosed = true
	return k.pricedRelay(label)
}

func (k *kg5State) kl5Is503(msg string) error { return k.statusMsg(503, msg) }

func (k *kg5State) kl5NoHoldLeft() error {
	h, r, err := k.kl5Holds()
	if err != nil {
		return err
	}
	if h != r {
		return fmt.Errorf("%d holds, %d releases: the refused relay left a hold", h, r)
	}
	return nil
}

func (k *kg5State) kl5NotBypassed() error {
	if k.resp.code == 200 {
		return fmt.Errorf("the relay was served with the key reserve unavailable")
	}
	return nil
}

func (k *kg5State) kl5ABSettled(a, b float64, label string) error {
	k.instanceB()
	if err := k.spendOn(k.b, label, a); err != nil {
		return err
	}
	return k.spendOn(k.b2, label, b)
}

func (k *kg5State) kl5RemainingBoth(rem float64) error {
	for _, b := range []*broker{k.b, k.b2} {
		e, err := k.entryOn(b, "k1")
		if err != nil {
			return err
		}
		got, err := k.fieldNum(e, "limit_remaining")
		if err != nil {
			return err
		}
		if !kg5Near(got, rem) {
			return fmt.Errorf("an instance reads remaining $%v, want $%v", got, rem)
		}
	}
	return nil
}

// ---- window edges -------------------------------------------------------------------------------------

func (k *kg5State) kl5DailySpentToday(label string, amt float64) error {
	k.at(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if err := k.patchReset("acct-a", label, "daily"); err != nil {
		return err
	}
	if err := k.status(200); err != nil {
		return err
	}
	return k.spend(label, amt)
}

func (k *kg5State) kl5At235959Is402() error {
	if err := k.pricedAt("k1", "2026-10-15T23:59:59Z"); err != nil {
		return err
	}
	return k.is402Source("key_limit")
}

func (k *kg5State) kl5AtMidnightServed() error {
	if err := k.pricedAt("k1", "2026-10-16T00:00:00Z"); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5StreamAcrossMidnight(label string, before, cost float64) error {
	k.at(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if err := k.patchReset("acct-a", label, "daily"); err != nil {
		return err
	}
	if err := k.spend(label, before); err != nil {
		return err
	}
	k.at(time.Date(2026, 10, 15, 23, 59, 58, 0, time.UTC))
	k.heldP, k.heldC = 0, int(math.Round(cost*1e6/2))
	if err := k.heldRelay(label, true); err != nil {
		return err
	}
	k.at(time.Date(2026, 10, 16, 0, 0, 3, 0, time.UTC))
	res, err := k.release()
	if err != nil {
		return err
	}
	if res.code != 200 {
		return fmt.Errorf("the stream across midnight ended %d: %.300s", res.code, res.body)
	}
	return nil
}

func (k *kg5State) kl5AttributedToStart() error {
	if err := k.wantNum("k1", "limit_remaining", 5); err != nil {
		return fmt.Errorf("today's window: %w", err)
	}
	k.at(time.Date(2026, 10, 15, 23, 59, 59, 0, time.UTC))
	if err := k.wantNum("k1", "limit_remaining", 0); err != nil {
		return fmt.Errorf("yesterday's window: %w", err)
	}
	return nil
}

func (k *kg5State) kl5HitOn10th(label string) error {
	k.at(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	return k.spend(label, 5)
}

func (k *kg5State) kl5ResetDailyOn10th() error {
	k.at(time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC))
	if err := k.patchReset("acct-a", "k1", "daily"); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5NewAnchor() error {
	if err := k.pricedRelay("k1"); err != nil {
		return err
	}
	if err := k.status(200); err != nil {
		return err
	}
	e, err := k.entry("k1")
	if err != nil {
		return err
	}
	if u, _ := kg5Num(e["usage"]); u < 5 {
		return fmt.Errorf("lifetime usage %v, want the earlier $5 to stay", e["usage"])
	}
	return nil
}

func (k *kg5State) kl5NonePriorMonth(label string) error {
	k.at(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	if err := k.mintWith("acct-a", label, `limit_usd 1, reset "none"`); err != nil {
		return err
	}
	if err := k.spend(label, 1); err != nil {
		return err
	}
	k.at(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	return nil
}

// ---- allow-lists --------------------------------------------------------------------------------------

func (k *kg5State) kl5AllowsModels(label, list string) error {
	if err := k.acctPATCHes("acct-a", label, `{"allowed_models":`+list+`}`); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5AllowsNodes(label, list string) error {
	ids, err := k.nodeList(list)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"allowed_nodes": ids})
	if err := k.acctPATCHes("acct-a", label, string(b)); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5Code403Msg(code, msg string) error {
	return k.statusMsg(403, k.kl5Name(msg))
}

func (k *kg5State) kl5NoSideEffects() error {
	if err := k.statusCode(403, "key_model_denied"); err != nil {
		return err
	}
	if h, _ := k.kl5HoldFor(k.resp.reqID); h != 0 {
		return fmt.Errorf("a $%v hold was placed", h)
	}
	for n := range k.stations {
		if n != "kg5-spender" && k.kl5Upstream(n) != 0 {
			return fmt.Errorf("station %s was dispatched to", n)
		}
	}
	return k.kl5NoDangling("k1")
}

func (k *kg5State) kl5IllegalDenied() error {
	k.kl5ModServer()
	if err := k.kl5AllowsModels("k1", `["qwen3-32b"]`); err != nil {
		return err
	}
	k.ensureStation("kl5-llama", "llama-3.3-70b", 1, 2)
	return nil
}

func (k *kg5State) kl5DeniedArrives(label string) error {
	return k.keyRelayOn(k.b, label, "llama-3.3-70b", false, map[string]any{"messages": []map[string]string{{"role": "user", "content": "kl5-illegal"}}}, nil)
}

func (k *kg5State) kl5Is451() error { return k.status(451) }

func (k *kg5State) kl5ListsModels(label, list string) error {
	var models []string
	if err := json.Unmarshal([]byte(list), &models); err != nil {
		return err
	}
	// "Given a relay bearing k1 lists models [qwen3-32b, llama-3.3-70b]" stages the list for the
	// following When ("the first model has no eligible station and the second serves").
	if list == `["qwen3-32b","llama-3.3-70b"]` {
		return k.kl5ListsTwo(label)
	}
	for _, m := range models {
		if m != "qwen3-32b" {
			k.ensureStation("kl5-"+m, m, 1, 2)
		}
	}
	return k.keyRelayOn(k.b, label, models[0], false, map[string]any{"models": models[1:]}, nil)
}

func (k *kg5State) kl5SkippedPlanned(skipped, planned string) error {
	if err := k.status(200); err != nil {
		return err
	}
	if got := k.kl5Hdr("X-RogerAI-Model"); got != planned {
		return fmt.Errorf("served model %q, want %q (%q skipped)", got, planned, skipped)
	}
	return nil
}

func (k *kg5State) kl5BothNamed() error {
	if err := k.statusCode(403, "key_model_denied"); err != nil {
		return err
	}
	for _, m := range []string{"llama-3.3-70b", "gpt-oss-20b"} {
		if !strings.Contains(k.errMsg(), m) {
			return fmt.Errorf("the 403 does not name %s: %q", m, k.errMsg())
		}
	}
	return nil
}

func (k *kg5State) kl5BareProceeds() error { return k.status(200) }

func (k *kg5State) kl5PinsBoth(label, node, _ string) error {
	id := k.st(node).id
	if err := k.keyRelay(label, false, nil, map[string]string{"X-Roger-Node": id}); err != nil {
		return err
	}
	first := k.resp
	if err := k.keyRelay(label, false, map[string]any{"provider": map[string]any{"order": []string{id}, "allow_fallbacks": false}}, nil); err != nil {
		return err
	}
	if first.code != k.resp.code || !bytes.Equal(first.body, k.resp.body) {
		return fmt.Errorf("the header pin answered %d %q, the body order %d %q", first.code, first.body, k.resp.code, k.resp.body)
	}
	return nil
}

func (k *kg5State) kl5SendsOnlyN2N3(label string) error {
	k.ensureStation("n3", k.model, 1, 2)
	k.resps = nil
	for i := 0; i < 8; i++ {
		if err := k.keyRelay(label, false, map[string]any{"provider": map[string]any{"only": []string{k.st("n2").id, k.st("n3").id}}}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) kl5CandidateN2() error {
	for i, r := range k.resps {
		if r.code != 200 {
			return fmt.Errorf("relay %d = %d: %.300s", i, r.code, r.body)
		}
		if got := r.hdr.Get("X-RogerAI-Provider"); got != k.st("n2").id {
			return fmt.Errorf("relay %d served by %q, outside {n2}", i, got)
		}
	}
	return nil
}

func (k *kg5State) kl5SendsOrderN2N1(label string) error {
	return k.keyRelay(label, false, map[string]any{"provider": map[string]any{"order": []string{k.st("n2").id, k.st("n1").id}}}, nil)
}

func (k *kg5State) kl5N2SkippedN1First() error {
	if err := k.status(200); err != nil {
		return err
	}
	if got := k.kl5Hdr("X-RogerAI-Provider"); got != k.st("n1").id {
		return fmt.Errorf("served by %q, want n1", got)
	}
	if n := k.kl5Upstream("n2"); n != 0 {
		return fmt.Errorf("n2 received %d requests; it is outside the allow-list", n)
	}
	return nil
}

func (k *kg5State) kl5NoMatchNoDispatch() error {
	if err := k.statusCode(503, "no_match"); err != nil {
		return err
	}
	for _, n := range []string{"n1", "n2"} {
		if k.kl5Upstream(n) != 0 {
			return fmt.Errorf("%s was dispatched to", n)
		}
	}
	return nil
}

func (k *kg5State) kl5AllowsPrivate(label string) error {
	st := k.ensureStation("n-private", k.model, 1, 2)
	k.b.private[st.id] = true
	code := "147.520 MHz · KLFV-" + strings.ToUpper(k.nonce[:4])
	k.bandCode = code
	if err := k.db.CreateBand(store.Band{ID: "band_" + k.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: st.acct, NodeID: st.id, CreatedAt: time.Now().Unix()}); err != nil {
		return err
	}
	return k.kl5AllowsNodes(label, `["n-private"]`)
}

func (k *kg5State) kl5NoMatchThenBand(node string) error {
	if err := k.statusCode(503, "no_match"); err != nil {
		return err
	}
	if err := k.withBandCode(); err != nil {
		return err
	}
	return k.servedOn(node)
}

func (k *kg5State) kl5AllowsN1Tower(label, tower string) error {
	if err := k.kl5AllowsNodes(label, `["n1"]`); err != nil {
		return err
	}
	_, err := k.standUpTower(tower, k.model, 0.5, 1, 0)
	return err
}

func (k *kg5State) kl5CoinBridge() error {
	edgeCoinForTest = func() bool { return true }
	return nil
}

func (k *kg5State) kl5BridgeSkipped(node string) error {
	if err := k.pricedRelay("k1"); err != nil {
		return err
	}
	if err := k.status(200); err != nil {
		return err
	}
	if r := k.kl5Hdr("X-RogerAI-Relay"); r != "" {
		return fmt.Errorf("served through Tower %q, outside the key's allow-list", r)
	}
	if got := k.kl5Hdr("X-RogerAI-Provider"); got != k.st(node).id {
		return fmt.Errorf("served by %q, want %s", got, node)
	}
	return nil
}

func (k *kg5State) kl5AllowsN1Curated(label, cur string) error {
	if err := k.kl5AllowsNodes(label, `["n1"]`); err != nil {
		return err
	}
	st := k.ensureStation(cur, k.model, 0.1, 0.2)
	k.b.mu.Lock()
	reg := k.b.nodes[st.id]
	reg.Curated, reg.CuratedProvider = true, "openrouter"
	k.b.nodes[st.id] = reg
	k.b.mu.Unlock()
	return nil
}

func (k *kg5State) kl5CuratedNotCandidate(cur string) error {
	k.resps = nil
	for i := 0; i < 8; i++ {
		if err := k.pricedRelay("k1"); err != nil {
			return err
		}
	}
	for i, r := range k.resps {
		if r.hdr.Get("X-RogerAI-Provider") == k.st(cur).id {
			return fmt.Errorf("relay %d was served by the curated %s", i, cur)
		}
	}
	return nil
}

func (k *kg5State) kl5DeniedModel403(label string) error {
	if err := k.kl5AllowsModels(label, `["qwen3-32b"]`); err != nil {
		return err
	}
	k.snap(label)
	k.ensureStation("kl5-llama", "llama-3.3-70b", 1, 2)
	k.at0 = k.now()
	if err := k.keyRelayOn(k.b, label, "llama-3.3-70b", false, nil, nil); err != nil {
		return err
	}
	return k.statusCode(403, "key_model_denied")
}

func (k *kg5State) kl5LastUsedOnly() error {
	before := k.snaps["k1"]
	e, err := k.entry("k1")
	if err != nil {
		return err
	}
	for _, f := range []string{"usage", "limit_remaining"} {
		bj, _ := json.Marshal(before[f])
		aj, _ := json.Marshal(e[f])
		if string(bj) != string(aj) {
			return fmt.Errorf("%s changed %s -> %s on a denied relay", f, bj, aj)
		}
	}
	if fmt.Sprint(e["last_used"]) == fmt.Sprint(before["last_used"]) {
		return fmt.Errorf("last_used was not updated (%v)", e["last_used"])
	}
	wallet, _ := k.walletOf("acct-a")
	if ok, _ := k.settledOn(wallet, k.resp.reqID); ok {
		return fmt.Errorf("the denied relay wrote a lineage row")
	}
	return nil
}

// ---- grants versus keys -------------------------------------------------------------------------------

func (k *kg5State) kl5GrantBearer() error {
	if err := k.mintGrant("gk", "n1", true); err != nil {
		return err
	}
	return k.keyRelay("gk", false, nil, nil)
}

func (k *kg5State) kl5IsGrantRequest() error {
	if err := k.status(200); err != nil {
		return err
	}
	return k.kl5NoKeyHeaders()
}

func (k *kg5State) kl5KeyBearer() error { return k.pricedRelay("k1") }

func (k *kg5State) kl5IsKeyRequest() error {
	if err := k.status(200); err != nil {
		return err
	}
	return k.kl5HdrNum("X-RogerAI-Key-Limit", 5)
}

func (k *kg5State) kl5OwnerGrant(label string) error {
	if err := k.acctOwnsNode("acct-a", "a-node"); err != nil {
		return err
	}
	return k.mintGrant(label, "a-node", false)
}

func (k *kg5State) kl5FriendServed(label string) error {
	if err := k.keyRelay(label, false, nil, map[string]string{"X-Roger-Node": k.st("a-node").id}); err != nil {
		return err
	}
	return k.status(200)
}

func (k *kg5State) kl5GrantRules(label string) error {
	spent, _, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	if spent != 0 {
		return fmt.Errorf("%s window $%v moved on a grant request", label, spent)
	}
	return nil
}

func (k *kg5State) kl5OwnsN1(acct, node string) error {
	old := k.st(node)
	k.b.mu.Lock()
	delete(k.b.nodes, old.id)
	k.b.mu.Unlock()
	delete(k.stations, node)
	a, err := k.acct(acct)
	if err != nil {
		return err
	}
	owner := &fstation{ownerPriv: a.who.priv, acct: kl5Pub(a)}
	st := k.standUp(node, stationOpts{model: k.model, priceIn: old.priceIn, priceOut: old.priceOut, owner: owner})
	k.b.mu.Lock()
	k.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	k.b.mu.Unlock()
	k.scriptJSON(node, 20, "", 0)
	return nil
}

func (k *kg5State) kl5BilledCounts(label string) error {
	if err := k.billedAtOffer(); err != nil {
		return err
	}
	spent, _, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	c, _ := strconv.ParseFloat(k.kl5Hdr("X-RogerAI-Cost"), 64)
	if !kg5Near(spent, c) {
		return fmt.Errorf("%s window $%v, the relay cost $%v", label, spent, c)
	}
	return nil
}

// ---- lineage, generation, telemetry --------------------------------------------------------------------

func (k *kg5State) kl5ConsoleKeyID(label string) error {
	res, err := k.call(kg5Req{as: "acct:acct-a", method: http.MethodGet, path: "/console"})
	if err != nil {
		return err
	}
	if !bytes.Contains(res.body, []byte(`"key_id":"`+k.keyID(label)+`"`)) {
		return fmt.Errorf("the console lineage does not carry key_id %s: %.300s", k.keyID(label), res.body)
	}
	return nil
}

func (k *kg5State) kl5GenConsumer(label string, limit, after float64) error {
	reqID := k.resp.reqID
	res, err := k.call(kg5Req{as: "acct:acct-a", method: http.MethodGet, path: "/generation?id=" + reqID})
	if err != nil {
		return err
	}
	if res.js["key_id"] != k.keyID(label) {
		return fmt.Errorf("key_id %v, want %s: %.300s", res.js["key_id"], k.keyID(label), res.body)
	}
	if v, _ := kg5Num(res.js["key_limit"]); !kg5Near(v, limit) {
		return fmt.Errorf("key_limit %v, want %v", res.js["key_limit"], limit)
	}
	if v, _ := kg5Num(res.js["key_spend_after"]); !kg5Near(v, after) {
		return fmt.Errorf("key_spend_after %v, want %v", res.js["key_spend_after"], after)
	}
	return nil
}

func (k *kg5State) kl5GenOwner() error {
	reqID := k.lastHdr.Get("X-RogerAI-Request-Id")
	served := k.lastHdr.Get("X-RogerAI-Provider")
	var owner *fstation
	for _, st := range k.stations {
		if st.id == served {
			owner = st
		}
	}
	if owner == nil {
		return fmt.Errorf("no station served the relay")
	}
	r := httptest.NewRequest(http.MethodGet, "/generation?id="+reqID, nil)
	signReq(r, owner.ownerPriv, nil)
	rr := httptest.NewRecorder()
	k.b.routes().ServeHTTP(rr, r)
	var js map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &js)
	if rr.Code != 200 || js["key_id"] != k.keyID("k1") {
		return fmt.Errorf("owner view = %d key_id %v: %.300s", rr.Code, js["key_id"], rr.Body.Bytes())
	}
	for _, f := range []string{"key_limit", "key_spend_after"} {
		if _, ok := js[f]; ok {
			return fmt.Errorf("the owner view carries %s", f)
		}
	}
	return nil
}

// kl5UsageByKey: the scenario reads groups; one key relay and one signed relay give it both.
func (k *kg5State) kl5UsageByKey(acct string) error {
	if err := k.kl5Served("k1"); err != nil {
		return err
	}
	if err := k.kl5SignedServed(); err != nil {
		return err
	}
	return k.do("acct:"+acct, http.MethodGet, "/usage?by=key", nil)
}

func (k *kg5State) kl5RowsByKey() error {
	if err := k.status(200); err != nil {
		return err
	}
	var obj map[string]any
	_ = json.Unmarshal(k.resp.body, &obj)
	rows, _ := obj["rows"].([]any)
	if rows == nil {
		rows, _ = obj["by_key"].([]any)
	}
	var sawKey, sawNull bool
	for _, x := range rows {
		m, _ := x.(map[string]any)
		for _, f := range []string{"spend", "requests", "tokens"} {
			if _, ok := m[f]; !ok {
				return fmt.Errorf("row %v has no %s", m, f)
			}
		}
		switch m["key_id"] {
		case k.keyID("k1"):
			sawKey = true
		case nil:
			sawNull = true
		}
	}
	if !sawKey || !sawNull {
		return fmt.Errorf("/usage?by=key rows: key row %v, null row %v: %.400s", sawKey, sawNull, k.resp.body)
	}
	return nil
}

func (k *kg5State) kl5RefusedThree() error {
	if err := k.kl5Relay402("k1"); err != nil {
		return err
	}
	if err := k.mintWith("acct-a", "kdeny", `allowed_models ["qwen3-32b"]`); err != nil {
		return err
	}
	k.ensureStation("kl5-llama", "llama-3.3-70b", 1, 2)
	if err := k.keyRelayOn(k.b, "kdeny", "llama-3.3-70b", false, nil, nil); err != nil {
		return err
	}
	if err := k.mintWith("acct-a", "knode", `allowed_nodes ["n1"]`); err != nil {
		return err
	}
	return k.keyRelay("knode", false, nil, map[string]string{"X-Roger-Node": k.st("n2").id})
}

func (k *kg5State) kl5AdminCounters() error {
	if err := k.readAdminLive(); err != nil {
		return err
	}
	for _, c := range []string{"key_limit_refusals", "key_model_denials", "key_node_denials"} {
		v, ok := kg5Num(k.adminResp[c])
		if !ok || v < 1 {
			return fmt.Errorf("/admin/live %s = %v, want >= 1", c, k.adminResp[c])
		}
	}
	return nil
}

func (k *kg5State) kl5FiveHundred(n int, label string) error {
	if err := k.spend(label, 5); err != nil {
		return err
	}
	k.logMark = len(k.logs.String())
	for i := 0; i < n; i++ {
		if err := k.pricedRelay(label); err != nil {
			return err
		}
		if k.resp.code != 402 {
			return fmt.Errorf("relay %d = %d, want 402 key_limit", i, k.resp.code)
		}
	}
	return nil
}

func (k *kg5State) kl5OneLogLine(label string) error {
	logs := k.logs.String()[k.logMark:]
	n := 0
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, k.keyID(label)) && strings.Contains(l, "limit") {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d log lines name %s at its limit, want 1", n, label)
	}
	return nil
}

func (k *kg5State) kl5EmailOnFile(acct string) error {
	a, err := k.acct(acct)
	if err != nil {
		return err
	}
	k.t.Setenv("RESEND_API_KEY", "test")
	return k.db.BindOwner(store.Owner{GitHubID: a.gid, Login: a.who.login, Pubkey: kl5Pub(a), Email: acct + "@example.com"})
}

func (k *kg5State) kl5Crosses(label string) error {
	k.mailMu.Lock()
	k.mails = nil
	k.mailMu.Unlock()
	if err := k.spend(label, 4); err != nil {
		return err
	}
	if err := k.kl5Served(label); err != nil {
		return err
	}
	_, limit, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	spent, _, _ := k.kl5Window(label)
	if err := k.spend(label, limit-spent); err != nil {
		return err
	}
	return k.pricedRelay(label)
}

func (k *kg5State) kl5EmailsDeduped(label string) error {
	k.mailMu.Lock()
	mails := append([]string(nil), k.mails...)
	k.mailMu.Unlock()
	kk := k.keys[label]
	n80, n100 := 0, 0
	for _, m := range mails {
		if !strings.Contains(m, kk.id) {
			continue
		}
		if strings.Contains(m, kk.secret) {
			return fmt.Errorf("a key email carries the secret")
		}
		if !strings.Contains(m, label) {
			return fmt.Errorf("a key email does not name the key %q", label)
		}
		switch {
		case strings.Contains(m, "100%"):
			n100++
		case strings.Contains(m, "80%"):
			n80++
		}
	}
	if n80 != 1 || n100 != 1 {
		return fmt.Errorf("%d 80%% and %d 100%% emails for %s, want exactly one each", n80, n100, label)
	}
	return nil
}

func (k *kg5State) kl5NoSecretInLogs() error {
	if len(k.resps) == 0 {
		if err := k.kl5Served("k1"); err != nil {
			return err
		}
	}
	if strings.Contains(k.logs.String(), "rog-key_") {
		return fmt.Errorf("a log line carries a key secret")
	}
	return nil
}

// ---- adversarial ----------------------------------------------------------------------------------

func (k *kg5State) kl5SendsLimitHeader(label, v string) error {
	k.ensurePriced()
	return k.keyRelay(label, false, nil, map[string]string{"X-RogerAI-Key-Limit": v})
}

func (k *kg5State) kl5LimitStill5() error {
	if err := k.kl5HdrNum("X-RogerAI-Key-Limit", 5); err != nil {
		return err
	}
	return k.wantNum("k1", "limit_usd", 5)
}

func (k *kg5State) kl5SendsRogerKeyLimit(label string) error {
	return k.keyRelay(label, false, map[string]any{"roger": map[string]any{"key_limit": 1000}}, nil)
}

func (k *kg5State) kl5Code400Naming(code, field string) error { return k.code400Names(code, field) }

// kl5OtherWallet: the scenario names acct-b, which the Background does not create; it is
// created here so the step tests what it says.
func (k *kg5State) kl5OtherWallet(label string) error {
	if _, ok := k.accts["acct-b"]; !ok {
		if err := k.account("acct-b", "u_gh_b", 20); err != nil {
			return err
		}
	}
	wb, _ := k.walletOf("acct-b")
	k.ensurePriced()
	if err := k.keyRelay(label, false, nil, map[string]string{"X-Roger-User": wb}); err != nil {
		return err
	}
	first := k.resp
	b := k.accts["acct-b"]
	cookie := k.b.signSessionWallet(b.who.login, b.gid, b.who.wallet, time.Now().Add(time.Hour).Unix())
	if err := k.keyRelay(label, false, nil, map[string]string{"Origin": pbOrigin, "Cookie": sessionCookie + "=" + cookie}); err != nil {
		return err
	}
	k.resps = []kg5Resp{first, k.resp}
	return nil
}

func (k *kg5State) kl5PayerBearer(alias string) error {
	wallet, err := k.walletOf(alias)
	if err != nil {
		return err
	}
	for i, r := range k.resps {
		if r.code != 200 {
			return fmt.Errorf("variant %d = %d: %.300s", i, r.code, r.body)
		}
		if ok, _ := k.settledOn(wallet, r.reqID); !ok {
			return fmt.Errorf("variant %d did not settle on %s", i, alias)
		}
	}
	return nil
}

func (k *kg5State) kl5RemainingLong(label string, rem, _ float64) error {
	return k.kl5HasRemaining(label, rem)
}

func (k *kg5State) kl5StationClaims(cost float64) error {
	k.kl5Cost("n1", cost)
	return k.kl5Pinned("k1", "n1", true)
}

func (k *kg5State) kl5ClampedAtLimit(v float64) error {
	if err := k.status(200); err != nil {
		return err
	}
	u, err := kl5Usage(k.resp.body)
	if err != nil {
		return err
	}
	c, _ := kg5Num(u["cost"])
	if c > v+1e-9 {
		return fmt.Errorf("settled $%v, above the $%v authorized maximum", c, v)
	}
	return k.wantNum("k1", "limit_remaining", 0)
}

func (k *kg5State) kl5ExpiresSoon(label, _ string) error {
	exp := k.now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	if err := k.acctPATCHes("acct-a", label, `{"expires_at":"`+exp+`"}`); err != nil {
		return err
	}
	if err := k.status(200); err != nil {
		return err
	}
	return k.heldRelay(label, true)
}

func (k *kg5State) kl5ExpiryPasses() error {
	k.advance(3 * time.Second)
	return nil
}

func (k *kg5State) kl5CompletesNext401(code string) error {
	res, err := k.release()
	if err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	if err := k.settledOK(res, wallet); err != nil {
		return err
	}
	return k.nextBearer401("k1", code)
}

func (k *kg5State) kl5InFlightDeleted(label, acct, _ string) error {
	if err := k.heldRelay(label, true); err != nil {
		return err
	}
	if err := k.do("acct:"+acct, http.MethodDelete, "/account/keys/"+k.keyID(label), nil); err != nil {
		return err
	}
	return k.status(204)
}

func (k *kg5State) kl5CompletesAgainst(alias string) error {
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
	return k.nextBearer401("k1", "key_revoked")
}

func (k *kg5State) kl5Leaked(label string) error {
	if err := k.kl5SetBalance("acct-a", 100); err != nil {
		return err
	}
	wallet, _ := k.walletOf("acct-a")
	if err := k.db.SetMonthlyCap(wallet, 50); err != nil {
		return err
	}
	k.b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 120, burst: 40}
	_ = label
	return nil
}

func (k *kg5State) kl5Thief() error {
	k.codes = nil
	body := k.relayBody(k.model, false, nil)
	pin := map[string]string{"X-Roger-Node": k.st("n1").id}
	for i := 0; i < 10000; i++ {
		res, err := k.relayRaw(k.b, "k1", body, pin)
		if err != nil {
			return err
		}
		k.codes = append(k.codes, res.code)
		if res.code == 429 && res.hdr.Get("Retry-After") == "" {
			return fmt.Errorf("a 429 without Retry-After")
		}
	}
	return nil
}

func (k *kg5State) kl5ThiefBounded(label string) error {
	spent, _, err := k.kl5Window(label)
	if err != nil {
		return err
	}
	if spent > 5+1e-9 {
		return fmt.Errorf("%s spent $%v, above its $5 limit", label, spent)
	}
	wallet, _ := k.walletOf("acct-a")
	ms, err := k.db.MonthSpendOf(wallet, k.now())
	if err != nil {
		return err
	}
	if ms >= 50 {
		return fmt.Errorf("the account month reached $%v", ms)
	}
	paced := 0
	for _, c := range k.codes {
		if c == 429 {
			paced++
		}
	}
	if paced == 0 {
		return fmt.Errorf("the per-identity limiter never paced 10,000 requests")
	}
	return nil
}

func (k *kg5State) kl5OverReports() error {
	k.kl5Claim("n1", 40, 10*k.maxTokens)
	k.recCompletion = k.maxTokens
	return nil
}

func (k *kg5State) kl5RecountCapped() error {
	if err := k.status(200); err != nil {
		return err
	}
	c, _ := strconv.ParseFloat(k.kl5Hdr("X-RogerAI-Cost"), 64)
	if c > 0.003+1e-9 {
		return fmt.Errorf("cost $%v exceeds the hold", c)
	}
	spent, _, err := k.kl5Window("k1")
	if err != nil {
		return err
	}
	if !kg5Near(spent, c) {
		return fmt.Errorf("key window $%v, cost $%v", spent, c)
	}
	return nil
}

func (k *kg5State) kl5OrderingTrick() error {
	if err := k.kl5AllowsModels("k1", `["qwen3-32b"]`); err != nil {
		return err
	}
	k.ensureStation("kl5-llama", "llama-3.3-70b", 1, 2)
	k.b.mu.Lock()
	for _, n := range []string{"n1", "n2"} {
		delete(k.b.nodes, k.st(n).id)
	}
	k.b.mu.Unlock()
	return k.keyRelayOn(k.b, "k1", "qwen3-32b", false, map[string]any{"models": []string{"llama-3.3-70b"}}, nil)
}

func (k *kg5State) kl5StillSkipped() error {
	if err := k.statusCode(503, "no_match"); err != nil {
		return err
	}
	if k.kl5Upstream("kl5-llama") != 0 {
		return fmt.Errorf("the denied model's station was dispatched to")
	}
	return nil
}

// kl5NoKeyWork compares a signed relay by an account holding keys against one by an account
// holding none: the same shared-store work, so no key lookup, reserve, or window sum ran.
func (k *kg5State) kl5NoKeyWork() error {
	if _, ok := k.accts["acct-n"]; !ok {
		if err := k.account("acct-n", "u_gh_n", 20); err != nil {
			return err
		}
	}
	k.ensurePriced()
	body := k.relayBody(k.model, false, nil)
	pin := map[string]string{"X-Roger-Node": k.st("n1").id}
	_, _ = k.relayRaw(k.b, "", body, map[string]string{"sign": "acct-n", "X-Roger-Node": pin["X-Roger-Node"]}) // warm both paths alike
	_, _ = k.relayRaw(k.b, "", body, map[string]string{"sign": "acct-a", "X-Roger-Node": pin["X-Roger-Node"]})
	var rn, ra kg5Resp
	without := k.cmds(func() {
		rn, _ = k.relayRaw(k.b, "", body, map[string]string{"sign": "acct-n", "X-Roger-Node": pin["X-Roger-Node"]})
	})
	with := k.cmds(func() {
		ra, _ = k.relayRaw(k.b, "", body, map[string]string{"sign": "acct-a", "X-Roger-Node": pin["X-Roger-Node"]})
	})
	if rn.code != 200 || ra.code != 200 {
		return fmt.Errorf("signed relays = %d / %d", rn.code, ra.code)
	}
	if with != without {
		return fmt.Errorf("a signed relay by a key-holding account ran %d shared-store commands, one by an account with no keys %d", with, without)
	}
	return nil
}

func kl5Pub(a *kg5Acct) string {
	return hex.EncodeToString(a.who.priv.Public().(ed25519.PublicKey))
}

// ---- registration -----------------------------------------------------------------------------------

func (k *kg5State) registerLimits(sc *godog.ScenarioContext) {
	k.registerCommon(sc)
	// Background
	sc.Step(`^the fee rate is 10%$`, k.kl5Fee10)
	sc.Step(`^account "([^"]+)" is logged in \(wallet "([^"]+)"\) with balance \$([0-9.]+) and no monthly cap$`, k.kl5NoCap)
	sc.Step(`^"([^"]+)" minted key "([^"]+)" with (.+)$`, k.mintWith)
	sc.Step(`^node "([^"]+)" is on air for "([^"]+)" at \$([0-9.]+)/1M in, \$([0-9.]+)/1M out, proven live$`, k.kl5Node)
	sc.Step(`^a relay for "([^"]+)" with max_tokens (\d+) holds \$([0-9.]+) on "([^"]+)" and \$([0-9.]+) on "([^"]+)"$`, k.kl5BackgroundHolds)

	// gate position
	sc.Step(`^moderation mode is "sync"$`, k.kl5SyncMod)
	sc.Step(`^a priced relay bearing "([^"]+)" arrives$`, k.pricedRelay)
	sc.Step(`^moderation runs first, then the pick, then the key-limit check next to the monthly-cap check, then the hold, then dispatch$`, k.kl5OrderObserved)
	sc.Step(`^a prompt moderation rejects is 451 with no key-limit check, no hold, and no usage change$`, k.kl5Illegal451)
	sc.Step(`^"([^"]+)" has \$([0-9.]+) spent this window$`, k.kl5HasSpent)
	sc.Step(`^it is 402 before HoldFor is called and the balance stays \$([0-9.]+)$`, k.kl5BeforeHold)
	sc.Step(`^node "([^"]+)" is on air for "([^"]+)" at \$0/\$0$`, func(n, m string) error { return k.kl5Node(n, m, 0, 0) })
	sc.Step(`^a relay bearing "([^"]+)" for "([^"]+)" arrives$`, k.relayAsksFor)
	sc.Step(`^it is served with X-RogerAI-Cost 0 and no 402 \(there is nothing to protect\)$`, k.kl5FreeServed)
	sc.Step(`^"([^"]+)" usage stays \$([0-9.]+) and its request count increments$`, k.kl5UsageStaysCount)
	sc.Step(`^every station fails the relay bearing "([^"]+)"$`, k.kl5EveryStationFails)
	sc.Step(`^the relay returns its error$`, k.kl5RelayReturnsError)
	sc.Step(`^the key reserve is released with the wallet hold and "([^"]+)" usage is unchanged$`, k.kl5ReleasedUnchanged)

	// ceiling
	sc.Step(`^a relay bearing "([^"]+)" that holds \$([0-9.]+) arrives$`, k.kl5RelayHolding)
	sc.Step(`^it is served \(spend \+ hold == limit is allowed, like the monthly cap\)$`, func() error { return k.status(200) })
	sc.Step(`^it is 402 with limit_source "([^"]+)"$`, k.itIs402Source)
	sc.Step(`^the request would in practice cost \$([0-9.]+)$`, k.kl5InPractice)
	sc.Step(`^it arrives holding \$([0-9.]+)$`, k.kl5ArrivesHolding)
	sc.Step(`^it is 402 \(we never authorize what we could not capture\)$`, k.kl5Is402)
	sc.Step(`^"([^"]+)" has \$([0-9.]+) spent this window \(remaining \$([0-9.]+)\)$`, k.kl5SpentRemaining)
	sc.Step(`^a relay bearing "([^"]+)" plans n1 \(\$0\.003\) then n2 \(\$0\.006\)$`, k.kl5PlansN1N2)
	sc.Step(`^the hold covers n1 alone, n2 is trimmed from the plan, no 402 \(a refused ceiling is not a refused request\)$`, k.kl5HoldCoversN1)
	sc.Step(`^no key notice email or at-limit header is triggered by the ceiling probe$`, k.kl5NoCeilingNotice)
	sc.Step(`^"([^"]+)" has remaining \$([0-9.]+)$`, k.kl5HasRemaining)
	sc.Step(`^a relay bearing "([^"]+)" whose cheapest pick holds \$([0-9.]+) arrives$`, k.kl5CheapestHolds)
	sc.Step(`^it is 402 \(shrinking the hold would break the settle-time ceiling; features/money/holds\.feature C1\)$`, k.kl5Is402)
	sc.Step(`^"([^"]+)" has monthly cap remaining \$([0-9.]+) and balance \$([0-9.]+)$`, k.kl5CapAndBalance)
	sc.Step(`^the outcome is (served|402) with limit_source "([^"]*)"$`, k.kl5Outcome)
	sc.Step(`^10,000 priced relays bearing "([^"]+)" are served over the month$`, k.kl5TenThousand)
	sc.Step(`^none is 402 for key_limit \(the monthly cap and balance still bind as today\)$`, k.kl5NoKeyLimit402)
	sc.Step(`^"([^"]+)" sets monthly cap \$([0-9.]+) and has \$([0-9.]+) month spend$`, k.kl5MonthlyCapSet)
	sc.Step(`^a relay bearing "([^"]+)" \(remaining \$([0-9.]+)\) arrives$`, k.kl5RelayRemaining)
	sc.Step(`^it is 402 with limit_source "monthly_cap" and the existing monthly message$`, k.kl5MonthlyExisting)

	// the 402 body
	sc.Step(`^a priced relay is refused because of (key_limit|monthly_cap|credits)$`, k.kl5RefusedBecause)
	sc.Step(`^the body is (\{.*\})$`, k.kl5BodyIs)
	sc.Step(`^a signed \(no key\) relay is refused by the monthly cap$`, k.kl5SignedMonthly)
	sc.Step("^the message is exactly today's \"(.+)\"$", k.kl5TodaysMessage)
	sc.Step(`^the body additionally carries metadata\.limit_source "monthly_cap" and code "monthly_cap_reached"$`, k.kl5AdditionallyCarries)
	sc.Step(`^the message reads "([^"]+)"$`, k.kl5NoneMessage)
	sc.Step(`^a relay bearing "([^"]+)" is 402 for key_limit$`, k.kl5Relay402)
	sc.Step(`^the response has X-RogerAI-Cost 0, no X-RogerAI-Receipt, no X-RogerAI-Provider$`, k.kl5NoReceipt)
	sc.Step(`^a relay bearing "([^"]+)" is refused$`, k.kl5Refused)
	sc.Step(`^X-RogerAI-Key-Limit is ([0-9.]+), X-RogerAI-Key-Spend is ([0-9.]+), X-RogerAI-Key-Pct is (\d+), X-RogerAI-Key-Notice is "([^"]+)"$`, k.kl5KeyHeadersNotice)

	// key notice headers
	sc.Step(`^a relay bearing "([^"]+)" is served$`, k.kl5Served)
	sc.Step(`^X-RogerAI-Key-Limit is ([0-9.]+), X-RogerAI-Key-Spend is ([0-9.]+), X-RogerAI-Key-Pct is (\d+), and X-RogerAI-Key-Notice is absent$`, k.kl5KeyHeadersNoNotice)
	sc.Step(`^X-RogerAI-Key-Notice is "([^"]+)"$`, k.kl5NoticeIs)
	sc.Step(`^X-RogerAI-Key-Notice is (absent|present)$`, k.kl5NoticePresence)
	sc.Step(`^key "([^"]+)" with limit 0$`, k.kl5KeyLimit0)
	sc.Step(`^no X-RogerAI-Key-\* header is set \(nothing to report\), as the monthly headers do when unlimited$`, k.kl5NoKeyHeaders)
	sc.Step(`^"([^"]+)" has monthly cap \$([0-9.]+) with \$([0-9.]+) spent, and "([^"]+)" has \$([0-9.]+) spent$`, k.kl5MonthlyAndKey)
	sc.Step(`^X-RogerAI-Monthly-Pct is (\d+) with its notice, X-RogerAI-Key-Pct is (\d+) with no key notice$`, k.kl5MonthlyPctKeyPct)
	sc.Step(`^a stream bearing "([^"]+)" is served$`, k.kl5StreamServed)
	sc.Step(`^X-RogerAI-Key-Limit / -Spend / -Pct \(/ -Notice\) are in the response headers before the first data: frame$`, k.kl5HeadersBeforeFrame)
	sc.Step(`^the spend value is the window spend BEFORE this request \(the request's own cost lands in the usage chunk\)$`, k.kl5SpendBefore)
	sc.Step(`^a stream bearing "([^"]+)" settles for \$([0-9.]+) with \$([0-9.]+) spent before it$`, k.kl5StreamSettles)
	sc.Step(`^the usage chunk's "rogerai" object carries key_id "([^"]+)", key_limit ([0-9.]+), key_spend ([0-9.]+), key_pct (\d+), key_reset_at "([^"]+)"$`, k.kl5ChunkKeyState)
	sc.Step(`^"([^"]+)" has \$([0-9.]+) spent$`, k.kl5HasSpent)
	sc.Step(`^a non-stream relay bearing "([^"]+)" settles for \$([0-9.]+)$`, k.kl5NonStreamSettles)
	sc.Step(`^X-RogerAI-Key-Spend is ([0-9.]+) \(the header is set at the gate; /account/keys shows ([0-9.]+) afterwards\)$`, k.kl5SpendHeaderGate)
	sc.Step(`^a signed relay with no key bearer is served$`, k.kl5SignedServed)
	sc.Step(`^no X-RogerAI-Key-\* header is set$`, k.kl5NoKeyHeaders)
	sc.Step(`^every X-RogerAI-Key-\* value is a number or the notice sentence; none contains "rog-key_", a hash, or a wallet id$`, k.kl5HeadersClean)

	// settle and recount
	sc.Step(`^a relay bearing "([^"]+)" holds \$([0-9.]+) and settles for \$([0-9.]+)$`, k.kl5HoldsSettles)
	sc.Step(`^"([^"]+)" window spend is \$([0-9.]+) and the \$([0-9.]+) remainder of the reserve is released$`, k.kl5WindowRemainder)
	sc.Step(`^a station claims 10x the tokens the hold was sized for$`, k.kl5Claims10x)
	sc.Step(`^the relay bearing "([^"]+)" settles$`, k.kl5RelaySettles)
	sc.Step(`^the cost is capped at the hold \(features/pricing/price_floor\.feature\) and the key window rises by at most the hold$`, k.kl5CappedAtHold)
	sc.Step(`^the broker re-counts the station's claim DOWN$`, k.kl5RecountDown)
	sc.Step(`^the relay settles$`, k.kl5RelaySettlesAny)
	sc.Step(`^key spend rises by the recounted \(lower\) amount; there is no path by which it rises above the reserve$`, k.kl5RecountedOnly)
	sc.Step(`^the station returns 2xx with empty output and every fallback fails$`, k.kl5EmptyThenFail)
	sc.Step(`^the relay ends$`, k.kl5RelayEnds)
	sc.Step(`^the wallet hold and the key reserve are both released and "([^"]+)" usage is unchanged$`, k.kl5BothReleased)
	sc.Step(`^"([^"]+)" has remaining \$([0-9.]+) and the plan is n1 \(\$0\.003\) then n2 \(\$0\.006\), ceiling covered$`, k.kl5RemainingPlan)
	sc.Step(`^n1 returns 429 and n2 serves for \$([0-9.]+)$`, k.kl5N1429N2Serves)
	sc.Step(`^key spend rises by \$([0-9.]+) and the rest of the \$([0-9.]+) reserve is released$`, k.kl5RisesRest)
	sc.Step(`^the first model has no eligible station and the second serves$`, k.kl5FirstNoneSecondServes)
	sc.Step(`^key spend rises by the served pair's cost, once$`, k.kl5RisesOnce)

	// concurrency
	sc.Step(`^two relays bearing "([^"]+)", each holding \$([0-9.]+), arrive at the same instant$`, k.kl5TwoRacing)
	sc.Step(`^one is served and one is 402 with limit_source "key_limit"$`, k.kl5OneEach)
	sc.Step(`^the window spend after settle is at most \$([0-9.]+)$`, k.kl5AtMost)
	sc.Step(`^(\d+) relays bearing "([^"]+)", each holding \$([0-9.]+), arrive concurrently$`, k.kl5NConcurrent)
	sc.Step(`^exactly (\d+) pass the gate and (\d+) are 402 \(floor\(remaining/hold\), the same law as wallet holds in features/money/idempotency_concurrency\.feature\)$`, k.kl5ExactlyPass)
	sc.Step(`^two instances share the store and "([^"]+)" has remaining \$([0-9.]+)$`, k.kl5TwoInstancesRemaining)
	sc.Step(`^(\d+) relays arrive on A and (\d+) on B at the same instant, each holding \$([0-9.]+)$`, k.kl5SplitInstances)
	sc.Step(`^exactly (\d+) pass in total; the reserve is one atomic operation in the shared store, not a per-instance read-then-hold$`, k.kl5ExactlyTotal)
	sc.Step(`^the key reserve succeeds and the wallet hold then fails \(insufficient balance\)$`, k.kl5ReserveThenHoldFails)
	sc.Step(`^the relay returns 402 credits$`, k.kl5Returns402Credits)
	sc.Step(`^the key reserve is released in the same exit path; "([^"]+)" has no dangling reservation$`, k.kl5NoDangling)
	sc.Step(`^a relay bearing "([^"]+)" reserved \$([0-9.]+) and the instance is SIGKILLed before settle$`, k.kl5Killed)
	sc.Step(`^the deploy-orphan sweep runs \(features/money/orphan_holds\.feature\)$`, k.kl5Sweep)
	sc.Step(`^the key reserve is released with the orphaned wallet hold, by request id$`, k.kl5ReleasedByRequest)
	sc.Step(`^the shared store is down when a relay bearing "([^"]+)" reaches the hold gate$`, k.kl5StoreDownAtGate)
	sc.Step(`^it is 503 "([^"]+)"$`, k.kl5Is503)
	sc.Step(`^no wallet hold is left behind \(the reserve and the hold succeed or fail together\)$`, k.kl5NoHoldLeft)
	sc.Step(`^the key limit is not bypassed$`, k.kl5NotBypassed)
	sc.Step(`^instance A settled \$([0-9.]+) and instance B settled \$([0-9.]+) for "([^"]+)" this window$`, k.kl5ABSettled)
	sc.Step(`^either computes the gate$`, k.eitherComputes)
	sc.Step(`^remaining is \$([0-9.]+) on both$`, k.kl5RemainingBoth)

	// window edges
	sc.Step(`^"([^"]+)" reset "daily" with \$([0-9.]+) spent today$`, k.kl5DailySpentToday)
	sc.Step(`^a relay arrives at 23:59:59Z it is 402$`, k.kl5At235959Is402)
	sc.Step(`^a relay arrives at 00:00:00Z it is served$`, k.kl5AtMidnightServed)
	sc.Step(`^"([^"]+)" reset "daily" with \$([0-9.]+) spent, a stream starts 23:59:58Z and settles 00:00:03Z for \$([0-9.]+)$`, k.kl5StreamAcrossMidnight)
	sc.Step(`^the settle is attributed to the request's start time: yesterday's window closes at \$5\.00 and today's opens at \$0\.00$`, k.kl5AttributedToStart)
	sc.Step(`^"([^"]+)" hit its monthly limit on the 10th$`, k.kl5HitOn10th)
	sc.Step(`^the account changes reset to "daily" on the 10th$`, k.kl5ResetDailyOn10th)
	sc.Step(`^the next request is served \(a new anchor\), and the month's earlier spend stays in lifetime usage$`, k.kl5NewAnchor)
	sc.Step(`^key "([^"]+)" with limit 1 and reset "none", \$1\.00 settled in a prior month$`, k.kl5NonePriorMonth)
	sc.Step(`^a relay bearing "([^"]+)" arrives$`, k.pricedRelay)

	// allow-lists
	sc.Step(`^"([^"]+)" allows models (\[.*\])$`, k.kl5AllowsModels)
	sc.Step(`^a relay bearing "([^"]+)" asks for "([^"]+)"$`, k.relayAsksFor)
	sc.Step(`^it is 403 with code "([^"]+)" and message "([^"]+)"$`, k.kl5Code403Msg)
	sc.Step(`^no hold, no reserve, no dispatch, no usage change$`, k.kl5NoSideEffects)
	sc.Step(`^moderation mode is "sync" and the prompt is illegal$`, k.kl5IllegalDenied)
	sc.Step(`^a relay bearing "([^"]+)" for a denied model arrives$`, k.kl5DeniedArrives)
	sc.Step(`^it is 451 \(moderation first\), not 403$`, k.kl5Is451)
	sc.Step(`^"([^"]+)" allows (\["[^"]+"(?:,"[^"]+")*\])$`, k.kl5AllowsModels)
	sc.Step(`^a relay bearing "([^"]+)" lists models (\[.*\])$`, k.kl5ListsModels)
	sc.Step(`^"([^"]+)" is skipped silently and "([^"]+)" is planned$`, k.kl5SkippedPlanned)
	sc.Step(`^it is 403 "key_model_denied" naming both models$`, k.kl5BothNamed)
	sc.Step(`^the bare id matches and the request proceeds$`, k.kl5BareProceeds)
	sc.Step(`^"([^"]+)" allows nodes (\[.*\])$`, k.kl5AllowsNodes)
	sc.Step(`^a relay bearing "([^"]+)" pins X-Roger-Node "([^"]+)" \(or provider\.order \["([^"]+)"\] with allow_fallbacks false\)$`, k.kl5PinsBoth)
	sc.Step(`^a relay bearing "([^"]+)" sends provider\.only \["n2","n3"\]$`, k.kl5SendsOnlyN2N3)
	sc.Step(`^the candidate set is \{n2\}$`, k.kl5CandidateN2)
	sc.Step(`^a relay bearing "([^"]+)" sends provider\.order \["n2","n1"\]$`, k.kl5SendsOrderN2N1)
	sc.Step(`^"n2" is skipped and "n1" is attempt 1$`, k.kl5N2SkippedN1First)
	sc.Step(`^it is 503 with code "no_match" and no dispatch$`, k.kl5NoMatchNoDispatch)
	sc.Step(`^"([^"]+)" allows \["n-private"\] which is band-only$`, k.kl5AllowsPrivate)
	sc.Step(`^a relay bearing "([^"]+)" arrives with no freq$`, k.arrivesNoFreq)
	sc.Step(`^it is 503 no_match; with the band code it is served on "([^"]+)"$`, k.kl5NoMatchThenBand)
	sc.Step(`^"([^"]+)" allows nodes \["n1"\] and a Tower "([^"]+)" also hosts the model$`, k.kl5AllowsN1Tower)
	sc.Step(`^the request-seeded coin would pick the bridge$`, k.kl5CoinBridge)
	sc.Step(`^the bridge is skipped \(the Tower's relay id is not allowed\) and "([^"]+)" serves$`, k.kl5BridgeSkipped)
	sc.Step(`^"([^"]+)" allows \["n1"\] and a curated station "([^"]+)" serves the model cheaper$`, k.kl5AllowsN1Curated)
	sc.Step(`^"([^"]+)" is not a candidate$`, k.kl5CuratedNotCandidate)
	sc.Step(`^a relay bearing "([^"]+)" is 403 key_model_denied$`, k.kl5DeniedModel403)
	sc.Step(`^last_used is updated and nothing else is$`, k.kl5LastUsedOnly)

	// grants versus keys
	sc.Step(`^a relay carries Authorization "Bearer rog-grant_\.\.\."$`, k.kl5GrantBearer)
	sc.Step(`^it is a grant request: grant caps and grant allow-lists apply; no key limit, no key headers$`, k.kl5IsGrantRequest)
	sc.Step(`^a relay carries Authorization "Bearer rog-key_\.\.\."$`, k.kl5KeyBearer)
	sc.Step(`^it is a key request: key limit and key allow-lists apply; grant logic never runs$`, k.kl5IsKeyRequest)
	sc.Step(`^"acct-a" \(owner\) minted grant "([^"]+)" for a friend$`, k.kl5OwnerGrant)
	sc.Step(`^the friend's relay bearing "([^"]+)" is served at the grant price$`, k.kl5FriendServed)
	sc.Step(`^it settles against the grant's wallet rules and "([^"]+)" usage is unchanged$`, k.kl5GrantRules)
	sc.Step(`^"([^"]+)" owns "([^"]+)"$`, k.kl5OwnsN1)
	sc.Step(`^a relay bearing "([^"]+)" is served by "([^"]+)"$`, k.servedBy)
	sc.Step(`^it is billed at the offer price and counts toward "([^"]+)"'s window$`, k.kl5BilledCounts)

	// lineage, generation, telemetry
	sc.Step(`^the /console lineage row carries key_id "([^"]+)"$`, k.kl5ConsoleKeyID)
	sc.Step(`^the consumer view of GET /generation\?id= carries key_id "([^"]+)", key_limit ([0-9.]+), key_spend_after ([0-9.]+) \(fields absent for non-key requests\)$`, k.kl5GenConsumer)
	sc.Step(`^the owner view \(the station's payout owner\) carries key_id only, never key_limit or key_spend_after$`, k.kl5GenOwner)
	sc.Step(`^"([^"]+)" GETs /usage\?by=key$`, k.kl5UsageByKey)
	sc.Step(`^rows are keyed by key id with spend, requests, tokens; non-key spend is under key_id null$`, k.kl5RowsByKey)
	sc.Step(`^relays are refused for key_limit, key_model_denied, key_node_denied$`, k.kl5RefusedThree)
	sc.Step(`^/admin/live carries key_limit_refusals, key_model_denials, key_node_denials counters \(read-only, multi-instance summed\)$`, k.kl5AdminCounters)
	sc.Step(`^(\d+) relays bearing "([^"]+)" are 402 key_limit in one window$`, k.kl5FiveHundred)
	sc.Step(`^one log line names key "([^"]+)" at limit; the others are counted, not logged$`, k.kl5OneLogLine)
	sc.Step(`^"([^"]+)" has an email on file and RESEND_API_KEY is set$`, k.kl5EmailOnFile)
	sc.Step(`^"([^"]+)" crosses 80% and then 100% in one window$`, k.kl5Crosses)
	sc.Step(`^at most one 80% email and one 100% email are sent for "([^"]+)" that window, naming the key by name and id, never the secret$`, k.kl5EmailsDeduped)
	sc.Step(`^no relay log line contains "rog-key_"$`, k.kl5NoSecretInLogs)

	// adversarial
	sc.Step(`^a relay bearing "([^"]+)" sends X-RogerAI-Key-Limit "([^"]+)"$`, k.kl5SendsLimitHeader)
	sc.Step(`^the request header is ignored \(response headers are never read\) and the limit is still 5$`, k.kl5LimitStill5)
	sc.Step(`^a relay bearing "([^"]+)" sends roger\.key_limit 1000$`, k.kl5SendsRogerKeyLimit)
	sc.Step(`^it is 400 with code "([^"]+)" naming "([^"]+)"$`, k.kl5Code400Naming)
	sc.Step(`^a relay bearing "([^"]+)" sends X-Roger-User "u_gh_b" or a web-session cookie of "acct-b"$`, k.kl5OtherWallet)
	sc.Step(`^the payer is "([^"]+)" \(the bearer decides; features/relay/spend\.feature\)$`, k.kl5PayerBearer)
	sc.Step(`^"([^"]+)" has remaining \$([0-9.]+) and a stream holding \$([0-9.]+) runs long$`, k.kl5RemainingLong)
	sc.Step(`^the station claims \$([0-9.]+) of tokens$`, k.kl5StationClaims)
	sc.Step(`^the settle is clamped to \$([0-9.]+) \(the authorized maximum\) and the window closes exactly at the limit$`, k.kl5ClampedAtLimit)
	sc.Step(`^"([^"]+)" expires in 2 seconds and a stream bearing "([^"]+)" is in flight$`, k.kl5ExpiresSoon)
	sc.Step(`^the expiry passes$`, k.kl5ExpiryPasses)
	sc.Step(`^the stream completes, settles, and counts toward the window; the next request is 401 (key_expired)$`, k.kl5CompletesNext401)
	sc.Step(`^a stream bearing "([^"]+)" is in flight and "([^"]+)" deletes "([^"]+)"$`, k.kl5InFlightDeleted)
	sc.Step(`^the stream completes, settles against "([^"]+)", and the next request is 401 key_revoked$`, k.kl5CompletesAgainst)
	sc.Step(`^"([^"]+)" leaked with limit \$5 and the account has monthly cap \$50$`, k.kl5Leaked)
	sc.Step(`^the thief runs 10,000 requests$`, k.kl5Thief)
	sc.Step(`^settled spend from "([^"]+)" is at most \$5\.00 that window, the account's month stays under \$50, and the per-identity limiter \(120 rpm / burst 40\) paces them$`, k.kl5ThiefBounded)
	sc.Step(`^a station over-reports tokens 10x$`, k.kl5OverReports)
	sc.Step(`^the recount lowers the claim and the settle is capped at the hold; key spend rises by the recounted amount only$`, k.kl5RecountCapped)
	sc.Step(`^a relay lists models \["qwen3-32b","llama-3\.3-70b"\] and "qwen3-32b" has no eligible station$`, k.kl5OrderingTrick)
	sc.Step(`^"llama-3\.3-70b" is still skipped \(the allow-list is applied before the plan\) and the result is 503 no_match$`, k.kl5StillSkipped)
	sc.Step(`^no key lookup, reserve, or window sum is performed \(the hot paid path stays one cap read \+ one spend read\)$`, k.kl5NoKeyWork)
}

func TestKeyLimitsBDD(t *testing.T) {
	kg5Run(t, "key_limits", "../../features/relay/key_limits.feature", func(k *kg5State, sc *godog.ScenarioContext) {
		k.registerLimits(sc)
	})
}
