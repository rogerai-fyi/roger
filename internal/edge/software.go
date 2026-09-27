package edge

// THE INVENTORY (features/edge/inventory.feature): the software each roger is, in words, and who is
// BEHIND the newest roger on the Edge. The facts are read on the machine (cmd/rogerai reads them);
// this file only orders and words them, so every surface says the same thing.

import (
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/store"
)

// Software is the store record, named here for the surfaces.
type Software = store.EdgeSoftware

// SoftwareWords is the one line every surface draws: "roger 6.9.0 · Debian GNU/Linux 12 · 6.1.0 ·
// arm64 · Raspberry Pi 5". Absent facts are left out, never drawn as blanks.
func SoftwareWords(sw *Software) string {
	if sw == nil {
		return ""
	}
	var parts []string
	if sw.Roger != "" {
		parts = append(parts, "roger "+sw.Roger)
	}
	for _, p := range []string{sw.OS, sw.Kernel, sw.Arch, sw.Machine} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// VersionLess orders two dotted versions numerically ("6.9.0" < "6.11.2"); a leading "v" and a
// pre-release suffix are tolerated. It reports false when either cannot be parsed, so an unknown
// version is neither newest nor behind.
func VersionLess(a, b string) bool {
	x, okA := parseVersion(a)
	y, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// NewestRoger is the newest roger version any instance on the Edge reports, "" when none does.
func NewestRoger(nodes []store.EdgeNode) string {
	newest := ""
	for _, n := range nodes {
		for _, in := range n.Instances {
			if in.Software == nil || in.Software.Roger == "" {
				continue
			}
			if _, ok := parseVersion(in.Software.Roger); !ok {
				continue
			}
			if newest == "" || VersionLess(newest, in.Software.Roger) {
				newest = in.Software.Roger
			}
		}
	}
	return newest
}

// Behind is the version an instance is behind, or "" when it is current, reports no version, or its
// version cannot be compared. The word for it on every surface is "behind <newest>".
func Behind(in Instance, newest string) string {
	if in.Software == nil || in.Software.Roger == "" || newest == "" {
		return ""
	}
	if VersionLess(in.Software.Roger, newest) {
		return newest
	}
	return ""
}
