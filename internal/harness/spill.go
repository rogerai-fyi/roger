package harness

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// spill.go - AN OVERSIZED TOOL RESULT IS SAVED, NOT DISCARDED.
//
// Our sizing was already the better half of this: toolOutputBudget scales the cap with
// the band's context window rather than using one flat number, because 16 KiB is a
// rounding error on a 128K band and half the window on an 8K one. What was worse than
// the DeepSeek Harness was the DISPOSAL - we cut at the budget, appended "(truncated)",
// and threw the rest away. The model was told the result was cut and had no way to ask
// for the part it needed, so a search that found the right file could still fail on
// reading it.
//
// Now the full text goes to a session-scoped file and the model gets the preview plus
// the path. It can read_file that path - the spill lives under the workspace root, so
// the same sandbox that bounds every other read bounds this one.
//
// TWO RULES BORROWED FROM THEIRS, both learned the hard way there:
//
//   1. read_file is EXEMPT. Spilling a read produces a path, and the obvious next move
//      for a model holding a path is to read it - which spills again. The loop is
//      avoidable by simply not spilling the tool whose whole job is reading files.
//   2. A SPILL FAILURE MUST NEVER TURN A GOOD RESULT INTO AN ERROR. If the disk is
//      full or the root is read-only, the call still succeeded and the model still gets
//      the preview; it just gets the old truncation notice instead of a path.

// spillStore writes oversized results under one directory for the session and cleans
// them up with it. Mutex-guarded: overlapped tool bodies can spill at once.
type spillStore struct {
	mu   sync.Mutex
	dir  string // absolute, inside the workspace root
	root string
	n    int
}

// spillDirName is visible on purpose. A hidden directory appearing in someone's project
// is worse manners than a named one they can see, understand and delete.
const spillDirName = ".roger-spill"

func newSpillStore(root string) *spillStore { return &spillStore{root: root} }

// save writes text and returns the path to show the model, relative to the workspace
// root so it reads as something read_file can take. Any failure returns "" - the caller
// falls back to plain truncation.
func (s *spillStore) save(tool, text string) string {
	if s == nil || s.root == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return ""
		}
		dir := filepath.Join(s.root, spillDirName, hex.EncodeToString(b[:]))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return ""
		}
		// A SELF-IGNORING DIRECTORY. The spill has to live under the workspace root -
		// that is the sandbox read_file is bounded by, so anywhere else and the model
		// could not read back what it was just told to read. But the workspace root is
		// usually someone's git repo, and `.roger-spill/` turning up in their
		// `git status` is us littering in their project.
		//
		// A .gitignore containing "*" inside the directory ignores the directory's whole
		// contents INCLUDING ITSELF, so git never sees any of it. Verified against a real
		// repo, not assumed: without this the directory shows as untracked.
		//
		// Best-effort like everything else here - failing to write it must not fail the
		// spill, it just means a tidier repo was not achievable.
		_ = os.WriteFile(filepath.Join(s.root, spillDirName, ".gitignore"), []byte("*\n"), 0o600)
		s.dir = dir
	}
	s.n++
	name := fmt.Sprintf("%s-%d.txt", safeToolName(tool), s.n)
	full := filepath.Join(s.dir, name)
	if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
		return ""
	}
	rel, err := filepath.Rel(s.root, full)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// cleanup removes the session's spill directory. Best-effort: a leftover directory is
// untidy, and failing a session teardown over it would be worse.
func (s *spillStore) cleanup() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
		s.dir = ""
		// Take the parent with it when it is empty, so a finished session leaves NOTHING
		// behind - not even an empty marker directory. RemoveAll on the parent would be
		// wrong: a concurrent session may have its own directory in there.
		_ = os.Remove(filepath.Join(s.root, spillDirName, ".gitignore"))
		if err := os.Remove(filepath.Join(s.root, spillDirName)); err != nil {
			// Not empty: another session is still using it. Put the ignore file back, or
			// that session's spill becomes visible to git.
			_ = os.WriteFile(filepath.Join(s.root, spillDirName, ".gitignore"), []byte("*\n"), 0o600)
		}
	}
}

// safeToolName keeps a tool name usable as a filename.
func safeToolName(tool string) string {
	if tool == "" {
		return "tool"
	}
	var b strings.Builder
	for _, r := range tool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// spillable reports whether a tool's oversized result should be saved rather than cut.
// read_file is exempt (rule 1 above).
func spillable(tool string) bool { return tool != "read_file" }

// clipOrSpill is clipTo with a memory. Over budget, it tries to save the whole text and
// hand back a preview that NAMES where the rest is; if saving is unavailable or fails,
// it falls back to the plain truncation notice so behaviour degrades rather than breaks.
func (l *Loop) clipOrSpill(tool, text string) string {
	budget := l.MaxToolOutput
	if budget <= 0 || len(text) <= budget {
		return text
	}
	if !spillable(tool) {
		return clipTo(text, budget)
	}
	path := l.spill.save(tool, text)
	if path == "" {
		return clipTo(text, budget)
	}
	// The notice is written for the MODEL: it says what it has, what it is missing, and
	// the one move that gets the rest.
	notice := fmt.Sprintf("\n... (%s of %s output; the full text is saved at %s - read_file it for the rest)",
		humanBytes(len(text)-budget), humanBytes(len(text)), path)
	// cutAt, not clipTo: clipTo appends its OWN "(truncated)" marker, so composing the
	// two produced a result carrying two different notices AND overflowing the budget
	// they were both supposed to respect. Caught by the budget assertion, not by review.
	keep := budget - len(notice)
	if keep < 0 {
		keep = 0
	}
	return cutAt(text, keep) + notice
}

// cutAt truncates to at most n bytes on a rune boundary, adding nothing. The caller
// says what the truncation means; this only makes it fit.
func cutAt(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := n
	// Walk BACK off a continuation byte (10xxxxxx) so the kept prefix never exceeds n -
	// clipTo walks forward, which is right when the budget is a floor and wrong here
	// where it is a ceiling.
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
