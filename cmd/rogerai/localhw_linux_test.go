//go:build linux

package main

import (
	"errors"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"

	"rogerai.fm/roger/v7/internal/detect"
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

// Found with tools/edgesim (2026-10-04): in a container, /proc/meminfo is the HOST's memory, while
// the container may be held to far less. The limit is what a model can actually use, so it is the
// RAM roger judges a fit against - and it says so.
func TestAContainersMemoryLimitIsTheRAMAModelCanUse(t *testing.T) {
	files := map[string]string{}
	origRun, origRead, origDisk := hwRun, hwReadFile, hwDiskFreeMiB
	t.Cleanup(func() { hwRun, hwReadFile, hwDiskFreeMiB = origRun, origRead, origDisk })
	hwRun = noTools
	hwDiskFreeMiB = func(string) (int, bool) { return 100000, true }
	hwReadFile = func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	files["/proc/meminfo"] = "MemTotal: 131072000 kB\n" // 125 GiB on the host
	files["/sys/fs/cgroup/memory.max"] = "8589934592\n"
	hw := detectLocalHW()
	require.Equal(t, 8192, hw.RAMTotalMiB, "an 8 GiB limit is the RAM a model can use")
	require.Contains(t, strings.Join(hw.Undetermined, " "), "memory limit")

	files["/sys/fs/cgroup/memory.max"] = "max\n"
	require.Equal(t, 128000, detectLocalHW().RAMTotalMiB, "no limit: the host's RAM")

	delete(files, "/sys/fs/cgroup/memory.max")
	files["/sys/fs/cgroup/memory/memory.limit_in_bytes"] = "9223372036854771712\n" // cgroup v1 "unlimited"
	require.Equal(t, 128000, detectLocalHW().RAMTotalMiB)
	files["/sys/fs/cgroup/memory/memory.limit_in_bytes"] = "4294967296\n"
	require.Equal(t, 4096, detectLocalHW().RAMTotalMiB, "a cgroup v1 limit counts too")

	files["/sys/fs/cgroup/memory.max"] = "274877906944\n" // a limit above the host's RAM is no limit
	delete(files, "/sys/fs/cgroup/memory/memory.limit_in_bytes")
	require.Equal(t, 128000, detectLocalHW().RAMTotalMiB)
}

// Pre-push audit (2026-10-26): the limit was read only at the cgroup mount's root, which is the limit
// inside a container but not on a host, where roger runs in its own cgroup - a boot service with
// MemoryMax= set, or a slice above it with a limit of its own. The limit is the tightest one on the
// way from roger's own cgroup to the root.
func TestTheMemoryLimitIsReadFromRogersOwnCgroupAndItsParents(t *testing.T) {
	files := map[string]string{}
	origRun, origRead, origDisk := hwRun, hwReadFile, hwDiskFreeMiB
	t.Cleanup(func() { hwRun, hwReadFile, hwDiskFreeMiB = origRun, origRead, origDisk })
	hwRun = noTools
	hwDiskFreeMiB = func(string) (int, bool) { return 100000, true }
	hwReadFile = func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	files["/proc/meminfo"] = "MemTotal: 131072000 kB\n"
	files["/proc/self/cgroup"] = "0::/system.slice/roger-edge.service\n"
	files["/sys/fs/cgroup/system.slice/roger-edge.service/memory.max"] = "2147483648\n" // MemoryMax=2G
	files["/sys/fs/cgroup/system.slice/memory.max"] = "max\n"
	require.Equal(t, 2048, detectLocalHW().RAMTotalMiB, "a boot service's own limit")

	files["/sys/fs/cgroup/system.slice/roger-edge.service/memory.max"] = "max\n"
	files["/sys/fs/cgroup/system.slice/memory.max"] = "4294967296\n"
	require.Equal(t, 4096, detectLocalHW().RAMTotalMiB, "a limit on a parent holds the child too")

	files["/sys/fs/cgroup/system.slice/roger-edge.service/memory.max"] = "3221225472\n"
	require.Equal(t, 3072, detectLocalHW().RAMTotalMiB, "the tightest limit on the way up")

	files["/proc/self/cgroup"] = "0::/../../etc\n"
	delete(files, "/sys/fs/cgroup/system.slice/roger-edge.service/memory.max")
	delete(files, "/sys/fs/cgroup/system.slice/memory.max")
	require.Equal(t, 128000, detectLocalHW().RAMTotalMiB, "a path that climbs out is not followed")
}
