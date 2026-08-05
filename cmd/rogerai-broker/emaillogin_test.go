package main

// First-party sign-in over the real routes, per features/auth/email_code_login.feature.
//
// The tests that matter most here are the ones asserting what the response does NOT say.
// With OAuth the provider owned enumeration resistance; mailing our own codes means we own
// it, and the only way to keep owning it is to assert on it.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/store"
)

// capturingMailer records what would have been mailed instead of mailing it, so a test can
// read the code a person would have received.
type capturedMail struct {
	mu   sync.Mutex
	sent []struct{ to, subject, text string }
}

func (c *capturedMail) add(to, subject, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, struct{ to, subject, text string }{to, subject, text})
}

func (c *capturedMail) all() []struct{ to, subject, text string } {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]struct{ to, subject, text string }, len(c.sent))
	copy(out, c.sent)
	return out
}

// emailTestBroker wires a broker whose mailer posts to a stub, capturing every send.
func emailTestBroker(t *testing.T) (*broker, *capturedMail) {
	t.Helper()
	cap := &capturedMail{}
	b := testBrokerWithDB(store.NewMem())
	b.seedFunds = 5
	b.mail = enabledMailer(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		cap.add("", "", string(body))
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id":"stub"}`)),
			Header:     http.Header{},
		}, nil
	})
	return b, cap
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, in any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(in)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	// Browsers send Origin on every cross-origin call, and these routes now require it.
	req.Header.Set("Origin", testWebOrigin)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// codeFromMail digs the six-digit code out of the captured provider payload.
