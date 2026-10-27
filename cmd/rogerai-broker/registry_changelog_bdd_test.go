package main

// registry_changelog_bdd_test.go makes features/multinode/registry_changelog.feature EXECUTABLE.
// Real broker instances over real HTTP (the cross-instance harness), nodes registering with
// signed registrations, one shared in-process miniredis (this suite counts store commands,
// which a remote Valkey cannot report per client). No mocks.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"rogerai.fm/roger/v6/internal/bddtest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/redis/go-redis/v9"
	"rogerai.fm/roger/v6/internal/protocol"
)

type clState struct {
	*xiState
	mr       *miniredis.Miniredis
	nodes    map[string]*xiNode
	snapsB   int64
	perTick  float64
	tickTook time.Duration
	staleTS  int64
	graceTok string
	unlogged bool // the next registration is written by code that predates the log
}

func (s *clState) inst(name string) *broker { return s.xiState.inst[name].b }

func (s *clState) twoInstances() error {
	s.db = xiStore(s.t)
	s.mr = miniredis.RunT(s.t)
	s.redisURL = "redis://" + s.mr.Addr()
	s.nodes = map[string]*xiNode{}
	for _, n := range []string{"A", "B"} {
		b := s.newInstance(n, true).b
		b.dispatchMode = dispatchViaQueueOnly // the rollout is complete: one liveness read
		b.shared.(*valkeyStore).livenessHashOnly.Store(true)
	}
	// Each instance syncs once as it boots, so every later tick is a change-log tick.
	s.inst("A").syncLivenessOnce()
	s.inst("B").syncLivenessOnce()
	return nil
}

// node makes (or returns) a scenario node; owner-signed so it may register as a private band.
func (s *clState) node(name string) *xiNode {
	if n, ok := s.nodes[name]; ok {
		return n
	}
	n := s.mkNode(name, "free-m", 0, true)
	s.nodes[name] = n
	return n
}

func (s *clState) registerOn(name, inst string, private bool) error {
	n := s.node(name)
	n.private = private
	if err := n.register(s.t, s.xiState.inst[inst]); err != nil {
		return err
	}
	n.beat(s.t, s.xiState.inst[inst])
	return nil
}

// tickLog runs one sync tick on inst and fails if it fell back to a full snapshot.
func (s *clState) tickLog(inst string) error {
	b := s.inst(inst)
	before := b.changes.snapshots.Load()
	b.syncLivenessOnce()
	if got := b.changes.snapshots.Load(); got != before {
		return fmt.Errorf("instance %s took a full snapshot on an ordinary tick", inst)
	}
	return nil
}

func (s *clState) known(inst, id string) (protocol.NodeRegistration, bool) {
	b := s.inst(inst)
	b.mu.Lock()
	defer b.mu.Unlock()
	reg, ok := b.nodes[id]
	return reg, ok
}

