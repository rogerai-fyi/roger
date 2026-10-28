package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// `roger tower ...` is the Tower operator's command set (features/packaging/one_binary.feature:
// it superseded the hint that pointed at a separate roger-tower binary). main hands "tower" to
// internal/towercli before dispatch ever sees it, so dispatch has no tower case left, and the
// help names the command set.
func TestRogerTowerIsACommandSetNotAHint(t *testing.T) {
	err := dispatch(config{}, []string{"roger-tower"})
	if !errors.Is(err, errUnknownCommand) {
		t.Fatalf("dispatch(roger-tower) = %v, want a plain unknown command", err)
	}
	if strings.Contains(err.Error(), "separate") {
		t.Fatalf("the old separate-binary hint is still there: %v", err)
	}

	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	old := os.Stdout
	os.Stdout = w
	usage()
	os.Stdout = old
	w.Close()
	var b strings.Builder
	buf := make([]byte, 64<<10)
	for {
		n, rerr := r.Read(buf)
		b.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	if !strings.Contains(b.String(), "roger tower <command>") {
		t.Fatalf("roger help does not name the tower command set:\n%s", b.String())
	}
}
