// rogerai - the single client binary: consume models (search/use/balance) and
// share your own (share). One binary, all OS. The broker (rogerai-broker) is the
// only separately-deployed component.
//
//	rogerai search                    discover models (cheapest first)
//	rogerai use <model> [--port N]    open a local OpenAI endpoint via the broker
//	rogerai balance                   wallet credits
//	rogerai share [flags]             become a provider (auto-detects a local LLM)
//	rogerai config set broker <url>   switch brokers (federation: pick who you trust)
//	rogerai config get [key]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/tui"
	"github.com/rogerai-fyi/roger/internal/update"
)

// Version is the client version (compared against the latest GitHub release for
// the update check / `rogerai upgrade`). Keep in sync with releases.
const Version = "0.2.1"

// The production broker is the default - `rogerai` works out of the box, no config.
// Override per-session with ROGER_BROKER=... or persist with `rogerai config set broker`.
const defaultBroker = "https://broker.rogerai.fyi"

// defaultGitHubClientID is the PUBLIC OAuth client id of the org-owned "RogerAI"
// GitHub app (Device Flow enabled). Public by design; overridable for forks via
// GITHUB_OAUTH_CLIENT_ID. No client secret ever lives in the CLI.
const defaultGitHubClientID = "Ov23liQE7Z6ITMbeJoF3"

func gitHubClientID() string {
	if v := os.Getenv("GITHUB_OAUTH_CLIENT_ID"); v != "" {
		return v
	}
	return defaultGitHubClientID
}

// Limit is the per-model spend ceiling a user sets once and enforces: max input
// price, max output price (the headline cap, since we bill on output), and a
// throughput floor. All in the same units as /discover (credits per 1M tokens,
// tok/s). A zero field means "no cap on that knob".
type Limit struct {
	MaxIn  float64 `json:"max_in,omitempty"`
	MaxOut float64 `json:"max_out,omitempty"`
	MinTPS float64 `json:"min_tps,omitempty"`
}

// Limits is the optional, backward-compatible spend-limits section of the config:
// a per-model map plus a Default that applies to any band not pinned, and a knob
// for the typical reply size used in the connect-time est-cost line. Absent =
// no caps (same as before this section existed); old configs still load.
type Limits struct {
	Default       Limit            `json:"default"`
	Models        map[string]Limit `json:"models,omitempty"`
	TypicalOutTok int              `json:"typical_out_tokens,omitempty"`
}

type config struct {
	Broker    string `json:"broker"`
	User      string `json:"user"`
	Limits    Limits `json:"limits"`
	Onboarded bool   `json:"onboarded,omitempty"` // first-run wizard completed
	Share     *Share `json:"share,omitempty"`     // saved provider config (the wizard's earn/free choice)
}

// Share is the provider config the onboarding wizard saves: the model to expose,
// the chosen port, and the price (0/0 = free). Absent = not a provider yet.
type Share struct {
	Model    string  `json:"model"`
	Port     int     `json:"port"`
	PriceIn  float64 `json:"price_in,omitempty"`
	PriceOut float64 `json:"price_out,omitempty"`
}

// resolve returns the effective limit for model m: the per-model limit if set,
// else the Default. typicalOut is the configured reply size, or 800.
func (c config) resolve(m string) (Limit, int) {
	typ := c.Limits.TypicalOutTok
	if typ <= 0 {
		typ = 800
	}
	if l, ok := c.Limits.Models[m]; ok {
		return l, typ
	}
	return c.Limits.Default, typ
}

func configPath() string {
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "rogerai", "config.json")
}

func defaultUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "anon"
}

func loadConfig() config {
	c := config{Broker: defaultBroker, User: defaultUser()}
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if v := os.Getenv("ROGER_BROKER"); v != "" {
		c.Broker = v
	}
	if v := os.Getenv("ROGER_USER"); v != "" {
		c.User = v
	}
	return c
}

func saveConfig(c config) error {
	_ = os.MkdirAll(filepath.Dir(configPath()), 0700)
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), b, 0600)
}

