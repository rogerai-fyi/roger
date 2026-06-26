package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// providerModels handles /provider/models - the owner's per-model PRICING + time-of-use
// SCHEDULE management surface for the web Console (PRICING-OVERRIDE design):
//
//	GET            list THIS owner's served models/nodes: current published price
//	               (in/out), free flag, schedule windows, online state, and whether the
//	               price is an owner-authored web override.
//	PATCH / POST   set (or clear) an owner-authored price + schedule for one (node,model).
//
// Owner-auth via payoutOwner (dual-path: web session cookie OR signed CLI), and EVERY
// row/edit is scoped strictly to the caller's owner.Pubkey via AccountOfNode - exactly
// the ownership gate /earnings uses. An owner only ever sees/edits their OWN nodes; a
// node bound to a different account (or unbound) is 403, never readable.
//
// MONEY-SAFETY: an edit sets only the PUBLISHED (future) price. It is applied as the
// EFFECTIVE price at serve time (it seeds the node's in-memory offer immediately AND is
// re-applied on every node re-register, so it survives re-registration and a broker
// restart - see store.OfferOverride + applyOfferOverrides). It NEVER rewrites a past
// UsageReceipt or any ledger row: those were settled at the price quoted at serve time
// and are immutable. The public price CEILING (registerPriceCeiling) is enforced on the
// override exactly as it is at CLI registration.
func (b *broker) providerModels(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPatch && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, PATCH, POST, OPTIONS")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	corsCreds(w, r)

	// Read the body BEFORE resolving identity: a signed write's Ed25519 signature
	// covers the body, so the verify must see the same bytes (a GET sends none).
	var body []byte
	if r.Method != http.MethodGet {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<16))
	}
	_, o, ok := b.payoutOwner(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `rogerai login` to manage your models")
		return
	}
	if o.GitHubID == 0 || o.Pubkey == "" {
		jsonErr(w, http.StatusForbidden, "no operator account for this login")
		return
	}

	if r.Method == http.MethodGet {
		b.providerModelsList(w, o)
		return
	}
	b.providerModelsSet(w, o, body)
}

// providerModelRow is one served (node, model) and its current published pricing.
type providerModelRow struct {
	Node       string                 `json:"node"`
	Model      string                 `json:"model"`
	Online     bool                   `json:"online"`
	Ctx        int                    `json:"ctx"`
	PriceIn    float64                `json:"price_in"`  // base/fallback published input price ($/1M)
	PriceOut   float64                `json:"price_out"` // base/fallback published output price ($/1M)
	Free       bool                   `json:"free"`      // base price is $0
	Schedule   []protocol.PriceWindow `json:"schedule"`
	Overridden bool                   `json:"overridden"`  // owner authored this from the web Console
	ActiveIn   float64                `json:"active_in"`   // the price in effect RIGHT NOW
	ActiveOut  float64                `json:"active_out"`  // (after applying the time-of-use schedule)
	ActiveFree bool                   `json:"active_free"` // a free window is active now
}

// providerModelsList returns every model on every node bound to this owner account.
func (b *broker) providerModelsList(w http.ResponseWriter, o store.Owner) {
	now := time.Now()
	nodeIDs, _ := b.db.NodesOfAccount(o.Pubkey)

	rows := make([]providerModelRow, 0)
	b.mu.Lock()
	for _, id := range nodeIDs {
		reg, known := b.nodes[id]
		if !known {
			continue // bound but not currently in the registry (never registered / not re-hydrated)
		}
		online := time.Since(b.lastSeen[id]) < nodeTTL
		for _, off := range reg.Offers {
			ain, aout, afree, _ := off.ActivePrice(now)
			rows = append(rows, providerModelRow{
				Node: id, Model: off.Model, Online: online, Ctx: off.Ctx,
				PriceIn: off.PriceIn, PriceOut: off.PriceOut,
				Free:     off.PriceIn == 0 && off.PriceOut == 0,
				Schedule: scheduleOrEmpty(off.Schedule),
				// the price comes from an override iff the store holds one we authored
				ActiveIn: ain, ActiveOut: aout, ActiveFree: afree,
			})
		}
	}
	b.mu.Unlock()

	// Flag which rows are owner-authored overrides (store reads, off the lock).
	for i := range rows {
		if ov, found, _ := b.db.OfferOverride(rows[i].Node, rows[i].Model); found && ov.Owner == o.Pubkey {
			rows[i].Overridden = true
		}
	}
	// Stable order: node, then model (the table reads the same on every refresh).
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Node != rows[j].Node {
			return rows[i].Node < rows[j].Node
		}
		return rows[i].Model < rows[j].Model
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"models": rows,
		// the public hard ceilings, so the editor can guard the inputs client-side too.
		"ceiling_in":  maxPriceInCeiling(),
		"ceiling_out": maxPriceOutCeiling(),
	})
}

// providerModelPatch is the set/clear body.
type providerModelPatch struct {
	Node     string                 `json:"node"`
	Model    string                 `json:"model"`
	PriceIn  float64                `json:"price_in"`
	PriceOut float64                `json:"price_out"`
	Schedule []protocol.PriceWindow `json:"schedule,omitempty"`
	// Clear removes the owner's override for (node,model). The node's NEXT registration
	// then restores its own node-supplied price/schedule (the live in-memory offer keeps
	// the last published value until that re-register).
	Clear bool `json:"clear,omitempty"`
}

