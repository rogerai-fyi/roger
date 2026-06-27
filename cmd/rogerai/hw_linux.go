//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/detect"
)

// detectHW returns a human-readable CPU model for advertising a shared node.
// On Linux it reads the kernel's /proc/cpuinfo.
//
// NOTE: this is kept for callers that want the raw CPU string; the node now
// advertises the privacy-bucketed detectHWClass() instead (it reports a class, not
// the CPU/GPU rig).
func detectHW() string {
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				if i := strings.Index(line, ":"); i >= 0 {
					return strings.TrimSpace(line[i+1:])
				}
			}
		}
	}
	return "unknown"
}

// detectHWClass returns the PRIVACY-BUCKETED hardware class a Linux node advertises:
// multi-gpu / single-gpu / cpu. It probes nvidia-smi first, then rocm-smi, counts
// discrete GPUs, and buckets - so the exact rig (model/count/VRAM beyond "multi")
// never leaves the host. No GPU tooling present -> cpu.
func detectHWClass() string {
	if n := nvidiaGPUCount(); n > 0 {
		return detect.BucketGPUCount(n)
	}
	if n := rocmGPUCount(); n > 0 {
		return detect.BucketGPUCount(n)
	}
	return detect.HWCPU
}

// nvidiaGPUCount returns the number of NVIDIA GPUs via nvidia-smi, or 0 when the
// tool is absent or reports none. We query name+memory.total (matching the audit's
// command) but discard everything except the COUNT - the per-GPU details never reach
// the advertised class.
func nvidiaGPUCount() int {
	out, ok := hwRun("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
	if !ok {
		return 0
	}
	return detect.CountNvidiaSMI(out)
}

// rocmGPUCount returns the number of AMD GPUs via rocm-smi (product-name listing),
// or 0 when absent/none.
func rocmGPUCount() int {
	out, ok := hwRun("rocm-smi", "--showproductname")
	if !ok {
		return 0
	}
	return detect.CountROCmSMI(out)
}

// hwRun is a behaviour-preserving seam over the GPU-probe command runner (default
// runHW, which shells out to nvidia-smi / rocm-smi). Production runs the real probe
// unchanged; a test points it at a fake that returns canned smi output so the
// GPU-present branches of nvidiaGPUCount / rocmGPUCount / detectHWClass are reachable
// on a GPU-less CI box (where the real tools are absent and only the cpu branch runs).
var hwRun = runHW

// runHW runs a short-lived hardware-probe command and returns its stdout. It is
// hard-capped at 2s so a wedged tool can never stall share startup, and any error
// (missing binary, non-zero exit) yields ok=false.
func runHW(name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}
