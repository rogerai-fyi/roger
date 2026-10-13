package main

// This machine's power (edgepower.go): read from a fake /sys and from pmset's words; a machine with no
// battery reports nothing.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fakeSupply(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "sys/class/power_supply", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for f, v := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(v+"\n"), 0o644))
	}
}

func TestALinuxBatteryIsReadAndAMainsOnlyMachineSaysNothing(t *testing.T) {
	now := time.Unix(1_760_400_000, 0)
	root := t.TempDir()
	fakeSupply(t, root, "AC", map[string]string{"type": "Mains", "online": "1"})
	require.Nil(t, edgeLinuxPower(root), "mains and no battery: nothing is claimed")

	fakeSupply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "57", "status": "Charging"})
	fakeSupply(t, root, "hidpp_battery_0", map[string]string{"type": "Battery", "scope": "Device", "capacity": "5", "status": "Discharging"})
	p := edgeReadPower(root, now)
	require.NotNil(t, p)
	require.Equal(t, 57, *p.BatteryPct, "the system battery, not the mouse's")
	require.True(t, *p.Charging)
	require.Equal(t, "ac", p.Source)
	require.Nil(t, p.LowPower, "no platform profile: not reported")
	require.Equal(t, now.Unix(), p.ReadAt)

	// A Pi on a UPS HAT that lost mains: discharging, on battery, with time left from energy/power.
	ups := t.TempDir()
	fakeSupply(t, ups, "AC", map[string]string{"type": "Mains", "online": "0"})
	fakeSupply(t, ups, "ups", map[string]string{"type": "Battery", "capacity": "81", "status": "Discharging",
		"energy_now": "20000000", "power_now": "10000000"})
	require.NoError(t, os.MkdirAll(filepath.Join(ups, "sys/firmware/acpi"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ups, "sys/firmware/acpi/platform_profile"), []byte("low-power\n"), 0o644))
	p = edgeLinuxPower(ups)
	require.Equal(t, "battery", p.Source)
	require.False(t, *p.Charging)
	require.Equal(t, 120, *p.MinutesLeft)
	require.True(t, *p.LowPower)

	require.Nil(t, edgeLinuxPower(t.TempDir()), "no power_supply at all")
}

func TestPmsetIsRead(t *testing.T) {
	p := parsePmsetBatt("Now drawing from 'Battery Power'\n -InternalBattery-0 (id=4653155)\t87%; discharging; 4:12 remaining present: true\n")
	require.Equal(t, 87, *p.BatteryPct)
	require.False(t, *p.Charging)
	require.Equal(t, "battery", p.Source)
	require.Equal(t, 252, *p.MinutesLeft)

	p = parsePmsetBatt("Now drawing from 'AC Power'\n -InternalBattery-0 (id=4653155)\t100%; charged; 0:00 remaining present: true\n")
	require.True(t, *p.Charging)
	require.Equal(t, "ac", p.Source)
	require.Nil(t, p.MinutesLeft)

	p = parsePmsetBatt("Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t80%; AC attached; not charging present: true\n")
	require.False(t, *p.Charging)

	require.Nil(t, parsePmsetBatt("Now drawing from 'AC Power'\n"), "a desktop Mac has no internal battery")

	low, ok := parsePmsetLowPower("System-wide power settings:\nCurrently in use:\n standby              1\n lowpowermode         1\n")
	require.True(t, ok)
	require.True(t, low)
	_, ok = parsePmsetLowPower("Currently in use:\n standby 1\n")
	require.False(t, ok)
}
