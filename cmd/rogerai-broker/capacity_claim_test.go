package main

// capacity_claim_test.go is about the LAST self-declared input to placement.
//
// The commit before this removed `hw`, a hardware class string the node typed, from the edge
// capacity divisor - because a permanent multiplier on something a node asserts about itself is
// a lever, whatever it is called. The very next field over was doing the same thing and had
// been all along: concurrentTPS, the "measured" capacity input, was measured off
// rec.CompletionTokens - THE NODE'S OWN CLAIM - three lines below the block that clamps the
// same field to min(claim, broker re-count) for money.
//
// So the money was honest and the ranking was not, and the log line printed "(billed/claim)"
// directly underneath. These tests fail against that code.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
)

// truthfulSidecar is a tokenizer stub that counts one token per whitespace-separated word -
// which is what the node's completion text actually is here, so its answer is the TRUE count
// and any gap between it and the receipt is the node's inflation and nothing else.
func truthfulSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tokens": len(strings.Fields(in.Text)), "exact": true,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// capacityRig stands up a broker with a truthful re-count sidecar, one node, and one funded
// consumer, and returns a function that drives ONE real relay round trip UNDER LOAD with the
// node claiming `claim` completion tokens for a completion of `words` words.
//
// Under load is not decoration: recordServed only folds the throughput into the capacity EWMA
// when at least one other request shared the node at dispatch. That gate is what stops an idle
// canary winning capacity, and it is the gate the review correctly said was NOT the one being
// bypassed here - the claim is inside a genuinely concurrent request.
func capacityRig(t *testing.T, stream bool) (b *broker, drive func(claim, words int)) {
	t.Helper()
	db := store.NewMem()
	b = relayBroker(db)
	b.recount = recountConfig{url: truthfulSidecar(t).URL, tolerance: 0.02,
		strikeTolerance: 0.25, client: &http.Client{Timeout: 5 * time.Second}}

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["n1"] = protocol.NodeRegistration{
		NodeID: "n1", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0, PriceOut: 1.0, Ctx: 4096}},
	}
	b.lastSeen["n1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 4), waiters: map[string]chan protocol.JobResult{}, token: "tok"}
	b.tunnels["n1"] = tun
	_ = db.BindNode("n1", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 9, Login: "consumer", Pubkey: userPubHex})
	_, _ = db.AddCredits("u_gh_9", 1e9)

	claimed, wordCount := 0, 0
	go func() {
		for job := range tun.jobs {
			rec := protocol.UsageReceipt{
				RequestID: job.ID, NodeID: "n1", Model: "m",
				PromptTokens: 0, CompletionTokens: claimed, TS: time.Now().Unix(),
			}
			rec.SignNode(nodePriv)
			text := strings.TrimSpace(strings.Repeat("ok ", wordCount))
			// A REAL SERVE TAKES REAL TIME. Without this the whole round trip is
			// sub-millisecond and tok/s is dominated by scheduler noise rather than by the
			// token count - which would make an assertion about the COUNT pass or fail on
			// timing. Fifty milliseconds puts the elapsed term firmly in the denominator's
			// control and leaves the count as the only thing that moves.
			time.Sleep(50 * time.Millisecond)
			if stream {
				// The streamed path re-counts the text it CAPTURED, so the node has to
				// actually stream it - a receipt with no deltas behind it is the
				// unverifiable-claim case and is voided before any capacity is recorded.
				var sse strings.Builder
				for _, wrd := range strings.Fields(text) {
					sse.WriteString(`data: {"choices":[{"delta":{"content":"` + wrd + ` "}}]}` + "\n")
				}
				sr := httptest.NewRequest(http.MethodPost,
					"/agent/stream?node=n1&job="+job.ID, strings.NewReader(sse.String()))
				sr.Header.Set("Authorization", "Bearer tok")
				b.agentStream(httptest.NewRecorder(), sr)
			}
			body := fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, text)
			res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte(body), Receipt: rec}
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- res
			}
		}
	}()

	drive = func(claim, words int) {
		claimed, wordCount = claim, words
		// ONE OTHER REQUEST ALREADY IN FLIGHT, so this one dispatches at concurrency two and
		// its throughput is admitted as capacity evidence.
		b.metricsMu.Lock()
		b.inflight["n1"]++
		b.metricsMu.Unlock()
		defer func() {
			b.metricsMu.Lock()
			b.inflight["n1"]--
			b.metricsMu.Unlock()
		}()
		payload := `{"model":"m","max_tokens":100000}`
		if stream {
			payload = `{"model":"m","max_tokens":100000,"stream":true}`
		}
		body := []byte(payload)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
		signReq(r, userPriv, body)
		w := httptest.NewRecorder()
		b.relay(w, r)
	}
	return b, drive
}

