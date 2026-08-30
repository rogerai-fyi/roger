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

// MinTopupUSD is the smallest top-up the broker will open a checkout for. It lives here,
// beside the parser, because it was previously three different numbers on four surfaces:
// the CLI and the web console each refused only <= $0, while the broker - the actual
// enforcement point - silently REWROTE anything under a dollar to $10. So `roger topup
// 0.50` charged $10 and nothing said otherwise. Every surface reads this constant now,
// and the broker refuses rather than substitutes.
const MinTopupUSD = 1.0

// WholeCents reports whether an amount is an exact number of cents. Stripe is charged
// int(usd*100), which TRUNCATES, while credits are granted on the untouched float - so
// $1.999 took $1.99 and credited 1.999. Neither figure is wrong to want; picking one
// silently is the problem. An amount finer than a cent is refused instead, on the client
// and again at the broker.
//
// The comparison rounds first because binary floats cannot hold most decimal cents
// exactly: 1.15*100 is 114.99999999999999, and a bare integer check would refuse a price
// a person can obviously type.
func WholeCents(usd float64) bool {
	cents := usd * 100
	return math.Abs(cents-math.Round(cents)) < 1e-6
}

// ParseTopupAmount reads the dollar amount for a top-up on the CLI and TUI surfaces.
//
// It is the ONE reader for those. There used to be three,
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
	if usd < MinTopupUSD {
		return 0, fmt.Errorf("top-up minimum is $%.0f - got %q", MinTopupUSD, raw)
	}
	if !WholeCents(usd) {
		return 0, fmt.Errorf("top-up amount %q is finer than a cent - try $%.2f", raw, usd)
	}
	return usd, nil
}
