package main

// quiet_output.feature on the paths its runner does not walk: a name served nearby and a pick from
// the model hub say whose word the file is in the default line too, and a fetch into a pipe writes
// each archive's progress once.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/localmodel"
	"rogerai.fm/roger/v7/internal/provision"
	"rogerai.fm/roger/v7/internal/runtime"
)

// quietDo is nearbyRig.do with the default output, not the full account.
func quietDo(t *testing.T, r *nearbyRig, in string, args ...string) string {
	t.Helper()
	edgeStdin = strings.NewReader(in)
	r.out.Reset()
	require.NoError(t, cmdModel(r.cfg(), args))
	return r.out.String()
}

func TestQuietANearbyNameSaysWhoseWordAndWhoseDigestInItsFirstLine(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	out := quietDo(t, r, "n\n", "get", "qwen3-8b")
	require.Contains(t, out, "Qwen/Qwen3-8B-GGUF:Q4_K_M · 4.3 KiB · licence apache-2.0 "+
		"(the author's word, from the model hub; checked against the digest your Edge member atlas reported)")
	require.Contains(t, out, "fits at 8K context")
	require.NotContains(t, out, "licence: apache-2.0", "the full account waits for --verbose")
	require.Contains(t, out, "not fetched. Nothing changed.")
}

func TestQuietAHubPickSaysItIsTheAuthorsWord(t *testing.T) {
	r := newNearbyRig(t, 16384)
	out := quietDo(t, r, "n\n", "serve", "qwen3-8b-mini")
	require.Contains(t, out, "fourth/Qwen3-8B-Mini-GGUF:Q4_K_M · 4.6 KiB · licence apache-2.0 (the author's word, from the model hub)")
	require.Contains(t, out, "fetch it? [y/N]: not fetched. Nothing changed.")
}

func TestQuietProgressIntoAPipeIsOneLinePerArchive(t *testing.T) {
	var buf bytes.Buffer
	old, oldTerm := modelOut, modelOutTerminal
	modelOut, modelOutTerminal = &buf, func() bool { return false }
	t.Cleanup(func() { modelOut, modelOutTerminal = old, oldTerm })

	modelProgressDone() // nothing shown, nothing said
	modelProgress(provision.Progress{Done: 1 << 20, Total: 2 << 20, Rate: 1 << 20, Remaining: time.Second})
	modelProgress(provision.Progress{Done: 2 << 20, Total: 2 << 20, Rate: 2 << 20})
	modelProgress(provision.Progress{Done: 3 << 20, Total: 8 << 20, Resumed: true}) // the next archive
	modelProgress(provision.Progress{Done: 8 << 20, Total: 8 << 20, Rate: 4 << 20, Resumed: true})
	modelProgressDone()

	require.NotContains(t, buf.String(), "\r", "a pipe gets no redraws")
	ls := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	require.Len(t, ls, 2, "one line per archive: %q", buf.String())
	require.Equal(t, "  2.0 MiB of 2.0 MiB  ·  2.0 MiB/s  ·  took 0s", ls[0])
	require.Equal(t, "  8.0 MiB of 8.0 MiB  ·  4.0 MiB/s  ·  took 0s  ·  resumed", ls[1])
}

// quietRig is the feature's Background, for a test the feature does not spell out.
func quietRig(t *testing.T) *quietBDD {
	t.Helper()
	s := &quietBDD{t: t}
	require.NoError(t, s.reset())
	t.Cleanup(func() { _ = os.RemoveAll(s.root) })
	require.NoError(t, s.machine())
	require.NoError(t, s.q4Here(smolQ4ID))
	require.NoError(t, s.q8NotHere(smolQ8ID))
	require.NoError(t, s.cuda())
	return s
}

