package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPayoutAndMarketFuncs covers the payout client calls, the market/band helpers, the
// balance helper, clock skew, GrantCreate, and payoutErr.
func TestPayoutAndMarketFuncs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := fakeBroker(t)

	if _, err := FetchPayoutStatus(b); err != nil {
		t.Errorf("FetchPayoutStatus: %v", err)
	}
	if url, err := FetchOnboardURL(b); err != nil || url == "" {
		t.Errorf("FetchOnboardURL = %q / %v", url, err)
	}
	if _, err := RequestPayout(b); err != nil {
		t.Errorf("RequestPayout: %v", err)
	}
	if _, err := FetchPayoutHistory(b); err != nil {
		t.Errorf("FetchPayoutHistory: %v", err)
	}

	if bal := balanceOf(b, "u_gh_1"); bal < 12.4 || bal > 12.6 {
		t.Errorf("balanceOf = %v, want ~12.5", bal)
	}
	if _, ok := BandRangeFor(b, "m1"); !ok {
		t.Error("BandRangeFor(m1) should resolve a band")
	}
	if _, ok := MarketMedianOut(b, "m1"); !ok {
		t.Error("MarketMedianOut(m1) should find the online station")
	}
	if _, ok := MarketMedianOut(b, "absent"); ok {
		t.Error("MarketMedianOut(absent) should be ok=false")
	}
	if _, ok := BrokerClockSkew(b); !ok {
		t.Error("BrokerClockSkew should parse the broker Date header")
	}

	if err := GrantCreate(b, GrantCreateOpts{Name: "petlings", Free: true, FreeSet: true}); err != nil {
		t.Errorf("GrantCreate: %v", err)
	}

	// payoutErr: explicit error field, raw text, and status fallback.
	if payoutErr(400, []byte(`{"error":"nope"}`)).Error() != "nope" {
		t.Error("payoutErr should surface the error field")
	}
	if payoutErr(500, []byte("plain message")).Error() != "plain message" {
		t.Error("payoutErr should fall back to the raw body")
	}
	if payoutErr(503, []byte("")) == nil {
		t.Error("payoutErr should always return an error")
	}
}

// TestSearchTopupBind covers Search (discover table), Topup (checkout URL + auto-open),
// and bindToken (the login binding call) against the fake broker.
func TestSearchTopupBind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := fakeBroker(t)

	if err := Search(b); err != nil {
		t.Errorf("Search: %v", err)
	}
	var opened string
	if err := Topup(b, "u_gh_1", 10, func(u string) { opened = u }); err != nil {
		t.Errorf("Topup: %v", err)
	}
	if opened == "" {
		t.Error("Topup should auto-open the checkout URL")
	}
	// bindToken posts the GitHub token to the broker, which returns the linked login.
	if login, err := bindToken(b, "gho_token"); err != nil || login == "" {
		t.Errorf("bindToken = %q / %v", login, err)
	}
}

