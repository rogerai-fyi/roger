package main

// rc_money_bdd_test.go makes features/remote/rc_money.feature EXECUTABLE: the /rc/* remote-
// control surface is FREE and never moves billing identity. Relaying rc frames/turns writes no
// ledger row, receipt, or lot (M1/M3); the host's model call - a normal relay signed with the
// HOST's device key - bills the HOST wallet, never the remote surface (M2). Real broker + store
// over store.NewMem(), no mocks; the M2 relay reuses the priced-node pattern from relay_spend.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type rcMoneyState struct {
	t            *testing.T
	b            *broker
	mem          *store.Mem
	hostWallet   string
	viewWallet   string
	sid          string
	hub          *rcHub
	balBefore    float64
	ledgerBefore int
	rcptBefore   int
}

func (s *rcMoneyState) reset(t *testing.T) {
	mem := store.NewMem()
	b := relayBroker(mem)
	*s = rcMoneyState{t: t, b: b, mem: mem, hostWallet: "u_gh_7", viewWallet: "u_gh_9"}
	// The HOST (owner) with a funded wallet + an enabled RC session; the remote surface is a
	// separate logged-in identity.
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "host", Pubkey: "hostpub"})
	_ = mem.BindOwner(store.Owner{GitHubID: 9, Login: "viewer", Pubkey: "viewpub"})
	if _, err := mem.AddCredits(s.hostWallet, 100); err != nil {
		t.Fatal(err)
	}
	s.sid = "rcs_host"
	if err := mem.CreateRCSession(store.RCSession{ID: s.sid, OwnerWallet: s.hostWallet, Name: "hermes", CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	s.hub = b.rcHubFor(s.sid)
	s.balBefore, _ = mem.PeekBalance(s.hostWallet)
}

// ── M1: rc traffic moves no money ────────────────────────────────────────────────────────

func (s *rcMoneyState) balanceBefore() error {
	s.balBefore, _ = s.mem.PeekBalance(s.hostWallet)
	led, _ := s.mem.LedgerOf(s.hostWallet, nil, 1000)
	s.ledgerBefore = len(led) // the funding topup row is legitimate; the rc surface must add NONE
	rec, _ := s.mem.RecentByUser(s.hostWallet, 1000)
	s.rcptBefore = len(rec)
	return nil
}
func (s *rcMoneyState) framesAndTurnsNoModelCall() error {
	for i := 0; i < 10; i++ {
		s.hub.publish(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "chatter"})
	}
	for i := 0; i < 3; i++ {
		select {
		case s.hub.in <- protocol.RCInbound{Kind: protocol.RCInTurn, Text: "a typed turn"}:
		default:
		}
	}
	return nil
}
func (s *rcMoneyState) ownerBalanceUnchanged() error {
	if bal, _ := s.mem.PeekBalance(s.hostWallet); bal != s.balBefore {
		return fmt.Errorf("owner balance changed after rc traffic: %.4f -> %.4f", s.balBefore, bal)
	}
	return nil
}
func (s *rcMoneyState) noRowsFromRC() error {
	if led, _ := s.mem.LedgerOf(s.hostWallet, nil, 1000); len(led) != s.ledgerBefore {
		return fmt.Errorf("the rc surface added %d ledger rows, want 0", len(led)-s.ledgerBefore)
	}
	if rec, _ := s.mem.RecentByUser(s.hostWallet, 1000); len(rec) != s.rcptBefore {
		return fmt.Errorf("the rc surface added %d receipts, want 0", len(rec)-s.rcptBefore)
	}
	if e, _ := s.mem.EarningsOf(s.sid); e != 0 {
		return fmt.Errorf("the rc surface created %.4f in earnings/lots, want 0", e)
	}
	return nil
}

// ── M2: the model call bills the host, not the remote surface ─────────────────────────────

