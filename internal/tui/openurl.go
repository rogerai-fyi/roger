package tui

import (
	"os/exec"
	"runtime"
)

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

// openURL launches the default browser at url, fire-and-forget. Any error is
// swallowed: on a headless / SSH box there is no browser, and the device-flow
// panel always shows the URL + code as the fallback, so a failed open is never
// fatal to login. We Start (not Run) so the TUI never blocks on the launcher.
func openURL(url string) {
	name, args := openURLCommand(runtime.GOOS, url)
	cmd := exec.Command(name, args...)
	_ = cmd.Start()
	// Reap the child so a successful launcher (often a quick-exiting shim) does not
	// linger as a zombie; ignore the result - this is best-effort.
	if cmd.Process != nil {
		go func() { _ = cmd.Wait() }()
	}
}
