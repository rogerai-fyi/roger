//go:build !linux

package onair

import "time"

// ProcessStart can't tell a process's start time here; a live pid is believed.
func ProcessStart(int) (time.Time, bool, bool) { return time.Time{}, false, false }
