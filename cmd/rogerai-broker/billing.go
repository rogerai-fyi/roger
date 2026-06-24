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
		secretKey:     stripeSecretKey(),
		webhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		successURL:    envOr("STRIPE_SUCCESS_URL", "https://rogerai.fyi/topup/success"),
		cancelURL:     envOr("STRIPE_CANCEL_URL", "https://rogerai.fyi/topup/cancel"),
		creditUSD:     cu,
	}
	if requireLive() && !strings.HasPrefix(b.secretKey, "sk_live") {
		log.Printf("billing: ROGERAI_REQUIRE_LIVE set but STRIPE_SECRET_KEY is not an sk_live key - billing DISABLED (refusing test mode in production)")
		b.secretKey, b.webhookSecret = "", ""
	}
	if b.secretKey == "" {
		log.Printf("billing: disabled (set STRIPE_SECRET_KEY)")
	} else {
		mode := "test"
		if strings.HasPrefix(b.secretKey, "sk_live") {
			mode = "LIVE"
		}
		log.Printf("billing: Stripe enabled [%s mode] (1 credit = $%.2f)", mode, b.creditUSD)
	}
	return b
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// stripeLive reports whether the broker is EXPLICITLY in production mode. Going live
// REQUIRES this flag - it is never inferred from the presence of a prod key, so a
// stray or blanked prod var can never silently flip modes.
// requireLive (ROGERAI_REQUIRE_LIVE=1 on the live broker) makes billing FAIL CLOSED
// unless STRIPE_SECRET_KEY is a real sk_live key - so a misconfigured/test key in
// production disables billing instead of silently accepting fake test cards. Off
// locally so dev test keys work. The broker holds live values in STRIPE_SECRET_KEY /
// STRIPE_WEBHOOK_SECRET; the local .env holds test values under the same names.
func requireLive() bool {
	switch strings.ToLower(os.Getenv("ROGERAI_REQUIRE_LIVE")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func stripeSecretKey() string { return os.Getenv("STRIPE_SECRET_KEY") }

// checkout handles POST /billing/checkout {"usd": 10}: creates a Stripe Checkout
// session for the caller to buy credits and returns the {url, credits}.
func (b *broker) checkout(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	if b.bill.secretKey == "" {
		jsonErr(w, http.StatusServiceUnavailable, "billing not configured")
		return
	}
	// Top-up may be anonymous (design: anon top-up is OK, claimable on login), so we
	// do not require `authed` here. identityOf still rejects an unsigned request that
	// impersonates the reserved pubkey-derived id space, so a legacy header can never
	// add credits to (or otherwise touch) a signed user's wallet.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	user, ok := b.checkoutWallet(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	var req struct {
		USD float64 `json:"usd"`
	}
	_ = json.Unmarshal(body, &req)
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
	form.Set("line_items[0][price_data][product_data][name]", "RogerAI wallet top-up")
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
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Printf("stripe checkout error %d: %s", resp.StatusCode, respBody)
		jsonErr(w, http.StatusBadGateway, "stripe error")
		return
	}
	var sess struct {
		URL string `json:"url"`
		ID  string `json:"id"`
	}
	_ = json.Unmarshal(respBody, &sess)
	writeJSON(w, http.StatusOK, map[string]any{"url": sess.URL, "usd": req.USD, "credits": credits})
}