func measuredCapacity(b *broker) float64 {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.concurrentTPS["n1"]
}

// THE RELAY PATH. A node serves ten real words and claims six thousand output tokens. It is
// BILLED on ten - the clamp three lines above already does that - and it must be RANKED on ten
// too. recordServed has no prior to average against, so a single served-under-load request sets
// the EWMA outright: one lie is the whole measurement.
func TestTheCapacityEstimateUsesTheRecountedCountNotTheClaim(t *testing.T) {
	b, drive := capacityRig(t, false)
	drive(6000, 10)

	measured := measuredCapacity(b)
	require.Greater(t, measured, 0.0, "the under-load sample was never recorded at all")

	// The node held the request for at least 50ms, so ten verified tokens cannot read above
	// 200 tok/s however the scheduler behaves. The claim of 6000 reads around 120000, which is
	// three thousand capacity slots against five.
	require.LessOrEqual(t, measured, 200.0,
		"the capacity EWMA (%.1f tok/s) was measured off the node's CLAIM of 6000 output tokens "+
			"for a ten-word completion; the broker re-counted it at 10 and billed on that", measured)
	require.Less(t, edgeCapacityOf(measured), 10,
		"an over-reporting node won extra edge capacity slots off a number it made up")
}

// THE STREAM PATH, which is the path most real traffic takes. A capacity input verified on one
// path and self-declared on the other is not a fixed capacity input, it is one with a
// `"stream":true` bypass.
func TestTheStreamedCapacityEstimateUsesTheRecountedCountToo(t *testing.T) {
	b, drive := capacityRig(t, true)
	drive(6000, 10)

	measured := measuredCapacity(b)
	require.Greater(t, measured, 0.0, "the streamed under-load sample was never recorded at all")
	require.LessOrEqual(t, measured, 200.0,
		"the streamed capacity EWMA (%.1f tok/s) was measured off the node's claim", measured)
	require.Less(t, edgeCapacityOf(measured), 10,
		"an over-reporting node won extra edge capacity slots through the streaming path")
}

// AND THE PLACEMENT CONSEQUENCE, stated as placement rather than as arithmetic: an honest node
// and a boastful one that did identical work must score identically and tie in the P2C draw.
// The `hw` fix asserted exactly this shape one commit ago; the lever simply moved one field
// over, so the assertion has to move with it.
func TestAnInflatedTokenClaimDoesNotMovePlacement(t *testing.T) {
	honestB, honestDrive := capacityRig(t, false)
	honestDrive(10, 10) // ten words, honestly counted
	boastB, boastDrive := capacityRig(t, false)
	boastDrive(6000, 10) // the same ten words, claimed as six thousand

	honestCap := measuredCapacity(honestB)
	boastCap := measuredCapacity(boastB)
	require.InDelta(t, honestCap, boastCap, honestCap*0.5+1,
		"the boaster's measured capacity (%.1f) diverged from the honest node's (%.1f) on "+
			"identical work", boastCap, honestCap)
	require.Equal(t, edgeCapacityOf(honestCap), edgeCapacityOf(boastCap),
		"a token claim bought a bigger concurrency allotment")
	// And the tie-break the P2C draw uses, which is load PER UNIT OF CAPACITY - the same lever
	// wearing a different hat, and the half a scoring-only fix would leave open.
	require.InDelta(t, 1.0/float64(edgeCapacityOf(honestCap)), 1.0/float64(edgeCapacityOf(boastCap)), 1e-9,
		"the boaster still wins the power-of-two-choices draw")
}

// THE PROBE'S SPEED BAND IS A CLAIM TOO, and it feeds a different ranking: b.tps is what
// pickFor's speedFit ranks on, what the minTPS filter drops on, and what /discover shows. A
// canary asks for one bare word, so no tokenizer sidecar is needed to bound it - no tokenizer
// can emit more tokens than the answer has bytes, which is the same zero-doubt floor
// settleRecountPrompt already applies to the input axis.
func TestTheProbeSpeedBandIsBoundedByTheAnswerItActuallyGot(t *testing.T) {
	b := relayBroker(store.NewMem())
	rec := protocol.UsageReceipt{CompletionTokens: 10000}
	res := protocol.JobResult{Status: 200,
		Body:    []byte(`{"choices":[{"message":{"content":"paris"}}]}`),
		Receipt: rec}
	_, tps, _, completed := b.evalCanary(res, time.Second, canaryFingerprint{expect: "paris"})
	require.True(t, completed)
	require.LessOrEqual(t, tps, float64(len("paris")),
		"a canary answering one five-letter word claimed 10000 output tokens and moved the "+
			"speed band by three orders of magnitude (%.0f tok/s)", tps)
	require.Greater(t, tps, 0.0, "an honest short answer must still register a speed sample")
}
