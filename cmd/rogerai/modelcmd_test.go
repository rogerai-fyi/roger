package main

import (
	"rogerai.fm/roger/v7/internal/edge/wire"

	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/hfhub"
	"rogerai.fm/roger/v7/internal/localmodel"
	"rogerai.fm/roger/v7/internal/provision"
	"rogerai.fm/roger/v7/internal/runtime"
)

// TestMain lets this test binary stand in for the inference runtime, reading the same command line
// the real one is given.
func TestMain(m *testing.M) {
	// The tower runner (tower_cli_bdd_test.go) re-execs this test binary as roger itself, so
	// `roger tower ...` is exercised through the shipped main: args, streams and exit code.
	if os.Getenv("ROGER_TEST_AS_ROGER") == "1" {
		if g := os.Getenv("ROGER_TEST_GOOS"); g != "" {
			towerPlatform = func() string { return g }
		}
		main()
		os.Exit(0)
	}
	if mode := os.Getenv("FAKE_FIT"); mode != "" && !slices.Contains(os.Args[1:], "--port") {
		modelFakeFit(mode, os.Args[1:])
		return
	}
	if os.Getenv("FAKE_LLAMA") != "" {
		modelFakeLlama(os.Args[1:])
		return
	}
	edgeHostStarted = trackEdgeHost // no Edge host outlives the test that started it (config_isolation_test.go)
	// No test hears the real LAN: a Tower advertising on the developer's network must never
	// show up as a candidate in a test. Runners that test candidates set the seam themselves.
	lanBrowseTowers = func(context.Context) (wire.BrowseResult, error) { return wire.BrowseResult{}, nil }
	// No test inherits the shell's LAN mode: a ROGER_LAN or ROGER_LAN_EDGE_ONLY exported where the
	// tests run would change what every command does. Runners that test the variables set them.
	os.Unsetenv("ROGER_LAN")
	os.Unsetenv("ROGER_LAN_EDGE_ONLY")
	// No test's LAN share runs until a signal: a station that joins (and so starts sharing) in a
	// test stops on its own after a moment. Runners that need longer set the seam themselves.
	lanShareContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), time.Second)
	}
	code := m.Run()
	// The routing-flags runner builds roger and the broker once into a temp dir; remove it, or
	// every run leaves ~65 MB behind in the temp filesystem.
	if rfBrokerBin != "" {
		_ = os.RemoveAll(filepath.Dir(rfBrokerBin))
	}
	os.Exit(code)
}

func modelFakeLlama(args []string) {
	if f := os.Getenv("FAKE_ARGS_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(args, " ")), 0o600) // what the runtime was told, for a test to read
	}
	flag := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flag[args[i]] = args[i+1]
		}
	}
	key := ""
	if f, err := os.Open(flag["--api-key-file"]); err == nil {
		sc := bufio.NewScanner(f)
		if sc.Scan() {
			key = strings.TrimSpace(sc.Text())
		}
		f.Close()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key || !slices.Contains(args, "--metrics") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "llamacpp:prompt_tokens_total 9\nllamacpp:tokens_predicted_total 5\nllamacpp:tokens_predicted_seconds_total 0.1\nllamacpp:requests_processing 0\n")
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, flag["--alias"])
	})
	_ = http.ListenAndServe("127.0.0.1:"+flag["--port"], mux)
}

// modelFakeFit answers as llama-fit-params b11146 does (internal/runtime/fit_test.go has the
// captured output): a breakdown with -fitp on, fitted arguments or a reason otherwise.
func modelFakeFit(mode string, args []string) {
	c := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" {
			c = args[i+1]
		}
	}
	switch {
	case slices.Contains(args, "-fitp"):
		fmt.Println(cmp.Or(os.Getenv("FAKE_FIT_P"), "Host 2400 1152 300")) // FAKE_FIT_P: a breakdown of a test's own
	case mode == "yes" || c == "":
		fmt.Printf("-c %s -ngl -1\n", cmp.Or(c, os.Getenv("FAKE_FIT_MAX"), "4096")) // FAKE_FIT_MAX: what it would choose
	default:
		fmt.Fprintln(os.Stderr, "0.00.150.671 W common_fit_params: failed to fit params to free device memory: was unable to fit model into system memory by reducing context, abort")
		os.Exit(1)
	}
}

// modelGGUF is a real-shaped header (Qwen3-4B's numbers) padded to a file.
func modelGGUF(pad int) []byte {
	return ggufOf([]ggufKV{
		{"general.architecture", "qwen3"},
		{"qwen3.block_count", uint32(36)},
		{"qwen3.context_length", uint32(40960)},
		{"qwen3.embedding_length", uint32(2560)},
		{"qwen3.attention.head_count", uint32(32)},
		{"qwen3.attention.head_count_kv", uint32(8)},
		{"qwen3.attention.key_length", uint32(128)},
		{"qwen3.attention.value_length", uint32(128)},
		{"qwen3.vocab_size", uint32(151936)},
	}, pad)
}

// ggufKV is one header key: a string or a uint32 value.
type ggufKV struct {
	k string
	v any
}

// ggufOf is a GGUF header with these keys, padded to a file.
func ggufOf(kvs []ggufKV, pad int) []byte {
	var b []byte
	put := func(v any) {
		switch x := v.(type) {
		case string:
			b = binary.LittleEndian.AppendUint64(b, uint64(len(x)))
			b = append(b, x...)
		case uint32:
			b = binary.LittleEndian.AppendUint32(b, x)
		case uint64:
			b = binary.LittleEndian.AppendUint64(b, x)
		}
	}
	b = append(b, 'G', 'G', 'U', 'F')
	put(uint32(3))
	put(uint64(0))
	put(uint64(len(kvs)))
	for _, e := range kvs {
		put(e.k)
		if _, ok := e.v.(string); ok {
			put(uint32(8))
		} else {
			put(uint32(4))
		}
		put(e.v)
	}
	return append(b, make([]byte, pad)...)
}

