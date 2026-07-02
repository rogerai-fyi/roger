package main

// seed_failure_bdd_test.go makes features/money/seed_failure.feature EXECUTABLE, driving the
// REAL paid relay money path (b.relay / b.audioRelay, signed identities, real holds) against
// the REAL Postgres store - the repo testcontainer pattern (ROGERAI_TEST_DATABASE_URL; the
// suite skips without it, and the cover-gate always provisions it). NO MOCKS:
//   - The store failure is a REAL Postgres error: the seed relation (rogerai.seed_grants /
//     rogerai.seed_counter) is renamed offline, so the seed transaction inside BalanceOf
//     fails with undefined_table while the wallet/hold tables stay healthy - exactly the
//     "DB blip during the seed write" shape of the 2026-07-02 smell.
//   - The W4 seeded-flag accelerator runs on miniredis (real Redis protocol), like
//     cacheaccel_test.go / cross_instance_bdd_test.go.
//   - The suite runs in a PRIVATE database (<db>_brokerseed) so its global seed_counter
//     assertions can never cross-pollinate with parallel packages (the storePrivateDSN
//     convention from internal/store).
// signReq lives in auth_test.go; relayBroker in enforce_test.go; feApprox/feParseFloat in
// fee_splits_bdd_test.go; testValkeyShared in cacheaccel_test.go (same package).

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// sfGitHubIDs gives each scenario persona a stable account wallet (u_gh_<id>).
var sfGitHubIDs = map[string]int64{"alice": 7101, "bob": 7102}

type sfUser struct {
	priv   ed25519.PrivateKey
	pubHex string
	wallet string
}

type sfStation struct {
	node, model, modality string
	priceIn, priceOut     float64
	priv                  ed25519.PrivateKey
	pub                   string
}

type sfState struct {
	t     *testing.T
	sqlDB *sql.DB // raw conn on the private db: DDL fault injection + ledger assertions
	pg    *store.Postgres

	b, b2    *broker // instance A (default) and the optional peer instance B
	vs       *valkeyStore
	mr       *miniredis.Miniredis
	seed     float64
	users    map[string]*sfUser
	stations []*sfStation
	broken   map[string]bool // relations currently renamed offline

	lastCode int
	lastBody string
}

