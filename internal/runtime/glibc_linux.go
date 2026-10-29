//go:build linux

package runtime

import (
	"os/exec"
	"time"
)

// hostGlibc reads this machine's glibc version, which is the one host fact that has already
// silently broken a platform: upstream raised the arm64 build's floor to 2.38 and Raspberry Pi
// OS Bookworm ships 2.36, so the published binary cannot load there at all.
//
// Two sources because neither is universal: getconf is the direct question, ldd answers on
// systems where getconf does not carry that key. A musl system answers neither, and reports
// unknown rather than zero - unknown means "do not refuse on this", which is the right direction
// for a check that exists to catch one specific known breakage.
func hostGlibc() (major, minor int, ok bool) {
	for _, try := range [][]string{
		{"getconf", "GNU_LIBC_VERSION"},
		{"ldd", "--version"},
	} {
		out, err := runBounded(try[0], try[1:]...)
		if err != nil {
			continue
		}
		if maj, min, found := parseGlibc(out); found {
			return maj, min, true
		}
	}
	return 0, 0, false
}

// runBounded runs a probe with a short deadline. This happens while an owner is waiting, and a
// hung probe must never be the reason a screen does not draw.
func runBounded(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return string(out), err
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", errProbeTimeout
	}
}
