package main

// Makes features/operator/stations_dashboard.feature executable against the REAL
// /stations handler, the real account bindings, and the real store - so the
// authorization and privacy claims are asserted, not asserted-about.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
)

type stState struct {
	b        *broker
	db       *store.Mem
	ownerPub ed25519.PublicKey
	ownerKey ed25519.PrivateKey
	code     int
	body     map[string]any
	raw      string
	noCreds  bool
}

func (s *stState) reset() {
	s.db = store.NewMem()
	s.b = relayBroker(s.db)
	s.ownerPub, s.ownerKey, _ = ed25519.GenerateKey(nil)
	s.code, s.body, s.raw = 0, nil, ""
	s.noCreds = false
	_ = s.db.BindOwner(store.Owner{GitHubID: 4242, Login: "op", Pubkey: hex.EncodeToString(s.ownerPub)})
}

func (s *stState) ownerAcct() string { return hex.EncodeToString(s.ownerPub) }

// addStation registers a node bound to the given account and marks it live.
func (s *stState) addStation(id, acct string, live bool) {
	nodePub, _, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{
		NodeID: id, PubKey: hex.EncodeToString(nodePub), Region: "eu", HW: "gpu",
		BridgeToken: "SECRET-BRIDGE-TOKEN",
		Offers:      []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 2, Ctx: 4096}},
	}
	s.b.mu.Lock()
	s.b.nodes[id] = reg
	if live {
		s.b.lastSeen[id] = time.Now()
	} else {
		s.b.lastSeen[id] = time.Now().Add(-24 * time.Hour)
	}
	s.b.mu.Unlock()
	_ = s.db.UpsertNode(store.NodeRecord{NodeID: id, Reg: reg, RegisteredAt: time.Now().Unix()})
	_ = s.db.BindNode(id, acct)
}

func (s *stState) authedOwner() error { return nil }

func (s *stState) runsStations(a, b string) error {
	s.addStation(a, s.ownerAcct(), true)
	s.addStation(b, s.ownerAcct(), true)
	return nil
}

func (s *stState) otherOperatorRuns(id string) error {
	otherPub, _, _ := ed25519.GenerateKey(nil)
	s.addStation(id, hex.EncodeToString(otherPub), true)
	return nil
}

func (s *stState) runsNoStations() error { return nil }

func (s *stState) liveStation() error {
	s.addStation("live-1", s.ownerAcct(), true)
	return nil
}

func (s *stState) staleStation() error {
	s.addStation("stale-1", s.ownerAcct(), false)
	return nil
}

func (s *stState) stationWithEarnings() error {
	s.addStation("earner", s.ownerAcct(), true)
	// Mint through the real settle path so the earning is genuine, not injected.
	rec := protocol.UsageReceipt{RequestID: "r-earn", NodeID: "earner", Model: "m", PromptTokens: 10, CompletionTokens: 10, PriceIn: 1000, PriceOut: 1000}
	_, _ = s.db.Settle("u_gh_4242", "earner", 0.02, 0.014, rec)
	return nil
}

func (s *stState) chainRecorded() error {
	s.addStation("chained", s.ownerAcct(), true)
	_, _ = s.db.AdvanceChain("chained", "", "head-1")
	return nil
}

func (s *stState) noChainRecorded() error {
	s.addStation("fresh", s.ownerAcct(), true)
	return nil
}

func (s *stState) ownerHasStrikes() error {
	s.addStation("struck", s.ownerAcct(), true)
	_, _ = s.db.OwnerStrike(s.ownerAcct(), store.StrikeReceiptUnbound, `{"dispatched_request":"req-A"}`, "k1")
	return nil
}

func (s *stState) privateBandStation() error {
	s.addStation("priv", s.ownerAcct(), true)
	s.b.mu.Lock()
	reg := s.b.nodes["priv"]
	reg.Private = true
	s.b.nodes["priv"] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *stState) servedManyRequests() error {
	s.addStation("busy", s.ownerAcct(), true)
	return nil
}

// --- the call --------------------------------------------------------------

func (s *stState) readList() error { return s.call(!s.noCreds) }

func (s *stState) noCredentials() error { s.noCreds = true; return nil }

func (s *stState) call(authed bool) error {
	r := httptest.NewRequest(http.MethodGet, "/stations", nil)
	if authed {
		signReq(r, s.ownerKey, nil)
	}
	w := httptest.NewRecorder()
	s.b.stations(w, r)
	s.code = w.Code
	s.raw = w.Body.String()
	_ = json.Unmarshal([]byte(s.raw), &s.body)
	return nil
}

// --- assertions ------------------------------------------------------------

