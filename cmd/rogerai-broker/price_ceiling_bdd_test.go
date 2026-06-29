package main

// price_ceiling_bdd_test.go makes features/pricing/price_ceiling.feature EXECUTABLE,
// driving the REAL /nodes/register path (registerWith) to pin the founder invariant: a
// GLOBAL price ceiling that NO flag exempts. An over-ceiling registration is rejected for
// a PUBLIC, a PRIVATE, AND a CONFIDENTIAL band alike; a within-ceiling one is accepted.
// This is the spec-layer companion to TestRegisterCeilingGlobalAllBands - it executes the
// (previously stale) "private bypasses the ceiling" claim and proves it false.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type ceilingState struct {
	t *testing.T

	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string

	over protocol.ModelOffer // the offer the scenario registers (set ABOVE/within the ceiling)
	code int                 // last register HTTP status
	msg  string              // last register error message
}

func (s *ceilingState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.over = protocol.ModelOffer{}
	s.code, s.msg = 0, ""
}

func (s *ceilingState) nodePricedAboveCeiling() error {
	// $250/1M out is well over the $100/1M public out-ceiling default.
	s.over = protocol.ModelOffer{Model: "m", Ctx: 4096, PriceOut: 250}
	return nil
}

func (s *ceilingState) nodePricedWithinCeiling() error {
	s.over = protocol.ModelOffer{Model: "m", Ctx: 4096, PriceOut: 5}
	return nil
}

func (s *ceilingState) registeredAs(band string) error {
	var private, confidential bool
	switch band {
	case "--private", "private":
		private = true
	case "confidential":
		confidential = true
	case "public", "a public station":
		// both false
	default:
		return fmt.Errorf("unknown band kind %q", band)
	}
	s.code, s.msg = registerWith(s.t, s.b, "n1", s.nodePriv, s.nodePubHex, s.userPriv, true, s.over, private, confidential)
	return nil
}

func (s *ceilingState) rejectedByCeiling() error {
	if s.code != 400 {
		return fmt.Errorf("over-ceiling register = %d, want 400 (no flag exempts the global ceiling); msg=%q", s.code, s.msg)
	}
	if !strings.Contains(s.msg, "ceiling") {
		return fmt.Errorf("rejection missing the ceiling copy: %q", s.msg)
	}
	return nil
}

func (s *ceilingState) accepted() error {
	if s.code != 200 {
		return fmt.Errorf("register = %d, want 200 (within-ceiling); msg=%q", s.code, s.msg)
	}
	return nil
}

func TestPriceCeilingBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &ceilingState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an owner registers a node priced ABOVE the public ceiling$`, st.nodePricedAboveCeiling)
			sc.Step(`^an owner registers a node priced WITHIN the public ceiling$`, st.nodePricedWithinCeiling)
			sc.Step(`^the node is registered with --private$`, func() error { return st.registeredAs("--private") })
			sc.Step(`^the node is registered as a public station$`, func() error { return st.registeredAs("public") })
			sc.Step(`^the node is registered with the confidential tier$`, func() error { return st.registeredAs("confidential") })
			sc.Step(`^the registration is rejected by the global ceiling$`, st.rejectedByCeiling)
			sc.Step(`^the registration is accepted$`, st.accepted)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/pricing/price_ceiling.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("price-ceiling behavior scenarios failed (see godog output above)")
	}
}
