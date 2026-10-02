package localmodel

// WILL IT RUN HERE: the pre-download answer, always with the context it was computed at.

import (
	"fmt"
	"path"
	"strings"

	"rogerai.fm/roger/v6/internal/catalog"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/hfhub"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/runtime"
)

// DefaultCtx is the context proposed when the owner names none: long enough for a real
// conversation, short enough that the cache does not dwarf the weights on a small machine.
const DefaultCtx = 8192

// Budget is the memory a model is judged against, and which pool it is.
type Budget struct {
	Bytes int64
	Pool  string    // "video memory", "unified memory", "system memory"
	Why   string    // when Bytes is zero: the reading that failed
	BW    Bandwidth // how fast this pool reads, for how fast a model answers
	Note  string    // why this pool and not another the machine has, when that is not obvious
}

// Known reports whether there is a budget at all.
func (b Budget) Known() bool { return b.Bytes > 0 }

// BudgetFor is the pool the runtime that would serve uses: a CPU-only runtime serves from system
// memory however many cards the machine has, and saying otherwise promises a fit it will not keep.
// One shared pool is measured as one pool, and with a runtime that offloads, a GPU's own memory is
// preferred where it was read, because that is where the layers go; spilling to system memory
// runs, slowly, and is not what "fits" means.
func BudgetFor(hw detect.LocalHW, offloads bool) Budget { return budgetFor(hw, offloads, MeasureRAM) }

func budgetFor(hw detect.LocalHW, offloads bool, measure func(ramMiB int) Bandwidth) Budget {
	const mib = int64(1) << 20
	switch {
	case hw.UnifiedMemory && hw.RAMKnown:
		return Budget{Bytes: int64(hw.RAMTotalMiB) * mib, Pool: "unified memory", BW: bandwidthOf(hw, measure)}
	case offloads && hw.VRAMKnown && hw.VRAMTotalMiB > 0:
		pool := "video memory"
		if len(hw.GPUs) > 1 {
			// The runtime splits layers across cards by default, so the sum is the honest budget -
			// but it is a sum, and the owner should know that is what they are reading.
			pool = fmt.Sprintf("video memory across %d GPUs", len(hw.GPUs))
		}
		return Budget{Bytes: int64(hw.VRAMTotalMiB) * mib, Pool: pool, BW: bandwidthOf(hw, measure)}
	case hw.RAMKnown && hw.RAMTotalMiB > 0:
		b := Budget{Bytes: int64(hw.RAMTotalMiB) * mib, Pool: "system memory", BW: measure(hw.RAMTotalMiB)}
		if !offloads && len(hw.GPUs) > 0 {
			b.Note = "the runtime roger starts here runs on the CPU, so a model lives in system memory, not on the GPU"
		}
		return b
	}
	why := "this machine's memory could not be read"
	if len(hw.Undetermined) > 0 {
		why += ": " + hw.Undetermined[0]
	}
	return Budget{Why: why}
}

// Verdict is the answer and the working behind it.
type Verdict struct {
	Fit    catalog.Fit
	Need   catalog.Need // every term, so the owner can check the sum
	Budget Budget
	Ctx    int // the context the verdict is AT. A verdict without one is not a verdict.
	MaxCtx int // the longest context that does fit, 0 when none does
	Why    string
	Pace   Pace // how fast it would answer: a separate claim, never folded into Fit

	ByRuntime bool   // the runtime's own check answered, on the file on disk
	Unasked   string // why the runtime could not be asked, when this is roger's estimate of a file on disk
}

// runtimeMargin is what llama.cpp's fit leaves free on each device by default (--fit-target).
const runtimeMargin = int64(1024) << 20

