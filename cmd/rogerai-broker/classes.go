package main

// classes.go: model class aliases (contract §14.B5, features/routing/class_aliases.feature).
// `@class/<name>` names a transparent group of models that the relay expands, per request and
// under the caller's own visibility, into an ordinary models[] list: no classifier, the same
// market always gives the same list.

import (
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

const classPrefix = "@class/"

// modelClasses is the built-in alias table, as data: a model is in a class when its params_b
// (declared, else the id estimate) is inside [minB, maxB] and, when tools is set, some eligible
// offer of it is tools-verified. A model with no known size is never in a size class.
var modelClasses = map[string]struct {
	minB, maxB float64
	tools      bool
}{
	"coding-30b": {24, 40, true},
	"small":      {0, 9, false},
	"large":      {60, math.Inf(1), false},
}

// classMaxModels caps an expansion (the models[] limit, §3).
const classMaxModels = routingModelsMax

// className returns the class a bare `@class/...` id names, or an unknown_model_class error.
// Names are exact (lowercase, no further path).
func className(bare string) (string, *routingError) {
	name := strings.TrimPrefix(bare, classPrefix)
	if _, ok := modelClasses[name]; !ok {
		return "", &routingError{code: "unknown_model_class", msg: "unknown model class " + bare + " (known: @class/coding-30b, @class/small, @class/large)"}
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
	c := modelClasses[name]
	cost := map[string]float64{}
	tools := map[string]bool{}
	for _, o := range offers {
		if !o.Online || o.Modality != "chat" || o.ParamsEstimated == nil || o.ParamsB < c.minB || o.ParamsB > c.maxB {
			continue
		}
		tools[o.Model] = tools[o.Model] || slices.Contains(o.Capabilities, protocol.CapTools)
		in, out := o.In, o.Out
		if o.FreeNow {
			in, out = 0, 0
		}
		if k, seen := cost[o.Model]; !seen || blendedPrice(in, out) < k {
			cost[o.Model] = blendedPrice(in, out)
		}
	}
	ids := make([]string, 0, len(cost))
	for id := range cost {
		if !c.tools || tools[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if cost[ids[i]] != cost[ids[j]] {
			return cost[ids[i]] < cost[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > classMaxModels {
		ids = ids[:classMaxModels]
	}
	return ids
}

// classEntries lists each class that expands to something over offers as its own catalog
// entry (§14.B4): rogerai.expands_to, and the head model's top-level fields.
func classEntries(offers []offerView, data []modelEntry) []modelEntry {
	byID := make(map[string]modelEntry, len(data))
	for _, e := range data {
		byID[e.ID] = e
	}
	var out []modelEntry
	for name := range modelClasses {
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
