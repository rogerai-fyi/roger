// Package comp is the canonical integer arithmetic the compensated-Tower program is built on.
//
// Contract: features/tower/operator_revenue_share.feature (the share-rate wire form, the reserve
// split, the exposure cap, and the checked integer arithmetic those rest on).
//
// # WHY A SEPARATE ARITHMETIC PACKAGE
//
// The compensation specs invoke the SAME small vocabulary of money math on every page - a checked
// add/subtract/multiply that refuses to overflow, a parts-per-million share applied by floor, a
// reserve split that conserves every atom, an exposure cap that never inverts. A money system
// gets these wrong in exactly one way (a silent wrap, a lost atom, a negative that floors to
// zero and hides a debt), so they live in one tested place rather than being re-derived at each
// call site. Everything here is PURE: no time, no randomness, no database, no money movement -
// just integers that behave.
//
// # UNITS
//
// Amounts are "atoms" - the smallest indivisible accounting unit, a non-negative int64. A share
// is parts-per-million (ppm): 100000 ppm is ten percent, 1000000 ppm is one hundred percent.
package comp

import (
	"errors"
	"math"
	"math/bits"
)

// PPMScale is the denominator of a parts-per-million share. 100000/PPMScale is ten percent.
const PPMScale = 1_000_000

var (
	// ErrOverflow is a sum or product that would exceed the int64 range. In a money system this
	// is never wrapped or saturated silently at the arithmetic layer - the caller decides whether
	// to quarantine (the ledger) or saturate-and-log (a best-effort accrual).
	ErrOverflow = errors.New("comp: integer overflow")
	// ErrNegative is a negative operand. Every accounting amount here is non-negative; a negative
	// is a bug upstream, surfaced rather than absorbed.
	ErrNegative = errors.New("comp: negative amount")
)

func nonneg(xs ...int64) error {
	for _, x := range xs {
		if x < 0 {
			return ErrNegative
		}
	}
	return nil
}

// CheckedAdd returns a+b, or ErrOverflow if it would exceed MaxInt64. Operands must be
// non-negative.
func CheckedAdd(a, b int64) (int64, error) {
	if err := nonneg(a, b); err != nil {
		return 0, err
	}
	if a > math.MaxInt64-b {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// CheckedSub returns a-b, or ErrNegative if b>a. Money here never goes below zero, so an
// under-run is an error to surface, not a value to floor.
func CheckedSub(a, b int64) (int64, error) {
	if err := nonneg(a, b); err != nil {
		return 0, err
	}
	if b > a {
		return 0, ErrNegative
	}
	return a - b, nil
}

// CheckedMul returns a*b, or ErrOverflow if it would exceed MaxInt64. Operands must be
// non-negative.
func CheckedMul(a, b int64) (int64, error) {
	if err := nonneg(a, b); err != nil {
		return 0, err
	}
	if a == 0 || b == 0 {
		return 0, nil
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return int64(lo), nil
}

// CheckedSum adds a slice with overflow checked at every step - the specs' "checked sum of every
// candidate's N" and "checked sum of each N multiplied by rate_ppm".
func CheckedSum(xs []int64) (int64, error) {
	var total int64
	for _, x := range xs {
		var err error
		if total, err = CheckedAdd(total, x); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// ApplyPPM returns floor(atoms * ppm / 1_000_000), the canonical way the specs turn a net-revenue
// figure into an entitlement (rate_ppm) or an entitlement into a reserve (reserve_ppm). It is
// overflow-safe: the intermediate product is held in 128 bits, so it is exact for any
// non-negative int64 atoms and any ppm in [0, PPMScale]. Because ppm <= PPMScale the result is
// always <= atoms and therefore always fits int64.
func ApplyPPM(atoms int64, ppm uint32) (int64, error) {
	if err := nonneg(atoms); err != nil {
		return 0, err
	}
	if ppm > PPMScale {
		return 0, errors.New("comp: ppm above one hundred percent")
	}
	if atoms == 0 || ppm == 0 {
		return 0, nil
	}
	hi, lo := bits.Mul64(uint64(atoms), uint64(ppm))
	// hi < PPMScale always (max atoms*ppm is MaxInt64*1e6 < 2^64 * 1e6, so the high word is well
	// under the divisor), so Div64 cannot overflow its quotient; guard anyway rather than trust it.
	if hi >= PPMScale {
		return 0, ErrOverflow
	}
	q, _ := bits.Div64(hi, lo, PPMScale)
	if q > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return int64(q), nil
}

// ReserveSplit divides an entitlement E into the reserve held back and the remainder payable now,
// with EXACT conservation: held + payable == e, no atom created or lost. held = floor(E * ppm /
// 1_000_000), the spec's rolling reserve.
func ReserveSplit(e int64, reservePpm uint32) (held, payable int64, err error) {
	held, err = ApplyPPM(e, reservePpm)
	if err != nil {
		return 0, 0, err
	}
	payable, err = CheckedSub(e, held) // held <= e since reservePpm <= PPMScale
	if err != nil {
		return 0, 0, err
	}
	return held, payable, nil
}

// AccrueUnderCap splits a proposed accrual against a per-operator exposure cap: the part that
// fits under the cap accrues, the rest is withheld (deferred, not forfeited). An already
// over-cap balance accrues nothing and never inverts - room is floored at zero, never negative.
// This is the exposure-cap Examples table in operator_revenue_share.feature.
func AccrueUnderCap(current, cap, accrual int64) (accrued, withheld int64, err error) {
	if err := nonneg(current, cap, accrual); err != nil {
		return 0, 0, err
	}
	room := int64(0)
	if cap > current {
		room = cap - current
	}
	if accrual <= room {
		return accrual, 0, nil
	}
	return room, accrual - room, nil
}
