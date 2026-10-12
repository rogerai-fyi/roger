package main

// join_approve_bdd_test.go runs features/join/approve.feature against a REAL serving standalone
// Tower (serve.Prepare over TLS, its admin socket and plane), a REAL client (`roger join <address>`
// in process), and the owner deciding through the same admin socket the TUI and console use. A
// middle party is a real TLS reverse proxy with its own key. Scenarios that need the TUI or console
// panels or an Edge authority run with those surfaces (surfaces.feature, items in the plan).

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/coreclient"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/joinsas"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/localplane/serve"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
	"rogerai.fm/roger/v6/internal/towercli"
	"rogerai.fm/roger/v6/internal/towerpin"
)

const approveFeature = "../../features/join/approve.feature"

var clientPhraseRE = regexp.MustCompile(`matches the owner's screen: (\S+)`)

type approveBDD struct {
	t          *testing.T
	dir, addr  string
	fp         string
	p          *serve.Prepared
	now        time.Time
	clockMu    sync.Mutex
	runs       []*pairRun
	owner      string // the owner's phrase for the last decided request
	ownerLine  string
	rawBodies  []string
	middleAddr string
	middleFP   string
}

type pairRun struct {
	out  string
	err  error
	done chan struct{}
}

func (s *approveBDD) clock() time.Time {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	return s.now
}

func (s *approveBDD) advance(d time.Duration) {
	s.clockMu.Lock()
	s.now = s.now.Add(d)
	s.clockMu.Unlock()
}

func (s *approveBDD) reset() error {
	// The previous scenario's Tower stops first: nothing of it may read this scenario's clock.
	if s.p != nil {
		_ = s.p.Close()
		for _, r := range s.runs {
			<-r.done
		}
	}
	useTempConfig(s.t)
	base, err := os.MkdirTemp("", "ap")
	if err != nil {
		return err
	}
	s.t.Cleanup(func() { _ = os.RemoveAll(base) })
	s.dir = filepath.Join(base, "lab")
	if _, err := tower.Init(s.dir, tower.ModeStandalone); err != nil {
		return err
	}
	coreclient.Reset()
	p, err := serve.Prepare([]string{"--dir", s.dir, "--bind", "127.0.0.1:0", "--label", "lab"}, &bytes.Buffer{})
	if err != nil {
		return err
	}
	s.p, s.addr = p, p.Addr()
	s.clockMu.Lock()
	s.now = time.Now()
	s.clockMu.Unlock()
	p.Plane().SetPairClock(s.clock)
	p.Plane().SetPairPoll(50 * time.Millisecond)
	go func() { _ = p.Serve() }()
	s.t.Cleanup(func() { _ = p.Close() })
	for i := 0; i < 100 && !toweradmin.Running(s.dir); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := tower.Open(s.dir)
	s.fp, _ = st.JoinFingerprint()
	s.runs, s.owner, s.ownerLine, s.rawBodies, s.middleAddr, s.middleFP = nil, "", "", nil, "", ""
	pairWaitFor = localplane.PairLifetime
	return nil
}

// ask runs `roger join <addr> <name>` in the background, as a client or a station.
func (s *approveBDD) ask(addr, name string, station bool) *pairRun {
	r := &pairRun{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		var b bytes.Buffer
		r.err = joinPairAt(&b, addr, name, station)
		r.out = b.String()
	}()
	s.runs = append(s.runs, r)
	return r
}

func (s *approveBDD) last() *pairRun { return s.runs[len(s.runs)-1] }

func (s *approveBDD) finish(r *pairRun) error {
	select {
	case <-r.done:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("roger join never finished")
	}
}

