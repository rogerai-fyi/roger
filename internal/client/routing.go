package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"rogerai.fm/roger/v6/internal/protocol"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/operator"
)

// Routing is the consumer-side routing object a first-party client puts in the request
// BODY (features/routing/ROUTING-EXPRESSION-CONTRACT.md §1, §9): the `roger{...}` knobs and
// the OpenRouter-shaped `provider{...}` keys. It replaces the X-Roger-* request headers as
// the carrier; the header form is kept only for an OLD broker (HeaderMode, negotiated at
// tune time by NegotiateRouting) and, for one release, the X-Roger-Max-Price-Out cap, which
// every attempt still sends as a header so an old broker applies the consumer cap.
//
// Zero fields are ABSENT keys, never `false`/`0`/`[]` on the wire: a default can never
// weaken a restriction, and an empty quant list would read as "no filter" (§1a, §5).
type Routing struct {
	Pref           string   // roger.pref: cheap | balanced | fast | reliable ("" = balanced)
	MinTPS         float64  // roger.min_tps: measured tok/s floor
	Confidential   bool     // roger.confidential: TEE-attested stations only
	SelfHostedOnly bool     // roger.self_hosted_only: curated (commercial-proxy) stations excluded
	Quantizations  []string // provider.quantizations: verbatim labels; "unknown" admits unlabeled offers
	Order          []string // provider.order: the failover's preferred alternative, never a pin
	Ignore         []string // provider.ignore: the failed set + the caller's standing exclusions
	// MaxOut / MaxIn are the session OWNER's price caps ($/1M): provider.max_price.completion
	// (always the owner's EFFECTIVE out cap on the proxy path, so never 0 there) and
	// provider.max_price.prompt (0 = the owner set none). A caller may only tighten them.
	MaxOut, MaxIn float64
	// MaxReq is the owner's per-request USD cap (provider.max_price.request; 0 = none): a
	// ceiling a caller may only lower.
	MaxReq float64
	// TrustMin / Region / Only / Models are the owner's ceilings (roger use --trust --region
	// --only --models). A caller may only tighten them: a lower trust is raised to the owner's,
	// a region or an order outside the owner's set is refused, an only list is intersected, and
	// a models[] list is filtered to the owner's.
	TrustMin string
	Region   []string
	Only     []string
	Models   []string
	// The owner's remaining routing (roger use --sort --order/--node --no-fallbacks --require
	// --params --min-ctx --max-ttft, or a profile): a guest may restate each only toward
	// stricter. Sort is a default (set when the guest states neither a sort nor a pref);
	// Prefer is the owner's provider.order (the failover's Order wins on a re-pick); the
	// requirement list is unioned, the params range intersected, the context floor raised,
	// the first-token ceiling lowered, and the two booleans forced.
	Sort          string
	Prefer        []string
	NoFallbacks   bool
	Require       []string
	ParamsB       []float64 // [min, max] billions; nil = none
	MinCtx        int
	MaxTTFT       int
	RequireParams bool
	// FreeOnly is the booth's F filter: the model carries the `:free` variant, so only a
	// station free right now may serve (re-checked by the broker on every turn).
	FreeOnly bool
	// HeaderMode speaks the pre-body wire (X-Roger-* headers) to a broker whose GET
	// /v1/models answered 404 at tune time. Keys with no header form are dropped (Dropped).
	HeaderMode bool
}

// RoutingPrefs are the four values roger.pref / X-Roger-Pref accept; anything else is
// refused at the CLI before a request is made (regression_pins.feature, defect 1).
var RoutingPrefs = []string{"cheap", "balanced", "fast", "reliable"}

// ValidPref reports whether p is one of RoutingPrefs (or empty = unset).
func ValidPref(p string) bool {
	if p == "" {
		return true
	}
	for _, v := range RoutingPrefs {
		if v == p {
			return true
		}
	}
	return false
}

// QuantUnknown is the quantizations value that admits an UNLABELED offer (contract §5): a
// standing quant RULE is sent as its labels plus this, so an unlabeled station keeps passing
// the rule exactly as Limit.acceptsQuant reads absence; a tuned ROW is sent without it.
const QuantUnknown = "unknown"

// RuleQuantizations is the body form of a standing quant rule: the labels plus "unknown".
// nil in, nil out (no rule = no key).
func RuleQuantizations(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	return append(append([]string{}, labels...), QuantUnknown)
}

