package detect

import (
	"strings"
	"testing"
)

// goodBox is a machine that clears every requirement, used as the base each case below
// breaks in exactly one way.
func goodBox() LocalHW {
	return LocalHW{
		Class:        HWSingleGPU,
		GPUs:         []LocalGPU{{Model: "NVIDIA GeForce RTX 4090", VRAMMiB: 24564}},
		VRAMTotalMiB: 24564, VRAMKnown: true,
		RAMTotalMiB: 64266, RAMKnown: true,
		DiskFreeMiB: 400000, DiskKnown: true, DiskPath: "/home/op",
		CPUCores: 16,
	}
}

func TestAssessClearsAFullyMeasuredCapableBox(t *testing.T) {
	p := Assess(goodBox())
	if p.Verdict != VerdictClears || !p.Clears() {
		t.Fatalf("verdict = %q, want %q\n%s", p.Verdict, VerdictClears, p)
	}
	if len(Consequences(p.Verdict)) != 0 {
		t.Error("a passing box was told what happens if it shares anyway")
	}
}

// TestAssessBelowOnEachRequirement: every requirement has to be able to fail on its own,
// or one of them is decorative.
func TestAssessBelowOnEachRequirement(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*LocalHW)
		check  string
	}{
		{"vram", func(h *LocalHW) { h.VRAMTotalMiB = BarVRAMMiB - 1 }, "GPU memory"},
		{"ram", func(h *LocalHW) { h.RAMTotalMiB = BarRAMMiB - 1 }, "system RAM"},
		{"disk", func(h *LocalHW) { h.DiskFreeMiB = BarDiskFreeMiB - 1 }, "free disk"},
		{"cpu", func(h *LocalHW) { h.CPUCores = BarCPUCores - 1 }, "CPU cores"},
	}
	for _, c := range cases {
		hw := goodBox()
		c.break_(&hw)
		p := Assess(hw)
		if p.Verdict != VerdictBelow {
			t.Errorf("%s: verdict = %q, want %q", c.name, p.Verdict, VerdictBelow)
			continue
		}
		if !strings.Contains(p.AdvisoryLine(), c.check) {
			t.Errorf("%s: advisory does not name the failed requirement %q: %s", c.name, c.check, p.AdvisoryLine())
		}
	}
}

// TestCPUOnlyIsBelowNotUnknown. "No GPU was found" is a measurement, not a gap: the
// probe ran and the answer was none. Reporting it as UNKNOWN would let the most common
// unsuitable machine on the network slip through as merely unmeasured.
func TestCPUOnlyIsBelowNotUnknown(t *testing.T) {
	hw := goodBox()
	hw.Class, hw.GPUs, hw.VRAMKnown, hw.VRAMTotalMiB = HWCPU, nil, false, 0
	p := Assess(hw)
	if p.Verdict != VerdictBelow {
		t.Fatalf("verdict = %q, want %q\n%s", p.Verdict, VerdictBelow, p)
	}
	if !strings.Contains(p.String(), "no GPU detected") {
		t.Errorf("report does not say plainly that no GPU was found:\n%s", p)
	}
}

// TestAppleUnifiedMemoryIsCheckedAsRAM. There is no VRAM figure on Apple Silicon to
// print, so the requirement is checked against unified memory and the report says which
// quantity it just checked. Printing "VRAM" over a RAM number would be an invented fact
// about the machine.
func TestAppleUnifiedMemoryIsCheckedAsRAM(t *testing.T) {
	hw := LocalHW{Class: HWApple, UnifiedMemory: true,
		RAMTotalMiB: 8192, RAMKnown: true,
		DiskFreeMiB: 400000, DiskKnown: true, DiskPath: "/Users/op", CPUCores: 10}
	p := Assess(hw)
	if p.Verdict != VerdictBelow {
		t.Fatalf("8 GiB unified: verdict = %q, want %q (the unified bar is %d MiB)", p.Verdict, VerdictBelow, BarUnifiedMiB)
	}
	if !strings.Contains(p.String(), "unified") {
		t.Errorf("report does not say the checked quantity was unified memory:\n%s", p)
	}
	if strings.Contains(p.String(), "of VRAM") {
		t.Errorf("report claims a VRAM figure on a machine that has no VRAM pool:\n%s", p)
	}

	hw.RAMTotalMiB = BarUnifiedMiB
	if v := Assess(hw).Verdict; v != VerdictClears {
		t.Errorf("16 GiB unified: verdict = %q, want %q", v, VerdictClears)
	}
}

