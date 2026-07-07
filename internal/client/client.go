// Package client is the consumer side: discover models, check balance, and open
// a local OpenAI-compatible endpoint that relays through the broker.
//
// The proxy is self-healing: when a relayed request fails (5xx / timeout /
// connection drop) it transparently re-routes to an alternative provider that
// still meets the user's criteria (price / tps / confidential), keeping the SAME
// local endpoint + key so Hermes/bots never notice. See failover.go.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/glyphs"
	"github.com/rogerai-fyi/roger/internal/pricetier"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// AlertFunc receives a human-readable line when the proxy can't recover (no
// alternative provider fits the criteria). The TUI wires this to its status line;
// the CLI logs it to stderr. nil = no surfacing.
type AlertFunc func(string)

// ErrBrokerUnreachable marks a getJSON failure where the broker could not be reached
// or returned a non-2xx status. Callers wrap it (errors.Is) to tell "the broker is
// down / erroring" apart from a genuine empty/zero result (no offers, no balance) -
// so `balance` no longer prints a misleading $0 and `search` no longer prints "no
// offers" when the broker is actually down or 500ing.
var ErrBrokerUnreachable = errors.New("couldn't reach the broker")

// getJSON issues GET broker+path (optionally as `user`) and decodes the JSON body
// into out. It centralizes the request/decode boilerplate the consumer commands share.
// A transport failure OR a non-2xx status is returned wrapped in ErrBrokerUnreachable
// (distinct from a real empty/zero body), so a broker-down / 500 never masquerades as
// "logged out" / "$0" / "no offers". A decode error on a 2xx body is still ignored (the
// caller validates fields).
func getJSON(broker, path, user string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, broker+path, nil)
	// Wallet/dashboard reads are signed so the broker serves the verified identity
	// (not whoever sets a header). Public reads (e.g. /discover) pass user="" and are
	// still signed harmlessly; the broker only uses the identity where it matters.
	signRequest(req, nil)
	if user != "" {
		req.Header.Set("X-Roger-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: broker returned status %d", ErrBrokerUnreachable, resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(out)
	return nil
}

// Search prints the live model marketplace (GET /discover), cheapest first, as a
// table - node, model, in/out price, throughput, context, region, status, flags.
func Search(broker string) error {
	var d struct {
		Offers []struct {
			NodeID       string  `json:"node_id"`
			Region       string  `json:"region"`
			Model        string  `json:"model"`
			PriceIn      float64 `json:"price_in"`
			PriceOut     float64 `json:"price_out"`
			PriceTier    int     `json:"price_tier"` // broker's neutral 0..4 $-tier (0 = FREE/unknown)
			Ctx          int     `json:"ctx"`
			Online       bool    `json:"online"`
			Confidential bool    `json:"confidential"`
			FreeNow      bool    `json:"free_now"`
			TPS          float64 `json:"tps"`
			Signal       int     `json:"signal"`
		} `json:"offers"`
	}
	if err := getJSON(broker, "/discover", "", &d); err != nil {
		return err
	}
	if len(d.Offers) == 0 {
		fmt.Println("no offers yet - run `roger share` on a box with a local model")
		return nil
	}
	// Station rows mirror the TUI band table's instrument language so the piped CLI
	// reads as a terminal twin of the on-screen one: a ◉ on-air / ○ off-air glyph in
	// the STATUS cell, a ▁▂▃▄▅▆▇ SIGNAL tower driven by tok/s, the broker's neutral
	// $-TIER right beside the out-price, and the verified ◆ in FLAGS. Plain text (no
	// color), so it degrades cleanly under NO_COLOR / a pipe.
	fmt.Printf("%-8s %-12s %-22s %-9s %-9s %-13s %-7s %-7s %-7s %-7s %s\n",
		"STATUS", "SIGNAL", "MODEL", "$/1M in", "$/1M out", "TIER", "TOK/S", "CTX", "REGION", "NODE", "FLAGS")
	for _, o := range d.Offers {
		status := glyphOnAir
		if !o.Online {
			status = glyphOffAir
		}
		tps := "-"
		if o.TPS > 0 {
			tps = fmt.Sprintf("%.0f", o.TPS)
		}
		flags := ""
		if o.Confidential {
			flags += glyphVerify + " verified "
		}
		if o.FreeNow {
			flags += "FREE-now"
		}
		fmt.Printf("%-8s %-12s %-22s %-9.2f %-9.2f %-13s %-7s %-7d %-7s %-7s %s\n",
			status, signalTower(o.Signal, o.TPS, o.Online), o.Model, o.PriceIn, o.PriceOut,
			pricetier.Label(o.PriceTier, o.PriceOut), tps, o.Ctx, o.Region, o.NodeID, flags)
	}
	return nil
}

// The CLI band table's $-tier cell is the shared canonical render (internal/pricetier.Label),
// so it reads identically to the TUI + web surfaces - one impl, no drift.

// Shared CLI iconography, kept in lock-step with the TUI's glyphs - BOTH route through
// internal/glyphs (one set, one chooser): ◉ on air / ○ off air / ◆ verified on capable
// terminals, or the ASCII fallback ((o)/( )/<>) on a legacy Windows console (or under
// ROGERAI_ASCII=1 / NO_UNICODE). They are vars (not consts) because the mark is chosen
// once at startup. The CLI prints plain text (no color), so the glyph alone carries the
// meaning under NO_COLOR / a pipe.
var (
	glyphOnAir  = glyphs.Current().OnAir
	glyphOffAir = glyphs.Current().OffAir
	glyphVerify = glyphs.Current().Verify
)

// signalTower renders a 5-cell ▁▂▃▄▅▆▇█ signal bar driven by the broker's 0..100
// channel signal, mirroring the TUI band table's inline meter. The signal carries
// even when tok/s is 0 (an online-but-untrafficked node still scores its baseline),
// so an on-air band never reads blank. When the broker signal is absent (legacy /
// pre-signal offers, signal<=0) we fall back to the old tps-derived bar. Offline
// shows the flat "no signal" tower. No color - the glyph heights carry the reading
// in a pipe (NO_COLOR safe).
func signalTower(signal int, tps float64, online bool) string {
	if !online {
		return glyphs.Current().SigOff
	}
	base := signalLevel(signal)
	if base == 0 {
		// No broker signal (legacy offer) - fall back to the tps-derived level so a
		// node that DOES report throughput still meters.
		base = tpsLevel(tps)
	}
	if base == 0 {
		// Online with neither a broker signal nor measured tps: show one bar, never a
		// fully blank tower (online always reads as at least faint carrier).
		base = 1
	}
	ramp := glyphs.Current().Signal
	var b strings.Builder
	for i := 0; i < 5; i++ {
		lvl := base - (i % 2)
		if lvl < 0 {
			lvl = 0
		}
		if lvl >= len(ramp) {
			lvl = len(ramp) - 1
		}
		b.WriteRune(ramp[lvl])
	}
	return b.String()
}

// signalLevel maps the broker's 0..100 signal onto the 0..7 glyph ramp (▁..█). An
// online node's baseline signal (~43 with no traffic) lands mid-tower; 100 pins
// the top. 0 means "no broker signal" (the caller then falls back to tps).
func signalLevel(signal int) int {
	if signal <= 0 {
		return 0
	}
	// 1..100 -> 1..7 (never 0 for a positive signal, so an online node always meters).
	lvl := 1 + (signal*6+99)/100 // ceil((signal/100)*6), then +1 base; ~43 -> 4
	if lvl > 7 {
		lvl = 7
	}
	return lvl
}

// tpsLevel is the legacy tok/s -> level mapping, kept as the fallback meter when an
// offer carries no broker signal.
func tpsLevel(tps float64) int {
	switch {
	case tps >= 600:
		return 6
	case tps >= 300:
		return 5
	case tps >= 150:
		return 4
	case tps >= 60:
		return 3
	case tps >= 20:
		return 2
	case tps > 0:
		return 1
	}
	return 0
}

// Balance prints the caller's wallet credits (GET /balance as `user`). When the
// caller is NOT logged in (an anonymous keypair) there is no wallet/balance: it says
// so and points at `roger login` instead of printing a misleading 0.
func Balance(broker, user string) error {
	var b struct {
		User         string  `json:"user"`
		Balance      float64 `json:"balance"`
		LoggedIn     bool    `json:"logged_in"`
		MonthlyCap   float64 `json:"monthly_cap"`
		MonthlySpend float64 `json:"monthly_spend"`
	}
	if err := getJSON(broker, "/balance", user, &b); err != nil {
		return err
	}
	if !b.LoggedIn {
		fmt.Println("not logged in - run `roger login` to use your wallet (free models and grant keys work without an account)")
		return nil
	}
	fmt.Printf("logged in - wallet %s: $%.4f\n", b.User, b.Balance)
	// Monthly spend cap (a budget limit): show month-to-date vs the cap. 0 = unlimited
	// (the opt-in default) - say so + how to set one.
	if b.MonthlyCap > 0 {
		fmt.Printf("monthly spend: $%.2f of $%.2f this month%s\n", b.MonthlySpend, b.MonthlyCap, monthlyNotice(b.MonthlySpend, b.MonthlyCap))
	} else {
		fmt.Printf("monthly spend: $%.2f this month  (no cap - set one with `roger limit --monthly $X`)\n", b.MonthlySpend)
	}
	return nil
}

// monthlyNotice renders the near/at-cap tail for the balance line: a 100% "limit
// reached" warning, an 80% "approaching" warning, or "" when comfortably under.
func monthlyNotice(spend, cap float64) string {
	if cap <= 0 {
		return ""
	}
	switch {
	case spend >= cap:
		return "  - LIMIT REACHED (raise it with `roger limit --monthly $X`)"
	case spend >= cap*0.80:
		return fmt.Sprintf("  - %.0f%% used", spend/cap*100)
	}
	return ""
}

// MonthlyCapInfo is the per-account monthly spend cap snapshot (GET /account/limit).
type MonthlyCapInfo struct {
	Cap   float64 `json:"monthly_cap"`
	Spend float64 `json:"monthly_spend"`
}

// GetMonthlyLimit reads the caller's monthly spend cap + month-to-date spend.
func GetMonthlyLimit(broker, user string) (MonthlyCapInfo, error) {
	var out MonthlyCapInfo
	req, _ := http.NewRequest(http.MethodGet, broker+"/account/limit", nil)
	signRequest(req, nil)
	if user != "" {
		req.Header.Set("X-Roger-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return out, fmt.Errorf("log in first - run `roger login` (the monthly limit is per account)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("broker returned status %d", resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// SetMonthlyLimit sets the caller's monthly spend cap ($; 0 = unlimited / clear) and
// returns the resulting snapshot.
func SetMonthlyLimit(broker, user string, cap float64) (MonthlyCapInfo, error) {
	var out MonthlyCapInfo
	if cap < 0 {
		cap = 0
	}
	body, _ := json.Marshal(map[string]float64{"monthly_cap": cap})
	req, _ := http.NewRequest(http.MethodPatch, broker+"/account/limit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, body)
	if user != "" {
		req.Header.Set("X-Roger-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return out, fmt.Errorf("log in first - run `roger login` (the monthly limit is per account)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("broker returned status %d", resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// Topup asks the broker for a Stripe Checkout URL to buy `usd` of credits and opens
// it in the browser. `open` is the guarded default-browser launcher (tui.OpenURL),
// which self-gates on an interactive TTY - so on a headless / piped box it is a no-op
// and the printed URL below stays as the copy-paste fallback. A nil open just prints.
func Topup(broker, user string, usd float64, open func(string)) error {
	body, _ := json.Marshal(map[string]float64{"usd": usd})
	req, _ := http.NewRequest(http.MethodPost, broker+"/billing/checkout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, body)
	req.Header.Set("X-Roger-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("billing isn't configured on this broker yet")
	}
	var d struct {
		URL     string  `json:"url"`
		Credits float64 `json:"credits"`
	}
	json.NewDecoder(resp.Body).Decode(&d)
	if d.URL == "" {
		return fmt.Errorf("no checkout URL returned")
	}
	// 1 credit = $1, so the credit count is the dollar amount added to the wallet.
	fmt.Printf("Add $%.2f to your wallet - open this to pay:\n  %s\n", d.Credits, d.URL)
	// Auto-open the checkout URL (guarded: no-op on a headless / piped box, where the
	// printed URL above is the fallback) so the worst-friction moment - paying - does
	// not dead-end on a copy-paste, matching login/onboard/payout.
	if open != nil {
		open(d.URL)
	}
	return nil
}

// TopupURL asks the broker for a Stripe Checkout URL to buy `usd` of credits and
// returns it (the data form of Topup, for the in-TUI /topup flow).
func TopupURL(broker, user string, usd float64) (string, error) {
	body, _ := json.Marshal(map[string]float64{"usd": usd})
	req, _ := http.NewRequest(http.MethodPost, broker+"/billing/checkout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, body)
	req.Header.Set("X-Roger-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		return "", fmt.Errorf("billing isn't configured on this broker yet")
	}
	var d struct {
		URL string `json:"url"`
	}
	json.NewDecoder(resp.Body).Decode(&d)
	if d.URL == "" {
		return "", fmt.Errorf("no checkout URL returned")
	}
	return d.URL, nil
}

// ProxyOptions configures the local relay handler.
type ProxyOptions struct {
	Broker, User string
	Confidential bool
	MinTPS       float64 // X-Roger-Min-TPS floor (0 = none)
	MaxPriceIn   float64 // X-Roger-Max-Price cap on input price (0 = none)
	MaxPriceOut  float64 // X-Roger-Max-Price-Out cap on output price (0 = none)
	Freq         string  // X-Roger-Freq private band code (empty = open market)
	Pref         string  // X-Roger-Pref routing knob: cheap/balanced/fast/reliable (empty = balanced)
	// Model is the TUNED band's model. It is the /v1/models identity AND the rewrite
	// target: every incoming request's `model` field is rewritten to this before relay,
	// so an agent's arbitrary default ("gpt-4o", "sonnet") just works. Empty = legacy
	// single-user mode (no rewrite; the body's own model is honored) - kept so `roger use`
	// and the pre-existing relay tests behave exactly as before.
	Model string
	// SessionKey is the per-session bearer secret. When set, every proxy route enforces
	// `Authorization: Bearer <SessionKey>` with a constant-time compare. Empty = auth
	// disabled (the legacy single-user path; production callers generate one via
	// NewSessionKey so a guest agent / other local process can't spend the wallet).
	SessionKey string
	// Budget is the per-session spend cap in dollars (1 credit = $1). The proxy accumulates
	// each response's billed X-RogerAI-Cost and hard-stops the NEXT request with a 402 once
	// the running total reaches the cap. 0 = no local cap (unlimited); the guest-operator
	// launch sets DefaultSessionBudget.
	Budget float64
	Alert  AlertFunc // surfaced when failover is exhausted (nil = silent)
}

// DefaultSessionBudget is the per-session spend cap the guest-operator launch applies by
// default (founder ruling, 2026-07-06: $2.00, raisable). It is NOT imposed on the legacy
// single-user `roger use` path (which passes Budget 0 = unlimited) so that flow is unchanged.
const DefaultSessionBudget = 2.00

// proxyBodyCap is the request-body ceiling. A body over it is rejected with an OpenAI-shaped
// 413 (never silently truncated-and-relayed).
const proxyBodyCap = 4 << 20 // 4 MiB

// Stream bounds for the relay client (founder ruling 7): drop the blanket 120s
// http.Client.Timeout that cut legitimate long streams; bound only the TCP dial and the
// response-header wait, letting a healthy body/stream trickle to the broker's own 300s
// ceiling. Package vars so a test can inject small values and run fast.
var (
	proxyDialTimeout           = 10 * time.Second
	proxyResponseHeaderTimeout = 30 * time.Second
)

// newRelayClient builds the relay http.Client with NO blanket Timeout (which would cover the
// body read and cut long streams); it bounds the dial + response-header wait via a Transport.
func newRelayClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: proxyDialTimeout}).DialContext,
			ResponseHeaderTimeout: proxyResponseHeaderTimeout,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// NewSessionKey mints a per-session bearer secret (256-bit, hex). Stable for the session so a
// running guest agent's generated config keeps working across a band re-tune (ruling 6).
func NewSessionKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is catastrophic; fail closed with an unusable key rather than a
		// predictable one (a blank key would DISABLE auth).
		return "unavailable-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// ProxyOptionsHolder is a concurrency-safe LIVE snapshot of ProxyOptions the handler reads
// per request, so a re-tune re-points the SAME endpoint atomically (ruling 9). It also owns
// the per-session spend accumulator (survives a re-tune) and the connected flag (a
// disconnected proxy refuses to spend, ruling 5).
type ProxyOptionsHolder struct {
	mu        sync.RWMutex
	opts      ProxyOptions
	connected bool
	created   int64

	budgetMu sync.Mutex
	spent    float64
}

// NewProxyOptionsHolder wraps a fixed ProxyOptions as a live source (starts connected).
func NewProxyOptionsHolder(opts ProxyOptions) *ProxyOptionsHolder {
	return &ProxyOptionsHolder{opts: opts, connected: true, created: time.Now().Unix()}
}

// Get returns a consistent snapshot of the current options (never a half-updated mix).
func (h *ProxyOptionsHolder) Get() ProxyOptions {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.opts
}

// Connected reports whether a band is currently tuned (false => refuse relays, ruling 5).
func (h *ProxyOptionsHolder) Connected() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.connected
}

// SetBand re-points the live band routing/model/caps on a (re)tune, KEEPING the session key,
// budget, and running spend stable (rulings 6 + 9) and marking the proxy connected again.
func (h *ProxyOptionsHolder) SetBand(b ProxyOptions) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b.SessionKey = h.opts.SessionKey // the bearer key is STABLE for the session
	b.Budget = h.opts.Budget         // the spend cap carries across a re-tune
	h.opts = b
	h.connected = true
}

// Disconnect marks the proxy as serving no band; subsequent relays are refused (ruling 5).
func (h *ProxyOptionsHolder) Disconnect() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected = false
}

// SetBudget raises/lowers the live session spend cap (the /budget knob). connected/key/spend
// are untouched.
func (h *ProxyOptionsHolder) SetBudget(usd float64) {
	h.mu.Lock()
	h.opts.Budget = usd
	h.mu.Unlock()
}

// ResetSpend zeroes the session spend accumulator (a fresh session).
func (h *ProxyOptionsHolder) ResetSpend() {
	h.budgetMu.Lock()
	h.spent = 0
	h.budgetMu.Unlock()
}

// Spent returns the accumulated session spend in dollars.
func (h *ProxyOptionsHolder) Spent() float64 {
	h.budgetMu.Lock()
	defer h.budgetMu.Unlock()
	return h.spent
}

// admit gates one request on the session budget - the LITERAL CEILING (founder ruling
// 2026-07-07): admit while cumulative spent < budget; refuse (ok=false -> 402) once
// spent >= budget. The call that CROSSES the budget completes (the spend may tip slightly
// over); the NEXT call is the one refused. On ok it returns a release closure the caller MUST
// invoke exactly once with the request's billed cost; the budget mutex is held from the check
// THROUGH the release so N concurrent requests cannot each read "under budget" and all slip
// through (the check+accumulate is atomic - the parallel-subagent invariant, budget.feature
// "at most 4 served"). The mutex is thus held across the upstream dial + header-wait (release
// fires when the response headers with X-RogerAI-Cost arrive, BEFORE the body streams), so the
// body/stream runs unlocked but admissions are serialized - a slower but spend-SAFE gate.
// Callers of Spent()/ResetSpend() block behind an in-flight relay's header phase; keep those
// off any hot render path.
func (h *ProxyOptionsHolder) admit(budget float64) (release func(cost float64), ok bool) {
	h.budgetMu.Lock()
	if budget > 0 && h.spent >= budget-1e-9 {
		h.budgetMu.Unlock()
		return nil, false
	}
	var once sync.Once
	return func(cost float64) {
		once.Do(func() {
			h.spent += cost
			h.budgetMu.Unlock()
		})
	}, true
}

// openAIError writes an OpenAI-shaped JSON error envelope: {"error":{"message","type","code"}}
// with the given status and application/json (agents JSON-decode every non-2xx and branch on
// error.type, so it is a contract - ruling 3). json encoding keeps the body valid even when
// the message carries quotes/newlines. code "" is omitted.
func openAIError(w http.ResponseWriter, status int, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	e := map[string]any{"message": msg, "type": typ}
	if code != "" {
		e["code"] = code
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// bearerOK constant-time-compares the request's Authorization against "Bearer <key>". It uses
// crypto/subtle.ConstantTimeCompare (never == / no prefix match) so a local attacker cannot
// time-oracle the key byte by byte. A missing/short/wrong-scheme header is refused.
func bearerOK(authHeader, key string) bool {
	const p = "Bearer "
	if !strings.HasPrefix(authHeader, p) {
		return false
	}
	got := authHeader[len(p):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(key)) == 1
}

// writeModelsList answers a GET /v1/models probe in OpenAI list shape reflecting the
// CURRENTLY-tuned band only (one entry, ruling 4). owned_by "rogerai".
func writeModelsList(w http.ResponseWriter, model string, created int64) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":       model,
			"object":   "model",
			"created":  created,
			"owned_by": "rogerai",
		}},
	})
}

// rewriteModel replaces the top-level "model" field of a chat body with the tuned band's
// model, preserving every other field's VALUE unchanged (map[string]json.RawMessage keeps each
// value's raw JSON, so numbers/tools/stream/unknown fields survive exactly; only top-level key
// ORDER may differ after the re-marshal, which is semantically irrelevant). A body that is
// not a JSON object (malformed, empty, an array, null) is rejected: ok=false -> the caller
// 400s BEFORE any relay/hold so a broken client never spends. When target=="" (legacy
// single-user) the body is returned unchanged and the body's own model is reported.
func rewriteModel(body []byte, target string) (out []byte, model string, ok bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, "", false
	}
	if target == "" {
		var mm struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &mm)
		return body, mm.Model, true
	}
	enc, _ := json.Marshal(target)
	m["model"] = enc
	out, err := json.Marshal(m)
	if err != nil {
		return nil, "", false
	}
	return out, target, true
}

// ProxyHandler returns the local OpenAI-compatible handler over a FIXED options snapshot. It
// is the stable entry point for the legacy single-user path (`roger use`) and the relay tests.
func ProxyHandler(opts ProxyOptions) http.Handler {
	return ProxyHandlerLive(NewProxyOptionsHolder(opts))
}

// ProxyHandlerLive returns the OpenAI-compatible handler reading its options LIVE from the
// holder on every request, so a re-tune re-points the SAME endpoint (ruling 9). It hardens the
// proxy per §5: an OpenAI-list /v1/models probe, per-request model rewrite, per-session bearer
// auth, a per-session spend budget, OpenAI-shaped JSON on every originated error, a 413 body
// cap, Retry-After passthrough, dial/header stream bounds, and a "no band tuned" refusal.
func ProxyHandlerLive(h *ProxyOptionsHolder) http.Handler {
	httpClient := newRelayClient()
	policy := defaultPolicy()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		opts := h.Get()
		if opts.SessionKey != "" && !bearerOK(r.Header.Get("Authorization"), opts.SessionKey) {
			openAIError(w, http.StatusUnauthorized, "authentication_error", "", "missing or invalid API key")
			return
		}
		if r.Method != http.MethodGet {
			openAIError(w, http.StatusNotFound, "invalid_request_error", "unknown_url", "unknown url: "+r.Method+" "+r.URL.Path)
			return
		}
		writeModelsList(w, opts.Model, h.created)
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		opts := h.Get()
		// Ruling 5: a disconnected proxy (endpoint bound, no band tuned) refuses to spend,
		// never serves a stale band.
		if !h.Connected() {
			openAIError(w, http.StatusServiceUnavailable, "api_error", "no_band_tuned", "no band tuned - open a channel first")
			return
		}
		// Auth precedes spend (ruling 3): a missing/wrong bearer never reaches the broker.
		if opts.SessionKey != "" && !bearerOK(r.Header.Get("Authorization"), opts.SessionKey) {
			openAIError(w, http.StatusUnauthorized, "authentication_error", "", "missing or invalid API key")
			return
		}
		// Body cap (ruling 8): over 4 MiB -> OpenAI-shaped 413, never silent truncation.
		body, over := readCappedBody(r.Body, proxyBodyCap)
		if over {
			openAIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "request body exceeds the 4 MiB limit")
			return
		}
		// Model rewrite + malformed-body guard (ruling 2): rewrite `model` to the band's, keep
		// every other field; a non-object body is a 400 before any relay/hold.
		rewritten, model, ok := rewriteModel(body, opts.Model)
		if !ok {
			openAIError(w, http.StatusBadRequest, "invalid_request_error", "", "request body is not valid JSON")
			return
		}
		crit := Criteria{Model: model, Confidential: opts.Confidential, MinTPS: opts.MinTPS, MaxPriceIn: opts.MaxPriceIn, MaxPriceOut: opts.MaxPriceOut, Pref: opts.Pref}
		// Per-session spend budget (ruling 1/2): hard-stop the NEXT request once spent >= cap.
		release, admitted := h.admit(opts.Budget)
		if !admitted {
			openAIError(w, http.StatusPaymentRequired, "insufficient_quota", "budget_exceeded", "session spend budget reached - raise it with /budget to continue")
			return
		}
		// release must fire exactly once; the relay fires it with the billed cost right at the
		// response headers. The deferred release(0) is a no-op backstop (sync.Once) that
		// guarantees the budget slot is freed even on an unexpected relay return path.
		defer release(0)
		relayWithFailover(r.Context(), w, opts, crit, rewritten, httpClient, policy, release)
	})

	// Catch-all: every other route (/, /v1/embeddings, /v1/responses, /healthz, …) is an
	// OpenAI-shaped JSON 404, never Go's plain-text "404 page not found" that crashes SDK
	// JSON decoders (ruling 3).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		openAIError(w, http.StatusNotFound, "invalid_request_error", "unknown_url", "unknown url: "+r.URL.Path)
	})
	return mux
}

