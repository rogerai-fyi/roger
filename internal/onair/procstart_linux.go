//go:build linux

package onair

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// ProcessStart reads a process's start time and zombie state from /proc. The kernel counts start
// in clock ticks since boot (USER_HZ, 100 on Linux), added to the boot time /proc/stat records.
func ProcessStart(pid int) (time.Time, bool, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // the command name may hold spaces and parens
	if i < 0 {
		return time.Time{}, false, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return time.Time{}, false, false
	}
	zombie := f[0] == "Z"
	ticks, err := strconv.ParseInt(f[19], 10, 64) // field 22 overall: starttime
	if err != nil {
		return time.Time{}, false, false
	}
	boot, ok := bootTime()
	if !ok {
		return time.Time{}, zombie, false
	}
	return boot.Add(time.Duration(ticks) * time.Second / 100), zombie, true
}

func bootTime() (time.Time, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			return time.Unix(n, 0), true
		}
	}
	return time.Time{}, false
}
