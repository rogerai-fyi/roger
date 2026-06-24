//go:build !windows

package harness

import "testing"

// On non-Windows, run_shell must wrap the literal command in /bin/sh -c so a
// shell command line (pipes, &&, globs) works as the user typed it.
func TestShellArgvUnix(t *testing.T) {
	name, args := shellArgv("ls -la | wc -l")
	if name != "/bin/sh" {
		t.Fatalf("shell = %q, want /bin/sh", name)
	}
	if len(args) != 2 || args[0] != "-c" || args[1] != "ls -la | wc -l" {
		t.Fatalf("args = %#v, want [-c, <cmd>]", args)
	}
}
