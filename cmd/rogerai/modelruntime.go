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
	"slices"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/runtime"
	"rogerai.fm/roger/v6/internal/webui"
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

// modelThisHost is this machine's runtime facts, GPU ones included. A GPU is found by its driver
// (nvidia-smi answering with a CUDA version) and its PCI vendor, never by what its name says:
// nvidia-smi calls cards "Tesla T4" and "Quadro RTX 6000", and an AMD card on Mesa has no rocm-smi.
// An Intel GPU alone is not taken for one auto should serve on; `roger model runtime vulkan` tries it.
func modelThisHost() runtime.Host {
	h := runtime.ThisHost()
	hw := modelHW()
	if out, ok := hwRun("nvidia-smi"); ok {
		if maj, minor, ok := runtime.ParseCUDAVersion(out); ok {
			h.NVIDIA, h.CUDAMajor, h.CUDAMinor = true, maj, minor
		}
	}
	amd := false
	for _, v := range modelDRMVendors() {
		switch v {
		case "0x10de":
			h.NVIDIA = true // the card is there even when its driver does not answer
		case "0x1002":
			amd = true
		}
	}
	h.GPU = h.NVIDIA || amd || len(hw.GPUs) > 0
	return h
}

// modelDRMVendors is the PCI vendor of each display device this machine has, where the OS says
// (Linux: /sys/class/drm). Elsewhere it is empty, and the hardware report stands alone.
var modelDRMVendors = func() []string {
	paths, _ := filepath.Glob("/sys/class/drm/card*/device/vendor")
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		card := filepath.Base(filepath.Dir(filepath.Dir(p)))
		if strings.Contains(card, "-") {
			continue // a connector of a card, not a card
		}
		if b, err := os.ReadFile(p); err == nil {
			v := strings.ToLower(strings.TrimSpace(string(b)))
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
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
			exe, err = modelInstallLocked(ctx, a, dir)
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

// modelInstallLocked installs one build with the runtime directory locked, so two serves started at
// once (the console and an Edge request) cannot unpack the same build over each other.
func modelInstallLocked(ctx context.Context, a runtime.Asset, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	unlock, err := edge.FileLock(filepath.Join(dir, ".install.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	if exe, ok := runtime.InstalledBuild(a, dir); ok {
		return exe, nil // the other one finished it while this one waited
	}
	return runtime.InstallBuild(ctx, a, dir, modelFetcher())
}

// modelFetchFirst says, for a serve that runs in the background, the build it will fetch first and
// its size, or "" when the build is already here.
func modelFetchFirst() string {
	a, _, err := modelPredict()
	if err != nil {
		return ""
	}
	if _, ok := runtime.InstalledBuild(a, modelHome().Runtime()); ok {
		return ""
	}
	return fmt.Sprintf(" - it fetches the %s build first (%s), verified before it runs", a.Build, provision.BytesWord(a.TotalBytes()))
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
func modelRuntimeCmd(pos []string) error { return modelPrint(modelRuntimeSays(pos)) }

// modelRuntimeSays shows or sets the owner's choice and says what it means on THIS machine.
func modelRuntimeSays(pos []string) (string, error) {
	h := modelHome()
	s, err := h.Settings()
	if err != nil {
		return "", err
	}
	if len(pos) > 1 {
		return "", errors.New("usage: roger model runtime [auto | cpu | gpu | vulkan | cuda]")
	}
	if len(pos) == 1 {
		choice := strings.ToLower(pos[0])
		if !slices.Contains(runtime.Choices, choice) {
			return "", fmt.Errorf("%q is not a runtime choice: auto, cpu, gpu, vulkan or cuda", pos[0])
		}
		s.Runtime = choice
		if err := h.SetSettings(s); err != nil {
			return "", err
		}
	}
	choice := s.Runtime
	if choice == "" {
		choice = "auto"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "runtime: %s\n", choice)
	fmt.Fprintf(&b, "  %s\n", modelRuntimeHere())
	_, notes, _ := modelPredict()
	for _, n := range notes {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	b.WriteString("  choices: auto (the best this machine can use), cpu, gpu, vulkan, cuda - it applies from the next serve")
	return b.String(), nil
}

// modelRuntimeHere is what the owner's choice means on this machine, in one line.
func modelRuntimeHere() string {
	a, _, err := modelPredict()
	if err != nil {
		return "on this machine it cannot serve here: " + strings.TrimPrefix(err.Error(), runtime.ErrNoRuntime.Error()+": ")
	}
	state := "not fetched yet"
	if _, ok := runtime.InstalledBuild(a, modelHome().Runtime()); ok {
		state = "installed"
	}
	return fmt.Sprintf("on this machine: the %s build of llama.cpp %s (%s, %s)", a.Build, a.Tag, provision.BytesWord(a.TotalBytes()), state)
}

// modelSetSays changes one serving setting from any surface, with the CLI's own sentence.
func modelSetSays(kind, value string) (string, error) {
	switch kind {
	case "runtime":
		return modelRuntimeSays([]string{value})
	case "idle":
		return modelIdleSays([]string{value})
	case "budget":
		return modelBudgetSays([]string{value})
	}
	return "", fmt.Errorf("%q is not a serving setting: runtime, idle or budget", kind)
}

// modelSettingsView is the settings line SHARE shows (local_models.feature §11).
func modelSettingsView() webui.ServingSettings {
	s, _ := modelHome().Settings()
	v := webui.ServingSettings{Runtime: s.Runtime, Here: modelRuntimeHere(), Idle: "off", DiskBudget: s.DiskBudget,
		Choices: append([]string(nil), runtime.Choices...)}
	if v.Runtime == "" {
		v.Runtime = "auto"
	}
	if s.IdleAfter > 0 {
		v.Idle = s.IdleAfter.String()
	}
	return v
}

// modelMeasured is the speed a live serve's runtime measured, or 0 before it has answered.
func modelMeasured(s localmodel.Serving) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := localmodel.Counters(ctx, s)
	if err != nil {
		return 0
	}
	return c.TokPerSec()
}

// modelEstimate is the speed a live serve is estimated at, for before it has answered.
func modelEstimate(s localmodel.Serving) string {
	m, err := modelHome().Find(s.ID)
	if err != nil {
		return ""
	}
	sh, err := detect.ReadShape(m.Path)
	if err != nil {
		return ""
	}
	p := localmodel.Judge(sh, modelBudgetFor(s.Build != "" && s.Build != string(runtime.BuildCPU)), s.Ctx).Pace
	if p.Short() == "" {
		return ""
	}
	return p.Short() + " (estimate)"
}

// modelBudgetFor is the pool a build serves from.
func modelBudgetFor(offloads bool) localmodel.Budget {
	return localmodel.BudgetFor(modelHW(), offloads)
}

// modelPickDevice is the card a model that fits on one should be pinned to: the one with the most free
// memory, when need fits there (need already carries roger's reserve). "" leaves placement to the runtime's own split - for a
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
	if best < 0 || need > int64(ds[best].FreeMiB)<<20 {
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
