package localmodel

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/catalog"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/hfhub"
	"rogerai.fm/roger/v6/internal/provision"
)

// TestMain lets this binary stand in for the runtime, reading the SAME command line the real one
// is given, so the flags are proven against a consumer rather than only listed.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_LLAMA") != "" {
		fakeLlama(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func fakeLlama(args []string) {
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
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if key == "" || r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, flag["--alias"])
	})
	if flag["--host"] != "127.0.0.1" {
		os.Exit(4) // it must never be asked to listen beyond loopback
	}
	_ = http.ListenAndServe("127.0.0.1:"+flag["--port"], mux)
}

func put(t *testing.T, p string, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// =========================================================================
// The disk
// =========================================================================

func TestTheDiskListsFetchedAndFoundModelsAndNothingElse(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	fetched := filepath.Join(h.Models(), "unsloth", "Qwen3-4B-GGUF", "Qwen3-4B-Q4_K_M.gguf")
	put(t, fetched, "weights")
	require.NoError(t, h.record(Model{ID: "unsloth/Qwen3-4B-GGUF:Q4_K_M", Repo: "unsloth/Qwen3-4B-GGUF",
		File: "Qwen3-4B-Q4_K_M.gguf", Path: fetched, Bytes: 7, SHA256: "ab", Quant: "Q4_K_M", Band: "qwen3-4b"}))
	put(t, filepath.Join(h.Models(), "gemma-3-1b-it-Q8_0.gguf"), "dropped in by hand")
	put(t, filepath.Join(h.Models(), "x", "y", "mmproj-F16.gguf"), "projector")
	put(t, filepath.Join(h.Models(), "big-00001-of-00002.gguf"), "part")
	put(t, filepath.Join(h.Models(), "half.gguf.part"), "partial")
	put(t, filepath.Join(h.Models(), "notes.txt"), "no")

	all, err := h.List()
	require.NoError(t, err)
	require.Len(t, all, 2, "only whole servable models are models: %+v", all)

	byID := map[string]Model{}
	for _, m := range all {
		byID[m.ID] = m
	}
	f := byID["unsloth/Qwen3-4B-GGUF:Q4_K_M"]
	require.True(t, f.Verified())
	require.Equal(t, fetched, f.Path, "the path is where it IS, not what the record says")
	g := byID["gemma-3-1b-it-Q8_0"]
	require.False(t, g.Verified(), "a file nobody checked is not presented as checked")
	require.Equal(t, "Q8_0", g.Quant)
	require.Equal(t, "gemma-3-1b-it", g.Band)

	n, count, err := h.Usage()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, int64(7+len("dropped in by hand")), n)
}

