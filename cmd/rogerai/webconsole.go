package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/rogerai-fyi/roger/internal/node"
	"github.com/rogerai-fyi/roger/internal/tui"
	"github.com/rogerai-fyi/roger/internal/webui"
)

// defaultWebuiPort is where the browser node console binds first; if it's taken the
// server scans upward (webui.Listen), so a busy port never dead-ends.
const defaultWebuiPort = "4180"

// webuiEnabled reports whether the browser console should come up. It is ON by default;
// the saved config can opt out (config.Webui=false), and the --no-webui flag forces it
// off for a single run (--webui forces it on). The flags are consumed by stripWebuiFlags.
func (c config) webuiEnabled() bool { return c.Webui == nil || *c.Webui }

// webuiOpenEnabled reports whether the console also auto-opens in a browser at launch.
// OFF by default (founder respec 2026-07-14: on terminal-embedded browsers the auto-open
// trapped the TUI); `roger config set webui-open true` opts back in. Either way the URL
// is printed and `w` (BROWSE) / /webui (AGENT) open it on demand.
func (c config) webuiOpenEnabled() bool { return c.WebuiOpen != nil && *c.WebuiOpen }

// openBrowser is a seam over the guarded default-browser launcher, so a test can
// observe the auto-open decision without spawning a process.
var openBrowser = tui.OpenURL

// stripWebuiFlags removes the global --no-webui / --webui / --webui-port=N flags from a
// raw argv tail and reports the resulting enabled state + port. They are global (not tied
// to a subcommand), so the dispatcher filters them before reading os.Args[1]; a real
// command keeps all its own args.
func stripWebuiFlags(args []string, enabled bool, port string) (rest []string, outEnabled bool, outPort string) {
	outEnabled, outPort = enabled, port
	for _, a := range args {
		switch {
		case a == "--no-webui":
			outEnabled = false
		case a == "--webui":
			outEnabled = true
		case strings.HasPrefix(a, "--webui-port="):
			if p := strings.TrimPrefix(a, "--webui-port="); p != "" {
				outPort = p
			}
		default:
			rest = append(rest, a)
		}
	}
	return rest, outEnabled, outPort
}

// startWebConsole stands up the localhost browser console over ctrl (the SAME controller
// the TUI/daemon drives), printing the tokenized URL, and returns that URL ("" on a bind
// failure) so the TUI can open it on demand (`w` / /webui). It returns immediately; the
// server runs in a background goroutine for the life of the process. A bind failure is
// non-fatal — the terminal front-end carries on.
func startWebConsole(cfg config, ctrl *node.Controller, port string) string {
	s := webui.New(ctrl, webui.Options{Broker: cfg.Broker, User: cfg.User, ClientID: gitHubClientID()})
	ln, url, err := s.Listen("127.0.0.1:" + port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web console: could not bind a localhost port:", err)
		return ""
	}
	fmt.Printf("web console → %s\n", url)
	go func() { _ = s.Serve(ln) }()
	// Kick an initial detection in the background so the browser SHARE tab is populated on
	// first paint. The TUI only detects lazily (on entering SHARE), so without this a fresh
	// launch would show an empty table until the user clicked re-detect. Best-effort; the
	// snapshot/SSE picks up whatever it finds, and re-detect can refine it.
	go func() {
		found, _ := ctrl.Detect("", "")
		// No-persist: a passive launch scan populates the table for display but must NOT
		// rewrite saved share config — that's reserved for an explicit re-detect.
		ctrl.LoadRowsNoPersist(found)
	}()
	// Auto-open ONLY when the saved config opts in (webui_open: true) - the default is
	// to just print the URL (founder respec 2026-07-14: the auto-open trapped the TUI
	// under terminal-embedded browsers). The launcher still self-gates on a real
	// interactive terminal, so a headless `roger share` daemon never hijacks a browser.
	if cfg.webuiOpenEnabled() {
		openBrowser(url)
	}
	return url
}
