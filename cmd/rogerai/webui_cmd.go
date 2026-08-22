package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"rogerai.fm/roger/v5/internal/tui"
)

// `roger webui` - THE CONSOLE ON ITS OWN.
//
// FOUNDER 2026-08-21: "there is a roger webui or equivalent cli way to open it".
//
// THE GAP. The browser console only existed as a side effect of launching the TUI: it was
// started by `roger` (no args), printed its URL into the terminal the TUI then took over,
// and could be opened from inside with `w`. So the console was unreachable to anyone who
// wanted the browser and NOT a full-screen terminal app - and on a headless box, where
// the console is the more useful of the two front-ends, there was no way to it at all.
//
// This runs the console in the foreground over its own controller and blocks until ctrl-c,
// which is what a `<tool> web` command is expected to do (dsh web, jupyter, and every
// other local-server command behave this way).
//
// It opens the browser BY DEFAULT, unlike the TUI-launched console. The reasoning that
// made auto-open wrong there does not apply: the founder typed a command whose entire
// purpose is the browser, and there is no terminal UI for a browser to trap. --no-open
// covers the headless/remote case where opening a browser is impossible or unwanted.
func cmdWebui(cfg config, args []string) error {
	port, open := "", true
	for _, a := range args {
		switch {
		case a == "--no-open" || a == "--print":
			open = false
		case a == "-h" || a == "--help" || a == "help":
			webuiUsage()
			return nil
		case strings.HasPrefix(a, "--port="):
			port = strings.TrimPrefix(a, "--port=")
		default:
			return fmt.Errorf("unknown flag %q; run 'roger webui --help'", a)
		}
	}

	hooks := tuiHooks(cfg)
	ctrl := tui.NewController(cfg.Broker, hooks)
	limits := tuiLimits(cfg)
	// startWebConsole prints the URL, serves in the background and self-gates its own
	// auto-open on the saved config. Reuse it whole rather than standing up a second
	// launcher: a divergence here would mean the console you get from `roger webui`
	// differs from the one `roger` gives you, in ways nobody would think to test.
	url := startWebConsole(cfg, ctrl, port, limits)
	if url == "" {
		return fmt.Errorf("could not bind a localhost port for the console")
	}
	if open && !cfg.webuiOpenEnabled() {
		// The saved config did not already open it, and this command means to.
		openBrowser(url)
	}
	fmt.Println("the console is serving. ctrl-c to stop.")

	// Block until interrupted. Every model this node is sharing keeps running for as long
	// as the console does, so stopping must be an explicit act rather than the process
	// falling off the end of main.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nconsole stopped.")
	return nil
}

func webuiUsage() {
	fmt.Println(`roger webui - the browser console, on its own

  roger webui                  serve the console and open it in your browser
  roger webui --no-open        serve it and just print the URL (headless / remote)
  roger webui --port=8391      pick the port instead of letting the OS choose

The console does everything the terminal app does except take over your terminal:
chat with tools, share models, manage private bands, set spend limits, wallet and
payouts. It binds 127.0.0.1 only, behind a per-run token embedded in the URL.

Inside the terminal app, w opens the same console.`)
}
