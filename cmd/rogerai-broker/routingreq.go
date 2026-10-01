package main

// routingreq.go is the BODY form of the consumer's routing preferences
// (features/routing/ROUTING-EXPRESSION-CONTRACT.md §1): the OpenRouter-compatible
// `provider{}` object, the network-specific `roger{}` object, and the top-level `models`
// list. Slice 0 (features/routing/regression_pins.feature) decodes the whole shape so an
// unknown or mistyped key is refused up front (400, naming the key) and honours the knobs
// the pins need: roger.pref / confidential / min_tps / freq / self_hosted_only,
// provider.ignore / order / allow_fallbacks / quantizations / max_price.prompt+completion.
// Every other key of the contract is recognised by NAME only (json.RawMessage) and refused
// with 400 unsupported_routing_key until its slice ships (§1a: no silent drop - a consumer
// who asked for trust_min or only must never be routed as if they had not).
//
// Body wins over header for the same knob; `null` means absent (pointers/slices stay nil).
// The narrowing carriers compose instead of replacing: provider.ignore is UNIONED with
// X-Roger-Exclude-Nodes, and a body confidential:false never cancels the header (§1a).
// The three carriers never leave the broker: stripRoutingCarriers removes them before the
// body reaches a station or the edge bridge.

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// routingListMax bounds every node/label list in the object (§1a: more than 32 entries is a
// 400). A consumer that needs more is expressing a filter, not a list.
const routingListMax = 32

type routingMaxPrice struct {
	Prompt     *float64 `json:"prompt"`
	Completion *float64 `json:"completion"`
	// Not honoured yet (slice 1): presence-only, refused by name.
	Request json.RawMessage `json:"request"`
	Image   json.RawMessage `json:"image"`
}

type routingProvider struct {
	Order          []string `json:"order"`
	Ignore         []string `json:"ignore"`
	AllowFallbacks *bool    `json:"allow_fallbacks"`
	Quantizations  []string `json:"quantizations"`
	// max_price is decoded in a second pass (decodeMaxPrice) so an unknown key under it is
	// named with its full path, provider.max_price.<key>.
	MaxPrice json.RawMessage `json:"max_price"`
	maxPrice *routingMaxPrice
	// Not honoured yet (slice 1): presence-only, refused by name.
	Only              json.RawMessage `json:"only"`
	Sort              json.RawMessage `json:"sort"`
	RequireParameters json.RawMessage `json:"require_parameters"`
}

type routingRoger struct {
	Pref           *string  `json:"pref"`
	MinTPS         *float64 `json:"min_tps"`
	SelfHostedOnly *bool    `json:"self_hosted_only"`
	Confidential   *bool    `json:"confidential"`
	Freq           *string  `json:"freq"`
	// Not honoured yet (slices 1-4): presence-only, refused by name.
	Require   json.RawMessage `json:"require"`
	ParamsB   json.RawMessage `json:"params_b"`
	MinCtx    json.RawMessage `json:"min_ctx"`
	MaxTTFTMs json.RawMessage `json:"max_ttft_ms"`
	TrustMin  json.RawMessage `json:"trust_min"`
	Region    json.RawMessage `json:"region"`
	Profile   json.RawMessage `json:"profile"`
}

// present reports a presence-only key that was sent with a non-null value.
func present(raw json.RawMessage) bool { return len(raw) > 0 && !isJSONNull(raw) }

// routingBody is the decoded routing object. A nil Provider / Roger means the carrier was
// absent (or null); present is the set of top-level carriers that were in the body, which
// is what stripRoutingCarriers keys on so a body without any stays byte-identical.
type routingBody struct {
	Provider *routingProvider
	Roger    *routingRoger
	present  bool
	object   bool // the body decoded as a JSON object at all
}

// routingError is a 400 the routing object earns before any pick: the error code from
// contract §1a/§2 plus the message that names the key.
type routingError struct {
	code, msg string
}

func (e *routingError) Error() string { return e.msg }

var routingCarriers = [...]string{"models", "provider", "roger"}

// parseRoutingBody decodes the routing carriers out of an already-read request body. A body
// without any carrier decodes to the zero routingBody with no error. Unknown keys under
// provider / roger (at any depth) are unknown_routing_key; wrong types, non-finite or
// negative numbers, empty strings and over-long lists are invalid_routing_value.
func parseRoutingBody(body []byte) (routingBody, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return routingBody{}, nil // not an object: relay() answers the plain 400
	}
	rb := routingBody{object: true}
	for _, k := range routingCarriers {
		if raw, ok := top[k]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			rb.present = true
		}
	}
	if !rb.present {
		return rb, nil
	}
	if raw, ok := top["models"]; ok && !isJSONNull(raw) {
		// Not honoured yet (slice 1): recognised by name, refused (§1a: no silent drop).
		return rb, &routingError{code: "unsupported_routing_key", msg: "unsupported routing key models (not honoured by this release)"}
	}
	if raw, ok := top["provider"]; ok && !isJSONNull(raw) {
		rb.Provider = &routingProvider{}
		if err := decodeRoutingCarrier("provider", raw, rb.Provider); err != nil {
			return rb, err
		}
		if present(rb.Provider.MaxPrice) {
			rb.Provider.maxPrice = &routingMaxPrice{}
			if err := decodeRoutingCarrier("provider.max_price", rb.Provider.MaxPrice, rb.Provider.maxPrice); err != nil {
				return rb, err
			}
		}
	}
	if raw, ok := top["roger"]; ok && !isJSONNull(raw) {
		rb.Roger = &routingRoger{}
		if err := decodeRoutingCarrier("roger", raw, rb.Roger); err != nil {
			return rb, err
		}
	}
	return rb, (&rb).validate()
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// decodeRoutingCarrier decodes one carrier strictly: DisallowUnknownFields turns a stray key
// at any depth into unknown_routing_key, and a type mismatch into invalid_routing_value.
func decodeRoutingCarrier(name string, raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			key := name
			if ute.Field != "" {
				key += "." + ute.Field
			}
			return &routingError{code: "invalid_routing_value", msg: "invalid routing value for " + key}
		}
		if s := err.Error(); strings.HasPrefix(s, "json: unknown field ") {
			field := strings.Trim(strings.TrimPrefix(s, "json: unknown field "), `"`)
			return &routingError{code: "unknown_routing_key", msg: "unknown routing key " + name + "." + field}
		}
		return &routingError{code: "invalid_routing_value", msg: "invalid routing value for " + name}
	}
	return nil
}

