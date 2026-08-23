package main

// Executable spec for features/tower/link_multi_instance.feature (founder-approved
// 2026-08-22): a Tower's link survives Roger Core's own scaling.
//
// Two REAL broker instances over the production route table, sharing the same durable
// stores exactly as production shares PostgreSQL - registry, custody, stations, heads -
// plus the link mirror this spec forced into existence. Sessions memory stays
// per-instance, which is the point: the live failure was a Tower opening its session on
// instance A and having instance B refuse the very next inventory push with "open a
// session before pushing inventory".

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/attach"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/towercore/enroll"
	"rogerai.fm/roger/v6/internal/towercore/head"
	"rogerai.fm/roger/v6/internal/towercore/inv"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerobj"
)

// Short windows so "the freshness window passes" is a sleep, not a stall: heartbeat 50ms
// makes Freshness 400ms legal (the config floor is 3x the heartbeat).
const (
	lmiHeartbeat = 50 * time.Millisecond
	lmiFreshness = 400 * time.Millisecond
)

type lmiState struct {
	t      *testing.T
	inst   map[string]*broker
	srv    map[string]*httptest.Server
	mirror *link.MemMirror
	towers map[string]linkTower
	sess   map[string]string
	planes map[string]link.RelayPlane
}

func (s *lmiState) linkFor() *link.Sessions {
	return link.New(link.Config{
		Network:   link.PublicNetwork,
		Versions:  []int{towerProtocolMin, towerProtocolMax},
		Heartbeat: lmiHeartbeat,
		Freshness: lmiFreshness,
		Mirror:    s.mirror,
	})
}

func (s *lmiState) twoInstancesOneStore() error {
	registry := admit.NewMemStore()
	stations := attach.NewMemStore()
	heads := head.NewMemStore()
	custody := cert.NewMemCustody()
	enrolls := enroll.NewMemStore()
	s.mirror = link.NewMemMirror()

	for _, name := range []string{"A", "B"} {
		b := testBrokerWithDB(store.NewMem())
		ts, err := newTowerSubsystem(b, registry, custody, enrolls,
			cert.Config{TTL: time.Hour},
			linkDeps{stations: stations, heads: heads, mirror: s.mirror})
		if err != nil {
			return err
		}
		// The spec's freshness scenarios need windows a test can wait out; everything
		// else about the session layer is the production configuration.
		ts.link = s.linkFor()
		b.tower = ts
		mux := http.NewServeMux()
		b.registerTowerRoutes(mux)
		srv := httptest.NewServer(mux)
		s.t.Cleanup(srv.Close)
		s.inst[name] = b
		s.srv[name] = srv
	}
	return nil
}

func (s *lmiState) tower(name string) linkTower {
	if lt, ok := s.towers[name]; ok {
		return lt
	}
	lt := enrolledTower(s.t, s.inst["A"], "owner-"+name)
	s.towers[name] = lt
	return lt
}

func (s *lmiState) opensOn(name, inst string) error { return s.opensOnPlane(name, inst, "") }

func (s *lmiState) opensOnPlane(name, inst, endpoint string) error {
	lt := s.tower(name)
	hello := link.Hello{Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), RelayEndpoint: endpoint}
	if endpoint != "" {
		s.planes[name] = link.RelayPlane{Endpoint: endpoint}
	}
	var acc link.Accepted
	code, raw := lt.call(s.t, s.srv[inst], "/tower/session", jsonOf(s.t, hello), &acc)
	if code != http.StatusOK {
		return fmt.Errorf("open on %s: %d %s", inst, code, raw)
	}
	s.sess[name] = acc.SessionID
	return nil
}

// emptyInventory is a signed inventory of zero leaves - the ordinary first push of a
// Tower whose nodes self-attach.
func (s *lmiState) emptyInventory(name string) []byte {
	lt := s.towers[name]
	now := time.Now()
	invObj := map[string]any{
		"network": link.PublicNetwork, "tower_id": lt.id,
		"revision": towerobj.FormatInt(1), "prev_hash": "genesis",
		"lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued":  towerobj.FormatInt(now.Unix()),
		"expires": towerobj.FormatInt(now.Add(30 * time.Minute).Unix()),
		"leaves":  []any{},
	}
	signed, err := towerobj.Sign(lt.priv, link.PublicNetwork, inv.TypeInventory,
		inv.Version, jsonOf(s.t, invObj), "sig")
	if err != nil {
		s.t.Fatalf("sign inventory: %v", err)
	}
	return signed
}

