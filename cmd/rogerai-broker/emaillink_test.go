package main

// features/auth/email_link.feature: a signed-in provider account adds a VERIFIED email.
// The session proves who is adding; the emailed code proves the address is theirs; a signed
// token (not server memory) ties the code to the account that asked, so it holds across
// broker instances.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/emailauth"
	"rogerai.fm/roger/v6/internal/store"
)

func postWithSession(t *testing.T, h http.HandlerFunc, path string, in any, c *http.Cookie, origin string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(in)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func ghCookie(b *broker, login string, gid int64) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: b.signSession(login, gid, time.Now().Add(time.Hour).Unix())}
}

// linkFixture: an operator account (GitHub 7, "octocat") with a bound CLI key.
func linkFixture(t *testing.T) (*broker, *capturedMail, *http.Cookie) {
	b, cap := emailTestBroker(t)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk-1", GitHubID: 7, Login: "octocat"}))
	return b, cap, ghCookie(b, "octocat", 7)
}

func linkStart(t *testing.T, b *broker, c *http.Cookie, addr string) (*httptest.ResponseRecorder, string) {
	rec := postWithSession(t, b.emailLinkStart, "/auth/email/link/start", map[string]string{"email": addr}, c, testWebOrigin)
	var out struct{ Token string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out.Token
}

func linkVerify(t *testing.T, b *broker, c *http.Cookie, addr, code, token string) *httptest.ResponseRecorder {
	return postWithSession(t, b.emailLinkVerify, "/auth/email/link/verify", map[string]string{"email": addr, "code": code, "token": token}, c, testWebOrigin)
}

func addAndVerify(t *testing.T, b *broker, cap *capturedMail, c *http.Cookie, addr string) *httptest.ResponseRecorder {
	n := len(cap.all())
	rec, token := linkStart(t, b, c, addr)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	waitForMail(t, cap, n+1)
	return linkVerify(t, b, c, addr, codeFromMail(t, cap), token)
}

func TestLinkRequiresASignedInSessionAndMailsNothingWithout(t *testing.T) {
	b, cap, _ := linkFixture(t)
	rec, _ := linkStart(t, b, nil, "me@example.com")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = linkVerify(t, b, nil, "me@example.com", "123456", "x")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, cap.all(), "nothing is mailed to an unauthenticated caller")
}

func TestLinkRequiresTheWebOrigin(t *testing.T) {
	b, cap, c := linkFixture(t)
	rec := postWithSession(t, b.emailLinkStart, "/auth/email/link/start", map[string]string{"email": "me@example.com"}, c, "https://evil.example")
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = postWithSession(t, b.emailLinkStart, "/auth/email/link/start", map[string]string{"email": "me@example.com"}, c, "")
	require.Equal(t, http.StatusForbidden, rec.Code, "no Origin at all is refused too")
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, cap.all())
}

func TestAddingAnAddressRecordsItVerifiedOnThisAccountOnly(t *testing.T) {
	b, cap, c := linkFixture(t)
	rec := addAndVerify(t, b, cap, c, "me@example.com")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	o, ok, _ := b.db.OwnerByVerifiedEmail("me@example.com")
	require.True(t, ok)
	require.Equal(t, "pk-1", o.Pubkey)
	require.Equal(t, "octocat", o.Login, "login unchanged")
	require.Equal(t, int64(7), o.GitHubID)
}

func TestAfterAddingTheEmailedCodeReachesTheSameAccount(t *testing.T) {
	b, cap, c := linkFixture(t)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "me@example.com").Code)

	sess := signInByEmail(t, b, cap, "me@example.com")
	_, gid, wallet, _, ok := b.verifySessionFull(sess.Value)
	require.True(t, ok)
	require.Equal(t, int64(7), gid, "the emailed code now reaches the GitHub account")
	require.Equal(t, "u_gh_7", wallet)
	require.Equal(t, http.StatusOK, getWithSession(b.stations, "/stations", sess).Code, "with its stations")
}

