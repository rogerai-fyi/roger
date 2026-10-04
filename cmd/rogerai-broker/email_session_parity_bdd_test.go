package main

// Makes features/auth/email_session_parity.feature executable: broker scenarios run the
// real handlers over the real routes; page scenarios execute the real page scripts via
// web/test/login-loop.test.mjs (skipped, not faked, when node is absent).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/store"
)

type espState struct {
	t      *testing.T
	b      *broker
	cap    *capturedMail
	sends  chan map[string]any
	sess   *http.Cookie
	codes  map[string]int
	before int
}

var (
	webOnce sync.Once
	webOut  string
	webErr  error
)

func runWebLoopTests() (string, error) {
	webOnce.Do(func() {
		node, err := exec.LookPath("node")
		if err != nil {
			webErr = godog.ErrSkip
			return
		}
		out, err := exec.Command(node, "--test", "../../web/test/login-loop.test.mjs").CombinedOutput()
		webOut, webErr = string(out), err
	})
	return webOut, webErr
}

func (s *espState) reset() {
	s.b, s.cap = emailTestBroker(s.t)
	s.sess, s.codes = nil, map[string]int{}
}

func (s *espState) signIn(addr string) {
	s.sess = signInByEmail(s.t, s.b, s.cap, addr)
}

func (s *espState) getAll(routes map[string]http.HandlerFunc) {
	for p, h := range routes {
		s.codes[p] = getWithSession(h, p, s.sess).Code
	}
}

func (s *espState) allOK() error {
	for p, c := range s.codes {
		if c != http.StatusOK {
			return fmt.Errorf("%s answered %d, want 200", p, c)
		}
	}
	return nil
}

func (s *espState) webPasses() error {
	out, err := runWebLoopTests()
	if err == godog.ErrSkip {
		return err
	}
	if err != nil {
		return fmt.Errorf("web loop tests failed:\n%s", out)
	}
	return nil
}

func fileHas(path string, subs ...string) error {
	by, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if !strings.Contains(string(by), s) {
			return fmt.Errorf("%s does not contain %q", path, s)
		}
	}
	return nil
}

