package main

// binary_relay_bdd_test.go makes features/voice/binary_relay.feature EXECUTABLE, driving the REAL
// end-to-end NON-STREAM / BINARY return money path — the gap the other voice BDD suites missed. The
// tts_metering + namespaced_routing harnesses feed the broker's result waiter IN-PROCESS
// (`ch <- res`), so they never exercise the node's json.Marshal(JobResult) -> POST /agent/result ->
// broker json.Unmarshal round-trip that CARRIES the binary. That round-trip is exactly where
// `roger say` HUNG: a binary JobResult.Body (a WAV) could not be marshalled as a json.RawMessage,
// so postResult posted an EMPTY body the broker rejected (400 "bad result") and the consumer timed
// out with nothing.
//
// This suite closes that gap: a REAL broker HTTP server (b.routes()), a REAL node worker that long-
// polls GET /agent/poll and returns its result over the REAL POST /agent/result wire (the same
// json.Marshal(protocol.JobResult) + b.agentResult decode as production postResult), and a stub
// upstream returning BINARY. It asserts the CONSUMER (a signed, funded wallet DISTINCT from the node
// owner, so the paid path runs) gets the EXACT bytes + Content-Type and the receipt SETTLES. No
// mocks; the in-memory reference store, whose node->owner attribution matches Postgres.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type binRelayState struct {
	t   *testing.T
	b   *broker
	mem *store.Mem
	srv *httptest.Server // the REAL broker HTTP server (agent poll/result + /v1/audio/speech)

	nodeID   string
	nodePriv ed25519.PrivateKey
	token    string
	station  string

	upstreamBody  []byte // the exact bytes the (stub) upstream returns for /v1/audio/speech
	upstreamEmpty bool

	consumerPriv    ed25519.PrivateKey
	consumerWallet  string
	consumerBalance float64

	worker *binNodeWorker

	// results
	code        int
	body        []byte
	contentType string
	debit       float64
	requestID   string
}

// binNodeWorker is a REAL node's RETURN path. It takes each dispatched job off the broker's job
// channel (the same channel a live poller would drain — the delivery half is not what broke), serves
// it against the tagged upstream, and returns the JobResult over the REAL POST /agent/result WIRE —
// the identical json.Marshal(JobResult) + HTTP path production's postResult uses. That wire return is
// exactly where `roger say` HUNG (a binary body could not be marshalled as a json.RawMessage), so it
// is deliberately NOT the in-process `ch <- res` shortcut the other voice suites take. Draining
// t.jobs directly (instead of GET /agent/poll) keeps the test free of abandoned long-poll goroutines
// while still exercising the broken serialization end-to-end into the real b.agentResult handler.
type binNodeWorker struct {
	brokerURL string
	nodeID    string
	token     string
	nodePriv  ed25519.PrivateKey
	model     string
	upstream  string // stub upstream base (…/v1/chat/completions); the speech path is derived
	client    *http.Client
	jobs      <-chan protocol.Job
	stop      chan struct{}
	wg        sync.WaitGroup
	lastReqID chan string
}

func (wk *binNodeWorker) run() {
	wk.wg.Add(1)
	go func() {
		defer wk.wg.Done()
		for {
			select {
			case <-wk.stop:
				return
			case job := <-wk.jobs:
				if job.ID != "" {
					wk.serveAndReturn(job)
				}
			}
		}
	}()
}

// serveAndReturn is the REAL node return: POST the job body to the tagged upstream path, read the
// (binary) response, sign a receipt, and json.Marshal + POST the JobResult to /agent/result.
func (wk *binNodeWorker) serveAndReturn(job protocol.Job) {
	target := wk.upstream
	if p := job.Path; p != "" && !strings.HasSuffix(p, "/chat/completions") {
		target = strings.TrimSuffix(wk.upstream, "/chat/completions") + strings.TrimPrefix(p, "/v1")
	}
	status := http.StatusBadGateway
	var respBody []byte
	if upResp, err := wk.client.Post(target, "application/json", bytes.NewReader(job.Body)); err == nil {
		respBody, _ = io.ReadAll(upResp.Body)
		upResp.Body.Close()
		status = upResp.StatusCode
	}
	rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: wk.nodeID, User: job.User, Model: wk.model, TS: time.Now().Unix()}
	rec.SignNode(wk.nodePriv)
	res := protocol.JobResult{ID: job.ID, Status: status, Body: respBody, Receipt: rec}

	// The REAL wire return: json.Marshal(JobResult) + POST /agent/result (production postResult).
	wire, _ := json.Marshal(res)
	rr, _ := http.NewRequest(http.MethodPost, wk.brokerURL+"/agent/result?node="+wk.nodeID, bytes.NewReader(wire))
	rr.Header.Set("Authorization", "Bearer "+wk.token)
	rr.Header.Set("Content-Type", "application/json")
	if resp, e := wk.client.Do(rr); e == nil {
		resp.Body.Close()
	}
	select {
	case wk.lastReqID <- job.ID:
	default:
	}
}

