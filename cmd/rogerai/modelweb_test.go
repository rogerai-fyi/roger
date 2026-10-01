package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/runtime"
)

// features/serving/local_models.feature §8: the console's hooks are `roger model` reshaped, so a
// test of them is a test that the page and the terminal agree.

func webRig(t *testing.T) *modelRig {
	t.Helper()
	r := newModelRig(t, 16384)
	oldHost := modelHost
	t.Cleanup(func() { modelHost = oldHost; modelBudgetCache.at = time.Time{} })
	modelBudgetCache.at = time.Time{}
	return r
}

func TestConsoleViewIsTheDiskTheServeAndTheFetch(t *testing.T) {
	r := webRig(t)
	modelHost = func() runtime.Host { return runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42} }
	v, err := modelWebView()
	require.NoError(t, err)
	require.Empty(t, v.Models)
	require.Equal(t, "system memory", v.Pool)
	require.True(t, v.CanServe)
	require.Contains(t, v.Runtime, "will be fetched first", "a machine with no runtime yet is told what it costs")

	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	v, _ = modelWebView()
	require.Len(t, v.Models, 1)
	require.True(t, v.Models[0].Verified)
	require.NotNil(t, v.Job, "a fetch that just finished is still shown, briefly")
	require.Equal(t, localmodel.JobDone, v.Job.State)
	require.Positive(t, v.DiskUsed)

	// A failed fetch names its fix, read from the record a page in another process would read.
	b, _ := json.Marshal(localmodel.Job{Repo: "acme/x", File: "x.gguf", State: localmodel.JobFailed, Err: "not enough free disk", Reason: "no_space"})
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "fetch.json"), b, 0o644))
	v, _ = modelWebView()
	require.Equal(t, "free some space, or remove a model you no longer use", v.Job.Fix)

	// A board with no runtime anywhere says so and offers no serve.
	modelHost = func() runtime.Host { return runtime.Host{OS: "linux", Arch: "arm"} }
	v, _ = modelWebView()
	require.False(t, v.CanServe)
	require.Contains(t, v.Runtime, "not available here")
	_, err = modelWebServe("acme/Tiny-GGUF:Q4_K_M", 0)
	require.ErrorContains(t, err, "controlled from your Edge")
}

func TestConsoleSearchAndFilesAreTheCLIsJudgement(t *testing.T) {
	webRig(t)
	rows, err := modelWebSearch(context.Background(), "tiny")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.True(t, rows[1].Gated)

	fv, err := modelWebFiles(context.Background(), "acme/Tiny-GGUF", 0)
	require.NoError(t, err)
	require.Equal(t, "apache-2.0", fv.License)
	require.Equal(t, 8192, fv.Ctx)
	var suggested, projector bool
	for _, c := range fv.Choices {
		if c.Suggested {
			suggested = true
			require.Equal(t, "Q4_K_M", c.Label)
			require.Equal(t, "fits", c.Fit)
			require.Contains(t, c.Sum, "weights")
		}
		if c.Unservable != "" {
			projector = true
			require.Empty(t, c.Fit)
		}
	}
	require.True(t, suggested)
	require.True(t, projector)

	_, err = modelWebFiles(context.Background(), "../etc", 0)
	require.Error(t, err)
	_, err = modelWebFiles(context.Background(), "acme/Locked-GGUF", 0)
	require.ErrorContains(t, err, "HF_TOKEN")
}

func TestConsoleFetchesOnlyAFileTheIndexLists(t *testing.T) {
	r := webRig(t)
	require.ErrorContains(t, modelWebFetch("acme/Tiny-GGUF", "not-listed.gguf"), "no file")
	require.Error(t, modelWebFetch("acme/Tiny-GGUF", "../../etc/passwd"))
	require.Error(t, modelWebFetch("http://evil", "x.gguf"))
	require.NoError(t, modelWebFetch("acme/Tiny-GGUF", "Tiny-Q4_K_M.gguf"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := r.home.Job(); ok && j.State == localmodel.JobDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m, err := r.home.Find("acme/Tiny-GGUF:Q4_K_M")
	require.NoError(t, err)
	require.Equal(t, "apache-2.0", m.License)
	require.Empty(t, r.out.String(), "a console fetch prints nothing where the TUI is drawing")
}

func TestConsoleServeIsSettledFirstThenHandedOff(t *testing.T) {
	r := webRig(t)
	modelHost = func() runtime.Host { return runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42} }
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	oldSpawn := modelSpawn
	t.Cleanup(func() { modelSpawn = oldSpawn })
	var got []string
	modelSpawn = func(args []string, logPath string) error { got = args; return nil }

	says, err := modelWebServe("tiny", 4096)
	require.NoError(t, err)
	require.Contains(t, says, "fits at 4K")
	require.Equal(t, []string{"model", "serve", "acme/Tiny-GGUF:Q4_K_M", "--yes", "--ctx", "4096"}, got)

	got = nil
	_, err = modelWebServe("tiny", 2_000_000)
	require.Error(t, err)
	require.Nil(t, got, "a serve that cannot fit never reached the background")

	modelSpawn = func([]string, string) error { return errors.New("no fork") }
	_, err = modelWebServe("tiny", 0)
	require.ErrorContains(t, err, "could not start")
	_, err = modelWebServe("nothing", 0)
	require.ErrorIs(t, err, localmodel.ErrUnknownModel)

	// A serve that never came on the air leaves its last words for the page.
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "serve.log"),
		[]byte("update available v1 -> v2\nstarting...\r\nerror: out of memory\n"), 0o600))
	v, _ := modelWebView()
	require.Equal(t, []string{"starting...", "error: out of memory"}, v.ServeLog)
}

func TestConsoleStopAndRemoveKeepTheTerminalsRules(t *testing.T) {
	r := webRig(t)
	h := modelWebHooks()
	says, err := h.Stop()
	require.NoError(t, err)
	require.Contains(t, says, "nothing is being served")

	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", File: "Tiny-Q4_K_M.gguf"})
	rec := filepath.Join(r.home.Dir, "serving.json")
	require.NoError(t, os.WriteFile(rec, b, 0o600))
	_, err = h.Remove("tiny")
	require.ErrorContains(t, err, "being served right now")

	oldSig, oldIs := edgeSignal, edgeProcIsRoger
	t.Cleanup(func() { edgeSignal, edgeProcIsRoger = oldSig, oldIs })
	edgeProcIsRoger = func(int) (bool, bool) { return true, true }
	edgeSignal = func(int, syscall.Signal) error { return os.Remove(rec) }
	says, err = h.Stop()
	require.NoError(t, err)
	require.Contains(t, says, "stopped serving tiny")

	says, err = h.Remove("tiny")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(says, "removed acme/Tiny-GGUF:Q4_K_M"))
	_, err = h.Remove("tiny")
	require.ErrorIs(t, err, localmodel.ErrUnknownModel)
}
