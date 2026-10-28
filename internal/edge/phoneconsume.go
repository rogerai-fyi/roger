package edge

// A PHONE CONSUMES THE EDGE'S BANDS (features/edge/phone_consume.feature; founder ruling 2026-10-13,
// "we should support that").
//
// A member asks another member's face for a turn over mutual TLS (infer.go). A phone cannot: its
// node identity is an Ed25519 key kept by CryptoKit, and Apple's Security framework has no SecKey
// type for Ed25519, so no SecIdentity can be built on iOS to present as a client certificate. Nor
// can the phone count on finishing a TLS handshake with a face whose own certificate is an Ed25519
// leaf. So the phone asks the AUTHORITY, on the listener it already reaches for presence, and proves
// itself the way presence does: its member certificate travels with the request, and the request is
// signed by that certificate's key over a canonical form that covers a hash of the body.
//
// THE AUTHORITY RELAYS. It checks the phone (fresh, under this root and not revoked - read fresh - the
// id its key derives, a member of this fleet, the signature, a nonce not seen), finds the member that
// serves the band, and relays the very bytes the phone signed to that member's face over its own
// pinned mutual TLS. With them go the phone's proof and the authority's RELAY ATTESTATION: the Edge
// root's signature over the face's node id and the phone's signature. A face credits a turn to a
// phone only with that attestation naming ITSELF (onBehalf), so a member that captures a relayed turn
// cannot replay it to another face, and only the authority, the one holder of the root key, can
// relay at all. A face from before this reads nothing new and receipts the turn to the authority.
//
// TRANSPORT (ruled 2026-10-13, docs/edge-authority-https.md). These paths are served only on the
// authority's TLS listener, under a server certificate the Edge root issues; the plain listener
// refuses them (cmd/rogerai/edgeauthtls.go), with or without a TLS listener up. The request itself
// cannot be forged or altered (the signature covers its body, target and action), and it is refused
// if replayed to the authority or relayed to a face it was not made for. A restart of the authority
// empties its nonce cache, so within five minutes a captured request can be presented once more to
// that restarted authority.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/cert"
)

// The phone's paths on the authority listener.
const (
	PhoneBandsPath  = "/edge/phone/bands"
	PhoneInferPath  = "/edge/phone/infer"
	PhoneSpeechPath = "/edge/phone/speech"
)

// consumePrefix domain-separates a consume signature from presence, claim, enrol and trouble.
const consumePrefix = "rogerai-edge-infer/v1"

// relayPrefix domain-separates the authority's relay attestation from every other root signature.
const relayPrefix = "rogerai-edge-relay/v1"

// The actions a consume signature is bound to, one per path, so a signature made to list the bands
// cannot be presented as a turn.
const (
	ActionBands  = "bands"
	ActionInfer  = "infer"
	ActionSpeech = "speech"
)

// The proof travels in headers, so the body is the exact OpenAI-shaped body /edge/infer takes.
const (
	HeaderNode   = "Roger-Node"   // the caller's node id
	HeaderCert   = "Roger-Cert"   // base64 (standard, padded) of the caller's member certificate DER
	HeaderTarget = "Roger-Target" // optional: the member to serve it, as `roger use` names one (node or node/instance)
	HeaderNonce  = "Roger-Nonce"  // hex, 16 random bytes
	HeaderTS     = "Roger-TS"     // unix seconds
	HeaderSig    = "Roger-Sig"    // hex Ed25519 signature over ConsumeCanonical, by the certificate's key
	// HeaderBodySHA256 is REQUIRED on every /edge/phone/* request: the lowercase hex SHA-256 of the
	// exact body (of "" for the band list). It lets the signature be checked before a byte of the
	// body is read; the body is then held to it.
	HeaderBodySHA256 = "Roger-Body-SHA256"
	// HeaderRelay is set by the AUTHORITY on a relayed turn, never by a phone: base64 of the Edge
	// root's ECDSA (ASN.1) signature over SHA-256 of RelayCanonical(face node id, Roger-Sig).
	HeaderRelay = "Roger-Relay"
)

