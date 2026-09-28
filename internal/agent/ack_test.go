package agent

// ack_test.go: the node side of features/multinode/node_ack.feature. The end-to-end scenarios
// (the real agent against two real broker instances) live in
// cmd/rogerai-broker/node_ack_bdd_test.go; these pin the node's own rules.

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

func TestAcceptedJobsRemembersForTheTTL(t *testing.T) {
	var a acceptedJobs
	now := time.Now()
	if !a.first("j1", now) {
		t.Fatal("a new id was not reported first")
	}
	if a.first("j1", now.Add(acceptedJobsTTL-time.Second)) {
		t.Fatal("a re-delivered id inside the TTL was reported first (it would be served twice)")
	}
	if !a.first("j1", now.Add(acceptedJobsTTL+time.Second)) {
		t.Fatal("an id older than the TTL was not reported first again")
	}
}

func TestAcceptedJobsIsBounded(t *testing.T) {
	var a acceptedJobs
	old := time.Now().Add(-2 * acceptedJobsTTL)
	for i := 0; i < 4096; i++ {
		a.first(string(rune('a'+i%26))+strings.Repeat("x", i/26), old)
	}
	a.first("fresh", time.Now())
	if n := len(a.at); n > 10 {
		t.Fatalf("expired ids were not pruned: %d remain", n)
	}
}

// A broker without /agent/ack answers 404: the ack is ignored and never triggers a re-register.
func TestAckJobIgnores404AndNeverReregisters(t *testing.T) {
	var acks, registers atomic.Int64
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/ack":
			acks.Add(1)
			gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.RawQuery
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		case "/nodes/register":
			registers.Add(1)
		}
	}))
	defer srv.Close()
	ackJob(Config{Broker: srv.URL, NodeID: "n 1"}, "tok", "job/1")
	if acks.Load() != 1 || registers.Load() != 0 {
		t.Fatalf("acks=%d registers=%d, want 1 ack and no re-register", acks.Load(), registers.Load())
	}
	if gotAuth != "Bearer tok" || gotQuery != "node=n+1&job=job%2F1" {
		t.Fatalf("ack sent auth=%q query=%q", gotAuth, gotQuery)
	}
}

// The poll carries the ack header; each job is acked BEFORE it is served; a re-delivered id is
// acked again but not served again.
func TestPollAcksBeforeServingAndDedupes(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("serve")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()
	var polls atomic.Int64
	var headers atomic.Int64
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/poll":
			if r.Header.Get(ackHeader) == "1" {
				headers.Add(1)
			}
			if n := polls.Add(1); n <= 2 { // the same job twice: a re-delivery after a lost ack
				_ = json.NewEncoder(w).Encode(protocol.Job{ID: "same", Body: []byte(`{"model":"m"}`)})
				return
			}
			time.Sleep(20 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		case "/agent/ack":
			record("ack")
		case "/agent/result":
			record("result")
		}
	}))
	defer broker.Close()
	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{Broker: broker.URL, Upstream: upstream.URL, NodeID: "n1", Model: "m"}
	reg := protocol.NodeRegistration{NodeID: "n1", BridgeToken: "tok"}
	sess := &Session{cfg: cfg, stop: make(chan struct{}), rereg: newReregistrar(broker.URL, reg, priv)}
	go pollLoop(cfg, protocol.ModelOffer{Model: "m"}, priv, sess)
	defer close(sess.stop)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 4 && polls.Load() >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events %v", events)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, ","); got != "ack,serve,result,ack" {
		t.Fatalf("events = %s, want ack,serve,result,ack (ack before serving; the re-delivery acked, not served)", got)
	}
	if headers.Load() != polls.Load() {
		t.Fatalf("%d of %d polls carried the ack header", headers.Load(), polls.Load())
	}
}
