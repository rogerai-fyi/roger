package main

// The broker halves of features/curated/curated_web.feature: the owner's station
// roll-up and the consumer's usage history must carry the curated designation, so the
// web surfaces can label a proxy apart from human service (and pass-through apart from
// income) without guessing from prices.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

func curatedWebFixture(t *testing.T) (*broker, *store.Mem, string) {
	t.Helper()
	db := store.NewMem()
	b := relayBroker(db)
	ownerPub, _, _ := ed25519.GenerateKey(nil)
	acct := hex.EncodeToString(ownerPub)
	if err := db.BindOwner(store.Owner{GitHubID: 7, Login: "op", Pubkey: acct}); err != nil {
		t.Fatal(err)
	}
	addNode := func(id string, curated bool) {
		nodePub, _, _ := ed25519.GenerateKey(nil)
		reg := protocol.NodeRegistration{
			NodeID: id, PubKey: hex.EncodeToString(nodePub), BridgeToken: "tok",
			Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 2, Ctx: 4096}},
		}
		if curated {
			reg.Curated, reg.CuratedProvider = true, "openrouter"
			reg.Region = "openrouter"
		}
		b.mu.Lock()
		b.nodes[id] = reg
		b.lastSeen[id] = time.Now()
		b.mu.Unlock()
		if err := db.UpsertNode(store.NodeRecord{NodeID: id, Reg: reg, RegisteredAt: time.Now().Unix()}); err != nil {
			t.Fatal(err)
		}
		if err := db.BindNode(id, acct); err != nil {
			t.Fatal(err)
		}
	}
	addNode("cur1", true)
	addNode("hum1", false)
	return b, db, acct
}

func TestStationsRollupLabelsCuratedApart(t *testing.T) {
	b, _, _ := curatedWebFixture(t)
	views := map[string]stationView{}
	recs := map[string]store.NodeRecord{}
	if all, err := b.db.AllNodes(); err == nil {
		for _, rec := range all {
			recs[rec.NodeID] = rec
		}
	}
	for _, id := range []string{"cur1", "hum1"} {
		views[id] = b.stationView(id, recs[id])
	}
	if !views["cur1"].Curated || views["cur1"].CuratedProvider != "openrouter" {
		t.Fatalf("the curated station is unlabeled in the owner roll-up: %+v", views["cur1"])
	}
	if views["hum1"].Curated || views["hum1"].CuratedProvider != "" {
		t.Fatalf("a human station carries a curated label: %+v", views["hum1"])
	}
}

func TestUsageHistoryNamesTheCuratedProviderAndSplit(t *testing.T) {
	b, db, _ := curatedWebFixture(t)
	// One curated settlement (posted 1.30, pass-through 1.00) and one human (0.10/0.07).
	if _, err := db.Settle("user-w", "cur1", 1.30, 1.00, protocol.UsageReceipt{RequestID: "rq-c", NodeID: "cur1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Settle("user-w", "hum1", 0.10, 0.07, protocol.UsageReceipt{RequestID: "rq-h", NodeID: "hum1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/usage", nil)
	w := httptest.NewRecorder()
	b.usageFor(w, r, "user-w")
	if w.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Recent []struct {
			RequestID       string  `json:"request_id"`
			Model           string  `json:"model"`
			Cost            float64 `json:"cost"`
			OwnerShare      float64 `json:"owner_share"`
			CuratedProvider string  `json:"curated_provider"`
		} `json:"recent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byReq := map[string]string{}
	for _, row := range out.Recent {
		byReq[row.RequestID] = row.CuratedProvider
		if row.RequestID == "rq-c" && (row.Cost != 1.30 || row.OwnerShare != 1.00) {
			t.Fatalf("the split is not visible on the curated row: %+v", row)
		}
	}
	if byReq["rq-c"] != "openrouter" {
		t.Fatalf("the curated row does not name its provider: %+v", out.Recent)
	}
	if byReq["rq-h"] != "" {
		t.Fatal("a human row carries a curated provider")
	}
}
