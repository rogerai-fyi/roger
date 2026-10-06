package localplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"rogerai.fm/roger/v6/internal/client"
)

// The routing body object on the Core-free plane (ROUTING-EXPRESSION-CONTRACT §5a). The plane
// knows a station's attach id and model list ONLY, so it honors exactly the keys it can
// evaluate over those: models[], provider.only / ignore / order / allow_fallbacks (local attach
// ids). roger.confidential and roger.trust_min are EVALUATED, never ignored: nothing local is
// attested or canary-verified, so a request requiring either is a 503 no_match rather than
// served by a station that does not meet it. Every other contract key is ignored and NAMED in
// X-Roger-Routing-Ignored (names only, never values). "@profile/" resolves on the client and is
// a 400 here. The carriers never reach a station: the job body is the request minus models /
// provider / roger, with `model` set to the model being served.

const maxLocalModels = 5

// routeErr is a refusal decided before anything is queued.
type routeErr struct {
	status int
	code   string // "" = the plane's plain {"error": "<text>"} refusal
	msg    string
}

// localRouting is a parsed request.
type localRouting struct {
	models      []string        // effective list: model, then models[], bare ids, de-duplicated
	only        map[string]bool // nil = any station
	ignore      map[string]bool
	order       []string
	noFallbacks bool
	needAttest  bool // roger.confidential / trust_min confidential / X-Roger-Confidential
	needVerify  bool // trust_min verified
	ignored     []string
	raw         map[string]json.RawMessage // the request minus the carriers
	keys        []string                   // the request's top-level keys in document order
}

var (
	localProviderHonored = map[string]bool{"order": true, "only": true, "ignore": true, "allow_fallbacks": true}
	localProviderIgnored = map[string]bool{"sort": true, "quantizations": true, "max_price": true, "require_parameters": true}
	localRogerHonored    = map[string]bool{"confidential": true, "trust_min": true, "profile": true}
	localRogerIgnored    = map[string]bool{"pref": true, "require": true, "params_b": true, "min_ctx": true, "min_tps": true,
		"max_ttft_ms": true, "self_hosted_only": true, "region": true, "freq": true}
	localSugar = []string{":free", ":floor", ":nitro"}
	// maxModelID bounds one model id (contract §3); maxModelsEntries bounds models[] by raw
	// entries (§1a), as the broker does.
	maxModelID       = 256
	maxModelsEntries = 32
	maxPriceKeys     = map[string]bool{"prompt": true, "completion": true, "request": true, "image": true}
)

// parseLocalRouting reads the routing carriers of a consumer request. hdrConfidential is the
// X-Roger-Confidential request header.
// knownKeysInOrder returns a carrier's keys sorted, refusing the first unknown one (by name)
// before any value is read, so a body with several faults always gets the same 400. The order
// is alphabetical, where the broker reports in document order: the two can name a different
// one of several faults, never accept what the other refuses.
func knownKeysInOrder(carrier string, obj map[string]json.RawMessage, sets ...map[string]bool) ([]string, *routeErr) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		known := false
		for _, set := range sets {
			known = known || set[k]
		}
		if !known {
			return nil, &routeErr{status: 400, msg: "unknown routing key " + carrier + "." + k}
		}
	}
	return keys, nil
}

// validateCarriers runs the shared value rules (client.ValidateRoutingValues) over the
// provider and roger carriers. Not the cross-key ones: this plane ignores sort and pref, so
// stating both is not a conflict here (localplane_routing.feature).
func validateCarriers(m map[string]json.RawMessage) error {
	sub := map[string]any{}
	for _, k := range []string{"provider", "roger"} {
		if raw, ok := m[k]; ok && string(raw) != "null" {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			sub[k] = v
		}
	}
	return client.ValidateRoutingValues(sub)
}

