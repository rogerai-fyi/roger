package localmodel

// IS IT USABLE HERE: how fast a model that fits would answer (local_models.feature §4, "fitting and
// being usable are two different claims"). Writing one token reads every weight once, so the speed
// is the pool's bandwidth over the bytes read per token. The bandwidth comes from the maker's sheet
// for a card or chip roger knows, or from reading this machine's memory; either way it says which.

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/detect"
)

// Bandwidth is how fast the model's pool can be read, and where the number came from.
type Bandwidth struct {
	BytesPerSec float64
	Measured    bool   // read on this machine, rather than from a spec sheet
	From        string // where the number came from, for the owner to check
	Why         string // when BytesPerSec is zero: why there is no number
}

// specGBps is memory bandwidth in GB/s from the makers' published specifications. A card that is
// not here is "cannot say", never a guess. Where one name covers parts with different memory
// (a binned M3 Max), the slower figure is the one written down.
var specGBps = map[string]float64{
	// NVIDIA GeForce
	"RTX 5090": 1792, "RTX 5080": 960, "RTX 5070 Ti": 896, "RTX 5070": 672, "RTX 5060 Ti": 448,
	"RTX 4090": 1008, "RTX 4080 SUPER": 736, "RTX 4080": 716.8, "RTX 4070 Ti SUPER": 672, "RTX 4070 Ti": 504,
	"RTX 4070 SUPER": 504, "RTX 4070": 504, "RTX 4060 Ti": 288, "RTX 4060": 272,
	"RTX 3090 Ti": 1008, "RTX 3090": 936, "RTX 3080 Ti": 912, "RTX 3080": 760, "RTX 3070 Ti": 608,
	"RTX 3070": 448, "RTX 3060 Ti": 448, "RTX 3060": 360,
	// NVIDIA workstation and data centre
	"RTX PRO 6000 Blackwell": 1792, "RTX PRO 5000 Blackwell": 1344, "RTX PRO 4500 Blackwell": 896,
	"RTX PRO 4000 Blackwell": 672, "RTX 6000 Ada": 960, "RTX A6000": 768,
	"A100-SXM4-80GB": 2039, "A100 80GB PCIe": 1935, "A100-PCIE-40GB": 1555, "H100 PCIe": 2000,
	"H100 80GB HBM3": 3350, "L40S": 864, "L4": 300, "Tesla T4": 320, "V100": 900,
	// Apple silicon, one pool
	"Apple M1": 68.25, "Apple M1 Pro": 200, "Apple M1 Max": 400, "Apple M1 Ultra": 800,
	"Apple M2": 100, "Apple M2 Pro": 200, "Apple M2 Max": 400, "Apple M2 Ultra": 800,
	"Apple M3": 100, "Apple M3 Pro": 150, "Apple M3 Max": 300, "Apple M3 Ultra": 819,
	"Apple M4": 120, "Apple M4 Pro": 273, "Apple M4 Max": 410,
}

// specFor finds a device's figure by the longest name it contains, so an RTX 4080 SUPER is not read
// as an RTX 4080, and "Apple M2" does not answer for an M2 Max.
func specFor(model string) (string, float64) {
	best, bw := "", 0.0
	for name, v := range specGBps {
		if len(name) > len(best) && containsWord(model, name) {
			best, bw = name, v
		}
	}
	return best, bw * 1e9
}

// containsWord is a case-insensitive match that ends on a word boundary, so "RTX 4070" is not
// found inside "RTX 4070 Ti" by a shorter key winning on length alone, and "L4" not inside "L40S".
func containsWord(s, word string) bool {
	s, word = strings.ToLower(s), strings.ToLower(word)
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		end := i + j + len(word)
		before := i+j == 0 || !isWordByte(s[i+j-1])
		if before && (end == len(s) || !isWordByte(s[end])) {
			return true
		}
		i += j + 1
	}
}

