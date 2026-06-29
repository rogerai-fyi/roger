//go:build windows

package main

import (
	"context"
	"os/exec"
	"time"

	"github.com/rogerai-fyi/roger/internal/detect"
)

// detectHWClass returns the PRIVACY-BUCKETED hardware class a Windows node
// advertises: multi-gpu / single-gpu / cpu. It probes nvidia-smi, counts discrete
// GPUs, and buckets - the exact rig never leaves the host. No NVIDIA tooling -> cpu.
func detectHWClass() string {
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
