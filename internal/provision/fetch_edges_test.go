package provision_test

// THE REMAINING QUIET FAILURES: a body shorter or longer than promised on a clean close, a
// cancellation that lands before the request or inside a read, a partial that can be read but not
// written, a finished file that cannot be read back, and a fetcher with no disk probe at all.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/provision"
)

// A source that closes cleanly after the WRONG number of bytes is a corrupt file, and the words say
// both numbers, because a truncation and an over-long body point at different problems.
func TestABodyOfTheWrongLengthOnACleanCloseIsCorrupt(t *testing.T) {
	for name, c := range map[string]struct {
		send, want int
		words      string
	}{
		"short": {1 << 10, 2 << 10, "1.0 KiB arrived, 2.0 KiB expected"},
		"long":  {3 << 10, 2 << 10, "3.0 KiB arrived, 2.0 KiB expected"},
	} {
		t.Run(name, func(t *testing.T) {
			body := payload(c.send)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			dest := filepath.Join(t.TempDir(), "model.gguf")
			r := provision.Request{URL: srv.URL, Dest: dest, Bytes: int64(c.want), SHA256: sum(payload(c.want))}
			err := fetcher(dest).Fetch(context.Background(), r)
			require.ErrorIs(t, err, provision.ErrCorrupt)
			require.Contains(t, err.Error(), c.words)
			_, statErr := os.Stat(dest)
			require.True(t, os.IsNotExist(statErr), "a wrong-length file was put in place")
		})
	}
}

// A fetcher built without a disk probe does not invent a refusal: it fetches.
func TestAFetcherWithNoDiskProbeDoesNotRefuse(t *testing.T) {
	body := payload(4 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	f := &provision.Fetcher{}
	require.NoError(t, f.Fetch(context.Background(), req(srv.URL, dest, body)))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

// An explicit margin is honoured instead of the default: the same free space that passes at the
// default fails when the owner asked for double headroom.
func TestAnExplicitSpaceMarginIsTheOneApplied(t *testing.T) {
	body := payload(10 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	free := func(string) (int64, bool) { return int64(len(body)) * 3 / 2, true }

	f := &provision.Fetcher{FreeSpace: free, SpaceMargin: 2}
	err := f.Fetch(context.Background(), req(srv.URL, dest, body))
	require.ErrorIs(t, err, provision.ErrNoSpace)
	require.Contains(t, err.Error(), "it needs 20.0 KiB and 15.0 KiB is free on "+filepath.Dir(dest))

	f.SpaceMargin = 0 // unset means the default 5 per cent, which fits
	require.NoError(t, f.Fetch(context.Background(), req(srv.URL, dest, body)))
}

// A rate limit with no Retry-After still says it is a rate limit, and one with it says how long.
func TestARateLimitSaysHowLongOnlyWhenTheSourceDoes(t *testing.T) {
	body := payload(1 << 10)
	retry := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")

	err := fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
	require.ErrorIs(t, err, provision.ErrRateLimited)
	require.Equal(t, provision.ErrRateLimited.Error(), err.Error(), "no wait was invented")

	retry = "30"
	err = fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
	require.ErrorIs(t, err, provision.ErrRateLimited)
	require.Contains(t, err.Error(), "try again in 30s")

	// A gated source with no reason code still names what is needed.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv2.Close()
	err = fetcher(dest).Fetch(context.Background(), req(srv2.URL, dest, body))
	require.ErrorIs(t, err, provision.ErrGated)
	require.Contains(t, err.Error(), "(access is required)")

	// 410 Gone is a missing file, not an upstream fault.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv3.Close()
	require.ErrorIs(t, fetcher(dest).Fetch(context.Background(), req(srv3.URL, dest, body)), provision.ErrNotFound)
}

// A URL that cannot be made into a request is refused as such, not reported as a dead host.
func TestAnUnparseableURLIsNotAnUnreachableHost(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "model.gguf")
	err := fetcher(dest).Fetch(context.Background(), req("http://bad host/x", dest, payload(8)))
	require.Error(t, err)
	require.NotErrorIs(t, err, provision.ErrUnreachable)
	entries, _ := os.ReadDir(filepath.Dir(dest))
	require.Empty(t, entries)
}

// A fetch cancelled before it starts is a cancellation, never "the host did not answer", and it
// writes nothing.
func TestAFetchCancelledBeforeItStartsIsACancellation(t *testing.T) {
	var hits int64
	body := payload(4 << 10)
	srv := serveRange(t, body, &hits)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fetcher(dest).Fetch(ctx, req(srv.URL, dest, body))
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, provision.ErrUnreachable)
	entries, _ := os.ReadDir(filepath.Dir(dest))
	require.Empty(t, entries, "a cancelled fetch wrote something")
}

// A cancellation that arrives while the fetch is blocked waiting for the next bytes is still a
// cancellation, not a network failure, and the bytes already on disk are kept.
func TestACancelDuringAStalledReadIsACancellation(t *testing.T) {
	body := payload(64 << 10)
	sent := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:8<<10])
		w.(http.Flusher).Flush()
		close(sent)
		<-r.Context().Done() // stall: the rest never comes
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-sent; cancel() }()

	err := fetcher(dest).Fetch(ctx, req(srv.URL, dest, body))
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, provision.ErrUnreachable)
	_, statErr := os.Stat(dest)
	require.True(t, os.IsNotExist(statErr))
}

// A partial that can be read but not written to is an error at the resume, not a silent start-over
// that throws away what is there, and nothing is published.
func TestAPartialThatCannotBeWrittenIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only file, so this cannot be exercised as root")
	}
	body := payload(8 << 10)
	srv := serveRange(t, body, nil)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	part := dest + ".part"
	require.NoError(t, os.WriteFile(part, body[:4<<10], 0o444))
	t.Cleanup(func() { _ = os.Chmod(part, 0o644) })

	err := fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body))
	require.Error(t, err)
	require.True(t, os.IsPermission(err), "got %v", err)
	got, _ := os.ReadFile(part)
	require.Equal(t, body[:4<<10], got, "the partial was altered")
	_, statErr := os.Stat(dest)
	require.True(t, os.IsNotExist(statErr))
}

// A file at the destination of the right size that cannot be read back is not trusted as
// finished: it is fetched and replaced by verified bytes.
func TestAnUnreadableFinishedFileIsNotTrusted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000 file, so this cannot be exercised as root")
	}
	body := payload(4 << 10)
	var hits int64
	srv := serveRange(t, body, &hits)
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(dest, body, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dest, 0o644) })

	require.NoError(t, fetcher(dest).Fetch(context.Background(), req(srv.URL, dest, body)))
	require.Equal(t, int64(1), hits, "it trusted a file it could not read")
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

// The same boundary the PiB fix pinned, one unit up: the fallback past the list must divide once
// more, or one exbibyte reads as "1024.0 EiB".
func TestTheSizeWordAtTheExbibyteBoundary(t *testing.T) {
	require.Equal(t, "1023.0 PiB", provision.BytesWord(1023<<50))
	require.Equal(t, "1.0 EiB", provision.BytesWord(1<<60))
	require.Equal(t, "8.0 EiB", provision.BytesWord(1<<63-1))
}