// phoneBodyDeadline bounds how long a phone may take to send its body: a slow sender does not hold a
// relay slot for the length of a stream.
const phoneBodyDeadline = 30 * time.Second

// phoneMaxInFlight bounds the phone requests the authority serves at once, and phoneMaxPerNode what
// one member may hold of them, so one phone's ten-minute streams cannot hold every slot.
const (
	phoneMaxInFlight = 8
	phoneMaxPerNode  = 2
)

// errNotVerified is the ONE refusal for every way a caller's identity can fail - no certificate, not
// this root, revoked, expired, a key that does not derive the id, not in this fleet, a bad signature -
// so a captured certificate without its key cannot tell a live member from a removed one (audit
// 2026-10-13).
const errNotVerified = "that request could not be verified as a member of this Edge"

// ConsumeCanonical is exactly what a phone signs: the domain, its node id, the action, the target
// ("" for any member), the lowercase hex SHA-256 of the exact body bytes, the nonce and the time,
// joined by "\n". Golden vectors: internal/edge/testdata/infer_sig_vectors.json.
func ConsumeCanonical(nodeID, action, target, bodySHA256, nonce string, ts int64) []byte {
	return []byte(strings.Join([]string{
		consumePrefix, nodeID, action, target, bodySHA256, nonce, strconv.FormatInt(ts, 10),
	}, "\n"))
}

// RelayCanonical is what the authority's root signs when it relays a phone's turn to one face.
func RelayCanonical(faceNodeID, phoneSigHex string) []byte {
	return []byte(relayPrefix + "\n" + faceNodeID + "\n" + phoneSigHex)
}

// BodyHash is the lowercase hex SHA-256 a consume signature covers.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// memberProof is one signed consume request, as its headers carry it.
type memberProof struct {
	NodeID, Cert, Target, Nonce, Sig, BodySHA string
	TS                                        int64
	tsOK                                      bool
}

func proofFrom(h http.Header) memberProof {
	p := memberProof{NodeID: h.Get(HeaderNode), Cert: h.Get(HeaderCert), Target: h.Get(HeaderTarget),
		Nonce: h.Get(HeaderNonce), Sig: h.Get(HeaderSig), BodySHA: h.Get(HeaderBodySHA256)}
	ts, err := strconv.ParseInt(strings.TrimSpace(h.Get(HeaderTS)), 10, 64)
	p.TS, p.tsOK = ts, err == nil
	return p
}

// hasProof reports whether a request carries a consume proof at all.
func hasProof(h http.Header) bool { return h.Get(HeaderSig) != "" || h.Get(HeaderRelay) != "" }

func (p memberProof) set(h http.Header) {
	h.Set(HeaderNode, p.NodeID)
	h.Set(HeaderCert, p.Cert)
	if p.Target != "" {
		h.Set(HeaderTarget, p.Target)
	}
	h.Set(HeaderNonce, p.Nonce)
	h.Set(HeaderTS, strconv.FormatInt(p.TS, 10))
	h.Set(HeaderSig, p.Sig)
	if p.BodySHA != "" {
		h.Set(HeaderBodySHA256, p.BodySHA)
	}
}

func (p memberProof) fresh(now time.Time) bool {
	d := now.Sub(time.Unix(p.TS, 0))
	return p.tsOK && d <= presenceFreshness && d >= -presenceFreshness
}

// leaf parses the proof's certificate and checks its key derives the id it claims. It needs no trust.
func (p memberProof) leaf() (*x509.Certificate, ed25519.PublicKey, bool) {
	der, err := base64.StdEncoding.DecodeString(p.Cert)
	if err != nil || len(der) == 0 {
		return nil, nil, false
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, false
	}
	pub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok || NodeID(pub) != p.NodeID {
		return nil, nil, false
	}
	return leaf, pub, true
}

