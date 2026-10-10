package main

// join_adversarial_bdd_test.go runs features/join/adversarial.feature: every way the simple join
// could be abused, against a real standalone Tower on the real TLS-and-plain listener
// (startCountingTower), the real `roger join`, the real join wire (towerPost, the same signed and
// pinned post roger join sends), the real pairing handlers and the real local-address rule. The
// feature's "192.168.1.20:8787" is the fixture Tower's loopback address; addresses that must be
// public or LAN-only are injected through the resolver seams the product uses.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge/wire"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/joinsas"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
	"rogerai.fm/roger/v6/internal/towercli"
	"rogerai.fm/roger/v6/internal/towerpin"
)

const adversarialFeature = "../../features/join/adversarial.feature"

// joiner is a device with its own key, posting the real join wire.
type joiner struct {
	key ed25519.PrivateKey
	pub string
}

func newJoiner() joiner {
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	return joiner{key: k, pub: hex.EncodeToString(k.Public().(ed25519.PublicKey))}
}

func (j joiner) id() string { return protocol.UserIDFromPubkey(j.pub) }

type adversarialBDD struct {
	t        *testing.T
	dir      string
	st       *tower.State
	fp       string
	tw       *countingTower
	inv      joininvite.Invite
	out      string
	err      error
	status   int
	raw      string
	logs     *syncBuf
	captured struct {
		body  []byte
		nonce string
		j     joiner
	}
	other   *tower.State
	otherTw *countingTower
	plain   *httptest.Server
	plainN  int
	codes   []int
	winners int
	msgs    []string
}

func (s *adversarialBDD) reset() {
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	lanmode.SetPins(nil)
	s.dir = s.t.TempDir() + "/lab"
	st, err := tower.Init(s.dir, tower.ModeStandalone)
	if err != nil {
		s.t.Fatal(err)
	}
	s.st, s.fp = st, mustFP(st)
	s.logs = &syncBuf{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(s.logs)
	log.SetFlags(0)
	s.t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	s.tw = startCountingTower(s.t, st, "")
	s.inv, s.out, s.err, s.status, s.raw = joininvite.Invite{}, "", nil, 0, ""
	s.other, s.otherTw, s.plain, s.plainN, s.codes, s.winners, s.msgs = nil, nil, nil, 0, nil, 0, nil
	joinResolve = nil
	s.t.Cleanup(func() { joinResolve = nil })
}

// mint is a live client invite for lab at the fixture's address.
func (s *adversarialBDD) mint() string {
	_, code, err := s.st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	if err != nil {
		s.t.Fatal(err)
	}
	s.inv = joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{s.tw.addr}, FP: s.fp, Code: code}
	return code
}

// post sends a join from j with the given code over the real pinned wire to lab.
func (s *adversarialBDD) post(j joiner, code, name string) (int, string) {
	nonce := protocol.NewNonce()
	body, _ := json.Marshal(localplane.JoinRequestBody{Kind: "client", Proof: joincode.Tower.Proof(code, nonce, j.pub, s.fp), Name: name})
	status, raw, err := towerPost(j.key, j.pub, s.tw.addr, s.fp, "/local/join", body, nonce, 5*time.Second)
	if err != nil {
		s.t.Fatal(err)
	}
	return status, string(raw)
}

// joinMe runs the real `roger join` with the current invite as this machine.
func (s *adversarialBDD) joinMe(in joininvite.Invite) {
	s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), []string{in.String()}) })
}

func (s *adversarialBDD) meAdmitted() bool {
	return s.st.IsAdmitted(protocol.UserIDFromPubkey(hex.EncodeToString(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey))))
}

func (s *adversarialBDD) inviteSpent() bool {
	for _, inv := range s.st.LiveInvitations() {
		if inv.Bearer && !inv.Consumed {
			return false
		}
	}
	return true
}

// signed builds a request signed by j with a fresh nonce, as the join wire does, for the plane's
// handler (so a source address can be chosen).
func signed(j joiner, path string, body []byte, from string) *http.Request {
	nonce := protocol.NewNonce()
	_, ts, sig := protocol.SignRequestNonce(j.key, http.MethodPost, path, body, nonce)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set(protocol.HeaderPubkey, j.pub)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)
	r.Header.Set(protocol.HeaderNonce, nonce)
	r.RemoteAddr = from
	return r
}

