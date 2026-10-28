package detect

import (
	"os"
	"testing"
)

// TestMain keeps every test here off this host's real listening ports. A test that wants the scan
// sets enumPorts itself; one that forgot would otherwise probe whatever this machine runs - and,
// under the gate, every other package's test servers (CI, 2026-10-28).
func TestMain(m *testing.M) {
	enumPorts = func() []int { return nil }
	os.Exit(m.Run())
}