// sfPrivateDSN mirrors internal/store's storePrivateDSN: this suite asserts the GLOBAL
// seed_counter, so it gets its own database (<db>_brokerseed) that no parallel package
// can touch. Created once, tolerating "already exists" across runs.
func sfPrivateDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_brokerseed"
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("brokerseed db: open admin: %v", err)
	}
	defer admin.Close()
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if _, err := admin.Exec(`CREATE DATABASE ` + quoted); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("brokerseed db: create %s: %v", name, err)
	}
	u.Path = "/" + name
	private := u.String()
	pdb, err := sql.Open("pgx", private)
	if err != nil {
		t.Fatalf("brokerseed db: open: %v", err)
	}
	defer pdb.Close()
	if _, err := pdb.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`); err != nil {
		t.Fatalf("brokerseed db: schema: %v", err)
	}
	return private
}

// reset gives every scenario a clean slate: relations restored, every money table
// truncated, the seed counter row re-established (grantSeedTx bumps WHERE id=1), the
// seed limit back to unlimited, and fresh broker/user/station state.
func (s *sfState) reset() error {
	if err := s.restoreAll(); err != nil {
		return err
	}
	if _, err := s.sqlDB.Exec(`TRUNCATE
		rogerai.wallet, rogerai.earnings, rogerai.earning_lots, rogerai.receipts,
		rogerai.ledger, rogerai.node_owner, rogerai.nodes, rogerai.owners,
		rogerai.processed_events, rogerai.seed_counter, rogerai.seed_grants,
		rogerai.grants, rogerai.grant_usage, rogerai.payouts, rogerai.disputes, rogerai.refunds,
		rogerai.pending_holds
		RESTART IDENTITY CASCADE`); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	if _, err := s.sqlDB.Exec(`INSERT INTO rogerai.seed_counter(id,count) VALUES(1,0)
		ON CONFLICT (id) DO UPDATE SET count=0`); err != nil {
		return fmt.Errorf("reset seed_counter: %w", err)
	}
	s.pg.SetSeedLimit(0)
	s.b, s.b2, s.vs, s.mr = nil, nil, nil, nil
	s.seed = 0
	s.users = map[string]*sfUser{}
	s.stations = nil
	s.lastCode, s.lastBody = 0, ""
	return nil
}

// --- REAL fault injection: take a seed relation offline ----------------------

func (s *sfState) breakRel(rel string) error {
	if s.broken[rel] {
		return nil
	}
	if _, err := s.sqlDB.Exec(fmt.Sprintf(`ALTER TABLE rogerai.%s RENAME TO %s_offline`, rel, rel)); err != nil {
		return err
	}
	s.broken[rel] = true
	return nil
}

func (s *sfState) restoreRel(rel string) error {
	if !s.broken[rel] {
		return nil
	}
	if _, err := s.sqlDB.Exec(fmt.Sprintf(`ALTER TABLE rogerai.%s_offline RENAME TO %s`, rel, rel)); err != nil {
		return err
	}
	delete(s.broken, rel)
	return nil
}

func (s *sfState) restoreAll() error {
	for rel := range s.broken {
		if err := s.restoreRel(rel); err != nil {
			return err
		}
	}
	return nil
}

func (s *sfState) seedGrantsOffline() error  { return s.breakRel("seed_grants") }
func (s *sfState) seedCounterOffline() error { return s.breakRel("seed_counter") }
func (s *sfState) seedGrantsRestored() error { return s.restoreRel("seed_grants") }

// --- broker / station / user setup -------------------------------------------

func (s *sfState) brokerOnPostgres() error {
	s.b = relayBroker(s.pg)
	return nil
}

func (s *sfState) starterSeed(v string) error {
	f, err := feParseFloat(v)
	if err != nil {
		return err
	}
	s.seed = f
	for _, b := range []*broker{s.b, s.b2} {
		if b != nil {
			b.seedFunds = f
		}
	}
	return nil
}

// addStation registers one station on every live instance and TOFU-binds it to `owner`
// in the shared store (once - the binding is store-level and first-account-wins).
func (s *sfState) addStation(model, modality string, priceIn, priceOut float64, owner string) error {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	st := &sfStation{
		node: "n-" + model, model: model, modality: modality,
		priceIn: priceIn, priceOut: priceOut,
		priv: priv, pub: hex.EncodeToString(pub),
	}
	s.stations = append(s.stations, st)
	for _, b := range []*broker{s.b, s.b2} {
		if b != nil {
			s.registerOn(b, st)
		}
	}
	if owner == "" {
		owner = "op-" + st.node
	}
	return s.pg.BindNode(st.node, owner)
}

func (s *sfState) registerOn(b *broker, st *sfStation) {
	b.nodes[st.node] = protocol.NodeRegistration{
		NodeID: st.node, PubKey: st.pub,
		Offers: []protocol.ModelOffer{{Model: st.model, Modality: st.modality, PriceIn: st.priceIn, PriceOut: st.priceOut, Ctx: 4096}},
	}
	b.lastSeen[st.node] = time.Now()
	b.tunnels[st.node] = &nodeTunnel{jobs: make(chan protocol.Job, 4), waiters: map[string]chan protocol.JobResult{}}
}

func (s *sfState) paidChatStation(model string) error {
	return s.addStation(model, "", 1.0, 1.0, "")
}
func (s *sfState) zeroPricedStation(model string) error { return s.addStation(model, "", 0, 0, "") }
func (s *sfState) paidVoiceStation(model string) error {
	return s.addStation(model, protocol.ModalityTTS, 1.0, 0, "")
}

// ownStation registers a station TOFU-bound to the persona's OWN signing pubkey, so their
// signed requests to it resolve as self-use (pricing.free, maxCost 0 - no hold, no seed).
func (s *sfState) ownStation(name, model string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	return s.addStation(model, "", 1.0, 1.0, u.pubHex)
}

func (s *sfState) stationFor(model string) *sfStation {
	for _, st := range s.stations {
		if st.model == model {
			return st
		}
	}
	return nil
}

// firstStation returns the registered station matching the (modality, priced) shape a
// send step asks for - "priced chat", "free chat", or "priced speech".
func (s *sfState) firstStation(modality string, priced bool) (*sfStation, error) {
	for _, st := range s.stations {
		if st.modality != modality {
			continue
		}
		if priced == (st.priceIn > 0 || st.priceOut > 0) {
			return st, nil
		}
	}
	return nil, fmt.Errorf("no %s station with priced=%v is on air", modality, priced)
}

func (s *sfState) loggedIn(name string) error {
	ghID, ok := sfGitHubIDs[name]
	if !ok {
		return fmt.Errorf("unknown persona %q", name)
	}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if err := s.pg.BindOwner(store.Owner{GitHubID: ghID, Login: name, Pubkey: pubHex}); err != nil {
		return err
	}
	s.users[name] = &sfUser{priv: priv, pubHex: pubHex, wallet: fmt.Sprintf("u_gh_%d", ghID)}
	return nil
}

func (s *sfState) seedLimit(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	s.pg.SetSeedLimit(n)
	return nil
}

func (s *sfState) consumedLastSlot(name string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	_, _, err := s.pg.SeedOnce(u.wallet, s.seed)
	return err
}

// --- W4 shared seeded-flag / multi-instance -----------------------------------

func (s *sfState) withSharedFlag() error {
	if s.vs == nil {
		s.vs, s.mr = testValkeyShared(s.t)
	}
	s.b.shared = s.vs
	if s.b2 != nil {
		s.b2.shared = s.vs
	}
	return nil
}

func (s *sfState) sharedStoreDown() error {
	if s.mr == nil {
		return fmt.Errorf("no shared store to take down")
	}
	s.mr.Close()
	return nil
}

func (s *sfState) twoInstances() error {
	if err := s.withSharedFlag(); err != nil {
		return err
	}
	s.b2 = relayBroker(s.pg)
	s.b2.seedFunds = s.seed
	s.b2.shared = s.vs
	for _, st := range s.stations {
		s.registerOn(s.b2, st)
	}
	return nil
}

// --- driving requests ----------------------------------------------------------

// serveOnce arms a one-shot node-side server on the instance's tunnel (the relay_spend
// pattern): it answers the next dispatched job with a signed receipt at the offer price.
// Requests rejected before dispatch simply leave it parked.
func (s *sfState) serveOnce(b *broker, st *sfStation) {
	tun := b.tunnels[st.node]
	if tun == nil {
		return
	}
	go func() {
		job, ok := <-tun.jobs
		if !ok {
			return
		}
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: st.node, Model: st.model,
			PromptTokens: 8, CompletionTokens: 16,
			PriceIn: st.priceIn, PriceOut: st.priceOut, TS: time.Now().Unix(),
		}
		rec.SignNode(st.priv)
		body := []byte(`{"choices":[{"message":{"role":"assistant","content":"a real answer"}}]}`)
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: body, Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()
}

func (s *sfState) instance(tag string) (*broker, error) {
	switch tag {
	case "", "A":
		if s.b == nil {
			return nil, fmt.Errorf("instance A is not running")
		}
		return s.b, nil
	case "B":
		if s.b2 == nil {
			return nil, fmt.Errorf("instance B is not running")
		}
		return s.b2, nil
	}
	return nil, fmt.Errorf("unknown instance %q", tag)
}

func (s *sfState) sendChat(name, model, inst string) error {
	b, err := s.instance(inst)
	if err != nil {
		return err
	}
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	st := s.stationFor(model)
	if st == nil {
		return fmt.Errorf("no station offers %q", model)
	}
	s.serveOnce(b, st)
	body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"seed spec probe"}]}`, model))
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, u.priv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)
	s.lastCode, s.lastBody = w.Code, w.Body.String()
	return nil
}

