package runtime

// WHICH BUILD SERVES (features/serving/local_models.feature §10, approved 2026-10-02 with CUDA as
// well as Vulkan). Plan turns the owner's choice and the host's facts into the builds to try, in
// order. Whether a GPU build can actually see a device is its own answer (ListDevices), asked
// once it is fetched: a card's name is not proof its driver works.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Choices is what an owner may choose.
var Choices = []string{"auto", "cpu", "gpu", "vulkan", "cuda"}

// Plan is the builds to try for the owner's choice, best first, and the reasons a better one was
// passed over. An empty choice is auto.
func Plan(h Host, choice string) ([]Asset, []string, error) {
	choice = strings.ToLower(strings.TrimSpace(choice))
	if choice == "" {
		choice = "auto"
	}
	known := false
	for _, c := range Choices {
		known = known || c == choice
	}
	if !known {
		return nil, nil, fmt.Errorf("%q is not a runtime choice: auto, cpu, gpu, vulkan or cuda", choice)
	}
	cpu, err := Pick(h)
	if err != nil {
		return nil, nil, err
	}
	var notes []string
	if cpu.Build == BuildMetal {
		if choice != "auto" && choice != "gpu" {
			notes = append(notes, "this platform has one build, Metal on its one pool of memory")
		}
		return []Asset{cpu}, notes, nil
	}
	if choice == "cpu" {
		return []Asset{cpu}, nil, nil
	}
	gpus := gpuPlatforms[h.OS+"/"+h.Arch]
	usable := func(b Build) (Asset, bool) {
		a, ok := assetFor(gpus[b])
		if !ok {
			return Asset{}, false
		}
		if err := a.loadableOn(h); err != nil {
			notes = append(notes, fmt.Sprintf("the %s build cannot load here: %v", b, err))
			return Asset{}, false
		}
		return a, true
	}
	cudaOK := func(a Asset) (bool, string) {
		switch {
		case !h.NVIDIA:
			return false, "the CUDA build runs only on an NVIDIA card, and none was found"
		case h.CUDAMajor == 0:
			return false, "the NVIDIA driver's CUDA version could not be read, so the CUDA build is not guessed at"
		case h.CUDAMajor < a.CUDAMajor || h.CUDAMajor == a.CUDAMajor && h.CUDAMinor < a.CUDAMinor:
			return false, fmt.Sprintf("the NVIDIA driver supports CUDA %d.%d; the CUDA build needs %d.%d",
				h.CUDAMajor, h.CUDAMinor, a.CUDAMajor, a.CUDAMinor)
		}
		return true, ""
	}

	switch choice {
	case "vulkan":
		a, ok := usable(BuildVulkan)
		if !ok {
			return nil, notes, fmt.Errorf("%w: no Vulkan build is pinned for %s/%s", ErrNoRuntime, h.OS, h.Arch)
		}
		return []Asset{a}, notes, nil
	case "cuda":
		a, ok := usable(BuildCUDA)
		if !ok {
			return nil, notes, fmt.Errorf("%w: no CUDA build is pinned for %s/%s", ErrNoRuntime, h.OS, h.Arch)
		}
		if fine, why := cudaOK(a); !fine {
			return nil, notes, fmt.Errorf("%w: %s", ErrNoRuntime, why)
		}
		return []Asset{a}, notes, nil
	}
	// auto and gpu: the best GPU build this machine can take, then (auto only) the CPU build.
	var out []Asset
	if h.GPU {
		if a, ok := usable(BuildCUDA); ok && h.NVIDIA {
			if fine, why := cudaOK(a); fine {
				out = append(out, a)
			} else {
				notes = append(notes, why+" - using Vulkan")
			}
		}
		if a, ok := usable(BuildVulkan); ok {
			out = append(out, a)
		}
	}
	if choice == "gpu" {
		if len(out) == 0 {
			why := "no GPU was found on this machine"
			if h.GPU {
				why = fmt.Sprintf("no GPU build is pinned for %s/%s", h.OS, h.Arch)
			}
			return nil, notes, fmt.Errorf("%w: %s", ErrNoRuntime, why)
		}
		return out, notes, nil
	}
	return append(out, cpu), notes, nil
}

// Device is one device a build says it can use.
type Device struct {
	ID                string // the build's own name for it, "Vulkan0", "CUDA0"
	Name              string
	TotalMiB, FreeMiB int
}

var deviceLine = regexp.MustCompile(`^\s*([A-Za-z]+[0-9]+):\s+(.+?)\s+\((\d+) MiB, (\d+) MiB free\)\s*$`)

// ParseDevices reads `llama-server --list-devices`.
func ParseDevices(out string) []Device {
	var ds []Device
	for _, l := range strings.Split(out, "\n") {
		m := deviceLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		total, _ := strconv.Atoi(m[3])
		free, _ := strconv.Atoi(m[4])
		ds = append(ds, Device{ID: m[1], Name: m[2], TotalMiB: total, FreeMiB: free})
	}
	return ds
}

// ListDevices asks a build which devices it can use. An error means it could not answer at all - a
// driver or loader library missing usually - and says the build's own last words.
func ListDevices(ctx context.Context, exe string, env map[string]string) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--list-devices")
	cmd.Env = append(os.Environ(), envPairs(env)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		last := strings.TrimSpace(out.String())
		if i := strings.LastIndex(last, "\n"); i >= 0 {
			last = last[i+1:]
		}
		return nil, fmt.Errorf("the build could not list its devices (%v): %s", err, last)
	}
	return ParseDevices(out.String()), nil
}

var cudaVersion = regexp.MustCompile(`CUDA Version:\s*(\d+)\.(\d+)`)

// ParseCUDAVersion reads the CUDA version a driver supports out of what nvidia-smi prints.
func ParseCUDAVersion(out string) (major, minor int, ok bool) {
	m := cudaVersion.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}

// ErrNoDevice is a GPU build that runs but lists no device it can use.
var ErrNoDevice = errors.New("lists no GPU it can use")
