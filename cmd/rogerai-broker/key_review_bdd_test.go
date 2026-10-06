package main

// key_review_bdd_test.go: the steps for the "# slice-5 review 2026-10-05" scenarios in
// features/auth/key_guardrails.feature. The races run on TWO brokers over the same stores
// (instanceB); the reversal scenarios settle real relays and post the refund or chargeback
// through the store, as the payment webhooks do.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/store"
)

func (k *kg5State) registerReview(sc *godog.ScenarioContext) {
	sc.Step(`^"([^"]+)" renames "([^"]+)" on A while "([^"]+)" DELETEs it on B between A's read and write$`,
		func(acct, label, _ string) error { return k.rvPatchRace(acct, label, "x", "delete") })
	sc.Step(`^"([^"]+)" renames "([^"]+)" to "([^"]+)" on A while "([^"]+)" disables it on B between A's read and write$`,
		func(acct, label, name, _ string) error { return k.rvPatchRace(acct, label, name, "disable") })
	sc.Step(`^the rename is 404 "([^"]+)"$`, func(code string) error { return k.statusCode(http.StatusNotFound, code) })
	sc.Step(`^"([^"]+)" is disabled and named "([^"]+)"$`, k.rvDisabledNamed)
	sc.Step(`^"([^"]+)" minted "([^"]+)" with a \$([0-9.]+) daily limit and spent all of it yesterday \(UTC\)$`, k.rvSpentYesterday)
	sc.Step(`^that request is charged back \$([0-9.]+) today$`, k.rvChargeback)
	sc.Step(`^"([^"]+)" shows usage_daily ([0-9.]+), limit_remaining ([0-9.]+) and usage ([0-9.]+)$`, k.rvShows)
	sc.Step(`^key "([^"]+)" settled \$([0-9.]+) in one request$`, k.rvSettledOne)
	sc.Step(`^that request is refunded \$([0-9.]+) and then charged back \$([0-9.]+)$`, k.rvRefundThenChargeback)
	sc.Step(`^"([^"]+)" shows usage ([0-9.]+) \(never below zero\)$`, func(label string, v float64) error { return k.wantNum(label, "usage", v) })
	sc.Step(`^"([^"]+)" reads "([^"]+)" on A$`, k.rvReadsOnA)
	sc.Step(`^the shared store refuses every command$`, k.saSharedDown)
	sc.Step(`^the shared store answers again$`, k.rvSharedBack)
	sc.Step(`^within (\d+) seconds a relay bearing "([^"]+)" on B is 401 "([^"]+)"$`, k.rvEventually401OnB)
	k.registerUnknownKeys(sc)
	k.registerMgmtBearer(sc)
	k.registerTouch(sc)
}

// rvPatchRace renames label on A; between A's read and its write, B deletes or disables it.
func (k *kg5State) rvPatchRace(acct, label, name, onB string) error {
	if k.b2 == nil {
		k.instanceB()
	}
	a := k.b
	var bRes kg5Resp
	var bErr error
	fired := false
	keyPatchGapForTest = func(b *broker) {
		if b != a || fired {
			return
		}
		fired = true
		rq := kg5Req{b: k.b2, as: "acct:" + acct, path: "/account/keys/" + k.keyID(label)}
		if onB == "delete" {
			rq.method = http.MethodDelete
		} else {
			rq.method, rq.body = http.MethodPatch, []byte(`{"disabled":true}`)
		}
		bRes, bErr = k.call(rq)
	}
	defer func() { keyPatchGapForTest = nil }()
	aRes, err := k.call(kg5Req{b: a, as: "acct:" + acct, method: http.MethodPatch, path: "/account/keys/" + k.keyID(label),
		body: []byte(fmt.Sprintf(`{"name":%q}`, name))})
	if err != nil {
		return err
	}
	if bErr != nil {
		return bErr
	}
	if !fired {
		return fmt.Errorf("A's PATCH never reached its write (A answered %d: %.200s)", aRes.code, aRes.body)
	}
	if bRes.code/100 != 2 {
		return fmt.Errorf("B's %s answered %d, want 2xx: %.200s", onB, bRes.code, bRes.body)
	}
	k.resp = aRes
	return nil
}

func (k *kg5State) rvDisabledNamed(label, name string) error {
	e, err := k.entry(label)
	if err != nil {
		return err
	}
	if e["disabled"] != true || e["name"] != name {
		return fmt.Errorf("%s shows disabled=%v name=%v, want disabled=true name=%q", label, e["disabled"], e["name"], name)
	}
	return nil
}

var rvToday = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) // a Wednesday

func (k *kg5State) rvSpentYesterday(acct, label string, limit float64) error {
	k.at(rvToday.Add(-24 * time.Hour))
	if err := k.mintWith(acct, label, fmt.Sprintf(`limit_usd %g, reset "daily"`, limit)); err != nil {
		return err
	}
	if err := k.spend(label, limit); err != nil {
		return err
	}
	k.at(rvToday)
	return nil
}

