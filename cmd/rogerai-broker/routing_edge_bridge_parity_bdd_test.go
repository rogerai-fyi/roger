package main

// routing_edge_bridge_parity_bdd_test.go makes features/routing/edge_bridge_parity.feature
// EXECUTABLE against the REAL broker and the REAL sealed edge fabric (contract §6, §1a, §5,
// §7). It embeds rpState (routing_regression_pins_bdd_test.go): the upstream_failover
// harness for the direct fabric (a real relayBroker over the real store - Postgres when
// ROGERAI_TEST_DATABASE_URL is set - a valkey shared store, real stations behind scripted
// httptest upstreams) plus the tower subsystem. The Tower side here is this file's own
// stand-up (eb1Live), the same construction as rpState.standUpTower / liveSealedFabric - an
// in-process hub, a share node self-attached through agent.ServeTower, an approved Tower -
// with two additions the parity scenarios need: the Tower station's upstream is SCRIPTABLE
// and RECORDS every plaintext it was handed, and the hub's submit endpoint is COUNTED, so
// "the bridge tried t1 once" / "nothing was submitted to t1's hub" are read at the hub.
// Nothing is mocked; relay() is the production relay and the fan-out coin is the production
// coin.
//
// HOW THINGS ARE OBSERVED (and the limits of the fabric, stated rather than faked):
//
//   - THE COIN. relay() seeds the 50 % fan-out coin from the request id it mints itself; there
//     is no seam to force it. "N consumers relay ..." fires N real relays and the Then steps
//     read the whole batch (both sides of the coin occur). A single relay "with the coin saying
//     edge" is re-fired (at most eb1CoinTries times) until the hub SAW a submit during that
//     relay - the only observable sign that the coin said edge and the bridge took it. If no
//     relay ever reaches the hub the step fails: the bridge declines that request shape.
//   - "THE BRIDGE WAS NEVER ENTERED" is: no hub received a submit, no relay carried
//     X-RogerAI-Relay, and edgebridge.go wrote no "edge bridge:" line, since the batch began.
//     A bridge entry that declines silently before any of those is not distinguishable from
//     never calling it, and is the same thing to the consumer.
//   - TOWER ROW ATTRIBUTES. A routable row (fleet.Station) carries a price, a model and the
//     broker NODE id of the machine behind it - nothing else. Quant, ctx, capabilities, region
//     and the curated flag are therefore declared where the broker keeps them for any node: on
//     the registration of the node the row joins to (b.nodes[row.NodeID]); measured tps / TTFT
//     / trust on that node's metrics. "the node behind t1-a" is that node. A params_b
//     declaration has no field at all yet (protocol.ModelOffer; slice 2): the Given that
//     declares one sets it by reflection and FAILS, naming the missing field, until it exists.
//   - TWO LIVE TOWERS FOR ONE MODEL cannot be stood up (Core places a share node on one Tower
//     per model). Where a scenario names a second Tower it is an approved Tower on a routable
//     row whose data plane refuses connections (rpState.deadPricedTower's construction); the
//     bridge's ranking shows in the ORDER it tries them ("edge bridge: tower <id> failed"), so
//     "t2 serves more often" is read as "t2 is tried first more often".
//   - A Tower station id is Core-shaped ("st-<lowercase alnum>", attach.ValidStationID), so a
//     Tower can never take the relay name "s1". The spoof scenarios build the only collision
//     the system admits: a direct node whose id EQUALS a Tower station id / a Tower id.
//
// Behaviour that does not exist yet (real evaluation of quant / capabilities / self-hosted /
// order / only / sort / models[] / per-request cap on the bridge instead of the slice-0
// blanket decline, the receipt and usage chunk on bridged answers, the edge_coin_flips and
// edge_bridge_declined counters, the uniform anonymous refusal) is asserted honestly so each
// scenario FAILS for the right reason. Slice-2 filters (params_b, min_ctx, max_ttft_ms,
// trust_min, region) and slice-3 lookups are defined the same way and stay red until built.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

const (
	eb1PoolSize  = 40                   // "40 consumers relay": forty signed-in, funded accounts
	eb1CoinTries = 40                   // a fair coin never saying "edge" 40 times is 2^-40
	eb1Origin    = "https://rogerai.fm" // the allowlisted web origin a Playbox session presents
	eb1Stated    = "roger.min_tps 20"   // "a constraint the consumer stated" (a measured floor)
)

// eb1Tower is one Tower in the scenario: live (a hub + a share node that really serves) or
// dead (a routable row on a data plane that refuses connections).
type eb1Tower struct {
	rp        *rpTower
	stationID string
	model     string
	inMicros  int64
	outMicros int64
	endpoint  string
	live      bool
	priv      ed25519.PrivateKey // the Tower's own key (its relay identity)
	ownerPub  string             // the account behind the share node

	mu      sync.Mutex
	script  func(w http.ResponseWriter, body []byte)
	bodies  [][]byte
	submits atomic.Int64
}

func (t *eb1Tower) eb1Bodies() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.bodies))
	copy(out, t.bodies)
	return out
}

func (t *eb1Tower) eb1Script(f func(w http.ResponseWriter, body []byte)) {
	t.mu.Lock()
	t.script = f
	t.mu.Unlock()
}

// eb1Consumer is one signed-in, funded account. A batch rotates through the pool because an
// account may hold only maxOpenEdgeAttemptsPerAccount (32) edge attempts open at once, and in
// this in-process fabric no Tower process posts the settlement that closes one - so a single
// account would stop riding the edge after 32 relays for a reason no scenario is about.
type eb1Consumer struct {
	priv   ed25519.PrivateKey
	wallet string
}

type eb1State struct {
	*rpState
	tw   map[string]*eb1Tower
	pool []eb1Consumer

	// the request under test (rebuilt by every When)
	hdr      map[string]string
	provider map[string]any
	roger    map[string]any
	top      map[string]any
	stream   bool
	tools    bool
	image    bool
	caller   string // "", "owner", "grant", "anon", "session"
	wantEdge bool
	wantVia  string // "the bridge serves through <tower>"
	bands    map[string]string
	bodies   map[string][][]byte // direct stations: every body their upstream was handed
	bodiesMu sync.Mutex
	sent     []byte // the last body the consumer sent

	// snapshots taken when a batch begins
	subBefore  map[string]int64
	upBefore   map[string]int
	twBefore   map[string]int
	ledgerMark map[string]int64 // per wallet: the newest hold row id when the batch began
	ledgerLen  map[string]int
	coinBase   float64
	coinKnown  bool

	// two-Tower ranking baselines (tried-first share under balanced pref)
	baseFirst map[string]float64
	sessionCk string
	other     []byte // a comparison response body ("identical to those for ...")
	otherCost string
	otherCode int
}

// --- lifecycle ---------------------------------------------------------------------

func (s *eb1State) eb1Reset() error {
	if err := s.resetPins(); err != nil {
		return err
	}
	s.tw = map[string]*eb1Tower{}
	s.bands = map[string]string{}
	s.bodies = map[string][][]byte{}
	s.caller, s.sessionCk = "", ""
	s.pool = nil
	s.baseFirst = nil
	s.other, s.otherCost, s.otherCode = nil, "", 0
	s.model = "m"
	s.eb1ResetReq()
	return nil
}

func (s *eb1State) eb1Teardown() {
	_ = os.Unsetenv("ROGERAI_WEB_ORIGIN")
	if s.rpState != nil && s.foState != nil && s.b != nil {
		s.teardownPins()
	}
}

func (s *eb1State) eb1ResetReq() {
	s.hdr = map[string]string{}
	s.provider, s.roger, s.top = map[string]any{}, map[string]any{}, map[string]any{}
	s.stream, s.tools, s.image = false, false, false
	s.wantEdge, s.wantVia = false, ""
	s.model = "m"
	s.tokens = 50
}

// --- fixtures: direct stations --------------------------------------------------------

// eb1Station stands up a REAL direct station whose scripted upstream records every body it
// is handed (the plaintext a direct station sees) before answering.
func (s *eb1State) eb1Station(name, model string, in, out float64) *fstation {
	prev := s.model
	s.model = model
	st := s.station(name, in, out)
	s.model = prev
	s.eb1Answer(name, 200, "", "")
	return st
}

// eb1Answer scripts a direct station: 200 = the harness's real completion, anything else the
// default error body for that status (with an optional Retry-After). Bodies are recorded.
func (s *eb1State) eb1Answer(name string, code int, retryAfter, _ string) {
	st := s.st(name)
	st.set(func(_ int, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.bodiesMu.Lock()
		s.bodies[name] = append(s.bodies[name], body)
		s.bodiesMu.Unlock()
		if code == 200 {
			utRealCompletion(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(utDefaultBody(code)))
	})
}

func (s *eb1State) eb1StationBodies(name string) [][]byte {
	s.bodiesMu.Lock()
	defer s.bodiesMu.Unlock()
	out := make([][]byte, len(s.bodies[name]))
	copy(out, s.bodies[name])
	return out
}

// eb1RemoveDirect takes a direct station off the air entirely (no registration, no tunnel).
func (s *eb1State) eb1RemoveDirect(name string) {
	st, ok := s.stations[name]
	if !ok {
		return
	}
	s.b.mu.Lock()
	delete(s.b.nodes, st.id)
	delete(s.b.tunnels, st.id)
	delete(s.b.lastSeen, st.id)
	s.b.mu.Unlock()
}

// eb1Rekey gives a direct station a chosen node id (the id-collision scenarios).
func (s *eb1State) eb1Rekey(name, newID string) error {
	st := s.st(name)
	old := st.id
	s.b.mu.Lock()
	reg := s.b.nodes[old]
	delete(s.b.nodes, old)
	reg.NodeID = newID
	s.b.nodes[newID] = reg
	delete(s.b.lastSeen, old)
	s.b.lastSeen[newID] = time.Now()
	s.b.tunnels[newID] = s.b.tunnels[old]
	delete(s.b.tunnels, old)
	s.b.trust[newID] = s.b.trust[old]
	delete(s.b.trust, old)
	s.b.mu.Unlock()
	st.mu.Lock()
	st.id = newID
	st.mu.Unlock()
	return s.db.BindNode(newID, st.acct)
}

// --- fixtures: Towers ------------------------------------------------------------------

func eb1Micros(price float64) int64 { return int64(math.Round(price * 1e6)) }

// eb1Live stands up a LIVE sealed fabric for `model` under Tower `name`: rpState.standUpTower's
// construction with a scriptable, recording upstream and a counted hub submit.
func (s *eb1State) eb1Live(name, model string, in, out float64) (*eb1Tower, error) {
	if tw, ok := s.tw[name]; ok {
		return tw, nil
	}
	if err := s.ensureTower(); err != nil {
		return nil, err
	}
	t, b, srv := s.t, s.b, s.towerSrv
	tw := &eb1Tower{rp: &rpTower{name: name}, model: model, live: true, inMicros: eb1Micros(in), outMicros: eb1Micros(out)}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tw.mu.Lock()
		tw.bodies = append(tw.bodies, body)
		script := tw.script
		tw.mu.Unlock()
		if script != nil {
			script(w, body)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong from `+name+`"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
	tw.rp.closers = append(tw.rp.closers, upstream.Close)

	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tw.rp.closers = append(tw.rp.closers, func() { _ = hubLn.Close() })
	lt := liveEdgeTower(t, b, srv, name+"-op-"+s.nonce, hubLn.Addr().String())
	tw.rp.id, tw.priv = lt.id, lt.priv

	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, lt.id, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv, SubmitTTL: 10 * time.Second, PollTTL: 500 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, func(w http.ResponseWriter, r *http.Request) {
		tw.submits.Add(1) // the bridge handed this Tower's hub a sealed request
		hubServer.Submit(w, r)
	})
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()

	nodeOp := signedInOperator(t, b, name+"-node-"+s.nonce)
	tw.ownerPub = hexOf(nodeOp.priv.Public().(ed25519.PublicKey))
	shareNodeID := registerShareNode(t, b, nodeOp)
	ctx, cancel := context.WithCancel(context.Background())
	tw.rp.closers = append(tw.rp.closers, cancel)
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{
			NodeID: shareNodeID, Broker: srv.URL, Model: model, Modality: "chat",
			PriceIn: 0, PriceOut: 0, Upstream: upstream.URL, Parallel: 1,
		}, nodeOp.priv, t.TempDir(), io.Discard, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ats, aerr := b.tower.stations.ByTower(lt.id)
		if aerr == nil && len(ats) > 0 {
			hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
			tw.stationID = ats[0].StationID
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("tower %s: the share node never attached", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
	tw.endpoint = liveEndpointOf(t, b, lt.id)
	routableEdgePriced(t, b, lt.id, tw.stationID, model, tw.endpoint, tw.inMicros, tw.outMicros)
	tw.rp.nodeID = "n-" + tw.stationID
	s.b.mu.Lock()
	s.b.trust[tw.rp.nodeID] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	s.towers[name], s.tw[name] = tw.rp, tw
	return tw, nil
}

// eb1Dead is an approved Tower hosting the model on a priced routable row whose data plane
// nothing serves (connection refused): rpState.deadPricedTower's construction, recorded here
// so the row can be re-published and its station id is known.
func (s *eb1State) eb1Dead(name, model string, in, out, tps float64) (*eb1Tower, error) {
	if tw, ok := s.tw[name]; ok {
		return tw, nil
	}
	if err := s.ensureTower(); err != nil {
		return nil, err
	}
	op := signedInOperator(s.t, s.b, name+"-op-"+s.nonce)
	lt := enrolledTower(s.t, s.b, op.login)
	if err := s.b.tower.registry.Transition(lt.id, admit.StateActive); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(name + "|" + s.nonce))
	stationID := "st-" + hex.EncodeToString(sum[:8])
	attachStation(s.t, s.b, stationID, lt.id, op.login)
	tw := &eb1Tower{rp: &rpTower{name: name, id: lt.id}, stationID: stationID, model: model, priv: lt.priv,
		endpoint: "127.0.0.1:1", inMicros: eb1Micros(in), outMicros: eb1Micros(out)}
	routableEdgePriced(s.t, s.b, lt.id, stationID, model, tw.endpoint, tw.inMicros, tw.outMicros)
	tw.rp.nodeID = "n-" + stationID
	s.b.mu.Lock()
	s.b.trust[tw.rp.nodeID] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	if tps > 0 {
		s.setTPS(tw.rp.nodeID, tps)
	}
	s.towers[name], s.tw[name] = tw.rp, tw
	return tw, nil
}

// eb1Publish re-publishes a Tower's routable row (price / model changed) keeping the node
// registration the row joins to.
func (s *eb1State) eb1Publish(tw *eb1Tower) error {
	return s.b.tower.routable.Replace(tw.rp.id, []fleet.Station{{
		TowerID: tw.rp.id, StationID: tw.stationID, OfferID: "self-" + tw.stationID,
		Model: tw.model, Modality: "text", Expires: time.Now().Add(time.Hour),
		Endpoint: tw.endpoint, NodeID: tw.rp.nodeID, PriceIn: tw.inMicros, PriceOut: tw.outMicros,
	}})
}

// eb1T resolves a Tower by its name or by its station's name ("t1-a" is Tower t1's station).
func (s *eb1State) eb1T(name string) (*eb1Tower, error) {
	name = strings.TrimSuffix(name, "-a")
	tw, ok := s.tw[name]
	if !ok {
		return nil, fmt.Errorf("no Tower %q in this scenario", name)
	}
	return tw, nil
}

func (s *eb1State) eb1IsTower(name string) bool {
	_, ok := s.tw[strings.TrimSuffix(name, "-a")]
	return ok
}

// eb1NodeID is the broker node id behind a scenario name: a direct station's id, or the node
// the Tower row joins to.
func (s *eb1State) eb1NodeID(name string) (string, error) {
	if st, ok := s.stations[name]; ok {
		return st.id, nil
	}
	tw, err := s.eb1T(name)
	if err != nil {
		return "", err
	}
	return tw.rp.nodeID, nil
}

// eb1Reg edits the registration of the node behind `name`, making sure it carries an offer
// for the model so declared attributes have somewhere to live.
func (s *eb1State) eb1Reg(name string, f func(reg *protocol.NodeRegistration, o *protocol.ModelOffer)) error {
	id, err := s.eb1NodeID(name)
	if err != nil {
		return err
	}
	model := s.model
	if s.eb1IsTower(name) {
		tw, _ := s.eb1T(name)
		model = tw.model
	} else if st, ok := s.stations[name]; ok {
		model = st.model
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	reg := s.b.nodes[id]
	reg.NodeID = id
	idx := -1
	for i := range reg.Offers {
		if reg.Offers[i].Model == model {
			idx = i
		}
	}
	if idx < 0 {
		o := protocol.ModelOffer{Model: model, Ctx: 32768}
		if s.eb1IsTower(name) {
			tw, _ := s.eb1T(name)
			o.PriceIn, o.PriceOut = edgeRowPrice(tw.inMicros), edgeRowPrice(tw.outMicros)
		}
		reg.Offers = append(reg.Offers, o)
		idx = len(reg.Offers) - 1
	}
	f(&reg, &reg.Offers[idx])
	s.b.nodes[id] = reg
	return nil
}

// eb1SetOfferField sets a declared attribute by its JSON name through reflection, so an
// attribute the protocol has no field for yet (params_b) fails here, naming what is missing.
func eb1SetOfferField(o *protocol.ModelOffer, jsonName string, v float64) error {
	rv := reflect.ValueOf(o).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag != jsonName {
			continue
		}
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.Float32, reflect.Float64:
			f.SetFloat(v)
		case reflect.Int, reflect.Int32, reflect.Int64:
			f.SetInt(int64(v))
		case reflect.Ptr:
			p := reflect.New(f.Type().Elem())
			if p.Elem().Kind() == reflect.Float64 {
				p.Elem().SetFloat(v)
			} else {
				p.Elem().SetInt(int64(v))
			}
			f.Set(p)
		default:
			return fmt.Errorf("protocol.ModelOffer.%s has an unexpected kind %s", rt.Field(i).Name, f.Kind())
		}
		return nil
	}
	return fmt.Errorf("protocol.ModelOffer has no %q field: the attribute cannot be declared by a station or a Tower row yet", jsonName)
}

func (s *eb1State) eb1Trust(name string, f func(t *trustState)) error {
	id, err := s.eb1NodeID(name)
	if err != nil {
		return err
	}
	s.b.mu.Lock()
	tq := s.b.trust[id]
	f(&tq)
	s.b.trust[id] = tq
	s.b.mu.Unlock()
	return nil
}

func (s *eb1State) eb1TPS(name string, tps float64) error {
	id, err := s.eb1NodeID(name)
	if err != nil {
		return err
	}
	s.b.metricsMu.Lock()
	if tps <= 0 {
		delete(s.b.tps, id)
	} else {
		s.b.tps[id] = tps
	}
	s.b.metricsMu.Unlock()
	return nil
}

func (s *eb1State) eb1Price(name, axis string, price float64) error {
	if s.eb1IsTower(name) {
		tw, _ := s.eb1T(name)
		if axis == "in" {
			tw.inMicros = eb1Micros(price)
		} else {
			tw.outMicros = eb1Micros(price)
		}
		if err := s.eb1Publish(tw); err != nil {
			return err
		}
		return s.eb1Reg(name, func(_ *protocol.NodeRegistration, o *protocol.ModelOffer) {
			o.PriceIn, o.PriceOut = edgeRowPrice(tw.inMicros), edgeRowPrice(tw.outMicros)
		})
	}
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) {
		if axis == "in" {
			o.PriceIn = price
		} else {
			o.PriceOut = price
		}
	})
	return nil
}

