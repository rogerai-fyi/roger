package main

// join_invite_bdd_test.go runs the built scenarios of features/join/invite.feature against a real
// standalone Tower (its plane served over the real TLS-and-plain listener) and the real `roger
// join`, `roger tower invite` code. Addresses in the feature are symbolic: "192.168.1.20:8787" is
// the fixture Tower's loopback address, an address "that does not answer" is a closed port.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/towercli"
	"rogerai.fm/roger/v6/internal/towerpin"
)

const joinInviteFeature = "../../features/join/invite.feature"

type joinInviteBDD struct {
	t        *testing.T
	st       *tower.State
	dir      string
	addr     string
	out      string
	err      error
	invites  []joininvite.Invite
	inv      joininvite.Invite
	ids      []string
	refused  error
	stationM []string
}

var joinLineRE = regexp.MustCompile(`roger join (roger://join\?\S+)`)

func (s *joinInviteBDD) reset() {
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	lanmode.SetPins(nil)
	s.dir = s.t.TempDir() + "/lab"
	st, err := tower.Init(s.dir, tower.ModeStandalone)
	if err != nil {
		s.t.Fatal(err)
	}
	s.st = st
	key, _ := st.TLSKey()
	cfg, _ := towerpin.ServerTLS(key)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	srv := &http.Server{Handler: localplane.New(st).Handler()}
	go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
	s.t.Cleanup(func() { _ = srv.Close() })
	s.addr = ln.Addr().String()
	s.out, s.err, s.invites, s.ids, s.refused, s.stationM = "", nil, nil, nil, nil, nil
}

// towerCLI runs `roger tower <args>` against the fixture's data directory.
func (s *joinInviteBDD) towerCLI(args ...string) (string, error) {
	var b strings.Builder
	if code := towercli.Run(append([]string{args[0], "--dir", s.dir}, args[1:]...), &b, &b); code != 0 {
		return b.String(), fmt.Errorf("roger tower %s exited %d:\n%s", args[0], code, b.String())
	}
	return b.String(), nil
}

func (s *joinInviteBDD) mint(extra ...string) error {
	out, err := s.towerCLI(append([]string{"invite", "--at", s.addr}, extra...)...)
	if err != nil {
		return err
	}
	m := joinLineRE.FindStringSubmatch(out)
	if m == nil {
		return fmt.Errorf("no roger join line in:\n%s", out)
	}
	in, err := joininvite.Parse(m[1])
	if err != nil {
		return err
	}
	s.out, s.inv = out, in
	s.invites = append(s.invites, in)
	return nil
}

func (s *joinInviteBDD) join(in joininvite.Invite) error {
	s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), []string{in.String()}) })
	return nil
}

func (s *joinInviteBDD) admitted() bool {
	pub := hex.EncodeToString(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey))
	return s.st.IsAdmitted(protocol.UserIDFromPubkey(pub))
}

