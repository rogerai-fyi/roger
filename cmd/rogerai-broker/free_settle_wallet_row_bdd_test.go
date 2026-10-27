package main

// free_settle_wallet_row_bdd_test.go makes features/money/free_settle_wallet_row.feature
// EXECUTABLE. It drives the REAL relay (relay() over real stations on real tunnels, the
// upstream_failover harness) against the REAL store: the in-memory reference, or the real
// Postgres when ROGERAI_TEST_DATABASE_URL is set. No mocks; only the model server behind a
// station is an httptest stand-in, as in every sibling runner.
//
// It pins the founder ruling of 2026-10-01: a $0 settle (signed self-use, a free grant, the
// $0 lineage receipt of a voided attempt) for a wallet that has never had a row records its
// receipt and creates a ZERO-BALANCE wallet row - never a seed. The wallet's one-time starter
// seed is still granted by its first paid request, exactly once. Both stores must agree.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// fsParty is one named caller of a scenario: its signing key and the wallet relay() bills.
type fsParty struct {
	priv   ed25519.PrivateKey
	acct   string // owner pubkey hex ("" for a caller that owns nothing)
	wallet string
}

type fsState struct {
	*foState

	parties map[string]*fsParty
	grants  map[string]string // scenario grant name -> grant id
	tokens  map[string]string // scenario grant name -> bearer secret

	seedBefore   int     // seeded-wallet counter at the Background
	anonReceipts int     // receipts of the "anon" identity before the unsigned request
	anonRow      string  // the "anon" wallet row, rendered, before the unsigned request
	codes        []int   // statuses of a repeated relay
	lastCost     float64 // X-RogerAI-Cost of the last relay
}

func (s *fsState) resetFS() error {
	if err := s.foState.reset(); err != nil {
		return err
	}
	s.parties, s.grants, s.tokens = map[string]*fsParty{}, map[string]string{}, map[string]string{}
	s.codes, s.lastCost = nil, 0
	return nil
}

// --- Background --------------------------------------------------------------

func (s *fsState) brokerOverRealStore() error { return nil } // built by resetFS

func (s *fsState) starterSeed(amount float64) error {
	s.b.seedFunds = amount
	seeded, _, _, err := s.db.SeedStatus()
	s.seedBefore = seeded
	return err
}

// --- fixtures ----------------------------------------------------------------

// payerWallet is the wallet relay() bills a signed caller: walletOf() resolves the unified
// account wallet for a linked owner, else the pubkey-derived id.
func (s *fsState) payerWallet(pubHex string) string {
	if o, ok, err := s.db.OwnerByPubkey(pubHex); err == nil && ok {
		if w, ok := accountWalletForOwner(o); ok {
			return w
		}
	}
	return protocol.UserIDFromPubkey(pubHex)
}

func (s *fsState) party(name string) (*fsParty, error) {
	p, ok := s.parties[name]
	if !ok {
		return nil, fmt.Errorf("no party %q in this scenario", name)
	}
	return p, nil
}

func (s *fsState) walletOfName(name string) (string, error) {
	if name == "the grant wallet" {
		for _, id := range s.grants {
			return "g_" + id, nil
		}
		return "", fmt.Errorf("no grant in this scenario")
	}
	p, err := s.party(name)
	if err != nil {
		return "", err
	}
	return p.wallet, nil
}

func (s *fsState) ownerRunsStation(owner, name, model string, in, out float64) error {
	st := s.standUp(name, stationOpts{model: model, priceIn: in, priceOut: out})
	st.scriptReal()
	s.parties[owner] = &fsParty{priv: st.ownerPriv, acct: st.acct, wallet: s.payerWallet(st.acct)}
	return nil
}

func (s *fsState) anotherOwnerRunsStation(name, model string, in, out float64) error {
	s.standUp(name, stationOpts{model: model, priceIn: in, priceOut: out}).scriptReal()
	return nil
}

func (s *fsState) signedCallerNeverSeen(name string) error {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	s.parties[name] = &fsParty{priv: priv, wallet: protocol.UserIDFromPubkey(pubHex)}
	return s.walletNeverTouched(name)
}

func (s *fsState) issuedFreeGrant(owner, grant string) error {
	p, err := s.party(owner)
	if err != nil {
		return err
	}
	secret := "rog-grant_" + grant + "_" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	id := "grant_" + grant + "_" + s.nonce
	s.grants[grant], s.tokens[grant] = id, secret
	return s.db.CreateGrant(store.Grant{ID: id, SecretHash: hex.EncodeToString(sum[:]), Owner: p.acct, Label: grant, Free: true, CreatedAt: time.Now().Unix()})
}

