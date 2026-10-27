package edge

// A phone consumes the Edge's bands through the authority (phoneconsume.go,
// features/edge/phone_consume.feature). Real authority, real member face over real mutual TLS, a
// real upstream; only the phone is played by this test, and it signs exactly as the app must.

import (
	"bufio"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/cert"
)

type phoneRig struct {
	h         http.Handler
	a         *cert.Authority
	fleet     *Fleet
	phoneKey  ed25519.PrivateKey
	phoneID   string
	phoneCert string // base64 DER
	phoneSer  *big.Int
	member    ed25519.PrivateKey
	memberID  string
	got       chan []byte // what the upstream received
	face      *httptest.Server
	selfCert  tls.Certificate
}

func issueMember(t *testing.T, a *cert.Authority) (ed25519.PrivateKey, string, tls.Certificate, *big.Int) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	id := NodeID(pub)
	leaf, err := a.Issue(id, pub)
	require.NoError(t, err)
	return priv, id, tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}, leaf.SerialNumber
}

func newPhoneRig(t *testing.T) *phoneRig {
	t.Helper()
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	rig := &phoneRig{a: a, got: make(chan []byte, 8)}

	// The model the member serves: chat (streamed when asked) and a voice.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rig.got <- b
		if strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"roger\"}}]}\n\n"+
				"data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			return
		}
		if strings.Contains(r.URL.Path, "speech") {
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write([]byte("RIFF-audio"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"roger"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)

	// The member "jetson", its face over mutual TLS.
	var memberCert tls.Certificate
	rig.member, rig.memberID, memberCert, _ = issueMember(t, a)
	// The authority's own node is jetson: a test face can only listen on loopback, and the authority
	// dials loopback only for its own face (relayAddr).
	selfID := rig.memberID
	rig.selfCert = memberCert
	srv := NewServer(Describe{NodeID: rig.memberID, Account: "acct-1", Kind: "host"}, memberCert)
	srv.SetServing(Serving{
		Trust:    a,
		TrustNow: func() *cert.Authority { return a },
		Member:   func(id string) bool { return id == selfID }, // the authority is a member; the phone is not in this fleet
		Upstream: func(band string) (string, string, string, bool) {
			return up.URL + "/v1/chat/completions", "", "serve", band == "qwen3-8b"
		},
		Speech: func(band string) (string, string, string, bool) {
			return up.URL + "/v1/audio/speech", "", "serve", band == "kokoro"
		},
		NodeID: rig.memberID,
		Key:    rig.member,
	})
	rig.face = httptest.NewUnstartedServer(srv.Handler())
	rig.face.TLS = srv.ListenerTLS()
	rig.face.StartTLS()
	t.Cleanup(rig.face.Close)
	addr := strings.TrimPrefix(rig.face.URL, "https://")

	// The phone, issued under the same root.
	var phoneTLS tls.Certificate
	rig.phoneKey, rig.phoneID, phoneTLS, rig.phoneSer = issueMember(t, a)
	rig.phoneCert = base64.StdEncoding.EncodeToString(phoneTLS.Certificate[0])

	jetson := store.EdgeNode{ID: rig.memberID, Name: "jetson", Kind: "host", Presence: string(PresenceVerified),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: addr, Fingerprint: Fingerprint(memberCert.Certificate[0])}},
		Instances: []Instance{{Name: "serve", Bands: []string{"qwen3-8b", "kokoro"}, Voices: []string{"kokoro"},
			Caps: []store.EdgeCap{{Name: string(Serve), State: string(Verified)}}}}}
	phone := store.EdgeNode{ID: rig.phoneID, Name: "pocket", Kind: "mobile", Presence: string(PresenceVerified)}
	rig.fleet = seedFleet(t, jetson, phone)

	self := rig.selfCert
	rig.h = PhoneConsumeHandler(PhoneConsume{
		Fleet:  rig.fleet,
		Trust:  func() *cert.Authority { return a },
		Member: func(id string) bool { _, ok, err := rig.fleet.Get(id); return err == nil && ok },
		Self:   func() *tls.Certificate { return &self },
		Root:   a.RootKey(),
	})
	return rig
}

type signed struct {
	action, target, nonce, certB64, nodeID string
	ts                                     int64
	key                                    ed25519.PrivateKey
	signBody                               []byte // when set, signed over this instead of the body sent
	noBodyHash                             bool   // leave Roger-Body-SHA256 off
}

func freshNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ask sends one signed phone request to the authority handler, as the app must build it.
func (rig *phoneRig) ask(t *testing.T, method, path string, body string, s signed) *httptest.ResponseRecorder {
	t.Helper()
	if s.nonce == "" {
		s.nonce = freshNonce()
	}
	if s.ts == 0 {
		s.ts = time.Now().Unix()
	}
	if s.key == nil {
		s.key, s.nodeID, s.certB64 = rig.phoneKey, rig.phoneID, rig.phoneCert
	}
	signOver := []byte(body)
	if s.signBody != nil {
		signOver = s.signBody
	}
	sig := ed25519.Sign(s.key, ConsumeCanonical(s.nodeID, s.action, s.target, BodyHash(signOver), s.nonce, s.ts))
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(HeaderNode, s.nodeID)
	r.Header.Set(HeaderCert, s.certB64)
	if s.target != "" {
		r.Header.Set(HeaderTarget, s.target)
	}
	r.Header.Set(HeaderNonce, s.nonce)
	r.Header.Set(HeaderTS, fmt.Sprint(s.ts))
	r.Header.Set(HeaderSig, hex.EncodeToString(sig))
	if !s.noBodyHash {
		r.Header.Set(HeaderBodySHA256, BodyHash(signOver))
	}
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, r)
	return w
}

const chatBody = `{"model":"qwen3-8b","messages":[{"role":"user","content":"hello"}]}`

func TestAPhoneListsTheBandsAndWhoServesThem(t *testing.T) {
	rig := newPhoneRig(t)
	w := rig.ask(t, http.MethodPost, PhoneBandsPath, "", signed{action: ActionBands})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got ConsumeBands
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Bands, 2)
	require.Equal(t, "kokoro", got.Bands[0].Band)
	require.True(t, got.Bands[0].Voice)
	require.Equal(t, "qwen3-8b", got.Bands[1].Band)
	require.False(t, got.Bands[1].Voice)
	require.Equal(t, []ConsumeServer{{Node: "jetson", NodeID: rig.memberID, Instance: "serve", Target: "jetson/serve"}}, got.Bands[1].ServedBy)
}

// A member's turn reaches the band on another member, and comes back with that member's own receipt,
// made out to the PHONE: the member checked the phone's proof itself.
func TestAPhoneReachesABandOnAnotherMember(t *testing.T) {
	rig := newPhoneRig(t)
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "roger")
	require.Equal(t, chatBody, string(<-rig.got), "the member's model got the very bytes the phone signed")
	rec, err := protocol.DecodeReceipt(w.Header().Get("X-RogerAI-Receipt"))
	require.NoError(t, err)
	require.True(t, rec.Local)
	require.Equal(t, rig.phoneID, rec.User, "the turn is receipted to the phone, not to the relay")
	require.Equal(t, rig.memberID, rec.NodeID)
	require.True(t, rec.VerifyNode(hex.EncodeToString(rig.member.Public().(ed25519.PublicKey))), "signed by the serving member")
	require.Equal(t, "0", w.Header().Get("X-RogerAI-Cost"))
}

