// rogerai - the single client binary: consume models (search/use/balance) and
// share your own (share). One binary, all OS. The broker (rogerai-broker) is the
// only separately-deployed component.
//
//	roger search                    discover models (cheapest first)
//	roger use <model> [--port N]    open a local OpenAI endpoint via the broker
//	roger balance                   your wallet balance
//	roger limit --monthly $X        cap your spend per calendar month (0/off = no cap)
//	roger share [flags]             become a provider (auto-detects a local LLM)
//	roger config set broker <url>   switch brokers (federation: pick who you trust)
//	roger config get [key]
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
// the update check / `roger upgrade`). It is a var (not a const) so a release/beta
// build can stamp a semver via the linker without editing source:
//
//	go build -ldflags "-X main.Version=4.8.0-beta.1" ./cmd/rogerai
//
// The default below is the fallback for a plain `go build`. Keep it in sync with
// releases. Use semver, optionally with a prerelease suffix (e.g. 4.8.0-beta.1).
var Version = "4.14.0"

// The production broker is the default - `rogerai` works out of the box, no config.
// Override per-session with ROGER_BROKER=... or persist with `roger config set broker`.
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
	Webui     *bool                 `json:"webui,omitempty"`        // browser node console: nil/true = on (default), false = off; --no-webui overrides off for a run
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
	// UpstreamKey is the bearer key a key-protected local server requires (vLLM
	// --api-key, a LiteLLM master key, llama.cpp --api-key, LM Studio's API-key
	// toggle). Saved so a keyed upstream is not re-prompted every launch; sent as a
	// Bearer when the agent forwards jobs. Empty for the common no-auth local server.
	UpstreamKey string `json:"upstream_key,omitempty"`
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
const defaultShareMaxOnAir = 5

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
	if err := os.WriteFile(configPath(), b, 0600); err != nil {
		return err
	}
	// WriteFile does NOT re-apply the mode to a file that already exists, so a config
	// created by an older build (before it held secrets like upstream_key) could linger
	// world-readable. Force 0600 now that it can contain a bearer credential at rest.
	_ = os.Chmod(configPath(), 0600)
	return nil
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
		// HW is the PRIVACY-BUCKETED class (multi-gpu / single-gpu / apple / cpu), not the
		// raw rig string, so the TUI share path advertises the same honest, leak-free class
		// the CLI does.
		HW:          detectHWClass(),
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
		// Persist a newly verified / pasted local endpoint + any key it needed, so a
		// custom or key-protected upstream survives a restart (the TUI mirror of the save
		// in `roger share`; the host owns the config write).
		SaveUpstream: func(upstream, key string) {
			c := loadConfig()
			if c.Share == nil {
				c.Share = &Share{}
			}
			c.Share.Upstream = upstream
			c.Share.UpstreamKey = key
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
		// Seed the saved/verified upstream + its key so the TUI reuses a custom / keyed
		// endpoint on its first scan (matches bare `roger share`), instead of re-hunting.
		h.ShareUpstream, h.ShareUpstreamKey = cfg.Share.Upstream, cfg.Share.UpstreamKey
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

// toProtocolWindows converts the persisted config schedule windows (what the TUI
// editor saved into cfg.Prices) into the wire protocol.PriceWindow the agent
// publishes - so the headless `roger share` daemon advertises exactly the
// time-of-use schedule the in-TUI editor produced (P0-A parity).
func toProtocolWindows(ws []SchedWindow) []protocol.PriceWindow {
	if len(ws) == 0 {
		return nil
	}
	out := make([]protocol.PriceWindow, 0, len(ws))
	for _, w := range ws {
		out = append(out, protocol.PriceWindow{Start: w.Start, End: w.End, In: w.In, Out: w.Out, Free: w.Free})
	}
	return out
}

// runTUI / startWebConsoleFn are behaviour-preserving seams over run()'s two blocking,
// terminal/port-bound side effects on the no-args launch path: the interactive TUI
// program (default tui.RunWithController, which blocks until the user quits) and the
// browser-console http server (default startWebConsole, which binds a localhost port).
// Production wires the real implementations so the launch path is byte-for-byte
// unchanged; a test points them at stubs so run()'s no-args branch is reachable without
// a real TTY or a bound port.
var (
	runTUI            = tui.RunWithController
	startWebConsoleFn = startWebConsole
)

func main() {
	if err := run(os.Args[1:], loadConfig()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run is main()'s testable body: it takes the argv tail (os.Args[1:]) plus the loaded
// config, wires the startup update banner + the global browser-console flag strip, and
// either launches the no-args interactive app (via the runTUI / startWebConsoleFn seams)
// or dispatches a subcommand. It returns an error instead of calling os.Exit so a test
// can drive every branch; main() owns turning that error into a stderr line + exit 1.
func run(argv []string, cfg config) error {
	tui.SetVersion(Version) // help/about surfaces match `roger version`
	// Sweep a leftover binary from a prior Windows self-update (the locked .old that
	// couldn't be deleted while the old process was still running). No-op elsewhere.
	update.CleanupOld()
	// A subtle, cached (~daily), non-blocking update banner. Computed once here so
	// the TUI does no network at startup; the cache refreshes in the background.
	notice := update.CachedNotice(Version)
	// Global browser-console flags (--no-webui / --webui / --webui-port=N) are not
	// subcommands; strip them here so the dispatcher reads the real command, and resolve
	// whether the console comes up (ON by default; saved config or --no-webui opts out).
	rest, webuiOn, webuiPort := stripWebuiFlags(argv, cfg.webuiEnabled(), defaultWebuiPort)
	if len(rest) == 0 {
		// First run: a tiny guided wizard (consume vs share, free vs earn) before the
		// app. Non-interactive / already-onboarded runs skip it and launch straight in.
		cfg = maybeOnboard(cfg)
		// no args -> launch the interactive radio TUI with the in-TUI flow hooks, plus the
		// browser console (unless disabled) over the SAME shared node controller, so a
		// change in either front-end shows up in the other.
		hooks := tuiHooks(cfg)
		ctrl := tui.NewController(cfg.Broker, hooks)
		if webuiOn {
			startWebConsoleFn(cfg, ctrl, webuiPort)
		}
		return runTUI(cfg.Broker, cfg.User, tuiLimits(cfg), notice, hooks, ctrl)
	}
	// On plain CLI subcommands (not the TUI / the upgrade command itself), print the
	// banner to stderr so scripted stdout stays clean.
	if notice != "" {
		switch rest[0] {
		case "upgrade", "update", "self-update", "ping", "--ping", "-ping", "version":
		default:
			fmt.Fprintln(os.Stderr, notice)
		}
	}
	return dispatch(cfg, rest)
}

// detectFull / detectProbeKey are behaviour-preserving seams over the local-LLM
// detector (default detect.DetectFull / detect.ProbeKey). Production calls the real
// detector unchanged; a test points them at a fake so cmdShare's no-upstream detection
// path, finishShare's detect-success path, and cmdDrPhil's upstream check run to
// completion WITHOUT a live local model server on the box.
var (
	detectFull     = detect.DetectFull
	detectProbeKey = detect.ProbeKey
)

// errUnknownCommand is returned by dispatch for an unrecognized subcommand (main turns
// any dispatch error into a stderr line + exit 1). Split out of main() so the command
// routing is testable without os.Exit / os.Args mutation.
var errUnknownCommand = fmt.Errorf("unknown command")

// dispatch routes a parsed argv (args[0] is the subcommand, args[1:] its arguments) to
// the matching handler and returns its error. main() owns the process-exit; this owns
// only the routing, so a test can drive every verb.
func dispatch(cfg config, args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "search", "discover", "models":
		return client.Search(cfg.Broker)
	case "balance":
		return cmdBalance(cfg, args[1:])
	case "account", "identity":
		return cmdAccount(cfg, args[1:])
	case "login":
		return client.Login(cfg.Broker, gitHubClientID())
	case "logout":
		return client.Logout()
	case "whoami":
		return client.Whoami()
	case "topup":
		return cmdTopup(cfg, args[1:])
	case "use", "connect", "tune":
		return cmdUse(cfg, args[1:])
	case "share":
		return cmdShare(cfg, args[1:])
	case "limits":
		return cmdConfig(append([]string{"limits"}, args[1:]...))
	case "limit":
		return cmdLimit(cfg, args[1:])
	case "payout", "payouts", "cashout":
		return cmdPayout(cfg, args[1:])
	case "grant":
		return cmdGrant(cfg, args[1:])
	case "onboard", "setup":
		return cmdOnboard(cfg, args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "support", "community", "help-me", "discord":
		return cmdSupport()
	case "appeal":
		return cmdAppeal(cfg, args[1:])
	case "drphil", "dr-phil", "diagnose", "doctor":
		return cmdDrPhil(cfg, args[1:])
	case "ping":
		return tui.PingWalk() // the quick 2-lap walk easter egg
	case "--ping", "-ping":
		return tui.PingWorld(cfg.Broker) // the full-screen "Ping World" screensaver (live towers)
	case "upgrade", "update", "self-update":
		return cmdUpgrade(args[1:])
	case "version":
		fmt.Printf("rogerai %s\n", Version)
		return nil
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return errUnknownCommand
	}
}

// supportURL is the website (community + Discord link live in its footer). Per the
// founder, `roger support` / the TUI's /support point here, not straight at Discord,
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
		return fmt.Errorf("usage: roger use <model> [--max-out $] [--advanced]")
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

// shareModelArg pulls an optional LEADING positional model token out of `share`'s
// args, mirroring how `cmdUse` treats its first positional. If args[0] is a non-flag
// token (does not start with "-"), it is returned as the model and stripped from the
// remaining args the flag parser sees; otherwise model is "" and args pass through
// unchanged. This lets `roger share gpt-oss-120b` expose that exact model instead of
// silently dropping the positional and falling back to the saved/first-detected one.
// A bare "-"/"--" (or any flag) is left for the flag parser, never treated as a model.
func shareModelArg(args []string) (model string, rest []string) {
	if len(args) > 0 && args[0] != "" && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func cmdShare(cfg config, args []string) error {
	// Defaults inherit the saved onboarding share config (model + price) when set,
	// so `roger share` after the wizard Just Works with the choices already made.
	defModel, defIn, defOut := "", 0.0, 0.0
	if cfg.Share != nil {
		defModel, defIn, defOut = cfg.Share.Model, cfg.Share.PriceIn, cfg.Share.PriceOut
	}
	// A leading POSITIONAL model arg (e.g. `roger share gpt-oss-120b`) is honored the
	// same way `cmdUse` honors its first positional: if args[0] is a non-flag token it IS
	// the model to expose, OVERRIDING the saved-config --model default, and the remaining
	// args are what we hand to the flag parser. Without it, a bare `roger share` keeps
	// falling back to the saved/first-detected model, and an explicit `--model` still works
	// (and still wins when both are given, since flag parsing runs after this).
	posModel, rest := shareModelArg(args)
	defModelFlag := defModel
	if posModel != "" {
		defModelFlag = posModel
	}
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	broker := fs.String("broker", cfg.Broker, "broker URL")
	// --node sets the friendly STATION callsign (e.g. `brave-otter`). Empty default: use
	// the persisted station (auto-generated once on first share, never the hostname). A
	// given --node is REMEMBERED as the station so it sticks across restarts and the TUI.
	// The broker node id is then `<station>-<model-slug>` (no hostname, no port leak).
	node := fs.String("node", "", "station callsign (e.g. brave-otter); persisted. default: your saved/auto station")
	model := fs.String("model", defModelFlag, "model to expose (default: first detected)")
	upstream := fs.String("upstream", "", "local OpenAI endpoint (default: auto-detect)")
	upKey := fs.String("upstream-key", "", "bearer key for the upstream (optional; auto-detected from env / saved)")
	region := fs.String("region", "home", "region")
	parallel := fs.Int("parallel", 4, "concurrent poll workers (per-node concurrency)")
	// FREE BY DEFAULT (price 0/0): a bare `roger share` goes on air with NO login
	// (a priced node would require `roger login` and otherwise 403). Set a price to
	// EARN (that does require login). See the onboarding wizard's earn branch.
	priceIn := fs.Float64("price-in", defIn, "$/1M input tokens to EARN (default 0 = free, no login needed)")
	priceOut := fs.Float64("price-out", defOut, "$/1M output tokens to EARN (default 0 = free, no login needed)")
	ctx := fs.Int("ctx", 0, "context length (default: auto-detect from the upstream)")
	confidential := fs.Bool("confidential", false, "GATED enterprise tier: advertise as confidential - needs data-center silicon (AMD EPYC SEV-SNP + an H100-class confidential GPU), not consumer hardware. Apply at "+confidentialApplyURL+" (see docs/tee-eligibility.md)")
	private := fs.Bool("private", false, "share on a PRIVATE band: hidden from the public market, reachable only by a secret frequency code (shown once). Requires `roger login`.")
	freeWindow := fs.String("free-window", "", "daily FREE window in UTC, e.g. 03:00-03:30")
	schedule := fs.String("schedule", "", `time-of-use schedule, JSON e.g. '[{"start":"18:00","end":"22:00","price_in":0.5,"price_out":0.7}]'`)
	advanced := fs.Bool("advanced", false, "show advanced flags (--node --region --parallel --upstream --ctx --confidential --free-window --schedule)")
	fs.Usage = func() {
		fmt.Print(`roger share - go on air as a provider (auto-detects your local model)

  roger share                       go on air FREE - no login needed
  roger share <model>               expose a specific model
  roger share --price-out 0.30      EARN: set a price (needs ` + "`roger login`" + `)
  roger login                       link GitHub - only needed to EARN

  --model <name>      model to expose (default: first detected)
  --price-out <P>     $/1M output tokens to earn (default 0 = free, no login)
  --private           hidden band, frequency-code only (needs ` + "`roger login`" + `)
  --advanced          reveal: --node --region --parallel --upstream --ctx --confidential --free-window --schedule

Earning needs a GitHub-linked owner: run ` + "`roger login`" + ` first. Free sharing
needs no login. When you earn, payouts are 120-day hold, $25 min, monthly.
`)
	}
	fs.Parse(rest)
	if *advanced {
		fmt.Println("advanced flags: --node --region --parallel --upstream --upstream-key --ctx --confidential --free-window --schedule")
	}
	// EARN login-gate, UP FRONT (mirrors the --private pre-check below): a priced share
	// 401s at the broker if the owner is not GitHub-linked. Fail FAST here - before any
	// detection / upstream probe / register - so a would-be earner is not led all the way
	// to a late 403. Catches the flag (--price-*) and the wizard's saved earn price
	// (cfg.Share.Price*, the defaults above). Free sharing (price 0/0) needs no login.
	if (*priceIn > 0 || *priceOut > 0) && client.LinkedLogin() == "" {
		return fmt.Errorf("earning needs a GitHub-linked owner - run `roger login` to earn (free sharing needs no login)")
	}
	// Pre-disclose the payout policy ONCE, at the point a price is set, so the 120-day
	// hold / $25 min / monthly cadence is never a surprise at cash-out time (F3).
	if *priceIn > 0 || *priceOut > 0 {
		fmt.Println("earning: payouts are 120-day hold, $25 min, monthly (`roger payout status` for details).")
	}
	// Record which pricing/schedule flags the user EXPLICITLY passed. The single source
	// of truth for a station's per-model price is cfg.Prices (what the TUI editor saves):
	// when the user gives none of these flags we seed price-in/out + schedule from it
	// below, so "set it in the TUI, it applies when you `share` headless" actually holds.
	// An explicit flag is always honored as an override (never clobbered by the saved
	// profile). fs.Visit only reports flags that were set on the command line.
	var setIn, setOut, setFreeWin, setSched bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "price-in":
			setIn = true
		case "price-out":
			setOut = true
		case "free-window":
			setFreeWin = true
		case "schedule":
			setSched = true
		}
	})

	up := *upstream
	mdl := *model
	var foundModality string // detected modality of the shared model (tts/stt); "" = chat
	ctxLen := *ctx
	// ctxEstimated tracks whether ctxLen is the real detected window or the last-resort
	// default. A user-pinned --ctx (ctxLen>0 here) is authoritative, never estimated.
	ctxEstimated := false
	// A saved/verified upstream (from the guided fallback) is the (e) source: probe
	// it first so a non-default / custom-port server is remembered, not re-hunted.
	savedUp, savedKey := "", ""
	if cfg.Share != nil {
		savedUp, savedKey = cfg.Share.Upstream, cfg.Share.UpstreamKey
	}
	// A saved upstream key belongs to the SAVED endpoint: reuse it on a bare re-share
	// (or an explicit --upstream pointing at that same endpoint), but NEVER default it
	// onto a DIFFERENT --upstream - that would send a stale bearer to another server.
	if *upKey == "" && savedKey != "" && (up == "" || sameEndpoint(up, savedUp)) {
		*upKey = savedKey
	}
	if up == "" {
		// Saved keyed upstream: try it WITH its key first (the broad DetectFull scan does
		// not carry the saved key), so a custom keyed endpoint is reused without a re-prompt.
		var found []detect.Found
		var needKey []string
		if savedUp != "" && *upKey != "" {
			if f, st := detectProbeKey(savedUp, *upKey); st == detect.Reachable {
				found = []detect.Found{f}
			}
		}
		if len(found) == 0 {
			found, needKey = detectFull(savedUp)
		}
		if len(found) == 0 {
			// GUIDED FALLBACK: nothing usable. Walk the user through it instead of
			// erroring out - pick your tool for a one-liner, paste an endpoint we verify,
			// or (when a server is there but key-protected) paste its API key. A
			// non-interactive run still gets the clear "start one or --upstream".
			picked, ok := guidedUpstream(cfg.Broker, needKey)
			if !ok {
				if len(needKey) > 0 {
					return fmt.Errorf("found a local server at %s but it needs an API key - pass --upstream-key <key> (or set OPENAI_API_KEY)", needKey[0])
				}
				return fmt.Errorf("no local LLM detected (tried Ollama/LM Studio/llama.cpp/vLLM/Jan/LiteLLM and your open ports). Start one, then `roger share`, or pass --upstream <url>")
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
		// A key-protected upstream the detector authenticated to (from env or the guided
		// paste) carries its working key on the Found; adopt it unless --upstream-key was
		// given explicitly, so the agent forwards jobs with the same Bearer.
		if *upKey == "" && pick.Key != "" {
			*upKey = pick.Key
		}
		if mdl == "" && len(pick.Models) > 0 {
			mdl = pick.Models[0]
		}
		foundModality = pick.Modality[mdl] // tts/stt/chat for the model we're about to share
		// Auto-detect --ctx from the upstream when the user didn't pin it: detect.ResolveCtx
		// prefers the REAL per-model window (Ollama /api/show + /api/ps, llama.cpp /props,
		// LM Studio /api/v0/models, then /v1/models) and only falls back to the estimated
		// default - flagging that so the offer is honest about a guess.
		if ctxLen == 0 {
			ctxLen, ctxEstimated = detect.ResolveCtx(pick.Ctx, mdl)
		}
		// Remember the verified upstream (and any key it needed) so a custom-port /
		// guided-fallback / key-protected endpoint is not re-hunted or re-prompted next
		// launch (the (e) saved-config source).
		if (pick.BaseURL != "" && pick.BaseURL != savedUp) || (*upKey != "" && (cfg.Share == nil || *upKey != cfg.Share.UpstreamKey)) {
			c := loadConfig()
			if c.Share == nil {
				c.Share = &Share{}
			}
			if pick.BaseURL != "" {
				c.Share.Upstream = pick.BaseURL
			}
			c.Share.UpstreamKey = *upKey
			_ = saveConfig(c)
		}
	} else if *upKey == "" {
		// Explicit --upstream with no key resolved: best-effort harvest a working key
		// from the environment (OPENAI_API_KEY / friends) for THIS endpoint and confirm
		// reachability - but NEVER block here, the agent self-heals if the server is
		// momentarily down (the same reason the explicit path skips a hard preflight).
		if f, st := detectProbeKey(up, ""); st == detect.Reachable && f.Key != "" {
			*upKey = f.Key
		}
	}
	if mdl == "" {
		return fmt.Errorf("could not determine a model; pass --model")
	}
	// ctx fallback: auto-detect (above) or the safe default when the upstream did
	// not report a context length and the user didn't pass --ctx. The --upstream branch
	// skips the auto-detect block, so resolve here too; an explicit --ctx stays real.
	if ctxLen <= 0 {
		ctxLen, ctxEstimated = detect.ResolveCtx(nil, mdl)
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

	// Build the flag-derived schedule first (an explicit --free-window / --schedule is
	// a deliberate one-off that fully owns the schedule for this run).
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
	// P0-A parity: seed price + schedule from the TUI editor's saved per-model profile
	// (cfg.Prices) when the user passed no explicit flags, so the headless daemon serves
	// exactly what the editor produced. Explicit flags always win.
	*priceIn, *priceOut, sched = seedSharePricing(cfg, mdl, *priceIn, *priceOut, sched, sharePricingFlags{setIn, setOut, setFreeWin, setSched})
	if *confidential {
		// Preflight FIRST (cheap, local, no broker round-trip): if this host is not an AMD
		// SEV-SNP confidential VM there is no /dev/sev-guest and we cannot produce a real
		// quote, so abort here with an actionable message rather than sending a fake claim
		// or failing deep in registration. This is the "wrong hardware" case; the distinct
		// "right hardware, unblessed image" case is surfaced AFTER register (the broker owns
		// the measurement allowlist) via the confidential-grant echo below.
		if err := agent.ConfidentialPreflight(); err != nil {
			fmt.Println(confidentialIneligibleMsg())
			return err
		}
		fmt.Println("confidential: SEV-SNP device present - generating a real attestation quote at registration; the broker verifies it (signature chain + nonce binding + allowlisted launch measurement) before granting the ◆ badge.")
	}

	if *private {
		// A private band requires login (the broker 401s an anonymous private register).
		// Fail clearly here rather than after a detection/upstream probe.
		if client.LinkedLogin() == "" {
			return fmt.Errorf("`--private` needs a GitHub-linked owner - run `roger login` first (anonymous private sharing is not allowed)")
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
		// HW carries the PRIVACY-BUCKETED class (multi-gpu / single-gpu / apple / cpu),
		// NOT the raw CPU/GPU string - so a consumer learns the band's tier without the
		// node leaking its exact rig.
		NodeID: nodeID, Region: *region, HW: detectHWClass(), Model: mdl, Modality: foundModality,
		PriceIn: *priceIn, PriceOut: *priceOut, Ctx: ctxLen, CtxEstimated: ctxEstimated, Parallel: *parallel,
		Confidential: *confidential, Private: *private, Schedule: sched,
	}
	// Single-instance guard: detect (via a per-node-id lockfile) a `roger share`
	// already on air for THIS node id and bow out, rather than double-registering it
	// and breaking routing/earnings. A stale lock from a crashed daemon is reclaimed.
	releaseLock, err := acquireOnAirLock(nodeID, station, mdl)
	if err != nil {
		return err
	}
	defer releaseLock()
	if !*private {
		// Start (not Run) so we can confirm the broker actually ACCEPTED us (the heartbeat
		// ACK) and print a SINGLE truthful "on air" line, instead of several sequential
		// Printlns around a blind go-live. Then block forever.
		sess, err := agentStart(cfgRun)
		if err != nil {
			return err
		}
		// Wait briefly for the broker to ACK our first heartbeat (LinkOnAir) so the single
		// success line is TRUTHFUL - we are genuinely routable, not blindly "on air". If the
		// ACK does not land in a couple seconds we still print it (the agent keeps
		// self-healing in the background and the line points the operator at the website).
		waitOnAir(sess, 3*time.Second)
		// Show the broker-EFFECTIVE price (after any owner web-console override) so an
		// owner who priced this node on the web sees the published number, not the local
		// one. One source of truth: the price the broker actually publishes.
		effIn, effOut, override := sess.EffectivePrice()
		fmt.Println(onAirLine(mdl, station, effIn, effOut, override))
		if line := confidentialFeedback(sess.RequestedConfidential(), sess.Confidential()); line != "" {
			fmt.Println(line)
		}
		fmt.Println(earningsLine())
		shareBlock() // serve forever (a test seam makes this return)
		return nil
	}
	// Private: start (not Run) so we can surface the one-time frequency code, then block.
	sess, err := agentStart(cfgRun)
	if err != nil {
		return err
	}
	if line := confidentialFeedback(sess.RequestedConfidential(), sess.Confidential()); line != "" {
		fmt.Println(line)
	}
	if _, code, _ := sess.Band(); code != "" {
		// One-time reveal: show the FULL code (with the secret tail). It is shown ONCE and
		// never retrievable again - the persisted display is masked (lost => revoke + re-mint).
		fmt.Printf("\n  %s YOUR FREQUENCY CODE (shown once - copy it now)\n", "◉")
		fmt.Printf("\n      %s\n\n", code)
		fmt.Println("  share this with whoever should reach your station. They tune in with:")
		fmt.Printf("      roger use %s --freq %q\n", mdl, code)
		fmt.Println("  the cosmetic \"MHz\" part is optional - the code after it is what matters.")
	} else if _, _, display := sess.Band(); display != "" {
		fmt.Printf("\n  on air on your existing private band: %s (code shown only at first creation)\n", display)
	}
	shareBlock() // serve forever (a test seam makes this return)
	return nil
}

// agentStart / shareBlock are seams over cmdShare's two un-testable side effects: the
// real node register+serve (default agent.Start) and the forever-block after go-live
// (default select{}). Tests point agentStart at a stub session and shareBlock at a no-op
// so cmdShare's setup + go-live path runs to completion without registering or blocking.
var (
	agentStart = agent.Start
	shareBlock = func() { select {} }
)

// onAirLine is the SINGLE go-live success line for a public share (audit #5): the one
// thing a new provider needs to see - what's live, under which station, and where to
// view it - instead of several sequential status Printlns. A price/free suffix tells
// the operator at a glance whether they are earning.
func onAirLine(model, station string, priceIn, priceOut float64, override bool) string {
	mode := "free"
	if priceIn > 0 || priceOut > 0 {
		mode = fmt.Sprintf("earning $%s/$%s per 1M", trimAmt(priceIn), trimAmt(priceOut))
	}
	// Note when the published price is a broker-side owner override (set on the web
	// Console), so the on-air number never looks "wrong" versus what was requested.
	if override {
		mode += " (broker override active)"
	}
	return fmt.Sprintf("on air - %s · %s · %s · view at rogerai.fyi", model, station, mode)
}

// earningsLine is the provider's money-OUT pointer printed right under the go-live
// line: where to watch earnings accrue and check a payout. Without it a fresh provider
// is on air with no idea where their money shows up. One tasteful line, mirroring the
// single on-air line above.
func earningsLine() string {
	return "earnings: rogerai.fyi/dashboard.html  (or: roger payout status)"
}

// confidentialApplyURL is where an operator with qualifying data-center silicon applies to
// the gated confidential ◆ tier. The tier is NOT self-serve (it needs hardware almost
// nobody running a home GPU has - see confidentialIneligibleMsg), so the CLI points here
// rather than implying anyone can flip it on.
const confidentialApplyURL = "https://rogerai.fyi/confidential"

// confidentialIneligibleMsg is the guidance printed when `roger share --confidential` runs
// on a host with no SEV-SNP device. It is honest about WHY this is not consumer hardware
// (CPU TEE + a confidential GPU, both data-center only) and routes the operator to the
// standard tier (which still earns, with co-signed lineage receipts) or the apply page.
func confidentialIneligibleMsg() string {
	return "confidential ◆ is a gated, data-center-only tier - it needs an AMD EPYC (Milan+) host\n" +
		"with SEV-SNP AND an H100-class confidential GPU, so it does not run on consumer CPUs/GPUs\n" +
		"(Threadripper, Ryzen, and gaming GPUs do not qualify). Two honest options:\n" +
		"  • just run `roger share` (standard) - you serve + earn the same way, and every request\n" +
		"    carries a co-signed lineage receipt (verifiable, attributable serving).\n" +
		"  • if you DO have qualifying hardware, apply: " + confidentialApplyURL + "\n" +
		"  (background: docs/tee-eligibility.md)"
}

// confidentialFeedback returns the one-line confidential-tier outcome for a go-live, or
// "" when this session did not ask for the confidential tier. It closes the silent-
// downgrade gap: the broker echoes whether the ◆ badge was GRANTED, so a node that
// CLAIMED confidential but landed as standard (fail-soft, e.g. an unblessed launch
// measurement or a transient attestation failure) is told plainly - rather than wrongly
// implying it is confidential. A granted node gets the verified line. Pure (booleans in)
// so the three outcomes are unit-testable without constructing a live agent.Session.
func confidentialFeedback(requested, granted bool) string {
	if !requested {
		return ""
	}
	if granted {
		return "confidential: ◆ VERIFIED by the broker (real TEE attestation passed) - this band serves confidential traffic."
	}
	return "confidential: NOT granted - running STANDARD this session. The broker did not verify the attestation " +
		"(most often: your launch measurement is not on the broker's allowlist, i.e. an unblessed image). " +
		"You are still serving + earning as a standard node; see docs/tee-eligibility.md or apply at " + confidentialApplyURL + "."
}

// waitOnAir blocks until the session's link reaches LinkOnAir (the broker has ACKed a
// heartbeat) or the timeout elapses, so the on-air line is keyed to a real ACK rather
// than a blind go-live. Returns whether we observed the ACK in time.
func waitOnAir(sess *agent.Session, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sess.Link() == agent.LinkOnAir {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return sess.Link() == agent.LinkOnAir
}

// sharePricingFlags records which pricing/schedule flags the user EXPLICITLY passed
// to `share` (so a deliberate one-off override is never clobbered by the saved
// profile).
type sharePricingFlags struct {
	in, out, freeWindow, schedule bool
}

// seedSharePricing applies the TUI editor's saved per-model price + schedule
// (cfg.Prices[model], the single source of truth both surfaces read) on top of the
// flag-derived values for `roger share`. It is the P0-A parity fix: "set it in the
// TUI, it applies when you `share` headless".
//
//   - price-in/out: seeded from the saved profile ONLY when the user did not pass that
//     explicit flag (an explicit --price-in/--price-out fully overrides).
//   - schedule: the saved time-of-use windows are APPENDED to the flag-derived schedule
//     ONLY when the user passed NEITHER --free-window nor --schedule (an explicit
//     schedule flag is a deliberate one-off that owns the schedule for this run).
//
// A model with no saved profile returns the inputs unchanged (free stays free).
func seedSharePricing(cfg config, model string, priceIn, priceOut float64, sched []protocol.PriceWindow, set sharePricingFlags) (float64, float64, []protocol.PriceWindow) {
	saved, ok := cfg.Prices[model]
	if !ok {
		return priceIn, priceOut, sched
	}
	if !set.in {
		priceIn = saved.PriceIn
	}
	if !set.out {
		priceOut = saved.PriceOut
	}
	if !set.freeWindow && !set.schedule {
		sched = append(sched, toProtocolWindows(saved.Windows)...)
	}
	return priceIn, priceOut, sched
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
		fmt.Printf(`roger upgrade - self-update to the latest release (alias: update)

  roger upgrade           download + verify + atomically replace this binary
  roger upgrade --check   only report whether a newer version is available

Downloads the per-os/arch asset from github.com/%s, verifies its SHA256 against
the published checksums, then atomically swaps the running binary. "Already on
the latest version" is handled. Needs write permission on the install directory.

The background check (shown subtly at startup) can be disabled with
ROGERAI_NO_UPDATE_CHECK=1.
`, update.Repo)
	}
	fs.Parse(args)
	if *check {
		res, err := updateCheck(Version)
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
	return updateUpgrade(Version, os.Stdout)
}

// updateCheck / updateUpgrade are behaviour-preserving seams over the self-update
// network boundary (default update.Check / update.Upgrade, both of which hit GitHub).
// Production wires the real functions so `roger upgrade` is byte-for-byte unchanged; a
// test points them at fakes so cmdUpgrade's branches are reachable without a real
// release download / network call.
var (
	updateCheck   = update.Check
	updateUpgrade = update.Upgrade
)

func cmdTopup(cfg config, args []string) error {
	usd := 10.0
	if len(args) > 0 {
		if f, err := strconv.ParseFloat(args[0], 64); err == nil {
			usd = f
		}
	}
	return client.Topup(cfg.Broker, cfg.User, usd, tui.OpenURL)
}

// cmdPayout is the provider money-OUT verb group: cash out earnings from the
// terminal. Every call is Ed25519-signed (the same identity the rest of the client
// uses), so a headless `roger share` provider can withdraw + see KYC status without
// a browser session. Requires a GitHub-linked account (run `roger login`); the
// broker enforces the unchanged policy (120-day hold, $25 min, monthly, Connect-KYC).
// Amounts are shown in dollars (1 credit == $1).
//
//	roger payout            -> status (default)
//	roger payout status     -> KYC state + payable/held + next-payable date + policy
//	roger payout onboard    -> open the Stripe Connect KYC link (prints it too)
//	roger payout request    -> request a payout (broker pays the full payable amount)
//	roger payout history    -> past payouts + their states
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
	// no signing identity bound to an account, so point at `roger login` up front.
	if client.LinkedLogin() == "" {
		fmt.Println("not logged in - run `roger login` to link GitHub (required to earn + cash out)")
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
	fmt.Println(`roger payout - cash out your provider earnings (dollars; 1 credit = $1)

  roger payout status     Connect/KYC state + payable vs held + next-payable date
  roger payout onboard     complete Stripe Connect KYC (opens the browser)
  roger payout request      request a payout of your payable balance
  roger payout history      past payouts and their states

  Policy: 120-day hold, $25 minimum, monthly. Requires GitHub login + Connect KYC.`)
}

// payoutPolicyLine is the single one-liner describing the unchanged policy, reused by
// status so the user always sees the terms.
func payoutPolicyLine(st client.PayoutStatus) string {
	hold := st.HoldDays
	if hold == 0 {
		hold = 120
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
		fmt.Println("\n  complete KYC to cash out:  roger payout onboard")
	case payable < minOr25(st):
		fmt.Printf("\n  below the $%s minimum - keep earning, then `roger payout request`.\n", trimAmt(minOr25(st)))
	default:
		fmt.Println("\n  ready to cash out:  roger payout request")
	}
	return nil
}

func holdOr90(st client.PayoutStatus) int {
	if st.HoldDays == 0 {
		return 120
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
		fmt.Println("KYC not complete - run `roger payout onboard` first.")
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
		fmt.Println("no payouts yet - run `roger payout status` to see what's payable.")
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

// cmdBalance is the money-IN verb (C7 - ONE money grammar): bare `roger balance`
// shows credits; `roger topup <amt>` adds funds. The older `balance --topup` and
// `balance topup` spellings still WORK as hidden aliases (so nothing breaks) but are
// out of help - one documented form, `topup`.
func cmdBalance(cfg config, args []string) error {
	// Hidden aliases, parsed by hand so they do NOT appear in `balance -h`:
	//   roger balance topup [usd]   /   roger balance --topup[=usd]
	if usd, ok := balanceTopupAlias(args); ok {
		return client.Topup(cfg.Broker, cfg.User, usd, tui.OpenURL)
	}
	return client.Balance(cfg.Broker, cfg.User)
}

// balanceTopupAlias recognizes the retired-but-still-working topup spellings under
// `balance` (C7 hidden aliases): `balance topup [usd]`, `balance --topup`, and
// `balance --topup <usd>` / `--topup=<usd>`. Returns the dollar amount (defaulting to
// $10) and true when one matched, else (_, false). The documented form is the
// top-level `roger topup <amt>`.
func balanceTopupAlias(args []string) (float64, bool) {
	usd := 10.0
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "topup" || a == "--topup" || a == "-topup":
			if i+1 < len(args) {
				if f, e := strconv.ParseFloat(strings.TrimPrefix(args[i+1], "$"), 64); e == nil && f > 0 {
					usd = f
				}
			}
			return usd, true
		case strings.HasPrefix(a, "--topup=") || strings.HasPrefix(a, "-topup="):
			v := a[strings.IndexByte(a, '=')+1:]
			if f, e := strconv.ParseFloat(strings.TrimPrefix(v, "$"), 64); e == nil && f > 0 {
				usd = f
			}
			return usd, true
		}
	}
	return 0, false
}

// cmdLimit is the per-account MONTHLY SPEND CAP verb (a budget limit, modeled on
// Groq's "set a max you'll pay per month"). `roger limit --monthly $X` sets the
// cap; `--monthly 0` or `--monthly off` clears it (unlimited); bare `roger limit`
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
			fmt.Println("set one with `roger limit --monthly $X`")
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
		return fmt.Errorf("usage: roger account [login|logout]")
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
			return fmt.Errorf("usage: roger config clear-limit <model>")
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
			return fmt.Errorf("usage: roger config set <broker|user> <value>")
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

// cmdSetLimit handles `roger config set-limit <model> [--max-in P] [--max-out P]
// [--min-tps N]`. Use "default" as the model to set the fallback limit. Only the
// flags passed are changed (the rest of that model's limit is preserved).
func cmdSetLimit(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: roger config set-limit <model|default> [--max-in P] [--max-out P] [--min-tps N]")
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
		fmt.Println("  (none set - no caps; `roger config set-limit <model> --max-out P`)")
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

// sameEndpoint reports whether two upstream URLs point at the same server, comparing
// their normalized chat-completions form so a base / /v1 / full-chat spelling of the
// SAME endpoint matches. Used to decide when a saved upstream key may be reused (only
// for its own endpoint - never sprayed onto a different --upstream).
func sameEndpoint(a, b string) bool {
	return b != "" && normalizeUpstream(a) == normalizeUpstream(b)
}

// normalizeUpstream turns a user-supplied --upstream into the OpenAI-compatible
// chat-completions URL the agent POSTs to. It accepts a base URL
// (http://host:port), a /v1 URL, or the already-full /v1/chat/completions URL,
// so the natural inputs all work and match what detect.DetectFull produces.
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

  roger                         open the app (browse, tune in, chat) + browser console
  roger --no-webui              open the app WITHOUT the browser console
  roger --ping                  full-screen "Ping World" screensaver (or press z in the app)
  roger search                list models, cheapest first
  roger use <model>           local OpenAI endpoint for your bots  (alias: connect · --max-out $ caps spend)
  roger balance               your wallet balance
  roger topup <amt>           add funds to your wallet
  roger limit --monthly $X    cap your spend per calendar month  (0/off = no cap)

providers (share your GPU):
  roger share <model>         go on air - FREE by default, no login (auto-detects your model)
  roger login                 link GitHub - only needed to EARN
  roger payout                cash out your earnings (status · onboard · request · history)
  roger grant create --name my-bots   a free private key for your bots/family
  roger drphil                diagnose why your node isn't earning (auto-fixes config)
  roger appeal --reason "..." contest a strike/ban (self-serve; "appeal status" to track)

more: account · config · support · upgrade

advanced flags live behind --advanced (e.g. roger use <model> --advanced,
roger share --advanced, roger grant create --advanced).

env: ROGER_BROKER, ROGER_USER override config (%s)
`, configPath())
}