// waiting is the owner's view of the waiting requests, once one shows up.
func (s *approveBDD) waiting(n int) ([]tower.PairRequest, error) {
	for i := 0; i < 300; i++ {
		got, err := toweradmin.Client{Dir: s.dir}.PairRequests()
		if err != nil {
			return nil, err
		}
		if len(got) >= n {
			return got, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("the owner never saw %d request(s)", n)
}

func (s *approveBDD) decide(approve bool) error {
	reqs, err := s.waiting(1)
	if err != nil {
		return err
	}
	r := reqs[len(reqs)-1]
	s.owner, s.ownerLine = r.Phrase, towercli.RequestLine(r, time.Now())
	c := toweradmin.Client{Dir: s.dir}
	if approve {
		return c.ApprovePair(r.ID)
	}
	return c.RefusePair(r.ID)
}

func clientPhrase(out string) string {
	if m := clientPhraseRE.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

func (s *approveBDD) admitted() bool {
	st, err := tower.Open(s.dir)
	if err != nil {
		return false
	}
	pub := hex.EncodeToString(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey))
	return st.IsAdmitted(protocol.UserIDFromPubkey(pub))
}

// raw sends one pairing start (and, when reveal is set, its reveal) from a fresh key, from the
// loopback address src, so a test can be many devices on many addresses.
func (s *approveBDD) rawPair(src string, reveal bool) (int, string, error) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	eph, _ := ecdh.X25519().GenerateKey(rand.Reader)
	nonce := joinsas.NewNonce()
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: towerpin.ClientTLS(s.fp),
		DialContext:     (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}}).DialContext,
	}}
	post := func(path string, body any) (int, []byte, error) {
		b, _ := json.Marshal(body)
		n := protocol.NewNonce()
		_, ts, sig := protocol.SignRequestNonce(priv, http.MethodPost, path, b, n)
		req, _ := http.NewRequest(http.MethodPost, "https://"+s.addr+path, bytes.NewReader(b))
		req.Header.Set(protocol.HeaderPubkey, pub)
		req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		req.Header.Set(protocol.HeaderSig, sig)
		req.Header.Set(protocol.HeaderNonce, n)
		resp, err := hc.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		if resp.Header.Get("Retry-After") != "" {
			return resp.StatusCode, append([]byte("retry-after;"), buf.Bytes()...), nil
		}
		return resp.StatusCode, buf.Bytes(), nil
	}
	code, body, err := post("/local/pair/start", localplane.PairStartBody{Kind: "client", Name: "dev-" + src,
		ClientEph: hex.EncodeToString(eph.PublicKey().Bytes()), Commit: joinsas.Commit(nonce)})
	if err != nil || code != http.StatusOK || !reveal {
		return code, string(body), err
	}
	var start localplane.PairStartAnswer
	_ = json.Unmarshal(body, &start)
	code, body, err = post("/local/pair/reveal", localplane.PairRevealBody{ID: start.ID, ClientNonce: nonce})
	return code, string(body), err
}

// middle stands a TLS reverse proxy with its own key between the client and the Tower.
func (s *approveBDD) middle() error {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	cfg, err := towerpin.ServerTLS(priv)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	up, _ := url.Parse("https://" + s.addr)
	rp := httputil.NewSingleHostReverseProxy(up)
	rp.Transport = &http.Transport{TLSClientConfig: towerpin.ClientTLS(s.fp)}
	srv := &http.Server{Handler: rp}
	go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
	s.t.Cleanup(func() { _ = srv.Close() })
	s.middleAddr, s.middleFP = ln.Addr().String(), towerpin.Fingerprint(priv.Public().(ed25519.PublicKey))
	return nil
}