func parseLocalRouting(body []byte, hdrConfidential bool) (localRouting, *routeErr) {
	lr := localRouting{needAttest: hdrConfidential}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return lr, &routeErr{status: 400, msg: "a model is required"}
	}
	ignored := map[string]bool{}

	var model string
	if raw, ok := m["model"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &model) != nil {
			return lr, &routeErr{status: 400, msg: "a model is required"}
		}
	}
	if strings.HasPrefix(model, "@profile/") {
		return lr, &routeErr{status: 400, code: "unknown_profile", msg: "profiles resolve on the client; send the model"}
	}
	add := func(id string) {
		bare := id
		for {
			trimmed := bare
			for _, sfx := range localSugar {
				if strings.HasSuffix(trimmed, sfx) && len(trimmed) > len(sfx) {
					ignored["model"+sfx] = true
					trimmed = strings.TrimSuffix(trimmed, sfx)
				}
			}
			if trimmed == bare {
				break
			}
			bare = trimmed
		}
		for _, x := range lr.models {
			if x == bare {
				return
			}
		}
		lr.models = append(lr.models, bare)
	}
	if len(model) > maxModelID {
		return lr, &routeErr{status: 400, msg: fmt.Sprintf("a model id longer than %d characters", maxModelID)}
	}
	if model != "" {
		add(model)
	}

	if raw, ok := m["models"]; ok && string(raw) != "null" {
		var list []any
		if json.Unmarshal(raw, &list) != nil {
			return lr, &routeErr{status: 400, msg: "models must be a list of model ids"}
		}
		if len(list) > maxModelsEntries { // bounded before any entry is read (contract §1a)
			return lr, &routeErr{status: 400, msg: fmt.Sprintf("models has more than %d entries", maxModelsEntries)}
		}
		for _, e := range list {
			id, isStr := e.(string)
			if !isStr || strings.TrimSpace(id) == "" {
				return lr, &routeErr{status: 400, msg: "models must be a list of model ids"}
			}
			if len(id) > maxModelID {
				return lr, &routeErr{status: 400, msg: fmt.Sprintf("a model id longer than %d characters", maxModelID)}
			}
			add(id)
			if len(lr.models) > maxLocalModels {
				return lr, &routeErr{status: 400, msg: fmt.Sprintf("too many models (max %d)", maxLocalModels)}
			}
		}
	}
	if len(lr.models) == 0 {
		return lr, &routeErr{status: 400, msg: "a model is required"}
	}
	if len(lr.models) > maxLocalModels {
		return lr, &routeErr{status: 400, msg: fmt.Sprintf("too many models (max %d)", maxLocalModels)}
	}

	if raw, ok := m["provider"]; ok && string(raw) != "null" {
		var p map[string]json.RawMessage
		if json.Unmarshal(raw, &p) != nil || p == nil {
			return lr, &routeErr{status: 400, msg: "provider must be an object"}
		}
		keys, e := knownKeysInOrder("provider", p, localProviderHonored, localProviderIgnored)
		if e != nil {
			return lr, e
		}
		for _, k := range keys {
			v := p[k]
			switch {
			case localProviderHonored[k]:
				if string(v) == "null" {
					continue
				}
				if k == "allow_fallbacks" {
					var b bool
					if json.Unmarshal(v, &b) != nil {
						return lr, &routeErr{status: 400, msg: "provider.allow_fallbacks must be a boolean"}
					}
					lr.noFallbacks = !b
					continue
				}
				ids, err := localIDs(v)
				// An empty ignore is "nothing to deny" (as on the broker); an empty order or only
				// is a malformed preference.
				if err != nil || (len(ids) == 0 && k != "ignore") {
					return lr, &routeErr{status: 400, msg: "provider." + k + " must be a non-empty list of local station ids"}
				}
				switch k {
				case "only":
					lr.only = setOf(ids)
				case "ignore":
					lr.ignore = setOf(ids)
				case "order":
					lr.order = ids
				}
			case localProviderIgnored[k]:
				if string(v) == "null" {
					continue // a null key is absent, never named as ignored (as on the broker)
				}
				if k == "max_price" {
					var mp map[string]json.RawMessage
					if json.Unmarshal(v, &mp) != nil || mp == nil {
						return lr, &routeErr{status: 400, msg: "provider.max_price must be an object"}
					}
					// A closed set, as on the broker: the ignored header only ever echoes
					// these four names, never a caller-chosen string.
					sks := make([]string, 0, len(mp))
					for sk := range mp {
						sks = append(sks, sk)
					}
					sort.Strings(sks)
					for _, sk := range sks {
						if !maxPriceKeys[sk] {
							return lr, &routeErr{status: 400, msg: "provider.max_price keys are prompt, completion, request and image"}
						}
						if string(mp[sk]) != "null" {
							ignored["provider.max_price."+sk] = true
						}
					}
					continue
				}
				ignored["provider."+k] = true
			}
		}
	}

	if lr.only != nil {
		for _, id := range lr.order {
			if !lr.only[id] {
				return lr, &routeErr{status: 400, code: "conflicting_routing_keys", msg: "provider.order names a station outside provider.only"}
			}
		}
	}

	if raw, ok := m["roger"]; ok && string(raw) != "null" {
		var r map[string]json.RawMessage
		if json.Unmarshal(raw, &r) != nil || r == nil {
			return lr, &routeErr{status: 400, msg: "roger must be an object"}
		}
		if v, has := r["profile"]; has && string(v) != "null" {
			var ref string
			if json.Unmarshal(v, &ref) != nil || !strings.HasPrefix(ref, "@profile/") || len(ref) == len("@profile/") {
				return lr, &routeErr{status: 400, msg: "roger.profile must be @profile/<name>"}
			}
			return lr, &routeErr{status: 400, code: "unknown_profile", msg: "profiles resolve on the client; send the model"}
		}
		rkeys, e := knownKeysInOrder("roger", r, localRogerHonored, localRogerIgnored)
		if e != nil {
			return lr, e
		}
		for _, k := range rkeys {
			v := r[k]
			if string(v) == "null" {
				continue // a null key is absent, as on the provider side
			}
			switch {
			case k == "confidential":
				var b bool
				if json.Unmarshal(v, &b) != nil {
					return lr, &routeErr{status: 400, msg: "roger.confidential must be a boolean"}
				}
				lr.needAttest = lr.needAttest || b
			case k == "trust_min":
				var t string
				if json.Unmarshal(v, &t) != nil {
					return lr, &routeErr{status: 400, msg: "roger.trust_min must be any, verified or confidential"}
				}
				switch t {
				case "any":
				case "verified":
					lr.needVerify = true
				case "confidential":
					lr.needAttest = true
				default:
					return lr, &routeErr{status: 400, msg: "roger.trust_min must be any, verified or confidential"}
				}
			case localRogerIgnored[k]:
				ignored["roger."+k] = true
			}
		}
	}

	// The keys this plane does not honor are still checked with the contract's value rules
	// (the same ones the client and the broker apply), so a value the broker would refuse is
	// refused here too, never served. Local checks above run first and keep their codes.
	if err := validateCarriers(m); err != nil {
		return lr, &routeErr{status: 400, msg: err.Error()}
	}
	for k := range ignored {
		lr.ignored = append(lr.ignored, k)
	}
	sort.Strings(lr.ignored)
	delete(m, "models")
	delete(m, "provider")
	delete(m, "roger")
	lr.raw = m
	lr.keys = objectKeys(body)
	return lr, nil
}

