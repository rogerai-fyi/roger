package tui

import (
	"os"
	"os/exec"
	"runtime"
)

// stdinIsTTY / stdoutIsTTY report whether each standard stream is an interactive
// terminal (a character device). They are vars, not plain funcs, so a test can
// inject a fake non-TTY ("headless") result without a real pipe and without a new
// dependency: we Stat the file and check the os.ModeCharDevice bit, which is
// exactly what golang.org/x/term.IsTerminal does for the common case, minus the
// extra module.
var (
	stdinIsTTY  = func() bool { return isCharDevice(os.Stdin) }
	stdoutIsTTY = func() bool { return isCharDevice(os.Stdout) }
)

// isCharDevice is true when f is a TTY (a character device). A pipe, a regular
// file (redirected stdout), or a closed/nil stream is NOT - which is precisely
// the headless / piped / service (`roger share` daemon) case where we must
// never spawn a browser.
func isCharDevice(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// interactive reports whether we are attached to a real interactive terminal on
// BOTH stdin and stdout. Auto-opening the default browser is gated on this: in a
// non-TTY / headless / piped / background-service context (e.g. `roger share`
// running as a daemon, or any process with no controlling terminal) we never
// hijack a browser - the caller still prints the URL + code as the fallback.
func interactive() bool { return stdinIsTTY() && stdoutIsTTY() }

// openURLCommand returns the OS default-browser launcher for a URL: the command
// name + args that hand the URL to the platform's URL handler. It is split out
// from the exec so the selection is unit-testable per GOOS without spawning a
// process (the tests assert the command, they never run it).
//
//	linux/bsd -> xdg-open <url>
//	darwin    -> open <url>
//	windows   -> rundll32 url.dll,FileProtocolHandler <url>
//
// The Windows form avoids `cmd /c start <url>`, whose `start` mis-parses a URL
// that contains `&` (it splits on it); rundll32's FileProtocolHandler takes the
// whole URL as one argument and is the standard headless-safe launcher.
func openURLCommand(goos, url string) (name string, args []string) {
	switch goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		return "open", []string{url}
	default:
		// linux, freebsd, openbsd, netbsd, ... all ship xdg-open (xdg-utils).
		return "xdg-open", []string{url}
	}
}

// openURLExec is the actual browser-launcher, split out as a var so tests can
// observe whether (and how often) an open was attempted without spawning a real
// process. It is the ONLY place exec happens; the TTY guard sits in openURL above
// it, so a test that swaps this still sees the guard's decision.
var openURLExec = func(url string) {
	name, args := openURLCommand(runtime.GOOS, url)
	cmd := exec.Command(name, args...)
	_ = cmd.Start()
	// Reap the child so a successful launcher (often a quick-exiting shim) does not
	// linger as a zombie; ignore the result - this is best-effort.
	if cmd.Process != nil {
		go func() { _ = cmd.Wait() }()
	}
}

// OpenURL is the exported wrapper so plain CLI commands (cmd/rogerai) can reuse the
// single default-browser launcher the TUI uses (e.g. `roger payout onboard` opening
// the Stripe Connect link). Fire-and-forget; the caller always prints the URL as a
// fallback for headless / SSH boxes and for the non-interactive case below.
func OpenURL(url string) { openURL(url) }

// openURL launches the default browser at url, fire-and-forget - but ONLY when we
// are attached to a real interactive terminal. The founder bug: a TUI/CLI running
// in a non-interactive / headless / piped / background-service context auto-opened
// (and re-opened) the GitHub device page on a machine with nobody in front of it.
// The interactive() gate makes that impossible; every caller still prints the URL
// + code, so login/onboarding is never blocked when we decline to open. Any exec
// error is otherwise swallowed (no browser on an SSH box is not fatal), and we
// Start (not Run) so the TUI never blocks on the launcher.
func openURL(url string) {
	if !interactive() {
		return
	}
	openURLExec(url)
}
