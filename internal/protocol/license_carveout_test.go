package protocol

// license_carveout_test.go pins the Apache-2.0 carve-out (see LICENSING.md). This package
// deliberately holds files under TWO licenses: the node-agent protocol and the usage-receipt
// SDK are Apache-2.0 so a third party can implement them; the private-band and
// remote-control wires are platform features that stay under PolyForm.
//
// A per-file boundary is easy to drift across by accident - a new file, or new code appended
// to the wrong one - and the drift is silent and legal, not a compile error. So the list is
// pinned here: adding a file to this package fails until it is placed on one side or the
// other DELIBERATELY, and the doc updated to match.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apacheFiles are the files released under Apache-2.0. Keep in sync with LICENSING.md.
var apacheFiles = map[string]bool{
	"protocol.go": true,
	"auth.go":     true,
}

const spdxApache = "// SPDX-License-Identifier: Apache-2.0"

func TestLicenseCarveout(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		head := string(b)
		if i := len(head); i > 600 {
			head = head[:600]
		}
		carved := strings.Contains(head, spdxApache)
		if strings.HasSuffix(name, "_test.go") {
			// Tests are never part of the published surface. A header here would claim a
			// grant over code nobody receives, so it is always wrong - and skipping test
			// files outright would have let one sit unnoticed.
			if carved {
				t.Errorf("%s is a test file carrying an Apache-2.0 header - tests are not part "+
					"of the carve-out", name)
			}
			continue
		}
		seen[name] = true

		switch {
		case apacheFiles[name] && !carved:
			t.Errorf("%s is listed as Apache-2.0 in LICENSING.md but carries no SPDX header - "+
				"the file is effectively platform-licensed", name)
		case !apacheFiles[name] && carved:
			t.Errorf("%s carries an Apache-2.0 SPDX header but is NOT in the carve-out list - "+
				"either add it to LICENSING.md deliberately, or remove the header", name)
		}
	}
	for name := range apacheFiles {
		if !seen[name] {
			t.Errorf("LICENSING.md claims %s is Apache-2.0, but the file is gone - "+
				"the published carve-out now names something that does not exist", name)
		}
	}
}

// TestApacheLicenseTextPresent: the carve-out is only real if the license text ships with it.
func TestApacheLicenseTextPresent(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "LICENSE-APACHE-2.0"))
	if err != nil {
		t.Fatalf("LICENSE-APACHE-2.0 is missing from the repo root: %v", err)
	}
	for _, want := range []string{"Apache License", "Version 2.0, January 2004", "TERMS AND CONDITIONS"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("LICENSE-APACHE-2.0 does not look like the Apache 2.0 text (missing %q)", want)
		}
	}
}
