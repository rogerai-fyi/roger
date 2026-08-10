package main

// towerlink.go is the joined-Tower LINK: the session a registered Tower holds open, and the
// inventory it pushes over it. Registration proved who a Tower is; this is where it starts
// telling us what it has.
//
// AUTHENTICATION HERE IS THE TOWER, NOT AN OPERATOR. Every other /tower route resolves a
// signed-in account, because an operator is asking for something. These routes are the
// machine talking, so the caller is authenticated as a Tower: the request is signed, and the
// signing key's hash must equal the one recorded at admission. Comparing the HASH means Core
// never has to store the key, and it means an operator's account key - which can sign
// perfectly well - cannot drive a Tower's link.
//
// THE PUBLIC KEY THAT AUTHENTICATED THE REQUEST IS THE ONE THE INVENTORY IS VERIFIED WITH.
// That is deliberate and it is the whole reason no key is stored: the request signature
// proves possession, the hash comparison proves it is the admitted Tower's key, and only
// then is it handed to towerinv. A key taken from the message body instead would make
// "signed by the Tower" mean "signed by whoever wrote the message".
//
// The durable head is consulted on every session open, so a Tower reconnecting to an
// instance that has never seen it can still resume - and so a replay or a fork is visible to
// whichever instance happens to take the connection. See internal/towerhead.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"errors"

	"rogerai.fm/roger/v5/internal/stationattach"
	"rogerai.fm/roger/v5/internal/toweradmit"
	"rogerai.fm/roger/v5/internal/towerinv"
	"rogerai.fm/roger/v5/internal/towerlink"
)

// maxInventoryBody bounds what a Tower may push in one request. towerinv enforces its own
// ceiling on the decoded object; this stops an unbounded read before that.
const maxInventoryBody = 8 << 20

// towerCaller authenticates the TOWER behind a signed request and returns its admission
// record together with the public key that signed - the key the inventory will be verified
// against.
//
// claimedID is the Tower ID the message names. It is checked rather than trusted: the
// registered key hash for THAT id must match the key that actually signed, so naming another
// Tower gets you nothing.
func (b *broker) towerCaller(r *http.Request, body []byte, claimedID string) (toweradmit.Tower, ed25519.PublicKey, bool) {
	if claimedID == "" {
		return toweradmit.Tower{}, nil, false
	}
	if _, authed, ok := b.identityOf(r, body); !ok || !authed {
		return toweradmit.Tower{}, nil, false
	}
	raw, err := hex.DecodeString(r.Header.Get("X-Roger-Pubkey"))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return toweradmit.Tower{}, nil, false
	}
	ts := b.tower
	if ts == nil {
		return toweradmit.Tower{}, nil, false
	}
	tw, ok := ts.registry.Get(claimedID)
	if !ok {
		return toweradmit.Tower{}, nil, false
	}
	sum := sha256.Sum256(raw)
	// Constant time, like the enrollment path: a key-hash comparison that leaks timing is a
	// key-hash comparison an attacker can walk.
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(tw.KeyHash)) != 1 {
		return toweradmit.Tower{}, nil, false
	}
	return tw, ed25519.PublicKey(raw), true
}

// readTowerBody reads a bounded body once, so the signature check and the handler see the
// same bytes.
func readTowerBody(r *http.Request) []byte {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxInventoryBody))
	return body
}

// towerSessionOpen handles POST /tower/session: the Tower says hello and learns whether it
// must resend everything.
func (b *broker) towerSessionOpen(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)

	var hello towerlink.Hello
	if err := json.Unmarshal(body, &hello); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tw, _, ok := b.towerCaller(r, body, hello.TowerID)
	if !ok {
		jsonErr(w, http.StatusForbidden, "this link requires a registered Tower's own signed request")
		return
	}

	acc, err := ts.link.Open(hello, tw.ID)
	if err != nil {
		// A negotiation failure is the Tower's to fix and says so; it is not a server fault.
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// The DURABLE head decides whether a full snapshot is needed, not just this instance's
	// memory. Without it a Tower reconnecting to a fresh instance always resends everything.
	if ts.heads != nil {
		out, herr := ts.heads.Reconcile(tw.ID, hello.HeadRevision, hello.HeadHash)
		if herr != nil {
			log.Printf("tower %s: head store unavailable, asking for a full inventory: %v", tw.ID, herr)
		}
		acc.NeedFullInventory = out.NeedsFullInventory()
		if out.Suspicious() {
			// Evidence, not a penalty. One fork is a bug; a pattern of them is an operator
			// worth removing, and that is a separate approved decision made on accumulated
			// record. Logged rather than counted for now: the admission registry's
			// FalseClaims counter means something specific (a Tower asserting a state it does
			// not hold), and overloading it with chain anomalies would corrupt the one signal
			// enforcement already reads.
			log.Printf("tower %s: inventory chain %s (claimed rev=%d, hash=%.12s) - demanding a full snapshot",
				tw.ID, out, hello.HeadRevision, hello.HeadHash)
		}
	}
	writeJSON(w, http.StatusOK, acc)
}

