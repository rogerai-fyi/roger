package main

// namespaced_routing_bdd_test.go makes features/voice/namespaced_routing.feature an EXECUTABLE
// Cucumber suite for STATION-namespaced public voices: attribution (@<station>/<slug>),
// register-time station uniqueness, and the NAMESPACED VOICE-ID ROUTING money path. It drives the
// REAL register + /voices (computeVoices) + /v1/audio/speech + /v1/audio/transcriptions paths
// end-to-end (no mocks): owner-bound TTS/STT nodes are put on air with real tunnels that answer
// signed receipts, and the suite asserts which node SERVED a namespaced say (X-RogerAI-Provider),
// which operator was CREDITED (EarningsOf(nodeID)), and the consumer DEBIT. The store is the
// in-memory reference store (as every voice/money BDD suite uses); the node->owner attribution
// path is identical to Postgres.
//
// STATION SOURCE (see the feature header's RECON FINDING): the broker cannot recover the station
// from the node-id prefix (slugify is lossy, the station is multi-segment, and instance>=2 adds a
// suffix), so the recommended implementation is an AUTHORITATIVE, SIGNED Station field on the
// registration. This harness stands each node up with (a) a realistic node id "<station>-<model>"
// AND (b) the station recorded on the state, so the steps refer to the authoritative station the
// impl will read — NOT a prefix guess.
//
// RED-FIRST: at the time these steps are written the broker (i) namespaces /voices by GitHub LOGIN
// (voices.go operatorLogin -> o.Login), not station, and (ii) does NOT resolve any namespaced id in
// the relay (audio.go passes pin="" to pickFor, which matches the RAW model only), and (iii) has NO
// cross-owner station-uniqueness guard. So the station-attribution, station-uniqueness, and
// namespaced-routing scenarios are EXPECTED TO FAIL — the RED evidence for this work.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// srOwner is one operator: an ed25519 keypair whose hex pubkey IS the owner account id a node binds
// to. login "" models an APPLE-ONLY owner (no GitHub login). station is the operator's broadcast
// callsign (per-install today; the authoritative namespace under this spec).
type srOwner struct {
	station string
	login   string // "" => Apple-only (no GitHub login)
	priv    ed25519.PrivateKey
	pub     string
}

// srNode is one on-air node: its store id (station-prefixed), modality, owning station, and — set
// by its tunnel goroutine — whether it was CALLED.
type srNode struct {
	id       string
	modality string
	station  string
	called   bool
}

type srState struct {
	t       *testing.T
	b       *broker
	mem     *store.Mem
	owners  map[string]*srOwner // station -> owner
	nodes   map[string]*srNode  // short handle ("nb") -> node
	salt    string
	secrets []string // node addresses/pubkeys that must NEVER appear in /voices

	consumerPriv    ed25519.PrivateKey
	consumerWallet  string
	consumerBalance float64
	anonConsumer    bool

	fetchedNS string

	// /voices results
	voices  []voiceView
	payload string
	// register results
	regCode int
	regMsg  string
	// relay results
	code     int
	body     []byte
	provider string
	debit    float64

	modStub *httptest.Server
}

func (s *srState) reset(t *testing.T) {
	if s.modStub != nil {
		s.modStub.Close()
		s.modStub = nil
	}
	for _, k := range []string{"MODERATION_URL", "ROGERAI_REQUIRE_MODERATION", "ROGERAI_VOICE_IMPERSONATION_DENYLIST"} {
		os.Setenv(k, "")
	}
	s.t = t
	s.mem = store.NewMem()
	s.b = relayBroker(s.mem)
	s.b.mod = moderation{} // screening off in dev by default (a Given can require+break it)
	s.owners = map[string]*srOwner{}
	s.nodes = map[string]*srNode{}
	s.salt = srSalt()
	s.secrets = nil
	s.consumerBalance = 100
	s.anonConsumer = false
	s.fetchedNS = ""
	s.voices, s.payload = nil, ""
	s.regCode, s.regMsg = 0, ""
	s.code, s.body, s.provider, s.debit = 0, nil, "", 0

	_, cpriv, _ := ed25519.GenerateKey(nil)
	s.consumerPriv = cpriv
	cpub := hex.EncodeToString(cpriv.Public().(ed25519.PublicKey))
	_ = s.mem.BindOwner(store.Owner{GitHubID: 777, Login: "consumer", Pubkey: cpub})
	s.consumerWallet = "u_gh_777"
	_, _ = s.mem.AddCredits(s.consumerWallet, s.consumerBalance)
}

