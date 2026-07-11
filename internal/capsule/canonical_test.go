package capsule

import (
	"encoding/json"
	"strings"
	"testing"
)

// goldenInput builds the brief's golden fixture capsule (unsigned). exportedBy is the
// only value that differs by producer ("roger-cli" vs "roger-ios"); everything else is
// identical. The owner_pubkey is a literal "aa" in the vector (not derived from a key)
// because this test pins the CANONICAL BYTES, not a signature.
func goldenInput(exportedBy string) Capsule {
	return Capsule{
		Capsule:   Version,
		ID:        "cap_x",
		Thread:    Thread{OriginThreadID: "t1", Title: "Hi", BaseWatermark: 1},
		Redaction: "full",
		Summary:   Summary{Text: "hi", ProducedBy: "none", AsOfTurn: 1},
		Memory:    Memory{Notes: "", Facts: nil},
		Messages: []Message{{
			Role:    "user",
			Content: "hi",
			XRoger:  XRoger{Turn: 0, Agent: "user", Model: nil, Provider: nil, TS: 100},
		}},
		Meta: Meta{ToolsUsed: nil, ExportedBy: exportedBy, CreatedAt: 100, OwnerPubkey: "aa"},
	}
}

// TestGoldenVector is the ONE load-bearing interop contract: canonical() must reproduce
// the brief's golden bytes byte-for-byte, for BOTH producers. An app-signed capsule can
// only verify in Go if these bytes match token-for-token (RogerAI/Services/CapsuleWire.swift).
func TestGoldenVector(t *testing.T) {
	const cliGolden = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","model":null,"provider":null,"ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-cli","created_at":100,"owner_pubkey":"aa"}}`
	const iosGolden = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","model":null,"provider":null,"ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-ios","created_at":100,"owner_pubkey":"aa"}}`

	if got := string(goldenInput("roger-cli").canonical()); got != cliGolden {
		t.Errorf("roger-cli canonical mismatch\n got: %s\nwant: %s", got, cliGolden)
	}
	if got := string(goldenInput("roger-ios").canonical()); got != iosGolden {
		t.Errorf("roger-ios canonical mismatch\n got: %s\nwant: %s", got, iosGolden)
	}
}

// toolCallGoldenInput builds the app side's pinned tool_calls golden fixture (unsigned):
// the Stage-1 base with ONE assistant message carrying ONE FLAT tool_call whose arguments
// deliberately exercise <, >, &, /, and an escaped quote. The ToolCalls raw here is
// intentionally NON-canonical - the keys are SCRAMBLED (name,id,failed,denied,arguments) -
// to PROVE canonical() sorts them to the golden order (arguments,denied,failed,id,name).
// (Escaping NORMALIZATION - un-escaping HTML-escaped input - is proven separately by
// TestCanonicalToolCallsUnescapesHTML.) exportedBy is the only value that differs by
// producer. owner_pubkey is a literal "aa": this pins the CANONICAL BYTES.
func toolCallGoldenInput(exportedBy string) Capsule {
	scrambled := json.RawMessage(`[{"name":"open_url","id":"call_1","failed":false,"denied":false,"arguments":"{\"url\":\"https://x.com/a?b=1&c=2\",\"note\":\"<tag> & \\\"q\\\"\"}"}]`)
	return Capsule{
		Capsule:   Version,
		ID:        "cap_x",
		Thread:    Thread{OriginThreadID: "t1", Title: "Hi", BaseWatermark: 1},
		Redaction: "full",
		Summary:   Summary{Text: "hi", ProducedBy: "none", AsOfTurn: 1},
		Memory:    Memory{Notes: "", Facts: nil},
		Messages: []Message{{
			Role:      "assistant",
			Content:   "ok",
			ToolCalls: scrambled,
			XRoger:    XRoger{Turn: 0, Agent: "roger:m", Model: strp("m"), Provider: strp("p"), TS: 100},
		}},
		Meta: Meta{ToolsUsed: nil, ExportedBy: exportedBy, CreatedAt: 100, OwnerPubkey: "aa"},
	}
}

