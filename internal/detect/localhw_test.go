package detect

import (
	"strings"
	"testing"
)

// TestParseNvidiaGPUsKeepsModelAndVRAM: the local parser keeps exactly what the
// advertised-class count throws away. The details are safe to keep here BECAUSE this
// result is never transmitted; the count that is transmitted is derived from the same
// parse so the two cannot disagree about how many cards are in the box.
func TestParseNvidiaGPUsKeepsModelAndVRAM(t *testing.T) {
	out := "NVIDIA GeForce RTX 4090, 24564 MiB\nNVIDIA A100-SXM4-40GB, 40960 MiB\n"
	gpus := ParseNvidiaGPUs(out)
	if len(gpus) != 2 {
		t.Fatalf("ParseNvidiaGPUs = %d GPUs, want 2: %+v", len(gpus), gpus)
	}
	if gpus[0].Model != "NVIDIA GeForce RTX 4090" || gpus[0].VRAMMiB != 24564 {
		t.Errorf("first GPU = %+v, want RTX 4090 / 24564 MiB", gpus[0])
	}
	if gpus[1].VRAMMiB != 40960 {
		t.Errorf("second GPU VRAM = %d, want 40960", gpus[1].VRAMMiB)
	}
	if n := CountNvidiaSMI(out); n != len(gpus) {
		t.Errorf("CountNvidiaSMI = %d but the local parse found %d - the advertised class "+
			"and the local report would describe different machines", n, len(gpus))
	}
}

// TestParseNvidiaGPUsUnitlessAndUnknown: nvidia-smi's --format=csv,nounits drops the
// suffix, and some virtualised devices answer "N/A" for memory. A device that does not
// report its size is still a device - dropping it would under-count the rig, and the
// count is the value that reaches the network.
func TestParseNvidiaGPUsUnitlessAndUnknown(t *testing.T) {
	gpus := ParseNvidiaGPUs("Tesla T4, 15360\nGRID A100-4C, [N/A]\n")
	if len(gpus) != 2 {
		t.Fatalf("got %d GPUs, want 2 (a device with unreadable memory must still count)", len(gpus))
	}
	if gpus[0].VRAMMiB != 15360 {
		t.Errorf("unit-less memory parsed as %d, want 15360 MiB", gpus[0].VRAMMiB)
	}
	if gpus[1].VRAMMiB != 0 {
		t.Errorf("N/A memory parsed as %d, want 0 (meaning undetermined)", gpus[1].VRAMMiB)
	}
}

// TestSetGPUsWithholdsAPartialVRAMTotal: one silent card makes the TOTAL unknown, not
// smaller. Summing what was reported would under-state the rig by however much the silent
// card holds, and a confidently wrong number is the exact failure this check exists to
// avoid.
func TestSetGPUsWithholdsAPartialVRAMTotal(t *testing.T) {
	var hw LocalHW
	hw.SetGPUs([]LocalGPU{{Model: "a", VRAMMiB: 24564}, {Model: "b"}})
	if hw.VRAMKnown {
		t.Errorf("VRAMKnown = true with a silent card; total would read %d", hw.VRAMTotalMiB)
	}
	if hw.Class != HWMultiGPU {
		t.Errorf("Class = %q, want %q - the count is unaffected by unreadable memory", hw.Class, HWMultiGPU)
	}
	if len(hw.Undetermined) != 1 || !strings.Contains(hw.Undetermined[0], "VRAM") {
		t.Errorf("Undetermined = %v, want one line naming VRAM", hw.Undetermined)
	}
}

// TestSetVRAMTotalWithdrawsTheUndeterminedNote: AMD needs two rocm-smi calls, so the
// device list legitimately reports "memory unknown" a moment before the memory query
// answers. A report that prints a VRAM total AND says the total is undetermined is worse
// than either alone.
func TestSetVRAMTotalWithdrawsTheUndeterminedNote(t *testing.T) {
	var hw LocalHW
	hw.SetGPUs([]LocalGPU{{Model: "AMD GPU"}})
	if len(hw.Undetermined) != 1 {
		t.Fatalf("setup: Undetermined = %v, want the VRAM note", hw.Undetermined)
	}
	hw.SetVRAMTotal(16368)
	if !hw.VRAMKnown || hw.VRAMTotalMiB != 16368 {
		t.Errorf("VRAM = %d/%v, want 16368/true", hw.VRAMTotalMiB, hw.VRAMKnown)
	}
	if len(hw.Undetermined) != 0 {
		t.Errorf("Undetermined = %v, want empty - the note was answered", hw.Undetermined)
	}
}

