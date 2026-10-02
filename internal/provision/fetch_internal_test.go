package provision

// Pieces of the fetch that cannot be reached through a server: a disk that fills mid-stream and a
// partial that is shorter than the size it was opened at. Both use real files.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// A disk that fills during the stream returns the write error and how far it got, rather than
// reporting the network or carrying on.
func TestADiskThatFillsMidStreamStopsWithTheWriteError(t *testing.T) {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skip("no /dev/full on this platform")
	}
	defer full.Close()
	var f Fetcher
	done, err := f.stream(context.Background(), full, sha256.New(), bytes.NewReader(make([]byte, 1024)), 100, 1124, true)
	require.ErrorIs(t, err, syscall.ENOSPC)
	require.NotErrorIs(t, err, ErrUnreachable)
	require.Equal(t, int64(100), done, "nothing past what was already there was counted")
}

// A partial shorter than the length it is opened at cannot be hashed as a whole, so it is an error
// rather than a digest over fewer bytes than the resume assumes.
func TestAPartialShorterThanClaimedIsAnError(t *testing.T) {
	part := filepath.Join(t.TempDir(), "m.gguf.part")
	require.NoError(t, os.WriteFile(part, []byte("abcd"), 0o644))
	f, h, err := openPartial(part, 10)
	require.Error(t, err)
	require.Nil(t, f)
	require.Nil(t, h)

	// The same file opened at its true length hashes what is there and appends after it.
	f, h, err = openPartial(part, 4)
	require.NoError(t, err)
	_, _ = f.Write([]byte("ef"))
	_, _ = h.Write([]byte("ef"))
	require.NoError(t, f.Close())
	got, _ := os.ReadFile(part)
	require.Equal(t, "abcdef", string(got))
	want := sha256.Sum256([]byte("abcdef"))
	require.Equal(t, want[:], h.Sum(nil))
}
