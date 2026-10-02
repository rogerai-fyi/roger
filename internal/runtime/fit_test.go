package runtime_test

// THE RUNTIME'S OWN FITNESS ANSWER WINS ONCE THE FILE IS ON DISK (local_models.feature §4). The
// fake below prints exactly what llama-fit-params b11146 printed on a real machine:
//
//	$ llama-fit-params -m Qwen3-0.6B-Q4_K_M.gguf -c 8192            -> stdout "-c 8192 -ngl -1", exit 0
//	$ llama-fit-params -m ... -c 8192 -fitp on                      -> stdout "Host 167 896 300", exit 0
//	$ llama-fit-params -m ... -c 40960 -fitt 130000                 -> exit 1, stderr:
//	  W common_fit_params: failed to fit params to free device memory: was unable to fit model into
//	    system memory by reducing context, abort
//	  E llama_fit_params: failed to fit CLI arguments to free memory, exiting...

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/runtime"
)

func fakeFitParams(mode string) {
	args := os.Args[1:]
	flag := map[string]string{}
	for i := 0; i < len(args); i++ {
		if i+1 < len(args) {
			flag[args[i]] = args[i+1]
		}
	}
	if mode == "fit-old" {
		fmt.Fprintln(os.Stderr, "error: invalid argument: -fitp")
		os.Exit(1)
	}
	if _, err := os.Stat(flag["-m"]); err != nil {
		fmt.Fprintln(os.Stderr, "E llama_model_load: failed to open "+flag["-m"])
		os.Exit(1)
	}
	c, hasC := 0, false
	if v, ok := flag["-c"]; ok {
		c, _ = strconv.Atoi(v)
		hasC = true
	}
	if flag["-fitp"] == "on" {
		if !hasC {
			c = 40960
		}
		fmt.Fprintln(os.Stderr, "0.00.005.328 I llama_fit_params: printing estimated memory in MiB to stdout (device, model, context, compute) ...")
		fmt.Printf("Host 167 %d 300\n", c*896/8192)
		return
	}
	limit := map[string]int{"fit-ok": 1 << 30, "fit-cap": 16384, "fit-none": 0}[mode]
	if hasC && c <= limit || !hasC && limit >= 4096 {
		if !hasC {
			c = min(limit, 40960)
		}
		fmt.Fprintln(os.Stderr, "0.00.172.409 I llama_fit_params: printing fitted CLI arguments to stdout...")
		fmt.Printf("-c %d -ngl -1\n", c)
		return
	}
	fmt.Fprintln(os.Stderr, "0.00.150.671 W common_fit_params: failed to fit params to free device memory: was unable to fit model into system memory by reducing context, abort")
	fmt.Fprintln(os.Stderr, "0.00.150.682 E llama_fit_params: failed to fit CLI arguments to free memory, exiting...")
	os.Exit(1)
}

func fitSpec(t *testing.T, mode string, ctx int) runtime.FitSpec {
	t.Helper()
	model := filepath.Join(t.TempDir(), "model.gguf")
	require.NoError(t, os.WriteFile(model, []byte("weights"), 0o644))
	return runtime.FitSpec{Exe: os.Args[0], Model: model, Ctx: ctx, Env: map[string]string{"FAKE_RUNTIME": mode}}
}

func TestTheRuntimeIsAskedWhetherAModelOnDiskFits(t *testing.T) {
	f, err := runtime.Fit(context.Background(), fitSpec(t, "fit-ok", 8192))
	require.NoError(t, err)
	require.True(t, f.Fits)
	require.Equal(t, 8192, f.Ctx)
	require.Equal(t, []runtime.DeviceUse{{Name: "Host", Model: 167 << 20, Context: 896 << 20, Compute: 300 << 20}}, f.Devices,
		"its own breakdown, so the owner can check the sum it judged")
}

func TestARuntimeNoIsTakenWithItsReasonAndTheContextThatDoesFit(t *testing.T) {
	f, err := runtime.Fit(context.Background(), fitSpec(t, "fit-cap", 40960))
	require.NoError(t, err)
	require.False(t, f.Fits)
	require.Equal(t, 40960, f.Ctx, "the answer is AT the context asked; nothing is silently reduced")
	require.Equal(t, "was unable to fit model into system memory by reducing context", f.Said)
	require.Equal(t, 16384, f.MaxCtx, "and it says where it would fit, by asking the runtime again")
	require.Equal(t, int64(40960*896/8192)<<20, f.Devices[0].Context)

	f, err = runtime.Fit(context.Background(), fitSpec(t, "fit-none", 4096))
	require.NoError(t, err)
	require.False(t, f.Fits)
	require.Zero(t, f.MaxCtx, "nothing fits at any context the runtime would choose")
}

// A runtime that cannot answer is an error, never a "no": the caller falls back to its own
// estimate and says so, rather than refusing a model because a tool was missing.
func TestARuntimeThatCannotAnswerIsAnErrorNotAVerdict(t *testing.T) {
	_, err := runtime.Fit(context.Background(), fitSpec(t, "fit-old", 8192))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid argument")

	sp := fitSpec(t, "fit-ok", 8192)
	sp.Model = filepath.Join(t.TempDir(), "absent.gguf")
	_, err = runtime.Fit(context.Background(), sp)
	require.Error(t, err)

	sp = fitSpec(t, "fit-ok", 8192)
	sp.Exe = filepath.Join(t.TempDir(), "llama-fit-params")
	_, err = runtime.Fit(context.Background(), sp)
	require.Error(t, err)
}

func TestTheFitToolSitsBesideTheServer(t *testing.T) {
	require.Equal(t, filepath.Join("a", "b", "llama-fit-params"), runtime.FitExe(filepath.Join("a", "b", "llama-server")))
	require.Equal(t, filepath.Join("a", "llama-fit-params.exe"), runtime.FitExe(filepath.Join("a", "llama-server.exe")))
}
