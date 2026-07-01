// Package protocol holds the shared types for RogerAI P0: model offers, node
// registration, and the hash-chained, co-signed UsageReceipt that is the basis
// of the "model-lineage guarantee" - every served request produces a receipt
// signed by the node and counter-signed by the broker. (P1 adds independent
// token re-count + activation/logprob lineage proofs; the hooks live here.)
package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ModelOffer is one model a node exposes, with per-1M-unit credit pricing. Chat bills per
// token; voice adds tts (per input char) and stt (per audio-byte) — see Modality/Unit.
// Schedule (optional) overrides the base price by time-of-use (ChargePoint-style).
type ModelOffer struct {
	Model string `json:"model"`
	// Modality is what the model DOES: "" (back-compat) and "chat" bill per token; "tts"
	// (speak, /v1/audio/speech) bills per input char; "stt" (listen, /v1/audio/transcriptions)
	// bills per audio-byte. See VOICE-AUDIO-DESIGN.md.
	Modality string `json:"modality,omitempty"`
	// Unit is the billing unit, CANONICAL for the modality (token|char|byte) — set by
	// Normalize, never trusted from the wire, so a node cannot mis-state how it is metered.
	Unit     string  `json:"unit,omitempty"`
	PriceIn  float64 `json:"price_in"`  // credits per 1,000,000 input units (tokens or chars; see Unit)
	PriceOut float64 `json:"price_out"` // credits per 1,000,000 output units (tokens or audio-bytes)
	Ctx      int     `json:"ctx"`
	// CtxEstimated is true when Ctx is the last-resort default (no upstream reported a
	// real per-model window), so the UI can render it as an estimate (~32k, dim) instead
	// of a detected value (131k, solid). Truth-in-labeling, like TokenizerExact on the
	// receipt: a guess is never displayed as a measured fact.
	CtxEstimated bool          `json:"ctx_estimated,omitempty"`
	Schedule     []PriceWindow `json:"schedule,omitempty"`
	// Voice metadata (optional; set only for voice offers) — surfaced by GET /voices for the app
	// picker (BROKER-VOICE-API.md). Passive display labels ONLY; a node address is never here.
	Name      string `json:"name,omitempty"`
	Language  string `json:"language,omitempty"`
	SampleURL string `json:"sample_url,omitempty"`
	LatencyMS int    `json:"latency_ms,omitempty"`
}

// Modality + Unit values. The enum is CLOSED (ValidModality); the unit is DERIVED from the
// modality (canonicalUnit), never trusted from the wire — truth-in-labeling for how a request
// is metered, like CtxEstimated for the context window.
const (
	ModalityChat = "chat" // /v1/chat/completions,      billed per token
	ModalityTTS  = "tts"  // /v1/audio/speech,          billed per input char
	ModalitySTT  = "stt"  // /v1/audio/transcriptions,  billed per audio-byte

	UnitToken = "token"
	UnitChar  = "char"
	UnitByte  = "byte"
)

// canonicalUnit is the ONE billing unit for a modality. A tts offer always bills chars and an
// stt offer always bills audio-bytes, regardless of what unit the node claimed.
func canonicalUnit(modality string) string {
	switch modality {
	case ModalityTTS:
		return UnitChar
	case ModalitySTT:
		return UnitByte
	default: // chat + the empty back-compat default
		return UnitToken
	}
}

// Normalize fills the back-compat default modality (empty -> chat) and sets the CANONICAL unit
// for that modality. The broker calls it on every registered offer, so a node can never
// mis-state its billing unit (protecting the customer who pays it).
func (o *ModelOffer) Normalize() {
	if o.Modality == "" {
		o.Modality = ModalityChat
	}
	o.Unit = canonicalUnit(o.Modality)
}

// ValidModality reports whether the offer's modality is one the broker accepts. The enum is
// CLOSED — an unknown modality is rejected, not silently trusted. Empty is valid (-> chat).
func (o ModelOffer) ValidModality() bool {
	switch o.Modality {
	case "", ModalityChat, ModalityTTS, ModalitySTT:
		return true
	default:
		return false
	}
}

// PriceWindow is a time-of-use rule. Times are "HH:MM" UTC; a window may wrap past
// midnight. Empty Days = every day (0=Sun..6=Sat). Free zeroes the price (e.g. a
// free 30-min daily window). First matching window wins.
type PriceWindow struct {
	Days  []int   `json:"days,omitempty"`
	Start string  `json:"start"`
	End   string  `json:"end"`
	In    float64 `json:"price_in,omitempty"`
	Out   float64 `json:"price_out,omitempty"`
	Free  bool    `json:"free,omitempty"`
}

