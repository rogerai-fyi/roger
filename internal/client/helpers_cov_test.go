package client

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadOrCreateUserKeyReadsExisting covers the load-existing-key branch: a valid key
// already on disk is read back (not regenerated).
func TestLoadOrCreateUserKeyReadsExisting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, priv, _ := ed25519.GenerateKey(nil)
	if err := os.MkdirAll(filepath.Dir(userKeyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userKeyPath(), []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Reset the process cache so the read path runs, restore after.
	old := userKeyOnce
	userKeyOnce = nil
	t.Cleanup(func() { userKeyOnce = old })

	got := LoadOrCreateUserKey()
	if hex.EncodeToString(got) != hex.EncodeToString(priv) {
		t.Error("LoadOrCreateUserKey should read the existing key, not regenerate")
	}
}

// TestLoginBeginError covers LoginBegin's device-flow error branch (github returns
// garbage -> startDeviceFlow fails).
func TestLoginBeginError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	old := ghDeviceCodeURL
	ghDeviceCodeURL = srv.URL
	defer func() { ghDeviceCodeURL = old }()
	if _, err := LoginBegin("http://broker", "cid"); err == nil {
		t.Error("LoginBegin should error when the device-code request fails")
	}
}

// fakeBrokerNoOffers answers /bands/resolve (and everything) with an empty offer set,
// so ResolveBand reports "no station" - the uniform negative case.
func fakeBrokerNoOffers(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"offers":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestSmallPureHelpers covers clamp01, maxInt, and tpsLabel's zero branch.
func TestSmallPureHelpers(t *testing.T) {
	if clamp01(-0.5) != 0 || clamp01(1.5) != 1 || clamp01(0.4) != 0.4 {
		t.Error("clamp01 wrong")
	}
	if maxInt(3, 7) != 7 || maxInt(9, 2) != 9 {
		t.Error("maxInt wrong")
	}
	if itoa(0) != "0" || itoa(42) != "42" || itoa(-7) != "-7" {
		t.Errorf("itoa wrong: %q %q %q", itoa(0), itoa(42), itoa(-7))
	}
	if tpsLabel(0) != "- t/s" || tpsLabel(120) == "- t/s" {
		t.Errorf("tpsLabel wrong: %q / %q", tpsLabel(0), tpsLabel(120))
	}
}

// TestBalanceLoggedOut covers Balance's not-logged-in branch (logged_in:false ->
// the "run roger login" message, not a misleading $0).
func TestBalanceLoggedOut(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user":"anon","balance":0,"logged_in":false}`))
	}))
	t.Cleanup(srv.Close)
	if err := Balance(srv.URL, ""); err != nil {
		t.Errorf("Balance(logged out) = %v, want nil", err)
	}
}

// TestBrokerErrorBranches drives the broker-call functions against a broker that fails
// every request, exercising their transport/error branches (they must return an error or
// a clean non-fatal result, never panic).
func TestBrokerErrorBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "broker down", http.StatusInternalServerError)
	}))
	t.Cleanup(errSrv.Close)
	b := errSrv.URL

	// These return an explicit error on a failing broker.
	if _, err := GetMonthlyLimit(b, "u"); err == nil {
		t.Error("GetMonthlyLimit should error on a 500 broker")
	}
	if _, err := SetMonthlyLimit(b, "u", 5); err == nil {
		t.Error("SetMonthlyLimit should error on a 500 broker")
	}
	if _, err := FetchPayoutStatus(b); err == nil {
		t.Error("FetchPayoutStatus should error on a 500 broker")
	}
	if _, err := FetchOnboardURL(b); err == nil {
		t.Error("FetchOnboardURL should error on a 500 broker")
	}
	if _, err := GrantCreateSecret(b, "x", true); err == nil {
		t.Error("GrantCreateSecret should error on a 500 broker")
	}
	// These print + return nil (tolerant), but still exercise the error path internally.
	_ = Search(b)
	_ = GrantList(b)
	_ = GrantRevoke(b, "x")
	_ = Balance(b, "u")
	// A totally unreachable broker exercises the transport-error branch.
	dead := "http://127.0.0.1:0"
	if _, err := FetchPayoutHistory(dead); err == nil {
		t.Error("FetchPayoutHistory should error on an unreachable broker")
	}
}

