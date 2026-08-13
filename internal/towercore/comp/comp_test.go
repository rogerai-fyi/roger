package comp

// comp_test.go exhaustively exercises the compensation arithmetic, including the two boundary
// tables from operator_revenue_share.feature verbatim (rate wire validation and the exposure
// cap), because a money system's arithmetic is exactly where an off-by-one becomes a payout.

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePPMCanonicalBoundary(t *testing.T) {
	// The spec's "Compensation rate wire validation covers the complete canonical boundary"
	// Examples, as the decoded string value of "rate_ppm". JSON-number, null, and absent fixtures
	// never reach ParsePPM (they are not strings), so they are asserted at the decode layer's
	// callers; here we cover every STRING fixture plus the canonical accepts.
	for _, tc := range []struct {
		in    string
		ok    bool
		value uint32
	}{
		{"0", true, 0},
		{"1", true, 1},
		{"100000", true, 100000},
		{"1000000", true, 1000000},
		{"999999", true, 999999},
		{"-1", false, 0},
		{"1000001", false, 0},
		{"1.0", false, 0},
		{"01", false, 0},
		{"+1", false, 0},
		{"1e6", false, 0},
		{"", false, 0},
		{" 1", false, 0},
		{"1 ", false, 0},
		{"9223372036854775808", false, 0},  // rejected before bounded conversion (form)
		{"18446744073709551616", false, 0}, // rejected before bounded conversion (form)
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParsePPM(tc.in)
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, tc.value, got)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestApplyPPMFloorsAndIsOverflowSafe(t *testing.T) {
	// Ten percent of 1000 is 100; floor of 999*100000/1e6 = 99 (99.9 floored).
	got, err := ApplyPPM(1000, 100000)
	require.NoError(t, err)
	require.Equal(t, int64(100), got)

	got, err = ApplyPPM(999, 100000)
	require.NoError(t, err)
	require.Equal(t, int64(99), got, "floored, never rounded up")

	// Zero rate and zero atoms are zero; full rate is identity.
	z, err := ApplyPPM(12345, 0)
	require.NoError(t, err)
	require.Equal(t, int64(0), z)
	full, err := ApplyPPM(12345, PPMScale)
	require.NoError(t, err)
	require.Equal(t, int64(12345), full)

	// Overflow-safe at the extreme: MaxInt64 * full rate is exact (<= atoms), not a wrap.
	big, err := ApplyPPM(math.MaxInt64, PPMScale)
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64), big)
	half, err := ApplyPPM(math.MaxInt64, 500000)
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64/2), half, "no 128-bit-intermediate overflow")

	_, err = ApplyPPM(-1, 1)
	require.ErrorIs(t, err, ErrNegative)
	_, err = ApplyPPM(1, PPMScale+1)
	require.Error(t, err, "a rate above 100% is refused")
}

func TestReserveSplitConservesEveryAtom(t *testing.T) {
	for _, e := range []int64{0, 1, 7, 1000, 999, math.MaxInt64} {
		for _, ppm := range []uint32{0, 1, 250000, 333333, PPMScale} {
			held, payable, err := ReserveSplit(e, ppm)
			require.NoError(t, err)
			require.Equal(t, e, held+payable, "held+payable must equal E exactly (e=%d ppm=%d)", e, ppm)
			require.GreaterOrEqual(t, held, int64(0))
			require.GreaterOrEqual(t, payable, int64(0))
		}
	}
}

func TestAccrueUnderCapExamples(t *testing.T) {
	// The exposure-cap Examples table, verbatim.
	for _, tc := range []struct {
		cap, current, accrual int64
		accrued, withheld     int64
	}{
		{1000, 0, 100, 100, 0},     // accrues in full
		{1000, 900, 100, 100, 0},   // accrues in full and reaches the cap exactly
		{1000, 900, 200, 100, 100}, // accrues 100, withholds 100
		{1000, 1000, 50, 0, 50},    // accrues zero, withholds 50
		{1000, 1200, 50, 0, 50},    // over-cap never inverts: room floored at 0
	} {
		accrued, withheld, err := AccrueUnderCap(tc.current, tc.cap, tc.accrual)
		require.NoError(t, err)
		require.Equal(t, tc.accrued, accrued, "accrued for cap=%d current=%d accrual=%d", tc.cap, tc.current, tc.accrual)
		require.Equal(t, tc.withheld, withheld, "withheld for cap=%d current=%d accrual=%d", tc.cap, tc.current, tc.accrual)
		require.Equal(t, tc.accrual, accrued+withheld, "no atom created or lost")
	}
}

func TestCheckedArithmetic(t *testing.T) {
	s, err := CheckedAdd(2, 3)
	require.NoError(t, err)
	require.Equal(t, int64(5), s)
	_, err = CheckedAdd(math.MaxInt64, 1)
	require.ErrorIs(t, err, ErrOverflow)
	_, err = CheckedAdd(-1, 1)
	require.ErrorIs(t, err, ErrNegative)

	d, err := CheckedSub(5, 3)
	require.NoError(t, err)
	require.Equal(t, int64(2), d)
	_, err = CheckedSub(3, 5)
	require.ErrorIs(t, err, ErrNegative, "money never goes below zero")

	p, err := CheckedMul(1_000_000, 1_000_000)
	require.NoError(t, err)
	require.Equal(t, int64(1_000_000_000_000), p)
	_, err = CheckedMul(math.MaxInt64, 2)
	require.ErrorIs(t, err, ErrOverflow)
	z, err := CheckedMul(0, math.MaxInt64)
	require.NoError(t, err)
	require.Equal(t, int64(0), z)

	sum, err := CheckedSum([]int64{1, 2, 3, 4})
	require.NoError(t, err)
	require.Equal(t, int64(10), sum)
	_, err = CheckedSum([]int64{math.MaxInt64, 1})
	require.ErrorIs(t, err, ErrOverflow)
	empty, err := CheckedSum(nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), empty)
}

// Every operation refuses a negative operand rather than absorbing it - a negative accounting
// amount is an upstream bug, and a money layer that silently accepted one would hide it.
func TestNegativeOperandsAreRefused(t *testing.T) {
	_, err := CheckedSub(-1, 0)
	require.ErrorIs(t, err, ErrNegative)
	_, err = CheckedMul(-1, 2)
	require.ErrorIs(t, err, ErrNegative)
	_, _, err = ReserveSplit(-1, 0)
	require.ErrorIs(t, err, ErrNegative)
	_, _, err = ReserveSplit(100, PPMScale+1)
	require.Error(t, err, "a reserve rate above 100% is refused")
	_, _, err = AccrueUnderCap(-1, 0, 0)
	require.ErrorIs(t, err, ErrNegative)
	_, _, err = AccrueUnderCap(0, -1, 0)
	require.ErrorIs(t, err, ErrNegative)
	_, _, err = AccrueUnderCap(0, 0, -1)
	require.ErrorIs(t, err, ErrNegative)
}
