package main

// towerselfdeal_test.go pins the wash-trading defence ON THE MONEY, not just on the
// read-only accrual trail. An operator buying from themselves pays in full and earns
// nothing - otherwise, now that earnings cash out to a bank, self-dealing would be a way to
// convert credits into money at a 20-30% discount.
//
// Contract: features/tower/edge_dispatch.feature (what the operator is paid for).

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// settleSelfDealt runs one priced attempt where the CONSUMER is the given account, and
// returns the station owner's and tower operator's payable balances afterwards.
func settleSelfDealt(t *testing.T, consumerIsTowerOp, consumerIsStationOwner bool) (towerPayable, stationPayable float64) {
	t.Helper()
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30

	op := signedInOperator(t, b, "tower-op")
	towerAcct := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)

	stationOp := signedInOperator(t, b, "station-op")
	stationOwner := ownerPubkeyOf(t, b, stationOp.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, stationOwner)

	// The consumer: the tower operator, the station owner, or an unrelated account.
	cpub := issuedEdgeGrantPriced(t, b, "att-sd", tw.id, "st-1", 0, 5_000_000_000)
	var consumerWallet string
	switch {
	case consumerIsTowerOp:
		consumerWallet = rebindConsumerTo(t, b, cpub, op.login)
	case consumerIsStationOwner:
		consumerWallet = rebindConsumerTo(t, b, cpub, stationOp.login)
	default:
		consumerWallet = bindEdgeConsumer(t, b, cpub)
	}
	_, err := b.db.AddCredits(consumerWallet, 100)
	require.NoError(t, err)
	before, err := b.db.BalanceOf(consumerWallet, 0)
	require.NoError(t, err)
	held, err := b.db.HoldFor(consumerWallet, "att-sd", 10)
	require.NoError(t, err)
	require.True(t, held)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-sd",
		"receipt": signedReceiptTok(t, stationPriv, "att-sd", "st-1", make([]byte, 300),
			dispatch.Usage{In: 0, Out: 300}, dispatch.Usage{In: 0, Out: 200}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)

	// THE CONSUMER PAYS IN FULL either way - withholding the operator's share is the
	// defence; making self-service free would be its own exploit.
	after, err := b.db.BalanceOf(consumerWallet, 0)
	require.NoError(t, err)
	require.InDelta(t, 1.0, before-after, 1e-6, "200 tokens at $5,000/1M = 1.0 credit, charged in full")
	now := time.Now()
	sTw, _ := b.db.EarningSplitOf(towerAcct, now)
	sSt, _ := b.db.EarningSplitOf(stationOwner, now)
	return sTw.Payable, sSt.Payable
}

// rebindConsumerTo binds the grant's consumer key to an EXISTING account (same GitHub id),
// so sameAccount resolves the consumer and the operator to one account.
func rebindConsumerTo(t *testing.T, b *broker, cpub []byte, login string) string {
	t.Helper()
	existing, found, err := b.db.OwnerByLogin(login)
	require.NoError(t, err)
	require.True(t, found)
	o := store.Owner{
		Pubkey: hexOf(cpub), Login: login, GitHubID: existing.GitHubID,
		Email: existing.Email, EmailVerifiedAt: existing.EmailVerifiedAt,
	}
	require.NoError(t, b.db.BindOwner(o))
	wallet, ok := accountWalletForOwner(o)
	require.True(t, ok)
	return wallet
}

// An unrelated consumer: both operators earn normally. The control case.
func TestArmsLengthTrafficEarnsBothShares(t *testing.T) {
	towerPayable, stationPayable := settleSelfDealt(t, false, false)
	require.InDelta(t, 0.10, towerPayable, 1e-6, "the tower earns 10% of gross")
	require.InDelta(t, 0.70, stationPayable, 1e-6, "the station owner earns 70%")
}

// THE TOWER OPERATOR RELAYING THEIR OWN TRAFFIC earns nothing - the case that was not
// checked anywhere before, in either ledger.
func TestATowerRelayingItsOwnersTrafficEarnsNothing(t *testing.T) {
	towerPayable, stationPayable := settleSelfDealt(t, true, false)
	require.Zero(t, towerPayable, "relaying your own spend is not earning")
	require.InDelta(t, 0.70, stationPayable, 1e-6, "the arms-length station is still paid")
}

// THE STATION OWNER SERVING THEIR OWN TRAFFIC earns nothing on the money ledger, not just a
// flag on the trail.
func TestAStationServingItsOwnersTrafficEarnsNothing(t *testing.T) {
	towerPayable, stationPayable := settleSelfDealt(t, false, true)
	require.Zero(t, stationPayable, "serving your own spend is not earning")
	require.InDelta(t, 0.10, towerPayable, 1e-6, "the arms-length tower is still paid")
}
