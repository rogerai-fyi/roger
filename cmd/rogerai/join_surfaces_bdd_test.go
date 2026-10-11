package main

// join_surfaces_bdd_test.go runs features/join/surfaces.feature's CLI scenarios against the real
// join code: a real standalone Tower over TLS (joinInviteBDD), a real Tower that takes requests
// (approveBDD) and a real Edge authority. Each situation is produced, not described; what the
// CLI then printed is what the scenario reads. The TUI and console scenarios run in
// TestJoinOwnerScreensBDD.
//
// "<says>" is found in the output. "<next>" is the one next step: exactly one line carries it
// (case aside; the approved refusals in approve.feature and adversarial.feature read "X. Ask the
// owner ..." on one line), where a line ending in ":" and the command line under it count as one.
// The feature's address 192.168.1.20:8787 stands for the fixture Tower's own loopback address.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/qr"
	"rogerai.fm/roger/v6/internal/tower"
)

type surfacesBDD struct {
	t      *testing.T
	inv    *joinInviteBDD
	ap     *approveBDD
	out    string // everything the situation printed, its error included
	addr   string // the address the feature's 192.168.1.20:8787 stands for
	shown  []string
	helpOK bool
}

// say runs f and keeps what it printed, its error last.
func (s *surfacesBDD) say(out string, err error) {
	s.out = out
	if err != nil {
		s.out += "\n" + err.Error()
	}
	s.shown = append(s.shown, s.out)
}

// placeholderRE turns a scenario's text into a pattern: <invite>, <band>, <bands> and <name> stand
// for what a real run prints there.
func placeholderRE(text, addr string) *regexp.Regexp {
	q := regexp.QuoteMeta(text)
	q = strings.ReplaceAll(q, regexp.QuoteMeta("192.168.1.20:8787"), regexp.QuoteMeta(addr))
	for ph, re := range map[string]string{"<invite>": `roger://join\?\S+`, "<band>": `<band>`, "<bands>": `\S.*`, "<name>": `\S+`} {
		q = strings.ReplaceAll(q, regexp.QuoteMeta(ph), re)
	}
	return regexp.MustCompile(`(?i)` + q)
}

// nextLines are the output's lines, plus each "...:" line joined to the command line under it.
func nextLines(out string) []string {
	lines := strings.Split(out, "\n")
	var cands []string
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasSuffix(l, ":") && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
			cands = append(cands, l+" "+strings.TrimSpace(lines[i+1]))
			continue
		}
		cands = append(cands, l)
	}
	return cands
}

