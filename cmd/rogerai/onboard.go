package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/rogerai-fyi/roger/internal/detect"
)

// The first-run onboarding wizard (charmbracelet/huh). It runs once, before the
// TUI, and answers the two questions that matter: are you here to CONSUME (just
// open the app) or SHARE your GPU, and if sharing, free (no login) or earn (set a
// price + `roger login`). Everything else is auto-detected: the model, the
// context length, and a free local port. Re-runnable via `roger onboard`. The
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

// cmdOnboard is the explicit `roger onboard` entry: re-run the wizard, offering
// Keep / Modify / Reset when a config already exists.
func cmdOnboard(cfg config, args []string) error {
	fs := flag.NewFlagSet("onboard", flag.ExitOnError)
	free := fs.Bool("free", false, "non-interactive: share FREE (no login)")
	earn := fs.Bool("earn", false, "non-interactive: share to earn (sets a price; needs `roger login`)")
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
// onboarded. It does NOT start serving - it sets the user up; `roger share`
// (or `/share` in the TUI) goes on air.
func finishShare(cfg config, earn bool, opts wizardOpts) (config, bool, error) {
	found, needKey := detect.DetectFull()
	if len(found) == 0 {
		// GUIDED FALLBACK: walk the user through starting a tool, pasting an endpoint, or
		// (when a key-protected server is detected) entering its API key, instead of
		// dead-ending. Non-interactive / declined -> the plain hint.
		if picked, ok := guidedUpstream(cfg.Broker, needKey); ok {
			found = []detect.Found{picked}
		} else {
			fmt.Println("no local LLM detected (tried Ollama / LM Studio / llama.cpp / vLLM / Jan / LiteLLM and your open ports).")
			fmt.Println("start one, then run `roger share` (or `roger onboard`).")
			cfg.Onboarded = true
			return cfg, true, nil
		}
	}
	pick := found[0]
	model := ""
	if len(pick.Models) > 0 {
		model = pick.Models[0]
	}
	port, err := freePort(4140)
	if err != nil {
		return cfg, false, err
	}

	sh := Share{Model: model, Port: port, Upstream: pick.BaseURL, UpstreamKey: pick.Key}
	if earn {
		// Earn path: tell the user UP FRONT that earning needs a GitHub login and
		// pre-disclose the payout terms (F3 / #2) - BEFORE collecting a price - so the
		// login requirement is never a surprise 403 after they've set everything up.
		fmt.Println("earning needs a linked GitHub: you'll run `roger login` once before going on air.")
		fmt.Println("payouts when you earn: 120-day hold, $25 min, monthly (`roger payout status` for details).")
		// Collect a price (default the platform suggestion). Login is a separate
		// explicit step we point the user at - we never block here.
		in, out := "0.20", "0.30"
		if interactive() && !opts.yes {
			_ = huh.NewInput().Title("Price per 1M OUTPUT tokens ($)").Value(&out).Run()
			_ = huh.NewInput().Title("Price per 1M INPUT tokens ($)").Value(&in).Run()
		}
		sh.PriceIn = parsePrice(in)
		sh.PriceOut = parsePrice(out)
	}

	// Preflight: confirm the upstream is serving the model. A broker hiccup is NOT a
	// warning at setup time (#5) - the agent self-heals and you go on air later - so we
	// no longer print a scary "broker unreachable" line on a perfectly healthy first run.
	fmt.Printf("preflight: serving %q at %s\n", model, pick.BaseURL)

	cfg.Share = &sh
	cfg.Onboarded = true
	if earn {
		fmt.Printf("\nset up to EARN: model %q at $%.2f/$%.2f per 1M (in/out), port %d.\n", model, sh.PriceIn, sh.PriceOut, port)
		fmt.Println("earning needs a linked GitHub: run `roger login`, then `roger share`.")
	} else {
		fmt.Printf("\nset up to share FREE: model %q on port %d - no login needed.\n", model, port)
		fmt.Println("go on air now with `roger share` (or /share inside the app).")
		fmt.Println("want private free keys for your bots/family? `roger grant create --name my-bots`.")
	}
	return cfg, true, nil
}

// startOneLiner maps a local-LLM tool to a copy-paste command that starts it
// serving an OpenAI-compatible endpoint. These are the canonical per-tool
// quickstarts; the user runs one in another terminal, then we re-detect.
var startOneLiner = map[string]string{
	"ollama":    "ollama serve   # then:  ollama run llama3.2   (serves http://127.0.0.1:11434)",
	"lm-studio": "open LM Studio -> Developer tab -> Start Server   (serves http://127.0.0.1:1234)",
	"vllm":      "vllm serve <model> --port 8000   (serves http://127.0.0.1:8000)",
	"llamacpp":  "llama-server -m <model>.gguf --port 8080   (serves http://127.0.0.1:8080)",
}

// guidedUpstream is the interactive guided fallback when detection finds nothing:
// it asks what the user is running, prints that tool's start one-liner (so they
// can launch it and we re-detect), or takes a pasted endpoint and verifies it
// serves /v1/models. needKey carries base URLs of servers that ARE running but are
// key-protected (a 401/403 the env keys didn't satisfy): for those we ask for an API
// key first, since that is the most likely fix. Returns (verified server, true) on
// success. A non-interactive run returns ok=false so the caller prints the plain
// "start one / --upstream" hint instead of hanging.
// promptUpstreamKey asks for an API key for each detected-but-key-protected base URL
// (a 401/403 the env keys didn't satisfy) and returns the first that verifies with
// the pasted key. Shared by the initial needKey pass and the post-rescan path. A
// blank entry skips that endpoint; an input error or no match returns ok=false so the
// caller falls through to the tool menu.
func promptUpstreamKey(needKey []string) (detect.Found, bool) {
	for _, base := range needKey {
		key := ""
		if err := huh.NewInput().
			Title("Found a local server at " + base + " that needs an API key").
			Description("Paste its API key (e.g. your vLLM --api-key / LiteLLM master key), or leave blank to skip.").
			Value(&key).Run(); err != nil {
			return detect.Found{}, false
		}
		if strings.TrimSpace(key) == "" {
			continue
		}
		if f, st := detect.ProbeKey(base, strings.TrimSpace(key)); st == detect.Reachable {
			fmt.Printf("verified %s - serves %d model(s)\n", f.BaseURL, len(f.Models))
			return f, true
		}
		fmt.Printf("that key did not unlock %s - check it and try again, or pick a tool below.\n", base)
	}
	return detect.Found{}, false
}

func guidedUpstream(broker string, needKey []string) (detect.Found, bool) {
	if !interactive() {
		return detect.Found{}, false
	}
	// A detected-but-key-protected server is the clearest fix: ask for its key first.
	if f, ok := promptUpstreamKey(needKey); ok {
		return f, true
	}
	for {
		choice := "other"
		err := huh.NewSelect[string]().
			Title("No running model found. What are you using?").
			Description("Pick your tool for a one-liner to start it, or paste an endpoint and we'll verify it.").
			Options(
				huh.NewOption("Ollama", "ollama"),
				huh.NewOption("LM Studio", "lm-studio"),
				huh.NewOption("vLLM", "vllm"),
				huh.NewOption("llama.cpp", "llamacpp"),
				huh.NewOption("Other - paste a URL", "other"),
				huh.NewOption("Cancel", "cancel"),
			).Value(&choice).Run()
		if err != nil || choice == "cancel" {
			return detect.Found{}, false
		}
		if choice == "other" {
			url := ""
			if err := huh.NewInput().
				Title("Paste your local OpenAI-compatible endpoint").
				Description("e.g. http://127.0.0.1:8081  (we'll check it serves /v1/models)").
				Value(&url).Run(); err != nil {
				return detect.Found{}, false
			}
			f, st := detect.ProbeKey(url, "")
			if st == detect.NeedsKey {
				// The endpoint is there but key-protected: ask for the key and re-verify.
				key := ""
				if err := huh.NewInput().
					Title("That endpoint needs an API key").
					Description("Paste the API key it expects (sent as a Bearer to your local server).").
					Value(&key).Run(); err == nil && strings.TrimSpace(key) != "" {
					f, st = detect.ProbeKey(url, strings.TrimSpace(key))
				}
			}
			if st == detect.Reachable {
				fmt.Printf("verified %s - serves %d model(s)\n", f.BaseURL, len(f.Models))
				return f, true
			}
			fmt.Printf("could not reach an OpenAI-compatible server at %q (no /v1/models). Let's try again.\n", url)
			continue
		}
		// A named tool: show the one-liner, let the user start it, then re-detect.
		fmt.Printf("\nstart %s with:\n  %s\n\n", choice, startOneLiner[choice])
		again := true
		if err := huh.NewConfirm().
			Title("Started it? Re-scan for a running model now?").
			Affirmative("Re-scan").Negative("Cancel").
			Value(&again).Run(); err != nil || !again {
			return detect.Found{}, false
		}
		found, needKey := detect.DetectFull()
		if len(found) > 0 {
			fmt.Printf("found %s at %s\n", found[0].Name, found[0].BaseURL)
			return found[0], true
		}
		// The tool may have come up key-protected (e.g. vLLM --api-key): ask for the key
		// rather than reporting "still nothing".
		if f, ok := promptUpstreamKey(needKey); ok {
			return f, true
		}
		fmt.Println("still nothing on the default ports / your open ports - give it a moment, or paste the URL.")
	}
}

// freePort returns the first free TCP port at/above start (auto-pick so a user
// never hits "address in use"); start itself if it binds, else scans upward. It
// returns an error when the whole scan window is busy - it must NOT fall back to a
// known-busy port (the caller would then bind-fail with a confusing "address in
// use" the auto-pick was meant to avoid).
func freePort(start int) (int, error) {
	for p := start; p < start+200; p++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			ln.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free TCP port in %d-%d (close some listeners or pass --port)", start, start+199)
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
