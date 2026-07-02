package main

// audio_error_passthrough_bdd_test.go makes features/voice/error_passthrough.feature EXECUTABLE:
// the founder-approved contract (roger-ios docs/BROKER-VOICE-API.md "Error passthrough") that a
// station-side failure returns HTTP 500 with a SHORT SANITIZED REASON in the standard error body
// — never the node's raw error, never a 502/504 whose body the edge replaces with HTML — while
// every pre-dispatch status and the money invariant (failed synth never charges) stay identical.
//
// Harness mirrors audio_speech_bdd_test.go (relayBroker + a stub node answering the tunnel with a
// JobResult — the exact bytes agent.serve() relays verbatim from the local TTS/STT server, pinned
// by binary_relay.feature) with per-scenario control of the node's status/body/receipt, a real
// url-provider moderation stub, a real grant row, and a real miniredis bus for the
// multi-instance scenario. NO mocks of the code under test: b.audioRelay/b.transcribeRelay run
// for real against store.NewMem().

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type epState struct {
	t *testing.T

	// request knobs
	stt      bool   // hit /v1/audio/transcriptions instead of /v1/audio/speech
	model    string // requested voice id
	input    string // tts text (stt sends binary bytes)
	unsigned bool
	badSig   bool
	anonUser bool
	grant    bool // authorize via a grant bearer instead of a signature
	selfUse  bool // the consumer IS the station's operator

	// wallet
	balance float64

	// station knobs
	noStation  bool
	nodeStatus int
	nodeBody   []byte
	nodeEmpty  bool // status 200 + empty body
	badReceipt bool // receipt signed by the WRONG key
	noReply    bool // never answer (relay timeout)

	// moderation knobs
	modMarker string // the url-stub flags any screened text containing this
	modOutage bool   // the stub OKs the first (input) screen then dies; require=1
	require   bool

	// pre-dispatch knobs
	exhaustRL bool

	// multi-instance
	crossInstance bool

	savedWait time.Duration // non-zero: restore nonStreamRelayWait after the scenario
	// stubJoin closes + joins the scenario's stub-station goroutines in the After
	// hook, BEFORE the next scenario's reset(): an unjoined stub reading epState
	// fields raced the next reset's write (caught by `go test -race`).
	stubJoin []func()

	// results
	code       int
	errMsg     string
	body       string
	ctHeader   string
	costHeader string
	before     float64
	after      float64
	mem        *store.Mem
	wallet     string
	grantID    string
	nodeCalled atomic.Bool
}

func (s *epState) reset(t *testing.T) {
	*s = epState{t: t, model: "@op/voice-a", input: "hi", balance: 10, nodeStatus: 200, nodeBody: []byte("~audio~")}
}

// nodeAnswer builds the stub station's JobResult from the scenario knobs.
func (s *epState) nodeAnswer(job protocol.Job, nodePriv ed25519.PrivateKey) protocol.JobResult {
	s.nodeCalled.Store(true)
	status, body := s.nodeStatus, s.nodeBody
	if s.nodeEmpty {
		status, body = 200, nil
	}
	rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: "v1", Model: "voice-raw", TS: time.Now().Unix()}
	if s.badReceipt {
		_, wrong, _ := ed25519.GenerateKey(nil)
		rec.SignNode(wrong)
	} else {
		rec.SignNode(nodePriv)
	}
	return protocol.JobResult{ID: job.ID, Status: status, Body: body, Receipt: rec}
}

// modStub stands in for the moderation URL provider: flags any screened text containing the
// marker; in outage mode it serves the FIRST screen (the request input) then dies.
func (s *epState) modStub() *httptest.Server {
	var calls atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if s.modOutage && n > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var in struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		flagged := s.modMarker != "" && strings.Contains(in.Input, s.modMarker)
		_, _ = fmt.Fprintf(w, `{"results":[{"flagged":%v}]}`, flagged)
	}))
}

