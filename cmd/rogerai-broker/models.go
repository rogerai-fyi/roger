package main

// models.go serves GET /v1/models (and /v1/models/{id}): the OpenAI-shaped catalog every SDK
// probes at startup, one entry per distinct model id with a public offer ON AIR
// (features/discovery/models_endpoint.feature, ROUTING-EXPRESSION-CONTRACT §8). It is a
// collapse of the same computeDiscover view /discover serves - the same liveness, ban and
// private-band rules by construction - so nothing enumerable here is hidden there or vice
// versa. Public read: the same CORS + cache posture as /market, no per-IP gate.
//
// Slice 0 carries the OpenAI fields plus the routing facts the pins need (cooling); the full
// `rogerai` block and the filter params are slice 2's (their scenarios stay untagged).

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

type modelEntry struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	OwnedBy string         `json:"owned_by"`
	RogerAI map[string]any `json:"rogerai,omitempty"`
}

func (b *broker) models(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w)
	if id := strings.TrimPrefix(r.URL.Path, "/v1/models/"); id != "" && id != r.URL.Path {
		// One entry, filtered out of the SAME cached list (never a per-hit recompute of the
		// discover view on an ungated public path).
		var list struct {
			Data []modelEntry `json:"data"`
		}
		_ = json.Unmarshal(b.cachedJSON("models", publicMarketTTL, b.computeModels), &list)
		for _, e := range list.Data {
			if e.ID == id {
				writeJSON(w, http.StatusOK, e)
				return
			}
		}
		jsonErr(w, http.StatusNotFound, "not found")
		return
	}
	b.serveCachedJSON(w, "models", publicMarketTTL, b.computeModels)
}

// computeModels collapses the live /discover offers by model id: listed iff at least one
// public offer is on air (stale, banned and private nodes are already out of that view).
func (b *broker) computeModels() any {
	offers, _ := b.computeDiscover().(map[string]any)["offers"].([]offerView)
	now := time.Now().Unix()
	byID := map[string]*modelEntry{}
	for _, o := range offers {
		if !o.Online {
			continue
		}
		e := byID[o.Model]
		if e == nil {
			e = &modelEntry{ID: o.Model, Object: "model", OwnedBy: "rogerai", Created: b.firstSeenModel(o.Model, now),
				RogerAI: map[string]any{"cooling": true}}
			byID[o.Model] = e
		}
		// cooling: every on-air station of the model is in an upstream-429 cooldown; the
		// soonest expiry is when routing resumes.
		if o.CoolingUntil == 0 {
			e.RogerAI["cooling"] = false
			delete(e.RogerAI, "cooling_until")
		} else if e.RogerAI["cooling"] == true {
			if cur, _ := e.RogerAI["cooling_until"].(int64); cur == 0 || o.CoolingUntil < cur {
				e.RogerAI["cooling_until"] = o.CoolingUntil
			}
		}
	}
	data := make([]modelEntry, 0, len(byID))
	for _, e := range byID {
		data = append(data, *e)
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })
	return map[string]any{"object": "list", "data": data}
}

func (b *broker) firstSeenModel(id string, now int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.modelFirstSeen[id]; ok {
		return t
	}
	if b.modelFirstSeen == nil {
		b.modelFirstSeen = map[string]int64{}
	}
	b.modelFirstSeen[id] = now
	return now
}
