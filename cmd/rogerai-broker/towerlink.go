package main

// link.go is the joined-Tower LINK: the session a registered Tower holds open, and the
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
// then is it handed to inv. A key taken from the message body instead would make
// "signed by the Tower" mean "signed by whoever wrote the message".
//
// The durable head is consulted on every session open, so a Tower reconnecting to an
// instance that has never seen it can still resume - and so a replay or a fork is visible to
// whichever instance happens to take the connection. See internal/head.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"errors"

	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/inv"
	"rogerai.fm/roger/v5/internal/towercore/link"
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
func (b *broker) towerCaller(r *http.Request, body []byte, claimedID string) (admit.Tower, ed25519.PublicKey, bool) {
	if claimedID == "" {
		return admit.Tower{}, nil, false
	}
	if _, authed, ok := b.identityOf(r, body); !ok || !authed {
		return admit.Tower{}, nil, false
	}
	raw, err := hex.DecodeString(r.Header.Get("X-Roger-Pubkey"))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return admit.Tower{}, nil, false
	}
	ts := b.tower
	if ts == nil {
		return admit.Tower{}, nil, false
	}
	tw, ok := ts.registry.Get(claimedID)
	if !ok {
		return admit.Tower{}, nil, false
	}
	sum := sha256.Sum256(raw)
	// Constant time, like the enrollment path: a key-hash comparison that leaks timing is a
	// key-hash comparison an attacker can walk.
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(tw.KeyHash)) != 1 {
		return admit.Tower{}, nil, false
	}

	// IDENTITY IS NOT ENTITLEMENT, and two audit passes missed this. Proving you are the
	// Tower Core admitted says nothing about whether Core still wants you: a suspended,
	// revoked or lease-expired Tower holds a perfectly valid key, and without this check
	// could open sessions, push inventory, and redeem Station invitations.
	//
	// NOT MayTakeWork, though that is the obvious reach. MayTakeWork asks whether a Tower may
	// take WORK, and a quarantined Tower may not - EligibleFor(quarantine) is "probes or
	// bounded beta only". But quarantine is exactly the state a Tower is admitted INTO, and
	// the whole point of it is that the Tower connects, stays connected and is visible while
	// Core gathers evidence. Gating the link on MayTakeWork would lock every newly admitted
	// Tower out of the network it was just admitted to.
	//
	// The link asks a different question: may this Tower be HERE at all? That is any
	// non-terminal state with a live lease - and DRAINING counts, because draining is
	// precisely when a Tower needs its link: it has to heartbeat while it winds down and
	// then POST /tower/session/close. Refusing it would leave the fleet to age out over the
	// freshness window instead, which is the outcome the drain exists to avoid.
	if !towerMayHoldLink(tw) {
		return admit.Tower{}, nil, false
	}
	// CERTIFICATE REVOCATION, enforced here. This deployment authenticates a Tower by its
	// signed request rather than by a certificate presented at a TLS handshake, so a revoked
	// certificate would otherwise be inert - the review's finding. The certificate serial is
	// bound to the Tower at enrollment, so a revoked serial is a per-Tower kill switch that
	// takes effect on the Tower's very next request, without waiting for its lease to lapse.
	if ts.ca != nil && ts.ca.SerialRevoked(tw.CertSerial) {
		return admit.Tower{}, nil, false
	}
	// MUTUAL TLS, the channel-binding half, enforced when the Tower connected over TLS and
	// presented a client certificate. The signed request above proves possession of the
	// admitted identity KEY; this additionally binds the CONNECTION to the admitted
	// certificate, so a stolen request signature replayed over a different channel is refused,
	// and a revoked certificate is caught at the handshake as well as by the serial check.
	//
	// Verify-if-presented rather than require: a Tower that connects over plain HTTP (or a test
	// harness) still authenticates by its signed request alone, so this can be rolled out
	// without a flag day. A deployment that terminates TLS at the broker and requires client
	// certs gets the full mutual-TLS guarantee the spec describes; one that does not still has
	// the object-signature guarantee it always had.
	if ts.ca != nil && r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		if err := ts.ca.AuthenticateAs(r.TLS.PeerCertificates[0], tw.ID); err != nil {
			return admit.Tower{}, nil, false
		}
	}
	return tw, ed25519.PublicKey(raw), true
}

