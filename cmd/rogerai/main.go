// rogerai - the single client binary: consume models (search/use/balance) and
// share your own (share). One binary, all OS. The broker (rogerai-broker) is the
// only separately-deployed component.
//
//	rogerai search                    discover models (cheapest first)
//	rogerai use <model> [--port N]    open a local OpenAI endpoint via the broker
//	rogerai balance                   your wallet balance
//	rogerai limit --monthly $X        cap your spend per calendar month (0/off = no cap)
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
	"time"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/tui"
	"github.com/rogerai-fyi/roger/internal/update"
)

// Version is the client version (compared against the latest GitHub release for
// the update check / `rogerai upgrade`). Keep in sync with releases.
const Version = "4.5.2"

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
	Broker    string                `json:"broker"`
	User      string                `json:"user"`
	Limits    Limits                `json:"limits"`
	Onboarded bool                  `json:"onboarded,omitempty"`    // first-run wizard completed
	Share     *Share                `json:"share,omitempty"`        // saved provider config (the wizard's earn/free choice)
	Prices    map[string]SharePrice `json:"share_prices,omitempty"` // per-model price + schedule from the in-TUI editor
	Compact   bool                  `json:"compact,omitempty"`      // windowshade compact-mode toggle (the in-TUI [m] choice, persisted)
	// Station is this install's friendly, NON-SENSITIVE broadcast callsign (e.g.
	// `brave-otter-37`). It is the public-facing identity in /discover - NOT the
	// hostname - so it never leaks the machine name. Auto-generated once and persisted
	// (loadOrCreateStation); the owner can rename it (`share --node`, or the TUI [2]
	// SHARE `n` rename). The broker node id is derived as `<station>-<model-slug>`.
	Station string `json:"station,omitempty"`
}

// SharePrice is a per-model price + time-of-use schedule the in-TUI pricing editor
// produced, persisted so the choice survives the session. Mirrors tui.Pricing.
type SharePrice struct {
	PriceIn  float64       `json:"price_in,omitempty"`
	PriceOut float64       `json:"price_out,omitempty"`
	Windows  []SchedWindow `json:"windows,omitempty"`
}

// SchedWindow mirrors tui.SchedWindow / protocol.PriceWindow for persistence.
type SchedWindow struct {
	Start string  `json:"start"`
	End   string  `json:"end"`
	In    float64 `json:"price_in,omitempty"`
	Out   float64 `json:"price_out,omitempty"`
	Free  bool    `json:"free,omitempty"`
}

// Share is the provider config the onboarding wizard saves: the model to expose,
// the chosen port, the price (0/0 = free), and optionally the verified local
// upstream endpoint the guided fallback found (so a non-default / custom-port
// server is remembered and re-detection isn't needed next time). Absent = not a
// provider yet.
type Share struct {
	Model    string  `json:"model"`
	Port     int     `json:"port"`
	PriceIn  float64 `json:"price_in,omitempty"`
	PriceOut float64 `json:"price_out,omitempty"`
	Upstream string  `json:"upstream,omitempty"` // saved/verified local endpoint (the (e) source)
	// MaxOnAir is the SOFT local cap on how many bands may be ON AIR at once from this
	// CLI (the share.max_on_air knob). It is a deliberate "reset the CLI" guard read
	// ONCE at startup: changing it requires a restart. <=0 means "use the default" (see
	// defaultShareMaxOnAir). The TUI blocks flipping another row on air past this and
	// tells the user to take one off air or raise the knob + restart.
	MaxOnAir int `json:"max_on_air,omitempty"`
}

// defaultShareMaxOnAir is the soft local on-air cap when share.max_on_air is unset
// (or <=0). Local UX guard against over-subscribing a host's GPU; the broker's hard
// per-owner cap is the real backstop.
const defaultShareMaxOnAir = 4

