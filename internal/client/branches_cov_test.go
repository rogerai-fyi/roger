package client

import (
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// jsonServer is a tiny per-test broker stub that answers every request with the given
// status + body (and optional headers), so a single error/edge branch can be driven
// without the permissive fakeBroker. Closed via t.Cleanup.
func jsonServer(t *testing.T, status int, body string, headers map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// deadBroker returns the URL of a server that is immediately closed, so every request
// to it fails at the transport layer (connection refused) - the unreachable branch.
func deadBroker(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// TestParseChatErrorBranches covers parseChatError's every shape: a broker error
// message (with the 402 topup hint), a raw-text body, an empty 402, a non-402 4xx with
// no body, and a sub-400 empty response.
func TestParseChatErrorBranches(t *testing.T) {
	// error.message present on a 402 -> message + topup hint.
	if got := parseChatError([]byte(`{"error":{"message":"insufficient balance"}}`), http.StatusPaymentRequired); !strings.Contains(got.Error(), TopupHint) || !strings.Contains(got.Error(), "insufficient balance") {
		t.Errorf("parseChatError(402 msg) = %q, want the message + topup hint", got)
	}
	// No error.message, raw text body on a 500 -> "<raw> (status 500)".
	if got := parseChatError([]byte(`upstream exploded`), http.StatusInternalServerError); got.Error() != "upstream exploded (status 500)" {
		t.Errorf("parseChatError(raw 500) = %q, want the raw body + status", got)
	}
	// 402 with an EMPTY body -> the synthetic insufficient-balance + topup hint.
	if got := parseChatError([]byte(``), http.StatusPaymentRequired); !strings.Contains(got.Error(), TopupHint) || !strings.Contains(got.Error(), "insufficient balance") {
		t.Errorf("parseChatError(empty 402) = %q, want the topup hint", got)
	}
	// A non-402 4xx with NO body -> the "no reply" status line.
	if got := parseChatError([]byte(``), http.StatusBadGateway); got.Error() != "the station returned status 502 with no reply" {
		t.Errorf("parseChatError(empty 502) = %q, want the no-reply status line", got)
	}
	// A sub-400 status with no choices -> the empty-response line.
	if got := parseChatError([]byte(`{}`), http.StatusOK); got.Error() != "the station sent an empty response (status 200)" {
		t.Errorf("parseChatError(empty 200) = %q, want the empty-response line", got)
	}
	// A body >= 300 bytes on a 4xx is too long to surface verbatim -> the no-reply line.
	long := `{"x":"` + strings.Repeat("z", 320) + `"}`
	if got := parseChatError([]byte(long), http.StatusInternalServerError); got.Error() != "the station returned status 500 with no reply" {
		t.Errorf("parseChatError(long 500) = %q, want the no-reply status line", got)
	}
}

// TestFailoverErrorBranches covers failoverError's reason selection and the full
// constraints suffix.
func TestFailoverErrorBranches(t *testing.T) {
	// Transport error reason + every constraint in the suffix.
	crit := Criteria{Model: "m", Confidential: true, MinTPS: 50, MaxPriceIn: 1.5, MaxPriceOut: 3.5}
	got := failoverError(crit, 0, http.ErrServerClosed)
	for _, want := range []string{"broker unreachable", "confidential", "min-tps=50", "max-in=1.5", "max-out=3.5", `"m"`} {
		if !strings.Contains(got, want) {
			t.Errorf("failoverError = %q, want it to contain %q", got, want)
		}
	}
	// A last-status reason, no constraints.
	if got := failoverError(Criteria{Model: "m"}, 503, nil); !strings.Contains(got, "last provider returned 503") || strings.Contains(got, "matching") {
		t.Errorf("failoverError(status) = %q, want the status reason and no constraints suffix", got)
	}
	// Neither error nor status -> the generic "all matching providers failed".
	if got := failoverError(Criteria{Model: "m"}, 0, nil); !strings.Contains(got, "all matching providers failed") {
		t.Errorf("failoverError(generic) = %q, want the all-failed reason", got)
	}
}

// TestSearchRendersEveryFlag covers Search's offer-row loop across the online/offline,
// confidential, free-now, measured/unmeasured-tps, and price-tier (good-price / neutral
// bars) combinations, plus the TIER column header.
func TestSearchRendersEveryFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := jsonServer(t, 0, `{"offers":[
		{"node_id":"n1","model":"m","price_in":0.1,"price_out":0.2,"price_tier":1,"online":true,"confidential":true,"free_now":true,"tps":120,"signal":80},
		{"node_id":"n2","model":"m","price_in":0.3,"price_out":0.4,"price_tier":3,"online":false,"tps":0}
	]}`, nil)

	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := Search(b)
	_ = w.Close()
	os.Stdout = orig
	raw, _ := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Search(flags) = %v, want nil", err)
	}
	out := string(raw)
	// Both station rows must render; the online offer carries its verified + FREE-now
	// flags, and the table header is printed once.
	if !strings.Contains(out, "n1") || !strings.Contains(out, "n2") {
		t.Errorf("Search should render both offer rows (n1, n2); got:\n%s", out)
	}
	if !strings.Contains(out, "FLAGS") || !strings.Contains(out, "STATUS") {
		t.Errorf("Search should print the table header; got:\n%s", out)
	}
	if !strings.Contains(out, "verified") {
		t.Errorf("the confidential offer (n1) should be flagged verified; got:\n%s", out)
	}
	if !strings.Contains(out, "FREE-now") {
		t.Errorf("the free_now offer (n1) should be flagged FREE-now; got:\n%s", out)
	}
	// The neutral $-tier renders beside the price: the TIER column header, n1's
	// editorialized cheapest tier ("$ good price"), and n2's neutral "$$$".
	if !strings.Contains(out, "TIER") {
		t.Errorf("Search should print the TIER column header; got:\n%s", out)
	}
	if !strings.Contains(out, "good price") {
		t.Errorf("the tier-1 offer (n1) should render the \"good price\" tag; got:\n%s", out)
	}
	if !strings.Contains(out, "$$$") {
		t.Errorf("the tier-3 offer (n2) should render \"$$$\"; got:\n%s", out)
	}
}

// The CLI $-tier cell render contract is tested once in internal/pricetier (TestLabel); the
// client now consumes pricetier.Label directly, so there is no client-local copy to test.

// TestBalanceNoCapBranch covers Balance's logged-in-but-no-monthly-cap branch (the
// "no cap - set one" line), distinct from the capped branch the fakeBroker drives.
func TestBalanceNoCapBranch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := jsonServer(t, 0, `{"user":"u","balance":3.25,"logged_in":true,"monthly_cap":0,"monthly_spend":1.5}`, nil)
	if err := Balance(b, "u"); err != nil {
		t.Errorf("Balance(no cap) = %v, want nil", err)
	}
}