// TestParseROCmGPUsAgreesWithTheCount across the output shapes rocm-smi has shipped. The
// count feeds the advertised class, so a divergence here is a divergence in what the
// network is told.
func TestParseROCmGPUsAgreesWithTheCount(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"indexed", "GPU[0]\t\t: Card series: \t\tInstinct MI300X\nGPU[1]\t\t: Card series: \t\tInstinct MI300X\n", 2},
		{"index only", "GPU[0]\nGPU[0]\n", 1},
		{"card lines only", "Card series: \t\tRadeon RX 7900 XTX\nCard model: \t\t0x744c\n", 2},
		{"empty", "", 0},
	}
	for _, c := range cases {
		gpus := ParseROCmGPUs(c.out)
		if len(gpus) != c.want {
			t.Errorf("%s: ParseROCmGPUs = %d, want %d (%+v)", c.name, len(gpus), c.want, gpus)
		}
		if n := CountROCmSMI(c.out); n != len(gpus) {
			t.Errorf("%s: CountROCmSMI = %d but ParseROCmGPUs = %d", c.name, n, len(gpus))
		}
	}
	if gpus := ParseROCmGPUs("GPU[0]\t\t: Card series: \t\tInstinct MI300X\n"); gpus[0].Model != "Instinct MI300X" {
		t.Errorf("product name = %q, want %q", gpus[0].Model, "Instinct MI300X")
	}
}

// TestParseROCmVRAMRequiresAnExplicitUnit: rocm-smi's memory output has changed shape
// repeatedly, and the failure mode of guessing the unit is being wrong by a factor of a
// million. A line whose unit cannot be established is skipped, and if none survive the
// total is reported unknown rather than zero.
func TestParseROCmVRAMRequiresAnExplicitUnit(t *testing.T) {
	total, ok := ParseROCmVRAMMiB("GPU[0]\t\t: vram Total Memory (B): 17163091968\n")
	if !ok || total != 16368 {
		t.Errorf("bytes: got %d/%v, want 16368/true", total, ok)
	}
	if total, ok := ParseROCmVRAMMiB("VRAM Total Memory (MB): 24576\n"); !ok || total != 24576 {
		t.Errorf("MB: got %d/%v, want 24576/true", total, ok)
	}
	if total, ok := ParseROCmVRAMMiB("vram total memory: 17163091968\n"); ok {
		t.Errorf("unit-less line parsed as %d MiB - it must be refused, not guessed", total)
	}
	if _, ok := ParseROCmVRAMMiB(""); ok {
		t.Error("empty output reported ok - unknown must not read as a measurement")
	}
}

// TestParseMemTotalMiB, including the container case where /proc is masked and the field
// is simply absent. "Absent" has to be distinguishable from "zero".
func TestParseMemTotalMiB(t *testing.T) {
	mib, ok := ParseMemTotalMiB("MemTotal:       65809056 kB\nMemFree:  100 kB\n")
	if !ok || mib != 64266 {
		t.Errorf("got %d/%v, want 64266/true", mib, ok)
	}
	if _, ok := ParseMemTotalMiB("MemFree: 100 kB\n"); ok {
		t.Error("a meminfo with no MemTotal reported ok")
	}
	if _, ok := ParseMemTotalMiB("MemTotal:  notanumber kB\n"); ok {
		t.Error("an unparseable MemTotal reported ok")
	}
}

// TestParseSysctlBytesMiB covers the macOS/BSD RAM path.
func TestParseSysctlBytesMiB(t *testing.T) {
	mib, ok := ParseSysctlBytesMiB("  17179869184\n")
	if !ok || mib != 16384 {
		t.Errorf("got %d/%v, want 16384/true", mib, ok)
	}
	if _, ok := ParseSysctlBytesMiB("hw.memsize: unknown"); ok {
		t.Error("non-numeric sysctl output reported ok")
	}
}
