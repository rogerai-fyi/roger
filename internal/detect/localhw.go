package detect

// localhw.go is the LOCAL half of the minimum hardware requirement, and it exists
// because of a constraint that reads backwards until you see it stated: the network is
// forbidden from knowing any of this.
//
// hwclass.go carries the PUBLIC answer - a four-value bucket (multi-gpu / single-gpu /
// apple / cpu) chosen so that a consumer learns the tier of a band without learning the
// rig behind it. docs/relay-selection-design.md §4.1 goes further and says the supply
// side must not be believed about its own capability at all, because a capability the
// node declares is a lever: claim the best hardware, receive the most work. That has
// already been found twice in this tree (a decorative `--region`, and a self-declared
// `hw` that was moving edge placement by 2x).
//
// So there are two audiences and they get different things:
//
//   - the NETWORK gets the bucket, and even that is only a cold-start prior it corrects
//     with its own measurements;
//   - the OPERATOR gets everything below - GPU model, VRAM, system RAM, free disk, core
//     count - because it is their machine, they are entitled to know whether putting it
//     on the network is worth their electricity, and none of it is transmitted.
//
// LocalHW is therefore deliberately NOT reachable from anything that serializes to the
// wire. It is gathered in `roger share`'s process, rendered to that operator's terminal,
// and dropped. cmd/rogerai pins that with a test.
//
// The parsers here are separate from the platform gatherers on purpose. Shelling out to
// nvidia-smi is untestable on a GPU-less CI box; parsing its output is not, and the
// parsers are where the mistakes live.

import (
	"strconv"
	"strings"
)

// LocalGPU is one accelerator as the host itself sees it. VRAMMiB is 0 when the tool
// reported the device but not its memory (nvidia-smi answers "N/A" for some virtualised
// and older devices), which is a different fact from "the device has no memory" and is
// reported as undetermined rather than as zero.
type LocalGPU struct {
	Model   string
	VRAMMiB int
}

// LocalHW is the rich, local-only hardware picture. Every "Known" flag exists because
// the alternative - a zero that could mean either "none" or "could not read it" - is the
// exact shape of an overclaim, and this repo's standing rule is that user-facing copy
// must not overclaim. A preflight that says "0 MiB VRAM" on a machine whose VRAM it
// simply could not read has lied to the operator about their own hardware.
type LocalHW struct {
	// Class is the privacy bucket this node WOULD advertise. It is carried here so the
	// preflight can show the operator the one value that leaves the host, next to all the
	// values that do not. It must equal what detectHWClass() would return; cmd/rogerai
	// pins that equality with a test.
	Class string

	GPUs         []LocalGPU
	VRAMTotalMiB int
	VRAMKnown    bool

	// UnifiedMemory marks a machine where there is no separate VRAM pool to measure
	// because the GPU addresses system RAM directly (Apple Silicon). The accelerator
	// requirement is then checked against system RAM, and the report says so rather than
	// printing a VRAM figure that does not exist.
	UnifiedMemory bool

	RAMTotalMiB int
	RAMKnown    bool

	// DiskFreeMiB is free space on the filesystem holding DiskPath. DiskPath is reported
	// verbatim because the honest scope of the number is "this filesystem": we cannot know
	// where Ollama, LM Studio or llama.cpp actually keep their weights, and guessing would
	// produce a confident number about the wrong disk.
	DiskFreeMiB int
	DiskKnown   bool
	DiskPath    string

	CPUCores int

	// Undetermined is one plain-language line per thing this platform could not read, and
	// why. "Could not determine" is a fine answer; a guess dressed as a measurement is not.
	Undetermined []string
}

// note records a thing this platform could not determine, in words an operator can act
// on. Duplicate lines are dropped so a probe that fails twice does not say so twice.
func (h *LocalHW) note(s string) {
	for _, existing := range h.Undetermined {
		if existing == s {
			return
		}
	}
	h.Undetermined = append(h.Undetermined, s)
}

// Note is the exported form of note, for platform gatherers in package main.
func (h *LocalHW) Note(s string) { h.note(s) }