func TestAnEmptyOrMissingModelsDirectoryIsNotAnError(t *testing.T) {
	all, err := Home{Dir: filepath.Join(t.TempDir(), "never")}.List()
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestFindingAModelByWhatAnOwnerWouldType(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	for _, f := range []string{"a/Qwen3-4B-GGUF/Qwen3-4B-Q4_K_M.gguf", "a/Qwen3-4B-GGUF/Qwen3-4B-Q8_0.gguf", "solo-Q4_0.gguf"} {
		put(t, filepath.Join(h.Models(), f), "w")
	}
	m, err := h.Find("solo-Q4_0")
	require.NoError(t, err)
	require.Equal(t, "solo-Q4_0", m.ID)

	m, err = h.Find("SOLO")
	require.NoError(t, err, "the band alone names it when it names one")
	require.Equal(t, "solo-Q4_0", m.ID)

	_, err = h.Find("qwen3-4b")
	require.Error(t, err)
	require.Contains(t, err.Error(), "matches 2 models", "ambiguity is never resolved silently")

	_, err = h.Find("nope")
	require.ErrorIs(t, err, ErrUnknownModel)
}

func TestRemovingAModelGivesTheSpaceBackAndTouchesNothingElse(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	p := filepath.Join(h.Models(), "o", "r", "m-Q4_K_M.gguf")
	put(t, p, "w")
	put(t, p+recordExt, `{"id":"o/r:Q4_K_M"}`)
	keep := filepath.Join(h.Models(), "o", "other.gguf")
	put(t, keep, "w")
	m, err := h.Find("o/r:Q4_K_M")
	require.NoError(t, err)
	require.NoError(t, h.Remove(m))
	for _, gone := range []string{p, p + recordExt, filepath.Dir(p)} {
		_, err := os.Stat(gone)
		require.True(t, os.IsNotExist(err), "%s is still there", gone)
	}
	_, err = os.Stat(keep)
	require.NoError(t, err, "a neighbour was removed too")

	outside := filepath.Join(t.TempDir(), "mine.gguf")
	put(t, outside, "w")
	require.Error(t, h.Remove(Model{ID: "mine", Path: outside}), "roger deleted a file outside its own directory")
	_, err = os.Stat(outside)
	require.NoError(t, err)
}

func TestANameFromOutsideCannotClimbOutOfTheModelsDirectory(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	for _, c := range []struct{ repo, file string }{
		{"../etc", "x.gguf"}, {"owner", "x.gguf"}, {"a/b/c", "x.gguf"}, {"/abs/olute", "x.gguf"},
		{"a/b", "../../x.gguf"}, {"a/b", "/etc/passwd"}, {"a/b", "sub/../../../x.gguf"},
	} {
		_, err := h.PathFor(c.repo, c.file)
		require.Error(t, err, "%s %s was accepted", c.repo, c.file)
	}
	p, err := h.PathFor("unsloth/Qwen3-4B-GGUF", "Q4_K_M/model.gguf")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(p, h.Models()))
}

func TestTheBandIsTheModelNotItsPackaging(t *testing.T) {
	for in, want := range map[string]string{
		"unsloth/Qwen3-4B-GGUF":       "qwen3-4b",
		"Qwen3-4B-Q4_K_M":             "qwen3-4b",
		"gemma-3-1b-it-Q8_0.gguf":     "gemma-3-1b-it",
		"bartowski/Llama-3.2-1B-GGUF": "llama-3.2-1b",
		"plainname":                   "plainname",
	} {
		require.Equal(t, want, BandFor(in), in)
	}
}

func TestFilesThatAreNotAModelOnTheirOwnSaySo(t *testing.T) {
	require.Contains(t, Unservable("mmproj-model-f16.gguf"), "projector")
	require.Contains(t, Unservable("Q8_0/big-00002-of-00003.gguf"), "split")
	require.Contains(t, Unservable("imatrix.gguf"), "calibration")
	require.Empty(t, Unservable("Qwen3-4B-Q4_K_M.gguf"))
}

// =========================================================================
// The arithmetic
// =========================================================================

const gib = int64(1) << 30

func qwen(file int64) catalog.Shape {
	// Qwen3-4B's real header: 36 layers, 8 KV heads, 128-wide heads, 40960 trained context.
	return catalog.Shape{FileBytes: file, Layers: 36, KVHeads: 8, KeyDim: 128, ValueDim: 128, TrainCtx: 40960, Vocab: 151936}
}

func TestTheBudgetIsThePoolTheModelWillLiveIn(t *testing.T) {
	b := BudgetFor(detect.LocalHW{UnifiedMemory: true, RAMKnown: true, RAMTotalMiB: 16384, VRAMKnown: true, VRAMTotalMiB: 999}, true)
	require.Equal(t, "unified memory", b.Pool)
	require.Equal(t, 16*gib, b.Bytes, "one pool is measured as one pool")

	b = BudgetFor(detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 8192, RAMKnown: true, RAMTotalMiB: 65536}, true)
	require.Equal(t, "video memory", b.Pool)
	require.Equal(t, 8*gib, b.Bytes)

	b = BudgetFor(detect.LocalHW{RAMKnown: true, RAMTotalMiB: 4096}, true)
	require.Equal(t, "system memory", b.Pool)

	var hw detect.LocalHW
	hw.Note("could not read /proc/meminfo")
	b = BudgetFor(hw, true)
	require.False(t, b.Known())
	require.Contains(t, b.Why, "/proc/meminfo", "the missing reading is named")
}

