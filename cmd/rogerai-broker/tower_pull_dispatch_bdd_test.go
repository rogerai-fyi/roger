package main

// Executable spec for features/tower/tower_pull_dispatch.feature (founder approved 2026-11-14):
// a Tower pulls its work over the link it already holds.
//
// Two REAL broker instances over the production route table share every durable store plus one
// Valkey (miniredis), exactly as production shares PostgreSQL and Valkey. The Tower is played by
// this runner with the production pieces it runs: a real towerhub.Server for its hub, a real
// `roger share` node (agent.ServeTower) attached to it, and towerhub.ServePulled - the function
// `roger tower serve` runs every pulled request through - driven by signed pulls and answers on
// the real routes. The serve, status and documentation scenarios run in internal/towercli.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/cert"
	"rogerai.fm/roger/v7/internal/towercore/dispatch"
	"rogerai.fm/roger/v7/internal/towercore/enroll"
	"rogerai.fm/roger/v7/internal/towercore/fleet"
	"rogerai.fm/roger/v7/internal/towercore/head"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/mobility"
	"rogerai.fm/roger/v7/internal/towercore/placement"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
	"rogerai.fm/roger/v7/internal/towerhub"
)

const tpdModel = "pull-model"

// tpdPull is one pull's outcome.
type tpdPull struct {
	code int
	hdr  http.Header
	work []towerhub.PullWork
	err  error
}

type tpdState struct {
	t      *testing.T
	mr     *miniredis.Miniredis
	vs     *valkeyStore
	inst   map[string]*broker
	srv    map[string]*httptest.Server
	mirror *link.MemMirror
	health health.Store

	// the Tower under test, its live hub and its Station
	lt         linkTower
	sess       string
	endpoint   string
	hubServer  *towerhub.Server
	hubMux     *http.ServeMux
	hubLn      net.Listener
	netSubmits atomic.Int64 // submits that arrived on the hub's listener (a dial)
	netPolls   atomic.Int64 // identity checks (a nonce-only poll) that arrived on the listener
	cancelWait func()       // ends the request submitVia is waiting on
	hubDownFn  func()       // takes the hub's listener down, open connections included
	pulled     atomic.Int64 // submits that arrived by pull
	upCode     atomic.Int64 // the Station's upstream status; 0 = a real completion
	stationID  string
	consumer   ed25519.PrivateKey
	closers    []func()

	// the runner's stand-in for `roger tower serve`'s pull loop
	autoStop chan struct{}
	autoDone sync.WaitGroup
	autoInst string
	hold     atomic.Bool // answers held back (a Tower slow to answer)
	heldMu   sync.Mutex
	heldWork []towerhub.PullWork

	// a second Tower, for the scenarios about two
	other     linkTower
	otherSess string

	// per scenario
	pulls     []chan tpdPull
	lastPull  tpdPull
	rec       *httptest.ResponseRecorder
	work      towerhub.PullWork
	ansCode   int
	ansBody   string
	balBefore float64
	failedBef int
	gotResp   chan tpdResp
	outcome   reputation.Outcome
	firstAns  towerhub.PullAnswer
}

type tpdResp struct {
	resp *http.Response
	err  error
	body []byte
}

// --- the two instances ------------------------------------------------------------------

func (s *tpdState) twoInstances() error {
	s.mr = miniredis.RunT(s.t)
	vs, err := newValkeyStore("redis://" + s.mr.Addr())
	if err != nil {
		return err
	}
	s.vs = vs
	s.closers = append(s.closers, func() { _ = vs.Close() })
	registry, custody, enrolls := admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore()
	stations, heads := attach.NewMemStore(), head.NewMemStore()
	s.mirror = link.NewMemMirror()
	s.health = health.NewMemStore()
	mob := mobility.NewMemStore()
	routable := fleet.NewMemStore()
	db := store.NewMem()
	s.inst, s.srv = map[string]*broker{}, map[string]*httptest.Server{}
	for _, name := range []string{"A", "B"} {
		b := testBrokerWithDB(db)
		b.shared = vs
		ts, err := newTowerSubsystem(b, registry, custody, enrolls, cert.Config{TTL: time.Hour},
			linkDeps{stations: stations, heads: heads, mirror: s.mirror, health: s.health, mobility: mob, routable: routable})
		if err != nil {
			return err
		}
		b.tower = ts
		mux := http.NewServeMux()
		b.registerTowerRoutes(mux)
		srv := httptest.NewServer(mux)
		s.closers = append(s.closers, srv.Close)
		s.inst[name], s.srv[name] = b, srv
	}
	return nil
}