// tuiLimits builds the TUI spend-limit store from the config, with a Save
// callback that persists edits back to config.json (the TUI owns no I/O).
func tuiLimits(cfg config) *tui.LimitStore {
	models := map[string]tui.Limit{}
	for m, l := range cfg.Limits.Models {
		models[m] = tui.Limit{MaxIn: l.MaxIn, MaxOut: l.MaxOut, MinTPS: l.MinTPS}
	}
	typ := cfg.Limits.TypicalOutTok
	if typ <= 0 {
		typ = 800
	}
	return &tui.LimitStore{
		Models:     models,
		Default:    tui.Limit{MaxIn: cfg.Limits.Default.MaxIn, MaxOut: cfg.Limits.Default.MaxOut, MinTPS: cfg.Limits.Default.MinTPS},
		TypicalOut: typ,
		Save: func(tm map[string]tui.Limit, def tui.Limit) {
			c := loadConfig()
			c.Limits.Models = map[string]Limit{}
			for m, l := range tm {
				c.Limits.Models[m] = Limit{MaxIn: l.MaxIn, MaxOut: l.MaxOut, MinTPS: l.MinTPS}
			}
			c.Limits.Default = Limit{MaxIn: def.MaxIn, MaxOut: def.MaxOut, MinTPS: def.MinTPS}
			_ = saveConfig(c)
		},
	}
}

// tuiHooks supplies the host bits the TUI can't compute (hostname, HW, the public
// GitHub client id, the saved share config) plus the login/topup/grant closures,
// so the in-TUI /share, /login, /topup, /grant flows are real actions.
func tuiHooks(cfg config) tui.Hooks {
	h := tui.Hooks{
		NodeID:      hostname(),
		HW:          detectHW(),
		GitHubID:    gitHubClientID(),
		LinkedLogin: client.LinkedLogin(), // "" when not logged in -> header shows the /login prompt
		Login:       client.LoginReturn,
		TopupURL:    client.TopupURL,
		GrantCreate: func(broker, name string, free bool) (string, error) {
			return client.GrantCreateSecret(broker, name, free)
		},
		GrantList: func(broker string) ([]tui.GrantRow, error) {
			rows, err := client.GrantListRows(broker)
			if err != nil {
				return nil, err
			}
			out := make([]tui.GrantRow, 0, len(rows))
			for _, r := range rows {
				out = append(out, tui.GrantRow{Name: r.Name, Price: r.Price, Status: r.Status})
			}
			return out, nil
		},
	}
	if cfg.Share != nil {
		h.ShareModel, h.SharePriceI, h.SharePriceO = cfg.Share.Model, cfg.Share.PriceIn, cfg.Share.PriceOut
	}
	return h
}

