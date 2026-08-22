package main

// End-to-end tests for the broker half of the broker-mediated device login.
// Contract: features/auth/broker_mediated_login.feature.
//
// The point of this flow is that the CLI talks ONLY to us, and the human picks their
// provider on our page. So the tests below check both provider paths bind the CLI's key
// to the right account, and that neither the code nor the approval can be redirected.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func deviceBroker(t *testing.T) (*broker, *store.Mem) {
	t.Helper()
	db := store.NewMem()
	return relayBroker(db), db
}

// startDevice runs POST /auth/device/start as a CLI would: signed with its own key.
func startDevice(t *testing.T, b *broker, priv ed25519.PrivateKey) map[string]any {
	t.Helper()
	body := []byte(`{}`)
	r := httptest.NewRequest(http.MethodPost, "/auth/device/start", strings.NewReader(string(body)))
	signReq(r, priv, body)
	w := httptest.NewRecorder()
	b.deviceStart(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func pollDevice(t *testing.T, b *broker, priv ed25519.PrivateKey, deviceCode string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"device_code": deviceCode})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/auth/device/token", strings.NewReader(string(body)))
	signReq(r, priv, body)
	w := httptest.NewRecorder()
	b.deviceToken(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// approveAs posts an approval carrying a web session cookie, as the /device page does.
// testWebOrigin is one of the first-party origins originAllowed accepts by default.
const testWebOrigin = "https://rogerai.fm"

func approveAs(t *testing.T, b *broker, cookie, userCode string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"user_code": userCode})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/auth/device/approve", strings.NewReader(string(body)))
	// A real browser always sends Origin here: the site and the broker are different
	// origins, so every credentialed call from the page is cross-origin. The routes now
	// require it (CSRF defence), and a test that omitted it was modelling a request no
	// browser makes.
	r.Header.Set("Origin", testWebOrigin)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	b.deviceApprove(w, r)
	return w.Code, w.Body.String()
}

func githubSession(b *broker, login string, gid int64) string {
	return b.signSessionWallet(login, gid, "u_gh_1234", time.Now().Add(time.Hour).Unix())
}

// --- the happy path, both providers ---------------------------------------

func TestDeviceLoginBindsAGitHubApproverToTheCLIKey(t *testing.T) {
	b, db := deviceBroker(t)
	pub, priv, _ := ed25519.GenerateKey(nil)

	start := startDevice(t, b, priv)
	require.NotEmpty(t, start["device_code"])
	require.NotEmpty(t, start["user_code"])
	// The CLI must never be handed a provider endpoint.
	uri, _ := start["verification_uri"].(string)
	require.NotContains(t, strings.ToLower(uri), "github")
	require.NotContains(t, strings.ToLower(uri), "apple")

	code, _ := approveAs(t, b, githubSession(b, "alice", 1234), start["user_code"].(string))
	require.Equal(t, http.StatusOK, code)

	status, out := pollDevice(t, b, priv, start["device_code"].(string))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "approved", out["status"])

	// The CLI's key now resolves to the approver's account.
	o, ok, err := db.OwnerByPubkey(hex.EncodeToString(pub))
	require.NoError(t, err)
	require.True(t, ok, "approval must bind the CLI key to an owner")
	require.Equal(t, int64(1234), o.GitHubID)
}

func TestDeviceLoginBindsAnAppleApproverToTheCLIKey(t *testing.T) {
	b, db := deviceBroker(t)
	pub, priv, _ := ed25519.GenerateKey(nil)

	start := startDevice(t, b, priv)
	// An Apple WEB session: githubID 0, an apple wallet, and the sub carried so the
	// approval has something to bind. Without the sub there is no way to create the
	// owner row, which is why Apple could not previously work from a CLI.
	sess := b.signSessionFull("user@example.com", 0, walletForAppleSub("apple-sub-1"), "apple-sub-1",
		time.Now().Add(time.Hour).Unix())

	code, msg := approveAs(t, b, sess, start["user_code"].(string))
	require.Equal(t, http.StatusOK, code, msg)

	_, out := pollDevice(t, b, priv, start["device_code"].(string))
	require.Equal(t, "approved", out["status"])

	o, ok, err := db.OwnerByPubkey(hex.EncodeToString(pub))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "apple-sub-1", o.AppleSub, "an Apple approver must bind by sub")
	require.Zero(t, o.GitHubID)
}

// --- authorization --------------------------------------------------------