func (s *epState) run() error {
	if s.crossInstance {
		return s.runCrossInstance()
	}
	mem := store.NewMem()
	s.mem = mem
	b := relayBroker(mem)

	if s.modMarker != "" || s.modOutage || s.require {
		stub := s.modStub()
		defer stub.Close()
		b.mod = moderation{provider: "url", url: stub.URL, client: stub.Client(), require: s.require}
	}

	// the station: a namespaced id "@op/<slug>" resolves via resolveNamespacedVoice, which
	// needs the node BOUND to an owner, reg.Station == "op", and an offer whose NAME slugs to
	// the requested voice slug (the offer MODEL is the raw local id, as in prod).
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	modality, voiceName := protocol.ModalityTTS, "voice-a"
	if s.stt {
		modality, voiceName = protocol.ModalitySTT, "ears-a"
	}
	b.nodes["v1"] = protocol.NodeRegistration{
		NodeID: "v1", PubKey: hex.EncodeToString(nodePub), Station: "op",
		Offers: []protocol.ModelOffer{{Model: "voice-raw", Modality: modality, Name: voiceName, PriceIn: 10, PriceOut: 10}},
	}
	b.lastSeen["v1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["v1"] = tun
	if err := mem.BindNode("v1", "oppub"); err != nil {
		return err
	}
	if err := mem.BindOwner(store.Owner{GitHubID: 42, Login: "op", Pubkey: "oppub"}); err != nil {
		return err
	}
	stubDone := make(chan struct{})
	s.stubJoin = append(s.stubJoin, func() { close(tun.jobs); <-stubDone })
	go func() {
		defer close(stubDone)
		job, ok := <-tun.jobs
		if !ok || s.noReply {
			return
		}
		res := s.nodeAnswer(job, nodePriv)
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()
	if s.noStation {
		delete(b.nodes, "v1")
	}

	// the consumer
	userPub, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPub)
	s.wallet = "u_gh_7"
	if s.selfUse {
		// the consumer IS the operator: bind the node to the CONSUMER's pubkey
		if err := mem.BindNode("v1", userPubHex); err != nil {
			return err
		}
	}
	if !s.anonUser {
		if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: userPubHex}); err != nil {
			return err
		}
		if _, err := mem.AddCredits(s.wallet, s.balance); err != nil {
			return err
		}
	}
	if s.grant {
		s.grantID = "g_ep"
		sum := sha256.Sum256([]byte("rog-grant_ep"))
		if err := mem.CreateGrant(store.Grant{
			ID: s.grantID, SecretHash: hex.EncodeToString(sum[:]), Owner: userPubHex, Free: true,
		}); err != nil {
			return err
		}
	}
	if s.exhaustRL {
		// the per-user bucket is keyed on the SIGNED identity (the pubkey-derived id), not
		// the wallet — drain exactly the bucket the handler will consult.
		rlKey := protocol.UserIDFromPubkey(userPubHex)
		for i := 0; i < 100000; i++ {
			if ok, _ := b.rl.allow(rlKey); !ok {
				break
			}
		}
	}

	s.before, _ = mem.PeekBalance(s.wallet)

	var r *http.Request
	var body []byte
	if s.stt {
		body = []byte("RIFF~fake-audio-bytes~")
		r = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions?model="+s.model, strings.NewReader(string(body)))
	} else {
		body = []byte(fmt.Sprintf(`{"model":%q,"input":%q,"response_format":"mp3"}`, s.model, s.input))
		r = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(body)))
	}
	switch {
	case s.grant:
		r.Header.Set("Authorization", "Bearer rog-grant_ep")
	case s.unsigned:
	case s.badSig:
		signReq(r, userPriv, body)
		r.Header.Set(protocol.HeaderSig, "deadbeef") // garble AFTER signing
	default:
		signReq(r, userPriv, body)
	}

	w := httptest.NewRecorder()
	if s.stt {
		b.transcribeRelay(w, r)
	} else {
		b.audioRelay(w, r)
	}
	s.capture(w)
	s.after, _ = mem.PeekBalance(s.wallet)
	return nil
}

