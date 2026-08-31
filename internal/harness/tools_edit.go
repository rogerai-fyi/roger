package harness

// tools_edit.go - the editing and navigation half of the agent's toolset: a surgical edit,
// a search, a file finder, and the paging that makes read_file able to finish a long file.
//
// Before these, the agent could only WRITE A WHOLE FILE. Changing one line meant
// reproducing the entire file from context: expensive every turn, and silently destructive
// on a long one, because anything it failed to reproduce was simply gone and nothing in the
// loop could tell a deliberate deletion from a dropped paragraph.
//
// grep and glob are READS, and read like every other read here: Mutating stays false, so
// they run without a y/N. Routing a search through run_shell instead - the only option
// before - raised the confirm gate on the most ordinary operation there is, which does not
// make anything safer. It teaches the operator to approve shell commands by reflex, and
// that spends the attention the gate exists to collect.

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// skipDirs are never walked by grep or glob. They are large, machine-generated, and nobody
// searching a repository means to search them; walking them buries the real hits.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	spillDirName: true, // the agent's OWN spilled tool output: searching it returns its own echo
	".venv":      true, "__pycache__": true, "dist": true,
}

// looksBinary reports whether b is not text. A NUL byte in the first block is the same
// cheap test `grep -I` uses, and it is what keeps a compiled artifact out of a transcript.
func looksBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// boolArg pulls a JSON boolean out of a tool argument. (intArg already lives in search.go.)
func boolArg(v any) bool { b, _ := v.(bool); return b }

// editTool replaces an EXACT string, and fails on anything ambiguous.
//
// Every failure here is loud on purpose. A no-match that returned quietly would let the
// model believe it had made a change it had not; a multi-match that edited the first
// occurrence would edit the wrong one about as often as the right one. The model can always
// widen old_string until it is unique, or say replace_all when it genuinely means all - but
// it can only do that if it is told.
func editTool() Tool {
	return Tool{
		Name: "edit_file",
		Description: "Replace an exact string in an existing file in the working directory. " +
			"old_string must appear EXACTLY once unless replace_all is true; include enough " +
			"surrounding text to make it unique. Prefer this over write_file for changing an " +
			"existing file - write_file replaces the whole file. Side-effecting: the user " +
			"confirms before this runs.",
		Mutating: true,
		Timeout:  10 * time.Second,
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "Path to the file, relative to the working directory."},
				"old_string": map[string]any{"type": "string", "description": "The exact text to replace."},
				"new_string": map[string]any{"type": "string", "description": "The text to put in its place. Empty deletes the match."},
				"replace_all": map[string]any{"type": "boolean",
					"description": "Replace every occurrence instead of requiring exactly one."},
			},
			"required": []any{"path", "old_string", "new_string"},
		},
		Run: func(_ context.Context, root string, args map[string]any) (string, error) {
			p, err := resolveInRoot(root, str(args["path"]))
			if err != nil {
				return "", err
			}
			oldS, newS := str(args["old_string"]), str(args["new_string"])
			if oldS == "" {
				return "", fmt.Errorf("old_string is empty: there is nothing to match. " +
					"Use write_file to create or replace a whole file")
			}
			if oldS == newS {
				return "", fmt.Errorf("new_string is identical to old_string, so this edit would " +
					"change nothing")
			}
			b, err := os.ReadFile(p)
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("%s does not exist; edit_file only changes an existing "+
						"file. Use write_file to create one", str(args["path"]))
				}
				return "", err
			}
			if looksBinary(b) {
				return "", fmt.Errorf("%s is not a UTF-8 text file; refusing to edit it",
					str(args["path"]))
			}
			body := string(b)
			n := strings.Count(body, oldS)
			switch {
			case n == 0:
				return "", fmt.Errorf("old_string was not found in %s. It must match the file "+
					"exactly, including whitespace and indentation", str(args["path"]))
			case n > 1 && !boolArg(args["replace_all"]):
				return "", fmt.Errorf("old_string appears %d times in %s; it must be unique. "+
					"Add surrounding context to single one out, or pass replace_all to change "+
					"all %d", n, str(args["path"]), n)
			}
			out := strings.ReplaceAll(body, oldS, newS)
			// Preserve the file's own mode rather than imposing one.
			mode := fs.FileMode(0o644)
			if st, err := os.Stat(p); err == nil {
				mode = st.Mode().Perm()
			}
			if err := os.WriteFile(p, []byte(out), mode); err != nil {
				return "", err
			}
			word := "occurrence"
			if n > 1 {
				word = "occurrences"
			}
			return fmt.Sprintf("edited %s (%d %s replaced)", str(args["path"]), n, word), nil
		},
	}
}

