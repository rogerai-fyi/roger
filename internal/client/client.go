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
	"strings"
	"time"
)

// AlertFunc receives a human-readable line when the proxy can't recover (no
// alternative provider fits the criteria). The TUI wires this to its status line;
// the CLI logs it to stderr. nil = no surfacing.
type AlertFunc func(string)

func Search(broker string) error {
	resp, err := http.Get(broker + "/discover")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
	json.NewDecoder(resp.Body).Decode(&d)
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

func Balance(broker, user string) error {
	req, _ := http.NewRequest(http.MethodGet, broker+"/balance", nil)
	req.Header.Set("X-Roger-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var b struct {
		User    string  `json:"user"`
		Balance float64 `json:"balance"`
	}
	json.NewDecoder(resp.Body).Decode(&b)
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
	MaxPrice     float64   // X-Roger-Max-Price cap (0 = none)
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
		crit := Criteria{Model: model.Model, Confidential: opts.Confidential, MinTPS: opts.MinTPS, MaxPrice: opts.MaxPrice}
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
		if opts.MaxPrice > 0 {
			req.Header.Set("X-Roger-Max-Price", fmt.Sprintf("%g", opts.MaxPrice))
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
	if crit.MaxPrice > 0 {
		constraints = append(constraints, fmt.Sprintf("max-price=%g", crit.MaxPrice))
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

// Use opens a local OpenAI-compatible endpoint that relays to the broker.
func Use(broker, user, model string, port int, confidential bool) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Printf("RogerAI endpoint: http://%s/v1   model=%s  user=%s  broker=%s\n", addr, model, user, broker)
	fmt.Printf("  OPENAI_API_BASE=http://%s/v1  OPENAI_API_KEY=roger-local   (Ctrl-C to stop)\n", addr)
	opts := ProxyOptions{Broker: broker, User: user, Confidential: confidential, Alert: func(s string) {
		fmt.Fprintln(os.Stderr, "rogerai: "+s)
	}}
	return http.ListenAndServe(addr, ProxyHandler(opts))
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