func TestJoinAdversarialBDD(t *testing.T) {
	s := &adversarialBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})
			sc.Step(`^a standalone Tower "lab" at 192\.168\.1\.20:8787 with fingerprint "sha256:9f3c\.\.\.e1"$`, func() error { return nil })

			// ----- impostor Tower -----
			impostor := func() *countingTower {
				st, err := tower.Init(s.t.TempDir()+"/imp", tower.ModeStandalone)
				if err != nil {
					s.t.Fatal(err)
				}
				return startCountingTower(s.t, st, "")
			}
			sc.Step(`^a different device answers at 192\.168\.1\.20:8787 with its own key$`, func() error {
				s.otherTw = impostor()
				return nil
			})
			sc.Step(`^a client runs "roger join" with a valid invite for "lab"$`, func() error {
				s.mint()
				in := s.inv
				in.Addrs = []string{s.otherTw.addr}
				s.joinMe(in)
				return nil
			})
			sc.Step(`^the client refuses because the key does not match the invite's fingerprint$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "is not the one in the invite") {
					return fmt.Errorf("join = %v, want the fingerprint refusal", s.err)
				}
				return nil
			})
			sc.Step(`^it sends no code proof, no prompt and nothing else$`, func() error {
				if n := s.otherTw.requests(); n != 0 {
					return fmt.Errorf("%d request(s) reached the impostor", n)
				}
				return nil
			})
			sc.Step(`^the invite is not spent$`, func() error {
				if s.inviteSpent() {
					return fmt.Errorf("the real invite was spent")
				}
				return nil
			})
			sc.Step(`^an impostor terminates the client's TLS with its own key and forwards to the real Tower$`, func() error {
				// The impostor serves TLS under its own key and relays whatever it receives to lab.
				st, err := tower.Init(s.t.TempDir()+"/mitm", tower.ModeStandalone)
				if err != nil {
					return err
				}
				key, _ := st.TLSKey()
				cfg, _ := towerpin.ServerTLS(key)
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					return err
				}
				var relayed sync.Mutex
				n := 0
				srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					relayed.Lock()
					n++
					relayed.Unlock()
					req, _ := http.NewRequest(r.Method, "https://"+s.tw.addr+r.URL.Path, r.Body)
					req.Header = r.Header
					if resp, err := towerpin.Client(s.fp, 5*time.Second).Do(req); err == nil {
						w.WriteHeader(resp.StatusCode)
						resp.Body.Close()
					}
				})}
				go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
				s.t.Cleanup(func() { _ = srv.Close() })
				s.otherTw = &countingTower{addr: ln.Addr().String()}
				s.t.Cleanup(func() {
					relayed.Lock()
					defer relayed.Unlock()
					if n != 0 {
						s.t.Errorf("the relaying impostor received %d request(s)", n)
					}
				})
				return nil
			})
			sc.Step(`^the client joins with a valid invite$`, func() error {
				s.mint()
				in := s.inv
				in.Addrs = []string{s.otherTw.addr}
				s.joinMe(in)
				return nil
			})
			sc.Step(`^the client refuses on the fingerprint before the proof is sent$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "is not the one in the invite") {
					return fmt.Errorf("join = %v, want the fingerprint refusal", s.err)
				}
				if s.meAdmitted() || s.inviteSpent() {
					return fmt.Errorf("the relay completed the join")
				}
				return nil
			})
			sc.Step(`^a device advertises itself as Tower "lab" on the LAN$`, func() error {
				s.otherTw = impostor()
				host, port, _ := net.SplitHostPort(s.otherTw.addr)
				p, _ := strconv.Atoi(port)
				return s.advertise(net.ParseIP(host), p, "lab")
			})
			sc.Step(`^it appears only as a candidate on the dial and is never used until joined$`, func() error {
				lines := s.candidates()
				if len(lines) != 1 || lines[0].State != client.LANTowerCandidate || lines[0].Addr != s.otherTw.addr {
					return fmt.Errorf("candidates %+v", lines)
				}
				if n := s.otherTw.requests(); n != 0 {
					return fmt.Errorf("%d request(s) reached the advertised device", n)
				}
				return nil
			})
			sc.Step(`^joining it by invite fails on the fingerprint$`, func() error {
				s.mint()
				in := s.inv
				in.Addrs = []string{s.otherTw.addr}
				s.joinMe(in)
				if s.err == nil || !strings.Contains(s.err.Error(), "is not the one in the invite") {
					return fmt.Errorf("join = %v", s.err)
				}
				return nil
			})

			// ----- impostor client -----
			sc.Step(`^a device posts a join request with no code and no pending approval$`, func() error {
				s.status, s.raw = s.post(newJoiner(), "", "x")
				return nil
			})
			sc.Step(`^it gets the uniform 401 and its key is not admitted$`, func() error {
				if s.status != http.StatusUnauthorized || strings.TrimSpace(s.raw) != `{"error":"unauthorized"}` {
					return fmt.Errorf("answer %d %s", s.status, s.raw)
				}
				return nil
			})
			sc.Step(`^a device presents a wrong code for a real invite$`, func() error {
				s.mint()
				st1, raw1 := s.post(newJoiner(), joincode.Tower.NewCode(), "x")
				for _, inv := range s.st.LiveInvitations() {
					_ = s.st.VoidInvitation(inv.ID)
				}
				st2, raw2 := s.post(newJoiner(), joincode.Tower.NewCode(), "x")
				s.codes = []int{st1, st2}
				s.msgs = []string{raw1, raw2}
				return nil
			})
			sc.Step(`^the answer is the same as for an unknown invite$`, func() error {
				if s.codes[0] != s.codes[1] || s.msgs[0] != s.msgs[1] || s.codes[0] != http.StatusUnauthorized {
					return fmt.Errorf("a wrong code answered %d %q, an unknown invite %d %q", s.codes[0], s.msgs[0], s.codes[1], s.msgs[1])
				}
				return nil
			})
			sc.Step(`^the attempt counts against that invite's budget and the global limiter$`, func() error {
				code := s.mint()
				for i := 0; i < tower.JoinGuessBudget; i++ {
					s.post(newJoiner(), joincode.Tower.NewCode(), "x")
				}
				if st, _ := s.post(newJoiner(), code, "x"); st == http.StatusOK {
					return fmt.Errorf("the right code still joined after %d wrong guesses", tower.JoinGuessBudget)
				}
				if g := s.st.JoinGuesses(); g < tower.JoinGuessBudget {
					return fmt.Errorf("the global limiter counted %d guesses, want at least %d", g, tower.JoinGuessBudget)
				}
				return nil
			})

			// ----- replay -----
			sc.Step(`^a captured successful join request$`, func() error {
				code := s.mint()
				j := newJoiner()
				nonce := protocol.NewNonce()
				body, _ := json.Marshal(localplane.JoinRequestBody{Kind: "client", Proof: joincode.Tower.Proof(code, nonce, j.pub, s.fp), Name: "laptop-x"})
				status, raw, err := towerPost(j.key, j.pub, s.tw.addr, s.fp, "/local/join", body, nonce, 5*time.Second)
				if err != nil || status != http.StatusOK {
					return fmt.Errorf("the original join answered %d %s (%v)", status, raw, err)
				}
				s.captured.body, s.captured.nonce, s.captured.j = body, nonce, j
				return nil
			})
			sc.Step(`^it is sent again$`, func() error {
				status, raw, err := towerPost(s.captured.j.key, s.captured.j.pub, s.tw.addr, s.fp, "/local/join", s.captured.body, s.captured.nonce, 5*time.Second)
				s.status, s.raw, s.err = status, string(raw), err
				return nil
			})
			sc.Step(`^it is refused because the code is spent and the nonce was used$`, func() error {
				if s.err != nil || s.status != http.StatusUnauthorized {
					return fmt.Errorf("the replay answered %d %s (%v)", s.status, s.raw, s.err)
				}
				return nil
			})
			sc.Step(`^no second admission happens$`, func() error {
				if n := len(s.st.AdmittedClients()); n != 1 {
					return fmt.Errorf("%d clients admitted, want 1", n)
				}
				return nil
			})
			sc.Step(`^a proof made with a valid invite for "lab"$`, func() error {
				s.mint()
				other, err := tower.Init(s.t.TempDir()+"/other", tower.ModeStandalone)
				if err != nil {
					return err
				}
				s.other = other
				s.otherTw = startCountingTower(s.t, other, "")
				// the other Tower has a live invite of its own, so a match would be possible at all
				if _, _, err := other.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute); err != nil {
					return err
				}
				return nil
			})
			sc.Step(`^it is presented to another Tower$`, func() error {
				j := newJoiner()
				nonce := protocol.NewNonce()
				body, _ := json.Marshal(localplane.JoinRequestBody{Kind: "client", Proof: joincode.Tower.Proof(s.inv.Code, nonce, j.pub, s.fp), Name: "x"})
				otherFP := mustFP(s.other)
				status, raw, err := towerPost(j.key, j.pub, s.otherTw.addr, otherFP, "/local/join", body, nonce, 5*time.Second)
				s.status, s.raw, s.err = status, string(raw), err
				if s.other.IsAdmitted(j.id()) {
					return fmt.Errorf("the other Tower admitted it")
				}
				return nil
			})
			sc.Step(`^it does not verify because it covers "lab"'s fingerprint$`, func() error {
				if s.err != nil || s.status != http.StatusUnauthorized {
					return fmt.Errorf("answer %d %s (%v)", s.status, s.raw, s.err)
				}
				return nil
			})
			sc.Step(`^the Tower proof domain differs from the Edge proof domain$`, func() error {
				if joincode.Tower.Name == joincode.EdgeJoin.Name || joincode.Tower.Name == joincode.Edge.Name {
					return fmt.Errorf("the domains share a name")
				}
				return nil
			})
			sc.Step(`^a proof from one never verifies as the other$`, func() error {
				code := joincode.Tower.NewCode()
				p := joincode.Tower.Proof(code, "n", "a", "b")
				if joincode.EdgeJoin.Check(code, p, "n", "a", "b") == nil {
					return fmt.Errorf("a Tower proof verified as an Edge join")
				}
				ec := joincode.EdgeJoin.NewCode()
				if joincode.Tower.Check(ec, joincode.EdgeJoin.Proof(ec, "n", "a", "b"), "n", "a", "b") == nil {
					return fmt.Errorf("an Edge proof verified as a Tower join")
				}
				return nil
			})

			// ----- brute force -----
			sc.Step(`^a device sends wrong codes for one invite$`, func() error {
				s.mint()
				for i := 0; i < tower.JoinGuessBudget; i++ {
					s.post(newJoiner(), joincode.Tower.NewCode(), "x")
				}
				return nil
			})
			sc.Step(`^after the per-invite budget the invite is dead, even for the right code$`, func() error {
				if st, raw := s.post(newJoiner(), s.inv.Code, "x"); st == http.StatusOK {
					return fmt.Errorf("the right code joined after the budget: %s", raw)
				}
				return nil
			})
			sc.Step(`^continued guessing across invites hits the durable global limiter$`, func() error {
				for s.st.JoinGuesses() < tower.GlobalJoinGuessBudget {
					s.mint()
					if _, err := s.st.ConsumeJoin(tower.JoinRequest{Proof: "00", Nonce: "n", PubHex: newJoiner().pub,
						KeyHash: "", Name: "x"}); err == nil {
						return fmt.Errorf("a malformed guess was accepted")
					}
					j := newJoiner()
					_, _ = s.st.ConsumeJoin(tower.JoinRequest{Proof: joincode.Tower.Proof(joincode.Tower.NewCode(), "n", j.pub, s.fp),
						Nonce: "n", PubHex: j.pub, KeyHash: j.id(), Name: "x"})
				}
				code := s.mint()
				if st, _ := s.post(newJoiner(), code, "x"); st == http.StatusOK {
					return fmt.Errorf("a fresh invite's right code joined past the global limiter")
				}
				return nil
			})
			sc.Step(`^the limits hold across restarts and across source addresses$`, func() error {
				restarted, err := tower.Open(s.dir)
				if err != nil {
					return err
				}
				if restarted.JoinGuesses() < tower.GlobalJoinGuessBudget {
					return fmt.Errorf("the global count did not survive a restart: %d", restarted.JoinGuesses())
				}
				code := s.mint()
				plane := localplane.New(restarted)
				for _, from := range []string{"192.168.1.44:5000", "192.168.1.45:5000"} {
					j := newJoiner()
					nonce := protocol.NewNonce()
					body, _ := json.Marshal(localplane.JoinRequestBody{Kind: "client", Proof: joincode.Tower.Proof(code, nonce, j.pub, s.fp), Name: "x"})
					r := signed(j, "/local/join", body, from)
					r.Header.Set(protocol.HeaderNonce, nonce)
					_, ts, sig := protocol.SignRequestNonce(j.key, http.MethodPost, "/local/join", body, nonce)
					r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
					r.Header.Set(protocol.HeaderSig, sig)
					rec := httptest.NewRecorder()
					plane.Handler().ServeHTTP(rec, r)
					if rec.Code == http.StatusOK {
						return fmt.Errorf("a join from %s succeeded past the limiter after a restart", from)
					}
				}
				return nil
			})
			sc.Step(`^a Tower invite code carries at least 128 bits$`, func() error {
				if bits := joincode.Tower.CodeLen * 5; bits < 128 {
					return fmt.Errorf("a Tower code carries %d bits", bits)
				}
				return nil
			})

			// ----- stolen or expired invites -----
			sc.Step(`^laptop-x joined with an invite$`, func() error {
				code := s.mint()
				if st, raw := s.post(newJoiner(), code, "laptop-x"); st != http.StatusOK {
					return fmt.Errorf("laptop-x's join answered %d %s", st, raw)
				}
				return nil
			})
			sc.Step(`^someone else presents the same invite$`, func() error {
				s.joinMe(s.inv)
				return nil
			})
			sc.Step(`^it is refused as spent and the owner is told "a used invite was presented again from 192\.168\.1\.44"$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "that invite was already used") {
					return fmt.Errorf("the second join = %v, want the already-used refusal", s.err)
				}
				// the fixture's clients connect from loopback; the owner line names the real source
				if !strings.Contains(s.logs.String(), "a used invite was presented again from 127.0.0.1") {
					return fmt.Errorf("the owner was not told:\n%s", s.logs.String())
				}
				return nil
			})
			sc.Step(`^an attacker uses an invite before the intended client$`, func() error {
				code := s.mint()
				if st, raw := s.post(newJoiner(), code, "mallory"); st != http.StatusOK {
					return fmt.Errorf("the attacker's join answered %d %s", st, raw)
				}
				return nil
			})
			sc.Step(`^the owner sees "<attacker-name> joined" immediately$`, func() error {
				if !strings.Contains(s.logs.String(), "mallory joined") {
					return fmt.Errorf("the owner did not see it:\n%s", s.logs.String())
				}
				return nil
			})
			sc.Step(`^the new tower command revoke-client <name> removes it$`, func() error {
				var b strings.Builder
				if code := towercli.Run([]string{"revoke-client", "--dir", s.dir, "mallory"}, &b, &b); code != 0 {
					return fmt.Errorf("revoke-client exited %d:\n%s", code, b.String())
				}
				if len(s.st.AdmittedClients()) != 0 {
					return fmt.Errorf("mallory is still admitted")
				}
				return nil
			})
			sc.Step(`^the intended client is told the invite was already used and to ask for a new one$`, func() error {
				s.joinMe(s.inv)
				if s.err == nil || !strings.Contains(s.err.Error(), "that invite was already used. Ask the owner for a new one") {
					return fmt.Errorf("the intended client got %v", s.err)
				}
				return nil
			})
			sc.Step(`^an invite minted 16 minutes ago$`, func() error {
				_, code, err := s.st.CreateJoinInvitation(tower.JoinClient, "", nil, time.Millisecond)
				if err != nil {
					return err
				}
				time.Sleep(5 * time.Millisecond)
				s.inv = joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{s.tw.addr}, FP: s.fp, Code: code}
				return nil
			})
			sc.Step(`^a client presents it$`, func() error {
				s.joinMe(s.inv)
				return nil
			})
			sc.Step(`^it is refused with "that invite has expired\. Ask the owner for a new one"$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "that invite has expired. Ask the owner for a new one") {
					return fmt.Errorf("join = %v", s.err)
				}
				return nil
			})
			sc.Step(`^two clients present the same valid invite at the same moment$`, func() error {
				code := s.mint()
				var wg sync.WaitGroup
				var mu sync.Mutex
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						j := newJoiner()
						_, err := joinPost(j.key, j.pub, s.tw.addr, s.inv, "client", "racer", nil)
						mu.Lock()
						defer mu.Unlock()
						if err == nil {
							s.winners++
						} else {
							s.msgs = append(s.msgs, err.Error())
						}
					}()
				}
				_ = code
				wg.Wait()
				return nil
			})
			sc.Step(`^exactly one is admitted and the other is told the invite was already used$`, func() error {
				if s.winners != 1 || len(s.msgs) != 1 || !strings.Contains(s.msgs[0], "that invite was already used") {
					return fmt.Errorf("%d winner(s), refusals %q", s.winners, s.msgs)
				}
				return nil
			})

			// ----- MITM on approve -----
			transcript := func(clientKey, towerKey string, ce, te *ecdh.PrivateKey, cn, tn string) string {
				return joinsas.Phrase(joinsas.Transcript{ClientKey: clientKey, TowerKey: towerKey,
					ClientEph: hex.EncodeToString(ce.PublicKey().Bytes()), TowerEph: hex.EncodeToString(te.PublicKey().Bytes()),
					ClientNonce: cn, TowerNonce: tn})
			}
			var clientPhrase, ownerPhrase string
			sc.Step(`^a middle party substitutes its own keys during a pairing$`, func() error {
				cl, mid := newJoiner(), newJoiner()
				ceph, _ := ecdh.X25519().GenerateKey(rand.Reader)
				teph, _ := ecdh.X25519().GenerateKey(rand.Reader)
				meph, _ := ecdh.X25519().GenerateKey(rand.Reader)
				cn, tn, mn := joinsas.NewNonce(), joinsas.NewNonce(), joinsas.NewNonce()
				midTowerFP := towerpin.Fingerprint(mid.key.Public().(ed25519.PublicKey))
				// the client sees the middle as the Tower; the Tower sees the middle as the client
				clientPhrase = transcript(cl.pub, midTowerFP, ceph, meph, cn, mn)
				ownerPhrase = transcript(mid.pub, s.fp, meph, teph, mn, tn)
				return nil
			})
			sc.Step(`^the client's phrase and the owner's phrase differ$`, func() error {
				if clientPhrase == ownerPhrase {
					return fmt.Errorf("both screens showed %q", clientPhrase)
				}
				return nil
			})
			sc.Step(`^the owner approved despite different phrases$`, func() error {
				// the client's approved pin is the middle party's key, as an approval through it would leave
				mid, err := tower.Init(s.t.TempDir()+"/mid", tower.ModeStandalone)
				if err != nil {
					return err
				}
				c := loadConfig()
				c.LanTowers = []string{s.tw.addr}
				c.LanTowerPins = map[string]string{s.tw.addr: mustFP(mid)}
				return saveConfig(c)
			})
			sc.Step(`^the client's pin is the middle party's key$`, func() error {
				if loadConfig().LanTowerPins[s.tw.addr] == s.fp {
					return fmt.Errorf("the pin is the real Tower's")
				}
				return nil
			})
			sc.Step(`^the client refuses the real Tower's different key on the next direct contact, telling the user to re-join$`, func() error {
				lanmode.SetPins(loadConfig().LanTowerPins)
				before := s.tw.requests()
				_, status := client.LANDiscover(context.Background(), client.LANInputs{Towers: []string{s.tw.addr}, Timeout: 3 * time.Second})
				if status[0].State != client.LANTowerRefused || status[0].Detail != client.TowerKeyChanged(s.tw.addr) {
					return fmt.Errorf("status %+v", status[0])
				}
				if !strings.Contains(status[0].Detail, "join") {
					return fmt.Errorf("the refusal does not say to re-join: %q", status[0].Detail)
				}
				if s.tw.requests() != before {
					return fmt.Errorf("a request reached the real Tower over a mismatched pin")
				}
				return nil
			})

			// ----- flood -----
			sc.Step(`^thousands of join requests arrive from many addresses$`, func() error {
				plane := localplane.New(s.st)
				h := plane.Handler()
				s.codes = nil
				for i := 0; i < 1000; i++ {
					j := newJoiner()
					eph, _ := ecdh.X25519().GenerateKey(rand.Reader)
					nonce := joinsas.NewNonce()
					body, _ := json.Marshal(localplane.PairStartBody{Kind: "client", Name: "x",
						ClientEph: hex.EncodeToString(eph.PublicKey().Bytes()), Commit: joinsas.Commit(nonce)})
					from := fmt.Sprintf("192.168.%d.%d:5000", i/250, i%250+1)
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, signed(j, "/local/pair/start", body, from))
					if rec.Code == http.StatusOK {
						var a localplane.PairStartAnswer
						_ = json.Unmarshal(rec.Body.Bytes(), &a)
						rb, _ := json.Marshal(localplane.PairRevealBody{ID: a.ID, ClientNonce: nonce})
						h.ServeHTTP(httptest.NewRecorder(), signed(j, "/local/pair/reveal", rb, from))
					}
				}
				// one address sends more than its share
				for i := 0; i < localplane.PairPerAddressPerMinute+1; i++ {
					j := newJoiner()
					eph, _ := ecdh.X25519().GenerateKey(rand.Reader)
					body, _ := json.Marshal(localplane.PairStartBody{Kind: "client", Name: "x",
						ClientEph: hex.EncodeToString(eph.PublicKey().Bytes()), Commit: joinsas.Commit(joinsas.NewNonce())})
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, signed(j, "/local/pair/start", body, "192.168.9.9:5000"))
					s.codes = append(s.codes, rec.Code)
					if rec.Code == http.StatusTooManyRequests {
						s.raw = rec.Header().Get("Retry-After")
					}
				}
				s.other = s.st
				s.out = strconv.Itoa(len(plane.PairRequests()))
				// serving: an admitted client's discover still answers at once
				me := newJoiner()
				code := s.mint()
				if st, raw := s.post(me, code, "reader"); st != http.StatusOK {
					return fmt.Errorf("a client could not join during the flood: %d %s", st, raw)
				}
				start := time.Now()
				rec := httptest.NewRecorder()
				dr := signed(me, "/discover", nil, "192.168.200.1:5000")
				dr.Method = http.MethodGet
				_, ts, sig := protocol.SignRequestNonce(me.key, http.MethodGet, "/discover", nil, dr.Header.Get(protocol.HeaderNonce))
				dr.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
				dr.Header.Set(protocol.HeaderSig, sig)
				h.ServeHTTP(rec, dr)
				if rec.Code != http.StatusOK || time.Since(start) > time.Second {
					return fmt.Errorf("serving slowed or failed during the flood: %d after %v", rec.Code, time.Since(start))
				}
				return nil
			})
			sc.Step(`^at most the pending cap is shown to the owner$`, func() error {
				if n, _ := strconv.Atoi(s.out); n > localplane.PairPendingCap || n == 0 {
					return fmt.Errorf("%d requests shown, cap %d", n, localplane.PairPendingCap)
				}
				return nil
			})
			sc.Step(`^per-address rate limits refuse the rest with a retry-after$`, func() error {
				if s.codes[len(s.codes)-1] != http.StatusTooManyRequests || s.raw == "" {
					return fmt.Errorf("the address over its share got %v (Retry-After %q)", s.codes, s.raw)
				}
				return nil
			})
			sc.Step(`^serving clients is not slowed$`, func() error { return nil }) // measured in the When
			sc.Step(`^a join request is oversized, malformed or has unknown fields$`, func() error {
				code := s.mint()
				before := s.st.JoinGuesses()
				j := newJoiner()
				for _, body := range [][]byte{
					bytes.Repeat([]byte("x"), 2<<20),
					[]byte(`{"kind":`),
					[]byte(`{"kind":"client","proof":"` + joincode.Tower.Proof(code, "n", j.pub, s.fp) + `","name":"x","extra":1}`),
				} {
					nonce := protocol.NewNonce()
					status, _, err := towerPost(j.key, j.pub, s.tw.addr, s.fp, "/local/join", body, nonce, 5*time.Second)
					if err != nil {
						return err
					}
					s.codes = append(s.codes, status)
				}
				s.status = s.st.JoinGuesses() - before
				return nil
			})
			sc.Step(`^it is refused before any proof is computed$`, func() error {
				for _, c := range s.codes {
					if c != http.StatusUnauthorized {
						return fmt.Errorf("answers %v, want the uniform 401 each time", s.codes)
					}
				}
				if s.status != 0 {
					return fmt.Errorf("%d proof check(s) were spent on malformed requests", s.status)
				}
				if s.inviteSpent() {
					return fmt.Errorf("a malformed request spent the invite")
				}
				return nil
			})

			// ----- downgrade -----
			plainOnly := func() string {
				s.plain = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					s.plainN++
					w.WriteHeader(http.StatusUnauthorized)
				}))
				s.t.Cleanup(s.plain.Close)
				return strings.TrimPrefix(s.plain.URL, "http://")
			}
			sc.Step(`^the client pinned "lab"$`, func() error {
				c := loadConfig()
				c.LanTowers = []string{s.tw.addr}
				c.LanTowerPins = map[string]string{s.tw.addr: s.fp}
				return saveConfig(c)
			})
			sc.Step(`^an attacker makes the TLS port fail so only plain http answers$`, func() error {
				addr := plainOnly()
				c := loadConfig()
				c.LanTowers = []string{addr}
				c.LanTowerPins = map[string]string{addr: s.fp}
				return saveConfig(c)
			})
			sc.Step(`^the client refuses and does not send over plain http$`, func() error {
				c := loadConfig()
				lanmode.SetPins(c.LanTowerPins)
				_, status := client.LANDiscover(context.Background(), client.LANInputs{Towers: c.LanTowers, Timeout: 3 * time.Second})
				if status[0].State == client.LANTowerAnswering {
					return fmt.Errorf("a pinned Tower answered over plain http")
				}
				if s.plainN != 0 {
					return fmt.Errorf("%d request(s) went over plain http", s.plainN)
				}
				return nil
			})
			sc.Step(`^the client joins with an invite and only plain http answers$`, func() error {
				s.mint()
				in := s.inv
				in.Addrs = []string{plainOnly()}
				s.joinMe(in)
				return nil
			})
			sc.Step(`^the join is refused, because an invite join always verifies the fingerprint over TLS$`, func() error {
				if s.err == nil {
					return fmt.Errorf("the join succeeded over plain http")
				}
				if s.plainN != 0 {
					return fmt.Errorf("%d request(s) went over plain http", s.plainN)
				}
				return nil
			})
			sc.Step(`^admin\.sock is a local socket in the data directory only, never a TCP listener$`, func() error {
				ln, err := toweradmin.Listen(s.dir)
				if err != nil {
					return err
				}
				defer func() { _ = ln.Close(); toweradmin.Remove(s.dir) }()
				if ln.Addr().Network() != "unix" || ln.Addr().String() != toweradmin.Path(s.dir) {
					return fmt.Errorf("the admin socket listens on %s %s", ln.Addr().Network(), ln.Addr())
				}
				return nil
			})

			// ----- addresses -----
			sc.Step(`^an invite whose address is public$`, func() error {
				s.mint()
				s.inv.Addrs = []string{"203.0.113.9:8787"}
				return nil
			})
			sc.Step(`^the client runs "roger join" with it$`, func() error {
				start := time.Now()
				s.joinMe(s.inv)
				if time.Since(start) > time.Second {
					return fmt.Errorf("the refusal took %v: a connection was attempted", time.Since(start))
				}
				return nil
			})
			sc.Step(`^it is refused by the local-address rule with no connection made$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "is not a local address") {
					return fmt.Errorf("join = %v", s.err)
				}
				return nil
			})
			sc.Step(`^an invite whose address is a hostname resolving to a public address$`, func() error {
				s.mint()
				s.inv.Addrs = []string{"lab.example:8787"}
				joinResolve = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.9")}, nil }
				s.joinMe(s.inv)
				return nil
			})
			sc.Step(`^it is refused, re-checked at connect time, as lanmode requires$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "is not a local address") {
					return fmt.Errorf("join = %v", s.err)
				}
				// and a name that answers private then public is connected at the private answer only
				var calls int
				joinResolve = func(context.Context, string) ([]net.IP, error) {
					calls++
					if calls == 1 {
						host, _, _ := net.SplitHostPort(s.tw.addr)
						return []net.IP{net.ParseIP(host)}, nil
					}
					return []net.IP{net.ParseIP("203.0.113.9")}, nil
				}
				_, port, _ := net.SplitHostPort(s.tw.addr)
				code := s.mint()
				s.inv = joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{"lab.example:" + port}, FP: s.fp, Code: code}
				s.joinMe(s.inv)
				if s.err != nil || calls != 1 || !s.meAdmitted() {
					return fmt.Errorf("join over a flipping name = %v after %d lookup(s)", s.err, calls)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: []string{adversarialFeature}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/adversarial.feature failed")
	}
}

// advertise and candidates drive the dial's real candidate path with one heard advert.
func (s *adversarialBDD) advertise(ip net.IP, port int, name string) error {
	prev := lanBrowseTowers
	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) {
		return wire.BrowseResult{Adverts: []wire.Advert{{Kind: wire.TowerKind, IP: ip, Port: port, Name: name}}, Seen: 1}, nil
	}
	s.t.Cleanup(func() { lanBrowseTowers = prev })
	return nil
}

func (s *adversarialBDD) candidates() []client.LANTowerStatus {
	end := edgeScans.Begin(scanLAN)
	defer end()
	return lanCandidates(context.Background(), loadConfig().LanTowers)
}
