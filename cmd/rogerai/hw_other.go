//go:build !linux && !windows

package main

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/detect"
)

// detectHWClass returns the PRIVACY-BUCKETED hardware class for macOS/BSD. On Apple
// Silicon the GPU is the integrated Metal device on unified memory - reported as the
// "apple" class (never the chip model or memory size). On an Intel Mac / BSD with an
// NVIDIA card we count discrete GPUs and bucket; otherwise cpu.
func detectHWClass() string {
	if runtime.GOOS == "darwin" {
		if runtime.GOARCH == "arm64" {
			return detect.HWApple // Apple Silicon: integrated Metal GPU, unified memory
		}
		// Intel Mac: a Metal GPU may still be present. Ask system_profiler for the
		// display hardware; an Apple/AMD GPU line means a real GPU is available.
		if out, ok := runHW("system_profiler", "SPDisplaysDataType"); ok {
			l := strings.ToLower(out)
			if strings.Contains(l, "metal") || strings.Contains(l, "chipset model") {
				if strings.Contains(l, "apple") {
					return detect.HWApple
				}
				return detect.HWSingleGPU
			}
		}
	}
	if out, ok := runHW("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader"); ok {
		if n := detect.CountNvidiaSMI(out); n > 0 {
			return detect.BucketGPUCount(n)
		}
	}
	return detect.HWCPU
}

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