func main() {
	cfg := loadConfig()
	tui.SetVersion(Version) // help/about surfaces match `rogerai version`
	// A subtle, cached (~daily), non-blocking update banner. Computed once here so
	// the TUI does no network at startup; the cache refreshes in the background.
	notice := update.CachedNotice(Version)
	if len(os.Args) < 2 {
		// First run: a tiny guided wizard (consume vs share, free vs earn) before the
		// app. Non-interactive / already-onboarded runs skip it and launch straight in.
		cfg = maybeOnboard(cfg)
		// no args -> launch the interactive radio TUI with the in-TUI flow hooks
		if err := tui.RunWithHooks(cfg.Broker, cfg.User, tuiLimits(cfg), notice, tuiHooks(cfg)); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	// On plain CLI subcommands (not the TUI / the upgrade command itself), print the
	// banner to stderr so scripted stdout stays clean.
	if notice != "" {
		switch os.Args[1] {
		case "upgrade", "update", "self-update", "ping", "version":
		default:
			fmt.Fprintln(os.Stderr, notice)
		}
	}
	var err error
	switch os.Args[1] {
	case "search", "discover", "models":
		err = client.Search(cfg.Broker)
	case "balance":
		err = cmdBalance(cfg, os.Args[2:])
	case "account", "identity":
		err = cmdAccount(cfg, os.Args[2:])
	case "login":
		err = client.Login(cfg.Broker, gitHubClientID())
	case "logout":
		err = client.Logout()
	case "whoami":
		err = client.Whoami()
	case "topup":
		err = cmdTopup(cfg, os.Args[2:])
	case "use":
		err = cmdUse(cfg, os.Args[2:])
	case "share":
		err = cmdShare(cfg, os.Args[2:])
	case "grant":
		err = cmdGrant(cfg, os.Args[2:])
	case "onboard", "setup":
		err = cmdOnboard(cfg, os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "ping":
		// easter egg: walk the mascot across the terminal once, then exit.
		err = tui.PingWalk()
	case "upgrade", "update", "self-update":
		err = cmdUpgrade(os.Args[2:])
	case "version":
		fmt.Printf("rogerai %s\n", Version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdUse(cfg config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: rogerai use <model> [--max-out $] [--advanced]")
	}
	// The model is the first positional; flags follow it. (Go's flag package stops
	// at the first non-flag arg, so we pull the model out before parsing.)
	model := args[0]
	fs := flag.NewFlagSet("use", flag.ExitOnError)
	// The headline cap, in everyone's face.
	maxOut := fs.Float64("max-out", -1, "cap: skip stations above this $/1M OUTPUT price (the headline cap); 0 = no cap")
	// Advanced - defaulted and tucked away (CLI-SIMPLICITY-AUDIT C7). --port 0 =
	// auto-pick a free port; --max-in is the rare input-heavy cap (C1 drops the
	// --max-price alias entirely).
	advanced := fs.Bool("advanced", false, "show advanced flags (--port --max-in --min-tps --confidential --yes)")
	port := fs.Int("port", 0, "local endpoint port (0 = auto-pick a free one)")
	confidential := fs.Bool("confidential", false, "route only to confidential (TEE-attested) nodes")
	maxIn := fs.Float64("max-in", -1, "cap: skip stations above this $/1M INPUT price; 0 = no cap")
	minTPS := fs.Float64("min-tps", -1, "require at least this measured throughput (tok/s); 0 = no floor")
	yes := fs.Bool("yes", false, "skip the connect-time confirm (for scripts / Hermes / bots)")
	fs.Parse(args[1:])
	if *advanced {
		fmt.Println("advanced flags: --port --max-in --min-tps --confidential --yes")
	}
	// Start from the resolved per-model limit (or Default), then let flags override
	// it for this session. -1 sentinel = flag not passed (keep the stored limit).
	lim, typical := cfg.resolve(model)
	if *maxIn >= 0 {
		lim.MaxIn = *maxIn
	}
	if *maxOut >= 0 {
		lim.MaxOut = *maxOut
	}
	if *minTPS >= 0 {
		lim.MinTPS = *minTPS
	}
	useport := *port
	if useport == 0 {
		useport = freePort(4141) // auto-pick + the endpoint line prints the chosen port
	}
	return client.Use(cfg.Broker, cfg.User, model, client.UseOptions{
		Port: useport, Confidential: *confidential,
		MaxIn: lim.MaxIn, MaxOut: lim.MaxOut, MinTPS: lim.MinTPS,
		TypicalOut: typical, Yes: *yes,
	})
}

func cmdShare(cfg config, args []string) error {
	// Defaults inherit the saved onboarding share config (model + price) when set,
	// so `rogerai share` after the wizard Just Works with the choices already made.
	defModel, defIn, defOut := "", 0.0, 0.0
	if cfg.Share != nil {
		defModel, defIn, defOut = cfg.Share.Model, cfg.Share.PriceIn, cfg.Share.PriceOut
	}
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	broker := fs.String("broker", cfg.Broker, "broker URL")
	node := fs.String("node", hostname(), "node id")
	model := fs.String("model", defModel, "model to expose (default: first detected)")
	upstream := fs.String("upstream", "", "local OpenAI endpoint (default: auto-detect)")
	upKey := fs.String("upstream-key", "", "bearer key for the upstream (optional)")
	region := fs.String("region", "home", "region")
	parallel := fs.Int("parallel", 4, "concurrent poll workers (per-node concurrency)")
	// FREE BY DEFAULT (price 0/0): a bare `rogerai share` goes on air with NO login
	// (a priced node would require `rogerai login` and otherwise 403). Set a price to
	// EARN (that does require login). See the onboarding wizard's earn branch.
	priceIn := fs.Float64("price-in", defIn, "$/1M input tokens to EARN (default 0 = free, no login needed)")
	priceOut := fs.Float64("price-out", defOut, "$/1M output tokens to EARN (default 0 = free, no login needed)")
	ctx := fs.Int("ctx", 0, "context length (default: auto-detect from the upstream)")
	confidential := fs.Bool("confidential", false, "advertise as confidential (TEE-attested)")
	attestation := fs.String("attestation", "", "TEE attestation blob (dev placeholder if --confidential without it)")
	freeWindow := fs.String("free-window", "", "daily FREE window in UTC, e.g. 03:00-03:30")
	schedule := fs.String("schedule", "", `time-of-use schedule, JSON e.g. '[{"start":"18:00","end":"22:00","price_in":0.5,"price_out":0.7}]'`)
	advanced := fs.Bool("advanced", false, "show advanced flags (--node --region --parallel --upstream --ctx --confidential --free-window --schedule)")
	fs.Parse(args)
	if *advanced {
		fmt.Println("advanced flags: --node --region --parallel --upstream --upstream-key --ctx --confidential --attestation --free-window --schedule")
	}

	up := *upstream
	mdl := *model
	ctxLen := *ctx
	if up == "" {
		found := detect.Detect()
		if len(found) == 0 {
			return fmt.Errorf("no local LLM detected (tried Ollama/LM Studio/llama.cpp/vLLM/LiteLLM). Start one or pass --upstream")
		}
		// prefer one that serves the requested model; else the first
		pick := found[0]
		if mdl != "" {
			for _, f := range found {
				for _, m := range f.Models {
					if m == mdl {
						pick = f
					}
				}
			}
		}
		up = pick.Chat
		if mdl == "" && len(pick.Models) > 0 {
			mdl = pick.Models[0]
		}
		// Auto-detect --ctx from the upstream's /v1/models when the user didn't pin it.
		if ctxLen == 0 {
			if c, ok := pick.Ctx[mdl]; ok && c > 0 {
				ctxLen = c
			}
		}
		fmt.Printf("detected %s at %s - exposing model %q\n", pick.Name, pick.BaseURL, mdl)
	}
	if mdl == "" {
		return fmt.Errorf("could not determine a model; pass --model")
	}
	// ctx fallback: auto-detect (above) or the safe default when the upstream did
	// not report a context length and the user didn't pass --ctx.
	if ctxLen <= 0 {
		ctxLen = 32768
	}
	// Accept --upstream as a base URL (http://host:port), a /v1 URL, or the full
	// /v1/chat/completions URL - normalize to the chat-completions endpoint the
	// agent POSTs to. Auto-detected upstreams already carry the full path (this
	// is idempotent for them).
	up = normalizeUpstream(up)

	var sched []protocol.PriceWindow
	if *freeWindow != "" {
		p := strings.SplitN(*freeWindow, "-", 2)
		if len(p) != 2 {
			return fmt.Errorf("bad --free-window %q, want HH:MM-HH:MM", *freeWindow)
		}
		sched = append(sched, protocol.PriceWindow{Start: strings.TrimSpace(p[0]), End: strings.TrimSpace(p[1]), Free: true})
	}
	if *schedule != "" {
		var ws []protocol.PriceWindow
		if err := json.Unmarshal([]byte(*schedule), &ws); err != nil {
			return fmt.Errorf("bad --schedule json: %w", err)
		}
		sched = append(sched, ws...)
	}
	att := *attestation
	if *confidential && att == "" {
		att = "dev-placeholder-attestation"
		fmt.Println("warning: --confidential without --attestation won't earn the confidential badge - the broker rejects placeholder attestations (real TEE attestation required).")
	}

	if *priceIn == 0 && *priceOut == 0 && len(sched) == 0 {
		fmt.Println("sharing FREE (price 0/0) - on air with no login. set --price-out to earn (needs `rogerai login`).")
	}
	return agent.Run(agent.Config{
		Broker: *broker, Upstream: up, UpstreamKey: *upKey,
		NodeID: *node, Region: *region, HW: detectHW(), Model: mdl,
		PriceIn: *priceIn, PriceOut: *priceOut, Ctx: ctxLen, Parallel: *parallel,
		Confidential: *confidential, Attestation: att, Schedule: sched,
	})
}

// cmdUpgrade self-updates the binary to the latest GitHub release (alias of the
// `update` command). --help describes it; --check only reports availability.
func cmdUpgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	check := fs.Bool("check", false, "only check whether an update is available; do not install")
	fs.Usage = func() {
		fmt.Printf(`rogerai upgrade - self-update to the latest release (alias: update)

  rogerai upgrade           download + verify + atomically replace this binary
  rogerai upgrade --check   only report whether a newer version is available

Downloads the per-os/arch asset from github.com/%s, verifies its SHA256 against
the published checksums, then atomically swaps the running binary. "Already on
the latest version" is handled. Needs write permission on the install directory.

The background check (shown subtly at startup) can be disabled with
ROGERAI_NO_UPDATE_CHECK=1.
`, update.Repo)
	}
	fs.Parse(args)
	if *check {
		res, err := update.Check(Version)
		if err != nil {
			fmt.Printf("could not check for updates (offline?): %v\n", err)
			return nil // never fail the command on a network hiccup
		}
		if n := res.Notice(); n != "" {
			fmt.Println(n)
		} else {
			fmt.Printf("rogerai is up to date (v%s)\n", res.Current)
		}
		return nil
	}
	return update.Upgrade(Version, os.Stdout)
}

func cmdTopup(cfg config, args []string) error {
	usd := 10.0
	if len(args) > 0 {
		if f, err := strconv.ParseFloat(args[0], 64); err == nil {
			usd = f
		}
	}
	return client.Topup(cfg.Broker, cfg.User, usd)
}

// cmdBalance is the one money verb (C4): `balance` shows credits; `balance --topup
// [usd]` (or `balance topup [usd]`) opens checkout. Folds the old top-level `topup`
// into balance so a user has one noun for money.
func cmdBalance(cfg config, args []string) error {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	topup := fs.Float64("topup", -1, "buy this many $ of credits (opens checkout); bare --topup uses $10")
	fs.Parse(args)
	// Allow `rogerai balance topup [usd]` too (positional spelling).
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "topup" {
		usd := 10.0
		if len(rest) > 1 {
			if f, e := strconv.ParseFloat(rest[1], 64); e == nil {
				usd = f
			}
		}
		return client.Topup(cfg.Broker, cfg.User, usd)
	}
	if *topup >= 0 {
		usd := *topup
		if usd == 0 {
			usd = 10
		}
		return client.Topup(cfg.Broker, cfg.User, usd)
	}
	return client.Balance(cfg.Broker, cfg.User)
}

// cmdAccount is the one identity verb (C4): bare prints who you are (whoami);
// `account login` / `account logout` manage the GitHub link. Old top-level
// login/logout/whoami stay as hidden aliases.
func cmdAccount(cfg config, args []string) error {
	if len(args) == 0 {
		return client.Whoami()
	}
	switch args[0] {
	case "login":
		return client.Login(cfg.Broker, gitHubClientID())
	case "logout":
		return client.Logout()
	case "whoami", "show":
		return client.Whoami()
	default:
		return fmt.Errorf("usage: rogerai account [login|logout]")
	}
}

func cmdConfig(args []string) error {
	if len(args) == 0 {
		c := loadConfig()
		fmt.Printf("broker = %s\nuser   = %s\n", c.Broker, c.User)
		printLimits(c)
		fmt.Printf("(%s)\n", configPath())
		return nil
	}
	switch args[0] {
	case "limits":
		printLimits(loadConfig())
		return nil
	case "set-limit":
		return cmdSetLimit(args[1:])
	case "clear-limit":
		if len(args) < 2 {
			return fmt.Errorf("usage: rogerai config clear-limit <model>")
		}
		c := loadConfig()
		if c.Limits.Models != nil {
			delete(c.Limits.Models, args[1])
		}
		if err := saveConfig(c); err != nil {
			return err
		}
		fmt.Printf("cleared limit for %s\n", args[1])
		return nil
	case "get":
		c := loadConfig()
		if len(args) > 1 {
			switch args[1] {
			case "broker":
				fmt.Println(c.Broker)
			case "user":
				fmt.Println(c.User)
			}
			return nil
		}
		fmt.Printf("broker = %s\nuser   = %s\n", c.Broker, c.User)
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: rogerai config set <broker|user> <value>")
		}
		c := loadConfig()
		switch args[1] {
		case "broker":
			c.Broker = strings.TrimRight(args[2], "/")
		case "user":
			c.User = args[2]
		default:
			return fmt.Errorf("unknown key %q", args[1])
		}
		if err := saveConfig(c); err != nil {
			return err
		}
		fmt.Printf("set %s = %s\n", args[1], args[2])
	}
	return nil
}

// cmdSetLimit handles `rogerai config set-limit <model> [--max-in P] [--max-out P]
// [--min-tps N]`. Use "default" as the model to set the fallback limit. Only the
// flags passed are changed (the rest of that model's limit is preserved).
func cmdSetLimit(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: rogerai config set-limit <model|default> [--max-in P] [--max-out P] [--min-tps N]")
	}
	// The model is the first positional; flags follow it. (Go's flag package stops
	// at the first non-flag arg, so we pull the model out before parsing.)
	model := args[0]
	fs := flag.NewFlagSet("set-limit", flag.ExitOnError)
	maxIn := fs.Float64("max-in", -1, "$/1M input price cap (0 = no cap)")
	maxOut := fs.Float64("max-out", -1, "$/1M output price cap (the headline cap; 0 = no cap)")
	minTPS := fs.Float64("min-tps", -1, "min throughput floor in tok/s (0 = no floor)")
	fs.Parse(args[1:])
	c := loadConfig()
	var cur Limit
	if model == "default" {
		cur = c.Limits.Default
	} else if c.Limits.Models != nil {
		cur = c.Limits.Models[model]
	}
	if *maxIn >= 0 {
		cur.MaxIn = *maxIn
	}
	if *maxOut >= 0 {
		cur.MaxOut = *maxOut
	}
	if *minTPS >= 0 {
		cur.MinTPS = *minTPS
	}
	if model == "default" {
		c.Limits.Default = cur
	} else {
		if c.Limits.Models == nil {
			c.Limits.Models = map[string]Limit{}
		}
		c.Limits.Models[model] = cur
	}
	if err := saveConfig(c); err != nil {
		return err
	}
	fmt.Printf("set limit for %s: %s\n", model, limitStr(cur))
	return nil
}

// limitStr renders a Limit as a compact human line.
func limitStr(l Limit) string {
	parts := []string{}
	if l.MaxOut > 0 {
		parts = append(parts, fmt.Sprintf("max-out=%g", l.MaxOut))
	}
	if l.MaxIn > 0 {
		parts = append(parts, fmt.Sprintf("max-in=%g", l.MaxIn))
	}
	if l.MinTPS > 0 {
		parts = append(parts, fmt.Sprintf("min-tps=%g", l.MinTPS))
	}
	if len(parts) == 0 {
		return "no caps"
	}
	return strings.Join(parts, "  ")
}

// printLimits shows the spend-limits section (the static 3.4 view) on the CLI.
func printLimits(c config) {
	d := c.Limits.Default
	typ := c.Limits.TypicalOutTok
	if typ <= 0 {
		typ = 800
	}
	fmt.Printf("limits (typical reply ~%d out tokens):\n", typ)
	if len(c.Limits.Models) == 0 && d == (Limit{}) {
		fmt.Println("  (none set - no caps; `rogerai config set-limit <model> --max-out P`)")
		return
	}
	models := make([]string, 0, len(c.Limits.Models))
	for m := range c.Limits.Models {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		fmt.Printf("  %-22s %s\n", m, limitStr(c.Limits.Models[m]))
	}
	fmt.Printf("  %-22s %s\n", "· default (any other)", limitStr(d))
}

func hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "node"
	}
	return strings.ToLower(h)
}