// --- Background -----------------------------------------------------------------------

func (s *eb1State) eb1Background() error {
	if err := s.ensureTower(); err != nil {
		return err
	}
	// The signed-in consumer is the harness's GitHub-bound buyer (ensureTower funded it); top
	// it up so hundreds of relays and their holds never run it dry. It is consumer 0 of the
	// pool; the rest are the same kind of account (a GitHub-bound owner with a funded wallet:
	// the direct spend gate and the bridge's signed-in gate both accept it).
	if _, err := s.db.AddCredits(s.wallet, 5000); err != nil {
		return err
	}
	s.pool = []eb1Consumer{{priv: s.edgeConsumer, wallet: s.wallet}}
	for i := 1; i < eb1PoolSize; i++ {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			return err
		}
		gid, err := crand.Int(crand.Reader, big.NewInt(1<<62))
		if err != nil {
			return err
		}
		id := gid.Int64() + 10
		if err := s.db.BindOwner(store.Owner{GitHubID: id, Login: fmt.Sprintf("buyer%d-%s", i, s.nonce), Pubkey: hexOf(pub)}); err != nil {
			return err
		}
		wallet := "u_gh_" + strconv.FormatInt(id, 10)
		if _, err := s.db.AddCredits(wallet, 5000); err != nil {
			return err
		}
		s.pool = append(s.pool, eb1Consumer{priv: priv, wallet: wallet})
	}
	return nil
}

func (s *eb1State) eb1DirectStation(name, model string, out float64) error {
	s.eb1Station(name, model, 0.10, out)
	return nil
}

func (s *eb1State) eb1ApprovedTower(name, model, _ string, out float64) error {
	_, err := s.eb1Live(name, model, 0.10, out)
	return err
}

// eb1CoinControl: the coin is the production coin (see the file header). What "under test
// control" means here is recorded once: the edge_coin_flips reading before the scenario.
func (s *eb1State) eb1CoinControl() error {
	s.coinBase, s.coinKnown = s.eb1Counter("edge_coin_flips", "")
	return nil
}

// --- the request ------------------------------------------------------------------------

var (
	eb1ReCoinAlways = regexp.MustCompile(`the coin always says "edge"`)
	eb1ReCoin       = regexp.MustCompile(`the coin (?:says|saying) "edge"`)
	eb1ReVia        = regexp.MustCompile(`the bridge serves through "([^"]+)"`)
	eb1ReThat       = regexp.MustCompile(`that constraint`)
	eb1ReFreq       = regexp.MustCompile(`(roger\.freq|X-Roger-Freq) for band "([^"]+)"`)
	eb1ReFor        = regexp.MustCompile(`(?:^|\s)for "([^"]+)"`)
	eb1ReModels     = regexp.MustCompile(`\bmodels (\[[^\]]*\])`)
	eb1ReModel      = regexp.MustCompile(`\bmodel "([^"]+)"`)
	eb1RePrompt     = regexp.MustCompile(`\ban? (\d+)-token prompt`)
	eb1ReMaxTok     = regexp.MustCompile(`\bmax_tokens (\d+)`)
	eb1ReMaxPrice   = regexp.MustCompile(`provider\.max_price\.(prompt|completion|request) ([0-9.]+)`)
	eb1ReNoMax      = regexp.MustCompile(`no max_price \(the \$10 default\)`)
	eb1RePin        = regexp.MustCompile(`X-Roger-Node "([^"]+)"`)
	eb1ReNodeList   = regexp.MustCompile(`provider\.(order|only|ignore) (\[[^\]]*\])`)
	eb1ReQuants     = regexp.MustCompile(`provider\.quantizations (\[[^\]]*\])`)
	eb1ReExclude    = regexp.MustCompile(`X-Roger-Exclude-Nodes "([^"]+)"`)
	eb1ReFallbacks  = regexp.MustCompile(`provider\.allow_fallbacks (true|false)`)
	eb1ReSort       = regexp.MustCompile(`provider\.sort "([^"]+)"`)
	eb1RePref       = regexp.MustCompile(`roger\.pref "([^"]+)"`)
	eb1ReRequire    = regexp.MustCompile(`roger\.require (\[[^\]]*\])`)
	eb1ReNoRequire  = regexp.MustCompile(`no roger\.require`)
	eb1ReReqParams  = regexp.MustCompile(`provider\.require_parameters (true|false)`)
	eb1ReToolChoice = regexp.MustCompile(`tool_choice "([^"]+)"`)
	eb1ReToolsArr   = regexp.MustCompile(`a non-empty tools array|\btools\b`)
	eb1ReImage      = regexp.MustCompile(`an image_url content part`)
	eb1ReRogerList  = regexp.MustCompile(`roger\.(params_b|region) (\[[^\]]*\])`)
	eb1ReRogerNum   = regexp.MustCompile(`roger\.(min_ctx|min_tps|max_ttft_ms) ([0-9.]+)`)
	eb1ReMinTPSHdr  = regexp.MustCompile(`X-Roger-Min-TPS ([0-9.]+)`)
	eb1ReTrust      = regexp.MustCompile(`roger\.trust_min "([^"]+)"`)
	eb1ReRogerBool  = regexp.MustCompile(`roger\.(confidential|self_hosted_only) (true|false)`)
	eb1ReConfHdr    = regexp.MustCompile(`X-Roger-Confidential 1`)
	eb1ReStream     = regexp.MustCompile(`"stream": true`)
	eb1ReTooSlow    = regexp.MustCompile(`the Tower row is too slow`)
	eb1ReFiller     = regexp.MustCompile(`both fabrics price identically|no routing object`)
	eb1ReLeftover   = regexp.MustCompile(`^(?:\s|,|\band\b|\bwith\b|\ba\b)*$`)
)

// eb1Take applies re to *expr: every match is handed to f and blanked out of the expression,
// so later (looser) patterns never see it.
func eb1Take(expr *string, re *regexp.Regexp, f func(m []string) error) error {
	var ferr error
	*expr = re.ReplaceAllStringFunc(*expr, func(match string) string {
		if ferr == nil {
			ferr = f(re.FindStringSubmatch(match))
		}
		return " "
	})
	return ferr
}

func eb1List(raw string) ([]any, error) {
	var out []any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("cannot read the list %s: %w", raw, err)
	}
	return out, nil
}

// eb1Apply reads the request shaping out of a When's wording into the body and headers of
// the relay. Every clause must be understood: anything left over is an error, never ignored.
func (s *eb1State) eb1Apply(expr string) error {
	orig := expr
	set := func(re *regexp.Regexp, f func(m []string) error) error { return eb1Take(&expr, re, f) }
	steps := []struct {
		re *regexp.Regexp
		f  func(m []string) error
	}{
		{eb1ReCoinAlways, func([]string) error { return nil }}, // a batch already covers both sides
		{eb1ReCoin, func([]string) error { s.wantEdge = true; return nil }},
		{eb1ReVia, func(m []string) error { s.wantVia = m[1]; return nil }},
		{eb1ReThat, func([]string) error { return s.eb1Apply(eb1Stated) }},
		{eb1ReFreq, func(m []string) error {
			code, ok := s.bands[m[2]]
			if !ok {
				return fmt.Errorf("no private band %q in this scenario", m[2])
			}
			if m[1] == "roger.freq" {
				s.roger["freq"] = code
			} else {
				s.hdr["X-Roger-Freq"] = code
			}
			return nil
		}},
		{eb1ReFor, func(m []string) error { s.model = m[1]; return nil }},
		{eb1ReModels, func(m []string) error {
			l, err := eb1List(m[1])
			s.top["models"] = l
			return err
		}},
		{eb1ReModel, func(m []string) error { s.model = m[1]; return nil }},
		{eb1RePrompt, func(m []string) error { s.tokens, _ = strconv.Atoi(m[1]); return nil }},
		{eb1ReMaxTok, func(m []string) error { n, _ := strconv.Atoi(m[1]); s.top["max_tokens"] = n; return nil }},
		{eb1ReMaxPrice, func(m []string) error {
			v, _ := strconv.ParseFloat(m[2], 64)
			mp, _ := s.provider["max_price"].(map[string]any)
			if mp == nil {
				mp = map[string]any{}
			}
			mp[m[1]] = v
			s.provider["max_price"] = mp
			return nil
		}},
		{eb1ReNoMax, func([]string) error { return nil }},
		{eb1RePin, func(m []string) error { s.hdr["X-Roger-Node"] = s.idOf(m[1]); return nil }},
		{eb1ReNodeList, func(m []string) error {
			l, err := eb1List(m[2])
			for i := range l {
				if n, ok := l[i].(string); ok {
					l[i] = s.idOf(n)
				}
			}
			s.provider[m[1]] = l
			return err
		}},
		{eb1ReQuants, func(m []string) error {
			l, err := eb1List(m[1])
			s.provider["quantizations"] = l
			return err
		}},
		{eb1ReExclude, func(m []string) error {
			var ids []string
			for _, n := range strings.Split(m[1], ",") {
				ids = append(ids, s.idOf(strings.TrimSpace(n)))
			}
			s.hdr["X-Roger-Exclude-Nodes"] = strings.Join(ids, ",")
			return nil
		}},
		{eb1ReFallbacks, func(m []string) error { s.provider["allow_fallbacks"] = m[1] == "true"; return nil }},
		{eb1ReSort, func(m []string) error { s.provider["sort"] = m[1]; return nil }},
		{eb1RePref, func(m []string) error { s.roger["pref"] = m[1]; return nil }},
		{eb1ReRequire, func(m []string) error {
			l, err := eb1List(m[1])
			s.roger["require"] = l
			return err
		}},
		{eb1ReNoRequire, func([]string) error { return nil }},
		{eb1ReReqParams, func(m []string) error { s.provider["require_parameters"] = m[1] == "true"; return nil }},
		{eb1ReToolChoice, func(m []string) error { s.top["tool_choice"] = m[1]; return nil }},
		{eb1ReToolsArr, func([]string) error { s.tools = true; return nil }},
		{eb1ReImage, func([]string) error { s.image = true; return nil }},
		{eb1ReRogerList, func(m []string) error {
			l, err := eb1List(m[2])
			s.roger[m[1]] = l
			return err
		}},
		{eb1ReRogerNum, func(m []string) error {
			v, _ := strconv.ParseFloat(m[2], 64)
			s.roger[m[1]] = v
			return nil
		}},
		{eb1ReMinTPSHdr, func(m []string) error { s.hdr["X-Roger-Min-TPS"] = m[1]; return nil }},
		{eb1ReTrust, func(m []string) error { s.roger["trust_min"] = m[1]; return nil }},
		{eb1ReRogerBool, func(m []string) error { s.roger[m[1]] = m[2] == "true"; return nil }},
		{eb1ReConfHdr, func([]string) error { s.hdr["X-Roger-Confidential"] = "1"; return nil }},
		{eb1ReStream, func([]string) error { s.stream = true; return nil }},
		{eb1ReTooSlow, func([]string) error {
			if err := s.eb1TPS("t1-a", 10); err != nil {
				return err
			}
			return s.eb1TPS("s1", 40)
		}},
		{eb1ReFiller, func([]string) error { return nil }},
	}
	for _, st := range steps {
		if err := set(st.re, st.f); err != nil {
			return err
		}
	}
	if !eb1ReLeftover.MatchString(expr) {
		return fmt.Errorf("the request wording %q has a clause this runner does not understand: %q", orig, strings.TrimSpace(expr))
	}
	return nil
}

