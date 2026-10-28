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
	require.Equal(t, want, again.Digest, "a path lookup reads the record beside the file")
	require.Equal(t, want, again.Sum())
	require.False(t, again.Verified(), "a digest is not a check on arrival: the file was still placed by hand")
	all, err := h.List()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, want, all[0].Digest, "the list reads the same record")
	require.Empty(t, all[0].SHA256)
	require.Equal(t, p, all[0].Path)
	require.NoError(t, srv.Stop(context.Background()))
	h.Release(s)
	require.NoError(t, h.SetSettings(Settings{DiskBudget: 64}))
	_, err = h.RoomFor(40)
	require.ErrorContains(t, err, "was placed by hand", "a hand-placed file is never one the budget may delete, digested or not")
	require.NoError(t, h.SetSettings(Settings{}))

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
// record is still written (the digest is true of the file), the serve's is not. A file outside
// the models directory gets no record beside it at all.
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
	require.NotEmpty(t, again.Digest, "the file's record is still written")
	require.False(t, again.Verified())

	// Another serve's record on that port (a restart took it) is not touched either.
	other := Serving{Supervisor: os.Getpid(), Port: 3999, Band: "other", ID: "other", Started: gone.Started.Add(time.Hour)}
	require.NoError(t, h.writeServing(other))
	h.digestLater(m, gone)
	cur, ok := h.readServing(3999)
	require.True(t, ok)
	require.Empty(t, cur.SHA256, "a record that is not this serve's keeps its own digest field")

	usb := filepath.Join(t.TempDir(), "media", "wave-Q4_K_M.gguf")
	put(t, usb, "bytes on a stick")
	um, err := FromPath(usb)
	require.NoError(t, err)
	h.digestLater(um, Serving{Supervisor: os.Getpid(), Port: 3998, Started: time.Now()})
	_, err = os.Stat(usb + recordExt)
	require.True(t, os.IsNotExist(err), "nothing is written beside a file outside the models directory")
}

// A record beside a file describes THAT file. When the owner copies a different file in under the
// same name (a new quant, a re-export), the old digest must not be said of it: the record is
// trusted only while the file's size and modification time are the ones it remembers. A record
// from before modification times were kept is trusted on its size alone.
func TestAFileReplacedUnderTheSameNameForgetsTheOldDigest(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	p := filepath.Join(h.Models(), "wave-nano-1p45b-Q4_K_M.gguf")
	put(t, p, "the first copy")
	m, err := FromPath(p)
	require.NoError(t, err)
	h.digestLater(m, Serving{Supervisor: os.Getpid(), Port: 3997, Started: time.Now()})
	first, err := FromPath(p)
	require.NoError(t, err)
	require.NotEmpty(t, first.Digest)

	// A longer file under the same name: the size gives it away.
	put(t, p, "the second copy, which is longer")
	again, err := FromPath(p)
	require.NoError(t, err)
	require.Empty(t, again.Digest, "the digest of the first copy is not said of the second")
	require.Empty(t, again.Sum())
	all, err := h.List()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Empty(t, all[0].Digest, "the list forgets it too")

	// The second copy is digested in its turn, and that digest is remembered.
	h.digestLater(again, Serving{Supervisor: os.Getpid(), Port: 3997, Started: time.Now()})
	sum := sha256.Sum256([]byte("the second copy, which is longer"))
	second, err := FromPath(p)
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(sum[:]), second.Digest)

	// The same size, different bytes, written later: the modification time gives it away.
	const thirdCopy = "the third copy, same size as two"
	require.Len(t, thirdCopy, len("the second copy, which is longer"), "the step must turn on the time, not the size")
	put(t, p, thirdCopy)
	require.NoError(t, os.Chtimes(p, time.Now(), time.Now().Add(time.Minute)))
	third, err := FromPath(p)
	require.NoError(t, err)
	require.Empty(t, third.Digest, "a same-size replacement is still a different file")

	// A record written before modification times were kept is trusted while the size matches.
	put(t, p+recordExt, `{"id":"wave-nano-1p45b-Q4_K_M","file":"wave-nano-1p45b-Q4_K_M.gguf","bytes":32,"digest":"old","band":"wave-nano-1p45b"}`)
	old, err := FromPath(p)
	require.NoError(t, err)
	require.Equal(t, "old", old.Digest)
}

// The digest finishes after the owner stopped the serve: the serve's record, already cleared by
// Release, must not come back. The write is serialised with Release so that whichever runs
// second sees the other's work.
func TestADigestNeverBringsBackTheRecordOfAServeThatStopped(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	p := filepath.Join(h.Models(), "by-hand-Q4_K_M.gguf")
	put(t, p, "bytes")
	m, err := FromPath(p)
	require.NoError(t, err)
	s := Serving{Supervisor: os.Getpid(), Port: 3996, Band: m.Band, ID: m.ID, Started: time.Now()}
	require.NoError(t, h.writeServing(s))

	released := make(chan struct{})
	prev := digestHold
	digestHold = func() { // the owner's stop lands between the digest's read and its write
		go func() { h.Release(s); close(released) }()
		time.Sleep(150 * time.Millisecond)
	}
	t.Cleanup(func() { digestHold = prev })
	h.digestLater(m, s)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("Release never finished")
	}
	_, err = os.Stat(h.recordPath(3996))
	require.True(t, os.IsNotExist(err), "the stopped serve's record came back")
	_, ok := h.Live()
	require.False(t, ok)
}

// Live E2E 2026-10-27: a model directory copied to another machine (edgesim seed, tar, rsync, podman
// cp, a FAT USB stick) keeps the file's time only to the second, so an exact comparison dropped the
// record and the fetched model became a nameless found file that `roger model serve <id>` then went
// to the hub for. The time is compared to the second, with one second either way for rounding.
func TestACopyThatKeepsTheTimeToTheSecondKeepsItsRecord(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	p := filepath.Join(h.Models(), "wave-nano-1p45b-Q4_K_M.gguf")
	put(t, p, "the weights")
	at := time.Date(2026, 10, 1, 13, 36, 50, 791022039, time.UTC)
	require.NoError(t, os.Chtimes(p, at, at))
	put(t, p+recordExt, `{"id":"rogerai/wave-nano-1.45b:Q4_K_M","file":"wave-nano-1p45b-Q4_K_M.gguf","bytes":11,"band":"wave-nano-1.45b","modtime":"`+at.Format(time.RFC3339Nano)+`"}`)
	for _, copied := range []time.Time{at.Truncate(time.Second), at.Truncate(time.Second).Add(time.Second)} {
		require.NoError(t, os.Chtimes(p, copied, copied))
		m, err := FromPath(p)
		require.NoError(t, err)
		require.Equal(t, "rogerai/wave-nano-1.45b:Q4_K_M", m.ID, "a copy kept to %v is the same file", copied)
	}
	later := at.Add(5 * time.Second)
	require.NoError(t, os.Chtimes(p, later, later))
	m, err := FromPath(p)
	require.NoError(t, err)
	require.NotEqual(t, "rogerai/wave-nano-1.45b:Q4_K_M", m.ID, "a file written seconds later is a different file")
}
