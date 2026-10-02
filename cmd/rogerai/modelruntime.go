package main

// WHICH BUILD SERVES (features/serving/local_models.feature §10, approved 2026-10-02 with CUDA as
// well as Vulkan): the owner's choice, resolved on THIS machine by runtime.Plan and then by asking
// each GPU build which devices it can use. Every surface - CLI, TUI, console, an Edge request -
// reaches the runtime through modelRuntimeFor, so they cannot choose differently.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/runtime"
)

// modelRT is the runtime that will serve, and what it is.
type modelRT struct {
	Exe, Tag string
	Build    runtime.Build
	Offloads bool
	Notes    []string         // why a better build was passed over, said to the owner
	Devices  []runtime.Device // the GPUs the build said it can use, with their free memory
}

// Word is the runtime as a status line says it.
func (rt modelRT) Word() string { return fmt.Sprintf("%s, the %s build", rt.Tag, rt.Build) }

// The seams a test replaces: which build serves, and a build's own answer about its devices.
var (
	modelRuntimeFor  = modelResolveRuntime
	modelListDevices = runtime.ListDevices
)

// modelRuntime is the build the owner's choice resolves to on this machine.
func modelRuntime(ctx context.Context) (modelRT, error) {
	return modelRuntimeFor(ctx, modelRuntimeChoice())
}

// modelRuntimeChoice is the owner's choice, auto when unset.
func modelRuntimeChoice() string {
	if s, err := modelHome().Settings(); err == nil && s.Runtime != "" {
		return s.Runtime
	}
	return "auto"
}

// modelThisHost is this machine's runtime facts, GPU ones included: whether there is a GPU, whether
// it is NVIDIA, and the CUDA version its driver supports, read from nvidia-smi and never guessed.
func modelThisHost() runtime.Host {
	h := runtime.ThisHost()
	hw := modelHW()
	h.GPU = len(hw.GPUs) > 0
	for _, g := range hw.GPUs {
		if strings.Contains(strings.ToLower(g.Model), "nvidia") {
			h.NVIDIA = true
		}
	}
	if h.NVIDIA {
		if out, ok := hwRun("nvidia-smi"); ok {
			h.CUDAMajor, h.CUDAMinor, _ = runtime.ParseCUDAVersion(out)
		}
	}
	return h
}

// modelResolveRuntime walks the plan for choice: each build is fetched if it must be (its whole size
// said first), and a GPU build is asked which devices it can use. The first that can serve, serves.
func modelResolveRuntime(ctx context.Context, choice string) (modelRT, error) {
	plan, notes, err := runtime.Plan(modelHost(), choice)
	if err != nil {
		return modelRT{Notes: notes}, err
	}
	dir := modelHome().Runtime()
	var last error
	for _, a := range plan {
		exe, ok := runtime.InstalledBuild(a, dir)
		if !ok {
			fmt.Fprintf(modelOut, "fetching llama.cpp %s, the %s build (%s), verified before it ever runs.\n",
				a.Tag, a.Build, provision.BytesWord(a.TotalBytes()))
			exe, err = runtime.InstallBuild(ctx, a, dir, modelFetcher())
			modelProgressDone()
			if err != nil {
				last = fmt.Errorf("the %s build could not be fetched: %w", a.Build, err)
				notes = append(notes, last.Error())
				continue
			}
		}
		var ds []runtime.Device
		if a.Offloads && a.Build != runtime.BuildMetal {
			ds, err = modelListDevices(ctx, exe, modelServeEnv)
			if err != nil || len(ds) == 0 {
				why := "the " + string(a.Build) + " build lists no GPU it can use"
				if err != nil {
					why += " (" + err.Error() + ")"
				}
				last = errors.New(why + " - is the GPU's " + modelDriverWord(a.Build) + " installed?")
				notes = append(notes, last.Error())
				continue
			}
		}
		modelRecordSettled(modelSettled{Choice: choice, Platform: a.Platform, Tag: a.Tag, Notes: notes})
		return modelRT{Exe: exe, Tag: a.Tag, Build: a.Build, Offloads: a.Offloads, Notes: notes, Devices: ds}, nil
	}
	if last == nil {
		last = runtime.ErrNoRuntime
	}
	return modelRT{Notes: notes}, last
}

func modelDriverWord(b runtime.Build) string {
	if b == runtime.BuildCUDA {
		return "NVIDIA driver"
	}
	return "Vulkan driver"
}