// TestGoldenVectorToolCalls is the tool_calls interop gate: canonical() must reproduce the
// app's pinned tool_calls golden bytes byte-for-byte, for BOTH producers. The flat tool_call
// shape (fields sorted arguments,denied,failed,id,name) and its escaping (< > & literal, / not
// escaped, pre-escaped quotes preserved) match RogerAI/Services/CapsuleWire.swift, so an
// app-signed tool-call capsule verifies in Go and vice-versa.
func TestGoldenVectorToolCalls(t *testing.T) {
	const cliGolden = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"assistant","content":"ok","tool_calls":[{"arguments":"{\"url\":\"https://x.com/a?b=1&c=2\",\"note\":\"<tag> & \\\"q\\\"\"}","denied":false,"failed":false,"id":"call_1","name":"open_url"}],"x_roger":{"turn":0,"agent":"roger:m","model":"m","provider":"p","ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-cli","created_at":100,"owner_pubkey":"aa"}}`
	const iosGolden = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"assistant","content":"ok","tool_calls":[{"arguments":"{\"url\":\"https://x.com/a?b=1&c=2\",\"note\":\"<tag> & \\\"q\\\"\"}","denied":false,"failed":false,"id":"call_1","name":"open_url"}],"x_roger":{"turn":0,"agent":"roger:m","model":"m","provider":"p","ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-ios","created_at":100,"owner_pubkey":"aa"}}`

	if got := string(toolCallGoldenInput("roger-cli").canonical()); got != cliGolden {
		t.Errorf("roger-cli tool_calls canonical mismatch\n got: %s\nwant: %s", got, cliGolden)
	}
	if got := string(toolCallGoldenInput("roger-ios").canonical()); got != iosGolden {
		t.Errorf("roger-ios tool_calls canonical mismatch\n got: %s\nwant: %s", got, iosGolden)
	}
}

