package edge_test

// Executable spec: features/lan/trust_and_honesty.feature (@edge). LAN mode trusts the LAN for
// Towers only; Edge members are verified exactly as without it. These scenarios run the REAL Edge
// discovery (the same peers, certificates and VerifyPeer as features/edge/discovery.feature) in a
// process with ROGER_LAN=1, and LAN mode's dial reads only what that discovery admitted.

import (
	"context"
	"rogerai.fm/roger/v6/internal/bddrun"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
)

func TestLANTrustEdgeBDD(t *testing.T) {
	st := &discState{t: t}
	var scenario string
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, s *godog.Scenario) (context.Context, error) {
				scenario = s.Name
				st.reset()
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if st.disc != nil {
					st.disc.Stop()
					st.disc = nil
				}
				for _, p := range st.peers {
					p.stop()
				}
				if st.alpha != nil {
					st.alpha.stop()
				}
				return c, nil
			})
			sc.Step(`^LAN mode is on$`, func() error {
				t.Setenv("ROGER_LAN", "1")
				if err := st.instanceEnrolled("alpha", "acct-1"); err != nil {
					return err
				}
				return st.discoveryEnabled()
			})
			sc.Step(`^the Edge member "([^"]*)" presents a certificate whose fingerprint does not match its advertisement$`, func(string) error {
				return st.peerAdvertisingWrongFingerprint()
			})
			sc.Step(`^an advertisement claims the node id of "([^"]*)" with a different certificate$`, func(node string) error {
				if err := st.verifiedNodeAtOneAddress(node); err != nil {
					return err
				}
				return st.attackerAtDifferentAddress()
			})
			sc.Step(`^a peer on the LAN is enrolled to a different account and serves "([^"]*)"$`, func(string) error {
				return st.strangerWithValidCert("stranger", "acct-2")
			})
			sc.Step(`^the dial is read$`, func() error {
				switch scenario {
				case "a spoofed Edge advertisement is still refused in LAN mode":
					return st.browsesAndDialsTheAttacker("alpha")
				case "a peer enrolled to a different account is never a source in LAN mode":
					return st.alphaBrowses("alpha")
				}
				return st.browsesAndDialsIt("alpha")
			})
			sc.Step(`^"([^"]*)" is not listed as a source$`, func(string) error { return st.peerNotInFleet() })
			sc.Step(`^the refusal reason is the same as features/edge/discovery\.feature gives$`, func() error {
				return st.reportedWithBothFingerprints(edge.ReasonFingerprintMismatch)
			})
			sc.Step(`^the spoofed record is not a source$`, st.attackerFailsVerification)
			sc.Step(`^"([^"]*)"'s real record is unaffected$`, st.keepsItsPlace)
			sc.Step(`^it is not listed as a source$`, func() error {
				if err := st.namedPeerNotInFleet("stranger"); err != nil {
					return err
				}
				return st.notOfferedAsCandidate()
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: []string{
			"../../features/lan/trust_and_honesty.feature:74",
			"../../features/lan/trust_and_honesty.feature:81",
			"../../features/lan/trust_and_honesty.feature:88",
		}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("LAN trust (Edge) scenarios failed")
	}
}
