package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// rc.go is the /rc/* remote-control surface (BASE STATION, v5.0.0): a live embedded-agent
// session on a HOST, continuable from any other surface logged into the SAME account. See
// docs-internal/REMOTE-CONTROL-DESIGN.md. It reuses the proven internals — the band-code
// crypto for the link secret (protocol.NewRCLinkCode + BandCodeHash), the agentPoll long-poll
// shape (25s holds), bandResolve's constant-work uniform-404, requireOwner/sessionOwner auth,
// corsCreds — but is NOT a node: a session never enters pickFor/discover/market/earnings. The
// broker stays content-blind: it relays RCFrames and keeps only a small TRANSIENT ring; it
// NEVER persists a frame (the host owns the transcript). Money: $0 (no billing path at all).

// rcRingFrames / rcRingBytes bound the per-session transient replay ring (memory only, used
// solely to bridge SSE reconnect gaps via Last-Event-ID). Never durable.
const (
	rcRingFrames  = 200
	rcRingBytes   = 256 << 10
	rcPollHold    = 25 * time.Second
	rcMaxFrameLen = 128 << 10 // a single inbound/outbound frame body cap
)

// rcHub is the per-session in-memory rendezvous, mirroring nodeTunnel. Single-instance path;
// the Valkey bus (Increment 5) is the multi-instance path.
type rcHub struct {
	mu       sync.Mutex
	in       chan protocol.RCInbound          // broker -> host (drained by /rc/{sid}/poll)
	viewers  map[string]chan protocol.RCFrame // viewerID -> that SSE conn's frame chan
	ring     []protocol.RCFrame               // bounded transient replay
	ringByte int
	seq      uint64
	hostUp   bool
	lastHost time.Time
}

func newRCHub() *rcHub {
	return &rcHub{in: make(chan protocol.RCInbound, 64), viewers: map[string]chan protocol.RCFrame{}}
}

// publish assigns the next seq, appends to the bounded ring, and fans out to every viewer
// (non-blocking: a slow viewer drops the frame rather than stalling the host). Returns the seq.
func (h *rcHub) publish(f protocol.RCFrame) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	f.Seq = h.seq
	if f.TS == 0 {
		f.TS = time.Now().Unix()
	}
	h.ring = append(h.ring, f)
	h.ringByte += len(f.Text) + len(f.Args)
	for len(h.ring) > rcRingFrames || (h.ringByte > rcRingBytes && len(h.ring) > 1) {
		h.ringByte -= len(h.ring[0].Text) + len(h.ring[0].Args)
		h.ring = h.ring[1:]
	}
	for _, ch := range h.viewers {
		select {
		case ch <- f:
		default:
		}
	}
	return h.seq
}

// subscribe registers a viewer's frame channel and replays ring frames newer than sinceSeq
// (Last-Event-ID reconnect). Returns the channel + an unsubscribe func.
func (h *rcHub) subscribe(viewerID string, sinceSeq uint64) (<-chan protocol.RCFrame, func()) {
	ch := make(chan protocol.RCFrame, 256)
	h.mu.Lock()
	for _, f := range h.ring {
		if f.Seq > sinceSeq {
			select {
			case ch <- f:
			default:
			}
		}
	}
	h.viewers[viewerID] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.viewers, viewerID)
		h.mu.Unlock()
	}
}

func (h *rcHub) markHost(up bool) {
	h.mu.Lock()
	h.hostUp = up
	if up {
		h.lastHost = time.Now()
	}
	h.mu.Unlock()
}

// rcHubFor returns (creating if needed) the hub for a session id.
func (b *broker) rcHubFor(sid string) *rcHub {
	b.rcMu.Lock()
	defer b.rcMu.Unlock()
	if b.rcHubs == nil {
		b.rcHubs = map[string]*rcHub{}
	}
	h, ok := b.rcHubs[sid]
	if !ok {
		h = newRCHub()
		b.rcHubs[sid] = h
	}
	return h
}

func (b *broker) rcDropHub(sid string) {
	b.rcMu.Lock()
	delete(b.rcHubs, sid)
	b.rcMu.Unlock()
}

func rcHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func rcRandToken(prefix string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func rcRandID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "rcs_" + hex.EncodeToString(b)
}

