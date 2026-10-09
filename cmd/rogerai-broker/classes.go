package main

// classes.go: model class aliases (contract §14.B5, features/routing/class_aliases.feature).
// `@class/<name>` names a transparent group of models that the relay expands, per request and
// under the caller's own visibility, into an ordinary models[] list: no classifier, the same
// market always gives the same list.

import (
	"slices"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/routingrules"
)

const classPrefix = routingrules.ClassPrefix

// classMaxModels caps an expansion (the models[] limit, §3).
const classMaxModels = routingModelsMax

// className returns the class a bare `@class/...` id names, or an unknown_model_class error.
// Names are exact (lowercase, no further path); the table is routingrules', shared with the
// LAN ladder.
func className(bare string) (string, *routingError) {
	name, ok := routingrules.ClassName(bare)
	if !ok {
		known := make([]string, 0, 3)
		for _, n := range routingrules.ClassNames() {
			known = append(known, classPrefix+n)
		}
		return "", &routingError{code: "unknown_model_class", msg: "unknown model class " + bare + " (known: " + strings.Join(known, ", ") + ")"}
	}
	return name, nil
}

// expandClass is a class's current expansion over the on-air chat offers the caller can see:
// scope nil = the public market (private stations never widen it); a grant's or a band's node
// set otherwise. Ordered by each model's cheapest coherent blended cost, ties by bare id, at
// most classMaxModels.
func (b *broker) expandClass(name string, scope map[string]bool) []string {
	now := time.Now()
	var offers []offerView
	b.mu.Lock()
	for _, n := range b.nodes {
		if b.isBanned(n.NodeID) {
			continue
		}
		if scope != nil && !scope[n.NodeID] || scope == nil && b.private[n.NodeID] {
			continue
		}
		offers = b.enrichOffersForNode(offers, n, now, nil, false)
	}
	b.mu.Unlock()
	return expandOffers(name, offers)
}

// expandOffers is a class's expansion over a set of offers (the relay's visible set, or the
// /v1/models feed): one rule for both, so the catalog shows what a request would get.
func expandOffers(name string, offers []offerView) []string {
	cands := make([]routingrules.ClassCandidate, 0, len(offers))
	for _, o := range offers {
		if !o.Online || o.Modality != "chat" {
			continue
		}
		in, out := o.In, o.Out
		if o.FreeNow {
			in, out = 0, 0
		}
		cands = append(cands, routingrules.ClassCandidate{Model: o.Model, ParamsB: o.ParamsB, ParamsKnown: o.ParamsEstimated != nil,
			Tools: slices.Contains(o.Capabilities, protocol.CapTools), Cost: blendedPrice(in, out)})
	}
	return routingrules.ExpandClass(name, cands, classMaxModels)
}

// classEntries lists each class that expands to something over offers as its own catalog
// entry (§14.B4): rogerai.expands_to, and the head model's top-level fields.
func classEntries(offers []offerView, data []modelEntry) []modelEntry {
	byID := make(map[string]modelEntry, len(data))
	for _, e := range data {
		byID[e.ID] = e
	}
	var out []modelEntry
	for _, name := range routingrules.ClassNames() {
		ids := expandOffers(name, offers)
		head, ok := byID[firstOr(ids)]
		if !ok {
			continue
		}
		block := map[string]any{"expands_to": ids}
		if caps, ok := head.RogerAI["capabilities"]; ok {
			block["capabilities"] = caps // the head's, matching the supported_parameters it lends
		}
		head.ID, head.RogerAI = classPrefix+name, block
		out = append(out, head)
	}
	return out
}

func firstOr(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}