// towerMayHoldLink reports whether a Tower may hold a session and push inventory.
//
// Deliberately broader than MayTakeWork (which gates DISPATCH) and narrower than "the key
// verifies" (which gates nothing at all). Quarantine passes, because that is the state
// admission puts a Tower in and it must be able to connect. Suspended, revoked, expired and
// pending do not, and neither does a lapsed lease - the lease is what bounds what a Tower may
// do while nobody is watching it closely.
func towerMayHoldLink(tw admit.Tower) bool {
	if time.Now().After(tw.LeaseExpires) {
		return false
	}
	if tw.State == admit.StateDraining {
		return true // winding down still needs the link to wind down ON
	}
	return admit.EligibleFor(tw.State) != admit.EligibilityNone
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

	var hello link.Hello
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

	// THE DURABLE HEAD DETECTS; IT DOES NOT RESUME. This is the correction the audit forced,
	// and it matters because the wrong version was worse than useless.
	//
	// A head records a revision and a hash - never the inventory BODY, by design: the body is
	// large and reconstructible. So an instance that has the head but not the leaves cannot
	// accept a delta: the first op touching a leaf it does not hold sends towercore/inv
	// straight to "there is no accepted revision to amend". Reporting Resume on the strength
	// of the durable head alone therefore promised something this instance could not honour,
	// and cost the Tower an extra failed round trip before the snapshot it needed anyway.
	//
	// So resume requires BOTH: our recorded head agrees, AND this instance is actually
	// holding that chain. What the durable head buys is the other thing - seeing a replay or
	// a fork from any instance, including one that has never met this Tower.
	if ts.heads != nil {
		out, herr := ts.heads.Reconcile(tw.ID, hello.HeadRevision, hello.HeadHash)
		if herr != nil {
			log.Printf("tower %s: head store unavailable, asking for a full inventory: %v", tw.ID, herr)
		}
		// Presence is not enough: an instance holding an OLDER revision would report Resume
		// and then refuse the delta that followed. The position has to match on this
		// instance as well as in the durable record.
		ourRev, ourHash, holdsChain := ts.inv.Head(tw.ID)
		inStep := holdsChain && ourRev == hello.HeadRevision && ourHash == hello.HeadHash
		acc.NeedFullInventory = out.NeedsFullInventory() || !inStep
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

	var f link.Frame
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

	var f link.Frame
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
	// Draining is the point at which a fleet stops being offered AT ONCE rather than aging
	// out over the freshness window - which is only true if every broker stops offering it.
	b.forgetRoutable(tw.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "drained": true})
}

// towerInventory handles POST /tower/inventory (a full signed revision) and
// POST /tower/inventory/delta (a hash-chained amendment).
//
// The Tower ID comes from the AUTHENTICATED caller, never from the object, and the object's
// own tower_id is checked against it inside inv. Two independent places agreeing is the
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

		var res inv.Result
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
			// PUBLISH THE FLEET VIEW, so a broker that is NOT holding this Tower's link can
			// still route to its Stations. Without it a Tower's capacity is visible only
			// through the one instance it happens to be connected to, and with two brokers
			// roughly half the requests miss capacity that is sitting right there.
			//
			// After the head, and never instead of it: this is a read model, and a failure
			// to publish costs reachability rather than correctness.
			b.publishRoutable(tw.ID)
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
func errorIsResync(err error) bool { return errors.Is(err, inv.ErrResync) }

