package main

// cross_instance_bdd_test.go makes features/multinode/liveness_churn.feature and
// features/multinode/cross_instance_relay.feature EXECUTABLE (task #52): the node-
// liveness churn root cause + the cross-instance relay contract, driven end to end.
//
// REAL DEPS, NO MOCKS (repo testcontainer pattern, like ROGERAI_TEST_DATABASE_URL):
//   - Broker instances run the FULL route table over REAL HTTP (httptest servers
//     wrapping streamSafeHandler(b.routes())), sharing ONE broker signing key + ONE
//     store, exactly as the 2-instance production deploy does.
//   - Shared state is a REAL Valkey container when ROGERAI_TEST_REDIS_URL is set (CI
//     service container / local podman), else an in-process miniredis speaking the
//     real Redis protocol. Each instance gets its OWN client, as in production.
//   - The store is the REAL Postgres (ROGERAI_TEST_DATABASE_URL) when set, else the
//     in-memory reference implementation. Ids are per-scenario unique so the shared
//     Postgres never cross-pollinates scenarios (the parityStores convention).
//   - The node is a real signed registration + a real long-polling tunnel loop that
//     mirrors internal/agent's reregistrar EXACTLY: heartbeat/poll with the live
//     bridge token; on 404/401/403 rotate the token and re-register (recover()).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/redis/go-redis/v9"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// xiRedisURL returns the shared-state backend URL for one scenario: the REAL Valkey
// container when ROGERAI_TEST_REDIS_URL is set (its rogerai:* namespace flushed so
// scenarios are independent), else a fresh in-process miniredis.
func xiRedisURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("ROGERAI_TEST_REDIS_URL"); url != "" {
		opt, err := redis.ParseURL(url)
		if err != nil {
			t.Fatalf("ROGERAI_TEST_REDIS_URL: %v", err)
		}
		rdb := redis.NewClient(opt)
		defer rdb.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		keys, err := rdb.Keys(ctx, keyPrefix+"*").Result()
		if err != nil {
			t.Fatalf("flush %s*: %v", keyPrefix, err)
		}
		if len(keys) > 0 {
			if err := rdb.Del(ctx, keys...).Err(); err != nil {
				t.Fatalf("flush %s*: %v", keyPrefix, err)
			}
		}
		return url
	}
	mr := miniredis.RunT(t)
	return "redis://" + mr.Addr()
}

// xiStore returns the ONE store both instances share: real Postgres when
// ROGERAI_TEST_DATABASE_URL is set, else the in-memory reference.
func xiStore(t *testing.T) store.Store {
	t.Helper()
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		pg, err := store.NewPostgres(dsn)
		if err != nil {
			t.Fatalf("postgres: %v", err)
		}
		return pg
	}
	return store.NewMem()
}

// xiNonce uniquifies ids on the shared Postgres (TOFU node bindings, wallets, grants
// persist across runs there).
func xiNonce() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	return strconv.FormatInt(n.Int64(), 36)
}

// xiInst is one broker instance: the broker + its real HTTP server.
type xiInst struct {
	b   *broker
	srv *httptest.Server
}

func (i *xiInst) url() string { return i.srv.URL }

// xiNode mirrors internal/agent's node behavior over real HTTP.
type xiNode struct {
	id       string // real (nonce-suffixed) node id
	specName string // the name the scenario used
	pub      string
	priv     ed25519.PrivateKey
	token    string
	model    string
	priceOut float64
	station  string
	private  bool // register as a --private band (owner-signed, separate mirror namespace)

	ownerPriv ed25519.PrivateKey // set when the register is owner-signed
	ownerPub  string

	hc *http.Client

	mu       sync.Mutex
	beats    []int
	reregs   int
	pollWG   sync.WaitGroup
	pollErr  error
	polled   *protocol.Job // the job the poller received (nil = none)
	pollCode int
	resCode  int // the /agent/result POST status (0 = not posted)
	strmCode int // the /agent/stream POST status (0 = not posted)
}

// register signs and POSTs the node's registration to inst; ownerSigned adds the
// signed-identity headers so the broker binds the node to the owner account.
func (n *xiNode) register(t *testing.T, inst *xiInst) error {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: n.id, PubKey: n.pub, BridgeToken: n.token, TS: time.Now().Unix(),
		Station: n.station, Private: n.private,
		Offers: []protocol.ModelOffer{{Model: n.model, PriceOut: n.priceOut, Ctx: 4096}},
	}
	reg.SignRegistration(n.priv)
	body, _ := json.Marshal(reg)
	req, _ := http.NewRequest(http.MethodPost, inst.url()+"/nodes/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if n.ownerPriv != nil {
		pub, ts, sig := protocol.SignRequest(n.ownerPriv, req.Method, "/nodes/register", body)
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		req.Header.Set(protocol.HeaderSig, sig)
	}
	resp, err := n.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register on %s = %d: %s", inst.url(), resp.StatusCode, b)
	}
	return nil
}

// beat sends one heartbeat to inst with the LIVE token. On 404/401/403 it does what
// reregistrar.recover does: rotate the token, re-register (against the same instance
// the failing request hit - the LB gives no other choice), count the re-register.
func (n *xiNode) beat(t *testing.T, inst *xiInst) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"node_id": n.id})
	req, _ := http.NewRequest(http.MethodPost, inst.url()+"/nodes/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+n.token)
	resp, err := n.hc.Do(req)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	resp.Body.Close()
	n.mu.Lock()
	n.beats = append(n.beats, resp.StatusCode)
	n.mu.Unlock()
	if brokerForgotXI(resp.StatusCode) {
		n.mu.Lock()
		n.reregs++
		n.token = fmt.Sprintf("tok-rot-%d-%s", n.reregs, xiNonce())
		n.mu.Unlock()
		if err := n.register(t, inst); err != nil {
			t.Fatalf("recover re-register: %v", err)
		}
	}
	return resp.StatusCode
}

// brokerForgotXI mirrors internal/agent's brokerForgot (404/401/403 -> re-register).
func brokerForgotXI(status int) bool {
	return status == http.StatusNotFound || status == http.StatusUnauthorized || status == http.StatusForbidden
}

