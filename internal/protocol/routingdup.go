package protocol

import (
	"bytes"
	"encoding/json"
)

// DuplicateRoutingKey names the first key that appears twice inside a routing object of body
// (provider, roger, or provider.max_price), as "provider.only", or "" for none. The broker, the
// local proxy and the standalone Tower's local plane all refuse such a body (founder ruling
// 2026-10-07): a decoder that keeps the last occurrence would read a different value than one
// that keeps the first, so neither reading is trusted. Duplicate top-level keys are not a
// routing object's and are left to each reader.
func DuplicateRoutingKey(body []byte) string {
	top, ok := objectMembers(body)
	if !ok {
		return ""
	}
	for _, carrier := range top {
		if carrier.key != "provider" && carrier.key != "roger" {
			continue
		}
		members, isObj := objectMembers(carrier.val)
		if !isObj {
			continue
		}
		if k := firstRepeat(members); k != "" {
			return carrier.key + "." + k
		}
		if carrier.key != "provider" {
			continue
		}
		for _, m := range members {
			if m.key != "max_price" {
				continue
			}
			if inner, ok := objectMembers(m.val); ok {
				if k := firstRepeat(inner); k != "" {
					return "provider.max_price." + k
				}
			}
		}
	}
	return ""
}

type member struct {
	key string
	val json.RawMessage
}

// objectMembers reads a JSON object's members in document order, duplicates included.
func objectMembers(raw []byte) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var out []member
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
		out = append(out, member{k, v})
	}
	return out, true
}

func firstRepeat(ms []member) string {
	seen := make(map[string]bool, len(ms))
	for _, m := range ms {
		if seen[m.key] {
			return m.key
		}
		seen[m.key] = true
	}
	return ""
}
