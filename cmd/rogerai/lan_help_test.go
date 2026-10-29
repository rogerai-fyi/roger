package main

// `roger lan --help` (and -h, help, and --help after a lan command) prints the lan usage and exits
// 0, like every other roger command, instead of reading the flag as an unknown command (found
// 2026-10-10).

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLANHelpPrintsUsage(t *testing.T) {
	useTempConfig(t)
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"add", "--help"}, {"list", "-h"}} {
		out, err := captureEdgeStdout(func() error { return cmdLAN(loadConfig(), args) })
		require.NoError(t, err, "%v", args)
		require.Contains(t, out, "roger lan add <address>", "%v", args)
	}
}
