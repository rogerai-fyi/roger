package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// bodydoc.go - the consumer's request body decoded ONCE per relay (contract §14.B, #12). A
// relay used to re-parse the whole body for every question it asked of it (the model, the
// routing object, each capability, the output limit) and again for every plan candidate (the
// default max_tokens, the hold estimate, the per-request cap). With a 2 MiB body and a long
// plan that was routing work a caller could inflate at will.
//
// A reqDoc holds the body's top-level members in document order. Every question is answered
// from one member's bytes, and every rewrite (strip the carriers, set `model`, set
// `max_tokens`) is rebuilt from the members - value bytes copied untouched - without parsing
// the body again. The relay counts its whole-body decodes: this one (the routing object) and
// promptScan (the token estimate), and logs them on its routing-work line.

type reqDoc struct {
	body []byte
	kvs  []jsonKV
	ok   bool // the body is one JSON object
}

// decodeReqDoc reads a body's top-level members. ok is false for anything json.Unmarshal
// would not take as one object (not an object, malformed, trailing data).
func decodeReqDoc(body []byte) reqDoc {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return reqDoc{body: body}
	}
	var kvs []jsonKV
	for dec.More() {
		t, err := dec.Token()
		k, isKey := t.(string)
		if err != nil || !isKey {
			return reqDoc{body: body}
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return reqDoc{body: body}
		}
		kvs = append(kvs, jsonKV{k, v})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return reqDoc{body: body}
	}
	if _, err := dec.Token(); err != io.EOF {
		return reqDoc{body: body}
	}
	return reqDoc{body: body, kvs: kvs, ok: true}
}

// get is a member by its exact key, the last one winning (map decoding).
func (d reqDoc) get(key string) (json.RawMessage, bool) {
	var raw json.RawMessage
	found := false
	for _, kv := range d.kvs {
		if kv.key == key {
			raw, found = kv.val, true
		}
	}
	return raw, found
}

