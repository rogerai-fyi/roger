package main

import "testing"

// `roger topup <amt>` is now the documented spelling - pricing.html and the manual both
// teach it - and it silently charged the wrong amount. cmdTopup parsed args[0] bare, so
// `roger topup $25` failed ParseFloat and fell through to the $10 default without saying
// anything, while the retired `balance --topup $25` alias stripped the dollar sign and
// got it right. The two spellings of the same verb disagreed, and the one being promoted
// was the wrong one.
//
// On a money path, "I could not read that amount" is an error, never a different charge.
// Both paths go through one parser now so they cannot drift apart again.
func TestParseTopupAmount(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		want    float64
		wantErr bool
	}{
		{"no argument takes the documented default", nil, 10, false},
		{"empty argument list", []string{}, 10, false},
		{"a plain amount", []string{"25"}, 25, false},
		{"a dollar sign is tolerated", []string{"$25"}, 25, false},
		{"cents survive", []string{"12.50"}, 12.5, false},
		{"surrounding space", []string{" 25 "}, 25, false},

		// The whole point: none of these may quietly become $10.
		{"a typo is refused, not rounded to the default", []string{"twentyfive"}, 0, true},
		{"a stray flag is refused", []string{"--yes"}, 0, true},
		{"zero is refused", []string{"0"}, 0, true},
		{"a negative is refused", []string{"-5"}, 0, true},
		{"a lone dollar sign is refused", []string{"$"}, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTopupAmount(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseTopupAmount(%q) = $%v, want an error rather than a charge", c.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTopupAmount(%q) errored: %v", c.args, err)
			}
			if got != c.want {
				t.Errorf("parseTopupAmount(%q) = $%v, want $%v", c.args, got, c.want)
			}
		})
	}
}

// The hidden `balance --topup` aliases must read an amount exactly the way the documented
// verb does. They were the ones that got it right; the risk now runs the other way.
func TestBalanceTopupAliasAgreesWithTheDocumentedVerb(t *testing.T) {
	for _, amount := range []string{"25", "$25", "12.50"} {
		want, err := parseTopupAmount([]string{amount})
		if err != nil {
			t.Fatalf("parseTopupAmount(%q) errored: %v", amount, err)
		}
		for _, args := range [][]string{
			{"topup", amount},
			{"--topup", amount},
			{"--topup=" + amount},
		} {
			got, ok := balanceTopupAlias(args)
			if !ok {
				t.Errorf("balanceTopupAlias(%q) did not recognize a top-up", args)
				continue
			}
			if got != want {
				t.Errorf("balanceTopupAlias(%q) = $%v, but `roger topup %s` = $%v", args, got, amount, want)
			}
		}
	}
}