// TestBalanceOfUnavailable covers balanceOf's error branch: a broker-down read yields
// the -1 sentinel (not a misleading 0).
func TestBalanceOfUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := balanceOf(deadBroker(t), "u"); got != -1 {
		t.Errorf("balanceOf(dead) = %v, want -1 (unavailable sentinel)", got)
	}
}

// TestTopupServiceUnavailable covers both checkout helpers' 503 (billing not
// configured) and empty-URL branches.
func TestTopupServiceUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	down := jsonServer(t, http.StatusServiceUnavailable, `{}`, nil)
	if err := Topup(down, "u", 10, nil); err == nil || !strings.Contains(err.Error(), "billing isn't configured") {
		t.Errorf("Topup(503) = %v, want a billing-not-configured error", err)
	}
	if _, err := TopupURL(down, "u", 10); err == nil || !strings.Contains(err.Error(), "billing isn't configured") {
		t.Errorf("TopupURL(503) = %v, want a billing-not-configured error", err)
	}
	// 200 but no URL in the body -> the "no checkout URL" error.
	empty := jsonServer(t, http.StatusOK, `{"credits":10}`, nil)
	if err := Topup(empty, "u", 10, nil); err == nil || !strings.Contains(err.Error(), "no checkout URL") {
		t.Errorf("Topup(empty url) = %v, want a no-checkout-URL error", err)
	}
	if _, err := TopupURL(empty, "u", 10); err == nil || !strings.Contains(err.Error(), "no checkout URL") {
		t.Errorf("TopupURL(empty url) = %v, want a no-checkout-URL error", err)
	}
	// Transport error on a dead broker.
	if err := Topup(deadBroker(t), "u", 10, nil); err == nil {
		t.Error("Topup(dead) should error")
	}
	if _, err := TopupURL(deadBroker(t), "u", 10); err == nil {
		t.Error("TopupURL(dead) should error")
	}
}