// excludedView reports WHY each leaf was dropped, so an operator can see which of their
// Stations is not earning without Core having to accept it in order to tell them.
func excludedView(ex []inv.Exclusion) []map[string]string {
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

	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: newInviteID(), Network: link.PublicNetwork, StationID: stationID, Owner: ownerPubkey,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: tw.ID},
		AssertionKey: req.AssertionKey, SessionKey: req.SessionKey,
		Role: req.Role, CeilingHash: req.CeilingHash,
	}, stationInviteTTL, time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// CAPPED PER OWNER. Without this any signed-in free account could loop this route and
	// grow rogerai.station_authorizations without bound for a full TTL - the same
	// database-filling vector the enrollment-token layer already closed
	// (admit.PutTokenCapped). The cap is enforced by the write, not by counting first:
	// concurrent calls would all read the same count and all insert.
	wrote, err := ts.stationStore.PutAuthorizationCapped(auth, maxOpenInvitesPerOwner)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not record the invitation - try again in a moment")
		return
	}
	if !wrote {
		jsonErr(w, http.StatusTooManyRequests, fmt.Sprintf(
			"you already have %d unredeemed Station invitations - redeem or wait for one to expire",
			maxOpenInvitesPerOwner))
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

// towerStationAttach handles POST /tower/station/attach: the Station redeeming the
// invitation its operator was given.
//
// WITHOUT THIS ROUTE THE INVITE WAS A DEAD END. The audit put it plainly: /tower/station/
// invite handed out a one-time secret and a TTL for something nothing could redeem, so the
// registry every leaf is verified against stayed empty and towercore/inv refused every offer
// as unknown - the exact condition the invite was introduced to end.
//
// The caller here is the TOWER, not the operator and not the Station: a Station reaches Core
// through the Tower relaying it, and the Tower is the party holding an admitted identity we
// can authenticate. What stops the Tower attaching a Station of its own invention is that
// the invitation pins the Station ID, the owner and BOTH keys, and it can only have come
// from an operator who owns this Tower.
func (b *broker) towerStationAttach(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)

	var req struct {
		TowerID      string `json:"tower_id"`
		InvitationID string `json:"invitation_id"`
		Secret       string `json:"secret"`
		StationID    string `json:"station_id"`
		Owner        string `json:"owner"`
		AssertionKey string `json:"assertion_key"`
		SessionKey   string `json:"session_key"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tw, _, ok := b.towerCaller(r, body, req.TowerID)
	if !ok {
		jsonErr(w, http.StatusForbidden, "attaching a Station requires a registered Tower's own signed request")
		return
	}

	at, err := ts.stations.Admit(attach.Proof{
		AuthID: req.InvitationID, Secret: req.Secret,
		Network: link.PublicNetwork, StationID: req.StationID, Owner: req.Owner,
		// The origin is not taken from the request: it is THIS Tower, the one that
		// authenticated. A Tower cannot attach a Station onto somebody else's origin.
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: tw.ID},
		AssertionKey: req.AssertionKey, SessionKey: req.SessionKey,
	})
	switch {
	case errors.Is(err, attach.ErrUnavailable):
		jsonErr(w, http.StatusServiceUnavailable, "could not record the attachment - try again in a moment")
		return
	case err != nil:
		// Every refusal reads the same to the caller. Which check refused it is an oracle a
		// Station has no business probing, and attach.ErrRejected is deliberately uniform.
		jsonErr(w, http.StatusForbidden, "that invitation cannot be redeemed")
		return
	}

	log.Printf("station %s attached to tower %s (state %s)", at.StationID, tw.ID, at.State)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "station_id": at.StationID, "state": at.State, "epoch": at.Epoch,
		// Said plainly, because it is the next thing the operator will ask: attachment is
		// identity, not eligibility.
		"note": "attached in quarantine - it carries no public work until Core's own evidence promotes it",
	})
}

// towerStationRevoke handles POST /tower/station/revoke: the operator retiring a Station
// identity terminally. It is the action the invite route's conflict message names, and
// naming an action no route exposes is the failure this whole feature exists to correct.
func (b *broker) towerStationRevoke(w http.ResponseWriter, r *http.Request) {
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
		jsonErr(w, http.StatusUnauthorized, "revoking a Station requires a signed-in account - run `roger-tower login`")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	var req struct {
		StationID string `json:"station_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Only the owner who holds it. Answered identically for a Station that does not exist,
	// so this cannot enumerate other people's Stations.
	at, found, err := ts.stations.Station(req.StationID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the Station registry - try again in a moment")
		return
	}
	ownerPubkey := r.Header.Get("X-Roger-Pubkey")
	if !found || at.Owner != ownerPubkey {
		jsonErr(w, http.StatusNotFound, "no such Station on this account")
		return
	}
	if _, err := ts.stations.Revoke(req.StationID); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not revoke - try again in a moment")
		return
	}
	// The revoked Station stops being routable at once - policy refuses it on the next
	// revision, and the origin claim is released so it can attach elsewhere.
	//
	// Deliberately NOT inv.Forget(tower): dropping the whole Tower's inventory to retire one
	// Station forces every sibling leaf through a full resync, and for a direct-origin
	// attachment the Tower ID is empty so it would forget nothing at all while looking like
	// it had. The leaf itself goes when the Tower next pushes, and policy refuses it in the
	// meantime because the attachment is revoked.
	ts.inv.ReleaseStation(req.StationID)
	// Re-publish rather than forget: only this Station is retired, and the rest of the
	// Tower's fleet must go on being routable from every instance.
	b.publishRoutable(at.Origin.TowerID)
	log.Printf("station %s revoked by %s", req.StationID, owner)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true})
}

