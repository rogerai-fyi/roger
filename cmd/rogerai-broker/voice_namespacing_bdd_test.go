package main

// voice_namespacing_bdd_test.go makes features/voice/namespacing_attribution.feature an
// EXECUTABLE Cucumber suite. It drives the REAL register path (b.register) and the REAL
// GET /voices aggregation (computeVoices) end-to-end: an owner is bound in the store, a
// TTS offer is registered through the signed register handler (owner sig => BindNode =>
// AccountOfNode), then /voices is fetched and asserted for the namespaced id, the operator
// attribution, the impersonation/duplicate/moderation rejections, and — always — the
// standing security rule that NO node address (pubkey/node id/bridge URL/hostname/IP)
// appears in the payload. NO mocks: the store is a REAL ephemeral Postgres when
// ROGERAI_TEST_DATABASE_URL is set (the cover-gate provisions one), else the in-memory
// reference store (the attribution path — BindOwner/BindNode/AccountOfNode/OwnerByPubkey —
// is identical across both). The moderation screen is a local httptest stub (the same
// OpenAI-moderation shape moderation_bdd_test.go uses), never a mock of b.mod.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
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
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type nsOwner struct {
	priv ed25519.PrivateKey
	pub  string // hex user (owner) pubkey — the account id
}

type nsState struct {
	b       *broker
	modStub *httptest.Server
	owners  map[string]nsOwner // login -> keypair
	nodeSeq int
	salt    string // per-scenario unique node-id salt (real Postgres BindNode is TOFU: a
	// node id may only ever bind to its FIRST owner, so a repeated node id across scenarios
	// — or across test runs against the same shared DB — would keep a stale binding; a fresh
	// salt makes every scenario's node ids globally unique so each binds cleanly).
	voices     []voiceView
	payload    string
	secrets    []string
	lastCode   int
	lastMsg    string
	lastNodeID string   // node id of the most recent register (for an idempotent re-register)
	lastOwner  *nsOwner // owner of the most recent register (for an idempotent re-register)
}

// reset builds a fresh broker on a REAL store (Postgres when a DSN is configured, else Mem)
// with the register-path maps wired and content screening ON by default (a local stub that
// ALLOWs). Individual scenarios re-point/override the screen.
func (s *nsState) reset(t *testing.T) {
	if s.modStub != nil {
		s.modStub.Close()
		s.modStub = nil
	}
	for _, k := range []string{"MODERATION_PROVIDER", "MODERATION_URL", "MODERATION_GROQ_KEY", "GROQ_API_KEY", "ROGERAI_REQUIRE_MODERATION", "ROGERAI_VOICE_IMPERSONATION_DENYLIST"} {
		os.Setenv(k, "")
	}
	s.owners = map[string]nsOwner{}
	s.nodeSeq = 0
	s.salt = newNodeSalt()
	s.voices, s.payload, s.secrets = nil, "", nil
	s.lastCode, s.lastMsg = 0, ""

	db := nsTestStore(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	s.b = &broker{
		db:           db,
		priv:         brokerPriv,
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		private:      map[string]bool{},
		bandOf:       map[string]string{},
		banned:       map[string]bool{},
		attestedAt:   map[string]time.Time{},
		attest:       loadAttestRegistry(),
		trust:        map[string]trustState{},
		tps:          map[string]float64{},
		pubOfUser:    map[string]string{},
		freeRegByIP:  map[string][]time.Time{},
	}
	// Default posture for the suite: a REQUIRED screen that ALLOWs (so a clean voice
	// passes but the fail-closed/flag scenarios can override).
	s.screenAllow(true)
}

// screenAllow points MODERATION_URL at a stub returning flagged=<!allow> and reloads b.mod
// with require=1 (the suite's default posture: screening configured and required).
func (s *nsState) screenAllow(allow bool) {
	if s.modStub != nil {
		s.modStub.Close()
	}
	s.modStub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"flagged": !allow})
	}))
	os.Setenv("MODERATION_URL", s.modStub.URL)
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	s.b.mod = loadModeration()
}

// screenFlagsText points the stub at a screen that flags ONLY when the screened text
// contains `needle` (the abusive-name scenario), allowing everything else.
func (s *nsState) screenFlagsText(needle string) {
	if s.modStub != nil {
		s.modStub.Close()
	}
	s.modStub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(map[string]any{"flagged": strings.Contains(in.Input, needle)})
	}))
	os.Setenv("MODERATION_URL", s.modStub.URL)
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	s.b.mod = loadModeration()
}