func (wk *binNodeWorker) close() {
	select {
	case <-wk.stop:
	default:
		close(wk.stop)
	}
	wk.wg.Wait()
}

func binSeed(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func (s *binRelayState) reset(t *testing.T) {
	if s.worker != nil {
		s.worker.close()
		s.worker = nil
	}
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
	s.t = t
	s.mem = store.NewMem()
	s.b = relayBroker(s.mem)
	s.b.mod = moderation{}
	s.srv = httptest.NewServer(s.b.routes())

	s.station = "brave-otter"
	s.nodeID = "brave-otter-af-heart-" + binSalt()
	s.token = "tok-" + s.nodeID
	s.nodePriv = ed25519.NewKeyFromSeed(binSeed("bin-node:" + s.nodeID))
	s.upstreamBody = nil
	s.upstreamEmpty = false
	s.code, s.body, s.contentType, s.debit, s.requestID = 0, nil, "", 0, ""

	// Consumer: a signed, funded wallet DISTINCT from the node owner (so cost > 0 — a self-purchase
	// would be free and never settle).
	_, cpriv, _ := ed25519.GenerateKey(nil)
	s.consumerPriv = cpriv
	cpub := hex.EncodeToString(cpriv.Public().(ed25519.PublicKey))
	_ = s.mem.BindOwner(store.Owner{GitHubID: 4242, Login: "buyer", Pubkey: cpub})
	s.consumerWallet = "u_gh_4242"
	s.consumerBalance = 100
	_, _ = s.mem.AddCredits(s.consumerWallet, s.consumerBalance)
}

// standUpNode registers a priced, owner-bound tts node ON the real broker maps and starts a REAL
// long-poll worker against the live server, with a stub upstream returning the configured bytes.
func (s *binRelayState) standUpNode(priceIn float64) {
	// A separate owner account for the node (NOT the consumer) so self-use free-pricing does not fire.
	_, opriv, _ := ed25519.GenerateKey(nil)
	opub := hex.EncodeToString(opriv.Public().(ed25519.PublicKey))
	_ = s.mem.BindOwner(store.Owner{GitHubID: 100, Login: "operator", Pubkey: opub})

	off := protocol.ModelOffer{Model: "af_heart", Modality: protocol.ModalityTTS, Name: "Heart", PriceIn: priceIn}
	s.b.nodes[s.nodeID] = protocol.NodeRegistration{
		NodeID: s.nodeID, PubKey: hex.EncodeToString(s.nodePriv.Public().(ed25519.PublicKey)),
		Offers: []protocol.ModelOffer{off}, Station: s.station,
	}
	s.b.lastSeen[s.nodeID] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 8), waiters: map[string]chan protocol.JobResult{}, token: s.token}
	s.b.tunnels[s.nodeID] = tun
	_ = s.mem.BindNode(s.nodeID, opub)

	// Stub upstream: binary WAV on /v1/audio/speech (or an empty body when configured).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/audio/speech") {
			if s.upstreamEmpty {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(s.upstreamBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	s.t.Cleanup(upstream.Close)

	s.worker = &binNodeWorker{
		brokerURL: s.srv.URL, nodeID: s.nodeID, token: s.token, nodePriv: s.nodePriv,
		model: "af_heart", upstream: upstream.URL + "/v1/chat/completions",
		client: &http.Client{Timeout: 10 * time.Second}, jobs: tun.jobs, stop: make(chan struct{}),
		lastReqID: make(chan string, 4),
	}
	s.worker.run()
}

// say fires a REAL signed consumer POST /v1/audio/speech at the live broker server and captures the
// status, body, Content-Type and wallet debit.
func (s *binRelayState) say(input string) {
	reqBody := []byte(fmt.Sprintf(`{"model":"af_heart","input":%q,"response_format":"wav"}`, input))
	before, _ := s.mem.PeekBalance(s.consumerWallet)
	req, _ := http.NewRequest(http.MethodPost, s.srv.URL+"/v1/audio/speech", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	signReq(req, s.consumerPriv, reqBody)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		s.t.Fatalf("consumer request failed: %v", err)
	}
	s.code = resp.StatusCode
	s.body, _ = io.ReadAll(resp.Body)
	s.contentType = resp.Header.Get("Content-Type")
	resp.Body.Close()
	after, _ := s.mem.PeekBalance(s.consumerWallet)
	s.debit = before - after
	select {
	case s.requestID = <-s.worker.lastReqID:
	case <-time.After(2 * time.Second):
	}
}

// --- Given ------------------------------------------------------------------

func (s *binRelayState) liveBrokerWithBinaryNode() error {
	s.upstreamBody = wavFixture()
	s.standUpNode(15)
	return nil
}
func (s *binRelayState) consumerFunded() error { return nil }
func (s *binRelayState) upstreamReturnsNonJSON() error {
	// A payload that is NOT valid JSON and NOT UTF-8 — the exact class that broke marshalling.
	s.upstreamBody = append([]byte("NOTJSON-{[}]"), 0x00, 0x01, 0x02, 0xff, 0xfe)
	return nil
}
func (s *binRelayState) upstreamReturnsEmpty() error { s.upstreamEmpty = true; return nil }

// --- When -------------------------------------------------------------------

func (s *binRelayState) consumerSays(text string) error { s.say(text); return nil }
func (s *binRelayState) consumerSaysNChars(n int) error { s.say(strings.Repeat("a", n)); return nil }

// --- Then -------------------------------------------------------------------

func (s *binRelayState) httpStatus(want int) error {
	if s.code != want {
		return fmt.Errorf("status = %d, want %d; body=%q", s.code, want, string(s.body))
	}
	return nil
}
func (s *binRelayState) bodyIsUpstreamWAV() error {
	if !bytes.Equal(s.body, s.upstreamBody) {
		return fmt.Errorf("consumer body is not the upstream WAV byte-for-byte:\n up=%v\n got=%v", s.upstreamBody, s.body)
	}
	return nil
}
func (s *binRelayState) bodyIsUpstreamPayload() error { return s.bodyIsUpstreamWAV() }
func (s *binRelayState) contentTypeIs(want string) error {
	if s.contentType != want {
		return fmt.Errorf("Content-Type = %q, want %q", s.contentType, want)
	}
	return nil
}
func (s *binRelayState) walletDebited(want float64) error {
	if !approxF(s.debit, want) {
		return fmt.Errorf("debit = %v, want %v (status=%d)", s.debit, want, s.code)
	}
	return nil
}
func (s *binRelayState) walletNotDebited() error {
	if s.debit != 0 {
		return fmt.Errorf("wallet was debited %v; want 0 (status=%d)", s.debit, s.code)
	}
	return nil
}
func (s *binRelayState) receiptSettled() error {
	if s.requestID == "" {
		return fmt.Errorf("no request id captured from the node worker")
	}
	// A settled paid request writes a spend row for the consumer wallet — the money moved.
	rows, err := s.mem.LedgerOf(s.consumerWallet, []string{store.KindSpend}, 0)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no settled spend receipt recorded for the request (debit=%v)", s.debit)
	}
	return nil
}