// towerStationPromote handles POST /tower/station/promote: the manual opener for the
// quarantine gate.
//
// IT IS ADMIN-GATED, NOT OPERATOR-GATED, and that is the whole design of it. Quarantine
// exists so that admission (proving who you are) and eligibility (being trusted with
// customer traffic) are separate decisions. An operator who could promote their own Station
// would collapse them back into one and the gate would mean nothing - the person asking to
// be trusted cannot also be the person granting it.
//
// This is the MANUAL path, and it is deliberately the only one that exists today. The
// approved design has promotion driven by evidence Core observed itself - session uptime it
// held, probes it dispatched, signatures it verified - graduating through a bounded share of
// traffic. None of that is built, so pretending an automatic ladder exists would be worse
// than an explicit switch a human has to throw. See docs/tower-relay-link-design.md section
// 7, and note that the same design requires exactly this switch as the escape hatch for when
// automated promotion turns out to be wrong.
func (b *broker) towerStationPromote(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	// requireAdmin accepts a browser session as well as the header, so this route is
	// reachable from the console and needs the same CORS preamble its siblings carry.
	if b.requireAdmin(w, r) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	var req struct {
		StationID string `json:"station_id"`
	}
	if err := json.Unmarshal(readTowerBody(r), &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	promoted, err := ts.stations.Promote(req.StationID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not promote - try again in a moment")
		return
	}
	if !promoted {
		// Unknown, already promoted, or terminal. Said plainly rather than as a 404, because
		// the caller here is an administrator who needs to know which.
		at, found, ferr := ts.stations.Station(req.StationID)
		state := "unknown"
		if ferr == nil && found {
			state = at.State
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "promoted": false, "state": state,
			"reason": "only a Station in quarantine can be promoted",
		})
		return
	}
	log.Printf("station %s promoted out of quarantine by an administrator", req.StationID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "promoted": true, "state": attach.StateActive})
}

// stationInviteTTL bounds how long an invitation may sit unredeemed. Long enough for an
// operator to paste it into a machine they are standing at; short enough that a leaked one
// stops mattering quickly.
const stationInviteTTL = time.Hour

// maxOpenInvitesPerOwner bounds unredeemed invitations per account. Generous enough that an
// operator attaching a rack of Stations never notices; low enough that the table cannot be
// used as free storage.
const maxOpenInvitesPerOwner = 25

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

