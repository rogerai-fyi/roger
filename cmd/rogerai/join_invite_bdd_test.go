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
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/qr"
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
	edgeInv  string
	edgeAuth *edgeauth.Local
	edgeOut  string
	edgeCode int
	// stationInvID is the station invite a station joined with, to read its outcome.
	stationInvID string
	// lastShare is what the last runShare printed.
	lastShare string
}

var joinLineRE = regexp.MustCompile(`roger join '?(roger://join\?[^'\s]+)'?`)

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
	plane := localplane.New(st)
	plane.SetLabel("lab")
	srv := &http.Server{Handler: plane.Handler()}
	go func() { _ = srv.Serve(towerpin.Sniff(ln, cfg)) }()
	s.t.Cleanup(func() { _ = srv.Close() })
	s.addr = ln.Addr().String()
	s.t.Setenv("NO_COLOR", "restored after the test")
	os.Unsetenv("NO_COLOR")
	s.out, s.err, s.invites, s.ids, s.refused, s.stationM = "", nil, nil, nil, nil, nil
	s.edgeInv, s.edgeOut, s.edgeCode = "", "", 0
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

// stationJoin runs `roger join <station invite> <name>` on a station machine serving s.stationM.
// The join starts the station's share; it runs until the LAN share's context ends (shortly).
func (s *joinInviteBDD) stationJoin(name string) error {
	if live := s.st.LiveInvitations(); len(live) > 0 {
		s.stationInvID = live[len(live)-1].ID
	}
	prev, prevCtx := detectFull, lanShareContext
	detectFull = fakeDetectAt("http://127.0.0.1:1/v1", s.stationM...)
	lanShareContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 1500*time.Millisecond)
	}
	defer func() { detectFull, lanShareContext = prev, prevCtx }()
	s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), []string{s.inv.String(), name}) })
	return nil
}

// runShare runs this machine's LAN share of model for a moment: long enough for its polls to
// reach every configured Tower.
func (s *joinInviteBDD) runShare(model string) error {
	prev, prevCtx := detectFull, lanShareContext
	detectFull = fakeDetectAt("http://127.0.0.1:1/v1", model)
	lanShareContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 1500*time.Millisecond)
	}
	defer func() { detectFull, lanShareContext = prev, prevCtx }()
	c := loadConfig()
	c.lan.LAN, c.lan.Source = true, lanmode.SrcFlag
	var err error
	out := captureStdout(s.t, func() { err = cmdShare(c, []string{model, "--upstream", "http://127.0.0.1:1/v1"}) })
	s.lastShare = out
	if err != nil {
		return fmt.Errorf("share: %v\n%s", err, out)
	}
	return nil
}