func (s *stState) rejectedUnauthorized() error {
	if s.code != http.StatusUnauthorized {
		return fmt.Errorf("want 401, got %d: %s", s.code, s.raw)
	}
	return nil
}

func (s *stState) noStationData() error {
	if strings.Contains(s.raw, "\"stations\"") {
		return fmt.Errorf("an unauthorized response must carry no station data: %s", s.raw)
	}
	return nil
}

func (s *stState) stationList() []map[string]any {
	out := []map[string]any{}
	arr, _ := s.body["stations"].([]any)
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (s *stState) contains(id string) error {
	for _, st := range s.stationList() {
		if st["node_id"] == id {
			return nil
		}
	}
	return fmt.Errorf("station %q missing from the list: %s", id, s.raw)
}

func (s *stState) doesNotContain(id string) error {
	for _, st := range s.stationList() {
		if st["node_id"] == id {
			return fmt.Errorf("station %q must not appear in another owner's list", id)
		}
	}
	return nil
}

func (s *stState) emptyList() error {
	if n := len(s.stationList()); n != 0 {
		return fmt.Errorf("want an empty list, got %d stations", n)
	}
	if s.code != http.StatusOK {
		return fmt.Errorf("an owner with no stations must get 200, got %d", s.code)
	}
	return nil
}

func (s *stState) one() (map[string]any, error) {
	l := s.stationList()
	if len(l) != 1 {
		return nil, fmt.Errorf("want exactly one station, got %d", len(l))
	}
	return l[0], nil
}

func (s *stState) reportsIdentityAndTimes() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	for _, k := range []string{"node_id", "registered_at", "last_seen"} {
		if st[k] == nil {
			return fmt.Errorf("station is missing %s: %v", k, st)
		}
	}
	return nil
}

func (s *stState) reportsOffer() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	offers, _ := st["offers"].([]any)
	if len(offers) == 0 {
		return fmt.Errorf("station reports no offers")
	}
	o, _ := offers[0].(map[string]any)
	if o["model"] == nil || o["price_in"] == nil {
		return fmt.Errorf("offer is missing model/price: %v", o)
	}
	return nil
}

func (s *stState) reportsOnAir(want bool) error {
	st, err := s.one()
	if err != nil {
		return err
	}
	if st["on_air"] != want {
		return fmt.Errorf("on_air = %v, want %v", st["on_air"], want)
	}
	return nil
}

func (s *stState) reportsOnAirTrue() error  { return s.reportsOnAir(true) }
func (s *stState) reportsOnAirFalse() error { return s.reportsOnAir(false) }

func (s *stState) historyVisible() error { return s.reportsIdentityAndTimes() }

func (s *stState) reportsEarnings() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	if v, _ := st["earnings"].(float64); v <= 0 {
		return fmt.Errorf("earnings = %v, want the minted balance", st["earnings"])
	}
	return nil
}

func (s *stState) reportsServed() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	if st["recent_served"] == nil {
		return fmt.Errorf("station does not report served count")
	}
	return nil
}

func (s *stState) reportsChain() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	ch, _ := st["chain"].(map[string]any)
	if ch == nil || ch["head"] == nil {
		return fmt.Errorf("station does not report a chain head: %v", st)
	}
	if ch["breaks"] == nil {
		return fmt.Errorf("station does not report a break count")
	}
	return nil
}

func (s *stState) breaksAreAudit() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	if st["chain_state"] == nil {
		return fmt.Errorf("station does not label its chain state")
	}
	return nil
}

func (s *stState) reportsUnknownChain() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	ch, _ := st["chain"].(map[string]any)
	if ch != nil && ch["head"] != nil && ch["head"] != "" {
		return fmt.Errorf("a station with no receipts must report no chain head: %v", ch)
	}
	if b, _ := ch["breaks"].(float64); b != 0 {
		return fmt.Errorf("a station with no receipts must report zero breaks, got %v", b)
	}
	return nil
}

func (s *stState) notPresentedAsBroken() error {
	st, err := s.one()
	if err != nil {
		return err
	}
	if st["chain_state"] != "unknown" {
		return fmt.Errorf("chain_state = %v, want unknown for a station that has not served", st["chain_state"])
	}
	return nil
}

func (s *stState) reportsStrikes() error {
	arr, _ := s.body["strikes"].([]any)
	if len(arr) == 0 {
		return fmt.Errorf("the owner's strikes are not surfaced: %s", s.raw)
	}
	return nil
}

func (s *stState) strikesCarryEvidence() error {
	arr, _ := s.body["strikes"].([]any)
	for _, v := range arr {
		m, _ := v.(map[string]any)
		if ev, _ := m["evidence"].(string); ev == "" {
			return fmt.Errorf("a strike carries no evidence: %v", m)
		}
	}
	return nil
}