// towerLifecycle handles POST /tower/lifecycle: move a Tower through the approved state
// table. This is the quarantine gate, and its absence was a hard stop.
//
// EVERY TOWER WAS STUCK. Enrollment puts a Tower in quarantine, which by design takes no
// ordinary work. The state machine has the quarantine->active edge and Registry.Transition
// enforces the whole approved table under a CAS - but nothing in the broker ever called it.
// There was no route and no admin control, so a Tower could enroll, hold the link and push
// inventory, and never become eligible for a single request. Nothing failed. No test
// noticed, because every test that cared about eligibility set the state directly.
//
// ADMIN-GATED, NOT OPERATOR-GATED, for the reason Station promotion is: admission (proving
// who you are) and eligibility (being trusted with customer traffic) are separate decisions,
// and the person asking to be trusted cannot also be the one granting it.
//
// It applies the TABLE rather than the caller's string. Suspended does not go straight back
// to active - clearing a suspension returns a Tower to quarantine for fresh probes - and
// Transition refuses whatever the table does not permit. That refusal is a 409: the request
// was well formed and the answer is "not from where this Tower is standing".
//
// This is the MANUAL path, and deliberately the only one. The approved design promotes on
// evidence Core observed itself - session uptime it held, probes it dispatched, signatures
// it verified - through a bounded share of traffic. None of that is built, and pretending an
// automatic ladder exists would be worse than a switch a human has to throw. The same design
// requires exactly this switch anyway, as the escape hatch for when the automatic one is
// wrong.
func (b *broker) towerLifecycle(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	var req struct {
		TowerID string `json:"tower_id"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(readTowerBody(r), &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TowerID == "" {
		jsonErr(w, http.StatusBadRequest, "tower_id is required")
		return
	}
	to := admit.State(req.State)
	// Checked HERE as well as inside Transition so an unknown state is a 400 (the caller
	// sent nonsense) and a refused edge is a 409 (the caller sent something real that the
	// table would not allow). An administrator acts on those two differently.
	if !admit.Valid(to) {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("%q is not a Tower state", req.State))
		return
	}
	before, found := ts.registry.Get(req.TowerID)
	if !found {
		jsonErr(w, http.StatusNotFound, "no such Tower")
		return
	}
	if err := ts.registry.Transition(req.TowerID, to); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	// Eligibility is cached by the inventory policy, so a Tower that just lost it must stop
	// being routable now rather than whenever the cache next happens to refresh.
	if ts.policy != nil {
		ts.policy.Invalidate()
	}
	log.Printf("tower %s moved %s -> %s by an administrator", req.TowerID, before.State, to)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tower_id": req.TowerID, "was": string(before.State), "state": string(to),
		"eligibility": string(admit.EligibleFor(to)),
	})
}

// operatorMayMove is what an operator may do to a Tower THEY OWN, keyed by the state it is
// IN as well as the state they are asking for.
//
//	active   -> draining   pause my own hardware. Keeps the link, takes no new work.
//	draining -> active     un-pause it.
//	anything -> revoked    retire it. Terminal, and it is their hardware.
//
// THE "FROM" IS THE WHOLE SECURITY PROPERTY, and the first version of this got it wrong.
// It allowed `active` as a DESTINATION and reasoned that the approved transition table would
// refuse it out of quarantine - but quarantine->active is exactly the edge an administrator
// uses to promote, so it is legal, and an operator could promote themselves out of quarantine
// in one call. The admission gate would have meant nothing.
//
// Resuming from DRAINING is returning a Tower to a state an administrator already granted.
// Leaving QUARANTINE is that grant. They are different decisions and only the pair makes
// them distinguishable.
//
// Suspension is absent for the same class of reason: it is a decision ABOUT an operator, and
// self-service suspend-then-reinstate would clear a Tower that is under review. Expired and
// pending are absent because neither is a thing anybody decides - they are things that happen.
var operatorMayMove = map[admit.State]map[admit.State]bool{
	admit.StateActive:     {admit.StateDraining: true, admit.StateRevoked: true},
	admit.StateDraining:   {admit.StateActive: true, admit.StateRevoked: true},
	admit.StateQuarantine: {admit.StateRevoked: true},
	admit.StateSuspended:  {admit.StateRevoked: true},
	admit.StateExpired:    {admit.StateRevoked: true},
	admit.StatePending:    {admit.StateRevoked: true},
}

// towerSelfLifecycle handles POST /tower/self/lifecycle: an operator pausing, resuming or
// retiring a Tower they own.
//
// SEPARATE FROM THE ADMIN ROUTE, with its own authentication and its own allowlist, rather
// than one handler that decides which caller it has. Mixing the two would put "is this an
// administrator" and "may this state be set" in the same branch, and getting that wrong is
// how an operator promotes themselves out of quarantine.
//
// What makes this safe is not the allowlist alone but the approved TABLE underneath it: even
// with `active` permitted here, Transition refuses quarantine->active, so the one decision an
// operator must not make about themselves is refused by the state machine rather than by a
// list somebody has to remember to keep short.
func (b *broker) towerSelfLifecycle(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	owner, ok := b.towerOperator(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "this needs a signed-in account - run `roger-tower login`")
		return
	}
	var req struct {
		TowerID string `json:"tower_id"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TowerID == "" {
		jsonErr(w, http.StatusBadRequest, "tower_id is required")
		return
	}
	to := admit.State(req.State)
	// THE TOWER MUST BE THEIRS. Checked against the registry rather than taken from the
	// request, and answered exactly like a Tower that does not exist - otherwise this route
	// would tell a stranger which Tower IDs are real. It is read BEFORE the permission check
	// because what an operator may do depends on the state their Tower is in.
	before, found := ts.registry.Get(req.TowerID)
	if !found || before.Owner != owner {
		jsonErr(w, http.StatusNotFound, "no such Tower on this account")
		return
	}
	if !operatorMayMove[before.State][to] {
		jsonErr(w, http.StatusForbidden, fmt.Sprintf(
			"an operator may drain, resume or retire their own Tower; moving it from %s to %q "+
				"is an administrator's decision", before.State, req.State))
		return
	}
	if err := ts.registry.Transition(req.TowerID, to); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if ts.policy != nil {
		ts.policy.Invalidate()
	}
	// A Tower that has stopped taking work must stop being OFFERED, on every instance, now.
	// Leaving the fleet view up would keep sending requests at a Tower that is refusing them
	// for the rest of the freshness window.
	if admit.EligibleFor(to) != admit.EligibilityEligible {
		b.forgetRoutable(req.TowerID)
	}
	log.Printf("tower %s moved %s -> %s by its operator %s", req.TowerID, before.State, to, owner)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tower_id": req.TowerID, "was": string(before.State), "state": string(to),
	})
}

