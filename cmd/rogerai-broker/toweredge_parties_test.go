package main

// toweredge_parties_test.go pins the two SELF-DEALING gaps whose failure mode is money moving
// to the wrong account, and the third-pair evidence that makes the second one measurable.
//
// GAP 1 - the self-dealing check used to FAIL OPEN. `sameAccount` answered a store error with
// `false`, false means "not the same account", which means "this is not self-dealing", which
// means PAY. So a database blip during settlement paid out a self-dealt request. The tests
// here drive that exact blip - one owner row unreadable, everything else healthy - and require
// the settlement to be REFUSED and retried rather than resolved wrongly, and then require the
// retry to pay the honest party in full, because "never pay a self-dealer" must not be bought
// with "sometimes strand an operator".
//
// GAP 2 - nothing anywhere compared the STATION OWNER against the TOWER OPERATOR, so one
// account serving through its own relay collected 70% + 10% = 80% of an arms-length consumer's
// spend, unrecorded. It is not blocked (under per-request relay selection it is often the
// CORRECT placement - see docs/relay-selection-design.md section 6.7); it is recorded.
//
// Contract: features/tower/operator_revenue_share.feature - "Self-dealing traffic is withheld
// pending review" and "Ledger or payment-store failure fails closed for share money".

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/attach"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/enroll"
	"rogerai.fm/roger/v6/internal/towercore/head"
)

var errOwnerIndexDown = errors.New("owner index unreachable")

// blindStore is a real store with a hole in it: OwnerByPubkey fails for a chosen set of keys
// and works for everything else.
//
// THE PRECISION IS THE POINT. Failing every owner lookup would prove nothing about gap 1,
// because the consumer's own wallet lookup would fail too and the old code would then bill
// nobody rather than paying a self-dealer. The bug being pinned needs the store to answer
// truthfully about the consumer and to fall over on exactly one other row - which is also what
// a real partial outage looks like from one connection: some reads land, some do not.
type blindStore struct {
	store.Store
	mu    sync.Mutex
	blind map[string]bool
}

func newBlindStore() *blindStore {
	return &blindStore{Store: store.NewMem(), blind: map[string]bool{}}
}

// blindTo makes this pubkey's owner row unreadable from now on. Guarded, because the broker's
// HTTP handler reads it from the server's goroutine while the test writes it from its own.
func (s *blindStore) blindTo(pubkeys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range pubkeys {
		s.blind[p] = true
	}
}

func (s *blindStore) heal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blind = map[string]bool{}
}

func (s *blindStore) OwnerByPubkey(pubkey string) (store.Owner, bool, error) {
	s.mu.Lock()
	dark := s.blind[pubkey]
	s.mu.Unlock()
	if dark {
		return store.Owner{}, false, errOwnerIndexDown
	}
	return s.Store.OwnerByPubkey(pubkey)
}

// towerTestBrokerOn is towerTestBroker with the wallet store supplied by the caller, so a test
// can settle against a store that is deliberately unwell. Same production route table.
func towerTestBrokerOn(t *testing.T, db store.Store) (*broker, *httptest.Server) {
	t.Helper()
	b := testBrokerWithDB(db)
	ts, err := newTowerSubsystem(b,
		admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour},
		linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore()})
	require.NoError(t, err)
	b.tower = ts
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return b, srv
}

// partiesRig is one priced edge attempt, held and ready to settle, with the three parties
// (consumer, Station owner, Tower operator) placed by the caller.
type partiesRig struct {
	b              *broker
	srv            *httptest.Server
	db             *blindStore
	tw             linkTower
	stationPriv    ed25519.PrivateKey
	stationOwner   string
	towerAcct      string
	consumerWallet string
}