func (s *eb1State) eb1Body() []byte {
	var content any = utPrompt(s.tokens)
	if s.image {
		content = []map[string]any{
			{"type": "text", "text": "what is in this picture?"},
			{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}
	}
	m := map[string]any{"model": s.model, "messages": []map[string]any{{"role": "user", "content": content}}}
	if s.tools {
		m["tools"] = []map[string]any{{"type": "function", "function": map[string]any{
			"name": "get_weather", "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}}}
	}
	if len(s.provider) > 0 {
		m["provider"] = s.provider
	}
	if len(s.roger) > 0 {
		m["roger"] = s.roger
	}
	for k, v := range s.top {
		m[k] = v
	}
	if s.stream {
		m["stream"] = true
	}
	b, _ := json.Marshal(m)
	return b
}

// eb1CallerWallet is the wallet the current caller's holds land on.
func (s *eb1State) eb1CallerWallet() string {
	switch s.caller {
	case "anon":
		return protocol.UserIDFromPubkey(hexOf(s.anonPriv.Public().(ed25519.PublicKey)))
	case "owner":
		pub := hexOf(s.callerPriv.Public().(ed25519.PublicKey))
		if o, ok, err := s.b.db.OwnerByPubkey(pub); err == nil && ok {
			if w, ok := accountWalletForOwner(o); ok {
				return w
			}
		}
		return protocol.UserIDFromPubkey(pub)
	}
	return s.wallet
}

func (s *eb1State) eb1Holds(wallet string) ([]store.LedgerRow, error) {
	return s.db.LedgerOf(wallet, []string{store.KindHold}, 100000)
}

// eb1HoldWallets are the wallets a hold of the current caller(s) could land on.
func (s *eb1State) eb1HoldWallets() []string {
	if s.caller == "anon" || s.caller == "owner" {
		return []string{s.eb1CallerWallet()}
	}
	out := make([]string, 0, len(s.pool))
	for _, c := range s.pool {
		out = append(out, c.wallet)
	}
	if len(out) == 0 {
		out = append(out, s.wallet)
	}
	return out
}

// eb1Snapshot marks "the batch begins": the log offset, every upstream's count, every hub's
// submit count, the caller's hold rows; and it heartbeats every node as a live one does.
func (s *eb1State) eb1Snapshot() error {
	s.logMark = len(s.logs.String())
	s.heartbeat()
	s.b.mu.Lock()
	for _, tw := range s.tw {
		s.b.lastSeen[tw.rp.nodeID] = time.Now()
	}
	s.b.mu.Unlock()
	s.subBefore, s.upBefore, s.twBefore = map[string]int64{}, map[string]int{}, map[string]int{}
	for n, tw := range s.tw {
		s.subBefore[n] = tw.submits.Load()
		s.twBefore[n] = len(tw.eb1Bodies())
	}
	for n, st := range s.stations {
		s.upBefore[n] = st.upstreamCount()
	}
	s.ledgerLen, s.ledgerMark = map[string]int{}, map[string]int64{}
	for _, w := range s.eb1HoldWallets() {
		rows, err := s.eb1Holds(w)
		if err != nil {
			return err
		}
		s.ledgerLen[w] = len(rows)
		for _, r := range rows {
			if r.ID > s.ledgerMark[w] {
				s.ledgerMark[w] = r.ID
			}
		}
	}
	return nil
}

// eb1NewHolds are the hold amounts placed on the caller's wallet since the snapshot.
func (s *eb1State) eb1NewHolds() ([]float64, error) {
	var out []float64
	for _, w := range s.eb1HoldWallets() {
		rows, err := s.eb1Holds(w)
		if err != nil {
			return nil, err
		}
		fresh := len(rows) - s.ledgerLen[w]
		for i, r := range rows { // newest first
			if r.ID > s.ledgerMark[w] || (r.ID == 0 && i < fresh) {
				out = append(out, -r.Amount)
			}
		}
	}
	return out, nil
}

func (s *eb1State) eb1Fire() { s.eb1FireAs(0) }

// eb1FireAs fires one relay; an ordinary consumer is pool member i (see eb1Consumer).
func (s *eb1State) eb1FireAs(i int) {
	body := s.eb1Body()
	s.sent = body
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	switch s.caller {
	case "grant":
		r.Header.Set("Authorization", "Bearer "+s.grantToken)
	case "session":
		r.Header.Set("Origin", eb1Origin)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.sessionCk})
	case "anon":
		signReq(r, s.anonPriv, body)
	case "owner":
		signReq(r, s.callerPriv, body)
	default:
		priv := s.edgeConsumer
		if len(s.pool) > 0 {
			priv = s.pool[i%len(s.pool)].priv
		}
		signReq(r, priv, body)
	}
	for k, v := range s.hdr {
		r.Header.Set(k, v)
	}
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(w, r)
	s.batch = append(s.batch, rpResult{code: w.Code, hdr: w.Header(), body: w.Body.Bytes()})
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = w.Code, w.Body.Bytes(), w.Header(), w
}

func (s *eb1State) eb1Batch(n int) error {
	s.batch = nil
	if err := s.eb1Snapshot(); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		s.eb1FireAs(i)
	}
	return nil
}

func (s *eb1State) eb1Submits(name string) int64 {
	tw := s.tw[name]
	return tw.submits.Load() - s.subBefore[name]
}

func (s *eb1State) eb1AnySubmit() bool {
	for n := range s.tw {
		if s.eb1Submits(n) > 0 {
			return true
		}
	}
	return false
}

// eb1Single fires ONE relay. "the coin says edge" / "the bridge serves through t1" re-fire it
// until that actually happened (see the file header); the kept relay is the last one, and the
// snapshots are of the moment just before it.
func (s *eb1State) eb1Single() error {
	tries := 1
	if s.wantEdge || s.wantVia != "" {
		tries = eb1CoinTries
	}
	for i := 0; i < tries; i++ {
		if err := s.eb1Batch(1); err != nil {
			return err
		}
		if s.wantVia != "" {
			tw, err := s.eb1T(s.wantVia)
			if err != nil {
				return err
			}
			if s.lastCode == 200 && s.lastHdr.Get("X-RogerAI-Relay") == tw.rp.id {
				return nil
			}
			continue
		}
		if !s.wantEdge || s.eb1AnySubmit() {
			return nil
		}
	}
	if s.wantVia != "" {
		return fmt.Errorf("no relay in %d was served through %q (last: %d %.200s)", tries, s.wantVia, s.lastCode, s.lastBody)
	}
	return fmt.Errorf("the coin never put this request on the bridge in %d relays (2^-%d by chance): the bridge declines this request shape (last: %d %.200s)",
		tries, tries, s.lastCode, s.lastBody)
}

func (s *eb1State) eb1WhenMany(n int, tail string) error {
	s.eb1ResetReq()
	if err := s.eb1Apply(tail); err != nil {
		return err
	}
	return s.eb1Batch(n)
}

func (s *eb1State) eb1WhenOne(tail string) error {
	s.eb1ResetReq()
	if err := s.eb1Apply(tail); err != nil {
		return err
	}
	return s.eb1Single()
}

func (s *eb1State) eb1WhenOneBare() error { return s.eb1WhenOne("") }

func (s *eb1State) eb1WhenOwner(tail string) error {
	s.caller = "owner"
	return s.eb1WhenOne(tail)
}

func (s *eb1State) eb1WhenGrant(tail string) error {
	s.caller = "grant"
	return s.eb1WhenOne(tail)
}

func (s *eb1State) eb1WhenAnon(tail string) error {
	s.caller = "anon"
	return s.eb1WhenOne(tail)
}

// eb1WhenProxyRetries: the retry is one request, but one request is served direct half the
// time by the coin alone; rpBatch retries make "served by s1" mean "the Tower was excluded".
func (s *eb1State) eb1WhenProxyRetries(tail string) error {
	return s.eb1WhenMany(rpBatch, tail)
}

// --- reading a batch --------------------------------------------------------------------

type eb1Tally struct {
	total, ok200 int
	codes        map[int]int
	direct       map[string]int // by station name
	relay        map[string]int // by Tower name
	other        map[string]int // a 200 naming neither
	sample       string
}

func (s *eb1State) eb1Tally() eb1Tally {
	t := eb1Tally{codes: map[int]int{}, direct: map[string]int{}, relay: map[string]int{}, other: map[string]int{}}
	stByID, twByID := map[string]string{}, map[string]string{}
	for n, st := range s.stations {
		stByID[st.id] = n
	}
	for n, tw := range s.tw {
		twByID[tw.rp.id] = n
	}
	for _, r := range s.batch {
		t.total++
		t.codes[r.code]++
		if r.code != 200 {
			if t.sample == "" {
				t.sample = fmt.Sprintf("%d %.220s", r.code, r.body)
			}
			continue
		}
		t.ok200++
		if rel := r.hdr.Get("X-RogerAI-Relay"); rel != "" {
			if n, ok := twByID[rel]; ok {
				t.relay[n]++
			} else {
				t.other["relay="+rel]++
			}
			continue
		}
		if n, ok := stByID[r.hdr.Get("X-RogerAI-Provider")]; ok {
			t.direct[n]++
		} else {
			t.other["provider="+r.hdr.Get("X-RogerAI-Provider")]++
		}
	}
	return t
}

func (t eb1Tally) String() string {
	return fmt.Sprintf("%d relays: codes=%v direct=%v through-tower=%v other=%v first non-200: %s", t.total, t.codes, t.direct, t.relay, t.other, t.sample)
}

func (s *eb1State) eb1EveryDirect(name string) error {
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	if _, ok := s.stations[name]; !ok {
		return fmt.Errorf("no direct station %q in this scenario", name)
	}
	t := s.eb1Tally()
	if t.direct[name] != t.total {
		return fmt.Errorf("%q served %d of %d relays, want all of them (%s)", name, t.direct[name], t.total, t)
	}
	return nil
}

func (s *eb1State) eb1EveryThrough(name string) error {
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	if _, err := s.eb1T(name); err != nil {
		return err
	}
	t := s.eb1Tally()
	if t.relay[name] != t.total {
		return fmt.Errorf("%d of %d relays were served through Tower %q, want all of them (%s)", t.relay[name], t.total, name, t)
	}
	return nil
}

func (s *eb1State) eb1SomeBoth(tower, station string) error {
	t := s.eb1Tally()
	if t.ok200 != t.total || t.relay[tower] == 0 || t.direct[station] == 0 || t.relay[tower]+t.direct[station] != t.total {
		return fmt.Errorf("want every relay served, some through %q and some by %q (%s)", tower, station, t)
	}
	return nil
}

func (s *eb1State) eb1SomeBothRev(station, tower string) error { return s.eb1SomeBoth(tower, station) }

func (s *eb1State) eb1SomeRideCap(tower string) error {
	t := s.eb1Tally()
	if t.ok200 != t.total || t.relay[tower] == 0 {
		return fmt.Errorf("want every relay served and some through %q (the cap admits its price) (%s)", tower, t)
	}
	return nil
}

func (s *eb1State) eb1EveryNoMatch() error {
	if len(s.batch) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	for i, r := range s.batch {
		if err := eb1IsCode(r, 503, "no_match"); err != nil {
			return fmt.Errorf("relay %d of %d: %w (%s)", i+1, len(s.batch), err, s.eb1Tally())
		}
	}
	return nil
}

func eb1ErrCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func eb1IsCode(r rpResult, status int, code string) error {
	if r.code != status || eb1ErrCode(r.body) != code {
		return fmt.Errorf("response = %d code=%q, want %d %q: %.220s", r.code, eb1ErrCode(r.body), status, code, r.body)
	}
	return nil
}

func (s *eb1State) eb1Last() (rpResult, error) {
	if len(s.batch) == 0 {
		return rpResult{}, fmt.Errorf("no relay was fired")
	}
	return s.batch[len(s.batch)-1], nil
}

func (s *eb1State) eb1RespNoMatch() error {
	r, err := s.eb1Last()
	if err != nil {
		return err
	}
	return eb1IsCode(r, 503, "no_match")
}

func (s *eb1State) eb1RespBandCooling(secs string) error {
	r, err := s.eb1Last()
	if err != nil {
		return err
	}
	if err := eb1IsCode(r, 503, "band_cooling"); err != nil {
		return err
	}
	if got := r.hdr.Get("Retry-After"); got != secs {
		return fmt.Errorf("Retry-After = %q, want %s", got, secs)
	}
	return nil
}

func (s *eb1State) eb1Resp429RA(secs string) error {
	r, err := s.eb1Last()
	if err != nil {
		return err
	}
	if r.code != 429 {
		return fmt.Errorf("response = %d, want 429: %.220s", r.code, r.body)
	}
	if got := r.hdr.Get("Retry-After"); got == "" || (secs != "" && got != secs) {
		return fmt.Errorf("Retry-After = %q, want %q", got, secs)
	}
	return nil
}

func (s *eb1State) eb1Resp429AnyRA() error { return s.eb1Resp429RA("") }

func (s *eb1State) eb1RespCtx400(window int) error {
	r, err := s.eb1Last()
	if err != nil {
		return err
	}
	if r.code != 400 || !bytes.Contains(r.body, []byte("exceeds the context window")) || !bytes.Contains(r.body, []byte(strconv.Itoa(window))) {
		return fmt.Errorf("response = %d, want 400 \"exceeds the context window\" naming %d: %.240s", r.code, window, r.body)
	}
	return nil
}

func (s *eb1State) eb1Sees200() error {
	r, err := s.eb1Last()
	if err != nil {
		return err
	}
	if r.code != 200 {
		return fmt.Errorf("the consumer saw %d, want 200: %.220s", r.code, r.body)
	}
	return nil
}

func (s *eb1State) eb1EdgeLogLines() []string {
	all := s.logs.String()
	if s.logMark > len(all) {
		s.logMark = 0
	}
	var out []string
	for _, l := range strings.Split(all[s.logMark:], "\n") {
		if strings.Contains(l, "edge bridge:") {
			out = append(out, l)
		}
	}
	return out
}

func (s *eb1State) eb1NeverEntered() error {
	for n := range s.tw {
		if d := s.eb1Submits(n); d != 0 {
			return fmt.Errorf("the bridge handed Tower %q's hub %d sealed request(s); it should never have been entered (%s)", n, d, s.eb1Tally())
		}
	}
	for i, r := range s.batch {
		if rel := r.hdr.Get("X-RogerAI-Relay"); rel != "" {
			return fmt.Errorf("relay %d of %d rode a Tower (X-RogerAI-Relay=%s); the bridge should never have been entered", i+1, len(s.batch), rel)
		}
	}
	if lines := s.eb1EdgeLogLines(); len(lines) > 0 {
		return fmt.Errorf("the bridge ran for this batch (%d \"edge bridge:\" line(s), first: %s)", len(lines), lines[0])
	}
	return nil
}

func (s *eb1State) eb1ReceivedNothing(name string) error {
	st, ok := s.stations[name]
	if !ok {
		return fmt.Errorf("no direct station %q in this scenario", name)
	}
	if d := st.upstreamCount() - s.upBefore[name]; d != 0 {
		return fmt.Errorf("%q received %d request(s), want none (%s)", name, d, s.eb1Tally())
	}
	return nil
}

func (s *eb1State) eb1HubNothing(name string) error {
	if _, err := s.eb1T(name); err != nil {
		return err
	}
	name = strings.TrimSuffix(name, "-a")
	if d := s.eb1Submits(name); d != 0 {
		return fmt.Errorf("the bridge submitted %d sealed request(s) to %q's hub, want none", d, name)
	}
	return nil
}

func (s *eb1State) eb1NoHold() error {
	holds, err := s.eb1NewHolds()
	if err != nil {
		return err
	}
	if len(holds) != 0 {
		return fmt.Errorf("%d hold(s) were placed (%v), want none", len(holds), holds)
	}
	return nil
}

func (s *eb1State) eb1NothingAnywhereNoHold() error {
	for n := range s.stations {
		if err := s.eb1ReceivedNothing(n); err != nil {
			return err
		}
	}
	for n, tw := range s.tw {
		if d := len(tw.eb1Bodies()) - s.twBefore[n]; d != 0 {
			return fmt.Errorf("Tower %q's station received %d request(s), want none", n, d)
		}
	}
	return s.eb1NoHold()
}

func (s *eb1State) eb1NoHoldNoStrike() error {
	if err := s.eb1NoHold(); err != nil {
		return err
	}
	accts := map[string]string{}
	for n, st := range s.stations {
		accts[n] = st.acct
	}
	for n, tw := range s.tw {
		if tw.ownerPub != "" {
			accts[n] = tw.ownerPub
		}
	}
	for n, acct := range accts {
		strikes, err := s.db.StrikesByOwner(acct, 0)
		if err != nil {
			return err
		}
		if len(strikes) != 0 {
			return fmt.Errorf("the operator behind %q was struck %d time(s), want none", n, len(strikes))
		}
	}
	return nil
}

// --- Given: the generic "constraint the consumer stated" ----------------------------------

func (s *eb1State) eb1Fails(fail, pass string) error {
	if err := s.eb1TPS(fail, 10); err != nil {
		return err
	}
	if pass != "" {
		return s.eb1TPS(pass, 40)
	}
	return nil
}

func (s *eb1State) eb1FailsOnly(fail string) error { return s.eb1Fails(fail, "") }

func (s *eb1State) eb1BothFail(a, b string) error {
	if err := s.eb1TPS(a, 10); err != nil {
		return err
	}
	return s.eb1TPS(b, 10)
}

func (s *eb1State) eb1NoDirect(model string) error {
	for n, st := range s.stations {
		if st.model == model {
			s.eb1RemoveDirect(n)
		}
	}
	return nil
}

func (s *eb1State) eb1NoDirectTowerFails(model, tower string) error {
	if err := s.eb1NoDirect(model); err != nil {
		return err
	}
	return s.eb1FailsOnly(tower)
}

// eb1TwoTowers429Fail: t1 is the live Tower and answers 429; t2 is a second approved Tower on
// a dead data plane whose node is too slow for the stated constraint - it must never be tried.
func (s *eb1State) eb1TwoTowers429Fail(t1, t2, model string) error {
	tw, err := s.eb1T(t1)
	if err != nil {
		return err
	}
	tw.eb1Script(eb1TowerStatus(429, "9"))
	if err := s.eb1TPS(t1, 40); err != nil {
		return err
	}
	_, err = s.eb1Dead(t2, model, 0.10, 1.00, 10)
	return err
}

func eb1TowerStatus(code int, retryAfter string) func(w http.ResponseWriter, _ []byte) {
	return func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(utDefaultBody(code)))
	}
}

