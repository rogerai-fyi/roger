package main

// account_rulings_bdd_test.go - the steps for the founder's 2026-10-04 rulings on the account
// fixes: several devices for one email account (the verified-address uniqueness rule binds only
// GitHub- and Apple-linked accounts), and cap notices that reach only a proven or
// provider-reported address. Real broker, real store (Postgres when ROGERAI_TEST_DATABASE_URL is
// set): devices are approved through the real device flow, profile edits go through the real
// PATCH /account handler, and a proven address is linked by the real approve path.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/store"
)

func (s *eaState) registerDeviceRulingSteps(sc *godog.ScenarioContext) {
	sc.Step(`^"([^"]+)" approves (\d+) devices$`, s.approvesN)
	sc.Step(`^every approval succeeds$`, s.everyApprovalSucceeds)
	sc.Step(`^both approvals succeed$`, s.everyApprovalSucceeds)
	sc.Step(`^a paid request from each device is served from "([^"]+)"'s account wallet$`, s.eachDeviceServedFrom)
	sc.Step(`^"([^"]+)" approves the same device twice$`, s.approvesSameTwice)
	sc.Step(`^the device spends from "([^"]+)"'s account wallet$`, s.eachDeviceServedFrom)
	sc.Step(`^a (GitHub-linked|Apple-linked) account holds the verified address "([^"]+)"$`, s.providerHolds)
	sc.Step(`^a different (GitHub-linked|Apple-linked) account proves the address "([^"]+)"$`, s.differentProves)
	sc.Step(`^the second link is refused$`, s.secondLinkRefused)
	sc.Step(`^"([^"]+)" still resolves to the (GitHub-linked|Apple-linked) account$`, s.stillResolvesToFirst)
	sc.Step(`^that account has been deleted and anonymized$`, s.firstDeleted)
	sc.Step(`^the link succeeds$`, s.linkSucceeds)
}

func (s *eaState) approvesN(addr string, n int) error {
	if s.u(addr) != s.addr {
		return fmt.Errorf("%s is not the signed-in account", addr)
	}
	for i := 0; i < n; i++ {
		k, err := s.approveDevice(s.session)
		s.devKeys = append(s.devKeys, k)
		s.approveErrs = append(s.approveErrs, err)
	}
	return nil
}

func (s *eaState) approvesSameTwice(addr string) error {
	if s.u(addr) != s.addr {
		return fmt.Errorf("%s is not the signed-in account", addr)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	s.devKeys = []ed25519.PrivateKey{priv}
	s.approveErrs = []error{s.approveKey(s.session, priv), s.approveKey(s.session, priv)}
	return nil
}

func (s *eaState) everyApprovalSucceeds() error {
	if len(s.approveErrs) == 0 {
		return fmt.Errorf("no approval was made")
	}
	for i, err := range s.approveErrs {
		if err != nil {
			return fmt.Errorf("approval %d failed: %v", i+1, err)
		}
	}
	return nil
}

func (s *eaState) eachDeviceServedFrom(addr string) error {
	wallet := walletForEmail(s.u(addr))
	for i, k := range s.devKeys {
		if k == nil {
			return fmt.Errorf("device %d has no key", i+1)
		}
		before, _ := s.db.PeekBalance(wallet)
		rec := s.relayAs("", k, "qwen3-32b", "n-1", false, nil)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("device %d: paid request = %d %s, want 200", i+1, rec.Code, rec.Body.String())
		}
		cost, err := strconv.ParseFloat(rec.Header().Get("X-RogerAI-Cost"), 64)
		if err != nil || cost <= 0 {
			return fmt.Errorf("device %d: X-RogerAI-Cost %q, want a positive cost", i+1, rec.Header().Get("X-RogerAI-Cost"))
		}
		after, _ := s.db.PeekBalance(wallet)
		if !approxF(before-after, cost) {
			return fmt.Errorf("device %d: %s moved $%.6f, want the cost $%.6f", i+1, wallet, before-after, cost)
		}
	}
	return nil
}

// provider makes a provider-linked owner row bound to a fresh device key.
func (s *eaState) provider(kind, tag string) (store.Owner, error) {
	pub, _, _ := ed25519.GenerateKey(nil)
	o := store.Owner{Pubkey: hex.EncodeToString(pub)}
	switch kind {
	case "GitHub-linked":
		o.GitHubID, o.Login = eaGID(), "gh-"+tag+"-"+s.nonce
	case "Apple-linked":
		o.AppleSub = "apple-" + tag + "-" + s.nonce
	default:
		return o, fmt.Errorf("unknown kind %q", kind)
	}
	return o, s.db.BindOwner(o)
}