func (s *stState) noConsumerOrContent() error {
	for _, bad := range []string{"prompt", "completion", "u_gh_", "\"usr\""} {
		if strings.Contains(s.raw, bad) {
			return fmt.Errorf("response leaks %q: %s", bad, s.raw)
		}
	}
	return nil
}

func (s *stState) noOtherOperatorData() error { return s.doesNotContain("gamma") }

func (s *stState) noBridgeToken() error {
	if strings.Contains(s.raw, "SECRET-BRIDGE-TOKEN") || strings.Contains(s.raw, "bridge_token") {
		return fmt.Errorf("response leaks the bridge token: %s", s.raw)
	}
	return nil
}

func (s *stState) noBandCode() error {
	if strings.Contains(s.raw, "band_code") || strings.Contains(s.raw, "freq_code") {
		return fmt.Errorf("response leaks a private band code: %s", s.raw)
	}
	return nil
}

func TestStationsDashboardBDD(t *testing.T) {
	st := &stState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^an authenticated owner account$`, st.authedOwner)
			sc.Step(`^the caller presents no session and no valid request signature$`, st.noCredentials)
			sc.Step(`^they read the station list$`, st.readList)
			sc.Step(`^they read their station list$`, st.readList)
			sc.Step(`^the request is rejected as unauthorized$`, st.rejectedUnauthorized)
			sc.Step(`^no station data is returned$`, st.noStationData)
			sc.Step(`^the owner runs stations "([^"]*)" and "([^"]*)"$`, st.runsStations)
			sc.Step(`^another operator runs station "([^"]*)"$`, st.otherOperatorRuns)
			sc.Step(`^the owner reads their station list$`, st.readList)
			sc.Step(`^it contains "([^"]*)" and "([^"]*)"$`, func(a, b string) error {
				if err := st.contains(a); err != nil {
					return err
				}
				return st.contains(b)
			})
			sc.Step(`^it does not contain "([^"]*)"$`, st.doesNotContain)
			sc.Step(`^the owner runs no stations$`, st.runsNoStations)
			sc.Step(`^the response is an empty list$`, st.emptyList)
			sc.Step(`^the owner runs a station that registered and last heartbeated recently$`, st.liveStation)
			sc.Step(`^the station reports its id, registered-at time, and last-seen time$`, st.reportsIdentityAndTimes)
			sc.Step(`^it reports its offered model, modality, and price$`, st.reportsOffer)
			sc.Step(`^it reports whether it is currently on air$`, st.reportsOnAirTrue)
			sc.Step(`^a station has not heartbeated within the liveness window$`, st.staleStation)
			sc.Step(`^that station reports on-air false$`, st.reportsOnAirFalse)
			sc.Step(`^its registration and history remain visible$`, st.historyVisible)
			sc.Step(`^a station has served requests and minted earnings$`, st.stationWithEarnings)
			sc.Step(`^the station reports its current earnings balance$`, st.reportsEarnings)
			sc.Step(`^it reports how many requests it has served$`, st.reportsServed)
			sc.Step(`^the broker has recorded a chain head for the station$`, st.chainRecorded)
			sc.Step(`^the station reports its current chain head, last check time, and break count$`, st.reportsChain)
			sc.Step(`^the break count is labelled an audit signal, not a penalty$`, st.breaksAreAudit)
			sc.Step(`^the broker has never recorded a receipt from the station$`, st.noChainRecorded)
			sc.Step(`^the station reports no chain head and zero breaks$`, st.reportsUnknownChain)
			sc.Step(`^it is not presented as broken$`, st.notPresentedAsBroken)
			sc.Step(`^the owner's account has accrued strikes$`, st.ownerHasStrikes)
			sc.Step(`^the response reports the owner's strike count and kinds$`, st.reportsStrikes)
			sc.Step(`^each strike carries the evidence that produced it$`, st.strikesCarryEvidence)
			sc.Step(`^a station has served many requests$`, st.servedManyRequests)
			sc.Step(`^no consumer identity, prompt, completion, or request body appears in the response$`, st.noConsumerOrContent)
			sc.Step(`^no other operator's earnings or account identifiers appear$`, st.noOtherOperatorData)
			sc.Step(`^a station registered with a bridge token and runs on a private band$`, st.privateBandStation)
			sc.Step(`^the bridge token is absent from the response$`, st.noBridgeToken)
			sc.Step(`^the band's secret frequency code is absent$`, st.noBandCode)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/stations/stations_dashboard.feature"},
			TestingT: t,
			Output:   os.Stdout,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("stations-dashboard scenarios failed (see godog output above)")
	}
}
