//go:build windows

package main

import (
	"context"
	"os/exec"
	"time"

	"rogerai.fm/roger/v6/internal/detect"
)

// detectHWClass returns the PRIVACY-BUCKETED hardware class a Windows node
// advertises: multi-gpu / single-gpu / cpu. It probes nvidia-smi, counts discrete
// GPUs, and buckets - the exact rig never leaves the host. No NVIDIA tooling -> cpu.
//
// As on the other platforms, the class is read off the single local probe rather than
// counted separately, so the bucket that is advertised and the report the operator sees
// can never disagree about the same machine.
func detectHWClass() string {
	return detectLocalHW().Class
}

// detectLocalHW gathers the LOCAL-ONLY picture on Windows. None of it is transmitted; see
// internal/detect/localhw.go.
//
// Windows is the thinnest of the three platforms, and the report says so on the machine
// rather than only here. What it can determine: the NVIDIA accelerators and their VRAM,
// because nvidia-smi ships with the driver and behaves identically to its Linux build; and
// the core count. What it cannot: system RAM and free disk, both of which live behind
// kernel32 calls the Go standard library does not export, reachable only through a
// hand-rolled DLL binding this tree has no way to exercise. Rather than write that binding
// blind and print a number nobody has ever seen be correct, those two report "could not
// determine" and the verdict degrades to INCOMPLETE.
//
// An AMD or Intel GPU is likewise invisible here: rocm-smi is not a Windows tool, so a
// Radeon box reports as CPU-only. That is a detection gap and is noted as one, which
// matters because the SAME gap already decides the advertised class - so a Windows Radeon
// node has always gone on air as `hw=cpu`, and now its operator is told why.
func detectLocalHW() detect.LocalHW {
	var hw detect.LocalHW
	hw.CPUCores = hwCores()

	hw.SetGPUs(nvidiaLocalGPUs())
	if len(hw.GPUs) == 0 {
		hw.Note("GPU: nvidia-smi did not answer. This host is being treated as CPU-only, and " +
			"that is also what it advertises - on Windows there is no AMD/Intel probe here, " +
			"so a non-NVIDIA GPU is invisible to this build")
	}

	hw.Note("system RAM: not readable on Windows by this build - the figure lives behind a " +
		"kernel32 call the Go standard library does not expose, and a guessed number in this " +
		"report would be worse than an admitted gap")
	dir := hwProbeDir()
	hw.DiskPath = dir
	if mib, ok := hwDiskFreeMiB(dir); ok {
		hw.DiskFreeMiB, hw.DiskKnown = mib, true
	} else {
		hw.Note("free disk: not readable on Windows by this build, for the same reason as system RAM")
	}
	return hw
}

// nvidiaLocalGPUs enumerates NVIDIA devices with their models and memory sizes. The
// Windows nvidia-smi ships with the driver and takes the identical query flags.
func nvidiaLocalGPUs() []detect.LocalGPU {
	out, ok := hwRun("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
	if !ok {
		return nil
	}
	return detect.ParseNvidiaGPUs(out)
}

// hwRun is the same behaviour-preserving seam the Linux path carries over the probe
// command runner, so the three platform files differ only where the operating systems do.
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
