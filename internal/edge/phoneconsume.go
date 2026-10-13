package edge

// A PHONE CONSUMES THE EDGE'S BANDS (features/edge/phone_consume.feature; founder ruling 2026-10-13,
// "we should support that").
//
// A member asks another member's face for a turn over mutual TLS (infer.go). A phone cannot: its
// node identity is an Ed25519 key kept by CryptoKit, and Apple's Security framework has no SecKey
// type for Ed25519, so no SecIdentity can be built on iOS to present as a client certificate. Nor
// can the phone count on finishing a TLS handshake with a face whose own certificate is an Ed25519
// leaf. So the phone asks the AUTHORITY, on the listener it already reaches for presence, and proves
// itself the way presence and the authority-side trouble feed do: its member certificate travels
// with the request, and the request is signed by that certificate's key over a canonical form.
//
// THE AUTHORITY RELAYS. It checks the phone exactly as a face checks a peer (chains to this root, in
// date, not revoked - read fresh - the id its key derives, a member of this Edge) plus fresh and not
// a replay, finds the member that serves the band, and relays the very bytes the phone signed to
// that member's /edge/infer over the authority's own mutual-TLS connection, pinned to the member's
// recorded fingerprint. It forwards the phone's proof headers too, and a member that reads them
// checks the phone's signature ITSELF and receipts the turn to the phone (infer.go); one from before
// this reads nothing new and receipts it to the authority. The reply, streamed or not, receipt and
// all, comes back unchanged.
//
// WHAT THIS DOES NOT PROTECT, SAID OUT LOUD. The authority listener is plain HTTP. A prompt and its
// answer cross the LAN between the phone and the authority in the clear: anyone on that network can
// read them, though nobody can forge, alter or replay a request (the signature covers a hash of the
// body). The trouble feed already travels this way; prompts are more private than alerts. The fix
// is proposed in the spec for the founder: an HTTPS authority listener whose server certificate is a
// P-256 leaf under the Edge root (which iOS's TLS stack does handshake with, and which the phone
// checks against the root it already holds), or the body sealed to the authority's key.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

// ConsumeCanonical is exactly what a phone signs: the domain, its node id, the action, the target
// ("" for any member), the lowercase hex SHA-256 of the exact body bytes, the nonce and the time,
// joined by "\n". Golden vectors: internal/edge/testdata/infer_sig_vectors.json.
func ConsumeCanonical(nodeID, action, target, bodySHA256, nonce string, ts int64) []byte {
	return []byte(strings.Join([]string{
		consumePrefix, nodeID, action, target, bodySHA256, nonce, strconv.FormatInt(ts, 10),
	}, "\n"))
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
func hasProof(h http.Header) bool { return h.Get(HeaderSig) != "" }

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

// onBehalf is the face's side of a relayed turn (infer.go, speech.go): when the member that relayed
// it forwarded a phone's proof, the face checks that proof ITSELF - the phone's certificate under the
// root it trusts now, its signature over these very bytes, fresh, not a replay - and the turn is the
// phone's, receipted to it. It does not ask whether the phone is in THIS node's fleet: a phone dials
// only its authority, and the relaying member (already admitted by caller()) answered that.
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
	return verifyProof(trust, proofFrom(r.Header), action, body, s.now(), s.phoneSeen)
}

// verifyProof settles who signed a consume request, in the order presence and the trouble feed use:
// the certificate parses, chains to this root, is in date and not revoked (trust is read fresh by the
// caller), names the id its key derives, the time is fresh, the signature covers exactly this action,
// target and body, and the nonce is new. It returns the node id, or the status and words to refuse with.
func verifyProof(trust *cert.Authority, p memberProof, action string, body []byte, now time.Time, seen *nonceCache) (string, int, string) {
	der, err := base64.StdEncoding.DecodeString(p.Cert)
	if err != nil || len(der) == 0 {
		return "", http.StatusUnauthorized, "a turn from a phone needs its node certificate"
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return "", http.StatusUnauthorized, "a turn from a phone needs its node certificate"
	}
	if trust == nil {
		return "", http.StatusServiceUnavailable, "this node cannot verify callers right now"
	}
	id, err := trust.Authenticate(leaf)
	if err != nil {
		// The same words for a foreign root, an expired certificate and a revoked one, as caller()
		// says them: telling them apart would tell a stranger who is enrolled here.
		return "", http.StatusUnauthorized, "that certificate is not under this Edge's root"
	}
	pub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok || id != p.NodeID || NodeID(pub) != p.NodeID {
		return "", http.StatusForbidden, "that request's identity does not match its certificate"
	}
	if d := now.Sub(time.Unix(p.TS, 0)); !p.tsOK || d > presenceFreshness || d < -presenceFreshness {
		return "", http.StatusForbidden, "that request is stale: check this device's clock"
	}
	sig, err := hex.DecodeString(p.Sig)
	if err != nil || !ed25519.Verify(pub, ConsumeCanonical(p.NodeID, action, p.Target, BodyHash(body), p.Nonce, p.TS), sig) {
		return "", http.StatusForbidden, "that request's signature does not check out"
	}
	if !seen.admit(p.Nonce, time.Unix(p.TS, 0).Add(presenceFreshness).Unix(), now.Unix()) {
		return "", http.StatusForbidden, "that request was already seen"
	}
	return id, 0, ""
}