func TestAPhoneTurnStreamsWithItsReceiptAtTheEnd(t *testing.T) {
	rig := newPhoneRig(t)
	body := `{"model":"qwen3-8b","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	w := rig.ask(t, http.MethodPost, PhoneInferPath, body, signed{action: ActionInfer, target: "jetson"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	var receipt string
	sc := bufio.NewScanner(strings.NewReader(w.Body.String()))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), ": rogerai-receipt=") {
			receipt = strings.TrimPrefix(sc.Text(), ": rogerai-receipt=")
		}
	}
	rec, err := protocol.DecodeReceipt(receipt)
	require.NoError(t, err, w.Body.String())
	require.Equal(t, rig.phoneID, rec.User)
	require.Equal(t, 3, rec.PromptTokens)
}

func TestAPhoneSpeaksAndTheTextCapHolds(t *testing.T) {
	rig := newPhoneRig(t)
	body := `{"model":"kokoro","input":"roger that"}`
	w := rig.ask(t, http.MethodPost, PhoneSpeechPath, body, signed{action: ActionSpeech})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "RIFF-audio", w.Body.String())
	rec, err := protocol.DecodeReceipt(w.Header().Get("X-RogerAI-Receipt"))
	require.NoError(t, err)
	require.Equal(t, rig.phoneID, rec.User)

	big := `{"model":"kokoro","input":"` + strings.Repeat("a", speechBodyCap) + `"}`
	w = rig.ask(t, http.MethodPost, PhoneSpeechPath, big, signed{action: ActionSpeech})
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)

	w = rig.ask(t, http.MethodPost, PhoneSpeechPath, chatBody, signed{action: ActionSpeech})
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "no member of this Edge speaks with qwen3-8b; it offers: kokoro")
}

func TestAnUnknownBandOrMemberIsRefusedWithAClearLine(t *testing.T) {
	rig := newPhoneRig(t)
	w := rig.ask(t, http.MethodPost, PhoneInferPath, `{"model":"llama-9000"}`, signed{action: ActionInfer})
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "no member of this Edge serves llama-9000; it offers: kokoro, qwen3-8b")

	w = rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, target: "nas"})
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "no member of this Edge is called nas")

	w = rig.ask(t, http.MethodPost, PhoneInferPath, `{"messages":[]}`, signed{action: ActionInfer})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestANonMemberIsRefused(t *testing.T) {
	rig := newPhoneRig(t)
	// Another Edge's member entirely.
	other, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	key, id, c, _ := issueMember(t, other)
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, key: key, nodeID: id,
		certB64: base64.StdEncoding.EncodeToString(c.Certificate[0])})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)

	// Under this root, but not a member of this Edge's fleet (forgotten here).
	key, id, c, _ = issueMember(t, rig.a)
	w = rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, key: key, nodeID: id,
		certB64: base64.StdEncoding.EncodeToString(c.Certificate[0])})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified, "the same words as any other stranger")

	// No certificate at all.
	w = rig.ask(t, http.MethodPost, PhoneBandsPath, "", signed{action: ActionBands, key: rig.phoneKey, nodeID: rig.phoneID, certB64: ""})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)
	require.Empty(t, len(rig.got), "nothing reached a model")
}

func TestARevokedPhoneIsRefused(t *testing.T) {
	rig := newPhoneRig(t)
	require.NoError(t, rig.a.Revoke(rig.phoneSer))
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified, "the same words as any other stranger")

	// A captured certificate without its key learns nothing either: a fresh request with a wrong
	// signature gets the same answer from a revoked member as from a live one.
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	w = rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, key: wrong, nodeID: rig.phoneID, certB64: rig.phoneCert})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)
}

func TestAReplayIsRefused(t *testing.T) {
	rig := newPhoneRig(t)
	s := signed{action: ActionInfer, nonce: freshNonce(), ts: time.Now().Unix()}
	require.Equal(t, http.StatusOK, rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, s).Code)
	<-rig.got
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, s)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "already seen")
}

func TestAStaleRequestIsRefused(t *testing.T) {
	rig := newPhoneRig(t)
	for _, ts := range []int64{time.Now().Add(-6 * time.Minute).Unix(), time.Now().Add(6 * time.Minute).Unix()} {
		w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, ts: ts})
		require.Equal(t, http.StatusForbidden, w.Code)
		require.Contains(t, w.Body.String(), "stale")
	}
}

func TestATamperedBodyIsRefused(t *testing.T) {
	rig := newPhoneRig(t)
	w := rig.ask(t, http.MethodPost, PhoneInferPath, strings.Replace(chatBody, "hello", "hellp", 1),
		signed{action: ActionInfer, signBody: []byte(chatBody)})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)
	require.Empty(t, len(rig.got))
}

// A signature is bound to its action and its target: a captured band list is not a turn, and a turn
// signed for one member is not one for another.
func TestASignatureIsBoundToItsActionAndTarget(t *testing.T) {
	rig := newPhoneRig(t)
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionBands})
	require.Equal(t, http.StatusForbidden, w.Code)

	s := signed{action: ActionInfer, target: "jetson", nonce: freshNonce(), ts: time.Now().Unix()}
	sig := ed25519.Sign(rig.phoneKey, ConsumeCanonical(rig.phoneID, s.action, "jetson", BodyHash([]byte(chatBody)), s.nonce, s.ts))
	r := httptest.NewRequest(http.MethodPost, PhoneInferPath, strings.NewReader(chatBody))
	r.Header.Set(HeaderNode, rig.phoneID)
	r.Header.Set(HeaderCert, rig.phoneCert)
	r.Header.Set(HeaderTarget, "jetson/serve") // retargeted after signing
	r.Header.Set(HeaderNonce, s.nonce)
	r.Header.Set(HeaderTS, fmt.Sprint(s.ts))
	r.Header.Set(HeaderSig, hex.EncodeToString(sig))
	r.Header.Set(HeaderBodySHA256, BodyHash([]byte(chatBody)))
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestOnlyPostIsServed(t *testing.T) {
	rig := newPhoneRig(t)
	require.Equal(t, http.StatusMethodNotAllowed, rig.ask(t, http.MethodGet, PhoneBandsPath, "", signed{action: ActionBands}).Code)
}

// relayed is one turn as the authority would relay it to the face at addr, with its attestation made
// for faceID (the face it is meant for) and an optional body change after the phone signed.
func (rig *phoneRig) relayed(t *testing.T, addr, pin, faceID, body string, attest bool) *http.Response {
	t.Helper()
	ts, nonce := time.Now().Unix(), freshNonce()
	sig := hex.EncodeToString(ed25519.Sign(rig.phoneKey, ConsumeCanonical(rig.phoneID, ActionInfer, "", BodyHash([]byte(chatBody)), nonce, ts)))
	p := memberProof{NodeID: rig.phoneID, Cert: rig.phoneCert, Nonce: nonce, TS: ts, Sig: sig, BodySHA: BodyHash([]byte(chatBody))}
	resp, err := dialMember(t.Context(), addr, pin, &rig.selfCert, InferPath, []byte(body), func(h http.Header) {
		p.set(h)
		if attest {
			sum := sha256.Sum256(RelayCanonical(faceID, sig))
			a, err := rig.a.RootKey().Sign(rand.Reader, sum[:], crypto.SHA256)
			require.NoError(t, err)
			h.Set(HeaderRelay, base64.StdEncoding.EncodeToString(a))
		}
	})
	require.NoError(t, err)
	return resp
}

// The member checks the phone's proof itself, and credits it only when the Edge's root vouches the
// turn was relayed to IT: a body changed after signing, a proof with no attestation, and a turn
// attested for another face are all refused, and nothing reaches its model (audit 2026-10-13: a
// member that captured a relayed turn could replay it to every other face serving the band).
func TestTheMemberChecksTheRelayedProofItself(t *testing.T) {
	rig := newPhoneRig(t)
	addr := strings.TrimPrefix(rig.face.URL, "https://")
	jetson, ok, err := rig.fleet.Get(rig.memberID)
	require.NoError(t, err)
	require.True(t, ok)
	pin := jetson.Transports[0].Fingerprint

	resp := rig.relayed(t, addr, pin, rig.memberID, strings.Replace(chatBody, "hello", "hellp", 1), true)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "a body changed after the phone signed it")

	resp = rig.relayed(t, addr, pin, rig.memberID, chatBody, false)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "a member relaying with no attestation from the root")

	resp = rig.relayed(t, addr, pin, "n_nas", chatBody, true)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "a turn attested for another face")
	require.Empty(t, len(rig.got))

	resp = rig.relayed(t, addr, pin, rig.memberID, chatBody, true)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the authority's own relay to this face is served")
	rec, err := protocol.DecodeReceipt(resp.Header.Get("X-RogerAI-Receipt"))
	require.NoError(t, err)
	require.Equal(t, rig.phoneID, rec.User)
}

// The phone's X-Roger-Request is not signed, so it never reaches the receipt: the face names the turn.
func TestAPhonesRequestIDDoesNotReachTheReceipt(t *testing.T) {
	rig := newPhoneRig(t)
	s := signed{action: ActionInfer}
	s.nonce, s.ts = freshNonce(), time.Now().Unix()
	sig := ed25519.Sign(rig.phoneKey, ConsumeCanonical(rig.phoneID, ActionInfer, "", BodyHash([]byte(chatBody)), s.nonce, s.ts))
	r := httptest.NewRequest(http.MethodPost, PhoneInferPath, strings.NewReader(chatBody))
	r.Header.Set(HeaderNode, rig.phoneID)
	r.Header.Set(HeaderCert, rig.phoneCert)
	r.Header.Set(HeaderNonce, s.nonce)
	r.Header.Set(HeaderTS, fmt.Sprint(s.ts))
	r.Header.Set(HeaderSig, hex.EncodeToString(sig))
	r.Header.Set(HeaderBodySHA256, BodyHash([]byte(chatBody)))
	r.Header.Set("X-Roger-Request", "chosen-by-someone-in-the-path")
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	rec, err := protocol.DecodeReceipt(w.Header().Get("X-RogerAI-Receipt"))
	require.NoError(t, err)
	require.NotEqual(t, "chosen-by-someone-in-the-path", rec.RequestID)
}

// A stranger's body is never read: identity is settled from the headers first.
func TestAStrangersBodyIsNotRead(t *testing.T) {
	rig := newPhoneRig(t)
	other, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	key, id, c, _ := issueMember(t, other)
	body := &countingReader{r: strings.NewReader(chatBody)}
	ts, nonce := time.Now().Unix(), freshNonce()
	r := httptest.NewRequest(http.MethodPost, PhoneInferPath, body)
	r.Header.Set(HeaderNode, id)
	r.Header.Set(HeaderCert, base64.StdEncoding.EncodeToString(c.Certificate[0]))
	r.Header.Set(HeaderNonce, nonce)
	r.Header.Set(HeaderTS, fmt.Sprint(ts))
	r.Header.Set(HeaderSig, hex.EncodeToString(ed25519.Sign(key, ConsumeCanonical(id, ActionInfer, "", BodyHash([]byte(chatBody)), nonce, ts))))
	r.Header.Set(HeaderBodySHA256, BodyHash([]byte(chatBody)))
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, r)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Zero(t, body.n, "not one byte of a stranger's body was read")
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A band list is asked with an empty body; anything else is refused with the words that say so.
func TestABandListTakesAnEmptyBody(t *testing.T) {
	rig := newPhoneRig(t)
	w := rig.ask(t, http.MethodPost, PhoneBandsPath, "{}", signed{action: ActionBands})
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "empty body")
}

// Phone requests are bounded in flight: two per member and eight in all, and the slot is taken only
// after the signature is checked.
func TestPhoneRequestsAreBoundedInFlight(t *testing.T) {
	pc := &phoneConsume{seen: newNonceCache(), perNode: map[string]int{}}
	require.True(t, pc.take("n_a"))
	require.True(t, pc.take("n_a"))
	require.False(t, pc.take("n_a"), "a third from one member waits")
	for i := 0; i < phoneMaxInFlight-2; i++ {
		require.True(t, pc.take(fmt.Sprintf("n_%d", i)))
	}
	require.False(t, pc.take("n_b"), "eight in all")
	pc.give("n_a")
	require.True(t, pc.take("n_b"))
	pc.give("n_b")
	pc.give("n_a")
	require.Equal(t, phoneMaxInFlight-2, pc.inFlight)
	require.NotContains(t, pc.perNode, "n_a")

}

// Audit 2026-10-13: the member certificate travels in the clear, so a device that sniffed it, without
// the key, must not take a slot or make the authority read a body. Its signature is checked first,
// from the headers alone.
func TestASniffedCertificateHoldsNoSlotAndIsReadNothing(t *testing.T) {
	rig := newPhoneRig(t)
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	body := &countingReader{r: strings.NewReader(chatBody)}
	ts, nonce := time.Now().Unix(), freshNonce()
	r := httptest.NewRequest(http.MethodPost, PhoneInferPath, body)
	r.Header.Set(HeaderNode, rig.phoneID)
	r.Header.Set(HeaderCert, rig.phoneCert)
	r.Header.Set(HeaderNonce, nonce)
	r.Header.Set(HeaderTS, fmt.Sprint(ts))
	r.Header.Set(HeaderBodySHA256, BodyHash([]byte(chatBody)))
	r.Header.Set(HeaderSig, hex.EncodeToString(ed25519.Sign(wrong, ConsumeCanonical(rig.phoneID, ActionInfer, "", BodyHash([]byte(chatBody)), nonce, ts))))
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, r)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)
	require.Zero(t, body.n, "not one byte read")
}

// Roger-Body-SHA256 is required, and a body that does not match it is refused with the same 403 as
// any other failure of identity; a revoked and a live phone read the same for a body over the cap.
func TestTheBodyHashHeaderIsRequiredAndHeld(t *testing.T) {
	rig := newPhoneRig(t)
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, noBodyHash: true})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)

	// The over-cap and body-shape refusals come only after the signature: a captured certificate
	// cannot use them to tell a live member from a revoked one.
	big := `{"model":"kokoro","input":"` + strings.Repeat("a", speechBodyCap) + `"}`
	require.NoError(t, rig.a.Revoke(rig.phoneSer))
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	w = rig.ask(t, http.MethodPost, PhoneSpeechPath, big, signed{action: ActionSpeech, key: wrong, nodeID: rig.phoneID, certB64: rig.phoneCert})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)
	w = rig.ask(t, http.MethodPost, PhoneBandsPath, "{}", signed{action: ActionBands, key: wrong, nodeID: rig.phoneID, certB64: rig.phoneCert})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), errNotVerified)
}

// A nonce is exactly 16 bytes as 32 lowercase hex characters.
func TestANonceOfAnyOtherShapeIsRefused(t *testing.T) {
	rig := newPhoneRig(t)
	for _, n := range []string{strings.ToUpper(freshNonce()), freshNonce()[:30], freshNonce() + "00", strings.Repeat("a", 800<<10)} {
		w := rig.ask(t, http.MethodPost, PhoneBandsPath, "", signed{action: ActionBands, nonce: n})
		require.Equal(t, http.StatusForbidden, w.Code)
	}
}

// The golden vectors the Roger app pins its signer to: computed independently (pure-Python RFC 8032,
// testdata/gen_infer_sig_vectors.py), and reproduced here byte for byte.
func TestConsumeSignatureGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/infer_sig_vectors.json")
	require.NoError(t, err)
	var file struct {
		Domain   string `json:"domain"`
		JoinedBy string `json:"joined_by"`
		Vectors  []struct {
			Name         string `json:"name"`
			Seed         string `json:"seed"`
			PublicKey    string `json:"public_key"`
			NodeID       string `json:"node_id"`
			Action       string `json:"action"`
			Target       string `json:"target"`
			Body         string `json:"body"`
			BodySHA256   string `json:"body_sha256"`
			Nonce        string `json:"nonce"`
			TS           int64  `json:"ts"`
			Canonical    string `json:"canonical"`
			CanonicalHex string `json:"canonical_hex"`
			Sig          string `json:"sig"`
		} `json:"vectors"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.Equal(t, consumePrefix, file.Domain)
	require.Equal(t, "\n", file.JoinedBy)
	require.Len(t, file.Vectors, 8)
	for _, v := range file.Vectors {
		seed, err := hex.DecodeString(v.Seed)
		require.NoError(t, err)
		key := ed25519.NewKeyFromSeed(seed)
		pub := key.Public().(ed25519.PublicKey)
		require.Equal(t, v.PublicKey, hex.EncodeToString(pub), v.Name)
		require.Equal(t, v.NodeID, NodeID(pub), v.Name)
		require.Equal(t, v.BodySHA256, BodyHash([]byte(v.Body)), v.Name)
		c := ConsumeCanonical(v.NodeID, v.Action, v.Target, v.BodySHA256, v.Nonce, v.TS)
		require.Equal(t, v.Canonical, string(c), v.Name)
		require.Equal(t, v.CanonicalHex, hex.EncodeToString(c), v.Name)
		require.Equal(t, v.Sig, hex.EncodeToString(ed25519.Sign(key, c)), v.Name)
	}
}

