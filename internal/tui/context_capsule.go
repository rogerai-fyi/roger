package tui

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/client"
)

// context_capsule.go is the TUI side of roger.context.v1: a MINIMAL per-turn ring (ruling
// Q4) that records each completed turn so a conversation can be EXPORTED into a signed,
// portable capsule on an operator handoff, and a returning capsule MERGED back append-only
// on recall. The flat transcript/agentLines slices stay the render source (no render
// rewrite); this ring exists only to feed export/merge.
//
// Stage 1 handoff is SAME-OWNER / LOCAL only: the capsule is written to a file the local
// guest process can read, and its return capsule is merged back. The encrypted broker
// transport for a MARKETPLACE/STRANGER guest is a follow-on (ruling Q3); a stranger export
// is summary-only by default (redaction invariant) and gated here with a clear message.

// contextRingCap bounds the per-turn ring: the capsule carries at most the most recent N
// completed turns (older turns age out, but their turn INDEX is preserved so a later merge
// still dedups correctly).
const contextRingCap = 400

// handoffCapsuleFile / recallCapsuleFile are the local same-owner rendezvous under the
// guest's workdir: the DJ writes the outbound context, the guest writes its return.
const (
	handoffDir         = ".roger"
	handoffCapsuleFile = "context.rcap.json"
	recallCapsuleFile  = "return.rcap.json"
)

// recordTurn appends one completed turn to the per-turn ring (Q4), assigning the next
// sequential turn index. mdl/provider are pointers so an unknown value carries as a literal
// null in the capsule (distinct from an empty string). It is a no-op for an empty
// role+content. The ring is bounded to contextRingCap (oldest ages out).
func (m *model) recordTurn(role, content, agent string, mdl, provider *string) {
	if role == "" && content == "" {
		return
	}
	msg := capsule.Message{Role: role, Content: content, XRoger: capsule.XRoger{
		Turn: m.ringTurn, Agent: agent, Model: mdl, Provider: provider, TS: time.Now().Unix(),
	}}
	m.ringTurn++
	m.ring = append(m.ring, msg)
	if len(m.ring) > contextRingCap {
		m.ring = m.ring[len(m.ring)-contextRingCap:]
	}
}

// contextThreadID returns this session's stable origin thread id, minting one on first use.
func (m *model) contextThreadID() string {
	if m.threadID == "" {
		m.threadID = "th_" + randHex(8)
	}
	return m.threadID
}

// exportContextCapsule builds a signed roger.context.v1 capsule from the ring using the
// operator's EXISTING identity (client.LoadOrCreateUserKey - no new key is minted). When
// summaryOnly is set (the STRANGER default), the capsule carries only the summary + the
// current turn, no full transcript or memory (redaction invariant).
func (m *model) exportContextCapsule(summaryOnly bool) (capsule.Capsule, error) {
	title := ""
	if m.connected != nil {
		title = m.connected.Model
	}
	d := capsule.Draft{
		ID:        "cap_" + randHex(8),
		Thread:    capsule.Thread{OriginThreadID: m.contextThreadID(), Title: title, BaseWatermark: m.ringTurn},
		Redaction: "full",
		Messages:  append([]capsule.Message(nil), m.ring...),
	}
	if summaryOnly {
		d = capsule.SummaryOnly(d)
	}
	return capsule.Export(d, client.LoadOrCreateUserKey(), "roger-cli", nil)
}

// mergeReturnCapsule verifies a returning capsule and append-only merges its turns into the
// ring (never truncate/replace). It returns the number of NEW turns added.
func (m *model) mergeReturnCapsule(raw []byte) (int, error) {
	incoming, err := capsule.Import(raw)
	if err != nil {
		return 0, err
	}
	base := capsule.Capsule{Capsule: capsule.Version, Thread: capsule.Thread{BaseWatermark: m.ringTurn}, Messages: m.ring}
	merged, err := capsule.Merge(incoming, base)
	if err != nil {
		return 0, err
	}
	added := len(merged.Messages) - len(m.ring)
	m.ring = merged.Messages
	m.ringTurn = merged.Thread.BaseWatermark
	return added, nil
}