// RoutingRefusal is Apply's answer to a caller body that tries to WIDEN the session owner's
// routing (a quant label outside the owner's rule): the proxy refuses it locally with an
// OpenAI-shaped 400 carrying Msg, and nothing reaches the broker (contract §9).
type RoutingRefusal struct{ Msg string }

func (e *RoutingRefusal) Error() string { return e.Msg }

// GuestModelsWithin enforces "a guest may only tighten" on a caller's models[] (founder
// ruling 2026-10-02): every entry's bare id (variant sugar :free / :floor / :nitro removed)
// must be the tuned band's model, or the proxy refuses locally - a guest's list would
// otherwise reach models the owner never tuned, billed to the owner. A body with no models
// key, or a session with no tuned model (legacy single-user), passes.
//
// The same rule covers the guest's own `model` (founder ruling 2026-10-02): when the body
// carries a routing carrier, a model whose bare id is not the tuned band's is refused. Without
// a carrier a foreign id is simply rewritten to the band model (model_rewrite.feature). An
// empty model and a client-side `@profile/` reference are left to the rewrite.
func GuestModelsWithin(body []byte, tuned string) error {
	if tuned == "" {
		return nil
	}
	// The band may carry a variant suffix (`roger use m:free`); a guest names the bare id.
	tuned = sugarless(tuned) // the band's own id, prefix and all
	// Decoded key by key: a struct decode that fails on a mistyped model would fill the rest
	// and look like "nothing to check". A body that is not an object is the rewrite's 400.
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top == nil {
		return nil
	}
	// (A model that is not a model id was refused by the handler first: mistypedGuestModel.)
	var m struct {
		Model  string
		Models json.RawMessage
	}
	if raw, ok := top["model"]; ok {
		_ = json.Unmarshal(raw, &m.Model)
	}
	m.Models = top["models"]
	if guestNamesOtherModel(body, m.Model, tuned) {
		return &RoutingRefusal{Msg: "model " + m.Model + " is outside this session's band"}
	}
	if len(m.Models) == 0 || string(m.Models) == "null" {
		return nil
	}
	var entries []any
	if json.Unmarshal(m.Models, &entries) != nil {
		return &RoutingRefusal{Msg: "models must be a list of model ids"}
	}
	for _, e := range entries {
		id, ok := e.(string)
		if !ok {
			return &RoutingRefusal{Msg: "models must be a list of model ids"}
		}
		if bareModel(id) != tuned {
			return &RoutingRefusal{Msg: "model " + id + " is outside this session's band"}
		}
	}
	return nil
}

// mistypedGuestModel refuses a body whose model is present, not null, and not a string. A
// body that is not a JSON object is left to the rewrite's 400.
func mistypedGuestModel(body []byte) error {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top == nil {
		return nil
	}
	if raw, ok := top["model"]; ok && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return &RoutingRefusal{Msg: "model must be a model id"}
		}
	}
	return nil
}

// guestStatesSort reports whether a caller body already names a strict sort: provider.sort,
// or a :floor / :nitro suffix on its model or any models[] entry.
func guestStatesSort(m map[string]json.RawMessage, provider map[string]any) bool {
	if v, ok := provider["sort"]; ok && v != nil {
		return true
	}
	ids := []string{}
	var model string
	if json.Unmarshal(m["model"], &model) == nil {
		ids = append(ids, model)
	}
	var models []string
	if json.Unmarshal(m["models"], &models) == nil {
		ids = append(ids, models...)
	}
	for _, id := range ids {
		id = guestModelID(id)
		if b := sugarless(id); b != id && (strings.Contains(id[len(b):], ":floor") || strings.Contains(id[len(b):], ":nitro")) {
			return true
		}
	}
	return false
}

// bareModel strips the routing sugar suffixes (contract §4), right to left.
// hasCarrier reports whether a body carries a non-null routing carrier (models / provider /
// roger): the signal that the caller is routing explicitly rather than sending a stock body.
func hasCarrier(body []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	for _, k := range []string{"models", "provider", "roger"} {
		if v, ok := m[k]; ok && string(v) != "null" {
			return true
		}
	}
	return false
}

// guestNamesOtherModel: a carrier-bearing body names a model outside the tuned band.
func guestNamesOtherModel(body []byte, model, tuned string) bool {
	if model == "" || strings.HasPrefix(guestModelID(model), ProfileRef) || bareModel(model) == sugarless(tuned) {
		return false
	}
	return hasCarrier(body)
}