func TestAnAddressVerifiedOnAnotherAccountIsRefusedWithoutNamingIt(t *testing.T) {
	b, cap, c := linkFixture(t)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk-2", GitHubID: 8, Login: "other", Email: "taken@example.com", EmailVerifiedAt: time.Now().Unix()}))

	rec := addAndVerify(t, b, cap, c, "taken@example.com")
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "cannot be added")
	require.NotContains(t, rec.Body.String(), "other", "the other account is not named")
	require.NotContains(t, rec.Body.String(), "pk-2")
	o, _, _ := b.db.OwnerByVerifiedEmail("taken@example.com")
	require.Equal(t, "pk-2", o.Pubkey, "the other account is untouched")
	mine, _, _ := b.db.OwnerByPubkey("pk-1")
	require.Zero(t, mine.EmailVerifiedAt)
}

func TestAddingTheSameAddressAgainIsIdempotent(t *testing.T) {
	b, cap, c := linkFixture(t)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "me@example.com").Code)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "me@example.com").Code)
	o, ok, _ := b.db.OwnerByVerifiedEmail("me@example.com")
	require.True(t, ok)
	require.Equal(t, "pk-1", o.Pubkey)
}

func TestReplacingTheAddressDropsTheOldProof(t *testing.T) {
	b, cap, c := linkFixture(t)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "old@example.com").Code)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "new@example.com").Code)
	_, found, _ := b.db.OwnerByVerifiedEmail("old@example.com")
	require.False(t, found)
	o, found, _ := b.db.OwnerByVerifiedEmail("new@example.com")
	require.True(t, found)
	require.Equal(t, "pk-1", o.Pubkey)
}

func TestAWrongSpentOrExpiredCodeIsRefusedUniformlyAndNothingIsRecorded(t *testing.T) {
	b, cap, c := linkFixture(t)
	rec, token := linkStart(t, b, c, "me@example.com")
	require.Equal(t, http.StatusOK, rec.Code)
	waitForMail(t, cap, 1)
	code := codeFromMail(t, cap)

	wrong := linkVerify(t, b, c, "me@example.com", "000000", token)
	require.Equal(t, http.StatusBadRequest, wrong.Code)
	msg := wrong.Body.String()

	require.Equal(t, http.StatusOK, linkVerify(t, b, c, "me@example.com", code, token).Code)
	spent := linkVerify(t, b, c, "me@example.com", code, token)
	require.Equal(t, http.StatusBadRequest, spent.Code)
	require.Equal(t, msg, spent.Body.String(), "a wrong and a spent code look identical")

	// a code past its TTL is refused (a tiny-TTL flow, so no sleeping through real windows)
	b.emailLinks = emailauth.New(emailauth.Config{TTL: 30 * time.Millisecond})
	rec2, token2 := linkStart(t, b, c, "late@example.com")
	require.Equal(t, http.StatusOK, rec2.Code)
	waitForMail(t, cap, 2)
	code2 := codeFromMail(t, cap)
	time.Sleep(80 * time.Millisecond)
	late := linkVerify(t, b, c, "late@example.com", code2, token2)
	require.Equal(t, http.StatusBadRequest, late.Code)
	require.Equal(t, msg, late.Body.String())
	_, found, _ := b.db.OwnerByVerifiedEmail("late@example.com")
	require.False(t, found)
}

// The signed token carries its own expiry, so a stolen-and-held token does not outlive the code.
func TestAnExpiredTokenIsRefused(t *testing.T) {
	b, cap, c := linkFixture(t)
	rec, token := linkStart(t, b, c, "me@example.com")
	require.Equal(t, http.StatusOK, rec.Code)
	waitForMail(t, cap, 1)
	code := codeFromMail(t, cap)
	b.linkClock = func() time.Time { return time.Now().Add(emailCodeTTL + time.Minute) }
	require.Equal(t, http.StatusBadRequest, linkVerify(t, b, c, "me@example.com", code, token).Code)
}

func TestATokenFromAnotherAccountOrForAnotherAddressOrTamperedIsRefused(t *testing.T) {
	b, cap, c := linkFixture(t)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk-2", GitHubID: 8, Login: "mallory"}))
	mallory := ghCookie(b, "mallory", 8)

	_, token := linkStart(t, b, c, "me@example.com")
	waitForMail(t, cap, 1)
	code := codeFromMail(t, cap)

	// another account's session cannot redeem it (even with the real code)
	require.Equal(t, http.StatusBadRequest, linkVerify(t, b, mallory, "me@example.com", code, token).Code)
	// a different address with the same token
	require.Equal(t, http.StatusBadRequest, linkVerify(t, b, c, "else@example.com", code, token).Code)
	// a tampered token
	require.Equal(t, http.StatusBadRequest, linkVerify(t, b, c, "me@example.com", code, token+"x").Code)
	require.Equal(t, http.StatusBadRequest, linkVerify(t, b, c, "me@example.com", code, "").Code)
	_, found, _ := b.db.OwnerByVerifiedEmail("me@example.com")
	require.False(t, found, "nothing was recorded by any refused attempt")
	// and the real holder can STILL redeem it: the refused attempts did not burn the code
	require.Equal(t, http.StatusOK, linkVerify(t, b, c, "me@example.com", code, token).Code)
}