// TestMonthlyLimitUnauthorized covers the 401 "log in first" branch of both monthly
// limit helpers (distinct from the generic non-2xx and transport branches).
func TestMonthlyLimitUnauthorized(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	un := jsonServer(t, http.StatusUnauthorized, `{}`, nil)
	if _, err := GetMonthlyLimit(un, "u"); err == nil || !strings.Contains(err.Error(), "log in first") {
		t.Errorf("GetMonthlyLimit(401) = %v, want a log-in-first error", err)
	}
	if _, err := SetMonthlyLimit(un, "u", 5); err == nil || !strings.Contains(err.Error(), "log in first") {
		t.Errorf("SetMonthlyLimit(401) = %v, want a log-in-first error", err)
	}
	// Transport error on a dead broker.
	if _, err := GetMonthlyLimit(deadBroker(t), "u"); err == nil {
		t.Error("GetMonthlyLimit(dead) should error")
	}
	if _, err := SetMonthlyLimit(deadBroker(t), "u", 5); err == nil {
		t.Error("SetMonthlyLimit(dead) should error")
	}
	// A negative cap is clamped to 0 (clear) and still round-trips a 2xx.
	ok := jsonServer(t, http.StatusOK, `{"monthly_cap":0,"monthly_spend":0}`, nil)
	if info, err := SetMonthlyLimit(ok, "u", -7); err != nil || info.Cap != 0 {
		t.Errorf("SetMonthlyLimit(-7) = %+v / %v, want cap clamped to 0", info, err)
	}
}

// TestChatDetailedMetricsFromHeaders covers ChatDetailed's metric-extraction tail: a
// 200 reply carrying a signed receipt (with broker re-counts), a TPS header, and a
// price header all decode into the ChatResult.
func TestChatDetailedMetricsFromHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rec := protocol.UsageReceipt{
		PromptTokens: 11, CompletionTokens: 22,
		BrokerPromptTokens: 9, BrokerCompletionTokens: 20,
	}
	receiptHdr := protocol.EncodeReceipt(rec)
	b := jsonServer(t, http.StatusOK, `{"choices":[{"message":{"content":"pong"}}]}`, map[string]string{
		"Content-Type":       "application/json",
		"X-RogerAI-Cost":     "0.0042",
		"X-RogerAI-Provider": "node-x",
		"X-RogerAI-TPS":      "137.5",
		"X-RogerAI-Price":    "in=0.20;out=0.50",
		"X-RogerAI-Receipt":  receiptHdr,
	})
	res, err := ChatDetailed(b, "u", "m", "ping", false, 0)
	if err != nil {
		t.Fatalf("ChatDetailed = %v, want nil", err)
	}
	if res.Reply != "pong" || res.Provider != "node-x" {
		t.Errorf("reply/provider = %q/%q, want pong/node-x", res.Reply, res.Provider)
	}
	// Broker re-counts win over the node's claimed counts.
	if res.TokensIn != 9 || res.TokensOut != 20 {
		t.Errorf("tokens = %d/%d, want the broker re-counts 9/20", res.TokensIn, res.TokensOut)
	}
	if res.TPS != 137.5 {
		t.Errorf("TPS = %v, want 137.5", res.TPS)
	}
	if res.PriceIn != 0.20 || res.PriceOut != 0.50 {
		t.Errorf("price = %v/%v, want 0.20/0.50", res.PriceIn, res.PriceOut)
	}
	if res.Cost != 0.0042 {
		t.Errorf("cost = %v, want 0.0042", res.Cost)
	}
}

// TestChatDetailedReasoningFallback covers the empty-content -> reasoning fallback: when
// the model returns only hidden reasoning, that becomes the reply.
func TestChatDetailedReasoningFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := jsonServer(t, http.StatusOK, `{"choices":[{"message":{"content":"","reasoning":"thought it through"}}]}`, map[string]string{"Content-Type": "application/json"})
	res, err := ChatDetailed(b, "u", "m", "ping", false, 0)
	if err != nil {
		t.Fatalf("ChatDetailed = %v, want nil", err)
	}
	if res.Reply != "thought it through" {
		t.Errorf("reply = %q, want the reasoning fallback", res.Reply)
	}
}

// TestChatDetailedExhaustsAndSurfacesCause covers the all-attempts-failed tail: a broker
// that 5xxs every attempt is retried to exhaustion, then the last parsed cause is
// returned (not a blank turn).
func TestChatDetailedExhaustsAndSurfacesCause(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RogerAI-Provider", "flaky")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"node timed out"}}`))
	}))
	t.Cleanup(srv.Close)
	_, err := ChatDetailed(srv.URL, "u", "m", "ping", false, 0)
	if err == nil || !strings.Contains(err.Error(), "node timed out") {
		t.Errorf("ChatDetailed(all 503) = %v, want the surfaced last cause", err)
	}
	if calls < 2 {
		t.Errorf("expected retries to exhaustion, got %d calls", calls)
	}
}

