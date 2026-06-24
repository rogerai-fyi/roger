//go:build windows

package harness

import "testing"

// On Windows /bin/sh does not exist; run_shell must select cmd /C <cmd> or the
// [0] AGENT command tool is dead. This compiles + runs only under GOOS=windows.
func TestShellArgvWindows(t *testing.T) {
	name, args := shellArgv("dir /b & echo done")
	if name != "cmd" {
		t.Fatalf("shell = %q, want cmd", name)
	}
	if len(args) != 2 || args[0] != "/C" || args[1] != "dir /b & echo done" {
		t.Fatalf("args = %#v, want [/C, <cmd>]", args)
	}
}
