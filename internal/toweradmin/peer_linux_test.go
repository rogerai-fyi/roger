package toweradmin

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnotherUserIsRefused(t *testing.T) {
	_, dir, logged := serving(t)
	prev := peerUID
	peerUID = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { peerUID = prev })
	_, err := Client{Dir: dir}.JoinFingerprint()
	require.Error(t, err, "a peer that is not the serving user gets no answer")
	require.Contains(t, strings.Join(logged(), "\n"), "refused a connection")
}