// towerHeartbeat handles POST /tower/session/heartbeat. The frame is the liveness signal;
// nothing about it reaches the database.
func (b *broker) towerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)

	var f towerlink.Frame
	if err := json.Unmarshal(body, &f); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tw, _, ok := b.towerCaller(r, body, f.TowerID)
	if !ok {
		jsonErr(w, http.StatusForbidden, "this link requires a registered Tower's own signed request")
		return
	}
	if err := ts.link.Heartbeat(f.SessionID, tw.ID); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// towerSessionClose handles POST /tower/session/close - a drain, so the fleet behind this
// Tower leaves routing at once rather than aging out over the freshness window.
func (b *broker) towerSessionClose(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)

	var f towerlink.Frame
	if err := json.Unmarshal(body, &f); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tw, _, ok := b.towerCaller(r, body, f.TowerID)
	if !ok {
		jsonErr(w, http.StatusForbidden, "this link requires a registered Tower's own signed request")
		return
	}
	ts.link.Close(f.SessionID, tw.ID)
	// An orderly drain drops the inventory immediately. The expiry would get there on its
	// own, but leaving leaves routable after the operator SAID they were going is the
	// "immortal inventory" failure the design calls out by name.
	ts.inv.Forget(tw.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "drained": true})
}

// towerInventory handles POST /tower/inventory (a full signed revision) and
// POST /tower/inventory/delta (a hash-chained amendment).
//
// The Tower ID comes from the AUTHENTICATED caller, never from the object, and the object's
// own tower_id is checked against it inside towerinv. Two independent places agreeing is the
// point: one of them being wrong should not be enough.
func (b *broker) towerInventory(delta bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, http.MethodPost) {
			return
		}
		ts := b.towerAvailable(w)
		if ts == nil {
			return
		}
		body := readTowerBody(r)

		// The signed object carries its own tower_id; read it only to name the caller we are
		// about to authenticate, and let towerinv do the authoritative comparison.
		var envelope struct {
			TowerID string `json:"tower_id"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		tw, key, ok := b.towerCaller(r, body, envelope.TowerID)
		if !ok {
			jsonErr(w, http.StatusForbidden, "this link requires a registered Tower's own signed request")
			return
		}
		if !ts.link.Live(tw.ID) {
			// Inventory outside a session has no lifetime and nothing to expire against.
			jsonErr(w, http.StatusConflict, "open a session before pushing inventory")
			return
		}

		var res towerinv.Result
		var err error
		if delta {
			res, err = ts.inv.AcceptDelta(tw.ID, key, body)
		} else {
			res, err = ts.inv.AcceptFull(tw.ID, key, body)
		}
		switch {
		case err == nil:
			// Nothing else may happen between accepting and recording: an accepted revision
			// whose head was not written would look like a fork on the next reconnect.
			if ts.heads != nil {
				if _, herr := ts.heads.Accept(tw.ID, res.Revision, res.Hash); herr != nil {
					log.Printf("tower %s: accepted revision %d but could not record the head: %v",
						tw.ID, res.Revision, herr)
				}
			}
			ts.link.RecordHead(tw.ID, res.Revision, res.Hash)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "revision": res.Revision, "hash": res.Hash,
				"routable": res.Routable, "excluded": excludedView(res.Excluded),
			})
		case errorIsResync(err):
			// 409 with an explicit instruction: the Tower cannot be left guessing whether to
			// retry the delta or start again.
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok": false, "need_full_inventory": true, "error": err.Error(),
			})
		default:
			jsonErr(w, http.StatusBadRequest, err.Error())
		}
	}
}

// errorIsResync reports whether towerinv is asking for a full snapshot rather than refusing
// the push. The two are answered with different status codes because they need different
// things from the Tower, and a Tower that cannot tell them apart will retry the wrong one.
func errorIsResync(err error) bool { return errors.Is(err, towerinv.ErrResync) }

// excludedView reports WHY each leaf was dropped, so an operator can see which of their
// Stations is not earning without Core having to accept it in order to tell them.
func excludedView(ex []towerinv.Exclusion) []map[string]string {
	out := make([]map[string]string, 0, len(ex))
	for _, e := range ex {
		out = append(out, map[string]string{
			"station_id": e.StationID, "offer_id": e.OfferID, "reason": e.Reason,
		})
	}
	return out
}

// --- Station invitations ----------------------------------------------------

// towerStationInvite handles POST /tower/station/invite: the operator saying "this Station,
// with these two keys, may attach to my Tower".
//
// IT IS THE ONLY THING THAT CREATES ATTACHMENT AUTHORITY, exactly as /tower/token is the
// only thing that creates admission authority. Without it the Station registry can only ever
// be empty, and towerinv refuses every leaf with "not consistent with any registered key" -
// which is precisely the state this whole subsystem was in before this route existed.
//
// The invitation is bound to the OPERATOR'S OWN Tower. An operator who names somebody else's
// Tower is refused: attaching a Station is capacity and earnings, and neither belongs to a
// person who merely knows a Tower ID.
//
// The plaintext secret is returned ONCE and never stored - only its verifier is. A database
// read therefore cannot hand anybody an attachment they were not given.
func (b *broker) towerStationInvite(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body := readTowerBody(r)

	owner, ok := b.towerOperator(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "inviting a Station requires a signed-in account - run `roger-tower login`")
		return
	}
	// TWO DIFFERENT NOTIONS OF "OWNER", and conflating them is a real bug this route hit.
	//
	// Tower ownership is recorded by LOGIN (toweradmit), but an attachment records the
	// account PUBKEY, because that is what towerpolicy resolves when it asks whether the
	// owner is present and in good standing. Storing the login here produced an attachment
	// that verified perfectly and was then refused for "no owner, which public admission
	// requires" - a leaf rejected for a reason that had nothing to do with the leaf.
	//
	// The pubkey is taken from the request that was already authenticated, not from the
	// body: it is the key that signed, which is the key bound to this account.
	ownerPubkey := r.Header.Get("X-Roger-Pubkey")
	if _, found, oerr := b.db.OwnerByPubkey(ownerPubkey); oerr != nil || !found {
		jsonErr(w, http.StatusUnauthorized, "inviting a Station requires a signed-in account - run `roger-tower login`")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}

	var req struct {
		TowerID      string `json:"tower_id"`
		StationID    string `json:"station_id"`
		AssertionKey string `json:"assertion_key"`
		SessionKey   string `json:"session_key"`
		Role         string `json:"role"`
		CeilingHash  string `json:"capability_ceiling_hash"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// The Tower must be one THIS operator holds. Checked against the registry rather than
	// taken from the request: a Tower ID is not a secret and knowing one proves nothing.
	tw, found := ts.registry.Get(req.TowerID)
	if !found || tw.Owner != owner {
		// Indistinguishable from "no such Tower" on purpose, so this cannot be used to
		// enumerate other people's Towers.
		jsonErr(w, http.StatusNotFound, "no such Tower on this account")
		return
	}

	// Both keys are checked for SHAPE here, before anything is stored. towerinv will later
	// verify signatures against the assertion key, and a key that is not a key would produce
	// a confusing refusal at that distance from the mistake.
	for name, k := range map[string]string{"assertion_key": req.AssertionKey, "session_key": req.SessionKey} {
		raw, derr := hex.DecodeString(k)
		if derr != nil || len(raw) != ed25519.PublicKeySize {
			jsonErr(w, http.StatusBadRequest, name+" must be a hex-encoded Ed25519 public key")
			return
		}
	}

	stationID := strings.TrimSpace(req.StationID)
	if stationID == "" {
		stationID = newStationID()
	}
	// A Station ID that is already attached cannot be re-invited: the attachment path would
	// refuse it anyway, and saying so here costs the operator nothing to fix.
	if _, exists, serr := ts.stations.Station(stationID); serr != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not check the Station registry - try again in a moment")
		return
	} else if exists {
		jsonErr(w, http.StatusConflict, "that Station is already attached - revoke it before attaching a new identity")
		return
	}

	auth, secret, err := stationattach.NewInvite(stationattach.Authorization{
		ID: newInviteID(), Network: towerlink.PublicNetwork, StationID: stationID, Owner: ownerPubkey,
		Origin:       stationattach.Origin{Kind: stationattach.OriginJoined, TowerID: tw.ID},
		AssertionKey: req.AssertionKey, SessionKey: req.SessionKey,
		Role: req.Role, CeilingHash: req.CeilingHash,
	}, stationInviteTTL, time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ts.stationStore.PutAuthorization(auth); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not record the invitation - try again in a moment")
		return
	}

	log.Printf("station invite %s issued for Station %s on Tower %s by %s",
		auth.ID, stationID, tw.ID, owner)
	writeJSON(w, http.StatusOK, map[string]any{
		"invitation_id": auth.ID,
		"station_id":    stationID,
		"tower_id":      tw.ID,
		// Shown ONCE. It is not stored and cannot be shown again; a lost invitation is
		// re-issued, never recovered.
		"secret":     secret,
		"expires_in": int(stationInviteTTL.Seconds()),
	})
}

// stationInviteTTL bounds how long an invitation may sit unredeemed. Long enough for an
// operator to paste it into a machine they are standing at; short enough that a leaked one
// stops mattering quickly.
const stationInviteTTL = time.Hour

func newStationID() string { return "st-" + randomHex(12) }
func newInviteID() string  { return "sinv-" + randomHex(12) }

func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failing is not something to paper over with a predictable id: an id an
		// attacker can guess is an invitation they can try to redeem.
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw)
}