// readCappedBody reads up to limit+1 bytes; if the extra byte is present the body EXCEEDED the
// cap (over=true) so the caller 413s. A body of exactly limit bytes is returned whole.
func readCappedBody(r io.Reader, limit int64) (body []byte, over bool) {
	b, _ := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(b)) > limit {
		return nil, true
	}
	return b, false
}

// relayWithFailover runs the bounded retry/failover loop for one client request.
// It first lets the broker pick (cheapest match); on a retryable failure it
// re-queries /discover, picks an alternative that still meets the criteria,
// pins it, and retries with backoff - excluding every provider that already
// failed. On total exhaustion it returns a clear 502 and fires opts.Alert.
// onServed, when non-nil, is invoked EXACTLY once with the request's billed cost (in dollars,
// from X-RogerAI-Cost) the moment a response is settled - on success right before the body is
// streamed, or with 0 on total failover exhaustion. The proxy handler uses it to accumulate
// the per-session spend and release the budget slot before the (possibly long) body stream.
func relayWithFailover(ctx context.Context, w http.ResponseWriter, opts ProxyOptions, crit Criteria, body []byte, httpClient *http.Client, policy failoverPolicy, onServed func(cost float64)) {
	if onServed == nil {
		onServed = func(float64) {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	failed := map[string]bool{}
	pin := "" // "" = let the broker choose; otherwise a failover-selected node
	var lastErr error
	var lastStatus int

	for attempt := 0; attempt < policy.maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(policy.backoff(attempt))
		}
		// Thread the caller's request context so a client disconnect / cancel propagates
		// upstream (ruling 7: bound by dial + response-header timeouts AND the request context;
		// a healthy body still streams to the broker's own ceiling).
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, opts.Broker+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// Sign the request with the local user key: the broker derives the spending
		// wallet from the verified pubkey (X-Roger-User is sent only as a legacy,
		// unauthenticated hint). This is the P0 security fix - a header alone can no
		// longer spend someone else's wallet.
		signRequest(req, body)
		req.Header.Set("X-Roger-User", opts.User)
		if opts.Confidential {
			req.Header.Set("X-Roger-Confidential", "1")
		}
		if opts.MinTPS > 0 {
			req.Header.Set("X-Roger-Min-TPS", fmt.Sprintf("%g", opts.MinTPS))
		}
		if opts.MaxPriceIn > 0 {
			req.Header.Set("X-Roger-Max-Price", fmt.Sprintf("%g", opts.MaxPriceIn))
		}
		// Always carry an out-price cap: the caller's, or the default consumer ceiling
		// when none was set. This is the enforced overpay guard - it bounds even a
		// headless / --yes / scripted caller that never saw the interactive confirm.
		req.Header.Set("X-Roger-Max-Price-Out", fmt.Sprintf("%g", effectiveMaxOut(opts.MaxPriceOut)))
		// Private band tune-in: carry the frequency code so the broker admits ONLY the
		// resolved (hidden) station. The code is discovery + routing admission, NOT
		// spend-auth - the request is still signed (above) and billed to the signed
		// wallet; self-use stays $0. Failover via /discover won't see a private node, so
		// a freq channel simply has no public alternative to fail over to (by design).
		if opts.Freq != "" {
			req.Header.Set("X-Roger-Freq", opts.Freq)
		}
		// Routing knob: forward the user's cheap/fast/reliable preference so the broker
		// reshapes the SCORE accordingly (default balanced when unset).
		if opts.Pref != "" {
			req.Header.Set("X-Roger-Pref", opts.Pref)
		}
		if pin != "" {
			req.Header.Set("X-Roger-Node", pin)
		}
		if len(failed) > 0 {
			req.Header.Set("X-Roger-Exclude-Nodes", joinSet(failed))
		}

		resp, err := httpClient.Do(req)
		if err == nil && !retryable(resp.StatusCode, nil) {
			// Success (or a non-retryable 4xx the caller must see) - stream it back.
			provider := resp.Header.Get("X-RogerAI-Provider")
			if attempt > 0 && opts.Alert != nil && resp.StatusCode < 400 {
				opts.Alert(fmt.Sprintf("recovered: re-routed to %s after %d attempt(s)", provider, attempt))
			}
			// Bill the session budget from the settled cost header, and release the budget
			// slot, BEFORE streaming the (possibly long) body - so only the header phase is
			// serialized. A response with no cost header accumulates nothing (fail-safe).
			cost, _ := strconv.ParseFloat(resp.Header.Get("X-RogerAI-Cost"), 64)
			onServed(cost)
			copyRelayResponse(w, resp)
			resp.Body.Close()
			return
		}

		// Retryable failure - record what failed and pick an alternative.
		if err != nil {
			lastErr, lastStatus = err, 0
		} else {
			lastErr, lastStatus = nil, resp.StatusCode
			if p := resp.Header.Get("X-RogerAI-Provider"); p != "" {
				failed[p] = true
			}
			resp.Body.Close()
		}
		// If we had pinned a node, it failed too - never retry it.
		if pin != "" {
			failed[pin] = true
		}
		alt, ok := selectAlternative(opts.Broker, crit, failed)
		if !ok {
			break // nothing else fits the criteria
		}
		pin = alt
	}

	msg := failoverError(crit, lastStatus, lastErr)
	if opts.Alert != nil {
		opts.Alert(msg)
	}
	// Exhaustion bills nothing; free the budget slot, then return an OpenAI-shaped 502 (SDKs
	// JSON-decode the body and crash on Go's plain text) - ruling 3.
	onServed(0)
	openAIError(w, http.StatusBadGateway, "api_error", "upstream_unavailable", msg)
}

