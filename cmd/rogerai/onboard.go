package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/rogerai-fyi/roger/internal/detect"
)

// The first-run onboarding wizard (charmbracelet/huh). It runs once, before the
// TUI, and answers the two questions that matter: are you here to CONSUME (just
// open the app) or SHARE your GPU, and if sharing, free (no login) or earn (set a
// price + `rogerai login`). Everything else is auto-detected: the model, the
// context length, and a free local port. Re-runnable via `rogerai onboard`. The
// FREE default means a provider goes on air with no login (fixes the 403).

// interactive reports whether stdin+stdout are a real TTY (so the wizard can
// prompt). Non-TTY / piped / NO_COLOR runs skip the wizard entirely.
func interactive() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd())
}

// maybeOnboard runs the first-run wizard when the user has never onboarded and we
// are on an interactive terminal. It returns the (possibly updated) config. On a
// non-interactive run, or any wizard error/abort, it returns the config unchanged
// so the app still launches.
func maybeOnboard(cfg config) config {
	if cfg.Onboarded || !interactive() {
		return cfg
	}
	updated, ran, err := runWizard(cfg, wizardOpts{})
	if err != nil || !ran {
		return cfg // never block launch on a wizard hiccup / abort
	}
	_ = saveConfig(updated) // remember the choice so we never re-prompt
	return updated
}

// wizardOpts carries non-interactive overrides (flags), so the wizard can be
// scripted: --free / --earn pick the share path, --yes accepts all defaults.
type wizardOpts struct {
	forceFree bool
	forceEarn bool
	yes       bool
	reset     bool
}

// cmdOnboard is the explicit `rogerai onboard` entry: re-run the wizard, offering
// Keep / Modify / Reset when a config already exists.
func cmdOnboard(cfg config, args []string) error {
	fs := flag.NewFlagSet("onboard", flag.ExitOnError)
	free := fs.Bool("free", false, "non-interactive: share FREE (no login)")
	earn := fs.Bool("earn", false, "non-interactive: share to earn (sets a price; needs `rogerai login`)")
	yes := fs.Bool("yes", false, "accept the detected defaults without prompting")
	reset := fs.Bool("reset", false, "forget the saved setup and start fresh")
	fs.Parse(args)
	opts := wizardOpts{forceFree: *free, forceEarn: *earn, yes: *yes, reset: *reset}
	updated, _, err := runWizard(cfg, opts)
	if err != nil {
		return err
	}
	return saveConfig(updated)
}

// runWizard drives the form. Returns (updatedConfig, ran, err). ran=false means
// the user chose to keep things as-is (no save needed by the caller).
func runWizard(cfg config, opts wizardOpts) (config, bool, error) {
	// Non-interactive fast paths: --free / --earn / --yes script the share choice.
	if opts.forceFree || opts.forceEarn {
		return finishShare(cfg, opts.forceEarn, opts)
	}
	if !interactive() {
		return cfg, false, nil
	}

	// Re-run on an existing setup: Keep / Modify / Reset.
	if (cfg.Onboarded || cfg.Share != nil) && !opts.reset {
		choice := "keep"
		if err := huh.NewSelect[string]().
			Title("RogerAI is already set up. What now?").
			Options(
				huh.NewOption("Keep it as is", "keep"),
				huh.NewOption("Modify my setup", "modify"),
				huh.NewOption("Reset and start over", "reset"),
			).Value(&choice).Run(); err != nil {
			return cfg, false, err
		}
		switch choice {
		case "keep":
			return cfg, false, nil
		case "reset":
			cfg.Share = nil
		}
	}

	// The one decision that matters: consume vs share.
	intent := "consume"
	if err := huh.NewSelect[string]().
		Title("Welcome to RogerAI - a two-way radio for GPUs.").
		Description("Are you here to use models, or to share your GPU?").
		Options(
			huh.NewOption("Just use models (open the app)", "consume"),
			huh.NewOption("Share my GPU - QuickStart, FREE, no login", "free"),
			huh.NewOption("Share my GPU - earn (set prices + log in)", "earn"),
		).Value(&intent).Run(); err != nil {
		return cfg, false, err
	}
	switch intent {
	case "free":
		return finishShare(cfg, false, opts)
	case "earn":
		return finishShare(cfg, true, opts)
	default:
		cfg.Onboarded = true
		return cfg, true, nil
	}
}

// finishShare detects the local model, auto-picks a free port, runs preflight, and
// (for the earn path) collects prices. It saves the share config and marks
// onboarded. It does NOT start serving - it sets the user up; `rogerai share`
// (or `/share` in the TUI) goes on air.
func finishShare(cfg config, earn bool, opts wizardOpts) (config, bool, error) {
	found := detect.Detect()
	if len(found) == 0 {
		fmt.Println("no local LLM detected (tried Ollama / LM Studio / llama.cpp / vLLM / LiteLLM).")
		fmt.Println("start one, then run `rogerai share` (or `rogerai onboard`).")
		cfg.Onboarded = true
		return cfg, true, nil
	}
	pick := found[0]
	model := ""
	if len(pick.Models) > 0 {
		model = pick.Models[0]
	}
	port := freePort(4140)

	sh := Share{Model: model, Port: port}
	if earn {
		// Earn path: collect a price (default the platform suggestion). Login is a
		// separate explicit step we point the user at - we never block here.
		in, out := "0.20", "0.30"
		if interactive() && !opts.yes {
			_ = huh.NewInput().Title("Price per 1M OUTPUT tokens ($)").Value(&out).Run()
			_ = huh.NewInput().Title("Price per 1M INPUT tokens ($)").Value(&in).Run()
		}
		sh.PriceIn = parsePrice(in)
		sh.PriceOut = parsePrice(out)
	}

	// Preflight: broker reachable + the upstream is actually serving the model.
	if reachable(cfg.Broker) {
		fmt.Printf("preflight: broker %s reachable\n", cfg.Broker)
	} else {
		fmt.Printf("preflight: WARNING broker %s not reachable right now (you can still go on air later)\n", cfg.Broker)
	}
	fmt.Printf("preflight: serving %q at %s\n", model, pick.BaseURL)

	cfg.Share = &sh
	cfg.Onboarded = true
	if earn {
		fmt.Printf("\nset up to EARN: model %q at $%.2f/$%.2f per 1M (in/out), port %d.\n", model, sh.PriceIn, sh.PriceOut, port)
		fmt.Println("earning needs a linked GitHub: run `rogerai login`, then `rogerai share`.")
	} else {
		fmt.Printf("\nset up to share FREE: model %q on port %d - no login needed.\n", model, port)
		fmt.Println("go on air now with `rogerai share` (or /share inside the app).")
		fmt.Println("want private free keys for your bots/family? `rogerai grant create --name my-bots`.")
	}
	return cfg, true, nil
}

// freePort returns the first free TCP port at/above start (auto-pick so a user
// never hits "address in use"); start itself if it binds, else scans upward.
func freePort(start int) int {
	for p := start; p < start+200; p++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return start
}

// reachable does a fast GET /health on the broker for preflight.
func reachable(broker string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(broker + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// parsePrice parses a price input, clamping to 0 on a bad value.
func parsePrice(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}
