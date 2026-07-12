package main

// reasoning_stream_bdd_test.go makes features/trust/reasoning_stream_output.feature an
// EXECUTABLE godog suite against the REAL streaming relay. A node goroutine streams real SSE
// deltas to /agent/stream (the same wire the production node uses) then posts a signed
// receipt; the broker runs its capture + void + re-count + strike stack for real, with a real
// (in-process) tokenizer sidecar so the recount-discrepancy path is live. No mocks.
//
// It pins the thinking-model fix end-to-end: reasoning deltas are captured (not dropped), so
// an honest reasoning stream is served (not voided), the re-count counts reasoning (no
// discrepancy), and neither the empty-output nor the recount strike fires - so corroboration
// never trips and the honest node is never auto-banned. The TRUE-negative (no text, zero
// tokens) still voids + strikes, keeping the strike useful.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// rsWordCount is the deterministic "tokenizer" the in-process sidecar uses: whitespace words.
// The node claims exactly this many completion tokens for the text it streams, so a correct
// (reasoning-inclusive) broker re-count matches the claim and never flags a discrepancy.
func rsWordCount(text string) int { return len(strings.Fields(text)) }

type rsState struct {
	t *testing.T
	b *broker

	nodePub  ed25519.PublicKey
	nodePriv ed25519.PrivateKey

	// per-scenario stream config
	chunks    []string      // SSE lines the node streams to /agent/stream
	gap       time.Duration // inter-delta delay (idle-timer scenario)
	claimComp int           // completion_tokens the node claims in its receipt
	status    int
	idle      time.Duration // broker idle/void window override (0 = default)

	// results (single-run scenarios)
	spend float64
	earn  float64
}

func (s *rsState) reset() {
	s.nodePub, s.nodePriv, _ = ed25519.GenerateKey(nil)
	s.chunks = nil
	s.gap = 0
	s.claimComp = 0
	s.status = 200
	s.idle = 0
	s.spend, s.earn = 0, 0
	s.b = nil
}

// newStreamBroker builds a broker with the streaming relay wired to a real in-process
// tokenizer sidecar (so settleRecount runs a live re-count) and a funded logged-in consumer.
func (s *rsState) newStreamBroker() (*broker, ed25519.PrivateKey, *nodeTunnel, *httptest.Server) {
	db := store.NewMem()
	b := relayBroker(db)
	b.strikeDecayDays = strikeDecayDays()
	b.strikeCorroborateKinds = strikeCorroborateKinds()
	if s.idle > 0 {
		b.streamIdleTimeout = s.idle
	}
	// Real in-process tokenizer sidecar: counts whitespace words, exact.
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		fmt.Fprintf(w, `{"tokens":%d,"exact":true}`, rsWordCount(req.Text))
	}))
	b.recount = recountConfig{url: sidecar.URL, tolerance: 0.02, strikeTolerance: 0.25, client: &http.Client{Timeout: 2 * time.Second}}

	b.nodes["n1"] = protocol.NodeRegistration{
		NodeID: "n1", PubKey: hex.EncodeToString(s.nodePub), BridgeToken: "tok",
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1.0, PriceOut: 1.0, Ctx: 4096}},
	}
	b.lastSeen["n1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}, token: "tok"}
	b.tunnels["n1"] = tun
	if err := db.BindNode("n1", "op1"); err != nil {
		s.t.Fatal(err)
	}

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	if err := db.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: userPubHex}); err != nil {
		s.t.Fatal(err)
	}
	if _, err := db.AddCredits("u_gh_7", 1e9); err != nil {
		s.t.Fatal(err)
	}
	s.b = b
	return b, userPriv, tun, sidecar
}

