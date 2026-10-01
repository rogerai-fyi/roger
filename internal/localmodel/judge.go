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
)

// DefaultCtx is the context proposed when the owner names none: long enough for a real
// conversation, short enough that the cache does not dwarf the weights on a small machine.
const DefaultCtx = 8192

// Budget is the memory a model is judged against, and which pool it is.
type Budget struct {
	Bytes int64
	Pool  string // "video memory", "unified memory", "system memory"
	Why   string // when Bytes is zero: the reading that failed
}

// Known reports whether there is a budget at all.
func (b Budget) Known() bool { return b.Bytes > 0 }

// BudgetOf picks the pool a model has to live in. A machine with one shared pool is measured as
// one pool, and a GPU's own memory is preferred where it was read, because that is where the
// runtime puts the layers; spilling to system memory runs, slowly, and is not what "fits" means.
func BudgetOf(hw detect.LocalHW) Budget {
	const mib = int64(1) << 20
	switch {
	case hw.UnifiedMemory && hw.RAMKnown:
		return Budget{Bytes: int64(hw.RAMTotalMiB) * mib, Pool: "unified memory"}
	case hw.VRAMKnown && hw.VRAMTotalMiB > 0:
		pool := "video memory"
		if len(hw.GPUs) > 1 {
			// The runtime splits layers across cards by default, so the sum is the honest budget -
			// but it is a sum, and the owner should know that is what they are reading.
			pool = fmt.Sprintf("video memory across %d GPUs", len(hw.GPUs))
		}
		return Budget{Bytes: int64(hw.VRAMTotalMiB) * mib, Pool: pool}
	case hw.RAMKnown && hw.RAMTotalMiB > 0:
		return Budget{Bytes: int64(hw.RAMTotalMiB) * mib, Pool: "system memory"}
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
	v.MaxCtx, _ = catalog.MaxCtxFor(s, b.Bytes, 1, catalog.CacheF16, catalog.CacheF16)
	if s.TrainCtx > 0 && ctx > s.TrainCtx {
		v.Why = fmt.Sprintf("it was trained for %d tokens of context, not %d", s.TrainCtx, ctx)
	}
	return v
}

// Line is the verdict in one sentence, with the context in it.
func (v Verdict) Line() string {
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

// Sum is the working, term by term.
func (v Verdict) Sum() string {
	n := v.Need
	if n.Total == 0 {
		return ""
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