// guestModelID strips the provider prefix a materialized guest puts in front of the band's
// model id (operator.ModelPrefixes: hermes/opencode "roger/", aider "openai/"). Any other
// prefix is part of the id.
func guestModelID(id string) string {
	for _, p := range operator.ModelPrefixes {
		if rest, ok := strings.CutPrefix(id, p); ok && rest != "" {
			return rest
		}
	}
	return id
}

// bareModel is a GUEST's model id without its guest-tool provider prefix and variant
// suffixes. A band, offer or owner id is never prefix-stripped (a station id may itself start
// with openai/ or roger/): those go through sugarless.
func bareModel(id string) string { return sugarless(guestModelID(id)) }

// sugarless is an id without its variant suffixes (:free / :floor / :nitro), nothing else.
func sugarless(id string) string {
	for {
		trimmed := id
		for _, sfx := range []string{":free", ":floor", ":nitro"} {
			trimmed = strings.TrimSuffix(trimmed, sfx)
		}
		if trimmed == id {
			return id
		}
		id = trimmed
	}
}

// Apply merges r into a JSON request body, keeping every other field's raw value
// byte-identical. r is the session OWNER's routing; a caller (guest) that already sent
// `roger` / `provider` may only TIGHTEN it (§1a, §9): `pref` is a default the caller may
// override; `min_tps` is the max of the two; `confidential` / `self_hosted_only` are OR'd;
// `ignore` is unioned; `quantizations` from the caller must be a subset of the owner's rule
// (case-insensitive; "unknown" is a label) or Apply returns a *RoutingRefusal; a caller
// `freq` is never taken (the owner's band stands; the code travels as the X-Roger-Freq
// header, not in the body); the owner's price caps (MaxOut, MaxIn) are a ceiling: a caller
// `provider.max_price.completion` / `.prompt` above them is clamped (one log line), one
// below is kept, and the effective cap is always written. The body is always re-encoded
// (every value's raw JSON is kept byte-identical; top-level key order may change), even for
// a zero Routing; a body that is not a JSON object is an error.
// mistyped reports whether obj states key with a value of the wrong type (absent or null is
// not mistyped: the owner's value applies).
func mistyped(obj map[string]any, key string, ok func(any) bool) bool {
	v, present := obj[key]
	return present && v != nil && !ok(v)
}

func isNumber(v any) bool { _, ok := v.(float64); return ok }

func isBool(v any) bool { _, ok := v.(bool); return ok }

func isNumberPair(v any) bool {
	pair, ok := v.([]any)
	return ok && len(pair) == 2 && isNumber(pair[0]) && isNumber(pair[1])
}

func isTrustValue(v any) bool {
	s, _ := v.(string)
	return s == "any" || s == "verified" || s == "confidential"
}

func isStringList(v any) bool {
	list, ok := v.([]any)
	for _, e := range list {
		if _, isStr := e.(string); !isStr {
			return false
		}
	}
	return ok
}

