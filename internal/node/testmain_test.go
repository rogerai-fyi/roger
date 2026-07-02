package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates os.UserConfigDir for the WHOLE package run: the controller's
// on-air toggles hold the REAL per-node-id lock (a file under <UserConfigDir>/rogerai),
// so without this every toggle in these tests would write into the developer's real
// config dir (the `b.example` lesson: isolate on EVERY platform, then verify loudly).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "node-test-config-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir) // Linux
	os.Setenv("HOME", dir)            // macOS + Linux fallback
	os.Setenv("AppData", dir)         // Windows
	if got, err := os.UserConfigDir(); err != nil || !strings.HasPrefix(got, dir) {
		panic("config isolation FAILED: UserConfigDir=" + got + " not under " + dir)
	}
	code := m.Run()
	_ = os.RemoveAll(filepath.Clean(dir))
	os.Exit(code)
}
