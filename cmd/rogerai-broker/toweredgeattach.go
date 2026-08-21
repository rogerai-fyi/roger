package main

// toweredgeattach.go is Option C's SELF-ATTACH: a `roger share` node registers itself as a
// servable Station in ONE owner-signed call, replacing the file-based invite → attach flow
// (which required a second binary and a human carrying a secret between machines).
//
// Contract: features/tower/edge_dispatch.feature.
//
// # POSSESSION IS THE AUTHORIZATION
//
// The classic flow exists so an operator can authorize a MACHINE THAT IS NOT THE CALLER: the
// invitation secret is how authority crosses from the operator's terminal to the Station box.
// Here the caller IS the machine being attached - the node generated its own keys and signs
// this request with the account key it holds from `roger login`. There is no second machine
// for a secret to reach, so the invitation degenerates into an internal detail: this handler
// mints one and redeems it IN THE SAME CALL, through the same attach.Registry.Admit - keeping
// every uniqueness, cap, and atomicity guarantee of the classic path with zero new store
// semantics. The plaintext secret never leaves this function.
//
// # CORE ASSIGNS THE TOWER
//
// The node does not pick where it serves; Core matchmakes a live, admitted tower that
// advertises a data-plane endpoint (mirroring edgeTargetFor's own eligibility rules). The
// tower relaying a stranger's node is safe precisely because it is blind - it carries sealed
// bytes it cannot read - and polling rights are bound to the node by SIGNATURE: it signs each
// hub request with the assertion key recorded here, and the tower verifies against the copy
// Core hands it. The HubToken below is the credential that scheme replaced, kept for one
// release so a node built before signed polls still authenticates somewhere.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// newHubToken mints the bearer token a self-attached node USED to present to its tower's hub.
//
// It is still minted, and only for the transition: a node running a build from before signed hub
// polls has no other way to authenticate, and refusing to issue one would take an already-shipped
// provider off the fabric for a defect on our side of the wire. A current node receives it and
// never transmits it, and the moment it signs to its tower once, that tower stops accepting the
// token for it at all (internal/towerhub/nodeauth.go) - so this is minted for a population that
// shrinks to nothing on its own.
//
// IT IS NOT ROTATED ON RE-ATTACH, and that is a decision rather than an omission. The only node
// that still presents this token puts it on a plaintext wire every twenty-five seconds, so an
// attacker who captured the old one captures the new one just as easily; rotation would be
// motion without protection. What actually retires the credential is the node that holds it
// ceasing to send it, which is what the tower's latch detects.
//
// DELETE THIS, the column, and towerhub's bearer path together, one release after signed polls
// ship. Nothing else keys on the field: attach.Attachment.SelfAttached is what the readers that
// used to test it for emptiness ask now, precisely so this deletion is a deletion and not an
// outage.
func newHubToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw)
}