func (r Routing) Apply(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, fmt.Errorf("request body is not a JSON object")
	}
	// A carrier that is present but not an object, or that the proxy cannot decode (a number
	// out of range), is refused here (founder ruling 2026-10-07). Forwarding it would skip
	// every owner rule, and the broker's reader can accept a body the proxy cannot read (a
	// duplicate key whose last value decodes), so the broker's 400 is not a guarantee.
	for _, k := range []string{"provider", "roger"} {
		if raw, ok := m[k]; ok && string(raw) != "null" && (!rawObjectOK(raw) || !decodes(raw)) {
			return nil, &RoutingRefusal{Msg: k + ": an undecodable routing object is refused locally"}
		}
	}
	// A key twice inside a routing object is refused, as the broker and the local plane do.
	if k := protocol.DuplicateRoutingKey(body); k != "" {
		return nil, &RoutingRefusal{Msg: "invalid routing value for " + k + ": duplicate key"}
	}
	roger, provider := rawObject(m["roger"]), rawObject(m["provider"])
	// The owner's pref is a DEFAULT, and a sort the guest stated (provider.sort, or a :floor /
	// :nitro suffix on any model id) excludes pref (§1a), so adding it would earn the guest a
	// self-inflicted 400 conflicting_routing_keys.
	if r.Pref != "" && !guestStatesSort(m, provider) {
		setDefault(roger, "pref", r.Pref)
	}
	if r.MinTPS > 0 && !mistyped(roger, "min_tps", isNumber) {
		if f, ok := roger["min_tps"].(float64); !ok || f < r.MinTPS {
			roger["min_tps"] = r.MinTPS
		}
	}
	if r.Confidential && !mistyped(roger, "confidential", isBool) {
		if v, ok := roger["confidential"].(bool); ok && !v {
			log.Printf("guest confidential=false ignored: owner requires confidential")
		}
		roger["confidential"] = true
	}
	// A guest value of the wrong type is forwarded as sent, for the broker's 400 (as a
	// non-object max_price is): replacing it with the owner's would hide the guest's error.
	if r.TrustMin != "" && !mistyped(roger, "trust_min", isTrustValue) {
		if g, _ := roger["trust_min"].(string); trustRank(g) < trustRank(r.TrustMin) {
			roger["trust_min"] = r.TrustMin
		}
	}
	if len(r.Region) > 0 && !mistyped(roger, "region", isStringList) {
		if got := stringsOf(roger["region"]); len(got) > 0 {
			for _, x := range got {
				if !slices.Contains(r.Region, x) {
					return nil, &RoutingRefusal{Msg: "region " + x + " is outside this session's allowed regions"}
				}
			}
		} else {
			roger["region"] = r.Region
		}
	}
	delete(roger, "freq") // the owner's band stands; a guest cannot drop or swap it
	if r.SelfHostedOnly && !mistyped(roger, "self_hosted_only", isBool) {
		roger["self_hosted_only"] = true
	}
	if len(r.Quantizations) > 0 && !mistyped(provider, "quantizations", isStringList) {
		if got := stringsOf(provider["quantizations"]); len(got) > 0 {
			for _, q := range got {
				if !hasFold(r.Quantizations, q) {
					return nil, &RoutingRefusal{Msg: "quantization " + q + " is outside this session's quant rule"}
				}
			}
		} else {
			provider["quantizations"] = r.Quantizations
		}
	}
	if len(r.Only) > 0 && !mistyped(provider, "only", isStringList) {
		g, stated := provider["only"]
		guestOnly := stated && g != nil // an explicit [] is the guest's, forwarded for the broker's 400
		if err := capStations(provider, r.Only, "allowed"); err != nil {
			return nil, err
		}
		if !guestOnly {
			provider["only"] = r.Only
		}
	}
	if len(r.Models) > 0 {
		var ids []string
		if raw, present := m["models"]; present && string(raw) != "null" && json.Unmarshal(raw, &ids) != nil {
			return nil, &RoutingRefusal{Msg: "models must be a list of model ids"}
		}
		if len(ids) > 0 {
			// The band's own model (the request's model) is always routable, so it counts as
			// inside the owner's set.
			// Compared on the bare id: an owner entry written with a variant (a:free) is still a.
			allowed := make([]string, 0, len(r.Models)+1)
			for _, id := range r.Models {
				allowed = append(allowed, sugarless(id)) // an owner's id
			}
			var band string
			if json.Unmarshal(m["model"], &band) == nil && band != "" {
				allowed = append(allowed, sugarless(band))
			}
			kept := []string{}
			for _, id := range ids {
				if id == "" || id != strings.TrimSpace(id) { // padded or empty: refused, as on the broker
					return nil, &RoutingRefusal{Msg: "models must be a list of model ids"}
				}
				if slices.Contains(allowed, bareModel(id)) { // exact, as the broker and GuestModelsWithin compare
					kept = append(kept, id)
				}
			}
			// An empty filtered list would reach the broker as "no list" and route the owner's
			// whole set: a guest list with no model inside the owner's is refused here.
			if len(kept) == 0 {
				return nil, &RoutingRefusal{Msg: "models names no model inside this session's allowed models"}
			}
			enc, _ := json.Marshal(kept)
			m["models"] = enc
		} else {
			enc, _ := json.Marshal(r.Models)
			m["models"] = enc
		}
	}
	if r.NoFallbacks && len(r.Prefer) > 0 {
		// With no fallbacks the owner's order is the whole routable set: a ceiling like --only.
		if err := capStations(provider, r.Prefer, "pinned"); err != nil {
			return nil, err
		}
	}
	if len(r.Order) > 0 {
		provider["order"] = r.Order
	} else if len(r.Prefer) > 0 {
		// A default the guest's only can narrow: an order naming a station outside only is a
		// broker 400 for what is a legitimate tightening. Nothing left means no order.
		order := r.Prefer
		if only := stringsOf(provider["only"]); len(only) > 0 {
			order = nil
			for _, x := range r.Prefer {
				if slices.Contains(only, x) {
					order = append(order, x)
				}
			}
		}
		if len(order) > 0 {
			setDefault(provider, "order", order)
		}
	}
	if r.Sort != "" && !guestStatesSort(m, provider) && roger["pref"] == nil {
		provider["sort"] = r.Sort
	}
	if r.NoFallbacks && !mistyped(provider, "allow_fallbacks", isBool) {
		provider["allow_fallbacks"] = false
	}
	if r.RequireParams && !mistyped(provider, "require_parameters", isBool) {
		provider["require_parameters"] = true
	}
	if len(r.Require) > 0 && !mistyped(roger, "require", isStringList) {
		roger["require"] = unionStrings(stringsOf(roger["require"]), r.Require)
	}
	if len(r.ParamsB) == 2 && !mistyped(roger, "params_b", isNumberPair) {
		lo, hi := r.ParamsB[0], r.ParamsB[1]
		if g, isArr := roger["params_b"].([]any); isArr && len(g) == 2 {
			if a, ok := g[0].(float64); ok && a > lo {
				lo = a
			}
			if b, ok := g[1].(float64); ok && b < hi {
				hi = b
			}
			if lo > hi {
				return nil, &RoutingRefusal{Msg: "params_b is outside this session's allowed range"}
			}
		}
		roger["params_b"] = []float64{lo, hi}
	}
	if r.MinCtx > 0 && !mistyped(roger, "min_ctx", isNumber) {
		if g, ok := roger["min_ctx"].(float64); !ok || g < float64(r.MinCtx) {
			roger["min_ctx"] = r.MinCtx
		}
	}
	if r.MaxTTFT > 0 && !mistyped(roger, "max_ttft_ms", isNumber) {
		if g, ok := roger["max_ttft_ms"].(float64); !ok || g <= 0 || g > float64(r.MaxTTFT) {
			roger["max_ttft_ms"] = r.MaxTTFT
		}
	}
	if len(r.Ignore) > 0 && !mistyped(provider, "ignore", isStringList) {
		provider["ignore"] = unionStrings(stringsOf(provider["ignore"]), r.Ignore)
	}
	if r.MaxOut > 0 || r.MaxIn > 0 || r.MaxReq > 0 {
		// The owner's price caps are the ceiling. A max_price that is not an object is left
		// for the broker to refuse (never silently repaired).
		mp, isObj := provider["max_price"].(map[string]any)
		if _, present := provider["max_price"]; !present || provider["max_price"] == nil {
			mp, isObj = map[string]any{}, true
		}
		if isObj {
			capPrice(mp, "completion", r.MaxOut)
			capPrice(mp, "prompt", r.MaxIn)
			capPrice(mp, "request", r.MaxReq)
			provider["max_price"] = mp
		}
	}
	if r.FreeOnly {
		// The broker applies :free per entry, so every models[] fallback carries it too.
		free := func(id string) string {
			if id == "" || hasFreeSugar(id) { // :free anywhere in stacked sugar (m:free:nitro)
				return id
			}
			return id + ":free"
		}
		var model string
		if json.Unmarshal(m["model"], &model) == nil {
			m["model"], _ = json.Marshal(free(model))
		}
		var ids []string
		if json.Unmarshal(m["models"], &ids) == nil && len(ids) > 0 {
			for i, id := range ids {
				ids[i] = free(id)
			}
			m["models"], _ = json.Marshal(ids)
		}
	}
	putObject(m, "roger", roger)
	putObject(m, "provider", provider)
	return json.Marshal(m)
}