func (s *tpdState) a() *broker { return s.inst["A"] }

// openSession (re)opens the Tower's link on inst, with or without the pull capability.
func (s *tpdState) openSession(lt linkTower, inst string, pull bool, endpoint string) (string, error) {
	caps := mandatoryCaps()
	if pull {
		caps = append(caps, link.CapPull)
	}
	var acc link.Accepted
	code, raw := s.signedPost(lt, inst, "/tower/session", jsonOf(s.t, link.Hello{Network: link.PublicNetwork,
		Versions: []int{1}, TowerID: lt.id, Capabilities: caps, RelayEndpoint: endpoint}), &acc)
	if code != http.StatusOK {
		return "", fmt.Errorf("session open answered %d: %s", code, raw)
	}
	// Core classes the hub off the request path; wait until it has.
	deadline := time.Now().Add(5 * time.Second)
	for endpoint != "" && s.inst[inst].tower.link.HubClass(lt.id) == placement.HubNone {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the hub was never classed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return acc.SessionID, nil
}

// signedPost is the Tower's signed request, safe from any goroutine (no t.Fatal).
func (s *tpdState) signedPost(lt linkTower, inst, path string, body []byte, out any) (int, string) {
	return signedPostTo(lt, s.srv[inst].URL, path, body, out, 40*time.Second)
}

func signedPostTo(lt linkTower, base, path string, body []byte, out any, timeout time.Duration) (int, string) {
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(lt.priv, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if out != nil && len(raw) > 0 && resp.StatusCode < 300 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, string(raw)
}

// --- the live Tower ------------------------------------------------------------------------

// liveTower stands up the Background's Tower on instance A: an approved Tower whose session
// carries the pull capability, its real hub, and a real share node attached and serving.
func (s *tpdState) liveTower() error {
	b, srv := s.a(), s.srv["A"]
	t := s.t
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if c := s.upCode.Load(); c != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(int(c))
			fmt.Fprint(w, `{"error":{"message":"rate limited upstream"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong by pull"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
	s.closers = append(s.closers, upstream.Close)
	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.closers = append(s.closers, func() { _ = hubLn.Close() })
	s.hubLn = hubLn
	s.endpoint = hubLn.Addr().String()
	op := signedInOperator(t, b, "pull-op")
	s.lt = enrolledTower(t, b, op.login)
	if err := b.tower.registry.Transition(s.lt.id, admit.StateActive); err != nil {
		return err
	}
	setStage(t, b, s.lt.id, stageEstablished)
	s.sess, err = s.openSession(s.lt, "A", true, s.endpoint)
	if err != nil {
		return err
	}
	lt := s.lt
	s.hubServer = towerhub.NewServer(towerhub.New(), func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, lt.id, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv, SubmitTTL: 10 * time.Second, PollTTL: 500 * time.Millisecond})
	s.hubMux = http.NewServeMux()
	s.hubMux.HandleFunc(towerhub.PathSubmit, func(w http.ResponseWriter, r *http.Request) {
		if r.RemoteAddr == "pulled" {
			s.pulled.Add(1)
		} else {
			s.netSubmits.Add(1)
		}
		s.hubServer.Submit(w, r)
	})
	s.hubMux.HandleFunc(towerhub.PathPoll, func(w http.ResponseWriter, r *http.Request) {
		if r.RemoteAddr != "pulled" && r.URL.Query().Get("station") == "" {
			s.netPolls.Add(1)
		}
		s.hubServer.Poll(w, r)
	})
	s.hubMux.HandleFunc(towerhub.PathComplete, s.hubServer.Complete)
	hubSrv := &http.Server{Handler: s.hubMux}
	s.hubDownFn = func() { _ = hubSrv.Close() } // the listener and every open connection
	go func() { _ = hubSrv.Serve(hubLn) }()

	nodeOp := signedInOperator(t, b, "pull-node-op")
	shareNodeID := registerShareNode(t, b, nodeOp)
	ctx, cancel := context.WithCancel(context.Background())
	s.closers = append(s.closers, cancel)
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{NodeID: shareNodeID, Broker: srv.URL, Model: tpdModel,
			Modality: "chat", Upstream: upstream.URL, Parallel: 1}, nodeOp.priv, t.TempDir(), io.Discard, nil)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		ats, aerr := b.tower.stations.ByTower(s.lt.id)
		if aerr == nil && len(ats) > 0 {
			s.hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
			s.stationID = ats[0].StationID
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the share node never attached")
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.consumer = signedInConsumer(t, b)
	s.startAuto("A")
	return nil
}

// startAuto runs the runner's pull loop against inst: two held pulls, each piece of work run
// through towerhub.ServePulled against the real hub, the answer posted back.
func (s *tpdState) startAuto(inst string) {
	s.stopAuto()
	s.autoStop, s.autoInst = make(chan struct{}), inst
	stop := s.autoStop
	for i := 0; i < 2; i++ {
		s.autoDone.Add(1)
		go func() {
			defer s.autoDone.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p := s.pullNow(inst, s.lt, s.sess)
				if p.code != http.StatusOK {
					select {
					case <-stop:
						return
					case <-time.After(20 * time.Millisecond):
					}
					continue
				}
				for _, w := range p.work {
					go s.serve(inst, w)
				}
			}
		}()
	}
}

func (s *tpdState) stopAuto() {
	if s.autoStop != nil {
		close(s.autoStop)
		s.autoStop = nil
		s.autoDone.Wait()
	}
}

// serve runs one pulled request against the hub and posts the answer, unless answers are held.
func (s *tpdState) serve(inst string, w towerhub.PullWork) {
	ans := towerhub.ServePulled(context.Background(), s.hubMux, w, 32<<20)
	if s.hold.Load() {
		s.heldMu.Lock()
		s.heldWork = append(s.heldWork, w)
		s.heldMu.Unlock()
		return
	}
	s.postAnswer(inst, s.lt, s.sess, ans)
}

func (s *tpdState) postAnswer(inst string, lt linkTower, sess string, a towerhub.PullAnswer) (int, string) {
	a.TowerID, a.SessionID = lt.id, sess
	return s.signedPost(lt, inst, "/tower/work/answer", jsonOf(s.t, a), nil)
}

// pullNow is one signed pull, held until Core answers.
func (s *tpdState) pullNow(inst string, lt linkTower, sess string) tpdPull {
	body := jsonOf(s.t, map[string]string{"tower_id": lt.id, "session_id": sess})
	req, _ := http.NewRequest(http.MethodPost, s.srv[inst].URL+"/tower/work/pull", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(lt.priv, http.MethodPost, "/tower/work/pull", body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		return tpdPull{err: err}
	}
	defer resp.Body.Close()
	out := tpdPull{code: resp.StatusCode, hdr: resp.Header}
	if resp.StatusCode == http.StatusOK {
		var v struct {
			Work []towerhub.PullWork `json:"work"`
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, &v)
		out.work = v.Work
	}
	return out
}

// pullAsync starts a held pull and returns where its outcome lands.
func (s *tpdState) pullAsync(inst string, lt linkTower, sess string) chan tpdPull {
	ch := make(chan tpdPull, 1)
	go func() { ch <- s.pullNow(inst, lt, sess) }()
	return ch
}

// waitHeld waits until n pulls are held for the Tower in the shared store.
func (s *tpdState) waitHeld(towerID string, n int) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s.mr.Exists(edgeSlotsPrefix + "towerpull:" + towerID) {
			members, _ := s.mr.ZMembers(edgeSlotsPrefix + "towerpull:" + towerID)
			if len(members) >= n {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("never saw %d held pulls", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// relay sends one consumer request through the ordinary endpoint on instance A.
func (s *tpdState) relay() *httptest.ResponseRecorder {
	body := `{"model":"` + tpdModel + `","messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(s.consumer, http.MethodPost, "/v1/chat/completions", []byte(body))
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	rec := httptest.NewRecorder()
	s.a().relay(rec, req)
	return rec
}

func (s *tpdState) reset() error {
	s.stopAuto()
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
	*s = tpdState{t: s.t}
	return nil
}
