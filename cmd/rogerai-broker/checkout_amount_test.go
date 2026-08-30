package main

import (
	"math"
	"net/http"
	"net/http/httptest"
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
func topupCents(t *testing.T, usd float64) int {
	t.Helper()
	cents, err := stripeUnitAmount(usd)
	if err != nil {
		t.Fatalf("stripeUnitAmount(%v) errored: %v", usd, err)
	}
	return cents
}

// Out of range is a refusal, not a nearby number. A clamp here would be the same silent
// substitution the whole path refuses, just moved one function down.
func TestStripeUnitAmountRefusesOutOfRange(t *testing.T) {
	for _, usd := range []float64{0.5, 0, -1, 1e18, math.NaN(), math.Inf(1)} {
		if cents, err := stripeUnitAmount(usd); err == nil {
			t.Errorf("stripeUnitAmount(%v) = %d with no error, want a refusal", usd, cents)
		}
	}
}

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
		if got := topupCents(t, c.usd); got != c.want {
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
		if got := topupCents(t, usd); got != cents {
			t.Fatalf("a $%.2f top-up charges %d cents, want %d", usd, got, cents)
		}
	}
}

// The two tests above exercise the helper. This one exercises the HANDLER, against a
// stand-in Stripe, and asserts the form field that is actually sent - because a test that
// only calls stripeUnitAmount stays green if the handler goes back to int(usd*100), which
// is precisely the regression it is supposed to prevent.
func TestCheckoutSendsStripeTheTypedAmount(t *testing.T) {
	var got url.Values
	stripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://checkout.stripe.test/s/1"}`))
	}))
	defer stripe.Close()
	old := stripeAPIBase
	stripeAPIBase = stripe.URL
	defer func() { stripeAPIBase = old }()

	b, priv := newCheckoutBroker(t)
	b.bill.creditUSD = 1
	w := postCheckout(t, b, priv, `{"usd":1.15}`)
	if w.Code != http.StatusOK {
		t.Fatalf("checkout = %d, want 200: %s", w.Code, w.Body.String())
	}
	if v := got.Get("line_items[0][price_data][unit_amount]"); v != "115" {
		t.Errorf("Stripe was charged %q cents for a $1.15 top-up, want \"115\"", v)
	}
	credits, err := strconv.ParseFloat(got.Get("metadata[credits]"), 64)
	if err != nil || credits != 1.15 {
		t.Errorf("metadata[credits] = %q (err %v), want 1.15 - the credits must describe the charge",
			got.Get("metadata[credits]"), err)
	}
}