// capPrice applies the session owner's cap on one price axis of a caller's max_price: an
// absent / null / zero caller value states no cap, so the owner's is written; a caller value
// above the owner's is clamped to it (logged once, the caller is not told); one at or below
// it is the caller tightening and is kept. owner <= 0 means the owner set none on this axis
// and the caller's value passes as given. A non-number is left for the broker to refuse.
func capPrice(mp map[string]any, axis string, owner float64) {
	if owner <= 0 {
		return
	}
	switch g := mp[axis].(type) {
	case nil:
		mp[axis] = owner
	case float64:
		if g <= 0 {
			mp[axis] = owner
		} else if g > owner {
			log.Printf("guest max_price.%s %g clamped to owner cap %g", axis, g, owner)
			mp[axis] = owner
		}
	}
}

// liftPrice is capPrice for the header wire: the caller's value on one axis folded into the
// cap the session sends as a header (owner 0 = none set: the caller's value becomes the cap).
func liftPrice(v any, axis string, owner float64) float64 {
	g, isNum := v.(float64)
	if !isNum || g <= 0 {
		return owner
	}
	if owner > 0 && g > owner {
		log.Printf("guest max_price.%s %g clamped to owner cap %g", axis, g, owner)
		return owner
	}
	return g
}

// trustRank orders roger.trust_min values: any < verified < confidential ("" = any).
func trustRank(t string) int {
	switch t {
	case "verified":
		return 1
	case "confidential":
		return 2
	}
	return 0
}

