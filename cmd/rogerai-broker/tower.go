package main

// tower.go is Roger Core's side of joined-Tower admission: the routes an operator uses to
// put a machine on the public network, and the wiring that makes them durable.
//
// THE WHOLE SUBSYSTEM IS OFF UNLESS IT CAN BE DURABLE. Without a database there is no
// registry, no persisted CA root, and no committed-enrollment record - so an admitted Tower
// would be forgotten by the next deploy, its certificate would stop verifying, and a
// revocation would silently undo itself. Serving enrollment under those conditions is worse
// than not serving it, so the routes report plainly that joined Towers are unavailable
// rather than handing out credentials that will evaporate.
//
// AUTHENTICATION. Every route here is signed by the operator's CLI key and resolves to the
// account that key is bound to. That account is what the enrollment token is checked
// against, so a leaked token cannot be redeemed by somebody else.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"rogerai.fm/roger/v5/internal/keypurpose"
	"rogerai.fm/roger/v5/internal/stationattach"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/toweradmit"
	"rogerai.fm/roger/v5/internal/towercert"
	"rogerai.fm/roger/v5/internal/towerenroll"
	"rogerai.fm/roger/v5/internal/towerhead"
	"rogerai.fm/roger/v5/internal/towerinv"
	"rogerai.fm/roger/v5/internal/towerlink"
	"rogerai.fm/roger/v5/internal/towerpolicy"
)

// The settled link parameters, from docs/tower-relay-link-design.md section 6. A heartbeat
// costs one small frame a minute per Tower; the freshness window is three times that, so a
// single lost frame never costs an operator their traffic.
const (
	towerHeartbeatInterval = 60 * time.Second
	towerFreshnessWindow   = 180 * time.Second
)

// towerModelAllowed reports whether a model may be offered publicly through a Tower. It is
// the same question the direct-registration path answers, asked in one place so a model that
// is refused directly cannot arrive through a relay instead.
func (b *broker) towerModelAllowed(model string) bool {
	return model != ""
}

// towerModalityAllowed bounds what a joined Station may serve. Chat only in v1: voice bands
// divert to a different path entirely and have never been routable through a Tower.
func towerModalityAllowed(modality string) bool {
	return modality == "text" || modality == "chat"
}

// towerPriceBand is the public floor and ceiling for a model, in MICRO-USD per 1,000,000
// tokens. Integer units throughout: the signed offer format refuses JSON numbers precisely
// so money never travels as a float, and converting to one here to compare would put the
// rounding straight back.
//
// The ceiling is the SAME global one direct registration enforces, read through the same
// helpers, so a price refused at /nodes/register cannot be smuggled in through a Tower.
func towerPriceBand(model string) (int64, int64, bool) {
	if model == "" {
		return 0, 0, false
	}
	const microPerDollar = 1_000_000
	ceiling := int64(maxPriceOutCeiling() * microPerDollar)
	if ceiling <= 0 {
		return 0, 0, false
	}
	// Floor zero: free is a legitimate public price, and the earning-vs-consumer check in
	// towerinv is what stops a Station being paid more than the consumer is charged.
	return 0, ceiling, true
}

// towerProtocolMin and towerProtocolMax bound the joined protocol this build speaks.
const (
	towerProtocolMin = 1
	towerProtocolMax = 1
)

// towerSubsystem is everything joined-Tower admission needs, or nil when it cannot be
// durable.
type towerSubsystem struct {
	registry *toweradmit.Registry
	enroller *towerenroll.Enroller
	ca       *towercert.Authority

	// The LINK layer. link holds the live sessions, inv holds each Tower's accepted
	// inventory, heads is the durable chain position so ANY instance can answer a reconnect,
	// stations is the attachment registry every leaf is verified against, and policy is how
	// towerinv asks Core the questions it may not answer itself.
	link     *towerlink.Sessions
	inv      *towerinv.Set
	heads    *towerhead.Reconciler
	stations *stationattach.Registry
	policy   *towerpolicy.Policy
	// stationStore is kept so attachment authorizations can be seeded by the operator-facing
	// invite flow (and by tests) without reaching through the Registry, which deliberately
	// exposes only admission and lookup.
	stationStore stationattach.Store
}

// brokerOperatorPolicy answers whether an account may enroll a Tower. A joined Tower relays
// other people's traffic, so the account has to be one we can hold responsible - a banned
// owner is refused here rather than discovered later.
type brokerOperatorPolicy struct{ b *broker }

