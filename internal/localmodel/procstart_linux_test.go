//go:build linux

package localmodel

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The real /proc read for this test's own process: running, not a zombie, started in the past.
func TestReadProcStartOfThisProcess(t *testing.T) {
	started, zombie, ok := readProcStart(os.Getpid())
	require.True(t, ok)
	require.False(t, zombie)
	require.True(t, started.Before(time.Now().Add(time.Second)), "started %v", started)
	require.True(t, started.After(time.Now().Add(-24*time.Hour*365)), "started %v", started)
	_, _, ok = readProcStart(1 << 30)
	require.False(t, ok, "no such process")
}
