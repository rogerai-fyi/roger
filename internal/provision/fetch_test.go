package provision_test

// ACQUIRING A LARGE FILE WITHOUT LYING ABOUT IT (features/serving/local_models.feature §4, and
// the acquisition clauses of the approved features/share/model_catalog.feature).
//
// Every test here is a way this goes QUIETLY wrong, which is the only kind of failure that
// matters for a multi-gigabyte download: a loud one the owner sees and retries. The quiet ones
// are a truncated file that looks finished, a resume that appends to the wrong bytes, a server
// that ignores a Range header and restarts the stream while we keep appending, and a disk that
// fills at 94 per cent.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/provision"
)

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// serveRange is a server that honours Range the way HuggingFace's CDN does.
func serveRange(t *testing.T, body []byte, hits *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt64(hits, 1)
		}
		w.Header().Set("Accept-Ranges", "bytes")
		from := 0
		if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
			spec := strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-")
			if n, err := strconv.Atoi(spec); err == nil {
				from = n
			}
		}
		if from >= len(body) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if from > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		_, _ = w.Write(body[from:])
	}))
}

func fetcher(dest string) *provision.Fetcher {
	return &provision.Fetcher{
		// Plenty of room unless a test says otherwise.
		FreeSpace: func(string) (int64, bool) { return 1 << 40, true },
	}
}

func req(url, dest string, body []byte) provision.Request {
	return provision.Request{URL: url, Dest: dest, Bytes: int64(len(body)), SHA256: sum(body)}
}

func TestAFetchLandsTheRightBytesAndOnlyAtTheEnd(t *testing.T) {
	body := payload(64 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")

	var last provision.Progress
	f := fetcher(dest)
	f.OnProgress = func(p provision.Progress) { last = p }
	require.NoError(t, f.Fetch(context.Background(), req(srv.URL, dest, body)))

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, body, got)

	// Progress reported the real numbers, not a fraction.
	require.Equal(t, int64(len(body)), last.Done)
	require.Equal(t, int64(len(body)), last.Total)
	require.Greater(t, last.Rate, float64(0), "a rate of zero tells an owner nothing")

	// And no partial file is left lying about next to it.
	rest, _ := filepath.Glob(dest + "*")
	require.Len(t, rest, 1, "something other than the finished file was left behind: %v", rest)
}

func TestAnInterruptedFetchResumesAndEndsIdenticalToOneRunThrough(t *testing.T) {
	body := payload(128 << 10)
	var hits int64
	srv := serveRange(t, body, &hits)
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")

	// A previous attempt got most of the way and stopped.
	require.NoError(t, os.WriteFile(dest+".part", body[:100<<10], 0o644))

	f := fetcher(dest)
	var first provision.Progress
	f.OnProgress = func(p provision.Progress) {
		if first.Total == 0 {
			first = p
		}
	}
	require.NoError(t, f.Fetch(context.Background(), req(srv.URL, dest, body)))

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, body, got, "a resumed file differs from one fetched in a single run")

	// It really resumed: the first progress report already counted what was on disk.
	require.Equal(t, int64(100<<10), first.Done, "it started from zero instead of resuming")
	require.Equal(t, int64(1), atomic.LoadInt64(&hits))
}

// THE QUIET ONE. A server that ignores Range answers 200 with the WHOLE body. Appending that to
// what is already on disk produces a file of the right size made of the wrong bytes, whose hash
// fails for a reason nobody can diagnose. It must start over instead.
func TestAServerThatIgnoresRangeMakesUsStartOverRatherThanAppend(t *testing.T) {
	body := payload(32 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body) // 200 and the whole thing, whatever was asked for
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(dest+".part", body[:20<<10], 0o644))

	require.NoError(t, fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body)))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestAFileThatDoesNotMatchIsNeverPutInPlace(t *testing.T) {
	body := payload(8 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")

	// The index said a different hash: the bytes are not what was asked for.
	r := req(srv.URL, dest, body)
	r.SHA256 = sum(payload(9 << 10))
	err := fetcher(dest).Fetch(context.Background(), r)
	require.ErrorIs(t, err, provision.ErrCorrupt)
	require.Contains(t, err.Error(), r.SHA256[:12], "the failure does not name what was expected")
	_, statErr := os.Stat(dest)
	require.True(t, os.IsNotExist(statErr), "a file that failed its hash was put in place anyway")

	// The same for a size that does not match, which is the cheaper check and so runs first.
	r2 := req(srv.URL, dest, body)
	r2.Bytes = int64(len(body)) + 1
	require.ErrorIs(t, fetcher(dest).Fetch(context.Background(), r2), provision.ErrCorrupt)
	_, statErr = os.Stat(dest)
	require.True(t, os.IsNotExist(statErr))
}

func TestAFetchThatWouldNotFitIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	body := payload(16 << 10)
	var hits int64
	srv := serveRange(t, body, &hits)
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")

	f := &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return int64(len(body)), true }}
	err := f.Fetch(context.Background(), req(srv.URL, dest, body))
	require.ErrorIs(t, err, provision.ErrNoSpace)
	require.Contains(t, err.Error(), "free", "the refusal does not state the arithmetic")
	require.Equal(t, int64(0), atomic.LoadInt64(&hits), "it contacted the server before checking the disk")
	entries, _ := os.ReadDir(dir)
	require.Empty(t, entries, "it wrote something before refusing")

	// A machine whose free space cannot be read is not refused on a guess.
	f2 := &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 0, false }}
	require.NoError(t, f2.Fetch(context.Background(), req(srv.URL, dest, body)))
}

