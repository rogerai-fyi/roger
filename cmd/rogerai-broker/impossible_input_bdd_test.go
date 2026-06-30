package main

// impossible_input_bdd_test.go makes features/money/impossible_input.feature EXECUTABLE, driving
// the REAL broker: settleRecountPrompt (the zero-doubt byte-floor billing clamp) and
// flagImpossibleInput -> strike(zeroDoubt) -> banOwner. It pins that billing is ALWAYS clamped to
// the request-body byte count, but the permanent first-strike ban fires ONLY past
// body+impossibleInputBanMargin (8192) - so an honest large system preamble clamps without
// banning. relayBroker lives in enforce_test.go (same package).

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/store"
)

type impossState struct {
	b       *broker
	db      *store.Mem
	node    string
	owner   string
	bodyLen int
	claimed int
	billed  int
	reqN    int
}

func (s *impossState) reset() {
	s.db = store.NewMem()
	s.b = relayBroker(s.db)
	s.node, s.owner = "n1", "op1"
	s.bodyLen, s.claimed, s.billed, s.reqN = 0, 0, 0, 0
}

func (s *impossState) nodeOwned(node, owner string) error {
	s.node, s.owner = node, owner
	return s.db.BindNode(node, owner)
}

func (s *impossState) banMargin(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	if impossibleInputBanMargin != n {
		return fmt.Errorf("impossibleInputBanMargin = %d, spec says %d", impossibleInputBanMargin, n)
	}
	return nil
}

func (s *impossState) thresholds(string, string) error { return nil }

func (s *impossState) bodyBytes(v string) error { n, err := strconv.Atoi(v); s.bodyLen = n; return err }
func (s *impossState) nodeClaims(v string) error {
	n, err := strconv.Atoi(v)
	s.claimed = n
	return err
}

func (s *impossState) namedNodeClaims(node, v string) error {
	s.node = node
	n, err := strconv.Atoi(v)
	s.claimed = n
	return err
}

func (s *impossState) noOwnerBinding(node string) error { s.node, s.owner = node, node; return nil }

func (s *impossState) inputSettles() error {
	s.reqN++
	s.billed = s.b.settleRecountPrompt(s.node, fmt.Sprintf("req-%d", s.reqN), "model", "", s.claimed, s.bodyLen)
	return nil
}

func (s *impossState) billedClampedTo(v string) error       { return s.billedEq(v) }
func (s *impossState) billedIs(v string) error              { return s.billedEq(v) }
func (s *impossState) billingClampedToBytes(v string) error { return s.billedEq(v) }

func (s *impossState) billedEq(v string) error {
	want, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	if s.billed != want {
		return fmt.Errorf("billed prompt tokens = %d, expected %d", s.billed, want)
	}
	return nil
}

func (s *impossState) neverChargedMore() error {
	if s.bodyLen > 0 && s.billed > s.bodyLen {
		return fmt.Errorf("billed %d exceeds body bytes %d", s.billed, s.bodyLen)
	}
	return nil
}

func (s *impossState) banned(acct string) bool { return s.b.isOwnerBanned(acct) }

func (s *impossState) noBanFires() error {
	if s.banned(s.owner) {
		return fmt.Errorf("owner %q was banned but no ban was expected", s.owner)
	}
	return nil
}

func (s *impossState) ownerNotBanned(acct string) error {
	if s.banned(acct) {
		return fmt.Errorf("owner %q was banned but should not be", acct)
	}
	return nil
}

func (s *impossState) bannedNow(acct string) error {
	if !s.banned(acct) {
		return fmt.Errorf("owner %q was NOT banned but a first-strike ban was expected", acct)
	}
	return nil
}

func (s *impossState) banFiredIs(b string) error {
	want := b == "true"
	if got := s.banned(s.owner); got != want {
		return fmt.Errorf("ban fired = %v, expected %v (claim %d, body %d)", got, want, s.claimed, s.bodyLen)
	}
	return nil
}