// rcOwnerWallet resolves the LOGGED-IN account wallet on r — a web session cookie OR a
// VERIFIED signed request bound to an owner. ok=false for anonymous / unauthenticated /
// unlinked callers: remote control is same-account only, so an anonymous keypair is never an
// owner here. deviceLabel is a human tag for origin attribution.
func (b *broker) rcOwnerWallet(r *http.Request, body []byte) (wallet, deviceLabel string, ok bool) {
	if login, _, w, sok := b.sessionOwner(r); sok && w != "" {
		return w, "web (" + login + ")", true
	}
	id, authed, iok := b.identityOf(r, body)
	if !iok || !authed {
		return "", "", false
	}
	w := b.walletOf(r, id)
	if !walletLoggedIn(w) { // an unbound anonymous keypair is not an account
		return "", "", false
	}
	label := "roger"
	if dev := r.Header.Get("X-Roger-Device"); dev != "" {
		label = "roger @ " + dev
	}
	return w, label, true
}

// rcEnable handles POST /rc/enable (host, signed): create a session, returning the one-time
// link code + host token (each shown ONCE). Enforces the per-owner active-session quota.
func (b *broker) rcEnable(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
	wallet, _, ok := b.rcOwnerWallet(r, body)
	if !ok {
		jsonErr(w, http.StatusForbidden, "remote control requires a logged-in account - run `roger login`")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "remote session"
	}
	// Quota: count active (non-revoked) sessions for this wallet.
	existing, err := b.db.RCSessionsByOwner(wallet)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	active := 0
	for _, s := range existing {
		if s.Active() {
			active++
		}
	}
	if active >= store.RCSessionQuota(wallet) {
		jsonErr(w, http.StatusTooManyRequests, "remote-control session limit reached ("+strconv.Itoa(store.RCSessionQuota(wallet))+") - end one first")
		return
	}
	code, display, tail := protocol.NewRCLinkCode()
	hostTok := rcRandToken("rc_host_")
	sid := rcRandID()
	sess := store.RCSession{
		ID: sid, OwnerWallet: wallet, Name: name,
		CodeHash:      protocol.BandCodeHash(tail),
		CodeExpires:   time.Now().Add(store.RCCodeTTL).Unix(),
		CodeDisplay:   display,
		HostTokenHash: rcHash(hostTok),
		LastHostSeen:  time.Now().Unix(),
	}
	if err := b.db.CreateRCSession(sess); err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	h := b.rcHubFor(sid)
	h.markHost(true)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":   sid,
		"name":         name,
		"code":         code,                       // ONCE
		"code_short":   protocol.RCLinkShort(code), // typeable / deep-link form
		"code_display": display,
		"host_token":   hostTok, // ONCE
		"code_expires": sess.CodeExpires,
	})
}