// The background roger may not start the build the foreground judged: when its record says another
// build serves, the fit line is that build's own, after the line that says it fell back.
func TestQuietADetachedServeThatFellBackSaysWhichBuildServes(t *testing.T) {
	s := quietRig(t)
	modelSpawn = func(args []string, logPath string) error {
		if err := s.spawn(args, logPath); err != nil {
			return err
		}
		p := modelHome().RecordOf(localmodel.Serving{Port: 18932})
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var rec localmodel.Serving
		require.NoError(t, json.Unmarshal(b, &rec))
		rec.Build = "cpu" // the cuda build could not start it, so the background served with the CPU build
		b, _ = json.Marshal(rec)
		return os.WriteFile(p, b, 0o600)
	}
	modelDetachPort(t, 18932)
	require.NoError(t, s.exec("roger model serve smollm2-135m-instruct --detach --port 18932"))
	require.NoError(t, s.err)
	ls := lines(s.out)
	require.Equal(t, "the cuda build could not start it in the background - serving with the cpu build instead (serve.log says why)", ls[0], s.out)
	require.Contains(t, ls[2], "· the cpu build", "the fit line is the build that serves:\n%s", s.out)
	require.NotContains(t, s.out, "the cuda build, ", s.out)
}

// The fit line after a background fallback is a display: it reads what is on this disk and resolves,
// fetches and records nothing. The background roger's own record (auto, the CPU build, and why the GPU
// build did not start) stays as it wrote it, so the next prediction is still the build that serves.
func TestQuietADetachedFallbackDisplayLeavesTheBackgroundsRecord(t *testing.T) {
	s := quietRig(t)
	oldHost, oldLoad := modelHost, modelLoadCheck
	t.Cleanup(func() { modelHost, modelLoadCheck = oldHost, oldLoad })
	modelHost = func() runtime.Host { return hostNVIDIA }
	modelLoadCheck = func(context.Context, string, map[string]string) error { return nil }
	cpu := placeBuild(t, modelHome().Runtime(), runtime.BuildCPU)
	stub := modelRuntimeFor
	resolved := []string{}
	modelRuntimeFor = func(ctx context.Context, choice string) (modelRT, error) {
		resolved = append(resolved, choice)
		if choice == "auto" {
			return stub(ctx, choice) // the foreground judges the cuda build, as the rig has it
		}
		return modelResolveRuntime(ctx, choice) // the real resolver for anything else
	}
	failed := "the cuda build could not start it: exit status 1"
	modelSpawn = func(args []string, logPath string) error {
		if err := s.spawn(args, logPath); err != nil {
			return err
		}
		// What the background roger leaves: the CPU build serves, and its record says why.
		modelRecordSettled(modelSettled{Choice: "auto", Platform: cpu.Platform, Tag: cpu.Tag, Notes: []string{failed}})
		p := modelHome().RecordOf(localmodel.Serving{Port: 18933})
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var rec localmodel.Serving
		require.NoError(t, json.Unmarshal(b, &rec))
		rec.Build = "cpu"
		b, _ = json.Marshal(rec)
		return os.WriteFile(p, b, 0o600)
	}
	modelDetachPort(t, 18933)
	require.NoError(t, s.exec("roger model serve smollm2-135m-instruct --detach --port 18933"))
	require.NoError(t, s.err)
	require.Contains(t, lines(s.out)[2], "· the cpu build", s.out)
	require.NotContains(t, s.out, "fetching llama.cpp", "a display never fetches a runtime")
	st, ok := modelReadSettled()
	require.True(t, ok)
	require.Equal(t, modelSettled{Choice: "auto", Platform: cpu.Platform, Tag: cpu.Tag, Notes: []string{failed}}, st,
		"the background's record is left as it wrote it")
	a, notes, err := modelPredict()
	require.NoError(t, err)
	require.Equal(t, runtime.BuildCPU, a.Build, "the next prediction is the build that serves, not the cuda build that failed")
	require.Equal(t, []string{failed}, notes)
	require.Equal(t, []string{"auto"}, resolved, "the display path resolves nothing")
}