func (s *sfState) sendPricedChat(name string) error {
	st, err := s.firstStation("", true)
	if err != nil {
		return err
	}
	return s.sendChat(name, st.model, "A")
}

func (s *sfState) sendPricedChatTo(name, inst string) error {
	st, err := s.firstStation("", true)
	if err != nil {
		return err
	}
	return s.sendChat(name, st.model, inst)
}

func (s *sfState) sendZeroPricedChat(name string) error {
	st, err := s.firstStation("", false)
	if err != nil {
		return err
	}
	return s.sendChat(name, st.model, "A")
}

// sendOwnChat sends to the persona's own station (registered via ownStation): the LAST
// registered station is theirs in the only scenario that uses this step.
func (s *sfState) sendOwnChat(name string) error {
	if len(s.stations) == 0 {
		return fmt.Errorf("no stations on air")
	}
	return s.sendChat(name, s.stations[len(s.stations)-1].model, "A")
}

func (s *sfState) sendPricedSpeech(name string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	st, err := s.firstStation(protocol.ModalityTTS, true)
	if err != nil {
		return err
	}
	body := []byte(fmt.Sprintf(`{"model":%q,"input":"seed spec voice probe"}`, st.model))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(body)))
	signReq(r, u.priv, body)
	w := httptest.NewRecorder()
	s.b.audioRelay(w, r)
	s.lastCode, s.lastBody = w.Code, w.Body.String()
	return nil
}