func (p memberProof) signed(pub ed25519.PublicKey, action string, body []byte) bool {
	return p.signedOver(pub, action, BodyHash(body))
}

// signedOver checks the signature over a body hash, so it can run before the body is read.
func (p memberProof) signedOver(pub ed25519.PublicKey, action, bodySHA string) bool {
	sig, err := hex.DecodeString(p.Sig)
	return err == nil && ed25519.Verify(pub, ConsumeCanonical(p.NodeID, action, p.Target, bodySHA, p.Nonce, p.TS), sig)
}

// isLowerHex reports whether s is exactly n lowercase hex characters.
func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// verifyRelay checks the authority's attestation that it relayed this phone's turn to THIS face.
func verifyRelay(root *x509.Certificate, faceID string, h http.Header) bool {
	pub, ok := root.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(h.Get(HeaderRelay))
	if err != nil || len(sig) == 0 {
		return false
	}
	sum := sha256.Sum256(RelayCanonical(faceID, h.Get(HeaderSig)))
	return ecdsa.VerifyASN1(pub, sum[:], sig)
}

// onBehalf is the face's side of a relayed turn (infer.go, speech.go). A turn with no proof is the
// caller's own. One with a proof is credited to the phone only when the Edge's root vouches that the
// authority relayed it to THIS face, and the phone's own proof holds here too: fresh, signed over
// these very bytes, its certificate under the root this face trusts now, its nonce new here. The
// authority checked that the phone is a member of the Edge's fleet before it signed the attestation;
// a phone dials only its authority, so it is in no other member's fleet to check.
func (s *Server) onBehalf(r *http.Request, action string, body []byte, relayer string) (string, int, string) {
	if !hasProof(r.Header) {
		return relayer, 0, ""
	}
	trust := s.serving.Trust
	if s.serving.TrustNow != nil {
		if trust = s.serving.TrustNow(); trust == nil {
			return "", http.StatusServiceUnavailable, "this node cannot verify callers right now"
		}
	}
	if trust == nil || !verifyRelay(trust.Root(), s.serving.NodeID, r.Header) {
		return "", http.StatusForbidden, "a relayed turn needs this Edge's authority to vouch for it, to this node"
	}
	p := proofFrom(r.Header)
	if !p.fresh(s.now()) {
		return "", http.StatusForbidden, "that request is stale: check this device's clock"
	}
	leaf, pub, ok := p.leaf()
	if !ok || !p.signed(pub, action, body) || (p.BodySHA != "" && p.BodySHA != BodyHash(body)) {
		return "", http.StatusForbidden, errNotVerified
	}
	if id, err := trust.Authenticate(leaf); err != nil || id != p.NodeID {
		return "", http.StatusForbidden, errNotVerified
	}
	if !s.phoneSeen.admit(p.NodeID, p.Nonce, time.Unix(p.TS, 0).Add(presenceFreshness).Unix(), s.now().Unix()) {
		return "", http.StatusForbidden, orReplay(s.phoneSeen.refusal(p.NodeID, p.Nonce, s.now().Unix()), "that request was already seen")
	}
	return p.NodeID, 0, ""
}

// PhoneConsume is what the authority needs to serve a phone the Edge's bands.
type PhoneConsume struct {
	Fleet *Fleet
	// Trust returns a VERIFY-ONLY authority for this Edge, built per request from the root
	// certificate and the revocation list as they are now, so a node revoked while this runs is
	// refused at once. nil (it cannot be read) refuses every request.
	Trust func() *cert.Authority
	// Member reports whether a node is a member of this Edge (in this authority's fleet).
	Member func(nodeID string) bool
	// Self is this machine's own member identity, presented to the member a turn is relayed to.
	Self func() *tls.Certificate
	// Root signs the relay attestation a face checks: the Edge root's private key, which only the
	// authority holds.
	Root crypto.Signer
	Now  func() time.Time
}

