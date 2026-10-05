package main

// shared_boot_alert_test.go - a broker configured to share state that cannot reach the shared
// store at boot refuses every request (readinessGate). That is an outage the founder must hear
// about: the health checker raises the existing valkey_down alert while the phase is
// connecting or not configured, and clears it once the store is wired and healthy.

import (
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestValkeyDownPagesWhileNotReadyAtBoot(t *testing.T) {
	for _, phase := range []int32{sharedConnecting, sharedNotConfigured} {
		b, sends := alertBroker(t, "ops@example.com")
		b.shared = nil
		b.sharedPhase.Store(phase)
		b.checkHealthAlerts()
		got := firstAlert(t, sends)
		if s, _ := got["subject"].(string); !strings.Contains(strings.ToLower(s), "valkey") {
			t.Fatalf("phase %d: subject %q, want the valkey_down alert", phase, s)
		}
		b.alertMu.Lock()
		firing := b.alertFiring["valkey_down"]
		b.alertMu.Unlock()
		if !firing {
			t.Fatalf("phase %d: valkey_down not marked firing", phase)
		}
		// Deduped as today: a second tick while still not ready sends nothing new.
		b.checkHealthAlerts()
		noAlert(t, sends)
	}
}

func TestValkeyDownClearsOnceTheBootRetryConnects(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	b.shared = nil
	b.sharedPhase.Store(sharedConnecting)
	b.checkHealthAlerts()
	_ = firstAlert(t, sends)

	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	b.shared = vs
	b.sharedPhase.Store(sharedConnected)
	b.checkHealthAlerts()
	b.alertMu.Lock()
	firing := b.alertFiring["valkey_down"]
	b.alertMu.Unlock()
	if firing {
		t.Fatal("valkey_down still firing after the store connected and is healthy")
	}
}
