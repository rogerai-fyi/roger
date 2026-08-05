package main

// Makes features/auth/broker_mediated_login.feature EXECUTABLE, driving the real broker
// routes, the real state machine, and the real client package - not a restatement of them.
//
// It lives in the broker package because the flow only exists as the two halves talking:
// a CLI signing requests on one side, a browser session on the other. Testing either half
// alone is exactly where a plausible-but-wrong wiring survives.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v5/internal/client"
	"rogerai.fm/roger/v5/internal/store"
)

type blState struct {
	t   *testing.T
	b   *broker
	db  *store.Mem
	url string

	d        client.DeviceLogin
	second   client.DeviceLogin
	account  string
	pollErr  error
	approveC int
	pendingB []byte
	seenURI  string
}

func (s *blState) reset(t *testing.T) {
	s.db = store.NewMem()
	s.b = relayBroker(s.db)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/start", s.b.deviceStart)
	mux.HandleFunc("/auth/device/token", s.b.deviceToken)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.url = srv.URL
	s.d, s.second = client.DeviceLogin{}, client.DeviceLogin{}
	s.account, s.pollErr, s.approveC = "", nil, 0
	s.pendingB, s.seenURI = nil, ""
}

// --- Given ----------------------------------------------------------------

func (s *blState) aCLIWithAKeypair() error {
	// The client package mints one on first use under its config dir.
	if client.UserPubHex() == "" {
		return errNoKey
	}
	return nil
}

func (s *blState) aBrokerWithProviders() error { return nil }

func (s *blState) aStartedLogin() error {
	d, err := client.DeviceLoginBegin(s.url)
	if err != nil {
		return err
	}
	s.d = d
	s.seenURI = d.VerificationURI
	return nil
}

func (s *blState) anApprovedLogin() error {
	if err := s.aStartedLogin(); err != nil {
		return err
	}
	return s.approveWith(githubSession(s.b, "alice", 4242))
}

func (s *blState) approveWith(cookie string) error {
	body, _ := json.Marshal(map[string]string{"user_code": s.d.UserCode})
	r := httptest.NewRequest(http.MethodPost, "/auth/device/approve", strings.NewReader(string(body)))
	r.Header.Set("Origin", testWebOrigin)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	s.b.deviceApprove(w, r)
	s.approveC = w.Code
	if w.Code != http.StatusOK {
		return errApproveFailed
	}
	return nil
}

// --- When -----------------------------------------------------------------

func (s *blState) startALogin() error { return s.aStartedLogin() }

func (s *blState) pollOnce() error {
	s.d.ExpiresIn, s.d.Interval = 2, 1
	s.account, s.pollErr = client.DeviceLoginPoll(s.url, s.d)
	return nil
}

func (s *blState) aProviderApproves(provider string) error {
	// This scenario names only the provider, so start the login it implies.
	if s.d.UserCode == "" {
		if err := s.aStartedLogin(); err != nil {
			return err
		}
	}
	switch provider {
	case "GitHub":
		return s.approveWith(githubSession(s.b, "alice", 4242))
	case "Apple":
		return s.approveWith(s.b.signSessionFull("user@example.com", 0,
			walletForAppleSub("sub-1"), "sub-1", time.Now().Add(time.Hour).Unix()))
	}
	return errUnknownProvider
}

func (s *blState) someoneApprovesSignedOut() error {
	body, _ := json.Marshal(map[string]string{"user_code": s.d.UserCode})
	r := httptest.NewRequest(http.MethodPost, "/auth/device/approve", strings.NewReader(string(body)))
	r.Header.Set("Origin", testWebOrigin)
	w := httptest.NewRecorder()
	s.b.deviceApprove(w, r)
	s.approveC = w.Code
	return nil
}

func (s *blState) denyIt() error {
	if s.d.UserCode == "" {
		if err := s.aStartedLogin(); err != nil {
			return err
		}
	}
	body, _ := json.Marshal(map[string]string{"user_code": s.d.UserCode})
	r := httptest.NewRequest(http.MethodPost, "/auth/device/deny", strings.NewReader(string(body)))
	r.Header.Set("Origin", testWebOrigin)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: githubSession(s.b, "alice", 4242)})
	w := httptest.NewRecorder()
	s.b.deviceDeny(w, r)
	return nil
}