type modelRig struct {
	out   *bytes.Buffer
	home  localmodel.Home
	files map[string][]byte
}

// newModelRig points every seam at something local: a fake index, a temp home, a machine with a
// stated memory, and the test binary as the runtime.
func newModelRig(t *testing.T, ramMiB int) *modelRig {
	t.Helper()
	r := &modelRig{out: &bytes.Buffer{}, home: localmodel.Home{Dir: t.TempDir()}, files: map[string][]byte{
		"Tiny-Q4_K_M.gguf": modelGGUF(4000),
		"Tiny-Q8_0.gguf":   modelGGUF(8000),
		"mmproj-F16.gguf":  modelGGUF(10),
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		p := q.URL.Path
		switch {
		case p == "/api/models":
			fmt.Fprint(w, `[{"id":"acme/Tiny-GGUF","downloads":51234,"tags":["gguf","license:apache-2.0"]},
			  {"id":"acme/Locked-GGUF","downloads":7,"gated":"manual","tags":["gguf"]}]`)
		case p == "/api/models/acme/Tiny-GGUF/tree/main":
			var out []map[string]any
			for name, body := range r.files {
				sum := sha256.Sum256(body)
				out = append(out, map[string]any{"type": "file", "path": name, "size": 130,
					"lfs": map[string]any{"oid": hex.EncodeToString(sum[:]), "size": len(body)}})
			}
			_ = json.NewEncoder(w).Encode(out)
		case p == "/api/models/acme/Locked-GGUF/tree/main":
			w.Header().Set("X-Error-Code", "GatedRepo")
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasPrefix(p, "/acme/Tiny-GGUF/resolve/main/"):
			body, ok := r.files[strings.TrimPrefix(p, "/acme/Tiny-GGUF/resolve/main/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	oldOut, oldHome, oldHW, oldBase, oldRT, oldUntil, oldEnv, oldIn, oldFetch, oldFit := modelOut, modelHome, modelHW, modelHubBase, modelRuntimeFor, modelUntilDone, modelServeEnv, edgeStdin, modelFetcher, modelFitExe
	t.Cleanup(func() {
		modelOut, modelHome, modelHW, modelHubBase, modelRuntimeFor, modelUntilDone, modelServeEnv, edgeStdin, modelFetcher, modelFitExe = oldOut, oldHome, oldHW, oldBase, oldRT, oldUntil, oldEnv, oldIn, oldFetch, oldFit
	})
	modelOut = r.out
	modelHome = func() localmodel.Home { return r.home }
	modelHW = func() detect.LocalHW { return detect.LocalHW{RAMKnown: true, RAMTotalMiB: ramMiB} }
	modelHubBase = srv.URL
	modelFetcher = func() *provision.Fetcher {
		return &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }, OnProgress: modelProgress}
	}
	modelRuntimeFor = func(context.Context, string) (modelRT, error) {
		return modelRT{Exe: os.Args[0], Tag: "bTEST", Build: runtime.BuildCPU}, nil
	}
	modelUntilDone = func(context.Context) {}
	modelServeEnv = map[string]string{"FAKE_LLAMA": "1"}
	edgeStdin = strings.NewReader("")
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGING_FACE_HUB_TOKEN", "")
	return r
}

// fullAccount asks for today's full account (--verbose): these tests pin its wording, which
// quiet_output.feature keeps byte for byte behind the flag; the few-line default has its own runner.
// The flag goes right after the verb, so a test of a flag missing its value still means that flag.
func fullAccount(args []string) []string {
	if len(args) == 0 {
		return args // `roger model`: the list, which has no other account
	}
	return append([]string{args[0], "--verbose"}, args[1:]...)
}

func (r *modelRig) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r.out.Reset()
	err := cmdModel(config{}, fullAccount(args))
	return r.out.String(), err
}

func TestModelAnEmptyMachineSaysWhereToStart(t *testing.T) {
	r := newModelRig(t, 16384)
	out, err := r.run(t)
	require.NoError(t, err)
	require.Contains(t, out, "no models on this machine yet")
	require.Contains(t, out, "roger model search")
}

func TestModelSearchIsTheWiderWorldAndSaysWhoseWordItIs(t *testing.T) {
	r := newModelRig(t, 16384)
	out, err := r.run(t, "search", "tiny", "--limit", "5")
	require.NoError(t, err)
	require.Contains(t, out, "not RogerAI's word")
	require.Contains(t, out, "acme/Tiny-GGUF")
	require.Contains(t, out, "51.2K downloads")
	require.Contains(t, out, "apache-2.0")
	require.Contains(t, out, "GATED", "a gated repo is marked before anyone tries it")
	require.Contains(t, out, "roger model files acme/Tiny-GGUF")
}

func TestModelFilesJudgesEveryQuantAgainstThisMachine(t *testing.T) {
	r := newModelRig(t, 16384)
	out, err := r.run(t, "files", "acme/Tiny-GGUF")
	require.NoError(t, err)
	require.Contains(t, out, "this machine: 16.0 GiB of system memory")
	require.Contains(t, out, "* Q4_K_M", "the suggestion is marked")
	require.Contains(t, out, "at 8K - needs")
	require.Contains(t, out, "not servable: a vision projector")
	require.Contains(t, out, "roger model get acme/Tiny-GGUF:Q4_K_M")
	// Fitting and being usable are two claims (local_models.feature §4): the speed has its own
	// column and the machine's memory speed says where it came from.
	require.Contains(t, out, "memory speed: measured")
	require.Regexp(t, `at 8K - needs [^\n]*  ~[0-9.]+ tok/s usable`, out)

	// The runtime roger would start on Linux is the CPU build, so a machine with a card is judged
	// in the memory that build will use, and says why (local_models.feature §4).
	modelHW = func() detect.LocalHW {
		return detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 8192, RAMKnown: true, RAMTotalMiB: 16384,
			GPUs: []detect.LocalGPU{{Model: "NVIDIA GeForce RTX 4070", VRAMMiB: 8192}}}
	}
	oldHost := modelHost
	modelHost = func() runtime.Host { return runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42} }
	out, err = r.run(t, "files", "acme/Tiny-GGUF")
	modelHost = oldHost
	require.NoError(t, err)
	require.Contains(t, out, "this machine: 16.0 GiB of system memory")
	require.Contains(t, out, "the runtime roger starts here runs on the CPU")

	// A machine that cannot hold even the smallest at any context is told so, and nothing is suggested.
	r = newModelRig(t, 512)
	out, err = r.run(t, "files", "acme/Tiny-GGUF", "--ctx", "40960")
	require.NoError(t, err)
	require.Contains(t, out, "will not fit")
	require.Contains(t, out, "nothing here fits comfortably")

	// A machine whose memory could not be read says it cannot say.
	modelHW = func() detect.LocalHW { return detect.LocalHW{} }
	out, err = r.run(t, "files", "acme/Tiny-GGUF")
	require.NoError(t, err)
	require.Contains(t, out, "this machine: cannot say")
}