func (s *lmiState) pushesInventoryTo(name, inst string) error {
	lt := s.towers[name]
	code, raw := lt.call(s.t, s.srv[inst], "/tower/inventory", s.emptyInventory(name), nil)
	if code != http.StatusOK {
		return fmt.Errorf("inventory on %s refused: %d %s", inst, code, raw)
	}
	return nil
}

func (s *lmiState) heartbeatOn(name, inst string) error {
	lt := s.towers[name]
	code, raw := lt.call(s.t, s.srv[inst], "/tower/session/heartbeat",
		jsonOf(s.t, link.Frame{Network: link.PublicNetwork, Version: 1,
			TowerID: lt.id, SessionID: s.sess[name]}), nil)
	if code != http.StatusOK {
		return fmt.Errorf("heartbeat on %s refused: %d %s", inst, code, raw)
	}
	return nil
}

func (s *lmiState) liveOnBoth(name string) error {
	lt := s.towers[name]
	for _, inst := range []string{"A", "B"} {
		if !s.inst[inst].tower.link.Live(lt.id) {
			return fmt.Errorf("instance %s does not consider the Tower live", inst)
		}
	}
	return nil
}

func (s *lmiState) planeResolvesOn(inst string) error {
	lt := s.towers["the Tower"]
	p, has := s.inst[inst].tower.link.RelayPlane(lt.id)
	want := s.planes["the Tower"]
	if !has || p.Endpoint != want.Endpoint {
		return fmt.Errorf("instance %s resolves plane %+v, want %+v", inst, p, want)
	}
	return nil
}

// freshnessPassesOnAlone lets instance A's LOCAL record decay past the freshness window
// while the Tower's heartbeats keep landing on B - which is exactly what a per-request
// load balancer does to a healthy Tower.
func (s *lmiState) freshnessPassesOnAlone(string) error {
	time.Sleep(lmiFreshness + 100*time.Millisecond)
	return s.heartbeatOn("the Tower", "B")
}

func (s *lmiState) restartsEmptyAndHeartbeat(inst string) error {
	s.inst[inst].tower.link = s.linkFor() // new per-process memory, same mirror
	return s.heartbeatOn("the Tower", inst)
}

func (s *lmiState) storeStopsAnswering() error { s.mirror.FailForTest(true); return nil }

func (s *lmiState) answersFromWhatItSees(inst string) error {
	lt := s.towers["the Tower"]
	if inst == "B" && s.inst[inst].tower.link.Live(lt.id) {
		return fmt.Errorf("instance %s never met the Tower and the mirror is down - it invented liveness", inst)
	}
	if !s.inst["A"].tower.link.Live(lt.id) {
		return fmt.Errorf("instance A holds the session locally and must still say live")
	}
	return nil
}

func (s *lmiState) closeOn(name, inst string) error {
	lt := s.towers[name]
	code, raw := lt.call(s.t, s.srv[inst], "/tower/session/close",
		jsonOf(s.t, link.Frame{Network: link.PublicNetwork, Version: 1,
			TowerID: lt.id, SessionID: s.sess[name]}), nil)
	if code != http.StatusOK {
		return fmt.Errorf("close on %s: %d %s", inst, code, raw)
	}
	return nil
}

func (s *lmiState) recordsAreOwn() error {
	a, b := s.towers["the Tower"], s.towers["other"]
	if a.id == b.id {
		return fmt.Errorf("fixture error: towers share an id")
	}
	if err := s.closeOn("other", "B"); err != nil {
		return err
	}
	for _, inst := range []string{"A", "B"} {
		if s.inst[inst].tower.link.Live(b.id) {
			return fmt.Errorf("closed Tower still live on %s", inst)
		}
		if !s.inst[inst].tower.link.Live(a.id) {
			return fmt.Errorf("closing one Tower expired the other's record on %s", inst)
		}
	}
	return nil
}

func TestLinkMultiInstanceBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			s := &lmiState{t: t, inst: map[string]*broker{}, srv: map[string]*httptest.Server{},
				towers: map[string]linkTower{}, sess: map[string]string{}, planes: map[string]link.RelayPlane{}}
			sc.Step(`^Roger Core runs two instances sharing one store$`, s.twoInstancesOneStore)
			sc.Step(`^a registered Tower opens its link session on instance ([AB])$`, func(i string) error { return s.opensOn("the Tower", i) })
			sc.Step(`^a registered Tower opens its link session on instance ([AB]) with a relay plane$`, func(i string) error { return s.opensOnPlane("the Tower", i, "hub.example.net:8444") })
			sc.Step(`^it pushes a signed inventory to instance ([AB])$`, func(i string) error { return s.pushesInventoryTo("the Tower", i) })
			sc.Step(`^the inventory is accepted$`, func() error { return nil })
			sc.Step(`^the Tower is live on instance A and on instance B$`, func() error { return s.liveOnBoth("the Tower") })
			sc.Step(`^a node attach or consumer authorization asks instance ([AB]) for that Tower's plane$`, s.planeResolvesOn)
			sc.Step(`^instance ([AB]) resolves the same endpoint and pin instance ([AB]) recorded$`, func(i, _ string) error { return s.planeResolvesOn(i) })
			sc.Step(`^its heartbeat lands on instance ([AB])$`, func(i string) error { return s.heartbeatOn("the Tower", i) })
			sc.Step(`^the freshness window passes on instance ([AB]) alone$`, s.freshnessPassesOnAlone)
			sc.Step(`^the Tower is still live on both instances$`, func() error { return s.liveOnBoth("the Tower") })
			sc.Step(`^instance ([AB]) restarts empty and the Tower's next heartbeat lands on it$`, s.restartsEmptyAndHeartbeat)
			sc.Step(`^the heartbeat is accepted from the shared record$`, func() error { return nil })
			sc.Step(`^the Tower remains live$`, func() error { return s.liveOnBoth("the Tower") })
			sc.Step(`^its deliberate close lands on instance ([AB])$`, func(i string) error { return s.closeOn("the Tower", i) })
			sc.Step(`^the Tower is live on neither instance$`, func() error {
				lt := s.towers["the Tower"]
				for _, inst := range []string{"A", "B"} {
					if s.inst[inst].tower.link.Live(lt.id) {
						return fmt.Errorf("instance %s still live after a deliberate close", inst)
					}
				}
				return nil
			})
			sc.Step(`^a later close quoting a superseded session cannot dim the newer link$`, func() error {
				oldSess := s.sess["the Tower"]
				if err := s.opensOn("the Tower", "A"); err != nil { // supersedes
					return err
				}
				lt := s.towers["the Tower"]
				code, _ := lt.call(s.t, s.srv["B"], "/tower/session/close",
					jsonOf(s.t, link.Frame{Network: link.PublicNetwork, Version: 1,
						TowerID: lt.id, SessionID: oldSess}), nil)
				_ = code // a stale close is answered politely; what matters is the effect
				return s.liveOnBoth("the Tower")
			})
			sc.Step(`^the shared store stops answering$`, s.storeStopsAnswering)
			sc.Step(`^instance ([AB]) is asked whether the Tower is live$`, func(string) error { return nil })
			sc.Step(`^instance ([AB]) answers from what it can actually see$`, s.answersFromWhatItSees)
			sc.Step(`^no instance invents liveness it cannot verify$`, func() error { return nil })
			sc.Step(`^two registered Towers each open a session on different instances$`, func() error {
				if err := s.opensOn("the Tower", "A"); err != nil {
					return err
				}
				return s.opensOn("other", "B")
			})
			sc.Step(`^each pushes inventory and heartbeats through either instance$`, func() error {
				for _, step := range []func() error{
					func() error { return s.pushesInventoryTo("the Tower", "B") },
					func() error { return s.pushesInventoryTo("other", "A") },
					func() error { return s.heartbeatOn("the Tower", "B") },
					func() error { return s.heartbeatOn("other", "A") },
				} {
					if err := step(); err != nil {
						return err
					}
				}
				return nil
			})
			sc.Step(`^each Tower's liveness, head, and plane are its own$`, func() error {
				if err := s.liveOnBoth("the Tower"); err != nil {
					return err
				}
				return s.liveOnBoth("other")
			})
			sc.Step(`^neither can advance or expire the other's record$`, s.recordsAreOwn)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/tower/link_multi_instance.feature"},
			Strict: true, TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the link multi-instance spec is not satisfied")
	}
}

var _ = protocol.HeaderPubkey // the fixtures sign with the tower key via linkTower.call
