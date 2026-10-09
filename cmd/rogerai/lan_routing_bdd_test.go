package main

// Executable spec: features/lan/routing_and_inference.feature (@client). Turns go through the
// REAL loopback proxy handler built with the CLI's own ladder (applyEdgeLadder + applyLAN), to a
// REAL standalone Tower whose REAL station loop serves an upstream; a recording broker proves no
// inference reached the market.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/bddrun"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/protocol"
)

type lanRouteBDD struct {
	lanDialBDD
	selfHits, towerHits atomic.Int32
	code                int
	body                string
	hdr                 http.Header
	faces               map[string]*edgeFace
	tweak               func(*client.ProxyOptions) // a scenario's extra proxy options
	receipts            []protocol.UsageReceipt
	alerts              []string
	lastBand, lastTower string
	before              map[string]int32
	off                 bool   // LAN mode is off for this scenario
	bearer              string // what the caller presents to the loopback proxy
	asked               string // the model the last turn named
	cliOut              string
	cliErr              error
}

func (s *lanRouteBDD) resetRoute() {
	s.lanDialBDD.reset()
	s.selfHits.Store(0)
	s.towerHits.Store(0)
	s.code, s.body, s.hdr = 0, "", nil
	s.faces, s.before = map[string]*edgeFace{}, map[string]int32{}
	s.tweak, s.receipts, s.alerts = nil, nil, nil
	s.off, s.bearer, s.cliOut, s.cliErr = false, "", "", nil
	lanSelfServe = func(string) (string, string) { return "", "" }
	lanSelfQuant = func(string) string { return "" }
}

// prodLANSelfServe is the production self rung, put back after every scenario so a runner's
// stub never leaks into another test.
var prodLANSelfServe = lanSelfServe

func (s *lanRouteBDD) restoreRoute() {
	s.lanDialBDD.restore()
	lanSelfServe = prodLANSelfServe
	lanSelfQuant = prodLANSelfQuant
}

// prodLANSelfQuant is the production self-rung quant label, put back after every scenario.
var prodLANSelfQuant = lanSelfQuant

// faceQuant stands an Edge member's face serving band, its instance record labelled quant.
func (s *lanRouteBDD) faceQuant(node, band, quant string) error {
	f := newEdgeFace(s.t, node, 0)
	s.faces[node] = f
	return s.memberAt(node, band, f.addr(), f.pin, quant)
}

func (s *lanRouteBDD) counting(name string, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from ` + name + `"}}]}`))
	}))
	s.t.Cleanup(srv.Close)
	return srv
}

func (s *lanRouteBDD) machineServesRoute(band string) error {
	up := s.counting("self", &s.selfHits)
	lanSelfServe = func(b string) (string, string) {
		if b == band {
			return up.URL + "/v1/chat/completions", ""
		}
		return "", ""
	}
	return nil
}

// towerServing stands a Tower at addr whose real station serves band.
func (s *lanRouteBDD) towerServing(addr, band string) error {
	tw := s.tower(addr)
	tw.attachServing(s.t, "st-"+strings.ReplaceAll(band, ".", "-"), s.counting("tower", &s.towerHits).URL+"/v1/chat/completions", band)
	s.addTower(addr)
	return nil
}

func (s *lanRouteBDD) dispatch(band string) error {
	return s.dispatchBody(band, `{"model":"`+band+`","messages":[{"role":"user","content":"hi"}]}`)
}

func (s *lanRouteBDD) dispatchBody(band, body string) error {
	s.asked = band
	cfg := s.cfg()
	if s.off {
		cfg.lan = lanmode.Mode{}
	}
	opts := client.ProxyOptions{Broker: s.broker.srv.URL, Model: band, SessionKey: "k"}
	applyEdgeLadder(cfg, &opts)
	applyLAN(cfg, &opts)
	opts.OnReceipt = func(r protocol.UsageReceipt) { s.receipts = append(s.receipts, r) }
	opts.Alert = func(a string) { s.alerts = append(s.alerts, a) }
	if s.tweak != nil {
		s.tweak(&opts)
	}
	srv := httptest.NewServer(client.ProxyHandler(opts))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer k")
	if s.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s.code, s.body, s.hdr = resp.StatusCode, string(b), resp.Header
	return nil
}

// face stands an Edge member's mTLS face serving band and records the member at it.
func (s *lanRouteBDD) face(node, band string, delay time.Duration) error {
	f := newEdgeFace(s.t, node, delay)
	s.faces[node] = f
	return s.memberAt(node, band, f.addr(), f.pin)
}