func TestAVerdictAlwaysNamesItsContext(t *testing.T) {
	pi := Budget{Bytes: 4 * gib, Pool: "system memory"}
	v := Judge(qwen(2500<<20), pi, 0)
	require.Equal(t, DefaultCtx, v.Ctx)
	require.Contains(t, v.Line(), "8K context")
	require.Contains(t, v.Sum(), "weights")
	require.Contains(t, v.Sum(), "cache")

	// The full trained context is what kills a small board, and the verdict says where it fits.
	v = Judge(qwen(2500<<20), pi, 40960)
	require.Equal(t, catalog.FitWontFit, v.Fit)
	require.Positive(t, v.MaxCtx)
	require.Less(t, v.MaxCtx, 40960)
	require.Contains(t, v.Line(), "will not fit at 40K context")
	require.Contains(t, v.Line(), "fits at")
	require.Contains(t, v.Short(), "will not fit at 40K - fits at")
	require.Contains(t, Judge(qwen(2500<<20), pi, 2048).Short(), "at 2K - needs")
	require.Equal(t, "cannot say", Judge(qwen(1), Budget{}, 0).Short())
	require.Contains(t, Judge(qwen(8*gib), pi, 4096).Short(), "will not fit - needs")

	// A model trained for less than the default is proposed at what it was trained for.
	s := qwen(1 << 30)
	s.TrainCtx = 2048
	require.Equal(t, 2048, Judge(s, pi, 0).Ctx)
	require.Contains(t, Judge(s, pi, 4096).Why, "trained for 2048")
}

func TestFourVerdictsAndEachMeansSomethingDifferent(t *testing.T) {
	big := Budget{Bytes: 64 * gib, Pool: "system memory"}
	require.Equal(t, catalog.FitFits, Judge(qwen(2*gib), big, 4096).Fit)

	need := Judge(qwen(2*gib), big, 4096).Need.Total
	tight := Budget{Bytes: need + need/10, Pool: "system memory"}
	require.Equal(t, catalog.FitTight, Judge(qwen(2*gib), tight, 4096).Fit)

	tiny := Budget{Bytes: gib, Pool: "system memory"}
	v := Judge(qwen(2*gib), tiny, 4096)
	require.Equal(t, catalog.FitWontFit, v.Fit)
	require.Zero(t, v.MaxCtx)
	require.Contains(t, v.Line(), "needs")

	v = Judge(qwen(2*gib), Budget{Why: "memory could not be read"}, 4096)
	require.Equal(t, catalog.FitUnknown, v.Fit)
	require.Contains(t, v.Line(), "cannot say - memory could not be read")

	v = Judge(catalog.Shape{FileBytes: gib}, big, 4096)
	require.Equal(t, catalog.FitUnknown, v.Fit, "a header missing a term is never a confident answer")
	require.Empty(t, v.Sum())
}