// modelDetachPort makes the stand-in's port the one the command names, so its record is findable.
func modelDetachPort(t *testing.T, port int) {
	t.Helper()
	old := freePortFn
	t.Cleanup(func() { freePortFn = old })
	freePortFn = func() (int, error) { return port, nil }
}

// A serve that fetched the file and then could not go on the air still says what it fetched and
// verified: its line 3, which carries the digest, was never printed.
func TestQuietAServeThatFetchedThenFailedSaysWhatItFetched(t *testing.T) {
	s := quietRig(t)
	s.cudaCannotStart()
	require.NoError(t, modelHome().SetSettings(localmodel.Settings{Runtime: "gpu"}))
	require.NoError(t, s.runsAndAnswers("roger model serve "+smolQ8ID, "y"))
	require.Error(t, s.err)
	require.Contains(t, s.out, "fetched and verified: "+smolQ8ID+" (sha256 "+provision.ShortDigest(smolQ8Digest())+")")
}

// The background roger keeps the full account in serve.log, where a failure is read from.
func TestQuietTheBackgroundServeIsAskedForTheFullAccount(t *testing.T) {
	s := quietRig(t)
	var got []string
	modelSpawn = func(args []string, logPath string) error { got = args; return s.spawn(args, logPath) }
	require.NoError(t, s.exec("roger model serve smollm2-135m-instruct --detach"))
	require.Contains(t, got, "--verbose")
}

// §5 with --verbose: the cuda build's own yes, the line that says it could not start, and a refusal
// that names the CPU build's check.
func TestQuietVerboseContradictionNamesEachBuild(t *testing.T) {
	s := quietRig(t)
	require.NoError(t, s.cudaSaysFits())
	require.NoError(t, s.cudaFailsCPUWillNot())
	require.NoError(t, s.exec("roger model serve smollm2-135m-instruct --ctx 9999999 --verbose"))
	ls := lines(s.out)
	require.True(t, strings.HasPrefix(ls[0], smolQ4ID+": fits at 9999999 context by llama.cpp's own check"), s.out)
	require.True(t, strings.HasPrefix(ls[1], "starting llama.cpp b11146, the cuda build"), s.out)
	require.True(t, strings.HasPrefix(ls[2], "the cuda build could not start it: "), s.out)
	require.True(t, strings.HasSuffix(ls[2], " - serving with the CPU build instead"), s.out)
	require.True(t, strings.HasPrefix(s.errLine, "error: with the CPU build: will not fit at 9999999 context by llama.cpp's own check"), s.errLine)
}

// The nearby name whose file is not the one the member serves (another quantisation) is the hub
// listing's, and its default line says so.
func TestQuietANearbyNameAtAnotherQuantSaysItIsTheHubsListing(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	out := quietDo(t, r, "n\n", "get", "qwen3-8b", "--quant", "Q8_0")
	require.Contains(t, out, "Qwen/Qwen3-8B-GGUF:Q8_0 · ")
	require.Contains(t, out, "(the author's word, from the model hub)")
	require.NotContains(t, out, "reported)", "the member's digest is not this file's")
}

// A file roger cannot judge is served on the owner's word, said as one sentence with its build.
func TestQuietAFileThatCannotBeJudgedReadsAsOneSentence(t *testing.T) {
	s := quietRig(t)
	junk := filepath.Join(s.root, "junk.gguf")
	require.NoError(t, os.WriteFile(junk, []byte("not a gguf"), 0o600))
	require.NoError(t, s.exec("roger model serve "+junk))
	l := lines(s.out)[1]
	require.True(t, strings.HasPrefix(l, "cannot judge the fit ("), l)
	require.True(t, strings.HasSuffix(l, "); serving on your word · the cuda build, on CUDA0") ||
		strings.HasSuffix(l, "); serving on your word · the cuda build"), l)
}