// grepTool searches file CONTENTS. Read-only, so it runs without a prompt.
func grepTool() Tool {
	return Tool{
		Name: "grep",
		Description: "Search file contents in the working directory for a regular expression " +
			"and return matching lines as path:line:text. Read-only. Optionally scope to a " +
			"subdirectory (path) or a filename pattern (glob, e.g. '*.go').",
		Mutating:   false,
		Concurrent: true,
		Timeout:    30 * time.Second,
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "A regular expression (Go syntax)."},
				"path":    map[string]any{"type": "string", "description": "Optional subdirectory to search under, relative to the working directory."},
				"glob":    map[string]any{"type": "string", "description": "Optional filename pattern to restrict the search, e.g. '*.go'."},
			},
			"required": []any{"pattern"},
		},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			re, err := regexp.Compile(str(args["pattern"]))
			if err != nil {
				return "", fmt.Errorf("pattern is not a valid regular expression: %w", err)
			}
			base := root
			if rel := strings.TrimSpace(str(args["path"])); rel != "" && rel != "." {
				if base, err = resolveInRoot(root, rel); err != nil {
					return "", err
				}
			}
			glob := strings.TrimSpace(str(args["glob"]))
			var hits []string
			total := 0
			err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // an unreadable corner is skipped, not fatal
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if d.IsDir() {
					if skipDirs[d.Name()] {
						return filepath.SkipDir
					}
					return nil
				}
				if glob != "" {
					if ok, _ := filepath.Match(glob, d.Name()); !ok {
						return nil
					}
				}
				b, err := os.ReadFile(p)
				if err != nil || looksBinary(b) {
					return nil
				}
				rel, _ := filepath.Rel(root, p)
				for i, line := range strings.Split(string(b), "\n") {
					if re.MatchString(line) {
						total++
						if len(hits) < maxGrepHits {
							hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, i+1, clipLine(line)))
						}
					}
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			if total == 0 {
				return "no matches", nil
			}
			out := strings.Join(hits, "\n")
			if total > len(hits) {
				out += fmt.Sprintf("\n... (truncated: showing %d of %d matches)", len(hits), total)
			}
			return clip(out), nil
		},
	}
}

// maxGrepHits bounds a search by MATCHES as well as by bytes, so a pattern that hits a
// generated file cannot spend the whole turn's context on one tool result.
const maxGrepHits = 200

// globTool finds files by NAME. Read-only, so it runs without a prompt.
func globTool() Tool {
	return Tool{
		Name: "glob",
		Description: "Find files in the working directory by name pattern (e.g. '**/*.go', " +
			"'cmd/*/main.go') and return their paths, most recently modified first. Read-only.",
		Mutating:   false,
		Concurrent: true,
		Timeout:    30 * time.Second,
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Filename pattern, '**' matching any depth."},
			},
			"required": []any{"pattern"},
		},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			pat := strings.TrimSpace(str(args["pattern"]))
			if pat == "" {
				return "", fmt.Errorf("pattern is empty")
			}
			// A pattern is matched against paths INSIDE the root, so one that starts by
			// climbing out is refused rather than quietly matching nothing - the difference
			// matters when the agent believes it looked and found none.
			if strings.HasPrefix(pat, "/") || strings.HasPrefix(pat, "../") || strings.Contains(pat, "/../") {
				return "", fmt.Errorf("pattern must stay inside the working directory")
			}
			type hit struct {
				rel string
				mod time.Time
			}
			var hits []hit
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if d.IsDir() {
					if skipDirs[d.Name()] {
						return filepath.SkipDir
					}
					return nil
				}
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return nil
				}
				if !globMatch(pat, filepath.ToSlash(rel)) {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				hits = append(hits, hit{rel: filepath.ToSlash(rel), mod: info.ModTime()})
				return nil
			})
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "no matches", nil
			}
			// Most recently modified first: when a pattern matches many files, the ones just
			// touched are almost always the ones being asked about.
			sort.Slice(hits, func(i, j int) bool { return hits[i].mod.After(hits[j].mod) })
			var lines []string
			for _, h := range hits {
				lines = append(lines, h.rel)
			}
			return clip(strings.Join(lines, "\n")), nil
		},
	}
}

