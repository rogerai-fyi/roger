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

type config struct {
	Broker string `json:"broker"`
	User   string `json:"user"`
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

func main() {
	cfg := loadConfig()
	if len(os.Args) < 2 {
		// no args -> launch the interactive radio TUI
		if err := tui.Run(cfg.Broker, cfg.User); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch os.Args[1] {
	case "tui":
		err = tui.Run(cfg.Broker, cfg.User)
	case "search", "discover", "models":
		err = client.Search(cfg.Broker)
	case "balance":
		err = client.Balance(cfg.Broker, cfg.User)
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
	fs := flag.NewFlagSet("use", flag.ExitOnError)
	port := fs.Int("port", 4141, "local endpoint port")
	confidential := fs.Bool("confidential", false, "route only to confidential (TEE-attested) nodes")
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: rogerai use <model> [--port N] [--confidential]")
	}
	return client.Use(cfg.Broker, cfg.User, fs.Arg(0), *port, *confidential)
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
		fmt.Printf("broker = %s\nuser   = %s\n(%s)\n", c.Broker, c.User, configPath())
		return nil
	}
	switch args[0] {
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
  rogerai use <model> [--port N]     local OpenAI endpoint via the broker
  rogerai balance                    wallet credits
  rogerai topup [usd]                buy credits (Stripe checkout link)
  rogerai share [flags]              share your local model (auto-detects it)
  rogerai config set broker <url>    switch brokers
  rogerai version

env: ROGER_BROKER, ROGER_USER override config (%s)
`, configPath())
}