// runCrossInstance: the consumer calls instance B while the station "polls" instance A —
// modeled at the bus layer exactly as tunnel.go does it: the node side subscribes the job
// channel and publishes the JobResult on the per-job result channel, over one shared miniredis.
func (s *epState) runCrossInstance() error {
	mr := miniredis.RunT(s.t)
	newVS := func() *valkeyStore {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		if err != nil {
			s.t.Fatalf("newValkeyStore: %v", err)
		}
		s.t.Cleanup(func() { _ = vs.Close() })
		return vs
	}
	mem := store.NewMem()
	s.mem = mem
	bB := relayBroker(mem) // the instance the CONSUMER calls
	bB.shared, bB.multiInstance = newVS(), true

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	bB.nodes["v1"] = protocol.NodeRegistration{
		NodeID: "v1", PubKey: hex.EncodeToString(nodePub), Station: "op",
		Offers: []protocol.ModelOffer{{Model: "voice-raw", Modality: protocol.ModalityTTS, Name: "voice-a", PriceIn: 10}},
	}
	bB.lastSeen["v1"] = time.Now()
	bB.tunnels["v1"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	if err := mem.BindNode("v1", "oppub"); err != nil {
		return err
	}
	if err := mem.BindOwner(store.Owner{GitHubID: 42, Login: "op", Pubkey: "oppub"}); err != nil {
		return err
	}

	// the "instance A" side: a bus subscriber standing in for the node's poll on the peer
	vsNode := newVS()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs, cancelSub, err := vsNode.busSubscribeJobs(ctx, "v1")
	if err != nil {
		return err
	}
	defer cancelSub()
	stubDone := make(chan struct{})
	// The deferred cancel/cancelSub already end the subscription when this func
	// returns; the After hook only needs to JOIN so no stub read outlives the scenario.
	s.stubJoin = append(s.stubJoin, func() { <-stubDone })
	go func() {
		defer close(stubDone)
		raw, ok := <-jobs
		if !ok {
			return
		}
		var job protocol.Job
		if json.Unmarshal(raw, &job) != nil {
			return
		}
		res := s.nodeAnswer(job, nodePriv)
		out, _ := json.Marshal(res)
		_ = vsNode.busPublishResult(job.ID, out)
	}()

	userPub, userPriv, _ := ed25519.GenerateKey(nil)
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: hex.EncodeToString(userPub)}); err != nil {
		return err
	}
	s.wallet = "u_gh_7"
	if _, err := mem.AddCredits(s.wallet, s.balance); err != nil {
		return err
	}
	s.before, _ = mem.PeekBalance(s.wallet)

	body := []byte(fmt.Sprintf(`{"model":%q,"input":%q,"response_format":"mp3"}`, s.model, s.input))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	bB.audioRelay(w, r)
	s.capture(w)
	s.after, _ = mem.PeekBalance(s.wallet)
	return nil
}

func (s *epState) capture(w *httptest.ResponseRecorder) {
	s.code = w.Code
	s.body = w.Body.String()
	s.ctHeader = w.Header().Get("Content-Type")
	s.costHeader = w.Header().Get("X-RogerAI-Cost")
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(w.Body.Bytes(), &e) == nil {
		s.errMsg = e.Error.Message
	}
}

// --- Given steps -------------------------------------------------------------------------

func (s *epState) fundedWallet(dollars float64) error { s.balance = dollars; return nil }
func (s *epState) ttsStation(string, float64) error   { return nil } // Background defaults
func (s *epState) sttStation(string, float64) error   { return nil }
func (s *epState) walletHoldsZero() error             { s.balance = 0; return nil }

func (s *epState) serverAnswers(status int, doc *godog.DocString) error {
	s.nodeStatus, s.nodeBody = status, []byte(doc.Content)
	return nil
}
func (s *epState) sttServerAnswers(status int, doc *godog.DocString) error {
	s.stt, s.model = true, "@op/ears-a"
	return s.serverAnswers(status, doc)
}
func (s *epState) emptyResult() error { s.nodeEmpty = true; return nil }
func (s *epState) binaryErrorBody() error {
	buf := make([]byte, 64<<10)
	for i := range buf {
		buf[i] = byte(i % 251) // not valid UTF-8/JSON
	}
	s.nodeStatus, s.nodeBody = 500, buf
	return nil
}
func (s *epState) longMultibyteReason() error {
	reason := strings.Repeat("é≈", 250) // 500 runes, multibyte
	body, _ := json.Marshal(map[string]string{"error": reason})
	s.nodeStatus, s.nodeBody = 500, body
	return nil
}
func (s *epState) badReceiptResult() error { s.badReceipt = true; return nil }
func (s *epState) sttUnreadableResult() error {
	s.stt, s.model = true, "@op/ears-a"
	s.nodeStatus, s.nodeBody = 200, []byte(`{"not":"a transcription"}`)
	return nil
}
func (s *epState) neverReturns() error {
	s.noReply = true
	// shorten the relay wait for THIS scenario only (restored by the After hook) so the
	// timeout pin doesn't sleep the full production window.
	s.savedWait = nonStreamRelayWait
	nonStreamRelayWait = 300 * time.Millisecond
	return nil
}