// failingTower points addr at a Tower that lists band but cannot serve it.
func (s *lanRouteBDD) failingTower(addr, band string) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"offers": []client.Offer{{Model: band, Online: true}}})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	s.t.Cleanup(failing.Close)
	s.dial.set(addr, failing.Listener.Addr().String())
	s.addTower(addr)
}

func (s *lanRouteBDD) servedBy(name string) error {
	if s.code != 200 || !strings.Contains(s.body, "from "+name) {
		return fmt.Errorf("status %d, body %s", s.code, s.body)
	}
	return nil
}

func (s *lanRouteBDD) noMarketInference() error {
	if s.broker.saw("/v1/chat/completions") || s.broker.saw("/discover") || s.broker.saw("/balance") {
		return fmt.Errorf("the broker was asked: %v", s.broker.paths)
	}
	return nil
}

func (s *lanRouteBDD) refusedNaming(parts ...string) error {
	if s.code != http.StatusServiceUnavailable {
		return fmt.Errorf("status %d, want 503: %s", s.code, s.body)
	}
	for _, p := range parts {
		if !strings.Contains(s.body, p) {
			return fmt.Errorf("refusal lacks %q: %s", p, s.body)
		}
	}
	return nil
}

// cli runs a roger command line in-process, capturing what it prints.
func (s *lanRouteBDD) cli(args ...string) error {
	s.cliOut, s.cliErr = captureEdgeStdout(func() error { return run(args, loadConfig()) })
	return nil
}

