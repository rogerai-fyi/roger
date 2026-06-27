package client

import (
	"net/http"
	"os"
	"testing"
)

// withStdin points the Use confirm-reader at a pipe carrying `input`, restored after.
func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(input)
	w.Close()
	old := useStdin
	useStdin = r
	t.Cleanup(func() { useStdin = old; r.Close() })
}

// captureServe points useServe at a capture func (so the relay never blocks), restored
// after. It records the address Use would have served on.
func captureServe(t *testing.T, gotAddr *string) {
	t.Helper()
	old := useServe
	useServe = func(addr string, _ http.Handler) error {
		*gotAddr = addr
		return nil
	}
	t.Cleanup(func() { useServe = old })
}

// TestUseNoStation covers the early return when no station is on air for the model.
func TestUseNoStation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := fakeBroker(t) // only serves "m1"
	if err := Use(b, "u_gh_1", "no-such-model", UseOptions{}); err != nil {
		t.Errorf("Use(no station) = %v, want nil (clean message)", err)
	}
}

// TestUseOverLimitYes covers the headless over-limit guard: --yes with a max-out below
// the cheapest station is a hard error (never silently overpays).
func TestUseOverLimitYes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := fakeBroker(t) // m1 is 2.0 $/1M out
	if err := Use(b, "u_gh_1", "m1", UseOptions{MaxOut: 0.5, Yes: true}); err == nil {
		t.Error("Use(--yes, over limit) should error, not overpay")
	}
}

// TestUseOverLimitAbort covers the interactive over-limit path: a blank line aborts.
func TestUseOverLimitAbort(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withStdin(t, "\n") // blank -> abort
	b := fakeBroker(t)
	if err := Use(b, "u_gh_1", "m1", UseOptions{MaxOut: 0.5}); err != nil {
		t.Errorf("Use(over limit, abort) = %v, want nil", err)
	}
}

// TestUseConfirmDenied covers the simple y/N confirm being declined.
func TestUseConfirmDenied(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withStdin(t, "n\n")
	b := fakeBroker(t)
	if err := Use(b, "u_gh_1", "m1", UseOptions{}); err != nil { // default cap 10 > 2.0, within limit
		t.Errorf("Use(denied) = %v, want nil", err)
	}
}

// TestUseConfirmYesOpensChannel covers the happy path: confirm yes -> the local proxy is
// served on the requested port (captured, not actually bound).
func TestUseConfirmYesOpensChannel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withStdin(t, "y\n")
	var addr string
	captureServe(t, &addr)
	b := fakeBroker(t)
	if err := Use(b, "u_gh_1", "m1", UseOptions{Port: 4321}); err != nil {
		t.Fatalf("Use(confirm yes) = %v, want nil", err)
	}
	if addr != "127.0.0.1:4321" {
		t.Errorf("relay addr = %q, want 127.0.0.1:4321", addr)
	}
}

// TestUseYesNonInteractiveOpens covers the --yes within-limit path (no prompt) reaching
// the channel-open + serve.
func TestUseYesNonInteractiveOpens(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var addr string
	captureServe(t, &addr)
	b := fakeBroker(t)
	if err := Use(b, "u_gh_1", "m1", UseOptions{Yes: true, MaxOut: 5, Port: 7000}); err != nil {
		t.Fatalf("Use(--yes within limit) = %v, want nil", err)
	}
	if addr != "127.0.0.1:7000" {
		t.Errorf("relay addr = %q, want 127.0.0.1:7000", addr)
	}
}