// SetGPUs records the enumerated accelerators and derives the two facts that follow from
// them: the privacy bucket (via the SAME BucketGPUCount the advertised class uses, so the
// two can never disagree) and the total VRAM. Total VRAM is only "known" when EVERY
// enumerated device reported its memory - a 2-GPU box where one card answered "N/A" has
// an unknown total, not a half total, and summing anyway would under-report a rig by
// however much the silent card holds.
func (h *LocalHW) SetGPUs(gpus []LocalGPU) {
	h.GPUs = gpus
	h.Class = BucketGPUCount(len(gpus))
	if len(gpus) == 0 {
		return
	}
	total, all := 0, true
	for _, g := range gpus {
		if g.VRAMMiB <= 0 {
			all = false
			continue
		}
		total += g.VRAMMiB
	}
	if all {
		h.VRAMTotalMiB, h.VRAMKnown = total, true
		return
	}
	h.note(vramUnreportedNote)
}

// vramUnreportedNote is named rather than inline because SetVRAMTotal has to be able to
// withdraw it: on AMD the device list and the memory sizes come from two different
// rocm-smi invocations, so SetGPUs legitimately records "no memory reported" a moment
// before the second query supplies it.
const vramUnreportedNote = "VRAM: at least one GPU did not report its memory size, so the total is unknown"

// SetVRAMTotal records a VRAM total that arrived from a SEPARATE query rather than from
// the device enumeration, and withdraws the note SetGPUs left when the enumeration itself
// was silent about memory. A report that both prints a VRAM total and says the VRAM total
// could not be determined is worse than either alone.
func (h *LocalHW) SetVRAMTotal(mib int) {
	if mib <= 0 {
		return
	}
	h.VRAMTotalMiB, h.VRAMKnown = mib, true
	kept := h.Undetermined[:0]
	for _, n := range h.Undetermined {
		if n != vramUnreportedNote {
			kept = append(kept, n)
		}
	}
	h.Undetermined = kept
}

// ParseNvidiaGPUs parses
// `nvidia-smi --query-gpu=name,memory.total --format=csv,noheader` into one LocalGPU per
// device. This is the same command hwclass.go's CountNvidiaSMI already consumes for the
// count - the difference is only that the count deliberately DISCARDS the model and the
// memory, and this deliberately keeps them, because this output never leaves the host.
//
// The memory column carries its unit ("24564 MiB"), and nvidia-smi answers "N/A" for
// devices that do not report one; both are handled, and an unparseable memory column
// yields VRAMMiB 0 (undetermined) rather than dropping the GPU, since the device is
// unambiguously present even when its size is not readable.
func ParseNvidiaGPUs(out string) []LocalGPU {
	var gpus []LocalGPU
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		model, mem := line, ""
		if i := strings.LastIndex(line, ","); i >= 0 {
			model, mem = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		gpus = append(gpus, LocalGPU{Model: model, VRAMMiB: parseMemFieldMiB(mem)})
	}
	return gpus
}