// rcSessions handles GET /rc/sessions (any owner surface): the roster, metadata only. Host
// online/offline is derived from LastHostSeen + the live hub.
func (b *broker) rcSessions(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodGet) {
		return
	}
	wallet, _, ok := b.rcOwnerWallet(r, nil)
	if !ok {
		jsonErr(w, http.StatusForbidden, "remote control requires a logged-in account")
		return
	}
	// Self-clean the roster on read (the lazy GC this list was always meant to run): an ENDED
	// (revoked) session and any host silent past RCIdleGC are dropped, so ending a session makes
	// it actually disappear from every surface (TUI + app) instead of lingering as "ended", and
	// long-dead sessions age out. Best-effort - a prune error must not fail the list.
	_, _ = b.db.PruneRCSessions(wallet, time.Now().Add(-store.RCIdleGC).Unix())
	list, err := b.db.RCSessionsByOwner(wallet)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(list))
	for _, s := range list {
		out = append(out, map[string]any{
			"id": s.ID, "name": s.Name, "code_display": s.CodeDisplay,
			"online":     b.rcOnline(s, now),
			"revoked":    s.Revoked,
			"created_at": s.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// rcOnline reports whether the host is currently connected (a recent poll).
func (b *broker) rcOnline(s store.RCSession, now time.Time) bool {
	if s.Revoked {
		return false
	}
	return now.Unix()-s.LastHostSeen < int64(store.RCHostOfflineAfter/time.Second)
}

// rcAttach handles POST /rc/attach (remote surface, owner-authed + {code}). CONSTANT-WORK +
// UNIFORM-ERROR, exactly like bandResolve: hash the tail, look up, require the caller wallet
// to OWN the session and the code window to be open; ANY failure returns the identical 404
// "no such session". On success, mint a per-device attach token (shown once).
func (b *broker) rcAttach(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
	wallet, deviceLabel, ok := b.rcOwnerWallet(r, body)
	uniform := func() { writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such session"}) }
	if !ok {
		uniform() // even "not logged in" gets the uniform 404 (no oracle on session existence)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &req)
	// Always hash + look up (constant work), even for empty/garbage input.
	sess, found, _ := b.db.RCSessionByCodeHash(protocol.BandCodeHash(req.Code))
	now := time.Now()
	// The ONLY success path: found, owned by THIS wallet, code window open.
	if !found || sess.OwnerWallet != wallet || !sess.CodeOpen(now) {
		uniform()
		return
	}
	attach := rcRandToken("rc_at_")
	if err := b.db.PutRCAttachToken(store.RCAttachToken{
		Hash: rcHash(attach), SessionID: sess.ID, DeviceLabel: deviceLabel,
	}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.ID, "name": sess.Name, "attach_token": attach, // ONCE
	})
}

// rcRevokeAll handles POST /rc/revoke-all (owner): end every session for the wallet.
func (b *broker) rcRevokeAll(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	wallet, _, ok := b.rcOwnerWallet(r, nil)
	if !ok {
		jsonErr(w, http.StatusForbidden, "remote control requires a logged-in account")
		return
	}
	// Terminal frame + drop hubs for each of this owner's live sessions before revoking.
	if list, err := b.db.RCSessionsByOwner(wallet); err == nil {
		for _, s := range list {
			if s.Active() {
				b.rcEndSession(s.ID)
			}
		}
	}
	n, err := b.db.RevokeRCSessions(wallet)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": n})
}

// rcEndSession pushes a terminal `ended` frame to viewers and drops the hub. The roster row's
// revoked flag is set by the caller (disable / revoke-all / account delete).
func (b *broker) rcEndSession(sid string) {
	// The terminal frame must reach viewers on EVERY instance, so fan it out through the same
	// path host frames take (bus in multi-instance, local hub otherwise).
	b.rcFanOut(sid, b.rcHubFor(sid), protocol.RCFrame{Kind: protocol.RCKindEnded})
	b.rcDropHub(sid)
}

// rcMultiInstance reports whether cross-instance RC relay is active (the flag AND a live bus).
func (b *broker) rcMultiInstance() bool { return b.multiInstance && b.shared != nil }

// rcFanOut delivers a host frame to viewers. Multi-instance: assign a SHARED seq (so viewers on
// any instance order identically) and publish to the bus. Single-instance: the local hub
// assigns the seq, records the ring, and notifies local viewers. In multi-instance a viewer
// re-backfills on connect, so we don't serve ring replay cross-instance (the broker never
// persists the transcript).
func (b *broker) rcFanOut(sid string, h *rcHub, f protocol.RCFrame) {
	if !b.rcMultiInstance() {
		h.publish(f)
		return
	}
	if seq, err := b.shared.busNextRCSeq(sid); err == nil {
		f.Seq = seq
	}
	if f.TS == 0 {
		f.TS = time.Now().Unix()
	}
	raw, _ := json.Marshal(f)
	_ = b.shared.busPublishRCOut(sid, raw)
}

// rcDeliverInbound routes a viewer inbound (turn/confirm/backfill) to the host. Multi-instance:
// publish to the session's inbound bus channel (the host's poll — on any instance — is
// subscribed). Single-instance: hand it to the local hub's poll channel (non-blocking; an
// offline host simply misses it, exactly as before).
func (b *broker) rcDeliverInbound(sid string, h *rcHub, in protocol.RCInbound) {
	if b.rcMultiInstance() {
		raw, _ := json.Marshal(in)
		_ = b.shared.busPublishRCIn(sid, raw)
		return
	}
	select {
	case h.in <- in:
	default:
	}
}

// rcSubtree dispatches /rc/{sid}/{send|stream|poll|events|code|disable}.
func (b *broker) rcSubtree(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/rc/")
	sid, action, _ := strings.Cut(rest, "/")
	if sid == "" {
		jsonErr(w, http.StatusNotFound, "no such session")
		return
	}
	switch action {
	case "poll":
		b.rcPoll(w, r, sid)
	case "events":
		b.rcEvents(w, r, sid)
	case "send":
		b.rcSend(w, r, sid)
	case "stream":
		b.rcStream(w, r, sid)
	case "join":
		b.rcJoin(w, r, sid)
	case "code":
		b.rcRotateCode(w, r, sid)
	case "disable":
		b.rcDisable(w, r, sid)
	default:
		jsonErr(w, http.StatusNotFound, "no such session endpoint")
	}
}

// rcJoin handles POST /rc/{sid}/join (OWNER, no code): mint a per-device attach token for one
// of the caller's OWN sessions. The link code is only needed to link a NOT-logged-in device
// (a phone via QR); an already-logged-in same-account surface (another roger, the web console)
// attaches to its own session by id. Wrong account / unknown session gets the uniform 404.
func (b *broker) rcJoin(w http.ResponseWriter, r *http.Request, sid string) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	wallet, deviceLabel, ok := b.rcOwnerWallet(r, nil)
	uniform := func() { writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such session"}) }
	if !ok {
		uniform()
		return
	}
	sess, found, _ := b.db.RCSessionByID(sid)
	if !found || sess.Revoked || sess.OwnerWallet != wallet {
		uniform()
		return
	}
	attach := rcRandToken("rc_at_")
	if err := b.db.PutRCAttachToken(store.RCAttachToken{
		Hash: rcHash(attach), SessionID: sess.ID, DeviceLabel: deviceLabel,
	}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sess.ID, "name": sess.Name, "attach_token": attach})
}

// rcAuthHost verifies the Bearer host token against the session's stored hash (constant-time).
func (b *broker) rcAuthHost(r *http.Request, sess store.RCSession) bool {
	a := r.Header.Get("Authorization")
	if len(a) < 8 || a[:7] != "Bearer " {
		return false
	}
	got := rcHash(a[7:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(sess.HostTokenHash)) == 1
}

// rcAuthViewer verifies the caller owns the session AND presents a valid attach bearer bound
// to THIS session. Returns the device label for origin tagging.
func (b *broker) rcAuthViewer(r *http.Request, body []byte, sess store.RCSession) (label string, ok bool) {
	wallet, _, wok := b.rcOwnerWallet(r, body)
	if !wok || wallet != sess.OwnerWallet {
		return "", false
	}
	a := r.Header.Get("X-Roger-Attach")
	if a == "" {
		if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
			a = h[7:]
		}
	}
	t, found, _ := b.db.RCAttachTokenByHash(rcHash(a))
	if !found || t.SessionID != sess.ID {
		return "", false
	}
	return t.DeviceLabel, true
}

// rcPoll handles GET /rc/{sid}/poll (host, Bearer host token): 25s long-poll for inbound.
func (b *broker) rcPoll(w http.ResponseWriter, r *http.Request, sid string) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	sess, found, _ := b.db.RCSessionByID(sid)
	if !found || sess.Revoked || !b.rcAuthHost(r, sess) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Touch last-seen so the roster shows online (throttled write is fine; keep it simple).
	sess.LastHostSeen = time.Now().Unix()
	_ = b.db.UpdateRCSession(sess)
	h := b.rcHubFor(sid)
	h.markHost(true)

	// MULTI-INSTANCE: a viewer's inbound may have been published on a PEER instance, so for the
	// life of this long-poll also subscribe to the session's inbound bus channel. The local
	// h.in is still drained (mixed-mode safety across a flag flip). On a bus-subscribe error we
	// fall through to a 204 re-poll (no inbound is lost — the sender's publish reports 0
	// subscribers and the viewer's send simply doesn't reach an offline host, as before).
	if b.rcMultiInstance() {
		busIn, cancel, err := b.shared.busSubscribeRCIn(r.Context(), sid)
		if err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		defer cancel()
		// Drain one inbound a viewer sent during the poll gap: while no poll was subscribed the
		// PUBLISH reached 0 receivers (dropped), so busPublishRCIn buffered it (audit #5). We
		// SUBSCRIBE first (above) THEN pop: Redis serializes our SUBSCRIBE ahead of this pop, so
		// any earlier publish is already listed (we pop it) and any later one arrives live on
		// busIn - lossless, no duplicate. One per poll (the client re-polls for the rest).
		if raw, ok, _ := b.shared.busPopRCIn(sid); ok {
			var in protocol.RCInbound
			if json.Unmarshal(raw, &in) == nil {
				_ = json.NewEncoder(w).Encode(in)
				return
			}
		}
		select {
		case msg := <-h.in:
			_ = json.NewEncoder(w).Encode(msg)
		case raw, ok := <-busIn:
			if !ok {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var in protocol.RCInbound
			if json.Unmarshal(raw, &in) != nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(in)
		case <-time.After(rcPollHold):
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
		return
	}

	select {
	case msg := <-h.in:
		_ = json.NewEncoder(w).Encode(msg)
	case <-time.After(rcPollHold):
		w.WriteHeader(http.StatusNoContent) // re-poll
	case <-r.Context().Done():
		// the host disconnected (Stop / quit); release the handler at once rather than
		// holding it for the full poll window.
	}
}

// rcEvents handles POST /rc/{sid}/events (host): a batch of RCFrames to fan out to viewers.
func (b *broker) rcEvents(w http.ResponseWriter, r *http.Request, sid string) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	sess, found, _ := b.db.RCSessionByID(sid)
	if !found || sess.Revoked || !b.rcAuthHost(r, sess) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var frames []protocol.RCFrame
	if err := json.Unmarshal(body, &frames); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad frames")
		return
	}
	h := b.rcHubFor(sid)
	for _, f := range frames {
		if len(f.Text) > rcMaxFrameLen {
			f.Text = f.Text[:rcMaxFrameLen]
		}
		b.rcFanOut(sid, h, f)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// rcSend handles POST /rc/{sid}/send (viewer): an RCInbound (turn/confirm/interrupt) → the
// host, plus an echoed `user` frame to all viewers (so every surface sees the interleaved turn).
func (b *broker) rcSend(w http.ResponseWriter, r *http.Request, sid string) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, rcMaxFrameLen))
	sess, found, _ := b.db.RCSessionByID(sid)
	if !found || sess.Revoked {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	label, ok := b.rcAuthViewer(r, body, sess)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Per-owner rate limit on sends (abuse control; RC is $0 so pricing can't bound it).
	if b.shared != nil {
		if allowed, _, _ := b.shared.rateAllow("rc:"+sess.OwnerWallet, 120, 40, time.Now()); !allowed {
			jsonErr(w, http.StatusTooManyRequests, "slow down")
			return
		}
	}
	var in protocol.RCInbound
	if err := json.Unmarshal(body, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad message")
		return
	}
	in.Origin = label
	in.TS = time.Now().Unix()
	h := b.rcHubFor(sid)
	// Echo a user frame for a turn so every viewer sees who typed what.
	if in.Kind == protocol.RCInTurn && in.Text != "" {
		b.rcFanOut(sid, h, protocol.RCFrame{Kind: protocol.RCKindUser, Origin: label, Text: in.Text})
	}
	b.rcDeliverInbound(sid, h, in)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// rcStream handles GET /rc/{sid}/stream (viewer, SSE): fan-out of RCFrames + Last-Event-ID
// replay from the ring. Triggers a backfill request to the host on first connect.
func (b *broker) rcStream(w http.ResponseWriter, r *http.Request, sid string) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodGet) {
		return
	}
	sess, found, _ := b.db.RCSessionByID(sid)
	if !found || sess.Revoked {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	label, ok := b.rcAuthViewer(r, nil, sess)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	var since uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		since, _ = strconv.ParseUint(v, 10, 64)
	}
	h := b.rcHubFor(sid)
	viewerID := label + ":" + rcRandToken("")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()

	// MULTI-INSTANCE: the host may be posting frames to a PEER instance, so subscribe to the
	// session's frame bus channel. Cross-instance we don't serve ring replay (the host
	// re-backfills on connect); Last-Event-ID is best-effort. Single-instance keeps the local
	// hub ring + viewer channel exactly as before.
	if b.rcMultiInstance() {
		busOut, cancel, err := b.shared.busSubscribeRCOut(ctx, sid)
		if err != nil {
			return
		}
		defer cancel()
		// Ask the host for a snapshot addressed to this viewer (over the inbound bus).
		b.rcDeliverInbound(sid, h, protocol.RCInbound{Kind: protocol.RCInBackfill, Viewer: viewerID, TS: time.Now().Unix()})
		for {
			select {
			case <-ctx.Done():
				return
			case raw, ok := <-busOut:
				if !ok {
					return
				}
				var f protocol.RCFrame
				if json.Unmarshal(raw, &f) != nil {
					continue
				}
				if f.Kind == protocol.RCKindBackfill && f.Viewer != "" && f.Viewer != viewerID {
					continue
				}
				rcWriteSSE(w, flusher, f)
				if f.Kind == protocol.RCKindEnded {
					return
				}
			}
		}
	}

	ch, unsub := h.subscribe(viewerID, since)
	defer unsub()
	// Ask the host for a transcript snapshot addressed to this viewer (content-blind: the
	// broker never has the history; the host serves it).
	select {
	case h.in <- protocol.RCInbound{Kind: protocol.RCInBackfill, Viewer: viewerID, TS: time.Now().Unix()}:
	default:
	}
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-ch:
			// A backfill frame is addressed to ONE viewer; others skip it.
			if f.Kind == protocol.RCKindBackfill && f.Viewer != "" && f.Viewer != viewerID {
				continue
			}
			rcWriteSSE(w, flusher, f)
			if f.Kind == protocol.RCKindEnded {
				return
			}
		}
	}
}