// pollAuth reports the status of a poll on inst: 401/404 at once, or 200 when the broker
// accepted it and held it (the context ends the hold).
func (s *clState) pollAuth(inst, node, token string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.xiState.inst[inst].url()+"/agent/poll?node="+node, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return http.StatusOK // held until our deadline: authenticated
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (s *clState) discover(inst string) (string, error) {
	resp, err := http.Get(s.xiState.inst[inst].url() + "/discover")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

// ---- C1 -------------------------------------------------------------------------------------

func (s *clState) manyRegistered(pub, priv int) error {
	for i := 0; i < pub+priv; i++ {
		inst := []string{"A", "B"}[i%2]
		if err := s.registerOn(fmt.Sprintf("q%03d", i), inst, i >= pub); err != nil {
			return err
		}
	}
	return nil
}

func (s *clState) bothSynced() error {
	for _, inst := range []string{"A", "B"} {
		s.inst(inst).syncLivenessOnce()
	}
	return nil
}

func (s *clState) quietTicks(n int) error {
	for _, inst := range []string{"A", "B"} {
		before := s.mr.CommandCount()
		for i := 0; i < n; i++ {
			if err := s.tickLog(inst); err != nil {
				return err
			}
		}
		per := float64(s.mr.CommandCount()-before) / float64(n)
		if per > s.perTick {
			s.perTick = per
		}
	}
	return nil
}

func (s *clState) tickCostAtMost(max int) error {
	if s.perTick > float64(max) {
		return fmt.Errorf("a quiet tick cost %.1f store commands on 550 nodes, want at most %d", s.perTick, max)
	}
	return nil
}

// ---- C2 -------------------------------------------------------------------------------------

func (s *clState) registersOnA(name string) error {
	if s.unlogged {
		return s.registersUnlogged(name)
	}
	return s.registerOn(name, "A", false)
}
func (s *clState) ticksOnce(inst string) error { return s.tickLog(inst) }

func (s *clState) canPickAndAuth(inst, name string) error {
	n := s.node(name)
	if _, ok := s.known(inst, n.id); !ok {
		return fmt.Errorf("instance %s has not learned %s from the change log", inst, name)
	}
	if code := s.pollAuth(inst, n.id, n.token); code != http.StatusOK {
		return fmt.Errorf("poll on %s = %d, want accepted", inst, code)
	}
	return nil
}

func (s *clState) registeredOnBoth(name string) error {
	if err := s.registerOn(name, "A", false); err != nil {
		return err
	}
	return s.tickLog("B")
}

func (s *clState) rotatesOnA(name string) error {
	n := s.node(name)
	n.token = "tok-rot-" + xiNonce()
	time.Sleep(1100 * time.Millisecond) // registrations carry a unix-second TS: make it newer
	return s.registerOn(name, "A", n.private)
}

func (s *clState) onlyNewToken(inst, name string) error {
	n := s.node(name)
	if code := s.pollAuth(inst, n.id, n.token); code != http.StatusOK {
		return fmt.Errorf("the rotated token was refused on %s (%d)", inst, code)
	}
	if code := s.pollAuth(inst, n.id, "tok-0-"+s.nonce); code != http.StatusUnauthorized {
		return fmt.Errorf("the old token was answered %d on %s, want 401", code, inst)
	}
	return nil
}

func (s *clState) registeredPubliclyOnBoth(name string) error { return s.registeredOnBoth(name) }

func (s *clState) turnsPrivateOnA(name string) error {
	time.Sleep(1100 * time.Millisecond)
	return s.registerOn(name, "A", true)
}

func (s *clState) absentFromPublic(name, inst string) error {
	body, err := s.discover(inst)
	if err != nil {
		return err
	}
	if strings.Contains(body, s.node(name).id) {
		return fmt.Errorf("%s is still in instance %s's public discover", name, inst)
	}
	return nil
}

func (s *clState) routableForOwner(name, inst string) error {
	n := s.node(name)
	reg, ok := s.known(inst, n.id)
	b := s.inst(inst)
	b.mu.Lock()
	priv := b.private[n.id]
	b.mu.Unlock()
	if !ok || !reg.Private || !priv {
		return fmt.Errorf("instance %s does not hold %s as a private band (known=%v private=%v)", inst, name, ok, priv)
	}
	return nil
}

func (s *clState) privateOnBoth(name string) error {
	if err := s.registerOn(name, "A", true); err != nil {
		return err
	}
	return s.tickLog("B")
}

func (s *clState) turnsPublicOnA(name string) error {
	time.Sleep(1100 * time.Millisecond)
	return s.registerOn(name, "A", false)
}

func (s *clState) appearsInPublic(name, inst string) error {
	s.node(name).beat(s.t, s.xiState.inst["A"])
	s.inst(inst).syncLivenessOnce() // liveness for the listing
	body, err := s.discover(inst)
	if err != nil {
		return err
	}
	if !strings.Contains(body, s.node(name).id) {
		return fmt.Errorf("%s is missing from instance %s's public discover: %s", name, inst, body)
	}
	return nil
}

func (s *clState) coolsOnA(name string, secs int) error {
	s.inst("A").coolStation(s.node(name).id, "free-m", secs)
	return nil
}

func (s *clState) notRoutedUntilCool(inst, name string) error {
	b := s.inst(inst)
	b.metricsMu.Lock()
	until := b.cooling[s.node(name).id]
	b.metricsMu.Unlock()
	if time.Until(until) < 50*time.Second {
		return fmt.Errorf("instance %s has no 60 s cooldown for %s (until %v)", inst, name, until)
	}
	return nil
}

func (s *clState) marksTools(name, model string) error {
	return s.inst("A").shared.markToolsVerified(s.node(name).id, model, toolsVerifiedTTL)
}

func (s *clState) clearsTools() error {
	return s.inst("A").shared.clearToolsVerified(s.node("n1").id, "m1")
}

func (s *clState) toolsListed(inst, name, model string) error {
	b := s.inst(inst)
	b.metricsMu.Lock()
	ok := b.toolsMerged[toolKey(s.node(name).id, model)]
	b.metricsMu.Unlock()
	if !ok {
		return fmt.Errorf("instance %s does not list %s as tool-capable for %s", inst, name, model)
	}
	return nil
}

func (s *clState) toolsNotListed(inst string) error {
	b := s.inst(inst)
	b.metricsMu.Lock()
	ok := b.toolsMerged[toolKey(s.node("n1").id, "m1")]
	b.metricsMu.Unlock()
	if ok {
		return fmt.Errorf("instance %s still lists the cleared verdict", inst)
	}
	return nil
}

// ---- C3 -------------------------------------------------------------------------------------

func (s *clState) fresherOnB(name string) error {
	if err := s.registerOn(name, "B", false); err != nil {
		return err
	}
	// Out of the local grace window, so the stale-mirror rule (not the grace) decides.
	b := s.inst("B")
	b.mu.Lock()
	b.localRegAt[s.node(name).id] = time.Now().Add(-2 * syncLocalRegisterGrace)
	b.mu.Unlock()
	return nil
}

// logFromA publishes a registration of the node from instance A with the given TS and token:
// exactly what A's register or heal publishes (the shared key plus its log entry).
func (s *clState) logFromA(name string, ts int64, token string) error {
	n := s.node(name)
	reg := protocol.NodeRegistration{NodeID: n.id, PubKey: n.pub, BridgeToken: token, TS: ts,
		Offers: []protocol.ModelOffer{{Model: "free-m", Ctx: 4096}}}
	raw, _ := json.Marshal(reg)
	return s.inst("A").shared.putNode(n.id, raw, livenessTTL)
}

func (s *clState) bAppliesOlder() error {
	reg, _ := s.known("B", s.node("n1").id)
	s.staleTS = reg.TS - 100
	if err := s.logFromA("n1", s.staleTS, "tok-stale"); err != nil {
		return err
	}
	return s.tickLog("B")
}

func (s *clState) bKeepsFresher() error {
	reg, ok := s.known("B", s.node("n1").id)
	if !ok || reg.TS == s.staleTS || reg.BridgeToken == "tok-stale" {
		return fmt.Errorf("instance B regressed to the older registration (%+v)", reg)
	}
	return nil
}

func (s *clState) sharedHealed() error {
	raw, ok, err := s.inst("B").shared.getNode(s.node("n1").id)
	if err != nil || !ok {
		return fmt.Errorf("shared registration missing: %v", err)
	}
	var reg protocol.NodeRegistration
	_ = json.Unmarshal(raw, &reg)
	if reg.TS == s.staleTS {
		return fmt.Errorf("the shared registry still holds the stale registration")
	}
	return nil
}

func (s *clState) registeredOnBRecently(name string) error {
	if err := s.registerOn(name, "B", false); err != nil {
		return err
	}
	s.graceTok = s.node(name).token
	return nil
}

func (s *clState) bAppliesEntryFromA() error {
	reg, _ := s.known("B", s.node("n1").id)
	if err := s.logFromA("n1", reg.TS+5, "tok-from-A"); err != nil {
		return err
	}
	return s.tickLog("B")
}

func (s *clState) bKeepsOwn() error {
	reg, _ := s.known("B", s.node("n1").id)
	if reg.BridgeToken != s.graceTok {
		return fmt.Errorf("instance B replaced its own fresh registration inside the grace window")
	}
	// And the entry is not lost: once the grace has passed it is applied (its TS is newer).
	b := s.inst("B")
	b.mu.Lock()
	b.localRegAt[s.node("n1").id] = time.Now().Add(-2 * syncLocalRegisterGrace)
	b.mu.Unlock()
	if err := s.tickLog("B"); err != nil {
		return err
	}
	if reg, _ := s.known("B", s.node("n1").id); reg.BridgeToken != "tok-from-A" {
		return fmt.Errorf("the deferred newer entry was never applied after the grace (token %q)", reg.BridgeToken)
	}
	return nil
}

func (s *clState) appliesSameTwice() error {
	if err := s.registerOn("n1", "A", false); err != nil {
		return err
	}
	vs := s.inst("B").shared.(*valkeyStore)
	entries, _, err := vs.changesAfter("0-0", "", 10000)
	if err != nil {
		return err
	}
	var e changeEntry
	for _, x := range entries {
		if x.key == s.node("n1").id && x.kind == "reg" {
			e = x
		}
	}
	if e.id == "" {
		return fmt.Errorf("no log entry for the registration")
	}
	b := s.inst("B")
	b.changes.mu.Lock()
	b.applyChanges([]changeEntry{e, e})
	b.changes.mu.Unlock()
	return nil
}

func (s *clState) oneCopy() error {
	id := s.node("n1").id
	body, err := s.discover("B")
	if err != nil {
		return err
	}
	if c := strings.Count(body, `"`+id+`"`); c > 1 {
		return fmt.Errorf("the node appears %d times in instance B's discover", c)
	}
	if _, ok := s.known("B", id); !ok {
		return fmt.Errorf("instance B does not hold the node")
	}
	return nil
}

// ---- C4 -------------------------------------------------------------------------------------

func (s *clState) bAppliedUpTo() error {
	if err := s.registerOn("n1", "A", false); err != nil {
		return err
	}
	if err := s.tickLog("B"); err != nil {
		return err
	}
	s.snapsB = s.inst("B").changes.snapshots.Load()
	return nil
}

func (s *clState) logTrimmed() error {
	ctx, cancel := opCtx()
	defer cancel()
	if err := s.inst("A").shared.(*valkeyStore).rdb.XTrimMaxLen(ctx, changeLogKey, 0).Err(); err != nil {
		return err
	}
	// A registration logged after the trim: what a partial view would still pick up.
	return s.registerOn("n2", "A", false)
}

func (s *clState) bTicksAny() error {
	s.inst("B").syncLivenessOnce()
	return nil
}

func (s *clState) bTookSnapshot() error {
	if got := s.inst("B").changes.snapshots.Load(); got != s.snapsB+1 {
		return fmt.Errorf("instance B took %d snapshots after the gap, want 1", got-s.snapsB)
	}
	return nil
}

func (s *clState) registryMatchesShared() error {
	pub, _ := s.inst("B").shared.allNodes()
	priv, _ := s.inst("B").shared.allPrivateNodes()
	var want []string
	for id := range pub {
		want = append(want, id)
	}
	for id := range priv {
		want = append(want, id)
	}
	b := s.inst("B")
	b.mu.Lock()
	var got []string
	for id := range b.nodes {
		got = append(got, id)
	}
	b.mu.Unlock()
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, ",") != strings.Join(got, ",") {
		return fmt.Errorf("instance B holds %v, the shared registry holds %v", got, want)
	}
	return nil
}

func (s *clState) twentyNodes() error {
	for i := 0; i < 20; i++ {
		if err := s.registerOn(fmt.Sprintf("t%02d", i), []string{"A", "B"}[i%2], false); err != nil {
			return err
		}
	}
	return nil
}

func (s *clState) thirdStarts() error {
	b := s.newInstance("C", true).b
	b.dispatchMode = dispatchViaQueueOnly
	b.shared.(*valkeyStore).livenessHashOnly.Store(true)
	return nil
}

func (s *clState) servesAllAfterFirstTick() error {
	s.inst("C").syncLivenessOnce()
	for i := 0; i < 20; i++ {
		if _, ok := s.known("C", s.node(fmt.Sprintf("t%02d", i)).id); !ok {
			return fmt.Errorf("the new instance is missing t%02d after its first tick", i)
		}
	}
	return nil
}

func (s *clState) laterReachesByLog() error {
	if err := s.registerOn("late", "A", false); err != nil {
		return err
	}
	if err := s.tickLog("C"); err != nil {
		return err
	}
	if _, ok := s.known("C", s.node("late").id); !ok {
		return fmt.Errorf("a registration after boot did not reach the new instance by the log")
	}
	return nil
}

func (s *clState) unloggedWriter() error { s.unlogged = true; return nil }

func (s *clState) registersUnlogged(name string) error {
	n := s.node(name)
	reg := protocol.NodeRegistration{NodeID: n.id, PubKey: n.pub, BridgeToken: n.token, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "free-m", Ctx: 4096}}}
	raw, _ := json.Marshal(reg)
	ctx, cancel := opCtx()
	defer cancel()
	rdb := s.inst("A").shared.(*valkeyStore).rdb
	// Exactly what putNode wrote before the change log existed.
	_, err := rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, regKey(n.id), raw, livenessTTL)
		p.SAdd(ctx, keyPrefix+"regset", n.id)
		return nil
	})
	return err
}