// failoverError builds the user-facing message when no provider could serve the
// request after exhausting failover.
func failoverError(crit Criteria, lastStatus int, lastErr error) string {
	reason := "all matching providers failed"
	switch {
	case lastErr != nil:
		reason = "broker unreachable: " + lastErr.Error()
	case lastStatus != 0:
		reason = fmt.Sprintf("last provider returned %d", lastStatus)
	}
	constraints := []string{}
	if crit.Confidential {
		constraints = append(constraints, "confidential")
	}
	if crit.MinTPS > 0 {
		constraints = append(constraints, fmt.Sprintf("min-tps=%g", crit.MinTPS))
	}
	if crit.MaxPriceIn > 0 {
		constraints = append(constraints, fmt.Sprintf("max-in=%g", crit.MaxPriceIn))
	}
	if crit.MaxPriceOut > 0 {
		constraints = append(constraints, fmt.Sprintf("max-out=%g", crit.MaxPriceOut))
	}
	suffix := ""
	if len(constraints) > 0 {
		suffix = " matching [" + strings.Join(constraints, " ") + "]"
	}
	return fmt.Sprintf("no provider available for %q%s - %s", crit.Model, suffix, reason)
}

// copyRelayResponse mirrors the broker's response (status, meter headers, body)
// to the local client, flushing per chunk so SSE streaming works end-to-end.
func copyRelayResponse(w http.ResponseWriter, resp *http.Response) {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	// Deny-by-default allowlist: the safe meter headers plus Retry-After (so a 429'd agent can
	// back off - ruling 7). Hop-by-hop / connection-scoped / cookie / server headers are NEVER
	// forwarded (RFC 7230 §6.1); keep this list tight.
	for _, h := range []string{"X-RogerAI-Provider", "X-RogerAI-Cost", "X-RogerAI-Balance", "X-RogerAI-Receipt", "X-RogerAI-Price", "X-RogerAI-TPS", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

// joinSet renders a set as a comma-separated header value.
func joinSet(set map[string]bool) string {
	parts := make([]string, 0, len(set))
	for k := range set {
		parts = append(parts, k)
	}
	return strings.Join(parts, ",")
}

// Consumer price-safety bounds (the spend side of the marketplace's price guards).
//
//   - ConsumerDefaultMaxOut is the out-price ceiling APPLIED when the caller set no cap
//     (no --max-out, no stored limit). It closes the accidental-overpay path: even a
//     headless / --yes caller is bounded to this unless it opts into a higher cap.
//   - ConsumerConfirmThreshold is the out-price above which the interactive confirm
//     escalates from a (y/N) to TYPE-THE-PRICE, so an expensive station cannot be
//     waved through by a reflexive yes.
const (
	ConsumerDefaultMaxOut    = 10.0 // $/1M out
	ConsumerConfirmThreshold = 20.0 // $/1M out
)

// priceMatches reports whether a typed out-price confirms the shown one, tolerating
// float/round noise (the user reads "12.50" and types "12.50"; an exact-string match
// would be brittle). A cent of slack is plenty for a $/1M price.
func priceMatches(typed, shown float64) bool {
	d := typed - shown
	if d < 0 {
		d = -d
	}
	return d <= 0.01
}

// EffectiveMaxOut applies the default consumer out-price cap when none was set, so the
// relay always carries a max-out the broker can enforce (the headless-overpay guard).
// Exported so the agent harness (which builds its own relay request) injects the SAME
// cap as `use`/`Chat` - one source of truth for the consumer cap across every path.
func EffectiveMaxOut(maxOut float64) float64 {
	if maxOut <= 0 {
		return ConsumerDefaultMaxOut
	}
	return maxOut
}

// effectiveMaxOut is the internal alias kept for the package's existing call sites.
func effectiveMaxOut(maxOut float64) float64 { return EffectiveMaxOut(maxOut) }

// UseOptions are the resolved spend limits + flags for `roger use`.
type UseOptions struct {
	Port         int
	Confidential bool
	MaxIn        float64 // cap on $/1M input price (0 = none)
	MaxOut       float64 // cap on $/1M output price (0 = none); the headline cap
	MinTPS       float64 // throughput floor (0 = none)
	TypicalOut   int     // output tokens for the est-cost line (default 800)
	Yes          bool    // skip the (y/N) confirm (scripts / Hermes / bots)
	Freq         string  // private band frequency code (empty = open market). Routes via X-Roger-Freq.
}

// balanceOf fetches the caller's wallet credits (best-effort; -1 if unavailable).
func balanceOf(broker, user string) float64 {
	var b struct {
		Balance float64 `json:"balance"`
	}
	if err := getJSON(broker, "/balance", user, &b); err != nil {
		return -1
	}
	return b.Balance
}

// Use opens a local OpenAI-compatible endpoint that relays to the broker. Before
// binding the endpoint it surfaces the live cross-station out-price range for the
// band, picks the cheapest station within the spend limits, shows the estimated
// cost per typical reply + balance, and requires an explicit (y/N) confirm
// (default DENY). --yes skips the prompt for scripts/Hermes. When nothing is on
// air within the limits it prints the gap (cheapest vs your max) and lets the
// user type a new max or abort; a new max re-checks.
func Use(broker, user, model string, opt UseOptions) error {
	typical := opt.TypicalOut
	if typical <= 0 {
		typical = 800
	}
	maxOut := opt.MaxOut
	// Consumer price-safety: when NO out-price cap was set (no --max-out, no stored
	// limit), apply the default ConsumerDefaultMaxOut ceiling. This closes the one real
	// accidental-overpay path - a headless / --yes caller with no cap would otherwise
	// pay whatever the cheapest station charges. The relay ALSO enforces this default
	// (relayWithFailover), so the guard holds even for callers that bypass this prompt.
	defaultedCap := false
	if maxOut <= 0 {
		maxOut = ConsumerDefaultMaxOut
		defaultedCap = true
	}
	in := useStdin
	var locked BandRange // the station we resolve + confirm (used for the staged lock)
	_ = defaultedCap

	// Private band tune-in (--freq): resolve the frequency code against the broker's
	// PUBLIC constant-work resolver (no login), then open the channel routed via
	// X-Roger-Freq. A wrong / off-air code returns the SAME uniform "no station" reply
	// the broker gives (no oracle). The price-safety confirm + default cap still apply.
	if opt.Freq != "" {
		return useOnFreq(broker, user, model, opt, maxOut, typical, defaultedCap, in)
	}

	for {
		br, ok := BandRangeFor(broker, model)
		if !ok {
			fmt.Printf("no station on air for %q right now - try `roger search` or come back.\n", model)
			return nil
		}
		locked = br
		// Is the cheapest station within the out-price cap?
		if maxOut > 0 && br.Min > maxOut {
			gap := br.Min - maxOut
			pct := gap / maxOut * 100
			fmt.Printf("\n  the band is above your limit  %s\n", model)
			fmt.Printf("    cheapest on air   %.2f $/1M out   @%s   %s\n", br.Min, br.CheapNode, tpsLabel(br.CheapTPS))
			fmt.Printf("    your max          %.2f $/1M out\n", maxOut)
			fmt.Printf("    gap               +%.2f  (%.0f%% over)   you would pay $%.6f / reply\n", gap, pct, estReplyCost(br.Min, typical))
			fmt.Printf("    the band is %s today.\n", rangeLabel(br))
			if opt.Yes {
				return fmt.Errorf("cheapest on air %.2f > your max-out %.2f for %q (--yes: not raising the limit)", br.Min, maxOut, model)
			}
			fmt.Printf("\n  raise your max for %s (enter a new $/1M out, or blank to abort): ", model)
			line, _ := readLine(in)
			line = strings.TrimSpace(line)
			if line == "" {
				fmt.Println("  aborted - no channel opened.")
				return nil
			}
			nm, err := strconv.ParseFloat(line, 64)
			if err != nil || nm <= 0 {
				fmt.Println("  not a number - aborting.")
				return nil
			}
			maxOut = nm
			continue // re-check with the new max
		}
		// Within limits (or no cap): show the deal and confirm.
		fmt.Printf("\n  tune in to  %s\n", model)
		if br.Stations == 1 {
			fmt.Printf("    price now      %.2f $/1M out   ·   %.2f $/1M in\n", br.Min, br.CheapIn)
		} else {
			fmt.Printf("    live range     %s   (%d stations on air)\n", rangeLabel(br), br.Stations)
			fmt.Printf("    price now      %.2f $/1M out   ·   %.2f $/1M in   (cheapest)\n", br.Min, br.CheapIn)
		}
		fmt.Printf("    station        @%s   %s   (the strongest match)\n", br.CheapNode, tpsLabel(br.CheapTPS))
		if maxOut > 0 {
			note := "(within limit)"
			if defaultedCap {
				note = "(default safety cap - pass --max-out to change)"
			}
			fmt.Printf("    your max       %.2f $/1M out   %s\n", maxOut, note)
		}
		fmt.Printf("    est. cost      ~ $%.6f / typical reply  (~%d out tokens)\n", estReplyCost(br.Min, typical), typical)
		if bal := balanceOf(broker, user); bal >= 0 {
			per100 := estReplyCost(br.Min, typical) * 100
			fmt.Printf("                   ~ $%.6f / 100 replies        balance $%.4f\n", per100, bal)
		}
		fmt.Printf("    locked         each reply price-locks at send; a hold pre-auths your session\n")

		// HIGH-PRICE confirm: above ConsumerConfirmThreshold $/1M out we require the user
		// to TYPE THE PRICE (not just "y"), so a fat-finger on an expensive station can't
		// be waved through by a reflexive yes. A --yes/headless caller is still bounded by
		// the relay's enforced max-out cap, so it cannot silently overpay either.
		if !opt.Yes {
			if br.Min > ConsumerConfirmThreshold {
				fmt.Printf("\n  this station is %.2f $/1M out - above the $%.0f confirm line.\n", br.Min, ConsumerConfirmThreshold)
				fmt.Printf("  to confirm, TYPE THE OUT-PRICE exactly (%.2f), or blank to abort: ", br.Min)
				line, _ := readLine(in)
				typed, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
				if err != nil || !priceMatches(typed, br.Min) {
					fmt.Println("  price not confirmed - no channel opened.")
					return nil
				}
			} else {
				fmt.Printf("\n  open the channel? (y/N) ")
				line, _ := readLine(in)
				if !isYes(line) {
					fmt.Println("  denied - no channel opened.")
					return nil
				}
			}
		}
		break
	}

	addr := fmt.Sprintf("127.0.0.1:%d", opt.Port)
	// The staged tune-in: scan -> lock -> lineage handshake -> CHANNEL OPEN, mirroring
	// the TUI sequence + the website's animation. Plain text (CLI is non-interactive),
	// ◉ on-air / ◆ verified shared with the band table, so the lock reads the same on
	// screen and in a pipe.
	verified := ""
	if opt.Confidential {
		verified = "  " + glyphVerify + " verified"
	}
	fmt.Printf("\n  %s scanning stations ... ok\n", glyphOnAir)
	fmt.Printf("  %s locking strongest @%s · %s · %.2f $/M ... ok\n", glyphOnAir, locked.CheapNode, tpsLabel(locked.CheapTPS), locked.Min)
	fmt.Printf("  %s lineage handshake %s weights·shard·token ... ok\n", glyphOnAir, glyphVerify)
	fmt.Printf("  %s CHANNEL OPEN %s via @%s%s\n", glyphOnAir, model, locked.CheapNode, verified)
	// A per-session bearer key (the hardened proxy enforces Authorization on every route), the
	// tuned band's model (the proxy rewrites any incoming model to it), and NO session spend cap
	// (Budget 0 = unlimited - `roger use` is a single-user, hands-on flow; the guest-operator
	// launch is where DefaultSessionBudget applies).
	sessionKey := NewSessionKey()
	// The clean, aligned BASE URL / API KEY / MODEL plate (matches the TUI plate).
	fmt.Printf("\n  %-9s http://%s/v1\n", "BASE URL", addr)
	fmt.Printf("  %-9s %s\n", "API KEY", sessionKey)
	fmt.Printf("  %-9s %s\n", "MODEL", model)
	if opt.MaxIn > 0 || maxOut > 0 || opt.MinTPS > 0 {
		fmt.Printf("  %-9s max-in=%g  max-out=%g $/1M   min-tps=%g t/s\n", "LIMITS", opt.MaxIn, maxOut, opt.MinTPS)
	}
	fmt.Printf("\n  drop-in, OpenAI-compatible - point any OpenAI tool here. roger that.\n")
	fmt.Printf("  OPENAI_API_BASE=http://%s/v1  OPENAI_API_KEY=%s   (Ctrl-C to stop)\n", addr, sessionKey)
	opts := ProxyOptions{Broker: broker, User: user, Model: model, SessionKey: sessionKey, Confidential: opt.Confidential, MaxPriceIn: opt.MaxIn, MaxPriceOut: maxOut, MinTPS: opt.MinTPS, Alert: func(s string) {
		fmt.Fprintln(os.Stderr, "rogerai: "+s)
	}}
	return useServe(addr, ProxyHandler(opts))
}

// useStdin / useServe are seams over the two side effects Use can't run in a test: the
// interactive confirm reader (default os.Stdin) and the blocking local-proxy listener
// (default http.ListenAndServe). Tests point useStdin at an os.Pipe and useServe at a
// capture func so every branch up to and including "channel open" is exercised without
// reading the real terminal or binding a forever-blocking port.
var (
	useStdin = os.Stdin
	useServe = http.ListenAndServe
)

// useOnFreq is the private-band branch of Use: resolve a frequency code, confirm the
// price (same price-safety as the open market), then bind a local endpoint that routes
// every request via X-Roger-Freq. A wrong / off-air code gets the broker's uniform
// "no station on that frequency" reply. The code is discovery + routing admission only
// - spend still uses the signed wallet, self-use stays $0.
func useOnFreq(broker, user, model string, opt UseOptions, maxOut float64, typical int, defaultedCap bool, in *os.File) error {
	offers, display, ok := ResolveBand(broker, opt.Freq, model)
	if !ok {
		fmt.Println("  no station on that frequency (it may be off air) - check the code.")
		return nil
	}
	// Cheapest matching station on the band (out-price), for the price screen.
	br, _ := bandRange(offers, model)
	if br.Stations == 0 {
		// Resolved offers but none match the model exactly (shouldn't happen: resolve
		// filtered by model) - treat as no station, uniform.
		fmt.Println("  no station on that frequency (it may be off air) - check the code.")
		return nil
	}
	if display == "" {
		display = "private band"
	}
	fmt.Printf("\n  tune in to  %s   on   %s\n", model, display)
	fmt.Printf("    price now      %.2f $/1M out   ·   %.2f $/1M in   (private)\n", br.Min, br.CheapIn)
	fmt.Printf("    station        @%s   %s\n", br.CheapNode, tpsLabel(br.CheapTPS))
	if maxOut > 0 {
		note := "(within limit)"
		if defaultedCap {
			note = "(default safety cap - pass --max-out to change)"
		}
		fmt.Printf("    your max       %.2f $/1M out   %s\n", maxOut, note)
	}
	fmt.Printf("    est. cost      ~ $%.6f / typical reply  (~%d out tokens)\n", estReplyCost(br.Min, typical), typical)

	// Same price-safety confirm as the open market: above the threshold the user must
	// TYPE THE PRICE; otherwise a (y/N). --yes/headless is still bounded by the relay's
	// enforced max-out cap.
	if !opt.Yes {
		if br.Min > ConsumerConfirmThreshold {
			fmt.Printf("\n  this station is %.2f $/1M out - above the $%.0f confirm line.\n", br.Min, ConsumerConfirmThreshold)
			fmt.Printf("  to confirm, TYPE THE OUT-PRICE exactly (%.2f), or blank to abort: ", br.Min)
			line, _ := readLine(in)
			typed, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
			if err != nil || !priceMatches(typed, br.Min) {
				fmt.Println("  price not confirmed - no channel opened.")
				return nil
			}
		} else {
			fmt.Printf("\n  open the channel? (y/N) ")
			line, _ := readLine(in)
			if !isYes(line) {
				fmt.Println("  denied - no channel opened.")
				return nil
			}
		}
	}

	addr := fmt.Sprintf("127.0.0.1:%d", opt.Port)
	fmt.Printf("\n  %s scanning frequency ... ok\n", glyphOnAir)
	fmt.Printf("  %s locking @%s · %s · %.2f $/M ... ok\n", glyphOnAir, br.CheapNode, tpsLabel(br.CheapTPS), br.Min)
	fmt.Printf("  %s CHANNEL OPEN (private) %s via @%s\n", glyphOnAir, model, br.CheapNode)
	sessionKey := NewSessionKey()
	fmt.Printf("\n  %-9s http://%s/v1\n", "BASE URL", addr)
	fmt.Printf("  %-9s %s\n", "API KEY", sessionKey)
	fmt.Printf("  %-9s %s\n", "MODEL", model)
	fmt.Printf("  %-9s %s\n", "FREQ", display)
	fmt.Printf("\n  drop-in, OpenAI-compatible - point any OpenAI tool here. roger that.\n")
	fmt.Printf("  OPENAI_API_BASE=http://%s/v1  OPENAI_API_KEY=%s   (Ctrl-C to stop)\n", addr, sessionKey)
	opts := ProxyOptions{Broker: broker, User: user, Model: model, SessionKey: sessionKey, MaxPriceIn: opt.MaxIn, MaxPriceOut: maxOut, MinTPS: opt.MinTPS, Freq: opt.Freq, Alert: func(s string) {
		fmt.Fprintln(os.Stderr, "rogerai: "+s)
	}}
	return useServe(addr, ProxyHandler(opts))
}

// rangeLabel renders a cross-station spread as "min ~ max" ($/1M out), or a single
// point price when there is only one station (do not fake a spread).
func rangeLabel(br BandRange) string {
	if br.Stations <= 1 || br.Min == br.Max {
		return fmt.Sprintf("%.2f $/1M out", br.Min)
	}
	return fmt.Sprintf("%.2f ~ %.2f $/1M out", br.Min, br.Max)
}

// tpsLabel renders measured throughput, or a dash when unmeasured.
func tpsLabel(tps float64) string {
	if tps <= 0 {
		return "- t/s"
	}
	return fmt.Sprintf("%.0f t/s", tps)
}

// readLine reads one line from r (stdin), without the trailing newline.
func readLine(r *os.File) (string, error) {
	buf := make([]byte, 0, 64)
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				break
			}
			if one[0] != '\r' {
				buf = append(buf, one[0])
			}
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}

// isYes reports whether a confirm answer is an explicit yes (default is DENY, so
// only "y"/"yes" accept; anything else - including blank - denies).
func isYes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

// MaxAnswerTokens is the per-turn completion budget shared by the in-channel chat
// (client.ChatDetailed) AND the [0] AGENT harness (harness.agentMaxTokens). It is deliberately
// generous because the channel's model is often a REASONING model (e.g. gpt-oss) whose
// hidden reasoning is billed into this same budget: at a low ceiling (256/1024) the
// reasoning ate nearly all of it and the visible answer truncated mid-word or came back
// EMPTY (the "list my home dir ... stopped at .gtk" bug, and the in-channel 256 truncation
// / empty-reasoning-turn bug). 4096 leaves headroom for the reasoning AND a complete
// answer. One const so the chat surface and the agent never drift apart again.
const MaxAnswerTokens = 4096

// TopupHint is the actionable next step appended to a 402 insufficient-balance reply so
// the user is never dead-ended on "insufficient balance" with nowhere to go. The same
// string is reused by the CLI chat, the TUI channel, and the agent harness so the call
// to action stays identical everywhere.
const TopupHint = "run `roger topup` (or /topup in the TUI) to add funds"

// WithTopupHint appends TopupHint to a broker error message when status is 402
// (insufficient balance). For any other status it returns msg unchanged. Centralized so
// both the chat client and the agent harness map 402 -> the same actionable hint.
func WithTopupHint(status int, msg string) string {
	if status == http.StatusPaymentRequired {
		if strings.TrimSpace(msg) == "" {
			return "insufficient balance - " + TopupHint
		}
		// A monthly-spend-limit 402 already names its own remedy (raise the cap / wait
		// for next month); topping up won't unblock it, so don't append the topup hint.
		if strings.Contains(msg, "monthly spend limit") {
			return msg
		}
		return msg + " - " + TopupHint
	}
	return msg
}

// chatTimeout is generous on purpose: CPU MoE inference (gpt-oss-20b/120b) can
// take well over a minute for a long reply, and the founder's silent-failure
// report was on slow local inference. It must exceed the broker's own 120s
// resCh wait so the broker's "node timed out" message wins the race instead of
// the client's transport timeout (which would surface as an opaque dial error).
const chatTimeout = 300 * time.Second

// ChatResult is the rich outcome of one in-channel relay: the reply plus the
// per-turn performance/billing metrics the TUI surfaces (tokens in/out, tok/s, the
// wall-clock latency, price, and cost). Status keeps the legacy "provider · $cost"
// one-liner for back-compat. Zero-valued metric fields mean "the broker did not
// report it" (the renderer omits those).
type ChatResult struct {
	Reply     string
	Status    string        // legacy compact footer: "provider · $cost"
	Provider  string        // serving node id (X-RogerAI-Provider)
	Cost      float64       // credits billed for this turn (1 cr = $1)
	TokensIn  int           // billed prompt tokens (broker re-count if present, else the claim)
	TokensOut int           // billed completion tokens
	TPS       float64       // provider output tokens/sec (X-RogerAI-TPS)
	PriceIn   float64       // $/1M in for this turn (locked price)
	PriceOut  float64       // $/1M out
	Latency   time.Duration // wall-clock time of the served request (how long you waited)
}

// FormatUSD is the ONE canonical money renderer for every consumer surface, so a cost or
// balance reads identically in the TUI and the CLI: the TUI's dollars() delegates here, and
// the in-channel reply footer's legacy Status line uses it. The rule:
//   - 0            -> "$0.00"
//   - 0 < v < 0.01 -> ~3 significant figures as a PLAIN decimal (e.g. $0.00000036), so a real
//     sub-cent charge never reads as free
//   - v >= 0.01    -> two decimals (e.g. $0.12)
//   - v < 0        -> "-" (never real money here)
func FormatUSD(v float64) string {
	if v < 0 {
		return "-"
	}
	if v == 0 {
		return "$0.00"
	}
	if v >= 0.01 {
		return "$" + fmt.Sprintf("%.2f", v)
	}
	s := strconv.FormatFloat(v, 'g', 3, 64)
	if strings.ContainsAny(s, "eE") {
		// FormatFloat may pick scientific for very small values; expand to plain decimal.
		s = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return "$" + s
}

// ChatDetailed sends one message through the broker and returns the reply plus the
// per-turn metrics (see ChatResult). Used by the TUI's in-CHANNEL chat / session.
// Every failure path returns a clear, human-readable error so the TUI never shows a
// blank no-response: a missing station, a slow-inference timeout, the broker's own
// error body, or a transport drop are all surfaced verbatim instead of as an empty turn.
// maxOut is the consumer out-price cap ($/1M) the relay must carry so the in-channel
// chat is bounded like every other consume path: 0 means "use the default consumer cap"
// (effectiveMaxOut), a positive value is the user's explicit opt-in to pay up to that.
func ChatDetailed(broker, user, model, prompt string, confidential bool, maxOut float64) (ChatResult, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": MaxAnswerTokens,
	})
	httpClient := &http.Client{Timeout: chatTimeout}
	policy := defaultPolicy()
	failed := map[string]bool{} // providers that already failed this turn - never re-pick them
	var lastErr error

	// Bounded retry/failover, mirroring `roger use` (relayWithFailover): on a retryable
	// failure (transport drop, or a broker/node 5xx like "node timed out" / "no node
	// offers") re-send asking the broker to EXCLUDE the station(s) that just failed, with
	// backoff, so one slow/zombie/just-restarted provider no longer dead-ends the channel.
	// A 4xx (bad request, no credits) is the caller's and returns immediately.
	for attempt := 0; attempt < policy.maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(policy.backoff(attempt))
		}
		req, _ := http.NewRequest(http.MethodPost, broker+"/v1/chat/completions", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		signRequest(req, reqBody)
		req.Header.Set("X-Roger-User", user)
		if confidential {
			req.Header.Set("X-Roger-Confidential", "1")
		}
		// Always carry an out-price cap (the caller's, or the default consumer ceiling when
		// none was set) so the in-channel chat relay is bounded against overpay exactly like
		// `roger use` - not only the interactive tune-in confirm.
		req.Header.Set("X-Roger-Max-Price-Out", fmt.Sprintf("%g", effectiveMaxOut(maxOut)))
		if len(failed) > 0 {
			req.Header.Set("X-Roger-Exclude-Nodes", joinSet(failed))
		}

		start := time.Now()
		resp, derr := httpClient.Do(req)
		if derr != nil {
			// Transport timeout/drop: retryable. Keep a clean message in case we exhaust.
			if ne, ok := derr.(interface{ Timeout() bool }); ok && ne.Timeout() {
				lastErr = fmt.Errorf("no reply from the station within %s (it may be slow or offline) - try again or re-tune", chatTimeout)
			} else {
				lastErr = fmt.Errorf("could not reach the broker: %v", derr)
			}
			continue
		}

		// A retryable 5xx (node timed out / no node / broker restarting): note the failed
		// provider so the re-pick avoids it, then fail over to another station.
		if resp.StatusCode >= 500 {
			if p := resp.Header.Get("X-RogerAI-Provider"); p != "" {
				failed[p] = true
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			lastErr = parseChatError(raw, resp.StatusCode)
			continue
		}

		// Terminal: a 2xx success, or a non-retryable 4xx the caller must see (bad request,
		// insufficient credits) - parse and return.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var d struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning"`
				} `json:"message"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(raw, &d)
		if len(d.Choices) == 0 {
			return ChatResult{}, parseChatError(raw, resp.StatusCode)
		}
		reply := d.Choices[0].Message.Content
		if reply == "" {
			reply = d.Choices[0].Message.Reasoning
		}
		costStr := resp.Header.Get("X-RogerAI-Cost")
		costCr, _ := strconv.ParseFloat(costStr, 64)
		provider := resp.Header.Get("X-RogerAI-Provider")
		res := ChatResult{
			Reply:    reply,
			Provider: provider,
			Cost:     costCr,
			Latency:  time.Since(start),
			// Display in dollars (1 credit = $1) via the ONE canonical renderer, so the legacy
			// fallback footer matches the TUI's dollars() exactly (a relabel only; settlement
			// math unchanged). costCr is the parsed exact value from the X-RogerAI-Cost header.
			Status: fmt.Sprintf("%s · %s", provider, FormatUSD(costCr)),
		}
		// Per-turn metrics from the broker's response headers (best-effort: any missing one
		// stays zero and the renderer omits it). The signed receipt carries the BILLED token
		// counts (broker re-count when present), the truthful in/out the user actually paid for.
		if rec, derr := protocol.DecodeReceipt(resp.Header.Get("X-RogerAI-Receipt")); derr == nil {
			res.TokensIn, res.TokensOut = rec.PromptTokens, rec.CompletionTokens
			if rec.BrokerPromptTokens > 0 {
				res.TokensIn = rec.BrokerPromptTokens
			}
			if rec.BrokerCompletionTokens > 0 {
				res.TokensOut = rec.BrokerCompletionTokens
			}
		}
		if tps, perr := strconv.ParseFloat(resp.Header.Get("X-RogerAI-TPS"), 64); perr == nil {
			res.TPS = tps
		}
		res.PriceIn, res.PriceOut = parsePriceHeader(resp.Header.Get("X-RogerAI-Price"))
		return res, nil
	}

	// Every attempt failed over - surface the last real cause.
	if lastErr == nil {
		lastErr = fmt.Errorf("no station could serve %s right now (tried %d)", model, policy.maxAttempts)
	}
	return ChatResult{}, lastErr
}

// parsePriceHeader parses the broker's "in=0.2000;out=0.5000;locked_until=..." price
// header into the in/out $/1M values (0,0 if absent/malformed).
func parsePriceHeader(h string) (in, out float64) {
	for _, part := range strings.Split(h, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		v, _ := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64)
		switch strings.TrimSpace(kv[0]) {
		case "in":
			in = v
		case "out":
			out = v
		}
	}
	return in, out
}

// parseChatError turns an errorful /v1/chat/completions response (no choices) into the
// best human message: the broker/provider's own error text when present (with the topup
// hint on a 402), else a status-coded fallback. Shared by the relay's failover retries
// and its terminal path so both name the real cause.
func parseChatError(raw []byte, status int) error {
	var d struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &d)
	if d.Error.Message != "" {
		return fmt.Errorf("%s", WithTopupHint(status, d.Error.Message))
	}
	if status >= 400 {
		if msg := strings.TrimSpace(string(raw)); msg != "" && len(msg) < 300 {
			return fmt.Errorf("%s (status %d)", WithTopupHint(status, msg), status)
		}
		if status == http.StatusPaymentRequired {
			return fmt.Errorf("%s", WithTopupHint(status, ""))
		}
		return fmt.Errorf("the station returned status %d with no reply", status)
	}
	return fmt.Errorf("the station sent an empty response (status %d)", status)
}
