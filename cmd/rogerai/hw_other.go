//go:build !linux && !windows

package main

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/detect"
)

// detectHWClass returns the PRIVACY-BUCKETED hardware class for macOS/BSD. On Apple
// Silicon the GPU is the integrated Metal device on unified memory - reported as the
// "apple" class (never the chip model or memory size). On an Intel Mac / BSD with an
// NVIDIA card we count discrete GPUs and bucket; otherwise cpu.
//
// As on Linux, the class is now read off the single local probe rather than derived a
// second time, so the advertised bucket and the operator's own report can never disagree
// about the same machine.
func detectHWClass() string {
	return detectLocalHW().Class
}

// detectLocalHW gathers the rich, LOCAL-ONLY picture on macOS and BSD. None of it is
// transmitted; see internal/detect/localhw.go for why that is a rule and not a preference.
//
// This is the platform where the honest gaps are largest, and they are recorded rather
// than papered over:
//
//   - Apple Silicon has NO separate VRAM pool to measure. The GPU addresses system RAM
//     directly, macOS reserves an unpublished share of it, and there is no supported way
//     to read the fraction Metal will actually grant a process. So the report says
//     "unified" and checks system RAM, rather than printing a VRAM figure that does not
//     exist.
//   - An Intel Mac's discrete VRAM is in system_profiler's prose, in a format that has
//     changed across macOS releases and is localized. The device is detected; its memory
//     is reported undetermined rather than scraped hopefully.
func detectLocalHW() detect.LocalHW {
	var hw detect.LocalHW
	hw.CPUCores = hwCores()

	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		// Apple Silicon: integrated Metal GPU on unified memory. The class is assigned
		// directly rather than counted, exactly as it always has been - "apple" is a bucket
		// of its own and never falls out of counting discrete cards.
		hw.Class = detect.HWApple
		hw.UnifiedMemory = true
		if out, ok := hwRun("sysctl", "-n", "machdep.cpu.brand_string"); ok {
			if name := strings.TrimSpace(out); name != "" {
				hw.GPUs = []detect.LocalGPU{{Model: name + " (integrated, unified memory)"}}
			}
		}
		hw.Note("VRAM: Apple Silicon has no separate GPU memory pool to measure - the GPU shares " +
			"system RAM, and the share macOS will actually grant Metal is not readable")
	case runtime.GOOS == "darwin":
		// Intel Mac: a Metal GPU may still be present. Ask system_profiler for the
		// display hardware; an Apple/AMD GPU line means a real GPU is available.
		hw.Class = detect.HWCPU
		if out, ok := hwRun("system_profiler", "SPDisplaysDataType"); ok {
			l := strings.ToLower(out)
			if strings.Contains(l, "metal") || strings.Contains(l, "chipset model") {
				if strings.Contains(l, "apple") {
					hw.Class = detect.HWApple
					hw.UnifiedMemory = true
				} else {
					hw.Class = detect.HWSingleGPU
					hw.GPUs = []detect.LocalGPU{{Model: "discrete GPU (reported by system_profiler)"}}
					hw.Note("VRAM: an Intel Mac's GPU memory appears only in system_profiler's prose, " +
						"whose format and language vary by macOS release, so it is not read here")
				}
			}
		}
		// Only when nothing Metal answered do we fall through to nvidia-smi, which is the
		// order the advertised class has always used.
		if hw.Class == detect.HWCPU {
			hw.SetGPUs(nvidiaLocalGPUs())
		}
	default:
		// BSD, and anything else that is neither Linux nor Windows nor Darwin. NVIDIA's
		// tool is the only accelerator probe available here.
		hw.SetGPUs(nvidiaLocalGPUs())
		if len(hw.GPUs) == 0 {
			hw.Note("GPU: nvidia-smi did not answer, so this host is being treated as CPU-only")
		}
	}

	// System RAM. macOS publishes it as hw.memsize, the BSDs as hw.physmem. Both are a
	// bare byte count from `sysctl -n`, so there is no unit to get wrong.
	key := "hw.physmem"
	if runtime.GOOS == "darwin" {
		key = "hw.memsize"
	}
	if out, ok := hwRun("sysctl", "-n", key); ok {
		if mib, ok := detect.ParseSysctlBytesMiB(out); ok {
			hw.RAMTotalMiB, hw.RAMKnown = mib, true
		}
	}
	if !hw.RAMKnown {
		hw.Note("system RAM: `sysctl -n " + key + "` did not return a usable byte count")
	}

	// Free disk, on the filesystem holding the home directory, with the path reported so
	// the number's scope is visible rather than assumed.
	dir := hwProbeDir()
	hw.DiskPath = dir
	if mib, ok := hwDiskFreeMiB(dir); ok {
		hw.DiskFreeMiB, hw.DiskKnown = mib, true
	} else {
		hw.Note("free disk: " + dir + " could not be stat'd")
	}
	return hw
}

// nvidiaLocalGPUs enumerates NVIDIA devices with their models and memory sizes, for the
// Intel-Mac and BSD paths where nvidia-smi is the only accelerator probe there is.
func nvidiaLocalGPUs() []detect.LocalGPU {
	out, ok := hwRun("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
	if !ok {
		return nil
	}
	return detect.ParseNvidiaGPUs(out)
}

// hwRun is the same behaviour-preserving seam the Linux path carries over the probe
// command runner. It is here so the three platform files differ only where the operating
// systems genuinely do, and so a test running on any of them can drive the tool-present
// branches on a machine that has none of the tools.
var hwRun = runHW

// runHW runs a short-lived hardware-probe command and returns its stdout, hard-capped
// at 2s so a wedged tool can never stall share startup. Any error yields ok=false.
func runHW(name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}
