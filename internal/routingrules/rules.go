// Package routingrules holds the value rules of the routing object (contract §1a) that the
// client, the local proxy and the standalone local plane all apply, so a plane that ignores
// some keys still refuses a value the broker would. It imports nothing of this module, so the
// standalone serve package can use it and stay Core-free (features/packaging/one_binary.feature).
package routingrules

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

var (
	// ProviderKeys, RogerKeys and MaxPriceKeys are the routing keys a profile or plane accepts.
	ProviderKeys = map[string]bool{"order": true, "only": true, "ignore": true, "allow_fallbacks": true, "sort": true,
		"quantizations": true, "max_price": true, "require_parameters": true}
	RogerKeys = map[string]bool{"pref": true, "require": true, "params_b": true, "min_ctx": true, "min_tps": true,
		"max_ttft_ms": true, "trust_min": true, "self_hosted_only": true, "confidential": true, "region": true, "freq": true, "profile": true}
	MaxPriceKeys  = map[string]bool{"prompt": true, "completion": true, "request": true}
	regionTokenRE = regexp.MustCompile(`^[a-z]{2,8}$`) // the broker's rule (routingreq.go)
)

// Prefs are the four values roger.pref / X-Roger-Pref accept.
var Prefs = []string{"cheap", "balanced", "fast", "reliable"}

// ValidPref reports whether p is one of Prefs (or empty = unset).
func ValidPref(p string) bool {
	if p == "" {
		return true
	}
	for _, v := range Prefs {
		if v == p {
			return true
		}
	}
	return false
}

// The broker's size bounds on a routing object (cmd/rogerai-broker routingreq.go; a broker
// test pins the two sets of values together): list entries, one quant label's length, and the
// largest integer limit (min_ctx, max_ttft_ms).
const (
	RoutingListMax = 32
	QuantLabelMax  = 40
	RoutingIntMax  = math.MaxInt32
	ModelsMax      = 5   // distinct models in [model] ++ models (contract §3)
	ModelIDMax     = 256 // characters in one model id
)

// Values checks the type and range of each provider and roger key present
// (§1a), without the rules that relate one key to another (sort vs pref). A plane that
// ignores some keys still refuses a value the broker would.
func Values(b map[string]any) error {
	if v, ok := b["provider"]; ok && v != nil {
		p, isObj := v.(map[string]any)
		if !isObj {
			return fmt.Errorf("provider must be an object")
		}
		if err := validateProvider(p); err != nil {
			return err
		}
	}
	if v, ok := b["roger"]; ok && v != nil {
		r, isObj := v.(map[string]any)
		if !isObj {
			return fmt.Errorf("roger must be an object")
		}
		if err := validateRoger(r); err != nil {
			return err
		}
	}
	return nil
}

func validateProvider(p map[string]any) error {
	for _, k := range []string{"order", "only", "ignore"} {
		v, ok := p[k]
		if !ok || v == nil {
			continue
		}
		ids, err := idList(v)
		if err != nil || (k != "ignore" && len(ids) == 0) {
			return fmt.Errorf("provider.%s must be a non-empty list of station ids", k)
		}
		if len(ids) > RoutingListMax {
			return fmt.Errorf("provider.%s has more than %d entries", k, RoutingListMax)
		}
	}
	if order, only := p["order"], p["only"]; order != nil && only != nil {
		o, _ := idList(order)
		on, _ := idList(only)
		set := map[string]bool{}
		for _, x := range on {
			set[x] = true
		}
		for _, x := range o {
			if !set[x] {
				return fmt.Errorf("provider.order names %s, outside provider.only", x)
			}
		}
	}
	if v, ok := p["allow_fallbacks"]; ok && v != nil {
		if _, isBool := v.(bool); !isBool {
			return fmt.Errorf("provider.allow_fallbacks must be a boolean")
		}
	}
	if v, ok := p["require_parameters"]; ok && v != nil {
		if _, isBool := v.(bool); !isBool {
			return fmt.Errorf("provider.require_parameters must be a boolean")
		}
	}
	if v, ok := p["sort"]; ok && v != nil {
		if s, _ := v.(string); s != "price" && s != "throughput" && s != "latency" {
			return fmt.Errorf("provider.sort must be price, throughput or latency")
		}
	}
	if v, ok := p["quantizations"]; ok && v != nil {
		labels, err := idList(v)
		if err != nil {
			return fmt.Errorf("provider.quantizations must be a list of labels")
		}
		if len(labels) > RoutingListMax {
			return fmt.Errorf("provider.quantizations has more than %d entries", RoutingListMax)
		}
		for _, l := range labels {
			if len([]rune(strings.TrimSpace(l))) > QuantLabelMax {
				return fmt.Errorf("provider.quantizations: a label is longer than %d characters", QuantLabelMax)
			}
		}
	}
	if v, ok := p["max_price"]; ok && v != nil {
		mp, isObj := v.(map[string]any)
		if !isObj {
			return fmt.Errorf("provider.max_price must be an object")
		}
		keys := make([]string, 0, len(mp))
		for k := range mp {
			keys = append(keys, k)
		}
		sort.Strings(keys) // a fixed order: several faults always name the same one
		for _, k := range keys {
			pv := mp[k]
			// As on the broker: an unknown key is refused even when null; a known one that is
			// null is absent, image included (no image pricing exists, so a priced one is refused).
			if !MaxPriceKeys[k] && k != "image" {
				return fmt.Errorf("provider.max_price.%s is not supported", k)
			}
			if pv == nil {
				continue
			}
			if !MaxPriceKeys[k] {
				return fmt.Errorf("provider.max_price.%s is not supported", k)
			}
			f, isNum := pv.(float64)
			if !isNum || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
				return fmt.Errorf("provider.max_price.%s must be a price >= 0", k)
			}
		}
	}
	return nil
}

