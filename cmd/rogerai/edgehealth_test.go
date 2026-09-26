package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// features/edge/health.feature §1: a member reads real health facts - CPU temperature, load, memory
// used/total, uptime, each mounted disk's used/total - and a metric the platform does not expose is
// ABSENT, never zero. The reader is rooted so a test can stand up a fake /proc and /sys.
func TestEdgeReadResourcesReadsRealFactsAndOmitsWhatIsAbsent(t *testing.T) {
	root := t.TempDir()
	w := func(rel, body string) {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	w("proc/meminfo", "MemTotal:       32000000 kB\nMemAvailable:   24000000 kB\n")
	w("proc/loadavg", "1.25 0.90 0.70 2/512 4242\n")
	w("proc/uptime", "86400.50 300000.00\n")
	w("sys/class/thermal/thermal_zone0/type", "x86_pkg_temp\n")
	w("sys/class/thermal/thermal_zone0/temp", "61500\n")
	w("proc/mounts", "/dev/root / ext4 rw 0 0\nproc /proc proc rw 0 0\ntmpfs /run tmpfs rw 0 0\n")
	prev := edgeStatfs
	edgeStatfs = func(path string) (used, total float64, ok bool) {
		if path == "/" {
			return 40, 100, true
		}
		return 0, 0, false
	}
	t.Cleanup(func() { edgeStatfs = prev })

	now := time.Unix(1_700_000_000, 0)
	r := edgeReadResourcesFrom(root, now)
	require.NotNil(t, r)
	require.InDelta(t, 32000000.0/1024/1024, r.RAMTotalGB, 0.01)
	require.InDelta(t, 8000000.0/1024/1024, r.RAMUsedGB, 0.01)
	require.NotNil(t, r.CPULoad1)
	require.InDelta(t, 1.25, *r.CPULoad1, 0.001)
	require.Equal(t, int64(86400), r.UptimeS)
	require.NotNil(t, r.CPUTempC)
	require.InDelta(t, 61.5, *r.CPUTempC, 0.001)
	require.Len(t, r.Disks, 1, "only real filesystems, never proc/tmpfs pseudo-mounts")
	require.Equal(t, "/", r.Disks[0].Mount)
	require.Equal(t, 40.0, r.Disks[0].UsedGB)
	require.Equal(t, 100.0, r.Disks[0].TotalGB)
	require.Nil(t, r.SDWearPct, "SD wear is absent where the platform does not expose it - never 0")
	require.Equal(t, now.Unix(), r.ReadAt, "every value carries the moment it was taken")

	// A board with no thermal sensor: no temperature field at all, and nothing else is disturbed.
	require.NoError(t, os.RemoveAll(filepath.Join(root, "sys")))
	r2 := edgeReadResourcesFrom(root, now)
	require.NotNil(t, r2)
	require.Nil(t, r2.CPUTempC, "no sensor -> no field, never 0 degrees")
}

// features/edge/health.feature @cli: `roger edge health` lists each node with temperature, memory,
// disk, uptime and presence - a dash for a metric it does not report, never a zero - and an offline
// node marked offline with how long. `roger edge health <node>` shows every metric with its age.
func TestEdgeHealthCLI(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	temp := 61.5
	load := 1.2
	edgeSeed(t,
		store.EdgeNode{ID: "n_jetson", Name: "jetson", Kind: "host", Presence: string(edge.PresenceVerified),
			LastSeen: now.Add(-10 * time.Second).Unix(),
			Instances: []store.EdgeInstance{{Name: "agent", Resources: &store.EdgeResources{
				RAMUsedGB: 8, RAMTotalGB: 32, CPUTempC: &temp, CPULoad1: &load, UptimeS: 90000,
				Disks:  []store.EdgeDisk{{Mount: "/", UsedGB: 40, TotalGB: 100}},
				ReadAt: now.Add(-90 * time.Second).Unix()}}}},
		store.EdgeNode{ID: "n_quiet", Name: "quiet", Kind: "mobile", Presence: string(edge.PresenceVerified), LastSeen: now.Unix()},
		store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "host", Presence: string(edge.PresenceDark), LastSeen: now.Add(-2 * time.Hour).Unix()},
	)

	out, code := edgeRun(t, "edge", "health")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "jetson")
	require.Contains(t, out, "61.5", "a reported temperature shows")
	require.Contains(t, out, "8.0/32", "memory as used/total")
	require.Contains(t, out, "40%", "disk as a level of its total")
	require.Contains(t, out, "quiet")
	require.NotContains(t, out, "0.0°C", "a node that reports no temperature never shows zero degrees")
	require.Contains(t, out, "offline", "an offline node is marked offline")
	require.Contains(t, out, "2h", "...with how long")

	full, code := edgeRun(t, "edge", "health", "jetson")
	require.Equal(t, 0, code, full)
	require.Contains(t, full, "cpu temp")
	require.Contains(t, full, "61.5")
	require.Contains(t, full, "ago", "every metric shows how old the reading is")
	require.Contains(t, full, "/ ", "each mounted disk")
	require.Contains(t, full, "uptime")
}
