package payauth

import (
	"errors"
	"testing"
	"time"
)

func expectation() Expectation {
	return Expectation{Merchant: "acct_1", SourceID: "pi_1", Currency: "usd", Scale: 2}
}

func reading() Reading {
	return Reading{
		Adapter: "provider-x", Merchant: "acct_1", SourceID: "pi_1", SourceKind: KindPaymentIntent,
		Currency: "usd", Scale: 2,
		OriginalPrincipal: 10_000, CapturedPrincipal: 10_000, RefundedTotal: 0,
		FeeTotal: 320, FeeState: FeeFinal, Dispute: DisputeNone,
		ProviderRevision: 4, EventIDs: []string{"evt_1"},
	}
}

func TestAConsistentReadingCommitsOneRevision(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rev, err := Reconcile(expectation(), reading(), 1, now)
	if err != nil {
		t.Fatalf("a consistent reading was refused: %v", err)
	}
	if rev.Sequence != 1 || !rev.CommittedAt.Equal(now) {
		t.Fatalf("Core's own sequence and clock must stamp the revision: %+v", rev)
	}
	if got := rev.MatureCash(); got.Amount != 10_000 || got.Currency != "usd" || got.Scale != 2 {
		t.Fatalf("mature cash = %+v, want the captured principal with its currency and scale", got)
	}
	if !rev.CompensationEligible() {
		t.Fatal("final fees, no dispute, positive cash: this must be eligible")
	}
}

// FEES ARE NOT SUBTRACTED. Under the 10%-of-gross basis the operator's base is the externally
// funded cash itself; the processor fee is the platform's cost of collection.
func TestMatureCashIsGrossOfProcessorFees(t *testing.T) {
	r := reading()
	r.FeeTotal = 320
	rev, err := Reconcile(expectation(), r, 1, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if rev.MatureCash().Amount != 10_000 {
		t.Fatalf("fees must not reduce the base: got %d, want 10000", rev.MatureCash().Amount)
	}
}

// A flat dispute fee LARGER than the charge it landed on is legitimate - the revenue-share
// spec calls it out and books the excess as platform expense - so it must reconcile, not
// strand the source in pending.
func TestAFlatFeeLargerThanTheChargeStillReconciles(t *testing.T) {
	r := reading()
	r.CapturedPrincipal, r.OriginalPrincipal, r.FeeTotal = 500, 500, 1_500
	rev, err := Reconcile(expectation(), r, 1, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("a flat fee above principal must reconcile: %v", err)
	}
	if rev.MatureCash().Amount != 500 {
		t.Fatalf("and the operator's base is still the gross cash: got %d", rev.MatureCash().Amount)
	}
}

func TestRefundsReduceMatureCash(t *testing.T) {
	r := reading()
	r.RefundedTotal = 2_500
	rev, err := Reconcile(expectation(), r, 1, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if rev.MatureCash().Amount != 7_500 {
		t.Fatalf("mature cash = %d, want 7500", rev.MatureCash().Amount)
	}
}

// THE FAIL-CLOSED TABLE from the spec: every one leaves the source pending rather than
// committing a guessed value.
func TestAnInconsistentReadingCommitsNothing(t *testing.T) {
	prior := Revision{Reading: reading(), Sequence: 1}
	for _, tc := range []struct {
		defect string
		want   error
		exp    Expectation
		bend   func(*Reading)
	}{
		{"merchant mismatch", ErrMerchantMismatch, expectation(), func(r *Reading) { r.Merchant = "acct_other" }},
		{"source id mismatch", ErrSourceMismatch, expectation(), func(r *Reading) { r.SourceID = "pi_other" }},
		{"unexpected source kind", ErrUnexpectedKind, expectation(), func(r *Reading) { r.SourceKind = "subscription" }},
		{"currency mismatch", ErrCurrencyMismatch, expectation(), func(r *Reading) { r.Currency = "eur" }},
		{"scale mismatch", ErrCurrencyMismatch, expectation(), func(r *Reading) { r.Scale = 0 }},
		{"negative amount", ErrAmountRange, expectation(), func(r *Reading) { r.FeeTotal = -1 }},
		{"refund above capture", ErrRefundAboveCapture, expectation(), func(r *Reading) { r.RefundedTotal = 20_000 }},
		{"capture above authorized", ErrCaptureAboveAuth, expectation(), func(r *Reading) { r.CapturedPrincipal = 20_000 }},
		{"fee state outside the closed set", ErrFeeInconsistent, expectation(), func(r *Reading) {
			r.FeeState = "settled-ish"
		}},
		{"dispute state outside the closed set", ErrFeeInconsistent, expectation(), func(r *Reading) {
			r.Dispute = "maybe"
		}},
		{"missing event lineage", ErrNoLineage, expectation(), func(r *Reading) { r.EventIDs = nil }},
		{"provider revision rewind", ErrRevisionRewind, Expectation{
			Merchant: "acct_1", SourceID: "pi_1", Currency: "usd", Scale: 2, Prior: &prior,
		}, func(r *Reading) { r.ProviderRevision = 1 }},
		{"equal revision, different bytes", ErrRevisionForked, Expectation{
			Merchant: "acct_1", SourceID: "pi_1", Currency: "usd", Scale: 2, Prior: &prior,
		}, func(r *Reading) { r.CapturedPrincipal = 9_000 }},
	} {
		t.Run(tc.defect, func(t *testing.T) {
			r := reading()
			tc.bend(&r)
			if _, err := Reconcile(tc.exp, r, 2, time.Unix(1, 0)); !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.defect, err, tc.want)
			}
		})
	}
}

