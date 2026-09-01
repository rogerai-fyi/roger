package main

// The rendered payout policy line goes through TWO Sprintf layers (the reserve clause
// is built first, then spliced into the outer format), which is exactly where a %%
// double-escape silently prints "%!d(..." one day. Pin the rendered text.

import (
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/client"
)

func TestPayoutPolicyLineRendersTheReserveClause(t *testing.T) {
	line := payoutPolicyLine(client.PayoutStatus{
		HoldDays: 30, Reserve: 0.10, ReserveDays: 90, MinPayout: 25, Schedule: "monthly",
	})
	for _, want := range []string{"30-day hold", "10% reserved to day 90", "$25 min", "monthly"} {
		if !strings.Contains(line, want) {
			t.Fatalf("policy line %q lacks %q", line, want)
		}
	}
	// The zero-value fallback renders the ruled defaults, reserve clause omitted when unknown.
	fb := payoutPolicyLine(client.PayoutStatus{})
	if !strings.Contains(fb, "30-day hold") || strings.Contains(fb, "%!") {
		t.Fatalf("fallback policy line broken: %q", fb)
	}
}
