package client

// The LAN-mode relay (features/lan/routing_and_inference.feature): this machine's own serve,
// then Edge members over mutual TLS, then LAN Towers, or a refusal. There is no market rung,
// so no path through here can reach the broker.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/lanmode"
)

// relayLAN serves the turn on the LAN or refuses it; it never returns (false, false).
func relayLAN(ctx context.Context, w http.ResponseWriter, opts ProxyOptions, band string, body []byte, onServed func(float64)) (served, refused bool) {
	if opts.Freq != "" {
		// a private band is broker-resolved: it is not a LAN source, and nothing is resolved
		openAIError(w, http.StatusServiceUnavailable, "lan_error", "lan_only",
			"private bands are on the network and LAN mode is on: tune a band this LAN serves, or run without --lan to use the private band")
		return false, true
	}
	// A routing variant (:free, :nitro, :floor) names how a turn is routed, never which band: on
	// the LAN every turn is free and the measured-fastest member is tried first anyway, so the
	// band is looked up bare and the source is sent the model it serves.
	band = lanBand(band)
	if out, _, ok := rewriteModel(body, band); ok {
		body = out
	}
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
	if len(tried) > 0 {
		noteLANLeft(w, tried)
	}
	if ok, peerTried := tryEdgePeers(ctx, w, opts, band, body, onServed); ok {
		return true, false
	} else {
		tried = append(tried, peerTried...)
	}
	if opts.LANTowers != nil && !opts.LANEdgeOnly {
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
			noteLANLeft(w, tried)
			if resp.Header.Get("X-Roger-Local") == "1" {
				w.Header().Set("X-RogerAI-Cost", "0") // a LAN turn is free and says so
				if opts.OnReceipt != nil {
					opts.OnReceipt(localTowerReceipt(entry, band, true))
				}
			} else {
				// Something at a configured address answered, but not as a standalone Tower does:
				// it is not trusted as free, and no later turn goes to it until the owner re-adds it.
				if opts.Alert != nil {
					opts.Alert("this LAN source did not say it is local: " + entry + " answered without X-Roger-Local: 1; it gets no more turns until you re-add it (roger lan add " + entry + ")")
				}
				if opts.LANNotLocal != nil {
					opts.LANNotLocal(entry)
				}
			}
			copyRelayResponse(w, resp, !opts.ReasoningFallbackOff, nil)
			resp.Body.Close()
			return true, false
		}
	}
	msg := lanNothingServes(band, opts.LANEdgeOnly)
	if len(tried) > 0 {
		msg = "every local source serving " + band + " failed (" + strings.Join(tried, "; ") + "); LAN mode is on and the market was not tried"
		if opts.LANEdgeOnly {
			msg += "; edge-only is on, so LAN Towers are not used"
		}
	}
	if !opts.LANMode {
		// edge.prefer local-only without LAN mode: the same one relay, Edge-scoped words
		// (features/edge/mode.feature).
		msg = "local-only: no instance on this Edge serves " + band + " - serve it here, or set the preference back to local (roger edge prefer local); this roger will not go to the market"
		if len(tried) > 0 {
			msg = "local-only: every peer serving " + band + " failed (" + strings.Join(tried, "; ") + ") and this roger will not go to the market"
		}
	}
	openAIError(w, http.StatusServiceUnavailable, "lan_error", "lan_only", msg)
	return false, true
}

// lanBand is the band a requested model names: a routing variant suffix (:free, :nitro,
// :floor) changes how a turn is routed, never which band it is.
func lanBand(model string) string {
	for _, v := range []string{":free", ":nitro", ":floor"} {
		if strings.HasSuffix(model, v) {
			return strings.TrimSuffix(model, v)
		}
	}
	return model
}

// lanServes reports whether any local source serves band right now.
func lanServes(opts ProxyOptions, band string) bool {
	if opts.LANSelf != nil {
		if up, _ := opts.LANSelf(band); up != "" {
			return true
		}
	}
	if opts.EdgePeers != nil && len(opts.EdgePeers(band)) > 0 {
		return true
	}
	return opts.LANTowers != nil && !opts.LANEdgeOnly && len(opts.LANTowers(band)) > 0
}

