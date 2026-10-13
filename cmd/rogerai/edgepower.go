package main

// THIS MACHINE'S POWER (features/edge/fleet_window.feature, power; ruled 2026-10-13). roger reports
// its own battery and power source with its other health facts, so the authority can warn when a Pi
// on a UPS HAT loses mains, a laptop runs low, or a sensor's long-life battery fades. A machine with
// no battery reports NOTHING: mains power is never claimed on a machine's behalf.
//
// Linux: /sys/class/power_supply, where a battery (a laptop's, or a Pi UPS HAT with a driver) is a
// "Battery" supply with capacity and status, and AC/USB adapters are "Mains"/"USB" supplies with
// online; energy saver is the ACPI platform profile "low-power". macOS: `pmset -g batt` and the
// lowpowermode line of `pmset -g`. Windows: not read yet, so nothing is reported there.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// edgeReadPower reads this machine's power under root, or nil when it has no battery to speak of.
func edgeReadPower(root string, now time.Time) *store.EdgePower {
	var p *store.EdgePower
	if runtime.GOOS == "darwin" && root == "/" {
		p = edgeDarwinPower()
	} else {
		p = edgeLinuxPower(root)
	}
	if p == nil || p.Empty() {
		return nil
	}
	p.ReadAt = now.Unix()
	return p
}

func readTrim(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// edgeLinuxPower reads /sys/class/power_supply under root.
func edgeLinuxPower(root string) *store.EdgePower {
	supplies, _ := filepath.Glob(filepath.Join(root, "sys/class/power_supply/*"))
	var bat string
	external := ""
	for _, s := range supplies {
		typ, _ := readTrim(filepath.Join(s, "type"))
		switch typ {
		case "Battery":
			// The system's own battery, not a mouse's: a peripheral reports scope "Device".
			if scope, ok := readTrim(filepath.Join(s, "scope")); ok && scope == "Device" {
				continue
			}
			if bat == "" {
				bat = s
			}
		case "Mains", "USB", "USB_C", "USB_PD":
			if on, ok := readTrim(filepath.Join(s, "online")); ok {
				if on == "1" {
					external = "1"
				} else if external == "" {
					external = "0"
				}
			}
		}
	}
	if bat == "" {
		return nil // no battery: nothing to report, and mains is not claimed
	}
	p := &store.EdgePower{}
	if v, ok := readTrim(filepath.Join(bat, "capacity")); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
			p.BatteryPct = &n
		}
	}
	status, _ := readTrim(filepath.Join(bat, "status"))
	switch status {
	case "Charging", "Full":
		t := true
		p.Charging = &t
	case "Discharging", "Not charging":
		f := false
		p.Charging = &f
	}
	switch {
	case external == "1":
		p.Source = "ac"
	case external == "0" || status == "Discharging":
		p.Source = "battery"
	}
	if prof, ok := readTrim(filepath.Join(root, "sys/firmware/acpi/platform_profile")); ok {
		low := prof == "low-power"
		p.LowPower = &low
	}
	// Time left while discharging, from energy (µWh / µW) or charge (µAh / µA).
	if status == "Discharging" {
		for _, pair := range [][2]string{{"energy_now", "power_now"}, {"charge_now", "current_now"}} {
			nowS, ok1 := readTrim(filepath.Join(bat, pair[0]))
			rateS, ok2 := readTrim(filepath.Join(bat, pair[1]))
			left, err1 := strconv.ParseFloat(nowS, 64)
			rate, err2 := strconv.ParseFloat(rateS, 64)
			if ok1 && ok2 && err1 == nil && err2 == nil && rate > 0 && left >= 0 {
				m := int(left / rate * 60)
				p.MinutesLeft = &m
				break
			}
		}
	}
	return p
}

// edgeDarwinPower asks pmset, within a second each.
func edgeDarwinPower() *store.EdgePower {
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "pmset", args...).Output()
		if err != nil {
			return ""
		}
		return string(out)
	}
	p := parsePmsetBatt(run("-g", "batt"))
	if p == nil {
		return nil
	}
	if low, ok := parsePmsetLowPower(run("-g")); ok {
		p.LowPower = &low
	}
	return p
}

var (
	pmsetPct  = regexp.MustCompile(`(\d{1,3})%;\s*([^;]+?)(?:;|\s+present|$)`)
	pmsetLeft = regexp.MustCompile(`(\d+):(\d{2}) remaining`)
	pmsetLow  = regexp.MustCompile(`(?m)^\s*lowpowermode\s+(\d)`)
)

// parsePmsetBatt reads `pmset -g batt`:
//
//	Now drawing from 'Battery Power'
//	 -InternalBattery-0 (id=1234)	87%; discharging; 4:12 remaining present: true
//
// nil when it names no internal battery (a desktop Mac).
func parsePmsetBatt(out string) *store.EdgePower {
	if !strings.Contains(out, "InternalBattery") {
		return nil
	}
	p := &store.EdgePower{}
	switch {
	case strings.Contains(out, "'AC Power'"):
		p.Source = "ac"
	case strings.Contains(out, "'Battery Power'"):
		p.Source = "battery"
	}
	if m := pmsetPct.FindStringSubmatch(out); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n <= 100 {
			p.BatteryPct = &n
		}
		state := strings.TrimSpace(m[2])
		switch {
		case strings.Contains(out, "not charging") || state == "discharging":
			f := false
			p.Charging = &f
		case state == "charging" || state == "charged" || state == "finishing charge":
			t := true
			p.Charging = &t
		}
	}
	if m := pmsetLeft.FindStringSubmatch(out); m != nil && p.Charging != nil && !*p.Charging {
		h, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		left := h*60 + mm
		p.MinutesLeft = &left
	}
	return p
}

// parsePmsetLowPower reads the lowpowermode line of `pmset -g`, when the system has one.
func parsePmsetLowPower(out string) (bool, bool) {
	m := pmsetLow.FindStringSubmatch(out)
	if m == nil {
		return false, false
	}
	return m[1] == "1", true
}
