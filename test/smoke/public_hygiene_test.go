package smoke

// public_hygiene_test.go keeps internal working context out of a public repository.
//
// This repo is open source. What it must never carry is the shape of one developer's
// machine or the existence of things the public cannot read: a home-directory path, a
// scratch directory, a private repository's name, an agent worktree. None of those are
// secrets, but each one is a breadcrumb, and an audit on 2026-08-22 found all of them
// live in tracked files - in comments, in committed data snapshots as "provenance", and
// in the defaults of the scripts that produce those snapshots. Scrubbing once is not a
// control; this test is.
//
// It scans every TRACKED file (git ls-files), so an untracked local note cannot trip it
// and a tracked one cannot hide. Allowed exceptions are listed by exact line, not by
// file, so adding a new hit to an already-excepted file still fails.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// forbidden is the breadcrumb list. Each entry says what it catches.
var forbidden = []struct {
	re   *regexp.Regexp
	what string
}{
	// Placeholder users (/home/u, /home/op, /home/alice) are how fixtures spell "some
	// home directory" and are allowed; a real-looking login is the breadcrumb.
	{regexp.MustCompile(`/home/(?:[a-z][a-z0-9_-]{2,})/`), "an absolute home-directory path"},
	// (placeholders longer than two letters - alice, bob, user - are listed in allowedUsers)
	{regexp.MustCompile(`~/ai/`), "a home-relative path into one developer's checkout layout"},
	{regexp.MustCompile(`rogerai-internal-docs`), "the private documentation repository's name"},
	{regexp.MustCompile(`claude-1000|/scratchpad/`), "an agent scratch directory"},
	{regexp.MustCompile(`\.claude/worktrees|worktree-agent-[0-9a-f]+`), "an agent worktree"},
	{regexp.MustCompile(`rogerai-backups|rogerai-wt/`), "a local backup or worktree directory"},
}

// allowed are deliberate generic examples, keyed by exact line content. They read as
// examples to a stranger ("~/ai/proj"), which is the test for whether a path is a
// breadcrumb or an illustration. Keys are compared with surrounding whitespace trimmed.
var allowed = map[string]bool{
	`#     boundary is EXACTLY $HOME — a child dir like ~/ai/proj single-confirms.]`: true,
	`// like ~/ai/proj single-confirms).`:                                            true,
	`And a host on "hermes" running the embedded agent in "~/ai/RogerAI"`:            true,
}

var allowedUsers = regexp.MustCompile(`/home/(?:user|alice|bob|carol|operator|example)/`)

func TestTrackedFilesCarryNoInternalWorkingContext(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}

	var hits []string
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" || rel == "test/smoke/public_hygiene_test.go" || isBinary(rel) {
			continue
		}
		data, err := readFile(filepath.Join(root, rel))
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue // unreadable or binary: not text that can leak a path
		}
		for n, line := range strings.Split(string(data), "\n") {
			if allowed[strings.TrimSpace(line)] {
				continue
			}
			for _, f := range forbidden {
				if f.re.MatchString(line) && !(f.what == "an absolute home-directory path" && allowedUsers.MatchString(line)) {
					hits = append(hits, rel+":"+itoa(n+1)+": "+f.what+": "+strings.TrimSpace(line))
					break
				}
			}
		}
	}
	if len(hits) > 0 {
		t.Fatalf("%d tracked line(s) carry internal working context; move it to the private docs repo or make it a generic example:\n  %s",
			len(hits), strings.Join(hits, "\n  "))
	}
}

func isBinary(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".woff", ".woff2", ".ttf", ".otf",
		".mp3", ".mp4", ".wav", ".ogg", ".pdf", ".zip", ".gz", ".bin", ".icns":
		return true
	}
	return false
}

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
func itoa(n int) string                 { return strconv.Itoa(n) }
