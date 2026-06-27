package main

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// strikeBroker builds a broker with a node bound to an owner account and the strike
// thresholds set, so b.strike's escalation ladder can be driven directly.
func strikeBroker(t *testing.T, warn, ban, corroborate int) (*broker, store.Store) {
	t.Helper()
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 1, Login: "op", Pubkey: "acct1"})
	_ = db.BindNode("n", "acct1")
	b := &broker{
		db: db, bannedOwners: map[string]bool{},
		strikeWarnAt: warn, strikeBanAt: ban, strikeCorroborateKinds: corroborate,
	}
	return b, db
}

// TestStrikeWarningDoesNotBan locks the warn rung: hitting the warn count with a single
// signal class freezes earnings (recount hold) but does NOT ban the owner.
func TestStrikeWarningDoesNotBan(t *testing.T) {
	b, db := strikeBroker(t, 2, 5, 2)
	b.strike("n", store.StrikeRecountDiscrepancy, "k1", false, map[string]any{"axis": "output"})
	b.strike("n", store.StrikeRecountDiscrepancy, "k2", false, map[string]any{"axis": "output"})
	if banned, _, _ := db.IsOwnerBanned("acct1"); banned {
		t.Error("a warn-level single-class strike must NOT ban")
	}
	if strikes, _ := db.StrikesByOwner("acct1", 0); len(strikes) != 2 {
		t.Errorf("strikes recorded = %d, want 2", len(strikes))
	}
}

// TestStrikeAtBanCountButOneClassHeldNotBanned locks the corroboration guard: at/above the
// ban count but with only ONE distinct signal class, the owner is held (frozen) but NOT
// durably banned.
func TestStrikeAtBanCountButOneClassHeldNotBanned(t *testing.T) {
	b, db := strikeBroker(t, 1, 2, 2)
	b.strike("n", store.StrikeRecountDiscrepancy, "k1", false, map[string]any{"axis": "output"})
	b.strike("n", store.StrikeRecountDiscrepancy, "k2", false, map[string]any{"axis": "output"})
	if banned, _, _ := db.IsOwnerBanned("acct1"); banned {
		t.Error("a single-class count at ban threshold must be HELD, not banned (corroboration guard)")
	}
}

// TestStrikeCorroboratedBans locks the accumulating ban: enough recent strikes across
// MORE THAN ONE distinct signal class durably bans the owner.
func TestStrikeCorroboratedBans(t *testing.T) {
	b, db := strikeBroker(t, 1, 2, 2)
	b.strike("n", store.StrikeRecountDiscrepancy, "k1", false, map[string]any{"axis": "output"})
	b.strike("n", store.StrikeEmptyOutput, "k2", false, map[string]any{"axis": "output"})
	if banned, _, _ := db.IsOwnerBanned("acct1"); !banned {
		t.Error("a corroborated (2-class) count at ban threshold must durably ban the owner")
	}
	if !b.isOwnerBanned("acct1") {
		t.Error("the in-memory owner-ban cache must reflect the ban")
	}
}

// TestStrikeZeroDoubtBansImmediately locks the impossible-input path: a zero-doubt strike
// bans on the FIRST strike, bypassing decay + corroboration.
func TestStrikeZeroDoubtBansImmediately(t *testing.T) {
	b, db := strikeBroker(t, 3, 5, 2)
	b.strike("n", store.StrikeImpossibleInput, "k1", true, map[string]any{"claimed": 100, "bytes": 1})
	if banned, _, _ := db.IsOwnerBanned("acct1"); !banned {
		t.Error("a zero-doubt strike must ban on the first strike")
	}
}
