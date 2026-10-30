package main

// join_migration_bdd_test.go runs features/join/migration.feature: a client and a station admitted
// the old way keep working after the upgrade and pin their Tower on first TLS contact, once,
// logged; the old commands still work with one line naming the new way; the manual says so. The
// Tower is a real standalone plane on the real TLS-and-plain listener (startCountingTower), the
// admissions are the real `roger tower invite --client` / `admit` / `attach`, and the client and
// station are the real decideLAN, LAN dial and share loop. Addresses in the feature are symbolic:
// "192.168.1.20:8787" is the fixture Tower's loopback address, and the Tower "lab" is named by
// that address, which is all an upgraded client knows of it.
//
// Not run: "The release after removes the old Tower admission commands" describes the next
// release; the commands are deliberately still here in this one.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towercli"
	"rogerai.fm/roger/v7/internal/towerpin"
)

const joinMigrationFeature = "../../features/join/migration.feature"

type joinMigrationBDD struct {
	t       *testing.T
	st      *tower.State
	dir     string
	tw      *countingTower
	fp      string
	out     string
	shareTo string
	status  []client.LANTowerStatus
	pinsWas map[string]string
	old     string
	answer  bool
}

func (s *joinMigrationBDD) reset() {
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	lanmode.SetPins(nil)
	s.dir = s.t.TempDir() + "/lab"
	st, err := tower.Init(s.dir, tower.ModeStandalone)
	if err != nil {
		s.t.Fatal(err)
	}
	s.st, s.fp = st, mustFP(st)
	s.tw = startCountingTower(s.t, st, "")
	s.out, s.shareTo, s.status, s.pinsWas, s.old, s.answer = "", "", nil, nil, "", false
}

// tower runs `roger tower <args>` against the fixture's data directory.
func (s *joinMigrationBDD) tower(args ...string) (string, error) {
	var b strings.Builder
	if code := towercli.Run(append([]string{args[0], "--dir", s.dir}, args[1:]...), &b, &b); code != 0 {
		return b.String(), fmt.Errorf("roger tower %s exited %d:\n%s", args[0], code, b.String())
	}
	return b.String(), nil
}

// clientID is this machine's consumer key hash, the id the old flow admitted.
func clientID() string {
	return protocol.UserIDFromPubkey(hex.EncodeToString(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey)))
}

// valueAfter is the rest of the line that starts with prefix.
func valueAfter(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, prefix); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// oldInvite mints the older bound invitation for this machine's key: its id and code.
func (s *joinMigrationBDD) oldInvite() (out, id, code string, err error) {
	out, err = s.tower("invite", "--client", clientID())
	return out, valueAfter(out, "invitation: "), valueAfter(out, "code: "), err
}

// adminOldWay admits this machine with the older two-step flow.
func (s *joinMigrationBDD) admitOldWay() (string, error) {
	_, id, code, err := s.oldInvite()
	if err != nil {
		return "", err
	}
	return s.tower("admit", "--client", clientID(), "--id", id, "--code", code)
}

// upgradedStart is the client starting after the upgrade in LAN mode, then dialing the Tower.
func (s *joinMigrationBDD) upgradedStart() {
	s.out = captureStdout(s.t, func() {
		_, _, err := decideLAN([]string{"--lan", "status"}, loadConfig())
		if err != nil {
			s.t.Error(err)
		}
	})
	_, s.status = client.LANDiscover(context.Background(), client.LANInputs{Towers: []string{s.tw.addr}, Timeout: 3 * time.Second})
}

func (s *joinMigrationBDD) pinLine() string {
	return "pinned the Tower at " + s.tw.addr + " now (first use after upgrade): sha256:" + s.fp
}