// PhoneConsumeHandler serves PhoneBandsPath, PhoneInferPath and PhoneSpeechPath.
func PhoneConsumeHandler(pc PhoneConsume) http.Handler {
	if pc.Now == nil {
		pc.Now = time.Now
	}
	h := &phoneConsume{PhoneConsume: pc, seen: newNonceCache(), perNode: map[string]int{}, mux: http.NewServeMux()}
	h.mux.HandleFunc(PhoneBandsPath, h.bands)
	h.mux.HandleFunc(PhoneInferPath, func(w http.ResponseWriter, r *http.Request) { h.relay(w, r, ActionInfer) })
	h.mux.HandleFunc(PhoneSpeechPath, func(w http.ResponseWriter, r *http.Request) { h.relay(w, r, ActionSpeech) })
	return h
}

// ServeHTTP routes to the three phone paths.
func (h *phoneConsume) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

type phoneConsume struct {
	PhoneConsume
	mux      *http.ServeMux
	seen     *nonceCache
	mu       sync.Mutex
	inFlight int
	perNode  map[string]int
}

// admit settles who is asking from the headers alone - fresh, signed by the key the certificate names
// over the body hash the headers declare, under this root and not revoked, in this fleet - BEFORE it
// takes a slot or reads a byte of the body (audit 2026-10-13: a sniffed certificate alone, which
// travels in the clear, held every slot with trickled bodies). Only then does it take a slot (eight in
// all, two per member), spend the nonce (after the slot, so a busy answer can be retried as it was),
// read the body bounded in size and time, and hold it to the declared hash. Every identity failure, and a body that does not match, is the same 403. It writes
// every refusal itself; the caller must call release when ok.
func (h *phoneConsume) admit(w http.ResponseWriter, r *http.Request, action string, limit int64, tooBig string) (memberProof, []byte, func(), bool) {
	refuse := func(status int, msg string) (memberProof, []byte, func(), bool) {
		inferError(w, status, msg)
		return memberProof{}, nil, nil, false
	}
	if r.Method != http.MethodPost {
		return refuse(http.StatusMethodNotAllowed, "method not allowed")
	}
	now := h.Now()
	p := proofFrom(r.Header)
	// 1. Fresh. This depends only on the time the request itself carries, so it tells nobody anything
	//    about who is enrolled, and it is the one refusal a phone with a wrong clock needs to read.
	if !p.fresh(now) {
		return refuse(http.StatusForbidden, "that request is stale: check this device's clock")
	}
	// 2. Signed, by the key its certificate names, over what its headers declare - nothing about the
	//    caller's standing is consulted before this, so every failure here and below reads the same.
	leaf, pub, okLeaf := p.leaf()
	if !okLeaf || !isLowerHex(p.BodySHA, 64) || !p.signedOver(pub, action, p.BodySHA) {
		return refuse(http.StatusForbidden, errNotVerified)
	}
	// 3. A member: under this root, not revoked, in this fleet.
	var trust *cert.Authority
	if h.Trust != nil {
		trust = h.Trust()
	}
	if trust == nil {
		return refuse(http.StatusServiceUnavailable, "this node cannot verify callers right now")
	}
	if id, err := trust.Authenticate(leaf); err != nil || id != p.NodeID || h.Member == nil || !h.Member(id) {
		return refuse(http.StatusForbidden, errNotVerified)
	}
	// 4a. A replay of a request already served is refused BEFORE it takes a slot, so replays (a face
	//     that relayed a turn holds its bytes) can never crowd the member's own turns out. Checked,
	//     not spent: the nonce is spent below, after the slot, so a busy answer can be retried.
	if h.seen.spent(p.NodeID, p.Nonce, now.Unix()) {
		return refuse(http.StatusForbidden, "that request was already seen")
	}
	// 4. A slot: two per member, eight in all. Released on every path out, a panic included, unless
	//    it is handed to the caller. Taken BEFORE the nonce is spent, so the busy answer's "try again
	//    shortly" is true for the very same signed request.
	if !h.take(p.NodeID) {
		w.Header().Set("Retry-After", "2")
		return refuse(http.StatusServiceUnavailable, "this Edge is answering as many phone requests as it can; try again shortly")
	}
	handed := false
	release := func() { h.give(p.NodeID) }
	defer func() {
		if !handed {
			release()
		}
	}()
	// 5. Not a replay. Only the holder of the key gets this far, and a replay only ever holds one of
	//    its own member's slots for this check.
	if !h.seen.admit(p.NodeID, p.Nonce, time.Unix(p.TS, 0).Add(presenceFreshness).Unix(), now.Unix()) {
		return refuse(http.StatusForbidden, orReplay(h.seen.refusal(p.NodeID, p.Nonce, now.Unix()), "that request was already seen"))
	}
	// 6. The body, bounded in size and in time, and held to the hash that was signed.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(now.Add(phoneBodyDeadline))
	var body []byte
	if limit > 0 {
		b, err := readAllLimited(w, r, limit)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return refuse(http.StatusRequestEntityTooLarge, tooBig)
			}
			return refuse(http.StatusBadRequest, "that request's body could not be read")
		}
		body = b
	} else if n, _ := r.Body.Read(make([]byte, 1)); n > 0 {
		return refuse(http.StatusBadRequest, tooBig)
	}
	_ = rc.SetReadDeadline(time.Time{})
	if BodyHash(body) != p.BodySHA {
		return refuse(http.StatusForbidden, errNotVerified)
	}
	handed = true
	return p, body, release, true
}

