package towerjoin

// earnings.go is the operator asking Roger Core what they have earned, from the machine that
// earned it. Signed by the ACCOUNT (`roger-tower login`), not by the Tower: earnings belong
// to the account that owns the fleet, and Core scopes the answer to the key that signs.

import "rogerai.fm/roger/v5/internal/tower"

// Earnings is what Core reports for the signed-in account, in CREDITS - the same unit and the
// same numbers the website's Payouts page shows.
type Earnings struct {
	Unit         string  `json:"unit"`
	Held         float64 `json:"held"`
	Payable      float64 `json:"payable"`
	Paid         float64 `json:"paid"`
	NextRelease  int64   `json:"next_release"`
	FromRelaying float64 `json:"from_relaying"`
	FromServing  float64 `json:"from_serving"`
	Attempts     int64   `json:"attempts"`
	CashOut      string  `json:"cash_out"`
}

// FetchEarnings reads the signed-in account's earnings.
func FetchEarnings(st *tower.State) (Earnings, error) {
	var out Earnings
	if err := signedPost(brokerBase()+"/tower/earnings/owed", nil, []byte("{}"), &out); err != nil {
		return Earnings{}, err
	}
	return out, nil
}
