package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"rogerai.fm/roger/v6/internal/client"
)

// keysSecretWarning follows a freshly minted secret, the only time it is ever shown.
const keysSecretWarning = "store it now - it is not shown again"

// cmdKeys is the account-keys verb group: list | mint | set | rm, over the same signed
// /account/keys endpoints the web account page uses (features/auth/key_guardrails.feature).
func cmdKeys(cfg config, args []string) error {
	return runKeys(cfg.Broker, args, os.Stdout, os.Stderr, isatty.IsTerminal(os.Stdout.Fd()))
}

func runKeys(broker string, args []string, stdout, stderr io.Writer, tty bool) error {
	if len(args) == 0 {
		keysUsage(stdout)
		return nil
	}
	switch args[0] {
	case "list", "ls":
		keys, err := client.KeysList(broker)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Fprintln(stdout, "no keys yet - `roger keys mint --name ci` mints one.")
			return nil
		}
		client.WriteKeysTable(stdout, keys)
		return nil
	case "mint", "new":
		fields, err := keysFields("mint", args[1:], false)
		if err != nil {
			return err
		}
		k, secret, err := client.KeysMint(broker, fields)
		if err != nil {
			return err
		}
		if !tty {
			// A pipe gets the secret alone (machine-readable); the warning goes to the person.
			fmt.Fprintln(stdout, secret)
			fmt.Fprintf(stderr, "minted %s - %s\n", k.ID, keysSecretWarning)
			return nil
		}
		fmt.Fprintf(stdout, "minted %s\n\n  %s\n\n%s\n", k.ID, secret, keysSecretWarning)
		return nil
	case "set":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("usage: roger keys set <key-id> [--name N] [--limit $] [--reset R] [--models a,b] [--nodes a,b] [--expires T] [--disable|--enable]")
		}
		fields, err := keysFields("set", args[2:], true)
		if err != nil {
			return err
		}
		if len(fields) == 0 {
			return fmt.Errorf("nothing to change - pass at least one flag (see `roger keys`)")
		}
		k, err := client.KeysSet(broker, args[1], fields)
		if err != nil {
			return err
		}
		client.WriteKeysTable(stdout, []client.AccountKey{k})
		return nil
	case "rm", "revoke", "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: roger keys rm <key-id>")
		}
		if err := client.KeysRevoke(broker, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "revoked %s\n", args[1])
		return nil
	case "-h", "--help", "help":
		keysUsage(stdout)
		return nil
	}
	return fmt.Errorf("unknown keys command %q (try list|mint|set|rm)", args[0])
}

// keysFields parses the key flags into the JSON fields the broker takes, naming only the
// flags actually passed (a PATCH changes nothing it does not name).
func keysFields(verb string, args []string, patch bool) (map[string]any, error) {
	fs := flag.NewFlagSet("keys "+verb, flag.ExitOnError)
	name := fs.String("name", "", "label shown in the key list")
	limit := fs.Float64("limit", 0, "spend limit in $ per window (0 = unlimited)")
	reset := fs.String("reset", "", "limit window: daily, weekly, monthly or none")
	models := fs.String("models", "", "allow only these models (comma-separated; empty = any)")
	nodes := fs.String("nodes", "", "allow only these stations (comma-separated; empty = any)")
	expires := fs.String("expires", "", "expiry: 30d, 12h, 2026-12-31 or never")
	disable := fs.Bool("disable", false, "turn the key off (set only)")
	enable := fs.Bool("enable", false, "turn the key back on (set only)")
	_ = fs.Parse(args)
	out := map[string]any{}
	var bad error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			out["name"] = *name
		case "limit":
			out["limit_usd"] = *limit
		case "reset":
			out["reset"] = *reset
		case "models":
			out["allowed_models"] = nonNilCSV(*models)
		case "nodes":
			out["allowed_nodes"] = nonNilCSV(*nodes)
		case "expires":
			if strings.EqualFold(strings.TrimSpace(*expires), "never") {
				out["expires_at"] = nil
				return
			}
			t, err := parseExpires(*expires)
			if err != nil {
				bad = err
				return
			}
			out["expires_at"] = time.Unix(t, 0).UTC().Format(time.RFC3339)
		case "disable", "enable":
			if !patch {
				bad = fmt.Errorf("--%s applies to `roger keys set`", f.Name)
				return
			}
			out["disabled"] = *disable && !*enable
		}
	})
	if *disable && *enable {
		return nil, fmt.Errorf("--disable and --enable together")
	}
	return out, bad
}

// nonNilCSV is splitCSV that sends an empty list (clear the allow-list) rather than null.
func nonNilCSV(s string) []string {
	if v := splitCSV(s); v != nil {
		return v
	}
	return []string{}
}

func keysUsage(w io.Writer) {
	fmt.Fprint(w, `roger keys - account API keys (rog-key_...) for scripts, CI and other tools

  roger keys list                                    your keys, their limits and use
  roger keys mint --name ci --limit 5 --reset weekly mint a key (the secret is shown ONCE)
  roger keys set <key-id> --limit 10 --models a,b    change a key
  roger keys set <key-id> --disable                  turn a key off (--enable to turn it back on)
  roger keys rm <key-id>                             revoke a key

  --name <label>  --limit <$>  --reset daily|weekly|monthly|none
  --models a,b  --nodes a,b  --expires 30d|2026-12-31|never

A key pays from your account. Use one with `+"`roger use <model> --key rog-key_...`"+`, or as the
OpenAI API key for any tool pointed at the broker. Keys need `+"`roger login`"+`.
`)
}