// take claims a slot for node, or reports that none is free (all eight, or this node's two).
func (h *phoneConsume) take(node string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight >= phoneMaxInFlight || h.perNode[node] >= phoneMaxPerNode {
		return false
	}
	h.inFlight++
	h.perNode[node]++
	return true
}

func (h *phoneConsume) give(node string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inFlight--
	if h.perNode[node]--; h.perNode[node] <= 0 {
		delete(h.perNode, node)
	}
}

func readAllLimited(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(http.MaxBytesReader(w, r.Body, limit))
	return buf.Bytes(), err
}

// ConsumeServer is one member serving a band, as the phone's band list names it.
type ConsumeServer struct {
	Node     string `json:"node"`
	NodeID   string `json:"node_id"`
	Instance string `json:"instance"`
	Target   string `json:"target"` // what to send as Roger-Target to ask this one by name
}

// ConsumeBand is one band the Edge offers and who serves it.
type ConsumeBand struct {
	Band     string          `json:"band"`
	Voice    bool            `json:"voice"` // a text-to-speech voice: ask it at PhoneSpeechPath
	ServedBy []ConsumeServer `json:"served_by"`
}

// ConsumeBands is the answer to PhoneBandsPath.
type ConsumeBands struct {
	Bands []ConsumeBand `json:"bands"`
}

// consumable is the dispatch ladder's rule (cmd/rogerai edgePeersFor): an instance whose serve the
// probe VERIFIED for this band, on a node that is not dark and that this machine can dial and pin.
func consumable(l Located, band string) bool {
	return Presence(l.Node.Presence) != PresenceDark && l.ServesBand(band) && Controllable(l.Node)
}

func isVoice(in Instance, band string) bool {
	for _, v := range in.Voices {
		if v == band {
			return true
		}
	}
	return false
}

