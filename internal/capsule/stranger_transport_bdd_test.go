package capsule

// stranger_transport_bdd_test.go makes features/capsule/stranger_transport.feature EXECUTABLE
// under godog: the client-side seal/open, the redaction floor + full-refusal, and that a
// decrypt-valid but tamper-signed capsule still fails verify-before-merge. REAL ed25519 + AES-
// GCM/HKDF, no mocks.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type strangerBDD struct {
	priv        ed25519.PrivateKey
	capsuleJSON []byte // the signed summary-only capsule (wire JSON)
	code        string
	blob        []byte
	opened      []byte
	openErr     error
	sealErr     error
	built       Capsule
	recall      string
	recallBlob  []byte
	openTried   bool
}

// --- key domain-separation steps (the load-bearing invariant) ---

func (s *strangerBDD) keyNeqLookup() error {
	key := transportKey(s.code)
	lookup := TransportLookup(s.code)
	if len(key) != 32 {
		return bddErrf("key length %d, want 32", len(key))
	}
	if bytes.Equal([]byte(lookup), key) {
		return bddErrf("key equals lookup (not domain-separated)")
	}
	return nil
}

func (s *strangerBDD) lookupIsSha256Tail() error {
	if TransportLookup(s.code) != protocol.BandCodeHash(s.code) {
		return bddErrf("lookup is not BandCodeHash(code)")
	}
	return nil
}

func (s *strangerBDD) unrecoverableWithoutCode() error {
	lookup := TransportLookup(s.code)
	if _, err := OpenWithCode(s.blob, lookup); err == nil {
		return bddErrf("the lookup opened the blob (broker could decrypt)")
	}
	if _, err := OpenWithCode(s.blob, ""); err == nil {
		return bddErrf("an empty code opened the blob")
	}
	return nil
}

func (s *strangerBDD) freshKeypair() error {
	_, s.priv, _ = ed25519.GenerateKey(nil)
	return nil
}

// signedSummaryCapsule builds a signed summary-only capsule (one turn, no memory).
func (s *strangerBDD) signedSummaryCapsule() error {
	d := SummaryOnly(Draft{
		ID:        "cap_s",
		Thread:    Thread{OriginThreadID: "t1", BaseWatermark: 3},
		Redaction: "full",
		Memory:    Memory{Notes: "should-be-dropped", Facts: []string{"secret"}},
		Messages: []Message{
			{Role: "user", Content: "old-1", XRoger: XRoger{Turn: 0}},
			{Role: "assistant", Content: "old-2", XRoger: XRoger{Turn: 1}},
			{Role: "user", Content: "current-turn", XRoger: XRoger{Turn: 2}},
		},
	})
	c, err := Export(d, s.priv, "roger-cli", func() int64 { return 100 })
	if err != nil {
		return err
	}
	s.built = c
	raw, err := c.Marshal()
	s.capsuleJSON = raw
	return err
}

func (s *strangerBDD) signedFullCapsule() error {
	d := Draft{
		ID:        "cap_f",
		Thread:    Thread{OriginThreadID: "t1", BaseWatermark: 1},
		Redaction: "full",
		Messages:  []Message{{Role: "user", Content: "full-body", XRoger: XRoger{Turn: 0}}},
	}
	c, err := Export(d, s.priv, "roger-cli", func() int64 { return 100 })
	if err != nil {
		return err
	}
	s.capsuleJSON, err = c.Marshal()
	return err
}

func (s *strangerBDD) fullTranscriptConversation() error {
	// a "full" draft with memory + many turns, pre-redaction.
	s.built = Capsule{}
	d := Draft{
		ID:        "cap_full",
		Thread:    Thread{OriginThreadID: "t1", BaseWatermark: 4},
		Redaction: "full",
		Memory:    Memory{Notes: "notes", Facts: []string{"f1", "f2"}},
		Messages: []Message{
			{Role: "user", Content: "t0", XRoger: XRoger{Turn: 0}},
			{Role: "assistant", Content: "t1", XRoger: XRoger{Turn: 1}},
			{Role: "user", Content: "t2", XRoger: XRoger{Turn: 2}},
			{Role: "assistant", Content: "t3-current", XRoger: XRoger{Turn: 3}},
		},
	}
	c, err := Export(SummaryOnly(d), s.priv, "roger-cli", func() int64 { return 100 })
	if err != nil {
		return err
	}
	s.built = c
	return nil
}

func (s *strangerBDD) sealAnyForFreshCode() error {
	s.code, _, _ = protocol.NewRCLinkCode()
	blob, err := SealForCode([]byte(`{"capsule":"roger.context.v1","redaction":"summary"}`), s.code)
	s.blob = blob
	return err
}

