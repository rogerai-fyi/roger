package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/detect"
)

// drphil.go is `roger drphil` (a.k.a. doctor/diagnose): an operator diagnostic that tells
// a provider WHY their node isn't earning and auto-fixes the obvious config faults it can.
// It gathers LOCAL checks (broker URL sanity, login/key presence, clock skew vs the
// broker, local upstream reachability) plus the broker's owner-scoped strike/ban status,
// prints a prioritized worst-first checklist, AUTO-FIXES a stale/wrong broker URL (with
// --fix), and emits a copy-pasteable `roger appeal` bundle when you're banned/held. It
// NEVER prints secrets (no keys, no tokens). See cmd/rogerai-broker/recourse.go.
//
// `roger appeal` is the companion self-serve recourse command (file/list appeals).

// drPhilOpts are the parsed flags for `roger drphil`.
type drPhilOpts struct {
	fix     bool // apply safe auto-fixes (e.g. reset a broken broker URL to the default)
	jsonOut bool // machine-readable output (reserved; the human checklist is the default)
}

// parseDrPhilFlags parses `roger drphil` flags. Split out so it is unit-testable without
// running the diagnostic (which does network I/O).
func parseDrPhilFlags(args []string) (drPhilOpts, error) {
	fs := flag.NewFlagSet("drphil", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we surface the parse error ourselves (no double-printing)
	fix := fs.Bool("fix", false, "apply safe auto-fixes (e.g. reset a broken broker URL to the default)")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return drPhilOpts{}, err
	}
	return drPhilOpts{fix: *fix, jsonOut: *jsonOut}, nil
}

// statusline prints one checklist row with a severity marker. level: "ok" | "warn" | "fail".
func statusline(level, msg string) {
	mark := "[ ok ]"
	switch level {
	case "warn":
		mark = "[warn]"
	case "fail":
		mark = "[FAIL]"
	}
	fmt.Printf("  %s %s\n", mark, msg)
}

// validBrokerURL reports whether a broker URL is well-formed (http/https with a host).
func validBrokerURL(b string) bool {
	u, err := url.Parse(strings.TrimSpace(b))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func cmdDrPhil(cfg config, args []string) error {
	opts, err := parseDrPhilFlags(args)
	if err != nil {
		return err
	}

	fmt.Println("\nDr. Phil - operator diagnostic (why isn't my node earning?)")
	fmt.Printf("  broker: %s\n\n", cfg.Broker)

	var redFlags []string // worst-first action items collected as we go

	// 1) Broker URL sanity (+ optional auto-fix to the default).
	switch {
	case validBrokerURL(cfg.Broker):
		statusline("ok", "broker URL is well-formed")
	default:
		statusline("fail", fmt.Sprintf("broker URL %q is malformed", cfg.Broker))
		redFlags = append(redFlags, "fix the broker URL: roger config set broker "+defaultBroker)
		if opts.fix {
			c := loadConfig()
			c.Broker = defaultBroker
			if err := saveConfig(c); err == nil {
				cfg.Broker = defaultBroker
				statusline("ok", "auto-fixed: broker URL reset to the default "+defaultBroker)
			}
		}
	}

	// 2) Login / signing key presence (needed to EARN, to appeal, and to see strikes).
	login := client.LinkedLogin()
	if login == "" {
		statusline("warn", "not logged in - free sharing works, but EARNING + appeals + strike status need `roger login`")
	} else {
		statusline("ok", "logged in as "+login+" (signing key present)")
	}

	// 3) Broker reachability + local clock skew (signatures are time-bound; a skewed
	//    clock silently rejects every signed request).
	skew, reachable := client.BrokerClockSkew(cfg.Broker)
	if !reachable {
		statusline("fail", "broker is unreachable (or sent no Date header) - check your network / the broker URL")
		redFlags = append(redFlags, "broker unreachable: verify "+cfg.Broker+" (or `roger drphil --fix` to reset to the default)")
		// Offer the reset when the broker is unreachable AND not already the default.
		if opts.fix && cfg.Broker != defaultBroker {
			c := loadConfig()
			c.Broker = defaultBroker
			if err := saveConfig(c); err == nil {
				statusline("ok", "auto-fixed: broker URL reset to the default "+defaultBroker+" (re-run to re-check)")
			}
		}
	} else {
		abs := skew
		if abs < 0 {
			abs = -abs
		}
		switch {
		case abs <= 30*time.Second:
			statusline("ok", fmt.Sprintf("clock in sync with the broker (skew %s)", skew.Round(time.Second)))
		case abs <= 2*time.Minute:
			statusline("warn", fmt.Sprintf("clock skew %s vs the broker - sync NTP soon (large skew rejects signatures)", skew.Round(time.Second)))
		default:
			statusline("fail", fmt.Sprintf("clock skew %s vs the broker - signatures will be REJECTED; sync your clock (NTP)", skew.Round(time.Second)))
			redFlags = append(redFlags, "fix clock skew (sync NTP): signed requests are time-bound and your clock is off by "+skew.Round(time.Second).String())
		}
	}

	// 4) Local upstream reachability (reuse the share detector): no local model = nothing
	//    to serve = no earnings, regardless of broker state.
	found, needKey := detect.DetectFull("")
	switch {
	case len(found) > 0:
		models := []string{}
		for _, f := range found {
			models = append(models, f.Models...)
		}
		statusline("ok", fmt.Sprintf("local LLM reachable (%d endpoint(s), models: %s)", len(found), summarizeModels(models)))
	case len(needKey) > 0:
		statusline("fail", "found a local server at "+needKey[0]+" but it needs an API key")
		redFlags = append(redFlags, "give the local server its key: roger share --upstream-key <key>")
	default:
		statusline("fail", "no local LLM detected (Ollama/LM Studio/llama.cpp/vLLM/Jan/LiteLLM)")
		redFlags = append(redFlags, "start a local model server, then `roger share`")
	}

	// 5) Broker-side strike / ban status (owner-scoped). Only meaningful when logged in.
	var st client.StrikesStatus
	haveStrikes := false
	if login != "" && reachable {
		if s, err := client.FetchStrikes(cfg.Broker); err == nil {
			st, haveStrikes = s, true
		}
	}
	if haveStrikes {
		switch {
		case st.Banned:
			statusline("fail", "your account is BANNED: "+orDash(st.BanReason))
			redFlags = append(redFlags, "your account is banned - file an appeal: roger appeal --reason \"<why this is a mistake>\"")
		case st.Count > 0:
			statusline("warn", fmt.Sprintf("you have %d strike(s) on record (earnings may be held pending review)", st.Count))
		default:
			statusline("ok", "no strikes on your account")
		}
		if len(st.NodeBans) > 0 {
			for node, reason := range st.NodeBans {
				statusline("fail", fmt.Sprintf("node %s is SUSPENDED: %s", node, orDash(reason)))
				redFlags = append(redFlags, fmt.Sprintf("node %s suspended - appeal it: roger appeal --node %s --reason \"<why this is a mistake>\"", node, node))
			}
		} else {
			statusline("ok", "none of your nodes are suspended")
		}
	} else if login != "" && reachable {
		statusline("warn", "could not read your strike/ban status (try `roger login` again)")
	}

	// Worst-first action list + the copy-pasteable appeal bundle.
	fmt.Println()
	if len(redFlags) == 0 {
		fmt.Println("  All clear. If you're still not earning, your price may be above the market or")
		fmt.Println("  your node may be landing on a different broker instance (see status above).")
		return nil
	}
	fmt.Println("  ACTION ITEMS (worst first):")
	for i, f := range redFlags {
		fmt.Printf("   %d. %s\n", i+1, f)
	}
	// Appeal bundle: a ready-to-run command for any ban/suspension found.
	if haveStrikes && (st.Banned || len(st.NodeBans) > 0) {
		fmt.Println("\n  APPEAL BUNDLE (copy-paste):")
		if st.Banned {
			fmt.Println("    roger appeal --reason \"My account ban is a false positive because ...\"")
		}
		for node := range st.NodeBans {
			fmt.Printf("    roger appeal --node %s --reason \"This node suspension is a mistake because ...\"\n", node)
		}
	}
	return nil
}

// summarizeModels renders up to a few model names for the diagnostic line.
func summarizeModels(models []string) string {
	if len(models) == 0 {
		return "(none reported)"
	}
	seen := map[string]bool{}
	out := []string{}
	for _, m := range models {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
		if len(out) >= 3 {
			break
		}
	}
	s := strings.Join(out, ", ")
	if len(models) > len(out) {
		s += ", ..."
	}
	return s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(no reason recorded)"
	}
	return s
}

