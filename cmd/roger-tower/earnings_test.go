package main

// earnings_test.go: `roger-tower earnings` shows the operator the SAME money the website
// does - credits, held/payable/paid, relaying told apart from serving - because both read
// the ledger the payout rail pays from.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEarningsShowsWhatCoreSaysTheAccountHasEarned(t *testing.T) {
	core := newCoreStub(t)
	core.reply["/tower/earnings/owed"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{
			"owner":"ab12","unit":"credits","held":4.5,"payable":30.25,"paid":12,
			"next_release":1767225600,"from_relaying":30.25,"from_serving":16.5,
			"attempts":97,
			"cash_out":"POST /payouts/request once payable clears the minimum"
		}`))
		return true
	}

	out, err := runCLI(t, "earnings")
	require.NoError(t, err)
	require.Contains(t, out, "credits")
	require.Contains(t, out, "payable now   30.2500")
	require.Contains(t, out, "held          4.5000")
	require.Contains(t, out, "paid to date  12.0000")
	require.Contains(t, out, "lifetime by stream:")
	require.Contains(t, out, "relaying    30.2500")
	require.Contains(t, out, "serving     16.5000")
	require.Contains(t, out, "97 attempt(s)")
	require.Contains(t, out, "cash out:", "the operator is told how to get it, not left guessing")
}

// Core unreachable is an error the operator sees, not a zero balance they might believe.
func TestEarningsSurfacesAnUnreachableCore(t *testing.T) {
	core := newCoreStub(t)
	core.reply["/tower/earnings/owed"] = func(w http.ResponseWriter, _ int) bool {
		http.Error(w, `{"error":{"message":"the funding ledger is not available"}}`,
			http.StatusServiceUnavailable)
		return true
	}
	_, err := runCLI(t, "earnings")
	require.Error(t, err)
	require.Contains(t, err.Error(), "funding ledger")
}

// An ABSENT lifetime split (Core could not read the rollup) is reported as unavailable, not
// as zeros: "relaying 0.0000" beside a real payable reads as earnings that vanished.
func TestEarningsSaysUnavailableRatherThanZeroWhenTheSplitIsMissing(t *testing.T) {
	core := newCoreStub(t)
	core.reply["/tower/earnings/owed"] = func(w http.ResponseWriter, _ int) bool {
		_, _ = w.Write([]byte(`{"unit":"credits","payable":30.25,"held":0,"paid":0}`))
		return true
	}
	out, err := runCLI(t, "earnings")
	require.NoError(t, err)
	require.Contains(t, out, "payable now   30.2500")
	require.Contains(t, out, "lifetime by stream: unavailable right now")
	require.NotContains(t, out, "relaying    0.0000", "absent is not zero")
}
