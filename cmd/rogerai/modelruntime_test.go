package main

// A GPU BUILD AND A CPU BUILD (features/serving/local_models.feature §10, approved 2026-10-02 with
// CUDA as well as Vulkan): the owner's choice, the build each machine resolves it to, the build's own
// answer about its devices, and the fallback under auto.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/runtime"
)

var hostNVIDIA = runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true, GPU: true, NVIDIA: true, CUDAMajor: 13, CUDAMinor: 2}

// placeBuild puts a verified tree for one build where InstalledBuild finds it, as if fetched.
func placeBuild(t *testing.T, dir string, b runtime.Build) runtime.Asset {
	t.Helper()
	for _, a := range runtime.Assets() {
		if a.Build != b || !strings.HasPrefix(a.Platform, "ubuntu-") || strings.Contains(a.Platform, "arm64") {
			continue
		}
		root := filepath.Join(dir, a.Tag, a.Platform)
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, a.Exe)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, a.Exe), []byte("#!/bin/sh\n"), 0o755))
		pin := a.SHA256
		if a.Libs != nil {
			pin += "+" + a.Libs.SHA256
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, ".verified-sha256"), []byte(pin), 0o644))
		return a
	}
	t.Fatalf("no linux x64 %s build pinned", b)
	return runtime.Asset{}
}

func gpuRig(t *testing.T) (*modelRig, map[string]int) {
	r := newModelRig(t, 16384)
	oldHost, oldList := modelHost, modelListDevices
	t.Cleanup(func() { modelHost, modelListDevices = oldHost, oldList })
	modelHost = func() runtime.Host { return hostNVIDIA }
	asked := map[string]int{}
	modelListDevices = func(ctx context.Context, exe string, env map[string]string) ([]runtime.Device, error) {
		build := filepath.Base(filepath.Dir(filepath.Dir(exe)))
		asked[build]++
		switch {
		case strings.Contains(build, "cuda"):
			return nil, errors.New("error while loading shared libraries: libcuda.so.1: cannot open shared object file")
		case strings.Contains(build, "vulkan"):
			return []runtime.Device{{ID: "Vulkan0", Name: "NVIDIA RTX PRO 6000", TotalMiB: 97887, FreeMiB: 72071}}, nil
		}
		return nil, nil
	}
	return r, asked
}

func TestModelRuntimeChoiceIsTheOwnersAndSaid(t *testing.T) {
	r, _ := gpuRig(t)
	out, err := r.run(t, "runtime")
	require.NoError(t, err)
	require.Contains(t, out, "runtime: auto")
	require.Contains(t, out, "the cuda build", "auto on an NVIDIA card whose driver supports it means CUDA")
	require.Contains(t, out, "727.9 MiB", "with its whole size, runtime included")
	require.Contains(t, out, "not fetched yet")

	out, err = r.run(t, "runtime", "cpu")
	require.NoError(t, err)
	require.Contains(t, out, "runtime: cpu")
	require.Contains(t, out, "the cpu build")
	s, _ := r.home.Settings()
	require.Equal(t, "cpu", s.Runtime)

	modelHost = func() runtime.Host {
		return runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true, GPU: true}
	}
	out, err = r.run(t, "runtime", "cuda")
	require.NoError(t, err, "the choice is kept")
	require.Contains(t, out, "cannot serve here")
	require.Contains(t, out, "NVIDIA")

	_, err = r.run(t, "runtime", "turbo")
	require.ErrorContains(t, err, "auto, cpu, gpu, vulkan or cuda")
}

