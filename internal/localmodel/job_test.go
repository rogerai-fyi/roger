package localmodel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/hfhub"
	"rogerai.fm/roger/v6/internal/provision"
)

func slowHub(t *testing.T, body []byte, gate chan struct{}) *hfhub.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		half := len(body) / 2
		_, _ = w.Write(body[:half])
		w.(http.Flusher).Flush()
		if gate != nil {
			<-gate
		}
		_, _ = w.Write(body[half:])
	}))
	t.Cleanup(srv.Close)
	return &hfhub.Client{Base: srv.URL}
}

func waitJob(t *testing.T, h Home, state string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := h.Job(); ok && j.State == state {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := h.Job()
	t.Fatalf("the fetch never reached %q (last: %+v)", state, j)
	return Job{}
}

func TestAFetchIsRecordedWhileItRunsAndWhenItEnds(t *testing.T) {
	body := []byte(strings.Repeat("w", 64<<10))
	gate := make(chan struct{})
	h := Home{Dir: t.TempDir()}
	f := provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }}
	got := make(chan error, 1)
	require.NoError(t, h.StartFetch(slowHub(t, body, gate), f, "acme/Tiny-GGUF", fileFor("Tiny-Q4_K_M.gguf", body), "mit",
		func(m Model, err error) { got <- err }))

	j := waitJob(t, h, JobFetching)
	require.Equal(t, "acme/Tiny-GGUF", j.Repo)
	require.Equal(t, int64(len(body)), j.Total)
	require.Equal(t, os.Getpid(), j.PID)

	// A second fetch while one runs is refused, by name.
	require.ErrorIs(t, h.StartFetch(slowHub(t, body, nil), f, "acme/Tiny-GGUF", fileFor("Tiny-Q4_K_M.gguf", body), "", nil), ErrBusy)

	close(gate)
	require.NoError(t, <-got)
	j = waitJob(t, h, JobDone)
	require.Equal(t, "acme/Tiny-GGUF:Q4_K_M", j.ID)
	require.Equal(t, j.Total, j.Done)
}

func TestAFailedFetchSaysWhyAndAnInterruptedOneIsNotDone(t *testing.T) {
	body := []byte("weights")
	h := Home{Dir: t.TempDir()}
	f := provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }}
	bad := fileFor("Tiny-Q4_K_M.gguf", body)
	bad.SHA256 = strings.Repeat("0", 64)
	_, err := h.Fetch(context.Background(), slowHub(t, body, nil), f, "acme/Tiny-GGUF", bad, "")
	require.ErrorIs(t, err, provision.ErrCorrupt)
	j, ok := h.Job()
	require.True(t, ok)
	require.Equal(t, JobFailed, j.State)
	require.Contains(t, j.Err, "not what was promised")

	// A record left "fetching" by a roger that has gone is interrupted, never running, never done.
	h.writeJob(Job{Repo: "acme/x", File: "x.gguf", State: JobFetching, PID: 424242})
	defer func(a func(int) bool) { alive = a }(alive)
	alive = func(int) bool { return false }
	j, _ = h.Job()
	require.Equal(t, JobInterrupted, j.State)
	// ...and does not block the next fetch.
	_, err = h.Fetch(context.Background(), slowHub(t, body, nil), f, "acme/Tiny-GGUF", fileFor("Tiny-Q4_K_M.gguf", body), "")
	require.NoError(t, err)

	// Nothing recorded yet is no job, and nonsense in the record is no job.
	_, ok = Home{Dir: t.TempDir()}.Job()
	require.False(t, ok)
	h2 := Home{Dir: t.TempDir()}
	require.NoError(t, os.WriteFile(h2.jobPath(), []byte("{nope"), 0o644))
	_, ok = h2.Job()
	require.False(t, ok)
}

func TestStartFetchRefusesWhatCouldNeverBeServedBeforeAnyByteMoves(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	f := provision.Fetcher{}
	require.ErrorContains(t, h.StartFetch(&hfhub.Client{}, f, "acme/x", hfhub.File{Path: "mmproj-F16.gguf"}, "", nil), "projector")
	require.Error(t, h.StartFetch(&hfhub.Client{}, f, "../etc", hfhub.File{Path: "x.gguf"}, "", nil))
	_, ok := h.Job()
	require.False(t, ok, "a refused fetch left a record")
}
