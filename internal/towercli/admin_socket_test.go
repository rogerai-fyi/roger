package towercli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/localplane/serve"
	"rogerai.fm/roger/v6/internal/tower"
)

// features/join/admin_socket.feature: administration commands work while the Tower serves,
// through the owner-only admin socket, without stopping serve; with no serve they use the
// directory directly; rotate-key still asks for serve to stop.
func TestAdministrationWhileServing(t *testing.T) {
	base, err := os.MkdirTemp("", "tc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dir := filepath.Join(base, "lab")
	_, err = tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)

	var out bytes.Buffer
	// No serve: the directory directly.
	require.NoError(t, run([]string{"invite", "--dir", dir, "--at", "127.0.0.1:8787"}, &out))
	require.Contains(t, out.String(), "roger join ")

	p, err := serve.Prepare([]string{"--dir", dir, "--bind", "127.0.0.1:0"}, &bytes.Buffer{})
	require.NoError(t, err)
	go func() { _ = p.Serve() }()
	t.Cleanup(func() { _ = p.Close() })

	out.Reset()
	require.NoError(t, run([]string{"invite", "--dir", dir, "--at", "127.0.0.1:8787"}, &out), out.String())
	require.Contains(t, out.String(), "roger join ")
	out.Reset()
	require.NoError(t, run([]string{"invite", "--dir", dir, "--station", "--at", "127.0.0.1:8787"}, &out), out.String())
	out.Reset()
	require.NoError(t, run([]string{"invite", "--dir", dir, "--list"}, &out), out.String())
	require.Equal(t, 3, strings.Count(out.String(), " left"), out.String())

	out.Reset()
	require.NoError(t, run([]string{"invite", "--dir", dir, "--client", "u_owner"}, &out), out.String())
	id, code := field(out.String(), "invitation: "), field(out.String(), "code: ")
	out.Reset()
	require.NoError(t, run([]string{"admit", "--dir", dir, "--client", "u_owner", "--id", id, "--code", code}, &out), out.String())
	require.Contains(t, out.String(), "admitted as")
	out.Reset()
	require.NoError(t, run([]string{"attach", "--dir", dir, "--station", "s", "--key", strings.Repeat("ab", 8), "--models", "m"}, &out), out.String())
	require.Contains(t, out.String(), "attached station s")
	out.Reset()
	require.NoError(t, run([]string{"stations", "--dir", dir}, &out))
	require.Contains(t, out.String(), "s  models=m")

	out.Reset()
	err = run([]string{"rotate-key", "--dir", dir}, &out)
	require.ErrorContains(t, err, "stop the running `roger tower serve`", "a key rotation still needs serve stopped")
}

func field(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix))
		}
	}
	return ""
}
