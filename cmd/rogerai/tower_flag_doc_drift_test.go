package main

// tower_flag_doc_drift_test.go sweeps the written docs for a command that no longer parses.
//
// `roger share --tower` was removed because reaching the relay fabric is not a mode. The
// removal commit updated the places it could see; an audit later found four more - the
// manual's Station section, the tower-operations runbook and two lines of the packaging
// README - each still telling a provider, in the present tense, to run a command that now
// exits non-zero. That is the worst kind of stale doc: it does not read as out of date, it
// reads as a broken install.
//
// It sweeps rather than pinning known paths, because pinning paths is how the four survived.
//
// And it sweeps web/src, which the first version of this test did not - an omission with the
// shape of its own subject, since web/src/manual.html was one of the files the audit caught
// and the removal commit had to fix by hand. A sweep that skips the directory containing the
// most-read operator documentation is a sweep with a hole in exactly the wrong place.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// historicalAccounts may say `roger share --tower` because their subject IS the state before
// the flag was removed - a design document's "what is true today" section, written and dated
// against an older tree, is a record and not an instruction. Every other file is teaching.
var historicalAccounts = map[string]bool{
	"docs/relay-selection-design.md": true,
}

func TestNoDocTeachesTheRemovedTowerFlag(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	for _, dir := range []string{"docs", "packaging", "features", "web/src"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".md", ".feature", ".txt", ".html":
			default:
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			if historicalAccounts[filepath.ToSlash(rel)] {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, "share --tower") {
					offenders = append(offenders, filepath.ToSlash(rel)+":"+itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("these docs still tell an operator to run a flag that exits non-zero:\n  %s\n\n"+
			"an ordinary `roger share` reaches the relay fabric on its own. If a mention is a genuine "+
			"historical record rather than an instruction, add its path to historicalAccounts and say why.",
			strings.Join(offenders, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
