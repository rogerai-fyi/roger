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

	"github.com/bownux/rogerai/internal/agent"
	"github.com/bownux/rogerai/internal/client"
	"github.com/bownux/rogerai/internal/detect"
	"github.com/bownux/rogerai/internal/protocol"
	"github.com/bownux/rogerai/internal/tui"
)

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
	Broker string `json:"broker"`
	User   string `json:"user"`
	Limits Limits `json:"limits"`
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

func main() {
	cfg := loadConfig()
	if len(os.Args) < 2 {
		// no args -> launch the interactive radio TUI
		if err := tui.RunWith(cfg.Broker, cfg.User, tuiLimits(cfg)); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch os.Args[1] {
	case "tui":
		err = tui.RunWith(cfg.Broker, cfg.User, tuiLimits(cfg))
	case "search", "discover", "models":
		err = client.Search(cfg.Broker)
	case "balance":
		err = client.Balance(cfg.Broker, cfg.User)
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
	case "config":
		err = cmdConfig(os.Args[2:])
	case "version":
		fmt.Println("rogerai 0.1.0 (P1)")
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
		return fmt.Errorf("usage: rogerai use <model> [--port N] [--confidential] [--max-in P] [--max-out P] [--min-tps N] [--yes]")
	}
	// The model is the first positional; flags follow it. (Go's flag package stops
	// at the first non-flag arg, so we pull the model out before parsing.)
	model := args[0]
	fs := flag.NewFlagSet("use", flag.ExitOnError)
	port := fs.Int("port", 4141, "local endpoint port")
	confidential := fs.Bool("confidential", false, "route only to confidential (TEE-attested) nodes")
	// --max-in is the new name; --max-price is its backward-compatible alias.
	maxIn := fs.Float64("max-in", -1, "cap: skip stations above this $/1M INPUT price; 0 = no cap")
	maxPrice := fs.Float64("max-price", -1, "alias of --max-in ($/1M input price)")
	maxOut := fs.Float64("max-out", -1, "cap: skip stations above this $/1M OUTPUT price (the headline cap); 0 = no cap")
	minTPS := fs.Float64("min-tps", -1, "require at least this measured throughput (tok/s); 0 = no floor")
	yes := fs.Bool("yes", false, "skip the connect-time confirm (for scripts / Hermes / bots)")
	fs.Parse(args[1:])
	// Start from the resolved per-model limit (or Default), then let flags override
	// it for this session. -1 sentinel = flag not passed (keep the stored limit).
	lim, typical := cfg.resolve(model)
	if *maxIn >= 0 {
		lim.MaxIn = *maxIn
	}
	if *maxPrice >= 0 { // alias; an explicit --max-price overrides --max-in
		lim.MaxIn = *maxPrice
	}
	if *maxOut >= 0 {
		lim.MaxOut = *maxOut
	}
	if *minTPS >= 0 {
		lim.MinTPS = *minTPS
	}
	return client.Use(cfg.Broker, cfg.User, model, client.UseOptions{
		Port: *port, Confidential: *confidential,
		MaxIn: lim.MaxIn, MaxOut: lim.MaxOut, MinTPS: lim.MinTPS,
		TypicalOut: typical, Yes: *yes,
	})
}

func cmdShare(cfg config, args []string) error {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	broker := fs.String("broker", cfg.Broker, "broker URL")
	node := fs.String("node", hostname(), "node id")
	model := fs.String("model", "", "model to expose (default: first detected)")
	upstream := fs.String("upstream", "", "local OpenAI endpoint (default: auto-detect)")
	upKey := fs.String("upstream-key", "", "bearer key for the upstream (optional)")
	region := fs.String("region", "home", "region")
	parallel := fs.Int("parallel", 4, "concurrent poll workers (per-node concurrency)")
	priceIn := fs.Float64("price-in", 0.20, "credits per 1M input tokens (base/fallback)")
	priceOut := fs.Float64("price-out", 0.30, "credits per 1M output tokens (base/fallback)")
	ctx := fs.Int("ctx", 32768, "context length")
	confidential := fs.Bool("confidential", false, "advertise as confidential (TEE-attested)")
	attestation := fs.String("attestation", "", "TEE attestation blob (dev placeholder if --confidential without it)")
	freeWindow := fs.String("free-window", "", "daily FREE window in UTC, e.g. 03:00-03:30")
	schedule := fs.String("schedule", "", `time-of-use schedule, JSON e.g. '[{"start":"18:00","end":"22:00","price_in":0.5,"price_out":0.7}]'`)
	fs.Parse(args)

	up := *upstream
	mdl := *model
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
		fmt.Printf("detected %s at %s - exposing model %q\n", pick.Name, pick.BaseURL, mdl)
	}
	if mdl == "" {
		return fmt.Errorf("could not determine a model; pass --model")
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

	return agent.Run(agent.Config{
		Broker: *broker, Upstream: up, UpstreamKey: *upKey,
		NodeID: *node, Region: *region, HW: detectHW(), Model: mdl,
		PriceIn: *priceIn, PriceOut: *priceOut, Ctx: *ctx, Parallel: *parallel,
		Confidential: *confidential, Attestation: att, Schedule: sched,
	})
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
	fmt.Printf(`rogerai - crowd-sourced LLM marketplace client

  rogerai search                     discover models (cheapest first)
  rogerai use <model> [--max-out P] [--max-in P] [--min-tps N] [--yes]   local OpenAI endpoint; set your spend limits
  rogerai balance                    wallet credits
  rogerai topup [usd]                buy credits (opens a checkout link)
  rogerai login                      link a GitHub account (only needed to monetize)
  rogerai logout                     forget the local GitHub link
  rogerai whoami                     show your signed identity + linked GitHub
  rogerai share [flags]              share your local model (auto-detects it)
  rogerai config set broker <url>    switch brokers
  rogerai config limits              show your per-model spend limits
  rogerai config set-limit <model> --max-out P [--max-in P] [--min-tps N]
  rogerai config clear-limit <model>
  rogerai version

env: ROGER_BROKER, ROGER_USER override config (%s)
`, configPath())
}
