package main

// Executable scenarios for features/relay/browser_session.feature: the Playbox chat
// path. The relay accepts credentialed browser calls from allowlisted web origins -
// the session cookie is an identity ONLY behind a verified Origin (CSRF defense),
// resolves to the SAME u_gh_ wallet as the CLI, and the signed/grant paths are
// untouched. Table-driven where the scenarios share a shape.

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"net/http"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"

	"github.com/stretchr/testify/require"
)

const pbOrigin = "https://rogerai.fm"

// pbBroker is relayBroker plus the pieces the browser path makes reachable: the
// per-IP anon limiter and a web-origin allowlist.
func pbBroker(t *testing.T, db store.Store) *broker {
	t.Setenv("ROGERAI_WEB_ORIGIN", pbOrigin)
	t.Setenv("ROGERAI_WEB_ORIGINS", "")
	b := relayBroker(db)
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 10}
	// Every public-market request places a floored hold (estimateMaxCost's 1e-6),
	// so even $0 offers need the seed a real broker grants - match prod.
	b.seedFunds = 1
	return b
}

// pbStation puts one station on air for model with the given prices and answers the
// next job with a canned completion.
func pbStation(b *broker, db store.Store, nodeID, owner, model string, price float64) {
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes[nodeID] = protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: model, PriceIn: price, PriceOut: price, Ctx: 4096}},
	}
	b.lastSeen[nodeID] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels[nodeID] = tun
	_ = db.BindNode(nodeID, owner)
	respondOnce(tun, nodeID, nodePriv, `{"choices":[{"message":{"content":"73 de the station"}}]}`, 8, 4, price, price)
}

