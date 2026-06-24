package tui

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// TestListenFreePortFallback: when the configured port is already taken, listenFreePort
// auto-picks a higher free port instead of dead-ending (the TUI tune-in bind fix). It
// must never return the busy port.
func TestListenFreePortFallback(t *testing.T) {
	// Occupy a port, then ask listenFreePort to start at that same port.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not occupy a port: %v", err)
	}
	defer busy.Close()
	_, busyPortStr, _ := net.SplitHostPort(busy.Addr().String())
	busyPort, _ := strconv.Atoi(busyPortStr)

	ln, err := listenFreePort("127.0.0.1:" + busyPortStr)
	if err != nil {
		t.Fatalf("listenFreePort should fall back to a free port, got error: %v", err)
	}
	defer ln.Close()
	_, gotPortStr, _ := net.SplitHostPort(ln.Addr().String())
	gotPort, _ := strconv.Atoi(gotPortStr)
	if gotPort == busyPort {
		t.Errorf("listenFreePort returned the busy port %d (should have picked another)", busyPort)
	}
	if gotPort <= busyPort {
		t.Errorf("fallback port %d should be above the busy start %d", gotPort, busyPort)
	}
}

// TestListenFreePortBindsRequestedWhenFree: a free start port is used as-is.
func TestListenFreePortBindsRequestedWhenFree(t *testing.T) {
	// Find a definitely-free port, close it, then ask listenFreePort to bind it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(probe.Addr().String())
	probe.Close()

	ln, err := listenFreePort("127.0.0.1:" + portStr)
	if err != nil {
		t.Fatalf("listenFreePort on a free port errored: %v", err)
	}
	defer ln.Close()
	_, gotStr, _ := net.SplitHostPort(ln.Addr().String())
	if gotStr != portStr {
		t.Errorf("free start port %s not used (got %s)", portStr, gotStr)
	}
}

// TestChannelHelpListsAcceptedVerbs: the in-channel /help listing must match what
// runSession actually accepts, including the aliases that used to be omitted
// (/conf, /tune, /retune, /ep, /leave, /dc).
func TestChannelHelpListsAcceptedVerbs(t *testing.T) {
	m := browseSeed(100)
	mm, _ := m.runSession("/help")
	gm := asModel(mm)
	help := strings.ToLower(strings.Join(gm.transcript, "\n"))

	// The previously-missing aliases/commands must now appear in the listing.
	for _, want := range []string{"/conf", "/tune", "/retune", "/ep", "/leave", "/dc"} {
		if !strings.Contains(help, want) {
			t.Errorf("/help listing omits %q (runSession accepts it):\n%s", want, help)
		}
	}
	// And the core verbs stay listed.
	for _, want := range []string{"/model", "/clear", "/system", "/cost", "/confidential", "/endpoint", "/disconnect", "/quit"} {
		if !strings.Contains(help, want) {
			t.Errorf("/help listing omits core verb %q:\n%s", want, help)
		}
	}
}