func (s *fsState) stationAnswers500(name string) error {
	s.st(name).scriptStatus(500, utDefaultBody(500), nil)
	return nil
}

// walletRow reads a wallet's row: on Postgres straight from rogerai.wallet; the in-memory
// store has no row to read, so `known` there means "has a balance or any ledger row".
func (s *fsState) walletRow(wallet string) (known bool, bal, seedRemain float64, err error) {
	if s.pg != nil {
		err = s.pg.DB().QueryRow(`SELECT balance, seed_remaining FROM rogerai.wallet WHERE usr=$1`, wallet).Scan(&bal, &seedRemain)
		if err == sql.ErrNoRows {
			return false, 0, 0, nil
		}
		return err == nil, bal, seedRemain, err
	}
	bal, err = s.db.PeekBalance(wallet)
	if err != nil {
		return false, 0, 0, err
	}
	rows, err := s.db.LedgerOf(wallet, nil, 10)
	return bal != 0 || len(rows) > 0, bal, 0, err
}

func (s *fsState) walletNeverTouched(name string) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	known, bal, _, err := s.walletRow(w)
	if err != nil {
		return err
	}
	if known || bal != 0 {
		return fmt.Errorf("wallet %s already exists (balance %v) before the scenario touched it", w, bal)
	}
	es, err := s.db.RecentByUser(w, 10)
	if err != nil {
		return err
	}
	if len(es) != 0 {
		return fmt.Errorf("wallet %s already has %d receipt(s)", w, len(es))
	}
	return nil
}

func (s *fsState) opWalletNeverTouched(name string) error { return s.walletNeverTouched(name) }
func (s *fsState) grantWalletNeverTouched() error         { return s.walletNeverTouched("the grant wallet") }

// --- When: relays ------------------------------------------------------------

func (s *fsState) fire(stream bool, priv ed25519.PrivateKey, model, grant string) {
	s.model = model
	s.grantToken = s.tokens[grant]
	s.relay(stream, priv)
	s.grantToken = ""
	s.codes = append(s.codes, s.lastCode)
	s.lastCost, _ = strconv.ParseFloat(s.lastHdr.Get("X-RogerAI-Cost"), 64)
}

// ownModel is the model of the one station a party owns.
func (s *fsState) ownModel(name string) (string, error) {
	p, err := s.party(name)
	if err != nil {
		return "", err
	}
	for _, st := range s.stations {
		if st.acct == p.acct {
			return st.model, nil
		}
	}
	return "", fmt.Errorf("%q owns no station in this scenario", name)
}

func (s *fsState) relaysToOwnStation(name string) error {
	return s.relaysToOwnStationN(name, 1)
}

func (s *fsState) relaysToOwnStationN(name string, n int) error {
	p, err := s.party(name)
	if err != nil {
		return err
	}
	model, err := s.ownModel(name)
	if err != nil {
		return err
	}
	s.codes = nil
	for i := 0; i < n; i++ {
		s.fire(false, p.priv, model, "")
	}
	return nil
}

func (s *fsState) streamsFromOwnStation(name string) error {
	p, err := s.party(name)
	if err != nil {
		return err
	}
	model, err := s.ownModel(name)
	if err != nil {
		return err
	}
	for _, st := range s.stations {
		if st.acct == p.acct {
			st.scriptStream(3, false) // a real SSE upstream: three content chunks, usage, [DONE]
		}
	}
	s.codes = nil
	s.fire(true, p.priv, model, "")
	return nil
}

func (s *fsState) hasRelayedToOwnStation(name string) error {
	if err := s.relaysToOwnStation(name); err != nil {
		return err
	}
	return s.served200()
}

func (s *fsState) relaysWithGrant(grant string) error { return s.relaysWithGrantN(grant, 1) }

func (s *fsState) relaysWithGrantN(grant string, n int) error {
	if _, ok := s.tokens[grant]; !ok {
		return fmt.Errorf("no grant %q in this scenario", grant)
	}
	s.codes = nil
	for i := 0; i < n; i++ {
		s.fire(false, nil, "m", grant)
	}
	return nil
}

func (s *fsState) relaysToStationFor(name, station, model string) error {
	p, err := s.party(name)
	if err != nil {
		return err
	}
	_ = s.st(station) // the station exists; it is the only one serving `model`
	s.codes = nil
	s.fire(false, p.priv, model, "")
	return nil
}