// --- housekeeping -----------------------------------------------------------

// towerInviteSweepInterval paces the invitation reaper. Well under the TTL, so an expired
// invitation never lingers for long, and rare enough to be invisible.
const towerInviteSweepInterval = 10 * time.Minute

// inviteRetryHorizon is how long a CONSUMED invitation is kept after it expires, so a Station
// retrying after a lost response still gets its committed answer. Past this, no plausible
// retry is still in flight and the row is only storage.
const inviteRetryHorizon = 24 * time.Hour

// towerInviteSweep deletes expired UNREDEEMED Station invitations.
//
// The reaper existed and nothing called it, which is how a bounded table becomes an
// unbounded one: the per-owner cap stops any single account running away, but without a
// sweep the rows only ever accumulate, and an operator who invites and never redeems
// eventually cannot invite at all. Consumed invitations are deliberately KEPT - they are
// what answers a lost-response retry.
func (b *broker) towerInviteSweep(stop <-chan struct{}) {
	if b.tower == nil || b.tower.stationStore == nil {
		return
	}
	t := time.NewTicker(towerInviteSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.towerInviteSweepOnce(time.Now())
		}
	}
}

// towerInviteSweepOnce is one iteration, split out so the reaping is testable without a
// ticker - the same shape the hold sweeper uses.
func (b *broker) towerInviteSweepOnce(now time.Time) {
	if b.tower == nil || b.tower.stationStore == nil {
		return
	}
	n, err := b.tower.stationStore.Reap(now, inviteRetryHorizon)
	if err != nil {
		log.Printf("station invites: sweep failed: %v", err)
	} else if n > 0 {
		log.Printf("station invites: reaped %d expired unredeemed invitation(s)", n)
	}
	// Reputation evidence ages out of the window it is judged in, so a table that kept every
	// outcome forever would grow without bound while nothing older than the window is ever
	// read. Reap past the window, on the same sweep - one fewer ticker to keep alive.
	if b.tower.outcomes != nil {
		if r, rerr := b.tower.outcomes.Reap(now.Add(-reputationWindow)); rerr != nil {
			log.Printf("tower outcomes: sweep failed: %v", rerr)
		} else if r > 0 {
			log.Printf("tower outcomes: reaped %d aged-out outcome(s)", r)
		}
	}
	// And transcripts that were selected for audit and never arrived: a Station that cannot
	// show its work for a sampled attempt is the spec's quarantine trigger.
	b.sweepAuditOverdue(now)
}