func TestACancelledFetchKeepsWhatItHadAndPublishesNothing(t *testing.T) {
	body := payload(512 << 10)
	ctx, cancel := context.WithCancel(context.Background())
	// The server keeps feeding in chunks; the CANCEL comes from the progress callback below, once
	// bytes are provably on disk. Cancelling on a timer would make this test about scheduling.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		for at := 0; at < len(body); at += 32 << 10 {
			if _, err := w.Write(body[at : at+(32<<10)]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")

	f := fetcher(dest)
	f.OnProgress = func(p provision.Progress) {
		if p.Done > 0 {
			cancel() // the owner pressed escape, with some of the file already written
		}
	}
	err := f.Fetch(ctx, req(srv.URL, dest, body))
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	_, statErr := os.Stat(dest)
	require.True(t, os.IsNotExist(statErr), "a cancelled fetch left a file that could be served")
	// What it had is kept, under a name nothing serves, so the next attempt resumes.
	part, err := os.Stat(dest + ".part")
	require.NoError(t, err, "a cancelled fetch threw away the bytes it had")
	require.Greater(t, part.Size(), int64(0))
}

// The failures are told apart, because "it did not work" is not an answer an owner can act on.
func TestEachFailureSaysWhichFailureItWas(t *testing.T) {
	body := payload(1 << 10)
	dest := filepath.Join(t.TempDir(), "model.gguf")
	for name, h := range map[string]struct {
		code int
		hdr  map[string]string
		want error
	}{
		"gated":     {401, map[string]string{"X-Error-Code": "GatedRepo"}, provision.ErrGated},
		"forbidden": {403, nil, provision.ErrGated},
		"missing":   {404, nil, provision.ErrNotFound},
		"rate limited": {429, map[string]string{"Retry-After": "30"},
			provision.ErrRateLimited},
		"server error": {500, nil, provision.ErrUpstream},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range h.hdr {
					w.Header().Set(k, v)
				}
				w.WriteHeader(h.code)
			}))
			defer srv.Close()
			err := fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
			require.ErrorIs(t, err, h.want, "got %v", err)
		})
	}
	// A host that does not answer at all is its own kind, not an upstream error.
	err := fetcher(dest).Fetch(context.Background(), req("http://127.0.0.1:1/x", dest, body))
	require.ErrorIs(t, err, provision.ErrUnreachable)
}

// A file already on disk and already correct is not fetched again.
func TestAFileThatIsAlreadyRightIsNotFetchedAgain(t *testing.T) {
	body := payload(4 << 10)
	var hits int64
	srv := serveRange(t, body, &hits)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(dest, body, 0o644))

	require.NoError(t, fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body)))
	require.Equal(t, int64(0), atomic.LoadInt64(&hits), "it re-downloaded a file it already had")
}

// A file on disk at the right path whose bytes are WRONG is replaced, not trusted.
func TestAFileOnDiskThatIsWrongIsReplaced(t *testing.T) {
	body := payload(4 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(dest, payload(4<<10+7), 0o644))

	require.NoError(t, fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body)))
	got, _ := os.ReadFile(dest)
	require.Equal(t, body, got)
}

