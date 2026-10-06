package tui

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain isolates os.UserConfigDir for the WHOLE package run: the SHARE tests drive
// real controller toggles, which now hold the REAL per-node-id on-air lock (a file
// under <UserConfigDir>/rogerai). Without this every toggle here would write into the
// developer's real config dir (the `b.example` lesson: isolate on EVERY platform,
// then verify loudly).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tui-test-config-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir) // Linux
	os.Setenv("HOME", dir)            // macOS + Linux fallback
	os.Setenv("AppData", dir)         // Windows
	if got, err := os.UserConfigDir(); err != nil || !strings.HasPrefix(got, dir) {
		panic("config isolation FAILED: UserConfigDir=" + got + " not under " + dir)
	}
	// No clipboard tool is ever found and the OSC 52 escape goes nowhere, so copy paths
	// never reach (and overwrite) the developer's real clipboard.
	clipboardLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	clipboardOut = io.Discard
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
