package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunReturnsSetupErrors(t *testing.T) {
	// run surfaces a setup error rather than starting to serve.
	err := run([]string{}, io.Discard)
	require.ErrorContains(t, err, "--dir is required")
}

func TestMainExitsNonZeroOnSetupError(t *testing.T) {
	oldArgs, oldExit := os.Args, osExit
	defer func() { os.Args, osExit = oldArgs, oldExit }()
	code := -1
	osExit = func(c int) { code = c }
	os.Args = []string{"roger-tower-local"} // no --dir
	main()
	require.Equal(t, 1, code, "main exits non-zero when setup fails")
}

func TestVersionCommandPrintsTheBuild(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		require.NoError(t, run([]string{arg}, &out))
		require.Equal(t, version+"\n", out.String())
	}
}
