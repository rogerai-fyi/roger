//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"time"

	"rogerai.fm/roger/v5/internal/detect"
)

// detectHWClass returns the PRIVACY-BUCKETED hardware class a Linux node advertises:
// multi-gpu / single-gpu / cpu. It probes nvidia-smi first, then rocm-smi, counts
// discrete GPUs, and buckets - so the exact rig (model/count/VRAM beyond "multi")
// never leaves the host. No GPU tooling present -> cpu.
//
// It now reads that bucket off detectLocalHW rather than counting a second time. Two
// things want the same probe - the class that is advertised, and the local preflight that
// is not - and running the probe twice would have been the cheaper mistake. The expensive
// one would have been two independent count paths that could disagree about how many GPUs
// this box has, since one of them decides what the network is told. There is one probe
// and one count, and LocalHW.Class IS the advertised class.
func detectHWClass() string {
	return detectLocalHW().Class
}

// detectLocalHW gathers the rich, LOCAL-ONLY hardware picture: GPU models and VRAM,
// system RAM, free disk, core count. None of it is transmitted - see
// internal/detect/localhw.go for why that is a rule rather than a preference, and
// preflight_nowire_test.go for the pin that keeps it one.
//
// Every probe degrades to "could not determine" rather than to a guess, and each failure
// records a line the operator can act on. Linux is the platform where nearly everything is
// readable: /proc/meminfo is authoritative for RAM, statfs is authoritative for disk, and
// the vendor tools are authoritative for the accelerators when they are installed at all.
func detectLocalHW() detect.LocalHW {
	var hw detect.LocalHW
	hw.CPUCores = hwCores()

	// Accelerators. NVIDIA first, then AMD, matching the order the advertised class has
	// always probed in: a box with both is counted as the NVIDIA one it almost certainly
	// is, and reordering here would silently change what such a box advertises.
	gpus := nvidiaLocalGPUs()
	rocmVRAM, rocmVRAMOK := 0, false
	if len(gpus) == 0 {
		gpus = rocmLocalGPUs()
		if len(gpus) > 0 {
			// AMD needs a second invocation for memory: --showproductname lists the
			// devices and says nothing about their size.
			if out, ok := hwRun("rocm-smi", "--showmeminfo", "vram"); ok {
				rocmVRAM, rocmVRAMOK = detect.ParseROCmVRAMMiB(out)
			}
		}
	}
	hw.SetGPUs(gpus)
	if rocmVRAMOK {
		hw.SetVRAMTotal(rocmVRAM)
	}
	if len(gpus) == 0 {
		hw.Note("GPU: neither nvidia-smi nor rocm-smi answered, so this host is being treated " +
			"as CPU-only. If you do have a GPU, its management tool is not installed or not on PATH")
	}

	// System RAM. MemTotal is the kernel's own figure and needs no unit sniffing; a
	// container with /proc masked is the case that legitimately fails here.
	if b, err := hwReadFile("/proc/meminfo"); err == nil {
		if mib, ok := detect.ParseMemTotalMiB(string(b)); ok {
			hw.RAMTotalMiB, hw.RAMKnown = mib, true
		}
	}
	if !hw.RAMKnown {
		hw.Note("system RAM: /proc/meminfo could not be read (a container with /proc masked does this)")
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

// nvidiaLocalGPUs enumerates NVIDIA devices WITH their models and memory sizes. It runs
// the identical command the advertised-class count has always run; the difference is only
// that the count throws the details away and this does not, which is safe precisely
// because this result never reaches the wire.
func nvidiaLocalGPUs() []detect.LocalGPU {
	out, ok := hwRun("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
	if !ok {
		return nil
	}
	return detect.ParseNvidiaGPUs(out)
}

// rocmLocalGPUs enumerates AMD devices from the product-name listing. Memory is not in
// that output and is fetched separately by the caller.
func rocmLocalGPUs() []detect.LocalGPU {
	out, ok := hwRun("rocm-smi", "--showproductname")
	if !ok {
		return nil
	}
	return detect.ParseROCmGPUs(out)
}

// nvidiaGPUCount returns the number of NVIDIA GPUs via nvidia-smi, or 0 when the
// tool is absent or reports none. We query name+memory.total (matching the audit's
// command) but discard everything except the COUNT - the per-GPU details never reach
// the advertised class.
func nvidiaGPUCount() int { return len(nvidiaLocalGPUs()) }

// rocmGPUCount returns the number of AMD GPUs via rocm-smi (product-name listing),
// or 0 when absent/none.
func rocmGPUCount() int { return len(rocmLocalGPUs()) }

// hwRun is a behaviour-preserving seam over the GPU-probe command runner (default
// runHW, which shells out to nvidia-smi / rocm-smi). Production runs the real probe
// unchanged; a test points it at a fake that returns canned smi output so the
// GPU-present branches of nvidiaGPUCount / rocmGPUCount / detectHWClass are reachable
// on a GPU-less CI box (where the real tools are absent and only the cpu branch runs).
var hwRun = runHW

// hwReadFile is the same kind of seam over the /proc read, so the "MemTotal is
// unreadable" branch - which on a real Linux box never fires - can be exercised.
var hwReadFile = os.ReadFile

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