func (s *eb1State) eb1Answers429(name, retryAfter string) error {
	if s.eb1IsTower(name) {
		tw, _ := s.eb1T(name)
		tw.eb1Script(eb1TowerStatus(429, retryAfter))
		return nil
	}
	s.eb1Answer(name, 429, retryAfter, "")
	return nil
}

func (s *eb1State) eb1Answers429Plain(name string) error { return s.eb1Answers429(name, "") }

// --- Given: prices ------------------------------------------------------------------------

func (s *eb1State) eb1Prices(tower, tAxis string, tPrice float64, direct, dAxis string, dPrice float64) error {
	if err := s.eb1Price(tower, tAxis, tPrice); err != nil {
		return err
	}
	return s.eb1Price(direct, dAxis, dPrice)
}

func (s *eb1State) eb1PricesBothIn(tower string, tOut float64, direct string, dOut, in float64) error {
	for _, n := range []string{tower, direct} {
		if err := s.eb1Price(n, "in", in); err != nil {
			return err
		}
	}
	if err := s.eb1Price(tower, "out", tOut); err != nil {
		return err
	}
	return s.eb1Price(direct, "out", dOut)
}

func (s *eb1State) eb1PricedAt(tower string, tOut float64, direct string, dOut float64, _ string) error {
	if err := s.eb1Price(tower, "out", tOut); err != nil {
		return err
	}
	return s.eb1Price(direct, "out", dOut)
}

func (s *eb1State) eb1SamePriceBetterScore(tower, direct, _ string) error {
	if err := s.eb1Price(tower, "out", 1.00); err != nil {
		return err
	}
	if err := s.eb1Price(direct, "out", 1.00); err != nil {
		return err
	}
	// The direct station is the better-scored candidate: faster and measurably reliable.
	if err := s.eb1TPS(direct, 200); err != nil {
		return err
	}
	s.setSuccess(s.st(direct).id, 0.95)
	return s.eb1TPS(tower, 20)
}

// --- Given: measurements --------------------------------------------------------------------

func (s *eb1State) eb1MeasuresTPS(tower string, tTPS int, direct string, dTPS int) error {
	if err := s.eb1TPS(tower, float64(tTPS)); err != nil {
		return err
	}
	return s.eb1TPS(direct, float64(dTPS))
}

func (s *eb1State) eb1NoTPS(tower, direct string, dTPS int) error {
	if err := s.eb1TPS(tower, 0); err != nil {
		return err
	}
	return s.eb1TPS(direct, float64(dTPS))
}

func (s *eb1State) eb1MeasuresTTFT(tower string, tMs int, direct string, dMs int) error {
	if err := s.eb1Trust(tower, func(t *trustState) { t.ttftMs = float64(tMs) }); err != nil {
		return err
	}
	return s.eb1Trust(direct, func(t *trustState) { t.ttftMs = float64(dMs) })
}

func (s *eb1State) eb1NoTTFT(tower, direct string, dMs int) error {
	if err := s.eb1Trust(tower, func(t *trustState) { t.ttftMs = 0 }); err != nil {
		return err
	}
	return s.eb1Trust(direct, func(t *trustState) { t.ttftMs = float64(dMs) })
}

// --- Given: capabilities --------------------------------------------------------------------

func (s *eb1State) eb1Canary(name string, passed bool) error {
	id, err := s.eb1NodeID(name)
	if err != nil {
		return err
	}
	model := s.model
	if s.eb1IsTower(name) {
		tw, _ := s.eb1T(name)
		model = tw.model
	}
	if passed {
		s.b.recordToolProbe(id, model, true, false, true) // the SOLE writer of a verified "tools"
	}
	return nil
}

func (s *eb1State) eb1TowerNoCanaryDirectDid(tower, direct string) error {
	if err := s.eb1Canary(tower, false); err != nil {
		return err
	}
	return s.eb1Canary(direct, true)
}

func (s *eb1State) eb1TowerCanaryDirectNot(tower, direct string) error {
	if err := s.eb1Canary(tower, true); err != nil {
		return err
	}
	return s.eb1Canary(direct, false)
}

func (s *eb1State) eb1TowerDeclaresToolsUnverified(tower, direct string) error {
	if err := s.eb1Reg(tower, func(_ *protocol.NodeRegistration, o *protocol.ModelOffer) {
		o.Capabilities = append(o.Capabilities, protocol.CapTools) // self-declared, never canaried
	}); err != nil {
		return err
	}
	return s.eb1Canary(direct, true)
}

func (s *eb1State) eb1Vision(name string, has bool) error {
	return s.eb1Reg(name, func(_ *protocol.NodeRegistration, o *protocol.ModelOffer) {
		o.Capabilities = nil
		if has {
			o.Capabilities = []string{protocol.CapVision}
		}
	})
}

func (s *eb1State) eb1VisionOnlyDirect(tower, direct string) error {
	if err := s.eb1Vision(tower, false); err != nil {
		return err
	}
	return s.eb1Vision(direct, true)
}

func (s *eb1State) eb1VisionOnlyTower(tower, direct string) error {
	if err := s.eb1Vision(tower, true); err != nil {
		return err
	}
	return s.eb1Vision(direct, false)
}

func (s *eb1State) eb1VisionNeither(tower, direct, _ string) error {
	if err := s.eb1Vision(tower, false); err != nil {
		return err
	}
	return s.eb1Vision(direct, false)
}

// --- Given: declared attributes ----------------------------------------------------------------

func (s *eb1State) eb1Quant(name, quant string) error {
	return s.eb1Reg(name, func(_ *protocol.NodeRegistration, o *protocol.ModelOffer) { o.Quant = quant })
}

func (s *eb1State) eb1Quants(tower, tq, direct, dq string) error {
	if err := s.eb1Quant(tower, tq); err != nil {
		return err
	}
	return s.eb1Quant(direct, dq)
}

func (s *eb1State) eb1NoQuantTower(tower, direct, dq string) error {
	return s.eb1Quants(tower, "", direct, dq)
}

func (s *eb1State) eb1NoQuantEither(tower, direct string) error {
	return s.eb1Quants(tower, "", direct, "")
}

func (s *eb1State) eb1QuantNoDirect(tower, quant, model string) error {
	if err := s.eb1Quant(tower, quant); err != nil {
		return err
	}
	return s.eb1NoDirect(model)
}

func (s *eb1State) eb1Param(name string, params float64) error {
	var ferr error
	if err := s.eb1Reg(name, func(_ *protocol.NodeRegistration, o *protocol.ModelOffer) {
		ferr = eb1SetOfferField(o, "params_b", params)
	}); err != nil {
		return err
	}
	return ferr
}

func (s *eb1State) eb1Params(tower string, tp float64, direct string, dp float64) error {
	if err := s.eb1Param(tower, tp); err != nil {
		return err
	}
	return s.eb1Param(direct, dp)
}

func (s *eb1State) eb1NoParamsTower(_, direct string, dp float64) error {
	return s.eb1Param(direct, dp)
}

func (s *eb1State) eb1Ctx(name string, ctx int, estimated bool) error {
	return s.eb1Reg(name, func(_ *protocol.NodeRegistration, o *protocol.ModelOffer) {
		o.Ctx, o.CtxEstimated = ctx, estimated
	})
}

func (s *eb1State) eb1Ctxs(tower string, tc int, direct string, dc int) error {
	if err := s.eb1Ctx(tower, tc, false); err != nil {
		return err
	}
	return s.eb1Ctx(direct, dc, false)
}

func (s *eb1State) eb1CtxEstimate(tower string, tc int, direct string, dc int) error {
	if err := s.eb1Ctx(tower, tc, true); err != nil {
		return err
	}
	return s.eb1Ctx(direct, dc, false)
}

func (s *eb1State) eb1CtxNoDirect(tower string, tc int, model string) error {
	if err := s.eb1Ctx(tower, tc, false); err != nil {
		return err
	}
	return s.eb1NoDirect(model)
}

func (s *eb1State) eb1Region(name, region string) error {
	return s.eb1Reg(name, func(reg *protocol.NodeRegistration, _ *protocol.ModelOffer) { reg.Region = region })
}

func (s *eb1State) eb1Regions(tower, tr, direct, dr string) error {
	if err := s.eb1Region(tower, tr); err != nil {
		return err
	}
	return s.eb1Region(direct, dr)
}

func (s *eb1State) eb1NoRegionTower(tower, direct, dr string) error {
	return s.eb1Regions(tower, "", direct, dr)
}

func (s *eb1State) eb1Curated(name string, curated bool) error {
	return s.eb1Reg(name, func(reg *protocol.NodeRegistration, _ *protocol.ModelOffer) {
		reg.Curated, reg.CuratedProvider = curated, ""
		if curated {
			reg.CuratedProvider = "openrouter"
		}
	})
}

func (s *eb1State) eb1CuratedTowerHumanDirect(tower, direct string) error {
	if err := s.eb1Curated(tower, true); err != nil {
		return err
	}
	return s.eb1Curated(direct, false)
}

func (s *eb1State) eb1CuratedDirectHumanTower(direct, tower string) error {
	if err := s.eb1Curated(direct, true); err != nil {
		return err
	}
	return s.eb1Curated(tower, false)
}

func (s *eb1State) eb1CuratedBoth(direct, tower string) error {
	if err := s.eb1Curated(direct, true); err != nil {
		return err
	}
	return s.eb1Curated(tower, true)
}

func (s *eb1State) eb1CuratedOne(name string) error { return s.eb1Curated(name, true) }

// --- Given: trust ---------------------------------------------------------------------------

func (s *eb1State) eb1Verified(name string, verified bool) error {
	return s.eb1Trust(name, func(t *trustState) {
		t.probed, t.probeOK, t.probeFails, t.modelMismatch = true, true, 0, false
		t.probeCompleted = verified // verifiedServing: a passed canary that ran to completion
	})
}

