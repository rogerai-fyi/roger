package main

// stream_cost_meter_test.go pins the SSE COST METER (founder ruling 2026-07-07): the
// streaming relay path flushes its response headers BEFORE any output, so a stream can
// never carry the X-RogerAI-Cost header - which made every downstream header-based spend
// accumulator (the local proxy's per-session budget) a silent NO-OP for stream:true
// traffic, exactly what agent CLIs default to. relayStream therefore emits the billed
// cost at STREAM END as a spec-compliant SSE comment line `: rogerai-cost=<amount>`
// (parsers ignore comment lines by spec, so no client breaks). This drives the REAL
// relay round-trip (b.relay -> relayStream -> node responder -> settle) and pins:
//   1. a settled stream's body carries exactly one `: rogerai-cost=` comment whose
//      amount equals the settled cost;
//   2. the streaming response still carries NO X-RogerAI-Cost header (the wire shape
//      the proxy's stream-faithful stub reproduces);
//   3. a non-streaming relay is untouched (header meter, no comment).

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func TestStreamEmitsSSECostMeterComment(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
		// PriceIn 0 -> the settled cost is exactly completion_tokens * out / 1e6,
		// making the metered amount assertion unambiguous.
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0, PriceOut: 1.0, Ctx: 4096}},
	}
	b.lastSeen["paid"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 2), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["paid"] = tun
	_ = db.BindNode("paid", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 9, Login: "consumer", Pubkey: userPubHex})
	_, _ = db.AddCredits("u_gh_9", 1e9)

	// Node responder: answer every dispatched job with a signed receipt for 1000
	// completion tokens (cost = 1000 * 1.0 / 1e6 = 0.001).
	go func() {
		for job := range tun.jobs {
			rec := protocol.UsageReceipt{
				RequestID: job.ID, NodeID: "paid", Model: "m",
				PromptTokens: 0, CompletionTokens: 1000, TS: time.Now().Unix(),
			}
			rec.SignNode(nodePriv)
			res := protocol.JobResult{
				ID: job.ID, Status: 200,
				Body:    []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
				Receipt: rec,
			}
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- res
			}
		}
	}()

	doRelay := func(stream bool) *httptest.ResponseRecorder {
		body := []byte(`{"model":"m","max_tokens":1000}`)
		if stream {
			body = []byte(`{"model":"m","max_tokens":1000,"stream":true}`)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
		signReq(r, userPriv, body)
		w := httptest.NewRecorder()
		b.relay(w, r)
		return w
	}

	const wantCost = 1000 * 1.0 / 1e6 // 0.001

	// 1+2) STREAM: the body carries exactly one meter comment with the settled cost,
	//      and the response has NO X-RogerAI-Cost header (headers pre-flushed).
	sw := doRelay(true)
	if got := sw.Header().Get("X-RogerAI-Cost"); got != "" {
		t.Errorf("streaming response carried X-RogerAI-Cost=%q; a stream must NOT carry the header meter (headers flush before output)", got)
	}
	body := sw.Body.String()
	const prefix = ": rogerai-cost="
	if n := strings.Count(body, prefix); n != 1 {
		t.Fatalf("streamed body carries %d %q comments, want exactly 1; body=%q", n, prefix, body)
	}
	rest := body[strings.Index(body, prefix)+len(prefix):]
	amount := rest
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		amount = rest[:i]
	}
	metered, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil {
		t.Fatalf("meter comment amount %q does not parse: %v", amount, err)
	}
	if !approxEq(metered, wantCost) {
		t.Errorf("meter comment reports %.6f, want the settled cost %.6f", metered, wantCost)
	}
	// The comment must be a whole SSE COMMENT LINE (starts the line with ':'), so
	// spec-compliant parsers skip it - never spliced into a data: frame.
	for _, ln := range strings.Split(body, "\n") {
		if strings.Contains(ln, prefix) && !strings.HasPrefix(ln, ":") {
			t.Errorf("meter comment is not a standalone SSE comment line: %q", ln)
		}
	}
	// And the ledger settled the SAME amount the comment reports (meter honesty).
	ents, _ := db.RecentByUser("u_gh_9", 10)
	if len(ents) != 1 {
		t.Fatalf("want 1 settled entry after the stream, got %d", len(ents))
	}
	if !approxEq(ents[0].Cost, metered) {
		t.Errorf("ledger settled %.6f but the meter comment reported %.6f - they must match", ents[0].Cost, metered)
	}

	// 3) NON-STREAM: header meter as before, and no comment spliced into a JSON body.
	nw := doRelay(false)
	if got := nw.Header().Get("X-RogerAI-Cost"); got == "" {
		t.Errorf("non-streaming response lost its X-RogerAI-Cost header meter")
	}
	if strings.Contains(nw.Body.String(), prefix) {
		t.Errorf("non-streaming JSON body must not carry the SSE meter comment: %q", nw.Body.String())
	}
}