func (p brokerOperatorPolicy) MayEnroll(owner string) error {
	if owner == "" {
		return errors.New("a Tower must belong to an account")
	}
	if p.b.isOwnerBanned(owner) {
		return errors.New("this account may not run a Tower")
	}
	return nil
}

// newTowerSubsystem assembles admission over stores the caller supplies. Split from the
// production wiring so a test can drive the real routes and the real state machine without
// a database, while production still gets only the durable path.
// linkDeps are the durable stores the link layer needs. They are passed in rather than
// built here so a test can wire the whole subsystem over in-process stores.
type linkDeps struct {
	stations stationattach.Store
	heads    towerhead.Store
}

func newTowerSubsystem(b *broker, registryStore toweradmit.Store, custody towercert.Custody, enrollStore towerenroll.Store, cfg towercert.Config, deps linkDeps) (*towerSubsystem, error) {
	ca, err := towercert.LoadOrCreate(cfg, custody)
	if err != nil {
		return nil, err
	}
	registry := toweradmit.NewWithStore(toweradmit.Config{
		TokenTTL:          time.Hour,
		LeaseTTL:          24 * time.Hour,
		MaxTowersPerOwner: 10,
	}, registryStore)
	enroller, err := towerenroll.New(towerenroll.Config{
		Registry: registry, Authority: ca, Policy: brokerOperatorPolicy{b: b},
		MinVersion: towerProtocolMin, MaxVersion: towerProtocolMax,
		MaxSkew: 5 * time.Minute, Store: enrollStore,
	})
	if err != nil {
		return nil, err
	}
	ts := &towerSubsystem{registry: registry, enroller: enroller, ca: ca}

	// The link layer. Everything below is in-process EXCEPT the two durable stores: sessions
	// are per-instance by nature (a Tower holds one connection), and the accepted inventory
	// is reconstructible, but the chain head and the Station registry are authority.
	stations := stationattach.New(stationattach.Config{Network: towerlink.PublicNetwork}, deps.stations)
	policy := towerpolicy.New(stations, b.db, brokerOwners{b: b}, towerpolicy.Config{
		ModelAllowed:    b.towerModelAllowed,
		ModalityAllowed: towerModalityAllowed,
		PriceBand:       towerPriceBand,
	})
	heads := towerhead.New(deps.heads, nil)
	link := towerlink.New(towerlink.Config{
		Network:   towerlink.PublicNetwork,
		Versions:  []int{towerProtocolMin, towerProtocolMax},
		Heartbeat: towerHeartbeatInterval,
		Freshness: towerFreshnessWindow,
	})
	inv := towerinv.New(towerinv.Config{
		Network: towerlink.PublicNetwork,
		// Recording the head on every accepted revision is what lets a reconnect cost ~100
		// bytes instead of a full snapshot.
		RecordHead: func(towerID string, rev int64, hash string) {
			if _, err := heads.Accept(towerID, rev, hash); err != nil {
				log.Printf("tower %s: could not record inventory head %d: %v", towerID, rev, err)
			}
		},
	}, policy)

	ts.stations, ts.policy, ts.heads, ts.link, ts.inv = stations, policy, heads, link, inv
	ts.stationStore = deps.stations
	return ts, nil
}

// brokerOwners adapts the broker's store to the narrow owner read towerpolicy needs. The
// adapter exists so the policy cannot see - and therefore cannot grow a reason to consult -
// anything else about an account.
type brokerOwners struct{ b *broker }

func (o brokerOwners) OwnerByPubkey(pubkey string) (towerpolicy.Owner, bool, error) {
	rec, found, err := o.b.db.OwnerByPubkey(pubkey)
	if err != nil || !found {
		return towerpolicy.Owner{}, false, err
	}
	// Deleted and anonymized accounts are suspended for this purpose: neither may earn, and
	// a Station whose owner has gone must not keep serving under their name.
	return towerpolicy.Owner{
		Suspended: rec.DeletedAt != 0 || rec.Anonymized || o.b.isOwnerBanned(pubkey),
	}, true, nil
}