// pollServe long-polls pollInst ONCE for a job and serves it: waits delay, then either
// streams SSE chunks to strmInst (stream mode) and posts the receipt, or posts the
// signed result to resInst. Runs in the background; join with pollWG.Wait().
func (n *xiNode) pollServe(t *testing.T, pollInst, resInst, strmInst *xiInst, delay time.Duration, chunks []string, completion string) {
	t.Helper()
	n.pollWG.Add(1)
	go func() {
		defer n.pollWG.Done()
		req, _ := http.NewRequest(http.MethodGet, pollInst.url()+"/agent/poll?node="+n.id, nil)
		req.Header.Set("Authorization", "Bearer "+n.token)
		hc := &http.Client{Timeout: 35 * time.Second}
		resp, err := hc.Do(req)
		if err != nil {
			n.pollErr = err
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		n.mu.Lock()
		n.pollCode = resp.StatusCode
		n.mu.Unlock()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var job protocol.Job
		if err := json.Unmarshal(body, &job); err != nil {
			n.pollErr = err
			return
		}
		n.mu.Lock()
		n.polled = &job
		n.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if len(chunks) > 0 {
			// Stream the SSE chunks to strmInst, then post the receipt like the real node.
			var sse bytes.Buffer
			for _, c := range chunks {
				fmt.Fprintf(&sse, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			}
			sse.WriteString("data: [DONE]\n\n")
			sreq, _ := http.NewRequest(http.MethodPost, strmInst.url()+"/agent/stream?node="+n.id+"&job="+job.ID, bytes.NewReader(sse.Bytes()))
			sreq.Header.Set("Authorization", "Bearer "+n.token)
			sresp, serr := hc.Do(sreq)
			if serr != nil {
				n.pollErr = serr
				return
			}
			io.Copy(io.Discard, sresp.Body)
			sresp.Body.Close()
			n.mu.Lock()
			n.strmCode = sresp.StatusCode
			n.mu.Unlock()
			completion = strings.Join(chunks, "")
		}
		res := miSignedResult(job.ID, n.id, n.model, completion, n.priv, 200)
		rb, _ := json.Marshal(res)
		rreq, _ := http.NewRequest(http.MethodPost, resInst.url()+"/agent/result?node="+n.id, bytes.NewReader(rb))
		rreq.Header.Set("Authorization", "Bearer "+n.token)
		rresp, rerr := hc.Do(rreq)
		if rerr != nil {
			n.pollErr = rerr
			return
		}
		io.Copy(io.Discard, rresp.Body)
		rresp.Body.Close()
		n.mu.Lock()
		n.resCode = rresp.StatusCode
		n.mu.Unlock()
	}()
	// Let the long-poll (and, multi-instance, its bus subscription) land before any
	// dispatch - mirrors the dispatch BDD's settle.
	time.Sleep(300 * time.Millisecond)
}

// xiState is the shared world for BOTH multinode BDD suites.
type xiState struct {
	t     *testing.T
	nonce string

	db       store.Store
	redisURL string
	brokerPr ed25519.PrivateKey

	inst map[string]*xiInst // "A", "B" ("A" alone for single-broker scenarios)

	node *xiNode

	// duNodes holds the multiple nodes a discovery-union scenario registers (the
	// single-node harness keeps s.node; the union suite needs a fleet).
	duNodes []*xiNode

	// consumer
	consumerPriv   ed25519.PrivateKey
	consumerWallet string
	startBalance   float64

	// owner (paid/grant scenarios)
	ownerPriv   ed25519.PrivateKey
	ownerPub    string
	grantSecret string
	grantID     string

	// relay outcome
	relayCode    int
	relayBody    []byte
	relayHeaders http.Header
	relayDone    chan struct{}

	prevRelayWait time.Duration
}

func (s *xiState) reset(t *testing.T) {
	s.t = t
	s.nonce = xiNonce()
	s.db = nil
	s.redisURL = ""
	_, s.brokerPr, _ = ed25519.GenerateKey(nil)
	s.inst = map[string]*xiInst{}
	s.node = nil
	s.duNodes = nil
	s.consumerPriv = nil
	s.consumerWallet = ""
	s.startBalance = 0
	s.ownerPriv = nil
	s.ownerPub = ""
	s.grantSecret, s.grantID = "", ""
	s.relayCode, s.relayBody, s.relayHeaders = 0, nil, nil
	s.relayDone = nil
	// Keep the non-stream relay window short so the give-up scenario runs in seconds;
	// every completing scenario finishes far inside it. Restored in the After hook.
	s.prevRelayWait = nonStreamRelayWait
	nonStreamRelayWait = 5 * time.Second
}

func (s *xiState) cleanup() {
	if s.node != nil {
		s.node.pollWG.Wait()
	}
	for _, i := range s.inst {
		i.srv.Close()
	}
	nonStreamRelayWait = s.prevRelayWait
}

// newInstance builds one broker instance on the shared db + valkey, running the full
// route table over a real HTTP server.
func (s *xiState) newInstance(name string, multi bool) *xiInst {
	b := relayBroker(s.db)
	b.priv = s.brokerPr // one signing identity across instances, as in production
	b.seedFunds = 100   // production seeds new wallets (the free-credit seed ledger row)
	vs, err := newValkeyStore(s.redisURL)
	if err != nil {
		s.t.Fatalf("valkey connect: %v", err)
	}
	s.t.Cleanup(func() { _ = vs.Close() })
	b.shared = vs
	if multi {
		b.multiInstance = true
		b.instanceID = newInstanceID()
		b.peerInflight = map[string]int{}
	}
	inst := &xiInst{b: b, srv: httptest.NewServer(streamSafeHandler(b.routes()))}
	s.inst[name] = inst
	return inst
}

func (s *xiState) sweepAll() {
	for _, i := range s.inst {
		i.b.syncLivenessOnce()
	}
}

// --- Given: topology ---------------------------------------------------------

func (s *xiState) singleBroker(flag string) error {
	s.db = xiStore(s.t)
	s.redisURL = xiRedisURL(s.t)
	s.newInstance("A", flag == "ON")
	return nil
}

func (s *xiState) twoBrokers(flag string) error {
	s.db = xiStore(s.t)
	s.redisURL = xiRedisURL(s.t)
	s.newInstance("A", flag == "ON")
	s.newInstance("B", flag == "ON")
	return nil
}

// --- Given: node registration --------------------------------------------------

func (s *xiState) mkNode(specName, model string, priceOut float64, ownerSigned bool) *xiNode {
	pub, priv, _ := ed25519.GenerateKey(nil)
	n := &xiNode{
		id:       specName + "-" + s.nonce,
		specName: specName,
		pub:      hex.EncodeToString(pub),
		priv:     priv,
		token:    "tok-0-" + s.nonce,
		model:    model,
		priceOut: priceOut,
		hc:       &http.Client{Timeout: 10 * time.Second},
	}
	if ownerSigned {
		opub, opriv, _ := ed25519.GenerateKey(nil)
		n.ownerPriv, n.ownerPub = opriv, hex.EncodeToString(opub)
		s.ownerPriv, s.ownerPub = opriv, n.ownerPub
		gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
		if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64(), Login: "op-" + s.nonce, Pubkey: n.ownerPub}); err != nil {
			s.t.Fatalf("bind owner: %v", err)
		}
	}
	s.node = n
	return n
}

