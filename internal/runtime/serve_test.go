package runtime_test

// SUPERVISING AN INFERENCE RUNTIME (features/serving/local_models.feature §3 and §5).
//
// The runtime is a program we did not write, holding gigabytes, answering on a port. Everything
// that can go wrong with that is an operational failure an owner will blame roger for, so the
// tests are those failures rather than a happy path: a runtime that starts and never answers,
// one that dies a second later, one that answers but serves a different model than it was asked
// for, one that ignores a polite stop, and roger exiting while it is still running.
//
// The fake runtime is THIS TEST BINARY re-executed with an environment variable set, the pattern
// os/exec's own tests use. That keeps every case fast and hermetic: no llama.cpp, no network, and
// the behaviour under test is the supervisor's rather than the runtime's.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/runtime"
)

// TestMain lets this binary act as the fake runtime when asked.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_RUNTIME"); mode != "" {
		if strings.HasPrefix(mode, "load-") {
			fakeLoad(mode)
			return
		}
		if strings.HasPrefix(mode, "devices-") {
			fakeDevices(mode)
			return
		}
		if strings.HasPrefix(mode, "fit-") {
			fakeFitParams(mode)
			return
		}
		fakeRuntime(mode)
		return
	}
	os.Exit(m.Run())
}

// fakeRuntime behaves like an inference server, badly, in whichever way the mode names.
func fakeRuntime(mode string) {
	port := os.Getenv("FAKE_PORT")
	band := os.Getenv("FAKE_BAND")
	switch mode {
	case "die":
		os.Exit(3) // starts and dies at once
	case "silent":
		time.Sleep(30 * time.Second) // never answers
		os.Exit(0)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if mode == "unhealthy" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if key := os.Getenv("FAKE_KEY"); key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		served := band
		if mode == "wrongmodel" {
			served = "something-else"
		}
		fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model"}]}`, served)
	})
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux}
	if mode == "stubborn" {
		// Ignore a polite stop, so the supervisor has to insist.
		signal.Ignore(syscall.SIGTERM)
	}
	fmt.Fprintln(os.Stderr, "fake runtime listening on "+port)
	_ = srv.ListenAndServe()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// spec builds a Spec whose "runtime" is this test binary in the given mode.
func spec(t *testing.T, mode string, port int) runtime.Spec {
	t.Helper()
	model := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(model, []byte("weights"), 0o644))
	return runtime.Spec{
		Exe:   os.Args[0],
		Model: model,
		Band:  "qwen3-4b",
		Ctx:   4096,
		Port:  port,
		Env: map[string]string{
			"FAKE_RUNTIME": mode,
			"FAKE_PORT":    strconv.Itoa(port),
			"FAKE_BAND":    "qwen3-4b",
		},
		ReadyWithin: 8 * time.Second,
	}
}

