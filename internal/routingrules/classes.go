package routingrules

// Model class aliases (contract §14.B5, features/routing/class_aliases.feature) and the
// quantization filter (contract §5): the same rules on the market relay and on the LAN ladder.

import (
	"math"
	"sort"
	"strings"
)

// ClassPrefix starts a class alias model id.
const ClassPrefix = "@class/"

// QuantUnknown is the provider.quantizations value that admits an UNLABELED source (the
// broker's protocol.QuantUnknown, which a broker test pins equal).
const QuantUnknown = "unknown"

// classRule: a model is in a class when its params_b (declared, else the id estimate) is
// inside [minB, maxB] and, when tools is set, some eligible source of it is tools-verified.
// A model with no known size is never in a size class.
type classRule struct {
	minB, maxB float64
	tools      bool
}

var classes = map[string]classRule{
	"coding-30b": {24, 40, true},
	"small":      {0, 9, false},
	"large":      {60, math.Inf(1), false},
}

// ClassNames are the built-in class names, sorted.
func ClassNames() []string {
	out := make([]string, 0, len(classes))
	for n := range classes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ClassName returns the class a `@class/...` id names; ok is false for an unknown class.
// Names are exact (lowercase, no further path).
func ClassName(id string) (name string, ok bool) {
	name = strings.TrimPrefix(id, ClassPrefix)
	_, ok = classes[name]
	return name, ok
}

// ClassCandidate is one source of a model a class may expand over.
type ClassCandidate struct {
	Model       string
	ParamsB     float64
	ParamsKnown bool
	Tools       bool    // this source is tools-verified
	Cost        float64 // its blended price (0 when free)
}

// ExpandClass is a class's expansion over a set of sources: every model in the class, ordered
// by its cheapest source's cost, ties by id, at most max. One rule for the market relay, the
// /v1/models catalog and the LAN ladder, so each shows what the others would route to.
func ExpandClass(name string, sources []ClassCandidate, max int) []string {
	c, ok := classes[name]
	if !ok {
		return nil
	}
	cost := map[string]float64{}
	tools := map[string]bool{}
	for _, s := range sources {
		if !s.ParamsKnown || s.ParamsB < c.minB || s.ParamsB > c.maxB {
			continue
		}
		tools[s.Model] = tools[s.Model] || s.Tools
		if k, seen := cost[s.Model]; !seen || s.Cost < k {
			cost[s.Model] = s.Cost
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
	if max > 0 && len(ids) > max {
		ids = ids[:max]
	}
	return ids
}

// QuantSet is a provider.quantizations list as a lookup set (nil for an absent or empty list).
func QuantSet(labels []string) map[string]bool {
	if len(labels) == 0 {
		return nil
	}
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		set[strings.ToLower(strings.TrimSpace(l))] = true
	}
	return set
}

// QuantAdmits applies provider.quantizations to one source's label: a nil set admits every
// source; labels match case-insensitively; "unknown" admits a source that carries no label.
func QuantAdmits(set map[string]bool, quant string) bool {
	if set == nil {
		return true
	}
	if quant == "" {
		return set[QuantUnknown]
	}
	return set[strings.ToLower(quant)]
}
