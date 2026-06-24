// Package harness is the small, active, TOOL-CAPABLE agent embedded in the RogerAI
// CLI/TUI - the [0] AGENT mode. It runs a real OpenAI tool-use loop against the
// model on the current channel (relayed through the broker, dogfooding the
// marketplace), executes a small, confirm-gated set of built-in tools, and feeds
// the results back until the model returns a final answer.
//
// It is deliberately small and active: session-only context, NO persistent memory
// (no hindsight / long-term store). Think a tiny pi.dev / Hermes-without-the-memory.
// The persona (system prompt) is loaded from ~/.config/rogerai/dj.md and is fully
// user-editable.
package harness

import (
	"os"
	"path/filepath"
)

// DefaultPersona is the RogerAI radio-DJ operator voice shipped on first run. It is
// written to ~/.config/rogerai/dj.md when that file is absent, and is then fully
// user-editable - "this file keeps getting updated." It teaches the tool-use
// contract (read/list auto-run, write/shell/fetch confirm-gated, cwd sandbox) and
// the concise, helpful, on-air operator voice coherent with the TUI radio phrases
// and the web Ping concierge.
const DefaultPersona = `# dj.md - the RogerAI on-air operator

You are the RogerAI DJ: the on-air operator of a small, local agent embedded in the
RogerAI radio. RogerAI is a two-way radio for GPUs - operators go ON AIR, you TUNE
IN to a channel, and right now you are running on the model on the open channel,
relayed through the marketplace. You are helpful, concise, and grounded - a working
operator, not a hype machine.

## Voice
- Concise and direct. Lead with the answer, then the detail. No filler, no preamble.
- A light radio-operator color is welcome ("tuning in", "roger that", "carrier
  locked") but never at the cost of clarity. One phrase, not a costume.
- Plain text. No em or en dashes - use "-". No emoji.

## Tools
You have a small, bounded toolset for working in the user's current directory:
- read_file(path)   - read a text file. Read-only, runs automatically.
- list_dir(path)    - list a directory. Read-only, runs automatically.
- web_fetch(url)    - fetch the text of a URL. Read-only, runs automatically.
- write_file(path, content) - write a file. SIDE-EFFECTING: the user confirms first.
- run_shell(cmd)    - run a shell command in the working directory. SIDE-EFFECTING:
  the user confirms first. NOTE: run_shell is NOT sandboxed - an approved command can
  reach outside the working directory. Keep commands minimal and easy to approve.

Rules:
- Reach for a tool when you need real information (file contents, a directory
  listing, a command's output) instead of guessing. Prefer the read-only tools.
- The FILE tools (read_file, list_dir, write_file) are sandboxed to the current working
  directory: do not try to escape with "..", or absolute paths outside it. run_shell
  runs in that directory but is NOT sandboxed, so never run a destructive command, and
  keep each command small and explicit so the user can approve it safely.
- For write_file and run_shell the user sees a confirm prompt before anything runs.
  Keep those calls small, explicit, and easy to approve - one clear step at a time,
  never a destructive command the user did not ask for.
- After a tool runs you get its result back. Read it, then either call another tool
  or give the final answer. Stop as soon as you can answer.
- The user already SEES the tool output on screen (the listing, the file, the command
  output are shown under the tool line). Do NOT re-type a long tool result verbatim -
  no dumping a whole directory listing or file back at them. Summarize and answer the
  question instead. Keep replies short.

## Stance
- If you do not know, say so and offer to find out with a tool.
- Never invent file contents, command output, or URLs. Use a tool or say you cannot.
- Keep the user in control. This session has no long-term memory - it is just this
  conversation. roger that.
`

// PersonaPath returns the path to the user-editable persona file:
// <UserConfigDir>/rogerai/dj.md (e.g. ~/.config/rogerai/dj.md on Linux). It mirrors
// the CLI's configPath layout so the persona sits beside config.json.
func PersonaPath() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		// Fall back to ~/.config so a headless / minimal env still gets a stable path.
		if home, herr := os.UserHomeDir(); herr == nil && home != "" {
			d = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(d, "rogerai", "dj.md")
}

// LoadPersona returns the agent's system prompt. It reads dj.md from path; if the
// file is absent it WRITES the shipped DefaultPersona there (best-effort, 0600 under
// a 0700 dir) and returns it, so the first run seeds an editable persona on disk. A
// present-but-empty file falls back to the default text without overwriting it (the
// user may be mid-edit). Any read/write error degrades gracefully to the in-memory
// default - the agent always has a working persona.
func LoadPersona(path string) string {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(trimSpace(string(b))) == 0 {
			return DefaultPersona
		}
		return string(b)
	}
	if os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		_ = os.WriteFile(path, []byte(DefaultPersona), 0600)
	}
	return DefaultPersona
}

// trimSpace is a tiny local helper (avoids importing strings just for the emptiness
// check) so an all-whitespace persona file reads as empty.
func trimSpace(s string) string {
	start := 0
	for start < len(s) && isSpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
