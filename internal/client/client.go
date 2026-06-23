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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AlertFunc receives a human-readable line when the proxy can't recover (no
// alternative provider fits the criteria). The TUI wires this to its status line;
// the CLI logs it to stderr. nil = no surfacing.
type AlertFunc func(string)

// getJSON issues GET broker+path (optionally as `user`) and decodes the JSON body
// into out. It centralizes the request/decode boilerplate the consumer commands
// share; a decode error on a 2xx body is ignored (the caller validates fields).
func getJSON(broker, path, user string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, broker+path, nil)
	if user != "" {
		req.Header.Set("X-Roger-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
			Ctx          int     `json:"ctx"`
			Online       bool    `json:"online"`
			Confidential bool    `json:"confidential"`
			FreeNow      bool    `json:"free_now"`
			TPS          float64 `json:"tps"`
		} `json:"offers"`
	}
	if err := getJSON(broker, "/discover", "", &d); err != nil {
		return err
	}
	if len(d.Offers) == 0 {
		fmt.Println("no offers yet - run `rogerai share` on a box with a local model")
		return nil
	}
	fmt.Printf("%-12s %-22s %-9s %-9s %-7s %-7s %-7s %-8s %s\n", "NODE", "MODEL", "$/1M in", "$/1M out", "TOK/S", "CTX", "REGION", "STATUS", "FLAGS")
	for _, o := range d.Offers {
		status := "online"
		if !o.Online {
			status = "offline"
		}
		tps := "-"
		if o.TPS > 0 {
			tps = fmt.Sprintf("%.0f", o.TPS)
		}
		flags := ""
		if o.Confidential {
			flags += "◆confidential "
		}
		if o.FreeNow {
			flags += "FREE-now"
		}
		fmt.Printf("%-12s %-22s %-9.2f %-9.2f %-7s %-7d %-7s %-8s %s\n", o.NodeID, o.Model, o.PriceIn, o.PriceOut, tps, o.Ctx, o.Region, status, flags)
	}
	return nil
}

// Balance prints the caller's wallet credits (GET /balance as `user`).
func Balance(broker, user string) error {
	var b struct {
		User    string  `json:"user"`
		Balance float64 `json:"balance"`
	}
	if err := getJSON(broker, "/balance", user, &b); err != nil {
		return err
	}
	fmt.Printf("%s: %.6f credits\n", b.User, b.Balance)
	return nil
}

// Topup asks the broker for a Stripe Checkout URL to buy `usd` of credits.
func Topup(broker, user string, usd float64) error {
	body, _ := json.Marshal(map[string]float64{"usd": usd})
	req, _ := http.NewRequest(http.MethodPost, broker+"/billing/checkout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	fmt.Printf("Buy %.0f credits - open this to pay:\n  %s\n", d.Credits, d.URL)
	return nil
}

// ProxyOptions configures the local relay handler.
type ProxyOptions struct {
	Broker, User string
	Confidential bool
	MinTPS       float64   // X-Roger-Min-TPS floor (0 = none)
	MaxPriceIn   float64   // X-Roger-Max-Price cap on input price (0 = none)
	MaxPriceOut  float64   // X-Roger-Max-Price-Out cap on output price (0 = none)
	Alert        AlertFunc // surfaced when failover is exhausted (nil = silent)
}

// ProxyHandler returns the local OpenAI-compatible handler that relays to the
// broker with transparent provider failover (used by `rogerai use` and by the
// TUI's "tune in"). Bots see one stable endpoint; under the hood a dropped
// provider is routed around automatically.
func ProxyHandler(opts ProxyOptions) http.Handler {
	httpClient := &http.Client{Timeout: 120 * time.Second}
	policy := defaultPolicy()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		// Per-request criteria, derived from how the user opened this endpoint.
		var model struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &model)
		crit := Criteria{Model: model.Model, Confidential: opts.Confidential, MinTPS: opts.MinTPS, MaxPriceIn: opts.MaxPriceIn, MaxPriceOut: opts.MaxPriceOut}
		relayWithFailover(w, opts, crit, body, httpClient, policy)
	})
	return mux
}

