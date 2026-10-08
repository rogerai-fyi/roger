package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	var m struct {
		Model  string          `json:"model"`
		Models json.RawMessage `json:"models"`
	}
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
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
		if b := bareModel(id); b != id && (strings.Contains(id[len(b):], ":floor") || strings.Contains(id[len(b):], ":nitro")) {
			return true
		}
	}
	return false
}

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
	if model == "" || strings.HasPrefix(model, "@profile/") || bareModel(model) == tuned {
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

// bareModel is a guest's model id without its guest provider prefix and variant suffixes.
func bareModel(id string) string {
	id = guestModelID(id)
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
func (r Routing) Apply(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, fmt.Errorf("request body is not a JSON object")
	}
	roger, provider := rawObject(m["roger"]), rawObject(m["provider"])
	// The owner's pref is a DEFAULT, and a sort the guest stated (provider.sort, or a :floor /
	// :nitro suffix on any model id) excludes pref (§1a), so adding it would earn the guest a
	// self-inflicted 400 conflicting_routing_keys.
	if r.Pref != "" && !guestStatesSort(m, provider) {
		setDefault(roger, "pref", r.Pref)
	}
	if r.MinTPS > 0 {
		if f, ok := roger["min_tps"].(float64); !ok || f < r.MinTPS {
			roger["min_tps"] = r.MinTPS
		}
	}
	if r.Confidential {
		roger["confidential"] = true
	}
	delete(roger, "freq") // the owner's band stands; a guest cannot drop or swap it
	if r.SelfHostedOnly {
		roger["self_hosted_only"] = true
	}
	if len(r.Quantizations) > 0 {
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
	if len(r.Order) > 0 {
		provider["order"] = r.Order
	}
	if len(r.Ignore) > 0 {
		provider["ignore"] = unionStrings(stringsOf(provider["ignore"]), r.Ignore)
	}
	if r.MaxOut > 0 || r.MaxIn > 0 {
		// The owner's price caps are the ceiling. A max_price that is not an object is left
		// for the broker to refuse (never silently repaired).
		mp, isObj := provider["max_price"].(map[string]any)
		if _, present := provider["max_price"]; !present || provider["max_price"] == nil {
			mp, isObj = map[string]any{}, true
		}
		if isObj {
			capPrice(mp, "completion", r.MaxOut)
			capPrice(mp, "prompt", r.MaxIn)
			provider["max_price"] = mp
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

// hasFold reports whether list contains s, comparing case-insensitively (quant labels are
// verbatim but a Q8_0 and a q8_0 are the same weights).
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
	return d
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
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
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
