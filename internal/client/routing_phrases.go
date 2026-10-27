package client

import (
	"fmt"
	"strings"
)

// RoutingPhrases renders the body's routing keys in a fixed order, one short phrase each, with
// the dotted key it came from. A band code is never rendered.
func RoutingPhrases(b map[string]any) [][2]string {
	var out [][2]string
	add := func(key, text string) { out = append(out, [2]string{key, text}) }
	ids := func(p string) string {
		v, _ := bodyPath(b, p)
		l := stringsOf(v)
		if len(l) > 3 {
			return strings.Join(l[:3], ",") + fmt.Sprintf(" +%d", len(l)-3)
		}
		return strings.Join(l, ",")
	}
	has := func(p string) bool { v, ok := bodyPath(b, p); return ok && v != nil }
	get := func(p string) any { v, _ := bodyPath(b, p); return v }
	if has("roger.pref") {
		add("roger.pref", fmt.Sprint(get("roger.pref")))
	}
	if has("provider.sort") {
		add("provider.sort", fmt.Sprint(get("provider.sort")))
	}
	for _, k := range []struct{ key, word string }{
		{"models", "models"}, {"provider.only", "only"}, {"provider.order", "order"}, {"provider.ignore", "exclude"},
	} {
		if has(k.key) {
			add(k.key, k.word+" "+ids(k.key))
		}
	}
	if v, ok := get("provider.allow_fallbacks").(bool); ok && !v {
		add("provider.allow_fallbacks", "no fallbacks")
	}
	if has("roger.require") {
		add("roger.require", "require "+ids("roger.require"))
	}
	if a, _ := get("roger.params_b").([]any); len(a) == 2 {
		add("roger.params_b", fmt.Sprintf("%g-%gB", a[0], a[1]))
	}
	if has("roger.min_ctx") {
		add("roger.min_ctx", fmt.Sprintf("ctx ≥ %g", get("roger.min_ctx")))
	}
	if has("roger.max_ttft_ms") {
		add("roger.max_ttft_ms", fmt.Sprintf("ttft ≤ %gms", get("roger.max_ttft_ms")))
	}
	if has("roger.trust_min") {
		add("roger.trust_min", fmt.Sprint("trust ", get("roger.trust_min")))
	}
	if v, _ := get("roger.self_hosted_only").(bool); v {
		add("roger.self_hosted_only", "self-hosted")
	}
	if has("roger.region") {
		add("roger.region", "region "+ids("roger.region"))
	}
	if has("provider.quantizations") {
		add("provider.quantizations", "quant "+ids("provider.quantizations"))
	}
	if v, _ := get("provider.require_parameters").(bool); v {
		add("provider.require_parameters", "require params")
	}
	if v, _ := get("roger.confidential").(bool); v {
		add("roger.confidential", "confidential")
	}
	if has("roger.min_tps") {
		add("roger.min_tps", fmt.Sprintf("≥%g t/s", get("roger.min_tps")))
	}
	if has("provider.max_price.prompt") {
		add("provider.max_price.prompt", fmt.Sprintf("in ≤ $%g/1M", get("provider.max_price.prompt")))
	}
	if has("provider.max_price.completion") {
		add("provider.max_price.completion", fmt.Sprintf("out ≤ $%g/1M", get("provider.max_price.completion")))
	}
	if has("provider.max_price.request") {
		add("provider.max_price.request", fmt.Sprintf("cost ≤ $%g", get("provider.max_price.request")))
	}
	if has("roger.freq") {
		add("roger.freq", "private freq")
	}
	return out
}

// bodyPath reads a dotted key of a body object.
func bodyPath(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Overlay lays the routing keys a body object sets (a profile) over r; keys it does not set
// keep r's value.
func (r Routing) Overlay(b map[string]any) Routing {
	str := func(p string) (string, bool) { v, _ := bodyPath(b, p); s, ok := v.(string); return s, ok && s != "" }
	num := func(p string) (float64, bool) { v, _ := bodyPath(b, p); f, ok := v.(float64); return f, ok }
	list := func(p string) ([]string, bool) { v, _ := bodyPath(b, p); l := stringsOf(v); return l, len(l) > 0 }
	boolean := func(p string) (bool, bool) { v, _ := bodyPath(b, p); x, ok := v.(bool); return x, ok }
	if v, ok := str("roger.pref"); ok {
		r.Pref, r.Sort = v, ""
	}
	if v, ok := str("provider.sort"); ok {
		r.Sort, r.Pref = v, ""
	}
	if v, ok := list("models"); ok {
		r.Models = v
	}
	if v, ok := list("provider.only"); ok {
		r.Only = v
	}
	if v, ok := list("provider.order"); ok {
		r.Prefer = v
	}
	if v, ok := list("provider.ignore"); ok {
		r.Ignore = unionStrings(r.Ignore, v)
	}
	if v, ok := boolean("provider.allow_fallbacks"); ok {
		r.NoFallbacks = !v
	}
	if v, ok := boolean("provider.require_parameters"); ok {
		r.RequireParams = v
	}
	if v, ok := list("provider.quantizations"); ok {
		r.Quantizations = v
	}
	if v, ok := num("provider.max_price.prompt"); ok {
		r.MaxIn = v
	}
	if v, ok := num("provider.max_price.completion"); ok {
		r.MaxOut = v
	}
	if v, ok := num("provider.max_price.request"); ok {
		r.MaxReq = v
	}
	if v, ok := list("roger.require"); ok {
		r.Require = v
	}
	if v, _ := bodyPath(b, "roger.params_b"); v != nil {
		if a, _ := v.([]any); len(a) == 2 {
			lo, _ := a[0].(float64)
			hi, _ := a[1].(float64)
			r.ParamsB = []float64{lo, hi}
		}
	}
	if v, ok := num("roger.min_ctx"); ok {
		r.MinCtx = int(v)
	}
	if v, ok := num("roger.max_ttft_ms"); ok {
		r.MaxTTFT = int(v)
	}
	if v, ok := num("roger.min_tps"); ok {
		r.MinTPS = v
	}
	if v, ok := str("roger.trust_min"); ok {
		r.TrustMin = v
	}
	if v, ok := boolean("roger.self_hosted_only"); ok {
		r.SelfHostedOnly = r.SelfHostedOnly || v
	}
	if v, ok := boolean("roger.confidential"); ok {
		r.Confidential = r.Confidential || v
	}
	if v, ok := list("roger.region"); ok {
		r.Region = v
	}
	return r
}