func (s *fsState) hasRelayedToStationFor(name, station, model string) error {
	if err := s.relaysToStationFor(name, station, model); err != nil {
		return err
	}
	return s.served200()
}

func (s *fsState) seedingRead(name string) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	// The read /balance and /me perform (dashboards.go): BalanceOf with the starter seed.
	_, err = s.db.BalanceOf(w, s.b.seedFunds)
	return err
}

func (s *fsState) unsignedNoOrigin(model string) error {
	es, err := s.db.RecentByUser("anon", 5000)
	if err != nil {
		return err
	}
	s.anonReceipts = len(es)
	known, bal, seed, err := s.walletRow("anon")
	if err != nil {
		return err
	}
	s.anonRow = fmt.Sprintf("%v/%v/%v", known, bal, seed)
	s.model = model
	body := s.body(false)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.b.relay(w, r)
	s.lastCode, s.lastBody, s.lastHdr = w.Code, w.Body.Bytes(), w.Header()
	return nil
}

// --- Then --------------------------------------------------------------------

func (s *fsState) logTail() string {
	l := s.logs.String()
	if len(l) > 500 {
		l = l[len(l)-500:]
	}
	return strings.TrimSpace(l)
}

func (s *fsState) served200() error {
	if s.lastCode != 200 {
		return fmt.Errorf("relay = %d %s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *fsState) responseServedBy(code int, name string) error {
	if s.lastCode != code {
		return fmt.Errorf("status %d, want %d (%s)", s.lastCode, code, s.lastBody)
	}
	if got, want := s.lastHdr.Get("X-RogerAI-Provider"), s.st(name).id; got != want {
		return fmt.Errorf("X-RogerAI-Provider = %q, want %q: the relay was answered without its billing headers; broker log: %s", got, want, s.logTail())
	}
	return nil
}

func (s *fsState) responseIs(code int) error {
	if s.lastCode != code {
		return fmt.Errorf("status %d, want %d (%s)", s.lastCode, code, s.lastBody)
	}
	return nil
}

func (s *fsState) responseNot200() error {
	if s.lastCode == 200 {
		return fmt.Errorf("status 200, want a failure")
	}
	return nil
}

func (s *fsState) everyResponse200() error {
	for i, c := range s.codes {
		if c != 200 {
			return fmt.Errorf("relay %d of %d = %d", i+1, len(s.codes), c)
		}
	}
	if len(s.codes) == 0 {
		return fmt.Errorf("no relay was fired")
	}
	return nil
}

func (s *fsState) streamCompletes200() error {
	if s.lastCode != 200 {
		return fmt.Errorf("stream status %d (%s)", s.lastCode, s.lastBody)
	}
	if !strings.Contains(string(s.lastBody), "[DONE]") {
		return fmt.Errorf("stream did not complete: %.200s", s.lastBody)
	}
	return nil
}

func (s *fsState) carriesCostAndReceipt(cost string) error {
	if got := s.lastHdr.Get("X-RogerAI-Cost"); got != cost {
		return fmt.Errorf("X-RogerAI-Cost = %q, want %q; broker log: %s", got, cost, s.logTail())
	}
	raw := s.lastHdr.Get("X-RogerAI-Receipt")
	if raw == "" {
		return fmt.Errorf("no X-RogerAI-Receipt header; broker log: %s", s.logTail())
	}
	rec, err := protocol.DecodeReceipt(raw)
	if err != nil {
		return fmt.Errorf("X-RogerAI-Receipt does not decode: %v", err)
	}
	if !rec.VerifyBroker(hex.EncodeToString(s.b.priv.Public().(ed25519.PublicKey))) {
		return fmt.Errorf("the receipt's broker signature does not verify")
	}
	return nil
}

func (s *fsState) receiptsStored(name string, n int) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	es, err := s.db.RecentByUser(w, 5000)
	if err != nil {
		return err
	}
	if len(es) != n {
		return fmt.Errorf("%d receipt(s) stored for %s (%s), want %d; broker log: %s", len(es), name, w, n, s.logTail())
	}
	for _, e := range es {
		if e.Cost != 0 {
			return fmt.Errorf("receipt %s has cost %v, want 0", e.RequestID, e.Cost)
		}
	}
	return nil
}

func (s *fsState) oneReceiptStored(name string) error       { return s.receiptsStored(name, 1) }
func (s *fsState) oneGrantReceiptStored() error             { return s.receiptsStored("the grant wallet", 1) }
func (s *fsState) nGrantReceiptsStored(n int) error         { return s.receiptsStored("the grant wallet", n) }
func (s *fsState) nReceiptsStored(n int, name string) error { return s.receiptsStored(name, n) }

func (s *fsState) lineageListsRequest(name string) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	hdr, err := protocol.DecodeReceipt(s.lastHdr.Get("X-RogerAI-Receipt"))
	if err != nil {
		return fmt.Errorf("no receipt header to match the lineage against: %v", err)
	}
	es, err := s.db.RecentByUser(w, 5000)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID == hdr.RequestID {
			stored, _, err := s.storedReceipt(e.RequestID)
			if err != nil {
				return err
			}
			if stored.RequestID != hdr.RequestID || stored.NodeID != hdr.NodeID {
				return fmt.Errorf("stored receipt %+v does not match the header's", stored)
			}
			return nil
		}
	}
	return fmt.Errorf("request %s is not in %s's lineage (%d entries)", hdr.RequestID, name, len(es))
}