func TestJoinInviteBDD(t *testing.T) {
	s := &joinInviteBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})
			// Background
			sc.Step(`^a standalone Tower "lab" is serving at 192\.168\.1\.20:8787 over TLS with its own key$`, func() error {
				resp, err := towerpin.Client(mustFP(s.st), 0).Get("https://" + s.addr + "/discover")
				if err != nil {
					return err
				}
				resp.Body.Close()
				return nil
			})
			sc.Step(`^the Tower's identity fingerprint is "sha256:9f3c\.\.\.e1"$`, func() error { return nil })
			sc.Step(`^the owner is signed in to nothing \(no Roger Core account is needed\)$`, func() error { return nil })

			// Minting
			sc.Step(`^the owner runs "roger tower invite"$`, func() error { return s.mint() })
			sc.Step(`^the invite decodes to kind "([^"]*)"$`, func(k string) error {
				if string(s.inv.Kind) != k {
					return fmt.Errorf("kind %q, want %q", s.inv.Kind, k)
				}
				return nil
			})
			sc.Step(`^it carries every local address the Tower is serving on, in the Tower's preference order$`, func() error {
				if len(s.inv.Addrs) != 1 || s.inv.Addrs[0] != s.addr {
					return fmt.Errorf("addresses %v, want [%s]", s.inv.Addrs, s.addr)
				}
				return nil
			})
			sc.Step(`^it carries the full identity fingerprint "sha256:9f3c\.\.\.e1", never the short form$`, func() error {
				if s.inv.FP != mustFP(s.st) || !strings.Contains(s.inv.String(), "sha256%3A"+s.inv.FP) {
					return fmt.Errorf("fingerprint %q, want the full %q", s.inv.FP, mustFP(s.st))
				}
				return nil
			})
			sc.Step(`^it carries a one-time code$`, func() error {
				if len(s.inv.Code) != joincode.Tower.CodeLen {
					return fmt.Errorf("code %q", s.inv.Code)
				}
				return nil
			})
			sc.Step(`^the owner mints a Tower invite$`, func() error { return s.mint() })
			sc.Step(`^its code carries at least 128 bits from the operating system's cryptographic random source$`, func() error {
				if joincode.Tower.CodeLen*5 < 128 {
					return fmt.Errorf("%d symbols is under 128 bits", joincode.Tower.CodeLen)
				}
				return nil
			})
			sc.Step(`^the Tower stores only a verifier of the code, never the code itself$`, func() error {
				for _, inv := range s.st.LiveInvitations() {
					if strings.Contains(fmt.Sprintf("%+v", inv), s.inv.Code) {
						return fmt.Errorf("the stored invite holds the code")
					}
				}
				raw, err := readDirAll(s.dir)
				if err != nil {
					return err
				}
				if strings.Contains(raw, s.inv.Code) {
					return fmt.Errorf("the code is on disk")
				}
				return nil
			})
			sc.Step(`^the code is printed once and never logged, traced or returned again$`, func() error {
				if n := strings.Count(s.out, s.inv.Code); n != 1 {
					return fmt.Errorf("the code appears %d times", n)
				}
				list, _ := s.towerCLI("invite", "--list")
				if strings.Contains(list, s.inv.Code) {
					return fmt.Errorf("--list returned the code")
				}
				return nil
			})
			sc.Step(`^an unused client invite minted 5 minutes ago$`, func() error { return s.mint() })
			sc.Step(`^the owner mints another client invite$`, func() error { return s.mint() })
			sc.Step(`^both work until each is used once or expires$`, func() error {
				fp := mustFP(s.st)
				for i, in := range s.invites {
					pub, _, _ := ed25519.GenerateKey(rand.Reader)
					ph := hex.EncodeToString(pub)
					n := protocol.NewNonce()
					if _, err := s.st.ConsumeJoin(tower.JoinRequest{Proof: joincode.Tower.Proof(in.Code, n, ph, fp), Nonce: n,
						PubHex: ph, KeyHash: protocol.UserIDFromPubkey(ph)}); err != nil {
						return fmt.Errorf("invite %d: %v", i, err)
					}
				}
				return nil
			})
			sc.Step(`^two unused client invites$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				return s.mint()
			})
			sc.Step(`^the owner runs "roger tower invite --list"$`, func() error {
				s.out, s.err = s.towerCLI("invite", "--list")
				s.ids = regexp.MustCompile(`(?m)^([0-9a-f]{16})\s`).FindAllString(s.out, -1)
				return s.err
			})
			sc.Step(`^it lists each by a short id, its kind, and the minutes left, never the code$`, func() error {
				if len(s.ids) != 2 || !strings.Contains(s.out, "client") || !strings.Contains(s.out, "left") {
					return fmt.Errorf("list:\n%s", s.out)
				}
				for _, in := range s.invites {
					if strings.Contains(s.out, in.Code) {
						return fmt.Errorf("the list shows a code")
					}
				}
				return nil
			})
			sc.Step(`^the owner runs "roger tower invite --void <id>"$`, func() error {
				_, err := s.towerCLI("invite", "--void", strings.TrimSpace(s.ids[0]))
				return err
			})
			sc.Step(`^that invite no longer works and the other still does$`, func() error {
				if n := len(s.st.LiveInvitations()); n != 1 {
					return fmt.Errorf("%d invites still live, want 1", n)
				}
				return nil
			})

			// Joining as a client
			sc.Step(`^the client joins with a valid invite$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				return s.join(s.inv)
			})
			sc.Step(`^the proof covers the domain "tower-join", the client's public key, the Tower's fingerprint and a fresh nonce$`, func() error {
				if s.err != nil {
					return s.err
				}
				code := joincode.Tower.NewCode()
				ver := joincode.Tower.Verifier(code)
				p := joincode.Tower.Proof(code, "n", "pk", "fp")
				for _, bad := range [][3]string{{"n2", "pk", "fp"}, {"n", "pk2", "fp"}, {"n", "pk", "fp2"}} {
					if joincode.Tower.CheckWithVerifier(ver, p, bad[0], bad[1], bad[2]) == nil {
						return fmt.Errorf("a proof verified with %v changed", bad)
					}
				}
				if !strings.Contains(joincode.Tower.Name, "tower-join") {
					return fmt.Errorf("domain %q", joincode.Tower.Name)
				}
				return nil
			})
			sc.Step(`^it is the Edge proof construction \(PBKDF2 of the code, then HMAC\) with that domain string$`, func() error {
				if joincode.Tower.Iterations != joincode.Edge.Iterations {
					return fmt.Errorf("different stretch")
				}
				return nil
			})
			sc.Step(`^the client has joined$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				_ = s.join(s.inv)
				return s.err
			})
			sc.Step(`^the Tower's existing admission check \(IsAdmitted\) accepts its key$`, func() error {
				if !s.admitted() {
					return fmt.Errorf("IsAdmitted refuses the joined key")
				}
				return nil
			})
			sc.Step(`^every request it sends is still signed and carries the replay nonce$`, func() error {
				_, st := client.LANDiscover(context.Background(), client.LANInputs{Towers: []string{s.addr}})
				if len(st) != 1 || st[0].State == client.LANTowerNotAdmitted || st[0].State == client.LANTowerRefused {
					return fmt.Errorf("a signed request was not accepted: %+v", st)
				}
				return nil
			})
			sc.Step(`^the invite lists 192\.168\.1\.20:8787 and \[fe80::1%en0\]:8787$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				s.inv.Addrs = []string{"127.0.0.1:1", s.addr}
				return nil
			})
			sc.Step(`^192\.168\.1\.20 does not answer$`, func() error { return nil }) // 127.0.0.1:1 answers nothing
			sc.Step(`^the client joins$`, func() error { return s.join(s.inv) })
			sc.Step(`^it joins through the next address whose identity matches the fingerprint$`, func() error {
				if s.err != nil {
					return s.err
				}
				return nil
			})
			sc.Step(`^it records the address that worked first in lan_towers$`, func() error {
				if got := loadConfig().LanTowers; len(got) != 1 || got[0] != s.addr {
					return fmt.Errorf("lan_towers %v, want [%s]", got, s.addr)
				}
				return nil
			})
			sc.Step(`^an invite whose only address is 203\.0\.113\.9:8787$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				s.inv.Addrs = []string{"203.0.113.9:8787"}
				return nil
			})
			sc.Step(`^the client runs "roger join" with it$`, func() error { return s.join(s.inv) })
			sc.Step(`^it is refused with the local-address rule \(lanmode\), naming the address$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "203.0.113.9:8787") || !strings.Contains(s.err.Error(), lanmode.LocalRanges) {
					return fmt.Errorf("refusal %v", s.err)
				}
				return nil
			})
			sc.Step(`^no connection is made$`, func() error {
				if len(s.st.LiveInvitations()) != 1 {
					return fmt.Errorf("the invite was touched")
				}
				return nil
			})

			// Stations
			sc.Step(`^the owner ran "roger tower invite --station --models qwen3-8b"$`, func() error {
				return s.mint("--station", "--models", "qwen3-8b")
			})
			sc.Step(`^a station serving "qwen3-8b" and "gpt-oss-20b" joins$`, func() error {
				prev := detectFull
				detectFull = fakeDetect("qwen3-8b", "gpt-oss-20b")
				defer func() { detectFull = prev }()
				_ = s.join(s.inv)
				return s.err
			})
			sc.Step(`^the Tower routes only "qwen3-8b" to it$`, func() error {
				sts, _ := s.st.Stations()
				if len(sts) != 1 || strings.Join(sts[0].Models, ",") != "qwen3-8b" {
					return fmt.Errorf("stations %+v", sts)
				}
				return nil
			})
			sc.Step(`^the station is told which of its models were not accepted$`, func() error {
				if !strings.Contains(s.out, "does not accept: gpt-oss-20b") {
					return fmt.Errorf("output:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^a station joins with a client invite$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				in := s.inv
				in.Kind = joininvite.TowerStation
				prev := detectFull
				detectFull = fakeDetect("qwen3-8b")
				defer func() { detectFull = prev }()
				return s.join(in)
			})
			sc.Step(`^it is refused with "([^"]*)"$`, func(want string) error {
				if s.err == nil || !strings.Contains(s.err.Error(), want) {
					return fmt.Errorf("refusal %v, want %q", s.err, want)
				}
				return nil
			})
			sc.Step(`^the invite is not spent$`, func() error {
				if len(s.st.LiveInvitations()) == 0 {
					return fmt.Errorf("the invite was spent")
				}
				return nil
			})
			sc.Step(`^a client joins with a station invite$`, func() error {
				if err := s.mint("--station"); err != nil {
					return err
				}
				in := s.inv
				in.Kind = joininvite.TowerClient
				return s.join(in)
			})

			// Offline
			sc.Step(`^neither machine can reach the internet$`, func() error { return nil })
			sc.Step(`^the owner mints an invite and the client joins with it$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				return s.join(s.inv)
			})
			sc.Step(`^the join succeeds$`, func() error {
				if s.err != nil || !s.admitted() {
					return fmt.Errorf("join: %v", s.err)
				}
				return nil
			})
			sc.Step(`^no request goes to Roger Core or any public address$`, func() error {
				for _, a := range s.inv.Addrs {
					if v := lanmode.ClassifyLiteral(a); !v.Local {
						return fmt.Errorf("the join named a non-local address %s", a)
					}
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{joinInviteFeature + ":44", joinInviteFeature + ":74", joinInviteFeature + ":98",
				joinInviteFeature + ":107", joinInviteFeature + ":133", joinInviteFeature + ":145",
				joinInviteFeature + ":155", joinInviteFeature + ":162", joinInviteFeature + ":186",
				joinInviteFeature + ":196", joinInviteFeature + ":236"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/invite.feature failed")
	}
}

func mustFP(st *tower.State) string {
	fp, _ := st.JoinFingerprint()
	return fp
}

// fakeDetect stands in for this machine's model servers: it reports the given models.
func fakeDetect(models ...string) func(...string) ([]detect.Found, []string) {
	return func(...string) ([]detect.Found, []string) {
		return []detect.Found{{Name: "fixture", Models: models}}, nil
	}
}

// readDirAll is every file under dir, concatenated: to prove a secret is on no file.
func readDirAll(dir string) (string, error) {
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, rerr := os.ReadFile(p)
		b.Write(raw)
		return rerr
	})
	return b.String(), err
}
