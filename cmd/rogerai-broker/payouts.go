package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// errStripeTransfer is the sentinel for a rejected/empty Stripe Transfer response.
var errStripeTransfer = errors.New("stripe transfer rejected")

// This file is the operator money-out rail (ACCOUNT-PAYOUTS-DESIGN section 6):
// Stripe Connect Express onboarding + KYC gate, payout request (>= minimum, payable
// only, KYC-required), payout history, and the dispute -> clawback webhook path.
//
// Stripe Connect is GATED behind STRIPE_SECRET_KEY (like the moderation screen): with
// a key it talks to the real (test/live) API; without one it STUBS gracefully with a
// loud log so the whole flow is exercisable in dev without money moving.

// connect holds the Connect config + payout policy. SDK-free (raw Stripe API).
type connect struct {
	secretKey  string
	refreshURL string
	returnURL  string
	policy     store.PayoutPolicy
	// transfer creates a Stripe Transfer of amountCents to destination, idempotent on
	// idemKey, returning the transfer id. nil = the real Stripe API call. Injectable so
	// the payout flow is testable without real money / network.
	transfer func(destination string, amountCents int64, idemKey string) (string, error)
}

func loadConnect() connect {
	c := connect{
		secretKey:  stripeSecretKey(), // Connect reuses the platform secret key (prod-aware)
		refreshURL: envOr("STRIPE_CONNECT_REFRESH_URL", "https://rogerai.fyi/payouts?onboard=refresh"),
		returnURL:  envOr("STRIPE_CONNECT_RETURN_URL", "https://rogerai.fyi/payouts?onboard=done"),
		policy:     store.LoadPayoutPolicy(),
	}
	if c.secretKey == "" {
		log.Printf("CONNECT: Stripe payouts DISABLED (no STRIPE_SECRET_KEY). Onboarding + transfers are STUBBED - safe in dev, NOT a real money rail. Set STRIPE_SECRET_KEY before launch.")
	} else {
		log.Printf("CONNECT: Stripe Connect enabled (hold=%dd reserve=%.0f%% min=%.0f schedule=%s)",
			c.policy.HoldDays, c.policy.Reserve*100, c.policy.MinPayout, c.policy.Schedule)
	}
	return c
}

// stripeForm POSTs an application/x-www-form-urlencoded request to the Stripe API
// and decodes the JSON response into out. Returns the HTTP status.
func (c connect) stripeForm(method, path string, form url.Values, out any) (int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, "https://api.stripe.com"+path, body)
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Printf("stripe %s %s -> %d: %s", method, path, resp.StatusCode, rb)
	}
	if out != nil {
		_ = json.Unmarshal(rb, out)
	}
	return resp.StatusCode, nil
}

