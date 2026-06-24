package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The payout client: a headless / CLI provider's money-out calls. Every request is
// Ed25519-signed with the local user key (signRequest), so the broker resolves the
// caller via the SAME signing the rest of the client uses - no web session cookie
// needed. The broker still requires the keypair to be linked to a GitHub account
// (KYC) and enforces the unchanged payout policy (90-day hold, $25 min, monthly,
// Connect-onboarding-complete). See cmd/rogerai-broker/payouts.go.

// PayoutStatus is the Connect/KYC state + the earnings split for `payout status`.
// Credits are dollars (1 credit == $1), surfaced to the user as dollars.
type PayoutStatus struct {
	Status    string  `json:"status"`     // none | onboarding | active | restricted
	CanPayout bool    `json:"can_payout"` // transfers capability is active (KYC done)
	ConnectID string  `json:"connect_id"`
	MinPayout float64 `json:"min_payout"`
	HoldDays  int     `json:"hold_days"`
	Schedule  string  `json:"schedule"` // "monthly" | "weekly"
	Earnings  struct {
		Held        float64 `json:"held"`         // not yet releasable (inside the hold)
		Reserved    float64 `json:"reserved"`     // reserve portion not yet released
		Payable     float64 `json:"payable"`      // releasable now, not yet paid
		Paid        float64 `json:"paid"`         // lifetime transferred out
		NextRelease int64   `json:"next_release"` // unix of the soonest upcoming release (0 = none)
	} `json:"earnings"`
}

// PayoutRecord is one past payout (the `payout history` row).
type PayoutRecord struct {
	ID               int64   `json:"id"`
	Amount           float64 `json:"amount"`
	StripeTransferID string  `json:"stripe_transfer_id,omitempty"`
	State            string  `json:"state"` // pending | paid | reversed | failed
	CreatedAt        int64   `json:"created_at"`
}

// payoutErr extracts the broker's plain-text error message from a non-2xx response
// body (the broker emits {"error":"..."}), falling back to a status-coded message.
func payoutErr(status int, raw []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if msg := strings.TrimSpace(e.Error); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	if msg := strings.TrimSpace(string(raw)); msg != "" && len(msg) < 300 {
		return fmt.Errorf("%s", msg)
	}
	return fmt.Errorf("broker returned status %d", status)
}

// signedDo signs (method, path, body) with the local user key and runs the request,
// returning the response. The caller closes the body.
func signedDo(method, broker, path string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, broker+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	signRequest(req, body)
	return (&http.Client{Timeout: 30 * time.Second}).Do(req)
}

// FetchPayoutStatus reads GET /connect/status as the signed CLI identity.
func FetchPayoutStatus(broker string) (PayoutStatus, error) {
	var st PayoutStatus
	resp, err := signedDo(http.MethodGet, broker, "/connect/status", nil)
	if err != nil {
		return st, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return st, payoutErr(resp.StatusCode, raw)
	}
	_ = json.Unmarshal(raw, &st)
	return st, nil
}

// FetchOnboardURL POSTs /connect/onboard as the signed CLI identity and returns the
// Stripe Connect onboarding URL (a stub URL in dev). The caller opens it.
func FetchOnboardURL(broker string) (string, error) {
	resp, err := signedDo(http.MethodPost, broker, "/connect/onboard", []byte("{}"))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", payoutErr(resp.StatusCode, raw)
	}
	var d struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &d)
	if d.URL == "" {
		return "", fmt.Errorf("no onboarding URL returned")
	}
	return d.URL, nil
}

// RequestPayout POSTs /payouts/request as the signed CLI identity. The broker
// enforces every gate (KYC active, >= $25 min, payable-only, debit-first transfer
// rail); a clear error is returned on any rejection. On success it returns the
// recorded payout.
func RequestPayout(broker string) (PayoutRecord, error) {
	var pr PayoutRecord
	resp, err := signedDo(http.MethodPost, broker, "/payouts/request", []byte("{}"))
	if err != nil {
		return pr, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pr, payoutErr(resp.StatusCode, raw)
	}
	var d struct {
		Payout PayoutRecord `json:"payout"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Payout, nil
}

// FetchPayoutHistory reads GET /payouts/history as the signed CLI identity.
func FetchPayoutHistory(broker string) ([]PayoutRecord, error) {
	resp, err := signedDo(http.MethodGet, broker, "/payouts/history", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, payoutErr(resp.StatusCode, raw)
	}
	var d struct {
		Payouts []PayoutRecord `json:"payouts"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.Payouts, nil
}
