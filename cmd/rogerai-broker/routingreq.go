package main

// routingreq.go is the BODY form of the consumer's routing preferences
// (features/routing/ROUTING-EXPRESSION-CONTRACT.md §1): the OpenRouter-compatible
// `provider{}` object, the network-specific `roger{}` object, the top-level `models`
// list, and the variant sugar on a model id (§4). The whole shape is decoded and
// validated up front (§1a), so an unknown key, a mistyped value or an exclusive pair is
// refused with a 400 naming the key BEFORE moderation or any pick:
//
//	unknown_routing_key       a key under provider / roger the contract does not define
//	                          (keys match case-sensitively: `provider.Order` is unknown)
//	invalid_routing_value     a wrong type, a value out of range, an unknown enum value
//	                          (enumerated values are exact lowercase), an over-long list
//	conflicting_routing_keys  an exclusive pair (sort + pref, order outside only, ...)
//	unsupported_routing_key   a well-formed value for a key this release recognises but
//	                          does not honour yet (no silent drop - a consumer who asked
//	                          for trust_min must never be routed as if they had not)
//	unknown_profile           a `@profile/<name>` reference (resolved client-side only, §9)
//
// Honoured: models, roger.pref / confidential / min_tps / freq / self_hosted_only / require,
// provider.only / ignore / order / allow_fallbacks / sort / quantizations /
// require_parameters / max_price.prompt + completion + request, and the :free / :floor /
// :nitro sugar. Recognised and refused (unsupported_routing_key): provider.max_price.image,
// roger.params_b / min_ctx / max_ttft_ms / trust_min / region / profile.
//
// `null` means absent at every level. Limits compose with their header form to the
// stricter (stricterCap / stricterFloor / freqConflict); only preferences are body-wins.
// The three carriers never leave the broker: stripCarriers removes them before the
// body reaches a station or the edge bridge.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// routingListMax bounds every node/label list in the object (§1a: more than 32 entries is a
// 400). A consumer that needs more is expressing a filter, not a list.
const routingListMax = 32

// routingModelsMax bounds the effective model list (model + models, de-duplicated on the
// bare id, §3); routingModelIDMax bounds one model id.
const (
	routingModelsMax  = 5
	routingModelIDMax = 256
)

// sortKey is provider.sort (or the sugar that means one): a STRICT single-metric ordering
// of the eligible set, with no power-of-two-choices spread (§5).
type sortKey int

const (
	sortNone sortKey = iota
	sortPrice
	sortThroughput
	sortLatency
)

func (s sortKey) String() string {
	switch s {
	case sortPrice:
		return "price"
	case sortThroughput:
		return "throughput"
	case sortLatency:
		return "latency"
	}
	return "none"
}

func parseSortKey(s string) (sortKey, bool) {
	for _, k := range []sortKey{sortPrice, sortThroughput, sortLatency} {
		if k.String() == s {
			return k, true
		}
	}
	return sortNone, false
}

// routingBody is the decoded and validated routing object. Pointer / nil-slice fields are
// "not stated" (absent or null).
type routingBody struct {
	object  bool // the body decoded as a JSON object at all
	present bool // a carrier was in the body (what stripCarriers keys on)
	used    bool // a carrier stated at least one thing (the routing_body_requests counter)

	// provider{}
	Order          []string
	Only           []string
	Ignore         []string
	Quantizations  []string
	AllowFallbacks *bool
	Sort           sortKey // sortNone = not stated
	MaxPrompt      *float64
	MaxCompletion  *float64
	MaxRequest     *float64 // per-request USD cap; 0 states no cap
	RequireParams  bool

	// roger{}
	Pref           *string
	MinTPS         *float64
	SelfHostedOnly *bool
	Confidential   *bool
	Freq           *string
	Require        []string   // de-duplicated, closed set
	Net            netFilters // params_b, min_ctx, max_ttft_ms, trust_min verified, region (slice 2)
	TrustConf      bool       // trust_min "confidential": the same as confidential:true
	// Session is roger.session or the top-level session_id (§14.B1): the conversation whose
	// previous turn's station is preferred as the head. Never forwarded, never logged raw.
	Session string

	// models[] as sent (every entry a non-empty string); the effective list is built by
	// effectiveModels once the primary model is known.
	Models []string

	// unsupported names the first well-formed key this release does not honour yet. It is
	// answered only after every invalid / conflicting check has passed.
	unsupported string
}