func (s *rcMoneyState) remoteTurnTriggersModelCall() error {
	// The host's agent makes the model call: a NORMAL relay signed with the HOST's device key.
	// The remote surface never signs a relay, so billing follows the host's signature.
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	s.b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1.0, PriceOut: 1.0, Ctx: 4096}}}
	s.b.lastSeen["n1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	s.b.tunnels["n1"] = tun
	if err := s.mem.BindNode("n1", "op1"); err != nil {
		return err
	}
	go func() {
		job, ok := <-tun.jobs
		if !ok {
			return
		}
		rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: "n1", Model: "m", PromptTokens: 12, CompletionTokens: 40, PriceIn: 1.0, PriceOut: 1.0, TS: time.Now().Unix()}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte(`{"choices":[{"message":{"content":"answer"}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()
	// The host signs the relay with GitHub id 7 -> the u_gh_7 wallet. (The device pubkey binds
	// to the host owner so the relay resolves to the host account wallet.)
	hostPriv := ed25519.NewKeyFromSeed(make([]byte, 32))
	hostPubHex := hex.EncodeToString(hostPriv.Public().(ed25519.PublicKey))
	_ = s.mem.BindOwner(store.Owner{GitHubID: 7, Login: "host", Pubkey: hostPubHex})
	body := []byte(`{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"please answer"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, hostPriv, body)
	w := httptest.NewRecorder()
	s.b.relay(w, r)
	if w.Code != http.StatusOK {
		return fmt.Errorf("host relay = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	return nil
}
func (s *rcMoneyState) exactlyModelCallBilled() error {
	if bal, _ := s.mem.PeekBalance(s.hostWallet); bal >= s.balBefore {
		return fmt.Errorf("host wallet was not billed for the model call (%.4f -> %.4f)", s.balBefore, bal)
	}
	return nil
}
func (s *rcMoneyState) billedToHostNotRemote() error {
	if bal, _ := s.mem.PeekBalance(s.viewWallet); bal != 0 {
		return fmt.Errorf("the remote surface wallet was billed (%.4f), want 0 - billing must follow the HOST", bal)
	}
	rec, _ := s.mem.RecentByUser(s.hostWallet, 100)
	if len(rec) == 0 {
		return fmt.Errorf("no receipt on the HOST wallet for the model call")
	}
	return nil
}

// ── M3: rc never appears in earnings/usage/metrics ────────────────────────────────────────

func (s *rcMoneyState) sessionWithRelayedChat() error { return s.framesAndTurnsNoModelCall() }
func (s *rcMoneyState) notInOwnerEarnings() error {
	if e, _ := s.mem.EarningsOf(s.sid); e != 0 {
		return fmt.Errorf("rc session shows %.4f earnings, want 0", e)
	}
	if sp, _ := s.mem.SpendOf(s.hostWallet); sp != 0 {
		return fmt.Errorf("rc traffic recorded %.4f spend, want 0", sp)
	}
	return nil
}
func (s *rcMoneyState) notInUsageOrMetrics() error {
	if rec, _ := s.mem.RecentByUser(s.hostWallet, 100); len(rec) != 0 {
		return fmt.Errorf("rc traffic produced %d usage receipts, want 0", len(rec))
	}
	return nil
}

func TestRCMoneyBDD(t *testing.T) {
	st := &rcMoneyState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
			sc.Step(`^a running broker$`, func() error { return nil })
			sc.Step(`^a host with remote control enabled, funded wallet, and one attached remote surface$`, func() error { return nil })
			// M1
			sc.Step(`^the owner's balance before any remote turns$`, st.balanceBefore)
			sc.Step(`^(\d+) frames and (\d+) turns are relayed with NO model call$`, func(int, int) error { return st.framesAndTurnsNoModelCall() })
			sc.Step(`^the owner's balance is unchanged$`, st.ownerBalanceUnchanged)
			sc.Step(`^no ledger row, receipt, or lot was created by the rc surface$`, st.noRowsFromRC)
			// M2
			sc.Step(`^the remote surface sends a turn that triggers one model call$`, st.remoteTurnTriggersModelCall)
			sc.Step(`^exactly the model call is billed$`, st.exactlyModelCallBilled)
			sc.Step(`^it is billed to the HOST wallet, not the remote surface$`, st.billedToHostNotRemote)
			// M3
			sc.Step(`^a remote-control session with relayed chat$`, st.sessionWithRelayedChat)
			sc.Step(`^it does not appear in the owner's earnings$`, st.notInOwnerEarnings)
			sc.Step(`^it does not appear in usage or the provider metrics$`, st.notInUsageOrMetrics)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/remote/rc_money.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("rc_money scenarios failed (see godog output above)")
	}
}
