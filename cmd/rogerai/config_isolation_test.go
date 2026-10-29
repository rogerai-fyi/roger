package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// useTempConfig isolates configPath() to a throwaway directory for the duration of a
// test, on EVERY platform, and then VERIFIES the isolation actually took effect.
//
// Why this exists (the `b.example` incident): configPath() resolves through
// os.UserConfigDir(), which reads a DIFFERENT env var per OS:
//
//	Linux   -> $XDG_CONFIG_HOME (else $HOME/.config)
//	macOS   -> $HOME/Library/Application Support   (XDG_CONFIG_HOME is IGNORED)
//	Windows -> %AppData%                           (XDG_CONFIG_HOME is IGNORED)
//
// Tests that set only XDG_CONFIG_HOME are therefore isolated on Linux but, on macOS
// and Windows, write to the developer's REAL config - so a test fixture like
// {"broker":"https://b.example"} clobbered a real config and `roger share` then tried
// to register against b.example. We set all three env vars, then assert configPath()
// now lives under the temp dir: if some platform resolves it elsewhere, the test
// FAILS LOUDLY here instead of silently destroying the real config.
func useTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("HOME", dir)            // macOS (~/Library/Application Support) + Linux $HOME/.config fallback
	t.Setenv("AppData", dir)         // Windows (%AppData%)
	t.Setenv("ROGER_BROKER", "")     // don't let an ambient env override leak in
	t.Setenv("ROGER_USER", "")
	// Registered after the env, so it runs BEFORE the env is restored and the dir removed: every Edge
	// host started under this config is stopped while its paths still point here.
	t.Cleanup(func() { drainEdgeHosts(t) })
	if got := configPath(); !strings.HasPrefix(got, dir) {
		t.Fatalf("config isolation FAILED: configPath()=%q is not under the temp dir %q - "+
			"this test would pollute the REAL user config. os.UserConfigDir resolves from a "+
			"per-OS env var; add it to useTempConfig.", got, dir)
	}
	return dir
}

// TestConfigIsolationIsCrossPlatform is the regression guard for the b.example
// incident: it asserts useTempConfig actually redirects configPath() (and the derived
// onAirLockPath) off the real config dir on whatever platform the suite runs on. If a
// future change to configPath() or os.UserConfigDir handling breaks isolation, THIS
// fails first - before any fixture write can reach a developer's real config.
func TestConfigIsolationIsCrossPlatform(t *testing.T) {
	dir := useTempConfig(t)
	if cp := configPath(); !strings.HasPrefix(cp, dir) {
		t.Fatalf("configPath()=%q not isolated under %q", cp, dir)
	}
	if lp := onAirLockPath("any-node"); !strings.HasPrefix(lp, dir) {
		t.Fatalf("onAirLockPath()=%q not isolated under %q", lp, dir)
	}
}

// liveEdgeHosts are the hosts started since the last drain (edgeHostStarted, wired in TestMain).
var liveEdgeHosts struct {
	sync.Mutex
	hosts []*edgeHost
}

func trackEdgeHost(h *edgeHost) {
	liveEdgeHosts.Lock()
	liveEdgeHosts.hosts = append(liveEdgeHosts.hosts, h)
	liveEdgeHosts.Unlock()
}

// drainEdgeHosts stops every host started under this test's config and waits for its serve loop to
// END, not just for stop's grace: a loop still in a pass would write into the next test's config.
func drainEdgeHosts(t *testing.T) {
	liveEdgeHosts.Lock()
	hosts := liveEdgeHosts.hosts
	liveEdgeHosts.hosts = nil
	liveEdgeHosts.Unlock()
	for _, h := range hosts {
		h.stop()
		h.mu.Lock()
		done := h.done
		h.mu.Unlock()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("an Edge host started by %s was still running 30s after it was stopped", t.Name())
		}
	}
}