// TestChatDetailedTransportError covers the transport-drop branch: a dead broker yields
// the "could not reach the broker" cause after exhausting retries.
func TestChatDetailedTransportError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := ChatDetailed(deadBroker(t), "u", "m", "ping", false, 0)
	if err == nil || !strings.Contains(err.Error(), "could not reach the broker") {
		t.Errorf("ChatDetailed(dead) = %v, want a broker-unreachable cause", err)
	}
}

// TestChatDetailedEmptyChoices covers the terminal "no choices" branch on a 2xx: an
// errorful body with no choices surfaces via parseChatError.
func TestChatDetailedEmptyChoices(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := jsonServer(t, http.StatusOK, `{"error":{"message":"empty completion"}}`, map[string]string{"Content-Type": "application/json"})
	_, err := ChatDetailed(b, "u", "m", "ping", false, 0)
	if err == nil || !strings.Contains(err.Error(), "empty completion") {
		t.Errorf("ChatDetailed(no choices) = %v, want the broker's error", err)
	}
}

// TestDiscoverBranches covers discover's transport, non-200, and decode-error branches.
func TestDiscoverBranches(t *testing.T) {
	if _, err := discover(deadBroker(t)); err == nil {
		t.Error("discover(dead) should error (transport)")
	}
	if _, err := discover(jsonServer(t, http.StatusInternalServerError, "boom", nil)); err == nil {
		t.Error("discover(500) should error (status)")
	}
	if _, err := discover(jsonServer(t, http.StatusOK, "not json", nil)); err == nil {
		t.Error("discover(bad json) should error (decode)")
	}
}

// TestSelectAlternativeAndBandRangeForDown covers the discover-error branches of
// selectAlternative and BandRangeFor (a down broker yields no alternative / no range).
func TestSelectAlternativeAndBandRangeForDown(t *testing.T) {
	dead := deadBroker(t)
	if _, ok := selectAlternative(dead, Criteria{Model: "m"}, nil); ok {
		t.Error("selectAlternative(dead) should be ok=false")
	}
	if _, ok := BandRangeFor(dead, "m"); ok {
		t.Error("BandRangeFor(dead) should be ok=false")
	}
}

// TestMarketMedianOutBranches covers the even-count median (averaged middle pair) and
// the discover-error branch.
func TestMarketMedianOutBranches(t *testing.T) {
	// Two online stations -> the median is the average of the pair.
	b := jsonServer(t, http.StatusOK, `{"offers":[
		{"node_id":"a","model":"m","price_out":1.0,"online":true},
		{"node_id":"b","model":"m","price_out":3.0,"online":true}
	]}`, nil)
	if med, ok := MarketMedianOut(b, "m"); !ok || med != 2.0 {
		t.Errorf("MarketMedianOut(even) = %v/%v, want 2.0/true", med, ok)
	}
	if _, ok := MarketMedianOut(deadBroker(t), "m"); ok {
		t.Error("MarketMedianOut(dead) should be ok=false")
	}
}

// TestResolveBandBranches covers ResolveBand's transport-error branch and the
// model-filter-excludes-all branch (a band whose offers don't match the model).
func TestResolveBandBranches(t *testing.T) {
	if _, _, ok := ResolveBand(deadBroker(t), "123.45", "m"); ok {
		t.Error("ResolveBand(dead) should be ok=false")
	}
	// Offers present, but none match the requested model -> ok=false.
	b := jsonServer(t, http.StatusOK, `{"band":{"display":"X"},"offers":[{"node_id":"n","model":"other","online":true}]}`, nil)
	if _, _, ok := ResolveBand(b, "123.45", "m"); ok {
		t.Error("ResolveBand(model mismatch) should be ok=false")
	}
	// Offers present AND match -> ok=true with the display string.
	b2 := jsonServer(t, http.StatusOK, `{"band":{"display":"147.520 MHz"},"offers":[{"node_id":"n","model":"m","price_out":1,"online":true}]}`, nil)
	offers, display, ok := ResolveBand(b2, "147.520", "m")
	if !ok || display != "147.520 MHz" || len(offers) != 1 {
		t.Errorf("ResolveBand(match) = %+v / %q / %v, want one offer with the display", offers, display, ok)
	}
}