// link proves addr onto the provider owner through the real approve path: a session that holds
// the address approving the owner's existing key binds the verified address to that row.
func (s *eaState) link(o store.Owner, addr string) error {
	return s.b.bindApprovedDevice(o.Pubkey, addr, 0, "", walletForEmail(addr))
}

func (s *eaState) providerHolds(kind, addr string) error {
	s.sharedAddr = s.u(addr)
	o, err := s.provider(kind, "a")
	if err != nil {
		return err
	}
	if err := s.link(o, s.sharedAddr); err != nil {
		return fmt.Errorf("the first provider account could not prove %s: %v", s.sharedAddr, err)
	}
	s.provA = o
	return nil
}

func (s *eaState) differentProves(kind, addr string) error {
	if s.u(addr) != s.sharedAddr {
		return fmt.Errorf("address mismatch")
	}
	o, err := s.provider(kind, "b")
	if err != nil {
		return err
	}
	s.provB = o
	s.linkErr = s.link(o, s.sharedAddr)
	return nil
}

func (s *eaState) secondLinkRefused() error {
	if s.linkErr == nil {
		return fmt.Errorf("the second provider account was linked to %s, want refused", s.sharedAddr)
	}
	if b, ok, _ := s.db.OwnerByPubkey(s.provB.Pubkey); ok && b.EmailVerifiedAt != 0 && strings.EqualFold(b.Email, s.sharedAddr) {
		return fmt.Errorf("the refused link still left %s verified on the second account", s.sharedAddr)
	}
	return nil
}

func (s *eaState) stillResolvesToFirst(addr, kind string) error {
	o, ok, err := s.db.OwnerByVerifiedEmail(s.u(addr))
	if err != nil || !ok {
		return fmt.Errorf("%s resolves no account (%v)", addr, err)
	}
	if o.GitHubID != s.provA.GitHubID || o.AppleSub != s.provA.AppleSub {
		return fmt.Errorf("%s resolves gh=%d apple=%q, want the first %s account", addr, o.GitHubID, o.AppleSub, kind)
	}
	return nil
}

func (s *eaState) firstDeleted() error {
	ok, err := s.db.DeleteAccount(s.provA.Login)
	if err != nil || !ok {
		return fmt.Errorf("delete %s: ok=%v err=%v", s.provA.Login, ok, err)
	}
	return nil
}

func (s *eaState) linkSucceeds() error {
	if s.linkErr != nil {
		return fmt.Errorf("the link was refused: %v", s.linkErr)
	}
	return nil
}

// --- cap notices: proven or provider-reported addresses only -----------------------------------

func (s *cnState) registerNoticeRulingSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the account changes its profile email to "([^"]+)"$`, s.changesProfileEmail)
	sc.Step(`^the account changes its profile email to its proven address in upper case$`, s.changesToUpper)
	sc.Step(`^the account proves "([^"]+)" with an emailed code$`, s.provesWithCode)
	sc.Step(`^no cap notice is sent to "([^"]+)"$`, s.noCapNoticeTo)
}

// noCapNoticeTo checks only the monthly-cap notices: the welcome email an address edit can
// trigger is a separate, unchanged message.
func (s *cnState) noCapNoticeTo(addr string) error {
	addr = s.u(addr)
	s.settled()
	for _, m := range s.sent() {
		if strings.EqualFold(m.to, addr) && strings.HasPrefix(m.subject, "Monthly spend") {
			return fmt.Errorf("a cap notice was sent to %s: %q", m.to, m.subject)
		}
	}
	return nil
}

// patchEmail drives the real PATCH /account handler for the GitHub-linked account.
func (s *cnState) patchEmail(email string) error {
	if s.acctOwner.GitHubID == 0 {
		return fmt.Errorf("the profile email edit is a GitHub-session action; this account is not GitHub-linked")
	}
	body := []byte(`{"email":` + strconv.Quote(email) + `}`)
	req := httptest.NewRequest(http.MethodPatch, "/account", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.b.accountPatch(rec, req, s.acctOwner.Login, s.acctOwner.GitHubID, s.acctWallet)
	if rec.Code != http.StatusOK {
		return fmt.Errorf("PATCH /account = %d %s", rec.Code, rec.Body.String())
	}
	return nil
}

func (s *cnState) changesProfileEmail(addr string) error { return s.patchEmail(s.u(addr)) }

func (s *cnState) changesToUpper() error { return s.patchEmail(strings.ToUpper(s.acctAddr)) }

func (s *cnState) provesWithCode(addr string) error {
	sess, err := s.signInByEmail(s.u(addr))
	if err != nil {
		return err
	}
	return s.approveKey(sess, s.acctKey)
}