// connectOnboard handles POST /connect/onboard: creates (or reuses) the operator's
// Express connected account and returns a Stripe Account Link to complete KYC. In
// dev (no key) it returns a stub link + marks the account onboarding.
func (b *broker) connectOnboard(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	login, _, _, ok := b.sessionOwner(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	o, found, _ := b.db.OwnerByLogin(login)
	if !found {
		jsonErr(w, http.StatusForbidden, "no operator account for this login (run `rogerai login` on a node first)")
		return
	}

	if b.conn.secretKey == "" {
		// Dev stub: pretend onboarding started so the UI flow is testable end-to-end.
		_ = b.db.SetConnect(login, "acct_dev_stub", "onboarding")
		log.Printf("CONNECT(STUB): onboard %s -> acct_dev_stub (no STRIPE_SECRET_KEY)", login)
		writeJSON(w, http.StatusOK, map[string]any{
			"stub":   true,
			"url":    b.conn.returnURL,
			"status": "onboarding",
		})
		return
	}

	acctID := o.ConnectID
	if acctID == "" {
		var acct struct {
			ID string `json:"id"`
		}
		form := url.Values{}
		form.Set("type", "express")
		form.Set("capabilities[transfers][requested]", "true")
		if o.Email != "" {
			form.Set("email", o.Email)
		}
		if code, err := b.conn.stripeForm(http.MethodPost, "/v1/accounts", form, &acct); err != nil || acct.ID == "" {
			jsonErr(w, http.StatusBadGateway, "could not create connected account")
			_ = code
			return
		}
		acctID = acct.ID
		_ = b.db.SetConnect(login, acctID, "onboarding")
	}

	var link struct {
		URL string `json:"url"`
	}
	lf := url.Values{}
	lf.Set("account", acctID)
	lf.Set("refresh_url", b.conn.refreshURL)
	lf.Set("return_url", b.conn.returnURL)
	lf.Set("type", "account_onboarding")
	if _, err := b.conn.stripeForm(http.MethodPost, "/v1/account_links", lf, &link); err != nil || link.URL == "" {
		jsonErr(w, http.StatusBadGateway, "could not create onboarding link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": link.URL, "status": "onboarding"})
}

// connectStatus handles GET /connect/status: reports the operator's Connect
// capability (none|onboarding|active|restricted). With a key it refreshes from
// Stripe (transfers capability == active); in dev it returns the stored status.
func (b *broker) connectStatus(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	login, _, _, ok := b.sessionOwner(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	o, found, _ := b.db.OwnerByLogin(login)
	if !found {
		jsonErr(w, http.StatusForbidden, "no operator account for this login")
		return
	}
	status := o.ConnectStatus
	if status == "" {
		status = "none"
	}
	if b.conn.secretKey != "" && o.ConnectID != "" && o.ConnectID != "acct_dev_stub" {
		if live := b.refreshConnectStatus(login, o.ConnectID); live != "" {
			status = live
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"can_payout": status == "active",
		"connect_id": o.ConnectID,
		"min_payout": b.conn.policy.MinPayout,
		"schedule":   b.conn.policy.Schedule,
	})
}

// refreshConnectStatus reads the connected account and maps the transfers capability
// to our status vocabulary, persisting it. Returns "" on transport error.
func (b *broker) refreshConnectStatus(login, acctID string) string {
	var acct struct {
		Capabilities struct {
			Transfers string `json:"transfers"`
		} `json:"capabilities"`
		Requirements struct {
			DisabledReason string `json:"disabled_reason"`
		} `json:"requirements"`
	}
	if _, err := b.conn.stripeForm(http.MethodGet, "/v1/accounts/"+acctID, nil, &acct); err != nil {
		return ""
	}
	status := "onboarding"
	switch {
	case acct.Capabilities.Transfers == "active":
		status = "active"
	case acct.Requirements.DisabledReason != "":
		status = "restricted"
	}
	_ = b.db.SetConnect(login, acctID, status)
	return status
}

// payoutsRequest handles POST /payouts/request: KYC-gated (transfers active),
// minimum-gated (>= policy minimum), payable-only payout. Promotes held->payable,
// debits payable lots, creates a Stripe Transfer (or a stub one in dev), and writes
// the payout + ledger rows in the store transaction.
func (b *broker) payoutsRequest(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	login, _, _, ok := b.sessionOwner(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	o, found, _ := b.db.OwnerByLogin(login)
	if !found {
		jsonErr(w, http.StatusForbidden, "no operator account for this login")
		return
	}

	// KYC gate: Connect transfers capability must be active before any money out.
	status := o.ConnectStatus
	if b.conn.secretKey != "" && o.ConnectID != "" && o.ConnectID != "acct_dev_stub" {
		if live := b.refreshConnectStatus(login, o.ConnectID); live != "" {
			status = live
		}
	}
	if status != "active" {
		jsonErr(w, http.StatusForbidden, "complete Stripe Connect onboarding (KYC) before requesting a payout")
		return
	}

	// Single-flight per account: serialize concurrent payout requests for the same
	// operator so two in-flight requests can never both debit the payable lots.
	unlock := b.lockPayout(o.Pubkey)
	defer unlock()

	// Pre-check the payable amount against the minimum before debiting, to return a
	// clean 400 (no transfer, no payout row) when below minimum.
	split, _ := b.db.EarningSplitOf(o.Pubkey, time.Now())
	if split.Payable < b.conn.policy.MinPayout {
		jsonErr(w, http.StatusBadRequest, "below minimum payout ($"+strconv.FormatFloat(b.conn.policy.MinPayout, 'f', -1, 64)+")")
		return
	}

	// Debit + record a PENDING payout in the store FIRST (atomic: marks the payable
	// lots paid and returns the EXACT debited amount). The transfer is created for that
	// returned amount, then the payout is settled or rolled back - so a transfer is
	// never issued for a different amount than was debited, nor without a payout row.
	pay, okp, reason, err := b.db.RequestPayout(o.Pubkey, time.Now(), b.conn.policy.MinPayout)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !okp {
		jsonErr(w, http.StatusBadRequest, reason)
		return
	}

	// Create the Stripe Transfer for EXACTLY the debited amount. Idempotency-Key is the
	// store payout id (stable per payout - a retry of the same payout never double-pays;
	// distinct payouts never collide).
	idemKey := "payout:" + strconv.FormatInt(pay.ID, 10)
	transferID, terr := b.payoutTransfer(o.ConnectID, login, pay.Amount, idemKey)
	if terr != nil {
		// Transfer failed AFTER the debit: roll the lots back to payable + mark the
		// payout failed, so no completed transfer is ever left with payable lots and no
		// orphan debit remains.
		if ferr := b.db.FailPayout(pay.ID); ferr != nil {
			log.Printf("payout %s: transfer failed AND rollback failed (payout %d): transfer=%v rollback=%v", login, pay.ID, terr, ferr)
		} else {
			log.Printf("payout %s: transfer failed, rolled back payout %d: %v", login, pay.ID, terr)
		}
		jsonErr(w, http.StatusBadGateway, "stripe transfer failed")
		return
	}

	if err := b.db.SettlePayout(pay.ID, transferID); err != nil {
		// The money MOVED but we couldn't flip the record to paid. Do NOT roll back the
		// lots (that would imply the transfer didn't happen). Surface a 500 with the
		// transfer id so the operator state is reconcilable.
		log.Printf("payout %s: transfer %s succeeded but SettlePayout(%d) failed: %v", login, transferID, pay.ID, err)
		jsonErr(w, http.StatusInternalServerError, "transfer completed but payout record update failed; contact support with transfer "+transferID)
		return
	}
	pay.State = store.PayoutPaid
	pay.StripeTransferID = transferID
	log.Printf("payout %s: %.4f credits -> transfer %s (state=%s)", login, pay.Amount, transferID, pay.State)
	writeJSON(w, http.StatusOK, map[string]any{"payout": pay})
}

// lockPayout acquires the per-account single-flight lock, returning the unlock func.
func (b *broker) lockPayout(accountID string) func() {
	mu, _ := b.payoutLocks.LoadOrStore(accountID, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// payoutTransfer moves `amount` credits to the operator's connected account,
// idempotent on idemKey, returning the Stripe transfer id. It uses the injectable
// conn.transfer when set (tests), then a dev stub when Stripe is unconfigured, else
// the real Stripe Transfers API.
func (b *broker) payoutTransfer(connectID, login string, amount float64, idemKey string) (string, error) {
	// 1 credit == creditUSD dollars; Stripe wants the smallest unit (cents).
	cents := int64(amount*b.bill.creditUSD*100 + 0.5)
	if b.conn.transfer != nil {
		return b.conn.transfer(connectID, cents, idemKey)
	}
	if b.conn.secretKey == "" || connectID == "" || connectID == "acct_dev_stub" {
		id := "tr_dev_stub_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		log.Printf("CONNECT(STUB): transfer %.4f credits to %s -> %s (no real money moved)", amount, login, id)
		return id, nil
	}
	var tr struct {
		ID string `json:"id"`
	}
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(cents, 10))
	form.Set("currency", "usd")
	form.Set("destination", connectID)
	req, _ := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/transfers", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+b.conn.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("stripe transfer error %d: %s", resp.StatusCode, rb)
		return "", errStripeTransfer
	}
	_ = json.Unmarshal(rb, &tr)
	if tr.ID == "" {
		return "", errStripeTransfer
	}
	return tr.ID, nil
}

// payoutsHistory handles GET /payouts/history: the operator's payout + clawback log.
func (b *broker) payoutsHistory(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	login, _, _, ok := b.sessionOwner(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in")
		return
	}
	o, found, _ := b.db.OwnerByLogin(login)
	if !found {
		jsonErr(w, http.StatusForbidden, "no operator account for this login")
		return
	}
	pays, _ := b.db.PayoutsOf(o.Pubkey, recentLimit(r))
	if pays == nil {
		pays = []store.Payout{}
	}
	led, _ := b.db.LedgerOf(o.Pubkey, []string{store.KindPayout, store.KindChargeback, store.KindAdjustment}, recentLimit(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"payouts": pays,
		"ledger":  nonNilLedger(led),
	})
}