// Settled takes the runtime's answer over this estimate once the file is on disk, with its own
// breakdown. When the runtime could not answer, the estimate stands and says it is one.
func (v Verdict) Settled(f runtime.Fitted, err error) Verdict {
	if err != nil {
		v.Unasked = err.Error()
		return v
	}
	out := Verdict{Budget: v.Budget, Ctx: f.Ctx, Pace: v.Pace, ByRuntime: true, Fit: catalog.FitFits}
	for _, d := range f.Devices {
		out.Need.Weights += d.Model
		out.Need.KV += d.Context
		out.Need.Compute += d.Compute
		out.Need.Headroom += runtimeMargin
	}
	out.Need.Total = out.Need.Weights + out.Need.KV + out.Need.Compute + out.Need.Headroom
	if !f.Fits {
		out.Fit, out.MaxCtx, out.Why = catalog.FitWontFit, f.MaxCtx, f.Said
	}
	return out
}

// Judge computes the verdict at ctx, or at a proposed context when ctx is zero.
func Judge(s catalog.Shape, b Budget, ctx int) Verdict {
	if ctx <= 0 {
		ctx = DefaultCtx
		if s.TrainCtx > 0 && s.TrainCtx < ctx {
			ctx = s.TrainCtx
		}
	}
	v := Verdict{Budget: b, Ctx: ctx}
	need, ok := catalog.NeedAt(s, ctx, 1, catalog.CacheF16, catalog.CacheF16)
	if !ok {
		v.Why = "the model's header does not carry what the sum needs"
		return v
	}
	v.Need = need
	if !b.Known() {
		v.Why = b.Why
		return v
	}
	v.Fit = catalog.AssessFit(b.Bytes, need.Total)
	v.Pace = paceOf(s.FileBytes, s.Experts, s.ExpertsUsed, b.BW)
	v.MaxCtx, _ = catalog.MaxCtxFor(s, b.Bytes, 1, catalog.CacheF16, catalog.CacheF16)
	if s.TrainCtx > 0 && ctx > s.TrainCtx {
		v.Why = fmt.Sprintf("it was trained for %d tokens of context, not %d", s.TrainCtx, ctx)
	}
	return v
}

// Line is the verdict in one sentence, with the context in it.
func (v Verdict) Line() string {
	if v.ByRuntime {
		if v.Fit == catalog.FitWontFit {
			where := "it fits at no context the runtime would choose"
			if v.MaxCtx > 0 {
				where = "fits at " + ctxWord(v.MaxCtx)
			}
			return fmt.Sprintf("will not fit at %s context by llama.cpp's own check: %s; %s", ctxWord(v.Ctx), v.Why, where)
		}
		return fmt.Sprintf("fits at %s context by llama.cpp's own check: needs %s, leaving its margin free",
			ctxWord(v.Ctx), BytesWord(v.Need.Total-v.Need.Headroom))
	}
	if v.Unasked != "" {
		return v.estimateLine() + " (roger's estimate - the runtime's own check could not run: " + v.Unasked + ")"
	}
	return v.estimateLine()
}

func (v Verdict) estimateLine() string {
	switch v.Fit {
	case catalog.FitUnknown:
		return "cannot say - " + v.Why
	case catalog.FitWontFit:
		if v.MaxCtx > 0 {
			return fmt.Sprintf("will not fit at %s context in %s of %s; fits at %s", ctxWord(v.Ctx),
				BytesWord(v.Budget.Bytes), v.Budget.Pool, ctxWord(v.MaxCtx))
		}
		return fmt.Sprintf("will not fit: needs %s, %s has %s", BytesWord(v.Need.Total), v.Budget.Pool, BytesWord(v.Budget.Bytes))
	}
	return fmt.Sprintf("%s at %s context: needs %s of %s %s", v.Fit, ctxWord(v.Ctx), BytesWord(v.Need.Total),
		BytesWord(v.Budget.Bytes), v.Budget.Pool)
}