func (s *eb1State) eb1TowerNotVerified(tower, direct string) error {
	if err := s.eb1Verified(tower, false); err != nil {
		return err
	}
	return s.eb1Verified(direct, true)
}

func (s *eb1State) eb1TowerVerified(tower, direct string) error {
	if err := s.eb1Verified(tower, true); err != nil {
		return err
	}
	return s.eb1Verified(direct, false)
}

func (s *eb1State) eb1Attested(name string) error {
	s.b.mu.Lock()
	s.b.confidential[s.st(name).id] = true
	s.b.mu.Unlock()
	return nil
}

func (s *eb1State) eb1NotAttested(name string) error {
	s.b.mu.Lock()
	delete(s.b.confidential, s.st(name).id)
	s.b.mu.Unlock()
	return nil
}

func (s *eb1State) eb1TierBTower(tower, direct string) error {
	// Tier B on the edge is a failing probe streak below the dead-probe bar (edgeEligibleC).
	if err := s.eb1Trust(tower, func(t *trustState) { t.probed, t.probeOK, t.probeFails = true, false, 2 }); err != nil {
		return err
	}
	return s.eb1Trust(direct, func(t *trustState) { t.probed, t.probeOK, t.probeFails = true, true, 0 })
}

// --- Given: private bands ---------------------------------------------------------------------

func (s *eb1State) eb1Band(band, station, model string) error {
	st := s.eb1Station(station, model, 0.10, 1.00)
	s.b.mu.Lock()
	s.b.private[st.id] = true
	s.b.mu.Unlock()
	code := "147.520 MHz · EB1" + strings.ToUpper(band) + "-" + strings.ToUpper(s.nonce[:4])
	s.bands[band] = code
	return s.db.CreateBand(store.Band{ID: "band_" + band + "_" + s.nonce, CodeHash: protocol.BandCodeHash(code),
		CodeDisplay: "147.520 MHz · ••••-••••", Owner: st.acct, NodeID: st.id, CreatedAt: time.Now().Unix()})
}

// eb1BandCooling cools the band's station through the REAL path: a band relay whose upstream
// answers 429 with that Retry-After.
func (s *eb1State) eb1BandCooling(band, station, model string, secs int) error {
	if err := s.eb1Band(band, station, model); err != nil {
		return err
	}
	s.eb1Answer(station, 429, strconv.Itoa(secs), "")
	s.eb1ResetReq()
	s.hdr["X-Roger-Freq"] = s.bands[band]
	if err := s.eb1Batch(1); err != nil {
		return err
	}
	if s.lastCode != 429 {
		return fmt.Errorf("cooling %s: the band relay = %d, want 429 (%.200s)", station, s.lastCode, s.lastBody)
	}
	s.eb1Answer(station, 200, "", "")
	return nil
}

// --- Given: models[] ----------------------------------------------------------------------------

func (s *eb1State) eb1TowerHosts(tower, model string) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	tw.model = model
	if err := s.eb1Publish(tw); err != nil {
		return err
	}
	s.b.mu.Lock()
	reg := s.b.nodes[tw.rp.nodeID]
	reg.Offers = nil // attributes are re-declared for the new model
	s.b.nodes[tw.rp.nodeID] = reg
	s.b.mu.Unlock()
	return nil
}

func (s *eb1State) eb1SplitModels429(direct, _, tower, m2, _ string) error {
	if err := s.eb1TowerHosts(tower, m2); err != nil {
		return err
	}
	return s.eb1Answers429(direct, "5")
}

func (s *eb1State) eb1NothingServes(_, _, _ string) error { return nil }

func (s *eb1State) eb1SplitQuants429(tower, m2, tq, direct, _, dq, _ string) error {
	if err := s.eb1TowerHosts(tower, m2); err != nil {
		return err
	}
	if err := s.eb1Quant(tower, tq); err != nil {
		return err
	}
	if err := s.eb1Quant(direct, dq); err != nil {
		return err
	}
	return s.eb1Answers429(direct, "5")
}

func (s *eb1State) eb1SplitPrices(direct, _ string, dOut float64, tower, m2 string, tOut float64) error {
	if err := s.eb1Price(direct, "out", dOut); err != nil {
		return err
	}
	if err := s.eb1TowerHosts(tower, m2); err != nil {
		return err
	}
	return s.eb1Price(tower, "out", tOut)
}

// --- Given: callers -----------------------------------------------------------------------------

func (s *eb1State) eb1Grant(nodes []string) error {
	owner := s.st("s1")
	secret := "rog-grant_" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	s.grantID, s.grantToken = "grant_"+s.nonce, secret
	if err := s.db.CreateGrant(store.Grant{ID: s.grantID, SecretHash: hex.EncodeToString(sum[:]), Owner: owner.acct,
		Label: "eb1", Free: true, Nodes: nodes, DailyCap: 1_000_000, CreatedAt: time.Now().Unix()}); err != nil {
		return err
	}
	// The grant's wallet gets a row, as every $0 payer in this runner does: on the Postgres
	// store a free settle against a wallet with no row serves the relay unreceipted (the known
	// free/self ensureSeeded gap), which is not what these scenarios are about.
	_, err := s.db.BalanceOf("g_"+s.grantID, s.b.seedFunds)
	return err
}

func (s *eb1State) eb1OwnerOf(name string) error {
	st := s.st(name)
	s.caller, s.callerPriv = "owner", st.ownerPriv
	// A realistic owner has a wallet row (see rpState.callerOwnsNode): a $0 self-use settle on
	// the Postgres store needs one.
	if _, err := s.db.BalanceOf(s.eb1CallerWallet(), s.b.seedFunds); err != nil {
		return fmt.Errorf("seed the owner's wallet row: %w", err)
	}
	return nil
}

func (s *eb1State) eb1CallerIs(kind string) error {
	switch {
	case strings.HasPrefix(kind, "a grant holder"):
		s.caller = "grant"
		return s.eb1Grant(nil)
	case strings.HasPrefix(kind, "a browser-session"):
		s.caller = "session"
		if err := os.Setenv("ROGERAI_WEB_ORIGIN", eb1Origin); err != nil {
			return err
		}
		gid, err := strconv.ParseInt(strings.TrimPrefix(s.wallet, "u_gh_"), 10, 64)
		if err != nil {
			return fmt.Errorf("the consumer wallet %q is not a GitHub wallet: %w", s.wallet, err)
		}
		s.sessionCk = s.b.signSession("buyer-"+s.nonce, gid, time.Now().Add(time.Hour).Unix())
		return nil
	case kind == "anonymous":
		s.caller = "anon"
		return nil
	case strings.HasPrefix(kind, "a free or self-use"):
		return s.eb1OwnerOf("s1")
	}
	return fmt.Errorf("unknown caller %q", kind)
}

func (s *eb1State) eb1GrantScoped(list string) error {
	l, err := eb1List(list)
	if err != nil {
		return err
	}
	var ids []string
	for _, n := range l {
		ids = append(ids, s.idOf(fmt.Sprint(n)))
	}
	return s.eb1Grant(ids)
}

func (s *eb1State) eb1ConsumerOwns(name string) error { return s.eb1OwnerOf(name) }

// --- Then: caller outcomes ----------------------------------------------------------------------

func (s *eb1State) eb1EveryDirectIfGrant(name string) error { return s.eb1EveryDirect(name) }

// "served by s1 if free, else refused as today": an anonymous caller cannot pay, so each relay
// is either a 200 from the direct station (a free offer) or today's refusal (401 log in to
// spend); never a Tower, never a 200 from anyone else.
func (s *eb1State) eb1EveryDirectIfFree(name string) error {
	st := s.st(name)
	for i, r := range s.batch {
		switch {
		case r.code == 200 && r.hdr.Get("X-RogerAI-Relay") == "" && r.hdr.Get("X-RogerAI-Provider") == st.id:
		case r.code == http.StatusUnauthorized:
		default:
			return fmt.Errorf("relay %d of %d = %d (provider=%q relay=%q), want a 200 from %q or today's 401: %.200s",
				i+1, len(s.batch), r.code, r.hdr.Get("X-RogerAI-Provider"), r.hdr.Get("X-RogerAI-Relay"), name, r.body)
		}
	}
	return nil
}

// --- Then: attempts ------------------------------------------------------------------------------

func (s *eb1State) eb1Tried(name string, want int64) error {
	if _, err := s.eb1T(name); err != nil {
		return err
	}
	if got := s.eb1Submits(name); got != want {
		return fmt.Errorf("the bridge handed %q's hub %d sealed request(s), want %d (response %d %.200s)", name, got, want, s.lastCode, s.lastBody)
	}
	return nil
}

func (s *eb1State) eb1TriedOnce(name string) error { return s.eb1Tried(name, 1) }

func (s *eb1State) eb1TriedOnceAnd429(name string) error {
	if err := s.eb1Tried(name, 1); err != nil {
		return err
	}
	if s.lastCode != 429 {
		return fmt.Errorf("response = %d, want the Tower's 429: %.200s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *eb1State) eb1TriedAndServedBy(tower, station string) error {
	if got := s.eb1Submits(tower); got < 1 {
		return fmt.Errorf("the bridge never tried %q (0 sealed requests at its hub; response %d %.200s)", tower, s.lastCode, s.lastBody)
	}
	return s.eb1EveryDirect(station)
}

func (s *eb1State) eb1TriedOnceFellBack(tower, station string) error {
	if err := s.eb1Tried(tower, 1); err != nil {
		return err
	}
	return s.eb1EveryDirect(station)
}

// eb1TriedOnly: the live Tower was tried; no other Tower was (a dead Tower that is tried
// leaves edgebridge.go's "tower <id> failed" line).
func (s *eb1State) eb1TriedOnly(name string) error {
	if got := s.eb1Submits(name); got < 1 {
		return fmt.Errorf("the bridge never tried %q (response %d %.200s)", name, s.lastCode, s.lastBody)
	}
	lines := s.eb1EdgeLogLines()
	for n, tw := range s.tw {
		if n == name {
			continue
		}
		for _, l := range lines {
			if strings.Contains(l, tw.rp.id) {
				return fmt.Errorf("the bridge also tried Tower %q: %s", n, l)
			}
		}
		if s.eb1Submits(n) != 0 {
			return fmt.Errorf("the bridge also submitted to Tower %q's hub", n)
		}
	}
	return nil
}

func (s *eb1State) eb1LastUpstreamErrRA() error { return s.eb1Resp429RA("") }

func (s *eb1State) eb1OneAttemptTo(name string) error {
	st := s.st(name)
	if d := st.upstreamCount() - s.upBefore[name]; d != 1 {
		return fmt.Errorf("%q received %d request(s), want exactly 1", name, d)
	}
	for n, other := range s.stations {
		if n != name && other.upstreamCount() != s.upBefore[n] {
			return fmt.Errorf("%q was also tried", n)
		}
	}
	return nil
}

func (s *eb1State) eb1Attempt1Then2(direct, m1, tower, m2 string) error {
	if d := s.st(direct).upstreamCount() - s.upBefore[direct]; d != 1 {
		return fmt.Errorf("attempt 1: %q received %d request(s) for %q, want 1 (response %d %.200s)", direct, d, m1, s.lastCode, s.lastBody)
	}
	if got := s.eb1Submits(tower); got != 1 {
		return fmt.Errorf("attempt 2: %q's hub received %d sealed request(s) for %q, want 1 (response %d %.200s)", tower, got, m2, s.lastCode, s.lastBody)
	}
	return nil
}

func (s *eb1State) eb1Attempt1NoSecond(direct string) error {
	if d := s.st(direct).upstreamCount() - s.upBefore[direct]; d != 1 {
		return fmt.Errorf("attempt 1: %q received %d request(s), want 1 (response %d %.200s)", direct, d, s.lastCode, s.lastBody)
	}
	if s.eb1AnySubmit() {
		return fmt.Errorf("there was an attempt 2: a Tower's hub received a sealed request")
	}
	return nil
}

// --- Then: per-request cap ------------------------------------------------------------------------

func eb1MaxTokensOf(body []byte) (int, bool) {
	var req struct {
		MaxTokens *int `json:"max_tokens"`
	}
	if json.Unmarshal(body, &req) != nil || req.MaxTokens == nil {
		return 0, false
	}
	return *req.MaxTokens, true
}

func (s *eb1State) eb1CapBound(bodies [][]byte, who string, capUSD, out float64) error {
	if len(bodies) == 0 {
		return fmt.Errorf("no relay reached %s, so no max_tokens can be read (%s)", who, s.eb1Tally())
	}
	inCost := float64(approxPromptTokens(s.sent)) * 0.10 / 1e6
	bound := int(math.Floor((capUSD - inCost) / out * 1e6))
	for i, b := range bodies {
		mt, ok := eb1MaxTokensOf(b)
		if !ok {
			return fmt.Errorf("request %d handed to %s carries no max_tokens", i+1, who)
		}
		if mt > bound {
			return fmt.Errorf("request %d handed to %s carries max_tokens %d, want at most %d ($%g buys that at %g/1M after the input cost)", i+1, who, mt, bound, capUSD, out)
		}
	}
	return nil
}

func (s *eb1State) eb1TowerCapBound(tower string, capUSD, out float64) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	return s.eb1CapBound(tw.eb1Bodies()[s.twBefore[tower]:], "Tower "+tower+"'s station", capUSD, out)
}

func (s *eb1State) eb1DirectCapBound(direct string, capUSD, out float64) error {
	all := s.eb1StationBodies(direct)
	return s.eb1CapBound(all, direct, capUSD, out)
}

func (s *eb1State) eb1EveryHoldWas(amount float64) error {
	holds, err := s.eb1NewHolds()
	if err != nil {
		return err
	}
	if len(holds) == 0 {
		return fmt.Errorf("no hold was placed (%s)", s.eb1Tally())
	}
	for _, h := range holds {
		if math.Abs(h-amount) > 1e-9 {
			return fmt.Errorf("a hold of $%g was placed, want every hold to be $%g (all: %v)", h, amount, holds)
		}
	}
	return nil
}

// --- Then: counters and logs -----------------------------------------------------------------------

// eb1Find walks /admin/live for a counter by name (and an optional label): `name` -> number,
// `name` -> {label: number}, or a flat `name_label` / `name{label}`.
func eb1Find(v any, name, label string) (float64, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, false
	}
	keys := []string{name}
	if label != "" {
		keys = []string{name + "_" + label, name + "{" + label + "}"}
		if sub, ok := m[name].(map[string]any); ok {
			if f, ok := sub[label].(float64); ok {
				return f, true
			}
		}
	}
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return f, true
		}
	}
	for _, child := range m {
		if f, ok := eb1Find(child, name, label); ok {
			return f, true
		}
	}
	return 0, false
}

