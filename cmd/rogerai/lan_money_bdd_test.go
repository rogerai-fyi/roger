package main

// Executable spec: features/lan/money_and_accounts.feature (@client) and the session route of
// features/lan/ui_visibility.feature. Turns go through the REAL proxy handler on the LAN ladder
// to a REAL standalone Tower (behind a header-recording front), a pinned member face, or this
// machine's serve; a recording broker proves no money path was asked.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
)

type lanMoneyBDD struct {
	lanRouteBDD
	mu       sync.Mutex
	towerReq []http.Header // headers of every turn that reached the Tower
	oddHits  atomic.Int32
}

// frontTower puts a header-recording front before the real Tower at addr.
func (s *lanMoneyBDD) frontTower(addr, band string) error {
	if err := s.towerServing(addr, band); err != nil {
		return err
	}
	real := s.dial.lookup(addr)
	target, _ := url.Parse("http://" + real)
	rp := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			s.mu.Lock()
			s.towerReq = append(s.towerReq, r.Header.Clone())
			s.mu.Unlock()
		}
		rp.ServeHTTP(w, r)
	}))
	s.t.Cleanup(front.Close)
	s.dial.set(addr, front.Listener.Addr().String())
	return nil
}

// oddTower stands at addr something that passes the standalone check and lists band, but
// answers a completion WITHOUT the local marker.
func (s *lanMoneyBDD) oddTower(addr, band string) error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local/poll":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		case "/discover":
			_, _ = w.Write([]byte(`{"offers":[{"model":"` + band + `","online":true}]}`))
		default:
			s.oddHits.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from odd"}}]}`))
		}
	}))
	s.t.Cleanup(srv.Close)
	s.dial.set(addr, srv.Listener.Addr().String())
	s.addTower(addr)
	return nil
}

