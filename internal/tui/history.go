package tui

// Shell-style input history (readline Up/Down recall) for the TUI's two text-entry
// surfaces: the CHANNEL chat input (modeChat) and the [0] AGENT prompt (agent.go). It
// is a small, self-contained store - load once, append on each send, walk with Up
// (older) / Down (newer) - plus a per-session navigation cursor that stashes the
// in-progress draft on the first Up and restores it when you walk back past the newest
// entry.
//
// Per-surface: the chat and the agent keep DISTINCT histories so they never bleed
// together. Each surface persists to its own file under <UserConfigDir>/rogerai/
// (history-chat / history-agent), beside dj.md and config.json. The file is a plain
// newline-delimited list, oldest first; it is created on demand, capped to the last
// historyCap entries, with consecutive duplicates collapsed. A missing or corrupt
// file simply starts an empty history - it never crashes the TUI.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// historyCap bounds a surface's on-disk + in-memory history to the most recent N
// SENT entries (older ones roll off the front). ~500 is generous for recall without
// letting the file grow unbounded.
const historyCap = 500

// inputHistory is one surface's recall buffer. entries is the persisted list, oldest
// first (entries[len-1] is the most recently sent). It also owns the live navigation
// state (the Up/Down cursor + the stashed draft) so the model can keep a single value
// per surface and the key handler stays a few thin calls.
//
// Navigation model (cursor):
//   - cursor == len(entries): NOT navigating - at the live draft (the bottom).
//   - 0 <= cursor < len(entries): showing entries[cursor]; smaller is older.
//
// draft holds the in-progress text stashed on the first Up so Down past the newest
// entry restores exactly what the user was typing.
type inputHistory struct {
	path    string   // persistence file ("" = in-memory only, e.g. when no config dir)
	entries []string // sent inputs, oldest first, capped + consecutive-deduped
	cursor  int      // navigation position; == len(entries) means "at the live draft"
	draft   string   // the in-progress line stashed on the first Up
}

// newInputHistory loads a surface's history from <UserConfigDir>/rogerai/<name> (e.g.
// history-chat). A missing or unreadable/corrupt file yields an empty-but-usable
// history (never an error) so recall degrades gracefully and the TUI always starts.
// The cursor begins at the bottom (not navigating).
func newInputHistory(name string) *inputHistory {
	h := &inputHistory{path: historyPath(name)}
	h.load()
	h.cursor = len(h.entries)
	return h
}

// historyPath resolves <UserConfigDir>/rogerai/<name>, mirroring PersonaPath's layout
// so the history files sit beside dj.md / config.json. It falls back to ~/.config so a
// headless/minimal env still gets a stable path; "" only when nothing resolves (the
// store then runs purely in-memory for the session).
func historyPath(name string) string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		if home, herr := os.UserHomeDir(); herr == nil && home != "" {
			d = filepath.Join(home, ".config")
		}
	}
	if d == "" {
		return ""
	}
	return filepath.Join(d, "rogerai", name)
}

// load reads the persisted entries (newline-delimited, oldest first). Blank lines are
// skipped; consecutive duplicates collapsed; the list is clamped to the last
// historyCap. Any read error (missing/corrupt file) leaves entries empty - it never
// surfaces an error to the caller.
func (h *inputHistory) load() {
	if h.path == "" {
		return
	}
	f, err := os.Open(h.path)
	if err != nil {
		return // missing/unreadable - start empty
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	// Allow long single-line prompts (the default 64 KiB token cap is plenty, but be
	// explicit so a big pasted prompt is not silently dropped).
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1] == line {
			continue // collapse consecutive duplicates
		}
		out = append(out, line)
	}
	if len(out) > historyCap {
		out = out[len(out)-historyCap:]
	}
	h.entries = out
}

// add records a freshly SENT input as the newest entry and resets navigation to the
// bottom. Empty/whitespace-only inputs are NOT stored (they are not a recallable turn),
// and an input identical to the current newest entry is collapsed (no consecutive
// dupes). It appends to the persisted file best-effort; a write failure is silent (the
// in-memory history still works for the session).
func (h *inputHistory) add(s string) {
	// Reset navigation regardless of whether we store: the next Up should start from
	// the most-recent entry against a clean draft.
	h.cursor = len(h.entries)
	h.draft = ""
	if strings.TrimSpace(s) == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == s {
		h.cursor = len(h.entries) // unchanged length; keep cursor at the bottom
		return
	}
	h.entries = append(h.entries, s)
	if len(h.entries) > historyCap {
		h.entries = h.entries[len(h.entries)-historyCap:]
	}
	h.cursor = len(h.entries)
	h.persist()
}

// prev walks one entry OLDER (the Up key). On the first Up it stashes the live draft
// (the in-progress text the user had typed) so Down can later restore it. It returns
// the text to show and ok=false when there is nothing older to recall (empty history,
// or already at the oldest entry) so the caller can leave the input untouched.
func (h *inputHistory) prev(currentDraft string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if h.cursor == len(h.entries) {
		h.draft = currentDraft // first Up from the live draft: stash it
	}
	if h.cursor == 0 {
		return h.entries[0], true // already oldest: stay put, keep showing it
	}
	h.cursor--
	return h.entries[h.cursor], true
}

// next walks one entry NEWER (the Down key). Walking down PAST the newest entry
// restores the stashed in-progress draft and returns to the bottom (not navigating).
// It returns ok=false only when already at the bottom with no history navigation in
// progress, so Down there is a no-op (the caller leaves the input alone).
func (h *inputHistory) next() (string, bool) {
	if h.cursor >= len(h.entries) {
		return "", false // already at the live draft - nothing newer
	}
	h.cursor++
	if h.cursor == len(h.entries) {
		return h.draft, true // walked past the newest: restore the draft
	}
	return h.entries[h.cursor], true
}

// persist writes the full (capped, deduped) history back to disk, oldest first,
// creating the roger config dir + file if missing. It is best-effort: any error
// (no config dir, unwritable path) is swallowed so a failed write never breaks the
// session. The dir is 0700 and the file 0600, matching the persona/user-key layout
// (the file can hold what the user typed, so keep it private). Note: these POSIX
// modes do not enforce on Windows (NTFS ignores the mode bits); there the user-profile
// location (%USERPROFILE%/.config) plus default ACL inheritance provides the scoping.
func (h *inputHistory) persist() {
	if h.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return
	}
	var b strings.Builder
	for _, e := range h.entries {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	_ = os.WriteFile(h.path, []byte(b.String()), 0o600)
}
