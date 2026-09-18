package client

// Executable spec: features/edge/session_attribution.feature (@client) - the local proxy
// hands each relayed response's receipt to one optional callback. A REAL proxy handler
// over a REAL stub broker (httptest); the program is a real HTTP client of the proxy.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
)

type receiptBDD struct {
	t       *testing.T
	broker  *httptest.Server
	mode    string // "receipt" | "none" | "refuse"
	handler http.Handler
	key     string

	mu   sync.Mutex
	got  []protocol.UsageReceipt
	resp *httptest.ResponseRecorder
}

func (s *receiptBDD) reset() {
	if s.broker != nil {
		s.broker.Close()
	}
	s.broker, s.handler, s.key, s.mode = nil, nil, "", ""
	s.got, s.resp = nil, nil
}

func (s *receiptBDD) startBroker() {
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/discover") {
			_, _ = io.WriteString(w, `{"offers":[{"node_id":"house-or-1","model":"gpt-oss-120b","price_out":0.5,"price_in":0.2,"online":true}]}`)
			return
		}
		if strings.HasPrefix(s.mode, "stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-RogerAI-Provider", "house-or-1")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			_, _ = io.WriteString(w, ": rogerai-cost=0.001\n\n")
			switch s.mode {
			case "stream":
				_, _ = io.WriteString(w, ": rogerai-receipt="+protocol.EncodeReceipt(protocol.UsageReceipt{
					RequestID: "req-77", NodeID: "house-or-1", Model: "gpt-oss-120b", CompletionTokens: 9})+"\n\n")
			case "stream-bad":
				_, _ = io.WriteString(w, ": rogerai-receipt={not json\n\n")
			}
			return
		}
		switch s.mode {
		case "receipt":
			w.Header().Set("X-RogerAI-Provider", "house-or-1")
			w.Header().Set("X-RogerAI-Cost", "0.0042")
			w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(protocol.UsageReceipt{
				RequestID: "req-77", NodeID: "house-or-1", Model: "gpt-oss-120b", CompletionTokens: 9}))
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`)
		case "refuse":
			w.Header().Set("X-RogerAI-Provider", "house-or-1")
			w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(protocol.UsageReceipt{
				RequestID: "req-78", NodeID: "house-or-1", Model: "gpt-oss-120b", VoidReason: "over-limit"}))
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":{"message":"over-limit","type":"insufficient_quota"}}`)
		default:
			w.Header().Set("X-RogerAI-Provider", "house-or-1")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`)
		}
	}))
}

func (s *receiptBDD) sink(rec protocol.UsageReceipt) {
	s.mu.Lock()
	s.got = append(s.got, rec)
	s.mu.Unlock()
}

func (s *receiptBDD) proxyWith(mode string, withCallback bool) error {
	s.mode = mode
	s.startBroker()
	s.key = NewSessionKey()
	opts := ProxyOptions{Broker: s.broker.URL, User: "u", Model: "gpt-oss-120b", SessionKey: s.key}
	if withCallback {
		opts.OnReceipt = s.sink
	}
	s.handler = ProxyHandler(opts)
	return nil
}

func (s *receiptBDD) proxyReceipts() error   { return s.proxyWith("receipt", true) }
func (s *receiptBDD) proxyStream() error     { return s.proxyWith("stream", true) }
func (s *receiptBDD) proxyStreamCost() error { return s.proxyWith("stream-cost-only", true) }
func (s *receiptBDD) proxyStreamBad() error  { return s.proxyWith("stream-bad", true) }

func (s *receiptBDD) relayStreamed() error {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"x","stream":true,"messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	s.resp = httptest.NewRecorder()
	s.handler.ServeHTTP(s.resp, req)
	return nil
}

func (s *receiptBDD) receiptCommentPassedThrough() error {
	body := s.resp.Body.String()
	if !strings.Contains(body, ": rogerai-receipt=") || !strings.Contains(body, "pong") {
		return fmt.Errorf("the stream reaching the program is not the broker's:\n%s", body)
	}
	return nil
}
func (s *receiptBDD) proxyNoReceipt() error  { return s.proxyWith("none", true) }
func (s *receiptBDD) proxyRefuses() error    { return s.proxyWith("refuse", true) }
func (s *receiptBDD) proxyNoCallback() error { return s.proxyWith("receipt", false) }

func (s *receiptBDD) useWithCallback() error {
	s.mode = "receipt"
	s.startBroker()
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	withStdin(s.t, "y\n")
	old, oldServe := newProxyHandler, useServe
	newProxyHandler = func(o ProxyOptions) http.Handler {
		s.key = o.SessionKey
		s.handler = ProxyHandler(o)
		return s.handler
	}
	useServe = func(string, http.Handler) error { return nil }
	s.t.Cleanup(func() { newProxyHandler, useServe = old, oldServe })
	return Use(s.broker.URL, "u", "gpt-oss-120b", UseOptions{Port: 4321, OnReceipt: s.sink})
}

func (s *receiptBDD) relayOnce() error {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	s.resp = httptest.NewRecorder()
	s.handler.ServeHTTP(s.resp, req)
	return nil
}

func (s *receiptBDD) exactlyOne() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) != 1 {
		return fmt.Errorf("callback got %d receipts: %+v", len(s.got), s.got)
	}
	return nil
}

func (s *receiptBDD) namesTheReceipt() error {
	r := s.got[0]
	if r.RequestID != "req-77" || r.Model != "gpt-oss-120b" || r.NodeID != "house-or-1" {
		return fmt.Errorf("receipt = %+v", r)
	}
	return nil
}

func (s *receiptBDD) replyUnchanged() error {
	if s.resp.Code != http.StatusOK || !strings.Contains(s.resp.Body.String(), "pong") {
		return fmt.Errorf("status %d body %s", s.resp.Code, s.resp.Body.String())
	}
	return nil
}

func (s *receiptBDD) notCalled() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) != 0 {
		return fmt.Errorf("callback was called with %+v", s.got)
	}
	return nil
}

func (s *receiptBDD) gotVoid() error {
	if err := s.exactlyOne(); err != nil {
		return err
	}
	if s.got[0].VoidReason != "over-limit" || s.got[0].RequestID != "req-78" {
		return fmt.Errorf("receipt = %+v", s.got[0])
	}
	return nil
}

func (s *receiptBDD) sawRefusal() error {
	if s.resp.Code != http.StatusPaymentRequired {
		return fmt.Errorf("status %d", s.resp.Code)
	}
	return nil
}

func TestProxyReceiptCallbackFeature(t *testing.T) {
	st := &receiptBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.broker != nil {
					st.broker.Close()
					st.broker = nil
				}
				return c, nil
			})
			sc.Step(`^a local proxy with a receipt callback over a broker that receipts every turn$`, st.proxyReceipts)
			sc.Step(`^a local proxy with a receipt callback over a broker that sends no receipt header$`, st.proxyNoReceipt)
			sc.Step(`^a local proxy with a receipt callback over a broker that refuses with a voided receipt$`, st.proxyRefuses)
			sc.Step(`^a local proxy with no receipt callback$`, st.proxyNoCallback)
			sc.Step(`^a local proxy with a receipt callback over a stream-faithful broker that ends with a receipt comment$`, st.proxyStream)
			sc.Step(`^a local proxy with a receipt callback over a stream-faithful broker that ends with a cost comment only$`, st.proxyStreamCost)
			sc.Step(`^a local proxy with a receipt callback over a stream-faithful broker that ends with a malformed receipt comment$`, st.proxyStreamBad)
			sc.Step(`^a program relays one streamed turn through the proxy$`, st.relayStreamed)
			sc.Step(`^the receipt comment passes through to the program unchanged$`, st.receiptCommentPassedThrough)
			sc.Step("^`roger use` opened with a receipt callback, confirmed$", st.useWithCallback)
			sc.Step(`^a program relays one turn through (?:the proxy|that endpoint)$`, st.relayOnce)
			sc.Step(`^the callback receives exactly one receipt$`, st.exactlyOne)
			sc.Step(`^the callback receives the receipt$`, st.exactlyOne)
			sc.Step(`^it names the request id, the band and the station the broker named$`, st.namesTheReceipt)
			sc.Step(`^the reply reached the program unchanged$`, st.replyUnchanged)
			sc.Step(`^the reply still reached the program$`, st.replyUnchanged)
			sc.Step(`^the callback is not called$`, st.notCalled)
			sc.Step(`^the callback receives the voided receipt with its reason$`, st.gotVoid)
			sc.Step(`^the program saw the refusal status$`, st.sawRefusal)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@client",
			Paths: []string{"../../features/edge/session_attribution.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the proxy receipt callback scenarios failed")
	}
}
