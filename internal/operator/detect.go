package operator

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProbeTimeout bounds one `<bin> --version` probe (the audio.PlayTimeout discipline): a
// wedged binary returns an error at the deadline and the guest degrades to UNVERIFIED -
// it can never hang the desk scan. A package var (the proxyDialTimeout precedent) so a
// test can prove the kill with a genuinely hung binary and a small deadline.
var ProbeTimeout = 3 * time.Second

// Env is the injectable runtime seam for detection (the internal/audio/audio.go Env
// pattern): LookPath resolves a binary on PATH; Probe runs `<path> --version` BOUNDED and
// returns its raw output. Both are injected so every PATH/version permutation is
// table-testable with no real binary.
type Env struct {
	LookPath func(string) (string, error)
	Probe    func(bin string) (string, error)
}

// DefaultEnv wires the real OS seams: exec.LookPath + a ProbeTimeout-bounded `--version`.
func DefaultEnv() Env {
	return Env{
		LookPath: exec.LookPath,
		Probe: func(bin string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "--version")
			// WaitDelay: without it, Output() blocks past the context kill whenever the
			// probed binary spawned a child that keeps the stdout pipe open - the exact
			// hang the bounded-probe spec forbids (caught by the real hung-binary test).
			cmd.WaitDelay = time.Second
			out, err := cmd.Output()
			return string(out), err
		},
	}
}

// Detection is one guest found at the desk: the registry entry, the resolved PATH binary,
// and the probed version. Unverified means the probe failed / was unparsable / is below
// the known-good floor - the guest is STILL listed (§8: degrade gracefully, never hide)
// so the picker can warn instead of lying that the desk is empty.
type Detection struct {
	Guest      Guest
	Path       string
	Version    string
	Unverified bool
}

// Detect scans the desk: for each registry guest, LookPath (a miss - including a file
// without the execute bit, which exec.LookPath already rejects - means simply absent,
// never an error), then the bounded version probe. Pure and stateless: a re-scan reflects
// the live PATH. It launches nothing, writes nothing, and bills nothing.
func Detect(env Env) []Detection {
	var out []Detection
	for _, g := range Registry() {
		path, err := env.LookPath(g.Bin)
		if err != nil || path == "" {
			continue
		}
		d := Detection{Guest: g, Path: path}
		raw, perr := "", error(nil)
		if env.Probe != nil {
			raw, perr = env.Probe(path)
		}
		v, ok := ParseVersion(g.Name, raw)
		switch {
		case perr != nil || !ok:
			d.Unverified = true // failed/garbled probe: UNVERIFIED, never hidden
		case versionBelow(v, g.KnownGood):
			d.Version, d.Unverified = v, true // below the proven floor (§8 version skew)
		default:
			d.Version = v
		}
		out = append(out, d)
	}
	return out
}

// ParseVersion extracts a semver-ish version from a guest's raw `--version` output. It is
// format-tolerant across the real shapes observed on the dev box (bare "1.17.11",
// "Hermes Agent v0.16.0 (…)", "aider 0.86.2"): the FIRST whitespace token that is a
// dotted all-digit group (optionally v-prefixed) wins. Garbage (tracebacks, empty output)
// returns ok=false. The guest name is accepted for future format pinning but the parse is
// deliberately generic - a new release changing cosmetic text must not un-detect a guest.
func ParseVersion(_ string, raw string) (string, bool) {
	for _, tok := range strings.Fields(raw) {
		tok = strings.TrimPrefix(tok, "v")
		if isDottedVersion(tok) {
			return tok, true
		}
	}
	return "", false
}

// isDottedVersion reports whether s is digits separated by at least one dot ("1.17.11").
func isDottedVersion(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// versionBelow reports v < floor by numeric dot-segment comparison (missing segments = 0).
func versionBelow(v, floor string) bool {
	if floor == "" {
		return false
	}
	a, b := strings.Split(v, "."), strings.Split(floor, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			bv, _ = strconv.Atoi(b[i])
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}