func (s *epState) modFlags(text string) error {
	s.modMarker, s.require = text, true
	return nil
}
func (s *epState) modFlagsTheInput() error {
	s.modMarker, s.require = "FLAGGED-INPUT", true
	s.input = "some FLAGGED-INPUT text"
	return nil
}
func (s *epState) modUnavailable() error { s.modOutage, s.require = true, true; return nil }

func (s *epState) rlExhausted() error    { s.exhaustRL = true; return nil }
func (s *epState) grantScoped() error    { s.grant = true; return nil }
func (s *epState) consumerIsOp() error   { s.selfUse = true; return nil }
func (s *epState) twoInstances() error   { s.crossInstance = true; return nil }
func (s *epState) nodeOnPeer() error     { return nil } // modeled inside runCrossInstance
func (s *epState) reqOnInstanceB() error { return s.run() }

// --- When steps --------------------------------------------------------------------------

func (s *epState) unsignedReq(model string) error { s.unsigned, s.model = true, model; return s.run() }
func (s *epState) badSigReq(model string) error   { s.badSig, s.model = true, model; return s.run() }
func (s *epState) anonReq(model string) error     { s.anonUser, s.model = true, model; return s.run() }
func (s *epState) signedReqInput(model, input string) error {
	s.model, s.input = model, input
	if model != "@op/voice-a" {
		s.noStation = true // "@op/no-such-voice": nothing on air for it
	}
	return s.run()
}
func (s *epState) signedReq(model string) error { return s.signedReqInput(model, "hello") }
func (s *epState) signedReqFlagged(model string) error {
	s.model = model
	return s.run() // input was set by modFlagsTheInput
}
func (s *epState) sttReq(model string) error {
	s.stt, s.model = true, model
	return s.run()
}
func (s *epState) grantReq(model, input string) error {
	s.grant, s.model, s.input = true, model, input
	return s.run()
}
func (s *epState) operatorReq(input string) error {
	s.selfUse, s.input = true, input
	return s.run()
}

// --- Then steps --------------------------------------------------------------------------

