package detect

// Founder ruling 2026-10-28: NAMED PORTS ONLY. Detection probes the known default ports, the
// environment and configured or explicit URLs, and never asks this host which ports are open.
// This pins it structurally: no enumerator of listening ports is left in the package to call.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectionHasNoListeningPortEnumerator(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		src := string(b)
		for _, banned := range []string{"/proc/net/tcp", "netstat", "lsof", "GetExtendedTcpTable", "listeningPorts", "enumPorts"} {
			require.NotContains(t, src, banned, "%s still enumerates this host's listening ports (%s)", f, banned)
		}
	}
}
