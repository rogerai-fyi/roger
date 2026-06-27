//go:build linux

package main

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/detect"
)

// TestDetectHWClassGPU drives the GPU-present branches of detectHWClass /
// nvidiaGPUCount / rocmGPUCount via the hwRun seam, so the multi-gpu / single-gpu
// classes are reachable on a GPU-less CI box (where the real smi tools are absent and
// only the cpu branch otherwise runs).
func TestDetectHWClassGPU(t *testing.T) {
	orig := hwRun
	t.Cleanup(func() { hwRun = orig })

	// Two NVIDIA GPUs reported -> multi-gpu.
	hwRun = func(name string, args ...string) (string, bool) {
		if name == "nvidia-smi" {
			return "NVIDIA A100, 40960 MiB\nNVIDIA A100, 40960 MiB\n", true
		}
		return "", false
	}
	if got := nvidiaGPUCount(); got != 2 {
		t.Errorf("nvidiaGPUCount = %d, want 2", got)
	}
	if got := detectHWClass(); got != detect.HWMultiGPU {
		t.Errorf("detectHWClass(2 nvidia) = %q, want %q", got, detect.HWMultiGPU)
	}

	// No NVIDIA, one AMD GPU via rocm-smi -> single-gpu.
	hwRun = func(name string, args ...string) (string, bool) {
		if name == "rocm-smi" {
			return "GPU[0]\t\tCard series: Instinct MI300\n", true
		}
		return "", false
	}
	if got := rocmGPUCount(); got != 1 {
		t.Errorf("rocmGPUCount = %d, want 1", got)
	}
	if got := detectHWClass(); got != detect.HWSingleGPU {
		t.Errorf("detectHWClass(1 rocm) = %q, want %q", got, detect.HWSingleGPU)
	}

	// Nothing present -> cpu (the GPU-less default).
	hwRun = func(name string, args ...string) (string, bool) { return "", false }
	if got := detectHWClass(); got != detect.HWCPU {
		t.Errorf("detectHWClass(no gpu) = %q, want %q", got, detect.HWCPU)
	}
}