// TestTpsLevel covers the throughput -> lit-bar-count mapping across every band
// boundary of the 5-bar staircase meter.
func TestTpsLevel(t *testing.T) {
	cases := map[float64]int{0: 0, 10: 1, 30: 1, 100: 2, 200: 3, 400: 4, 700: 5}
	for tps, want := range cases {
		if got := tpsLevel(tps); got != want {
			t.Errorf("tpsLevel(%v) = %d, want %d", tps, got, want)
		}
	}
}

// TestPrefWeightsFailover covers the routing-preference weight table (client side).
func TestPrefWeightsFailover(t *testing.T) {
	if prefWeights("cheap").kPrice != 0.45 || prefWeights("fast").priceExp != 1.5 ||
		prefWeights("reliable").kPrice != 0.20 || prefWeights("balanced").kPrice != 0.25 {
		t.Error("prefWeights table wrong")
	}
}

// TestMonthlyNotice covers the cap-warning tail: under, approaching (>=80%), and reached.
func TestMonthlyNotice(t *testing.T) {
	if monthlyNotice(1, 0) != "" {
		t.Error("no cap -> empty")
	}
	if monthlyNotice(10, 100) != "" {
		t.Error("comfortably under -> empty")
	}
	if monthlyNotice(85, 100) == "" {
		t.Error("80%+ should warn")
	}
	if monthlyNotice(100, 100) == "" {
		t.Error("at the cap should warn LIMIT REACHED")
	}
}

// TestParsePriceHeader covers the "in=..;out=.." price header parse incl. malformed parts.
func TestParsePriceHeader(t *testing.T) {
	in, out := parsePriceHeader("in=1.5;out=2.5")
	if in != 1.5 || out != 2.5 {
		t.Errorf("parsePriceHeader = %v/%v, want 1.5/2.5", in, out)
	}
	in, out = parsePriceHeader("garbage;out=3;noeq")
	if in != 0 || out != 3 {
		t.Errorf("parsePriceHeader(partial) = %v/%v, want 0/3", in, out)
	}
}

// TestLogoutWhoamiLoggedIn covers the logged-in branches of Logout (clears the auth
// file) and Whoami (reports the linked login), using a real local auth file.
func TestLogoutWhoamiLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveAuth(authState{GitHubLogin: "octocat", GitHubID: 7, BoundAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := Whoami(); err != nil { // logged-in branch
		t.Errorf("Whoami(logged in) = %v", err)
	}
	if err := Logout(); err != nil { // logged-in branch -> removes the file
		t.Errorf("Logout(logged in) = %v", err)
	}
	if _, ok := loadAuth(); ok {
		t.Error("Logout should have cleared the auth file")
	}
}

// TestUseOnFreq covers the private-band tune-in: a --freq resolve + within-limit --yes
// opens the channel on the requested port (relay captured, not bound).
func TestUseOnFreq(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var addr string
	captureServe(t, &addr)
	b := fakeBroker(t) // /bands/resolve returns the m1 offer
	if err := Use(b, "u_gh_1", "m1", UseOptions{Freq: "147.520", Yes: true, MaxOut: 5, Port: 8123}); err != nil {
		t.Fatalf("Use(--freq) = %v, want nil", err)
	}
	if addr != "127.0.0.1:8123" {
		t.Errorf("freq relay addr = %q, want 127.0.0.1:8123", addr)
	}

	// A frequency that resolves to no station -> clean message, nil.
	noband := fakeBrokerNoOffers(t)
	if err := Use(noband, "u_gh_1", "m1", UseOptions{Freq: "000.000", Yes: true, MaxOut: 5}); err != nil {
		t.Errorf("Use(--freq, no station) = %v, want nil", err)
	}
}