// pbReq builds a browser-shaped POST: no signing headers, optional Origin + cookie.
func pbReq(model, origin, cookie string) *http.Request {
	body := `{"model":"` + model + `","max_tokens":8}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	return r
}

// ---------- CORS surface ----------------------------------------------------

func TestPlayboxRelayPreflight(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)

	t.Run("allowlisted origin is granted with credentials, never a wildcard", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
		r.Header.Set("Origin", pbOrigin)
		w := httptest.NewRecorder()
		b.relay(w, r)
		require.Equal(t, http.StatusNoContent, w.Code)
		require.Equal(t, pbOrigin, w.Header().Get("Access-Control-Allow-Origin"))
		require.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
		require.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "POST")
		require.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "Content-Type")
	})

	t.Run("a foreign origin gets no grant", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		b.relay(w, r)
		require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
		require.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
	})
}

// ---------- identity: the session cookie ------------------------------------

func TestPlayboxRelaySessionCookieChatsFree(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbStation(b, db, "free-stn", "owner-a", "wave-nano-chat", 0)
	cookie := b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())

	w := httptest.NewRecorder()
	b.relay(w, pbReq("wave-nano-chat", pbOrigin, cookie))

	body := readBody(w)
	require.Equal(t, http.StatusOK, w.Code, body)
	require.Contains(t, body, "73 de the station")
	require.Equal(t, pbOrigin, w.Header().Get("Access-Control-Allow-Origin"),
		"the completion response must carry the credentialed CORS headers")
}

func TestPlayboxRelaySessionSpendsTheOneWallet(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbStation(b, db, "paid-stn", "owner-b", "big-model", 9)
	start, err := db.AddCredits("u_gh_7", 500)
	require.NoError(t, err)
	cookie := b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())

	w := httptest.NewRecorder()
	b.relay(w, pbReq("big-model", pbOrigin, cookie))

	require.Equal(t, http.StatusOK, w.Code, readBody(w))
	end, _ := db.PeekBalance("u_gh_7")
	require.Less(t, end, start, "the spend must settle against the github-scoped wallet the CLI also uses")
	earn, _ := db.EarningsOf("paid-stn")
	require.Greater(t, earn, 0.0, "the station must mint an earning lot")
}

func TestPlayboxRelayCookieNeedsAllowlistedOrigin(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbStation(b, db, "paid-stn", "owner-b", "big-model", 9)
	_, _ = db.AddCredits("u_gh_7", 500)
	valid := b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())

	cases := []struct {
		name, origin, cookie string
	}{
		{"foreign origin: the cookie is ignored (CSRF)", "https://evil.example", valid},
		{"no origin header: the cookie is ignored (CSRF)", "", valid},
		{"expired cookie", pbOrigin, b.signSession("octocat", 7, time.Now().Add(-time.Minute).Unix())},
		{"forged cookie", pbOrigin, "Zm9yZ2Vk.Zm9yZ2Vk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, _ := db.PeekBalance("u_gh_7")
			w := httptest.NewRecorder()
			b.relay(w, pbReq("big-model", tc.origin, tc.cookie))
			require.Equal(t, http.StatusUnauthorized, w.Code, readBody(w))
			end, _ := db.PeekBalance("u_gh_7")
			require.Equal(t, start, end, "no wallet may be touched")
		})
	}
}

// ---------- the anonymous visitor -------------------------------------------

func TestPlayboxRelayAnonBrowserFreeModel(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbStation(b, db, "free-stn", "owner-a", "wave-nano-chat", 0)

	w := httptest.NewRecorder()
	b.relay(w, pbReq("wave-nano-chat", pbOrigin, ""))
	body := readBody(w)
	require.Equal(t, http.StatusOK, w.Code, body)
	require.Contains(t, body, "73 de the station")
}

func TestPlayboxRelayAnonBrowserPerIPLimit(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	pbStation(b, db, "free-stn", "owner-a", "wave-nano-chat", 0)

	w := httptest.NewRecorder()
	b.relay(w, pbReq("wave-nano-chat", pbOrigin, ""))
	require.Equal(t, http.StatusOK, w.Code, readBody(w))

	w = httptest.NewRecorder()
	b.relay(w, pbReq("wave-nano-chat", pbOrigin, ""))
	require.Equal(t, http.StatusTooManyRequests, w.Code, "the per-IP anon limiter must bound a session-less browser")
	require.NotEmpty(t, w.Header().Get("Retry-After"))
}

func TestPlayboxRelaySpoofedOriginLegacyIDStaysAnon(t *testing.T) {
	// Push-audit regression (2026-08-01): Origin is spoofable outside a browser, so
	// a legacy X-Roger-User / Bearer id on the cookieless browser path must NOT mint
	// its own rate bucket - it is the anonymous identity, bounded per IP.
	db := store.NewMem()
	b := pbBroker(t, db)
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	pbStation(b, db, "free-stn", "owner-a", "wave-nano-chat", 0)

	r := pbReq("wave-nano-chat", pbOrigin, "")
	r.Header.Set("X-Roger-User", "rotating-id-1")
	w := httptest.NewRecorder()
	b.relay(w, r)
	require.Equal(t, http.StatusOK, w.Code, readBody(w))

	r = pbReq("wave-nano-chat", pbOrigin, "")
	r.Header.Set("X-Roger-User", "rotating-id-2")
	w = httptest.NewRecorder()
	b.relay(w, r)
	require.Equal(t, http.StatusTooManyRequests, w.Code,
		"a rotated legacy id behind a spoofed Origin must stay inside the ONE per-IP anon bucket")

	r = pbReq("wave-nano-chat", pbOrigin, "")
	r.Header.Set("Authorization", "Bearer rotating-id-3")
	w = httptest.NewRecorder()
	b.relay(w, r)
	require.Equal(t, http.StatusTooManyRequests, w.Code,
		"a rotated Bearer legacy id must not mint a fresh bucket either")
}

func TestPlayboxRelayAnonBrowserPaidModelSignIn(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbStation(b, db, "paid-stn", "owner-b", "big-model", 9)

	w := httptest.NewRecorder()
	b.relay(w, pbReq("big-model", pbOrigin, ""))
	body := readBody(w)
	require.Equal(t, http.StatusUnauthorized, w.Code, body)
	require.Contains(t, body, "log in", "the error must tell the visitor to sign in")
	earn, _ := db.EarningsOf("paid-stn")
	require.Zero(t, earn, "no station may receive the request")
}

// ---------- unchanged invariants ---------------------------------------------

func TestPlayboxRelayBareCurlStays401(t *testing.T) {
	// No origin, no cookie, no signature: exactly the pre-feature behavior.
	db := store.NewMem()
	b := pbBroker(t, db)
	pbStation(b, db, "free-stn", "owner-a", "wave-nano-chat", 0)

	w := httptest.NewRecorder()
	b.relay(w, pbReq("wave-nano-chat", "", ""))
	require.Equal(t, http.StatusUnauthorized, w.Code,
		"an unsigned non-browser request must stay rejected - the browser door opens ONLY behind an allowlisted Origin")
}

func TestPlayboxRelayStreamCrossOrigin(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbStation(b, db, "free-stn", "owner-a", "wave-nano-chat", 0)
	cookie := b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())

	body := `{"model":"wave-nano-chat","max_tokens":8,"stream":true}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Origin", pbOrigin)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	b.relay(w, r)

	require.Equal(t, http.StatusOK, w.Code, readBody(w))
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	require.Equal(t, pbOrigin, w.Header().Get("Access-Control-Allow-Origin"),
		"the SSE stream must carry the credentialed CORS headers")
}

func TestPlayboxRelaySessionSharesRateBucketWithCLI(t *testing.T) {
	// One identity, one bucket: a browser session and a logged-in CLI keypair that
	// resolve to the same u_gh_ wallet must drain the SAME per-identity bucket.
	db := store.NewMem()
	b := pbBroker(t, db)
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	pbStation(b, db, "free-stn", "owner-a", "wave-nano-chat", 0)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	require.NoError(t, db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: userPubHex}))
	cookie := b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())

	// Browser drains the bucket...
	w := httptest.NewRecorder()
	b.relay(w, pbReq("wave-nano-chat", pbOrigin, cookie))
	require.Equal(t, http.StatusOK, w.Code, readBody(w))

	// ...so the CLI's very next request on the same account is limited.
	body := []byte(`{"model":"wave-nano-chat","max_tokens":8}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w = httptest.NewRecorder()
	b.relay(w, r)
	require.Equal(t, http.StatusTooManyRequests, w.Code,
		"the browser session and the CLI keypair are one identity and must share one rate bucket")
}
