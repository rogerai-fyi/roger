package edge_test

// Executable spec: features/lan/edge_only.feature:250. Edge-only trusts nothing on the LAN but
// certificates: the REAL Edge discovery (the same peers, certificates and VerifyPeer as
// features/edge/discovery.feature) refuses a forged member advert in a process running edge-only.

import (
	"context"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/edge"
)

func TestLANEdgeOnlyEdgeBDD(t *testing.T) {
	st := &discState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
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
			sc.Step(`^edge-only is on$`, func() error {
				t.Setenv("ROGER_LAN_EDGE_ONLY", "1")
				if err := st.instanceEnrolled("alpha", "acct-1"); err != nil {
					return err
				}
				return st.discoveryEnabled()
			})
			sc.Step(`^a device advertises itself as the Edge member "([^"]*)" without a valid certificate$`, func(string) error {
				return st.peerAdvertisingWrongFingerprint()
			})
			sc.Step(`^the LAN dial is read$`, func() error { return st.browsesAndDialsIt("alpha") })
			sc.Step(`^the advertisement is refused for its typed reason$`, func() error {
				return st.reportedWithBothFingerprints(edge.ReasonFingerprintMismatch)
			})
			sc.Step(`^nothing is dialed on its behalf$`, func() error { return st.peerNotInFleet() })
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/lan/edge_only.feature:250"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("edge_only.feature (Edge advert) failed")
	}
}