// providerModelsSet upserts (or clears) an owner-authored price/schedule override for
// one of the owner's own (node, model) pairs.
func (b *broker) providerModelsSet(w http.ResponseWriter, o store.Owner, body []byte) {
	var req providerModelPatch
	if json.Unmarshal(body, &req) != nil || req.Node == "" || req.Model == "" {
		jsonErr(w, http.StatusBadRequest, "node and model are required")
		return
	}
	// Ownership gate (copied from /earnings): the node MUST be bound to THIS owner's
	// account. Node ids are public, so without this an operator could price another
	// owner's node. Unbound or bound-to-another-account => 403.
	acct, bound, _ := b.db.AccountOfNode(req.Node)
	if !bound || acct != o.Pubkey {
		jsonErr(w, http.StatusForbidden, "you do not own this node")
		return
	}

	if req.Clear {
		if _, err := b.db.ClearOfferOverride(o.Pubkey, req.Node, req.Model); err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"clear": true,
			"note":  "override cleared - the node's own price/schedule is restored on its next registration",
		})
		return
	}

	// Validate shape (non-negative, well-formed windows) then the public hard ceiling -
	// the SAME ceiling a CLI registration is held to.
	if msg := validateOfferInput(req.PriceIn, req.PriceOut, req.Schedule); msg != "" {
		jsonErr(w, http.StatusBadRequest, msg)
		return
	}
	synthetic := protocol.ModelOffer{PriceIn: req.PriceIn, PriceOut: req.PriceOut, Schedule: req.Schedule}
	if msg := registerPriceCeiling([]protocol.ModelOffer{synthetic}); msg != "" {
		jsonErr(w, http.StatusBadRequest, msg)
		return
	}

	ov := store.OfferOverride{
		Owner: o.Pubkey, NodeID: req.Node, Model: req.Model,
		PriceIn: req.PriceIn, PriceOut: req.PriceOut, Schedule: req.Schedule,
		UpdatedAt: time.Now().Unix(),
	}
	if err := b.db.SetOfferOverride(ov); err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}

	// Apply to the LIVE in-memory offer immediately (so the new price is effective at
	// once, not only after the node's next re-register), and re-persist the node record
	// so a restart re-hydrates the overridden offer. applyOfferOverrides re-applies on
	// every subsequent register, which is what makes it survive re-registration.
	row, ok := b.applyOverrideLive(o.Pubkey, ov)
	if !ok {
		// The node isn't currently registered in memory (offline / not re-hydrated). The
		// override is stored and seeds the offer the moment the node next registers.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "applied": "stored", "override": ov,
			"note": "saved - it applies the moment this node is next on air",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": "live", "model": row})
}

// applyOverrideLive mutates the live in-memory offer for (node,model) to the override's
// price/schedule and re-persists the node record, returning the updated row. ok=false
// when the node is not currently in the registry (no live offer to mutate).
func (b *broker) applyOverrideLive(owner string, ov store.OfferOverride) (providerModelRow, bool) {
	now := time.Now()
	b.mu.Lock()
	reg, known := b.nodes[ov.NodeID]
	if !known {
		b.mu.Unlock()
		return providerModelRow{}, false
	}
	var row providerModelRow
	matched := false
	for i := range reg.Offers {
		if reg.Offers[i].Model != ov.Model {
			continue
		}
		reg.Offers[i].PriceIn = ov.PriceIn
		reg.Offers[i].PriceOut = ov.PriceOut
		reg.Offers[i].Schedule = ov.Schedule
		ain, aout, afree, _ := reg.Offers[i].ActivePrice(now)
		row = providerModelRow{
			Node: ov.NodeID, Model: ov.Model, Online: time.Since(b.lastSeen[ov.NodeID]) < nodeTTL,
			Ctx: reg.Offers[i].Ctx, PriceIn: ov.PriceIn, PriceOut: ov.PriceOut,
			Free: ov.PriceIn == 0 && ov.PriceOut == 0, Schedule: scheduleOrEmpty(ov.Schedule),
			Overridden: true, ActiveIn: ain, ActiveOut: aout, ActiveFree: afree,
		}
		matched = true
	}
	conf := b.confidential[ov.NodeID]
	seen := b.lastSeen[ov.NodeID]
	b.nodes[ov.NodeID] = reg
	b.mu.Unlock()
	if !matched {
		// Node is registered but does not currently offer this model; the override is
		// stored and will seed the offer if/when the node advertises it.
		return providerModelRow{}, false
	}
	if b.db != nil {
		_ = b.db.UpsertNode(store.NodeRecord{NodeID: ov.NodeID, Reg: reg, Confidential: conf, LastSeen: seen.Unix()})
	}
	return row, true
}

// scheduleOrEmpty returns a non-nil slice so the JSON is always a [] (never null),
// which keeps the web editor's "no windows" rendering simple.
func scheduleOrEmpty(s []protocol.PriceWindow) []protocol.PriceWindow {
	if s == nil {
		return []protocol.PriceWindow{}
	}
	return s
}
