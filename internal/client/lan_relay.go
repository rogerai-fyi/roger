package client

// The LAN-mode relay (features/lan/routing_and_inference.feature): this machine's own serve,
// then Edge members over mutual TLS, then LAN Towers, or a refusal. There is no market rung,
// so no path through here can reach the broker.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/lanmode"
)

// relayLAN serves the turn on the LAN or refuses it; it never returns (false, false).
func relayLAN(ctx context.Context, w http.ResponseWriter, opts ProxyOptions, band string, body []byte, onServed func(float64)) (served, refused bool) {
	var tried []string
	if opts.LANSelf != nil {
		if up, key := opts.LANSelf(band); up != "" {
			if resp, err := lanPost(ctx, opts, nil, up, body, false, key); err == nil {
				onServed(0)
				w.Header().Set("X-RogerAI-Cost", "0") // a LAN turn is free and says so
				copyRelayResponse(w, resp, !opts.ReasoningFallbackOff, nil)
				resp.Body.Close()
				return true, false
			} else {
				tried = append(tried, "this machine: "+err.Error())
			}
		}
	}
	if ok, peerTried := tryEdgePeers(ctx, w, opts, band, body, onServed); ok {
		return true, false
	} else {
		tried = append(tried, peerTried...)
	}
	if opts.LANTowers != nil {
		for _, entry := range opts.LANTowers(band) {
			base, why := lanTowerBase(ctx, opts, entry)
			if why != "" {
				tried = append(tried, entry+": "+why)
				continue
			}
			resp, err := lanPost(ctx, opts, opts.LANHTTP, base+"/v1/chat/completions", body, true, "")
			if err != nil {
				tried = append(tried, entry+": "+err.Error())
				continue
			}
			if resp.StatusCode >= 500 {
				// a Tower that could not serve (no station free, overloaded) is a failure to move
				// past, inside the LAN, not an answer to hand the caller
				resp.Body.Close()
				tried = append(tried, fmt.Sprintf("%s: status %d", entry, resp.StatusCode))
				continue
			}
			onServed(0)
			w.Header().Set("X-RogerAI-Cost", "0") // a LAN turn is free and says so
			copyRelayResponse(w, resp, !opts.ReasoningFallbackOff, nil)
			resp.Body.Close()
			return true, false
		}
	}
	msg := "nothing on this LAN serves " + band + ": LAN mode is on and the market was not tried - serve it here (roger model serve), add an Edge member, or add a Tower (roger lan add); turning LAN mode off would use the market"
	if len(tried) > 0 {
		msg = "every local source serving " + band + " failed (" + strings.Join(tried, "; ") + "); LAN mode is on and the market was not tried"
	}
	openAIError(w, http.StatusServiceUnavailable, "lan_error", "lan_only", msg)
	return false, true
}

// lanTowerBase checks a Tower entry NOW and returns the base URL of the address the check
// approved, so the connection can never go to a different address than the one judged local.
func lanTowerBase(ctx context.Context, opts ProxyOptions, entry string) (string, string) {
	a, err := lanmode.ParseAddr(entry)
	if err != nil {
		return "", err.Error()
	}
	v := lanmode.Classify(ctx, a, opts.LANResolve)
	if !v.Local && !(v.CGNAT && a.Literal()) {
		return "", v.Reason
	}
	if a.Literal() {
		return a.Base(), ""
	}
	return "http://" + resolvedKey(ctx, a, opts.LANResolve), ""
}

// lanPost sends one turn to a local endpoint (hc nil = a plain client, for this machine's own
// loopback serve). A Tower request is signed (with the per-request
// nonce a LAN Tower's replay guard wants) and carries no account key; a redirect is never
// followed, because a source is an address the owner chose.
func lanPost(ctx context.Context, opts ProxyOptions, hc *http.Client, url string, body []byte, sign bool, bearer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer) // this machine's own runtime key, loopback only
	}
	if sign {
		key := opts.LANKey
		if key == nil {
			key = LoadOrCreateUserKey()
		}
		signWithKey(req, body, key)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := noRedirects(hc).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("answered with a redirect, which is not followed")
	}
	return resp, nil
}

// useLocal opens a session on local sources only (LAN mode, or a pinned node or Tower): no
// market lookup, no balance, no price, hold or cost text, because nothing here is billed.
func useLocal(broker, user, model string, opt UseOptions) error {
	var sources []LANSource
	if opt.LANSources != nil {
		sources = opt.LANSources(model)
	}
	if len(sources) == 0 && opt.Pinned == "" {
		return fmt.Errorf("nothing on this LAN serves %q: checked this machine, your Edge and your LAN Towers - serve it here (roger model serve), add an Edge member, or add a Tower (roger lan add); turning LAN mode off would use the market", model)
	}
	label := opt.Pinned
	if label == "" {
		labels := make([]string, 0, len(sources))
		for _, s := range sources {
			labels = append(labels, s.Label())
		}
		label = strings.Join(labels, ", ")
	}
	addr := fmt.Sprintf("127.0.0.1:%d", opt.Port)
	sessionKey := NewSessionKey()
	mode := "LAN"
	if !opt.LANMode {
		mode = "pinned"
	}
	fmt.Printf("\n  %s CHANNEL OPEN %s · %s · %s\n", glyphOnAir, model, mode, label)
	fmt.Printf("    source    %s\n", label)
	fmt.Printf("    price     free · local (nothing is billed)\n")
	fmt.Printf("\n  %-9s http://%s/v1\n", "BASE URL", addr)
	fmt.Printf("  %-9s %s\n", "API KEY", sessionKey)
	fmt.Printf("  %-9s %s\n", "MODEL", model)
	fmt.Printf("\n  OPENAI_API_BASE=http://%s/v1  OPENAI_API_KEY=%s   (Ctrl-C to stop)\n", addr, sessionKey)
	opts := ProxyOptions{Broker: broker, User: user, Model: model, SessionKey: sessionKey,
		ReasoningFallbackOff: opt.Raw || rawReasoningEnv(), OnReceipt: opt.OnReceipt,
		Alert:     func(s string) { fmt.Fprintln(os.Stderr, "rogerai: "+s) },
		EdgePeers: opt.EdgePeers, Prefer: "local-only", EdgeCert: opt.EdgeCert, EdgeVerify: opt.EdgeVerify,
		LANMode: opt.LANMode, LANSelf: opt.LANSelf, LANTowers: opt.LANTowers, LANHTTP: opt.LANHTTP, LANResolve: opt.LANResolve}
	return useServe(addr, newProxyHandler(opts))
}

// The process-wide LAN relay: the TUI chat, the console chat and the agent send their turns
// through it when this process runs in LAN mode, so no path that carries a turn can reach the
// broker (features/lan/regressions.feature). The mode is decided once per process.
var (
	lanRelayMu sync.RWMutex
	lanRelay   *ProxyOptions
)

// SetLANRelay sets (or clears, with nil) the LAN relay for this process.
func SetLANRelay(o *ProxyOptions) {
	lanRelayMu.Lock()
	defer lanRelayMu.Unlock()
	lanRelay = o
}

// LANDo serves a /v1/chat/completions request on the LAN relay when one is set, in process,
// and reports false when this process is not in LAN mode (the caller sends it as before).
func LANDo(req *http.Request, body []byte) (*http.Response, bool) {
	lanRelayMu.RLock()
	o := lanRelay
	lanRelayMu.RUnlock()
	if o == nil {
		return nil, false
	}
	var p struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &p)
	rec := httptest.NewRecorder()
	relayLAN(req.Context(), rec, *o, p.Model, body, func(float64) {})
	return rec.Result(), true
}