// routingError is a 400 the routing object earns before any pick: the error code from
// contract §1a/§2 plus the message that names the key.
type routingError struct {
	code, msg string
}

func (e *routingError) Error() string { return e.msg }

func invalidRouting(key, why string) *routingError {
	msg := "invalid routing value for " + key
	if why != "" {
		msg += ": " + why
	}
	return &routingError{code: "invalid_routing_value", msg: msg}
}

func conflictRouting(msg string) *routingError {
	return &routingError{code: "conflicting_routing_keys", msg: msg}
}

var routingCarriers = [...]string{"models", "provider", "roger", "session_id"}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// jsonKV is one member of a JSON object, in document order.
type jsonKV struct {
	key string
	val json.RawMessage
}

// jsonObject reads a JSON object's members in document order, keys verbatim (Go's struct
// decoder matches field names case-insensitively, which would let `provider.Order` pass as
// `order`). ok=false when raw is not an object.
func jsonObject(raw json.RawMessage) ([]jsonKV, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var out []jsonKV
	for dec.More() {
		t, err := dec.Token()
		k, isKey := t.(string)
		if err != nil || !isKey {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		out = append(out, jsonKV{k, v})
	}
	return out, true
}

var (
	providerKeys = map[string]bool{"order": true, "only": true, "ignore": true, "allow_fallbacks": true, "sort": true,
		"quantizations": true, "max_price": true, "require_parameters": true}
	maxPriceKeys = map[string]bool{"prompt": true, "completion": true, "request": true, "image": true}
	rogerKeys    = map[string]bool{"pref": true, "require": true, "params_b": true, "min_ctx": true, "min_tps": true,
		"max_ttft_ms": true, "trust_min": true, "self_hosted_only": true, "confidential": true, "region": true,
		"freq": true, "profile": true, "session": true}
	regionToken = regexp.MustCompile(`^[a-z]{2,8}$`)
)

// routingObject reads one carrier (or provider.max_price): it must be an object, and every
// key must be one the contract defines - checked for ALL keys before any value, so an
// unknown key is never masked by an earlier invalid value. Later duplicates win.
func routingObject(path string, raw json.RawMessage, known map[string]bool) (map[string]json.RawMessage, *routingError) {
	kvs, ok := jsonObject(raw)
	if !ok {
		return nil, invalidRouting(path, "want an object")
	}
	for _, kv := range kvs {
		if !known[kv.key] {
			return nil, &routingError{code: "unknown_routing_key", msg: "unknown routing key " + path + "." + kv.key}
		}
	}
	m := make(map[string]json.RawMessage, len(kvs))
	for _, kv := range kvs {
		if isJSONNull(kv.val) {
			delete(m, kv.key) // null means absent, and the LAST occurrence of a key wins
			continue
		}
		m[kv.key] = kv.val
	}
	return m, nil
}

// routingNum decodes a finite JSON NUMBER (a numeric string like "0.5" or "NaN" is a type
// error, and an out-of-range literal like 1e400 fails the decode).
func routingNum(raw json.RawMessage) (float64, bool) {
	var f float64
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] == '"' || json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func routingBool(raw json.RawMessage) (bool, bool) {
	var v bool
	if t := bytes.TrimSpace(raw); !bytes.Equal(t, []byte("true")) && !bytes.Equal(t, []byte("false")) {
		return false, false
	}
	_ = json.Unmarshal(raw, &v)
	return v, true
}

func routingString(raw json.RawMessage) (string, bool) {
	var s string
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// routingList decodes a list of non-empty strings with no surrounding whitespace, at most
// routingListMax entries (counted BEFORE de-duplication), de-duplicated keeping the first
// occurrence. A non-nil empty result is an explicitly empty list.
func routingList(path string, raw json.RawMessage) ([]string, *routingError) {
	var items []json.RawMessage
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' || json.Unmarshal(raw, &items) != nil {
		return nil, invalidRouting(path, "want a list of strings")
	}
	if len(items) > routingListMax {
		return nil, invalidRouting(path, "more than 32 entries")
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		s, ok := routingString(it)
		if !ok || s == "" || s != strings.TrimSpace(s) {
			return nil, invalidRouting(path, "every entry must be a non-empty string")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// quantList reads provider.quantizations: a list of labels canonicalized by the SAME rule a
// station's label is (protocol.CanonicalQuant: trimmed, control characters stripped), so a
// requested label compares like the one it names. A label longer than the canonical bound
// is refused rather than truncated into a different name.
func quantList(raw json.RawMessage) ([]string, *routingError) {
	const path = "provider.quantizations"
	var items []json.RawMessage
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' || json.Unmarshal(raw, &items) != nil {
		return nil, invalidRouting(path, "want a list of strings")
	}
	if len(items) > routingListMax {
		return nil, invalidRouting(path, "more than 32 entries")
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		s, ok := routingString(it)
		if !ok {
			return nil, invalidRouting(path, "every entry must be a non-empty string")
		}
		if len([]rune(strings.TrimSpace(s))) > quantLabelMax {
			return nil, invalidRouting(path, "a label is longer than 40 characters")
		}
		c := strings.ToLower(protocol.CanonicalQuant(s))
		if c == "" {
			return nil, invalidRouting(path, "every entry must be a non-empty string")
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// quantLabelMax is the canonical label bound (protocol variantTextMax).
const quantLabelMax = 40

// parseRoutingBody decodes the routing carriers out of an already-read request body. A body
// without any carrier decodes to the zero routingBody with no error.
func parseRoutingBody(body []byte) (routingBody, error) {
	return parseRoutingDoc(decodeReqDoc(body))
}

// parseRoutingDoc is parseRoutingBody over an already-decoded body (the relay's one decode).
func parseRoutingDoc(d reqDoc) (routingBody, error) {
	if !d.ok {
		return routingBody{}, nil // not an object: relay() answers the plain 400
	}
	top := make(map[string]json.RawMessage, len(d.kvs))
	for _, kv := range d.kvs {
		top[kv.key] = kv.val // later duplicates win, as map decoding does
	}
	rb := routingBody{object: true}
	for _, k := range routingCarriers {
		if raw, ok := top[k]; ok && !isJSONNull(raw) {
			rb.present = true
		}
	}
	if !rb.present {
		return rb, nil
	}
	var provider, roger map[string]json.RawMessage
	if raw, ok := top["provider"]; ok && !isJSONNull(raw) {
		p, err := routingObject("provider", raw, providerKeys)
		if err != nil {
			return rb, err
		}
		provider = p
	}
	if raw, ok := top["roger"]; ok && !isJSONNull(raw) {
		r, err := routingObject("roger", raw, rogerKeys)
		if err != nil {
			return rb, err
		}
		roger = r
	}
	// provider.max_price's own keys are part of the unknown-key pass (provider.max_price.<k>).
	var maxPrice map[string]json.RawMessage
	if raw, ok := provider["max_price"]; ok {
		mp, err := routingObject("provider.max_price", raw, maxPriceKeys)
		if err != nil {
			return rb, err
		}
		maxPrice = mp
	}
	if raw, ok := top["models"]; ok && !isJSONNull(raw) {
		var items []json.RawMessage
		if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' || json.Unmarshal(raw, &items) != nil {
			return rb, invalidRouting("models", "want a list of model ids")
		}
		if len(items) > routingListMax {
			// Counted BEFORE de-duplication, like every list in the object (§1a): the
			// effective list is at most 5, so a long list is never needed and must never
			// cost more than a bounded amount of work.
			return rb, invalidRouting("models", "more than 32 entries")
		}
		rb.Models = make([]string, 0, len(items))
		for _, it := range items {
			s, ok := routingString(it)
			if !ok || s == "" || s != strings.TrimSpace(s) {
				return rb, invalidRouting("models", "every entry must be a non-empty model id")
			}
			rb.Models = append(rb.Models, s)
		}
	}
	if err := rb.readProvider(provider, maxPrice); err != nil {
		return rb, err
	}
	if err := rb.readRoger(roger); err != nil {
		return rb, err
	}
	if raw, ok := roger["session"]; ok {
		s, err := sessionID("roger.session", raw)
		if err != nil {
			return rb, err
		}
		rb.Session = s
	}
	if raw, ok := top["session_id"]; ok && !isJSONNull(raw) {
		s, err := sessionID("session_id", raw)
		if err != nil {
			return rb, err
		}
		if rb.Session != "" && rb.Session != s {
			return rb, conflictRouting("roger.session and session_id name different sessions")
		}
		rb.Session = s
	}
	rb.used = len(provider) > 0 || len(roger) > 0 || len(rb.Models) > 0 || rb.Session != ""
	if err := rb.conflicts(); err != nil {
		return rb, err
	}
	return rb, nil
}

// sessionID reads a session id: a string of 1..256 printable ASCII bytes (§14.B1).
func sessionID(key string, raw json.RawMessage) (string, *routingError) {
	s, ok := routingString(raw)
	if !ok || s == "" || len(s) > 256 {
		return "", invalidRouting(key, "want a string of 1 to 256 printable ASCII characters")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return "", invalidRouting(key, "want a string of 1 to 256 printable ASCII characters")
		}
	}
	return s, nil
}

func (rb *routingBody) notYet(key string) {
	if rb.unsupported == "" {
		rb.unsupported = key
	}
}

func (rb *routingBody) readProvider(p, maxPrice map[string]json.RawMessage) *routingError {
	for _, l := range []struct {
		key  string
		into *[]string
	}{{"order", &rb.Order}, {"only", &rb.Only}, {"ignore", &rb.Ignore}} {
		raw, ok := p[l.key]
		if !ok {
			continue
		}
		list, err := routingList("provider."+l.key, raw)
		if err != nil {
			return err
		}
		*l.into = list
	}
	if raw, ok := p["quantizations"]; ok {
		list, err := quantList(raw)
		if err != nil {
			return err
		}
		rb.Quantizations = list
	}
	// An EMPTY order / only is a malformed preference ("nothing" is not a priority and not an
	// allow-list); an empty ignore / quantizations means "nothing to deny / no filter".
	if rb.Order != nil && len(rb.Order) == 0 {
		return invalidRouting("provider.order", "empty list")
	}
	if rb.Only != nil && len(rb.Only) == 0 {
		return &routingError{code: "invalid_routing_value", msg: "provider.only must not be empty"}
	}
	if raw, ok := p["allow_fallbacks"]; ok {
		v, ok := routingBool(raw)
		if !ok {
			return invalidRouting("provider.allow_fallbacks", "want true or false")
		}
		rb.AllowFallbacks = &v
	}
	if raw, ok := p["require_parameters"]; ok {
		v, ok := routingBool(raw)
		if !ok {
			return invalidRouting("provider.require_parameters", "want true or false")
		}
		rb.RequireParams = v
	}
	if raw, ok := p["sort"]; ok {
		s, isStr := routingString(raw)
		k, known := parseSortKey(s)
		if !isStr || !known {
			return invalidRouting("provider.sort", "want price, throughput or latency")
		}
		rb.Sort = k
	}
	for _, c := range []struct {
		key  string
		into **float64
	}{{"prompt", &rb.MaxPrompt}, {"completion", &rb.MaxCompletion}, {"request", &rb.MaxRequest}, {"image", nil}} {
		raw, ok := maxPrice[c.key]
		if !ok {
			continue
		}
		v, ok := routingNum(raw)
		if !ok || v < 0 {
			return invalidRouting("provider.max_price."+c.key, "want a non-negative number")
		}
		if c.into != nil {
			*c.into = &v
		} else {
			rb.notYet("provider.max_price." + c.key) // image pricing does not exist at all
		}
	}
	return nil
}

func (rb *routingBody) readRoger(r map[string]json.RawMessage) *routingError {
	if raw, ok := r["pref"]; ok {
		s, isStr := routingString(raw)
		if !isStr || parsePref(s).String() != s {
			return invalidRouting("roger.pref", "want cheap, balanced, fast or reliable")
		}
		rb.Pref = &s
	}
	if raw, ok := r["min_tps"]; ok {
		v, ok := routingNum(raw)
		if !ok || v < 0 {
			return invalidRouting("roger.min_tps", "want a non-negative number")
		}
		rb.MinTPS = &v
	}
	for _, f := range []struct {
		key  string
		into **bool
	}{{"self_hosted_only", &rb.SelfHostedOnly}, {"confidential", &rb.Confidential}} {
		if raw, ok := r[f.key]; ok {
			v, ok := routingBool(raw)
			if !ok {
				return invalidRouting("roger."+f.key, "want true or false")
			}
			*f.into = &v
		}
	}
	if raw, ok := r["freq"]; ok {
		s, isStr := routingString(raw)
		if !isStr {
			return invalidRouting("roger.freq", "want a band code")
		}
		// An empty band code is no band code (§1a: null/empty means absent for a scalar
		// carrier); it must never replace the X-Roger-Freq header with "no band".
		if strings.TrimSpace(s) != "" {
			rb.Freq = &s
		}
	}
	if raw, ok := r["require"]; ok {
		list, err := routingList("roger.require", raw)
		if err != nil {
			return err
		}
		for _, c := range list {
			// The closed set is protocol.knownCapabilities; exact lowercase, never folded.
			if canon := protocol.CanonicalCapabilities([]string{c}); len(canon) != 1 || canon[0] != c {
				return invalidRouting("roger.require", "want tools or vision")
			}
		}
		rb.Require = list
	}
	// --- recognised, validated, not honoured yet (slice 2) ---------------------------------
	if raw, ok := r["params_b"]; ok {
		var pair []json.RawMessage
		if json.Unmarshal(raw, &pair) != nil || len(pair) != 2 {
			return invalidRouting("roger.params_b", "want [min, max]")
		}
		lo, okLo := routingNum(pair[0])
		hi, okHi := routingNum(pair[1])
		if !okLo || !okHi || lo <= 0 || hi <= 0 || lo > hi {
			return invalidRouting("roger.params_b", "want two positive numbers, min <= max")
		}
		rb.Net.paramsLo, rb.Net.paramsHi = lo, hi
	}
	for _, key := range []string{"min_ctx", "max_ttft_ms"} {
		if raw, ok := r[key]; ok {
			v, ok := routingNum(raw)
			if !ok || v <= 0 || v != math.Trunc(v) || v > math.MaxInt32 {
				return invalidRouting("roger."+key, "want a positive integer")
			}
			if key == "min_ctx" {
				rb.Net.minCtx = int(v)
			} else {
				rb.Net.maxTTFT = v
			}
		}
	}
	if raw, ok := r["trust_min"]; ok {
		s, isStr := routingString(raw)
		if !isStr || (s != "any" && s != "verified" && s != "confidential") {
			return invalidRouting("roger.trust_min", "want any, verified or confidential")
		}
		// "any" is the default: it states no restriction.
		rb.Net.trustVerified = s == "verified"
		rb.TrustConf = s == "confidential"
	}
	if raw, ok := r["region"]; ok {
		list, err := routingList("roger.region", raw)
		if err != nil {
			return err
		}
		for _, tok := range list {
			if !regionToken.MatchString(tok) {
				return invalidRouting("roger.region", "want lowercase tokens of 2-8 letters")
			}
		}
		if len(list) > 0 {
			rb.Net.regions = stringSet(list)
		}
	}
	if raw, ok := r["profile"]; ok {
		s, isStr := routingString(raw)
		if !isStr || !isProfileRef(s) {
			return invalidRouting("roger.profile", "want @profile/<name>")
		}
		// Profiles are resolved by the first-party client (§9); one that reaches the broker
		// was not resolved, and the broker keeps no profiles.
		return &routingError{code: "unknown_profile", msg: "unknown profile " + s + " (profiles are resolved by the client)"}
	}
	return nil
}

// isProfileRef reports a well-formed `@profile/<name>` reference.
func isProfileRef(s string) bool {
	name, ok := strings.CutPrefix(s, "@profile/")
	return ok && name != "" && !strings.ContainsAny(name, ": \t/")
}

// conflicts applies the exclusive pairs of §1a that live entirely inside the body. The
// pairs that involve the model's sugar or the pin header are checked by relay(), which
// knows both.
func (rb *routingBody) conflicts() *routingError {
	if rb.Sort != sortNone && rb.Pref != nil {
		return conflictRouting("provider.sort and roger.pref are exclusive")
	}
	if rb.Only != nil {
		for _, id := range rb.Order {
			if !containsString(rb.Only, id) {
				return conflictRouting("provider.order names " + id + ", which is not in provider.only")
			}
		}
	}
	return nil
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// --- the model id and its variant sugar (§3, §4) -------------------------------------------

// routedModel is one model of the request with its sugar resolved: the BARE id everything
// downstream sees, whether the entry is free-only, and the sort its suffixes named.
type routedModel struct {
	bare  string
	free  bool
	sort  sortKey
	class string // a `@class/<name>` entry before its expansion (§14.B5); "" for a model id
}

// parseModelSugar splits the variant sugar off a model id. Suffixes are recognised only
// from the closed set :free / :floor / :nitro, right-to-left; any other `:tag` is part of
// the id (Ollama-style `llama3:8b` stays intact). The LAST sort suffix wins. An empty colon
// segment (leading, trailing or doubled) is malformed. key names the field in the error.
func parseModelSugar(key, id string) (routedModel, []string, *routingError) {
	if len(id) > routingModelIDMax {
		return routedModel{}, nil, invalidRouting(key, "a model id longer than 256 characters")
	}
	if strings.HasPrefix(id, "@profile/") {
		if !isProfileRef(id) {
			return routedModel{}, nil, invalidRouting(key, "a profile reference takes no suffix")
		}
		return routedModel{}, nil, &routingError{code: "unknown_profile", msg: "unknown profile " + id + " (profiles are resolved by the client)"}
	}
	if strings.HasPrefix(id, classPrefix) && key == "models" {
		return routedModel{}, nil, invalidRouting(key, "a class is a list of models, not an entry")
	}
	e := routedModel{bare: id}
	var used []string
	for {
		i := strings.LastIndexByte(e.bare, ':')
		if i < 0 {
			break
		}
		suffix := e.bare[i+1:]
		var k sortKey
		switch suffix {
		case "free":
			e.free = true
		case "floor":
			k = sortPrice
		case "nitro":
			k = sortThroughput
		default:
			suffix = ""
		}
		if suffix == "" {
			break
		}
		if k != sortNone && e.sort == sortNone {
			e.sort = k // right-to-left: the first one seen is the last one written
		}
		used = append(used, suffix)
		e.bare = e.bare[:i]
	}
	if strings.Contains(id, ":") && (e.bare == "" || strings.HasPrefix(e.bare, ":") || strings.HasSuffix(e.bare, ":") || strings.Contains(e.bare, "::")) {
		return routedModel{}, nil, invalidRouting(key, "an empty variant suffix")
	}
	if strings.HasPrefix(e.bare, classPrefix) {
		name, err := className(e.bare)
		if err != nil {
			return routedModel{}, nil, err
		}
		e.class = name
	}
	return e, used, nil
}

// effectiveModels builds the request's model list (§3): [model] ++ models, de-duplicated on
// the bare id keeping the first occurrence, at most routingModelsMax entries. A sort suffix
// on ANY entry applies to the whole request, the last one written winning. suffixes is every
// sugar word the request used (for the variant counters).
func effectiveModels(model string, models []string) (list []routedModel, sugarSort sortKey, suffixes []string, rerr *routingError) {
	add := func(key, id string) *routingError {
		e, used, err := parseModelSugar(key, id)
		if err != nil {
			return err
		}
		suffixes = append(suffixes, used...)
		if e.sort != sortNone {
			sugarSort = e.sort
		}
		for _, have := range list {
			if have.bare == e.bare {
				return nil
			}
		}
		if len(list) == routingModelsMax {
			return invalidRouting("models", "more than 5 models after de-duplication")
		}
		list = append(list, e)
		return nil
	}
	if strings.HasPrefix(model, classPrefix) && len(models) > 0 {
		return nil, sortNone, nil, conflictRouting("a model class is already a models list: send @class/... without models")
	}
	if model != "" {
		if err := add("model", model); err != nil {
			return nil, sortNone, nil, err
		}
	}
	for _, m := range models {
		if err := add("models", m); err != nil {
			return nil, sortNone, nil, err
		}
	}
	if len(list) == 0 {
		return nil, sortNone, nil, invalidRouting("model", "model is required")
	}
	return list, sugarSort, suffixes, nil
}

// registerModelSuffix returns a rejection message when an offer's model id literally ends in
// a sugar word: such an id cannot be OFFERED (the suffix wins on the consumer side, §4), so
// it is refused at register and dropped on re-hydrate. "" when every id is fine; an
// Ollama-style tag (`llama3:8b`) is not a sugar word.
func registerModelSuffix(offers []protocol.ModelOffer) string {
	for _, o := range offers {
		if strings.HasPrefix(o.Model, classPrefix) {
			return "model id " + o.Model + " starts with the reserved class prefix " + classPrefix + " (consumer routing alias)"
		}
		for _, sfx := range []string{":free", ":floor", ":nitro"} {
			if strings.HasSuffix(o.Model, sfx) {
				return "model id " + o.Model + " ends in the reserved variant suffix " + sfx + " (consumer routing sugar) - register the model under its bare id"
			}
		}
	}
	return ""
}

// stripCarriers removes models / provider / roger from the body the station will
// see and sets `model` to the bare served id. A body that carried no carrier and whose
// model needs no rewrite is returned untouched (byte-identical), so nothing about today's
// forwarding changes for callers that never used the routing expression.
func (d reqDoc) stripCarriers(rb routingBody, sentModel, model string) reqDoc {
	if !rb.present && sentModel == model {
		return d
	}
	var set map[string]json.RawMessage
	if sentModel != model {
		v, _ := json.Marshal(model)
		set = map[string]json.RawMessage{"model": v}
	}
	return d.rewrite(carrierKeys, set)
}

// carrierKeys are the routing carriers a station never sees.
var carrierKeys = map[string]bool{"models": true, "provider": true, "roger": true, "session_id": true}

// capBuys is what a per-request USD cap buys at one station's billed prices: the output
// tokens left after the prompt's input cost (promptTokens is the request's one measured
// estimate, the same number every cap decision uses). drop reports a station the cap buys
// NO output at - its input cost alone meets the cap - which is never dispatched (sending
// max_tokens 1 would serve a reply the consumer cannot use). buys is -1 when output is
// free, which needs no bound.
func capBuys(capUSD float64, promptTokens int, in, out float64) (buys int, drop bool) {
	left := capUSD - float64(promptTokens)*in/1e6
	if out <= 0 {
		return -1, in > 0 && left <= 0
	}
	buys = int(math.Floor(left*1e6/out + 1e-9)) // the epsilon keeps an exact token from flooring to the one below
	return buys, buys < 1
}

// capBody bounds every output limit the station is sent at buys tokens: max_tokens and
// max_completion_tokens (OpenAI-compatible servers honour either), each lowered when it is
// above the bound and left alone when it is within it; a body naming neither gains a
// max_tokens. Generation stops where the money stops, instead of the operator serving
// tokens the settle clamp will not pay for. buys < 0 (free output) returns the body as given.
func capBody(body []byte, buys int) []byte {
	set := decodeReqDoc(body).outLimits().capSet(buys)
	if set == nil {
		return body
	}
	return rewriteBody(body, false, set)
}

// rewriteBody rebuilds a JSON object body member by member, in document order: the routing
// carriers are dropped (when asked) and the keys in set get their new value (in place when
// the body has the key, appended otherwise). Every OTHER member's value bytes are copied
// untouched - re-marshalling through a map would compact them, and the station must receive
// the consumer's tools / response_format / messages byte-identical. A body that is not an
// object is returned as given.
func rewriteBody(body []byte, dropCarriers bool, set map[string]json.RawMessage) []byte {
	var drop map[string]bool
	if dropCarriers {
		drop = carrierKeys
	}
	return rewriteBodyDrop(body, drop, set)
}

// rewriteBodyDrop is rewriteBody with an explicit set of keys to drop: the same document-order,
// value-bytes-preserving rebuild (every other key's bytes ride through untouched).
func rewriteBodyDrop(body []byte, drop map[string]bool, set map[string]json.RawMessage) []byte {
	kvs, ok := jsonObject(body)
	if !ok {
		return body
	}
	return encodeKVs(rebuildKVs(kvs, drop, set))
}

// quantSet lowercases a provider.quantizations list into the case-insensitive set pickFor
// matches an offer's verbatim quant label against. "unknown" admits an unlabeled offer.
func quantSet(labels []string) map[string]bool {
	if len(labels) == 0 {
		return nil
	}
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		set[strings.ToLower(strings.TrimSpace(l))] = true
	}
	return set
}

// stringSet turns a node-id list into a lookup set (nil for an absent list).
func stringSet(l []string) map[string]bool {
	if l == nil {
		return nil
	}
	set := make(map[string]bool, len(l))
	for _, s := range l {
		set[s] = true
	}
	return set
}

// intersectAllow narrows an admission set by another: nil means "everyone", so the result
// is nil only when both are. A consumer constraint can only NARROW what a grant or a band
// admits (§1b), so every allow-list composes through here.
func intersectAllow(a, b map[string]bool) map[string]bool {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := make(map[string]bool, len(a))
	for id := range a {
		if b[id] {
			out[id] = true
		}
	}
	return out
}

// bodyNeedsTools / bodyNeedsVision are the IMPLICIT capability requirements (§5): a request
// carrying a non-empty tools array must land on a (node, model) with the VERIFIED tools bit;
// one carrying any image_url content part must land on an offer that declared vision.
func bodyNeedsTools(body []byte) bool { return decodeReqDoc(body).needsTools() }

func bodyNeedsVision(body []byte) bool {
	_, vision := decodeReqDoc(body).promptScan()
	return vision
}

// paramsNeedTools is provider.require_parameters:true (§5): a tool_choice requires tools even
// without a tools array, and a JSON response_format requires a tools-verified station (the
// only structured-output signal the network verifies today).
func paramsNeedTools(body []byte) bool { return decodeReqDoc(body).paramsNeedTools() }

// A LIMIT stated in a header and in the body composes to the STRICTER of the two; only
// preferences are body-wins (contract §1a, founder ruling 2026-10-01). A local proxy's owner
// states limits in headers on every installed client while a guest application controls the
// body, so a body must never be able to loosen a header limit.

// stricterCap composes the header and body forms of one price cap ($/1M): the LOWER of the
// two when both state one. A side that is absent or 0 states no cap, so the other side's
// value applies; 0 means neither side stated one (the caller applies its own default).
func stricterCap(header, body float64) float64 {
	switch {
	case header > 0 && body > 0:
		return math.Min(header, body)
	case header > 0:
		return header
	case body > 0:
		return body
	}
	return 0
}

// stricterFloor composes the header and body forms of a floor (min tok/s): the HIGHER one.
func stricterFloor(header, body float64) float64 { return math.Max(math.Max(header, body), 0) }

// freqConflict reports a body band code that names a DIFFERENT band than the header's: the
// header code is the session's band and a body cannot replace it. Codes are compared on
// their canonical tail (the cosmetic frequency is not part of a code); an empty body value
// means absent.
func freqConflict(header, body string) bool {
	if header == "" || body == "" {
		return false
	}
	return protocol.CanonicalBandTail(header) != protocol.CanonicalBandTail(body)
}

// grantIsFree reports a grant that bills nothing (free or self), so every station it
// reaches costs its holder nothing and `:free` has nothing to filter.
func grantIsFree(gc grantContext) bool {
	in, out := gc.grant.GrantPrice()
	return in == 0 && out == 0
}

// ownedNodes is the set of stations the SIGNED caller owns: consuming your own station is
// $0 (resolvePricing's self-use), so `:free` admits them whatever they charge others. One
// store read; nil for a grant, a browser session or an anonymous caller (no self-use there).
func (b *broker) ownedNodes(r *http.Request, grant bool, user string) map[string]bool {
	pub := r.Header.Get(protocol.HeaderPubkey)
	if grant || pub == "" || !b.ownsNode(user, pub) {
		return nil
	}
	ids, _ := b.db.NodesOfAccount(pub)
	return stringSet(ids)
}

// unionSets is a fresh set holding every member of the given sets (none of them is modified).
func unionSets(sets ...map[string]bool) map[string]bool {
	n := 0
	for _, s := range sets {
		n += len(s)
	}
	out := make(map[string]bool, n)
	for _, s := range sets {
		for k, v := range s {
			if v {
				out[k] = true
			}
		}
	}
	return out
}

// regDecodeField names the field a registration body failed to decode at, so a refusal says
// which declaration was wrong (": invalid params_b"); "" when it cannot tell.
func regDecodeField(body []byte, err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		f := te.Field
		if i := strings.LastIndexByte(f, '.'); i >= 0 {
			f = f[i+1:]
		}
		return ": invalid " + f
	}
	var se *json.SyntaxError
	if errors.As(err, &se) && se.Offset > 0 && int(se.Offset) <= len(body) {
		if m := lastJSONKeyRe.FindAllSubmatch(body[:se.Offset], -1); len(m) > 0 {
			return ": invalid " + string(m[len(m)-1][1])
		}
	}
	return ""
}

var lastJSONKeyRe = regexp.MustCompile(`"([A-Za-z0-9_]+)"\s*:`)

// regParamsBError checks every offer's params_b as sent: present means a JSON number that
// protocol.ValidParamsB accepts. "" = acceptable (including absent).
func regParamsBError(body []byte) string {
	var raw struct {
		Offers []map[string]json.RawMessage `json:"offers"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return ""
	}
	for _, o := range raw.Offers {
		v, ok := o["params_b"]
		if !ok || isJSONNull(v) {
			continue
		}
		f, isNum := routingNum(v)
		if !isNum || !protocol.ValidParamsB(f) {
			return fmt.Sprintf("invalid params_b: want a number of billions above 0 and at most %d", protocol.ParamsBMax)
		}
	}
	return ""
}
