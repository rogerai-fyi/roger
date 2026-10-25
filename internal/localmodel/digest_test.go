package localmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// edge_device_runtime.feature §1 "the digest of a hand-placed file is computed once and
// remembered": a file the owner copied in is served at once, its sha256 is worked out behind it,
// and from then on the serve, the list and a path lookup all say it. A file roger fetched and
// checked is not digested again.
func TestAHandPlacedFileIsDigestedOnceItIsOnTheAir(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	p := filepath.Join(h.Models(), "wave", "wave-nano-1p45b-Q4_K_M.gguf")
	put(t, p, "weights copied in by hand")
	sum := sha256.Sum256([]byte("weights copied in by hand"))
	want := hex.EncodeToString(sum[:])

	m, err := FromPath(p)
	require.NoError(t, err)
	require.False(t, m.Verified(), "nobody has checked it yet")

	done := make(chan struct{}, 8)
	prev := digestDone
	digestDone = func() { done <- struct{}{} }
	t.Cleanup(func() { digestDone = prev })

	srv, s, err := h.Serve(context.Background(), ServeOpts{Model: m, Exe: os.Args[0], Tag: "bX",
		Budget: Budget{Bytes: 4 * gib}, ReadyWithin: 10 * time.Second, Env: map[string]string{"FAKE_LLAMA": "1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	require.Empty(t, s.SHA256, "the serve does not wait for the digest")

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the digest never finished")
	}
	live, ok := h.Live()
	require.True(t, ok)
	require.Equal(t, want, live.SHA256, "the serve on the air says the digest")
	again, err := FromPath(p)
	require.NoError(t, err)
	require.Equal(t, want, again.SHA256, "a path lookup reads the record beside the file")
	require.True(t, again.Verified())
	all, err := h.List()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, want, all[0].SHA256, "the list reads the same record")
	require.Equal(t, p, all[0].Path)

	// A checked file is not digested again.
	srv2, s2, err := h.Serve(context.Background(), ServeOpts{Model: again, Exe: os.Args[0], Tag: "bX", Port: 0,
		Budget: Budget{Bytes: 4 * gib}, ReadyWithin: 10 * time.Second, Env: map[string]string{"FAKE_LLAMA": "1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv2.Stop(context.Background()) })
	require.Equal(t, want, s2.SHA256)
	select {
	case <-done:
		t.Fatal("a verified file was digested again")
	case <-time.After(300 * time.Millisecond):
	}
}

// A serve that left the air before its digest was known gets no record resurrected: the file's
// record is still written (the digest is true of the file), the serve's is not.
func TestADigestForAServeThatLeftTheAirWritesNoServeRecord(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	p := filepath.Join(h.Models(), "by-hand-Q4_K_M.gguf")
	put(t, p, "bytes")
	m, err := FromPath(p)
	require.NoError(t, err)
	gone := Serving{Supervisor: os.Getpid(), Port: 3999, Band: m.Band, ID: m.ID, Started: time.Now()}
	h.digestLater(m, gone)
	_, err = os.Stat(h.recordPath(3999))
	require.True(t, os.IsNotExist(err), "no serve record for a serve that is not on the air")
	again, err := FromPath(p)
	require.NoError(t, err)
	require.True(t, again.Verified(), "the file's record is still written")
}