// TestFetchBalanceErrors covers console.FetchBalance's transport and decode-error
// branches.
func TestFetchBalanceErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := FetchBalance(deadBroker(t), "u"); err == nil {
		t.Error("FetchBalance(dead) should error")
	}
	if _, err := FetchBalance(jsonServer(t, http.StatusOK, "not json", nil), "u"); err == nil {
		t.Error("FetchBalance(bad json) should error (decode)")
	}
}

// TestBrokerClockSkewBranches covers BrokerClockSkew's three negative branches:
// transport error, a missing Date header, and an unparseable Date header.
func TestBrokerClockSkewBranches(t *testing.T) {
	if _, ok := BrokerClockSkew(deadBroker(t)); ok {
		t.Error("BrokerClockSkew(dead) should be ok=false")
	}
	// 200 with no Date header (httptest sets one by default, so clear it explicitly).
	noDate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Date"] = nil
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(noDate.Close)
	if _, ok := BrokerClockSkew(noDate.URL); ok {
		t.Error("BrokerClockSkew(no Date) should be ok=false")
	}
	// A garbage Date header that http.ParseTime rejects.
	badDate := jsonServer(t, http.StatusOK, "ok", map[string]string{"Date": "not-a-date"})
	if _, ok := BrokerClockSkew(badDate); ok {
		t.Error("BrokerClockSkew(bad Date) should be ok=false")
	}
}

// TestAppealReadErrorBranches covers the non-2xx (payoutErr) branches of FetchStrikes,
// FileAppeal, and ListAppeals, plus their transport branches.
func TestAppealReadErrorBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bad := jsonServer(t, http.StatusForbidden, `{"error":"log in first"}`, nil)
	if _, err := FetchStrikes(bad); err == nil || !strings.Contains(err.Error(), "log in first") {
		t.Errorf("FetchStrikes(403) = %v, want the broker error", err)
	}
	if _, err := FileAppeal(bad, "n", "reason"); err == nil || !strings.Contains(err.Error(), "log in first") {
		t.Errorf("FileAppeal(403) = %v, want the broker error", err)
	}
	if _, err := ListAppeals(bad); err == nil || !strings.Contains(err.Error(), "log in first") {
		t.Errorf("ListAppeals(403) = %v, want the broker error", err)
	}
	dead := deadBroker(t)
	if _, err := FetchStrikes(dead); err == nil {
		t.Error("FetchStrikes(dead) should error")
	}
	if _, err := FileAppeal(dead, "n", "r"); err == nil {
		t.Error("FileAppeal(dead) should error")
	}
	if _, err := ListAppeals(dead); err == nil {
		t.Error("ListAppeals(dead) should error")
	}
}

// TestGrantCreateBranches covers GrantCreate's self/priced "kind" lines, the daily-cap
// line, the rejection (ok=false with a message), and the transport-error branch.
func TestGrantCreateBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A self grant ($0 on your own boxes) with a daily cap.
	self := jsonServer(t, http.StatusOK, `{"ok":true,"secret":"rog-grant_x","openai_api_base":"http://b/v1","grant":{"name":"mine","self":true,"daily_cap":1000}}`, nil)
	if err := GrantCreate(self, GrantCreateOpts{Name: "mine", Self: true}); err != nil {
		t.Errorf("GrantCreate(self) = %v, want nil", err)
	}
	// A priced grant (free=false derived from a set price).
	priced := jsonServer(t, http.StatusOK, `{"ok":true,"secret":"rog-grant_y","openai_api_base":"http://b/v1","grant":{"name":"sold","free":false,"price":"0.2/0.5"}}`, nil)
	if err := GrantCreate(priced, GrantCreateOpts{Name: "sold", PriceIn: 0.2, PriceOut: 0.5}); err != nil {
		t.Errorf("GrantCreate(priced) = %v, want nil", err)
	}
	// A rejection that carries an error message.
	rej := jsonServer(t, http.StatusForbidden, `{"ok":false,"error":{"message":"owner required"}}`, nil)
	if err := GrantCreate(rej, GrantCreateOpts{Name: "x"}); err == nil || !strings.Contains(err.Error(), "owner required") {
		t.Errorf("GrantCreate(rejected) = %v, want the broker message", err)
	}
	// A rejection with no message -> the status fallback.
	rej2 := jsonServer(t, http.StatusBadRequest, `{"ok":false}`, nil)
	if err := GrantCreate(rej2, GrantCreateOpts{Name: "x"}); err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Errorf("GrantCreate(rejected no msg) = %v, want the status fallback", err)
	}
	// Transport error.
	if err := GrantCreate(deadBroker(t), GrantCreateOpts{Name: "x"}); err == nil {
		t.Error("GrantCreate(dead) should error")
	}
}