func isWordByte(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

func bandwidthOf(hw detect.LocalHW, measure func(ramMiB int) Bandwidth) Bandwidth {
	switch {
	case hw.UnifiedMemory && hw.RAMKnown:
		if len(hw.GPUs) > 0 {
			if name, bw := specFor(hw.GPUs[0].Model); bw > 0 {
				return Bandwidth{BytesPerSec: bw, From: fmt.Sprintf("spec sheet: %s %.0f GB/s", name, bw/1e9)}
			}
		}
		m := measure(hw.RAMTotalMiB)
		if m.BytesPerSec > 0 {
			m.From += "; the GPU may read faster than the processor that measured it"
		}
		return m
	case hw.VRAMKnown && hw.VRAMTotalMiB > 0:
		// Layers are split across cards in proportion to their memory, and a token passes through
		// every card in turn: the time per byte is each card's share over its own speed, summed.
		var total float64
		for _, g := range hw.GPUs {
			total += float64(g.VRAMMiB)
		}
		var perByte float64
		names := []string{}
		for _, g := range hw.GPUs {
			name, bw := specFor(g.Model)
			if bw == 0 {
				return Bandwidth{Why: fmt.Sprintf("the memory speed of %s is not in roger's table", g.Model)}
			}
			share := 1 / float64(len(hw.GPUs))
			if total > 0 {
				share = float64(g.VRAMMiB) / total
			}
			perByte += share / bw
			names = append(names, fmt.Sprintf("%s %.0f GB/s", name, bw/1e9))
		}
		if perByte == 0 {
			return Bandwidth{Why: "no card was named"}
		}
		return Bandwidth{BytesPerSec: 1 / perByte, From: "spec sheet: " + strings.Join(dedupe(names), ", ")}
	case hw.RAMKnown && hw.RAMTotalMiB > 0:
		return measure(hw.RAMTotalMiB)
	}
	return Bandwidth{Why: "this machine's memory could not be read"}
}

func dedupe(xs []string) []string {
	seen, out := map[string]int{}, []string{}
	for _, x := range xs {
		if seen[x] == 0 {
			out = append(out, x)
		}
		seen[x]++
	}
	for i, x := range out {
		if n := seen[x]; n > 1 {
			out[i] = fmt.Sprintf("%d x %s", n, x)
		}
	}
	return out
}

var ramOnce struct {
	sync.Once
	bw Bandwidth
}

// MeasureRAM reads a buffer larger than any cache on every core at once and times it. Once per
// process: a monitor that benchmarks memory every refresh would become the load it reports.
func MeasureRAM(ramMiB int) Bandwidth {
	ramOnce.Do(func() { ramOnce.bw = measureRAM(measureSize(ramMiB), 150*time.Millisecond) })
	return ramOnce.bw
}

// measureSize is an eighth of the machine's memory, between 64 MiB and 1 GiB: past any processor's
// cache (a server part carries hundreds of megabytes), and small enough that a board can spare it.
func measureSize(ramMiB int) int {
	const mib = 1 << 20
	return min(max(ramMiB/8, 64), 1024) * mib
}

func measureRAM(size int, d time.Duration) Bandwidth {
	buf := make([]uint64, size/8)
	for i := range buf {
		buf[i] = uint64(i) // written first: untouched pages all map the one zero page, which caches
	}
	workers := runtime.NumCPU()
	part := len(buf) / workers
	var mu sync.Mutex
	var read int64
	var sink uint64
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(s []uint64) {
			defer wg.Done()
			var n int64
			var acc uint64
			for time.Since(start) < d {
				for i := 0; i+4 <= len(s); i += 4 {
					acc += s[i] ^ s[i+1] ^ s[i+2] ^ s[i+3]
				}
				n += int64(len(s) * 8)
			}
			mu.Lock()
			read += n
			sink += acc
			mu.Unlock()
		}(buf[w*part : (w+1)*part])
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	_ = sink
	bps := float64(read) / el
	return Bandwidth{BytesPerSec: bps, Measured: true,
		From: fmt.Sprintf("measured %.1f GB/s reading memory on %d cores", bps/1e9, workers)}
}

// Pace is how fast a model would write, and how sure roger is of it.
type Pace struct {
	TokPerSec float64
	Word      string // "usable", "slow", "too slow to chat"; empty when there is no number
	AtLeast   bool   // a mixture of experts reads part of its weights, so this is a floor
	PerToken  int64  // bytes read per token in the sum
	BW        Bandwidth
}

// The share of the stated bandwidth the runtime reaches writing tokens. A spec sheet is a peak. A
// measurement is what this machine's memory does, but the runtime runs one thread per physical core
// and does work besides reading: calibrated against llama-bench on a 64-thread workstation (159.7
// GB/s measured, Qwen3-0.6B Q4_K_M: 233 tok/s real against 241 from this sum).
const (
	achievedSpec     = 0.7
	achievedMeasured = 0.6
)

// paceOf is the sum: bandwidth over the weights, read once per token at the start of a conversation.
// gpuLayerCost is the fixed work each layer does per token on a GPU, beyond reading its weights:
// measured with llama-bench b11146 on an idle RTX PRO 6000 (Vulkan, Qwen3-0.6B Q4_K_M, 28 layers:
// 538 tok/s, of which reading the weights explains only a sixth). It is what bounds a small model on a
// fast card. A measured (CPU) bandwidth already includes it, so it is added to spec-sheet figures only.
const gpuLayerCost = 55e-6 // seconds per layer per token

func paceOf(fileBytes int64, layers, experts, used int, bw Bandwidth) Pace {
	p := Pace{BW: bw, PerToken: fileBytes, AtLeast: experts > 0 && used > 0 && used < experts}
	if bw.BytesPerSec <= 0 || fileBytes <= 0 {
		return p
	}
	eff := achievedSpec
	if bw.Measured {
		eff = achievedMeasured
	}
	perToken := float64(fileBytes) / (eff * bw.BytesPerSec)
	if !bw.Measured {
		perToken += float64(layers) * gpuLayerCost
	}
	p.TokPerSec = 1 / perToken
	switch {
	case p.TokPerSec >= 10:
		p.Word = "usable"
	case p.TokPerSec >= 3:
		p.Word = "slow"
	default:
		p.Word = "too slow to chat"
	}
	return p
}

// Line is the speed in one sentence with its working. Empty when there is no speed to speak of.
func (p Pace) Line() string {
	if p.Word == "" {
		why := p.BW.Why
		if why == "" {
			why = "this machine's memory speed is not known"
		}
		return "speed: cannot say - " + why
	}
	floor := ""
	if p.AtLeast {
		floor = "at least "
	}
	s := fmt.Sprintf("writes %sabout %s tok/s - %s (%s per token at %s", floor, tokWord(p.TokPerSec), p.Word,
		BytesWord(p.PerToken), p.BW.From)
	if p.AtLeast {
		s += "; a mixture of experts reads only part of its weights per token, so likely faster"
	}
	return s + ")"
}

// Short is the speed for a row.
func (p Pace) Short() string {
	if p.Word == "" {
		return ""
	}
	if p.AtLeast {
		return fmt.Sprintf("%s+ tok/s %s", tokWord(p.TokPerSec), p.Word)
	}
	return fmt.Sprintf("~%s tok/s %s", tokWord(p.TokPerSec), p.Word)
}

func tokWord(f float64) string {
	if f < 10 {
		return fmt.Sprintf("%.1f", f)
	}
	return fmt.Sprintf("%.0f", f)
}