// offered is every band some member can serve a phone now, and who.
func (h *phoneConsume) offered() ([]ConsumeBand, error) {
	list, err := h.Fleet.List()
	if err != nil {
		return nil, err
	}
	byBand := map[string]*ConsumeBand{}
	for _, n := range list {
		for _, in := range n.Instances {
			for _, b := range in.Bands {
				l := Located{Node: n, Instance: in}
				if !consumable(l, b) {
					continue
				}
				cb := byBand[b]
				if cb == nil {
					cb = &ConsumeBand{Band: b, ServedBy: []ConsumeServer{}}
					byBand[b] = cb
				}
				if isVoice(in, b) {
					cb.Voice = true
				}
				cb.ServedBy = append(cb.ServedBy, ConsumeServer{Node: n.Name, NodeID: n.ID, Instance: in.Name, Target: l.Address()})
			}
		}
	}
	out := make([]ConsumeBand, 0, len(byBand))
	for _, b := range byBand {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Band < out[j].Band })
	return out, nil
}

// bands answers the phone's "what can I use here". It is asked with an empty body.
func (h *phoneConsume) bands(w http.ResponseWriter, r *http.Request) {
	_, _, release, ok := h.admit(w, r, ActionBands, 0, "a band list is asked with an empty body")
	if !ok {
		return
	}
	defer release()
	got, err := h.offered()
	if err != nil {
		inferError(w, http.StatusInternalServerError, "the fleet could not be read")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ConsumeBands{Bands: got})
}

