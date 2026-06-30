package main

// rate_limits_bdd_test.go makes features/multinode/rate_limits.feature EXECUTABLE, driving
// the REAL buildBroker limiter wiring over TWO broker instances sharing ONE in-process Valkey
// (miniredis). No mocks: it exercises the same b.rl/b.grantRL + shared rateAllow path as the
// live broker. The invariant: a per-identity / per-grant budget is ONE shared limit across
// instances — a key exhausted on A is denied on B (not handed a fresh bucket = 2x leak).

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/store"

	"github.com/cucumber/godog"
)

type rlState struct {
	t    *testing.T
	mr   *miniredis.Miniredis
	a, b *broker
}

func (s *rlState) reset(t *testing.T) { s.t = t; s.mr, s.a, s.b = nil, nil, nil }

// buildPair builds two REAL brokers on one shared miniredis (multi-instance), with the
// per-identity burst set via env so the budget is tiny + deterministic.
func (s *rlState) buildPair(burst string) {
	s.mr = miniredis.RunT(s.t)
	s.t.Setenv("ROGERAI_REDIS_URL", "redis://"+s.mr.Addr())
	s.t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	s.t.Setenv("ROGERAI_RATE_RPM", "60") // 1 token/sec refill: negligible within the test
	s.t.Setenv("ROGERAI_RATE_BURST", burst)
	_, priv, _ := ed25519.GenerateKey(nil)
	s.a = buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	s.b = buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	s.t.Cleanup(func() {
		if s.a.shared != nil {
			_ = s.a.shared.Close()
		}
		if s.b.shared != nil {
			_ = s.b.shared.Close()
		}
	})
}

// --- Given -----------------------------------------------------------------

func (s *rlState) pairIdentityBurst2() error { s.buildPair("2"); return nil }
func (s *rlState) pairDefault() error        { s.buildPair("2"); return nil }

func (s *rlState) oneSharedBroker() error {
	mr := miniredis.RunT(s.t)
	s.t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	s.t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	s.a = buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	s.t.Cleanup(func() {
		if s.a.shared != nil {
			_ = s.a.shared.Close()
		}
	})
	return nil
}

// --- When ------------------------------------------------------------------

// exhaust consumes the identity limiter on A until it denies (bounded), proving the bucket
// is empty afterwards.
func (s *rlState) identityExhaustsOnA(id string) error {
	denied := false
	for i := 0; i < 20; i++ {
		if ok, _ := s.a.rl.allow(id); !ok {
			denied = true
			break
		}
	}
	if !denied {
		return fmt.Errorf("identity %q never hit its limit on A within 20 tries (burst too high?)", id)
	}
	return nil
}

func (s *rlState) grantExhaustsOnA(id string) error {
	denied := false
	for i := 0; i < 20; i++ {
		if ok, _ := s.a.grantRL.allowAt(id, 60, 2); !ok {
			denied = true
			break
		}
	}
	if !denied {
		return fmt.Errorf("grant %q never hit its limit on A within 20 tries", id)
	}
	return nil
}

// --- Then ------------------------------------------------------------------

func (s *rlState) identityDeniedOnB(id string) error {
	if ok, _ := s.b.rl.allow(id); ok {
		return fmt.Errorf("identity %q was ALLOWED on instance B after exhausting its budget on A — the per-identity limit is per-instance (2x leak)", id)
	}
	return nil
}

func (s *rlState) grantDeniedOnB(id string) error {
	if ok, _ := s.b.grantRL.allowAt(id, 60, 2); ok {
		return fmt.Errorf("grant %q was ALLOWED on instance B after exhausting its budget on A — the per-grant limit is per-instance (2x leak)", id)
	}
	return nil
}

func (s *rlState) limitersHaveNamedSharedBuckets() error {
	if s.a.rl.shared == nil || s.a.rl.name == "" {
		return fmt.Errorf("per-identity limiter has no named shared bucket (name=%q shared=%v)", s.a.rl.name, s.a.rl.shared != nil)
	}
	if s.a.grantRL.shared == nil || s.a.grantRL.name == "" {
		return fmt.Errorf("per-grant limiter has no named shared bucket (name=%q shared=%v)", s.a.grantRL.name, s.a.grantRL.shared != nil)
	}
	return nil
}

func (s *rlState) bucketNamesDistinct() error {
	if s.a.rl.name == s.a.grantRL.name {
		return fmt.Errorf("per-identity and per-grant limiters share the bucket name %q — they would collide", s.a.rl.name)
	}
	return nil
}

func TestMultinodeRateLimitsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &rlState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.Step(`^two brokers A and B built on one shared Valkey with a per-identity burst of 2$`, st.pairIdentityBurst2)
			sc.Step(`^two brokers A and B built on one shared Valkey$`, st.pairDefault)
			sc.Step(`^a broker built with a shared Valkey backend$`, st.oneSharedBroker)
			sc.Step(`^identity "([^"]*)" exhausts its request budget on instance A$`, st.identityExhaustsOnA)
			sc.Step(`^grant "([^"]*)" exhausts a burst of 2 on instance A$`, st.grantExhaustsOnA)
			sc.Step(`^the same identity "([^"]*)" is denied on instance B$`, st.identityDeniedOnB)
			sc.Step(`^the same grant "([^"]*)" is denied on instance B$`, st.grantDeniedOnB)
			sc.Step(`^its per-identity and per-grant limiters each have a named shared bucket$`, st.limitersHaveNamedSharedBuckets)
			sc.Step(`^those two bucket names are distinct$`, st.bucketNamesDistinct)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/rate_limits.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/rate_limits behavior scenarios failed (see godog output above)")
	}
}
