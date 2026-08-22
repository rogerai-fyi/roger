package tui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"rogerai.fm/roger/v6/internal/brief"
	"rogerai.fm/roger/v6/internal/capsule"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/operator"
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
	m.recordTurnWithCalls(role, content, agent, mdl, provider, nil)
}

// recordTurnWithCalls is recordTurn plus the tool calls the turn made. calls are serialized
// through capsule.ToolCallsRaw so the at-rest bytes are already canonical (the signing
// contract), and an empty set carries as an absent field rather than an empty array.
func (m *model) recordTurnWithCalls(role, content, agent string, mdl, provider *string, calls []capsule.ToolCall) {
	if role == "" && content == "" {
		return
	}
	msg := capsule.Message{Role: role, Content: content, XRoger: capsule.XRoger{
		Turn: m.ringTurn, Agent: agent, Model: mdl, Provider: provider, TS: time.Now().Unix(),
	}}
	if len(calls) > 0 {
		msg.ToolCalls = capsule.ToolCallsRaw(calls)
	}
	m.ringTurn++
	m.ring = append(m.ring, msg)
	if len(m.ring) > contextRingCap {
		m.ring = m.ring[len(m.ring)-contextRingCap:]
	}
}

// capsuleResultCap bounds ONE tool result carried in the capsule. A capsule is handed to
// another agent and may cross the wire; a single fetched page must not be able to make it
// enormous. The transcript the user sees is unaffected - this is the travelling copy.
const capsuleResultCap = 2 << 10 // 2 KiB

// agentSurfaceUser / agentSurfacePrefix tag the turns that happened in the AGENT rather
// than on the channel. The tag is what lets /clear drop exactly what the user cleared
// from their screen, without touching the channel's turns in the same thread.
const (
	agentSurfaceUser   = "user:agent"
	agentSurfacePrefix = "roger-agent"
)

// recordAgentPrompt records the user's agent prompt into the shared ring.
func (m *model) recordAgentPrompt(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	m.recordTurn("user", text, agentSurfaceUser, nil, nil)
}

// recordAgentAnswer records the agent's completed answer, carrying any tool calls the turn
// made. The pending calls are consumed here, so they ride on the turn that made them and
// never leak into the next one.
func (m *model) recordAgentAnswer(text string) {
	calls := m.agentTurnCalls
	m.agentTurnCalls = nil
	if strings.TrimSpace(text) == "" {
		return
	}
	var mdl *string
	agent := agentSurfacePrefix
	if m.agent != nil && m.agent.model != "" {
		model := m.agent.model
		mdl = &model
		agent = agentSurfacePrefix + ":" + model
	}
	m.recordTurnWithCalls("assistant", text, agent, mdl, nil, calls)
}

// noteAgentToolCall opens a tool call for the turn in flight. The result lands later
// (noteAgentToolResult), which is why the two are separate: the capsule's flat ToolCall
// carries the result INLINE on the call, so the pair has to be stitched back together.
func (m *model) noteAgentToolCall(id, name, args string) {
	m.agentTurnCalls = append(m.agentTurnCalls, capsule.ToolCall{ID: id, Name: name, Arguments: args})
}

// noteAgentToolResult closes the most recent open call of that tool with its outcome.
func (m *model) noteAgentToolResult(e harness.Event) {
	for i := len(m.agentTurnCalls) - 1; i >= 0; i-- {
		c := &m.agentTurnCalls[i]
		if c.Name != e.Tool || c.Result != nil || c.Denied {
			continue
		}
		switch {
		case e.Denied:
			// A refusal is context: the call is kept, marked, and carries NO result
			// because nothing ran.
			c.Denied = true
		case e.IsError:
			c.Failed = true
			res := clipCapsule(e.Result)
			c.Result = &res
		default:
			res := clipCapsule(e.Result)
			c.Result = &res
		}
		return
	}
}

// clipCapsule bounds one carried tool result, marking any truncation so a reader (human or
// agent) never mistakes a cut-off page for the whole of it.
func clipCapsule(s string) string {
	s = stripControlBytes(s)
	if len(s) <= capsuleResultCap {
		return s
	}
	return cutRunes(s, capsuleResultCap) + "\n... (truncated)"
}