// normalizeUpstream turns a user-supplied --upstream into the OpenAI-compatible
// chat-completions URL the agent POSTs to. It accepts a base URL
// (http://host:port), a /v1 URL, or the already-full /v1/chat/completions URL,
// so the natural inputs all work and match what detect.Detect produces.
func normalizeUpstream(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return u
	}
	u = strings.TrimRight(u, "/")
	switch {
	case strings.HasSuffix(u, "/chat/completions"):
		return u
	case strings.HasSuffix(u, "/v1"):
		return u + "/chat/completions"
	default:
		return u + "/v1/chat/completions"
	}
}

func usage() {
	fmt.Printf(`rogerai - a two-way radio for GPUs. run with no args for the interactive app.

  rogerai                       open the app (browse, tune in, chat)
  rogerai search                list models, cheapest first
  rogerai use <model>           local OpenAI endpoint for your bots  (--max-out $ caps spend)
  rogerai balance               wallet credits  (balance --topup [usd] to add funds)

providers (share your GPU):
  rogerai share                 go on air - FREE by default, no login (auto-detects your model)
  rogerai login                 link GitHub - only needed to EARN
  rogerai grant create --name my-bots   a free private key for your bots/family

more:
  rogerai account               who you are (login / logout)
  rogerai onboard               re-run the first-run setup
  rogerai config ...            broker, spend limits (rogerai config --help)
  rogerai upgrade · version · ping

advanced flags live behind --advanced (e.g. rogerai use <model> --advanced,
rogerai share --advanced, rogerai grant create --advanced).

env: ROGER_BROKER, ROGER_USER override config (%s)
`, configPath())
}
