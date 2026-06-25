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
	// reverseTransfer reverses amountCents of a prior Stripe Transfer (transferID),
	// idempotent on idemKey, returning the reversal id. Used on a post-payout dispute
	// (ACCOUNT-PAYOUTS-DESIGN 6.4 step 4) to pull an already-paid operator share back
	// from their connected account. nil = the real Stripe API call. Injectable for tests.
	reverseTransfer func(transferID string, amountCents int64, idemKey string) (string, error)
}

func loadConnect() connect {
	c := connect{
		secretKey:  stripeSecretKey(), // Connect reuses the platform secret key (prod-aware)
		refreshURL: envOr("STRIPE_CONNECT_REFRESH_URL", "https://rogerai.fyi/payouts?onboard=refresh"),
		returnURL:  envOr("STRIPE_CONNECT_RETURN_URL", "https://rogerai.fyi/payouts?onboard=done"),
		policy:     store.LoadPayoutPolicy(),
	}
	// Fail-closed in production, mirroring billing: if ROGERAI_REQUIRE_LIVE is set, the
	// payout rail REFUSES to run on anything but a real sk_live key. This blanks the key
	// so onboarding/transfers are disabled (never the dev stub, never a test-mode
	// transfer) rather than silently moving fake money in production. The
	// fail-closed transfer guard in payoutTransfer enforces the same at call time.
	if requireLive() && !strings.HasPrefix(c.secretKey, "sk_live") {
		log.Printf("CONNECT: ROGERAI_REQUIRE_LIVE set but STRIPE_SECRET_KEY is not an sk_live key - payouts DISABLED (refusing the dev stub / test mode in production)")
		c.secretKey = ""
	}
	if c.secretKey == "" {
		log.Printf("CONNECT: Stripe payouts DISABLED (no usable STRIPE_SECRET_KEY). Onboarding + transfers are STUBBED - safe in dev, NOT a real money rail. Set STRIPE_SECRET_KEY before launch.")
	} else {
		mode := "test"
		if strings.HasPrefix(c.secretKey, "sk_live") {
			mode = "LIVE"
		}
		log.Printf("CONNECT: Stripe Connect enabled [%s mode] (hold=%dd reserve=%.0f%% min=%.0f schedule=%s)",
			mode, c.policy.HoldDays, c.policy.Reserve*100, c.policy.MinPayout, c.policy.Schedule)
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

// payoutOwner resolves the GitHub-linked operator behind a connect/payout request,
// accepting EITHER auth path:
//
//  1. a logged-in BROWSER session cookie (the web /payouts page), or
//  2. a signed CLI request (Ed25519, the SAME request-signing the rest of the client
//     uses) whose pubkey is bound to a non-anonymized GitHub owner.
//
// Both paths converge on the owner's GitHub login + owner record, so every downstream
// gate (KYC / 90-day hold / $25 min / debit-first transfer rail / dispute clawback)
// is identical no matter how the caller authenticated. This is purely an additional
// AUTH path - it changes no policy. A signed-but-UNBOUND keypair (not logged in via
// `rogerai login`) is rejected here: payouts are KYC + GitHub-linked only, so a
// headless provider must have linked GitHub to cash out. An unsigned / anonymous
// request (no cookie, no valid signature) returns ok=false -> 401.
//
// body is the exact request body the signature is verified over (nil for GET).
func (b *broker) payoutOwner(r *http.Request, body []byte) (login string, o store.Owner, ok bool) {
	// 1) Web session cookie (browser). Unchanged web path.
	if l, _, _, sok := b.sessionOwner(r); sok {
		if rec, found, _ := b.db.OwnerByLogin(l); found {
			return l, rec, true
		}
		// A valid session whose login is not (yet) a bound operator: still a logged-in
		// identity - return it so the handler emits the "no operator account" 403.
		return l, store.Owner{}, true
	}
	// 2) Signed CLI request: it MUST verify (identityOf rejects an offered-but-invalid
	// signature), and its pubkey MUST be bound to a non-anonymized GitHub owner (the
	// GitHub-link/KYC prerequisite). A signed-but-unbound keypair is anonymous here -
	// no wallet, no payouts.
	if _, authed, iok := b.identityOf(r, body); iok && authed {
		if rec, found := b.requireOwner(r); found && !rec.Anonymized && rec.GitHubID != 0 {
			return rec.Login, rec, true
		}
	}
	return "", store.Owner{}, false
}

// connectOnboard handles POST /connect/onboard: creates (or reuses) the operator's
// Express connected account and returns a Stripe Account Link to complete KYC. In
// dev (no key) it returns a stub link + marks the account onboarding. Accepts a
// logged-in web session OR a signed CLI request (see payoutOwner).
func (b *broker) connectOnboard(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	login, o, ok := b.payoutOwner(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to link GitHub")
		return
	}
	if o.GitHubID == 0 {
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
// Accepts a logged-in web session OR a signed CLI request (see payoutOwner). It
// also returns the earnings split (payable vs held) + the next-payable date so the
// CLI `rogerai payout status` renders the whole picture in one call.
func (b *broker) connectStatus(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	login, o, ok := b.payoutOwner(r, nil)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to link GitHub")
		return
	}
	if o.GitHubID == 0 {
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
	out := map[string]any{
		"status":     status,
		"can_payout": status == "active",
		"connect_id": o.ConnectID,
		"min_payout": b.conn.policy.MinPayout,
		"hold_days":  b.conn.policy.HoldDays,
		"schedule":   b.conn.policy.Schedule,
	}
	// The earnings split (payable / held / paid + next release) keyed by the owner
	// pubkey (the account id), so `rogerai payout status` shows payable-vs-held +
	// the next-payable date without a second round trip.
	if split, err := b.db.EarningSplitOf(o.Pubkey, time.Now()); err == nil {
		out["earnings"] = split
	}
	writeJSON(w, http.StatusOK, out)
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
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	login, o, ok := b.payoutOwner(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to link GitHub")
		return
	}
	if o.GitHubID == 0 {
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
	// Flag-gated transactional notice (async, best-effort): tell the operator their
	// payout is on its way. No-op when RESEND_API_KEY is unset or no email on file.
	b.emailPayoutSent(o.Email, pay.Amount, transferID)
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
	// Fail-closed in production: under ROGERAI_REQUIRE_LIVE, never run the dev stub and
	// never issue a transfer without a real sk_live key + a real connected account. A
	// missing/test key or a stub account aborts with an error so SettlePayout is NEVER
	// reached with a fake tr_dev_stub_... id (the payout rolls back via FailPayout).
	if requireLive() && (!strings.HasPrefix(b.conn.secretKey, "sk_live") || connectID == "" || connectID == "acct_dev_stub") {
		log.Printf("CONNECT: REFUSING payout transfer for %s - REQUIRE_LIVE set but key/connect account is not live (key live=%v connect=%q)", login, strings.HasPrefix(b.conn.secretKey, "sk_live"), connectID)
		return "", errStripeTransfer
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

// payoutTransferReversal reverses `amount` credits of a prior Stripe Transfer (the
// operator's already-paid share on a disputed charge - ACCOUNT-PAYOUTS-DESIGN 6.4 step
// 4), idempotent on idemKey, returning the reversal id. Uses the injectable
// conn.reverseTransfer when set (tests), then a dev stub when Stripe is unconfigured /
// the transfer id is a dev stub, else the real Stripe transfer_reversals API. Best
// effort on the money rail: the store already recorded the payout_reversed ledger row,
// so a transient Stripe failure here is logged for reconciliation, not lost.
func (b *broker) payoutTransferReversal(transferID string, amount float64, idemKey string) (string, error) {
	cents := int64(amount*b.bill.creditUSD*100 + 0.5)
	if b.conn.reverseTransfer != nil {
		return b.conn.reverseTransfer(transferID, cents, idemKey)
	}
	// Fail-closed in production: never run the dev stub under REQUIRE_LIVE without a real
	// live key + a real transfer id. A stub/empty transfer id can't be reversed for real.
	if requireLive() && (!strings.HasPrefix(b.conn.secretKey, "sk_live") || transferID == "" || strings.HasPrefix(transferID, "tr_dev_stub_")) {
		log.Printf("CONNECT: REFUSING transfer reversal - REQUIRE_LIVE set but key/transfer is not live (transfer=%q)", transferID)
		return "", errStripeTransfer
	}
	if b.conn.secretKey == "" || transferID == "" || strings.HasPrefix(transferID, "tr_dev_stub_") {
		id := "trr_dev_stub_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		log.Printf("CONNECT(STUB): reverse %.4f credits of transfer %s -> %s (no real money moved)", amount, transferID, id)
		return id, nil
	}
	var rev struct {
		ID string `json:"id"`
	}
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(cents, 10))
	req, _ := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/transfers/"+transferID+"/reversals", strings.NewReader(form.Encode()))
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
		log.Printf("stripe transfer reversal error %d: %s", resp.StatusCode, rb)
		return "", errStripeTransfer
	}
	_ = json.Unmarshal(rb, &rev)
	if rev.ID == "" {
		return "", errStripeTransfer
	}
	return rev.ID, nil
}

// reversePaidLots issues a Stripe Transfer Reversal for each already-paid earning lot
// a dispute clawed (ACCOUNT-PAYOUTS-DESIGN 6.4 step 4). The store already recorded the
// payout_reversed ledger row + marked the lots clawed atomically; this pulls the money
// back from the operator's connected account. Idempotent per (dispute, lot) via the
// Stripe Idempotency-Key, so a webhook redelivery never double-reverses. A reversal
// whose transfer id is unknown (e.g. a legacy paid lot with no recorded transfer) is
// logged and skipped - the ledger clawback stands and it is reconciled out of band.
func (b *broker) reversePaidLots(disputeID string, reversals []store.Reversal) {
	for _, rv := range reversals {
		if rv.TransferID == "" {
			log.Printf("dispute %s: paid lot %d has no recorded Stripe transfer id - reversal skipped (ledger clawback stands; reconcile manually)", disputeID, rv.LotID)
			continue
		}
		idem := "reverse:" + disputeID + ":" + strconv.FormatInt(rv.LotID, 10)
		revID, err := b.payoutTransferReversal(rv.TransferID, rv.Amount, idem)
		if err != nil {
			log.Printf("dispute %s: transfer reversal FAILED for lot %d (transfer %s, %.4f credits): %v - ledger clawback stands, reconcile",
				disputeID, rv.LotID, rv.TransferID, rv.Amount, err)
			continue
		}
		log.Printf("dispute %s: reversed %.4f credits of transfer %s (lot %d) -> %s", disputeID, rv.Amount, rv.TransferID, rv.LotID, revID)
		// Flag-gated transactional notice (async, best-effort): tell the operator their
		// paid-out earning was clawed back on a dispute. No-op when RESEND_API_KEY is
		// unset or the owner has no email on file.
		b.emailPayoutReversed(b.emailOf(rv.AccountID), rv.Amount, disputeID)
	}
}

// payoutsHistory handles GET /payouts/history: the operator's payout + clawback log.
// Accepts a logged-in web session OR a signed CLI request (see payoutOwner).
func (b *broker) payoutsHistory(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	_, o, ok := b.payoutOwner(r, nil)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to link GitHub")
		return
	}
	if o.GitHubID == 0 {
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
