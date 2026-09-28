package main

// Executable pins for features/edge/inventory.feature: the read from a machine, and the table.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func writeUnder(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

func TestSoftwareIsReadFromTheMachineAndNeverInvented(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "etc/os-release", "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n")
	writeUnder(t, root, "proc/sys/kernel/osrelease", "6.1.0-rpi7-rpi-2712\n")
	writeUnder(t, root, "proc/sys/kernel/hostname", "shed\n")
	writeUnder(t, root, "proc/device-tree/model", "Raspberry Pi 5 Model B Rev 1.0\x00")
	sw := edgeReadSoftwareFrom(root, "6.9.0")
	require.Equal(t, &store.EdgeSoftware{Roger: "6.9.0", OS: "Debian GNU/Linux 12 (bookworm)", Kernel: "6.1.0-rpi7-rpi-2712",
		Arch: runtime.GOARCH, Hostname: "shed", Machine: "Raspberry Pi 5 Model B Rev 1.0"}, sw)

	// os-release quotes with either mark (Gentoo uses single quotes), and neither ends up on screen.
	gentoo := t.TempDir()
	writeUnder(t, gentoo, "etc/os-release", "NAME=Gentoo\nPRETTY_NAME='Gentoo Linux'\nID=gentoo\n")
	require.Equal(t, "Gentoo Linux", edgeReadSoftwareFrom(gentoo, "6.11.2").OS)

	// A PC: no device tree, a DMI product; vendor prefixed unless the product already names it.
	pc := t.TempDir()
	writeUnder(t, pc, "sys/class/dmi/id/product_name", "ThinkStation P360\n")
	writeUnder(t, pc, "sys/class/dmi/id/sys_vendor", "LENOVO\n")
	require.Equal(t, "LENOVO ThinkStation P360", edgeReadSoftwareFrom(pc, "6.11.2").Machine)
	filler := t.TempDir()
	writeUnder(t, filler, "sys/class/dmi/id/product_name", "To Be Filled By O.E.M.\n")
	require.Equal(t, "", edgeReadSoftwareFrom(filler, "6.11.2").Machine, "a vendor's filler text is not a machine")

	// Nothing exposed: the version and the arch only, the rest absent.
	bare := edgeReadSoftwareFrom(t.TempDir(), "6.11.2")
	require.Equal(t, &store.EdgeSoftware{Roger: "6.11.2", Arch: runtime.GOARCH}, bare)
	require.Equal(t, "roger 6.11.2 · "+runtime.GOARCH, edge.SoftwareWords(bare))
}

func TestEdgeInventoryListsEveryUnitAndWhoIsBehind(t *testing.T) {
	offline(t)
	edgeSeed(t,
		store.EdgeNode{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(edge.PresenceVerified),
			Instances: []edge.Instance{{Name: "desk", Software: &store.EdgeSoftware{Roger: "6.11.2", OS: "Gentoo Linux", Kernel: "6.18.31", Arch: "amd64"},
				Resources: &store.EdgeResources{UptimeS: 3600 * 30}}}},
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "host", Presence: string(edge.PresenceVerified),
			Instances: []edge.Instance{{Name: "shed", Software: &store.EdgeSoftware{Roger: "6.9.0", OS: "Debian GNU/Linux 12", Kernel: "6.1.0", Arch: "arm64", Machine: "Raspberry Pi 5"}}}},
		store.EdgeNode{ID: "n_old", Name: "old-box", Kind: "host", Presence: string(edge.PresenceDark),
			Instances: []edge.Instance{{Name: "old-box"}}}, // an older face: no software record
	)
	out, code := edgeRun(t, "edge", "inventory")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "INVENTORY · newest roger 6.11.2")
	require.Contains(t, out, "desk/desk")
	require.Contains(t, out, "6.11.2")
	require.Contains(t, out, "Gentoo Linux")
	require.Contains(t, out, "1d")
	require.Contains(t, out, "shed/shed")
	require.Contains(t, out, "Raspberry Pi 5")
	require.Contains(t, out, "behind 6.11.2")
	require.Contains(t, out, "old-box/old-box")
	require.Equal(t, 1, strings.Count(out, "behind "), "only the unit behind carries the mark")

	out, code = edgeRun(t, "edge", "inventory", "--json")
	require.Equal(t, 0, code, out)
	var got struct {
		Newest string             `json:"newest"`
		Units  []edgeInventoryRow `json:"units"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, "6.11.2", got.Newest)
	require.Len(t, got.Units, 3)
	byName := map[string]edgeInventoryRow{}
	for _, u := range got.Units {
		byName[u.Node] = u
	}
	require.Equal(t, "6.11.2", byName["shed"].Behind)
	require.Equal(t, "", byName["desk"].Behind)
	require.Equal(t, "", byName["old-box"].Behind)

	_, code = edgeRun(t, "edge", "inventory", "extra")
	require.NotEqual(t, 0, code, "inventory takes no positional argument")
}
