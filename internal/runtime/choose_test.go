package runtime_test

// A GPU BUILD AND A CPU BUILD (features/serving/local_models.feature §10, approved 2026-10-02 with
// CUDA as well as Vulkan). The choice is a pure function of the owner's choice and the host's facts,
// so every row of the approved table is pinned here; whether the chosen GPU build can actually see
// a device is the build's own answer (ListDevices), asked after it is fetched.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/runtime"
)

func builds(as []runtime.Asset) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, string(a.Build))
	}
	return out
}

var (
	linuxNoGPU   = runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true}
	linuxNVIDIA  = runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true, GPU: true, NVIDIA: true, CUDAMajor: 13, CUDAMinor: 2}
	linuxOldNV   = runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true, GPU: true, NVIDIA: true, CUDAMajor: 12, CUDAMinor: 2}
	linuxAMD     = runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true, GPU: true}
	winNVIDIA    = runtime.Host{OS: "windows", Arch: "amd64", GPU: true, NVIDIA: true, CUDAMajor: 12, CUDAMinor: 4}
	linuxArmGPU  = runtime.Host{OS: "linux", Arch: "arm64", GlibcMajor: 2, GlibcMinor: 39, GlibcKnown: true, GPU: true, NVIDIA: true, CUDAMajor: 12, CUDAMinor: 6}
	macSilicon   = runtime.Host{OS: "darwin", Arch: "arm64", GPU: true}
	linuxUnknown = runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42, GlibcKnown: true, GPU: true, NVIDIA: true}
)

func TestTheOwnersChoiceBecomesTheBuildsToTry(t *testing.T) {
	for _, c := range []struct {
		name   string
		h      runtime.Host
		choice string
		want   []string
		note   string
	}{
		{"auto, no GPU: CPU and nothing else is ever fetched", linuxNoGPU, "auto", []string{"cpu"}, ""},
		{"auto, NVIDIA whose driver supports the CUDA build", linuxNVIDIA, "auto", []string{"cuda", "vulkan", "cpu"}, ""},
		{"auto, NVIDIA whose driver is older than the CUDA build", linuxOldNV, "auto", []string{"vulkan", "cpu"}, "supports CUDA 12.2; the CUDA build needs 12.8"},
		{"auto, AMD or Intel", linuxAMD, "auto", []string{"vulkan", "cpu"}, ""},
		{"auto, NVIDIA driver version unknown: no CUDA guess", linuxUnknown, "auto", []string{"vulkan", "cpu"}, "could not be read"},
		{"cpu with a GPU", linuxNVIDIA, "cpu", []string{"cpu"}, ""},
		{"gpu is auto short of CPU", linuxNVIDIA, "gpu", []string{"cuda", "vulkan"}, ""},
		{"vulkan", linuxNVIDIA, "vulkan", []string{"vulkan"}, ""},
		{"windows NVIDIA gets its own CUDA 12.4 build", winNVIDIA, "auto", []string{"cuda", "vulkan", "cpu"}, ""},
		{"arm64 has Vulkan but no pinned CUDA (it targets arm servers, not Jetson)", linuxArmGPU, "auto", []string{"vulkan", "cpu"}, ""},
		{"Apple silicon has one build, Metal, whatever is chosen", macSilicon, "auto", []string{"metal"}, ""},
		{"Apple silicon, cpu chosen: still its one build", macSilicon, "cpu", []string{"metal"}, "one build"},
		{"empty choice is auto", linuxAMD, "", []string{"vulkan", "cpu"}, ""},
	} {
		got, notes, err := runtime.Plan(c.h, c.choice)
		require.NoError(t, err, c.name)
		require.Equal(t, c.want, builds(got), c.name)
		if c.note != "" {
			require.Contains(t, strings.Join(notes, " "), c.note, c.name)
		}
	}
}

func TestAChoiceThatCannotBeMetIsRefusedWithItsReason(t *testing.T) {
	_, _, err := runtime.Plan(linuxOldNV, "cuda")
	require.ErrorContains(t, err, "12.2")
	require.ErrorContains(t, err, "12.8", "both versions are named")
	_, _, err = runtime.Plan(linuxAMD, "cuda")
	require.ErrorContains(t, err, "NVIDIA")
	_, _, err = runtime.Plan(linuxNoGPU, "gpu")
	require.ErrorContains(t, err, "no GPU")
	_, _, err = runtime.Plan(runtime.Host{OS: "windows", Arch: "arm64", GPU: true}, "vulkan")
	require.ErrorContains(t, err, "no Vulkan build")
	_, _, err = runtime.Plan(linuxNoGPU, "turbo")
	require.ErrorContains(t, err, "auto, cpu, gpu, vulkan or cuda")
	_, _, err = runtime.Plan(runtime.Host{OS: "linux", Arch: "arm"}, "auto")
	require.ErrorIs(t, err, runtime.ErrNoRuntime)
	_, _, err = runtime.Plan(runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 20, GlibcKnown: true}, "cpu")
	require.ErrorIs(t, err, runtime.ErrNoRuntime, "a library floor still refuses")
}