// lanRefuseForeign refuses, on the LAN, a guest's turn naming a model that is not the tuned band
// and that nothing local serves: rewriting it onto the tuned band would answer a question the
// caller did not ask, and the market is not a rung in LAN mode.
func lanRefuseForeign(w http.ResponseWriter, opts ProxyOptions, body []byte) bool {
	if !opts.LANMode || opts.Model == "" {
		return false
	}
	var p struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &p) != nil || p.Model == "" {
		return false
	}
	asked := lanBand(p.Model)
	if asked == lanBand(opts.Model) || lanServes(opts, asked) {
		return false
	}
	openAIError(w, http.StatusServiceUnavailable, "lan_error", "lan_only", lanNothingServes(asked, opts.LANEdgeOnly))
	return true
}

// lanNothingServes is the one refusal for a band nothing local serves: what LAN mode tried, how to
// get it here, and that the market was not tried. Under edge-only it says LAN Towers are not used.
func lanNothingServes(band string, edgeOnly bool) string {
	if edgeOnly {
		return "nothing on this LAN serves " + band + ": edge-only is on, so LAN Towers are not used, and the market was not tried - serve it here (roger model serve) or add an Edge member (roger edge adopt); drop --edge-only to use LAN Towers"
	}
	return "nothing on this LAN serves " + band + ": LAN mode is on and the market was not tried - serve it here (roger model serve), add an Edge member, or add a Tower (roger lan add); turning LAN mode off would use the market"
}

// noteLANLeft names, on the answer, the local sources a turn failed over from, so the caller can
// see that it left them (features/lan/routing_and_inference.feature, failover inside the LAN).
func noteLANLeft(w http.ResponseWriter, tried []string) {
	if len(tried) == 0 {
		return
	}
	names := make([]string, 0, len(tried))
	for _, t := range tried {
		name, _, _ := strings.Cut(t, ":")
		name, _, _ = strings.Cut(name, "/")
		names = append(names, strings.TrimSpace(name))
	}
	w.Header().Set("X-RogerAI-LAN-Left", strings.Join(names, ", "))
}

// lanPace is how long each Edge member took to answer a turn from this machine, keyed by its
// pinned face. It only orders members within the LAN ladder's Edge rung, so it is a per-process
// hint, never a record.
var lanPace sync.Map // EdgePeer.Addr+"|"+EdgePeer.Pin -> time.Duration

func lanPaceKey(p EdgePeer) string { return p.Addr + "|" + p.Pin }

// lanOrderPeers puts members not yet measured first (so each gets measured), then the measured
// ones fastest first; ties keep the fleet's order.
func lanOrderPeers(peers []EdgePeer) []EdgePeer {
	out := append([]EdgePeer(nil), peers...)
	pace := func(p EdgePeer) (time.Duration, bool) {
		v, ok := lanPace.Load(lanPaceKey(p))
		if !ok {
			return 0, false
		}
		return v.(time.Duration), true
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, mi := pace(out[i])
		dj, mj := pace(out[j])
		if mi != mj {
			return !mi
		}
		return di < dj
	})
	return out
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
		return errors.New(lanNothingServes(model, opt.LANEdgeOnly))
	}
	label := opt.Pinned
	if label == "" {
		labels := make([]string, 0, len(sources))
		for _, s := range sources {
			labels = append(labels, s.Label())
		}
		label = strings.Join(labels, ", ")
	}
	// One bind: the endpoint printed below is the socket already held (netbind), never a port
	// probed now and bound later, which another process could take in between.
	ln, lerr := useListen(opt.Port)
	if lerr != nil {
		return lerr
	}
	addr := ln.Addr().String()
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
		LANMode: opt.LANMode, LANEdgeOnly: opt.LANEdgeOnly, LANSelf: opt.LANSelf, LANTowers: opt.LANTowers, LANHTTP: opt.LANHTTP, LANResolve: opt.LANResolve}
	return useServe(ln, newProxyHandler(opts))
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