func (s *epState) statusIs(want int) error {
	if s.code != want {
		return fmt.Errorf("status = %d, want %d (body %s)", s.code, want, s.body)
	}
	return nil
}
func (s *epState) msgIs(want string) error {
	if s.errMsg != want {
		return fmt.Errorf("error message = %q, want %q", s.errMsg, want)
	}
	return nil
}
func (s *epState) msgContains(want string) error {
	if !strings.Contains(s.errMsg, want) {
		return fmt.Errorf("error message = %q, want it to contain %q", s.errMsg, want)
	}
	return nil
}
func (s *epState) msgStartsWith(want string) error {
	if !strings.HasPrefix(s.errMsg, want) {
		return fmt.Errorf("error message = %q, want prefix %q", s.errMsg, want)
	}
	return nil
}
func (s *epState) msgNotContains(leak string) error {
	if strings.Contains(s.errMsg, leak) {
		return fmt.Errorf("error message %q leaks %q", s.errMsg, leak)
	}
	return nil
}
func (s *epState) bodyNotContains(leak string) error {
	if strings.Contains(strings.ToLower(s.body), strings.ToLower(leak)) {
		return fmt.Errorf("response body %q leaks %q", s.body, leak)
	}
	return nil
}
func (s *epState) standardErrorShape() error {
	if s.errMsg == "" {
		return fmt.Errorf("body %q is not the standard {\"error\":{\"message\":...}} shape", s.body)
	}
	return nil
}
func (s *epState) contentTypeIs(want string) error {
	if !strings.HasPrefix(s.ctHeader, want) {
		return fmt.Errorf("Content-Type = %q, want %q", s.ctHeader, want)
	}
	return nil
}
func (s *epState) reasonTruncated(maxRunes int, suffix string) error {
	reason := strings.TrimPrefix(s.errMsg, "station error: ")
	if reason == s.errMsg {
		return fmt.Errorf("error message %q does not carry a station reason", s.errMsg)
	}
	if n := len([]rune(reason)); n > maxRunes {
		return fmt.Errorf("reason is %d runes, want <= %d", n, maxRunes)
	}
	if !strings.HasSuffix(reason, suffix) {
		return fmt.Errorf("reason %q does not end with %q", reason, suffix)
	}
	return nil
}
func (s *epState) validUTF8() error {
	if !utf8.ValidString(s.errMsg) {
		return fmt.Errorf("error message is not valid UTF-8: %q", s.errMsg)
	}
	return nil
}
func (s *epState) noDispatch() error {
	// settle briefly: the stub node goroutine sets nodeCalled asynchronously, so a wrongly
	// dispatched job could otherwise be observed before the goroutine runs (false pass).
	time.Sleep(20 * time.Millisecond)
	if s.nodeCalled.Load() {
		return fmt.Errorf("the station was dispatched but should not have been")
	}
	return nil
}
func (s *epState) noHold() error {
	if s.before != s.after {
		return fmt.Errorf("balance moved %v -> %v; a hold/charge escaped", s.before, s.after)
	}
	return s.noOpenHold() // "no hold" also means none left OPEN, not merely refunded-to-even
}
func (s *epState) walletStillHolds(dollars float64) error {
	if !approxF(s.after, dollars) {
		return fmt.Errorf("wallet holds %v, want %v", s.after, dollars)
	}
	return nil
}
func (s *epState) noOpenHold() error {
	// releasing again returns the amount still held for the request: must be zero for all
	released, _ := s.mem.ReleaseStaleHolds(time.Now().Add(time.Minute))
	if released != 0 {
		return fmt.Errorf("%d hold(s) were still open after the failure", released)
	}
	return nil
}
func (s *epState) noLineage() error {
	rows, _ := s.mem.RecentByUser(s.wallet, 10)
	if len(rows) != 0 {
		return fmt.Errorf("lineage has %d row(s) for the failed request, want 0", len(rows))
	}
	return nil
}
func (s *epState) noCostHeader() error {
	if s.costHeader != "" {
		return fmt.Errorf("X-RogerAI-Cost = %q on a failure, want absent", s.costHeader)
	}
	return nil
}
func (s *epState) grantZeroUsage() error {
	u, err := s.mem.GrantUsageOf(s.grantID, time.Now())
	if err != nil {
		return err
	}
	if u.DayTokens != 0 || u.MonthTokens != 0 {
		return fmt.Errorf("grant usage day=%d month=%d, want 0/0", u.DayTokens, u.MonthTokens)
	}
	return nil
}