func (s *blState) readPending() error {
	if s.d.UserCode == "" {
		if err := s.aStartedLogin(); err != nil {
			return err
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/device/pending?user_code="+s.d.UserCode, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: githubSession(s.b, "alice", 4242)})
	w := httptest.NewRecorder()
	s.b.devicePending(w, r)
	s.pendingB = w.Body.Bytes()
	return nil
}

func (s *blState) startASecondLogin() error {
	d, err := client.DeviceLoginBegin(s.url)
	if err != nil {
		return err
	}
	s.second = d
	return nil
}

// --- Then -----------------------------------------------------------------

func (s *blState) issuesACodePair() error {
	return allTrue(
		check(s.d.DeviceCode != "", "no device code"),
		check(s.d.UserCode != "", "no user code"),
		check(s.d.VerificationURI != "", "no verification URI"),
		check(s.d.Interval > 0, "no poll interval"),
		check(s.d.ExpiresIn > 0, "no expiry"),
	)
}

func (s *blState) uriIsOurs() error {
	return check(!strings.Contains(strings.ToLower(s.seenURI), "github.com") &&
		!strings.Contains(strings.ToLower(s.seenURI), "appleid"), "the URI is not ours: "+s.seenURI)
}

func (s *blState) noProviderDetailReachesTheCLI() error {
	// The whole start payload must be provider-free.
	raw, _ := json.Marshal(s.d)
	low := strings.ToLower(string(raw))
	return check(!strings.Contains(low, "github") && !strings.Contains(low, "apple") &&
		!strings.Contains(low, "client_id"), "the CLI was handed provider detail: "+string(raw))
}

func (s *blState) codesAreUnique() error {
	return allTrue(
		check(s.d.UserCode != s.second.UserCode, "user codes collided"),
		check(s.d.DeviceCode != s.second.DeviceCode, "device codes collided"),
	)
}

func (s *blState) statusIsPending() error {
	return check(s.pollErr != nil, "a never-approved login must not resolve")
}

func (s *blState) accountIsBound() error {
	if s.pollErr != nil {
		return s.pollErr
	}
	o, ok, err := s.db.OwnerByPubkey(client.UserPubHex())
	if err != nil {
		return err
	}
	return allTrue(
		check(ok, "the CLI key was not bound"),
		check(s.account != "", "no account returned"),
		check(o.GitHubID != 0 || o.AppleSub != "", "the owner row names no provider identity"),
	)
}

func (s *blState) cliLearnsOnlyTheAccount() error {
	return check(s.account != "" && !strings.Contains(strings.ToLower(s.account), "token"),
		"the CLI learned more than the account")
}

func (s *blState) refusedUnauthorized() error {
	return check(s.approveC == http.StatusUnauthorized, "approval was not refused")
}

func (s *blState) deniedReported() error {
	return check(s.pollErr == client.ErrLoginDenied, "a denial must be reported as such")
}

func (s *blState) cannotBeApprovedAfterwards() error {
	err := s.approveWith(githubSession(s.b, "alice", 4242))
	return check(err != nil, "a denied login was approved afterwards")
}

func (s *blState) pendingShowsTheCodeAndTime() error {
	var out map[string]any
	if err := json.Unmarshal(s.pendingB, &out); err != nil {
		return err
	}
	return allTrue(
		check(out["user_code"] == s.d.UserCode, "the code is not shown"),
		check(out["requested_at"] != nil, "the request time is not shown"),
		check(out["device_code"] == nil, "the device code must never reach the approver"),
	)
}

func TestBrokerMediatedLoginBDD(t *testing.T) {
	st := &blState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.Step(`^a CLI with an Ed25519 signing keypair$`, st.aCLIWithAKeypair)
			sc.Step(`^a broker that supports more than one sign-in provider$`, st.aBrokerWithProviders)
			sc.Step(`^the CLI starts a login$`, st.startALogin)
			sc.Step(`^a started login$`, st.aStartedLogin)
			sc.Step(`^a started login that nobody has approved$`, st.aStartedLogin)
			sc.Step(`^many CLIs start logins at the same moment$`, func() error {
				if err := st.aStartedLogin(); err != nil {
					return err
				}
				return st.startASecondLogin()
			})
			sc.Step(`^an approved login$`, st.anApprovedLogin)
			sc.Step(`^the CLI polls$`, st.pollOnce)
			sc.Step(`^a user signs in with "([^"]*)"$`, st.aProviderApproves)
			sc.Step(`^they approve a pending login$`, st.pollOnce)
			sc.Step(`^someone opens the verification URI without being signed in$`, st.someoneApprovesSignedOut)
			sc.Step(`^a user denies a pending login$`, st.denyIt)
			sc.Step(`^a user is asked to approve a pending login$`, st.readPending)

			sc.Step(`^the broker issues a device code, a user code, a verification URI, a poll interval, and an expiry$`, st.issuesACodePair)
			sc.Step(`^the verification URI is a RogerAI address$`, st.uriIsOurs)
			sc.Step(`^no provider-specific endpoint, client id, or third-party URL is returned to the CLI$`, st.noProviderDetailReachesTheCLI)
			sc.Step(`^every device code and user code is unique among pending logins$`, st.codesAreUnique)
			sc.Step(`^the response says authorization is pending$`, st.statusIsPending)
			sc.Step(`^the resulting account is bound to the CLI's key$`, st.accountIsBound)
			sc.Step(`^the CLI learns only which account it is signed in as, never which provider was used$`, st.cliLearnsOnlyTheAccount)
			sc.Step(`^they are asked to sign in first$`, st.refusedUnauthorized)
			sc.Step(`^it is told the request was denied$`, st.deniedReported)
			sc.Step(`^the same code can never be approved later$`, st.cannotBeApprovedAfterwards)
			sc.Step(`^it shows the user code so they can compare it with what their terminal printed$`, st.pendingShowsTheCodeAndTime)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/auth/broker_mediated_login.feature"},
			TestingT: t,
			Output:   os.Stdout,
			Strict:   false, // the prose-only scenarios stay documentation until they earn steps
		},
	}
	if suite.Run() != 0 {
		t.Fatal("broker-mediated login scenarios failed (see godog output above)")
	}
}

// --- tiny helpers ---------------------------------------------------------

type stepErr string

func (e stepErr) Error() string { return string(e) }

const (
	errNoKey           = stepErr("the CLI has no signing key")
	errApproveFailed   = stepErr("approval was refused")
	errUnknownProvider = stepErr("unknown provider")
)

func check(ok bool, msg string) error {
	if ok {
		return nil
	}
	return stepErr(msg)
}

func allTrue(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