// rcWriteSSE writes one RCFrame as an SSE event (id: <seq>\ndata: <json>\n\n) and flushes.
func rcWriteSSE(w http.ResponseWriter, flusher http.Flusher, f protocol.RCFrame) {
	buf, _ := json.Marshal(f)
	_, _ = w.Write([]byte("id: " + strconv.FormatUint(f.Seq, 10) + "\ndata: "))
	_, _ = w.Write(buf)
	_, _ = w.Write([]byte("\n\n"))
	flusher.Flush()
}

// rcRotateCode handles POST /rc/{sid}/code (owner): mint a fresh link code (10-min window),
// retiring the old one. Returns the code ONCE.
func (b *broker) rcRotateCode(w http.ResponseWriter, r *http.Request, sid string) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	// Uniform 404 for BOTH a nonexistent session and a foreign/revoked one, matching the rest
	// of the RC surface (rcAttach/rcSend/rcStream) - a cross-account caller must not be able to
	// tell an existing-but-not-yours session from a nonexistent one (audit finding #10). The
	// owner check short-circuits so sess is only read when found.
	sess, found, _ := b.db.RCSessionByID(sid)
	wallet, _, ok := b.rcOwnerWallet(r, nil)
	if !(found && !sess.Revoked && ok && wallet == sess.OwnerWallet) {
		jsonErr(w, http.StatusNotFound, "no such session")
		return
	}
	code, display, tail := protocol.NewRCLinkCode()
	sess.CodeHash = protocol.BandCodeHash(tail)
	sess.CodeDisplay = display
	sess.CodeExpires = time.Now().Add(store.RCCodeTTL).Unix()
	if err := b.db.UpdateRCSession(sess); err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": code, "code_short": protocol.RCLinkShort(code), "code_display": display,
		"code_expires": sess.CodeExpires,
	})
}

