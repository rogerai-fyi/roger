package store

import (
	"testing"
	"time"
)

// TestModerationFlagsParity pins the moderation_flags surface the off-path screener writes
// (features/moderation/off_path_screening.feature §2: a block-net verdict is RECORDED against
// the consumer pseudonym, never enforced) on the Mem store AND, when ROGERAI_TEST_DATABASE_URL
// is set, the real Postgres store: insert returns an id, the lookup is pseudonym-scoped,
// newest-first, honors `since` (the once-per-day repeat-flag window) and `limit`, and the
// sealed window round-trips byte-for-byte (it is ciphertext; the store never inspects it).
func TestModerationFlagsParity(t *testing.T) {
	now := time.Now().Unix() // real now: the zero-CreatedAt row below is stamped with it
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			mk := func(pseud, cat string, at int64) ModerationFlag {
				return ModerationFlag{Pseudonym: pseud, RequestID: "req-" + cat, Model: "gpt-oss-120b", Node: "n1",
					Category: cat, Window: []byte("SEALED-" + cat), CreatedAt: at}
			}
			id1, err := db.AddModerationFlag(mk("p1", "S1", now-3600))
			if err != nil || id1 == 0 {
				t.Fatalf("AddModerationFlag: id=%d err=%v", id1, err)
			}
			id2, err := db.AddModerationFlag(mk("p1", "S5", now-60))
			if err != nil || id2 <= id1 {
				t.Fatalf("second flag: id=%d (prev %d) err=%v", id2, id1, err)
			}
			if _, err := db.AddModerationFlag(mk("p2", "S3", now)); err != nil {
				t.Fatal(err)
			}
			// A zero CreatedAt is stamped now (not stored as 0).
			idZ, err := db.AddModerationFlag(ModerationFlag{Pseudonym: "p3", Category: "S6"})
			if err != nil || idZ == 0 {
				t.Fatalf("zero-time flag: %v", err)
			}
			z, _ := db.ModerationFlagsByPseudonym("p3", 0, 10)
			if len(z) != 1 || z[0].CreatedAt <= 0 {
				t.Fatalf("zero CreatedAt must be stamped: %+v", z)
			}

			all, err := db.ModerationFlagsByPseudonym("p1", 0, 10)
			if err != nil || len(all) != 2 {
				t.Fatalf("p1 flags = %d (%v), want 2", len(all), err)
			}
			if all[0].Category != "S5" || all[1].Category != "S1" {
				t.Fatalf("want newest first, got %s,%s", all[0].Category, all[1].Category)
			}
			if string(all[0].Window) != "SEALED-S5" || all[0].RequestID != "req-S5" || all[0].Model != "gpt-oss-120b" || all[0].Node != "n1" || all[0].ID != id2 {
				t.Fatalf("flag fields did not round-trip: %+v", all[0])
			}
			recent, _ := db.ModerationFlagsByPseudonym("p1", now-600, 10)
			if len(recent) != 1 || recent[0].Category != "S5" {
				t.Fatalf("since filter: got %+v", recent)
			}
			lim, _ := db.ModerationFlagsByPseudonym("p1", 0, 1)
			if len(lim) != 1 {
				t.Fatalf("limit 1: got %d", len(lim))
			}
			other, _ := db.ModerationFlagsByPseudonym("p2", 0, 10)
			if len(other) != 1 || other[0].Category != "S3" {
				t.Fatalf("pseudonym scoping leaked: %+v", other)
			}
			none, err := db.ModerationFlagsByPseudonym("nobody", 0, 10)
			if err != nil || len(none) != 0 {
				t.Fatalf("unknown pseudonym: %v %v", none, err)
			}

			// Retention: rows at or before the horizon go, newer ones stay; idempotent.
			n, err := db.PurgeModerationFlags(time.Unix(now-3600, 0))
			if err != nil || n != 1 {
				t.Fatalf("PurgeModerationFlags = %d, %v; want 1 (the S1 row at exactly the horizon)", n, err)
			}
			left, _ := db.ModerationFlagsByPseudonym("p1", 0, 10)
			if len(left) != 1 || left[0].Category != "S5" {
				t.Fatalf("after purge p1 = %+v, want only S5", left)
			}
			if n, err := db.PurgeModerationFlags(time.Unix(now-3600, 0)); err != nil || n != 0 {
				t.Fatalf("second purge = %d, %v; want 0", n, err)
			}
		})
	}
}