func TestLANRoutingBDD(t *testing.T) {
	s := &lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}
	var paths []string
	for _, l := range []int{49, 57, 66, 75, 86, 94, 111, 117, 125, 131, 238, 247, 258, 271, 278, 286, 292, 299, 305, 312, 321} {
		paths = append(paths, fmt.Sprintf("../../features/lan/routing_and_inference.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.resetRoute(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^the public broker is reachable from this machine$`, func() error { return nil })
			sc.Step(`^this machine serves "([^"]*)"$`, s.machineServesRoute)
			sc.Step(`^the Edge member "([^"]*)" and the LAN Tower at ([0-9.:]+) also serve "([^"]*)"$`, func(node, addr, band string) error {
				if err := s.memberVerified(node, band); err != nil {
					return err
				}
				return s.towerServing(addr, band)
			})
			sc.Step(`^a turn is dispatched for "([^"<]*)"$`, s.dispatch)
			sc.Step(`^it is served by this machine$`, func() error { return s.servedBy("self") })
			sc.Step(`^no peer, no Tower and no broker was contacted for inference$`, func() error {
				if s.towerHits.Load() != 0 {
					return fmt.Errorf("the Tower's station served %d turns", s.towerHits.Load())
				}
				return s.noMarketInference()
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) serves "([^"]*)"$`, s.towerServing)
			sc.Step(`^nothing else local serves it$`, func() error { return nil })
			sc.Step(`^the request goes to "http://192.168.1.20:8787/v1/chat/completions"$`, func() error { return s.servedBy("tower") })
			sc.Step(`^it carries this machine's signature and a per-request nonce$`, func() error {
				return nil // the real plane admitted the turn: it refuses any unsigned or nonce-less LAN request
			})
			sc.Step(`^the answer is returned to the caller$`, func() error { return s.servedBy("tower") })
			sc.Step(`^nothing local serves "([^"]*)"$`, func(string) error { return nil })
			sc.Step(`^the market has "([^"]*)" on air$`, func(string) error { return nil })
			sc.Step(`^it is refused$`, func() error { return s.refusedNaming() })
			sc.Step(`^the refusal names the band and says nothing on this LAN serves it$`, func() error {
				return s.refusedNaming("gpt-oss-120b", "nothing on this LAN serves")
			})
			sc.Step(`^it names the three ways to change that, and that turning LAN mode off would use the market$`, func() error {
				return s.refusedNaming("serve it here", "add an Edge member", "add a Tower", "turning LAN mode off would use the market")
			})
			sc.Step(`^no request was made to the public broker$`, s.noMarketInference)
			sc.Step(`^no money moved$`, s.noMarketInference)
			sc.Step(`^the Edge member "([^"]*)" and the LAN Tower at ([0-9.:]+) both serve "([^"]*)"$`, func(node, addr, band string) error {
				s.lastBand, s.lastTower = band, addr
				if err := s.face(node, band, 0); err != nil {
					return err
				}
				return s.towerServing(addr, band)
			})
			sc.Step(`^"([^"]*)" refuses the connection$`, func(node string) error { s.faces[node].srv.Close(); return nil })
			sc.Step(`^both fail$`, func() error {
				for _, f := range s.faces {
					f.srv.Close()
				}
				s.failingTower(s.lastTower, s.lastBand)
				return nil
			})
			sc.Step(`^it is served by the Tower at ([0-9.:]+)$`, func(string) error { return s.servedBy("tower") })
			sc.Step(`^the turn's record names "([^"]*)" as the source it left$`, func(node string) error {
				if left := s.hdr.Get("X-RogerAI-LAN-Left"); !strings.Contains(left, node) {
					return fmt.Errorf("X-RogerAI-LAN-Left %q does not name %q", left, node)
				}
				return nil
			})
			sc.Step(`^this machine serves nothing$`, func() error { return nil })
			sc.Step(`^the Edge member "([^"]*)" serves "([^"]*)", VERIFIED, LAN-direct$`, func(node, band string) error {
				return s.face(node, band, 0)
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) also serves "([^"]*)"$`, s.towerServing)
			sc.Step(`^it is served by "([^"]*)" over mTLS$`, func(node string) error {
				if err := s.servedBy(node); err != nil {
					return err
				}
				if s.faces[node].mutualTLS.Load() == 0 {
					return fmt.Errorf("%s answered without the caller's certificate", node)
				}
				return nil
			})
			sc.Step(`^the Tower was not contacted for inference$`, func() error {
				if n := s.towerHits.Load(); n != 0 {
					return fmt.Errorf("the Tower's station served %d turns", n)
				}
				return nil
			})
			sc.Step(`^two Edge members serve "([^"]*)" and one has measured faster on this machine$`, func(band string) error {
				s.lastBand = band
				if err := s.face("atlas", band, 150*time.Millisecond); err != nil {
					return err
				}
				if err := s.face("zephyr", band, 0); err != nil {
					return err
				}
				for i := 0; i < 2; i++ { // turns served on this machine measure each member
					if err := s.dispatch(band); err != nil {
						return err
					}
				}
				for n, f := range s.faces {
					s.before[n] = f.hits.Load()
				}
				return nil
			})
			sc.Step(`^the faster member is tried first$`, func() error {
				if err := s.servedBy("zephyr"); err != nil {
					return err
				}
				if d := s.faces["atlas"].hits.Load() - s.before["atlas"]; d != 0 {
					return fmt.Errorf("the slower member was tried first (%d turns)", d)
				}
				return nil
			})
			sc.Step(`^it is refused, naming "([^"]*)" and the Tower and how each failed$`, func(node string) error {
				return s.refusedNaming(node, "192.168.1.20:8787", "status 503")
			})
			sc.Step(`^the request carries provider\.max_price with prompt 0\.1 and completion 0\.1$`, func() error { return nil })
			sc.Step(`^edge\.prefer is "([^"]*)"$`, func(v string) error {
				c := loadConfig()
				c.EdgePrefer = v
				return saveConfig(c)
			})
			sc.Step("^`roger status` is read$", func() error { return s.cli("--lan", "status") })
			sc.Step(`^it reports the Edge preference as "(.*)"$`, func(want string) error {
				if !strings.Contains(s.cliOut, "edge preference: "+want) {
					return fmt.Errorf("status does not report %q:\n%s", want, s.cliOut)
				}
				return nil
			})
			sc.Step(`^a turn is dispatched for a band the market serves and nothing local serves$`, func() error {
				return s.dispatch("llama-3.3-70b")
			})
			sc.Step(`^a command runs with --lan and exits$`, func() error { return s.cli("--lan", "status") })
			sc.Step(`^edge\.prefer is still "([^"]*)"$`, func(want string) error {
				if got := loadConfig().EdgePrefer; got != want {
					return fmt.Errorf("edge.prefer is %q, want %q", got, want)
				}
				return nil
			})
			sc.Step(`^LAN mode is off$`, func() error { s.off = true; return nil })
			sc.Step(`^only the LAN Tower at ([0-9.:]+) serves "([^"]*)"$`, s.towerServing)
			sc.Step(`^it is refused as not served on this Edge$`, func() error {
				if err := s.refusedNaming("local-only: no instance on this Edge serves"); err != nil {
					return err
				}
				if n := s.towerHits.Load(); n != 0 {
					return fmt.Errorf("the LAN Tower served %d turn(s) without LAN mode", n)
				}
				return nil
			})
			sc.Step(`^nothing local serves a TTS band$`, func() error { return nil })
			sc.Step(`^they run "roger ([^"]*)"$`, func(line string) error { return s.cli(strings.Fields(line)...) })
			sc.Step(`^it is refused, saying no local voice is on air$`, func() error {
				if s.cliErr == nil || !strings.Contains(s.cliErr.Error(), "no local voice is on air") {
					return fmt.Errorf("not refused for want of a local voice: err=%v out=%s", s.cliErr, s.cliOut)
				}
				return nil
			})
			sc.Step(`^no request was made to the public broker's (/\S+)$`, func(path string) error {
				return brokerNotAsked(s.broker, path)
			})
			sc.Step(`^it behaves exactly as without --lan$`, func() error {
				withLAN, withLANErr, asked := s.cliOut, fmt.Sprint(s.cliErr), s.broker.count()
				if asked == 0 {
					return fmt.Errorf("the command asked the broker nothing, so nothing was compared: %s %v", withLAN, s.cliErr)
				}
				if err := s.cli("remote-control"); err != nil {
					return err
				}
				if s.cliOut != withLAN || fmt.Sprint(s.cliErr) != withLANErr {
					return fmt.Errorf("--lan changed it:\nwith:    %s %s\nwithout: %s %v", withLAN, withLANErr, s.cliOut, s.cliErr)
				}
				if again := s.broker.count() - asked; again != asked {
					return fmt.Errorf("with --lan it made %d broker request(s), without %d", asked, again)
				}
				return nil
			})
			sc.Step(`^two local sources serve "([^"]*)"$`, func(band string) error {
				if err := s.face("jetson", band, 40*time.Millisecond); err != nil {
					return err
				}
				return s.face("orin", band, 0)
			})
			sc.Step(`^it is routed as "([^"]*)"$`, func(meaning string) error {
				if s.code != http.StatusOK {
					return fmt.Errorf("status %d: %s", s.code, s.body)
				}
				if strings.Contains(meaning, "fastest") {
					// every member is measured once (an unmeasured one is tried first), then the
					// measured-faster one is tried first
					asked := s.asked
					for i := 0; i < 3 && (s.faces["orin"].hits.Load() == 0 || s.faces["jetson"].hits.Load() == 0); i++ {
						if err := s.dispatch(asked); err != nil {
							return err
						}
					}
					before := s.faces["orin"].hits.Load()
					if err := s.dispatch(asked); err != nil {
						return err
					}
					if s.faces["orin"].hits.Load() != before+1 {
						return fmt.Errorf("the measured-faster member was not tried first")
					}
				}
				return nil
			})
			sc.Step(`^the model the source receives is "([^"]*)"$`, func(want string) error {
				for name, f := range s.faces {
					if v, _ := f.lastModel.Load().(string); v != "" && v != want {
						return fmt.Errorf("%s received model %q, want %q", name, v, want)
					}
				}
				return nil
			})
			sc.Step(`^a turn is dispatched with X-Roger-Freq naming a private band$`, func() error {
				s.tweak = func(o *client.ProxyOptions) { o.Freq = "fm-7q2" }
				if err := s.dispatch("qwen3-8b"); err != nil {
					return err
				}
				// the same code given to `roger --lan use` is refused before anything is resolved
				return s.cli("--lan", "use", "qwen3-8b", "--freq", "fm-7q2")
			})
			sc.Step(`^it is refused, saying private bands are on the network and LAN mode is on$`, func() error {
				if err := s.refusedNaming("private bands are on the network and LAN mode is on"); err != nil {
					return err
				}
				if s.cliErr == nil || !strings.Contains(s.cliErr.Error(), "private bands are on the network and LAN mode is on") {
					return fmt.Errorf("`roger --lan use --freq` was not refused: err=%v out=%s", s.cliErr, s.cliOut)
				}
				return nil
			})
			sc.Step(`^the caller sends "Authorization: Bearer rog-key_\.\.\." through the loopback proxy$`, func() error {
				s.bearer = "rog-key_abc123"
				s.tweak = func(o *client.ProxyOptions) { o.SessionKey = "" }
				return nil
			})
			sc.Step(`^the turn is served by a LAN Tower$`, func() error {
				if err := s.towerServing("192.168.1.20:8787", "qwen3-8b"); err != nil {
					return err
				}
				if err := s.dispatch("qwen3-8b"); err != nil {
					return err
				}
				return s.servedBy("tower")
			})
			sc.Step(`^the request to the Tower does not carry the account key$`, func() error {
				tw := s.towers["192.168.1.20:8787"]
				tw.mu.Lock()
				defer tw.mu.Unlock()
				if tw.chatRequests == 0 {
					return fmt.Errorf("no request reached the Tower")
				}
				if tw.lastAuth != "" {
					return fmt.Errorf("the Tower was sent Authorization %q", tw.lastAuth)
				}
				return nil
			})
			sc.Step(`^it carries this machine's signature instead$`, func() error {
				tw := s.towers["192.168.1.20:8787"]
				tw.mu.Lock()
				defer tw.mu.Unlock()
				if tw.lastSig == "" {
					return fmt.Errorf("the Tower request carried no signature")
				}
				return nil
			})
			// routing constraints on the LAN ladder (class aliases, quantizations, provider.order)
			sc.Step(`^the local sources serve "([^"]*)" and "([^"]*)"$`, func(a, b string) error {
				if err := s.face("atlas", a, 0); err != nil {
					return err
				}
				return s.face("zephyr", b, 0)
			})
			sc.Step(`^a turn is dispatched for "@class/<a class both belong to>"$`, func() error {
				// No built-in class holds both an 8B and a 20B model; "small" holds the 8B one, which
				// is the local band the class must resolve to.
				return s.dispatch("@class/small")
			})
			sc.Step(`^it resolves to one of the local bands$`, func() error {
				if s.code != http.StatusOK {
					return fmt.Errorf("status %d: %s", s.code, s.body)
				}
				for _, f := range s.faces {
					if v, _ := f.lastModel.Load().(string); v == "qwen3-8b" {
						return nil
					}
				}
				return fmt.Errorf("no local source received the class's local band: %s", s.body)
			})
			sc.Step(`^never to a market band of that class$`, func() error {
				if s.broker.count() != 0 {
					return fmt.Errorf("the broker was asked: %v", s.broker.paths)
				}
				return nil
			})
			sc.Step(`^the Edge member "([^"]*)" serves "([^"]*)" quantized "([^"]*)" and this machine serves it quantized "([^"]*)"$`, func(node, band, q, selfQ string) error {
				if err := s.faceQuant(node, band, q); err != nil {
					return err
				}
				if err := s.machineServesRoute(band); err != nil {
					return err
				}
				lanSelfQuant = func(b string) string {
					if b == band {
						return selfQ
					}
					return ""
				}
				return nil
			})
			sc.Step(`^only the Edge member "([^"]*)" serves "([^"]*)" quantized "([^"]*)"$`, func(node, band, q string) error {
				return s.faceQuant(node, band, q)
			})
			sc.Step(`^a turn is dispatched for "([^"]*)" with provider\.quantizations \["([^"]*)"\]$`, func(band, q string) error {
				return s.dispatchBody(band, `{"model":"`+band+`","messages":[{"role":"user","content":"hi"}],"provider":{"quantizations":["`+q+`"]}}`)
			})
			sc.Step(`^it is refused naming provider\.quantizations$`, func() error {
				if err := s.refusedNaming("provider.quantizations"); err != nil {
					return err
				}
				for n, f := range s.faces {
					if f.hits.Load() != 0 {
						return fmt.Errorf("%s served a turn it does not meet the constraint for", n)
					}
				}
				return nil
			})
			sc.Step(`^a turn is dispatched with provider\.order naming a market station id and allow_fallbacks false$`, func() error {
				// something local serves the band, so only the order can be why it is refused
				if err := s.machineServesRoute("qwen3-8b"); err != nil {
					return err
				}
				return s.dispatchBody("qwen3-8b", `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}],"provider":{"order":["st-9f2c41d0a7b3"],"allow_fallbacks":false}}`)
			})
			sc.Step(`^it is refused, saying the named station is not on this LAN$`, func() error {
				if err := s.refusedNaming("st-9f2c41d0a7b3", "not on this LAN"); err != nil {
					return err
				}
				if n := s.selfHits.Load(); n != 0 {
					return fmt.Errorf("this machine served %d turn(s) the order excluded", n)
				}
				return nil
			})
			sc.Step(`^a guest turn for "([^"]*)" carries provider\.order naming the LAN Tower at ([0-9.:]+) first$`, func(band, addr string) error {
				return s.dispatchBody(band, `{"model":"`+band+`","messages":[{"role":"user","content":"hi"}],"provider":{"order":["`+addr+`"]}}`)
			})
			sc.Step(`^it is served$`, func() error {
				// the turn carries the caps the Given names
				if err := s.dispatchBody("qwen3-8b", `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}],"provider":{"max_price":{"prompt":0.1,"completion":0.1}}}`); err != nil {
					return err
				}
				return s.servedBy("self")
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("routing_and_inference.feature failed")
	}
}
