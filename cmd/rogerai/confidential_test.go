package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/agent"
)

// TestConfidentialFeedback: the go-live confidential line has exactly three outcomes -
// silent when not requested, a VERIFIED ◆ line when granted, and an explicit
// "running STANDARD" warning (naming the allowlist) when a claim was downgraded. This is
// what closes the silent-downgrade gap for the operator.
func TestConfidentialFeedback(t *testing.T) {
	if s := confidentialFeedback(false, false); s != "" {
		t.Errorf("not requested -> %q, want empty", s)
	}
	if s := confidentialFeedback(false, true); s != "" {
		t.Errorf("not requested (granted irrelevant) -> %q, want empty", s)
	}
	granted := confidentialFeedback(true, true)
	if !strings.Contains(granted, "VERIFIED") || !strings.Contains(granted, "◆") {
		t.Errorf("granted line = %q, want a VERIFIED ◆ line", granted)
	}
	down := confidentialFeedback(true, false)
	if !strings.Contains(down, "NOT granted") || !strings.Contains(down, "STANDARD") ||
		!strings.Contains(strings.ToLower(down), "allowlist") {
		t.Errorf("downgraded line = %q, want the standard-downgrade warning naming the allowlist", down)
	}
}

// TestConfidentialIneligibleMsg: the ineligible guidance must be honest + actionable -
// it names why consumer hardware (Threadripper) does not qualify, points at the standard
// tier (co-signed receipts), and gives the gated apply URL.
func TestConfidentialIneligibleMsg(t *testing.T) {
	m := confidentialIneligibleMsg()
	for _, want := range []string{"SEV-SNP", "GPU", "Threadripper", "roger share", "lineage receipt", confidentialApplyURL} {
		if !strings.Contains(m, want) {
			t.Errorf("confidentialIneligibleMsg() missing %q\n--- got ---\n%s", want, m)
		}
	}
}

// TestCmdShareConfidentialNoDeviceAborts: `roger share --confidential` on a host that is
// not an SEV-SNP CVM (the CI/dev box) must abort with the wrapped ErrNoTEEDevice - BEFORE
// acquiring the on-air lock or registering - rather than send a fake claim. --upstream
// skips local detection so the run reaches the confidential preflight deterministically.
func TestCmdShareConfidentialNoDeviceAborts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubShareSeams(t) // agentStart is never reached, but keep the seams safe

	cfg := config{Broker: "https://b", User: "u"}
	err := cmdShare(cfg, []string{"m1", "--upstream", "http://127.0.0.1:1234/v1", "--confidential"})
	if err == nil || !errors.Is(err, agent.ErrNoTEEDevice) {
		t.Fatalf("cmdShare(--confidential) on a non-CVM host = %v, want wrapped ErrNoTEEDevice", err)
	}
}