// PUSH VS PULL: the fetch decides, every time. The webhook's claim is not an input to any of
// these - it cannot be, because nothing in Hint carries an amount.
func TestThePullDecidesNotThePush(t *testing.T) {
	t.Run("push says captured, pull says authorization only", func(t *testing.T) {
		r := reading()
		r.CapturedPrincipal = 0 // authorized but not captured
		rev, err := Reconcile(expectation(), r, 1, time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		if rev.MatureCash().Amount != 0 || rev.CompensationEligible() {
			t.Fatal("a push claiming capture cannot create mature cash")
		}
	})
	t.Run("push says dispute won, pull says still open: payout held", func(t *testing.T) {
		r := reading()
		r.Dispute = DisputeOpen
		rev, err := Reconcile(expectation(), r, 1, time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		if !rev.PayoutHeld() || rev.CompensationEligible() {
			t.Fatal("an open dispute holds payout regardless of what the push claimed")
		}
	})
	t.Run("push says fee final, pull says pending: no positive delta", func(t *testing.T) {
		r := reading()
		r.FeeState = FeePending
		rev, err := Reconcile(expectation(), r, 1, time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		if rev.CompensationEligible() {
			t.Fatal("a pending fee can still move; accruing against it would mean clawing back " +
				"from an operator who did nothing wrong")
		}
	})
}

// The deadline comes from CORE's clock and cannot be pushed out by the provider.
func TestTheFeeDeadlineIsCoreAuthority(t *testing.T) {
	capture := time.Unix(1_700_000_000, 0)
	d, err := FeeDeadline(capture, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Equal(capture.Add(72 * time.Hour)) {
		t.Fatalf("deadline = %v, want capture + interval", d)
	}
	if _, err := FeeDeadline(capture, 0); err == nil {
		t.Fatal("a non-positive interval is not a deadline")
	}
	if FeeDeadlineReached(d, d.Add(-time.Nanosecond)) {
		t.Fatal("not yet reached")
	}
	// Equality counts as reached, so two sweeps a tick apart cannot disagree about one instant.
	if !FeeDeadlineReached(d, d) {
		t.Fatal("exactly reached must count as reached")
	}
	if !FeeDeadlineReached(d, d.Add(time.Hour)) {
		t.Fatal("already passed")
	}
}