func TestModelFetchingIsTheOwnersDecisionAndIsVerified(t *testing.T) {
	r := newModelRig(t, 16384)
	edgeStdin = strings.NewReader("n\n")
	out, err := r.run(t, "get", "acme/Tiny-GGUF")
	require.NoError(t, err, "saying no is an answer, not a failure")
	require.Contains(t, out, "licence: apache-2.0", "the licence is shown before the fetch")
	require.Contains(t, out, "the author's word")
	require.Contains(t, out, "not fetched. Nothing changed.")
	all, _ := r.home.List()
	require.Empty(t, all)

	edgeStdin = strings.NewReader("y\n")
	out, err = r.run(t, "get", "acme/Tiny-GGUF")
	require.NoError(t, err)
	require.Contains(t, out, "fetched and verified: acme/Tiny-GGUF:Q4_K_M")

	out, err = r.run(t, "get", "acme/Tiny-GGUF:Q8_0", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, "acme/Tiny-GGUF:Q8_0")

	out, err = r.run(t, "list")
	require.NoError(t, err)
	require.Contains(t, out, "2 models")
	require.Contains(t, out, "checked")
}

func TestModelFetchFailuresSayWhichFailure(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Locked-GGUF", "--yes")
	require.ErrorContains(t, err, "HF_TOKEN", "a gated repo names the fix")
	_, err = r.run(t, "get", "acme/Nope-GGUF", "--yes")
	require.ErrorContains(t, err, "spelling")
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q5_K_M", "--yes")
	require.ErrorContains(t, err, "no servable file")
	_, err = r.run(t, "get", "acme/Tiny-GGUF", "--yes", "--ctx", "999999")
	require.ErrorContains(t, err, "fits this machine comfortably")

	// What arrives is not what was promised: nothing is kept, and the owner is told to run again.
	modelFetcher = func() *provision.Fetcher {
		return &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true },
			Client: &http.Client{Transport: corruptingTransport{}}}
	}
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.ErrorContains(t, err, "nothing was kept")
}

// corruptingTransport flips the last byte of every download, as a bad mirror would.
type corruptingTransport struct{}

func (corruptingTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(q)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	resp.Body.Close()
	b := buf.Bytes()
	if len(b) > 0 {
		b[len(b)-1] ^= 0xff
	}
	resp.Body = ioNopCloser{bytes.NewReader(b)}
	return resp, nil
}

type ioNopCloser struct{ *bytes.Reader }

func (ioNopCloser) Close() error { return nil }

func TestModelServeStartsProvesRecordsAndCleansUp(t *testing.T) {
	r := newModelRig(t, 16384)
	var during localmodel.Serving
	var startOut string
	modelUntilDone = func(context.Context) {
		startOut = r.out.String()
		var ok bool
		during, ok = r.home.Live()
		require.True(t, ok, "nothing was recorded while it served")
		// Detection's served rung hands over the endpoint WITH its key, and the key opens it.
		eps := detect.Served()
		require.Len(t, eps, 1)
		require.Equal(t, during.Key, eps[0].Key)
		f, probed := detect.ProbeKey(eps[0].Base, eps[0].Key)
		require.Equal(t, detect.Reachable, probed)
		require.Equal(t, []string{"tiny"}, f.Models)
		st, err := r.run(t, "status")
		require.NoError(t, err)
		require.Contains(t, st, "serving \"tiny\"")
		require.Contains(t, st, "acme/Tiny-GGUF:Q4_K_M")
		require.Contains(t, st, "llama.cpp bTEST")
		ls, _ := r.run(t, "list")
		require.Contains(t, ls, "▶ serving acme/Tiny-GGUF:Q4_K_M")
		_, err = r.run(t, "rm", "tiny", "--yes")
		require.ErrorContains(t, err, "being served right now")
	}
	// A repo not on the machine is fetched first, with the same consent.
	_, err := r.run(t, "serve", "acme/Tiny-GGUF", "--yes")
	require.NoError(t, err)
	require.Contains(t, startOut, "is not on this machine yet")
	require.Contains(t, startOut, "on the air locally: \"tiny\"")
	require.Contains(t, startOut, "8192 context")
	require.Equal(t, "tiny", during.Band)
	_, ok := r.home.Live()
	require.False(t, ok, "the record outlived the serving")
}

