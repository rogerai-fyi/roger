package store

import (
	"testing"
)

// TestDefaultMonthlyCapInvalidEnv covers DefaultMonthlyCap's guard arms: an unparseable or
// negative value falls back to 0 (unlimited). The valid-value path is covered elsewhere.
func TestDefaultMonthlyCapInvalidEnv(t *testing.T) {
	t.Setenv("ROGERAI_DEFAULT_MONTHLY_CAP", "not-a-number")
	if c := DefaultMonthlyCap(); c != 0 {
		t.Errorf("unparseable cap = %v, want 0 (unlimited)", c)
	}
	t.Setenv("ROGERAI_DEFAULT_MONTHLY_CAP", "-5")
	if c := DefaultMonthlyCap(); c != 0 {
		t.Errorf("negative cap = %v, want 0 (unlimited)", c)
	}
}

// TestSetMonthlyCapNegativeIsUnlimited locks that a negative SetMonthlyCap is stored as 0
// (an explicit "unlimited" opt-out), and that an absent wallet resolves to the env default.
func TestSetMonthlyCapNegativeIsUnlimited(t *testing.T) {
	t.Setenv("ROGERAI_DEFAULT_MONTHLY_CAP", "100")
	m := NewMem()
	// Absent wallet -> env default.
	if c, _ := m.MonthlyCapOf("nobody"); c != 100 {
		t.Errorf("default cap = %v, want 100", c)
	}
	// Negative is clamped to 0 = unlimited, and is NOT re-defaulted from the env.
	if err := m.SetMonthlyCap("w", -42); err != nil {
		t.Fatal(err)
	}
	if c, _ := m.MonthlyCapOf("w"); c != 0 {
		t.Errorf("cap after SetMonthlyCap(-42) = %v, want 0 (clamped unlimited)", c)
	}
}

// TestOfferOverrideRoundTrip covers SetOfferOverride (incl. the lazy map init) and the
// (node,model)-keyed lookup on a fresh Mem store.
func TestOfferOverrideRoundTrip(t *testing.T) {
	m := NewMem()
	if _, ok, _ := m.OfferOverride("n", "gpt"); ok {
		t.Error("unset override should resolve ok=false")
	}
	if err := m.SetOfferOverride(OfferOverride{NodeID: "n", Model: "gpt", UpdatedAt: 7}); err != nil {
		t.Fatal(err)
	}
	ov, ok, err := m.OfferOverride("n", "gpt")
	if err != nil || !ok || ov.UpdatedAt != 7 {
		t.Errorf("OfferOverride = %+v ok=%v err=%v, want UpdatedAt 7", ov, ok, err)
	}
	// A different (node,model) key is independent.
	if _, ok, _ := m.OfferOverride("n", "other"); ok {
		t.Error("a different model key must not resolve to the set override")
	}
}

// TestLoadPayoutPolicyMinAndSchedule covers the ROGERAI_PAYOUT_MIN and
// ROGERAI_PAYOUT_SCHEDULE arms of LoadPayoutPolicy (the two least-exercised knobs).
func TestLoadPayoutPolicyMinAndSchedule(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_MIN", "12.5")
	t.Setenv("ROGERAI_PAYOUT_SCHEDULE", "weekly")
	p := LoadPayoutPolicy()
	if p.MinPayout != 12.5 {
		t.Errorf("MinPayout = %v, want 12.5", p.MinPayout)
	}
	if p.Schedule != "weekly" {
		t.Errorf("Schedule = %q, want weekly", p.Schedule)
	}
}

// TestModelKeyUnknownBucket covers modelKey's empty-name normalization: an empty model
// groups under "unknown" in the per-model metrics rollup.
func TestModelKeyUnknownBucket(t *testing.T) {
	m := NewMem()
	_ = m.BindNode("nmk", "acct-mk")
	if _, err := m.AddCredits("umk", 100); err != nil {
		t.Fatal(err)
	}
	serveAt(t, m, "umk", "nmk", "", 3, 4, 10, 7, 1000) // empty model id

	usage, err := m.UsageMetrics("umk", 0, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].Model != "unknown" {
		t.Fatalf("UsageMetrics = %+v, want one row keyed 'unknown'", usage)
	}
	prov, err := m.ProviderMetrics("acct-mk", 0, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if len(prov) != 1 || prov[0].Model != "unknown" {
		t.Fatalf("ProviderMetrics = %+v, want one row keyed 'unknown'", prov)
	}
}
