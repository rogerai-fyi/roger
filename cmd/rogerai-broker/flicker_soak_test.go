package main

// Real-time /discover FLICKER soak against a REAL Valkey (podman), the acceptance test
// for the "8-online <-> 0-online" prod flicker. It is DELIBERATELY not fake-clock: it
// stands up real broker instance(s) on a real redis, drives a node that heartbeats +
// long-polls + serves (or fails) canary probes at a realistic cadence, and samples
// /discover in a tight loop, counting how often the market momentarily reads 0-online
// while the node is provably heartbeat-live.
//
// Gated on ROGERAI_TEST_REDIS_URL (the podman Valkey). It is skipped in the normal
// cover-gate run (no real redis wired) so it never slows CI; run it explicitly:
//
//	ROGERAI_TEST_REDIS_URL=redis://127.0.0.1:6399 go test ./cmd/rogerai-broker \
//	  -run TestDiscoverFlicker -count=1 -v
//
// Timings are scaled DOWN from prod by 10x but keep the ratios (TTL 4.5s : throttle 2s :
// sync 0.5s == prod 45s : 20s : 5s), so a ~20s soak reproduces what a ~3m prod window does.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// flickCfg configures one soak run.
type flickCfg struct {
	instances       int           // 1 (single-instance + shared) or 2 (cross-instance)
	nodes           int           // number of nodes registered
	probe           bool          // run the canary prober
	canaryFailRate  float64       // 0..1 fraction of canary probes the node fails (empty body)
	sharedWriteFail float64       // 0..1 fraction of durable shared markSeen writes that error
	soak            time.Duration // total sample window
}

// faultyStore wraps a real valkeyStore and makes markSeen's durable write FAIL a
// configurable fraction of the time, reproducing the prod condition (a shared cluster
// with latency / intermittent write failures) that miniredis never exhibits. Every other
// method delegates unchanged (embedded interface).
type faultyStore struct {
	sharedStore
	failRate float64
	mu       sync.Mutex
	rnd      *rand.Rand
	fails    atomic.Int64
	oks      atomic.Int64
}

func (f *faultyStore) markSeen(node string, now time.Time) error {
	f.mu.Lock()
	drop := f.rnd.Float64() < f.failRate
	f.mu.Unlock()
	if drop {
		f.fails.Add(1)
		return fmt.Errorf("injected: shared markSeen write failed")
	}
	f.oks.Add(1)
	return f.sharedStore.markSeen(node, now)
}

// flickInstance is one fully-wired broker instance on the shared redis, running the same
// background loops production runs when shared != nil (syncLiveness, and the prober when
// enabled), behind a real HTTP server.
type flickInst struct {
	b    *broker
	srv  *httptest.Server
	stop chan struct{}
}

func flickBuild(t *testing.T, db store.Store, priv ed25519.PrivateKey, redisURL string, cfg flickCfg) *flickInst {
	t.Helper()
	b := relayBroker(db)
	b.priv = priv
	b.seedFunds = 100
	b.anonRL = loadAnonRateLimiter()
	b.localRegAt = map[string]time.Time{}
	b.attestedAt = map[string]time.Time{}
	b.lastPersist = map[string]time.Time{}
	b.lastSharedSeen = map[string]time.Time{}
	vs, err := newValkeyStore(redisURL)
	if err != nil {
		t.Fatalf("valkey connect: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	var sh sharedStore = vs
	if cfg.sharedWriteFail > 0 {
		sh = &faultyStore{sharedStore: vs, failRate: cfg.sharedWriteFail, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
	}
	b.shared = sh
	if cfg.instances > 1 {
		b.multiInstance = true
		b.instanceID = newInstanceID()
		b.peerInflight = map[string]int{}
	}
	if cfg.probe {
		// Fast, non-backing-off probe so failures accumulate on the scaled clock.
		b.probe = probeConfig{interval: 150 * time.Millisecond, ceiling: 150 * time.Millisecond}
	}
	stop := make(chan struct{})
	go b.syncLiveness(stop)
	if cfg.probe {
		go b.proberLoop(stop)
	}
	inst := &flickInst{b: b, srv: httptest.NewServer(streamSafeHandler(b.routes())), stop: stop}
	t.Cleanup(func() { close(stop); inst.srv.Close() })
	return inst
}

// flickNode drives one node over real HTTP: register, heartbeat loop, and a poll+serve
// loop that answers canary jobs (failing a configurable fraction with an empty body).
type flickNode struct {
	id    string
	pub   string
	priv  ed25519.PrivateKey
	token string
	model string
	hc    *http.Client
}

func (n *flickNode) register(t *testing.T, base string) {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: n.id, PubKey: n.pub, BridgeToken: n.token, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: n.model, PriceOut: 0, Ctx: 4096}},
	}
	reg.SignRegistration(n.priv)
	body, _ := json.Marshal(reg)
	req, _ := http.NewRequest(http.MethodPost, base+"/nodes/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.hc.Do(req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status %d", resp.StatusCode)
	}
}

