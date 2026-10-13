package edge

// A phone consumes the Edge's bands through the authority (phoneconsume.go,
// features/edge/phone_consume.feature). Real authority, real member face over real mutual TLS, a
// real upstream; only the phone is played by this test, and it signs exactly as the app must.

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
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

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
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
	var selfID string
	_, selfID, rig.selfCert, _ = issueMember(t, a)
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
	})
	return rig
}

type signed struct {
	action, target, nonce, certB64, nodeID string
	ts                                     int64
	key                                    ed25519.PrivateKey
	signBody                               []byte // when set, signed over this instead of the body sent
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
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "not under this Edge's root")

	// Under this root, but not a member of this Edge's fleet (forgotten here).
	key, id, c, _ = issueMember(t, rig.a)
	w = rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, key: key, nodeID: id,
		certB64: base64.StdEncoding.EncodeToString(c.Certificate[0])})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "not a member of this Edge")

	// No certificate at all.
	w = rig.ask(t, http.MethodPost, PhoneBandsPath, "", signed{action: ActionBands, key: rig.phoneKey, nodeID: rig.phoneID, certB64: ""})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, len(rig.got), "nothing reached a model")
}

func TestARevokedPhoneIsRefused(t *testing.T) {
	rig := newPhoneRig(t)
	require.NoError(t, rig.a.Revoke(rig.phoneSer))
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "not under this Edge's root", "the same words as any other stranger")
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
	require.Contains(t, w.Body.String(), "signature does not check out")
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
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestOnlyPostIsServed(t *testing.T) {
	rig := newPhoneRig(t)
	require.Equal(t, http.StatusMethodNotAllowed, rig.ask(t, http.MethodGet, PhoneBandsPath, "", signed{action: ActionBands}).Code)
}

// The member checks the phone's proof itself: a relay that alters the body after the authority let it
// through, or forges a phone's headers, is refused by the member, and nothing reaches its model.
func TestTheMemberChecksTheRelayedProofItself(t *testing.T) {
	rig := newPhoneRig(t)
	ts := time.Now().Unix()
	nonce := freshNonce()
	sig := ed25519.Sign(rig.phoneKey, ConsumeCanonical(rig.phoneID, ActionInfer, "", BodyHash([]byte(chatBody)), nonce, ts))
	p := memberProof{NodeID: rig.phoneID, Cert: rig.phoneCert, Nonce: nonce, TS: ts, Sig: hex.EncodeToString(sig)}
	addr := strings.TrimPrefix(rig.face.URL, "https://")
	jetson, ok, err := rig.fleet.Get(rig.memberID)
	require.NoError(t, err)
	require.True(t, ok)
	resp, err := dialMember(t.Context(), addr, jetson.Transports[0].Fingerprint, &rig.selfCert, InferPath,
		[]byte(strings.Replace(chatBody, "hello", "hellp", 1)), p.set)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Empty(t, len(rig.got))
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

// The authority's own face is recorded as it listens ("[::]:port"); relaying to itself dials loopback.
func TestTheAuthorityDialsItsOwnFaceOnLoopback(t *testing.T) {
	require.Equal(t, "127.0.0.1:8443", dialable("[::]:8443"))
	require.Equal(t, "127.0.0.1:8443", dialable("0.0.0.0:8443"))
	require.Equal(t, "127.0.0.1:8443", dialable(":8443"))
	require.Equal(t, "192.168.1.9:8443", dialable("192.168.1.9:8443"))
}
