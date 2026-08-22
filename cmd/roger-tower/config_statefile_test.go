package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Passing the data directory's tower.json to --config is the mistake this CLI's shape
// invites: `init --dir DIR` writes it and announces the directory, so it is the file
// nearest to hand when the next command asks for a --config.
//
// The value of the fix is entirely in what it does NOT do. A check that fired on anything
// unparseable would be worse than the raw decoder error - it would confidently name the
// wrong repair for every ordinary typo - so the negatives below matter more than the
// positive, and each one asserts the decoder's message SURVIVES.
func TestStateFileIsRefusedAsConfig(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "tower.json")
	if err := os.WriteFile(state, []byte(`{"mode":"standalone","tower_id":"abc123","local_network_id":"local-xyz"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(state)
	if err == nil {
		t.Fatal("a state file was accepted as a configuration")
	}
	msg := err.Error()
	for _, want := range []string{"state file", "--dir", "--config", "packaging/tower/"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q, so it does not name the repair:\n%s", want, msg)
		}
	}
	// The whole point is telling the operator WHICH directory is the data directory.
	if !strings.Contains(msg, dir) {
		t.Errorf("the refusal does not name the data directory %q:\n%s", dir, msg)
	}
}

func TestOrdinaryConfigErrorsAreNotReportedAsStateFiles(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		// A misspelled field: the operator needs the decoder's line and field name.
		"typo": "apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: standalone\nidentity:\n  dir: /tmp/x\nstationListener:\n  addres: 127.0.0.1:7070\n",
		// JSON, but not a state file. Valid YAML, so it reaches the decoder as normal.
		"json-without-tower-id": `{"hello":"world"}`,
		// Not parseable at all.
		"garbage": "\x00\x01 not a document",
		"empty":   "",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadConfig(p)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "state file") {
				t.Fatalf("an ordinary configuration error was misreported as a state file, "+
					"which sends the operator to fix the wrong thing:\n%s", err)
			}
		})
	}
}

// A state file for a JOINED Tower carries no local_network_id, so the detection must key
// on tower_id alone. Keying on both would silently miss half the cases.
func TestJoinedStateFileIsAlsoRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tower.json")
	if err := os.WriteFile(p, []byte(`{"mode":"joined","tower_id":"def456"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(p); err == nil || !strings.Contains(err.Error(), "state file") {
		t.Fatalf("a joined-mode state file was not recognised: %v", err)
	}
}