// Whether the GPU can be used is the build's own answer: CUDA that cannot load its driver library is
// passed over for Vulkan, which lists a card - and the owner is told why.
func TestModelTheBuildIsChosenByAskingIt(t *testing.T) {
	r, asked := gpuRig(t)
	dir := r.home.Runtime()
	placeBuild(t, dir, runtime.BuildCUDA)
	placeBuild(t, dir, runtime.BuildVulkan)
	placeBuild(t, dir, runtime.BuildCPU)

	rt, err := modelResolveRuntime(context.Background(), "auto")
	require.NoError(t, err)
	require.Equal(t, runtime.BuildVulkan, rt.Build, "%v", rt.Notes)
	require.True(t, rt.Offloads)
	require.Contains(t, strings.Join(rt.Notes, " "), "the cuda build")
	require.Contains(t, strings.Join(rt.Notes, " "), "libcuda.so.1")

	// Vulkan sees nothing either: auto serves with the CPU build, saying so; gpu refuses.
	modelListDevices = func(ctx context.Context, exe string, env map[string]string) ([]runtime.Device, error) {
		return nil, nil
	}
	rt, err = modelResolveRuntime(context.Background(), "auto")
	require.NoError(t, err)
	require.Equal(t, runtime.BuildCPU, rt.Build)
	require.Contains(t, strings.Join(rt.Notes, " "), "lists no GPU it can use")
	_, err = modelResolveRuntime(context.Background(), "gpu")
	require.ErrorContains(t, err, "lists no GPU it can use")
	require.ErrorContains(t, err, "Vulkan driver", "and says the fix")

	// A machine with no GPU never asks a GPU build anything.
	modelHost = func() runtime.Host {
		return runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true}
	}
	before := len(asked)
	rt, err = modelResolveRuntime(context.Background(), "auto")
	require.NoError(t, err)
	require.Equal(t, runtime.BuildCPU, rt.Build)
	require.Len(t, asked, before)
}

// A GPU build that cannot start the model is not a silent fallback: auto says so and serves with the
// CPU build; an explicit gpu choice serves nothing and names the way out.
func TestModelAGPUBuildThatCannotStartFallsBackOnlyUnderAuto(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	modelRuntimeFor = func(ctx context.Context, choice string) (modelRT, error) {
		if choice == "cpu" {
			return modelRT{Exe: os.Args[0], Tag: "bTEST", Build: runtime.BuildCPU}, nil
		}
		return modelRT{Exe: filepath.Join(t.TempDir(), "llama-server"), Tag: "bTEST", Build: runtime.BuildVulkan, Offloads: true}, nil
	}
	out, err := r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "the vulkan build could not start it")
	require.Contains(t, out, "serving with the CPU build instead")
	require.Contains(t, out, "on the air locally")

	require.NoError(t, r.home.SetSettings(withRuntime(r, "gpu")))
	_, err = r.run(t, "serve", "tiny")
	require.ErrorContains(t, err, "the vulkan build could not start it")
	require.ErrorContains(t, err, "roger model runtime cpu")
}

// What is running says which build.
func TestModelStatusNamesTheBuild(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	var st string
	modelUntilDone = func(context.Context) { st, _ = r.run(t, "status") }
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, st, "llama.cpp bTEST, the cpu build")
}

func withRuntime(r *modelRig, choice string) localmodel.Settings {
	s, _ := r.home.Settings()
	s.Runtime = choice
	return s
}

// The screens say what the choice means here: the best build auto would pick, not merely one that
// happens to be installed - and once a serve has settled it (a GPU build passed over, say), that.
func TestModelThePredictionIsWhatAutoPicksThenWhatAServeSettled(t *testing.T) {
	r, _ := gpuRig(t)
	placeBuild(t, r.home.Runtime(), runtime.BuildCPU)
	a, _, err := modelPredict()
	require.NoError(t, err)
	require.Equal(t, runtime.BuildCUDA, a.Build, "an installed CPU build does not stand in for what auto picks")

	placeBuild(t, r.home.Runtime(), runtime.BuildCUDA)
	placeBuild(t, r.home.Runtime(), runtime.BuildVulkan)
	rt, err := modelResolveRuntime(context.Background(), "auto")
	require.NoError(t, err)
	require.Equal(t, runtime.BuildVulkan, rt.Build)
	a, notes, err := modelPredict()
	require.NoError(t, err)
	require.Equal(t, runtime.BuildVulkan, a.Build, "a serve settled it: the screens say the build that serves")
	require.Contains(t, strings.Join(notes, " "), "libcuda.so.1", "and why the better one was passed over")

	// A different choice is not answered by an old settlement.
	require.NoError(t, r.home.SetSettings(withRuntime(r, "cpu")))
	a, _, err = modelPredict()
	require.NoError(t, err)
	require.Equal(t, runtime.BuildCPU, a.Build)
}

