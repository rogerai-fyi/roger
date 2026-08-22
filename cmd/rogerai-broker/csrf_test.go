package main

// Cross-site request forgery on the credentialed browser surfaces.
//
// Found by the pre-push audit, and it is the attack the device-approval design exists to
// prevent: the session cookie is SameSite=None (it has to be - the broker is on a different
// origin from the site), so a browser attaches it to requests from ANY page. corsCreds only
// ADDS response headers when the origin is allowed; it never rejects the request. The
// browser refuses to let the attacker READ the response, which is no help at all when the
// request itself is the attack.
//
// So an attacker page could POST its own user_code to /auth/device/approve with the
// victim's cookie riding along, and bind the ATTACKER'S key to the victim's account and
// wallet. originAllowed's own comment says a session cookie authenticates only behind it;
// these routes did not honour that.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

// browserPost is a request carrying the victim's cookie from a given page.
func browserPost(t *testing.T, h http.HandlerFunc, path, origin string, cookie *http.Cookie, in any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(in)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// victimSession mints a real signed-in session cookie.
func victimSession(t *testing.T, b *broker, login string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	b.setWebSessionWallet(rec, login, 7, "u_gh_7", time.Now().Add(time.Hour).Unix())
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie minted")
	return nil
}

func TestAnAttackerPageCannotApproveADeviceWithAVictimsCookie(t *testing.T) {
	// The takeover: the attacker starts their own CLI login, then gets a victim who is
	// signed in to open a page that POSTs the attacker's user_code.
	b := testBrokerWithDB(store.NewMem())
	b.devices = newDeviceFlow()
	pending, err := b.deviceFlow().Start("attacker-cli-pubkey")
	require.NoError(t, err)

	victim := victimSession(t, b, "victim")

	rec := browserPost(t, b.deviceApprove, "/auth/device/approve",
		"https://evil.example", victim, map[string]string{"user_code": pending.UserCode})

	require.NotEqual(t, http.StatusOK, rec.Code,
		"an approval from a foreign origin must be refused")

	// And nothing happened: the attacker's key is not bound to anybody.
	res, err := b.deviceFlow().Poll(pending.DeviceCode, "attacker-cli-pubkey")
	require.NoError(t, err)
	require.NotEqual(t, deviceauthApproved, string(res.Status),
		"the login must still be pending, not approved")
}

func TestTheRealSiteCanStillApprove(t *testing.T) {
	// The fix must not break the flow it protects.
	b := testBrokerWithDB(store.NewMem())
	b.devices = newDeviceFlow()
	pending, err := b.deviceFlow().Start("cli-pubkey")
	require.NoError(t, err)
	victim := victimSession(t, b, "octocat")

	rec := browserPost(t, b.deviceApprove, "/auth/device/approve",
		"https://rogerai.fm", victim, map[string]string{"user_code": pending.UserCode})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestEveryCredentialedBrowserRouteRefusesAForeignOrigin(t *testing.T) {
	// One test over all of them, so a route added later that forgets the check is caught
	// here rather than by an audit.
	b := testBrokerWithDB(store.NewMem())
	b.devices = newDeviceFlow()
	b.mail = enabledMailer(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})
	victim := victimSession(t, b, "victim")

	for name, h := range map[string]http.HandlerFunc{
		"device approve": b.deviceApprove,
		"device deny":    b.deviceDeny,
		"email start":    b.emailStart,
		"email verify":   b.emailVerify,
	} {
		t.Run(name, func(t *testing.T) {
			rec := browserPost(t, h, "/x", "https://evil.example", victim,
				map[string]string{"user_code": "AAAA1111", "email": "someone@rogerai.fm", "code": "000000"})
			require.NotEqual(t, http.StatusOK, rec.Code, "a foreign origin must never succeed")
		})
	}
}

func TestAForeignOriginCannotSignAVictimIntoTheAttackersAccount(t *testing.T) {
	// Login CSRF: /auth/email/verify SETS a session. If an attacker page can drive it with
	// a code they hold, the victim's browser silently becomes signed in as the ATTACKER,
	// and everything the victim then does lands in the attacker's account.
	b := testBrokerWithDB(store.NewMem())
	b.mail = enabledMailer(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})

	rec := browserPost(t, b.emailVerify, "/auth/email/verify",
		"https://evil.example", nil, map[string]string{"email": "attacker@evil.example", "code": "123456"})
	require.NotEqual(t, http.StatusOK, rec.Code)
	for _, c := range rec.Result().Cookies() {
		require.NotEqual(t, sessionCookie, c.Name, "no session may be minted from a foreign origin")
	}
}

func TestASignedCLIRequestStillNeedsNoOrigin(t *testing.T) {
	// The CLI is not a browser and sends no Origin. Requiring one on the signed routes
	// would break every `roger login` on earth, so the check belongs only on the surfaces
	// that authenticate from a cookie.
	b := testBrokerWithDB(store.NewMem())
	b.devices = newDeviceFlow()

	req := httptest.NewRequest(http.MethodPost, "/auth/device/start", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	b.deviceStart(rec, req)
	require.NotEqual(t, http.StatusForbidden, rec.Code,
		"a signed CLI request must not be rejected for having no Origin")
}

const deviceauthApproved = "approved"
