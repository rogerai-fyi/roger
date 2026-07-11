package capsule

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Boundary errors. These are the rejections the app<->CLI/guest boundary enforces; the
// copy is deliberately terse (a public repo does not narrate the escalation each guards).
var (
	// ErrUnverified rejects a capsule whose sig does not verify against meta.owner_pubkey.
	ErrUnverified = errors.New("capsule: signature does not verify")
	// ErrUnknownVersion rejects a capsule whose format is not Version.
	ErrUnknownVersion = errors.New("capsule: unknown version")
	// ErrToolCalls rejects a capsule carrying tool_calls (ruling Q1): the canonical form
	// for tool_calls is not yet pinned cross-language, so none may cross the boundary.
	ErrToolCalls = errors.New("capsule: tool_calls not supported at this boundary")
	// ErrForkedTurn rejects a merge where an incoming turn collides with a present turn of
	// the same index but different content (ruling Q2): the whole capsule is rejected and
	// the target is left unchanged - a returning capsule may never rewrite history.
	ErrForkedTurn = errors.New("capsule: forked turn (same turn index, different content)")
)

// turnHash is the dedup/fork identity of a message: sha256 over role and content with a
// NUL separator (so "a"+"b" and "ab" can never collide). Turn index + this hash is a
// message's identity for append-only merge.
func turnHash(m Message) string {
	h := sha256.Sum256([]byte(m.Role + "\x00" + m.Content))
	return hex.EncodeToString(h[:])
}

// validateBoundary runs the version + tool_calls checks every crossing capsule must
// pass. It does NOT verify the signature (callers that need verification call Verify or
// Merge, which does both) - Export produces boundary-valid capsules, Merge/Import
// consume them.
func validateBoundary(c Capsule) error {
	if c.Capsule != Version {
		return ErrUnknownVersion
	}
	for i := range c.Messages {
		if len(c.Messages[i].ToolCalls) > 0 {
			return ErrToolCalls
		}
	}
	return nil
}

// Merge appends the turns in incoming that into does not already have, append-only. It
// (1) rejects an incoming capsule that fails validateBoundary (version / tool_calls),
// (2) rejects one whose sig does not verify (verify-before-merge), (3) rejects the whole
// capsule if any incoming turn forks a present turn (ruling Q2), then (4) appends only
// incoming messages at/after into's base_watermark that are not already present (dedup by
// turn index + turnHash). It NEVER truncates or replaces - a handoff can only add context.
//
// The returned capsule is into with the new turns appended, base_watermark advanced, and
// Sig cleared: the merged thread is the holder's own local state and must be re-signed
// (Export) before it crosses a boundary again. into is trusted local state and is not
// re-verified; only incoming is.
func Merge(incoming, into Capsule) (Capsule, error) {
	if err := validateBoundary(incoming); err != nil {
		return into, err
	}
	if !incoming.Verify() {
		return into, ErrUnverified
	}

	// Identity index of the target: turn index -> content hash. A present turn is one at
	// index t < base_watermark OR any turn already in Messages.
	present := make(map[int]string, len(into.Messages))
	for _, m := range into.Messages {
		present[m.XRoger.Turn] = turnHash(m)
	}

	// Fork check FIRST, so a rejection leaves into fully unchanged (ruling Q2). A fork is
	// any two turns sharing an index but differing in content - checked both against the
	// TARGET and WITHIN the incoming set itself (an incoming capsule that carries turn 2
	// twice with different content is a rewrite too, and must not slip through).
	seen := make(map[int]string, len(incoming.Messages))
	for _, m := range incoming.Messages {
		h := turnHash(m)
		if e, ok := present[m.XRoger.Turn]; ok && e != h {
			return into, ErrForkedTurn
		}
		if e, ok := seen[m.XRoger.Turn]; ok && e != h {
			return into, ErrForkedTurn
		}
		seen[m.XRoger.Turn] = h
	}

	// Append-only: add incoming turns at/after the watermark that are not already present.
	out := into
	out.Sig = "" // messages change; the old signature no longer covers them
	maxTurn := into.Thread.BaseWatermark - 1
	for _, m := range incoming.Messages {
		if m.XRoger.Turn < into.Thread.BaseWatermark {
			continue // the holder already had this turn (or an earlier one); never backdate-insert
		}
		if h, ok := present[m.XRoger.Turn]; ok && h == turnHash(m) {
			continue // exact duplicate already appended - idempotent
		}
		out.Messages = append(out.Messages, m)
		present[m.XRoger.Turn] = turnHash(m)
		if m.XRoger.Turn > maxTurn {
			maxTurn = m.XRoger.Turn
		}
	}
	if maxTurn+1 > out.Thread.BaseWatermark {
		out.Thread.BaseWatermark = maxTurn + 1
	}
	return out, nil
}

