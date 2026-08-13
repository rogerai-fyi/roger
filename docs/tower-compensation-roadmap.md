# Compensated-Tower revenue share — build roadmap

Three approved specs describe the compensated-Tower program:

- `features/tower/operator_revenue_share.feature` — 98 scenarios: eligibility, attribution,
  funds verification, accrual, reserve, exposure cap, clawback, self-dealing, forfeiture.
- `features/tower/compensation_state_machines.feature` — 68 scenarios: the entitlement
  aggregate, payout lots, the double-entry journal, control totals, dust/debt/enforcement.
- `features/tower/payment_authority.feature` — 29 scenarios: provider-neutral authentication of
  external cash and payout-rail events; the zero-withholding tax authority.

**This is a financial subsystem, not a feature.** It is a hash-chained, double-entry,
serializable-CAS accounting ledger with canonical (JCS) hashing, sixteen closed journal accounts,
per-currency control totals replayable from source, and a provider-authenticated payment/rail/tax
authority. It is deliberately gated behind the founder/money approval and must be built
bottom-up, each layer proven before the next rests on it. **Nothing below moves money until the
disbursement layer, which is the last milestone and behind its own authorization.**

## The load-bearing invariant

Everything reduces to one program-cap invariant, checked independently of any per-operator logic:

```
T_N = Σ externally-funded net revenue N over all eligible candidates (share atoms)
T_C = Σ (N · rate_ppm)                        the policy ceiling
T_A = Σ A over all entitlement aggregates     what operators are owed
      T_A == T_C   and   0 ≤ T_A ≤ T_N
```

This is what makes wash-trading unprofitable (you are paid `rate_ppm` of *funded* revenue, capped
at what actually came in) and what a full source-replay must reproduce exactly.

## Milestones (dependency order)

| # | Milestone | Depends on | Moves money? | Status |
|---|---|---|---|---|
| **0** | **Arithmetic foundation** — checked int math, ppm share, reserve split, exposure cap, rate_ppm wire form (`internal/towercore/comp`) | — | no | **built (this slice)** |
| 1 | Canonical object + hashing kit — JCS canonicalization, `*V1` complete-hash, series-ID derivation, strict integer-string fields | 0 | no | next |
| 2 | Payment authority — webhook auth (bounded hint), authenticated provider fetch → `AuthoritativePaymentRevisionV1`, fee-finality, push/pull reconciliation | 1 | no | |
| 3 | Eligibility — `CompensatedTowerCapabilityV1`, the six fact heads, `GrantCompensationSnapshotV1` at grant issue, `SettlementReceiptV2` candidate (eligible only with external-cash lineage) | 1,2 | no | |
| 4 | Entitlement aggregate — the CAS state machine (absent → pending_reconciliation → current_zero/positive → conflict_quarantined), signed deltas, the T_N/T_C/T_A cap as an independent backstop | 1,2,3 | no | |
| 5 | Double-entry journal + control totals — the 16 closed accounts, balanced postings per event template, per-currency `CompensationControlTotalLeafV1`, full source replay | 4 | no (accounting only) | |
| 6 | Payout lots + reserve + dust + debt — lot state machine, rolling reserve, below-threshold dust cycles, operator-scoped `DebtRangeV1`, offset-before-accrual | 5 | no | |
| 7 | Tax authority — `TaxProfileFactV1`, `TaxWithholdingDecisionV1`, applicability anchoring, post-send correction incidents | 1,2,6 | no | |
| 8 | Payout preparation + send fence — prepare → attest → instruction → irreversible send fence; rail-result reconciliation; clawback/enforcement coverage | 6,7 | **yes (rails)** | last, own gate |

Milestones 1–7 are all money-*safe* (they decide numbers; only 8 disburses). Self-dealing's
account-level floor is already built in `internal/towercore/earnings` (own-account traffic earns
nothing); the sybil/funded-work defence is milestones 2–4.

## Open questions to pin before milestone 4

- **`T_C` exact formula.** The state-machine spec says "the checked sum of each current N
  multiplied by that candidate's grant-bound rate_ppm"; the reserve/rate scenarios apply ppm as
  `floor(x · ppm / 1e6)`. Confirm whether `T_C` carries the `/1e6` per candidate (entitlement
  atoms) or is a ppm-scaled control quantity divided once — this changes the atom accounting and
  must be settled against a full reading of both specs before any aggregate math is written.
- **Share-atom scale vs currency minor unit.** `N` is "expressed as 1000000 share atoms"; nail the
  relationship between share atoms, the currency's minor unit, and each rail's minor unit (the tax
  decision speaks of "accounting quanta per rail minor unit").

## Why milestone 0 first

The arithmetic is the one part that is fully unambiguous, universally used, and impossible to get
"partly right" safely — a single silent wrap or lost atom anywhere above it is a wrong payout. It
is pure and exhaustively testable, so it is the floor the rest can stand on. It is built and wired
(the earnings ledger and the accrual path use its checked ops); the ppm/reserve/cap functions are
allow-listed as foundation-ahead-of-consumer until milestone 4 consumes them.