// situation produces one row's situation for real.
func (s *surfacesBDD) situation(what string) error {
	j := s.inv
	j.reset()
	s.addr = j.addr
	switch what {
	case "the owner mints a client invite":
		if err := j.mint(); err != nil {
			return err
		}
		s.say(j.out, nil)
	case "the owner mints a station invite":
		if err := j.mint("--station"); err != nil {
			return err
		}
		s.say(j.out, nil)
	case "a client joins":
		if err := j.mint(); err != nil {
			return err
		}
		_ = j.join(j.inv)
		s.say(j.out, j.err)
	case "a station joins":
		if err := j.mint("--station"); err != nil {
			return err
		}
		j.stationM = []string{"qwen3-8b", "smollm2-135m-instruct"}
		_ = j.stationJoin("kitchen-pi")
		s.say(j.out, j.err)
	case "a device joins an Edge":
		inv, _ := ownerEdgeInviteNamed(s.t, "home")
		asSecondMachine(s.t)
		out, code := edgeRun(s.t, "join", inv, "workshop")
		var err error
		if code != 0 {
			err = fmt.Errorf("roger join exited %d", code)
		}
		s.say(out, err)
	case "the invite expired":
		_, code, err := j.st.CreateJoinInvitation(tower.JoinClient, "", nil, time.Millisecond)
		if err != nil {
			return err
		}
		time.Sleep(5 * time.Millisecond)
		fp, _ := j.st.JoinFingerprint()
		_ = j.join(joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{j.addr}, FP: fp, Code: code})
		s.say(j.out, j.err)
	case "the invite was already used":
		if err := j.mint(); err != nil {
			return err
		}
		_ = j.join(j.inv) // the first machine uses it
		if j.err != nil {
			return fmt.Errorf("the first join failed: %v", j.err)
		}
		j.t.Setenv("XDG_CONFIG_HOME", j.t.TempDir()) // a second machine, with its own key
		_ = j.join(j.inv)
		s.say(j.out, j.err)
	case "the Tower's key does not match":
		if err := j.mint(); err != nil {
			return err
		}
		in := j.inv
		// Another Tower's key: the real fingerprint with one character changed.
		last := in.FP[len(in.FP)-1]
		flip := byte('0')
		if last == '0' {
			flip = '1'
		}
		in.FP = in.FP[:len(in.FP)-1] + string(flip)
		_ = j.join(in)
		s.say(j.out, j.err)
	case "the Tower can't be reached":
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		closed := ln.Addr().String()
		ln.Close()
		if err := j.mint(); err != nil {
			return err
		}
		in := j.inv
		in.Addrs = []string{closed}
		s.addr = closed
		_ = j.join(in)
		s.say(j.out, j.err)
	case "the owner refused a request":
		if err := s.ap.reset(); err != nil {
			return err
		}
		s.ap.ask(s.ap.addr, "laptop-x", false)
		if err := s.ap.decide(false); err != nil {
			return err
		}
		if err := s.ap.finish(s.ap.last()); err != nil {
			return err
		}
		s.say(s.ap.last().out, s.ap.last().err)
	case "nobody answered a request":
		if err := s.ap.reset(); err != nil {
			return err
		}
		s.ap.ask(s.ap.addr, "laptop-x", false)
		if _, err := s.ap.waiting(1); err != nil {
			return err
		}
		s.ap.advance(localplane.PairLifetime + time.Second)
		if err := s.ap.finish(s.ap.last()); err != nil {
			return err
		}
		s.say(s.ap.last().out, s.ap.last().err)
	default:
		return fmt.Errorf("no such situation %q", what)
	}
	return nil
}