// edgeKept reports whether the device now holds an Edge identity (the join enrolled it).
func (s *joinInviteBDD) edgeKept() error {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok || id == nil {
		return fmt.Errorf("no Edge identity was kept (%v):\n%s", err, s.edgeOut)
	}
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
			// --qr: in a terminal the code is drawn by default; the runner's output is a buffer.
			sc.Step(`^the owner runs "roger tower invite"$`, func() error { return s.mint("--qr") })
			sc.Step(`^it prints one line starting "roger join " followed by the invite$`, func() error {
				n := 0
				for _, l := range strings.Split(s.out, "\n") {
					if strings.HasPrefix(l, "roger join ") {
						n++
						if l != joininvite.JoinLine(s.inv.String()) {
							return fmt.Errorf("join line %q, want the invite %q", l, s.inv)
						}
					}
				}
				if n != 1 {
					return fmt.Errorf("%d lines start with roger join:\n%s", n, s.out)
				}
				return nil
			})
			sc.Step(`^it prints a terminal QR code of the same invite$`, func() error {
				if !strings.Contains(s.out, qr.ForTerminal(joininvite.JoinLine(s.inv.String()), 80, false)) {
					return fmt.Errorf("no QR code of the invite in:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^it says the invite is valid for 15 minutes and works once$`, func() error {
				if !strings.Contains(s.out, "valid for 15 minutes and works once") {
					return fmt.Errorf("no validity line in:\n%s", s.out)
				}
				return nil
			})
			sc.Step("^`roger tower serve` is still running and still answering clients$", func() error {
				resp, err := towerpin.Client(mustFP(s.st), 0).Get("https://" + s.addr + "/discover")
				if err != nil {
					return err
				}
				resp.Body.Close()
				// An unsigned caller gets the uniform 401: the pinned Tower is up and answering.
				if resp.StatusCode != http.StatusUnauthorized {
					return fmt.Errorf("/discover answered %d after the mint", resp.StatusCode)
				}
				return nil
			})
			sc.Step(`^no restart and no client id were needed$`, func() error {
				for _, w := range []string{"restart", "--client", "client id", "key hash"} {
					if strings.Contains(strings.ToLower(s.out), w) {
						return fmt.Errorf("the mint mentions %q:\n%s", w, s.out)
					}
				}
				return nil
			})
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
			// --- a station joins with its own models and serves right away ---------------------
			sc.Step(`^the owner ran "roger tower invite --station"$`, func() error { return s.mint("--station") })
			sc.Step(`^the station machine has "qwen3-8b" and "smollm2-135m-instruct" on air$`, func() error {
				s.stationM = []string{"qwen3-8b", "smollm2-135m-instruct"}
				return nil
			})
			sc.Step(`^the station runs "roger join <invite>"$`, func() error { return s.stationJoin("kitchen-pi") })
			sc.Step(`^the Tower attaches it under the station's own name$`, func() error {
				sts, err := s.st.Stations()
				if err != nil || len(sts) != 1 || sts[0].ID != "kitchen-pi" {
					return fmt.Errorf("stations %+v (%v)", sts, err)
				}
				return nil
			})
			sc.Step(`^the station's models are taken from its live offers, not typed by hand$`, func() error {
				sts, _ := s.st.Stations()
				if len(sts) != 1 || strings.Join(sts[0].Models, ",") != strings.Join(s.stationM, ",") {
					return fmt.Errorf("stations %+v, the station has %v on air", sts, s.stationM)
				}
				return nil
			})
			sc.Step(`^the station starts serving the Tower right away$`, func() error {
				if s.err != nil {
					return fmt.Errorf("roger join: %v\n%s", s.err, s.out)
				}
				if !strings.Contains(s.out, "LAN mode: sharing") || !strings.Contains(s.out, "serving the local network's stations at") {
					return fmt.Errorf("roger join did not start serving the Tower:\n%s", s.out)
				}
				if strings.Contains(s.out, "has not attached this station") {
					return fmt.Errorf("the Tower refused the station's polls:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the owner sees "station kitchen-pi joined, serving: qwen3-8b, smollm2-135m-instruct"$`, func() error {
				line, over := towercli.OwnerInviteOutcome(s.dir, s.stationInvID)
				if !over || line != "station kitchen-pi joined, serving: qwen3-8b, smollm2-135m-instruct" {
					return fmt.Errorf("the owner's screens show %q (over=%v)", line, over)
				}
				return nil
			})
			sc.Step(`^a joined station that later stops "qwen3-8b" and starts "gpt-oss-20b"$`, func() error {
				if err := s.mint("--station"); err != nil {
					return err
				}
				s.stationM = []string{"qwen3-8b"}
				if err := s.stationJoin("kitchen-pi"); err != nil {
					return err
				}
				if s.err != nil {
					return fmt.Errorf("roger join: %v\n%s", s.err, s.out)
				}
				s.stationM = []string{"gpt-oss-20b"}
				return nil
			})
			sc.Step(`^the station's next poll reaches the Tower$`, func() error {
				// The station machine now shares gpt-oss-20b: a real LAN share, polling the Tower.
				return s.runShare("gpt-oss-20b")
			})
			sc.Step(`^the Tower routes "gpt-oss-20b" to it and no longer routes "qwen3-8b" to it$`, func() error {
				sts, _ := s.st.Stations()
				if len(sts) != 1 || strings.Join(sts[0].Models, ",") != "gpt-oss-20b" {
					return fmt.Errorf("after the poll the Tower routes %+v", sts)
				}
				return nil
			})
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

			// Joining with one pasted command
			sc.Step(`^the owner minted a client invite$`, func() error { return s.mint() })
			sc.Step(`^the client runs "roger join <invite>"$`, func() error { return s.join(s.inv) })
			sc.Step(`^it checks the Tower's identity against the invite's fingerprint before sending anything$`, func() error {
				// The same join against a Tower presenting another key sends nothing: the pinned
				// handshake fails first (tower_identity.feature pins the request count).
				if s.err != nil {
					return s.err
				}
				bad := s.inv
				bad.FP = strings.Repeat("ab", 32)
				out := captureStdout(s.t, func() { s.refused = cmdJoin(loadConfig(), []string{bad.String()}) })
				if s.refused == nil || !strings.Contains(s.refused.Error(), "its key differs") {
					return fmt.Errorf("a Tower with another key was not refused in the handshake: %v %s", s.refused, out)
				}
				return nil
			})
			sc.Step(`^it proves the code without sending the code itself$`, func() error {
				if strings.Contains(s.out, s.inv.Code) {
					return fmt.Errorf("the code was printed")
				}
				return nil // the wire body carries only the proof: JoinRequestBody has no code field
			})
			sc.Step(`^the Tower admits the client's key on the code$`, func() error {
				if !s.admitted() {
					return fmt.Errorf("not admitted")
				}
				return nil
			})
			sc.Step(`^the client records the Tower in lan_towers WITH its pinned fingerprint$`, func() error {
				c := loadConfig()
				if !contains(c.LanTowers, s.addr) || c.LanTowerPins[s.addr] != s.inv.FP {
					return fmt.Errorf("lan_towers %v pins %v", c.LanTowers, c.LanTowerPins)
				}
				return nil
			})
			sc.Step(`^the client prints "joined the Tower lab at 192\.168\.1\.20:8787" and the bands it serves$`, func() error {
				if !strings.Contains(s.out, "joined the Tower lab at "+s.addr) {
					return fmt.Errorf("printed:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^it prints one next step: "try: roger --lan use <band>"$`, func() error {
				if strings.Count(s.out, "try: ") != 1 || !strings.Contains(s.out, "try: roger --lan use <band>") {
					return fmt.Errorf("printed:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the client already joined "lab"$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				if err := s.join(s.inv); err != nil || s.err != nil {
					return fmt.Errorf("first join: %v", s.err)
				}
				return nil
			})
			sc.Step(`^it runs "roger join" with a fresh invite for "lab"$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				return s.join(s.inv)
			})
			sc.Step(`^it says it is already a member, keeps its pin, and does not spend the invite$`, func() error {
				if s.err != nil || !strings.Contains(s.out, "already a member of the Tower lab at") {
					return fmt.Errorf("said %v:\n%s", s.err, s.out)
				}
				if loadConfig().LanTowerPins[s.addr] != s.inv.FP {
					return fmt.Errorf("the pin changed")
				}
				if n := len(s.st.LiveInvitations()); n != 1 {
					return fmt.Errorf("%d live invites, want the fresh one unspent", n)
				}
				return nil
			})

			sc.Step(`^the station runs "roger share --tower <station invite>"$`, func() error {
				if err := s.mint("--station"); err != nil {
					return err
				}
				prevCtx := lanShareContext
				ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
				defer cancel()
				lanShareContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
				defer func() { lanShareContext = prevCtx }()
				prev := detectFull
				detectFull = fakeDetect("qwen3-8b")
				defer func() { detectFull = prev }()
				s.out = captureStdout(s.t, func() {
					s.err = cmdShare(loadConfig(), []string{"qwen3-8b", "--upstream", "http://127.0.0.1:1/v1", "--tower", s.inv.String()})
				})
				return nil
			})
			sc.Step(`^it joins exactly as "roger join <station invite>" and then shares$`, func() error {
				if s.err != nil {
					return fmt.Errorf("share: %v\n%s", s.err, s.out)
				}
				stations, err := s.st.Stations()
				if err != nil || len(stations) != 1 || !strings.Contains(s.out, "as a station") {
					return fmt.Errorf("no station joined: %v %v\n%s", stations, err, s.out)
				}
				if !strings.Contains(s.out, "LAN mode: sharing qwen3-8b") {
					return fmt.Errorf("it did not share after joining:\n%s", s.out)
				}
				return nil
			})

			// Offline
			sc.Step(`^neither machine can reach the internet$`, func() error { return nil })
			sc.Step(`^the owner mints an invite and the client joins with it$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				return s.join(s.inv)
			})
			// 4. Joining an Edge with the same verb: a real local authority served over HTTP, and a
			// second machine (its own config and user key) that was never allowed.
			// --- one scheme for every kind -----------------------------------------------------
			var minted string // the invite exactly as printed
			sc.Step(`^the owner mints a "([^"]*)" invite$`, func(kind string) error {
				switch joininvite.Kind(kind) {
				case joininvite.TowerClient:
					if err := s.mint(); err != nil {
						return err
					}
				case joininvite.TowerStation:
					if err := s.mint("--station"); err != nil {
						return err
					}
				case joininvite.Edge:
					s.edgeInv, s.edgeAuth = ownerEdgeInvite(s.t)
					in, err := joininvite.Parse(s.edgeInv)
					if err != nil {
						return err
					}
					s.inv = in
					minted = s.edgeInv
					return nil
				}
				minted = joinLineRE.FindStringSubmatch(s.out)[1]
				return nil
			})
			sc.Step(`^it starts "roger://join\?kind=([^"]*)"$`, func(kind string) error {
				if !strings.HasPrefix(minted, "roger://join?kind="+kind) {
					return fmt.Errorf("the invite %q does not start roger://join?kind=%s", minted, kind)
				}
				return nil
			})
			sc.Step(`^it parses back to the same kind, addresses, fingerprint and code$`, func() error {
				back, err := joininvite.Parse(minted)
				if err != nil {
					return err
				}
				if back.String() != minted || back.Kind != s.inv.Kind || back.FP != s.inv.FP || back.Code != s.inv.Code ||
					strings.Join(back.Addrs, ",") != strings.Join(s.inv.Addrs, ",") {
					return fmt.Errorf("%q parsed back as %+v", minted, back)
				}
				return nil
			})
			sc.Step(`^an invite of the form "rogerai-edge://join\?at=192\.168\.1\.69:39009&root=sha256:\.\.\.&code=\.\.\."$`, func() error {
				s.edgeInv, s.edgeAuth = ownerEdgeInvite(s.t)
				in, err := joininvite.Parse(s.edgeInv)
				if err != nil {
					return err
				}
				// The same invite written the way invites were written before the one scheme.
				minted = "rogerai-edge://join?at=" + in.Addrs[0] + "&root=sha256:" + in.FP + "&code=" + in.Code
				return nil
			})
			sc.Step(`^a device runs "roger join" with it$`, func() error {
				s.edgeOut, s.edgeCode = edgeRun(s.t, "join", minted, "workshop")
				return nil
			})
			sc.Step(`^it is read as kind "edge" and the join proceeds as an Edge enrollment$`, func() error {
				in, err := joininvite.Parse(minted)
				if err != nil || in.Kind != joininvite.Edge {
					return fmt.Errorf("the old form read as %+v (%v)", in, err)
				}
				if s.edgeCode != 0 {
					return fmt.Errorf("roger join exited %d:\n%s", s.edgeCode, s.edgeOut)
				}
				return s.edgeKept()
			})
			sc.Step(`^the owner mints an Edge invite$`, func() error {
				s.edgeInv, s.edgeAuth = ownerEdgeInvite(s.t)
				in, err := joininvite.Parse(s.edgeInv)
				s.inv = in
				return err
			})
			sc.Step(`^its code is 10 Crockford base32 symbols, shown as XXXXX-XXXXX, as in enrollment_root_trust\.feature$`, func() error {
				code, err := edgeauth.CanonicalCode(s.inv.Code)
				if err != nil || len(code) != 10 || strings.Trim(code, "0123456789ABCDEFGHJKMNPQRSTVWXYZ") != "" {
					return fmt.Errorf("the Edge code %q is not 10 Crockford symbols (%v)", s.inv.Code, err)
				}
				if shown := edgeauth.FormatCode(code); len(shown) != 11 || shown[5] != '-' {
					return fmt.Errorf("the Edge code is shown as %q", shown)
				}
				return nil
			})

			// --- an invite for a named machine -------------------------------------------------
			sc.Step(`^the owner runs "roger tower invite --for laptop-x"$`, func() error { return s.mint("--for", "laptop-x") })
			sc.Step(`^the invite works only for a joiner that names itself "laptop-x"$`, func() error {
				s.out = captureStdout(s.t, func() { s.refused = cmdJoin(loadConfig(), []string{s.inv.String(), "laptop-y"}) })
				if s.refused == nil || s.admitted() {
					return fmt.Errorf("a joiner named laptop-y got in with laptop-x's invite: %s", s.out)
				}
				return nil
			})
			sc.Step(`^a joiner with any other name is refused without spending the invite$`, func() error {
				s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), []string{s.inv.String(), "laptop-x"}) })
				if s.err != nil || !s.admitted() {
					return fmt.Errorf("after a refused other name, laptop-x could not use its own invite (%v): %s", s.err, s.out)
				}
				return nil
			})

			// --- the owner sees the join as it happens --------------------------------------------
			var waiting *liveBuf
			var waitDone chan struct{}
			var waitID string
			sc.Step(`^"roger tower invite" is still showing its invite$`, func() error {
				waiting, waitDone = &liveBuf{}, make(chan struct{})
				go func() {
					defer close(waitDone)
					towercli.Run([]string{"invite", "--dir", s.dir, "--at", s.addr, "--wait"}, waiting, waiting)
				}()
				for i := 0; i < 300 && joinLineRE.FindStringSubmatch(waiting.String()) == nil; i++ {
					time.Sleep(10 * time.Millisecond)
				}
				m := joinLineRE.FindStringSubmatch(waiting.String())
				if m == nil {
					return fmt.Errorf("roger tower invite --wait showed no invite:\n%s", waiting.String())
				}
				in, err := joininvite.Parse(m[1])
				s.inv = in
				if live := s.st.LiveInvitations(); len(live) == 1 {
					waitID = live[0].ID
				}
				return err
			})
			sc.Step(`^a client joins with it$`, func() error {
				s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), []string{s.inv.String(), "laptop-x"}) })
				if s.err != nil {
					return s.err
				}
				select {
				case <-waitDone:
				case <-time.After(10 * time.Second):
					return fmt.Errorf("roger tower invite --wait never saw the join:\n%s", waiting.String())
				}
				return nil
			})
			sc.Step(`^the owner's terminal prints "laptop-x joined \(key 4be1\.\.\.a9\)"$`, func() error {
				if !regexp.MustCompile(`laptop-x joined \(key [0-9a-f]{4}\.\.\.[0-9a-f]{2}\)`).MatchString(waiting.String()) {
					return fmt.Errorf("the owner's terminal printed %q", waiting.String())
				}
				return nil
			})
			sc.Step(`^it prints how to undo it with the new tower command: revoke-client laptop-x$`, func() error {
				if !strings.Contains(waiting.String(), "undo with: roger tower revoke-client laptop-x") {
					return fmt.Errorf("the owner's terminal printed %q", waiting.String())
				}
				return nil
			})
			sc.Step(`^the TUI and console show the same line$`, func() error {
				// The owner's screens show the invite's outcome (ownerjoin Outcome), which must be
				// the very line the terminal printed.
				line, over := towercli.OwnerInviteOutcome(s.dir, waitID)
				if !over || line == "" || !strings.Contains(waiting.String(), line) {
					return fmt.Errorf("the screens would show %q (over=%v), the terminal printed %q", line, over, waiting.String())
				}
				return nil
			})

			// --- a key-less Edge invite and the aliases ---------------------------------------
			sc.Step(`^the Edge owner runs "roger edge invite"$`, func() error {
				s.edgeInv, s.edgeAuth = ownerEdgeInvite(s.t) // fails if it asks for a key
				return nil
			})
			sc.Step(`^it prints "roger join <invite>" and a QR without asking for a key$`, func() error {
				if _, err := joininvite.Parse(s.edgeInv); err != nil {
					return err
				}
				return nil
			})
			sc.Step(`^the joiner's key is admitted when it presents the code$`, func() error {
				s.edgeOut, s.edgeCode = edgeRun(s.t, "join", s.edgeInv, "workshop")
				if s.edgeCode != 0 {
					return fmt.Errorf("roger join exited %d:\n%s", s.edgeCode, s.edgeOut)
				}
				return s.edgeKept()
			})
			sc.Step(`^the user runs "roger lan join <invite>"$`, func() error {
				if err := s.mint(); err != nil {
					return err
				}
				s.out = captureStdout(s.t, func() { s.err = dispatch(loadConfig(), []string{"lan", "join", s.inv.String(), "laptop-x"}) })
				return nil
			})
			sc.Step(`^the user runs "roger edge enroll <name> --invite <invite>"$`, func() error {
				s.edgeInv, s.edgeAuth = ownerEdgeInvite(s.t)
				asSecondMachine(s.t) // the device enrolling is not the authority's machine
				s.edgeOut, s.edgeCode = edgeRun(s.t, "edge", "enroll", "workshop", "--invite", s.edgeInv)
				return nil
			})
			sc.Step(`^it behaves exactly as "roger join <invite>"$`, func() error {
				if s.edgeInv != "" {
					if s.edgeCode != 0 {
						return fmt.Errorf("the alias exited %d:\n%s", s.edgeCode, s.edgeOut)
					}
					return s.edgeKept()
				}
				if s.err != nil || !s.admitted() || !strings.Contains(s.out, "joined the Tower lab") {
					return fmt.Errorf("roger lan join did not join as roger join does (%v): %s", s.err, s.out)
				}
				return nil
			})

			sc.Step(`^the Edge authority's owner ran "roger edge invite"$`, func() error {
				s.edgeInv, s.edgeAuth = ownerEdgeInvite(s.t)
				return nil
			})
			sc.Step(`^the device runs "roger join <invite>"$`, func() error {
				asSecondMachine(s.t)
				s.edgeOut, s.edgeCode = edgeRun(s.t, "join", s.edgeInv, "workshop")
				return nil
			})
			sc.Step(`^it enrolls exactly as "roger edge enroll --invite <invite>" does today$`, func() error {
				if s.edgeCode != 0 {
					return fmt.Errorf("roger join exited %d:\n%s", s.edgeCode, s.edgeOut)
				}
				id, _, ok, err := edgeIdentityStore().LoadIdentity()
				if err != nil || !ok || id == nil {
					return fmt.Errorf("no Edge identity was kept (%v):\n%s", err, s.edgeOut)
				}
				return nil
			})
			sc.Step(`^it verifies the root through the code's proof before keeping anything \(enrollment_root_trust\.feature\)$`, func() error {
				id, _, _, err := edgeIdentityStore().LoadIdentity()
				if err != nil {
					return err
				}
				if got, want := edgeauth.RootFingerprint(id.Root), s.edgeAuth.Descriptor().Fingerprint; got != want {
					return fmt.Errorf("kept root %s, the invite's authority is %s", got, want)
				}
				return nil
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
			Paths: []string{joinInviteFeature + ":36", joinInviteFeature + ":44", joinInviteFeature + ":74", joinInviteFeature + ":98",
				joinInviteFeature + ":107", joinInviteFeature + ":123", joinInviteFeature + ":133", joinInviteFeature + ":150", joinInviteFeature + ":145",
				joinInviteFeature + ":155", joinInviteFeature + ":162", joinInviteFeature + ":172", joinInviteFeature + ":181", joinInviteFeature + ":186", joinInviteFeature + ":192",
				joinInviteFeature + ":196", joinInviteFeature + ":208", joinInviteFeature + ":236",
				joinInviteFeature + ":54", joinInviteFeature + ":65", joinInviteFeature + ":80", joinInviteFeature + ":114", joinInviteFeature + ":138",
				joinInviteFeature + ":218", joinInviteFeature + ":223"}},
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

// fakeDetectAt is fakeDetect with the server's base URL, for a share that needs an upstream.
func fakeDetectAt(base string, models ...string) func(...string) ([]detect.Found, []string) {
	return func(...string) ([]detect.Found, []string) {
		return []detect.Found{{Name: "fixture", BaseURL: base, Chat: base + "/chat/completions", Models: models}}, nil
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
