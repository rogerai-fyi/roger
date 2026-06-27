package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// probeReg wires a serving node + a live local tunnel into b and returns the tunnel.
func probeReg(b *broker, nodeID string) (*nodeTunnel, ed25519.PrivateKey) {
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes[nodeID] = protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 1, Ctx: 4096}},
	}
	b.lastSeen[nodeID] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels[nodeID] = tun
	return tun, nodePriv
}

// answerProbe reads the probe job and replies with the given status + body on the waiter.
func answerProbe(tun *nodeTunnel, status int, body string) {
	go func() {
		job := <-tun.jobs
		res := protocol.JobResult{ID: job.ID, Status: status, Body: []byte(body),
			Receipt: protocol.UsageReceipt{RequestID: job.ID, CompletionTokens: 5}}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()
}

// TestProbeNodeSingleInstancePass drives the single-instance probeNode round-trip: a node
// that answers 200 with the canary's expected token records a passing (alive) probe -
// trust is stamped probed+OK and the probe count increments.
func TestProbeNodeSingleInstancePass(t *testing.T) {
	b := relayBroker(store.NewMem())
	tun, _ := probeReg(b, "pnode")
	fp := nextCanary(0)
	answerProbe(tun, http.StatusOK, `{"choices":[{"message":{"content":"`+fp.expect+`"}}]}`)

	b.probeNode(b.nodes["pnode"], "m", fp)

	b.metricsMu.Lock()
	tq := b.trust["pnode"]
	b.metricsMu.Unlock()
	if !tq.probed || !tq.probeOK {
		t.Fatalf("trust after pass = %+v, want probed+OK", tq)
	}
	if tq.probes != 1 {
		t.Errorf("probe count = %d, want 1", tq.probes)
	}
}

// TestProbeNodeSingleInstanceDead drives the failure path: a node answering a non-2xx is
// recorded probeDead - trust flips probeOK false and probeFails increments.
func TestProbeNodeSingleInstanceDead(t *testing.T) {
	b := relayBroker(store.NewMem())
	tun, _ := probeReg(b, "dnode")
	answerProbe(tun, http.StatusInternalServerError, `{"error":"boom"}`)

	b.probeNode(b.nodes["dnode"], "m", nextCanary(1))

	b.metricsMu.Lock()
	tq := b.trust["dnode"]
	b.metricsMu.Unlock()
	if tq.probeOK {
		t.Fatalf("trust after non-2xx = %+v, want probeOK=false", tq)
	}
	if tq.probeFails != 1 {
		t.Errorf("probeFails = %d, want 1", tq.probeFails)
	}
}

// TestProbeNodeNoTunnel locks the early-return guard: with no local tunnel (and not
// multi-instance) probeNode is a clean no-op (no trust row written).
func TestProbeNodeNoTunnel(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.nodes["ghost"] = protocol.NodeRegistration{NodeID: "ghost"}
	b.probeNode(b.nodes["ghost"], "m", nextCanary(0))
	b.metricsMu.Lock()
	_, ok := b.trust["ghost"]
	b.metricsMu.Unlock()
	if ok {
		t.Error("probeNode with no tunnel must not record any probe")
	}
}
