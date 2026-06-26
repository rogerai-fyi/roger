package client

import (
	"encoding/json"
	"net/http"
)

// This file exposes data-form readers used by the localhost web console (internal/webui),
// which renders these surfaces itself rather than printing them. They wrap the same broker
// calls the CLI/TUI already use, so there is one code path per surface.

// Discover fetches the broker's current open-market offer list. Exported data form of the
// internal failover discover.
func Discover(broker string) ([]Offer, error) { return discover(broker) }

// BalanceInfo is the signed wallet read: the balance, whether the broker recognizes a real
// account (logged in), and the month-to-date spend cap + spend.
type BalanceInfo struct {
	Balance      float64 `json:"balance"`
	LoggedIn     bool    `json:"logged_in"`
	MonthlyCap   float64 `json:"monthly_cap"`
	MonthlySpend float64 `json:"monthly_spend"`
}

// FetchBalance reads the signed wallet balance for user from broker — the data form of the
// CLI's Balance print and the TUI's fetchBalance.
func FetchBalance(broker, user string) (BalanceInfo, error) {
	req, _ := http.NewRequest(http.MethodGet, broker+"/balance", nil)
	SignRequest(req, nil)
	req.Header.Set("X-Roger-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return BalanceInfo{}, err
	}
	defer resp.Body.Close()
	var b BalanceInfo
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return BalanceInfo{}, err
	}
	return b, nil
}