// Short is the verdict for a row under a heading that already names the machine's pool.
func (v Verdict) Short() string {
	switch v.Fit {
	case catalog.FitUnknown:
		return "cannot say"
	case catalog.FitWontFit:
		if v.MaxCtx > 0 {
			return fmt.Sprintf("will not fit at %s - fits at %s", ctxWord(v.Ctx), ctxWord(v.MaxCtx))
		}
		return fmt.Sprintf("will not fit - needs %s", BytesWord(v.Need.Total))
	}
	return fmt.Sprintf("%-5s at %s - needs %s", v.Fit, ctxWord(v.Ctx), BytesWord(v.Need.Total))
}

// PaceLine is the speed claim, beside the fit and never inside it. Empty when the model does not
// fit, because a speed in a pool it cannot live in is not a speed it would have.
func (v Verdict) PaceLine() string {
	if v.Fit != catalog.FitFits && v.Fit != catalog.FitTight {
		return ""
	}
	return v.Pace.Line()
}

// Sum is the working, term by term - the runtime's own terms when it answered.
func (v Verdict) Sum() string {
	n := v.Need
	if n.Total == 0 {
		return ""
	}
	if v.ByRuntime {
		return fmt.Sprintf("model %s + context %s + compute %s + margin %s = %s",
			BytesWord(n.Weights), BytesWord(n.KV), BytesWord(n.Compute), BytesWord(n.Headroom), BytesWord(n.Total))
	}
	return fmt.Sprintf("weights %s + cache %s + output %s + working %s + reserve %s = %s",
		BytesWord(n.Weights), BytesWord(n.KV), BytesWord(n.Output), BytesWord(n.Compute),
		BytesWord(n.Headroom), BytesWord(n.Total))
}

func ctxWord(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return fmt.Sprintf("%dK", n/1024)
	}
	return fmt.Sprint(n)
}

// Choice is one file of a repository with its own verdict, or the reason it cannot be served.
type Choice struct {
	File       hfhub.File
	Verdict    Verdict
	Unservable string
}

// Rank judges every file of one repository. The architecture is the same in every quantisation,
// so ONE header read answers for all of them and only the file size changes.
func Rank(files []hfhub.File, shape catalog.Shape, b Budget, ctx int) []Choice {
	out := make([]Choice, 0, len(files))
	for _, f := range files {
		c := Choice{File: f, Unservable: Unservable(f.Path)}
		if c.Unservable == "" {
			s := shape
			s.FileBytes = f.Bytes
			c.Verdict = Judge(s, b, ctx)
		}
		out = append(out, c)
	}
	return out
}

// Suggest is the index of the file roger would pick, or -1. The rule is short so it can be said:
// Q4_K_M when it fits comfortably, because it is where quality per byte levels off; otherwise the
// largest that fits comfortably; otherwise the largest that fits at all.
func Suggest(cs []Choice) int {
	best, bestTight := -1, -1
	for i, c := range cs {
		if c.Unservable != "" {
			continue
		}
		switch c.Verdict.Fit {
		case catalog.FitFits:
			if strings.EqualFold(c.File.Quant, "Q4_K_M") {
				return i
			}
			if best < 0 || c.File.Bytes > cs[best].File.Bytes {
				best = i
			}
		case catalog.FitTight:
			if bestTight < 0 || c.File.Bytes > cs[bestTight].File.Bytes {
				bestTight = i
			}
		}
	}
	if best >= 0 {
		return best
	}
	return bestTight
}

// BytesWord is a size the way every roger screen says sizes.
func BytesWord(n int64) string { return provision.BytesWord(n) }

// Labels names each choice for a person: its quantisation where that is unique in the repo, and
// its file name where it is not, because Q4_0_4_4 and Q4_0_8_8 are different files that read alike.
func Labels(cs []Choice) []string {
	seen := map[string]int{}
	for _, c := range cs {
		seen[strings.ToUpper(c.File.Quant)]++
	}
	out := make([]string, len(cs))
	for i, c := range cs {
		if c.File.Quant != "" && seen[strings.ToUpper(c.File.Quant)] == 1 {
			out[i] = c.File.Quant
		} else {
			out[i] = strings.TrimSuffix(path.Base(c.File.Path), ".gguf")
		}
	}
	return out
}