// TestUnknownDegradesToIncompleteNotToAPass. An unreadable value must not read as a
// passing one; INCOMPLETE says "does not know" and Clears() stays false.
func TestUnknownDegradesToIncompleteNotToAPass(t *testing.T) {
	hw := goodBox()
	hw.RAMKnown, hw.RAMTotalMiB = false, 0
	p := Assess(hw)
	if p.Verdict != VerdictIncomplete {
		t.Fatalf("verdict = %q, want %q\n%s", p.Verdict, VerdictIncomplete, p)
	}
	if p.Clears() {
		t.Error("Clears() is true on an INCOMPLETE report - an unmeasured box would read as a passing one")
	}
	if p.AdvisoryLine() != "" {
		t.Errorf("INCOMPLETE printed an advisory on the normal share path: %q", p.AdvisoryLine())
	}
}

// TestAMeasuredMissBeatsAnUnknown. A machine with one requirement missed and another
// unreadable has an established problem; softening that to INCOMPLETE because some other
// value was unreadable would bury the finding.
func TestAMeasuredMissBeatsAnUnknown(t *testing.T) {
	hw := goodBox()
	hw.DiskKnown, hw.DiskFreeMiB = false, 0
	hw.VRAMTotalMiB = 4096
	if v := Assess(hw).Verdict; v != VerdictBelow {
		t.Errorf("verdict = %q, want %q", v, VerdictBelow)
	}
}

// TestConsequencesDescribeWhatTheCodeActuallyDoes. This is the wording that matters most
// and the easiest to overstate. The honest failure mode of an underpowered node is being
// out-scored, not being refused, and the report must not threaten something the software
// does not do.
func TestConsequencesDescribeWhatTheCodeActuallyDoes(t *testing.T) {
	body := strings.ToLower(strings.Join(Consequences(VerdictBelow), " "))
	for _, must := range []string{
		"nothing refuses you",            // it does not block
		"canary probes",                  // the mechanism that actually decides placement
		"earns approximately nothing",    // the real economic outcome
		"unmeasured node scores neutral", // M1's rule: no penalty before measurement
		"private band",                   // the case where none of this applies
	} {
		if !strings.Contains(body, must) {
			t.Errorf("the below-bar consequences never say %q:\n%s", must, body)
		}
	}
	for _, mustNot := range []string{"refused", "rejected", "not allowed", "banned from"} {
		if strings.Contains(body, mustNot) {
			t.Errorf("the consequences claim %q, which the code does not do:\n%s", mustNot, body)
		}
	}
}

// TestReportNamesTheOneValueThatLeavesTheHost. The privacy claim in the report's first
// line is checkable rather than asserted: the advertised bucket is printed beside
// everything that is not advertised, so an operator can see the difference for themselves.
func TestReportNamesTheOneValueThatLeavesTheHost(t *testing.T) {
	s := Assess(goodBox()).String()
	if !strings.Contains(s, "local only") {
		t.Errorf("report does not open by saying it is local:\n%s", s)
	}
	if !strings.Contains(s, `hw="single-gpu"`) {
		t.Errorf("report does not name the advertised bucket:\n%s", s)
	}
	if !strings.Contains(s, "RTX 4090") {
		t.Errorf("report withholds the GPU model from the operator, who owns it:\n%s", s)
	}
}

// TestUndeterminedLinesAreStableAndDeduplicated: two runs on one machine must produce the
// same report, and a probe that fails twice must not say so twice.
func TestUndeterminedLinesAreStableAndDeduplicated(t *testing.T) {
	hw := goodBox()
	hw.note("z: second")
	hw.note("a: first")
	hw.note("z: second")
	s := Assess(hw).String()
	if strings.Count(s, "z: second") != 1 {
		t.Errorf("a repeated note printed twice:\n%s", s)
	}
	if strings.Index(s, "a: first") > strings.Index(s, "z: second") {
		t.Errorf("notes are not in a stable order:\n%s", s)
	}
}

// TestDiskCheckNamesTheFilesystemItMeasured. We cannot know where the upstream server
// keeps its weights, so the honest scope of the number is the filesystem it came from -
// and the report has to say which one that was.
func TestDiskCheckNamesTheFilesystemItMeasured(t *testing.T) {
	hw := goodBox()
	hw.DiskPath = "/var/lib/roger"
	if s := Assess(hw).String(); !strings.Contains(s, "/var/lib/roger") {
		t.Errorf("the free-disk line does not say which filesystem was measured:\n%s", s)
	}
}