// newPartiesRig stands up one attempt worth 1.0 credit: 0.70 to the Station owner, 0.10 to the
// Tower operator, 0.20 to the platform. stationLogin and towerLogin may name the SAME account
// (the gap-2 shape); consumerLogin is "" for an arms-length consumer, or an existing login to
// make the consumer that account on a second device key (the gap-1 shape).
func newPartiesRig(t *testing.T, stationLogin, towerLogin, consumerLogin string) *partiesRig {
	t.Helper()
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	db := newBlindStore()
	b, srv := towerTestBrokerOn(t, db)
	b.feeRate = 0.30

	signedInOperator(t, b, towerLogin)
	if stationLogin != towerLogin {
		signedInOperator(t, b, stationLogin)
	}
	r := &partiesRig{b: b, srv: srv, db: db}
	r.towerAcct = ownerPubkeyOf(t, b, towerLogin)
	r.stationOwner = ownerPubkeyOf(t, b, stationLogin)
	r.tw = enrolledTower(t, b, towerLogin)
	r.stationPriv = attachStation(t, b, "st-1", r.tw.id, r.stationOwner)

	cpub := issuedEdgeGrantPriced(t, b, "att-1", r.tw.id, "st-1", 0, 5_000_000_000)
	if consumerLogin == "" {
		r.consumerWallet = bindEdgeConsumer(t, b, cpub)
	} else {
		r.consumerWallet = rebindConsumerTo(t, b, cpub, consumerLogin)
	}
	_, err := b.db.AddCredits(r.consumerWallet, 100)
	require.NoError(t, err)
	held, err := b.db.HoldFor(r.consumerWallet, "att-1", 10)
	require.NoError(t, err)
	require.True(t, held)
	return r
}

// settle forwards the Station's receipt exactly as the Tower's courier does, and returns the
// HTTP status Core answered - which is the thing the courier's retry policy keys on.
func (r *partiesRig) settle(t *testing.T) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"tower_id": r.tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceiptTok(t, r.stationPriv, "att-1", "st-1", make([]byte, 300),
			dispatch.Usage{In: 0, Out: 300}, dispatch.Usage{In: 0, Out: 200}),
	})
	require.NoError(t, err)
	return r.tw.call(t, r.srv, "/tower/edge/settle", body, nil)
}

func (r *partiesRig) payable(t *testing.T, acct string) float64 {
	t.Helper()
	s, err := r.b.db.EarningSplitOf(acct, time.Now())
	require.NoError(t, err)
	return s.Payable
}

func (r *partiesRig) balance(t *testing.T) float64 {
	t.Helper()
	bal, err := r.b.db.BalanceOf(r.consumerWallet, 0)
	require.NoError(t, err)
	return bal
}

// GAP 1, THE WHOLE OF IT. The consumer IS the Station's owner on a second device key, and the
// store cannot read the Station owner's row at settle time. That is the exact input that used
// to pay: `sameAccount` returned false on the error, false meant "not self-dealing", and the
// self-dealer collected 70% of their own spend.
//
// The requirement is that the settlement decides NOTHING. Not paid, not withheld - refused,
// with the attempt left in a state a retry can still settle correctly, because withholding on
// an unverified guess strands an honest operator just as permanently as paying strands the
// fisc (a lot is minted once and nothing revisits it).
func TestAnUnreadableOwnerRowRefusesTheSettlementInsteadOfPayingTheSelfDealer(t *testing.T) {
	r := newPartiesRig(t, "station-op", "tower-op", "station-op")
	beforeBalance := r.balance(t)
	r.db.blindTo(r.stationOwner)

	code, body := r.settle(t)
	require.Equal(t, http.StatusServiceUnavailable, code, body)

	// NOTHING MOVED. No lot for either party, and the consumer's hold is still a hold - the
	// balance has not changed, so the capture did not run and the reservation was not released.
	require.Zero(t, r.payable(t, r.stationOwner), "a settlement that could not identify the payee paid nobody")
	require.Zero(t, r.payable(t, r.towerAcct), "the Tower's share is not minted on a settlement that never committed")
	require.InDelta(t, beforeBalance, r.balance(t), 1e-9, "the consumer's hold is untouched, so a retry can still capture it")
}

