package main

// join_tower_identity_bdd_test.go runs features/join/tower_identity.feature against a real
// standalone plane served over the real TLS-and-plain listener (towerpin.Sniff, exactly what
// `roger tower serve` binds), the real `roger join`, `roger lan add`, LAN dial and station loop.
// A counter wraps the plane only to OBSERVE which requests arrived, so "nothing was sent" and
// "never fell back to plain http" are proven, not assumed. Addresses in the feature are symbolic:
// "192.168.1.20:8787" is the fixture Tower's loopback address.
//
// Not run here yet: "The Tower's fingerprint is shown where the owner looks" (`roger tower status`
// is built; the owner's TUI and console Tower panel land with request-and-approve) and "A client
// joined by approval pins the key it saw during the pairing" (request-and-approve).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/towercli"
	"rogerai.fm/roger/v6/internal/towerpin"
)

const towerIdentityFeature = "../../features/join/tower_identity.feature"

// countingTower is a real plane on a real TLS-and-plain listener, with a count of the requests
// that reached it, split by whether they came over TLS.
type countingTower struct {
	st        *tower.State
	addr      string
	srv       *http.Server
	tlsReqs   atomic.Int64
	plainReqs atomic.Int64
	polls     atomic.Int64
	polls401  atomic.Int64 // polls the plane refused: the station's key is not the attached one
}

// statusRecorder notes the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func startCountingTower(t *testing.T, st *tower.State, addr string) *countingTower {
	t.Helper()
	key, err := st.TLSKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := towerpin.ServerTLS(key)
	if err != nil {
		t.Fatal(err)
	}
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	ct := &countingTower{st: st, addr: ln.Addr().String()}
	plane := localplane.New(st).Handler()
	ct.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			ct.tlsReqs.Add(1)
		} else {
			ct.plainReqs.Add(1)
		}
		if r.URL.Path == "/local/poll" {
			ct.polls.Add(1) // counted on arrival: an accepted poll is a long-poll
		}
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		plane.ServeHTTP(rec, r)
		if r.URL.Path == "/local/poll" && rec.code == http.StatusUnauthorized {
			ct.polls401.Add(1)
		}
	})}
	go func() { _ = ct.srv.Serve(towerpin.Sniff(ln, cfg)) }()
	t.Cleanup(func() { _ = ct.srv.Close() })
	return ct
}

func (ct *countingTower) requests() int64 { return ct.tlsReqs.Load() + ct.plainReqs.Load() }

type towerIdentityBDD struct {
	t        *testing.T
	st       *tower.State
	dir      string
	tw       *countingTower
	fp       string
	out      string
	err      error
	dial     []client.LANBand
	status   []client.LANTowerStatus
	pinsWas  map[string]string
	logs     *bytes.Buffer
	logMu    sync.Mutex
	newAddr  string
	plain    *http.Server
	plainAt  string
	stOut    string
	edgeBand []client.LANEdgeBand
}

func (s *towerIdentityBDD) reset() {
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	lanmode.SetPins(nil)
	s.dir = s.t.TempDir() + "/lab"
	st, err := tower.Init(s.dir, tower.ModeStandalone)
	if err != nil {
		s.t.Fatal(err)
	}
	s.st = st
	s.fp = mustFP(st)
	s.tw = startCountingTower(s.t, st, "")
	s.out, s.err, s.dial, s.status, s.pinsWas, s.newAddr, s.plainAt, s.stOut = "", nil, nil, nil, nil, "", "", ""
	s.edgeBand = nil
}

// joinByInvite admits this machine through a real bearer invite and the real `roger join`.
func (s *towerIdentityBDD) joinByInvite() error {
	_, code, err := s.st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	if err != nil {
		return err
	}
	in := joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{s.tw.addr}, FP: s.fp, Code: code}
	s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), []string{in.String()}) })
	return s.err
}

// admitPlain admits this machine's key the way an older client was admitted (no pin).
func (s *towerIdentityBDD) admitPlain() error {
	_, code, err := s.st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	if err != nil {
		return err
	}
	pubHex := hex.EncodeToString(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey))
	n := protocol.NewNonce()
	_, err = s.st.ConsumeJoin(tower.JoinRequest{Proof: joincode.Tower.Proof(code, n, pubHex, s.fp), Nonce: n,
		PubHex: pubHex, KeyHash: protocol.UserIDFromPubkey(pubHex), Name: "laptop-x"})
	return err
}

