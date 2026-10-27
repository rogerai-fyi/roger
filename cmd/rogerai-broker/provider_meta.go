package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// providerMeta handles POST /provider/meta: the owner declares (or clears) what one of their
// OWN (node, model) pairs is for, from the web console (features/categories/owner_override
// .feature). Same identity + ownership gates as /provider/models (payoutOwner, then
// AccountOfNode == the caller); the same strict validation the node applies
// (protocol.Taxonomy.Declare), so every surface refuses the same values with the same reason.
//
// Stored as a MetaOverride - a sibling of the price override, never the same row - applied
// to the live offer at once and re-applied on every register (applyMetaOverrides), so it
// survives the node reconnecting and a broker restart. It is the declaration on air, over
// the node's own. It describes the model; it never changes a price, routing or screening.
func (b *broker) providerMeta(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	corsCreds(w, r)
	// Read the body BEFORE identity: a signed write's signature covers it.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	_, o, ok := b.payoutOwner(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "not logged in - run `roger login` to manage your models")
		return
	}
	if !hasVerifiedIdentity(o) || o.Pubkey == "" {
		jsonErr(w, http.StatusForbidden, "no operator account for this login")
		return
	}
	var req struct {
		Node          string   `json:"node"`
		Model         string   `json:"model"`
		Category      string   `json:"category"` // "cat" or "cat/sub", as operators type it
		Tags          []string `json:"tags"`
		Tuning        string   `json:"tuning"`
		BaseModel     string   `json:"base_model"`
		ContentPolicy string   `json:"content_policy"`
		License       string   `json:"license"`
		Clear         bool     `json:"clear"`
	}
	if json.Unmarshal(body, &req) != nil || req.Node == "" || req.Model == "" {
		jsonErr(w, http.StatusBadRequest, "node and model are required")
		return
	}
	if acct, bound, _ := b.db.AccountOfNode(req.Node); !bound || acct != o.Pubkey {
		jsonErr(w, http.StatusForbidden, "you do not own this node")
		return
	}
	cat, sub := protocol.SplitCategory(req.Category)
	t, err := protocol.Taxonomy{Category: cat, Subcategory: sub, Tags: req.Tags, Tuning: req.Tuning,
		BaseModel: req.BaseModel, ContentPolicy: req.ContentPolicy, License: req.License}.Declare(b.liveModality(req.Node, req.Model))
	if req.Clear || (err == nil && !t.Declared()) {
		if _, err := b.db.ClearMetaOverride(o.Pubkey, req.Node, req.Model); err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "clear": true,
			"note": "category cleared - the node's own category is restored on its next registration"})
		return
	}
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t.TaxonomySource = protocol.SourceDeclared
	ov := store.MetaOverride{Owner: o.Pubkey, NodeID: req.Node, Model: req.Model, Taxonomy: t, UpdatedAt: time.Now().Unix()}
	if err := b.db.SetMetaOverride(ov); err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	_, online, known := b.mutateLiveOffer(req.Node, req.Model, func(off *protocol.ModelOffer) {
		off.Taxonomy = t.OnAir(off.Modality, off.Taxonomy)
	})
	if !known || !online {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": "stored", "taxonomy": t,
			"note": "saved - it applies the next time this node is on air with this model"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": "live", "taxonomy": t})
}

// liveModality is the modality the node currently offers the model with ("" = chat, also
// when the node is not on air: the override is re-canonicalized with the real modality at
// every register, so a voice model still reads as audio).
func (b *broker) liveModality(node, model string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, o := range b.nodes[node].Offers {
		if o.Model == model {
			return o.Modality
		}
	}
	return ""
}

// applyMetaOverrides seeds each offer's taxonomy from the owner's web-set override, on every
// register (off the broker lock: a store read). Only an owner-bound node carries overrides,
// and only its own owner's.
func (b *broker) applyMetaOverrides(owner, node string, offers []protocol.ModelOffer) {
	if b.db == nil || owner == "" {
		return
	}
	for i := range offers {
		ov, ok, err := b.db.MetaOverride(node, offers[i].Model)
		if err != nil || !ok || ov.Owner != owner {
			continue
		}
		// OnAir: the override as a whole when a claim survives for this offer's modality,
		// else the node's own (an override saved while the node was off air - checked as
		// chat - never blanks a voice model's own declaration).
		offers[i].Taxonomy = ov.Taxonomy.OnAir(offers[i].Modality, offers[i].Taxonomy)
	}
}