// rcDisable handles POST /rc/{sid}/disable (owner or host): revoke the session + push ended.
func (b *broker) rcDisable(w http.ResponseWriter, r *http.Request, sid string) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodPost) {
		return
	}
	// The host (bearer) OR the owner may disable. Uniform 404 for a nonexistent session AND a
	// foreign one (no existence oracle, audit finding #10); the auth checks short-circuit so
	// sess is only read when found.
	sess, found, _ := b.db.RCSessionByID(sid)
	wallet, _, wok := b.rcOwnerWallet(r, nil)
	if !(found && (b.rcAuthHost(r, sess) || (wok && wallet == sess.OwnerWallet))) {
		jsonErr(w, http.StatusNotFound, "no such session")
		return
	}
	sess.Revoked = true
	sess.CodeExpires = 0
	sess.CodeHash = ""
	_ = b.db.UpdateRCSession(sess)
	_, _ = b.db.RevokeRCSessions(sess.OwnerWallet) // drops attach tokens for the owner's revoked sessions
	b.rcEndSession(sid)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "revoked": true})
}

// rcGCOnce garbage-collects sessions idle past RCIdleGC. Called from the existing sweep loop.
func (b *broker) rcGCOnce(now time.Time) {
	// The store has no "all sessions" scan by design (owner-scoped), so GC is best-effort
	// over live hubs: a hub with no recent host poll AND a stale roster row is dropped. The
	// durable roster is cleaned lazily on the owner's next list (revoked rows filtered). For
	// the in-memory path this simply reclaims idle hubs.
	b.rcMu.Lock()
	defer b.rcMu.Unlock()
	for sid, h := range b.rcHubs {
		h.mu.Lock()
		idle := now.Sub(h.lastHost)
		h.mu.Unlock()
		if idle > store.RCIdleGC {
			delete(b.rcHubs, sid)
		}
	}
}