// shareAsStation runs the upgraded `roger share` of a station whose broker setting is the Tower,
// for a moment: the real fold, the real first-use pin and the real share loop.
func (s *joinMigrationBDD) shareAsStation() error {
	prev := lanShareContext
	lanShareContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 1500*time.Millisecond)
	}
	defer func() { lanShareContext = prev }()
	cfg := loadConfig()
	cfg = foldBrokerTower(cfg, io.Discard)
	base, ok := brokerTower(cfg)
	if !ok {
		return fmt.Errorf("the broker setting %s is not seen as a standalone Tower", cfg.Broker)
	}
	// What the station's loop sends is counted from here: the fold's probe above is the older
	// plain-http check of the broker setting, made before anything is pinned.
	s.tw.polls.Store(0)
	s.tw.polls401.Store(0)
	s.tw.plainReqs.Store(0)
	s.tw.tlsReqs.Store(0)
	var loop syncBuf
	pins := captureStdout(s.t, func() {
		serveBrokerTower(cfg, agent.Config{Model: "m", Upstream: "http://127.0.0.1:1/v1/chat/completions"}, base, &loop)
	})
	s.out = pins + loop.String()
	return nil
}

func TestJoinMigrationBDD(t *testing.T) {
	s := &joinMigrationBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})

			// An already-admitted client
			sc.Step(`^a client admitted by "roger tower invite --client" and "roger tower admit" before this change$`, func() error {
				_, err := s.admitOldWay()
				return err
			})
			sc.Step(`^lan_towers lists the Tower with no pin$`, func() error {
				c := loadConfig()
				c.LanTowers = []string{s.tw.addr}
				return saveConfig(c)
			})
			sc.Step(`^both upgrade and the client sends its next request over TLS$`, func() error {
				s.upgradedStart()
				return nil
			})
			sc.Step(`^the request is served$`, func() error {
				if s.status[0].State != client.LANTowerAnswering {
					return fmt.Errorf("the Tower is %s (%s), want answering", s.status[0].State, s.status[0].Detail)
				}
				if s.tw.tlsReqs.Load() == 0 || s.tw.plainReqs.Load() != 0 {
					return fmt.Errorf("tls %d plain %d: want every request over TLS", s.tw.tlsReqs.Load(), s.tw.plainReqs.Load())
				}
				return nil
			})
			sc.Step(`^the client pins the Tower's fingerprint as seen on that first TLS contact$`, func() error {
				if got := loadConfig().LanTowerPins[s.tw.addr]; got != s.fp {
					return fmt.Errorf("pin %q, want the key the Tower presents %q", got, s.fp)
				}
				return nil
			})
			sc.Step(`^it prints once "pinned the Tower lab now \(first use after upgrade\): sha256:9f3c\.\.\.e1"$`, func() error {
				if n := strings.Count(s.out, s.pinLine()); n != 1 {
					return fmt.Errorf("%d pin line(s), want one %q in:\n%s", n, s.pinLine(), s.out)
				}
				return nil
			})
			sc.Step(`^the config records the pin and that it came from first use$`, func() error {
				c := loadConfig()
				if c.LanTowerPins[s.tw.addr] != s.fp || !c.LanTowerPinFirstUse[s.tw.addr] {
					return fmt.Errorf("pins %v, first use %v", c.LanTowerPins, c.LanTowerPinFirstUse)
				}
				return nil
			})

			// Once only
			sc.Step(`^the client pinned the Tower on first use after upgrading$`, func() error {
				if _, err := s.admitOldWay(); err != nil {
					return err
				}
				c := loadConfig()
				c.LanTowers = []string{s.tw.addr}
				if err := saveConfig(c); err != nil {
					return err
				}
				s.upgradedStart()
				if !strings.Contains(s.out, s.pinLine()) {
					return fmt.Errorf("no first-use pin:\n%s", s.out)
				}
				s.pinsWas = loadConfig().LanTowerPins
				return nil
			})
			sc.Step(`^the key later changes$`, func() error {
				if _, _, err := s.st.RotateTLSKey(); err != nil {
					return err
				}
				_ = s.tw.srv.Close()
				s.tw = startCountingTower(s.t, s.st, s.tw.addr)
				s.upgradedStart()
				return nil
			})
			sc.Step(`^the client refuses exactly as tower_identity\.feature says, and does not re-pin$`, func() error {
				if s.status[0].State != client.LANTowerRefused || s.status[0].Detail != client.TowerKeyChanged(s.tw.addr) {
					return fmt.Errorf("the Tower is %s (%q), want refused with %q", s.status[0].State, s.status[0].Detail, client.TowerKeyChanged(s.tw.addr))
				}
				if n := s.tw.requests(); n != 0 {
					return fmt.Errorf("%d request(s) reached the changed Tower", n)
				}
				if strings.Contains(s.out, "pinned the Tower") {
					return fmt.Errorf("re-pinned:\n%s", s.out)
				}
				if got := loadConfig().LanTowerPins; fmt.Sprint(got) != fmt.Sprint(s.pinsWas) {
					return fmt.Errorf("pins %v, were %v", got, s.pinsWas)
				}
				return nil
			})

			// Only added Towers
			sc.Step(`^an address that is not in lan_towers$`, func() error {
				if c := loadConfig(); len(c.LanTowers) != 0 {
					return fmt.Errorf("lan_towers is %v", c.LanTowers)
				}
				return nil
			})
			sc.Step(`^the client contacts it$`, func() error {
				s.upgradedStart()
				return nil
			})
			sc.Step(`^nothing is pinned by first use; joining needs an invite or an approval$`, func() error {
				if c := loadConfig(); len(c.LanTowerPins) != 0 || strings.Contains(s.out, "pinned the Tower") {
					return fmt.Errorf("pinned %v:\n%s", c.LanTowerPins, s.out)
				}
				if s.status[0].State != client.LANTowerNotAdmitted {
					return fmt.Errorf("the Tower is %s, want not admitted", s.status[0].State)
				}
				return nil
			})

			// An already-attached station
			sc.Step(`^a station attached with "roger tower attach --station s --key K --models m" before this change$`, func() error {
				if _, err := s.admitOldWay(); err != nil { // the old flow's first admission made the operator
					return err
				}
				if _, err := s.tower("attach", "--station", "s", "--key", agent.NodeID(), "--models", "m"); err != nil {
					return err
				}
				c := loadConfig()
				c.Broker = "http://" + s.tw.addr
				return saveConfig(c)
			})
			sc.Step(`^both upgrade$`, s.shareAsStation)
			sc.Step(`^the station keeps serving its listed models$`, func() error {
				if s.tw.polls.Load() == 0 || s.tw.polls401.Load() != 0 {
					return fmt.Errorf("polls %d, refused %d:\n%s", s.tw.polls.Load(), s.tw.polls401.Load(), s.out)
				}
				sts, err := s.st.Stations()
				if err != nil {
					return err
				}
				if len(sts) != 1 || strings.Join(sts[0].Models, ",") != "m" {
					return fmt.Errorf("stations %+v", sts)
				}
				return nil
			})
			sc.Step(`^it pins the Tower on its first TLS poll, once, logged$`, func() error {
				if n := strings.Count(s.out, s.pinLine()); n != 1 {
					return fmt.Errorf("%d pin line(s):\n%s", n, s.out)
				}
				if s.tw.plainReqs.Load() != 0 {
					return fmt.Errorf("%d poll(s) went over plain http after the pin", s.tw.plainReqs.Load())
				}
				if c := loadConfig(); c.LanTowerPins[s.tw.addr] != s.fp || !c.LanTowerPinFirstUse[s.tw.addr] {
					return fmt.Errorf("pins %v, first use %v", c.LanTowerPins, c.LanTowerPinFirstUse)
				}
				if err := s.shareAsStation(); err != nil {
					return err
				}
				if strings.Contains(s.out, "pinned the Tower") {
					return fmt.Errorf("pinned again on the next share:\n%s", s.out)
				}
				return nil
			})

			// The old commands
			sc.Step(`^the owner runs "([^"]*)"$`, func(old string) error {
				s.old = old
				var err error
				switch {
				case strings.HasPrefix(old, "roger tower invite --client"):
					s.out, _, _, err = s.oldInvite()
				case strings.HasPrefix(old, "roger tower admit"):
					s.out, err = s.admitOldWay()
				case strings.HasPrefix(old, "roger tower attach"):
					if _, err := s.admitOldWay(); err != nil {
						return err
					}
					s.out, err = s.tower("attach", "--station", "s", "--key", agent.NodeID(), "--models", "m")
				case strings.HasPrefix(old, "roger lan add"):
					var b strings.Builder
					err = lanAddTo(&b, []string{s.tw.addr})
					s.out = b.String()
				default:
					return godog.ErrPending
				}
				return err
			})
			sc.Step(`^it works as before$`, func() error {
				switch {
				case strings.HasPrefix(s.old, "roger tower invite --client"):
					if valueAfter(s.out, "invitation: ") == "" || valueAfter(s.out, "code: ") == "" {
						return fmt.Errorf("no invitation and code:\n%s", s.out)
					}
				case strings.HasPrefix(s.old, "roger tower admit"):
					st, err := tower.Open(s.dir)
					if err != nil {
						return err
					}
					if !st.IsAdmitted(clientID()) {
						return fmt.Errorf("not admitted:\n%s", s.out)
					}
				case strings.HasPrefix(s.old, "roger tower attach"):
					if !strings.Contains(s.out, "attached station s") {
						return fmt.Errorf("not attached:\n%s", s.out)
					}
				default:
					if c := loadConfig(); len(c.LanTowers) != 1 || c.LanTowers[0] != s.tw.addr {
						return fmt.Errorf("lan_towers %v", c.LanTowers)
					}
				}
				return nil
			})
			sc.Step(`^it prints one line naming the new way: "([^"]*)"$`, func(newWay string) error {
				if n := strings.Count(s.out, "the new way: "+newWay); n != 1 {
					return fmt.Errorf("%d line(s) naming %q in:\n%s", n, newWay, s.out)
				}
				return nil
			})

			// roger lan add asks
			sc.Step(`^the client is already admitted to a TLS Tower but has no pin for it$`, func() error {
				_, err := s.admitOldWay()
				return err
			})
			sc.Step(`^the client runs "roger lan add 192\.168\.1\.20:8787"$`, func() error {
				for _, yes := range []bool{false, true} {
					c := loadConfig() // each answer starts from a Tower not yet added
					c.LanTowers, c.LanTowerPins = nil, nil
					if err := saveConfig(c); err != nil {
						return err
					}
					prev := lanAddConfirm
					lanAddConfirm = func() bool { return yes }
					var b strings.Builder
					err := lanAddTo(&b, []string{s.tw.addr})
					lanAddConfirm = prev
					if err != nil {
						return err
					}
					_, pinned := loadConfig().LanTowerPins[s.tw.addr]
					if pinned != yes {
						return fmt.Errorf("answered %v, pinned %v", yes, pinned)
					}
					s.out += b.String()
				}
				return nil
			})
			sc.Step(`^it shows the Tower's short fingerprint and asks "pin this Tower\? \[y/N\]"$`, func() error {
				if want := towerpin.Short(s.fp) + "; pin this Tower? [y/N]"; strings.Count(s.out, want) != 2 {
					return fmt.Errorf("want %q asked each time in:\n%s", want, s.out)
				}
				return nil
			})
			sc.Step(`^only y records the pin$`, func() error {
				if loadConfig().LanTowerPins[s.tw.addr] != s.fp {
					return fmt.Errorf("y did not record the pin")
				}
				return nil
			})

			// The manual
			sc.Step(`^the manual's LAN section is read$`, func() error {
				b, err := os.ReadFile("../../web/app/routes/manual/+page.svelte") // the manual is an app route
				if err != nil {
					return err
				}
				i := strings.Index(string(b), "<h3>Joining Towers</h3>")
				if i < 0 {
					return fmt.Errorf("the manual has no Joining Towers section")
				}
				s.out = string(b[i:])
				s.out = s.out[:strings.Index(s.out, "</section>")]
				return nil
			})
			sc.Step(`^it says Towers are now joined with "roger join" and verified by their key$`, func() error {
				for _, w := range []string{"Towers are now joined with <code>roger join</code>", "verified by their key"} {
					if strings.Count(s.out, w) != 1 {
						return fmt.Errorf("the LAN section does not say %q once", w)
					}
				}
				return nil
			})
			sc.Step(`^it says older clients pin their Tower on first contact after upgrading$`, func() error {
				if strings.Count(s.out, "Older clients pin their Tower on first contact after upgrading") != 1 {
					return fmt.Errorf("the LAN section does not explain the first-use pin once")
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{joinMigrationFeature + ":9", joinMigrationFeature + ":18", joinMigrationFeature + ":23",
				joinMigrationFeature + ":28", joinMigrationFeature + ":34", joinMigrationFeature + ":48", joinMigrationFeature + ":59"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/migration.feature failed")
	}
}
