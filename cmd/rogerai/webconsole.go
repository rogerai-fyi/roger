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
// the TUI/daemon drives), printing the tokenized URL and opening the browser. It returns
// immediately; the server runs in a background goroutine for the life of the process. A
// bind failure is non-fatal — the terminal front-end carries on.
func startWebConsole(cfg config, ctrl *node.Controller, port string) {
	s := webui.New(ctrl, webui.Options{Broker: cfg.Broker, User: cfg.User, ClientID: gitHubClientID()})
	ln, url, err := s.Listen("127.0.0.1:" + port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web console: could not bind a localhost port:", err)
		return
	}
	fmt.Printf("web console → %s\n", url)
	go func() { _ = s.Serve(ln) }()
	// Kick an initial detection in the background so the browser SHARE tab is populated on
	// first paint. The TUI only detects lazily (on entering SHARE), so without this a fresh
	// launch would show an empty table until the user clicked re-detect. Best-effort; the
	// snapshot/SSE picks up whatever it finds, and re-detect can refine it.
	go func() {
		found, _ := ctrl.Detect("", "")
		ctrl.LoadRows(found)
	}()
	// Open the browser only on a real interactive terminal (the helper self-gates), so a
	// headless `roger share` daemon prints the URL but never hijacks a browser.
	tui.OpenURL(url)
}