func TestJoinApproveBDD(t *testing.T) {
	s := &approveBDD{t: t}
	phraseShape := regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z]+-[0-9]{2}$`)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, s.reset() })
			sc.Step(`^a standalone Tower "lab" is serving at 192\.168\.1\.20:8787 over TLS$`, func() error { return nil })
			sc.Step(`^the owner has the Tower's TUI open$`, func() error { return nil })
			sc.Step(`^the Tower runs headless with no TUI or console open$`, func() error { return nil })
			sc.Step(`^neither machine can reach the internet$`, func() error {
				s.t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
				return nil
			})

			// --- the client asks ---------------------------------------------------------------
			ask := func() error { s.ask(s.addr, "laptop-x", false); _, err := s.waiting(1); return err }
			sc.Step(`^a client asks to join$`, ask)
			sc.Step(`^a request is shown$`, ask)
			sc.Step(`^the client asks to join$`, func() error {
				addr := s.addr
				if s.middleAddr != "" {
					addr = s.middleAddr
				}
				s.ask(addr, "laptop-x", false)
				_, err := s.waiting(1)
				return err
			})
			sc.Step(`^a station runs "roger join 192\.168\.1\.20:8787 --station"$`, func() error {
				detectFull = fakeDetect("qwen3-8b")
				fixtureIsRogerServed(t) // the fixture's model is one roger serves, which a station offers
				s.ask(s.addr, "kitchen-pi", true)
				_, err := s.waiting(1)
				return err
			})
			sc.Step(`^the client asks to join and the owner presses n$`, func() error {
				s.ask(s.addr, "laptop-x", false)
				if err := s.decide(false); err != nil {
					return err
				}
				return s.finish(s.last())
			})
			pairing := func() error {
				s.ask(s.addr, "laptop-x", false)
				if err := s.decide(true); err != nil {
					return err
				}
				return s.finish(s.last())
			}
			sc.Step(`^a pairing runs$`, pairing)
			// tower_identity.feature: the key a pairing pins is the one its phrase covered.
			sc.Step(`^a standalone Tower "lab" serves at 192\.168\.1\.20:8787 over TLS with key fingerprint "sha256:9f3c\.\.\.e1"$`, func() error { return nil })
			sc.Step(`^the client joins by request and approve$`, pairing)
			sc.Step(`^lan_towers records the TLS fingerprint the SAS covered$`, func() error {
				if err := s.last().err; err != nil {
					return fmt.Errorf("the pairing failed: %v\n%s", err, s.last().out)
				}
				c := loadConfig()
				if !slices.Contains(c.LanTowers, s.addr) {
					return fmt.Errorf("lan_towers %v does not list the Tower at %s", c.LanTowers, s.addr)
				}
				// The fingerprint the phrase covered is the Tower's TLS key: the one this runner's
				// Tower serves, and the one a direct pinned dial to it accepts.
				if pin := c.LanTowerPins[s.addr]; pin != s.fp {
					return fmt.Errorf("pinned %q, the Tower's TLS key is %q", pin, s.fp)
				}
				resp, err := towerpin.Client(s.fp, 2*time.Second).Get("https://" + s.addr + "/discover")
				if err != nil {
					return fmt.Errorf("a dial pinned to the recorded fingerprint failed: %v", err)
				}
				resp.Body.Close()
				return nil
			})
			sc.Step(`^a client asks and the owner approves$`, pairing)
			// 2026-10-11 founder test: an admitted client asking again is told it is already a member.
			var againOut string
			sc.Step(`^laptop-x already joined the Tower by request and approve$`, func() error {
				if err := pairing(); err != nil {
					return err
				}
				if err := s.last().err; err != nil || !s.admitted() {
					return fmt.Errorf("the first join did not admit laptop-x: %v\n%s", err, s.last().out)
				}
				return nil
			})
			sc.Step(`^laptop-x runs roger join with the Tower's address again$`, func() error {
				// A join that wrongly asks the owner waits for an answer that never comes: bound the
				// wait so the scenario reports what was printed instead of hanging.
				pairWaitFor = 2 * time.Second
				r := s.ask(s.addr, "laptop-x", false)
				if err := s.finish(r); err != nil {
					return err
				}
				againOut = r.out
				if r.err != nil {
					againOut += "error: " + r.err.Error()
				}
				return nil
			})
			sc.Step(`^laptop-x runs roger join with a fresh client invite$`, func() error {
				_, code, err := toweradmin.Client{Dir: s.dir}.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
				if err != nil {
					return err
				}
				in := joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{s.addr}, FP: s.fp, Code: code}
				key := client.LoadOrCreateUserKey()
				pub := hex.EncodeToString(key.Public().(ed25519.PublicKey))
				ans, err := joinPost(key, pub, s.addr, in, "client", "laptop-x", nil)
				if err != nil {
					return err
				}
				var b bytes.Buffer
				if err := joinRecord(&b, s.addr, in, ans, "laptop-x"); err != nil {
					return err
				}
				againOut = b.String()
				return nil
			})
			sc.Step(`^it prints "([^"]*)"$`, func(want string) error {
				want = strings.ReplaceAll(want, "<address>", s.addr)
				if !strings.Contains(againOut, want) {
					return fmt.Errorf("want %q in:\n%s", want, againOut)
				}
				return nil
			})
			sc.Step(`^it shows no phrase and never mentions an invite$`, func() error {
				if strings.Contains(againOut, "phrase") || strings.Contains(againOut, "invite") {
					return fmt.Errorf("an admitted client asking again saw:\n%s", againOut)
				}
				return nil
			})
			sc.Step(`^no request waits on the owner's screen$`, func() error {
				got, err := toweradmin.Client{Dir: s.dir}.PairRequests()
				if err != nil {
					return err
				}
				if len(got) != 0 {
					return fmt.Errorf("the owner sees %d request(s) from a client that is already a member", len(got))
				}
				return nil
			})
			sc.Step(`^the invite is still live for another device$`, func() error {
				if n := len(toweradmin.Client{Dir: s.dir}.LiveInvitations()); n != 1 {
					return fmt.Errorf("live invitations: %d, want the unused one", n)
				}
				return nil
			})
			sc.Step(`^the same client asks to join twice$`, func() error {
				for i := 0; i < 2; i++ {
					s.ask(s.addr, "laptop-x", false)
					if err := s.decide(false); err != nil {
						return err
					}
					if err := s.finish(s.last()); err != nil {
						return err
					}
				}
				return nil
			})
			sc.Step(`^a client asks to join and nobody answers for 2 minutes$`, func() error {
				s.ask(s.addr, "laptop-x", false)
				if _, err := s.waiting(1); err != nil {
					return err
				}
				s.advance(localplane.PairLifetime + time.Second)
				return s.finish(s.last())
			})

			// --- the owner decides -------------------------------------------------------------
			sc.Step(`^the owner presses y$`, func() error {
				if err := s.decide(true); err != nil {
					return err
				}
				return s.finish(s.last())
			})
			sc.Step(`^the owner sees "kitchen-pi \(a station\) wants to join · <phrase> · serving: qwen3-8b"$`, func() error {
				reqs, err := s.waiting(1)
				if err != nil {
					return err
				}
				line := towercli.RequestLine(reqs[0], time.Now())
				if want := "kitchen-pi (a station) wants to join · " + reqs[0].Phrase + " · serving: qwen3-8b"; !strings.Contains(line, want) {
					return fmt.Errorf("the owner's line %q lacks %q", line, want)
				}
				return nil
			})
			sc.Step(`^the station is attached with its self-reported models$`, func() error {
				if r := s.last(); r.err != nil {
					return fmt.Errorf("the station's join failed: %v\n%s", r.err, r.out)
				}
				st, _ := tower.Open(s.dir)
				sts, err := st.Stations()
				if err != nil || len(sts) != 1 || !slices.Equal(sts[0].Models, []string{"qwen3-8b"}) {
					return fmt.Errorf("stations: %+v (%v)", sts, err)
				}
				return nil
			})
			sc.Step(`^the client prints "lab refused the request"$`, func() error {
				if r := s.last(); r.err == nil || strings.SplitN(r.err.Error(), "\n", 2)[0] != "lab refused the request" {
					return fmt.Errorf("the client said %v", r.err)
				}
				return nil
			})
			noTrace := func() error {
				if s.admitted() {
					return errors.New("the key was admitted")
				}
				if len(loadConfig().LanTowerPins) != 0 {
					return fmt.Errorf("a Tower was pinned: %v", loadConfig().LanTowerPins)
				}
				return nil
			}
			sc.Step(`^the client's key is not admitted and nothing is pinned$`, noTrace)
			sc.Step(`^nothing is admitted and nothing is pinned until the owner approves$`, noTrace)
			sc.Step(`^a refused or expired request leaves no trace beyond a log line$`, func() error {
				if err := s.decide(false); err != nil {
					return err
				}
				if err := s.finish(s.last()); err != nil {
					return err
				}
				if reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests(); len(reqs) != 0 {
					return fmt.Errorf("the refused request is still shown: %+v", reqs)
				}
				return noTrace()
			})

			// --- the phrase --------------------------------------------------------------------
			sc.Step(`^the phrase is derived from a hash of the transcript \(domain "join-sas/v1", K, T, both ephemeral X25519 public keys, the client's committed nonce, the Tower's nonce\)$`, func() error {
				if joinsas.Domain != "join-sas/v1" {
					return fmt.Errorf("domain %q", joinsas.Domain)
				}
				if got := clientPhrase(s.last().out); got == "" || got != s.owner {
					return fmt.Errorf("client %q, owner %q: computed independently, they must agree", got, s.owner)
				}
				return nil
			})
			sc.Step(`^the client's nonce commitment is sent before the Tower's nonce is received$`, func() error {
				start := reflect.TypeOf(localplane.PairStartBody{})
				for i := 0; i < start.NumField(); i++ {
					if strings.Contains(strings.ToLower(start.Field(i).Name), "nonce") {
						return fmt.Errorf("the opening message carries the nonce itself (%s), not only its commitment", start.Field(i).Name)
					}
				}
				if _, ok := start.FieldByName("Commit"); !ok {
					return errors.New("the opening message carries no commitment")
				}
				return nil
			})
			sc.Step(`^both screens show the phrase as three words and two digits joined by hyphens$`, func() error {
				for _, p := range []string{clientPhrase(s.last().out), s.owner} {
					if !phraseShape.MatchString(p) {
						return fmt.Errorf("phrase %q is not three words and two digits", p)
					}
				}
				return nil
			})
			sc.Step(`^the words come from a fixed list shipped with roger, the same on every platform$`, func() error {
				words := joinsas.Words()
				if len(words) != 2048 {
					return fmt.Errorf("%d words", len(words))
				}
				for _, w := range strings.Split(s.owner, "-")[:3] {
					if !slices.Contains(words, w) {
						return fmt.Errorf("%q is not in the list", w)
					}
				}
				return nil
			})
			sc.Step(`^a device between the client and the Tower terminates both connections with its own keys$`, s.middle)
			sc.Step(`^the phrase on the client differs from the phrase on the owner's screen$`, func() error {
				reqs, err := s.waiting(1)
				if err != nil {
					return err
				}
				s.owner = reqs[0].Phrase
				return nil // compared once the client has printed, below
			})
			sc.Step(`^if the owner presses y anyway, the client's pin is the middle device's key and the pin is refused on the next direct contact with the real Tower$`, func() error {
				if err := s.decide(true); err != nil {
					return err
				}
				if err := s.finish(s.last()); err != nil {
					return err
				}
				if got := clientPhrase(s.last().out); got == "" || got == s.owner {
					return fmt.Errorf("client %q and owner %q: a middle party must make them differ", got, s.owner)
				}
				pin := loadConfig().LanTowerPins[s.middleAddr]
				if pin != s.middleFP {
					return fmt.Errorf("pinned %q, the middle's key is %q", pin, s.middleFP)
				}
				_, err := towerpin.Client(pin, 2*time.Second).Get("https://" + s.addr + "/discover")
				if !errors.Is(err, towerpin.ErrPinMismatch) {
					return fmt.Errorf("direct contact with the real Tower under the middle's pin: %v", err)
				}
				return nil
			})
			sc.Step(`^a middle party that waits to see the client's nonce before choosing its own$`, func() error { return nil })
			sc.Step(`^the pairing runs$`, func() error {
				// The middle tries to steer: it reveals a nonce other than the one committed to.
				code, _, err := s.rawPairSteered()
				if err != nil {
					return err
				}
				s.rawBodies = append(s.rawBodies, strconv.Itoa(code))
				return nil
			})
			sc.Step(`^the client refuses because the reveal does not match the commitment, or the phrases differ$`, func() error {
				if s.rawBodies[0] != strconv.Itoa(http.StatusUnauthorized) {
					return fmt.Errorf("a steered reveal answered %s", s.rawBodies[0])
				}
				if reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests(); len(reqs) != 0 {
					return errors.New("a steered request reached the owner's screen")
				}
				return nil
			})
			sc.Step(`^the two pairings show different phrases$`, func() error {
				a, b := clientPhrase(s.runs[0].out), clientPhrase(s.runs[1].out)
				if a == "" || a == b {
					return fmt.Errorf("phrases %q and %q", a, b)
				}
				return nil
			})

			// --- limits ------------------------------------------------------------------------
			sc.Step(`^the client prints "lab did not answer in 2 minutes\. Ask the owner to open roger, or use an invite"$`, func() error {
				if r := s.last(); r.err == nil || r.err.Error() != "lab did not answer in 2 minutes. Ask the owner to open roger, or use an invite" {
					return fmt.Errorf("the client said %v", r.err)
				}
				return nil
			})
			sc.Step(`^the request disappears from the owner's screen$`, func() error {
				if reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests(); len(reqs) != 0 {
					return fmt.Errorf("still shown: %+v", reqs)
				}
				return nil
			})
			sc.Step(`^the request waits as pending$`, func() error { _, err := s.waiting(1); return err })
			sc.Step(`^the new tower command requests, run on the Tower, lists it with its phrase$`, func() error {
				reqs, _ := s.waiting(1)
				var b strings.Builder
				if code := towercli.Run([]string{"requests", "--dir", s.dir}, &b, &b); code != 0 || !strings.Contains(b.String(), reqs[0].Phrase) {
					return fmt.Errorf("roger tower requests (exit %d):\n%s", code, b.String())
				}
				return nil
			})
			sc.Step(`^the new tower command approve <id> approves it from the CLI$`, func() error {
				reqs, _ := s.waiting(1)
				var b strings.Builder
				if code := towercli.Run([]string{"approve", "--dir", s.dir, reqs[0].ID}, &b, &b); code != 0 {
					return fmt.Errorf("roger tower approve (exit %d):\n%s", code, b.String())
				}
				if err := s.finish(s.last()); err != nil {
					return err
				}
				if !s.admitted() {
					return fmt.Errorf("not admitted after approve:\n%v\n%s", s.last().err, s.last().out)
				}
				return nil
			})
			sc.Step(`^if nobody acts within 2 minutes it expires$`, func() error {
				// Another device (its own key): this runner's own client was just approved, and an
				// admitted client asking again creates no request (approve.feature:148).
				if code, body, err := s.rawPair("127.0.0.2", true); err != nil || code != http.StatusOK {
					return fmt.Errorf("a second device could not ask: %d %s %v", code, body, err)
				}
				if _, err := s.waiting(1); err != nil {
					return err
				}
				s.advance(localplane.PairLifetime + time.Second)
				if reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests(); len(reqs) != 0 {
					return fmt.Errorf("an expired request is still shown: %+v", reqs)
				}
				return nil
			})
			sc.Step(`^laptop-x has a pending request$`, func() error {
				s.ask(s.addr, "laptop-x", false)
				reqs, err := s.waiting(1)
				if err == nil {
					s.owner = reqs[0].Phrase
				}
				return err
			})
			sc.Step(`^laptop-x asks again$`, func() error {
				s.ask(s.addr, "laptop-x", false)
				for i := 0; i < 300; i++ {
					reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests()
					if len(reqs) == 1 && reqs[0].Phrase != s.owner {
						return nil
					}
					time.Sleep(10 * time.Millisecond)
				}
				return errors.New("the second request never replaced the first")
			})
			sc.Step(`^the earlier request is replaced, not added, and the new one gets a new phrase$`, func() error {
				reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests()
				if len(reqs) != 1 || reqs[0].Phrase == s.owner {
					return fmt.Errorf("requests %+v (earlier phrase %q)", reqs, s.owner)
				}
				return nil
			})
			sc.Step(`^8 requests are already pending$`, func() error {
				for i := 2; i < 2+localplane.PairPendingCap; i++ {
					if code, body, err := s.rawPair("127.0.0."+strconv.Itoa(i), true); err != nil || code != http.StatusOK {
						return fmt.Errorf("request %d: %d %s %v", i, code, body, err)
					}
				}
				return nil
			})
			sc.Step(`^a ninth device asks to join$`, func() error {
				code, body, err := s.rawPair("127.0.0.30", true)
				s.rawBodies = append(s.rawBodies, strconv.Itoa(code)+" "+body)
				return err
			})
			sc.Step(`^it is told "lab is busy with other join requests, try again in a minute"$`, func() error {
				if !strings.Contains(s.rawBodies[0], "lab is busy with other join requests, try again in a minute") {
					return fmt.Errorf("the ninth was told %q", s.rawBodies[0])
				}
				return nil
			})
			sc.Step(`^it is not shown to the owner$`, func() error {
				reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests()
				if len(reqs) != localplane.PairPendingCap {
					return fmt.Errorf("%d shown", len(reqs))
				}
				return nil
			})
			sc.Step(`^one address sends 6 join requests within a minute$`, func() error {
				for i := 0; i < 6; i++ {
					code, body, err := s.rawPair("127.0.0.40", true)
					if err != nil {
						return err
					}
					s.rawBodies = append(s.rawBodies, strconv.Itoa(code)+" "+body)
				}
				return nil
			})
			sc.Step(`^the sixth is refused with a retry-after$`, func() error {
				if !strings.HasPrefix(s.rawBodies[5], "429 retry-after;") {
					return fmt.Errorf("the sixth answered %q", s.rawBodies[5])
				}
				return nil
			})
			sc.Step(`^none beyond the cap reach the owner's screen$`, func() error {
				reqs, _ := (toweradmin.Client{Dir: s.dir}).PairRequests()
				if len(reqs) > localplane.PairPerAddressPerMinute {
					return fmt.Errorf("%d shown", len(reqs))
				}
				return nil
			})

			// --- honest screens ----------------------------------------------------------------
			sc.Step(`^the owner's line says "approve only if laptop-x shows the same phrase"$`, func() error {
				var b strings.Builder
				towercli.Run([]string{"requests", "--dir", s.dir}, &b, &b)
				if !strings.Contains(b.String(), "approve only if laptop-x shows the same phrase") {
					return fmt.Errorf("roger tower requests:\n%s", b.String())
				}
				return nil
			})
			sc.Step(`^the client's line says "the owner should see the same phrase"$`, func() error {
				if err := s.decide(false); err != nil {
					return err
				}
				if err := s.finish(s.last()); err != nil {
					return err
				}
				if !strings.Contains(s.last().out, "the owner should see the same phrase") {
					return fmt.Errorf("the client printed:\n%s", s.last().out)
				}
				return nil
			})
			sc.Step(`^the client joins and nothing contacts Roger Core$`, func() error {
				if r := s.last(); r.err != nil || !s.admitted() {
					return fmt.Errorf("join: %v\n%s", r.err, r.out)
				}
				if n := coreclient.Noted(); len(n) != 0 {
					return fmt.Errorf("a Core client was built: %v", n)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{approveFeature + ":43", approveFeature + ":56", approveFeature + ":65", approveFeature + ":72",
				approveFeature + ":77", approveFeature + ":83", approveFeature + ":88", approveFeature + ":97",
				approveFeature + ":102", approveFeature + ":110", approveFeature + ":117", approveFeature + ":123",
				approveFeature + ":128", approveFeature + ":137", approveFeature + ":142",
				approveFeature + ":148", approveFeature + ":156",
				towerIdentityFeature + ":41"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/approve.feature failed")
	}
}

// rawPairSteered is a request whose reveal is not the nonce it committed to: what a middle party
// steering the phrase would have to send.
func (s *approveBDD) rawPairSteered() (int, string, error) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	eph, _ := ecdh.X25519().GenerateKey(rand.Reader)
	committed := joinsas.NewNonce()
	b, _ := json.Marshal(localplane.PairStartBody{Kind: "client", Name: "x",
		ClientEph: hex.EncodeToString(eph.PublicKey().Bytes()), Commit: joinsas.Commit(committed)})
	code, raw, err := towerPost(priv, pub, s.addr, s.fp, "/local/pair/start", b, protocol.NewNonce(), 5*time.Second)
	if err != nil || code != http.StatusOK {
		return code, string(raw), err
	}
	var start localplane.PairStartAnswer
	_ = json.Unmarshal(raw, &start)
	b, _ = json.Marshal(localplane.PairRevealBody{ID: start.ID, ClientNonce: joinsas.NewNonce()})
	code, raw, err = towerPost(priv, pub, s.addr, s.fp, "/local/pair/reveal", b, protocol.NewNonce(), 5*time.Second)
	return code, string(raw), err
}