func (s *xiState) nodeRegisters(name string) error {
	return s.mkNode(name, "free-m", 0, false).register(s.t, s.inst["A"])
}

func (s *xiState) nodeRegistersOn(name, instName string) error {
	return s.mkNode(name, "free-m", 0, false).register(s.t, s.inst[instName])
}

func (s *xiState) nodeRegistersOffering(name, instName, kind, model string) error {
	price := 0.0
	owner := false
	if kind == "paid" {
		price = 0.5
		owner = true // a priced register HARD-requires a GitHub-linked owner
	}
	n := s.mkNode(name, model, price, owner)
	if err := n.register(s.t, s.inst[instName]); err != nil {
		return err
	}
	// One heartbeat + a sweep so the shared liveness carries the node everywhere a
	// relay might pick it (production: the node heartbeats within seconds).
	n.beat(s.t, s.inst[instName])
	s.sweepAll()
	return nil
}

func (s *xiState) nodeRegistersPrivateOn(name, instName string) error {
	n := s.mkNode(name, "free-m", 0, true) // a private band HARD-requires an owner
	n.private = true
	return n.register(s.t, s.inst[instName])
}

func (s *xiState) nodeRegistersBoundToOwner(name, instName, model string) error {
	n := s.mkNode(name, model, 0, true) // free but OWNER-SIGNED -> bound to the account
	if err := n.register(s.t, s.inst[instName]); err != nil {
		return err
	}
	n.beat(s.t, s.inst[instName])
	s.sweepAll()
	return nil
}

// --- Given: consumer / owner money ----------------------------------------------

func (s *xiState) fundedConsumer() error {
	_, priv, _ := ed25519.GenerateKey(nil)
	s.consumerPriv = priv
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64(), Login: "buyer-" + s.nonce, Pubkey: pubHex}); err != nil {
		return err
	}
	s.consumerWallet = "u_gh_" + strconv.FormatInt(gid.Int64(), 10)
	// A REAL-funded consumer: trigger the one-time free-credit seed (the seed IS a
	// ledger row, by design), SPEND it down (P0-1: seed-funded spend mints operators
	// no earning, pinned by the money features), then top up with real credits - so
	// the relay under test is real-money-funded and the operator-earning assertion is
	// exact. The relay-time ensureSeeded is then the idempotent no-op it is for every
	// returning production wallet.
	if _, err := s.db.BalanceOf(s.consumerWallet, 100); err != nil {
		return err
	}
	// ownerShare must be nonzero for the seed to be CONSUMED (realEarnShare returns
	// early on ownerShare<=0 without touching seedRemain); the sink still earns $0
	// because the whole cost is seed-funded (P0-1).
	if _, err := s.db.Settle(s.consumerWallet, "seed-sink-"+s.nonce, 100, 1,
		protocol.UsageReceipt{RequestID: "seed-burn-" + s.nonce, Model: "seed-burn", TS: time.Now().Unix()}); err != nil {
		return err
	}
	if _, err := s.db.AddCredits(s.consumerWallet, 100); err != nil {
		return err
	}
	s.startBalance, _ = s.db.BalanceOf(s.consumerWallet, 0)
	return nil
}

// --- Given: tunnels --------------------------------------------------------------

func (s *xiState) nodePolls(inst string) error {
	s.node.pollServe(s.t, s.inst[inst], s.inst[inst], s.inst[inst], 0, nil, "xi-completion")
	return nil
}

func (s *xiState) nodePollsPostsTo(pollInst, resInst string) error {
	s.node.pollServe(s.t, s.inst[pollInst], s.inst[resInst], s.inst[pollInst], 0, nil, "xi-completion")
	return nil
}

func (s *xiState) nodePollsAndStreams(inst string) error {
	s.node.pollServe(s.t, s.inst[inst], s.inst[inst], s.inst[inst], 0, []string{"alpha ", "bravo ", "charlie"}, "")
	return nil
}

func (s *xiState) nodePollsStreamsTo(pollInst, strmInst string) error {
	s.node.pollServe(s.t, s.inst[pollInst], s.inst[pollInst], s.inst[strmInst], 0, []string{"alpha ", "bravo ", "charlie"}, "")
	return nil
}

func (s *xiState) nodePollsDelayed(inst string) error {
	s.node.pollServe(s.t, s.inst[inst], s.inst[inst], s.inst[inst], 8*time.Second, nil, "late-completion")
	return nil
}

func (s *xiState) noPoller() error { return nil } // explicit marker: nobody polls

func (s *xiState) nodeMovesPollTo(inst string) error { return s.nodePolls(inst) }

func (s *xiState) noRegistrySyncYet(string) error { return nil } // marker: do NOT sweep

// --- Given: grace / poison / persistence -----------------------------------------

