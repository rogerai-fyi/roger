package towercli

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Run maps errors to exit codes: success 0, a usage refusal its own code (2), anything else 1,
// and prints errors to stderr with the command's name.
func TestRunExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	require.Equal(t, 0, Run([]string{"version"}, &out, &errb))
	require.Empty(t, errb.String())

	errb.Reset()
	require.Equal(t, 2, Run([]string{"serve"}, &out, &errb))
	require.Equal(t, "roger tower: --dir is required\n", errb.String())

	errb.Reset()
	require.Equal(t, 1, Run([]string{"nosuchcommand"}, &out, &errb))
	require.True(t, strings.HasPrefix(errb.String(), "roger tower: unknown command"))
}

func TestServeDispatchesOnTheDirectorysMode(t *testing.T) {
	// --dir in its = form is read too.
	sdir := initStandalone(t)
	_, err := runCLI(t, "serve", "--dir="+sdir, "--hub", ":8443")
	require.EqualError(t, err, "--hub :8443 is a joined flag; this directory is standalone")
	_, err = runCLI(t, "serve", "--dir", sdir, "--relay-public")
	require.EqualError(t, err, "--relay-public is a joined flag; this directory is standalone")
	_, err = runCLI(t, "serve", "--dir", sdir, "--hub-tls-cert=c.pem")
	require.EqualError(t, err, "--hub-tls-cert=c.pem is a joined flag; this directory is standalone")

	// A standalone directory runs the consumer plane (here the in-process --check stand-in).
	out, err := runCLI(t, "serve", "--dir", sdir, "--bind", "127.0.0.1:0")
	require.NoError(t, err)
	require.Contains(t, out, "would serve the standalone consumer plane")

	jdir := filepath.Join(t.TempDir(), "j")
	_, err = runCLI(t, "init", "--dir", jdir, "--mode", "joined")
	require.NoError(t, err)
	_, err = runCLI(t, "serve", "--dir", jdir, "--label", "shed")
	require.EqualError(t, err, "--label shed is a standalone flag; this directory is joined")
	// A joined directory with its own flags reaches the joined serve (which refuses an
	// unregistered Tower).
	_, err = runCLI(t, "serve", "--dir", jdir, "--hub", "127.0.0.1:0")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "flag; this directory is")
}

func TestServeOnAnUninitializedDirectory(t *testing.T) {
	empty := t.TempDir()
	_, err := runCLI(t, "serve", "--dir", empty)
	require.ErrorContains(t, err, "roger tower init --dir "+empty+" --mode standalone|joined")

	// A joined flag means the joined serve, whose flag checks come first.
	_, err = runCLI(t, "serve", "--dir", empty, "--hub-tls")
	require.ErrorContains(t, err, "no hub listener")
}

func TestLoginReusesRogersSignIn(t *testing.T) {
	jdir := filepath.Join(t.TempDir(), "j")
	_, err := runCLI(t, "init", "--dir", jdir, "--mode", "joined")
	require.NoError(t, err)

	old, oldLogin := signedIn, deviceLogin
	defer func() { signedIn, deviceLogin = old, oldLogin }()
	signedIn = func() (string, bool) { return "op", true }
	deviceLogin = func(string) (string, error) {
		t.Fatal("an already signed-in machine ran the device flow")
		return "", nil
	}
	out, err := runCLI(t, "login", "--dir", jdir)
	require.NoError(t, err)
	require.Contains(t, out, "signed in as @op")
	require.Contains(t, out, "next: roger tower register --dir "+jdir)
}

func TestJoinedInitRunsOnLinuxOnly(t *testing.T) {
	old := Platform
	defer func() { Platform = old }()
	for _, goos := range []string{"darwin", "windows"} {
		Platform = goos
		var out bytes.Buffer
		err := run([]string{"init", "--dir", filepath.Join(t.TempDir(), "j"), "--mode", "joined"}, &out)
		require.ErrorContains(t, err, "joined Towers run on Linux")
		require.NoError(t, run([]string{"init", "--dir", filepath.Join(t.TempDir(), "s"), "--mode", "standalone"}, io.Discard),
			"standalone runs everywhere")
	}
}