func (n *flickNode) heartbeatLoop(stop <-chan struct{}, base string, every time.Duration) {
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			body, _ := json.Marshal(map[string]string{"node_id": n.id})
			req, _ := http.NewRequest(http.MethodPost, base+"/nodes/heartbeat", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+n.token)
			if resp, err := n.hc.Do(req); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
	}
}

// pollLoop long-polls the instance and serves any canary job. failRate controls how many
// canaries get an empty (probe-FAIL) body vs a good ("ok") body.
func (n *flickNode) pollLoop(stop <-chan struct{}, base string, failRate float64) {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(len(n.id))))
	hc := &http.Client{Timeout: 30 * time.Second}
	for {
		select {
		case <-stop:
			return
		default:
		}
		req, _ := http.NewRequest(http.MethodGet, base+"/agent/poll?node="+n.id, nil)
		req.Header.Set("Authorization", "Bearer "+n.token)
		resp, err := hc.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			continue // 204 re-poll
		}
		var job protocol.Job
		if json.Unmarshal(body, &job) != nil {
			continue
		}
		completion := "ok"
		if rnd.Float64() < failRate {
			completion = "" // empty body => probeDead => probeFails++
		}
		res := miSignedResult(job.ID, n.id, n.model, completion, n.priv, 200)
		rb, _ := json.Marshal(res)
		rreq, _ := http.NewRequest(http.MethodPost, base+"/agent/result?node="+n.id, bytes.NewReader(rb))
		rreq.Header.Set("Authorization", "Bearer "+n.token)
		if rresp, rerr := hc.Do(rreq); rerr == nil {
			io.Copy(io.Discard, rresp.Body)
			rresp.Body.Close()
		}
	}
}

// onlineCount returns how many offers the given broker's /discover reports ONLINE right
// now (direct compute, bypassing the HTTP cache to read the raw liveness decision).
func onlineCount(b *broker) (online, total int) {
	res := b.computeDiscover().(map[string]any)
	offers := res["offers"].([]offerView)
	for _, o := range offers {
		total++
		if o.Online {
			online++
		}
	}
	return online, total
}

// flickResult is the measured soak outcome.
type flickResult struct {
	reads        int
	zeroReads    int // reads where online==0 while node(s) heartbeat-live
	minOnline    int
	transitions  int // online<->0 flips
	sharedFails  int64
	sharedOKs    int64
}

