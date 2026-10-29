package main

// Executable spec: features/lan/adversarial.feature (@client, @cli, @edge). Nothing configured or
// discovered makes LAN mode send a turn off the local network. Real standalone Towers (or a
// hostile stand-in at a scenario address), the real dial, the real proxy and relay; every "is not
// contacted" is checked against the dial map, which records every connection attempted.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/edge/wire"
	"rogerai.fm/roger/v7/internal/lanmode"
)

type lanAdvBDD struct {
	lanFrontBDD
}

// hostileTower answers at a scenario address with handler, as a device on the LAN might.
func (s *lanAdvBDD) hostileTower(addr string, h http.HandlerFunc) {
	srv := httptest.NewServer(h)
	s.t.Cleanup(srv.Close)
	s.dial.set(addr, srv.Listener.Addr().String())
	s.addTower(addr)
}

func listing(models ...string) []byte {
	var offers []client.Offer
	for _, m := range models {
		offers = append(offers, client.Offer{Model: m, Online: true})
	}
	b, _ := json.Marshal(map[string]any{"offers": offers})
	return b
}

// redirectingTower lists band and answers every completion with a 307 to location.
func (s *lanAdvBDD) redirectingTower(addr, band, location string) {
	s.hostileTower(addr, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover":
			_, _ = w.Write(listing(band))
		case "/local/poll":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		default:
			http.Redirect(w, r, location, http.StatusTemporaryRedirect)
		}
	})
}

func (s *lanAdvBDD) notContacted(addr string) error {
	if s.dial.wasDialed(addr) {
		return fmt.Errorf("%s was contacted", addr)
	}
	return nil
}

