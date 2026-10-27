package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"rogerai.fm/roger/v7/internal/buildinfo"
	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/netbind"
	"rogerai.fm/roger/v7/internal/tui"
)

// The share setup (charmbracelet/huh), run by `roger onboard`: free (no login) or earn (set a
// price + `roger login`). Everything else is auto-detected: the model, the context length,
// and a free local port. The FREE default means a provider goes on air with no login.
//
// A first launch no longer runs anything before the TUI: the TUI opens with a one-time
// welcome card instead (features/onboarding/welcome_card.feature), decided here by
// welcomeOnLaunch and remembered by saveWelcomeSeen.

// interactive reports whether stdin+stdout are a real TTY (so the wizard can
// prompt). Non-TTY / piped / NO_COLOR runs skip the wizard entirely.
func interactive() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd())
}

// welcomeOnLaunch decides the one-time welcome card for a TUI launch. It is due on an
// interactive terminal for a config that has never seen it and never set anything up, or
// once more after `roger onboard --welcome` / `--reset`. A user who already onboarded or
// shares has welcome_seen settled to true here (saved now), so they never see it.
func welcomeOnLaunch(cfg config, tty bool) (config, bool) {
	if !cfg.WelcomeSeen && !cfg.WelcomeAgain && (cfg.Onboarded || cfg.Share != nil) {
		cfg.WelcomeSeen = true
		_ = saveWelcomeSeen()
	}
	return cfg, tty && (cfg.WelcomeAgain || !cfg.WelcomeSeen)
}

// saveWelcomeSeen remembers the card was seen. It writes welcome_seen (and clears the
// show-again flag) and nothing else, re-reading the file under the config lock first so no
// other writer's section is touched.
func saveWelcomeSeen() error {
	release, err := acquireConfigLock()
	if err != nil {
		return err
	}
	defer release()
	c := loadConfig()
	c.WelcomeSeen, c.WelcomeAgain = true, false
	return saveConfigLocked(c)
}

// tuiLaunch is the no-args launch up to the TUI, in order: the version boot (once per
// version), then the welcome card's rule, then the TUI's hooks. Split out of run() so the
// order is tested; boot is tui.PlayBoot in production.
func tuiLaunch(cfg config, tty bool, boot func()) (config, tui.Hooks) {
	if bootShouldPlay(cfg.LastSeenVersion, buildinfo.Version) {
		boot()
		cfg.LastSeenVersion = buildinfo.Version
		_ = saveConfig(cfg)
	}
	cfg, due := welcomeOnLaunch(cfg, tty)
	hooks := tuiHooks(cfg)
	hooks.WelcomeDue = due
	return cfg, hooks
}

// saveOnboardConfig persists a wizard outcome WITHOUT clobbering sections other
// writers own. The wizard's in-memory cfg is a startup snapshot that can be minutes
// stale by the time it saves (the operator sat in the interactive form; detection
// scanned), so writing the whole snapshot deleted anything another process had added
// to config.json in between (the 2026-07-02 07:58 share_voices loss - share_prices
// and station were exposed the same way). Instead, follow the merge convention every
// other writer already uses (saveStation, SavePrice, SaveUpstream, SaveCompact,
// cmdShare's upstream save: re-read, mutate only the owned section, save): re-read
// the file and apply only the sections the wizard owns - Onboarded and Share.
func saveOnboardConfig(updated config) error {
	c := loadConfig()
	c.Onboarded = updated.Onboarded
	c.Share = updated.Share
	c.WelcomeSeen, c.WelcomeAgain = updated.WelcomeSeen, updated.WelcomeAgain
	return saveConfig(c)
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
	reset := fs.Bool("reset", false, "forget the saved setup and start fresh (the welcome card shows again)")
	welcome := fs.Bool("welcome", false, "show the one-time welcome card on the next launch, and change nothing else")
	fs.Parse(args)
	if *welcome {
		release, err := acquireConfigLock()
		if err != nil {
			return err
		}
		defer release()
		c := loadConfig()
		c.WelcomeAgain = true
		if err := saveConfigLocked(c); err != nil {
			return err
		}
		fmt.Println("the welcome card will show the next time roger opens.")
		return nil
	}
	opts := wizardOpts{forceFree: *free, forceEarn: *earn, yes: *yes, reset: *reset}
	if opts.reset {
		// Starting over includes the first-launch welcome.
		cfg.WelcomeSeen, cfg.WelcomeAgain = false, true
	}
	updated, _, err := runWizard(cfg, opts)
	if err != nil {
		return err
	}
	return saveOnboardConfig(updated)
}