func (s *towerIdentityBDD) dialNow(towers ...string) {
	s.dial, s.status = client.LANDiscover(context.Background(), client.LANInputs{Towers: towers, Edge: s.edgeBand, Timeout: 3 * time.Second})
}

func TestJoinTowerIdentityBDD(t *testing.T) {
	s := &towerIdentityBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})
			sc.Step(`^a standalone Tower "lab" serves at 192\.168\.1\.20:8787 over TLS with key fingerprint "sha256:9f3c\.\.\.e1"$`, func() error {
				fp, err := towerpin.Fetch(s.tw.addr, 2*time.Second)
				if err != nil {
					return err
				}
				if fp != s.fp {
					return fmt.Errorf("the Tower serves %s, its tls.key is %s", fp, s.fp)
				}
				return nil
			})

			// 1. The Tower serves TLS
			sc.Step("^`roger tower serve` starts in a standalone directory$", func() error {
				var b bytes.Buffer
				p, err := prepareServe([]string{"--dir", s.dir + "-serve", "--bind", "127.0.0.1:0"}, &b)
				if err == nil {
					return fmt.Errorf("serve opened a directory that was never initialized")
				}
				if _, err := tower.Init(s.dir+"-serve", tower.ModeStandalone); err != nil {
					return err
				}
				b.Reset()
				p, err = prepareServe([]string{"--dir", s.dir + "-serve", "--bind", "127.0.0.1:0"}, &b)
				if err != nil {
					return err
				}
				go func() { _ = p.Serve() }()
				s.t.Cleanup(func() { _ = p.Close() })
				s.stOut, s.newAddr = b.String(), p.Addr()
				return nil
			})
			sc.Step(`^it serves TLS using a certificate self-signed by the Tower's tls\.key$`, func() error {
				st, err := tower.Open(s.dir + "-serve")
				if err != nil {
					return err
				}
				want := mustFP(st)
				conn, err := tls.Dial("tcp", s.newAddr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // inspecting the certificate
				if err != nil {
					return err
				}
				defer conn.Close()
				cert := conn.ConnectionState().PeerCertificates[0]
				pub, ok := cert.PublicKey.(ed25519.PublicKey)
				if !ok || towerpin.Fingerprint(pub) != want {
					return fmt.Errorf("the certificate's key is not the Tower's tls.key")
				}
				if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
					return fmt.Errorf("the certificate is not self-signed: %v", err)
				}
				if !bytes.Equal(cert.RawIssuer, cert.RawSubject) {
					return fmt.Errorf("issuer differs from subject")
				}
				return nil
			})
			sc.Step("^`roger tower serve` prints the fingerprint it serves under$", func() error {
				st, _ := tower.Open(s.dir + "-serve")
				if !strings.Contains(s.stOut, "sha256:"+mustFP(st)) {
					return fmt.Errorf("serve printed no fingerprint:\n%s", s.stOut)
				}
				return nil
			})
			sc.Step(`^/discover, /v1/models, /v1/chat/completions, /local/poll, /local/complete, /local/chunk and the join routes all answer only over that TLS listener \(plain http aside, see section 4\)$`, func() error {
				hc := towerpin.Client(s.fp, 3*time.Second)
				for _, p := range []string{"/discover", "/v1/models", "/v1/chat/completions", "/local/poll", "/local/complete", "/local/chunk", "/local/join"} {
					resp, err := hc.Post("https://"+s.tw.addr+p, "application/json", strings.NewReader("{}"))
					if err != nil {
						return fmt.Errorf("%s over TLS: %v", p, err)
					}
					resp.Body.Close()
					if resp.StatusCode == http.StatusNotFound {
						return fmt.Errorf("%s is not served over TLS", p)
					}
				}
				return nil
			})

			// 2. The client pins and checks
			sc.Step(`^the client joins with an invite naming "sha256:9f3c\.\.\.e1"$`, s.joinByInvite)
			sc.Step(`^lan_towers records the Tower with pin "sha256:9f3c\.\.\.e1"$`, func() error {
				c := loadConfig()
				if !contains(c.LanTowers, s.tw.addr) || c.LanTowerPins[s.tw.addr] != s.fp {
					return fmt.Errorf("lan_towers %v, pins %v; want %s pinned to %s", c.LanTowers, c.LanTowerPins, s.tw.addr, s.fp)
				}
				return nil
			})
			sc.Step(`^the client pinned "sha256:9f3c\.\.\.e1"$`, s.joinByInvite)
			sc.Step(`^it sends any request to the Tower$`, func() error {
				s.dialNow(s.tw.addr)
				return nil
			})
			sc.Step(`^the TLS handshake is accepted only if the Tower's certificate key hashes to the pin$`, func() error {
				if len(s.status) != 1 || s.status[0].State != client.LANTowerAnswering {
					return fmt.Errorf("the pinned Tower did not answer: %+v", s.status)
				}
				if s.tw.tlsReqs.Load() == 0 || s.tw.plainReqs.Load() != 0 {
					return fmt.Errorf("tls %d, plain %d: the request did not go over TLS", s.tw.tlsReqs.Load(), s.tw.plainReqs.Load())
				}
				// The same request with any other pin is refused in the handshake.
				lanmode.SetPins(map[string]string{s.tw.addr: strings.Repeat("ab", 32)})
				before := s.tw.requests()
				s.dialNow(s.tw.addr)
				if s.status[0].State == client.LANTowerAnswering || s.tw.requests() != before {
					return fmt.Errorf("a Tower whose key does not hash to the pin was accepted")
				}
				return nil
			})
			sc.Step(`^no CA, hostname or expiry check replaces the pin$`, func() error {
				// The Tower's certificate names no host and chains to no CA, yet the pin alone is
				// enough: the request above reached it over TLS by its IP address.
				conn, err := tls.Dial("tcp", s.tw.addr, towerpin.ClientTLS(s.fp))
				if err != nil {
					return err
				}
				defer conn.Close()
				cert := conn.ConnectionState().PeerCertificates[0]
				if len(cert.DNSNames)+len(cert.IPAddresses) != 0 {
					return fmt.Errorf("the certificate names hosts: %v %v", cert.DNSNames, cert.IPAddresses)
				}
				return nil
			})
			sc.Step(`^the device now answering at 192\.168\.1\.20:8787 presents a different key$`, func() error {
				c := loadConfig()
				s.pinsWas = map[string]string{}
				for k, v := range c.LanTowerPins {
					s.pinsWas[k] = v
				}
				if _, _, err := s.st.RotateTLSKey(); err != nil {
					return err
				}
				_ = s.tw.srv.Close()
				s.tw = startCountingTower(s.t, s.st, s.tw.addr)
				return nil
			})
			sc.Step(`^the client sends a request$`, func() error {
				s.dialNow(s.tw.addr)
				return nil
			})
			sc.Step(`^the request is refused before any prompt is sent$`, func() error {
				if s.status[0].State != client.LANTowerRefused {
					return fmt.Errorf("state %s, want refused", s.status[0].State)
				}
				if n := s.tw.requests(); n != 0 {
					return fmt.Errorf("%d request(s) reached the Tower", n)
				}
				return nil
			})
			sc.Step(`^it prints "the Tower at 192\.168\.1\.20:8787 is not the one you joined \(its key changed\)\. If the owner reset it, re-join with a new invite: roger join <invite>"$`, func() error {
				want := "the Tower at " + s.tw.addr + " is not the one you joined (its key changed). If the owner reset it, re-join with a new invite: roger join <invite>"
				if s.status[0].Detail != want {
					return fmt.Errorf("said %q, want %q", s.status[0].Detail, want)
				}
				return nil
			})
			sc.Step(`^the pin is not changed$`, func() error {
				if got := loadConfig().LanTowerPins; fmt.Sprint(got) != fmt.Sprint(s.pinsWas) {
					return fmt.Errorf("pins %v, were %v", got, s.pinsWas)
				}
				return nil
			})
			sc.Step(`^the Tower moves to 192\.168\.1\.21:8787 with the same key$`, func() error {
				if err := s.joinByInvite(); err != nil {
					return err
				}
				s.pinsWas = loadConfig().LanTowerPins
				_ = s.tw.srv.Close()
				s.tw = startCountingTower(s.t, s.st, "")
				return nil
			})
			sc.Step(`^the client re-adds it or is given a new invite$`, func() error {
				var b bytes.Buffer
				s.err = lanAddTo(&b, []string{s.tw.addr})
				s.out = b.String()
				return s.err
			})
			sc.Step(`^the existing pin is kept and nothing is re-trusted$`, func() error {
				pins := loadConfig().LanTowerPins
				for a, fp := range s.pinsWas {
					if pins[a] != fp {
						return fmt.Errorf("the existing pin for %s changed", a)
					}
				}
				if pins[s.tw.addr] != s.fp {
					return fmt.Errorf("the moved Tower at %s is not pinned to its key: %v\n%s", s.tw.addr, pins, s.out)
				}
				for _, fp := range pins {
					if fp != s.fp {
						return fmt.Errorf("a new key was trusted: %s", fp)
					}
				}
				return nil
			})
			sc.Step(`^a pinned Tower and an Edge member are on the dial$`, func() error {
				if err := s.joinByInvite(); err != nil {
					return err
				}
				// the pinned Tower needs a band on air to have a row: attach a station for one
				if _, err := s.st.AttachStation("st-1", protocol.UserIDFromPubkey(strings.Repeat("ab", 32)), []string{"qwen3-8b"}); err != nil {
					return err
				}
				s.edgeBand = []client.LANEdgeBand{{Node: "jetson", Band: "gpt-oss-20b", Verified: true, LANDirect: true}}
				s.dialNow(s.tw.addr)
				return nil
			})
			sc.Step(`^the Tower row's source carries "verified", like the Edge row$`, func() error {
				got := map[string]bool{}
				for _, b := range s.dial {
					for _, src := range b.Sources {
						got[string(src.Kind)] = src.Verified
					}
				}
				if !got["tower"] || !got["edge"] {
					return fmt.Errorf("verified by source kind: %v (dial %+v)", got, s.dial)
				}
				return nil
			})
			sc.Step(`^a station joined with a station invite$`, func() error {
				_, code, err := s.st.CreateJoinInvitation(tower.JoinStation, "", nil, 15*time.Minute)
				if err != nil {
					return err
				}
				in := joininvite.Invite{Kind: joininvite.TowerStation, Addrs: []string{s.tw.addr}, FP: s.fp, Code: code}
				prev := detectFull
				detectFull = fakeDetect("qwen3-8b")
				defer func() { detectFull = prev }()
				fixtureIsRogerServed(s.t) // the fixture's model is one roger serves, which a station offers
				s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), []string{in.String(), "st-1"}) })
				return s.err
			})
			sc.Step(`^its /local/poll, /local/complete and /local/chunk calls check the Tower's pin$`, func() error {
				poll := func() int64 {
					before := s.tw.polls.Load()
					ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
					defer cancel()
					_ = agent.ServeLocalTower(ctx, agent.Config{Broker: "http://" + s.tw.addr}, agent.NodeKey(), &bytes.Buffer{})
					return s.tw.polls.Load() - before
				}
				if poll() == 0 || s.tw.plainReqs.Load() != 0 {
					return fmt.Errorf("the pinned station did not poll over TLS (plain %d)", s.tw.plainReqs.Load())
				}
				if n := s.tw.polls401.Load(); n != 0 {
					return fmt.Errorf("the Tower refused %d of the joined station's polls: it attached a key the station does not poll with", n)
				}
				lanmode.SetPins(map[string]string{s.tw.addr: strings.Repeat("ab", 32)})
				if n := poll(); n != 0 {
					return fmt.Errorf("a station pinned to another key reached the Tower %d time(s)", n)
				}
				return nil
			})

			// 3. A reset Tower
			sc.Step(`^the owner runs the new tower command rotate-key$`, func() error {
				// The Tower is stopped for the rotation (the running Tower's admin socket lands
				// next), then served again under its new key.
				_ = s.tw.srv.Close()
				var b strings.Builder
				if code := towercli.Run([]string{"rotate-key", "--dir", s.dir}, &b, &b); code != 0 {
					return fmt.Errorf("rotate-key exited %d:\n%s", code, b.String())
				}
				s.out = b.String()
				s.tw = startCountingTower(s.t, s.st, s.tw.addr)
				return nil
			})
			sc.Step(`^the Tower serves under a new fingerprint$`, func() error {
				fp, err := towerpin.Fetch(s.tw.addr, 2*time.Second)
				if err != nil {
					return err
				}
				if fp == s.fp || fp != mustFP(s.st) {
					return fmt.Errorf("serving %s; was %s, tls.key now %s", fp, s.fp, mustFP(s.st))
				}
				return nil
			})
			sc.Step(`^it prints that every client and station must re-join with a new invite$`, func() error {
				if !strings.Contains(s.out, "every client and station must re-join with a new invite") {
					return fmt.Errorf("rotate-key said:\n%s", s.out)
				}
				return nil
			})
			sc.Step("^`roger tower invite` mints invites naming the new fingerprint$", func() error {
				var b strings.Builder
				if code := towercli.Run([]string{"invite", "--dir", s.dir, "--at", s.tw.addr}, &b, &b); code != 0 {
					return fmt.Errorf("invite exited %d:\n%s", code, b.String())
				}
				m := joinLineRE.FindStringSubmatch(b.String())
				if m == nil {
					return fmt.Errorf("no join line:\n%s", b.String())
				}
				in, err := joininvite.Parse(m[1])
				if err != nil {
					return err
				}
				if in.FP != mustFP(s.st) || in.FP == s.fp {
					return fmt.Errorf("the invite names %s, the Tower serves %s", in.FP, mustFP(s.st))
				}
				return nil
			})

			// 4. Plain http for one release
			sc.Step(`^a Tower that answers only plain http \(an older roger\)$`, func() error {
				if err := s.admitPlain(); err != nil {
					return err
				}
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					return err
				}
				s.plain = &http.Server{Handler: localplane.New(s.st).Handler()}
				go func() { _ = s.plain.Serve(ln) }()
				s.t.Cleanup(func() { _ = s.plain.Close() })
				s.plainAt = ln.Addr().String()
				_, _ = s.st.AttachStation("st-1", protocol.UserIDFromPubkey(strings.Repeat("ab", 32)), []string{"qwen3-8b"})
				return nil
			})
			sc.Step(`^a client uses it$`, func() error {
				s.dialNow(s.plainAt)
				var src []client.LANSource
				for _, b := range s.dial {
					src = append(src, b.Sources...)
				}
				s.out = captureStdout(s.t, func() { lanTrustNoteOnce(src, "") })
				return nil
			})
			sc.Step(`^it works as today, marked "unverified", with the one-time trust note$`, func() error {
				if len(s.status) != 1 || s.status[0].State != client.LANTowerAnswering || len(s.dial) == 0 {
					return fmt.Errorf("the plain Tower did not work: %+v", s.status)
				}
				for _, b := range s.dial {
					for _, src := range b.Sources {
						if src.Verified {
							return fmt.Errorf("a plain-http Tower was marked verified")
						}
					}
				}
				if !strings.Contains(s.out, lanTrustNote) {
					return fmt.Errorf("no trust note:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the client prints "this Tower uses plain http; ask its owner to upgrade roger so it can be verified"$`, func() error {
				if !strings.Contains(s.out, "this Tower uses plain http; ask its owner to upgrade roger so it can be verified") {
					return fmt.Errorf("said:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the Tower serves TLS and plain http$`, func() error {
				s.logs = &bytes.Buffer{}
				log.SetOutput(&lockedWriter{mu: &s.logMu, w: s.logs})
				s.t.Cleanup(func() { log.SetOutput(os.Stderr) })
				return s.admitPlain()
			})
			sc.Step(`^a client uses plain http$`, func() error {
				s.dialNow(s.tw.addr) // unpinned: plain http
				s.dialNow(s.tw.addr)
				if s.tw.plainReqs.Load() < 2 {
					return fmt.Errorf("the client did not use plain http (%d)", s.tw.plainReqs.Load())
				}
				return nil
			})
			sc.Step(`^the Tower logs once per client "laptop-x used plain http; upgrade it"$`, func() error {
				s.logMu.Lock()
				got := s.logs.String()
				s.logMu.Unlock()
				if n := strings.Count(got, "laptop-x used plain http; upgrade it"); n != 1 {
					return fmt.Errorf("logged %d time(s):\n%s", n, got)
				}
				return nil
			})
			sc.Step(`^the client pinned the Tower's key$`, s.joinByInvite)
			sc.Step(`^the TLS handshake fails$`, func() error {
				before := s.tw.requests()
				lanmode.SetPins(map[string]string{s.tw.addr: strings.Repeat("ab", 32)})
				s.dialNow(s.tw.addr)
				if s.tw.requests() != before {
					return fmt.Errorf("a request reached the Tower")
				}
				return nil
			})
			sc.Step(`^the client refuses and does not retry over plain http$`, func() error {
				if s.status[0].State != client.LANTowerRefused {
					return fmt.Errorf("state %s, want refused", s.status[0].State)
				}
				if s.tw.plainReqs.Load() != 0 {
					return fmt.Errorf("the client fell back to plain http (%d request(s))", s.tw.plainReqs.Load())
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{towerIdentityFeature + ":21", towerIdentityFeature + ":30", towerIdentityFeature + ":37",
				towerIdentityFeature + ":45", towerIdentityFeature + ":51", towerIdentityFeature + ":59",
				towerIdentityFeature + ":64", towerIdentityFeature + ":68", towerIdentityFeature + ":76",
				towerIdentityFeature + ":90", towerIdentityFeature + ":96", towerIdentityFeature + ":101"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/tower_identity.feature failed")
	}
}

// lockedWriter serializes writes from the log package and the test reading them.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
