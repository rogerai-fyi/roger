// Package client is the consumer side: discover models, check balance, and open
// a local OpenAI-compatible endpoint that relays through the broker.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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

// ProxyHandler returns the local OpenAI-compatible handler that relays to the
// broker (used by `rogerai use` and by the TUI's "tune in").
func ProxyHandler(broker, user string, confidential bool) http.Handler {
	httpClient := &http.Client{Timeout: 120 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		req, _ := http.NewRequest(http.MethodPost, broker+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Roger-User", user)
		if confidential {
			req.Header.Set("X-Roger-Confidential", "1")
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		for _, h := range []string{"X-RogerAI-Provider", "X-RogerAI-Cost", "X-RogerAI-Balance"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(rb)
	})
	return mux
}

// Use opens a local OpenAI-compatible endpoint that relays to the broker.
func Use(broker, user, model string, port int, confidential bool) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Printf("RogerAI endpoint: http://%s/v1   model=%s  user=%s  broker=%s\n", addr, model, user, broker)
	fmt.Printf("  OPENAI_API_BASE=http://%s/v1  OPENAI_API_KEY=roger-local   (Ctrl-C to stop)\n", addr)
	return http.ListenAndServe(addr, ProxyHandler(broker, user, confidential))
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
