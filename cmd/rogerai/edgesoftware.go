package main

// THE INVENTORY, read on the machine (features/edge/inventory.feature): what this roger IS, put in
// its instance record beside its resources, and `roger edge inventory`, the table of every unit's
// software with who is BEHIND the newest roger on the Edge. Read, never invented: a fact the machine
// does not expose is absent.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"rogerai.fm/roger/v7/internal/buildinfo"
	"runtime"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

// envEdgeMachine names this machine's hardware model where it has no device tree to read - a
// container standing in for a Raspberry Pi (tools/edgesim).
const envEdgeMachine = "ROGERAI_EDGE_MACHINE"

// edgeReadSoftware is this process's software facts, from the live machine.
func edgeReadSoftware() *store.EdgeSoftware { return edgeReadSoftwareFrom("/", buildinfo.Version) }

// edgeReadSoftwareFrom reads the software facts under root: the OS pretty name from etc/os-release,
// the kernel release, the hardware model from the device tree (a Pi, a Jetson) or the DMI product
// (a PC), the hostname from the kernel. The roger version and the architecture are always known.
func edgeReadSoftwareFrom(root, version string) *store.EdgeSoftware {
	sw := &store.EdgeSoftware{Roger: version, Arch: runtime.GOARCH}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(strings.TrimRight(string(b), "\x00\n"))
	}
	for _, line := range strings.Split(read("etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
			// os-release quotes with either mark: Debian writes "Debian GNU/Linux 12", Gentoo
			// writes 'Gentoo Linux'. Strip both, or the quotes end up on screen.
			sw.OS = strings.Trim(v, `"'`)
		}
	}
	sw.Kernel = read("proc/sys/kernel/osrelease")
	sw.Hostname = read("proc/sys/kernel/hostname")
	if m := strings.TrimSpace(os.Getenv(envEdgeMachine)); m != "" {
		sw.Machine = m // a simulated machine (tools/edgesim) names itself; a model is the machine's own word anyway
	} else if m := read("proc/device-tree/model"); m != "" {
		sw.Machine = m
	} else if p := read("sys/class/dmi/id/product_name"); p != "" && !edgeDMIFiller(p) {
		if v := read("sys/class/dmi/id/sys_vendor"); v != "" && !edgeDMIFiller(v) && !strings.HasPrefix(strings.ToLower(p), strings.ToLower(v)) {
			p = v + " " + p
		}
		sw.Machine = p
	}
	return sw
}

// edgeDMIFiller is the text a board vendor leaves in a DMI field it never filled.
func edgeDMIFiller(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "to be filled by o.e.m.", "default string", "system product name", "system manufacturer", "none", "not specified", "unknown":
		return true
	}
	return false
}

// edgeInventoryRow is one unit's line in `roger edge inventory`, and its JSON record.
type edgeInventoryRow struct {
	Node     string `json:"node"`
	Instance string `json:"instance"`
	Roger    string `json:"roger,omitempty"`
	OS       string `json:"os,omitempty"`
	Kernel   string `json:"kernel,omitempty"`
	Arch     string `json:"arch,omitempty"`
	Machine  string `json:"machine,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	UptimeS  int64  `json:"uptime_s,omitempty"`
	Behind   string `json:"behind,omitempty"` // the newest version this unit is behind
	Presence string `json:"presence,omitempty"`
}

// edgeInventoryRows is the inventory of the fleet as read now: this machine's own household on its
// row, every member's instances as its face last reported them.
func edgeInventoryRows(st *edgeState, now time.Time) ([]edgeInventoryRow, string, error) {
	nodes, err := st.fleet.List()
	if err != nil {
		return nil, "", err
	}
	edgeAttachOwnHousehold(nodes, now)
	newest := edge.NewestRoger(nodes)
	var rows []edgeInventoryRow
	for _, n := range nodes {
		for _, in := range n.Instances {
			r := edgeInventoryRow{Node: edgeNodeLabel(n), Instance: in.Name, Behind: edge.Behind(in, newest), Presence: strings.ToLower(n.Presence)}
			if sw := in.Software; sw != nil {
				r.Roger, r.OS, r.Kernel, r.Arch, r.Machine, r.Hostname = sw.Roger, sw.OS, sw.Kernel, sw.Arch, sw.Machine, sw.Hostname
			}
			if in.Resources != nil {
				r.UptimeS = in.Resources.UptimeS
			}
			rows = append(rows, r)
		}
	}
	return rows, newest, nil
}

// cmdEdgeInventory prints the fleet's software inventory: `roger edge inventory [--json]`.
func cmdEdgeInventory(cfg config, args []string) error {
	leaf, _ := edgeLeaf("inventory")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	asJSON := argv.has("json")
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	rows, newest, err := edgeInventoryRows(st, time.Now())
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"newest": newest, "units": rows})
	}
	edgeWriteInventory(os.Stdout, rows, newest)
	return nil
}

func edgeWriteInventory(w io.Writer, rows []edgeInventoryRow, newest string) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "no roger on this Edge has reported its software yet")
		return
	}
	head := "INVENTORY"
	if newest != "" {
		head += " · newest roger " + newest
	}
	fmt.Fprintln(w, head)
	fmt.Fprintf(w, "%-24s %-8s %-24s %-12s %-7s %-22s %-8s %s\n", "unit", "roger", "os", "kernel", "arch", "machine", "up", "")
	for _, r := range rows {
		note := ""
		if r.Behind != "" {
			note = "behind " + r.Behind
		}
		up := ""
		if r.UptimeS > 0 {
			up = edge.Age(time.Duration(r.UptimeS) * time.Second)
		}
		fmt.Fprintf(w, "%-24s %-8s %-24s %-12s %-7s %-22s %-8s %s\n", elideTo(r.Node+"/"+r.Instance, 24), edgeOrDash(r.Roger), elideTo(edgeOrDash(r.OS), 24),
			elideTo(edgeOrDash(r.Kernel), 12), edgeOrDash(r.Arch), elideTo(edgeOrDash(r.Machine), 22), up, note)
	}
}

func edgeOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func elideTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