// local_models.feature §4: once the file is on disk the runtime's own check is asked and its answer
// taken - here it says no where roger's estimate said yes, because it judges what is free now.
func TestModelServeTakesTheRuntimesOwnFitAnswer(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)

	// The test binary is not beside a fit tool, so the runtime cannot be asked: the estimate
	// stands and says so.
	modelUntilDone = func(context.Context) {}
	out, err := r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "roger's estimate - the runtime's own check could not run")

	modelFitExe = func(string) string { return os.Args[0] }
	modelServeEnv = map[string]string{"FAKE_LLAMA": "1", "FAKE_FIT": "no"}
	_, err = r.run(t, "serve", "tiny")
	require.Error(t, err)
	require.Contains(t, err.Error(), "will not fit at 8K context by llama.cpp's own check: was unable to fit model into system memory")
	require.Contains(t, err.Error(), "model 2.3 GiB + context 1.1 GiB + compute 300.0 MiB + margin 1.0 GiB", "its own terms")
	require.Contains(t, err.Error(), "roger model serve acme/Tiny-GGUF:Q4_K_M --ctx 4096", "the context the runtime would choose")
	_, live := r.home.Live()
	require.False(t, live, "nothing was started")

	// The runtime judges what is free NOW, so a model being served counts against the new one. A
	// refused switch says so, rather than leaving the owner to wonder why a smaller machine said yes.
	rec, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Band: "older", ID: "x/older:Q8_0", Started: time.Now()})
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "serving.json"), rec, 0o600))
	_, err = r.run(t, "serve", "tiny")
	require.ErrorContains(t, err, "x/older:Q8_0 is being served and holds memory the check counted as in use")
	require.NoError(t, os.Remove(filepath.Join(r.home.Dir, "serving.json")))

	modelServeEnv = map[string]string{"FAKE_LLAMA": "1", "FAKE_FIT": "yes"}
	out, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "fits at 8K context by llama.cpp's own check")
	require.Contains(t, out, "on the air locally")
}

func TestModelServeRefusesWithTheArithmeticNeverSilentlyShrinking(t *testing.T) {
	r := newModelRig(t, 2048)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	_, err = r.run(t, "serve", "tiny", "--ctx", "40960")
	require.Error(t, err)
	require.Contains(t, err.Error(), "will not fit at 40K")
	require.Contains(t, err.Error(), "weights", "the sum is shown")
	require.Contains(t, err.Error(), "roger model serve acme/Tiny-GGUF:Q4_K_M --ctx", "the context that does fit is offered")
}

func TestModelAMachineWithNoRuntimeSaysWhatItCanStillDo(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	modelRuntimeFor = func(context.Context, string) (modelRT, error) {
		return modelRT{}, fmt.Errorf("%w: needs glibc 2.38, this has 2.36", runtime.ErrNoRuntime)
	}
	_, err = r.run(t, "serve", "tiny")
	require.ErrorContains(t, err, "local serving is not available on this machine")
	require.ErrorContains(t, err, "2.36")
	require.ErrorContains(t, err, "monitored and controlled from the authority")

	modelRuntimeFor = func(context.Context, string) (modelRT, error) { return modelRT{}, errors.New("disk full") }
	_, err = r.run(t, "serve", "tiny")
	require.ErrorContains(t, err, "could not get the runtime")
}

func TestModelAFileTheOwnerAlreadyHasServesWhereItIs(t *testing.T) {
	r := newModelRig(t, 16384)
	p := filepath.Join(t.TempDir(), "mine-Q4_K_M.gguf")
	require.NoError(t, os.WriteFile(p, modelGGUF(100), 0o644))
	out, err := r.run(t, "serve", p)
	require.NoError(t, err)
	require.Contains(t, out, "on the air locally: \"mine\"")

	// A file whose header cannot be read is served on the owner's word, and says so.
	junk := filepath.Join(t.TempDir(), "junk.gguf")
	require.NoError(t, os.WriteFile(junk, []byte("not a gguf"), 0o644))
	out, err = r.run(t, "serve", junk)
	require.NoError(t, err)
	require.Contains(t, out, "cannot judge the fit")
}

func TestModelASwitchStopsTheOldOnlyOnceTheNewAnswers(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)

	old := localmodel.Serving{Supervisor: os.Getppid(), Band: "older", ID: "x/older:Q4_K_M", Started: time.Now()}
	b, _ := json.Marshal(old)
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "serving.json"), b, 0o600))

	oldSig, oldIs := edgeSignal, edgeProcIsRoger
	t.Cleanup(func() { edgeSignal, edgeProcIsRoger = oldSig, oldIs })
	var signalled []int
	edgeSignal = func(pid int, _ syscall.Signal) error { signalled = append(signalled, pid); return nil }
	edgeProcIsRoger = func(int) (bool, bool) { return true, true }

	// A start that fails leaves the old one serving and says so.
	modelServeEnv = map[string]string{}
	modelRuntimeFor = func(context.Context, string) (modelRT, error) {
		return modelRT{Exe: filepath.Join(t.TempDir(), "absent"), Tag: "bTEST", Build: runtime.BuildCPU}, nil
	}
	_, err = r.run(t, "serve", "tiny")
	require.ErrorContains(t, err, "x/older:Q4_K_M is still serving - nothing was switched")
	require.Empty(t, signalled, "the old one was stopped before the new one answered")

	modelServeEnv = map[string]string{"FAKE_LLAMA": "1"}
	modelRuntimeFor = func(context.Context, string) (modelRT, error) {
		return modelRT{Exe: os.Args[0], Tag: "bTEST", Build: runtime.BuildCPU}, nil
	}
	out, err := r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "switched from x/older:Q4_K_M")
	require.Equal(t, []int{os.Getppid()}, signalled)
}