// relay serves one turn (or one spoken line) for the phone through the member that serves the band.
func (h *phoneConsume) relay(w http.ResponseWriter, r *http.Request, action string) {
	limit, tooBig, path := int64(inferBodyCap), "request body exceeds the 4 MiB limit", InferPath
	if action == ActionSpeech {
		limit, tooBig, path = speechBodyCap, "speech request exceeds the 64 KiB limit", SpeechPath
	}
	p, body, release, ok := h.admit(w, r, action, limit, tooBig)
	if !ok {
		return
	}
	defer release()
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		inferError(w, http.StatusBadRequest, "a turn names the band it wants")
		return
	}
	loc, status, msg := h.locate(p.Target, req.Model, action == ActionSpeech)
	if status != 0 {
		inferError(w, status, msg)
		return
	}
	var self *tls.Certificate
	if h.Self != nil {
		self = h.Self()
	}
	selfID := certNodeID(self)
	if self == nil || selfID == "" || h.Root == nil {
		inferError(w, http.StatusServiceUnavailable, "this Edge's authority has no member identity to ask another member with")
		return
	}
	sum := sha256.Sum256(RelayCanonical(loc.Node.ID, p.Sig))
	attest, err := h.Root.Sign(rand.Reader, sum[:], crypto.SHA256)
	if err != nil {
		inferError(w, http.StatusServiceUnavailable, "this Edge's authority could not vouch for the turn")
		return
	}
	addr, pin := relayAddr(loc, selfID)
	if addr == "" {
		inferError(w, http.StatusBadGateway, loc.Node.Name+" has no address this Edge can reach")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inferTimeout)
	defer cancel()
	// X-Roger-Request is not forwarded: it is not signed, so the face names the turn itself.
	resp, err := dialMember(ctx, addr, pin, self, path, body, func(hd http.Header) {
		p.set(hd)
		hd.Set(HeaderRelay, base64.StdEncoding.EncodeToString(attest))
	})
	if err != nil {
		inferError(w, http.StatusBadGateway, loc.Node.Name+" did not answer")
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Content-Length", "X-RogerAI-Provider", "X-RogerAI-Cost", "X-RogerAI-Receipt"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// locate finds the member to serve band for a phone: the one it named, or the first that serves it.
func (h *phoneConsume) locate(target, band string, voice bool) (Located, int, string) {
	if target != "" {
		loc, _, err := h.Fleet.ServingPeer(target, band)
		switch {
		case err == nil:
		case errors.Is(err, ErrNoSuchNode), errors.Is(err, ErrNoSuchInstance):
			return Located{}, http.StatusNotFound, "no member of this Edge is called " + target
		case errors.Is(err, ErrAmbiguousNode):
			return Located{}, http.StatusConflict, err.Error() + ": name one by its id"
		case errors.Is(err, ErrNodeDark):
			return Located{}, http.StatusServiceUnavailable, target + " is dark: this Edge has not heard it lately"
		case errors.Is(err, ErrNoLANReach):
			return Located{}, http.StatusBadGateway, target + " has no address this Edge can reach"
		default: // serves no model, not this one, or only claims to
			return Located{}, http.StatusNotFound, err.Error()
		}
		if !consumable(loc, band) {
			return Located{}, http.StatusNotFound, target + " is not proven to serve " + band
		}
		if voice && !isVoice(loc.Instance, band) {
			return Located{}, http.StatusBadRequest, band + " is not a voice on " + target
		}
		return loc, 0, ""
	}
	who, err := h.Fleet.ServersOf(band)
	if err != nil {
		return Located{}, http.StatusInternalServerError, "the fleet could not be read"
	}
	for _, l := range who {
		if consumable(l, band) && (!voice || isVoice(l.Instance, band)) {
			return l, 0, ""
		}
	}
	names := []string{}
	if got, err := h.offered(); err == nil {
		for _, b := range got {
			if !voice || b.Voice {
				names = append(names, b.Band)
			}
		}
	}
	if len(names) == 0 {
		names = []string{"nothing"}
	}
	what := "serves"
	if voice {
		what = "speaks with"
	}
	return Located{}, http.StatusNotFound, "no member of this Edge " + what + " " + band + "; it offers: " + strings.Join(names, ", ")
}

func lanTransport(l Located) (addr, pin string) {
	for _, t := range l.Node.Transports {
		if t.Kind == "lan" && t.Addr != "" && t.Fingerprint != "" {
			return t.Addr, t.Fingerprint
		}
	}
	return "", ""
}

// relayAddr is where to dial the member: its recorded LAN face, turned to loopback only when the
// member is this machine itself. Another member recorded at a wildcard, loopback or link-local address
// is not dialed at all ("" here): the first two are THIS machine as the system resolves them, and a
// link-local address names no member reachable without a zone. (The pin would refuse the answer
// anyway; this does not get as far as asking.)
func relayAddr(loc Located, selfID string) (addr, pin string) {
	addr, pin = lanTransport(loc)
	if loc.Node.ID == selfID {
		return loopback(addr), pin
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || host == "" || (ip != nil && (ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast())) {
		return "", pin
	}
	return addr, pin
}

// certNodeID is the node id a member certificate's key derives, or "".
func certNodeID(c *tls.Certificate) string {
	if c == nil || len(c.Certificate) == 0 {
		return ""
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return ""
	}
	pub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return NodeID(pub)
}

// loopback turns THIS machine's own face address, recorded as it listens ("[::]:port"), into one to
// dial. It is applied only to the authority's own node: a member that advertises a wildcard address
// is dialed as advertised, never redirected to this machine (audit 2026-10-13).
func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && (ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast())) {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// dialMember is one pinned, mutually authenticated POST to a member's face: the member's certificate
// must hash to the pin the fleet recorded before a byte of the body is sent, and this machine
// presents its own member certificate.
func dialMember(ctx context.Context, addr, pin string, self *tls.Certificate, path string, body []byte, headers func(http.Header)) (*http.Response, error) {
	cfg := &tls.Config{
		InsecureSkipVerify: true, // verified below, against the pin, before any byte is sent
		MinVersion:         tls.VersionTLS12,
		Certificates:       []tls.Certificate{*self},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("the member presented no certificate")
			}
			if Fingerprint(cs.PeerCertificates[0].Raw) != pin {
				return fmt.Errorf("the member's certificate does not match its pin")
			}
			return nil
		},
	}
	tr := &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true,
		DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	headers(req.Header)
	return (&http.Client{Transport: tr}).Do(req)
}