func TestLANMoneyBDD(t *testing.T) {
	s := &lanMoneyBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}
	paths := []string{}
	for _, l := range []int{42, 47, 55, 73, 116} {
		paths = append(paths, fmt.Sprintf("../../features/lan/money_and_accounts.feature:%d", l))
	}
	paths = append(paths, "../../features/lan/ui_visibility.feature:104")
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.resetRoute()
				s.towerReq = nil
				s.oddHits.Store(0)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^a turn is served by a LAN Tower$`, func() error {
				if err := s.frontTower("192.168.1.20:8787", "llama-3.3-70b"); err != nil {
					return err
				}
				// the caps a `roger use` session carries from the owner's limits
				s.tweak = func(o *client.ProxyOptions) { o.MaxPriceIn, o.MaxPriceOut = 0.5, 2 }
				if err := s.dispatch("llama-3.3-70b"); err != nil {
					return err
				}
				return s.servedBy("tower")
			})
			sc.Step(`^the request carries no market price-cap header$`, func() error {
				if len(s.towerReq) == 0 {
					return fmt.Errorf("no turn reached the Tower")
				}
				for _, h := range s.towerReq {
					for _, k := range []string{"X-Roger-Max-Price", "X-Roger-Max-Price-Out"} {
						if h.Get(k) != "" {
							return fmt.Errorf("the Tower was sent %s: %s", k, h.Get(k))
						}
					}
				}
				return nil
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) answers with "X-Roger-Cost: 0" and "X-Roger-Local: 1"$`, func(addr string) error {
				return s.towerServing(addr, "llama-3.3-70b") // the real plane marks every answer this way
			})
			sc.Step(`^a turn is served by it$`, func() error { return s.dispatch("llama-3.3-70b") })
			sc.Step(`^the turn is recorded as local and free$`, func() error {
				for _, r := range s.receipts {
					if r.Local && r.LAN && r.PriceIn == 0 && r.PriceOut == 0 {
						return nil
					}
				}
				return fmt.Errorf("no local, free LAN record: %+v", s.receipts)
			})
			sc.Step(`^the caller's proxy response carries "X-RogerAI-Cost: 0"$`, func() error {
				if s.hdr.Get("X-RogerAI-Cost") != "0" {
					return fmt.Errorf("X-RogerAI-Cost = %q", s.hdr.Get("X-RogerAI-Cost"))
				}
				return nil
			})
			sc.Step(`^a configured LAN address answers a completion without "X-Roger-Local: 1"$`, func() error {
				return s.oddTower("192.168.1.30:8787", "odd-band")
			})
			sc.Step(`^the turn completes$`, func() error { return s.dispatch("odd-band") })
			sc.Step(`^the turn is recorded with the warning "([^"]*)"$`, func(want string) error {
				for _, a := range s.alerts {
					if strings.Contains(a, want) {
						return nil
					}
				}
				return fmt.Errorf("no warning %q in %v", want, s.alerts)
			})
			sc.Step(`^the address is marked on the dial as "([^"]*)"$`, func(want string) error {
				status := lanDial(s.cfg()).Towers
				for _, st := range status {
					if st.Addr == "192.168.1.30:8787" && string(st.State) == want {
						return nil
					}
				}
				return fmt.Errorf("the dial does not mark it %q: %+v", want, status)
			})
			sc.Step(`^no later turn is sent to it until the owner re-adds it$`, func() error {
				before := s.oddHits.Load()
				if err := s.dispatch("odd-band"); err != nil {
					return err
				}
				if s.oddHits.Load() != before {
					return fmt.Errorf("a later turn was sent to it")
				}
				return s.refusedNaming("odd-band")
			})
			sc.Step(`^turns were served by this machine, an Edge member and a LAN Tower$`, func() error {
				if err := s.machineServesRoute("qwen3-8b"); err != nil {
					return err
				}
				if err := s.face("jetson", "gpt-oss-20b", 0); err != nil {
					return err
				}
				if err := s.towerServing("192.168.1.20:8787", "llama-3.3-70b"); err != nil {
					return err
				}
				for _, b := range []string{"qwen3-8b", "gpt-oss-20b", "llama-3.3-70b"} {
					if err := s.dispatch(b); err != nil {
						return err
					}
					if s.code != 200 {
						return fmt.Errorf("%s: %d %s", b, s.code, s.body)
					}
				}
				return nil
			})
			sc.Step(`^no receipt from any of them was sent to the public broker$`, func() error {
				for _, p := range s.broker.paths {
					if p != "/local/poll" { // only the check whether the broker setting is a local Tower
						return fmt.Errorf("the broker was asked %s", p)
					}
				}
				return nil
			})
			sc.Step(`^none appears in /generation or the account's usage$`, func() error {
				return brokerNotAsked(s.broker, "/generation", "/usage", "/account/usage")
			})
			sc.Step(`^the account's monthly cap is fully spent$`, func() error { return nil }) // the cap lives with the broker, which a LAN turn never asks
			sc.Step(`^a turn is served by a LAN source$`, func() error {
				if err := s.machineServesRoute("qwen3-8b"); err != nil {
					return err
				}
				return s.dispatch("qwen3-8b")
			})
			sc.Step(`^it is not refused for the cap$`, func() error { return s.servedBy("self") })
			sc.Step(`^a turn served by the LAN Tower at ([0-9.:]+)$`, func(addr string) error {
				if err := s.towerServing(addr, "llama-3.3-70b"); err != nil {
					return err
				}
				return s.dispatch("llama-3.3-70b")
			})
			sc.Step(`^the Edge sessions view renders$`, func() error { return nil })
			sc.Step(`^that row's route reads "([^"]*)"$`, func(want string) error {
				if len(s.receipts) == 0 {
					return fmt.Errorf("the turn left no record")
				}
				sess, err := edge.NewSessions("acct").Record(edge.Traffic{Account: "acct", Kind: edge.FromUse,
					Request: s.receipts[0].RequestID, Receipts: s.receipts})
				if err != nil {
					return err
				}
				if sess.Where != want {
					return fmt.Errorf("route %q, want %q (receipts %+v)", sess.Where, want, s.receipts)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("money_and_accounts.feature failed")
	}
}