func TestModelStopSignalsOnlyWhatItCanVouchFor(t *testing.T) {
	r := newModelRig(t, 16384)
	out, err := r.run(t, "stop")
	require.NoError(t, err)
	require.Contains(t, out, "nothing to stop")
	out, err = r.run(t, "status")
	require.NoError(t, err)
	require.Contains(t, out, "nothing is being served")

	rec := filepath.Join(r.home.Dir, "serving.json")
	b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Band: "tiny"})
	require.NoError(t, os.WriteFile(rec, b, 0o600))
	oldSig, oldIs, oldWait := edgeSignal, edgeProcIsRoger, modelStopWait
	t.Cleanup(func() { edgeSignal, edgeProcIsRoger, modelStopWait = oldSig, oldIs, oldWait })

	edgeProcIsRoger = func(int) (bool, bool) { return false, true }
	_, err = r.run(t, "stop")
	require.ErrorContains(t, err, "not a roger any more")

	edgeProcIsRoger = func(int) (bool, bool) { return true, true }
	edgeSignal = func(int, syscall.Signal) error { return errors.New("no permission") }
	_, err = r.run(t, "stop")
	require.ErrorContains(t, err, "could not ask it to stop")

	modelStopWait = 300 * time.Millisecond
	edgeSignal = func(int, syscall.Signal) error { return nil }
	out, err = r.run(t, "stop")
	require.NoError(t, err)
	require.Contains(t, out, "had not finished")

	edgeSignal = func(int, syscall.Signal) error { return os.Remove(rec) }
	out, err = r.run(t, "stop")
	require.NoError(t, err)
	require.Contains(t, out, "stopped serving tiny")
}

func TestModelRemoveAsksFirstAndGivesTheSpaceBack(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	edgeStdin = strings.NewReader("n\n")
	out, err := r.run(t, "rm", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "kept. Nothing changed.")
	edgeStdin = strings.NewReader("y\n")
	out, err = r.run(t, "rm", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "removed acme/Tiny-GGUF:Q4_K_M")
	_, err = r.run(t, "rm", "tiny")
	require.ErrorIs(t, err, localmodel.ErrUnknownModel)
}

func TestModelUsageAndFlagMistakesAreSaidPlainly(t *testing.T) {
	r := newModelRig(t, 16384)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"files"}, "usage: roger model files"},
		{[]string{"get"}, "usage: roger model get"},
		{[]string{"serve", "a", "b"}, "usage: roger model serve"},
		{[]string{"rm"}, "usage: roger model rm"},
		{[]string{"frobnicate"}, "unknown: roger model frobnicate"},
		{[]string{"serve", "x", "--ctx"}, "--ctx needs a value"},
		{[]string{"serve", "x", "--ctx", "lots"}, "positive number"},
		{[]string{"serve", "x", "--port=-1"}, "positive number"},
		{[]string{"serve", "x", "--colour", "red"}, "positive number"},
		{[]string{"serve", "x", "--colour=3"}, "unknown flag --colour"},
	} {
		_, err := r.run(t, c.args...)
		require.ErrorContains(t, err, c.want, "%v", c.args)
	}
	// superseded 2026-10-11 by founder ruling (short names nearby): a name that is not on this disk
	// is no longer an error; it is looked up nearby, then on the model hub, and nothing is fetched
	// without a yes (features/models/short_names_nearby.feature section 4).
	out, err := r.run(t, "serve", "nothing-here")
	require.ErrorContains(t, err, "no terminal to pick on, so nothing was fetched")
	require.Contains(t, out, "nothing-here is not served nearby; looking it up on the model hub")
}

func TestModelProgressIsBytesRateAndTimeNeverABareFraction(t *testing.T) {
	var buf bytes.Buffer
	old, oldTerm := modelOut, modelOutTerminal
	modelOut, modelOutTerminal = &buf, func() bool { return true } // a line that moves needs a terminal
	t.Cleanup(func() { modelOut, modelOutTerminal = old, oldTerm })
	modelProgress(provision.Progress{})
	require.Empty(t, buf.String(), "nothing to say before the size is known")
	modelProgress(provision.Progress{Done: 1 << 10, Total: 1 << 30, Rate: 1 << 10, Remaining: 3 * time.Hour})
	require.NotContains(t, buf.String(), "left", "an estimate from the first kilobyte is not shown")
	modelProgress(provision.Progress{Done: 512 << 20, Total: 1 << 30, Rate: 40 << 20, Remaining: 13 * time.Second, Resumed: true})
	modelProgressDone()
	s := buf.String()
	require.Contains(t, s, "512.0 MiB of 1.0 GiB")
	require.Contains(t, s, "40.0 MiB/s")
	require.Contains(t, s, "13s left")
	require.Contains(t, s, "resumed")
	require.True(t, strings.HasSuffix(s, "\n"))
}

func TestModelErrorsNameTheFix(t *testing.T) {
	for err, want := range map[error]string{
		provision.ErrNoSpace:     "roger model rm",
		provision.ErrGated:       "HF_TOKEN",
		provision.ErrCorrupt:     "nothing was kept",
		provision.ErrUnreachable: "resumes where it stopped",
		provision.ErrRateLimited: "wait a minute",
	} {
		require.ErrorContains(t, modelFetchErr(err), want)
	}
	for err, want := range map[error]string{
		hfhub.ErrGated:       "HF_TOKEN",
		hfhub.ErrNotFound:    "spelling",
		hfhub.ErrRateLimited: "HF_TOKEN",
		hfhub.ErrUnreachable: "network",
	} {
		require.ErrorContains(t, modelSourceErr(err), want)
	}
	other := errors.New("other")
	require.Equal(t, other, modelFetchErr(other))
	require.Equal(t, other, modelSourceErr(other))
	require.Equal(t, "1.2M", countWord(1_234_567))
	require.Equal(t, "12", countWord(12))
}