// Import decodes and verifies a capsule from raw JSON (a .rcap.json file / stdin). It
// enforces the same boundary as Merge: valid JSON, known version, no tool_calls, and a
// signature that verifies. The receiving side of the file interop.
func Import(data []byte) (Capsule, error) {
	var c Capsule
	if err := json.Unmarshal(data, &c); err != nil {
		return Capsule{}, err
	}
	if err := validateBoundary(c); err != nil {
		return Capsule{}, err
	}
	if !c.Verify() {
		return Capsule{}, ErrUnverified
	}
	return c, nil
}

// Draft is the unsigned content Export signs into a capsule: everything except the
// producer-stamped meta (exported_by / created_at / owner_pubkey) and the sig, which
// Export fills. Turns carries the ordered messages (the TUI ring feeds these).
type Draft struct {
	ID        string
	Thread    Thread
	Redaction string
	Summary   Summary
	Memory    Memory
	Messages  []Message
	ToolsUsed []string
}

// Export builds a signed roger.context.v1 capsule from d, stamping meta.exported_by =
// exportedBy (e.g. "roger-cli"), created_at = now, and signing with priv (which also
// sets owner_pubkey). It REJECTS a draft carrying tool_calls (ruling Q1) before signing,
// so a capsule that crosses the boundary can never contain them. now is injectable for
// deterministic tests; pass nil to use time.Now.
func Export(d Draft, priv ed25519.PrivateKey, exportedBy string, now func() int64) (Capsule, error) {
	ts := time.Now().Unix
	if now != nil {
		ts = now
	}
	c := Capsule{
		Capsule:   Version,
		ID:        d.ID,
		Thread:    d.Thread,
		Redaction: d.Redaction,
		Summary:   d.Summary,
		Memory:    d.Memory,
		Messages:  d.Messages,
		Meta: Meta{
			ToolsUsed:  d.ToolsUsed,
			ExportedBy: exportedBy,
			CreatedAt:  ts(),
		},
	}
	if err := validateBoundary(c); err != nil {
		return Capsule{}, err
	}
	c.Sign(priv)
	return c, nil
}

// Marshal serializes a signed capsule to its wire JSON (a .rcap.json file). It is the
// standard encoding/json form (the sig-bearing at-rest object), distinct from the
// canonical signing bytes.
func (c Capsule) Marshal() ([]byte, error) { return json.Marshal(c) }

// SummaryOnly returns the redacted draft the CLI hands to a MARKETPLACE/STRANGER
// operator: redaction="summary", memory dropped, and only the CURRENT (last) turn kept -
// no full transcript. The redaction level is signed (it is in canonical()), so a stranger
// cannot silently upgrade a summary-only capsule to full and re-sign. Same-owner/trusted
// targets get the full draft; the encrypted stranger transport itself is a follow-on (Q3).
func SummaryOnly(d Draft) Draft {
	d.Redaction = "summary"
	d.Memory = Memory{}
	if n := len(d.Messages); n > 0 {
		d.Messages = d.Messages[n-1:]
	}
	return d
}