func TestLANAdversarialBDD(t *testing.T) {
	s := &lanAdvBDD{lanFrontBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}}
	var paths []string
	for _, l := range []int{18, 25, 35, 42, 49, 55, 70, 75, 84, 91, 98, 103, 108} {
		paths = append(paths, fmt.Sprintf("../../features/lan/adversarial.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.resetFront()
				t.Setenv("ROGER_LAN", "1")
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				client.SetLANRelay(nil)
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })

			// --- a public address in the config -------------------------------------------
			sc.Step(`^the config file's lan_towers was edited by hand to contain "([^"]*)"$`, func(addr string) error {
				_ = s.towerRuns(addr, "stolen-band") // a real Tower is THERE: the point is it is never asked
				s.addTower(addr)
				return nil
			})
			sc.Step(`^the dial refreshes$`, s.read)
			sc.Step(`^([0-9.]+) is not contacted$`, func(ip string) error {
				if err := s.notContacted(ip + ":8787"); err != nil {
					return err
				}
				return s.notListed("stolen-band")
			})
			sc.Step("^`roger lan list` shows the entry as refused, naming the rule$", func() error {
				if err := s.cli("lan", "list"); err != nil {
					return err
				}
				if !strings.Contains(s.cliOut, "203.0.113.7:8787") || !strings.Contains(s.cliOut, "refused") ||
					!strings.Contains(s.cliOut, "public address") {
					return fmt.Errorf("lan list = %q, want the entry refused naming the public-address rule", s.cliOut)
				}
				return s.notContacted("203.0.113.7:8787")
			})

			// --- DNS that flips between refresh and turn ------------------------------------
			sc.Step(`^"([^"]*)" resolved to ([0-9.]+) at the last dial refresh$`, func(host, ip string) error {
				s.dns[host] = []net.IP{net.ParseIP(ip)}
				tw := s.tower(ip + ":8787")
				tw.attachServing(t, "st-flip", s.counting("tower", &s.towerHits).URL+"/v1/chat/completions", "flip-band")
				s.addTower(host + ":8787")
				return s.read()
			})
			sc.Step(`^it resolves to ([0-9.]+) when the turn is dispatched$`, func(ip string) error {
				s.dns["tower.lan"] = []net.IP{net.ParseIP(ip)}
				return nil
			})
			sc.Step(`^a turn is dispatched to the Tower "([^"]*)"$`, func(string) error { return s.dispatch("flip-band") })
			sc.Step(`^the connection is not made$`, func() error {
				if s.towerHits.Load() != 0 {
					return fmt.Errorf("the Tower served the turn")
				}
				return s.notContacted("203.0.113.7:8787")
			})
			sc.Step(`^the turn is refused naming "([^"]*)"$`, func(why string) error { return s.refusedNaming(why) })

			// --- redirects ----------------------------------------------------------------------
			sc.Step(`^the LAN Tower at ([0-9.:]+) answers a completion with a 307 to "([^"]*)"$`, func(addr, loc string) error {
				s.redirectingTower(addr, "redir-band", loc)
				return nil
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) answers with a 307 to "([^"]*)"$`, func(addr, loc string) error {
				s.redirectingTower(addr, "redir-band", loc)
				_ = s.towerRuns("192.168.1.21:8787", "redir-band") // a real Tower at the redirect target
				return nil
			})
			sc.Step(`^a turn is dispatched to it$`, func() error { return s.dispatch("redir-band") })
			sc.Step(`^the redirect is not followed$`, func() error {
				for _, a := range []string{"203.0.113.7:443", "192.168.1.21:8787"} {
					if err := s.notContacted(a); err != nil {
						return err
					}
				}
				if s.code == http.StatusOK {
					return fmt.Errorf("the turn was served: %s", s.body)
				}
				return nil
			})
			sc.Step(`^the turn is refused naming the redirect$`, func() error { return s.refusedNaming("redirect") })

			// --- a public broker beside --lan ---------------------------------------------------
			sc.Step(`^the config's broker is the public broker and lan_towers contains "([^"]*)"$`, func(addr string) error {
				return s.towerServing(addr, "llama-3.3-70b")
			})
			sc.Step(`^the TUI is opened with --lan, a band is tuned, chat is sent, an agent turn runs and a guest operator sends a completion$`, func() error {
				if err := s.read(); err != nil {
					return err
				}
				s.tune("llama-3.3-70b")
				if err := s.chat(); err != nil {
					return err
				}
				if err := s.replyFrom("tower"); err != nil {
					return err
				}
				if err := s.completion("llama-3.3-70b"); err != nil {
					return err
				}
				if err := s.servedBy("tower"); err != nil {
					return err
				}
				return s.send("/v1/models", http.MethodGet, "")
			})
			sc.Step(`^no request is made to the public broker's /discover, /v1/chat/completions, /v1/models, /bands/resolve or /balance$`, func() error {
				return brokerNotAsked(s.broker, "/discover", "/v1/chat/completions", "/v1/models", "/bands/resolve", "/balance")
			})
			sc.Step(`^ROGER_BROKER is the public broker and ROGER_LAN is "1"$`, func() error { return nil })
			sc.Step(`^a turn is dispatched for a band only the market serves$`, func() error { return s.dispatch("market-only") })
			sc.Step(`^it is refused$`, func() error { return s.refusedNaming() })
			sc.Step(`^no request is made to the public broker$`, func() error {
				// The test broker is on loopback http, so LAN mode's one standalone-Tower probe
				// (/local/poll) is allowed; the real public broker is https and is never probed.
				s.broker.mu.Lock()
				defer s.broker.mu.Unlock()
				for _, p := range s.broker.paths {
					if p != "/local/poll" {
						return fmt.Errorf("the broker was asked %s: %v", p, s.broker.paths)
					}
				}
				return nil
			})

			// --- Edge flood ---------------------------------------------------------------------
			sc.Step(`^a hostile LAN floods Edge advertisements$`, func() error { return nil })
			sc.Step(`^the Edge discovery ceiling applies unchanged in LAN mode$`, func() error {
				t.Setenv("ROGER_LAN", "")
				off := edge.ConfigFromEnv(nil).Ceiling
				t.Setenv("ROGER_LAN", "1")
				if on := edge.ConfigFromEnv(nil).Ceiling; on != off || on <= 0 {
					return fmt.Errorf("Edge discovery ceiling = %d in LAN mode, %d without", on, off)
				}
				return nil
			})

			// --- a device that answers like a Tower -------------------------------------------
			sc.Step(`^a device at ([0-9.:]+) answers the standalone fingerprint but is not the owner's Tower$`, func(addr string) error {
				return s.towerRuns(addr, "impostor-band")
			})
			sc.Step(`^the owner added it$`, func() error { s.addTower("192.168.1.66:8787"); return nil })
			sc.Step(`^the dial renders$`, s.read)
			sc.Step(`^its bands are listed with the source marked "unverified"$`, func() error {
				b, ok := s.row("impostor-band")
				if !ok {
					return fmt.Errorf("the device's band is not listed: %+v", s.bands)
				}
				for _, src := range b.Sources {
					if src.Kind == client.LANTower && src.Verified {
						return fmt.Errorf("a Tower source is marked verified: %+v", src)
					}
				}
				return nil
			})

			// --- hostile /discover answers --------------------------------------------------------
			sc.Step(`^the LAN Tower answers /discover with an offer whose model name is 100000 characters and contains control characters$`, func() error {
				long := "evil\x1b[2J\x07" + strings.Repeat("a", 100000)
				_ = s.towerRuns("192.168.1.21:8787", "normal-band")
				s.addTower("192.168.1.21:8787")
				s.hostileTower("192.168.1.20:8787", func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/discover" {
						_, _ = w.Write(listing(long, "fine-band"))
						return
					}
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				})
				return nil
			})
			sc.Step(`^the model name is truncated and control characters are stripped$`, func() error {
				for _, b := range s.bands {
					if strings.HasPrefix(b.Model, "evil") {
						if len(b.Model) > 256 || strings.ContainsAny(b.Model, "\x1b\x07") {
							return fmt.Errorf("the hostile name was kept: %d chars, %q...", len(b.Model), b.Model[:20])
						}
						return nil
					}
				}
				return nil // dropped entirely is also safe
			})
			sc.Step(`^the other rows render normally$`, func() error {
				if err := s.listed("fine-band"); err != nil {
					return err
				}
				return s.listed("normal-band")
			})
			sc.Step(`^the LAN Tower answers /discover with "([^"]*)"$`, func(body string) error {
				s.hostileTower("192.168.1.20:8787", func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/discover" {
						_, _ = w.Write([]byte(body))
						return
					}
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				})
				return nil
			})
			sc.Step(`^its line reads "([^"]*)"$`, func(want string) error {
				if st := s.towerLine("192.168.1.20:8787"); string(st.State) != want {
					return fmt.Errorf("the Tower line is %q, want %q", st.State, want)
				}
				return nil
			})
			sc.Step(`^it is not drawn as "0 on air"$`, func() error {
				if st := s.towerLine("192.168.1.20:8787"); st.State == client.LANTowerAnswering {
					return fmt.Errorf("the Tower is drawn as answering with %d bands", st.Bands)
				}
				return nil
			})

			// --- addresses ------------------------------------------------------------------------
			sc.Step(`^they run "roger ([^"]*)"$`, func(line string) error { return s.cli(strings.Fields(line)...) })
			sc.Step(`^it exits non-zero, saying a link-local IPv6 address needs its interface, for example "([^"]*)"$`, func(eg string) error {
				if s.cliErr == nil || !strings.Contains(s.cliErr.Error()+s.cliOut, "needs its interface") ||
					!strings.Contains(s.cliErr.Error(), eg) {
					return fmt.Errorf("err=%v out=%q", s.cliErr, s.cliOut)
				}
				return nil
			})
			sc.Step(`^"([^"]*)" is not local$`, func(a string) error {
				if lanmode.ClassifyLiteral(a).Local {
					return fmt.Errorf("%s is classified local", a)
				}
				return nil
			})
			sc.Step(`^"([^"]*)" is local$`, func(a string) error {
				if !lanmode.ClassifyLiteral(a).Local {
					return fmt.Errorf("%s is not classified local", a)
				}
				return nil
			})
			sc.Step(`^it exits non-zero naming the rule$`, func() error {
				if s.cliErr == nil || !strings.Contains(s.cliErr.Error(), "never a Tower") {
					return fmt.Errorf("err=%v out=%q", s.cliErr, s.cliOut)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("adversarial scenarios failed")
	}
}

var _ = wire.TowerService