func TestModelTheTokenComesFromWhereOwnersAlreadyPutIt(t *testing.T) {
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGING_FACE_HUB_TOKEN", "hf_b")
	require.Equal(t, "hf_b", modelHub().Token)
	t.Setenv("HF_TOKEN", "hf_a")
	require.Equal(t, "hf_a", modelHub().Token)
}

func TestModelADetachedServeIsSettledInFrontOfTheOwnerThenHandedOff(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	oldSpawn, oldWait := modelSpawn, modelDetachWait
	t.Cleanup(func() { modelSpawn, modelDetachWait = oldSpawn, oldWait })

	var got []string
	modelSpawn = func(args []string, logPath string) error {
		got = args
		// The background roger comes on the air: its record appears.
		b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M",
			File: "Tiny-Q4_K_M.gguf", Ctx: 4096, BaseURL: "http://127.0.0.1:1/v1"})
		return os.WriteFile(filepath.Join(r.home.Dir, "serving.json"), b, 0o600)
	}
	out, err := r.run(t, "serve", "tiny", "--ctx", "4096", "--port", "9999", "-d")
	require.NoError(t, err)
	require.Equal(t, []string{"model", "serve", "acme/Tiny-GGUF:Q4_K_M", "--yes", "--ctx", "4096", "--port", "9999", "--verbose"}, got,
		"the background is handed an exact id and every decision already made, and keeps the full account in its log")
	require.Contains(t, out, "keeps serving after this terminal closes")

	// Added beside another in the background: the flag is handed on, and the result says beside what.
	otherModel(t, r)
	modelSpawn = func(args []string, logPath string) error {
		got = args
		b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Port: 7002, Band: "other", ID: "other-Q4_K_M",
			File: "other-Q4_K_M.gguf", Ctx: 4096, BaseURL: "http://127.0.0.1:2/v1", Started: time.Now()})
		return os.WriteFile(filepath.Join(r.home.Dir, "serving-7002.json"), b, 0o600)
	}
	out, err = r.run(t, "serve", "other", "--add", "-d")
	require.NoError(t, err)
	require.Contains(t, got, "--add")
	require.Contains(t, out, `on the air locally beside tiny: "other"`)
	require.NoError(t, os.Remove(filepath.Join(r.home.Dir, "serving-7002.json")))

	// One that never comes on the air says so, with its own last words, and leaves the old one.
	require.NoError(t, os.Remove(filepath.Join(r.home.Dir, "serving.json")))
	modelDetachWait = 300 * time.Millisecond
	modelSpawn = func(args []string, logPath string) error {
		return os.WriteFile(logPath, []byte("loading\nerror: out of memory\n"), 0o600)
	}
	_, err = r.run(t, "serve", "tiny", "--detach")
	require.ErrorContains(t, err, "did not come on the air")
	require.ErrorContains(t, err, "error: out of memory")

	modelSpawn = func([]string, string) error { return errors.New("no fork") }
	_, err = r.run(t, "serve", "tiny", "--detach")
	require.ErrorContains(t, err, "could not start serving in the background")
}

func TestModelStatusShowsAFetchThatDidNotFinish(t *testing.T) {
	r := newModelRig(t, 16384)
	write := func(j localmodel.Job) {
		b, _ := json.Marshal(j)
		require.NoError(t, os.MkdirAll(r.home.Dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "fetch.json"), b, 0o644))
	}
	write(localmodel.Job{Repo: "acme/Tiny-GGUF", File: "Tiny-Q4_K_M.gguf", Total: 2 << 30, Done: 1 << 30, Rate: 40 << 20,
		State: localmodel.JobFetching, PID: os.Getpid(), Updated: time.Now()})
	out, err := r.run(t, "status")
	require.NoError(t, err)
	require.Contains(t, out, "fetching acme/Tiny-GGUF Tiny-Q4_K_M.gguf: 1.0 GiB of 2.0 GiB · 40.0 MiB/s")

	// A second fetch while one runs is refused, saying which one is running.
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q8_0", "--yes")
	require.ErrorIs(t, err, localmodel.ErrBusy)
	require.ErrorContains(t, err, "Tiny-Q4_K_M.gguf")

	write(localmodel.Job{Repo: "acme/Tiny-GGUF", File: "x.gguf", State: localmodel.JobFailed, Err: "not enough free disk"})
	out, _ = r.run(t, "status")
	require.Contains(t, out, "failed: not enough free disk")
	write(localmodel.Job{Repo: "acme/Tiny-GGUF", File: "x.gguf", Total: 10, Done: 5, State: localmodel.JobFetching, PID: 0})
	out, _ = r.run(t, "status")
	require.Contains(t, out, "was interrupted")
}

// local_models.feature §5 "nothing is unloaded by surprise": off by default, set by the owner, and
// when it fires the band is withdrawn and the reason recorded where status and the member's report
// read it.
func TestModelIdleTimeoutIsTheOwnersAndSaysWhyWhenItFires(t *testing.T) {
	r := newModelRig(t, 16384)
	out, err := r.run(t, "idle")
	require.NoError(t, err)
	require.Contains(t, out, "idle timeout: off - a model roger serves keeps serving until it is stopped")
	st, err := r.run(t, "status")
	require.NoError(t, err)
	require.Contains(t, st, "idle timeout: off")

	out, err = r.run(t, "idle", "2h")
	require.NoError(t, err)
	require.Contains(t, out, "idle timeout: 2h0m0s")
	s, _ := r.home.Settings()
	require.Equal(t, 2*time.Hour, s.IdleAfter)
	for _, bad := range []string{"soon", "30s", "-1h"} {
		_, err = r.run(t, "idle", bad)
		require.Error(t, err, bad)
	}
	out, err = r.run(t, "idle", "off")
	require.NoError(t, err)
	require.Contains(t, out, "idle timeout: off")

	// It fires: the runtime's counters stand still past the timeout, the serve ends on its own.
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	require.NoError(t, r.home.SetSettings(localmodel.Settings{IdleAfter: 150 * time.Millisecond}))
	oldEvery := modelIdleEvery
	modelIdleEvery = 20 * time.Millisecond
	t.Cleanup(func() { modelIdleEvery = oldEvery })
	modelUntilDone = func(ctx context.Context) {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Error("the idle timeout never fired")
		}
	}
	out, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "stopped: no turn for 150ms - the owner's idle timeout")
	_, live := r.home.Live()
	require.False(t, live, "the band is withdrawn")
	stop, ok := r.home.LastStop()
	require.True(t, ok)
	require.Equal(t, "tiny", stop.Band)
	st, err = r.run(t, "status")
	require.NoError(t, err)
	require.Contains(t, st, "tiny stopped", "status says what stopped and why")
	require.Contains(t, edgeModelNow().Stopped, "no turn for 150ms", "and so does this member's report")
}