// fakeGitHubDevice points the device-flow URLs at a local server that returns a device
// code then an access token, and restores them after the test.
func fakeGitHubDevice(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/device":
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"WXYZ-1234","verification_uri":"https://gh/device","interval":1,"expires_in":300}`))
		default: // token poll
			_, _ = w.Write([]byte(`{"access_token":"gho_token"}`))
		}
	}))
	t.Cleanup(srv.Close)
	od, oa := ghDeviceCodeURL, ghAccessTokenURL
	ghDeviceCodeURL = srv.URL + "/device"
	ghAccessTokenURL = srv.URL + "/token"
	t.Cleanup(func() { ghDeviceCodeURL, ghAccessTokenURL = od, oa })
}

// TestLoginDeviceFlow covers the full GitHub device-login flow against a local GitHub +
// the fake broker bind: startDeviceFlow, pollDeviceToken, LoginBegin/Poll, Login,
// LoginReturn — none of which reaches github.com here.
func TestLoginDeviceFlow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeGitHubDevice(t)
	b := fakeBroker(t)

	dev, err := startDeviceFlow("cid")
	if err != nil || dev.DeviceCode != "dc" || dev.Interval != 1 {
		t.Fatalf("startDeviceFlow = %+v / %v", dev, err)
	}
	if tok, err := pollDeviceToken("cid", dev); err != nil || tok != "gho_token" {
		t.Fatalf("pollDeviceToken = %q / %v", tok, err)
	}

	// LoginBegin (no poll) then LoginPoll authorizes + binds.
	d, err := LoginBegin(b, "cid")
	if err != nil || d.UserCode != "WXYZ-1234" {
		t.Fatalf("LoginBegin = %+v / %v", d, err)
	}
	if login, err := LoginPoll(b, "cid", d); err != nil || login != "octocat" {
		t.Fatalf("LoginPoll = %q / %v", login, err)
	}

	// Login (begin+poll+bind) end-to-end, then LoginReturn (data form).
	if err := Login(b, "cid"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login, err := LoginReturn(b, "cid"); err != nil || login != "octocat" {
		t.Fatalf("LoginReturn = %q / %v", login, err)
	}
	// Login with no client id is a clear error (no network).
	if err := Login(b, ""); err == nil {
		t.Error("Login with empty client id should error")
	}
}

// TestRangeLabel covers the band-range label formatting (single vs range).
func TestRangeLabel(t *testing.T) {
	if got := rangeLabel(BandRange{Min: 2, Max: 2, Stations: 1}); got != "2.00 $/1M out" {
		t.Errorf("rangeLabel(single) = %q", got)
	}
	if got := rangeLabel(BandRange{Min: 1, Max: 3, Stations: 2}); got != "1.00 ~ 3.00 $/1M out" {
		t.Errorf("rangeLabel(range) = %q", got)
	}
}

// fakeBroker answers every signed broker read/write the client makes with one permissive
// JSON blob (each decoder reads only its own fields), so the client's broker-facing
// functions can be driven to their success path without a live broker.
func fakeBroker(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", "Mon, 02 Jan 2006 15:04:05 GMT")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"user":"u_gh_1","balance":12.5,"logged_in":true,"monthly_cap":100,"monthly_spend":20,
			"cap":100,"url":"https://pay.example/checkout",
			"offers":[{"node_id":"amber-fox-m1","model":"m1","price_out":2.0,"online":true}],
			"strikes":[],"warn":false,"banned":false,
			"appeals":[],"appeal":{"id":1,"state":"open"},"ok":true,
			"github_login":"octocat","github_id":7,
			"grants":[{"id":"grant_1","name":"petlings","free":true,"status":"active","price":"free"}],
			"grant":{"id":"grant_1","name":"petlings"},"secret":"rog-grant_abc",
			"status":"none","connected":false,"payout":{"id":1,"amount":0,"state":"pending"},"payouts":[]
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestGrantLabelHelpers covers the pure grant display helpers.
func TestGrantLabelHelpers(t *testing.T) {
	if priceLabel(grantJSON{Self: true}) != "self" || priceLabel(grantJSON{Free: true}) != "free" ||
		priceLabel(grantJSON{Price: "0.2/0.5"}) != "0.2/0.5" {
		t.Error("priceLabel wrong")
	}
	if scopeLabel(nil) != "any" || scopeLabel([]string{"a", "b"}) != "a,b" {
		t.Error("scopeLabel wrong")
	}
	if numOrDash(0) != "-" || numOrDash(1.5) != "1.5" {
		t.Error("numOrDash wrong")
	}
	if capOrDash(0) != "-" || capOrDash(42) != "42" {
		t.Error("capOrDash wrong")
	}
	if expiresLabel(0) != "never" || expiresLabel(1_700_000_000) == "never" {
		t.Error("expiresLabel wrong")
	}
	if trunc("short", 10) != "short" || trunc("0123456789", 5) != "0123…" {
		t.Errorf("trunc wrong: %q", trunc("0123456789", 5))
	}
}

// TestIdentitySigning covers the local signing identity: key creation, pubkey/wallet
// derivation, and request signing (the headers the broker verifies).
func TestIdentitySigning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if len(UserPubHex()) != 64 { // 32-byte ed25519 pubkey hex
		t.Errorf("UserPubHex len = %d, want 64", len(UserPubHex()))
	}
	if SignedUserID() == "" {
		t.Error("SignedUserID should be non-empty")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://b/x", nil)
	SignRequest(req, nil)
	for _, h := range []string{"X-Roger-Pubkey", "X-Roger-Ts", "X-Roger-Sig"} {
		if req.Header.Get(h) == "" {
			t.Errorf("SignRequest missing header %s", h)
		}
	}
}

// TestAuthRoundTrip covers the local auth-state file: save -> load -> LinkedLogin,
// then LogoutReturn clears it.
func TestAuthRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, ok := loadAuth(); ok {
		t.Error("loadAuth on a fresh dir should miss")
	}
	if LinkedLogin() != "" {
		t.Error("LinkedLogin with no auth should be empty")
	}
	if err := saveAuth(authState{GitHubLogin: "octocat", GitHubID: 7, BoundAt: 1}); err != nil {
		t.Fatal(err)
	}
	if a, ok := loadAuth(); !ok || a.GitHubLogin != "octocat" {
		t.Fatalf("loadAuth after save = %+v ok=%v", a, ok)
	}
	if LinkedLogin() != "octocat" {
		t.Error("LinkedLogin should report the saved login")
	}
	if err := LogoutReturn(); err != nil {
		t.Fatalf("LogoutReturn: %v", err)
	}
	if _, ok := loadAuth(); ok {
		t.Error("LogoutReturn should clear the auth file")
	}
	// Whoami + Logout (anonymous now) print + return nil.
	if err := Whoami(); err != nil {
		t.Errorf("Whoami = %v", err)
	}
	if err := Logout(); err != nil {
		t.Errorf("Logout(anon) = %v", err)
	}
}

// TestBrokerReads covers the client's broker-facing reads/writes against a permissive
// fake broker: each returns successfully (no transport/parse error).
func TestBrokerReads(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := fakeBroker(t)

	if err := Balance(b, "u_gh_1"); err != nil {
		t.Errorf("Balance: %v", err)
	}
	if offers, err := Discover(b); err != nil || len(offers) != 1 {
		t.Errorf("Discover = %+v / %v, want 1 offer", offers, err)
	}
	if _, err := FetchBalance(b, "u_gh_1"); err != nil {
		t.Errorf("FetchBalance: %v", err)
	}
	if _, err := GetMonthlyLimit(b, "u_gh_1"); err != nil {
		t.Errorf("GetMonthlyLimit: %v", err)
	}
	if _, err := SetMonthlyLimit(b, "u_gh_1", 50); err != nil {
		t.Errorf("SetMonthlyLimit: %v", err)
	}
	if url, err := TopupURL(b, "u_gh_1", 10); err != nil || url == "" {
		t.Errorf("TopupURL = %q / %v", url, err)
	}
	if _, err := FetchStrikes(b); err != nil {
		t.Errorf("FetchStrikes: %v", err)
	}
	if ap, err := ListAppeals(b); err != nil {
		t.Errorf("ListAppeals: %v", err)
	} else if ap == nil {
		t.Error("ListAppeals should decode the appeals array (got nil)")
	}
	if res, err := FileAppeal(b, "node-1", "false positive"); err != nil || !res.OK {
		t.Errorf("FileAppeal = %+v / %v, want ok", res, err)
	}
	if err := GrantList(b); err != nil {
		t.Errorf("GrantList: %v", err)
	}
	if _, err := GrantListRows(b); err != nil {
		t.Errorf("GrantListRows: %v", err)
	}
	if secret, err := GrantCreateSecret(b, "petlings", true); err != nil || secret == "" {
		t.Errorf("GrantCreateSecret = %q / %v", secret, err)
	}
	if err := GrantShow(b, "petlings"); err != nil {
		t.Errorf("GrantShow: %v", err)
	}
	if err := GrantRevoke(b, "petlings"); err != nil {
		t.Errorf("GrantRevoke: %v", err)
	}
}