// field decodes one member into v the way decoding the whole body into a struct with that
// field would: the key matched case-insensitively, every matching member applied in
// document order (so the last wins). The error is the decode error, as the struct decode
// would have reported it.
func (d reqDoc) field(name string, v any) error {
	var first error
	for _, kv := range d.kvs {
		if strings.EqualFold(kv.key, name) {
			if err := json.Unmarshal(kv.val, v); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// rewrite is the body with the members in drop removed and the keys in set given their new
// value (in place when present, appended in key order otherwise); every other member's bytes
// ride through untouched. Built from the members: nothing is parsed.
func (d reqDoc) rewrite(drop map[string]bool, set map[string]json.RawMessage) reqDoc {
	if !d.ok {
		return d
	}
	kvs := rebuildKVs(d.kvs, drop, set)
	return reqDoc{body: encodeKVs(kvs), kvs: kvs, ok: true}
}

// rebuildKVs is rewriteBodyDrop over already-read members.
func rebuildKVs(in []jsonKV, drop map[string]bool, set map[string]json.RawMessage) []jsonKV {
	out := make([]jsonKV, 0, len(in)+len(set))
	done := map[string]bool{}
	for _, kv := range in {
		if drop[kv.key] {
			continue
		}
		if v, replace := set[kv.key]; replace {
			if !done[kv.key] {
				out = append(out, jsonKV{kv.key, v})
				done[kv.key] = true
			}
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		if !done[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, jsonKV{k, set[k]})
	}
	return out
}

// encodeKVs writes members as one JSON object, keys encoded as json.Marshal encodes a string.
func encodeKVs(kvs []jsonKV) []byte {
	n := 2
	for _, kv := range kvs {
		n += len(kv.key) + len(kv.val) + 4
	}
	var out bytes.Buffer
	out.Grow(n)
	out.WriteByte('{')
	for i, kv := range kvs {
		if i > 0 {
			out.WriteByte(',')
		}
		kb, _ := json.Marshal(kv.key)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(kv.val)
	}
	out.WriteByte('}')
	return out.Bytes()
}

// withIncludeUsage is ensureStreamIncludeUsage on the members: a body with a `stream` member
// and no stream_options.include_usage gains include_usage:true. Like the map re-marshal it
// replaces, the result has its keys sorted, duplicates collapsed and values compacted.
func (d reqDoc) withIncludeUsage() reqDoc {
	if !d.ok {
		return d
	}
	m := make(map[string]json.RawMessage, len(d.kvs))
	for _, kv := range d.kvs {
		m[kv.key] = kv.val
	}
	if _, ok := m["stream"]; !ok {
		return d // only rewrite streaming requests
	}
	so := map[string]json.RawMessage{}
	if raw, ok := m["stream_options"]; ok {
		_ = json.Unmarshal(raw, &so) // preserve existing options; ignore a non-object
	}
	if so == nil {
		so = map[string]json.RawMessage{} // "stream_options": null decodes to a nil map
	}
	if _, set := so["include_usage"]; set {
		return d // respect an explicit client choice (true OR false); do not override
	}
	so["include_usage"] = json.RawMessage("true")
	sob, err := json.Marshal(so)
	if err != nil {
		return d
	}
	m["stream_options"] = sob
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kvs := make([]jsonKV, 0, len(keys))
	for _, k := range keys {
		vb, err := json.Marshal(m[k]) // compacted exactly as the map re-marshal compacts it
		if err != nil {
			return d
		}
		kvs = append(kvs, jsonKV{k, vb})
	}
	return reqDoc{body: encodeKVs(kvs), kvs: kvs, ok: true}
}

// outLimits are the output limits a body states (nil = absent).
type outLimits struct {
	maxTokens, maxCompletion *int
}

func (d reqDoc) outLimits() outLimits {
	var l outLimits
	_ = d.field("max_tokens", &l.maxTokens)
	_ = d.field("max_completion_tokens", &l.maxCompletion)
	return l
}

// stated is the output limit the body states: max_completion_tokens, else max_tokens; 0 when
// it states neither (or a non-positive value).
func (l outLimits) stated() int {
	if l.maxCompletion != nil && *l.maxCompletion > 0 {
		return *l.maxCompletion
	}
	if l.maxTokens != nil && *l.maxTokens > 0 {
		return *l.maxTokens
	}
	return 0
}

// capSet is capBody's rewrite: every output limit above buys lowered to it, and a body naming
// neither gains max_tokens. nil when nothing changes (buys < 0 is free output: no bound).
func (l outLimits) capSet(buys int) map[string]json.RawMessage {
	if buys < 0 {
		return nil
	}
	v, _ := json.Marshal(buys)
	set := map[string]json.RawMessage{}
	if l.maxCompletion != nil && *l.maxCompletion > buys {
		set["max_completion_tokens"] = v
	}
	if (l.maxTokens != nil && *l.maxTokens > buys) || (l.maxTokens == nil && l.maxCompletion == nil) {
		set["max_tokens"] = v
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// apply is the limits after set was written into the body.
func (l outLimits) apply(set map[string]json.RawMessage) outLimits {
	for k, p := range map[string]**int{"max_tokens": &l.maxTokens, "max_completion_tokens": &l.maxCompletion} {
		if raw, ok := set[k]; ok {
			var n int
			_ = json.Unmarshal(raw, &n)
			*p = &n
		}
	}
	return l
}

// needsTools: the body carries a non-empty tools array.
func (d reqDoc) needsTools() bool {
	var tools []json.RawMessage
	return d.ok && d.field("tools", &tools) == nil && len(tools) > 0
}

// paramsNeedTools: a tool_choice, or a JSON response_format (provider.require_parameters).
func (d reqDoc) paramsNeedTools() bool {
	var choice json.RawMessage
	var rf struct {
		Type string `json:"type"`
	}
	if !d.ok || d.field("tool_choice", &choice) != nil || d.field("response_format", &rf) != nil {
		return false
	}
	return (len(choice) > 0 && !isJSONNull(choice)) || rf.Type == "json_object" || rf.Type == "json_schema"
}

// promptScan is the token-estimate decode: the prompt text (promptText) and whether a message
// carries an image_url part (bodyNeedsVision), from one pass over messages.
func (d reqDoc) promptScan() (text string, vision bool) {
	if !d.ok {
		return "", false
	}
	var msgs []struct {
		Content json.RawMessage `json:"content"`
	}
	var tools []struct {
		Function json.RawMessage `json:"function"`
	}
	var functions []json.RawMessage
	msgErr := d.field("messages", &msgs)
	textOK := msgErr == nil && d.field("tools", &tools) == nil && d.field("functions", &functions) == nil
	var b bytes.Buffer
	for _, msg := range msgs {
		var s string
		if json.Unmarshal(msg.Content, &s) == nil {
			if textOK {
				b.WriteString(s)
				b.WriteByte('\n')
			}
			continue
		}
		var parts []struct {
			Text string          `json:"text"`
			Type json.RawMessage `json:"type"` // RawMessage never fails, so Text alone decides the error
		}
		if json.Unmarshal(msg.Content, &parts) == nil {
			for _, p := range parts {
				if textOK {
					b.WriteString(p.Text)
					b.WriteByte('\n')
				}
				var typ string
				if !vision && json.Unmarshal(p.Type, &typ) == nil && typ == "image_url" {
					vision = msgErr == nil && partsTyped(msg.Content)
				}
			}
		} else if msgErr == nil && !vision && partsHaveImage(msg.Content) {
			vision = true // a malformed text field does not hide an image part
		}
	}
	if !textOK {
		return "", vision
	}
	for _, t := range tools {
		collectStrings(t.Function, &b)
	}
	for _, f := range functions {
		collectStrings(f, &b)
	}
	return b.String(), vision
}

// partsTyped: every part's type decodes as a string (bodyNeedsVision skips a message whose
// parts do not).
func partsTyped(content json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(content, &parts) == nil
}

// partsHaveImage is bodyNeedsVision's per-message rule.
func partsHaveImage(content json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}