// PhoneConsume is what the authority needs to serve a phone the Edge's bands.
type PhoneConsume struct {
	Fleet *Fleet
	// Trust returns this Edge's authority FRESH per request, so a node revoked while this runs is
	// refused at once; nil (it cannot be read) refuses every request.
	Trust func() *cert.Authority
	// Member reports whether a node is a member of this Edge (in this authority's fleet). nil admits
	// any certificate that verifies.
	Member func(nodeID string) bool
	// Self is this machine's own member identity, presented to the member a turn is relayed to.
	Self func() *tls.Certificate
	Now  func() time.Time
}

// PhoneConsumeHandler serves PhoneBandsPath, PhoneInferPath and PhoneSpeechPath.
func PhoneConsumeHandler(pc PhoneConsume) http.Handler {
	if pc.Now == nil {
		pc.Now = time.Now
	}
	h := &phoneConsume{PhoneConsume: pc, seen: &nonceCache{seen: map[string]int64{}}}
	mux := http.NewServeMux()
	mux.HandleFunc(PhoneBandsPath, h.bands)
	mux.HandleFunc(PhoneInferPath, func(w http.ResponseWriter, r *http.Request) { h.relay(w, r, ActionInfer) })
	mux.HandleFunc(PhoneSpeechPath, func(w http.ResponseWriter, r *http.Request) { h.relay(w, r, ActionSpeech) })
	return mux
}

type phoneConsume struct {
	PhoneConsume
	seen *nonceCache
}

// admit reads the body up to its cap and settles who is asking, writing every refusal itself.
func (h *phoneConsume) admit(w http.ResponseWriter, r *http.Request, action string, limit int64, tooBig string) (memberProof, []byte, bool) {
	if r.Method != http.MethodPost {
		inferError(w, http.StatusMethodNotAllowed, "method not allowed")
		return memberProof{}, nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		inferError(w, http.StatusRequestEntityTooLarge, tooBig)
		return memberProof{}, nil, false
	}
	p := proofFrom(r.Header)
	var trust *cert.Authority
	if h.Trust != nil {
		trust = h.Trust()
	}
	id, status, msg := verifyProof(trust, p, action, body, h.Now(), h.seen)
	if status != 0 {
		inferError(w, status, msg)
		return memberProof{}, nil, false
	}
	if h.Member != nil && !h.Member(id) {
		inferError(w, http.StatusForbidden, "that node is not a member of this Edge")
		return memberProof{}, nil, false
	}
	return p, body, true
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

// bands answers the phone's "what can I use here".
func (h *phoneConsume) bands(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.admit(w, r, ActionBands, 1<<10, "a band list asks with an empty body"); !ok {
		return
	}
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
	p, body, ok := h.admit(w, r, action, limit, tooBig)
	if !ok {
		return
	}
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
	self := (*tls.Certificate)(nil)
	if h.Self != nil {
		self = h.Self()
	}
	if self == nil {
		inferError(w, http.StatusServiceUnavailable, "this Edge's authority has no member identity to ask another member with")
		return
	}
	addr, pin := lanTransport(loc)
	ctx, cancel := context.WithTimeout(r.Context(), inferTimeout)
	defer cancel()
	resp, err := dialMember(ctx, dialable(addr), pin, self, path, body, func(hd http.Header) {
		p.set(hd)
		if id := r.Header.Get("X-Roger-Request"); id != "" {
			hd.Set("X-Roger-Request", id)
		}
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

// dialable turns a face's listen address into one to dial: a wildcard host (this machine's own
// face, recorded as it listens) is this machine.
func dialable(addr string) string {
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