// Every build is pinned the same way, and a CUDA build carries its runtime bundle as a pinned asset
// of its own, with the CUDA version a driver must support.
func TestEveryBuildIsPinnedTheSameWay(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range runtime.Assets() {
		require.NotEmpty(t, a.Build, a.Platform)
		require.Equal(t, a.Build != runtime.BuildCPU, a.Offloads, "%s: only a GPU build offloads", a.Platform)
		seen[a.Platform] = true
		if a.Build == runtime.BuildCUDA {
			require.NotNil(t, a.Libs, "%s carries its CUDA runtime", a.Platform)
			require.Len(t, a.Libs.SHA256, 64)
			require.Positive(t, a.Libs.Bytes)
			require.True(t, strings.HasPrefix(a.Libs.URL, "https://github.com/ggml-org/llama.cpp/releases/download/"+runtime.Tag()+"/"))
			require.Positive(t, a.CUDAMajor)
		} else {
			require.Nil(t, a.Libs, a.Platform)
		}
		require.Positive(t, a.TotalBytes())
	}
	for _, p := range []string{"ubuntu-vulkan-x64", "ubuntu-vulkan-arm64", "ubuntu-cuda-12.8-x64", "win-vulkan-x64", "win-cuda-12.4-x64"} {
		require.True(t, seen[p], p)
	}
}

func TestTheDriversCUDAVersionIsReadFromWhatItPrints(t *testing.T) {
	for out, want := range map[string][2]int{
		"| NVIDIA-SMI 595.71.05   Driver Version: 595.71.05   CUDA Version: 13.2     |": {13, 2},
		"CUDA Version: 12.8": {12, 8},
	} {
		maj, minor, ok := runtime.ParseCUDAVersion(out)
		require.True(t, ok)
		require.Equal(t, want, [2]int{maj, minor})
	}
	_, _, ok := runtime.ParseCUDAVersion("NVIDIA-SMI has failed because it couldn't communicate with the driver")
	require.False(t, ok)
}

// Whether a GPU can be used is the GPU build's own answer, read from what it prints.
func TestTheDevicesABuildListsAreReadFromWhatItPrints(t *testing.T) {
	out := "0.00.000.416 I srv  llama_server: initializing ...\nAvailable devices:\n" +
		"  Vulkan0: NVIDIA RTX PRO 6000 Blackwell Max-Q Workstation Edition (97887 MiB, 72071 MiB free)\n" +
		"  Vulkan1: NVIDIA RTX PRO 4500 Blackwell (32623 MiB, 11718 MiB free)\n"
	ds := runtime.ParseDevices(out)
	require.Len(t, ds, 2)
	require.Equal(t, runtime.Device{ID: "Vulkan0", Name: "NVIDIA RTX PRO 6000 Blackwell Max-Q Workstation Edition", TotalMiB: 97887, FreeMiB: 72071}, ds[0])
	require.Empty(t, runtime.ParseDevices("Available devices:\n"), "a build that sees no GPU lists none")
	require.Empty(t, runtime.ParseDevices("ggml_vulkan: no devices found"))
}

// The build's own answer, from the build itself (the fake prints what b11146's --list-devices prints).
func TestABuildIsAskedWhichDevicesItCanUse(t *testing.T) {
	ds, err := runtime.ListDevices(context.Background(), os.Args[0], map[string]string{"FAKE_RUNTIME": "devices-two"})
	require.NoError(t, err)
	require.Len(t, ds, 2)
	require.Equal(t, "Vulkan1", ds[1].ID)
	ds, err = runtime.ListDevices(context.Background(), os.Args[0], map[string]string{"FAKE_RUNTIME": "devices-none"})
	require.NoError(t, err)
	require.Empty(t, ds)
	_, err = runtime.ListDevices(context.Background(), os.Args[0], map[string]string{"FAKE_RUNTIME": "devices-broken"})
	require.ErrorContains(t, err, "libvulkan.so.1", "the build's last words say what is missing")
}

// Review 2026-10-02: a refused explicit choice says the real reason, not "not pinned".
func TestARefusedChoiceSaysTheRealReason(t *testing.T) {
	old := runtime.Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 35, GlibcKnown: true, GPU: true, NVIDIA: true, CUDAMajor: 13, CUDAMinor: 2}
	_, _, err := runtime.Plan(old, "cuda")
	require.ErrorContains(t, err, "glibc 2.38", "the CUDA build needs a newer system, and says so")
	plan, notes, err := runtime.Plan(old, "auto")
	require.NoError(t, err)
	require.Equal(t, []string{"vulkan", "cpu"}, builds(plan))
	require.NotContains(t, strings.Join(notes, " "), "using Vulkan -", "no claim about Vulkan beyond what is true")
	older := old
	older.GlibcMinor = 30
	_, notes, err = runtime.Plan(older, "auto")
	require.ErrorIs(t, err, runtime.ErrNoRuntime, "the CPU build cannot load either")
	_ = notes
}

// Found preparing the Pi test (2026-10-02): the Linux arm64 build needs libgomp.so.1, which a minimal
// OS may not ship. A build that cannot load is said plainly, with the package that fixes it, before a
// serve is attempted - not as a loader error after (local_models.feature §3).
func TestABuildThatCannotLoadNamesThePackageThatFixesIt(t *testing.T) {
	require.NoError(t, runtime.LoadCheck(context.Background(), os.Args[0], map[string]string{"FAKE_RUNTIME": "load-ok"}))
	err := runtime.LoadCheck(context.Background(), os.Args[0], map[string]string{"FAKE_RUNTIME": "load-missing"})
	require.ErrorIs(t, err, runtime.ErrMissingLibrary)
	require.ErrorContains(t, err, "libgomp.so.1")
	require.ErrorContains(t, err, "sudo apt install libgomp1", "the package that fixes it, on Debian and Raspberry Pi OS")
	err = runtime.LoadCheck(context.Background(), os.Args[0], map[string]string{"FAKE_RUNTIME": "load-odd"})
	require.ErrorContains(t, err, "libweird.so.9", "an unknown library is still named")
	require.NotContains(t, err.Error(), "apt install", "and no package is guessed")
}