// runOneStream drives ONE full streaming relay round-trip on b: a node goroutine answers the
// dispatched job by streaming s.chunks to /agent/stream (optionally spaced by s.gap) then
// posting a signed receipt claiming claimComp completion tokens. Returns after settlement.
func (s *rsState) runOneStream(b *broker, userPriv ed25519.PrivateKey, tun *nodeTunnel, chunks []string, gap time.Duration, claimComp, status int) {
	go func() {
		job := <-tun.jobs
		// Stream the SSE chunks to the broker exactly as a real node's /agent/stream POST does.
		if gap == 0 {
			req := httptest.NewRequest(http.MethodPost, "/agent/stream?node=n1&job="+job.ID, strings.NewReader(strings.Join(chunks, "")))
			req.Header.Set("Authorization", "Bearer tok")
			b.agentStream(httptest.NewRecorder(), req)
		} else {
			pr, pw := io.Pipe()
			req := httptest.NewRequest(http.MethodPost, "/agent/stream?node=n1&job="+job.ID, pr)
			req.Header.Set("Authorization", "Bearer tok")
			done := make(chan struct{})
			go func() { b.agentStream(httptest.NewRecorder(), req); close(done) }()
			for _, c := range chunks {
				_, _ = pw.Write([]byte(c))
				time.Sleep(gap)
			}
			_ = pw.Close()
			<-done
		}
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "n1", Model: "m",
			PromptTokens: 1, CompletionTokens: claimComp, PriceIn: 1.0, PriceOut: 1.0,
			TS: time.Now().Unix(),
		}
		rec.SignNode(s.nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: status, Body: []byte(`{"choices":[{"message":{"content":""}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	reqBody := []byte(`{"model":"m","max_tokens":1000,"stream":true}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	signReq(r, userPriv, reqBody)
	b.relay(httptest.NewRecorder(), r)
}

func (s *rsState) settle() {
	// settleRecount folds the discrepancy into an owner strike from a goroutine; give it a
	// bounded moment to land so a "NOT struck" assertion is not read too early.
	time.Sleep(200 * time.Millisecond)
	s.spend, _ = s.b.db.SpendOf("u_gh_7")
	s.earn, _ = s.b.db.EarningsOf("n1")
}

func (s *rsState) strikesOfKind(kind string) int {
	st, err := s.b.db.StrikesByOwner("op1", 200)
	if err != nil {
		s.t.Fatal(err)
	}
	n := 0
	for _, k := range st {
		if kind == "" || k.Kind == kind {
			n++
		}
	}
	return n
}

// --- reasoning content builders ---------------------------------------------

func rsDelta(field, text string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{%q:%q}}]}`+"\n\n", field, text)
}

// --- Given steps ------------------------------------------------------------

func (s *rsState) bgNode() error     { return nil }
func (s *rsState) bgConsumer() error { return nil }

func (s *rsState) reasoningThenContent() error {
	s.chunks = []string{
		rsDelta("reasoning", "let me think about "),
		rsDelta("reasoning", "the users question "),
		rsDelta("reasoning", "carefully step by "),
		rsDelta("reasoning", "step now "),
		rsDelta("content", "the final answer is 42"),
	}
	return nil
}

func (s *rsState) claimFullCount() error {
	s.claimComp = rsWordCount(rsGeneratedText(s.chunks))
	return nil
}

func (s *rsState) reasoningOnlyLength() error {
	s.chunks = []string{
		rsDelta("reasoning", "the answer requires "),
		rsDelta("reasoning", "a long private deliberation "),
		rsDelta("reasoning", "that never emits content "),
	}
	return nil
}

func (s *rsState) claimReasoningCount() error {
	s.claimComp = rsWordCount(rsGeneratedText(s.chunks))
	return nil
}

func (s *rsState) noTextClaims5() error {
	s.chunks = []string{": keepalive\n\n"} // liveness only, no output text
	s.claimComp = 5
	return nil
}

func (s *rsState) reasoningLastContent() error {
	s.chunks = []string{
		rsDelta("reasoning", "consider the options "),
		rsDelta("reasoning", "weigh them "),
		rsDelta("reasoning", "decide "),
		rsDelta("content", "answer chosen"),
	}
	return nil
}

func (s *rsState) noTextClaims0() error {
	s.chunks = []string{": keepalive\n\n"}
	s.claimComp = 0
	return nil
}

func (s *rsState) shortIdleSpacedDeltas() error {
	s.idle = 120 * time.Millisecond
	s.gap = 40 * time.Millisecond
	// ~6 gaps * 40ms = 240ms total, each 40ms < the 120ms idle window: a reset-on-delta timer
	// never trips; a fixed deadline would abort before the receipt.
	s.chunks = []string{
		rsDelta("reasoning", "slow think one "),
		rsDelta("reasoning", "slow think two "),
		rsDelta("reasoning", "slow think three "),
		rsDelta("reasoning", "slow think four "),
		rsDelta("content", "done at last"),
	}
	return nil
}

// rsGeneratedText is the text the NODE actually generated across its deltas (content + every
// reasoning alias), parsed DIRECTLY from the SSE JSON - independent of the production sseDelta
// capture. The node's usage.completion_tokens counts ALL generated tokens, so an honest node's
// claim is rsWordCount(rsGeneratedText). A correct broker re-count (which now counts reasoning)
// matches it; the pre-fix reasoning-stripped re-count falls far short and false-fires
// recount-discrepancy - which is exactly what this suite pins.
func rsGeneratedText(chunks []string) string {
	var b strings.Builder
	for _, c := range chunks {
		for _, line := range strings.Split(c, "\n") {
			i := strings.IndexByte(line, '{')
			if i < 0 {
				continue
			}
			var d struct {
				Choices []struct {
					Delta struct {
						Content          string `json:"content"`
						Reasoning        string `json:"reasoning"`
						ReasoningContent string `json:"reasoning_content"`
						Thinking         string `json:"thinking"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(line[i:]), &d) != nil {
				continue
			}
			for _, ch := range d.Choices {
				b.WriteString(ch.Delta.Content)
				b.WriteString(ch.Delta.Reasoning)
				b.WriteString(ch.Delta.ReasoningContent)
				b.WriteString(ch.Delta.Thinking)
			}
		}
	}
	return b.String()
}

// --- When steps -------------------------------------------------------------