// TestGrantCreateSecretRejection covers GrantCreateSecret's message + status-fallback
// rejection branches (the success + 500 branches are already covered elsewhere).
func TestGrantCreateSecretRejection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rej := jsonServer(t, http.StatusForbidden, `{"ok":false,"error":{"message":"need owner"}}`, nil)
	if _, err := GrantCreateSecret(rej, "x", true); err == nil || !strings.Contains(err.Error(), "need owner") {
		t.Errorf("GrantCreateSecret(rejected) = %v, want the broker message", err)
	}
	rej2 := jsonServer(t, http.StatusBadRequest, `{"ok":false}`, nil)
	if _, err := GrantCreateSecret(rej2, "x", true); err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Errorf("GrantCreateSecret(no msg) = %v, want the status fallback", err)
	}
}

// TestGrantShowRevokeNotFound covers GrantShow/GrantRevoke's "no grant named" branch
// (the lookup succeeds but the name is absent) and the forbidden-fetch branch.
func TestGrantShowRevokeNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A broker with grants, none named "ghost".
	withGrants := jsonServer(t, http.StatusOK, `{"grants":[{"id":"g1","name":"real","status":"active"}]}`, nil)
	if err := GrantShow(withGrants, "ghost"); err == nil || !strings.Contains(err.Error(), "no grant named") {
		t.Errorf("GrantShow(absent) = %v, want a not-found error", err)
	}
	if err := GrantRevoke(withGrants, "ghost"); err == nil || !strings.Contains(err.Error(), "no grant named") {
		t.Errorf("GrantRevoke(absent) = %v, want a not-found error", err)
	}
	// GrantShow on the present grant prints its full scope (the happy detail path).
	if err := GrantShow(withGrants, "real"); err != nil {
		t.Errorf("GrantShow(real) = %v, want nil", err)
	}
	// A forbidden /grants fetch -> the login-required error surfaces through findGrant.
	forbidden := jsonServer(t, http.StatusForbidden, `{}`, nil)
	if err := GrantShow(forbidden, "x"); err == nil || !strings.Contains(err.Error(), "roger login") {
		t.Errorf("GrantShow(403) = %v, want the login-required error", err)
	}
	if err := GrantRevoke(forbidden, "x"); err == nil || !strings.Contains(err.Error(), "roger login") {
		t.Errorf("GrantRevoke(403) = %v, want the login-required error", err)
	}
}

// TestGrantRevokeSuccessAndFailStatus covers GrantRevoke's DELETE success path and its
// non-200 failure branch.
func TestGrantRevokeSuccessAndFailStatus(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The /grants list returns one grant; the DELETE returns 200 -> revoked.
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"grants":[{"id":"g1","name":"real","status":"active"}]}`))
	}))
	t.Cleanup(okSrv.Close)
	if err := GrantRevoke(okSrv.URL, "real"); err != nil {
		t.Errorf("GrantRevoke(real) = %v, want nil", err)
	}
	// The DELETE returns 500 -> the revoke-failed status error.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"grants":[{"id":"g1","name":"real","status":"active"}]}`))
	}))
	t.Cleanup(failSrv.Close)
	if err := GrantRevoke(failSrv.URL, "real"); err == nil || !strings.Contains(err.Error(), "revoke failed") {
		t.Errorf("GrantRevoke(delete 500) = %v, want a revoke-failed error", err)
	}
}

// TestPayoutErrorBranches covers RequestPayout, FetchOnboardURL, FetchPayoutStatus, and
// FetchPayoutHistory non-2xx (payoutErr) branches, plus FetchOnboardURL's empty-URL one.
func TestPayoutErrorBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bad := jsonServer(t, http.StatusBadRequest, `{"error":"below the $25 minimum"}`, nil)
	if _, err := RequestPayout(bad); err == nil || !strings.Contains(err.Error(), "below the $25 minimum") {
		t.Errorf("RequestPayout(400) = %v, want the broker error", err)
	}
	if _, err := FetchOnboardURL(bad); err == nil || !strings.Contains(err.Error(), "below the $25 minimum") {
		t.Errorf("FetchOnboardURL(400) = %v, want the broker error", err)
	}
	if _, err := FetchPayoutHistory(bad); err == nil || !strings.Contains(err.Error(), "below the $25 minimum") {
		t.Errorf("FetchPayoutHistory(400) = %v, want the broker error", err)
	}
	// Onboard returns 200 but no URL -> the no-onboarding-URL error.
	noURL := jsonServer(t, http.StatusOK, `{}`, nil)
	if _, err := FetchOnboardURL(noURL); err == nil || !strings.Contains(err.Error(), "no onboarding URL") {
		t.Errorf("FetchOnboardURL(empty) = %v, want a no-URL error", err)
	}
	// RequestPayout on a dead broker (transport).
	if _, err := RequestPayout(deadBroker(t)); err == nil {
		t.Error("RequestPayout(dead) should error")
	}
	if _, err := FetchPayoutStatus(deadBroker(t)); err == nil {
		t.Error("FetchPayoutStatus(dead) should error")
	}
}

