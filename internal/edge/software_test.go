package edge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
)

func TestSoftwareWordsLeaveAbsentFactsOut(t *testing.T) {
	require.Equal(t, "", SoftwareWords(nil))
	require.Equal(t, "roger 6.9.0 · Debian GNU/Linux 12 · 6.1.0 · arm64 · Raspberry Pi 5",
		SoftwareWords(&Software{Roger: "6.9.0", OS: "Debian GNU/Linux 12", Kernel: "6.1.0", Arch: "arm64", Machine: "Raspberry Pi 5", Hostname: "shed"}))
	require.Equal(t, "roger 6.11.2 · amd64", SoftwareWords(&Software{Roger: "6.11.2", Arch: "amd64"}))
}

func TestVersionOrderIsNumeric(t *testing.T) {
	cases := []struct {
		a, b string
		less bool
	}{
		{"6.9.0", "6.11.2", true},
		{"6.11.2", "6.9.0", false},
		{"v6.9.0", "6.9.1", true},
		{"6.9.0", "6.9.0", false},
		{"6.9.0-rc1", "6.9.0", false},
		{"dev", "6.9.0", false},
		{"6.9.0", "dev", false},
		{"", "6.9.0", false},
		{"6.9", "6.10", true},
		{"1.2.3.4", "6.9.0", false},
	}
	for _, c := range cases {
		require.Equal(t, c.less, VersionLess(c.a, c.b), "%s < %s", c.a, c.b)
	}
}

func TestNewestRogerAndWhoIsBehind(t *testing.T) {
	desk := Instance{Name: "desk", Software: &Software{Roger: "6.11.2"}}
	shed := Instance{Name: "shed", Software: &Software{Roger: "6.9.0"}}
	odd := Instance{Name: "odd", Software: &Software{Roger: "dev"}}
	old := Instance{Name: "old"} // an older face: no software record
	nodes := []store.EdgeNode{{Name: "desk", Instances: []Instance{desk, odd}}, {Name: "shed", Instances: []Instance{shed, old}}}
	newest := NewestRoger(nodes)
	require.Equal(t, "6.11.2", newest)
	require.Equal(t, "6.11.2", Behind(shed, newest))
	require.Equal(t, "", Behind(desk, newest))
	require.Equal(t, "", Behind(odd, newest), "an unparseable version is neither newest nor behind")
	require.Equal(t, "", Behind(old, newest), "no record: not counted behind")
	require.Equal(t, "", NewestRoger([]store.EdgeNode{{Instances: []Instance{old, odd}}}), "nothing comparable: no reference")
}
