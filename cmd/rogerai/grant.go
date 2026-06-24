package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
)

// defaultGrantDailyCap is the conservative non-zero daily token cap a fresh grant
// gets by default (GRANT-KEYS-DESIGN section 4.1: a forgotten/leaked grant should
// be self-limiting). Override with --daily-cap (advanced) or 0 to disable.
const defaultGrantDailyCap = 2_000_000

// cmdGrant is the owner-facing grant-keys verb group: create | list | revoke |
// show. `create` leads with --name + --free|--price-out; everything else is
// behind --advanced (section 6 / CLI-SIMPLICITY-AUDIT C6).
func cmdGrant(cfg config, args []string) error {
	if len(args) == 0 {
		grantUsage()
		return nil
	}
	switch args[0] {
	case "create", "new":
		return cmdGrantCreate(cfg, args[1:])
	case "list", "ls":
		return client.GrantList(cfg.Broker)
	case "revoke", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: rogerai grant revoke <name>")
		}
		return client.GrantRevoke(cfg.Broker, args[1])
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: rogerai grant show <name>")
		}
		return client.GrantShow(cfg.Broker, args[1])
	case "-h", "--help", "help":
		grantUsage()
		return nil
	default:
		return fmt.Errorf("unknown grant command %q (try create|list|revoke|show)", args[0])
	}
}

func cmdGrantCreate(cfg config, args []string) error {
	fs := flag.NewFlagSet("grant create", flag.ExitOnError)
	// The lean, in-everyone's-face surface: name + free-vs-priced.
	name := fs.String("name", "", "label shown on your dashboard (required), e.g. my-bots")
	free := fs.Bool("free", false, "free key - costs nobody (the default)")
	priceOut := fs.Float64("price-out", 0, "charge $/1M output tokens (makes it a priced/sponsored grant)")
	// Advanced (hidden unless --advanced): the full power, defaulted sanely.
	advanced := fs.Bool("advanced", false, "show the advanced flags (models, nodes, rpm, caps, expiry, self, price-in)")
	models := fs.String("models", "", "restrict to these models (comma-separated; default: any)")
	nodes := fs.String("nodes", "", "restrict to these of YOUR nodes (comma-separated; default: all)")
	rpm := fs.Float64("rpm", 0, "sustained requests/min (0 = broker default)")
	dailyCap := fs.Int64("daily-cap", defaultGrantDailyCap, "max tokens/UTC-day (0 = unlimited)")
	monthlyCap := fs.Int64("monthly-cap", 0, "max tokens/UTC-month (0 = unlimited)")
	expires := fs.String("expires", "", "lifetime, e.g. 30d or 2026-12-31 (default: never)")
	self := fs.Bool("self", false, "a self key for YOUR own headless boxes/agents ($0)")
	priceIn := fs.Float64("price-in", 0, "charge $/1M input tokens (advanced)")
	fs.Usage = func() {
		fmt.Print(`rogerai grant create - mint a private access key

  rogerai grant create --name my-bots               a FREE key for your bots/family
  rogerai grant create --name jane --price-out 0.30 a priced key you sponsor
  rogerai grant create --self --name hermes-box     a $0 key for your own remote box

  --name <label>     (required) shown on your dashboard
  --free             free key, costs nobody (default)
  --price-out <P>    charge $/1M output (makes it a sponsored grant)
  --advanced         reveal: --models --nodes --rpm --daily-cap --monthly-cap --expires --self --price-in

The secret is printed ONCE. A conservative daily token cap is set by default so a
forgotten key is self-limiting; override with --daily-cap (or 0 to disable).
`)
	}
	fs.Parse(args)
	if strings.TrimSpace(*name) == "" {
		fs.Usage()
		return fmt.Errorf("--name is required")
	}
	if *advanced {
		// --advanced is a help affordance: re-print so the user sees the full set.
		fmt.Println("advanced flags: --models --nodes --rpm --daily-cap --monthly-cap --expires --self --price-in")
	}
	var expiresAt int64
	if *expires != "" {
		t, err := parseExpires(*expires)
		if err != nil {
			return err
		}
		expiresAt = t
	}
	// --free was explicitly passed iff it appears in args (so a price can flip the
	// default to priced, but an explicit --free always wins).
	freeSet := flagPassed(fs, "free")
	return client.GrantCreate(cfg.Broker, client.GrantCreateOpts{
		Name: *name, Free: *free, FreeSet: freeSet,
		PriceIn: *priceIn, PriceOut: *priceOut,
		Models: splitCSV(*models), Nodes: splitCSV(*nodes),
		RPM: *rpm, DailyCap: *dailyCap, MonthlyCap: *monthlyCap,
		ExpiresAt: expiresAt, Self: *self,
	})
}

// parseExpires accepts a Go duration (30d / 720h) or an absolute date
// (2006-01-02) and returns the unix expiry.
func parseExpires(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Unix(), nil
	}
	// support a "d" (days) suffix on top of Go's duration units
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Now().Add(time.Duration(days) * 24 * time.Hour).Unix(), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d).Unix(), nil
	}
	return 0, fmt.Errorf("bad --expires %q, want e.g. 30d or 2026-12-31", s)
}

// flagPassed reports whether a flag was explicitly set on the command line.
func flagPassed(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// splitCSV splits a comma list into a trimmed, non-empty slice (nil for empty).
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func grantUsage() {
	fmt.Print(`rogerai grant - private access keys for your bots, family, and friends

  rogerai grant create --name my-bots     a free key (they use your models, no login)
  rogerai grant list                      your keys + usage
  rogerai grant show <name>               one key's scope, caps, usage
  rogerai grant revoke <name>             kill a key (effective next request)

  rogerai grant create --self --name hermes-box   a $0 key for your own remote box
  rogerai grant create --help                      the full create surface
`)
}