// writeHandoffCapsule exports the current conversation and writes it under the guest's
// workdir so a SAME-OWNER local guest can import it (the reference the guest reads, not
// bytes inline on a frame). Best-effort: it returns the path written, or an error the
// caller narrates without aborting the handoff. An empty ring writes nothing.
func (m *model) writeHandoffCapsule(workdir string) (string, error) {
	if len(m.ring) == 0 {
		return "", nil
	}
	c, err := m.exportContextCapsule(false) // same-owner local guest gets the full transcript
	if err != nil {
		return "", err
	}
	raw, err := c.Marshal()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(workdir, handoffDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, handoffCapsuleFile)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// strangerHandoffBroker returns the broker endpoint to publish a stranger capsule to, or ""
// when the encrypted stranger transport is not enabled. It is OFF by default (Stage 3 is
// build-and-hold, pending founder ratification of the crypto choices): it requires BOTH the
// ROGERAI_CAPSULE_STRANGER opt-in AND a known broker endpoint. Gating it here (not in the
// operator exec) keeps the same-owner LOCAL handoff the unchanged default.
func (m *model) strangerHandoffBroker() string {
	if os.Getenv("ROGERAI_CAPSULE_STRANGER") == "" || m.endpoint == "" {
		return ""
	}
	return m.endpoint
}

// publishStrangerCapsule is the DJ side of the ENCRYPTED STRANGER transport (Stage 3): it
// exports a SUMMARY-ONLY capsule (the redaction floor), signs it with the operator's existing
// identity, seals it under the one-time code, and mints the ciphertext to the broker's
// content-blind rendezvous. The broker never sees the code, the key, or the plaintext. The
// RAW code is handed to the guest via the reference channel (env / operator_handoff), NEVER
// inline bytes and NEVER on a frame field. client.PublishStrangerCapsule enforces the
// redaction floor (a full capsule is refused). An empty ring publishes nothing.
func (m *model) publishStrangerCapsule(broker, code string) error {
	if len(m.ring) == 0 {
		return nil
	}
	c, err := m.exportContextCapsule(true) // summary-only for a stranger (redaction invariant)
	if err != nil {
		return err
	}
	raw, err := c.Marshal()
	if err != nil {
		return err
	}
	return client.PublishStrangerCapsule(broker, code, raw)
}

// resolveStrangerRecall is the DJ side of the RETURN path: it resolves the guest's return
// capsule from the broker under the FRESH recall code (no key reuse), opens it, and merges it
// back into the ring append-only (verify-before-merge inside mergeReturnCapsule). It returns
// the number of new turns added. A gone/expired/wrong-code recall is client.ErrCapsuleGone.
func (m *model) resolveStrangerRecall(broker, recallCode string) (int, error) {
	raw, err := client.FetchCapsule(broker, recallCode)
	if err != nil {
		return 0, err
	}
	return m.mergeReturnCapsule(raw)
}

// readRecallCapsule merges a guest's return capsule (if it left one under the workdir) back
// into the ring append-only. It returns the number of turns added (0 when no return file
// exists - the common case), or an error the caller narrates. A missing file is not an
// error.
func (m *model) readRecallCapsule(workdir string) (int, error) {
	path := filepath.Join(workdir, handoffDir, recallCapsuleFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return m.mergeReturnCapsule(raw)
}

// channelAgent is the x_roger.agent for a CHANNEL assistant turn: "roger:<model>" when a
// band is tuned, else "roger".
func (m *model) channelAgent() string {
	if m.connected != nil && m.connected.Model != "" {
		return "roger:" + m.connected.Model
	}
	return "roger"
}

// channelModelProvider returns the model + provider pointers for a CHANNEL assistant turn:
// the tuned band's public model (nil if none) and the broker-reported provider (nil if
// empty). Nil pointers become a literal null in the capsule (distinct from "").
func (m *model) channelModelProvider(provider string) (mdl, prov *string) {
	if m.connected != nil && m.connected.Model != "" {
		mm := m.connected.Model
		mdl = &mm
	}
	if provider != "" {
		pp := provider
		prov = &pp
	}
	return mdl, prov
}

// randHex returns n random bytes hex-encoded (2n chars). Used for opaque capsule/thread
// ids; rand.Read from crypto/rand does not fail in practice, and a short id is cosmetic.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