// TestWithTopupHintMonthlyLimitPassthrough covers WithTopupHint's special 402 branch: a
// monthly-spend-limit 402 already names its own remedy, so the topup hint is NOT appended
// (topping up won't unblock a cap).
func TestWithTopupHintMonthlyLimitPassthrough(t *testing.T) {
	msg := "monthly spend limit reached - raise it or wait for next month"
	if got := WithTopupHint(http.StatusPaymentRequired, msg); got != msg {
		t.Errorf("WithTopupHint(monthly 402) = %q, want it unchanged (no topup hint)", got)
	}
}

// TestGrantListErrorBranches covers the fetchGrants-error branches of GrantList and
// GrantListRows (a forbidden broker), plus GrantList's empty-list line.
func TestGrantListErrorBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	forbidden := jsonServer(t, http.StatusForbidden, `{}`, nil)
	if err := GrantList(forbidden); err == nil || !strings.Contains(err.Error(), "roger login") {
		t.Errorf("GrantList(403) = %v, want the login-required error", err)
	}
	if _, err := GrantListRows(forbidden); err == nil || !strings.Contains(err.Error(), "roger login") {
		t.Errorf("GrantListRows(403) = %v, want the login-required error", err)
	}
	// An empty grant list -> the "no grants yet" line (a clean nil, not an error).
	none := jsonServer(t, http.StatusOK, `{"grants":[]}`, nil)
	if err := GrantList(none); err != nil {
		t.Errorf("GrantList(empty) = %v, want nil", err)
	}
}

// TestLoadOrCreateUserKeyGeneratesOnCorrupt covers LoadOrCreateUserKey's corrupted-key
// fallthrough: a non-hex / wrong-size key on disk is discarded and a fresh valid key is
// generated and persisted.
func TestLoadOrCreateUserKeyGeneratesOnCorrupt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(userKeyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	// Garbage (not valid hex of the right length) -> the decode/size guard fails.
	if err := os.WriteFile(userKeyPath(), []byte("not-a-valid-hex-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := userKeyOnce
	userKeyOnce = nil
	t.Cleanup(func() { userKeyOnce = old })

	got := LoadOrCreateUserKey()
	if len(got) != ed25519.PrivateKeySize {
		t.Fatalf("LoadOrCreateUserKey(corrupt) len = %d, want a freshly generated %d-byte key", len(got), ed25519.PrivateKeySize)
	}
	// It must have been persisted as valid hex (so the next process reads it back).
	raw, err := os.ReadFile(userKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, derr := hex.DecodeString(strings.TrimSpace(string(raw))); derr != nil {
		t.Errorf("persisted key is not valid hex: %v", derr)
	}
}

// TestLoginReturnError covers LoginReturn's error branch (Login fails -> "" + error)
// without any network: an empty client id is a clear, offline failure.
func TestLoginReturnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if login, err := LoginReturn("http://broker", ""); err == nil || login != "" {
		t.Errorf("LoginReturn(no client id) = %q / %v, want an error", login, err)
	}
}

// TestLoginPollInvalidHandle covers LoginPoll's invalid-handle guard: a Device whose
// Handle is not a deviceFlow is rejected before any network call.
func TestLoginPollInvalidHandle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := LoginPoll("http://broker", "cid", Device{Handle: "not-a-deviceflow"}); err == nil || !strings.Contains(err.Error(), "invalid login handle") {
		t.Errorf("LoginPoll(bad handle) = %v, want an invalid-handle error", err)
	}
}

// TestBindTokenRejectionBranches covers bindToken's rejection branches: a 200 with a
// broker error message, a non-200 with no message, and a transport error.
func TestBindTokenRejectionBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// ok=false with a message.
	withMsg := jsonServer(t, http.StatusOK, `{"ok":false,"error":{"message":"token expired"}}`, nil)
	if _, err := bindToken(withMsg, "gho_x"); err == nil || !strings.Contains(err.Error(), "token expired") {
		t.Errorf("bindToken(msg) = %v, want the broker message", err)
	}
	// non-200 with no message -> the status fallback.
	noMsg := jsonServer(t, http.StatusBadGateway, `{"ok":false}`, nil)
	if _, err := bindToken(noMsg, "gho_x"); err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Errorf("bindToken(no msg) = %v, want the status fallback", err)
	}
	// Transport error.
	if _, err := bindToken(deadBroker(t), "gho_x"); err == nil {
		t.Error("bindToken(dead) should error")
	}
}