func (s *eb1State) eb1Counter(name, label string) (float64, bool) {
	if err := s.readAdminLive(); err != nil {
		return 0, false
	}
	return eb1Find(any(s.adminResp), name, label)
}

func (s *eb1State) eb1AdminShows(name, label string, want int) error {
	got, ok := s.eb1Counter(name, label)
	if !ok {
		return fmt.Errorf("/admin/live carries no %s{%s} counter", name, label)
	}
	if int(got) != want {
		return fmt.Errorf("/admin/live %s{%s} = %v, want %d", name, label, got, want)
	}
	return nil
}

func (s *eb1State) eb1DeclinedIs(label string, want int) error {
	return s.eb1AdminShows("edge_bridge_declined", label, want)
}

func (s *eb1State) eb1DeclinedBoth(l1 string, w1 int, l2 string, w2 int) error {
	if err := s.eb1DeclinedIs(l1, w1); err != nil {
		return err
	}
	return s.eb1DeclinedIs(l2, w2)
}

func (s *eb1State) eb1CoinUnchanged() error {
	got, ok := s.eb1Counter("edge_coin_flips", "")
	if !ok || !s.coinKnown {
		return fmt.Errorf("/admin/live carries no edge_coin_flips counter: a coin that is never consulted cannot be told from one that is")
	}
	if got != s.coinBase {
		return fmt.Errorf("edge_coin_flips moved from %v to %v under a strict sort; the coin must not be consulted", s.coinBase, got)
	}
	return nil
}

func (s *eb1State) eb1CoinIncreased(want int) error {
	got, ok := s.eb1Counter("edge_coin_flips", "")
	if !ok || !s.coinKnown {
		return fmt.Errorf("/admin/live carries no edge_coin_flips counter")
	}
	if int(got-s.coinBase) != want {
		return fmt.Errorf("edge_coin_flips increased by %v, want exactly %d", got-s.coinBase, want)
	}
	return nil
}

func (s *eb1State) eb1ReadAdmin() error { return s.readAdminLive() }

func (s *eb1State) eb1TenEligibleBalanced(n int) error {
	s.eb1ResetReq()
	return s.eb1Batch(n)
}

func (s *eb1State) eb1TenEligibleSort(n int, sortBy string) error {
	s.eb1ResetReq()
	s.provider["sort"] = sortBy
	return s.eb1Batch(n)
}

func (s *eb1State) eb1DeclineMix(nTPS, nReq int) error {
	if err := s.eb1MeasuresTPS("t1-a", 10, "s1", 40); err != nil {
		return err
	}
	if err := s.eb1Canary("s1", true); err != nil {
		return err
	}
	if err := s.eb1WhenMany(nTPS, "with roger.min_tps 20"); err != nil {
		return err
	}
	return s.eb1WhenMany(nReq, `with roger.require ["tools"]`)
}

// eb1RequestID is the request id of the last relay, as far as the response reveals it: the
// receipt's request id with its attempt suffix removed.
func (s *eb1State) eb1RequestID() string {
	rec, err := protocol.DecodeReceipt(s.lastHdr.Get("X-RogerAI-Receipt"))
	if err != nil || rec.RequestID == "" {
		return ""
	}
	if i := strings.LastIndex(rec.RequestID, "-"); i > 0 {
		return rec.RequestID[:i]
	}
	return rec.RequestID
}

func (s *eb1State) eb1OneDeclineLine(tower, constraint string) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	all := s.logs.String()
	if s.logMark > len(all) {
		s.logMark = 0
	}
	var hits []string
	for _, l := range strings.Split(all[s.logMark:], "\n") {
		if strings.Contains(l, tw.rp.id) && strings.Contains(l, constraint) {
			hits = append(hits, l)
		}
	}
	if len(hits) != 1 {
		return fmt.Errorf("%d log line(s) name Tower %q and %q, want exactly one: %v", len(hits), tower, constraint, hits)
	}
	if id := s.eb1RequestID(); id == "" || !strings.Contains(hits[0], id) {
		return fmt.Errorf("the decline line does not name the request (%q): %s", id, hits[0])
	}
	return nil
}

func (s *eb1State) eb1NoBandInLogs() error {
	logs := s.logs.String()
	for b, code := range s.bands {
		if strings.Contains(logs, code) {
			return fmt.Errorf("a log line contains band %q's code", b)
		}
	}
	if strings.Contains(logs, "MHz") {
		return fmt.Errorf("a log line contains a band code")
	}
	return nil
}

func (s *eb1State) eb1FanoutBand(tower string) error {
	t := s.eb1Tally()
	if t.ok200 != t.total {
		return fmt.Errorf("not every relay was served (%s)", t)
	}
	// edge_fanout.feature: a fair request-seeded coin between two eligible fabrics. Over 1000
	// relays a fair coin stays inside 40-60 % by more than six standard deviations.
	share := float64(t.relay[tower]) / float64(t.total)
	if share < 0.40 || share > 0.60 {
		return fmt.Errorf("%.1f%% of %d relays rode %q, want the fair-coin band 40-60%% (%s)", share*100, t.total, tower, t)
	}
	return nil
}

// --- two Towers: ranking by attempt order -------------------------------------------------------

// eb1FirstShare is, per Tower, the share of the batch in which the bridge tried it FIRST.
// Every Tower in a ranking scenario is on a dead data plane, so each relay leaves one
// "tower <id> failed" line per attempt, in attempt order (edgebridge.go); with two Towers the
// lines come in pairs and the first of each pair is the Tower the bridge ranked first.
func (s *eb1State) eb1FirstShare() map[string]float64 {
	out := map[string]float64{}
	byID := map[string]string{}
	for name, tw := range s.tw {
		byID[tw.rp.id] = name
		out[name] = 0
	}
	firsts, seen := 0.0, 0
	for _, l := range s.eb1EdgeLogLines() {
		i := strings.Index(l, "edge bridge: tower ")
		if i < 0 || !strings.Contains(l, " failed for ") {
			continue
		}
		id := strings.Fields(l[i+len("edge bridge: tower "):])[0]
		if seen%len(s.tw) == 0 {
			out[byID[id]]++
			firsts++
		}
		seen++
	}
	for name := range out {
		if firsts > 0 {
			out[name] /= firsts
		}
	}
	return out
}

func (s *eb1State) eb1TwoTowersTpsPrice(t1 string, tps1 int, out1 float64, t2 string, tps2 int, out2 float64, model string) error {
	if err := s.eb1NoDirect(model); err != nil {
		return err
	}
	// Two LIVE fabrics for one model cannot be stood up, and a live Tower beside a dead one
	// is not a fair ranking either: in this fabric nothing settles a served attempt, so the
	// live Tower's in-flight load only grows. Both Towers therefore sit on a dead data plane
	// (the Background's t1 is re-published onto one) and the ranking is read from the order
	// the bridge tries them.
	tw1, err := s.eb1T(t1)
	if err != nil {
		return err
	}
	tw1.endpoint, tw1.live = "127.0.0.1:1", false
	if err := s.eb1Price(t1, "out", out1); err != nil {
		return err
	}
	if err := s.eb1TPS(t1, float64(tps1)); err != nil {
		return err
	}
	if _, err := s.eb1Dead(t2, model, 0.10, out2, float64(tps2)); err != nil {
		return err
	}
	// The balanced baseline, measured before any pref is stated.
	s.eb1ResetReq()
	if err := s.eb1Batch(200); err != nil {
		return err
	}
	s.baseFirst = s.eb1FirstShare()
	return nil
}

func (s *eb1State) eb1MoreOftenThanBalanced(tower string) error {
	if s.baseFirst == nil {
		return fmt.Errorf("no balanced baseline was measured")
	}
	got := s.eb1FirstShare()[tower]
	base := s.baseFirst[tower]
	// "More often" cannot be exceeded once balanced already ranks this Tower first every time
	// (a two-row P2C draw is decided by score): there, staying at every time is the most a
	// pref can do, and the scenario's other half (the other pref lifting the other Tower from
	// its balanced share) is what shows the pref reached the bridge.
	if got > base || (base >= 1 && got >= 1) {
		return nil
	}
	return fmt.Errorf("%q was tried first in %.1f%% of relays under %v, not more than the %.1f%% under balanced",
		tower, got*100, s.roger["pref"], base*100)
}

// --- Then: what the Tower sees ---------------------------------------------------------------------

func (s *eb1State) eb1LastSealed(tower string) ([]byte, error) {
	tw, err := s.eb1T(tower)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(tower, "-a")
	bodies := tw.eb1Bodies()
	if len(bodies) <= s.twBefore[name] {
		return nil, fmt.Errorf("nothing was sealed to %q for this request (response %d %.200s)", tower, s.lastCode, s.lastBody)
	}
	return bodies[len(bodies)-1], nil
}

func (s *eb1State) eb1SealedNoCarriers(tower string) error {
	body, err := s.eb1LastSealed(tower)
	if err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("the plaintext sealed to %q is not JSON: %.200s", tower, body)
	}
	for _, k := range []string{"provider", "roger", "models"} {
		if _, ok := m[k]; ok {
			return fmt.Errorf("the plaintext sealed to %q still carries %q: %.300s", tower, k, body)
		}
	}
	return nil
}

func (s *eb1State) eb1SealedModel() error {
	body, err := s.eb1LastSealed("t1-a")
	if err != nil {
		return err
	}
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	if served := s.lastHdr.Get("X-RogerAI-Model"); m.Model == "" || m.Model != served {
		return fmt.Errorf("the sealed body's model is %q, the served model (X-RogerAI-Model) is %q", m.Model, served)
	}
	return nil
}

func (s *eb1State) eb1SealedOtherwiseIdentical() error {
	body, err := s.eb1LastSealed("t1-a")
	if err != nil {
		return err
	}
	var got, sent map[string]json.RawMessage
	if json.Unmarshal(body, &got) != nil || json.Unmarshal(s.sent, &sent) != nil {
		return fmt.Errorf("cannot compare the sealed body with what the consumer sent")
	}
	skip := map[string]bool{"provider": true, "roger": true, "models": true, "model": true, "stream": true}
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	for k := range sent {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		if !skip[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		if !bytes.Equal(got[k], sent[k]) {
			return fmt.Errorf("key %q differs: the consumer sent %s, the Tower's station was handed %s", k, sent[k], got[k])
		}
	}
	return nil
}

// --- Then: what the consumer learns -----------------------------------------------------------------

func (s *eb1State) eb1ModelHeaderIs(model string) error {
	if got := s.lastHdr.Get("X-RogerAI-Model"); got != model {
		return fmt.Errorf("X-RogerAI-Model = %q, want %q (response %d %.200s)", got, model, s.lastCode, s.lastBody)
	}
	return nil
}

func (s *eb1State) eb1ModelAndRelay(model, tower string) error {
	if err := s.eb1ModelHeaderIs(model); err != nil {
		return err
	}
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	if got := s.lastHdr.Get("X-RogerAI-Relay"); got != tw.rp.id {
		return fmt.Errorf("X-RogerAI-Relay = %q, want Tower %q (%s)", got, tower, tw.rp.id)
	}
	return nil
}

func (s *eb1State) eb1ProviderAndRelay(tower string) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	want := tw.stationID + "." + relayDomain()
	if got := s.lastHdr.Get("X-RogerAI-Provider"); got != want {
		return fmt.Errorf("X-RogerAI-Provider = %q, want the relay name %q", got, want)
	}
	if got := s.lastHdr.Get("X-RogerAI-Relay"); got != tw.rp.id {
		return fmt.Errorf("X-RogerAI-Relay = %q, want Tower %q (%s)", got, tower, tw.rp.id)
	}
	return nil
}

func (s *eb1State) eb1BridgedReceipt() (protocol.UsageReceipt, string, error) {
	raw := s.lastHdr.Get("X-RogerAI-Receipt")
	if raw == "" {
		return protocol.UsageReceipt{}, "", fmt.Errorf("the bridged answer carries no X-RogerAI-Receipt (headers: %v)", s.lastHdr)
	}
	rec, err := protocol.DecodeReceipt(raw)
	if err != nil {
		return rec, raw, fmt.Errorf("the bridged receipt does not decode: %w", err)
	}
	return rec, raw, nil
}

func (s *eb1State) eb1ReceiptNames(model, tower string) error {
	rec, raw, err := s.eb1BridgedReceipt()
	if err != nil {
		return err
	}
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	if rec.Model != model {
		return fmt.Errorf("the receipt names model %q, want %q", rec.Model, model)
	}
	if !strings.Contains(raw, tw.stationID) {
		return fmt.Errorf("the receipt does not name the relay (%s): %s", tw.stationID, raw)
	}
	if !strings.Contains(raw, tw.rp.id) {
		return fmt.Errorf("the receipt does not name Tower %q (%s): %s", tower, tw.rp.id, raw)
	}
	return nil
}

func (s *eb1State) eb1ReceiptModelEqualsHeader() error {
	rec, _, err := s.eb1BridgedReceipt()
	if err != nil {
		return err
	}
	if h := s.lastHdr.Get("X-RogerAI-Model"); rec.Model != h {
		return fmt.Errorf("the receipt's model %q differs from X-RogerAI-Model %q", rec.Model, h)
	}
	return nil
}

func (s *eb1State) eb1ReceiptNodeSig(tower string) error {
	rec, _, err := s.eb1BridgedReceipt()
	if err != nil {
		return err
	}
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	if !rec.VerifyNode(hexOf(tw.priv.Public().(ed25519.PublicKey))) {
		return fmt.Errorf("the receipt's node signature does not verify against Tower %q's relay key", tower)
	}
	return nil
}

func (s *eb1State) eb1ReceiptBrokerSig() error {
	rec, _, err := s.eb1BridgedReceipt()
	if err != nil {
		return err
	}
	pub := hexOf(s.b.priv.Public().(ed25519.PublicKey))
	if !rec.VerifyBroker(pub) {
		return fmt.Errorf("the bridged receipt's broker signature does not verify under VerifyBroker")
	}
	if ok, covers := rec.VerifyBrokerCoverage(pub); !ok || !covers {
		return fmt.Errorf("the bridged receipt's broker signature is not the covering scheme a direct receipt uses (ok=%v covers=%v)", ok, covers)
	}
	return nil
}

func (s *eb1State) eb1ReceiptNotDirectKey() error {
	rec, _, err := s.eb1BridgedReceipt()
	if err != nil {
		return err
	}
	for n, st := range s.stations {
		if rec.VerifyNode(st.pubHex) {
			return fmt.Errorf("the bridged receipt verifies against direct station %q's key", n)
		}
	}
	return nil
}