func (k *kg5State) rvChargeback(amount float64) error {
	wallet, err := k.walletOf("acct-a")
	if err != nil {
		return err
	}
	_, err = k.db.Chargeback("dp_rv_"+k.nonce, wallet, k.spendReq, amount, k.now())
	return err
}

func (k *kg5State) rvShows(label string, daily, remaining, usage float64) error {
	for f, v := range map[string]float64{"usage_daily": daily, "limit_remaining": remaining, "usage": usage} {
		if err := k.wantNum(label, f, v); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) rvSettledOne(label string, amount float64) error {
	if err := k.mint("acct-a", label, `{"name":"`+label+`"}`, nil); err != nil {
		return err
	}
	return k.spend(label, amount)
}

func (k *kg5State) rvRefundThenChargeback(refund, charge float64) error {
	wallet, err := k.walletOf("acct-a")
	if err != nil {
		return err
	}
	if _, _, err := k.db.RefundLineage("re_rv_"+k.nonce, nil, wallet, k.spendReq, refund, k.now()); err != nil {
		return err
	}
	_, err = k.db.Chargeback("dp_rv_"+k.nonce, wallet, k.spendReq, charge, k.now())
	return err
}

// rvReadsOnA reads the key on A, so A holds it in its lookup cache.
func (k *kg5State) rvReadsOnA(acct, label string) error {
	res, err := k.call(kg5Req{b: k.b, as: "acct:" + acct, method: http.MethodGet, path: "/account/keys/" + k.keyID(label)})
	if err != nil {
		return err
	}
	if res.code != http.StatusOK {
		return fmt.Errorf("GET %s on A = %d: %.200s", label, res.code, res.body)
	}
	return nil
}

func (k *kg5State) rvSharedBack() error {
	if err := k.mr.Restart(); err != nil {
		return err
	}
	k.mrClosed = false
	return nil
}

func (k *kg5State) rvEventually401OnB(secs int, label, code string) error {
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for {
		if err := k.bearerOnB(label); err != nil {
			return err
		}
		if k.statusCode(http.StatusUnauthorized, code) == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("after %ds a relay bearing %s on B still answers %d: %.200s", secs, label, k.resp.code, k.resp.body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---- unknown keys from one address (push audit 2026-10-06) -----------------------------------

// rvHashCounter counts the key-store lookups by secret hash.
type rvHashCounter struct {
	store.Store
	mu sync.Mutex
	n  int
}

func (c *rvHashCounter) AccountKeyByHash(h string) (store.AccountKey, bool, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Store.AccountKeyByHash(h)
}

func (k *kg5State) registerUnknownKeys(sc *godog.ScenarioContext) {
	sc.Step(`^10,000 relays from one address each bear a different unknown key$`, k.rvUnknownKeys)
	sc.Step(`^the relays past the per-address limit for unknown keys are 429 with Retry-After$`, k.rvUnknownLimited)
	sc.Step(`^at most that limit's burst of them reach the key store$`, k.rvUnknownLookups)
	sc.Step(`^the broker keeps no lookup entry for any of them$`, k.rvUnknownNoCache)
	sc.Step(`^a relay bearing a valid key from another address is served$`, k.rvValidElsewhere)
}

func (k *kg5State) rvUnknownKeys() error {
	counter := &rvHashCounter{Store: k.b.db}
	saved := k.b.db
	k.b.db = counter
	defer func() { k.b.db = saved }()
	body := k.relayBody(k.model, false, nil)
	k.codes, k.rvRetryAfter = nil, 0
	for i := 0; i < 10000; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", fmt.Sprintf("Bearer rog-key_unknown%064d", i))
		r.Header.Set("CF-Connecting-IP", "203.0.113.7")
		rr := httptest.NewRecorder()
		k.b.routes().ServeHTTP(rr, r)
		k.codes = append(k.codes, rr.Code)
		if rr.Code == http.StatusTooManyRequests && rr.Header().Get("Retry-After") != "" {
			k.rvRetryAfter++
		}
	}
	k.rvLookups = counter.n
	return nil
}

func (k *kg5State) rvUnknownLimited() error {
	limited := 0
	for _, c := range k.codes {
		switch c {
		case http.StatusTooManyRequests:
			limited++
		case http.StatusUnauthorized:
		default:
			return fmt.Errorf("an unknown key got %d, want 401 or 429", c)
		}
	}
	if limited < 9000 {
		return fmt.Errorf("only %d of 10,000 unknown-key relays were rate limited", limited)
	}
	if k.rvRetryAfter != limited {
		return fmt.Errorf("%d of %d 429s carried Retry-After", k.rvRetryAfter, limited)
	}
	return nil
}

func (k *kg5State) rvUnknownLookups() error {
	burst := int(k.b.keyMissLimiter().burst)
	if k.rvLookups > burst+1 {
		return fmt.Errorf("%d unknown keys reached the key store, want at most the burst of %d", k.rvLookups, burst)
	}
	return nil
}

func (k *kg5State) rvUnknownNoCache() error {
	k.b.ak.mu.Lock()
	defer k.b.ak.mu.Unlock()
	for h, c := range k.b.ak.byHash {
		if !c.found {
			return fmt.Errorf("the broker cached a miss (%d entries, e.g. %.12s...)", len(k.b.ak.byHash), h)
		}
	}
	return nil
}

func (k *kg5State) rvValidElsewhere() error {
	if err := k.mintWith("acct-a", "k-valid", "defaults"); err != nil {
		return err
	}
	k.ensurePriced()
	if err := k.keyRelayOn(k.b, "k-valid", k.model, false, nil, nil); err != nil {
		return err
	}
	return k.status(200)
}

// ---- key-management attempts bearing a key (push audit 2026-10-06) --------------------------

func (k *kg5State) registerMgmtBearer(sc *godog.ScenarioContext) {
	sc.Step(`^a request bearing "([^"]+)" PATCHes /account/keys/k1 (\d+) times from one address$`, k.rvMgmtBearer)
	sc.Step(`^the attempts past the per-address limit are 429 with Retry-After$`, k.rvMgmtLimited)
	sc.Step(`^at most that limit's burst of "denied" audit rows are written$`, k.rvMgmtAudits)
}

func (k *kg5State) rvMgmtBearer(label string, n int) error {
	secret, err := k.secretOf(label)
	if err != nil {
		return err
	}
	k.codes, k.rvRetryAfter = nil, 0
	body := []byte(`{"name":"x"}`)
	for i := 0; i < n; i++ {
		r := httptest.NewRequest(http.MethodPatch, "/account/keys/"+k.keyID(label), bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("CF-Connecting-IP", "203.0.113.9")
		rr := httptest.NewRecorder()
		k.b.routes().ServeHTTP(rr, r)
		k.codes = append(k.codes, rr.Code)
		if rr.Code == http.StatusTooManyRequests && rr.Header().Get("Retry-After") != "" {
			k.rvRetryAfter++
		}
	}
	return nil
}

func (k *kg5State) rvMgmtLimited() error {
	limited := 0
	for _, c := range k.codes {
		switch c {
		case http.StatusTooManyRequests:
			limited++
		case http.StatusForbidden:
		default:
			return fmt.Errorf("a key-management attempt with a key got %d, want 403 or 429", c)
		}
	}
	if limited == 0 || k.rvRetryAfter != limited {
		return fmt.Errorf("%d attempts were 429 (%d with Retry-After)", limited, k.rvRetryAfter)
	}
	return nil
}

func (k *kg5State) rvMgmtAudits() error {
	wallet, _ := k.walletOf("acct-a")
	rows, err := k.keyEvents(wallet)
	if err != nil {
		return err
	}
	denied := 0
	for _, r := range rows {
		if strings.Contains(r.Ref, "action=denied") {
			denied++
		}
	}
	if burst := int(k.b.keyMissLimiter().burst); denied > burst {
		return fmt.Errorf("%d denied audit rows were written, want at most the burst of %d", denied, burst)
	}
	return nil
}

// ---- a key's use is recorded after admission (push audit 2026-10-06) ------------------------

func (k *kg5State) registerTouch(sc *godog.ScenarioContext) {
	sc.Step(`^"([^"]+)" mints "([^"]+)" on A and the per-identity limit admits one relay$`, func(acct, label string) error {
		if err := k.mintsOnA(acct, label); err != nil {
			return err
		}
		k.ensurePriced()
		k.b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
		return nil
	})
	sc.Step(`^two relays bearing "([^"]+)" arrive back to back$`, func(label string) error {
		k.rvCodes = nil
		for i := 0; i < 2; i++ {
			if err := k.keyRelayOn(k.b, label, k.model, false, nil, nil); err != nil {
				return err
			}
			k.rvCodes = append(k.rvCodes, k.resp.code)
			if i == 0 {
				e, err := k.entry(label)
				if err != nil {
					return err
				}
				k.rvFirstUsed = e["last_used"]
			}
		}
		return nil
	})
	sc.Step(`^the second is 429 and "([^"]+)" shows 1 request and the first relay's last_used$`, func(label string) error {
		if len(k.rvCodes) != 2 || k.rvCodes[0] != http.StatusOK || k.rvCodes[1] != http.StatusTooManyRequests {
			return fmt.Errorf("the two relays answered %v, want [200 429]", k.rvCodes)
		}
		e, err := k.entry(label)
		if err != nil {
			return err
		}
		if n, _ := kg5Num(e["requests"]); n != 1 || e["last_used"] != k.rvFirstUsed {
			return fmt.Errorf("%s shows requests=%v last_used=%v after a 429, want 1 and %v", label, e["requests"], e["last_used"], k.rvFirstUsed)
		}
		return nil
	})
}