// unsupported names a recognised-but-not-honoured key: 400 unsupported_routing_key. The
// message says why, so an SDK user does not read it as a typo.
func unsupported(key string) error {
	return &routingError{code: "unsupported_routing_key", msg: "unsupported routing key " + key + " (not honoured by this release)"}
}

// validate applies the range rules for the keys slice 0 honours and refuses, by name, the
// keys it only recognises (presence-only RawMessage fields; null counts as absent).
func (rb *routingBody) validate() error {
	if p := rb.Provider; p != nil {
		for name, raw := range map[string]json.RawMessage{"provider.only": p.Only, "provider.sort": p.Sort, "provider.require_parameters": p.RequireParameters} {
			if present(raw) {
				return unsupported(name)
			}
		}
		for name, l := range map[string][]string{"provider.order": p.Order, "provider.ignore": p.Ignore, "provider.quantizations": p.Quantizations} {
			if err := validRoutingList(name, l); err != nil {
				return err
			}
		}
		// An EMPTY order is a malformed preference (node_preference.feature: `[]` is a 400),
		// unlike an empty ignore / quantizations, which mean "nothing to deny / no filter".
		if p.Order != nil && len(p.Order) == 0 {
			return &routingError{code: "invalid_routing_value", msg: "invalid routing value for provider.order: empty list"}
		}
		if mp := p.maxPrice; mp != nil {
			for name, raw := range map[string]json.RawMessage{"provider.max_price.request": mp.Request, "provider.max_price.image": mp.Image} {
				if present(raw) {
					return unsupported(name)
				}
			}
			for name, v := range map[string]*float64{"provider.max_price.prompt": mp.Prompt, "provider.max_price.completion": mp.Completion} {
				if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
					return &routingError{code: "invalid_routing_value", msg: "invalid routing value for " + name}
				}
			}
		}
	}
	if r := rb.Roger; r != nil {
		for name, raw := range map[string]json.RawMessage{"roger.require": r.Require, "roger.params_b": r.ParamsB, "roger.min_ctx": r.MinCtx,
			"roger.max_ttft_ms": r.MaxTTFTMs, "roger.trust_min": r.TrustMin, "roger.region": r.Region, "roger.profile": r.Profile} {
			if present(raw) {
				return unsupported(name)
			}
		}
		if r.Pref != nil && parsePref(*r.Pref).String() != *r.Pref {
			return &routingError{code: "invalid_routing_value", msg: "invalid routing value for roger.pref: want cheap, balanced, fast or reliable"}
		}
		if r.MinTPS != nil && (math.IsNaN(*r.MinTPS) || math.IsInf(*r.MinTPS, 0) || *r.MinTPS < 0) {
			return &routingError{code: "invalid_routing_value", msg: "invalid routing value for roger.min_tps"}
		}
		// An empty band code is no band code (§1a: null/empty means absent for a scalar
		// carrier); it must never replace the X-Roger-Freq header with "no band".
		if r.Freq != nil && strings.TrimSpace(*r.Freq) == "" {
			r.Freq = nil
		}
	}
	return nil
}

func validRoutingList(name string, l []string) error {
	if len(l) > routingListMax {
		return &routingError{code: "invalid_routing_value", msg: "invalid routing value for " + name + ": more than 32 entries"}
	}
	for _, s := range l {
		if strings.TrimSpace(s) == "" {
			return &routingError{code: "invalid_routing_value", msg: "invalid routing value for " + name + ": empty entry"}
		}
	}
	return nil
}

// stripRoutingCarriers removes models / provider / roger from the body the station will
// see. A body that carried none is returned untouched (byte-identical), so nothing about
// today's forwarding changes for callers that never used the object.
func stripRoutingCarriers(body []byte, rb routingBody) []byte {
	if !rb.present {
		return body
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	for _, k := range routingCarriers {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
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

// bodyNeedsTools / bodyNeedsVision are the IMPLICIT capability requirements (§5): a request
// carrying a non-empty tools array must land on a (node, model) with the VERIFIED tools bit;
// one carrying any image_url content part must land on an offer that declared vision.
func bodyNeedsTools(body []byte) bool {
	var req struct {
		Tools []json.RawMessage `json:"tools"`
	}
	return json.Unmarshal(body, &req) == nil && len(req.Tools) > 0
}

func bodyNeedsVision(body []byte) bool {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	for _, m := range req.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue // a string content has no parts
		}
		for _, p := range parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}

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