// TestStartDeviceFlowDefaultsInterval covers startDeviceFlow's interval<=0 default (a
// device-code response with no interval gets the RFC-default 5s) and the URL-complete
// pass-through.
func TestStartDeviceFlowDefaultsInterval(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, `{"device_code":"dc","user_code":"AAAA-1111","verification_uri":"https://gh/device","verification_uri_complete":"https://gh/device?code=AAAA-1111"}`, map[string]string{"Content-Type": "application/json"})
	old := ghDeviceCodeURL
	ghDeviceCodeURL = srv
	t.Cleanup(func() { ghDeviceCodeURL = old })
	dev, err := startDeviceFlow("cid")
	if err != nil {
		t.Fatalf("startDeviceFlow = %v", err)
	}
	if dev.Interval != 5 {
		t.Errorf("interval = %d, want the RFC default 5", dev.Interval)
	}
	if dev.VerificationURIComplete == "" {
		t.Error("verification_uri_complete should pass through")
	}
}

// TestPollDeviceTokenErrorBranches covers pollDeviceToken's terminal error replies:
// expired_token, access_denied, and a generic github error - each returns promptly with
// a clear message (Interval 1 keeps the single poll sleep short).
func TestPollDeviceTokenErrorBranches(t *testing.T) {
	cases := []struct {
		resp string
		want string
	}{
		{`{"error":"expired_token"}`, "expired"},
		{`{"error":"access_denied"}`, "denied"},
		{`{"error":"some_other_error"}`, "some_other_error"},
	}
	for _, c := range cases {
		srv := jsonServer(t, http.StatusOK, c.resp, map[string]string{"Content-Type": "application/json"})
		old := ghAccessTokenURL
		ghAccessTokenURL = srv
		_, err := pollDeviceToken("cid", deviceFlow{DeviceCode: "dc", Interval: 1, ExpiresIn: 300})
		ghAccessTokenURL = old
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("pollDeviceToken(%s) = %v, want it to contain %q", c.resp, err, c.want)
		}
	}
}

// TestPollDeviceTokenPendingThenSlowDownThenToken covers the keep-polling branches:
// authorization_pending (loop), slow_down (interval bump + loop), then a granted token.
func TestPollDeviceTokenPendingThenSlowDownThenToken(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		case 2:
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"gho_final"}`))
		}
	}))
	t.Cleanup(srv.Close)
	old := ghAccessTokenURL
	ghAccessTokenURL = srv.URL
	t.Cleanup(func() { ghAccessTokenURL = old })
	tok, err := pollDeviceToken("cid", deviceFlow{DeviceCode: "dc", Interval: 1, ExpiresIn: 300})
	if err != nil || tok != "gho_final" {
		t.Fatalf("pollDeviceToken = %q / %v, want gho_final after pending+slow_down", tok, err)
	}
	if calls < 3 {
		t.Errorf("expected >=3 polls (pending, slow_down, granted), got %d", calls)
	}
}

// TestLoginPrintsVerificationURIComplete covers Login's pre-filled-link branch: when the
// device flow returns a verification_uri_complete, Login prints it (and runs end-to-end
// through bindToken against the fake broker).
func TestLoginPrintsVerificationURIComplete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/device") {
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"AAAA-1111","verification_uri":"https://gh/device","verification_uri_complete":"https://gh/device?code=AAAA-1111","interval":1,"expires_in":300}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"gho_token"}`))
	}))
	t.Cleanup(gh.Close)
	od, oa := ghDeviceCodeURL, ghAccessTokenURL
	ghDeviceCodeURL = gh.URL + "/device"
	ghAccessTokenURL = gh.URL + "/token"
	t.Cleanup(func() { ghDeviceCodeURL, ghAccessTokenURL = od, oa })

	b := fakeBroker(t)
	if err := Login(b, "cid"); err != nil {
		t.Fatalf("Login(with URI-complete) = %v, want nil", err)
	}
	if LinkedLogin() != "octocat" {
		t.Errorf("after Login, LinkedLogin = %q, want octocat", LinkedLogin())
	}
}