// owner binds (once) an operator for a station. apple=true models an Apple-only owner (empty Login).
func (s *srState) owner(station string, apple bool) *srOwner {
	station = agent.SlugStation(station)
	if o, ok := s.owners[station]; ok {
		return o
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	o := &srOwner{station: station, priv: priv, pub: pub}
	if apple {
		_ = s.mem.BindOwner(store.Owner{AppleSub: "apple-" + station, Pubkey: pub}) // no Login
	} else {
		o.login = "op-" + station
		_ = s.mem.BindOwner(store.Owner{GitHubID: int64(100 + len(s.owners)), Login: o.login, Pubkey: pub})
	}
	s.owners[station] = o
	return o
}

// onAirNode stands up a real on-air node for `station` offering (rawModel,name,modality,priceIn),
// bound to the station-owner's pubkey, node id = ShareNodeID(station, rawModel) + a per-scenario
// salt to keep it globally unique. A tunnel goroutine answers ONE job with a signed receipt +
// modality-appropriate body. The short `handle` maps to the store id via s.nodes.
func (s *srState) onAirNode(handle, station, rawModel, name, modality string, priceIn float64, owned, apple bool) {
	realID := agent.ShareNodeID(station, rawModel, 0) + "-" + s.salt
	// Deterministic node key (same seed doRegister uses) so a later idempotent re-register of the
	// SAME node id reuses this key and does not trip the TOFU key guard (tunnel.go:331).
	nodePriv := ed25519.NewKeyFromSeed(sha256Seed("sr-node:" + realID))
	nodePub := nodePriv.Public().(ed25519.PublicKey)
	off := protocol.ModelOffer{Model: rawModel, Modality: modality, Name: name, PriceIn: priceIn}
	s.b.nodes[realID] = protocol.NodeRegistration{
		NodeID: realID, PubKey: hex.EncodeToString(nodePub), Offers: []protocol.ModelOffer{off},
		// Carry the AUTHORITATIVE station the broker namespaces + resolves by (an anon node has "").
		Station: agent.SlugStation(station),
	}
	s.b.lastSeen[realID] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	s.b.tunnels[realID] = tun
	if owned {
		o := s.owner(station, apple)
		_ = s.mem.BindNode(realID, o.pub)
	}
	rec := srNode{id: realID, modality: modality, station: agent.SlugStation(station)}
	s.nodes[handle] = &rec
	s.secrets = append(s.secrets, realID, hex.EncodeToString(nodePub))
	body := []byte("~audio bytes~")
	if modality == protocol.ModalitySTT {
		body = []byte(`{"text":"hello world"}`)
	}
	called := &s.nodes[handle].called
	go func() {
		job, ok := <-tun.jobs
		if !ok {
			return
		}
		*called = true
		r := protocol.UsageReceipt{RequestID: job.ID, NodeID: realID, Model: rawModel, TS: time.Now().Unix()}
		r.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: body, Receipt: r}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()
}

func (s *srState) realID(handle string) string {
	if n, ok := s.nodes[handle]; ok {
		return n.id
	}
	return ""
}

// registerVoice drives the REAL signed register for `station` (a fresh owner, or apple-only),
// capturing the HTTP status + error message. differentOwner=true forces a NEW keypair even for an
// already-seen station (the cross-owner collision case). Used by the uniqueness scenarios.
func (s *srState) registerVoice(station, rawModel, name string, differentOwner bool) {
	var o *srOwner
	if differentOwner {
		_, priv, _ := ed25519.GenerateKey(nil)
		pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
		login := "intruder-" + srSalt()
		_ = s.mem.BindOwner(store.Owner{GitHubID: int64(9000 + len(s.owners)), Login: login, Pubkey: pub})
		o = &srOwner{station: agent.SlugStation(station), login: login, priv: priv, pub: pub}
	} else {
		o = s.owner(station, false)
	}
	off := protocol.ModelOffer{Model: rawModel, Modality: protocol.ModalityTTS, Name: name, PriceIn: 15}
	reg := protocol.NodeRegistration{
		NodeID: agent.ShareNodeID(station, rawModel, 0) + "-" + srSalt(), TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{off}, Station: agent.SlugStation(station),
	}
	s.doRegister(reg, o)
}

// doRegister signs the node key + the owner request signature, drives b.register, captures result.
func (s *srState) doRegister(reg protocol.NodeRegistration, o *srOwner) {
	seed := sha256Seed("sr-node:" + reg.NodeID)
	nodePriv := ed25519.NewKeyFromSeed(seed)
	reg.PubKey = hex.EncodeToString(nodePriv.Public().(ed25519.PublicKey))
	if reg.BridgeToken == "" {
		reg.BridgeToken = "tok-" + reg.NodeID
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	signReq(r, o.priv, body)
	w := httptest.NewRecorder()
	s.b.register(w, r)
	s.regCode = w.Code
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	s.regMsg = resp.Error.Message
}

func (s *srState) getVoices() {
	res := s.b.computeVoices().(map[string]any)
	s.voices, _ = res["voices"].([]voiceView)
	b, _ := json.Marshal(res)
	s.payload = string(b)
}

func (s *srState) saySpeech(voice, input string) {
	reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":%q,"response_format":"wav"}`, voice, input))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(reqBody)))
	s.driveSigned(r, reqBody, func(w *httptest.ResponseRecorder, rr *http.Request) { s.b.audioRelay(w, rr) })
}

func (s *srState) transcribe(voice string, n int) {
	reqBody := []byte(strings.Repeat("x", n))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions?model="+voice, strings.NewReader(string(reqBody)))
	s.driveSigned(r, reqBody, func(w *httptest.ResponseRecorder, rr *http.Request) { s.b.transcribeRelay(w, rr) })
}

func (s *srState) driveSigned(r *http.Request, body []byte, h func(*httptest.ResponseRecorder, *http.Request)) {
	priv := s.consumerPriv
	wallet := s.consumerWallet
	if s.anonConsumer {
		_, ap, _ := ed25519.GenerateKey(nil)
		priv = ap
		wallet = protocol.UserIDFromPubkey(hex.EncodeToString(ap.Public().(ed25519.PublicKey)))
	}
	signReq(r, priv, body)
	before, _ := s.mem.PeekBalance(wallet)
	w := httptest.NewRecorder()
	h(w, r)
	s.code = w.Code
	s.body = w.Body.Bytes()
	s.provider = w.Header().Get("X-RogerAI-Provider")
	after, _ := s.mem.PeekBalance(wallet)
	s.debit = before - after
}

// screenRequiredUnreachable points MODERATION_URL at a dead server with require=1 (fail-closed).
func (s *srState) screenRequiredUnreachable() {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	os.Setenv("MODERATION_URL", url)
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	s.b.mod = loadModeration()
}

// --- Given ------------------------------------------------------------------

func (s *srState) screeningDisabled() error { s.b.mod = moderation{}; return nil }
func (s *srState) operatorStation(station string) error {
	s.owner(station, false)
	return nil
}
func (s *srState) appleOperator(station string) error {
	s.owner(station, true)
	return nil
}
func (s *srState) consumerFunded() error { return nil }

func (s *srState) hasTTSNode(station, handle, rawModel, name string, price float64) error {
	apple := false
	if o, ok := s.owners[agent.SlugStation(station)]; ok {
		apple = o.login == ""
	}
	s.onAirNode(handle, station, rawModel, name, protocol.ModalityTTS, price, true, apple)
	return nil
}
func (s *srState) hasSTTNode(station, handle, rawModel, name string, price float64) error {
	s.onAirNode(handle, station, rawModel, name, protocol.ModalitySTT, price, true, false)
	return nil
}
func (s *srState) chatOnlyNode(station, rawModel string) error {
	s.onAirNode("chat-"+station, station, rawModel, "", protocol.ModalityChat, 0, true, false)
	return nil
}
func (s *srState) anonFreeNode(handle, rawModel, name string) error {
	s.onAirNode(handle, "anon", rawModel, name, protocol.ModalityTTS, 0, false, false)
	return nil
}
func (s *srState) ownerLoginIs(login string) error {
	// re-bind the most recent brave-otter owner to carry a specific GitHub login (leak check).
	o := s.owner("brave-otter", false)
	_ = s.mem.BindOwner(store.Owner{GitHubID: 4242, Login: login, Pubkey: o.pub})
	o.login = login
	s.secrets = append(s.secrets, login)
	return nil
}
func (s *srState) nodeOffAir(handle string) error {
	if id := s.realID(handle); id != "" {
		s.b.lastSeen[id] = time.Now().Add(-2 * nodeTTL)
	}
	return nil
}
func (s *srState) stationBanned(station string) error {
	o := s.owner(station, false)
	s.b.banOwner(o.pub, "test ban", "")
	return nil
}
func (s *srState) walletHolds(bal float64) error {
	cur, _ := s.mem.PeekBalance(s.consumerWallet)
	_, _ = s.mem.AddCredits(s.consumerWallet, bal-cur)
	return nil
}
func (s *srState) callerAnon() error { s.anonConsumer = true; return nil }
func (s *srState) bridgeURLIs(url string) error {
	s.secrets = append(s.secrets, url)
	// attach the bridge URL to the most-recent brave-otter node so /voices has a chance to leak it.
	if n, ok := s.nodes["nb"]; ok {
		reg := s.b.nodes[n.id]
		reg.BridgeURL = url
		s.b.nodes[n.id] = reg
	}
	return nil
}
func (s *srState) localAddr(addr string) error { s.secrets = append(s.secrets, addr); return nil }
func (s *srState) screeningRequiredUnreachable() error {
	s.screenRequiredUnreachable()
	return nil
}

// --- When -------------------------------------------------------------------

func (s *srState) getVoicesStep() error { s.getVoices(); return nil }
func (s *srState) saysVoice(input, voice string) error {
	s.saySpeech(voice, input)
	return nil
}
func (s *srState) saysNChars(n int, voice string) error {
	s.saySpeech(voice, strings.Repeat("a", n))
	return nil
}
func (s *srState) transcribesBytes(n int, voice string) error { s.transcribe(voice, n); return nil }
func (s *srState) readsNamespacedFor(rawModel string) error {
	s.getVoices()
	for _, v := range s.voices {
		if v.ID == rawModel {
			s.fetchedNS = v.NamespacedID
			return nil
		}
	}
	return fmt.Errorf("no /voices entry for raw model %q (got %d voices)", rawModel, len(s.voices))
}
func (s *srState) saysThatExactID(input string) error {
	if s.fetchedNS == "" {
		return fmt.Errorf("no namespaced_id was fetched from /voices")
	}
	s.saySpeech(s.fetchedNS, input)
	return nil
}
func (s *srState) differentOwnerRegisters(station, rawModel, name string) error {
	s.registerVoice(station, rawModel, name, true)
	return nil
}
func (s *srState) stationRegistersSecond(station, rawModel, name string) error {
	s.registerVoice(station, rawModel, name, false)
	return nil
}
func (s *srState) stationReRegisters(handle, station, rawModel, name string) error {
	// re-register the SAME node id (idempotent) for this station's owner.
	o := s.owner(station, false)
	id := s.realID(handle)
	if id == "" {
		id = agent.ShareNodeID(station, rawModel, 0) + "-" + s.salt
	}
	off := protocol.ModelOffer{Model: rawModel, Modality: protocol.ModalityTTS, Name: name, PriceIn: 15}
	reg := protocol.NodeRegistration{NodeID: id, TS: time.Now().Unix(), Offers: []protocol.ModelOffer{off}, Station: agent.SlugStation(station)}
	s.doRegister(reg, o)
	return nil
}
func (s *srState) stationRegistersTTS(station, rawModel, name string) error {
	s.registerVoice(station, rawModel, name, false)
	return nil
}

// --- Then -------------------------------------------------------------------

func (s *srState) byRaw(id string) (voiceView, bool) {
	for _, v := range s.voices {
		if v.ID == id {
			return v, true
		}
	}
	return voiceView{}, false
}
func (s *srState) byNS(id string) (voiceView, bool) {
	for _, v := range s.voices {
		if v.NamespacedID == id {
			return v, true
		}
	}
	return voiceView{}, false
}
func (s *srState) listsRaw(id string) error {
	if _, ok := s.byRaw(id); !ok {
		return fmt.Errorf("raw id %q not listed; %d voices", id, len(s.voices))
	}
	return nil
}
func (s *srState) noRaw(id string) error {
	if _, ok := s.byRaw(id); ok {
		return fmt.Errorf("raw id %q should NOT be listed", id)
	}
	return nil
}
func (s *srState) listsNS(id string) error {
	if _, ok := s.byNS(id); !ok {
		got := make([]string, 0, len(s.voices))
		for _, v := range s.voices {
			got = append(got, v.NamespacedID)
		}
		return fmt.Errorf("namespaced_id %q not listed; got %v", id, got)
	}
	return nil
}
func (s *srState) rawNSIs(rawID, nsID string) error {
	v, ok := s.byRaw(rawID)
	if !ok {
		return fmt.Errorf("raw id %q not listed", rawID)
	}
	if v.NamespacedID != nsID {
		return fmt.Errorf("raw %q namespaced_id = %q, want %q", rawID, v.NamespacedID, nsID)
	}
	return nil
}
func (s *srState) rawOperatorIs(nsID, station string) error {
	v, ok := s.byNS(nsID)
	if !ok {
		return fmt.Errorf("namespaced_id %q not listed", nsID)
	}
	if v.Operator != station {
		return fmt.Errorf("operator = %q, want %q", v.Operator, station)
	}
	return nil
}
func (s *srState) operatorListed(station string) error {
	for _, v := range s.voices {
		if v.Operator == station {
			return nil
		}
	}
	return fmt.Errorf("no voice with operator %q; %d voices", station, len(s.voices))
}
func (s *srState) operatorNoAt() error {
	for _, v := range s.voices {
		if strings.HasPrefix(v.Operator, "@") {
			return fmt.Errorf("operator %q must not carry an @ prefix", v.Operator)
		}
	}
	return nil
}
func (s *srState) distinct() error {
	if len(s.voices) < 2 {
		return fmt.Errorf("expected >=2 distinct voices, got %d", len(s.voices))
	}
	return nil
}
func (s *srState) emptyList() error {
	if len(s.voices) != 0 {
		return fmt.Errorf("expected empty voices list, got %d", len(s.voices))
	}
	return nil
}
func (s *srState) notInPayload(needle string) error {
	if strings.Contains(s.payload, needle) {
		return fmt.Errorf("%q must not appear in /voices payload: %s", needle, s.payload)
	}
	return nil
}
func (s *srState) noLeak() error {
	for _, sec := range s.secrets {
		if sec != "" && strings.Contains(s.payload, sec) {
			return fmt.Errorf("SECURITY: /voices leaked %q", sec)
		}
	}
	return nil
}
func (s *srState) noHostFields() error {
	var probe map[string]any
	_ = json.Unmarshal([]byte(s.payload), &probe)
	arr, _ := probe["voices"].([]any)
	for _, e := range arr {
		m, _ := e.(map[string]any)
		for k := range m {
			lk := strings.ToLower(k)
			if lk == "sample_url" {
				continue
			}
			for _, bad := range []string{"host", "ip", "bridge", "pubkey", "node", "addr", "url"} {
				if strings.Contains(lk, bad) {
					return fmt.Errorf("SECURITY: /voices exposes forbidden field %q", k)
				}
			}
		}
	}
	return nil
}
func (s *srState) onlyOneOperator(station string) error {
	count := map[string]bool{}
	for _, v := range s.voices {
		if v.Operator == station {
			count[v.ID] = true
		}
	}
	// "only one operator brave-otter" — every listed brave-otter voice belongs to ONE owner; the
	// cross-owner intruder must not have added a second brave-otter entry.
	if len(count) > 1 {
		return fmt.Errorf("expected one operator %q, saw %d distinct voices under it", station, len(count))
	}
	return nil
}
func (s *srState) onlyOneNS(id string) error {
	s.getVoices() // the "...in /voices" phrasing (register Rule) has no explicit GET step; fetch fresh
	c := 0
	for _, v := range s.voices {
		if v.NamespacedID == id {
			c++
		}
	}
	if c != 1 {
		return fmt.Errorf("expected exactly one %q, got %d", id, c)
	}
	return nil
}

// register outcomes
func (s *srState) regRejectedStationInUse() error {
	return s.regRejected("station", "in use", "already", "taken")
}
func (s *srState) regRejectedDuplicate() error   { return s.regRejected("duplicate", "already") }
func (s *srState) regRejectedImpersonation() error {
	return s.regRejected("impersonat", "chat", "model", "reserved")
}
func (s *srState) regRejected503() error {
	if s.regCode != http.StatusServiceUnavailable {
		return fmt.Errorf("register = %d (%q), want 503 fail-closed", s.regCode, s.regMsg)
	}
	return nil
}
func (s *srState) regAccepted() error {
	if s.regCode != http.StatusOK {
		return fmt.Errorf("register = %d (%q), want 200 accepted", s.regCode, s.regMsg)
	}
	return nil
}
func (s *srState) regRejected(tokens ...string) error {
	if s.regCode == http.StatusOK {
		return fmt.Errorf("register succeeded (%d), want rejected (%v); msg=%q", s.regCode, tokens, s.regMsg)
	}
	low := strings.ToLower(s.regMsg)
	for _, tok := range tokens {
		if strings.Contains(low, tok) {
			return nil
		}
	}
	return fmt.Errorf("reject message %q mentions none of %v", s.regMsg, tokens)
}

// relay outcomes
func (s *srState) servedByNode(handle string) error {
	id := s.realID(handle)
	if s.provider != id {
		return fmt.Errorf("served by X-RogerAI-Provider=%q, want node %q (%s); status=%d body=%s",
			s.provider, handle, id, s.code, string(s.body))
	}
	return nil
}
func (s *srState) responseIs(want int) error {
	if s.code != want {
		return fmt.Errorf("status = %d, want %d; body=%s", s.code, want, string(s.body))
	}
	return nil
}
func (s *srState) errorNames(sub string) error {
	if !strings.Contains(strings.ToLower(string(s.body)), strings.ToLower(sub)) {
		return fmt.Errorf("error body %q does not name %q", string(s.body), sub)
	}
	return nil
}
func (s *srState) nodeCredited(handle string) error {
	earn, _ := s.mem.EarningsOf(s.realID(handle))
	if earn <= 0 {
		return fmt.Errorf("node %q earned %v, want a positive owner share (status=%d)", handle, earn, s.code)
	}
	return nil
}
func (s *srState) nodeNotCredited(handle string) error {
	earn, _ := s.mem.EarningsOf(s.realID(handle))
	if earn != 0 {
		return fmt.Errorf("node %q earned %v, want 0 (it must NOT be billed)", handle, earn)
	}
	return nil
}
func (s *srState) noNodeCredited() error {
	for h := range s.nodes {
		if earn, _ := s.mem.EarningsOf(s.realID(h)); earn != 0 {
			return fmt.Errorf("node %q earned %v, want no node credited", h, earn)
		}
	}
	return nil
}
func (s *srState) walletDebited(want float64) error {
	if !approxF(s.debit, want) {
		return fmt.Errorf("debit = %v credits, want %v (status=%d)", s.debit, want, s.code)
	}
	return nil
}
func (s *srState) nodeNeverCalled(handle string) error {
	if n, ok := s.nodes[handle]; ok && n.called {
		return fmt.Errorf("node %q was called but should not have been (status=%d)", handle, s.code)
	}
	return nil
}

func TestNamespacedRoutingBDD(t *testing.T) {
	st := &srState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.modStub != nil {
					st.modStub.Close()
					st.modStub = nil
				}
				return ctx, nil
			})

			// Background
			sc.Step(`^a broker with content screening disabled$`, st.screeningDisabled)
			sc.Step(`^an operator broadcasting as station "([^"]*)" who has logged in$`, st.operatorStation)
			sc.Step(`^a signed-in consumer with a funded wallet$`, st.consumerFunded)

			// Given — operators / nodes
			sc.Step(`^station "([^"]*)" is an Apple-only operator with no GitHub login$`, st.appleOperator)
			sc.Step(`^station "([^"]*)" has an on-air tts node "([^"]*)" offering raw model "([^"]*)" named "([^"]*)" at ([0-9.]+) credits per 1M chars$`, st.hasTTSNode)
			sc.Step(`^station "([^"]*)" has an on-air stt node "([^"]*)" offering raw model "([^"]*)" named "([^"]*)" at ([0-9.]+) credits per 1M bytes$`, st.hasSTTNode)
			sc.Step(`^station "([^"]*)" runs a chat-only node offering raw model "([^"]*)"$`, st.chatOnlyNode)
			sc.Step(`^an anonymous free tts node "([^"]*)" offering raw model "([^"]*)" named "([^"]*)"$`, st.anonFreeNode)
			sc.Step(`^the operator's GitHub login is "([^"]*)"$`, st.ownerLoginIs)
			sc.Step(`^node "([^"]*)" has gone off air$`, st.nodeOffAir)
			sc.Step(`^station "([^"]*)" is owner-banned$`, st.stationBanned)
			sc.Step(`^the consumer's wallet holds ([0-9.]+) credits$`, st.walletHolds)
			sc.Step(`^the caller is anonymous and not logged in$`, st.callerAnon)
			sc.Step(`^that node's bridge URL is "([^"]*)"$`, st.bridgeURLIs)
			sc.Step(`^its local address is "([^"]*)"$`, st.localAddr)
			sc.Step(`^content screening is required but unreachable$`, st.screeningRequiredUnreachable)

			// When — /voices
			sc.Step(`^an anonymous GET /voices arrives$`, st.getVoicesStep)
			// When — register (uniqueness)
			sc.Step(`^a DIFFERENT owner registers an on-air tts node under station "([^"]*)" offering raw model "([^"]*)" named "([^"]*)"$`, st.differentOwnerRegisters)
			sc.Step(`^station "([^"]*)" re-registers node "([^"]*)" offering raw model "([^"]*)" named "([^"]*)"$`, func(station, handle, rawModel, name string) error { return st.stationReRegisters(handle, station, rawModel, name) })
			sc.Step(`^station "([^"]*)" registers another on-air tts node offering raw model "([^"]*)" named "([^"]*)"$`, st.stationRegistersSecond)
			sc.Step(`^station "([^"]*)" registers an on-air tts node offering raw model "([^"]*)" named "([^"]*)"$`, st.stationRegistersTTS)
			// When — relay
			sc.Step(`^the consumer says "([^"]*)" with voice "([^"]*)"$`, st.saysVoice)
			sc.Step(`^the caller says "([^"]*)" with voice "([^"]*)"$`, st.saysVoice)
			sc.Step(`^the consumer says a (\d+)-character line with voice "([^"]*)"$`, st.saysNChars)
			sc.Step(`^the consumer transcribes (\d+) bytes with voice "([^"]*)"$`, st.transcribesBytes)
			sc.Step(`^the consumer reads the namespaced_id for raw model "([^"]*)" from /voices$`, st.readsNamespacedFor)
			sc.Step(`^the consumer says "([^"]*)" with that exact namespaced id$`, st.saysThatExactID)

			// Then — /voices attribution
			sc.Step(`^a voice with raw id "([^"]*)" is listed$`, st.listsRaw)
			sc.Step(`^no voice with raw id "([^"]*)" is listed$`, st.noRaw)
			sc.Step(`^a voice with namespaced_id "([^"]*)" is listed$`, st.listsNS)
			sc.Step(`^that voice's namespaced_id is "([^"]*)"$`, func(id string) error {
				// scoped to the two raw ids the phrasing is used with
				if _, ok := st.byRaw("af_heart"); ok {
					return st.rawNSIs("af_heart", id)
				}
				return st.rawNSIs("roger-operator-voice", id)
			})
			sc.Step(`^that voice's operator is "([^"]*)"$`, func(station string) error {
				// the two scenarios phrasing it this way both name the brave-otter voice
				for _, v := range st.voices {
					if strings.HasPrefix(v.NamespacedID, "@"+station+"/") {
						if v.Operator != station {
							return fmt.Errorf("operator = %q, want %q", v.Operator, station)
						}
						return nil
					}
				}
				return fmt.Errorf("no voice under station %q listed", station)
			})
			sc.Step(`^a voice with operator "([^"]*)" is listed$`, st.operatorListed)
			sc.Step(`^it carries no "@" prefix in the operator field$`, st.operatorNoAt)
			sc.Step(`^"([^"]*)" never appears anywhere in the response$`, st.notInPayload)
			sc.Step(`^the two voices are distinct entries$`, st.distinct)
			sc.Step(`^the voices list is empty$`, st.emptyList)
			sc.Step(`^no pubkey, node id, bridge URL, or IP appears anywhere in the response$`, st.noLeak)
			sc.Step(`^the response body contains neither the bridge URL nor the local address$`, st.noLeak)
			sc.Step(`^no field named for a host, ip, bridge, url-to-node, or pubkey exists$`, st.noHostFields)
			sc.Step(`^only one "([^"]*)" operator is listed in /voices$`, st.onlyOneOperator)
			sc.Step(`^only one "([^"]*)" is listed in /voices$`, st.onlyOneNS)

			// Then — register outcomes
			sc.Step(`^the registration is rejected as a station already in use by another operator$`, st.regRejectedStationInUse)
			sc.Step(`^the registration is rejected as a duplicate voice name for this station$`, st.regRejectedDuplicate)
			sc.Step(`^the registration is rejected as chat-model impersonation$`, st.regRejectedImpersonation)
			sc.Step(`^the registration is rejected 503 \(fail closed\), not served unscreened$`, st.regRejected503)
			sc.Step(`^the registration is accepted$`, st.regAccepted)

			// Then — relay outcomes
			sc.Step(`^the request is served by node "([^"]*)"$`, st.servedByNode)
			sc.Step(`^the response is (\d+)$`, st.responseIs)
			sc.Step(`^the error names "([^"]*)"$`, st.errorNames)
			sc.Step(`^node "([^"]*)" is credited for the request$`, st.nodeCredited)
			sc.Step(`^node "([^"]*)" is NOT credited for the request$`, st.nodeNotCredited)
			sc.Step(`^no node is credited$`, st.noNodeCredited)
			sc.Step(`^the consumer's wallet is debited ([0-9.]+) credits$`, st.walletDebited)
			sc.Step(`^node "([^"]*)" is never called$`, st.nodeNeverCalled)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/voice/namespaced_routing.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/namespaced_routing behavior scenarios failed (see godog output above)")
	}
}

func srSalt() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