// parseMemFieldMiB reads a memory quantity that may or may not carry a unit suffix
// ("24564 MiB", "24564", "23 GiB", "N/A"). Unit-less is MiB, which is what nvidia-smi's
// --format=csv,nounits produces. Anything it cannot read is 0, meaning undetermined.
func parseMemFieldMiB(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := 1
	low := strings.ToLower(s)
	switch {
	case strings.HasSuffix(low, "gib"), strings.HasSuffix(low, "gb"):
		mult = 1024
	case strings.HasSuffix(low, "kib"), strings.HasSuffix(low, "kb"):
		// A sub-MiB accelerator does not exist, so a KiB suffix here means the output is
		// not what we think it is. Zero, which the caller reads as UNDETERMINED - the one
		// answer that cannot be wrong about a number nobody understands.
		mult = 0
	}
	digits := strings.TrimLeftFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	digits = strings.TrimRightFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
	if digits == "" {
		return 0
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return n * mult
}

// ParseROCmGPUs parses `rocm-smi --showproductname` into one LocalGPU per device.
//
// It is the single implementation behind BOTH the local report and hwclass.go's
// CountROCmSMI, which is deliberate: two parsers over the same output would eventually
// disagree about how many GPUs this box has, and the one that feeds the advertised class
// is the one that must not drift. rocm-smi's output has changed shape across releases, so
// this counts distinct "GPU[<n>]" index markers and falls back to counting "Card series" /
// "Card model" lines, exactly as the count always has.
//
// The product name is captured when the output offers one and is otherwise left as a
// generic label - it is shown to the operator only, and never contributes to the count.
func ParseROCmGPUs(out string) []LocalGPU {
	order := []string{}
	byIdx := map[string]string{}
	var cards []LocalGPU
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if i := strings.Index(l, "GPU["); i >= 0 {
			rest := l[i+4:]
			j := strings.Index(rest, "]")
			if j < 0 {
				continue
			}
			idx := rest[:j]
			if _, seen := byIdx[idx]; !seen {
				order = append(order, idx)
				byIdx[idx] = ""
			}
			if name := rocmSeriesName(l); name != "" {
				byIdx[idx] = name
			}
			continue
		}
		low := strings.ToLower(l)
		if strings.Contains(low, "card series") || strings.Contains(low, "card model") {
			name := rocmSeriesName(l)
			if name == "" {
				name = "AMD GPU"
			}
			cards = append(cards, LocalGPU{Model: name})
		}
	}
	if len(order) > 0 {
		gpus := make([]LocalGPU, 0, len(order))
		for _, idx := range order {
			name := byIdx[idx]
			if name == "" {
				name = "AMD GPU"
			}
			gpus = append(gpus, LocalGPU{Model: name})
		}
		return gpus
	}
	return cards
}

// rocmSeriesName pulls the product name off a "... Card series: Instinct MI300X" line.
// Returns "" for any line that does not carry one, including the index-only lines that
// exist purely to establish that a device number is in use.
func rocmSeriesName(line string) string {
	low := strings.ToLower(line)
	if !strings.Contains(low, "card series") && !strings.Contains(low, "card model") {
		return ""
	}
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(line[i+1:])
}

// ParseROCmVRAMMiB parses `rocm-smi --showmeminfo vram` into a total across devices.
//
// rocm-smi's output has changed shape repeatedly across releases, so this reads ONLY
// lines that state their unit explicitly - "vram Total Memory (B): 17163091968" and its
// (KB)/(MB)/(GB) variants. A line whose unit cannot be established is skipped and the
// total is reported unknown, because the failure mode of guessing here is off by a factor
// of a million in either direction. ok is false when no usable line was found.
func ParseROCmVRAMMiB(out string) (total int, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		if l == "" || !strings.Contains(l, "vram") || !strings.Contains(l, "total") {
			continue
		}
		div := 0
		switch {
		case strings.Contains(l, "(b)"):
			div = 1024 * 1024
		case strings.Contains(l, "(kb)"), strings.Contains(l, "(kib)"):
			div = 1024
		case strings.Contains(l, "(mb)"), strings.Contains(l, "(mib)"):
			div = 1
		case strings.Contains(l, "(gb)"), strings.Contains(l, "(gib)"):
			div = -1024 // negative marks a multiplier rather than a divisor
		default:
			continue
		}
		i := strings.LastIndex(l, ":")
		if i < 0 {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(l[i+1:]), 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		if div < 0 {
			total += int(n * int64(-div))
		} else {
			total += int(n / int64(div))
		}
		ok = true
	}
	return total, ok
}

// ParseMemTotalMiB reads MemTotal out of a Linux /proc/meminfo. The kernel always
// reports it in kB (the label says "kB" and means KiB), so no unit sniffing is needed;
// ok is false when the field is absent, which is the case on a stripped container where
// /proc is not mounted.
func ParseMemTotalMiB(meminfo string) (int, bool) {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || kb <= 0 {
			return 0, false
		}
		return int(kb / 1024), true
	}
	return 0, false
}

// ParseSysctlBytesMiB reads a bare byte count printed by `sysctl -n` (hw.memsize on
// macOS, hw.physmem on BSD) and converts it to MiB.
func ParseSysctlBytesMiB(out string) (int, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return int(n / (1024 * 1024)), true
}
