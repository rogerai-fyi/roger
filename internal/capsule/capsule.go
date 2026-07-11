// Package capsule implements roger.context.v1, the portable signed context capsule
// that carries a conversation across operators (CLI/TUI <-> iOS <-> guest agents).
//
// The format is defined authoritatively by the iOS app
// (RogerAI/Services/CapsuleWire.swift). The ONE load-bearing interop contract is that
// canonical() reproduces the app's canonical signing bytes token-for-token, so an
// app-signed capsule verifies in Go and vice-versa. That parity is pinned by the
// golden vector in canonical_test.go, exactly like the share-receipt / IAP JWS goldens.
//
// Stage 1 scope (founder-approved): the capsule package + the `roger context` CLI +
// SAME-OWNER / LOCAL handoff only. The encrypted stranger broker transport is a
// follow-on (ruling Q3). tool_calls now INTEROPERATE: the flat cross-language shape and
// its canonical form are pinned against the app (canonicalToolCalls + the golden), so a
// verified tool-call capsule imports and merges like any other (verify-before-merge and
// append-only still apply; an unverified one is still rejected, the safe state).
package capsule

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// Version is the only capsule format this package speaks. Merge/Import reject any other.
const Version = "roger.context.v1"

// Capsule is a single signed roger.context.v1 object. The owner ed25519 sig covers
// every field except sig (over the canonical bytes, so it also covers redaction - a
// stranger cannot flip a summary-only capsule to full and re-sign).
type Capsule struct {
	Capsule   string    `json:"capsule"`
	ID        string    `json:"id"`
	Thread    Thread    `json:"thread"`
	Redaction string    `json:"redaction"` // full | summary | minimal
	Summary   Summary   `json:"summary"`
	Memory    Memory    `json:"memory"`
	Messages  []Message `json:"messages"`
	Meta      Meta      `json:"meta"`
	Sig       string    `json:"sig"`
}

// Thread is the origin-thread provenance. BaseWatermark is the count of turns the
// holder had at export time (= the next-expected turn index): a turn index t is
// "already present" iff t < BaseWatermark. Merge appends only turns at/after it.
type Thread struct {
	OriginThreadID string `json:"origin_thread_id"`
	Title          string `json:"title"`
	BaseWatermark  int    `json:"base_watermark"`
}

// Summary is the optional condensed context (Stage 2 fills it; Stage 1 carries it
// verbatim). ProducedBy is "none" | "on-device" | "operator:<model>".
type Summary struct {
	Text       string `json:"text"`
	ProducedBy string `json:"produced_by"`
	AsOfTurn   int    `json:"as_of_turn"`
}

// Memory is durable notes/facts carried with the thread (empty in Stage 1 exports).
type Memory struct {
	Notes string   `json:"notes"`
	Facts []string `json:"facts"`
}

// Message is one turn in plain OpenAI shape so any agent can read it; RogerAI
// provenance lives under the ignore-unknown x_roger namespace. ToolCalls is carried as
// raw JSON (any shape); canonical() re-serializes it in the pinned cross-language form
// (see canonicalToolCalls). Producers build it from the flat ToolCall shape via
// ToolCallsRaw so the wire bytes are already canonical.
type Message struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
	XRoger    XRoger          `json:"x_roger"`
}

// ToolCall is the FLAT, cross-language tool-call shape (NOT OpenAI-nested {id,type,
// function{}}): the app's internal struct, byte-aligned with CapsuleWire.swift. Every
// value is a string or a JSON bool - no numbers. Arguments is a STRING holding
// already-escaped JSON. Result is present ONLY when the tool has run (a nil Result is
// omitted). Fields are declared in sorted key order (arguments,denied,failed,id,name,
// result) so a plain marshal is already sorted; canonical() re-sorts regardless.
type ToolCall struct {
	Arguments string  `json:"arguments"`
	Denied    bool    `json:"denied"`
	Failed    bool    `json:"failed"`
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Result    *string `json:"result,omitempty"`
}

// ToolCallsRaw is the PRODUCER helper: it serializes the flat tool_calls of a turn into
// the canonical cross-language wire bytes (sorted keys, compact, < > & literal, U+2028/
// U+2029 escaped). A producer attaches the result to Message.ToolCalls; canonical() would
// normalize any shape, but building via this keeps the at-rest bytes canonical too. It
// returns nil for an empty slice (so the tool_calls slot is omitted).
func ToolCallsRaw(tcs []ToolCall) json.RawMessage {
	if len(tcs) == 0 {
		return nil
	}
	raw, _ := json.Marshal(tcs) // marshaling a fixed struct slice never errors
	return canonicalToolCalls(raw)
}

// canonicalToolCalls re-serializes a tool_calls value in the pinned cross-language
// canonical form: parsed generically (numbers preserved via json.Number, no float
// rounding), object keys sorted lexicographically at every level, arrays kept in order,
// compact, and strings escaped like the app - SetEscapeHTML(false) leaves < > & and /
// literal (as the golden requires) while Go still escapes U+2028/U+2029. It is
// shape-agnostic. An unparseable value is emitted verbatim: it simply will not match a
// peer's canonical bytes (so verify fails - the safe state) unless it already is canonical.
func canonicalToolCalls(raw json.RawMessage) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return raw
	}
	return bytes.TrimRight(buf.Bytes(), "\n") // Encoder appends a newline; canonical form has none
}

// XRoger is the RogerAI provenance for a message. Model/Provider are pointers so a nil
// emits the literal null in canonical() (NOT omitted) - the format distinguishes
// "no model" from an empty string.
type XRoger struct {
	Turn     int     `json:"turn"`
	Agent    string  `json:"agent"`
	Model    *string `json:"model"`
	Provider *string `json:"provider"`
	TS       int64   `json:"ts"`
}

