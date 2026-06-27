package main

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestSweepInterval covers the cadence clamp: ~1/24 of the window, floored at 1h and
// capped at 24h.
func TestSweepInterval(t *testing.T) {
	if got := sweepInterval(24 * time.Hour); got != time.Hour {
		t.Errorf("sweepInterval(24h) = %s, want 1h", got)
	}
	if got := sweepInterval(2 * time.Hour); got != time.Hour { // 2h/24 < 1h -> floor
		t.Errorf("sweepInterval(2h) = %s, want 1h floor", got)
	}
	if got := sweepInterval(120 * 24 * time.Hour); got != 24*time.Hour { // capped
		t.Errorf("sweepInterval(120d) = %s, want 24h cap", got)
	}
	if got := sweepInterval(48 * time.Hour); got != 2*time.Hour { // 48h/24 = 2h
		t.Errorf("sweepInterval(48h) = %s, want 2h", got)
	}
}

// TestNodeBanSweepOnce covers the per-iteration auto-lift: a report-threshold ban older
// than the cutoff is lifted from the store AND the in-memory ban cache; a manual ban
// survives.
func TestNodeBanSweepOnce(t *testing.T) {
	mem := store.NewMem()
	_ = mem.BanNode("n-report", "report threshold")
	_ = mem.BanNode("n-manual", "manual abuse")
	b := &broker{db: mem, nodeBanDays: 3, banned: map[string]bool{"n-report": true, "n-manual": true}}

	b.nodeBanSweepOnce(time.Now().Add(time.Hour)) // cutoff in the future -> the report ban is "old"

	if b.banned["n-report"] {
		t.Error("the report-origin ban should be lifted from the cache")
	}
	if !b.banned["n-manual"] {
		t.Error("a manual ban must survive auto-lift")
	}
	bans, _ := mem.BannedNodes()
	if _, ok := bans["n-report"]; ok {
		t.Error("the report ban should be cleared from the store")
	}
}

// TestRecountHoldSweepOnce covers the per-iteration recount-hold expiry: a hold older
// than the cutoff clears so its earnings can promote again.
func TestRecountHoldSweepOnce(t *testing.T) {
	mem := store.NewMem()
	_ = mem.SetNodeRecountHold("n1", true)
	b := &broker{db: mem, recountHoldDays: 7}

	b.recountHoldSweepOnce(time.Now().Add(time.Hour)) // cutoff in the future -> the hold is "old"

	if held, _ := mem.RecountHeldNodes(); held["n1"] {
		t.Error("an expired recount hold should be cleared")
	}
}
