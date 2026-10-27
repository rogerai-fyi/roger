package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// latereceipt_test.go - a result after its 504 (contract §14.10): recorded once at $0 on
// whichever instance it lands, within the grace; discarded after it, or when not expected.

func lateFixture(t *testing.T, db store.Store, shared sharedStore) (*broker, ed25519.PrivateKey) {
	t.Helper()
	b := relayBroker(db)
	b.shared = shared
	pub, priv, _ := ed25519.GenerateKey(nil)
	b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", PubKey: hex.EncodeToString(pub)}
	return b, priv
}

func lateRes(priv ed25519.PrivateKey, job string) protocol.JobResult {
	rec := protocol.UsageReceipt{RequestID: job, NodeID: "n1", User: "u", Model: "m", PromptTokens: 10, CompletionTokens: 5, PriceIn: 1, PriceOut: 1, TS: time.Now().Unix()}
	rec.SignNode(priv)
	return protocol.JobResult{ID: job, Status: 200, Receipt: rec}
}

func TestLateReceiptAcrossInstancesOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	db := store.NewMem()
	vsA, _ := newValkeyStore("redis://" + mr.Addr())
	vsB, _ := newValkeyStore("redis://" + mr.Addr())
	a, priv := lateFixture(t, db, vsA)
	bb, _ := lateFixture(t, db, vsB)
	bb.nodes["n1"] = a.nodes["n1"]

	a.expectLate("job1", lateTicket{Node: "n1", Payer: "w1", Model: "m"})
	res := lateRes(priv, "job1")
	raw, _ := json.Marshal(res)
	bb.lateResultRaw("n1", raw) // lands on the other instance
	bb.lateResult("n1", res)    // delivered twice: recorded once
	es, err := db.RecentByUser("w1", 10)
	require.NoError(t, err)
	require.Len(t, es, 1)
	require.Zero(t, es[0].Cost, "recorded at $0")
	require.Zero(t, es[0].OwnerShare, "the operator is not paid")

	a.lateResult("n1", lateRes(priv, "never-expected"))
	es, _ = db.RecentByUser("w1", 10)
	require.Len(t, es, 1, "a result nobody timed out on is discarded")
}

func TestLateReceiptGraceAndLocalFallback(t *testing.T) {
	db := store.NewMem()
	b, priv := lateFixture(t, db, newMemStore())
	t.Setenv("ROGERAI_LATE_RECEIPT_GRACE", "50ms")
	b.expectLate("job2", lateTicket{Node: "n1", Payer: "w2"})
	time.Sleep(80 * time.Millisecond)
	b.lateResult("n1", lateRes(priv, "job2"))
	es, _ := db.RecentByUser("w2", 10)
	require.Empty(t, es, "after the grace the late result is discarded")

	t.Setenv("ROGERAI_LATE_RECEIPT_GRACE", "5s")
	b.expectLate("job3", lateTicket{Node: "n1", Payer: "w2"})
	b.lateResult("n2", lateRes(priv, "job3"))
	b.lateResult("n1", lateRes(priv, "job3"))
	es, _ = db.RecentByUser("w2", 10)
	require.Empty(t, es, "only the node it was dispatched to can answer it, and the claim is spent")

	b.expectLate("job4", lateTicket{Node: "n1", Payer: "w2"})
	b.lateResult("n1", lateRes(priv, "job4"))
	es, _ = db.RecentByUser("w2", 10)
	require.Len(t, es, 1)

	t.Setenv("ROGERAI_LATE_RECEIPT_GRACE", "0")
	b.expectLate("job5", lateTicket{Node: "n1", Payer: "w2"})
	_, ok := b.takeLate("job5")
	require.False(t, ok, "a zero grace expects nothing")
}