func (s *clState) routableWithinReconcile(name string) error {
	id := s.node(name).id
	if err := s.tickLog("B"); err != nil {
		return err
	}
	if _, ok := s.known("B", id); ok {
		return fmt.Errorf("an unlogged write reached instance B by the log - the test would be vacuous")
	}
	// The reconcile interval passes.
	b := s.inst("B")
	b.changes.mu.Lock()
	b.changes.fullAt = time.Now().Add(-changeReconcile - time.Second)
	b.changes.mu.Unlock()
	b.syncLivenessOnce()
	if _, ok := s.known("B", id); !ok {
		return fmt.Errorf("the reconcile did not pick up the unlogged registration")
	}
	return nil
}

func (s *clState) storeRestartsEmpty() error {
	s.mr.FlushAll()
	return s.registerOn("n3", "A", false)
}

// ---- C5 -------------------------------------------------------------------------------------

func (s *clState) registeredOnB(name string) error { return s.registerOn(name, "B", false) }

func (s *clState) storeUnreachable() error {
	s.mr.Close()
	start := time.Now()
	s.inst("B").syncLivenessOnce()
	s.tickTook = time.Since(start)
	return nil
}

func (s *clState) stillRoutes(name string) error {
	if _, ok := s.known("B", s.node(name).id); !ok {
		return fmt.Errorf("instance B lost %s when the store went away", name)
	}
	return nil
}

