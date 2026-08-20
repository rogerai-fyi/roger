//go:build linux

package main

import (
	"errors"
	"strings"
	"testing"

	"rogerai.fm/roger/v5/internal/detect"
)

// stubHWProbes points the three Linux gatherer seams at canned answers, so the
// GPU-present, RAM-readable and disk-readable branches are reachable on a CI box that has
// none of those things. Returning nil for a seam leaves the real implementation in place.
func stubHWProbes(t *testing.T, run func(string, ...string) (string, bool), meminfo string, meminfoErr error, disk func(string) (int, bool)) {
	t.Helper()
	origRun, origRead, origDisk := hwRun, hwReadFile, hwDiskFreeMiB
	if run != nil {
		hwRun = run
	}
	hwReadFile = func(string) ([]byte, error) {
		if meminfoErr != nil {
			return nil, meminfoErr
		}
		return []byte(meminfo), nil
	}
	if disk != nil {
		hwDiskFreeMiB = disk
	}
	t.Cleanup(func() { hwRun, hwReadFile, hwDiskFreeMiB = origRun, origRead, origDisk })
}

func noTools(string, ...string) (string, bool) { return "", false }

// TestAdvertisedClassIsTheLocalProbesClass. detectHWClass now reads the bucket off
// detectLocalHW instead of counting a second time. That is the whole point of the
// refactor: one probe, one count, so the class the network is told and the report the
// operator reads can never describe two different machines. If these two ever diverge, a
// node is advertising something its own preflight disagrees with.
func TestAdvertisedClassIsTheLocalProbesClass(t *testing.T) {
	cases := []struct {
		name  string
		run   func(string, ...string) (string, bool)
		class string
	}{
		{"two nvidia", func(n string, _ ...string) (string, bool) {
			if n == "nvidia-smi" {
				return "NVIDIA A100, 40960 MiB\nNVIDIA A100, 40960 MiB\n", true
			}
			return "", false
		}, detect.HWMultiGPU},
		{"one nvidia", func(n string, _ ...string) (string, bool) {
			if n == "nvidia-smi" {
				return "NVIDIA GeForce RTX 4090, 24564 MiB\n", true
			}
			return "", false
		}, detect.HWSingleGPU},
		{"one amd", func(n string, args ...string) (string, bool) {
			if n == "rocm-smi" && len(args) > 0 && args[0] == "--showproductname" {
				return "GPU[0]\t\t: Card series: \t\tInstinct MI300X\n", true
			}
			return "", false
		}, detect.HWSingleGPU},
		{"nothing", noTools, detect.HWCPU},
	}
	for _, c := range cases {
		stubHWProbes(t, c.run, "MemTotal: 65809056 kB\n", nil, func(string) (int, bool) { return 400000, true })
		local := detectLocalHW()
		if local.Class != c.class {
			t.Errorf("%s: detectLocalHW().Class = %q, want %q", c.name, local.Class, c.class)
		}
		if adv := detectHWClass(); adv != local.Class {
			t.Errorf("%s: advertised %q but the local report says %q - the network and the "+
				"operator are being told about different machines", c.name, adv, local.Class)
		}
	}
}

