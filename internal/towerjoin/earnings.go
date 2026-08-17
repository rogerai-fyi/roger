package towerjoin

// earnings.go is the operator asking Roger Core what they have earned, from the machine that
// earned it. Signed by the ACCOUNT (`roger-tower login`), not by the Tower: earnings belong
// to the account that owns the fleet, and Core scopes the answer to the key that signs.

import "encoding/json"

// Earnings is what Core reports for the signed-in account, in CREDITS - the same unit and the
// same numbers the website's Payouts page shows.
type Earnings struct {
	Unit        string  `json:"unit"`
	Held        float64 `json:"held"`
	Payable     float64 `json:"payable"`
	Paid        float64 `json:"paid"`
	NextRelease int64   `json:"next_release"`
	// FromRelaying/FromServing are LIFETIME totals by stream, present only when Core could
	// read the rollup - absent means "unknown", which is not the same as zero.
	FromRelaying float64 `json:"from_relaying"`
	FromServing  float64 `json:"from_serving"`
	SplitKnown   bool    `json:"-"`
	Attempts     int64   `json:"attempts"`
	CashOut      string  `json:"cash_out"`
}

// FetchEarnings reads the signed-in account's earnings. It takes no Tower state: the
// question is about the ACCOUNT, signed with the operator's own key, so it works from any
// machine - including one whose data directory is locked by a running `serve`.
func FetchEarnings() (Earnings, error) {
	// Decoded twice on purpose: the typed shape for the caller, and the raw map to tell an
	// ABSENT lifetime split (Core could not read the rollup) from a genuine zero.
	var raw map[string]json.RawMessage
	if err := signedPost(brokerBase()+"/tower/earnings/owed", nil, []byte("{}"), &raw); err != nil {
		return Earnings{}, err
	}
	merged, err := json.Marshal(raw)
	if err != nil {
		return Earnings{}, err
	}
	var out Earnings
	if err := json.Unmarshal(merged, &out); err != nil {
		return Earnings{}, err
	}
	_, out.SplitKnown = raw["from_relaying"]
	return out, nil
}