// checkoutWallet resolves which wallet a top-up must credit, so the payment lands
// where the dashboard reads. A logged-in web session credits its SESSION wallet (the
// same "u_gh_<githubID>" /me shows); a signed keypair credits walletOf (the github
// wallet after login, else its own anon pubkey wallet - anon top-up is allowed and
// claimable on login); an unsigned/unauthenticated request resolves nothing.
func (b *broker) checkoutWallet(r *http.Request, body []byte) (string, bool) {
	if _, sw, sok := b.webSession(r); sok {
		return sw, true
	}
	if u, _, iok := b.identityOf(r, body); iok {
		return b.walletOf(r, u), true
	}
	return "", false
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
				Amount            int    `json:"amount"`         // dispute objects carry `amount`
				PaymentIntent     string `json:"payment_intent"` // session + dispute carry this
				Charge            string `json:"charge"`         // dispute carries the charge id
				Metadata          struct {
					User      string `json:"user"`
					Credits   string `json:"credits"`
					RequestID string `json:"request_id"`
				} `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	_ = json.Unmarshal(payload, &evt)
	// Platform-liable dispute (ACCOUNT-PAYOUTS-DESIGN section 6.4): a consumer
	// chargeback against a funding charge -> chargeback ledger row + clawback of any
	// still-held/payable operator earnings derived from that consumer. A dispute object
	// carries NONE of the checkout metadata (no metadata.user / request_id), only a
	// payment_intent + charge id, so we resolve the wallet via the mapping persisted at
	// checkout.session.completed time. The clawback is then attributed by wallet+recency
	// (no request id is available) up to the disputed amount.
	if evt.Type == "charge.dispute.created" {
		o := evt.Data.Object
		amount := float64(o.Amount) / 100 / b.bill.creditUSD
		// Resolve the consumer wallet from the stored charge mapping (payment_intent or
		// charge id). Fall back to any metadata/client_reference_id only if the mapping
		// is missing (e.g. a charge created before this mapping shipped).
		user, _, ok, err := b.db.WalletByCharge(o.PaymentIntent)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		if !ok {
			if user, _, ok, err = b.db.WalletByCharge(o.Charge); err != nil {
				jsonErr(w, http.StatusInternalServerError, "store error")
				return
			}
		}
		if !ok {
			user = o.Metadata.User
			if user == "" {
				user = o.ClientReferenceID
			}
			if user != "" {
				log.Printf("stripe: dispute %s has no stored charge mapping (pi=%s ch=%s), falling back to metadata wallet %s", o.ID, o.PaymentIntent, o.Charge, user)
			}
		}
		if user != "" && amount > 0 {
			// requestID is empty for a real dispute: Chargeback claws lots by wallet+recency.
			clawed, err := b.db.Chargeback(o.ID, user, o.Metadata.RequestID, amount, time.Now())
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, "store error")
				return
			}
			log.Printf("stripe: dispute %s on %s -%.4f credits (clawed %.4f from operators)", o.ID, user, amount, clawed)
		} else {
			log.Printf("stripe: dispute %s could not resolve a wallet (pi=%s ch=%s amount=%.4f) - no clawback", o.ID, o.PaymentIntent, o.Charge, amount)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"received": true})
		return
	}
	if evt.Type == "checkout.session.completed" {
		o := evt.Data.Object
		user := o.Metadata.User
		if user == "" {
			user = o.ClientReferenceID
		}
		// Credits derive from the REAL money charged (amount_total), never from the
		// caller-supplied metadata - metadata is advisory only (log if it diverges so a
		// tampering attempt is visible). creditUSD converts dollars-charged to credits.
		credits := float64(o.AmountTotal) / 100 / b.bill.creditUSD
		if mc, mErr := strconv.ParseFloat(o.Metadata.Credits, 64); mErr == nil && mc != 0 {
			if d := mc - credits; d > 1e-6 || d < -1e-6 {
				log.Printf("stripe: session %s metadata credits %.4f diverge from amount_total-derived %.4f - using amount_total", o.ID, mc, credits)
			}
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
			// Persist the charge mapping so a later charge.dispute.created (which carries
			// none of this metadata) can resolve this wallet. Idempotent on session id.
			if err := b.db.LinkCharge(o.ID, o.PaymentIntent, o.Charge, user, credits); err != nil {
				log.Printf("stripe: LinkCharge(session %s) failed: %v (dispute clawback may not resolve this charge)", o.ID, err)
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