// Meta is capsule-level provenance. OwnerPubkey is the hex ed25519 key the sig
// verifies against; Sign sets it from the signing key.
type Meta struct {
	ToolsUsed   []string `json:"tools_used"`
	ExportedBy  string   `json:"exported_by"`
	CreatedAt   int64    `json:"created_at"`
	OwnerPubkey string   `json:"owner_pubkey"`
}

// goString encodes a string exactly as Go's encoding/json does (HTML-escaping < > &),
// matching the app's goString so byte-parity holds. json.Marshal of a string never
// errors, so the error is discarded.
func goString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// canonical builds the exact bytes signed: the capsule with sig cleared, emitted in a
// FIXED field order (never runtime-sorted), each string via goString, numbers in plain
// decimal, nil model/provider as literal null, tool_calls only when present. It is
// HAND-BUILT (not json.Marshal of a struct) because the format needs literal null for
// nil pointers and a conditional tool_calls slot that struct tags cannot express, and
// because the byte order must be pinned against the app rather than left to a marshaler.
func (c Capsule) canonical() []byte {
	var b []byte
	b = append(b, '{')
	b = append(b, `"capsule":`...)
	b = append(b, goString(c.Capsule)...)
	b = append(b, `,"id":`...)
	b = append(b, goString(c.ID)...)

	b = append(b, `,"thread":{"origin_thread_id":`...)
	b = append(b, goString(c.Thread.OriginThreadID)...)
	b = append(b, `,"title":`...)
	b = append(b, goString(c.Thread.Title)...)
	b = append(b, `,"base_watermark":`...)
	b = strconv.AppendInt(b, int64(c.Thread.BaseWatermark), 10)
	b = append(b, '}')

	b = append(b, `,"redaction":`...)
	b = append(b, goString(c.Redaction)...)

	b = append(b, `,"summary":{"text":`...)
	b = append(b, goString(c.Summary.Text)...)
	b = append(b, `,"produced_by":`...)
	b = append(b, goString(c.Summary.ProducedBy)...)
	b = append(b, `,"as_of_turn":`...)
	b = strconv.AppendInt(b, int64(c.Summary.AsOfTurn), 10)
	b = append(b, '}')

	b = append(b, `,"memory":{"notes":`...)
	b = append(b, goString(c.Memory.Notes)...)
	b = append(b, `,"facts":`...)
	b = appendStringArray(b, c.Memory.Facts)
	b = append(b, '}')

	b = append(b, `,"messages":[`...)
	for i, m := range c.Messages {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"role":`...)
		b = append(b, goString(m.Role)...)
		b = append(b, `,"content":`...)
		b = append(b, goString(m.Content)...)
		if len(m.ToolCalls) > 0 {
			b = append(b, `,"tool_calls":`...)
			b = append(b, canonicalToolCalls(m.ToolCalls)...)
		}
		b = append(b, `,"x_roger":{"turn":`...)
		b = strconv.AppendInt(b, int64(m.XRoger.Turn), 10)
		b = append(b, `,"agent":`...)
		b = append(b, goString(m.XRoger.Agent)...)
		b = append(b, `,"model":`...)
		b = appendNullableString(b, m.XRoger.Model)
		b = append(b, `,"provider":`...)
		b = appendNullableString(b, m.XRoger.Provider)
		b = append(b, `,"ts":`...)
		b = strconv.AppendInt(b, m.XRoger.TS, 10)
		b = append(b, '}', '}')
	}
	b = append(b, ']')

	b = append(b, `,"meta":{"tools_used":`...)
	b = appendStringArray(b, c.Meta.ToolsUsed)
	b = append(b, `,"exported_by":`...)
	b = append(b, goString(c.Meta.ExportedBy)...)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, c.Meta.CreatedAt, 10)
	b = append(b, `,"owner_pubkey":`...)
	b = append(b, goString(c.Meta.OwnerPubkey)...)
	b = append(b, '}')

	b = append(b, '}')
	return b
}

// appendStringArray emits a JSON array of goString-encoded strings with no spaces
// (an empty or nil slice emits []), matching the app's canonical array form.
func appendStringArray(b []byte, ss []string) []byte {
	b = append(b, '[')
	for i, s := range ss {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, goString(s)...)
	}
	return append(b, ']')
}

// appendNullableString emits the literal null for a nil pointer, else the goString of
// the pointed-to value. The format distinguishes an absent model/provider (null) from
// an empty string ("").
func appendNullableString(b []byte, s *string) []byte {
	if s == nil {
		return append(b, "null"...)
	}
	return append(b, goString(*s)...)
}

// Sign sets Meta.OwnerPubkey from priv and Sig to the hex ed25519 signature over the
// canonical bytes (sig cleared). Deterministic per RFC-8032, but callers must assert
// the BYTES + that a sig VERIFIES, never a fixed sig value (CryptoKit randomizes).
func (c *Capsule) Sign(priv ed25519.PrivateKey) {
	c.Meta.OwnerPubkey = hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	// canonical() never emits sig, so the signature is inherently over sig-cleared bytes.
	c.Sig = hex.EncodeToString(ed25519.Sign(priv, c.canonical()))
}

// Verify reports whether Sig is a valid ed25519 signature over the canonical bytes for
// Meta.OwnerPubkey. Malformed hex / wrong-length inputs are rejected, never panicked.
func (c Capsule) Verify() bool {
	pub, err := hex.DecodeString(c.Meta.OwnerPubkey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(c.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), c.canonical(), sig)
}
