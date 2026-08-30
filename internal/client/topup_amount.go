package client

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// DefaultTopupUSD is what a bare top-up adds - `roger topup`, `roger balance --topup`,
// and the TUI's `/topup` alike. It is documented on the pricing page and in the manual,
// so it lives in one place rather than in each caller.
const DefaultTopupUSD = 10.0

// ParseTopupAmount reads the dollar amount for a top-up.
//
// It is the ONE reader for every surface that starts a checkout. There used to be three,
// and they disagreed: the documented `roger topup $25` parsed the argument bare, failed,
// and silently charged the $10 default, while the retained `balance --topup $25` alias
// stripped the dollar sign and charged $25. The TUI carried a third copy of the same bug.
//
// On a money path an unreadable amount is an ERROR, never a different charge. Callers
// must surface it rather than fall back to the default - a top-up nobody typed is not
// noticed until the receipt.
//
// Non-finite values are refused explicitly. ParseFloat accepts "NaN" and "Inf", and both
// walk past a naive `usd <= 0` guard (NaN compares false against everything, Inf is
// greater than zero); they then reach json.Marshal, which fails on a non-finite float,
// and the request goes out with an empty body.
func ParseTopupAmount(args []string) (float64, error) {
	if len(args) == 0 {
		return DefaultTopupUSD, nil
	}
	raw := strings.TrimSpace(args[0])
	amt := strings.TrimSpace(strings.TrimPrefix(raw, "$"))
	usd, err := strconv.ParseFloat(amt, 64)
	if err != nil {
		return 0, fmt.Errorf("top-up amount %q is not a number - try `roger topup 25`", raw)
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, fmt.Errorf("top-up amount %q is not a real amount", raw)
	}
	if usd <= 0 {
		return 0, fmt.Errorf("top-up amount must be more than $0 - got %q", raw)
	}
	return usd, nil
}