// globMatch matches a slash-separated path against a pattern where '**' spans directory
// separators. filepath.Match alone cannot: its '*' stops at a separator, so '**/*.go' would
// never match 'sub/deep/c.go'.
func globMatch(pat, rel string) bool {
	if !strings.Contains(pat, "**") {
		if ok, _ := filepath.Match(pat, rel); ok {
			return true
		}
		// A bare 'name' pattern is also matched against the basename, so '*.go' finds a
		// nested file the way a user expects it to.
		if !strings.Contains(pat, "/") {
			ok, _ := filepath.Match(pat, filepath.Base(rel))
			return ok
		}
		return false
	}
	// Split on '**' and require the pieces to appear in order.
	parts := strings.Split(pat, "**")
	rest := rel
	for i, part := range parts {
		part = strings.Trim(part, "/")
		if part == "" {
			continue
		}
		if i == len(parts)-1 {
			// The tail must match the end of the path, segment-wise.
			segs := strings.Split(rest, "/")
			for j := range segs {
				cand := strings.Join(segs[j:], "/")
				if ok, _ := filepath.Match(part, cand); ok {
					return true
				}
			}
			return false
		}
		idx := strings.Index(rest, part)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return true
}

// readRange returns the whole file, or the requested window of lines.
//
// A file over the output cap used to be simply UNREACHABLE: clip() cut it at 16 KiB and
// nothing said how to see the rest, so the agent was left to work from a copy it had only
// partly read - and then asked to rewrite it whole. A truncation now names the range to ask
// for next, which is the difference between a limit and a dead end.
// offset and limit are nil when the caller did not pass them.
//
// A sentinel int will not do here. -1 was the obvious "absent" marker and it is also a
// perfectly plausible thing for a model to send by mistake - so an explicit -1 was read as
// "no range given" and quietly returned the whole file instead of reporting the bad
// argument. Absence is not a value, so it is not encoded as one.
func readRange(body string, offset, limit *int) (string, error) {
	if offset != nil && *offset < 1 {
		return "", fmt.Errorf("offset must be a line number of 1 or more, got %d", *offset)
	}
	if limit != nil && *limit < 1 {
		return "", fmt.Errorf("limit must be 1 or more lines, got %d", *limit)
	}
	if offset == nil && limit == nil {
		out := clip(body)
		if len(out) < len(body) {
			// Count the newlines in the BODY that survived, not in the marker clip() appends
			// - and stop at the last whole line, so continuing does not skip the remainder of
			// one cut in half.
			kept := strings.TrimSuffix(out, "\n... (truncated)")
			shown := strings.Count(kept, "\n")
			out += fmt.Sprintf("\n(showing the first %d lines; read again with offset %d to continue)",
				shown, shown+1)
		}
		return out, nil
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	off, lim := 1, len(lines) // a limit without an offset reads from the top; an offset
	if offset != nil {        // without a limit reads to the end
		off = *offset
	}
	if limit != nil {
		lim = *limit
	}
	if off > len(lines) {
		return "", fmt.Errorf("offset %d is past the end: the file has %d lines", off, len(lines))
	}
	end := off - 1 + lim
	if end > len(lines) {
		end = len(lines)
	}
	// CLIP FIRST, THEN DESCRIBE WHAT SURVIVED. Describing the requested range and then
	// clipping told the model it had lines it did not get, and pointed it past them - so a
	// generous limit silently dropped the middle of a file and the continuation offset
	// skipped it for good.
	rangeBody := strings.Join(lines[off-1:end], "\n") + "\n"
	kept := clip(rangeBody)
	delivered := end
	if len(kept) < len(rangeBody) {
		// Count only whole lines that survived, and never the one cut mid-way: continuing
		// from a partial line would lose its remainder.
		if cut := strings.LastIndexByte(strings.TrimSuffix(kept, "\n... (truncated)"), '\n'); cut >= 0 {
			whole := strings.Count(kept[:cut+1], "\n")
			kept = kept[:cut+1] + "... (truncated)"
			delivered = off - 1 + whole
		}
	}
	out := fmt.Sprintf("(lines %d-%d of %d)\n", off, delivered, len(lines)) + kept
	if delivered < len(lines) {
		out += fmt.Sprintf("\n(read again with offset %d to continue)", delivered+1)
	}
	return out, nil
}