// ...AND 503 SPECIFICALLY, because the courier's rule is that a 4xx other than 409 is
// ErrSettlePermanent: it ABANDONS the receipt and drops it from the spool
// (internal/towerjoin/hub.go, cmd/roger-tower/hub.go). Refusing with a 4xx would turn a
// five-second database blip into an operator's pay deleted forever, which is the failure this
// change exists to prevent, wearing a different hat. The status code is load-bearing.
func TestTheRefusalIsRetryableRatherThanPermanent(t *testing.T) {
	r := newPartiesRig(t, "station-op", "tower-op", "")
	r.db.blindTo(r.stationOwner)
	code, body := r.settle(t)
	require.False(t, code >= 400 && code < 500,
		"a 4xx makes the Tower's courier abandon the receipt and the operator is never paid: got %d %s", code, body)
	require.Equal(t, http.StatusServiceUnavailable, code, body)
}

// THE OTHER HALF OF THE RULING: refusing must not strand honest money. The same attempt, once
// the store is back, settles on the courier's next pass - the Station's 70% withheld because
// it really was self-dealt, the arms-length Tower's 10% paid in full.
func TestTheRetryAfterTheStoreRecoversSettlesCorrectly(t *testing.T) {
	r := newPartiesRig(t, "station-op", "tower-op", "station-op")
	r.db.blindTo(r.stationOwner)
	code, body := r.settle(t)
	require.Equal(t, http.StatusServiceUnavailable, code, body)

	r.heal(t)
	code, body = r.settle(t)
	require.Equal(t, http.StatusOK, code, body)
	require.Zero(t, r.payable(t, r.stationOwner), "the self-dealt share is withheld once we can actually tell")
	require.InDelta(t, 0.10, r.payable(t, r.towerAcct), 1e-6,
		"the arms-length Tower is paid in full - a refusal defers money, it does not destroy it")
}

func (r *partiesRig) heal(t *testing.T) { t.Helper(); r.db.heal() }

// A STORE ERROR ON THE TOWER'S SIDE IS THE SAME RULING. Here the unreadable row is the Tower
// operator's, so the question that cannot be answered is "is this relay's owner the consumer".
// The old code answered it "no" and paid the 10%; a relay operator who could provoke that
// error would be paid for carrying their own traffic. Nothing may be paid on the guess.
func TestAnUnreadableTowerOperatorRowAlsoRefusesRatherThanPaying(t *testing.T) {
	r := newPartiesRig(t, "station-op", "tower-op", "")
	r.db.blindTo(r.towerAcct)
	code, body := r.settle(t)
	require.Equal(t, http.StatusServiceUnavailable, code, body)
	require.Zero(t, r.payable(t, r.towerAcct), "no share is paid to an operator we could not identify")
	require.Zero(t, r.payable(t, r.stationOwner), "and nothing else about the settlement committed either")
}

// THE CONTROL. An arms-length request against a healthy store is unaffected by any of this:
// both operators are paid, and the settlement answers 200.
func TestArmsLengthTrafficStillSettlesAndPaysBothShares(t *testing.T) {
	r := newPartiesRig(t, "station-op", "tower-op", "")
	code, body := r.settle(t)
	require.Equal(t, http.StatusOK, code, body)
	require.InDelta(t, 0.70, r.payable(t, r.stationOwner), 1e-6)
	require.InDelta(t, 0.10, r.payable(t, r.towerAcct), 1e-6)
}

// A VERIFIED EMAIL IS AN ACCOUNT LINKAGE, and sameAccount did not know it while accountOwnerOf
// did. Two device keys, no GitHub id, no Apple subject, different logins, one PROVED email
// address: the money path treats those as one account (accountkey.go resolves both to one
// canonical row and mints both their lots under one key), and the self-dealing check used to
// treat them as strangers. A hole with the same shape as the fail-open one, entered from the
// other side.
func TestSameAccountLinksTwoDeviceKeysByAVerifiedEmail(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	now := time.Now().Unix()
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "aa11", Login: "laptop-login", Email: "One.Person@rogerai.fm", EmailVerifiedAt: now,
	}))
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "bb22", Login: "server-login", Email: "one.person@rogerai.fm", EmailVerifiedAt: now,
	}))
	same, err := b.sameAccount("aa11", "bb22")
	require.NoError(t, err)
	require.True(t, same, "one proved address is one account, whatever the device rows are called")
}