func TestStartRequiresASignedRequest(t *testing.T) {
	b, _ := deviceBroker(t)
	r := httptest.NewRequest(http.MethodPost, "/auth/device/start", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	b.deviceStart(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code, "an unsigned start would issue a code bound to nobody")
}

func TestApprovalRequiresASignedInHuman(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	start := startDevice(t, b, priv)

	code, _ := approveAs(t, b, "", start["user_code"].(string))
	require.Equal(t, http.StatusUnauthorized, code)

	code, _ = approveAs(t, b, "not-a-valid-session", start["user_code"].(string))
	require.Equal(t, http.StatusUnauthorized, code)
}

// The whole point of binding at issue: a second CLI key cannot redeem someone else's
// approved login even if it learns the device code.
func TestAnotherKeyCannotRedeemAnApprovedLogin(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	_, attacker, _ := ed25519.GenerateKey(nil)

	start := startDevice(t, b, priv)
	approveAs(t, b, githubSession(b, "alice", 1234), start["user_code"].(string))

	status, _ := pollDevice(t, b, attacker, start["device_code"].(string))
	require.NotEqual(t, http.StatusOK, status, "only the issuing key may redeem")
}

func TestPollBeforeApprovalReportsPending(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	start := startDevice(t, b, priv)

	_, out := pollDevice(t, b, priv, start["device_code"].(string))
	require.Equal(t, "pending", out["status"])
	require.Empty(t, out["account"])
}

func TestDenyIsFinal(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	start := startDevice(t, b, priv)

	body, _ := json.Marshal(map[string]string{"user_code": start["user_code"].(string)})
	r := httptest.NewRequest(http.MethodPost, "/auth/device/deny", strings.NewReader(string(body)))
	r.Header.Set("Origin", testWebOrigin)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: githubSession(b, "alice", 1234)})
	w := httptest.NewRecorder()
	b.deviceDeny(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	_, out := pollDevice(t, b, priv, start["device_code"].(string))
	require.Equal(t, "denied", out["status"])

	code, _ := approveAs(t, b, githubSession(b, "alice", 1234), start["user_code"].(string))
	require.NotEqual(t, http.StatusOK, code, "a denied login can never be approved afterwards")
}

// --- what the approval screen may read ------------------------------------

func TestDescribeShowsTheApproverWhatTheyNeedAndNothingMore(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	start := startDevice(t, b, priv)

	r := httptest.NewRequest(http.MethodGet, "/auth/device/pending?user_code="+start["user_code"].(string), nil)
	r.Header.Set("Origin", testWebOrigin)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: githubSession(b, "alice", 1234)})
	w := httptest.NewRecorder()
	b.devicePending(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, start["user_code"], out["user_code"])
	require.NotEmpty(t, out["requested_at"], "the approver must see when it was requested")
	require.Empty(t, out["device_code"], "the device code is the CLI's secret, not the approver's")
}

func TestDescribeRequiresASession(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	start := startDevice(t, b, priv)

	r := httptest.NewRequest(http.MethodGet, "/auth/device/pending?user_code="+start["user_code"].(string), nil)
	w := httptest.NewRecorder()
	b.devicePending(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// --- session compatibility -------------------------------------------------

// Old four-field sessions must keep working; they simply carry no Apple sub.
func TestLegacyFourFieldSessionStillVerifies(t *testing.T) {
	b, _ := deviceBroker(t)
	val := b.signSessionWallet("alice", 1234, "u_gh_1234", time.Now().Add(time.Hour).Unix())
	login, gid, wallet, sub, ok := b.verifySessionFull(val)
	require.True(t, ok)
	require.Equal(t, "alice", login)
	require.Equal(t, int64(1234), gid)
	require.Equal(t, "u_gh_1234", wallet)
	require.Empty(t, sub)
}

func TestFiveFieldSessionCarriesTheSub(t *testing.T) {
	b, _ := deviceBroker(t)
	val := b.signSessionFull("user@example.com", 0, "u_apple_x", "the-sub", time.Now().Add(time.Hour).Unix())
	_, gid, _, sub, ok := b.verifySessionFull(val)
	require.True(t, ok)
	require.Zero(t, gid)
	require.Equal(t, "the-sub", sub)
}

func TestATamperedSessionIsRejected(t *testing.T) {
	b, _ := deviceBroker(t)
	val := b.signSessionFull("alice", 1234, "u_gh_1234", "", time.Now().Add(time.Hour).Unix())
	_, _, _, _, ok := b.verifySessionFull(val[:len(val)-2] + "xy")
	require.False(t, ok)
}

// The remaining refusal doors, each of which measured zero. A device flow's refusals ARE
// its security model: the poll and the pending-read are what a guesser probes, and the
// method checks are what keeps a CSRF-able GET from standing in for a signed POST.
func TestDeviceFlowRefusalDoors(t *testing.T) {
	b, _ := deviceBroker(t)
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	t.Run("token: unsigned is nobody", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/auth/device/token", strings.NewReader(`{"device_code":"x"}`))
		w := httptest.NewRecorder()
		b.deviceToken(w, r)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	})
	t.Run("token: a signed poll still needs its code", func(t *testing.T) {
		body := []byte(`{}`)
		r := httptest.NewRequest(http.MethodPost, "/auth/device/token", strings.NewReader(string(body)))
		signReq(r, priv, body)
		w := httptest.NewRecorder()
		b.deviceToken(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "device_code required")
	})
	t.Run("start: GET is not a login", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/auth/device/start", nil)
		w := httptest.NewRecorder()
		b.deviceStart(w, r)
		require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
	t.Run("token: GET is not a poll", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/auth/device/token", nil)
		w := httptest.NewRecorder()
		b.deviceToken(w, r)
		require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}