// local_models.feature §6 "models are gigabytes and they accumulate": a budget is the owner's to set,
// nothing is evicted without one, and with one a fetch says first what it will remove.
func TestModelABudgetIsSetByTheOwnerAndAFetchSaysWhatItRemoves(t *testing.T) {
	r := newModelRig(t, 16384)
	out, err := r.run(t, "budget")
	require.NoError(t, err)
	require.Contains(t, out, "budget for models: none - nothing is ever removed to make room")

	out, err = r.run(t, "budget", "20G")
	require.NoError(t, err)
	require.Contains(t, out, "budget for models: 20.0 GiB")
	s, _ := r.home.Settings()
	require.Equal(t, int64(20)<<30, s.DiskBudget)
	for _, bad := range []string{"lots", "-5G", "0.5K"} {
		_, err = r.run(t, "budget", bad)
		require.Error(t, err, bad)
	}

	// Two quants of the tiny repo: fetch one, then a budget that holds only one of them.
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	q4, err := r.home.Find("acme/Tiny-GGUF:Q4_K_M")
	require.NoError(t, err)
	q8 := int64(len(modelGGUF(8000))) // the rig's Q8_0 file
	require.NoError(t, r.home.SetSettings(localmodel.Settings{DiskBudget: q4.Bytes + q8 - 1}))

	edgeStdin = strings.NewReader("n\n")
	out, err = r.run(t, "get", "acme/Tiny-GGUF:Q8_0")
	require.NoError(t, err)
	require.Contains(t, out, "roger removes first, least recently served: acme/Tiny-GGUF:Q4_K_M", "said before the question")
	_, err = r.home.Find("acme/Tiny-GGUF:Q4_K_M")
	require.NoError(t, err, "a No removes nothing")

	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q8_0", "--yes")
	require.NoError(t, err)
	_, err = r.home.Find("acme/Tiny-GGUF:Q4_K_M")
	require.ErrorIs(t, err, localmodel.ErrUnknownModel, "the planned model made room")
	_, err = r.home.Find("acme/Tiny-GGUF:Q8_0")
	require.NoError(t, err)

	ls, err := r.run(t, "list")
	require.NoError(t, err)
	require.Contains(t, ls, "of a ", "the list says the budget beside the usage")

	out, err = r.run(t, "budget", "off")
	require.NoError(t, err)
	require.Contains(t, out, "budget for models: none")
}

// otherModel places a second model by hand, under a band of its own.
func otherModel(t *testing.T, r *modelRig) {
	t.Helper()
	require.NoError(t, os.MkdirAll(r.home.Models(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Models(), "other-Q4_K_M.gguf"), modelGGUF(100), 0o644))
}

// local_models.feature §5 "two at once only when they really fit together": --add serves a second
// beside the first only if both fit by the same arithmetic, otherwise offers the switch; a machine
// serving two says what each is, and stop can take either or both.
func TestModelTwoAtOnceOnlyWhenTheyFitTogether(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	otherModel(t, r)

	calls := 0
	var inner, status, list string
	var served []detect.Endpoint
	modelUntilDone = func(context.Context) {
		calls++
		if calls == 1 {
			_, err := r.run(t, "serve", "other", "--add")
			require.NoError(t, err)
			return
		}
		inner = r.out.String() // the added serve's own words, before the runs below reuse the buffer
		require.Len(t, r.home.LiveAll(), 2, "both are on the air")
		served = detect.Served()
		now := edgeModelNow()
		require.Equal(t, "other", now.Band, "the member's report: the latest")
		require.Len(t, now.Also, 1)
		require.Equal(t, "tiny", now.Also[0].Band, "and the one beside it")
		status, _ = r.run(t, "status")
		list, _ = r.run(t, "list")
	}
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, inner, "beside tiny", "it says it is added, not switched")
	require.NotContains(t, inner, "switched from")
	require.Len(t, served, 2, "detection finds both, each with its own key")
	require.NotEqual(t, served[0].Key, served[1].Key)
	require.Contains(t, status, "serving 2 models")
	require.Contains(t, status, `"tiny"`)
	require.Contains(t, status, `"other"`)
	require.Equal(t, 2, strings.Count(list, "▶ serving"), "the list marks each")

	// They do not fit together on a smaller machine: the estimate (the runtime cannot be asked here)
	// adds both needs, refuses, and offers the switch instead.
	r = newModelRig(t, 4096)
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	otherModel(t, r)
	calls = 0
	var refused error
	modelUntilDone = func(context.Context) {
		calls++
		if calls == 1 {
			_, refused = r.run(t, "serve", "other", "--add")
		}
	}
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Error(t, refused)
	require.Contains(t, refused.Error(), "do not fit together with tiny")
	require.Contains(t, refused.Error(), "roger model serve other-Q4_K_M", "the switch is offered")
	require.Equal(t, 1, calls, "nothing second was started")

	// The same band twice would answer one name with two models: refused.
	r = newModelRig(t, 16384)
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	_, err = r.run(t, "get", "acme/Tiny-GGUF:Q8_0", "--yes")
	require.NoError(t, err)
	calls = 0
	modelUntilDone = func(context.Context) {
		calls++
		if calls == 1 {
			_, refused = r.run(t, "serve", "acme/Tiny-GGUF:Q8_0", "--add")
		}
	}
	_, err = r.run(t, "serve", "acme/Tiny-GGUF:Q4_K_M")
	require.NoError(t, err)
	require.ErrorContains(t, refused, `"tiny" is already on the air`)
}

