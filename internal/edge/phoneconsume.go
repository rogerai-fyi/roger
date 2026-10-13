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
// WHAT THIS DOES NOT PROTECT, SAID OUT LOUD (audit 2026-10-13). The authority listener is plain HTTP.
//   - Confidentiality: a prompt and its answer cross the LAN between the phone and the authority in
//     the clear. Anyone on that network can read them.
//   - Reply integrity: nothing authenticates the REPLY. A device in the path can rewrite an answer, a
//     band list or a receipt header, and the phone cannot tell.
//   - Authority authenticity: the phone reaches the authority by an address it was given or saw
//     advertised; an impostor answering there (mDNS spoofing) can read the turn and answer it.
//   - The request itself cannot be forged or altered (the signature covers its body, target and
//     action), and it is refused if replayed to the authority or relayed to a face it was not made for.
//     Within five minutes a captured request can still be presented ONCE where it has not been seen,
//     which the authority's nonce cache refuses; a restart of the authority empties that cache.
// The fix is docs/edge-authority-https.md, pending the founder's ruling.

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
	"time"

	"rogerai.fm/roger/v6/internal/towercore/cert"
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
	// HeaderRelay is set by the AUTHORITY on a relayed turn, never by a phone: base64 of the Edge
	// root's ECDSA (ASN.1) signature over SHA-256 of RelayCanonical(face node id, Roger-Sig).
	HeaderRelay = "Roger-Relay"
)

// phoneBodyDeadline bounds how long a phone may take to send its body: a slow sender does not hold a
// relay slot for the length of a stream.
const phoneBodyDeadline = 30 * time.Second

// phoneMaxInFlight bounds the phone requests the authority serves at once.
const phoneMaxInFlight = 8

// errNotVerified is the ONE refusal for every way a caller's identity can fail - no certificate, not
// this root, revoked, expired, a key that does not derive the id, not in this fleet, a bad signature -
// so a captured certificate without its key cannot tell a live member from a removed one (audit
// 2026-10-13). The authority-side trouble feed (f3cec7d1, not on this branch) still answers these
// apart and checks the certificate before the signature; it should take the same order and words.
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
	NodeID, Cert, Target, Nonce, Sig string
	TS                               int64
	tsOK                             bool
}

func proofFrom(h http.Header) memberProof {
	p := memberProof{NodeID: h.Get(HeaderNode), Cert: h.Get(HeaderCert), Target: h.Get(HeaderTarget),
		Nonce: h.Get(HeaderNonce), Sig: h.Get(HeaderSig)}
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
	sig, err := hex.DecodeString(p.Sig)
	return err == nil && ed25519.Verify(pub, ConsumeCanonical(p.NodeID, action, p.Target, BodyHash(body), p.Nonce, p.TS), sig)
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
	if !ok || !p.signed(pub, action, body) {
		return "", http.StatusForbidden, errNotVerified
	}
	if id, err := trust.Authenticate(leaf); err != nil || id != p.NodeID {
		return "", http.StatusForbidden, errNotVerified
	}
	if !s.phoneSeen.admit(p.NodeID, p.Nonce, time.Unix(p.TS, 0).Add(presenceFreshness).Unix(), s.now().Unix()) {
		return "", http.StatusForbidden, "that request was already seen"
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
	h := &phoneConsume{PhoneConsume: pc, seen: newNonceCache(), slots: make(chan struct{}, phoneMaxInFlight)}
	mux := http.NewServeMux()
	mux.HandleFunc(PhoneBandsPath, h.bands)
	mux.HandleFunc(PhoneInferPath, func(w http.ResponseWriter, r *http.Request) { h.relay(w, r, ActionInfer) })
	mux.HandleFunc(PhoneSpeechPath, func(w http.ResponseWriter, r *http.Request) { h.relay(w, r, ActionSpeech) })
	return mux
}

type phoneConsume struct {
	PhoneConsume
	seen  *nonceCache
	slots chan struct{}
}

// admit settles who is asking before the body is read, then reads the body and checks the
// signature over it, writing every refusal itself. The caller must call release when ok.
func (h *phoneConsume) admit(w http.ResponseWriter, r *http.Request, action string, limit int64, tooBig string) (memberProof, []byte, func(), bool) {
	if r.Method != http.MethodPost {
		inferError(w, http.StatusMethodNotAllowed, "method not allowed")
		return memberProof{}, nil, nil, false
	}
	select {
	case h.slots <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "2")
		inferError(w, http.StatusServiceUnavailable, "this Edge is answering as many phone requests as it can; try again shortly")
		return memberProof{}, nil, nil, false
	}
	release := func() { <-h.slots }
	fail := func(status int, msg string) (memberProof, []byte, func(), bool) {
		release()
		inferError(w, status, msg)
		return memberProof{}, nil, nil, false
	}
	now := h.Now()
	p := proofFrom(r.Header)
	// 1. Fresh, before anything that depends on who is asking.
	if !p.fresh(now) {
		return fail(http.StatusForbidden, "that request is stale: check this device's clock")
	}
	// 2. Who, from the headers alone, so a stranger never makes this machine read a body: the
	//    certificate's key derives the id, it is under this root and not revoked, and it is in the fleet.
	leaf, pub, ok := p.leaf()
	if !ok {
		return fail(http.StatusForbidden, errNotVerified)
	}
	var trust *cert.Authority
	if h.Trust != nil {
		trust = h.Trust()
	}
	if trust == nil {
		return fail(http.StatusServiceUnavailable, "this node cannot verify callers right now")
	}
	if id, err := trust.Authenticate(leaf); err != nil || id != p.NodeID || h.Member == nil || !h.Member(id) {
		return fail(http.StatusForbidden, errNotVerified)
	}
	// 3. The body, bounded in size and in time.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(now.Add(phoneBodyDeadline))
	var body []byte
	if limit > 0 {
		b, err := readAllLimited(w, r, limit)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return fail(http.StatusRequestEntityTooLarge, tooBig)
			}
			return fail(http.StatusBadRequest, "that request's body could not be read")
		}
		body = b
	} else if n, _ := r.Body.Read(make([]byte, 1)); n > 0 {
		return fail(http.StatusBadRequest, tooBig)
	}
	_ = rc.SetReadDeadline(time.Time{})
	// 4. Signed over exactly this action, target and body; 5. not a replay.
	if !p.signed(pub, action, body) {
		return fail(http.StatusForbidden, errNotVerified)
	}
	if !h.seen.admit(p.NodeID, p.Nonce, time.Unix(p.TS, 0).Add(presenceFreshness).Unix(), now.Unix()) {
		return fail(http.StatusForbidden, "that request was already seen")
	}
	return p, body, release, true
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
// member is this machine itself. Another member recorded at a wildcard address is not dialed at all
// ("" here): the operating system would connect that to THIS machine. (The pin would refuse the
// answer anyway; this does not get as far as asking.)
func relayAddr(loc Located, selfID string) (addr, pin string) {
	addr, pin = lanTransport(loc)
	if loc.Node.ID == selfID {
		return loopback(addr), pin
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || host == "" || (ip != nil && ip.IsUnspecified()) {
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
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
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
