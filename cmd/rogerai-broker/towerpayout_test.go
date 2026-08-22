package main

// towerpayout_test.go proves the claim the operator-facing copy makes: a Tower operator's
// relay share is not a parallel bookkeeping entry - it is a real earning lot on the SAME
// payout rail a serving node uses, and it cashes out through the SAME endpoint, to a Stripe
// transfer, with the same hold / minimum / KYC gates.
//
// Contract: features/tower/edge_dispatch.feature ("what the operator is paid for").

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
)

// THE WHOLE RAIL, from a relayed request to money leaving for the operator's bank.
func TestATowerOperatorCashesOutTheirRelayShare(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0") // byte tariff OFF: only the pinned price bills
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")     // the hold is policy; this test is about the rail
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	t.Setenv("ROGERAI_PAYOUT_MIN", "25") // the REAL default minimum, not a convenient one
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30
	b.conn = loadConnect()
	b.bill.creditUSD = 1

	op := signedInOperator(t, b, "tower-op")
	towerAcct := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)

	stPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stationOwner := hexOf(stPub)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: stationOwner, Login: "station-op", Email: "sp@x.test", EmailVerifiedAt: time.Now().Unix(),
	}))
	stationPriv := attachStation(t, b, "st-1", tw.id, stationOwner)

	// A relayed request big enough to clear the real $25 minimum on the tower's 10%:
	// 60,000 output tokens at $5,000/1M = 300 credits gross -> station 210, tower 30.
	cpub := issuedEdgeGrantPriced(t, b, "att-cash", tw.id, "st-1", 0, 5_000_000_000)
	consumerWallet := bindEdgeConsumer(t, b, cpub)
	_, err = b.db.AddCredits(consumerWallet, 1000)
	require.NoError(t, err)
	held, err := b.db.HoldFor(consumerWallet, "att-cash", 400)
	require.NoError(t, err)
	require.True(t, held)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-cash",
		"receipt": signedReceiptTok(t, stationPriv, "att-cash", "st-1", make([]byte, 60_000),
			dispatch.Usage{In: 0, Out: 60_000}, dispatch.Usage{In: 0, Out: 60_000}),
	})
	require.NoError(t, err)
	var settled map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &settled)
	require.Equal(t, http.StatusOK, code, settled)

	// The relay share is a real PAYABLE lot on the shared ledger - not a separate balance.
	split, err := b.db.EarningSplitOf(towerAcct, time.Now())
	require.NoError(t, err)
	require.InDelta(t, 30.0, split.Payable, 1e-6, "the tower operator's 10% is payable")

	// And the dashboard tells relaying apart from serving for the same account.
	_, byNode, err := b.db.EarningRollups(towerAcct)
	require.NoError(t, err)
	require.NotEmpty(t, byNode)
	require.True(t, IsTowerNode(byNode[0].Key), "the relay share is tagged as relay provenance")

	// CASH OUT through the ordinary endpoint: KYC gate, minimum, single-flight, transfer.
	require.NoError(t, b.db.SetConnect(op.login, "acct_dev_stub", "active"))
	var gotCents int64
	var gotDest string
	b.conn.transfer = func(dest string, cents int64, idem string) (string, error) {
		gotDest, gotCents = dest, cents
		return "tr_tower_1", nil
	}
	w := httptest.NewRecorder()
	b.payoutsRequest(w, signedReq(http.MethodPost, "/payouts/request", []byte("{}"), op.priv))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var out struct {
		Payout store.Payout `json:"payout"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, store.PayoutPaid, out.Payout.State)
	require.Equal(t, "tr_tower_1", out.Payout.StripeTransferID)
	require.InDelta(t, 30.0, out.Payout.Amount, 1e-6, "the paid amount is the relay share")
	require.Equal(t, "acct_dev_stub", gotDest, "to the operator's own connected account")
	require.Equal(t, int64(30*100), gotCents, "the transfer moves exactly what was debited")

	// The lots are spent: a second cash-out has nothing to pay.
	after, err := b.db.EarningSplitOf(towerAcct, time.Now())
	require.NoError(t, err)
	require.InDelta(t, 0, after.Payable, 1e-9)
	require.InDelta(t, 30.0, after.Paid, 1e-6)
}

// The gates are the SAME gates: no Connect onboarding, no money out - a tower operator is
// not a special case of the payout rail.
func TestATowerOperatorMustFinishKYCLikeAnyone(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_MIN", "25")
	b, _ := towerTestBroker(t)
	b.conn = loadConnect()
	b.bill.creditUSD = 1
	op := signedInOperator(t, b, "tower-op")

	w := httptest.NewRecorder()
	b.payoutsRequest(w, signedReq(http.MethodPost, "/payouts/request", []byte("{}"), op.priv))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "Stripe Connect onboarding")
}