func (s *fsState) voidedReceiptStored(name string) error {
	if err := s.receiptsStored(name, 1); err != nil {
		return err
	}
	w, _ := s.walletOfName(name)
	es, _ := s.db.RecentByUser(w, 10)
	_, keys, err := s.storedReceipt(es[0].RequestID)
	if err != nil {
		return err
	}
	if vr, _ := keys["void_reason"].(string); vr == "" {
		return fmt.Errorf("the stored receipt carries no void_reason: %v", keys)
	}
	return nil
}

func (s *fsState) voidedGrantReceiptStored() error { return s.voidedReceiptStored("the grant wallet") }

func (s *fsState) receiptNamesGrant(grant string) error {
	w, _ := s.walletOfName("the grant wallet")
	es, err := s.db.RecentByUser(w, 10)
	if err != nil || len(es) == 0 {
		return fmt.Errorf("no grant receipt (err %v)", err)
	}
	rec, _, err := s.storedReceipt(es[0].RequestID)
	if err != nil {
		return err
	}
	if rec.GrantID != s.grants[grant] {
		return fmt.Errorf("receipt grant id %q, want %q", rec.GrantID, s.grants[grant])
	}
	return nil
}

func (s *fsState) balanceIs(name string, want float64) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	bal, err := s.db.PeekBalance(w)
	if err != nil {
		return err
	}
	if math.Abs(bal-want) > 1e-9 {
		return fmt.Errorf("%s's balance = %v, want %v", name, bal, want)
	}
	return nil
}

func (s *fsState) grantBalanceIs(want float64) error { return s.balanceIs("the grant wallet", want) }

func (s *fsState) balanceIsSeedLessCost(name string, seed float64) error {
	if s.lastCost <= 0 {
		return fmt.Errorf("the paid request reported cost %v (X-RogerAI-Cost %q), want > 0", s.lastCost, s.lastHdr.Get("X-RogerAI-Cost"))
	}
	return s.balanceIs(name, seed-s.lastCost)
}

func (s *fsState) seedRows(wallet string) ([]store.LedgerRow, error) {
	rows, err := s.db.LedgerOf(wallet, []string{store.KindAdjustment}, 1000)
	if err != nil {
		return nil, err
	}
	var out []store.LedgerRow
	for _, r := range rows {
		if r.IdemKey == "seed:"+wallet {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fsState) noSeedRow(name string) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	rows, err := s.seedRows(w)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("%s (%s) has %d seed ledger row(s) (amount %v); a $0 settle must not seed", name, w, len(rows), rows[0].Amount)
	}
	return nil
}

func (s *fsState) grantNoSeedRow() error { return s.noSeedRow("the grant wallet") }

func (s *fsState) oneSeedRow(name string, amount float64) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	rows, err := s.seedRows(w)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		bal, _ := s.db.PeekBalance(w)
		return fmt.Errorf("%s (%s) has %d seed ledger row(s), want exactly 1 (balance %v, last status %d %s)", name, w, len(rows), bal, s.lastCode, s.lastBody)
	}
	if math.Abs(rows[0].Amount-amount) > 1e-9 {
		return fmt.Errorf("seed row amount %v, want %v", rows[0].Amount, amount)
	}
	return nil
}

func (s *fsState) seedCounterDelta(want int) error {
	seeded, _, _, err := s.db.SeedStatus()
	if err != nil {
		return err
	}
	if got := seeded - s.seedBefore; got != want {
		return fmt.Errorf("seeded-wallet counter moved by %d, want %d", got, want)
	}
	return nil
}

