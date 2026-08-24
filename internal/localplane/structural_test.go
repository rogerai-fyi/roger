package localplane

// Contract: features/tower/standalone_consumer_plane.feature
//
// The no-egress source-scan gate. The consumer handler must make no outbound call: it reads a
// request and writes a reply, and the listener that hands it connections lives in the Core-free
// binary's main, not here. A runtime check could be bypassed; reading the source cannot - if
// any non-test file in this package acquires the ability to dial, listen, resolve, or exec,
// this fails, and a new outbound path needs its own approved spec first.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsumerHandlerMakesNoOutboundCall(t *testing.T) {
	forbidden := []string{
		"net.Dial", "net.DialTimeout", "net.Listen", "net.ListenPacket",
		"http.Get", "http.Post", "http.Head", "http.NewRequest", "http.Client{",
		"http.DefaultClient", "http.ListenAndServe",
		"net.LookupHost", "net.LookupIP", "net.LookupAddr", "net.LookupCNAME", "net.Resolver",
		"exec.Command", "exec.CommandContext",
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		src := string(b)
		for _, bad := range forbidden {
			require.NotContains(t, src, bad,
				"%s uses %s: the standalone consumer handler must make no outbound call. The "+
					"listener belongs in the binary's main; any outbound path needs an approved spec.", f, bad)
		}
		checked++
	}
	require.Greater(t, checked, 1, "the scan must actually have read the package")
}