// RoutingKeysAdded names the routing keys present in sent but absent from orig (the proxy's
// own defaults), dotted and sorted: "provider.max_price.completion", "roger.min_tps", ... The
// failover's own provider.order hint is never a default.
func RoutingKeysAdded(orig, sent []byte) []string {
	flat := func(b []byte) map[string]bool {
		out := map[string]bool{}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			return out
		}
		if _, ok := m["models"]; ok {
			out["models"] = true
		}
		for _, top := range []string{"provider", "roger"} {
			obj, _ := m[top].(map[string]any)
			for k, v := range obj {
				if mp, isObj := v.(map[string]any); isObj && k == "max_price" {
					for sk := range mp {
						out[top+"."+k+"."+sk] = true
					}
					continue
				}
				out[top+"."+k] = true
			}
		}
		return out
	}
	o, s := flat(orig), flat(sent)
	var added []string
	for k := range s {
		if !o[k] && k != "provider.order" {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	return added
}

// hasFold reports whether list contains s, comparing case-insensitively. For quant labels only
// (a Q8_0 and a q8_0 are the same weights); station ids, regions and capabilities compare
// exactly, as the broker compares them.
func hasFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

// Dropped names the session OWNER's keys the header wire cannot carry, so a caller on an
// old broker is told (X-Roger-Routing-Dropped) instead of silently losing a constraint. The
// failover's own `provider.order` hint is the proxy's, never the caller's, so it is not
// reported.
func (r Routing) Dropped() []string {
	var d []string
	if r.SelfHostedOnly {
		d = append(d, "roger.self_hosted_only")
	}
	if len(r.Quantizations) > 0 {
		d = append(d, "provider.quantizations")
	}
	for _, k := range []struct {
		key string
		on  bool
	}{
		{"models", len(r.Models) > 0}, {"provider.only", len(r.Only) > 0}, {"provider.order", len(r.Prefer) > 0},
		{"provider.allow_fallbacks", r.NoFallbacks}, {"provider.sort", r.Sort != ""},
		{"provider.require_parameters", r.RequireParams}, {"provider.max_price.request", r.MaxReq > 0},
		{"roger.require", len(r.Require) > 0}, {"roger.params_b", len(r.ParamsB) == 2},
		{"roger.min_ctx", r.MinCtx > 0}, {"roger.max_ttft_ms", r.MaxTTFT > 0},
		{"roger.trust_min", r.TrustMin != ""}, {"roger.region", len(r.Region) > 0}, {"model:free", r.FreeOnly},
	} {
		if k.on {
			d = append(d, k.key)
		}
	}
	return d
}

// RoutingFlag names the `roger use` flag that sets a routing body key ("" when none does).
func RoutingFlag(key string) string {
	return map[string]string{
		"models": "--models", "provider.only": "--only", "provider.order": "--order",
		"provider.ignore": "--exclude", "provider.allow_fallbacks": "--no-fallbacks", "provider.sort": "--sort",
		"provider.quantizations": "--quant", "provider.max_price.prompt": "--max-in",
		"provider.max_price.completion": "--max-out", "provider.max_price.request": "--max-cost",
		"roger.pref": "--pref", "roger.require": "--require", "roger.params_b": "--params",
		"roger.min_ctx": "--min-ctx", "roger.max_ttft_ms": "--max-ttft", "roger.trust_min": "--trust",
		"roger.self_hosted_only": "--self-hosted", "roger.region": "--region", "roger.min_tps": "--min-tps",
		"roger.confidential": "--confidential", "roger.freq": "--freq",
	}[key]
}

// SetHeaders writes the pre-body wire form onto req (HeaderMode). The failover's preferred
// alternative has no header equivalent that is not a pin, so it is dropped rather than sent
// as X-Roger-Node (a pin disables the broker's own failover - defect 8).
func (r Routing) SetHeaders(req *http.Request) {
	if r.Confidential {
		req.Header.Set("X-Roger-Confidential", "1")
	}
	if r.MinTPS > 0 {
		req.Header.Set("X-Roger-Min-TPS", fmt.Sprintf("%g", r.MinTPS))
	}
	if r.Pref != "" {
		req.Header.Set("X-Roger-Pref", r.Pref)
	}
	if ex := strings.Join(unionStrings(r.Ignore), ","); ex != "" {
		req.Header.Set("X-Roger-Exclude-Nodes", ex)
	}
}

// carry puts r on one relay attempt: the body carriers (body mode) or the header form
// (HeaderMode). Returns the body to send; the caller's body is returned unchanged in header
// mode. The error is Apply's: a *RoutingRefusal, or malformed JSON.
func (r Routing) carry(req *http.Request, body []byte) ([]byte, error) {
	if r.HeaderMode {
		r.SetHeaders(req)
		return body, nil
	}
	return r.Apply(body)
}

// CarryRouting is carry for the other first-party paths (the agent harness), so every
// in-booth request speaks the one carrier policy.
func CarryRouting(r Routing, req *http.Request, body []byte) ([]byte, error) {
	return r.carry(req, body)
}

func rawObject(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func setDefault(m map[string]any, k string, v any) {
	if cur, ok := m[k]; !ok || cur == nil {
		m[k] = v
	}
}

func putObject(m map[string]json.RawMessage, k string, obj map[string]any) {
	if len(obj) == 0 {
		delete(m, k)
		return
	}
	enc, _ := json.Marshal(obj)
	m[k] = enc
}

func stringsOf(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// unionStrings is the sorted, de-duplicated union (a stable wire order); nil when empty.
func unionStrings(lists ...[]string) []string {
	set := map[string]bool{}
	for _, l := range lists {
		for _, s := range l {
			if s = strings.TrimSpace(s); s != "" {
				set[s] = true
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// NegotiateRouting probes GET <broker>/v1/models once at TUNE time (contract §9): 200 = the
// broker reads the body carriers (body mode); 404 = an old broker (header mode); anything
// else assumes body mode and logs one line. A positive signal, never a 400 sniff: an old
// broker forwards unknown top-level keys to the station untouched, so it would never answer
// unknown_routing_key. The result lives in the session's ProxyOptions (HeaderRouting) and is
// never persisted.
func NegotiateRouting(broker string) (headerMode bool) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(broker + "/v1/models")
	if err != nil {
		log.Printf("routing mode probe failed (%v), assuming body mode", err)
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusOK:
		return false
	case http.StatusNotFound:
		return true
	default:
		log.Printf("routing mode probe failed (%d), assuming body mode", resp.StatusCode)
		return false
	}
}

// bandCoolingWait is the longest band-cooling Retry-After the proxy waits out on the
// caller's behalf (regression_pins.feature, defect 8): a 503 band_cooling with Retry-After
// <= this is waited ONCE, then retried; longer, or a second one, is passed through.
const bandCoolingWait = 5 * time.Second

// bandCooling reads a response body and reports whether it is the broker's 503 band_cooling
// refusal and how long it asked to wait. The body is consumed and returned so a pass-through
// can replay it verbatim.
func bandCooling(resp *http.Response) (raw []byte, wait time.Duration, ok bool) {
	raw, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusServiceUnavailable {
		return raw, 0, false
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Error.Code != "band_cooling" {
		return raw, 0, false
	}
	secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || secs < 0 {
		return raw, 0, false
	}
	return raw, time.Duration(secs) * time.Second, true
}

// passThrough replays an upstream response the proxy decided not to retry (its status,
// headers and the already-read body) to the caller unchanged.
func passThrough(w http.ResponseWriter, resp *http.Response, raw []byte) {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	copyAllowedHeaders(w, resp) // the same allowlist as a relayed reply
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, bytes.NewReader(raw))
}

// foldCallerCarriers is the HeaderMode treatment of a CALLER's own body carriers (a guest
// that sent `roger` / `provider` / `models` to an old broker): keys with a header form are
// lifted into r (min_tps raised never lowered, confidential, pref, ignore, the two price caps
// lowered never raised), the rest are
// removed from the body and NAMED so the guest is told (dropped), and a `models` list, which
// the header wire cannot express at all, is reported (hasModels) for the caller to refuse
// honestly. r arrives seeded with the session owner's routing so the same ceiling rules as
// Apply hold. The error is a *RoutingRefusal (a guest quant outside the owner's rule) or
// malformed JSON.
func (r *Routing) foldCallerCarriers(body []byte) (out []byte, dropped []string, hasModels bool, err error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, nil, false, fmt.Errorf("request body is not a JSON object")
	}
	if raw, present := m["models"]; present && len(raw) > 0 && string(raw) != "null" {
		hasModels = true
	}
	roger, provider := rawObject(m["roger"]), rawObject(m["provider"])
	for k, v := range roger {
		switch k {
		case "min_tps":
			if f, isNum := v.(float64); isNum && f > r.MinTPS {
				r.MinTPS = f // the owner's floor is a ceiling the guest may only raise
			}
		case "confidential":
			if b, isBool := v.(bool); isBool && b {
				r.Confidential = true
			}
		case "pref":
			if p, isStr := v.(string); isStr && p != "" {
				r.Pref = p
			}
		case "freq":
			// the owner's band stands (never logged: the value is a band code)
		default:
			dropped = append(dropped, "roger."+k)
		}
	}
	for k, v := range provider {
		switch k {
		case "ignore":
			r.Ignore = unionStrings(r.Ignore, stringsOf(v))
		case "quantizations":
			// The header wire cannot carry a quant list, but a guest label outside the
			// owner's rule is a widening and is refused the same way as in body mode.
			if len(r.Quantizations) > 0 {
				for _, q := range stringsOf(v) {
					if !hasFold(r.Quantizations, q) {
						return nil, nil, false, &RoutingRefusal{Msg: "quantization " + q + " is outside this session's quant rule"}
					}
				}
			}
			dropped = append(dropped, "provider."+k)
		case "max_price":
			// The two per-token caps have header forms (X-Roger-Max-Price-Out / -Max-Price):
			// lifted under the owner's ceiling, tightened never raised. Anything else in the
			// object has no header form and is named.
			mp, isObj := v.(map[string]any)
			if !isObj {
				dropped = append(dropped, "provider."+k)
				break
			}
			for ax, raw := range mp {
				switch ax {
				case "completion":
					r.MaxOut = liftPrice(raw, ax, r.MaxOut)
				case "prompt":
					r.MaxIn = liftPrice(raw, ax, r.MaxIn)
				default:
					dropped = append(dropped, "provider.max_price."+ax)
				}
			}
		default:
			dropped = append(dropped, "provider."+k)
		}
	}
	delete(m, "roger")
	delete(m, "provider")
	delete(m, "models")
	sort.Strings(dropped)
	out, err = json.Marshal(m)
	return out, dropped, hasModels, err
}

// guestNamesOtherModelOf is guestNamesOtherModel reading the body's own model.
func guestNamesOtherModelOf(body []byte, tuned string) bool {
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	return guestNamesOtherModel(body, m.Model, tuned)
}

// rawObjectOK reports whether raw JSON is an object.
// decodes reports whether a carrier decodes in full (a number out of range does not).
func decodes(raw json.RawMessage) bool {
	var m map[string]any
	return json.Unmarshal(raw, &m) == nil
}

func rawObjectOK(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}

// capStations holds a guest's provider.order and provider.only inside the owner's station
// set: an order naming a station outside it is refused, an only list is intersected with it
// (and refused when nothing is left, since an empty list would read as no filter).
func capStations(provider map[string]any, set []string, kind string) *RoutingRefusal {
	for _, x := range stringsOf(provider["order"]) {
		if !slices.Contains(set, x) {
			return &RoutingRefusal{Msg: "order names " + x + ", outside this session's " + kind + " stations"}
		}
	}
	got := stringsOf(provider["only"])
	if len(got) == 0 {
		return nil
	}
	kept := []string{}
	for _, x := range got {
		if slices.Contains(set, x) {
			kept = append(kept, x)
		}
	}
	if len(kept) == 0 {
		return &RoutingRefusal{Msg: "only names no station inside this session's " + kind + " stations"}
	}
	provider["only"] = kept
	return nil
}