func (s *fsState) seedCounterUnchanged() error { return s.seedCounterDelta(0) }
func (s *fsState) seedCounterRoseByOne() error { return s.seedCounterDelta(1) }

func (s *fsState) walletRowZero(name string) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	known, bal, seed, err := s.walletRow(w)
	if err != nil {
		return err
	}
	// The in-memory store has no row to read: there the row's existence is what the seed
	// scenarios observe, and the balance / seed-remaining facts are read here.
	if s.pg != nil && !known {
		return fmt.Errorf("no rogerai.wallet row for %s (%s) after the $0 settle; broker log: %s", name, w, s.logTail())
	}
	if bal != 0 || seed != 0 {
		return fmt.Errorf("wallet row balance %v seed_remaining %v, want 0 and 0", bal, seed)
	}
	return nil
}

func (s *fsState) derivedEqualsStored(name string) error {
	w, err := s.walletOfName(name)
	if err != nil {
		return err
	}
	derived, err := s.db.DeriveBalance(w)
	if err != nil {
		return err
	}
	bal, err := s.db.PeekBalance(w)
	if err != nil {
		return err
	}
	if math.Abs(derived-bal) > 1e-9 {
		return fmt.Errorf("derived ledger balance %v != stored balance %v", derived, bal)
	}
	return nil
}

func (s *fsState) stationEarnedNothing(name string) error {
	e, err := s.db.EarningsOf(s.st(name).id)
	if err != nil {
		return err
	}
	if e != 0 {
		return fmt.Errorf("station %s earned %v on $0 settles, want 0", name, e)
	}
	return nil
}

func (s *fsState) noLotMinted(name string) error {
	sp, err := s.db.EarningSplitOfNode(s.st(name).id, time.Now().Add(400*24*time.Hour))
	if err != nil {
		return err
	}
	if sp != (store.EarningSplit{}) {
		return fmt.Errorf("station %s has earning lots %+v, want none", name, sp)
	}
	return nil
}

func (s *fsState) grantCountedTokens(grant string) error {
	u, err := s.db.GrantUsageOf(s.grants[grant], time.Now())
	if err != nil {
		return err
	}
	if u.DayTokens <= 0 {
		return fmt.Errorf("grant %s counted %d tokens, want > 0", grant, u.DayTokens)
	}
	return nil
}

func (s *fsState) noAnonReceiptAdded() error {
	es, err := s.db.RecentByUser("anon", 5000)
	if err != nil {
		return err
	}
	if len(es) != s.anonReceipts {
		return fmt.Errorf("the anonymous identity has %d receipts, had %d before the refused request", len(es), s.anonReceipts)
	}
	return nil
}

func (s *fsState) anonWalletUnchanged() error {
	known, bal, seed, err := s.walletRow("anon")
	if err != nil {
		return err
	}
	if now := fmt.Sprintf("%v/%v/%v", known, bal, seed); now != s.anonRow {
		return fmt.Errorf("the anonymous wallet row changed: %s -> %s", s.anonRow, now)
	}
	return nil
}