// shareMaxOnAir resolves the effective soft on-air cap from the config: the saved
// share.max_on_air when positive, else the default. Read once at CLI startup.
func (c config) shareMaxOnAir() int {
	if c.Share != nil && c.Share.MaxOnAir > 0 {
		return c.Share.MaxOnAir
	}
	return defaultShareMaxOnAir
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

// loadOrCreateStation returns this install's friendly, NON-SENSITIVE broadcast
// callsign (e.g. `brave-otter-37`), generating + persisting one with crypto/rand on
// first use. It is the PUBLIC station identity surfaced in /discover - deliberately
// NOT the hostname - and is stable across restarts so a node re-registers as the same
// broker id. The owner can override it with `share --node` or the TUI rename, both of
// which persist via saveStation.
func loadOrCreateStation() string {
	c := loadConfig()
	if s := agentSlugStation(c.Station); s != "" {
		return s
	}
	st := agent.GenerateStation()
	saveStation(st)
	return st
}

// saveStation persists the owner's station callsign (a rename or the first
// auto-generated one). Empty input is ignored so a rename never blanks the station.
func saveStation(station string) {
	station = agentSlugStation(station)
	if station == "" {
		return
	}
	c := loadConfig()
	c.Station = station
	_ = saveConfig(c)
}

// agentSlugStation normalizes a station name to the same broker-safe slug the node id
// uses (lowercased, non-alphanumerics collapsed to single dashes), so what the owner
// types, what is persisted, and what appears in /discover all match. Empty in -> empty.
func agentSlugStation(s string) string { return agent.SlugStation(s) }

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

// tuiHooks supplies the host bits the TUI can't compute (the broadcast station, HW,
// the public GitHub client id, the saved share config) plus the login/topup/grant
// closures, so the in-TUI /share, /login, /topup, /grant flows are real actions.
func tuiHooks(cfg config) tui.Hooks {
	h := tui.Hooks{
		// Station is the PUBLIC, NON-SENSITIVE callsign the TUI derives every band's node
		// id from (`<station>-<model>`). It is the saved/auto-generated station, NEVER the
		// hostname, so going on air in the TUI leaks no machine name or port. SaveStation
		// persists a rename (the TUI does no disk I/O itself).
		Station:     loadOrCreateStation(),
		SaveStation: saveStation,
		HW:          detectHW(),
		GitHubID:    gitHubClientID(),
		LinkedLogin: client.LinkedLogin(), // "" when not logged in -> header shows the /login prompt
		Login:       client.LoginReturn,
		// Split begin/poll so the TUI renders its own clean login panel + auto-opens the
		// browser (instead of the CLI printing the code to the hidden-behind-the-TUI stdout).
		LoginBegin: func(broker, clientID string) (tui.LoginDevice, error) {
			d, err := client.LoginBegin(broker, clientID)
			if err != nil {
				return tui.LoginDevice{}, err
			}
			return tui.LoginDevice{VerificationURI: d.VerificationURI, UserCode: d.UserCode, Handle: d.Handle}, nil
		},
		LoginPoll: func(broker, clientID string, d tui.LoginDevice) (string, error) {
			return client.LoginPoll(broker, clientID, client.Device{VerificationURI: d.VerificationURI, UserCode: d.UserCode, Handle: d.Handle})
		},
		Logout:   client.LogoutReturn,
		TopupURL: client.TopupURL,
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
		// Persist a per-model price + schedule the in-TUI editor produced (the host
		// owns the config write; the TUI does no disk I/O).
		SavePrice: func(model string, p tui.Pricing) {
			c := loadConfig()
			if c.Prices == nil {
				c.Prices = map[string]SharePrice{}
			}
			c.Prices[model] = SharePrice{PriceIn: p.In, PriceOut: p.Out, Windows: toCfgWindows(p.Windows)}
			_ = saveConfig(c)
		},
		// Seed + persist the windowshade compact-mode choice so [m] sticks across launches
		// (the host owns the config write; the TUI does no disk I/O).
		Compact: cfg.Compact,
		SaveCompact: func(on bool) {
			c := loadConfig()
			c.Compact = on
			_ = saveConfig(c)
		},
	}
	// Soft local on-air cap (share.max_on_air), read ONCE here at startup: the TUI shows
	// the ON AIR n/max slots and blocks flipping another band on air at the cap. Changing
	// it is a deliberate restart-the-CLI knob (we never re-read it mid-session).
	h.ShareMaxOnAir = cfg.shareMaxOnAir()
	if cfg.Share != nil {
		h.ShareModel, h.SharePriceI, h.SharePriceO = cfg.Share.Model, cfg.Share.PriceIn, cfg.Share.PriceOut
	}
	// Seed the editor with prices set in a previous session.
	if len(cfg.Prices) > 0 {
		h.SavedPrices = map[string]tui.Pricing{}
		for mdl, p := range cfg.Prices {
			h.SavedPrices[mdl] = tui.Pricing{In: p.PriceIn, Out: p.PriceOut, Windows: toTUIWindows(p.Windows)}
		}
	}
	return h
}

// toCfgWindows / toTUIWindows convert the in-TUI schedule windows to/from the
// persisted config form.
func toCfgWindows(ws []tui.SchedWindow) []SchedWindow {
	if len(ws) == 0 {
		return nil
	}
	out := make([]SchedWindow, 0, len(ws))
	for _, w := range ws {
		out = append(out, SchedWindow{Start: w.Start, End: w.End, In: w.In, Out: w.Out, Free: w.Free})
	}
	return out
}

func toTUIWindows(ws []SchedWindow) []tui.SchedWindow {
	if len(ws) == 0 {
		return nil
	}
	out := make([]tui.SchedWindow, 0, len(ws))
	for _, w := range ws {
		out = append(out, tui.SchedWindow{Start: w.Start, End: w.End, In: w.In, Out: w.Out, Free: w.Free})
	}
	return out
}

func main() {
	cfg := loadConfig()
	tui.SetVersion(Version) // help/about surfaces match `rogerai version`
	// Sweep a leftover binary from a prior Windows self-update (the locked .old that
	// couldn't be deleted while the old process was still running). No-op elsewhere.
	update.CleanupOld()
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
	case "use", "connect", "tune":
		// `connect`/`tune` mirror the TUI's /connect /tune so the CLI verbs and the
		// in-app slash commands are one mental model.
		err = cmdUse(cfg, os.Args[2:])
	case "share":
		err = cmdShare(cfg, os.Args[2:])
	case "limits":
		// Mirror the TUI's /limits (prints the spend-limits view); editing stays under
		// `config set-limit` for the flags.
		err = cmdConfig(append([]string{"limits"}, os.Args[2:]...))
	case "limit":
		// `rogerai limit --monthly $X` sets the per-account MONTHLY SPEND CAP (a budget
		// limit). Bare `rogerai limit` shows the current cap + month-to-date spend.
		err = cmdLimit(cfg, os.Args[2:])
	case "payout", "payouts", "cashout":
		err = cmdPayout(cfg, os.Args[2:])
	case "grant":
		err = cmdGrant(cfg, os.Args[2:])
	case "onboard", "setup":
		err = cmdOnboard(cfg, os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "support", "community", "help-me", "discord":
		// Mirror the TUI's /support: open the website (community + Discord live there).
		err = cmdSupport()
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

// supportURL is the website (community + Discord link live in its footer). Per the
// founder, `rogerai support` / the TUI's /support point here, not straight at Discord,
// so the footer stays the single source of truth for the community link.
const supportURL = "https://rogerai.fyi"

// cmdSupport opens the website where the community / Discord link lives. tui.OpenURL
// self-gates on an interactive TTY (never auto-opens headless / piped), and we print
// the URL regardless as the fallback.
func cmdSupport() error {
	fmt.Println("RogerAI support - community, docs, and the Discord invite live on the site:")
	fmt.Printf("  %s\n", supportURL)
	fmt.Println("  (if your browser didn't open, paste the URL above)")
	tui.OpenURL(supportURL)
	return nil
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
	freq := fs.String("freq", "", "tune in to a PRIVATE band by its frequency code, e.g. \"147.520 MHz 8F3K-9M2Q\" (the code is what matters; cosmetic part optional)")
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
		p, err := freePort(4141) // auto-pick + the endpoint line prints the chosen port
		if err != nil {
			return err
		}
		useport = p
	}
	return client.Use(cfg.Broker, cfg.User, model, client.UseOptions{
		Port: useport, Confidential: *confidential,
		MaxIn: lim.MaxIn, MaxOut: lim.MaxOut, MinTPS: lim.MinTPS,
		TypicalOut: typical, Yes: *yes, Freq: strings.TrimSpace(*freq),
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
	// --node sets the friendly STATION callsign (e.g. `brave-otter`). Empty default: use
	// the persisted station (auto-generated once on first share, never the hostname). A
	// given --node is REMEMBERED as the station so it sticks across restarts and the TUI.
	// The broker node id is then `<station>-<model-slug>` (no hostname, no port leak).
	node := fs.String("node", "", "station callsign (e.g. brave-otter); persisted. default: your saved/auto station")
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
	confidential := fs.Bool("confidential", false, "advertise as confidential - requires real TEE hardware (AMD SEV-SNP); a fresh hardware quote is generated + verified by the broker")
	private := fs.Bool("private", false, "share on a PRIVATE band: hidden from the public market, reachable only by a secret frequency code (shown once). Requires `rogerai login`.")
	freeWindow := fs.String("free-window", "", "daily FREE window in UTC, e.g. 03:00-03:30")
	schedule := fs.String("schedule", "", `time-of-use schedule, JSON e.g. '[{"start":"18:00","end":"22:00","price_in":0.5,"price_out":0.7}]'`)
	advanced := fs.Bool("advanced", false, "show advanced flags (--node --region --parallel --upstream --ctx --confidential --free-window --schedule)")
	fs.Parse(args)
	if *advanced {
		fmt.Println("advanced flags: --node --region --parallel --upstream --upstream-key --ctx --confidential --free-window --schedule")
	}

	up := *upstream
	mdl := *model
	ctxLen := *ctx
	// A saved/verified upstream (from the guided fallback) is the (e) source: probe
	// it first so a non-default / custom-port server is remembered, not re-hunted.
	savedUp := ""
	if cfg.Share != nil {
		savedUp = cfg.Share.Upstream
	}
	if up == "" {
		found := detect.DetectWith(savedUp)
		if len(found) == 0 {
			// GUIDED FALLBACK: nothing is running. Walk the user through it instead of
			// erroring out - pick your tool for a one-liner, or paste an endpoint we
			// verify. Non-interactive runs still get the clear "start one or --upstream".
			picked, ok := guidedUpstream(cfg.Broker)
			if !ok {
				return fmt.Errorf("no local LLM detected (tried Ollama/LM Studio/llama.cpp/vLLM/Jan/LiteLLM and your open ports). Start one, then `rogerai share`, or pass --upstream <url>")
			}
			found = []detect.Found{picked}
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
		// Remember the verified upstream so a custom-port / guided-fallback endpoint is
		// not re-hunted next launch (the (e) saved-config source).
		if pick.BaseURL != "" && pick.BaseURL != savedUp {
			c := loadConfig()
			if c.Share == nil {
				c.Share = &Share{}
			}
			c.Share.Upstream = pick.BaseURL
			_ = saveConfig(c)
		}
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

	// Resolve the PUBLIC station callsign and derive the broker node id from it. A
	// `--node` value is the owner naming/renaming their station: persist it so it sticks
	// across restarts and matches the TUI. Otherwise use the saved/auto-generated station
	// (never the hostname). The node id is `<station>-<model-slug>` - no hostname and no
	// upstream port ever appear in it (it is echoed verbatim to consumers in /discover).
	station := ""
	if s := agentSlugStation(*node); s != "" {
		station = s
		saveStation(station) // a --node rename sticks
	} else {
		station = loadOrCreateStation()
	}
	// instance 0: the CLI serves one model per process, so no same-model disambiguation
	// is needed here (the TUI passes a real index when one host shares the same model on
	// two local servers).
	nodeID := agent.ShareNodeID(station, mdl, 0)

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
	if *confidential {
		// Fail FAST and CLEARLY on hardware without a TEE, rather than sending a fake
		// claim: the node-side attestation generates a REAL SEV-SNP quote at register
		// time and errors out here if no TEE device is present.
		fmt.Println("confidential: generating a real TEE attestation quote at registration (needs AMD SEV-SNP hardware) - the broker verifies it before granting the badge.")
	}

	if *priceIn == 0 && *priceOut == 0 && len(sched) == 0 {
		fmt.Println("sharing FREE (price 0/0) - on air with no login. set --price-out to earn (needs `rogerai login`).")
	}
	if *private {
		// A private band requires login (the broker 401s an anonymous private register).
		// Fail clearly here rather than after a detection/upstream probe.
		if client.LinkedLogin() == "" {
			return fmt.Errorf("`--private` needs a GitHub-linked owner - run `rogerai login` first (anonymous private sharing is not allowed)")
		}
		fmt.Println("sharing PRIVATE - hidden from the public market; only people with your frequency code can tune in.")
	}
	// Operator soft price-warn (non-blocking): if your out-price is far above the live
	// per-model market median, flag it so a fat-finger surfaces before you go on air.
	if msg := softPriceWarn(*broker, mdl, *priceOut); msg != "" {
		fmt.Println(msg)
	}
	cfgRun := agent.Config{
		Broker: *broker, Upstream: up, UpstreamKey: *upKey,
		NodeID: nodeID, Region: *region, HW: detectHW(), Model: mdl,
		PriceIn: *priceIn, PriceOut: *priceOut, Ctx: ctxLen, Parallel: *parallel,
		Confidential: *confidential, Private: *private, Schedule: sched,
	}
	if !*private {
		return agent.Run(cfgRun)
	}
	// Private: start (not Run) so we can surface the one-time frequency code, then block.
	sess, err := agent.Start(cfgRun)
	if err != nil {
		return err
	}
	if _, code, display := sess.Band(); code != "" {
		fmt.Printf("\n  %s YOUR FREQUENCY CODE (shown once - copy it now)\n", "◉")
		fmt.Printf("\n      %s\n\n", display)
		fmt.Println("  share this with whoever should reach your station. They tune in with:")
		fmt.Printf("      rogerai use %s --freq %q\n", mdl, code)
		fmt.Println("  the cosmetic \"MHz\" part is optional - the code after it is what matters.")
	} else if _, _, display := sess.Band(); display != "" {
		fmt.Printf("\n  on air on your existing private band: %s (code shown only at first creation)\n", display)
	}
	select {} // serve forever
}

// softPriceWarn returns a non-blocking warning when out-price is well above the live
// per-model market median (>3x), so an operator fat-finger surfaces before going on
// air. Returns "" when there is no signal (no market data, price 0, or within range).
// Best-effort: a market-fetch failure is silent (never blocks sharing).
func softPriceWarn(broker, model string, priceOut float64) string {
	if priceOut <= 0 {
		return ""
	}
	med, ok := client.MarketMedianOut(broker, model)
	if !ok || med <= 0 {
		return ""
	}
	if priceOut > 3*med {
		return fmt.Sprintf("  ! heads up: your %.2f $/1M out is %.1fx the current market median (%.2f) for %q - double-check it's not a typo.", priceOut, priceOut/med, med, model)
	}
	return ""
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

// cmdPayout is the provider money-OUT verb group: cash out earnings from the
// terminal. Every call is Ed25519-signed (the same identity the rest of the client
// uses), so a headless `rogerai share` provider can withdraw + see KYC status without
// a browser session. Requires a GitHub-linked account (run `rogerai login`); the
// broker enforces the unchanged policy (90-day hold, $25 min, monthly, Connect-KYC).
// Amounts are shown in dollars (1 credit == $1).
//
//	rogerai payout            -> status (default)
//	rogerai payout status     -> KYC state + payable/held + next-payable date + policy
//	rogerai payout onboard    -> open the Stripe Connect KYC link (prints it too)
//	rogerai payout request    -> request a payout (broker pays the full payable amount)
//	rogerai payout history    -> past payouts + their states
func cmdPayout(cfg config, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	// Help works without login (so a new provider can read it before linking).
	if sub == "-h" || sub == "--help" || sub == "help" {
		payoutUsage()
		return nil
	}
	// Login gate: payouts are KYC + GitHub-linked only. Without a local link there is
	// no signing identity bound to an account, so point at `rogerai login` up front.
	if client.LinkedLogin() == "" {
		fmt.Println("not logged in - run `rogerai login` to link GitHub (required to earn + cash out)")
		return nil
	}
	switch sub {
	case "status", "":
		return payoutStatus(cfg)
	case "onboard", "kyc", "setup":
		return payoutOnboard(cfg)
	case "request", "withdraw", "cashout":
		return payoutRequest(cfg, args[1:])
	case "history", "log", "list":
		return payoutHistory(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown payout command %q\n", sub)
		payoutUsage()
		return nil
	}
}

func payoutUsage() {
	fmt.Println(`rogerai payout - cash out your provider earnings (dollars; 1 credit = $1)

  rogerai payout status     Connect/KYC state + payable vs held + next-payable date
  rogerai payout onboard     complete Stripe Connect KYC (opens the browser)
  rogerai payout request      request a payout of your payable balance
  rogerai payout history      past payouts and their states

  Policy: 90-day hold, $25 minimum, monthly. Requires GitHub login + Connect KYC.`)
}

// payoutPolicyLine is the single one-liner describing the unchanged policy, reused by
// status so the user always sees the terms.
func payoutPolicyLine(st client.PayoutStatus) string {
	hold := st.HoldDays
	if hold == 0 {
		hold = 90
	}
	min := st.MinPayout
	if min == 0 {
		min = 25
	}
	sched := st.Schedule
	if sched == "" {
		sched = "monthly"
	}
	return fmt.Sprintf("policy     %d-day hold · $%s min · %s", hold, trimAmt(min), sched)
}

// trimAmt formats a dollar amount without trailing zeros (25 -> "25", 25.5 -> "25.50").
func trimAmt(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// payoutDate renders a unix time as a short date, or "-" for 0.
func payoutDate(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	return time.Unix(unix, 0).Format("2006-01-02")
}

// kycLabel maps the Connect status to a human phrase.
func kycLabel(status string) string {
	switch status {
	case "active":
		return "active (KYC complete)"
	case "onboarding":
		return "pending (finish onboarding)"
	case "restricted":
		return "restricted (Stripe needs more info)"
	default:
		return "not onboarded"
	}
}

func payoutStatus(cfg config) error {
	st, err := client.FetchPayoutStatus(cfg.Broker)
	if err != nil {
		return err
	}
	payable := st.Earnings.Payable
	held := st.Earnings.Held + st.Earnings.Reserved
	fmt.Println("\n  PAYOUT")
	fmt.Printf("    KYC        %s\n", kycLabel(st.Status))
	fmt.Printf("    payable    $%.2f   (ready to cash out)\n", payable)
	fmt.Printf("    held       $%.2f   (inside the %d-day hold)\n", held, holdOr90(st))
	if st.Earnings.Paid > 0 {
		fmt.Printf("    paid out   $%.2f   (lifetime)\n", st.Earnings.Paid)
	}
	if next := st.Earnings.NextRelease; next > 0 {
		fmt.Printf("    next due   %s   (held earnings become payable)\n", payoutDate(next))
	}
	fmt.Printf("    %s\n", payoutPolicyLine(st))
	// Actionable next step.
	switch {
	case st.Status != "active":
		fmt.Println("\n  complete KYC to cash out:  rogerai payout onboard")
	case payable < minOr25(st):
		fmt.Printf("\n  below the $%s minimum - keep earning, then `rogerai payout request`.\n", trimAmt(minOr25(st)))
	default:
		fmt.Println("\n  ready to cash out:  rogerai payout request")
	}
	return nil
}

func holdOr90(st client.PayoutStatus) int {
	if st.HoldDays == 0 {
		return 90
	}
	return st.HoldDays
}

func minOr25(st client.PayoutStatus) float64 {
	if st.MinPayout == 0 {
		return 25
	}
	return st.MinPayout
}

func payoutOnboard(cfg config) error {
	url, err := client.FetchOnboardURL(cfg.Broker)
	if err != nil {
		return err
	}
	fmt.Println("opening Stripe Connect onboarding (complete KYC to enable payouts)...")
	fmt.Printf("  %s\n", url)
	fmt.Println("  (if your browser didn't open, paste the URL above)")
	tui.OpenURL(url)
	return nil
}

func payoutRequest(cfg config, args []string) error {
	// Pre-flight against the live status so the user gets a clear, local error (KYC /
	// minimum / payable cap) before the broker round-trip. The broker re-checks every
	// gate authoritatively; this just turns rejections into friendly messages.
	st, err := client.FetchPayoutStatus(cfg.Broker)
	if err != nil {
		return err
	}
	min := minOr25(st)
	payable := st.Earnings.Payable
	if st.Status != "active" {
		fmt.Println("KYC not complete - run `rogerai payout onboard` first.")
		return nil
	}
	// Optional [amount]: validate it fits the rules. The broker pays out the FULL
	// payable balance (monthly batch), so an amount is a sanity check, not a partial
	// withdrawal; surface that honestly rather than silently ignoring it.
	if len(args) > 0 {
		amt, perr := strconv.ParseFloat(strings.TrimPrefix(args[0], "$"), 64)
		if perr != nil || amt <= 0 {
			return fmt.Errorf("not a valid amount: %q", args[0])
		}
		if amt < min {
			fmt.Printf("$%.2f is below the $%s minimum.\n", amt, trimAmt(min))
			return nil
		}
		if amt > payable+1e-9 {
			fmt.Printf("$%.2f is more than your payable balance ($%.2f).\n", amt, payable)
			return nil
		}
		if amt < payable-1e-9 {
			fmt.Printf("note: payouts transfer your FULL payable balance ($%.2f), not a partial amount.\n", payable)
		}
	}
	if payable < min {
		fmt.Printf("payable $%.2f is below the $%s minimum - keep earning.\n", payable, trimAmt(min))
		return nil
	}
	rec, err := client.RequestPayout(cfg.Broker)
	if err != nil {
		return err
	}
	fmt.Printf("payout requested: $%.2f (state: %s", rec.Amount, rec.State)
	if rec.StripeTransferID != "" {
		fmt.Printf(", transfer %s", rec.StripeTransferID)
	}
	fmt.Println(")")
	return nil
}

func payoutHistory(cfg config) error {
	pays, err := client.FetchPayoutHistory(cfg.Broker)
	if err != nil {
		return err
	}
	if len(pays) == 0 {
		fmt.Println("no payouts yet - run `rogerai payout status` to see what's payable.")
		return nil
	}
	fmt.Printf("%-12s %-9s %-9s %s\n", "DATE", "AMOUNT", "STATE", "TRANSFER")
	for _, p := range pays {
		tr := p.StripeTransferID
		if tr == "" {
			tr = "-"
		}
		fmt.Printf("%-12s $%-8.2f %-9s %s\n", payoutDate(p.CreatedAt), p.Amount, p.State, tr)
	}
	return nil
}

// cmdBalance is the one money verb (C4): `balance` shows credits; `balance --topup
// [usd]` (or `balance topup [usd]`) opens checkout. Folds the old top-level `topup`
// into balance so a user has one noun for money.
func cmdBalance(cfg config, args []string) error {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	topup := fs.Float64("topup", -1, "add this many $ to your wallet (opens checkout); bare --topup uses $10")
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

// cmdLimit is the per-account MONTHLY SPEND CAP verb (a budget limit, modeled on
// Groq's "set a max you'll pay per month"). `rogerai limit --monthly $X` sets the
// cap; `--monthly 0` or `--monthly off` clears it (unlimited); bare `rogerai limit`
// shows the current cap + month-to-date spend. Requires login (the cap is per
// account/wallet, enforced server-side at every paid path).
func cmdLimit(cfg config, args []string) error {
	fs := flag.NewFlagSet("limit", flag.ExitOnError)
	monthly := fs.String("monthly", "", "max $ to spend per calendar month (e.g. 25); 0 or off = no cap")
	fs.Parse(args)
	if *monthly == "" {
		// Read-only: show the current cap + month-to-date spend.
		info, err := client.GetMonthlyLimit(cfg.Broker, cfg.User)
		if err != nil {
			return err
		}
		if info.Cap > 0 {
			fmt.Printf("monthly spend limit: $%.2f   (used $%.2f this month)\n", info.Cap, info.Spend)
		} else {
			fmt.Printf("monthly spend limit: none   (used $%.2f this month)\n", info.Spend)
			fmt.Println("set one with `rogerai limit --monthly $X`")
		}
		return nil
	}
	cap, err := parseMonthlyCap(*monthly)
	if err != nil {
		return err
	}
	info, err := client.SetMonthlyLimit(cfg.Broker, cfg.User, cap)
	if err != nil {
		return err
	}
	if info.Cap > 0 {
		fmt.Printf("monthly spend limit set: $%.2f   (used $%.2f this month)\n", info.Cap, info.Spend)
	} else {
		fmt.Printf("monthly spend limit cleared - no cap   (used $%.2f this month)\n", info.Spend)
	}
	return nil
}

// parseMonthlyCap reads the `--monthly` value: "off"/"none"/"unlimited"/"0" clear the
// cap (return 0); otherwise a positive dollar amount (a leading "$" is tolerated).
func parseMonthlyCap(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	switch strings.ToLower(s) {
	case "off", "none", "unlimited", "0":
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid monthly limit %q - use a dollar amount (e.g. 25) or `off`", s)
	}
	return f, nil
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
  rogerai use <model>           local OpenAI endpoint for your bots  (alias: connect · --max-out $ caps spend)
  rogerai balance               your wallet balance  (balance --topup [usd] to add funds)
  rogerai limit --monthly $X    cap your spend per calendar month  (0/off = no cap)

providers (share your GPU):
  rogerai share                 go on air - FREE by default, no login (auto-detects your model)
  rogerai login                 link GitHub - only needed to EARN
  rogerai payout                cash out your earnings (status · onboard · request · history)
  rogerai grant create --name my-bots   a free private key for your bots/family

more:
  rogerai account               who you are (login / logout)
  rogerai onboard               re-run the first-run setup
  rogerai config ...            broker, spend limits (rogerai config --help)
  rogerai support               open rogerai.fyi - community + Discord
  rogerai upgrade · version · ping

advanced flags live behind --advanced (e.g. rogerai use <model> --advanced,
rogerai share --advanced, rogerai grant create --advanced).

env: ROGER_BROKER, ROGER_USER override config (%s)
`, configPath())
}
