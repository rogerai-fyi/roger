package main

// key_state_audit_bdd_test.go: the steps for the "# state audit 2026-10-05" scenarios in
// features/auth/key_guardrails.feature and features/relay/key_limits.feature. Each one runs
// the per-account key rules on TWO brokers over the same stores (instanceB), so a rule kept in
// one instance's memory fails here.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cucumber/godog"
)

func (k *kg5State) registerStateAudit(sc *godog.ScenarioContext) {
	sc.Step(`^"([^"]+)" mints on A and on B at the same moment$`, func(acct string) error { return k.saMintRace(acct, "") })
	sc.Step(`^"([^"]+)" mints with "Idempotency-Key: ([^"]+)" on A and on B at the same moment$`, k.saMintRace)
	sc.Step(`^exactly one mint is 201 and the other is 400 "([^"]+)"$`, func(code string) error { return k.saRaceOutcome(400, code) })
	sc.Step(`^exactly one mint is 201 and the other is 409 "already_minted" naming the first key's id$`, func() error { return k.saRaceOutcome(409, "already_minted") })
	sc.Step(`^"([^"]+)" holds exactly (\d+) live keys?$`, k.saHoldsExactly)
	sc.Step(`^"([^"]+)" POSTs /account/keys (\d+) times in one minute, alternating between A and B$`, k.saMintBurstAB)
	sc.Step(`^at most (\d+) keys were created across both instances$`, k.saAtMostCreated)
	sc.Step(`^the shared store goes down$`, k.saSharedDown)
	sc.Step(`^the next relay bearing "([^"]+)" on B is 401 "([^"]+)" \(never served from a stale cache\)$`, k.saNextOnB401)
	sc.Step(`^a second instance shares the store, with its own mailer$`, k.saSecondWithMailer)
	sc.Step(`^"([^"]+)" crosses 80% and then 100% in one window on A$`, k.kl5Crosses)
	sc.Step(`^a relay bearing "([^"]+)" on B is 402 key_limit in the same window$`, k.saOnB402)
}

// saMintRace mints for acct on A; between A's checks and its write, the same mint runs on B.
func (k *kg5State) saMintRace(acct, idem string) error {
	if k.b2 == nil {
		k.instanceB()
	}
	var hdr map[string]string
	if idem != "" {
		hdr = map[string]string{"Idempotency-Key": idem}
	}
	body := []byte(`{"name":"race"}`)
	a := k.b
	var bRes kg5Resp
	var bErr error
	fired := false
	keyMintGapForTest = func(b *broker) {
		if b != a || fired {
			return
		}
		fired = true
		bRes, bErr = k.call(kg5Req{b: k.b2, as: "acct:" + acct, method: http.MethodPost, path: "/account/keys", body: body, hdr: hdr})
	}
	defer func() { keyMintGapForTest = nil }()
	aRes, err := k.call(kg5Req{b: a, as: "acct:" + acct, method: http.MethodPost, path: "/account/keys", body: body, hdr: hdr})
	if err != nil {
		return err
	}
	if bErr != nil {
		return bErr
	}
	if !fired {
		return fmt.Errorf("A's mint never reached its write (A answered %d: %.200s)", aRes.code, aRes.body)
	}
	k.resps = []kg5Resp{aRes, bRes}
	return nil
}

func (k *kg5State) saRaceOutcome(other int, code string) error {
	if len(k.resps) != 2 {
		return fmt.Errorf("%d race outcomes, want 2", len(k.resps))
	}
	var won, lost *kg5Resp
	for i := range k.resps {
		switch k.resps[i].code {
		case http.StatusCreated:
			if won != nil {
				return fmt.Errorf("both mints answered 201 (ids %v and %v)", won.js["id"], k.resps[i].js["id"])
			}
			won = &k.resps[i]
		default:
			lost = &k.resps[i]
		}
	}
	if won == nil || lost == nil {
		return fmt.Errorf("want one 201 and one %d, got %d and %d", other, k.resps[0].code, k.resps[1].code)
	}
	if lost.code != other {
		return fmt.Errorf("the losing mint answered %d, want %d: %.300s", lost.code, other, lost.body)
	}
	k.resp = *lost
	if err := k.statusCode(other, code); err != nil {
		return err
	}
	if other == http.StatusConflict {
		if id, _ := lost.js["id"].(string); id == "" || id != won.js["id"] {
			return fmt.Errorf("the 409 names %q, want the first key's id %v", id, won.js["id"])
		}
	}
	return nil
}

func (k *kg5State) saHoldsExactly(acct string, n int) error {
	got, err := k.count(acct)
	if err != nil {
		return err
	}
	if got != n {
		return fmt.Errorf("%s holds %d live keys, want %d", acct, got, n)
	}
	return nil
}

func (k *kg5State) saMintBurstAB(acct string, n int) error {
	if k.b2 == nil {
		k.instanceB()
	}
	k.resps = nil
	for i := 0; i < n; i++ {
		b := k.b
		if i%2 == 1 {
			b = k.b2
		}
		if _, err := k.call(kg5Req{b: b, as: "acct:" + acct, method: http.MethodPost, path: "/account/keys", body: []byte(fmt.Sprintf(`{"name":"ab%d"}`, i))}); err != nil {
			return err
		}
	}
	return nil
}

func (k *kg5State) saAtMostCreated(max int) error {
	created := 0
	for _, r := range k.resps {
		if r.code == http.StatusCreated {
			created++
		}
	}
	if created > max {
		return fmt.Errorf("%d keys were created across both instances, want at most %d", created, max)
	}
	return nil
}

func (k *kg5State) saSharedDown() error {
	k.mr.Close()
	k.mrClosed = true
	return nil
}

func (k *kg5State) saNextOnB401(label, code string) error {
	if err := k.bearerOnB(label); err != nil {
		return err
	}
	return k.statusCode(http.StatusUnauthorized, code)
}

func (k *kg5State) saSecondWithMailer() error {
	k.instanceB()
	k.b2.mail = k.kl5CaptureMailer()
	return nil
}

func (k *kg5State) saOnB402(label string) error {
	if err := k.bearerOnB(label); err != nil {
		return err
	}
	if k.resp.code != http.StatusPaymentRequired || !strings.Contains(string(k.resp.body), "key_limit") {
		return fmt.Errorf("the relay on B answered %d, want 402 key_limit: %.300s", k.resp.code, k.resp.body)
	}
	return nil
}