func TestFreeSettleWalletRowBDD(t *testing.T) {
	st := &fsState{foState: &foState{t: t, logs: &utLog{}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardown()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.resetFS()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})
			sc.Step(`^a broker over the real store$`, st.brokerOverRealStore)
			sc.Step(`^the starter seed grant is ([0-9.]+) credits$`, st.starterSeed)

			sc.Step(`^owner "([^"]*)" runs station "([^"]*)" for "([^"]*)" priced at in ([0-9.]+) out ([0-9.]+)$`, st.ownerRunsStation)
			sc.Step(`^another owner runs station "([^"]*)" for "([^"]*)" priced at in ([0-9.]+) out ([0-9.]+)$`, st.anotherOwnerRunsStation)
			sc.Step(`^signed caller "([^"]*)" has never been seen$`, st.signedCallerNeverSeen)
			sc.Step(`^"([^"]*)" issued a free grant "([^"]*)"$`, st.issuedFreeGrant)
			sc.Step(`^"([^"]*)"'s wallet has never been touched$`, st.opWalletNeverTouched)
			sc.Step(`^the grant wallet has never been touched$`, st.grantWalletNeverTouched)
			sc.Step(`^station "([^"]*)" answers its next request with an upstream 500$`, st.stationAnswers500)
			sc.Step(`^"([^"]*)" has relayed to their own station$`, st.hasRelayedToOwnStation)
			sc.Step(`^"([^"]*)" has relayed to "([^"]*)" for "([^"]*)"$`, st.hasRelayedToStationFor)
			sc.Step(`^"([^"]*)"'s balance has been read through the seeding read$`, st.seedingRead)

			sc.Step(`^"([^"]*)" relays to their own station$`, st.relaysToOwnStation)
			sc.Step(`^"([^"]*)" relays to their own station (\d+) times$`, st.relaysToOwnStationN)
			sc.Step(`^"([^"]*)" streams from their own station$`, st.streamsFromOwnStation)
			sc.Step(`^a caller relays with grant "([^"]*)"$`, st.relaysWithGrant)
			sc.Step(`^a caller relays with grant "([^"]*)" (\d+) times$`, st.relaysWithGrantN)
			sc.Step(`^"([^"]*)" relays to "([^"]*)" for "([^"]*)"$`, st.relaysToStationFor)
			sc.Step(`^"([^"]*)"'s balance is read through the seeding read$`, st.seedingRead)
			sc.Step(`^an unsigned request with no Origin relays for "([^"]*)"$`, st.unsignedNoOrigin)

			sc.Step(`^the response is (\d+) served by "([^"]*)"$`, st.responseServedBy)
			sc.Step(`^the response is (\d+)$`, st.responseIs)
			sc.Step(`^the response is not 200$`, st.responseNot200)
			sc.Step(`^every response is 200$`, st.everyResponse200)
			sc.Step(`^the stream completes with status 200$`, st.streamCompletes200)
			sc.Step(`^the response carries X-RogerAI-Cost "([^"]*)" and an X-RogerAI-Receipt$`, st.carriesCostAndReceipt)
			sc.Step(`^one receipt is stored for "([^"]*)" with cost 0$`, st.oneReceiptStored)
			sc.Step(`^(\d+) receipts are stored for "([^"]*)", each with cost 0$`, st.nReceiptsStored)
			sc.Step(`^one receipt is stored for the grant wallet with cost 0$`, st.oneGrantReceiptStored)
			sc.Step(`^(\d+) receipts are stored for the grant wallet, each with cost 0$`, st.nGrantReceiptsStored)
			sc.Step(`^one voided receipt is stored for "([^"]*)" with cost 0$`, st.voidedReceiptStored)
			sc.Step(`^one voided receipt is stored for the grant wallet with cost 0$`, st.voidedGrantReceiptStored)
			sc.Step(`^the receipt names grant "([^"]*)"$`, st.receiptNamesGrant)
			sc.Step(`^"([^"]*)"'s lineage lists that request$`, st.lineageListsRequest)
			sc.Step(`^"([^"]*)"'s balance is ([0-9.]+)$`, st.balanceIs)
			sc.Step(`^the grant wallet's balance is ([0-9.]+)$`, st.grantBalanceIs)
			sc.Step(`^"([^"]*)"'s balance is ([0-9.]+) less the cost of that request$`, st.balanceIsSeedLessCost)
			sc.Step(`^"([^"]*)"'s wallet has no seed ledger row$`, st.noSeedRow)
			sc.Step(`^the grant wallet has no seed ledger row$`, st.grantNoSeedRow)
			sc.Step(`^"([^"]*)"'s wallet has exactly one seed ledger row of ([0-9.]+)$`, st.oneSeedRow)
			sc.Step(`^the seeded-wallet counter is unchanged$`, st.seedCounterUnchanged)
			sc.Step(`^the seeded-wallet counter rose by exactly 1$`, st.seedCounterRoseByOne)
			sc.Step(`^"([^"]*)"'s wallet row exists with balance 0 and no seed remaining$`, st.walletRowZero)
			sc.Step(`^"([^"]*)"'s derived ledger balance equals the stored balance$`, st.derivedEqualsStored)
			sc.Step(`^station "([^"]*)" has earned nothing$`, st.stationEarnedNothing)
			sc.Step(`^no earning lot was minted for station "([^"]*)"$`, st.noLotMinted)
			sc.Step(`^grant "([^"]*)" has counted the request's tokens$`, st.grantCountedTokens)
			sc.Step(`^no receipt was added for the anonymous identity$`, st.noAnonReceiptAdded)
			sc.Step(`^the anonymous identity's wallet is unchanged$`, st.anonWalletUnchanged)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/free_settle_wallet_row.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("free_settle_wallet_row.feature: non-zero status")
	}
}