// modelPredict is the build the owner's choice means here: the one the last serve settled on for the
// same choice and tag (a GPU build may have been passed over, and the screens say so), else the best
// the plan names. It fetches and asks nothing.
func modelPredict() (runtime.Asset, []string, error) {
	choice := modelRuntimeChoice()
	plan, notes, err := runtime.Plan(modelHost(), choice)
	if err != nil {
		return runtime.Asset{}, notes, err
	}
	if st, ok := modelReadSettled(); ok && st.Choice == choice {
		for _, a := range plan {
			if a.Platform == st.Platform && a.Tag == st.Tag {
				return a, st.Notes, nil
			}
		}
	}
	return plan[0], notes, nil
}

// modelSettled is what the last resolution chose, kept so the screens say the build that serves.
type modelSettled struct {
	Choice   string   `json:"choice"`
	Platform string   `json:"platform"`
	Tag      string   `json:"tag"`
	Notes    []string `json:"notes,omitempty"`
}

func modelSettledPath() string { return filepath.Join(modelHome().Runtime(), "settled.json") }

func modelRecordSettled(st modelSettled) {
	if b, err := json.Marshal(st); err == nil && os.MkdirAll(modelHome().Runtime(), 0o755) == nil {
		_ = os.WriteFile(modelSettledPath(), b, 0o644)
	}
}

func modelReadSettled() (modelSettled, bool) {
	var st modelSettled
	b, err := os.ReadFile(modelSettledPath())
	return st, err == nil && json.Unmarshal(b, &st) == nil && st.Platform != ""
}

// modelRuntimeCmd shows or sets the owner's choice (local_models.feature §10).
func modelRuntimeCmd(pos []string) error {
	h := modelHome()
	s, err := h.Settings()
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New("usage: roger model runtime [auto | cpu | gpu | vulkan | cuda]")
	}
	if len(pos) == 1 {
		choice := strings.ToLower(pos[0])
		if _, _, err := runtime.Plan(runtime.Host{OS: "linux", Arch: "amd64"}, choice); err != nil && !errors.Is(err, runtime.ErrNoRuntime) {
			return err // not a choice at all
		}
		s.Runtime = choice
		if err := h.SetSettings(s); err != nil {
			return err
		}
	}
	choice := s.Runtime
	if choice == "" {
		choice = "auto"
	}
	fmt.Fprintf(modelOut, "runtime: %s\n", choice)
	a, notes, err := modelPredict()
	if err != nil {
		fmt.Fprintf(modelOut, "  on this machine it cannot serve here: %s\n", strings.TrimPrefix(err.Error(), runtime.ErrNoRuntime.Error()+": "))
	} else {
		state := "not fetched yet"
		if _, ok := runtime.InstalledBuild(a, h.Runtime()); ok {
			state = "installed"
		}
		fmt.Fprintf(modelOut, "  on this machine: the %s build of llama.cpp %s (%s, %s)\n", a.Build, a.Tag,
			provision.BytesWord(a.TotalBytes()), state)
	}
	for _, n := range notes {
		fmt.Fprintf(modelOut, "  %s\n", n)
	}
	fmt.Fprintln(modelOut, "  choices: auto (the best this machine can use), cpu, gpu, vulkan, cuda - it applies from the next serve")
	return nil
}

// modelBudgetFor is the pool a build serves from.
func modelBudgetFor(offloads bool) localmodel.Budget {
	return localmodel.BudgetFor(modelHW(), offloads)
}

// modelCardMargin is what a pinned card keeps free beyond the model's need, as the runtime's own fit
// keeps by default (--fit-target).
const modelCardMargin = int64(1024) << 20

// modelPickDevice is the card a model that fits on one should be pinned to: the one with the most free
// memory, when need and a margin fit there. "" leaves placement to the runtime's own split - for a
// model too big for any one card, or when the need or the devices are not known.
func modelPickDevice(ds []runtime.Device, need int64) string {
	if need <= 0 {
		return ""
	}
	best := -1
	for i, d := range ds {
		if best < 0 || d.FreeMiB > ds[best].FreeMiB {
			best = i
		}
	}
	if best < 0 || need+modelCardMargin > int64(ds[best].FreeMiB)<<20 {
		return ""
	}
	return ds[best].ID
}

// modelNeed is roger's estimate of what a model needs at ctxLen, or 0 when it cannot say.
func modelNeed(m localmodel.Model, b localmodel.Budget, ctxLen int) int64 {
	sh, err := detect.ReadShape(m.Path)
	if err != nil {
		return 0
	}
	return localmodel.Judge(sh, b, ctxLen).Need.Total
}