// stripControlBytes removes C0 control bytes and DEL, keeping newline and tab. Tool results
// are untrusted text (a fetched page, a command's output) and they travel from here into
// another agent's context and another terminal.
func stripControlBytes(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// clearAgentTurns drops the AGENT-surface turns from the ring, leaving the channel's turns
// in place. What the user cleared from their screen must not still travel to a guest.
func (m *model) clearAgentTurns() {
	kept := m.ring[:0]
	for _, msg := range m.ring {
		if msg.XRoger.Agent == agentSurfaceUser || strings.HasPrefix(msg.XRoger.Agent, agentSurfacePrefix) {
			continue
		}
		kept = append(kept, msg)
	}
	m.ring = kept
	m.agentTurnCalls = nil
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

// writeHandoffBrief writes the READABLE half of the handoff beside the capsule: the file a
// guest is told to read first. The capsule is a merge format - perfect for appending a
// returning thread, useless as another agent's opening context.
func (m *model) writeHandoffBrief(workdir string) error {
	path := filepath.Join(workdir, operator.BriefRelPath)
	if len(m.ring) == 0 {
		// Nothing to hand over: clear any brief a PREVIOUS handoff left here, or the guest
		// would be pointed at an old session as though it were the current one.
		_ = os.Remove(path)
		return nil
	}
	cap, err := m.exportContextCapsule(false)
	if err != nil {
		return err
	}
	text := brief.Render(cap)
	if strings.TrimSpace(text) == "" {
		_ = os.Remove(path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// clearHandoffBrief removes a brief left by an earlier handoff in this workdir.
func (m *model) clearHandoffBrief(workdir string) error {
	err := os.Remove(filepath.Join(workdir, operator.BriefRelPath))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// returnNoteFile is the PLAIN note a guest leaves behind. The signed return.rcap.json path
// still works, but no guest that lacks a key can produce one - and Claude Code cannot. This
// file needs no signature: it was written by a process THIS session launched, in a directory
// this session created, on this machine, by this user. A guest that wanted to forge context
// could already run `roger context export` with the user's own key; the signature protects
// the STRANGER path, where "did this really come from them" is the whole question.
var returnNoteFile = filepath.Base(brief.ReturnNoteRelPath)

// returnNoteCap bounds what a returning guest can append. It goes into the ring and travels
// in every capsule after it.
const returnNoteCap = 8 << 10

// readReturnNote reads the guest's plain note, returning the text to append (empty when
// there is nothing to bring back). A note that is not text is refused rather than pasted
// into the transcript.
func (m *model) readReturnNote(workdir, guest string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(workdir, handoffDir, returnNoteFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("the note from %s is not readable text", guest)
	}
	text := strings.TrimSpace(stripControlBytes(string(raw)))
	if text == "" {
		return "", nil
	}
	if len(text) > returnNoteCap {
		text = cutRunes(text, returnNoteCap) + "\n... (truncated)"
	}
	return text, nil
}

// cutRunes truncates to at most n bytes without splitting a multi-byte rune - the note was
// validated as UTF-8 before the cut, and it must still be UTF-8 after it.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// mergeReturnNote appends a guest's note to the thread as ONE turn attributed to the guest.
// Attribution comes from WHO wrote the file, never from what the file says, so a note that
// claims to be a user turn from the band is still recorded as the guest speaking.
func (m *model) mergeReturnNote(workdir, guest string) (bool, error) {
	text, err := m.readReturnNote(workdir, guest)
	// The note is a ONE-TIME rendezvous: consume it either way. Left behind it would merge
	// again on every later handoff in this workdir, attributed to whichever guest came next
	// - and an unreadable one would re-narrate its failure forever.
	_ = os.Remove(filepath.Join(workdir, handoffDir, returnNoteFile))
	if err != nil || text == "" {
		return false, err
	}
	m.recordTurn("assistant", text, "guest:"+guest, nil, nil)
	return true, nil
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