// towerLeaseExpire handles POST /tower/lease/expire: an administrator taking a Tower off the
// link now rather than at the end of its lease term.
//
// This route is why admit.ExpireLease exists in the production binary at all. It was
// previously a test hook that shipped, and renaming it to sound like an operation did not
// change that - an audit pointed out, correctly, that a capability nothing exposes is not a
// capability. towerMayHoldLink keys off the lease, so ending one is the immediate,
// reversible-by-renewal way to stop a Tower holding sessions and pushing inventory, short of
// the terminal states.
func (b *broker) towerLeaseExpire(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	if b.requireAdmin(w, r) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	var req struct {
		TowerID string `json:"tower_id"`
	}
	if err := json.Unmarshal(readTowerBody(r), &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := ts.registry.ExpireLease(req.TowerID); err != nil {
		jsonErr(w, http.StatusNotFound, "no such Tower")
		return
	}
	// Its sessions and inventory go with it: leaving leaves routable after the lease is gone
	// is the immortal-inventory failure by another route.
	ts.inv.Forget(req.TowerID)
	b.forgetRoutable(req.TowerID)
	log.Printf("tower %s: lease expired by an administrator", req.TowerID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expired": true})
}

// registerTowerRoutes mounts every /tower/ route.
//
// It is ONE function called by both the production mux and the test server, because it used
// to be two lists. They diverged the moment a route was added: /tower/lifecycle went into
// production and the tests kept 404ing against a mux that had never heard of it. A test
// harness that mounts its own approximation of the routes is testing the approximation.
func (b *broker) registerTowerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/tower/token", b.towerToken)                // operator: mint a one-time enrollment token
	mux.HandleFunc("/tower/enroll/challenge", b.towerChallenge) // Tower: get the nonce to sign
	mux.HandleFunc("/tower/enroll", b.towerEnroll)              // Tower: admission itself
	mux.HandleFunc("/tower/status", b.towerStatus)              // operator: my Towers

	// RENEWAL, signed by the Tower rather than the operator. Certificates and leases are
	// short-lived, so this runs forever on a schedule and no human is involved - a human
	// asked to re-authenticate their fleet daily acquires the habit a phishing mail needs.
	mux.HandleFunc("/tower/renew/challenge", b.towerRenewChallenge)
	mux.HandleFunc("/tower/renew", b.towerRenew)

	// The LINK: the Tower itself talking, authenticated by its admitted identity key rather
	// than by an operator account. Session first, then inventory over it.
	mux.HandleFunc("/tower/session", b.towerSessionOpen)             // Tower: open the link
	mux.HandleFunc("/tower/session/heartbeat", b.towerHeartbeat)     // Tower: still here
	mux.HandleFunc("/tower/session/close", b.towerSessionClose)      // Tower: orderly drain
	mux.HandleFunc("/tower/inventory", b.towerInventory(false))      // Tower: full signed revision
	mux.HandleFunc("/tower/inventory/delta", b.towerInventory(true)) // Tower: chained amendment

	mux.HandleFunc("/tower/station/invite", b.towerStationInvite)      // operator: authorize a Station to attach
	mux.HandleFunc("/tower/station/attach", b.towerStationAttach)      // Tower: redeem a Station invitation
	mux.HandleFunc("/tower/station/edge-cert", b.towerStationEdgeCert) // operator: get a Station its edge TLS certificate
	mux.HandleFunc("/tower/station/revoke", b.towerStationRevoke)      // operator: retire a Station identity
	mux.HandleFunc("/tower/station/promote", b.towerStationPromote)    // admin: open the Station quarantine gate

	mux.HandleFunc("/tower/cert/revoke", b.towerCertRevoke)       // admin: revoke a Tower certificate now
	mux.HandleFunc("/tower/lease/expire", b.towerLeaseExpire)     // admin: take a Tower off the link now
	mux.HandleFunc("/tower/lifecycle", b.towerLifecycle)          // admin: the Tower quarantine gate
	mux.HandleFunc("/tower/self/lifecycle", b.towerSelfLifecycle) // operator: drain/resume/retire my own

	// DISPATCH. The Tower collects work for its Stations and returns the answer; the key is
	// public so a Station can pin what a real grant is signed by.
	mux.HandleFunc("/tower/dispatch", b.towerDispatchPoll)          // Tower: collect work
	mux.HandleFunc("/tower/dispatch/result", b.towerDispatchResult) // Tower: return the answer
	mux.HandleFunc("/tower/dispatch/key", b.towerDispatchKey)       // public: Core's grant key

	// THE EDGE PATH. Core's whole involvement in a Tower-served request: it authorized one
	// earlier, and here it takes the consumer's account of what came back. The payload went
	// nowhere near this process.
	mux.HandleFunc("/tower/edge/authorize", b.towerEdgeAuthorize) // consumer: route me to a Station
	mux.HandleFunc("/tower/edge/ack", b.towerEdgeAck)             // consumer: what I actually received
	mux.HandleFunc("/tower/edge/settle", b.towerEdgeSettle)       // Tower: the Station's receipt

	// AUDIT: the post-hoc content review that replaces pre-dispatch screening on the edge
	// path. The courier asks what is wanted and forwards the Station-signed transcripts.
	mux.HandleFunc("/tower/audit/wanted", b.towerAuditWanted)         // Tower: what do I owe you?
	mux.HandleFunc("/tower/audit/transcript", b.towerAuditTranscript) // Tower: here it is
}