func (s *rsState) relayStreaming() error {
	b, userPriv, tun, sc := s.newStreamBroker()
	defer sc.Close()
	s.runOneStream(b, userPriv, tun, s.chunks, s.gap, s.claimComp, s.status)
	s.settle()
	return nil
}

func (s *rsState) relayHonestMix() error {
	b, userPriv, tun, sc := s.newStreamBroker()
	defer sc.Close()
	reasoningOnly := []string{
		rsDelta("reasoning", "private chain of "),
		rsDelta("reasoning", "thought only "),
	}
	reasoningContent := []string{
		rsDelta("reasoning", "brief think "),
		rsDelta("content", "the answer"),
	}
	for i := 0; i < 3; i++ {
		s.runOneStream(b, userPriv, tun, reasoningOnly, 0, rsWordCount(rsGeneratedText(reasoningOnly)), 200)
	}
	for i := 0; i < 3; i++ {
		s.runOneStream(b, userPriv, tun, reasoningContent, 0, rsWordCount(rsGeneratedText(reasoningContent)), 200)
	}
	time.Sleep(300 * time.Millisecond) // let async recount observations land
	return nil
}

// --- Then steps -------------------------------------------------------------

func (s *rsState) servedNonZero() error {
	if !(s.spend > 0) {
		return fmt.Errorf("stream must be served and billed a non-zero cost, spend=%.8f", s.spend)
	}
	return nil
}

func (s *rsState) nodeEarns() error {
	if !(s.earn > 0) {
		return fmt.Errorf("a served stream must mint an earning, earn=%.8f", s.earn)
	}
	return nil
}

func (s *rsState) ownerNotStruck() error {
	if n := s.strikesOfKind(""); n != 0 {
		return fmt.Errorf("honest reasoning node must NOT be struck, got %d strike(s)", n)
	}
	return nil
}

func (s *rsState) voidedZero() error {
	if s.spend != 0 {
		return fmt.Errorf("a true-negative empty stream must charge 0, spend=%.8f", s.spend)
	}
	if s.earn != 0 {
		return fmt.Errorf("a true-negative empty stream must mint no earning, earn=%.8f", s.earn)
	}
	return nil
}

func (s *rsState) struckEmptyOutput() error {
	if n := s.strikesOfKind(store.StrikeEmptyOutput); n < 1 {
		return fmt.Errorf("a true-negative empty stream must strike empty-output (strike stays useful), got %d", n)
	}
	return nil
}

func (s *rsState) ownerNotBanned() error {
	if s.b.isOwnerBanned("op1") {
		return fmt.Errorf("an honest reasoning node's owner must NOT be auto-banned")
	}
	return nil
}

func (s *rsState) ownerNoStrikes() error {
	if n := s.strikesOfKind(""); n != 0 {
		return fmt.Errorf("an honest reasoning node's owner must have zero strikes, got %d", n)
	}
	return nil
}

func TestReasoningStreamOutputBDD(t *testing.T) {
	st := &rsState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a reasoning-capable node registered with the broker$`, st.bgNode)
			sc.Step(`^a funded consumer$`, st.bgConsumer)

			sc.Step(`^the node streams 4 reasoning deltas then 1 content delta$`, st.reasoningThenContent)
			sc.Step(`^the node's receipt claims the full reasoning\+content token count$`, st.claimFullCount)
			sc.Step(`^the node streams reasoning deltas only with an empty content and finish_reason length$`, st.reasoningOnlyLength)
			sc.Step(`^the node's receipt claims the reasoning token count$`, st.claimReasoningCount)
			sc.Step(`^the node streams no output text but its receipt claims 5 completion tokens$`, st.noTextClaims5)
			sc.Step(`^the node streams 3 reasoning deltas and puts the only content in the last delta$`, st.reasoningLastContent)
			sc.Step(`^the node streams no output text and its receipt claims 0 completion tokens$`, st.noTextClaims0)
			sc.Step(`^a short idle window and the node streams reasoning deltas spaced across more than that window$`, st.shortIdleSpacedDeltas)
			sc.Step(`^the node serves 3 reasoning-only and 3 reasoning\+content honest streams$`, func() error { return nil })

			sc.Step(`^the consumer relays the streaming request$`, st.relayStreaming)
			sc.Step(`^the consumer relays each streaming request$`, st.relayHonestMix)

			sc.Step(`^the stream is served and settled for a non-zero cost$`, st.servedNonZero)
			sc.Step(`^the node earns for the request$`, st.nodeEarns)
			sc.Step(`^the node's owner is NOT struck$`, st.ownerNotStruck)
			sc.Step(`^the stream is voided at zero cost with the hold refunded$`, st.voidedZero)
			sc.Step(`^the node's owner IS struck for empty output$`, st.struckEmptyOutput)
			sc.Step(`^the node's owner is NOT banned$`, st.ownerNotBanned)
			sc.Step(`^the node's owner has no strikes of any kind$`, st.ownerNoStrikes)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/trust/reasoning_stream_output.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("reasoning-stream-output behavior scenarios failed (see godog output above)")
	}
}