func (s *strangerBDD) sealForFreshCode() error {
	s.code, _, _ = protocol.NewRCLinkCode()
	blob, err := SealForStranger(s.capsuleJSON, s.code)
	s.blob = blob
	return err
}

func (s *strangerBDD) sealAndOpenSameCode() error {
	if err := s.sealForFreshCode(); err != nil {
		return err
	}
	s.opened, s.openErr = OpenWithCode(s.blob, s.code)
	return s.openErr
}

func (s *strangerBDD) openSameCode() error {
	s.opened, s.openErr = OpenWithCode(s.blob, s.code)
	s.openTried = true
	return nil
}

func (s *strangerBDD) openDifferentCode() error {
	other, _, _ := protocol.NewRCLinkCode()
	s.opened, s.openErr = OpenWithCode(s.blob, other)
	s.openTried = true
	return nil
}

func (s *strangerBDD) flipAByte() error {
	s.blob[len(s.blob)-1] ^= 0x01
	return nil
}

func (s *strangerBDD) truncateToNonce() error {
	s.blob = s.blob[:12]
	return nil
}

func (s *strangerBDD) tamperThenSeal() error {
	// tamper the SIGNED body then seal: decrypt will succeed, verify must fail.
	tampered := bytes.Replace(s.capsuleJSON, []byte("current-turn"), []byte("HIJACKED-TURN"), 1)
	s.code, _, _ = protocol.NewRCLinkCode()
	blob, err := SealForCode(tampered, s.code) // SealForCode, not SealForStranger (redaction still summary)
	s.blob = blob
	return err
}

func (s *strangerBDD) tryStrangerSeal() error {
	s.code, _, _ = protocol.NewRCLinkCode()
	_, s.sealErr = SealForStranger(s.capsuleJSON, s.code)
	return nil
}

func (s *strangerBDD) buildStrangerCapsule() error { return s.fullTranscriptConversation() }

// --- Then ---

func (s *strangerBDD) openedVerifies() error {
	if s.openErr != nil {
		return bddErrf("open failed: %v", s.openErr)
	}
	c, err := Import(s.opened)
	if err != nil {
		return bddErrf("opened capsule does not import/verify: %v", err)
	}
	s.built = c
	return nil
}

func (s *strangerBDD) openedRedactionIs(want string) error {
	c, err := Import(s.opened)
	if err != nil {
		return err
	}
	if c.Redaction != want {
		return bddErrf("redaction=%q want %q", c.Redaction, want)
	}
	return nil
}

func (s *strangerBDD) openFailsNoPlaintext() error {
	// scenarios that flip/truncate the blob have no explicit open step: perform the open
	// here (with the correct code) so the assertion is that a MANGLED blob cannot be opened.
	if !s.openTried {
		s.opened, s.openErr = OpenWithCode(s.blob, s.code)
	}
	if s.openErr == nil {
		return bddErrf("open unexpectedly succeeded (plaintext leaked)")
	}
	if len(s.opened) != 0 {
		return bddErrf("open returned bytes on failure")
	}
	return nil
}

func (s *strangerBDD) decryptOKButVerifyFails() error {
	if s.openErr != nil {
		return bddErrf("decrypt should succeed (valid GCM): %v", s.openErr)
	}
	// the bytes decrypt, but merging must reject on the ed25519 signature.
	_, err := Merge(mustImportRaw(s.opened), Capsule{Capsule: Version, Thread: Thread{BaseWatermark: 0}})
	if err != ErrUnverified {
		return bddErrf("merge err = %v, want ErrUnverified (tamper must fail verify-before-merge)", err)
	}
	return nil
}

func (s *strangerBDD) mergeSucceedsAppendOnly() error {
	incoming, err := Import(s.opened)
	if err != nil {
		return err
	}
	out, err := Merge(incoming, Capsule{Capsule: Version, Thread: Thread{BaseWatermark: incoming.Messages[0].XRoger.Turn}})
	if err != nil {
		return bddErrf("merge failed: %v", err)
	}
	if len(out.Messages) == 0 {
		return bddErrf("merge appended nothing")
	}
	return nil
}

func (s *strangerBDD) strangerRedactionSummary() error {
	if s.built.Redaction != "summary" {
		return bddErrf("stranger capsule redaction=%q want summary", s.built.Redaction)
	}
	return nil
}

func (s *strangerBDD) onlyCurrentTurnNoMemory() error {
	if len(s.built.Messages) != 1 {
		return bddErrf("stranger capsule carries %d turns, want 1 (current only)", len(s.built.Messages))
	}
	if s.built.Memory.Notes != "" || len(s.built.Memory.Facts) != 0 {
		return bddErrf("stranger capsule still carries memory")
	}
	return nil
}

func (s *strangerBDD) sealRefusedFull() error {
	if s.sealErr != ErrNotSummary {
		return bddErrf("seal err = %v, want ErrNotSummary", s.sealErr)
	}
	return nil
}