// Found live 2026-10-02: llama.cpp splits even a small model across every card by default, and a
// split costs hops between cards (59.7 tok/s on one card against 26.8 across four, same model). A
// model that fits on one card is pinned to the card with the most free memory; one that does not is
// left to the runtime's own split.
func TestModelAModelThatFitsOnOneCardIsPinnedToTheFreestCard(t *testing.T) {
	ds := []runtime.Device{{ID: "CUDA0", FreeMiB: 55736}, {ID: "CUDA1", FreeMiB: 11075}, {ID: "CUDA2", FreeMiB: 60000}}
	const gib = int64(1) << 30
	require.Equal(t, "CUDA2", modelPickDevice(ds, 2*gib), "the freest card that holds it")
	require.Equal(t, "CUDA2", modelPickDevice(ds, 57*gib), "a margin is left on the card")
	require.Empty(t, modelPickDevice(ds, 60*gib), "too big for any one card: the runtime splits it")
	require.Empty(t, modelPickDevice(nil, gib), "no device list, no pin")
	require.Empty(t, modelPickDevice(ds, 0), "an unknown need is not pinned on a guess")
}

// A build fetched as two archives shows two progress lines, the first left finished, not overwritten.
func TestModelEachArchiveGetsItsOwnProgressLine(t *testing.T) {
	r := newModelRig(t, 16384)
	r.out.Reset()
	modelProgress(provision.Progress{Done: 100, Total: 100})
	modelProgress(provision.Progress{Done: 10, Total: 900})
	modelProgress(provision.Progress{Done: 900, Total: 900})
	modelProgressDone()
	out := r.out.String()
	require.Equal(t, 2, strings.Count(out, "\n"), "one line per archive: %q", out)
	require.Contains(t, out, "100 B of 100 B")
}

// Review 2026-10-02: a GPU is found by its driver and its PCI vendor, never by what its name says -
// nvidia-smi calls cards "Tesla T4" and "Quadro RTX 6000", and an AMD card on Mesa has no rocm-smi.
func TestModelTheHostSeesGPUsByDriverAndVendorNotName(t *testing.T) {
	newModelRig(t, 16384)
	oldRun, oldVendors := hwRun, modelDRMVendors
	t.Cleanup(func() { hwRun, modelDRMVendors = oldRun, oldVendors })
	smi := ""
	hwRun = func(name string, args ...string) (string, bool) {
		if name == "nvidia-smi" && smi != "" {
			return smi, true
		}
		return "", false
	}
	modelHW = func() detect.LocalHW { return detect.LocalHW{RAMKnown: true, RAMTotalMiB: 16384} }
	for _, c := range []struct {
		name        string
		vendors     []string
		smi         string
		gpu, nvidia bool
		cuda        int
	}{
		{"a Tesla T4: NVIDIA by its driver, whatever its name", []string{"0x10de"}, "CUDA Version: 12.8", true, true, 12},
		{"an AMD card with no rocm-smi", []string{"0x1002"}, "", true, false, 0},
		{"an NVIDIA card whose driver does not answer", []string{"0x10de"}, "", true, true, 0},
		{"an Intel GPU alone is not taken for one auto should use", []string{"0x8086"}, "", false, false, 0},
		{"no display adapter at all", nil, "", false, false, 0},
	} {
		modelDRMVendors = func() []string { return c.vendors }
		smi = c.smi
		h := modelThisHost()
		require.Equal(t, c.gpu, h.GPU, c.name)
		require.Equal(t, c.nvidia, h.NVIDIA, c.name)
		require.Equal(t, c.cuda, h.CUDAMajor, c.name)
	}
}

// Review 2026-10-02: after a GPU build could not start a model and auto served it with the CPU build,
// the screens say the CPU build serves, and why - not the GPU build that failed.
func TestModelAfterAFallbackTheScreensSayWhatServes(t *testing.T) {
	r, _ := gpuRig(t)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	placeBuild(t, r.home.Runtime(), runtime.BuildCPU)
	modelRuntimeFor = func(ctx context.Context, choice string) (modelRT, error) {
		if choice == "cpu" {
			return modelRT{Exe: os.Args[0], Tag: "b11146", Build: runtime.BuildCPU}, nil
		}
		return modelRT{Exe: filepath.Join(t.TempDir(), "llama-server"), Tag: "b11146", Build: runtime.BuildCUDA, Offloads: true}, nil
	}
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	a, notes, err := modelPredict()
	require.NoError(t, err)
	require.Equal(t, runtime.BuildCPU, a.Build, "the build that served")
	require.Contains(t, strings.Join(notes, " "), "the cuda build could not start it")
}

