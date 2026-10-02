package runtime

// WHERE THE RUNTIME COMES FROM (features/serving/local_models.feature §3).
//
// A model file is DATA that a runtime interprets. A runtime is a PROGRAM this machine will
// execute, so it gets different treatment and this file is that difference: one pinned list,
// resolved in advance, carrying the exact asset name, URL, size and digest, verified before it is
// ever run, and never taken from an address that arrived from outside.
//
// THE NAMES ARE NOT COMPUTED AT RUN TIME, and that is deliberate rather than cautious. Upstream's
// naming has already rotted: an older release's macOS asset is a .zip where today's is a .tar.gz,
// and some assets in old releases carry the wrong version in their own name. Any template built
// from the platform at run time breaks the first time that happens again, on a machine we cannot
// see, in a way the owner reads as "roger is broken".
//
// THE STABLE CHANNEL, NOT THE FIREHOSE. Upstream publishes roughly twenty prerelease builds a
// day, and separately marks a stable release whose only asset is a text file naming the build it
// blesses. This manifest is pinned to such a blessed build. Refreshing it is a deliberate act with
// a diff, the same way roger's own release assets are handled.
//
// THE SYSTEM-LIBRARY FLOOR IS MEASURED, NOT ASSUMED. Each entry carries the oldest glibc that can
// load it, read out of the actual downloaded objects with readelf rather than taken from a
// changelog. Upstream raised the arm64 floor to 2.38 by moving a build runner and nobody
// announced it; that one change locks out Raspberry Pi OS Bookworm, which is the most common Pi
// install there is. Measuring is the only way we find out before an owner does.

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// ErrNoRuntime means this machine cannot serve locally, and the message says why in one sentence.
// It is a first-class answer rather than a failure: a board that can relay and control but not
// serve is a useful board, and telling its owner that plainly beats a broken download.
var ErrNoRuntime = errors.New("no inference runtime is published for this machine")

// Asset is one runtime build, resolved in advance.
type Asset struct {
	Platform string // upstream's own name for the build, kept verbatim for traceability
	Tag      string // the build it came from
	Name     string // the exact asset file name
	URL      string
	Bytes    int64
	SHA256   string
	// Exe is where llama-server sits INSIDE the archive. The artifact is a directory of several
	// dozen files whose backends are loaded from alongside the binary, so the tree is kept
	// together and this is the path within it.
	Exe string
	// MinGlibc is the oldest glibc that can load this build, measured from the objects
	// themselves. Zero means the question does not apply to that platform.
	MinGlibcMajor, MinGlibcMinor int
	// Offloads is whether this build puts layers on a GPU. The pinned Linux and Windows builds are
	// upstream's CPU builds, so a model served by them lives in system memory whatever cards the
	// machine has - which is the pool its fit and its speed have to be judged in.
	Offloads bool
}

// runtimeTag is the blessed upstream build this manifest describes. One place, so a refresh is
// one diff.
const runtimeTag = "b11146"

const assetBase = "https://github.com/ggml-org/llama.cpp/releases/download/" + runtimeTag + "/"

// manifest is the resolved list. Digests and sizes came from the release API; the glibc floors
// were read with readelf out of the downloaded archives on 2026-10-01.
//
// CUDA is deliberately absent for now. The published arm64 CUDA build does not target the compute
// capability a Jetson Orin actually has, so it would fall back to compiling from intermediate
// code at every start, and the x64 CUDA build is a 149 MB download plus a separate runtime
// library. Both are real work with real test cases, and a CPU build that definitely works is a
// better first answer than a GPU build that might.
var manifest = []Asset{
	{
		Platform: "ubuntu-x64", Tag: runtimeTag,
		Name:          "llama-" + runtimeTag + "-bin-ubuntu-x64.tar.gz",
		URL:           assetBase + "llama-" + runtimeTag + "-bin-ubuntu-x64.tar.gz",
		Bytes:         16998357,
		SHA256:        "c150306eb16b5ab696f76a8bdf810c35fd98a24e82158742e6fa28f420ff8410",
		Exe:           "llama-" + runtimeTag + "/llama-server",
		MinGlibcMajor: 2, MinGlibcMinor: 34,
	},
	{
		Platform: "ubuntu-arm64", Tag: runtimeTag,
		Name:   "llama-" + runtimeTag + "-bin-ubuntu-arm64.tar.gz",
		URL:    assetBase + "llama-" + runtimeTag + "-bin-ubuntu-arm64.tar.gz",
		Bytes:  13598346,
		SHA256: "4aeda6fe68831547e49b7fa87607383ca5352b3d72ca5f70d52ed265f58c131f",
		Exe:    "llama-" + runtimeTag + "/llama-server",
		// MEASURED 2.38, which is ABOVE Raspberry Pi OS Bookworm's 2.36. A Pi on Bookworm is
		// refused with that sentence rather than handed a binary that cannot load.
		MinGlibcMajor: 2, MinGlibcMinor: 38,
	},
	{
		Platform: "macos-arm64", Tag: runtimeTag, Offloads: true,
		Name:   "llama-" + runtimeTag + "-bin-macos-arm64.tar.gz",
		URL:    assetBase + "llama-" + runtimeTag + "-bin-macos-arm64.tar.gz",
		Bytes:  11189714,
		SHA256: "1ad3f9eff80edb9dbef4259ad564d1720612ef7eea48fa4afed0e54f5f3d5711",
		Exe:    "llama-" + runtimeTag + "/llama-server",
	},
	{
		Platform: "macos-x64", Tag: runtimeTag,
		Name:   "llama-" + runtimeTag + "-bin-macos-x64.tar.gz",
		URL:    assetBase + "llama-" + runtimeTag + "-bin-macos-x64.tar.gz",
		Bytes:  11237237,
		SHA256: "305f0e3a17d2c01eb205cd0a62128357f1ec3b55329cb084d94e5ec0115d7a3b",
		Exe:    "llama-" + runtimeTag + "/llama-server",
	},
	{
		Platform: "win-cpu-x64", Tag: runtimeTag,
		Name:   "llama-" + runtimeTag + "-bin-win-cpu-x64.zip",
		URL:    assetBase + "llama-" + runtimeTag + "-bin-win-cpu-x64.zip",
		Bytes:  18560055,
		SHA256: "14cf1303ca9ac3abd94816850532f9f9a69ac66fbaca3776fc6f9061c2fac1d1",
		Exe:    "llama-server.exe",
	},
	{
		Platform: "win-cpu-arm64", Tag: runtimeTag,
		Name:   "llama-" + runtimeTag + "-bin-win-cpu-arm64.zip",
		URL:    assetBase + "llama-" + runtimeTag + "-bin-win-cpu-arm64.zip",
		Bytes:  12034624,
		SHA256: "1727d241f3bf6d27360e984e851cf013928fd655bf89f8628e70da027f377b7d",
		Exe:    "llama-server.exe",
	},
}