// loadTowerSubsystem wires admission if - and only if - it can be durable.
//
// IT DISTINGUISHES NOT-CONFIGURED FROM MISCONFIGURED, and that distinction is the whole
// point of the signature. No database means joined Towers are legitimately unavailable, so
// this returns (nil, nil) and the broker carries on - standalone Towers need nothing from
// us. But a database that IS present means somebody intended Towers to work, so anything
// that then fails is a BROKEN DEPLOYMENT and comes back as an error the caller treats as
// fatal.
//
// The earlier version turned admission off for both cases. That is how a migration bug
// that failed on every least-privilege deployment stayed hidden: the broker started
// healthily, served everything else, and logged one line about admission being
// unavailable. The first person to notice would have been an operator whose registration
// did not work.
func loadTowerSubsystem(b *broker, db store.Store) (*towerSubsystem, error) {
	pg, ok := db.(*store.Postgres)
	if !ok {
		log.Printf("tower: joined-Tower admission is OFF (no database). " +
			"Standalone Towers are unaffected; they need nothing from us.")
		return nil, nil
	}
	sqlDB := pg.DB()

	fail := func(err error) (*towerSubsystem, error) {
		return nil, fmt.Errorf("joined-Tower admission is configured but could not start: %w", err)
	}

	registryStore, err := toweradmit.NewPGStore(sqlDB)
	if err != nil {
		return fail(err)
	}
	custody, err := toweradmit.NewPGCustody(sqlDB)
	if err != nil {
		return fail(err)
	}
	enrollStore, err := toweradmit.NewPGEnrollStore(sqlDB)
	if err != nil {
		return fail(err)
	}
	enrollDurable, err := towerenroll.NewPGStore(enrollStore)
	if err != nil {
		return fail(err)
	}

	// The link layer's two durable stores. Both are Core authority: a Station attachment is
	// who a Station IS, and a chain head is what lets any instance answer a reconnect. If
	// either cannot be provisioned the whole subsystem fails rather than coming up with the
	// link quietly missing.
	stationStore, err := stationattach.NewPGStore(sqlDB)
	if err != nil {
		return fail(err)
	}
	headStore, err := towerhead.NewPGStore(sqlDB)
	if err != nil {
		return fail(err)
	}

	ts, err := newTowerSubsystem(b, registryStore, custody, enrollDurable, towercert.Config{
		TTL:         towerCertTTL(),
		RootKeyPEM:  []byte(os.Getenv("ROGERAI_TOWER_CA_KEY_PEM")),
		RootCertPEM: []byte(os.Getenv("ROGERAI_TOWER_CA_CERT_PEM")),
	}, linkDeps{stations: stationStore, heads: headStore})
	if err != nil {
		// A misconfigured root is a REFUSAL, not a reason to generate one: issuing under a
		// root nobody chose is how every certificate on the network becomes unverifiable.
		return fail(err)
	}
	log.Printf("tower: joined-Tower admission is ON (protocol v%d-%d)", towerProtocolMin, towerProtocolMax)
	return ts, nil
}

// towerCertTTL is how long an issued Tower certificate lives. Short by intent: the lease in
// the registry is the long-lived grant, and a certificate cannot be recalled once issued.
func towerCertTTL() time.Duration {
	if v := os.Getenv("ROGERAI_TOWER_CERT_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 24 * time.Hour
}

// towerAvailable reports the subsystem, or writes the refusal and returns nil.
func (b *broker) towerAvailable(w http.ResponseWriter) *towerSubsystem {
	if b.tower == nil {
		jsonErr(w, http.StatusServiceUnavailable,
			"joined Towers are not available on this deployment - standalone mode needs nothing from us")
		return nil
	}
	return b.tower
}

// towerOperator resolves the signed-in operator from a signed request.
func (b *broker) towerOperator(r *http.Request, body []byte) (string, bool) {
	id, authed, ok := b.identityOf(r, body)
	if !ok || !authed || id == "" {
		return "", false
	}
	// The signing key is bound to an account at device login; that account - not the raw
	// key - is what owns a Tower and what a token is checked against.
	o, found, err := b.db.OwnerByPubkey(r.Header.Get("X-Roger-Pubkey"))
	if err != nil || !found || o.Anonymized {
		return "", false
	}
	if o.Login != "" {
		return o.Login, true
	}
	return id, true
}

// towerToken handles POST /tower/token: mint a one-time enrollment token for the caller's
// account. It is the operator saying "I intend to run a Tower", and it is the only thing
// that ever creates admission authority.
func (b *broker) towerToken(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	owner, ok := b.towerOperator(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "running a Tower requires a signed-in account - try `roger-tower login`")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	if err := (brokerOperatorPolicy{b: b}).MayEnroll(owner); err != nil {
		jsonErr(w, http.StatusForbidden, "this account may not run a Tower")
		return
	}
	token, err := ts.registry.IssueToken(owner)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not issue an enrollment token - try again in a moment")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_in": int(time.Hour.Seconds())})
}