// --- outcome assertions ----------------------------------------------------------

func (s *sfState) servedOK() error {
	if s.lastCode != http.StatusOK {
		return fmt.Errorf("expected the request to be SERVED (200), got %d body=%s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *sfState) rejectedWith(codeStr, msg string) error {
	want, err := strconv.Atoi(codeStr)
	if err != nil {
		return err
	}
	if s.lastCode != want {
		return fmt.Errorf("expected status %d with %q, got %d body=%s", want, msg, s.lastCode, s.lastBody)
	}
	if !strings.Contains(s.lastBody, msg) {
		return fmt.Errorf("expected the %d body to carry %q, got %s", want, msg, s.lastBody)
	}
	return nil
}

// retryableRange pins the client failover contract (internal/client/failover.go
// retryable(): every status >= 500 is retried; a 4xx - the false 402 - is surfaced to
// the caller as their own fault and never retried).
func (s *sfState) retryableRange() error {
	if s.lastCode < 500 || s.lastCode > 599 {
		return fmt.Errorf("a store failure must be a retryable 5xx, got %d body=%s", s.lastCode, s.lastBody)
	}
	if s.lastCode == http.StatusPaymentRequired {
		return fmt.Errorf("a store failure must never read as the 402 billing verdict")
	}
	return nil
}

func (s *sfState) noInsufficientClaim() error {
	if strings.Contains(s.lastBody, "insufficient balance") {
		return fmt.Errorf("a server-side seed failure must never be blamed on the user's balance: %s", s.lastBody)
	}
	return nil
}

// --- ledger / seed-state assertions (raw SQL on the private db) -------------------

func (s *sfState) countQ(q string, args ...any) (int, error) {
	var n int
	err := s.sqlDB.QueryRow(q, args...).Scan(&n)
	return n, err
}

func (s *sfState) seedRowsOf(wallet string) (int, error) {
	return s.countQ(`SELECT COUNT(*) FROM rogerai.ledger WHERE holder=$1 AND idem_key='seed:'||$1`, wallet)
}

func (s *sfState) seededExactlyOnce(name string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	n, err := s.seedRowsOf(u.wallet)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%s must have EXACTLY ONE seed ledger row, found %d", name, n)
	}
	claims, err := s.countQ(`SELECT COUNT(*) FROM rogerai.seed_grants WHERE wallet=$1`, u.wallet)
	if err != nil {
		return err
	}
	if claims != 1 {
		return fmt.Errorf("%s must hold exactly one seed_grants claim, found %d", name, claims)
	}
	return nil
}

// claimNoCredit pins the cap-blocked mechanics grantSeedTx documents: the per-wallet
// seed_grants CLAIM row is inserted, but the counter gate granted no credit - so there
// is NO seed ledger row and the wallet stays at 0.
func (s *sfState) claimNoCredit(name string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	claims, err := s.countQ(`SELECT COUNT(*) FROM rogerai.seed_grants WHERE wallet=$1`, u.wallet)
	if err != nil {
		return err
	}
	rows, err := s.seedRowsOf(u.wallet)
	if err != nil {
		return err
	}
	if claims != 1 || rows != 0 {
		return fmt.Errorf("%s must hold a claim with NO credit (claims=%d, seed ledger rows=%d)", name, claims, rows)
	}
	return nil
}

func (s *sfState) neverSeeded(name string) error {
	if err := s.restoreAll(); err != nil { // assert on the healthy store: nothing leaked through the outage
		return err
	}
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	rows, err := s.seedRowsOf(u.wallet)
	if err != nil {
		return err
	}
	claims, err := s.countQ(`SELECT COUNT(*) FROM rogerai.seed_grants WHERE wallet=$1`, u.wallet)
	if err != nil {
		return err
	}
	if rows != 0 || claims != 0 {
		return fmt.Errorf("%s must not be seeded (ledger rows=%d, claims=%d)", name, rows, claims)
	}
	return nil
}

// noPartialSeedState is the atomicity pin: a failed seed tx must leave NOTHING behind -
// no wallet row (so no phantom $0 account), no ledger rows, no seed claim, no counter
// bump. Asserted after restoring the relations, proving nothing leaked through the outage.
func (s *sfState) noPartialSeedState(name string) error {
	if err := s.restoreAll(); err != nil {
		return err
	}
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	walletRows, err := s.countQ(`SELECT COUNT(*) FROM rogerai.wallet WHERE usr=$1`, u.wallet)
	if err != nil {
		return err
	}
	ledgerRows, err := s.countQ(`SELECT COUNT(*) FROM rogerai.ledger WHERE holder=$1`, u.wallet)
	if err != nil {
		return err
	}
	claims, err := s.countQ(`SELECT COUNT(*) FROM rogerai.seed_grants WHERE wallet=$1`, u.wallet)
	if err != nil {
		return err
	}
	counter, err := s.countQ(`SELECT count FROM rogerai.seed_counter WHERE id=1`)
	if err != nil {
		return err
	}
	if walletRows != 0 || ledgerRows != 0 || claims != 0 || counter != 0 {
		return fmt.Errorf("failed seed must roll back COMPLETELY for %s: wallet rows=%d ledger rows=%d claims=%d counter=%d",
			name, walletRows, ledgerRows, claims, counter)
	}
	return nil
}

func (s *sfState) seededCount(v string) error {
	want, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	got, err := s.countQ(`SELECT count FROM rogerai.seed_counter WHERE id=1`)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("seeded-wallet count = %d, expected %d", got, want)
	}
	return nil
}