func TestVoiceErrorPassthroughBDD(t *testing.T) {
	st := &epState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				// Join the scenario's stub goroutines BEFORE the next reset() can
				// rewrite the state they read (the -race catch).
				for _, join := range st.stubJoin {
					join()
				}
				if st.savedWait != 0 {
					nonStreamRelayWait = st.savedWait
				}
				return ctx, nil
			})

			// Background
			sc.Step(`^a broker with a funded consumer wallet "u_test" holding \$([0-9.]+)$`, st.fundedWallet)
			sc.Step(`^an on-air tts station "([^"]*)" priced at \$([0-9.]+) per 1M input chars$`, st.ttsStation)
			sc.Step(`^an on-air stt station "([^"]*)" priced at \$([0-9.]+) per 1M audio bytes$`, st.sttStation)

			// Givens
			sc.Step(`^the consumer wallet "u_test" holds \$0\.00$`, st.walletHoldsZero)
			sc.Step(`^the station's local server answers (\d+) with body:$`, st.serverAnswers)
			sc.Step(`^the station's local server answers (\d+) with a 64 KiB binary body$`, func(status int) error { return st.binaryErrorBody() })
			sc.Step(`^the station's local server answers 500 with a 500-character error reason containing multibyte runes$`, st.longMultibyteReason)
			sc.Step(`^the station's job result arrives with status 200 and an empty body$`, st.emptyResult)
			sc.Step(`^the station returns a valid result whose receipt signature does not verify$`, st.badReceiptResult)
			sc.Step(`^the stt station returns 200 with a body that is not the transcription shape$`, st.sttUnreadableResult)
			sc.Step(`^the stt station's local server answers (\d+) with body:$`, st.sttServerAnswers)
			sc.Step(`^the station never returns a result within the relay wait$`, st.neverReturns)
			sc.Step(`^the moderation screen flags the text "([^"]*)"$`, st.modFlags)
			sc.Step(`^the moderation screen flags the input text$`, st.modFlagsTheInput)
			sc.Step(`^the moderation screen is unavailable$`, st.modUnavailable)
			sc.Step(`^the consumer "u_test" has exhausted its rate limit$`, st.rlExhausted)
			sc.Step(`^a grant key funded by "u_test" scoped to "([^"]*)"$`, func(string) error { return st.grantScoped() })
			sc.Step(`^the consumer is the operator of "([^"]*)"$`, func(string) error { return st.consumerIsOp() })
			sc.Step(`^two broker instances sharing one bus and one store$`, st.twoInstances)
			sc.Step(`^the station polls instance A while the consumer calls instance B$`, st.nodeOnPeer)

			// Whens
			sc.Step(`^an unsigned /v1/audio/speech request for "([^"]*)" arrives$`, st.unsignedReq)
			sc.Step(`^a /v1/audio/speech request for "([^"]*)" arrives with an invalid signature$`, st.badSigReq)
			sc.Step(`^an anonymous-keypair /v1/audio/speech request for "([^"]*)" arrives$`, st.anonReq)
			sc.Step(`^a signed /v1/audio/speech request for "([^"]*)" with input "([^"]*)" arrives$`, st.signedReqInput)
			sc.Step(`^a signed /v1/audio/speech request for "([^"]*)" with the flagged input arrives$`, st.signedReqFlagged)
			sc.Step(`^a signed /v1/audio/speech request for "([^"]*)" arrives$`, st.signedReq)
			sc.Step(`^a signed /v1/audio/speech request for "([^"]*)" arrives on instance B$`, func(string) error { return st.reqOnInstanceB() })
			sc.Step(`^a signed /v1/audio/transcriptions request for "([^"]*)" arrives$`, st.sttReq)
			sc.Step(`^a grant-key /v1/audio/speech request for "([^"]*)" with input "([^"]*)" arrives$`, st.grantReq)
			sc.Step(`^the operator's signed /v1/audio/speech request with input "([^"]*)" arrives$`, st.operatorReq)

			// Thens
			sc.Step(`^the response status is (\d+)$`, st.statusIs)
			sc.Step(`^the error message is "([^"]*)"$`, st.msgIs)
			sc.Step(`^the error message contains "([^"]*)"$`, st.msgContains)
			sc.Step(`^the error message starts with "([^"]*)"$`, st.msgStartsWith)
			sc.Step(`^the error message does not contain "([^"]*)"$`, st.msgNotContains)
			sc.Step(`^the response body does not contain "([^"]*)"$`, st.bodyNotContains)
			sc.Step(`^the response body is the standard error shape$`, st.standardErrorShape)
			sc.Step(`^the response Content-Type is "([^"]*)"$`, st.contentTypeIs)
			sc.Step(`^the error message reason is at most (\d+) runes and ends with "([^"]*)"$`, st.reasonTruncated)
			sc.Step(`^the error message is valid UTF-8$`, st.validUTF8)
			sc.Step(`^no job is dispatched to the station$`, st.noDispatch)
			sc.Step(`^no hold is placed$`, st.noHold)
			sc.Step(`^the consumer wallet "u_test" still holds \$([0-9.]+)$`, st.walletStillHolds)
			sc.Step(`^no hold remains open for the request$`, st.noOpenHold)
			sc.Step(`^no receipt enters lineage for the request$`, st.noLineage)
			sc.Step(`^the response carries no X-RogerAI-Cost header$`, st.noCostHeader)
			sc.Step(`^the grant records zero usage for the request$`, st.grantZeroUsage)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/voice/error_passthrough.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/error_passthrough behavior scenarios failed (see godog output above)")
	}
}
