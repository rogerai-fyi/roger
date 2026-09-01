package main

// Executable spec: features/curated/curated_tower.feature, the @failover scenarios.
//
// Both are PARITY PINS: the broker's strike and failover machinery must treat a curated
// station as one more station on the band - no exemption (a proxy that could fail without
// strikes would be a safe place to be unreliable) and no exile (a live proxy is a valid
// retry target). The "tower-curated" wording covers the same door: a station attached
// behind a tower registers through the identical register/strike/pick spine.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type curFailState struct {
	t          *testing.T
	b          *broker
	userPriv   ed25519.PrivateKey
	nodePriv   ed25519.PrivateKey
	nodePubHex string
	failedNode string
}

func (s *curFailState) reset() {
	s.b, s.userPriv, s.nodePriv, s.nodePubHex = newBandBroker(s.t)
	s.b.trust = map[string]trustState{}
	s.b.inflight = map[string]int{}
	s.b.success = map[string]float64{}
	// The REAL strike ladder (main() wires these; a zero ban threshold would ban the
	// owner on the first strike and exile every station they run - not the ladder).
	s.b.strikeWarnAt, s.b.strikeBanAt = strikeWarnAt(), strikeBanAt()
	s.b.strikeCorroborateKinds, s.b.strikeDecayDays = strikeCorroborateKinds(), strikeDecayDays()
	s.failedNode = ""
}

func (s *curFailState) reg(id string, curated bool) error {
	reg := protocol.NodeRegistration{
		NodeID: id, PubKey: s.nodePubHex, BridgeToken: "tok", TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "gpt-oss-20b", Ctx: 8192}},
	}
	if curated {
		reg.Curated, reg.CuratedProvider = true, "openrouter"
	}
	reg.SignRegistration(s.nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("register %s = %d: %s", id, w.Code, w.Body.String())
	}
	s.b.recordProbe(id, probePass, 50, 40, true, true)
	return nil
}

func (s *curFailState) bandWithBoth() error {
	if err := s.reg("h1", false); err != nil {
		return err
	}
	return s.reg("c1", true)
}

func (s *curFailState) upstreamRefuses() error {
	// The relay's no-usable-output signal, exactly as the serve path raises it when a
	// station returns an error/empty body - here the curated station's upstream 502s.
	rec := protocol.UsageReceipt{RequestID: "req-fail-1", NodeID: "c1", Model: "gpt-oss-20b", PromptTokens: 10}
	s.b.flagEmptyOutput("c1", rec, http.StatusBadGateway)
	s.failedNode = "c1"
	return nil
}

func (s *curFailState) standardStrikeApplies() error {
	// The strike ledger holds the SAME evidence-bound empty-output strike a human
	// station earns for the same signal - recorded against the curated node's account.
	acct, _ := s.b.ownerOf("c1")
	rows, err := s.b.db.StrikesByOwner(acct, 10)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Kind == store.StrikeEmptyOutput {
			return nil
		}
	}
	return fmt.Errorf("no empty-output strike recorded against the curated station (rows: %d)", len(rows))
}

func (s *curFailState) retryFollowsNormalRule() error {
	// The failover rule: retry excludes the struck station, everything else competes.
	n, _, ok := s.b.pickFor("gpt-oss-20b", false, 0, 0, 0, "", map[string]bool{s.failedNode: true}, nil, nil, pickReq{rng: seededRand("fail-retry")})
	if !ok {
		return fmt.Errorf("no retry pick with a healthy station on the band")
	}
	if n.NodeID != "h1" {
		return fmt.Errorf("retry landed on %s, want the surviving human station", n.NodeID)
	}
	return nil
}

func (s *curFailState) humanFailsMidRequest() error {
	s.failedNode = "h1"
	return nil
}

func (s *curFailState) retryMayLandCurated() error {
	n, _, ok := s.b.pickFor("gpt-oss-20b", false, 0, 0, 0, "", map[string]bool{"h1": true}, nil, nil, pickReq{rng: seededRand("fail-retry-2")})
	if !ok {
		return fmt.Errorf("no retry pick: the curated station was exiled from failover")
	}
	if n.NodeID != "c1" {
		return fmt.Errorf("retry landed on %s, want the curated station (the only one left)", n.NodeID)
	}
	return nil
}

func (s *curFailState) receiptNamesStation() error {
	// The receipt spine names the SERVING node and stamps the curated designation, so a
	// failover to a proxy is visible in the consumer's history, never blurred.
	rec := protocol.UsageReceipt{RequestID: "req-x", NodeID: "c1", Model: "gpt-oss-20b"}
	rec.Curated = s.b.nodeCurated(rec.NodeID)
	if rec.NodeID != "c1" || !rec.Curated {
		return fmt.Errorf("the receipt does not name the curated server: node=%s curated=%v", rec.NodeID, rec.Curated)
	}
	return nil
}

func TestCuratedTowerFailoverFeature(t *testing.T) {
	st := &curFailState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^a band with a tower-curated station and a human station$`, st.bandWithBoth)
			sc.Step(`^the tower's upstream refuses a request$`, st.upstreamRefuses)
			sc.Step(`^the standard empty-output strike applies$`, st.standardStrikeApplies)
			sc.Step(`^the retry follows the normal failover rule$`, st.retryFollowsNormalRule)
			sc.Step(`^a band with a human station and a curated station$`, st.bandWithBoth)
			sc.Step(`^the human station fails mid-request$`, st.humanFailsMidRequest)
			sc.Step(`^the retry may land on the curated station$`, st.retryMayLandCurated)
			sc.Step(`^the receipt names which station served$`, st.receiptNamesStation)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@failover",
			Paths: []string{"../../features/curated/curated_tower.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the @failover curated tower scenarios failed")
	}
}