func (s *sfState) walletRowAt(name, v string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	var bal float64
	if err := s.sqlDB.QueryRow(`SELECT balance FROM rogerai.wallet WHERE usr=$1`, u.wallet).Scan(&bal); err != nil {
		return fmt.Errorf("%s's wallet row must exist: %w", name, err)
	}
	return feApprox(bal, want)
}

func (s *sfState) derivedEqualsBalance(name string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	derived, err := s.pg.DeriveBalance(u.wallet)
	if err != nil {
		return err
	}
	bal, err := s.pg.PeekBalance(u.wallet)
	if err != nil {
		return err
	}
	if err := feApprox(derived, bal); err != nil {
		return fmt.Errorf("derived ledger balance must equal the wallet balance for %s: %w", name, err)
	}
	return nil
}

func (s *sfState) lifetimeSeedCredit(name, v string) error {
	u := s.users[name]
	if u == nil {
		return fmt.Errorf("%s is not logged in", name)
	}
	want, err := feParseFloat(v)
	if err != nil {
		return err
	}
	var sum float64
	if err := s.sqlDB.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM rogerai.ledger
		WHERE holder=$1 AND idem_key='seed:'||$1`, u.wallet).Scan(&sum); err != nil {
		return err
	}
	return feApprox(sum, want)
}

func (s *sfState) seedStatusIs(seededStr, limitStr, remStr string) error {
	wSeeded, _ := strconv.Atoi(seededStr)
	wLimit, _ := strconv.Atoi(limitStr)
	wRem, _ := strconv.Atoi(remStr)
	seeded, limit, rem, err := s.pg.SeedStatus()
	if err != nil {
		return err
	}
	if seeded != wSeeded || limit != wLimit || rem != wRem {
		return fmt.Errorf("seed status = (%d,%d,%d), expected (%d,%d,%d)", seeded, limit, rem, wSeeded, wLimit, wRem)
	}
	return nil
}

func TestSeedFailureBDD(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping the Postgres-backed seed-failure money spec")
	}
	private := sfPrivateDSN(t, dsn)
	pg, err := store.NewPostgres(private)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	rawDB, err := sql.Open("pgx", private)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	st := &sfState{t: t, sqlDB: rawDB, pg: pg, broken: map[string]bool{}}
	t.Cleanup(func() { _ = st.restoreAll() }) // never leave the private db broken across runs

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.reset()
			})

			// Background / setup
			sc.Step(`^a broker backed by the real Postgres money store$`, st.brokerOnPostgres)
			sc.Step(`^the starter seed grant is ([\d.]+) credits$`, st.starterSeed)
			sc.Step(`^a paid public chat station "([^"]*)" is on air$`, st.paidChatStation)
			sc.Step(`^a zero-priced public chat station "([^"]*)" is on air$`, st.zeroPricedStation)
			sc.Step(`^a paid voice station "([^"]*)" is on air$`, st.paidVoiceStation)
			sc.Step(`^(\w+) also operates (?:her|his|their) own station "([^"]*)"$`, st.ownStation)
			sc.Step(`^"([^"]*)" is logged in with a signed keypair$`, st.loggedIn)
			sc.Step(`^the seed limit is (\d+)$`, st.seedLimit)
			sc.Step(`^(\w+) has already consumed the last seed slot$`, st.consumedLastSlot)

			// Fault injection (REAL Postgres errors)
			sc.Step(`^the seed-grant relation is offline$`, st.seedGrantsOffline)
			sc.Step(`^the seed-counter relation is offline$`, st.seedCounterOffline)
			sc.Step(`^the seed-grant relation is restored$`, st.seedGrantsRestored)

			// W4 shared flag / multi-instance
			sc.Step(`^the broker runs with the shared seeded-flag accelerator$`, st.withSharedFlag)
			sc.Step(`^the shared store is down$`, st.sharedStoreDown)
			sc.Step(`^two broker instances share the Postgres store and the seeded-flag accelerator$`, st.twoInstances)

			// Requests
			sc.Step(`^(\w+) sends a priced chat request$`, st.sendPricedChat)
			sc.Step(`^(\w+) sends a priced chat request to instance ([AB])$`, st.sendPricedChatTo)
			sc.Step(`^(\w+) sends a chat request for the zero-priced model$`, st.sendZeroPricedChat)
			sc.Step(`^(\w+) sends a chat request to (?:her|his|their) own station$`, st.sendOwnChat)
			sc.Step(`^(\w+) sends a priced speech request$`, st.sendPricedSpeech)

			// Outcomes
			sc.Step(`^the request is served with status 200$`, st.servedOK)
			sc.Step(`^the request is rejected with status (\d+) and message "([^"]*)"$`, st.rejectedWith)
			sc.Step(`^the failure status is in the client-retryable 5xx range, not the 4xx billing range$`, st.retryableRange)
			sc.Step(`^the response does not claim "insufficient balance"$`, st.noInsufficientClaim)

			// Seed-state truth
			sc.Step(`^(\w+)'s wallet was seeded exactly once$`, st.seededExactlyOnce)
			sc.Step(`^(\w+)'s wallet was never seeded$`, st.neverSeeded)
			sc.Step(`^(\w+) holds a seed claim but received no seed credit$`, st.claimNoCredit)
			sc.Step(`^no partial seed state exists for (\w+)$`, st.noPartialSeedState)
			sc.Step(`^the seeded-wallet count is (\d+)$`, st.seededCount)
			sc.Step(`^(\w+)'s wallet row exists with balance ([\d.]+)$`, st.walletRowAt)
			sc.Step(`^(\w+)'s derived ledger balance equals (?:her|his|their) wallet balance$`, st.derivedEqualsBalance)
			sc.Step(`^(\w+)'s lifetime seed credit is exactly ([\d.]+)$`, st.lifetimeSeedCredit)
			sc.Step(`^the seed status is (\d+) seeded of (\d+), with (-?\d+) remaining$`, st.seedStatusIs)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/money/seed_failure.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("money/seed_failure behavior scenarios failed (see godog output above)")
	}
}