// jobBody is the request a station receives for model: the caller's fields minus the carriers,
// with `model` set to the model being served.
func (lr localRouting) jobBody(model string) []byte {
	enc, _ := json.Marshal(model)
	var b bytes.Buffer
	b.WriteByte('{')
	wrote, sawModel := false, false
	for _, k := range lr.keys {
		v, kept := lr.raw[k]
		if !kept {
			continue
		}
		if k == "model" {
			v, sawModel = enc, true
		}
		if wrote {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(v)
		wrote = true
	}
	if !sawModel {
		if wrote {
			b.WriteByte(',')
		}
		b.WriteString(`"model":`)
		b.Write(enc)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// objectKeys returns a JSON object's top-level keys in document order.
func objectKeys(body []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

// admits reports whether a station id may claim this request's job.
func (lr localRouting) admits(stationID string) bool {
	if lr.only != nil && !lr.only[stationID] {
		return false
	}
	if lr.ignore[stationID] {
		return false
	}
	if lr.noFallbacks && len(lr.order) > 0 && !containsID(lr.order, stationID) {
		return false
	}
	return true
}

// offered reports whether any attached station serves model, admitted or not.
func offered(stations []stationView, model string) bool {
	for _, st := range stations {
		if serves(st.models, model) {
			return true
		}
	}
	return false
}

// constraintMiss names why no attached station serving model is admitted, or "" when one is.
func (lr localRouting) constraintMiss(stations []stationView, model string) string {
	any := false
	for _, st := range stations {
		if serves(st.models, model) && lr.admits(st.id) {
			any = true
			break
		}
	}
	if any {
		return ""
	}
	switch {
	case lr.only != nil:
		// The constraint is named, never the requested ids: echoing them would let a caller probe
		// which ids exist (founder ruling 2026-10-04).
		return "no local station matches: only for " + model
	case lr.ignore != nil:
		return "no local station matches: ignore removed every station for " + model
	case lr.noFallbacks && len(lr.order) > 0:
		return "no local station matches: order for " + model
	}
	return "no local station matches for " + model
}

// stationView is the slice of an attached station routing reads.
type stationView struct {
	id     string
	models []string
}

func localIDs(raw json.RawMessage) ([]string, error) {
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok || strings.TrimSpace(s) == "" || s != strings.TrimSpace(s) { // padded ids are refused, as on the broker
			return nil, fmt.Errorf("bad id")
		}
		out = append(out, s)
	}
	return out, nil
}

func setOf(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func containsID(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// preferredFor is the order's claim-window list: only the entries that are attached, serve
// model and are admitted. An entry that could never claim must not hold the job for the
// window while an eligible station waits; none left means no window at all.
func (lr localRouting) preferredFor(stations []stationView, model string) []string {
	var out []string
	for _, id := range lr.order {
		for _, st := range stations {
			if st.id == id && serves(st.models, model) && lr.admits(id) {
				out = append(out, id)
				break
			}
		}
	}
	return out
}