// ActivePrice returns the price effective at t (first matching window; Free -> 0),
// falling back to the base price when no window matches. `scheduled` is true when
// a schedule window matched (so the caller knows this is a published time-of-use
// price to charge as-is, not a base price to lock).
func (o ModelOffer) ActivePrice(t time.Time) (in, out float64, free, scheduled bool) {
	for _, w := range o.Schedule {
		if w.Matches(t) {
			if w.Free {
				return 0, 0, true, true
			}
			return w.In, w.Out, false, true
		}
	}
	return o.PriceIn, o.PriceOut, false, false
}

// Matches reports whether t falls in this window (compared in UTC).
func (w PriceWindow) Matches(t time.Time) bool {
	t = t.UTC()
	if len(w.Days) > 0 {
		ok := false
		for _, d := range w.Days {
			if int(t.Weekday()) == d {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	s, ok1 := hhmm(w.Start)
	e, ok2 := hhmm(w.End)
	if !ok1 || !ok2 {
		return false
	}
	cur := t.Hour()*60 + t.Minute()
	if s <= e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e // wraps past midnight
}

func hhmm(s string) (int, bool) {
	p := strings.SplitN(s, ":", 2)
	if len(p) != 2 {
		return 0, false
	}
	h, e1 := strconv.Atoi(strings.TrimSpace(p[0]))
	m, e2 := strconv.Atoi(strings.TrimSpace(p[1]))
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// NodeRegistration is what a node agent POSTs to the broker on startup.
type NodeRegistration struct {
	NodeID    string `json:"node_id"`
	PubKey    string `json:"pub_key"` // hex-encoded ed25519 public key
	BridgeURL string `json:"bridge_url"`
	// BridgeToken is a shared secret the broker presents (Bearer) when relaying
	// to the node's bridge. It secures the PUBLIC tunnel URL so only the broker
	// can use it - randoms who discover the *.trycloudflare.com URL can't.
	BridgeToken string       `json:"bridge_token"`
	Region      string       `json:"region"`
	HW          string       `json:"hw"`
	Offers      []ModelOffer `json:"offers"`
	// Confidential: node claims it runs inference in a TEE/confidential VM where
	// the owner cannot read memory; Attestation is the (to-be-verified) hardware
	// quote. The broker only surfaces `confidential ◆` after CRYPTOGRAPHICALLY
	// verifying the attestation (signature chain to the silicon vendor root, an
	// allowlisted launch measurement, and a fresh nonce binding - see AttestNonce).
	Confidential bool `json:"confidential,omitempty"`
	// Attestation is a base64-encoded TEE quote. For AMD SEV-SNP it is the raw
	// extended attestation report (ATTESTATION_REPORT followed by its VCEK cert
	// table), as returned by the guest /dev/sev-guest device. Empty when the node
	// is not on TEE hardware (an honest node sends NO quote and gets NO badge).
	Attestation string `json:"attestation,omitempty"`
	// AttestKind names the TEE backend that produced Attestation ("sev-snp", later
	// "tdx" / "nvidia-cc"). Lets the broker route to the right verifier.
	AttestKind string `json:"attest_kind,omitempty"`
	// AttestNonce is the broker-issued challenge nonce (hex) this quote was bound
	// to: the quote's report_data MUST equal AttestationReportData(PubKey, nonce),
	// which binds the quote to THIS node's key AND to a fresh broker challenge so a
	// quote cannot be replayed by another node or reused after it goes stale.
	AttestNonce string `json:"attest_nonce,omitempty"`
	// Private marks this node as a PRIVATE band ("frequency code" discovery): the
	// broker hides it from /discover + /market and routes to it ONLY when a caller
	// resolves the node's secret frequency code (see BandID + /bands/resolve). It is
	// covered by regSigningBytes (the Sig field is the only exclusion), so the signed
	// flag cannot be stripped or flipped in flight by anyone but the node's key. A
	// private node MUST be registered by a logged-in owner (anonymous private is
	// rejected at register). See BANDS-DESIGN.
	Private bool `json:"private,omitempty"`
	// BandID is the broker-minted band id ("band_<rand>") this node's private channel
	// is bound to. The node leaves it EMPTY on first register; the broker mints a band
	// (returning the code ONCE in the register response) and echoes the band id on
	// every subsequent register so the node can carry it without ever seeing the
	// secret code again. It tags the node's band for idempotent re-register; it is NOT
	// the secret (that is the Crockford code, stored only as a sha256 hash).
	BandID string `json:"band_id,omitempty"`
	// TS (unix seconds) + Sig prove possession of PubKey's private key and bound the
	// registration to a moment (the broker rejects stale ones to stop replay). Sig is
	// hex(ed25519 sign over regSigningBytes), verified against PubKey on register.
	TS  int64  `json:"ts,omitempty"`
	Sig string `json:"sig,omitempty"`
}

// regSigningBytes is the canonical form a node signs to prove it owns PubKey
// (the Sig field itself is excluded).
func (r NodeRegistration) regSigningBytes() []byte {
	c := r
	c.Sig = ""
	b, _ := json.Marshal(c)
	return b
}

// SignRegistration signs the registration with the node's private key.
func (r *NodeRegistration) SignRegistration(priv ed25519.PrivateKey) {
	r.Sig = hex.EncodeToString(ed25519.Sign(priv, r.regSigningBytes()))
}

// VerifyRegistration confirms Sig was made by the private key matching PubKey -
// i.e. the registrant actually holds the key it claims (proof of possession).
func (r NodeRegistration) VerifyRegistration() bool {
	pub, err := hex.DecodeString(r.PubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(r.Sig)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), r.regSigningBytes(), sig)
}

// AttestChallenge is what POST /nodes/challenge returns: a single-use nonce the
// node must bind its TEE quote to (via the quote's report_data) plus when it
// expires. Binding to a broker-issued, short-lived nonce is what stops a quote
// from being replayed by a different node or reused after it goes stale.
type AttestChallenge struct {
	Nonce   string `json:"nonce"`   // hex; the node folds this into report_data
	Expires int64  `json:"expires"` // unix seconds; the broker rejects a quote after this
}

// AttestationReportData computes the 64-byte report_data a TEE quote MUST carry
// to be accepted: SHA-512 over the node's Ed25519 public key bytes followed by
// the broker's challenge nonce bytes. SHA-512 is used because SEV-SNP report_data
// is exactly 64 bytes. Binding the pubkey makes a quote useless to any OTHER node
// (it cannot forge this node's key), and binding the broker nonce makes it useless
// to replay (the nonce is single-use and short-lived). pubHex/nonceHex are the
// hex encodings carried on the wire; a decode error yields a nil (never-matching)
// result so a malformed input simply fails verification.
func AttestationReportData(pubHex, nonceHex string) []byte {
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		return nil
	}
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil
	}
	h := sha512.New()
	h.Write(pub)
	h.Write(nonce)
	return h.Sum(nil) // 64 bytes
}

// UsageReceipt is the per-request lineage record. It is hash-chained (PrevHash)
// per node, signed by the node, then counter-signed by the broker.
type UsageReceipt struct {
	RequestID        string  `json:"request_id"`
	NodeID           string  `json:"node_id"`
	User             string  `json:"user"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	PriceIn          float64 `json:"price_in"`
	PriceOut         float64 `json:"price_out"`
	TS               int64   `json:"ts"`
	PrevHash         string  `json:"prev_hash"`
	// Lineage proof slot - P0 carries the upstream-reported counts; P1 fills
	// LineageMethod ("toploc"/"logprob") + LineageProof (opaque bytes).
	LineageMethod string `json:"lineage_method,omitempty"`
	LineageProof  string `json:"lineage_proof,omitempty"`
	// L1 independent re-count (broker-side, off the hot path): the broker
	// re-tokenizes the completion with the canonical tokenizer for Model and
	// records its OWN count here. TokenizerExact is false when the re-count used
	// the calibrated heuristic (no exact tokenizer for the model) - then the
	// count is an outlier gate only, never a discrepancy trigger. Settlement
	// still bills the node's count for now; enforced re-bill is the next step
	// (see docs-internal/VERIFICATION-DESIGN.md). 0 = not re-counted.
	BrokerCompletionTokens int `json:"broker_completion_tokens,omitempty"`
	// BrokerPromptTokens is the broker's OWN re-count of the prompt (input) tokens,
	// the symmetric twin of BrokerCompletionTokens. Settlement bills the LESSER of the
	// node's claimed prompt tokens and this broker count, closing the input billing
	// axis that completion-only verification left open. Broker-set AFTER the node signs
	// (so it is zeroed in signingBytes, like GrantID/BrokerCompletionTokens). 0 = not
	// re-counted.
	BrokerPromptTokens int  `json:"broker_prompt_tokens,omitempty"`
	TokenizerExact     bool `json:"tokenizer_exact,omitempty"`
	// GrantID tags a receipt with the owner grant key that served it (empty for
	// public-market traffic), so the owner's dashboard can group usage per grant.
	// Broker-set after the node signs (the node never sees the grant), so it is
	// excluded from the node-signed bytes; see signingBytes.
	GrantID   string `json:"grant_id,omitempty"`
	NodeSig   string `json:"node_sig,omitempty"`
	BrokerSig string `json:"broker_sig,omitempty"`
}

// signingBytes is the canonical form signed by both parties (sigs excluded). The
// broker-set GrantID is also excluded: the node signs before the broker resolves
// the grant (the node never sees it), so including it would break node-sig
// verification. The grant tag is a billing/dashboard annotation, not lineage.
//
// BrokerPromptTokens + BrokerCompletionTokens are the broker's own re-counts,
// assigned AFTER the node signs and BEFORE the broker counter-signs. They MUST be
// zeroed here for the same reason as GrantID: they are not present when the node
// computes its NodeSig, so leaving them in would break VerifyNode. The broker's
// SignBroker (called after they are set) DOES cover them - it re-includes them via
// the same zeroing applied symmetrically, so a broker sig is over the same canonical
// shape but is computed once the broker counts are stable. (The broker counts live
// in the receipt JSON for the audit/lineage trail; they are simply excluded from the
// signed canonical bytes so both signatures verify regardless of count order.)
func (r UsageReceipt) signingBytes() []byte {
	c := r
	c.GrantID = ""
	c.BrokerPromptTokens = 0
	c.BrokerCompletionTokens = 0
	c.NodeSig = ""
	c.BrokerSig = ""
	b, _ := json.Marshal(c)
	return b
}

// Hash is the receipt's content hash (used as the next receipt's PrevHash).
func (r UsageReceipt) Hash() string {
	h := sha256.Sum256(r.signingBytes())
	return hex.EncodeToString(h[:])
}

// Cost in credits = (in*price_in + out*price_out) / 1e6.
func (r UsageReceipt) Cost() float64 {
	return (float64(r.PromptTokens)*r.PriceIn + float64(r.CompletionTokens)*r.PriceOut) / 1e6
}

// CostWith2 is Cost but billing the SUPPLIED prompt + completion token counts instead
// of the receipt's claimed PromptTokens/CompletionTokens, used to settle on
// broker-verified (re-counted) counts on BOTH axes without mutating the node-signed
// receipt. The settle path passes min(claim, recount) for each axis, so an
// over-reporting node is billed (and earns) on the verified lesser count on input AND
// output - closing the input billing gap that the completion-only CostWith left open.
func (r UsageReceipt) CostWith2(promptTokens, completionTokens int) float64 {
	return (float64(promptTokens)*r.PriceIn + float64(completionTokens)*r.PriceOut) / 1e6
}

// CostWith is the back-compat completion-only shim (input billed at the receipt's
// claimed PromptTokens). New call sites use CostWith2 to cap both axes.
func (r UsageReceipt) CostWith(completionTokens int) float64 {
	return r.CostWith2(r.PromptTokens, completionTokens)
}

func (r *UsageReceipt) SignNode(priv ed25519.PrivateKey) {
	r.NodeSig = hex.EncodeToString(ed25519.Sign(priv, r.signingBytes()))
}

func (r *UsageReceipt) SignBroker(priv ed25519.PrivateKey) {
	r.BrokerSig = hex.EncodeToString(ed25519.Sign(priv, r.signingBytes()))
}

func (r UsageReceipt) VerifyNode(pubHex string) bool {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(r.NodeSig)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), r.signingBytes(), sig)
}

// Job is a relayed inference request the broker hands to a polling node.
type Job struct {
	ID   string          `json:"id"`
	User string          `json:"user"`
	Body json.RawMessage `json:"body"` // the raw OpenAI request
	// Path is the upstream endpoint the node's bridge POSTs Body to, relative to the upstream
	// base: empty (or "/v1/chat/completions") = chat, back-compat; "/v1/audio/speech" for TTS.
	// One bridge can thus serve chat + voice from the same node.
	Path string `json:"path,omitempty"`
}

// JobResult is what the node POSTs back after serving a Job.
type JobResult struct {
	ID      string          `json:"id"`
	Status  int             `json:"status"`
	Body    json.RawMessage `json:"body"`
	Receipt UsageReceipt    `json:"receipt"`
}

// NewRequestID returns a short random hex id.
func NewRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// EncodeReceipt / DecodeReceipt for the X-RogerAI-Receipt transport header.
func EncodeReceipt(r UsageReceipt) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func DecodeReceipt(s string) (UsageReceipt, error) {
	var r UsageReceipt
	err := json.Unmarshal([]byte(s), &r)
	return r, err
}