// towerEdgeAttach handles POST /tower/edge/attach.
func (b *broker) towerEdgeAttach(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodPost) {
		return
	}
	corsCreds(w, r)
	body := readTowerBody(r)

	// The signed-in account. Same resolution as every tower operator surface: the request's
	// signature must verify and its pubkey must be bound to a non-anonymized account.
	owner, ok := b.towerOperator(r, body)
	if !ok {
		// THE TIGHT PER-IP BUCKET, ON THE WAY OUT - the same treatment /tower/edge/authorize
		// got when it was found bare, on the same condition and for the same reason. An
		// unsigned caller here is by definition never going to be served, but reaching this
		// line has already cost an ed25519 verification and an owner lookup, and until now
		// that cost was free to the caller and unbounded. A signed caller never reaches this
		// branch and keeps its own per-account bucket below.
		if allowed, retry := b.anonRL.allow(clientIP(r)); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
			return
		}
		jsonErr(w, http.StatusUnauthorized, "attaching a node requires a signed-in account - run `roger login`")
		return
	}
	// The attachment records the account PUBKEY (what towerpolicy resolves), taken from the
	// request that was authenticated - never from the body. towerOperator already resolved and
	// vetted this exact key (bound, non-anonymized).
	//
	// CANONICALIZED: this key is the payee of every lot this node ever earns, so it must be
	// the ACCOUNT's key rather than the key of whichever device happened to run the attach.
	// A provider who attaches from their server and cashes out from their laptop is one
	// account, and their money must not be split across the two.
	ownerPubkey := b.accountKeyOfPubkey(r.Header.Get("X-Roger-Pubkey"))
	// AND A PER-ACCOUNT BUCKET, WHICH THIS ENDPOINT HAS NEVER HAD. /tower/edge/authorize was
	// found registered bare and given exactly this pair; attach sits on the same mux, does
	// strictly more work per call, and was missed.
	//
	// IT BECAME LOAD-BEARING WITH THE CONSUME FIX ABOVE THIS RELEASE, which is the part worth
	// reading before anyone decides it is redundant. A self-attach mints an invitation, tries
	// to redeem it, and on refusal marks it spent. That mark used to land nowhere on Postgres,
	// so a refusal loop filled the owner's 25-invitation cap and every further attempt was
	// turned away cheaply at PutAuthorizationCapped - an accidental brake, and the reason
	// nobody noticed there was no limiter here. Making the refusal path work as designed
	// removes that brake by construction: refusals no longer accumulate, so the cap never
	// fills, and the loop can run at line rate doing a lock, two transactions and a rollback
	// each time. Fixing the lockout without this would trade an operator-facing bug for a
	// database-facing one.
	//
	// Keyed on the ACCOUNT key rather than the device key - one identity, one bucket, so a
	// caller cannot multiply its rate by generating keypairs against the same account. That is
	// the same discipline authorize uses, and it is the same key the cap and the payee are
	// drawn on, so all three bound the same thing.
	//
	// A REAL FLEET DOES NOT FEEL THIS. The default is 120/minute with a burst of 40 per
	// account, and a node attaches once per tenancy, not once per request; a hundred-machine
	// operator restarting everything at once clears the whole fleet inside a minute, and the
	// nodes that are briefly turned away are already on the jittered re-attach backoff that
	// exists for exactly this (internal/agent's reattachDelay). It bounds a loop, not a fleet.
	if allowed, retry := b.rl.allow("attach:" + ownerPubkey); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return
	}
	// The same standing check enrollment uses: a banned or barred account may not put
	// machines on the network under its name.
	if err := (brokerOperatorPolicy{b: b}).MayEnroll(owner); err != nil {
		jsonErr(w, http.StatusForbidden, "this account may not attach a node")
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}

	var req struct {
		// StationID is OPTIONAL: a node that already minted its persistent station identity
		// (station.Init) attaches under it, so the grants Core signs name the id the node's
		// executor answers to. Absent -> Core mints one. Shape-checked; uniqueness is the
		// store's (PK + live-key indexes).
		StationID string `json:"station_id"`
		// NodeID is the BROKER node id of the `roger share` half of this same machine. It is
		// the join that lets edge placement rank a station by measured health - probes record
		// reliability, TTFT and TPS against the node id, and nothing else here can reach them.
		// Verified below against a live registration, never believed on its own.
		NodeID       string `json:"node_id"`
		AssertionKey string `json:"assertion_key"`
		SessionKey   string `json:"session_key"`
		Model        string `json:"model"`
		Modality     string `json:"modality"`
		// The node's own consumer prices, micro-USD per 1,000,000 tokens. 0/0 = unpriced
		// (byte tariff / free). Band-checked below - the same public band every offer obeys.
		PriceInMicros  int64 `json:"price_in_micros"`
		PriceOutMicros int64 `json:"price_out_micros"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Key SHAPE is checked here, before anything is stored, exactly as the invite path does.
	// (Both keys happen to be 32 bytes: the assertion key is ed25519, the session key X25519 -
	// ed25519.PublicKeySize is used as the shared "32" for both, as the classic path does.)
	for name, k := range map[string]string{"assertion_key": req.AssertionKey, "session_key": req.SessionKey} {
		raw, derr := hex.DecodeString(k)
		if derr != nil || len(raw) != ed25519.PublicKeySize {
			jsonErr(w, http.StatusBadRequest, name+" must be a hex-encoded 32-byte public key")
			return
		}
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Modality) == "" {
		jsonErr(w, http.StatusBadRequest, "a node names the model and modality it serves")
		return
	}
	// THE JOIN IS PROVED, NOT CLAIMED. A node id is a routing identity that carries a
	// reputation, so accepting whichever one the body names would let a fresh station borrow
	// a well-probed node's history - and, once placement scores on that history, borrow its
	// traffic. Two conditions: the registration must exist (an unregistered node has no
	// measurements to join to, which is the whole point of M0), and its pubkey must be the
	// key that signed THIS request, so the claim can only be made by the machine it is about.
	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID == "" {
		jsonErr(w, http.StatusBadRequest,
			"node_id is required: attach with the same node this machine registered as (`roger share`)")
		return
	}
	if !b.nodeRegisteredTo(nodeID, r.Header.Get("X-Roger-Pubkey")) {
		jsonErr(w, http.StatusForbidden,
			"node_id is not registered to this key - register with `roger share` before attaching")
		return
	}
	// The SAME allowlists the signed-leaf path enforces - a self offer gets no wider a door.
	if !towerModelAllowed(req.Model) || !towerModalityAllowed(req.Modality) {
		jsonErr(w, http.StatusBadRequest, "this model or modality is not accepted on the tower path")
		return
	}
	// The node's listed price obeys the SAME public band as every signed offer - checked at
	// the door, and re-checked at authorize (the projection is not a security boundary).
	if req.PriceInMicros != 0 || req.PriceOutMicros != 0 {
		floor, ceiling, bok := towerPriceBand(req.Model)
		if !bok || req.PriceInMicros < floor || req.PriceInMicros > ceiling ||
			req.PriceOutMicros < floor || req.PriceOutMicros > ceiling {
			jsonErr(w, http.StatusBadRequest, "the listed price is outside the public band for this model")
			return
		}
	}

	// A LOST-RESPONSE RETRY IS ANSWERED, NOT PUNISHED. A node that attached but never saw the
	// reply retries with the same keys; without this it would hit the key-uniqueness refusal
	// forever, its live slot burned and its hub token unrecoverable. The caller is
	// authenticated as the owner of that attachment, so re-showing its own record (token
	// included) is safe - exactly the classic path's replay-idempotence, rebuilt for the
	// invite-less flow.
	if prior, found, aerr := ts.stations.ByAssertionKey(req.AssertionKey); aerr == nil && found &&
		prior.Owner == ownerPubkey && prior.SessionKey == req.SessionKey && prior.Live() {
		// The offer is IMMUTABLE for a live identity: a "retry" carrying a different model or
		// price must fail loudly rather than silently keep the old one - an operator who
		// believes their price change took would be listing a number nobody is billed at.
		// Changing the offer = revoke + attach with fresh keys.
		if prior.Model != strings.TrimSpace(req.Model) || prior.PriceIn != req.PriceInMicros ||
			prior.PriceOut != req.PriceOutMicros {
			jsonErr(w, http.StatusConflict, "these keys are attached with a different offer; "+
				"revoke and re-attach with fresh keys to change model or price")
			return
		}
		// THE RELAY PLANE HAS TO BE THERE, AND `has` IS NOT A BOOL TO DROP.
		//
		// This read was `plane, _ := ts.link.RelayPlane(...)`, so a miss answered 200 with
		// endpoint:"" and endpoint_tls_spki:"" - a reply shaped like a successful attach that
		// cannot be used as one. The node refuses it ("attach answered without an endpoint"),
		// counts its own re-attach as failed, backs off and asks again, and the discarded bool
		// turns "I cannot answer this right now" into a loop with no error in it anywhere. Since
		// re-attach became routine (internal/agent's serveTowerTenancy) this stopped being a
		// lost-response corner and became a path nodes take whenever their relay has a bad day,
		// which is what makes it worth a refusal rather than a silence.
		//
		// IT IS A REFUSAL AND NOT A RE-PLACEMENT, and that is a decision rather than laziness.
		// A miss here has two causes and this handler cannot tell them apart. The first is
		// ordinary and temporary: LiveTowers and RelayPlane reflect THIS instance's link
		// sessions, and a Tower's link is held by exactly one broker, so a node that attached
		// through the instance holding its Tower and re-attaches through a different one finds
		// nothing with nothing wrong anywhere. The second is the one that hurts - the Tower is
		// gone for good and this attachment names a relay that will never answer again.
		//
		// Re-placing would answer the second by breaking the first: a Station would ping-pong
		// between Towers on nothing but which instance happened to take its attach. And it would
		// do it by writing origin_tower, which today has exactly one writer - Admit's upsert,
		// scoped by its WHERE clause to a dormant row - precisely so that a live Station's origin
		// is not a value that moves underneath an attempt already in flight. Rehoming a LIVE
		// Station is a real change with a real design (docs/relay-selection-design.md section 6),
		// and it needs a settle-time fence that does not exist yet. What is owed here is an
		// honest, retryable answer, which is what this is: the node's re-attach loop backs off
		// and asks again, and the operator is told once rather than never.
		plane, has := ts.link.RelayPlane(prior.Origin.TowerID)
		if !has {
			jsonErr(w, http.StatusServiceUnavailable,
				"the tower this node is attached to has no data plane right now - try again shortly")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"station_id": prior.StationID,
			"tower_id":   prior.Origin.TowerID,
			"endpoint":   plane.Endpoint,
			// The hub certificate pin, on the retry answer as well as on the fresh one and for
			// the same reason the fingerprint below is: a node that lost the first reply is the
			// same node, and a retry that came back without it would connect in plaintext to a
			// TLS listener and never serve again.
			"endpoint_tls_spki": plane.TLSSPKI,
			"hub_token":         prior.HubToken,
			// The relay's admitted identity fingerprint, on the retry answer as well as on the
			// fresh one: a node that lost the first reply is the same node, and leaving it off
			// here would strand exactly the caller this branch exists to rescue.
			"tower_key_hash": b.towerKeyFingerprint(prior.Origin.TowerID),
			"state":          prior.State,
			"note":           "already attached - this is your existing registration",
		})
		return
	}

	// CORE ASSIGNS THE TOWER: the first live, admitted tower advertising a data-plane
	// endpoint. (LiveTowers reflects this instance's link sessions - the instance holding the
	// links is the one that can answer; matchmaking beyond first-fit is a later refinement.)
	var towerID string
	var plane link.RelayPlane
	for _, tw := range ts.link.LiveTowers() {
		if !ts.registry.MayTakeWork(tw) {
			continue
		}
		if p, has := ts.link.RelayPlane(tw); has && p.Endpoint != "" {
			towerID, plane = tw, p
			break
		}
	}
	if towerID == "" {
		jsonErr(w, http.StatusServiceUnavailable, "no tower can host this node right now - try again shortly")
		return
	}

	// The internal invitation: minted and redeemed in this one call. The secret exists only
	// on this stack; the cap and one-use guarantees are the store's, unchanged.
	stationID := strings.TrimSpace(req.StationID)
	if stationID == "" {
		stationID = newStationID()
	} else if !attach.ValidStationID(stationID) {
		jsonErr(w, http.StatusBadRequest, "station_id is not a valid station identifier")
		return
	}
	hubToken := newHubToken()
	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: newInviteID(), Network: link.PublicNetwork, StationID: stationID, Owner: ownerPubkey,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: req.AssertionKey, SessionKey: req.SessionKey,
		HubToken: hubToken,
		// The verified join rides on the authorization, so the attachment inherits a node id
		// Core checked rather than one the attaching party restated.
		NodeID: nodeID,
		Model:  strings.TrimSpace(req.Model), Modality: strings.TrimSpace(req.Modality),
		PriceIn: req.PriceInMicros, PriceOut: req.PriceOutMicros,
	}, stationInviteTTL, time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	wrote, err := ts.stationStore.PutAuthorizationCapped(auth, maxOpenInvitesPerOwner)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not record the attachment - try again in a moment")
		return
	}
	if !wrote {
		jsonErr(w, http.StatusTooManyRequests, "this account has too many open attachments in flight - try again shortly")
		return
	}
	at, err := ts.stations.Admit(attach.Proof{
		AuthID: auth.ID, Secret: secret, Network: link.PublicNetwork,
		StationID: stationID, Owner: ownerPubkey,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: req.AssertionKey, SessionKey: req.SessionKey,
	})
	if err != nil {
		// The two keys may already be attached (under another owner or session key - the same-
		// owner retry was answered above), or the account is at its live-station cap. Consume
		// the internal invite so a refused self-attach loop cannot fill the owner's open-invite
		// cap and lock them out of the classic route; then let the registry's own message say
		// what refused, without leaking others' state.
		auth.Consumed, auth.ConsumedBy = true, "self-attach-refused"
		_ = ts.stationStore.PutAuthorization(auth)
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}

	// PROMOTED IMMEDIATELY, deliberately - an explicit decision, not an oversight. A
	// self-attached node is the SAME provider a direct `roger share` node is, and the direct
	// path serves paid traffic the moment it registers with a price; station quarantine was
	// designed for the operator-invited roger-station world, where a tower operator vouched
	// machines onto the network. Here the account itself signed (MayEnroll standing), the
	// price is band-checked twice, every attempt is ceiling-held and token/byte-clamped, and
	// the adaptive audit ramps on NEW nodes - those are the exposure controls, and the state
	// should say what is true: this node is serving.
	if _, perr := ts.stations.Promote(at.StationID); perr != nil {
		log.Printf("self-attach: could not promote %s out of quarantine: %v", at.StationID, perr)
	} else {
		at.State = attach.StateActive
	}

	// The node is routable NOW: publish its row (merged with the tower's leaf rows) so
	// edgeTargetFor can find it at its listed price.
	b.publishRoutable(towerID)

	writeJSON(w, http.StatusOK, map[string]any{
		"station_id": at.StationID,
		"tower_id":   towerID,
		// Where the node POLLS for work: the tower's data-plane endpoint. A current node
		// authenticates there by signing each request with its assertion key; the token is the
		// pre-signature credential, shown once here and readable by the tower from the
		// attachment, and is transmitted only by a node too old to sign.
		"endpoint": plane.Endpoint,
		// WHAT THE NODE MUST SEE THE HUB PRESENT, or empty for a plaintext hub. The node dials
		// https and accepts exactly this certificate when it is set - which is how a tower on a
		// home connection with no domain gets a VERIFIED channel: the pin is the tower's own
		// advertisement, relayed by the party the node already trusts for the address itself.
		// See internal/towerhub/pin.go.
		"endpoint_tls_spki": plane.TLSSPKI,
		"hub_token":         hubToken,
		// What the node checks the hub's PROCESS EPOCH against - see towerKeyFingerprint. The
		// epoch rides in the node's signed target and is published on an unauthenticated 401,
		// so without this the value a node signs over is chosen by whoever answers the socket.
		"tower_key_hash": b.towerKeyFingerprint(towerID),
		"state":          at.State,
		"note": "poll the endpoint's hub to serve, signing each request with this station's " +
			"assertion key; the tower relays sealed work it cannot read",
	})
}

// towerKeyFingerprint is the admitted identity-key hash of one Tower, as the attach response
// hands it to a node.
//
// # WHY A NODE IS GIVEN THIS AT ALL
//
// A serving node signs every hub request over a target naming the hub's PROCESS EPOCH, which
// exists so a signature captured before a redeploy is worthless after one. The node has no way
// to know that value in advance - Core assigns tower ids and knows nothing about when a tower
// restarted - so it learns it from the hub's own 401. That 401 is unauthenticated and the link
// is plaintext by construction, which made the epoch the ATTACKER'S choice rather than the
// hub's: answer a poll with a forged epoch and the node signs over it, producing bytes no hub
// has seen and no nonce ring has recorded.
//
// So the hub signs its epoch with the identity key Core admitted it under, and the node checks
// that signature against this fingerprint. Core is the right party to hand it over: it is
// already the node's source of truth for the tower id, the endpoint and the grant key, and it
// is the only party that knows which key it admitted this tower under. The value is a hash of a
// public key - it is not a secret, and it is already compared against on every request the
// Tower makes here (towerCaller).
//
// An empty answer means Core has no admission record for that Tower, which a current node
// treats as a refusal rather than as permission to believe whatever the relay says.
func (b *broker) towerKeyFingerprint(towerID string) string {
	ts := b.tower
	if ts == nil || towerID == "" {
		return ""
	}
	tw, ok := ts.registry.Get(towerID)
	if !ok {
		return ""
	}
	return tw.KeyHash
}

// towerHubNodes handles POST /tower/hub/nodes: a TOWER (its own signed request, exactly the
// settle path's authentication) fetches the stations self-attached to it and how each node
// authenticates, so it can Server.RegisterNode them on its data-plane hub. Only the tower the
// attachment names ever sees a token - the response is scoped by the authenticated tower id,
// and no other surface serializes HubToken.
//
// # THE ASSERTION KEY RIDES HERE, AND IT HAD TO COME FROM SOMEWHERE
//
// A hub verifies a node's signed poll against the Station's assertion key, and before this the
// tower had NO WAY to learn it: the attachment records it (attach.Attachment.AssertionKey) but
// nothing shipped it outward, and towerhub.Server.RegisterNode took a token and nothing else.
// The tower cannot derive it, cannot be told it by the node (the node is the party being
// authenticated), and must not accept it from anyone else. So Core - which recorded it at
// attach and is the only party both ends already trust - sends it on the one call the tower
// already makes for exactly this purpose. Additive: the key is public, it is already on the
// attachment, and an older tower ignores the field.
func (b *broker) towerHubNodes(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	ts := b.towerAvailable(w)
	if ts == nil {
		return
	}
	body := readTowerBody(r)
	var req struct {
		TowerID string `json:"tower_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if _, _, ok := b.towerCaller(r, body, req.TowerID); !ok {
		jsonErr(w, http.StatusForbidden, "listing hub nodes requires the Tower's own signed request")
		return
	}
	ats, err := ts.stations.ByTower(req.TowerID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "could not read the Station registry - try again in a moment")
		return
	}
	nodes := make([]map[string]any, 0, len(ats))
	for _, at := range ats {
		if !at.SelfAttached() {
			continue // classic-flow attachment: it does not poll a hub
		}
		nodes = append(nodes, map[string]any{
			"station_id": at.StationID,
			// What the hub verifies a signed poll against. Public by nature - it is the same
			// key Core verifies this Station's receipts with.
			"assertion_key": at.AssertionKey,
			// The pre-signature bearer, still sent so a tower can keep serving a node that
			// has not updated yet. It goes when towerhub.Server.AllowLegacyBearer goes.
			"hub_token": at.HubToken,
			"state":     at.State,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}
