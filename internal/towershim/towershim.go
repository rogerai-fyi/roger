// Package towershim is the one-release stand-in for the old Tower binaries (features/packaging/
// one_binary.feature section 3). `roger-tower ARGS` runs `roger tower ARGS`, and
// `roger-tower-local ARGS` runs `roger tower serve ARGS`: one deprecation line on stderr (never
// stdout, so pipes see exactly roger's output), then roger takes the process over, so its exit
// code and its signals are roger's own. The shims ship for one release and are then removed.
package towershim

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// hopEnv marks a process a shim started. A shim that finds it set was started by a shim: the
// "roger" it found is itself a shim, and handing off again would loop forever.
const hopEnv = "ROGER_TOWER_SHIM_HOP"

type shim struct {
	now  string                  // what replaced the old name, for the notice
	args func([]string) []string // the roger argv after "roger"
}

var shims = map[string]shim{
	"roger-tower": {now: "roger tower", args: func(a []string) []string {
		return append([]string{"tower"}, a...)
	}},
	"roger-tower-local": {now: "roger tower serve", args: func(a []string) []string {
		if len(a) == 1 && (a[0] == "version" || a[0] == "--version") {
			return []string{"tower", "version"}
		}
		return append([]string{"tower", "serve"}, a...)
	}},
}

// Seams: where this executable is, how a program is found, and how the process is handed over.
var (
	executable           = os.Executable
	lookPath             = exec.LookPath
	handOff              = execReplace
	stderr     io.Writer = os.Stderr
	goos                 = runtime.GOOS
)

// Main runs the shim named old with the caller's arguments and returns the exit code (on Unix
// it returns only when the hand-off itself failed: roger has replaced the process otherwise).
func Main(old string, args []string) int {
	old = trimExe(old)
	// First, before anything else: a shim started by a shim means the `roger` it found is
	// itself a shim, under any name.
	if os.Getenv(hopEnv) != "" {
		fmt.Fprintf(stderr, "%s: refusing to hand off again: the `roger` it found is itself a shim (a loop); install roger itself\n", old)
		return 1
	}
	sh, ok := shims[old]
	if !ok {
		fmt.Fprintf(stderr, "%s: not a known Tower shim\n", old)
		return 2
	}
	argv := sh.args(args)
	roger, ok := findRoger()
	if !ok {
		fmt.Fprintf(stderr, "roger is not installed; install roger, then run `roger %s %s`\n", argv[0], argv[1])
		return 127
	}
	fmt.Fprintf(stderr, "%s is now `%s`; this name is removed in the next release\n", old, sh.now)
	env := append(os.Environ(), hopEnv+"=1")
	if err := handOff(roger, argv, env); err != nil {
		fmt.Fprintf(stderr, "%s: cannot run %s: %v\n", old, roger, err)
		return 1
	}
	return 0
}

func trimExe(name string) string {
	if goos == "windows" {
		if ext := filepath.Ext(name); ext == ".exe" {
			name = name[:len(name)-len(ext)]
		}
	}
	return name
}

// findRoger looks beside the shim first (the package installs them together), then on PATH. A
// candidate that is this very executable is never chosen.
func findRoger() (string, bool) {
	name := "roger"
	if goos == "windows" {
		name += ".exe"
	}
	self, _ := executable()
	if self != "" {
		beside := filepath.Join(filepath.Dir(self), name)
		if usable(beside, self) {
			return beside, true
		}
	}
	if p, err := lookPath(name); err == nil && usable(p, self) {
		return p, true
	}
	return "", false
}

func usable(candidate, self string) bool {
	ci, err := os.Stat(candidate)
	if err != nil || ci.IsDir() {
		return false
	}
	if self != "" {
		if si, err := os.Stat(self); err == nil && os.SameFile(ci, si) {
			return false
		}
	}
	return true
}