// cmdAppeal is the self-serve recourse command: file an appeal against a strike/ban, or
// list the status of your appeals. Owner-scoped at the broker (the account is your signed
// pubkey, never a request-supplied id), so it requires `roger login`.
//
//	roger appeal --reason "..."            appeal an account ban / strike
//	roger appeal --node <id> --reason "..." appeal a specific node suspension
//	roger appeal status                    list your appeals + their state
func cmdAppeal(cfg config, args []string) error {
	if len(args) > 0 && (args[0] == "status" || args[0] == "list") {
		if client.LinkedLogin() == "" {
			return fmt.Errorf("not logged in - run `roger login` to view your appeals")
		}
		appeals, err := client.ListAppeals(cfg.Broker)
		if err != nil {
			return err
		}
		if len(appeals) == 0 {
			fmt.Println("no appeals on file.")
			return nil
		}
		fmt.Println("\n  YOUR APPEALS")
		for _, a := range appeals {
			node := a.NodeID
			if node == "" {
				node = "(account)"
			}
			fmt.Printf("    #%d  %-10s  %s  node=%s\n", a.ID, a.State, time.Unix(a.CreatedAt, 0).Format("2006-01-02"), node)
			if strings.TrimSpace(a.Note) != "" {
				fmt.Printf("        note: %s\n", a.Note)
			}
		}
		return nil
	}
	fs := flag.NewFlagSet("appeal", flag.ContinueOnError)
	node := fs.String("node", "", "node id to appeal (omit to appeal an account-level strike/ban)")
	reason := fs.String("reason", "", "why you believe the action is a mistake (your evidence/note)")
	fs.Usage = func() {
		fmt.Println(`roger appeal - contest a strike/ban (self-serve, owner-scoped)

  roger appeal --reason "..."               appeal an account ban / strike
  roger appeal --node <id> --reason "..."   appeal a specific node suspension
  roger appeal status                       list your appeals + their state

Requires ` + "`roger login`" + ` (the appeal is scoped to your signed identity).`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if client.LinkedLogin() == "" {
		return fmt.Errorf("not logged in - run `roger login` to file an appeal (it is scoped to your account)")
	}
	if strings.TrimSpace(*reason) == "" {
		return fmt.Errorf("a --reason is required (explain why the action is a mistake)")
	}
	res, err := client.FileAppeal(cfg.Broker, *node, *reason)
	if err != nil {
		return err
	}
	fmt.Printf("appeal #%d filed (state: %s) - an admin will review the evidence.\n", res.AppealID, res.State)
	if res.AutoExonerated {
		fmt.Printf("  good news: node %s was auto-exonerated (the suspension was no longer corroborated) and is routing again.\n", res.NodeUnbanned)
	}
	return nil
}
