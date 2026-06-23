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
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ModelOffer is one model a node exposes, with per-1M-token credit pricing.
// Schedule (optional) overrides the base price by time-of-use (ChargePoint-style).
type ModelOffer struct {
	Model    string        `json:"model"`
	PriceIn  float64       `json:"price_in"`  // credits per 1,000,000 input tokens (base/fallback)
	PriceOut float64       `json:"price_out"` // credits per 1,000,000 output tokens (base/fallback)
	Ctx      int           `json:"ctx"`
	Schedule []PriceWindow `json:"schedule,omitempty"`
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
	// the owner cannot read memory; Attestation is the (to-be-verified) quote. The
	// broker only surfaces `confidential ◆` after verifying the attestation.
	Confidential bool   `json:"confidential,omitempty"`
	Attestation  string `json:"attestation,omitempty"`
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
	NodeSig       string `json:"node_sig,omitempty"`
	BrokerSig     string `json:"broker_sig,omitempty"`
}

// signingBytes is the canonical form signed by both parties (sigs excluded).
func (r UsageReceipt) signingBytes() []byte {
	c := r
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
