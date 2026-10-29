package serve

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tower.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

const standaloneHead = "apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: standalone\n"

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	a := ln.Addr().String()
	ln.Close()
	return a
}

// --config: the file's standalone bind, label and advertise apply; a flag wins over the file;
// --check resolves everything and binds nothing.
func TestPrepareReadsTheConfigAndAFlagWins(t *testing.T) {
	dir := standaloneDir(t)
	fileBind := freePort(t)
	cfg := writeYAML(t, standaloneHead+"standalone:\n  bind: "+fileBind+"\n  label: shed\n  advertise: true\n")

	var out bytes.Buffer
	p, err := Prepare([]string{"--dir", dir, "--config", cfg, "--check"}, &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "would serve the standalone consumer plane on "+fileBind)
	require.NoError(t, p.Serve(), "a checked plane serves nothing and returns")
	ln, lerr := net.Listen("tcp", fileBind)
	require.NoError(t, lerr, "--check bound nothing")
	ln.Close()

	flagBind := freePort(t)
	out.Reset()
	_, err = Prepare([]string{"--dir", dir, "--config", cfg, "--bind", flagBind, "--label", "flag", "--advertise=false", "--check"}, &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "on "+flagBind, "the flag wins over the file")
}

func TestPrepareRefusesABadConfig(t *testing.T) {
	dir := standaloneDir(t)
	_, err := Prepare([]string{"--dir", dir, "--config", filepath.Join(t.TempDir(), "none.yaml")}, io.Discard)
	require.Error(t, err)

	joined := writeYAML(t, "apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: joined\njoined:\n  authority: https://broker.rogerai.fm\n")
	_, err = Prepare([]string{"--dir", dir, "--config", joined}, io.Discard)
	require.ErrorContains(t, err, "is a joined configuration; this command serves a standalone directory")

	unreadable := writeYAML(t, standaloneHead+"storage:\n  urlFile: /nonexistent/db-url\n")
	_, err = Prepare([]string{"--dir", dir, "--config", unreadable, "--bind", "127.0.0.1:0"}, io.Discard)
	require.ErrorContains(t, err, "database URL file", "a configured database the plane cannot read fails closed")
}
