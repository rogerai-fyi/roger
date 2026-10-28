package main

// A zombie (or a kernel thread) has an empty /proc/<pid>/cmdline. Reading it as a roger's command
// line indexed an empty list and panicked, which killed a fresh `roger model serve` after a restart
// the moment it tried to retire the record a recycled pid made look live (found 2026-10-10).

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnEmptyCommandLineIsNotARoger(t *testing.T) {
	require.False(t, cmdlineIsRoger(nil))
	require.False(t, cmdlineIsRoger([]byte{}))
	require.False(t, cmdlineIsRoger([]byte("\x00")))
	require.True(t, cmdlineIsRoger([]byte("/usr/local/bin/roger\x00model\x00serve\x00")))
	require.False(t, cmdlineIsRoger([]byte("/usr/bin/sleep\x0030\x00")))
}
