package main

// The REFUSALS on the broker-mediated device login.
//
// The happy paths are covered in deviceauth_test.go and deviceauth_e2e_test.go. What was
// not covered is every way in: the method guard, the CORS preflight, and each way a
// request can be rejected. These are not error handling in the incidental sense - this is
// the boundary of a login flow, and each refusal below is the only thing standing between
// an unsigned or unauthenticated caller and someone else's account.
//
// Each asserts the STATUS and the operator-facing message, because a 401 that says the
// wrong thing sends a person to fix something that was never broken.

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// deviceReq builds a request the way the /device page does: cross-origin, credentialed.
func deviceReq(t *testing.T, path string, payload any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	r.Header.Set("Origin", testWebOrigin)
	return r
}

// --- deviceStart -----------------------------------------------------------

// Starting a login is what mints the code the human will approve. An unsigned caller must
// not be able to mint one: the whole flow binds an approval to the KEY that started it,
// so a code with no key behind it is an approval waiting to be bound to anyone.
func TestDeviceStartRefusesAnUnsignedRequest(t *testing.T) {
	b, _ := deviceBroker(t)
	r := deviceReq(t, "/auth/device/start", map[string]string{})
	w := httptest.NewRecorder()
	b.deviceStart(w, r)

	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "signed request")
}

// The method guard. A login start is a write, and a GET that fell through to it would be
// reachable from a link or a prefetch.
func TestDeviceStartRefusesANonPost(t *testing.T) {
	b, _ := deviceBroker(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(method, "/auth/device/start", nil)
		r.Header.Set("Origin", testWebOrigin)
		w := httptest.NewRecorder()
		b.deviceStart(w, r)
		require.Equal(t, http.StatusMethodNotAllowed, w.Code, "%s should not start a login", method)
	}
}

// The browser preflights every credentialed cross-origin call. It must be answered and
// must NOT run the handler - a preflight that started a login would mint a code nobody
// asked for.
func TestDeviceRoutesAnswerThePreflightWithoutRunning(t *testing.T) {
	b, _ := deviceBroker(t)
	for _, h := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"start", b.deviceStart, "/auth/device/start"},
		{"approve", b.deviceApprove, "/auth/device/approve"},
		{"deny", b.deviceDeny, "/auth/device/deny"},
	} {
		r := httptest.NewRequest(http.MethodOptions, h.path, nil)
		r.Header.Set("Origin", testWebOrigin)
		r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		w := httptest.NewRecorder()
		h.fn(w, r)
		require.Less(t, w.Code, 400, "%s preflight should be answered, got %d", h.name, w.Code)
		require.NotContains(t, w.Body.String(), "device_code",
			"%s preflight must not run the handler", h.name)
	}
}

// --- deviceApprove ---------------------------------------------------------

// Approving is the act that hands a CLI key an account. Without a session there is no
// account to hand over, and the message has to say "sign in" rather than reporting a bad
// code - otherwise a signed-out approver retypes a code that was never the problem.
func TestDeviceApproveRefusesWithoutASession(t *testing.T) {
	b, _ := deviceBroker(t)
	code, msg := approveAs(t, b, "", "ABCD-1234")

	require.Equal(t, http.StatusUnauthorized, code, msg)
	require.Contains(t, msg, "sign in")
}

// A signed-in approver who sends no code is a client bug, not an auth failure, and must
// be told which - a 401 here would send them to sign in again pointlessly.
func TestDeviceApproveRequiresAUserCode(t *testing.T) {
	b, _ := deviceBroker(t)
	sess := githubSession(b, "alice", 4242)

	for _, payload := range []any{
		map[string]string{},
		map[string]string{"user_code": ""},
	} {
		r := deviceReq(t, "/auth/device/approve", payload)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess})
		w := httptest.NewRecorder()
		b.deviceApprove(w, r)

		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "user_code")
	}
}

// A code that never existed, or has already been used, is refused the same way - the
// approver is not told which, because "that code is not valid" is all they can act on and
// distinguishing the two would confirm which codes exist to someone guessing.
func TestDeviceApproveRefusesAnUnknownCode(t *testing.T) {
	b, _ := deviceBroker(t)
	code, msg := approveAs(t, b, githubSession(b, "alice", 4242), "ZZZZ-9999")

	require.Equal(t, http.StatusBadRequest, code, msg)
	require.Contains(t, msg, "not valid")
}

// --- deviceDeny ------------------------------------------------------------

// Deny is as privileged as approve: it cancels somebody's pending login, so it needs the
// same session gate rather than being treated as harmless because it grants nothing.
func TestDeviceDenyRefusesWithoutASession(t *testing.T) {
	b, _ := deviceBroker(t)
	r := deviceReq(t, "/auth/device/deny", map[string]string{"user_code": "ABCD-1234"})
	w := httptest.NewRecorder()
	b.deviceDeny(w, r)

	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "sign in")
}

func TestDeviceDenyRequiresAUserCode(t *testing.T) {
	b, _ := deviceBroker(t)
	r := deviceReq(t, "/auth/device/deny", map[string]string{})
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: githubSession(b, "alice", 4242)})
	w := httptest.NewRecorder()
	b.deviceDeny(w, r)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "user_code")
}

// A deny that names a code nobody is waiting on must not report success: the person
// reading the page would believe they had cancelled a request that is still live.
func TestDeviceDenyRefusesAnUnknownCode(t *testing.T) {
	b, _ := deviceBroker(t)
	r := deviceReq(t, "/auth/device/deny", map[string]string{"user_code": "ZZZZ-9999"})
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: githubSession(b, "alice", 4242)})
	w := httptest.NewRecorder()
	b.deviceDeny(w, r)

	require.GreaterOrEqual(t, w.Code, 400, "an unknown code must not read as a successful deny: %s", w.Body.String())
}

// --- the flow's own guard --------------------------------------------------

// A device code is bound to the key that started it. Polling with a DIFFERENT key must
// not collect that approval - this is the property the whole flow exists to provide, and
// it is asserted here against a live approved code rather than in the abstract.
func TestAnotherKeyCannotCollectAnApprovedCode(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	_, other, _ := ed25519.GenerateKey(nil)

	d := startDevice(t, b, priv)
	userCode, _ := d["user_code"].(string)
	deviceCode, _ := d["device_code"].(string)
	require.NotEmpty(t, userCode)
	require.NotEmpty(t, deviceCode)

	status, _ := approveAs(t, b, githubSession(b, "alice", 4242), userCode)
	require.Equal(t, http.StatusOK, status)

	code, body := pollDevice(t, b, other, deviceCode)
	require.NotEqual(t, http.StatusOK, code,
		"a key that did not start the login collected its approval: %v", body)
}
