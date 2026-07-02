package store

// merge_wallet_test.go covers MergeWallet on BOTH backends via parityStores: Mem always, and a
// real Postgres when ROGERAI_TEST_DATABASE_URL is set (the money-path-on-real-Postgres bar,
// CLAUDE.md). MergeWallet moves the balance (and its unspent seed portion) from one wallet to
// another at dual-link time so a funded u_apple_ balance is not stranded by GitHub-wins.

import "testing"

func TestMergeWalletParity(t *testing.T) {
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			if _, err := db.AddCredits("from", 150); err != nil {
				t.Fatal(err)
			}
			moved, err := db.MergeWallet("from", "to")
			if err != nil {
				t.Fatalf("MergeWallet: %v", err)
			}
			if moved != 150 {
				t.Fatalf("moved = %.2f, want 150", moved)
			}
			// Source drained, destination credited.
			if b, _ := db.PeekBalance("from"); b != 0 {
				t.Errorf("from balance = %.2f, want 0", b)
			}
			if b, _ := db.PeekBalance("to"); b != 150 {
				t.Errorf("to balance = %.2f, want 150", b)
			}
			// Derived (ledger) balance agrees with the cached balance on both wallets - the
			// paired adjustment rows keep the drift check consistent.
			if d, _ := db.DeriveBalance("from"); d != 0 {
				t.Errorf("from derived = %.2f, want 0", d)
			}
			if d, _ := db.DeriveBalance("to"); d != 150 {
				t.Errorf("to derived = %.2f, want 150", d)
			}
			// Idempotent: a second merge of the drained source moves nothing.
			if again, err := db.MergeWallet("from", "to"); err != nil || again != 0 {
				t.Errorf("second merge moved=%.2f err=%v, want 0/nil (idempotent)", again, err)
			}
			// A no-op self-merge and an empty source both move 0.
			if m, err := db.MergeWallet("to", "to"); err != nil || m != 0 {
				t.Errorf("self-merge moved=%.2f err=%v, want 0/nil", m, err)
			}
			if m, err := db.MergeWallet("never-funded", "to"); err != nil || m != 0 {
				t.Errorf("empty-source merge moved=%.2f err=%v, want 0/nil", m, err)
			}
		})
	}
}
