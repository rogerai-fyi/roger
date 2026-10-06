package main

// key_review_bdd_test.go: the steps for the "# slice-5 review 2026-10-05" scenarios in
// features/auth/key_guardrails.feature. The races run on TWO brokers over the same stores
// (instanceB); the reversal scenarios settle real relays and post the refund or chargeback
// through the store, as the payment webhooks do.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cucumber/godog"
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