func TestASessionWithNoOwnerRowCannotLinkAndNothingIsMailed(t *testing.T) {
	b, cap := emailTestBroker(t)
	rec, _ := linkStart(t, b, ghCookie(b, "newbie", 99), "me@example.com")
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "roger login")
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, cap.all())
}

func TestADeletedAccountCannotLink(t *testing.T) {
	b, cap, c := linkFixture(t)
	b.seedFunds = 0
	ok, err := b.db.DeleteAccount("octocat")
	require.NoError(t, err)
	require.True(t, ok)
	rec, _ := linkStart(t, b, c, "me@example.com")
	require.GreaterOrEqual(t, rec.Code, 400)
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, cap.all())
}

func TestAnEmailSessionCannotAddAnotherAddress(t *testing.T) {
	b, cap := emailTestBroker(t)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk-e", Login: "op@example.com", Email: "op@example.com", EmailVerifiedAt: time.Now().Unix()}))
	sess := signInByEmail(t, b, cap, "op@example.com")
	n := len(cap.all())
	rec, _ := linkStart(t, b, sess, "second@example.com")
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "sign-in address")
	time.Sleep(100 * time.Millisecond)
	require.Len(t, cap.all(), n, "no further mail")
}

func TestLinkRequestsAreRateLimited(t *testing.T) {
	b, cap, c := linkFixture(t)
	var limited bool
	for i := 0; i < 20; i++ {
		rec, _ := linkStart(t, b, c, "me@example.com")
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	require.True(t, limited, "requests for one address are eventually refused")
	time.Sleep(100 * time.Millisecond)
	require.Less(t, len(cap.all()), 20, "no further mail once limited")
}

func TestLinkNeverLogsTheAddressOrTheCode(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	b, cap, c := linkFixture(t)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "private.person@example.com").Code)
	code := codeFromMail(t, cap)
	require.NotContains(t, buf.String(), "private.person", "the address is never logged in the clear")
	require.NotContains(t, buf.String(), code)
}

func TestLinkingNeverMergesWalletsOrBalances(t *testing.T) {
	b, cap, c := linkFixture(t)
	b.seedFunds = 5
	// a separate email-only account exists for the same address, with its own balance
	_, _ = b.db.AddCredits(walletForEmail("me@example.com"), 3)
	_, _ = b.db.AddCredits("u_gh_7", 2)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "me@example.com").Code)
	a, _ := b.db.BalanceOf(walletForEmail("me@example.com"), 0)
	g, _ := b.db.BalanceOf("u_gh_7", 0)
	require.InDelta(t, 3, a, 0.0001, "the email wallet is untouched")
	require.InDelta(t, 2, g, 0.0001, "the GitHub wallet is untouched")
}

// Spec: "Linking never merges wallets ... the person is told how to have them merged
// deliberately". When a separate email-only account for the address holds a balance, the
// success reply says so (it is stranded, not lost) and nothing moves.
func TestLinkTellsTheOwnerWhenASeparateEmailWalletHoldsAFunds(t *testing.T) {
	b, cap, c := linkFixture(t)
	_, _ = b.db.AddCredits(walletForEmail("me@example.com"), 3)
	rec := addAndVerify(t, b, cap, c, "me@example.com")
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.InDelta(t, 3, out["separate_email_balance"], 0.0001, "the stranded balance is reported")
	require.Contains(t, out["merge_note"], "merge", "and the person is told it can be merged deliberately")
	bal, _ := b.db.BalanceOf(walletForEmail("me@example.com"), 0)
	require.InDelta(t, 3, bal, 0.0001, "nothing moved")
}

func TestLinkSaysNothingAboutMergingWhenThereIsNoSeparateWallet(t *testing.T) {
	b, cap, c := linkFixture(t)
	b.seedFunds = 0
	rec := addAndVerify(t, b, cap, c, "fresh@example.com")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "separate_email_balance")
	require.NotContains(t, rec.Body.String(), "merge_note")
}