func runFlickSoak(t *testing.T, redisURL string, cfg flickCfg) flickResult {
	t.Helper()
	db := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)

	insts := make([]*flickInst, cfg.instances)
	for i := range insts {
		insts[i] = flickBuild(t, db, priv, redisURL, cfg)
	}
	// The node registers + heartbeats + polls on instance 0; /discover is read from the
	// LAST instance (instance 1 in the 2-instance case, instance 0 in the single case).
	homeBase := insts[0].srv.URL
	readInst := insts[len(insts)-1].b

	stop := make(chan struct{})
	var wg sync.WaitGroup
	nodes := make([]*flickNode, cfg.nodes)
	for i := range nodes {
		pub, npriv, _ := ed25519.GenerateKey(nil)
		n := &flickNode{
			id:    fmt.Sprintf("flick-node-%d", i),
			pub:   hex.EncodeToString(pub),
			priv:  npriv,
			token: fmt.Sprintf("tok-%d", i),
			model: "free-m",
			hc:    &http.Client{Timeout: 10 * time.Second},
		}
		nodes[i] = n
		n.register(t, homeBase)
		wg.Add(2)
		go func() { defer wg.Done(); n.heartbeatLoop(stop, homeBase, 200*time.Millisecond) }()
		go func() { defer wg.Done(); n.pollLoop(stop, homeBase, cfg.canaryFailRate) }()
	}

	// Let registration + the first sync land so the read instance has mirrored the node
	// and computeDiscover has a stable non-empty offer set before sampling begins.
	time.Sleep(2 * time.Second)

	res := flickResult{minOnline: 1 << 30}
	prevZero := false
	deadline := time.Now().Add(cfg.soak)
	for time.Now().Before(deadline) {
		online, total := onlineCount(readInst)
		if total == 0 {
			time.Sleep(50 * time.Millisecond)
			continue // node not yet mirrored on this instance; not a flicker sample
		}
		res.reads++
		if online < res.minOnline {
			res.minOnline = online
		}
		isZero := online == 0
		if isZero {
			res.zeroReads++
		}
		if isZero != prevZero {
			res.transitions++
			prevZero = isZero
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	for _, in := range insts {
		if fs, ok := in.b.shared.(*faultyStore); ok {
			res.sharedFails += fs.fails.Load()
			res.sharedOKs += fs.oks.Load()
		}
	}
	return res
}

func flickRedisURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("ROGERAI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set ROGERAI_TEST_REDIS_URL (a real Valkey/redis) to run the flicker soak")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("bad ROGERAI_TEST_REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	keys, _ := rdb.Keys(ctx, keyPrefix+"*").Result()
	if len(keys) > 0 {
		rdb.Del(ctx, keys...)
	}
	return url
}

// scaleFlickTimings shrinks the liveness clock 10x for the soak (keeping the prod
// ratios) and restores it after.
func scaleFlickTimings(t *testing.T) {
	t.Helper()
	pTTL, pThrottle, pSync := nodeTTL, persistThrottle, syncTickInterval
	nodeTTL = 4500 * time.Millisecond
	persistThrottle = 2000 * time.Millisecond
	syncTickInterval = 500 * time.Millisecond
	t.Cleanup(func() {
		nodeTTL, persistThrottle, syncTickInterval = pTTL, pThrottle, pSync
	})
}

// TestProbeVetoTimeQualified locks the flicker fix at the unit level (no Valkey): the
// /discover dead-upstream veto is qualified by RECENT positive serving evidence, so an
// intermittently-failing but recently-serving node stays ONLINE, while a node with NO
// recent evidence and a dead streak is still hidden (the approved dead-node contract).
func TestProbeVetoTimeQualified(t *testing.T) {
	pTTL := nodeTTL
	nodeTTL = 45 * time.Second
	defer func() { nodeTTL = pTTL }()

	pub, _, _ := ed25519.GenerateKey(nil)
	hexPub := hex.EncodeToString(pub)
	reg := protocol.NodeRegistration{NodeID: "n", PubKey: hexPub, Offers: []protocol.ModelOffer{{Model: "m"}}}
	now := time.Now()

	newB := func(fails int, lastMeasured time.Time) *broker {
		b := deadProbeBroker()
		b.probeSched = map[string]*probeState{}
		b.lastSeen["n"] = now // heartbeat-fresh
		b.trust["n"] = trustState{probed: true, probeFails: fails}
		if !lastMeasured.IsZero() {
			b.probeSched["n"] = &probeState{lastMeasured: lastMeasured}
		}
		return b
	}
	online := func(b *broker) bool {
		offers := b.enrichOffersForNode(nil, reg, now, nil, false)
		if len(offers) != 1 {
			t.Fatalf("want 1 offer, got %d", len(offers))
		}
		return offers[0].Online
	}

	// Dead streak BUT a passing probe within nodeTTL: FLICKERING/recovering -> stays ONLINE.
	if !online(newB(probeDeadStreak+2, now.Add(-nodeTTL/3))) {
		t.Fatal("a heartbeat-live node with a recent passing probe must stay ONLINE despite a dead streak (flicker fix)")
	}
	// Dead streak and NO positive evidence for a full nodeTTL: genuinely dead -> OFFLINE.
	if online(newB(probeDeadStreak, now.Add(-2*nodeTTL))) {
		t.Fatal("a node with a dead streak and no recent serving evidence must be OFFLINE (dead-node contract)")
	}
	// Dead streak and NEVER measured (zero lastMeasured): OFFLINE (approved contract).
	if online(newB(probeDeadStreak, time.Time{})) {
		t.Fatal("a never-served node with a dead streak must be OFFLINE")
	}
	// Below the streak: ONLINE regardless of evidence age.
	if !online(newB(probeDeadStreak-1, time.Time{})) {
		t.Fatal("a node below the dead streak must be ONLINE")
	}
}

// TestDiscoverFlicker_SingleInstance_ProbeFailing is the PRIMARY repro: ONE instance +
// real Valkey, a node that heartbeats continuously (so it is provably live) but whose
// canary probe fails intermittently. A heartbeat-live node must NEVER read 0-online.
func TestDiscoverFlicker_SingleInstance_ProbeFailing(t *testing.T) {
	url := flickRedisURL(t)
	scaleFlickTimings(t)
	res := runFlickSoak(t, url, flickCfg{
		instances:      1,
		nodes:          1,
		probe:          true,
		canaryFailRate: 0.5,
		soak:           20 * time.Second,
	})
	t.Logf("single-instance probe-failing: reads=%d zero-online=%d minOnline=%d transitions=%d",
		res.reads, res.zeroReads, res.minOnline, res.transitions)
	if res.zeroReads > 0 {
		t.Fatalf("FLICKER: %d/%d reads showed 0-online for a heartbeat-live node (transitions=%d)",
			res.zeroReads, res.reads, res.transitions)
	}
}

// TestDiscoverFlicker_TwoInstance_SharedWriteFailing is the cross-instance repro: a peer
// reads /discover while instance A's durable shared last_seen writes fail intermittently.
// The peer must keep the node online (its liveness must not age past TTL from a missed write).
func TestDiscoverFlicker_TwoInstance_SharedWriteFailing(t *testing.T) {
	url := flickRedisURL(t)
	scaleFlickTimings(t)
	res := runFlickSoak(t, url, flickCfg{
		instances:       2,
		nodes:           1,
		probe:           false,
		sharedWriteFail: 0.5,
		soak:            25 * time.Second,
	})
	t.Logf("two-instance shared-write-failing: reads=%d zero-online=%d minOnline=%d transitions=%d sharedFails=%d sharedOKs=%d",
		res.reads, res.zeroReads, res.minOnline, res.transitions, res.sharedFails, res.sharedOKs)
	if res.zeroReads > 0 {
		t.Fatalf("FLICKER: %d/%d reads showed 0-online on the peer (transitions=%d, injected shared-write fails=%d)",
			res.zeroReads, res.reads, res.transitions, res.sharedFails)
	}
}

// TestDiscoverFlicker_CleanBaseline is the control: no injected faults, healthy canaries.
// It must be flat-zero flicker both single- and two-instance.
func TestDiscoverFlicker_CleanBaseline(t *testing.T) {
	url := flickRedisURL(t)
	scaleFlickTimings(t)
	for _, n := range []int{1, 2} {
		t.Run(fmt.Sprintf("instances=%d", n), func(t *testing.T) {
			res := runFlickSoak(t, url, flickCfg{
				instances:      n,
				nodes:          2,
				probe:          true,
				canaryFailRate: 0,
				soak:           15 * time.Second,
			})
			t.Logf("clean instances=%d: reads=%d zero-online=%d minOnline=%d", n, res.reads, res.zeroReads, res.minOnline)
			if res.zeroReads > 0 {
				t.Fatalf("clean baseline flickered: %d/%d reads 0-online", res.zeroReads, res.reads)
			}
		})
	}
}