func codeFromMail(t *testing.T, cap *capturedMail) string {
	t.Helper()
	sent := cap.all()
	require.NotEmpty(t, sent, "a code should have been mailed")
	last := sent[len(sent)-1].text
	for _, field := range strings.Fields(strings.ReplaceAll(last, `\n`, " ")) {
		trimmed := strings.Trim(field, `"',.\`)
		if len(trimmed) == 6 && strings.Trim(trimmed, "0123456789") == "" {
			return trimmed
		}
	}
	t.Fatalf("no six-digit code found in %q", last)
	return ""
}

func waitForMail(t *testing.T, cap *capturedMail, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(cap.all()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d mails, saw %d", n, len(cap.all()))
}

func TestAKnownAndAnUnknownAddressGetIdenticalAnswers(t *testing.T) {
	// The response must not reveal whether an address has an account. This flow achieves
	// that by never asking - there is no branch to leak.
	b, _ := emailTestBroker(t)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "pk-1", Login: "known@rogerai.fm",
		Email: "known@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))

	known := postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "known@rogerai.fm"})
	unknown := postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "nobody@rogerai.fm"})

	require.Equal(t, known.Code, unknown.Code)
	require.Equal(t, known.Body.String(), unknown.Body.String(),
		"byte-for-byte identical, or the response is an account oracle")
}

func TestAnUnmailableAddressIsRefusedAndNothingIsSent(t *testing.T) {
	b, cap := emailTestBroker(t)
	for _, bad := range []string{"", "not-an-address", "someone@localhost", "a@b.com\ncc: x@y.com"} {
		rec := postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": bad})
		require.Equal(t, http.StatusBadRequest, rec.Code, "for %q", bad)
	}
	require.Empty(t, cap.all(), "no mail may be enqueued for an address we cannot reach")
}

func TestSignInWithAMailedCodeCreatesASession(t *testing.T) {
	b, cap := emailTestBroker(t)
	rec := postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	require.Equal(t, http.StatusOK, rec.Code)
	waitForMail(t, cap, 1)

	code := codeFromMail(t, cap)
	verify := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{
		"email": "someone@rogerai.fm", "code": code,
	})
	require.Equal(t, http.StatusOK, verify.Code)

	var sawSession, sawHint bool
	for _, c := range verify.Result().Cookies() {
		switch c.Name {
		case sessionCookie:
			sawSession = true
			require.True(t, c.HttpOnly, "the real credential is HttpOnly")
			require.True(t, c.Secure)
		case signedInHint:
			sawHint = true
			require.False(t, c.HttpOnly, "the hint is the JS-readable companion")
		}
	}
	require.True(t, sawSession, "an accepted code mints the same session the other providers mint")
	require.True(t, sawHint)
}

func TestAWrongCodeIsRefusedUniformly(t *testing.T) {
	b, cap := emailTestBroker(t)
	postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	waitForMail(t, cap, 1)

	wrong := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{
		"email": "someone@rogerai.fm", "code": "000000",
	})
	unknown := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{
		"email": "never-asked@rogerai.fm", "code": "000000",
	})
	require.Equal(t, http.StatusBadRequest, wrong.Code)
	require.Equal(t, wrong.Code, unknown.Code)
	require.Equal(t, wrong.Body.String(), unknown.Body.String(),
		"a wrong code and an address that never had one must look alike")
}

func TestACodeIsSpentOnFirstUse(t *testing.T) {
	b, cap := emailTestBroker(t)
	postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	waitForMail(t, cap, 1)
	code := codeFromMail(t, cap)

	first := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{
		"email": "someone@rogerai.fm", "code": code,
	})
	require.Equal(t, http.StatusOK, first.Code)

	replay := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{
		"email": "someone@rogerai.fm", "code": code,
	})
	require.Equal(t, http.StatusBadRequest, replay.Code, "a spent code is not a spare credential")
}

func TestTheMailCarriesNoOneClickSignInLink(t *testing.T) {
	// A followed link authenticates whoever followed it - including a corporate mail
	// scanner that fetches every URL it sees.
	b, cap := emailTestBroker(t)
	postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	waitForMail(t, cap, 1)

	body := cap.all()[0].text
	require.NotContains(t, strings.ToLower(body), "http://")
	require.NotContains(t, strings.ToLower(body), "https://")
	require.Contains(t, body, "was not you", "it tells a person who did not ask that they need do nothing")
}

func TestNoLogLineCarriesTheAddressOrTheCode(t *testing.T) {
	// Found by reading this flow's own test output: the mailer logged the full recipient
	// AND the subject, and the subject used to carry the code - so an ordinary log line
	// held a live credential next to the address it belonged to. Logs are shipped,
	// retained, searched and pasted into tickets; none of that has the account store's
	// protections. Permanent regression scenario.
	// The mailer sends in a goroutine, so the capture has to be safe to write from another
	// goroutine while this one reads it. A plain Builder here is a data race, and the race
	// detector is right to say so.
	logged := &lockedBuffer{}
	restore := log.Writer()
	log.SetOutput(logged)
	defer log.SetOutput(restore)

	b, cap := emailTestBroker(t)
	postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	waitForMail(t, cap, 1)
	code := codeFromMail(t, cap)

	out := logged.String()
	require.NotContains(t, out, "someone@rogerai.fm", "a full recipient address must never reach a log")
	require.NotContains(t, out, code, "a live sign-in code must never reach a log")
	require.Contains(t, out, "@rogerai.fm", "the masked form still identifies a delivery in an incident")
}

func TestNeitherTheAddressNorTheCodeIsMailedToTheWrongPlace(t *testing.T) {
	b, cap := emailTestBroker(t)
	postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	waitForMail(t, cap, 1)
	require.Contains(t, cap.all()[0].text, "someone@rogerai.fm", "the code goes to the address that asked")
}

func TestSignInIsRefusedLoudlyWhenTheMailerIsUnconfigured(t *testing.T) {
	// Claiming a code was sent when no provider is configured leaves a person waiting for
	// mail that will never arrive, with no way to discover why.
	b, _ := emailTestBroker(t)
	b.mail = &mailer{} // no api key => disabled

	rec := postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "sent")
}

func TestCodeRequestsAreRateLimited(t *testing.T) {
	b, _ := emailTestBroker(t)
	var lastCode int
	for i := 0; i < 12; i++ {
		rec := postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
		lastCode = rec.Code
	}
	require.Equal(t, http.StatusTooManyRequests, lastCode,
		"our mailer must not be usable to flood an inbox")
}

func TestAVerifiedAddressReachesTheExistingAccountRatherThanAParallelOne(t *testing.T) {
	b, cap := emailTestBroker(t)
	// An account that already proved it holds the address, and also holds a GitHub link.
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "pk-1", GitHubID: 7, Login: "octocat",
		Email: "octocat@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
	}))

	postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "octocat@rogerai.fm"})
	waitForMail(t, cap, 1)
	code := codeFromMail(t, cap)

	rec := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{
		"email": "octocat@rogerai.fm", "code": code,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			login, _, wallet, _, ok := b.verifySessionFull(c.Value)
			require.True(t, ok)
			require.Equal(t, "octocat", login, "it reaches the existing account, not a new one")
			require.Equal(t, "u_gh_7", wallet,
				"and keeps the wallet it already had - linking is not merging")
		}
	}
}

func TestAnEmailSessionMayApproveADeviceLogin(t *testing.T) {
	// The whole point of a first-party account: it works everywhere the others do,
	// including binding a CLI key.
	b, cap := emailTestBroker(t)
	b.devices = newDeviceFlow()

	pending, err := b.deviceFlow().Start("cli-pubkey")
	require.NoError(t, err)

	postJSON(t, b.emailStart, "/auth/email/start", map[string]string{"email": "someone@rogerai.fm"})
	waitForMail(t, cap, 1)
	code := codeFromMail(t, cap)
	verify := postJSON(t, b.emailVerify, "/auth/email/verify", map[string]string{
		"email": "someone@rogerai.fm", "code": code,
	})
	require.Equal(t, http.StatusOK, verify.Code)

	var session *http.Cookie
	for _, c := range verify.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	require.NotNil(t, session)

	body, _ := json.Marshal(map[string]string{"user_code": pending.UserCode})
	req := httptest.NewRequest(http.MethodPost, "/auth/device/approve", strings.NewReader(string(body)))
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	b.deviceApprove(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "an email session is a real identity, not a second-class one")

	// The CLI's poll now completes, bound to the key recorded at issue.
	res, err := b.deviceFlow().Poll(pending.DeviceCode, "cli-pubkey")
	require.NoError(t, err)
	require.Equal(t, "someone@rogerai.fm", res.Account)
	require.Equal(t, "cli-pubkey", res.BoundKey)

	// And the owner row now records the address as verified.
	o, ok, err := b.db.OwnerByVerifiedEmail("someone@rogerai.fm")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "cli-pubkey", o.Pubkey)
}

// lockedBuffer is a writer safe to share with the mailer's send goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