func TestEmailSessionParityBDD(t *testing.T) {
	s := &espState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})
			// 1
			sc.Step(`^a person signs in with an emailed code$`, func() error { s.signIn("loop@rogerai.fm"); return nil })
			sc.Step(`^their browser asks /me, /metrics/series and /account$`, func() error {
				s.getAll(map[string]http.HandlerFunc{"/me": s.b.me, "/metrics/series": s.b.metricsSeries, "/account": s.b.account})
				return nil
			})
			sc.Step(`^none of them answers 401 and /me reports logged_in true$`, func() error {
				if err := s.allOK(); err != nil {
					return err
				}
				if !strings.Contains(getWithSession(s.b.me, "/me", s.sess).Body.String(), `"logged_in":true`) {
					return fmt.Errorf("/me did not report logged_in true")
				}
				return nil
			})
			// 2
			sc.Step(`^a u_email_ wallet is an account wallet$`, func() error {
				if !isAccountWallet(walletForEmail("a@b.com")) {
					return fmt.Errorf("u_email_ wallet is not an account wallet")
				}
				return nil
			})
			sc.Step(`^an unsigned request may not claim a u_email_ wallet id$`, func() error {
				if !reservedID(walletForEmail("a@b.com")) {
					return fmt.Errorf("u_email_ id is claimable by an unsigned request")
				}
				return nil
			})
			// 3
			sc.Step(`^an account holds a GitHub link and a verified email$`, func() error {
				return s.b.db.BindOwner(store.Owner{Pubkey: "pk-1", GitHubID: 7, Login: "octocat",
					Email: "octocat@rogerai.fm", EmailVerifiedAt: time.Now().Unix()})
			})
			sc.Step(`^the person signs in with an emailed code for that address$`, func() error { s.signIn("octocat@rogerai.fm"); return nil })
			sc.Step(`^the session carries the GitHub identity and the same wallet$`, func() error {
				_, gid, wallet, _, ok := s.b.verifySessionFull(s.sess.Value)
				if !ok || gid != 7 || wallet != "u_gh_7" {
					return fmt.Errorf("session gid=%d wallet=%s ok=%v", gid, wallet, ok)
				}
				return nil
			})
			sc.Step(`^/account shows the account's email$`, func() error {
				if !strings.Contains(getWithSession(s.b.account, "/account", s.sess).Body.String(), `"email":"octocat@rogerai.fm"`) {
					return fmt.Errorf("/account did not show the email")
				}
				return nil
			})
			// 4
			sc.Step(`^an operator bound a CLI key through an email sign-in$`, func() error {
				if err := s.b.db.BindOwner(store.Owner{Pubkey: "pk-e", Email: "op@rogerai.fm", EmailVerifiedAt: time.Now().Unix()}); err != nil {
					return err
				}
				s.signIn("op@rogerai.fm")
				return nil
			})
			sc.Step(`^they read their stations, their API keys and their account$`, func() error {
				s.getAll(map[string]http.HandlerFunc{"/stations": s.b.stations, "/grants": s.b.grants, "/account": s.b.account})
				return nil
			})
			sc.Step(`^each answers 200, not "no operator account"$`, s.allOK)
			// 5
			sc.Step(`^an email account with no balance requests deletion$`, func() error {
				s.b.seedFunds = 0
				if err := s.b.db.BindOwner(store.Owner{Pubkey: "pk-d", Email: "bye@rogerai.fm", EmailVerifiedAt: time.Now().Unix(), Login: "bye@rogerai.fm"}); err != nil {
					return err
				}
				s.signIn("bye@rogerai.fm")
				req := httptest.NewRequest(http.MethodPost, "/account/delete", nil)
				req.Header.Set("Origin", testWebOrigin)
				req.AddCookie(s.sess)
				rec := httptest.NewRecorder()
				s.b.accountDelete(rec, req)
				s.codes["/account/delete"] = rec.Code
				return nil
			})
			sc.Step(`^it is deleted by its own owner row, never "can't be deleted in-app"$`, func() error {
				if s.codes["/account/delete"] != http.StatusOK {
					return fmt.Errorf("delete answered %d", s.codes["/account/delete"])
				}
				o, found, _ := s.b.db.OwnerByPubkey("pk-d")
				if !found || !o.Anonymized {
					return fmt.Errorf("the owner row was not anonymized")
				}
				return nil
			})
			// 6, 7: the pages, run for real
			sc.Step(`^/account says the person is signed in$`, func() error { return nil })
			sc.Step(`^/metrics/series or /console answers 401 or 403$`, func() error { return nil })
			sc.Step(`^the page shows its error state and does not navigate$`, s.webPasses)
			sc.Step(`^login and dashboard together never navigate more than twice$`, s.webPasses)
			sc.Step(`^the signed-in name for "([^"]*)" is "([^"]*)", never "([^"]*)"$`, func(_, _, _ string) error { return s.webPasses() })
			// 8
			sc.Step(`^five valid sessions were refused as logged-out inside ten minutes$`, func() error {
				s.b, s.sends = alertBroker(s.t, "ops@example.com")
				now := time.Now()
				for i := 0; i < sessionRefusedThreshold; i++ {
					s.b.noteSessionRefused(now)
				}
				s.b.checkSessionRefusedAlert(now)
				return nil
			})
			sc.Step(`^one "([^"]*)" alert is sent$`, func(sub string) error {
				select {
				case p := <-s.sends:
					if !strings.Contains(fmt.Sprint(p["subject"]), "refused") {
						return fmt.Errorf("unexpected subject %v", p["subject"])
					}
					return nil
				case <-time.After(2 * time.Second):
					return fmt.Errorf("no alert was sent")
				}
			})
			sc.Step(`^a logged-out visitor's 401 never counts toward it$`, func() error {
				before := s.b.sessionRefusedCount(time.Now())
				req := httptest.NewRequest(http.MethodGet, "/metrics/series", nil)
				req.Header.Set("Origin", testWebOrigin)
				s.b.metricsSeries(httptest.NewRecorder(), req)
				if after := s.b.sessionRefusedCount(time.Now()); after != before {
					return fmt.Errorf("count moved %d -> %d", before, after)
				}
				return nil
			})
			sc.Step(`^it clears after a quiet window$`, func() error {
				s.b.checkSessionRefusedAlert(time.Now().Add(sessionRefusedWindow + time.Minute))
				s.b.alertMu.Lock()
				defer s.b.alertMu.Unlock()
				if s.b.alertFiring["auth_session_refused"] {
					return fmt.Errorf("still firing")
				}
				return nil
			})
			// 9
			sc.Step(`^scripts/auth-probe.sh checks the login page, broker liveness, clean 401s and credentialed CORS$`, func() error {
				return fileHas("../../scripts/auth-probe.sh", "login.html", "/health", "401", "Access-Control-Request-Method")
			})
			sc.Step(`^a failed scheduled run emails the repository's workflow editor$`, func() error {
				return fileHas("../../.github/workflows/auth-probe.yml", "schedule:", "cron:", "scripts/auth-probe.sh")
			})
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/auth/email_session_parity.feature"},
			TestingT: t,
			Output:   os.Stdout,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("email-session-parity scenarios failed (see godog output above)")
	}
}