func (s *eb1State) eb1UsageChunkNames(model, tower string) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	var frames []string
	for _, l := range strings.Split(string(s.lastBody), "\n") {
		if strings.HasPrefix(l, "data: ") {
			frames = append(frames, strings.TrimPrefix(l, "data: "))
		}
	}
	if len(frames) < 2 || frames[len(frames)-1] != "[DONE]" {
		return fmt.Errorf("the stream does not end with a frame before [DONE]: %.300s", s.lastBody)
	}
	var chunk struct {
		Choices []any `json:"choices"`
		Usage   struct {
			Rogerai map[string]any `json:"rogerai"`
		} `json:"usage"`
	}
	last := frames[len(frames)-2]
	if err := json.Unmarshal([]byte(last), &chunk); err != nil {
		return fmt.Errorf("the frame before [DONE] is not JSON: %s", last)
	}
	if len(chunk.Choices) != 0 || chunk.Usage.Rogerai == nil {
		return fmt.Errorf("the frame before [DONE] is not the broker's usage chunk (empty choices + usage.rogerai): %s", last)
	}
	if chunk.Usage.Rogerai["model"] != model || chunk.Usage.Rogerai["relay"] != tw.rp.id {
		return fmt.Errorf("the usage chunk names model=%v relay=%v, want %q and %q (%s)", chunk.Usage.Rogerai["model"], chunk.Usage.Rogerai["relay"], model, tower, tw.rp.id)
	}
	return nil
}

func (s *eb1State) eb1CostZeroNoReceipt() error {
	r, err := s.eb1Last()
	if err != nil {
		return err
	}
	if c := r.hdr.Get("X-RogerAI-Cost"); c != "0" {
		return fmt.Errorf("X-RogerAI-Cost = %q, want 0 (response %d %.200s)", c, r.code, r.body)
	}
	if rc := r.hdr.Get("X-RogerAI-Receipt"); rc != "" {
		return fmt.Errorf("a refusal carries a receipt: %s", rc)
	}
	return nil
}

func (s *eb1State) eb1ConfidentialMessage() error {
	if !bytes.Contains(bytes.ToLower(s.lastBody), []byte("confidential")) {
		return fmt.Errorf("the message does not say no confidential station matches: %.240s", s.lastBody)
	}
	return nil
}

// eb1ServedByWithModel: one relay, but a model both fabrics host is served through the Tower
// half the time by the coin alone; the relay is re-fired until the coin lands direct.
func (s *eb1State) eb1ServedByWithModel(name, model string) error {
	st := s.st(name)
	for i := 0; i < eb1CoinTries; i++ {
		if s.lastCode != 200 {
			return fmt.Errorf("the relay = %d, want 200 from %q: %.220s", s.lastCode, name, s.lastBody)
		}
		if s.lastHdr.Get("X-RogerAI-Relay") == "" {
			break
		}
		if err := s.eb1Batch(1); err != nil {
			return err
		}
	}
	if s.lastHdr.Get("X-RogerAI-Provider") != st.id {
		return fmt.Errorf("the relay was served by %q, want %q", s.lastHdr.Get("X-RogerAI-Provider"), name)
	}
	return s.eb1ModelHeaderIs(model)
}

func (s *eb1State) eb1ServedThroughOne(tower string) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	if s.lastCode != 200 || s.lastHdr.Get("X-RogerAI-Relay") != tw.rp.id {
		return fmt.Errorf("the relay = %d relay=%q, want 200 through %q: %.220s", s.lastCode, s.lastHdr.Get("X-RogerAI-Relay"), tower, s.lastBody)
	}
	return nil
}

func (s *eb1State) eb1ServedAtZero(name string) error {
	if err := s.eb1EveryDirect(name); err != nil {
		return err
	}
	if c := s.lastHdr.Get("X-RogerAI-Cost"); c != "0" {
		return fmt.Errorf("the owner's relay was billed X-RogerAI-Cost=%q, want 0", c)
	}
	return nil
}

func (s *eb1State) eb1RetryServedBy(name string) error { return s.eb1EveryDirect(name) }

func (s *eb1State) eb1PrevAttemptFailed(tower string) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	tw.eb1Script(eb1TowerStatus(500, ""))
	return nil
}

// --- Then: holds across fabrics ---------------------------------------------------------------------

func (s *eb1State) eb1HoldCovers(out float64) error {
	holds, err := s.eb1NewHolds()
	if err != nil {
		return err
	}
	if len(holds) != 1 {
		return fmt.Errorf("%d hold(s) were placed (%v), want exactly one covering the priciest pair (response %d %.200s)", len(holds), holds, s.lastCode, s.lastBody)
	}
	want := estimateMaxCost(s.sent, 0.10, out, 32768)
	if holds[0]+1e-12 < want {
		return fmt.Errorf("the hold is $%g, which does not cover %g/1M (that needs $%g)", holds[0], out, want)
	}
	return nil
}

func (s *eb1State) eb1ChargedAt(name string, out float64) error {
	if err := s.eb1EveryDirect(name); err != nil {
		return err
	}
	rec, err := protocol.DecodeReceipt(s.lastHdr.Get("X-RogerAI-Receipt"))
	if err != nil {
		return fmt.Errorf("no receipt on the relay served by %q", name)
	}
	if math.Abs(rec.PriceOut-out) > 1e-9 {
		return fmt.Errorf("the consumer was charged at out %g/1M, want %g", rec.PriceOut, out)
	}
	return nil
}

// --- Then: anonymous uniformity -----------------------------------------------------------------------

func (s *eb1State) eb1IdenticalToUnknown(cost, list string) error {
	body, hdrCost := append([]byte(nil), s.lastBody...), s.lastHdr.Get("X-RogerAI-Cost")
	keepBatch, keepSub, keepLen, keepMark := s.batch, s.subBefore, s.ledgerLen, s.ledgerMark // maps are not re-made by a bare fire
	l, err := eb1List(list)
	if err != nil {
		return err
	}
	s.provider["only"] = l
	s.eb1Fire()
	other, otherCost := s.lastBody, s.lastHdr.Get("X-RogerAI-Cost")
	s.batch = keepBatch // the comparison relay is not part of the scenario's batch
	s.subBefore, s.ledgerLen, s.ledgerMark = keepSub, keepLen, keepMark
	last := s.batch[len(s.batch)-1]
	s.lastCode, s.lastBody, s.lastHdr = last.code, last.body, last.hdr
	if hdrCost != cost || otherCost != cost {
		return fmt.Errorf("X-RogerAI-Cost = %q (a Tower id) and %q (an unknown id), want %q on both", hdrCost, otherCost, cost)
	}
	if !bytes.Equal(body, other) {
		return fmt.Errorf("naming a Tower answers %.200s but naming an unknown id answers %.200s: the difference discloses which ids are Towers", body, other)
	}
	return nil
}

// --- Given / Then: namespaces and spoofing --------------------------------------------------------------

// eb1SpoofRelayName builds the only name collision the system admits (see the file header):
// the direct station takes a Core-shaped id and a second, approved Tower carries a station
// with that very id.
func (s *eb1State) eb1SpoofRelayName(name string) error {
	sum := sha256.Sum256([]byte("spoof|" + s.nonce))
	id := "st-" + hex.EncodeToString(sum[:8])
	if err := s.eb1Rekey(name, id); err != nil {
		return err
	}
	op := signedInOperator(s.t, s.b, "spoof-op-"+s.nonce)
	lt := enrolledTower(s.t, s.b, op.login)
	if err := s.b.tower.registry.Transition(lt.id, admit.StateActive); err != nil {
		return err
	}
	attachStation(s.t, s.b, id, lt.id, op.login)
	tw := &eb1Tower{rp: &rpTower{name: "spoof", id: lt.id, nodeID: "n-spoof-" + s.nonce}, stationID: id, model: s.model,
		priv: lt.priv, endpoint: "127.0.0.1:1", inMicros: eb1Micros(0.10), outMicros: eb1Micros(1.00)}
	s.b.mu.Lock()
	s.b.nodes[tw.rp.nodeID] = protocol.NodeRegistration{NodeID: tw.rp.nodeID}
	s.b.lastSeen[tw.rp.nodeID] = time.Now()
	s.b.trust[tw.rp.nodeID] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	s.towers["spoof"], s.tw["spoof"] = tw.rp, tw
	return s.eb1Publish(tw)
}

func (s *eb1State) eb1SpoofAndIgnore(tower, relayName, _ string) error {
	if _, err := s.eb1T(tower); err != nil {
		return err
	}
	return s.eb1SpoofRelayName(relayName)
}

func (s *eb1State) eb1EveryDirectStation(name string) error { return s.eb1EveryDirect(name) }

// eb1IDCollision gives a second direct station the node id that IS Tower t1's id.
func (s *eb1State) eb1IDCollision(_, tower, model string) error {
	tw, err := s.eb1T(tower)
	if err != nil {
		return err
	}
	s.eb1Station("twin", model, 0.10, 1.00)
	return s.eb1Rekey("twin", tw.rp.id)
}

func (s *eb1State) eb1EitherNamespace() error {
	t := s.eb1Tally()
	if t.ok200 != t.total || len(t.other) != 0 {
		return fmt.Errorf("want every relay served by the node or through the Tower that carry the listed id (%s)", t)
	}
	if t.direct["twin"]+t.relay["t1"] != t.total {
		return fmt.Errorf("a relay was served by neither holder of the listed id (%s)", t)
	}
	return nil
}

func (s *eb1State) eb1CollisionWarned() error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	raw, _ := json.Marshal(s.adminResp)
	if n := strings.Count(strings.ToLower(string(raw)), "collision"); n != 1 {
		return fmt.Errorf("/admin/live mentions an id collision %d time(s), want exactly once", n)
	}
	return nil
}

func (s *eb1State) eb1DeclaresQuantParamsMismatch(tower, quant string, params float64, _ string) error {
	if err := s.eb1Quant(tower, quant); err != nil {
		return err
	}
	return s.eb1Param(tower, params)
}

func (s *eb1State) eb1FlaggedMismatch() error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	raw, _ := json.Marshal(s.adminResp)
	tw, _ := s.eb1T("t1")
	low := strings.ToLower(string(raw))
	if !strings.Contains(low, "params") || !strings.Contains(low, "mismatch") || !strings.Contains(string(raw), tw.rp.nodeID) {
		return fmt.Errorf("/admin/live does not flag the row's params_b declaration as a mismatch")
	}
	return nil
}

func (s *eb1State) eb1RowIneligible() error {
	t := s.eb1Tally()
	if t.relay["t1"] != 0 {
		return fmt.Errorf("%d relay(s) rode the Tower whose declaration contradicts the known-model table (%s)", t.relay["t1"], t)
	}
	return nil
}

// --- wiring ---------------------------------------------------------------------------------------------

