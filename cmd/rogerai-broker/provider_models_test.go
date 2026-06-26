package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// pmGet does a signed GET /provider/models for the owner `priv`, returning status + the
// decoded models rows.
func pmGet(t *testing.T, b *broker, priv ed25519.PrivateKey) (int, []providerModelRow) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/provider/models", nil)
	signReq(r, priv, nil)
	w := httptest.NewRecorder()
	b.providerModels(w, r)
	var resp struct {
		Models []providerModelRow `json:"models"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.Models
}

// pmSet does a signed POST /provider/models with the given body for owner `priv`.
func pmSet(t *testing.T, b *broker, priv ed25519.PrivateKey, body map[string]any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/provider/models", bytes.NewReader(raw))
	signReq(r, priv, raw)
	w := httptest.NewRecorder()
	b.providerModels(w, r)
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.Error.Message
}

// TestProviderModelsSetReadbackAndReregister: an owner sets a custom price + schedule,
// reads it back, the live offer reflects it AT ONCE, and a node RE-REGISTER (which
// re-supplies its own lower price) does NOT clobber the owner's override.
func TestProviderModelsSetReadbackAndReregister(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	if code, msg := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("register n1 = %d (%q), want 200", code, msg)
	}

	// SET an owner-authored price + a free night window.
	code, msg := pmSet(t, b, userPriv, map[string]any{
		"node": "n1", "model": "m",
		"price_in": 2.0, "price_out": 3.0,
		"schedule": []map[string]any{
			{"start": "00:00", "end": "06:00", "free": true},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("set = %d (%q), want 200", code, msg)
	}

	// The LIVE in-memory offer reflects the override immediately (effective at serve time).
	b.mu.Lock()
	off := b.nodes["n1"].Offers[0]
	b.mu.Unlock()
	if off.PriceOut != 3.0 || off.PriceIn != 2.0 {
		t.Fatalf("live offer price = %v/%v, want 2/3 applied at once", off.PriceIn, off.PriceOut)
	}
	if len(off.Schedule) != 1 || !off.Schedule[0].Free {
		t.Fatalf("live offer schedule not applied: %+v", off.Schedule)
	}

	// GET reads it back, flagged as an owner override.
	gc, rows := pmGet(t, b, userPriv)
	if gc != http.StatusOK || len(rows) != 1 {
		t.Fatalf("get = %d rows=%d, want 200/1", gc, len(rows))
	}
	if rows[0].PriceOut != 3.0 || !rows[0].Overridden {
		t.Fatalf("get row = %+v, want price_out 3 + overridden", rows[0])
	}

	// RE-REGISTER n1 (the agent re-supplies its OWN price_out 1.0). The owner override
	// must survive: applyOfferOverrides re-seeds it on every register.
	if code, msg := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("re-register n1 = %d (%q), want 200", code, msg)
	}
	b.mu.Lock()
	off2 := b.nodes["n1"].Offers[0]
	b.mu.Unlock()
	if off2.PriceOut != 3.0 || off2.PriceIn != 2.0 {
		t.Fatalf("after re-register, price = %v/%v, want the owner override 2/3 preserved (not the node's 0/1)", off2.PriceIn, off2.PriceOut)
	}

	// And it survives a broker RESTART (re-hydrate from the persisted record).
	b2 := newBroker(b.db)
	b2.rehydrateNodes()
	if got := b2.nodes["n1"].Offers[0].PriceOut; got != 3.0 {
		t.Fatalf("after restart re-hydrate, price_out = %v, want the persisted override 3", got)
	}

	// CLEAR reverts to the node's own price on its next registration.
	if code, msg := pmSet(t, b, userPriv, map[string]any{"node": "n1", "model": "m", "clear": true}); code != http.StatusOK {
		t.Fatalf("clear = %d (%q), want 200", code, msg)
	}
	if code, _ := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("re-register after clear, want 200")
	}
	b.mu.Lock()
	off3 := b.nodes["n1"].Offers[0]
	b.mu.Unlock()
	if off3.PriceOut != 1.0 {
		t.Fatalf("after clear + re-register, price_out = %v, want the node's own 1.0 restored", off3.PriceOut)
	}
}

// TestProviderModelsOwnerScoping: owner B cannot read or edit owner A's node. A node id
// is public, so the AccountOfNode gate must 403 a cross-account edit.
func TestProviderModelsOwnerScoping(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t) // owner A == userPriv
	if code, _ := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("register n1 (owner A), want 200")
	}

	// A second owner B, bound but owning NO node.
	bPub, bPriv, _ := ed25519.GenerateKey(nil)
	bPubHex := hex.EncodeToString(bPub)
	mem := b.db.(*store.Mem)
	_ = mem.BindOwner(store.Owner{GitHubID: 2, Login: "ownerB", Pubkey: bPubHex})

	// B tries to price A's node -> 403.
	code, msg := pmSet(t, b, bPriv, map[string]any{
		"node": "n1", "model": "m", "price_in": 9.0, "price_out": 9.0,
	})
	if code != http.StatusForbidden {
		t.Fatalf("cross-account set = %d (%q), want 403", code, msg)
	}
	// And A's node is unchanged (B's attempt wrote nothing).
	b.mu.Lock()
	off := b.nodes["n1"].Offers[0]
	b.mu.Unlock()
	if off.PriceOut == 9.0 {
		t.Fatalf("owner A's node was mutated by owner B - scoping breach")
	}

	// B's own GET lists NONE of A's models (B owns no nodes).
	gc, rows := pmGet(t, b, bPriv)
	if gc != http.StatusOK {
		t.Fatalf("B get = %d, want 200", gc)
	}
	if len(rows) != 0 {
		t.Fatalf("owner B sees %d models, want 0 (none of A's leak)", len(rows))
	}
}

// TestProviderModelsPriceCeiling: a Console price over the public hard ceiling is
// rejected (the SAME ceiling a CLI registration is held to), on the base AND on a window.
func TestProviderModelsPriceCeiling(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	if code, _ := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("register n1, want 200")
	}

	// Base out-price way over the $100 ceiling.
	code, msg := pmSet(t, b, userPriv, map[string]any{
		"node": "n1", "model": "m", "price_in": 1.0, "price_out": 99999.0,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("over-ceiling base = %d (%q), want 400", code, msg)
	}

	// A scheduled WINDOW over the ceiling is rejected too.
	code, msg = pmSet(t, b, userPriv, map[string]any{
		"node": "n1", "model": "m", "price_in": 1.0, "price_out": 1.0,
		"schedule": []map[string]any{{"start": "09:00", "end": "17:00", "price_out": 99999.0}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("over-ceiling window = %d (%q), want 400", code, msg)
	}

	// Malformed window time is rejected.
	code, msg = pmSet(t, b, userPriv, map[string]any{
		"node": "n1", "model": "m", "price_in": 1.0, "price_out": 1.0,
		"schedule": []map[string]any{{"start": "9am", "end": "5pm"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("malformed window time = %d (%q), want 400", code, msg)
	}
}

// TestProviderModelsLeavesPastChargesUntouched: a price change only sets the PUBLISHED
// (future) price - it must NEVER rewrite a past receipt or any ledger row.
func TestProviderModelsLeavesPastChargesUntouched(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	if code, _ := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("register n1, want 200")
	}
	mem := b.db.(*store.Mem)

	// A PAST settled request at the OLD price (price_out 1.0) lands in the ledger.
	rec := protocol.UsageReceipt{
		RequestID: "req-old", Model: "m", PromptTokens: 1000, CompletionTokens: 2000,
		PriceIn: 1.0, PriceOut: 1.0, TS: time.Now().Add(-time.Hour).Unix(),
	}
	if _, err := mem.Settle("u_gh_buyer", "n1", 0.005, 0.0035, rec); err != nil {
		t.Fatalf("seed past receipt: %v", err)
	}
	// Snapshot the past receipt + the consumer ledger BEFORE the price change.
	beforeEntries, _ := mem.RecentByNode("n1", 10)
	beforeLedger, _ := mem.LedgerOf("u_gh_buyer", nil, 0)
	if len(beforeEntries) != 1 {
		t.Fatalf("want 1 past receipt seeded, got %d", len(beforeEntries))
	}

	// Now change the PUBLISHED price to 5.0.
	if code, msg := pmSet(t, b, userPriv, map[string]any{
		"node": "n1", "model": "m", "price_in": 5.0, "price_out": 5.0,
	}); code != http.StatusOK {
		t.Fatalf("set new price = %d (%q), want 200", code, msg)
	}

	// The past receipt's billed cost + the ledger are byte-for-byte unchanged: the
	// override touched only future pricing.
	afterEntries, _ := mem.RecentByNode("n1", 10)
	afterLedger, _ := mem.LedgerOf("u_gh_buyer", nil, 0)
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("receipt count changed: %d -> %d", len(beforeEntries), len(afterEntries))
	}
	if afterEntries[0].Cost != beforeEntries[0].Cost || afterEntries[0].RequestID != beforeEntries[0].RequestID {
		t.Fatalf("past receipt mutated: before=%+v after=%+v", beforeEntries[0], afterEntries[0])
	}
	if len(afterLedger) != len(beforeLedger) {
		t.Fatalf("ledger rows changed: %d -> %d (a price edit must write NO ledger rows)", len(beforeLedger), len(afterLedger))
	}
	for i := range afterLedger {
		if afterLedger[i].Amount != beforeLedger[i].Amount || afterLedger[i].Kind != beforeLedger[i].Kind {
			t.Fatalf("ledger row %d mutated: before=%+v after=%+v", i, beforeLedger[i], afterLedger[i])
		}
	}

	// But the FUTURE published price is now 5.0 on the live offer.
	b.mu.Lock()
	off := b.nodes["n1"].Offers[0]
	b.mu.Unlock()
	if off.PriceOut != 5.0 {
		t.Fatalf("new published price_out = %v, want 5.0", off.PriceOut)
	}
}

// TestProviderModelsRequiresOwner: an unsigned (anonymous) caller is rejected.
func TestProviderModelsRequiresOwner(t *testing.T) {
	b, _, _, _ := newBandBroker(t)
	w := httptest.NewRecorder()
	b.providerModels(w, httptest.NewRequest(http.MethodGet, "/provider/models", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous get = %d, want 401", w.Code)
	}
}
