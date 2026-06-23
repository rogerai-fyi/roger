package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// billing is the Stripe wallet top-up (prepaid credits). SDK-free: raw Stripe API
// for Checkout + stdlib HMAC for webhook verification. Inert until STRIPE_SECRET_KEY
// is set. 1 credit = $1 by default (creditUSD). Payouts (Connect) are a follow-up.
type billing struct {
	secretKey     string
	webhookSecret string
	successURL    string
	cancelURL     string
	creditUSD     float64 // USD per credit
}

func loadBilling() billing {
	cu := 1.0
	if v := os.Getenv("ROGERAI_CREDIT_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cu = f
		}
	}
	b := billing{
		secretKey:     os.Getenv("STRIPE_SECRET_KEY"),
		webhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		successURL:    envOr("STRIPE_SUCCESS_URL", "https://rogerai.fyi/topup/success"),
		cancelURL:     envOr("STRIPE_CANCEL_URL", "https://rogerai.fyi/topup/cancel"),
		creditUSD:     cu,
	}
	if b.secretKey == "" {
		log.Printf("billing: disabled (set STRIPE_SECRET_KEY to enable wallet top-ups)")
	} else {
		log.Printf("billing: Stripe enabled (1 credit = $%.2f)", b.creditUSD)
	}
	return b
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// checkout handles POST /billing/checkout {"usd": 10}: creates a Stripe Checkout
// session for the caller to buy credits and returns the {url, credits}.
func (b *broker) checkout(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if b.bill.secretKey == "" {
		jsonErr(w, http.StatusServiceUnavailable, "billing not configured")
		return
	}
	user := userOf(r)
	var req struct {
		USD float64 `json:"usd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.USD < 1 {
		req.USD = 10
	}
	credits := req.USD / b.bill.creditUSD

	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", b.bill.successURL)
	form.Set("cancel_url", b.bill.cancelURL)
	form.Set("client_reference_id", user)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", "usd")
	form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(int(req.USD*100)))
	form.Set("line_items[0][price_data][product_data][name]", "RogerAI credits")
	form.Set("metadata[user]", user)
	form.Set("metadata[credits]", strconv.FormatFloat(credits, 'f', 4, 64))

	sreq, _ := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	sreq.Header.Set("Authorization", "Bearer "+b.bill.secretKey)
	sreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "stripe unreachable")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Printf("stripe checkout error %d: %s", resp.StatusCode, body)
		jsonErr(w, http.StatusBadGateway, "stripe error")
		return
	}
	var sess struct {
		URL string `json:"url"`
		ID  string `json:"id"`
	}
	_ = json.Unmarshal(body, &sess)
	writeJSON(w, http.StatusOK, map[string]any{"url": sess.URL, "usd": req.USD, "credits": credits})
}

// webhook handles POST /billing/webhook: Stripe's payment callback. The signature
// is HMAC-verified and crediting is idempotent (each session credited once).
func (b *broker) webhook(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	if b.bill.secretKey == "" {
		jsonErr(w, http.StatusServiceUnavailable, "billing not configured")
		return
	}
	payload, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !verifyStripeSig(r.Header.Get("Stripe-Signature"), payload, b.bill.webhookSecret) {
		jsonErr(w, http.StatusBadRequest, "bad signature")
		return
	}
	var evt struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID                string `json:"id"`
				ClientReferenceID string `json:"client_reference_id"`
				AmountTotal       int    `json:"amount_total"`
				Metadata          struct {
					User    string `json:"user"`
					Credits string `json:"credits"`
				} `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	_ = json.Unmarshal(payload, &evt)
	if evt.Type == "checkout.session.completed" {
		o := evt.Data.Object
		user := o.Metadata.User
		if user == "" {
			user = o.ClientReferenceID
		}
		credits, _ := strconv.ParseFloat(o.Metadata.Credits, 64)
		if credits == 0 {
			credits = float64(o.AmountTotal) / 100 / b.bill.creditUSD
		}
		if user != "" && credits > 0 {
			// Atomic credit-once: dedups (Stripe redelivers at-least-once) AND can't
			// lose the credit (mark + add happen in one transaction).
			credited, newBal, err := b.db.CreditOnce("stripe:"+o.ID, user, credits)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, "store error")
				return
			}
			if credited {
				log.Printf("stripe: credited %s +%.4f -> %.4f (session %s)", user, credits, newBal, o.ID)
			} else {
				log.Printf("stripe: duplicate session %s ignored", o.ID)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

// verifyStripeSig validates the Stripe-Signature header (t=…,v1=…) via HMAC-SHA256.
func verifyStripeSig(header string, payload []byte, secret string) bool {
	if secret == "" {
		return false
	}
	var ts, v1 string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			v1 = kv[1]
		}
	}
	if ts == "" || v1 == "" {
		return false
	}
	// Reject stale signatures to prevent replay (Stripe default tolerance: 5 min).
	tsi, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if d := time.Now().Unix() - tsi; d > 300 || d < -300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s.%s", ts, payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(v1))
}
