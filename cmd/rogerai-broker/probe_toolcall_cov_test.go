package main

import (
	"net/http"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// probe_toolcall_cov_test.go covers the LIVE dispatch of the tool-call canary (probeToolCall)
// beyond the pure-verdict + emission scenarios in toolcall_probe_bdd_test.go: the
// single-instance tunnel round-trip that lands a verdict, the no-tunnel early return, and the
// non-2xx transient that never clears. It reuses probe_node_cov_test.go's probeReg/answerProbe
// harness (a real local tunnel), so the real dispatch path runs - no mocks.

// TestProbeToolCallSingleInstancePass: a node that answers the canary with a well-formed
// tool_calls response earns the verified tools bit through the live single-instance path.
func TestProbeToolCallSingleInstancePass(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.toolsOK = map[string]bool{}
	tun, _ := probeReg(b, "tnode")
	answerProbe(tun, http.StatusOK, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{\"ok\":true}"}}]}}]}`)

	b.probeToolCall(b.nodes["tnode"], "m", true)

	b.metricsMu.Lock()
	ok := b.toolsOK[toolKey("tnode", "m")]
	b.metricsMu.Unlock()
	if !ok {
		t.Fatal("a well-formed tool_calls response over the live tunnel did not earn the tools bit")
	}
}

// TestProbeToolCallSingleInstancePlainText: a plain-text answer is a definitive (2xx) verdict
// that leaves the bit unproven; on the authoritative host an existing bit would be cleared.
func TestProbeToolCallSingleInstancePlainText(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.toolsOK = map[string]bool{toolKey("tnode", "m"): true} // previously earned
	tun, _ := probeReg(b, "tnode")
	answerProbe(tun, http.StatusOK, `{"choices":[{"message":{"content":"no tools for me"}}]}`)

	b.probeToolCall(b.nodes["tnode"], "m", true) // authoritative => a definitive fail clears it

	b.metricsMu.Lock()
	ok := b.toolsOK[toolKey("tnode", "m")]
	b.metricsMu.Unlock()
	if ok {
		t.Fatal("a definitive plain-text verdict on the authoritative host did not clear the tools bit")
	}
}

// TestProbeToolCallNon2xxTransient: a non-2xx canary is a TRANSIENT non-verdict - it must not
// clear a previously earned bit, even on the authoritative host.
func TestProbeToolCallNon2xxTransient(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.toolsOK = map[string]bool{toolKey("tnode", "m"): true}
	tun, _ := probeReg(b, "tnode")
	answerProbe(tun, http.StatusTooManyRequests, `{"error":"rate limited"}`)

	b.probeToolCall(b.nodes["tnode"], "m", true)

	b.metricsMu.Lock()
	ok := b.toolsOK[toolKey("tnode", "m")]
	b.metricsMu.Unlock()
	if !ok {
		t.Fatal("a 429 transient non-verdict wrongly cleared the earned tools bit")
	}
}

// TestProbeToolCallNoTunnel: no local tunnel (and not multi-instance) is a clean no-op - the
// canary is never dispatched, so no verdict is recorded.
func TestProbeToolCallNoTunnel(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.toolsOK = map[string]bool{}
	b.nodes["ghost"] = protocol.NodeRegistration{NodeID: "ghost"}
	b.probeToolCall(b.nodes["ghost"], "m", true)
	b.metricsMu.Lock()
	n := len(b.toolsOK)
	b.metricsMu.Unlock()
	if n != 0 {
		t.Error("probeToolCall with no tunnel must not record any verdict")
	}
}

// TestHasVerifiedToolsLocked covers both branches of the register-time re-stamp gate: a node
// with a verified model reports true (re-stamp), a never-verified node reports false (skip).
func TestHasVerifiedToolsLocked(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.toolsOK = map[string]bool{toolKey("n1", "m"): true}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if !b.hasVerifiedToolsLocked("n1") {
		t.Error("hasVerifiedToolsLocked(n1) = false, want true (n1/m is verified)")
	}
	if b.hasVerifiedToolsLocked("other") {
		t.Error("hasVerifiedToolsLocked(other) = true, want false (never verified)")
	}
	// Prefix guard: "n1x" must not match the "n1\x00m" key of a DIFFERENT node.
	if b.hasVerifiedToolsLocked("n") {
		t.Error("hasVerifiedToolsLocked matched on a bare node-id prefix (missing NUL boundary)")
	}
}

// TestStripDeclaredToolsEmpty covers the empty/nil fast-path of the declared-tools strip.
func TestStripDeclaredToolsEmpty(t *testing.T) {
	if got := stripDeclaredTools(nil); got != nil {
		t.Errorf("stripDeclaredTools(nil) = %v, want nil", got)
	}
	if got := stripDeclaredTools([]string{}); len(got) != 0 {
		t.Errorf("stripDeclaredTools([]) = %v, want empty", got)
	}
	if got := stripDeclaredTools([]string{"vision", "tools", "TOOLS"}); len(got) != 1 || got[0] != "vision" {
		t.Errorf("stripDeclaredTools kept a declared tools: %v", got)
	}
}
