//go:build unix

package towershim

import "syscall"

// execReplace hands the process to roger: same PID, so a supervisor's signals and the exit code
// are roger's own.
func execReplace(path string, args []string, env []string) error {
	return syscall.Exec(path, append([]string{path}, args...), env)
}