// owner returns the keypair for a login, binding it in the store on first use. A leading
// "@" is stripped so the feature's "@bownux" (callsign form) and "bownux" (bare login) map
// to the SAME owner — the store login is always the bare GitHub login (Owner.Login).
func (s *nsState) owner(login string) nsOwner {
	login = strings.TrimPrefix(login, "@")
	if o, ok := s.owners[login]; ok {
		return o
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	_ = s.b.db.BindOwner(store.Owner{GitHubID: int64(len(s.owners) + 1), Login: login, Pubkey: pub})
	o := nsOwner{priv: priv, pub: pub}
	s.owners[login] = o
	return o
}

// nodeID builds a globally-unique node id (kind + bare login + per-scenario salt + seq) so a
// repeated register across scenarios/runs never collides with a stale Postgres TOFU binding.
func (s *nsState) nodeID(kind, login string) string {
	s.nodeSeq++
	return fmt.Sprintf("%s-%s-%s-%d", kind, strings.TrimPrefix(login, "@"), s.salt, s.nodeSeq)
}

// registerVoice posts a signed TTS register for `login`, model+name, optional bridge/private.
// Returns nothing; stores the HTTP status + error message on the state.
func (s *nsState) registerVoice(login, model, name string, priv2 ...bool) {
	o := s.owner(login)
	priv := false
	if len(priv2) > 0 {
		priv = priv2[0]
	}
	off := protocol.ModelOffer{Model: model, Modality: protocol.ModalityTTS, Name: name}
	reg := protocol.NodeRegistration{
		NodeID: s.nodeID("node", login), TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{off}, Private: priv,
	}
	s.doRegister(reg, &o)
}

// registerTwoOffers posts ONE signed register carrying TWO tts offers for `login` (distinct
// raw models, the given display names). Used to prove a single registration whose offers slug
// identically is rejected as a whole (not silently deduped onto one namespaced id).
func (s *nsState) registerTwoOffers(login, name1, name2 string) {
	o := s.owner(login)
	reg := protocol.NodeRegistration{
		NodeID: s.nodeID("node", login), TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{
			{Model: "two-a", Modality: protocol.ModalityTTS, Name: name1},
			{Model: "two-b", Modality: protocol.ModalityTTS, Name: name2},
		},
	}
	s.doRegister(reg, &o)
}

// registerAnonVoice posts an UNSIGNED (anonymous, unbound) FREE tts register.
func (s *nsState) registerAnonVoice(model, name string) {
	off := protocol.ModelOffer{Model: model, Modality: protocol.ModalityTTS, Name: name}
	reg := protocol.NodeRegistration{NodeID: s.nodeID("anon", ""), TS: time.Now().Unix(), Offers: []protocol.ModelOffer{off}}
	s.doRegister(reg, nil)
}

// registerChat posts an UNSIGNED free chat register (a legacy chat node).
func (s *nsState) registerChat(model string) {
	off := protocol.ModelOffer{Model: model, Modality: protocol.ModalityChat, Ctx: 4096}
	reg := protocol.NodeRegistration{NodeID: s.nodeID("chat", ""), TS: time.Now().Unix(), Offers: []protocol.ModelOffer{off}}
	s.doRegister(reg, nil)
}

// doRegister signs the node key (deterministic per node id so a re-register reuses it),
// optionally attaches the owner request signature, drives b.register, and captures the
// result. A registered node's bridge URL (when set) is tracked as a secret that must never
// leak into /voices.
func (s *nsState) doRegister(reg protocol.NodeRegistration, o *nsOwner) {
	s.lastNodeID = reg.NodeID
	s.lastOwner = o
	seed := sha256Seed("ns-node:" + reg.NodeID)
	nodePriv := ed25519.NewKeyFromSeed(seed)
	reg.PubKey = hex.EncodeToString(nodePriv.Public().(ed25519.PublicKey))
	if reg.BridgeToken == "" {
		reg.BridgeToken = "tok-" + reg.NodeID
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	r.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if o != nil {
		signReq(r, o.priv, body)
	}
	w := httptest.NewRecorder()
	s.b.register(w, r)
	s.lastCode = w.Code
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	s.lastMsg = resp.Error.Message
	if reg.BridgeURL != "" {
		s.secrets = append(s.secrets, reg.BridgeURL)
	}
	// track the (secret) node pubkey + node id so the no-leak assertion catches them.
	s.secrets = append(s.secrets, reg.PubKey, reg.NodeID, reg.BridgeToken)
}

func (s *nsState) getVoices() {
	res := s.b.computeVoices().(map[string]any)
	s.voices, _ = res["voices"].([]voiceView)
	b, _ := json.Marshal(res)
	s.payload = string(b)
}

func (s *nsState) byRaw(id string) (voiceView, bool) {
	for _, v := range s.voices {
		if v.ID == id {
			return v, true
		}
	}
	return voiceView{}, false
}
func (s *nsState) byNamespaced(id string) (voiceView, bool) {
	for _, v := range s.voices {
		if v.NamespacedID == id {
			return v, true
		}
	}
	return voiceView{}, false
}

// --- Given / When / Then steps ----------------------------------------------

func (s *nsState) brokerScreeningRequired() error { s.screenAllow(true); return nil }
func (s *nsState) ownerLoggedIn(_, login string) error {
	s.owner(login)
	return nil
}

func (s *nsState) registersVoice(login, model, name string) error {
	s.registerVoice(login, model, name)
	return nil
}
func (s *nsState) registersAnother(login, model, name string) error {
	s.registerVoice(login, model, name)
	return nil
}
func (s *nsState) registersTwoOffers(login, name1, name2 string) error {
	s.registerTwoOffers(login, name1, name2)
	return nil
}
func (s *nsState) registersPrivate(login, model, name string) error {
	s.registerVoice(login, model, name, true)
	return nil
}
func (s *nsState) registersLongName(login, model string) error {
	s.registerVoice(login, model, strings.Repeat("A", 90))
	return nil
}
func (s *nsState) anonFreeVoice(model, name string) error {
	s.registerAnonVoice(model, name)
	return nil
}
func (s *nsState) chatNode(model string) error {
	s.registerChat(model)
	return nil
}
func (s *nsState) bridgeURLIs(url string) error {
	// re-register the last @bownux voice carrying the bridge URL so the leak check sees it.
	return s.reRegisterLastWithBridge(url)
}
func (s *nsState) localAddr(addr string) error { s.secrets = append(s.secrets, addr); return nil }

// reRegisterLastWithBridge re-registers the SAME node id from the previous register, this
// time carrying a bridge URL (an idempotent re-register that puts a real node address on
// file), so the security-regression scenario can assert /voices never leaks it. Reuses the
// stored last node id + owner so it lands on the same node (same TOFU binding).
func (s *nsState) reRegisterLastWithBridge(url string) error {
	off := protocol.ModelOffer{Model: "m-leak", Modality: protocol.ModalityTTS, Name: "Operator"}
	reg := protocol.NodeRegistration{NodeID: s.lastNodeID, BridgeURL: url, TS: time.Now().Unix(), Offers: []protocol.ModelOffer{off}}
	s.doRegister(reg, s.lastOwner)
	return nil
}

func (s *nsState) ownerBanned(_ string) error {
	o := s.owner("bownux")
	s.b.banOwner(o.pub, "test ban", "")
	return nil
}

func (s *nsState) denylistEnv(list string) error {
	os.Setenv("ROGERAI_VOICE_IMPERSONATION_DENYLIST", list)
	return nil
}
func (s *nsState) screenFlagsName(name string) error { s.screenFlagsText(name); return nil }
func (s *nsState) screenRequiredUnreachable() error {
	if s.modStub != nil {
		s.modStub.Close()
		s.modStub = nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	os.Setenv("MODERATION_URL", url)
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	s.b.mod = loadModeration()
	return nil
}
func (s *nsState) screenDisabled() error {
	if s.modStub != nil {
		s.modStub.Close()
		s.modStub = nil
	}
	os.Setenv("MODERATION_URL", "")
	os.Setenv("ROGERAI_REQUIRE_MODERATION", "")
	s.b.mod = loadModeration()
	return nil
}

func (s *nsState) getVoicesStep() error { s.getVoices(); return nil }

func (s *nsState) listsRaw(id string) error {
	if _, ok := s.byRaw(id); !ok {
		return fmt.Errorf("raw id %q not listed; %d voices", id, len(s.voices))
	}
	return nil
}
func (s *nsState) noRaw(id string) error {
	if _, ok := s.byRaw(id); ok {
		return fmt.Errorf("raw id %q should NOT be listed", id)
	}
	return nil
}
func (s *nsState) listsNamespaced(id string) error {
	if _, ok := s.byNamespaced(id); !ok {
		got := make([]string, 0, len(s.voices))
		for _, v := range s.voices {
			got = append(got, v.NamespacedID)
		}
		return fmt.Errorf("namespaced_id %q not listed; got %v", id, got)
	}
	return nil
}
func (s *nsState) noNamespaced(id string) error {
	if _, ok := s.byNamespaced(id); ok {
		return fmt.Errorf("namespaced_id %q should NOT be listed", id)
	}
	return nil
}
func (s *nsState) rawHasNamespaced(rawID, nsID string) error {
	v, ok := s.byRaw(rawID)
	if !ok {
		return fmt.Errorf("raw id %q not listed", rawID)
	}
	if v.NamespacedID != nsID {
		return fmt.Errorf("raw %q namespaced_id = %q, want %q", rawID, v.NamespacedID, nsID)
	}
	return nil
}
func (s *nsState) operatorIs(nsID, login string) error {
	v, ok := s.byNamespaced(nsID)
	if !ok {
		return fmt.Errorf("namespaced_id %q not listed", nsID)
	}
	if v.Operator != login {
		return fmt.Errorf("operator = %q, want %q", v.Operator, login)
	}
	return nil
}
func (s *nsState) operatorFieldIs(login string) error {
	for _, v := range s.voices {
		if v.Operator == login {
			return nil
		}
	}
	return fmt.Errorf("no voice with operator %q; %d voices", login, len(s.voices))
}
func (s *nsState) operatorHasNoAt() error {
	for _, v := range s.voices {
		if strings.HasPrefix(v.Operator, "@") {
			return fmt.Errorf("operator %q must not carry an @ prefix", v.Operator)
		}
	}
	return nil
}
func (s *nsState) distinctEntries() error {
	if len(s.voices) < 2 {
		return fmt.Errorf("expected >=2 distinct voices, got %d", len(s.voices))
	}
	return nil
}
func (s *nsState) namespacedNotContain(sub string) error {
	for _, v := range s.voices {
		if strings.Contains(v.NamespacedID, sub) {
			return fmt.Errorf("namespaced_id %q must not contain %q", v.NamespacedID, sub)
		}
	}
	return nil
}
func (s *nsState) notInVoicesAny(id string) error {
	if strings.Contains(s.payload, id) {
		return fmt.Errorf("%q must not appear anywhere in /voices payload: %s", id, s.payload)
	}
	return nil
}
func (s *nsState) emptyList() error {
	if len(s.voices) != 0 {
		return fmt.Errorf("expected empty voices list, got %d", len(s.voices))
	}
	return nil
}
func (s *nsState) voiceNameSegAtMost(n int) error {
	found := false
	for _, v := range s.voices {
		if v.NamespacedID == "" {
			continue
		}
		found = true
		seg := v.NamespacedID
		if i := strings.LastIndex(seg, "/"); i >= 0 {
			seg = seg[i+1:]
		}
		if runes := []rune(seg); len(runes) > n {
			return fmt.Errorf("voice-name segment %q is %d runes, want <= %d", seg, len(runes), n)
		}
	}
	if !found {
		return fmt.Errorf("no namespaced voice listed to bound")
	}
	return nil
}
func (s *nsState) onlyOneNamespaced(id string) error {
	count := 0
	for _, v := range s.voices {
		if v.NamespacedID == id {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("expected exactly one %q, got %d", id, count)
	}
	return nil
}
func (s *nsState) noBownuxVoice() error {
	for _, v := range s.voices {
		if strings.HasPrefix(v.NamespacedID, "@bownux/") {
			return fmt.Errorf("a @bownux voice %q is listed, want none", v.NamespacedID)
		}
	}
	return nil
}
func (s *nsState) noAddressLeak() error {
	for _, sec := range s.secrets {
		if sec == "" {
			continue
		}
		if strings.Contains(s.payload, sec) {
			return fmt.Errorf("SECURITY: /voices leaked %q", sec)
		}
	}
	return nil
}
func (s *nsState) noHostFields() error {
	var probe map[string]any
	_ = json.Unmarshal([]byte(s.payload), &probe)
	arr, _ := probe["voices"].([]any)
	for _, e := range arr {
		m, _ := e.(map[string]any)
		for k := range m {
			lk := strings.ToLower(k)
			for _, bad := range []string{"host", "ip", "bridge", "pubkey", "node", "addr", "url"} {
				if lk == "sample_url" {
					continue // the ONLY allowed url is a broker/CDN sample, never a node url
				}
				if strings.Contains(lk, bad) {
					return fmt.Errorf("SECURITY: /voices exposes forbidden field %q", k)
				}
			}
		}
	}
	return nil
}
func (s *nsState) everyOpOnlyLogin() error { return s.noHostFields() }

// outcome assertions
func (s *nsState) rejectedDuplicate() error { return s.rejected("duplicate", "already") }
func (s *nsState) rejectedEmpty() error     { return s.rejected("empty", "name") }
func (s *nsState) rejectedImpersonation() error {
	return s.rejected("impersonat", "chat", "model", "reserved")
}
func (s *nsState) rejectedByScreen() error {
	if s.lastCode == http.StatusOK {
		return fmt.Errorf("register succeeded (%d), want rejected by screen; msg=%q", s.lastCode, s.lastMsg)
	}
	return nil
}
func (s *nsState) rejected503() error {
	if s.lastCode != http.StatusServiceUnavailable {
		return fmt.Errorf("register = %d (%q), want 503 fail-closed", s.lastCode, s.lastMsg)
	}
	return nil
}
func (s *nsState) screenNotTheReason() error {
	// a clean voice under this posture must NOT be rejected by the screen (503/451).
	if s.lastCode == http.StatusServiceUnavailable || s.lastCode == http.StatusUnavailableForLegalReasons {
		return fmt.Errorf("register = %d (%q), screen must not be the reason", s.lastCode, s.lastMsg)
	}
	return nil
}

// rejected asserts a non-200 whose message mentions one of the given tokens (case-insensitive).
func (s *nsState) rejected(tokens ...string) error {
	if s.lastCode == http.StatusOK {
		return fmt.Errorf("register succeeded (%d), want rejected (%v); msg=%q", s.lastCode, tokens, s.lastMsg)
	}
	low := strings.ToLower(s.lastMsg)
	for _, tok := range tokens {
		if strings.Contains(low, tok) {
			return nil
		}
	}
	return fmt.Errorf("reject message %q mentions none of %v", s.lastMsg, tokens)
}

func TestVoiceNamespacingBDD(t *testing.T) {
	st := &nsState{}
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
			sc.Step(`^the broker with content screening configured and required$`, st.brokerScreeningRequired)
			sc.Step(`^an owner "([^"]*)" \(GitHub login "([^"]*)"\) who has logged in$`, st.ownerLoggedIn)

			// register steps
			sc.Step(`^"([^"]*)" registers an on-air tts offer with model "([^"]*)" named "([^"]*)"$`, st.registersVoice)
			sc.Step(`^"([^"]*)" registers another on-air tts offer with model "([^"]*)" named "([^"]*)"$`, st.registersAnother)
			sc.Step(`^"([^"]*)" registers one node with two tts offers named "([^"]*)" and "([^"]*)"$`, st.registersTwoOffers)
			sc.Step(`^"([^"]*)" registers a PRIVATE tts node with model "([^"]*)" named "([^"]*)"$`, st.registersPrivate)
			sc.Step(`^"([^"]*)" registers an on-air tts offer with model "([^"]*)" named a 90-character name$`, st.registersLongName)
			sc.Step(`^an anonymous free tts node offering model "([^"]*)" named "([^"]*)"$`, st.anonFreeVoice)
			sc.Step(`^a chat node offering "([^"]*)"$`, st.chatNode)
			sc.Step(`^@bownux's node bridge URL is "([^"]*)"$`, st.bridgeURLIs)
			sc.Step(`^its local address is "([^"]*)"$`, st.localAddr)
			sc.Step(`^"([^"]*)" is owner-banned$`, st.ownerBanned)
			sc.Step(`^the impersonation denylist is set to "([^"]*)" via the environment$`, st.denylistEnv)
			sc.Step(`^a moderation screen that flags the name "([^"]*)"$`, st.screenFlagsName)
			sc.Step(`^content screening is required but unreachable$`, st.screenRequiredUnreachable)
			sc.Step(`^content screening is disabled$`, st.screenDisabled)

			// When
			sc.Step(`^an anonymous GET /voices arrives$`, st.getVoicesStep)

			// Then — listings
			sc.Step(`^a voice with raw id "([^"]*)" is listed$`, st.listsRaw)
			sc.Step(`^no voice with raw id "([^"]*)" is listed$`, st.noRaw)
			sc.Step(`^a voice with namespaced_id "([^"]*)" is listed$`, st.listsNamespaced)
			sc.Step(`^that voice's namespaced_id is "([^"]*)"$`, func(id string) error {
				// scoped to the roger-operator-voice raw id used in the two scenarios that phrase it this way
				return st.rawHasNamespaced("roger-operator-voice", id)
			})
			sc.Step(`^no voice has namespaced_id "([^"]*)"$`, st.noNamespaced)
			sc.Step(`^that voice's operator is "([^"]*)"$`, func(login string) error { return st.operatorIs("@bownux/1950s-operator", login) })
			sc.Step(`^the two voices are distinct entries$`, st.distinctEntries)
			sc.Step(`^"([^"]*)" never appears in /voices as a raw id or a namespaced id$`, st.notInVoicesAny)
			sc.Step(`^the voices list is empty$`, st.emptyList)
			sc.Step(`^the listed voice's namespaced_id voice-name segment is at most (\d+) runes$`, func(n int) error { return st.voiceNameSegAtMost(n) })
			sc.Step(`^only one "([^"]*)" is ever listed$`, st.onlyOneNamespaced)
			sc.Step(`^no voice with a namespaced_id containing "([^"]*)" is listed$`, st.namespacedNotContain)
			sc.Step(`^no "@bownux/\.\.\." voice is listed$`, func() error { return st.noBownuxVoice() })

			// Then — attribution + security
			sc.Step(`^no pubkey, node id, bridge URL, or IP appears anywhere in the response$`, st.noAddressLeak)
			sc.Step(`^the voice's operator field is "([^"]*)"$`, st.operatorFieldIs)
			sc.Step(`^it carries no "@" prefix in the operator field$`, st.operatorHasNoAt)
			sc.Step(`^no field named for a host, ip, bridge, url-to-node, or pubkey exists$`, st.noHostFields)
			sc.Step(`^the response body contains neither the bridge URL nor the local address$`, st.noAddressLeak)
			sc.Step(`^every voice with an operator exposes only the login handle, never an address$`, st.everyOpOnlyLogin)

			// Then — outcomes
			sc.Step(`^the registration is rejected as a duplicate voice name for this operator$`, st.rejectedDuplicate)
			sc.Step(`^the registration is rejected as an empty voice name$`, st.rejectedEmpty)
			sc.Step(`^the registration is rejected as chat-model impersonation$`, st.rejectedImpersonation)
			sc.Step(`^the registration is rejected with the screen's status$`, st.rejectedByScreen)
			sc.Step(`^the registration is rejected 503 \(fail closed\), not served unscreened$`, st.rejected503)
			sc.Step(`^the screen is not the reason for any rejection$`, st.screenNotTheReason)
			sc.Step(`^the offer was rejected at register as chat-model impersonation$`, st.rejectedImpersonation)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/voice/namespacing_attribution.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/namespacing_attribution behavior scenarios failed (see godog output above)")
	}
}

// nsTestStore returns a REAL store: an ephemeral Postgres when ROGERAI_TEST_DATABASE_URL is
// set (the cover-gate provisions one; matches the money BDD suites — no mocks), else the
// in-memory reference store. The owner-attribution path this suite exercises
// (BindOwner/BindNode/AccountOfNode/OwnerByPubkey) is identical across both backends.
func nsTestStore(t *testing.T) store.Store {
	t.Helper()
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		pg, err := store.NewPostgres(dsn)
		if err != nil {
			t.Fatalf("open test Postgres: %v", err)
		}
		return pg
	}
	return store.NewMem()
}

func sha256Seed(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// newNodeSalt returns a short random hex string, unique per scenario, so node ids never
// collide with a stale Postgres TOFU binding from a prior scenario or a prior test run.
func newNodeSalt() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