// TestCanonicalToolCallsEscaping pins the tool_calls string escaping + key-sorting rules,
// matching CapsuleWire.swift: object keys sort at every level, < > & and / stay LITERAL
// (SetEscapeHTML off), quotes/backslashes stay JSON-escaped, arrays keep order. The
// producer helper ToolCallsRaw builds exactly these bytes.
func TestCanonicalToolCallsEscaping(t *testing.T) {
	// arguments' decoded content exercises /, <, >, &, and an escaped quote.
	args := `{"u":"a/b","lt":"<","gt":">","amp":"&","q":"\"x\""}`
	const want = `[{"arguments":"{\"u\":\"a/b\",\"lt\":\"<\",\"gt\":\">\",\"amp\":\"&\",\"q\":\"\\\"x\\\"\"}","denied":false,"failed":false,"id":"call_1","name":"open_url"}]`

	if got := string(ToolCallsRaw([]ToolCall{{Arguments: args, ID: "call_1", Name: "open_url"}})); got != want {
		t.Errorf("ToolCallsRaw escaping mismatch\n got: %s\nwant: %s", got, want)
	}
	// canonical() sorts a scrambled-key raw to the same bytes.
	scrambled := json.RawMessage(`[{"name":"open_url","id":"call_1","failed":false,"denied":false,"arguments":"{\"u\":\"a/b\",\"lt\":\"<\",\"gt\":\">\",\"amp\":\"&\",\"q\":\"\\\"x\\\"\"}"}]`)
	if got := string(canonicalToolCalls(scrambled)); got != want {
		t.Errorf("canonicalToolCalls scrambled mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestCanonicalToolCallsUnescapesHTML proves the escaping NORMALIZATION: a naive producer's
// json.Marshal HTML-escapes < > & (to < > &); canonicalToolCalls parses that
// and re-emits them LITERALLY (SetEscapeHTML off), matching the app's golden. The escaped
// input is GENERATED by json.Marshal (not hand-typed) so the \u form is genuine; the checks
// use the ASCII substring "u003c"/"u0026" to avoid hand-typing the escape.
func TestCanonicalToolCallsUnescapesHTML(t *testing.T) {
	escaped, err := json.Marshal([]ToolCall{{Arguments: `<a> & b`, ID: "c", Name: "n"}})
	if err != nil {
		t.Fatal(err)
	}
	// Precondition: json.Marshal really did HTML-escape < and & (to < / &).
	if !strings.Contains(string(escaped), "u003c") || !strings.Contains(string(escaped), "u0026") {
		t.Fatalf("expected json.Marshal to HTML-escape < & (to \\u003c \\u0026), got: %s", escaped)
	}
	got := string(canonicalToolCalls(escaped))
	// After canonicalization the escapes are gone and < > & are LITERAL.
	if strings.Contains(got, "u003c") || strings.Contains(got, "u0026") {
		t.Errorf("canonical tool_calls must un-escape < & to literal, got: %s", got)
	}
	if !strings.Contains(got, `<a> & b`) {
		t.Errorf("expected literal < > & in canonical tool_calls, got: %s", got)
	}
}

// TestCanonicalEscapingAsymmetry pins the deliberate asymmetry the two goldens encode: the
// SAME < > & string is HTML-escaped (< ...) in a PLAIN field (content, via goString,
// matching the app's base golden) but stays LITERAL inside a tool_call's arguments (matching
// the tool_calls golden). Both are verified byte-for-byte against the app.
func TestCanonicalEscapingAsymmetry(t *testing.T) {
	const raw = `<a> & b`
	c := Capsule{
		Capsule:  Version,
		ID:       "cap_asym",
		Messages: []Message{{Role: "assistant", Content: raw, ToolCalls: ToolCallsRaw([]ToolCall{{Arguments: raw, ID: "c", Name: "n"}}), XRoger: XRoger{Turn: 0, Agent: "roger:m", TS: 1}}},
		Meta:     Meta{OwnerPubkey: "bb"},
	}
	got := string(c.canonical())
	// plain content: HTML-escaped by goString - the < form appears (checked via the
	// "u003c" ASCII substring), and the raw literal "<a> & b" is NOT the content value.
	if !strings.Contains(got, `"content":`) || !strings.Contains(got, "u003c") {
		t.Errorf("plain content must be HTML-escaped (goString), got: %s", got)
	}
	// tool_call arguments: literal < > & survive verbatim.
	if !strings.Contains(got, `"arguments":"<a> & b"`) {
		t.Errorf("tool_call arguments must stay literal, got: %s", got)
	}
}

// TestCanonicalToolCallsLineSeparators pins that U+2028/U+2029 in a tool_calls string are
// ESCAPED (to the 6-char   /  ), never emitted raw - matching the app's scalar
// rewrite. This is the one asymmetry from < > & (which stay literal). Runes are built
// numerically (0x2028/0x2029) to keep the source pure ASCII.
func TestCanonicalToolCallsLineSeparators(t *testing.T) {
	ls, ps := rune(0x2028), rune(0x2029)
	args := "sep:" + string(ls) + string(ps) + "end"
	got := string(ToolCallsRaw([]ToolCall{{Arguments: args, ID: "c", Name: "n"}}))
	if !strings.Contains(got, "u2028") || !strings.Contains(got, "u2029") {
		t.Errorf("U+2028/U+2029 must be escaped to \\u2028 / \\u2029, got: %s", got)
	}
	if strings.ContainsRune(got, ls) || strings.ContainsRune(got, ps) {
		t.Errorf("raw U+2028/U+2029 must not appear in canonical bytes, got: %q", got)
	}
}

// TestToolCallsRawResult: Result is emitted ONLY when set (sorted last), and an empty slice
// yields nil (the tool_calls slot is omitted).
func TestToolCallsRawResult(t *testing.T) {
	if ToolCallsRaw(nil) != nil {
		t.Error("empty tool_calls must be nil (omit the slot)")
	}
	res := "done"
	got := string(ToolCallsRaw([]ToolCall{{Arguments: "{}", ID: "c", Name: "n", Result: &res}}))
	const want = `[{"arguments":"{}","denied":false,"failed":false,"id":"c","name":"n","result":"done"}]`
	if got != want {
		t.Errorf("result field\n got: %s\nwant: %s", got, want)
	}
	noRes := string(ToolCallsRaw([]ToolCall{{Arguments: "{}", ID: "c", Name: "n"}}))
	if noRes != `[{"arguments":"{}","denied":false,"failed":false,"id":"c","name":"n"}]` {
		t.Errorf("nil result must be omitted, got: %s", noRes)
	}
}

// TestCanonicalToolCallsUnparseableFallback: a tool_calls value that is not valid JSON is
// emitted VERBATIM (the safe state) - it simply will not match a peer's canonical bytes, so
// a signature over it fails to verify rather than being silently "canonicalized" into
// something else.
func TestCanonicalToolCallsUnparseableFallback(t *testing.T) {
	bad := json.RawMessage(`{not json`)
	if got := string(canonicalToolCalls(bad)); got != `{not json` {
		t.Errorf("unparseable tool_calls must pass through verbatim, got: %s", got)
	}
}