// runWizard drives the form. Returns (updatedConfig, ran, err). ran=false means
// the user chose to keep things as-is (no save needed by the caller).
func runWizard(cfg config, opts wizardOpts) (config, bool, error) {
	// Non-interactive fast paths: --free / --earn / --yes script the share choice.
	if opts.forceFree || opts.forceEarn {
		return finishShare(cfg, opts.forceEarn, opts)
	}
	if !interactive() {
		return cfg, opts.reset, nil // a reset still saves (the welcome card comes back)
	}

	// Re-run on an existing setup: Keep / Modify / Reset.
	if (cfg.Onboarded || cfg.Share != nil) && !opts.reset {
		choice := "keep"
		if err := huh.NewSelect[string]().
			Title("RogerAI is already set up. What now?").
			Options(rerunChoices...).Value(&choice).Run(); err != nil {
			return cfg, false, err
		}
		var done bool
		if cfg, done = applyRerunChoice(cfg, choice); done {
			return cfg, false, nil
		}
	}

	// `roger onboard` sets up sharing: free or earn. (Using models needs no setup at all:
	// the TUI's welcome card points there.)
	mode := "free"
	if err := huh.NewSelect[string]().
		Title("Share a model from this machine").
		Options(
			huh.NewOption("Share FREE - QuickStart, no login", "free"),
			huh.NewOption("Share to earn - set prices + log in", "earn"),
		).Value(&mode).Run(); err != nil {
		return cfg, false, err
	}
	return finishShare(cfg, mode == "earn", opts)
}

// rerunChoices is the re-run menu on a set-up machine (Keep / Modify / Reset).
var rerunChoices = []huh.Option[string]{
	huh.NewOption("Keep it as is", "keep"),
	huh.NewOption("Modify my setup", "modify"),
	huh.NewOption("Reset and start over", "reset"),
}

// applyRerunChoice maps the re-run menu choice to its config effect, touching ONLY the
// local share config - never the linked GitHub identity, the saved prices, or the station
// (and earnings live broker-side, not in cfg, so re-running setup can never move money).
// "keep" ends the wizard with the config untouched (done=true); "reset" forgets the local
// share so the next step reconfigures from scratch; "modify" (or anything else) keeps the
// existing share and proceeds. Pure: the huh select that produces `choice` is the only
// interactive part and stays in runWizard.
func applyRerunChoice(cfg config, choice string) (config, bool) {
	switch choice {
	case "keep":
		return cfg, true // keep as-is: nothing to save
	case "reset":
		cfg.Share = nil // forget only the local share; reconfigure next
	}
	return cfg, false // modify / unknown: proceed, keeping the share
}

// finishShare detects the local model, auto-picks a free port, runs preflight, and
// (for the earn path) collects prices. It saves the share config and marks
// onboarded. It does NOT start serving - it sets the user up; `roger share`
// (or `/share` in the TUI) goes on air.
func finishShare(cfg config, earn bool, opts wizardOpts) (config, bool, error) {
	found, needKey := detectFull()
	if len(found) == 0 {
		// GUIDED FALLBACK: walk the user through starting a tool, pasting an endpoint, or
		// (when a key-protected server is detected) entering its API key, instead of
		// dead-ending. Non-interactive / declined -> the plain hint.
		if picked, ok := guidedUpstream(cfg.Broker, needKey); ok {
			found = []detect.Found{picked}
		} else {
			fmt.Println("no local LLM detected (tried common hosts including Ollama / LM Studio / Unsloth / llama.cpp / vLLM / Jan / LiteLLM and your open ports).")
			fmt.Println("any other OpenAI-compatible host works too: pass its URL with --upstream.")
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
	// A port for the share to use later: a suggestion (netbind.SuggestPort), never a bind
	// this process holds - the share binds its own listener when it starts.
	port, err := netbind.SuggestPort(4140)
	if err != nil {
		return cfg, false, err
	}

	sh := Share{Model: model, Port: port, Upstream: pick.BaseURL, UpstreamKey: pick.Key}
	if earn {
		// Earn path: tell the user UP FRONT that earning needs a GitHub login and
		// pre-disclose the payout terms (F3 / #2) - BEFORE collecting a price - so the
		// login requirement is never a surprise 403 after they've set everything up.
		fmt.Println(tui.EarnLoginLine)
		fmt.Println(tui.EarnPayoutLine)
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
	"unsloth":   "open Unsloth Studio -> load a model -> Settings -> API -> copy the endpoint + key   (defaults to http://127.0.0.1:8888)",
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
			EchoMode(huh.EchoModePassword). // bearer credential: mask it (no terminal echo / scrollback leak)
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
				huh.NewOption("Unsloth Studio", "unsloth"),
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
					EchoMode(huh.EchoModePassword). // bearer credential: mask it (no terminal echo / scrollback leak)
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
		found, needKey := detectFull()
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

// parsePrice parses a price input, clamping to 0 on a bad value.
func parsePrice(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}