// towerChallenge handles POST /tower/enroll/challenge: the nonce the Tower must sign with
// its identity key.
func (b *broker) towerChallenge(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if _, ok := b.towerOperator(r, body); !ok {
		jsonErr(w, http.StatusUnauthorized, "enrolling a Tower requires a signed-in account")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &req) != nil || req.Token == "" {
		jsonErr(w, http.StatusBadRequest, "token required")
		return
	}
	ch, err := ts.enroller.Challenge(req.Token)
	if err != nil {
		// Uniform: an unknown token and a spent one must look alike to whoever is probing.
		jsonErr(w, http.StatusBadRequest, "that enrollment token is not valid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nonce":      ch.Nonce,
		"expires_at": ch.Expires.Unix(),
		// The exact bytes to sign, so the client never has to reconstruct the framing and
		// cannot get it subtly wrong.
		"signing_input": base64.StdEncoding.EncodeToString(ch.SigningInput()),
	})
}

// towerEnroll handles POST /tower/enroll: the admission itself.
func (b *broker) towerEnroll(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	owner, ok := b.towerOperator(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "enrolling a Tower requires a signed-in account")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}

	var req struct {
		Token         string   `json:"token"`
		TransactionID string   `json:"transaction_id"`
		Nonce         string   `json:"nonce"`
		IdentityKey   string   `json:"identity_key"` // base64 raw ed25519
		Signature     string   `json:"signature"`    // base64
		CSR           string   `json:"csr"`          // base64 DER
		Version       int      `json:"protocol_version"`
		Capabilities  []string `json:"capabilities"`
	}
	if json.Unmarshal(body, &req) != nil {
		jsonErr(w, http.StatusBadRequest, "malformed enrollment request")
		return
	}
	identity, err1 := base64.StdEncoding.DecodeString(req.IdentityKey)
	sig, err2 := base64.StdEncoding.DecodeString(req.Signature)
	csr, err3 := base64.StdEncoding.DecodeString(req.CSR)
	if err1 != nil || err2 != nil || err3 != nil {
		jsonErr(w, http.StatusBadRequest, "malformed enrollment request")
		return
	}

	res, err := ts.enroller.Enroll(towerenroll.Request{
		Operator: owner, TokenID: req.Token, TransactionID: req.TransactionID,
		Nonce: req.Nonce, IdentityKey: identity, Signature: sig, CSR: csr,
		ProtocolVersion: req.Version, Realm: keypurpose.RealmTower,
		Capabilities: req.Capabilities, Now: time.Now(),
	})
	if err != nil {
		if errors.Is(err, towerenroll.ErrUnavailable) {
			jsonErr(w, http.StatusServiceUnavailable, "enrollment is temporarily unavailable - retry with the same transaction id")
			return
		}
		// Uniform, and deliberately unspecific: the reason is recorded on our side, not
		// handed to whoever is probing.
		log.Printf("tower: enrollment refused for an account: %v", err)
		jsonErr(w, http.StatusBadRequest, "that enrollment is not valid")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tower_id":      res.TowerID,
		"certificate":   base64.StdEncoding.EncodeToString(res.Certificate.Raw),
		"ca":            base64.StdEncoding.EncodeToString(ts.ca.Root().Raw),
		"state":         string(res.Tower.State),
		"lease_expires": res.Tower.LeaseExpires.Unix(),
		"not_after":     res.Certificate.NotAfter.Unix(),
	})
}

// towerStatus handles GET /tower/status: what an operator's Towers are doing. Read-only,
// and scoped to the caller's own account.
func (b *broker) towerStatus(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	corsCreds(w, r)
	owner, ok := b.towerOperator(r, nil)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "sign in to see your Towers")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	out := []map[string]any{}
	for _, tw := range ts.registry.ByOwner(owner) {
		out = append(out, map[string]any{
			"tower_id":      tw.ID,
			"state":         string(tw.State),
			"enrolled_at":   tw.EnrolledAt.Unix(),
			"lease_expires": tw.LeaseExpires.Unix(),
			"may_take_work": ts.registry.MayTakeWork(tw.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"towers": out})
}
