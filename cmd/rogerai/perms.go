package main

import (
	"fmt"
	"os"
	"strings"
)

// Agent tool-approval mode on the CLI (the founder's "roger --yolo"):
//
//   - `roger --yolo` / `roger --perms <mode>` set the mode FOR THIS RUN (they win
//     over the env and the saved config; nothing is persisted).
//   - `roger perms <mode>` PERSISTS the default to config.json; `roger perms` shows
//     the effective default and where it comes from.
//
// Precedence at launch: flag > ROGERAI_AGENT_PERMS env > config.json > confirm.
// The TUI reads the resolved value from the env (internal/tui reads
// ROGERAI_AGENT_PERMS at runtime build) and always shows a permissive mode in the
// AGENT masthead, so a persisted bypass is loud in every session.

// normalizePerms maps the accepted spellings onto the canonical mode names the TUI
// parses: confirm | edits | all. ok=false for anything else.
func normalizePerms(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "confirm", "ask", "default":
		return "confirm", true
	case "edits", "auto-edits", "edit":
		return "edits", true
	case "all", "auto-all", "yolo", "bypass":
		return "all", true
	}
	return "", false
}

// stripPermsFlags pulls the global approval-mode flags out of argv (mirrors
// stripWebuiFlags): --yolo, --perms=<mode>, --perms <mode>. The LAST flag wins.
// mode is "" when no flag was given; an invalid --perms value returns err so the
// user gets a clear message instead of a silently-ignored flag.
func stripPermsFlags(args []string) (rest []string, mode string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--yolo":
			mode = "all"
		case a == "--perms" && i+1 < len(args):
			m, ok := normalizePerms(args[i+1])
			if !ok {
				return nil, "", fmt.Errorf("--perms %q: want confirm, edits, or all", args[i+1])
			}
			mode = m
			i++
		case strings.HasPrefix(a, "--perms="):
			m, ok := normalizePerms(strings.TrimPrefix(a, "--perms="))
			if !ok {
				return nil, "", fmt.Errorf("%s: want confirm, edits, or all", a)
			}
			mode = m
		default:
			rest = append(rest, a)
		}
	}
	return rest, mode, nil
}

// applyPermsDefault resolves the launch-time approval mode into the env the TUI
// reads: the flag wins for this run; otherwise an already-set env stands; otherwise
// the persisted config default seeds it.
func applyPermsDefault(flagMode, cfgMode string) {
	switch {
	case flagMode != "":
		os.Setenv("ROGERAI_AGENT_PERMS", flagMode)
	case os.Getenv("ROGERAI_AGENT_PERMS") == "" && cfgMode != "":
		os.Setenv("ROGERAI_AGENT_PERMS", cfgMode)
	}
}

// permsBlurb is the one-line meaning of each mode (kept in plain CLI voice; the
// TUI's own /perms notes carry the in-app copy).
func permsBlurb(mode string) string {
	switch mode {
	case "edits":
		return "write_file auto-approves; run_shell still asks"
	case "all":
		return "EVERY mutating tool auto-approves - nothing asks"
	}
	return "write_file and run_shell ask y/N (the default)"
}

// cmdPerms is `roger perms [mode]`: bare shows the effective default and its source;
// with a mode it persists the default to config.json.
func cmdPerms(cfg config, args []string) error {
	if len(args) == 0 {
		mode, src := "confirm", "built-in default"
		if cfg.AgentPerms != "" {
			mode, src = cfg.AgentPerms, "config.json"
		}
		if env := os.Getenv("ROGERAI_AGENT_PERMS"); env != "" {
			if m, ok := normalizePerms(env); ok {
				mode, src = m, "ROGERAI_AGENT_PERMS env"
			}
		}
		fmt.Printf("agent tool approvals: %s (%s) - %s\n", mode, src, permsBlurb(mode))
		fmt.Println("set: roger perms confirm|edits|all   this run only: roger --perms <mode> / --yolo")
		return nil
	}
	m, ok := normalizePerms(args[0])
	if !ok {
		return fmt.Errorf("perms %q: want confirm, edits, or all", args[0])
	}
	cfg.AgentPerms = m
	if m == "confirm" {
		cfg.AgentPerms = "" // the default needs no config entry
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("saved: agent tool approvals default to %s - %s\n", m, permsBlurb(m))
	if m == "all" {
		fmt.Println("! every new session starts with the bypass ON (the AGENT masthead shows AUTO-ALL); roger perms confirm restores the gate")
	}
	return nil
}
