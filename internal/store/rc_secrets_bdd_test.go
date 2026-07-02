package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// rc_secrets_bdd_test.go makes features/remote/rc_secrets.feature EXECUTABLE at the STORE
// layer (the pure secret/hash discipline; the /rc HTTP surface is bound in Increment 2). It
// drives the real Mem roster with protocol.NewRCLinkCode, proving: the persisted display is
// non-recoverable, the one-time code resolves typo-tolerantly by hash, an RC display is
// visually distinct yet hashes with the SAME BandCodeHash, rotation retires the old hash, and
// every bearer is stored hash-only.

type rcSecState struct {
	db      *Mem
	code    string // the one-time full link code (kept only in the test, never persisted)
	display string
	sid     string
	hostTok string
	attach  string
	oldCode string
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func (s *rcSecState) fresh() error { s.db = NewMem(); return nil }

// enable mints a session exactly as the broker's /rc/enable will: NewRCLinkCode, store
// sha256(tail) + the masked display + the host-token hash. The full code + host token are
// returned once and never persisted.
func (s *rcSecState) enable(wallet string) error {
	code, display, tail := protocol.NewRCLinkCode()
	s.code, s.display = code, display
	s.hostTok = "rc_host_" + tail // a distinct bearer secret (test value)
	s.sid = "rcs_" + tail[:6]
	return s.db.CreateRCSession(RCSession{
		ID: s.sid, OwnerWallet: wallet, Name: "hermes · RogerAI",
		CodeHash:      protocol.BandCodeHash(code),
		CodeExpires:   time.Now().Add(RCCodeTTL).Unix(),
		CodeDisplay:   display,
		HostTokenHash: sha(s.hostTok),
	})
}

func (s *rcSecState) codeReturnedOnce() error {
	if s.code == "" {
		return errMsg("no one-time code was produced")
	}
	return nil
}

func (s *rcSecState) displayMaskedNoTail() error {
	sess, _, _ := s.db.RCSessionByID(s.sid)
	// The tail is masked out (bullets), so the display can never reconstruct the SECRET:
	// hashing the persisted display must NOT reproduce the session's real CodeHash. (The
	// "RC "+freq prefix canonicalizes to a spurious low-entropy string, but it is not the
	// random secret tail and hashes to a value that resolves to no session — asserted next.)
	if protocol.BandCodeHash(sess.CodeDisplay) == sess.CodeHash {
		return errMsg("the masked display reconstructs the secret code hash: " + sess.CodeDisplay)
	}
	if !strings.Contains(sess.CodeDisplay, "••••") {
		return errMsg("the persisted display is not masked: " + sess.CodeDisplay)
	}
	return nil
}

func (s *rcSecState) displayResolvesNothing() error {
	sess, _, _ := s.db.RCSessionByID(s.sid)
	_, ok, _ := s.db.RCSessionByCodeHash(protocol.BandCodeHash(sess.CodeDisplay))
	if ok {
		return errMsg("the masked display resolved to a session")
	}
	return nil
}

func (s *rcSecState) enabledFor(wallet string) error { return s.enable(wallet) }

func (s *rcSecState) attachExact() error { return s.resolve(s.code) }
func (s *rcSecState) attachRetyped() error {
	// lowercase, spaces, dashes, and O-for-0 — CanonicalBandTail must fold it back.
	messy := strings.ToLower(strings.ReplaceAll(s.code, "0", "O"))
	messy = strings.ReplaceAll(messy, "-", " - ")
	return s.resolve(messy)
}
func (s *rcSecState) resolve(input string) error {
	sess, ok, _ := s.db.RCSessionByCodeHash(protocol.BandCodeHash(input))
	if !ok || sess.ID != s.sid {
		return errMsg("code did not resolve to the session: " + input)
	}
	return nil
}
func (s *rcSecState) resolvesToSession() error   { return nil } // asserted inside resolve()
func (s *rcSecState) resolvesSameSession() error { return nil }

func (s *rcSecState) displayHasRCMarker() error {
	if !strings.HasPrefix(s.display, "RC ") {
		return errMsg("link display is not RC-marked: " + s.display)
	}
	return nil
}
func (s *rcSecState) sameBandHash() error {
	// The RC code hashes identically to a band code with the same tail (one lookup serves both).
	sess, _, _ := s.db.RCSessionByID(s.sid)
	if sess.CodeHash != protocol.BandCodeHash(s.code) {
		return errMsg("RC code hash is not BandCodeHash(code)")
	}
	return nil
}

func (s *rcSecState) rotate() error {
	code, display, _ := protocol.NewRCLinkCode()
	sess, _, _ := s.db.RCSessionByID(s.sid)
	s.oldCode = s.code
	s.code, s.display = code, display
	sess.CodeHash = protocol.BandCodeHash(code)
	sess.CodeDisplay = display
	sess.CodeExpires = time.Now().Add(RCCodeTTL).Unix()
	return s.db.UpdateRCSession(sess)
}
func (s *rcSecState) oldCodeDead() error {
	if _, ok, _ := s.db.RCSessionByCodeHash(protocol.BandCodeHash(s.oldCode)); ok {
		return errMsg("the rotated-away code still resolves")
	}
	return nil
}
func (s *rcSecState) newCodeResolves() error { return s.resolve(s.code) }

func (s *rcSecState) hostTokenHashOnly() error {
	sess, _, _ := s.db.RCSessionByID(s.sid)
	if sess.HostTokenHash == s.hostTok || sess.HostTokenHash != sha(s.hostTok) {
		return errMsg("host token is not stored hash-only")
	}
	return nil
}
func (s *rcSecState) deviceAttaches() error {
	s.attach = "rc_at_" + s.sid + "_dev"
	return s.db.PutRCAttachToken(RCAttachToken{Hash: sha(s.attach), SessionID: s.sid, DeviceLabel: "web"})
}
func (s *rcSecState) attachTokenHashOnly() error {
	t, ok, _ := s.db.RCAttachTokenByHash(sha(s.attach))
	if !ok {
		return errMsg("attach token not found by hash")
	}
	if t.Hash == s.attach {
		return errMsg("attach token stored in the clear")
	}
	return nil
}

// oldCode carries the pre-rotation code for the rotation scenario.
func (s *rcSecState) reset() { *s = rcSecState{} }

type rcSecErr string

func (e rcSecErr) Error() string { return string(e) }
func errMsg(m string) error      { return rcSecErr(m) }

func TestRCSecretsFeature(t *testing.T) {
	s := &rcSecState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.Step(`^a fresh store$`, s.fresh)
			sc.Step(`^a remote-control session is enabled for wallet "([^"]*)"$`, s.enable)
			sc.Step(`^a remote-control session enabled for wallet "([^"]*)"$`, s.enabledFor)
			sc.Step(`^the one-time link code is returned exactly once$`, s.codeReturnedOnce)
			sc.Step(`^the stored code display is masked and carries no resolvable tail$`, s.displayMaskedNoTail)
			sc.Step(`^resolving the stored display finds no session$`, s.displayResolvesNothing)
			sc.Step(`^the owner attaches with the exact link code$`, s.attachExact)
			sc.Step(`^it resolves to that session$`, s.resolvesToSession)
			sc.Step(`^the owner attaches with the link code retyped with lowercase, spaces, and O-for-0$`, s.attachRetyped)
			sc.Step(`^it resolves to that same session$`, s.resolvesSameSession)
			sc.Step(`^the link display begins with the "RC " marker$`, s.displayHasRCMarker)
			sc.Step(`^its hash is computed by the same BandCodeHash the private bands use$`, s.sameBandHash)
			sc.Step(`^the owner rotates the session link code$`, s.rotate)
			sc.Step(`^the old link code no longer resolves$`, s.oldCodeDead)
			sc.Step(`^the new link code resolves to the same session$`, s.newCodeResolves)
			sc.Step(`^the stored session carries a host-token HASH, never the host token$`, s.hostTokenHashOnly)
			sc.Step(`^a device attaches and receives an attach token$`, s.deviceAttaches)
			sc.Step(`^the stored attach token is a HASH, never the token$`, s.attachTokenHashOnly)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/remote/rc_secrets.feature"},
			TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("remote/rc_secrets behavior scenarios failed")
	}
}
