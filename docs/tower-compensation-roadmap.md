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
N     = one candidate's externally funded GROSS revenue, in accounting quanta
T_N   = Σ (N · 1e6)          the same revenue expressed in share atoms
T_C   = Σ (N · rate_ppm)     the policy ceiling, already in share atoms
T_A   = Σ A                  what operators are owed, in share atoms
        T_A == T_C   and   0 ≤ T_A ≤ T_N
```

One share atom is one millionth of one accounting quantum, so multiplying a quantum-valued `N`
by `rate_ppm` yields atoms directly - no divisor, no rounding. Because `rate_ppm ≤ 1e6` is
enforced at parse and at application, `T_C ≤ T_N` term by term, which makes the cap structural
rather than a check that could be forgotten.

This is what makes wash-trading unprofitable (you are paid `rate_ppm` of *funded* revenue, capped
at what actually came in) and what a full source-replay must reproduce exactly. The basis is
GROSS externally funded revenue, met from the platform's own margin (founder, 2026-08-17).

## Milestones (dependency order)

| # | Milestone | Depends on | Moves money? | Status |
|---|---|---|---|---|
| **0** | **Arithmetic foundation** — checked int math, ppm share, reserve split, exposure cap, rate_ppm wire form (`internal/towercore/comp`) | — | no | **built (this slice)** |
| 1 | Canonical object + hashing kit — JCS canonicalization, `*V1` complete-hash, series-ID derivation, strict integer-string fields | 0 | no | **built** — `internal/towerobj`: one canonical writer (`Canonical`/`CanonicalList`), `Hash` complete-hash, `HashList` for `strict JCS [tag, network, id, revision]` series IDs, `Sign`/`Verify` with a named signature member, and `ParseInt`/`FormatInt` for canonical integer strings. Proven in production by the attempt ledger, which derives its event and commitment IDs through it |
| 2 | Payment authority — **next** — webhook auth (bounded hint), authenticated provider fetch → `AuthoritativePaymentRevisionV1`, fee-finality, push/pull reconciliation | 1 | no | |
| 3 | Eligibility — `CompensatedTowerCapabilityV1`, the six fact heads, `GrantCompensationSnapshotV1` at grant issue, `SettlementReceiptV2` candidate (eligible only with external-cash lineage) | 1,2 | no | |
| 4 | Entitlement aggregate — the CAS state machine (absent → pending_reconciliation → current_zero/positive → conflict_quarantined), signed deltas, the T_N/T_C/T_A cap as an independent backstop | 1,2,3 | no | |
| 5 | Double-entry journal + control totals — the 16 closed accounts, balanced postings per event template, per-currency `CompensationControlTotalLeafV1`, full source replay | 4 | no (accounting only) | |
| 6 | Payout lots + reserve + dust + debt — lot state machine, rolling reserve, below-threshold dust cycles, operator-scoped `DebtRangeV1`, offset-before-accrual | 5 | no | |
| 7 | Tax authority — `TaxProfileFactV1`, `TaxWithholdingDecisionV1`, applicability anchoring, post-send correction incidents | 1,2,6 | no | |
| 8 | Payout preparation + send fence — prepare → attest → instruction → irreversible send fence; rail-result reconciliation; clawback/enforcement coverage | 6,7 | **yes (rails)** | last, own gate |

Milestones 1–7 are all money-*safe* (they decide numbers; only 8 disburses). Self-dealing's
account-level floor is already built in `internal/towercore/earnings` (own-account traffic earns
nothing); the sybil/funded-work defence is milestones 2–4.

## Questions that were open before milestone 4 — both now settled from the specs

Neither needed a ruling. The answers were already written in
`operator_revenue_share.feature`'s "Compensation math uses fixed-point cumulative rounding",
which the earlier reading of this roadmap had not cross-referenced.

- **`T_C` carries no divisor, because the units do the work.** `N` in the ceiling is the
  **quantum**-valued net figure, and `share atoms = checked_multiply(N_quanta, rate_ppm)` — the
  ppm multiply *is* the conversion from quanta to atoms, since one atom is one millionth of one
  quantum. `T_N` is that same `N` expressed in atoms (`N · 1e6`), so `T_A == T_C` compares atoms
  with atoms, and `T_A ≤ T_N` reduces to `ppm ≤ 1e6` — which `ParsePPM` and `ApplyPPM` both
  enforce. The invariant is therefore **structural**, not a runtime hope.
- **There is no per-candidate rounding.** The spec requires exact atoms "retained through
  aggregation without per-event rounding", and an integer multiply is exact, so nothing is
  floored on the way in. `ApplyPPM` (which floors) is the primitive for taking a percentage OF
  ATOMS — the rolling reserve — and is the wrong one for entitlement accrual, whose primitive is
  `CheckedMul`. Getting this backwards would silently under-pay every operator by up to one atom
  per settled attempt and break `T_A == T_C` exactness.
- **Scale.** One share atom is exactly one millionth of one accounting quantum (the currency's
  minor unit). Rounding happens **once**, at the payout boundary, converting atoms to the rail's
  minor unit by flooring, with the remainder left as unpaid entitlement — which is what the dust
  cycles exist for, and why payout lots are a separate state machine from entitlement.

### The one genuinely open item

**Quantum ↔ rail minor unit is not pinned.** For USD both are the cent, and it is tempting to
assume 100 everywhere. A zero-decimal currency (JPY) has no minor unit at all, and a rail may
quote a different scale again. The tax decision's "accounting quanta per rail minor unit" needs
to be an explicit per-currency, per-rail value carried in policy, not a constant — settle this
in milestone 2 (payment authority), where rail scope is already part of the adapter identity.

## Why milestone 0 first

The arithmetic is the one part that is fully unambiguous, universally used, and impossible to get
"partly right" safely — a single silent wrap or lost atom anywhere above it is a wrong payout. It
is pure and exhaustively testable, so it is the floor the rest can stand on. It is built and wired
(the earnings ledger and the accrual path use its checked ops); the ppm/reserve/cap functions are
allow-listed as foundation-ahead-of-consumer until milestone 4 consumes them.