func (s *clState) tickNonBlocking() error {
	if s.tickTook > 5*time.Second {
		return fmt.Errorf("the tick took %s against an unreachable store", s.tickTook)
	}
	return nil
}

func clInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		s := &clState{xiState: &xiState{}}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			s.xiState.reset(t)
			s.perTick, s.snapsB, s.unlogged = 0, 0, false
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			s.xiState.cleanup()
			return ctx, nil
		})
		sc.Step(`^a multi-instance broker of two instances sharing one store$`, s.twoInstances)
		sc.Step(`^(\d+) public and (\d+) private nodes registered across both instances$`, s.manyRegistered)
		sc.Step(`^both instances have synced once$`, s.bothSynced)
		sc.Step(`^(\d+) ticks pass with no registration, cooldown or tools change$`, s.quietTicks)
		sc.Step(`^each tick costs at most (\d+) store commands per instance$`, s.tickCostAtMost)
		sc.Step(`^node "([^"]*)" registers on instance A$`, s.registersOnA)
		sc.Step(`^instance (\w) ticks once$`, s.ticksOnce)
		sc.Step(`^instance (\w) can pick "([^"]*)" and authenticate its poll$`, s.canPickAndAuth)
		sc.Step(`^node "([^"]*)" is registered on both instances$`, s.registeredOnBoth)
		sc.Step(`^"([^"]*)" re-registers on instance A with a rotated token$`, s.rotatesOnA)
		sc.Step(`^instance (\w) authenticates "([^"]*)" only with the new token$`, s.onlyNewToken)
		sc.Step(`^node "([^"]*)" is registered publicly on both instances$`, s.registeredPubliclyOnBoth)
		sc.Step(`^"([^"]*)" re-registers on instance A as a private band$`, s.turnsPrivateOnA)
		sc.Step(`^"([^"]*)" is absent from instance (\w)'s public discover$`, s.absentFromPublic)
		sc.Step(`^"([^"]*)" is still routable on instance (\w) for its owner$`, s.routableForOwner)
		sc.Step(`^node "([^"]*)" is registered as a private band on both instances$`, s.privateOnBoth)
		sc.Step(`^"([^"]*)" re-registers on instance A as a public band$`, s.turnsPublicOnA)
		sc.Step(`^"([^"]*)" appears in instance (\w)'s public discover$`, s.appearsInPublic)
		sc.Step(`^instance A cools "([^"]*)" for (\d+) s after an upstream 429$`, s.coolsOnA)
		sc.Step(`^instance (\w) does not route to "([^"]*)" until the cooldown ends$`, s.notRoutedUntilCool)
		sc.Step(`^instance A marks "([^"]*)" tool-capable for "([^"]*)"$`, s.marksTools)
		sc.Step(`^instance (\w) lists "([^"]*)" as tool-capable for "([^"]*)"$`, s.toolsListed)
		sc.Step(`^instance A clears that verdict on an authoritative regression$`, s.clearsTools)
		sc.Step(`^instance (\w) no longer lists it$`, s.toolsNotListed)
		sc.Step(`^node "([^"]*)" registered on instance B more recently than the entry instance A logged$`, s.fresherOnB)
		sc.Step(`^instance B applies A's older entry$`, s.bAppliesOlder)
		sc.Step(`^instance B keeps its fresher registration$`, s.bKeepsFresher)
		sc.Step(`^the shared registry is healed to the fresher one$`, s.sharedHealed)
		sc.Step(`^node "([^"]*)" registered on instance B 2 s ago$`, s.registeredOnBRecently)
		sc.Step(`^instance B applies an entry for "n1" from instance A$`, s.bAppliesEntryFromA)
		sc.Step(`^instance B keeps its own registration$`, s.bKeepsOwn)
		sc.Step(`^instance B applies the same registration entry twice$`, s.appliesSameTwice)
		sc.Step(`^instance B's registry holds one copy of the node$`, s.oneCopy)
		sc.Step(`^instance B has applied the log up to some entry$`, s.bAppliedUpTo)
		sc.Step(`^the log is trimmed past that entry$`, s.logTrimmed)
		sc.Step(`^instance B ticks$`, s.bTicksAny)
		sc.Step(`^instance B takes a full snapshot$`, s.bTookSnapshot)
		sc.Step(`^its registry matches the shared registry exactly$`, s.registryMatchesShared)
		sc.Step(`^20 nodes registered across instances A and B$`, s.twentyNodes)
		sc.Step(`^a third instance starts$`, s.thirdStarts)
		sc.Step(`^it serves all 20 after its first tick$`, s.servesAllAfterFirstTick)
		sc.Step(`^a registration on instance A afterwards reaches it by the log$`, s.laterReachesByLog)
		sc.Step(`^instance A runs code that writes registrations without logging them$`, s.unloggedWriter)
		sc.Step(`^instance B can route to "([^"]*)" within the 60 s reconcile interval$`, s.routableWithinReconcile)
		sc.Step(`^the store restarts empty and nodes re-register$`, s.storeRestartsEmpty)
		sc.Step(`^instance B takes a full snapshot on its next tick$`, func() error {
			if err := s.bTicksAny(); err != nil {
				return err
			}
			return s.bTookSnapshot()
		})
		sc.Step(`^node "([^"]*)" is registered on instance B$`, s.registeredOnB)
		sc.Step(`^the store becomes unreachable$`, s.storeUnreachable)
		sc.Step(`^instance B still routes to "([^"]*)"$`, s.stillRoutes)
		sc.Step(`^its tick returns without blocking the request path$`, s.tickNonBlocking)
	}
}

func TestRegistryChangelogBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: clInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/registry_changelog.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("multinode/registry_changelog scenarios failed (see godog output above)")
	}
}
