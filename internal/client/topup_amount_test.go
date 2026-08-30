package client

import (
	"math"
	"testing"
)

// There were THREE readers of a top-up amount - `roger topup`, the `balance --topup`
// aliases, and the TUI's /topup - and they disagreed. The documented one was the worst:
// it parsed the argument bare, so `roger topup $25` failed ParseFloat and silently opened
// Checkout for the $10 default. The alias stripped the "$" and got it right. The TUI had
// its own third copy of the same bug.
//
// The parser lives here, beside Topup and TopupURL, because the amount is the argument to
// those calls and every surface that makes one already imports this package. One reader is
// the fix; three readers was the bug.
//
// The rule on a money path: an amount that cannot be read is an ERROR, never a different
// charge. A typo that quietly bills a number nobody typed is worse than a refusal, and it
// is not noticed until the receipt.
func TestParseTopupAmount(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		want    float64
		wantErr bool
	}{
		{"no argument takes the documented default", nil, DefaultTopupUSD, false},
		{"empty argument list", []string{}, DefaultTopupUSD, false},
		{"a plain amount", []string{"25"}, 25, false},
		{"a dollar sign is tolerated", []string{"$25"}, 25, false},
		{"cents survive", []string{"12.50"}, 12.5, false},
		{"surrounding space", []string{" 25 "}, 25, false},

		// None of these may quietly become the default.
		{"a typo is refused, not rounded to the default", []string{"twentyfive"}, 0, true},
		{"a stray flag is refused", []string{"--yes"}, 0, true},
		{"zero is refused", []string{"0"}, 0, true},
		{"a negative is refused", []string{"-5"}, 0, true},
		{"a lone dollar sign is refused", []string{"$"}, 0, true},
		// The broker will not open a checkout under a dollar. It used to REWRITE such a
		// request to $10 instead of refusing it, so $0.50 charged $10.
		{"below the minimum is refused", []string{"0.50"}, 0, true},
		{"a cent is refused", []string{"0.01"}, 0, true},
		{"the minimum itself is accepted", []string{"1"}, 1, false},
		// A fraction of a cent cannot be charged, so $1.999 has to become some other
		// number before it reaches Stripe. Choosing that number for the person is the
		// substitution this whole chain is about, so it is refused instead.
		{"a sub-cent amount is refused", []string{"1.999"}, 0, true},
		{"a third of a dollar is refused", []string{"1.333"}, 0, true},
		{"whole cents are fine", []string{"1.99"}, 1.99, false},
		{"a round dollar is fine", []string{"25"}, 25, false},
		{"an empty argument is refused", []string{""}, 0, true},

		// ParseFloat accepts these, and both walk past a `usd <= 0` guard: NaN compares
		// false against everything and Inf is greater than zero. They then reach
		// json.Marshal, which fails on a non-finite float, and Topup discards that error
		// and posts an empty body.
		{"NaN is refused", []string{"NaN"}, 0, true},
		{"Inf is refused", []string{"Inf"}, 0, true},
		{"+Inf is refused", []string{"+Inf"}, 0, true},
		{"-Inf is refused", []string{"-Inf"}, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseTopupAmount(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseTopupAmount(%q) = $%v, want an error rather than a charge", c.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTopupAmount(%q) errored: %v", c.args, err)
			}
			if got != c.want {
				t.Errorf("ParseTopupAmount(%q) = $%v, want $%v", c.args, got, c.want)
			}
		})
	}
}

// Whatever the parser returns must survive the marshal Topup performs. This is the
// property the NaN/Inf cases above exist to protect, stated directly.
func TestParseTopupAmountAlwaysReturnsAFiniteChargeableAmount(t *testing.T) {
	for _, arg := range []string{"25", "$25", "0.01", "1e6", "NaN", "Inf", "-Inf", "bogus", "0", "-1"} {
		usd, err := ParseTopupAmount([]string{arg})
		if err != nil {
			continue // refused, which is always an acceptable outcome
		}
		if math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
			t.Errorf("ParseTopupAmount(%q) accepted an uncharg[e]able amount: %v", arg, usd)
		}
	}
}
