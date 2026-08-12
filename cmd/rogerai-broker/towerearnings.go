package main

// towerearnings.go is the operator's read side of the funding ledger: "what am I owed?".
//
// Contract: features/tower/edge_dispatch.feature (the "what the operator is paid for" scenario).
//
// It only READS. The ledger accrues on the settlement path (toweredge.go); disbursing what is
// owed is a separate concern behind the payment rails, deliberately not reachable from here.
// An operator can see their balance without any endpoint in this process being able to move it.

import (
	"net/http"
	"time"
)

// towerEarningsOwed answers what the signed-in operator is owed.
//
// Authenticated as the OWNER, not as a Tower: earnings belong to the account that owns the
// Station, and the balance is summed for exactly the pubkey that signed this request. An
// operator can therefore read only their own, and knowing another account's pubkey reveals
// nothing - the sum is scoped to the authenticated key.
func (b *broker) towerEarningsOwed(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body := readTowerBody(r)

	_, ok := b.towerOperator(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "reading earnings requires a signed-in account - run `roger-tower login`")
		return
	}
	// The balance is summed for the pubkey that signed, taken from the authenticated request
	// rather than the body - the same account key an attachment records as its owner, which is
	// what the accrual was filed under.
	ownerPubkey := r.Header.Get("X-Roger-Pubkey")
	if _, found, oerr := b.db.OwnerByPubkey(ownerPubkey); oerr != nil || !found {
		jsonErr(w, http.StatusUnauthorized, "reading earnings requires a signed-in account - run `roger-tower login`")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	if ts.earnings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "the funding ledger is not available")
		return
	}
	// ALL-TIME, deliberately: the net balance is only trustworthy over the full history (a
	// windowed read can net a payout against accruals other than the ones it discharged - see
	// Store.OwedTo). The operator is shown their true standing, not a recent slice of it.
	owed, err := ts.earnings.OwedTo(ownerPubkey, time.Time{})
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the funding ledger - try again in a moment")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"owner":    ownerPubkey,
		"accrued":  owed.Accrued,
		"paid":     owed.Paid,
		"owed":     owed.Owed(),
		"attempts": owed.Attempts,
		// The unit is stated so nobody reads a raw integer as whole currency. Millionths of the
		// settlement currency's minor unit - the exact accrual, no rounding applied.
		"unit": "micros",
	})
}
