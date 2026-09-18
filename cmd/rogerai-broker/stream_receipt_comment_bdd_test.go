package main

// Executable spec: features/edge/session_attribution.feature (@broker) - a settled stream
// ends with its receipt beside its cost. Same REAL relay round-trip the SSE cost meter
// test drives (b.relay -> relayStream -> node responder -> settle), no mocks.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type streamReceiptBDD struct {
	t    *testing.T
	body string
	rec  protocol.UsageReceipt
}

// relayStreamed stands a broker up with one paid station whose responder answers every
// job with `tokens` completion tokens, then relays one streamed turn and keeps the body.
func (s *streamReceiptBDD) relayStreamed(tokens int) error {
	db := store.NewMem()
	b := relayBroker(db)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
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
	go func() {
		for job := range tun.jobs {
			rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: "paid", Model: "m",
				CompletionTokens: tokens, TS: time.Now().Unix()}
			rec.SignNode(nodePriv)
			body := []byte(`{"choices":[{"message":{"content":"ok"}}]}`)
			if tokens == 0 {
				body = nil
			}
			res := protocol.JobResult{ID: job.ID, Status: 200, Body: body, Receipt: rec}
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- res
			}
		}
	}()
	body := []byte(`{"model":"m","max_tokens":1000,"stream":true}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)
	s.body = w.Body.String()
	return nil
}

func (s *streamReceiptBDD) settles() error  { return s.relayStreamed(1000) }
func (s *streamReceiptBDD) noOutput() error { return s.relayStreamed(0) }

func (s *streamReceiptBDD) costComment() error {
	if n := strings.Count(s.body, ": rogerai-cost="); n != 1 {
		return fmt.Errorf("%d cost comments, want 1:\n%s", n, s.body)
	}
	return nil
}

func (s *streamReceiptBDD) receiptCommentFollows() error {
	const prefix = ": rogerai-receipt="
	if n := strings.Count(s.body, prefix); n != 1 {
		return fmt.Errorf("%d receipt comments, want 1:\n%s", n, s.body)
	}
	if strings.Index(s.body, prefix) < strings.Index(s.body, ": rogerai-cost=") {
		return fmt.Errorf("the receipt comment precedes the cost comment:\n%s", s.body)
	}
	for _, ln := range strings.Split(s.body, "\n") {
		if strings.Contains(ln, prefix) && !strings.HasPrefix(ln, ":") {
			return fmt.Errorf("the receipt comment is not a standalone SSE comment line: %q", ln)
		}
		if strings.HasPrefix(ln, prefix) {
			rec, err := protocol.DecodeReceipt(strings.TrimPrefix(ln, prefix))
			if err != nil {
				return fmt.Errorf("the receipt comment does not decode: %v: %q", err, ln)
			}
			s.rec = rec
		}
	}
	return nil
}

func (s *streamReceiptBDD) receiptNames() error {
	if s.rec.RequestID == "" || s.rec.NodeID != "paid" || s.rec.Model != "m" || s.rec.CompletionTokens != 1000 {
		return fmt.Errorf("receipt = %+v", s.rec)
	}
	return nil
}

func (s *streamReceiptBDD) neitherComment() error {
	if strings.Contains(s.body, ": rogerai-cost=") || strings.Contains(s.body, ": rogerai-receipt=") {
		return fmt.Errorf("an unsettled stream carries a meter comment:\n%s", s.body)
	}
	return nil
}

func TestStreamReceiptCommentFeature(t *testing.T) {
	st := &streamReceiptBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.body, st.rec = "", protocol.UsageReceipt{}
				return c, nil
			})
			sc.Step(`^a broker relaying a streamed turn that settles$`, st.settles)
			sc.Step(`^a broker relaying a streamed turn whose station produced no output$`, st.noOutput)
			sc.Step(`^the stream ends with the ": rogerai-cost=" comment as today$`, st.costComment)
			sc.Step(`^a ": rogerai-receipt=" comment carrying the settled receipt follows it$`, st.receiptCommentFollows)
			sc.Step(`^the receipt names the request, the station and the model that served$`, st.receiptNames)
			sc.Step(`^the stream carries neither a cost comment nor a receipt comment$`, st.neitherComment)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@broker",
			Paths: []string{"../../features/edge/session_attribution.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the stream receipt comment scenarios failed")
	}
}
