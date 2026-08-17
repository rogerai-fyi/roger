package main

// towerearnings.go is the operator's read side of what they have earned: "what am I owed?".
//
// Contract: features/tower/edge_dispatch.feature (the "what the operator is paid for" scenario).
//
// # WHICH LEDGER ANSWERS WHICH QUESTION
//
// Two ledgers record a settled attempt and they are NOT interchangeable - an earlier version
// of this endpoint answered from the wrong one, reporting a policy-priced accrual in micros
// as though it were the operator's balance, so this surface and the Payouts page disagreed
// about the same money:
//
//	MONEY  - internal/store earning lots. What the operator is actually paid: 10% of the gross
//	         the consumer paid at the serving node's pinned price, held then payable then paid,
//	         cashed out through /payouts/request. THIS is the balance, and it is quoted in
//	         CREDITS, the same unit the website shows.
//	TRAIL  - internal/towercore/earnings. One durable row per settled attempt (attempt id,
//	         tower, model, self-dealing flag), priced by operations policy for the future
//	         revenue-share program. It moves nothing and is not a balance; only its COUNTS
//	         are quoted here, as provenance.
//
// It only READS. Both ledgers are written on the settlement path (toweredge.go); no endpoint
// in this process can move a cent, and the cash-out itself lives on the shared payout rail.

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
	// THE MONEY, from the ledger that pays it - the same numbers /payouts/earnings serves the
	// website, so an operator reading the CLI and the dashboard can never see two answers.
	now := time.Now()
	split, serr := b.db.EarningSplitOf(ownerPubkey, now)
	if serr != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read your earnings - try again in a moment")
		return
	}
	// Relaying vs serving, told apart by the "tower:" provenance prefix the settle path
	// stamps on a relay lot. Lifetime attributed totals, as on the dashboard.
	var relay, serving float64
	if _, byNode, rerr := b.db.EarningRollups(ownerPubkey); rerr == nil {
		for _, rr := range byNode {
			if IsTowerNode(rr.Key) {
				relay += rr.Amount
			} else {
				serving += rr.Amount
			}
		}
	}
	out := map[string]any{
		"owner": ownerPubkey,
		// CREDITS, the unit the website and the payout rail use. Stated so nobody reads one
		// of these as the micros the trail below is priced in.
		"unit":          "credits",
		"held":          round6(split.Held),
		"payable":       round6(split.Payable),
		"paid":          round6(split.Paid),
		"next_release":  split.NextRelease,
		"from_relaying": round6(relay),
		"from_serving":  round6(serving),
		"cash_out": "POST /payouts/request once payable clears the minimum - the same rail, " +
			"hold and Stripe Connect onboarding a serving node uses",
	}
	// THE TRAIL, counts only: how many settled attempts stand behind that money, and how much
	// was excluded as self-dealing (own traffic through own Station - recorded, never owed).
	// Never quoted as a balance: it is priced by operations policy, not by what a consumer paid.
	if owed, oerr := ts.earnings.OwedTo(ownerPubkey, time.Time{}); oerr == nil {
		out["attempts"] = owed.Attempts
		out["self_dealt_attempts"] = owed.SelfDealt > 0
	}
	writeJSON(w, http.StatusOK, out)
}