// relayWithFailover runs the bounded retry/failover loop for one client request.
// It first lets the broker pick (cheapest match); on a retryable failure it
// re-queries /discover, picks an alternative that still meets the criteria,
// pins it, and retries with backoff - excluding every provider that already
// failed. On total exhaustion it returns a clear 502 and fires opts.Alert.
func relayWithFailover(w http.ResponseWriter, opts ProxyOptions, crit Criteria, body []byte, httpClient *http.Client, policy failoverPolicy) {
	failed := map[string]bool{}
	pin := "" // "" = let the broker choose; otherwise a failover-selected node
	var lastErr error
	var lastStatus int

	for attempt := 0; attempt < policy.maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(policy.backoff(attempt))
		}
		req, _ := http.NewRequest(http.MethodPost, opts.Broker+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
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
		if opts.MaxPriceOut > 0 {
			req.Header.Set("X-Roger-Max-Price-Out", fmt.Sprintf("%g", opts.MaxPriceOut))
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
	http.Error(w, msg, http.StatusBadGateway)
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
	for _, h := range []string{"X-RogerAI-Provider", "X-RogerAI-Cost", "X-RogerAI-Balance", "X-RogerAI-Receipt", "X-RogerAI-Price", "X-RogerAI-TPS"} {
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

// UseOptions are the resolved spend limits + flags for `rogerai use`.
type UseOptions struct {
	Port         int
	Confidential bool
	MaxIn        float64 // cap on $/1M input price (0 = none)
	MaxOut       float64 // cap on $/1M output price (0 = none); the headline cap
	MinTPS       float64 // throughput floor (0 = none)
	TypicalOut   int     // output tokens for the est-cost line (default 800)
	Yes          bool    // skip the (y/N) confirm (scripts / Hermes / bots)
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
	in := os.Stdin

	for {
		br, ok := BandRangeFor(broker, model)
		if !ok {
			fmt.Printf("no station on air for %q right now - try `rogerai search` or come back.\n", model)
			return nil
		}
		// Is the cheapest station within the out-price cap?
		if maxOut > 0 && br.Min > maxOut {
			gap := br.Min - maxOut
			pct := gap / maxOut * 100
			fmt.Printf("\n  the band is above your limit  %s\n", model)
			fmt.Printf("    cheapest on air   %.2f $/1M out   @%s   %s\n", br.Min, br.CheapNode, tpsLabel(br.CheapTPS))
			fmt.Printf("    your max          %.2f $/1M out\n", maxOut)
			fmt.Printf("    gap               +%.2f  (%.0f%% over)   you would pay %.6f cr / reply\n", gap, pct, estReplyCost(br.Min, typical))
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
			fmt.Printf("    your max       %.2f $/1M out   (within limit)\n", maxOut)
		}
		fmt.Printf("    est. cost      ~ %.6f cr / typical reply  (~%d out tokens)\n", estReplyCost(br.Min, typical), typical)
		if bal := balanceOf(broker, user); bal >= 0 {
			per100 := estReplyCost(br.Min, typical) * 100
			fmt.Printf("                   ~ %.6f cr / 100 replies        balance %.4f cr\n", per100, bal)
		}
		fmt.Printf("    locked         each reply price-locks at send; a hold pre-auths your session\n")

		if !opt.Yes {
			fmt.Printf("\n  open the channel? (y/N) ")
			line, _ := readLine(in)
			if !isYes(line) {
				fmt.Println("  denied - no channel opened.")
				return nil
			}
		}
		break
	}

	addr := fmt.Sprintf("127.0.0.1:%d", opt.Port)
	fmt.Printf("\nRogerAI endpoint: http://%s/v1   model=%s  user=%s  broker=%s\n", addr, model, user, broker)
	if opt.MaxIn > 0 || maxOut > 0 || opt.MinTPS > 0 {
		fmt.Printf("  limits: max-in=%g  max-out=%g $/1M   min-tps=%g t/s   (only tunes to stations within these)\n", opt.MaxIn, maxOut, opt.MinTPS)
	}
	fmt.Printf("  OPENAI_API_BASE=http://%s/v1  OPENAI_API_KEY=roger-local   (Ctrl-C to stop)\n", addr)
	opts := ProxyOptions{Broker: broker, User: user, Confidential: opt.Confidential, MaxPriceIn: opt.MaxIn, MaxPriceOut: maxOut, MinTPS: opt.MinTPS, Alert: func(s string) {
		fmt.Fprintln(os.Stderr, "rogerai: "+s)
	}}
	return http.ListenAndServe(addr, ProxyHandler(opts))
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

// Chat sends one message through the broker and returns the reply + a status
// line (provider · cost) - used by the TUI's in-CLI test chat.
func Chat(broker, user, model, prompt string, confidential bool) (reply, status string, err error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": 256,
	})
	req, _ := http.NewRequest(http.MethodPost, broker+"/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Roger-User", user)
	if confidential {
		req.Header.Set("X-Roger-Confidential", "1")
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var d struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&d)
	if len(d.Choices) == 0 {
		if d.Error.Message != "" {
			return "", "", fmt.Errorf("%s", d.Error.Message)
		}
		return "", "", fmt.Errorf("no response (status %d)", resp.StatusCode)
	}
	reply = d.Choices[0].Message.Content
	if reply == "" {
		reply = d.Choices[0].Message.Reasoning
	}
	status = fmt.Sprintf("%s · %s cr", resp.Header.Get("X-RogerAI-Provider"), resp.Header.Get("X-RogerAI-Cost"))
	return reply, status, nil
}
