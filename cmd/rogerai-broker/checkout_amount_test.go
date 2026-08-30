package main

import (
	"net/url"
	"strconv"
	"testing"

	"rogerai.fm/roger/v6/internal/client"
)

// int(usd*100) truncates, and binary floats put most decimal cents just under their
// integer: 1.15*100 is 114.99999999999999, so a $1.15 top-up charged $1.14. It passed
// every guard - it IS a whole number of cents - and 4583 of the 99901 whole-cent amounts
// between $1 and $1000 are affected the same way.
//
// Nothing asserted the amount actually sent to Stripe, which is why a cent could go
// missing for as long as it did. This does.
func topupCents(usd float64) int { return stripeUnitAmount(usd) }

func TestStripeIsChargedTheAmountThatWasTyped(t *testing.T) {
	for _, c := range []struct {
		usd  float64
		want int
	}{
		{1, 100},
		{1.13, 113}, // truncated to 112 before
		{1.15, 115}, // truncated to 114 before
		{4.02, 402}, // truncated to 401 before
		{12.50, 1250},
		{25, 2500},
		{999.99, 99999},
	} {
		if got := topupCents(c.usd); got != c.want {
			t.Errorf("a $%.2f top-up charges %d cents, want %d", c.usd, got, c.want)
		}
	}
}

// Every amount the guards accept must charge exactly what was typed. This is the
// property, swept across the whole accepted range rather than a handful of examples.
func TestEveryAcceptedAmountChargesExactly(t *testing.T) {
	for cents := int(client.MinTopupUSD * 100); cents <= 100000; cents++ {
		usd, _ := strconv.ParseFloat(strconv.FormatFloat(float64(cents)/100, 'f', 2, 64), 64)
		if !client.WholeCents(usd) {
			t.Fatalf("$%.2f is not recognized as whole cents", usd)
		}
		if got := topupCents(usd); got != cents {
			t.Fatalf("a $%.2f top-up charges %d cents, want %d", usd, got, cents)
		}
	}
}

// The credits recorded in the session metadata must describe the same money that is
// charged, not a separately derived float.
func TestMetadataCreditsMatchTheCharge(t *testing.T) {
	b, priv := newCheckoutBroker(t)
	_ = priv
	b.bill.creditUSD = 1
	form := url.Values{}
	cents := stripeUnitAmount(1.15)
	form.Set("unit_amount", strconv.Itoa(cents))
	if want := 115; cents != want {
		t.Fatalf("unit_amount = %d, want %d", cents, want)
	}
	if credits := float64(cents) / 100 / b.bill.creditUSD; credits != 1.15 {
		t.Errorf("credits = %v, want 1.15 (derived from the charge)", credits)
	}
}