func TestJoinSurfacesBDD(t *testing.T) {
	s := &surfacesBDD{t: t, inv: &joinInviteBDD{t: t}, ap: &approveBDD{t: t}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.out, s.addr = "", ""
				return ctx, nil
			})
			sc.Step(`^"([^"]*)"$`, s.situation)
			sc.Step(`^the CLI prints "([^"]*)"$`, func(says string) error {
				if !placeholderRE(says, s.addr).MatchString(s.out) {
					return fmt.Errorf("the CLI did not print %q:\n%s", says, s.out)
				}
				return nil
			})
			sc.Step(`^it prints exactly one next-step line "([^"]*)"$`, func(next string) error {
				re := placeholderRE(next, s.addr)
				n := 0
				for _, l := range nextLines(s.out) {
					if re.MatchString(l) {
						n++
					}
				}
				if n != 1 {
					return fmt.Errorf("%d lines carry the next step %q (want exactly one):\n%s", n, next, s.out)
				}
				return nil
			})

			// --- the not-admitted hint ------------------------------------------------------------
			sc.Step(`^a LAN Tower that answers this machine with the uniform 401$`, func() error {
				s.inv.reset() // a Tower that has admitted nobody
				return nil
			})
			sc.Step(`^the user runs "roger --lan use" against it$`, func() error {
				restore := client.SetUseServe(func(ln net.Listener, _ http.Handler) error { return ln.Close() })
				defer restore()
				c := loadConfig()
				c.LanTowers = []string{s.inv.addr}
				saveConfig(c)
				out, err := captureEdgeStdout(func() error { return run([]string{"--lan", "use", "qwen3-8b"}, loadConfig()) })
				s.say(out, err)
				return nil
			})
			sc.Step(`^the hint says "([^"]*)"$`, func(want string) error {
				if !strings.Contains(s.out, want) {
					return fmt.Errorf("roger --lan use did not give the hint %q:\n%s", want, s.out)
				}
				return nil
			})

			// --- no screen asks the user to carry a key or an id -----------------------------------
			sc.Step(`^no join-flow message asks the user to copy a key hash, client id or invitation id between machines$`, func() error {
				// Every situation of the CLI table, the hint, the owner's sentences, and what a station
				// that has not joined yet is told when it polls.
				for _, what := range []string{"the owner mints a client invite", "the owner mints a station invite",
					"a client joins", "a station joins", "the invite expired", "the invite was already used",
					"the Tower's key does not match", "the Tower can't be reached", "the owner refused a request",
					"nobody answered a request"} {
					if err := s.situation(what); err != nil {
						return fmt.Errorf("%s: %v", what, err)
					}
				}
				s.inv.reset()
				_ = s.inv.runShare("qwen3-8b") // a station the Tower has not attached
				s.shown = append(s.shown, s.inv.lastShare,
					client.NotAdmittedHint,
					tower.InviteHeading(tower.JoinClient, 15*time.Minute), tower.InviteNext(tower.JoinClient),
					tower.InviteHeading(tower.JoinStation, 15*time.Minute), tower.InviteNext(tower.JoinStation),
					tower.AskLine(tower.PairRequest{Name: "laptop-x", Kind: tower.JoinClient, Phrase: "a-b-c-01"}),
					tower.ApproveHint("laptop-x"))
				carry := regexp.MustCompile(`(?i)(key ?hash|client id|invitation id|--key\b|--client\b|roger whoami|copy (this|the) (key|id))`)
				for _, msg := range s.shown {
					if m := carry.FindString(msg); m != "" {
						return fmt.Errorf("a join-flow message asks the user to carry %q between machines:\n%s", m, msg)
					}
				}
				return nil
			})

			// --- the QR -----------------------------------------------------------------------------
			sc.Step(`^the invite is shown in an 80x24 terminal$`, func() error {
				s.inv.reset()
				return s.inv.mint()
			})
			sc.Step(`^the QR fits without wrapping, drawn with half-block characters$`, func() error {
				code := qr.ForTerminal("roger join "+s.inv.inv.String(), 80, false)
				if code == qr.TooSmall+"\n" {
					return fmt.Errorf("an 80-column terminal got %q", code)
				}
				if !strings.ContainsAny(code, "▀▄█") {
					return fmt.Errorf("the code is not drawn with half blocks:\n%s", code)
				}
				for _, l := range strings.Split(strings.TrimRight(code, "\n"), "\n") {
					if w := utf8.RuneCountInString(ansi.Strip(l)); w > 80 {
						return fmt.Errorf("a line of the code is %d columns, wider than 80", w)
					}
				}
				return nil
			})
			sc.Step(`^with NO_COLOR or a narrow terminal it says "([^"]*)"$`, func(want string) error {
				line := "roger join " + s.inv.inv.String()
				for name, got := range map[string]string{
					"NO_COLOR":        qr.ForTerminal(line, 80, true),
					"a narrow screen": qr.ForTerminal(line, 30, false),
				} {
					if strings.TrimSpace(got) != want {
						return fmt.Errorf("%s: got %q, want %q", name, got, want)
					}
				}
				return nil
			})

			// --- roger join --help ------------------------------------------------------------------
			sc.Step(`^the user runs "roger join --help"$`, func() error {
				out := captureStdout(s.t, func() { _ = cmdJoin(loadConfig(), []string{"--help"}) })
				s.say(out, nil)
				return nil
			})
			sc.Step(`^it explains: join with an invite \(roger join <invite>\), or ask by address \(roger join <address>\), and that the owner approves$`, func() error {
				var lines []string
				for _, l := range strings.Split(strings.TrimSpace(s.out), "\n") {
					if strings.TrimSpace(l) != "" {
						lines = append(lines, l)
					}
				}
				if len(lines) != 3 {
					return fmt.Errorf("the help is %d lines, want 3:\n%s", len(lines), s.out)
				}
				for _, want := range []string{"roger join <invite>", "roger join <address>", "approve"} {
					if !strings.Contains(s.out, want) {
						return fmt.Errorf("the help does not say %q:\n%s", want, s.out)
					}
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{surfacesFeature + ":11", surfacesFeature + ":30", surfacesFeature + ":36",
				surfacesFeature + ":52", surfacesFeature + ":60"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/surfaces.feature failed")
	}
}
