package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
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

	// Many boxes have NO thermal zone and expose the CPU only via hwmon (AMD k10temp): read it.
	w("sys/class/hwmon/hwmon1/name", "nvme\n")
	w("sys/class/hwmon/hwmon1/temp1_input", "63850\n")
	w("sys/class/hwmon/hwmon5/name", "k10temp\n")
	w("sys/class/hwmon/hwmon5/temp1_input", "95250\n")
	r3 := edgeReadResourcesFrom(root, now)
	require.NotNil(t, r3.CPUTempC, "hwmon k10temp is the CPU when there is no thermal zone")
	require.InDelta(t, 95.25, *r3.CPUTempC, 0.001, "...and it is the CPU sensor, not the nvme one")

	// btrfs subvolumes are ONE filesystem mounted several times - one disk, not three.
	w("proc/mounts", "/dev/nvme0n1p2 / btrfs rw,subvol=/@ 0 0\n/dev/nvme0n1p2 /home btrfs rw,subvol=/@home 0 0\n/dev/nvme0n1p2 /.snapshots btrfs rw 0 0\n/dev/sda1 /mnt/train ext4 rw 0 0\n")
	edgeStatfs = func(path string) (used, total float64, ok bool) {
		switch path {
		case "/":
			return 1167, 1830, true
		case "/mnt/train":
			return 886, 960, true
		}
		return 0, 0, false
	}
	r4 := edgeReadResourcesFrom(root, now)
	require.Len(t, r4.Disks, 2, "one entry per device: the btrfs volume once, plus the ext4 disk")
	require.Equal(t, "/", r4.Disks[0].Mount)
	require.Equal(t, "/mnt/train", r4.Disks[1].Mount)
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
	power := uint32(0x10000)
	edgeSeed(t,
		store.EdgeNode{ID: "n_jetson", Name: "jetson", Kind: "host", Presence: string(edge.PresenceVerified),
			LastSeen: now.Add(-10 * time.Second).Unix(),
			Instances: []store.EdgeInstance{{Name: "agent", Resources: &store.EdgeResources{
				RAMUsedGB: 8, RAMTotalGB: 32, CPUTempC: &temp, CPULoad1: &load, UptimeS: 90000, PiPower: &power,
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
	require.Contains(t, full, "good now · under-voltage since boot", "a Pi's power is shown in words")
}

// The machine's OWN fleet row must be kept alive and carry its live household - otherwise the
// authority itself reads "offline 5d" with no health, because discovery skips self. observeSelf
// takes the same Observe path every other node takes (no self-granted VERIFIED capabilities).
func TestSelfNodeHeartbeatsIntoItsOwnFleet(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code)

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })
	_, ok := h.startFace()
	require.True(t, ok, "the face must be up so the recorded address/fingerprint are real")
	h.registerInstance(h.cfg) // the household now holds this roger, with its live resources

	// Before: the enrolled row is bare (no instances) - the state the founder saw.
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	before, known, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.True(t, known)
	require.Empty(t, before.Instances)

	now := time.Now()
	h.observeSelf(now)

	after, _, err := h.st.fleet.Get(id.NodeID)
	require.NoError(t, err)
	require.Equal(t, string(edge.PresenceVerified), after.Presence, "the machine that is running is not offline")
	require.Equal(t, now.Unix(), after.LastSeen, "last-seen is refreshed every pass")
	require.NotEmpty(t, after.Instances, "its own household is on its row")
	require.NotNil(t, after.Instances[0].Resources, "...with live resources, so health has something to show")
	for _, c := range after.Caps {
		require.NotEqual(t, string(edge.Verified), c.State, "self must not grant itself a VERIFIED capability")
	}
	// And the health view now has the machine's readings, not dashes and "offline".
	hs, err := edge.FleetHealth(h.st.fleet, now)
	require.NoError(t, err)
	var mine *edge.NodeHealth
	for i := range hs {
		if hs[i].ID == id.NodeID {
			mine = &hs[i]
		}
	}
	require.NotNil(t, mine)
	require.False(t, mine.Dark)
	require.NotNil(t, mine.Readings)
}

// features/edge/health.feature: "a throttled or undervolted Pi is surfaced, because it is the classic
// Pi failure" and "SD-card wear is reported where the Pi exposes it". The firmware word is read as it
// is (the bits mean "now" and "since boot"), and wear comes from the card's own life_time register.
func TestEdgeReadResourcesReadsThePisPowerAndCardWear(t *testing.T) {
	root := t.TempDir()
	w := func(rel, body string) {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	w("proc/uptime", "100.0 100.0\n")
	now := time.Unix(1_700_000_000, 0)

	r := edgeReadResourcesFrom(root, now)
	require.Nil(t, r.PiPower, "a machine with no Pi firmware reports no power word - never 0")
	require.Nil(t, r.SDWearPct)

	// Pi 4 (and the Pi 5's own platform path): the firmware's word, in hex.
	w("sys/devices/platform/soc/soc:firmware/get_throttled", "50005\n")
	w("sys/block/mmcblk0/device/life_time", "0x03 0x02\n")
	r = edgeReadResourcesFrom(root, now)
	require.NotNil(t, r.PiPower)
	require.Equal(t, uint32(0x50005), *r.PiPower)
	require.NotNil(t, r.SDWearPct)
	require.Equal(t, 30.0, *r.SDWearPct, "life_time 0x03 is 20-30% used: the upper bound, never the hopeful one")

	// A healthy Pi reports a real zero, which is a reading - not an absence.
	w("sys/devices/platform/soc/soc:firmware/get_throttled", "0\n")
	r = edgeReadResourcesFrom(root, now)
	require.NotNil(t, r.PiPower)
	require.Zero(t, *r.PiPower)

	// Nonsense in either file is no reading, not a guess.
	w("sys/devices/platform/soc/soc:firmware/get_throttled", "lots\n")
	w("sys/block/mmcblk0/device/life_time", "0x00 0x00\n")
	r = edgeReadResourcesFrom(root, now)
	require.Nil(t, r.PiPower)
	require.Nil(t, r.SDWearPct, "0x00 means the card does not say")
}