func (s *xiState) graceLapsed() error {
	for _, i := range s.inst {
		i.b.mu.Lock()
		if i.b.localRegAt == nil {
			i.b.localRegAt = map[string]time.Time{}
		}
		i.b.localRegAt[s.node.id] = time.Now().Add(-2 * syncLocalRegisterGrace)
		i.b.mu.Unlock()
	}
	return nil
}

func (s *xiState) graceLapsedOn(inst string) error {
	i := s.inst[inst]
	i.b.mu.Lock()
	if i.b.localRegAt == nil {
		i.b.localRegAt = map[string]time.Time{}
	}
	i.b.localRegAt[s.node.id] = time.Now().Add(-2 * syncLocalRegisterGrace)
	i.b.mu.Unlock()
	return nil
}

func (s *xiState) reregistersRotatedOn(inst string) error {
	s.node.mu.Lock()
	s.node.token = "tok-rot-manual-" + s.nonce
	s.node.mu.Unlock()
	if err := s.node.register(s.t, s.inst[inst]); err != nil {
		return err
	}
	// The scenario then observes STEADY STATE (>15s later in production): the
	// register grace has lapsed everywhere.
	return s.graceLapsed()
}

func (s *xiState) poisonSharedRegistry(name string) error {
	stale := protocol.NodeRegistration{
		NodeID: s.node.id, PubKey: s.node.pub, BridgeToken: "tok-STALE-" + s.nonce,
		TS:     time.Now().Add(-time.Hour).Unix(),
		Offers: []protocol.ModelOffer{{Model: s.node.model, Ctx: 4096}},
	}
	raw, _ := json.Marshal(stale)
	return s.inst["A"].b.shared.putNode(s.node.id, raw, livenessTTL)
}

func (s *xiState) persistedRegistration(name string) error {
	// The store may be built lazily (this Given can precede the broker Given).
	if s.db == nil {
		s.db = xiStore(s.t)
		s.redisURL = xiRedisURL(s.t)
	}
	n := s.mkNode(name, "free-m", 0, false)
	reg := protocol.NodeRegistration{
		NodeID: n.id, PubKey: n.pub, BridgeToken: n.token, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: n.model, Ctx: 4096}},
	}
	return s.db.UpsertNode(store.NodeRecord{NodeID: n.id, Reg: reg, LastSeen: time.Now().Unix()})
}

func (s *xiState) singleBrokerAfterPersist(flag string) error {
	if s.db == nil {
		return s.singleBroker(flag)
	}
	// db + redis already built by the persisted-registration Given.
	s.newInstance("A", flag == "ON")
	return nil
}

func (s *xiState) rehydrates() error {
	s.inst["A"].b.rehydrateNodes()
	return nil
}

// --- Given/When: bans + grants ----------------------------------------------------
// All three drive the REAL HTTP endpoints (the #52 review ask): the ban through the
// public corroborated-report flow, the grant mint/revoke through the owner-signed
// /grants surface - never a direct store write from the When side.

// instanceBansNode ejects the node the way production does: enough DISTINCT
// reporters name it via POST /report (category=abuse) to cross the corroborated
// auto-eject threshold (the ROGERAI_REPORT_EJECT_AT knob, here configured to 2).
func (s *xiState) instanceBansNode(inst string) error {
	i := s.inst[inst]
	i.b.reportEjectAt = 2                                        // corroborated auto-eject ON, as production configures it
	for _, ip := range []string{"203.0.113.7", "198.51.100.9"} { // two DISTINCT reporters
		body, _ := json.Marshal(map[string]string{
			"category": "abuse", "node_id": s.node.id, "detail": "xi: abusive station",
		})
		req, _ := http.NewRequest(http.MethodPost, i.url()+"/report", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("CF-Connecting-IP", ip)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("/report = %d: %s", resp.StatusCode, rb)
		}
	}
	if !i.b.isBanned(s.node.id) {
		return fmt.Errorf("two distinct corroborating /report POSTs did not eject the node on instance %s", inst)
	}
	return nil
}