// '|' is a legal character in an email local part; the signed token must not be confused by it.
func TestAnAddressWithAPipeCanBeLinked(t *testing.T) {
	b, cap, c := linkFixture(t)
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, c, "a|b@example.com").Code)
	_, found, _ := b.db.OwnerByVerifiedEmail("a|b@example.com")
	require.True(t, found)
}

// The request budget is per ACCOUNT (the spec says per session), not per IP: one account
// spraying addresses must not lock out another account behind the same address (a campus or
// office NAT), and an account cannot dodge its budget by changing IP.
func TestTheLinkBudgetIsPerAccountNotPerIP(t *testing.T) {
	b, cap, a := linkFixture(t)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: "pk-2", GitHubID: 8, Login: "neighbour"}))
	other := ghCookie(b, "neighbour", 8)

	limited := false
	for i := 0; i < 40; i++ {
		rec, _ := linkStart(t, b, a, fmt.Sprintf("spray%d@example.com", i))
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	require.True(t, limited, "one account is eventually limited across addresses")
	rec, _ := linkStart(t, b, other, "neighbour@example.com")
	require.Equal(t, http.StatusOK, rec.Code, "a different account on the same IP is unaffected")
	_ = cap
}

// An email session that was already live when its address got linked to a GitHub account must
// not read that account's data while still carrying the old email wallet (a mixed identity).
// It resolves no owner until it signs in again, and a fresh sign-in is the full account.
func TestALiveEmailSessionIsNotAMixedIdentityAfterTheAddressIsLinked(t *testing.T) {
	b, cap, gh := linkFixture(t)
	oldEmailSession := signInByEmail(t, b, cap, "me@example.com") // a separate email account, signed in
	require.Equal(t, http.StatusOK, addAndVerify(t, b, cap, gh, "me@example.com").Code)

	require.Equal(t, http.StatusForbidden, getWithSession(b.stations, "/stations", oldEmailSession).Code,
		"the stale session does not read the GitHub account's stations with the email wallet")
	fresh := signInByEmail(t, b, cap, "me@example.com")
	require.Equal(t, http.StatusOK, getWithSession(b.stations, "/stations", fresh).Code, "a fresh sign-in is the full account")
}

// SECURITY: the link token is signed with a key derived from the session key, and must never
// be interchangeable with a session cookie. A '|' is legal in an email local part, so an
// attacker chooses an address that makes the token's signed payload parse as a session
// ("login|gid|wallet|exp|sub") naming a VICTIM's wallet. With one shared key, re-encoding the
// token's payload as a cookie forged that session.
func TestALinkTokenCanNeverBeReplayedAsASessionCookie(t *testing.T) {
	b, _, c := linkFixture(t)
	evil := "a|u_gh_999|9999999999|s@example.com" // wallet "u_gh_999" (the victim), far-future expiry
	rec, token := linkStart(t, b, c, evil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	parts := strings.SplitN(token, ".", 2)
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	// the forgery: the signed text of the token ("link|"+payload) re-encoded as a session cookie
	forged := base64.RawURLEncoding.EncodeToString([]byte("link|"+string(raw))) + "." + parts[1]

	_, _, wallet, _, ok := b.verifySessionFull(forged)
	require.False(t, ok, "a link token is never a session (resolved wallet %q)", wallet)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: forged})
	w := httptest.NewRecorder()
	b.me(w, req)
	require.NotContains(t, w.Body.String(), "u_gh_999", "the victim's wallet is not reachable with a forged cookie")
}

// A session payload whose numeric fields do not parse is not a session (defense in depth: the
// old reader ignored the parse error and used 0).
func TestASessionWithAnUnparseableGitHubIdIsRejected(t *testing.T) {
	b, _, _ := linkFixture(t)
	payload := "octocat|not-a-number|u_gh_7|9999999999"
	mac := hmac.New(sha256.New, b.sessionKey())
	mac.Write([]byte(payload))
	val := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	_, _, _, _, ok := b.verifySessionFull(val)
	require.False(t, ok)

	payload = "octocat|7|u_gh_7|not-a-time"
	mac = hmac.New(sha256.New, b.sessionKey())
	mac.Write([]byte(payload))
	val = base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	_, _, _, _, ok = b.verifySessionFull(val)
	require.False(t, ok, "an unparseable expiry is not a session either")
}
