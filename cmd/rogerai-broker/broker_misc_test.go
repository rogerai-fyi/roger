package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestRootHandler covers GET /: the service banner at "/" and a 404 for any other path.
func TestRootHandler(t *testing.T) {
	b := &broker{}
	w := httptest.NewRecorder()
	b.root(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("root / = %d, want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["service"] != "rogerai-broker" {
		t.Errorf("root service = %v, want rogerai-broker", resp["service"])
	}
	w2 := httptest.NewRecorder()
	b.root(w2, httptest.NewRequest(http.MethodGet, "/elsewhere", nil))
	if w2.Code != http.StatusNotFound {
		t.Errorf("root /elsewhere = %d, want 404", w2.Code)
	}
}

// TestSweepsDisabledGuards covers the auto-expiry sweeps' disabled/early-return guards
// (they must return immediately, not start a ticker, when disabled or store-less).
func TestSweepsDisabledGuards(t *testing.T) {
	(&broker{recountHoldDays: 0}).recountHoldSweep() // auto-expiry disabled -> returns
	(&broker{nodeBanDays: 0}).nodeBanSweep()         // auto-lift disabled -> returns
	(&broker{db: nil}).reversalRetrySweep()          // no store -> returns
}

// TestGroqCall covers the concierge LLM call: no key -> (",false); with a key it POSTs
// to the configured endpoint and returns the assistant message.
func TestGroqCall(t *testing.T) {
	// No key configured: a clean miss, no network.
	if _, ok := (&broker{concierge: &concierge{}}).groqCall(nil); ok {
		t.Error("groqCall with no key should return ok=false")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there"}}]}`))
	}))
	defer srv.Close()
	b := &broker{concierge: &concierge{groqKey: "k", groqURL: srv.URL, groqModel: "m", client: srv.Client()}}
	reply, ok := b.groqCall([]chatMsg{{Role: "user", Content: "hello"}})
	if !ok || reply != "hi there" {
		t.Errorf("groqCall = %q/%v, want \"hi there\"/true", reply, ok)
	}
}

// TestObserveRecountInput covers the L1 re-count observer: an over-report past tolerance
// flags a discrepancy, places the node's earnings on a recount hold, and strikes the
// owner; the per-node trust counters advance.
func TestObserveRecountInput(t *testing.T) {
	mem := store.NewMem()
	_ = mem.BindNode("n1", "acct1")
	b := &broker{
		db:    mem,
		trust: map[string]trustState{},
	}
	b.recount.tolerance = 0.01
	b.recount.strikeTolerance = 0.01

	// claimed 120 vs recounted 100 = +20% over the 1% tolerance -> flagged.
	b.observeRecountInput("n1", "r1", 120, 100, true)

	if b.trust["n1"].recounts != 1 || b.trust["n1"].discrepancies != 1 {
		t.Errorf("trust = %+v, want 1 recount / 1 discrepancy", b.trust["n1"])
	}
	// The node's earnings should now be on a recount hold.
	held, _ := mem.RecountHeldNodes()
	if !held["n1"] {
		t.Error("an over-reporting node should be placed on a recount hold")
	}

	// An honest exact match does NOT add a discrepancy.
	b.observeRecountInput("n1", "r2", 100, 100, true)
	if b.trust["n1"].discrepancies != 1 {
		t.Errorf("honest recount must not add a discrepancy: %+v", b.trust["n1"])
	}
}

// TestAdminAppeals covers GET /admin/appeals: admin-gated (403 without the key), and a
// 200 with the pending-appeal queue when authed by the broker admin key.
func TestAdminAppeals(t *testing.T) {
	mem := store.NewMem()
	_, _ = mem.AddAppeal(store.Appeal{AccountID: "pk", NodeID: "n1", Reason: "false positive"})
	b := &broker{db: mem, adminKey: "super-secret"}

	// No admin key -> 403.
	w := httptest.NewRecorder()
	b.adminAppeals(w, httptest.NewRequest(http.MethodGet, "/admin/appeals", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("adminAppeals without key = %d, want 403", w.Code)
	}

	// With the admin key -> 200 + the queue.
	r := httptest.NewRequest(http.MethodGet, "/admin/appeals", nil)
	r.Header.Set("X-Roger-Admin", "super-secret")
	w2 := httptest.NewRecorder()
	b.adminAppeals(w2, r)
	if w2.Code != http.StatusOK {
		t.Fatalf("adminAppeals with key = %d, want 200: %s", w2.Code, w2.Body.String())
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Errorf("admin appeals count = %d, want 1", resp.Count)
	}
}
