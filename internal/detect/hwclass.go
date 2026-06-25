package detect

import "strings"

// HWClass is the PRIVACY-BUCKETED hardware class a node advertises. It is a coarse
// category ONLY - never the exact rig, GPU model, count, or VRAM beyond the bucket -
// so a consumer learns "this band runs on multiple GPUs" without learning "this is a
// 4x RTX PRO 4500 box". The node owner's hardware is sensitive; the bucket is the
// public-safe summary, consistent with how node_id/region are already pseudonymized.
const (
	HWMultiGPU  = "multi-gpu"  // 2+ discrete GPUs
	HWSingleGPU = "single-gpu" // exactly 1 discrete GPU
	HWApple     = "apple"      // Apple Silicon / unified memory
	HWCPU       = "cpu"        // no GPU detected - CPU inference
	HWUnknown   = "unknown"    // detection failed / could not determine
)

// BucketGPUCount maps a discrete-GPU count to the privacy-safe class. 0 -> cpu,
// 1 -> single-gpu, 2+ -> multi-gpu. The exact count is deliberately collapsed past
// 1 so a multi-GPU rig's precise size never leaks.
func BucketGPUCount(n int) string {
	switch {
	case n >= 2:
		return HWMultiGPU
	case n == 1:
		return HWSingleGPU
	default:
		return HWCPU
	}
}

// CountNvidiaSMI parses the output of
// `nvidia-smi --query-gpu=name,memory.total --format=csv,noheader`
// into a discrete-GPU count. Each non-empty line is one GPU. The per-GPU name/VRAM
// are intentionally DROPPED here - only the count crosses into the class - so the
// caller cannot accidentally advertise the exact rig. Returns 0 on empty input.
func CountNvidiaSMI(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// CountROCmSMI parses `rocm-smi --showproductname` (or similar) output into a
// discrete-GPU count by counting "GPU[<n>]" / "Card series" style lines. Best-effort
// across rocm-smi versions: it counts distinct "GPU[" index markers, falling back to
// counting "Card" lines. Only the count is returned, never the product name.
func CountROCmSMI(out string) int {
	idx := map[string]bool{}
	cards := 0
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if i := strings.Index(l, "GPU["); i >= 0 {
			rest := l[i+4:]
			if j := strings.Index(rest, "]"); j >= 0 {
				idx[rest[:j]] = true
			}
			continue
		}
		if strings.Contains(strings.ToLower(l), "card series") || strings.Contains(strings.ToLower(l), "card model") {
			cards++
		}
	}
	if len(idx) > 0 {
		return len(idx)
	}
	return cards
}
