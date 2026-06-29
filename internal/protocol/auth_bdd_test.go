package protocol

// auth_bdd_test.go wires godog so the relay-auth behavior SPEC (features/relay/auth.feature)
// is an EXECUTABLE Cucumber test, not just a document. The step definitions drive the real
// SignRequest / VerifyRequest, so the scenarios fail red if the signing contract regresses.
// This is the proven pattern; more domains get their own *_bdd_test.go as they are wired.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

type relayAuthState struct {
	priv           ed25519.PrivateKey
	pubHex, sigHex string
	ts             int64
	method, path   string
	body           []byte
	userIDs        []string
	lastOK         bool
}

func (s *relayAuthState) freshKeypair() error {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	s.priv = priv
	return nil
}

func (s *relayAuthState) sign(method, path, body string) error {
	s.method, s.path, s.body = method, path, []byte(body)
	s.pubHex, s.ts, s.sigHex = SignRequest(s.priv, method, path, s.body)
	return nil
}

func (s *relayAuthState) corruptSig() error {
	raw, err := hex.DecodeString(s.sigHex)
	if err != nil || len(raw) == 0 {
		return err
	}
	raw[0] ^= 0xFF // flip a byte: valid length + hex, wrong signature
	s.sigHex = hex.EncodeToString(raw)
	return nil
}

func (s *relayAuthState) changeBody(b string) error { s.body = []byte(b); return nil }
func (s *relayAuthState) changePath(p string) error { s.path = p; return nil }

// timestamp shifts RE-SIGN with the new ts, so ONLY the skew gate decides the outcome
// (not an incidental signature mismatch from the changed canonical string).
func (s *relayAuthState) tsMinutes(n int) error {
	s.ts = time.Now().Add(time.Duration(n) * time.Minute).Unix()
	sig := ed25519.Sign(s.priv, []byte(CanonicalRequest(s.method, s.path, s.ts, s.body)))
	s.sigHex = hex.EncodeToString(sig)
	return nil
}
func (s *relayAuthState) tsMinutesAgo(n int) error   { return s.tsMinutes(-n) }
func (s *relayAuthState) tsMinutesAhead(n int) error { return s.tsMinutes(n) }

func (s *relayAuthState) setField(field, value string) error {
	switch field {
	case "pubkey":
		s.pubHex = value
	case "signature":
		s.sigHex = value
	default:
		return errUnknownField(field)
	}
	return nil
}

func (s *relayAuthState) verify() error {
	uid, ok := VerifyRequest(s.pubHex, s.sigHex, s.ts, s.method, s.path, s.body)
	s.lastOK = ok
	if ok {
		s.userIDs = append(s.userIDs, uid)
	} else {
		s.userIDs = append(s.userIDs, "")
	}
	return nil
}

func (s *relayAuthState) succeeds() error {
	if !s.lastOK {
		return errExpect("verification to succeed, but it failed")
	}
	return nil
}
func (s *relayAuthState) fails() error {
	if s.lastOK {
		return errExpect("verification to fail, but it succeeded")
	}
	return nil
}

func (s *relayAuthState) idDerived() error {
	if !s.lastOK {
		return errExpect("a successful verification")
	}
	if got := s.userIDs[len(s.userIDs)-1]; got != UserIDFromPubkey(s.pubHex) {
		return errExpect("user id derived from the pubkey, got " + got)
	}
	return nil
}

func (s *relayAuthState) sameID() error {
	if len(s.userIDs) < 2 {
		return errExpect("two verifications")
	}
	a, b := s.userIDs[0], s.userIDs[len(s.userIDs)-1]
	if a == "" || a != b {
		return errExpect("both verifications to resolve to the same non-empty user id")
	}
	return nil
}

type bddErr string

func (e bddErr) Error() string       { return string(e) }
func errExpect(s string) error       { return bddErr("expected " + s) }
func errUnknownField(f string) error { return bddErr("unknown field " + f) }

func TestRelayAuthBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &relayAuthState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = relayAuthState{}
				return ctx, nil
			})
			sc.Step(`^a fresh consumer keypair$`, st.freshKeypair)
			sc.Step(`^the consumer signs "([^"]*)" "([^"]*)" with body "([^"]*)"$`, st.sign)
			sc.Step(`^the broker verifies the signed request$`, st.verify)
			sc.Step(`^the signature is corrupted$`, st.corruptSig)
			sc.Step(`^the body is changed to "([^"]*)" before verification$`, st.changeBody)
			sc.Step(`^the path is changed to "([^"]*)" before verification$`, st.changePath)
			sc.Step(`^the request timestamp is set to (\d+) minutes ago$`, st.tsMinutesAgo)
			sc.Step(`^the request timestamp is set to (\d+) minutes ahead$`, st.tsMinutesAhead)
			sc.Step(`^the (pubkey|signature) is set to "([^"]*)"$`, st.setField)
			sc.Step(`^verification succeeds$`, st.succeeds)
			sc.Step(`^verification fails$`, st.fails)
			sc.Step(`^the user id is derived from the public key$`, st.idDerived)
			sc.Step(`^both verifications resolve to the same user id$`, st.sameID)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/relay/auth.feature"},
			TestingT: t,
			Strict:   true, // undefined/pending steps FAIL: every step must be wired
		},
	}
	if suite.Run() != 0 {
		t.Fatal("relay/auth behavior scenarios failed (see godog output above)")
	}
}