// ownerHTTP sends one owner-signed request to inst (the CLI's signed-management
// path: Ed25519 over method+path+body).
func (s *xiState) ownerHTTP(method, path string, body []byte, inst *xiInst) (int, []byte, error) {
	req, _ := http.NewRequest(method, inst.url()+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(s.ownerPriv, method, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, rb, nil
}

func (s *xiState) ownerMintsGrant() error {
	body, _ := json.Marshal(map[string]any{"name": "xi-" + s.nonce})
	code, rb, err := s.ownerHTTP(http.MethodPost, "/grants", body, s.inst["A"])
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("POST /grants = %d: %s", code, rb)
	}
	var out struct {
		Secret string `json:"secret"`
		Grant  struct {
			ID string `json:"id"`
		} `json:"grant"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return err
	}
	if out.Secret == "" || out.Grant.ID == "" {
		return fmt.Errorf("mint response carries no secret/id: %s", rb)
	}
	s.grantSecret, s.grantID = out.Secret, out.Grant.ID
	return nil
}

func (s *xiState) ownerRevokesGrant() error {
	code, rb, err := s.ownerHTTP(http.MethodDelete, "/grants/"+s.grantID, nil, s.inst["A"])
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("DELETE /grants/%s = %d: %s", s.grantID, code, rb)
	}
	return nil
}

// --- When: heartbeats ---------------------------------------------------------------

// beatCycle runs beats with a sync sweep before every beat, so the run spans well
// over two sweep cycles (8 sweeps).
func (s *xiState) beatCycle(targets []*xiInst) {
	for i := 0; i < 8; i++ {
		s.sweepAll()
		s.node.beat(s.t, targets[i%len(targets)])
		time.Sleep(30 * time.Millisecond)
	}
}

func (s *xiState) heartbeatsSingle() error {
	s.beatCycle([]*xiInst{s.inst["A"]})
	return nil
}

func (s *xiState) heartbeatsAlternating() error {
	s.beatCycle([]*xiInst{s.inst["A"], s.inst["B"]})
	return nil
}

func (s *xiState) heartbeatsBoth() error {
	s.node.beat(s.t, s.inst["A"])
	s.node.beat(s.t, s.inst["B"])
	return nil
}

func (s *xiState) heartbeatsOn(inst string) error {
	s.node.beat(s.t, s.inst[inst])
	return nil
}

// absentFromDiscover asserts the node never surfaces in inst's PUBLIC /discover -
// meaningful because the preceding heartbeat made inst LEARN the band (lazy-learn),
// so the absence is the private filter working, not ignorance.
func (s *xiState) absentFromDiscover(inst string) error {
	resp, err := http.Get(s.inst[inst].url() + "/discover")
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/discover = %d", resp.StatusCode)
	}
	if bytes.Contains(body, []byte(s.node.id)) {
		return fmt.Errorf("PRIVATE band %s leaked into instance %s's public /discover: %s", s.node.id, inst, body)
	}
	// The absence must not be vacuous: the instance does hold the band internally.
	s.inst[inst].b.mu.Lock()
	_, known := s.inst[inst].b.nodes[s.node.id]
	s.inst[inst].b.mu.Unlock()
	if !known {
		return fmt.Errorf("instance %s never learned the band - the absence check would be vacuous", inst)
	}
	return nil
}

func (s *xiState) sweepRepeatedly() error {
	for i := 0; i < 5; i++ {
		s.sweepAll()
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (s *xiState) sweepBoth() error {
	s.sweepAll()
	return nil
}

// --- When: relays --------------------------------------------------------------------

// relayHTTP POSTs a relay over real HTTP. auth: "signed" (fresh anon keypair),
// "consumer" (the funded logged-in consumer), or "grant" (Bearer grant secret).
func (s *xiState) relayHTTP(inst *xiInst, model, auth string, stream bool) error {
	payload := map[string]any{"model": model, "max_tokens": 8}
	if stream {
		payload["stream"] = true
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, inst.url()+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	switch auth {
	case "grant":
		req.Header.Set("Authorization", "Bearer "+s.grantSecret)
	case "consumer":
		pub, ts, sig := protocol.SignRequest(s.consumerPriv, req.Method, "/v1/chat/completions", body)
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		req.Header.Set(protocol.HeaderSig, sig)
	default: // fresh anonymous signed identity
		_, priv, _ := ed25519.GenerateKey(nil)
		pub, ts, sig := protocol.SignRequest(priv, req.Method, "/v1/chat/completions", body)
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		req.Header.Set(protocol.HeaderSig, sig)
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	defer resp.Body.Close()
	s.relayCode = resp.StatusCode
	s.relayHeaders = resp.Header
	s.relayBody, _ = io.ReadAll(resp.Body)
	return nil
}

func (s *xiState) consumerRelaysOn(model, inst string) error {
	return s.relayHTTP(s.inst[inst], model, "consumer", false)
}

func (s *xiState) anonRelaysOn(model, inst string) error {
	return s.relayHTTP(s.inst[inst], model, "signed", false)
}

func (s *xiState) anonRelaysStreamingOn(model, inst string) error {
	return s.relayHTTP(s.inst[inst], model, "signed", true)
}

func (s *xiState) grantRelaysOn(model, inst string) error {
	return s.relayHTTP(s.inst[inst], model, "grant", false)
}

func (s *xiState) relayWhilePollingSingle(model string) error {
	// The node heartbeats (as production nodes always do) and the sweep runs, so the
	// relay's pick sees fresh liveness wherever it lands.
	s.node.beat(s.t, s.inst["A"])
	s.sweepAll()
	if err := s.nodePolls("A"); err != nil {
		return err
	}
	return s.anonRelaysOn(model, "A")
}

func (s *xiState) relayOnWhilePolling(model, relayInst, pollInst string) error {
	s.node.beat(s.t, s.inst["A"])
	s.sweepAll()
	if err := s.nodePolls(pollInst); err != nil {
		return err
	}
	return s.anonRelaysOn(model, relayInst)
}

// --- When: forged poll ------------------------------------------------------------------

func (s *xiState) pollsWithForgedToken(inst string) error {
	req, _ := http.NewRequest(http.MethodGet, s.inst[inst].url()+"/agent/poll?node="+s.node.id, nil)
	req.Header.Set("Authorization", "Bearer forged-"+s.nonce)
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	s.node.mu.Lock()
	s.node.pollCode = resp.StatusCode
	s.node.mu.Unlock()
	return nil
}

// --- Then: heartbeat verdicts --------------------------------------------------------

func (s *xiState) everyHeartbeat200() error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if len(s.node.beats) == 0 {
		return fmt.Errorf("no heartbeats recorded")
	}
	for i, c := range s.node.beats {
		if c != http.StatusOK {
			return fmt.Errorf("heartbeat #%d answered %d (all: %v) - the node would be told to re-register", i+1, c, s.node.beats)
		}
	}
	return nil
}

func (s *xiState) neverToldToReregister() error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if s.node.reregs != 0 {
		return fmt.Errorf("the node was told to re-register %d times (beats: %v) - the production ~10s churn", s.node.reregs, s.node.beats)
	}
	return nil
}

func (s *xiState) atMostOneReregister() error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if s.node.reregs > 1 {
		return fmt.Errorf("%d re-registers demanded (beats: %v) - rotation ping-pong did not converge", s.node.reregs, s.node.beats)
	}
	return nil
}

func (s *xiState) lastFourBeats200() error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if len(s.node.beats) < 4 {
		return fmt.Errorf("only %d heartbeats recorded", len(s.node.beats))
	}
	tail := s.node.beats[len(s.node.beats)-4:]
	for _, c := range tail {
		if c != http.StatusOK {
			return fmt.Errorf("converged tail = %v, want all 200 (all beats: %v)", tail, s.node.beats)
		}
	}
	return nil
}

func (s *xiState) heartbeatStill200() error {
	if c := s.node.beat(s.t, s.inst["A"]); c != http.StatusOK {
		return fmt.Errorf("post-ban heartbeat = %d, want 200 (a ban must not start a re-register storm)", c)
	}
	return nil
}

// --- Then: poll verdicts ----------------------------------------------------------------

func (s *xiState) pollDeliversJob() error {
	s.node.pollWG.Wait()
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if s.node.pollErr != nil {
		return fmt.Errorf("poll: %v", s.node.pollErr)
	}
	if brokerForgotXI(s.node.pollCode) {
		return fmt.Errorf("poll answered %d - the node would be told to re-register", s.node.pollCode)
	}
	if s.node.pollCode != http.StatusOK || s.node.polled == nil {
		return fmt.Errorf("poll = %d, no job delivered (relay answered %d: %s)", s.node.pollCode, s.relayCode, s.relayBody)
	}
	return nil
}

func (s *xiState) pollUnauthorized() error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if s.node.pollCode != http.StatusUnauthorized {
		return fmt.Errorf("forged-token poll = %d, want 401", s.node.pollCode)
	}
	return nil
}

// --- Then: registry / tunnel state --------------------------------------------------------

func (s *xiState) tunnelHeldUnderToken() error {
	b := s.inst["A"].b
	b.mu.Lock()
	tun := b.tunnels[s.node.id]
	b.mu.Unlock()
	if tun == nil {
		return fmt.Errorf("the node's tunnel is gone - a sweep evicted it")
	}
	s.node.mu.Lock()
	want := s.node.token
	s.node.mu.Unlock()
	if tun.token != want {
		return fmt.Errorf("tunnel token = %q, want the registered %q (a sweep rotated it - heartbeats would 401)", tun.token, want)
	}
	return nil
}

func (s *xiState) sharedRegistryHoldsNode(name string) error {
	raw, found, err := s.inst["A"].b.shared.getNode(s.node.id)
	if err != nil {
		return fmt.Errorf("shared getNode: %v", err)
	}
	if !found {
		return fmt.Errorf("node %q (%s) is NOT in the shared registry - a peer/scale-out could never learn it", name, s.node.id)
	}
	var reg protocol.NodeRegistration
	if err := json.Unmarshal(raw, &reg); err != nil {
		return err
	}
	s.node.mu.Lock()
	want := s.node.token
	s.node.mu.Unlock()
	if reg.BridgeToken != want {
		return fmt.Errorf("shared registry carries token %q, want the live %q", reg.BridgeToken, want)
	}
	return nil
}

// --- Then: relay verdicts -------------------------------------------------------------------

func (s *xiState) relay200WithCompletion() error {
	s.node.pollWG.Wait()
	if s.relayCode != http.StatusOK {
		return fmt.Errorf("relay = %d, want 200 (body: %s)", s.relayCode, s.relayBody)
	}
	if !bytes.Contains(s.relayBody, []byte("completion")) && !bytes.Contains(s.relayBody, []byte("alpha")) {
		return fmt.Errorf("relay body %q does not carry the node's completion", s.relayBody)
	}
	return nil
}

func (s *xiState) chargedOnceEarnsOnce() error {
	s.node.pollWG.Wait()
	time.Sleep(200 * time.Millisecond) // any late double-settle would land here
	bal, err := s.db.BalanceOf(s.consumerWallet, 0)
	if err != nil {
		return err
	}
	charged := s.startBalance - bal
	if charged <= 0 {
		return fmt.Errorf("consumer was not charged (balance %v -> %v)", s.startBalance, bal)
	}
	earned, err := s.db.EarningsOf(s.node.id)
	if err != nil {
		return err
	}
	wantEarn := charged * (1 - s.inst["A"].b.feeRate)
	if diff := earned - wantEarn; diff > 1e-9 || diff < -1e-9 {
		return fmt.Errorf("operator earned %v, want exactly %v (one settle of the %v charge at %.0f%% fee) - a double settle/earning would break here",
			earned, wantEarn, charged, s.inst["A"].b.feeRate*100)
	}
	// Stability re-check: nothing else lands later.
	time.Sleep(200 * time.Millisecond)
	bal2, _ := s.db.BalanceOf(s.consumerWallet, 0)
	if bal2 != bal {
		return fmt.Errorf("balance moved again after settle (%v -> %v) - double charge", bal, bal2)
	}
	return nil
}

func (s *xiState) streamChunksInOrder() error {
	s.node.pollWG.Wait()
	if s.relayCode != http.StatusOK {
		return fmt.Errorf("stream relay = %d, want 200", s.relayCode)
	}
	s.node.mu.Lock()
	strmCode := s.node.strmCode
	s.node.mu.Unlock()
	if strmCode != 0 && strmCode != http.StatusOK {
		return fmt.Errorf("the node's /agent/stream POST was answered %d - chunks were rejected, the client stream is empty", strmCode)
	}
	body := string(s.relayBody)
	ia, ib, ic := strings.Index(body, "alpha "), strings.Index(body, "bravo "), strings.Index(body, "charlie")
	if ia < 0 || ib < 0 || ic < 0 || !(ia < ib && ib < ic) {
		return fmt.Errorf("stream did not deliver the chunks in order (alpha@%d bravo@%d charlie@%d): %q", ia, ib, ic, body)
	}
	if !strings.Contains(body, "[DONE]") {
		return fmt.Errorf("stream did not end with the done marker: %q", body)
	}
	return nil
}

func (s *xiState) relay503NodeBusy() error {
	if s.relayCode != http.StatusServiceUnavailable {
		return fmt.Errorf("relay = %d, want an honest 503 (body: %s)", s.relayCode, s.relayBody)
	}
	if !bytes.Contains(s.relayBody, []byte("busy")) {
		return fmt.Errorf("503 body %q does not say the node is busy", s.relayBody)
	}
	return nil
}

func (s *xiState) relay503NoNode() error {
	if s.relayCode != http.StatusServiceUnavailable {
		return fmt.Errorf("relay = %d, want 503 (body: %s)", s.relayCode, s.relayBody)
	}
	if !bytes.Contains(s.relayBody, []byte("no node offers")) {
		return fmt.Errorf("503 body %q does not say no node offers the model", s.relayBody)
	}
	return nil
}

func (s *xiState) relayCleanTimeout() error {
	if s.relayCode != http.StatusGatewayTimeout {
		return fmt.Errorf("relay = %d, want a clean 504 timeout (body: %s)", s.relayCode, s.relayBody)
	}
	return nil
}

func (s *xiState) balanceUnchanged() error {
	s.node.pollWG.Wait()
	time.Sleep(200 * time.Millisecond)
	bal, err := s.db.BalanceOf(s.consumerWallet, 0)
	if err != nil {
		return err
	}
	if bal != s.startBalance {
		return fmt.Errorf("consumer balance %v, want the untouched %v - money leaked", bal, s.startBalance)
	}
	return nil
}

func (s *xiState) lateResultAbsorbed() error {
	s.node.pollWG.Wait()
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if s.node.pollErr != nil {
		return fmt.Errorf("node loop error: %v", s.node.pollErr)
	}
	if s.node.polled == nil {
		return fmt.Errorf("the job never reached the node - dispatch did not cross instances")
	}
	if s.node.resCode != http.StatusOK {
		return fmt.Errorf("the node's late result POST = %d, want 200 (absorbed at-most-once)", s.node.resCode)
	}
	return nil
}

func (s *xiState) operatorEarnedNothing() error {
	earned, err := s.db.EarningsOf(s.node.id)
	if err != nil {
		return err
	}
	if earned != 0 {
		return fmt.Errorf("operator earned %v from a request that was never delivered", earned)
	}
	return nil
}

func (s *xiState) sweepFindsNoOrphan() error {
	n, err := s.db.ReleaseStaleHolds(time.Now().Add(time.Minute))
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("the stale-hold sweep released %d hold(s) - the relay's own give-up path left an orphan", n)
	}
	return nil
}

func (s *xiState) relayUnauthorized() error {
	if s.relayCode != http.StatusUnauthorized {
		return fmt.Errorf("relay with revoked grant = %d, want 401 (body: %s)", s.relayCode, s.relayBody)
	}
	return nil
}

// --- suite wiring ------------------------------------------------------------------

func xiInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		st := &xiState{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			st.reset(t)
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			st.cleanup()
			return ctx, nil
		})

		// topology
		sc.Step(`^a single broker with the shared backend wired and the multi-instance flag (ON|OFF)$`, st.singleBrokerAfterPersist)
		sc.Step(`^two broker (?:processes|instances) A and B with the multi-instance flag (ON|OFF) sharing one Valkey and one store$`, st.twoBrokers)

		// node registration
		sc.Step(`^node "([^"]*)" registers over HTTP$`, st.nodeRegisters)
		sc.Step(`^node "([^"]*)" registers over HTTP on instance ([AB])$`, st.nodeRegistersOn)
		sc.Step(`^node "([^"]*)" registers over HTTP on instance ([AB]) offering (paid|free) model "([^"]*)"$`, st.nodeRegistersOffering)
		sc.Step(`^node "([^"]*)" registers over HTTP on instance ([AB]) offering free model "([^"]*)" bound to an owner$`, st.nodeRegistersBoundToOwner)
		sc.Step(`^node "([^"]*)" registers over HTTP on instance ([AB]) as a private band$`, st.nodeRegistersPrivateOn)
		sc.Step(`^a persisted registration for node "([^"]*)" in the store$`, st.persistedRegistration)

		// consumer / owner
		sc.Step(`^a funded logged-in consumer$`, st.fundedConsumer)
		sc.Step(`^the owner mints a grant key for the node via instance A$`, st.ownerMintsGrant)
		sc.Step(`^the owner revokes the grant via instance A$`, st.ownerRevokesGrant)

		// tunnels
		sc.Step(`^the node long-polls instance ([AB])$`, st.nodePolls)
		sc.Step(`^the node long-polls instance ([AB]) but posts results to instance ([AB])$`, st.nodePollsPostsTo)
		sc.Step(`^the node long-polls instance ([AB]) and streams its answer chunk by chunk$`, st.nodePollsAndStreams)
		sc.Step(`^the node long-polls instance ([AB]) and streams its answer to instance ([AB])$`, st.nodePollsStreamsTo)
		sc.Step(`^the node long-polls instance ([AB]) but delays its answer past the relay window$`, st.nodePollsDelayed)
		sc.Step(`^no poller is attached to the node on any instance$`, st.noPoller)
		sc.Step(`^the node moves its long-poll to instance ([AB])$`, st.nodeMovesPollTo)
		sc.Step(`^instance ([AB]) has not yet run a registry sync$`, st.noRegistrySyncYet)

		// grace / poison / persistence
		sc.Step(`^the node's local-register grace has lapsed$`, st.graceLapsed)
		sc.Step(`^the node's local-register grace has lapsed on instance ([AB])$`, st.graceLapsedOn)
		sc.Step(`^the node re-registers once with a rotated token on instance ([AB]) and the register grace lapses$`, st.reregistersRotatedOn)
		sc.Step(`^the shared registry is poisoned with a strictly older registration for "([^"]*)" carrying a stale token$`, st.poisonSharedRegistry)
		sc.Step(`^the broker rehydrates its node registry at startup$`, st.rehydrates)

		// bans
		sc.Step(`^instance A bans the node$`, func() error { return st.instanceBansNode("A") })

		// heartbeats / sweeps
		sc.Step(`^the node heartbeats every beat across more than two sweep cycles$`, st.heartbeatsSingle)
		sc.Step(`^the node heartbeats alternating between A and B across more than two sweep cycles$`, st.heartbeatsAlternating)
		sc.Step(`^the node heartbeats instance A and instance B$`, st.heartbeatsBoth)
		sc.Step(`^the node heartbeats instance ([AB])$`, st.heartbeatsOn)
		sc.Step(`^the node is absent from instance ([AB])'s public discovery$`, st.absentFromDiscover)
		sc.Step(`^the broker runs its liveness sync sweep repeatedly$`, st.sweepRepeatedly)
		sc.Step(`^both instances run their liveness sync sweep$`, st.sweepBoth)

		// relays / polls
		sc.Step(`^a consumer relays "([^"]*)" while the node polls the broker for work$`, st.relayWhilePollingSingle)
		sc.Step(`^a consumer relays "([^"]*)" on instance ([AB]) while the node polls instance ([AB]) for work$`, st.relayOnWhilePolling)
		sc.Step(`^the consumer relays "([^"]*)" on instance ([AB])$`, st.consumerRelaysOn)
		sc.Step(`^an anonymous signed consumer relays "([^"]*)" on instance ([AB])$`, st.anonRelaysOn)
		sc.Step(`^an anonymous signed consumer relays "([^"]*)" on instance ([AB]) with streaming on$`, st.anonRelaysStreamingOn)
		sc.Step(`^a consumer relays "([^"]*)" on instance ([AB]) with the revoked grant$`, st.grantRelaysOn)
		sc.Step(`^the node polls instance ([AB]) with a forged token$`, st.pollsWithForgedToken)

		// verdicts: heartbeats
		sc.Step(`^every heartbeat is answered 200$`, st.everyHeartbeat200)
		sc.Step(`^the node is never told to re-register$`, st.neverToldToReregister)
		sc.Step(`^at most one heartbeat demands a re-register$`, st.atMostOneReregister)
		sc.Step(`^the last four heartbeats are answered 200$`, st.lastFourBeats200)
		sc.Step(`^the node's heartbeat is still answered 200$`, st.heartbeatStill200)

		// verdicts: polls
		sc.Step(`^the poll authenticates and delivers the job$`, st.pollDeliversJob)
		sc.Step(`^the poll is rejected as unauthorized$`, st.pollUnauthorized)

		// verdicts: registry / tunnel
		sc.Step(`^the broker still holds the node's tunnel under its registered bridge token$`, st.tunnelHeldUnderToken)
		sc.Step(`^the shared registry holds node "([^"]*)" with its bridge token$`, st.sharedRegistryHoldsNode)

		// verdicts: relays / money
		sc.Step(`^the relay responds 200 with the node's completion$`, st.relay200WithCompletion)
		sc.Step(`^the consumer is charged exactly once and the operator earns exactly once$`, st.chargedOnceEarnsOnce)
		sc.Step(`^the stream delivers the chunks in order followed by the done marker$`, st.streamChunksInOrder)
		sc.Step(`^the relay responds 503 saying the node is busy$`, st.relay503NodeBusy)
		sc.Step(`^the relay responds 503 because no node offers the model$`, st.relay503NoNode)
		sc.Step(`^the relay gives up with a clean timeout$`, st.relayCleanTimeout)
		sc.Step(`^the consumer's balance is unchanged$`, st.balanceUnchanged)
		sc.Step(`^the node's late result is absorbed without error$`, st.lateResultAbsorbed)
		sc.Step(`^the operator has earned nothing$`, st.operatorEarnedNothing)
		sc.Step(`^the stale-hold sweep finds no orphaned hold$`, st.sweepFindsNoOrphan)
		sc.Step(`^the relay is rejected as unauthorized$`, st.relayUnauthorized)
	}
}