func TestModelStopTakesOneByNameOrAll(t *testing.T) {
	r := newModelRig(t, 16384)
	out, err := r.run(t, "stop", "nothing-here")
	require.NoError(t, err)
	require.Contains(t, out, "nothing is being served")
	rec := func(port int, band, id string) {
		b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Port: port, Band: band, ID: id, Started: time.Now()})
		require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, fmt.Sprintf("serving-%d.json", port)), b, 0o600))
	}
	require.NoError(t, os.MkdirAll(r.home.Dir, 0o700))
	rec(4001, "a", "x/a:Q4")
	rec(4002, "b", "x/b:Q4")
	_, err = r.run(t, "stop", "c")
	require.ErrorContains(t, err, `"c" is not being served; serving: a, b`)
}

// Review 2026-10-01 findings, each pinned.
func TestModelReviewFindingsStayFixed(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)

	// Adding a model that is already on the air would start a second runtime under one band.
	var refused error
	calls := 0
	modelUntilDone = func(context.Context) {
		calls++
		if calls == 1 {
			_, refused = r.run(t, "serve", "tiny", "--add")
		}
	}
	start := time.Now()
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.ErrorContains(t, refused, "is already on the air")

	// Served is marked when a serve ENDS too, so a model served for a month is not "least recent".
	m, err := r.home.Find("acme/Tiny-GGUF:Q4_K_M")
	require.NoError(t, err)
	require.True(t, m.Served.After(start), "marked when it stopped")

	// An idle timeout turned off reaches a serve already running.
	require.NoError(t, r.home.SetSettings(localmodel.Settings{IdleAfter: 150 * time.Millisecond}))
	oldEvery := modelIdleEvery
	modelIdleEvery = 20 * time.Millisecond
	t.Cleanup(func() { modelIdleEvery = oldEvery })
	fired := false
	modelUntilDone = func(ctx context.Context) {
		require.NoError(t, r.home.SetSettings(localmodel.Settings{}))
		select {
		case <-ctx.Done():
			fired = true
		case <-time.After(600 * time.Millisecond):
		}
	}
	out, err := r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.False(t, fired, "the owner turned it off; the running serve heard")
	require.NotContains(t, out, "no turn for")

	// A band's stop stays visible while another band is on the air.
	require.NoError(t, r.home.RecordStop(localmodel.Stop{Band: "older", At: time.Now(), Why: "no turn for 2h"}))
	modelUntilDone = func(context.Context) {
		st, _ := r.run(t, "status")
		require.Contains(t, st, "older stopped", "status says it while tiny serves")
		require.Contains(t, edgeModelNow().Stopped, "older stopped", "and so does the member's card")
	}
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)

	// "Fits together" refuses when something on the air cannot be judged, rather than leaving it out.
	otherModel(t, r)
	ghost, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Port: 7101, Band: "ghost", ID: "ghost/x:Q4", Started: time.Now()})
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Dir, "serving-7101.json"), ghost, 0o600))
	_, err = r.run(t, "serve", "other", "--add")
	require.ErrorContains(t, err, "cannot judge ghost/x:Q4")
	require.NoError(t, os.Remove(filepath.Join(r.home.Dir, "serving-7101.json")))
}

// Review 2026-10-01: roger's own sum for two applies even when the runtime says yes, because the
// runtime may not count the first model's mapped weights as used.
func TestModelTogetherIsJudgedByRogersSumEvenWhenTheRuntimeSaysYes(t *testing.T) {
	r := newModelRig(t, 4096)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	otherModel(t, r)
	modelFitExe = func(string) string { return os.Args[0] }
	modelServeEnv = map[string]string{"FAKE_LLAMA": "1", "FAKE_FIT": "yes"}
	var refused error
	calls := 0
	modelUntilDone = func(context.Context) {
		calls++
		if calls == 1 {
			_, refused = r.run(t, "serve", "other", "--add")
		}
	}
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.ErrorContains(t, refused, "do not fit together with tiny")
}

// Finding 5b, ruled 2026-10-27: the container limit reaches the SERVE-time verdict. Under a limit
// below roger's sum, the runtime's yes (it counts the host's free memory) does not start the serve;
// with no limit the runtime's yes wins as before.
func TestModelServeHoldsRogersSumAgainstAContainerLimit(t *testing.T) {
	r := newModelRig(t, 2048)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	modelFitExe = func(string) string { return os.Args[0] }
	modelServeEnv = map[string]string{"FAKE_LLAMA": "1", "FAKE_FIT": "yes"}

	modelHW = func() detect.LocalHW { return detect.LocalHW{RAMKnown: true, RAMTotalMiB: 2048, MemLimitMiB: 2048} }
	_, err = r.run(t, "serve", "tiny")
	require.Error(t, err, "the runtime said yes, but roger's sum is over the container's limit")
	require.Contains(t, err.Error(), "memory limit of 2.0 GiB")
	_, live := r.home.Live()
	require.False(t, live, "nothing was started")

	modelHW = func() detect.LocalHW { return detect.LocalHW{RAMKnown: true, RAMTotalMiB: 2048} }
	out, err := r.run(t, "serve", "tiny")
	require.NoError(t, err, "no limit: the runtime's answer wins")
	require.Contains(t, out, "by llama.cpp's own check")
}