// AN UNVERIFIED ADDRESS IS NOT. It is a string anybody may type into their profile, so treating
// it as a binding identity would let one account withhold another's earnings by claiming their
// address.
func TestSameAccountIgnoresAnUnverifiedEmail(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "aa11", Login: "laptop-login", Email: "one.person@rogerai.fm",
	}))
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: "bb22", Login: "server-login", Email: "one.person@rogerai.fm",
	}))
	same, err := b.sameAccount("aa11", "bb22")
	require.NoError(t, err)
	require.False(t, same, "an address nobody proved is not an identity")
}

// GAP 2. One account owns the Station AND runs the Tower; the consumer is a stranger who got
// what they paid for. 80% of their spend lands in one place, and the point of this test is
// that it is ALLOWED to - under per-request relay selection, your own node behind your own
// relay is frequently the lowest-latency answer - and that it is RECORDED.
func TestOneAccountOnBothSidesOfTheSplitIsPaidAndRecorded(t *testing.T) {
	r := newPartiesRig(t, "both-ends", "both-ends", "")
	require.Equal(t, r.stationOwner, r.towerAcct, "the rig is meant to place one account on both sides")

	code, body := r.settle(t)
	require.Equal(t, http.StatusOK, code, body)
	// NOT ENFORCEMENT: the full 80% is paid. A test that asserted a withholding here would be
	// pinning a policy nobody has decided, on traffic that is usually honest.
	require.InDelta(t, 0.80, r.payable(t, r.stationOwner), 1e-6,
		"self-relaying is not blocked - 70% for serving plus 10% for carrying")

	// EVIDENCE: both lots say so, and the read side can answer "how much of this account's
	// earnings came from traffic it both served and carried" without a self-join that could
	// never have recovered the linkage verdict anyway.
	rollup, err := r.b.db.SelfRelayedRollup(r.stationOwner)
	require.NoError(t, err)
	byNode := map[string]float64{}
	for _, e := range rollup {
		byNode[e.Key] = e.Amount
	}
	require.InDelta(t, 0.70, byNode["st-1"], 1e-6, "the serving lot is flagged")
	require.InDelta(t, 0.10, byNode[towerNode(r.tw.id)], 1e-6, "the relay lot is flagged")
}

// THE CONTROL FOR GAP 2: two different accounts, nothing flagged. Without this the evidence
// column could be stuck true and every assertion above would still pass.
func TestArmsLengthPartiesAreNotRecordedAsSelfRelayed(t *testing.T) {
	r := newPartiesRig(t, "station-op", "tower-op", "")
	code, body := r.settle(t)
	require.Equal(t, http.StatusOK, code, body)
	for _, acct := range []string{r.stationOwner, r.towerAcct} {
		rollup, err := r.b.db.SelfRelayedRollup(acct)
		require.NoError(t, err)
		require.Empty(t, rollup, "a Station and a Tower under different accounts are not self-relayed")
	}
}

// AND THE SELF-DEALING CASE IS STILL SELF-DEALING when the same account is also both ends: the
// consumer, the Station owner and the Tower operator are one party, which is the scenario
// features/tower/operator_revenue_share.feature:832 names. Nothing is earned, and the request
// is still recorded as self-relayed - the two facts are independent and both are true.
func TestOneAccountAsAllThreePartiesEarnsNothing(t *testing.T) {
	r := newPartiesRig(t, "both-ends", "both-ends", "both-ends")
	code, body := r.settle(t)
	require.Equal(t, http.StatusOK, code, body)
	require.Zero(t, r.payable(t, r.stationOwner), "buying from yourself through yourself is not earning")
}

// A NOTE ON THIS FILE'S CONSUMER KEYS: issuedEdgeGrantPriced mints a fresh consumer key per
// rig, so rebindConsumerTo binds a SECOND device key to an existing account rather than reusing
// the operator's own key. That is what makes these tests about ACCOUNT identity rather than
// about pubkey equality, which the first line of sameAccount would have answered on its own.
var _ = rand.Reader