func TestRoutingEdgeBridgeParityBDD(t *testing.T) {
	st := &eb1State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.eb1Reset()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.eb1Teardown()
				return ctx, nil
			})

			// Background
			sc.Step(`^a broker with the tower subsystem and a signed-in consumer who accepted the terms$`, st.eb1Background)
			sc.Step(`^a direct station "([^"]+)" serves "([^"]+)", healthy, seen just now, out ([0-9.]+) per 1M$`, st.eb1DirectStation)
			sc.Step(`^an approved Tower "([^"]+)" hosts "([^"]+)" through station "([^"]+)" at out ([0-9.]+) per 1M$`, st.eb1ApprovedTower)
			sc.Step(`^the fan-out coin is under test control$`, st.eb1CoinControl)

			// Given: the stated constraint
			sc.Step(`^"([^"]+)" fails a constraint the consumer stated and "([^"]+)" passes it$`, st.eb1Fails)
			sc.Step(`^"([^"]+)" fails a constraint the consumer stated$`, st.eb1FailsOnly)
			sc.Step(`^both "([^"]+)" and "([^"]+)" fail a constraint the consumer stated$`, st.eb1BothFail)
			sc.Step(`^no direct station serves "([^"]+)"$`, st.eb1NoDirect)
			sc.Step(`^no direct station serves "([^"]+)" and "([^"]+)" fails the consumer's constraint$`, st.eb1NoDirectTowerFails)
			sc.Step(`^Towers "([^"]+)" and "([^"]+)" host "([^"]+)", "t1-a" 429s, and "t2-a" fails the consumer's constraint$`, st.eb1TwoTowers429Fail)
			sc.Step(`^"([^"]+)" 429s with Retry-After (\d+)$`, st.eb1Answers429)
			sc.Step(`^"([^"]+)" 429s$`, st.eb1Answers429Plain)

			// Given: prices
			sc.Step(`^"([^"]+)" prices (in|out) ([0-9.]+) and "([^"]+)" prices (in|out) ([0-9.]+)$`, st.eb1Prices)
			sc.Step(`^"([^"]+)" prices out ([0-9.]+) and "([^"]+)" prices out ([0-9.]+), both in ([0-9.]+)$`, st.eb1PricesBothIn)
			sc.Step(`^"([^"]+)" is priced at out \$([0-9.]+) and "([^"]+)" at out \$([0-9.]+) for "([^"]+)"$`, st.eb1PricedAt)
			sc.Step(`^"([^"]+)" and "([^"]+)" are priced identically and "([^"]+)" has the better score$`, st.eb1SamePriceBetterScore)

			// Given: measurements
			sc.Step(`^the node behind "([^"]+)" measures (\d+) tok/s and "([^"]+)" measures (\d+) tok/s$`, st.eb1MeasuresTPS)
			sc.Step(`^the node behind "([^"]+)" has no tps measurement and "([^"]+)" measures (\d+) tok/s$`, st.eb1NoTPS)
			sc.Step(`^the node behind "([^"]+)" measures (\d+)ms TTFT and "([^"]+)" (\d+)ms$`, st.eb1MeasuresTTFT)
			sc.Step(`^the node behind "([^"]+)" has no TTFT measurement and "([^"]+)" measures (\d+)ms$`, st.eb1NoTTFT)

			// Given: capabilities
			sc.Step(`^the node behind "([^"]+)" never passed a tool-call canary and "([^"]+)" did$`, st.eb1TowerNoCanaryDirectDid)
			sc.Step(`^the node behind "([^"]+)" passed a tool-call canary and "([^"]+)" did not$`, st.eb1TowerCanaryDirectNot)
			sc.Step(`^"([^"]+)" declares "tools" but its node never passed a canary, and "([^"]+)" passed$`, st.eb1TowerDeclaresToolsUnverified)
			sc.Step(`^"([^"]+)" does not declare "vision" and "([^"]+)" does$`, st.eb1VisionOnlyDirect)
			sc.Step(`^"([^"]+)" declares "vision" and "([^"]+)" does not$`, st.eb1VisionOnlyTower)
			sc.Step(`^neither "([^"]+)" nor "([^"]+)" has "([^"]+)"$`, st.eb1VisionNeither)

			// Given: declared attributes
			sc.Step(`^"([^"]+)" declares quant "([^"]+)" and "([^"]+)" declares quant "([^"]+)"$`, st.eb1Quants)
			sc.Step(`^"([^"]+)" declares no quant and "([^"]+)" declares quant "([^"]+)"$`, st.eb1NoQuantTower)
			sc.Step(`^neither "([^"]+)" nor "([^"]+)" declares a quant$`, st.eb1NoQuantEither)
			sc.Step(`^"([^"]+)" declares quant "([^"]+)" and no direct station serves "([^"]+)"$`, st.eb1QuantNoDirect)
			sc.Step(`^"([^"]+)" declares params_b ([0-9.]+) and "([^"]+)" declares params_b ([0-9.]+)$`, st.eb1Params)
			sc.Step(`^"([^"]+)" declares no params_b and "([^"]+)" declares params_b ([0-9.]+)$`, st.eb1NoParamsTower)
			sc.Step(`^"([^"]+)" declares ctx (\d+) and "([^"]+)" declares ctx (\d+)$`, st.eb1Ctxs)
			sc.Step(`^"([^"]+)"'s ctx (\d+) is an estimate and "([^"]+)" declares ctx (\d+)$`, st.eb1CtxEstimate)
			sc.Step(`^"([^"]+)" declares ctx (\d+) and no direct station serves "([^"]+)"$`, st.eb1CtxNoDirect)
			sc.Step(`^"([^"]+)" declares region "([^"]+)" and "([^"]+)" declares region "([^"]+)"$`, st.eb1Regions)
			sc.Step(`^"([^"]+)" declares no region and "([^"]+)" declares region "([^"]+)"$`, st.eb1NoRegionTower)
			sc.Step(`^"([^"]+)" is a curated \(commercial-proxy\) station and "([^"]+)" is a human station$`, st.eb1CuratedTowerHumanDirect)
			sc.Step(`^"([^"]+)" is curated and "([^"]+)" is a human station$`, st.eb1CuratedDirectHumanTower)
			sc.Step(`^both "([^"]+)" and "([^"]+)" are curated$`, st.eb1CuratedBoth)
			sc.Step(`^"([^"]+)" is curated$`, st.eb1CuratedOne)
			sc.Step(`^"([^"]+)" declares quant "([^"]+)" and params_b ([0-9.]+) but its known-model table entry says (\w+)$`, st.eb1DeclaresQuantParamsMismatch)

			// Given: trust
			sc.Step(`^the node behind "([^"]+)" is not verified and "([^"]+)" is verified$`, st.eb1TowerNotVerified)
			sc.Step(`^the node behind "([^"]+)" is verified and "([^"]+)" is not$`, st.eb1TowerVerified)
			sc.Step(`^"([^"]+)" is TEE-attested$`, st.eb1Attested)
			sc.Step(`^"([^"]+)" is not TEE-attested$`, st.eb1NotAttested)
			sc.Step(`^the node behind "([^"]+)" is Tier B and "([^"]+)" is Tier A$`, st.eb1TierBTower)

			// Given: bands, models, callers, towers
			sc.Step(`^a private band "([^"]+)" with station "([^"]+)" for "([^"]+)"$`, st.eb1Band)
			sc.Step(`^a private band "([^"]+)" with station "([^"]+)" for "([^"]+)" and "p1" is cooling for (\d+) more seconds$`, st.eb1BandCooling)
			sc.Step(`^"([^"]+)" serves "([^"]+)" only and "([^"]+)" hosts "([^"]+)" only, and "([^"]+)" 429s$`, st.eb1SplitModels429)
			sc.Step(`^no station or Tower serves "([^"]+)", "([^"]+)" serves "([^"]+)"$`, st.eb1NothingServes)
			sc.Step(`^"([^"]+)" hosts "([^"]+)" with quant "([^"]+)" and "([^"]+)" serves "([^"]+)" with quant "([^"]+)", "([^"]+)" 429s$`, st.eb1SplitQuants429)
			sc.Step(`^"([^"]+)" serves "([^"]+)" at out ([0-9.]+) and "([^"]+)" hosts "([^"]+)" at out ([0-9.]+)$`, st.eb1SplitPrices)
			sc.Step(`^the caller is (.+)$`, st.eb1CallerIs)
			sc.Step(`^a grant scoped to nodes (\[[^\]]*\])$`, st.eb1GrantScoped)
			sc.Step(`^the consumer owns "([^"]+)"$`, st.eb1ConsumerOwns)
			sc.Step(`^Towers "([^"]+)" \(node tps (\d+), out ([0-9.]+)\) and "([^"]+)" \(node tps (\d+), out ([0-9.]+)\) host "([^"]+)" and no direct station$`, st.eb1TwoTowersTpsPrice)
			sc.Step(`^the local proxy's previous attempt was served through "([^"]+)" and failed with a 5xx$`, st.eb1PrevAttemptFailed)
			sc.Step(`^a Tower station whose relay name is "([^"]+)"$`, st.eb1SpoofRelayName)
			sc.Step(`^a Tower "([^"]+)" whose station relay name is "([^"]+)" and the consumer ignores "([^"]+)"$`, st.eb1SpoofAndIgnore)
			sc.Step(`^a direct node with id "([^"]+)" and a Tower with id "([^"]+)" both serve "([^"]+)"$`, st.eb1IDCollision)
			sc.Step(`^(\d+) relays where both a direct station and the Tower row were eligible under balanced pref$`, st.eb1TenEligibleBalanced)
			sc.Step(`^(\d+) relays with provider\.sort "([^"]+)" where both were eligible$`, st.eb1TenEligibleSort)
			sc.Step(`^(\d+) relays where the Tower row failed min_tps and (\d+) where it failed require$`, st.eb1DeclineMix)

			// When
			sc.Step(`^(\d+) (?:consumers|such callers) relay (.+)$`, st.eb1WhenMany)
			sc.Step(`^a consumer relays$`, st.eb1WhenOneBare)
			sc.Step(`^a consumer relays (.+)$`, st.eb1WhenOne)
			sc.Step(`^the owner relays (.+)$`, st.eb1WhenOwner)
			sc.Step(`^the grant holder relays (.+)$`, st.eb1WhenGrant)
			sc.Step(`^an anonymous caller relays (.+)$`, st.eb1WhenAnon)
			sc.Step(`^the proxy retries (.+)$`, st.eb1WhenProxyRetries)
			sc.Step(`^the founder reads /admin/live$`, st.eb1ReadAdmin)

			// Then: who served
			sc.Step(`^every relay is served by "([^"]+)"$`, st.eb1EveryDirect)
			sc.Step(`^every relay is served by the direct station "([^"]+)"$`, st.eb1EveryDirectStation)
			sc.Step(`^every relay is served by "([^"]+)" if the grant covers it$`, st.eb1EveryDirectIfGrant)
			sc.Step(`^every relay is served by "([^"]+)" if free, else refused as today$`, st.eb1EveryDirectIfFree)
			sc.Step(`^every relay is served through "([^"]+)"$`, st.eb1EveryThrough)
			sc.Step(`^every relay is served by "([^"]+)" and the bridge was never entered$`, func(name string) error {
				if err := st.eb1EveryDirect(name); err != nil {
					return err
				}
				return st.eb1NeverEntered()
			})
			sc.Step(`^some relays ride "([^"]+)" and some "([^"]+)"$`, st.eb1SomeBoth)
			sc.Step(`^some are served by "([^"]+)" and some through "([^"]+)"$`, st.eb1SomeBothRev)
			sc.Step(`^some relays ride "([^"]+)" \(the cap admits it\)$`, st.eb1SomeRideCap)
			sc.Step(`^503 no_match$`, st.eb1EveryNoMatch)
			sc.Step(`^the bridge was never entered$`, st.eb1NeverEntered)
			sc.Step(`^"([^"]+)" received nothing$`, st.eb1ReceivedNothing)
			sc.Step(`^the relay is served through "([^"]+)"$`, st.eb1ServedThroughOne)
			sc.Step(`^the relay is served by "([^"]+)" with X-RogerAI-Model "([^"]+)"$`, st.eb1ServedByWithModel)
			sc.Step(`^the relay is served by "([^"]+)" at \$0$`, st.eb1ServedAtZero)
			sc.Step(`^the retry is served by "([^"]+)"$`, st.eb1RetryServedBy)
			sc.Step(`^relays may be served by either, and each is matched in its own namespace$`, st.eb1EitherNamespace)

			// Then: responses
			sc.Step(`^the response is 503 \{"error":\{"code":"no_match"\}\}$`, st.eb1RespNoMatch)
			sc.Step(`^the response is 503 \{"error":\{"code":"band_cooling"\}\} with Retry-After: (\d+)$`, st.eb1RespBandCooling)
			sc.Step(`^the response is 429 with Retry-After: (\d+)$`, st.eb1Resp429RA)
			sc.Step(`^the response is 429 with Retry-After$`, st.eb1Resp429AnyRA)
			sc.Step(`^the response is the last upstream error with Retry-After$`, st.eb1LastUpstreamErrRA)
			sc.Step(`^the response is 400 "request exceeds the context window" naming (\d+)$`, st.eb1RespCtx400)
			sc.Step(`^the consumer sees 200$`, st.eb1Sees200)
			sc.Step(`^the message says no confidential station matches$`, st.eb1ConfidentialMessage)
			sc.Step(`^X-RogerAI-Cost is 0 and there is no receipt$`, st.eb1CostZeroNoReceipt)
			sc.Step(`^the response body and X-RogerAI-Cost "([^"]*)" are identical to those for provider\.only (\[[^\]]*\])$`, st.eb1IdenticalToUnknown)

			// Then: attempts, hub, holds
			sc.Step(`^the bridge did not submit anything to "([^"]+)"'s hub$`, st.eb1HubNothing)
			sc.Step(`^nothing was submitted to "([^"]+)"'s hub$`, st.eb1HubNothing)
			sc.Step(`^no station received anything and no hold was placed$`, st.eb1NothingAnywhereNoHold)
			sc.Step(`^no hold was placed$`, st.eb1NoHold)
			sc.Step(`^no hold was placed and no operator is struck$`, st.eb1NoHoldNoStrike)
			sc.Step(`^the bridge tried "([^"]+)" only$`, st.eb1TriedOnly)
			sc.Step(`^the bridge tried "([^"]+)" once$`, st.eb1TriedOnce)
			sc.Step(`^the bridge tried "([^"]+)" once and the response is 429$`, st.eb1TriedOnceAnd429)
			sc.Step(`^the bridge tried "([^"]+)" once and fell back to "([^"]+)"$`, st.eb1TriedOnceFellBack)
			sc.Step(`^the bridge tried "([^"]+)" and the relay was served by "([^"]+)"$`, st.eb1TriedAndServedBy)
			sc.Step(`^exactly one attempt was made, to "([^"]+)"$`, st.eb1OneAttemptTo)
			sc.Step(`^attempt 1 hit "([^"]+)" for "([^"]+)" and attempt 2 rode "([^"]+)" for "([^"]+)"$`, st.eb1Attempt1Then2)
			sc.Step(`^attempt 1 hit "([^"]+)" and there is no attempt 2$`, st.eb1Attempt1NoSecond)
			sc.Step(`^relays served through "([^"]+)" carried a max_tokens no larger than what \$([0-9.]+) buys at ([0-9.]+)/1M after the input cost$`, st.eb1TowerCapBound)
			sc.Step(`^relays served by "([^"]+)" carried a max_tokens no larger than what \$([0-9.]+) buys at ([0-9.]+)/1M after the input cost$`, st.eb1DirectCapBound)
			sc.Step(`^every hold placed was \$([0-9.]+)$`, st.eb1EveryHoldWas)
			sc.Step(`^the hold covers ([0-9.]+) per 1M$`, st.eb1HoldCovers)
			sc.Step(`^on success at "([^"]+)" the consumer is charged at ([0-9.]+)$`, st.eb1ChargedAt)

			// Then: what the Tower sees and the consumer learns
			sc.Step(`^the plaintext sealed to "([^"]+)" has no "provider", "roger" or "models" key$`, st.eb1SealedNoCarriers)
			sc.Step(`^its "model" is the served model id$`, st.eb1SealedModel)
			sc.Step(`^it is otherwise byte-identical to what the consumer sent$`, st.eb1SealedOtherwiseIdentical)
			sc.Step(`^X-RogerAI-Model is "([^"]+)"$`, st.eb1ModelHeaderIs)
			sc.Step(`^X-RogerAI-Model is "([^"]+)" and X-RogerAI-Relay is "([^"]+)"$`, st.eb1ModelAndRelay)
			sc.Step(`^X-RogerAI-Provider names the relay and X-RogerAI-Relay names "([^"]+)"$`, st.eb1ProviderAndRelay)
			sc.Step(`^the receipt names model "([^"]+)", the relay, and Tower "([^"]+)"$`, st.eb1ReceiptNames)
			sc.Step(`^the receipt's model equals X-RogerAI-Model$`, st.eb1ReceiptModelEqualsHeader)
			sc.Step(`^the receipt's node signature verifies against "([^"]+)"'s relay public key$`, st.eb1ReceiptNodeSig)
			sc.Step(`^the receipt's broker signature verifies under VerifyBroker with the same key version scheme as a direct receipt$`, st.eb1ReceiptBrokerSig)
			sc.Step(`^the receipt does not verify against any direct station's key$`, st.eb1ReceiptNotDirectKey)
			sc.Step(`^the final usage chunk names model "([^"]+)" and relay "([^"]+)"$`, st.eb1UsageChunkNames)

			// Then: ranking, telemetry
			sc.Step(`^"([^"]+)" serves more often than under balanced$`, st.eb1MoreOftenThanBalanced)
			sc.Step(`^/admin/live shows edge_bridge_declined\{([a-z_]+)\} (\d+)$`, st.eb1DeclinedIs)
			sc.Step(`^it shows edge_bridge_declined\{([a-z_]+)\} (\d+) and edge_bridge_declined\{([a-z_]+)\} (\d+)$`, st.eb1DeclinedBoth)
			sc.Step(`^/admin/live edge_coin_flips did not change \(the coin is never consulted under a strict sort\)$`, st.eb1CoinUnchanged)
			sc.Step(`^edge_coin_flips increased by exactly (\d+)$`, st.eb1CoinIncreased)
			sc.Step(`^exactly one log line names the request, "([^"]+)", and "([^"]+)"$`, st.eb1OneDeclineLine)
			sc.Step(`^no log line contains a band code$`, st.eb1NoBandInLogs)
			sc.Step(`^the share served through "([^"]+)" is within the approved fan-out band of edge_fanout\.feature$`, st.eb1FanoutBand)
			sc.Step(`^the operator is warned once on /admin/live about the id collision$`, st.eb1CollisionWarned)
			sc.Step(`^the row is flagged params_estimated false with a mismatch on /admin/live$`, st.eb1FlaggedMismatch)
			sc.Step(`^the row is ineligible under the filter until the declaration is corrected$`, st.eb1RowIneligible)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/edge_bridge_parity.feature"},
			Tags:     "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing/edge_bridge_parity scenarios failed (see godog output above)")
	}
}