func validateRoger(r map[string]any) error {
	if v, ok := r["pref"]; ok && v != nil {
		if s, _ := v.(string); s == "" || !ValidPref(s) {
			return fmt.Errorf("roger.pref must be one of %s", strings.Join(Prefs, ", "))
		}
	}
	if v, ok := r["trust_min"]; ok && v != nil {
		if s, _ := v.(string); s != "any" && s != "verified" && s != "confidential" {
			return fmt.Errorf("roger.trust_min must be any, verified or confidential")
		}
	}
	if v, ok := r["require"]; ok && v != nil {
		caps, err := idList(v)
		if err != nil {
			return fmt.Errorf("roger.require must be a list")
		}
		if len(caps) > RoutingListMax {
			return fmt.Errorf("roger.require has more than %d entries", RoutingListMax)
		}
		for _, c := range caps {
			if c != "tools" && c != "vision" {
				return fmt.Errorf("roger.require: unknown capability %s", c)
			}
		}
	}
	if v, ok := r["region"]; ok && v != nil {
		regs, err := idList(v)
		if err != nil || len(regs) == 0 {
			return fmt.Errorf("roger.region must be a list of lowercase regions")
		}
		if len(regs) > RoutingListMax {
			return fmt.Errorf("roger.region has more than %d entries", RoutingListMax)
		}
		for _, x := range regs {
			if !regionTokenRE.MatchString(x) {
				return fmt.Errorf("roger.region: %s is not a lowercase region", x)
			}
		}
	}
	if v, ok := r["params_b"]; ok && v != nil {
		arr, isArr := v.([]any)
		if !isArr || len(arr) != 2 {
			return fmt.Errorf("roger.params_b must be [min, max]")
		}
		a, okA := arr[0].(float64)
		b, okB := arr[1].(float64)
		// 0 is allowed only as the lower bound: [0, N] means "up to N" (founder ruling 2026-10-04).
		if !okA || !okB || a < 0 || b <= 0 || a > b {
			return fmt.Errorf("roger.params_b must be [min, max] with 0 <= min <= max and max > 0")
		}
	}
	for _, k := range []string{"min_ctx", "max_ttft_ms"} {
		if v, ok := r[k]; ok && v != nil {
			if f, isNum := v.(float64); !isNum || f <= 0 || f != math.Trunc(f) || f > RoutingIntMax {
				return fmt.Errorf("roger.%s must be a positive integer", k)
			}
		}
	}
	if v, ok := r["min_tps"]; ok && v != nil {
		if f, isNum := v.(float64); !isNum || f < 0 {
			return fmt.Errorf("roger.min_tps must be >= 0")
		}
	}
	for _, k := range []string{"self_hosted_only", "confidential"} {
		if v, ok := r[k]; ok && v != nil {
			if _, isBool := v.(bool); !isBool {
				return fmt.Errorf("roger.%s must be a boolean", k)
			}
		}
	}
	if v, ok := r["freq"]; ok && v != nil {
		if _, isStr := v.(string); !isStr {
			return fmt.Errorf("roger.freq must be a string")
		}
	}
	return nil
}

func idList(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		if ss, isSS := v.([]string); isSS {
			arr = make([]any, len(ss))
			for i, s := range ss {
				arr[i] = s
			}
		} else {
			return nil, fmt.Errorf("not a list")
		}
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, isStr := e.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("empty entry")
		}
		out = append(out, s)
	}
	return out, nil
}