func TestOneRepoIsNotOneModel(t *testing.T) {
	files := []hfhub.File{
		{Path: "m-Q2_K.gguf", Bytes: 1 * gib, Quant: "Q2_K"},
		{Path: "m-Q4_K_M.gguf", Bytes: 2 * gib, Quant: "Q4_K_M"},
		{Path: "m-Q8_0.gguf", Bytes: 4 * gib, Quant: "Q8_0"},
		{Path: "mmproj-F16.gguf", Bytes: gib / 2, Quant: "F16"},
	}
	cs := Rank(files, qwen(0), Budget{Bytes: 32 * gib, Pool: "video memory"}, 4096)
	require.Len(t, cs, 4)
	require.NotEqual(t, cs[0].Verdict.Need.Weights, cs[2].Verdict.Need.Weights, "each quant is judged at its own size")
	require.NotEmpty(t, cs[3].Unservable)
	require.Equal(t, 1, Suggest(cs), "Q4_K_M when it fits comfortably")

	// Without a Q4_K_M, the largest that fits comfortably.
	require.Equal(t, 1, Suggest(Rank([]hfhub.File{files[0], files[2]}, qwen(0), Budget{Bytes: 32 * gib}, 4096)))

	// Only tight ones: the largest tight one.
	need := Judge(qwen(4*gib), Budget{Bytes: 64 * gib}, 4096).Need.Total
	cs = Rank([]hfhub.File{files[2]}, qwen(0), Budget{Bytes: need + need/20}, 4096)
	require.Equal(t, catalog.FitTight, cs[0].Verdict.Fit)
	require.Equal(t, 0, Suggest(cs))

	// Nothing fits: nothing is suggested.
	require.Equal(t, -1, Suggest(Rank(files, qwen(0), Budget{Bytes: gib / 4}, 4096)))
}

func TestPickTakesAQuantOrAPathAndNeverGuesses(t *testing.T) {
	files := []hfhub.File{{Path: "a/m-Q4_K_M.gguf", Quant: "Q4_K_M"}, {Path: "b/m-Q4_K_M.gguf", Quant: "Q4_K_M"}, {Path: "m-Q8_0.gguf", Quant: "Q8_0"}, {Path: "mmproj-Q8_0.gguf", Quant: "Q8_0"}}
	f, err := Pick(files, "q8_0")
	require.NoError(t, err, "a projector sharing the quant is not a second match")
	require.Equal(t, "m-Q8_0.gguf", f.Path)
	f, err = Pick(files, "b/m-Q4_K_M.gguf")
	require.NoError(t, err)
	require.Equal(t, "b/m-Q4_K_M.gguf", f.Path)
	_, err = Pick(files, "Q4_K_M")
	require.ErrorContains(t, err, "names 2 files")
	f, err = Pick(files, "m-Q8_0")
	require.NoError(t, err, "the label a screen shows is a name Pick takes")
	require.Equal(t, "m-Q8_0.gguf", f.Path)
	_, err = Pick(files, "Q5_K_M")
	require.ErrorContains(t, err, "no servable file")
}

// =========================================================================
// Fetching
// =========================================================================

func hub(t *testing.T, files map[string][]byte, gated bool) *hfhub.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gated && r.Header.Get("Authorization") == "" {
			w.Header().Set("X-Error-Code", "GatedRepo")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		b, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return &hfhub.Client{Base: srv.URL}
}

func fileFor(name string, body []byte) hfhub.File {
	sum := sha256.Sum256(body)
	return hfhub.File{Path: name, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Quant: detect.QuantInName(name)}
}

func roomy() *provision.Fetcher {
	return &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1 << 40, true }}
}

func TestAFetchedModelIsVerifiedRecordedAndThenOnTheList(t *testing.T) {
	body := []byte("gguf weights")
	h := Home{Dir: t.TempDir()}
	m, err := h.Get(context.Background(), hub(t, map[string][]byte{"Tiny-Q4_K_M.gguf": body}, false),
		roomy(), "acme/Tiny-GGUF", fileFor("Tiny-Q4_K_M.gguf", body), "apache-2.0")
	require.NoError(t, err)
	require.Equal(t, "acme/Tiny-GGUF:Q4_K_M", m.ID)
	require.Equal(t, "tiny", m.Band)
	require.Equal(t, "apache-2.0", m.License, "the licence is recorded against the file")

	got, err := h.Find("tiny")
	require.NoError(t, err)
	require.Equal(t, m.ID, got.ID)
	require.True(t, got.Verified())
}