// TestExpireStaleAttestationsRepublishesUnderFlagOff pins the widened gate at the
// attestation-lapse republish (tunnel.go expireStaleAttestations): a confidential
// downgrade must reach the SHARED registry whenever a shared backend is wired - with
// the bus flag OFF too - or a peer process keeps granting the lapsed node the
// confidential tier off the stale mirror. Before the task #52 symmetry fix this
// republish was skipped under ROGERAI_MULTI_INSTANCE=0.
func TestExpireStaleAttestationsRepublishesUnderFlagOff(t *testing.T) {
	vs, err := newValkeyStore(xiRedisURL(t))
	if err != nil {
		t.Fatalf("valkey: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	b := relayBroker(store.NewMem())
	b.shared = vs // flag OFF: b.multiInstance stays false
	nodeID := "conf-lapse-" + xiNonce()
	reg := protocol.NodeRegistration{NodeID: nodeID, BridgeToken: "tok", Confidential: true,
		Offers: []protocol.ModelOffer{{Model: "free-m", Ctx: 4096}}}
	b.nodes[nodeID] = reg
	b.confidential[nodeID] = true
	b.attestedAt = map[string]time.Time{nodeID: time.Now().Add(-2 * time.Hour)}
	raw, _ := json.Marshal(reg)
	if err := vs.putNode(nodeID, raw, livenessTTL); err != nil {
		t.Fatal(err)
	}

	b.expireStaleAttestations(time.Now(), time.Hour) // the lapse sweep

	got, found, err := vs.getNode(nodeID)
	if err != nil || !found {
		t.Fatalf("shared registry lost the node (found=%v err=%v)", found, err)
	}
	var mirrored protocol.NodeRegistration
	if err := json.Unmarshal(got, &mirrored); err != nil {
		t.Fatal(err)
	}
	if mirrored.Confidential {
		t.Fatal("the shared mirror still claims Confidential=true after the re-attestation lapsed - a peer process would keep granting the confidential tier (the flag=0 republish gate)")
	}
}

func TestLivenessChurnBDD(t *testing.T) {
	// Same payout-ladder pins as the store parity suites: earnings are immediately
	// payable so the settle-exactly-once assertions read them directly.
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	suite := godog.TestSuite{
		ScenarioInitializer: xiInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/liveness_churn.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/liveness_churn scenarios failed (see godog output above)")
	}
}

// TestFlagOffRelayBDD runs features/multinode/flag_off_relay.feature: the HONEST
// degraded contract for two processes under ROGERAI_MULTI_INSTANCE=0 (registrations
// mirror, dispatch does not) - a foreign-process relay must 504 cleanly with the
// hold refunded in full while the owner process keeps serving.
func TestFlagOffRelayBDD(t *testing.T) {
	// Same payout-ladder pins as the store parity suites: earnings are immediately
	// payable so the settle-exactly-once assertions read them directly.
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	suite := godog.TestSuite{
		ScenarioInitializer: xiInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/flag_off_relay.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/flag_off_relay scenarios failed (see godog output above)")
	}
}

func TestCrossInstanceRelayBDD(t *testing.T) {
	// Same payout-ladder pins as the store parity suites: earnings are immediately
	// payable so the settle-exactly-once assertions read them directly.
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	suite := godog.TestSuite{
		ScenarioInitializer: xiInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/cross_instance_relay.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("multinode/cross_instance_relay scenarios failed (see godog output above)")
	}
}