// The authority's own face is recorded as it listens ("[::]:port"); relaying to ITSELF dials loopback.
// relay applies this only when the member is the authority's own node.
func TestTheAuthorityDialsItsOwnFaceOnLoopback(t *testing.T) {
	require.Equal(t, "127.0.0.1:8443", loopback("[::]:8443"))
	require.Equal(t, "127.0.0.1:8443", loopback("0.0.0.0:8443"))
	require.Equal(t, "127.0.0.1:8443", loopback(":8443"))
	require.Equal(t, "192.168.1.9:8443", loopback("192.168.1.9:8443"))
}

// A member that advertises a wildcard address is NOT redirected to the authority's own loopback; only
// the authority's own node is.
func TestAMembersWildcardAddressIsNotThisMachine(t *testing.T) {
	loc := Located{Node: store.EdgeNode{ID: "n_member", Transports: []store.EdgeTransport{{Kind: "lan", Addr: "0.0.0.0:8443", Fingerprint: "ab"}}}}
	addr, _ := relayAddr(loc, "n_authority")
	require.Empty(t, addr, "never dialed: the system would connect a wildcard to this machine")
	for _, a := range []string{"[::]:8443", "127.0.0.1:8443", "[::1]:8443", "169.254.1.2:8443", "[fe80::1]:8443"} {
		loc.Node.Transports[0].Addr = a
		addr, _ = relayAddr(loc, "n_authority")
		require.Empty(t, addr, a)
	}
	loc.Node.Transports[0].Addr = "0.0.0.0:8443"
	addr, _ = relayAddr(loc, "n_member")
	require.Equal(t, "127.0.0.1:8443", addr, "this machine's own face")
}