// TestLinuxReadsTheRichLocalValues: the whole reason the preflight can be useful is that
// locally it may see what the advertised bucket withholds.
func TestLinuxReadsTheRichLocalValues(t *testing.T) {
	stubHWProbes(t, func(n string, _ ...string) (string, bool) {
		if n == "nvidia-smi" {
			return "NVIDIA GeForce RTX 4090, 24564 MiB\n", true
		}
		return "", false
	}, "MemTotal:       65809056 kB\n", nil, func(string) (int, bool) { return 400000, true })

	hw := detectLocalHW()
	if len(hw.GPUs) != 1 || hw.GPUs[0].Model != "NVIDIA GeForce RTX 4090" {
		t.Errorf("GPUs = %+v, want the model the bucket hides", hw.GPUs)
	}
	if !hw.VRAMKnown || hw.VRAMTotalMiB != 24564 {
		t.Errorf("VRAM = %d/%v, want 24564/true", hw.VRAMTotalMiB, hw.VRAMKnown)
	}
	if !hw.RAMKnown || hw.RAMTotalMiB != 64266 {
		t.Errorf("RAM = %d/%v, want 64266/true", hw.RAMTotalMiB, hw.RAMKnown)
	}
	if !hw.DiskKnown || hw.DiskFreeMiB != 400000 || hw.DiskPath == "" {
		t.Errorf("disk = %d/%v on %q, want 400000/true on a named path", hw.DiskFreeMiB, hw.DiskKnown, hw.DiskPath)
	}
	if hw.CPUCores <= 0 {
		t.Errorf("CPUCores = %d, want the real count", hw.CPUCores)
	}
	if len(hw.Undetermined) != 0 {
		t.Errorf("a fully readable box reported gaps: %v", hw.Undetermined)
	}
}

// TestAMDNeedsTwoCallsForMemory: --showproductname lists the devices and says nothing
// about their size, so the total arrives from a second query - and the "size unknown" note
// the first call justified has to be withdrawn when the second one answers.
func TestAMDNeedsTwoCallsForMemory(t *testing.T) {
	stubHWProbes(t, func(n string, args ...string) (string, bool) {
		if n != "rocm-smi" {
			return "", false
		}
		if len(args) > 0 && args[0] == "--showproductname" {
			return "GPU[0]\t\t: Card series: \t\tInstinct MI300X\n", true
		}
		return "GPU[0]\t\t: vram Total Memory (B): 17163091968\n", true
	}, "MemTotal: 65809056 kB\n", nil, func(string) (int, bool) { return 400000, true })

	hw := detectLocalHW()
	if !hw.VRAMKnown || hw.VRAMTotalMiB != 16368 {
		t.Errorf("VRAM = %d/%v, want 16368/true from the second rocm-smi call", hw.VRAMTotalMiB, hw.VRAMKnown)
	}
	for _, n := range hw.Undetermined {
		if strings.Contains(n, "VRAM") {
			t.Errorf("the report both prints a VRAM total and says it is unknown: %q", n)
		}
	}
}

// TestLinuxDegradesHonestly. Every gap has to be reported as a gap. A zero that could mean
// either "none" or "could not read it" is the exact shape of an overclaim, and this repo's
// rule is that user-facing copy must not overclaim - which includes copy about the
// operator's own machine.
func TestLinuxDegradesHonestly(t *testing.T) {
	stubHWProbes(t, noTools, "", errors.New("no /proc"), func(string) (int, bool) { return 0, false })

	hw := detectLocalHW()
	if hw.RAMKnown || hw.DiskKnown {
		t.Errorf("unreadable values reported as known: RAM %v, disk %v", hw.RAMKnown, hw.DiskKnown)
	}
	joined := strings.Join(hw.Undetermined, " | ")
	for _, want := range []string{"GPU", "/proc/meminfo", "free disk"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no line explains the missing %s: %s", want, joined)
		}
	}
	// And the verdict must not read as a pass on a machine nothing was measured on.
	if p := detect.Assess(hw); p.Clears() {
		t.Errorf("an unmeasurable box cleared the bar:\n%s", p)
	}
}

// TestCoreCountSeam drives the CPU requirement both ways on whatever machine the suite is
// running on, which is the only way that check is exercised at all.
func TestCoreCountSeam(t *testing.T) {
	orig := hwCores
	hwCores = func() int { return 2 }
	t.Cleanup(func() { hwCores = orig })
	stubHWProbes(t, noTools, "MemTotal: 65809056 kB\n", nil, func(string) (int, bool) { return 400000, true })

	if got := detectLocalHW().CPUCores; got != 2 {
		t.Fatalf("CPUCores = %d, want 2", got)
	}
	if p := detect.Assess(detectLocalHW()); p.Verdict != detect.VerdictBelow {
		t.Errorf("a 2-core CPU-only box is %q, want %q", p.Verdict, detect.VerdictBelow)
	}
}