func TestARequestThatCannotMeanAnythingIsRefused(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "m.gguf")
	for name, r := range map[string]provision.Request{
		"no url":  {Dest: dest, Bytes: 1, SHA256: sum(nil)},
		"no dest": {URL: "http://x/y", Bytes: 1, SHA256: sum(nil)},
		"no hash": {URL: "http://x/y", Dest: dest, Bytes: 1},
		"no size": {URL: "http://x/y", Dest: dest, SHA256: sum(nil)},
	} {
		err := fetcher(dest).Fetch(context.Background(), r)
		require.Error(t, err, name)
		require.False(t, errors.Is(err, provision.ErrUnreachable), "%s: it tried to fetch anyway", name)
	}
}

// The small helpers are where a sloppy edge case hides: a size word that reads wrong, a digest
// short enough to slice past its end, a destination with no directory in it.
func TestTheWordsForSizesAndDigestsAreRightAtTheEdges(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 512: "512 B", 1024: "1.0 KiB", 1536: "1.5 KiB",
		1 << 20: "1.0 MiB", 1 << 30: "1.0 GiB", 1 << 40: "1.0 TiB", 1 << 50: "1.0 PiB",
	} {
		require.Equal(t, want, provision.BytesWord(n), "%d", n)
	}
	// A digest is shortened for reading, and one that is already short is not sliced past its end.
	require.Equal(t, "abc", provision.ShortDigest("abc"))
	require.Equal(t, "0123456789ab", provision.ShortDigest("0123456789abcdef"))
	// A destination with no separator still yields a directory to measure.
	require.Equal(t, ".", provision.DirOf("model.gguf"))
	require.Equal(t, "/", provision.DirOf("/model.gguf"))
	require.Equal(t, "/a/b", provision.DirOf("/a/b/model.gguf"))
}

// A partial longer than the file is rubbish from an earlier, different attempt. Resuming from it
// would ask the server for bytes past the end; starting over is the honest response.
func TestAPartialLongerThanTheFileIsStartedOver(t *testing.T) {
	body := payload(8 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(dest+".part", payload(12<<10), 0o644))

	require.NoError(t, fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body)))
	got, _ := os.ReadFile(dest)
	require.Equal(t, body, got)
}

// A partial that cannot be read back is not silently treated as an empty one: that would append
// this attempt's bytes to a hash that does not cover what is on disk.
func TestAnUnreadablePartialIsAnErrorRatherThanASilentRestart(t *testing.T) {
	body := payload(8 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	part := dest + ".part"
	require.NoError(t, os.WriteFile(part, body[:4<<10], 0o000))
	t.Cleanup(func() { _ = os.Chmod(part, 0o644) })
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000 file, so this cannot be exercised as root")
	}
	err := fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
	require.Error(t, err)
	_, statErr := os.Stat(dest)
	require.True(t, os.IsNotExist(statErr))
}

// A destination directory that does not exist is a plain error, not a corrupt-file report.
func TestAnImpossibleDestinationFailsPlainly(t *testing.T) {
	body := payload(1 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "no", "such", "dir", "model.gguf")
	err := fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
	require.Error(t, err)
	require.NotErrorIs(t, err, provision.ErrCorrupt)
}

// A source that hangs up partway leaves a resumable partial and says the network failed - not
// that the file was corrupt, which would send the owner looking for the wrong problem.
func TestATruncatedStreamIsAConnectionFailureNotACorruptFile(t *testing.T) {
	body := payload(64 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:16<<10])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // the connection dies mid-body
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	err := fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
	require.Error(t, err)
	require.NotErrorIs(t, err, provision.ErrCorrupt)
	// And what arrived is kept for the next attempt.
	fi, statErr := os.Stat(dest + ".part")
	require.NoError(t, statErr)
	require.Greater(t, fi.Size(), int64(0))
}

// A request may carry headers, which is how a token reaches a gated source.
func TestRequestHeadersAreSent(t *testing.T) {
	body := payload(1 << 10)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	r := req(srv.URL, dest, body)
	r.Header = map[string]string{"Authorization": "Bearer tok"}
	require.NoError(t, fetcher(dest).Fetch(context.Background(), r))
	require.Equal(t, "Bearer tok", got)
}

// A source that says there is nothing past what we already have is a mismatch, not a resume.
func TestARangeBeyondTheEndIsReportedAsAMismatch(t *testing.T) {
	body := payload(8 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(dest+".part", body[:4<<10], 0o644))
	err := fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
	require.ErrorIs(t, err, provision.ErrCorrupt)
}