func TestAFetchThatFailsLeavesNoModelBehind(t *testing.T) {
	body := []byte("gguf weights")
	h := Home{Dir: t.TempDir()}
	bad := fileFor("Tiny-Q4_K_M.gguf", body)
	bad.SHA256 = strings.Repeat("0", 64)
	_, err := h.Get(context.Background(), hub(t, map[string][]byte{"Tiny-Q4_K_M.gguf": body}, false), roomy(), "acme/Tiny-GGUF", bad, "")
	require.ErrorIs(t, err, provision.ErrCorrupt)
	all, _ := h.List()
	require.Empty(t, all, "a file that failed its digest is listed as a model")

	_, err = h.Get(context.Background(), hub(t, nil, true), roomy(), "acme/Gated-GGUF", fileFor("G-Q4_0.gguf", body), "")
	require.ErrorIs(t, err, provision.ErrGated, "the far end refusing is told apart from a broken network")

	_, err = h.Get(context.Background(), hub(t, nil, false), roomy(), "acme/Tiny-GGUF", fileFor("mmproj-F16.gguf", body), "")
	require.ErrorContains(t, err, "projector")
	_, err = h.Get(context.Background(), hub(t, nil, false), roomy(), "../../etc", fileFor("x.gguf", body), "")
	require.Error(t, err)
}

// =========================================================================
// Serving
// =========================================================================

func servable(t *testing.T, h Home) Model {
	p := filepath.Join(h.Models(), "acme", "Tiny-GGUF", "Tiny-Q4_K_M.gguf")
	put(t, p, "weights")
	return Model{ID: "acme/Tiny-GGUF:Q4_K_M", File: "Tiny-Q4_K_M.gguf", Path: p, Band: "tiny", Quant: "Q4_K_M", SHA256: "ab"}
}

