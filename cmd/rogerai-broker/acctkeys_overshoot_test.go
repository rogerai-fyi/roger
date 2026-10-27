package main

// acctkeys_overshoot_test.go pins ZERO key-limit overshoot (contract §11) when the shared
// per-key lock is no longer held by the request that took it: a check-and-hold that outlives
// the lock's TTL, while another instance takes the lock, checks and holds. The race is made
// deterministic with two seams: the lock's lifetime is forced to lapse (miniredis
// fast-forward, so no sleeping), and keyHoldGapForTest runs instance B's whole relay in the
// window between instance A's key pre-check and A's hold. It drives the REAL brokers, the
// real money store (Postgres when ROGERAI_TEST_DATABASE_URL is set) and the shared store, on
// the key-limits harness (features/relay/key_limits.feature's Background).

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

const kofFeature = `Feature: A lapsed key lock never lets a key overshoot

  Background:
    Given a broker with the money store and the shared store wired
    And the fee rate is 10%
    And account "acct-a" is logged in (wallet "u_gh_a") with balance $20.00 and no monthly cap
    And "acct-a" minted key "k1" with limit_usd 5.00 and reset "monthly"
    And node "n1" is on air for "qwen3-32b" at $1.00/1M in, $2.00/1M out, proven live
    And node "n2" is on air for "qwen3-32b" at $2.00/1M in, $4.00/1M out, proven live
    And a relay for "qwen3-32b" with max_tokens 1000 holds $0.003 on "n1" and $0.006 on "n2"

  Scenario: Instance A's lock lapses between its key check and its hold while B serves
    Given two instances share the store and "k1" has remaining $0.003
    When a relay bearing "k1" on instance A passes its key check, its lock lapses, and a relay bearing "k1" on instance B runs before A holds
    Then exactly one of the two relays is served and the other is 402 key_limit
    And "k1" has spent no more than its limit and holds no reservation

  Scenario: Releasing a lapsed lock never frees another holder's lock
    Given two instances share the store and "k1" has remaining $0.003
    When instance A takes the key lock, the lock lapses, and instance B takes it
    And instance A releases its lock
    Then instance B still holds the key lock
`

type kofState struct {
	*kg5State
	resA, resB  kg5Resp
	releaseA    func()
	lockedB     bool
	lockTTLSave time.Duration
}

func (k *kofState) raceThroughTheGap(label string) error {
	if k.b2 == nil {
		k.instanceB()
	}
	for id, n := range k.b.nodes {
		k.b2.nodes[id] = n
		k.b2.tunnels[id] = k.b.tunnels[id]
		k.b2.trust[id] = k.b.trust[id]
	}
	k.kl5Cost("n1", 0.003)
	body := k.relayBody(k.model, false, nil)
	pin := map[string]string{"X-Roger-Node": k.st("n1").id}
	var once sync.Once
	var errB error
	keyHoldGapForTest = func(b *broker) {
		if b != k.b {
			return
		}
		once.Do(func() {
			// A has passed its pre-check and still holds the lock; let the lock lapse, then
			// let B take it, check, hold, serve and settle before A places its hold.
			k.mr.FastForward(2 * acctKeyLockTTL)
			k.resB, errB = k.relayRaw(k.b2, label, body, pin)
		})
	}
	defer func() { keyHoldGapForTest = nil }()
	var errA error
	k.resA, errA = k.relayRaw(k.b, label, body, pin)
	if errA != nil {
		return errA
	}
	return errB
}

func (k *kofState) exactlyOneServed() error {
	codes := []int{k.resA.code, k.resB.code}
	served, refused := 0, 0
	for _, r := range []kg5Resp{k.resA, k.resB} {
		switch {
		case r.code == 200:
			served++
		case r.code == 402 && strings.Contains(string(r.body), `"key_limit"`):
			refused++
		}
	}
	if served != 1 || refused != 1 {
		return fmt.Errorf("codes A=%d B=%d: %d served, %d refused for key_limit; want exactly 1 and 1 (A body %s | B body %s)",
			codes[0], codes[1], served, refused, k.resA.body, k.resB.body)
	}
	return nil
}

func (k *kofState) withinLimit(label string) error {
	id := k.keyID(label)
	reserved, err := k.b.db.KeyReserved(id)
	if err != nil {
		return err
	}
	spent, err := k.b.db.KeySpend(id, 0, 0)
	if err != nil {
		return err
	}
	if reserved > 1e-9 {
		return fmt.Errorf("key %s still holds a reservation of $%.6f", label, reserved)
	}
	if spent > 5.00+1e-9 {
		return fmt.Errorf("key %s spent $%.6f, over its $5.00 limit", label, spent)
	}
	return nil
}

func (k *kofState) aTakesThenLapsesThenBTakes() error {
	if k.b2 == nil {
		k.instanceB()
	}
	id := k.keyID("k1")
	rel, err := k.b.keyLock(id)
	if err != nil {
		return err
	}
	k.releaseA = rel
	k.mr.FastForward(2 * acctKeyLockTTL)
	relB, err := k.b2.keyLock(id)
	if err != nil {
		return err
	}
	_ = relB // B keeps the lock for the Then
	k.lockedB = true
	return nil
}

func (k *kofState) aReleases() error {
	if k.releaseA == nil {
		return fmt.Errorf("instance A holds no lock to release")
	}
	k.releaseA()
	return nil
}

func (k *kofState) bStillHolds() error {
	if !k.lockedB {
		return fmt.Errorf("instance B never took the lock")
	}
	ok, err := k.b.shared.setIfAbsent("acctkeylock:"+k.keyID("k1"), "probe", acctKeyLockTTL)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("the key lock was free after A's release: A's late release deleted B's lock")
	}
	return nil
}

func TestKeyLimitNeverOvershootsWhenTheLockLapses(t *testing.T) {
	k := &kofState{kg5State: newKG5(t), lockTTLSave: acctKeyLockTTL}
	prev := log.Writer()
	log.SetOutput(k.logs)
	var once sync.Once
	t.Cleanup(func() {
		log.SetOutput(prev)
		once.Do(k.teardown)
		acctKeyLockTTL = k.lockTTLSave
	})
	suite := godog.TestSuite{
		Name: "key_lock_lapse",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				k.resA, k.resB, k.releaseA, k.lockedB = kg5Resp{}, kg5Resp{}, nil, false
				return ctx, k.reset()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				k.teardown()
				return ctx, nil
			})
			k.registerLimits(sc)
			sc.Step(`^a relay bearing "([^"]+)" on instance A passes its key check, its lock lapses, and a relay bearing "[^"]+" on instance B runs before A holds$`, k.raceThroughTheGap)
			sc.Step(`^exactly one of the two relays is served and the other is 402 key_limit$`, k.exactlyOneServed)
			sc.Step(`^"([^"]+)" has spent no more than its limit and holds no reservation$`, k.withinLimit)
			sc.Step(`^instance A takes the key lock, the lock lapses, and instance B takes it$`, k.aTakesThenLapsesThenBTakes)
			sc.Step(`^instance A releases its lock$`, k.aReleases)
			sc.Step(`^instance B still holds the key lock$`, k.bStillHolds)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			FeatureContents: []godog.Feature{{Name: "key_lock_lapse.feature", Contents: []byte(kofFeature)}},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("key lock lapse scenarios failed")
	}
}
