package smoke

// tools/edgesim host mode, run for real against a stand-in authority (audit 2026-10-27):
//   - a simulated Pi whose join failed was never on the Edge, so `host down` must remove it, not wait
//     forever for an authority to forget a node it never had;
//   - a machine without setsid (macOS) still starts its simulated Pis.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// edgesimEnv is a scratch host-mode state and a stand-in authority roger that knows no nodes: `edge
// forget` fails, as the real one does for a name it does not have.
func edgesimEnv(t *testing.T, path string) (state string, env []string) {
	t.Helper()
	dir := t.TempDir()
	auth := filepath.Join(dir, "authority-roger")
	require.NoError(t, os.WriteFile(auth, []byte("#!/bin/sh\ncase \"$1 $2\" in\n\"edge forget\") echo \"no node named $3 on this Edge\" >&2; exit 1 ;;\nesac\nexit 0\n"), 0o755))
	state = filepath.Join(dir, "state")
	return state, []string{"PATH=" + path, "HOME=" + dir, "XDG_STATE_HOME=" + state,
		"EDGESIM_PREFIX=t", "EDGESIM_AUTHORITY_ROGER=" + auth}
}

func edgesim(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"../../tools/edgesim/edgesim"}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestEdgesimHostDownRemovesAPiThatNeverJoined(t *testing.T) {
	state, env := edgesimEnv(t, os.Getenv("PATH"))
	pi := filepath.Join(state, "edgesim", "t-host", "sim-pi-1")
	require.NoError(t, os.MkdirAll(filepath.Join(pi, "config"), 0o755)) // joined nothing: no node.crt
	out, err := edgesim(t, env, "host", "down")
	require.NoError(t, err, out)
	require.NoDirExists(t, pi, out)
	require.Contains(t, out, "host mode down")
}

func TestEdgesimHostStartsAPiWithoutSetsid(t *testing.T) {
	// A PATH with the tools the script needs and no setsid, as on macOS.
	bin := t.TempDir()
	for _, tool := range []string{"bash", "sh", "cat", "ps", "rm", "mkdir", "sleep", "seq", "kill", "sort", "basename", "cp", "mv", "grep", "sed", "head", "dirname", "nohup"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		require.NoError(t, os.Symlink(p, filepath.Join(bin, tool)))
	}
	state, env := edgesimEnv(t, bin)
	host := filepath.Join(state, "edgesim", "t-host")
	pi := filepath.Join(host, "sim-pi-1")
	require.NoError(t, os.MkdirAll(filepath.Join(pi, "config", "rogerai", "edge"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pi, "config", "rogerai", "edge", "node.crt"), []byte("joined"), 0o644))
	// The simulated Pi's roger: a stand-in that stays up like `roger edge run`.
	require.NoError(t, os.WriteFile(filepath.Join(host, "roger"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755))
	out, err := edgesim(t, env, "host", "reboot", "sim-pi-1")
	require.NoError(t, err, out)
	require.Contains(t, out, "sim-pi-1 restarted")
	pid, err := os.ReadFile(filepath.Join(pi, "pid"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = exec.Command("kill", strings.TrimSpace(string(pid))).Run() })
	require.Eventually(t, func() bool {
		return exec.Command("kill", "-0", strings.TrimSpace(string(pid))).Run() == nil
	}, 2*time.Second, 50*time.Millisecond, "the simulated Pi is running: %s", out)
}
