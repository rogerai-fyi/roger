//go:build !unix

package towershim

import (
	"errors"
	"os"
	"os/exec"
)

// execReplace runs roger as a child where the process cannot be replaced, passing the streams
// through and leaving with roger's exit code.
func execReplace(path string, args []string, env []string) error {
	cmd := exec.Command(path, args...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