func TestServingStartsTheRuntimeOnLoopbackWithAKeyAndRecordsIt(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	srv, s, err := h.Serve(context.Background(), ServeOpts{Model: servable(t, h), Exe: os.Args[0], Tag: "bX",
		Budget: Budget{Bytes: 4 * gib}, ReadyWithin: 10 * time.Second, Env: map[string]string{"FAKE_LLAMA": "1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	require.Equal(t, "tiny", s.Band)
	require.Equal(t, DefaultCtx, s.Ctx)
	require.Equal(t, "bX", s.Runtime)
	require.Contains(t, s.BaseURL, "127.0.0.1")
	require.NotEmpty(t, s.Key)

	// The record holds the key, so only the owner may read it.
	fi, err := os.Stat(h.recordPath(s.Port))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	live, ok := h.Live()
	require.True(t, ok)
	require.Equal(t, s.Key, live.Key)

	// A caller without the key is refused by the runtime itself.
	resp, err := http.Get(s.BaseURL + "/models")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// The key never appears on the command line, where every user can read it.
	for _, a := range Args(ServeOpts{Model: servable(t, h)}, 1, "/k") {
		require.NotEqual(t, s.Key, a)
	}

	h.Release(s)
	_, ok = h.Live()
	require.False(t, ok)
	_, err = os.Stat(h.keyPath(s.Port))
	require.True(t, os.IsNotExist(err), "the key file outlived the serving")
}

func TestARecordLeftByARogerThatDiedIsNotBelieved(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	require.NoError(t, h.writeServing(Serving{Supervisor: 424242, Band: "x"}))
	defer func(f func(int) bool) { alive = f }(alive)
	alive = func(int) bool { return false }
	_, ok := h.Live()
	require.False(t, ok, "a dead supervisor's band was reported as served")
	alive = func(int) bool { return true }
	_, ok = h.Live()
	require.True(t, ok)
}

func TestAnOldSupervisorDoesNotEraseItsSuccessor(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	require.NoError(t, h.writeServing(Serving{Supervisor: 2, Port: 2002, Band: "new"}))
	h.Release(Serving{Supervisor: 1, Port: 2001}) // the old one leaving after a switch
	b, err := os.ReadFile(h.recordPath(2002))
	require.NoError(t, err)
	require.Contains(t, string(b), `"new"`)
}

func TestAModelThatFailsToStartLeavesNoRecordAndNoKey(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	_, _, err := h.Serve(context.Background(), ServeOpts{Model: servable(t, h), Exe: filepath.Join(t.TempDir(), "absent"),
		Budget: Budget{Bytes: gib}, ReadyWithin: time.Second})
	require.Error(t, err)
	_, ok := h.Live()
	require.False(t, ok)
	keys, _ := filepath.Glob(filepath.Join(h.Dir, "serving-*.key"))
	require.Empty(t, keys)
}

func TestThePromptCacheIsSizedToTheMachine(t *testing.T) {
	require.Equal(t, int64(256), cacheRAMMiB(Budget{Bytes: 2 * gib}), "a Pi gets a floor, not the server default")
	require.Equal(t, int64(1024), cacheRAMMiB(Budget{Bytes: 16 * gib}))
	require.Equal(t, int64(8192), cacheRAMMiB(Budget{Bytes: 1024 * gib}))
	args := strings.Join(Args(ServeOpts{Model: Model{Path: "/m", Band: "b"}, Ctx: 4096, Budget: Budget{Bytes: 2 * gib}}, 9, "/k"), " ")
	for _, want := range []string{"--parallel 1", "--cache-ram 256", "--host 127.0.0.1", "--no-webui", "-c 4096", "--alias b"} {
		require.Contains(t, args, want)
	}
}

func TestQuantsThatReadAlikeAreNamedByTheirFile(t *testing.T) {
	cs := []Choice{
		{File: hfhub.File{Path: "m-Q4_0.gguf", Quant: "Q4_0"}},
		{File: hfhub.File{Path: "sub/m-Q4_0_4_4.gguf", Quant: "Q4_0"}},
		{File: hfhub.File{Path: "m-Q8_0.gguf", Quant: "Q8_0"}},
		{File: hfhub.File{Path: "m-odd.gguf"}},
	}
	require.Equal(t, []string{"m-Q4_0", "m-Q4_0_4_4", "Q8_0", "m-odd"}, Labels(cs))
}

func TestSeveralCardsAreSaidToBeASum(t *testing.T) {
	b := BudgetFor(detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 49152, GPUs: make([]detect.LocalGPU, 2)}, true)
	require.Equal(t, "video memory across 2 GPUs", b.Pool)
}

// Review 2026-10-01: two files of one quantisation from one repo never share an id, so a serve or a
// remove acts on the file the owner meant.
func TestTwoFilesOfOneQuantNeverShareAnID(t *testing.T) {
	body := []byte("gguf weights")
	h := Home{Dir: t.TempDir()}
	files := map[string][]byte{"m-Q4_K_M.gguf": body}
	hb := hub(t, files, false)
	a, err := h.Get(context.Background(), hb, roomy(), "acme/M-GGUF", fileFor("a/m-Q4_K_M.gguf", body), "")
	require.NoError(t, err)
	b, err := h.Get(context.Background(), hb, roomy(), "acme/M-GGUF", fileFor("b/m-Q4_K_M.gguf", body), "")
	require.NoError(t, err)
	require.Equal(t, "acme/M-GGUF:Q4_K_M", a.ID)
	require.Equal(t, "acme/M-GGUF:b/m-Q4_K_M.gguf", b.ID)
	got, err := h.Find(b.ID)
	require.NoError(t, err)
	require.Equal(t, "b/m-Q4_K_M.gguf", got.File)
}

// local_models.feature §5 "two at once only when they really fit together": each serve keeps its own
// record, so two can be on the air and each is said in full.
func TestTwoServesAreTwoRecordsAndEachIsSaid(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	defer func(f func(int) bool) { alive = f }(alive)
	alive = func(int) bool { return true }
	// The pids below are made up, and a real process may hold one of them (a kernel thread took
	// pid 10 on one machine). The reused-pid check is the stale-record test's business; here every
	// record's supervisor is the process that wrote it.
	defer func(f func(int) (time.Time, bool, bool)) { procStart = f }(procStart)
	procStart = func(int) (time.Time, bool, bool) { return time.Time{}, false, false }
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	second := Serving{Supervisor: 11, Port: 3011, Band: "second", ID: "a/second:Q4", Started: t0.Add(time.Minute)}
	require.NoError(t, h.writeServing(second))
	require.NoError(t, h.writeServing(Serving{Supervisor: 10, Port: 3010, Band: "first", ID: "a/first:Q4", Started: t0}))

	all := h.LiveAll()
	require.Len(t, all, 2)
	require.Equal(t, []string{"first", "second"}, []string{all[0].Band, all[1].Band}, "in the order they went on the air")
	s, ok := h.Live()
	require.True(t, ok)
	require.Equal(t, "second", s.Band, "Live is the one that went on the air last")

	h.Release(second)
	all = h.LiveAll()
	require.Len(t, all, 1)
	require.Equal(t, "first", all[0].Band, "one leaving does not take the other with it")

	// A record from a roger before this change is still read and still released.
	b, _ := json.Marshal(Serving{Supervisor: 9, Band: "legacy", Started: t0.Add(-time.Hour)})
	require.NoError(t, os.WriteFile(filepath.Join(h.Dir, "serving.json"), b, 0o600))
	require.Len(t, h.LiveAll(), 2)
	h.Release(Serving{Supervisor: 9})
	require.Len(t, h.LiveAll(), 1)
	_, err := os.Stat(filepath.Join(h.Dir, "serving.json"))
	require.True(t, os.IsNotExist(err))

	alive = func(pid int) bool { return pid != 10 }
	require.Empty(t, h.LiveAll(), "a dead supervisor's record is not believed")
}

func TestWhetherAServeOrAModelIsStillOnTheAir(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	defer func(f func(int) bool) { alive = f }(alive)
	alive = func(int) bool { return true }
	a := Serving{Supervisor: 10, Port: 3010, Band: "a", ID: "x/a:Q4", File: "a.gguf"}
	require.NoError(t, h.writeServing(a))
	require.True(t, h.StillServing(a))
	require.False(t, h.StillServing(Serving{Supervisor: 10, Port: 3099}))
	s, ok := h.ServingModel(Model{ID: "x/a:Q4", File: "a.gguf"})
	require.True(t, ok)
	require.Equal(t, "a", s.Band)
	_, ok = h.ServingModel(Model{ID: "x/b:Q4", File: "b.gguf"})
	require.False(t, ok)
}

// Review 2026-10-01: a port is claimed exclusively before its key is written, so two serves that
// picked the same port cannot share it; a claim left by a roger that died is stale and taken.
func TestAPortIsClaimedExclusively(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	require.NoError(t, os.MkdirAll(h.Dir, 0o700))
	release, err := h.claimPort(7001, Model{ID: "a/a:Q4"})
	require.NoError(t, err)
	_, err = h.claimPort(7001, Model{ID: "b/b:Q4"})
	require.Error(t, err, "a live claim is not shared")
	require.Equal(t, []string{"a/a:Q4"}, h.loadingIDs(), "and the model it is loading is known")
	release()
	require.Empty(t, h.loadingIDs())

	defer func(f func(int) bool) { alive = f }(alive)
	alive = func(int) bool { return true }
	require.NoError(t, h.writeServing(Serving{Supervisor: 99, Port: 7001, Band: "x"}))
	_, err = h.claimPort(7001, Model{ID: "c/c:Q4"})
	require.Error(t, err, "nor is a port a live serve holds")
	require.NoError(t, os.Remove(h.recordPath(7001)))

	stale, err := h.claimPort(7002, Model{ID: "d/d:Q4"})
	require.NoError(t, err)
	_ = stale // its roger "dies" without releasing
	alive = func(int) bool { return false }
	again, err := h.claimPort(7002, Model{ID: "e/e:Q4"})
	require.NoError(t, err, "a claim nobody alive holds is stale and taken")
	again()
}