func TestARuntimeIsNotAnnouncedUntilItReallyAnswers(t *testing.T) {
	port := freePort(t)
	s, err := runtime.Start(context.Background(), spec(t, "ok", port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	// It is only returned once /health answers AND the model list names the band asked for.
	require.Equal(t, "qwen3-4b", s.Band)
	require.Contains(t, s.BaseURL, strconv.Itoa(port))
	require.NotZero(t, s.PID)
	require.False(t, s.Started.IsZero())

	// The upstream really is the one roger would hand to the rest of the system.
	resp, err := http.Get(s.BaseURL + "/models")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestARuntimeThatNeverAnswersIsReportedNotLeftPending(t *testing.T) {
	sp := spec(t, "silent", freePort(t))
	sp.ReadyWithin = 400 * time.Millisecond
	started := time.Now()
	_, err := runtime.Start(context.Background(), sp)
	require.ErrorIs(t, err, runtime.ErrNotReady)
	require.Less(t, time.Since(started), 5*time.Second, "it waited far past its own deadline")
	// And it did not leave the process it started behind.
	require.NoError(t, noStrays(t))
}

func TestARuntimeThatDiesAtOnceSaysSoRatherThanTimingOut(t *testing.T) {
	sp := spec(t, "die", freePort(t))
	sp.ReadyWithin = 5 * time.Second
	started := time.Now()
	_, err := runtime.Start(context.Background(), sp)
	require.ErrorIs(t, err, runtime.ErrExited)
	require.Less(t, time.Since(started), 3*time.Second,
		"it waited for a deadline when the process was already gone")
	require.Contains(t, err.Error(), "3", "the exit status is the one useful fact and is missing")
}

// A runtime serving a DIFFERENT model than it was asked for is the subtle one: it is healthy, it
// answers, and routing to it would send somebody's turn to the wrong weights.
func TestARuntimeServingTheWrongModelIsRefused(t *testing.T) {
	sp := spec(t, "wrongmodel", freePort(t))
	_, err := runtime.Start(context.Background(), sp)
	require.ErrorIs(t, err, runtime.ErrWrongModel)
	require.Contains(t, err.Error(), "something-else", "the refusal does not say what it found")
	require.NoError(t, noStrays(t))
}

func TestStoppingGivesTheMemoryBackEvenWhenTheRuntimeIgnoresAPoliteAsk(t *testing.T) {
	port := freePort(t)
	s, err := runtime.Start(context.Background(), spec(t, "stubborn", port))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(ctx))
	require.True(t, s.Insisted(), "a runtime that ignored SIGTERM was reported as having gone quietly")

	// The port is free, which is the only proof that matters: the memory went with it.
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	require.NoError(t, err, "the runtime is still holding the port after Stop returned")
	l.Close()
}

func TestStoppingTwiceIsNotAnError(t *testing.T) {
	s, err := runtime.Start(context.Background(), spec(t, "ok", freePort(t)))
	require.NoError(t, err)
	require.NoError(t, s.Stop(context.Background()))
	require.NoError(t, s.Stop(context.Background()), "stopping something already stopped must be quiet")
}

// The log is kept so a failure can be explained, and BOUNDED so a chatty runtime cannot grow
// without limit in a process that runs for weeks.
func TestTheRuntimesOwnWordsAreKeptAndBounded(t *testing.T) {
	s, err := runtime.Start(context.Background(), spec(t, "ok", freePort(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	require.Eventually(t, func() bool {
		return strings.Contains(strings.Join(s.Tail(50), "\n"), "fake runtime listening")
	}, 3*time.Second, 50*time.Millisecond, "the runtime's own output was not captured")

	for i := 0; i < 500; i++ {
		s.Note("line " + strconv.Itoa(i))
	}
	require.LessOrEqual(t, len(s.Tail(10000)), runtime.MaxLogLines,
		"the log grew past its bound")
	require.Contains(t, strings.Join(s.Tail(5), "\n"), "line 499", "the NEWEST lines were dropped")
}

// A spec that cannot mean anything is refused before a process is started.
func TestASpecThatCannotMeanAnythingIsRefused(t *testing.T) {
	good := spec(t, "ok", freePort(t))
	for name, mut := range map[string]func(*runtime.Spec){
		"no runtime": func(s *runtime.Spec) { s.Exe = "" },
		"no model":   func(s *runtime.Spec) { s.Model = "" },
		"no band":    func(s *runtime.Spec) { s.Band = "" },
		"model that is not there": func(s *runtime.Spec) {
			s.Model = filepath.Join(t.TempDir(), "absent.gguf")
		},
	} {
		sp := good
		mut(&sp)
		_, err := runtime.Start(context.Background(), sp)
		require.Error(t, err, name)
		require.NotErrorIs(t, err, runtime.ErrNotReady, "%s: it started something anyway", name)
	}
}

// noStrays checks this process has no lingering children, which is how an orphaned runtime would
// show up: the supervisor waited for nothing and the memory never came back.
func noStrays(t *testing.T) error {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return nil // pgrep exits non-zero when there are none, which is the answer we want
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return fmt.Errorf("left %d child process(es) behind: %s", len(strings.Fields(s)), s)
	}
	return nil
}

// A runtime that demands a key is proven WITH the key, which is how every caller will ask it.
func TestAKeyedRuntimeIsProvenWithItsKey(t *testing.T) {
	port := freePort(t)
	sp := spec(t, "ok", port)
	sp.Key = "sekrit"
	sp.Env["FAKE_KEY"] = "sekrit"
	s, err := runtime.Start(context.Background(), sp)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	require.Equal(t, "sekrit", s.Key)

	resp, err := http.Get(s.BaseURL + "/models")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the keyed runtime answered a caller with no key")
}

// fakeDevices answers --list-devices the way b11146 does.
func fakeDevices(mode string) {
	switch mode {
	case "devices-two":
		fmt.Println("Available devices:")
		fmt.Println("  Vulkan0: NVIDIA RTX PRO 6000 Blackwell Max-Q Workstation Edition (97887 MiB, 72071 MiB free)")
		fmt.Println("  Vulkan1: NVIDIA RTX PRO 4500 Blackwell (32623 MiB, 11718 MiB free)")
	case "devices-none":
		fmt.Println("Available devices:")
	default:
		fmt.Fprintln(os.Stderr, "error while loading shared libraries: libvulkan.so.1: cannot open shared object file")
		os.Exit(127)
	}
}

// fakeLoad answers --version the way a build does, or fails the way the loader does.
func fakeLoad(mode string) {
	switch mode {
	case "load-ok":
		fmt.Println("version: 11146 (7fe450e19)")
	case "load-missing":
		fmt.Fprintln(os.Stderr, "./llama-server: error while loading shared libraries: libgomp.so.1: cannot open shared object file: No such file or directory")
		os.Exit(127)
	default:
		fmt.Fprintln(os.Stderr, "./llama-server: error while loading shared libraries: libweird.so.9: cannot open shared object file: No such file or directory")
		os.Exit(127)
	}
}
