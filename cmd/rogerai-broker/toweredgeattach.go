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
// never transmits it. Delete this, the column, and towerhub.Server.AllowLegacyBearer together,
// one release after signed polls ship.
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
		endpoint, _ := ts.link.RelayEndpoint(prior.Origin.TowerID)
		writeJSON(w, http.StatusOK, map[string]any{
			"station_id": prior.StationID,
			"tower_id":   prior.Origin.TowerID,
			"endpoint":   endpoint,
			"hub_token":  prior.HubToken,
			"state":      prior.State,
			"note":       "already attached - this is your existing registration",
		})
		return
	}

	// CORE ASSIGNS THE TOWER: the first live, admitted tower advertising a data-plane
	// endpoint. (LiveTowers reflects this instance's link sessions - the instance holding the
	// links is the one that can answer; matchmaking beyond first-fit is a later refinement.)
	var towerID, endpoint string
	for _, tw := range ts.link.LiveTowers() {
		if !ts.registry.MayTakeWork(tw) {
			continue
		}
		if ep, has := ts.link.RelayEndpoint(tw); has && ep != "" {
			towerID, endpoint = tw, ep
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
		"endpoint":  endpoint,
		"hub_token": hubToken,
		"state":     at.State,
		"note": "poll the endpoint's hub to serve, signing each request with this station's " +
			"assertion key; the tower relays sealed work it cannot read",
	})
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
		if at.HubToken == "" {
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