// Review 2026-10-02: a serve from the console or an Edge request says the build it will fetch, with
// its size, before anything moves.
func TestModelAServeFromTheConsoleSaysWhatItWillFetch(t *testing.T) {
	r, _ := gpuRig(t)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	oldSpawn := modelSpawn
	t.Cleanup(func() { modelSpawn = oldSpawn })
	modelSpawn = func([]string, string) error { return nil }
	says, err := modelWebServe("acme/Tiny-GGUF:Q4_K_M", 0, false)
	require.NoError(t, err)
	require.Contains(t, says, "fetches the cuda build first (727.9 MiB)")
}

// Review 2026-10-02: the pin to one card reaches the runtime's own command line.
func TestModelAPinnedServeTellsTheRuntimeItsCard(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	argsFile := filepath.Join(t.TempDir(), "args")
	modelServeEnv = map[string]string{"FAKE_LLAMA": "1", "FAKE_ARGS_FILE": argsFile}
	modelRuntimeFor = func(context.Context, string) (modelRT, error) {
		return modelRT{Exe: os.Args[0], Tag: "bTEST", Build: runtime.BuildCUDA, Offloads: true,
			Devices: []runtime.Device{{ID: "CUDA0", FreeMiB: 1000}, {ID: "CUDA1", FreeMiB: 90000}}}, nil
	}
	out, err := r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, out, "on one card, CUDA1")
	b, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Contains(t, string(b), "--device CUDA1")
}

// A build that cannot load here is never started: auto passes a GPU build over saying why, and the
// CPU build names the package that fixes it (found preparing the Pi test: libgomp1).
func TestModelABuildThatCannotLoadIsSaidBeforeAServe(t *testing.T) {
	r, _ := gpuRig(t)
	dir := r.home.Runtime()
	placeBuild(t, dir, runtime.BuildCUDA)
	placeBuild(t, dir, runtime.BuildVulkan)
	placeBuild(t, dir, runtime.BuildCPU)
	old := modelLoadCheck
	t.Cleanup(func() { modelLoadCheck = old })
	modelLoadCheck = func(ctx context.Context, exe string, env map[string]string) error {
		if strings.Contains(exe, "ubuntu-x64") {
			return fmt.Errorf("%w: libgomp.so.1 - on Debian or Raspberry Pi OS: sudo apt install libgomp1", runtime.ErrMissingLibrary)
		}
		if strings.Contains(exe, "cuda") {
			return fmt.Errorf("%w: libcuda.so.1", runtime.ErrMissingLibrary)
		}
		return nil
	}
	rt, err := modelResolveRuntime(context.Background(), "auto")
	require.NoError(t, err)
	require.Equal(t, runtime.BuildVulkan, rt.Build, "the CUDA build cannot load here, Vulkan can")
	require.Contains(t, strings.Join(rt.Notes, " "), "libcuda.so.1")

	_, err = modelResolveRuntime(context.Background(), "cpu")
	require.ErrorIs(t, err, runtime.ErrMissingLibrary)
	require.Contains(t, modelRuntimeErr(err).Error(), "sudo apt install libgomp1")
	require.NotContains(t, modelRuntimeErr(err).Error(), "could not get the runtime", "it is a missing package, said as one")
}

// Found with tools/edgesim: asked from afar, roger ran the fit tool of a GPU build that was not
// installed ("fork/exec .../ubuntu-vulkan-x64/...: no such file"). The build the choice predicts is
// only used for a fit check when it is really here.
func TestModelOnlyAnInstalledBuildIsAskedToFit(t *testing.T) {
	r, _ := gpuRig(t)
	placeBuild(t, r.home.Runtime(), runtime.BuildCPU)
	require.Empty(t, modelInstalledExe(), "auto predicts CUDA here, and CUDA is not installed")
	placeBuild(t, r.home.Runtime(), runtime.BuildCUDA)
	require.Contains(t, modelInstalledExe(), "ubuntu-cuda-12.8-x64", "once it is, its server is the one asked")
}