// Host is what the resolver needs to know about this machine. It is a struct rather than read
// from the environment so the choice is testable for every platform from any platform.
type Host struct {
	OS, Arch string
	// Glibc as reported by the system, and whether it could be read. An unknown glibc is NOT
	// treated as too old: refusing a machine because a version string could not be parsed would
	// be guessing in the direction that helps nobody.
	GlibcMajor, GlibcMinor int
	GlibcKnown             bool
}

// ThisHost describes the machine roger is running on.
func ThisHost() Host {
	h := Host{OS: runtime.GOOS, Arch: runtime.GOARCH}
	h.GlibcMajor, h.GlibcMinor, h.GlibcKnown = hostGlibc()
	return h
}

// Pick is the runtime for this machine, or says why there is none. The refusal is the product
// here as much as the choice is.
func Pick(h Host) (Asset, error) {
	platform, ok := platformFor(h.OS, h.Arch)
	if !ok {
		// linux/arm is the live case: roger ships a 32-bit arm binary and no inference runtime
		// exists for it from anybody. A Pi Zero 2 can relay and control; it cannot serve.
		return Asset{}, fmt.Errorf("%w (%s/%s). It can still join an Edge, relay and be controlled - it cannot serve a model locally",
			ErrNoRuntime, h.OS, h.Arch)
	}
	for _, a := range manifest {
		if a.Platform != platform {
			continue
		}
		if err := a.loadableOn(h); err != nil {
			return Asset{}, err
		}
		return a, nil
	}
	return Asset{}, fmt.Errorf("%w (%s/%s)", ErrNoRuntime, h.OS, h.Arch)
}

// loadableOn checks the one host fact that has already silently broken a platform.
func (a Asset) loadableOn(h Host) error {
	if a.MinGlibcMajor == 0 || !h.GlibcKnown {
		return nil
	}
	if h.GlibcMajor > a.MinGlibcMajor ||
		(h.GlibcMajor == a.MinGlibcMajor && h.GlibcMinor >= a.MinGlibcMinor) {
		return nil
	}
	return fmt.Errorf("%w: the published build needs glibc %d.%d and this machine has %d.%d. "+
		"A newer OS release would run it; this one cannot load it at all",
		ErrNoRuntime, a.MinGlibcMajor, a.MinGlibcMinor, h.GlibcMajor, h.GlibcMinor)
}

// platformFor maps a Go platform onto upstream's own build name.
func platformFor(goos, goarch string) (string, bool) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "ubuntu-x64", true
	case "linux/arm64":
		return "ubuntu-arm64", true
	case "darwin/arm64":
		return "macos-arm64", true
	case "darwin/amd64":
		return "macos-x64", true
	case "windows/amd64":
		return "win-cpu-x64", true
	case "windows/arm64":
		return "win-cpu-arm64", true
	}
	return "", false
}

// Assets is the whole manifest, for a command that lists what roger knows how to fetch and for
// the build-time check that every entry is still loadable on the floor we claim.
func Assets() []Asset { return append([]Asset(nil), manifest...) }

// Tag is the upstream build this roger is pinned to.
func Tag() string { return runtimeTag }

// ParseGlibcVersion is exported for the package's own tests, which pin it against the strings real
// tools actually print - they differ by tool and by distribution, and a parser that handles only
// the one on the developer's machine is a parser that refuses somebody else's.
func ParseGlibcVersion(s string) (int, int, bool) { return parseGlibc(s) }

// parseGlibc reads a version out of the strings the system reports, which differ by tool:
// "glibc 2.42" from getconf, "ldd (GNU libc) 2.36" from ldd.
func parseGlibc(s string) (major, minor int, ok bool) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= '0' && r <= '9') && r != '.'
	})
	for _, f := range fields {
		maj, min, found := strings.Cut(f, ".")
		if !found {
			continue
		}
		// Trim anything after a second dot: some systems report 2.36.1.
		if i := strings.Index(min, "."); i >= 0 {
			min = min[:i]
		}
		a, err1 := strconv.Atoi(maj)
		b, err2 := strconv.Atoi(min)
		if err1 == nil && err2 == nil && a > 0 {
			return a, b, true
		}
	}
	return 0, 0, false
}