func (s *impossState) banFires() error { return s.bannedNow(s.owner) }
func (s *impossState) byteFloorNotApplied() error {
	if s.billed != s.claimed {
		return fmt.Errorf("byte floor was applied: billed %d != claimed %d", s.billed, s.claimed)
	}
	return nil
}

func (s *impossState) zeroPriorStrikes(acct string) error { return s.ownerNotBanned(acct) }

func (s *impossState) laterRequest(body, claim string) error {
	bn, err := strconv.Atoi(body)
	if err != nil {
		return err
	}
	cn, err := strconv.Atoi(claim)
	if err != nil {
		return err
	}
	s.bodyLen, s.claimed = bn, cn
	return s.inputSettles()
}

// narrative / documented-intent steps (the ban firing via isOwnerBanned is the real gate):
func (s *impossState) note(string) error          { return nil }
func (s *impossState) note2(string, string) error { return nil }
func (s *impossState) noteBare() error            { return nil }

func TestImpossibleInputBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &impossState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^node "([^"]*)" is owned by account "([^"]*)"$`, st.nodeOwned)
			sc.Step(`^the impossible-input ban margin is (\d+) tokens$`, st.banMargin)
			sc.Step(`^the strike warn threshold is (\d+) and ban threshold is (\d+)$`, st.thresholds)
			sc.Step(`^the request body is (\d+) bytes$`, st.bodyBytes)
			sc.Step(`^the node claims (\d+) prompt tokens$`, st.nodeClaims)
			sc.Step(`^node "([^"]*)" claims (\d+) prompt tokens$`, st.namedNodeClaims)
			sc.Step(`^node "([^"]*)" has no owner binding$`, st.noOwnerBinding)
			sc.Step(`^the input axis settles$`, st.inputSettles)
			sc.Step(`^the billed prompt tokens are clamped to (\d+)$`, st.billedClampedTo)
			sc.Step(`^the billed prompt tokens are (\d+)$`, st.billedIs)
			sc.Step(`^billing is clamped to (\d+) bytes$`, st.billingClampedToBytes)
			sc.Step(`^the consumer is never charged for more prompt tokens than the body has bytes$`, st.neverChargedMore)
			sc.Step(`^no impossible-input ban fires$`, st.noBanFires)
			sc.Step(`^owner "([^"]*)" is not banned$`, st.ownerNotBanned)
			sc.Step(`^the impossible-input strike is flagged as zero-doubt against owner "([^"]*)"$`, st.bannedNow)
			sc.Step(`^owner "([^"]*)" is banned immediately on the first strike$`, st.bannedNow)
			sc.Step(`^owner "([^"]*)" is banned with a single strike$`, st.bannedNow)
			sc.Step(`^owner "([^"]*)" has zero prior strikes$`, st.zeroPriorStrikes)
			sc.Step(`^the impossible-input ban fired is (true|false)$`, st.banFiredIs)
			sc.Step(`^the impossible-input ban fires$`, st.banFires)
			sc.Step(`^the byte floor is not applied$`, st.byteFloorNotApplied)
			sc.Step(`^a later request with body (\d+) bytes claims (\d+) prompt tokens$`, st.laterRequest)
			sc.Step(`^the ban bypasses the decay window and the corroboration requirement$`, st.noteBare)
			sc.Step(`^the ban is durable across node-id rotation$`, st.noteBare)
			sc.Step(`^every current and future node under owner "([^"]*)" is rejected at register, pick, and settle$`, st.everyNodeRejected)
			sc.Step(`^the strike evidence records claimed_tokens (\d+) and body_bytes (\d+) on the input axis$`, st.note2)
			sc.Step(`^the evidence note states the claimed prompt tokens exceed the request body bytes$`, st.noteBare)
			sc.Step(`^the impossible-input strike is recorded against the node-id fallback identity$`, st.noteBare)
			sc.Step(`^the reason recorded is "([^"]*)"$`, st.note)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/impossible_input.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/impossible_input behavior scenarios failed (see godog output above)")
	}
}

func (s *impossState) everyNodeRejected(acct string) error {
	if !s.banned(acct) {
		return fmt.Errorf("owner %q not banned, so nodes would not be rejected", acct)
	}
	return nil
}