// --- recall (fresh code) ---

func (s *strangerBDD) sealedUnderOutbound() error {
	s.code, _, _ = protocol.NewRCLinkCode()
	blob, err := SealForCode([]byte(`{"capsule":"roger.context.v1","redaction":"summary"}`), s.code)
	s.blob = blob
	return err
}

func (s *strangerBDD) guestReturnsFreshRecall() error {
	recallCode, _, _ := protocol.NewRCLinkCode()
	if recallCode == s.code {
		return bddErrf("recall code collided with outbound (no fresh code)")
	}
	s.recall = recallCode
	blob, err := SealForCode([]byte(`{"capsule":"roger.context.v1","redaction":"summary"}`), recallCode)
	s.recallBlob = blob
	return err
}

func (s *strangerBDD) recallDiffersFromOutbound() error {
	if s.recall == s.code {
		return bddErrf("recall code equals outbound code")
	}
	if protocol.BandCodeHash(s.recall) == protocol.BandCodeHash(s.code) {
		return bddErrf("recall lookup equals outbound lookup (key reuse)")
	}
	return nil
}

func (s *strangerBDD) openRecallWithOutboundFails() error {
	if _, err := OpenWithCode(s.recallBlob, s.code); err == nil {
		return bddErrf("the outbound code opened the recall blob (key reuse)")
	}
	return nil
}

func mustImportRaw(raw []byte) Capsule {
	var c Capsule
	_ = json.Unmarshal(raw, &c)
	return c
}

func TestStrangerTransportBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &strangerBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = strangerBDD{}
				return ctx, nil
			})
			sc.Step(`^a fresh operator keypair$`, st.freshKeypair)
			sc.Step(`^a signed summary-only capsule for a stranger$`, st.signedSummaryCapsule)
			sc.Step(`^a signed FULL capsule$`, st.signedFullCapsule)
			sc.Step(`^a full-transcript conversation with memory and many turns$`, st.fullTranscriptConversation)
			sc.Step(`^a stranger capsule sealed under an outbound code$`, st.sealedUnderOutbound)

			sc.Step(`^I seal it for a fresh code and open it with the same code$`, st.sealAndOpenSameCode)
			sc.Step(`^I seal it for a fresh code$`, st.sealForFreshCode)
			sc.Step(`^I seal any capsule for a fresh code$`, st.sealAnyForFreshCode)
			sc.Step(`^I open it with the same code$`, st.openSameCode)
			sc.Step(`^I open it with a different code$`, st.openDifferentCode)
			sc.Step(`^I flip a byte of the sealed blob$`, st.flipAByte)
			sc.Step(`^I truncate the sealed blob to the nonce length$`, st.truncateToNonce)
			sc.Step(`^I tamper the capsule body then seal it for a fresh code$`, st.tamperThenSeal)
			sc.Step(`^I merge the opened capsule into a fresh thread$`, func() error { return nil })
			sc.Step(`^I build the stranger capsule$`, st.buildStrangerCapsule)
			sc.Step(`^I try to seal it for a stranger under a fresh code$`, st.tryStrangerSeal)
			sc.Step(`^the guest returns a capsule sealed under a fresh recall code$`, st.guestReturnsFreshRecall)

			sc.Step(`^the opened capsule verifies$`, st.openedVerifies)
			sc.Step(`^the opened capsule redaction is "([^"]*)"$`, st.openedRedactionIs)
			sc.Step(`^opening fails and yields no plaintext$`, st.openFailsNoPlaintext)
			sc.Step(`^the opened bytes decrypt but fail verification on merge$`, st.decryptOKButVerifyFails)
			sc.Step(`^the derived key does not equal the broker lookup$`, st.keyNeqLookup)
			sc.Step(`^the broker lookup is the sha256 of the canonical code tail$`, st.lookupIsSha256Tail)
			sc.Step(`^from the lookup and the blob alone the plaintext is unrecoverable without the code$`, st.unrecoverableWithoutCode)
			sc.Step(`^the stranger capsule redaction is "([^"]*)"$`, func(string) error { return st.strangerRedactionSummary() })
			sc.Step(`^it carries only the current turn and no memory$`, st.onlyCurrentTurnNoMemory)
			sc.Step(`^sealing is refused as full-not-allowed-for-stranger$`, st.sealRefusedFull)
			sc.Step(`^the merge succeeds and appends the turn append-only$`, st.mergeSucceedsAppendOnly)
			sc.Step(`^the recall code differs from the outbound code$`, st.recallDiffersFromOutbound)
			sc.Step(`^opening the recall with the outbound code fails$`, st.openRecallWithOutboundFails)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/capsule/stranger_transport.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("stranger transport scenarios failed (see godog output above)")
	}
}