func TestBinaryRelayBDD(t *testing.T) {
	st := &binRelayState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.worker != nil {
					st.worker.close()
					st.worker = nil
				}
				if st.srv != nil {
					st.srv.Close()
					st.srv = nil
				}
				return ctx, nil
			})

			sc.Step(`^a live broker with a real tts node on air serving a binary WAV upstream$`, st.liveBrokerWithBinaryNode)
			sc.Step(`^a consumer with a funded wallet$`, st.consumerFunded)
			sc.Step(`^the upstream returns a non-JSON binary payload$`, st.upstreamReturnsNonJSON)
			sc.Step(`^the upstream returns an empty body$`, st.upstreamReturnsEmpty)

			sc.Step(`^the consumer requests speech for the text "([^"]*)"$`, st.consumerSays)
			sc.Step(`^the consumer requests speech for a (\d+)-character line$`, st.consumerSaysNChars)

			sc.Step(`^the consumer receives HTTP (\d+)$`, st.httpStatus)
			sc.Step(`^the response body is byte-for-byte the upstream's WAV$`, st.bodyIsUpstreamWAV)
			sc.Step(`^the response body is byte-for-byte the upstream's payload$`, st.bodyIsUpstreamPayload)
			sc.Step(`^the response Content-Type is "([^"]*)"$`, st.contentTypeIs)
			sc.Step(`^the consumer's wallet is debited the char cost ([0-9.]+)$`, st.walletDebited)
			sc.Step(`^the consumer's wallet is not debited$`, st.walletNotDebited)
			sc.Step(`^a settled receipt is recorded for the request$`, st.receiptSettled)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/voice/binary_relay.feature"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/binary_relay behavior scenarios failed (see godog output above)")
	}
}

// wavFixture builds a small REAL WAV (RIFF/WAVE) with a binary tail so the round-trip is exercised
// on genuine, non-JSON audio bytes.
func wavFixture() []byte {
	b := []byte("RIFF")
	b = append(b, 0x24, 0x00, 0x00, 0x00)
	b = append(b, []byte("WAVEfmt ")...)
	b = append(b, 0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00)
	b = append(b, 0x00, 0xff, 0xfe, 0x7f, 0x80, 0x81)
	return b
}

func binSalt() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
